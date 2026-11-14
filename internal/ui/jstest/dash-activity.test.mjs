import test from 'node:test';
import assert from 'node:assert/strict';
import { mount } from '../static/dash.js';
import { createStore } from '../static/store.js';
import { El, installDom, snapshot, peer } from './activity-dom-fixture.mjs';

async function dashboard(fn) {
  const restore = installDom(), interval = globalThis.setInterval, clear = globalThis.clearInterval;
  let timer, answer = snapshot(), error = null, calls = 0, nextTimer = 0;
  const timers = new Set();
  let peerError = null, peers = [peer('peer_a', '<img src=x>'), peer('peer_absent')];
  const api = { activityStats: async () => { calls++; if (error) throw error; return answer; }, counters: async () => ({}), adminPeers: async () => { if (peerError) throw peerError; return { peers }; }, principals: async () => ({ principals: [] }), threads: async () => ({ threads: [] }), deliveryHealth: async () => ({}), deadLetters: async () => ({}), loopCuts: async () => ({}), audit: async () => ({}), subscriptions: async () => [] };
  globalThis.setInterval = (fn, ms) => { if (ms === 30000) timer = fn; const id = ++nextTimer; timers.add(id); return id; };
  globalThis.clearInterval = id => { timers.delete(id); };
  const store = createStore({ api }), root = new El('main');
  const stop = mount(root, { api, store, nameOf: id => id });
  try { await fn({ root, store, tick: async () => { timer(); await store.settled(); }, answer: v => { answer = v; }, error: e => { error = e; }, peers: v => { peers = v; }, peerError: e => { peerError = e; }, calls: () => calls }); }
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

test('activity refresh retains exact keyboard selection and focus without minute disclosures', async () => {
  await dashboard(async ({ root, store, tick, answer }) => {
    await store.settled();
    const chart = root.querySelector('.activity-chart'), svg = chart.querySelector('svg');
    svg.focus(); svg.on.keydown({ key: 'Home', preventDefault() {} });
    store.invalidate('peers'); await store.settled();
    assert.equal(root.querySelector('.activity-chart'), chart);
    assert.equal(document.activeElement, svg);
    answer(snapshot({ peer_a: [8] })); await tick();
    assert.equal(root.querySelector('.activity-chart'), chart);
    assert.equal(document.activeElement, svg);
    assert.match(chart.querySelector('.spark-readout').textContent, /8 messages.*59 min ago/);
    for (const c of root.querySelectorAll('.sparkline')) {
      for (const tag of ['details', 'summary', 'table']) assert.equal(c.querySelectorAll(tag).length, 0);
    }
  });
});

test('overview labels the hourly average and keeps exact timestamps out of routine metadata', async () => {
  await dashboard(async ({ root, store }) => {
    await store.settled();
    assert.match(root.querySelector('.activity-overview').textContent, /Messages \/ min.*Hourly average/);
    assert.match(root.querySelector('.activity-active').parentNode.textContent, /Sent in last hour/);
    assert.doesNotMatch(root.querySelector('.activity-status').textContent, /2026-11-09T|Snapshot/);
    assert.match(root.querySelector('.activity-status').textContent, /sources and hub/);
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

function peerView(root, id) {
  const row = root.querySelector('#d-peers').querySelector('tbody').children.find(row => row.querySelector('.pn')?.getAttribute('href') === '#peer/' + id);
  return row && { row, svg: row.querySelector('svg'), read: row.querySelector('.spark-readout') };
}

test('removed peer charts stop updating and return fresh while a survivor keeps identity', async () => {
  await dashboard(async ({ root, store, peers, answer, tick }) => {
    await store.settled();
    const old = peerView(root, 'peer_a'), survivor = peerView(root, 'peer_absent');
    old.svg.on.keydown({ key: 'Home', preventDefault() {} });
    assert.equal(old.read.hidden, false);
    survivor.svg.focus();
    survivor.svg.on.keydown({ key: 'Home', preventDefault() {} });
    peers([peer('peer_absent')]); store.invalidate('peers'); await store.settled();
    assert.equal(peerView(root, 'peer_a'), undefined);
    const label = old.svg.getAttribute('aria-label'), points = old.svg.querySelector('.spark-line').getAttribute('points');
    answer({ ...snapshot({ peer_a: [17], peer_absent: [2] }), now: '2026-11-09T12:01:30.000Z', window_end: '2026-11-09T12:01:30.000Z', window_start: '2026-11-09T11:01:30.000Z' }); await tick();
    assert.equal(old.svg.getAttribute('aria-label'), label, 'removed SVG must not consume later activity');
    assert.equal(old.svg.querySelector('.spark-line').getAttribute('points'), points);
    assert.match(survivor.svg.getAttribute('aria-label'), /2 messages/);
    assert.match(survivor.read.textContent, /2 messages.*59 min ago/, 'survivor inspection follows the rolling window');
    assert.equal(root.querySelector('.activity-count').textContent, '19', 'removal must not drop historical activity');
    peers([peer('peer_a'), peer('peer_absent')]); store.invalidate('peers'); await store.settled();
    const fresh = peerView(root, 'peer_a');
    assert.notEqual(fresh.row, old.row, 'removed row ownership must be released');
    assert.notEqual(fresh.svg, old.svg, 'removed chart ownership must be released');
    assert.equal(fresh.read.hidden, true); assert.equal(fresh.read.textContent, '');
    assert.equal(fresh.row.querySelector('.spark-cursor').getAttribute('visibility'), 'hidden');
    assert.equal(peerView(root, 'peer_absent').row, survivor.row);
    assert.equal(peerView(root, 'peer_absent').svg, survivor.svg);
    assert.equal(document.activeElement, survivor.svg);
  });
});

test('bounded archived cohorts stop updating while hidden and each show gets uninspected views', async () => {
  await dashboard(async ({ root, store, peers, answer, tick, peerError }) => {
    await store.settled();
    const survivor = peerView(root, 'peer_absent'), retired = [];
    const toggle = root.querySelector('.show-archived').querySelector('input');
    const show = checked => toggle.on.change({ target: { checked } });
    for (let round = 0; round < 3; round++) {
      const ids = Array.from({ length: 3 }, (_, i) => `peer_cohort${round}${i}`);
      const cohort = ids.map(id => ({ ...peer(id), archived_at: '2026-11-09T10:00:00Z' }));
      peers([peer('peer_absent'), ...cohort]); store.invalidate('peers'); await store.settled();
      for (const id of ids) assert.equal(peerView(root, id), undefined);
      show(true);
      const old = ids.map(id => peerView(root, id));
      for (const view of old) view.svg.on.keydown({ key: 'Home', preventDefault() {} });
      survivor.svg.focus();
      // A failed directory refresh must keep the last successful visible cohort.
      peers([peer('peer_absent')]); peerError(new Error('directory failed')); await tick();
      for (let i = 0; i < ids.length; i++) {
        assert.equal(peerView(root, ids[i]).svg, old[i].svg);
        assert.equal(old[i].read.hidden, false, 'directory errors retain inspection');
      }
      assert.equal(document.activeElement, survivor.svg);
      peerError(null); peers([peer('peer_absent'), ...cohort]); await tick();
      show(false);
      for (const id of ids) assert.equal(peerView(root, id), undefined);
      retired.push(...old.map((view, i) => ({ id: ids[i], svg: view.svg, label: view.svg.getAttribute('aria-label'), points: view.svg.querySelector('.spark-line').getAttribute('points') })));
      answer(snapshot(Object.fromEntries([['peer_absent', [round + 2]], ...retired.map(view => [view.id, [20 + round]])])));
      await tick();
      for (const view of retired) {
        assert.equal(view.svg.getAttribute('aria-label'), view.label, 'every retired chart must stop consuming snapshots');
        assert.equal(view.svg.querySelector('.spark-line').getAttribute('points'), view.points);
      }
      show(true);
      for (let i = 0; i < ids.length; i++) {
        const fresh = peerView(root, ids[i]);
        assert.notEqual(fresh.row, old[i].row); assert.notEqual(fresh.svg, old[i].svg);
        assert.equal(fresh.read.hidden, true); assert.equal(fresh.read.textContent, '');
        assert.equal(fresh.row.querySelector('.spark-cursor').getAttribute('visibility'), 'hidden');
      }
      assert.equal(peerView(root, 'peer_absent').row, survivor.row);
      assert.equal(peerView(root, 'peer_absent').svg, survivor.svg);
      assert.equal(document.activeElement, survivor.svg);
      show(false);
    }
  });
});
