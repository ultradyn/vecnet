// Pure helpers for the operator activity feed (GET /v1/admin/activity,
// PROTOCOL §2.13). No DOM, no network: covered by node.

// createSSEParser returns feed(text): it takes response text in any chunking
// and calls onFrame({ event, data }) once per complete frame. Comment lines
// (heartbeats) and frames with no fields are skipped; data is the raw text.
export function createSSEParser(onFrame) {
  let buf = '', event = '', data = null;
  const line = l => {
    if (l === '') {
      if (event || data !== null) onFrame({ event: event || 'message', data: data ?? '' });
      event = ''; data = null;
      return;
    }
    if (l[0] === ':') return;
    const i = l.indexOf(':');
    const name = i < 0 ? l : l.slice(0, i);
    let value = i < 0 ? '' : l.slice(i + 1);
    if (value[0] === ' ') value = value.slice(1);
    if (name === 'event') event = value;
    else if (name === 'data') data = data === null ? value : data + '\n' + value;
  };
  return text => {
    buf += text;
    let at;
    while ((at = buf.search(/\r\n|\r|\n/)) >= 0) {
      const sep = buf[at] === '\r' && buf[at + 1] === '\n' ? 2 : 1;
      if (buf[at] === '\r' && at + 1 === buf.length) break; // a \r\n split across chunks
      line(buf.slice(0, at));
      buf = buf.slice(at + sep);
    }
  };
}

// The order every thread list uses: last activity, then id, newest first.
function byActivity(a, b) {
  const at = a.last_message_at || '', bt = b.last_message_at || '';
  if (at !== bt) return at < bt ? 1 : -1;
  const ai = (a.thread || {}).thread_id || '', bi = (b.thread || {}).thread_id || '';
  return ai < bi ? 1 : ai > bi ? -1 : 0;
}

// applyActivity folds one feed entry into a thread page ({threads:
// [ThreadRow…], …}). known is false when the page has no row for the thread,
// or when the entry is a thread change (a rename) the page cannot apply:
// the caller refetches. A message bumps the row's count and last activity
// and re-sorts, unless the row already shows a message that recent (a
// refetch that raced the entry). page is a new object only when it changed.
export function applyActivity(page, ev) {
  const rows = (page && page.threads) || [];
  const id = ev && ev.data && ev.data.thread_id;
  const i = id ? rows.findIndex(r => r && r.thread && r.thread.thread_id === id) : -1;
  if (i < 0 || ev.type !== 'message') return { page, known: false, changed: false };
  const row = rows[i], at = ev.data.at || '';
  if (!at || (row.last_message_at || '') >= at) return { page, known: true, changed: false };
  const next = { ...row, last_message_at: at, thread: { ...row.thread, message_count: (row.thread.message_count || 0) + 1 } };
  const threads = rows.slice();
  threads[i] = next;
  threads.sort(byActivity);
  return { page: { ...page, threads }, known: true, changed: true };
}

// activityAction is what a thread list does with one feed event: { reload:
// true } (connect, resync, a thread it cannot update), { page } (the updated
// page to set), or null (nothing). skip holds thread ids the list leaves to
// other views: Other Threads skips Your Threads, which delivery Events keep
// current.
export function activityAction(page, ev, skip = new Set()) {
  if (!ev) return null;
  if (ev.type === 'ready' || ev.type === 'resync') return { reload: true };
  const id = ev.data && ev.data.thread_id;
  if (!id || skip.has(id)) return null;
  if (ev.type === 'thread') {
    const listed = ((page && page.threads) || []).some(r => r && r.thread && r.thread.thread_id === id);
    return listed ? { reload: true } : null;
  }
  if (ev.type !== 'message') return null;
  const r = applyActivity(page, ev);
  if (!r.known) return { reload: true };
  return r.changed ? { page: r.page } : null;
}
