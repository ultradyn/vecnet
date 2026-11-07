import { closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, readFileSync, writeFileSync } from 'node:fs';
import { isAbsolute, join } from 'node:path';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import { systemClock, stderrLog } from '../daemon.ts';
import { openLedger } from '../ledger.ts';
import { startCodexConnector } from './connector.ts';
import { mapStatus } from './protocol.ts';
import { openCodexRpc } from './rpc.ts';
import type { CodexRpc } from './rpc.ts';

/** Metadata only: Node passes config paths to Go and never reads profile TOML. */
export type DeliverySource = { config: string; bindings: string };
export type DeliveryRegistry = { version: 1; sources: DeliverySource[] };
export type DeliveryProbe = { loaded: boolean; previous: 'gone' | 'live' | 'unknown' };
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const quietLog: Log = { info() {}, warn() {}, error() {} };
const euid = () => process.geteuid?.() ?? -1;

/** A read can establish absence only from a successful, understood status. */
export async function probeCodexDelivery(o: { codexHome: string; thread: string; previousThread?: string }): Promise<DeliveryProbe> {
  const result: DeliveryProbe = { loaded: false, previous: 'unknown' };
  if (!isAbsolute(o.codexHome) || !uuid.test(o.thread) || (o.previousThread !== undefined && !uuid.test(o.previousThread))) return result;
  let rpc: CodexRpc | undefined;
  try {
    rpc = await openCodexRpc({ codexHome: o.codexHome, euid: euid(), log: quietLog, timeoutMs: 1500 });
    const state = async (threadId: string): Promise<DeliveryProbe['previous']> => {
      try {
        const read = await rpc!.call<{ thread?: { status?: unknown; archived?: unknown } }>('thread/read', { threadId, includeTurns: false }, 1500);
        if (!read?.thread || typeof read.thread !== 'object') return 'unknown';
        if (read.thread.archived === true) return 'gone';
        const status = mapStatus(read.thread.status);
        return status === 'not_loaded' ? 'gone' : status === 'unknown' ? 'unknown' : 'live';
      } catch { return 'unknown'; }
    };
    const current = await state(o.thread);
    result.loaded = current === 'live';
    if (o.previousThread) result.previous = o.previousThread === o.thread ? current : await state(o.previousThread);
  } catch { /* No daemon, refused version, transport errors: never infer a takeover. */ }
  finally { rpc?.close(); }
  return result;
}

function privateFile(path: string, create = false): number {
  const fd = openSync(path, constants.O_RDWR | constants.O_NONBLOCK | constants.O_NOFOLLOW | (create ? constants.O_CREAT : 0), 0o600);
  const st = fstatSync(fd);
  if (!st.isFile() || st.uid !== euid() || (st.mode & 0o777) !== 0o600) {
    closeSync(fd); throw new Error('unsafe delivery metadata');
  }
  return fd;
}
function readMetadata(path: string): unknown {
  // Open read-only: metadata never needs write access, and FIFO/device paths
  // are refused by descriptor before any read can block.
  const fd = openSync(path, constants.O_RDONLY | constants.O_NONBLOCK | constants.O_NOFOLLOW);
  try {
    const st = fstatSync(fd);
    if (!st.isFile() || st.uid !== euid() || (st.mode & 0o777) !== 0o600) throw new Error('unsafe delivery metadata');
    return JSON.parse(readFileSync(fd, 'utf8'));
  } finally { closeSync(fd); }
}
export function readDeliveryRegistry(path: string): DeliverySource[] {
  const registry = readMetadata(path) as DeliveryRegistry | null;
  if (!registry || registry.version !== 1 || !Array.isArray(registry.sources)) throw new Error('invalid delivery registry');
  return registry.sources.map(source => {
    if (!source || typeof source.config !== 'string' || !isAbsolute(source.config) ||
        typeof source.bindings !== 'string' || !isAbsolute(source.bindings) || Object.keys(source).some(k => k !== 'config' && k !== 'bindings')) {
      throw new Error('invalid delivery source');
    }
    return { config: source.config, bindings: source.bindings };
  });
}

/** Go acquires FD3's lifetime flock, then execs Node in the same PID.
 * Fixture bypass is a library option only: production CLI cannot omit the fence.
 */
export function startDeliveryRunner(o: { hub: string; stateDir: string; registry: string;
  env?: NodeJS.ProcessEnv; clock?: Clock; log?: Log; fixture?: { allowMissingLeaseFd: true } }) {
  if (!isAbsolute(o.stateDir) || !isAbsolute(o.registry) || !o.hub) throw new Error('invalid delivery paths or hub');
  const hub = new URL(o.hub);
  if (!['http:', 'https:'].includes(hub.protocol) || hub.username || hub.password) throw new Error('invalid delivery hub');
  if (!o.fixture?.allowMissingLeaseFd) {
    const lease = fstatSync(3);
    if (!lease.isFile() || lease.uid !== euid() || (lease.mode & 0o777) !== 0o600) {
      throw new Error('delivery requires inherited lease');
    }
  }
  mkdirSync(o.stateDir, { recursive: true, mode: 0o700 });
  const dir = lstatSync(o.stateDir);
  if (!dir.isDirectory() || dir.uid !== euid() || (dir.mode & 0o777) !== 0o700) throw new Error('unsafe delivery state directory');
  const identityPath = join(o.stateDir, 'hub.json');
  try {
    const fd = openSync(identityPath, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    try { writeFileSync(fd, JSON.stringify({ version: 1, hub: o.hub }) + '\n'); } finally { closeSync(fd); }
  } catch (error) { if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error; }
  const identity = readMetadata(identityPath) as { version?: unknown; hub?: unknown } | null;
  if (identity?.version !== 1 || identity.hub !== o.hub) throw new Error('delivery hub identity mismatch');
  const ledgerPath = join(o.stateDir, 'ledger.sqlite');
  closeSync(privateFile(ledgerPath, true));
  const clock = o.clock ?? systemClock, log = o.log ?? stderrLog();
  const ledger = openLedger(ledgerPath, clock), kv = ledger.kv('codex');
  // Only metadata reaches engines. No profile parser, bearer or launcher config.
  const inherited = o.env ?? process.env;
  const env: NodeJS.ProcessEnv = {};
  for (const key of ['HOME', 'PATH', 'XDG_CONFIG_HOME', 'XDG_RUNTIME_DIR', 'XDG_DATA_HOME', 'VECNET_BIN', 'VECNET_CODEX_MAX_BINDINGS']) {
    if (inherited[key] !== undefined) env[key] = inherited[key];
  }
  // Task 3 must add this control to engine.ts's child env allowlist before
  // enabling Go next-action automation. The current engine drops this name.
  env.VECNET_CODEX_AUTO_DELIVERY = '0';
  const connector = startCodexConnector({ kv, clock, log, env, hub: o.hub, sources: () => {
    try { return readDeliveryRegistry(o.registry); }
    catch { log.warn('codex_delivery_registry_refused'); return []; }
  } });
  let stopping: Promise<void> | undefined;
  let resolveStopped: () => void;
  const stopped = new Promise<void>(resolve => { resolveStopped = resolve; });
  function stop(): Promise<void> {
    stopping ??= (async () => {
      try {
        await connector.stop();
        ledger.close();
      } finally {
        process.removeListener('SIGTERM', onSignal); process.removeListener('SIGINT', onSignal);
        // Last: never release the ownership fence while a sender is draining.
        if (!o.fixture?.allowMissingLeaseFd) closeSync(3);
        resolveStopped();
      }
    })();
    return stopping;
  }
  function onSignal() { void stop(); }
  process.once('SIGTERM', onSignal); process.once('SIGINT', onSignal);
  return { kv, stop, stopped };
}
