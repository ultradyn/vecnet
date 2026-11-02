// Placeholder for a new message (operator UI Task 15 builds it).
import { h } from './dom.js';

export function mount(root) {
  root.replaceChildren(h('h1', { text: 'New message' }), h('p', { class: 'lede', text: 'Not available yet.' }));
  return () => {};
}
