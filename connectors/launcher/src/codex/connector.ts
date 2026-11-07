import { closeSync, constants, fstatSync, openSync, readFileSync, realpathSync } from 'node:fs';
import { dirname, isAbsolute, join } from 'node:path';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { KeyValue } from '../backend.ts';
import { redactingLog } from '../redact.ts';
import { createEngine, vecnetConfigPath } from './engine.ts';
import type { Binding, Engine } from './engine.ts';
import { openCodexRpc } from './rpc.ts';
import type { DeliverySource } from './delivery.ts';

/** Default number of explicit bindings kept loaded per canonical Codex home. */
export const CODEX_MAX_KEPT_LOADED = 8;
/** The bindings file is re-read this often; unbind and expiry take effect within one reload. */
export const CODEX_RELOAD_MS = 10_000;

export type LauncherBinding = Omit<Binding, 'origin'> & { ensureLoaded: () => Promise<boolean> };
export interface CodexConnector {
  stop(): Promise<void>;
  /** For the launcher's own Codex threads (launcher P4). Memory only, never written to the bindings file. */
  addLauncherBinding(b: LauncherBinding): void;
  removeLauncherBinding(threadId: string): void;
}

const threadPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/** The same path as the Go CLI's `codexBindingsPath`, unless VECNET_CODEX_BINDINGS names one. */
export function codexBindingsPath(env: NodeJS.ProcessEnv): string | undefined {
  if (env.VECNET_CODEX_BINDINGS) return env.VECNET_CODEX_BINDINGS;
  const config = vecnetConfigPath(env);
  return config === undefined ? undefined : join(dirname(config), 'codex', 'bindings.json');
}

/** VECNET_CODEX_MAX_BINDINGS, from 1 to 32; anything else is the default, as in `codex bind`. */
export function codexMaxBindings(env: NodeJS.ProcessEnv): number {
  const raw = env.VECNET_CODEX_MAX_BINDINGS ?? '';
  const n = /^[0-9]{1,2}$/.test(raw) ? Number(raw) : NaN;
  return n >= 1 && n <= 32 ? n : CODEX_MAX_KEPT_LOADED;
}

type FileState = { state: 'absent' | 'refused' | 'invalid' | 'unlocatable'; reason?: string } | { state: 'ok'; bindings: Binding[]; skipped: number };

/** Read the bindings file. Refuse anything but a regular file owned by us with no group or world bits. */
function readBindings(path: string | undefined, euid: number): FileState {
  if (path === undefined) return { state: 'unlocatable' };
  let raw: string;
  try {
    // Check the descriptor we read, so the file cannot be swapped between check and read.
    // O_NONBLOCK: a FIFO at the path must not block the daemon's event loop in open.
    const fd = openSync(path, constants.O_RDONLY | constants.O_NONBLOCK);
    try {
      const st = fstatSync(fd);
      if (!st.isFile()) return { state: 'refused', reason: 'not_file' };
      if (st.uid !== euid) return { state: 'refused', reason: 'foreign_owner' };
      if ((st.mode & 0o077) !== 0) return { state: 'refused', reason: 'mode' };
      raw = readFileSync(fd, 'utf8');
    } finally { closeSync(fd); }
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === 'ENOENT' ? { state: 'absent' } : { state: 'invalid', reason: 'unreadable' };
  }
  let file: unknown;
  try { file = JSON.parse(raw); } catch { return { state: 'invalid', reason: 'json' }; }
  const f = file as { version?: unknown; bindings?: unknown };
  if (!f || typeof f !== 'object' || f.version !== 1 || !Array.isArray(f.bindings)) return { state: 'invalid', reason: 'shape' };
  const bindings: Binding[] = [];
  let skipped = 0;
  for (const entry of f.bindings) {
    const b = parseBinding(entry);
    if (b) bindings.push(b); else skipped++;
  }
  return { state: 'ok', bindings, skipped };
}

function parseBinding(x: unknown): Binding | undefined {
  if (!x || typeof x !== 'object' || Array.isArray(x)) return undefined;
  const r = x as Record<string, unknown>;
  const str = (k: string) => typeof r[k] === 'string' ? r[k] as string : undefined;
  const profile = str('profile'), as = r.as === undefined ? '' : str('as'), hub = str('hub'), caller = str('caller');
  const codexHome = str('codex_home'), threadId = str('thread_id');
  const createdAt = Date.parse(str('created_at') ?? ''), expiresAt = Date.parse(str('expires_at') ?? '');
  const idle = r.idle_dormant_hours, attended = r.attended;
  if (!profile || as === undefined || !hub || !caller || !codexHome || !isAbsolute(codexHome) || !threadId || !threadPattern.test(threadId)) return undefined;
  if (!Number.isFinite(createdAt) || !Number.isFinite(expiresAt)) return undefined;
  if (idle !== undefined && (typeof idle !== 'number' || !Number.isFinite(idle) || idle <= 0)) return undefined;
  if (attended !== undefined && typeof attended !== 'boolean') return undefined;
  return { profile, ...(as ? { as } : {}), hub, caller, codexHome, threadId, createdAt, expiresAt,
    ...(idle !== undefined ? { idleDormantMs: Math.round(idle * 3_600_000) } : {}), attended: attended === true, origin: 'explicit' };
}

/** Everything that makes an engine a different engine. expiresAt alone is a renewal, applied in place. */
type SourcedBinding = Binding & { source?: DeliverySource };
const bindingKey = (b: Binding) => JSON.stringify([b.threadId, b.hub]);

function fingerprint(b: SourcedBinding): string {
  return JSON.stringify([b.origin, b.profile, b.as ?? '', b.hub, b.caller, b.codexHome, b.threadId, b.createdAt, b.idleDormantMs ?? null, b.attended, b.source?.config, b.source?.bindings]);
}

type Entry = { binding: SourcedBinding; engine: Engine; print: string; over: boolean; expired: boolean; expiry?: { cancel(): void } };

/**
 * Run one delivery engine per opt-in binding. Explicit bindings come from the
 * `vecnet codex bind` file, re-read every 10 s; launcher bindings are added in
 * memory by the launcher. The connector never starts Codex and never loads a thread.
 */
export function startCodexConnector(o: { kv: KeyValue; clock: Clock; log: Log; env: NodeJS.ProcessEnv; hub?: string; sources?: () => DeliverySource[] }): CodexConnector {
  const log = redactingLog(o.log), { clock, kv } = o;
  const euid = process.geteuid?.() ?? -1;
  const env = Object.fromEntries(Object.entries(o.env).filter((e): e is [string, string] => typeof e[1] === 'string'));
  const path = codexBindingsPath(o.env), limit = codexMaxBindings(o.env);
  const deps = { clock, log: o.log, kv, vecnetBin: o.env.VECNET_BIN || 'vecnet', env, openRpc: openCodexRpc, euid };
  const entries = new Map<string, Entry>();
  let stopped = false, stopping: Promise<void> | undefined, chain: Promise<void> = Promise.resolve(), timer: { cancel(): void } | undefined;
  const fileStates = new Map<string, string>(), skippedStates = new Map<string, number>();
  const unresolvedHomes = new Set<string>(), overLogged = new Set<string>(), conflictLogged = new Set<string>();

  /** All changes run one at a time, so one thread never has two engines. */
  function serial(fn: () => Promise<void>): Promise<void> {
    chain = chain.then(() => stopped ? undefined : fn()).catch(() => { log.warn('codex_connector_failed'); });
    return chain;
  }
  function canonical(home: string): string | undefined {
    try { return realpathSync(home); }
    catch {
      if (!unresolvedHomes.has(home)) { unresolvedHomes.add(home); log.warn('codex_home_unresolved', { codexHome: home }); }
      return undefined;
    }
  }
  function launch(b: SourcedBinding, over: boolean): Entry {
    // New owners fence recovery by hub. Legacy callers keep their existing KV
    // key format until that delivery path is retired; preserve recovery rows.
    const prefix = 'hub:' + JSON.stringify(b.hub) + ':';
    const scopedKv: KeyValue = o.hub !== undefined || o.sources !== undefined ? {
      get: key => kv.get(prefix + key), set: (key, value) => kv.set(prefix + key, value),
      delete: key => kv.delete(prefix + key), keys: (p = '') => kv.keys(prefix + p).map(key => key.slice(prefix.length)),
    } : kv;
    const sourceEnv = b.source ? { ...env, VECNET_CONFIG: b.source.config, VECNET_CODEX_BINDINGS: b.source.bindings } : env;
    const engine = createEngine(b, { ...deps, kv: scopedKv, env: sourceEnv });
    // Before start: an engine over the limit never subscribes, not even once.
    engine.setOverLimit(over);
    engine.start();
    return { binding: b, engine, print: fingerprint(b), over, expired: clock.now() >= b.expiresAt };
  }
  async function retire(threadId: string) {
    const e = entries.get(threadId);
    if (!e) return;
    entries.delete(threadId); e.expiry?.cancel();
    // Stopping unsubscribes; Codex unloads the thread 60 s later unless another client holds it.
    await e.engine.stop();
  }

  function fileChanged(path: string, s: FileState) {
    const sig = s.state + ':' + ('reason' in s ? s.reason ?? '' : '');
    if (sig === fileStates.get(path)) return;
    fileStates.set(path, sig);
    if (s.state === 'absent' || s.state === 'unlocatable') log.info('codex_connector_disabled', { reason: s.state === 'absent' ? 'no_bindings_file' : 'no_config_path' });
    else if (s.state === 'refused') log.warn('codex_bindings_refused', { reason: s.reason });
    else if (s.state === 'invalid') log.warn('codex_bindings_invalid', { reason: s.reason });
  }

  async function reload() {
    const listed: SourcedBinding[] = [];
    const sources = o.sources ? o.sources() : [{ config: vecnetConfigPath(o.env) ?? '', bindings: path ?? '' }];
    // Stable ordering also makes ties independent of registry enumeration order.
    const unique = new Map(sources.map(source => [JSON.stringify([source.config, source.bindings]), source]));
    for (const [id, source] of [...unique].sort(([a], [b]) => a.localeCompare(b))) {
      const s = readBindings(source.bindings || undefined, euid);
      fileChanged(id, s);
      if (s.state !== 'ok') continue;
      if (s.skipped !== skippedStates.get(id)) {
        skippedStates.set(id, s.skipped);
        if (s.skipped) log.warn('codex_bindings_skipped', { count: s.skipped });
      }
      for (const b of s.bindings) listed.push({ ...b, ...(o.sources ? { source } : {}) });
    }
    const sorted = listed.sort((a, b) => a.createdAt - b.createdAt ||
      bindingKey(a).localeCompare(bindingKey(b)) || (a.source?.config ?? '').localeCompare(b.source?.config ?? ''));
    const wanted = new Map<string, SourcedBinding>(), identities = new Set<string>();
    for (const b of sorted) {
      const key = bindingKey(b), identity = JSON.stringify([b.source?.config ?? '', b.hub, b.profile, b.as ?? '']);
      const held = entries.get(key);
      if (wanted.has(key) || identities.has(identity) || held?.binding.origin === 'launcher') {
        if (!conflictLogged.has(key)) { conflictLogged.add(key); log.warn('codex_binding_duplicate', { threadId: b.threadId }); }
        continue;
      }
      wanted.set(key, b); identities.add(identity);
    }
    // Count across every registered source and hub BEFORE selecting this hub.
    // Unresolved homes might alias any existing home, so count them conservatively.
    const now = clock.now(), counts = new Map<string, number>(), over = new Map<string, boolean>();
    let unresolved = 0, total = 0;
    for (const [key, b] of wanted) {
      if (now >= b.expiresAt) { over.set(key, false); continue; }
      const home = canonical(b.codexHome);
      const n = home === undefined ? ++unresolved + total : (counts.get(home) ?? 0) + 1 + unresolved;
      if (home !== undefined) { counts.set(home, (counts.get(home) ?? 0) + 1); total++; }
      over.set(key, n > limit);
    }
    for (const [key, b] of wanted) if (o.hub !== undefined && b.hub !== o.hub) wanted.delete(key);
    // Stop engines whose binding is gone or changed identity, before any replacement starts.
    const retiring: Promise<void>[] = [];
    for (const [threadId, e] of entries) {
      if (e.binding.origin !== 'explicit') continue;
      const next = wanted.get(threadId);
      if (!next || fingerprint(next) !== e.print) retiring.push(retire(threadId));
    }
    await Promise.all(retiring);
    if (stopped) return;
    for (const [threadId, b] of wanted) {
      const isOver = over.get(threadId)!;
      if (isOver && !overLogged.has(threadId)) { overLogged.add(threadId); log.warn('codex_binding_over_limit', { threadId: b.threadId, limit }); }
      if (!isOver) overLogged.delete(threadId);
      const e = entries.get(threadId);
      if (!e) { entries.set(threadId, launch(b, isOver)); continue; }
      // A renewal moves expiresAt only; the engine reads it on every check.
      e.binding.expiresAt = b.expiresAt;
      const expired = now >= b.expiresAt;
      if (e.over !== isOver || e.expired !== expired) {
        // setOverLimit also re-checks liveness now: an unbind, expiry or freed
        // slot takes effect within this reload, not at the next heartbeat.
        e.over = isOver; e.expired = expired; e.engine.setOverLimit(isOver);
      }
    }
  }
  function schedule() {
    if (stopped) return;
    timer = clock.timer(CODEX_RELOAD_MS, () => { timer = undefined; void serial(reload).then(schedule); });
  }
  void serial(reload).then(schedule);

  return {
    stop() {
      stopping ??= (async () => {
        stopped = true; timer?.cancel();
        await chain;
        // Every engine unsubscribes. Bound threads with no TUI go dormant if the
        // launcher daemon stays down for more than about 60 s.
        await Promise.allSettled([...entries.keys()].map(retire));
      })();
      return stopping;
    },
    addLauncherBinding(input) {
      const b: Binding = { ...input, attended: true, origin: 'launcher' };
      void serial(async () => {
        if (!threadPattern.test(b.threadId) || !isAbsolute(b.codexHome) || !Number.isFinite(b.expiresAt)) {
          log.warn('codex_launcher_binding_invalid', { threadId: b.threadId }); return;
        }
        if (clock.now() >= b.expiresAt) return;
        if (o.hub !== undefined && b.hub !== o.hub) return;
        const key = bindingKey(b), held = entries.get(key);
        if (held?.binding.origin === 'explicit') { log.warn('codex_binding_duplicate', { threadId: b.threadId }); return; }
        if (held) await retire(key);
        if (stopped) return;
        // The launcher owns this thread's policy: it counts as attended, and
        // outside the explicit limit, because the launcher bounds its own jobs.
        const e = launch(b, false);
        e.expiry = clock.timer(Math.max(0, b.expiresAt - clock.now()), () => {
          void serial(async () => { if (entries.get(key) === e) await retire(key); });
        });
        entries.set(key, e);
      });
    },
    removeLauncherBinding(threadId) {
      void serial(async () => {
        for (const [key, e] of entries) if (e.binding.threadId === threadId && e.binding.origin === 'launcher') await retire(key);
      });
    },
  };
}
