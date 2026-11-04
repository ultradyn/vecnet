// Pure display helpers. No DOM, and no clock unless a caller leaves one out.

// Times are en-AU 24-hour local. h23, because hour12:false can print 24:05.
const TIME = { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' };
const DATE = { day: 'numeric', month: 'short' };

export function when(iso, nowMs = Date.now()) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const t = d.toLocaleTimeString('en-AU', TIME);
  return d.toDateString() === new Date(nowMs).toDateString() ? t : d.toLocaleDateString('en-AU', DATE) + ' ' + t;
}

// nowMs is the hub's clock wherever the caller has one.
export function ago(iso, nowMs = Date.now()) {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = Math.floor((nowMs - t) / 1000);
  if (s < 1) return 'now';
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

// deltaLabel is how long after prevIso a message came: +3.8s, +42s, +4m 12s,
// +2h 5m, +3d. Empty for the first message, a bad time or an earlier one.
export function deltaLabel(prevIso, iso) {
  const a = Date.parse(prevIso), b = Date.parse(iso);
  if (Number.isNaN(a) || Number.isNaN(b) || b < a) return '';
  const ms = b - a;
  if (ms < 9950) return '+' + (ms / 1000).toFixed(1) + 's';
  const s = Math.round(ms / 1000);
  if (s < 60) return '+' + s + 's';
  if (s < 3600) return '+' + Math.floor(s / 60) + 'm ' + (s % 60) + 's';
  if (s < 86400) return '+' + Math.floor(s / 3600) + 'h ' + Math.floor((s % 3600) / 60) + 'm';
  return '+' + Math.floor(s / 86400) + 'd';
}

// deltaTitle is deltaLabel spelled out for a tooltip: "3.8 s after the
// previous message", "4m 12s after the previous message". Empty when the
// label is.
export function deltaTitle(prevIso, iso) {
  const d = deltaLabel(prevIso, iso);
  return d ? d.slice(1).replace(/^([\d.]+)s$/, '$1 s') + ' after the previous message' : '';
}

// fullWhen is the full local date and time, for the title of a relative
// time: "Tue, 3 Nov 2026, 14:05:09".
const FULL = { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' };
export function fullWhen(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString('en-AU', FULL);
}

// PRIORITY_HELP explains each priority (PROTOCOL §5.2) for the hover and
// focus tips on the priority buttons (issue 21).
export const PRIORITY_HELP = {
  wake: 'Interrupt now. Pushed to the recipient straight away (push routes, Grok Bot routines, dots); pull clients see it at once.',
  wake_idle: 'Interrupt only if the recipient is idle or its presence is unknown. Otherwise it waits, and is pushed once when the recipient goes idle.',
  hold: 'Never pushed. It waits in the recipient\'s inbox until it next checks; pull clients still see it as it arrives.',
};

export function bytes(n) {
  if (n < 1024) return n + ' B';
  return n < 1 << 20 ? (n / 1024).toFixed(1) + ' KiB' : (n / 1048576).toFixed(1) + ' MiB';
}

export function short(id) {
  const p = idParts(id);
  return p.mid ? p.head + '…' + p.tail : p.head;
}

// idParts splits an id for display: head and tail are what short() shows
// either side of its ellipsis, and mid is the elided rest. head + mid + tail
// is the whole id, so a page can keep the full id in its text and hide only
// mid (dom.js idText), and a selection then copies the id, never the "…".
export function idParts(id) {
  if (!id || id.length <= 14) return { head: id || '?', mid: '', tail: '' };
  const cut = id.indexOf('_') + 1;
  return { head: id.slice(0, cut), mid: id.slice(cut, -5), tail: id.slice(-5) };
}

// title is a thread's own title, else its stand-in (standInTitle).
export function title(thread, me, nameOf) {
  if (!thread) return '';
  return thread.title || standInTitle(thread, me, nameOf);
}

// standInTitle names an untitled thread by its participants, leaving out
// `me` (the operator): two peers read "A ↔ B"; a 1:1 with the operator reads
// as the other peer alone; three names are listed and more read "A, B +3", so
// the count survives the sidebar's width. A name is cleaned and folded to one
// line; an unknown or blank one is the peer's short id, as is the thread with
// no one else in it. The result is text, never markup.
const GROUP_LISTED = 3, GROUP_NAMED = 2;
export function standInTitle(thread, me, nameOf) {
  if (!thread) return '';
  const all = thread.participants || [];
  const names = all.filter(p => p !== me).map(id => clean(nameOf(id) ?? '').replace(/\s+/g, ' ').trim() || short(id));
  if (!names.length) return short(thread.thread_id);
  if (names.length === 2 && names.length === all.length) return names.join(' ↔ ');
  if (names.length <= GROUP_LISTED) return names.join(', ');
  return names.slice(0, GROUP_NAMED).join(', ') + ' +' + (names.length - GROUP_NAMED);
}

// quotaLabel is the quota rule the TUI dash shares (internal/tui dashCellQuota):
// a numeric *_pct value reads 61%, the standard weekly_remaining_pct first
// (PROTOCOL §2.6) and otherwise the first such key in sorted order; an empty
// quota reads none; anything else is compact sorted k=v.
export function quotaLabel(quota) {
  const keys = quota && typeof quota === 'object' ? Object.keys(quota).sort() : [];
  if (!keys.length) return 'none';
  for (const key of ['weekly_remaining_pct', ...keys]) {
    const v = quota[key];
    if (key.endsWith('_pct') && typeof v === 'number' && Number.isFinite(v)) return Math.round(v) + '%';
  }
  return keys.map(k => k + '=' + (typeof quota[k] === 'string' ? quota[k] : JSON.stringify(quota[k]))).join(' ');
}

// clean makes a peer-supplied string inert on the page, as tui.Clean does in
// the terminal: C0 and C1 controls, DEL and the bidi embedding, override and
// isolate controls (U+202A–202E, U+2066–2069) are dropped, so a name cannot
// reverse the UI's own words beside it; line and paragraph separators become
// \n. \n and \t stay, for bodies shown with white-space: pre-wrap.
const CONTROLS = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/g;
export function clean(s) {
  return String(s).replace(/[\u2028\u2029]/g, '\n').replace(CONTROLS, '');
}

// plural is a count with its noun: 1 message, 2 messages.
export function plural(n, noun) {
  return n + ' ' + noun + (n === 1 ? '' : 's');
}
