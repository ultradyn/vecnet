import test from 'node:test';
import assert from 'node:assert/strict';
import { createStore } from '../static/store.js';

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

const turn = () => new Promise(resolve => setImmediate(resolve));
async function until(condition) {
  for (let i = 0; i < 1000 && !condition(); i++) await turn();
  assert.ok(condition(), 'expected lifecycle boundary was never reached');
}

// Observe only this real signal. Track native listener identity/capture and
// automatic once-removal, so abort cleanup need not call removeEventListener.
function observedController() {
  const controller = new AbortController();
  const { signal } = controller;
  const add = signal.addEventListener.bind(signal);
  const remove = signal.removeEventListener.bind(signal);
  const active = new Set();
  const captureOf = options => typeof options === 'boolean' ? options : !!options?.capture;
  signal.addEventListener = (type, listener, options) => {
    if (type !== 'abort' || listener == null) return add(type, listener, options);
    const capture = captureOf(options);
    const prior = [...active].find(r => r.listener === listener && r.capture === capture);
    if (prior) return add(type, prior.wrapped, options);
    const record = { listener, capture, wrapped: null };
    record.wrapped = event => {
      if (options?.once) active.delete(record);
      if (typeof listener === 'function') listener.call(signal, event);
      else listener.handleEvent(event);
    };
    active.add(record);
    return add(type, record.wrapped, options);
  };
  signal.removeEventListener = (type, listener, options) => {
    const record = type === 'abort' && [...active].find(r =>
      r.listener === listener && r.capture === captureOf(options));
    if (!record) return remove(type, listener, options);
    active.delete(record);
    return remove(type, record.wrapped, options);
  };
  return { controller, signal, listeners: () => active.size };
}

const error = status => Object.assign(new Error(`synthetic ${status}`), { status });
const batch = cursor => ({
  events: [{ type: 'presence', data: { peer_id: 'peer_test' } }],
  next_cursor: cursor,
});

function fixture(t, script) {
  const observed = observedController();
  t.after(() => observed.controller.abort());
  const polls = [], acks = [], sleeps = [], order = [];
  const api = {
    async events(args) {
      polls.push({ after: args.after, wait: args.wait, listeners: observed.listeners() });
      order.push(`poll:${args.after ?? 'first'}`);
      const step = script.length ? script.shift() : null;
      const value = typeof step === 'function' ? await step() : step;
      if (value instanceof Error) throw value;
      return value;
    },
    async ack(cursor) { acks.push(cursor); order.push(`ack:${cursor}`); },
  };
  const store = createStore({
    api, random: () => 0.5,
    sleep: async ms => { sleeps.push({ ms, listeners: observed.listeners() }); },
  });
  return { ...observed, store, polls, acks, sleeps, order,
    run: () => store.pump(observed.signal) };
}

test('completed batch waits release abort bookkeeping before each resumed poll', async t => {
  const cursors = Array.from({ length: 24 }, (_, i) => `e1.${i + 1}`);
  const h = fixture(t, cursors.map(batch));
  let loads = 0;
  h.store.resource('peers', async () => { h.order.push(`load:${++loads}`); return loads; });
  await h.run();
  assert.deepEqual(h.acks, cursors);
  assert.equal(loads, cursors.length);
  assert.deepEqual(h.order, ['poll:first', ...cursors.flatMap((cursor, i) =>
    [`load:${i + 1}`, `ack:${cursor}`, `poll:${cursor}`])]);
  assert.deepEqual(h.polls.slice(1).map(p => p.after), cursors);
  assert.ok(h.polls.slice(1).every(p => p.wait === 25));
  assert.deepEqual(h.polls.slice(1).map(p => p.listeners), cursors.map(() => 0),
    'completed waits must release abort listeners before polling again');
  assert.equal(h.signal.aborted, false);
  assert.equal(h.listeners(), 0);
});

for (const [label, terminal] of [['disabled/null', null], ['auth401', error(401)], ['auth403', error(403)]]) {
  for (const afterBatch of [false, true]) {
    test(`${label} termination releases abort bookkeeping ${afterBatch ? 'after a batch' : 'without a batch'}`, async t => {
      const h = fixture(t, [...(afterBatch ? [batch('e2.1')] : []), terminal]);
      let loads = 0;
      h.store.resource('peers', async () => ++loads);
      await h.run();
      assert.equal(h.signal.aborted, false, 'termination must not need a caller abort');
      assert.equal(h.store.conn(), 'stopped');
      assert.equal(h.polls.length, afterBatch ? 2 : 1);
      assert.equal(loads, afterBatch ? 1 : 0);
      assert.deepEqual(h.acks, afterBatch ? ['e2.1'] : []);
      assert.deepEqual(h.sleeps, []);
      assert.equal(h.listeners(), 0, 'normal termination must leave no abort listeners');
    });
  }
}

for (const status of [404, 503]) {
  test(`a settled ${status} resource error releases wait bookkeeping`, async t => {
    const h = fixture(t, [batch('e3.1'), null]);
    let loads = 0;
    const resource = h.store.resource('peers', async () => {
      if (++loads === 1) throw error(status);
      return 'recovered';
    });
    await h.run();
    await h.store.settled();
    assert.deepEqual(h.acks, ['e3.1']);
    if (status === 503) {
      assert.equal(loads, 2);
      assert.equal(resource.get(), 'recovered');
      assert.equal(resource.error(), null);
      assert.deepEqual(h.sleeps.map(s => s.ms), [1000]);
    } else {
      assert.equal(loads, 1);
      assert.equal(resource.error().status, 404);
      assert.deepEqual(h.sleeps, []);
    }
    assert.deepEqual(h.sleeps.map(s => s.listeners), h.sleeps.map(() => 0),
      'a completed failed load must release abort listeners before backoff');
    assert.equal(h.polls[1].listeners, 0, 'error settlement must clean up before polling');
    assert.equal(h.signal.aborted, false);
    assert.equal(h.listeners(), 0);
  });
}

test('a cursor reset releases its completed reload wait before resuming events', async t => {
  const purged = Object.assign(error(409), { details: { oldest_cursor: 'e4.0' } });
  const h = fixture(t, [purged, null]);
  let loads = 0;
  h.store.resource('peers', async () => ++loads);
  await h.run();
  assert.equal(loads, 1);
  assert.deepEqual(h.acks, []);
  assert.deepEqual(h.sleeps, []);
  assert.equal(h.polls[1].after, 'e4.0');
  assert.equal(h.polls[1].listeners, 0);
  assert.equal(h.signal.aborted, false);
  assert.equal(h.listeners(), 0);
});

test('a gated resource must finish before its batch is acked', { timeout: 5000 }, async t => {
  const gate = deferred();
  const h = fixture(t, [batch('e5.1'), null]);
  let started = false;
  const resource = h.store.resource('peers', async () => {
    started = true;
    await gate.promise;
    return 'shown';
  });
  const running = h.run();
  try {
    await until(() => started);
    await turn();
    assert.equal(resource.loading(), true);
    assert.deepEqual(h.acks, []);
    assert.equal(h.polls.length, 1);
    gate.resolve();
    await running;
    assert.equal(resource.get(), 'shown');
    assert.equal(resource.loading(), false);
    assert.deepEqual(h.acks, ['e5.1']);
    assert.equal(h.store.conn(), 'stopped');
  } finally {
    h.controller.abort();
    gate.resolve();
    await running;
    await h.store.settled();
  }
});

for (const reset of [false, true]) {
  test(`abort during a gated ${reset ? 'cursor-reset' : 'batch'} load stops without awaiting or acking it`,
    { timeout: 5000 }, async t => {
      const gate = deferred();
      const step = reset
        ? Object.assign(error(409), { details: { oldest_cursor: 'e6.0' } })
        : batch('e6.1');
      const h = fixture(t, [step]);
      let started = false, completed = false, stopped = false;
      const resource = h.store.resource('peers', async () => {
        started = true;
        await gate.promise;
        completed = true;
        return 'late snapshot';
      });
      const running = h.run().then(() => { stopped = true; });
      try {
        await until(() => started);
        await turn();
        assert.equal(resource.loading(), true);
        assert.deepEqual(h.acks, []);
        h.controller.abort();
        await until(() => stopped);
        assert.equal(completed, false, 'the pump must stop while the loader is still gated');
        assert.equal(h.store.conn(), 'stopped');
        assert.equal(h.listeners(), 0, 'abort must leave no listeners behind');
        assert.deepEqual(h.acks, []);
        assert.equal(h.polls.length, 1);
        gate.resolve();
        await h.store.settled();
        await turn();
        assert.equal(completed, true);
        assert.deepEqual(h.acks, [], 'a late load must not ack after abort');
        assert.equal(h.polls.length, 1);
        assert.equal(h.listeners(), 0);
      } finally {
        h.controller.abort();
        gate.resolve();
        await running;
        await h.store.settled();
      }
    });
}
