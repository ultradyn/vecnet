// Hub notice and restart banners (hub notices Task 9), in the #notices slot
// (role="status" aria-live="polite"). Text goes in through dom.h only, so as
// textContent. Dismiss hides a notice id for this browser session; the hub
// keeps it.
import { h } from './dom.js';
import { bannerItems, restartLine, restartState } from './notice-model.js';

const KEY = 'vecnet.ui.dismissed';

export function renderBanner(root, { items = [], restart, now = Date.now(), onDismiss = () => {} } = {}) {
  const rows = items.map(it => h('div', { class: 'ntc k-' + it.kind, title: it.text },
    h('div', { class: 'nb' },
      h('span', { class: 'lb', text: it.label }),
      it.when ? h('span', { class: 'wh', text: it.when }) : null,
      it.host ? h('span', { class: 'to' }, '→ ', h('a', { href: it.url, rel: 'noopener noreferrer', target: '_blank', text: it.host })) : null,
      h('span', { class: 'tx', text: it.text })),
    h('button', { type: 'button', class: 'x', 'aria-label': 'Dismiss notice', title: 'Hide for this session', text: '×', onclick: () => onDismiss(it.id) })));
  const line = restartLine(restart, now);
  if (line) rows.unshift(h('div', { class: 'ntc ' + line.tone }, h('div', { class: 'nb' }, h('i'), h('span', { class: 'tx', text: line.text }))));
  root.replaceChildren(...rows);
  root.hidden = !rows.length;
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
