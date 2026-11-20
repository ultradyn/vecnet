import { test } from 'node:test';
import assert from 'node:assert/strict';
import { writeFileSync, readFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { ownerOf } from '../src/codex/ownership.ts';
import { CodexRpcError } from '../src/codex/rpc.ts';
import type { AllowedMethod } from '../src/codex/protocol.ts';
import { threadId, minute, hour, allRequests, eventually, checkRequests, parseTraceRecords, harness, active, row, receiptSettles, claimReservations, deliveryGuards, ambiguousAfterAccept } from './codex-engine-harness.ts';
import type { RecoveryHarness } from './codex-engine-harness.ts';

test('trace records reject malformed complete records even before an incomplete suffix', () => {
  assert.throws(() => parseTraceRecords('{"argv":[}\n'), SyntaxError);
  assert.throws(() => parseTraceRecords('{"argv":[}\n{"argv":['), SyntaxError);
});

test('F2 stale terminal-only completion cannot idle a newer running turn', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  h.fake.completeTurnOnly(threadId, 'active', active);
  h.fake.setThread({ id: threadId, status: active, activeTurnId: 'new-active' });
  await h.sync(); await h.tick(25);
  h.fake.notifySubscribers(threadId, 'turn/completed', { threadId: threadId, turn: { id: 'active', status: 'completed' } });
  await h.sync(); await h.tick(1000);
  assert.equal(h.sends().length, 0, 'authoritative newer active turn still excludes wake_idle');
  assert.equal(h.engine.subscribed(), true); assert.deepEqual(h.engine.liveness(), { live: true });
  assert.ok(deliveryGuards(h).length === 0);
  assert.deepEqual(h.rows(), []); assert.equal(h.releases().length, 0);
  assert.equal(h.plan.events.length, 1); assert.deepEqual(h.fake.replies, []);
});

for (const newer of ['started', 'waiting', 'unload', 'archive'] as const) test(`F2-M2 newer ${newer} invalidates a captured idle completion read`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let lastNotice = '';
  const h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => { fn(method, params); lastNotice = method; }); },
  }) });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  const gate = h.fake.gateNextRead();
  try {
    h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' });
    await eventually(() => gate.entered, 'old idle response was captured before newer activity');
    const method = newer === 'started' ? 'turn/started' : newer === 'unload' ? 'thread/closed' : newer === 'archive' ? 'thread/archived' : 'thread/status/changed';
    if (newer === 'waiting') h.fake.setThread({ id: threadId, status: { type: 'active', activeFlags: ['waitingOnApproval'] } });
    else h.fake.notifySubscribers(threadId, method, { threadId: threadId, turn: { id: 'new-active', status: 'inProgress' } });
    await eventually(() => lastNotice === method, 'newer invalidation reached the engine before releasing old idle');
    gate.release(); await h.sync(); await h.tick(25); await h.tick(2 * minute);
    assert.equal(h.sends().length, 0, 'captured idle cannot erase newer activity or liveness gates');
    assert.ok(deliveryGuards(h).length === 0);
    assert.deepEqual(h.rows(), []); assert.deepEqual(claimReservations(h), {});
    assert.ok(!existsSync(h.plan.claims + '.accepted')); assert.equal(h.releases().length, 0);
    if (newer !== 'started') assert.equal(h.engine.liveness().live, false);
  } finally { gate.release(); }
});

test('F2 terminal-only failed settlement retries original proof before draining retained wake_idle', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  h.fake.withholdNextItem(); await h.wake();
  const original = structuredClone(h.rows()[0]); assert.ok(original);
  h.plan.settleFailure = true; h.save();
  h.fake.recordLater(original.key, h.clock.now()); await h.tick(0);
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' }); await h.sync();
  assert.deepEqual(h.rows(), [original]); assert.equal(h.sends().length, 1);
  const proof = receiptSettles(h)[0].argv;
  h.plan.settleFailure = false; h.save();
  await h.tick(1000); await h.tick(25);
  assert.equal(h.sends().length, 2); assert.equal(h.sends()[1].method, 'turn/start');
  assert.deepEqual(Object.keys((h.sends()[1].params as { additionalContext: object }).additionalContext), ['vecnet_i1']);
  const confirmations = receiptSettles(h).filter(r => r.argv.includes(original.key));
  assert.ok(confirmations.length >= 2);
  for (const r of confirmations) assert.deepEqual(r.argv, proof);
  assert.deepEqual(h.rows(), []); assert.equal(h.releases().length, 0);
  await h.tick(2 * minute); assert.equal(h.sends().length, 2); assert.equal(h.plan.events.length, 2);
});

test('F2 failed-read retry timer cannot revive replaced ownership', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let observing = false, reads = 0;
  const h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      const result = await rpc.call<T>(method, params, ms);
      if (observing && method === 'thread/read' && ++reads === 1) throw new CodexRpcError(-32603, 'synthetic read failure');
      return result;
    },
  }) });
  await h.wake([{ id: 'i1', priority: 'wake_idle' }]); observing = true;
  h.fake.completeTurnOnly(threadId, 'active', { type: 'idle' }); await h.sync(); assert.equal(reads, 1);
  h.plan.owner = { ...h.plan.owner!, generation: 'b'.repeat(32) }; h.save();
  await h.tick(2 * minute);
  assert.equal(reads, 1, 'retry rechecks authority before issuing another status read');
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'ownership_changed' });
  assert.equal(h.sends().length, 0); assert.equal(deliveryGuards(h).length, 0); assert.deepEqual(h.rows(), []);
});

for (const firstFails of [false, true]) test(`F2 fix2 TR61-03 duplicate receipts with first confirmation failure ${firstFails}`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: RecoveryHarness, key = '', notices = 0, removals = 0;
  h = await harness(t, { status: active, wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => {
      if ((method === 'item/started' || method === 'item/completed') && (params as { item?: { clientId?: string } }).item?.clientId === key) {
        assert.ok(h.kv.get('inflight:' + key), 'both receipt notices arrive while the original row exists'); notices++;
      }
      fn(method, params);
    }); },
  }) });
  h.fake.withholdNextItem(); await h.wake();
  const original = structuredClone(h.rows()[0]); key = original.key;
  const remove = h.kv.delete;
  h.kv.delete = k => {
    remove(k);
    if (k === 'inflight:' + key) {
      assert.equal(notices, 2, 'both clears were requested before first successful cleanup');
      removals++; h.plan.settleFailure = true; h.save();
    }
  };
  h.plan.settleFailure = firstFails; h.save();
  h.fake.recordLater(key, h.clock.now()); h.clock.advance(0);
  h.fake.notifySubscribers(threadId, 'item/completed', { threadId, turnId: 'active', item: { type: 'userMessage', clientId: key } });
  await h.sync(); assert.equal(notices, 2);
  if (firstFails) { assert.deepEqual(h.rows(), [original]); h.plan.settleFailure = false; h.save(); }
  for (let i = 0; i < 20; i++) await h.tick(1000);
  assert.equal(removals, 1); assert.deepEqual(h.rows(), []);
  assert.deepEqual(h.engine.liveness(), { live: true }, 'a stale duplicate cannot quarantine unchanged ownership');
  assert.equal(h.log.entries.filter(e => e.msg === 'codex_recovery_quarantined').length, 0);
  const confirmations = receiptSettles(h).filter(r => r.argv.includes(key));
  if (!firstFails) assert.equal(confirmations.length, 1, 'confirmed stale clear must not make another fallible confirmation');
  else assert.ok(confirmations.length >= 2, 'failed first confirmation retains its exact proof retry');
  for (const r of confirmations) assert.deepEqual(r.argv, confirmations[0].argv);
  h.plan.settleFailure = false; h.save(); await h.wake([{ id: 'w2', priority: 'wake' }]);
  assert.equal(h.sends().length, 2, 'no recreated proof barrier blocks one subsequent eligible delivery');
  await h.tick(2 * minute); assert.equal(h.sends().length, 2); assert.equal(h.releases().length, 0);
});

for (const outcome of ['accepted', 'ambiguous']) test(`over limit preserves ${outcome} inflight for receipt recovery`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  h.fake.withholdNextItem();
  if (outcome === 'ambiguous') h.fake.failNext('turn/steer', 'drop');
  await h.wake(); const inflight = h.rows()[0]; assert.ok(inflight);
  h.engine.setOverLimit(true); await h.sync(); await h.tick(2 * minute);
  assert.equal(h.releases().length, 0); assert.equal(h.sends().length, 1);
  assert.ok(h.kv.get('inflight:' + inflight.key));
});

test('unbind during a definite context rejection releases instead of sending the fallback', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let finish!: () => void, entered = false;
  const gate = new Promise<void>(resolve => { finish = resolve; });
  t.after(() => finish());
  const h = await harness(t, { wrap: rpc => ({ ...rpc,
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      try { return await rpc.call<T>(method, params, ms); }
      finally { if (method === 'turn/start') { entered = true; await gate; } }
    },
  }) });
  h.fake.failNext('turn/start', { code: -32602, message: 'additionalContext unsupported' });
  const wake = h.wake(); await eventually(() => entered, 'definite rejection before unbind');
  const rejected = h.rows()[0]; assert.ok(rejected);
  const stopped = h.engine.stop(); finish(); await stopped; await wake;
  assert.equal(h.sends().length, 1, 'unbind prevents the fallback turn RPC');
  assert.equal(h.releases().length, 1); assert.ok(h.releases()[0].argv.includes(rejected.claim));
  assert.equal(h.rows().length, 0);
});

test('hand2 expiry never releases an accepted steer without a receipt', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active, binding: { expiresAt: 5000 } });
  h.fake.withholdNextItem(); await h.wake();
  await h.tick(2 * minute);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'expired' });
  assert.equal(h.rows().length, 1); assert.equal(h.releases().length, 0); assert.equal(h.sends().length, 1);
});

for (const flag of ['waitingOnApproval', 'waitingOnUserInput']) test(`idle claim deferred by ${flag} never steers wake_idle`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { binding: { attended: true } });
  h.plan.hookGate = join(dirname(h.deps.env.VECNET_CONFIG), 'hook-open'); h.save();
  const before = h.records().filter(r => r.hookClaimed).length;
  const wake = h.wake([{ id: 'w1', priority: 'wake' }, { id: 'i1', priority: 'wake_idle' }]);
  await eventually(() => h.records().filter(r => r.hookClaimed).length > before, 'hook claimed idle batch');
  h.fake.setThread({ id: threadId, status: { type: 'active', activeFlags: [flag] }, activeTurnId: 'blocked' });
  await eventually(() => !h.engine.liveness().live, 'waiting flag received before hook output');
  writeFileSync(h.plan.hookGate, 'open'); await wake;
  assert.equal(h.sends().length, 0); assert.equal(h.rows().length, 1);
  h.fake.setThread({ id: threadId, status: active, activeTurnId: 'blocked' }); await h.sync(); await h.tick();
  assert.equal(h.sends().length, 0, 'claimed wake_idle stays pending until idle');
  h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.sync(); await h.tick();
  assert.equal(h.sends().length, 1); assert.equal(h.sends()[0].method, 'turn/start');
  assert.deepEqual(Object.keys((h.sends()[0].params as any).additionalContext), ['vecnet_w1', 'vecnet_i1']);
});

for (const state of ['idle', 'active']) for (const failures of [1, 7]) test(`${state} fresh wake survives ${failures} transient history read failures without another trigger`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: state === 'active' ? active : { type: 'idle' } });
  const opens = h.opens, subscriptions = h.fake.requests.filter(r => r.method === 'thread/resume').length;
  const reads = () => h.fake.requests.filter(r => r.method === 'thread/turns/list').length;
  const before = reads(), claims = h.records().filter(r => r.hookClaimed).length;
  for (let i = 0; i < failures; i++) h.fake.failNext('thread/turns/list', { code: -32603, message: 'temporary history read failure' });
  await h.wake();
  assert.equal(reads(), before + 1, 'the original wake fails its first history read');
  assert.equal(h.sends().length, 0); assert.equal(h.rows().length, 0);
  assert.equal(h.kv.keys('claiming:').length, 0);
  assert.equal(h.records().filter(r => r.hookClaimed).length, claims, 'no claim before the checkpoint read succeeds');
  for (let i = 0; i < failures; i++) {
    const delayMs = [1000, 2000, 4000, 8000, 15000][Math.min(i, 4)];
    await h.tick(delayMs - 50);
    assert.equal(reads(), before + i + 1, 'history retry waits for its backoff');
    await h.tick(50);
    if (i + 1 < failures) {
      assert.equal(reads(), before + i + 2, 'one independent retry at each bounded backoff step');
      assert.equal(h.sends().length, 0);
    } else assert.equal(h.sends().length, 1, 'the successful retry delivers the original wake');
  }
  await h.tick(2 * minute);
  assert.equal(h.sends().length, 1, 'original wake delivered exactly once');
  assert.equal(h.sends()[0].method, state === 'active' ? 'turn/steer' : 'turn/start');
  assert.deepEqual(Object.keys((h.sends()[0].params as any).additionalContext), ['vecnet_w1']);
  const claimed = JSON.parse(readFileSync(h.plan.claims, 'utf8')) as Record<string, string[]>;
  const deliveryClaims = Object.keys(claimed).filter(nonce => claimed[nonce].includes('w1'));
  assert.equal(deliveryClaims.length, 1, 'one delivery-bearing claim, regardless of empty status-triggered polls');
  assert.equal(h.records().filter(r => r.hookClaimed === deliveryClaims[0]).length, 1);
  assert.equal(h.rows().length, 0); assert.equal(h.releases().length, 0);
  assert.equal(h.opens, opens, 'no reconnect or restart');
  assert.equal(h.fake.requests.filter(r => r.method === 'thread/resume').length, subscriptions);
  assert.equal(h.plan.events.length, 1, 'no further watch event or status broadcast');
});

test('wake_idle on an active thread waits for idle', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active }); await h.wake([{ id: 'i1', priority: 'wake_idle' }]);
  await eventually(() => h.hooks().some(r => r.argv.includes('wake'))); assert.equal(h.sends().length, 0);
  h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.sync(); await h.tick(); await eventually(() => h.sends().length === 1);
  assert.equal(h.sends()[0].method, 'turn/start'); assert.ok(h.hooks().some(r => r.argv.includes('wake,wake_idle')));
});

test('ambiguous send is resolved by clientId without resending', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>;
  h = await harness(t, { wrap: ambiguousAfterAccept(async (rpc, key) => {
    rpc.close(); h.fake.recordLater(key, h.clock.now()); h.clock.advance(0);
  }) });
  h.fake.withholdNextItem(); await h.wake();
  assert.equal(h.rows().length, 1, 'no live receipt settled the disconnected send');
  await h.tick(1000); await h.loaded();
  await eventually(() => h.rows().length === 0);
  assert.ok(h.fake.requests.some(r => r.method === 'thread/turns/list' && (r.params as any).itemsView === 'full'));
  assert.equal(h.sends().length, 1); assert.equal(h.releases().length, 0);
});

test('unreached checkpoint stays unresolved', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active, seed: row(), history: 110 });
  // The attempted turn really exists, beyond the twenty-page boundary.
  assert.equal(h.fake.requests.filter(r => r.method === 'thread/turns/list' && (r.params as any).itemsView === 'full').length, 20); await eventually(() => h.rows()[0]?.state === 'unresolved');
  const before = h.fake.requests.filter(r => r.method === 'thread/turns/list').length;
  assert.equal(h.releases().length, 0); assert.equal(h.log.entries.filter(e => e.msg === 'codex_delivery_unresolved').length, 1);
  await h.restart(); assert.ok(h.fake.requests.filter(r => r.method === 'thread/turns/list').length >= before + 20); assert.equal(h.releases().length, 0);
});

test('failed release retains the unsent claim and retries recovery without restarting', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t), opens = h.opens, baseline = h.records().length;
  h.plan.hookFailure = 'exit'; h.plan.releaseFailure = true; h.save(); await h.wake();
  const claimed = h.records().slice(baseline).find(r => r.hookClaimed)?.hookClaimed;
  assert.ok(claimed); assert.ok(h.kv.get('claiming:' + claimed));
  assert.equal(h.rows().length, 0); assert.equal(h.sends().length, 0);
  assert.ok(h.releases().some(r => r.argv.includes(claimed)), 'release was attempted before retaining the claim');
  h.plan.hookFailure = ''; h.plan.releaseFailure = false; h.save(); await h.tick(2000);
  await eventually(() => h.sends().length === 1, 'scheduled recovery reoffers the existing delivery');
  assert.ok(h.releases().filter(r => r.argv.includes(claimed)).length >= 2);
  assert.equal(h.kv.keys('claiming:').length, 0); assert.equal(h.opens, opens);
  assert.deepEqual(Object.keys((h.sends()[0].params as any).additionalContext), ['vecnet_w1']);
  await h.tick(2 * minute); assert.equal(h.sends().length, 1);
});

test('unbound or notLoaded threads receive nothing', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: { type: 'notLoaded' } }); await h.wake(); assert.equal(h.sends().length, 0);
  await h.engine.stop(); h.fake.setThread({ id: threadId, status: { type: 'idle' } }); await h.wake([{ id: 'w2', priority: 'wake' }]); assert.equal(h.sends().length, 0);
});

test('idle_stale only when idle_dormant_hours is set', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); await h.tick(30 * hour); assert.deepEqual(h.engine.liveness(), { live: true });
  const s = await harness(t, { binding: { idleDormantMs: 6 * hour } }); await s.tick(6 * hour);
  assert.deepEqual(s.engine.liveness(), { live: false, reason: 'idle_stale' });
});

test('clientId scan requests the full items view', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active, seed: row() });
  assert.ok(h.fake.requests.some(r => r.method === 'thread/turns/list' && (r.params as any).itemsView === 'full'));
});

test('accidental cold resume is undone at once', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let fakeControl: (() => void) | undefined;
  const h = await harness(t, { wrap: rpc => ({ ...rpc, async call<T>(m: AllowedMethod, p: unknown, timeout: number): Promise<T> {
    if (m === 'thread/resume') { fakeControl?.(); } return rpc.call<T>(m, p, timeout);
  } }) });
  await h.engine.stop(); const baseline = h.fake.requests.length; fakeControl = () => h.fake.setThread({ id: threadId, status: { type: 'notLoaded' } });
  await h.restart(); await eventually(() => !h.engine.subscribed());
  const resume = h.fake.requests.slice(baseline).find(r => r.method === 'thread/resume'); assert.ok(resume);
  assert.ok(h.fake.requests.slice(baseline).some(r => r.method === 'thread/unsubscribe' && r.conn === resume.conn));
  assert.deepEqual(h.fake.subscribers(threadId), []); await h.wake();
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'not_loaded' }); assert.equal(h.sends().length, 0);
});

for (const startupDelayMs of [0, 1000]) for (const evidence of ['active status', 'new turn']) test(`independent ${evidence} after unsubscribe survives startup delay ${startupDelayMs} ms and waits for a fresh warm subscription`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  let h: Awaited<ReturnType<typeof harness>>;
  let closeBeforeResume: (() => void) | undefined, revive: (() => void) | undefined;
  let suppressActiveStatus = false, delayStartup = false;
  let startupAt: number | undefined, activityAt: number | undefined;
  const resumeTimes: number[] = [];
  h = await harness(t, { wrap: rpc => ({ ...rpc,
    onNotification(fn) { rpc.onNotification((method, params) => {
      if (suppressActiveStatus && method === 'thread/status/changed' && (params as any).status?.type === 'active') return;
      if (delayStartup && method === 'mcpServer/startupStatus/updated') {
        delayStartup = false;
        if (startupDelayMs) {
          h.clock.timer(startupDelayMs, () => { startupAt = h.clock.now(); fn(method, params); }); return;
        }
      }
      fn(method, params);
      if ((evidence === 'active status' && method === 'thread/status/changed' && (params as any).status?.type === 'active') ||
          (evidence === 'new turn' && method === 'turn/started')) {
        assert.equal(h.engine.subscribed(), false, 'activity arrives after the rejected subscription is removed');
        activityAt = h.clock.now();
      }
    }); },
    async call<T>(method: AllowedMethod, params: unknown, ms: number): Promise<T> {
      if (method === 'thread/resume') closeBeforeResume?.();
      const result = await rpc.call<T>(method, params, ms);
      if (method === 'thread/resume' && h) resumeTimes.push(h.clock.now());
      if (method === 'thread/unsubscribe') { const fn = revive; revive = undefined; fn?.(); }
      return result;
    },
  }) });
  await h.engine.stop(); h.fake.detachOther(threadId); resumeTimes.length = 0;
  h.plan.items.push({ id: 'w1', priority: 'wake' }); h.save();
  closeBeforeResume = () => {
    closeBeforeResume = undefined; delayStartup = true;
    h.fake.setThread({ id: threadId, status: { type: 'notLoaded' } });
    h.fake.notify('thread/closed', { threadId: threadId });
    revive = () => {
      h.fake.attachOther(threadId); suppressActiveStatus = evidence === 'new turn';
      h.fake.setThread({ id: threadId, status: active, activeTurnId: 'external' });
      if (evidence === 'new turn') h.fake.notify('turn/started', { threadId: threadId, turn: { id: 'external' } });
    };
  };
  await h.restart();
  assert.ok(activityAt !== undefined, 'independent activity reached the engine');
  if (startupDelayMs) assert.ok(startupAt! > activityAt, 'startup arrives after independent activity');
  assert.equal(resumeTimes.length, 2, 'a fresh subscription follows the rejected one');
  assert.ok(resumeTimes[1] - resumeTimes[0] > 2000, 'the rejected resume guard finishes before reattaching');
  assert.equal(h.sends().length, 0, 'the fresh subscription has its own startup guard');
  await h.tick(2000);
  assert.deepEqual(h.engine.liveness(), { live: true }); assert.equal(h.engine.subscribed(), true);
  assert.equal(h.sends().length, 1); assert.equal(h.sends()[0].method, 'turn/steer');
  assert.equal((h.sends()[0].params as any).expectedTurnId, 'external');
  await h.tick(2 * minute);
  assert.deepEqual(h.engine.liveness(), { live: true }); assert.equal(h.engine.subscribed(), true);
  assert.equal(h.sends().length, 1, 'quiet thread delivers once without later activity');
  assert.equal(resumeTimes.length, 2, 'no further resumes after recovery');
  assert.equal(h.plan.events.length, 0, 'no watch event was needed');
});

test('asking policy without attended unsubscribes and sends nothing', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  for (const [policy, reviewer, allowed] of [['on-request', 'user', false], ['never', 'user', true], ['on-request', 'auto_review', true]] as const) {
    const h = await harness(t); h.fake.setThread({ id: threadId, status: { type: 'idle' }, approvalPolicy: policy, approvalsReviewer: reviewer }); await h.engine.stop(); const baseline = h.fake.requests.length; await h.restart();
    await h.wake(); assert.equal(h.engine.subscribed(), allowed);
    if (!allowed) { assert.deepEqual(h.engine.liveness(), { live: false, reason: 'approval_policy' }); assert.equal(h.sends().length, 0); const resume = h.fake.requests.slice(baseline).find(r => r.method === 'thread/resume'); assert.ok(resume);
      assert.ok(h.fake.requests.slice(baseline).some(r => r.method === 'thread/unsubscribe' && r.conn === resume.conn));
      assert.deepEqual(h.fake.subscribers(threadId), []); }
    else await eventually(() => h.sends().length === 1);
  }
});

test('identity_changed binding unsubscribes and sends nothing', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); h.plan.refused = 'identity_changed'; h.save(); await h.wake(); await eventually(() => !h.engine.subscribed(), JSON.stringify({ hooks: h.hooks(), live: h.engine.liveness(), log: h.log.entries }));
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'identity_changed' }); assert.equal(h.sends().length, 0); assert.equal(h.kv.keys('claiming:').length, 0);
  assert.equal(h.fake.requests.filter(r => r.method === 'thread/unsubscribe').length, 1); assert.equal(h.log.entries.filter(e => e.msg === 'codex_binding_identity_changed').length, 1);
  for (const r of h.hooks()) { assert.equal(r.argv[r.argv.indexOf('--expect-hub') + 1], h.binding.hub); assert.equal(r.argv[r.argv.indexOf('--expect-caller') + 1], h.binding.caller); }
});

test('every recorded request uses only allowlisted methods and keys', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); await h.wake(); await eventually(() => h.sends().length === 1); checkRequests([...allRequests, ...h.fake.requests]);
  assert.equal(h.fake.overrides.length, 0);
});

test('over limit unsubscribes and clearing the limit permits a new subscription', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); h.engine.setOverLimit(true); await eventually(() => !h.engine.subscribed());
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'over_limit' }); await h.wake(); assert.equal(h.sends().length, 0);
  h.engine.setOverLimit(false); await h.sync(); await h.tick(2000); await eventually(() => h.sends().length === 1);
  assert.equal(h.engine.subscribed(), true);
});

test('thread closing rejection makes the binding not loaded', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t); h.fake.failNext('turn/start', { code: -32600, message: 'thread closing' }); await h.wake();
  await eventually(() => h.releases().length === 1);
  assert.deepEqual(h.engine.liveness(), { live: false, reason: 'not_loaded' });
  assert.equal(h.engine.subscribed(), false); await h.wake([{ id: 'w2', priority: 'wake' }]); assert.equal(h.sends().length, 1);
});

for (const change of [{ generation: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' }, { profile: 'replacement' }, { caller: 'replacement-peer' }]) {
  test(`recovery quarantines the same thread with mismatched ${Object.keys(change)[0]}`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
    const h = await harness(t, { status: active });
    await h.wake(); assert.equal(h.sends().length, 1, 'control: valid owner can really deliver');
    await h.engine.stop();
    const original = row({ owner: { ...ownerOf(h.binding), ...change } });
    h.kv.set('inflight:' + original.key, JSON.stringify(original));
    h.kv.set('releasing:' + original.claim, JSON.stringify(original));
    h.kv.set('claiming:old', JSON.stringify({ owner: original.owner, threadId: threadId, at: 0 }));
    const baseline = h.records().length;
    await h.restart();
    assert.equal(h.releases().length, 0, 'replacement owner cannot release old claims');
    assert.equal(h.sends().length, 1);
    assert.equal(h.kv.get('inflight:' + original.key), JSON.stringify(original));
    assert.ok(h.kv.get('claiming:old'));
    assert.equal(h.records().slice(baseline).filter(r => !r.ack && r.argv.includes('watch')).length, 0);
  });
}

for (const binding of [{ generation: undefined }, { configPath: undefined }, { generation: 'invalid' }]) {
  test(`missing or invalid owner ${JSON.stringify(binding)} cannot start watch`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
    const h = await harness(t, { binding });
    assert.equal(h.records().filter(r => !r.ack).length, 0);
    assert.equal(h.engine.liveness().live, false);
  });
}

test('ambiguous restart retains its guard until exact receipt then resumes watching', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { wrap: ambiguousAfterAccept() });
  h.fake.withholdNextItem(); await h.wake();
  const inflight = h.rows()[0], marker = h.plan.trace + '.pending';
  assert.ok(inflight); assert.ok(existsSync(marker));
  assert.deepEqual(JSON.parse(readFileSync(marker, 'utf8')).owner, ownerOf(h.binding));
  await h.restart();
  const polls = h.hooks().length, watches = h.records().filter(r => !r.ack && r.argv.includes('watch')).length;
  h.plan.items.push({ id: 'after-recovery', priority: 'wake' }); h.save();
  await h.tick(minute);
  assert.equal(h.hooks().length, polls, 'unresolved send prevents new claims');
  assert.equal(h.releases().length, 0); assert.ok(existsSync(marker));
  h.fake.recordLater(inflight.key, h.clock.now()); await h.tick();
  assert.equal(existsSync(marker), false, 'receipt settles the original ambiguity marker');
  assert.equal(h.rows().some(r => r.key === inflight.key), false);
  await h.tick(2000);
  assert.equal(h.records().filter(r => !r.ack && r.argv.includes('watch')).length, watches + 1, 'receipt recovery restarts verified watch');
  assert.equal(h.sends().length, 2, 'pending delivery resumes without another external trigger');
  assert.equal(h.releases().length, 0);
});

test('summary-only backlog never acquires a synthetic guard or sends a turn', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t);
  Object.assign(h.plan, { summaryOnly: true }); h.save();
  h.engine.start(); await h.tick();
  assert.equal(h.sends().length, 0);
  assert.equal(h.records().filter(r => !r.ack && r.argv.includes('guard')).length, 0);
  assert.equal(h.rows().length, 0); assert.equal(h.kv.keys('claiming:').length, 0);
});

test('symlink HOME and canonical authority override retain the same owner', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { homeAlias: true });
  await h.wake(); assert.equal(h.sends().length, 1);
  assert.equal(h.engine.liveness().live, true);
  assert.ok(h.records().filter(r => !r.ack).every(r => r.env.VECNET_CODEX_BINDINGS === undefined));
});

test('restart clears recovery after the claim release already committed', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const h = await harness(t, { status: active });
  h.fake.failNext('turn/steer', { code: -32602, message: 'invalid input' });
  h.plan.releaseFailure = true; h.save(); await h.wake();
  const rejected = h.rows()[0];
  assert.ok(rejected); assert.ok(h.kv.get('releasing:' + rejected.claim));
  await h.engine.stop();
  // Simulate the CLI's durable release succeeding just before the crash.
  writeFileSync(h.plan.claims, '{}');
  h.plan.releaseFailure = false; h.save();
  await h.restart();
  assert.equal(h.kv.get('inflight:' + rejected.key), undefined);
  assert.equal(h.kv.get('releasing:' + rejected.claim), undefined);
});
