// Placeholder inbox (operator UI Task 8 builds the real page). It already calls
// the subscription and presence slots, so those tasks can test through it.
import { h } from './dom.js';
import * as presence from './presence.js';
import * as subs from './subs.js';

export function mount(root, ctx) {
  const inbox = ctx.inboxHead;
  const attention = h('div', { class: 'attention' });
  const body = h('div');
  const render = () => {
    const value = inbox.get(), err = inbox.error();
    subs.renderAttention(attention, value ? value.attention : [], ctx);
    if (value) {
      body.replaceChildren(h('p', { class: 'lede', text: value.unread + ' unread' }));
    } else if (err) {
      body.replaceChildren(h('div', { class: 'empty error' }, h('b', { text: 'Inbox unavailable' }), err.message));
    } else {
      body.replaceChildren(h('div', { class: 'loading', text: 'Loading your inbox…' }));
    }
  };
  root.replaceChildren(h('h1', { text: 'Inbox' }), attention, body);
  render();
  return inbox.subscribe(render);
}

export function mountAside(root, ctx) {
  const inbox = ctx.inboxHead;
  const fallback = { peer_id: ctx.me, name: ctx.nameOf(ctx.me), presence: 'unknown', presence_at: null };
  const render = () => presence.renderAside(root, (inbox.get() || {}).me || fallback, ctx);
  render();
  return inbox.subscribe(render);
}
