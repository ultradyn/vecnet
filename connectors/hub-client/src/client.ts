import { HubError } from './errors.ts';
import type { Clock, HubApi } from './ports.ts';
import type { DirectoryEntry, GroupInfo } from './wire.ts';

export function createHubClient(opts: { baseUrl: string; credential: string; fetch?: typeof fetch; timeoutMs?: number; clock?: Pick<Clock, 'timer'> }): HubApi {
  const fetcher = opts.fetch ?? fetch;
  const base = new URL(opts.baseUrl);
  const clock: Pick<Clock, 'timer'> = opts.clock ?? {
    timer(ms, fn) {
      const timer = setTimeout(fn, ms);
      return {cancel: () => clearTimeout(timer)};
    },
  };

  async function request<T>(path: string, method: string, body?: BodyInit, mediaType?: string, signal?: AbortSignal, timeoutMs = opts.timeoutMs ?? 30_000): Promise<T> {
    const deadline = new AbortController();
    const timer = clock.timer(timeoutMs, () => deadline.abort());
    const combined = signal ? AbortSignal.any([signal, deadline.signal]) : deadline.signal;
    try {
      // Header validation can quote the credential, so it shares fetch's safe boundary.
      const headers = new Headers({Authorization:`Bearer ${opts.credential}`});
      if (mediaType) headers.set('Content-Type', mediaType);
      const response = await fetcher(new URL(path, base), {method,headers,body,signal:combined,redirect:'error'});
      if (!response.ok) throw await responseError(response);
      if (response.status === 204) return undefined as T;
      const text = await response.text(); // Transport/abort failures remain status-0 errors.
      try { return JSON.parse(text) as T; }
      catch { throw new HubError(response.status,'bad_response'); }
    } catch (err) {
      if (err instanceof HubError) throw err;
      throw new HubError(0,'network_error');
    } finally { timer.cancel(); }
  }

  const post = <T>(path: string, body: unknown, signal?: AbortSignal) => request<T>(path,'POST',JSON.stringify(body),'application/json',signal);
  return {
    send: (body, signal) => post('/v1/messages',body,signal),
    ack: (cursor, signal) => post('/v1/ack',{cursor},signal),
    receipt: (body, signal) => post('/v1/receipts',body,signal),
    presence: (body, signal) => post('/v1/presence',body,signal),
    putBlob: (bytes, mediaType, signal) => request('/v1/blobs','PUT',new Uint8Array(bytes),mediaType,signal,300_000),
    async peers(signal) { return (await request<{peers:DirectoryEntry[]}>('/v1/peers','GET',undefined,undefined,signal)).peers; },
    async groups(signal) { return (await request<{groups:GroupInfo[]}>('/v1/groups','GET',undefined,undefined,signal)).groups; },
    async fetchBlob(blobUrl, {maxBytes,signal}) {
      let url: URL;
      try { url = new URL(blobUrl,base); } catch { throw new HubError(0,'foreign_origin'); }
      if (url.origin !== base.origin || url.username || url.password) throw new HubError(0,'foreign_origin');
      if (!Number.isSafeInteger(maxBytes) || maxBytes < 0) throw new HubError(0,'too_large');
      const deadline = new AbortController();
      const timer = clock.timer(opts.timeoutMs ?? 30_000, () => deadline.abort());
      let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
      try {
        const response = await fetcher(url,{redirect:'error',signal:signal ? AbortSignal.any([signal,deadline.signal]) : deadline.signal});
        const length = response.headers.get('Content-Length');
        if (length !== null && Number(length) > maxBytes) {
          try {await response.body?.cancel();} catch { /* Size refusal takes precedence over cleanup. */ }
          throw new HubError(0,'too_large');
        }
        reader = response.body?.getReader();
        const chunks: Uint8Array[] = [];
        let size = 0;
        while (reader) {
          const {done,value} = await reader.read();
          if (done) break;
          size += value.byteLength;
          if (size > maxBytes) throw new HubError(0,'too_large');
          chunks.push(value);
        }
        const result = new Uint8Array(size);
        let offset = 0;
        for (const chunk of chunks) {result.set(chunk,offset);offset+=chunk.byteLength;}
        if (!response.ok) throw await responseError(response,new TextDecoder().decode(result));
        return result;
      } catch (err) {
        if (err instanceof HubError) throw err;
        throw new HubError(0,'network_error');
      } finally {
        timer.cancel();
        if (reader) {try {await reader.cancel();} catch { /* Preserve the original safe error. */ } reader.releaseLock();}
      }
    },
  };
}

async function responseError(response: Response, boundedText?: string): Promise<HubError> {
  // Read failures are network errors; only malformed JSON becomes bad_response.
  const text = boundedText ?? await response.text();
  let code = 'bad_response';
  let details: Record<string,unknown> = {};
  try {
    const body: unknown = JSON.parse(text);
    if (body && typeof body === 'object' && 'error' in body) {
      const error = body.error;
      if (error && typeof error === 'object' && 'code' in error && typeof error.code === 'string') {
        code = error.code;
        if ('details' in error && error.details && typeof error.details === 'object' && !Array.isArray(error.details)) details = error.details as Record<string,unknown>;
      }
    }
  } catch { /* Never include a raw server response in errors. */ }
  const retry = response.headers.get('Retry-After');
  let retryAfterMs: number | undefined;
  if (retry !== null) {
    const ms = /^\d+(?:\.\d+)?$/.test(retry.trim()) ? Number(retry)*1000 : Date.parse(retry)-Date.now();
    if (Number.isFinite(ms)) retryAfterMs = Math.max(0,ms);
  }
  return new HubError(response.status,code,details,retryAfterMs);
}
