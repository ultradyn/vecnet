// Subscription health (PROTOCOL §2.13, §13.4). The dashboard section only
// reads; the inbox's attention list is where Resume lives, and only for items
// the hub marks `action: "resume"` (Review Focus 5). Every hub-supplied string
// becomes text through h().
import { h } from './dom.js';
import { ago, short, when } from './format.js';
import { chip, expiresIn } from './subs-model.js';

const TICK = 15000;
const COLUMNS = ['Peer', 'Host', 'Health', 'Expires', 'Last refresh', 'Last delivery', 'Attempts', 'Cursor'];

// One subscriptions view per store, so remounting the dashboard reuses it.
const resources = new WeakMap();
function subsResource(ctx) {
  let r = resources.get(ctx.store);
  if (!r) {
    r = ctx.store.resource('subs', () => ctx.api.subscriptions());
    resources.set(ctx.store, r);
  }
  return r;
}

// reloadShared reloads the shared subscriptions view, for a page that also
// shows it and refreshes on its own schedule (the dashboard).
export async function reloadShared(ctx) {
  const r = subsResource(ctx);
  await r.reload();
  return true;
}

const hubNow = ctx => (ctx.store && ctx.store.hubNow ? ctx.store.hubNow() : Date.now());
const dash = text => text || '—';

function healthChip(health) {
  const c = chip(health);
  return h('span', { class: 'chip health h-' + c.tone, 'data-health': health || '', text: c.label });
}

function deliveryCell(s, now) {
  const status = s.last_status;
  const tone = status === null || status === undefined ? '' : status >= 200 && status < 300 ? ' good' : ' fail';
  return h('td', { class: 'dl', title: when(s.last_delivery_at, now) || null },
    h('span', { class: 'status' + tone, text: status === null || status === undefined ? '—' : String(status) }),
    s.last_delivery_at ? h('span', { class: 'when', text: ' · ' + ago(s.last_delivery_at, now) }) : null);
}

function expiresTitle(s, now) {
  const parts = [];
  if (s.expires_at) parts.push('expires ' + when(s.expires_at, now));
  if (s.refresh_before) parts.push('refresh before ' + when(s.refresh_before, now));
  return parts.join(' · ') || null;
}

function row(s, ctx, now) {
  const ok = s.health === 'ok';
  const note = s.health_note || '';
  const tr = h('tr', { class: 'sub' + (ok ? '' : ' warn'), 'data-sub': s.sub_id, 'data-health': s.health || '', title: note || null },
    h('td', { class: 'peer', title: s.peer_id + ' · ' + s.sub_id, text: ctx.nameOf(s.peer_id) }),
    h('td', { class: 'host mono', text: dash(s.url) }),
    h('td', {}, healthChip(s.health)),
    h('td', { class: 'exp', title: expiresTitle(s, now), text: dash(expiresIn(s.expires_at, now)) }),
    h('td', { class: 'ref', title: when(s.last_refresh_at, now) || null, text: dash(ago(s.last_refresh_at, now)) }),
    deliveryCell(s, now),
    h('td', { class: 'num mono', text: String(s.attempts ?? 0) }),
    h('td', { class: 'cur mono', title: s.cursor || null, text: dash(s.cursor) }));
  if (ok || !note) return [tr];
  return [tr, h('tr', { class: 'note', 'data-sub': s.sub_id, title: note }, h('td', { colspan: String(COLUMNS.length), text: note }))];
}

// The live unsubscribe per section element, so a second renderSection on the
// same element replaces the first instead of stacking timers and listeners.
const sections = new WeakMap();

// renderSection fills el with the subscription table for the dashboard. It has
// no buttons: the dashboard performs no writes. It returns an unsubscribe;
// calling it again on the same element first stops the previous one.
export function renderSection(el, ctx) {
  const prev = sections.get(el);
  if (prev) prev();
  const subs = subsResource(ctx);
  const draw = () => {
    if (!el.isConnected && el.childNodes.length) return;
    const list = subs.get(), err = subs.error(), now = hubNow(ctx);
    const head = h('h2', {}, 'Subscriptions', Array.isArray(list) ? h('span', { class: 'n', text: String(list.length) }) : null);
    let body;
    if (err && !Array.isArray(list)) {
      body = h('div', { class: 'empty error' }, h('b', { text: 'Subscriptions unavailable' }), err.message);
    } else if (!Array.isArray(list)) {
      body = h('div', { class: 'loading', text: 'Loading subscriptions…' });
    } else if (!list.length) {
      body = h('div', { class: 'empty' }, h('b', { text: 'No subscriptions' }),
        'A dot appears here once it subscribes to vecnet.message for its peer.');
    } else {
      body = h('div', { class: 'subs-scroll' }, h('table', { class: 'subs-table' },
        h('thead', {}, h('tr', {}, COLUMNS.map(c => h('th', { scope: 'col', text: c })))),
        h('tbody', {}, list.flatMap(s => row(s, ctx, now)))));
    }
    el.replaceChildren(head, body);
  };
  const offs = [subs.subscribe(draw)];
  // The hub's clock arrives with the inbox snapshot; redraw when it moves.
  if (ctx.inboxHead) offs.push(ctx.inboxHead.subscribe(draw));
  const tick = setInterval(() => { if (el.isConnected) draw(); else off(); }, TICK);
  function off() {
    clearInterval(tick);
    for (const fn of offs.splice(0)) fn();
    if (sections.get(el) === off) sections.delete(el);
  }
  sections.set(el, off);
  draw();
  subs.reload();
  return off;
}

// Per-element state, because the inbox redraws the list on every snapshot and
// a pending or failed Resume must survive that.
const states = new WeakMap();

function attentionItem(item, state) {
  const id = item.id;
  const pending = state.pending.has(id), resumed = state.resumed.has(id);
  const failed = state.failed.get(id);
  let action = null;
  if (item.action === 'resume') {
    action = h('button', {
      type: 'button', class: 'btn resume', disabled: pending || resumed,
      'data-sub': id, onclick: () => resume(id, state),
      text: pending ? 'Resuming…' : resumed ? 'Resumed' : 'Resume',
    });
  }
  return h('div', { class: 'attn-item', 'data-sub': id, 'data-health': item.health || '' },
    healthChip(item.health),
    h('div', { class: 'body' },
      h('div', { class: 'sum', text: item.summary || '' }),
      h('div', { class: 'meta', title: id }, (item.kind || 'subscription') + ' · ' + short(id) + (item.at ? ' · ' + ago(item.at, hubNow(state.ctx)) : '')),
      failed ? h('p', { class: 'err', role: 'alert', text: 'Resume failed: ' + failed }) : null),
    action);
}

function draw(state) {
  const el = state.el, items = state.items;
  if (!items.length) { el.replaceChildren(); return; }
  el.replaceChildren(h('section', { class: 'attn', 'aria-label': 'Subscriptions' },
    // The inbox's heading convention: a title span first, then the .n count.
    h('h2', {}, h('span', { text: 'Subscriptions' }), h('span', { class: 'n', text: String(items.length) })),
    items.map(item => attentionItem(item, state))));
}

async function resume(id, state) {
  if (state.pending.has(id) || state.resumed.has(id)) return;
  const ctx = state.ctx;
  state.pending.add(id);
  state.failed.delete(id);
  draw(state);
  try {
    await ctx.api.resumeSubscription(id);
    state.pending.delete(id);
    // Stays disabled until the next snapshot shows the hub's answer.
    state.resumed.add(id);
    draw(state);
    if (ctx.toast) ctx.toast('Subscription resumed');
    ctx.store.invalidate('inbox', 'subs');
  } catch (e) {
    state.pending.delete(id);
    state.failed.set(id, (e && e.message) || 'unknown error');
    draw(state);
    if (ctx.toast) ctx.toast('Resume failed: ' + ((e && e.message) || 'unknown error'), true);
    // The item may be gone already; a fresh snapshot settles it.
    if (e && e.status === 404) ctx.store.invalidate('inbox', 'subs');
  }
}

// renderAttention lists the inbox's subscription attention items under
// "Subscriptions", with Resume only where the hub offers it.
export function renderAttention(el, items, ctx) {
  let state = states.get(el);
  if (!state) {
    state = { el, ctx, items: [], pending: new Set(), failed: new Map(), resumed: new Set() };
    states.set(el, state);
  }
  const list = Array.isArray(items) ? items.filter(i => i && typeof i === 'object') : [];
  // A new snapshot carries the hub's answer, so "Resumed" gives way to it.
  if (items !== state.raw) state.resumed.clear();
  state.raw = items;
  state.ctx = ctx;
  state.items = list;
  const live = new Set(list.map(i => i.id));
  for (const id of [...state.failed.keys()]) if (!live.has(id)) state.failed.delete(id);
  draw(state);
}
