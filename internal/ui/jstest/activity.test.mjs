import test from 'node:test';
import assert from 'node:assert/strict';
import { activityAction, applyActivity, createSSEParser } from '../static/activity-model.js';

const row = (id, at, count = 1) => ({ thread: { thread_id: id, title: null, participants: ['peer_a'], message_count: count }, last_message_at: at, cut: null });
const msg = (id, at) => ({ type: 'message', data: { thread_id: id, message_id: 'msg_' + at, sender: 'peer_a', kind: 'message', at } });

test('createSSEParser splits frames across any chunking and skips heartbeats', () => {
  const frames = [];
  const feed = createSSEParser(f => frames.push(f));
  const text = 'retry: 3000\n\nevent: ready\ndata: {}\n\n: hb\n\nevent: message\r\ndata: {"thread_id":"thr_1"}\r\n\r\nevent: resync\ndata: {}\n\n';
  for (const ch of text) feed(ch);
  assert.deepEqual(frames, [
    { event: 'ready', data: '{}' },
    { event: 'message', data: '{"thread_id":"thr_1"}' },
    { event: 'resync', data: '{}' },
  ]);
  const whole = [];
  createSSEParser(f => whole.push(f))(text);
  assert.deepEqual(whole, frames);
});

test('createSSEParser joins multi-line data and defaults the event name', () => {
  const frames = [];
  createSSEParser(f => frames.push(f))('data: a\ndata: b\n\n');
  assert.deepEqual(frames, [{ event: 'message', data: 'a\nb' }]);
});

test('applyActivity bumps the row, moves it to the top and keeps the rest in order', () => {
  const page = { threads: [row('thr_c', '2026-11-04T10:03:00.000Z'), row('thr_b', '2026-11-04T10:02:00.000Z'), row('thr_a', '2026-11-04T10:01:00.000Z', 7)], next: null };
  const r = applyActivity(page, msg('thr_a', '2026-11-04T10:05:00.000Z'));
  assert.equal(r.known, true);
  assert.equal(r.changed, true);
  assert.deepEqual(r.page.threads.map(t => t.thread.thread_id), ['thr_a', 'thr_c', 'thr_b']);
  assert.equal(r.page.threads[0].thread.message_count, 8);
  assert.equal(r.page.threads[0].last_message_at, '2026-11-04T10:05:00.000Z');
  assert.equal(r.page.next, null);
  // The input page is untouched.
  assert.equal(page.threads[2].thread.message_count, 7);
  assert.deepEqual(page.threads.map(t => t.thread.thread_id), ['thr_c', 'thr_b', 'thr_a']);
});

test('applyActivity: unknown thread or a rename asks for a refetch', () => {
  const page = { threads: [row('thr_a', '2026-11-04T10:01:00.000Z')] };
  assert.deepEqual(applyActivity(page, msg('thr_new', '2026-11-04T10:05:00.000Z')), { page, known: false, changed: false });
  assert.equal(applyActivity(page, { type: 'thread', data: { thread_id: 'thr_a', at: '2026-11-04T10:05:00.000Z' } }).known, false);
  assert.equal(applyActivity(undefined, msg('thr_a', '2026-11-04T10:05:00.000Z')).known, false);
});

test('applyActivity skips an entry a refetch already shows', () => {
  const page = { threads: [row('thr_a', '2026-11-04T10:05:00.000Z', 3)] };
  const r = applyActivity(page, msg('thr_a', '2026-11-04T10:05:00.000Z'));
  assert.equal(r.known, true);
  assert.equal(r.changed, false);
  assert.equal(r.page, page);
});

test('activityAction: refetch on connect and resync, update a listed row, skip what another view owns', () => {
  const page = { threads: [row('thr_b', '2026-11-04T10:02:00.000Z'), row('thr_a', '2026-11-04T10:01:00.000Z')] };
  assert.deepEqual(activityAction(page, { type: 'ready', data: {} }), { reload: true });
  assert.deepEqual(activityAction(page, { type: 'resync', data: {} }), { reload: true });
  const moved = activityAction(page, msg('thr_a', '2026-11-04T10:09:00.000Z'));
  assert.deepEqual(moved.page.threads.map(t => t.thread.thread_id), ['thr_a', 'thr_b']);
  assert.deepEqual(activityAction(page, msg('thr_new', '2026-11-04T10:09:00.000Z')), { reload: true });
  assert.equal(activityAction(page, msg('thr_mine', '2026-11-04T10:09:00.000Z'), new Set(['thr_mine'])), null);
  assert.equal(activityAction(page, msg('thr_a', '2026-11-04T10:01:00.000Z')), null);
  // A rename re-reads a listed thread and ignores one the list does not show.
  assert.deepEqual(activityAction(page, { type: 'thread', data: { thread_id: 'thr_b', at: 'x' } }), { reload: true });
  assert.equal(activityAction(page, { type: 'thread', data: { thread_id: 'thr_zz', at: 'x' } }), null);
  assert.equal(activityAction(page, { type: 'future', data: { thread_id: 'thr_a' } }), null);
});

test('createActivity emits each frame, reopens after the stream ends, and stops on 403', async () => {
  const { createActivity } = await import('../static/activity.js');
  const stop = new AbortController();
  let calls = 0;
  const api = {
    activity: async () => {
      calls++;
      if (calls === 1) return new Response('event: ready\ndata: {}\n\n: hb\n\nevent: message\ndata: {"thread_id":"thr_1","at":"t"}\n\nevent: bad\ndata: {nope\n\n');
      if (calls === 2) return new Response('event: ready\ndata: {}\n\nevent: resync\ndata: {}\n\n');
      throw Object.assign(new Error('forbidden'), { status: 403 });
    },
  };
  const sleeps = [];
  const feed = createActivity({ api, signal: stop.signal, random: () => 0.5, sleep: async ms => { sleeps.push(ms); } });
  const seen = [];
  feed.subscribe(ev => seen.push(ev));
  await feed.start();
  assert.equal(calls, 3);
  assert.deepEqual(seen, [
    { type: 'ready', data: {} },
    { type: 'message', data: { thread_id: 'thr_1', at: 't' } },
    { type: 'ready', data: {} },
    { type: 'resync', data: {} },
  ]);
  // A connection that reached ready resets the backoff.
  assert.deepEqual(sleeps, [1000, 1000]);
});

test('activity restart reconnects without server EOF, releases the reader and refetches on ready', { timeout: 2000 }, async t => {
  const { createActivity } = await import('../static/activity.js');
  const stop = new AbortController();
  t.after(() => stop.abort());
  let calls = 0, cancelled = 0;
  const signals = [], bodies = [];
  const api = {
    activity: async ({ signal }) => {
      signals.push(signal);
      if (++calls === 3) throw Object.assign(new Error('forbidden'), { status: 403 });
      const body = calls === 1 ? new ReadableStream({
        start(controller) {
          controller.enqueue(new TextEncoder().encode('event: ready\ndata: {}\n\nevent: hub_restarting\nretry: 1000\ndata: {"retry_after_ms":1000}\n\nevent: message\ndata: {"thread_id":"stale"}\n\n'));
        },
        cancel() { cancelled++; },
      }) : new Response('event: ready\ndata: {}\n\nevent: resync\ndata: {}\n\n').body;
      bodies.push(body);
      return new Response(body);
    },
  };
  const sleeps = [], states = [], seen = [];
  const feed = createActivity({ api, signal: stop.signal, random: () => 0.5, sleep: async ms => { sleeps.push(ms); } });
  feed.onConn(state => states.push(state));
  feed.subscribe(ev => seen.push(ev));
  await feed.start();
  assert.equal(calls, 3);
  assert.equal(cancelled, 1);
  assert.ok(signals.every(signal => signal.aborted));
  assert.ok(bodies.every(body => !body.locked));
  assert.deepEqual(sleeps, [1000, 1000]);
  assert.deepEqual(states, ['live', 'reconnecting', 'live', 'reconnecting', 'stopped']);
  assert.equal(feed.conn(), 'stopped');
  assert.deepEqual(seen, [
    { type: 'ready', data: {} }, { type: 'ready', data: {} }, { type: 'resync', data: {} },
  ]);
});

test('activity retries proxy outages with bounded jitter; credentials and disabled status are terminal', async () => {
  const { createActivity } = await import('../static/activity.js');
  const { createApi } = await import('../static/api.js');
  for (const status of [401, 403, 204]) {
    const stop = new AbortController();
    let calls = 0, unauthorized = 0;
    const api = createApi({ secret: 's', onUnauthorized: () => unauthorized++, fetch: async () => {
      calls++;
      return new Response(null, { status });
    } });
    const sleeps = [];
    const feed = createActivity({ api, signal: stop.signal, sleep: async ms => sleeps.push(ms) });
    await feed.start();
    assert.equal(calls, 1);
    assert.equal(feed.conn(), 'stopped');
    assert.deepEqual(sleeps, []);
    assert.equal(unauthorized, status === 401 ? 1 : 0);
  }
  const delays = [];
  for (const random of [() => 0, () => 1]) {
    const stop = new AbortController();
    let calls = 0;
    const sleeps = [];
    const api = { activity: async () => {
      if (++calls === 9) throw Object.assign(new Error('forbidden'), { status: 403 });
      throw Object.assign(new Error('proxy unavailable'), { status: 503 });
    } };
    await createActivity({ api, signal: stop.signal, random, sleep: async ms => sleeps.push(ms) }).start();
    assert.equal(sleeps.length, 8);
    assert.ok(sleeps.every(ms => ms >= 800 && ms <= 30000));
    delays.push(sleeps[0]);
  }
  assert.deepEqual(delays, [800, 1200]);
});

test('connection indicator shows activity-only reconnects until both streams recover', async () => {
  const { connectionState } = await import('../static/connection-model.js');
  assert.equal(connectionState('live', 'reconnecting'), 'reconnecting');
  assert.equal(connectionState('reconnecting', 'live'), 'reconnecting');
  assert.equal(connectionState('live', 'connecting'), 'connecting');
  assert.equal(connectionState('live', 'live'), 'live');
  assert.equal(connectionState('stopped', 'live'), 'stopped');
  assert.equal(connectionState('live', 'stopped'), 'stopped');
});

test('silent activity reconnect releases its pending read and cancels the watchdog on stop', { timeout: 2000 }, async t => {
  const { createActivity } = await import('../static/activity.js');
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const stop = new AbortController();
  t.after(() => stop.abort());
  let ready, cancelled = 0, attempts = 0, connSignal;
  const opened = new Promise(resolve => { ready = resolve; });
  const body = new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode('event: ready\ndata: {}\n\n')); },
    cancel() { cancelled++; },
  });
  const feed = createActivity({
    api: { activity: async ({ signal }) => { attempts++; connSignal = signal; return new Response(body); } },
    signal: stop.signal,
    sleep: async () => { assert.ok(connSignal.aborted); assert.equal(body.locked, false); stop.abort(); },
  });
  feed.subscribe(ev => { if (ev.type === 'ready') ready(); });
  const running = feed.start();
  await opened;
  t.mock.timers.tick(45000);
  await running;
  t.mock.timers.tick(90000);
  assert.equal(cancelled, 1);
  assert.equal(attempts, 1);
  assert.equal(feed.conn(), 'stopped');
});
