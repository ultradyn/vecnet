// Hub notice and restart banners (hub notices Task 9), in the #notices slot
// (role="status" aria-live="polite"). Text goes in through dom.h only, so as
// textContent. Dismiss hides a notice id for this browser session; the hub
// keeps it.
import { h } from './dom.js';
import { bannerItems, restartLine, restartState } from './notice-model.js';

const KEY = 'vecnet.ui.dismissed';

// renderBanner updates #notices in place: a notice row is rebuilt only when
// its notice changes, and a tick rewrites only the countdown and the relative
// times. Unchanged rows stay the same nodes, so a focused dismiss button or
// link keeps focus, and the live region announces only what is new.
export function renderBanner(root, { items = [], restart, now = Date.now(), onDismiss = () => {} } = {}) {
  const st = root.__banner ??= { rows: new Map(), restart: null, order: [] };
  st.onDismiss = onDismiss;
  const keep = new Map(), order = [];
  const line = restartLine(restart, now);
  if (line) {
    let r = st.restart;
    if (!r || r.tone !== line.tone || r.lead !== line.lead) {
      // The countdown is aria-hidden: the live region reads the lead once.
      const tail = h('span', { 'aria-hidden': 'true', text: line.tail });
      r = { tone: line.tone, lead: line.lead, tail, tailText: line.tail,
        el: h('div', { class: 'ntc ' + line.tone }, h('div', { class: 'nb' }, h('i'), h('span', { class: 'tx' }, line.lead, tail))) };
    } else if (r.tailText !== line.tail) {
      r.tail.textContent = r.tailText = line.tail;
    }
    st.restart = r;
    order.push(r.el);
  } else st.restart = null;
  for (const it of items) {
    const sig = [it.kind, it.label, it.host, it.url, it.text, it.abs].join('\u0000');
    let row = st.rows.get(it.id);
    if (!row || row.sig !== sig) row = noticeRow(it, sig, id => st.onDismiss(id));
    else if (row.relText !== it.rel && row.rel) row.rel.textContent = row.relText = it.rel;
    keep.set(it.id, row);
    order.push(row.el);
  }
  st.rows = keep;
  if (order.length !== st.order.length || order.some((el, i) => el !== st.order[i])) {
    // Re-inserting a node drops its focus: put it back if it survived.
    const doc = globalThis.document, active = doc && doc.activeElement;
    const refocus = active && order.some(el => el !== active && el.contains && el.contains(active)) ? active : null;
    root.replaceChildren(...order);
    if (refocus && refocus.focus) refocus.focus();
    st.order = order;
  }
  root.hidden = !order.length;
}

function noticeRow(it, sig, onDismiss) {
  // The relative time is aria-hidden; the clock time says the same for
  // screen readers and does not change every minute.
  const rel = it.rel ? h('span', { 'aria-hidden': 'true', text: it.rel }) : null;
  const el = h('div', { class: 'ntc k-' + it.kind, title: it.text },
    h('div', { class: 'nb' },
      h('span', { class: 'chip lb', text: it.label }),
      it.when ? h('span', { class: 'wh' }, rel, rel ? h('span', { 'aria-hidden': 'true', text: ' · ' }) : null, it.abs) : null,
      it.host ? h('span', { class: 'to' }, '→\u00a0', h('a', { href: it.url, rel: 'noopener noreferrer', target: '_blank', text: it.host })) : null,
      h('span', { class: 'tx', text: it.text })),
    h('button', { type: 'button', class: 'x', 'aria-label': 'Dismiss notice', title: 'Hide for this session', text: '×', onclick: () => onDismiss(it.id) }));
  return { el, sig, rel, relText: it.rel };
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
