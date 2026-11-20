import assert from 'node:assert/strict';
import { test } from 'node:test';
import type { TestContext } from 'node:test';
import type { KeyValue } from '../src/backend.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { Log } from '../../hub-client/src/ports.ts';
import { createClaudeDeliveryAdapter, claudeDeliveryCanaryAllowed, claudeInboxArgs, claudeDeliverArgs } from '../src/delivery/claude-adapter.ts';
import type { ClaudeCandidatePage, ClaudeClaimRecord, ClaudeDeliveryAdapterDeps, ClaudeDeliveryFrame, DeliverExit } from '../src/delivery/claude-adapter.ts';
import type { WatchPool, WatchRequest, WakeEvent, WatchEnd } from '../src/delivery/watch-pool.ts';

// Task 7 (claude delivery chain): the hosted native pointer-delivery adapter.
// Every test drives the adapter through injected fakes only: no hub, no CLI
// child, no socket. The transport is a recording fake; the claim ledger is an
// in-memory kv. The one real invariant under test everywhere: exit 0 is
// write-completed and never presentation proof, and nothing ever acks.

const KEY = 'ab'.repeat(32), GEN = '01'.repeat(16), OTHER_GEN = '02'.repeat(16);
const THREAD = 'thr_01ARZ3NDEKTSV4RRFFQ69G5FAV';

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
};
function fakePool() {
  const requests: WatchRequest[] = [];
  const subs = new Map<string, { notify: (e: WakeEvent) => void; stopped: (e: WatchEnd) => void }>();
  const pool: WatchPool = {
    subscribe(request, notify, stopped) {
      requests.push(request);
      subs.set(request.scope.scope, { notify, stopped });
      return () => { subs.delete(request.scope.scope); };
    },
    async close() { /* nothing owned */ },
  };
  const recording: PoolRecording = {
    requests,
    notify: (scope, event) => { const s = subs.get(scope); if (s) s.notify(event); },
    end: (scope, end) => { const s = subs.get(scope); if (s) s.stopped(end); },
  };
  return { pool, recording };
}

function admittedRow(over: Record<string, unknown> = {}) {
  return {
    binding_key: KEY, generation: GEN, state: 'registered',
    admission: { live: true, policy: true, version_qualified: true, canary: true },
    scope: { profile: 'p', hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' },
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

function fixture(t: TestContext, deps: Partial<ClaudeDeliveryAdapterDeps> = {}): Fixture {
  const clock = fakeClock(1000), log = memoryLog(), store = memoryKv();
  const { pool, recording } = fakePool();
  const transportCalls: ClaudeDeliveryFrame[] = [];
  let exit: DeliverExit = 0;
  let snapshot: unknown = { version: 1, bindings: [admittedRow()] };
  // The adapter re-validates every row; the fixture's raw shape is the point.
  let pages: ClaudeCandidatePage[] = [];
  const adapter = createClaudeDeliveryAdapter({
    pool, clock, log, kv: store.kv, env: { HOME: '/nonexistent', VECNET_BIN: '/nonexistent/vecnet' },
    fetchSnapshot: async () => snapshot as import('../src/delivery/claude-status.ts').ClaudeSnapshot,
    fetchCandidates: async () => pages.shift() ?? { events: [] },
    transport: async frame => { transportCalls.push(frame); return exit; },
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

test('a reserved claim for a vanished scope expires without a write', async t => {
  const f = fixture(t);
  f.kv.kv.set('claude-claims:claim:dly_seeded', JSON.stringify(seededClaim({ scope: 'dd'.repeat(32) })));
  f.adapter.start();
  await settle();
  assert.equal(f.transportCalls.length, 0);
  assert.equal(f.kv.claim('dly_seeded')!.state, 'expired');
});

test('an attempting claim from a crashed run surfaces uncertain and is never resent', async t => {
  const f = fixture(t);
  f.kv.kv.set('claude-claims:claim:dly_seeded', JSON.stringify(seededClaim({ state: 'attempting' })));
  f.setPages([{ events: [deliveryEvent('e1.9', 9)] }]);
  f.adapter.start();
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
  await settle();
  const refusedClaim = refused.kv.claim('dly_' + ulid(1))!;
  assert.equal(refusedClaim.state, 'refused');
  assert.equal(refusedClaim.dispositionReason, 'connect_refused_or_timeout');
  assert.notEqual(refusedClaim.hubAckObserved, true, 'a refusal is never acked either');

  const uncertain = fixture(t);
  uncertain.setPages([{ events: [deliveryEvent('e1.1', 1)] }]);
  uncertain.setExit(4);
  uncertain.adapter.start();
  await settle();
  const uncertainClaim = uncertain.kv.claim('dly_' + ulid(1))!;
  assert.equal(uncertainClaim.state, 'uncertain');
  assert.equal(uncertainClaim.dispositionReason, 'write_failed_or_partial');
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
  await settle();
  const claim = f.kv.claim('dly_' + ulid(1))!;
  assert.equal(claim.state, 'uncertain');
  assert.equal(claim.dispositionReason, 'transport_outcome_unknown');
});

test('a later wake after a quarantined hold delivers the new message and keeps the old tombstone', async t => {
  const f = fixture(t);
  f.setPages([{ events: [deliveryEvent('e1.1', 1, { priority: 'hold' })] }]);
  f.adapter.start();
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
  await settle();
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
  await settle();
  assert.deepEqual(f.transportCalls.map(c => c.threadId), [THREAD], 'only the ordinary message is delivered');
  assert.equal(f.kv.claims().length, 1);
});

test('child argument builders carry no ack, no display proof and no raw endpoint coordinates', () => {
  const inbox = claudeInboxArgs({ profile: 'p', hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' }, 'e1.4');
  assert.deepEqual(inbox, ['--net', 'p', 'inbox', '--json', '--limit', '32', '--no-display-proof', '--since', 'e1.4']);
  const fresh = claudeInboxArgs({ profile: 'p', as: 'peer_B', hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_B' }, undefined);
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
  await settle();
  assert.equal(f.transportCalls.length, 0, 'an unknown event type is never skipped past');
  assert.equal(f.kv.scope(KEY)!.cursor, '', 'the checkpoint does not advance past an unknown event type');
});

test('peer-visible non-delivery events (reaction, hub_notice, operator_view) advance the window', async t => {
  // The hub appends reaction, hub_notice and operator_view rows to peer logs
  // (reactions.go, notices.go, operator_view.go); one of them ahead of a wake
  // must never wedge the scope: the cursor stages past every known
  // non-delivery type so the wake behind it still delivers.
  const f = fixture(t);
  f.setPages([{ events: [
    { cursor: 'e1.0', type: 'reaction', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
    { cursor: 'e1.0b', type: 'hub_notice', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
    { cursor: 'e1.0c', type: 'operator_view', deliveryId: '', messageId: '', threadId: '', priority: '', kind: '' },
    deliveryEvent('e1.1', 1),
  ] }]);
  f.adapter.start();
  await settle();
  assert.deepEqual(f.transportCalls.map(c => c.threadId), [THREAD], 'exactly the wake behind the non-delivery events is delivered');
  assert.match(f.kv.scope(KEY)!.cursor as string, /e1\.1/, 'the checkpoint advances past known non-delivery events');
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
