// Pure thread logic: what each message renders as. No DOM, no clock.
import { SPILL_SLOT } from './attach-model.js';

export const SPILL_BYTES = 4096, TEXT_CAP = 65536;
// Show full text reads at most this much, and only text/* (PROTOCOL §2.7).
export const FULL_TEXT_CAP = 1 << 20;
const SPILL_NAME = 'message.md';

// A one-line description of a message, for reply quotes.
export function excerpt(m) {
  if (!m) return '';
  if (m.approval_request) return 'Approval: ' + m.approval_request.question;
  if (m.approval_response) return 'Decision: ' + m.approval_response.decision;
  if (m.job_status) return 'Job ' + m.job_status.state + (m.content && m.content.text ? ' · ' + m.content.text : '');
  return (m.content && m.content.text) || '(' + m.kind + ')';
}

// The hub writes sender "hub" on the messages it authors (PROTOCOL §14.3).
export function isSystem(m) {
  return !!m && m.sender === 'hub';
}

function systemText(m) {
  const r = m.approval_response;
  if (r && r.decision === 'expired') return 'approval expired';
  if (r) return 'approval closed: ' + r.decision;
  return (m.content && m.content.text) || m.kind;
}

// The hub sets metadata["vecnet.spilled"] and appends message.md (§2.2 step 6).
// Peers cannot set vecnet.* keys, so the metadata is the hub's word; a file
// that is merely named message.md is not.
export function spillOf(m) {
  if (!m || !m.metadata || m.metadata['vecnet.spilled'] !== true) return null;
  const atts = m.attachments || [];
  if (!atts.length) return null;
  for (let i = atts.length - 1; i >= 0; i--) if (atts[i] && atts[i].name === SPILL_NAME) return atts[i];
  return atts[atts.length - 1];
}

// The hub sets metadata["vecnet.client"] on a CLI send with the operator
// credential (§2.2 step 11): "Operator on <host> CLI", plus "via ssh" and
// "(agent suspected)". Peers cannot write vecnet.* keys, so the value is the
// hub's word; it is still flattened to one line and capped.
export const CLIENT_LABEL_CAP = 96;
const CLIENT_UNSAFE = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/g;
export function clientLabel(m) {
  const c = m && m.metadata && m.metadata['vecnet.client'];
  if (!c || typeof c !== 'object' || c.kind !== 'operator-cli' || typeof c.label !== 'string') return '';
  return c.label.replace(CLIENT_UNSAFE, ' ').replace(/\s+/g, ' ').trim().slice(0, CLIENT_LABEL_CAP);
}

// clientSuspect: the hub marked the CLI send agent_suspected (§13.7).
export function clientSuspect(m) {
  return !!clientLabel(m) && m.metadata['vecnet.client'].agent_suspected === true;
}

export function canShowFull(att) {
  if (!att || typeof att.media_type !== 'string') return false;
  return /^text\//i.test(att.media_type) && Number.isFinite(att.size) && att.size >= 0 && att.size <= FULL_TEXT_CAP;
}

function receiptsOf(row, sender) {
  if (!row) return [];
  return (row.cells || [])
    .filter(c => c && c.recipient !== sender)
    .map(c => ({ recipient: c.recipient, display: c.display, state: c.state, delivery_id: c.delivery_id }));
}

// items turns a page of thread history into render items. matrix is the
// §2.13 delivery matrix (or null); openApprovals is the thread detail's list.
export function items(messages, matrix, openApprovals) {
  const list = messages || [];
  const byId = new Map(list.map(m => [m.message_id, m]));
  const rows = new Map(((matrix && matrix.rows) || []).map(r => [r.message_id, r]));
  const open = new Map((openApprovals || []).map(a => [a.approval_id, a]));
  const decisions = new Map();
  for (const m of list) if (m.approval_response) decisions.set(m.approval_response.approval_id, m.approval_response.decision);

  return list.map(m => {
    const system = isSystem(m);
    let parent = null;
    if (m.in_reply_to) {
      const p = byId.get(m.in_reply_to), row = rows.get(m.in_reply_to);
      parent = p
        ? { message_id: p.message_id, sender: p.sender, excerpt: excerpt(p), loaded: true }
        : { message_id: m.in_reply_to, sender: row ? row.sender : null, excerpt: row ? row.excerpt || '' : '', loaded: false };
    }
    let approval = null;
    const req = m.approval_request;
    if (req && !system) {
      const o = open.get(req.approval_id);
      approval = {
        approval_id: req.approval_id,
        message_id: m.message_id,
        question: req.question,
        options: (o && o.options) || req.options || [],
        expires_at: (o && o.expires_at) || req.expires_at,
        open: !!o,
        actionable: !!o && o.addressed_to_me === true,
        decision: o ? null : decisions.get(req.approval_id) ?? null,
      };
    }
    return {
      message: m,
      parent,
      receipts: system ? [] : receiptsOf(rows.get(m.message_id), m.sender),
      spill: spillOf(m),
      approval,
      system,
      text: system ? systemText(m) : null,
    };
  });
}

// budgetBanner is the composer's notice for a thread with no budget left. It
// speaks to the operator, who is the one able to raise it, and names how.
export function budgetBanner() {
  return {
    kind: 'budget',
    text: 'Thread budget exhausted: 0 messages left. The hub refuses new messages here until you raise the budget with the admin thread PATCH (PROTOCOL §2.12), or start a new thread.',
  };
}

// budgetOut is whether a thread summary says its budget is spent, so the
// banner shows before a send is refused, not only after.
export function budgetOut(t) {
  return !!t && t.budget_remaining === 0;
}

// sendError says what the composer shows for a failed send, and whether the
// next attempt keeps the idempotency key: a lost or throttled send might have
// landed, so it must be retried under the same key; a definite rejection will
// not change, so the next attempt is a new send.
// thread_budget_exhausted means the budget is 0 (PROTOCOL §2.2), so the
// banner says 0 rather than a possibly stale budget_remaining.
export function sendError(e) {
  const status = e && e.status, code = (e && e.code) || 'error', message = (e && e.message) || '';
  const keepKey = !status || status >= 500 || status === 429;
  if (code === 'thread_budget_exhausted') return { ...budgetBanner(), keepKey };
  // The spilled message.md needs an attachment slot (internal/hub/spill.go).
  if (status === 413 && code === 'payload_too_large' && e.details && e.details.field === 'attachments') return { kind: 'error', keepKey, text: SPILL_SLOT };
  let text = code + ': ' + message;
  const retry = e && e.details && e.details.retry_after_seconds;
  if (code === 'rate_limited' && Number.isFinite(retry)) text += ' (retry after ' + retry + ' s)';
  return { kind: 'error', keepKey, text };
}

// replySig is the attachments' part of a reply's signature: every wire field
// of each attachment, in order. The sha256s alone (attach-model signature())
// would keep the key when a chip is replaced by the same bytes under another
// name or media type, and the hub refuses that changed body under the old key
// as 409 idempotency_conflict (PROTOCOL §5.3).
// An R2 reference adds its grant, recorded class and deadline: the same
// bytes under a new grant are a changed body.
export function replySig(files) {
  return JSON.stringify((files || []).map(a => {
    const base = [a.sha256, a.size, a.media_type, a.name ?? null];
    return a.blob_id ? [...base, a.blob_id, a.retention_class ?? null, a.expires_at ?? null] : base;
  }));
}

// replyKey is the reply's pending send: the same one, idempotency key and all,
// while its text, priority, target and attachments' signature are unchanged
// (PROTOCOL §5.3), else a new one under a fresh key from mint.
export function replyKey(pending, { text, prio, replyTo, sig }, mint) {
  if (pending && pending.text === text && pending.prio === prio && pending.replyTo === replyTo && pending.sig === sig) return pending;
  return { key: mint(), text, prio, replyTo, sig };
}

// unreadOf lists { message_id, delivery_id } for each message whose
// delivery to me (§2.13 matrix) has no read_at (CoS unread ruling), as the
// inbox counts (opview/inbox.go): not superseded; an approval only once
// closed (answered, or expired by now) and not by me (or answered here).
export function unreadOf(messages, matrix, { me, now, openApprovals, answered } = {}) {
  if (!matrix || !me) return [];
  const list = messages || [];
  const mine = new Map();
  for (const row of matrix.rows || []) {
    for (const c of row.cells || []) if (c && c.recipient === me) mine.set(row.message_id, c);
  }
  const open = new Set((openApprovals || []).map(a => a.approval_id));
  const by = new Map(); // approval id -> who answered first
  for (const m of list) {
    const r = m.approval_response;
    if (r && !by.has(r.approval_id)) by.set(r.approval_id, m.sender);
  }
  for (const id of answered || []) if (!by.has(id)) by.set(id, me);
  const out = [];
  for (const m of list) {
    const c = mine.get(m.message_id);
    if (!c || !c.delivery_id || c.read_at || c.state === 'superseded') continue;
    const req = m.approval_request;
    if (req) {
      const who = by.get(req.approval_id), end = Date.parse(req.expires_at);
      if (who === me || open.has(req.approval_id) || (who === undefined && !(end <= now))) continue;
    }
    out.push({ message_id: m.message_id, delivery_id: c.delivery_id });
  }
  return out;
}

// opening: a thread opens at its first unread under New, else at the bottom.
export function opening(unread) {
  const first = (unread || [])[0];
  return first ? { divider: first.message_id, at: first.message_id } : { divider: null, at: 'bottom' };
}

// nextDivider places New for messages turning unread while open (fresh:
// live arrivals). New marks only the unseen: fresh messages move it (above
// the oldest unseen) unless watched arriving (page active, view at the
// bottom) or New still marks unread messages not yet seen.
export function nextDivider({ divider, unread, fresh, seen, watching }) {
  if (!fresh || !fresh.length || watching) return divider;
  const isFresh = new Set(fresh.map(u => u.message_id));
  const unseen = (unread || []).filter(u => isFresh.has(u.message_id) || !seen(u.message_id));
  if (divider && unseen.some(u => !isFresh.has(u.message_id))) return divider;
  return (unseen[0] || fresh[0]).message_id; // the oldest unseen, so none sits above New
}
