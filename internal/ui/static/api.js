// The operator API client: one method per hub route. The secret travels only
// in the Authorization header, never in a URL (PROTOCOL §2).
//
// R2 attachments (plan 2026-11-05-r2-blobs Task 7) move their bytes directly
// between this page and the storage provider: the hub hands out a short
// signed URL for one PUT or one GET, and the page sends the provider no
// bearer, no cookies and no referrer, refuses redirects, and holds the URL
// only for the request it is making. Messages and models keep references
// (blob_id, sha256, size, class, deadline), never URLs.
import { mediaTypeOf } from './attach-model.js';
import { hashFile, toHex } from './blob-hash.js';

export class APIError extends Error {
  // status 0 means the hub was never reached.
  constructor(status, code, message, details) {
    super(message);
    this.name = 'APIError';
    this.status = status;
    this.code = code;
    this.details = details || {};
  }
}

function query(params) {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') q.set(key, value);
  }
  const text = q.toString();
  return text ? '?' + text : '';
}

const seg = encodeURIComponent;
const UNREADABLE = 'unreadable reply from the hub';

function transportError(e, message) {
  if (e && e.name === 'AbortError') return new APIError(0, 'aborted', 'request aborted');
  return new APIError(0, 'network', message);
}

// ---------- direct transfer rules ----------

export const MAX_BLOB = 25 << 20;
const PUT_TTL = 300, GET_TTL = 600; // seconds: the hub's PUT and GET capability caps
// A hub truncates deadlines to whole seconds or milliseconds and clocks
// drift a little; past this, a capability outlives its cap and is refused.
const SKEW_MS = 2000;
const SHA = /^[0-9a-f]{64}$/;
const BLOB_ID = /^blo_[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const CLASSES = ['1h', '6h', '12h', '1d', '7d', '30d', 'keep'];
// The exact headers a hub signs into an upload grant, lower-cased. The
// browser derives Content-Length from the fixed body; the page never sets it.
const SIGNED = ['content-length', 'content-type', 'content-disposition', 'cache-control', 'if-none-match', 'x-amz-checksum-sha256', 'x-amz-meta-vecnet-checksum-profile', 'x-amz-meta-vecnet-checksum'];

const aborted = () => new APIError(0, 'aborted', 'request aborted');
const untrusted = what => new APIError(0, 'bad_response', 'the hub sent ' + what + ' this page does not trust');
const unreachable = () => new APIError(0, 'storage_unreachable', 'storage could not be reached, or refused this page (CORS)');
const capExpired = status => new APIError(status, 'blob_capability_expired', 'the transfer link expired before storage accepted it');
const mismatch = () => new APIError(0, 'blob_checksum_mismatch', 'the bytes do not match the verified SHA-256');

// verifiedRef: the hub recorded provider verification for exactly this digest.
export function verifiedRef(sha, r) {
  const i = r && r.integrity;
  return !!r && r.sha256_verified === true && !!i && i.algorithm === 'sha256' && i.verified === true && i.value === sha;
}

// origin: an https origin, or http on loopback (fixture services only).
function originOk(u) {
  if (u.username || u.password || u.hash) return false;
  // WHATWG URL accepts '*' in a host; a wildcard is never an exact origin.
  if (u.hostname.includes('*')) return false;
  if (u.protocol === 'https:') return true;
  return u.protocol === 'http:' && ['127.0.0.1', 'localhost', '[::1]'].includes(u.hostname);
}

// normalizeCaps checks GET /v1/blob-capabilities. download_origins are exact
// origins; anything else (a path, a wildcard) is dropped, so it can match no URL.
function normalizeCaps(c) {
  if (!c || (c.backend !== 'disk' && c.backend !== 'r2')) throw untrusted('blob capabilities');
  if (c.backend === 'r2' && c.checksum_profile !== 'sha256') throw untrusted('a checksum profile');
  const origins = (Array.isArray(c.download_origins) ? c.download_origins : []).filter(o => {
    if (typeof o !== 'string') return false;
    try { const u = new URL(o); return originOk(u) && u.origin === o; } catch { return false; }
  });
  const max = Number.isSafeInteger(c.max_size) && c.max_size > 0 && c.max_size <= MAX_BLOB ? c.max_size : MAX_BLOB;
  return { ...c, max_size: max, download_origins: origins };
}

// capability checks one signed URL from a grant against the authenticated
// capabilities and the clock: { url } to use now, { expired: true } when its
// own deadline has passed (worth one fresh grant), or throws bad_response.
function capability(raw, deadline, ttl, origins, objectExpires, nowMs) {
  let u;
  try { u = new URL(raw); } catch { throw untrusted('a transfer link'); }
  if (!originOk(u) || !origins.includes(u.origin)) throw untrusted('a transfer link');
  const end = Date.parse(deadline);
  if (typeof deadline !== 'string' || !Number.isFinite(end)) throw untrusted('a transfer deadline');
  if (end > nowMs + ttl * 1000 + SKEW_MS) throw untrusted('a transfer link that outlives its cap');
  if (objectExpires !== null && objectExpires !== undefined) {
    const at = Date.parse(objectExpires);
    if (!Number.isFinite(at) || end > at || at <= nowMs) throw untrusted('a transfer link past the file\'s deadline');
  }
  if (end <= nowMs) return { expired: true };
  return { url: u.href };
}

// refFields: the stable reference a completed upload resolves to.
function refFields(r) {
  if (!CLASSES.includes(r.retention_class)) throw untrusted('a retention class');
  const keep = r.retention_class === 'keep';
  if (keep ? r.expires_at !== null : typeof r.expires_at !== 'string' || !Number.isFinite(Date.parse(r.expires_at))) throw untrusted('a file deadline');
  return {
    blob_id: r.blob_id, sha256: r.sha256, sha256_verified: true,
    integrity: { algorithm: 'sha256', value: r.sha256, verified: true },
    size: r.size, retention_class: r.retention_class, expires_at: keep ? null : r.expires_at,
  };
}

function newKey() {
  const b = globalThis.crypto.getRandomValues(new Uint8Array(16));
  return 'ui-blob-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

// readBounded reads at most max bytes; null when the body runs longer.
async function readBounded(r, max) {
  if (!r.body || typeof r.body.getReader !== 'function') {
    const b = new Uint8Array(await r.arrayBuffer());
    return b.length > max ? null : b;
  }
  const reader = r.body.getReader(), parts = [];
  let n = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.length;
    if (n > max) { reader.cancel().catch(() => {}); return null; }
    parts.push(value);
  }
  const out = new Uint8Array(n);
  let at = 0;
  for (const p of parts) { out.set(p, at); at += p.length; }
  return out;
}

// onUnauthorized fires at most once per client: every request parked when a
// secret is rotated answers 401, and one sign-out is enough. XHR is only for
// uploads, because fetch reports no upload progress.
export function createApi({ secret, fetch = globalThis.fetch, onUnauthorized = () => {}, XHR = globalThis.XMLHttpRequest, now = Date.now }) {
  let unauthorized = false;

  // httpError turns a non-2xx reply (j is its parsed body or null) into an
  // APIError, for fetch requests and uploads alike. A Retry-After header
  // fills in details.retry_after_seconds when the body left it out.
  function httpError(status, statusText, j, retryAfter) {
    const e = (j && j.error) || {};
    const details = e.details && typeof e.details === 'object' ? { ...e.details } : {};
    if (!(Number(details.retry_after_seconds) > 0) && /^\d+$/.test(String(retryAfter || '').trim()) && Number(retryAfter) > 0) details.retry_after_seconds = Number(retryAfter);
    const err = new APIError(status, e.code || 'http_' + status, e.message || statusText, details);
    if (status === 401 && !unauthorized) { unauthorized = true; onUnauthorized(err); }
    return err;
  }

  // meta: resolve to { status, etag, body } so a caller can use the ETag; a
  // 304 (If-None-Match matched) resolves to { status: 304, etag, body: null }.
  async function request(method, path, { body, headers, signal, read = 'json', meta = false } = {}) {
    const hd = { Authorization: 'Bearer ' + secret, Accept: 'application/json', ...headers };
    if (body !== undefined) hd['Content-Type'] = 'application/json';
    let r;
    try {
      r = await fetch(path, {
        method, headers: hd, signal, cache: 'no-store', credentials: 'omit',
        body: body === undefined ? undefined : JSON.stringify(body),
      });
    } catch (e) {
      throw transportError(e, 'hub unreachable');
    }
    const etag = (meta && r.headers && r.headers.get('ETag')) || '';
    if (meta && r.status === 304) return { status: 304, etag, body: null };
    // null means 204 and nothing else: the pump reads it as "peer disabled".
    if (r.status === 204) return null;
    if (r.ok && read === 'blob') {
      try { return await r.blob(); } catch (e) { throw transportError(e, UNREADABLE); }
    }
    let j = null;
    try {
      j = await r.json();
    } catch (e) {
      // A proxy's error page or no body: the status below still says what happened.
      if (e && e.name === 'AbortError') throw transportError(e);
    }
    if (r.ok) {
      // The reply was lost on the way, as a dropped connection is.
      if (j === null) throw new APIError(0, 'network', UNREADABLE);
      return meta ? { status: r.status, etag, body: j } : j;
    }
    throw httpError(r.status, r.statusText, j, r.headers && r.headers.get('Retry-After'));
  }

  // legacyUpload PUTs file to the hub as a blob that expires after a day
  // unless a message references it (PROTOCOL §2.7): disk hubs and hubs too
  // old to serve capabilities. The File is the body, so the browser sends its
  // exact Content-Length. onProgress(loaded, total) follows the upload.
  function legacyUpload(file, { onProgress, signal } = {}) {
    return new Promise((resolve, reject) => {
      if (signal && signal.aborted) { reject(aborted()); return; }
      const x = new XHR();
      const stop = () => x.abort();
      const settle = (fn, v) => { if (signal) signal.removeEventListener('abort', stop); fn(v); };
      x.open('PUT', '/v1/blobs?ttl=86400');
      x.setRequestHeader('Authorization', 'Bearer ' + secret);
      x.setRequestHeader('Accept', 'application/json');
      x.setRequestHeader('Content-Type', mediaTypeOf(file));
      if (onProgress && x.upload) x.upload.onprogress = e => onProgress(e.loaded, e.lengthComputable ? e.total : file.size);
      x.onabort = () => settle(reject, aborted());
      x.onerror = x.ontimeout = () => settle(reject, new APIError(0, 'network', 'hub unreachable'));
      x.onload = () => {
        let j = null;
        try { j = JSON.parse(x.responseText); } catch { /* the status below still says what happened */ }
        if (x.status < 200 || x.status > 299) { settle(reject, httpError(x.status, x.statusText, j)); return; }
        if (!j || typeof j.sha256 !== 'string') { settle(reject, new APIError(0, 'network', UNREADABLE)); return; }
        settle(resolve, { sha256: j.sha256, size: j.size });
      };
      if (signal) signal.addEventListener('abort', stop, { once: true });
      x.send(file);
    });
  }

  // capabilities is GET /v1/blob-capabilities, asked afresh for every upload
  // and download so a backend or retention change is seen at once. Only an
  // old hub's 404 means legacy disk; any other failure is an error, never a
  // fallback to a hub PUT.
  async function capabilities({ signal } = {}) {
    try {
      return normalizeCaps(await get('/v1/blob-capabilities', { signal }));
    } catch (e) {
      if (e instanceof APIError && e.status === 404 && e.code !== 'bad_response') return { backend: 'disk', legacy: true, max_size: MAX_BLOB, download_origins: [] };
      throw e;
    }
  }

  // putDirect sends body to a signed provider URL: no bearer, no cookies, the
  // signed headers only. XHR follows redirects on its own, so a final URL
  // that is not the signed one is refused. 412 means a write through this
  // same grant already landed (create-only), which completion then checks.
  function putDirect(url, headers, body, { signal, onProgress }) {
    return new Promise((resolve, reject) => {
      if (signal && signal.aborted) { reject(aborted()); return; }
      const x = new XHR();
      const stop = () => x.abort();
      const settle = (fn, v) => { if (signal) signal.removeEventListener('abort', stop); fn(v); };
      x.open('PUT', url);
      x.withCredentials = false;
      for (const [k, v] of Object.entries(headers)) if (k.toLowerCase() !== 'content-length') x.setRequestHeader(k, v);
      if (onProgress && x.upload) x.upload.onprogress = e => onProgress(e.loaded, e.lengthComputable ? e.total : body.size);
      x.onabort = () => settle(reject, aborted());
      x.onerror = x.ontimeout = () => settle(reject, unreachable());
      x.onload = () => {
        let final = url;
        try { final = x.responseURL ? new URL(x.responseURL).href : url; } catch { final = ''; }
        if (final !== url) { settle(reject, new APIError(0, 'storage_redirect', 'storage redirected the upload, so it was refused')); return; }
        const st = x.status;
        if ((st >= 200 && st < 300) || st === 412) settle(resolve, st);
        // The body is the hashed bytes themselves, so a 400 is a refused
        // request (headers, signature), not changed bytes: keep the grant.
        else if (st === 400) settle(reject, new APIError(400, 'storage_refused', 'storage refused the signed upload request'));
        else if (st === 403) settle(reject, capExpired(403));
        else settle(reject, new APIError(st, 'blob_unavailable', 'storage refused the upload'));
      };
      if (signal) signal.addEventListener('abort', stop, { once: true });
      x.send(body);
    });
  }

  // directUpload: hash, ask for a grant under the idempotency key, PUT the
  // hashed bytes themselves, then ask the hub to verify the stored object.
  // onPhase names each step: hashing, uploading, verifying.
  async function directUpload(file, caps, { onProgress, onPhase, signal, idempotencyKey }) {
    const phase = p => { if (onPhase) onPhase(p); };
    phase('hashing');
    let h;
    try {
      h = await hashFile(file, { signal, onProgress: onProgress && ((loaded, total) => onProgress(loaded, total, 'hashing')) });
    } catch (e) {
      if (e && e.name === 'AbortError') throw aborted();
      if (e && e.name === 'NotSupportedError') throw new APIError(0, 'hash_unavailable', e.message);
      throw new APIError(0, 'blob_read_failed', 'the file could not be read; it may have changed since it was picked');
    }
    const size = h.bytes.length;
    if (size > caps.max_size) throw new APIError(413, 'blob_too_large', 'blob exceeds the per-blob cap', { limit: caps.max_size });
    if (!size) throw new APIError(422, 'invalid_value', 'an empty file cannot be uploaded');
    // No transfer origin means no grant could be trusted: refuse before one
    // is reserved against the daily quota.
    if (!caps.download_origins.length) throw untrusted('blob capabilities without a transfer origin');
    const g = await post('/v1/blobs/uploads', { body: { size, sha256: h.hex, media_type: mediaTypeOf(file) }, headers: { 'Idempotency-Key': idempotencyKey || newKey() }, signal });
    if (!g || !BLOB_ID.test(g.blob_id) || g.sha256 !== h.hex || g.size !== size || !g.upload || g.upload.method !== 'PUT') throw untrusted('an upload grant');
    refFields({ ...g }); // class and deadline are well formed
    const cap = capability(g.upload.url, g.upload.expires_at, PUT_TTL, caps.download_origins, g.expires_at, now());
    if (cap.expired) throw capExpired(0);
    const want = {
      'content-length': String(size), 'content-type': 'application/octet-stream', 'content-disposition': 'attachment', 'cache-control': 'private, no-store',
      'if-none-match': '*', 'x-amz-checksum-sha256': h.base64, 'x-amz-meta-vecnet-checksum-profile': 'sha256', 'x-amz-meta-vecnet-checksum': h.base64,
    };
    const headers = {}, seen = new Set();
    for (const [k, v] of Object.entries(g.upload.headers || {})) {
      const key = k.toLowerCase();
      if (!SIGNED.includes(key) || seen.has(key) || v !== want[key]) throw untrusted('upload headers');
      seen.add(key);
      headers[k] = v;
    }
    if (seen.size !== SIGNED.length) throw untrusted('upload headers');
    phase('uploading');
    await putDirect(cap.url, headers, new Blob([h.bytes]), { signal, onProgress: onProgress && ((loaded, total) => onProgress(loaded, total, 'uploading')) });
    phase('verifying');
    const r = await post('/v1/blobs/uploads/' + seg(g.blob_id) + '/complete', { body: {}, signal });
    if (!r || r.blob_id !== g.blob_id || r.sha256 !== h.hex || r.size !== size || !verifiedRef(h.hex, r)) throw untrusted('an unverified completion');
    return refFields(r);
  }

  // upload resolves the reference a send carries: the hub's { sha256, size }
  // on a disk hub, the verified R2 Ref on a direct one. idempotencyKey keeps
  // one grant across retries (an ambiguous PUT is sent again, never twice
  // reserved). onProgress(loaded, total, phase) and onPhase(phase) follow it.
  async function upload(file, { onProgress, onPhase, signal, idempotencyKey } = {}) {
    if (signal && signal.aborted) throw aborted();
    const caps = await capabilities({ signal });
    if (caps.backend !== 'r2') return legacyUpload(file, { onProgress, signal });
    return directUpload(file, caps, { onProgress, onPhase, signal, idempotencyKey });
  }

  // download resolves an attachment's bytes as an application/octet-stream
  // Blob, never its declared type: the page saves it and never renders it.
  // A reference with a blob_id is exchanged for a fresh grant on every call;
  // the URL lives only in this function. A grant whose own deadline already
  // passed, or a provider 403, is minted again once per call, never in a loop.
  async function download(ref, { signal } = {}) {
    const sha = ref && ref.sha256, id = ref && ref.blob_id;
    if (!id) return get('/v1/blobs/' + seg(sha), { read: 'blob', signal });
    if (!SHA.test(sha || '') || !BLOB_ID.test(id)) throw untrusted('a file reference');
    const caps = await capabilities({ signal });
    for (let attempt = 0; attempt < 2; attempt++) {
      const g = await get('/v1/blobs/' + seg(sha) + '/download' + query({ blob_id: id }), { signal });
      if (!g || g.local_path || g.blob_id !== id || g.sha256 !== sha || g.method !== 'GET' || !Number.isSafeInteger(g.size) || g.size < 0 || g.size > MAX_BLOB || !verifiedRef(sha, g)) throw untrusted('a download grant');
      const cap = capability(g.url, g.expires_at, GET_TTL, caps.download_origins, g.object_expires_at, now());
      if (cap.expired) continue;
      let r;
      try {
        r = await fetch(cap.url, { method: 'GET', credentials: 'omit', redirect: 'error', cache: 'no-store', referrerPolicy: 'no-referrer', mode: 'cors', signal });
      } catch (e) {
        if (e && e.name === 'AbortError') throw aborted();
        throw unreachable();
      }
      if (r.status === 403) { if (r.body && r.body.cancel) r.body.cancel().catch(() => {}); continue; }
      if (r.status !== 200) throw new APIError(r.status, 'blob_unavailable', 'storage could not serve the file');
      let bytes;
      try { bytes = await readBounded(r, g.size); } catch (e) {
        if (e && e.name === 'AbortError') throw aborted();
        throw unreachable();
      }
      if (!bytes || bytes.length !== g.size) throw mismatch();
      const digest = toHex(new Uint8Array(await globalThis.crypto.subtle.digest('SHA-256', bytes)));
      if (digest !== sha) throw mismatch();
      return new Blob([bytes], { type: 'application/octet-stream' });
    }
    throw capExpired(403);
  }
  // activity opens the operator activity feed (PROTOCOL §2.13), an SSE
  // stream. EventSource cannot send headers, so this is a fetch whose body
  // the caller reads: the bearer stays in Authorization, never in the URL.
  async function activity({ signal } = {}) {
    let r;
    try {
      r = await fetch('/v1/admin/activity', {
        method: 'GET', signal, cache: 'no-store', credentials: 'omit',
        headers: { Authorization: 'Bearer ' + secret, Accept: 'text/event-stream' },
      });
    } catch (e) {
      throw transportError(e, 'hub unreachable');
    }
    if (r.ok && r.body) return r;
    let j = null;
    try { j = await r.json(); } catch { /* the status still says what happened */ }
    throw httpError(r.status, r.statusText, j);
  }
  const get = (path, options) => request('GET', path, options);
  const post = (path, options) => request('POST', path, options);

  // Hub preferences (operator API, plan 2026-11-05 Task 4). GET carries an
  // ETag; PATCH and preview send it back as the one strong If-Match. A
  // confirmation token travels only in the JSON body, never in a URL.
  const prefs = ({ etag, signal } = {}) => get('/v1/admin/prefs', { signal, meta: true, headers: etag ? { 'If-None-Match': etag } : undefined });
  const patchPrefs = (patch, etag, { signal } = {}) => request('PATCH', '/v1/admin/prefs', { body: patch, signal, meta: true, headers: { 'If-Match': etag } });
  const previewPrefs = (patch, etag, { signal } = {}) => post('/v1/admin/prefs/preview', { body: patch, signal, headers: { 'If-Match': etag } });

  return {
    peers: () => get('/v1/peers'),
    adminPeers: () => get('/v1/admin/peers'),
    addPeer: body => post('/v1/admin/peers', { body }),
    dotLink: peerId => post('/v1/admin/dot-links', { body: { peer_id: peerId } }),
    principals: () => get('/v1/admin/principals'),
    routes: () => get('/v1/admin/routes'),
    inbox: ({ limit, before, unread } = {}) => get('/v1/admin/inbox' + query({ limit, before, unread: unread ? 1 : undefined })),
    myThreads: ({ limit } = {}) => get('/v1/threads' + query({ limit })),
    threads: ({ participant, excludeParticipant, limit, cursor } = {}) => get('/v1/admin/threads' + query({ participant, exclude_participant: excludeParticipant, limit, cursor })),
    thread: id => get('/v1/admin/threads/' + seg(id)),
    messages: (id, { after, limit } = {}) => get('/v1/threads/' + seg(id) + '/messages' + query({ after, limit })),
    matrix: (id, { after, limit } = {}) => get('/v1/admin/threads/' + seg(id) + '/deliveries' + query({ after, limit })),
    readThread: id => post('/v1/admin/threads/' + seg(id) + '/read'),
    // One delivery's receipt (PROTOCOL §2.5): the thread view posts 'read'.
    receipt: (deliveryId, state) => post('/v1/receipts', { body: { delivery_id: deliveryId, state } }),
    // signal lets a sign-out end a parked long-poll at once.
    activity,
    events: ({ after, wait, signal } = {}) => get('/v1/events' + query({ after, wait }), { signal }),
    ack: cursor => post('/v1/ack', { body: { cursor } }),
    send: body => post('/v1/messages', { body, headers: { 'X-Vecnet-Ingress': 'operator_ui' } }),
    react: body => post('/v1/reactions', { body, headers: { 'X-Vecnet-Ingress': 'operator_ui' } }),
    presence: (state, at) => post('/v1/presence', { body: { state, at } }),
    deadLetters: () => get('/v1/admin/dead-letters'),
    retryDeadLetter: dly => post('/v1/admin/dead-letters/' + seg(dly) + '/retry'),
    discardDeadLetter: dly => post('/v1/admin/dead-letters/' + seg(dly) + '/discard'),
    subscriptions: () => get('/v1/admin/subscriptions'),
    resumeSubscription: id => post('/v1/admin/subscriptions/' + seg(id) + '/resume'),
    loopCuts: hours => get('/v1/admin/loop-cuts' + query({ hours })),
    deliveryHealth: () => get('/v1/admin/delivery-health'),
    audit: ({ limit, before } = {}) => get('/v1/admin/audit' + query({ limit, before })),
    counters: () => get('/v1/admin/counters'),
    notices: () => get('/v1/hub/notices'),
    prefs, patchPrefs, previewPrefs,
    // blob(sha) reads a hub-hosted blob; blob(sha, id) a direct R2 one.
    blob: (sha, id) => (id ? download({ sha256: sha, blob_id: id }) : get('/v1/blobs/' + seg(sha), { read: 'blob' })),
    capabilities, download,
    // Metadata only (no link, no mint): an R2 file's state on the hub clock.
    blobStatus: (sha, id, { signal } = {}) => get('/v1/blobs/' + seg(sha) + '/status' + query({ blob_id: id }), { signal }),
    // Operator-only R2 storage figures (retained, reserved, used, limit).
    blobStorage: () => get('/v1/admin/blobs/storage'),
    // A peer's avatar PNG (PROTOCOL §8.1), by bearer like a blob.
    avatar: id => get('/v1/peers/' + seg(id) + '/avatar', { read: 'blob' }),
    upload,
  };
}
