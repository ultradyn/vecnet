import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { APIError, createApi } from '../static/api.js';
import { createStore, tagsFor } from '../static/store.js';

const fixture = JSON.parse(await readFile(new URL('../../opapi/testdata/tags.json', import.meta.url), 'utf8'));
const api401 = () => new APIError(401, 'unauthenticated', 'no');

// A fake hub. `script` holds one entry per /v1/events call: a value is
// returned, an Error is thrown, a function is called. When the script runs out
// the pump is aborted, so a run ends without the test having to stop it.
function hub(script, { onAck = () => {} } = {}) {
  const calls = [], acks = [], sleeps = [];
  const controller = new AbortController();
  const api = {
    events: async args => {
      const { signal, ...query } = args;
      calls.push(query);
      if (!script.length) { controller.abort(); return { events: [], next_cursor: query.after }; }
      const step = script.shift();
      const value = typeof step === 'function' ? await step() : step;
      if (value instanceof Error) throw value;
      return value;
    },
    ack: async cursor => { await onAck(cursor); acks.push(cursor); return null; },
  };
  const store = createStore({ api, sleep: async ms => { sleeps.push(ms); } });
  return { api, store, calls, acks, sleeps, controller, run: () => store.pump(controller.signal) };
}

const delivery = thread => ({ type: 'delivery', data: { message: { thread_id: thread } } });
function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
const turn = () => new Promise(resolve => setImmediate(resolve));
// Counts event-loop turns, not time, and fails the test that was waiting: these
// loaders only ever need a few turns, and node --test has no default timeout.
async function until(cond) {
  for (let i = 0; i < 1000 && !cond(); i++) await turn();
  assert.ok(cond(), 'never happened: ' + cond);
}

test('tagsFor matches internal/opapi/testdata/tags.json row for row', () => {
  assert.ok(fixture.length >= 7);
  for (const row of fixture) assert.deepEqual(tagsFor(row.event), row.tags, row.event.type);
});

test('tagsFor mirrors opapi.Tags on malformed events: they invalidate nothing', () => {
  for (const event of [
    { type: 'reaction' },
    { type: 'reaction', data: {} },
    { type: 'reaction', data: { thread_id: 7 } },
    { type: 'reaction', data: { thread_id: '' } },
    { type: 'delivery' },
    { type: 'delivery', data: null },
    { type: 'delivery', data: [] },
    { type: 'delivery', data: 'thr_1' },
    { type: 'delivery', data: {} },
    { type: 'delivery', data: { message: 'thr_1' } },
    { type: 'delivery', data: { message: { thread_id: 7 } } },
    { type: 'receipt', data: { thread_id: 7 } },
    { type: 'receipt', data: {} },
    { type: 'thread_updated', data: { thread_id: '' } },
    { type: 'approval_expired', data: {} },
    { type: 'presence', data: {} },
    { type: 'presence', data: { peer_id: 7 } },
    { type: 'presence', data: { peer_id: 'peer_1', thread_id: 7 } },
    { type: 'directory_updated', data: { peer_id: '' } },
    { type: 'constructor', data: { thread_id: 'thr_1' } },
  ]) assert.deepEqual(tagsFor(event), [], JSON.stringify(event));
});

test('first events call has no after and wait=0; never sends e0.0', async () => {
  const { calls, run } = hub([
    { events: [], next_cursor: 'e3.4' },
    { events: [], next_cursor: 'e3.4' },
  ]);
  await run();
  assert.deepEqual(calls[0], { wait: 0 });
  assert.deepEqual(calls[1], { after: 'e3.4', wait: 25 });
  assert.deepEqual(calls[2], { after: 'e3.4', wait: 25 });
  assert.ok(calls.every(call => call.after !== 'e0.0'));
});

test('a first call that fails is retried without after', async () => {
  const { calls, run } = hub([new APIError(0, 'network', 'down'), { events: [], next_cursor: 'e3.4' }]);
  await run();
  assert.deepEqual(calls[0], { wait: 0 });
  assert.deepEqual(calls[1], { wait: 0 });
  assert.deepEqual(calls[2], { after: 'e3.4', wait: 25 });
});

test('batch: invalidates, awaits reload, then acks; ack never precedes settled', async () => {
  const order = [];
  const gate = deferred();
  const h = hub([{ events: [delivery('thr_1')], next_cursor: 'e1.1' }], { onAck: cursor => { order.push(`ack ${cursor}`); } });
  let loads = 0;
  const resource = h.store.resource('thread:thr_1', async () => {
    loads++;
    order.push('load');
    await gate.promise;
    order.push('loaded');
    return loads;
  });
  const running = h.run();
  // The load is parked on the gate: give the pump every chance to ack early.
  await until(() => order.includes('load'));
  for (let i = 0; i < 20; i++) await turn();
  assert.deepEqual(h.acks, []);
  assert.equal(resource.loading(), true);
  gate.resolve();
  await running;
  assert.deepEqual(order, ['load', 'loaded', 'ack e1.1']);
  assert.deepEqual(h.acks, ['e1.1']);
  assert.equal(resource.get(), 1);
});

test('a batch of unknown events still acks and reloads nothing', async () => {
  const h = hub([{ events: [{ type: 'future_event', data: {} }], next_cursor: 'e1.1' }]);
  let loads = 0;
  h.store.resource('inbox', async () => ++loads);
  await h.run();
  assert.equal(loads, 0);
  assert.deepEqual(h.acks, ['e1.1']);
});

test('a batch reloads only the tags it names', async () => {
  const h = hub([{ events: [delivery('thr_1')], next_cursor: 'e1.1' }]);
  const loads = { inbox: 0, threads: 0, one: 0, two: 0, peers: 0 };
  h.store.resource('inbox', async () => loads.inbox++);
  h.store.resource('threads', async () => loads.threads++);
  h.store.resource('thread:thr_1', async () => loads.one++);
  h.store.resource('thread:thr_2', async () => loads.two++);
  h.store.resource('peers', async () => loads.peers++);
  await h.run();
  assert.deepEqual(loads, { inbox: 1, threads: 1, one: 1, two: 0, peers: 0 });
});

test('a failed ack is retried before the next poll, and a batch is acked once', async () => {
  let fails = 1;
  const h = hub([{ events: [delivery('thr_1')], next_cursor: 'e1.1' }, { events: [], next_cursor: 'e1.1' }], {
    onAck: () => { if (fails-- > 0) throw new APIError(503, 'unavailable', 'later'); },
  });
  await h.run();
  assert.deepEqual(h.acks, ['e1.1']);
  assert.deepEqual(h.sleeps, [1000]);
  assert.equal(h.calls.length, 3);
});

test('an ack the hub refuses outright does not wedge the loop', async () => {
  const h = hub([{ events: [delivery('thr_1')], next_cursor: 'e1.1' }, { events: [], next_cursor: 'e1.1' }], {
    onAck: () => { throw new APIError(422, 'invalid_value', 'cursor is past the newest event'); },
  });
  await h.run();
  assert.deepEqual(h.sleeps, [1000]);
  assert.equal(h.calls.length, 3);
});

test('409 resumes at oldest_cursor and invalidates all', async () => {
  const purged = new APIError(409, 'cursor_purged', 'purged', { oldest_cursor: 'e2.9' });
  const h = hub([purged, { events: [], next_cursor: 'e2.9' }]);
  const loads = { inbox: 0, thread: 0 };
  h.store.resource('inbox', async () => loads.inbox++);
  h.store.resource('thread:thr_1', async () => loads.thread++);
  await h.run();
  assert.deepEqual(h.calls[0], { wait: 0 });
  assert.deepEqual(h.calls[1], { after: 'e2.9', wait: 25 });
  assert.deepEqual(loads, { inbox: 1, thread: 1 });
  assert.deepEqual(h.sleeps, []);
  assert.deepEqual(h.acks, []);
});

test('409 re-syncs before it resumes', async () => {
  const gate = deferred();
  const purged = new APIError(409, 'cursor_purged', 'purged', { oldest_cursor: 'e2.9' });
  const h = hub([purged]);
  let started = false;
  h.store.resource('inbox', async () => { started = true; await gate.promise; });
  const running = h.run();
  await until(() => started);
  for (let i = 0; i < 20; i++) await turn();
  assert.equal(h.calls.length, 1);
  gate.resolve();
  await running;
  assert.deepEqual(h.calls[1], { after: 'e2.9', wait: 25 });
});

test('a 409 with no oldest_cursor backs off instead of spinning', async () => {
  const h = hub([new APIError(409, 'conflict', 'no cursor named')]);
  await h.run();
  assert.deepEqual(h.sleeps, [1000]);
});

test('401 stops the loop; 403 and 204 stop with conn stopped; no retry', async () => {
  for (const step of [api401(), new APIError(403, 'forbidden', 'no'), null]) {
    const h = hub([step, { events: [], next_cursor: 'e1.1' }]);
    assert.equal(h.store.conn(), 'connecting');
    await h.run();
    assert.equal(h.calls.length, 1);
    assert.equal(h.store.conn(), 'stopped');
    assert.deepEqual(h.sleeps, []);
    assert.deepEqual(h.acks, []);
  }
});

test('a 200 with no readable body backs off and retries; only a real 204 stops', async () => {
  const lost = () => new Response(new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode('{"events":['));
      controller.error(new TypeError('terminated'));
    },
  }), { status: 200 });
  const replies = [
    () => new Response('<html>proxy</html>', { status: 200 }),
    () => new Response('', { status: 200 }),
    () => new Response('{"events":[{"cur', { status: 200 }),
    lost,
    () => new Response('null', { status: 200 }),
    () => new Response(JSON.stringify({ events: [], next_cursor: 'e1.1' }), { status: 200 }),
    () => new Response(null, { status: 204 }),
  ];
  const urls = [], sleeps = [], conns = [];
  const controller = new AbortController();
  const fetch = async url => {
    urls.push(url);
    if (!replies.length) { controller.abort(); throw new DOMException('stopped', 'AbortError'); }
    return replies.shift()();
  };
  const store = createStore({ api: createApi({ secret: 's', fetch }), sleep: async ms => { sleeps.push(ms); } });
  store.onConn(state => conns.push(state));
  await store.pump(controller.signal);
  assert.deepEqual(sleeps, [1000, 2000, 4000, 8000, 16000]);
  assert.equal(urls.length, 7);
  for (const url of urls.slice(0, 6)) assert.equal(url, '/v1/events?wait=0');
  assert.equal(urls[6], '/v1/events?after=e1.1&wait=25');
  assert.deepEqual(conns, ['offline', 'live', 'stopped']);
});

test('a poll that answers retries what failed, with no event to prompt it', async () => {
  const quiet = { events: [], next_cursor: 'e1.1' };
  for (const script of [
    [new APIError(503, 'unavailable', 'restarting'), quiet, quiet], // a hub restart over a page load
    [quiet, quiet],                                                  // back before the first poll
  ]) {
    const h = hub(script);
    let fail = true, loads = 0;
    const resource = h.store.resource('threads', async () => {
      loads++;
      if (fail) throw new Error('hub down');
      return loads;
    });
    await resource.reload();
    assert.equal(resource.error().message, 'hub down');
    fail = false;
    await h.run();
    await h.store.settled();
    assert.equal(resource.error(), null);
    assert.equal(resource.get(), 2);
    assert.equal(loads, 2);
  }
});

test('a poll reloads only what failed, and a resource that keeps failing costs one load a poll', async () => {
  const quiet = { events: [], next_cursor: 'e1.1' };
  // Each poll answers once the last one's retry has finished, as a real long-poll would.
  const poll = async () => { await h.store.settled(); return quiet; };
  const h = hub([poll, poll, poll]);
  const loads = { good: 0, bad: 0 };
  const good = h.store.resource('peers', async () => ++loads.good);
  const bad = h.store.resource('inbox', async () => { loads.bad++; throw new APIError(503, 'unavailable', 'hub down'); });
  await good.reload();
  await bad.reload();
  await h.run();
  await h.store.settled();
  assert.equal(loads.good, 1);
  assert.equal(loads.bad, 4);
  assert.deepEqual(h.sleeps, []);
});

test('a batch whose snapshot fails is not acked until the snapshot loads, then exactly once', async () => {
  const h = hub([{ events: [delivery('thr_1')], next_cursor: 'e1.5' }, { events: [], next_cursor: 'e1.5' }]);
  let loads = 0;
  const ackedAt = [];
  const history = h.store.resource('thread:thr_1', async () => {
    if (++loads === 1) throw new APIError(503, 'unavailable', 'snapshot unavailable');
    ackedAt.push(h.acks.length); // nothing acked while the snapshot is unshown
    return loads;
  });
  await h.run();
  await h.store.settled();
  assert.deepEqual(ackedAt, [0]);
  assert.deepEqual(h.acks, ['e1.5']);
  assert.equal(loads, 2);
  assert.equal(history.get(), 2);
  assert.equal(history.error(), null);
  assert.deepEqual(h.sleeps, [1000]);
});

test('a snapshot that keeps failing holds the cursor and backs off; the hub refusing it does not wedge', async () => {
  for (const [err, wantAcks] of [
    [new APIError(503, 'unavailable', 'down'), []],
    [new APIError(0, 'network', 'unreachable'), []],
    [new APIError(404, 'not_found', 'gone'), ['e1.5']],
  ]) {
    const h = hub([{ events: [delivery('thr_1')], next_cursor: 'e1.5' }, { events: [], next_cursor: 'e1.5' }]);
    let loads = 0;
    // Stop after a few retries: a retryable failure never acks. The fake sleep
    // does not yield, so the loader stops the run itself.
    h.store.resource('thread:thr_1', async () => { if (++loads >= 4) h.controller.abort(); throw err; });
    await h.run();
    assert.deepEqual(h.acks, wantAcks, err.message);
    if (!wantAcks.length) assert.ok(h.sleeps.length >= 3, 'backs off');
  }
});

test('5xx backs off 1,2,4…30 s with fake sleep; success resets', async () => {
  const down = () => new APIError(503, 'unavailable', 'down');
  const states = [];
  const h = hub([
    down(), () => (states.push(h.store.conn()), down()), down(), down(), down(), down(), down(),
    () => (states.push(h.store.conn()), { events: [], next_cursor: 'e1.1' }),
    () => (states.push(h.store.conn()), down()),
    new APIError(0, 'network', 'unreachable'),
    { events: [], next_cursor: 'e1.1' },
  ]);
  await h.run();
  assert.deepEqual(h.sleeps, [1000, 2000, 4000, 8000, 16000, 30000, 30000, 1000, 2000]);
  assert.deepEqual(states, ['offline', 'offline', 'live']);
});

test('conn goes connecting, live, offline, live, and the watcher hears each change', async () => {
  const h = hub([{ events: [], next_cursor: 'e1.1' }, new APIError(0, 'network', 'down'), { events: [], next_cursor: 'e1.1' }]);
  const seen = [];
  const off = h.store.onConn(state => seen.push(state));
  await h.run();
  off();
  assert.deepEqual(seen, ['live', 'offline', 'live', 'stopped']);
});

test('aborting stops the loop, parks nothing and does not ack', async () => {
  const gate = deferred();
  const h = hub([{ events: [delivery('thr_1')], next_cursor: 'e1.1' }]);
  h.store.resource('inbox', async () => { await gate.promise; });
  const running = h.run();
  await until(() => h.calls.length > 0);
  for (let i = 0; i < 5; i++) await turn();
  h.controller.abort();
  await running;
  assert.deepEqual(h.acks, []);
  assert.equal(h.store.conn(), 'stopped');
  gate.resolve();
});

test('two invalidates in one tick call the loader once; failed load keeps last value and sets error', async () => {
  const store = createStore({ api: {} });
  let loads = 0, fail = false;
  const resource = store.resource('inbox', async () => {
    loads++;
    if (fail) throw new Error('hub down');
    return { n: loads };
  });
  store.invalidate('inbox');
  store.invalidate('inbox', 'inbox');
  await store.settled();
  assert.equal(loads, 1);
  assert.deepEqual(resource.get(), { n: 1 });
  assert.equal(resource.error(), null);

  fail = true;
  store.invalidate('inbox');
  await store.settled();
  assert.equal(loads, 2);
  assert.deepEqual(resource.get(), { n: 1 });
  assert.equal(resource.error().message, 'hub down');

  fail = false;
  await resource.reload();
  assert.deepEqual(resource.get(), { n: 3 });
  assert.equal(resource.error(), null);
});

test('a loader that throws before it returns a promise is an ordinary failed load', async () => {
  const store = createStore({ api: {} });
  let fail = true;
  const resource = store.resource('inbox', () => {
    if (fail) throw new Error('bad query');
    return Promise.resolve('ok');
  });
  await resource.reload();
  assert.equal(resource.error().message, 'bad query');
  assert.equal(resource.loading(), false);
  fail = false;
  await resource.reload();
  assert.equal(resource.get(), 'ok');
  assert.equal(resource.loading(), false);
});

test('an invalidate during a load reloads once more, with no overlapping loads', async () => {
  const store = createStore({ api: {} });
  const gates = [deferred(), deferred()];
  let loads = 0, active = 0, most = 0;
  const resource = store.resource('peers', async () => {
    const gate = gates[loads++];
    most = Math.max(most, ++active);
    await gate.promise;
    active--;
    return loads;
  });
  store.invalidate('peers');
  await until(() => loads > 0);
  store.invalidate('peers');
  store.invalidate('peers');
  let settled = false;
  const done = store.settled().then(() => { settled = true; });
  gates[0].resolve();
  await until(() => loads >= 2);
  assert.equal(settled, false);
  gates[1].resolve();
  await done;
  assert.equal(loads, 2);
  assert.equal(most, 1);
  assert.equal(resource.get(), 2);
  assert.equal(resource.loading(), false);
});

test('invalidateAll reloads every resource; settled resolves at once when idle', async () => {
  const store = createStore({ api: {} });
  await store.settled();
  const loads = [];
  store.resource('a', async () => loads.push('a'));
  store.resource('b', async () => loads.push('b'));
  store.resource('thread:1', async () => loads.push('c'));
  store.invalidateAll();
  await store.settled();
  assert.deepEqual(loads.sort(), ['a', 'b', 'c']);
});

test('two resources can share a tag', async () => {
  const store = createStore({ api: {} });
  const seen = [];
  const a = store.resource('thread:1', async () => seen.push('messages'));
  const b = store.resource('thread:1', async () => seen.push('matrix'));
  store.invalidate('thread:1');
  await store.settled();
  assert.deepEqual(seen.sort(), ['matrix', 'messages']);
  assert.ok(a.get() && b.get());
});

test('the first subscriber loads; subscribers hear changes; a thrown subscriber harms nobody', async () => {
  const store = createStore({ api: {} });
  const originalError = console.error;
  console.error = () => {};
  try {
    let loads = 0, heard = 0;
    const resource = store.resource('inbox', async () => ++loads);
    assert.equal(resource.get(), undefined);
    assert.equal(resource.loading(), false);
    const noisy = resource.subscribe(() => { throw new Error('render bug'); });
    const off = resource.subscribe(() => { heard++; });
    assert.equal(resource.loading(), true);
    await store.settled();
    assert.equal(loads, 1);
    assert.equal(resource.get(), 1);
    assert.equal(resource.loading(), false);
    assert.ok(heard >= 1);
    const before = heard;
    off();
    noisy();
    await resource.reload();
    assert.equal(heard, before);
  } finally { console.error = originalError; }
});

test('a resource whose last subscriber left stops loading, and reloads when it returns', async () => {
  const store = createStore({ api: {} });
  let loads = 0;
  const resource = store.resource('inbox', async () => ++loads);
  const off = resource.subscribe(() => {});
  await store.settled();
  off();
  store.invalidate('inbox');
  store.invalidateAll();
  await store.settled();
  assert.equal(loads, 1);
  const again = resource.subscribe(() => {});
  await store.settled();
  assert.equal(loads, 2);
  again();
});

test('a load that ends after the last subscriber left does not make the resource current', async () => {
  const store = createStore({ api: {} });
  const gate = deferred();
  let loads = 0, version = 1;
  const resource = store.resource('inbox', async () => {
    const seen = version;
    if (++loads === 1) await gate.promise;
    return seen;
  });
  const off = resource.subscribe(() => {});
  await until(() => loads === 1);
  off();
  version = 2;
  store.invalidate('inbox');
  gate.resolve();
  await store.settled();
  const again = resource.subscribe(() => {});
  await store.settled();
  assert.equal(loads, 2);
  assert.equal(resource.get(), 2);
  again();
});

test('a resource loaded before its first subscriber is not loaded again for it', async () => {
  const store = createStore({ api: {} });
  let loads = 0;
  const resource = store.resource('inbox', async () => ++loads);
  await resource.reload();
  const off = resource.subscribe(() => {});
  await store.settled();
  assert.equal(loads, 1);
  off();
});

test('hubNow applies the setHubNow offset with a client clock 5 min off', () => {
  const hubTime = Date.parse('2026-11-02T10:00:00.000Z');
  let client = hubTime - 5 * 60_000;
  const store = createStore({ api: {}, now: () => client });
  assert.equal(store.hubNow(), client);
  store.setHubNow('2026-11-02T10:00:00.000Z');
  assert.equal(store.hubNow(), hubTime);
  client += 12_345;
  assert.equal(store.hubNow(), hubTime + 12_345);
  store.setHubNow('not a time');
  assert.equal(store.hubNow(), hubTime + 12_345);
});

test('a loaded snapshot that carries now moves the hub clock', async () => {
  const client = Date.parse('2026-11-02T10:05:00.000Z');
  const store = createStore({ api: {}, now: () => client });
  const resource = store.resource('inbox', async () => ({ now: '2026-11-02T10:00:00.000Z' }));
  await resource.reload();
  assert.equal(store.hubNow(), Date.parse('2026-11-02T10:00:00.000Z'));
});

test('tagsFor operator_view mirrors opapi.Tags', () => {
  for (const [data, want] of [
    [{ reason: 'read', thread_id: 'thr_x' }, ['inbox', 'threads', 'peers', 'thread:thr_x']],
    [{ reason: 'presence', thread_id: null }, ['inbox', 'threads', 'peers']],
    [{ reason: 'sent', thread_id: '' }, ['inbox', 'threads', 'peers']],
    [{ reason: 7, thread_id: 'thr_x' }, []],
    [{ reason: null, thread_id: null }, []],
    [{ reason: null, Reason: 'read', thread_id: null }, []],
    [{ Reason: 'read', thread_id: null }, []],
    [{ reason: 'read', THREAD_ID: null }, []],
    [{ thread_id: null }, []],
    [{ reason: 'read' }, []],
    [{ reason: 'read', thread_id: 7 }, []],
    ['x', []],
  ]) assert.deepEqual(tagsFor({ type: 'operator_view', data }), want, JSON.stringify(data));
});

test('set replaces a loaded value, clears its error and notifies', async () => {
  const store = createStore({ api: {} });
  let fail = true;
  const res = store.resource('threads', async () => { if (fail) throw new Error('down'); return { n: 1 }; });
  let seen = 0;
  res.subscribe(() => { seen++; });
  await store.settled();
  assert.ok(res.error());
  const before = seen;
  res.set({ n: 2 });
  assert.deepEqual(res.get(), { n: 2 });
  assert.equal(res.error(), null);
  assert.equal(seen, before + 1);
  fail = false;
  await res.reload();
  assert.deepEqual(res.get(), { n: 1 });
});

test('tagsFor(hub_notice) is ["notices"]; malformed data invalidates nothing', () => {
  const notice = {
    notice_id: 'ntc_01JD3Q5H0X2Z4B6D8F0H2K4M6P', kind: 'maintenance', message: 'hello', hub: 'https://hub.example',
    issued_at: '2026-11-05T07:00:00.000Z', expires_at: '2026-11-06T07:00:00.000Z', sig: 'A'.repeat(86),
  };
  const moving = { ...notice, kind: 'moving', new_url: 'https://vecnet.xk.io', at: '2026-11-05T09:00:00.000Z' };
  for (const data of [
    { change: 'posted', notice },
    { change: 'posted', notice: moving },
    { change: 'cleared', notice: { ...notice, cleared_at: '2026-11-05T08:00:00.000Z', cleared_how: 'operator' } },
  ]) assert.deepEqual(tagsFor({ type: 'hub_notice', data }), ['notices'], JSON.stringify(data));
  const bad = [
    undefined, null, [], 'x', {}, { change: 'posted' }, { change: 'posted', notice: null },
    { change: 'posted', notice: {} }, { change: 'unknown', notice }, { change: ['posted'], notice },
    { change: 'posted', notice, extra: 1 }, { change: 'cleared', notice: { notice_id: 'ntc_test' } },
    { change: 'posted', notice: { notice_id: 'ntc_test', kind: 7, message: false } },
    { change: 'posted', notice: { ...notice, kind: 'party' } },
    { change: 'posted', notice: { ...notice, new_url: 'https://vecnet.xk.io' } }, // only moving carries new_url
    { change: 'posted', notice: { ...moving, new_url: undefined } },
    { change: 'posted', notice: { ...notice, new_url: 'javascript:alert(1)', kind: 'moving' } },
    { change: 'posted', notice: { ...notice, at: '2026-11-05' } },
    { change: 'posted', notice: { ...notice, cleared_how: 'whim' } },
    { change: 'posted', notice: { ...notice, extra: 'x' } },
  ];
  for (const field of Object.keys(notice)) {
    const without = { ...notice };
    delete without[field];
    bad.push({ change: 'posted', notice: without }, { change: 'posted', notice: { ...notice, [field]: 7 } });
  }
  for (const data of bad) assert.deepEqual(tagsFor({ type: 'hub_notice', data }), [], JSON.stringify(data));
});

test('the pump passes on a long-poll hub_restarting hint, and null for each plain answer', async () => {
  const h = hub([
    { events: [], next_cursor: 'e1.1' },
    { events: [], next_cursor: 'e1.1', hub_restarting: { retry_after_ms: 5000 } },
    { events: [], next_cursor: 'e1.1', hub_restarting: { retry_after_ms: 'soon' } },
    { events: [], next_cursor: 'e1.1' },
  ]);
  const seen = [];
  const off = h.store.onRestart(ms => seen.push(ms));
  await h.run();
  off();
  assert.deepEqual(seen.slice(0, 4), [null, 5000, null, null]);
});
