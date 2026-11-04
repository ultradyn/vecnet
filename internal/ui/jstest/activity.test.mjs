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
  const feed = createActivity({ api, signal: stop.signal, sleep: async ms => { sleeps.push(ms); } });
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
