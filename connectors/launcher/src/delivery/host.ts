import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { KeyValue } from '../backend.ts';
import { openLedger } from '../ledger.ts';
import { CODEX_RELOAD_MS, codexAuthorityLocation, codexRecoveryPath, isCurrentCodexAuthority, startCodexConnector, validCodexRegistryLeaf } from '../codex/connector.ts';
import type { CodexConnector, CodexConnectorOutcome } from '../codex/connector.ts';
import { openDeliveryLease } from '../codex/connector-status.ts';
import type { DeliveryLease } from '../codex/connector-status.ts';
import { createClaudeDeliveryAdapter } from './claude-adapter.ts';
import { createWatchPool } from './watch-pool.ts';
import type { WatchPool } from './watch-pool.ts';

export type DeliveryHost = CodexConnector;
export interface HostedAdapter {
  start(): void;
  /** Fence new work synchronously, then await cleanup. */
  stop(): Promise<void>;
}
/** Run hosted adapters as one: each starts in order, and stop() awaits every
 * cleanup even if one fails. */
function composeHosted(adapters: HostedAdapter[]): HostedAdapter {
  return {
    start() { for (const adapter of adapters) adapter.start(); },
    stop() { return Promise.all(adapters.map(adapter => adapter.stop())).then(() => { }); },
  };
}
export interface DeliveryHostOptions {
  env: NodeJS.ProcessEnv;
  clock: Clock;
  log: Log;
  kv?: KeyValue;
  openLease?: typeof openDeliveryLease;
  startCodex?: typeof startCodexConnector;
  /** The caller's adapter (passive Claude status). It receives the native
   * delivery adapter's current admissions (binding key -> generation),
   * which are always empty when the delivery adapter is not constructed. */
  auxiliary?: (native: () => ReadonlyMap<string, string>) => HostedAdapter;
  /** Notification-watch pool owned by this host; defaults to a fresh pool. */
  watchPool?: WatchPool;
  /** Pointer-only Claude native-delivery canary (task 7; Phase 2 task 3).
   * This is the host kill switch (`claudeDeliveryCanaryAllowed`): false
   * constructs no delivery adapter. True constructs it, but it delivers only
   * to the single scope holding the durable per-profile canary record
   * (`admission.canary`) — the env var is never an enable, and `claude
   * enable` alone can never turn native delivery on. Its claim ledger lives
   * in the canonical per-user state domain (the shared delivery database
   * beside the elected bindings), in both host modes. */
  claudeDeliveryCanary?: boolean;
}

const NO_ADMISSIONS: ReadonlyMap<string, string> = new Map();

/** Both service entrypoints elect here, and only the host releases the lease. */
export function startDeliveryHost(o: DeliveryHostOptions): DeliveryHost {
  const { clock, log, kv } = o;
  // Keep the configuration used for election even if the caller updates its env.
  const env = { ...o.env };
  // The elected location is fixed for the host's lifetime; only its registry
  // leaf is revalidated, so a repairable refusal retries at the same place.
  const location = codexAuthorityLocation(env), euid = process.geteuid?.() ?? -1;
  // The host owns the notification pool: engines subscribe to it, and it is
  // drained (children exited) before the lease is released.
  const watchPool = o.watchPool ?? createWatchPool({ log });
  const leaseLost = new AbortController();
  let markStopped!: (outcome: CodexConnectorOutcome) => void;
  const finished = new Promise<CodexConnectorOutcome>(resolve => { markStopped = resolve; });
  const pending: Array<(adapter: CodexConnector) => void> = [];
  let lease: DeliveryLease | undefined, codex: CodexConnector | undefined, auxiliary: HostedAdapter | undefined;
  let election: Promise<void> | undefined, retry: ReturnType<Clock['timer']> | undefined;
  let stopping: Promise<void> | undefined, auxiliaryDrain: Promise<void> | undefined, duplicateLogged = false;

  function onLost() {
    if (leaseLost.signal.aborted) return;
    leaseLost.abort(); // Fence Codex even if its ordinary shutdown is already draining.
    void stop().catch(() => { log.warn('delivery_host_cleanup_failed'); });
    if (auxiliary) auxiliaryDrain ??= drainAuxiliary();
  }

  async function drainAuxiliary() {
    try { await auxiliary?.stop(); }
    catch { log.warn('delivery_auxiliary_stop_failed'); }
  }

  async function elect() {
    if (stopping) return;
    if (!location) {
      log.info('codex_connector_disabled', { reason: 'invalid_authority_path' });
      return;
    }
    // A refused leaf at the fixed location is repairable; keep retrying there.
    if (!validCodexRegistryLeaf(location, env)) {
      log.info('codex_connector_disabled', { reason: 'unsafe_registry_leaf' });
      return;
    }
    // Keep the original private state checks before opening the lock.
    try { codexRecoveryPath(env, location); }
    catch { log.warn('codex_recovery_refused'); return; }
    lease = await (o.openLease ?? openDeliveryLease)(location.bindings, euid, { env, onLost, pinnedBindings: true });
    if (stopping) return;
    if (!lease) {
      if (!duplicateLogged) { duplicateLogged = true; log.warn('codex_connector_duplicate'); }
      return;
    }
    if (!lease.valid()) { onLost(); return; }
    if (!isCurrentCodexAuthority(env, location)) {
      log.warn('codex_recovery_refused');
      try { await lease.close(); } finally { lease = undefined; }
      return;
    }
    codex = (o.startCodex ?? startCodexConnector)({
      env, clock, log, kv, lease, leaseLost: leaseLost.signal, authority: location, watchPool,
    });
    void codex.stopped.then(outcome => { if (outcome.reason === 'status_lock_lost') onLost(); });
    if (stopping) return;
    for (const apply of pending.splice(0)) apply(codex);
    // The caller's adapter (passive Claude status) always rides the lease;
    // the native-delivery canary adapter composes beside it unless the kill
    // switch is thrown, and reports its admissions to the status adapter.
    // The delivery adapter is built first so status can read it; status is
    // listed (and so started) first. `auxiliary` is reassigned after every
    // built part, so a failure partway through (a throwing status factory, a
    // failing start) still drains everything already built — including the
    // never-started delivery adapter and its ledger.
    const adapters: HostedAdapter[] = [];
    const compose = () => { auxiliary = adapters.length === 1 ? adapters[0] : composeHosted([...adapters]); };
    try {
      let native = () => NO_ADMISSIONS;
      if (o.claudeDeliveryCanary) {
        const ledger = openLedger(codexRecoveryPath(env, location), clock);
        const delivery = (() => {
          try { return createClaudeDeliveryAdapter({ pool: watchPool, clock, log, kv: ledger.kv('claude'), env }); }
          catch (error) { ledger.close(); throw error; }
        })();
        native = () => delivery.admitted();
        adapters.push({
          start: () => delivery.start(),
          stop: async () => { try { await delivery.stop(); } finally { ledger.close(); log.info('claude_delivery_ledger_closed'); } },
        });
        compose();
      }
      const base = o.auxiliary?.(native);
      if (base) { adapters.unshift(base); compose(); }
      if (adapters.length === 0) auxiliary = undefined;
      if (!stopping) auxiliary?.start();
    } catch {
      log.warn('delivery_auxiliary_start_failed');
      auxiliaryDrain ??= drainAuxiliary();
    }
  }

  function attempt() {
    retry = undefined;
    election = Promise.resolve().then(elect).catch(async () => {
      log.warn('codex_connector_failed');
      if (!codex) {
        try { await lease?.close(); } finally { lease = undefined; }
      }
    }).finally(() => {
      election = undefined;
      if (!stopping && !lease && location !== undefined) retry = clock.timer(CODEX_RELOAD_MS, attempt);
    });
  }

  function stop(): Promise<void> {
    if (stopping) return stopping;
    retry?.cancel();
    pending.length = 0;
    stopping = Promise.resolve().then(async () => {
      try {
        await election;
        const [codexResult] = await Promise.allSettled([
          Promise.resolve().then(() => codex?.stop()),
          auxiliaryDrain ??= drainAuxiliary(),
        ]);
        if (codexResult.status === 'rejected') throw codexResult.reason;
      } finally {
        // Owned children exit before the lease can be released to another host.
        await watchPool.close().catch(() => { log.warn('delivery_watch_pool_close_failed'); });
        try { await lease?.close(); }
        finally { markStopped({ reason: leaseLost.signal.aborted ? 'status_lock_lost' : 'stopped' }); }
      }
    });
    return stopping;
  }

  function forward(apply: (adapter: CodexConnector) => void) {
    if (stopping) return;
    if (codex) apply(codex);
    else if (location) pending.push(apply);
  }

  attempt();
  return {
    stopped: finished,
    stop,
    addLauncherBinding: binding => forward(codex => codex.addLauncherBinding(binding)),
    removeLauncherBinding: (thread, home) => forward(codex => codex.removeLauncherBinding(thread, home)),
  };
}
