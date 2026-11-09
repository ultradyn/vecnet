// Small DOM fixture for real dashboard/component lifecycle tests; no renderer mocks.
export class El {
  constructor(tag, ns = null) { this.tag = tag; this.namespaceURI = ns; this.attrs = {}; this.kids = []; this.on = {}; this.className = ''; this.hidden = false; this.parentNode = null; }
  get childNodes() { return this.kids; }
  get children() { return this.kids.filter(k => k instanceof El); }
  set textContent(v) { this.replaceChildren(String(v)); }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'class') this.className = String(v); }
  getAttribute(k) { return this.attrs[k] ?? null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(type, fn) { this.on[type] = fn; }
  append(...xs) { for (const x of xs.flat()) { if (x == null || x === false) continue; if (x.parentNode) x.remove(); if (x instanceof El) x.parentNode = this; this.kids.push(x); } }
  replaceChildren(...xs) { for (const k of this.children) k.parentNode = null; this.kids = []; this.append(...xs); }
  insertBefore(node, ref) { if (node === ref) return; node.remove(); const i = this.kids.indexOf(ref); this.kids.splice(i < 0 ? this.kids.length : i, 0, node); node.parentNode = this; }
  remove() { if (this.parentNode) { const a = this.parentNode.kids; a.splice(a.indexOf(this), 1); this.parentNode = null; } }
  focus() { document.activeElement = this; }
  getBoundingClientRect() { return { left: 0, width: 240 }; }
  querySelector(s) { return this.querySelectorAll(s)[0] || null; }
  querySelectorAll(s) { return this.children.flatMap(k => [...(s[0] === '.' ? k.className.split(/\s+/).includes(s.slice(1)) : s[0] === '#' ? k.attrs.id === s.slice(1) : k.tag === s) ? [k] : [], ...k.querySelectorAll(s)]); }
}
export function installDom() {
  const previous = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag), createElementNS: (ns, tag) => new El(tag, ns), activeElement: null };
  return () => { globalThis.document = previous; };
}
export function snapshot(peers = { peer_a: [1, 3] }) {
  const entries = Object.fromEntries(Object.entries(peers).map(([id, values]) => { const buckets = Array.from({ length: 60 }, (_, i) => values[i] || 0), messages = buckets.reduce((a, b) => a + b, 0); return [id, { messages, messages_per_minute: messages / 60, buckets }]; }));
  const buckets = Array.from({ length: 60 }, (_, i) => Object.values(entries).reduce((sum, p) => sum + p.buckets[i], 0)), messages = buckets.reduce((a, b) => a + b, 0);
  return { now: '2026-11-09T12:00:30.000Z', window_start: '2026-11-09T11:00:30.000Z', window_end: '2026-11-09T12:00:30.000Z', bucket_seconds: 60, messages, messages_per_minute: messages / 60, active_peers: Object.entries(entries).filter(([id, p]) => id.startsWith('peer_') && p.messages).length, buckets, peers: entries };
}
export const peer = (id, name = id) => ({ peer: { peer_id: id, name, kind: 'session', presence: 'unknown', reachability: 'unknown' }, routes: [] });
