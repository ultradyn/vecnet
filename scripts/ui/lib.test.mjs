import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { setImmediate as nextTurn } from 'node:timers/promises';
import { chromium, networkQuiet, scrollMessageIntoView } from './lib.mjs';
import { paneText } from './tmux.mjs';

test('tmux pane matching accepts tab-compressed status spacing', () => {
  // capture-pane can return live\tapr even though the rendered cells are spaces.
  for (const output of [' local  live\tapr 2 ', ' local  live  apr 2 ', ' local \x1b[32m live\x1b[0m\tapr 2 ']) {
    assert.ok(paneText(output).includes(' live '), 'a visibly live pane must satisfy the live-status wait');
    assert.ok(paneText(output).includes('apr 2'));
  }
});

test('tmux pane matching preserves status boundaries and line breaks', () => {
  for (const status of ['reconnecting', 'offline', 'lively']) {
    assert.equal(paneText(` local \x1b[31m ${status} \x1b[0m apr 2\nnext line`).includes(' live '), false);
  }
  assert.equal(paneText('\x1b[31m offline \x1b[0m\nnext line'), ' offline \nnext line');
});

test('message scroll reaches the viewport while live updates replace message nodes', async () => {
  const browser = await (await chromium()).launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    page.setDefaultTimeout(3000);
    await page.setContent('<div style="height:2000px"></div><article id="m-target" style="height:300px">message</article>');
    // Thread rendering replaces .msg nodes on resource notifications. Replace
    // at every animation frame so the detached-node race is deterministic.
    await page.evaluate(() => {
      window.rebuilds = 0;
      const rebuild = () => {
        const message = document.getElementById('m-target');
        message.replaceWith(message.cloneNode(true));
        window.rebuilds++;
        if (window.rebuilds < 24) requestAnimationFrame(rebuild);
      };
      requestAnimationFrame(rebuild);
    });
    await page.waitForFunction(() => window.rebuilds >= 2);
    await scrollMessageIntoView(page, 'target');
    assert.ok(await page.evaluate(() => {
      const r = document.getElementById('m-target').getBoundingClientRect();
      return r.width > 0 && r.height > 0 && r.top < innerHeight && r.bottom > 0;
    }), 'the current message node is outside the viewport');
  } finally {
    await browser.close();
  }
});

test('quiet waits for a slow snapshot and the request its render starts, not for streams or held requests', async () => {
  const browser = await (await chromium()).launch({ headless: true });
  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    const net = networkQuiet(page);
    let releaseHeld;
    const held = new Promise(resolve => { releaseHeld = resolve; });
    await page.route('http://vn.test/**', async route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/v1/admin/activity' || path === '/held') { await held; return route.fulfill({ body: '' }); }
      await new Promise(resolve => setTimeout(resolve, path === '/snapshot' ? 400 : 200));
      await route.fulfill({ body: path, headers: { 'Access-Control-Allow-Origin': '*' } });
    });
    await page.goto('http://vn.test/');
    const unignore = net.ignore(req => req.url().endsWith('/held'));
    await page.evaluate(() => {
      fetch('/v1/admin/activity');
      fetch('/v1/events?wait=25');
      fetch('/held');
      // The snapshot's render asks for one more resource, as a thread's first
      // draw asks for its aside.
      fetch('/snapshot').then(r => r.text()).then(t => {
        document.body.textContent = t;
        return fetch('/avatar').then(r => r.text()).then(a => { document.body.textContent += ' ' + a; });
      });
    });
    await net.quiet({ timeout: 5000 });
    assert.equal(await page.evaluate(() => document.body.textContent), '/snapshot /avatar');
    unignore();
    await assert.rejects(net.quiet({ timeout: 600 }), /never went quiet: GET \/held/);
    releaseHeld();
    await net.quiet({ timeout: 5000 });
  } finally {
    await browser.close();
  }
});

// Finite Page seam: frame identity survives reload; timeOrigin changes only
// on a full commit. Missing terminal events reproduce the captured orphan.
function quietPage() {
  const page = new EventEmitter();
  const observations = [];
  let url = 'http://vn.test/ui/', timeOrigin = 1;
  const main = { url: () => url, parentFrame: () => null, evaluate: async () => observations.length ? observations.shift() : timeOrigin };
  const child = { url: () => 'http://vn.test/frame', parentFrame: () => main };
  page.mainFrame = () => main;
  page.url = () => url;
  // A browser evaluation turn can reveal work started by rendering.
  page.evaluate = async () => { page.onFrames?.(); return timeOrigin; };
  const request = (path, { frame = main, navigation = false, unavailableFrame = false } = {}) => {
    const req = {
      url: () => new URL(path, 'http://vn.test').href, method: () => 'GET',
      frame: () => { if (unavailableFrame) throw new Error('Frame is not available yet'); return frame; },
      isNavigationRequest: () => navigation,
      resourceType: () => navigation ? 'document' : 'fetch',
    };
    page.emit('request', req);
    return req;
  };
  const commit = async ({ frame = main, full = false, hash = '' } = {}) => {
    if (frame === main) { url = 'http://vn.test/ui/' + hash; if (full) timeOrigin++; }
    page.emit('framenavigated', frame);
    await nextTurn(); // Allow asynchronous document-identity observations.
  };
  const deferObservation = () => {
    let resolve, reject;
    observations.push(new Promise((yes, no) => { resolve = yes; reject = no; }));
    return { resolve, reject };
  };
  return { page, main, child, request, commit, deferObservation };
}

test('quiet retires reload orphans on full main commit but waits for navigation, new snapshots and render followups', async () => {
  const { page, request, commit } = quietPage();
  const net = networkQuiet(page);
  const old = request('/v1/peers');
  const navigation = request('/ui/', { navigation: true });
  const orphan = request('/v1/events?wait=0'); // Arrives after reload starts, before commit.
  assert.deepEqual(net.busy(), [old, navigation, orphan], 'starting navigation must not retire requests');
  await commit({ full: true }); // Same URL and Frame, different document; no orphan terminal.
  assert.deepEqual(net.busy(), [navigation], 'commit retires old fetches but retains the navigation response');
  await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/ui\//);
  const snapshot = request('/v1/events?wait=0');
  request('/v1/events?wait=25&after=current');
  page.emit('requestfinished', navigation);
  page.emit('requestfailed', old); // A late old terminal must not affect the new snapshot.
  assert.deepEqual(net.busy(), [snapshot]);
  await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/v1\/events/);
  page.emit('requestfinished', snapshot);
  let followup, resolved = false;
  page.onFrames = () => { if (!followup) followup = request('/avatar'); };
  const ready = net.quiet({ idleMs: 0, timeout: 1000 });
  ready.then(() => { resolved = true; }, () => {});
  try {
    await nextTurn();
    assert.deepEqual(net.busy(), [followup]);
    assert.equal(resolved, false, 'render-started request must delay quiet');
  } finally {
    if (followup) page.emit('requestfinished', followup);
    await ready;
  }
  assert.deepEqual(net.busy(), []);
});

test('quiet keeps current bootstrap pending across hash navigation and a failed full-navigation attempt', async () => {
  const { page, request, commit } = quietPage();
  const net = networkQuiet(page);
  const snapshot = request('/v1/events?wait=0');
  await commit({ hash: '#new' }); // Same document, despite framenavigated.
  assert.deepEqual(net.busy(), [snapshot]);
  await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/v1\/events/);
  const failedNavigation = request('/ui/', { navigation: true });
  page.emit('requestfailed', failedNavigation);
  await commit({ hash: '#dash' }); // Failed attempt must not turn this into a full commit.
  assert.deepEqual(net.busy(), [snapshot]);
  await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/v1\/events/);
  page.emit('requestfinished', snapshot);
  await net.quiet({ idleMs: 0, timeout: 1000 });
});

test('quiet keeps current bootstrap pending across subframe document navigation', async () => {
  const { page, child, request, commit } = quietPage();
  const net = networkQuiet(page);
  const snapshot = request('/v1/events?wait=0');
  const navigation = request('/frame', { frame: child, navigation: true });
  await commit({ frame: child, full: true });
  assert.deepEqual(net.busy(), [snapshot, navigation]);
  page.emit('requestfinished', navigation);
  assert.deepEqual(net.busy(), [snapshot]);
  await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/v1\/events/);
  page.emit('requestfinished', snapshot);
  await net.quiet({ idleMs: 0, timeout: 1000 });
});

test('quiet tracks navigation with an unavailable frame until its own terminal without losing the current snapshot', async () => {
  for (const terminal of ['requestfinished', 'requestfailed']) {
    const { page, request, commit } = quietPage();
    const net = networkQuiet(page);
    let ambiguous;
    assert.doesNotThrow(() => { ambiguous = request('/early-frame', { navigation: true, unavailableFrame: true }); });
    assert.deepEqual(net.busy(), [ambiguous]);
    await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/early-frame/);
    const navigation = request('/ui/', { navigation: true });
    await commit({ full: true });
    assert.deepEqual(net.busy(), [ambiguous, navigation], 'unknown frame ownership cannot authorize retirement');
    page.emit('requestfinished', navigation);
    const snapshot = request('/v1/events?wait=0');
    page.emit(terminal, ambiguous);
    assert.deepEqual(net.busy(), [snapshot], 'the ambiguous request terminal releases only that request');
    await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/v1\/events/);
    page.emit('requestfinished', snapshot);
    await net.quiet({ idleMs: 0, timeout: 1000 });
  }
});

test('quiet waits for delayed identity and retains current work when overlapping observations settle out of order', async () => {
  for (const duringFrames of [false, true]) {
    const { page, commit, deferObservation } = quietPage();
    const net = networkQuiet(page);
    await nextTurn();
    const identity = deferObservation();
    if (duringFrames) page.onFrames = () => { page.onFrames = null; void commit({ full: true }); };
    else await commit({ full: true });
    let resolved = false;
    const ready = net.quiet({ idleMs: 0, timeout: 1000 });
    ready.then(() => { resolved = true; }, () => {});
    try {
      await nextTurn();
      assert.deepEqual(net.busy(), []);
      assert.equal(resolved, false, 'unresolved identity must block quiet even with no open requests');
    } finally {
      identity.resolve(2);
      await ready;
    }
  }

  const { page, request, commit, deferObservation } = quietPage();
  const net = networkQuiet(page);
  await nextTurn();
  request('/v1/events?wait=0'); // Old document orphan.
  const first = deferObservation();
  await commit({ full: true });
  request('/v1/peers'); // Starts after the first candidate capture.
  const second = deferObservation();
  await commit({ full: true });
  const current = request('/v1/events?wait=0'); // Starts after both captures.
  try {
    second.resolve(3);
    await nextTurn();
    assert.ok(net.busy().includes(current), 'a later identity result must retain the current snapshot');
    first.resolve(2);
    await nextTurn();
    assert.deepEqual(net.busy(), [current], 'delayed retirement must affect only the captured old requests');
    await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/v1\/events/);
  } finally {
    first.resolve(2);
    second.resolve(3);
    page.emit('requestfinished', current);
  }
  await net.quiet({ idleMs: 0, timeout: 1000 });
});

test('quiet retains an orphan when document identity rejects or is nonfinite', async () => {
  for (const result of [new Error('document unavailable'), NaN, Infinity, undefined]) {
    const { page, request, commit, deferObservation } = quietPage();
    const net = networkQuiet(page);
    await nextTurn();
    const orphan = request('/v1/events?wait=0');
    const identity = deferObservation();
    await commit({ full: true });
    if (result instanceof Error) identity.reject(result);
    else identity.resolve(result);
    await nextTurn();
    assert.deepEqual(net.busy(), [orphan]);
    await assert.rejects(net.quiet({ idleMs: 0, timeout: 60 }), /never went quiet: GET \/v1\/events/);
    page.emit('requestfinished', orphan);
    await net.quiet({ idleMs: 0, timeout: 1000 });
  }
});
