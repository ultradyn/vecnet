import type { PluginContext } from '../../backend.ts';
// Every poll sleep goes through PluginContext.sleep, the daemon's Clock.
export type T3Context = PluginContext;

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

// Machine codes t3-bridge and the T3 server emit. Anything else is reported
// only as the generic failure, never as text.
const KNOWN_CODES = new Set(['runtime_mode_escalation_denied', 'thread_not_found', 'not_found', 'server_down', 'unauthorized',
  'forbidden', 'rate_limited', 'timeout', 'invalid_request', 'invalid_params', 'project_not_found', 'unknown_target',
  'target_required', 'no_token', 'no_full_token', 'token_unreadable', 'live_refused', 'outbox_unwritable', 'escalation_denied',
  'conflict', 'internal_error', 'missing_thread_context', 'ambiguous_thread_context']);
/** A safe description of a nonzero bridge exit: `t3-bridge [HTTP n] [RPC error] [code]`. */
export function bridgeFailure(stderr: string): string {
  // Escalation errors in the real bridge include a preamble; the code alone decides.
  if (/\bruntime_mode_escalation_denied\b/.test(stderr)) return 'runtime_mode_escalation_denied';
  const parts = ['t3-bridge'];
  const http = /\bHTTP (\d{3})\b/.exec(stderr);
  if (http) parts.push(`HTTP ${http[1]}`);
  if (/\bRPC error\b/.test(stderr)) parts.push('RPC error');
  const code = [...stderr.matchAll(/[a-z][a-z0-9]*(?:_[a-z0-9]+)+|[a-z]+/g)].map(m => m[0]).find(w => KNOWN_CODES.has(w));
  parts.push(code ?? 'failed');
  return parts.join(' ');
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
      // Never keep raw stdout/stderr: a diagnostic can quote the request,
      // including prompt prose that no token redaction would catch. Keep only
      // allowlisted machine codes; the exit status travels separately.
      const first = result.stderr.split(/\r?\n/, 1)[0];
      // HTTP/RPC not_found can refer to the endpoint or another resource.
      // Only a thread-specific error proves this session is gone.
      const notFound = /\bthread_not_found\b/.test(first);
      throw new BridgeError(result.code, bridgeFailure(result.stderr), notFound);
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
