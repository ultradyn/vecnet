import test from 'node:test';
import assert from 'node:assert/strict';
import { createThumbBudget } from '../static/thumbs-budget.js';

// Pure admission contracts only: no actual transport, payload, cache or DOM.
const MiB = 1048576;
const DEFAULTS = { B: 32 * MiB, C: 2, E: 16, N: 16, Q: 16, M: 10 * MiB };
const TINY = { B: 8, C: 2, E: 3, N: 2, Q: 2, M: 4 };
const flush = async () => { await Promise.resolve(); await Promise.resolve(); };
function rig(t, overrides = {}, defaults = false) {
  const limits = { ...(defaults ? DEFAULTS : TINY), ...overrides }, bounds = { ...limits };
  const budget = defaults ? createThumbBudget() : createThumbBudget({ limits });
  const errors = [], trace = [], started = new Map(), dispatched = new Set();
  const check = () => {
    const s = budget.stats;
    assert.deepEqual(Object.keys(s).sort(), ['queued', 'reservedBytes', 'residentBytes', 'residentEntries', 'running', 'sources']);
    for (const n of Object.values(s)) assert.ok(Number.isSafeInteger(n) && n >= 0);
    assert.equal(s.reservedBytes, bounds.M * s.running);
    assert.ok(s.residentBytes + s.reservedBytes <= bounds.B);
    assert.ok(s.running <= bounds.C && s.running + s.residentEntries <= bounds.E);
    assert.ok(s.queued <= bounds.Q && s.sources <= bounds.N);
  };
  // Callback exceptions are deliberately contained by the API: retain assertion
  // failures outside it so a swallowed assertion cannot make a test pass.
  const guard = fn => (...args) => { try { check(); return fn(...args); } catch (e) { errors.push(e); } };
  const add = (key, priority = 'automatic', hooks = {}) => budget.enqueue({ key, priority,
    onStart: guard(ticket => {
      assert.ok(ticket); assert.ok(!dispatched.has(ticket), 'onStart repeated for one ticket'); dispatched.add(ticket);
      started.set(key, ticket); trace.push(`start:${key}`); hooks.start?.(ticket);
    }),
    onCancel: guard(reason => { trace.push(`cancel:${key}:${reason}`); hooks.cancel?.(reason); }) });
  t.after(async () => { await flush(); check(); assert.deepEqual(errors, []); });
  return { budget, limits, trace, started, check, guard, add };
}
function expect(r, expected) {
  r.check();
  for (const [key, value] of Object.entries(expected)) assert.equal(r.budget.stats[key], value, key);
}
const finish = (ticket, residentBytes = null) => ticket.settle({ residentBytes });

test('WEB-MEM-3 T2a defaults_and_tiny_limits', async t => {
  const r = rig(t, {}, true), a = r.add('a'), b = r.add('b'); await flush();
  expect(r, { running: 2, reservedBytes: 20 * MiB });
  const queued = Array.from({ length: 16 }, (_, i) => r.add(`q${i}`));
  assert.ok(queued.every(Boolean)); assert.equal(r.add('q16'), null); expect(r, { queued: 16 });
  queued.forEach(ticket => ticket.cancel()); finish(a, 10 * MiB); finish(b, 10 * MiB);
  const c = r.add('c'); await flush(); finish(c, 2 * MiB);
  const d = r.add('d'); await flush(); expect(r, { residentBytes: 22 * MiB, reservedBytes: 10 * MiB });
  finish(d, 1); const blocked = r.add('blocked'); await flush(); expect(r, { running: 0, queued: 1 }); blocked.cancel();
  for (const ticket of [a, b, c, d]) ticket.dropPayload();
  const residents = [];
  for (let i = 0; i < 16; i++) { const ticket = r.add(`zero${i}`); await flush(); finish(ticket, 0); residents.push(ticket); }
  const eBlocked = r.add('entry17'); await flush(); expect(r, { residentEntries: 16, running: 0, queued: 1 }); eBlocked.cancel();
  const permits = Array.from({ length: 16 }, (_, i) => r.budget.claimSource(`src${i}`));
  assert.ok(permits.every(Boolean)); assert.equal(r.budget.claimSource('src16'), null); expect(r, { sources: 16 });
  permits.forEach(p => p.drop()); residents.forEach(ticket => ticket.dropPayload());
  const tiny = rig(t), old = tiny.budget.stats;
  assert.ok(Object.isFrozen(old)); assert.throws(() => { old.running = 99; }, TypeError);
  assert.throws(() => { tiny.budget.stats = {}; }, TypeError);
  tiny.limits.M = 1; tiny.limits.C = 99; tiny.limits.B = 999;
  tiny.add('tiny-a'); tiny.add('tiny-b'); tiny.add('tiny-c'); await flush(); expect(tiny, { running: 2, reservedBytes: 8, queued: 1 });
  assert.deepEqual(old, { running: 0, residentEntries: 0, residentBytes: 0, reservedBytes: 0, queued: 0, sources: 0 });
  for (const key of Object.keys(TINY)) for (const bad of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1])
    assert.throws(() => createThumbBudget({ limits: { ...TINY, [key]: bad } }), RangeError);
  assert.throws(() => createThumbBudget({ limits: { M: 10 * MiB + 1 } }), RangeError);
  assert.throws(() => createThumbBudget({ limits: { B: 3, M: 4 } }), RangeError);
  for (const limits of [null, 1, 'bad']) assert.throws(() => createThumbBudget({ limits }), TypeError);
  assert.equal(createThumbBudget({ limits: { C: 4, E: 1 } }).stats.running, 0);
  assert.equal(r.budget.reset, undefined); assert.equal(r.budget.release, undefined);
});

test('WEB-MEM-3 T2a inputs_and_live_duplicate_keys', async t => {
  const r = rig(t), noop = () => {}, valid = { key: 'cache/hash/g1', priority: 'automatic', onStart: noop, onCancel: noop };
  for (const patch of [{ key: '' }, { key: 1 }, { priority: 'viewer' }, { onStart: null }, { onCancel: null }])
    assert.throws(() => r.budget.enqueue({ ...valid, ...patch }), TypeError);
  assert.throws(() => r.budget.enqueue(null), TypeError);
  for (const key of ['', 1]) assert.throws(() => r.budget.claimSource(key), TypeError);
  assert.throws(() => r.budget.claimSource('owner', { onCapacity: 1 }), TypeError);
  expect(r, { running: 0, queued: 0, sources: 0 });
  const a = r.add(valid.key), g2 = r.add('cache/hash/g2'); await flush();
  let denied = 0;
  const duplicate = () => r.budget.enqueue({ ...valid, onStart: () => denied++, onCancel: () => denied++ });
  assert.equal(duplicate(), null); finish(a, 0); assert.equal(duplicate(), null);
  const waiting = r.add('waiting'); expect(r, { queued: 0 });
  const queued = r.add('queued'); assert.equal(r.add('queued'), null);
  const permit = r.budget.claimSource(valid.key); assert.ok(permit); assert.equal(r.budget.claimSource(valid.key), null);
  a.dropPayload(); const successor = r.add(valid.key); assert.ok(successor);
  finish(g2); await flush(); finish(waiting); await flush(); finish(queued); await flush(); finish(successor);
  permit.drop(); assert.ok(r.budget.claimSource(valid.key)); await flush(); assert.equal(denied, 0);
});

test('WEB-MEM-3 T2a request_reserves_M_before_start', async t => {
  for (const limits of [{ B: 40, C: 1, E: 8 }, { B: 4, C: 4, E: 8 }]) {
    const r = rig(t, limits), seen = [];
    const a = r.add('a', 'automatic', { start: () => seen.push(r.budget.stats) }), b = r.add('b'); await flush();
    expect(r, { running: 1, reservedBytes: 4, queued: 1 }); assert.equal(seen[0].reservedBytes, 4);
    a.dropPayload(); expect(r, { running: 1, queued: 1 }); finish(a, 0); await flush();
    assert.ok(r.started.has('b')); finish(b); a.dropPayload(); expect(r, { running: 0, residentEntries: 0 });
  }
});

test('WEB-MEM-3 T2a entry_limit_counts_zero_byte_residents', async t => {
  const r = rig(t, { B: 40, C: 4, E: 1 }), zero = r.add('zero'); await flush(); finish(zero, 0);
  const next = r.add('next'); await flush(); expect(r, { running: 0, residentEntries: 1, residentBytes: 0, queued: 1 });
  zero.setPriority('explicit'); zero.cancel(); expect(r, { residentEntries: 1 });
  assert.deepEqual(r.trace, ['start:zero']); zero.dropPayload(); await flush();
  assert.ok(r.started.has('next')); finish(next, null); expect(r, { running: 0, residentEntries: 0 });
  const last = r.add('last'); await flush(); assert.ok(r.started.has('last')); finish(last, 0); last.dropPayload();
});

test('WEB-MEM-3 T2a settlement_validation_and_first_result', async t => {
  const r = rig(t, { C: 1 }), a = r.add('a'), queued = r.add('queued'); await flush();
  const before = r.budget.stats;
  for (const residentBytes of [-1, 1.5, NaN, Infinity, 5, Number.MAX_SAFE_INTEGER + 1]) {
    assert.throws(() => finish(a, residentBytes), RangeError); assert.deepEqual(r.budget.stats, before);
  }
  for (const shape of [undefined, null, {}]) { assert.throws(() => a.settle(shape), TypeError); assert.deepEqual(r.budget.stats, before); }
  assert.throws(() => finish(queued, 0), Error); assert.deepEqual(r.budget.stats, before);
  assert.throws(() => queued.setPriority('viewer'), TypeError); assert.deepEqual(r.budget.stats, before);
  assert.equal(finish(a, 4), undefined); await flush(); expect(r, { residentBytes: 4, residentEntries: 1, running: 1 });
  assert.equal(finish(a, 0), undefined); finish(a, null); expect(r, { residentBytes: 4 });
  finish(queued, null); finish(queued, 4); expect(r, { running: 0, residentEntries: 1 }); a.dropPayload();
});

test('WEB-MEM-3 T2a cancelled_running_counts_until_actual_settle', async t => {
  // Each row isolates one admission constraint; cancellation must not credit any.
  for (const limits of [{ B: 40, C: 1, E: 8 }, { B: 4, C: 4, E: 8 }, { B: 40, C: 4, E: 1 }]) {
    const r = rig(t, limits), a = r.add('a'), waiting = r.add('waiting'); await flush();
    let release; const gate = new Promise(resolve => { release = resolve; });
    const task = gate.then(() => finish(a, 4));
    try {
      assert.equal(a.cancel(), undefined); a.cancel(); a.dropPayload(); await flush();
      expect(r, { running: 1, reservedBytes: 4, residentEntries: 0, queued: 1 });
      assert.deepEqual(r.trace, ['start:a', 'cancel:a:cancelled']); assert.ok(!r.started.has('waiting'));
    } finally { release(); await task; await flush(); }
    assert.ok(r.started.has('waiting')); expect(r, { running: 1, residentBytes: 0, residentEntries: 0 }); finish(waiting);
    const zero = r.add('cancelled-zero'); await flush(); zero.cancel(); finish(zero, 0); expect(r, { running: 0, residentEntries: 0 });
  }
});

test('WEB-MEM-3 T2a many_mounts_share_stalled_budget', async t => {
  for (const reverse of [false, true]) {
    const r = rig(t), old = [r.add('old/hash/g1'), r.add('old/hash/g2')]; await flush(); old.forEach(x => x.cancel());
    for (let mount = 0; mount < 30; mount++) {
      const ticket = r.add(`mount${mount}/hash/g1`); assert.ok(ticket); ticket.cancel();
      expect(r, { running: 2, reservedBytes: 8, queued: 0 }); assert.equal(r.started.size, 2);
    }
    const current = r.add('current/hash/g1'); const order = reverse ? [...old].reverse() : old;
    finish(order[0], 4); await flush(); assert.ok(r.started.has('current/hash/g1')); expect(r, { running: 2, residentBytes: 0 });
    finish(order[1], 0); await flush(); const snapshot = r.budget.stats;
    old.forEach(x => { finish(x, 4); x.dropPayload(); x.cancel(); }); assert.deepEqual(r.budget.stats, snapshot);
    finish(current, 0); current.dropPayload(); expect(r, { running: 0, residentEntries: 0, queued: 0 });
  }
});

test('WEB-MEM-3 T2a queue_limit_and_queued_cancel', async t => {
  const r = rig(t, { B: 40, C: 1, E: 8 }), running = r.add('running'), a = r.add('a'), b = r.add('b'); await flush();
  let denied = 0;
  assert.equal(r.budget.enqueue({ key: 'denied', priority: 'automatic', onStart: () => denied++, onCancel: () => denied++ }), null);
  expect(r, { queued: 2, running: 1 }); a.cancel(); a.cancel(); a.dropPayload(); expect(r, { queued: 1, running: 1 });
  const c = r.add('c'); assert.ok(c); finish(running); await flush(); finish(b); await flush(); finish(c); await flush();
  assert.equal(denied, 0); assert.ok(!r.started.has('a'));
  assert.equal(r.trace.filter(x => x === 'cancel:a:cancelled').length, 1);
});

test('WEB-MEM-3 T2a explicit_displaces_newest_automatic_only', async t => {
  const r = rig(t, { C: 1 }), running = r.add('running'), a = r.add('a'); let observation;
  const newest = r.add('newest', 'automatic', { cancel: reason => {
    observation = { reason, stats: r.budget.stats, denied: r.add('reentrant-auto') };
  } });
  const explicit = r.add('explicit', 'explicit'); await flush(); assert.ok(explicit);
  assert.equal(observation.reason, 'displaced'); assert.equal(observation.stats.queued, 2); assert.equal(observation.denied, null);
  newest.cancel(); assert.equal(r.trace.filter(x => x.startsWith('cancel:newest:')).length, 1);
  finish(running); await flush(); assert.ok(r.started.has('explicit')); assert.ok(!r.started.has('a'));
  finish(explicit); await flush(); finish(a); assert.ok(!r.started.has('newest'));
  const hold = r.add('hold'), earlier = r.add('earlier', 'explicit'), automatic = r.add('automatic');
  const incoming = r.add('incoming', 'explicit'); await flush();
  assert.ok(incoming); assert.ok(r.trace.includes('cancel:automatic:displaced'));
  assert.ok(!r.trace.some(x => x.startsWith('cancel:earlier:')));
  finish(hold); await flush(); assert.ok(r.started.has('earlier')); assert.ok(!r.started.has('incoming'));
  finish(earlier); await flush(); finish(incoming); automatic.cancel();
});

test('WEB-MEM-3 T2a accepted_explicit_cannot_be_displaced', async t => {
  const r = rig(t, { C: 1 }), running = r.add('running'), first = r.add('first', 'explicit'), second = r.add('second', 'explicit'); await flush();
  for (const priority of ['automatic', 'explicit']) for (let i = 0; i < 4; i++) assert.equal(r.add(`${priority}${i}`, priority), null);
  expect(r, { running: 1, reservedBytes: 4, queued: 2 }); assert.deepEqual(r.trace, ['start:running']);
  running.cancel(); await flush(); assert.equal(r.started.size, 1); expect(r, { running: 1, queued: 2 });
  finish(running); await flush(); finish(first); await flush(); finish(second);
  assert.deepEqual(r.trace.filter(x => x.startsWith('start:')), ['start:running', 'start:first', 'start:second']);
});

test('WEB-MEM-3 T2a explicit_FIFO_upgrade_order', async t => {
  const r = rig(t, { C: 1, Q: 6 }), running = r.add('running'), auto = r.add('auto');
  const leader = r.add('leader', 'explicit'), e1 = r.add('e1', 'explicit');
  assert.equal(auto.setPriority('explicit'), undefined); const e2 = r.add('e2', 'explicit');
  e1.setPriority('explicit'); auto.setPriority('explicit'); e1.setPriority('automatic'); e1.setPriority('explicit');
  const lastAuto = r.add('last-auto'); await flush(); finish(running); await flush();
  for (const [key, ticket] of [['leader', leader], ['auto', auto], ['e2', e2], ['e1', e1], ['last-auto', lastAuto]]) {
    assert.equal([...r.started.keys()].at(-1), key); finish(ticket); await flush();
  }
  assert.deepEqual([...r.started.keys()], ['running', 'leader', 'auto', 'e2', 'e1', 'last-auto']);
  const hold = r.add('hold'), earlier = r.add('earlier'), later = r.add('later', 'explicit');
  later.setPriority('automatic'); await flush(); finish(hold); await flush(); assert.ok(r.started.has('earlier'));
  assert.ok(!r.started.has('later')); finish(earlier); await flush(); finish(later);
});

test('WEB-MEM-3 T2a explicit_precedes_later_automatic', async t => {
  const r = rig(t, { C: 1, Q: 6 }), hold = r.add('hold'), auto = r.add('auto'), e1 = r.add('e1', 'explicit'), e2 = r.add('e2', 'explicit');
  hold.cancel(); const later = r.add('later'); await flush(); assert.deepEqual([...r.started.keys()], ['hold']);
  finish(hold); await flush(); const arrival = r.add('arrival');
  for (const [key, ticket] of [['e1', e1], ['e2', e2], ['auto', auto], ['later', later], ['arrival', arrival]]) {
    assert.equal([...r.started.keys()].at(-1), key); finish(ticket); await flush();
  }
  expect(r, { running: 0, queued: 0 });
});

test('WEB-MEM-3 T2a payload_drop_and_terminal_handles_are_idempotent', async t => {
  const r = rig(t, { C: 1 }), old = r.add('same'), queued = r.add('queued'); await flush();
  queued.dropPayload(); old.dropPayload(); expect(r, { running: 1, queued: 1 }); queued.cancel();
  finish(old, 3); assert.equal(old.dropPayload(), undefined); old.dropPayload();
  const next = r.add('same'); await flush(); const snapshot = r.budget.stats;
  for (let i = 0; i < 5; i++) { old.cancel(); finish(old, 4); old.dropPayload(); old.setPriority('explicit'); }
  assert.deepEqual(r.budget.stats, snapshot); assert.ok(!r.trace.some(x => x.startsWith('cancel:same:')));
  finish(next, 0); expect(r, { residentEntries: 1, residentBytes: 0 }); next.dropPayload();
  const failed = r.add('failed'); await flush(); finish(failed); const replacement = r.add('failed'); await flush();
  finish(failed, 4); failed.dropPayload(); expect(r, { running: 1, residentEntries: 0 }); finish(replacement);
});

test('WEB-MEM-3 T2a source_limit_counts_occurrences', async t => {
  const r = rig(t); let denied = 0;
  const a = r.budget.claimSource('cache/hash/inline1'), b = r.budget.claimSource('cache/hash/viewer');
  assert.ok(a && b); expect(r, { sources: 2, running: 0 });
  assert.equal(r.budget.claimSource('cache/hash/inline2', { onCapacity: () => denied++ }), null);
  assert.equal(r.budget.claimSource('cache/hash/inline1', { onCapacity: () => denied++ }), null);
  a.drop(); await flush(); assert.ok(r.budget.claimSource('cache/hash/inline2')); b.drop(); await flush(); assert.equal(denied, 0);
});

test('WEB-MEM-3 T2a permit_move_drop_idempotence', async t => {
  const r = rig(t, { N: 1 }); let old = 0, moved = 0;
  const p = r.budget.claimSource('inline', { onCapacity: r.guard(() => old++) });
  assert.equal(p.move('viewer', { onCapacity: r.guard(() => moved++) }), undefined); expect(r, { sources: 1 });
  assert.equal(r.budget.claimSource('inline'), null); const a = r.add('a'); await flush(); finish(a); await flush();
  assert.equal(old, 0); assert.equal(moved, 1);
  p.move('viewer'); p.move('viewer'); const b = r.add('b'); await flush(); finish(b); await flush(); assert.equal(moved, 1);
  assert.equal(p.drop(), undefined); p.drop(); p.move('resurrect'); expect(r, { sources: 0 });
  const current = r.budget.claimSource('viewer'); p.drop(); expect(r, { sources: 1 }); current.drop();
  const two = rig(t); let retained = 0;
  const left = two.budget.claimSource('left', { onCapacity: two.guard(() => retained++) });
  const right = two.budget.claimSource('right'), snapshot = two.budget.stats;
  assert.throws(() => left.move('right'), RangeError); assert.deepEqual(two.budget.stats, snapshot);
  assert.throws(() => left.move(''), TypeError); assert.throws(() => left.move('next', { onCapacity: 1 }), TypeError);
  assert.deepEqual(two.budget.stats, snapshot); assert.equal(two.budget.claimSource('left'), null);
  right.drop(); await flush(); assert.equal(retained, 1); left.drop();
});

test('WEB-MEM-3 T2a capacity_notifications_are_coalesced_live_only', async t => {
  const r = rig(t, { N: 3 }); let old = 0, current = 0, gone = 0, denied = 0;
  const p = r.budget.claimSource('p', { onCapacity: r.guard(() => old++) });
  const dropped = r.budget.claimSource('drop', { onCapacity: r.guard(() => gone++) });
  const stable = r.budget.claimSource('stable', { onCapacity: r.guard(() => current++) });
  assert.equal(r.budget.claimSource('denied', { onCapacity: () => denied++ }), null);
  const a = r.add('a'), b = r.add('b'); await flush(); assert.equal(old + current + gone, 0);
  a.setPriority('explicit'); p.move('p', { onCapacity: r.guard(() => current++) }); await flush(); assert.equal(current, 0);
  finish(a, 0); finish(b, 0); a.dropPayload(); b.dropPayload(); dropped.drop();
  p.move('moved', { onCapacity: r.guard(() => current++) }); await flush();
  assert.equal(old, 0); assert.equal(gone, 0); assert.equal(denied, 0); assert.equal(current, 2);
  dropped.drop(); a.dropPayload(); b.dropPayload(); await flush(); assert.equal(current, 2);
  p.drop(); await flush(); assert.equal(current, 3); stable.drop(); await flush(); assert.equal(current, 3);
});

test('WEB-MEM-3 T2a callback_reentry_observes_committed_accounting', async t => {
  const r = rig(t, { C: 1, Q: 3 }), hold = r.add('hold'); let queued, reentered;
  const permit = r.budget.claimSource('inline');
  queued = r.add('queued', 'automatic', { cancel: () => {
    assert.equal(r.budget.stats.queued, 0); permit.move('viewer'); permit.drop();
    reentered = r.add('replacement'); assert.ok(reentered);
  } });
  queued.cancel(); await flush(); expect(r, { sources: 0, queued: 1, running: 1 });
  finish(hold); await flush(); assert.ok(r.started.has('replacement')); finish(reentered);
  const before = r.started.size;
  const self = r.add('self', 'automatic', { start: ticket => {
    expect(r, { running: 1, reservedBytes: 4 }); ticket.cancel(); expect(r, { running: 1 }); finish(ticket, 4);
  } }); await flush(); assert.ok(self); assert.equal(r.started.size, before + 1); expect(r, { running: 0, residentEntries: 0 });
  const blocker = r.add('blocker'), victim = r.add('victim'); let hints = 0, retry;
  const observer = r.budget.claimSource('observer', { onCapacity: r.guard(() => {
    hints++; if (!retry) retry = r.add('retry', 'explicit');
  }) });
  victim.cancel(); await flush(); assert.equal(hints, 1); assert.ok(retry); assert.ok(!r.started.has('victim'));
  finish(blocker); await flush(); assert.ok(r.started.has('retry')); observer.drop(); finish(retry);
  const anchor = r.add('anchor'); let doomed;
  const first = r.add('first', 'automatic', { start: () => { doomed.cancel(); expect(r, { queued: 0, running: 1 }); } });
  doomed = r.add('doomed'); await flush(); finish(anchor); await flush(); finish(first); await flush();
  assert.ok(!r.started.has('doomed'));
});

test('WEB-MEM-3 T2a throwing_callbacks_do_not_corrupt_ownership', async t => {
  const r = rig(t, { C: 1 }); let startedTicket, starts = 0, cancels = 0, hints = 0;
  const thrown = new Error('synthetic callback failure');
  let ticket;
  assert.doesNotThrow(() => { ticket = r.budget.enqueue({ key: 'throws', priority: 'automatic',
    onStart: x => { startedTicket = x; starts++; throw thrown; }, onCancel: () => { cancels++; throw thrown; } }); });
  await flush(); assert.equal(startedTicket, ticket); assert.equal(starts, 1);
  const next = r.add('next'); expect(r, { running: 1, reservedBytes: 4, queued: 1 });
  assert.doesNotThrow(() => ticket.cancel()); ticket.cancel(); await flush(); assert.equal(cancels, 1);
  expect(r, { running: 1, queued: 1 }); assert.ok(!r.started.has('next'));
  const bad = r.budget.claimSource('bad', { onCapacity: () => { throw thrown; } });
  const good = r.budget.claimSource('good', { onCapacity: r.guard(() => hints++) });
  assert.doesNotThrow(() => finish(ticket, 4)); await flush(); assert.ok(r.started.has('next')); assert.equal(hints, 1);
  expect(r, { running: 1, residentEntries: 0 }); bad.drop(); await flush(); assert.equal(hints, 2);
  good.drop(); finish(next); await flush(); expect(r, { running: 0, queued: 0, sources: 0 });
});
