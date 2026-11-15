// Pure helpers for the sidebar's Your Threads and Other Threads lists, which
// share one two-line row. No DOM: covered by node.
import { ago, fullWhen, plural, standInTitle } from './format.js';

// One page of other threads, and the hub's page cap (PROTOCOL §2.13). Past
// the cap the dashboard lists every thread.
export const OTHER_PAGE = 15;
export const OTHER_MAX = 200;
const NAMED = 3;

export function nextLimit(limit) {
  return Math.min(limit + OTHER_PAGE, OTHER_MAX);
}

// peopleLine names the first participants and counts the rest: "a, b, c +2".
export function peopleLine(ids, nameOf, named = NAMED) {
  const all = ids || [];
  const shown = all.slice(0, named).map(nameOf).join(', ');
  return all.length > named ? shown + ' +' + (all.length - named) : shown;
}

const byActivity = (a, b) => {
  const at = a.last_message_at || '', bt = b.last_message_at || '';
  if (at !== bt) return at < bt ? 1 : -1;
  const ai = a.thread.thread_id, bi = b.thread.thread_id;
  return ai < bi ? 1 : ai > bi ? -1 : 0;
};

// The widest unread label; past it the pill reads 99+.
export const UNREAD_MAX = 99;

// unreadOf reads one thread's count from the inbox head's unread_by_thread
// (PROTOCOL §2.13), the badge's own count split by thread. Anything but a
// positive integer is 0: an older hub sends no map at all.
export function unreadOf(byThread, id) {
  const n = byThread && Object.hasOwn(byThread, id) ? byThread[id] : 0;
  return Number.isInteger(n) && n > 0 ? n : 0;
}

export function unreadLabel(n) {
  return n > UNREAD_MAX ? UNREAD_MAX + '+' : n > 0 ? String(n) : '';
}

// threadRow is one sidebar row from a ThreadRow (or a bare ThreadSummary
// under `thread`): title, message count and unread count, then participants
// and last activity. `me` is left out of the participants line, and named
// "you" in the tip. An untitled thread's title is its stand-in, which already
// names the participants, so its second line counts them instead. Peer
// strings stay strings: the view sets them as text. unread is the operator's
// unread deliveries in the thread; Other Threads pass none.
export function threadRow(r, me, nameOf, now, unread = 0) {
  const t = r.thread, count = Number.isInteger(t.message_count) ? t.message_count : null;
  const ids = t.participants || [];
  const untitled = !t.title;
  const title = untitled ? standInTitle(t, me, nameOf) : t.title;
  const all = ids.map(p => (p === me ? 'you' : nameOf(p))).join(', ');
  const tip = [title, all, [count === null ? '' : plural(count, 'message'), unread ? unread + ' unread' : '', r.last_message_at ? 'last active ' + fullWhen(r.last_message_at) : ''].filter(Boolean).join(' · ')].filter(Boolean).join('\n');
  return {
    id: t.thread_id,
    href: '#thread/' + t.thread_id,
    title,
    untitled,
    people: untitled ? plural(ids.length, 'participant') : peopleLine(ids.filter(p => p !== me), nameOf),
    // The peers `people` names, for per-name peer links; none when people
    // is a stand-in title's plain participant count.
    ids: untitled ? [] : ids.filter(p => p !== me),
    count: count === null ? '' : String(count),
    unread,
    unreadText: unreadLabel(unread),
    active: ago(r.last_message_at, now),
    tip,
  };
}

// yourThreads is the operator's own threads, in the hub's order (newest
// activity first). page is a GET /v1/admin/threads?participant=<me> page,
// whose ThreadRows carry last_message_at; until it arrives, summaries (the
// shell's GET /v1/threads list) stand in without times. error shows only
// when neither has loaded; rows already shown stay through a failed reload.
// unread is the inbox head's unread_by_thread, so the rows' counts sum to the
// inbox badge over the threads listed.
export function yourThreads({ page, summaries, error, me, nameOf, now, unread }) {
  const src = page ? page.threads || [] : summaries ? summaries.map(thread => ({ thread, last_message_at: '' })) : null;
  if (!src) return error ? { state: 'error', note: 'Threads unavailable', rows: [] } : { state: 'loading', note: '…', rows: [] };
  const rows = src.filter(r => r && r.thread && !r.archived_at).map(r => threadRow(r, me, nameOf, now, unreadOf(unread, r.thread.thread_id)));
  return rows.length ? { state: 'ok', note: '', rows } : { state: 'empty', note: 'No threads yet', rows };
}

// otherThreads turns a GET /v1/admin/threads page (ThreadRows, already
// filtered by the hub to threads `me` is not in) into sidebar rows. It drops
// anything Your Threads lists or `me` is in, in case the two snapshots were
// read at different moments. state is loading, error, empty or ok; note is
// the placeholder text; more is '' (all shown), 'show' (another page) or
// 'threads' (past the cap).
export function otherThreads({ page, error, mine, me, nameOf, now, limit = OTHER_PAGE }) {
  if (!page) {
    return error ? { state: 'error', note: 'Other threads unavailable', rows: [], more: '' } : { state: 'loading', note: '…', rows: [], more: '' };
  }
  const listed = new Set((mine || []).map(t => t.thread_id));
  const kept = (page.threads || [])
    .filter(r => r && r.thread && !r.archived_at && !listed.has(r.thread.thread_id) && !(r.thread.participants || []).includes(me))
    .sort(byActivity);
  const rows = kept.slice(0, limit).map(r => threadRow(r, me, nameOf, now));
  const cut = kept.length > limit || !!page.next;
  const more = !cut ? '' : limit >= OTHER_MAX ? 'threads' : 'show';
  if (!rows.length) return { state: 'empty', note: 'No other threads', rows, more };
  return { state: 'ok', note: '', rows, more };
}


export function disclosureOpen(storage, section) {
 try { return storage?.getItem('vecnet.ui.sidebar.' + section) !== 'closed'; } catch { return true; }
}
export function saveDisclosure(storage, section, open) {
 try { storage?.setItem('vecnet.ui.sidebar.' + section, open ? 'open' : 'closed'); } catch {}
}
