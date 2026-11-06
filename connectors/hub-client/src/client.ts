import { createHash, randomUUID } from 'node:crypto';
import { HubError } from './errors.ts';
import type { BlobRef, Clock, HubApi } from './ports.ts';
import type { DirectoryEntry, GroupInfo } from './wire.ts';

interface BlobCapabilities {
  backend?: string; upload_mode?: string; mcp_transfer_mode?: string; checksum_profile?: string; download_origins?: string[];
}
interface UploadGrant {
  blob_id: string; sha256: string; sha256_verified?: boolean; size: number; retention_class?: string; expires_at?: string | null;
  upload: { method: string; url: string; headers?: Record<string, string>; expires_at: string };
  complete_url: string;
}
interface DownloadGrant { method?: string; url?: string; headers?: Record<string, string>; expires_at?: string; object_expires_at?: string | null }

const UPLOAD_DEADLINE_MS = 300_000;
const DOWNLOAD_DEADLINE_MS = 600_000;
const BLOB_ID = /^blo_[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const PUT_HEADERS = new Set(['content-type', 'content-disposition', 'cache-control', 'if-none-match', 'x-amz-checksum-sha256', 'x-amz-meta-vecnet-checksum-profile', 'x-amz-meta-vecnet-checksum']);

type ClientClock = Pick<Clock, 'timer'> & Partial<Pick<Clock, 'now'>>;

export function createHubClient(opts: { baseUrl: string; credential: string; fetch?: typeof fetch; timeoutMs?: number; clock?: ClientClock }): HubApi {
  const fetcher = opts.fetch ?? fetch;
  const base = new URL(opts.baseUrl);
  const clock: ClientClock = opts.clock ?? {
    timer(ms, fn) {
      const timer = setTimeout(fn, ms);
      return {cancel: () => clearTimeout(timer)};
    },
  };
  const now = () => clock.now?.() ?? Date.now();
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
  // Discovery is read on every blob operation. A reconnect or config generation
  // cannot reuse a disk decision, and direct_upload_required reads it once more
  // before any second legacy PUT.
  async function discover(signal?: AbortSignal): Promise<{ caps: BlobCapabilities; mode: 'signed' | 'legacy' }> {
    try {
      const caps = await request<BlobCapabilities>('/v1/blob-capabilities','GET',undefined,undefined,signal);
      return { caps, mode: transferMode(caps) };
    } catch (err) {
      // Only an old hub's missing route is legacy disk. Any other failure stays failed.
      if (err instanceof HubError && err.status === 404) return { caps: { backend: 'disk', upload_mode: 'legacy', mcp_transfer_mode: 'base64', download_origins: [] }, mode: 'legacy' };
      throw err;
    }
  }
  async function readHubBytes(url: URL, maxBytes: number, signal?: AbortSignal): Promise<Uint8Array> {
    if (!Number.isSafeInteger(maxBytes) || maxBytes < 0) throw new HubError(0,'too_large');
    return readBounded(url, undefined, maxBytes, signal, true);
  }
  async function putDirect(caps: BlobCapabilities, grant: UploadGrant, bytes: Uint8Array, signal?: AbortSignal): Promise<void> {
    if (grant.upload?.method !== 'PUT') throw new HubError(0,'blob_unavailable');
    const headers = copyTransferHeaders(grant.upload.headers, bytes.byteLength);
    const target = directTarget(grant.upload.url, caps.download_origins, grant.upload.expires_at, UPLOAD_DEADLINE_MS, grant.expires_at);
    const deadline = new AbortController();
    const timer = clock.timer(UPLOAD_DEADLINE_MS, () => deadline.abort());
    const combined = signal ? AbortSignal.any([signal, deadline.signal]) : deadline.signal;
    try {
      const response = await fetcher(target, {method:'PUT',headers,body:Buffer.from(bytes),redirect:'error',signal:combined});
      if (!response.ok) {
        try { await response.body?.cancel(); } catch { /* The refusal is already safe. */ }
        throw new HubError(response.status,'blob_unavailable');
      }
    } catch (err) {
      if (err instanceof HubError) throw err;
      throw new HubError(0,'blob_unavailable');
    } finally { timer.cancel(); }
  }
  async function putSigned(caps: BlobCapabilities, bytes: Uint8Array, mediaType: string, signal?: AbortSignal): Promise<BlobRef> {
    const sha256 = createHash('sha256').update(bytes).digest('hex');
    const grant = await request<UploadGrant>('/v1/blobs/uploads','POST',JSON.stringify({size:bytes.byteLength,sha256,media_type:mediaType}),'application/json',signal,opts.timeoutMs ?? 30_000,{'Idempotency-Key':`blob-${randomUUID()}`});
    // Refuse the grant's destination before any further credential-bearing request.
    const path = completionPath(grant.blob_id, grant.complete_url);
    await putDirect(caps, grant, bytes, signal);
    const ref = await request<BlobRef>(path,'POST','{}','application/json',signal);
    if (!ref.sha256_verified || !ref.integrity?.verified || ref.integrity.algorithm !== 'sha256' || ref.integrity.value !== sha256) throw new HubError(409,'blob_proof_mismatch');
    return {sha256:ref.sha256,size:ref.size,blob_id:ref.blob_id,retention_class:ref.retention_class,expires_at:ref.expires_at,sha256_verified:true,integrity:ref.integrity,media_type:mediaType};
  }
  async function fetchReference(ref: {sha256:string; blob_id:string}, optsBlob: {maxBytes:number; signal?:AbortSignal}, allowRetry: boolean): Promise<Uint8Array> {
    const found = await discover(optsBlob.signal);
    const grant = await request<DownloadGrant>(`/v1/blobs/${ref.sha256}/download?blob_id=${encodeURIComponent(ref.blob_id)}`,'GET',undefined,undefined,optsBlob.signal);
    if (!grant.url || grant.method !== 'GET') throw new HubError(0,'blob_unavailable');
    if (Object.keys(grant.headers ?? {}).length) throw new HubError(0,'blob_unavailable');
    const target = directTarget(grant.url, found.caps.download_origins, grant.expires_at ?? '', DOWNLOAD_DEADLINE_MS, grant.object_expires_at);
    try { return await readBounded(target, undefined, optsBlob.maxBytes, optsBlob.signal, false); }
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
        const lower = key.toLowerCase();
        if (lower === 'content-length') continue;
        if (!hubErrorBody && !PUT_HEADERS.has(lower)) throw new HubError(0,'blob_unavailable');
        headers.set(key, value);
      }
      const response = await fetcher(url,{headers,redirect:'error',signal:signal ? AbortSignal.any([signal,deadline.signal]) : deadline.signal});
      // A provider refusal is status only. Its body must not become too_large
      // and swallow the stale-capability retry. Hub errors stay bounded below.
      if (!hubErrorBody && !response.ok) {
        try { await response.body?.cancel(); } catch { /* Keep the status, drop the provider body. */ }
        throw new HubError(response.status,'blob_unavailable');
      }
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
  function directTarget(raw: string, origins: string[] | undefined, deadline: string, ttlMs: number, objectExpiry?: string | null): URL {
    let target: URL;
    try { target = new URL(raw); } catch { throw new HubError(0,'blob_unavailable'); }
    const host = target.hostname;
    const local = host === 'localhost' || host === '127.0.0.1' || host === '::1';
    if (target.username || target.password || target.hash || (target.protocol !== 'https:' && !(target.protocol === 'http:' && local)) || !(origins ?? []).includes(target.origin)) {
      throw new HubError(0,'blob_unavailable');
    }
    const at = now();
    const end = Date.parse(deadline);
    if (!Number.isFinite(end) || end <= at || end > at + ttlMs) throw new HubError(0,'blob_unavailable');
    if (objectExpiry != null && objectExpiry !== '') {
      const objectEnd = Date.parse(objectExpiry);
      if (!Number.isFinite(objectEnd) || end > objectEnd || objectEnd <= at) throw new HubError(0,'blob_unavailable');
    }
    return target;
  }
  return {
    send: (body, signal) => post('/v1/messages',body,signal),
    ack: (cursor, signal) => post('/v1/ack',{cursor},signal),
    receipt: (body, signal) => post('/v1/receipts',body,signal),
    presence: (body, signal) => post('/v1/presence',body,signal),
    async putBlob(bytes, mediaType, signal) {
      const found = await discover(signal);
      if (found.mode !== 'signed') {
        try { return await request<BlobRef>('/v1/blobs','PUT',new Uint8Array(bytes),mediaType,signal,UPLOAD_DEADLINE_MS); }
        catch (err) {
          if (!(err instanceof HubError) || err.code !== 'direct_upload_required') throw err;
          const again = await discover(signal);
          if (again.mode !== 'signed') throw err;
          return putSigned(again.caps, bytes, mediaType, signal);
        }
      }
      return putSigned(found.caps, bytes, mediaType, signal);
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

function transferMode(caps: BlobCapabilities): 'signed' | 'legacy' {
  const signed = caps.backend === 'r2' || caps.mcp_transfer_mode === 'signed_urls' || caps.upload_mode === 'direct' || caps.checksum_profile !== undefined;
  if (signed) {
    if (caps.backend === 'r2' && caps.upload_mode === 'direct' && caps.mcp_transfer_mode === 'signed_urls' && caps.checksum_profile === 'sha256' && Array.isArray(caps.download_origins)) return 'signed';
    throw new HubError(422,'bad_response');
  }
  if (caps.backend === 'disk' && (caps.upload_mode === undefined || caps.upload_mode === 'legacy') && caps.mcp_transfer_mode === 'base64' && (caps.download_origins === undefined || Array.isArray(caps.download_origins))) return 'legacy';
  throw new HubError(422,'bad_response');
}

function completionPath(blobID: string, completeURL: string): string {
  if (!BLOB_ID.test(blobID)) throw new HubError(0,'bad_response');
  const path = `/v1/blobs/uploads/${blobID}/complete`;
  // Absolute and protocol-relative destinations are refused. The hub path is built locally.
  if (completeURL !== path) throw new HubError(0,'bad_response');
  return path;
}

function copyTransferHeaders(input: Record<string, string> | undefined, length: number): Headers {
  const headers = new Headers();
  const entries = Object.entries(input ?? {});
  const present = new Map(entries.map(([key, value]) => [key.toLowerCase(), value]));
  for (const name of ['content-length', 'if-none-match', 'x-amz-checksum-sha256', 'content-type']) {
    if (!present.has(name)) throw new HubError(0,'blob_unavailable');
  }
  if (present.get('if-none-match') !== '*' || present.get('content-length') !== String(length)) throw new HubError(0,'blob_unavailable');
  for (const [key, value] of entries) {
    const lower = key.toLowerCase();
    if (lower === 'content-length') continue;
    if (!PUT_HEADERS.has(lower)) throw new HubError(0,'blob_unavailable');
    headers.set(key, value);
  }
  return headers;
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
