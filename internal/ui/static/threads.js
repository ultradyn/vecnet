// Full operator thread browser; requests stay server-filtered and cursor-bounded.
import { h, chip, peerLink } from './dom.js';
import { ago, title } from './format.js';
import { bulkArchive, createThreadList } from './threads-model.js';
export function mount(root, ctx) {
 const params = ctx.query || new URLSearchParams();
 const filters = { q: params.get('q') || '', peer: params.get('peer') || '', state: ['all', 'archived'].includes(params.get('state')) ? params.get('state') : 'open', mine: params.get('mine') === '1' };
 let alive = true, timer, busy = false, failures = [], selected = new Set();
 const list = createThreadList(ctx.api, ctx.me, render);
 const search = h('input', { type: 'search', placeholder: 'Search thread titles', 'aria-label': 'Search thread titles', maxlength: 128, value: filters.q, oninput: e => { filters.q = e.target.value; clearTimeout(timer); timer = setTimeout(reload, 250); } });
 const directory = (ctx.peers?.get()?.peers || []);
 const participant = h('input', { type: 'search', list: 'thread-participants', placeholder: 'Any participant', 'aria-label': 'Participant', value: filters.peer, onchange: e => {
  const value = e.target.value.trim(); const peer = directory.find(p => p.peer_id === value || p.name === value); filters.peer = peer?.peer_id || value; reload();
 } });
 const options = h('datalist', { id: 'thread-participants' }, directory.map(p => h('option', { value: p.peer_id, label: p.name })));
 const state = h('select', { 'aria-label': 'Thread state', onchange: e => { filters.state = e.target.value; selected.clear(); reload(); } }, ['open', 'archived', 'all'].map(v => h('option', { value: v, selected: filters.state === v, text: v[0].toUpperCase() + v.slice(1) })));
 const mine = h('label', null, h('input', { type: 'checkbox', checked: filters.mine, onchange: e => { filters.mine = e.target.checked; reload(); } }), ' Only mine');
 const body = h('div', { class: 'thread-results' }), status = h('p', { role: 'status', class: 'note' });
 const archive = h('button', { class: 'btn', text: 'Archive selected', onclick: () => act([...selected], true) });
 const unarchive = h('button', { class: 'btn', text: 'Unarchive selected', onclick: () => act([...selected], false) });
 const all = h('input', { type: 'checkbox', 'aria-label': 'Select all loaded threads', onchange: e => { selected = e.target.checked ? new Set(list.state().rows.map(r => r.thread.thread_id)) : new Set(); render(); } });
 const more = h('button', { class: 'btn', text: 'Load more', onclick: () => list.more() });
 root.replaceChildren(h('div', { class: 'dash thread-browser' }, h('h1', { text: 'Threads' }), h('p', { class: 'lede', text: 'Browse conversations across the hub. Archiving tidies the operator view; peers can still read and post.' }), h('div', { class: 'browse-toolbar' }, search, participant, options, state, mine), h('div', { class: 'bulk-toolbar' }, h('label', null, all, ' Select loaded'), archive, unarchive), status, body, more));
 function url() { const q = new URLSearchParams(); for (const [k, v] of Object.entries(filters)) if (v && v !== 'open') q.set(k, v === true ? '1' : v); history.replaceState(null, '', '#threads' + (q.size ? '?' + q : '')); }
 function reload() { if (!alive) return; selected.clear(); all.checked = false; url(); return list.load(filters); }
 async function act(ids, archived) {
  if (busy || !ids.length || !window.confirm(`${archived ? 'Archive' : 'Unarchive'} ${ids.length} thread${ids.length === 1 ? '' : 's'}? ${archived ? 'Peers can still read and post. A new message reopens an archived thread.' : 'They will return to the open thread lists.'}`)) return;
  busy = true; failures = []; render();
  failures = await bulkArchive(ctx.api, ids, archived);
  if (!alive) return;
  busy = false; ctx.store.invalidate('threads'); await reload();
 }
 function render() {
  if (!alive) return;
  const v = list.state();
  archive.disabled = unarchive.disabled = busy || !selected.size;
  more.hidden = !v.next; more.disabled = v.loading || busy;
  status.textContent = [busy ? 'Updating threads…' : v.loading ? 'Loading threads…' : `${v.rows.length} threads loaded`, v.error, ...failures.map(f => `${f.id}: ${f.error}`)].filter(Boolean).join(' · ');
  body.replaceChildren(v.rows.length ? h('table', null, h('thead', null, h('tr', null, ['Select', 'Thread', 'Participants', 'Messages', 'Last activity', 'Action'].map(text => h('th', { text })))), h('tbody', null, v.rows.map(r => {
   const t = r.thread, id = t.thread_id;
   return h('tr', { 'data-thread-id': id }, h('td', null, h('input', { type: 'checkbox', checked: selected.has(id), 'aria-label': 'Select ' + (t.title || id), onchange: e => { if (e.target.checked) selected.add(id); else selected.delete(id); archive.disabled = unarchive.disabled = busy || !selected.size; } })),
    h('td', null, h('a', { href: '#thread/' + id, text: title(t, ctx.me, ctx.nameOf) }), r.archived_at ? [' ', chip(r.archived_by === 'hub' ? 'archived · auto' : 'archived')] : null),
    h('td', null, (t.participants || []).flatMap((id, i) => [i ? ', ' : '', peerLink(id, ctx.nameOf(id))])), h('td', { text: String(t.message_count || 0) }), h('td', { text: ago(r.last_message_at, ctx.store.hubNow()), title: r.last_message_at || '' }), h('td', null, h('button', { class: 'btn', disabled: busy, text: r.archived_at ? 'Unarchive' : 'Archive', onclick: () => act([id], !r.archived_at) })));
  }))) : h('p', { class: 'none', text: v.loading ? 'Loading…' : 'No threads match. Try another title, participant or state.' }));
 }
 const off = ctx.store.onTag ? ctx.store.onTag('threads', reload) : () => {};
 list.load(filters);
 return () => { alive = false; clearTimeout(timer); list.close(); off(); selected.clear(); };
}
