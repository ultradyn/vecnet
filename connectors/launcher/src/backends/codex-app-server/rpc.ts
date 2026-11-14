import type { Logger as Log } from '../../backend.ts';
import { connectUnixWebSocket } from './ws.ts';
import { socketPathFor, verifySocket, parseServerVersion, CODEX_APPSERVER_VERSIONS } from './discover.ts';
type AllowedMethod = string;
const CODEX_OPT_OUT_NOTIFICATIONS = ['item/agentMessage/delta','item/reasoning/textDelta','item/reasoning/summaryTextDelta','item/commandExecution/outputDelta','turn/tokenUsage/updated'];

export interface CodexRpc {
  version: string;
  call<T>(method: AllowedMethod, params: unknown, timeoutMs: number): Promise<T>;
  onNotification(fn: (method: string, params: unknown) => void): void;
  onClose(fn: (why: string) => void): void;
  onRequest(fn: (id: number | string, method: string, params: unknown) => void): void;
  respond(id: number | string, result: unknown): void;
  close(): void;
}
export class CodexRpcError extends Error {
  code: number;
  ambiguous: boolean;
  /** Discovery failure detail; no socket path or server-supplied body. */
  reason?: string;
  constructor(code: number, message: string, ambiguous = false) {
    super(message); this.name = 'CodexRpcError'; this.code = code; this.ambiguous = ambiguous;
  }
}
const allowed: ReadonlySet<string> = new Set<AllowedMethod>([
  'initialize', 'thread/start', 'thread/name/set', 'thread/list', 'thread/read', 'thread/turns/list', 'thread/resume', 'thread/unsubscribe', 'thread/archive', 'turn/start', 'turn/steer', 'turn/interrupt',
]);
type Pending = { resolve(value: unknown): void; reject(error: CodexRpcError): void; timer: ReturnType<typeof setTimeout> };

export async function openCodexRpc(o: { codexHome: string; euid: number; log: Log; timeoutMs: number }): Promise<CodexRpc> {
  const log = o.log;
  const checked = verifySocket(socketPathFor(o.codexHome), o.euid);
  if (!checked.ok) {
    const error = new CodexRpcError(-1, 'unreachable'); error.reason = checked.reason; throw error;
  }
  if (!Number.isFinite(o.timeoutMs) || o.timeoutMs <= 0) throw new RangeError('invalid RPC timeout');
  let ws;
  try { ws = await connectUnixWebSocket(checked.target, { maxMessageBytes: 4 * 1024 * 1024, timeoutMs: o.timeoutMs }); }
  catch { throw new CodexRpcError(-1, 'unreachable'); }
  const pending = new Map<number, Pending>();
  const requests: ((id: number | string, method: string, params: unknown) => void)[] = [];
  const notifications: ((method: string, params: unknown) => void)[] = [], closers: ((why: string) => void)[] = [];
  let nextId = 0, ended: string | undefined;
  function finish(why: string) {
    if (ended !== undefined) return;
    ended = why;
    for (const p of pending.values()) { clearTimeout(p.timer); p.reject(new CodexRpcError(-1, 'connection_closed', true)); }
    pending.clear();
    for (const fn of closers) fn(why);
  }
  ws.onMessage(text => {
    let value: unknown;
    try { value = JSON.parse(text); } catch { finish('invalid_json'); ws.close(); return; }
    if (!value || typeof value !== 'object' || Array.isArray(value)) { finish('invalid_rpc'); ws.close(); return; }
    const m = value as Record<string, unknown>;
    if (typeof m.method === 'string') {
      if (Object.hasOwn(m, 'id')) {
        if (typeof m.id === 'number' || typeof m.id === 'string') for (const fn of requests) fn(m.id, m.method, m.params);
        return;
      }
      for (const fn of notifications) fn(m.method, m.params);
      return;
    }
    if (typeof m.id !== 'number') return;
    const p = pending.get(m.id); if (!p) return;
    const hasResult = Object.hasOwn(m, 'result'), hasError = Object.hasOwn(m, 'error');
    if (hasResult === hasError) { finish('invalid_rpc'); ws.close(); return; }
    if (hasError) {
      const error = m.error as Record<string, unknown> | null;
      if (!error || typeof error.code !== 'number' || typeof error.message !== 'string') {
        finish('invalid_rpc'); ws.close(); return;
      }
      pending.delete(m.id); clearTimeout(p.timer);
      p.reject(new CodexRpcError(error.code, error.message));
    } else { pending.delete(m.id); clearTimeout(p.timer); p.resolve(m.result); }
  });
  ws.onClose(() => finish('connection_closed'));
  const rpc: CodexRpc = {
    version: '',
    call<T>(method: AllowedMethod, params: unknown, timeoutMs: number): Promise<T> {
      if (!allowed.has(method)) return Promise.reject(new CodexRpcError(-1, 'forbidden_method'));
      if (ended !== undefined) return Promise.reject(new CodexRpcError(-1, 'connection_closed'));
      if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) return Promise.reject(new RangeError('invalid RPC timeout'));
      let text: string;
      const id = ++nextId;
      try { text = JSON.stringify({ id, method, params }); }
      catch { return Promise.reject(new CodexRpcError(-1, 'invalid_params')); }
      return new Promise<T>((resolve, reject) => {
        const timer = setTimeout(() => {
          pending.delete(id);
          // A sent request may have been accepted even if its response is late.
          reject(new CodexRpcError(-1, 'response_timeout', true));
        }, timeoutMs);
        pending.set(id, { resolve: value => resolve(value as T), reject, timer });
        try { ws.send(text); }
        catch {
          clearTimeout(timer); pending.delete(id);
          reject(new CodexRpcError(-1, 'connection_closed', true));
          finish('connection_closed'); ws.close();
        }
      });
    },
    onRequest(fn) { requests.push(fn); },
    respond(id, result) { ws.send(JSON.stringify({ id, result })); },
    onNotification(fn) { notifications.push(fn); },
    onClose(fn) { if (ended !== undefined) fn(ended); else closers.push(fn); },
    close() { finish('local_close'); ws.close(); },
  };
  try {
    const initialized = await rpc.call<unknown>('initialize', {
      clientInfo: { name: 'vecnet-launcher', title: 'vecnet launcher', version: '0.1.0' },
      capabilities: { experimentalApi: true, optOutNotificationMethods: CODEX_OPT_OUT_NOTIFICATIONS },
    }, o.timeoutMs);
    const ua = initialized && typeof initialized === 'object' ? (initialized as Record<string, unknown>).userAgent : undefined;
    const version = typeof ua === 'string' ? parseServerVersion(ua) : null;
    if (version === null || !CODEX_APPSERVER_VERSIONS.includes(version)) {
      log.error('refused_version'); throw new CodexRpcError(-1, 'refused_version');
    }
    rpc.version = version;
    ws.send(JSON.stringify({ method: 'initialized' }));
    return rpc;
  } catch (error) { rpc.close(); throw error; }
}
