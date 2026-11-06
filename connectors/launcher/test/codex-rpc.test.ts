import test, { beforeEach } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import { mkdtemp, rm, chmod, readlink, writeFile, symlink } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { startFakeCodexAppServer } from './fake-codex-appserver.ts';
import { socketPathFor, verifySocket, parseServerVersion, CODEX_APPSERVER_VERSIONS } from '../src/codex/discover.ts';
import { openCodexRpc, CodexRpcError } from '../src/codex/rpc.ts';
import { buildThreadResume, buildTurnStart, CODEX_OPT_OUT_NOTIFICATIONS } from '../src/codex/protocol.ts';
import type { AllowedMethod } from '../src/codex/protocol.ts';

const roots = new WeakMap<TestContext, string>();
beforeEach(async t => {
  if (!('after' in t)) throw new Error('expected a test context');
  const dir = await mkdtemp('/tmp/vn-rpc-'); roots.set(t, dir);
  const names = ['VECNET_CONFIG', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'CODEX_HOME', 'VECNET_CODEX_BINDINGS'];
  const saved = names.map(n => process.env[n]);
  names.forEach((n, i) => { process.env[n] = join(dir, `isolated-${i}`); });
  t.after(async () => {
    names.forEach((n, i) => { if (saved[i] === undefined) delete process.env[n]; else process.env[n] = saved[i]; });
    await rm(dir, { recursive: true, force: true });
  });
});
const euid = process.geteuid!();
async function setup(t: TestContext, version = '0.166.0') {
  const clock = fakeClock(), dir = roots.get(t)!;
  const fake = await startFakeCodexAppServer({ dir, clock }); t.after(() => fake.close()); fake.setVersion(version);
  fake.setThread({ id: 't', status: { type: 'idle' } });
  const log = memoryLog(), options = { codexHome: join(dir, 'home'), euid, log, timeoutMs: 1000 };
  async function open() { const rpc = await openCodexRpc(options); t.after(() => rpc.close()); return rpc; }
  return { fake, clock, log, options, open };
}
const read = (rpc: Awaited<ReturnType<typeof openCodexRpc>>) => rpc.call<{ thread: { id: string } }>('thread/read', { threadId: 't' }, 1000);

test('socketPathFor and verifySocket accept the fake layout', { timeout: 5000 }, async t => {
  const { fake, options } = await setup(t);
  assert.equal(socketPathFor(options.codexHome), fake.socketPath);
  assert.deepEqual(verifySocket(fake.socketPath, euid), { ok: true, target: await readlink(fake.socketPath) });
  const relative = join(dirname(fake.socketPath), 'relative.sock');
  await symlink('../../d/' + (await readlink(fake.socketPath)).split('/').at(-1), relative);
  assert.deepEqual(verifySocket(relative, euid), { ok: true, target: await readlink(fake.socketPath) });
  assert.deepEqual(CODEX_APPSERVER_VERSIONS, ['0.166.0']);
  assert.equal(parseServerVersion('codex-tui/0.166.0 (fake)'), '0.166.0');
  for (const bad of ['', '0.166.0', ' codex/0.166.0', 'codex/0.166', 'codex tui/0.166.0']) assert.equal(parseServerVersion(bad), null);
});

test('refuses a socket outside a 0700 euid directory', { timeout: 5000 }, async t => {
  const { fake } = await setup(t), target = await readlink(fake.socketPath);
  await chmod(dirname(target), 0o755);
  assert.deepEqual(verifySocket(fake.socketPath, euid), { ok: false, reason: 'bad_dir_mode' });
  await chmod(dirname(target), 0o700);
  assert.deepEqual(verifySocket(target, euid), { ok: false, reason: 'not_symlink' });
  assert.deepEqual(verifySocket(fake.socketPath, euid + 1), { ok: false, reason: 'foreign_owner' });
  const ordinary = join(dirname(target), 'file'); await writeFile(ordinary, 'not a socket');
  const link = join(dirname(fake.socketPath), 'file.sock'); await symlink(ordinary, link);
  assert.equal(verifySocket(link, euid).ok, false);
});

test('missing socket is unreachable and runs nothing', { timeout: 5000 }, async t => {
  let spawns = 0;
  for (const name of ['spawn', 'spawnSync', 'exec', 'execSync', 'execFile', 'execFileSync', 'fork'] as const) {
    t.mock.method(childProcess, name, () => { spawns++; throw new Error('must not spawn'); });
  }
  syncBuiltinESMExports();
  t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
  const log = memoryLog();
  await assert.rejects(openCodexRpc({ codexHome: join(roots.get(t)!, 'absent'), euid, log, timeoutMs: 100 }),
    (e: any) => e instanceof CodexRpcError && e.message === 'unreachable' && e.reason === 'missing' && !e.ambiguous);
  assert.equal(spawns, 0);
});

test('connects with path / only', { timeout: 5000 }, async t => {
  const { fake, open } = await setup(t), rpc = await open(); await read(rpc);
  assert.deepEqual(fake.paths, ['/']); assert.equal(rpc.version, '0.166.0');
  assert.deepEqual(fake.requests.slice(0, 2).map(r => r.method), ['initialize', 'initialized']);
  const initialize = fake.requests[0].params as any;
  assert.deepEqual(initialize.clientInfo, { name: 'vecnet', title: 'vecnet connector', version: '0.1.0' });
  assert.equal(initialize.capabilities.experimentalApi, true);
});

test('unknown version is refused', { timeout: 5000 }, async t => {
  const { fake, open, log } = await setup(t, '0.167.0');
  await assert.rejects(open(), (e: any) => e instanceof CodexRpcError && e.code === -1 && e.message === 'refused_version' && !e.ambiguous);
  assert.deepEqual(fake.requests.map(r => r.method), ['initialize']);
  assert.equal(log.entries.filter(e => e.level === 'error').length, 1);
  assert.equal(log.entries[0].msg, 'refused_version');
});

test('server requests are dropped without a reply', { timeout: 5000 }, async t => {
  const { fake, clock, log, open } = await setup(t), rpc = await open();
  await rpc.call('thread/resume', buildThreadResume('t'), 1000);
  for (const method of ['item/commandExecution/requestApproval', 'item/tool/requestUserInput']) {
    fake.serverRequest('t', method, { secret: 'DO-NOT-LOG', questions: ['peer body'] });
  }
  await read(rpc); clock.advance(2000); await read(rpc);
  assert.deepEqual(fake.replies, []);
  assert.deepEqual(log.entries.map(e => e.meta), [{ method: 'item/commandExecution/requestApproval' }, { method: 'item/tool/requestUserInput' }]);
  assert.doesNotMatch(log.text(), /DO-NOT-LOG|peer body/);
});

test('drop before reply is ambiguous', { timeout: 5000 }, async t => {
  const { fake, open } = await setup(t), rpc = await open();
  fake.failNext('turn/start', 'drop');
  const closed: string[] = []; rpc.onClose(why => closed.push(why));
  await assert.rejects(rpc.call('turn/start', buildTurnStart({ threadId: 't', stub: 'stub', entries: [], key: 'vn.key' }), 1000),
    (e: any) => e instanceof CodexRpcError && e.ambiguous === true);
  assert.equal(closed.length, 1);
  rpc.close(); assert.equal(closed.length, 1);
  await assert.rejects(read(rpc), (e: any) => e instanceof CodexRpcError && e.ambiguous === false);
});

test('initialize opts out of delta notifications', { timeout: 5000 }, async t => {
  const { fake, open } = await setup(t), rpc = await open();
  const notices: string[] = []; rpc.onNotification(method => notices.push(method));
  await rpc.call('thread/resume', buildThreadResume('t'), 1000);
  assert.deepEqual(fake.optOuts(fake.requests[0].conn), CODEX_OPT_OUT_NOTIFICATIONS);
  fake.notifySubscribers('t', 'item/agentMessage/delta', { delta: 'hidden' });
  fake.notifySubscribers('t', 'item/completed', { item: { type: 'userMessage', clientId: 'vn.key' } });
  await read(rpc);
  assert.deepEqual(notices, ['item/completed']);
});

test('concurrent calls correlate replies and definite errors stay non-ambiguous', { timeout: 5000 }, async t => {
  const { fake, open } = await setup(t), rpc = await open();
  fake.failNext('thread/read', { code: -32600, message: 'definite rejection' });
  const rejected = assert.rejects(read(rpc), (e: any) => e instanceof CodexRpcError && e.code === -32600 && !e.ambiguous);
  const passed = read(rpc); await rejected; assert.equal((await passed).thread.id, 't');
  await assert.rejects(rpc.call('turn/interrupt' as AllowedMethod, {}, 1000), /method/i);
  await read(rpc);
  assert.equal(fake.requests.some(r => r.method === 'turn/interrupt'), false);
});
