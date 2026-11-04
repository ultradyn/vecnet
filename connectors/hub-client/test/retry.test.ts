import test from 'node:test';
import assert from 'node:assert/strict';
import { HubError } from '../src/errors.ts';
import { retryHub } from '../src/retry.ts';
import { fakeClock } from '../testing/fixtures.ts';

test('retryHub retries retryable errors and stops on terminal ones', async () => {
  const clock = fakeClock();
  let calls = 0;
  const result = retryHub(async () => { if (++calls < 3) throw new HubError(503, 'unavailable'); return 'ok'; }, {attempts:3, clock, baseMs:10});
  await Promise.resolve(); clock.advance(10);
  await Promise.resolve(); await Promise.resolve(); clock.advance(20);
  assert.equal(await result, 'ok');
  assert.equal(calls, 3);
  calls = 0;
  await assert.rejects(retryHub(async () => { calls++; throw new HubError(422, 'invalid_input'); }, {attempts:3, clock}), {status:422});
  assert.equal(calls, 1);
  calls = 0;
  await assert.rejects(retryHub(async () => { calls++; throw new HubError(429, 'rate_limited', {}, 90000); }, {attempts:3, clock, maxWaitMs:60000}), {status:429});
  assert.equal(calls, 1);
  assert.equal(clock.now(), 30);
  const error = new HubError(400, 'bad', {secret:'not in message'});
  assert.equal(error.message, 'hub error 400');
  for (const status of [0, 408, 429, 500, 503]) assert.equal(new HubError(status, 'error').retryable, true);
  assert.equal(new HubError(403, 'forbidden').retryable, false);
});

test('fakeClock fires timers in order and cancel stops one', async () => {
  const clock = fakeClock(100);
  const seen: number[] = [];
  clock.timer(20, () => seen.push(clock.now()));
  const cancelled = clock.timer(5, () => seen.push(-1));
  clock.timer(10, () => { seen.push(clock.now()); clock.timer(2, () => seen.push(clock.now())); });
  cancelled.cancel();
  let awake = false;
  const sleeping = clock.sleep(15).then(() => { awake = true; });
  clock.advance(14);
  assert.deepEqual(seen, [110,112]); assert.equal(awake, false);
  clock.advance(6); await sleeping;
  assert.deepEqual(seen, [110,112,120]); assert.equal(clock.now(), 120);
  const abort = new AbortController();
  const pending = clock.sleep(10, abort.signal); abort.abort();
  await assert.rejects(pending, {name:'AbortError'});
  clock.advance(10);
});
