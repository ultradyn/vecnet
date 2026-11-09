import test, { after } from 'node:test';
import assert from 'node:assert/strict';

// Self-contained modeled shell. Import the actual app once, after URL capture.
// No screen/coordinator substitution, session export, production parser or cache mock.
const NOW = '2026-11-09T10:00:00Z', SECRET_KEY = 'vecnet.ui.secret';
const sha = n => n.toString(16).padStart(64, '0');
const rect = (left = 0, top = 0, width = 900, height = 900) => ({ left, top, width, height, right: left + width, bottom: top + height });
const turn = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
function thrown(fn) { try { fn(); return { failed: false }; } catch (error) { return { failed: true, error }; } }
function exact(result, error) { assert.equal(result.failed, true); assert.equal(result.error, error); }
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });

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
    this.env.inject?.('attributeAfter', this, key);
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

function shell(t) {
  const s = { errors: [], overflow: false, actions: [], urls: new Map(), kinds: new Map(), sources: new Map(), active: new Map(),
    raf: new Map(), timers: new Map(), tasks: [], observers: new Set(), serial: 0, clock: 0, replacements: 0,
    width: 1440, height: 900, documentHeight: 900, made: 0, revoked: 0, fault: null, revokeFault: null, loginGuard: null };
  s.keydownOff = [];
  s.record = action => { assert.ok(s.actions.length < 256, 'scalar action overflow'); s.actions.push(String(action)); };
  s.recordError = error => { if (s.errors.length < 16) s.errors.push(error); else s.overflow = true; };
  s.tap = fn => { try { fn(); } catch (error) { s.recordError(error); } };
  s.check = () => { assert.equal(s.overflow, false); assert.deepEqual(s.errors, []); assert.ok(s.active.size <= 64); };
  s.take = error => { assert.equal(s.overflow, false); assert.equal(s.errors.length, 1); assert.equal(s.errors[0], error); s.errors.splice(0, 1); };
  s.audit = () => s.tap(() => {
    assert.ok(s.sources.size <= 64, 'bounded actual-image observations');
    const thumbnails = [...s.sources.values()].filter(url => s.kinds.get(url) === 'thumb');
    assert.ok(thumbnails.length <= 16, 'document default physical bound; exact permit audits live in lifecycle suite');
  });
  s.boxOf = node => node.matches('.composer') ? rect(0, 720, 900, 180) : node.matches('.thumb,.msg') ? rect(24, 100, 240, 180) : rect();
  s.rawBox = s.boxOf;
  s.visible = node => {
    if (!node?.isConnected) return false;
    for (let p = node; p; p = p.parentElement) if (p.hidden || p.inert || p.style.display === 'none' || (p.localName === 'dialog' && !p.open)) return false;
    return true;
  };
  s.inject = (operation, node, key, ...rest) => {
    s.fault?.(operation, node, key, ...rest);
    if (operation === 'removeAttribute' && node.id === 'login' && key === 'hidden') { s.loginGuard?.(); s.record('login'); }
    if (operation === 'replaceAfter' && node.id === 'view') s.record('view');
    if (operation === 'attribute' && node.id === 'aside' && key === 'class') s.record('aside-class');
    if (operation === 'removeEventListener' && node === s.doc && key === 'keydown') {
      assert.ok(s.keydownOff.length < 16, 'document keydown identity overflow');
      s.keydownOff.push(rest[0]); s.record('search-off');
    }
  };
  const doc = new Target(s), win = new Target(s); s.doc = doc; s.win = win;
  doc.nodeType = 9; doc.isConnected = true; doc.parentNode = null;
  doc.documentElement = new ElementNode('html', s); doc.documentElement.parentNode = doc;
  doc.body = new ElementNode('body', s); doc.documentElement.append(doc.body); doc.activeElement = doc.body;
  doc.visibilityState = 'visible'; doc.hasFocus = () => true;
  doc.createElement = tag => tag === 'img' ? new ImageNode(s) : new ElementNode(tag, s);
  doc.createElementNS = (_, tag) => doc.createElement(tag); doc.createTextNode = value => new TextNode(value, s);
  doc.querySelector = selector => doc.documentElement.querySelector(selector); doc.querySelectorAll = selector => doc.documentElement.querySelectorAll(selector);
  doc.getElementById = id => doc.querySelector('[id="' + id + '"]'); doc.execCommand = () => true;
  const add = (tag, id, parent) => { const node = doc.createElement(tag); node.id = id; parent.append(node); return node; };
  const login = add('section', 'login', doc.body), form = add('form', 'login-form', login);
  add('input', 'secret', form); add('button', 'submit', form); add('p', 'login-err', form); login.hidden = true;
  const app = add('div', 'app', doc.body); app.hidden = true;
  for (const id of ['notices', 'hubname', 'inbox-count', 'nav-inbox', 'nav-new', 'nav-dash', 'nav-settings', 'nav-add', 'me', 'search', 'lists', 'results', 'conn', 'signout', 'build-info']) add(id === 'signout' || id === 'nav-add' ? 'button' : 'div', id, app);
  add('div', 'threads', doc.getElementById('lists')); add('div', 'others', doc.getElementById('lists'));
  doc.getElementById('conn').append(doc.createElement('i'), doc.createElement('span'));
  doc.getElementById('signout').textContent = 'Sign out';
  s.main = add('main', 'main', app); s.root = add('div', 'view', s.main); s.aside = add('aside', 'aside', app);
  add('div', 'toast', doc.body); doc.scrollingElement = s.main;
  const data = new Map(), storage = { getItem: key => data.get(key) ?? null, setItem: (key, value) => data.set(key, String(value)), removeItem: key => data.delete(key) };
  const location = { hash: '#thread/thr_test', host: 'synthetic.test', origin: 'http://synthetic.test' };
  class Observer {
    constructor(fn) { assert.ok(s.observers.size < 64); this.fn = fn; this.targets = new Set(); s.observers.add(this); }
    observe(node) { this.targets.add(node); s.observers.add(this); }
    unobserve(node) { this.targets.delete(node); }
    disconnect() { s.fault?.('disconnect', this); this.targets.clear(); s.observers.delete(this); }
  }
  const timer = (fn, ms, every) => { assert.ok(s.timers.size < 64); const id = ++s.serial; s.timers.set(id, { fn, ms, every }); return id; };
  const clear = id => { if (s.timers.delete(id)) s.record('timer-off'); };
  const globals = { document: doc, window: win, location, localStorage: storage, sessionStorage: storage,
    history: { replaceState: (_, __, hash) => { location.hash = hash; } }, Element: ElementNode, HTMLElement: ElementNode, HTMLImageElement: ImageNode,
    Event: FixtureEvent, MouseEvent: FixtureEvent, CustomEvent: FixtureEvent, KeyboardEvent: FixtureEvent, innerWidth: 1440, innerHeight: 900,
    IntersectionObserver: Observer, ResizeObserver: Observer,
    requestAnimationFrame: fn => { assert.ok(s.raf.size < 64); const id = ++s.serial; s.raf.set(id, fn); return id; }, cancelAnimationFrame: id => s.raf.delete(id),
    setTimeout: (fn, ms) => timer(fn, ms, false), setInterval: (fn, ms) => timer(fn, ms, true), clearTimeout: clear, clearInterval: clear,
    getComputedStyle: node => ({ display: s.visible(node) ? 'block' : 'none', visibility: 'visible', overflow: 'visible', overflowY: 'visible', overflowX: 'visible', lineHeight: '20px' }) };
  Object.assign(win, globals, { confirm: () => true, matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }) });
  const descriptors = new Map(Object.keys({ ...globals, fetch: null }).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  const create = URL.createObjectURL, revoke = URL.revokeObjectURL, log = console.error;
  URL.createObjectURL = blob => {
    assert.ok(s.kinds.size < 64 && s.actions.length < 256, 'URL observation overflow before native allocation');
    const url = create(blob), kind = blob.size === 4 ? 'thumb' : 'avatar'; s.made++;
    s.kinds.set(url, kind); s.urls.set(url, { kind, size: blob.size }); s.record('create:' + url); return url;
  };
  URL.revokeObjectURL = url => {
    s.record('attempt:' + url); assert.ok(s.urls.has(url), 'no duplicate successful native revoke');
    if (s.kinds.get(url) === 'thumb') assert.ok(![...s.sources.values()].includes(url), 'thumbnail src removed before native revoke');
    s.revokeFault?.(url); revoke(url); assert.equal(s.urls.delete(url), true); s.revoked++; s.record('revoked:' + url);
  };
  s.peers = ['peer_other']; s.avatars = false; s.heldAvatar = false;
  s.message = (sender, i, threadId) => ({ message_id: 'msg_' + i, thread_id: threadId, sender, priority: 'hold', kind: 'message', recipients: [],
    created_at: NOW, content: { text: 'synthetic body' }, attachments: [{ sha256: sha(1), size: 4, media_type: 'image/png', name: 'fixture.png' }] });
  s.fetch = async (path, options = {}) => {
    const url = new URL(path, location.origin), p = url.pathname;
    s.record('request:' + p + ':' + (options.headers?.Authorization || ''));
    if (p === '/healthz') return json({ version: 'test' });
    if (p === '/v1/peers') return json({ peers: [{ peer_id: 'peer_op', name: 'Operator', kind: 'operator' },
      ...s.peers.map((id, i) => ({ peer_id: id, name: id, kind: 'agent', ...(s.avatars ? { avatar: sha(100 + i) } : {}) }))] });
    if (p === '/v1/admin/activity' || p === '/v1/events') {
      const gate = deferred(), id = ++s.serial;
      const entry = { kind: 'poll', path: p, respond: gate.resolve, signal: options.signal };
      const abort = () => { s.record('pump-abort'); gate.reject(new DOMException('stopped', 'AbortError')); };
      s.active.set(id, entry); assert.ok(s.active.size <= 64);
      if (options.signal?.aborted) abort(); else options.signal?.addEventListener('abort', abort, { once: true });
      try { return await gate.promise; } finally { s.active.delete(id); options.signal?.removeEventListener('abort', abort); }
    }
    if (p.endsWith('/avatar')) {
      const gate = deferred(), id = ++s.serial, entry = { kind: 'avatar', deliver: gate.resolve, path: p };
      s.active.set(id, entry); assert.ok(s.active.size <= 64);
      if (!s.heldAvatar) gate.resolve();
      return { ok: true, status: 200, blob: async () => { try { await gate.promise; return new Blob([new Uint8Array(2)], { type: 'image/png' }); } finally { s.active.delete(id); } } };
    }
    if (p.startsWith('/v1/blobs/')) {
      const body = deferred(), cleanup = deferred(), id = ++s.serial;
      const entry = { kind: 'thumb', path: p, signal: options.signal, deliver: body.resolve, cleanup: cleanup.resolve, cancelled: 0, delivered: false };
      let read = false; s.active.set(id, entry); assert.ok(s.active.size <= 64);
      const cancel = () => { entry.cancelled++; s.record('body-cancel'); return cleanup.promise; };
      return { status: 200, statusText: 'OK', headers: new Headers({ 'Content-Type': 'image/png', 'Content-Length': '4' }),
        body: { getReader: () => ({
          read: async () => { if (read) return { done: true }; read = true; const value = await body.promise; entry.delivered = true; return value === null ? { done: true } : { done: false, value }; },
          cancel, releaseLock: () => { s.record('body-release'); s.active.delete(id); },
        }), cancel } };
    }
    const match = p.match(/^\/v1\/admin\/threads\/([^/]+)$/);
    if (match) return json({ row: { thread: { thread_id: match[1], title: match[1], participants: ['peer_op', ...s.peers], message_count: s.peers.length } }, files: [], open_approvals: [] });
    if (p.endsWith('/messages')) {
      const threadId = p.split('/')[3], messages = s.peers.map((id, i) => s.message(id, i + 1, threadId));
      return json({ messages, hosted_blobs: Object.fromEntries(messages.map(m => [m.message_id, [sha(1)]])), next_after: null });
    }
    if (p.endsWith('/deliveries')) return json({ recipients: ['peer_op'], rows: [] });
    if (p === '/v1/admin/inbox') return json({ me: { peer_id: 'peer_op', name: 'Operator', presence: 'unknown' }, approvals: [], dead_letters: [], woke: [], held: [], unread: 0 });
    if (p === '/v1/admin/peers') return json({ peers: [] });
    if (p === '/v1/threads' || p === '/v1/admin/threads') return json({ threads: [] });
    if (p === '/v1/hub/notices') return json({ notices: [] });
    assert.fail('unexpected synthetic request: ' + p);
  };
  Object.assign(globals, { fetch: s.fetch });
  for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { value, writable: true, configurable: true });
  console.error = error => s.recordError(error);
  s.flush = async () => {
    for (let i = 0; i < 20; i++) {
      await turn(); const frames = [...s.raf.values()], tasks = s.tasks.splice(0); s.raf.clear();
      for (const fn of [...tasks, ...frames]) { s.tap(fn); s.tap(() => s.afterFrame?.()); }
    }
    assert.equal(s.raf.size + s.tasks.length, 0, 'bounded frames drain');
  };
  s.node = id => doc.getElementById(id);
  s.listenerCount = () => [doc, win, s.main, s.node('results')].map(target => [...target.listeners.values()].reduce((n, list) => n + list.length, 0));
  s.route = hash => { location.hash = hash; win.dispatchEvent(new FixtureEvent('hashchange')); };
  s.submit = secret => { s.node('secret').value = secret; form.dispatchEvent(new FixtureEvent('submit', { cancelable: true })); };
  s.signout = () => { const button = s.node('signout'); assert.equal(s.visible(button), true, 'ordinary Sign out remains reachable'); button.click(); };
  s.start = async () => { s.route('#thread/thr_test'); s.submit('synthetic-old'); await s.flush(); assert.ok(s.root.querySelector('.msgs')); };
  s.finishThumb = async () => { const entry = [...s.active.values()].find(x => x.kind === 'thumb'); assert.ok(entry); entry.deliver(new Uint8Array(4)); entry.cleanup(); await s.flush(); return entry; };
  s.count = (op, url) => s.actions.filter(x => x === op + ':' + url).length;
  s.reset = () => {
    assert.equal(s.urls.size, 0, 'previous case must drain through its actual owner');
    assert.equal(s.active.size, 0); assert.equal([...s.sources.values()].filter(url => s.kinds.get(url) === 'thumb').length, 0);
    assert.equal(s.observers.size + s.raf.size + s.tasks.length, 0); assert.equal(s.timers.size, s.baseTimers);
    assert.deepEqual(s.listenerCount(), s.baseListeners); s.check();
    s.actions.length = 0; s.kinds.clear(); s.sources.clear();
    s.keydownOff.length = 0;
    s.peers = ['peer_other']; s.avatars = s.heldAvatar = false;
  };
  s.drain = async () => {
    s.fault = s.revokeFault = s.loginGuard = s.afterFrame = null;
    if (!s.node('app').hidden) s.signout();
    for (let i = 0; i < 4 && s.active.size; i++) {
      for (const entry of s.active.values()) if (entry.kind === 'thumb') { entry.deliver(null); entry.cleanup(); } else if (entry.kind === 'avatar') entry.deliver();
      await s.flush();
    }
    assert.equal(s.urls.size, 0, 'public surviving session drains actual native allocations');
    assert.equal(s.active.size, 0); assert.equal([...s.sources.values()].filter(url => s.kinds.get(url) === 'thumb').length, 0);
    assert.equal(s.observers.size + s.raf.size + s.tasks.length, 0);
    assert.equal(s.timers.size, s.baseTimers); assert.deepEqual(s.listenerCount(), s.baseListeners);
  };
  t.after(async () => {
    try { await s.drain(); s.check(); }
    finally {
      s.keydownOff.length = 0;
      console.error = log; URL.createObjectURL = create; URL.revokeObjectURL = revoke;
      for (const [key, descriptor] of descriptors) if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key];
    }
  });
  return s;
}

const s = shell({ after });
const entry = (name, fn) => test('WEB-MEM-3 T3 actual_app_entry ' + name, { concurrency: false }, fn);
await import('../static/app.js'); // Canonical entry, exactly once; instrumentation already installed.
await s.flush(); s.check();
s.baseTimers = s.timers.size; s.baseListeners = s.listenerCount();
for (const mode of ['route-failure', 'signout-failure', 'independent', 'route-success', 'latest-route', 'close-dominates', 'model-reentry']) {
  for (const failure of mode.endsWith('failure') || mode === 'independent' ? [false, undefined] : [false]) {
    entry(`public owner ${mode} ${String(failure)}`, async () => {
      s.reset(); await s.start();
    const original = s.root.querySelector('.msgs'); let image = null, url = null, reentered = false, independent = false, exitAt = 0, checkedFrame = false;
      const fails = mode.endsWith('failure') || mode === 'independent', later = mode === 'independent' ? (failure === false ? undefined : false) : failure;
    s.afterFrame = () => {
      if (!image || checkedFrame) return;
      checkedFrame = true; assert.equal(image.hasAttribute('src'), false);
      if (fails) { assert.equal(s.urls.has(url), true); assert.equal(s.root.contains(original), true); }
      else if (mode === 'route-success' || mode === 'latest-route') assert.equal(s.root.contains(original), false, 'route mounts before frame stack returns');
      else assert.equal(s.node('login').hidden, false, 'close completes before any later microtask/event');
    };
      s.revokeFault = value => {
        if (value !== url) return;
        assert.equal(s.root.contains(original), true, 'actual view cannot be replaced before native retirement returns');
        assert.equal(s.node('login').hidden, true);
        if (mode === 'model-reentry' && !reentered) {
          reentered = true; s.route('#thread/thr_next'); s.signout();
          assert.equal(s.count('attempt', url), 1, 'public reentry does not recursively dispose or settle the active model');
          assert.equal(s.node('login').hidden, true);
        }
        if (fails) throw later;
      };
      s.fault = (operation, node, key) => {
        if (mode === 'independent' && image && !independent && operation === 'removeEventListener' && node === s.doc && key === 'close') {
          independent = true; throw failure;
        }
        if (image || operation !== 'attribute' || key !== 'src' || node.localName !== 'img') return;
        image = node; url = [...s.urls].find(([, value]) => value.kind === 'thumb')?.[0]; assert.ok(url);
        exitAt = s.actions.length;
        const result = thrown(() => {
          if (mode === 'signout-failure') s.signout();
          else {
            s.route('#inbox');
            if (mode === 'latest-route') s.route('#thread/thr_next');
            if (mode === 'close-dominates') { s.route('#thread/thr_next'); s.signout(); }
          }
        });
        if (mode === 'independent') exact(result, failure); else assert.equal(result.failed, false);
        assert.equal(s.root.contains(original), true); assert.equal(s.node('login').hidden, true);
        assert.equal(s.count('attempt', url), 0, 'pending write defers native disposal');
        if (mode === 'signout-failure' || mode === 'close-dominates') assert.equal(localStorage.getItem(SECRET_KEY), null);
      };
      try {
      await s.finishThumb(); assert.ok(image); assert.equal(checkedFrame, true); assert.equal(image.hasAttribute('src'), false);
        if (fails) {
          s.take(later);
          assert.equal(s.urls.has(url), true); assert.equal(s.count('attempt', url), 1); assert.equal(s.count('revoked', url), 0);
          assert.equal(s.root.contains(original), true); assert.equal(s.node('login').hidden, true);
          assert.equal(s.actions.some(x => x.startsWith('request:/v1/threads/thr_next/')), false);
          if (mode === 'signout-failure') {
            s.submit('synthetic-replacement'); await s.flush();
            assert.equal(localStorage.getItem(SECRET_KEY), null, 'blocked start cannot store replacement credentials');
            assert.equal(s.node('login').hidden, true, 'failed old cleanup cannot be hidden behind login');
            assert.equal(s.actions.filter(x => x === 'request:/v1/peers:Bearer synthetic-replacement').length, 1, 'validation alone is not new-session admission');
          }
          s.fault = s.revokeFault = null; const before = s.count('attempt', url);
          s.signout(); assert.equal(s.urls.has(url), false); assert.equal(s.count('attempt', url), before + 1);
          assert.equal(s.node('login').hidden, false);
          const requests = s.actions.length; await s.flush();
          assert.equal(s.actions.slice(requests).some(x => x.includes('Bearer synthetic-replacement')), false, 'no queued secret/session resumes later');
          s.submit('synthetic-replacement'); await s.flush();
          assert.equal(localStorage.getItem(SECRET_KEY), 'synthetic-replacement');
          assert.equal(s.node('login').hidden, true, 'explicit resubmission may admit the replacement after retirement');
        } else if (mode === 'route-success' || mode === 'latest-route') {
          assert.equal(s.urls.has(url), false); assert.equal(s.count('revoked', url), 1);
          assert.equal(s.root.contains(original), false); assert.equal(s.doc.body.dataset.route, mode === 'route-success' ? 'inbox' : 'thread');
          const path = mode === 'route-success' ? 'request:/v1/admin/inbox:' : 'request:/v1/threads/thr_next/messages:';
          const admitted = s.actions.findIndex((x, i) => i >= exitAt && x.startsWith(path));
          assert.ok(admitted > s.actions.indexOf('revoked:' + url), 'actual successor requests follow successful native retirement');
          if (mode === 'latest-route') assert.equal(s.actions.slice(exitAt).some(x => x.startsWith('request:/v1/admin/inbox:')), false, 'intermediate route is never mounted');
          const successor = s.root.firstChild; await s.flush();
          assert.equal(s.root.firstChild, successor, 'old completion cannot clear the successor');
        } else {
          assert.equal(s.node('login').hidden, false); assert.equal(s.count('revoked', url), 1);
          assert.equal(s.actions.some(x => x.startsWith('request:/v1/threads/thr_next/')), false, 'close dominates the single latest route intent');
          assert.equal(s.actions.filter(x => x === 'login').length, 1, 'one continuation at outer quiescence');
        }
        assert.ok(s.actions.includes('aside-class'), 'actual aside teardown participates');
        s.fault = s.revokeFault = null; s.check();
      } finally { await s.drain(); }
    });
  }
}

for (const failure of [false, undefined]) entry(`pending aside failure ${String(failure)} retains only unfinished cleanup`, async () => {
  s.reset(); await s.start(); const original = s.root.querySelector('.msgs');
  let pending = null, faulted = false, url = null;
  s.fault = (operation, node, key) => {
    if (pending && !faulted && operation === 'attribute' && node === s.aside && key === 'class') { faulted = true; throw failure; }
    if (pending || operation !== 'attribute' || key !== 'src' || node.localName !== 'img') return;
    pending = node; url = [...s.urls].find(([, value]) => value.kind === 'thumb')?.[0];
    exact(thrown(() => s.route('#inbox')), failure);
    assert.equal(s.root.contains(original), true); assert.equal(s.count('attempt', url), 0);
  };
  try {
    await s.finishThumb(); assert.equal(faulted, true); assert.equal(pending.hasAttribute('src'), false);
    assert.equal(s.urls.has(url), false, 'successful thumbnail retirement is not retained because aside failed');
    assert.equal(s.root.contains(original), true, 'failed aside still blocks replacement');
    s.fault = null; s.route('#inbox'); await s.flush();
    assert.equal(s.root.contains(original), false); assert.equal(s.count('attempt', url), 1, 'public retry does not repeat successful published disposal'); s.check();
  } finally { await s.drain(); }
});
for (const failure of [false, undefined]) entry(`401 native failure ${String(failure)} keeps public retry reachable`, async () => {
  s.reset(); await s.start(); await s.finishThumb(); const url = s.root.querySelector('.thumb img')?.src; assert.ok(url);
  s.revokeFault = value => { if (value === url) throw failure; };
  try {
    const poll = [...s.active.values()].find(x => x.path === '/v1/events'); assert.ok(poll);
    poll.respond(json({ error: { code: 'unauthorized', message: 'synthetic' } }, 401)); await s.flush();
    assert.equal(s.node('login').hidden, true); assert.equal(s.visible(s.node('signout')), true);
    assert.equal(s.urls.has(url), true); assert.equal(s.count('attempt', url), 1); assert.equal(localStorage.getItem(SECRET_KEY), null);
    assert.ok(s.actions.includes('pump-abort')); assert.ok(s.actions.includes('search-off'));
    s.revokeFault = null; s.signout(); assert.equal(s.count('attempt', url), 2);
    assert.equal(s.urls.has(url), false); assert.equal(s.node('login').hidden, false); s.check();
  } finally { await s.drain(); }
});
entry('401 uses the actual session owner; successful close needs no second event', async () => {
  s.reset(); await s.start(); await s.finishThumb();
  const url = s.root.querySelector('.thumb img')?.src; assert.ok(url);
  const poll = [...s.active.values()].find(x => x.path === '/v1/events'); assert.ok(poll);
  s.loginGuard = () => assert.equal(s.urls.has(url), false, '401 cannot show login before old native retirement');
  try {
    poll.respond(json({ error: { code: 'unauthorized', message: 'synthetic' } }, 401)); await s.flush();
    assert.equal(s.node('login').hidden, false); assert.equal(localStorage.getItem(SECRET_KEY), null);
    assert.equal(s.count('attempt', url), 1); assert.equal(s.count('revoked', url), 1); s.check();
  } finally { await s.drain(); }
});
entry('held body remains an actual pending cleanup after public close', async () => {
  s.reset(); await s.start(); const held = [...s.active.values()].find(x => x.kind === 'thumb'); assert.ok(held);
  try {
    s.signout(); await s.flush(); assert.equal(held.signal.aborted, true); assert.equal(held.cancelled, 1);
    assert.equal([...s.active.values()].includes(held), true);
    held.deliver(new Uint8Array(4)); await s.flush();
    assert.equal([...s.active.values()].includes(held), true, 'body without terminal cancellation settlement does not finish transport');
    held.cleanup(); await s.flush(); assert.equal([...s.active.values()].includes(held), false); assert.equal(s.urls.size, 0); s.check();
  } finally { await s.drain(); }
});

// R1's parameterized original-avatar oracle occupies this bounded slice only.
for (const mode of ['release', 'prune', 'release-reentry', 'prune-reentry', 'partial', 'success', 'held']) {
  for (const failure of mode === 'success' || mode === 'held' ? [false] : [false, undefined]) {
    entry(`avatar_shutdown_retry_original_url ${mode} ${String(failure)}`, async () => {
      s.reset(); s.avatars = true; s.heldAvatar = mode === 'held'; if (mode === 'partial') s.peers = ['peer_early', 'peer_other'];
      await s.start(); await s.finishThumb();
      const thumb = s.root.querySelector('.thumb img')?.src; assert.ok(thumb);
      const asideBefore = s.actions.filter(x => x === 'aside-class').length;
      const keydown = s.doc.handlers('keydown').slice(0, 3);
      const avatars = s.root.querySelectorAll('.av img[src]'), U = avatars.at(-1)?.src, earlier = mode === 'partial' ? avatars[0]?.src : null;
      const held = mode === 'held' ? [...s.active.values()].find(x => x.kind === 'avatar') : null;
      if (held) assert.ok(held); else assert.ok(U, 'genuinely READY original avatar is displayed by real dom.avatar');
      let reentered = false; const pruneFailure = failure === false ? undefined : false;
      s.loginGuard = () => {
        if (U) assert.equal(s.urls.has(U), false, 'original avatar native disposal precedes login/session forgetting');
        assert.equal(s.urls.has(thumb), false);
      };
      s.revokeFault = url => {
        if (url !== U) return;
        const attempt = s.count('attempt', U);
        if (!reentered && ((mode === 'release-reentry' && attempt === 1) || (mode === 'prune-reentry' && attempt === 2))) {
          reentered = true; s.signout(); assert.equal(s.node('login').hidden, true);
          assert.equal(s.count('attempt', U), attempt, 'active release/prune is pending, never recursively reinvoked');
        }
        if (mode !== 'success' && attempt === 1) throw failure;
        if (mode === 'prune' && attempt === 2) throw pruneFailure;
      };
      try {
        const initial = thrown(s.signout);
        if (mode === 'success' || mode === 'held') {
          assert.equal(initial.failed, false); assert.equal(s.node('login').hidden, false, 'no-failure shutdown continues without another action');
          if (U) { assert.equal(s.count('attempt', U), 1); assert.equal(s.count('revoked', U), 1); }
          if (held) {
            const made = s.made; held.deliver(); await s.flush(); assert.equal(s.made, made, 'dead cache cannot publish an ordinary late avatar body');
            assert.equal(s.root.querySelector('.av img'), null);
          }
        } else {
          exact(initial, failure); assert.equal(s.count('attempt', U), 1); assert.equal(s.count('revoked', U), 0);
          assert.equal(s.urls.has(U), true); assert.equal(s.node('login').hidden, true); assert.equal(s.visible(s.node('signout')), true);
          const initialThumbAllocated = s.urls.has(thumb);
          const initialAsideWork = s.actions.filter(x => x === 'aside-class').length;
          const initialKeydownOff = s.keydownOff.slice();
          const initialPumpAborted = s.actions.includes('pump-abort');
          const initialSecretPresent = localStorage.getItem(SECRET_KEY) !== null;
          await s.flush(); assert.equal(s.count('attempt', U), 1, 'no implicit retry in the failed close wave');
          if (mode === 'prune') { exact(thrown(s.signout), pruneFailure); assert.equal(s.count('attempt', U), 2); assert.equal(s.node('login').hidden, true); }
          const expected = mode === 'prune' ? 3 : 2;
          assert.equal(thrown(s.signout).failed, false, 'ordinary visible retry uses the retained original cache action');
          assert.equal(s.count('attempt', U), expected); assert.equal(s.count('revoked', U), 1); assert.equal(s.urls.has(U), false);
          assert.equal(s.node('login').hidden, false);
          assert.equal(initialThumbAllocated, false, 'thumbnail cleanup is independent of avatar failure');
          assert.ok(initialAsideWork > asideBefore);
          assert.equal(initialKeydownOff.length, 2, 'initial close retires both document keydown callbacks once');
          assert.deepEqual(new Set(initialKeydownOff), new Set(keydown), 'initial removals match the actual registered callback identities');
          assert.ok(initialPumpAborted);
          assert.equal(initialSecretPresent, false);
          assert.deepEqual(s.keydownOff, initialKeydownOff, 'public retries preserve the successful callback identity sequence without replay');
          if (mode.includes('reentry')) assert.equal(reentered, true);
          if (earlier) assert.equal(s.count('revoked', earlier), 1, 'partial release success is not revoked again by prune');
        }
        assert.equal(new Set(keydown).size, 2, 'the actual search/reaction document callbacks are distinct');
        assert.equal(s.keydownOff.length, 2, 'both document keydown callbacks retire exactly once');
        assert.deepEqual(new Set(s.keydownOff), new Set(keydown), 'only the actual registered callbacks were removed');
        const disposed = U ? s.count('attempt', U) : 0;
        s.node('signout').click(); if (U) assert.equal(s.count('attempt', U), disposed, 'completed action is idempotent');
        assert.equal(s.keydownOff.length, 2, 'completed close does not replay a successful callback removal');
        s.revokeFault = s.loginGuard = null; await s.flush(); s.check();
      } catch (error) {
        try { await s.drain(); } finally { throw error; }
      }
      await s.drain();
    });
  }
}

for (const failure of [false, undefined]) entry(`closing_show_more_refuses_old_session ${String(failure)}`, async () => {
  s.reset(); s.avatars = true;
  const fetchBefore = globalThis.fetch, requests = [];
  const rows = Array.from({ length: 52 }, (_, i) => ({
    thread: { thread_id: 'thr_other_' + i, title: 'Other ' + i, participants: ['peer_other'], message_count: 1 },
  }));
  let U = null;
  globalThis.fetch = async (path, options = {}) => {
    const url = new URL(path, location.origin), response = await fetchBefore(path, options);
    if (url.pathname !== '/v1/admin/threads' || !url.searchParams.has('exclude_participant')) return response;
    assert.ok(requests.length < 8, 'bounded actual Other Threads requests');
    requests.push({ limit: Number(url.searchParams.get('limit')), authorization: options.headers?.Authorization });
    return json({ threads: rows, next: 'synthetic-next-page' });
  };
  try {
    await s.start(); await s.finishThumb();
    const others = s.node('others'), more = () => others.querySelector('button.more');
    assert.equal(others.querySelectorAll('a[href^="#thread/"]').length, 50);
    assert.ok(more() && s.visible(more())); assert.equal(more().textContent, 'Show more');
    more().click(); await s.flush();
    assert.equal(others.querySelectorAll('a[href^="#thread/"]').length, 52, 'live control expands the real page');
    assert.deepEqual(requests.map(x => x.limit), [50, 100], 'live Show more changes the actual request limit');
    assert.ok(requests.every(x => x.authorization === 'Bearer synthetic-old'));
    U = s.root.querySelectorAll('.av img[src]').at(-1)?.src; assert.ok(U && s.urls.has(U));
    const thumb = s.root.querySelector('.thumb img')?.src; assert.ok(thumb);
    s.revokeFault = url => { if (url === U) throw failure; };
    exact(thrown(s.signout), failure); await s.flush();
    assert.equal(s.urls.has(U), true); assert.equal(s.urls.has(thumb), false);
    assert.equal(s.node('login').hidden, true); assert.ok(s.visible(s.node('signout')));
    assert.equal(localStorage.getItem(SECRET_KEY), null);
    const button = more(); assert.ok(button && s.visible(button)); assert.equal(button.disabled, false);
    assert.equal(button.closest('[inert]'), null, 'real sidebar control is outside inert view/aside');
    const before = requests.slice(), contents = others.textContent;
    const oldRequests = () => s.actions.filter(x => x === 'request:/v1/admin/threads:Bearer synthetic-old').length;
    const count = oldRequests();
    button.click(); await s.flush();
    assert.equal(oldRequests(), count, 'closing Show more admits no authenticated old-session threads request');
    assert.deepEqual(requests, before, 'closing Show more admits no request with a mutated old limit');
    assert.equal(others.textContent, contents, 'retained old page/results do not expand during close');
    assert.equal(s.urls.has(U), true); assert.ok(s.visible(s.node('signout'))); s.check();
  } finally {
    s.revokeFault = null;
    try {
      await s.drain(); assert.equal(s.node('login').hidden, false);
      if (U) { assert.equal(s.urls.has(U), false); assert.equal(s.count('revoked', U), 1); }
    } finally { globalThis.fetch = fetchBefore; requests.length = 0; }
  }
});
