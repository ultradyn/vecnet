import test from 'node:test';
import assert from 'node:assert/strict';
import { mount } from '../static/dash.js';
import { createStore } from '../static/store.js';
import { El, installDom, snapshot, peer } from './activity-dom-fixture.mjs';

async function dashboard(fn, { peers = [peer('peer_a', '<img src=x>'), peer('peer_absent')], now } = {}) {
  const restore = installDom(), interval = globalThis.setInterval, clear = globalThis.clearInterval;
  let timer, answer = snapshot(), error = null, calls = 0, nextTimer = 0;
  const timers = new Set();
  let peerError = null;
  const api = { activityStats: async () => { calls++; if (error) throw error; return answer; }, counters: async () => ({}), adminPeers: async () => { if (peerError) throw peerError; return { peers }; }, principals: async () => ({ principals: [] }), threads: async () => ({ threads: [] }), deliveryHealth: async () => ({}), deadLetters: async () => ({}), loopCuts: async () => ({}), audit: async () => ({}), subscriptions: async () => [] };
  globalThis.setInterval = (fn, ms) => { if (ms === 30000) timer = fn; const id = ++nextTimer; timers.add(id); return id; };
  globalThis.clearInterval = id => { timers.delete(id); };
  const store = createStore({ api, now }), root = new El('main');
  const stop = mount(root, { api, store, nameOf: id => id });
  try { await fn({ root, store, tick: async () => { timer(); await store.settled(); }, answer: v => { answer = v; }, error: e => { error = e; }, peerError: e => { peerError = e; }, calls: () => calls }); }
  finally { stop(); restore(); globalThis.setInterval = interval; globalThis.clearInterval = clear; assert.equal(timers.size, 0, 'unmount clears every owned timer'); }
}

test('activity mounts unknown, then joins immutable ids with shared-scale zero-filled peers', async () => {
  await dashboard(async ({ root, store }) => {
    assert.match(root.querySelector('.dash-activity').textContent, /Loading activity/);
    assert.equal(root.querySelector('.activity-count').textContent, '—');
    await store.settled();
    assert.equal(root.querySelector('.activity-count').textContent, '4');
    assert.match(root.querySelector('.activity-rate').textContent, /0\.07/);
    assert.equal(root.querySelector('.activity-active').textContent, '1');
    const cells = root.querySelectorAll('.peer-activity');
    assert.match(cells[0].textContent, /4 messages/);
    assert.match(cells[1].textContent, /0 messages/);
    assert.equal(cells[0].querySelector('svg').attrs['data-max'], '3');
    assert.equal(cells[1].querySelector('svg').attrs['data-max'], '3');
    assert.equal(root.querySelectorAll('img').length, 0);
  });
});

test('activity refresh retains charts, open table, keyboard selection and focus across unrelated resources', async () => {
  await dashboard(async ({ root, store, tick, answer }) => {
    await store.settled();
    const chart = root.querySelector('.activity-chart'), svg = chart.querySelector('svg'), details = chart.querySelector('details');
    svg.focus(); svg.on.keydown({ key: 'Home', preventDefault() {} }); details.open = true;
    store.invalidate('peers'); await store.settled();
    assert.equal(root.querySelector('.activity-chart'), chart);
    assert.equal(document.activeElement, svg);
    answer(snapshot({ peer_a: [8] })); await tick();
    assert.equal(root.querySelector('.activity-chart'), chart);
    assert.equal(document.activeElement, svg);
    assert.equal(details.open, true);
    assert.match(chart.querySelector('.spark-readout').textContent, /8 messages/);
    assert.equal(chart.querySelectorAll('tbody')[0].children.length, 60);
  });
});

test('activity failure is isolated; stale values survive malformed data and recover on refresh', async () => {
  await dashboard(async ({ root, store, error, answer, tick, calls }) => {
    error(new Error('query failed')); await store.settled();
    assert.match(root.querySelector('.activity-status').textContent, /Activity unavailable.*query failed/);
    assert.equal(root.querySelector('.activity-count').textContent, '—');
    assert.match(root.querySelector('.peer-activity').textContent, /unavailable/i);
    assert.match(root.querySelector('#d-peers').textContent, /peer_absent/);
    error(null); await tick();
    assert.equal(root.querySelector('.activity-count').textContent, '4');
    answer({}); await tick();
    assert.equal(root.querySelector('.activity-count').textContent, '4');
    assert.match(root.querySelector('.activity-status').textContent, /Stale.*invalid/i);
    assert.match(root.querySelector('.peer-activity').textContent, /stale/i);
    answer(snapshot({})); await tick();
    assert.equal(root.querySelector('.activity-count').textContent, '0');
    assert.doesNotMatch(root.querySelector('.activity-status').textContent, /Stale|unavailable/);
    assert.ok(calls() >= 4);
  });
});

test('peer directory failure cannot blank activity or detach a focused peer trace', async () => {
  await dashboard(async ({ root, store, peerError, tick }) => {
    await store.settled();
    const cell = root.querySelector('.peer-activity'), svg = cell.querySelector('svg'); svg.focus();
    peerError(new Error('directory failed')); await tick();
    assert.equal(root.querySelector('.activity-count').textContent, '4');
    assert.match(root.querySelector('#d-peers').textContent, /Peers unavailable.*directory failed/);
    assert.equal(root.querySelector('.peer-activity'), cell);
    assert.equal(document.activeElement, svg);
    peerError(null); await tick();
    assert.equal(root.querySelector('.peer-activity'), cell);
    assert.equal(document.activeElement, svg);
  });
});

test('peer freshness ages advance through repeated directory failures without detaching the focused chart', async () => {
  let clock = Date.parse(snapshot().now);
  const p = peer('peer_a');
  p.presence_at = new Date(clock - 1000).toISOString();
  p.peer.last_seen = p.presence_at;
  await dashboard(async ({ root, store, peerError, answer, tick }) => {
    await store.settled();
    const cell = root.querySelector('.peer-activity'), svg = cell.querySelector('svg'); svg.focus();
    assert.match(root.querySelector('#d-peers').textContent, /1s ago/);
    peerError(new Error('directory failed'));
    for (const minutes of [1, 2]) {
      clock += 60000;
      const next = snapshot();
      for (const field of ['now', 'window_start', 'window_end'])
        next[field] = new Date(Date.parse(next[field]) + minutes * 60000).toISOString();
      answer(next); await tick();
      assert.match(root.querySelector('#d-peers').textContent, new RegExp(minutes + 'm ago'));
      assert.doesNotMatch(root.querySelector('#d-peers').textContent, /1s ago/);
      assert.equal(root.querySelector('.peer-activity'), cell);
      assert.equal(document.activeElement, svg);
    }
  }, { peers: [p], now: () => clock });
});
