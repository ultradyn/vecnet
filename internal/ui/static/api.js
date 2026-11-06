// The operator API client: one method per hub route. The secret travels only
// in the Authorization header, never in a URL (PROTOCOL §2).
import { mediaTypeOf } from './attach-model.js';

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

// onUnauthorized fires at most once per client: every request parked when a
// secret is rotated answers 401, and one sign-out is enough. XHR is only for
// uploads, because fetch reports no upload progress.
export function createApi({ secret, fetch = globalThis.fetch, onUnauthorized = () => {}, XHR = globalThis.XMLHttpRequest }) {
  let unauthorized = false;

  // httpError turns a non-2xx reply (j is its parsed body or null) into an
  // APIError, for fetch requests and uploads alike.
  function httpError(status, statusText, j) {
    const e = (j && j.error) || {};
    const err = new APIError(status, e.code || 'http_' + status, e.message || statusText, e.details);
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
    throw httpError(r.status, r.statusText, j);
  }

  // upload PUTs file as a blob that expires after a day unless a message
  // references it (PROTOCOL §2.7). The File is the body, so the browser sends
  // its exact Content-Length. onProgress(loaded, total) follows the upload.
  function upload(file, { onProgress, signal } = {}) {
    return new Promise((resolve, reject) => {
      const aborted = () => new APIError(0, 'aborted', 'request aborted');
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
    // A peer's invite (PROTOCOL §2.12): its state, never a token; a new one,
    // whose answer carries the one-time token for the copy panel only; and
    // revoke, a no-op answer when nothing is live.
    inviteStatus: peerId => get('/v1/admin/peers/' + seg(peerId) + '/enrol-token'),
    reissueInvite: (peerId, ttl) => post('/v1/admin/peers/' + seg(peerId) + '/enrol-token', { body: ttl ? { enrol_ttl_seconds: ttl } : undefined }),
    revokeInvite: peerId => request('DELETE', '/v1/admin/peers/' + seg(peerId) + '/enrol-token'),
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
    blob: sha => get('/v1/blobs/' + seg(sha), { read: 'blob' }),
    // A peer's avatar PNG (PROTOCOL §8.1), by bearer like a blob.
    avatar: id => get('/v1/peers/' + seg(id) + '/avatar', { read: 'blob' }),
    upload,
  };
}
