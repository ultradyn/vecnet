import { createHash, randomUUID } from 'node:crypto';
import { HubError } from './errors.ts';
import type { BlobRef, Clock, HubApi } from './ports.ts';
import type { DirectoryEntry, GroupInfo } from './wire.ts';

interface BlobCapabilities { backend?: string; mcp_transfer_mode?: string; download_origins?: string[] }
interface UploadGrant {
  blob_id: string; sha256: string; sha256_verified?: boolean; size: number; retention_class?: string; expires_at?: string | null;
  upload: { method: string; url: string; headers?: Record<string, string>; expires_at: string };
  complete_url: string;
}
interface DownloadGrant { method?: string; url?: string; headers?: Record<string, string>; expires_at?: string; object_expires_at?: string | null }

export function createHubClient(opts: { baseUrl: string; credential: string; fetch?: typeof fetch; timeoutMs?: number; clock?: Pick<Clock, 'timer'> }): HubApi {
  const fetcher = opts.fetch ?? fetch;
  const base = new URL(opts.baseUrl);
  const clock: Pick<Clock, 'timer'> = opts.clock ?? {
    timer(ms, fn) {
      const timer = setTimeout(fn, ms);
      return {cancel: () => clearTimeout(timer)};
    },
  };

  let capabilities: BlobCapabilities | undefined;
  async function request<T>(path: string, method: string, body?: BodyInit, mediaType?: string, signal?: AbortSignal, timeoutMs = opts.timeoutMs ?? 30_000, extra?: Record<string, string>): Promise<T> {
    const deadline = new AbortController();
    const timer = clock.timer(timeoutMs, () => deadline.abort());
    const combined = signal ? AbortSignal.any([signal, deadline.signal]) : deadline.signal;
    try {
      // Header validation can quote the credential, so it shares fetch's safe boundary.
      const headers = new Headers({Authorization:`Bearer ${opts.credential}`});
      if (mediaType) headers.set('Content-Type', mediaType);
      for (const [key, value] of Object.entries(extra ?? {})) headers.set(key, value);
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
  async function discover(signal?: AbortSignal): Promise<BlobCapabilities> {
    if (capabilities) return capabilities;
    try { capabilities = await request<BlobCapabilities>('/v1/blob-capabilities','GET',undefined,undefined,signal); }
    catch (err) {
      if (err instanceof HubError && err.status === 404) capabilities = {backend:'disk',mcp_transfer_mode:'base64',download_origins:[]};
      else throw err;
    }
    return capabilities!;
  }
  async function readHubBytes(url: URL, maxBytes: number, signal?: AbortSignal): Promise<Uint8Array> {
    if (!Number.isSafeInteger(maxBytes) || maxBytes < 0) throw new HubError(0,'too_large');
    return readBounded(url, undefined, maxBytes, signal, true);
  }
  async function putDirect(grant: UploadGrant, bytes: Uint8Array, signal?: AbortSignal): Promise<void> {
    const headers = new Headers();
    const present = new Map(Object.entries(grant.upload?.headers ?? {}).map(([key, value]) => [key.toLowerCase(), value]));
    for (const name of ['content-length','if-none-match','x-amz-checksum-sha256','content-type']) {
      if (!present.get(name)) throw new HubError(0,'blob_unavailable');
    }
    if (present.get('if-none-match') !== '*' || present.get('content-length') !== String(bytes.byteLength)) throw new HubError(0,'blob_unavailable');
    for (const [key, value] of Object.entries(grant.upload.headers ?? {})) {
      if (key.toLowerCase() !== 'content-length') headers.set(key, value);
    }
    let target: URL;
    try { target = new URL(grant.upload.url); } catch { throw new HubError(0,'blob_unavailable'); }
    const response = await fetcher(target, {method:'PUT',headers,body:Buffer.from(bytes),redirect:'error',signal}).catch(() => { throw new HubError(0,'blob_unavailable'); });
    if (!response.ok) {
      try { await response.body?.cancel(); } catch { /* The refusal is already safe. */ }
      throw new HubError(response.status,'blob_unavailable');
    }
  }
  async function fetchReference(ref: {sha256:string; blob_id:string}, optsBlob: {maxBytes:number; signal?:AbortSignal}, allowRetry: boolean): Promise<Uint8Array> {
    const caps = await discover(optsBlob.signal);
    const grant = await request<DownloadGrant>(`/v1/blobs/${ref.sha256}/download?blob_id=${encodeURIComponent(ref.blob_id)}`,'GET',undefined,undefined,optsBlob.signal);
    if (!grant.url || grant.method !== 'GET') throw new HubError(0,'blob_unavailable');
    let target: URL;
    try { target = new URL(grant.url); } catch { throw new HubError(0,'blob_unavailable'); }
    if (target.username || target.password || !(caps.download_origins ?? []).includes(target.origin)) throw new HubError(0,'blob_unavailable');
    try { return await readBounded(target, grant.headers, optsBlob.maxBytes, optsBlob.signal, false); }
    catch (err) {
      if (allowRetry && err instanceof HubError && err.status === 403) return fetchReference(ref, optsBlob, false);
      throw err;
    }
  }
  async function readBounded(url: URL, signed: Record<string,string> | undefined, maxBytes: number, signal: AbortSignal | undefined, hubErrorBody: boolean): Promise<Uint8Array> {
    if (!Number.isSafeInteger(maxBytes) || maxBytes < 0) throw new HubError(0,'too_large');
    const deadline = new AbortController();
    const timer = clock.timer(opts.timeoutMs ?? 30_000, () => deadline.abort());
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    try {
      const headers = new Headers();
      for (const [key, value] of Object.entries(signed ?? {})) {
        if (key.toLowerCase() !== 'content-length') headers.set(key, value);
      }
      const response = await fetcher(url,{headers,redirect:'error',signal:signal ? AbortSignal.any([signal,deadline.signal]) : deadline.signal});
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
      if (!response.ok) {
        if (hubErrorBody) throw await responseError(response,new TextDecoder().decode(result));
        throw new HubError(response.status,'blob_unavailable');
      }
      return result;
    } catch (err) {
      if (err instanceof HubError) throw err;
      throw new HubError(0,'network_error');
    } finally {
      timer.cancel();
      if (reader) {try {await reader.cancel();} catch { /* Preserve the original safe error. */ } reader.releaseLock();}
    }
  }
  return {
    send: (body, signal) => post('/v1/messages',body,signal),
    ack: (cursor, signal) => post('/v1/ack',{cursor},signal),
    receipt: (body, signal) => post('/v1/receipts',body,signal),
    presence: (body, signal) => post('/v1/presence',body,signal),
    async putBlob(bytes, mediaType, signal) {
      const caps = await discover(signal);
      if (caps.mcp_transfer_mode !== 'signed_urls') return request<BlobRef>('/v1/blobs','PUT',new Uint8Array(bytes),mediaType,signal,300_000);
      const sha256 = createHash('sha256').update(bytes).digest('hex');
      const grant = await request<UploadGrant>('/v1/blobs/uploads','POST',JSON.stringify({size:bytes.byteLength,sha256,media_type:mediaType}),'application/json',signal,opts.timeoutMs ?? 30_000,{'Idempotency-Key':`blob-${randomUUID()}`});
      await putDirect(grant, bytes, signal);
      const ref = await request<BlobRef>(grant.complete_url,'POST','{}','application/json',signal);
      if (!ref.sha256_verified || !ref.integrity?.verified || ref.integrity.algorithm !== 'sha256' || ref.integrity.value !== sha256) throw new HubError(409,'blob_proof_mismatch');
      return {sha256:ref.sha256,size:ref.size,blob_id:ref.blob_id,retention_class:ref.retention_class,expires_at:ref.expires_at,sha256_verified:true,integrity:ref.integrity,media_type:mediaType};
    },
    async peers(signal) { return (await request<{peers:DirectoryEntry[]}>('/v1/peers','GET',undefined,undefined,signal)).peers; },
    async groups(signal) { return (await request<{groups:GroupInfo[]}>('/v1/groups','GET',undefined,undefined,signal)).groups; },
    async fetchBlob(locator, optsBlob) {
      if (typeof locator !== 'string') return fetchReference(locator, optsBlob, true);
      let url: URL;
      try { url = new URL(locator,base); } catch { throw new HubError(0,'foreign_origin'); }
      if (url.origin !== base.origin || url.username || url.password) throw new HubError(0,'foreign_origin');
      const named = url.searchParams.get('blob_id') || url.searchParams.get('b');
      const sha = url.pathname.match(/\/v1\/blobs\/([0-9a-f]{64})$/)?.[1];
      if (named && sha) return fetchReference({sha256:sha,blob_id:named}, optsBlob, true);
      return readHubBytes(url, optsBlob.maxBytes, optsBlob.signal);
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
