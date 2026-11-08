import test, { beforeEach } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { createServer } from 'node:net';
import type { Socket } from 'node:net';
import { createHash } from 'node:crypto';
import { syncBuiltinESMExports } from 'node:module';
import { mkdtemp, rm, chmod, readlink, writeFile, symlink, mkdir } from 'node:fs/promises';
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
  assert.deepEqual(CODEX_APPSERVER_VERSIONS, ['0.166.0', '0.167.0']);
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

test('connects to re-verified 0.167.0', { timeout: 5000 }, async t => {
  const { open } = await setup(t, '0.167.0'), rpc = await open();
  assert.equal(rpc.version, '0.167.0');
  assert.equal((await read(rpc)).thread.id, 't');
});

test('unknown version is refused', { timeout: 5000 }, async t => {
  const { fake, open, log } = await setup(t, '0.168.0');
  await assert.rejects(open(), (e: any) => e instanceof CodexRpcError && e.code === -1 && e.message === 'refused_version' && !e.ambiguous);
  assert.deepEqual(fake.requests.map(r => r.method), ['initialize']);
  assert.equal(log.entries.filter(e => e.level === 'error').length, 1);
  assert.equal(log.entries[0].msg, 'refused_version');
});

test('server requests are dropped without a reply', { timeout: 5000 }, async t => {
  const { fake, clock, log, open } = await setup(t), rpc = await open();
  await rpc.call('thread/resume', buildThreadResume('t'), 1000);
  // Mock client timers only after connecting; the server has its own controlled clock.
  t.mock.timers.enable({ apis: ['setTimeout'] });
  for (const method of ['item/commandExecution/requestApproval', 'item/tool/requestUserInput']) {
    fake.serverRequest('t', method, { secret: 'DO-NOT-LOG', questions: ['peer body'] });
  }
  // The barrier drains the incoming requests and clears its RPC deadline before ticking.
  await read(rpc);
  clock.advance(2000); t.mock.timers.tick(2000);
  // Flush any delayed reply onto the wire before checking the server's observations.
  await read(rpc);
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

test('configRequirements/read crosses the socket once without admitting mutation or other methods', { timeout: 5000 }, async t => {
  const dir = roots.get(t)!, targetDir = join(dir, 'policy-socket'), codexHome = join(dir, 'policy-home');
  await mkdir(targetDir, { mode: 0o700 }); await chmod(targetDir, 0o700);
  await mkdir(join(codexHome, 'app-server-control'), { recursive: true, mode: 0o700 });
  const target = join(targetDir, 'rpc.sock');
  const requests: { id?: number; method: string; params?: unknown }[] = [], paths: string[] = [];
  const sockets = new Set<Socket>();
  const expected = { requirements: null, sources: [{ kind: 'managed', detail: 'finite-socket-only' }],
    future: { revision: 7, flags: [false, null, 'retained'] } };
  // Independent finite wire fake: no changes to the shared app-server fixture,
  // and no production framing or RPC implementation substituted by the test.
  const server = createServer(socket => {
    sockets.add(socket); socket.on('error', () => {});
    let bytes: Buffer = Buffer.alloc(0), upgraded = false, policyId: number | undefined;
    const send = (value: unknown) => {
      const body = Buffer.from(JSON.stringify(value)), header = Buffer.alloc(body.length < 126 ? 2 : 4);
      header[0] = 0x81;
      if (header.length === 2) header[1] = body.length;
      else { header[1] = 126; header.writeUInt16BE(body.length, 2); }
      socket.write(Buffer.concat([header, body]));
    };
    socket.on('data', chunk => {
      bytes = Buffer.concat([bytes, chunk]);
      if (!upgraded) {
        const end = bytes.indexOf('\r\n\r\n'); if (end < 0) return;
        const head = bytes.subarray(0, end).toString('ascii'); bytes = bytes.subarray(end + 4);
        paths.push(head.split(' ')[1]);
        const key = /\r\nSec-WebSocket-Key:\s*([^\r\n]+)/i.exec(head)?.[1].trim();
        if (!key) { socket.destroy(); return; }
        const accept = createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
        socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
        upgraded = true;
      }
      while (bytes.length >= 2) {
        const op = bytes[0] & 15;
        if (op === 8) { socket.end(); return; }
        if (op !== 1 || !(bytes[0] & 128) || !(bytes[1] & 128)) { socket.destroy(); return; }
        let length = bytes[1] & 127, at = 2;
        if (length === 126) { if (bytes.length < 4) return; length = bytes.readUInt16BE(2); at = 4; }
        else if (length === 127) { socket.destroy(); return; }
        if (bytes.length < at + 4 + length) return;
        const maskAt = at, body = Buffer.from(bytes.subarray(at + 4, at + 4 + length));
        body.forEach((value, n) => { body[n] = value ^ bytes[maskAt + n % 4]; });
        bytes = bytes.subarray(at + 4 + length);
        const request = JSON.parse(body.toString()) as (typeof requests)[number]; requests.push(request);
        if (request.method === 'initialize') send({ id: request.id, result: { userAgent: 'codex-tui/0.166.0 (finite)' } });
        else if (request.method === 'initialized') continue;
        else if (request.method === 'configRequirements/read') policyId = request.id;
        else if (request.method === 'thread/read') {
          // Resolve the concurrent thread call first and include an unrelated ID:
          // only the matching policy reply may settle the requirements read.
          send({ id: request.id, result: { thread: { id: 't' } } });
          if (policyId !== undefined) {
            send({ id: policyId + 1000, result: { requirements: { wrongCorrelation: true } } });
            send({ id: policyId, result: expected }); policyId = undefined;
          }
        } else send({ id: request.id, error: { code: -32601, message: 'unexpected fake method' } });
      }
    });
  });
  t.after(async () => { for (const socket of sockets) socket.destroy(); await new Promise<void>(resolve => server.close(() => resolve())); });
  await new Promise<void>((resolve, reject) => { server.once('error', reject); server.listen(target, resolve); });
  await symlink(target, socketPathFor(codexHome));
  const rpc = await openCodexRpc({ codexHome, euid, log: memoryLog(), timeoutMs: 1000 }); t.after(() => rpc.close());
  // Cast only at the existing runtime-boundary seam. This keeps baseline tests
  // importable before AllowedMethod gains the read method: RED must be behavior.
  for (const method of ['config/value/write', 'config/batchWrite', 'configRequirements/write', 'config/read',
    'configRequirements/readExtra', 'thread/start', 'turn/interrupt', 'settings/update']) {
    await assert.rejects(rpc.call(method as AllowedMethod, {}, 1000),
      (error: unknown) => error instanceof CodexRpcError && error.code === -1 && error.message === 'forbidden_method' && !error.ambiguous);
  }
  await read(rpc); // Socket barrier: every forbidden call above must be absent.
  assert.deepEqual(requests.map(request => request.method), ['initialize', 'initialized', 'thread/read']);
  const requirements = rpc.call<typeof expected>('configRequirements/read' as AllowedMethod, {}, 1000);
  const barrier = read(rpc);
  const [result, thread] = await Promise.all([requirements, barrier]);
  assert.deepEqual(result, expected); assert.equal(thread.thread.id, 't');
  assert.deepEqual(requests.map(request => request.method),
    ['initialize', 'initialized', 'thread/read', 'configRequirements/read', 'thread/read']);
  const policyRequest = requests[3];
  assert.equal(typeof policyRequest.id, 'number'); assert.notEqual(policyRequest.id, requests[4].id);
  assert.deepEqual(policyRequest.params, {});
  rpc.close();
  // Closing the only connection bounds the observation: no retry, fallback,
  // settings/config mutation, thread creation or hidden observer connection.
  assert.deepEqual(paths, ['/']); assert.equal(sockets.size, 1);
  assert.deepEqual(requests.map(request => request.method),
    ['initialize', 'initialized', 'thread/read', 'configRequirements/read', 'thread/read']);
});
