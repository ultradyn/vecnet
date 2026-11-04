// Pure helpers for the sidebar's Other Threads list. No DOM: covered by node.
import { ago, fullWhen, plural } from './format.js';

// One page of other threads, and the hub's page cap (PROTOCOL §2.13). Past
// the cap the dashboard lists every thread.
export const OTHER_PAGE = 50;
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

// otherThreads turns a GET /v1/admin/threads page (ThreadRows, already
// filtered by the hub to threads `me` is not in) into sidebar rows. It drops
// anything Your Threads lists or `me` is in, in case the two snapshots were
// read at different moments. state is loading, error, empty or ok; note is
// the placeholder text; more is '' (all shown), 'show' (another page) or
// 'dash' (past the cap). Peer strings stay strings: the view sets them as text.
export function otherThreads({ page, error, mine, me, nameOf, now, limit = OTHER_PAGE }) {
  if (!page) {
    return error ? { state: 'error', note: 'Other threads unavailable', rows: [], more: '' } : { state: 'loading', note: '…', rows: [], more: '' };
  }
  const listed = new Set((mine || []).map(t => t.thread_id));
  const kept = (page.threads || [])
    .filter(r => r && r.thread && !listed.has(r.thread.thread_id) && !(r.thread.participants || []).includes(me))
    .sort(byActivity);
  const rows = kept.slice(0, limit).map(r => {
    const t = r.thread, count = Number.isInteger(t.message_count) ? t.message_count : null;
    const title = t.title || 'Untitled thread';
    const all = (t.participants || []).map(nameOf).join(', ');
    const tip = [title, all, [count === null ? '' : plural(count, 'message'), r.last_message_at ? 'last active ' + fullWhen(r.last_message_at) : ''].filter(Boolean).join(' · ')].filter(Boolean).join('\n');
    return {
      id: t.thread_id,
      href: '#thread/' + t.thread_id,
      title,
      untitled: !t.title,
      people: peopleLine(t.participants, nameOf),
      count: count === null ? '' : String(count),
      active: ago(r.last_message_at, now),
      tip,
    };
  });
  const cut = kept.length > limit || !!page.next;
  const more = !cut ? '' : limit >= OTHER_MAX ? 'dash' : 'show';
  if (!rows.length) return { state: 'empty', note: 'No other threads', rows, more };
  return { state: 'ok', note: '', rows, more };
}
