// Placeholder dashboard (operator UI Task 12 builds it). The dashboard never
// writes; the subscription slot is here so Task 13 can test through it.
import { h } from './dom.js';
import * as subs from './subs.js';

export function mount(root, ctx) {
  const section = h('section', { class: 'subs' });
  root.replaceChildren(h('h1', { text: 'Dashboard' }), section);
  subs.renderSection(section, ctx);
  return () => {};
}
