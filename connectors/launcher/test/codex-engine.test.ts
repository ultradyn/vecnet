import { test } from 'node:test';
import { execFileSync } from 'node:child_process';
import assert from 'node:assert/strict';
import { writeFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { ownerOf } from '../src/codex/ownership.ts';
import { createEngine } from '../src/codex/engine.ts';
import { openCodexRpc, CodexRpcError } from '../src/codex/rpc.ts';
import type { AllowedMethod } from '../src/codex/protocol.ts';
import { threadId, minute, hour, eventually, parseTraceRecords, harness, active, row, receiptSettles, claimReservations, deliveryGuards, ambiguousAfterAccept } from './codex-engine-harness.ts';
import type { RecoveryHarness } from './codex-engine-harness.ts';

test('trace records wait for a split record to be newline-terminated', () => {
  const first = '{"argv":["watch"],"env":{}}\n';
  const second = '{"argv":["hook"],"env":{},"hookClaimed":"claim-1"}';
  const watch = { argv: ['watch'], env: {} };
  const hook = { argv: ['hook'], env: {}, hookClaimed: 'claim-1' };
  assert.deepEqual(parseTraceRecords(''), []);
  // Poll the complete file after every possible partial append, including a
  // complete JSON value whose final newline has not been appended yet.
  for (let end = 0; end <= second.length; end++) {
    assert.deepEqual(parseTraceRecords(first + second.slice(0, end)), [watch], `partial append at ${end}`);
  }
  assert.deepEqual(parseTraceRecords(first + second + '\n'), [watch, hook]);
  assert.deepEqual(parseTraceRecords(second), [], 'newline is the record commit boundary');
});

test('F2 terminal-only completion queued before stop cannot restart delivery after shutdown', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' });
  await h.sync(); // Process the completion, but leave any newly armed fake-clock timer pending.
  await h.engine.stop();
  const requests = h.fake.requests.length, hooks = h.hooks().length;
  await h.tick(2 * minute);
  assert.equal(h.engine.subscribed(), false); assert.equal(h.sends().length, 0);
  assert.equal(h.fake.requests.length, requests); assert.equal(h.hooks().length, hooks);
  assert.equal(h.releases().length, 0); assert.deepEqual(h.rows(), []);
  assert.deepEqual(claimReservations(h), {});
  assert.equal(h.plan.events.length, 1); assert.deepEqual(h.fake.replies, []);
});

test('F2-M2 finite burst has at most one completion read in flight and one trailing read', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let observing = false, reads = 0, inFlight = 0, maximum = 0, notices = 0;
  const h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => { if (observing && method === 'turn/completed') notices++; fn(method, params); }); },
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      if (!observing || method !== 'thread/read') return rpc.call<T>(method, params, ms);
      reads++; inFlight++; maximum = Math.max(maximum, inFlight);
      try { return await rpc.call<T>(method, params, ms); } finally { inFlight--; }
    },
  }) });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  const gate = h.fake.gateNextRead(); observing = true;
  try {
    h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' });
    await eventually(() => gate.entered, 'first completion read is gated');
    for (let i = 0; i < 8; i++) h.fake.notifySubscribers(threadId, 'turn/completed', { threadId: threadId, turn: { id: 'active', status: 'completed' } });
    await eventually(() => notices === 9, 'the entire finite burst arrived while blocked');
    assert.equal(reads, 1); assert.equal(inFlight, 1); assert.equal(maximum, 1);
    assert.equal(h.sends().length, 0); assert.ok(deliveryGuards(h).length === 0);
    gate.release(); await h.sync(); await h.tick(25); await h.tick(2 * minute);
    assert.equal(maximum, 1); assert.equal(inFlight, 0); assert.ok(reads >= 1 && reads <= 2, 'one first read plus at most one trailing read');
    assert.equal(h.sends().length, 1); assert.equal(h.sends()[0].method, 'turn/start');
    assert.equal(deliveryGuards(h).length, 1);
    assert.equal(receiptSettles(h).length, 1);
    assert.equal(h.plan.events.length, 1); assert.equal(h.releases().length, 0);
  } finally { gate.release(); }
});

test('F2 unchanged idle observations during failed settlement retain idle age and original proof', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let observing = false, reads = 0;
  const h = await harness(t, { status: active, binding: { idleDormantMs: minute, expiresAt: hour }, wrap: rpc => ({ ...rpc,
    async call<T>(m: AllowedMethod, p: unknown, ms: number): Promise<T> {
      const result = await rpc.call<T>(m, p, ms);
      if (observing && m === 'thread/read') reads++;
      return result;
    },
  }) });
  h.fake.withholdNextItem(); await h.wake();
  const original = structuredClone(h.rows()[0]); assert.ok(original);
  h.plan.settleFailure = true; h.save();
  h.fake.recordLater(original.key, h.clock.now()); await h.tick(0);
  assert.deepEqual(h.rows(), [original]); assert.equal(receiptSettles(h).length, 1);
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  observing = true;
  h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' }); await h.sync();
  assert.ok(reads >= 1, 'the terminal notice supplies the authoritative idle checkpoint');
  assert.deepEqual(h.engine.liveness(), { live: true });
  for (let i = 0; i < 120; i++) {
    await h.tick(1000);
    assert.deepEqual(h.rows(), [original], 'failed durable acceptance retains original owner, attempt, claim and key');
    assert.equal(h.sends().length, 1, 'unsettled proof cannot authorize a fresh wake_idle');
  }
  assert.ok(reads > 1, 'settlement observation retries run without any new native activity');
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'idle_stale' });
  assert.equal(h.engine.subscribed(), false); assert.equal(h.releases().length, 0);
  const proof = receiptSettles(h)[0].argv;
  assert.ok(receiptSettles(h).length > 1);
  for (const r of receiptSettles(h)) assert.deepEqual(r.argv, proof);
  assert.equal(h.plan.events.length, 2);
  const retiredReads = reads, resumes = h.fake.requests.filter(r => r.method === 'thread/resume').length;
  h.plan.settleFailure = false; h.save();
  for (let i = 0; i < 20 && h.rows().length; i++) await h.tick(1000);
  assert.equal(reads, retiredReads, 'proof-only retry never observes the retired thread');
  assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, resumes);
  assert.equal(h.engine.subscribed(), false);
  assert.deepEqual(h.rows(), []); assert.equal(h.releases().length, 0);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'idle_stale' });
  assert.equal(h.sends().length, 1, 'proof confirmation cannot revive an idle-stale wake_idle');
  for (const r of receiptSettles(h)) assert.deepEqual(r.argv, proof);
});

test('F2 terminal-only completion racing a definite mismatch retries the retained claim on idle', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: RecoveryHarness, rejected = 0;
  h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      try { return await rpc.call<T>(method, params, ms); }
      catch (e) {
        if (method === 'turn/steer' && ++rejected === 2) {
          h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' });
          // Deliver the terminal notice while the row is still sending; the
          // definite rejection moves it to waitingRetry only after this await.
          await rpc.call('thread/read', { threadId }, ms);
        }
        throw e;
      }
    },
  }) });
  for (let i = 0; i < 2; i++) h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  await h.wake(); await h.tick(25);
  assert.deepEqual(h.sends().map(r => r.method), ['turn/steer', 'turn/steer', 'turn/start']);
  const keys = h.sends().map(r => (r.params as { clientUserMessageId: string }).clientUserMessageId);
  assert.equal(new Set(keys).size, 1, 'the known rejected attempt retains its original batch key');
  assert.deepEqual(h.rows(), []); assert.equal(receiptSettles(h).length, 1); assert.equal(h.releases().length, 0);
  assert.equal(h.plan.events.length, 1);
});

test('F2 fix1 TR61-01 receipt during an awaited checkpoint blocks fresh admission', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: RecoveryHarness, armed = false, key = '';
  h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      const result = await rpc.call<T>(method, params, ms);
      if (armed && method === 'thread/turns/list' && (params as { limit?: number }).limit === 1) {
        armed = false; h.fake.recordLater(key, h.clock.now()); h.clock.advance(0);
        await rpc.call('thread/read', { threadId }, ms);
      }
      return result;
    },
  }) });
  h.fake.withholdNextItem(); await h.wake(); key = h.rows()[0].key;
  h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.sync(); await h.tick(25);
  h.plan.settleFailure = true; h.save();
  const claims = h.hooks().filter(r => r.argv.includes('--event')).length;
  armed = true; await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  assert.equal(armed, false); assert.equal(h.sends().length, 1);
  assert.equal(h.hooks().filter(r => r.argv.includes('--event')).length, claims, 'proof received during latest() blocks claim creation');
  h.plan.settleFailure = false; h.save();
  for (let i = 0; i < 20 && h.sends().length === 1; i++) await h.tick(1000);
  assert.equal(h.sends().length, 2); assert.deepEqual(h.rows(), []); assert.equal(h.releases().length, 0);
});

for (const releaseFailure of [false, true]) test(`over limit releases rejected retries for ordinary hooks with release failure ${releaseFailure}`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  for (let i = 0; i < 2; i++) h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  await h.wake(); const rejected = h.rows()[0]; assert.ok(rejected);
  const other = row({ threadId: 'other', key: 'vn.other', claim: 'other' });
  h.kv.set('inflight:' + other.key, JSON.stringify(other));
  h.plan.releaseFailure = releaseFailure; h.save();
  h.engine.setOverLimit(true); await h.sync();
  assert.equal(h.engine.subscribed(), false);
  assert.equal(h.releases().length, 1, 'disabling delivery initiates durable cleanup');
  if (releaseFailure) {
    assert.ok(h.kv.get('releasing:' + rejected.claim));
    assert.ok(h.kv.get('inflight:' + rejected.key));
    h.plan.releaseFailure = false; h.save(); await h.tick(1000);
  }
  await h.tick(2 * minute);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'over_limit' });
  assert.equal(h.sends().length, 2, 'no turn RPC after the two rejected attempts');
  assert.ok(h.releases().every(r => r.argv.includes(rejected.claim)));
  assert.equal(h.kv.get('inflight:' + rejected.key), undefined);
  assert.equal(h.kv.get('releasing:' + rejected.claim), undefined);
  assert.equal(h.kv.get('inflight:' + other.key), JSON.stringify(other));
  const ordinary = JSON.parse(execFileSync(h.deps.vecnetBin, ['--net', 'test', '--as', 'peer', 'hook', '--event', 'user-prompt'], {
    env: { HOME: h.deps.env.HOME, PATH: h.deps.env.PATH, VECNET_CONFIG: h.deps.env.VECNET_CONFIG }, encoding: 'utf8',
  }));
  assert.deepEqual(ordinary.deliveries, ['w1'], 'ordinary hooks see the original delivery');
});

for (const outcome of ['accepted', 'ambiguous']) test(`unbind preserves ${outcome} inflight delivery for receipt recovery`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let finish!: () => void, entered = false;
  const gate = new Promise<void>(resolve => { finish = resolve; });
  t.after(() => finish());
  const h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      const result = await rpc.call<T>(method, params, ms);
      if (method === 'turn/steer') {
        entered = true; await gate;
        if (outcome === 'ambiguous') throw new CodexRpcError(-1, 'connection_closed', true);
      }
      return result;
    },
  }) });
  h.fake.withholdNextItem(); const wake = h.wake();
  await eventually(() => entered, 'turn RPC accepted before unbind');
  assert.equal(h.rows()[0].state, 'sending');
  const stopped = h.engine.stop(); finish(); await stopped; await wake;
  const pending = h.rows()[0]; assert.ok(pending);
  assert.equal(pending.state, outcome === 'accepted' ? 'pending' : 'unresolved');
  assert.deepEqual(h.rows(), [pending]); assert.equal(h.releases().length, 0); assert.equal(h.sends().length, 1);
  const ordinary = JSON.parse(execFileSync(h.deps.vecnetBin, ['--net', 'test', '--as', 'peer', 'hook', '--event', 'user-prompt'], {
    env: { HOME: h.deps.env.HOME, PATH: h.deps.env.PATH, VECNET_CONFIG: h.deps.env.VECNET_CONFIG }, encoding: 'utf8',
  }));
  assert.deepEqual(ordinary.deliveries, [], 'uncertain delivery stays claimed');
});

test('hand2 expiry releases a definitely rejected steer waiting for idle', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active, binding: { expiresAt: 5000 } });
  for (let i = 0; i < 2; i++) h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  await h.wake(); const rejected = h.rows()[0]; assert.ok(rejected);
  await h.tick(minute);
  assert.equal(h.releases().length, 1); assert.ok(h.releases()[0].argv.includes(rejected.claim));
  assert.equal(h.rows().length, 0); assert.equal(h.sends().length, 2);
});

test('ambiguous mismatch-to-idle retry scans history before any retransmission', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>;
  h = await harness(t, { status: active, wrap: ambiguousAfterAccept(async (rpc, key) => {
    rpc.close(); h.fake.recordLater(key, h.clock.now()); h.clock.advance(0);
  }) });
  h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  await h.wake(); assert.equal(h.sends().length, 2);
  h.fake.withholdNextItem(); h.fake.setThread({ id: threadId, status: { type: 'idle' } });
  await h.sync(); await h.tick(); await eventually(() => h.engine.state() === 'unreachable');
  assert.equal(h.rows().length, 1);
  await h.tick(1000); await h.loaded();
  assert.ok(h.fake.requests.some(r => (r.params as any)?.itemsView === 'full'));
  assert.equal(h.rows().length, 0); assert.equal(h.sends().filter(r => r.method === 'turn/start').length, 1);
  assert.equal(h.releases().length, 0);
});

test('active wake steers with the active turn id', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active }); await h.wake(); await eventually(() => h.sends().length === 1);
  assert.equal(h.sends()[0].method, 'turn/steer'); assert.equal((h.sends()[0].params as any).expectedTurnId, 'active');
});

test('failed mismatch re-read retries the original delivery on idle without restarting', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>, failRead = false;
  h = await harness(t, { status: active, wrap: rpc => ({ ...rpc, async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
    try { return await rpc.call<T>(method, params, ms); }
    catch (e) {
      if (method === 'turn/steer' && failRead) {
        failRead = false;
        h.fake.failNext('thread/turns/list', { code: -32603, message: 'temporary history read failure' });
      }
      throw e;
    }
  } }) });
  const opens = h.opens;
  failRead = true;
  h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  await h.wake();
  const original = h.rows()[0]; assert.ok(original); assert.equal(original.acceptedAt, null);
  assert.equal(h.sends().length, 1);
  h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.sync(); await h.tick(2000);
  await h.wake([{ id: 'w2', priority: 'wake' }]); await h.tick(2 * minute);
  const originalSends = h.sends().filter(r => Object.hasOwn((r.params as any).additionalContext ?? {}, 'vecnet_w1'));
  assert.equal(originalSends.length, 2, 'one rejected steer and one accepted start of the original delivery');
  assert.equal(originalSends[1].method, 'turn/start');
  assert.equal((originalSends[1].params as any).clientUserMessageId, original.key);
  assert.equal(h.opens, opens); assert.equal(h.releases().length, 0); assert.equal(h.rows().length, 0);
  assert.equal(h.plan.events.length, 2, 'no fresh event for the original delivery');
});

test('hold-only traffic sends nothing', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); const count = h.hooks().length; await h.wake([{ id: 'h1', priority: 'hold' }]);
  assert.equal(h.sends().length, 0); assert.equal(h.hooks().length, count);
});

test('accepted steer pending past 10 minutes is not released', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active }); h.fake.withholdNextItem(); await h.wake(); await eventually(() => h.rows()[0]?.state === 'pending');
  const key = h.rows()[0].key; await h.tick(24 * minute); assert.equal(h.releases().length, 0); assert.equal(h.rows().length, 1);
  assert.equal(h.log.entries.filter(e => e.msg === 'codex_receipt_pending').length, 1);
  h.fake.recordLater(key, 25 * minute); await h.tick(6 * minute); await eventually(() => h.rows().length === 0); assert.equal(h.releases().length, 0);
});

for (const failure of ['exit', 'invalid']) test(`hook ${failure} after durable claim is recovered without restarting`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t), opens = h.opens, baseline = h.records().length;
  h.plan.hookFailure = failure; h.save(); await h.wake();
  const claimed = h.records().slice(baseline).find(r => r.hookClaimed)?.hookClaimed;
  assert.ok(claimed); assert.equal(h.sends().length, 0);
  h.plan.hookFailure = ''; h.save(); await h.tick(2000);
  await eventually(() => h.sends().length === 1, 'original delivery recovered by the running engine');
  assert.ok(h.releases().some(r => r.argv.includes(claimed)));
  assert.equal(h.kv.keys('claiming:').length, 0); assert.equal(h.opens, opens);
  assert.deepEqual(Object.keys((h.sends()[0].params as any).additionalContext), ['vecnet_w1']);
  await h.tick(2 * minute); assert.equal(h.sends().length, 1);
});

test('restart with an inflight key recovers once', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active }); h.fake.withholdNextItem(); await h.wake(); await eventually(() => h.rows().length === 1);
  const inflight = h.rows()[0]; await h.engine.stop();
  assert.deepEqual(h.rows(), [inflight]); assert.equal(h.fake.subscribers(threadId).length, 0);
  h.fake.recordLater(inflight.key, h.clock.now()); h.clock.advance(0);
  assert.deepEqual(h.rows(), [inflight]);
  const scans = h.fake.requests.filter(r => (r.params as any)?.itemsView === 'full').length;
  await h.restart(); await eventually(() => h.rows().length === 0);
  assert.ok(h.fake.requests.filter(r => (r.params as any)?.itemsView === 'full').length > scans);
  await h.restart(); assert.equal(h.sends().length, 1); assert.equal(h.releases().length, 0);
});

test('archived thread goes dormant', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); h.fake.notify('thread/archived', { threadId: threadId }); await h.sync(); await h.wake();
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'archived' }); assert.equal(h.sends().length, 0);
});

test('launcher binding calls ensureLoaded when not_loaded', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>, ensures = 0;
  h = await harness(t, { status: { type: 'notLoaded' }, binding: { origin: 'launcher', attended: true, ensureLoaded: async () => { ensures++; return false; } } });
  h.binding.ensureLoaded = async () => { ensures++; h.fake.setThread({ id: threadId, status: { type: 'idle' } }); return true; };
  await h.tick(minute); await h.wake(); await eventually(() => h.sends().length === 1); assert.ok(ensures <= 2); assert.equal(h.sends()[0].method, 'turn/start');
});

test('unsubscribes on unbind, expiry, archive and idle_stale', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  for (const reason of ['unbind', 'expiry', 'archive', 'stale']) {
    const h = await harness(t, { binding: reason === 'expiry' ? { expiresAt: minute } : reason === 'stale' ? { idleDormantMs: minute } : {} });
    h.fake.detachOther(threadId);
    if (reason === 'unbind') await h.engine.stop(); else if (reason === 'archive') { h.fake.notify('thread/archived', { threadId: threadId }); await h.sync(); } else await h.tick(minute);
    await eventually(() => h.fake.requests.filter(r => r.method === 'thread/unsubscribe').length === 1);
    assert.equal(h.fake.subscribers(threadId).length, 0); await h.tick(minute); assert.equal(h.engine.subscribed(), false);
    const rpc = await openCodexRpc({ codexHome: h.binding.codexHome, euid: (process.geteuid?.() ?? -1), log: h.log, timeoutMs: 1000 });
    const r = await rpc.call<any>('thread/read', { threadId: threadId }, 1000); rpc.close(); assert.equal(r.thread.status.type, 'notLoaded');
  }
});

for (const startupDelayMs of [0, 1000]) for (const evidence of ['active status', 'new turn']) test(`independent ${evidence} before the pending resume continuation revives without another broadcast (startup delay ${startupDelayMs} ms)`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>;
  let rejectNext = false, suppressActiveStatus = false, consumed = false, delayStartup = false;
  const resumeTimes: number[] = [];
  h = await harness(t, { wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => {
      if (suppressActiveStatus && method === 'thread/status/changed' && (params as any).status?.type === 'active') return;
      if (delayStartup && startupDelayMs && method === 'mcpServer/startupStatus/updated') {
        delayStartup = false; h.clock.timer(startupDelayMs, () => fn(method, params)); return;
      }
      fn(method, params);
      if ((evidence === 'active status' && method === 'thread/status/changed' && (params as any).status?.type === 'active') ||
          (evidence === 'new turn' && method === 'turn/started')) consumed = true;
    }); },
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      const reject = method === 'thread/resume' && rejectNext;
      if (reject) {
        rejectNext = false; delayStartup = true;
        h.fake.setThread({ id: threadId, status: { type: 'notLoaded' } });
        h.fake.notify('thread/closed', { threadId: threadId });
      }
      const result = await rpc.call<T>(method, params, ms);
      if (method === 'thread/resume' && h) resumeTimes.push(h.clock.now());
      if (reject) {
        h.fake.attachOther(threadId); suppressActiveStatus = evidence === 'new turn';
        h.fake.setThread({ id: threadId, status: active, activeTurnId: 'external' });
        if (evidence === 'new turn') h.fake.notify('turn/started', { threadId: threadId, turn: { id: 'external' } });
        // A wire round trip consumes all earlier notifications before the
        // engine's awaited resume continuation can run.
        await rpc.call('thread/read', { threadId: threadId }, ms);
        assert.equal(consumed, true, 'independent activity consumed while attachment is pending');
      }
      return result;
    },
  }) });
  await h.engine.stop(); h.fake.detachOther(threadId); resumeTimes.length = 0;
  const baseline = h.fake.requests.length;
  h.plan.items.push({ id: 'w1', priority: 'wake' }); h.save(); rejectNext = true;
  await h.restart();
  assert.equal(resumeTimes.length, 2, 'a fresh warm subscription follows the cold rejection');
  assert.ok(resumeTimes[1] - resumeTimes[0] > 2000, 'the rejected subscription keeps its full cold guard');
  assert.ok(h.fake.requests.slice(baseline).some(r => r.method === 'thread/unsubscribe'));
  assert.equal(h.sends().length, 0, 'the fresh warm subscription also has a cold guard');
  await h.tick(2000); await h.tick(2 * minute);
  assert.deepEqual(h.engine.liveness(), { live: true }); assert.equal(h.engine.subscribed(), true);
  assert.equal(h.sends().length, 1); assert.equal(h.sends()[0].method, 'turn/steer');
  assert.equal((h.sends()[0].params as any).expectedTurnId, 'external');
  assert.equal(h.plan.events.length, 0, 'no later watch event or status broadcast needed');
});

test('re-subscribes after reconnect within the unload window', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); h.fake.detachOther(threadId); h.disconnect(); await h.sync(); await h.tick(1000); await h.loaded();
  assert.ok(h.clock.now() < minute); assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, 2); assert.equal(h.engine.subscribed(), true);
});

for (const [name, flag, reason, method] of [
  ['waitingOnApproval holds deliveries and keeps the subscription', 'waitingOnApproval', 'waiting_approval', 'item/commandExecution/requestApproval'],
  ['waitingOnUserInput holds deliveries and answers nothing', 'waitingOnUserInput', 'waiting_input', 'item/tool/requestUserInput'],
] as const) test(name, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active, binding: { attended: true } });
  h.fake.serverRequest(threadId, method, { threadId: threadId }); h.fake.setThread({ id: threadId, status: { type: 'active', activeFlags: [flag] }, activeTurnId: 'active' });
  await h.sync(); await h.wake(); assert.equal(h.sends().length, 0); assert.equal(h.engine.subscribed(), true); assert.deepEqual(h.engine.liveness(), { live: false, reason });
  assert.deepEqual(h.engine.runtimeStatus(), { state: 'blocked', delivery: 'unavailable', reason, version: '0.166.0' });
  await h.tick(11 * minute); assert.equal(h.log.entries.filter(e => e.msg === 'codex_binding_blocked').length, 1); assert.deepEqual(h.fake.replies, []);
  h.fake.setThread({ id: threadId, status: active, activeTurnId: 'active' }); await h.sync(); await h.tick(); await eventually(() => h.sends().length === 1);
  await h.tick(); assert.equal(h.sends().length, 1);
});

test('receipt from the item stream clears inflight', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active }); h.fake.withholdNextItem(); await h.wake(); await eventually(() => h.rows()[0]?.state === 'pending');
  const scans = h.fake.requests.filter(r => (r.params as any)?.itemsView === 'full').length;
  h.fake.recordLater(h.rows()[0].key, h.clock.now()); await h.tick(); await eventually(() => h.rows().length === 0);
  assert.equal(h.fake.requests.filter(r => (r.params as any)?.itemsView === 'full').length, scans); assert.equal(h.releases().length, 0);
});

test('late startup notification suppresses pushes during the cold-resume guard', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); await h.engine.stop();
  const baseline = h.fake.requests.length;
  const engine = createEngine(h.binding, h.deps); engine.start(); t.after(() => engine.stop());
  await eventually(() => engine.state() === 'ready');
  h.plan.items.push({ id: 'w1', priority: 'wake' }); h.save();
  h.clock.advance(1000); await h.sync(); assert.equal(h.sends().length, 0);
  h.fake.notify('mcpServer/startupStatus/updated', { threadId: threadId }); await h.sync();
  assert.deepEqual(engine.liveness(), { live: false, reason: 'not_loaded' }); assert.equal(engine.subscribed(), false);
  const resume = h.fake.requests.slice(baseline).find(r => r.method === 'thread/resume'); assert.ok(resume);
  assert.ok(h.fake.requests.slice(baseline).some(r => r.method === 'thread/unsubscribe' && r.conn === resume.conn));
  assert.deepEqual(h.fake.subscribers(threadId), []);
  h.clock.advance(2000); await h.sync(); assert.equal(h.sends().length, 0);
});

test('definite rejection cleanup survives restart while the target turn remains active', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  h.fake.failNext('turn/steer', { code: -32602, message: 'invalid input' });
  h.plan.releaseFailure = true; h.save(); await h.wake();
  assert.equal(h.rows().length, 1); const rejected = h.rows()[0];
  await h.engine.stop();
  assert.equal(h.rows().length, 1, 'cleanup intent must remain durable at shutdown');
  h.plan.releaseFailure = false; h.save(); await h.restart();
  assert.equal(h.rows().length, 0, 'definite rejection needs no completed-turn absence scan');
  assert.equal(h.sends().length, 2); assert.equal((h.sends()[1].params as any).clientUserMessageId, rejected.key);
  assert.deepEqual(Object.keys((h.sends()[1].params as any).additionalContext), ['vecnet_w1']);
  assert.equal(h.releases().filter(r => r.argv.includes(rejected.claim)).length, 3, 'initial failure, shutdown retry, then restart recovery');
  assert.equal(h.kv.keys('releasing:').length, 0); assert.equal(h.plan.events.length, 1);
});

test('terminal watch exit stops respawning until the config or binding changes', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  const watches = () => h.records().filter(r => !r.ack && r.argv.includes('watch')).length;
  const stopped = () => h.log.entries.filter(e => e.msg === 'codex_watch_stopped');
  const advanceUntil = async (fn: () => boolean, what: string) => {
    for (let i = 0; i < 100; i++) { if (fn()) return; await h.tick(1000); await delay(10); }
    assert.ok(fn(), what);
  };
  assert.equal(watches(), 1);
  // A crash without a stop line is a recoverable process failure: respawn after 1 s.
  const crash = join(dirname(h.plan.trace), 'watch-crash');
  writeFileSync(crash, ''); h.plan.watchCrash = crash; h.save();
  await eventually(() => !existsSync(crash), 'watch crashed');
  await advanceUntil(() => watches() === 2, 'watch respawned after a crash');
  assert.equal(stopped().length, 0);
  // A 401 stop line: the watch is not respawned, however long the engine runs.
  h.plan.watchStop = 401; h.save();
  await eventually(() => stopped().length === 1, 'terminal stop logged');
  assert.deepEqual(stopped()[0].meta, { threadId: threadId, status: 401 });
  for (let i = 0; i < 5; i++) await h.tick(minute);
  assert.equal(watches(), 2, 'no respawn after 401');
  // A credential/config change resumes the watch at the next minute check.
  h.plan.watchStop = 0; h.save();
  await advanceUntil(() => watches() === 3, 'watch resumed after the config changed');
  // 204 (disabled) likewise stops; a binding renewal resumes it.
  h.plan.watchStop = 204; h.save();
  await eventually(() => stopped().length === 2, 'disabled stop logged');
  for (let i = 0; i < 3; i++) await h.tick(minute);
  assert.equal(watches(), 3, 'no respawn after 204');
  // The config is unchanged (it still answers 204), so only the renewal
  // resumes the watch, which then stops again on the same answer.
  h.binding.expiresAt += hour;
  await advanceUntil(() => watches() === 4, 'watch resumed after a binding renewal');
  await eventually(() => stopped().length === 3, 'stopped again');
  for (let i = 0; i < 3; i++) await h.tick(minute);
  assert.equal(watches(), 4);
});

test('a tentatively admitted version cannot authorize an unknown root source', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { source: 'future-unknown-source' });
  h.fake.setVersion('1.0.0');
  await h.restart(); await h.wake();
  assert.equal(h.engine.liveness().live, false);
  assert.equal(h.records().filter(r => !r.ack && (r.argv.includes('watch') || r.argv.includes('hook'))).length, 0);
  assert.equal(h.sends().length, 0);
});

test('corrupt recovery shape cannot release a matching owner claim', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  await h.wake(); assert.equal(h.sends().length, 1);
  await h.engine.stop();
  const corrupt = { owner: ownerOf(h.binding), threadId: threadId, claim: 'old-claim', key: 'vn.corrupt' };
  h.kv.set('releasing:old-claim', JSON.stringify(corrupt));
  const baseline = h.records().length;
  await h.restart();
  assert.equal(h.releases().length, 0);
  assert.equal(h.kv.get('releasing:old-claim'), JSON.stringify(corrupt));
  assert.equal(h.records().slice(baseline).filter(r => !r.ack && r.argv.includes('watch')).length, 0);
});

test('a thread read naming another thread cannot authorize polling', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { wrap: rpc => ({ ...rpc, async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
    const result = await rpc.call<any>(method, params, ms);
    if (method === 'thread/read') result.thread.id = '22222222-2222-4222-8222-222222222222';
    return result;
  } }) });
  assert.equal(h.engine.liveness().live, false);
  assert.equal(h.records().filter(r => !r.ack).length, 0);
});

test('contradictory root source with parentThreadId cannot authorize polling', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { source: 'cli', parentThreadId: 'parent' });
  assert.equal(h.engine.liveness().live, false);
  assert.equal(h.records().filter(r => !r.ack).length, 0);
});

test('every retry persists a distinct guard attempt and settles only its own attempt', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  h.fake.failNext('turn/steer', { code: -32600, message: 'expectedTurnId mismatch' });
  await h.wake();
  const guards = h.records().filter(r => !r.ack && r.argv.includes('guard'));
  assert.equal(guards.length, 2);
  const attempts = guards.map(r => r.argv[r.argv.indexOf('--attempt') + 1]);
  assert.ok(attempts.every(a => /^[a-f0-9]{32}$/.test(a)));
  assert.notEqual(attempts[0], attempts[1]);
  const settles = h.records().filter(r => !r.ack && r.argv.includes('settle'));
  assert.ok(settles.length > 0);
  assert.ok(settles.every(r => r.argv[r.argv.indexOf('--attempt') + 1] === attempts[1]));
});
