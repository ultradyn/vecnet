// New message, pure part: who can be addressed, the SendRequest a form makes,
// and the composer's byte counter and idempotency-key rule. No DOM.
// attach-model.js imports SPILL from here; both sides use the other's
// bindings only inside functions, so the import cycle is safe.
import { MAX_ATTACHMENTS, SPILL_SLOT, sendRefusal as spillRefusal } from './attach-model.js';
import { bytes } from './format.js';

export const SPILL = 4096, TEXT_CAP = 65536;
const PRIORITIES = ['wake', 'wake_idle', 'hold'];
// The composer's existing initial priority when nobody addressed is a
// grokbot. PROTOCOL §5.2: priority is required on the wire; this is only
// the control's starting selection.
export const INITIAL_PRIORITY = 'wake';
// Preselect when any recipient or participant is grokbot (Decisions §6).
// The send still carries it explicitly (§2.2, §5.2). It is not a default
// the hub fills in, and it is not a promise that the bot will not wake.
export const GROK_PRIORITY = 'wake_idle';
// Shown beside the control while a Grok Bot is addressed. wake_idle still
// starts a routine run when presence is idle or unknown (§5.2, §13.3);
// hold does not. A started run spends quota.
export const GROK_QUOTA_NOTE = 'A Grok Bot is addressed. wake_idle starts a routine run when that bot is idle or its presence is unknown, and holds the push while the bot is busy. hold does not start a run. Each run that starts spends quota, and a run that also needs you in chat can start a second one.';
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

const attachmentsOf = form => (Array.isArray(form.attachments) ? form.attachments : []);
const hasText = form => String(form.text || '').trim() !== '';

// sendRefusal is why a form that is otherwise complete cannot go: a refusal
// the caller set, else the spill rule (text over 4096 bytes needs one of the
// 16 attachment slots for its message.md). '' when nothing refuses it.
export function sendRefusal(form) {
  if (form.sendRefusal) return String(form.sendRefusal);
  return spillRefusal(attachmentsOf(form).map(a => ({ ...a, state: 'done' })), form.text);
}

// canSend: a recipient, text or at least one attachment, every upload done
// (uploadsReady; a form without the field has no uploads), text under the
// cap, and no refusal.
export function canSend(form) {
  return (form.recipients || []).length > 0
    && (hasText(form) || attachmentsOf(form).length > 0)
    && form.uploadsReady !== false
    && textBytes(form.text) <= TEXT_CAP
    && sendRefusal(form) === '';
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
  const attachments = attachmentsOf(form);
  if (!hasText(form) && !attachments.length) throw new Error('text or at least one attachment is required');
  if (attachments.length > MAX_ATTACHMENTS) throw new Error('at most ' + MAX_ATTACHMENTS + ' attachments a message');
  const history = form.historyAccess || 'from_join';
  if (!HISTORY.includes(history)) throw new Error('history_access must be from_join or full');

  const body = { idempotency_key: key, recipients, priority: form.priority, content: { text: form.text || '' } };
  if (attachments.length) body.attachments = attachments.map(({ sha256, size, media_type, name }) => ({ sha256, size, media_type, name }));
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
// without the key, attachments included (their sha256s, in order).
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

// directoryPeers is the peer list, or null when the directory has not
// arrived. A loaded empty list is [], which names no grokbot.
function directoryPeers(peers) {
  if (Array.isArray(peers)) return peers;
  if (peers && Array.isArray(peers.peers)) return peers.peers;
  return null;
}

// anyGrokbot is true once the directory shows that any addressed id is kind
// grokbot. Ids the directory has not named are not grokbot: a name is not
// an address (§2.2), and a late directory must be allowed to change the
// preselect only while the operator has not chosen.
export function anyGrokbot(ids, peers) {
  const list = directoryPeers(peers);
  if (!list) return false;
  const kinds = new Map();
  for (const p of list) if (p && p.peer_id) kinds.set(p.peer_id, p.kind);
  return (ids || []).some(id => kinds.get(id) === 'grokbot');
}

// priorityChoice is the segmented control's value.
// manual: the operator has used the control. Later directory or presence
// updates must not overwrite it.
// locked: an in-flight send, or the retry of that same body, already
// captured current (§2.2 idempotency, §5.3). Do not change it.
// addressed: any recipient or participant is grokbot (§16.3 for a thread).
export function priorityChoice({ manual = false, locked = false, current = INITIAL_PRIORITY, addressed = false } = {}) {
  if (manual || locked) return current;
  return addressed ? GROK_PRIORITY : INITIAL_PRIORITY;
}

// composePriority applies that rule to the new-message recipients.
export function composePriority({ manual, locked, current, recipients, peers } = {}) {
  return priorityChoice({ manual, locked, current, addressed: anyGrokbot(recipients, peers) });
}

// grokQuotaNote is the explanatory line, or '' when no Grok Bot is addressed.
export function grokQuotaNote(addressed) {
  return addressed ? GROK_QUOTA_NOTE : '';
}

// errorText is the form's error line. Every peer-supplied part is a plain
// string for textContent.
export function errorText(err, nameOf, nowMs = Date.now()) {
  const d = err.details || {};
  // The hub's own spill-slot refusal reads as the form's (internal/hub/spill.go).
  if (err.status === 413 && err.code === 'payload_too_large' && d.field === 'attachments') return SPILL_SLOT;
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
