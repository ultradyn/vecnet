// Server-filtered cursor list. One generation owns all in-flight work.
export function threadQuery(filters = {}, me, cursor) {
 const q = { limit: 50 };
 if (filters.state !== 'all') q.archived = filters.state === 'archived';
 if (filters.q) q.q = filters.q;
 if (filters.mine && me) q.participant = me;
 else if (filters.peer) q.participant = filters.peer;
 if (cursor) q.cursor = cursor;
 return q;
}
export function createThreadList(api, me, changed = () => {}) {
 let generation = 0, alive = true, filters = {}, value = { rows: [], next: null, loading: false, error: null };
 async function request(append) {
  const token = ++generation;
  value = { ...value, loading: true, error: null }; changed();
  try {
   const page = await api.threads(threadQuery(filters, me, append ? value.next : null));
   if (!alive || token !== generation) return;
   const rows = [...(append ? value.rows : []), ...(page.threads || [])];
   value = { rows: [...new Map(rows.map(r => [r.thread.thread_id, r])).values()], next: page.next || null, loading: false, error: null };
  } catch (e) { if (alive && token === generation) value = { ...value, loading: false, error: e.message || String(e) }; }
  if (alive && token === generation) changed();
 }
 return { state: () => value, load: next => { filters = { ...next }; return request(false); }, more: () => value.next && !value.loading ? request(true) : Promise.resolve(), close: () => { alive = false; ++generation; } };
}
export async function bulkArchive(api, ids, archive) {
 const failures = [];
 for (const id of ids) try { await (archive ? api.archiveThread(id) : api.unarchiveThread(id)); } catch (e) { failures.push({ id, error: e.message || String(e) }); }
 return failures;
}


export function participantID(value, directory) {
 const text = value.trim();
 if (!text) return '';
 const peer = (directory || []).find(p => p.peer_id === text || p.name === text);
 return peer?.peer_id || null;
}
