import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import type { ChildProcessWithoutNullStreams } from 'node:child_process';
import { StringDecoder } from 'node:string_decoder';
import { commandInvocation, terminateProcess } from '../../backend.ts';
import type { PluginContext } from '../../backend.ts';

export type RpcId = string | number;
export type ObjectValue = Record<string, unknown>;
export function object(value: unknown): ObjectValue {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as ObjectValue : {};
}
const MAX_FRAME = 4 * 1024 * 1024;
const MAX_INCOMING_IDS = 4096;
interface Pending { resolve(value: unknown): void; reject(error: Error): void; cleanup(): void }

/** ACP v1 stdio: UTF-8 JSON-RPC 2.0, one JSON object per newline. No shell,
 * stdout logging, SDK, transport retry, or process respawn. */
export class AcpRpc {
  private readonly child: ChildProcessWithoutNullStreams;
  private readonly ctx: PluginContext;
  private readonly pending = new Map<number, Pending>();
  private readonly incoming = new Set<RpcId>();
  // Keep completed identities for the entire connection. Never evict/reuse an
  // approval identity: close on exhaustion. Fixed-size digests bound storage
  // even when an agent supplies a frame-sized string id.
  private readonly seenIncoming = new Set<string>();
  private nextId = 1;
  private dead = false;
  private exited = false;
  private buffer = '';
  private readonly decoder = new StringDecoder('utf8');
  private readonly closed: Promise<void>;
  private stopping?: Promise<void>;
  onNotification: (method: string, params: unknown) => void = () => {};
  onRequest: (method: string, params: unknown, id: RpcId) => Promise<unknown> = async () => { throw new Error('unsupported'); };
  onLost: () => void = () => {};
  onExit: (code: number | null) => void = () => {};

  constructor(argv: string[], cwd: string, env: Record<string, string>, ctx: PluginContext) {
    this.ctx = ctx;
    const command = commandInvocation(argv, env, cwd);
    this.child = spawn(command.file, command.args, { ...command, windowsHide: true, cwd, env, detached: process.platform !== 'win32', stdio: ['pipe', 'pipe', 'pipe'] });
    this.closed = new Promise(resolve => {
      this.child.once('close', code => {
        this.exited = true; this.fail();
        try { this.onExit(code); } catch { /* Journal failure must not lose exit confirmation. */ }
        resolve();
      });
    });
    this.child.once('error', () => this.fail());
    this.child.once('exit', () => this.fail());
    this.child.stdin.on('error', () => this.fail());
    // Agent stderr can contain prompts, tool output and authentication details.
    this.child.stderr.resume();
    this.child.stdout.on('data', (chunk: Buffer) => this.read(this.decoder.write(chunk)));
    this.child.stdout.once('end', () => { this.read(this.decoder.end()); this.fail(); });
  }
  get alive(): boolean { return !this.dead && !this.exited; }
  private kill(signal: NodeJS.Signals): void {
    if (this.exited) return;
    terminateProcess(this.child, signal, true);
  }
  private fail(): void {
    if (this.dead) return;
    this.dead = true;
    for (const p of this.pending.values()) { p.cleanup(); p.reject(new Error('acp_connection_lost: request outcome may be unknown')); }
    this.pending.clear(); this.incoming.clear(); this.seenIncoming.clear();
    try { this.onLost(); } catch { /* A full ledger must not prevent process teardown. */ }
    // A broken pipe must not leave an uncontrolled agent running.
    this.kill('SIGKILL');
  }
  private read(chunk: string): void {
    if (this.dead) return;
    this.buffer += chunk;
    let end: number;
    while ((end = this.buffer.indexOf('\n')) >= 0) {
      const line = this.buffer.slice(0, end); this.buffer = this.buffer.slice(end + 1);
      if (Buffer.byteLength(line) > MAX_FRAME || !line.trim()) { this.fail(); return; }
      try { this.message(JSON.parse(line)); } catch { this.fail(); return; }
      if (this.dead) return;
    }
    if (Buffer.byteLength(this.buffer) > MAX_FRAME) this.fail();
  }
  private message(value: unknown): void {
    const m = object(value);
    if (m.jsonrpc !== '2.0') throw new Error('invalid_rpc');
    if (typeof m.method === 'string') {
      if (!Object.hasOwn(m, 'id')) { this.onNotification(m.method, m.params); return; }
      if (!(typeof m.id === 'string' || typeof m.id === 'number' && Number.isSafeInteger(m.id))) throw new Error('invalid_id');
      const id = m.id;
      const identity = createHash('sha256').update(JSON.stringify([typeof id, id])).digest('hex');
      if (this.seenIncoming.has(identity)) throw new Error('reused_request_identity');
      if (this.seenIncoming.size >= MAX_INCOMING_IDS || this.incoming.size >= 128) throw new Error('too_many_requests');
      this.seenIncoming.add(identity);
      this.incoming.add(id);
      void Promise.resolve().then(() => this.onRequest(m.method as string, m.params, id)).then(
        result => { if (this.alive) this.write({ jsonrpc: '2.0', id, result }); },
        () => { if (this.alive) this.write({ jsonrpc: '2.0', id, error: { code: -32601, message: 'Unsupported or invalid ACP client request' } }); },
      ).catch(() => this.fail()).finally(() => this.incoming.delete(id));
      return;
    }
    if (typeof m.id !== 'number') throw new Error('invalid_response_id');
    const p = this.pending.get(m.id);
    if (!p) throw new Error('unknown_response');
    if (Object.hasOwn(m, 'result') === Object.hasOwn(m, 'error')) throw new Error('invalid_response');
    this.pending.delete(m.id); p.cleanup();
    if (Object.hasOwn(m, 'error')) p.reject(new Error('acp_request_rejected')); // Never echo agent error data.
    else p.resolve(m.result);
  }
  private write(message: unknown): void {
    if (!this.alive) throw new Error('acp_connection_lost');
    const data = JSON.stringify(message) + '\n';
    if (Buffer.byteLength(data) > MAX_FRAME || this.child.stdin.writableLength > MAX_FRAME * 2) {
      this.fail(); throw new Error('acp_transport_limit');
    }
    try { this.child.stdin.write(data); } catch { this.fail(); throw new Error('acp_write_unknown'); }
  }
  notify(method: string, params: unknown): void { this.write({ jsonrpc: '2.0', method, params }); }
  call(method: string, params: unknown, timeoutMs?: number, signal?: AbortSignal): Promise<unknown> {
    if (!this.alive || signal?.aborted) return Promise.reject(new Error('acp_unavailable_or_aborted'));
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      const timer = new AbortController();
      const abort = () => this.fail(); // Ambiguous I/O cannot safely be retried.
      const cleanup = () => { timer.abort(); signal?.removeEventListener('abort', abort); };
      this.pending.set(id, { resolve, reject, cleanup });
      signal?.addEventListener('abort', abort, { once: true });
      if (timeoutMs !== undefined) void this.ctx.sleep(timeoutMs, timer.signal).then(() => this.fail(), () => {});
      try { this.write({ jsonrpc: '2.0', id, method, params }); }
      catch { this.fail(); }
    });
  }
  stop(): Promise<void> {
    if (this.exited) return Promise.resolve();
    return this.stopping ??= this.stopOnce().catch(error => { this.stopping = undefined; throw error; });
  }
  private async stopOnce(): Promise<void> {
    if (this.exited) return;
    this.kill('SIGTERM');
    const timer = new AbortController();
    try {
      await Promise.race([this.closed, this.ctx.sleep(3000, timer.signal).then(() => this.kill('SIGKILL'))]);
    } finally { timer.abort(); }
    if (!this.exited) {
      const deadline = new AbortController();
      try {
        await Promise.race([this.closed, this.ctx.sleep(3000, deadline.signal).then(() => { throw new Error('acp_process_exit_unconfirmed'); })]);
      } finally { deadline.abort(); }
    }
  }
}
