// Attachment rules for the operator's uploads. Pure: no DOM, and no clock
// unless a caller leaves one out. Limits are the hub's (PROTOCOL §2.7):
// 25 MiB a blob, 16 attachments a send, one fewer when the text spills into
// message.md (internal/hub/spill.go).
import { SPILL } from './compose-model.js';
import { bytes, clean, when } from './format.js';

export const MAX_ATTACHMENTS = 16, MAX_BYTES = 25 << 20;
export const SPILL_SLOT = 'Remove one attachment or shorten the text: the long text needs an attachment slot.';

const enc = new TextEncoder();
const EXT = { 'image/png': 'png', 'image/jpeg': 'jpg', 'image/gif': 'gif', 'image/webp': 'webp' };
const TOKEN = "[!#$%&'*+.^_`|~0-9A-Za-z-]+";
const MEDIA = new RegExp('^' + TOKEN + '/' + TOKEN + '(\\s*;.*)?$');
const pad = n => String(n).padStart(2, '0');

// mediaTypeOf is the file's declared type, never sniffed; anything that is not
// a media type would be a 422, so it goes up as octet-stream instead.
export function mediaTypeOf(file) {
  const t = String((file && file.type) || '').trim();
  return t.length <= 128 && MEDIA.test(t) ? t : 'application/octet-stream';
}

const essence = type => type.split(';')[0].trim().toLowerCase();

// cutName cleans a name for display and the schema: no controls or bidi
// overrides, no line breaks, at most 255 characters (code points).
function cutName(name) {
  const s = clean(name || '').replace(/[\n\t]/g, ' ').trim();
  return Array.from(s).slice(0, 255).join('') || 'attachment';
}

// attachmentName: a picked or dropped file keeps its own name. A pasted one is
// named from now's local time, since browsers call every clipboard image
// image.png.
export function attachmentName(file, now = new Date(), { pasted = false } = {}) {
  if (!pasted) return cutName(file && file.name);
  const d = new Date(now);
  const stamp = d.getFullYear() + pad(d.getMonth() + 1) + pad(d.getDate()) + '-' + pad(d.getHours()) + pad(d.getMinutes()) + pad(d.getSeconds());
  return 'pasted-' + stamp + '.' + (EXT[essence(mediaTypeOf(file))] || 'bin');
}

// canAdd splits files into those that fit beside items and those refused.
// Every chip holds a slot, whatever its state; a refused file takes none.
export function canAdd(items, files) {
  const ok = [], refused = [];
  let n = (items || []).length;
  for (const f of Array.from(files || [])) {
    const name = cutName(f && f.name);
    if (!f || !f.size) refused.push({ name, reason: 'empty' });
    else if (f.size > MAX_BYTES) refused.push({ name, reason: 'too_large' });
    else if (n >= MAX_ATTACHMENTS) refused.push({ name, reason: 'too_many' });
    else { ok.push(f); n++; }
  }
  return { files: ok, refused };
}

export function refusalText({ name, reason }) {
  const why = { too_many: 'at most ' + MAX_ATTACHMENTS + ' attachments a message', too_large: 'over the 25 MiB limit', empty: 'empty file' };
  return name + ': ' + (why[reason] || reason);
}

// verified: the hub recorded provider verification for exactly this digest.
const verified = (sha, r) => !!r && r.sha256_verified === true && !!r.integrity && r.integrity.algorithm === 'sha256' && r.integrity.verified === true && r.integrity.value === sha;

// attachmentExpired: an R2 chip whose recorded object deadline has arrived
// on the caller's clock (the hub clock). keep, and a disk chip, have none.
export function attachmentExpired(it, nowMs = Date.now()) {
  if (!it || !it.blob_id || it.expires_at == null) return false;
  const at = Date.parse(it.expires_at);
  return Number.isFinite(at) && at <= nowMs;
}

// A chip is done once its upload resolved; an R2 chip also needs the hub's
// recorded verification, so a pending grant is never sent. A deadline that
// has passed is not sendable either: the draft re-uploads under a new grant.
const isDone = (it, nowMs) => it.state === 'done' && !!it.sha256 && (!it.blob_id || verified(it.sha256, it)) && !attachmentExpired(it, nowMs);

// ready: nothing queued, hashing, uploading, verifying, failed or expired.
export function ready(items, nowMs = Date.now()) {
  return (items || []).every(it => isDone(it, nowMs));
}

// toAttachments: the send's references. An R2 one names its grant, recorded
// class and deadline (null for keep); the verification fields stay out,
// since a send reference may not assert them (envelope attachmentReference).
// An expired ref is left out, so Send cannot carry it.
export function toAttachments(items, nowMs = Date.now()) {
  return (items || []).filter(it => isDone(it, nowMs)).map(it => (it.blob_id
    ? { blob_id: it.blob_id, sha256: it.sha256, size: it.size, media_type: it.media_type, name: it.name, retention_class: it.retention_class, expires_at: it.expires_at ?? null }
    : { sha256: it.sha256, size: it.size, media_type: it.media_type, name: it.name }));
}

// signature is the attachments' part of a send's idempotency signature: the
// same bytes under a new grant are a different attachment.
export function signature(items, nowMs = Date.now()) {
  return toAttachments(items, nowMs).map(a => (a.blob_id ? a.blob_id + ':' : '') + a.sha256).join(',');
}

// retryKey: the idempotency key a Retry reuses, or null for a fresh grant.
// A transfer that may have landed retries under the same grant (the hub
// replays it; the provider answers 412 if the bytes are there). A spent,
// conflicting or refused grant, or changed bytes, start again.
// An untrusted grant or a refused request keeps its key: replaying it costs
// no new reservation. An expired link (no bytes can have landed) or bytes the
// hub rejected start again.
const FRESH = new Set(['upload_grant_expired', 'idempotency_conflict', 'blob_checksum_mismatch', 'blob_capability_expired', 'blob_proof_mismatch', 'blob_storage_quota_exceeded', 'upload_quota_exceeded', 'blob_read_failed', 'invalid_value', 'blob_too_large']);
export function retryKey(item, err) {
  const code = err && err.code;
  return item && item.idem && !FRESH.has(code) ? item.idem : null;
}

// sendRefusal is the spill rule: text over 4096 bytes needs a slot for its
// message.md, so 16 attachments are one too many.
export function sendRefusal(items, text) {
  return enc.encode(text || '').length > SPILL && toAttachments(items).length >= MAX_ATTACHMENTS ? SPILL_SLOT : '';
}

// limitText: a limit of a MiB or more in MiB (10 MiB, 25 MiB), a smaller one
// as format.js bytes() shows sizes, never '0 MiB'.
const limitText = n => n >= 1 << 20 ? Math.round((n / (1 << 20)) * 10) / 10 + ' MiB' : bytes(n);

// errorText is one line for a failed upload or send.
export function errorText(err, nowMs = Date.now()) {
  const e = err || {}, d = e.details || {};
  if (e.status === 413 && e.code === 'payload_too_large' && d.field === 'attachments') return SPILL_SLOT;
  if (e.status === 413 && e.code === 'blob_too_large') return 'Too large: the hub takes at most ' + limitText(d.limit > 0 ? d.limit : MAX_BYTES) + ' a file.';
  if (e.status === 429 && e.code === 'blob_storage_quota_exceeded') return 'Hub storage is full: new uploads are refused until files expire or are purged. Nothing already uploaded is deleted.';
  if (e.status === 429) {
    const what = e.code === 'upload_quota_exceeded' ? 'Daily upload quota reached' : e.code === 'upload_busy' ? 'Too many uploads at once' : 'Too many uploads';
    const secs = Number(d.retry_after_seconds);
    return what + (secs > 0 ? '; try again at ' + when(new Date(nowMs + secs * 1000).toISOString(), nowMs) + '.' : '; try again later.');
  }
  if (e.code === 'aborted') return 'Upload cancelled.';
  if (e.code === 'network') return 'Upload failed: the hub could not be reached.';
  const direct = {
    storage_unreachable: 'Upload failed: storage could not be reached or refused this page (CORS). Retry sends it again under the same grant.',
    storage_redirect: 'Upload refused: storage tried to redirect it. Retry, or tell the hub operator.',
    blob_capability_expired: 'The upload link expired before storage took the file. Retry starts a new upload.',
    storage_refused: 'Upload refused by storage (400): the signed request did not match. Retry sends it again; if it keeps failing, tell the hub operator.',
    blob_proof_mismatch: 'The hub rejected what storage holds for this upload. Retry starts a new upload.',
    blob_checksum_mismatch: 'Upload refused: the file changed after it was hashed. Retry hashes it again.',
    upload_grant_expired: 'The upload grant expired. Retry starts a new upload.',
    upload_incomplete: 'The hub did not find the file in storage yet. Retry sends it again.',
    direct_upload_required: 'This hub now takes uploads directly to storage: reload the page, then retry.',
    blob_read_failed: 'The file could not be read; it may have changed or moved since it was picked.',
    hash_unavailable: 'This browser cannot hash files here (Web Crypto needs HTTPS), so it cannot upload to this hub.',
    bad_response: 'Upload stopped: the hub sent a transfer grant this page does not trust. Retry asks for the same grant again.',
    blob_unavailable: 'Upload failed: storage is unavailable right now. Retry in a moment.',
  }[e.code];
  if (direct) return direct;
  return 'Upload failed: ' + (e.message || e.code || 'unknown error') + (e.status ? ' (' + e.status + ')' : '');
}

// ---------- R2 attachment state (plan 2026-11-05-r2-blobs §4, Task 7) ----------

// Labels use each file's recorded class, never the hub's current preference.
export const RETENTION_TEXT = { '1h': '1-hour', '6h': '6-hour', '12h': '12-hour', '1d': '24-hour', '7d': '7-day', '30d': '30-day' };
export const KEPT_TEXT = 'Kept while referenced';
export const expiredText = cls => (RETENTION_TEXT[cls] ? `Attachment expired (${RETENTION_TEXT[cls]} retention)` : 'Attachment expired');
const PURGED_TEXT = 'Attachment purged';

// untilText: a coarse countdown for a chip, "in 30 min", "in 23 h", "in 6 d".
export function untilText(ms) {
  if (!(ms > 0)) return 'now';
  const m = Math.round(ms / 60000);
  if (m < 1) return 'in under a minute';
  if (m < 60) return `in ${m} min`;
  const hrs = Math.round(ms / 3600000);
  if (hrs <= 48) return `in ${hrs} h`;
  return `in ${Math.round(ms / 86400000)} d`;
}

// blobView merges an R2 reference (a message attachment, or a file row's
// reference) with its hub state projection (blob_states) on the hub clock.
// null for a legacy disk reference. A recorded deadline that has passed is
// expired here at once, before the hub's ticker or the next page says so.
export function blobView(att, state, nowMs = Date.now()) {
  if (!att || !att.blob_id) return null;
  const s = state && typeof state === 'object' ? state : {};
  const cls = att.retention_class || s.retention_class || '';
  const keep = cls === 'keep';
  const recorded = att.expires_at !== undefined ? att.expires_at : s.expires_at;
  const expiresAt = keep ? null : typeof recorded === 'string' && Number.isFinite(Date.parse(recorded)) ? recorded : null;
  let st = ['available', 'expired', 'purged', 'unavailable'].includes(s.state) ? s.state : 'available';
  if (att.purged) st = 'purged';
  if ((st === 'available' || st === 'unavailable') && expiresAt && Date.parse(expiresAt) <= nowMs) st = 'expired';
  const trusted = verified(att.sha256, s.sha256_verified !== undefined ? s : att);
  let label;
  if (st === 'expired') label = expiredText(cls);
  else if (st === 'purged') label = PURGED_TEXT;
  else if (st === 'unavailable') label = 'Storage unavailable';
  else if (keep) label = KEPT_TEXT;
  else if (expiresAt) label = 'Expires ' + untilText(Date.parse(expiresAt) - nowMs);
  else label = '';
  return {
    blobId: att.blob_id, state: st, cls, keep, expiresAt, verified: trusted, label,
    trust: trusted ? 'SHA-256 verified by storage' : 'SHA-256 not verified yet (upload pending)',
    canDownload: st === 'available' || st === 'unavailable',
  };
}

// nextExpiry: when the earliest file that is still downloadable expires (ms),
// for a redraw timer; null when none will. Unavailable stays downloadable
// until its deadline, so it schedules too; keep files never do.
export function nextExpiry(views, nowMs = Date.now()) {
  let next = null;
  for (const v of views || []) {
    if (!v || (v.state !== 'available' && v.state !== 'unavailable') || !v.expiresAt) continue;
    const at = Date.parse(v.expiresAt);
    if (at > nowMs && (next === null || at < next)) next = at;
  }
  return next;
}

// downloadErrorText: one line for a failed download. Expired and purged
// differ; a mint-limit refusal says when to try again. No URL is ever shown.
export function downloadErrorText(err, nowMs = Date.now(), cls = '') {
  const e = err || {}, d = e.details || {};
  switch (e.code) {
    case 'blob_expired': return expiredText(cls);
    case 'blob_purged': return 'Attachment purged from the hub';
    case 'blob_url_rate_limited': {
      const secs = Number(d.retry_after_seconds);
      return 'Too many download links in the last minute; ' + (secs > 0 ? `try again in ${Math.ceil(secs)} s.` : 'try again shortly.');
    }
    case 'blob_checksum_mismatch': return 'Download failed its integrity check (SHA-256 mismatch); nothing was saved.';
    case 'storage_unreachable': return 'Download failed: storage could not be reached or refused this page (CORS).';
    case 'blob_capability_expired': return 'The download link expired twice in a row; try again.';
    case 'blob_unavailable': return 'Storage is unavailable right now; try again in a moment.';
    case 'bad_response': return 'Download stopped: the hub sent a link this page does not trust.';
    case 'aborted': return 'Download cancelled.';
    case 'network': return 'Download failed: the hub could not be reached.';
  }
  return 'Download failed: ' + (e.code || 'error') + (e.message ? ': ' + e.message : '');
}
