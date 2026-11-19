import assert from 'node:assert/strict';
import { test } from 'node:test';
import type { TestContext } from 'node:test';
import type { KeyValue } from '../src/backend.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { Log } from '../../hub-client/src/ports.ts';
import { createClaudeDeliveryAdapter, claudeDeliveryCanaryEnabled, claudeInboxArgs, claudeDeliverArgs } from '../src/delivery/claude-adapter.ts';
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
    admission: { live: true, policy: true, version_qualified: true },
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
    setPages: next => { pages = next; },
    setExit: value => { exit = value; },
  };
}

async function settle(ms = 20): Promise<void> { await new Promise(resolve => setTimeout(resolve, ms)); }

test('admission gating: non-live and non-qualified bindings never reach the transport', async t => {
  const f = fixture(t);
  f.setSnapshot([
    admittedRow({ binding_key: 'cd'.repeat(32), admission: { live: false, policy: true, version_qualified: true } }),
    admittedRow({ binding_key: 'ce'.repeat(32), admission: { live: true, policy: false, version_qualified: true } }),
    admittedRow({ binding_key: 'cf'.repeat(32), admission: { live: true, policy: true, version_qualified: false } }),
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

test('child argument builders carry no ack and no raw endpoint coordinates', () => {
  const inbox = claudeInboxArgs({ profile: 'p', hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' }, 'e1.4');
  assert.deepEqual(inbox, ['--net', 'p', 'inbox', '--json', '--limit', '32', '--since', 'e1.4']);
  const fresh = claudeInboxArgs({ profile: 'p', as: 'peer_B', hubEndpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_B' }, undefined);
  assert.deepEqual(fresh, ['--net', 'p', '--as', 'peer_B', 'inbox', '--json', '--limit', '32']);
  const deliver = claudeDeliverArgs({ bindingKey: KEY, generation: GEN, msgId: 'n'.repeat(26), threadId: THREAD, messageId: 'msg_01E8YQZ5N8W3C4R6T7A9A2B4CD' });
  assert.deepEqual(deliver, ['claude', 'deliver', '--binding-key', KEY, '--expect-generation', GEN,
    '--msg-id', 'n'.repeat(26), '--thread-id', THREAD, '--message-id', 'msg_01E8YQZ5N8W3C4R6T7A9A2B4CD']);
});

test('the canary gate defaults to off and requires the explicit experimental opt-in', () => {
  assert.equal(claudeDeliveryCanaryEnabled({}), false);
  assert.equal(claudeDeliveryCanaryEnabled({ VECNET_CLAUDE_DELIVERY_CANARY: '' }), false);
  assert.equal(claudeDeliveryCanaryEnabled({ VECNET_CLAUDE_DELIVERY_CANARY: '0' }), false);
  assert.equal(claudeDeliveryCanaryEnabled({ VECNET_CLAUDE_DELIVERY_CANARY: 'pointer' }), false);
  assert.equal(claudeDeliveryCanaryEnabled({ VECNET_CLAUDE_DELIVERY_CANARY: '1' }), true);
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
