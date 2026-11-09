import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import { createThumbBudget } from '../static/thumbs-budget.js';
import { createThumbCache, retryThumbCleanup } from '../static/thumbs-model.js';
import { createStore } from '../static/store.js';
import { DWELL_MS, BATCH_MS } from '../static/thread-read.js';

// Draft A only: roots 1–9,17,18,20. No fallback cache or transaction shim.
// A single canonical import preserves the document budget across mounts.
const nativeCreate = URL.createObjectURL, nativeRevoke = URL.revokeObjectURL;
let current = null;
URL.createObjectURL = blob => {
  assert.ok(current, 'URL creation outside an owned fixture');
  return current.createURL(blob);
};
URL.revokeObjectURL = url => {
  assert.ok(current, 'URL disposal outside an owned fixture');
  return current.revokeURL(url);
};
let mountThread, createThumbs, mountAside;
try {
  ({ mount: mountThread } = await import('../static/thread.js'));
  ({ createThumbs } = await import('../static/thumbs.js'));
  ({ mountAside } = await import('../static/thread-aside.js'));
} catch (error) {
  URL.createObjectURL = nativeCreate; URL.revokeObjectURL = nativeRevoke; throw error;
}
after(() => { URL.createObjectURL = nativeCreate; URL.revokeObjectURL = nativeRevoke; });
const BASE = { B: 16, C: 2, E: 8, N: 8, Q: 4, M: 4 };
const NOW = Date.parse('2026-11-09T10:00:00Z');
const sha = n => n.toString(16).padStart(64, '0');
const att = (n = 1, extra = {}) => ({ sha256: sha(n), size: 1, media_type: 'image/png', name: `image-${n}.png`, ...extra });
const msg = (n, attachments = [], extra = {}) => ({ message_id: `msg_${n}`, thread_id: 'thr_test', sender: 'peer_other', priority: 'hold', kind: 'message', recipients: [], created_at: '2026-11-09T09:00:00Z', content: { text: `body ${n}` }, attachments, ...extra });
const raster = (size = 1) => new Blob([new Uint8Array(size)], { type: 'image/png' });
const hostTurn = () => new Promise(resolve => setImmediate(resolve));
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function exactThrow(fn, value) {
  let threw = false, caught;
  try { fn(); } catch (error) { threw = true; caught = error; }
  assert.ok(threw, 'expected current throw, including falsy values'); assert.equal(caught, value);
}
const rect = (left = 0, top = 0, width = 240, height = 180) => ({ left, top, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON() { return { left, top, width, height }; } });
const overlap = (a, b) => rect(Math.max(a.left, b.left), Math.max(a.top, b.top), Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left)), Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)));

// Selector support follows the selectors used by the actual modules, with
// ancestor/child combinators, attributes and :not/:is rather than hash maps.
function splitOutside(text, separator) {
  const out = []; let depth = 0, quote = '', start = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quote) { if (c === quote && text[i - 1] !== '\\') quote = ''; continue; }
    if (c === '"' || c === "'") { quote = c; continue; }
    if (c === '[' || c === '(') depth++;
    if (c === ']' || c === ')') depth--;
    if (!depth && separator(c)) { if (i > start) out.push(text.slice(start, i)); start = i + 1; }
  }
  if (start < text.length) out.push(text.slice(start));
  return out.filter(Boolean);
}
function simple(el, selector, scope) {
  if (!(el instanceof ElementNode)) return false;
  let s = selector.trim(), yes = true;
  s = s.replace(/:(not|is)\(([^()]*)\)/g, (_, op, inner) => {
    const match = splitOutside(inner, c => c === ',').some(part => simple(el, part, scope));
    yes &&= op === 'not' ? !match : match; return '';
  });
  s = s.replace(/:(scope|focus|disabled|enabled|first-child|last-child)/g, (_, kind) => {
    yes &&= kind === 'scope' ? el === scope : kind === 'focus' ? el === el.env.doc.activeElement : kind === 'disabled' ? el.disabled : kind === 'enabled' ? !el.disabled : kind === 'first-child' ? el.parentElement?.firstElementChild === el : el.parentElement?.lastElementChild === el;
    return '';
  });
  s = s.replace(/\[([^\s~|^$*=\]]+)\s*(?:([~|^$*]?=)\s*["']?([^"'\]]*?)["']?)?\]/g, (_, key, op, value) => {
    const got = el.getAttribute(key);
    yes &&= !op ? got !== null : got !== null && (op === '=' ? got === value : op === '^=' ? got.startsWith(value) : op === '$=' ? got.endsWith(value) : op === '*=' ? got.includes(value) : op === '~=' ? got.split(/\s+/).includes(value) : got === value || got.startsWith(value + '-'));
    return '';
  });
  s = s.replace(/#([\w-]+)/g, (_, id) => { yes &&= el.id === id; return ''; });
  s = s.replace(/\.([\w-]+)/g, (_, name) => { yes &&= el.classList.contains(name); return ''; });
  return yes && (!s || s === '*' || s.toLowerCase() === el.localName);
}
function matches(el, selector, scope = el) {
  return splitOutside(selector, c => c === ',').some(branch => {
    const parts = splitOutside(branch.trim().replace(/\s*>\s*/g, '>'), c => /\s/.test(c));
    // Expand an explicit child link, preserving descendant links between parts.
    const chain = parts.flatMap(part => part.split('>').flatMap((p, i) => i ? ['>', p] : [p]));
    let node = el, i = chain.length - 1;
    if (!simple(node, chain[i--], scope)) return false;
    while (i >= 0) {
      if (chain[i] === '>') { node = node.parentElement; i--; if (!simple(node, chain[i--], scope)) return false; }
      else { const part = chain[i--]; node = node.parentElement; while (node && !simple(node, part, scope)) node = node.parentElement; if (!node) return false; }
    }
    return true;
  });
}
class Target {
  constructor(env) { this.env = env; this.listeners = new Map(); }
  addEventListener(type, fn, options = {}) {
    const capture = typeof options === 'boolean' ? options : !!options.capture;
    const list = this.listeners.get(type) || [];
    if (!list.some(x => x.fn === fn && x.capture === capture)) list.push({ fn, capture, once: !!options?.once });
    this.listeners.set(type, list);
  }
  removeEventListener(type, fn, options = {}) {
    this.env.inject?.('removeEventListener', this, type, fn, options);
    const capture = typeof options === 'boolean' ? options : !!options.capture;
    this.listeners.set(type, (this.listeners.get(type) || []).filter(x => x.fn !== fn || x.capture !== capture));
  }
  handlers(type) { return (this.listeners.get(type) || []).map(x => x.fn); }
  dispatchEvent(event) {
    if (!event.target) event.target = this;
    const path = [this]; let node = this.parentNode;
    while (node) { path.push(node); node = node.parentNode; }
    if (path.includes(this.env.doc)) path.push(this.env.win);
    const deliver = (target, capture) => {
      event.currentTarget = target;
      for (const item of [...(target.listeners.get(event.type) || [])]) {
        if (!(target.listeners.get(event.type) || []).includes(item)) continue;
        if (item.capture !== capture) continue;
        if (item.once) target.removeEventListener(event.type, item.fn, { capture });
        item.fn.call(target, event); if (event.immediate) break;
      }
    };
    for (const target of [...path].reverse()) { deliver(target, true); if (event.stopped) break; }
    if (!event.stopped) deliver(this, false);
    if (event.bubbles && !event.stopped) for (const target of path.slice(1)) { deliver(target, false); if (event.stopped) break; }
    return !event.defaultPrevented;
  }
}
class FixtureEvent {
  constructor(type, init = {}) { Object.assign(this, { type, bubbles: false, cancelable: false, defaultPrevented: false, stopped: false }, init); }
  preventDefault() { if (this.cancelable) this.defaultPrevented = true; }
  stopPropagation() { this.stopped = true; }
  stopImmediatePropagation() { this.stopped = this.immediate = true; }
}
class TextNode {
  constructor(text, env) { this.nodeType = 3; this.data = String(text); this.env = env; this.parentNode = null; }
  get textContent() { return this.data; }
  get isConnected() { return !!this.parentNode?.isConnected; }
  remove() { this.parentNode?.removeChild(this); }
}
class ElementNode extends Target {
  constructor(tag, env) {
    super(env); this.nodeType = 1; this.localName = tag.toLowerCase(); this.tagName = tag.toUpperCase();
    this.attrs = new Map(); this.childNodes = []; this.parentNode = null; this.value = ''; this.selectionStart = this.selectionEnd = 0;
    this._scrollTop = this.scrollLeft = 0; this.open = false; this.box = null;
    this.style = { setProperty(k, v) { this[k] = String(v); }, removeProperty(k) { delete this[k]; }, getPropertyValue(k) { return this[k] || ''; } };
    this.classList = {
      contains: name => this.className.split(/\s+/).includes(name),
      add: (...names) => { this.className = [...new Set([...this.className.split(/\s+/).filter(Boolean), ...names])].join(' '); },
      remove: (...names) => { this.className = this.className.split(/\s+/).filter(n => !names.includes(n)).join(' '); },
      toggle: (name, force) => { const on = force === undefined ? !this.classList.contains(name) : !!force; this.classList[on ? 'add' : 'remove'](name); return on; },
    };
    this.dataset = new Proxy({}, { get: (_, key) => this.getAttribute('data-' + String(key).replace(/[A-Z]/g, c => '-' + c.toLowerCase())) ?? undefined,
      set: (_, key, value) => { this.setAttribute('data-' + String(key).replace(/[A-Z]/g, c => '-' + c.toLowerCase()), value); return true; } });
  }
  get ownerDocument() { return this.env.doc; }
  get parentElement() { return this.parentNode instanceof ElementNode ? this.parentNode : null; }
  get children() { return this.childNodes.filter(n => n instanceof ElementNode); }
  get firstElementChild() { return this.children[0] || null; }
  get lastElementChild() { return this.children.at(-1) || null; }
  get nextElementSibling() { const siblings = this.parentElement?.children || []; return siblings[siblings.indexOf(this) + 1] || null; }
  get firstChild() { return this.childNodes[0] || null; }
  get isConnected() { return this.parentNode === this.env.doc || !!this.parentNode?.isConnected; }
  get className() { return this.getAttribute('class') || ''; }
  set className(value) { this.setAttribute('class', value); }
  get id() { return this.getAttribute('id') || ''; }
  set id(value) { this.setAttribute('id', value); }
  get hidden() { return this.hasAttribute('hidden'); }
  set hidden(value) { value ? this.setAttribute('hidden', '') : this.removeAttribute('hidden'); }
  get disabled() { return this.hasAttribute('disabled'); }
  set disabled(value) { value ? this.setAttribute('disabled', '') : this.removeAttribute('disabled'); }
  get inert() { return this.hasAttribute('inert'); }
  set inert(value) { value ? this.setAttribute('inert', '') : this.removeAttribute('inert'); }
  get tabIndex() { return this.hasAttribute('tabindex') ? Number(this.getAttribute('tabindex')) : this.matches('button,input,textarea,select,a[href]') ? 0 : -1; }
  set tabIndex(value) { this.setAttribute('tabindex', value); }
  get src() { return this.getAttribute('src') || ''; }
  set src(value) { this.setAttribute('src', value); }
  get href() { return this.getAttribute('href') || ''; }
  set href(value) { this.setAttribute('href', value); }
  setAttribute(key, value) {
    this.env.inject?.('attribute', this, key);
    if (key === 'src' && this.localName === 'img' && this.hasAttribute('src')) this.env.audit();
    this.attrs.set(key, String(value)); if (key === 'value') this.value = String(value);
    if (key === 'src' && this.localName === 'img') { this.env.sources.set(this, String(value)); this.env.audit(); }
    if (key === 'src' && this.localName === 'img') this.env.inject?.('attributeAfter', this, key);
  }
  getAttribute(key) { return this.attrs.get(key) ?? null; }
  hasAttribute(key) { return this.attrs.has(key); }
  removeAttribute(key) {
    this.env.inject?.('removeAttribute', this, key);
    if (key === 'src' && this.localName === 'img' && this.hasAttribute('src')) {
      this.env.audit(); // Observe the old physical consumer before deleting its evidence.
      this.env.tap(() => assert.equal(this.handlers('error').length, 0, 'remove error listener before src'));
      this.env.sources.delete(this);
    }
    this.attrs.delete(key); this.env.audit();
  }
  get textContent() { return this.childNodes.map(n => n.textContent).join(''); }
  set textContent(value) { this.replaceChildren(String(value)); }
  append(...values) { for (const value of values.flat()) { if (value == null) continue; this.appendChild(value instanceof ElementNode || value instanceof TextNode ? value : new TextNode(value, this.env)); } }
  appendChild(node) { if (node.parentNode) node.parentNode.removeChild(node); node.parentNode = this; this.childNodes.push(node); this.env.audit(); return node; }
  removeChild(node) {
    const index = this.childNodes.indexOf(node); if (index < 0) throw new Error('NotFoundError');
    this.env.inject?.('removeChild', node);
    if (this.contains(this.env.doc.activeElement) && node.contains?.(this.env.doc.activeElement)) this.env.doc.activeElement = this.env.doc.body;
    if (node === this.env.doc.activeElement) this.env.doc.activeElement = this.env.doc.body;
    this.childNodes.splice(index, 1); node.parentNode = null; this.env.audit(); return node;
  }
  replaceChildren(...values) {
    this.env.inject?.('replaceBefore', this);
    if (this.matches('.msgs')) this.env.replacements++;
    for (const node of [...this.childNodes]) this.removeChild(node);
    this.append(...values); this.env.inject?.('replaceAfter', this);
  }
  insertBefore(node, before) { if (!before) return this.appendChild(node); if (node === before) return node; if (node.parentNode) node.remove(); const index = this.childNodes.indexOf(before); if (index < 0) throw new Error('NotFoundError'); this.childNodes.splice(index, 0, node); node.parentNode = this; return node; }
  remove() { this.parentNode?.removeChild(this); }
  contains(node) { for (let cur = node; cur; cur = cur.parentNode) if (cur === this) return true; return false; }
  matches(selector) { return matches(this, selector); }
  closest(selector) { for (let node = this; node instanceof ElementNode; node = node.parentElement) if (matches(node, selector)) return node; return null; }
  querySelectorAll(selector) { return this.children.flatMap(node => [...(matches(node, selector, this) ? [node] : []), ...node.querySelectorAllScoped(selector, this)]); }
  querySelectorAllScoped(selector, scope) { return this.children.flatMap(node => [...(matches(node, selector, scope) ? [node] : []), ...node.querySelectorAllScoped(selector, scope)]); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  getBoundingClientRect() { this.env.inject?.('geometry', this); return this.env.boxOf(this); }
  getClientRects() { return this.env.visible(this) ? [this.getBoundingClientRect()] : []; }
  get clientHeight() { return this.getBoundingClientRect().height; }
  get clientWidth() { return this.getBoundingClientRect().width; }
  get offsetHeight() { return this.clientHeight; }
  get offsetWidth() { return this.clientWidth; }
  get offsetParent() { return this.env.visible(this) ? this.parentElement : null; }
  get scrollTop() { return this._scrollTop; }
  set scrollTop(value) { this._scrollTop = Math.max(0, Math.min(Number(value), Math.max(0, this.scrollHeight - this.clientHeight))); }
  get scrollHeight() { return this.localName === 'main' ? this.env.documentHeight : this.clientHeight; }
  focus(options = {}) {
    const modal = this.env.doc.querySelectorAll('dialog[open]').at(-1);
    if (!this.env.visible(this) || this.disabled || this.closest('[inert]') || (modal && !modal.contains(this)) || (!this.hasAttribute('tabindex') && !this.matches('button,input,textarea,select,a[href],dialog'))) return;
    const old = this.env.doc.activeElement; this.env.doc.activeElement = this;
    if (!options.preventScroll) this.scrollIntoView({ block: 'nearest' });
    if (old !== this) { old?.dispatchEvent(new FixtureEvent('focusout', { bubbles: true, relatedTarget: this })); this.dispatchEvent(new FixtureEvent('focusin', { bubbles: true, relatedTarget: old })); }
    this.env.inject?.('focus', this);
  }
  setSelectionRange(start, end) { this.selectionStart = start; this.selectionEnd = end; }
  select() { this.setSelectionRange(0, this.value.length); }
  scrollIntoView(options = {}) {
    this.env.scrolled = this; this.env.lastScroll = { id: this.id, block: options.block };
    if (this.matches('.msg,.newdiv')) { const r = this.env.rawBox(this); this.env.main.scrollTop = Math.max(0, r.top); }
  }
  click() {
    if (this.disabled) return; // Programmatic click works on hidden download anchors.
    if (this.localName === 'a' && this.hasAttribute('download')) this.env.downloadClicks++;
    this.dispatchEvent(new FixtureEvent('click', { bubbles: true, cancelable: true }));
  }
  showModal() { if (!this.isConnected) throw new Error('InvalidStateError'); this.open = true; this.setAttribute('open', ''); this.focus(); }
  close(value = '') {
    if (!this.open) return;
    this.open = false; this.removeAttribute('open'); this.returnValue = value;
    if (this.contains(this.env.doc.activeElement)) this.env.doc.activeElement = this.env.doc.body;
    this.env.tasks.push(() => this.dispatchEvent(new FixtureEvent('close')));
  }
}
class ImageNode extends ElementNode { constructor(env) { super('img', env); } }

function installFixture(t, options = {}) {
  assert.equal(current === null, true, 'fixtures run serially');
  const s = { errors: [], urls: new Map(), sources: new Map(), activeCalls: new Map(), starts: new Map(), made: 0, revoked: 0, attempts: 0, issued: 0, legacy: 0, downloads: 0, downloadClicks: 0, replacements: 0,
    width: options.width || 1440, height: 900, documentHeight: 4000, composerHeight: 180, defaultTop: options.top ?? 100, boxes: new Map(), tasks: [], raf: new Map(), timers: new Map(), observers: new Set(), resizeObservers: new Set(), clock: 0, serial: 0,
    limits: { ...BASE, ...options.limits }, budget: null, wrappers: new Set(), offs: [], resources: [], closing: false, inject: null, onRevoke: null, onCreate: null, onStart: null };
  s.errorOverflow = 0;
  s.recordError = error => { if (s.errors.length < 16) s.errors.push(error); else s.errorOverflow++; };
  s.tap = fn => { try { return fn(); } catch (error) { s.recordError(error); } };
  s.check = () => { assert.equal(s.errorOverflow, 0, 'callback error-channel overflow is a failure'); assert.deepEqual(s.errors, [], 'assertions/errors inside contained callbacks'); s.equations(); };
  s.equations = () => {
    assert.equal(s.made, s.revoked + s.urls.size);
    if (s.budget) {
      const q = s.budget.stats, l = s.limits;
      for (const value of Object.values(q)) assert.ok(Number.isSafeInteger(value) && value >= 0);
      assert.equal(q.reservedBytes, q.running * l.M); assert.ok(q.reservedBytes + q.residentBytes <= l.B);
      assert.ok(q.running <= l.C && q.running + q.residentEntries <= l.E && q.queued <= l.Q && q.sources <= l.N);
      assert.ok(s.sources.size <= q.sources, 'every physical src, disconnected included, owns a real permit');
    } else assert.ok(s.sources.size <= 16, 'default physical source bound');
  };
  s.audit = () => s.tap(() => s.equations());
  s.createURL = blob => {
    assert.ok(blob instanceof Blob); s.onCreate?.(blob);
    const url = `blob:lifecycle/${++s.made}`;
    s.urls.set(url, { size: blob.size, type: blob.type }); s.audit(); return url;
  };
  s.revokeURL = url => {
    s.attempts++; s.tap(() => { assert.ok(s.urls.has(url), 'only an unreclaimed URL is disposed'); assert.ok(![...s.sources.values()].includes(url), 'all physical consumers detach BEFORE revoke'); });
    s.onRevoke?.(url); if (s.urls.delete(url)) s.revoked++; s.audit();
  };
  s.rawBox = el => {
    if (el.box) return typeof el.box === 'function' ? el.box() : el.box;
    if (el.matches('.newdiv') && el.nextElementSibling) return s.rawBox(el.nextElementSibling);
    const message = el.closest('.msg'), key = message?.id || el.closest('[data-occurrence]')?.dataset.occurrence;
    return s.boxes.get(key) || rect(24, (message ? Math.max(0, s.documentHeight - s.height) : 0) + s.defaultTop, 240, 180);
  };
  s.boxOf = el => {
    if (el.box) return typeof el.box === 'function' ? el.box() : el.box;
    if (el === s.main || el === s.root) return rect(0, 0, Math.min(s.width, 900), s.height);
    if (el.matches('.composer')) return rect(0, s.height - s.composerHeight, Math.min(s.width, 900), s.composerHeight);
    if (el.matches('dialog')) return rect(0, 0, s.width, s.height);
    const r = s.rawBox(el), inMain = s.main?.contains(el);
    return rect(r.left, r.top - (inMain ? s.main.scrollTop : 0), r.width, r.height);
  };
  s.visible = el => {
    if (!el?.isConnected) return false;
    for (let node = el; node instanceof ElementNode; node = node.parentElement) {
      if (node.hidden || node.style.display === 'none' || node.style.visibility === 'hidden' || (node.localName === 'dialog' && !node.open)) return false;
      if (node.matches('.aside-toggle') && s.width > 1100) return false;
      if (node.matches('.aside-body') && s.width <= 1100 && !node.closest('aside')?.classList.contains('open')) return false;
    }
    const r = s.boxOf(el); return r.width > 0 && r.height > 0;
  };
  const doc = new Target(s), win = new Target(s); s.doc = doc; s.win = win;
  doc.nodeType = 9; doc.isConnected = true; doc.parentNode = null;
  doc.documentElement = new ElementNode('html', s); doc.documentElement.parentNode = doc;
  doc.body = new ElementNode('body', s); doc.documentElement.append(doc.body); doc.activeElement = doc.body; doc.visibilityState = 'visible'; doc.hasFocus = () => true;
  doc.createElement = tag => { s.inject?.('createElement', tag); return tag.toLowerCase() === 'img' ? new ImageNode(s) : new ElementNode(tag, s); };
  doc.createElementNS = (_, tag) => doc.createElement(tag); doc.createTextNode = text => new TextNode(text, s);
  doc.querySelector = selector => doc.documentElement.querySelector(selector); doc.querySelectorAll = selector => doc.documentElement.querySelectorAll(selector);
  doc.getElementById = id => doc.querySelector(`[id="${id}"]`); doc.execCommand = () => true;
  s.main = doc.createElement('main'); s.root = doc.createElement('div'); s.root.id = 'view'; s.main.append(s.root);
  s.aside = doc.createElement('aside'); s.aside.id = 'aside'; doc.body.append(s.main, s.aside); doc.scrollingElement = s.main;
  class IO {
    constructor(callback, opts = {}) { this.callback = callback; this.options = opts; this.targets = new Set(); this.kind = 'intersection'; s.observers.add(this); }
    get root() { return this.options.root || null; }
    get rootMargin() { return this.options.rootMargin || '0px'; }
    get thresholds() { return [].concat(this.options.threshold || 0); }
    observe(node) { this.targets.add(node); (this.kind === 'resize' ? s.resizeObservers : s.observers).add(this); }
    unobserve(node) { this.targets.delete(node); }
    disconnect() {
      s.inject?.('disconnect', this, this.kind);
      this.targets.clear(); s.observers.delete(this); s.resizeObservers.delete(this);
    }
    takeRecords() { return []; }
  }
  class RO extends IO { constructor(callback) { super(callback); this.kind = 'resize'; s.observers.delete(this); s.resizeObservers.add(this); } }
  s.intersections = (filter = () => true) => {
    for (const observer of [...s.observers]) {
      const entries = [...observer.targets].filter(filter).map(target => {
        const bounds = target.getBoundingClientRect(), root = observer.options.root?.getBoundingClientRect() || rect(0, 0, s.width, s.height);
        const bottomMargin = Number((observer.options.rootMargin || '').split(/\s+/)[2]?.replace('px', '')) || 0;
        const clip = rect(root.left, root.top, root.width, Math.max(0, root.height + bottomMargin));
        let intersection = overlap(bounds, overlap(clip, rect(0, 0, s.width, s.height)));
        for (let parent = target.parentElement; parent; parent = parent.parentElement) {
          if (/(auto|scroll|hidden|clip)/.test(parent.style.overflowY || parent.style.overflow || '')) intersection = overlap(intersection, parent.getBoundingClientRect());
        }
        if (!s.visible(target)) intersection = rect(0, 0, 0, 0);
        return { target, rootBounds: clip, boundingClientRect: bounds, intersectionRect: intersection, intersectionRatio: bounds.width * bounds.height ? intersection.width * intersection.height / (bounds.width * bounds.height) : 0, isIntersecting: intersection.width > 0 && intersection.height > 0 };
      });
      if (entries.length) s.tap(() => observer.callback(entries, observer));
    }
  };
  s.resize = () => { for (const observer of [...s.resizeObservers]) if (observer.targets.size) s.tap(() => observer.callback([...observer.targets].map(target => ({ target, contentRect: target.getBoundingClientRect() })), observer)); win.dispatchEvent(new FixtureEvent('resize')); };
  s.flush = async () => {
    await hostTurn();
    for (let turn = 0; turn < 8 && (s.tasks.length || s.raf.size); turn++) {
      const tasks = s.tasks.splice(0), frames = [...s.raf.values()]; s.raf.clear();
      for (const fn of tasks) s.tap(fn); for (const fn of frames) s.tap(() => fn(s.clock)); await hostTurn();
    }
    assert.ok(!s.tasks.length && !s.raf.size, 'bounded task/frame queue must settle');
  };
  s.advance = async ms => {
    const end = s.clock + ms;
    for (let count = 0; ; count++) {
      const next = [...s.timers].filter(([, x]) => x.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break; assert.ok(count < 20000, 'bounded timer loop');
      const [id, timer] = next; s.clock = timer.at;
      if (timer.every) timer.at += timer.every; else s.timers.delete(id);
      s.tap(timer.fn); await hostTurn();
    }
    s.clock = end; await s.flush();
  };
  const globals = { document: doc, window: win, Element: ElementNode, HTMLElement: ElementNode, HTMLImageElement: ImageNode,
    Event: FixtureEvent, MouseEvent: FixtureEvent, KeyboardEvent: FixtureEvent, CustomEvent: FixtureEvent,
    innerWidth: s.width, innerHeight: s.height, IntersectionObserver: IO, ResizeObserver: RO,
    requestAnimationFrame: fn => { const id = ++s.serial; s.raf.set(id, fn); return id; }, cancelAnimationFrame: id => s.raf.delete(id),
    setTimeout: (fn, ms = 0) => { const id = ++s.serial; s.timers.set(id, { fn, at: s.clock + Math.max(0, Number(ms) || 0) }); return id; }, clearTimeout: id => s.timers.delete(id),
    setInterval: (fn, ms) => { const id = ++s.serial; s.timers.set(id, { fn, at: s.clock + ms, every: ms }); return id; }, clearInterval: id => s.timers.delete(id),
    getComputedStyle: el => ({ display: s.visible(el) ? (el.style.display || 'block') : 'none', visibility: el.style.visibility || 'visible', overflow: el.style.overflow || 'visible', overflowY: el.style.overflowY || el.style.overflow || 'visible', lineHeight: '20px', fontSize: '14px' }),
  };
  Object.assign(win, globals, { matchMedia: query => ({ matches: query.includes('1100') ? s.width <= 1100 : false, addEventListener() {}, removeEventListener() {} }), scrollX: 0, scrollY: 0 });
  const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { value, writable: true, configurable: true });
  const oldConsoleError = console.error; console.error = (...args) => s.recordError(args[0]);
  current = s;
  s.call = (hash, kind, options = {}) => {
    const body = deferred(), cleanup = deferred(), id = ++s.issued;
    const call = { id, hash, kind, body, cleanup, signal: options.signal, aborts: 0, bodyDone: false };
    call.terminal = body.promise.then(async value => { call.bodyDone = true; await cleanup.promise; return value; }, async error => { call.bodyDone = true; await cleanup.promise; throw error; });
    s.activeCalls.set(id, call); s.starts.set(hash, (s.starts.get(hash) || 0) + 1);
    assert.ok(s.starts.size <= 1200 && s.activeCalls.size <= 1200, 'bounded synthetic transport controls');
    if (kind === 'legacy') s.legacy++;
    if (options.signal) { s.tap(() => assert.ok(options.signal instanceof AbortSignal)); options.signal.addEventListener('abort', () => { call.aborts++; }, { once: true }); }
    if (kind === 'thumbnail') s.tap(() => assert.ok(Number.isSafeInteger(options.maxBytes) && options.maxBytes > 0));
    s.onStart?.(call); s.audit();
    if (s.closing) { body.resolve(null); cleanup.resolve(); }
    return call.terminal.finally(() => { s.activeCalls.delete(id); });
  };
  s.finish = async (call, value = raster(), rejected = false) => {
    assert.ok(call, 'expected real transport dispatch');
    if (rejected) call.body.reject(value); else call.body.resolve(value);
    await hostTurn(); call.cleanup.resolve(); await Promise.allSettled([call.terminal]); await s.flush();
  };
  s.calls = () => [...s.activeCalls.values()]; s.findCall = hash => s.calls().find(call => call.hash === hash);
  s.api = { thumbnailBlob: (hash, opts) => s.call(hash, 'thumbnail', opts), blob: hash => s.call(hash, 'legacy'),
    receipt: async (delivery, state) => { s.receipts.push([delivery, state]); }, readThread: async () => { s.markAll++; },
    thread: async () => { if (s.detailGate) return s.detailGate.promise; if (s.detailError) throw s.detailError; return s.detail; },
    messages: async (_, opts) => { s.messagePages.push({ ...opts }); if (s.historyGate) return s.historyGate.promise; if (s.historyError) throw s.historyError; return s.pageMessages(opts); },
    matrix: async (_, opts) => { s.matrixPages.push({ ...opts }); return s.pageMatrix(opts); },
    download: async () => { s.downloads++; return new Blob(['download']); }, adminPeers: async () => ({ peers: [] }), blobStatus: async () => ({ state: 'available' }) };
  s.receipts = []; s.markAll = 0; s.messagePages = []; s.matrixPages = [];
  s.messages = options.messages || [msg(1, [att()])]; s.unreadIds = new Set(options.unread || []);
  s.detail = { row: { thread: { thread_id: 'thr_test', participants: ['peer_op', 'peer_other'], title: 'Synthetic thread', message_count: s.messages.length } }, files: [], open_approvals: [] };
  s.historyValue = () => ({ messages: s.messages, hosted: new Map(s.messages.map(m => [m.message_id, new Set(m.attachments.filter(a => !a.origin_url).map(a => a.sha256))])), states: new Map() });
  s.matrixValue = () => ({ recipients: ['peer_op'], rows: s.messages.map(m => ({ message_id: m.message_id, sender: m.sender, cells: [{ recipient: 'peer_op', delivery_id: 'dly_' + m.message_id, state: 'accepted', display: s.unreadIds.has(m.message_id) ? 'accepted' : 'read', read_at: s.unreadIds.has(m.message_id) ? null : '2026-11-09T09:30:00Z' }] })) });
  s.pageMessages = () => ({ messages: s.messages, hosted_blobs: Object.fromEntries([...s.historyValue().hosted].map(([id, hashes]) => [id, [...hashes]])), next_after: null });
  s.pageMatrix = () => s.matrixValue();
  const actualStore = createStore({ api: s.api, now: () => NOW + s.clock });
  s.store = { ...actualStore, resource: (...args) => { const r = actualStore.resource(...args); s.resources.push(r); return r; } };
  const threads = actualStore.resource('threads', async () => ({ threads: [s.detail.row.thread] })), peers = actualStore.resource('peers', async () => ({ peers: [] }));
  threads.set({ threads: [s.detail.row.thread] }); peers.set({ peers: [] });
  s.ctx = { api: s.api, store: s.store, threadId: 'thr_test', me: 'peer_op', threads, peers, nameOf: id => id, kindOf: () => 'operator', toast() {}, nav() {} };
  s.mount = async () => { const off = mountThread(s.root, s.ctx); s.offs.push(off); await actualStore.settled(); await s.flush(); return off; };
  s.redraw = async () => { threads.set({ threads: [s.detail.row.thread] }); await s.flush(); };
  s.setHistory = async value => { s.resources[0].set(value); await s.flush(); };
  s.asideMount = async ({ settle = true } = {}) => { const off = mountAside(s.aside, s.ctx); s.offs.push(off); if (settle) await actualStore.settled(); await s.flush(); return off; };
  // Future Draft B can hold detailGate with settle:false, reject detailError,
  // or set the actual aside resource to empty/populated without a fake panel.
  s.forgetMount = off => { s.offs = s.offs.filter(item => item !== off); s.resources.length = 0; };
  s.forgetWrapper = thumbs => s.wrappers.delete(thumbs);
  s.make = () => {
    s.documentHeight = s.height;
    if (!s.root.querySelector('.composer') && !s.standaloneComposer) {
      s.standaloneComposer = doc.createElement('div'); s.standaloneComposer.className = 'composer'; s.main.append(s.standaloneComposer);
    }
    if (!s.budget) s.budget = createThumbBudget({ limits: s.limits });
    const thumbs = createThumbs({ api: s.api, budget: s.budget }); s.wrappers.add(thumbs); return thumbs;
  };
  s.transaction = (thumbs, rows, host = s.root) => {
    assert.equal(typeof thumbs.beginRender, 'function', 'transaction API is an interface gate, not a behavioral RED receipt');
    const composer = s.standaloneComposer || s.root.querySelector('.composer');
    assert.ok(composer?.isConnected, 'transaction owns an explicit connected composer');
    thumbs.beginRender(); const nodes = rows.map(row => thumbs.render(row.att, row.hosted, row.key)); host.replaceChildren(...nodes);
    assert.equal((s.standaloneComposer || s.root.querySelector('.composer')) === composer, true, 'composer identity survives replacement');
    assert.ok(composer.isConnected, 'composer remains connected before finalization');
    if (s.standaloneComposer) {
      assert.equal(host.contains(composer), false, 'standalone composer lives outside the replaced host');
      const box = composer.getBoundingClientRect();
      assert.deepEqual([box.left, box.top, box.width, box.height], [0, s.height - s.composerHeight, Math.min(s.width, 900), s.composerHeight]);
    }
    thumbs.endRender({ viewport: s.main, composer }); return nodes;
  };
  s.visibleChange = state => { doc.visibilityState = state; doc.dispatchEvent(new FixtureEvent('visibilitychange')); };
  s.press = (node, key) => {
    const down = new FixtureEvent('keydown', { key, bubbles: true, cancelable: true }); node.dispatchEvent(down);
    const up = new FixtureEvent('keyup', { key, bubbles: true, cancelable: true }); node.dispatchEvent(up);
    if (!down.defaultPrevented && !up.defaultPrevented && (key === 'Enter' || key === ' ') && node.matches('button') && s.visible(node) && !node.closest('[inert]')) node.click();
  };
  t.after(async () => {
    s.closing = true; s.inject = s.onRevoke = s.onCreate = s.onStart = null;
    s.historyGate?.resolve({ messages: [], next_after: null }); s.detailGate?.resolve(s.detail);
    const cleanupErrors = [];
    try {
      for (const off of [...s.offs].reverse()) { try { off(); } catch (error) { cleanupErrors.push(error); } }
      for (const thumbs of s.wrappers) { try { thumbs.release(); } catch (error) { cleanupErrors.push(error); } }
      for (let round = 0; round < 8 && s.activeCalls.size; round++) {
        const calls = s.calls(); for (const call of calls) { call.body.resolve(null); call.cleanup.resolve(); }
        await Promise.allSettled(calls.map(call => call.terminal)); await s.flush();
      }
      await actualStore.settled();
      if (s.budget) { try { retryThumbCleanup(s.budget); } catch (error) { cleanupErrors.push(error); } }
      await s.flush(); s.check(); assert.deepEqual(cleanupErrors, []); assert.equal(s.activeCalls.size, 0);
      assert.equal(s.sources.size, 0, 'no physical source survives cleanup, including disconnected nodes'); assert.equal(s.urls.size, 0);
      if (s.budget) for (const value of Object.values(s.budget.stats)) assert.equal(value, 0, 'real quota drains after terminal cleanup');
    } finally {
      console.error = oldConsoleError; current = null;
      for (const [key, descriptor] of previous) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key]; }
      s.tasks.length = 0; s.raf.clear(); s.timers.clear(); s.observers.clear(); s.resizeObservers.clear(); s.offs.length = 0; s.wrappers.clear();
    }
  });
  return s;
}

const root = (name, fn) => test('WEB-MEM-3 T3 ' + name, { concurrency: false }, fn);
const rowsOf = list => list.map((a, i) => ({ att: a, key: JSON.stringify(['fixture-message', i, a.sha256]), hosted: !a.origin_url }));
const imageNodes = s => [...s.sources.keys()];
const readyURL = s => s.root.querySelector('button.thumb img')?.getAttribute('src');
async function finishVisible(s) {
  for (let pass = 0; pass < 20 && s.activeCalls.size; pass++) {
    for (const call of s.calls()) await s.finish(call);
  }
  assert.equal(s.activeCalls.size, 0, 'finite demand drains');
}
function scrollEvent(s) { s.main.dispatchEvent(new FixtureEvent('scroll')); }

root('mount_offscreen_no_auto', async t => {
  const s = installFixture(t, { top: 10000, messages: Array.from({ length: 1000 }, (_, i) => msg(i + 1, [att(i + 1)])) });
  await s.mount();
  assert.equal(s.root.querySelectorAll('.msg').length, 1000, 'keep the loaded history');
  assert.equal(s.issued, 0, 'offscreen registration must not dispatch blob OR thumbnailBlob');
  assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0); s.check();
});

root('geometry_after_placement', async t => {
  for (const unread of [[], ['msg_1']]) await t.test(unread.length ? 'initial unread' : 'initial bottom', async sub => {
    const s = installFixture(sub, { messages: [msg(1, [att(1)]), msg(2, [att(2)]), msg(3, [att(3)])], unread });
    s.boxes.set('m-msg_1', rect(24, 3200, 240, 180));
    s.boxes.set('m-msg_2', rect(24, 3900, 240, 180)); // under floating composer
    s.boxes.set('m-msg_3', rect(24, 4200, 240, 180)); // outside viewport
    const clip = s.doc.createElement('div'); clip.style.overflow = 'hidden'; clip.box = rect(0, 0, 900, 600);
    s.root.remove(); s.main.append(clip); clip.append(s.root);
    await s.mount(); s.intersections(); await s.flush();
    assert.equal(s.main.scrollTop, 3100, 'placement precedes demand and scroll clamps like the browser');
    if (unread.length) assert.equal(s.lastScroll.block, 'start');
    assert.deepEqual([...s.starts.keys()], [sha(1)], 'clipped/composer-covered/offscreen rows never dispatch');
    s.boxes.set('m-msg_3', rect(24, 3600, 240, 180)); // visible in viewport, outside clipping ancestor
    clip.box = rect(0, 0, 900, 400); s.resize(); s.intersections(); await s.flush();
    assert.equal(s.starts.has(sha(3)), false, 'intersection with viewport alone is insufficient');
    clip.box = rect(0, 0, 900, 900); s.composerHeight = 100; s.resize(); s.intersections(); await s.flush();
    assert.equal(s.starts.has(sha(3)), true, 'real resize recomputes demand');
    assert.equal(s.starts.has(sha(2)), false, 'row at y800 is still covered by composer'); s.check();
  });
});

root('hidden_drops_auto_keeps_viewer', async t => {
  const s = installFixture(t, { messages: [msg(1, [att(1)]), msg(2, [att(2)])], unread: ['msg_1', 'msg_2'] });
  await s.mount(); await finishVisible(s);
  const button = s.root.querySelector('button.thumb'); assert.ok(button); button.click(); await s.flush();
  const dialog = s.doc.querySelector('dialog[open]'); assert.ok(dialog);
  assert.ok(imageNodes(s).some(node => !dialog.contains(node)), 'automatic sources exist before hide');
  s.visibleChange('hidden');
  assert.equal(s.sources.size, 1, 'automatic sources retire synchronously on hide');
  assert.ok(imageNodes(s).every(node => dialog.contains(node)), 'only explicit viewer survives');
  await s.advance(DWELL_MS + BATCH_MS + 1); assert.deepEqual(s.receipts, []);
  s.visibleChange('visible'); s.intersections(node => !node.id.startsWith('m-')); await s.flush();
  await finishVisible(s); assert.ok(s.sources.size >= 1); assert.deepEqual(s.receipts, [], 'thumbnail demand cannot manufacture reader observations'); s.check();
});

root('dense_stable_epoch', async t => {
  const axes = [{ B: 64, C: 1, E: 12, Q: 8 }, { B: 4, C: 8, E: 12, Q: 8 }, { B: 64, C: 8, E: 1, Q: 8 }, { B: 64, C: 1, E: 12, Q: 1 }];
  for (let axis = 0; axis < axes.length; axis++) await t.test(`independent pressure ${axis}`, async sub => {
    const s = installFixture(sub, { limits: { ...axes[axis], N: 4 } }), thumbs = s.make();
    const rows = rowsOf([att(1), att(1), ...Array.from({ length: 8 }, (_, i) => att(i + 2))]);
    // No focused candidate; every box has identical visible geometry. The
    // prescribed key tie-break independently chooses four occurrence owners,
    // including queued/waiting work, BEFORE any transport can complete.
    const admitted = [...rows].sort((a, b) => a.key < b.key ? -1 : a.key > b.key ? 1 : 0).slice(0, s.limits.N);
    const admittedKeys = new Set(admitted.map(row => row.key)), admittedHashes = new Set(admitted.map(row => row.att.sha256));
    assert.equal(s.doc.activeElement === s.doc.body, true, 'no focused occurrence changes initial priority');
    assert.deepEqual(admitted.map(row => rows.indexOf(row)), [0, 1, 2, 3]);
    assert.deepEqual([...admittedHashes], [sha(1), sha(2), sha(3)]);
    const checkEpoch = () => {
      s.check(); assert.equal(thumbs.stats.owners, admittedKeys.size); assert.equal(s.budget.stats.sources, admittedKeys.size);
      for (const hash of s.starts.keys()) assert.ok(admittedHashes.has(hash), 'initially denied hash cannot dispatch in unchanged epoch');
      for (const image of imageNodes(s)) assert.ok(admittedKeys.has(image.closest('[data-occurrence]')?.dataset.occurrence), 'only initially admitted occurrences attach');
    };
    s.onStart = call => s.tap(() => assert.ok(admittedHashes.has(call.hash), 'initially denied hash cannot dispatch in unchanged epoch'));
    const nodes = s.transaction(thumbs, rows); s.intersections(); await s.flush(); checkEpoch();
    assert.deepEqual(nodes.map(node => node.dataset.occurrence), rows.map(row => row.key));
    for (const node of nodes) {
      const box = node.getBoundingClientRect(); assert.ok(node.isConnected);
      assert.deepEqual([box.left, box.top, box.width, box.height], [24, s.defaultTop, 240, 180], 'equal visible boxes leave key as the only tie-break');
      assert.ok(box.bottom <= s.standaloneComposer.getBoundingClientRect().top);
    }
    assert.equal(thumbs.stats.entries, admittedHashes.size, 'initial owner set includes hashes not dispatched yet');
    assert.deepEqual([...s.starts.keys()], [sha(1)], 'initial dispatch alone does not describe all admitted work');
    const q = s.budget.stats;
    assert.equal(q.queued, axis === 3 ? 1 : 2, 'accepted queued jobs are part of the initial set');
    if (axis === 0 || axis === 3) assert.equal(q.running, 1);
    if (axis === 1) assert.equal(q.reservedBytes, s.limits.B);
    if (axis === 2) assert.equal(q.running + q.residentEntries, 1);
    if (axis === 3) assert.equal(q.queued, 1);
    const failed = s.calls()[0]; await s.finish(failed, new Error('explicit retry required'), true); checkEpoch();
    await finishVisible(s); checkEpoch(); const issued = s.issued, failedStarts = s.starts.get(failed.hash);
    assert.ok(issued <= s.limits.N, 'excess dense occurrences cannot rotate through admission');
    assert.ok(issued <= admittedHashes.size, 'completion count cannot enlarge the independently fixed admitted hash set');
    for (let i = 0; i < 8; i++) { s.transaction(thumbs, rows); s.intersections(); s.resize(); await s.flush(); checkEpoch(); }
    assert.equal(s.issued, issued, 'completion/redraw/chatter cannot restart displaced or failed work');
    s.onStart = null; // Visibility exit/reentry below is a real new demand epoch.
    s.visibleChange('hidden'); s.visibleChange('visible'); s.intersections(); await s.flush();
    assert.equal(s.starts.get(failed.hash), failedStarts, 'visibility does not reset a current error marker');
    s.check();
  });
  await t.test('real shared-budget displacement remains suppressed through redraw', async sub => {
    const s = installFixture(sub, { limits: { B: 32, C: 1, E: 8, N: 4, Q: 1 } }), thumbs = s.make();
    const rows = rowsOf([att(1), att(2), att(3)]);
    s.transaction(thumbs, rows); s.intersections(); await s.flush();
    assert.equal(s.issued, 1); assert.equal(s.budget.stats.queued, 1);
    // An actual second cache supplies pressure; no viewer implementation or
    // mocked ticket/cancellation callback is used to manufacture displacement.
    const explicit = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} });
    s.wrappers.add(explicit); explicit.acquire('explicit-pressure', att(20), true, { priority: 'explicit' }); await s.flush();
    await s.finish(s.findCall(sha(1))); assert.ok(s.findCall(sha(20)), 'accepted explicit work displaced queued automatic work');
    await s.finish(s.findCall(sha(20))); explicit.release(); await finishVisible(s);
    for (let i = 0; i < 8; i++) { s.transaction(thumbs, rows); s.intersections(); s.resize(); await s.flush(); }
    assert.equal(s.starts.has(sha(2)), false, 'displaced current epoch cannot restart through capacity/redraw chatter'); s.check();
  });
});

root('completion_local_identity', async t => {
  const s = installFixture(t); await s.mount();
  const list = s.root.querySelector('.msgs'), message = s.root.querySelector('.msg'), thumb = s.root.querySelector('.thumb');
  const textarea = s.root.querySelector('textarea'); textarea.value = 'unsent draft'; textarea.focus(); textarea.setSelectionRange(2, 7);
  const replaces = s.replacements, call = s.findCall(sha(1)); assert.ok(call, 'visible image dispatches');
  call.body.resolve(raster()); await hostTurn();
  assert.equal(s.urls.size, 0, 'body delivery is not terminal transport cleanup');
  call.cleanup.resolve(); await s.flush();
  assert.equal(s.root.querySelector('.msg') === message, true, 'completion must not rebuild message nodes');
  assert.equal(s.root.querySelector('.thumb') === thumb, true, 'patch the existing thumbnail button');
  assert.equal(s.root.querySelector('.msgs') === list, true); assert.equal(s.replacements, replaces);
  assert.equal(s.root.querySelector('textarea') === textarea, true); assert.equal(textarea.value, 'unsent draft');
  assert.deepEqual([textarea.selectionStart, textarea.selectionEnd], [2, 7]); assert.equal(s.doc.activeElement === textarea, true);
  assert.ok(thumb.querySelector('img[src]')); s.check();
});

root('atomic_all_history_branches', async t => {
  const s = installFixture(t); await s.mount(); await finishVisible(s);
  const url = readyURL(s), made = s.made, issued = s.issued; assert.ok(url);
  await s.redraw(); assert.equal(readyURL(s), url); assert.equal(s.made, made); assert.equal(s.issued, issued);
  const button = s.root.querySelector('button.thumb'); button.click(); await s.flush();
  const viewer = s.doc.querySelector('dialog[open]'); assert.ok(viewer);
  await s.setHistory({ messages: [], hosted: new Map(), states: new Map() });
  assert.equal(s.root.querySelectorAll('.msg').length, 0); assert.ok(imageNodes(s).every(node => viewer.contains(node)));
  await s.setHistory(undefined); s.historyError = new Error('synthetic history unavailable');
  await s.resources[0].reload(); await s.flush(); assert.ok(s.root.querySelector('.empty.error')); assert.ok(viewer.isConnected);
  s.historyError = null; await s.setHistory(undefined); s.historyGate = deferred(); const pending = s.resources[0].reload(); await hostTurn(); await s.flush();
  assert.ok(s.root.querySelector('.loading')); assert.ok(imageNodes(s).every(node => viewer.contains(node)));
  s.historyGate.resolve(s.pageMessages()); await pending; s.historyGate = null; await s.flush(); assert.equal(readyURL(s), url);
  // Construction, physical detach and final geometry each have independent fault/recovery probes.
  for (const kind of ['construction', 'detach', 'finalization']) {
    const sentinel = kind === 'detach' ? false : new Error(kind), oldMessage = s.root.querySelector('.msg'); let fired = false;
    s.inject = (operation, node, key) => {
      const match = kind === 'construction' ? operation === 'createElement' && node === 'time' : kind === 'detach' ? operation === 'removeAttribute' && key === 'src' : operation === 'geometry' && node.matches('.composer,.thumb');
      if (!fired && match) { fired = true; throw sentinel; }
    };
    await s.redraw(); s.inject = null;
    assert.ok(fired, `${kind} reached its physical seam`);
    assert.ok(s.errors.some(error => error === sentinel), 'first current exception propagates through the real store callback channel');
    s.errors = s.errors.filter(error => error !== sentinel);
    if (kind === 'detach') { assert.ok(oldMessage.isConnected, 'failed detach aborts unsafe message replacement'); assert.ok(imageNodes(s).some(node => node.closest('.msg') === oldMessage)); }
    await s.redraw(); assert.equal(s.issued, issued, 'recovery preserves surviving demand without refetch'); assert.equal(readyURL(s), url);
    s.intersections(); await s.flush(); s.check();
  }
});

root('occurrence_pre_spill_and_focus', async t => {
  const spill = att(9, { name: 'message.md', media_type: 'text/markdown' });
  const svgDuplicate = att(1, { name: 'same-hash.svg', media_type: 'image/svg+xml' });
  const s = installFixture(t, { messages: [msg(1, [spill, att(1), att(1), svgDuplicate], { metadata: { 'vecnet.spilled': true } })] });
  await s.mount(); await finishVisible(s);
  let buttons = s.root.querySelectorAll('button.thumb'); assert.equal(buttons.length, 2); buttons[1].focus();
  await s.redraw(); buttons = s.root.querySelectorAll('button.thumb');
  assert.equal(s.doc.activeElement === buttons[1], true, 'redraw restores SECOND identical hash, not first');
  const keys = buttons.map(node => node.dataset.occurrence);
  assert.ok(keys.every(key => typeof key === 'string' && key.length)); assert.notEqual(keys[0], keys[1]);
  const issued = s.issued;
  s.messages = [msg(1, [spill, att(1, { name: 'renamed.png' }), att(1), svgDuplicate], { metadata: { 'vecnet.spilled': false } })];
  await s.setHistory(s.historyValue()); buttons = s.root.querySelectorAll('button.thumb');
  assert.equal(buttons.length, 2, 'eligible hash sharing cannot authorize the SVG occurrence');
  assert.deepEqual(buttons.map(node => node.dataset.occurrence), keys, 'original array indices survive spill filtering changes');
  assert.equal(s.issued, issued); buttons[1].click(); await s.flush();
  const dialog = s.doc.querySelector('dialog[open]'); assert.ok(dialog); await s.redraw(); dialog.close(); await s.flush();
  assert.equal(s.doc.activeElement === s.root.querySelectorAll('button.thumb')[1], true, 'viewer close restores the exact occurrence after redraw');
  s.root.querySelectorAll('button.thumb')[1].click(); await s.flush(); const second = s.doc.querySelector('dialog[open]');
  await s.setHistory({ messages: [], hosted: new Map(), states: new Map() }); second.close(); await s.flush();
  assert.ok(s.doc.activeElement.isConnected && s.root.contains(s.doc.activeElement), 'missing occurrence uses connected thread fallback'); s.check();
});

root('physical_sources_every_boundary', async t => {
  for (const premature of [true, false]) await t.test(premature ? 'fixture rejects non-last drop before detach' : 'fixture accepts detach before non-last drop', async sub => {
    const s = installFixture(sub, { limits: { N: 2 } }), images = [], onError = () => {};
    s.budget = createThumbBudget({ limits: s.limits });
    const cache = createThumbCache({ api: s.api, URL, budget: s.budget, onChange() {}, beforeRevoke() {
      for (const image of images) { image.removeEventListener('error', onError); image.removeAttribute('src'); }
    } });
    s.wrappers.add(cache);
    const leases = ['first', 'last'].map(key => cache.acquire(key, att(1), true, { priority: 'automatic' }));
    await s.finish(s.findCall(sha(1)));
    assert.ok(leases.every(lease => lease.state === 'ready' && lease.url === leases[0].url));
    for (const lease of leases) {
      const image = s.doc.createElement('img'); images.push(image); s.root.append(image);
      image.addEventListener('error', onError); image.src = lease.url;
    }
    assert.equal(s.sources.size, 2); assert.equal(s.budget.stats.sources, 2); assert.equal(s.made, 1); s.check();
    if (premature) cache.drop('first'); // Real non-last drop: no revoke or synchronous capacity callback.
    images[0].removeEventListener('error', onError); images[0].removeAttribute('src');
    if (premature) {
      assert.equal(s.attempts, 0, 'shared payload remains live during the ordering probe');
      assert.equal(s.errorOverflow, 0); assert.equal(s.errors.length, 1, 'only the intended pre-detach audit fails');
      const error = s.errors[0]; assert.ok(error instanceof assert.AssertionError);
      assert.equal(error.message, 'every physical src, disconnected included, owns a real permit');
      assert.equal(error.actual, false); assert.equal(error.expected, true);
      s.errors.shift(); // Consume exactly this asserted control failure; retain every other error.
    } else cache.drop('first');
    await s.flush(); s.check(); assert.equal(s.sources.size, 1); assert.equal(s.budget.stats.sources, 1);
    cache.drop('last'); await s.flush(); s.check(); assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0);
  });
  await t.test('wrapper duplicate inline and viewer boundary accounting', async sub => {
    const s = installFixture(sub, { limits: { N: 3 } }), thumbs = s.make(), rows = rowsOf([att(1), att(1)]);
    s.transaction(thumbs, rows); s.intersections(); await s.flush(); await finishVisible(s);
    assert.equal(s.sources.size, 2); assert.equal(s.budget.stats.sources, 2); assert.equal(s.made, 1);
    const first = s.root.querySelector('button.thumb'); first.click(); await s.flush();
    assert.equal(s.sources.size, 3); assert.equal(s.budget.stats.sources, 3, 'duplicate hashes do not combine physical permits');
    const url = readyURL(s), made = s.made, issued = s.issued;
    for (let i = 0; i < 5; i++) { s.transaction(thumbs, rows); s.intersections(); await s.flush(); s.check(); }
    assert.equal(s.made, made); assert.equal(s.issued, issued); assert.equal(readyURL(s), url);
    s.visibleChange('hidden'); assert.equal(s.sources.size, 1); assert.equal(s.budget.stats.sources, 1);
    thumbs.release(); assert.equal(s.sources.size, 0); assert.equal(s.budget.stats.sources, 0); assert.equal(s.urls.size, 0); s.check();
  });
});

root('before_revoke_all_consumers', async t => {
  const s = installFixture(t, { limits: { N: 4 } }), thumbs = s.make(), rows = rowsOf([att(1), att(1)]);
  s.transaction(thumbs, rows); s.intersections(); await s.flush(); await finishVisible(s);
  s.root.querySelector('button.thumb').click(); await s.flush();
  const oldImages = imageNodes(s), oldURL = readyURL(s), stale = oldImages.flatMap(node => node.handlers('error').map(fn => ({ fn, node })));
  const savedIO = [...s.observers].map(observer => ({ observer, entries: [...observer.targets].map(target => ({ target, rootBounds: rect(0, 0, s.width, s.height), boundingClientRect: target.getBoundingClientRect(), intersectionRect: target.getBoundingClientRect(), intersectionRatio: 1, isIntersecting: true })) }));
  assert.equal(oldImages.length, 3); assert.ok(stale.length);
  s.onRevoke = url => s.tap(() => {
    assert.equal(url, oldURL); assert.ok(oldImages.every(node => !node.hasAttribute('src') && node.handlers('error').length === 0));
  });
  oldImages[0].dispatchEvent(new FixtureEvent('error')); await s.flush();
  assert.equal(s.urls.has(oldURL), false); assert.ok(oldImages.every(node => !node.hasAttribute('src'))); s.onRevoke = null;
  thumbs.release(); const successor = s.make(); s.transaction(successor, rowsOf([att(1)])); s.intersections(); await s.flush(); await finishVisible(s);
  const currentURL = readyURL(s), calls = s.issued, attempts = s.attempts; assert.ok(currentURL && currentURL !== oldURL);
  for (const { fn, node } of stale) fn.call(node, new FixtureEvent('error', { target: node, currentTarget: node }));
  for (const { observer, entries } of savedIO) s.tap(() => observer.callback(entries, observer));
  s.intersections(); await s.flush(); assert.equal(readyURL(s), currentURL); assert.equal(s.issued, calls); assert.equal(s.attempts, attempts); s.check();
});

root('budget_M1_active_delivery_mutation', async () => {
  for (const variant of ['drop', 'replace']) {
    const budget = createThumbBudget({ limits: { ...BASE, N: 4 } }), events = [], errors = [];
    const tap = fn => () => { try { fn(); } catch (error) { errors.push(error); } };
    let first = true, b, c;
    const a = budget.claimSource('a', { onCapacity: tap(() => {
      events.push('a'); if (!first) return; first = false;
      if (variant === 'drop') b.drop(); else b.move('b', { onCapacity: tap(() => events.push('new-b')) });
      c = budget.claimSource('c', { onCapacity: tap(() => events.push('c')) });
    }) });
    b = budget.claimSource('b', { onCapacity: tap(() => events.push('old-b')) });
    let running; budget.enqueue({ key: 'trigger', priority: 'automatic', onStart: ticket => { running = ticket; }, onCancel() {} });
    running.settle({ residentBytes: null });
    const checkpoint = deferred(); queueMicrotask(() => { try { assert.deepEqual(events, variant === 'drop' ? ['a'] : ['a', 'new-b']); } catch (error) { errors.push(error); } checkpoint.resolve(); });
    await checkpoint.promise; await hostTurn();
    assert.deepEqual(events, variant === 'drop' ? ['a', 'a', 'c'] : ['a', 'new-b'], 'new source cannot extend current snapshot');
    assert.deepEqual(errors, []); a.drop(); b.drop(); c.drop(); await hostTurn();
    assert.deepEqual(budget.stats, { running: 0, residentEntries: 0, residentBytes: 0, reservedBytes: 0, queued: 0, sources: 0 });
  }
});

root('budget_M2_large_valid_positive', () => {
  const max = Number.MAX_SAFE_INTEGER, names = ['B', 'C', 'E', 'N', 'Q'];
  for (const elevated of [...names.map(name => [name]), names]) {
    const limits = { ...BASE, ...Object.fromEntries(elevated.map(name => [name, max])) }, budget = createThumbBudget({ limits });
    const tickets = [], permits = [], faults = [];
    for (let i = 0; i < 3; i++) {
      permits.push(budget.claimSource(`source-${i}`));
      budget.enqueue({ key: `ticket-${i}`, priority: 'automatic', onStart: ticket => tickets.push(ticket), onCancel() {} });
    }
    const check = () => {
      const q = budget.stats; for (const value of Object.values(q)) assert.ok(Number.isSafeInteger(value) && value >= 0);
      assert.equal(q.reservedBytes, q.running * limits.M); assert.ok(q.residentBytes + q.reservedBytes <= limits.B);
      assert.ok(q.running <= limits.C && q.running + q.residentEntries <= limits.E && q.queued <= limits.Q && q.sources <= limits.N);
    };
    try {
      check(); for (let i = 0; i < tickets.length; i++) { tickets[i].settle({ residentBytes: i % 2 }); check(); }
      assert.equal(tickets.length, 3); for (const ticket of tickets) ticket.dropPayload();
      for (const permit of permits) { assert.ok(permit); permit.drop(); } check();
      for (const value of Object.values(budget.stats)) assert.equal(value, 0);
    } catch (error) { faults.push(error); } finally { for (const ticket of tickets) { ticket.cancel(); ticket.settle({ residentBytes: null }); ticket.dropPayload(); } for (const permit of permits) permit?.drop(); }
    assert.deepEqual(faults, []);
  }
});

// Fixture conformance for Draft B's future Files/close tests, not a new viewer root.
async function focusOracle(s) {
  const back = s.root.querySelector('textarea'), body = s.aside.querySelector('.aside-body'), toggle = s.aside.querySelector('.aside-toggle');
  assert.ok(back && body && toggle); body.tabIndex = -1;
  const width = value => { s.width = value; globalThis.innerWidth = s.win.innerWidth = value; };
  width(1440); back.focus(); toggle.focus(); assert.equal(s.doc.activeElement === back, true, 'hidden desktop toggle refuses focus');
  width(390); body.focus(); assert.equal(s.doc.activeElement === back, true, 'collapsed narrow body refuses focus');
  toggle.click(); body.focus(); assert.equal(s.doc.activeElement === body, true); assert.ok(s.visible(body)); assert.equal(s.scrolled === body, true);
  body.remove(); back.focus(); body.focus(); assert.equal(s.doc.activeElement === back, true, 'disconnected target refuses focus'); s.aside.append(body);
  const disabled = s.doc.createElement('button'); s.root.append(disabled); disabled.disabled = true; disabled.focus(); assert.equal(s.doc.activeElement === back, true);
  disabled.disabled = false; disabled.inert = true; disabled.focus(); assert.equal(s.doc.activeElement === back, true); disabled.remove();
  const plain = s.doc.createElement('div'); s.root.append(plain); plain.focus(); assert.equal(s.doc.activeElement === back, true, 'nonfocusable container needs tabindex'); plain.tabIndex = -1; plain.focus(); assert.equal(s.doc.activeElement === plain, true); plain.remove();
  const dialog = s.doc.createElement('dialog'); s.doc.body.append(dialog); dialog.showModal(); back.focus(); assert.equal(s.doc.activeElement === dialog, true, 'open modal excludes background focus');
  dialog.close(); assert.ok(s.tasks.length); dialog.remove(); await s.flush(); width(1440);
}

root('history_unread_download_controls', async t => {
  const spill = att(90, { name: 'message.md', media_type: 'text/markdown' }), reference = att(91, { origin_url: 'https://example.invalid/image.png' });
  const r2 = att(92, { blob_id: 'blo_test', retention_class: 'keep', expires_at: null, sha256_verified: true, integrity: { algorithm: 'sha256', value: sha(92), verified: true } });
  const messages = Array.from({ length: 50 }, (_, i) => msg(i + 1, i === 24 ? [spill, att(1), reference, r2] : [], { ...(i === 24 ? { metadata: { 'vecnet.spilled': true } } : {}), ...(i === 49 ? { in_reply_to: 'msg_1' } : {}) }));
  const s = installFixture(t, { messages, unread: ['msg_25'] });
  s.boxes.set('m-msg_25', rect(24, 3200, 240, 180));
  s.pageMessages = ({ after }) => {
    const i = after ? Number(after) : 0, m = messages[i];
    return { messages: [m], hosted_blobs: { [m.message_id]: m.attachments.filter(a => !a.origin_url).map(a => a.sha256) }, blob_states: { blo_test: { state: 'available', ...r2 } }, next_after: String(i + 1) };
  };
  s.pageMatrix = ({ after }) => { const i = after ? Number(after) : 0; return { recipients: ['peer_op'], rows: [s.matrixValue().rows[i]], next_after: String(i + 1) }; };
  await s.mount();
  assert.equal(s.messagePages.length, 50); assert.equal(s.matrixPages.length, 50);
  for (const pages of [s.messagePages, s.matrixPages]) { assert.ok(pages.every(page => page.limit === 200)); assert.deepEqual(pages.map(page => page.after), [undefined, ...Array.from({ length: 49 }, (_, i) => String(i + 1))]); }
  assert.equal(s.root.querySelectorAll('.msg').length, 50); assert.ok(s.root.querySelector('.newdiv')); assert.equal(s.lastScroll.block, 'start');
  assert.equal(s.starts.has(reference.sha256), false); assert.equal(s.starts.has(r2.sha256), false); assert.equal(s.starts.has(spill.sha256), false);
  assert.ok(s.root.querySelector('.spill')); assert.ok(s.root.querySelector('[data-blob="blo_test"]'));
  await s.advance(DWELL_MS + BATCH_MS + 1); assert.deepEqual(s.receipts, [], 'opening/layout alone is not a read');
  s.visibleChange('hidden'); s.intersections(node => node.id === 'm-msg_25'); await s.advance(DWELL_MS + BATCH_MS + 1); assert.deepEqual(s.receipts, []);
  s.visibleChange('visible');
  const modal = s.doc.createElement('dialog'); s.doc.body.append(modal); modal.showModal();
  s.intersections(node => node.id === 'm-msg_25'); await s.advance(DWELL_MS + BATCH_MS + 1); assert.deepEqual(s.receipts, [], 'modal hides reader');
  modal.close(); await s.flush(); modal.remove(); s.intersections(node => node.id === 'm-msg_25');
  await s.advance(DWELL_MS - 1); assert.deepEqual(s.receipts, []);
  await s.advance(1 + BATCH_MS); assert.deepEqual(s.receipts, [['dly_msg_25', 'read']]); assert.equal(s.markAll, 0);
  const quote = s.doc.getElementById('m-msg_50').querySelector('.reply'); assert.ok(quote); quote.focus(); s.press(quote, 'Enter'); assert.deepEqual(s.lastScroll, { id: 'm-msg_1', block: 'center' });
  s.detail.files = [att(93, { sender: 'peer_other', created_at: messages[0].created_at }), { ...r2, sender: 'peer_other', created_at: messages[0].created_at }];
  await s.asideMount(); await focusOracle(s);
  const plain = s.aside.querySelector(`[data-sha="${sha(93)}"]`); assert.ok(plain); plain.click();
  await s.finish(s.findCall(sha(93)), new Blob(['original bytes'], { type: 'application/octet-stream' }));
  assert.equal(s.downloadClicks, 1); assert.ok([...s.urls.values()].some(value => value.type === 'application/octet-stream' && value.size === 14), 'original bytes reach the download URL'); const downloadsBefore = s.downloads;
  s.aside.querySelector('[data-blob="blo_test"]').click(); await s.flush(); assert.equal(s.downloads, downloadsBefore + 1); assert.equal(s.downloadClicks, 2);
  await s.advance(1001); assert.ok([...s.urls.values()].every(value => value.type !== 'application/octet-stream'), 'aside retires its own download URLs'); s.check();
});

// Draft B: only roots10–13. Controls use accessible names, not product internals.
function viewerControl(s, scope, name) {
  const node = scope.querySelectorAll('button,[role="button"],a').find(el => name.test(el.getAttribute('aria-label') || el.textContent));
  assert.equal(!!node, true, `reachable viewer control ${name}`); assert.ok(s.visible(node) && !node.disabled); return node;
}
function liveViewer(s) {
  const dialog = s.doc.querySelectorAll('dialog[open]').at(-1);
  assert.equal(!!dialog, true, 'Open creates a current dialog'); assert.ok(dialog.isConnected); return dialog;
}
function escapeViewer(dialog) {
  // Browser Escape delivers cancellable cancel, then default close if allowed.
  if (dialog.dispatchEvent(new FixtureEvent('cancel', { cancelable: true }))) dialog.close();
}
function viewerCounts(s, thumbs, owners, physical) {
  s.check(); assert.equal(thumbs.stats.owners, owners); assert.equal(s.budget.stats.sources, owners); assert.equal(s.sources.size, physical);
}

root('viewer_N1_transfer_return', async t => {
  for (const N of [1, 2]) await t.test(`ready return N=${N}, N+2 cycles`, async sub => {
    const s = installFixture(sub, { limits: { N } }), thumbs = s.make(), rows = rowsOf([att(1)]);
    s.transaction(thumbs, rows); s.intersections(); await s.flush(); await finishVisible(s);
    const url = readyURL(s), baseline = [s.issued, s.made, s.attempts]; assert.ok(url); viewerCounts(s, thumbs, 1, 1);
    for (let cycle = 0; cycle < N + 2; cycle++) {
      const opener = s.root.querySelector('button.thumb'); assert.ok(opener); opener.focus(); opener.click(); await s.flush();
      const dialog = liveViewer(s); assert.equal(s.doc.activeElement === dialog || dialog.contains(s.doc.activeElement), true);
      viewerCounts(s, thumbs, N, N); assert.equal(dialog.querySelector('img')?.src, url);
      assert.equal(s.root.querySelectorAll('img[src]').length, N === 1 ? 0 : 1);
      s.inject = () => s.tap(() => assert.ok(s.budget.stats.sources >= 1, 'no zero-owner gap at a physical transfer boundary'));
      s.transaction(thumbs, rows); s.intersections(); await s.flush(); viewerCounts(s, thumbs, N, N);
      viewerControl(s, dialog, /^(Close|Cancel)( image)?$/i).click(); s.inject = null;
      viewerCounts(s, thumbs, 1, 1); assert.equal(readyURL(s), url); assert.equal(s.budget.stats.queued, 0);
      await s.flush(); assert.equal(s.doc.activeElement === s.root.querySelector('button.thumb'), true);
      assert.deepEqual([s.issued, s.made, s.attempts], baseline, 'redraw/close preserves payload and generation without reacquire');
    }
  });
  await t.test('real occupied-target/null and exact lease generation controls', async sub => {
    const s = installFixture(sub, { limits: { N: 2 } }); s.budget = createThumbBudget({ limits: s.limits });
    const cache = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} }); s.wrappers.add(cache);
    const k = cache.acquire('K', att(1), true, { priority: 'automatic' }); await s.finish(s.findCall(sha(1)));
    const v = cache.acquire('V', att(1), true, { priority: 'explicit' }), url = k.url, generation = k.generation, baseline = [s.issued, s.made, s.attempts];
    assert.equal(cache.moveOwner('V', 'K', { priority: 'automatic' }) === null, true); assert.equal(cache.stats.owners, 2);
    assert.equal(k.state, 'ready'); assert.equal(v.state, 'ready'); cache.drop('V'); assert.equal(cache.stats.owners, 1);
    for (let i = 0; i < 4; i++) {
      assert.equal(cache.moveOwner('K', 'V', { priority: 'explicit' }) === k, true);
      assert.equal(cache.moveOwner('V', 'K', { priority: 'automatic' }) === k, true);
      assert.equal(k.url, url); assert.equal(k.generation, generation); assert.equal(s.budget.stats.sources, 1);
    }
    assert.deepEqual([s.issued, s.made, s.attempts], baseline); cache.drop('K'); s.check();
  });
  await t.test('only local opener transfers back with automatic queued priority', async sub => {
    const s = installFixture(sub, { limits: { B: 16, C: 1, E: 8, N: 2, Q: 1 } }); s.budget = createThumbBudget({ limits: s.limits });
    const pressureCache = () => { const cache = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} }); s.wrappers.add(cache); return cache; };
    const blocker = pressureCache(); blocker.acquire('held', att(90), true, { priority: 'explicit' }); const held = s.findCall(sha(90));
    const thumbs = s.make(), rows = rowsOf([att(1)]); s.transaction(thumbs, rows); await s.flush();
    assert.equal(s.budget.stats.sources, 2); assert.equal(s.budget.stats.queued, 1);
    const opener = s.root.querySelector('button.thumb'); assert.ok(opener); opener.focus(); s.press(opener, 'Enter'); await s.flush();
    const dialog = liveViewer(s); assert.match(dialog.textContent, /Waiting for earlier image requests/);
    assert.equal(thumbs.stats.owners, 1, 'external permit cannot be reclaimed: transfer the only local opener');
    viewerControl(s, dialog, /^(Close|Cancel)( image)?$/i).click(); assert.equal(thumbs.stats.owners, 1); assert.equal(s.budget.stats.queued, 1);
    blocker.release(); assert.equal(held.signal.aborted, true); assert.equal(s.budget.stats.running, 1);
    const pressure = pressureCache(); pressure.acquire('replacement', att(20), true, { priority: 'explicit' }); await s.flush();
    assert.equal(thumbs.stats.owners, 0, 'new explicit job displaces the returned AUTOMATIC queued owner');
    assert.equal(s.starts.has(sha(1)), false); assert.equal(s.budget.stats.queued, 1); assert.equal(s.issued, 1);
    await s.finish(held, null); assert.ok(s.findCall(sha(20))); await s.finish(s.findCall(sha(20))); pressure.release(); await s.flush(); s.check();
  });
  for (const mode of ['absent', 'ineligible']) await t.test(`close after occurrence becomes ${mode}`, async sub => {
    const s = installFixture(sub, { limits: { N: 1 } }), thumbs = s.make(), rows = rowsOf([att(1)]);
    s.transaction(thumbs, rows); await s.flush(); await finishVisible(s); s.root.querySelector('button.thumb').click(); await s.flush();
    const dialog = liveViewer(s); s.transaction(thumbs, mode === 'absent' ? [] : [{ ...rows[0], att: att(1, { media_type: 'image/svg+xml' }) }]);
    viewerControl(s, dialog, /^(Close|Cancel)( image)?$/i).click(); viewerCounts(s, thumbs, 0, 0);
    assert.equal(s.budget.stats.queued, 0); assert.equal(s.urls.size, 0); await s.flush();
    assert.ok(s.doc.activeElement.isConnected && s.root.contains(s.doc.activeElement), 'no SHA sibling or disconnected focus fallback'); s.check();
  });
  for (const action of ['release', 'redraw', 'open']) await t.test(`physical viewer detach reenters ${action}`, async sub => {
    const s = installFixture(sub, { limits: { N: 3 } }), thumbs = s.make(), rows = rowsOf([att(1), att(2)]);
    s.transaction(thumbs, rows); await s.flush(); await finishVisible(s); s.root.querySelector('button.thumb').click(); await s.flush();
    const old = liveViewer(s); let fired = false;
    s.inject = (operation, node, key) => {
      if (fired || operation !== 'removeAttribute' || key !== 'src' || !old.contains(node)) return;
      fired = true; s.inject = null;
      if (action === 'release') thumbs.release();
      else if (action === 'redraw') s.transaction(thumbs, rows);
      else { const next = s.root.querySelectorAll('button.thumb')[1]; thumbs.open(rows[1].att, next, true, rows[1].key); }
    };
    viewerControl(s, old, /^(Close|Cancel)( image)?$/i).click(); s.inject = null; assert.equal(fired, true); await s.flush();
    if (action === 'release') { viewerCounts(s, thumbs, 0, 0); assert.equal(s.urls.size, 0); }
    else if (action === 'redraw') viewerCounts(s, thumbs, 2, 2);
    else {
      const next = liveViewer(s); assert.equal(next === old, false); viewerCounts(s, thumbs, 3, 3);
      assert.equal(next.querySelector('img')?.src, s.root.querySelectorAll('button.thumb img')[1]?.src);
      assert.equal(next.contains(s.doc.activeElement) || s.doc.activeElement === next, true);
    }
    assert.equal(s.budget.stats.queued, 0); s.check();
  });
});

root('viewer_priority_wait_cancel_switch', async t => {
  await t.test('fixture focus admits only the topmost of two appended modals', async sub => {
    const s = installFixture(sub), first = s.doc.createElement('dialog'), second = s.doc.createElement('dialog');
    s.doc.body.append(first, second); first.showModal(); assert.equal(s.doc.activeElement === first, true);
    second.showModal(); assert.equal(s.doc.activeElement === second, true); first.focus(); assert.equal(s.doc.activeElement === second, true);
    second.close(); first.focus(); assert.equal(s.doc.activeElement === first, true); first.close(); first.remove(); second.remove(); await s.flush(); s.check();
  });
  for (const scenario of ['FIFO protects accepted explicit', 'Escape Cancel switch keep running charge']) await t.test(scenario, async sub => {
    const s = installFixture(sub, { limits: { B: 32, C: 1, E: 8, N: 8, Q: 2 } }), a = s.make(), b = s.make();
    const ar = rowsOf([att(1), att(2), att(5)]), br = rowsOf([att(3), att(4)]), hostB = s.doc.createElement('section'); s.main.append(hostB);
    for (const row of br) s.boxes.set(row.key, rect(24, 10000, 240, 180));
    s.transaction(a, ar); s.transaction(b, br, hostB); await s.flush();
    assert.equal(s.issued, 1); assert.equal(s.budget.stats.queued, 2); const held = s.findCall(sha(1)); assert.ok(held);
    b.open(br[0].att, hostB.querySelector('[data-occurrence]'), true, br[0].key); await s.flush(); const bd = liveViewer(s);
    assert.match(bd.textContent, /Waiting for earlier image requests/); assert.equal(bd.querySelectorAll('img[src]').length, 0);
    a.open(ar[1].att, s.root.querySelectorAll('[data-occurrence]')[1], true, ar[1].key); await s.flush(); const ad = liveViewer(s);
    assert.equal(ad === bd, false); assert.equal(s.doc.activeElement === ad || ad.contains(s.doc.activeElement), true);
    assert.match(ad.textContent, /Waiting for earlier image requests/); assert.equal(s.budget.stats.queued, 2); assert.equal(s.issued, 1);
    viewerControl(s, ad, /^Retry( image)?$/i); viewerControl(s, ad, /^Files$/i);
    if (scenario.startsWith('FIFO')) {
      const extra = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} }); s.wrappers.add(extra);
      const denied = extra.acquire('denied-extra-explicit', att(99), true, { priority: 'explicit' }); assert.equal(denied.state, 'waiting');
      assert.equal(s.budget.stats.queued, 2); extra.release(); assert.equal(s.issued, 1, 'full explicit queue is not preempted');
      await s.finish(held); assert.ok(s.findCall(sha(3)), 'earlier accepted explicit dispatches ahead of automatic and later upgrade');
      assert.equal(s.starts.has(sha(2)), false); assert.equal(s.starts.has(sha(5)), false); assert.match(bd.textContent, /Loading image/);
      await s.finish(s.findCall(sha(3))); assert.ok(s.findCall(sha(2)), 'explicit upgrades retain explicit acceptance FIFO');
      await s.finish(s.findCall(sha(2))); viewerControl(s, ad, /^(Close|Cancel)( image)?$/i).click(); viewerControl(s, bd, /^(Close|Cancel)( image)?$/i).click();
    } else {
      s.transaction(a, []); escapeViewer(ad); assert.equal(a.stats.owners, 0); assert.equal(s.budget.stats.queued, 1);
      assert.ok(held.signal.aborted && held.aborts === 1); assert.equal(s.budget.stats.running, 1); assert.equal(s.budget.stats.reservedBytes, s.limits.M);
      b.open(br[1].att, hostB.querySelectorAll('[data-occurrence]')[1], true, br[1].key); const switched = liveViewer(s);
      assert.equal(switched === bd, false); assert.equal(s.budget.stats.queued, 1); assert.equal(s.starts.has(sha(3)), false);
      viewerControl(s, switched, /^(Cancel|Close)( image)?$/i).click(); assert.equal(b.stats.owners, 0); assert.equal(s.budget.stats.queued, 0);
      b.open(br[0].att, hostB.querySelector('[data-occurrence]'), true, br[0].key); const waiting = liveViewer(s);
      assert.match(waiting.textContent, /Waiting for earlier image requests/); assert.equal(s.issued, 1);
      held.body.resolve(null); await hostTurn(); assert.equal(s.issued, 1); assert.equal(s.budget.stats.reservedBytes, s.limits.M, 'body/abort is not terminal credit');
      await s.finish(held, null); assert.ok(s.findCall(sha(3))); assert.equal(liveViewer(s) === waiting, true); await s.finish(s.findCall(sha(3)));
      viewerControl(s, waiting, /^(Close|Cancel)( image)?$/i).click(); assert.equal(b.stats.owners, 0);
    }
    await s.flush(); s.check(); assert.equal(s.budget.stats.queued, 0);
  });
});

root('viewer_error_retry_files', async t => {
  for (const width of [1440, 390]) for (const detail of ['loading', 'failed', 'empty', 'populated']) await t.test(`Files ${width} ${detail}`, async sub => {
    const s = installFixture(sub, { width }); await s.mount(); await finishVisible(s);
    if (detail === 'loading') s.detailGate = deferred();
    if (detail === 'failed') s.detailError = new Error('synthetic details unavailable');
    if (detail === 'populated') s.detail.files = [att(9, { sender: 'peer_other', created_at: s.messages[0].created_at })];
    const threadRequest = s.api.thread; let detailReads = 0;
    s.api.thread = (...args) => { detailReads++; return threadRequest(...args); }; // Observe, preserving the actual fixture request/gate/result.
    await s.asideMount({ settle: detail !== 'loading' });
    const body = s.aside.querySelector('.aside-body'), toggle = s.aside.querySelector('.aside-toggle'), back = s.root.querySelector('textarea'); let toggles = 0;
    assert.equal(!!body.querySelector({ loading: '.loading', failed: '.empty.error', empty: '.none', populated: '.files' }[detail]), true, 'actual aside details state');
    toggle.addEventListener('click', () => { toggles++; }); back.focus();
    if (width === 1440) { toggle.focus(); assert.equal(s.doc.activeElement === back, true, 'desktop toggle is hidden'); }
    else { body.tabIndex = 7; body.focus(); assert.equal(s.doc.activeElement === back, true, 'collapsed body cannot focus'); }
    const inline = s.sources.size, downloads = s.downloads, clicks = s.downloadClicks, content = body.textContent, reads = detailReads;
    const opener = s.root.querySelector('button.thumb'); assert.ok(opener); opener.focus(); s.press(opener, 'Enter'); await s.flush();
    const dialog = liveViewer(s), issued = s.issued; opener.click(); await s.flush();
    assert.equal(liveViewer(s) === dialog, true, 'repeat pointer Open preserves intent'); assert.equal(s.issued, issued);
    viewerControl(s, dialog, /^Files$/i).click();
    const destination = () => s.aside.querySelector('.files') || s.aside.querySelector('.aside-body');
    const check = () => {
      const target = destination(); assert.ok(target?.isConnected && s.visible(target)); assert.equal(s.doc.activeElement === target, true);
      assert.equal(s.scrolled === target, true); assert.equal(s.doc.querySelectorAll('dialog[open]').length, 0);
      assert.equal(s.sources.size, inline); assert.equal(s.activeCalls.size, 0); assert.equal(s.downloads, downloads); assert.equal(s.downloadClicks, clicks);
      assert.equal(toggles, width === 390 ? 1 : 0); assert.equal(body.textContent, content); assert.equal(detailReads, reads, 'Files does not reload details');
      assert.equal(s.doc.activeElement === toggle, false); if (width === 390 && target === body) assert.equal(body.tabIndex, 7);
    };
    check(); await s.flush(); await s.redraw(); check();
    // A second handoff starts with the narrow panel already open: no toggle churn.
    s.root.querySelector('button.thumb').click(); await s.flush(); viewerControl(s, liveViewer(s), /^Files$/i).click(); check();
    s.root.querySelector('button.thumb').click(); const successor = liveViewer(s); await s.flush();
    assert.equal(liveViewer(s) === successor, true); assert.equal(s.doc.activeElement === successor || successor.contains(s.doc.activeElement), true, 'delayed Files close cannot steal a new intent focus');
    viewerControl(s, successor, /^Files$/i).click(); check(); await s.flush(); check();
    if (s.detailGate) { s.detailGate.resolve(s.detail); s.detailGate = null; await s.store.settled(); assert.equal(detailReads, reads, 'no reload deferred behind held details'); }
    s.check();
  });
  for (const failure of ['transport', 'decode', 'capability']) await t.test(`${failure} keeps reachable controls`, async sub => {
    const s = installFixture(sub);
    if (failure === 'capability') delete s.api.thumbnailBlob;
    await s.mount();
    if (failure === 'capability') {
      assert.equal(s.issued, 0, 'missing bounded transport capability never falls back to eager blob');
      await s.asideMount(); const opener = s.root.querySelector('button.thumb'); assert.equal(!!opener, true, 'capability failure keeps keyboard Open');
      s.press(opener, 'Enter'); await s.flush(); const dialog = liveViewer(s); assert.match(dialog.textContent, /unavailable|unsupported|cannot|not supported/i);
      viewerControl(s, dialog, /^Files$/i).click(); assert.ok(s.visible(s.doc.activeElement) && s.aside.contains(s.doc.activeElement));
      assert.equal(s.issued, 0); assert.equal(s.doc.querySelectorAll('dialog[open]').length, 0); s.check(); return;
    }
    if (failure === 'decode') await finishVisible(s);
    const opener = s.root.querySelector('button.thumb'); assert.equal(!!opener, true, 'deferred/loading image retains keyboard Open');
    s.press(opener, ' '); await s.flush(); const dialog = liveViewer(s);
    if (failure === 'transport') await s.finish(s.findCall(sha(1)), new Error('bounded request failed'), true);
    else { const image = dialog.querySelector('img'); assert.ok(image); image.dispatchEvent(new FixtureEvent('error')); await s.flush(); }
    const issued = s.issued; opener.click(); await s.flush(); assert.equal(liveViewer(s) === dialog, true); assert.equal(s.issued, issued, 'repeat error Open is inert');
    assert.equal(dialog.querySelectorAll('img[src]').length, 0); viewerControl(s, dialog, /^Files$/i);
    viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush(); assert.equal(s.issued, issued + 1, 'one retry dispatch, no duplicate request');
    opener.click(); await s.flush(); assert.equal(s.issued, issued + 1); assert.equal(liveViewer(s) === dialog, true);
    await s.finish(s.findCall(sha(1))); assert.ok(dialog.querySelector('img[src]')); viewerControl(s, dialog, /^(Close|Cancel)( image)?$/i).click(); await s.flush(); s.check();
  });
  await t.test('Retry performs one real cleanup pass while queued; pending Files retires ownership', async sub => {
    const s = installFixture(sub, { limits: { B: 4, C: 1, E: 1, N: 2, Q: 1 } }); s.budget = createThumbBudget({ limits: s.limits });
    const orphan = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} }); s.wrappers.add(orphan);
    orphan.acquire('late', att(90), true, { priority: 'automatic' });
    s.onCreate = () => { s.onCreate = null; orphan.release(); }; s.onRevoke = () => { throw false; };
    await s.finish(s.findCall(sha(90))); assert.equal(s.budget.stats.running, 1); assert.equal(s.budget.stats.sources, 0); const attempts = s.attempts; assert.equal(attempts, 1);
    const thumbs = s.make(), rows = rowsOf([att(7)]); s.boxes.set(rows[0].key, rect(24, 10000, 240, 180)); s.transaction(thumbs, rows); await s.flush(); await s.asideMount();
    thumbs.open(rows[0].att, s.root.querySelector('[data-occurrence]'), true, rows[0].key); const dialog = liveViewer(s); await s.flush();
    assert.match(dialog.textContent, /Waiting for earlier image requests/); const issued = s.issued;
    thumbs.open(rows[0].att, s.root.querySelector('[data-occurrence]'), true, rows[0].key); s.intersections(); s.resize(); await s.flush();
    assert.equal(liveViewer(s) === dialog, true); assert.equal(s.attempts, attempts, 'Open/repeat Open/observer/capacity work never runs cleanup');
    viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush(); assert.equal(s.attempts, attempts + 1, 'exactly one old-resource pass per explicit Retry');
    assert.equal(s.issued, issued); assert.equal(s.budget.stats.reservedBytes, s.limits.M); assert.equal(s.budget.stats.queued, 1); assert.match(dialog.textContent, /Waiting for earlier image requests/);
    s.onRevoke = null; viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush(); assert.equal(s.attempts, attempts + 2); assert.ok(s.findCall(sha(7)));
    const call = s.findCall(sha(7)); viewerControl(s, dialog, /^Files$/i).click(); viewerCounts(s, thumbs, 0, 0);
    assert.equal(call.signal.aborted, true); assert.equal(s.budget.stats.running, 1); assert.equal(s.budget.stats.queued, 0);
    assert.ok(s.aside.contains(s.doc.activeElement) && s.visible(s.doc.activeElement)); await s.finish(call, null); assert.equal(s.budget.stats.running, 0); s.check();
  });
});

root('stale_callbacks_no_successor_effect', async t => {
  const s = installFixture(t, { limits: { N: 2 } }), thumbs = s.make(), rows = rowsOf([att(1)]);
  s.transaction(thumbs, rows); await s.flush(); await finishVisible(s); s.root.querySelector('button.thumb').click(); await s.flush();
  const old = liveViewer(s), images = imageNodes(s), oldURL = old.querySelector('img')?.src;
  const errors = images.flatMap(node => node.handlers('error').map(fn => ({ node, fn }))), closes = old.handlers('close'), cancels = old.handlers('cancel');
  assert.ok(errors.length && closes.length); scrollEvent(s); s.resize(); assert.ok(s.raf.size, 'real demand events schedule a frame');
  const frames = [...s.raf.values()], observers = [...s.observers].map(observer => ({ observer, entries: [...observer.targets].map(target => ({ target, rootBounds: rect(0, 0, s.width, s.height), boundingClientRect: target.getBoundingClientRect(), intersectionRect: target.getBoundingClientRect(), intersectionRatio: 1, isIntersecting: true })) }));
  assert.ok(frames.length <= 16 && observers.length > 0 && observers.length <= 16); assert.equal(images.length, 2); s.transaction(thumbs, []);
  viewerControl(s, old, /^(Close|Cancel)( image)?$/i).click(); assert.equal(s.urls.has(oldURL), false);
  // A new SAME-hash generation/intent exists before delayed old close/frame delivery.
  const nodes = s.transaction(thumbs, rows); thumbs.open(rows[0].att, nodes[0], true, rows[0].key); const next = liveViewer(s);
  assert.equal(next === old, false); await finishVisible(s); assert.equal(liveViewer(s) === next, true);
  const url = next.querySelector('img')?.src; assert.ok(url && url !== oldURL); assert.ok(images.every(node => !node.isConnected && !node.hasAttribute('src')));
  const focus = s.doc.activeElement, quotas = { ...s.budget.stats }, owners = thumbs.stats.owners, counts = [s.issued, s.made, s.attempts], text = next.textContent;
  for (const { node, fn } of errors) s.tap(() => fn.call(node, new FixtureEvent('error', { target: node, currentTarget: node })));
  for (const fn of closes) s.tap(() => fn.call(old, new FixtureEvent('close', { target: old, currentTarget: old })));
  for (const fn of cancels) s.tap(() => fn.call(old, new FixtureEvent('cancel', { target: old, currentTarget: old, cancelable: true })));
  for (const { observer, entries } of observers) s.tap(() => observer.callback(entries, observer));
  for (const fn of frames) s.tap(() => fn(s.clock)); await s.flush();
  assert.equal(liveViewer(s) === next, true); assert.equal(next.querySelector('img')?.src, url); assert.equal(next.textContent, text);
  assert.equal(s.doc.activeElement === focus, true); assert.equal(thumbs.stats.owners, owners); assert.deepEqual(s.budget.stats, quotas);
  assert.deepEqual([s.issued, s.made, s.attempts], counts); s.check();
});

// Final lifecycle draft: current handles only; discarded callers have no retry registry.
function quotaFields(s, expected) {
  for (const [key, value] of Object.entries(expected)) assert.equal(s.budget.stats[key], value, key);
  s.check();
}
function liveListeners(s) {
  const count = target => { let live = 0; for (const list of target?.listeners.values() || []) live += list.length; return live; };
  return { document: count(s.doc), window: count(s.win), scroller: count(s.main), root: count(s.root), body: count(s.doc.body), composer: count(s.standaloneComposer), threadComposer: count(s.root.querySelector('.composer')) };
}
function retiredListeners(s, baseline) { assert.deepEqual(liveListeners(s), baseline, 'persistent live listeners return to pre-mount baseline'); }
function retiredFixture(s, baseline) {
  assert.equal(s.observers.size + s.resizeObservers.size + s.raf.size + s.timers.size, 0, 'retired schedulers release their callbacks');
  assert.equal(s.doc.querySelectorAll('dialog[open]').length, 0);
  retiredListeners(s, baseline);
}
async function unpublishedFailure(s, number, failure) {
  const listeners = liveListeners(s); // Preserve any genuine current wrapper's listeners while retiring this cache.
  let cache = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} });
  s.wrappers.add(cache); cache.acquire('old', att(number), true, { priority: 'automatic' });
  const made = s.made, physical = s.sources.size;
  s.onCreate = () => {
    s.onCreate = null; cache.release(); s.forgetWrapper(cache); cache = null; s.tap(() => retiredListeners(s, listeners));
  };
  s.onRevoke = () => { throw failure; };
  await s.finish(s.findCall(sha(number)));
  assert.equal(cache === null, true, 'old public cache and fixture teardown handle are forgotten');
  retiredListeners(s, listeners);
  assert.equal(s.made, made + 1); assert.equal(s.urls.size > 0, true); assert.equal(s.sources.size, physical, 'unpublished cleanup preserves independent physical consumers');
  s.onRevoke = null; return [...s.urls.keys()].at(-1);
}
function openThreadViewer(s, number = 1) {
  const opener = s.root.querySelector(`button.thumb[data-sha="${sha(number)}"]`);
  assert.equal(!!opener, true, 'current deferred/loading thread occurrence has an accessible Open control');
  s.press(opener, 'Enter'); return liveViewer(s);
}

root('release_repeat_published_failure', async t => {
  await t.test('retirement oracle detects an inert persistent listener', sub => {
    const s = installFixture(sub), listeners = liveListeners(s), dead = true, inert = () => { if (dead) return; };
    retiredFixture(s, listeners);
    try {
      s.win.addEventListener('resize', inert);
      assert.throws(() => retiredFixture(s, listeners), error => {
        assert.equal(error instanceof assert.AssertionError, true);
        assert.deepEqual(error.actual, { ...listeners, window: listeners.window + 1 }); assert.deepEqual(error.expected, listeners); return true;
      });
    } finally { s.win.removeEventListener('resize', inert); retiredFixture(s, listeners); s.check(); }
  });
  for (const mount of ['wrapper', 'thread']) for (const at of ['detach', 'revoke']) for (const failure of [false, undefined]) {
    await t.test(`${mount} ${at} throws ${String(failure)}`, async sub => {
      const s = installFixture(sub, { messages: [msg(1, [att(1), att(2), att(3), att(4)])] }), listeners = liveListeners(s);
      let thumbs = null, off;
      if (mount === 'thread') off = await s.mount();
      else { thumbs = s.make(); s.transaction(thumbs, rowsOf([att(1), att(2), att(3), att(4)])); off = () => thumbs.release(); await s.flush(); }
      await s.finish(s.findCall(sha(1))); await s.finish(s.findCall(sha(2))); await s.finish(s.findCall(sha(3)));
      const held = s.findCall(sha(4)); assert.ok(held, 'independent running work is present before teardown');
      const opener = s.root.querySelector(`button.thumb[data-sha="${sha(1)}"]`); assert.ok(opener); opener.click(); await s.flush();
      const badURL = s.root.querySelector('button.thumb img').src, secondURL = s.root.querySelector(`button.thumb[data-sha="${sha(2)}"] img`).src;
      const goodURL = s.root.querySelector(`button.thumb[data-sha="${sha(3)}"] img`).src; let firstSeen = false, firstValue;
      assert.ok(goodURL); assert.equal(imageNodes(s).filter(node => node.src === badURL).length, 2);
      const fail = url => { const value = url === badURL ? failure : failure === false ? undefined : false; if (!firstSeen) { firstSeen = true; firstValue = value; } throw value; };
      const releaseThrowsFirst = () => {
        firstSeen = false; let threw = false, caught;
        try { off(); } catch (error) { threw = true; caught = error; }
        assert.equal(threw && firstSeen, true, 'release propagates a current failure, including undefined'); assert.equal(caught, firstValue);
      };
      if (at === 'detach') s.inject = (op, node, key) => { if (op === 'removeAttribute' && key === 'src' && (node.src === badURL || node.src === secondURL)) fail(node.src); };
      else s.onRevoke = url => { if (url === badURL || url === secondURL) fail(url); };
      releaseThrowsFirst(); retiredFixture(s, listeners); await s.flush();
      assert.equal(s.urls.has(badURL), true, 'failed published URL stays owned');
      assert.equal(s.urls.has(secondURL), true, 'later independent failure remains owned too');
      assert.equal(s.urls.has(goodURL), false, 'independent published cleanup continues');
      assert.equal(held.signal?.aborted, true, 'independent running transport is aborted, not credited');
      if (at === 'detach') assert.ok(imageNodes(s).some(node => node.src === badURL));
      else assert.equal(s.sources.size, 0, 'successful detach alone cannot credit failed revoke');
      retiredFixture(s, listeners);
      if (thumbs) {
        assert.equal(thumbs.stats.residentEntries, 2); assert.equal(thumbs.stats.residentBytes, 2);
        assert.ok(thumbs.stats.owners >= 2); quotaFields(s, { running: 1, reservedBytes: 4, residentEntries: 2, residentBytes: 2 });
        assert.ok(s.budget.stats.sources >= 2, 'published source permits retained until disposal succeeds');
        const attempts = s.attempts; retryThumbCleanup(s.budget); assert.equal(s.attempts, attempts, 'document cleanup cannot bypass published detach');
      }
      const issued = s.issued, replacements = s.replacements; await s.redraw(); s.intersections(); s.resize(); await s.flush();
      assert.equal(s.issued, issued); assert.equal(s.replacements, replacements, 'retired thread subscriptions do not redraw after failure');
      releaseThrowsFirst(); retiredFixture(s, listeners); assert.equal(s.urls.has(badURL), true);
      s.inject = s.onRevoke = null; off(); retiredFixture(s, listeners); off(); await s.flush();
      assert.equal(s.urls.size, 0); assert.equal(s.sources.size, 0); retiredFixture(s, listeners);
      if (thumbs) quotaFields(s, { sources: 0, residentEntries: 0, residentBytes: 0, running: 1, reservedBytes: 4 });
      held.body.resolve(null); await hostTurn(); assert.equal(s.issued, issued);
      if (thumbs) assert.equal(s.budget.stats.running, 1);
      await s.finish(held, null); if (thumbs) for (const value of Object.values(s.budget.stats)) assert.equal(value, 0);
      assert.equal(s.made, s.revoked); s.check();
    });
  }
});

root('release_unpublished_late_failure', async t => {
  for (const [failure, control] of [[false, false], [undefined, false], [false, true]]) await t.test(control ? 'listener failure still recovers original URL through fresh default UI' : `discarded real thread; late revoke ${String(failure)}`, async sub => {
    const s = installFixture(sub, { messages: [msg(1, [att(1), att(2)])] }), listeners = liveListeners(s);
    let off = null, oldURL = null, mounted = false, retiredAt = 0;
    let controlError = null, controlOffset = -1, recoveryMounts = 0, recovered = false;
    const dead = true, inert = () => { if (dead) return; };
    const attempt = async work => { try { await work(); } catch (error) { s.recordError(error); } };
    const mountCurrent = async () => { try { await s.mount(); } finally { mounted = s.offs.length > 0; } };
    const retireCurrent = () => {
      const active = s.offs.at(-1); active(); mounted = false; off = null;
      s.tap(() => retiredFixture(s, listeners)); // Keep the failure while completing handle forgetting before DOM replacement.
      s.forgetMount(active); s.root.replaceChildren();
    };
    const capturedRevoke = URL.revokeObjectURL; let providerReads = 0;
    // This finally is installed before allocation; it drains through current UI, never an old off/cache.
    try {
      await mountCurrent(); off = s.offs.at(-1); assert.equal(s.legacy, 0, 'real thread uses bounded thumbnail transport');
      assert.equal(s.issued, 2, 'canonical default C2 is occupied');
      s.onCreate = () => {
        s.onCreate = null; retireCurrent();
        retiredAt = s.replacements; s.tap(() => retiredFixture(s, listeners));
      };
      s.onRevoke = url => { if (oldURL === null) oldURL = url; throw failure; };
      await s.finish(s.findCall(sha(1)), raster(0));
      assert.equal(off === null, true); assert.equal(s.offs.length, 0); assert.equal(s.resources.length, 0);
      assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 1); assert.equal(s.urls.has(oldURL), true);
      assert.equal(s.replacements, retiredAt, 'retired allocation cannot publish into old thread');
      URL.revokeObjectURL = () => { providerReads++; throw new Error('live provider must not replace captured standalone revoke'); };
      const other = s.findCall(sha(2)); assert.ok(other?.signal.aborted); const attempts = s.attempts; assert.equal(attempts, 1);
      s.messages = [msg(1, [att(1)])]; await mountCurrent();
      let dialog = openThreadViewer(s); await s.flush();
      assert.match(dialog.textContent, /Waiting for earlier image requests|unavailable|cannot/i); assert.equal(dialog.querySelectorAll('img[src]').length, 0);
      viewerControl(s, dialog, /^Retry( image)?$/i); viewerControl(s, dialog, /^(Cancel|Close)( image)?$/i);
      assert.equal(s.issued, 2); assert.equal(s.attempts, attempts);
      s.intersections(); s.resize(); s.visibleChange('hidden'); s.visibleChange('visible'); await s.flush();
      assert.equal(s.attempts, attempts, 'new mount/Open/observer/visibility do not retry old URL');
      viewerControl(s, dialog, /^(Cancel|Close)( image)?$/i).click(); await s.flush();
      if (control) { controlOffset = s.errors.length; s.win.addEventListener('resize', inert); }
      retireCurrent();
      if (control) { controlError = s.errors[controlOffset]; assert.equal(mounted, false); assert.equal(s.offs.length, 0); return; }
      await mountCurrent(); dialog = openThreadViewer(s); await s.flush();
      assert.equal(s.issued, 2); assert.equal(s.attempts, attempts, 'another remount still owns no implicit recovery');
      viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush();
      assert.equal(s.attempts, attempts + 1); assert.equal(s.urls.has(oldURL), true); assert.equal(s.issued, 2);
      s.onRevoke = url => s.tap(() => { assert.equal(url, oldURL); assert.equal(s.issued, 2, 'no credit before successful native-style return'); });
      viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush(); s.onRevoke = null;
      assert.equal(s.attempts, attempts + 2); assert.equal(s.urls.has(oldURL), false); assert.equal(s.issued, 3);
      const next = s.findCall(sha(1)); assert.ok(next && next.kind === 'thumbnail'); assert.match(dialog.textContent, /Loading image/);
      await s.finish(next); const url = dialog.querySelector('img')?.src; assert.ok(url && url !== oldURL);
      await s.finish(other, raster()); assert.equal(dialog.querySelector('img')?.src, url);
      assert.equal(s.made, 2, 'retired transport completion creates no extra URL'); assert.equal(providerReads, 0);
    } catch (error) { s.recordError(error);
    } finally {
      URL.revokeObjectURL = capturedRevoke;
      s.onCreate = s.onRevoke = s.inject = null; s.closing = true;
      if (control) s.win.removeEventListener('resize', inert);
      // If an assertion failed with the default pool still charged, recovery must use a NEW current viewer.
      if (oldURL && s.urls.has(oldURL) && !mounted) await attempt(async () => { s.messages = [msg(1, [att(1)])]; await mountCurrent(); recoveryMounts++; });
      await attempt(async () => {
        if (oldURL && s.urls.has(oldURL)) {
          const issued = s.issued, attempts = s.attempts;
          if (control) s.onRevoke = url => s.tap(() => {
            assert.equal(issued, 2, 'fresh mount cannot bypass the two original default reservations');
            assert.equal(url, oldURL); assert.equal(s.urls.has(oldURL), true); assert.equal(s.issued, issued, 'no admission credit before actual revoke return');
            assert.equal(s.errors.includes(controlError), true, 'listener failure remains recorded during recovery');
          });
          const dialog = s.doc.querySelector('dialog[open]') || openThreadViewer(s);
          viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush();
          assert.equal(s.urls.has(oldURL), false, 'independent finally drains original default obligation via explicit UI'); recovered = true;
          if (control) { assert.equal(s.attempts, attempts + 1); assert.equal(s.issued, issued + 1, 'real disposal admits fresh default work'); }
        }
      });
      s.onRevoke = null;
      await attempt(async () => { if (mounted) { retireCurrent(); await s.flush(); retiredFixture(s, listeners); } });
      await attempt(async () => {
        for (let round = 0; round < 8 && s.activeCalls.size; round++) {
          const calls = s.calls(); for (const call of calls) { call.body.resolve(null); call.cleanup.resolve(); }
          await Promise.allSettled(calls.map(call => call.terminal)); await s.flush();
        }
        assert.equal(s.activeCalls.size, 0); assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0);
      });
      if (control) await attempt(async () => {
        assert.equal(recoveryMounts, 1); assert.equal(recovered, true); assert.equal(controlError instanceof assert.AssertionError, true);
        assert.deepEqual(controlError.actual, { ...listeners, window: listeners.window + 1 }); assert.deepEqual(controlError.expected, listeners);
        const index = s.errors.indexOf(controlError); assert.ok(index >= 0); s.errors.splice(index, 1); // Consume ONLY the validated control-owned failure after real recovery/draining.
      });
      s.check();
    }
  });
  await t.test('discarded injected cache retains original full-M ticket until actual Retry disposal', async sub => {
    const s = installFixture(sub, { limits: { B: 4, C: 1, E: 1, N: 2, Q: 1 } }), listeners = liveListeners(s); s.budget = createThumbBudget({ limits: s.limits });
    const url = await unpublishedFailure(s, 90, undefined); quotaFields(s, { running: 1, reservedBytes: 4, residentEntries: 0, sources: 0 });
    const thumbs = s.make(), rows = rowsOf([att(1)]); s.boxes.set(rows[0].key, rect(24, 10000, 240, 180));
    s.transaction(thumbs, rows); thumbs.open(rows[0].att, s.root.querySelector('[data-occurrence]'), true, rows[0].key); await s.flush();
    const dialog = liveViewer(s), attempts = s.attempts; s.intersections(); s.resize(); await s.flush(); assert.equal(s.attempts, attempts);
    s.onRevoke = () => { throw false; }; viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush();
    assert.equal(s.attempts, attempts + 1); assert.equal(s.urls.has(url), true); quotaFields(s, { running: 1, reservedBytes: 4, queued: 1 });
    s.onRevoke = null; viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush();
    assert.equal(s.urls.has(url), false); assert.equal(s.attempts, attempts + 2); assert.equal(s.issued, 2);
    quotaFields(s, { running: 1, reservedBytes: 4, queued: 0 }); await s.finish(s.findCall(sha(1))); s.check();
    thumbs.release(); await s.flush(); retiredFixture(s, listeners);
  });
});

root('document_default_and_cross_mount', async t => {
  for (const final of ['thread', 'wrapper']) for (const reverse of [false, true]) await t.test(`canonical default owner to ${final}, reverse=${reverse}`, async sub => {
    const s = installFixture(sub, { messages: [msg(1, [att(1), att(2)])] }), listeners = liveListeners(s);
    let off = await s.mount(); assert.equal(s.legacy, 0, 'default owner uses bounded transport'); assert.equal(s.issued, 2);
    const first = s.findCall(sha(1)), second = s.findCall(sha(2));
    off(); retiredFixture(s, listeners); s.forgetMount(off); off = null; s.root.replaceChildren(); retiredFixture(s, listeners);
    for (let cycle = 0; cycle < 16; cycle++) {
      off = await s.mount(); assert.equal(s.issued, 2, 'remount cannot replace abort-ignoring C2');
      off(); retiredFixture(s, listeners); s.forgetMount(off); off = null; s.root.replaceChildren(); retiredFixture(s, listeners);
      assert.equal(s.offs.length + s.resources.length, 0); assert.equal(s.sources.size, 0);
    }
    assert.ok(first.signal.aborted && second.signal.aborted); assert.equal(first.aborts, 1); assert.equal(second.aborts, 1);
    let dialog;
    if (final === 'thread') { s.messages = [msg(1, [att(1)])]; await s.mount(); dialog = openThreadViewer(s); }
    else {
      s.documentHeight = s.height; s.standaloneComposer = s.doc.createElement('div'); s.standaloneComposer.className = 'composer'; s.main.append(s.standaloneComposer);
      const thumbs = createThumbs({ api: s.api }), rows = rowsOf([att(1)]); s.wrappers.add(thumbs);
      s.transaction(thumbs, rows); thumbs.open(rows[0].att, s.root.querySelector('[data-occurrence]'), true, rows[0].key); dialog = liveViewer(s);
    }
    await s.flush();
    assert.equal(s.issued, 2); assert.match(dialog.textContent, /Waiting for earlier image requests/);
    const early = reverse ? second : first, late = reverse ? first : second;
    early.body.resolve(raster()); await hostTurn(); await s.flush(); assert.equal(s.issued, 2, 'body/abort without terminal cleanup gives no default capacity');
    await s.finish(early); assert.equal(s.issued, 3); const next = s.calls().find(call => call.hash === sha(1) && call !== first); assert.ok(next);
    await s.finish(next); const url = dialog.querySelector('img')?.src, counts = [s.issued, s.made, s.attempts]; assert.ok(url);
    await s.finish(late); assert.equal(liveViewer(s) === dialog, true); assert.equal(dialog.querySelector('img')?.src, url);
    assert.deepEqual([s.issued, s.made, s.attempts], counts); assert.equal(s.made, 1, 'no retired default URL is published'); s.check();
    for (const active of s.offs) active(); for (const active of s.wrappers) active.release(); await s.flush(); retiredFixture(s, listeners);
  });
  const axes = [{ B: 64, C: 2, E: 16 }, { B: 8, C: 8, E: 16 }, { B: 64, C: 8, E: 2 }];
  for (const [axis, limits] of axes.entries()) for (const reverse of [false, true]) await t.test(`shared injected ${axis}, reverse=${reverse}`, async sub => {
    const s = installFixture(sub, { limits }), listeners = liveListeners(s), rows = rowsOf([att(1)]);
    // Each wrapper is retired and forgotten before its successor; only two terminal transport controls survive.
    for (const number of [1, 2]) {
      let old = s.make(); s.transaction(old, rowsOf([att(number)])); await s.flush();
      old.release(); retiredFixture(s, listeners); s.forgetWrapper(old); old = null; s.root.replaceChildren(); retiredFixture(s, listeners);
    }
    const first = s.findCall(sha(1)), second = s.findCall(sha(2)); assert.equal(s.issued, 2);
    for (let cycle = 0; cycle < 16; cycle++) {
      let old = s.make(); s.transaction(old, rows); await s.flush(); assert.equal(s.issued, 2);
      old.release(); retiredFixture(s, listeners); s.forgetWrapper(old); old = null; s.root.replaceChildren(); retiredFixture(s, listeners);
      assert.equal(s.wrappers.size, 0); quotaFields(s, { running: 2, reservedBytes: 8, sources: 0, queued: 0 });
    }
    const thumbs = s.make(); s.transaction(thumbs, rows); thumbs.open(rows[0].att, s.root.querySelector('[data-occurrence]'), true, rows[0].key); await s.flush();
    const dialog = liveViewer(s); assert.equal(s.issued, 2); const early = reverse ? second : first, late = reverse ? first : second;
    assert.ok(first.signal.aborted && second.signal.aborted); assert.equal(first.aborts, 1); assert.equal(second.aborts, 1);
    early.body.resolve(raster()); await hostTurn(); await s.flush(); quotaFields(s, { running: 2, reservedBytes: 8 }); assert.equal(s.issued, 2);
    await s.finish(early); assert.equal(s.issued, 3); const next = s.calls().find(call => call.hash === sha(1) && call !== first); assert.ok(next);
    await s.finish(next); const url = dialog.querySelector('img')?.src, quota = { ...s.budget.stats }; assert.ok(url);
    const counts = [s.issued, s.made, s.attempts]; await s.finish(late);
    assert.equal(liveViewer(s) === dialog, true); assert.equal(dialog.querySelector('img')?.src, url); assert.deepEqual([s.issued, s.made, s.attempts], counts);
    assert.equal(s.budget.stats.running, quota.running - 1); assert.equal(s.budget.stats.reservedBytes, quota.reservedBytes - s.limits.M);
    assert.equal(s.budget.stats.sources, quota.sources); assert.equal(s.budget.stats.residentBytes, quota.residentBytes); s.check();
    thumbs.release(); await s.flush(); retiredFixture(s, listeners);
  });
});

root('retry_partial_progress_reentry', async t => {
  const failures = [undefined, null, false, 0, '', new Error('current retry failure')];
  for (const [index, failure] of failures.entries()) for (const mode of ['loading', 'ready', 'queued', 'cancel', 'replace']) {
    await t.test(`current failure ${index}, preserve ${mode}`, async sub => {
      const s = installFixture(sub, { limits: { B: 12, C: 2, E: 3, N: 4, Q: 4 } }), listeners = liveListeners(s), thumbs = s.make();
      const rows = rowsOf([att(7), att(9)]); for (const row of rows) s.boxes.set(row.key, rect(24, 10000, 240, 180));
      if (mode === 'ready') s.boxes.set(rows[1].key, rect(24, 100, 240, 180));
      s.transaction(thumbs, rows); await s.flush();
      if (mode === 'ready') await s.finish(s.findCall(sha(9)));
      const oldA = await unpublishedFailure(s, 91), oldB = await unpublishedFailure(s, 92);
      quotaFields(s, { running: 2, reservedBytes: 8 }); assert.equal(s.wrappers.size, 1);
      let earlier = null;
      if (mode === 'queued') {
        earlier = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} }); s.wrappers.add(earlier);
        earlier.acquire('accepted', att(8), true, { priority: 'explicit' });
      }
      thumbs.open(rows[0].att, s.root.querySelector('[data-occurrence]'), true, rows[0].key); await s.flush();
      const dialog = liveViewer(s), ready = mode === 'ready' ? s.root.querySelector(`button.thumb[data-sha="${sha(9)}"] img`)?.src : null;
      let successor = null, pumped = 0;
      let later = null;
      if (mode === 'queued') {
        later = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} }); s.wrappers.add(later);
        later.acquire('behind-current', att(10), true, { priority: 'explicit' });
      }
      assert.equal(dialog.querySelectorAll('img[src]').length, 0); if (mode === 'ready') assert.ok(ready);
      const issued = s.issued, attempts = s.attempts; let nested = false;
      s.onRevoke = url => {
        if (!nested) {
          nested = true;
          s.tap(() => { const before = s.attempts; retryThumbCleanup(s.budget); assert.equal(s.attempts, before, 'nested pass cannot extend outer snapshot'); });
          nested = false;
        }
        if (url === oldB) throw failure;
        s.tap(() => { assert.equal(url, oldA); assert.equal(s.issued, issued, 'successful URL disposal precedes capacity dispatch'); });
      };
      s.onStart = call => s.tap(() => {
        pumped++; assert.equal(s.urls.has(oldA), false); assert.equal(s.urls.has(oldB), true);
        if (mode === 'ready') {
          thumbs.open(rows[1].att, s.root.querySelectorAll('[data-occurrence]')[1], true, rows[1].key); successor = liveViewer(s);
        } else if (mode === 'cancel' || mode === 'replace') {
          viewerControl(s, dialog, /^(Cancel|Close)( image)?$/i).click();
          if (mode === 'replace') {
            thumbs.open(rows[1].att, s.root.querySelectorAll('[data-occurrence]')[1], true, rows[1].key); successor = liveViewer(s);
          }
        }
        assert.equal(call.hash, sha(mode === 'queued' ? 8 : 7));
      });
      s.intersections(); s.resize(); await s.flush(); assert.equal(s.attempts, attempts, 'observer work is not a cleanup pass');
      viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush(); s.onStart = null;
      assert.equal(s.attempts, attempts + 2, 'one bounded snapshot attempts each old URL once'); assert.equal(pumped, 1);
      assert.equal(s.urls.has(oldA), false); assert.equal(s.urls.has(oldB), true); assert.equal(s.issued, issued + 1);
      quotaFields(s, { running: 2, reservedBytes: 8 });
      if (mode === 'cancel') { assert.equal(s.doc.querySelectorAll('dialog[open]').length, 0); assert.equal(thumbs.stats.owners, 0); assert.equal(s.findCall(sha(7)).signal.aborted, true); }
      else if (mode === 'replace') { assert.equal(liveViewer(s) === successor, true); assert.match(successor.textContent, /Waiting for earlier image requests/); }
      else if (mode === 'ready') {
        assert.equal(liveViewer(s) === successor, true); assert.equal(successor.querySelector('img')?.src, ready);
        assert.equal(s.findCall(sha(7)).signal.aborted, true, 'pumped request retired when a ready successor opens');
      }
      else {
        assert.equal(liveViewer(s) === dialog, true);
        assert.match(dialog.textContent, mode === 'queued' ? /Waiting for earlier image requests/ : /Loading image/);
        if (mode === 'queued') {
          viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush();
          assert.equal(s.attempts, attempts + 3, 'only the still-failed original resource is attempted'); assert.equal(s.issued, issued + 1);
          assert.match(dialog.textContent, /Waiting for earlier image requests/);
        }
      }
      s.onRevoke = null;
      if (mode === 'cancel' || mode === 'ready' || mode === 'loading') retryThumbCleanup(s.budget); // Independent final drain; loading/ready UI need not offer Retry.
      else viewerControl(s, mode === 'replace' ? successor : dialog, /^Retry( image)?$/i).click();
      await s.flush(); assert.equal(s.urls.has(oldB), false);
      if (mode === 'cancel') { await s.finish(s.findCall(sha(7)), null); assert.equal(s.issued, issued + 1); assert.equal(s.doc.querySelectorAll('dialog[open]').length, 0); }
      else if (mode === 'ready') {
        const counts = [s.issued, s.made, s.attempts]; await s.finish(s.findCall(sha(7)), raster());
        assert.equal(liveViewer(s) === successor, true); assert.equal(successor.querySelector('img')?.src, ready); assert.deepEqual([s.issued, s.made, s.attempts], counts);
      }
      else if (mode === 'replace') {
        const next = s.findCall(sha(9)); assert.ok(next); await s.finish(next); const url = successor.querySelector('img')?.src; assert.ok(url);
        await s.finish(s.findCall(sha(7)), raster()); assert.equal(liveViewer(s) === successor, true); assert.equal(successor.querySelector('img')?.src, url);
      } else {
        assert.equal(s.starts.has(sha(10)), false, 'Retry preserves original accepted FIFO position ahead of later explicit work');
        await s.finish(s.findCall(sha(7))); assert.ok(dialog.querySelector('img[src]'));
        if (later) await s.finish(s.findCall(sha(10)), null);
        if (earlier) await s.finish(s.findCall(sha(8)), null);
      }
      s.check();
      thumbs.release(); await s.flush(); retiredFixture(s, listeners);
    });
  }
});

// R-M62: independent regressions for production review I1–I4. No product shim.
root('viewer_reclaims_residents_with_transport', async t => {
  const s = installFixture(t, { limits: { B: 12, M: 4, C: 2, E: 8, N: 8, Q: 8 } });
  const listeners = liveListeners(s), thumbs = s.make(), rows = rowsOf([1, 2, 3, 4].map(n => att(n, { size: 4 })));
  s.boxes.set(rows[3].key, rect(24, 10000, 240, 180));
  s.transaction(thumbs, rows); await s.flush();
  assert.deepEqual([...s.starts.keys()], [sha(1), sha(2)]);
  await s.finish(s.findCall(sha(1)), raster(4));
  const held = s.findCall(sha(3)); assert.ok(held);
  await s.finish(s.findCall(sha(2)), raster(4));
  quotaFields(s, { running: 1, reservedBytes: 4, residentEntries: 2, residentBytes: 8, queued: 0, sources: 3 });
  assert.equal(s.budget.stats.running < s.limits.C, true, 'a concurrency slot is free before Open');
  const residentURLs = [...s.urls.keys()]; assert.equal(residentURLs.length, 2);
  assert.equal(s.sources.size, 2); assert.equal(held.bodyDone, false); assert.equal(held.aborts, 0);
  const opener = s.root.querySelectorAll('button.thumb')[3]; assert.ok(opener);
  thumbs.open(rows[3].att, opener, true, rows[3].key); const dialog = liveViewer(s); await s.flush();
  const explicit = s.findCall(sha(4));
  assert.ok(explicit, 'explicit fourth image dispatches by reclaiming ready bytes while the third transport is held');
  assert.deepEqual([...s.starts.keys()], [sha(1), sha(2), sha(3), sha(4)]);
  assert.equal(s.activeCalls.has(held.id), true); assert.equal(held.bodyDone, false); assert.equal(held.aborts, 0);
  quotaFields(s, { running: 2, reservedBytes: 8, residentEntries: 1, residentBytes: 4, queued: 0, sources: 3 });
  assert.equal(residentURLs.filter(url => s.urls.has(url)).length, 1, 'exactly one old resident is reclaimed');
  assert.equal(s.sources.size, 1); assert.equal(s.made, 2); assert.equal(s.revoked, 1);
  assert.match(dialog.textContent, /Loading image/);
  const later = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} });
  s.wrappers.add(later);
  later.acquire('later-first', att(5, { size: 4 }), true, { priority: 'explicit' });
  later.acquire('later-second', att(6, { size: 4 }), true, { priority: 'explicit' });
  assert.equal(s.budget.stats.queued, 2); assert.equal(s.starts.has(sha(5)), false); assert.equal(s.starts.has(sha(6)), false);
  await s.finish(explicit, raster(4)); assert.ok(dialog.querySelector('img[src]'));
  assert.equal(s.starts.has(sha(5)), false, 'later explicit work cannot bypass resident and held charges');
  viewerControl(s, dialog, /^(Close|Cancel)( image)?$/i).click(); await s.flush();
  assert.ok(s.findCall(sha(5)), 'later accepted explicit work dispatches after current viewer retirement');
  assert.deepEqual([...s.starts.keys()], [sha(1), sha(2), sha(3), sha(4), sha(5)]);
  assert.equal(held.bodyDone, false); assert.equal(held.aborts, 0); assert.equal(s.starts.has(sha(6)), false);
  quotaFields(s, { running: 2, reservedBytes: 8, residentEntries: 1, residentBytes: 4, queued: 1 });
  await s.finish(s.findCall(sha(5)), raster(4)); assert.equal(s.starts.has(sha(6)), false);
  later.drop('later-first'); assert.ok(s.findCall(sha(6)), 'accepted explicit FIFO survives reclamation and payload retirement');
  assert.deepEqual([...s.starts.keys()], [sha(1), sha(2), sha(3), sha(4), sha(5), sha(6)]);
  assert.equal(held.bodyDone, false); assert.equal(held.aborts, 0);
  await s.finish(s.findCall(sha(6)), raster(4)); later.release();
  assert.equal(s.activeCalls.has(held.id), true); assert.equal(held.bodyDone, false);
  await s.finish(held, null); thumbs.release(); await s.flush();
  retiredFixture(s, listeners); assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0);
  for (const value of Object.values(s.budget.stats)) assert.equal(value, 0); s.check();
});

root('initial_transfer_cancel_reopen_same', async t => {
  const s = installFixture(t, { limits: { N: 1 } }), listeners = liveListeners(s), thumbs = s.make(), rows = rowsOf([att(1)]);
  s.transaction(thumbs, rows); await s.flush(); await finishVisible(s);
  const opener = s.root.querySelector('button.thumb'); assert.ok(opener);
  const inline = opener.querySelector('img');
  assert.ok(inline); const url = inline.src, counts = [s.issued, s.made, s.attempts];
  let first = null, successor = null, fired = false;
  s.inject = (operation, node, key) => {
    if (fired || operation !== 'removeAttribute' || key !== 'src' || node !== inline) return;
    fired = true; s.inject = null; first = liveViewer(s);
    viewerControl(s, first, /^(Cancel|Close)( image)?$/i).click();
    thumbs.open(rows[0].att, opener, true, rows[0].key); successor = liveViewer(s);
  };
  try { opener.click(); await s.flush(); } finally { s.inject = null; }
  assert.equal(fired, true, 'reentry occurs during the INITIAL inline-to-viewer physical detach');
  assert.ok(successor); assert.equal(successor === first, false); assert.equal(first.isConnected, false);
  assert.equal(liveViewer(s) === successor, true);
  assert.equal(successor.querySelector('img')?.src, url, 'retired V1 cannot steal V2 ownership after the outer detach returns');
  viewerCounts(s, thumbs, 1, 1); assert.equal(s.budget.stats.queued, 0);
  assert.equal(successor.contains(s.doc.activeElement) || s.doc.activeElement === successor, true);
  assert.deepEqual([s.issued, s.made, s.attempts], counts);
  viewerControl(s, successor, /^Retry( image)?$/i).click(); await s.flush();
  assert.equal(liveViewer(s) === successor, true); assert.equal(successor.querySelector('img')?.src, url);
  assert.deepEqual([s.issued, s.made, s.attempts], counts, 'current ready successor makes progress without refetch');
  viewerControl(s, successor, /^(Cancel|Close)( image)?$/i).click(); await s.flush();
  viewerCounts(s, thumbs, 1, 1); assert.equal(readyURL(s), url);
  assert.equal(s.doc.activeElement === opener, true);
  s.transaction(thumbs, []); await s.flush();
  viewerCounts(s, thumbs, 0, 0); assert.equal(s.urls.size, 0); assert.equal(s.budget.stats.queued, 0);
  assert.equal(s.made, s.revoked, 'no orphan V1 owner requires whole-wrapper release to reclaim its URL');
  thumbs.release(); await s.flush(); retiredFixture(s, listeners); s.check();
});

root('src_assignment_release_reentry', async t => {
  const s = installFixture(t, { limits: { B: 4, M: 4, C: 1, E: 1, N: 1, Q: 1 } });
  const listeners = liveListeners(s), thumbs = s.make(), rows = rowsOf([att(1)]);
  s.transaction(thumbs, rows); await s.flush();
  const call = s.findCall(sha(1)); assert.ok(call);
  let pendingImage = null, releaseReturned = false;
  s.inject = (operation, node, key) => {
    if (operation !== 'attribute' || key !== 'src' || node.localName !== 'img' || pendingImage) return;
    pendingImage = node; s.inject = null;
    assert.equal(node.hasAttribute('src'), false, 'hook precedes the actual physical src write');
    thumbs.release(); releaseReturned = true;
  };
  try { await s.finish(call); } finally { s.inject = null; }
  assert.ok(pendingImage); assert.equal(releaseReturned, true);
  assert.equal(pendingImage.hasAttribute('src'), false, 'pending assignment return scrubs the actual retired image');
  assert.equal(pendingImage.handlers('error').length, 0);
  assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0); assert.equal(thumbs.stats.owners, 0);
  assert.ok(s.sources.size <= s.budget.stats.sources, 'physical ownership holds after the pending assignment returned');
  retiredFixture(s, listeners); s.check();
  thumbs.release(); thumbs.release(); await s.flush();
  assert.equal(pendingImage.hasAttribute('src'), false); assert.equal(s.sources.has(pendingImage), false);
  assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0);
  for (const value of Object.values(s.budget.stats)) assert.equal(value, 0);
  retiredFixture(s, listeners); assert.equal(s.made, s.revoked); s.check();
});

root('unregister_failure_retry', async t => {
  for (const at of ['listener', 'viewport', 'io', 'ro']) for (const failure of [false, undefined]) {
    await t.test(`${at} one-shot ${String(failure)}`, async sub => {
      const s = installFixture(sub), listeners = liveListeners(s), thumbs = s.make(), rows = rowsOf([att(1), att(2)]);
      s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
      const held = s.findCall(sha(2)); assert.ok(held); assert.equal(s.urls.size, 1);
      const observer = at === 'io' ? [...s.observers][0] : at === 'ro' ? [...s.resizeObservers][0] : null;
      if (at === 'io' || at === 'ro') {
        assert.ok(observer); assert.equal(observer.kind, at === 'io' ? 'intersection' : 'resize'); assert.ok(observer.targets.size > 0);
      }
      const target = at === 'listener' ? s.win : s.main, type = at === 'listener' ? 'resize' : 'scroll';
      const off = () => thumbs.release(); let fired = false;
      s.inject = (operation, node, detail, fn) => {
        const match = observer ? operation === 'disconnect' && node === observer :
          operation === 'removeEventListener' && node === target && detail === type && target.handlers(type).includes(fn);
        if (fired || !match) return;
        fired = true; throw failure; // Pre-mutation, once only; all other unregisters remain real.
      };
      try {
        exactThrow(off, failure); assert.equal(fired, true);
        assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0); assert.equal(thumbs.stats.owners, 0);
        assert.equal(held.signal.aborted, true); assert.equal(held.aborts, 1); assert.equal(held.bodyDone, false);
        quotaFields(s, { running: 1, reservedBytes: 4, residentEntries: 0, residentBytes: 0, sources: 0, queued: 0 });
        assert.equal(s.raf.size + s.timers.size, 0, 'independent schedulers retire on the first failing teardown');
        assert.equal(s.doc.querySelectorAll('dialog[open]').length, 0);
        const currentListeners = liveListeners(s), allowed = at === 'listener' ? 'window' : at === 'viewport' ? 'scroller' : '';
        for (const [key, baseline] of Object.entries(listeners)) {
          assert.ok(currentListeners[key] >= baseline && currentListeners[key] <= baseline + (key === allowed ? 1 : 0), 'only the failed registration may remain');
        }
        assert.ok(s.observers.size <= (at === 'io' ? 1 : 0));
        assert.ok(s.resizeObservers.size <= (at === 'ro' ? 1 : 0));
        s.inject = null;
        off(); retiredFixture(s, listeners); // A normal return cannot forget the failed target.
        off(); await s.flush(); retiredFixture(s, listeners);
        const issued = s.issued; s.intersections(); s.resize(); scrollEvent(s); s.visibleChange('visible'); await s.flush();
        assert.equal(s.issued, issued); retiredFixture(s, listeners);
        held.body.resolve(null); await hostTurn();
        assert.equal(s.budget.stats.running, 1, 'body delivery still cannot release the held reservation');
        await s.finish(held, null);
        for (const value of Object.values(s.budget.stats)) assert.equal(value, 0);
        retiredFixture(s, listeners); s.check();
      } finally { s.inject = null; }
    });
  }
});

function amendmentCase(s) {
  s.onStart = () => s.tap(() => assert.ok(s.activeCalls.size <= 64, 'amendment active-request cap'));
}
function completedRelease(off, observer, expected) {
  let threw = false, error;
  try { off(observer); } catch (value) { threw = true; error = value; }
  assert.equal(threw, expected.failed, 'current direct throw is distinct from a pending normal return');
  if (threw) assert.equal(error, expected.error);
}

root('concurrency_capacity_pair', async t => {
  for (const C of [2, 3]) await t.test(`ready successor and actual dispatch C${C}`, async sub => {
    const s = installFixture(sub, { limits: { B: 12, M: 4, C, E: 3, N: 4, Q: 4 } });
    amendmentCase(s); const listeners = liveListeners(s), thumbs = s.make(), rows = rowsOf([att(7), att(9)]);
    s.boxes.set(rows[0].key, rect(24, 10000)); s.transaction(thumbs, rows); await s.flush();
    await s.finish(s.findCall(sha(9))); const original = readyURL(s); assert.ok(original);
    const oldA = await unpublishedFailure(s, 91), oldB = await unpublishedFailure(s, 92);
    try {
      assert.equal(typeof s.budget.hasConcurrencySlot, 'function', 'API prerequisite: hasConcurrencySlot');
      assert.equal(s.budget.hasConcurrencySlot(), C === 3);
      thumbs.open(rows[0].att, s.root.querySelectorAll('[data-occurrence]')[0], true, rows[0].key); await s.flush();
      const dialog = liveViewer(s);
      assert.equal(s.urls.has(oldA) && s.urls.has(oldB), true, 'Open must not retry unpublished cleanup');
      if (C === 2) {
        quotaFields(s, { running: 2, reservedBytes: 8, residentEntries: 1, residentBytes: 1, sources: 2, queued: 1 });
        assert.equal(readyURL(s), original); assert.equal(s.findCall(sha(7)), undefined);
        let successor = null;
        s.onStart = call => s.tap(() => {
          assert.equal(call.hash, sha(7)); assert.ok(s.activeCalls.size <= 64);
          thumbs.open(rows[1].att, s.root.querySelectorAll('[data-occurrence]')[1], true, rows[1].key); successor = liveViewer(s);
        });
        s.onRevoke = url => { if (url === oldB) throw false; };
        viewerControl(s, dialog, /^Retry( image)?$/i).click(); await s.flush();
        assert.ok(successor); assert.equal(successor.querySelector('img')?.src, original);
        assert.equal(s.findCall(sha(7)).signal.aborted, true);
      } else {
        assert.ok(s.findCall(sha(7)), 'C3 uses real resident retirement to dispatch accepted explicit7');
        assert.equal(s.urls.has(original), false); assert.equal(s.attempts, 3);
        quotaFields(s, { running: 3, reservedBytes: 12, residentEntries: 0, residentBytes: 0, sources: 1, queued: 0 });
        assert.match(dialog.textContent, /Loading image/);
      }
      s.check();
    } finally {
      s.onRevoke = s.onStart = null; thumbs.release(); retryThumbCleanup(s.budget);
      for (const call of s.calls()) await s.finish(call, null);
    }
    retiredFixture(s, listeners); for (const value of Object.values(s.budget.stats)) assert.equal(value, 0);
  });
});

root('assignment_release_obligations', async t => {
  for (const kind of ['normal', 'pre', 'post', 'scrub', 'listener', 'revoke', 'independent', 'two-debts']) {
    for (const failure of kind === 'normal' ? [false] : [false, undefined]) await t.test(`${kind} ${String(failure)}`, async sub => {
      const s = installFixture(sub, { limits: { B: 4, M: 4, C: 1, E: 1, N: 1, Q: 1 } });
      amendmentCase(s); const listeners = liveListeners(s), thumbs = s.make(), rows = rowsOf([att(1)]);
      s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
      const url = readyURL(s), before = s.attempts; let actual = null, settled = 0, outcome = null, fired = false, faulted = false;
      const observer = value => { settled++; assert.equal(settled, 1); outcome = value; assert.equal(s.sources.size, 0); };
      const later = failure === false ? undefined : false;
      s.onRevoke = value => {
        assert.equal(value, url); assert.equal(s.sources.size, 0);
        assert.equal(settled, 0, 'completion cannot precede the outer model attempt');
        if ((kind === 'revoke' || kind === 'independent' || kind === 'two-debts') && s.attempts === before + 1) throw kind === 'independent' ? later : failure;
      };
      s.inject = (operation, node, key) => {
        if (operation === 'attribute' && key === 'src' && node.localName === 'img' && !fired) {
          fired = true; actual = node;
          const directFailure = kind === 'listener' || kind === 'independent';
          completedRelease(thumbs.release, observer, { failed: directFailure, error: failure });
          assert.equal(settled, 0, 'pending callback is an API/completion prerequisite');
          assert.equal(s.attempts, before, 'no model credit while the actual write is unresolved');
          quotaFields(s, { sources: 1, residentEntries: 1 }); assert.equal(s.urls.has(url), true);
          if (kind === 'two-debts') thumbs.release(observer);
          if (kind === 'pre') throw failure;
        }
        if (!fired) return;
        if (operation === 'attributeAfter' && node === actual && key === 'src') {
          assert.equal(node.src, url); assert.ok(s.sources.size <= s.budget.stats.sources);
          if (kind === 'post') throw failure;
        }
        const listener = kind === 'listener' && operation === 'removeEventListener' && node === actual && key === 'error';
        const independent = kind === 'independent' && operation === 'removeEventListener' && node === s.win && key === 'resize';
        const scrub = kind === 'scrub' && operation === 'removeAttribute' && node === actual && key === 'src' && node.hasAttribute('src');
        if (!faulted && (listener || independent || scrub)) { faulted = true; throw failure; }
      };
      try {
        const throws = ['pre', 'post', 'scrub', 'revoke', 'independent', 'two-debts'].includes(kind);
        completedRelease(() => s.transaction(thumbs, rows), undefined, { failed: throws, error: kind === 'independent' ? later : failure });
        assert.ok(actual); assert.equal(actual.hasAttribute('src'), false); assert.equal(actual.handlers('error').length, 0);
        assert.equal(settled, 1, 'API prerequisite: release(onSettled) must report completed retirement');
        assert.equal(outcome.failed, kind !== 'normal'); if (outcome.failed) assert.equal(outcome.error, failure);
        assert.equal(s.attempts - before, kind === 'two-debts' ? 2 : 1, 'each owed release is a real model attempt');
        const retained = kind === 'revoke' || kind === 'independent';
        assert.equal(s.urls.has(url), retained);
        quotaFields(s, { sources: retained ? 1 : 0, residentEntries: retained ? 1 : 0 });
        s.inject = s.onRevoke = null;
        let retryDone = 0; thumbs.release(result => { assert.equal(result.failed, false); retryDone++; });
        assert.equal(retryDone, 1); assert.equal(s.urls.size, 0); assert.equal(s.sources.size, 0);
        retiredFixture(s, listeners); s.check(); outcome = null;
      } finally { s.inject = s.onRevoke = null; thumbs.release(); }
    });
  }
  await t.test('N2 nested inline/viewer writes and two real obligations', async sub => {
    const s = installFixture(sub, { limits: { N: 2 } }), thumbs = s.make(), rows = rowsOf([att(1)]);
    amendmentCase(s); s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
    const url = readyURL(s), before = s.attempts; let depth = 0, writes = 0, returned = 0, notifications = 0;
    const observer = result => { notifications++; assert.equal(result.failed, false); assert.equal(depth, 0); };
    s.inject = (operation, node, key) => {
      if (key !== 'src' || node.localName !== 'img') return;
      if (operation === 'attribute') {
        depth++; writes++;
        if (depth === 1) {
          const button = s.root.querySelector('button.thumb'); thumbs.open(rows[0].att, button, true, rows[0].key);
          assert.equal(returned, 1); assert.equal(s.attempts, before); quotaFields(s, { sources: 2, residentEntries: 1 });
        } else {
          assert.equal(depth, 2); thumbs.release(observer); thumbs.release(observer);
          assert.equal(notifications, 0); assert.equal(s.urls.has(url), true);
        }
      } else if (operation === 'attributeAfter') {
        assert.equal(node.src, url); assert.ok(s.sources.size <= s.budget.stats.sources);
        depth--; returned++;
      }
    };
    try {
      s.transaction(thumbs, rows);
      assert.equal(writes, 2); assert.equal(returned, 2); assert.equal(notifications, 1, 'outermost unwind completes synchronously');
      assert.equal(s.attempts, before + 1, 'second real model call after successful disposal has no URL to revoke twice');
      assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0); s.check();
    } finally { s.inject = null; thumbs.release(); }
  });
  await t.test('model guard cannot settle before the outer model attempt returns', async sub => {
    const s = installFixture(sub), thumbs = s.make(), rows = rowsOf([att(1)]);
    amendmentCase(s); s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
    let inside = false, calls = 0, guardSeen = false, settled = 0;
    const observer = result => { assert.equal(inside, false); settled++; assert.equal(result.failed, true); assert.ok(guardSeen); };
    s.onRevoke = () => {
      inside = true; calls++;
      assert.throws(() => thumbs.release(observer), error => { guardSeen = true; return /reenter cache mutation/.test(error.message); });
      assert.equal(settled, 0, 'modelDepth prevents inner normal/guard completion');
      inside = false;
    };
    try { thumbs.release(observer); assert.equal(calls, 1); assert.equal(settled, 1); s.check(); }
    finally { s.onRevoke = null; thumbs.release(); }
  });
  await t.test('ordinary writing retirement refuses early drop and retains retry', async sub => {
    const s = installFixture(sub, { limits: { N: 1 } }), thumbs = s.make(), rows = rowsOf([att(1)]);
    amendmentCase(s); s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
    const url = readyURL(s); let reached = false;
    s.inject = (operation, node, key) => {
      if (reached || operation !== 'attribute' || key !== 'src' || node.localName !== 'img') return;
      reached = true; s.inject = null;
      assert.throws(() => s.visibleChange('hidden'), 'ordinary drop must report inability to retire an unresolved write');
      assert.equal(s.urls.has(url), true); quotaFields(s, { sources: 1, residentEntries: 1 });
    };
    try { s.transaction(thumbs, rows); assert.equal(reached, true); assert.equal(s.sources.size, 0); }
    finally { s.inject = null; thumbs.release(); }
    assert.equal(s.urls.size, 0); s.check();
  });
});

root('caller_retains_deferred_published', async t => {
  for (const mode of ['failure', 'success', 'independent', 'aside']) for (const failure of mode === 'success' ? [false] : [false, undefined]) {
    await t.test(`${mode} ${String(failure)}; only the direct owner can retry`, async sub => {
      const s = installFixture(sub), listeners = liveListeners(s); amendmentCase(s);
      const owner = { off: await s.mount(), aside: await s.asideMount(), invoking: false, requested: false, callback: null, commits: 0, notifications: 0 };
      let first = false, firstError, pending = null, url = null, directThrows = 0, revokeCalls = 0, asideCalls = 0;
      const finish = result => {
        owner.notifications++; assert.ok(owner.notifications <= 2, 'one observer notification per completed wave');
        if (result.failed) { assert.equal(result.error, failure); return; }
        owner.off = null; owner.callback = null;
        if (!owner.invoking && owner.requested && !owner.aside) owner.commits++;
      };
      owner.callback = finish;
      const requestFrame = requestAnimationFrame;
      globalThis.requestAnimationFrame = fn => requestFrame(time => {
        try { fn(time); }
        finally {
          if (pending && mode === 'success') {
            assert.equal(owner.commits, 1, 'real thread continuation completes before the initiating frame returns');
            assert.equal(owner.off, null); assert.equal(owner.callback, null);
          }
        }
      });
      // A small success-only owner for the real returned off, not an app implementation substitute.
      const request = () => {
        owner.requested = true; owner.invoking = true;
        try {
          if (owner.off) { try { owner.off(owner.callback); } catch (error) { directThrows++; if (!first) { first = true; firstError = error; } } }
          if (owner.aside) { try { asideCalls++; owner.aside(); owner.aside = null; } catch (error) { if (!first) { first = true; firstError = error; } } }
        } finally { owner.invoking = false; }
        if (!owner.off && !owner.aside) owner.commits++;
      };
      s.onRevoke = value => {
        url = value; revokeCalls++; assert.equal(s.sources.size, 0); assert.equal(owner.commits, 0);
        assert.equal(typeof owner.off, 'function', 'the actual owner remains through native disposal');
        if (['failure', 'independent'].includes(mode) && revokeCalls === 1) throw failure;
      };
      s.inject = (operation, node, key) => {
        if (!pending && operation === 'attribute' && key === 'src' && node.localName === 'img') {
          pending = node; request();
          assert.equal(owner.notifications, 0); assert.equal(owner.commits, 0);
          assert.equal(typeof owner.off, 'function', 'normal pending off does not authorize forgetting');
          s.forgetMount(owner.off); s.offs.length = 0; s.wrappers.clear(); // Remove direct finalizer rescue registrations, not actual resources.
          assert.equal(s.offs.length, 0); assert.equal(s.wrappers.size, 0);
          if (mode === 'success') request(); // Same stable observer, another real release obligation.
        }
        if (mode === 'independent' && pending && operation === 'removeEventListener' && node === s.doc && key === 'close' && !first) throw failure;
        if (mode === 'aside' && pending && operation === 'attribute' && node === s.aside && key === 'class' && !first) throw failure;
      };
      try {
        await s.finish(s.findCall(sha(1)));
        assert.ok(pending); assert.equal(asideCalls, 1, 'actual aside cleanup runs while thread retirement is pending');
        assert.equal(owner.notifications, 1, 'API prerequisite: actual thread off forwards completion');
        assert.equal(pending.hasAttribute('src'), false); assert.equal(revokeCalls, 1);
        if (mode === 'aside') {
          assert.equal(first, true); assert.equal(firstError, failure); assert.equal(owner.commits, 0);
          assert.equal(owner.off, null); assert.equal(owner.callback, null); assert.equal(typeof owner.aside, 'function');
          assert.equal(s.urls.size, 0, 'only the actual unfinished aside handle remains');
          s.inject = null; request(); assert.equal(asideCalls, 2); assert.equal(owner.commits, 1);
          assert.equal(s.attempts, 1, 'successfully retired thread is not repeated for an aside failure');
        } else if (mode === 'success') {
          assert.equal(owner.commits, 1, 'continuation runs in assignment unwind without another request');
          assert.equal(owner.off, null); assert.equal(owner.callback, null); assert.equal(s.urls.size, 0);
        } else {
          assert.equal(owner.commits, 0); assert.equal(typeof owner.off, 'function');
          assert.equal(s.urls.has(url), true, 'failed published URL remains under the surviving real off');
          if (mode === 'independent') { assert.equal(directThrows, 1); assert.equal(firstError, failure); }
          assert.equal(s.errors.length, 1); assert.equal(s.errors[0], failure);
          s.errors.splice(0, 1); // Consume only the exact direct assignment failure just asserted.
          s.inject = s.onRevoke = null; request();
          assert.equal(owner.commits, 1); assert.equal(owner.off, null); assert.equal(owner.callback, null);
          assert.equal(s.urls.has(url), false); assert.equal(s.attempts, 2);
        }
        retiredFixture(s, listeners); s.check(); assert.equal(s.offs.length, 0);
      } finally {
        s.inject = s.onRevoke = null;
        globalThis.requestAnimationFrame = requestFrame;
        if (owner.off || owner.aside) request(); // Only the surviving owner; no captured old wrapper/off rescue.
        assert.equal(owner.off, null); assert.equal(owner.aside, null);
      }
    });
  }
});

root('concurrency_capacity_reentry', async t => {
  const s = installFixture(t, { limits: { B: 12, M: 4, C: 2, E: 8, N: 8, Q: 8 } });
  amendmentCase(s); const thumbs = s.make(), rows = rowsOf([9, 3, 7].map(n => att(n, { size: 4 })));
  s.boxes.set(rows[2].key, rect(24, 10000)); s.transaction(thumbs, rows); await s.flush();
  await s.finish(s.findCall(sha(9)), raster(4)); const original = readyURL(s), held = s.findCall(sha(3));
  const other = createThumbCache({ api: s.api, URL, budget: s.budget, beforeRevoke() {}, onChange() {} }); s.wrappers.add(other);
  other.acquire('resident8', att(8, { size: 4 }), true, { priority: 'automatic' }); await s.finish(s.findCall(sha(8)), raster(4));
  let reached = false;
  s.inject = (operation, node, key) => {
    if (reached || operation !== 'removeAttribute' || key !== 'src' || node.src !== original) return;
    reached = true; other.drop('resident8'); // Real released capacity dispatches already accepted7 during detach.
    assert.ok(s.findCall(sha(7))); assert.equal(s.budget.stats.running, 2);
  };
  try {
    assert.equal(typeof s.budget.hasConcurrencySlot, 'function', 'API prerequisite: hasConcurrencySlot');
    const opener = s.root.querySelectorAll('button.thumb')[2]; thumbs.open(rows[2].att, opener, true, rows[2].key); await s.flush();
    assert.equal(reached, true); assert.equal(s.urls.has(original), true, 'fresh full-C/progress check preserves the original local payload');
    assert.equal(readyURL(s), original, 'still-demanded ready source is repatched without suppression');
    assert.equal(held.aborts, 0); assert.equal(held.bodyDone, false);
    quotaFields(s, { running: 2, reservedBytes: 8, residentEntries: 1, residentBytes: 4, sources: 3, queued: 0 }); s.check();
  } finally { s.inject = null; thumbs.release(); other.release(); for (const call of s.calls()) await s.finish(call, null); }
});

root('assignment_release_obligations_retry_and_observers', async t => {
  for (const failure of [false, undefined]) await t.test(`failed actual scrub retains retry authority ${String(failure)}`, async sub => {
    const s = installFixture(sub, { limits: { N: 1 } }), thumbs = s.make(), rows = rowsOf([att(1)]);
    amendmentCase(s); s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
    const url = readyURL(s), before = s.attempts; let actual = null, notifications = 0;
    s.inject = (operation, node, key) => {
      if (!actual && operation === 'attribute' && key === 'src' && node.localName === 'img') {
        actual = node; thumbs.release(result => { notifications++; assert.equal(result.failed, true); assert.equal(result.error, failure); });
      }
      if (operation === 'removeAttribute' && node === actual && key === 'src' && node.hasAttribute('src')) throw failure;
    };
    try {
      exactThrow(() => s.transaction(thumbs, rows), failure); assert.equal(notifications, 1, 'API prerequisite: completed failed wave is observed');
      assert.equal(actual.src, url); assert.equal(s.sources.get(actual), url); assert.equal(s.urls.has(url), true); assert.equal(s.attempts, before);
      quotaFields(s, { sources: 1, residentEntries: 1 }); s.check();
      s.inject = null; let recovered = 0; thumbs.release(result => { assert.equal(result.failed, false); recovered++; });
      assert.equal(recovered, 1); assert.equal(actual.hasAttribute('src'), false); assert.equal(actual.handlers('error').length, 0);
      assert.equal(s.attempts, before + 1); assert.equal(s.urls.size + s.sources.size, 0); s.check();
    } finally { s.inject = null; thumbs.release(); }
  });
  await t.test('different pending observer rejects without losing its real release debt', async sub => {
    const s = installFixture(sub), thumbs = s.make(), rows = rowsOf([att(1)]);
    amendmentCase(s); s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
    const before = s.attempts; let reached = false, notified = 0, wrong = 0, contract = null;
    const observer = result => { notified++; assert.equal(result.failed, true); assert.equal(result.error, contract); };
    s.inject = (operation, node, key) => {
      if (reached || operation !== 'attribute' || key !== 'src' || node.localName !== 'img') return;
      reached = true; thumbs.release(observer);
      assert.throws(() => thumbs.release(() => wrong++), error => { contract = error; return error instanceof Error; });
      assert.equal(notified + wrong, 0); assert.equal(s.attempts, before);
    };
    s.onRevoke = () => { if (s.attempts === before + 1) throw false; };
    try {
      exactThrow(() => s.transaction(thumbs, rows), false);
      assert.equal(notified, 1); assert.equal(wrong, 0); assert.equal(s.attempts, before + 2);
      assert.equal(s.urls.size + s.sources.size, 0); s.check(); contract = null;
    } finally { s.inject = s.onRevoke = null; thumbs.release(); }
  });
  for (const failure of [false, undefined]) await t.test(`notification exception is an actual current throw ${String(failure)}`, async sub => {
    const s = installFixture(sub), thumbs = s.make(); amendmentCase(s);
    s.transaction(thumbs, rowsOf([att(1)])); await s.flush(); await s.finish(s.findCall(sha(1)));
    let notified = 0;
    exactThrow(() => thumbs.release(result => { notified++; assert.equal(result.failed, false); throw failure; }), failure);
    assert.equal(notified, 1); assert.equal(s.urls.size + s.sources.size, 0);
    let next = 0; thumbs.release(result => { assert.equal(result.failed, false); next++; });
    assert.equal(next, 1, 'completed observer/wave was cleared before notification'); s.check();
  });
});

root('completion_reentry_fresh_wave', async t => {
  for (const kind of ['wrapper', 'thread']) for (const failure of [false, undefined]) {
    await t.test(`${kind}: failed completion explicitly retries ${String(failure)}`, async sub => {
      const s = installFixture(sub, { limits: { N: 1 } }), listeners = liveListeners(s); amendmentCase(s);
      let off = null, initial = 0, fresh = 0, probe = 0, inInitial = false, inFresh = false;
      try {
        if (kind === 'thread') { off = await s.mount(); s.forgetMount(off); }
        else {
          const thumbs = s.make(); off = thumbs.release; s.forgetWrapper(thumbs);
          s.transaction(thumbs, rowsOf([att(1)]));
        }
        assert.equal(s.offs.length + s.wrappers.size, 0, 'only the actual off/release owner may retry');
        await finishVisible(s);
        const image = s.root.querySelector('button.thumb img'), url = readyURL(s);
        assert.ok(image && url); const attempts = s.attempts, revoked = s.revoked, made = s.made, issued = s.issued;
        const completed = () => {
          assert.equal(image.hasAttribute('src'), false); assert.equal(image.handlers('error').length, 0);
          assert.equal(s.sources.size, 0); assert.equal(s.urls.size, 0); assert.equal(s.urls.has(url), false);
          assert.equal(s.attempts, attempts + 2); assert.equal(s.revoked, revoked + 1);
          if (s.budget) for (const value of Object.values(s.budget.stats)) assert.equal(value, 0);
        };
        const probeObserver = result => {
          probe++; assert.equal(probe, 1); assert.equal(inInitial && inFresh, true);
          assert.equal(result.failed, false); completed();
        };
        const freshObserver = result => {
          fresh++; assert.equal(fresh, 1); assert.equal(inInitial, true); assert.equal(result.failed, false);
          completed(); inFresh = true; off(probeObserver);
          assert.equal(probe, 1, 'fresh successful wave can itself reenter before its callback returns'); inFresh = false;
        };
        const initialObserver = result => {
          initial++; assert.equal(initial, 1); assert.equal(result.failed, true); assert.equal(result.error, failure);
          assert.equal(s.attempts, attempts + 1); assert.equal(s.urls.has(url), true); assert.equal(s.sources.size, 0);
          if (s.budget) quotaFields(s, { sources: 1, residentEntries: 1, residentBytes: 1 });
          s.onRevoke = null; inInitial = true; // Clear the fault INSIDE completion; retry only by an explicit test call.
          s.onRevoke = value => { assert.equal(value, url); assert.equal(inInitial, true); assert.equal(s.attempts, attempts + 2); };
          off(freshObserver);
          assert.equal(fresh, 1, 'new observer notified synchronously while failed callback remains on-stack');
          completed(); inInitial = false;
        };
        s.onRevoke = value => { assert.equal(value, url); assert.equal(s.sources.size, 0); throw failure; };
        exactThrow(() => off(initialObserver), failure); // Inner success cannot erase the outer current falsy throw.
        assert.equal(initial, 1, 'API prerequisite: real release/off completion notification');
        assert.equal(fresh, 1); assert.equal(probe, 1); assert.equal(inInitial || inFresh, false);
        completed(); assert.equal(s.made, made); assert.equal(s.issued, issued);
        retiredFixture(s, listeners); s.check();
      } finally {
        s.onRevoke = null; off?.(); // Only the actual surviving release/off owner, not a fixture rescue handle.
      }
    });
  }
});

root('construction_listener_release_reentry', async t => {
  for (const seam of ['constructor', 'listener']) await t.test(seam, async sub => {
    const s = installFixture(sub, { limits: { N: 1 } }), thumbs = s.make(), rows = rowsOf([att(1)]);
    amendmentCase(s); s.transaction(thumbs, rows); await s.flush(); await s.finish(s.findCall(sha(1)));
    const url = readyURL(s), create = s.doc.createElement, counts = [s.issued, s.made, s.revoked];
    assert.ok(url && s.urls.has(url)); quotaFields(s, { sources: 1, residentEntries: 1 });
    let actual = null, fired = false, settled = 0, resumed = 0, afterSuccess = 0, outcome = null;
    const observer = result => { settled++; outcome = result; assert.equal(s.sources.size, 0); };
    const release = () => {
      fired = true;
      thumbs.release(observer);
    };
    s.doc.createElement = tag => {
      const image = create(tag);
      if (tag.toLowerCase() !== 'img') return image;
      assert.equal(actual, null, 'only one replacement actual image is constructed');
      actual = image;
      if (seam === 'listener') {
        const add = image.addEventListener;
        image.addEventListener = function(type, fn, options) {
          const result = add.call(this, type, fn, options);
          if (!fired && type === 'error') {
            assert.equal(this.handlers('error').includes(fn), true, 'release follows real error-listener registration');
            release();
          }
          return result;
        };
      }
      return image;
    };
    s.inject = (operation, node, key) => {
      if (!fired && seam === 'constructor' && operation === 'createElement' && node === 'img') release();
      if (operation === 'attributeAfter' && node === actual && key === 'src') {
        resumed++;
        if (settled) afterSuccess++;
        assert.equal(actual.src, url, 'observe the saved actual image, including disconnected writes');
        s.tap(() => assert.ok(s.sources.size <= s.budget.stats.sources, 'resumed src requires a real source permit'));
        s.tap(() => assert.equal(settled, 0, 'old src assignment cannot follow successful teardown completion'));
      }
    };
    try {
      s.transaction(thumbs, rows);
      assert.equal(fired, true, 'actual construction/listener dependency reached release');
      assert.ok(actual); assert.ok(resumed <= 1, 'bounded actual resumed src observations');
      assert.equal(afterSuccess, 0, 'no old src write after successful release completion');
      assert.equal(settled, 1); assert.equal(outcome.failed, false);
      assert.equal(actual.hasAttribute('src'), false); assert.equal(actual.handlers('error').length, 0);
      assert.equal(s.sources.size, 0); assert.equal(s.urls.has(url), false);
      assert.deepEqual([s.issued, s.made, s.revoked], [counts[0], counts[1], counts[2] + 1]);
      for (const value of Object.values(s.budget.stats)) assert.equal(value, 0);
      s.check();
    } finally {
      s.inject = null; s.doc.createElement = create;
      thumbs.release();
    }
  });
});
