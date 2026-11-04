import { homedir } from 'node:os';
import { join } from 'node:path';
import type { PluginContext } from '../../backend.ts';
import { parseWait } from './transcript.ts';

export interface PaseoConfig {
  bridge_bin: string;
  workspace_root: string;
  presets: Record<string, { preset: string; service_tier?: string; route?: { base_url: string; key_file?: string } }>;
  wait_s?: number;
}
export interface Agent { id: string; name: string; cwd?: string; status?: string; archived?: boolean; archivedAt?: string | null }
export interface Preset { name: string; provider_model: string; mode: string | null; thinking?: string; description?: string }
export const fullAccessModes = new Set(['full-access', 'bypassPermissions', 'allowAll', 'auto_accept']);

export class Bridge {
  readonly config: PaseoConfig;
  readonly ctx: PluginContext;
  constructor(config: PaseoConfig, ctx: PluginContext) { this.config = config; this.ctx = ctx; }
  async raw(args: string[], options: { signal?: AbortSignal; env?: Record<string, string>; cwd?: string; timeoutMs?: number } = {}) {
    const bin = this.config.bridge_bin.startsWith('~/') ? join(homedir(), this.config.bridge_bin.slice(2)) : this.config.bridge_bin;
    const argv = [bin, '--json', ...args];
    const timeoutMs = options.timeoutMs ?? (['launch', 'doctor', 'settle'].includes(args[0]) ? 300_000 : 90_000);
    try {
      return await this.ctx.exec(options.cwd ? ['env', '-C', options.cwd, ...argv] : argv,
        { timeoutMs, signal: options.signal, env: options.env });
    } catch {
      if (options.signal?.aborted) throw options.signal.reason;
      // Never propagate an exec exception: it can quote argv, including --text.
      throw new Error('paseo_bridge_unreachable');
    }
  }
  async json<T>(args: string[], options: Parameters<Bridge['raw']>[1] = {}): Promise<T> {
    const result = await this.raw(args, options);
    if (result.code !== 0) throw new Error(`paseo_${args[0]}_failed`);
    try { return JSON.parse(result.stdout) as T; } catch { throw new Error('invalid_paseo_response'); }
  }
  async agents(all = false): Promise<Agent[]> {
    const rows = await this.json<Agent[]>(['ls', ...(all ? ['--all'] : [])]);
    if (!Array.isArray(rows) || rows.some(a => !a || typeof a.id !== 'string' || typeof a.name !== 'string')) throw new Error('invalid_paseo_directory');
    return rows;
  }
  async wait(id: string, signal?: AbortSignal, seconds = this.config.wait_s ?? 25) {
    const result = await this.raw(['wait', id, '--timeout', String(seconds)], { timeoutMs: (seconds + 30) * 1000, signal });
    return parseWait(result.stdout, result.stderr, result.code);
  }
  async git(args: string[], signal?: AbortSignal) {
    let result;
    try { result = await this.ctx.exec(['git', ...args], { timeoutMs: 90_000, signal }); }
    catch { throw new Error('paseo_git_unreachable'); }
    if (result.code !== 0) throw new Error('paseo_git_failed');
    return result.stdout.trim();
  }
}
