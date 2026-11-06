// Sidebar search, phase A (CoS ruling): peer names are matched here, over the
// directory the shell already holds; thread titles are matched by the hub,
// GET /v1/threads?q= (PROTOCOL §2.8). Titles are never filtered over admin
// pages in the browser: a page holds only the newest threads, so a client
// filter would say "no results" for a thread one page further back. Message
// text is not searched. No DOM: covered by node.
import { threadRow, unreadOf } from './sidebar-model.js';

// The pause after the last keystroke before the hub is asked.
export const DEBOUNCE_MS = 250;
// The hub refuses a longer q with 422 invalid_value (§2.8); it counts
// characters (code points), not UTF-16 units.
export const Q_MAX = 128;
// Threads per request; More results asks for the next page with `cursor`.
export const SEARCH_PAGE = 50;
// Peer matches shown before "N more".
export const PEERS_SHOWN = 8;

const str = v => (typeof v === 'string' ? v : '');
const PEER = /^peer_[A-Za-z0-9]+$/;

// normQuery is what a search box's text searches for: surrounding spaces
// dropped.
export function normQuery(text) {
  return str(text).trim();
}

export function tooLong(q) {
  return [...str(q)].length > Q_MAX;
}

// matchPeers is the directory's peers whose name contains q, ignoring case:
// an exact name first, then names that start with q, then the rest, each by
// name. An empty q is the whole directory. directory is the GET /v1/peers
// reply or its array.
export function matchPeers(directory, q) {
  const list = Array.isArray(directory) ? directory : directory && Array.isArray(directory.peers) ? directory.peers : [];
  const needle = normQuery(q).toLowerCase();
  const ranked = [];
  for (const p of list) {
    if (!p || !str(p.peer_id)) continue;
    const name = str(p.name).toLowerCase();
    const at = needle ? name.indexOf(needle) : 0;
    if (at < 0) continue;
    ranked.push({ p, name, rank: !needle ? 0 : name === needle ? 0 : at === 0 ? 1 : 2 });
  }
  ranked.sort((a, b) => a.rank - b.rank || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0) || (a.p.peer_id < b.p.peer_id ? -1 : 1));
  return ranked.map(r => r.p);
}

// peerResults is the Peers group of the search results: rows to show, how
// many more matched, and the placeholder when none did. A row can pick the
// peer as the threads' participant (`pick`) unless it is the operator, whose
// threads are the default scope, or a directory source, which is never a
// participant.
export function peerResults({ directory, text, me, shown = PEERS_SHOWN }) {
  const q = normQuery(text);
  if (!directory) return { state: 'loading', note: '…', rows: [], more: 0 };
  const all = matchPeers(directory, q);
  const rows = all.slice(0, shown).map(p => {
    const id = p.peer_id;
    return { id, name: str(p.name) || id, kind: str(p.kind), me: id === me, pick: id !== me && PEER.test(id) };
  });
  if (!rows.length) return { state: 'empty', note: q ? 'No peer name contains “' + q + '”' : 'No peers', rows, more: 0 };
  return { state: 'ok', note: '', rows, more: all.length - rows.length };
}

// threadResults is the Threads group: the hub's matches as sidebar rows, in
// the hub's order and unfiltered here. heading names the scope the hub
// searched: the operator's own threads, or one peer's. unread is the inbox
// head's unread_by_thread, as Your Threads shows it.
export function threadResults(st, { me, nameOf, now, unread }) {
  const heading = st.participant ? 'Threads with ' + nameOf(st.participant) : 'Your Threads';
  const base = { heading, rows: [], more: false, retry: false };
  if (st.tooLong) return { ...base, state: 'error', note: 'Title search takes at most ' + Q_MAX + ' characters' };
  // A query still waiting out the debounce shows nothing of the last one.
  if (st.pending) return { ...base, state: 'loading', note: 'Searching…' };
  const page = st.page;
  const rows = page ? page.threads.filter(t => t && str(t.thread_id)).map(t => threadRow({ thread: t, last_message_at: str(t.last_message_at) }, me, nameOf, now, unreadOf(unread, t.thread_id))) : [];
  if (st.error) {
    const e = st.error;
    const refused = e.code === 'invalid_value' && e.details && e.details.field === 'q';
    return { ...base, state: 'error', note: (refused ? 'The hub refused this search: ' : 'Search failed: ') + (e.message || 'error'), rows, retry: true };
  }
  if (!page) return { ...base, state: 'loading', note: 'Searching…' };
  const more = !!page.next;
  if (st.loading) return { ...base, state: 'loading', note: 'Loading more…', rows };
  if (!rows.length) return { ...base, state: 'empty', note: st.q ? 'No thread title contains “' + st.q + '”' : 'No threads', more };
  return { ...base, state: 'ok', note: '', rows, more };
}

// createSearch holds one search box's state and its requests to the hub.
// setText debounces: the hub is asked once typing pauses for `delay` ms,
// and a newer query cancels an older request (its late answer is dropped).
// Clearing the text is immediate and asks nothing. A q past Q_MAX is never
// sent. pick(participant) narrows thread results to a peer's threads at once.
// api.searchThreads is GET /v1/threads (api.js). Timers are injectable for
// tests.
export function createSearch({ api, delay = DEBOUNCE_MS, limit = SEARCH_PAGE, setTimer = setTimeout, clearTimer = clearTimeout }) {
  let text = '', q = '', participant = '';
  let page = null, error = null, loading = false, timer = null, ctl = null, seq = 0;
  const subs = new Set();
  const notify = () => { for (const fn of [...subs]) fn(); };
  const searching = () => (q !== '' || participant !== '') && !tooLong(q);

  function unschedule() {
    if (timer !== null) { clearTimer(timer); timer = null; }
  }
  function cancel() {
    seq++;
    if (ctl) { ctl.abort(); ctl = null; }
    loading = false;
  }
  function load(cursor) {
    cancel();
    const mine = seq, c = new AbortController();
    ctl = c;
    loading = true;
    error = null;
    if (!cursor) page = null;
    api.searchThreads({ q, participant, limit, cursor: cursor || undefined, signal: c.signal }).then(reply => {
      if (mine !== seq) return;
      ctl = null;
      loading = false;
      const got = reply && Array.isArray(reply.threads) ? reply.threads : [];
      page = { threads: cursor && page ? [...page.threads, ...got] : got, next: (reply && str(reply.next)) || null };
      notify();
    }, e => {
      if (mine !== seq) return;
      ctl = null;
      loading = false;
      error = e || new Error('search failed');
      notify();
    });
    notify();
  }
  function apply() {
    timer = null;
    q = normQuery(text);
    if (searching()) { load(''); return; }
    cancel();
    page = null;
    error = null;
    notify();
  }

  return {
    setText(next) {
      text = str(next);
      const want = normQuery(text);
      unschedule();
      // Unchanged, or typed back to the query already shown: nothing to ask.
      if (want === q && (page || loading || error || !searching())) { notify(); return; }
      // Clearing, or a q the hub would refuse: no reason to wait.
      if (want === '' || tooLong(want)) { apply(); return; }
      timer = setTimer(apply, delay);
      notify();
    },
    pick(id) {
      participant = str(id);
      unschedule();
      apply();
    },
    // clear is Esc and the clear button: the unfiltered sidebar again.
    clear() {
      unschedule();
      cancel();
      text = q = participant = '';
      page = null;
      error = null;
      notify();
    },
    more() {
      if (!page || !page.next || loading || timer !== null) return;
      load(page.next);
    },
    // retry asks again from the first page.
    retry() {
      unschedule();
      apply();
    },
    // open: the box has a query or a participant, so the sidebar shows
    // results in place of its thread lists. pending: a typed query is
    // waiting out the debounce.
    state() {
      return { text, q, participant, page, error, loading, pending: timer !== null, tooLong: tooLong(q), open: normQuery(text) !== '' || participant !== '' };
    },
    subscribe(fn) { subs.add(fn); return () => subs.delete(fn); },
    close() { unschedule(); cancel(); subs.clear(); },
  };
}
