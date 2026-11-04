import type { PluginContext } from '../../backend.ts';
import { setTimeout as sleep } from 'node:timers/promises';

// Structural Clock seam: plugins cannot import hub-client, and PluginContext
// currently exposes only now(). Task 17 can supply its shared Clock here.
export interface PollClock { sleep(ms: number, signal?: AbortSignal): Promise<void> }
export interface T3Context extends PluginContext { clock?: PollClock }
export const systemClock: PollClock = {
  sleep: (ms, signal) => sleep(ms, undefined, { signal }),
};

export class BridgeError extends Error {
  readonly exitCode: number;
  readonly code: number;
  readonly unreachable = true;
  readonly notFound: boolean;
  constructor(exitCode: number, line: string, notFound = false) {
    super(line);
    this.name = 'BridgeError';
    this.exitCode = exitCode;
    this.code = exitCode;
    this.notFound = notFound;
  }
}

export class Bridge {
  private readonly bin: string;
  private readonly caller: string;
  private readonly ctx: PluginContext;
  constructor(bin: string, caller: string, ctx: PluginContext) {
    this.bin = bin; this.caller = caller; this.ctx = ctx;
  }
  async exec<T>(args: string[], signal?: AbortSignal, timeoutMs = 30_000): Promise<T> {
    let result: {code: number; stdout: string; stderr: string};
    try { result = await this.ctx.exec([this.bin, '--as', this.caller, ...args], { timeoutMs, signal }); }
    catch {
      // Child-process exceptions often embed the entire argv, including the
      // prompt. Treat transport errors conservatively without preserving cause.
      throw new BridgeError(-1, 't3-bridge execution_failed');
    }
    if (result.code !== 0) {
      const first = result.stderr.split(/\r?\n/, 1)[0];
      // Escalation errors in the real bridge include a preamble; retain just
      // the reason code. Never attach raw stdout/stderr or request bodies.
      const escalation = /\bruntime_mode_escalation_denied\b/.test(result.stderr);
      // HTTP/RPC not_found can refer to the endpoint or another resource.
      // Only a thread-specific error proves this session is gone.
      const notFound = /\bthread_not_found\b/.test(first);
      throw new BridgeError(result.code, escalation ? 'runtime_mode_escalation_denied' : this.ctx.redact(first || 't3-bridge failed'), notFound);
    }
    try { return JSON.parse(result.stdout) as T; }
    catch { throw new Error('t3-bridge invalid_json'); }
  }
  call<T>(tool: string, args: Record<string, unknown>, signal?: AbortSignal, timeoutMs?: number): Promise<T> {
    const json = JSON.stringify(args);
    // Linux limits each argv entry by bytes. Escaping control characters and
    // quotes can double (or sextuple) the input, so check the actual argument.
    if (json.length > 120_000 || Buffer.byteLength(json, 'utf8') > 120_000) throw new Error('input_too_large');
    return this.exec(['call', tool, '--json', json], signal, timeoutMs);
  }
}
