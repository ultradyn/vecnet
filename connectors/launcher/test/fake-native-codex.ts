import { createServer } from 'node:net';
import type { Socket } from 'node:net';
import { createHash } from 'node:crypto';
import { chmod, mkdir, symlink } from 'node:fs/promises';
import { join } from 'node:path';

type Params = Record<string, any>;
export interface NativeTurn {
  id: string;
  status: 'inProgress' | 'completed' | 'interrupted' | 'failed';
  items: Params[];
  completedAt?: number;
}
export interface NativeThread {
  id: string;
  cwd: string;
  status: { type: string; activeFlags?: string[] };
  archived: boolean;
  name?: string;
  turns: NativeTurn[];
}
type Peer = { socket: Socket; connection: number; send(value: unknown): void };
type Failure = 'drop' | 'accept-drop' | { code: number; message: string };

// Only the client frames used by this native-backend fixture are needed here.
function frame(op: number, payload: Buffer): Buffer {
  const size = payload.length < 126 ? 2 : payload.length < 65536 ? 4 : 10;
  const header = Buffer.alloc(size);
  header[0] = 0x80 | op;
  header[1] = size === 2 ? payload.length : size === 4 ? 126 : 127;
  if (size === 4) header.writeUInt16BE(payload.length, 2);
  if (size === 10) header.writeBigUInt64BE(BigInt(payload.length), 2);
  return Buffer.concat([header, payload]);
}

/** A real Unix WebSocket daemon with native launcher methods, isolated from the
 * frozen connector fixture. No daemon processes or real Codex homes are used. */
export async function startFakeNativeCodex(dir: string) {
  const privateDir = join(dir, 'd'), home = join(dir, 'home');
  await mkdir(privateDir, { recursive: true, mode: 0o700 });
  await chmod(privateDir, 0o700);
  await mkdir(join(home, 'app-server-control'), { recursive: true, mode: 0o700 });
  const target = join(privateDir, 'native.sock');
  const socketPath = join(home, 'app-server-control', 'app-server-control.sock');
  const peers = new Set<Peer>();
  const threads = new Map<string, NativeThread>();
  const requests: { method: string; params: Params; connection: number }[] = [];
  const replies: { id: number | string; result?: Params; error?: unknown }[] = [];
  const paths: string[] = [];
  const failures = new Map<string, Failure[]>();
  let connection = 0, threadId = 0, turnId = 0, requestId = 10000;
  let listVisible = true, pageSize = 100;
  const snapshot = (t: NativeThread, turns = true) => ({ ...t, turns: turns ? t.turns : [] });
  function notice(method: string, params: Params) {
    for (const peer of peers) peer.send({ method, params });
  }
  function rpc(peer: Peer, message: Params) {
    if (typeof message.method !== 'string') {
      replies.push(message as typeof replies[number]);
      return;
    }
    const method = message.method, params = message.params ?? {};
    requests.push({ method, params, connection: peer.connection });
    const fail = failures.get(method)?.shift();
    const respond = (result: unknown) => {
      if (fail === 'accept-drop') peer.socket.destroy();
      else peer.send({ id: message.id, result });
    };
    const error = (code: number, text: string) => peer.send({ id: message.id, error: { code, message: text } });
    if (fail === 'drop') { peer.socket.destroy(); return; }
    if (typeof fail === 'object') { error(fail.code, fail.message); return; }
    if (method === 'initialize') { respond({ userAgent: 'codex-tui/0.166.0 (native fake)' }); return; }
    if (method === 'initialized') return;
    if (method === 'thread/start') {
      const t: NativeThread = { id: `native-${++threadId}`, cwd: params.cwd,
        status: { type: 'idle' }, archived: false, turns: [] };
      threads.set(t.id, t);
      respond({ thread: snapshot(t), model: params.model ?? 'native-default-model',
        modelProvider: 'native-default-provider', reasoningEffort: params.config?.model_reasoning_effort ?? 'medium' }); return;
    }
    if (method === 'thread/list') {
      const data = listVisible ? [...threads.values()].filter(t => !t.archived && (!params.cwd || params.cwd === t.cwd)) : [];
      respond({ data: data.map(t => snapshot(t, false)), nextCursor: null }); return;
    }
    const t = threads.get(params.threadId);
    if (!t) { error(-32602, 'unknown thread'); return; }
    if (method === 'thread/name/set') { t.name = params.name; respond({}); return; }
    if (method === 'thread/read' || method === 'thread/resume') {
      if (method === 'thread/resume' && t.status.type === 'notLoaded') t.status = { type: 'idle' };
      respond({ thread: snapshot(t, method === 'thread/read' ? params.includeTurns === true : params.excludeTurns !== true) }); return;
    }
    if (method === 'thread/turns/list') {
      const offset = params.cursor ? Number(String(params.cursor).replace('native-page:', '')) : 0;
      if (!Number.isSafeInteger(offset) || offset < 0) { error(-32602, 'bad cursor'); return; }
      const all = [...t.turns].reverse(), limit = Math.min(params.limit ?? 100, pageSize);
      respond({ data: all.slice(offset, offset + limit), nextCursor: offset + limit < all.length ? `native-page:${offset + limit}` : null }); return;
    }
    if (method === 'turn/start' || method === 'turn/steer') {
      // Native clientUserMessageId receipts let the backend reconcile accepted inputs.
      const previous = t.turns.find(turn => turn.items.some(item => item.clientId === params.clientUserMessageId));
      if (previous) { respond(method === 'turn/start' ? { turn: previous } : { turnId: previous.id }); return; }
      let turn = method === 'turn/steer' ? t.turns.find(turn => turn.id === params.expectedTurnId && turn.status === 'inProgress') : undefined;
      if (method === 'turn/steer' && !turn) { error(-32602, 'no matching active turn'); return; }
      if (!turn) {
        turn = { id: `native-turn-${++turnId}`, status: 'inProgress', items: [] };
        t.turns.push(turn);
      }
      turn.items.push({ id: `user-${turn.id}-${turn.items.length}`, type: 'userMessage',
        clientId: params.clientUserMessageId, content: params.input });
      t.status = { type: 'active', activeFlags: [] };
      respond(method === 'turn/start' ? { turn } : { turnId: turn.id });
      if (fail !== 'accept-drop') notice('turn/started', { threadId: t.id, turn });
      return;
    }
    if (method === 'turn/interrupt') {
      const turn = t.turns.find(turn => turn.id === params.turnId);
      if (turn) turn.status = 'interrupted';
      t.status = { type: 'idle' };
      respond({});
      if (turn) notice('turn/completed', { threadId: t.id, turn });
      return;
    }
    if (method === 'thread/unsubscribe') { t.status = { type: 'notLoaded' }; respond({ status: 'unsubscribed' }); return; }
    if (method === 'thread/archive') { t.archived = true; respond({}); return; }
    error(-32601, `unsupported native method: ${method}`);
  }
  const server = createServer(socket => {
    const peer: Peer = { socket, connection: ++connection,
      send(value) { if (!socket.destroyed) socket.write(frame(1, Buffer.from(JSON.stringify(value)))); } };
    peers.add(peer);
    socket.on('error', () => {});
    socket.once('close', () => peers.delete(peer));
    let bytes: Buffer = Buffer.alloc(0), ready = false;
    socket.on('data', chunk => {
      bytes = Buffer.concat([bytes, chunk]);
      if (!ready) {
        const end = bytes.indexOf('\r\n\r\n');
        if (end < 0) return;
        const header = bytes.subarray(0, end).toString('ascii');
        const path = /^GET (\S+) HTTP\/1\.1/.exec(header)?.[1];
        const key = /\r\nSec-WebSocket-Key:\s*([^\r\n]+)/i.exec(header)?.[1].trim();
        paths.push(path ?? '');
        if (path !== '/' || !key) { socket.destroy(); return; }
        const accept = createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
        socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
        bytes = bytes.subarray(end + 4); ready = true;
      }
      while (!socket.destroyed && bytes.length >= 2) {
        const op = bytes[0] & 15;
        if (!(bytes[0] & 0x80) || !(bytes[1] & 0x80)) { socket.destroy(); return; }
        let length = bytes[1] & 127, at = 2;
        if (length === 126) { if (bytes.length < 4) return; length = bytes.readUInt16BE(2); at = 4; }
        if (length === 127) {
          if (bytes.length < 10) return;
          const big = bytes.readBigUInt64BE(2);
          if (big > 4n * 1024n * 1024n) { socket.destroy(); return; }
          length = Number(big); at = 10;
        }
        if (bytes.length < at + 4 + length) return;
        const payload = Buffer.from(bytes.subarray(at + 4, at + 4 + length));
        for (let i = 0; i < payload.length; i++) payload[i] ^= bytes[at + i % 4];
        bytes = bytes.subarray(at + 4 + length);
        if (op === 8) { socket.end(frame(8, payload)); return; }
        if (op === 9) { socket.write(frame(10, payload)); continue; }
        if (op !== 1) { socket.destroy(); return; }
        rpc(peer, JSON.parse(payload.toString('utf8')));
      }
    });
  });
  await new Promise<void>((resolve, reject) => { server.once('error', reject); server.listen(target, resolve); });
  await symlink(target, socketPath);
  return {
    home, socketPath, target, requests, replies, paths, threads,
    failNext(method: string, failure: Failure) { failures.set(method, [...(failures.get(method) ?? []), failure]); },
    setListVisible(value: boolean) { listVisible = value; },
    setPageSize(value: number) { pageSize = value; },
    disconnect() { for (const p of peers) p.socket.destroy(); },
    serverRequest(method: string, params: Params) {
      const id = ++requestId;
      // Broadcast even foreign-thread requests, to exercise ownership filtering.
      for (const peer of peers) peer.send({ id, method, params });
      return id;
    },
    complete(id: string, items: Params[], status: NativeTurn['status'] = 'completed') {
      const t = [...threads.values()].find(t => t.turns.some(turn => turn.id === id));
      const turn = t?.turns.find(turn => turn.id === id);
      if (!t || !turn) throw new Error(`unknown native turn: ${id}`);
      turn.status = status; turn.items.push(...items); turn.completedAt = 1794096000;
      t.status = { type: 'idle' };
      notice('turn/completed', { threadId: t.id, turn });
    },
    async close() {
      for (const peer of peers) peer.socket.destroy();
      await new Promise<void>(resolve => server.close(() => resolve()));
    },
  };
}
