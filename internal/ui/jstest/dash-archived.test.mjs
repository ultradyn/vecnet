import test from 'node:test';
import assert from 'node:assert/strict';
import { createStore } from '../static/store.js';
import { mount } from '../static/dash.js';

// Enough of a document for the dashboard to draw the peer table. node has no
// DOM; md-dom.test.mjs keeps its own shim on globalThis.document afterwards.
class El {
  constructor(tag) {
    this.tag = tag;
    this.attrs = {};
    this.kids = [];
    this.className = '';
    this.on = {};
    this.hidden = false;
    this.isConnected = false;
    this.classList = { add() {}, remove() {}, contains: () => false };
  }
  get childNodes() { return this.kids; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => (typeof k === 'string' ? k : k.textContent)).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { this.on[type] = fn; }
  append(...xs) {
    for (const x of xs) {
      if (x == null || x === false) continue;
      if (typeof x !== 'string' && !(x instanceof El)) throw new Error('append got ' + Object.prototype.toString.call(x));
      this.kids.push(x);
    }
  }
  replaceChildren(...xs) { this.kids = []; this.append(...xs); }
  querySelector(sel) { return walk(this, sel, false); }
  querySelectorAll(sel) {
    const out = [];
    walk(this, sel, false, out);
    return out;
  }
}

function matches(el, sel) {
  if (sel.startsWith('#')) return el.attrs.id === sel.slice(1);
  if (sel.startsWith('.')) return el.className.split(/\s+/).includes(sel.slice(1));
  return el.tag === sel;
}
function walk(el, sel, self, out) {
  if (!(el instanceof El)) return out ? undefined : null;
  if (self && matches(el, sel)) {
    if (out) out.push(el);
    else return el;
  }
  for (const kid of el.kids) {
    const hit = walk(kid, sel, true, out);
    if (!out && hit) return hit;
  }
  return out ? undefined : null;
}

function peer(id, name, kind, extra = {}) {
  return {
    peer: {
      peer_id: id, name, kind, capabilities: [], quota: {}, location: null,
      presence: 'idle', reachability: 'connected', last_seen: null, enrolled_at: null, teams: [],
    },
    principal_id: 'prn_x', credential: extra.credential || 'active', disabled: !!extra.disabled,
    archived_at: extra.archived_at || null, presence_at: null, reach_via: null, routes: [],
  };
}

const page = body => ({ now: '2026-11-07T01:00:00.000Z', ...body });

async function withPeers(peers, fn) {
  const prev = globalThis.document;
  const setInterval = globalThis.setInterval;
  const clearInterval = globalThis.clearInterval;
  globalThis.document = { createElement: tag => new El(tag) };
  globalThis.setInterval = () => 0;
  globalThis.clearInterval = () => {};
  const api = {
    counters: async () => page({}),
    adminPeers: async () => page({ peers }),
    principals: async () => page({ principals: [] }),
    threads: async () => page({ threads: [], next: null }),
    deliveryHealth: async () => page({ blocked: [], failed: [] }),
    deadLetters: async () => page({ dead_letters: [] }),
    loopCuts: async () => page({ total: 0, hours: 24, by_reason: {} }),
    audit: async () => page({ entries: [], next: null }),
    subscriptions: async () => [],
  };
  const store = createStore({ api });
  const root = new El('div');
  let unmount = () => {};
  try {
    unmount = mount(root, { api, store });
    await store.settled();
    await fn(root.querySelector('#d-peers'));
  } finally {
    unmount();
    globalThis.document = prev;
    globalThis.setInterval = setInterval;
    globalThis.clearInterval = clearInterval;
  }
}

test('dashboard hides archived peers until Show archived is on', async () => {
  await withPeers([
    peer('peer_op', 'operator', 'operator'),
    peer('peer_live', 'live-bot', 'session'),
    peer('peer_off', 'paused-bot', 'session', { disabled: true, credential: 'revoked' }),
    peer('peer_old', 'retired-bot', 'session', { disabled: true, credential: 'revoked', archived_at: '2026-11-07T01:00:00.000Z' }),
  ], peers => {
    const toggle = () => peers.querySelector('input');
    const text = () => peers.textContent;
    assert.equal(toggle().attrs.type, 'checkbox');
    assert.equal(toggle().attrs['aria-label'], 'Show archived');
    assert.equal(toggle().attrs.checked, undefined);
    assert.match(text(), /Show archived \(1\)/);
    const chips = () => peers.querySelectorAll('.chip').map(el => el.textContent);
    assert.match(text(), /live-bot/);
    assert.match(text(), /paused-bot/);
    assert.deepEqual(chips(), ['disabled']);
    assert.doesNotMatch(text(), /retired-bot/);
    assert.match(peers.querySelector('h2').textContent, /Peers 3/);

    toggle().on.change({ target: { checked: true } });
    assert.equal(toggle().attrs.checked, '');
    assert.match(text(), /retired-bot/);
    assert.deepEqual(chips(), ['disabled', 'archived']);
    assert.match(text(), /live-bot/);
    assert.match(text(), /paused-bot/);
    assert.match(peers.querySelector('h2').textContent, /Peers 4/);

    toggle().on.change({ target: { checked: false } });
    assert.doesNotMatch(text(), /retired-bot/);
    assert.equal(toggle().attrs.checked, undefined);
  });
});

test('an operator list that is only archived peers says they are hidden', async () => {
  await withPeers([
    peer('peer_old', 'retired-bot', 'session', { disabled: true, archived_at: '2026-11-07T01:00:00.000Z' }),
  ], peers => {
    assert.match(peers.textContent, /Archived peers are hidden/);
    assert.match(peers.textContent, /Show archived \(1\)/);
    assert.doesNotMatch(peers.textContent, /retired-bot/);
    assert.doesNotMatch(peers.textContent, /No peers enrolled/);
    peers.querySelector('input').on.change({ target: { checked: true } });
    assert.match(peers.textContent, /retired-bot/);
    assert.deepEqual(peers.querySelectorAll('.chip').map(el => el.textContent), ['archived']);
  });
});
