import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { HubError } from '../../hub-client/src/errors.ts';
import type { HubApi } from '../../hub-client/src/ports.ts';
import type { SendRequest } from '../../hub-client/src/wire.ts';
import { openLedger } from '../src/ledger.ts';
import { createOutbox } from '../src/outbox.ts';
import { createJobReporter } from '../src/jobs.ts';
import type { JobRow } from '../src/ledger.ts';

function jobRow(jobId = 'job_one', patch: Partial<JobRow> = {}) {
  return {
    jobId, threadId: `thr_${jobId}`, launchMessageId: `msg_${jobId}`, requester: 'peer_requester', policyKey: 'prn_owner',
    backend: 't3', preset: 'default', permissionMode: 'ask' as const, project: 'repo', title: 'A job',
    input: { backend: 't3', preset: 'default', project: 'repo', permission_mode: 'ask' as const },
    tag: `vl-${jobId}`, state: 'accepted' as const, sessionRef: null, sessionKind: null, turnCursor: null,
    branch: null, workspace: null, lastActivityAt: 100, parkedAt: null, deadlineAt: 100000, createdAt: 100, ...patch,
  };
}

async function flush() { for (let n = 0; n < 40; n++) await Promise.resolve(); }
function response() { return { message_id: 'msg_sent', thread_id: 'thr_one', deliveries: [], spilled: false }; }
function request(key: string): SendRequest { return { idempotency_key: key, priority: 'hold', content: { text: 'secret body' } }; }

test('outbox sends in order, retries 503 then succeeds, keeps pending across a reopen', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vl-outbox-')); t.after(() => rmSync(dir, {recursive: true, force: true}));
  const path = join(dir, 'ledger.db'), clock = fakeClock(100), log = memoryLog();
  let l = openLedger(path, clock); t.after(() => l.close());
  l.enqueue('send', 'first', request('first'), 'first'); l.enqueue('receipt', 'second', {delivery_id: 'dly_two', state: 'processed'}, 'second');
  l.close(); l = openLedger(path, clock); assert.equal(l.pendingOutbox(10, 100).length, 2);
  const calls: string[] = [], sent: unknown[] = []; let active = 0, maximum = 0;
  const hub: Pick<HubApi, 'send' | 'receipt'> = {
    async send(req) { active++; maximum = Math.max(maximum, active); calls.push(req.idempotency_key); await Promise.resolve(); active--; if (calls.length === 1) throw new HubError(503, 'unavailable'); return response(); },
    async receipt(req) { calls.push(req.delivery_id); },
  };
  const o = createOutbox({ledger: l, clock, log, hub}), stop = new AbortController();
  o.onSent((row, res) => sent.push([row.key, res]));
  const running = o.run(stop.signal); o.kick(); o.kick(); const concurrent = o.run(stop.signal);
  await flush(); assert.deepEqual(calls, ['first']); assert.equal(l.pendingOutbox(10, clock.now()).length, 2);
  clock.advance(1000); await flush();
  assert.deepEqual(calls, ['first', 'first', 'dly_two']); assert.equal(maximum, 1);
  assert.deepEqual(sent, [['first', response()], ['second', undefined]]); assert.deepEqual(l.pendingOutbox(10, clock.now()), []);
  stop.abort(); await Promise.all([running, concurrent]);
});

test('outbox: 409 idempotency_conflict marks failed and logs the key, never sent', async t => {
  const clock = fakeClock(), l = openLedger(':memory:', clock), log = memoryLog(); t.after(() => l.close());
  const o = createOutbox({ledger: l, clock, log, hub: {async send() { throw new HubError(409, 'idempotency_conflict', {body: 'secret body'}); }, async receipt() {}}});
  o.enqueue('send', 'collision', request('collision'), 'tag_collision');
  let sent = 0, rejected = 0;
  o.onSent(() => sent++); o.onRejected(row => { rejected++; assert.equal(l.pendingOutbox(10, 0).length, 0); assert.equal(row.state, 'failed'); });
  const stop = new AbortController(), running = o.run(stop.signal); await flush(); stop.abort(); await running;
  assert.equal(sent, 0); assert.equal(rejected, 1); assert.equal(l.pendingOutbox(10, 0).length, 0);
  assert.match(log.text(), /collision/); assert.match(log.text(), /tag_collision/); assert.ok(!log.text().includes('secret body'));
  assert.equal(l.enqueue('send', 'collision', request('collision'), 'same'), 1); assert.equal(l.pendingOutbox(10, 0).length, 0);
});

test('outbox: 409 stale_sequence re-sequences the job_status to last_sequence+1 under a new key and sends it', async t => {
  const clock = fakeClock(), l = openLedger(':memory:', clock), log = memoryLog(); t.after(() => l.close()); l.createJob(jobRow());
  const calls: SendRequest[] = [], sent: string[] = [];
  const o = createOutbox({ledger: l, clock, log, hub: {async send(req) { calls.push(req); if (calls.length === 1) throw new HubError(409, 'stale_sequence', {last_sequence: 41}); return response(); }, async receipt() {}}});
  l.tx(() => createJobReporter({ledger: l, outbox: o}).report('job_one', 'accepted'));
  o.onSent(row => sent.push(row.key)); const stop = new AbortController(), running = o.run(stop.signal);
  await flush(); stop.abort(); await running;
  assert.deepEqual(calls.map(r => r.idempotency_key), ['vl.js.job_one.1', 'vl.js.job_one.42']);
  assert.deepEqual(calls.map(r => r.job_status?.sequence), [1, 42]); assert.deepEqual(sent, ['vl.js.job_one.42']);
  assert.equal(l.job('job_one')?.statusSeq, 42); assert.deepEqual(l.pendingOutbox(10, 0), []);
});

test('outbox: 429 Retry-After 120 defers the row, does not spin, does not fail it, and sends after fakeClock +120 s', async t => {
  const clock = fakeClock(100), l = openLedger(':memory:', clock), log = memoryLog(); t.after(() => l.close());
  const calls: string[] = []; let rejected = 0;
  const o = createOutbox({ledger: l, clock, log, hub: {async send(req) { calls.push(req.idempotency_key); if (calls.length === 1) throw new HubError(429, 'rate_limited', {}, 120000); return response(); }, async receipt() {}}});
  o.onRejected(() => rejected++); o.enqueue('send', 'first', request('first'), 'first'); o.enqueue('send', 'second', request('second'), 'second');
  const stop = new AbortController(), running = o.run(stop.signal); await flush();
  const deferred = l.pendingOutbox(10, 120100).find(r => r.key === 'first');
  assert.equal(deferred?.state, 'pending'); assert.equal(deferred?.notBefore, 120100);
  clock.advance(119999); o.kick(); await flush(); assert.deepEqual(calls, ['first']); assert.equal(rejected, 0);
  clock.advance(1); await flush(); assert.deepEqual(calls, ['first', 'first', 'second']);
  assert.equal(rejected, 0); assert.deepEqual(l.pendingOutbox(10, clock.now()), []); stop.abort(); await running;
});

test('outbox marks a 422 failed, logs no body, and calls onRejected', async t => {
  const clock = fakeClock(), l = openLedger(':memory:', clock), log = memoryLog(); t.after(() => l.close());
  const err = new HubError(422, 'expired', {body: 'secret body'}), rejected: unknown[] = [];
  const o = createOutbox({ledger: l, clock, log, hub: {async send() { throw err; }, async receipt() {}}});
  o.enqueue('send', 'expired', request('expired'), 'job_one');
  o.onRejected((row, error) => { assert.equal(l.pendingOutbox(10, 0).length, 0); rejected.push([row.state, error]); });
  const stop = new AbortController(), running = o.run(stop.signal); await flush(); stop.abort(); await running;
  assert.deepEqual(rejected, [['failed', err]]); assert.match(log.text(), /expired/); assert.match(log.text(), /job_one/);
  assert.ok(!log.text().includes('secret body')); assert.ok(!log.text().includes('hub error'));
});

test('stale sequence recovery preserves queued status order without colliding with reserved keys', async t => {
  const clock = fakeClock(), l = openLedger(':memory:', clock); t.after(() => l.close()); l.createJob(jobRow());
  const calls: SendRequest[] = [];
  const o = createOutbox({ledger: l, clock, log: memoryLog(), hub: {async send(req) { calls.push(req); if (calls.length === 1) throw new HubError(409, 'stale_sequence', {last_sequence: 1}); return response(); }, async receipt() {}}});
  const reporter = createJobReporter({ledger: l, outbox: o});
  l.tx(() => { reporter.report('job_one', 'accepted'); reporter.report('job_one', 'running'); reporter.report('job_one', 'succeeded'); });
  const stop = new AbortController(), running = o.run(stop.signal);
  // Attach immediately so a broken worker is observed as an assertion failure, never an unhandled rejection.
  const result = running.then(() => undefined, err => err);
  await flush(); stop.abort(); const error = await result; assert.equal(error, undefined);
  assert.deepEqual(calls.map(r => r.job_status?.sequence), [1, 2, 3, 4]);
  assert.deepEqual(calls.map(r => r.idempotency_key), ['vl.js.job_one.1', 'vl.js.job_one.2', 'vl.js.job_one.3', 'vl.js.job_one.4']);
  assert.equal(l.job('job_one')?.statusSeq, 4); assert.deepEqual(l.pendingOutbox(10, 0), []);
});

test('a deferred head survives reopening and prevents ready rows from overtaking it', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vl-deferred-')); t.after(() => rmSync(dir, {recursive: true, force: true}));
  const path = join(dir, 'ledger.db'), clock = fakeClock(); let l = openLedger(path, clock); t.after(() => l.close());
  const first = l.enqueue('send', 'first', request('first'), 'first'); l.deferOutbox(first, 120000);
  l.enqueue('send', 'second', request('second'), 'second'); l.close(); l = openLedger(path, clock);
  const calls: string[] = [];
  const o = createOutbox({ledger: l, clock, log: memoryLog(), hub: {async send(req) { calls.push(req.idempotency_key); return response(); }, async receipt() {}}});
  const stop = new AbortController(), running = o.run(stop.signal); await flush(); assert.deepEqual(calls, []);
  clock.advance(119999); await flush(); assert.deepEqual(calls, []);
  clock.advance(1); await flush(); assert.deepEqual(calls, ['first', 'second']); stop.abort(); await running;
});

test('sent callback writes commit with the sent row; a callback failure leaves a replayable pending row', async t => {
  const clock = fakeClock(), l = openLedger(':memory:', clock); t.after(() => l.close());
  const keys: string[] = [];
  const hub = {async send(req: SendRequest) { keys.push(req.idempotency_key); return response(); }, async receipt() {}};
  const broken = createOutbox({ledger: l, clock, log: memoryLog(), hub});
  broken.enqueue('send', 'atomic', request('atomic'), 'question');
  broken.onSent(() => { l.kv('questions').set('message_id', 'msg_sent'); throw new Error('callback crashed'); });
  await assert.rejects(broken.run(new AbortController().signal), /callback crashed/);
  assert.equal(l.kv('questions').get('message_id'), undefined); assert.equal(l.pendingOutbox(10, 0).length, 1);
  const recovered = createOutbox({ledger: l, clock, log: memoryLog(), hub});
  recovered.onSent((_row, result) => { l.kv('questions').set('message_id', (result as {message_id: string}).message_id); });
  const stop = new AbortController(), running = recovered.run(stop.signal); await flush(); stop.abort(); await running;
  assert.deepEqual(keys, ['atomic', 'atomic']); assert.equal(l.kv('questions').get('message_id'), 'msg_sent');
  assert.deepEqual(l.pendingOutbox(10, 0), []);
});
