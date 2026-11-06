import test, { beforeEach } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:net';
import type { Socket } from 'node:net';
import { createHash } from 'node:crypto';
import { mkdtemp, rm, lstat, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { fakeClock } from '../../hub-client/testing/fixtures.ts';
import { connectUnixWebSocket } from '../src/codex/ws.ts';
import type { WsConn } from '../src/codex/ws.ts';
import { startFakeCodexAppServer } from './fake-codex-appserver.ts';

const guid = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
const opts = { maxMessageBytes: 2 * 1024 * 1024, timeoutMs: 1500 };
const roots = new WeakMap<TestContext, string>();
beforeEach(async t => {
  if (!('after' in t)) throw new Error('expected a test context');
  const dir = await mkdtemp('/tmp/vn-ws-');
  roots.set(t, dir);
  const names = ['VECNET_CONFIG', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'CODEX_HOME', 'VECNET_CODEX_BINDINGS'];
  const saved = names.map(name => process.env[name]);
  names.forEach((name, n) => { process.env[name] = join(dir, `isolated-${n}`); });
  t.after(async () => {
    names.forEach((name, n) => { if (saved[n] === undefined) delete process.env[name]; else process.env[name] = saved[n]; });
    await rm(dir, { recursive: true, force: true });
  });
});

function queue<T>() {
  const values: T[] = [], waiters: ((value: T) => void)[] = [];
  return { put(value: T) { const waiter = waiters.shift(); if (waiter) waiter(value); else values.push(value); },
    next(): Promise<T> { return values.length ? Promise.resolve(values.shift()!) : new Promise(resolve => waiters.push(resolve)); } };
}
// Independent wire encoder/parser: transport tests never share production framing.
function frame(op: number, text: string | Buffer, fin = true) {
  const data = Buffer.isBuffer(text) ? text : Buffer.from(text);
  const head = Buffer.alloc(data.length < 126 ? 2 : data.length < 65536 ? 4 : 10);
  head[0] = (fin ? 128 : 0) | op;
  if (head.length === 2) head[1] = data.length;
  else if (head.length === 4) { head[1] = 126; head.writeUInt16BE(data.length, 2); }
  else { head[1] = 127; head.writeBigUInt64BE(BigInt(data.length), 2); }
  return Buffer.concat([head, data]);
}
async function wireServer(t: TestContext, handshake: 'good' | 'bad' | 'http' | 'silent' = 'good') {
  const path = join(roots.get(t)!, 'raw.sock');
  const paths: string[] = [], keys: string[] = [];
  const frames = queue<{ op: number; data: Buffer; masked: boolean }>();
  let peer: Socket | undefined;
  const server = createServer(socket => {
    peer = socket;
    socket.on('error', () => {});
    let bytes: Buffer = Buffer.alloc(0), upgraded = false;
    socket.on('data', chunk => {
      bytes = Buffer.concat([bytes, chunk]);
      if (!upgraded) {
        const end = bytes.indexOf('\r\n\r\n'); if (end < 0) return;
        const head = bytes.subarray(0, end).toString(); bytes = bytes.subarray(end + 4);
        paths.push(head.split(' ')[1]);
        const key = /Sec-WebSocket-Key: (.+)\r\n/i.exec(`${head}\r\n`)![1]; keys.push(key);
        if (handshake === 'silent') return;
        const accept = handshake === 'bad' ? 'wrong' : createHash('sha1').update(key + guid).digest('base64');
        socket.write(handshake === 'http' ? 'HTTP/1.1 403 Forbidden\r\n\r\n' :
          `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
        upgraded = true;
      }
      while (bytes.length >= 2) {
        let len = bytes[1] & 127, at = 2;
        if (len === 126) { if (bytes.length < 4) return; len = bytes.readUInt16BE(2); at = 4; }
        else if (len === 127) { if (bytes.length < 10) return; len = Number(bytes.readBigUInt64BE(2)); at = 10; }
        const masked = !!(bytes[1] & 128), maskAt = at; if (masked) at += 4;
        if (bytes.length < at + len) return;
        const data = Buffer.from(bytes.subarray(at, at + len));
        if (masked) data.forEach((b, n) => { data[n] = b ^ bytes[maskAt + n % 4]; });
        frames.put({ op: bytes[0] & 15, data, masked }); bytes = bytes.subarray(at + len);
      }
    });
  });
  await new Promise<void>((resolve, reject) => { server.once('error', reject); server.listen(path, resolve); });
  t.after(async () => { peer?.destroy(); await new Promise<void>(resolve => server.close(() => resolve())); });
  return { path, paths, keys, frames, write(bytes: Buffer) { peer!.write(bytes); } };
}
function messages(c: WsConn) { const q = queue<string>(); c.onMessage(q.put); return q; }
function closed(c: WsConn) { return new Promise<string>(resolve => c.onClose(resolve)); }
function rpc(c: WsConn) {
  let id = 0;
  const pending = new Map<number, (reply: any) => void>();
  const notices: any[] = [], requests: any[] = [];
  c.onMessage(text => {
    const m = JSON.parse(text);
    if (m.method) (m.id === undefined ? notices : requests).push(m);
    else { pending.get(m.id)?.(m); pending.delete(m.id); }
  });
  return { notices, requests, call(method: string, params: unknown = {}): Promise<any> {
    return new Promise((resolve, reject) => {
      const n = ++id;
      pending.set(n, reply => reply.error ? reject(reply.error) : resolve(reply.result));
      c.send(JSON.stringify({ id: n, method, params }));
    });
  } };
}
async function fake(t: TestContext, clock = fakeClock()) {
  const f = await startFakeCodexAppServer({ dir: roots.get(t)!, clock });
  t.after(() => f.close());
  const c = await connectUnixWebSocket(f.socketPath, opts); t.after(() => c.close());
  return { f, c, r: rpc(c), clock };
}
// An RPC response flushes preceding notifications on this connection.
async function flush(r: ReturnType<typeof rpc>) { await r.call('thread/read', { threadId: 't' }); }

test('handshake uses path / and validates Sec-WebSocket-Accept', { timeout: 5000 }, async t => {
  const { f, r } = await fake(t);
  assert.equal((await r.call('initialize', { capabilities: {} })).userAgent, 'codex-tui/0.166.0 (fake)');
  assert.deepEqual(f.paths, ['/']);
  const bad = await wireServer(t, 'bad');
  await assert.rejects(connectUnixWebSocket(bad.path, opts), /accept/i);
  assert.deepEqual(bad.paths, ['/']);
  assert.equal(Buffer.from(bad.keys[0], 'base64').length, 16);
});

test('text round trip, fragments and 1 MiB message', { timeout: 5000 }, async t => {
  const s = await wireServer(t), c = await connectUnixWebSocket(s.path, opts); t.after(() => c.close());
  const q = messages(c);
  c.send('hello 世界');
  const sent = await s.frames.next(); assert.equal(sent.masked, true); assert.equal(sent.op, 1);
  assert.equal(sent.data.toString(), 'hello 世界');
  s.write(frame(1, sent.data)); assert.equal(await q.next(), 'hello 世界');
  // Split a UTF-8 character across frames, and a frame header across writes.
  const data = Buffer.from('a🌏z'); const first = frame(1, data.subarray(0, 3), false);
  s.write(first.subarray(0, 1)); s.write(first.subarray(1)); s.write(frame(0, data.subarray(3)));
  assert.equal(await q.next(), 'a🌏z');
  const big = 'x'.repeat(1024 * 1024); c.send(big);
  const large = await s.frames.next(); assert.equal(large.masked, true); assert.equal(large.data.toString(), big);
  s.write(frame(1, large.data)); assert.equal(await q.next(), big);
});

test('ping is answered with pong; close frame ends the connection', { timeout: 5000 }, async t => {
  const s = await wireServer(t), c = await connectUnixWebSocket(s.path, opts); t.after(() => c.close());
  const q = messages(c), end = closed(c);
  s.write(frame(1, 'hel', false)); s.write(frame(9, 'alive')); s.write(frame(0, 'lo'));
  const pong = await s.frames.next(); assert.equal(pong.op, 10); assert.equal(pong.masked, true);
  assert.equal(pong.data.toString(), 'alive'); assert.equal(await q.next(), 'hello');
  s.write(frame(8, Buffer.from([3, 232])));
  assert.match(await end, /close/i);
  const reply = await s.frames.next(); assert.equal(reply.op, 8); assert.equal(reply.masked, true);
});

test('messages over maxMessageBytes close the connection', { timeout: 5000 }, async t => {
  for (const fragmented of [false, true]) {
    await t.test(fragmented ? 'cumulative fragments' : 'declared frame length', async sub => {
      roots.set(sub, roots.get(t)!);
      const s = await wireServer(sub), c = await connectUnixWebSocket(s.path, { ...opts, maxMessageBytes: 8 });
      sub.after(() => c.close()); const end = closed(c); let delivered = false; c.onMessage(() => { delivered = true; });
      if (fragmented) { s.write(frame(1, '12345', false)); s.write(frame(0, '6789')); }
      else s.write(frame(1, '123456789').subarray(0, 2)); // Reject the header without waiting for the body.
      assert.match(await end, /maxMessageBytes|large|limit/i); assert.equal(delivered, false);
    });
  }
});

test('fake unloads 60 s after its last subscriber leaves', { timeout: 5000 }, async t => {
  const { f, r, clock } = await fake(t);
  f.setThread({ id: 't', status: { type: 'idle' } }); f.attachOther('t');
  await r.call('thread/resume', { threadId: 't', excludeTurns: true });
  f.detachOther('t'); clock.advance(600_000);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.status.type, 'idle');
  assert.equal(f.subscribers('t').length, 1);
  assert.equal((await r.call('thread/unsubscribe', { threadId: 't' })).status, 'unsubscribed');
  clock.advance(59_999); await flush(r);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.status.type, 'idle');
  assert.equal(r.notices.some(x => x.method === 'thread/closed'), false);
  clock.advance(1); await flush(r);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.status.type, 'notLoaded');
  assert.ok(r.notices.some(x => x.method === 'thread/status/changed' && x.params.status.type === 'notLoaded'));
  assert.ok(r.notices.some(x => x.method === 'thread/closed' && x.params.threadId === 't'));
  f.setThread({ id: 't', status: { type: 'active', activeFlags: [] }, activeTurnId: 'running' });
  clock.advance(600_000); await flush(r);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.status.type, 'active');
});

test('fake replays a pending server request on resume', { timeout: 5000 }, async t => {
  const { f, c, r } = await fake(t);
  f.setThread({ id: 't', status: { type: 'active', activeFlags: ['waitingOnUserInput'] }, activeTurnId: 'run', approvalPolicy: 'on-request', approvalsReviewer: 'user' });
  const pending = f.serverRequest('t', 'item/tool/requestUserInput', { threadId: 't', questions: [] });
  const resumed = await r.call('thread/resume', { threadId: 't', excludeTurns: true }); await flush(r);
  assert.equal(resumed.approvalPolicy, 'on-request'); assert.equal(resumed.approvalsReviewer, 'user');
  assert.deepEqual(resumed.thread.status.activeFlags, ['waitingOnUserInput']); assert.ok(resumed.sandbox);
  assert.deepEqual(r.requests.map(x => x.id), [pending]); assert.deepEqual(f.replies, []);
  await r.call('thread/unsubscribe', { threadId: 't' });
  await r.call('thread/resume', { threadId: 't', excludeTurns: true }); await flush(r);
  assert.deepEqual(r.requests.map(x => x.id), [pending, pending]);
  f.resolve(pending); await flush(r);
  assert.ok(r.notices.some(x => x.method === 'serverRequest/resolved' && x.params.requestId === pending));
  await r.call('thread/unsubscribe', { threadId: 't' });
  await r.call('thread/resume', { threadId: 't', excludeTurns: true }); await flush(r);
  assert.equal(r.requests.length, 2); assert.deepEqual(f.overrides, []);
  const end = closed(c); c.close(); await end;
  // Ask a second connection to establish that the first has been removed.
  const other = await connectUnixWebSocket(f.socketPath, opts); t.after(() => other.close());
  await rpc(other).call('thread/read', { threadId: 't' }); assert.deepEqual(f.subscribers('t'), []);
});

test('fake pages turns/list newest-first and records a withheld item later', { timeout: 5000 }, async t => {
  const { f, r, clock } = await fake(t);
  f.setThread({ id: 't', status: { type: 'idle' } });
  await r.call('thread/resume', { threadId: 't', excludeTurns: true });
  f.addTurns('t', 11); f.withholdNextItem();
  const started = await r.call('turn/start', { threadId: 't', input: [{ type: 'text', text: 'stub' }], clientUserMessageId: 'vn.later' });
  const original = started.turn.id;
  let cursor: string | undefined, pages = 0; const ids: string[] = [];
  do {
    const page = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full', limit: 5, ...(cursor ? { cursor } : {}) });
    assert.equal(page.data.length, pages < 2 ? 5 : 2);
    ids.push(...page.data.map((x: any) => x.id)); cursor = page.nextCursor ?? undefined; pages++;
  } while (cursor);
  assert.equal(pages, 3); assert.equal(ids.length, 12); assert.equal(new Set(ids).size, 12); assert.equal(ids[0], original);
  assert.deepEqual(ids.slice(1), ['turn-11','turn-10','turn-9','turn-8','turn-7','turn-6','turn-5','turn-4','turn-3','turn-2','turn-1']);
  assert.equal(r.notices.some(x => x.method === 'item/started'), false);
  f.recordLater('vn.later', clock.now() + 5000); f.addTurns('t', 1);
  clock.advance(4999); await flush(r);
  let page = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full', limit: 5 });
  assert.deepEqual(page.data.find((x: any) => x.id === original).items, []);
  clock.advance(1); await flush(r);
  page = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full', limit: 5 });
  assert.equal(page.data.find((x: any) => x.id === original).items[0].clientId, 'vn.later');
  assert.equal(page.data[0].items.length, 0);
  assert.ok(r.notices.some(x => x.method === 'item/started' && x.params.turnId === original && x.params.item.clientId === 'vn.later'));
  assert.equal((await lstat(f.socketPath)).isSymbolicLink(), true);
  assert.equal((await stat(join(roots.get(t)!, 'd'))).mode & 0o777, 0o700);
});

test('fake generated turn IDs skip seeded IDs and steer receipts stay in the new turn', { timeout: 5000 }, async t => {
  const { f, r } = await fake(t);
  f.setThread({ id: 't', status: { type: 'active', activeFlags: [] }, activeTurnId: 'turn-1' });
  f.setThread({ id: 't', status: { type: 'idle' } });
  const started = await r.call('turn/start', { threadId: 't', input: [], clientUserMessageId: 'vn.start' });
  const steered = await r.call('turn/steer', { threadId: 't', expectedTurnId: started.turn.id, input: [], clientUserMessageId: 'vn.steer' });
  const page = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full' });
  assert.notEqual(started.turn.id, 'turn-1');
  assert.equal(steered.turnId, started.turn.id);
  assert.equal(new Set(page.data.map((x: any) => x.id)).size, 2);
  assert.deepEqual(page.data.map((x: any) => [x.id, x.status, x.items.map((item: any) => item.clientId)]), [
    [started.turn.id, 'inProgress', ['vn.start', 'vn.steer']], ['turn-1', 'completed', []],
  ]);
});

test('fake addTurns shares the allocator and reserves seeded IDs across threads', { timeout: 5000 }, async t => {
  const { f, r } = await fake(t);
  f.setThread({ id: 't', status: { type: 'active', activeFlags: [] }, activeTurnId: 'turn-1' });
  f.setThread({ id: 'other', status: { type: 'active', activeFlags: [] }, activeTurnId: 'turn-3' });
  f.setThread({ id: 't', status: { type: 'idle' } });
  f.addTurns('t', 2);
  const started = await r.call('turn/start', { threadId: 't', input: [] });
  const page = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full' });
  const ids = [...page.data.map((x: any) => x.id), 'turn-3'];
  assert.equal(ids[0], started.turn.id);
  assert.equal(ids.length, 5);
  assert.equal(new Set(ids).size, ids.length);
});

test('fake summary history hides steers on seeded turns including delayed receipts', { timeout: 5000 }, async t => {
  const { f, r, clock } = await fake(t);
  f.setThread({ id: 't', status: { type: 'active', activeFlags: [] }, activeTurnId: 'seeded-run' });
  await r.call('turn/steer', { threadId: 't', expectedTurnId: 'seeded-run', input: [], clientUserMessageId: 'vn.summary-steer' });
  f.withholdNextItem();
  await r.call('turn/steer', { threadId: 't', expectedTurnId: 'seeded-run', input: [], clientUserMessageId: 'vn.delayed-steer' });
  f.recordLater('vn.delayed-steer', clock.now() + 1); clock.advance(1);
  for (const view of [{}, { itemsView: 'summary' }]) {
    const page = await r.call('thread/turns/list', { threadId: 't', ...view });
    assert.deepEqual(page.data[0].items, []);
  }
  const full = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full' });
  assert.deepEqual(full.data[0].items.map((item: any) => item.clientId), ['vn.summary-steer', 'vn.delayed-steer']);
});

test('fake summary history preserves initial input and hides starts steered into an active turn', { timeout: 5000 }, async t => {
  const { f, r, clock } = await fake(t);
  f.setThread({ id: 't', status: { type: 'idle' } });
  f.withholdNextItem();
  const started = await r.call('turn/start', { threadId: 't', input: [], clientUserMessageId: 'vn.initial' });
  await r.call('turn/steer', { threadId: 't', expectedTurnId: started.turn.id, input: [], clientUserMessageId: 'vn.steer' });
  const raced = await r.call('turn/start', { threadId: 't', input: [], clientUserMessageId: 'vn.raced-start' });
  assert.equal(raced.turn.id, started.turn.id);
  f.recordLater('vn.initial', clock.now() + 1); clock.advance(1);
  for (const view of [{}, { itemsView: 'summary' }]) {
    const page = await r.call('thread/turns/list', { threadId: 't', ...view });
    assert.deepEqual(page.data[0].items.map((item: any) => item.clientId), ['vn.initial']);
  }
  const full = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full' });
  assert.deepEqual(full.data[0].items.map((item: any) => item.clientId), ['vn.steer', 'vn.raced-start', 'vn.initial']);
});

test('handshake failures, cancellation and local close settle promptly', { timeout: 5000 }, async t => {
  for (const mode of ['http', 'silent'] as const) {
    await t.test(mode, async sub => {
      const s = await wireServer(sub, mode);
      await assert.rejects(connectUnixWebSocket(s.path, mode === 'silent' ? { ...opts, timeoutMs: 30 } : opts), mode === 'http' ? /non-101/ : /timeout/);
    });
  }
  await t.test('abort during handshake', async sub => {
    const s = await wireServer(sub, 'silent'), ac = new AbortController();
    const pending = connectUnixWebSocket(s.path, { ...opts, signal: ac.signal }); ac.abort();
    await assert.rejects(pending, /abort/i);
  });
  const s = await wireServer(t), ac = new AbortController();
  const c = await connectUnixWebSocket(s.path, { ...opts, signal: ac.signal });
  let closes = 0; c.onClose(() => closes++); const end = closed(c); ac.abort();
  assert.match(await end, /abort/i); c.close(); assert.equal(closes, 1);
  await assert.rejects(connectUnixWebSocket(s.path, { ...opts, signal: ac.signal }));
  await assert.rejects(connectUnixWebSocket(join(roots.get(t)!, 'missing.sock'), opts), /socket/);
  const c2 = await connectUnixWebSocket(s.path, opts); const end2 = closed(c2); c2.close();
  assert.match(await end2, /local close/); assert.throws(() => c2.send('closed'), /closed/);
  assert.notEqual(s.keys[0], s.keys[1]);
});

test('malformed frames are rejected without delivering text', { timeout: 5000 }, async t => {
  const cases: [string, Buffer][] = [
    ['masked server frame', Buffer.from([0x81, 0x80, 1, 2, 3, 4])],
    ['reserved bits', Buffer.from([0xc1, 0])],
    ['unexpected continuation', frame(0, 'bad')],
    ['binary', frame(2, 'bad')],
    ['fragmented ping', frame(9, 'bad', false)],
    ['oversized ping', frame(9, 'x'.repeat(126))],
    ['noncanonical length', Buffer.from([0x81, 126, 0, 1, 120])],
    ['invalid UTF-8', frame(1, Buffer.from([0xff]))],
    ['invalid close size', frame(8, Buffer.from([1]))],
  ];
  for (const [name, bytes] of cases) {
    await t.test(name, async sub => {
      const s = await wireServer(sub), c = await connectUnixWebSocket(s.path, opts); sub.after(() => c.close());
      let count = 0; c.onMessage(() => count++); const end = closed(c); s.write(bytes);
      assert.match(await end, /WebSocket/); assert.equal(count, 0);
    });
  }
});

test('fake filters opt-outs, confines subscriber events and simulates failures', { timeout: 5000 }, async t => {
  const { f, r } = await fake(t);
  f.setThread({ id: 't', status: { type: 'idle' } });
  const c2 = await connectUnixWebSocket(f.socketPath, opts); t.after(() => c2.close()); const r2 = rpc(c2);
  f.setVersion('0.999.0');
  assert.equal((await r.call('initialize', { capabilities: { optOutNotificationMethods: ['item/agentMessage/delta'] } })).userAgent, 'codex-tui/0.999.0 (fake)');
  const conn = f.requests.find(x => x.method === 'initialize')!.conn;
  assert.deepEqual(f.optOuts(conn), ['item/agentMessage/delta']);
  await r.call('thread/resume', { threadId: 't', excludeTurns: true });
  f.notify('item/agentMessage/delta', { threadId: 't', delta: 'ignored' });
  f.notifySubscribers('t', 'item/completed', { threadId: 't', item: { id: 'only-subscriber' } });
  await flush(r); await flush(r2);
  assert.equal(r.notices.some(x => x.method === 'item/agentMessage/delta'), false);
  assert.equal(r2.notices.filter(x => x.method === 'item/agentMessage/delta').length, 1);
  assert.equal(r.notices.filter(x => x.method === 'item/completed').length, 1);
  assert.equal(r2.notices.some(x => x.method === 'item/completed'), false);
  const started = await r.call('turn/start', { threadId: 't', input: [], clientUserMessageId: 'vn.first' });
  await assert.rejects(r.call('turn/steer', { threadId: 't', expectedTurnId: 'wrong', input: [], clientUserMessageId: 'vn.wrong' }), (e: any) => e.code === -32600);
  assert.equal((await r.call('turn/steer', { threadId: 't', expectedTurnId: started.turn.id, input: [], clientUserMessageId: 'vn.steer' })).turnId, started.turn.id);
  const page = await r.call('thread/turns/list', { threadId: 't', itemsView: 'full' });
  assert.deepEqual(page.data[0].items.map((x: any) => x.clientId), ['vn.first', 'vn.steer']);
  f.failNext('thread/read', { code: -32000, message: 'injected failure' });
  await assert.rejects(r.call('thread/read', { threadId: 't' }), (e: any) => e.code === -32000);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.id, 't');
  f.failNext('thread/read', 'drop'); const end = closed(c2);
  c2.send(JSON.stringify({ id: 999, method: 'thread/read', params: { threadId: 't' } }));
  assert.match(await end, /ended|closed/);
});

test('fake cold resume broadcasts startup and disconnect releases keep-loaded subscription', { timeout: 5000 }, async t => {
  const { f, r, clock } = await fake(t);
  f.setThread({ id: 't', status: { type: 'notLoaded' } });
  assert.equal((await r.call('thread/unsubscribe', { threadId: 't' })).status, 'notLoaded');
  const c2 = await connectUnixWebSocket(f.socketPath, opts); t.after(() => c2.close()); const r2 = rpc(c2);
  assert.equal((await r2.call('thread/resume', { threadId: 't', excludeTurns: true })).thread.status.type, 'idle');
  await flush(r2);
  assert.equal(r2.notices.filter(x => x.method === 'mcpServer/startupStatus/updated').length, 1);
  assert.equal(r.notices.some(x => x.method === 'mcpServer/startupStatus/updated'), false);
  assert.equal((await r.call('thread/unsubscribe', { threadId: 't' })).status, 'notSubscribed');
  const end = closed(c2); c2.close(); await end;
  const c3 = await connectUnixWebSocket(f.socketPath, opts); t.after(() => c3.close()); const r3 = rpc(c3); await flush(r3);
  assert.deepEqual(f.subscribers('t'), []);
  clock.advance(59_999); await flush(r);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.status.type, 'idle');
  await r3.call('thread/resume', { threadId: 't', excludeTurns: true }); clock.advance(1); await flush(r);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.status.type, 'idle');
  await r3.call('thread/unsubscribe', { threadId: 't' }); clock.advance(60_000); await flush(r);
  assert.equal((await r.call('thread/read', { threadId: 't' })).thread.status.type, 'notLoaded');
});
