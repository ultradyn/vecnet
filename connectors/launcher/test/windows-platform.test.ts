import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, linkSync, existsSync, readFileSync, statSync, lstatSync, realpathSync, chmodSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { once } from 'node:events';
import { createConnection } from 'node:net';
import { localPlatform, readPrivateFileSync } from '../src/private-files.ts';
import { readCredential } from '../src/daemon.ts';
import { CODEX_RELOAD_MS, codexBindingsPath, codexRecoveryPath, startCodexConnector } from '../src/codex/connector.ts';
import { readBindings } from '../src/codex/connector-authority.ts';
import { openStatusPublisher } from '../src/codex/connector-status.ts';
import { openCodexRpc as openDeliveryRpc } from '../src/codex/rpc.ts';
import { nativeCodexBinary } from '../src/backends/codex-app-server/control-proxy.ts';
import { openCodexRpc as openBackendRpc } from '../src/backends/codex-app-server/rpc.ts';
import { startFakeCodexAppServer } from './fake-codex-appserver.ts';
import { makeOwnerPrivate, makePrivateDirectory, makePublicReadable, symlinkSync, writePrivate } from './fixtures/platform.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { harness, until } from './daemon-harness.ts';
// @ts-expect-error Test-only JS bootstrap also runs in the actual runner child.
import { fixtureSpawn } from './fixtures/connector-process.mjs';

const windows = process.platform === 'win32';
type Cleanup = () => void | Promise<void>;
type CleanupContext = Pick<TestContext, 'after'>;
const cleanups = new WeakMap<CleanupContext, Cleanup[]>();
/** Node after hooks are FIFO. Stop owners and join children before closing
 * their servers or deleting private paths, including failed setup/assertions. */
function onCleanup(t: CleanupContext, close: Cleanup): void {
  let stack = cleanups.get(t);
  if (!stack) {
    stack = []; cleanups.set(t, stack);
    t.after(async () => {
      const errors: unknown[] = [];
      let cleanupStage = 0;
      for (const cleanup of stack!.reverse()) {
        cleanupStage++;
        try { await cleanup(); } catch (error) {
          const message = error instanceof Error ? error.message : "";
          const code = ["EACCES", "EPERM", "ENOENT", "ENOTDIR"].find(code => (error as NodeJS.ErrnoException)?.code === code) ?? "OTHER";
          const reason = ["fixture ACL operation failed", "all owned ACLs must be restored exactly", "secret file must be a regular file readable only by its owner"].find(known => message.startsWith(known)) ?? "other-assertion";
          console.log(`SAFE_CLEANUP_PROBE stage=${cleanupStage} code=${code} reason=${reason}`);
          errors.push(error);
        }
      }
      if (errors.length) throw new AggregateError(errors, 'owned fixture cleanup failed');
    });
  }
  stack.push(close);
}
function sandbox(t: CleanupContext) {
  const root = mkdtempSync(join(tmpdir(), 'vecnet-native-')), dir = join(root, 'private');
  onCleanup(t, () => rmSync(root, { recursive: true, force: true }));
  makePrivateDirectory(dir); return realpathSync.native(dir);
}

/** Populate an empty native-created inode rather than copying a public owner
 * or exceeding the helper's unchanged 1 MiB stdin cap with executable bytes. */
function privateExecutable(dir: string, source: string, name: string): string {
  const executable = join(dir, name);
  assert.throws(() => lstatSync(executable), { code: 'ENOENT' });
  writePrivate(executable, '');
  localPlatform('check-private', executable);
  const seed = statSync(executable, { bigint: true }), bytes = readFileSync(source);
  assert.equal(seed.size, 0n);
  writeFileSync(executable, bytes, { flag: 'r+' });
  if (!windows) chmodSync(executable, 0o700);
  const populated = statSync(executable, { bigint: true });
  assert.equal(populated.dev, seed.dev); assert.equal(populated.ino, seed.ino);
  assert.equal(populated.size, BigInt(bytes.length)); assert.deepEqual(readFileSync(executable), bytes);
  localPlatform('check-private', executable); localPlatform('check-safe', executable);
  return executable;
}

function assertPrivateDiagnostic(invoke: () => void, sentinel: string, terminalErrno?: string): void {
  let failure: unknown;
  try { invoke(); } catch (error) { failure = error; }
  assert.ok(failure instanceof Error, 'unsafe parent must refuse fixture creation');
  const own = Object.fromEntries(Object.getOwnPropertyNames(failure).map(key => [key, Reflect.get(failure, key)]));
  const serialized = JSON.stringify(own, (_key, value) => value instanceof Error
    ? Object.fromEntries(Object.getOwnPropertyNames(value).map(key => [key, Reflect.get(value, key)])) : value);
  assert.equal(serialized.includes(sentinel), false, 'serialized own/nonenumerable diagnostics must omit the parent path');
  assert.equal(serialized.includes('private-input-SENTINEL'), false);
  assert.equal('path' in failure, false); assert.equal('cause' in failure, false);
  assert.deepEqual(Object.getOwnPropertyNames(failure).sort(), ['message', 'stack']);
  if (terminalErrno) assert.deepEqual(JSON.parse(failure.message), { operation: 'lstat', errno: terminalErrno });
  else {
    const prefix = 'native private fixture failed: ';
    assert.ok(failure.message.startsWith(prefix));
    const metadata = JSON.parse(failure.message.slice(prefix.length));
    assert.deepEqual(Object.keys(metadata).sort(), ['absoluteBinary', 'batch', 'command', 'configuredBinary', 'errno', 'inputBytes', 'outputBytes', 'signal', 'status']);
    assert.equal(metadata.command, 'check-safe'); assert.equal(metadata.status, 1);
    assert.equal(metadata.signal, null); assert.equal(metadata.errno, null);
    assert.equal(metadata.inputBytes, 0); assert.equal(metadata.outputBytes, 0);
    assert.equal(metadata.configuredBinary, true); assert.equal(metadata.absoluteBinary, true); assert.equal(metadata.batch, false);
  }
}

function isReadDenial(code: unknown, nativeWindows: boolean): boolean {
  // Node's bundled libuv maps Windows ERROR_ACCESS_DENIED to UV_EPERM:
  // https://github.com/nodejs/node/blob/v24.22.2/deps/uv/src/win/error.c#L158-L159
  return code === 'EACCES' || (nativeWindows && code === 'EPERM');
}

test('read refusal classification admits only platform access-denial codes', () => {
  for (const [code, nativeWindows, expected] of [
    ['EACCES', false, true], ['EACCES', true, true],
    ['EPERM', true, true], ['EPERM', false, false],
    ['ENOENT', true, false], ['ENOTDIR', true, false], ['EIO', true, false],
    ['EBUSY', true, false], ['UNKNOWN', true, false], ['NO_ERROR', true, false],
    [undefined, true, false], [null, true, false], [0, true, false],
    ['EACCES private-input-SENTINEL', true, false],
  ] as const) assert.equal(isReadDenial(code, nativeWindows), expected, `denial classification case ${String(code)}`);
});

for (const kind of ['file ancestor', 'inaccessible read'] as const) {
  test(`private fixture parent probes redact real ${kind} refusals`, t => {
    const dir = sandbox(t), sentinel = 'F1-parent-path-SENTINEL', parent = join(dir, sentinel), probe = join(parent, 'child');
    const bytes = 'unchanged parent fixture bytes';
    let restore = () => {};
    let verifyRestored = () => {};
    // Drain all checks even on setup/control/redaction failure. LIFO restores
    // ACLs first, verifies every saved ACL next, then checks data before rm.
    onCleanup(t, () => {
      if (kind === 'file ancestor') assert.equal(readFileSync(parent, 'utf8'), bytes);
      else {
        assert.deepEqual(readdirSync(parent), ['child']); assert.deepEqual(readdirSync(probe), ['keep']);
        assert.equal(readFileSync(join(probe, 'keep'), 'utf8'), bytes);
        if (windows) localPlatform('check-private', probe, { directory: true });
      }
    });
    onCleanup(t, () => verifyRestored());
    onCleanup(t, () => restore());
    if (kind === 'file ancestor') writeFileSync(parent, bytes);
    else {
      makePrivateDirectory(probe); writePrivate(join(probe, 'keep'), bytes);
      if (windows) {
        localPlatform('check-private', probe, { directory: true });
        localPlatform('check-private', join(probe, 'keep'));
        assert.equal(readFileSync(join(probe, 'keep'), 'utf8'), bytes);
        const identity = childProcess.spawnSync('whoami.exe', ['/user', '/fo', 'csv', '/nh'], { encoding: 'utf8', windowsHide: true, stdio: ['ignore', 'pipe', 'ignore'] });
        const sid = identity.stdout?.match(/S-1-\d+(?:-\d+)+/)?.[0];
        assert.ok(identity.status === 0 && sid, 'fixture token identity lookup failed');
        const acl = join(dir, 'saved-acl'), restored = join(dir, 'restored-acl');
        const aclCommand = (args: string[]) => {
          const result = childProcess.spawnSync('icacls.exe', args, { windowsHide: true, stdio: 'ignore' });
          assert.equal(result.status, 0, 'fixture ACL operation failed');
        };
        // Save every owned descendant before explicit deny ACEs. Node
        // lstat can query metadata despite deny; assert a real denied read.
        aclCommand([probe, '/save', acl, '/t', '/q']);
        restore = () => aclCommand([dirname(probe), '/restore', acl, '/q']);
        verifyRestored = () => {
          aclCommand([probe, '/save', restored, '/t', '/q']);
          const before = readFileSync(acl), after = readFileSync(restored);
          const descriptors = (bytes: Buffer) => bytes.toString('utf16le').split(/\r?\n/).filter(line => line.startsWith('D:')).map(line => line.replace(/S-1-\d+(?:-\d+)+/g, sid => 'S-MASKED'));
          console.log('SAFE_ACL_COMPARE ' + JSON.stringify({ equal: before.equals(after), beforeBytes: before.length, afterBytes: after.length, before: descriptors(before), after: descriptors(after) }));
          assert.deepEqual(after, before, 'all owned ACLs must be restored exactly');
        };
        // Native-private children have protected DACLs: deny the actual
        // read target explicitly before denying its container.
        aclCommand([join(probe, 'keep'), '/deny', `*${sid}:(F)`, '/q']);
        aclCommand([probe, '/deny', `*${sid}:(F)`, '/q']);
      } else {
        const mode = statSync(parent).mode & 0o777;
        chmodSync(parent, 0o000); restore = () => chmodSync(parent, mode);
      }
    }
    const errno = kind === 'file ancestor' ? windows ? 'ENOENT' : 'ENOTDIR' : 'EACCES';
    if (kind === 'inaccessible read') {
      // Only a real failed read (not metadata lookup) is a native control.
      let code: unknown = 'NO_ERROR';
      try { if (windows) readFileSync(join(probe, 'keep')); else lstatSync(probe); }
      catch (error) { code = (error as NodeJS.ErrnoException | undefined)?.code; }
      // Never pass the raw error to assert: its path/message may be private.
      const diagnostic = ['NO_ERROR', 'EACCES', 'EPERM', 'ENOENT', 'ENOTDIR', 'EIO', 'EBUSY', 'EBADF', 'EISDIR']
        .find(known => known === code) ?? 'UNKNOWN';
      if (windows) t.diagnostic(`native denied-read control code=${diagnostic}`);
      assert.ok(isReadDenial(code, windows), `the control must reach a real access-denied read (code=${diagnostic})`);
      if (windows) assert.equal(lstatSync(probe).isDirectory(), true, 'metadata lookup is not a denied file read');
    } else {
      assert.throws(() => lstatSync(probe), (error: unknown) => (error as NodeJS.ErrnoException).code === errno,
        'the control must reach the platform actual filesystem refusal');
    }
    const descriptor = Object.getOwnPropertyDescriptor(process, 'platform')!;
    try {
      // Linux reaches real terminal lstat errors. Native Windows instead
      // reaches the real helper's non-directory/inaccessible-parent open.
      Object.defineProperty(process, 'platform', { ...descriptor, value: 'win32' });
      for (const invoke of [() => writePrivate(join(probe, 'authority'), 'private-input-SENTINEL'), () => makePrivateDirectory(join(probe, 'directory'))]) {
        assertPrivateDiagnostic(invoke, sentinel, windows ? undefined : errno);
      }
    } finally { Object.defineProperty(process, 'platform', descriptor); }
  });
}

test('private executable fixture preserves bytes and identity through native validation', t => {
  const dir = sandbox(t), executable = privateExecutable(dir, process.execPath, windows ? 'owned-node.exe' : 'owned-node');
  const result = childProcess.spawnSync(executable, ['--version'], { encoding: 'utf8', windowsHide: true });
  assert.equal(result.status, 0); assert.equal(result.stdout.trim(), process.version);
  assert.throws(() => privateExecutable(dir, process.execPath, windows ? 'owned-node.exe' : 'owned-node'),
    'an existing executable must never be overwritten');
});

test('owned fixture cleanup reverses acquisition and still drains after a failure', async () => {
  const after: (() => void | Promise<void>)[] = [], order: string[] = [];
  const context: CleanupContext = { after: close => { after.push(() => close(undefined as unknown as TestContext)); } };
  const dir = sandbox(context);
  onCleanup(context, () => { order.push('root'); });
  try {
    const fake = await startFakeCodexAppServer({ dir: join(dir, 'fake') });
    onCleanup(context, async () => { order.push('server'); await fake.close(); });
    const child = childProcess.spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
    const closed = new Promise<void>(resolve => child.once('close', () => resolve()));
    onCleanup(context, async () => {
      order.push('process');
      if (child.exitCode === null && child.signalCode === null) child.kill();
      await closed; throw new Error('controlled cleanup failure');
    });
    await once(child, 'spawn');
    const socket = createConnection(windows ? fake.socketPath : realpathSync(fake.socketPath));
    onCleanup(context, () => { order.push('connector'); socket.destroy(); });
    await once(socket, 'connect');
    assert.equal(after.length, 1);
  } finally {
    await assert.rejects(async () => after[0](), AggregateError);
  }
  assert.deepEqual(order, ['connector', 'process', 'server', 'root']);
  assert.equal(existsSync(dirname(dir)), false, 'owned root is deleted only after socket/server and child close');
});

test('Windows DACL reads enforce private ownership for credentials and bindings; no chmod bypass', { skip: !windows }, async t => {
  const dir = sandbox(t), credential = join(dir, 'credential'), bindings = join(dir, 'bindings.json');
  writePrivate(credential, 'vn_peer_fixture-only\n'); writePrivate(bindings, '{"version":1,"bindings":[]}');
  assert.equal(await readCredential(credential), 'vn_peer_fixture-only');
  assert.equal(readBindings(bindings, -1).state, 'ok');
  makePublicReadable(credential); makePublicReadable(bindings);
  await assert.rejects(readCredential(credential), e => !String(e).includes('vn_peer_fixture-only'));
  assert.equal(readBindings(bindings, -1).state, 'refused');
  makeOwnerPrivate(credential); makeOwnerPrivate(bindings);
  assert.equal(await readCredential(credential), 'vn_peer_fixture-only');
  assert.equal(readBindings(bindings, -1).state, 'ok');
  const hardlink = join(dir, 'hardlink'); linkSync(bindings, hardlink);
  assert.equal(readBindings(hardlink, -1).state, 'refused', 'hard links cannot be authorities');
});

test('Windows native private reads are bounded and fail closed without the helper', { skip: !windows }, t => {
  const dir = sandbox(t), path = join(dir, 'large');
  writePrivate(path, 'x'.repeat(1_048_576));
  assert.equal(readPrivateFileSync(path).length, 1_048_576);
  writeFileSync(path, 'x'.repeat(1_048_577)); assert.throws(() => readPrivateFileSync(path));
  writeFileSync(path, 'private');
  assert.throws(() => readPrivateFileSync(path, { env: { ...process.env, VECNET_BIN: join(dir, 'missing.exe') } }));
});

test('Windows recovery selects explicit HOME, rejects public DACLs and junction descendants', { skip: !windows }, t => {
  const home = sandbox(t), env = { ...process.env, HOME: home, USERPROFILE: join(home, 'unselected') };
  const expected = join(home, '.local', 'state', 'vecnet', 'codex', 'bindings.json');
  assert.equal(codexBindingsPath(env), expected);
  const db = codexRecoveryPath(env); assert.equal(db, join(home, '.local', 'state', 'vecnet', 'codex', 'delivery.sqlite'));
  const child = join(home, 'alias-home'), target = join(home, 'target'); mkdirSync(child); mkdirSync(target);
  symlinkSync(target, join(child, '.local'));
  assert.throws(() => codexRecoveryPath({ ...env, HOME: child }));
  writePrivate(db + '-wal', ''); makePublicReadable(db + '-wal');
  assert.throws(() => codexRecoveryPath(env));
});

test('Windows publisher observes native helper loss and cannot publish or delete a successor', { skip: !windows, timeout: 10000 }, async t => {
  const dir = sandbox(t), path = join(dir, 'bindings'); writePrivate(path, '{}');
  const spawn = childProcess.spawn; let helper: ReturnType<typeof spawn> | undefined;
  const mock = t.mock.method(childProcess, 'spawn', (...args: Parameters<typeof spawn>) => {
    const child = Reflect.apply(spawn, childProcess, args);
    if ((args[1] as string[]).includes('lock')) helper = child;
    return child;
  }); syncBuiltinESMExports();
  onCleanup(t, () => { mock.mock.restore(); syncBuiltinESMExports(); });
  let lost = 0;
  const publisher = await openStatusPublisher(path, -1, { onLost() { lost++; } });
  onCleanup(t, async () => { await publisher?.close(); }); assert.ok(publisher); assert.ok(helper); publisher.publish(0, []);
  const died = once(helper, 'close'); helper.kill(); await died;
  assert.equal(lost, 1); assert.equal(publisher.valid(), false); assert.throws(() => publisher.publish(1, []));
  const next = await openStatusPublisher(path, -1); assert.ok(next);
  try { next.publish(2, []); await publisher.close(); assert.equal(existsSync(path + '.status.json'), true); }
  finally { await next.close(); }
});

/** Live exact-root fixtures; native DACL checks and the held lock stay real. */
async function liveBinding(t: TestContext) {
  const home = sandbox(t);
  const node = privateExecutable(home, process.execPath, 'node.exe');
  const cli = privateExecutable(home, process.env.VECNET_BIN!, 'vecnet.exe');
  const env = { ...process.env, HOME: home, VECNET_BIN: cli, VECNET_CODEX_BIN: node };
  const fake = await startFakeCodexAppServer({ dir: join(home, 'fake') });
  onCleanup(t, () => fake.close());
  const codexHome = join(home, 'codex'), control = join(codexHome, 'app-server-control');
  makePrivateDirectory(control); writePrivate(join(control, 'app-server-control.sock'), 'fixture only');
  const path = codexBindingsPath(env)!, config = join(home, 'config.json'), trace = join(home, 'trace'), claims = join(home, 'claims');
  const id = '00000000-0000-4000-8000-000000000001';
  fake.setThread({ id, status: { type: 'idle' }, source: 'cli', approvalPolicy: 'never' }); fake.attachOther(id);
  const owner = { configPath: config, profile: 'test', as: '', hub: 'https://hub.invalid', caller: 'peer',
    codexHome, threadId: id, generation: 'a'.repeat(32) };
  writePrivate(trace, ''); writePrivate(claims, '{}');
  writePrivate(config, JSON.stringify({ owner, trace, claims, revision: 0, events: [], items: [] }));
  localPlatform('mkdir-private', join(home, '.local', 'state', 'vecnet', 'codex'));
  writePrivate(path, JSON.stringify({ version: 1, bindings: [{ config_path: config, profile: owner.profile, as: '',
    hub: owner.hub, caller: owner.caller, codex_home: codexHome, thread_id: id, generation: owner.generation,
    created_at: '2026-01-01T00:00:00.000Z', expires_at: '2030-01-01T00:00:00.000Z', attended: true }] }));
  const status = () => { try { return JSON.parse(readFileSync(path + '.status.json', 'utf8')); } catch { return undefined; } };
  const ready = () => status()?.bindings?.some((b: any) => b.thread_id === id && b.generation === owner.generation && b.state === 'ready' && b.liveness.live === true) === true;
  const watches = () => readFileSync(trace, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line)).filter(r => r.watchRevision === 0).length;
  return { home, env, node, fake, path, id, status, ready, watches };
}

function mockLiveProcesses(t: TestContext, pipe: string) {
  const helpers: childProcess.ChildProcess[] = [], children: childProcess.ChildProcess[] = [];
  const closed: Promise<void>[] = [];
  onCleanup(t, async () => {
    for (const child of children) if (child.exitCode === null && child.signalCode === null) child.kill();
    await Promise.all(closed);
  });
  const run = fixtureSpawn(childProcess.spawn, { pipe, onLock: (child: childProcess.ChildProcess) => helpers.push(child) });
  const mock = t.mock.method(childProcess, 'spawn', (...args: Parameters<typeof childProcess.spawn>) => {
    const child = run(...args); children.push(child);
    closed.push(new Promise(resolve => child.once('close', () => resolve()))); return child;
  }); syncBuiltinESMExports();
  onCleanup(t, () => { mock.mock.restore(); syncBuiltinESMExports(); });
  return { helpers, children };
}

test('Windows live connector exposes fatal helper loss only after fencing, and a replacement regains readiness', { skip: !windows, timeout: 15000 }, async t => {
  console.log('LIVE_PROBE connector stage=1 before');
  const s = await liveBinding(t), { helpers, children } = mockLiveProcesses(t, s.fake.socketPath);
  console.log('LIVE_PROBE connector stage=1 after');
  const clock = fakeClock(Date.now()), log = memoryLog(), reports: { reason?: string }[] = [];
  const start = () => startCodexConnector({ env: s.env, log, clock, postRuntimeStatus: async (_b, snapshot) => { reports.push(snapshot); return true; } });
  const first = start(); onCleanup(t, () => first.stop());
  console.log('LIVE_PROBE connector stage=2 before');
  await until(() => s.watches() === 1 && s.fake.subscribers(s.id).length === 1, 'live bound watch and subscription');
  console.log('LIVE_PROBE connector stage=2 after');
  console.log('LIVE_PROBE connector stage=3 before');
  clock.advance(CODEX_RELOAD_MS); await until(s.ready, 'ready live binding before helper death');
  console.log('LIVE_PROBE connector stage=3 after');
  const oldSubscriber = s.fake.subscribers(s.id)[0], firstChildren = [...children]; assert.equal(helpers.length, 1);
  helpers[0].kill();
  console.log('LIVE_PROBE connector stage=4 before');
  assert.deepEqual(await first.stopped, { reason: 'status_lock_lost' }); // no explicit stop/signal
  console.log('LIVE_PROBE connector stage=4 after');
  assert.equal(s.fake.subscribers(s.id).length, 0, 'fatal outcome waits for unsubscribe');
  console.log('LIVE_PROBE connector stage=5 before');
  await until(() => firstChildren.every(c => c.exitCode !== null || c.signalCode !== null), 'owned children exit after fenced transport close');
  console.log('LIVE_PROBE connector stage=5 after');
  assert.equal(reports.some(r => r.reason === 'stopped'), false, 'no offline report after singleton loss');
  const count = children.length; clock.advance(CODEX_RELOAD_MS * 2);
  assert.equal(children.length, count, 'old connector never self-revives without a singleton');
  const second = start(); onCleanup(t, () => second.stop());
  console.log('LIVE_PROBE connector stage=6 before');
  await until(() => s.watches() === 2 && s.fake.subscribers(s.id).length === 1, 'replacement live binding');
  console.log('LIVE_PROBE connector stage=6 after');
  assert.notEqual(s.fake.subscribers(s.id)[0], oldSubscriber); assert.equal(helpers.length, 2);
  console.log('LIVE_PROBE connector stage=7 before');
  clock.advance(CODEX_RELOAD_MS); await until(s.ready, 'replacement readiness');
  console.log('LIVE_PROBE connector stage=7 after');
  const replacementStatus = readFileSync(s.path + '.status.json', 'utf8');
  console.log('LIVE_PROBE connector stage=8 before');
  await first.stop(); assert.equal(readFileSync(s.path + '.status.json', 'utf8'), replacementStatus, 'old cleanup cannot delete successor readiness');
  console.log('LIVE_PROBE connector stage=8 after');
  console.log('LIVE_PROBE connector stage=9 before');
  await second.stop(); assert.deepEqual(await second.stopped, { reason: 'stopped' });
  console.log('LIVE_PROBE connector stage=9 after');
  assert.equal(existsSync(s.path + '.status.json'), false); assert.equal(reports.at(-1)?.reason, 'stopped');
});


test('Windows standalone live runner exits 1 on actual helper death and restarts ready, normal stop remains exit 0', { skip: !windows, timeout: 15000 }, async t => {
  const s = await liveBinding(t), pidFile = join(s.home, 'helper.pid');
  const children: childProcess.ChildProcess[] = [];
  const closed: Promise<unknown>[] = [];
  onCleanup(t, async () => {
    for (const child of children) if (child.exitCode === null && child.signalCode === null) {
      if (child.connected) child.send('stop'); else child.kill();
    }
    await Promise.all(closed);
  });
  function launch() {
    const child = childProcess.spawn(s.node, ['--disable-warning=ExperimentalWarning', fileURLToPath(new URL('./fixtures/connector-process.mjs', import.meta.url))],
      { env: { ...s.env, FAKE_CODEX_PROXY_PIPE: s.fake.socketPath, FAKE_LOCK_PID_FILE: pidFile }, stdio: ['ignore', 'pipe', 'pipe', 'ipc'] });
    children.push(child);
    let stderr = ''; child.stderr!.on('data', part => { stderr += part; });
    const done = once(child, 'close'); closed.push(done); return { child, closed: done, stderr: () => stderr };
  }
  const first = launch();
  await until(() => s.ready() && s.watches() === 1 || first.child.exitCode !== null, 'standalone live readiness');
  assert.equal(first.child.exitCode, null, first.stderr()); assert.ok(s.ready());
  const oldSubscriber = s.fake.subscribers(s.id)[0];
  process.kill(Number(readFileSync(pidFile, 'utf8')));
  assert.deepEqual(await first.closed, [1, null], first.stderr());
  assert.match(first.stderr(), /codex_status_lock_lost/); assert.match(first.stderr(), /codex delivery: status lock lost/);
  assert.equal(s.fake.subscribers(s.id).length, 0, 'nonzero exit follows exact-root unsubscribe');
  const before = s.status().updated_at, second = launch();
  await until(() => s.ready() && s.watches() === 2 && s.status().updated_at !== before || second.child.exitCode !== null, 'restarted standalone live readiness');
  assert.equal(second.child.exitCode, null, second.stderr()); assert.ok(s.ready());
  assert.notEqual(s.fake.subscribers(s.id)[0], oldSubscriber);
  second.child.send('stop'); assert.deepEqual(await second.closed, [0, null], second.stderr());
  assert.equal(existsSync(s.path + '.status.json'), false, 'normal stop removes owned readiness');
});

test('Windows embedded live helper loss stops the launcher nonzero, then a fresh launcher becomes ready', { skip: !windows, timeout: 15000 }, async t => {
  const s = await liveBinding(t), { helpers } = mockLiveProcesses(t, s.fake.socketPath);
  const previousExit = process.exitCode; onCleanup(t, () => { process.exitCode = previousExit; });
  const daemonCleanup: (() => void | Promise<void>)[] = [];
  onCleanup(t, async () => {
    // The harness owns its stop-before-delete callback, then its fake hub.
    const errors: unknown[] = [];
    for (const close of daemonCleanup) { try { await close(); } catch (error) {
          const message = error instanceof Error ? error.message : "";
          const code = ["EACCES", "EPERM", "ENOENT", "ENOTDIR"].find(code => (error as NodeJS.ErrnoException)?.code === code) ?? "OTHER";
          const reason = ["fixture ACL operation failed", "all owned ACLs must be restored exactly", "secret file must be a regular file readable only by its owner"].find(known => message.startsWith(known)) ?? "other-assertion";
          console.log(`SAFE_CLEANUP_PROBE stage=${cleanupStage} code=${code} reason=${reason}`);
          errors.push(error);
        } }
    if (errors.length) throw new AggregateError(errors, 'daemon fixture cleanup failed');
  });
  const h = await harness({ after: close => { daemonCleanup.push(() => close(t)); } }, { env: s.env });
  await until(() => s.watches() === 1 && s.fake.subscribers(s.id).length === 1, 'embedded live binding');
  h.clock.advance(CODEX_RELOAD_MS); await until(s.ready, 'embedded readiness');
  helpers[0].kill(); await h.daemon.stopped; // no signal and no explicit stop
  assert.equal(process.exitCode, 1); assert.equal(s.fake.subscribers(s.id).length, 0);
  assert.ok(h.log.entries.some(e => e.msg === 'codex connector stopped' && e.meta?.reason === 'status_lock_lost'));
  assert.equal(h.presence().at(-1)?.state, 'offline', 'launcher cannot remain healthy-looking');
  process.exitCode = previousExit;
  await h.restart();
  await until(() => s.watches() === 2 && s.fake.subscribers(s.id).length === 1, 'fresh embedded live binding');
  h.clock.advance(CODEX_RELOAD_MS); await until(s.ready, 'fresh embedded readiness');
  assert.equal(helpers.length, 2); await h.daemon.stop(); assert.equal(process.exitCode, previousExit);
});

for (const [name, open] of [['exact-root delivery', openDeliveryRpc], ['launcher backend', openBackendRpc]] as const) {
  test(`Windows ${name} uses proxy-only binary RFC6455 tunnel to the existing server`, { skip: !windows, timeout: 10000 }, async t => {
    const dir = sandbox(t), fake = await startFakeCodexAppServer({ dir: join(dir, 'fake') }); onCleanup(t, () => fake.close());
    fake.setThread({ id: 'exact-root', status: { type: 'active', activeFlags: [] } });
    const node = privateExecutable(dir, process.execPath, 'node.exe');
    const home = join(dir, 'home'), control = join(home, 'app-server-control'); makePrivateDirectory(control);
    writePrivate(join(control, 'app-server-control.sock'), 'fixture only');
    const spawn = childProcess.spawn, calls: string[][] = [], children: childProcess.ChildProcess[] = [], closed: Promise<void>[] = [];
    onCleanup(t, async () => {
      for (const child of children) if (child.exitCode === null && child.signalCode === null) child.kill();
      await Promise.all(closed);
    });
    const mock = t.mock.method(childProcess, 'spawn', (...args: Parameters<typeof spawn>) => {
      const argv = args[1] as string[];
      if (argv[0] === 'app-server') {
        calls.push(argv);
        assert.deepEqual(argv, ['app-server', 'proxy', '--sock', join(control, 'app-server-control.sock')]);
        const options = args[2] as childProcess.SpawnOptions;
        assert.equal(options.env?.CODEX_HOME, home);
        assert.equal(options.env?.CODEX_THREAD_ID, undefined);
        assert.equal(options.env?.OPENAI_API_KEY, undefined);
        assert.equal(options.env?.VECNET_TOKEN, undefined);
        const child = spawn(node, [fileURLToPath(new URL('./fixtures/fake-codex-proxy.mjs', import.meta.url))],
          { ...options, env: { ...options.env, FAKE_CODEX_PROXY_PIPE: fake.socketPath } });
        children.push(child); closed.push(new Promise(resolve => child.once('close', () => resolve()))); return child;
      }
      return Reflect.apply(spawn, childProcess, args);
    }); syncBuiltinESMExports();
    onCleanup(t, () => { mock.mock.restore(); syncBuiltinESMExports(); });
    const rpc = await open({ codexHome: home, euid: -1, log: memoryLog(), timeoutMs: 1000, env: { ...process.env, VECNET_CODEX_BIN: node } });
    onCleanup(t, () => rpc.close());
    assert.equal(rpc.version, '0.166.0');
    const result = await rpc.call<{ thread: { id: string } }>('thread/read', { threadId: 'exact-root', includeTurns: false }, 1000);
    assert.equal(result.thread.id, 'exact-root'); assert.deepEqual(fake.paths, ['/']); assert.equal(calls.length, 1);
    assert.deepEqual(fake.requests.filter(r => r.method !== 'initialized').map(r => r.method), ['initialize', 'thread/read']);
    rpc.close();
  });
}

test('Windows custom CODEX_HOME finds the native install under USERPROFILE without APPDATA or PATH shims', { skip: !windows }, t => {
  const dir = sandbox(t), selected = join(dir, 'service-home'), user = join(dir, 'user-profile'), custom = join(dir, 'custom-codex');
  makePrivateDirectory(selected); makePrivateDirectory(user); makePrivateDirectory(custom);
  const architecture = process.arch === 'arm64' ? 'aarch64' : 'x86_64';
  const binDir = join(user, '.codex', 'packages', 'app-server-daemon', 'releases', `0.169.0-${architecture}-pc-windows-msvc`, 'bin');
  makePrivateDirectory(binDir);
  const executable = join(binDir, 'codex.exe');
  privateExecutable(binDir, process.execPath, 'codex.exe');
  assert.equal(nativeCodexBinary(custom, { HOME: selected, USERPROFILE: user, PATH: '', SystemRoot: process.env.SystemRoot,
    VECNET_BIN: process.env.VECNET_BIN }), executable);
});

test('Windows held helper receives only the CLI environment allowlist, not session or auth data', { skip: !windows }, async t => {
  const dir = sandbox(t), path = join(dir, 'bindings'); writePrivate(path, '{}');
  const spawn = childProcess.spawn; let observed: NodeJS.ProcessEnv | undefined;
  const mock = t.mock.method(childProcess, 'spawn', (...args: Parameters<typeof spawn>) => {
    if ((args[1] as string[]).includes('lock')) observed = (args[2] as childProcess.SpawnOptions).env;
    return Reflect.apply(spawn, childProcess, args);
  }); syncBuiltinESMExports();
  onCleanup(t, () => { mock.mock.restore(); syncBuiltinESMExports(); });
  const publisher = await openStatusPublisher(path, -1, { env: { ...process.env, VECNET_TOKEN: 'private', OPENAI_API_KEY: 'private',
    CODEX_HOME: '/unselected', CODEX_SESSION_ID: 'private', ARBITRARY: 'private' } });
  assert.ok(publisher);
  try {
    assert.ok(observed?.VECNET_BIN); assert.ok(observed?.SystemRoot);
    for (const key of ['VECNET_TOKEN', 'OPENAI_API_KEY', 'CODEX_HOME', 'CODEX_SESSION_ID', 'ARBITRARY']) assert.equal(observed?.[key], undefined);
  } finally { await publisher.close(); }
});
