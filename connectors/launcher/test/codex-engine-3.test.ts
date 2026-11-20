import { test } from 'node:test';
import { execFileSync } from 'node:child_process';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { ownerOf } from '../src/codex/ownership.ts';
import type { InflightRow } from '../src/codex/engine.ts';
import { CodexRpcError } from '../src/codex/rpc.ts';
import type { AllowedMethod } from '../src/codex/protocol.ts';
import { threadId, minute, hour, eventually, harness, active, row, receiptSettles, claimReservations, deliveryGuards, ambiguousAfterAccept } from './codex-engine-harness.ts';
import type { RecoveryHarness } from './codex-engine-harness.ts';

test('F2 terminal-only completion drains queued wake_idle exactly once after an authoritative idle read', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>;
  const notices: string[] = [], order: string[] = [];
  const reads: { status: unknown; terminal: unknown }[] = [];
  let atInput: { rows: InflightRow[]; claims: Record<string, string[]>; receipts: number; attempt?: string } | undefined;
  h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => {
      const p = params as { status?: { type?: string } };
      notices.push(method === 'thread/status/changed' ? `status:${p.status?.type}` : method);
      fn(method, params);
    }); },
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      if (method === 'turn/start') {
        order.push('input');
        const rows = h.rows();
        atInput = { rows, claims: claimReservations(h),
          receipts: receiptSettles(h).length,
          attempt: rows[0] && existsSync(h.plan.trace + '.pending') ? JSON.parse(readFileSync(h.plan.trace + '.pending', 'utf8')).attempt : undefined };
      }
      const result = await rpc.call<T>(method, params, ms);
      if (method === 'thread/read') {
        const thread = (result as { thread: { status: unknown; turns: { status: string }[] } }).thread;
        reads.push({ status: thread.status, terminal: thread.turns.at(-1)?.status }); order.push('read');
      }
      return result;
    },
  }) });
  assert.equal(h.engine.subscribed(), true); assert.deepEqual(h.engine.liveness(), { live: true });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  assert.equal(h.sends().length, 0); assert.deepEqual(h.rows(), []);
  assert.deepEqual(claimReservations(h), {});
  assert.ok(!existsSync(h.plan.claims + '.accepted'), 'queued input has no display proof');
  const opens = h.opens, resumes = h.fake.requests.filter(r => r.method === 'thread/resume').length;
  const readBase = reads.length, noticeBase = notices.length; order.length = 0;
  h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' });
  await h.sync();
  assert.deepEqual(notices.slice(noticeBase), ['turn/completed'], 'no idle status notice supplies the recovery trigger');
  await h.tick(25);
  await eventually(() => h.sends().length === 1, 'F2 existing wake_idle must start without another delivery or idle notice');
  assert.ok(reads.slice(readBase).some(r => (r.status as { type: string }).type === 'idle' && r.terminal === 'completed'),
    'engine, rather than the harness barrier, reads authoritative idle with terminal history');
  assert.ok(order.indexOf('read') >= 0 && order.indexOf('read') < order.indexOf('input'));
  assert.equal(h.sends()[0].method, 'turn/start');
  const input = h.sends()[0].params as { clientUserMessageId: string; additionalContext: Record<string, unknown> };
  assert.deepEqual(Object.keys(input.additionalContext), ['vecnet_i1']);
  assert.ok(atInput); assert.equal(atInput.rows.length, 1); assert.equal(atInput.rows[0].state, 'sending');
  assert.equal(atInput.rows[0].key, input.clientUserMessageId); assert.equal(atInput.attempt, atInput.rows[0].attempt);
  assert.deepEqual(atInput.rows[0].owner, ownerOf(h.binding));
  assert.deepEqual(atInput.claims, { [atInput.rows[0].claim]: ['i1'] });
  assert.equal(atInput.receipts, 0, 'claim and durable attempt precede RPC and receipt publication');
  await h.sync();
  const publications = receiptSettles(h);
  assert.equal(publications.length, 1);
  const args = publications[0].argv;
  assert.equal(args[args.indexOf('--claim') + 1], atInput.rows[0].claim);
  assert.equal(args[args.indexOf('--receipt-key') + 1], input.clientUserMessageId);
  assert.equal(args[args.indexOf('--delivery-ids') + 1], 'i1');
  assert.equal(args[args.indexOf('--attempt') + 1], atInput.attempt);
  assert.equal(JSON.parse(readFileSync(h.plan.claims + '.accepted', 'utf8'))[receiptSettles(h)[0].argv[receiptSettles(h)[0].argv.indexOf('--claim') + 1]], true);
  assert.deepEqual(h.rows(), []); assert.deepEqual(h.kv.keys('releasing:'), []);
  const repeatBase = reads.length;
  for (let i = 0; i < 3; i++) h.fake.notifySubscribers(threadId, 'turn/completed', {
    threadId: threadId, turn: { id: 'active', status: 'completed' },
  });
  await h.sync(); await h.tick(1000);
  assert.equal(h.sends().length, 1, 'repeated completion cannot start or steer the displayed delivery again');
  assert.equal(deliveryGuards(h).length, 1);
  assert.equal(receiptSettles(h).length, 1);
  assert.ok(reads.length - repeatBase <= 3, 'repeat burst permits at most one status probe per completion');
  assert.equal(h.releases().length, 0); assert.equal(h.plan.events.length, 1);
  assert.equal(h.opens, opens); assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, resumes);
  assert.deepEqual(h.fake.replies, []);
});

for (const failedPass of [1, 2]) test(`F2-I1 ${failedPass === 1 ? 'initial completion read fails' : 'gated idle read with duplicates then trailing read fails'} recovers queued wake_idle exactly once without a later notice`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>, observing = false, reads = 0, failures = 0, inFlight = 0, maximum = 0;
  const notices: string[] = [], outcomes: string[] = [];
  let atInputNotices: string[] = [];
  h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => { if (observing) notices.push(method); fn(method, params); }); },
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      if (observing && method === 'turn/start') { atInputNotices = [...notices]; outcomes.push('input'); }
      if (!observing || method !== 'thread/read') return rpc.call<T>(method, params, ms);
      reads++; inFlight++; maximum = Math.max(maximum, inFlight);
      try {
        const result = await rpc.call<T>(method, params, ms);
        // Fail only this engine-owned response; the harness has independent barrier reads.
        if (reads === failedPass) throw new CodexRpcError(-32603, 'temporary completion read failure');
        outcomes.push((result as { thread: { status: { type: string } } }).thread.status.type);
        return result;
      } catch (error) { failures++; outcomes.push('failed'); throw error; }
      finally { inFlight--; }
    },
  }) });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  assert.equal(h.sends().length, 0); assert.deepEqual(h.rows(), []);
  const opens = h.opens, resumes = h.fake.requests.filter(r => r.method === 'thread/resume').length;
  const gate = failedPass === 2 ? h.fake.gateNextRead() : undefined;
  observing = true;
  try {
    h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' });
    if (gate) {
      await eventually(() => gate.entered, 'successful idle read is blocked on the wire');
      for (let i = 0; i < 4; i++) h.fake.notifySubscribers(threadId, 'turn/completed', { threadId: threadId, turn: { id: 'active', status: 'completed' } });
      await eventually(() => notices.length === 5, 'finite duplicates reached the blocked engine');
      assert.equal(reads, 1); assert.equal(inFlight, 1); assert.equal(h.sends().length, 0);
      assert.ok(deliveryGuards(h).length === 0);
      gate.release();
    }
    await eventually(() => failures === 1, 'one temporary RPC error leaves the connection open');
    await h.sync();
    assert.deepEqual(outcomes, failedPass === 1 ? ['failed'] : ['idle', 'failed']);
    assert.equal(h.opens, opens); assert.equal(h.engine.state(), 'ready');
    assert.equal(h.sends().length, 0, 'failed observation does not directly send input');
    // Advance a finite fake-clock retry budget without a new notice or delivery.
    for (const ms of [1000, 2000, 4000, 8000, 15000]) { await h.tick(ms); if (h.sends().length) break; }
    assert.equal(h.sends().length, 1, 'F2-I1 failed completion observation must retry and drain the existing wake_idle');
    assert.ok(reads >= 2); assert.equal(failures, 1); assert.equal(maximum, 1);
    assert.deepEqual(atInputNotices, Array(failedPass === 1 ? 1 : 5).fill('turn/completed'));
    assert.ok(outcomes.lastIndexOf('idle') >= 0 && outcomes.lastIndexOf('idle') < outcomes.indexOf('input'));
    if (failedPass === 1) assert.ok(outcomes.lastIndexOf('idle') > outcomes.indexOf('failed'), 'initial failure needs a successful observation retry');
    await h.tick(2 * minute);
    assert.equal(h.sends().length, 1); assert.equal(h.sends()[0].method, 'turn/start');
    assert.equal(deliveryGuards(h).length, 1);
    const input = h.sends()[0].params as { clientUserMessageId: string };
    const receipts = receiptSettles(h);
    assert.equal(receipts.length, 1); assert.ok(receipts[0].argv.includes(input.clientUserMessageId));
    assert.equal(receipts[0].argv[receipts[0].argv.indexOf('--delivery-ids') + 1], 'i1');
    assert.equal(JSON.parse(readFileSync(h.plan.claims + '.accepted', 'utf8'))[receiptSettles(h)[0].argv[receiptSettles(h)[0].argv.indexOf('--claim') + 1]], true);
    assert.equal(Object.values(claimReservations(h)).flat().join(','), 'i1');
    assert.deepEqual(h.rows(), []); assert.deepEqual(h.kv.keys('releasing:'), []);
    assert.equal(h.releases().length, 0); assert.equal(h.plan.events.length, 1); assert.equal(h.opens, opens);
    assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, resumes);
  } finally { gate?.release(); }
});

test('F2 launcher-origin terminal-only notLoaded discovery never calls ensureLoaded or resumes', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let loads = 0;
  const h = await harness(t, { status: active, binding: { origin: 'launcher', ensureLoaded: async () => { loads++; return true; } } });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  await h.tick(minute);
  const resumes = h.fake.requests.filter(r => r.method === 'thread/resume').length;
  h.fake.completeTurnOnly(threadId, 'active', { type: 'notLoaded' }); await h.sync(); await h.tick(25);
  assert.equal(loads, 0, 'completion observation cannot authorize the launcher cold-load hook');
  assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, resumes);
  assert.equal(h.sends().length, 0); assert.deepEqual(h.rows(), []);
});

for (const invalidation of ['owner', 'generation', 'expiry', 'over_limit'] as const) test(`F2 captured idle read is fenced by ${invalidation}`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  const gate = h.fake.gateNextRead();
  try {
    h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' });
    await eventually(() => gate.entered, 'completion read is captured');
    if (invalidation === 'owner') { h.plan.owner = { ...h.plan.owner!, generation: 'b'.repeat(32) }; h.save(); }
    if (invalidation === 'generation') h.binding.generation = 'b'.repeat(32);
    if (invalidation === 'expiry') h.binding.expiresAt = h.clock.now();
    if (invalidation === 'over_limit') h.engine.setOverLimit(true);
    gate.release(); await h.sync(); await h.tick(2 * minute);
    assert.equal(h.engine.liveness().live, false); assert.equal(h.sends().length, 0);
    assert.deepEqual(h.rows(), []); assert.equal(deliveryGuards(h).length, 0); assert.equal(h.releases().length, 0);
  } finally { gate.release(); }
});

for (const watchTrigger of [false, true]) test(`F2 fix1 TR61-01 outstanding gap and watch ${watchTrigger} cannot bypass original proof`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: RecoveryHarness, atInputProofs = 0;
  h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      if (method === 'turn/start') { atInputProofs = receiptSettles(h).length; assert.deepEqual(h.rows().filter(r => r.state === 'pending'), []); }
      return rpc.call<T>(method, params, ms);
    },
  }) });
  h.fake.withholdNextItem(); await h.wake(); h.deps.minGapMs = 5000;
  const original = structuredClone(h.rows()[0]); assert.ok(original);
  let armedGap: { at: number; fired: boolean; cancelled: boolean } | undefined;
  const timer = h.clock.timer;
  h.clock.timer = (ms, fn) => {
    // With proof withheld, wake's two-second tick reaches push while the
    // five-second minimum gap still has roughly three seconds remaining.
    const observed = ms > 2500 && ms < 3500 ? { at: h.clock.now() + ms, fired: false, cancelled: false } : undefined;
    if (observed) { assert.equal(armedGap, undefined); armedGap = observed; }
    const alarm = timer(ms, () => { if (observed) observed.fired = true; fn(); });
    return { cancel() { if (observed) observed.cancelled = true; alarm.cancel(); } };
  };
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  h.clock.timer = timer;
  assert.ok(armedGap, 'the real minimum-gap timer must be created before proof arrives');
  assert.ok(armedGap.at > h.clock.now()); assert.equal(armedGap.fired, false); assert.equal(armedGap.cancelled, false);
  assert.equal(receiptSettles(h).length, 0, 'the original pending row has no item proof while the timer is armed');
  h.plan.settleFailure = true; h.save(); h.fake.recordLater(original.key, h.clock.now()); await h.tick(0);
  const claims = h.hooks().filter(r => r.argv.includes('--event')).length, guards = deliveryGuards(h).length;
  h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' }); await h.sync();
  const proof = receiptSettles(h)[0].argv;
  await h.tick(6000);
  assert.equal(armedGap.fired, true, 'the already-armed gap callback executes while proof still fails');
  assert.equal(armedGap.cancelled, false);
  assert.equal(h.hooks().filter(r => r.argv.includes('--event')).length, claims, 'gap cannot acquire a new claim before proof confirmation');
  assert.equal(deliveryGuards(h).length, guards); assert.equal(h.sends().length, 1); assert.deepEqual(h.rows(), [original]);
  if (watchTrigger) {
    await h.wake([{ id: 'i2', priority: 'wake_idle' }]);
    assert.equal(h.hooks().filter(r => r.argv.includes('--event')).length, claims, 'independent watch cannot bypass proof');
    assert.equal(deliveryGuards(h).length, guards); assert.equal(h.sends().length, 1);
  }
  h.plan.settleFailure = false; h.save();
  for (let i = 0; i < 20 && h.sends().length === 1; i++) await h.tick(1000);
  assert.equal(h.sends().length, 2); assert.equal(h.sends()[1].method, 'turn/start');
  assert.deepEqual(Object.keys((h.sends()[1].params as { additionalContext: object }).additionalContext), watchTrigger ? ['vecnet_i1', 'vecnet_i2'] : ['vecnet_i1']);
  assert.ok(atInputProofs >= 2, 'original proof confirmation precedes the queued turn');
  for (const r of receiptSettles(h).filter(r => r.argv.includes(original.key))) assert.deepEqual(r.argv, proof);
  assert.deepEqual(h.rows(), []); assert.equal(h.releases().length, 0);
  await h.tick(2 * minute); assert.equal(h.sends().length, 2);
});

for (const altered of ['attempt', 'deliveries', 'missing'] as const) test(`F2 fix2 TR61-03 unconfirmed altered ${altered} stays fail-closed`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  h.fake.withholdNextItem(); await h.wake();
  const original = structuredClone(h.rows()[0]);
  h.plan.settleFailure = true; h.save(); h.fake.recordLater(original.key, h.clock.now()); await h.tick(0);
  const proof = receiptSettles(h)[0].argv;
  if (altered === 'missing') h.kv.delete('inflight:' + original.key);
  else h.kv.set('inflight:' + original.key, JSON.stringify({ ...original, ...(altered === 'attempt' ? { attempt: 'd'.repeat(32) } : { deliveries: ['different'] }) }));
  h.plan.settleFailure = false; h.save();
  for (let i = 0; i < 20; i++) await h.tick(1000);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'ownership_changed' });
  assert.ok(h.log.entries.some(e => e.msg === 'codex_recovery_quarantined'));
  for (const r of receiptSettles(h)) assert.deepEqual(r.argv, proof);
  assert.equal(h.sends().length, 1); assert.equal(h.releases().length, 0);
});

test('unbind during a durable gated claim releases it for ordinary hooks', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  h.plan.hookGate = join(dirname(h.deps.env.VECNET_CONFIG), 'unbind-gate'); h.save();
  const before = h.records().filter(r => r.hookClaimed).length;
  const wake = h.wake();
  await eventually(() => h.records().filter(r => r.hookClaimed).length > before, 'claim committed before unbind');
  const claim = h.records().filter(r => r.hookClaimed).at(-1)!.hookClaimed!;
  assert.ok(h.kv.get('claiming:' + claim));
  await h.engine.stop(); await wake;
  assert.equal(h.sends().length, 0, 'unbind must not send the claimed delivery');
  assert.equal(h.releases().length, 1, 'unbind must release a claim never sent');
  assert.ok(h.releases()[0].argv.includes(claim));
  assert.equal(h.kv.keys('claiming:').length, 0); assert.equal(h.rows().length, 0);
  assert.equal(h.engine.subscribed(), false);
  const ordinary = JSON.parse(execFileSync(h.deps.vecnetBin, ['--net', 'test', '--as', 'peer', 'hook', '--event', 'user-prompt'], {
    env: { HOME: h.deps.env.HOME, PATH: h.deps.env.PATH, VECNET_CONFIG: h.deps.env.VECNET_CONFIG }, encoding: 'utf8',
  }));
  assert.deepEqual(ordinary.deliveries, ['w1'], 'ordinary hooks can see the original delivery after unbind');
});

test('unbind drains a failed release and retains cleanup state if it still fails', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { releaseFailure: true });
  h.fake.failNext('turn/start', { code: -32602, message: 'invalid input' });
  await h.wake(); const rejected = h.rows()[0]; assert.ok(rejected);
  assert.ok(h.kv.get('releasing:' + rejected.claim));
  await h.engine.stop();
  assert.equal(h.releases().length, 2, 'stop retries the durable release');
  assert.deepEqual(h.rows(), [rejected]); assert.ok(h.kv.get('releasing:' + rejected.claim));
  h.plan.releaseFailure = false; h.save();
  await h.restart();
  assert.equal(h.kv.get('releasing:' + rejected.claim), undefined);
  assert.equal(h.rows().length, 0);
});

test('hand2 identity refusal survives recreation and renewal until an explicit rebind', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  h.plan.refused = 'identity_changed'; h.save(); await h.wake();
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'identity_changed' });
  h.plan.refused = ''; h.save();
  const hooks = h.hooks().length, subscriptions = h.fake.requests.filter(r => r.method === 'thread/resume').length;
  await h.restart(); await h.tick(2 * minute);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'identity_changed' });
  h.binding.expiresAt += hour; await h.restart(); await h.tick(2 * minute);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'identity_changed' }, 'TTL renewal retains the binding generation');
  assert.equal(h.hooks().length, hooks); assert.equal(h.sends().length, 0);
  assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, subscriptions);
  h.binding.createdAt = h.clock.now(); await h.restart(); await h.tick();
  assert.deepEqual(h.engine.liveness(), { live: true }); assert.equal(h.sends().length, 1);
  assert.deepEqual(Object.keys((h.sends()[0].params as any).additionalContext), ['vecnet_w1']);
  assert.equal(h.plan.events.length, 1, 'fresh bind delivers the original queued wake');
});

for (const outcome of ['receipt', 'absence']) test(`ambiguous start into its active prior turn waits for ${outcome}`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>, race = false;
  const ambiguous = ambiguousAfterAccept();
  h = await harness(t, { wrap: rpc => {
    const wrapped = ambiguous(rpc);
    return { ...wrapped, async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      if (race && method === 'thread/turns/list' && (params as any).limit === 1) {
        race = false; h.fake.setThread({ id: threadId, status: active, activeTurnId: 'raced' });
      }
      return wrapped.call<T>(method, params, ms);
    } };
  } });
  race = true; h.fake.withholdNextItem(); await h.wake();
  await eventually(() => h.engine.state() === 'unreachable');
  assert.equal(h.rows()[0].method, 'turn/start'); assert.equal(h.rows()[0].priorTurnId, 'raced');
  assert.equal(h.rows()[0].attemptTurnId, null);
  await h.tick(1000); await h.loaded();
  assert.equal(h.rows()[0]?.state, 'unresolved'); assert.equal(h.releases().length, 0); assert.equal(h.sends().length, 1);
  if (outcome === 'receipt') {
    h.fake.recordLater(h.rows()[0].key, h.clock.now()); await h.tick();
    assert.equal(h.rows().length, 0); assert.equal(h.releases().length, 0);
  } else {
    h.fake.notify('turn/completed', { threadId: threadId, turn: { id: 'raced', status: 'completed' } }); await h.sync();
    assert.equal(h.releases().length, 1); assert.equal(h.rows().length, 0);
  }
});

test('steer mismatch re-reads once then waits for idle', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' }); h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  await h.wake(); await eventually(() => h.sends().length === 2); await h.tick(); assert.equal(h.sends().length, 2);
  const key = (h.sends()[0].params as any).clientUserMessageId;
  h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.sync(); await h.tick(); await eventually(() => h.sends().length === 3);
  assert.equal(h.sends()[2].method, 'turn/start'); assert.equal((h.sends()[2].params as any).clientUserMessageId, key);
  assert.ok(h.fake.requests.filter(r => r.method === 'thread/turns/list').length >= 4);
});

test('mixed batch on an active thread steers only the wake', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active }); await h.wake([{ id: 'w1', priority: 'wake' }, { id: 'i1', priority: 'wake_idle' }, { id: 'h1', priority: 'hold' }]);
  await eventually(() => h.sends().length === 1); assert.deepEqual(Object.keys((h.sends()[0].params as any).additionalContext), ['vecnet_w1']);
  h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.sync(); await h.tick(); await eventually(() => h.sends().length === 2);
  assert.equal(h.sends()[1].method, 'turn/start'); assert.deepEqual(Object.keys((h.sends()[1].params as any).additionalContext), ['vecnet_i1']);
  await h.tick(); assert.equal(h.sends().length, 2);
});

test('ambiguous send with no clientId releases', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); h.fake.failNext('turn/start', 'drop'); await h.wake(); await h.tick(1000); await h.loaded();
  await eventually(() => h.releases().length === 1); assert.ok(h.releases()[0].argv.includes('--claim'));
});

test('crash between claim and inflight write releases on restart', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { claiming: '123456789012345678901234' });
  await eventually(() => h.releases().length === 1); assert.equal(h.kv.keys('claiming:').length, 0);
  assert.equal(h.hooks()[0].argv.includes('--release'), true);
  assert.ok(h.releases()[0].argv.includes('123456789012345678901234'));
  assert.deepEqual(h.plan.items, [{ id: 'w1', priority: 'wake' }]); assert.deepEqual(h.plan.events, []);
  await eventually(() => h.sends().length === 1);
  assert.deepEqual(Object.keys((h.sends()[0].params as any).additionalContext), ['vecnet_w1']); await h.tick(); assert.equal(h.sends().length, 1);
});

test('definite rejection releases claims', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t), baseline = h.hooks().length;
  h.fake.failNext('turn/start', { code: -32602, message: 'invalid input' }); await h.wake();
  await eventually(() => h.releases().length === 1); assert.equal(h.rows().length, 0);
  const claimArgs = h.hooks().slice(baseline).find(r => r.argv.includes('--event') && r.argv.includes('--claim'))!.argv;
  const claim = claimArgs[claimArgs.indexOf('--claim') + 1], releaseArgs = h.releases()[0].argv;
  assert.equal(releaseArgs[releaseArgs.indexOf('--claim') + 1], claim);
  await h.restart();
  assert.equal(h.sends().length, 2, 'the released delivery can be claimed and sent again');
  assert.deepEqual(Object.keys((h.sends()[1].params as any).additionalContext), ['vecnet_w1']);
});

test('12-hour active turn is steered throughout', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  for (let i = 0; i < 6; i++) { await h.tick(2 * hour); await h.wake([{ id: 'w' + i, priority: 'wake' }]); await eventually(() => h.sends().length === i + 1); assert.deepEqual(h.engine.liveness(), { live: true }); }
  for (const r of h.sends()) { assert.equal(r.method, 'turn/steer'); assert.equal((r.params as any).expectedTurnId, 'active'); }
});

test('activity from another client revives', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { binding: { idleDormantMs: hour } }); await h.tick(2 * hour); assert.equal(h.engine.liveness().live, false);
  h.fake.setThread({ id: threadId, status: active, activeTurnId: 'revived' }); await h.sync(); await h.wake(); await eventually(() => h.sends().length === 1);
  assert.equal(h.sends()[0].method, 'turn/steer'); assert.deepEqual(h.engine.liveness(), { live: true });
});

test('approval request during a steered turn gets no reply', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active, binding: { attended: true } }); await h.wake(); await eventually(() => h.sends().length === 1);
  h.fake.serverRequest(threadId, 'item/commandExecution/requestApproval', { threadId: threadId }); await h.sync(); assert.deepEqual(h.fake.replies, []);
});

for (const idleDelayMs of [0, 1, 3000]) test(`rejected cold resume cannot use its own idle broadcast to reattach (delay ${idleDelayMs} ms)`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let closeBeforeResume: (() => void) | undefined;
  const coldOrder: string[] = [];
  let observing = false;
  const h = await harness(t, { coldResumeIdleDelayMs: idleDelayMs, wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => {
      if (observing && method === 'thread/status/changed' && (params as any).status?.type === 'idle') coldOrder.push('idle');
      fn(method, params);
    }); },
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      if (method === 'thread/resume') closeBeforeResume?.();
      const result = await rpc.call<T>(method, params, ms);
      if (observing && method === 'thread/resume') coldOrder.push('response');
      return result;
    },
  }) });
  await h.engine.stop(); h.fake.detachOther(threadId);
  const baseline = h.fake.requests.length;
  closeBeforeResume = () => {
    closeBeforeResume = undefined;
    observing = true;
    h.fake.setThread({ id: threadId, status: { type: 'notLoaded' } });
    h.fake.notify('thread/closed', { threadId: threadId });
  };
  await h.restart(); await h.tick(2000); await h.wake();
  assert.deepEqual(coldOrder.slice(0, 2), idleDelayMs ? ['response', 'idle'] : ['idle', 'response']);
  const requests = h.fake.requests.slice(baseline), resume = requests.find(r => r.method === 'thread/resume');
  assert.ok(resume);
  assert.ok(requests.some(r => r.method === 'thread/unsubscribe' && r.conn === resume.conn));
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'not_loaded' });
  assert.equal(requests.filter(r => r.method === 'thread/resume').length, 1);
  assert.deepEqual(h.fake.subscribers(threadId), []); assert.equal(h.sends().length, 0);
  await h.tick(2 * minute);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'not_loaded' });
  assert.equal(h.fake.requests.slice(baseline).filter(r => r.method === 'thread/resume').length, 1);
  assert.deepEqual(h.fake.subscribers(threadId), []); assert.equal(h.sends().length, 0);
  // A later, independent load is still allowed to revive this binding.
  h.fake.attachOther(threadId); h.fake.setThread({ id: threadId, status: { type: 'idle' } });
  await h.sync(); await h.tick(2000);
  assert.equal(h.sends().length, 1); assert.deepEqual(h.engine.liveness(), { live: true });
});

test('re-subscribes when another client reloads the thread', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: { type: 'notLoaded' } }); assert.equal(h.engine.subscribed(), false);
  h.fake.attachOther(threadId); h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.sync(); await h.wake();
  await eventually(() => h.sends().length === 1); assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, 1);
});

test('a tentatively admitted version still cannot deliver under an unattended asking policy', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  h.fake.setVersion('0.169.0');
  h.fake.setThread({ id: threadId, status: { type: 'idle' }, approvalPolicy: 'on-request' });
  await h.restart(); await h.wake();
  assert.equal(h.engine.state(), 'ready');
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'approval_policy' });
  assert.equal(h.engine.subscribed(), false);
  assert.equal(h.sends().length, 0);
});

test('replayed approval on resubscribe gets no reply', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { binding: { attended: true } }); h.fake.serverRequest(threadId, 'item/commandExecution/requestApproval', { threadId: threadId });
  await h.restart(); await h.sync(); assert.deepEqual(h.fake.replies, []); assert.ok(h.log.entries.some(e => e.msg === 'codex_server_request_ignored'));
});

test('unreachable socket backs off and never spawns codex', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { unreachable: true }); assert.equal(h.opens, 1);
  assert.deepEqual(h.engine.runtimeStatus(), { state: 'offline', delivery: 'unavailable', reason: 'unreachable' });
  for (const ms of [1000, 2000, 4000, 8000, 15000, 15000]) { const before: number = h.opens; await h.tick(ms); assert.equal(h.opens, before + 1); }
  await h.wake(); assert.equal(h.sends().length, 0); assert.ok(h.records().filter(r => !r.ack).every(r => r.argv.includes('watch')));
});

test('second definite additionalContext fallback rejection releases exactly once', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  h.fake.failNext('turn/start', { code: -32602, message: 'additionalContext rejected' });
  h.fake.failNext('turn/start', { code: -32602, message: 'additionalContext rejected again' });
  await h.wake(); await eventually(() => h.releases().length === 1);
  assert.equal(h.sends().length, 2); assert.equal((h.sends()[1].params as any).additionalContext, undefined); assert.equal(h.rows().length, 0);
});

test('a receipt arriving during recovery cannot be released by a stale scan', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>, inject = false;
  h = await harness(t, { status: active, seed: row(), wrap: rpc => ({ ...rpc,
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      const result = await rpc.call<T>(method, params, ms);
      if (inject && method === 'thread/turns/list' && (params as any).itemsView === 'full') {
        inject = false;
        h.fake.notifySubscribers(threadId, 'item/started', { threadId: threadId, turnId: 'active', item: { type: 'userMessage', clientId: 'vn.seed' } });
        await rpc.call('thread/read', { threadId: threadId }, ms);
      }
      return result;
    },
  }) });
  h.fake.setThread({ id: threadId, status: { type: 'idle' } }); inject = true; await h.restart();
  assert.equal(h.rows().length, 0); assert.equal(h.releases().length, 0); assert.equal(h.sends().length, 0);
});

test('handoff after a successful poll fences the actual send', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  await h.wake(); assert.equal(h.sends().length, 1, 'control: valid owner sends');
  h.plan.handoffAfterClaim = { generation: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' }; h.save();
  const before = h.records().filter(r => r.hookClaimed).length;
  await h.wake([{ id: 'handoff', priority: 'wake' }]);
  assert.ok(h.records().filter(r => r.hookClaimed).length > before, 'poll actually claimed the new delivery');
  assert.equal(h.sends().length, 1, 'no RPC after transfer');
  assert.equal(h.releases().length, 0, 'old generation must not release through replacement');
  assert.ok(h.rows().some(r => r.deliveries.includes('handoff')), 'retain recovery evidence');
});

test('unmatched authority refuses watch before any poll', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { authority: { generation: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' } });
  assert.equal(h.records().filter(r => !r.ack && (r.argv.includes('watch') || r.argv.includes('hook'))).length, 0);
  assert.equal(h.sends().length, 0);
});

test('fallback refuses replacement ownership after definite rejection', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>;
  h = await harness(t, { wrap: rpc => ({ ...rpc, async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
    try { return await rpc.call<T>(method, params, ms); }
    catch (e) {
      if (method === 'turn/start') { h.plan.owner = { ...h.plan.owner!, generation: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' }; h.save(); }
      throw e;
    }
  } }) });
  h.fake.failNext('turn/start', { code: -32602, message: 'additionalContext rejected' });
  await h.wake();
  assert.equal(h.sends().length, 1, 'fallback is fenced by replacement generation');
  assert.ok(h.rows()[0]); assert.equal(h.releases().length, 0);
  assert.equal(h.engine.liveness().live, false, 'replacement authority disables further delivery');
});

test('a successful CLI exit with refused release retains recovery evidence', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  Object.assign(h.plan, { releaseRefused: true }); h.save();
  h.fake.failNext('turn/start', { code: -32602, message: 'invalid input' });
  await h.wake();
  assert.equal(h.sends().length, 1);
  assert.ok(h.rows()[0], 'refusal must not erase durable cleanup');
  assert.ok(h.kv.get('releasing:' + h.rows()[0].claim));
  Object.assign(h.plan, { releaseRefused: false }); h.save(); await h.tick(1000);
  assert.equal(h.rows().length, 0);
  assert.equal(h.sends().length, 2, 'successful cleanup permits safe replay');
});

test('failed authenticated verification stays inactive until credentials recover', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { verifyFailure: true });
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'identity_unverified' });
  assert.equal(h.records().filter(r => !r.ack && (r.argv.includes('watch') || r.argv.includes('hook'))).length, 0);
  h.plan.verifyFailure = false; h.save(); await h.tick(minute); await h.loaded();
  await h.wake(); assert.equal(h.sends().length, 1);
});

test('runtime status reports activity and delivery health without private ownership fields', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  assert.deepEqual(h.engine.runtimeStatus(), { state: 'idle', delivery: 'ready', version: '0.166.0' });
  h.fake.setThread({ id: threadId, status: active, activeTurnId: 'active' }); await h.sync();
  assert.deepEqual(h.engine.runtimeStatus(), { state: 'working', delivery: 'ready', version: '0.166.0' });
  h.fake.setThread({ id: threadId, status: { type: 'active', activeFlags: ['waitingOnApproval'] }, activeTurnId: 'active' }); await h.sync();
  assert.deepEqual(h.engine.runtimeStatus(), { state: 'blocked', delivery: 'unavailable', reason: 'waiting_approval', version: '0.166.0' });
  await h.engine.stop();
  assert.equal(h.engine.runtimeStatus().state, 'offline');
  assert.equal(h.engine.runtimeStatus().reason, 'stopped');
});
