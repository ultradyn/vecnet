import { createServer } from 'node:net';
import type { Socket } from 'node:net';
import { createHash, randomBytes } from 'node:crypto';
import { mkdir, chmod, symlink } from 'node:fs/promises';
import { join } from 'node:path';
import type { Clock } from '../../hub-client/src/ports.ts';

type Params = Record<string, unknown>;
type ThreadInput = { id: string; status: unknown; activeTurnId?: string; approvalPolicy?: string; approvalsReviewer?: string; source?: string };
type Item = { id: string; type: 'userMessage'; clientId?: string; content: unknown };
type Turn = { id: string; status: string; items: Item[]; error: null };
type Thread = ThreadInput & { turns: Turn[]; members: Set<number>; other: number; unload?: { cancel(): void } };
type Peer = { id: number; socket: Socket; optOuts: string[]; send(value: unknown): void };
export interface FakeCodex {
  socketPath: string;
  requests: { method: string; params: unknown; id?: number; conn: number }[];
  paths: string[];
  setVersion(v: string): void;
  setThread(t: ThreadInput): void;
  notify(method: string, params: unknown): void;
  notifySubscribers(threadId: string, method: string, params: unknown): void;
  /** Update native state and history, emitting only the terminal notice. */
  completeTurnOnly(threadId: string, turnId: string, nextStatus: unknown): void;
  /** Capture the next read on the wire until explicitly released. */
  gateNextRead(): { entered: boolean; released: boolean; release(): void };
  serverRequest(threadId: string, method: string, params: unknown): number;
  resolve(requestId: number): void;
  replies: unknown[];
  subscribers(threadId: string): number[];
  optOuts(conn: number): string[];
  attachOther(threadId: string): void;
  detachOther(threadId: string): void;
  failNext(method: string, err: { code: number; message: string } | 'drop'): void;
  close(): Promise<void>;
  // Test controls described in the plan but omitted from its interface block.
  overrides: { threadId: string; params: unknown; conn: number }[];
  addTurns(threadId: string, n: number): void;
  withholdNextItem(): void;
  recordLater(clientId: string, atMs: number): void;
}
const guid = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
function frame(op: number, payload: Buffer): Buffer {
  const at = payload.length < 126 ? 2 : payload.length < 65536 ? 4 : 10;
  const head = Buffer.alloc(at); head[0] = 128 | op;
  head[1] = at === 2 ? payload.length : at === 4 ? 126 : 127;
  if (at === 4) head.writeUInt16BE(payload.length, 2);
  if (at === 10) head.writeBigUInt64BE(BigInt(payload.length), 2);
  return Buffer.concat([head, payload]);
}
const typeOf = (status: unknown): unknown => typeof status === 'string' ? status : (status as Params | undefined)?.type;
const paramsOf = (p: unknown): Params => p !== null && typeof p === 'object' && !Array.isArray(p) ? p as Params : {};

/** A local-only Codex protocol simulator. It never starts a child process. */
export async function startFakeCodexAppServer(o: { dir: string; clock?: Clock; unloadDelayMs?: number; coldResumeIdleDelayMs?: number;
  /** Real (wall-clock) delay before answering thread/unsubscribe: a slow socket or engine shutdown. */
  unsubscribeDelayMs?: number }): Promise<FakeCodex> {
  // dir is always supplied by the test; there is no real-home fallback.
  const clock: Pick<Clock, 'now' | 'timer'> = o.clock ?? {
    now: () => Date.now(), timer(ms, fn) { const id = setTimeout(fn, ms); return { cancel: () => clearTimeout(id) }; },
  };
  const delay = o.unloadDelayMs ?? 60_000;
  const targetDir = join(o.dir, 'd'), home = join(o.dir, 'home', 'app-server-control');
  await mkdir(targetDir, { recursive: true, mode: 0o700 }); await chmod(targetDir, 0o700);
  await mkdir(home, { recursive: true, mode: 0o700 });
  const target = process.platform === 'win32' ? `\\\\.\\pipe\\vn-codex-${randomBytes(12).toString('hex')}` : join(targetDir, randomBytes(8).toString('hex'));
  const socketPath = process.platform === 'win32' ? target : join(home, 'app-server-control.sock');
  const peers = new Map<number, Peer>(), threads = new Map<string, Thread>();
  const pending = new Map<number, { threadId: string; method: string; params: unknown }>();
  const failures = new Map<string, ({ code: number; message: string } | 'drop')[]>();
  const readGates: ((answer: () => void) => void)[] = [];
  const withheld = new Map<string, { thread: Thread; turn: Turn; item: Item }>();
  const timers = new Set<{ cancel(): void }>();
  const reservedTurnIds = new Set<string>();
  // Keep provenance off the wire, including when a receipt is recorded later.
  const steeredItems = new WeakSet<Item>();
  let version = '0.166.0', connectionId = 0, serverId = 10000, turnId = 0, itemId = 0;
  let holdNext = false, stopped = false;
  const requests: FakeCodex['requests'] = [], replies: unknown[] = [], paths: string[] = [], overrides: FakeCodex['overrides'] = [];
  function nextTurnId(): string {
    let id: string;
    do { id = `turn-${++turnId}`; } while (reservedTurnIds.has(id));
    reservedTurnIds.add(id);
    return id;
  }
  function get(id: string): Thread {
    const t = threads.get(id); if (!t) throw new Error(`unknown fake thread: ${id}`); return t;
  }
  function sendNotice(p: Peer, method: string, params: unknown) {
    if (!p.optOuts.includes(method)) p.send({ method, params });
  }
  function broadcast(method: string, params: unknown) { for (const p of peers.values()) sendNotice(p, method, params); }
  function subscribers(threadId: string, method: string, params: unknown) {
    for (const id of get(threadId).members) { const p = peers.get(id); if (p) sendNotice(p, method, params); }
  }
  function status(t: Thread, value: unknown) {
    t.status = value; broadcast('thread/status/changed', { threadId: t.id, status: value }); schedule(t);
  }
  function schedule(t: Thread) {
    if (stopped || t.members.size || t.other || typeOf(t.status) === 'active' || typeOf(t.status) === 'notLoaded') {
      t.unload?.cancel(); t.unload = undefined; return;
    }
    if (t.unload) return;
    t.unload = clock.timer(delay, () => {
      t.unload = undefined;
      if (stopped) return;
      status(t, { type: 'notLoaded' }); broadcast('thread/closed', { threadId: t.id });
    });
  }
  function detach(p: Peer) {
    peers.delete(p.id);
    for (const t of threads.values()) if (t.members.delete(p.id)) schedule(t);
  }
  function snapshot(t: Thread, includeTurns = true) {
    return { id: t.id, status: t.status, archived: false, ...(t.source ? { source: t.source } : {}), turns: includeTurns ? t.turns : [] };
  }
  function receipt(t: Thread, turn: Turn, item: Item) {
    turn.items.push(item); subscribers(t.id, 'item/started', { threadId: t.id, turnId: turn.id, item });
  }
  function rpc(p: Peer, text: string) {
    let m: Params;
    try { m = paramsOf(JSON.parse(text)); } catch { p.socket.destroy(); return; }
    if (typeof m.method !== 'string') { if ('id' in m) replies.push(m); return; }
    const method = m.method, args = paramsOf(m.params);
    requests.push({ method, params: m.params, ...(typeof m.id === 'number' ? { id: m.id } : {}), conn: p.id });
    const respond = (result: unknown) => { if ('id' in m) p.send({ id: m.id, result }); };
    const error = (code: number, message: string) => { if ('id' in m) p.send({ id: m.id, error: { code, message } }); };
    const fail = failures.get(method)?.shift();
    if (fail) { if (fail === 'drop') p.socket.destroy(); else error(fail.code, fail.message); return; }
    if (method === 'initialize') {
      const opted = paramsOf(args.capabilities).optOutNotificationMethods;
      p.optOuts = Array.isArray(opted) ? opted.filter((x): x is string => typeof x === 'string') : [];
      respond({ userAgent: `codex-tui/${version} (fake)` }); return;
    }
    if (method === 'initialized') return;
    const t = threads.get(String(args.threadId));
    if (!t) { error(-32600, 'thread not found'); return; }
    if (method === 'thread/read') {
      const gate = readGates.shift();
      if (gate) { const result = structuredClone({ thread: snapshot(t) }); gate(() => respond(result)); }
      else respond({ thread: snapshot(t) });
      return;
    }
    if (method === 'thread/turns/list') {
      const limit = typeof args.limit === 'number' && Number.isInteger(args.limit) && args.limit > 0 ? args.limit : 5;
      let offset = 0;
      if (args.cursor !== undefined && args.cursor !== null) {
        // Opaque, scoped token: callers must pass it back verbatim.
        const match = /^fake-page:(.*):(\d+)$/.exec(String(args.cursor));
        if (!match || match[1] !== t.id) { error(-32602, 'invalid cursor'); return; }
        offset = Number(match[2]);
      }
      const all = [...t.turns].reverse();
      const data = all.slice(offset, offset + limit).map(turn => ({ ...turn,
        items: args.itemsView === 'full' ? turn.items : turn.items.filter(item => !steeredItems.has(item)) }));
      respond({ data, nextCursor: offset + limit < all.length ? `fake-page:${t.id}:${offset + limit}` : null }); return;
    }
    if (method === 'thread/resume') {
      if (Object.keys(args).some(k => k !== 'threadId' && k !== 'excludeTurns')) overrides.push({ threadId: t.id, params: m.params, conn: p.id });
      const cold = typeOf(t.status) === 'notLoaded';
      t.members.add(p.id);
      if (cold && o.coldResumeIdleDelayMs) {
        // Keep the response warm while placing this resume's own broadcast
        // after it, as a real app-server may do.
        t.status = { type: 'idle' };
        const timer = clock.timer(o.coldResumeIdleDelayMs, () => {
          timers.delete(timer); if (!stopped) status(t, { type: 'idle' });
        });
        timers.add(timer);
      } else if (cold) status(t, { type: 'idle' }); else schedule(t);
      respond({ thread: snapshot(t, args.excludeTurns !== true), approvalPolicy: t.approvalPolicy ?? 'never',
        approvalsReviewer: t.approvalsReviewer ?? 'user', sandbox: { type: 'workspaceWrite' } });
      if (cold) sendNotice(p, 'mcpServer/startupStatus/updated', { threadId: t.id, name: 'fake', status: 'ready' });
      for (const [id, req] of pending) if (req.threadId === t.id) p.send({ id, method: req.method, params: req.params });
      return;
    }
    if (method === 'thread/unsubscribe') {
      const removed = t.members.delete(p.id); schedule(t);
      const answer = () => respond({ status: typeOf(t.status) === 'notLoaded' ? 'notLoaded' : removed ? 'unsubscribed' : 'notSubscribed' });
      if (o.unsubscribeDelayMs) setTimeout(answer, o.unsubscribeDelayMs); else answer();
      return;
    }
    if (method === 'turn/start' || method === 'turn/steer') {
      if (typeOf(t.status) === 'notLoaded') { error(-32600, 'thread not found'); return; }
      if (method === 'turn/steer' && (typeOf(t.status) !== 'active' || !t.activeTurnId || args.expectedTurnId !== t.activeTurnId)) {
        error(-32600, 'expectedTurnId mismatch'); return;
      }
      let turn = t.turns.find(x => x.id === t.activeTurnId);
      const starting = typeOf(t.status) !== 'active' || !turn;
      if (typeOf(t.status) !== 'active' || !turn) {
        turn = { id: nextTurnId(), items: [], status: 'inProgress', error: null };
        t.turns.push(turn); t.activeTurnId = turn.id; status(t, { type: 'active', activeFlags: [] });
        subscribers(t.id, 'turn/started', { threadId: t.id, turn });
      }
      const item: Item = { id: `item-${++itemId}`, type: 'userMessage', content: args.input ?? [],
        ...(typeof args.clientUserMessageId === 'string' ? { clientId: args.clientUserMessageId } : {}) };
      if (!starting) steeredItems.add(item);
      if (holdNext) {
        holdNext = false;
        if (!item.clientId) throw new Error('withheld fake item requires clientUserMessageId');
        withheld.set(item.clientId, { thread: t, turn, item });
      } else receipt(t, turn, item);
      respond(method === 'turn/start' ? { turn } : { turnId: turn.id }); return;
    }
    error(-32601, 'method not found');
  }
  const server = createServer(socket => {
    const p: Peer = { id: ++connectionId, socket, optOuts: [],
      send(value) { if (!socket.destroyed) socket.write(frame(1, Buffer.from(JSON.stringify(value)))); } };
    peers.set(p.id, p);
    let bytes: Buffer = Buffer.alloc(0), ready = false, fragmenting = false, parts: Buffer[] = [], total = 0;
    const max = 4 * 1024 * 1024;
    socket.on('error', () => {}); socket.once('close', () => detach(p));
    socket.on('data', chunk => {
      bytes = Buffer.concat([bytes, chunk]);
      if (!ready) {
        const end = bytes.indexOf('\r\n\r\n');
        if (end < 0) { if (bytes.length > 16384) socket.destroy(); return; }
        const head = bytes.subarray(0, end).toString('ascii'); bytes = bytes.subarray(end + 4);
        const request = /^GET (\S+) HTTP\/1\.1\r\n/.exec(head), key = /\r\nSec-WebSocket-Key:\s*([^\r\n]+)/i.exec(head)?.[1].trim();
        if (request) paths.push(request[1]);
        if (end > 16384 || request?.[1] !== '/' || !key || !/\r\nUpgrade:\s*websocket\r\n/i.test(head + '\r\n')) {
          socket.end('HTTP/1.1 400 Bad Request\r\n\r\n'); return;
        }
        const accept = createHash('sha1').update(key + guid).digest('base64');
        socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`); ready = true;
      }
      while (!socket.destroyed && bytes.length >= 2) {
        const op = bytes[0] & 15, fin = !!(bytes[0] & 128), control = op >= 8;
        let len = bytes[1] & 127, at = 2;
        if (bytes[0] & 112 || !(bytes[1] & 128) || ![0, 1, 8, 9, 10].includes(op) || (control && (!fin || len > 125))) { socket.destroy(); return; }
        if (len === 126) { if (bytes.length < 4) return; len = bytes.readUInt16BE(2); at = 4; }
        else if (len === 127) { if (bytes.length < 10) return; const big = bytes.readBigUInt64BE(2); if (big > BigInt(max)) { socket.destroy(); return; } len = Number(big); at = 10; }
        if (total + len > max || (op === 0 && !fragmenting) || (op === 1 && fragmenting)) { socket.destroy(); return; }
        if (bytes.length < at + 4 + len) return;
        const payload = Buffer.from(bytes.subarray(at + 4, at + 4 + len));
        payload.forEach((b, n) => { payload[n] = b ^ bytes[at + n % 4]; }); bytes = bytes.subarray(at + 4 + len);
        if (op === 8) { socket.end(frame(8, payload)); socket.destroySoon(); return; }
        if (op === 9) { socket.write(frame(10, payload)); continue; }
        if (op === 10) continue;
        parts.push(payload); total += len; fragmenting = !fin;
        if (fin) {
          let text: string;
          try { text = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(parts, total)); }
          catch { socket.destroy(); return; }
          parts = []; total = 0; rpc(p, text);
        }
      }
    });
  });
  await new Promise<void>((resolve, reject) => { server.once('error', reject); server.listen(target, resolve); });
  try { if (process.platform !== 'win32') await symlink(target, socketPath); }
  catch (error) { await new Promise<void>(resolve => server.close(() => resolve())); throw error; }
  return {
    socketPath, requests, paths, replies, overrides,
    setVersion(v) { version = v; },
    setThread(input) {
      if (input.activeTurnId) reservedTurnIds.add(input.activeTurnId);
      let t = threads.get(input.id);
      if (!t) { t = { ...input, turns: [], members: new Set(), other: 0 }; threads.set(t.id, t); }
      else {
        if (typeOf(input.status) !== 'active' && t.activeTurnId) {
          const activeId = t.activeTurnId;
          const turn = t.turns.find(x => x.id === activeId); if (turn) turn.status = 'completed';
          t.activeTurnId = undefined;
        }
        Object.assign(t, input);
      }
      if (input.activeTurnId && !t.turns.some(x => x.id === input.activeTurnId)) {
        t.turns.push({ id: input.activeTurnId, items: [], status: 'inProgress', error: null });
      }
      status(t, input.status);
    },
    notify(method, params) {
      const a = paramsOf(params), t = threads.get(String(a.threadId));
      if (t && method === 'thread/status/changed') { status(t, a.status); return; }
      if (t && method === 'turn/completed') {
        const turn = t.turns.find(x => x.id === paramsOf(a.turn).id || x.id === a.turnId);
        if (turn) Object.assign(turn, paramsOf(a.turn), { status: paramsOf(a.turn).status ?? 'completed' });
        t.activeTurnId = undefined; status(t, { type: 'idle' });
      }
      broadcast(method, params);
    },
    notifySubscribers: subscribers,
    completeTurnOnly(threadId, turnId, nextStatus) {
      const t = get(threadId), turn = t.turns.find(x => x.id === turnId);
      if (!turn) throw new Error(`unknown fake turn: ${turnId}`);
      turn.status = 'completed';
      if (t.activeTurnId === turnId) t.activeTurnId = undefined;
      t.status = nextStatus; schedule(t);
      subscribers(threadId, 'turn/completed', { threadId, turn: { ...turn } });
    },
    gateNextRead() {
      let answer: (() => void) | undefined;
      const gate = { entered: false, released: false, release() {
        if (gate.released) return;
        gate.released = true; answer?.();
      } };
      readGates.push(reply => { gate.entered = true; answer = reply; if (gate.released) reply(); });
      return gate;
    },
    serverRequest(threadId, method, params) {
      const id = ++serverId; pending.set(id, { threadId, method, params });
      for (const member of get(threadId).members) peers.get(member)?.send({ id, method, params });
      return id;
    },
    resolve(requestId) {
      const req = pending.get(requestId); if (!req) return;
      pending.delete(requestId); subscribers(req.threadId, 'serverRequest/resolved', { threadId: req.threadId, requestId });
    },
    subscribers: threadId => [...get(threadId).members],
    optOuts: id => [...(peers.get(id)?.optOuts ?? [])],
    attachOther(threadId) { const t = get(threadId); t.other++; schedule(t); },
    detachOther(threadId) { const t = get(threadId); t.other = Math.max(0, t.other - 1); schedule(t); },
    failNext(method, err) { failures.set(method, [...(failures.get(method) ?? []), err]); },
    addTurns(threadId, n) {
      const t = get(threadId);
      for (let k = 0; k < n; k++) t.turns.push({ id: nextTurnId(), items: [], status: 'completed', error: null });
    },
    withholdNextItem() { holdNext = true; },
    recordLater(clientId, atMs) {
      const entry = withheld.get(clientId); if (!entry) throw new Error(`no withheld item: ${clientId}`);
      withheld.delete(clientId);
      const timer = clock.timer(Math.max(0, atMs - clock.now()), () => {
        timers.delete(timer); if (!stopped) receipt(entry.thread, entry.turn, entry.item);
      });
      timers.add(timer);
    },
    async close() {
      if (stopped) return; stopped = true;
      for (const t of threads.values()) t.unload?.cancel();
      for (const timer of timers) timer.cancel();
      for (const p of peers.values()) p.socket.destroy();
      await new Promise<void>(resolve => server.close(() => resolve()));
    },
  };
}
