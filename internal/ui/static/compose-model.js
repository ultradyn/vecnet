// New message, pure part: who can be addressed, the SendRequest a form makes,
// and the composer's byte counter and idempotency-key rule. No DOM.
import { bytes } from './format.js';

export const SPILL = 4096, TEXT_CAP = 65536;
const PRIORITIES = ['wake', 'wake_idle', 'hold'];
const HISTORY = ['from_join', 'full'];
const ID = /^(peer|team|grp)_[A-Za-z0-9]+$/;
const enc = new TextEncoder();
const TIME = { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' };

// eligible is the peers a new message can go to, sorted by name: never the
// operator (kind or the signed-in id) and never a webhook source, which
// cannot receive. peers is the directory's array or its response object.
export function eligible(peers, me) {
  const list = Array.isArray(peers) ? peers : (peers && Array.isArray(peers.peers) ? peers.peers : []);
  return list
    .filter(p => p && p.kind !== 'operator' && p.kind !== 'source' && p.peer_id !== me)
    .sort((a, b) => String(a.name).localeCompare(String(b.name), 'en', { sensitivity: 'base' }) || (a.peer_id < b.peer_id ? -1 : 1));
}

// matches is the picker's search: by display name only, since a name is for
// finding a peer and its id is what gets sent (PROTOCOL §14.4).
export function matches(peer, query) {
  const q = String(query || '').trim().toLowerCase();
  return !q || String(peer.name || '').toLowerCase().includes(q);
}

const textBytes = text => enc.encode(text || '').length;

export function canSend(form) {
  return (form.recipients || []).length > 0 && String(form.text || '').trim() !== '' && textBytes(form.text) <= TEXT_CAP;
}

// buildSend is the POST /v1/messages body for form under key. It throws on a
// form the hub would reject for its shape, so nothing malformed is sent.
export function buildSend(form, key) {
  if (!key) throw new Error('an idempotency key is required');
  if (!PRIORITIES.includes(form.priority)) throw new Error('priority must be one of ' + PRIORITIES.join(', '));
  const recipients = [...new Set(form.recipients || [])];
  if (!recipients.length) throw new Error('at least one recipient is required');
  const name = recipients.find(r => !ID.test(String(r)));
  if (name !== undefined) throw new Error('recipients are ids, not names: ' + name);
  if (!String(form.text || '').trim()) throw new Error('text is required');
  const history = form.historyAccess || 'from_join';
  if (!HISTORY.includes(history)) throw new Error('history_access must be from_join or full');

  const body = { idempotency_key: key, recipients, priority: form.priority, content: { text: form.text } };
  const title = String(form.title || '').trim();
  if (title) body.thread_title = title;
  if (history !== 'from_join') body.history_access = history;
  if (form.lockedParticipants) body.locked_participants = true;
  return body;
}

// counter is the composer's byte counter: the spill threshold, then the cap.
export function counter(text) {
  const n = textBytes(text);
  return {
    label: n > SPILL ? bytes(n) + ' · spills to message.md' : n + ' / ' + SPILL + ' B',
    tone: n > TEXT_CAP ? 'over' : n > SPILL ? 'spill' : '',
  };
}

// signature is what the hub compares under one idempotency key: the body
// without the key.
function signature(form) {
  try { return JSON.stringify(buildSend(form, '-')); } catch { return ''; }
}

// keyFor returns pending while the form would send the same body, so a
// retry after a lost reply cannot send twice (PROTOCOL §5.3), and a fresh
// {key, sig} from mint once anything in the body changes.
export function keyFor(pending, form, mint) {
  const sig = signature(form);
  if (pending && pending.sig === sig) return pending;
  return { key: mint(), sig };
}

// keepKey is the composer's rule: an unreached hub, a 5xx, 409 or 429 may
// have a different answer next time under the same key; any other 4xx is a
// definite rejection, and the next attempt is a new send.
export function keepKey(err) {
  const s = err.status;
  return !(s >= 400 && s < 500 && s !== 429 && s !== 409);
}

// errorText is the form's error line. Every peer-supplied part is a plain
// string for textContent.
export function errorText(err, nameOf, nowMs = Date.now()) {
  const d = err.details || {};
  if (err.code === 'scope_denied' && d.recipient) {
    return 'Your send scopes do not allow messages to ' + nameOf(d.recipient) + (d.via ? ' (via ' + nameOf(d.via) + ')' : '') + '.';
  }
  if (err.code === 'rate_limited') {
    const secs = Number(d.retry_after_seconds);
    if (!(secs > 0)) return 'Rate limited. Try again shortly.';
    const at = new Date(nowMs + secs * 1000).toLocaleTimeString('en-AU', TIME);
    return 'Rate limited. Try again at ' + at + ' (in ' + secs + ' s).';
  }
  return err.code + ': ' + err.message;
}
