import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import type { TestContext } from 'node:test';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { openStatusPublisher } from '../src/codex/connector-status.ts';
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
