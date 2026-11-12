import { randomUUID } from 'node:crypto';
import { closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, realpathSync } from 'node:fs';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { KeyValue } from '../backend.ts';
import { redactingLog } from '../redact.ts';
import { openLedger } from '../ledger.ts';
import { createEngine } from './engine.ts';
import type { Binding, Engine } from './engine.ts';
import { openCodexRpc } from './rpc.ts';
import { bindingFingerprint, parseBinding, readBindings, threadKey } from './connector-authority.ts';
import type { FileState } from './connector-authority.ts';
import { openStatusPublisher } from './connector-status.ts';
import type { StatusPublisher } from './connector-status.ts';

export const CODEX_MAX_KEPT_LOADED = 8;
export const CODEX_RELOAD_MS = 10_000;
export type LauncherBinding = Omit<Binding, 'origin'> & { ensureLoaded: () => Promise<boolean> };
export interface CodexConnector {
  stop(): Promise<void>;
  /** Attach launcher lifecycle only to a complete, matching authoritative binding. */
  addLauncherBinding(b: LauncherBinding): void;
  /** A home is required when the same thread ID occurs in multiple homes. */
  removeLauncherBinding(threadId: string, codexHome?: string): void;
}

/** One authority for the OS user, independent of profiles, configs, and XDG.
 * An override may only name this same canonical path. Unknown HOME fails closed. */
export function codexBindingsPath(env: NodeJS.ProcessEnv): string | undefined {
  if (!env.HOME || !isAbsolute(env.HOME)) return;
  try {
    const home = realpathSync(env.HOME);
    const expected = canonicalPath(join(home, '.local', 'state', 'vecnet', 'codex', 'bindings.json'));
    if (env.VECNET_CODEX_BINDINGS && canonicalPath(env.VECNET_CODEX_BINDINGS) !== expected) return;
    return expected;
  } catch { return; }
}

/** Check every descendant before SQLite can open its database or sidecars.
 * HOME is canonicalized, but state-directory symlinks are never followed. */
export function codexRecoveryPath(env: NodeJS.ProcessEnv): string {
  const unsafe = () => new Error('unsafe state path');
  const uid = process.geteuid?.();
  if (uid === undefined || !env.HOME || !isAbsolute(env.HOME)) throw unsafe();
  const home = realpathSync(env.HOME);
  const checkDir = (path: string, privateDir: boolean) => {
    const st = lstatSync(path);
    if (!st.isDirectory() || st.isSymbolicLink() || st.uid !== uid || (st.mode & (privateDir ? 0o077 : 0o022)) !== 0) throw unsafe();
  };
  checkDir(home, false);
  let path = home;
  for (const component of ['.local', 'state', 'vecnet', 'codex']) {
    path = join(path, component);
    try { mkdirSync(path, { mode: 0o700 }); }
    catch (e) { if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e; }
    checkDir(path, component === 'codex');
  }
  const database = join(path, 'delivery.sqlite');
  for (const file of [database, database + '-wal', database + '-shm', database + '-journal']) {
    try {
      const st = lstatSync(file);
      if (!st.isFile() || st.isSymbolicLink() || st.uid !== uid || (st.mode & 0o077) !== 0 || st.nlink !== 1) throw unsafe();
    } catch (e) { if ((e as NodeJS.ErrnoException).code !== 'ENOENT') throw e; }
  }
  const fd = openSync(database, constants.O_CREAT | constants.O_RDWR | constants.O_NOFOLLOW | constants.O_NONBLOCK, 0o600);
  try {
    const st = fstatSync(fd);
    if (!st.isFile() || st.uid !== uid || (st.mode & 0o077) !== 0 || st.nlink !== 1) throw unsafe();
  } finally { closeSync(fd); }
  return database;
}

/** Import a caller's old namespace once, before engines can touch recovery.
 * The source id is persisted first; its completion marker and copied rows are
 * committed together in the shared DB. Retained offline rows can never replay
 * after another mode has settled them. Conflicts preserve both stores and stop
 * delivery. The caller namespace remains a compatibility mirror, not authority. */
function openRecovery(database: string, clock: Clock, legacy?: KeyValue) {
  const ledger = openLedger(database, clock), kv = ledger.kv('codex');
  try {
    if (legacy) {
      const sourceKey = 'shared-recovery-source:v1';
      let source = legacy.get(sourceKey);
      if (source === undefined) { source = randomUUID(); legacy.set(sourceKey, source); }
      if (!/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(source)) throw new Error('invalid recovery source');
      ledger.tx(() => {
        const imports = ledger.kv('codex:imports');
        if (imports.get(source!) !== undefined) return;
        for (const key of legacy.keys()) {
          if (key === sourceKey) continue;
          const value = legacy.get(key), current = kv.get(key);
          if (value === undefined || (current !== undefined && current !== value)) throw new Error('recovery conflict');
          kv.set(key, value);
        }
        imports.set(source!, '1');
      });
    }
    const store: KeyValue = {
      get: key => kv.get(key), keys: prefix => kv.keys(prefix),
      set(key, value) { kv.set(key, value); legacy?.set(key, value); },
      delete(key) { kv.delete(key); legacy?.delete(key); },
    };
    return { kv: store, close: () => ledger.close() };
  } catch (error) { ledger.close(); throw error; }
}

/** Resolve existing ancestors too, before the authority has been created. */
function canonicalPath(path: string): string {
  const absolute = resolve(path);
  try { return realpathSync(absolute); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT' || dirname(absolute) === absolute) throw error;
    return join(canonicalPath(dirname(absolute)), basename(absolute));
  }
}

/** VECNET_CODEX_MAX_BINDINGS, from 1 to 32; anything else is the default, as in `codex bind`. */
export function codexMaxBindings(env: NodeJS.ProcessEnv): number {
  const raw = env.VECNET_CODEX_MAX_BINDINGS ?? '';
  const n = /^[0-9]{1,2}$/.test(raw) ? Number(raw) : NaN;
  return n >= 1 && n <= 32 ? n : CODEX_MAX_KEPT_LOADED;
}

type Entry = { binding: Binding; engine: Engine; print: string; over: boolean; expired: boolean };
const fingerprint = (b: Binding) => b.origin + ':' + bindingFingerprint(b);

/** One engine per authoritative canonical home/thread, one connector per file.
 * Launcher registrations supply lifecycle callbacks, never delivery authority. */
export function startCodexConnector(o: { kv?: KeyValue; clock: Clock; log: Log; env: NodeJS.ProcessEnv; openRpc?: typeof openCodexRpc }): CodexConnector {
  const log = redactingLog(o.log), { clock } = o;
  const euid = process.geteuid?.() ?? -1;
  const env = Object.fromEntries(Object.entries(o.env).filter((e): e is [string, string] => typeof e[1] === 'string'));
  const path = codexBindingsPath(o.env), limit = codexMaxBindings(o.env);
  const entries = new Map<string, Entry>(), registrations = new Map<string, Binding>();
  let stopped = false, stopping: Promise<void> | undefined, chain: Promise<void> = Promise.resolve(), timer: { cancel(): void } | undefined;
  let recovery: ReturnType<typeof openRecovery> | undefined;
  let publisher: StatusPublisher | undefined, duplicateLogged = false, lastFile = '';
  const overLogged = new Set<string>();

  function serial(fn: () => Promise<void>): Promise<void> {
    chain = chain.then(() => stopped ? undefined : fn()).catch(() => { log.warn('codex_connector_failed'); });
    return chain;
  }
  function launch(b: Binding, over: boolean): Entry {
    const engine = createEngine(b, { clock, log: o.log, kv: recovery!.kv, vecnetBin: o.env.VECNET_BIN || 'vecnet',
      env, openRpc: o.openRpc ?? openCodexRpc, euid });
    engine.setOverLimit(over);
    engine.start();
    return { binding: b, engine, print: fingerprint(b), over, expired: clock.now() >= b.expiresAt };
  }
  async function retire(key: string) {
    const e = entries.get(key);
    if (!e) return;
    entries.delete(key);
    await e.engine.stop();
  }
  function fileChanged(s: FileState) {
    const sig = s.state + ':' + ('reason' in s ? s.reason ?? '' : '');
    if (sig === lastFile) return;
    lastFile = sig;
    if (s.state === 'absent' || s.state === 'unlocatable') log.info('codex_connector_disabled', { reason: s.state === 'absent' ? 'no_bindings_file' : 'invalid_authority_path' });
    else if (s.state === 'refused') log.warn('codex_bindings_refused', { reason: s.reason });
    else if (s.state === 'invalid') log.warn('codex_bindings_invalid', { reason: s.reason });
  }
  async function reload() {
    const s = readBindings(path, euid);
    fileChanged(s);
    if (!publisher && path !== undefined) {
      try { codexRecoveryPath(o.env); }
      catch { log.warn('codex_recovery_refused'); return; }
      publisher = openStatusPublisher(path, euid);
      if (!publisher) {
        if (!duplicateLogged) { duplicateLogged = true; log.warn('codex_connector_duplicate'); }
        return;
      }
      duplicateLogged = false;
    }
    if (!publisher) return;
    if (!recovery) {
      try { recovery = openRecovery(codexRecoveryPath(o.env), clock, o.kv); }
      catch { log.warn('codex_recovery_refused'); publisher.publish(clock.now(), []); return; }
    }
    const listed = s.state === 'ok' ? s.bindings : [];
    const sorted = [...listed].sort((a, b) => a.createdAt - b.createdAt || threadKey(a).localeCompare(threadKey(b)));
    const wanted = new Map<string, Binding>();
    for (const b of sorted) {
      const key = threadKey(b), registered = registrations.get(key);
      if (registered && bindingFingerprint(registered) === bindingFingerprint(b) && clock.now() < registered.expiresAt) {
        wanted.set(key, { ...b, origin: 'launcher', ensureLoaded: registered.ensureLoaded });
      } else {
        registrations.delete(key);
        wanted.set(key, b);
      }
    }
    for (const key of registrations.keys()) if (!wanted.has(key)) registrations.delete(key);
    const now = clock.now(), counts = new Map<string, number>(), over = new Map<string, boolean>();
    for (const [key, b] of wanted) {
      if (now >= b.expiresAt) { over.set(key, false); continue; }
      const n = (counts.get(b.codexHome) ?? 0) + 1;
      counts.set(b.codexHome, n); over.set(key, n > limit);
    }
    const retiring: Promise<void>[] = [];
    for (const [key, e] of entries) {
      const next = wanted.get(key);
      if (!next || fingerprint(next) !== e.print) retiring.push(retire(key));
    }
    await Promise.all(retiring);
    if (stopped) return;
    for (const [key, b] of wanted) {
      const isOver = over.get(key)!;
      if (isOver && !overLogged.has(key)) { overLogged.add(key); log.warn('codex_binding_over_limit', { threadId: b.threadId, limit }); }
      if (!isOver) overLogged.delete(key);
      const e = entries.get(key);
      if (!e) { entries.set(key, launch(b, isOver)); continue; }
      e.binding.expiresAt = b.expiresAt;
      const expired = now >= b.expiresAt;
      if (e.over !== isOver || e.expired !== expired) {
        e.over = isOver; e.expired = expired; e.engine.setOverLimit(isOver);
      }
    }
    publisher.publish(clock.now(), entries.values());
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
        await Promise.allSettled([...entries.keys()].map(retire));
        try { recovery?.close(); }
        finally { publisher?.close(); publisher = undefined; }
      })();
      return stopping;
    },
    addLauncherBinding(input) {
      void serial(async () => {
        const b = parseBinding({ config_path: input.configPath, generation: input.generation, profile: input.profile,
          as: input.as, hub: input.hub, caller: input.caller, codex_home: input.codexHome, thread_id: input.threadId,
          created_at: Number.isFinite(input.createdAt) ? new Date(input.createdAt).toISOString() : '',
          expires_at: Number.isFinite(input.expiresAt) ? new Date(input.expiresAt).toISOString() : '',
          attended: input.attended, idle_dormant_hours: input.idleDormantMs === undefined ? undefined : input.idleDormantMs / 3_600_000 });
        const s = readBindings(path, euid);
        const authority = b && s.state === 'ok' ? s.bindings.find(row => bindingFingerprint(row) === bindingFingerprint(b)) : undefined;
        if (!b || !authority || clock.now() >= b.expiresAt || typeof input.ensureLoaded !== 'function') {
          log.warn('codex_launcher_binding_unauthorized', { threadId: input.threadId });
          await reload(); return;
        }
        registrations.set(threadKey(b), { ...b, ensureLoaded: input.ensureLoaded });
        await reload();
      });
    },
    removeLauncherBinding(threadId, codexHome) {
      void serial(async () => {
        let home: string | undefined;
        if (codexHome !== undefined) { try { home = realpathSync(codexHome); } catch { return; } }
        const matches = [...registrations].filter(([, b]) => b.threadId === threadId && (home === undefined || b.codexHome === home));
        if (matches.length !== 1) return;
        registrations.delete(matches[0][0]);
        await reload();
      });
    },
  };
}
