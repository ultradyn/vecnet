// Private, offline-testable observation of the installed connector's real wire.
// The pipes own transport/backpressure; the parsers never write to either peer.
import net from 'node:net';
import { fork } from 'node:child_process';
import { constants } from 'node:fs';
import { constants as bufferConstants } from 'node:buffer';
import { open, lstat, link, unlink, mkdtemp, rmdir } from 'node:fs/promises';
import { dirname, basename, isAbsolute } from 'node:path';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const MAX_HEADER = 16 * 1024;
const MAX_PENDING = 1024;
const MAX_FRAGMENTS = 1024;
class ParseFailure extends Error {}
function requireTrace(ok, reason = 'invalid_rpc') {
  if (!ok) throw new ParseFailure(reason);
}
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const token = value => typeof value === 'string' && /^[A-Za-z0-9_.:-]{1,256}$/.test(value);
const rpcId = value => Number.isSafeInteger(value) || token(value);
const sameInode = (a, b) => a?.dev === b?.dev && a?.ino === b?.ino;
async function statOrMissing(path) {
  try { return await lstat(path); } catch (error) {
    if (error.code === 'ENOENT') return undefined;
    throw error;
  }
}

// Receiving a net.Server through documented IPC adopts the listening descriptor
// without the binder's libuv filename/unlink authority. The binder exits without
// calling server.close(); the kernel closes its descriptors but leaves the name.
async function bindListener(path) {
  const child = fork(fileURLToPath(import.meta.url), ['--rpc-tap-bind', path], {
    env: {}, execArgv: [], stdio: ['ignore', 'ignore', 'ignore', 'ipc'],
  });
  let server, failed = false;
  const receivedError = () => { failed = true; };
  child.on('error', receivedError);
  child.on('message', (message, handle) => {
    if (message !== 'rpc-tap-listener' || !(handle instanceof net.Server) || server) {
      failed = true;
      handle?.close?.();
      child.kill('SIGKILL');
      return;
    }
    server = handle;
    server.on('error', receivedError);
  });
  const deadline = setTimeout(() => { failed = true; child.kill('SIGKILL'); }, 2500);
  const code = await new Promise(resolve => child.once('close', code => resolve(code)));
  clearTimeout(deadline);
  if (failed || code !== 0 || !server?.listening) {
    if (server?.listening) await new Promise(resolve => server.close(resolve));
    throw new Error('tap_bind_failed');
  }
  server.off('error', receivedError);
  return server;
}

if (process.argv[1] === fileURLToPath(import.meta.url) && process.argv[2] === '--rpc-tap-bind' && process.send) {
  const server = net.createServer();
  // A disconnected/crashed caller cannot leave a helper alive. No inherited
  // environment, output or arbitrary error crosses this private IPC channel.
  setTimeout(() => process.exit(1), 2000).unref();
  process.once('disconnect', () => process.exit(1));
  server.once('error', () => process.exit(1));
  server.listen(process.argv[3], () => {
    process.send('rpc-tap-listener', server, error => process.exit(error ? 1 : 0));
  });
}

function headers(bytes, client, state) {
  // Header text (including authorization) is temporary and never enters events.
  requireTrace(!bytes.some(b => b < 32 && b !== 13 && b !== 10 && b !== 9 || b > 126), 'invalid_upgrade');
  const lines = bytes.toString('ascii').split('\r\n');
  const first = lines.shift();
  requireTrace(client ? /^GET \S+ HTTP\/1\.1$/.test(first) : /^HTTP\/1\.1 101(?: .*)?$/.test(first), 'invalid_upgrade');
  const fields = new Map();
  for (const line of lines) {
    if (!line) continue;
    const m = /^([!#$%&'*+.^_`|~0-9A-Za-z-]+):[ \t]*(.*)$/.exec(line);
    requireTrace(m, 'invalid_upgrade');
    const key = m[1].toLowerCase();
    requireTrace(!fields.has(key), 'invalid_upgrade');
    fields.set(key, m[2].trim());
  }
  requireTrace(fields.get('upgrade')?.toLowerCase() === 'websocket' &&
    fields.get('connection')?.toLowerCase().split(',').map(s => s.trim()).includes('upgrade'), 'invalid_upgrade');
  requireTrace(!fields.has('transfer-encoding') && (!fields.has('content-length') || fields.get('content-length') === '0'), 'invalid_upgrade');
  if (client) {
    const key = fields.get('sec-websocket-key');
    requireTrace(fields.get('sec-websocket-version') === '13' && typeof key === 'string' &&
      /^[A-Za-z0-9+/]{22}==$/.test(key) && Buffer.from(key, 'base64').length === 16, 'invalid_upgrade');
    state.accept = createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
  } else {
    requireTrace(state.accept && fields.get('sec-websocket-accept') === state.accept &&
      !fields.has('sec-websocket-extensions'), 'invalid_upgrade');
    state.accept = undefined;
  }
}

// One bounded streaming parser per direction. Even a huge coalesced input is
// consumed incrementally: only one message and a 14-byte frame header survive.
function parser(client, limit, state, message) {
  let http = Buffer.alloc(MAX_HEADER), httpSize = 0;
  let header = Buffer.alloc(14), headerSize = 0, headerNeed = 2;
  let payload, payloadSize = 0, opcode, final, mask;
  let parts = [], messageSize = 0, fragmented = false;
  const decoder = new TextDecoder('utf-8', { fatal: true });
  function finishFrame() {
    const body = payload;
    payload = undefined;
    if (opcode >= 8) {
      if (opcode === 8) {
        requireTrace(body.length !== 1, 'invalid_frame');
        if (body.length >= 2) {
          const code = body.readUInt16BE();
          requireTrace(code >= 1000 && code < 5000 && ![1004, 1005, 1006, 1015].includes(code) &&
            (code <= 1014 || code >= 3000), 'invalid_frame');
          try { decoder.decode(body.subarray(2)); } catch { throw new ParseFailure('invalid_utf8'); }
        }
        throw new ParseFailure('websocket_closed');
      }
      return; // Ping/pong do not disturb text continuation.
    }
    requireTrace(parts.length < MAX_FRAGMENTS, 'too_many_fragments');
    parts.push(body);
    if (!final) { fragmented = true; return; }
    const bytes = parts.length === 1 ? parts[0] : Buffer.concat(parts, messageSize);
    parts = []; messageSize = 0; fragmented = false;
    let text, value;
    try { text = decoder.decode(bytes); } catch { throw new ParseFailure('invalid_utf8'); }
    try { value = JSON.parse(text); } catch { throw new ParseFailure('invalid_json'); }
    message(value, client);
  }
  function validatePrefix() {
    if (headerSize < 2) return;
    const fin = Boolean(header[0] & 128), op = header[0] & 15;
    const masked = Boolean(header[1] & 128), short = header[1] & 127;
    requireTrace(!(header[0] & 0x70) && masked === client && [0, 1, 8, 9, 10].includes(op), 'invalid_frame');
    if (op >= 8) requireTrace(fin && short <= 125, 'invalid_frame');
    else requireTrace(op === 0 ? fragmented : !fragmented, 'invalid_frame');
    const lengthEnd = 2 + (short === 126 ? 2 : short === 127 ? 8 : 0);
    if (headerSize < lengthEnd) return;
    let size = short;
    if (short === 126) {
      size = header.readUInt16BE(2);
      requireTrace(size >= 126, 'invalid_frame');
    } else if (short === 127) {
      const big = header.readBigUInt64BE(2);
      requireTrace(big >= 65536n && big < (1n << 63n), 'invalid_frame');
      requireTrace(big <= BigInt(limit), 'message_too_large');
      size = Number(big);
    }
    if (op < 8) requireTrace(size <= limit - messageSize, 'message_too_large');
    requireTrace(size <= limit, 'message_too_large');
    return size;
  }
  function beginFrame() {
    const size = validatePrefix();
    final = Boolean(header[0] & 128);
    opcode = header[0] & 15;
    if (opcode < 8) messageSize += size;
    mask = client ? Buffer.from(header.subarray(headerNeed - 4, headerNeed)) : undefined;
    payload = Buffer.alloc(size);
    payloadSize = 0; headerSize = 0; headerNeed = 2;
    if (size === 0) finishFrame();
  }
  return {
    feed(chunk) {
      let at = 0;
      if (http) {
        while (at < chunk.length) {
          requireTrace(httpSize < MAX_HEADER, 'header_too_large');
          http[httpSize++] = chunk[at++];
          if (httpSize >= 4 && http.readUInt32BE(httpSize - 4) === 0x0d0a0d0a) {
            headers(http.subarray(0, httpSize), client, state);
            http = undefined;
            break;
          }
        }
      }
      while (!http && at < chunk.length) {
        if (payload) {
          const count = Math.min(payload.length - payloadSize, chunk.length - at);
          if (client) {
            for (let i = 0; i < count; i++) payload[payloadSize + i] = chunk[at + i] ^ mask[(payloadSize + i) % 4];
          } else chunk.copy(payload, payloadSize, at, at + count);
          payloadSize += count; at += count;
          if (payloadSize === payload.length) finishFrame();
        } else {
          const count = Math.min(headerNeed - headerSize, chunk.length - at);
          chunk.copy(header, headerSize, at, at + count);
          headerSize += count; at += count;
          if (headerSize === 2 && headerNeed === 2) {
            const short = header[1] & 127;
            headerNeed += (short === 126 ? 2 : short === 127 ? 8 : 0) + (header[1] & 128 ? 4 : 0);
          }
          validatePrefix(); // Reject an invalid known length before waiting for a mask.
          if (headerSize === headerNeed) beginFrame();
        }
      }
    },
    incomplete: () => Boolean(http || payload || headerSize || fragmented),
    dispose() { http = undefined; payload = undefined; parts = []; header = undefined; mask = undefined; },
  };
}

/**
 * Events share {seq, at (epoch ms), connectionId, client} with client restricted
 * to vecnet / observer / unknown. Kinds: request, response, completed, failure.
 * Only turn/steer requests and their correlated responses are exported. A
 * consumer MUST invalidate its proof on ANY failure, including during close().
 */
export async function startRpcTap({ listenPath, targetPath, onEvent, maxMessageBytes = 4 * 1024 * 1024 }) {
  if (process.platform !== 'linux' || typeof listenPath !== 'string' || !isAbsolute(listenPath) ||
    typeof targetPath !== 'string' || !isAbsolute(targetPath) || listenPath === targetPath ||
    typeof onEvent !== 'function' || !Number.isSafeInteger(maxMessageBytes) || maxMessageBytes <= 0 ||
    maxMessageBytes > bufferConstants.MAX_LENGTH) throw new Error('invalid_tap_options');

  let seq = 0, nextConnection = 0, closing = false, closePromise, sinkFailed = false;
  const connections = new Set();
  function emit(connectionId, client, fields) {
    if (sinkFailed) return;
    try { onEvent({ seq: ++seq, at: Date.now(), connectionId, client, ...fields }); }
    catch {
      sinkFailed = true;
      // Never expose an exception supplied by the event consumer.
      try { onEvent({ seq: ++seq, at: Date.now(), connectionId, client, kind: 'failure', reason: 'event_sink_failed' }); } catch {}
      for (const c of connections) c.destroy();
    }
  }

  let parent, privateHandle, privateDir, privateIdentity, ownedSocket, server, published = false;
  const accept = clientSocket => {
    const connectionId = ++nextConnection;
    let client = 'unknown', initialized = false, failed = false;
    const pending = new Map(), handshake = {};
    let upstream, clientParser, serverParser;
    const connection = {
      destroy() { clientSocket.destroy(); upstream?.destroy(); },
      stop() {
        if (!failed && (pending.size || clientParser.incomplete() || serverParser.incomplete())) fail('incomplete_trace');
        clientParser.dispose(); serverParser.dispose(); pending.clear();
        this.destroy();
      },
    };
    function fail(reason) {
      if (failed) return;
      failed = true;
      pending.clear();
      clientParser?.dispose(); serverParser?.dispose();
      emit(connectionId, client, { kind: 'failure', reason });
    }
    function responseShape(m) {
      const result = Object.hasOwn(m, 'result'), error = Object.hasOwn(m, 'error');
      requireTrace(rpcId(m.id) && result !== error);
      if (error) requireTrace(record(m.error) && Number.isSafeInteger(m.error.code) && typeof m.error.message === 'string');
      return result;
    }
    function message(m, fromClient) {
      requireTrace(record(m) && (!Object.hasOwn(m, 'jsonrpc') || m.jsonrpc === '2.0'));
      if (Object.hasOwn(m, 'method')) {
        requireTrace(typeof m.method === 'string' && m.method.length > 0 &&
          !Object.hasOwn(m, 'result') && !Object.hasOwn(m, 'error'));
        if (fromClient && ['initialize', 'turn/steer'].includes(m.method)) requireTrace(Object.hasOwn(m, 'id'));
        if (Object.hasOwn(m, 'id')) requireTrace(rpcId(m.id));
        if (fromClient && Object.hasOwn(m, 'id')) {
          requireTrace(!pending.has(m.id) && pending.size < MAX_PENDING, 'request_tracking_lost');
          let identity = null;
          if (m.method === 'initialize') {
            requireTrace(!initialized && record(m.params) && record(m.params.clientInfo) &&
              typeof m.params.clientInfo.name === 'string' && m.params.clientInfo.name.length > 0, 'invalid_initialize');
            // Retain only classification, never the arbitrary initialize name.
            client = m.params.clientInfo.name === 'vecnet' ? 'vecnet' : 'observer';
            initialized = true;
            identity = { initialize: true };
          } else if (m.method === 'turn/steer') {
            requireTrace(initialized, 'uninitialized_client');
            const p = m.params;
            requireTrace(record(p) && token(p.threadId) && token(p.expectedTurnId) && token(p.clientUserMessageId));
            identity = { id: m.id, method: 'turn/steer', threadId: p.threadId,
              expectedTurnId: p.expectedTurnId, receiptKey: p.clientUserMessageId };
            emit(connectionId, client, { kind: 'request', ...identity });
          }
          pending.set(m.id, identity);
        } else if (!fromClient && !Object.hasOwn(m, 'id') && m.method === 'turn/completed') {
          const p = m.params;
          requireTrace(record(p));
          const turnId = record(p.turn) ? p.turn.id : p.turnId;
          requireTrace(token(p.threadId) && token(turnId));
          emit(connectionId, client, { kind: 'completed', threadId: p.threadId, turnId });
        }
        return;
      }
      const ok = responseShape(m);
      if (fromClient) return; // Response to a server-originated request.
      requireTrace(pending.has(m.id), 'unmatched_response');
      const identity = pending.get(m.id);
      pending.delete(m.id);
      if (identity?.initialize) { requireTrace(ok, 'initialization_rejected'); return; }
      if (!identity) return;
      let turnId;
      if (ok) {
        requireTrace(record(m.result));
        const direct = m.result.turnId;
        const nested = record(m.result.turn) ? m.result.turn.id : undefined;
        if (direct !== undefined) requireTrace(token(direct));
        if (nested !== undefined) requireTrace(token(nested));
        requireTrace(direct === undefined || nested === undefined || direct === nested);
        turnId = direct ?? nested;
      }
      emit(connectionId, client, { kind: 'response', ...identity, ok, ...(turnId === undefined ? {} : { turnId }) });
      requireTrace(ok, 'request_rejected');
    }
    clientParser = parser(true, maxMessageBytes, handshake, message);
    serverParser = parser(false, maxMessageBytes, handshake, message);
    function observe(p, chunk) {
      if (failed || closing || sinkFailed) return;
      try { p.feed(chunk); } catch (error) { fail(error instanceof ParseFailure ? error.message : 'parser_failure'); }
    }
    function transportError() { if (!closing) fail('transport_error'); connection.destroy(); }
    function transportClosed() {
      if (!closing) fail('transport_closed');
      connection.destroy();
    }
    connections.add(connection);
    clientSocket.on('error', transportError);
    clientSocket.on('close', transportClosed);
    try { upstream = net.createConnection(targetPath); } catch { transportError(); return; }
    let openSockets = 2;
    for (const socket of [clientSocket, upstream]) socket.on('close', () => {
      if (--openSockets === 0) { connections.delete(connection); pending.clear(); clientParser.dispose(); serverParser.dispose(); }
    });
    upstream.on('error', transportError);
    upstream.on('close', transportClosed);
    clientSocket.on('data', chunk => observe(clientParser, chunk));
    upstream.on('data', chunk => observe(serverParser, chunk));
    clientSocket.pipe(upstream);
    upstream.pipe(clientSocket);
    if (closing || sinkFailed) connection.destroy();
  };

  async function removeOwned(path) {
    const current = await statOrMissing(path);
    if (!current) return true;
    if (current.isSocket() && sameInode(current, ownedSocket)) { await unlink(path); return true; }
    emit(0, 'unknown', { kind: 'failure', reason: 'listener_replaced' });
    return false;
  }
  async function cleanup() {
    let cleanupFailed = false;
    const attempt = async action => { try { await action(); } catch { cleanupFailed = true; } };
    // This transferred handle has no bound filename: close cannot auto-unlink
    // any private/public replacement. All namespace cleanup remains below.
    if (server?.listening) await attempt(() => new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve())));
    if (published) await attempt(() => removeOwned(`/proc/self/fd/${parent.fd}/${basename(listenPath)}`));
    if (ownedSocket && privateHandle) await attempt(async () => {
      if (!await removeOwned(`/proc/self/fd/${privateHandle.fd}/socket`)) cleanupFailed = true;
    });
    if (privateDir) await attempt(async () => {
      const current = await statOrMissing(privateDir);
      // Do not guess a new pathname after a rename. Retain the moved owned
      // directory and the replacement; closing descriptors still proceeds.
      if (!current?.isDirectory() || !sameInode(current, privateIdentity)) {
        cleanupFailed = true;
        return;
      }
      await rmdir(privateDir); // A retained replacement inside it yields ENOTEMPTY.
    });
    if (privateHandle) await attempt(() => privateHandle.close());
    if (parent) await attempt(() => parent.close());
    if (cleanupFailed || sinkFailed) {
      emit(0, 'unknown', { kind: 'failure', reason: 'cleanup_error' });
      throw new Error('tap_cleanup_failed');
    }
  }
  function close() {
    if (!closePromise) {
      closing = true;
      for (const c of connections) c.stop();
      closePromise = cleanup();
    }
    return closePromise;
  }
  try {
    const parentPath = dirname(listenPath), expectedParent = await lstat(parentPath);
    if (!expectedParent.isDirectory() || expectedParent.uid !== process.geteuid() ||
      (expectedParent.mode & 0o7777) !== 0o700) throw new Error('unsafe_tap_parent');
    parent = await open(parentPath, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
    if (!sameInode(expectedParent, await parent.stat())) throw new Error('unsafe_tap_parent');
    const anchored = `/proc/self/fd/${parent.fd}`;
    if (await statOrMissing(`${anchored}/${basename(listenPath)}`)) throw new Error('tap_path_exists');
    privateDir = await mkdtemp(`${anchored}/.rpc-tap-`);
    privateHandle = await open(privateDir, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
    privateIdentity = await privateHandle.stat();
    // The helper must resolve the caller's pinned directory, not its own FD.
    const socketPath = `/proc/${process.pid}/fd/${privateHandle.fd}/socket`;
    server = await bindListener(socketPath);
    server.on('connection', accept);
    server.on('error', () => { if (published && !closing) emit(0, 'unknown', { kind: 'failure', reason: 'listener_error' }); });
    ownedSocket = await lstat(socketPath);
    if (!ownedSocket.isSocket()) throw new Error('tap_bind_failed');
    await link(socketPath, `${anchored}/${basename(listenPath)}`); // Atomic EEXIST; never overwrite.
    published = true;
    return { close };
  } catch {
    await close();
    throw new Error('tap_setup_failed');
  }
}
