// Stubs: operator UI Task 14 fills these in. renderNav may return an unsubscribe
// function, which app.js calls on sign-out.
import { h } from './dom.js';

export function renderNav(el, ctx) {
  const render = () => {
    const entry = ((ctx.peers.get() || {}).peers || []).find(p => p.peer_id === ctx.me);
    const state = entry ? entry.presence : '';
    el.replaceChildren(
      h('span', { class: 'dot ' + state, title: state }),
      h('span', { text: ctx.nameOf(ctx.me) + (state ? ' · ' + state : '') }));
  };
  render();
  return ctx.peers.subscribe(render);
}

export function renderAside(el, _me, _ctx) { el.replaceChildren(); }

export function startHeartbeat(_ctx) { return () => {}; }
