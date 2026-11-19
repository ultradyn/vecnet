import assert from 'node:assert/strict';
import { existsSync, lstatSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import type { TestContext } from 'node:test';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { openDeliveryLease } from '../src/codex/connector-status.ts';
import { createClaudeStatus } from '../src/delivery/claude-status.ts';
import type { ClaudeSnapshot } from '../src/delivery/claude-status.ts';
import { until } from './daemon-harness.ts';

const unix = { skip: process.platform === 'win32' && 'Unix flock and sidecar fixture', timeout: 15_000 };
const euid = process.geteuid?.() ?? -1;

function fixture(t: TestContext) {
  const home = mkdtempSync(join(tmpdir(), 'vn-claude-status-'));
  const authority = join(home, '.local', 'state', 'vecnet', 'codex', 'bindings.json');
  const sidecar = authority + '.claude-status.json';
  const env = { HOME: home, PATH: process.env.PATH, VECNET_BIN: process.execPath };
  const clock = fakeClock(1000), log = memoryLog();
  t.after(() => { rmSync(home, { recursive: true, force: true }); });
  return { home, authority, sidecar, env, clock, log };
}

const snapshot: ClaudeSnapshot = {
  version: 1,
  bindings: [{ binding_key: 'ab'.repeat(32), generation: '01'.repeat(16), state: 'registered' }],
};

test('claude status never elects a lease and publishes only while the singleton is held', unix, async t => {
  const s = fixture(t);
  const status = createClaudeStatus({ ...s, fetchSnapshot: async () => snapshot });
  status.start();
  s.clock.advance(10_000);
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.equal(existsSync(s.sidecar), false, 'no lease means no sidecar');
  assert.ok(s.log.entries.some(e => e.msg === 'claude_status_lease_not_held'), 'the missing lease is reported');
  assert.equal(existsSync(s.authority + '.status.json.lock'), false, 'the adapter never creates the singleton lock');

  // An elected host holds the singleton: now the same adapter publishes.
  const lease = await openDeliveryLease(s.authority, euid, { env: s.env });
  assert.ok(lease);
  t.after(() => lease.close());
  s.clock.advance(10_000);
  await until(() => existsSync(s.sidecar), 'sidecar under the held lease');
  const doc = JSON.parse(readFileSync(s.sidecar, 'utf8'));
  assert.equal(doc.version, 1);
  assert.equal(doc.host_pid, process.pid);
  assert.equal(typeof doc.host_start, 'string');
  assert.match(doc.host_start, /^[0-9]{1,32}$/);
  assert.match(doc.boot_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.equal(doc.updated_at, new Date(s.clock.now()).toISOString());
  assert.deepEqual(doc.bindings, snapshot.bindings);
  assert.equal(statSync(s.sidecar).mode & 0o077, 0, 'the sidecar is owner-only');
  const before = readFileSync(s.sidecar, 'utf8');

  // Losing the singleton fences further heartbeats.
  await lease!.close();
  s.clock.advance(30_000);
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.equal(readFileSync(s.sidecar, 'utf8'), before, 'a fenced host published a new heartbeat');

  // A later host's sidecar is its own: an orderly stop removes exactly the
  // file this adapter last published, and nothing else.
  const again = await openDeliveryLease(s.authority, euid, { env: s.env });
  assert.ok(again);
  t.after(() => again!.close());
  s.clock.advance(10_000);
  await until(() => readFileSync(s.sidecar, 'utf8') !== before, 'the new host publishes');
  await status.stop();
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(existsSync(s.sidecar), false, 'orderly stop removed the owned file');
});

test('claude status sidecar carries only opaque, redacted fields', unix, async t => {
  const s = fixture(t);
  const lease = await openDeliveryLease(s.authority, euid, { env: s.env });
  assert.ok(lease);
  t.after(() => lease!.close());
  // A hostile snapshot carrying fields that must never reach the sidecar.
  const leaky = (async () => ({ version: 1, bindings: [{ binding_key: 'ab'.repeat(32), generation: '01'.repeat(16), state: 'registered',
    reason: 'x', socket_path: '/run/leak.sock', config: 'raw', credential: 'vn_peer_leak' }] })) as unknown as () => Promise<ClaudeSnapshot>;
  const status = createClaudeStatus({ ...s, fetchSnapshot: leaky });
  status.start();
  await until(() => existsSync(s.sidecar), 'sidecar published');
  const raw = readFileSync(s.sidecar, 'utf8');
  for (const marker of ['/run/leak.sock', 'raw', 'vn_peer_leak']) {
    assert.ok(!raw.includes(marker), `sidecar leaked ${marker}`);
  }
  const doc = JSON.parse(raw);
  assert.deepEqual(Object.keys(doc).sort(), ['bindings', 'boot_id', 'host_pid', 'host_start', 'updated_at', 'version']);
  assert.deepEqual(Object.keys(doc.bindings[0]).sort(), ['binding_key', 'generation', 'reason', 'state']);
  const owned = lstatSync(s.sidecar);
  await status.stop();
  assert.equal(existsSync(s.sidecar), false, 'orderly stop removed the owned file');
  assert.notEqual(owned, undefined);
});

test('claude status sidecar never republishes the adapter admission or scope coordinates', unix, async t => {
  const s = fixture(t);
  const lease = await openDeliveryLease(s.authority, euid, { env: s.env });
  assert.ok(lease);
  t.after(() => lease!.close());
  // The task-7 snapshot carries admission booleans and the host's
  // child-spawn coordinates; the strict sidecar must project both away.
  const admitted = (async () => ({ version: 1 as const, bindings: [{
    binding_key: 'ab'.repeat(32), generation: '01'.repeat(16), state: 'registered' as const,
    admission: { live: true, policy: true, version_qualified: true },
    scope: { profile: 'p', hub_endpoint: 'https://hub.example', caller: 'peer_A', recipient: 'peer_A' },
  }] })) as unknown as () => Promise<ClaudeSnapshot>;
  const status = createClaudeStatus({ ...s, fetchSnapshot: admitted });
  status.start();
  await until(() => existsSync(s.sidecar), 'sidecar published');
  const raw = readFileSync(s.sidecar, 'utf8');
  for (const marker of ['admission', 'scope', 'profile', 'hub.example', 'peer_A', 'version_qualified']) {
    assert.ok(!raw.includes(marker), `sidecar leaked ${marker}`);
  }
  assert.deepEqual(Object.keys(JSON.parse(raw).bindings[0]).sort(), ['binding_key', 'generation', 'state']);
  await status.stop();
});
