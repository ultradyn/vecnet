import { spawn, spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { closeSync, constants, lstatSync, openSync, readFileSync, renameSync, unlinkSync, writeFileSync } from 'node:fs';
import { cliEnvironment, commandInvocation, terminateProcess } from '../../../hub-client/src/platform.ts';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import { CODEX_RELOAD_MS, codexAuthorityLocation } from '../codex/connector.ts';
import type { HostedAdapter } from './host.ts';

/** One passively registered Claude root as the private CLI child reports it.
 * Only opaque coordinates: a binding key, its generation, a passive state and
 * a fixed reason. Never a socket path, config content or credential. */
export type ClaudeBindingState = 'registered' | 'unsupported' | 'opted_out' | 'blocked';
/** Native-delivery admission gate (task 7): process liveness, durable policy
 * consent, the build check (parked: version_qualified holds) and the
 * per-profile canary opt-in (Phase 2 task 3) — the sole durable gate on a
 * native write. Booleans only; a snapshot row without `canary` is refused. */
export interface ClaudeSnapshotAdmission { live: boolean; policy: boolean; version_qualified: boolean; canary: boolean }
/** The coordinates the shared host needs to open its own scoped CLI children
 * for this recipient. Hub coordinates and profile name only: no config paths,
 * no socket paths, no session ids. */
export interface ClaudeSnapshotScope { profile: string; as?: string; hub_endpoint: string; caller: string; recipient: string }
export interface ClaudeSnapshotBinding {
  binding_key: string; generation: string; state: ClaudeBindingState; reason?: string;
  admission?: ClaudeSnapshotAdmission; scope?: ClaudeSnapshotScope;
}
export interface ClaudeSnapshot { version: 1; bindings: ClaudeSnapshotBinding[] }

/** One sidecar row: the passive fields plus, in v2 only, `native: 'canary'`
 * when this host's delivery adapter admitted that exact key at that
 * generation. No other native value exists. */
export interface ClaudeStatusRow {
  binding_key: string; generation: string; state: ClaudeBindingState; reason?: string; native?: 'canary';
}

/** The host status sidecar (bindings.json.claude-status.json next to the
 * existing Codex status). host_start/boot_id use the same Linux identity
 * format as the CLI's /proc checks, so a reused pid never validates. */
export interface ClaudeStatusFile {
  version: 2;
  updated_at: string;
  host_pid: number;
  host_start: string;
  boot_id: string;
  bindings: ClaudeStatusRow[];
}

/** Passive Claude status hosted by the elected delivery host. It never elects
 * a lease of its own: it publishes only while the shared singleton is held,
 * revalidating that evidence before every heartbeat. */
export type ClaudeStatus = HostedAdapter;

const KEY = /^[0-9a-f]{64}$/, GENERATION = /^[0-9a-f]{32}$/, BOOT = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/, START = /^[0-9]{1,32}$/;
const STATES: readonly ClaudeBindingState[] = ['registered', 'unsupported', 'opted_out', 'blocked'];
const MAX_BINDINGS = 16, MAX_BYTES = 1_048_576;

/** Field 22 of /proc/<pid>/stat, the same start identity the CLI reads. */
function procStart(pid: number): string | undefined {
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, 'utf8');
    const fields = raw.slice(raw.lastIndexOf(')') + 2).split(' ');
    const start = fields[19];
    return START.test(start ?? '') ? start : undefined;
  } catch { return; }
}

function hostIdentity(): { pid: number; start: string; boot: string } | undefined {
  if (process.platform !== 'linux') return;
  try {
    const boot = readFileSync('/proc/sys/kernel/random/boot_id', 'utf8').trim();
    const start = procStart(process.pid);
    if (!BOOT.test(boot) || start === undefined) return;
    return { pid: process.pid, start, boot };
  } catch { return; }
}

function validAdmission(input: unknown): ClaudeSnapshotAdmission | undefined {
  if (input === null || typeof input !== 'object') return;
  const a = input as Record<string, unknown>;
  if (typeof a.live !== 'boolean' || typeof a.policy !== 'boolean' || typeof a.version_qualified !== 'boolean' ||
    typeof a.canary !== 'boolean') return; // An older CLI without the canary flag is refused: fail closed.
  return { live: a.live, policy: a.policy, version_qualified: a.version_qualified, canary: a.canary };
}

function validScope(input: unknown): ClaudeSnapshotScope | undefined {
  if (input === null || typeof input !== 'object') return;
  const s = input as Record<string, unknown>;
  if (typeof s.profile !== 'string' || s.profile.length < 1 || s.profile.length > 128) return;
  if (s.as !== undefined && (typeof s.as !== 'string' || s.as.length > 128)) return;
  for (const field of ['hub_endpoint', 'caller', 'recipient'] as const) {
    if (typeof s[field] !== 'string' || s[field]!.length < 1 || s[field]!.length > 256) return;
  }
  return s.as === undefined
    ? { profile: s.profile, hub_endpoint: s.hub_endpoint as string, caller: s.caller as string, recipient: s.recipient as string }
    : { profile: s.profile, as: s.as, hub_endpoint: s.hub_endpoint as string, caller: s.caller as string, recipient: s.recipient as string };
}

function validSnapshot(input: unknown): ClaudeSnapshot | undefined {
  if (input === null || typeof input !== 'object') return;
  const s = input as Record<string, unknown>;
  if (s.version !== 1 || !Array.isArray(s.bindings) || s.bindings.length > MAX_BINDINGS) return;
  const bindings: ClaudeSnapshotBinding[] = [];
  for (const row of s.bindings) {
    if (row === null || typeof row !== 'object') return;
    const b = row as Record<string, unknown>;
    if (typeof b.binding_key !== 'string' || !KEY.test(b.binding_key)) return;
    if (typeof b.generation !== 'string' || !GENERATION.test(b.generation)) return;
    if (!STATES.includes(b.state as ClaudeBindingState)) return;
    if (b.reason !== undefined && (typeof b.reason !== 'string' || b.reason.length > 256)) return;
    const admission = b.admission === undefined ? undefined : validAdmission(b.admission);
    if (b.admission !== undefined && admission === undefined) return;
    const scope = b.scope === undefined ? undefined : validScope(b.scope);
    if (b.scope !== undefined && scope === undefined) return;
    // Admission and scope coordinates stay in the adapter's snapshot; the
    // status sidecar below republishes only the whitelisted passive fields.
    const binding: ClaudeSnapshotBinding = b.reason === undefined
      ? { binding_key: b.binding_key, generation: b.generation, state: b.state as ClaudeBindingState }
      : { binding_key: b.binding_key, generation: b.generation, state: b.state as ClaudeBindingState, reason: b.reason };
    if (admission) binding.admission = admission;
    if (scope) binding.scope = scope;
    bindings.push(binding);
  }
  return { version: 1, bindings };
}

/** Evidence that the shared delivery singleton is currently held, gathered
 * without ever creating or unlinking the lock: a fresh descriptor must fail
 * flock. A free lock means no host is elected, so no heartbeat may claim one. */
function singletonHeld(bindingsPath: string): boolean {
  if (process.platform !== 'linux') return false;
  const lock = bindingsPath + '.status.json' + '.lock';
  let fd: number | undefined;
  try {
    fd = openSync(lock, constants.O_RDONLY | constants.O_NOFOLLOW);
    const probe = spawnSync('flock', ['--exclusive', '--nonblock', '3'], { stdio: ['ignore', 'ignore', 'ignore', fd], timeout: 2000 });
    if (probe.error) return false; // Cannot prove the singleton; fail closed.
    return probe.status !== 0; // Occupied (possibly by this host) = held.
  } catch {
    return false; // No lock file: no host ever elected one here.
  } finally {
    if (fd !== undefined) closeSync(fd); // Releases a probe that acquired.
  }
}

/** Run the snapshot fetch under a clock-bounded timeout; failures are
 * undefined, never thrown and never logged with child output. */
function bounded<T>(p: Promise<T>, ms: number, clock: Clock): Promise<T | undefined> {
  return new Promise(resolve => {
    const timer = clock.timer(ms, () => resolve(undefined));
    p.then(value => { timer.cancel(); resolve(value); }, () => { timer.cancel(); resolve(undefined); });
  });
}

export function createClaudeStatus(o: {
  env: NodeJS.ProcessEnv;
  clock: Clock;
  log: Log;
  fetchSnapshot: () => Promise<ClaudeSnapshot | undefined>;
  /** The delivery adapter's current admissions (binding key -> generation).
   * Absent when no adapter runs (kill switch), so no row is ever marked. */
  nativeAdmissions?: () => ReadonlyMap<string, string>;
}): ClaudeStatus {
  const { clock, log } = o;
  // Keep the configuration used by the elected host for this adapter's lifetime.
  const env = { ...o.env };
  const location = codexAuthorityLocation(env);
  const identity = hostIdentity();
  const sidecar = location ? location.bindings + '.claude-status.json' : undefined;
  let stopped = false, timer: { cancel(): void } | undefined, warnedLease = false, warnedIdentity = false;
  let owned: { dev: number; ino: number } | undefined;
  let chain: Promise<void> = Promise.resolve();

  function heartbeat(snapshot: ClaudeSnapshot) {
    if (stopped || sidecar === undefined || identity === undefined) return;
    // Revalidate lease ownership before publishing this host's heartbeat.
    if (!singletonHeld(location!.bindings)) {
      if (!warnedLease) { warnedLease = true; log.warn('claude_status_lease_not_held'); }
      return;
    }
    warnedLease = false;
    let native: ReadonlyMap<string, string>;
    try { native = o.nativeAdmissions?.() ?? new Map(); }
    catch { native = new Map(); } // Unknown admission state marks nothing.
    const data = JSON.stringify({
      version: 2, updated_at: new Date(clock.now()).toISOString(),
      host_pid: identity.pid, host_start: identity.start, boot_id: identity.boot,
      // The sidecar is a strict status document read by a strict decoder:
      // admission and scope coordinates never leak into it. A row is marked
      // native only for the exact key and generation the adapter admitted.
      bindings: snapshot.bindings.map(({ admission: _admission, scope: _scope, ...binding }): ClaudeStatusRow =>
        native.get(binding.binding_key) === binding.generation ? { ...binding, native: 'canary' } : binding),
    } satisfies ClaudeStatusFile);
    if (Buffer.byteLength(data) > MAX_BYTES) { log.warn('claude_status_too_large'); return; }
    const temp = sidecar + '.' + randomUUID() + '.tmp';
    try {
      writeFileSync(temp, data + '\n', { mode: 0o600, flag: 'wx' });
      renameSync(temp, sidecar);
    } catch {
      log.warn('claude_status_write_failed');
      try { unlinkSync(temp); } catch { /* never published */ }
      return;
    }
    try { owned = lstatSync(sidecar); } catch { owned = undefined; }
  }

  async function refresh() {
    if (stopped) return;
    if (sidecar === undefined || identity === undefined) {
      // Unqualified platform or unlocatable authority: nothing is ever claimed.
      if (!warnedIdentity) { warnedIdentity = true; log.info('claude_status_unavailable'); }
      return;
    }
    const snapshot = validSnapshot(await bounded(o.fetchSnapshot(), 5000, clock));
    if (stopped) return;
    if (!snapshot) { log.warn('claude_snapshot_unavailable'); return; }
    heartbeat(snapshot);
  }

  function tick() {
    timer = undefined;
    chain = chain
      .then(refresh)
      .catch(() => { log.warn('claude_status_refresh_failed'); })
      .finally(() => { if (!stopped && timer === undefined) timer = clock.timer(CODEX_RELOAD_MS, tick); });
  }

  return {
    start() {
      if (stopped || timer !== undefined) return;
      tick();
    },
    stop(): Promise<void> {
      if (stopped) return chain;
      stopped = true;
      timer?.cancel();
      timer = undefined;
      return chain.then(() => {
        // Remove exactly the file this adapter last published; another host's
        // sidecar (or a crash leftover it never owned) is never touched.
        if (sidecar === undefined || owned === undefined) return;
        try {
          const current = lstatSync(sidecar);
          if (current.dev === owned.dev && current.ino === owned.ino) unlinkSync(sidecar);
        } catch { /* already gone */ }
        owned = undefined;
      });
    },
  };
}

/** The elected host's snapshot source: one bounded private CLI child. Neither
 * the command line nor any child output is ever logged. */
export function spawnClaudeSnapshot(sourceEnv: NodeJS.ProcessEnv): () => Promise<ClaudeSnapshot | undefined> {
  const clean = Object.fromEntries(Object.entries(sourceEnv).filter((e): e is [string, string] => typeof e[1] === 'string'));
  const env = cliEnvironment(clean);
  const bin = env.VECNET_BIN || 'vecnet';
  return () => new Promise(resolve => {
    let command;
    try { command = commandInvocation([bin, 'claude', 'runtime-snapshot'], env); }
    catch { resolve(undefined); return; }
    const child = spawn(command.file, command.args, { ...command, windowsHide: true, env, stdio: ['ignore', 'pipe', 'ignore'] });
    let out = '', settled = false;
    const kill = () => terminateProcess(child);
    const timeout = setTimeout(kill, 4000);
    const finish = (value: ClaudeSnapshot | undefined) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      resolve(value);
    };
    child.once('error', () => finish(undefined));
    child.stdout.setEncoding('utf8').on('data', (chunk: string) => { if (out.length < 2 * MAX_BYTES) out += chunk; });
    child.once('close', () => {
      try { finish(JSON.parse(out) as ClaudeSnapshot); }
      catch { finish(undefined); }
    });
  });
}
