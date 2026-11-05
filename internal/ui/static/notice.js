// Hub notice and restart banners (hub notices Task 9), in the #notices slot
// (role="status" aria-live="polite"). Text goes in through dom.h only, so as
// textContent. Dismiss hides a notice id for this browser session; the hub
// keeps it.
import { h } from './dom.js';
import { bannerItems, restartLine, restartState } from './notice-model.js';

const KEY = 'vecnet.ui.dismissed';

// renderBanner updates in place: unchanged rows stay the same nodes (focus
// survives), and ticks touch only aria-hidden countdown and relative times.
export function renderBanner(root, { items = [], restart, now = Date.now(), onDismiss = () => {} } = {}) {
  const st = root.__banner ??= { rows: new Map(), order: [] }, rows = new Map(), order = [];
  st.onDismiss = onDismiss;
  const line = restartLine(restart, now);
  let r = line && st.restart;
  if (line && (!r || r.key !== line.tone + line.lead)) {
    const tail = h('span', { 'aria-hidden': 'true' });
    r = { key: line.tone + line.lead, tail, el: h('div', { class: 'ntc ' + line.tone }, h('div', { class: 'nb' }, h('i'), h('span', { class: 'tx' }, line.lead, tail))) };
  }
  if (r) { if (r.tail.textContent !== line.tail) r.tail.textContent = line.tail; order.push(r.el); }
  st.restart = r;
  for (const it of items) {
    const sig = [it.kind, it.host, it.url, it.text, it.abs].join('\n');
    let row = st.rows.get(it.id);
    if (!row || row.sig !== sig) row = noticeRow(it, sig, id => st.onDismiss(id));
    else if (row.rel && row.rel.textContent !== it.rel) row.rel.textContent = it.rel;
    rows.set(it.id, row);
    order.push(row.el);
  }
  st.rows = rows;
  if (order.length !== st.order.length || order.some((el, k) => el !== st.order[k])) {
    const a = globalThis.document && document.activeElement;
    const back = a && order.some(el => el !== a && el.contains && el.contains(a));
    root.replaceChildren(...order);
    if (back && a.focus) a.focus();
    st.order = order;
  }
  root.hidden = !order.length;
}

function noticeRow(it, sig, onDismiss) {
  const hid = text => h('span', { 'aria-hidden': 'true', text });
  const rel = it.rel ? hid(it.rel) : null;
  const el = h('div', { class: 'ntc k-' + it.kind, title: it.text },
    h('div', { class: 'nb' },
      h('span', { class: 'chip lb', text: it.label }),
      it.when ? h('span', { class: 'wh' }, rel, rel && hid(' · '), it.abs) : null,
      it.host ? h('span', { class: 'to' }, '→\u00a0', h('a', { href: it.url, rel: 'noopener noreferrer', target: '_blank', text: it.host })) : null,
      h('span', { class: 'tx', text: it.text })),
    h('button', { type: 'button', class: 'x', 'aria-label': 'Dismiss notice', title: 'Hide for this session', text: '×', onclick: () => onDismiss(it.id) }));
  return { el, sig, rel };
}

// createNotices loads `notices` at sign-in and on each hub_notice Event, and
// keeps the restart state; onPhase redraws #conn.
export function createNotices({ api, store, root, onPhase = () => {}, now = Date.now, storage = globalThis.sessionStorage }) {
  const res = store.resource('notices', () => api.notices());
  let restart = restartState(), timer = null, hidden;
  try { hidden = new Set(JSON.parse(storage.getItem(KEY)) || []); } catch { hidden = new Set(); }
  const dismiss = id => {
    hidden.add(id);
    try { storage.setItem(KEY, JSON.stringify([...hidden])); } catch { /* this page only */ }
    draw();
  };
  function draw() {
    const items = bannerItems((res.get() || {}).notices, store.hubNow()).filter(it => !hidden.has(it.id));
    renderBanner(root, { items, restart, now: now(), onDismiss: dismiss });
  }
  function feed(input) {
    const prev = restart;
    restart = restartState(restart, { ...input, at: now() });
    if (restart === prev && !input.tick) return;
    if (restart.phase !== 'restarting') { clearInterval(timer); timer = null; } else timer ??= setInterval(() => feed({ tick: 1 }), 1000);
    if (restart.phase !== prev.phase) onPhase(restart.phase);
    draw();
  }
  const minute = setInterval(draw, 60000); // relative times and expiry
  const off = res.subscribe(draw);
  return {
    phase: () => restart.phase,
    // activity feed events: { type: 'hub_restarting' | 'ready' | …, data }
    activity(ev) {
      if (ev && ev.type === 'hub_restarting') feed({ frame: 'hub_restarting', retryAfterMs: ev.data && ev.data.retry_after_ms });
      else if (ev && ev.type === 'ready') feed({ frame: 'ready' });
    },
    // store.onRestart: the long-poll's retry hint, or null for a plain answer.
    poll(ms) { feed(ms === null ? { frame: 'ready' } : { frame: 'hub_restarting', retryAfterMs: ms }); },
    close() { off(); clearInterval(timer); clearInterval(minute); root.replaceChildren(); root.hidden = true; },
  };
}
