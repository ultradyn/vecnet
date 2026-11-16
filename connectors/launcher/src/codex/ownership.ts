import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { readBindings } from './connector-authority.ts';
import { realpathSync } from 'node:fs';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import type { Clock } from '../../../hub-client/src/ports.ts';
import type { Binding } from './engine.ts';
import { cliEnvironment, commandInvocation, terminateProcess, userHome } from '../../../hub-client/src/platform.ts';

export type Owner = Pick<Binding, 'profile' | 'as' | 'hub' | 'caller' | 'codexHome' | 'threadId' | 'configPath' | 'generation'>;
export function ownerOf(b: Owner): Owner {
  return { profile: b.profile, as: b.as ?? '', hub: b.hub, caller: b.caller, codexHome: b.codexHome, threadId: b.threadId, configPath: b.configPath, generation: b.generation };
}
export function validOwner(value: unknown): value is Owner {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const r = value as Record<string, unknown>;
  return ['profile', 'hub', 'caller', 'codexHome', 'threadId', 'configPath'].every(k => typeof r[k] === 'string' && r[k] !== '') &&
    typeof r.as === 'string' && typeof r.generation === 'string' && /^[a-f0-9]{32}$/.test(r.generation) &&
    isAbsolute(r.configPath as string) && isAbsolute(r.codexHome as string);
}
export function sameOwner(a: unknown, b: Owner): boolean {
  return validOwner(a) && validOwner(b) && Object.entries(ownerOf(b)).every(([key, value]) => a[key as keyof Owner] === value);
}
/** Check the one machine-local authority before opening a delivery stream.
 * Claim/release/guard still revalidate authenticated identity under Go's lock. */
export function authorityPath(env: Record<string, string>): string | undefined {
  function canonical(path: string): string {
    const absolute = resolve(path);
    try { return realpathSync(absolute); }
    catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'ENOENT' || dirname(absolute) === absolute) throw e;
      return join(canonical(dirname(absolute)), basename(absolute));
    }
  }
  try {
    const home = userHome(env);
    if (!home) return;
    const expected = canonical(join(realpathSync(home), '.local', 'state', 'vecnet', 'codex', 'bindings.json'));
    if (env.VECNET_CODEX_BINDINGS && canonical(env.VECNET_CODEX_BINDINGS) !== expected) return;
    return expected;
  } catch { return; }
}
export function verifyOwner(env: Record<string, string>, owner: Owner, euid: number, now: number): boolean {
  const path = authorityPath(env);
  if (!path || !validOwner(owner)) return false;
  const file = readBindings(path, euid, env);
  return file.state === 'ok' && file.bindings.some(b => sameOwner(ownerOf(b), owner) && b.expiresAt > now);
}
export function ownerArgs(b: Owner): string[] {
  return ['--thread', b.threadId, '--codex-home', b.codexHome, '--expect-generation', b.generation ?? '', '--expect-hub', b.hub, '--expect-caller', b.caller];
}
export interface Guard { finish(outcome: 'accepted' | 'rejected' | 'ambiguous'): Promise<void>; valid(): boolean }

/** The CLI owns the same OS lock as bind/unbind and persists an ambiguity marker.
 * It never inherits session variables or a caller-selected replacement config. */
export function acquireGuard(bin: string, env: Record<string, string>, b: Owner, claim: string, attempt: string, clock?: Pick<Clock, 'timer'>): Promise<Guard> {
  if (!validOwner(b) || !/^[a-f0-9]{32}$/.test(attempt)) return Promise.reject(new Error('ownership_guard_invalid_owner'));
  return new Promise((resolve, reject) => {
    const command = commandInvocation([bin, '--net', b.profile, ...(b.as ? ['--as', b.as] : []), 'codex', 'guard', ...ownerArgs(b), '--claim', claim, '--attempt', attempt], env);
    const child = spawn(command.file, command.args,
      { ...command, windowsHide: true, env: { ...cliEnvironment(env), VECNET_CONFIG: b.configPath ?? '' }, stdio: ['pipe', 'pipe', 'ignore'] });
    let ready = false, live = true, settled = false, exitCode: number | null = null;
    const timer = clock?.timer(32_000, () => terminateProcess(child));
    const timeout = timer ? undefined : setTimeout(() => terminateProcess(child), 32_000);
    const reader = createInterface({ input: child.stdout });
    const closed = new Promise<void>(done => {
      child.once('error', () => { live = false; reject(new Error('ownership_guard_failed')); });
      child.once('close', code => { exitCode = code; live = false; clearTimeout(timeout); timer?.cancel(); reader.close(); if (!ready) reject(new Error('ownership_guard_refused')); done(); });
    });
    child.stdin.on('error', () => {});
    reader.once('line', line => {
      try { if (line.length > 1024 || JSON.parse(line).ready !== true) throw new Error(); }
      catch { terminateProcess(child); reject(new Error('ownership_guard_invalid')); return; }
      ready = true;
      resolve({ valid: () => live && !settled,
        async finish(outcome) {
          if (!settled) { settled = true; child.stdin.end(outcome === 'ambiguous' ? undefined : outcome + '\n'); }
          await closed;
          if (exitCode !== 0) throw new Error('ownership_guard_lost');
        } });
    });
  });
}
