// Attachment rules for the operator's uploads. Pure: no DOM, and no clock
// unless a caller leaves one out. Limits are the hub's (PROTOCOL §2.7):
// 25 MiB a blob, 16 attachments a send, one fewer when the text spills into
// message.md (internal/hub/spill.go).
import { SPILL } from './compose-model.js';
import { clean, when } from './format.js';

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

const isDone = it => it.state === 'done' && !!it.sha256;

// ready: nothing queued, uploading or failed.
export function ready(items) {
  return (items || []).every(isDone);
}

export function toAttachments(items) {
  return (items || []).filter(isDone).map(it => ({ sha256: it.sha256, size: it.size, media_type: it.media_type, name: it.name }));
}

// signature is the attachments' part of a send's idempotency signature.
export function signature(items) {
  return toAttachments(items).map(a => a.sha256).join(',');
}

// sendRefusal is the spill rule: text over 4096 bytes needs a slot for its
// message.md, so 16 attachments are one too many.
export function sendRefusal(items, text) {
  return enc.encode(text || '').length > SPILL && toAttachments(items).length >= MAX_ATTACHMENTS ? SPILL_SLOT : '';
}

const mib = n => Math.round((n / (1 << 20)) * 10) / 10 + ' MiB';

// errorText is one line for a failed upload or send.
export function errorText(err, nowMs = Date.now()) {
  const e = err || {}, d = e.details || {};
  if (e.status === 413 && e.code === 'payload_too_large' && d.field === 'attachments') return SPILL_SLOT;
  if (e.status === 413) return 'Too large: the hub takes at most ' + mib(d.limit > 0 ? d.limit : MAX_BYTES) + ' a file.';
  if (e.status === 429) {
    const what = e.code === 'upload_quota_exceeded' ? 'Daily upload quota reached' : 'Too many uploads';
    const secs = Number(d.retry_after_seconds);
    return what + (secs > 0 ? '; try again at ' + when(new Date(nowMs + secs * 1000).toISOString(), nowMs) + '.' : '; try again later.');
  }
  if (e.code === 'aborted') return 'Upload cancelled.';
  if (e.code === 'network') return 'Upload failed: the hub could not be reached.';
  return 'Upload failed: ' + (e.message || e.code || 'unknown error') + (e.status ? ' (' + e.status + ')' : '');
}
