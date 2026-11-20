import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import type { TestContext } from 'node:test';
import type { KeyValue } from '../src/backend.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { Log } from '../../hub-client/src/ports.ts';
import { createClaudeDeliveryAdapter, claudeDeliveryCanaryAllowed, claudeInboxArgs, claudeDeliverArgs, spawnClaudeDeliver, spawnClaudeInbox, CLAUDE_PENDING_LIMIT } from '../src/delivery/claude-adapter.ts';
import type { ClaudeCandidatePage, ClaudeClaimRecord, ClaudeDeliveryAdapterDeps, ClaudeDeliveryFrame, ClaudeDeliveryScopeCoordinates, DeliverExit, DeliverResult } from '../src/delivery/claude-adapter.ts';
import type { WatchPool, WatchRequest, WakeEvent, WatchEnd } from '../src/delivery/watch-pool.ts';

// Task 7 (claude delivery chain): the hosted native pointer-delivery adapter.
// Every test drives the adapter through injected fakes only: no hub, no CLI
// child, no socket. The transport is a recording fake; the claim ledger is an
// in-memory kv. The one real invariant under test everywhere: exit 0 is
// write-completed and never presentation proof, and nothing ever acks.

const KEY = 'ab'.repeat(32), GEN = '01'.repeat(16), OTHER_GEN = '02'.repeat(16);
const THREAD = 'thr_01ARZ3NDEKTSV4RRFFQ69G5FAV';
// The binding config must satisfy claudeConfigPathValid on every platform
// the suite may run (or be faked) on: drive-absolute on win32, POSIX-absolute
// elsewhere. Every derived path follows the same rule.
const BIND_CONFIG = process.platform === 'win32' ? 'C:\\bind\\config.toml' : '/bind/config.toml';
const MOVED_CONFIG = process.platform === 'win32' ? 'C:\\bind\\moved.toml' : '/bind/moved.toml';
const HUB_PIN = 'ed25519:IVL40Zt5HSRFMkLhXy6rbLfP-ntqXtMAl5YOBpiB2xI';

function memoryKv() {
  const map = new Map<string, string>();
  const kv: KeyValue = {
    get: k => map.get(k),
    set: (k, v) => { map.set(k, v); },
    delete: k => { map.delete(k); },
    keys: (p = '') => [...map.keys()].filter(k => k.startsWith(p)),
  };
  return {
    kv,
    raw: map,
    claims: () => [...map.keys()].filter(k => k.startsWith('claude-claims:claim:'))
      .map(k => JSON.parse(map.get(k)!) as ClaudeClaimRecord),
    claim: (deliveryId: string) => {
      const raw = map.get('claude-claims:claim:' + deliveryId);
      return raw === undefined ? undefined : JSON.parse(raw) as ClaudeClaimRecord;
    },
    scope: (key: string) => {
      const raw = map.get('claude-claims:scope:' + key);
      return raw === undefined ? undefined : JSON.parse(raw) as Record<string, unknown>;
    },
  };
}

type PoolRecording = {
  requests: WatchRequest[];
  notify(scope: string, event: WakeEvent): void;
  end(scope: string, end: WatchEnd): void;
  /** Subscriptions not yet released. */
  active(): number;
};
function fakePool() {
  const requests: WatchRequest[] = [];
  const subs = new Map<string, { notify: (e: WakeEvent) => void; stopped: (e: WatchEnd) => void }>();
  const live = new Set<object>(); // One token per unreleased subscription.
  const pool: WatchPool = {
    subscribe(request, notify, stopped) {
      requests.push(request);
      const entry = { notify, stopped };
      subs.set(request.scope.scope, entry);
      live.add(entry);
      return () => {
        live.delete(entry);
        if (subs.get(request.scope.scope) === entry) subs.delete(request.scope.scope);
      };
    },
    async close() { /* nothing owned */ },
  };
  const recording: PoolRecording = {
    requests,
    notify: (scope, event) => { const s = subs.get(scope); if (s) s.notify(event); },
    end: (scope, end) => { const s = subs.get(scope); if (s) s.stopped(end); },
    active: () => live.size,
  };
  return { pool, recording };
}

function admittedRow(over: Record<string, unknown> = {}) {
  return {
    binding_key: KEY, generation: GEN, state: 'registered',
    admission: { live: true, policy: true, version_qualified: true, canary: true, witness: true },
    scope: { profile: 'p', config: BIND_CONFIG, hub_identity: HUB_PIN, hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' },
    ...over,
  };
}

/** 26-digit pseudo-ULID: digits are valid Crockford, so ids match the
 * adapter's strict shapes. */
function ulid(n: number): string { return ('00000000000000000000000000' + String(n)).slice(-26); }

function deliveryEvent(cursor: string, n: number, over: Record<string, unknown> = {}) {
  return {
    cursor, type: 'delivery', deliveryId: 'dly_' + ulid(n), messageId: 'msg_' + ulid(n), threadId: THREAD,
    priority: 'wake', kind: 'message', ...over,
  };
}

interface Fixture {
  adapter: ReturnType<typeof createClaudeDeliveryAdapter>;
  kv: ReturnType<typeof memoryKv>;
  pool: ReturnType<typeof fakePool>;
  log: ReturnType<typeof memoryLog>;
  clock: ReturnType<typeof fakeClock>;
  transportCalls: ClaudeDeliveryFrame[];
  setSnapshot(bindings: unknown[]): void;
  setSnapshotUnavailable(): void;
  setPages(pages: ClaudeCandidatePage[]): void;
  setExit(exit: DeliverExit): void;
}

/** Most tests exercise staging on an already-running adapter: every
 * admission (including the first pass of this process) baselines at head,
 * so the fixture seeds a progress record at `e1.0`, its candidate source
 * answers every `head` read with an empty page at `e1.0`, and the first
 * (establishing) pass stages nothing. A test's pages are served to the
 * reads after it: trigger one with a wake through the pool or the reload
 * tick. `{ baselined: false }` starts from no record for tests that drive
 * the baseline reads themselves. */
function fixture(t: TestContext, deps: Partial<ClaudeDeliveryAdapterDeps> = {}, opts: { baselined?: boolean } = {}): Fixture {
  const clock = fakeClock(1000), log = memoryLog(), store = memoryKv();
  if (opts.baselined !== false) {
    store.kv.set('claude-claims:scope:' + KEY, JSON.stringify({ v: 1, generation: GEN, cursor: 'e1.0', blockedPendingLimit: false, updatedAt: 1000 }));
  }
  const { pool, recording } = fakePool();
  const transportCalls: ClaudeDeliveryFrame[] = [];
  let exit: DeliverExit = 0;
  let snapshot: unknown = { version: 1, bindings: [admittedRow()] };
  // The adapter re-validates every row; the fixture's raw shape is the point.
  let pages: ClaudeCandidatePage[] = [];
  const fixtureCandidates = async (_c: ClaudeDeliveryScopeCoordinates, cursor: string | undefined): Promise<ClaudeCandidatePage> =>
    cursor === 'head' ? { events: [], nextCursor: 'e1.0' } : pages.shift() ?? { events: [] };
  const adapter = createClaudeDeliveryAdapter({
    pool, clock, log, kv: store.kv, env: { HOME: '/nonexistent', VECNET_BIN: '/nonexistent/vecnet' },
    fetchSnapshot: async () => snapshot as import('../src/delivery/claude-status.ts').ClaudeSnapshot,
    fetchCandidates: fixtureCandidates,
    transport: async frame => { transportCalls.push(frame); return exit; },
    fetchAckWindow: async () => undefined, // No observation unless a test supplies one.
    ...deps,
  });
  t.after(() => adapter.stop());
  return {
    adapter, kv: store, pool: { pool, recording }, log, clock, transportCalls,
    setSnapshot: bindings => { snapshot = { version: 1, bindings }; },
    setSnapshotUnavailable: () => { snapshot = undefined; },
    setPages: next => { pages = next; },
    setExit: value => { exit = value; },
  };
}

async function settle(ms = 20): Promise<void> { await new Promise(resolve => setTimeout(resolve, ms)); }

test('admission gating: non-live and non-qualified bindings never reach the transport', async t => {
  const f = fixture(t);
  f.setSnapshot([
    admittedRow({ binding_key: 'cd'.repeat(32), admission: { live: false, policy: true, version_qualified: true, canary: true } }),
    admittedRow({ binding_key: 'ce'.repeat(32), admission: { live: true, policy: false, version_qualified: true, canary: true } }),
    admittedRow({ binding_key: 'cf'.repeat(32), admission: { live: true, policy: true, version_qualified: false, canary: true } }),
    admittedRow({ binding_key: 'aa'.repeat(32), state: 'opted_out' }),
  ]);
  f.adapter.start();
  await settle();
  assert.equal(f.pool.recording.requests.length, 0, 'no watch may be opened for a non-admitted scope');
  assert.equal(f.transportCalls.length, 0);
  // A wake delivered straight through the pool must still reach nothing.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  f.clock.advance(5000);
  await settle();
  assert.equal(f.transportCalls.length, 0, 'a wake for a non-admitted scope never reaches the transport');
});

test('an admitted binding subscribes one exclusive watch and delivers a wake pointer once', async t => {
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // The first pass of this process only establishes the baseline.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.pool.recording.requests.length, 1, 'exactly one exclusive watch per admitted scope');
  const request = f.pool.recording.requests[0];
  assert.equal(request.scope.scope, 'exclusive:claude:' + KEY);
  assert.equal(request.revision, GEN);
  assert.deepEqual(f.transportCalls.map(c => [c.bindingKey, c.generation, c.threadId]), [[KEY, GEN, THREAD]]);
  const claim = f.kv.claim('dly_' + ulid(1))!;
  assert.equal(claim.state, 'released');
  assert.equal(claim.disposition, 'attempted', 'exit 0 is a completed write, never presentation proof');
  assert.notEqual(claim.hubAckObserved, true, 'no hub acknowledgement is ever inferred');
  assert.match(f.kv.scope(KEY)!.cursor as string, /e1\.1/);
});

test('the attempting state is durably persisted before the transport is invoked', async t => {
  // The write-ordering pin: at the moment the transport runs, the canonical
  // kv row for the delivery under attempt must already say `attempting`.
  // Reading the ledger inside the injected transport fake is the only place
  // this is observable; a reorder (transport first, persist later) would see
  // `reserved` here instead.
  const observed: Array<string | undefined> = [];
  const f = fixture(t, {
    transport: async frame => {
      const claim = f.kv.claims().find(c => c.msgId === frame.msgId);
      observed.push(claim?.state);
      return 0;
    },
  });
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.deepEqual(observed, ['attempting'], 'the durable claim must say attempting before any byte can leave');
  assert.equal(f.kv.claim('dly_' + ulid(1))!.state, 'released', 'the attempt still settles its disposition');
});

test('the claim lifecycle is logged: admission, reserved, attempting, disposition', async t => {
  // The daemon's stderr log is the operator-facing proof chain for a native
  // delivery; every durable transition emits one opaque line (task 9). A
  // hold marker logs its inbox-only quarantine, a wake logs the reserved and
  // attempting commitments and the helper's exit disposition.
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1, { priority: 'hold' }), deliveryEvent('e1.2', 2)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  const msgs = f.log.entries.map(e => e.msg);
  const at = (needle: string) => msgs.indexOf(needle);
  const admitted = at('claude_delivery_scope_admitted');
  const held = at('claude_delivery_claim_held');
  const reserved = at('claude_delivery_claim_reserved');
  const attempting = at('claude_delivery_claim_attempting');
  const disposition = at('claude_delivery_disposition');
  assert.ok(admitted >= 0, 'the admitted scope is logged once per generation');
  assert.ok(held >= 0, 'an inbox-only hold is logged as quarantined');
  assert.ok(reserved > admitted && attempting > reserved && disposition > attempting, 'the wake chain is logged in commit order');
  const heldMeta = f.log.entries.find(e => e.msg === 'claude_delivery_claim_held')!.meta as Record<string, unknown>;
  assert.equal(heldMeta.deliveryId, 'dly_' + ulid(1));
  assert.equal(heldMeta.reason, 'inbox_only');
  const dispo = f.log.entries.find(e => e.msg === 'claude_delivery_disposition')!.meta as Record<string, unknown>;
  assert.equal(dispo.deliveryId, 'dly_' + ulid(2));
  assert.equal(dispo.state, 'released');
  assert.equal(dispo.disposition, 'attempted');
  assert.equal(dispo.exit, 0, 'the helper exit code is part of the chain');
  for (const entry of f.log.entries) {
    const meta = JSON.stringify(entry.meta ?? {});
    assert.ok(!/hello|body|secret/i.test(meta), 'log lines never carry message bodies or credentials');
  }
});

test('hold is never transported and never acked', async t => {
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1, { priority: 'hold' })] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 0, 'hold is inbox-only for the native adapter');
  const claim = f.kv.claim('dly_' + ulid(1))!;
  assert.equal(claim.state, 'held');
  assert.equal(claim.disposition, 'held');
  assert.equal(claim.dispositionReason, 'inbox_only');
  assert.notEqual(claim.hubAckObserved, true);
  // The staging checkpoint advanced past the hold without any acknowledgement.
  assert.match(f.kv.scope(KEY)!.cursor as string, /e1\.1/);
});

test('wake_idle stays on the legacy path: staged past, never transported', async t => {
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1, { priority: 'wake_idle' })] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 0, 'wake_idle is never silently degraded to a native wake');
  assert.equal(f.kv.claims().length, 0, 'wake_idle leaves no native reservation');
});

function seededClaim(over: Partial<ClaudeClaimRecord>): ClaudeClaimRecord {
  return {
    v: 1, deliveryId: 'dly_seeded', messageId: 'msg_' + ulid(90), threadId: THREAD, msgId: 'n'.repeat(26),
    scope: KEY, generation: GEN, cursor: 'e1.0', predecessor: '', priority: 'wake',
    state: 'reserved', attemptId: 'a'.repeat(32), createdAt: 500, updatedAt: 500,
    ...over,
  } as ClaudeClaimRecord;
}

test('a reserved claim from a crashed run is fenced, then rescheduled with the current owner', async t => {
  const f = fixture(t);
  f.kv.kv.set('claude-claims:claim:dly_seeded', JSON.stringify(seededClaim({})));
  f.adapter.start();
  await settle();
  assert.equal(f.transportCalls.length, 1, 'a fenced reserved claim is rescheduled with the current owner');
  assert.deepEqual(f.transportCalls[0].bindingKey, KEY);
  const claim = f.kv.claim('dly_seeded')!;
  assert.equal(claim.state, 'released');
  assert.equal(claim.disposition, 'attempted');
  assert.notEqual(f.transportCalls[0].msgId, 'n'.repeat(26), 'the rescheduled attempt uses a fresh correlation id');
});

test('a reserved claim from a rotated generation is released fenced without any write', async t => {
  const f = fixture(t);
  f.kv.kv.set('claude-claims:claim:dly_seeded', JSON.stringify(seededClaim({ generation: OTHER_GEN })));
  f.adapter.start();
  await settle();
  assert.equal(f.transportCalls.length, 0, 'a fenced claim never reaches the transport');
  const claim = f.kv.claim('dly_seeded')!;
  assert.equal(claim.state, 'released');
  assert.equal(claim.dispositionReason, 'fenced_generation_changed');
});

test('a reserved claim for a vanished scope is released fenced without a write', async t => {
  const f = fixture(t);
  f.kv.kv.set('claude-claims:claim:dly_seeded', JSON.stringify(seededClaim({ scope: 'dd'.repeat(32) })));
  f.adapter.start();
  await settle();
  assert.equal(f.transportCalls.length, 0);
  const claim = f.kv.claim('dly_seeded')!;
  assert.deepEqual([claim.state, claim.disposition, claim.dispositionReason], ['released', 'released', 'fenced_scope_not_admitted']);
});

test('an attempting claim from a crashed run surfaces uncertain and is never resent', async t => {
  const f = fixture(t);
  f.kv.kv.set('claude-claims:claim:dly_seeded', JSON.stringify(seededClaim({ state: 'attempting' })));
  f.setPages([{ events: [deliveryEvent('e1.9', 9)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass: the crashed claim turns uncertain.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 1, 'only the new message is attempted, never the crashed one');
  assert.deepEqual(f.transportCalls.map(c => c.threadId), [THREAD]);
  const claim = f.kv.claim('dly_seeded')!;
  assert.equal(claim.state, 'uncertain');
  assert.equal(claim.dispositionReason, 'restart_may_have_written');
  assert.equal(claim.attemptId, 'a'.repeat(32), 'the crashed attempt identity is retained for the operator');
});

test('transport exit 3 records refused; exit 4 records uncertain', async t => {
  const refused = fixture(t);
  refused.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  refused.setExit(3);
  refused.adapter.start();
  await settle(); // Baseline-only first pass.
  refused.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  const refusedClaim = refused.kv.claim('dly_' + ulid(1))!;
  assert.equal(refusedClaim.state, 'refused');
  assert.equal(refusedClaim.dispositionReason, 'connect_refused_or_timeout');
  assert.notEqual(refusedClaim.hubAckObserved, true, 'a refusal is never acked either');

  const uncertain = fixture(t);
  uncertain.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  uncertain.setExit(4);
  uncertain.adapter.start();
  await settle(); // Baseline-only first pass.
  uncertain.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  const uncertainClaim = uncertain.kv.claim('dly_' + ulid(1))!;
  assert.equal(uncertainClaim.state, 'uncertain');
  assert.equal(uncertainClaim.dispositionReason, 'write_failed_or_outcome_unknown');
  // An uncertain claim from this same run is never retried by a later wake.
  uncertain.setPages([{ events: [] }]);
  uncertain.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  uncertain.clock.advance(5000);
  await settle();
  assert.equal(uncertain.transportCalls.length, 1, 'no automatic second promotion of an uncertain attempt');
});

test('a transport rejection fails closed to uncertain', async t => {
  const f = fixture(t, {
    transport: async () => { throw new Error('child lost'); },
  });
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  const claim = f.kv.claim('dly_' + ulid(1))!;
  assert.equal(claim.state, 'uncertain');
  assert.equal(claim.dispositionReason, 'transport_outcome_unknown');
});

test('a later wake after a quarantined hold delivers the new message and keeps the old tombstone', async t => {
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1, { priority: 'hold' })] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.kv.claim('dly_' + ulid(1))!.state, 'held');

  f.setPages([{ events: [deliveryEvent('e1.2', 2)] }]);
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  f.clock.advance(5000);
  await settle();
  assert.equal(f.transportCalls.length, 1, 'the new wake is delivered independently of the quarantined hold');
  assert.equal(f.kv.claim('dly_' + ulid(2))!.disposition, 'attempted');
  const held = f.kv.claim('dly_' + ulid(1))!;
  assert.equal(held.state, 'held', 'the hold tombstone stays visible');
  assert.notEqual(held.hubAckObserved, true, 'the hold was never implicitly acknowledged');
});

test('the 129th unresolved reservation blocks with blocked_pending_limit and no send', async t => {
  const f = fixture(t);
  for (let i = 0; i < 128; i++) {
    const record = seededClaim({
      deliveryId: 'dly_pending_' + i, messageId: 'msg_' + ulid(100 + i), state: 'uncertain',
      disposition: 'uncertain', dispositionReason: 'write_failed_or_partial',
    });
    f.kv.kv.set('claude-claims:claim:' + record.deliveryId, JSON.stringify(record));
  }
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 0, 'a reached cap blocks additional native sends');
  assert.equal(f.kv.claim('dly_' + ulid(1)), undefined, 'the blocked candidate leaves no reservation');
  assert.equal(f.kv.scope(KEY)!.blockedPendingLimit, true);
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_blocked_pending_limit'));
  assert.notEqual(f.kv.scope(KEY)!.cursor, 'e1.1', 'the blocked event stays staged for a later pass');
});

test('tombstones survive repeated reconcile until a confirmed hub ack', async t => {
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1, { priority: 'hold' })] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle(); // Stages the hold.
  f.setPages([{ events: [deliveryEvent('e1.2', 2)] }]);
  f.clock.advance(10_000); await settle();   // periodic refresh pass
  f.setExit(3);
  f.setPages([{ events: [deliveryEvent('e1.3', 3)] }]);
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  f.clock.advance(5000); await settle();
  f.setPages([]);
  for (let pass = 0; pass < 3; pass++) { f.clock.advance(10_000); await settle(); }

  const states = new Map(f.kv.claims().map(c => [c.deliveryId, c.state]));
  assert.deepEqual([...states.entries()].sort(), [['dly_' + ulid(1), 'held'], ['dly_' + ulid(2), 'released'], ['dly_' + ulid(3), 'refused']]);
  for (const claim of f.kv.claims()) assert.notEqual(claim.hubAckObserved, true, 'no hub ack is ever inferred');
});

test('a confirmed hub ack retires the tombstone; without one it survives', async t => {
  const f = fixture(t);
  f.kv.kv.set('claude-claims:claim:dly_acked', JSON.stringify(seededClaim({
    deliveryId: 'dly_acked', messageId: 'msg_' + ulid(80), state: 'released',
    disposition: 'attempted', dispositionReason: 'write_completed_no_presentation_proof',
    hubAckObserved: true,
  })));
  f.kv.kv.set('claude-claims:claim:dly_plain', JSON.stringify(seededClaim({
    deliveryId: 'dly_plain', state: 'refused', disposition: 'refused',
    dispositionReason: 'connect_refused_or_timeout', hubAckObserved: false,
  })));
  f.adapter.start();
  await settle();
  assert.equal(f.kv.claim('dly_acked'), undefined, 'a confirmed hub ack observation retires the tombstone at reconcile');
  assert.notEqual(f.kv.claim('dly_plain'), undefined, 'without a confirmed ack the tombstone survives (fail closed)');
});

test('stop fences new work and drains an in-flight transport', async t => {
  let released!: (value: DeliverExit) => void;
  const release = new Promise<DeliverExit>(resolve => { released = resolve; });
  let seen = 0;
  const f = fixture(t, {
    transport: async frame => { seen++; f.transportCalls.push(frame); return release; },
  });
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(seen, 1);

  const stopped = f.adapter.stop();
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  f.setPages([{ events: [deliveryEvent('e1.2', 2)] }]);
  f.clock.advance(5000);
  await settle(10);
  assert.equal(seen, 1, 'a wake after stop never starts another attempt');
  released(0);
  await stopped;
  assert.equal(f.kv.claim('dly_' + ulid(1))!.disposition, 'attempted', 'the drained attempt settles its disposition');
  await f.adapter.stop();
});

test('an unknown or corrupt inbox event stalls the window instead of being skipped', async t => {
  const f = fixture(t);
  f.setPages([{ events: [
    deliveryEvent('e1.1', 1),
    deliveryEvent('e1.2', 2, { priority: 'flash' }),
    deliveryEvent('e1.3', 3),
  ] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 1, 'events before the unknown one are delivered in order');
  assert.equal(f.kv.claim('dly_' + ulid(3)), undefined, 'nothing past the unknown event is staged');
  assert.notEqual(f.kv.scope(KEY)!.cursor, 'e1.3', 'the checkpoint does not advance past an unknown event');
});

test('non-message kinds and non-delivery events never become native instructions', async t => {
  const f = fixture(t);
  f.setPages([{ events: [
    { cursor: 'e1.0', type: 'receipt', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
    deliveryEvent('e1.1', 1, { kind: 'approval_request' }),
    deliveryEvent('e1.2', 2),
  ] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.deepEqual(f.transportCalls.map(c => c.threadId), [THREAD], 'only the ordinary message is delivered');
  assert.equal(f.kv.claims().length, 1);
});

test('child argument builders carry no ack, no display proof and no raw endpoint coordinates', () => {
  const inbox = claudeInboxArgs({ profile: 'p', config: BIND_CONFIG, hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' }, 'e1.4');
  assert.deepEqual(inbox, ['--net', 'p', 'inbox', '--json', '--limit', '32', '--no-display-proof', '--since', 'e1.4']);
  const fresh = claudeInboxArgs({ profile: 'p', as: 'peer_B', config: BIND_CONFIG, hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_B' }, undefined);
  assert.deepEqual(fresh, ['--net', 'p', '--as', 'peer_B', 'inbox', '--json', '--limit', '32', '--no-display-proof']);
  const deliver = claudeDeliverArgs({ bindingKey: KEY, generation: GEN, msgId: 'n'.repeat(26), threadId: THREAD, messageId: 'msg_01E8YQZ5N8W3C4R6T7A9A2B4CD' });
  assert.deepEqual(deliver, ['claude', 'deliver', '--binding-key', KEY, '--expect-generation', GEN,
    '--msg-id', 'n'.repeat(26), '--thread-id', THREAD, '--message-id', 'msg_01E8YQZ5N8W3C4R6T7A9A2B4CD']);
});

test('the canary env var is a kill switch only: =0 refuses, nothing else enables beyond the durable record', () => {
  assert.equal(claudeDeliveryCanaryAllowed({ VECNET_CLAUDE_DELIVERY_CANARY: '0' }), false);
  for (const value of [undefined, '', '1', 'pointer', 'off', 'false']) {
    assert.equal(claudeDeliveryCanaryAllowed(value === undefined ? {} : { VECNET_CLAUDE_DELIVERY_CANARY: value }), true,
      `${String(value)} is not the kill switch; the durable canary record stays the gate`);
  }
});

test('an unrecognized canary env value warns once and is not a kill switch', () => {
  const log = memoryLog();
  for (const value of ['off', 'false', 'off']) assert.equal(claudeDeliveryCanaryAllowed({ VECNET_CLAUDE_DELIVERY_CANARY: value }, log), true);
  for (const value of ['', '0', '1']) claudeDeliveryCanaryAllowed({ VECNET_CLAUDE_DELIVERY_CANARY: value }, log);
  claudeDeliveryCanaryAllowed({}, log);
  assert.equal(log.entries.filter(e => e.msg === 'claude_delivery_canary_env_unrecognized').length, 1, 'warned exactly once per process');
  assert.doesNotMatch(JSON.stringify(log.entries), /off|false/, 'the raw value is never logged');
});

test('a caller that is not a peer credential is never admitted (defence in depth)', async t => {
  const f = fixture(t);
  f.setSnapshot([admittedRow({ scope: { profile: 'p', hub_endpoint: 'https://hub.example', caller: 'prn_A', recipient: 'prn_A' } })]);
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle();
  assert.equal(f.pool.recording.requests.length, 0);
  assert.equal(f.transportCalls.length, 0);
  assert.equal(f.adapter.admitted().size, 0);
});

test('only the single canary-opted peer scope is admitted', async t => {
  const unopted = admittedRow({ admission: { live: true, policy: true, version_qualified: true, canary: false } });
  const missing = admittedRow({ admission: { live: true, policy: true, version_qualified: true } });
  const delegated = admittedRow({ scope: { profile: 'p', as: 'peer_B', hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_B' } });
  // `as` naming the caller itself is still a delegated scope: refused on `as` alone.
  const selfDelegated = admittedRow({ scope: { profile: 'p', as: 'peer_A', hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' } });
  const notOwnInbox = admittedRow({ scope: { profile: 'p', hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_B' } });
  const twoCanaries = [admittedRow(), admittedRow({ binding_key: 'cd'.repeat(32), generation: OTHER_GEN })];
  for (const [name, rows] of [['canary false', [unopted]], ['canary absent', [missing]], ['delegated as', [delegated]], ['self-delegated as', [selfDelegated]],
    ['recipient is not the caller', [notOwnInbox]], ['two canary rows', twoCanaries]] as const) {
    const f = fixture(t);
    f.setSnapshot([...rows]);
    f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
    f.adapter.start();
    await settle();
    f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
    f.clock.advance(15_000);
    await settle();
    assert.equal(f.pool.recording.requests.length, 0, `${name}: no watch subscription`);
    assert.equal(f.transportCalls.length, 0, `${name}: no transport call`);
    assert.equal(f.kv.claims().length, 0, `${name}: no claim staged`);
    assert.equal(f.adapter.admitted().size, 0, `${name}: nothing reported admitted`);
    const ambiguous = f.log.entries.filter(e => e.msg === 'claude_delivery_canary_ambiguous');
    assert.equal(ambiguous.length, name === 'two canary rows' ? 1 : 0, `${name}: ambiguity logged exactly when two rows qualify (once)`);
    await f.adapter.stop();
  }

  // The single opted scope is admitted and reported with its generation;
  // stop clears the report.
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.deepEqual([...f.adapter.admitted()], [[KEY, GEN]]);
  assert.equal(f.transportCalls.length, 1);
  // A later snapshot without the opt-in withdraws admission and the watch.
  f.setSnapshot([unopted]);
  f.clock.advance(10_000);
  await settle();
  assert.equal(f.adapter.admitted().size, 0, 'a withdrawn opt-in is no longer reported admitted');
  f.setSnapshot([admittedRow()]);
  f.clock.advance(10_000);
  await settle();
  assert.equal(f.adapter.admitted().size, 1);
  // An unreadable snapshot reports nothing admitted.
  f.setSnapshotUnavailable();
  f.clock.advance(10_000);
  await settle();
  assert.equal(f.adapter.admitted().size, 0, 'an unproven pass reports nothing admitted');
  await f.adapter.stop();
  assert.equal(f.adapter.admitted().size, 0);
});

test('claim records carry no body and no credentials', async t => {
  const f = fixture(t);
  const secret = 'SENSITIVE-BODY-CONTENT';
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  const raw = f.kv.kv.get('claude-claims:claim:dly_' + ulid(1))!;
  assert.ok(!raw.includes(secret));
  const claim = JSON.parse(raw) as ClaudeClaimRecord;
  assert.deepEqual(Object.keys(claim).sort(), ['attemptId', 'createdAt', 'cursor', 'deliveryId', 'disposition', 'dispositionReason', 'generation', 'hubAckObserved', 'messageId', 'msgId', 'predecessor', 'priority', 'scope', 'state', 'threadId', 'updatedAt', 'v']);
  assert.equal(typeof claim.msgId, 'string');
});

test('an unknown stream event type stalls the window', async t => {
  const f = fixture(t);
  f.setPages([{ events: [
    { cursor: 'e1.0', type: 'mystery', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
  ] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 0, 'an unknown event type is never skipped past');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e1.0', 'the checkpoint does not advance past an unknown event type');
});

test('peer-visible non-delivery events (reaction, hub_notice, operator_view) advance the window', async t => {
  // The hub appends reaction, hub_notice and operator_view rows to peer logs
  // (reactions.go, notices.go, operator_view.go); one of them ahead of a wake
  // must never wedge the scope: the cursor stages past every known
  // non-delivery type so the wake behind it still delivers.
  const f = fixture(t);
  f.setPages([{ events: [
    { cursor: 'e1.1', type: 'reaction', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
    { cursor: 'e1.2', type: 'hub_notice', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
    { cursor: 'e1.3', type: 'operator_view', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
    deliveryEvent('e1.4', 1),
  ] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.deepEqual(f.transportCalls.map(c => c.threadId), [THREAD], 'exactly the wake behind the non-delivery events is delivered');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e1.4', 'the checkpoint advances past known non-delivery events');
});

test('an operator resolution landing mid-attempt is never overwritten by the adapter', async t => {
  // `claude resolve --keep-inbox` may fire while one attempt is in flight.
  // The adapter must reload the durable row before recording its disposition:
  // a foreign write (the operator's held row) wins and the stale in-memory
  // copy is dropped, never saved over it.
  let release!: (value: DeliverExit) => void;
  const gate = new Promise<DeliverExit>(resolve => { release = resolve; });
  const f = fixture(t, {
    transport: async frame => { f.transportCalls.push(frame); return gate; },
  });
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 1, 'the attempt is in flight');
  const id = 'dly_' + ulid(1);
  const held = { ...f.kv.claim(id)!, state: 'held', disposition: 'held',
    dispositionReason: 'operator_keep_inbox', updatedAt: 999_999 } as ClaudeClaimRecord;
  f.kv.kv.set('claude-claims:claim:' + id, JSON.stringify(held)); // the operator's write lands now
  release(0);
  await settle();
  const after = f.kv.claim(id)!;
  assert.equal(after.state, 'held', 'the operator resolution survives the in-flight attempt');
  assert.equal(after.dispositionReason, 'operator_keep_inbox');
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_claim_superseded'), 'the superseded write is surfaced');
});

// Phase 2 task 4: the helper's exit contract is 0 full write, 3 connect
// failed, 5 refused before any write, 4 write failure or unknown outcome.
// Exit 2 is flag parsing only, which for a correctly built argv can only
// mean an unrecoverable runtime fatal (Go exits 2 on a fatal panic) — the
// outcome is unknown, so it is never a refusal.

const unixChild = { skip: process.platform === 'win32' && 'POSIX shell stub child' };

/** A stub `vecnet` that runs `body` for the deliver child. */
function deliverStub(t: TestContext, body: string): string {
  const dir = mkdtempSync(join(tmpdir(), 'vn-claude-deliver-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = join(dir, 'vecnet');
  writeFileSync(bin, '#!/bin/sh\n' + body + '\n', { mode: 0o700 });
  return bin;
}

const frame: ClaudeDeliveryFrame = { bindingKey: KEY, generation: GEN, msgId: 'n'.repeat(32), threadId: THREAD, messageId: 'msg_' + ulid(1) };

async function spawnExit(t: TestContext, body: string | undefined, drive?: (clock: ReturnType<typeof fakeClock>) => Promise<void>): Promise<DeliverResult> {
  const clock = fakeClock(1000);
  const bin = body === undefined ? join(tmpdir(), 'vn-no-such-dir-' + randomUUID(), 'vecnet') : deliverStub(t, body);
  const pending = spawnClaudeDeliver({ HOME: tmpdir(), PATH: process.env.PATH, VECNET_BIN: bin }, clock)(frame);
  if (drive) await drive(clock);
  return pending;
}

test('helper exit 2, signals and unmapped codes are uncertain, never refused', unixChild, async t => {
  assert.equal(await spawnExit(t, 'exit 2'), 4, 'exit 2 (a fatal runtime panic) is uncertain');
  assert.equal(await spawnExit(t, 'kill -KILL $$'), 4, 'a signal death is uncertain');
  assert.equal(await spawnExit(t, 'exit 1'), 4, 'an unmapped code is uncertain');
  assert.equal(await spawnExit(t, 'exit 7'), 4, 'an unmapped code is uncertain');
  assert.equal(await spawnExit(t, 'exec sleep 30', async clock => {
    await settle(100); // the child is running
    clock.advance(5000);
  }), 4, 'a child timeout is uncertain');
  assert.equal(await spawnExit(t, 'exit 4'), 4);
  assert.equal(await spawnExit(t, 'exit 0'), 0);
  assert.equal(await spawnExit(t, 'exit 3'), 3);

  // The adapter maps anything outside 0/3/5 — including a stray 2 from an
  // injected transport — to uncertain with the unknown-outcome reason.
  for (const exit of [2, 4, 9] as number[]) {
    const f = fixture(t);
    f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
    f.setExit(exit as DeliverExit);
    f.adapter.start();
    await settle(); // Baseline-only first pass.
    f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
    await settle();
    const claim = f.kv.claim('dly_' + ulid(1))!;
    assert.equal(claim.state, 'uncertain', `exit ${exit} is uncertain`);
    assert.equal(claim.disposition, 'uncertain');
    assert.equal(claim.dispositionReason, 'write_failed_or_outcome_unknown');
  }
});

test('exit 5 is a definite pre-write refusal', unixChild, async t => {
  assert.equal(await spawnExit(t, 'exit 5'), 5, 'the helper refusal passes through');
  assert.equal(await spawnExit(t, undefined), 5, 'a child that never spawned wrote nothing');
  // An error event on a child that DID spawn (a failed kill on the timeout
  // emits one) is never a refusal: that child may be mid-write.
  let child: ChildProcess | undefined;
  const capture = ((file: string, args: readonly string[], options: object) => {
    child = spawn(file, args, options);
    return child;
  }) as typeof spawn;
  const bin = deliverStub(t, 'exec sleep 30');
  const pending = spawnClaudeDeliver({ HOME: tmpdir(), PATH: process.env.PATH, VECNET_BIN: bin }, fakeClock(1000), capture)(frame);
  t.after(() => { child?.kill('SIGKILL'); });
  await new Promise<void>((resolve, reject) => { child!.once('spawn', resolve); child!.once('error', reject); });
  assert.ok(child!.pid, 'the stub child is running');
  child!.emit('error', Object.assign(new Error('kill EPERM'), { code: 'EPERM', syscall: 'kill' }));
  assert.equal(await pending, 4, 'an error after spawn is uncertain, never refused_before_write');

  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.setExit(5);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  const claim = f.kv.claim('dly_' + ulid(1))!;
  assert.equal(claim.state, 'refused');
  assert.equal(claim.disposition, 'refused');
  assert.equal(claim.dispositionReason, 'refused_before_write');
  assert.notEqual(claim.hubAckObserved, true, 'a refusal is never acked');
});

test('a refusal surfaces the helper stderr, bounded, in the refused_before_write log line', unixChild, async t => {
  // The helper's own stderr says why it refused. The transport captures up
  // to 512 bytes of it; the adapter logs it trimmed to a single line with
  // the refused_before_write disposition, while the durable claim keeps only
  // its fixed reason — the stderr never enters a claim record or a sidecar.
  const bin = deliverStub(t, 'printf "vecnet: claude deliver: the recipient opted out\\nvecnet: claude deliver: see claude status\\n" >&2\nexit 5');
  const direct = await spawnClaudeDeliver({ HOME: tmpdir(), PATH: process.env.PATH, VECNET_BIN: bin }, fakeClock(1000))(frame);
  assert.deepEqual(direct, { exit: 5, stderr: 'vecnet: claude deliver: the recipient opted out\nvecnet: claude deliver: see claude status\n' },
    'the raw capture keeps the helper bytes');

  const overflow = deliverStub(t, `printf '%0.sx' $(seq 1 600) >&2\nexit 5`);
  const capped = await spawnClaudeDeliver({ HOME: tmpdir(), PATH: process.env.PATH, VECNET_BIN: overflow }, fakeClock(1000))(frame);
  assert.equal(typeof capped === 'number' ? capped : capped.stderr.length, 512, 'the capture is bounded at 512 bytes');

  const f = fixture(t, {
    transport: async frame2 => { f.transportCalls.push(frame2); return { exit: 5, stderr: 'vecnet: claude deliver: the recipient opted out\nvecnet: claude deliver: see claude status\n' }; },
  });
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  const claim = f.kv.claim('dly_' + ulid(1))!;
  assert.equal(claim.state, 'refused');
  assert.equal(claim.dispositionReason, 'refused_before_write', 'the claim reason is unchanged');
  assert.ok(!JSON.stringify(claim).includes('opted out'), 'the helper stderr never enters the durable claim');
  const line = f.log.entries.find(e => e.msg === 'claude_delivery_disposition')!;
  assert.equal((line.meta as Record<string, unknown>).helperStderr,
    'vecnet: claude deliver: the recipient opted out vecnet: claude deliver: see claude status',
    'the log line carries the refusal reason trimmed to one line');
});

// Phase 2 task 5: the host's scoped children run against the binding's own
// config, and a scope's first admission (no progress record, a new
// generation, or an empty cursor) baselines at the hub head and promotes
// only later arrivals. Earlier unacked deliveries stay on the legacy path.

test('children run against the binding config, not the daemon default', unixChild, async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-claude-config-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = join(dir, 'vecnet');
  const envLog = join(dir, 'envs.log');
  // Every child records its argv and VECNET_CONFIG; the inbox answers one
  // empty page at a fixed head, the watch idles.
  writeFileSync(bin, [
    '#!/bin/sh',
    `printf '%s|%s\\n' "$VECNET_CONFIG" "$*" >> ${JSON.stringify(envLog)}`,
    'case "$*" in',
    `  *inbox*) printf '%s\\n' '{"events":[],"next_cursor":"e1.7"}' ;;`,
    '  *watch*) exec sleep 30 ;;',
    'esac',
    '',
  ].join('\n'), { mode: 0o700 });
  const clock = fakeClock(1000), log = memoryLog(), store = memoryKv();
  // One settled tombstone in the scope makes an ack observation due, so the
  // ack-window read runs as a child too.
  store.kv.set('claude-claims:claim:dly_seeded', JSON.stringify(seededClaim({ state: 'released', disposition: 'attempted',
    dispositionReason: 'write_completed_no_presentation_proof' })));
  const { createWatchPool } = await import('../src/delivery/watch-pool.ts');
  const pool = createWatchPool({ log });
  t.after(() => pool.close());
  const adapter = createClaudeDeliveryAdapter({
    pool, clock, log, kv: store.kv,
    env: { HOME: dir, PATH: process.env.PATH, VECNET_BIN: bin, VECNET_CONFIG: '/other' },
    fetchSnapshot: async () => ({ version: 1, bindings: [admittedRow()] }) as unknown as import('../src/delivery/claude-status.ts').ClaudeSnapshot,
    transport: async () => 0,
  });
  t.after(() => adapter.stop());
  adapter.start();
  const lines = () => { try { return readFileSync(envLog, 'utf8').trim().split('\n').filter(Boolean); } catch { return []; } };
  const ackRead = (l: string) => l.includes(' inbox ') && l.includes('--limit 200') && !l.includes('--since');
  for (let i = 0; i < 200 && !(lines().some(l => l.includes('inbox')) && lines().some(l => l.includes('watch')) && lines().some(ackRead)); i++) await settle(25);
  const seen = lines();
  assert.ok(seen.some(l => l.includes(' inbox ') || l.includes('|--net p inbox')), `an inbox child ran: ${seen}`);
  assert.ok(seen.some(ackRead), `an ack-window read ran: ${seen}`);
  assert.ok(seen.some(l => l.includes('watch')), `a watch child ran: ${seen}`);
  for (const line of seen) {
    assert.equal(line.split('|')[0], BIND_CONFIG, `a child ran against the daemon default: ${line}`);
  }
  await adapter.stop();
});

test('first admission baselines at head and promotes only later arrivals', async t => {
  const calls: Array<string | undefined> = [];
  let pages: ClaudeCandidatePage[] = [];
  const backlog = Array.from({ length: 40 }, (_, i) => deliveryEvent('e1.' + (i + 1), i + 1));
  const f = fixture(t, {
    fetchCandidates: async (_c, cursor) => {
      calls.push(cursor);
      // The hub answers `--since head` with no events and the head cursor;
      // any other read would expose the 40-event unacked backlog.
      if (cursor === 'head') return { events: [], nextCursor: 'e1.40' };
      return pages.shift() ?? { events: cursor === undefined ? backlog : [] };
    },
  }, { baselined: false });
  f.adapter.start();
  await settle();
  assert.deepEqual(calls, ['head'], 'the first admitted pass reads only the head');
  assert.equal(f.kv.claims().length, 0, 'no claim for the pre-admission backlog');
  assert.equal(f.transportCalls.length, 0, 'no transport for the pre-admission backlog');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e1.40');
  assert.equal(f.kv.scope(KEY)!.generation, GEN);
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_scope_baselined'), 'the baseline is logged');

  // One wake after the baseline: exactly one claim, read since the head.
  pages = [{ events: [deliveryEvent('e1.41', 41)] }];
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(calls[1], 'e1.40', 'the next read continues from the baseline');
  assert.equal(f.kv.claims().length, 1, 'exactly the post-baseline arrival is claimed');
  assert.equal(f.transportCalls.length, 1);
  assert.equal(f.transportCalls[0].messageId, 'msg_' + ulid(41));
});

test('generation change rebaselines instead of replaying the window', async t => {
  const calls: Array<string | undefined> = [];
  const f = fixture(t, {
    fetchCandidates: async (_c, cursor) => {
      calls.push(cursor);
      if (cursor === 'head') return { events: [], nextCursor: 'e1.90' };
      return { events: [deliveryEvent('e1.5', 5), deliveryEvent('e1.6', 6)] };
    },
  });
  // The stored progress belongs to an older generation of this scope.
  f.kv.kv.set('claude-claims:scope:' + KEY, JSON.stringify({ v: 1, generation: OTHER_GEN, cursor: 'e1.4', blockedPendingLimit: false, updatedAt: 1000 }));
  f.adapter.start();
  await settle();
  assert.deepEqual(calls, ['head'], 'a generation change reads the head, never the old window');
  assert.equal(f.kv.claims().length, 0);
  assert.equal(f.transportCalls.length, 0);
  assert.deepEqual([f.kv.scope(KEY)!.generation, f.kv.scope(KEY)!.cursor], [GEN, 'e1.90']);

  // An empty cursor at the current generation also rebaselines.
  const g = fixture(t, {
    fetchCandidates: async (_c, cursor: string | undefined) => {
      (calls as Array<string | undefined>).push(cursor);
      return cursor === 'head' ? { events: [], nextCursor: 'e1.91' } : { events: [deliveryEvent('e1.5', 5)] };
    },
  });
  g.kv.kv.set('claude-claims:scope:' + KEY, JSON.stringify({ v: 1, generation: GEN, cursor: '', blockedPendingLimit: false, updatedAt: 1000 }));
  calls.length = 0;
  g.adapter.start();
  await settle();
  assert.deepEqual(calls, ['head'], 'an empty cursor reads the head');
  assert.equal(g.transportCalls.length, 0);
  assert.equal(g.kv.scope(KEY)!.cursor, 'e1.91');
});

test('re-admission at the same generation baselines at head and replays nothing', async t => {
  // Final-review fix: a scope that goes not-admitted -> admitted at the same
  // generation (canary disable/enable, a config-witness flap, a kill-switch
  // recovery) must not replay every wake from the gap. Native pointers are
  // for arrivals while the scope is admitted AND this adapter process runs.
  const calls: Array<string | undefined> = [];
  let pages: ClaudeCandidatePage[] = [];
  const f = fixture(t, {
    fetchCandidates: async (_c, cursor) => {
      calls.push(cursor);
      if (cursor === 'head') return { events: [], nextCursor: 'e1.43' };
      return pages.shift() ?? { events: [] };
    },
  });
  // A previous run of this scope left the cursor at e1.40; wakes 41-43 arrive
  // while the canary is off, so they are only reachable on the legacy path.
  f.kv.kv.set('claude-claims:scope:' + KEY, JSON.stringify({ v: 1, generation: GEN, cursor: 'e1.40', blockedPendingLimit: false, updatedAt: 1000 }));
  f.setSnapshot([admittedRow({ admission: { live: true, policy: true, version_qualified: true, canary: false, witness: true } })]);
  f.adapter.start();
  await settle();
  assert.equal(f.adapter.admitted().size, 0, 'the scope is not admitted');
  pages = [{ events: [deliveryEvent('e1.41', 41), deliveryEvent('e1.42', 42), deliveryEvent('e1.43', 43)] }];
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 0, 'a not-admitted scope transports nothing');
  calls.length = 0;

  // The canary returns at the SAME generation: the re-admission baselines at
  // head before any transport, so 41-43 are never replayed natively. (No
  // watch exists to carry a wake yet, so the reload tick drives the pass.)
  f.setSnapshot([admittedRow()]);
  f.clock.advance(10_000);
  await settle();
  assert.deepEqual(calls, ['head'], 'the re-admitted pass reads only the head');
  assert.equal(f.kv.claims().length, 0, 'no claim for a wake from the not-admitted gap');
  assert.equal(f.transportCalls.length, 0, 'no transport for a wake from the not-admitted gap');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e1.43');

  // A wake that arrives after the re-baseline is delivered exactly once.
  pages = [{ events: [deliveryEvent('e1.44', 44)] }];
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.transportCalls.length, 1, 'exactly the post-baseline wake is delivered');
  assert.equal(f.transportCalls[0].messageId, 'msg_' + ulid(44));
});

test('first admitted pass after adapter start baselines at head even with a stored cursor', async t => {
  // A daemon restart (or kill-switch recovery) begins with no in-memory
  // admission, so the first admitted pass re-baselines: anything that arrived
  // while this process was not running stays on the legacy path, and the
  // crash window (claim saved, progress not saved) is not re-staged.
  const calls: Array<string | undefined> = [];
  const f = fixture(t, {
    fetchCandidates: async (_c, cursor) => {
      calls.push(cursor);
      if (cursor === 'head') return { events: [], nextCursor: 'e1.7' };
      return { events: [deliveryEvent('e1.1', 1)] };
    },
  });
  // The fixture seeded a stored cursor at e1.0 for the current generation; a
  // resume from it would deliver e1.1 immediately.
  f.adapter.start();
  await settle();
  assert.deepEqual(calls, ['head'], 'the first admitted pass of this process reads only the head');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e1.7');
  assert.equal(f.kv.claims().length, 0, 'nothing from before this start is staged');
  assert.equal(f.transportCalls.length, 0, 'nothing from before this start is transported');
});

test('failed baseline read stages nothing and retries', async t => {
  const calls: Array<string | undefined> = [];
  const answers: Array<ClaudeCandidatePage | undefined> = [undefined, { events: [] }, { events: [], nextCursor: 'e1.12' }];
  const f = fixture(t, {
    fetchCandidates: async (_c, cursor) => {
      calls.push(cursor);
      if (cursor !== 'head') return { events: [deliveryEvent('e1.3', 3)] };
      return answers.shift();
    },
  }, { baselined: false });
  f.adapter.start();
  await settle();
  // A failed read (no page) and a page without a head cursor both stage
  // nothing and persist nothing.
  assert.deepEqual(calls, ['head']);
  assert.equal(f.kv.scope(KEY), undefined, 'a failed baseline persists no progress');
  assert.equal(f.kv.claims().length, 0);
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_reconcile_failed'), 'the failure backs off');
  f.clock.advance(1000); // first backoff step
  await settle();
  assert.deepEqual(calls, ['head', 'head'], 'the next pass retries the baseline');
  assert.equal(f.kv.scope(KEY), undefined, 'a head page without next_cursor is not a baseline');
  f.clock.advance(2000);
  await settle();
  assert.deepEqual(calls, ['head', 'head', 'head']);
  assert.equal(f.kv.scope(KEY)!.cursor, 'e1.12', 'a good head read baselines');
  assert.equal(f.kv.claims().length, 0);
  assert.equal(f.transportCalls.length, 0, 'the baseline pass stages nothing');
});

test('claudeInboxArgs emits --since head for the baseline read', () => {
  assert.deepEqual(claudeInboxArgs({ profile: 'p', config: BIND_CONFIG, hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' }, 'head'),
    ['--net', 'p', 'inbox', '--json', '--limit', '32', '--no-display-proof', '--since', 'head']);
});

test('admission requires the config witness and an absolute binding config', async t => {
  for (const [name, row] of [
    ['witness false', admittedRow({ admission: { live: true, policy: true, version_qualified: true, canary: true, witness: false } })],
    ['witness absent', admittedRow({ admission: { live: true, policy: true, version_qualified: true, canary: true } })],
    ['config absent', admittedRow({ scope: { profile: 'p', hub_identity: HUB_PIN, hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' } })],
    ['config relative', admittedRow({ scope: { profile: 'p', config: 'bind/config.toml', hub_identity: HUB_PIN, hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' } })],
  ] as const) {
    const f = fixture(t);
    f.setSnapshot([row]);
    f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
    f.adapter.start();
    await settle();
    assert.equal(f.pool.recording.requests.length, 0, `${name}: no watch`);
    assert.equal(f.transportCalls.length, 0, `${name}: no transport`);
    assert.equal(f.adapter.admitted().size, 0, `${name}: not admitted`);
  }
});

test('a config or generation change reopens the scope watch', async t => {
  const f = fixture(t);
  f.adapter.start();
  await settle();
  assert.equal(f.pool.recording.requests.length, 1, 'one watch for the admitted scope');
  assert.equal(f.pool.recording.active(), 1);
  // The same key and generation, but the binding now names another config.
  f.setSnapshot([admittedRow({ scope: { profile: 'p', config: MOVED_CONFIG, hub_identity: HUB_PIN, hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' } })]);
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.pool.recording.requests.length, 2, 'a config change reopens the watch');
  assert.equal(f.pool.recording.active(), 1, 'the prior subscription was released, not leaked');
  f.setSnapshot([admittedRow({ generation: OTHER_GEN, scope: { profile: 'p', config: MOVED_CONFIG, hub_identity: HUB_PIN, hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' } })]);
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.pool.recording.requests.length, 3, 'a generation change reopens the watch');
  assert.equal(f.pool.recording.requests[2].revision, OTHER_GEN);
  assert.equal(f.pool.recording.active(), 1, 'the prior subscription was released, not leaked');
  // Unchanged: no reopen.
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.equal(f.pool.recording.requests.length, 3, 'an unchanged scope keeps its watch');
  assert.equal(f.pool.recording.active(), 1);
});

test('a head page carrying events is not a baseline: it retries', async t => {
  // The hub resolves `head` and reads the page in separate snapshots: an
  // arrival in between comes back on the head page with next_cursor past
  // it. Baselining there would skip it forever, so the pass refuses it.
  const calls: Array<string | undefined> = [];
  const answers: ClaudeCandidatePage[] = [
    { events: [deliveryEvent('e1.5', 5)], nextCursor: 'e1.5' },
    { events: [], nextCursor: 'e1.5' },
  ];
  const f = fixture(t, {
    fetchCandidates: async (_c, cursor) => {
      calls.push(cursor);
      return cursor === 'head' ? answers.shift() : { events: [] };
    },
  }, { baselined: false });
  f.adapter.start();
  await settle();
  assert.deepEqual(calls, ['head']);
  assert.equal(f.kv.scope(KEY), undefined, 'a non-empty head page persists no baseline');
  assert.equal(f.kv.claims().length, 0);
  f.clock.advance(1000);
  await settle();
  assert.deepEqual(calls, ['head', 'head'], 'the next pass retries the baseline');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e1.5', 'an empty head page baselines');
  assert.equal(f.transportCalls.length, 0);
});

// Phase 2 task 6: claim ledger hygiene. Ack observation is the first and only
// hubAckObserved writer and is strictly read-only; retirement runs in the
// claim's own scope lane; a fence sweep drains every claim whose scope is no
// longer admitted, even when nothing is admitted; a pass is linear in claims.

type AckWindow = { deliveryIds: Set<string>; complete: boolean } | undefined;

function settledClaim(id: string, over: Partial<ClaudeClaimRecord> = {}): ClaudeClaimRecord {
  return seededClaim({ deliveryId: id, messageId: 'msg_' + ulid(Number(id.replace(/\D/g, '')) || 70), state: 'released',
    disposition: 'attempted', dispositionReason: 'write_completed_no_presentation_proof', ...over });
}

test('a complete acked window marks and then retires settled tombstones of its own scope only', async t => {
  const foreign = 'ee'.repeat(32);
  const reads: string[] = [];
  const f = fixture(t, {
    fetchAckWindow: async c => { reads.push(c.profile); return { deliveryIds: new Set(['dly_still']), complete: true }; },
  });
  for (const [id, over] of [
    ['dly_acked', {}],
    ['dly_refused', { state: 'refused', disposition: 'refused', dispositionReason: 'connect_refused_or_timeout' }],
    ['dly_held', { state: 'held', disposition: 'held', dispositionReason: 'inbox_only', priority: 'hold' }],
    ['dly_still', {}],
  ] as const) f.kv.kv.set('claude-claims:claim:' + id, JSON.stringify(settledClaim(id, over as Partial<ClaudeClaimRecord>)));
  // An already-acked row of another (not admitted) scope: never this lane's.
  f.kv.kv.set('claude-claims:claim:dly_foreign', JSON.stringify(settledClaim('dly_foreign', { scope: foreign, hubAckObserved: true })));
  f.adapter.start();
  await settle();
  assert.equal(reads.length, 1, 'one ack-window read for the admitted scope');
  const after = new Map(f.kv.claims().map(c => [c.deliveryId, c]));
  for (const id of ['dly_acked', 'dly_refused', 'dly_held']) {
    assert.equal(after.get(id), undefined, `${id} acked by the hub and settled: retired`);
  }
  assert.ok(after.get('dly_still'), 'a delivery still in the unacked window survives');
  assert.notEqual(after.get('dly_still')!.hubAckObserved, true, 'and is not marked');
  assert.ok(after.get('dly_foreign'), 'a foreign scope\'s acked row is never retired by this lane');
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_claim_retired'));
});

test('an incomplete window marks nothing', async t => {
  const f = fixture(t, {
    fetchAckWindow: async () => ({ deliveryIds: new Set<string>(), complete: false }),
  });
  f.kv.kv.set('claude-claims:claim:dly_a1', JSON.stringify(settledClaim('dly_a1')));
  f.kv.kv.set('claude-claims:claim:dly_a2', JSON.stringify(settledClaim('dly_a2', { state: 'uncertain', disposition: 'uncertain', dispositionReason: 'write_failed_or_outcome_unknown' })));
  const before = [...f.kv.raw.entries()].filter(([k]) => k.startsWith('claude-claims:claim:'));
  f.adapter.start();
  await settle();
  const after = [...f.kv.raw.entries()].filter(([k]) => k.startsWith('claude-claims:claim:'));
  assert.deepEqual(after, before, 'an incomplete window changes no claim byte');
  // A failed read (no window) also changes nothing.
  const g = fixture(t, { fetchAckWindow: async () => undefined });
  g.kv.kv.set('claude-claims:claim:dly_a1', JSON.stringify(settledClaim('dly_a1')));
  const raw = g.kv.raw.get('claude-claims:claim:dly_a1');
  g.adapter.start();
  await settle();
  assert.equal(g.kv.raw.get('claude-claims:claim:dly_a1'), raw);
});

test('uncertain claims are marked but never retired', async t => {
  const f = fixture(t, {
    fetchAckWindow: async () => ({ deliveryIds: new Set<string>(), complete: true }),
  });
  f.kv.kv.set('claude-claims:claim:dly_u1', JSON.stringify(settledClaim('dly_u1', {
    state: 'uncertain', disposition: 'uncertain', dispositionReason: 'write_failed_or_outcome_unknown' })));
  f.adapter.start();
  await settle();
  f.clock.advance(60_000); await settle(); // A second observation and pass.
  const claim = f.kv.claim('dly_u1')!;
  assert.ok(claim, 'an uncertain claim is never retired');
  assert.equal(claim.hubAckObserved, true, 'but the hub ack is recorded for the operator');
  assert.equal(claim.state, 'uncertain');
});

test('ack observation never posts ack or receipts', unixChild, async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-claude-ack-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = join(dir, 'vecnet'), argvLog = join(dir, 'argv.log');
  writeFileSync(bin, ['#!/bin/sh', `printf '%s\\n' "$*" >> ${JSON.stringify(argvLog)}`,
    `printf '%s\\n' '{"events":[],"next_cursor":"e1.3"}'`, ''].join('\n'), { mode: 0o700 });
  const f = fixture(t, {
    env: { HOME: dir, PATH: process.env.PATH, VECNET_BIN: bin },
    fetchCandidates: undefined, fetchAckWindow: undefined, // The real pinned inbox children.
  });
  f.kv.kv.set('claude-claims:claim:dly_s1', JSON.stringify(settledClaim('dly_s1')));
  f.adapter.start();
  const lines = () => { try { return readFileSync(argvLog, 'utf8').trim().split('\n').filter(Boolean); } catch { return []; } };
  for (let i = 0; i < 200 && !lines().some(l => l.includes('--limit 200')); i++) await settle(25);
  const seen = lines();
  const ack = seen.find(l => l.includes('--limit 200'));
  assert.ok(ack, `the ack window was read: ${seen}`);
  assert.deepEqual(ack!.split(' '), ['--net', 'p', 'inbox', '--json', '--limit', '200', '--no-display-proof'],
    'the ack read is a plain inbox read from the acked cursor');
  for (const line of seen) {
    const argv = line.split(' ');
    assert.ok(!argv.includes('--ack') && !argv.includes('ack') && !argv.includes('receipt') && !argv.includes('--auto-ack'),
      `a child acked or posted a receipt: ${line}`);
  }
  assert.equal(f.kv.claim('dly_s1'), undefined, 'an empty complete window retires the settled tombstone');
});

test('disabling the lone admitted scope drains reserved rows', async t => {
  const f = fixture(t);
  f.setSnapshot([admittedRow({ admission: { live: true, policy: false, version_qualified: true, canary: true, witness: true } })]);
  f.kv.kv.set('claude-claims:claim:dly_r1', JSON.stringify(seededClaim({ deliveryId: 'dly_r1' })));
  f.kv.kv.set('claude-claims:claim:dly_r2', JSON.stringify(seededClaim({ deliveryId: 'dly_r2', scope: 'ee'.repeat(32) })));
  f.kv.kv.set('claude-claims:claim:dly_t1', JSON.stringify(seededClaim({ deliveryId: 'dly_t1', state: 'attempting' })));
  f.kv.kv.set('claude-claims:claim:dly_k1', JSON.stringify(settledClaim('dly_k1')));
  f.adapter.start();
  await settle();
  assert.equal(f.adapter.admitted().size, 0, 'nothing is admitted');
  assert.equal(f.transportCalls.length, 0, 'a drained claim is never sent');
  for (const id of ['dly_r1', 'dly_r2']) {
    const c = f.kv.claim(id)!;
    assert.deepEqual([c.state, c.disposition, c.dispositionReason], ['released', 'released', 'fenced_scope_not_admitted'], id);
  }
  const t1 = f.kv.claim('dly_t1')!;
  assert.deepEqual([t1.state, t1.disposition, t1.dispositionReason], ['uncertain', 'uncertain', 'restart_may_have_written']);
  assert.equal(f.kv.claim('dly_k1')!.state, 'released', 'a settled tombstone is left as it was');
  assert.equal(f.kv.claim('dly_k1')!.dispositionReason, 'write_completed_no_presentation_proof');
});

test('a pass is linear in claims', async t => {
  // A counting kv over a plain map: 200 settled tombstones plus 32 staged
  // wakes in one pass (one snapshot, one page, one ack window).
  const map = new Map<string, string>();
  let gets = 0;
  const counted: KeyValue = {
    get: k => { gets++; return map.get(k); },
    set: (k, v) => { map.set(k, v); },
    delete: k => { map.delete(k); },
    keys: (prefix = '') => [...map.keys()].filter(k => k.startsWith(prefix)),
  };
  map.set('claude-claims:scope:' + KEY, JSON.stringify({ v: 1, generation: GEN, cursor: 'e1.0', blockedPendingLimit: false, updatedAt: 1000 }));
  for (let i = 0; i < 200; i++) {
    const id = 'dly_' + ulid(1000 + i);
    map.set('claude-claims:claim:' + id, JSON.stringify(settledClaim(id, { messageId: 'msg_' + ulid(1000 + i) })));
  }
  const events = Array.from({ length: 32 }, (_, i) => deliveryEvent('e1.' + (i + 1), i + 1));
  let served = false;
  const f = fixture(t, {
    kv: counted,
    fetchCandidates: async (_c, cursor) => {
      if (cursor === 'head') return { events: [], nextCursor: 'e1.0' };
      if (served) return { events: [] };
      served = true;
      return { events };
    },
  }, { baselined: false });
  f.adapter.start();
  await settle(); // Pass 1: the establishing baseline (one claim scan).
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle(150); // Pass 2: one page, one ack window, 32 staged wakes.
  const delivered = [...map.values()].filter(v => v.includes('"write_completed_no_presentation_proof"')).length;
  assert.equal(delivered, 232, 'all 32 staged wakes were delivered beside the 200 tombstones');
  assert.ok(served, 'the one page was staged');
  // Two passes, each linear: one claim scan (200 gets) per pass plus a
  // bounded handful of gets per staged attempt.
  assert.ok(gets <= 2 * 200 + 4 * 32, `two passes made ${gets} kv.get calls, want <= ${2 * 200 + 4 * 32}`);
});

test('a rejected stored cursor rebaselines; a transient failure keeps the cursor', async t => {
  const calls: Array<string | undefined> = [];
  const headCursors = ['e1.0', 'e2.0']; // The establishing baseline, then the rebaseline.
  let mode: 'rejected' | 'transient' = 'rejected';
  const f = fixture(t, {
    fetchCandidates: async (_c, cursor) => {
      calls.push(cursor);
      if (cursor === 'head') return { events: [], nextCursor: headCursors.shift() };
      return mode === 'rejected' ? { events: [], cursorRejected: true } : undefined;
    },
  });
  f.adapter.start();
  await settle(); // The first pass of this process establishes the baseline.
  assert.deepEqual(calls, ['head']);
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.deepEqual(calls, ['head', 'e1.0', 'head'], 'a rejected cursor reads the head in the same pass');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e2.0', 'the scope rebaselined');
  assert.equal(f.kv.claims().length, 0, 'the rebaseline stages nothing');
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_scope_baselined'));
  assert.ok(!f.log.entries.some(e => e.msg === 'claude_delivery_reconcile_failed'), 'a rebaseline is not a failure');

  mode = 'transient';
  calls.length = 0;
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle();
  assert.deepEqual(calls, ['e2.0'], 'a transient failure never reads the head');
  assert.equal(f.kv.scope(KEY)!.cursor, 'e2.0', 'and keeps the stored cursor');
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_reconcile_failed'), 'a transient failure backs off');
});

/** The real CLI's stderr and exit code for a hub cursor refusal, pinned
 * against a loopback hub by internal/cli TestInboxCursorRefusalStderr. */
const CURSOR_ERRORS = JSON.parse(readFileSync(new URL('./fixtures/cli-cursor-errors.json', import.meta.url), 'utf8')) as
  { exit: number; purged: string; malformed: string };

test('the inbox child reports a purged or malformed cursor as rejected, anything else as a failure', unixChild, async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-claude-purge-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const coordinates = { profile: 'p', config: BIND_CONFIG, hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' };
  /** A stub CLI that writes exactly `stderr` and exits `code`. */
  const run = async (stderr: string, code: number, cursor: string | undefined = 'e1.5') => {
    const id = randomUUID(), bin = join(dir, 'vecnet-' + id), text = join(dir, 'stderr-' + id);
    writeFileSync(text, stderr);
    writeFileSync(bin, `#!/bin/sh\ncat ${JSON.stringify(text)} >&2\nexit ${code}\n`, { mode: 0o700 });
    return spawnClaudeInbox({ HOME: dir, PATH: process.env.PATH, VECNET_BIN: bin }, fakeClock(1000))(coordinates, cursor);
  };
  const rejected = { events: [], cursorRejected: true };
  assert.deepEqual(await run(CURSOR_ERRORS.purged, CURSOR_ERRORS.exit), rejected, 'the real purged/foreign-epoch refusal');
  assert.deepEqual(await run(CURSOR_ERRORS.malformed, CURSOR_ERRORS.exit), rejected, 'the real malformed-cursor refusal');
  assert.equal(await run(CURSOR_ERRORS.purged, 3), undefined, 'a network exit is transient');
  assert.equal(await run('rate_limited: slow down\n', 2), undefined, 'another hub error is transient');
  assert.equal(await run('invalid_value: limit must be positive\n', 2), undefined, 'a non-cursor validation error is transient');
  assert.equal(await run('note: ' + CURSOR_ERRORS.purged, 2), undefined, 'the code must start the line');
  assert.equal(await run(CURSOR_ERRORS.purged, 2, 'head'), undefined, 'a head read is never a cursor refusal');
  assert.equal(await run(CURSOR_ERRORS.purged, 2, ''), undefined, 'nor is a read from the acked cursor (no --since)');
});

// Task 6 fix round: an operator resolution racing the ack read must never be
// lost. The ack observation marks a copy, never the index row; a refused
// (superseded) write refreshes the index from the kv; retirement re-reads
// and deletes only the exact bytes it last saw.

for (const [name, resolution] of [
  ['--retry', (c: ClaudeClaimRecord): ClaudeClaimRecord => ({ ...c, state: 'reserved', disposition: undefined, dispositionReason: undefined,
    attemptId: 'b'.repeat(32), updatedAt: 777_777 })],
  ['--keep-inbox', (c: ClaudeClaimRecord): ClaudeClaimRecord => ({ ...c, state: 'held', disposition: 'held',
    dispositionReason: 'operator_keep_inbox', updatedAt: 777_777 })],
] as const) {
  test(`an operator resolve ${name} racing the ack read survives byte-identical`, async t => {
    let operatorRaw: string | undefined;
    let f!: Fixture;
    f = fixture(t, {
      fetchAckWindow: async () => {
        // The operator's `claude resolve` lands while the ack read is out.
        const current = f.kv.claim('dly_u7')!;
        const next = JSON.parse(JSON.stringify(resolution(current))) as Record<string, unknown>;
        for (const k of Object.keys(next)) if (next[k] === undefined) delete next[k];
        operatorRaw = JSON.stringify(next);
        f.kv.kv.set('claude-claims:claim:dly_u7', operatorRaw);
        return { deliveryIds: new Set<string>(), complete: true };
      },
    });
    // A settled, retirable tombstone the hub has acked: before the fix the
    // stale in-memory copy kept the mark after its save was refused, and
    // retirement then deleted the operator's freshly resolved row.
    f.kv.kv.set('claude-claims:claim:dly_u7', JSON.stringify(settledClaim('dly_u7', {
      state: 'refused', disposition: 'refused', dispositionReason: 'connect_refused_or_timeout' })));
    f.adapter.start();
    await settle();
    assert.ok(operatorRaw, 'the race was staged');
    assert.equal(f.kv.raw.get('claude-claims:claim:dly_u7'), operatorRaw, 'the operator row survives the pass byte-identical');
    assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_claim_superseded'), 'the refused write is surfaced');
    assert.ok(!f.log.entries.some(e => e.msg === 'claude_delivery_claim_retired'), 'nothing was retired');
  });
}

test('a superseded write refreshes the index from the kv', async t => {
  let f!: Fixture;
  let reads = 0;
  f = fixture(t, {
    fetchAckWindow: async () => {
      reads++;
      if (reads === 1) {
        // Operator re-resolves to held during the first ack read.
        const c = f.kv.claim('dly_x1')!;
        f.kv.kv.set('claude-claims:claim:dly_x1', JSON.stringify({ ...c, state: 'held', disposition: 'held', dispositionReason: 'operator_keep_inbox', updatedAt: 888 }));
      }
      return { deliveryIds: new Set<string>(), complete: true };
    },
  });
  f.kv.kv.set('claude-claims:claim:dly_x1', JSON.stringify(settledClaim('dly_x1', {
    state: 'refused', disposition: 'refused', dispositionReason: 'connect_refused_or_timeout' })));
  f.adapter.start();
  await settle();
  // Pass 1: the mark was refused; the refreshed index holds the operator's
  // held row (unmarked), so nothing is retired.
  assert.equal(f.kv.claim('dly_x1')!.state, 'held');
  assert.notEqual(f.kv.claim('dly_x1')!.hubAckObserved, true);
  // Pass 2, a minute later: the refreshed row is marked and, being settled,
  // retired — proof the index followed the kv rather than the stale copy.
  f.clock.advance(60_000);
  await settle();
  assert.equal(reads, 2);
  assert.equal(f.kv.claim('dly_x1'), undefined, 'the refreshed held row is observed and retired next time');
});

test('retirement re-reads: an already-flagged tombstone resolved mid-read survives', async t => {
  // The row is flagged and retirable before the pass, so marking skips it
  // and nothing else writes it: only retirement's own re-read protects it.
  let operatorRaw: string | undefined;
  let f!: Fixture;
  f = fixture(t, {
    fetchAckWindow: async () => {
      const c = f.kv.claim('dly_f1')!;
      operatorRaw = JSON.stringify({ ...c, state: 'reserved', attemptId: 'c'.repeat(32), hubAckObserved: false, updatedAt: 999_999 });
      f.kv.kv.set('claude-claims:claim:dly_f1', operatorRaw);
      return { deliveryIds: new Set<string>(), complete: true };
    },
    transport: async () => 4,
  });
  f.kv.kv.set('claude-claims:claim:dly_f1', JSON.stringify(settledClaim('dly_f1', {
    state: 'refused', disposition: 'refused', dispositionReason: 'connect_refused_or_timeout', hubAckObserved: true })));
  f.adapter.start();
  await settle();
  assert.ok(operatorRaw);
  assert.ok(f.kv.claim('dly_f1'), 'the re-armed operator row is never deleted');
  assert.ok(!f.log.entries.some(e => e.msg === 'claude_delivery_claim_retired'), 'nothing was retired');
});

test('a superseded write refreshes the index within the pass (pending cap follows the kv)', async t => {
  // 128 uncertain claims fill the cap. During the ack read the operator
  // resolves one to held; that row's mark is refused as superseded and the
  // refreshed index frees one slot, so this same pass stages the new wake.
  let f!: Fixture;
  f = fixture(t, {
    fetchAckWindow: async () => {
      const c = f.kv.claim('dly_' + ulid(2000))!;
      f.kv.kv.set('claude-claims:claim:dly_' + ulid(2000), JSON.stringify({ ...c, state: 'held', disposition: 'held',
        dispositionReason: 'operator_keep_inbox', updatedAt: 999_999 }));
      // Every claim stays in the unacked window except the resolved one, so
      // no other uncertain claim is marked (marking does not free a slot).
      const ids = new Set<string>();
      for (let i = 1; i < CLAUDE_PENDING_LIMIT; i++) ids.add('dly_' + ulid(2000 + i));
      return { deliveryIds: ids, complete: true };
    },
  });
  for (let i = 0; i < CLAUDE_PENDING_LIMIT; i++) {
    const id = 'dly_' + ulid(2000 + i);
    f.kv.kv.set('claude-claims:claim:' + id, JSON.stringify(settledClaim(id, { messageId: 'msg_' + ulid(2000 + i),
      state: 'uncertain', disposition: 'uncertain', dispositionReason: 'write_failed_or_outcome_unknown' })));
  }
  f.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  f.adapter.start();
  await settle(); // Baseline-only first pass (the ack read resolves one claim).
  f.pool.recording.notify('exclusive:claude:' + KEY, { priority: 'wake' });
  await settle(60);
  assert.equal(f.kv.claim('dly_' + ulid(2000))!.state, 'held', 'the operator resolution stands');
  assert.equal(f.transportCalls.length, 1, 'the freed slot staged the new wake in the same pass');
  assert.equal(f.kv.scope(KEY)!.blockedPendingLimit, false);
});

test('a pass with an unreadable snapshot drains nothing', async t => {
  // Unknown admission is not "not admitted": no fence sweep may run.
  const f = fixture(t);
  f.setSnapshotUnavailable();
  f.kv.kv.set('claude-claims:claim:dly_r1', JSON.stringify(seededClaim({ deliveryId: 'dly_r1' })));
  f.kv.kv.set('claude-claims:claim:dly_t1', JSON.stringify(seededClaim({ deliveryId: 'dly_t1', state: 'attempting' })));
  const before = [...f.kv.raw.entries()].filter(([k]) => k.startsWith('claude-claims:claim:'));
  f.adapter.start();
  await settle();
  const after = [...f.kv.raw.entries()].filter(([k]) => k.startsWith('claude-claims:claim:'));
  assert.deepEqual(after, before, 'no claim byte changes on an unproven pass');
  assert.equal(f.transportCalls.length, 0);
  assert.ok(f.log.entries.some(e => e.msg === 'claude_delivery_reconcile_failed'), 'the unproven pass backs off');
});
