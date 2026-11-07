// R2 transfer fixtures: a fake hub (capabilities, upload grants, completion,
// download grants) and a fake S3 origin behind one fetch and one XHR, on an
// injected clock. Shapes follow internal/blobwire and internal/httpapi; the
// fake S3 checks the signed headers, the checksum, create-only and the URL
// deadline as R2 does in the Task 0 receipt. Not a test file; no network.
import { createHash } from 'node:crypto';

export const S3 = 'https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com';
export const MiB = 1 << 20;
export const sha256hex = bytes => createHash('sha256').update(bytes).digest('hex');
export const sha256b64 = bytes => createHash('sha256').update(bytes).digest('base64');
const enc = new TextEncoder();
export const bytesOf = s => (typeof s === 'string' ? enc.encode(s) : new Uint8Array(s));
export const tick = () => new Promise(r => setImmediate(r));
export async function settle(n = 20) { for (let i = 0; i < n; i++) await tick(); }
// until waits for cond on real timers (Web Crypto digests resolve off-thread),
// failing after ms rather than hanging.
export async function until(cond, ms = 3000) {
  for (const end = Date.now() + ms; !cond();) {
    if (Date.now() > end) throw new Error('condition not met within ' + ms + ' ms');
    await new Promise(r => setTimeout(r, 2));
  }
}

const CLASS_SECONDS = { '1h': 3600, '6h': 21600, '12h': 43200, '1d': 86400, '7d': 604800, '30d': 2592000 };
const json = (status, body, headers = {}) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
const hubError = (status, code, details = {}, headers = {}) => json(status, { error: { code, message: code.replace(/_/g, ' '), details } }, headers);
const iso = ms => new Date(ms).toISOString();
let ids = 0;
const blobId = () => 'blo_0' + String(++ids).padStart(25, '0');

// fakeR2: backend 'r2' | 'disk' | 'old' (an old hub without capabilities).
export function fakeR2({ backend = 'r2', retention = '1d', start = Date.parse('2026-11-06T10:00:00.000Z') } = {}) {
  const f = {
    clock: start, backend, retention, calls: [], grants: new Map(), byKey: new Map(), objects: new Map(), refs: new Map(),
    // Download behaviour per blob id: 'expired' | 'purged' | 'unavailable' | { rate: seconds } | { header: seconds }.
    download: new Map(),
    // Hooks for the next S3 PUT: 'network' (refused before any byte lands),
    // 'lost' (stored, then the reply is lost), 'cors', 'redirect', 'hold', a status.
    nextPut: [], nextGet: [], held: [], storage: null, capsStatus: 0, grantError: null, completeError: null, completeUnverified: false,
    urls: [],
  };
  f.now = () => f.clock;
  f.advance = ms => { f.clock += ms; };

  function record(c) { f.calls.push(c); return c; }
  const hubCalls = () => f.calls.filter(c => c.to === 'hub');
  f.hubCalls = (method, prefix) => hubCalls().filter(c => (!method || c.method === method) && (!prefix || c.path.startsWith(prefix)));
  f.s3Calls = method => f.calls.filter(c => c.to === 's3' && (!method || c.method === method));

  function grantFor(id) {
    const g = f.grants.get(id);
    const verified = f.objects.has(g.key) && g.complete;
    return { blob_id: id, sha256: g.sha256, sha256_verified: verified, integrity: { algorithm: 'sha256', value: g.sha256, verified }, size: g.size, retention_class: g.cls, expires_at: g.expires };
  }

  function createGrant(headers, body) {
    const key = headers['Idempotency-Key'];
    if (f.grantError) { const e = f.grantError; f.grantError = null; return e(); }
    const digest = JSON.stringify(body);
    const prior = key && f.byKey.get(key);
    if (prior) {
      if (prior.digest !== digest) return hubError(409, 'idempotency_conflict');
      const g = f.grants.get(prior.id);
      if (f.clock >= g.uploadDeadline) return hubError(410, 'upload_grant_expired');
      return issue(prior.id, 201);
    }
    if (f.storage && f.storage.used + body.size > f.storage.limit) return hubError(429, 'blob_storage_quota_exceeded', { retry_after_seconds: 0 });
    const id = blobId(), cls = f.retention === 'off' ? 'keep' : f.retention;
    const expires = cls === 'keep' ? null : iso(f.clock + CLASS_SECONDS[cls] * 1000);
    const prefix = cls === 'keep' ? 'keep/ns/' : `exp/${cls}/ns/`;
    f.grants.set(id, { id, sha256: body.sha256, size: body.size, media: body.media_type, cls, expires, key: 'attachments/v1/' + prefix + id, uploadDeadline: f.clock + 300000, complete: false });
    if (key) f.byKey.set(key, { id, digest });
    return issue(id, 201);
  }
  function issue(id, status) {
    const g = f.grants.get(id), deadline = Math.min(g.uploadDeadline, f.clock + 300000);
    const b64 = Buffer.from(g.sha256, 'hex').toString('base64');
    const url = `${S3}/${g.key}?X-Amz-Expires=300&X-Amz-Signature=secret-put-${f.urls.length}&exp=${deadline}`;
    f.urls.push(url);
    return json(status, {
      ...grantFor(id), sha256_verified: false, integrity: { algorithm: 'sha256', value: g.sha256, verified: false },
      upload: { method: 'PUT', url, expires_at: iso(deadline), headers: { 'Content-Length': String(g.size), 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment', 'Cache-Control': 'private, no-store', 'If-None-Match': '*', 'x-amz-checksum-sha256': b64, 'x-amz-meta-vecnet-checksum-profile': 'sha256', 'x-amz-meta-vecnet-checksum': b64 } },
      complete_url: '/v1/blobs/uploads/' + id + '/complete',
    }, { 'Cache-Control': 'no-store' });
  }
  function complete(id) {
    if (f.completeError) { const e = f.completeError; f.completeError = null; return e(); }
    const g = f.grants.get(id);
    if (!g) return hubError(422, 'unknown_blob');
    if (!f.objects.has(g.key)) return hubError(409, 'upload_incomplete');
    g.complete = !f.completeUnverified;
    return json(200, grantFor(id));
  }
  function downloadGrant(sha, id) {
    const g = f.grants.get(id) || f.refs.get(id);
    if (!g || g.sha256 !== sha) return hubError(422, 'unknown_blob');
    const mode = f.download.get(id);
    if (mode === 'expired' || (g.expires && f.clock >= Date.parse(g.expires))) return hubError(410, 'blob_expired');
    if (mode === 'purged') return hubError(410, 'blob_purged');
    if (mode === 'unavailable') return hubError(503, 'blob_unavailable');
    if (mode && mode.rate) return hubError(429, 'blob_url_rate_limited', { retry_after_seconds: mode.rate }, { 'Retry-After': String(mode.rate) });
    if (mode && mode.header) return hubError(429, 'blob_url_rate_limited', {}, { 'Retry-After': String(mode.header) });
    const ttl = g.expires ? Math.min(600000, Date.parse(g.expires) - f.clock) : 600000;
    let deadline = f.clock + Math.floor(ttl / 1000) * 1000;
    const tamper = f.tamper && f.tamper(g);
    if (tamper && tamper.deadline) deadline = tamper.deadline(f.clock);
    let url = `${S3}/${g.key}?X-Amz-Expires=${Math.floor(ttl / 1000)}&X-Amz-Signature=secret-get-${f.urls.length}&exp=${deadline}`;
    if (tamper && tamper.url) url = tamper.url;
    f.urls.push(url);
    const verified = tamper && 'verified' in tamper ? tamper.verified : g.complete !== false;
    return json(200, {
      method: 'GET', url, expires_at: iso(deadline), object_expires_at: tamper && 'objectExpires' in tamper ? tamper.objectExpires : g.expires,
      blob_id: id, sha256: g.sha256, sha256_verified: verified, integrity: { algorithm: 'sha256', value: g.sha256, verified }, size: tamper && tamper.size !== undefined ? tamper.size : g.size, media_type: g.media || 'application/octet-stream', retention_class: g.cls,
    }, { 'Cache-Control': 'no-store' });
  }

  // seed adds a completed object the operator did not upload here.
  f.seed = (bytes, { cls = '1d', id = blobId(), media = 'text/plain' } = {}) => {
    const b = bytesOf(bytes), sha = sha256hex(b);
    const expires = cls === 'keep' ? null : iso(f.clock + CLASS_SECONDS[cls] * 1000);
    const key = 'attachments/v1/' + (cls === 'keep' ? 'keep/ns/' : `exp/${cls}/ns/`) + id;
    f.grants.set(id, { id, sha256: sha, size: b.length, media, cls, expires, key, uploadDeadline: 0, complete: true });
    f.objects.set(key, b);
    return { blob_id: id, sha256: sha, size: b.length, retention_class: cls, expires_at: expires };
  };

  async function hub(path, init) {
    const headers = init.headers || {};
    const c = record({ to: 'hub', method: init.method || 'GET', path, headers: { ...headers }, credentials: init.credentials, body: init.body });
    if (headers.Authorization !== 'Bearer vn_op') return hubError(401, 'unauthenticated');
    const u = new URL(path, 'http://hub.test');
    const body = init.body ? JSON.parse(init.body) : undefined;
    c.json = body;
    if (u.pathname === '/v1/blob-capabilities') {
      if (f.capsStatus) return hubError(f.capsStatus, f.capsStatus === 404 ? 'not_found' : 'internal');
      if (f.backend === 'old') return hubError(404, 'not_found');
      const caps = { backend: f.backend, upload_mode: f.backend === 'r2' ? 'direct' : 'legacy', max_size: 25 * MiB, daily_quota: 500 * MiB, mcp_transfer_mode: f.backend === 'r2' ? 'signed_urls' : 'base64', mcp_remote_bytes: false, download_origins: [S3] };
      if (f.backend === 'r2') Object.assign(caps, { checksum_profile: 'sha256', retention: { value: f.retention, source: 'default', revision: 1, allowed_values: ['1h', '6h', '12h', '1d', '7d', '30d', 'off'], seconds: f.retention === 'off' ? null : CLASS_SECONDS[f.retention] } });
      if (f.capsTamper) f.capsTamper(caps);
      return json(200, caps);
    }
    if (u.pathname === '/v1/blobs/uploads' && init.method === 'POST') return createGrant(headers, body);
    let m = /^\/v1\/blobs\/uploads\/([^/]+)\/complete$/.exec(u.pathname);
    if (m && init.method === 'POST') return complete(decodeURIComponent(m[1]));
    m = /^\/v1\/blobs\/([^/]+)\/download$/.exec(u.pathname);
    if (m) return downloadGrant(decodeURIComponent(m[1]), u.searchParams.get('blob_id'));
    m = /^\/v1\/blobs\/([0-9a-f]{64})$/.exec(u.pathname);
    if (m) {
      const legacy = f.legacy && f.legacy.get(m[1]);
      if (!legacy) return hubError(404, 'not_found');
      return new Response(legacy.bytes, { status: 200, headers: { 'Content-Type': legacy.type || 'application/octet-stream' } });
    }
    return hubError(404, 'not_found');
  }

  async function s3Get(url, init) {
    const c = record({ to: 's3', method: init.method || 'GET', url, headers: { ...(init.headers || {}) }, credentials: init.credentials, redirect: init.redirect, referrerPolicy: init.referrerPolicy, mode: init.mode });
    const hook = f.nextGet.shift();
    if (hook === 'cors') throw new TypeError('Failed to fetch');
    if (hook === 'redirect') throw new TypeError('Failed to fetch (redirect)');
    if (typeof hook === 'number') return new Response('<Error><Code>X</Code></Error>', { status: hook });
    const u = new URL(url);
    if (Number(u.searchParams.get('exp')) <= f.clock) return new Response('<Error><Code>AccessDenied</Code><Message>Request has expired</Message></Error>', { status: 403 });
    const key = decodeURIComponent(u.pathname.slice(1));
    const b = f.objects.get(key);
    if (!b) return new Response('<Error><Code>NoSuchKey</Code></Error>', { status: 404 });
    if (hook && hook.body) return new Response(hook.body, { status: 200 });
    c.served = b.length;
    return new Response(b, { status: 200, headers: { 'Content-Type': 'application/octet-stream', 'Content-Length': String(b.length) } });
  }

  f.fetch = async (input, init = {}) => {
    const url = String(input);
    if (init.signal && init.signal.aborted) throw new DOMException('aborted', 'AbortError');
    if (url.startsWith(S3) || /^https?:\/\//.test(url)) return s3Get(url, init);
    return hub(url, init);
  };

  // FakeXHR: hub legacy PUTs answer from f.legacyPut; S3 PUTs run the fake
  // provider: signed headers, checksum (400 BadDigest), create-only (412).
  class FakeXHR {
    constructor() { this.headers = {}; this.upload = {}; this.status = 0; this.statusText = ''; this.responseText = ''; this.responseURL = ''; this.aborted = false; this.withCredentials = undefined; }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(k, v) { if (/^content-length$/i.test(k)) throw new Error('Refused to set unsafe header "Content-Length"'); this.headers[k] = v; }
    abort() { if (this.done) return; this.aborted = true; this.done = true; if (this.onabort) this.onabort(); }
    finish(status, text = '', url = this.url) { if (this.done) return; this.done = true; this.status = status; this.responseText = text; this.responseURL = url; this.onload(); }
    fail() { if (this.done) return; this.done = true; this.onerror(); }
    async send(body) {
      const c = record({ to: this.url.startsWith(S3) ? 's3' : 'hub', via: 'xhr', method: this.method, url: this.url, path: this.url, headers: { ...this.headers }, withCredentials: this.withCredentials, body });
      c.xhr = this;
      if (!this.url.startsWith(S3)) { if (f.legacyPut) f.legacyPut(this, body); return; }
      const bytes = new Uint8Array(await body.arrayBuffer());
      c.bytes = bytes;
      if (this.done) return;
      const hook = f.nextPut.shift();
      if (hook === 'hold') { f.held.push(() => this.store(bytes)); return; }
      if (hook === 'network' || hook === 'cors') { this.fail(); return; }
      if (hook === 'redirect') { this.finish(200, '', S3 + '/elsewhere'); return; }
      if (typeof hook === 'number') { this.finish(hook, '<Error><Code>X</Code></Error>'); return; }
      if (this.upload.onprogress) {
        this.upload.onprogress({ loaded: Math.floor(bytes.length / 2), total: bytes.length, lengthComputable: true });
        this.upload.onprogress({ loaded: bytes.length, total: bytes.length, lengthComputable: true });
      }
      const status = this.store(bytes, true);
      if (hook === 'lost') { this.done = true; this.onerror(); return; }
      if (status) this.finish(status, status === 200 ? '' : '<Error/>');
    }
    store(bytes, quiet) {
      const u = new URL(this.url), key = decodeURIComponent(u.pathname.slice(1));
      const h = Object.fromEntries(Object.entries(this.headers).map(([k, v]) => [k.toLowerCase(), v]));
      let status = 200;
      if (Number(u.searchParams.get('exp')) <= f.clock) status = 403;
      else if (h['x-amz-checksum-sha256'] !== sha256b64(bytes) || h['content-type'] !== 'application/octet-stream' || h['if-none-match'] !== '*') status = 400;
      else if (f.objects.has(key)) status = 412;
      else f.objects.set(key, bytes);
      if (!quiet) this.finish(status);
      return status;
    }
  }
  f.XHR = FakeXHR;
  f.release = () => { for (const go of f.held.splice(0)) go(); };
  return f;
}

// fileOf: a File with exact bytes.
export const fileOf = (content, name = 'report.txt', type = 'text/plain') => new File([bytesOf(content)], name, { type });
