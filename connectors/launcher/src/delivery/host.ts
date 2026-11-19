import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { KeyValue } from '../backend.ts';
import { CODEX_RELOAD_MS, codexAuthorityLocation, codexRecoveryPath, isCurrentCodexAuthority, startCodexConnector, validCodexRegistryLeaf } from '../codex/connector.ts';
import type { CodexConnector, CodexConnectorOutcome } from '../codex/connector.ts';
import { openDeliveryLease } from '../codex/connector-status.ts';
import type { DeliveryLease } from '../codex/connector-status.ts';

export type DeliveryHost = CodexConnector;
export interface HostedAdapter {
  start(): void;
  /** Fence new work synchronously, then await cleanup. */
  stop(): Promise<void>;
}
export interface DeliveryHostOptions {
  env: NodeJS.ProcessEnv;
  clock: Clock;
  log: Log;
  kv?: KeyValue;
  openLease?: typeof openDeliveryLease;
  startCodex?: typeof startCodexConnector;
  auxiliary?: () => HostedAdapter;
}

/** Both service entrypoints elect here, and only the host releases the lease. */
export function startDeliveryHost(o: DeliveryHostOptions): DeliveryHost {
  const { clock, log, kv } = o;
  // Keep the configuration used for election even if the caller updates its env.
  const env = { ...o.env };
  // The elected location is fixed for the host's lifetime; only its registry
  // leaf is revalidated, so a repairable refusal retries at the same place.
  const location = codexAuthorityLocation(env), euid = process.geteuid?.() ?? -1;
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
      env, clock, log, kv, lease, leaseLost: leaseLost.signal, authority: location,
    });
    void codex.stopped.then(outcome => { if (outcome.reason === 'status_lock_lost') onLost(); });
    if (stopping) return;
    for (const apply of pending.splice(0)) apply(codex);
    try {
      auxiliary = o.auxiliary?.();
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
