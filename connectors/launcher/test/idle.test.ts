import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { LauncherConfig } from '../src/config.ts';
import { createIdleClock, idleVerdict } from '../src/idle.ts';
import type { IdleInput } from '../src/idle.ts';

const minute = 60_000, hour = 60 * minute;
const cfg: LauncherConfig['idle'] = {
  park_after_ms: 30 * minute, settle_opaque_ms: 2 * hour,
  settle_observable_ms: 24 * hour, check_every_ms: minute, deadline_margin_ms: 5 * minute,
};
function input(patch: Partial<IdleInput> = {}): IdleInput {
  return {
    sessionKind: 'idle', activity: { turnRunning: false, openApprovals: 0, queuedInputs: 0,
      backgroundTasks: { known: 0, observable: false }, lastActivityAt: new Date(0).toISOString() },
    launcherBuffered: 0, openParent: 0, lastActivityAt: 0, lastRelayAt: 0, parkedAt: null,
    deadlineAt: 72 * hour, caps: { resume: 'transcript', backgroundTasks: 'opaque' }, canPark: true, ...patch,
  };
}
function none(i: IdleInput, now: number) {
  const verdict = idleVerdict(i, now, cfg);
  assert.equal(verdict.action, 'none');
  if (verdict.action === 'none') {
    assert.ok(verdict.nextCheckAt > now);
    assert.ok(verdict.nextCheckAt <= i.deadlineAt - cfg.deadline_margin_ms);
  }
}
async function flush() { for (let n = 0; n < 40; n++) await Promise.resolve(); }

test('busy states never park or settle: turn running, open approval, open question, buffered, native queue, observable background task', () => {
  const base = input();
  const busy: IdleInput[] = [
    input({sessionKind: 'turn_running'}), input({activity: {...base.activity, turnRunning: true}}),
    input({sessionKind: 'waiting_approval'}), input({activity: {...base.activity, openApprovals: 1}}),
    input({openParent: 1}), input({launcherBuffered: 1}), input({activity: {...base.activity, queuedInputs: 1}}),
    input({caps: {resume: 'transcript', backgroundTasks: 'observable'},
      activity: {...base.activity, backgroundTasks: {known: 1, observable: true}}}),
  ];
  for (const i of busy) { none(i, 30 * minute); none(i, 25 * hour); }
});

test('transcript + opaque with park offered: park at 30 min, settle at 2 h with quiescence assumed', () => {
  none(input(), 30 * minute - 1);
  const park = idleVerdict(input(), 30 * minute, cfg);
  assert.equal(park.action, 'park');
  if (park.action === 'park') assert.ok(park.nextCheckAt > 30 * minute);
  const parked = input({sessionKind: 'parked', parkedAt: 30 * minute});
  none(parked, 2 * hour - 1);
  assert.deepEqual(idleVerdict(parked, 2 * hour, cfg), {action: 'settle', reason: 'idle', quiescence: 'assumed'});
});

test('transcript + opaque without park (t3): never parks, settles at 2 h assumed', () => {
  const i = input({canPark: false});
  none(i, 30 * minute); none(i, 2 * hour - 1);
  assert.deepEqual(idleVerdict(i, 2 * hour, cfg), {action: 'settle', reason: 'idle', quiescence: 'assumed'});
});

test('observable parked: settle at 24 h proven', () => {
  const caps = {resume: 'transcript', backgroundTasks: 'observable'} as const;
  const i = input({caps, sessionKind: 'parked', parkedAt: 30 * minute});
  none(i, 2 * hour); none(i, 24 * hour - 1);
  assert.deepEqual(idleVerdict(i, 24 * hour, cfg), {action: 'settle', reason: 'idle', quiescence: 'proven'});
  assert.deepEqual(idleVerdict(input({caps, canPark: false}), 2 * hour, cfg),
    {action: 'settle', reason: 'idle', quiescence: 'proven'});
});

test('resume none: never parks; settles at 2 h', () => {
  for (const backgroundTasks of ['none', 'opaque'] as const) {
    const i = input({caps: {resume: 'none', backgroundTasks}});
    none(i, 30 * minute); none(i, 2 * hour - 1);
    assert.deepEqual(idleVerdict(i, 2 * hour, cfg),
      {action: 'settle', reason: 'idle', quiescence: backgroundTasks === 'opaque' ? 'assumed' : 'proven'});
  }
  none(input({caps: {resume: 'live', backgroundTasks: 'none'}}), 30 * minute);
});

test('requester wrapup_after 45m overrides the settle timer', () => {
  for (const backgroundTasks of ['none', 'opaque'] as const) {
    const i = input({caps: {resume: 'none', backgroundTasks}, settleAfterMs: 45 * minute});
    none(i, 45 * minute - 1);
    assert.deepEqual(idleVerdict(i, 45 * minute, cfg),
      {action: 'settle', reason: 'idle', quiescence: backgroundTasks === 'opaque' ? 'assumed' : 'proven'});
  }
});

test('deadline: settle with reason deadline at deadlineAt − 5 min even while a turn is running', () => {
  const i = input({deadlineAt: hour, sessionKind: 'turn_running'});
  none(i, 55 * minute - 1);
  for (const at of [55 * minute, hour, 2 * hour]) {
    assert.deepEqual(idleVerdict(i, at, cfg), {action: 'settle', reason: 'deadline'});
  }
});

test('a relayed message or extend (touch) resets both idle timers but not the deadline', async () => {
  const relayed = input({lastRelayAt: 29 * minute, canPark: false});
  none(relayed, 2 * hour);
  assert.deepEqual(idleVerdict(relayed, 149 * minute, cfg), {action: 'settle', reason: 'idle', quiescence: 'assumed'});
  none(input({lastActivityAt: 29 * minute}), 58 * minute);
  assert.equal(idleVerdict(input({lastRelayAt: 29 * minute}), 59 * minute, cfg).action, 'park');

  const clock = fakeClock(), calls: unknown[] = [];
  const i = input({deadlineAt: 3 * hour});
  const idle = createIdleClock({clock, cfg, log: memoryLog(), async probe() { return i; },
    async park() { calls.push('park'); i.sessionKind = 'parked'; i.parkedAt = clock.now(); },
    async settle(job, why) { calls.push([job, why]); }});
  idle.track('one'); clock.advance(0); await flush();
  clock.advance(29 * minute); await flush(); idle.touch('one'); clock.advance(0); await flush();
  clock.advance(30 * minute - 1); await flush(); assert.deepEqual(calls, []);
  clock.advance(1); await flush(); assert.deepEqual(calls, ['park']);
  clock.advance(70 * minute); await flush(); idle.touch('one'); clock.advance(0); await flush();
  clock.advance(46 * minute - 1); await flush(); assert.deepEqual(calls, ['park']);
  clock.advance(1); await flush();
  assert.deepEqual(calls, ['park', ['one', {reason: 'deadline'}]]);
  idle.stop();
});

test('clock: park then settle fire once each with fakeClock; untrack stops them', async () => {
  const clock = fakeClock(), calls: string[] = [];
  let probes = 0;
  const i = input();
  const idle = createIdleClock({clock, cfg, log: memoryLog(), async probe() { probes++; return i; },
    async park(job) { calls.push(`park:${job}`); i.sessionKind = 'parked'; i.parkedAt = clock.now(); },
    async settle(job, why) { assert.deepEqual(why, {reason: 'idle', quiescence: 'assumed'}); calls.push(`settle:${job}`); }});
  idle.track('one'); idle.track('one'); clock.advance(0); await flush(); assert.equal(probes, 1);
  clock.advance(30 * minute); await flush(); assert.deepEqual(calls, ['park:one']);
  clock.advance(minute); await flush(); assert.deepEqual(calls, ['park:one']);
  clock.advance(89 * minute); await flush(); assert.deepEqual(calls, ['park:one', 'settle:one']);
  clock.advance(72 * hour); await flush(); assert.deepEqual(calls, ['park:one', 'settle:one']);
  i.sessionKind = 'idle'; i.lastActivityAt = clock.now(); i.deadlineAt = clock.now() + 72 * hour;
  idle.track('removed'); clock.advance(0); await flush(); idle.untrack('removed');
  const before = probes; clock.advance(3 * hour); await flush(); assert.equal(probes, before);
  idle.track('stopped'); idle.stop(); clock.advance(3 * hour); await flush(); assert.equal(probes, before);
  idle.track('after-stop'); clock.advance(0); await flush(); assert.equal(probes, before);
});

test('clock forgets undefined probes and cancels pending probes on untrack or stop', async () => {
  for (const how of ['undefined', 'untrack', 'stop'] as const) {
    const clock = fakeClock(); let probes = 0, actions = 0;
    let resolve!: (value: IdleInput | undefined) => void;
    const idle = createIdleClock({clock, cfg, log: memoryLog(), probe() { probes++; return new Promise(r => { resolve = r; }); },
      async park() { actions++; }, async settle() { actions++; }});
    idle.track('one'); clock.advance(0); await flush();
    if (how === 'untrack') idle.untrack('one');
    if (how === 'stop') idle.stop();
    resolve(how === 'undefined' ? undefined : input({deadlineAt: 0})); await flush();
    clock.advance(100 * hour); await flush();
    assert.equal(probes, 1); assert.equal(actions, 0); idle.stop();
  }
});

test('clock serializes touches during probes and suppresses repeated park on a stale snapshot', async () => {
  const clock = fakeClock(30 * minute), i = input();
  let release!: () => void, probes = 0, parks = 0, settles = 0, active = 0, maximum = 0;
  const idle = createIdleClock({clock, cfg, log: memoryLog(), async probe() {
    probes++; active++; maximum = Math.max(maximum, active);
    if (probes === 1) await new Promise<void>(r => { release = r; });
    active--; return i;
  }, async park() { parks++; }, async settle() { settles++; }});
  idle.track('one'); clock.advance(0); await flush();
  idle.touch('one'); idle.touch('one'); clock.advance(0); await flush(); assert.equal(probes, 1);
  release(); await flush(); clock.advance(0); await flush(); assert.equal(maximum, 1); assert.equal(parks, 0);
  clock.advance(30 * minute); await flush(); assert.equal(parks, 1);
  clock.advance(minute); await flush(); assert.equal(parks, 1);
  clock.advance(89 * minute); await flush(); assert.equal(settles, 1); idle.stop();
});

test('clock retries probe failures without logging exception bodies and respects a near deadline', async () => {
  const clock = fakeClock(), log = memoryLog(); let probes = 0, settled = 0;
  const idle = createIdleClock({clock, cfg, log, async probe() {
    if (++probes === 1) throw new Error('secret body');
    return input({deadlineAt: 5 * minute + minute + 500, sessionKind: 'turn_running'});
  }, async park() { assert.fail('busy session parked'); }, async settle(_job, why) {
    assert.deepEqual(why, {reason: 'deadline'}); settled++;
  }});
  idle.track('one'); clock.advance(0); await flush(); assert.equal(probes, 1);
  assert.equal(log.text().includes('secret body'), false);
  clock.advance(minute); await flush(); assert.equal(probes, 2);
  clock.advance(499); await flush(); assert.equal(settled, 0);
  clock.advance(1); await flush(); assert.equal(settled, 1); idle.stop();
});

test('a slow park cannot defer the hard deadline past the next check', async () => {
  const clock = fakeClock(), i = input({deadlineAt: 100});
  const shortCfg = {...cfg, park_after_ms: 10, settle_opaque_ms: 200, check_every_ms: 60, deadline_margin_ms: 5};
  let release!: () => void, parks = 0, settled = 0;
  const idle = createIdleClock({clock, cfg: shortCfg, log: memoryLog(), async probe() { return i; },
    async park() { parks++; await new Promise<void>(r => { release = r; }); i.sessionKind = 'parked'; i.parkedAt = clock.now(); },
    async settle(_job, why) { assert.deepEqual(why, {reason: 'deadline'}); settled++; }});
  idle.track('one'); clock.advance(0); await flush();
  clock.advance(10); await flush(); assert.equal(parks, 1);
  clock.advance(84); release(); await flush(); assert.equal(settled, 0);
  clock.advance(1); await flush(); assert.equal(settled, 1); idle.stop();
});

for (const pending of ['probe', 'park'] as const) {
  for (const outcome of ['resolve', 'reject'] as const) {
    test(`deadline settles while ${pending} is pending past expiry; late ${outcome} is fenced`, async () => {
      const baseClock = fakeClock(), timers = new Set<symbol>();
      const clock = {...baseClock, timer(ms: number, fn: () => void) {
        const id = Symbol(); timers.add(id);
        assert.equal(timers.size, 1, 'at most one timer per job');
        const timer = baseClock.timer(ms, () => { timers.delete(id); fn(); });
        return {cancel() { timers.delete(id); timer.cancel(); }};
      }};
      const shortCfg = {...cfg, park_after_ms: 10, settle_opaque_ms: 200, check_every_ms: 60, deadline_margin_ms: 5};
      let release!: () => void, reject!: (error: Error) => void;
      const held = new Promise<void>((resolve, fail) => { release = resolve; reject = fail; });
      let releaseSettle!: () => void, probes = 0, parks = 0;
      const calls: unknown[] = [], log = memoryLog();
      const idle = createIdleClock({clock, cfg: shortCfg, log, async probe() {
        if (++probes === 2 && pending === 'probe') await held;
        return input({deadlineAt: 100});
      }, async park() { parks++; await held; }, async settle(job, why) {
        calls.push([clock.now(), job, why]);
        await new Promise<void>(resolve => { releaseSettle = resolve; });
      }});
      idle.track('one'); clock.advance(0); await flush();
      clock.advance(10); await flush();
      assert.equal(probes, 2); assert.equal(parks, pending === 'park' ? 1 : 0);
      idle.touch('one'); idle.touch('one');
      clock.advance(84); await flush(); assert.deepEqual(calls, []);
      clock.advance(1); await flush();
      assert.deepEqual(calls, [[95, 'one', {reason: 'deadline'}]]);
      assert.equal(timers.size, 0);
      clock.advance(15); await flush(); // Both callbacks remain pending beyond expiry at t=100.
      idle.touch('one'); clock.advance(0); await flush();
      assert.equal(calls.length, 1); assert.equal(probes, 2); assert.equal(timers.size, 0);
      if (outcome === 'resolve') release(); else reject(new Error('secret callback body'));
      await flush();
      clock.advance(1_000); await flush();
      assert.equal(calls.length, 1); assert.equal(probes, 2);
      assert.equal(parks, pending === 'park' ? 1 : 0); assert.equal(timers.size, 0);
      assert.equal(log.text().includes('secret callback body'), false);
      releaseSettle(); await flush();
      clock.advance(1_000); await flush();
      assert.equal(calls.length, 1); assert.equal(probes, 2); assert.equal(timers.size, 0);
      idle.stop();
    });
  }

  test(`late ${pending} completion after deadline cannot affect a re-tracked job`, async () => {
    const clock = fakeClock();
    const shortCfg = {...cfg, park_after_ms: 10, settle_opaque_ms: 200, check_every_ms: 60, deadline_margin_ms: 5};
    let release!: () => void, probes = 0;
    const held = new Promise<void>(resolve => { release = resolve; });
    let current = input({deadlineAt: 100});
    const calls: unknown[] = [];
    const idle = createIdleClock({clock, cfg: shortCfg, log: memoryLog(), async probe() {
      const snapshot = current;
      if (++probes === 2 && pending === 'probe') await held;
      return snapshot;
    }, async park() { await held; }, async settle(_job, why) { calls.push([clock.now(), why]); }});
    idle.track('one'); clock.advance(0); await flush();
    clock.advance(10); await flush(); clock.advance(100); await flush();
    assert.deepEqual(calls, [[95, {reason: 'deadline'}]]);
    current = input({canPark: false, lastActivityAt: 110, deadlineAt: 1_000});
    idle.track('one'); clock.advance(0); await flush();
    release(); await flush();
    clock.advance(199); await flush(); assert.equal(calls.length, 1);
    clock.advance(1); await flush();
    assert.deepEqual(calls, [[95, {reason: 'deadline'}], [310, {reason: 'idle', quiescence: 'assumed'}]]);
    idle.stop();
  });
}

test('a failed idle settlement retries at the deadline without re-probing or logging the exception', async () => {
  const clock = fakeClock(), log = memoryLog();
  const shortCfg = {...cfg, settle_opaque_ms: 10, check_every_ms: 100, deadline_margin_ms: 5};
  let probes = 0;
  const calls: unknown[] = [];
  const idle = createIdleClock({clock, cfg: shortCfg, log, async probe() {
    probes++; return input({canPark: false, deadlineAt: 100});
  }, async park() { assert.fail('park not offered'); }, async settle(_job, why) {
    calls.push([clock.now(), why]);
    if (calls.length === 1) throw new Error('secret settle body');
  }});
  idle.track('one'); clock.advance(0); await flush(); clock.advance(10); await flush();
  assert.deepEqual(calls, [[10, {reason: 'idle', quiescence: 'assumed'}]]);
  idle.touch('one'); clock.advance(84); await flush(); assert.equal(calls.length, 1);
  clock.advance(1); await flush();
  assert.deepEqual(calls, [[10, {reason: 'idle', quiescence: 'assumed'}], [95, {reason: 'deadline'}]]);
  assert.equal(probes, 2); assert.equal(log.entries.length, 1);
  assert.equal(log.text().includes('secret settle body'), false);
  clock.advance(200); await flush(); assert.equal(calls.length, 2); idle.stop();
});
