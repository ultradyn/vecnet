import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, statSync, symlinkSync, unlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import type { TestContext } from 'node:test';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { codexAuthority } from '../src/codex/connector.ts';
import { openDeliveryLease, openStatusPublisher } from '../src/codex/connector-status.ts';
import { startDeliveryHost } from '../src/delivery/host.ts';
import type { HostedAdapter } from '../src/delivery/host.ts';
import { harness, until } from './daemon-harness.ts';

const unix = { skip: process.platform === 'win32' && 'Unix flock and inode fixture', timeout: 15_000 };
const euid = process.geteuid?.() ?? -1;

function fixture(t: TestContext) {
  const home = mkdtempSync(join(tmpdir(), 'vn-delivery-host-'));
  const authority = join(home, '.local', 'state', 'vecnet', 'codex', 'bindings.json');
  const status = authority + '.status.json', lock = status + '.lock';
  const env = { HOME: home, PATH: process.env.PATH, VECNET_BIN: process.execPath };
  const clock = fakeClock(1000), log = memoryLog();
  const cleanup: Array<() => unknown> = [];
  t.after(async () => {
    try { for (const stop of cleanup.reverse()) await stop(); }
    finally { rmSync(home, { recursive: true, force: true }); }
  });
  return { authority, status, lock, env, clock, log, cleanup };
}

function deferred<T = void>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

function inode(path: string) {
  const { dev, ino } = statSync(path, { bigint: true });
  return { dev, ino };
}

async function assertLegacyExcluded(s: ReturnType<typeof fixture>) {
  const legacy = await openStatusPublisher(s.authority, euid, { env: s.env });
  try { assert.equal(legacy, undefined, 'the legacy publisher uses the very same lock'); }
  finally { await legacy?.close(); }
}

test('delivery host preserves exact legacy election and drains adapters before releasing', unix, async t => {
  const s = fixture(t);
  let started = 0, stopping = 0;
  const drain = deferred<void>();
  const auxiliary = (): HostedAdapter => ({ start() { started++; }, stop() { stopping++; return drain.promise; } });
  const host = startDeliveryHost({ ...s, auxiliary });
  s.cleanup.push(() => host.stop(), () => drain.resolve());
  await until(() => existsSync(s.status), 'Codex readiness under the host lease');
  assert.equal(started, 1);
  const original = inode(s.lock);
  await assertLegacyExcluded(s);

  let contenders = 0;
  const next = startDeliveryHost({ ...s, auxiliary: () => {
    contenders++; return { start() {}, async stop() {} };
  } });
  s.cleanup.push(() => next.stop());
  await until(() => s.log.entries.some(e => e.msg === 'codex_connector_duplicate'), 'the second host loses election');
  assert.equal(contenders, 0, 'a losing host must not even construct its adapter');

  const stopped = host.stop();
  await until(() => stopping === 1, 'auxiliary shutdown starts');
  await until(() => !existsSync(s.status), 'Codex publisher drains while the auxiliary is still held');
  await assertLegacyExcluded(s);
  drain.resolve();
  await stopped;
  assert.deepEqual(await host.stopped, { reason: 'stopped' });
  assert.equal(stopping, 1);
  s.clock.advance(10_000);
  await until(() => existsSync(s.status), 'the second host takes over');
  assert.equal(contenders, 1);
  assert.deepEqual(inode(s.lock), original);
});

test('hosted authority refuses a symbolic link at the bindings leaf', unix, t => {
  const s = fixture(t);
  const state = dirname(s.authority);
  mkdirSync(state, { recursive: true, mode: 0o700 });
  const ordinaryFile = join(state, 'registry.json');
  writeFileSync(ordinaryFile, '', { mode: 0o600 });
  symlinkSync(ordinaryFile, s.authority);
  assert.equal(codexAuthority(s.env), undefined);
});

test('delivery host keeps its elected bindings and recovery paths across a configuration change', unix, async t => {
  const s = fixture(t), other = fixture(t);
  const env = { ...s.env };
  mkdirSync(dirname(other.authority), { recursive: true, mode: 0o700 });
  writeFileSync(other.authority, '{', { mode: 0o600 });
  const host = startDeliveryHost({ ...s, env, openLease: async (path, uid, options) => {
    const lease = await openDeliveryLease(path, uid, options);
    env.HOME = other.env.HOME;
    return lease;
  } });
  s.cleanup.push(() => host.stop());
  await until(() => existsSync(s.status), 'Codex readiness at the elected location');
  assert.equal(existsSync(join(dirname(other.authority), 'delivery.sqlite')), false, 'recovery must not move to the newly configured home');
  assert.ok(statSync(join(dirname(s.authority), 'delivery.sqlite')).size > 0, 'recovery opens at the elected location');
  assert.ok(s.log.entries.some(e => e.msg === 'codex_connector_disabled' && e.meta?.reason === 'no_bindings_file'));
  assert.ok(!s.log.entries.some(e => e.msg === 'codex_bindings_invalid'), 'bindings must not be read from the newly configured home');
  await assertLegacyExcluded(s);
});

test('delivery host retries only at its original canonical home', unix, async t => {
  const s = fixture(t), other = fixture(t);
  const home = join(s.env.HOME, 'configured-home');
  symlinkSync(s.env.HOME, home, 'dir');
  const incumbent = await openDeliveryLease(s.authority, euid, { env: s.env });
  assert.ok(incumbent);
  s.cleanup.push(() => incumbent.close());
  const host = startDeliveryHost({ ...s, env: { ...s.env, HOME: home } });
  s.cleanup.push(() => host.stop());
  await until(() => s.log.entries.some(e => e.msg === 'codex_connector_duplicate'), 'host waits for the incumbent');
  const original = inode(s.lock);

  unlinkSync(home);
  symlinkSync(other.env.HOME, home, 'dir');
  await incumbent.close();
  s.clock.advance(10_000);
  const otherRecovery = join(dirname(other.authority), 'delivery.sqlite');
  await until(() => existsSync(otherRecovery) || s.log.entries.some(e => e.msg === 'codex_recovery_refused'), 'retry checks the configured home');
  assert.equal(existsSync(otherRecovery), false, 'retry must not initialize recovery at the new location');
  assert.equal(existsSync(s.status), false, 'host stays unelected until the original home is restored');

  unlinkSync(home);
  symlinkSync(s.env.HOME, home, 'dir');
  s.clock.advance(10_000);
  await until(() => existsSync(s.status), 'host takes over at the original home');
  assert.ok(statSync(join(dirname(s.authority), 'delivery.sqlite')).size > 0);
  assert.deepEqual(inode(s.lock), original);
  await assertLegacyExcluded(s);
});

test('delivery host retries a repairable registry leaf refusal at its fixed location', unix, async t => {
  const s = fixture(t);
  const state = dirname(s.authority);
  mkdirSync(state, { recursive: true, mode: 0o700 });
  const ordinaryFile = join(state, 'registry.json');
  writeFileSync(ordinaryFile, '', { mode: 0o600 });
  symlinkSync(ordinaryFile, s.authority);
  const refused = () => s.log.entries.filter(e => e.msg === 'codex_connector_disabled' && e.meta?.reason === 'unsafe_registry_leaf').length;

  const host = startDeliveryHost(s);
  s.cleanup.push(() => host.stop());
  await until(() => refused() === 1, 'host refuses the unsafe registry leaf');
  assert.equal(existsSync(s.status), false, 'a refused leaf never elects');

  s.clock.advance(10_000);
  await until(() => refused() === 2, 'the repairable refusal is retried');
  assert.equal(existsSync(s.status), false, 'the symlink still refuses');

  unlinkSync(s.authority);
  s.clock.advance(10_000);
  await until(() => existsSync(s.status), 'host elects at the original location after repair');
  await assertLegacyExcluded(s);

  // An unlocatable authority is permanent: no location was ever selected.
  const other = fixture(t);
  const permanent = startDeliveryHost({ ...other, env: { ...other.env, VECNET_CODEX_BINDINGS: join(other.env.HOME, 'elsewhere.json') } });
  other.cleanup.push(() => permanent.stop());
  await until(() => other.log.entries.some(e => e.msg === 'codex_connector_disabled' && e.meta?.reason === 'invalid_authority_path'), 'override mismatch is permanent');
  other.clock.advance(10_000);
  other.clock.advance(10_000);
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(other.log.entries.filter(e => e.msg === 'codex_connector_disabled').length, 1, 'an unlocatable authority never retries');
  assert.equal(existsSync(other.status), false);
});

test('delivery host fences a replaced state directory instead of reading replacement bindings', unix, async t => {
  const s = fixture(t);
  const state = dirname(s.authority);
  const host = startDeliveryHost(s);
  s.cleanup.push(() => host.stop());
  await until(() => existsSync(s.status), 'host elected');

  const moved = join(dirname(state), 'codex.moved');
  renameSync(state, moved);
  mkdirSync(state, { recursive: true, mode: 0o700 });
  // A plausible-looking replacement row; parsing it would log a refusal.
  writeFileSync(s.authority, JSON.stringify({ version: 1, bindings: [{
    profile: 'replacement', hub: 'https://hub.example', caller: 'attacker', as: '',
    config_path: '/nonexistent/config.toml', codex_home: '/nonexistent/home',
    thread_id: '01234567-89ab-cdef-0123-456789abcdef', generation: '0123456789abcdef0123456789abcdef',
    created_at: '2026-01-01T00:00:00Z', expires_at: '2027-01-01T00:00:00Z',
  }] }), { mode: 0o600 });

  s.clock.advance(10_000);
  await until(() => s.log.entries.some(e => e.msg === 'codex_state_directory_replaced'), 'the replaced directory is fenced');
  assert.ok(!s.log.entries.some(e => e.msg === 'codex_bindings_invalid' || e.msg === 'codex_bindings_refused'), 'replacement bindings must never be parsed');
  assert.deepEqual(await host.stopped, { reason: 'status_lock_lost' });
  assert.equal(existsSync(join(state, 'delivery.sqlite')), false, 'no recovery is opened at the replacement directory');
});

test('delivery host reports lease loss only after both adapters drain', unix, async t => {
  const s = fixture(t);
  let lose!: () => void, valid = true, released = 0;
  const auxStarted = deferred<void>(), auxDrain = deferred<void>();
  const codexDrain = deferred<void>(), codexStopped = deferred<{ reason: 'stopped' }>();
  let auxStops = 0, codexStops = 0, fenced = false, finished = false;
  const host = startDeliveryHost({ ...s,
    openLease: async (_path, _uid, options) => {
      lose = () => { valid = false; options!.onLost!(); };
      return { valid: () => valid, async close() { released++; } };
    },
    startCodex: ({ lease, leaseLost }) => {
      assert.ok(lease);
      assert.ok(lease.valid());
      assert.ok(leaseLost);
      leaseLost.addEventListener('abort', () => { fenced = !lease.valid(); });
      return {
        stopped: codexStopped.promise,
        stop() { codexStops++; return codexDrain.promise.then(() => { codexStopped.resolve({ reason: 'stopped' }); }); },
        addLauncherBinding() {}, removeLauncherBinding() {},
      };
    },
    auxiliary: () => ({ start() { auxStarted.resolve(); }, stop() { auxStops++; return auxDrain.promise; } }),
  });
  s.cleanup.push(async () => { codexDrain.resolve(); auxDrain.resolve(); await host.stop(); });
  void host.stopped.then(() => { finished = true; });
  await auxStarted.promise;
  lose(); lose();
  assert.equal(fenced, true, 'Codex receives lease loss synchronously');
  assert.equal(auxStops, 1, 'the auxiliary is also fenced synchronously on lease loss');
  const stopped = host.stop();
  await until(() => auxStops === 1 && codexStops === 1, 'both adapters are asked to stop');
  codexDrain.resolve();
  await codexStopped.promise;
  assert.equal(finished, false);
  assert.equal(released, 0);
  auxDrain.resolve();
  await stopped;
  assert.deepEqual(await host.stopped, { reason: 'status_lock_lost' });
  assert.deepEqual([auxStops, codexStops, released], [1, 1, 1]);
});

test('optional adapter startup failure is visible and leaves Codex v1 status healthy', unix, async t => {
  const s = fixture(t);
  let stops = 0;
  const host = startDeliveryHost({ ...s, auxiliary: () => ({
    start() { throw new Error('private adapter detail'); }, async stop() { stops++; },
  }) });
  s.cleanup.push(() => host.stop());
  await until(() => existsSync(s.status), 'Codex readiness despite auxiliary failure');
  assert.deepEqual(JSON.parse(readFileSync(s.status, 'utf8')), { version: 1, updated_at: new Date(1000).toISOString(), bindings: [] });
  assert.equal(stops, 1, 'a partially started auxiliary is cleaned up');
  assert.ok(s.log.entries.some(e => e.level === 'warn' && e.msg === 'delivery_auxiliary_start_failed'));
  assert.doesNotMatch(JSON.stringify(s.log.entries), /private adapter detail/);
  await host.stop();
  assert.equal(stops, 1);
});

test('embedded delivery takes over the managed entry’s lock after SIGKILL leaves stale readiness', unix, async t => {
  const s = fixture(t);
  const entry = fileURLToPath(new URL('../src/codex/delivery-main.ts', import.meta.url));
  const child = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', '--experimental-strip-types', entry], {
    env: s.env, stdio: ['ignore', 'ignore', 'pipe'],
  });
  const exited = once(child, 'close');
  let stderr = '';
  child.stderr.on('data', data => { stderr += data; });
  s.cleanup.push(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); await exited; });
  await until(() => existsSync(s.status) || child.exitCode !== null, 'managed entry startup');
  assert.equal(child.exitCode, null, stderr);
  await assertLegacyExcluded(s);
  const original = inode(s.lock), stale = readFileSync(s.status, 'utf8');
  assert.ok(child.kill('SIGKILL'));
  assert.deepEqual(await exited, [null, 'SIGKILL']);
  assert.equal(readFileSync(s.status, 'utf8'), stale, 'real crash leaves a status file');

  // Use the embedded daemon, with its existing private loopback/stream fixtures.
  const h = await harness(t, { env: s.env });
  const fresh = () => readFileSync(s.status, 'utf8');
  await until(() => fresh() !== stale, 'embedded host overwrites crashed readiness');
  assert.deepEqual(JSON.parse(fresh()), { version: 1, updated_at: new Date(h.clock.now()).toISOString(), bindings: [] });
  assert.deepEqual(inode(s.lock), original);
  await assertLegacyExcluded(s);
  await h.daemon.stop();
  assert.equal(existsSync(s.status), false);
  assert.deepEqual(inode(s.lock), original);
});

test('registered Claude root is visible through the same host across restart',
  { skip: process.platform === 'win32' && 'Unix flock and sidecar fixture', timeout: 30_000 }, async t => {
  const s = fixture(t);
  // A private stub stands in for the packaged CLI child: it answers only the
  // claude runtime-snapshot request and records every invocation.
  const key = 'cd'.repeat(32), generation = '02'.repeat(16);
  const calls = join(s.env.HOME, 'stub-calls.log');
  const stub = join(s.env.HOME, 'vecnet-stub');
  writeFileSync(stub, [
    '#!/bin/sh',
    `printf '%s\\n' "$*" >> ${JSON.stringify(calls)}`,
    'if [ "$1" = claude ] && [ "$2" = runtime-snapshot ]; then',
    `  printf '%s\\n' ${JSON.stringify(JSON.stringify({ version: 1, bindings: [{ binding_key: key, generation, state: 'registered' }] }))}`,
    '  exit 0',
    'fi',
    'exit 1',
    '',
  ].join('\n'), { mode: 0o700 });
  const env = { ...s.env, VECNET_BIN: stub };
  const claudeSidecar = s.authority + '.claude-status.json';

  const entry = fileURLToPath(new URL('../src/codex/delivery-main.ts', import.meta.url));
  const child = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', '--experimental-strip-types', entry], {
    env, stdio: ['ignore', 'ignore', 'pipe'],
  });
  const exited = once(child, 'close');
  let stderr = '';
  child.stderr.on('data', data => { stderr += data; });
  s.cleanup.push(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); await exited; });
  await until(() => existsSync(claudeSidecar) || child.exitCode !== null, 'managed entry publishes the claude sidecar', 15_000);
  assert.equal(child.exitCode, null, stderr);
  const first = JSON.parse(readFileSync(claudeSidecar, 'utf8'));
  assert.equal(first.host_pid, child.pid);
  assert.equal(first.boot_id, readFileSync('/proc/sys/kernel/random/boot_id', 'utf8').trim());
  assert.deepEqual(first.bindings, [{ binding_key: key, generation, state: 'registered' }]);
  assert.deepEqual(JSON.parse(readFileSync(s.status, 'utf8')).version, 1, 'Codex v1 status stays alongside');
  const original = inode(s.lock);

  // A real crash leaves the sidecar behind: only a fresh live writer counts.
  assert.ok(child.kill('SIGKILL'));
  assert.deepEqual(await exited, [null, 'SIGKILL']);
  assert.equal(existsSync(claudeSidecar), true, 'crash leaves the sidecar file');

  // The other host mode (the embedded daemon) takes the same singleton and
  // republishes the same root: no second daemon, no generation change.
  const h = await harness(t, { env });
  await until(() => {
    if (!existsSync(claudeSidecar)) return false;
    const doc = JSON.parse(readFileSync(claudeSidecar, 'utf8'));
    return doc.host_pid === process.pid;
  }, 'embedded host republishes the claude sidecar', 15_000);
  const second = JSON.parse(readFileSync(claudeSidecar, 'utf8'));
  assert.deepEqual(second.bindings, [{ binding_key: key, generation, state: 'registered' }], 'same root generation after restart');
  assert.deepEqual(inode(s.lock), original, 'no second delivery singleton');
  await assertLegacyExcluded(s);

  // Passive only: the stub saw exactly the snapshot child, never a watch or a
  // stream, and the daemon performed no delivery write. (The daemon's own
  // startup presence report is pre-existing behavior and may race in.)
  const seen = readFileSync(calls, 'utf8').trim().split('\n').filter(Boolean);
  assert.ok(seen.length > 0, 'the snapshot child ran at least once');
  for (const line of seen) assert.equal(line, 'claude runtime-snapshot', `unexpected CLI child: ${line}`);
  assert.equal(h.sends().length, 0, 'no message writes');
  assert.equal(h.receipts().length, 0, 'no receipt writes');
  assert.ok(h.presence().length <= 1, `unexpected presence writes: ${h.presence().length}`);

  await h.daemon.stop();
  assert.equal(existsSync(claudeSidecar), false, 'orderly stop removes the owned sidecar');
  assert.deepEqual(inode(s.lock), original);
});
