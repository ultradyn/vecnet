import test from 'node:test';
import assert from 'node:assert/strict';
import { mount } from '../static/dash.js';
import { createStore } from '../static/store.js';
import { El, installDom, snapshot, peer } from './activity-dom-fixture.mjs';

const PAGE = 50;
const descending = top => Array.from({ length: Math.min(PAGE, top) }, (_, i) => top - i);
function auditPage(top, { limit = PAGE, before } = {}) {
  const last = Math.min(top, before == null ? top : Number(before) - 1);
  const ids = Array.from({ length: Math.max(0, Math.min(limit, last)) }, (_, i) => last - i);
  return {
    entries: ids.map(id => ({ id, at: snapshot().now, actor: 'peer_actor', action: `audit-id-${id}`, target_ids: [], detail: { id } })),
    next: ids.at(-1) > 1 ? String(ids.at(-1)) : null,
  };
}
function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}
const section = root => root.querySelector('#d-audit');
const shownIDs = root => section(root).querySelectorAll('.act').map(el => {
  assert.match(el.textContent, /^audit-id-\d+$/);
  return Number(el.textContent.slice('audit-id-'.length));
});
function button(root, label, required = true) {
  const found = section(root).querySelectorAll('button').find(el =>
    new RegExp(`^(?:Load )?${label}(?: entries)?$`, 'i').test(el.textContent.trim()));
  if (required) assert.ok(found, `audit must expose ${label} navigation`);
  return found;
}
function press(root, label) {
  const el = button(root, label);
  assert.equal(el.getAttribute('disabled'), null, `${label} must be usable`);
  return el.on.click({ preventDefault() {} });
}
async function navigate(h, label) {
  await press(h.root, label);
  await h.store.settled();
  // Pagination need not be a store resource; drain its resolved continuations.
  for (let i = 0; i < 4; i++) await Promise.resolve();
}

async function dashboard(fn, { top = PAGE, peers = [peer('peer_a')] } = {}) {
  const restore = installDom();
  const original = { set: globalThis.setInterval, clear: globalThis.clearInterval };
  const create = document.createElement, createNS = document.createElementNS;
  let created = 0, nextTimer = 0, unmount;
  const timers = new Map(), watchers = new Set(), writes = new WeakMap(), requests = [];
  document.createElement = tag => { created++; return create(tag); };
  document.createElementNS = (ns, tag) => {
    created++;
    const el = createNS(ns, tag), set = el.setAttribute.bind(el);
    writes.set(el, 0);
    el.setAttribute = (key, value) => { writes.set(el, writes.get(el) + 1); set(key, value); };
    return el;
  };
  globalThis.setInterval = (fn, ms) => { const id = ++nextTimer; timers.set(id, { fn, ms }); return id; };
  globalThis.clearInterval = id => { timers.delete(id); };
  const state = { top, peers, activity: snapshot(), auditError: null, peerError: null, activityError: null, olderReply: null };
  const api = {
    activityStats: async () => { if (state.activityError) throw state.activityError; return state.activity; },
    counters: async () => ({}),
    adminPeers: async () => { if (state.peerError) throw state.peerError; return { peers: state.peers }; },
    principals: async () => ({ principals: [] }), threads: async () => ({ threads: [] }),
    deliveryHealth: async () => ({}), deadLetters: async () => ({}), loopCuts: async () => ({}),
    subscriptions: async () => [],
    audit: async args => {
      if (args.before != null && state.olderReply) return state.olderReply(args);
      if (state.auditError) throw state.auditError;
      return auditPage(state.top, args);
    },
  };
  for (const name of Object.keys(api)) {
    const load = api[name];
    api[name] = (...args) => { requests.push({ name, args }); return load(...args); };
  }
  const store = createStore({ api }), root = new El('main');
  const stop = () => { if (unmount) { const off = unmount; unmount = null; off(); } };
  const h = {
    root, store, state, requests, stop, timers, watchers, created: () => created,
    chartWrites: cell => {
      const svg = cell.querySelector('svg');
      return [svg, ...svg.querySelectorAll('line'), ...svg.querySelectorAll('path'), ...svg.querySelectorAll('polyline')]
        .reduce((n, el) => n + (writes.get(el) || 0), 0);
    },
    refresh: async (...tags) => { store.invalidate(...tags); await store.settled(); },
    tick: async () => {
      const reloads = [...timers.values()].filter(t => t.ms === 30000);
      assert.equal(reloads.length, 1, 'one dashboard refresh timer');
      reloads[0].fn(); await store.settled();
    },
  };
  try {
    unmount = mount(root, { api, store, nameOf: id => id, activity: {
      subscribe: fn => { watchers.add(fn); return () => watchers.delete(fn); },
    } });
    await store.settled();
    await fn(h);
  } finally {
    stop();
    const remaining = { timers: timers.size, watchers: watchers.size };
    restore(); globalThis.setInterval = original.set; globalThis.clearInterval = original.clear;
    assert.deepEqual(remaining, { timers: 0, watchers: 0 }, 'unmount releases timers and activity subscription');
  }
}

test('CONTROL audit same-head refresh deduplicates and errors preserve the last good page', async () => {
  await dashboard(async h => {
    for (let i = 0; i < 5; i++) { await h.tick(); assert.deepEqual(shownIDs(h.root), descending(PAGE)); }
    h.state.auditError = new Error('audit offline'); await h.tick();
    assert.deepEqual(shownIDs(h.root), descending(PAGE));
    assert.match(section(h.root).textContent, /Audit tail unavailable.*audit offline/);
    h.state.auditError = null; await h.tick();
    assert.deepEqual(shownIDs(h.root), descending(PAGE));
    assert.doesNotMatch(section(h.root).textContent, /audit offline/);
  });
});

test('REGRESSION 100 disjoint audit heads retain only the current 50 rendered entries', async () => {
  await dashboard(async h => {
    const oversized = [];
    for (let i = 1; i <= 100; i++) {
      h.state.top += PAGE; await h.tick();
      const ids = shownIDs(h.root);
      if (ids.length > PAGE) oversized.push({ cycle: i, rows: ids.length });
    }
    assert.equal(h.requests.filter(r => r.name === 'audit').length, 101, '100 real timer refreshes plus initial load');
    assert.deepEqual(oversized, [], 'each live head is a single bounded page');
    assert.deepEqual(shownIDs(h.root), descending(h.state.top));
  });
});

test('REGRESSION Older/Newer/Latest reach all 5050 prior entries while fresh heads arrive', async () => {
  await dashboard(async h => {
    const pages = [shownIDs(h.root)];
    let oldest = pages[0].at(-1);
    while (oldest > 1) {
      assert.ok(pages.length <= 101, 'finite older traversal');
      await navigate(h, 'Older');
      const ids = shownIDs(h.root);
      assert.deepEqual(ids, descending(oldest - 1), 'exclusive cursor, ordered single page, no gaps');
      pages.push(ids); oldest = ids.at(-1);
      if (pages.length === 2 || pages.length === 51) {
        h.state.top += PAGE; await h.tick();
        assert.deepEqual(shownIDs(h.root), ids, 'historical selection survives new head arrivals');
      }
    }
    assert.deepEqual(pages.flat(), Array.from({ length: 5050 }, (_, i) => 5050 - i));
    const end = button(h.root, 'Older', false);
    assert.ok(!end || end.getAttribute('disabled') !== null, 'oldest page has no further usable Older');
    for (let i = pages.length - 2; i >= 0; i--) {
      await navigate(h, 'Newer');
      assert.deepEqual(shownIDs(h.root), i === 0 ? descending(h.state.top) : pages[i]);
    }
    await navigate(h, 'Older');
    assert.deepEqual(shownIDs(h.root), descending(h.state.top - PAGE), 'new history between sampled heads stays reachable');
    await navigate(h, 'Latest');
    assert.deepEqual(shownIDs(h.root), descending(h.state.top));
    assert.ok(h.requests.filter(r => r.name === 'audit').every(r => r.args[0].limit === PAGE));
  }, { top: 5050 });
});

test('CONTROL older-page errors retain the selected audit page and allow retry', async () => {
  await dashboard(async h => {
    const ids = shownIDs(h.root);
    h.state.olderReply = async () => { throw new Error('older offline'); };
    await navigate(h, 'Older');
    assert.deepEqual(shownIDs(h.root), ids);
    assert.match(section(h.root).textContent, /older.*offline/i);
    button(h.root, 'Older');
    h.state.olderReply = null;
    await navigate(h, 'Older');
    assert.ok(shownIDs(h.root).includes(50), 'retry reaches older history');
    assert.doesNotMatch(section(h.root).textContent, /older offline/);
  }, { top: 100 });
});

for (const selection of ['Newer', 'Latest']) {
  test(`REGRESSION pending Older reply cannot overwrite ${selection} selection`, async () => {
    await dashboard(async h => {
      await navigate(h, 'Older');
      const pending = deferred(), reply = auditPage(150, { before: 51 });
      h.state.olderReply = () => pending.promise;
      const flight = press(h.root, 'Older');
      try {
        await navigate(h, selection);
        h.state.top += PAGE; await h.tick();
        const selected = shownIDs(h.root);
        assert.deepEqual(selected, descending(h.state.top), `${selection} returns to the live head`);
        pending.resolve(reply); await flight;
        for (let i = 0; i < 4; i++) await Promise.resolve();
        assert.deepEqual(shownIDs(h.root), selected, 'obsolete response leaves the current selection intact');
      } finally { pending.resolve(reply); await flight; }
    }, { top: 150 });
  });
}

test('CONTROL pending Older reply after unmount cannot render or restart subscriptions', async () => {
  await dashboard(async h => {
    const pending = deferred(), reply = auditPage(100, { before: 51 });
    h.state.olderReply = () => pending.promise;
    const flight = press(h.root, 'Older');
    try {
      h.stop();
      const ids = shownIDs(h.root), created = h.created(), calls = h.requests.length;
      pending.resolve(reply); await flight;
      await h.refresh('peers', 'threads', 'subs', 'dashboard-activity');
      assert.deepEqual(shownIDs(h.root), ids);
      assert.equal(h.created(), created, 'no DOM construction after unmount');
      assert.equal(h.requests.length, calls, 'unsubscribed resources do not reload');
      assert.equal(h.timers.size, 0); assert.equal(h.watchers.size, 0);
    } finally { pending.resolve(reply); await flight; }
  }, { top: 100 });
});

test('CONTROL same peer keeps focused chart, open minute table and keyboard minute through errors', async () => {
  await dashboard(async h => {
    const cell = h.root.querySelector('.peer-activity'), svg = cell.querySelector('svg');
    const details = cell.querySelector('details');
    svg.focus(); svg.on.keydown({ key: 'Home', preventDefault() {} }); details.open = true;
    const before = h.chartWrites(cell);
    h.state.peerError = new Error('directory offline');
    h.state.activityError = new Error('stats offline'); await h.tick();
    assert.equal(h.root.querySelector('.peer-activity'), cell);
    assert.match(h.root.querySelector('#d-peers').textContent, /Peers unavailable.*directory offline/);
    assert.match(cell.textContent, /stale/);
    assert.match(cell.querySelector('.spark-readout').textContent, /1 messages.*11:00:30/);
    h.state.peerError = null; h.state.activityError = null;
    h.state.activity = snapshot({ peer_a: [9, 4] }); await h.tick();
    assert.equal(h.root.querySelector('.peer-activity'), cell);
    assert.equal(cell.querySelector('svg'), svg);
    assert.equal(document.activeElement, svg); assert.equal(details.open, true);
    assert.equal(cell.querySelector('tbody').children.length, 60);
    assert.match(cell.querySelector('.spark-readout').textContent, /9 messages.*11:00:30/);
    assert.doesNotMatch(cell.textContent, /stale/);
    assert.ok(h.chartWrites(cell) > before, 'instrumentation observes real active chart updates');
  });
});

test('REGRESSION 100 peer replacements stop updating every retired chart', async () => {
  await dashboard(async h => {
    const retired = [], violations = [];
    for (let i = 1; i <= 100; i++) {
      const old = h.root.querySelector('.peer-activity');
      const id = `peer_churn_${i}`;
      h.state.peers = [peer(id)]; await h.refresh('peers');
      assert.equal(h.root.querySelectorAll('.peer-activity').length, 1);
      assert.notEqual(h.root.querySelector('.peer-activity'), old);
      retired.push(old);
      const before = retired.map(cell => h.chartWrites(cell));
      h.state.activity = snapshot({ [id]: [i + 2] }); await h.refresh('dashboard-activity');
      const updated = retired.filter((cell, j) => h.chartWrites(cell) !== before[j]);
      if (updated.length) violations.push({ cycle: i, retiredChartsUpdated: updated.length });
      assert.match(h.root.querySelector('.peer-activity').textContent, new RegExp(`${i + 2} messages`));
    }
    assert.deepEqual(violations, [], 'real SVG mutations expose updates to retired chart owners');
  });
});

test('CONTROL archived toggle preserves disabled peers, restores archived peers and error fallback', async () => {
  const live = peer('peer_a'), disabled = { ...peer('peer_disabled'), disabled: true };
  const archived = { ...peer('peer_archived'), archived_at: snapshot().now };
  await dashboard(async h => {
    const peers = h.root.querySelector('#d-peers'), toggle = peers.querySelector('input');
    assert.equal(peers.querySelectorAll('.peer-activity').length, 2);
    assert.match(peers.textContent, /peer_disabled/); assert.doesNotMatch(peers.textContent, /peer_archived/);
    toggle.on.change({ target: { checked: true } });
    assert.equal(peers.querySelectorAll('.peer-activity').length, 3);
    assert.match(peers.textContent, /peer_archived/);
    h.state.peerError = new Error('directory offline'); await h.tick();
    assert.equal(peers.querySelectorAll('.peer-activity').length, 3);
    toggle.on.change({ target: { checked: false } });
    assert.equal(peers.querySelectorAll('.peer-activity').length, 2);
    h.state.peerError = null; await h.tick();
    assert.match(peers.textContent, /peer_disabled/); assert.doesNotMatch(peers.textContent, /peer_archived/);
  }, { peers: [live, disabled, archived] });
});

test('REGRESSION charts hidden by archived toggle receive no subsequent activity updates', async () => {
  const archived = { ...peer('peer_archived'), archived_at: snapshot().now };
  await dashboard(async h => {
    const peers = h.root.querySelector('#d-peers'), toggle = peers.querySelector('input');
    toggle.on.change({ target: { checked: true } });
    const retired = peers.querySelectorAll('.peer-activity')[1];
    toggle.on.change({ target: { checked: false } });
    const before = h.chartWrites(retired);
    h.state.activity = snapshot({ peer_a: [8], peer_archived: [7] }); await h.refresh('dashboard-activity');
    assert.equal(h.chartWrites(retired), before, 'hidden archived chart is retired');
    toggle.on.change({ target: { checked: true } });
    assert.equal(peers.querySelectorAll('.peer-activity').length, 2);
    assert.match(peers.querySelectorAll('.peer-activity')[1].textContent, /7 messages/);
  }, { peers: [peer('peer_a'), archived] });
});
