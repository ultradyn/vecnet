// Offline Unix sockets only. These tests never start Codex or contact Fly.
import assert from 'node:assert/strict';
import test from 'node:test';
import net from 'node:net';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, chmod, lstat, readFile, readdir, readlink, rename, rm, unlink, writeFile, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { startRpcTap } from './codex-busy-rpc-tap.mjs';

const upgrade = Buffer.from('GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nAuthorization: Bearer HEADER_SECRET\r\n\r\n');
const upgraded = Buffer.from('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n');
const init = (name = 'vecnet', id = 1) => ({ id, method: 'initialize', params: { clientInfo: { name, title: 'PRIVATE_TITLE', version: 'PRIVATE_VERSION' } } });
const steer = (id = 2, threadId = 'thread-a', expectedTurnId = 'turn-a', receipt = 'vn.a') => ({
  id, method: 'turn/steer', params: { threadId, expectedTurnId, clientUserMessageId: receipt,
    input: [{ type: 'text', text: 'MESSAGE_SECRET' }], additionalContext: { secret: { value: 'CONTEXT_SECRET' } } },
});

function frame(value, { client = true, opcode = 1, fin = true, length } = {}) {
  const payload = Buffer.isBuffer(value) ? value : Buffer.from(JSON.stringify(value));
  const n = length ?? payload.length;
  const ext = n < 126 ? 0 : n <= 65535 ? 2 : 8;
  const header = Buffer.alloc(2 + ext + (client ? 4 : 0));
  header[0] = (fin ? 128 : 0) | opcode;
  header[1] = (client ? 128 : 0) | (ext === 0 ? n : ext === 2 ? 126 : 127);
  if (ext === 2) header.writeUInt16BE(n, 2);
  if (ext === 8) header.writeBigUInt64BE(BigInt(n), 2);
  const body = Buffer.from(payload);
  if (client) {
    const mask = Buffer.from([0x13, 0x37, 0x42, 0x99]);
    mask.copy(header, 2 + ext);
    for (let i = 0; i < body.length; i++) body[i] ^= mask[i % 4];
  }
  return Buffer.concat([header, body]);
}

async function until(check, why) {
  const end = Date.now() + 1500;
  while (!check()) {
    assert.ok(Date.now() < end, `timeout: ${why}`);
    await delay(2);
  }
}

function capture(socket) {
  const chunks = [];
  socket.on('data', chunk => chunks.push(chunk));
  socket.on('error', () => {});
  return { socket, bytes: () => Buffer.concat(chunks) };
}

async function fixture(t, options = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'rpc-tap-'));
  await chmod(dir, 0o700);
  const listenPath = join(dir, 'tap'), targetPath = join(dir, 'target');
  const peers = [], clients = [], events = [];
  const server = net.createServer(socket => peers.push(capture(socket)));
  server.listen(targetPath);
  await once(server, 'listening');
  let tap, closeObserved = false;
  t.after(async () => {
    for (const c of [...clients, ...peers]) c.socket.destroy();
    try { await tap?.close().catch(error => { if (!closeObserved) throw error; }); }
    finally {
      await new Promise(resolve => server.close(resolve));
      await rm(dir, { recursive: true, force: true });
    }
  });
  tap = await startRpcTap({ listenPath, targetPath, onEvent: event => events.push(event), ...options });
  async function connect() {
    const n = peers.length;
    const c = capture(net.createConnection(listenPath));
    clients.push(c);
    await once(c.socket, 'connect');
    await until(() => peers.length > n, 'target connection');
    return { c, s: peers[n] };
  }
  async function opened(name = 'vecnet') {
    const pair = await connect();
    pair.c.socket.write(Buffer.concat([upgrade, frame(init(name))]));
    await until(() => pair.s.bytes().length >= upgrade.length, 'request upgrade');
    pair.s.socket.write(Buffer.concat([upgraded, frame({ id: 1, result: {} }, { client: false })]));
    await until(() => pair.c.bytes().length >= upgraded.length, 'response upgrade');
    return pair;
  }
  async function closeOutcome() {
    try { await tap.close(); return 'resolved'; }
    catch (error) { return error.message; }
    finally { closeObserved = true; }
  }
  return { dir, listenPath, targetPath, peers, clients, events, tap, connect, opened, closeOutcome };
}

const identity = { id: 2, method: 'turn/steer', threadId: 'thread-a', expectedTurnId: 'turn-a', receiptKey: 'vn.a' };
const metadata = event => Object.fromEntries(Object.entries(event).filter(([key]) => !['seq', 'at', 'connectionId', 'client'].includes(key)));

test('fragments preserve relay bytes and metadata', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const { c, s } = await f.connect();
  const text = Buffer.from(JSON.stringify(steer()));
  const inbound = Buffer.concat([upgrade, frame(init()), frame(text.subarray(0, 31), { fin: false }),
    frame(Buffer.from('ping'), { opcode: 9 }), frame(text.subarray(31), { opcode: 0 }),
    frame({ id: 3, method: 'thread/read', params: { threadId: 'thread-a' } })]);
  // Wait for the real peer at each split so OS coalescing cannot hide a broken
  // streaming parser. Cross HTTP delimiter, frame header, mask and payload.
  let offset = 0;
  const cuts = [1, 37, upgrade.length - 3, upgrade.length - 1, upgrade.length,
    ...Array.from({ length: Math.ceil((inbound.length - upgrade.length) / 7) }, (_, i) => Math.min(inbound.length, upgrade.length + 7 * (i + 1)))];
  for (const end of cuts) {
    c.socket.write(inbound.subarray(offset, end));
    await until(() => s.bytes().length === end, 'client fragment forwarded');
    offset = end;
  }
  await until(() => s.bytes().length === inbound.length, 'all client bytes');
  assert.deepEqual(s.bytes(), inbound);
  const response = Buffer.from(JSON.stringify({ id: 2, result: { turnId: 'turn-a', content: 'RESULT_SECRET' } }));
  const outbound = Buffer.concat([upgraded, frame({ id: 1, result: {} }, { client: false }),
    frame(response.subarray(0, 11), { client: false, fin: false }), frame(Buffer.from('pong'), { client: false, opcode: 10 }),
    frame(response.subarray(11), { client: false, opcode: 0 }),
    frame({ method: 'turn/completed', params: { threadId: 'thread-a', turn: { id: 'turn-a', items: ['ITEM_SECRET'] } } }, { client: false }),
    frame({ id: 3, result: { thread: { text: 'READ_SECRET' } } }, { client: false })]);
  s.socket.write(outbound.subarray(0, 37));
  await until(() => c.bytes().length === 37, 'split server header forwarded');
  s.socket.write(outbound.subarray(37));
  await until(() => c.bytes().length === outbound.length && f.events.length === 3, 'server bytes and events');
  assert.deepEqual(c.bytes(), outbound);
  assert.deepEqual(f.events.map(metadata), [
    { kind: 'request', ...identity }, { kind: 'response', ...identity, ok: true, turnId: 'turn-a' },
    { kind: 'completed', threadId: 'thread-a', turnId: 'turn-a' },
  ]);
  assert.deepEqual(f.events.map(e => e.seq), [1, 2, 3]);
  assert.equal(new Set(f.events.map(e => e.connectionId)).size, 1);
  assert.ok(f.events.every(e => e.client === 'vecnet' && Number.isFinite(e.at) && Number.isSafeInteger(e.connectionId)));
});

test('concurrent connection ID reuse stays isolated', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const a = await f.opened(), b = await f.opened();
  a.c.socket.write(frame(steer(2, 'thread-a', 'turn-a', 'vn.a')));
  b.c.socket.write(frame(steer(2, 'thread-b', 'turn-b', 'vn.b')));
  await until(() => f.events.length === 2, 'both requests');
  b.s.socket.write(frame({ id: 2, result: { turnId: 'turn-b' } }, { client: false }));
  a.s.socket.write(frame({ id: 2, result: { turn: { id: 'turn-a' } } }, { client: false }));
  await until(() => f.events.length === 4, 'both responses');
  const responses = f.events.filter(e => e.kind === 'response');
  assert.equal(new Set(responses.map(e => e.connectionId)).size, 2);
  for (const r of responses) {
    const q = f.events.find(e => e.kind === 'request' && e.connectionId === r.connectionId);
    assert.deepEqual(metadata(r), { ...metadata(q), kind: 'response', ok: true, turnId: q.expectedTurnId });
  }
  assert.deepEqual(f.events.map(e => e.seq), [1, 2, 3, 4]);
});

test('observer client distinguished; start/read never qualify as steer', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const { c, s } = await f.opened('vecnet-busy-observer-PRIVATE_NAME');
  c.socket.write(Buffer.concat([
    frame({ id: 7, method: 'turn/start', params: { threadId: 'thread-a', input: ['START_SECRET'] } }),
    frame({ id: 8, method: 'thread/read', params: { threadId: 'thread-a' } }), frame(steer()),
  ]));
  await until(() => f.events.length === 1, 'observer request');
  s.socket.write(Buffer.concat([
    frame({ id: 7, result: { turn: { id: 'turn-a' } } }, { client: false }),
    frame({ id: 8, result: {} }, { client: false }), frame({ id: 2, result: { turnId: 'turn-a' } }, { client: false }),
  ]));
  await until(() => f.events.length === 2, 'observer response');
  assert.ok(f.events.every(e => e.client === 'observer' && e.method === 'turn/steer'));
  assert.ok(!JSON.stringify(f.events).includes('PRIVATE_NAME'));
});

test('rejected traces fail instead of success and never export arbitrary server errors', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const { c, s } = await f.opened();
  c.socket.write(frame(steer()));
  await until(() => f.events.length === 1, 'steer');
  s.socket.write(frame({ id: 2, error: { code: -32600, message: 'ERROR_SECRET', data: { token: 'TOKEN_SECRET' } } }, { client: false }));
  await until(() => f.events.some(e => e.kind === 'failure'), 'rejection failure');
  assert.deepEqual(metadata(f.events.find(e => e.kind === 'response')), { kind: 'response', ...identity, ok: false });
  assert.ok(!f.events.some(e => e.ok === true));
  assert.ok(!JSON.stringify(f.events).includes('SECRET'));
});

const invalidTraces = [
  ['oversized frame declaration', () => frame(Buffer.alloc(0), { length: 2048 })],
  ['oversized fragmented message', () => Buffer.concat([frame(Buffer.alloc(600, 32), { fin: false }), frame(Buffer.alloc(600, 32), { opcode: 0 })])],
  ['malformed JSON', () => frame(Buffer.from('{SECRET'))],
  ['invalid UTF-8', () => frame(Buffer.from([0xc3, 0x28]))],
  ['unmasked client', () => frame(steer(), { client: false })],
  ['unexpected continuation', () => frame(Buffer.from('{}'), { opcode: 0 })],
  ['interrupted text continuation', () => Buffer.concat([frame(Buffer.from('{'), { fin: false }), frame({})])],
  ['fragmented control', () => frame(Buffer.alloc(0), { opcode: 9, fin: false })],
  ['oversized control', () => frame(Buffer.alloc(126), { opcode: 9 })],
  ['reserved opcode', () => frame(Buffer.alloc(0), { opcode: 3 })],
  ['binary RPC', () => frame(Buffer.from('{}'), { opcode: 2 })],
  ['reserved bit', () => { const b = frame({}); b[0] |= 0x40; return b; }],
  ['nonminimal length', () => Buffer.from([0x81, 0xfe, 0, 1, 0, 0, 0, 0, 65])],
  ['invalid 64-bit length', () => Buffer.from([0x81, 0xff, 0x80, 0, 0, 0, 0, 0, 0, 0])],
  ['reinitialize client', () => frame(init('vecnet'))],
  ['duplicate pending ID', () => Buffer.concat([frame(steer()), frame(steer())])],
  ['invalid steer metadata', () => frame({ ...steer(), params: { ...steer().params, threadId: { secret: 'SECRET' } } })],
  ['wrong receipt field', () => frame({ ...steer(), params: { threadId: 'thread-a', expectedTurnId: 'turn-a', clientRequestId: 'vn.WRONG' } })],
  ['unbounded empty continuations', () => Buffer.concat([frame(Buffer.alloc(0), { fin: false }),
    ...Array.from({ length: 1024 }, () => frame(Buffer.alloc(0), { fin: false, opcode: 0 }))])],
  ['steer without request ID', () => frame({ method: 'turn/steer', params: steer().params })],
  ['request mixed with response', () => frame({ ...steer(), result: {} })],
  ['newline in exported identifier', () => frame(steer(2, 'thread-a\n'))],
];
for (const [name, bytes] of invalidTraces) {
  test(`oversized/malformed traces fail instead of success: ${name}`, { timeout: 5000 }, async t => {
    const f = await fixture(t, { maxMessageBytes: 1024 });
    const { c, s } = await f.opened();
    const bad = bytes(), before = s.bytes().length;
    c.socket.write(bad);
    await until(() => f.events.some(e => e.kind === 'failure') && s.bytes().length >= before + bad.length, name);
    assert.deepEqual(s.bytes().subarray(before), bad, 'observation failure must not rewrite traffic');
    assert.ok(!f.events.some(e => e.ok === true));
    assert.deepEqual(Object.keys(f.events.find(e => e.kind === 'failure')).sort(), ['at', 'client', 'connectionId', 'kind', 'reason', 'seq']);
    assert.ok(!JSON.stringify(f.events).includes('SECRET'));
  });
}

for (const [name, bad] of [
  ['malformed response', { id: 2, result: {}, error: { code: 1, message: 'ERROR_SECRET' } }],
  ['unmatched response', { id: 99, result: { turnId: 'turn-a' } }],
  ['invalid returned turn ID', { id: 2, result: { turnId: { token: 'SECRET' } } }],
  ['invalid completed notification', { method: 'turn/completed', params: { threadId: 'thread-a', turn: {} } }],
]) {
  test(`malformed server traces fail instead of success: ${name}`, { timeout: 5000 }, async t => {
    const f = await fixture(t);
    const { c, s } = await f.opened();
    c.socket.write(frame(steer()));
    await until(() => f.events.length === 1, 'steer');
    s.socket.write(frame(bad, { client: false }));
    await until(() => f.events.some(e => e.kind === 'failure'), name);
    assert.ok(!f.events.some(e => e.ok === true));
    assert.ok(!JSON.stringify(f.events).includes('SECRET'));
  });
}

for (const which of ['client', 'target', 'partial frame', 'partial upgrade', 'WebSocket close']) {
  test(`closed traces fail instead of success: ${which}`, { timeout: 5000 }, async t => {
    const f = await fixture(t);
    const { c, s } = which === 'partial upgrade' ? await f.connect() : await f.opened();
    if (which === 'partial upgrade') c.socket.write(upgrade.subarray(0, 15));
    else if (which === 'partial frame') c.socket.write(frame(steer()).subarray(0, 11));
    else c.socket.write(frame(steer()));
    if (which === 'WebSocket close') s.socket.write(frame(Buffer.alloc(0), { client: false, opcode: 8 }));
    else (which === 'target' ? s : c).socket.end();
    await until(() => f.events.some(e => e.kind === 'failure'), 'closure failure');
    assert.ok(!f.events.some(e => e.ok === true));
  });
}

for (const [name, response] of [
  ['rejected upgrade', Buffer.from('HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n')],
  ['wrong upgrade accept', Buffer.from(upgraded.toString().replace('s3pPLMBiTxaQ9kYGzzhZRbK+xOo=', 'BAD_SECRET'))],
  ['oversized HTTP header', Buffer.from('HTTP/1.1 101 OK\r\nX-Secret: ' + 'X'.repeat(17000))],
]) {
  test(`rejected/malformed upgrades fail instead of success: ${name}`, { timeout: 5000 }, async t => {
    const f = await fixture(t);
    const { c, s } = await f.connect();
    c.socket.write(upgrade);
    await until(() => s.bytes().length === upgrade.length, 'request');
    s.socket.write(response);
    await until(() => f.events.some(e => e.kind === 'failure') && c.bytes().length === response.length, 'upgrade failure');
    assert.deepEqual(c.bytes(), response);
    assert.ok(!JSON.stringify(f.events).includes('SECRET'));
  });
}

test('contents/secrets absent from exported events', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const { c, s } = await f.opened();
  const q = steer();
  q.params.clientRequestId = 'WRONG_SECRET';
  c.socket.write(frame(q));
  await until(() => f.events.length === 1, 'request');
  s.socket.write(Buffer.concat([
    frame({ method: 'item/agentMessage/delta', params: { delta: 'DELTA_SECRET', token: 'TOKEN_SECRET' } }, { client: false }),
    frame({ id: 2, result: { turnId: 'turn-a', text: 'RESULT_SECRET' } }, { client: false }),
    frame({ method: 'turn/completed', params: { threadId: 'thread-a', turn: { id: 'turn-a', items: [{ text: 'TURN_SECRET' }] } } }, { client: false }),
  ]));
  await until(() => f.events.length === 3, 'minimal events');
  assert.ok(!JSON.stringify(f.events).includes('SECRET'));
  assert.deepEqual(f.events.map(metadata), [
    { kind: 'request', ...identity }, { kind: 'response', ...identity, ok: true, turnId: 'turn-a' },
    { kind: 'completed', threadId: 'thread-a', turnId: 'turn-a' },
  ]);
});

test('backpressure preserves large frames and coalesced messages with bounded message limits', { timeout: 5000 }, async t => {
  const f = await fixture(t, { maxMessageBytes: 100000 });
  const { c, s } = await f.opened();
  const q = steer();
  q.params.input[0].text = 'PRIVATE'.repeat(12000); // Exercises 64-bit frame lengths.
  const bytes = Buffer.concat(Array.from({ length: 20 }, (_, i) => frame({ ...q, id: i + 10 })));
  const before = s.bytes().length;
  s.socket.pause();
  let accepted;
  const writing = new Promise(resolve => { accepted = c.socket.write(bytes, resolve); });
  assert.equal(accepted, false, 'large write must exercise backpressure');
  await delay(10);
  assert.equal(s.bytes().length, before, 'paused target must not consume the large write');
  s.socket.resume();
  await writing;
  await until(() => s.bytes().length === before + bytes.length && f.events.length === 20, 'all large bytes');
  assert.deepEqual(s.bytes().subarray(before), bytes);
  assert.ok(f.events.every(e => e.kind === 'request'));
});

test('close releases every owned socket and permits no unrelated deletion', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const targetBefore = await lstat(f.targetPath);
  const unrelated = join(f.dir, 'unrelated');
  await writeFile(unrelated, 'KEEP');
  const pairs = [await f.opened(), await f.opened(), await f.connect()];
  const closed = pairs.flatMap(({ c, s }) => [once(c.socket, 'close'), once(s.socket, 'close')]);
  await Promise.all([f.tap.close(), f.tap.close()]);
  await Promise.all(closed);
  await assert.rejects(lstat(f.listenPath), { code: 'ENOENT' });
  assert.deepEqual((await readdir(f.dir)).sort(), ['target', 'unrelated']);
  assert.equal((await lstat(f.targetPath)).ino, targetBefore.ino);
  assert.equal(await readFile(unrelated, 'utf8'), 'KEEP');
  const client = net.createConnection(f.listenPath);
  await assert.rejects(once(client, 'connect'), { code: 'ENOENT' });
});

test('close preserves a replacement at the listener path', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  await unlink(f.listenPath);
  await writeFile(f.listenPath, 'REPLACEMENT');
  await f.tap.close();
  assert.equal(await readFile(f.listenPath, 'utf8'), 'REPLACEMENT');
});

test('close preserves unrelated replacement socket and its ability to accept', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  await unlink(f.listenPath);
  const replacement = net.createServer(socket => socket.end('KEEP'));
  replacement.listen(f.listenPath);
  await once(replacement, 'listening');
  t.after(() => new Promise(resolve => replacement.close(resolve)));
  const inode = (await lstat(f.listenPath)).ino;
  await f.tap.close();
  assert.equal((await lstat(f.listenPath)).ino, inode);
  const client = net.createConnection(f.listenPath), chunks = [];
  client.on('data', b => chunks.push(b));
  await once(client, 'end');
  assert.equal(Buffer.concat(chunks).toString(), 'KEEP');
});

test('requires caller-owned 0700 parent and refuses every existing listener path', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const options = { targetPath: f.targetPath, onEvent: () => {} };
  await assert.rejects(startRpcTap({ ...options, listenPath: f.listenPath }));
  const existing = join(f.dir, 'existing');
  await writeFile(existing, 'KEEP');
  await assert.rejects(startRpcTap({ ...options, listenPath: existing }));
  assert.equal(await readFile(existing, 'utf8'), 'KEEP');
  const linked = join(f.dir, 'linked');
  await symlink('missing', linked);
  await assert.rejects(startRpcTap({ ...options, listenPath: linked }));
  assert.ok((await lstat(linked)).isSymbolicLink());
  await chmod(f.dir, 0o755);
  await assert.rejects(startRpcTap({ ...options, listenPath: join(f.dir, 'new') }));
  await chmod(f.dir, 0o700);
  const parentLink = join(f.dir, 'parent-link');
  await symlink(f.dir, parentLink);
  await assert.rejects(startRpcTap({ ...options, listenPath: join(parentLink, 'new') }));
  for (const maxMessageBytes of [0, -1, Infinity, 1.5]) {
    await assert.rejects(startRpcTap({ ...options, listenPath: join(f.dir, 'new'), maxMessageBytes }));
  }
});

test('unreachable target emits fixed transport failure and remains closeable', { timeout: 5000 }, async t => {
  const f = await fixture(t, { targetPath: '/nonexistent-rpc-tap-target' });
  const c = capture(net.createConnection(f.listenPath));
  f.clients.push(c);
  await until(() => f.events.some(e => e.kind === 'failure'), 'target failure');
  assert.equal(f.events[0].reason, 'transport_error');
  assert.ok(!JSON.stringify(f.events).includes('nonexistent'));
  await f.tap.close();
});

test('bounded request tracking fails closed instead of silently forgetting pending IDs', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const { c } = await f.opened();
  c.socket.write(Buffer.concat(Array.from({ length: 1025 }, (_, i) => frame({
    id: 100 + i, method: 'thread/read', params: { threadId: 'thread-a' },
  }))));
  await until(() => f.events.some(e => e.kind === 'failure'), 'tracking bound');
  assert.equal(f.events.find(e => e.kind === 'failure').reason, 'request_tracking_lost');
});

test('close records incomplete pending proof and removes private listener artifacts', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const { c } = await f.opened();
  c.socket.write(frame(steer()));
  await until(() => f.events.length === 1, 'pending request');
  await f.tap.close();
  assert.equal(f.events.at(-1).kind, 'failure');
  assert.equal(f.events.at(-1).reason, 'incomplete_trace');
  // Restarting in the same directory and at the same path must work.
  const next = await startRpcTap({ listenPath: f.listenPath, targetPath: f.targetPath, onEvent: () => {} });
  await next.close();
});

test('documented IPC server transfer does not inherit binder auto-unlink authority', { timeout: 5000 }, async t => {
  const dir = await mkdtemp(join(tmpdir(), 'rpc-ipc-'));
  const path = join(dir, 'socket');
  t.after(() => rm(dir, { recursive: true, force: true }));
  const child = spawn(process.execPath, ['-e', `
    const server = require('node:net').createServer();
    setTimeout(() => process.exit(2), 1500).unref();
    server.listen(process.argv[1], () => {
      process.send('listener', server, error => process.exit(error ? 1 : 0));
    });
  `, path], { env: {}, stdio: ['ignore', 'ignore', 'ignore', 'ipc'] });
  const exited = once(child, 'exit');
  t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); await exited; });
  const [message, server] = await once(child, 'message');
  t.after(() => server.listening && new Promise(resolve => server.close(resolve)));
  assert.equal(message, 'listener');
  assert.ok(server instanceof net.Server && server.listening);
  assert.deepEqual(await exited, [0, null]);
  assert.ok((await lstat(path)).isSocket(), 'binder exit must leave its name for the receiver');
  server.on('connection', socket => socket.end('TRANSFERRED'));
  const client = net.createConnection(path), chunks = [];
  client.on('data', bytes => chunks.push(bytes));
  await once(client, 'end');
  assert.equal(Buffer.concat(chunks).toString(), 'TRANSFERRED');
  await unlink(path);
  await writeFile(path, 'UNRELATED');
  const before = await lstat(path);
  await new Promise(resolve => server.close(resolve));
  assert.equal((await lstat(path)).ino, before.ino);
  assert.equal(await readFile(path, 'utf8'), 'UNRELATED');
});

test('binder is reaped before publication and close leaves no helper or pinned directory handles', { timeout: 5000 }, async t => {
  const childrenPath = `/proc/self/task/${process.pid}/children`;
  const before = (await readFile(childrenPath, 'utf8')).trim();
  const f = await fixture(t);
  assert.equal((await readFile(childrenPath, 'utf8')).trim(), before, 'start must await binder exit and IPC closure');
  const privateName = (await readdir(f.dir)).find(name => name.startsWith('.rpc-tap-'));
  const privateDir = join(f.dir, privateName);
  await f.tap.close();
  assert.equal((await readFile(childrenPath, 'utf8')).trim(), before);
  assert.deepEqual(await directoryDescriptors(privateDir), []);
  assert.deepEqual(await directoryDescriptors(f.dir), []);
});

async function directoryDescriptors(path) {
  const found = [];
  for (const fd of await readdir('/proc/self/fd')) {
    try {
      const target = await readlink(`/proc/self/fd/${fd}`);
      if (target === path || target === path + ' (deleted)') found.push(fd);
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  return found;
}

for (const change of ['private bound socket replacement', 'renamed private directory with replacement at original name']) {
  test(`I1 preserves ${change} and reports incomplete ownership cleanup`, { timeout: 5000 }, async t => {
    const f = await fixture(t);
    const { c, s } = await f.opened();
    const privateName = (await readdir(f.dir)).find(name => name.startsWith('.rpc-tap-'));
    assert.ok(privateName);
    const privateDir = join(f.dir, privateName), movedDir = join(f.dir, 'moved-owned-directory');
    const ownedDirectory = await lstat(privateDir);
    const descriptors = [...await directoryDescriptors(f.dir), ...await directoryDescriptors(privateDir)];
    assert.equal(descriptors.length, 2, 'parent and private directory handles are pinned');
    let replacement, before;
    if (change === 'private bound socket replacement') {
      replacement = join(privateDir, 'socket');
      await unlink(replacement);
      await writeFile(replacement, 'UNRELATED_REPLACEMENT');
    } else {
      await rename(privateDir, movedDir);
      await mkdir(privateDir, { mode: 0o700 });
      replacement = privateDir;
    }
    before = await lstat(replacement);
    const closed = [once(c.socket, 'close'), once(s.socket, 'close')];
    const outcome = await f.closeOutcome();
    await Promise.all(closed);
    const after = await lstat(replacement).catch(() => undefined);
    assert.ok(after, 'unrelated replacement must survive close');
    assert.equal(after.ino, before.ino, 'replacement inode must be unchanged');
    assert.equal(after.dev, before.dev);
    if (change === 'private bound socket replacement') {
      assert.equal(await readFile(replacement, 'utf8'), 'UNRELATED_REPLACEMENT');
      assert.deepEqual((await readdir(privateDir)).sort(), ['socket']);
    } else {
      assert.deepEqual(await readdir(privateDir), []);
      assert.equal((await lstat(movedDir)).ino, ownedDirectory.ino, 'retain renamed owned directory without guessing another pathname');
      assert.deepEqual(await readdir(movedDir), [], 'owned socket is removed via its pinned directory');
    }
    assert.equal(outcome, 'tap_cleanup_failed', 'ownership loss must not resolve as successful cleanup');
    assert.equal(await f.closeOutcome(), 'tap_cleanup_failed', 'repeated close retains its failed result');
    assert.ok(f.events.some(event => event.kind === 'failure' && event.reason === 'cleanup_error'));
    await assert.rejects(lstat(f.listenPath), { code: 'ENOENT' });
    assert.ok((await lstat(f.targetPath)).isSocket());
    for (const fd of descriptors) {
      const target = await readlink(`/proc/self/fd/${fd}`).catch(() => undefined);
      assert.ok(![f.dir, privateDir, movedDir].flatMap(path => [path, path + ' (deleted)']).includes(target), 'owned directory descriptor must be closed');
    }
    assert.deepEqual((await readdir(f.dir)).sort(), change === 'private bound socket replacement'
      ? [privateName, 'target'].sort() : [privateName, 'moved-owned-directory', 'target'].sort());
  });
}
