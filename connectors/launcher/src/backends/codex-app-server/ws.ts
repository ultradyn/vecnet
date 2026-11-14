import { createConnection } from 'node:net';
import { createHash, randomBytes } from 'node:crypto';
import { TextDecoder } from 'node:util';

export interface WsConn {
  send(text: string): void;
  onMessage(fn: (text: string) => void): void;
  onClose(fn: (why: string) => void): void;
  close(): void;
}

const guid = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
function frame(opcode: number, payload: Buffer): Buffer {
  const at = payload.length < 126 ? 2 : payload.length <= 65535 ? 4 : 10;
  const out = Buffer.allocUnsafe(at + 4 + payload.length);
  out[0] = 0x80 | opcode;
  out[1] = 0x80 | (at === 2 ? payload.length : at === 4 ? 126 : 127);
  if (at === 4) out.writeUInt16BE(payload.length, 2);
  if (at === 10) out.writeBigUInt64BE(BigInt(payload.length), 2);
  const mask = randomBytes(4); mask.copy(out, at);
  for (let n = 0; n < payload.length; n++) out[at + 4 + n] = payload[n] ^ mask[n % 4];
  return out;
}

/** Minimal RFC 6455 client. Discovery and JSON-RPC belong to the caller. */
export function connectUnixWebSocket(socketPath: string, o: {
  maxMessageBytes: number; timeoutMs: number; signal?: AbortSignal;
}): Promise<WsConn> {
  return new Promise((resolve, reject) => {
    if (!Number.isSafeInteger(o.maxMessageBytes) || o.maxMessageBytes < 1 ||
        !Number.isFinite(o.timeoutMs) || o.timeoutMs <= 0) {
      reject(new RangeError('invalid WebSocket limits')); return;
    }
    if (o.signal?.aborted) { reject(o.signal.reason ?? new Error('WebSocket aborted')); return; }
    const socket = createConnection({ path: socketPath });
    const key = randomBytes(16).toString('base64');
    const expected = createHash('sha1').update(key + guid).digest('base64');
    let ready = false, ended: string | undefined;
    let bytes: Buffer = Buffer.alloc(0), fragments: Buffer[] = [], fragmentBytes = 0, fragmenting = false;
    const listeners: ((text: string) => void)[] = [], closers: ((why: string) => void)[] = [];
    const queued: string[] = []; let queuedBytes = 0;
    const timeout = setTimeout(() => finish('WebSocket handshake timeout'), o.timeoutMs);
    function finish(why: string, closePayload?: Buffer) {
      if (ended !== undefined) return;
      ended = why; clearTimeout(timeout); o.signal?.removeEventListener('abort', abort);
      // Completed messages may precede close in the upgrade chunk, before the
      // connect caller can attach listeners. Keep them for onMessage to drain.
      bytes = Buffer.alloc(0); fragments = [];
      if (closePayload && ready && socket.writable) { socket.end(frame(8, closePayload)); socket.destroySoon(); }
      else socket.destroy();
      if (!ready) reject(new Error(why));
      else for (const fn of closers) fn(why);
    }
    function abort() { finish('WebSocket aborted'); }
    o.signal?.addEventListener('abort', abort, { once: true });
    const conn: WsConn = {
      send(text) {
        if (ended !== undefined) throw new Error('WebSocket closed');
        const data = Buffer.from(text);
        if (data.length > o.maxMessageBytes) throw new RangeError('WebSocket maxMessageBytes exceeded');
        socket.write(frame(1, data));
      },
      onMessage(fn) {
        listeners.push(fn);
        for (const text of queued.splice(0)) fn(text);
        queuedBytes = 0;
      },
      onClose(fn) { if (ended !== undefined) queueMicrotask(() => fn(ended!)); else closers.push(fn); },
      close() { finish('WebSocket local close', Buffer.from([3, 232])); },
    };
    socket.once('connect', () => {
      if (ended !== undefined) return;
      socket.write(`GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ${key}\r\nSec-WebSocket-Version: 13\r\n\r\n`);
    });
    socket.on('error', () => finish('WebSocket socket error'));
    socket.on('end', () => finish('WebSocket peer ended'));
    socket.on('close', () => finish('WebSocket socket closed'));
    function protocol(why: string, code = 1002) {
      const payload = Buffer.alloc(2); payload.writeUInt16BE(code); finish(why, payload);
    }
    function text(payload: Buffer) {
      let value: string;
      try { value = new TextDecoder('utf-8', { fatal: true }).decode(payload); }
      catch { protocol('WebSocket invalid UTF-8', 1007); return; }
      if (listeners.length) for (const fn of listeners) fn(value);
      else {
        queuedBytes += payload.length;
        if (queuedBytes > o.maxMessageBytes || queued.length >= 1024) { protocol('WebSocket message queue limit', 1009); return; }
        queued.push(value);
      }
    }
    socket.on('data', chunk => {
      if (ended !== undefined) return;
      bytes = Buffer.concat([bytes, chunk]);
      if (!ready) {
        const end = bytes.indexOf('\r\n\r\n');
        if (end < 0) { if (bytes.length > 16384) finish('WebSocket handshake header too large'); return; }
        if (end > 16384) { finish('WebSocket handshake header too large'); return; }
        const lines = bytes.subarray(0, end).toString('ascii').split('\r\n');
        if (!/^HTTP\/1\.1 101(?: |$)/.test(lines.shift()!)) { finish('WebSocket non-101 response'); return; }
        const headers = new Map<string, string[]>();
        for (const line of lines) {
          const colon = line.indexOf(':');
          if (colon < 1) { finish('WebSocket malformed handshake header'); return; }
          const name = line.slice(0, colon).toLowerCase();
          headers.set(name, [...(headers.get(name) ?? []), line.slice(colon + 1).trim()]);
        }
        const accepts = headers.get('sec-websocket-accept');
        if (accepts?.length !== 1 || accepts[0] !== expected) { finish('WebSocket invalid Sec-WebSocket-Accept'); return; }
        if (headers.get('upgrade')?.join(',').toLowerCase() !== 'websocket' ||
            !headers.get('connection')?.join(',').toLowerCase().split(',').some(s => s.trim() === 'upgrade') ||
            headers.has('sec-websocket-extensions') || headers.has('sec-websocket-protocol')) {
          finish('WebSocket invalid upgrade headers'); return;
        }
        bytes = bytes.subarray(end + 4); ready = true; clearTimeout(timeout); resolve(conn);
      }
      while (ended === undefined && bytes.length >= 2) {
        const fin = !!(bytes[0] & 128), op = bytes[0] & 15, control = op >= 8;
        if (bytes[0] & 0x70 || bytes[1] & 128) { protocol('WebSocket invalid frame flags'); return; }
        if (![0, 1, 8, 9, 10].includes(op) || (control && !fin)) { protocol('WebSocket unsupported frame'); return; }
        let length = bytes[1] & 127, at = 2;
        if (control && length > 125) { protocol('WebSocket oversized control frame'); return; }
        if (length === 126) {
          if (bytes.length < 4) return;
          length = bytes.readUInt16BE(2); at = 4;
          if (length < 126) { protocol('WebSocket noncanonical length'); return; }
        } else if (length === 127) {
          if (bytes.length < 10) return;
          const big = bytes.readBigUInt64BE(2);
          if (big < 65536n || big >> 63n) { protocol('WebSocket invalid 64-bit length'); return; }
          if (big > BigInt(o.maxMessageBytes)) { protocol('WebSocket maxMessageBytes exceeded', 1009); return; }
          length = Number(big); at = 10;
        }
        if (!control && (length > o.maxMessageBytes || fragmentBytes + length > o.maxMessageBytes)) {
          protocol('WebSocket maxMessageBytes exceeded', 1009); return;
        }
        if ((op === 0 && !fragmenting) || (op === 1 && fragmenting)) { protocol('WebSocket invalid continuation'); return; }
        if (bytes.length < at + length) return;
        const payload = Buffer.from(bytes.subarray(at, at + length)); bytes = bytes.subarray(at + length);
        if (op === 8) {
          if (length === 1) { protocol('WebSocket invalid close frame'); return; }
          if (length >= 2) {
            const code = payload.readUInt16BE();
            if (code < 1000 || code >= 5000 || [1004, 1005, 1006, 1015].includes(code) || (code >= 1016 && code < 3000)) {
              protocol('WebSocket invalid close code'); return;
            }
            try { new TextDecoder('utf-8', { fatal: true }).decode(payload.subarray(2)); }
            catch { protocol('WebSocket invalid close reason', 1007); return; }
          }
          finish('WebSocket peer close', payload); return;
        }
        if (op === 9) { socket.write(frame(10, payload)); continue; }
        if (op === 10) continue;
        if (op === 1 && fin) text(payload);
        else {
          fragments.push(payload); fragmentBytes += length; fragmenting = !fin;
          if (fin) { const complete = Buffer.concat(fragments, fragmentBytes); fragments = []; fragmentBytes = 0; text(complete); }
        }
      }
    });
  });
}
