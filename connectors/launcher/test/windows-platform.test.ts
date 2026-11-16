import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, copyFileSync, linkSync, existsSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { once } from 'node:events';
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
function sandbox(t: { after(fn: () => void): void }) {
  const dir = mkdtempSync(join(tmpdir(), 'vecnet-native-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  makePrivateDirectory(dir); return dir;
}

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
  t.after(() => { mock.mock.restore(); syncBuiltinESMExports(); });
  let lost = 0;
  const publisher = await openStatusPublisher(path, -1, { onLost() { lost++; } }); assert.ok(publisher); assert.ok(helper);
  t.after(() => publisher.close()); publisher.publish(0, []);
  const died = once(helper, 'close'); helper.kill(); await died;
  assert.equal(lost, 1); assert.equal(publisher.valid(), false); assert.throws(() => publisher.publish(1, []));
  const next = await openStatusPublisher(path, -1); assert.ok(next);
  try { next.publish(2, []); await publisher.close(); assert.equal(existsSync(path + '.status.json'), true); }
  finally { await next.close(); }
});

/** Live exact-root fixtures; native DACL checks and the held lock stay real. */
async function liveBinding(t: TestContext) {
  const home = sandbox(t), env = { ...process.env, HOME: home, VECNET_CODEX_BIN: process.execPath };
  const fake = await startFakeCodexAppServer({ dir: join(home, 'fake') });
  t.after(() => fake.close());
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
  return { home, env, fake, path, id, status, ready, watches };
}

function mockLiveProcesses(t: TestContext, pipe: string) {
  const helpers: childProcess.ChildProcess[] = [], children: childProcess.ChildProcess[] = [];
  const run = fixtureSpawn(childProcess.spawn, { pipe, onLock: (child: childProcess.ChildProcess) => helpers.push(child) });
  const mock = t.mock.method(childProcess, 'spawn', (...args: Parameters<typeof childProcess.spawn>) => {
    const child = run(...args); children.push(child); return child;
  }); syncBuiltinESMExports();
  t.after(() => { mock.mock.restore(); syncBuiltinESMExports(); });
  return { helpers, children };
}

test('Windows live connector exposes fatal helper loss only after fencing, and a replacement regains readiness', { skip: !windows, timeout: 15000 }, async t => {
  const s = await liveBinding(t), { helpers, children } = mockLiveProcesses(t, s.fake.socketPath);
  const clock = fakeClock(Date.now()), log = memoryLog(), reports: { reason?: string }[] = [];
  const start = () => startCodexConnector({ env: s.env, log, clock, postRuntimeStatus: async (_b, snapshot) => { reports.push(snapshot); return true; } });
  const first = start(); t.after(() => first.stop());
  await until(() => s.watches() === 1 && s.fake.subscribers(s.id).length === 1, 'live bound watch and subscription');
  clock.advance(CODEX_RELOAD_MS); await until(s.ready, 'ready live binding before helper death');
  const oldSubscriber = s.fake.subscribers(s.id)[0], firstChildren = [...children]; assert.equal(helpers.length, 1);
  helpers[0].kill();
  assert.deepEqual(await first.stopped, { reason: 'status_lock_lost' }); // no explicit stop/signal
  assert.equal(s.fake.subscribers(s.id).length, 0, 'fatal outcome waits for unsubscribe');
  await until(() => firstChildren.every(c => c.exitCode !== null || c.signalCode !== null), 'owned children exit after fenced transport close');
  assert.equal(reports.some(r => r.reason === 'stopped'), false, 'no offline report after singleton loss');
  const count = children.length; clock.advance(CODEX_RELOAD_MS * 2);
  assert.equal(children.length, count, 'old connector never self-revives without a singleton');
  const second = start(); t.after(() => second.stop());
  await until(() => s.watches() === 2 && s.fake.subscribers(s.id).length === 1, 'replacement live binding');
  assert.notEqual(s.fake.subscribers(s.id)[0], oldSubscriber); assert.equal(helpers.length, 2);
  clock.advance(CODEX_RELOAD_MS); await until(s.ready, 'replacement readiness');
  const replacementStatus = readFileSync(s.path + '.status.json', 'utf8');
  await first.stop(); assert.equal(readFileSync(s.path + '.status.json', 'utf8'), replacementStatus, 'old cleanup cannot delete successor readiness');
  await second.stop(); assert.deepEqual(await second.stopped, { reason: 'stopped' });
  assert.equal(existsSync(s.path + '.status.json'), false); assert.equal(reports.at(-1)?.reason, 'stopped');
});

test('Windows standalone live runner exits 1 on actual helper death and restarts ready, normal stop remains exit 0', { skip: !windows, timeout: 15000 }, async t => {
  const s = await liveBinding(t), pidFile = join(s.home, 'helper.pid');
  const children: childProcess.ChildProcess[] = [];
  t.after(() => { for (const child of children) if (child.exitCode === null && child.signalCode === null) child.kill(); });
  function launch() {
    const child = childProcess.spawn(process.execPath, ['--disable-warning=ExperimentalWarning', fileURLToPath(new URL('./fixtures/connector-process.mjs', import.meta.url))],
      { env: { ...s.env, FAKE_CODEX_PROXY_PIPE: s.fake.socketPath, FAKE_LOCK_PID_FILE: pidFile }, stdio: ['ignore', 'pipe', 'pipe', 'ipc'] });
    children.push(child);
    let stderr = ''; child.stderr!.on('data', part => { stderr += part; });
    const closed = once(child, 'close'); return { child, closed, stderr: () => stderr };
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
  const previousExit = process.exitCode; t.after(() => { process.exitCode = previousExit; });
  const h = await harness(t, { env: s.env });
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
    const dir = sandbox(t), fake = await startFakeCodexAppServer({ dir: join(dir, 'fake') }); t.after(() => fake.close());
    fake.setThread({ id: 'exact-root', status: { type: 'active', activeFlags: [] } });
    const home = join(dir, 'home'), control = join(home, 'app-server-control'); makePrivateDirectory(control);
    writePrivate(join(control, 'app-server-control.sock'), 'fixture only');
    const spawn = childProcess.spawn, calls: string[][] = [];
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
        return spawn(process.execPath, [fileURLToPath(new URL('./fixtures/fake-codex-proxy.mjs', import.meta.url))],
          { ...options, env: { ...options.env, FAKE_CODEX_PROXY_PIPE: fake.socketPath } });
      }
      return Reflect.apply(spawn, childProcess, args);
    }); syncBuiltinESMExports();
    t.after(() => { mock.mock.restore(); syncBuiltinESMExports(); });
    const rpc = await open({ codexHome: home, euid: -1, log: memoryLog(), timeoutMs: 1000, env: { ...process.env, VECNET_CODEX_BIN: process.execPath } });
    t.after(() => rpc.close());
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
  const executable = join(binDir, 'codex.exe'); copyFileSync(process.execPath, executable);
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
  t.after(() => { mock.mock.restore(); syncBuiltinESMExports(); });
  const publisher = await openStatusPublisher(path, -1, { env: { ...process.env, VECNET_TOKEN: 'private', OPENAI_API_KEY: 'private',
    CODEX_HOME: '/unselected', CODEX_SESSION_ID: 'private', ARBITRARY: 'private' } });
  assert.ok(publisher);
  try {
    assert.ok(observed?.VECNET_BIN); assert.ok(observed?.SystemRoot);
    for (const key of ['VECNET_TOKEN', 'OPENAI_API_KEY', 'CODEX_HOME', 'CODEX_SESSION_ID', 'ARBITRARY']) assert.equal(observed?.[key], undefined);
  } finally { await publisher.close(); }
});
