import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { statSync } from 'node:fs';
import { join } from 'node:path';
import { createInterface } from 'node:readline';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { KeyValue } from '../backend.ts';
import { planInput } from '../input-plan.ts';
import { redactingLog, scrubEnv } from '../redact.ts';
import { openCodexRpc, CodexRpcError } from './rpc.ts';
import type { CodexRpc } from './rpc.ts';
import { buildThreadResume, buildThreadUnsubscribe, buildTurnStart, buildTurnSteer, buildTurnsPage, mapStatus, parseResumePolicy } from './protocol.ts';

export interface Binding {
  profile: string; as?: string; hub: string; caller: string; codexHome: string; threadId: string;
  createdAt: number; expiresAt: number; idleDormantMs?: number;
  attended: boolean; origin: 'explicit' | 'launcher'; ensureLoaded?: () => Promise<boolean>;
}
export type DormantReason = 'expired' | 'archived' | 'not_loaded' | 'idle_stale' | 'approval_policy' |
  'waiting_approval' | 'waiting_input' | 'over_limit' | 'identity_changed';
export type InflightRow = {
  threadId: string; key: string; claim: string; deliveries: string[]; method: 'turn/start' | 'turn/steer';
  attemptTurnId: string | null; priorTurnId: string | null; acceptedAt: number | null; state: 'sending' | 'pending' | 'unresolved';
};
export type Liveness = { live: true } | { live: false; reason: DormantReason };
export interface EngineDeps {
  clock: Clock; log: Log; kv: KeyValue; vecnetBin: string; env: Record<string, string>;
  openRpc: typeof openCodexRpc; euid: number; debounceMs?: number; minGapMs?: number;
}
export interface Engine {
  start(): void; stop(): Promise<void>; state(): 'connecting' | 'ready' | 'unreachable' | 'refused_version';
  liveness(): Liveness; subscribed(): boolean; setOverLimit(over: boolean): void;
}
/** The Go CLI's `configPath`: VECNET_CONFIG, else XDG_CONFIG_HOME, else HOME. It holds the profiles' credentials. */
export function vecnetConfigPath(env: Record<string, string | undefined>): string | undefined {
  return env.VECNET_CONFIG ? env.VECNET_CONFIG
    : env.XDG_CONFIG_HOME ? join(env.XDG_CONFIG_HOME, 'vecnet', 'config.toml')
    : env.HOME ? join(env.HOME, '.config', 'vecnet', 'config.toml') : undefined;
}
type RecordValue = Record<string, any>;
type Batch = { stub: string; entries: { key: string; value: string }[]; deliveries: string[] };
const obj = (x: unknown): RecordValue => x !== null && typeof x === 'object' && !Array.isArray(x) ? x as RecordValue : {};
const timeout = 5000, blockedMs = 600_000;

/** One serial delivery lane. Only Go renders peer input and claims delivery ids. */
export function createEngine(b: Binding, d: EngineDeps): Engine {
  const log = redactingLog(d.log), { clock, kv } = d;
  const envKeys = ['HOME', 'PATH', 'XDG_CONFIG_HOME', 'XDG_RUNTIME_DIR', 'VECNET_CONFIG'];
  // scrubEnv removes credential-bearing names; intersect separately because the
  // launcher's helper intentionally preserves ordinary non-secret variables.
  const env = Object.fromEntries(Object.entries(scrubEnv(d.env, envKeys)).filter(([k]) => envKeys.includes(k)));
  const baseArgs = ['--net', b.profile, ...(b.as ? ['--as', b.as] : [])];
  const configPath = vecnetConfigPath(env);
  let running = false, rpc: CodexRpc | undefined, watch: ChildProcess | undefined;
  let connectionState: ReturnType<Engine['state']> = 'connecting';
  let status: unknown = { type: 'notLoaded' }, attached = false, archived = false, overLimit = false;
  // createdAt changes on a fresh bind, but survives TTL/attendance renewal.
  // Keep refusals scoped to this Codex home/thread and binding generation.
  const identityKey = 'identity_changed:' + JSON.stringify([b.codexHome, b.threadId]);
  const generation = JSON.stringify([b.createdAt, b.profile, b.as ?? null, b.hub, b.caller, b.origin]);
  const refusedGeneration = kv.get(identityKey);
  if (refusedGeneration !== undefined && refusedGeneration !== generation) kv.delete(identityKey);
  let policyDenied = false, identityChanged = refusedGeneration === generation, lastActivity = clock.now(), lastEnsure = -Infinity;
  let lastPush = -Infinity, retryIndex = 0, closedEpoch = 0, resumeAt = -Infinity, attaching = false;
  let cold = false, rejectedResume = false, independentResumeActivity = false, queue: Promise<void> = Promise.resolve();
  let debounce: { cancel(): void } | undefined, gap: { cancel(): void } | undefined;
  let reconnect: { cancel(): void } | undefined, minute: { cancel(): void } | undefined;
  let claimRecovery: { cancel(): void } | undefined, warmCheck: { cancel(): void } | undefined;
  let historyRetry: { cancel(): void } | undefined, historyRetryIndex = 0;
  const children = new Set<ChildProcess>();
  const unresolvedLogged = new Set<string>(), pendingLogged = new Set<string>();
  const receipts = new Set<string>(), waitingRetry = new Map<string, Batch>(), waitForIdle = new Set<string>();
  const blockedRequests = new Map<string | number, { method: string; at: number; logged: boolean }>();
  let waitingSince: number | undefined, blockedLogged = false;

  function enqueue(fn: () => Promise<void>) {
    queue = queue.then(async () => { if (running) await fn(); }).catch(() => { log.warn('codex_engine_operation_failed', { threadId: b.threadId }); });
  }
  function rows(): InflightRow[] {
    return kv.keys('inflight:').map(key => JSON.parse(kv.get(key)!) as InflightRow).filter(row => row.threadId === b.threadId);
  }
  const write = (r: InflightRow) => kv.set('inflight:' + r.key, JSON.stringify(r));
  function clear(r: InflightRow) { kv.delete('inflight:' + r.key); kv.delete('releasing:' + r.claim); kv.delete('diagnostic:unresolved:' + r.key); waitingRetry.delete(r.key); waitForIdle.delete(r.key); receipts.delete(r.key); unresolvedLogged.delete(r.key); pendingLogged.delete(r.key); }
  function reason(requireSubscription = true): DormantReason | undefined {
    if (identityChanged) return 'identity_changed';
    if (clock.now() >= b.expiresAt) return 'expired';
    if (archived) return 'archived';
    if (overLimit) return 'over_limit';
    if (policyDenied) return 'approval_policy';
    const s = mapStatus(status);
    if (cold || s === 'not_loaded' || s === 'unknown' || !running || !rpc) return 'not_loaded';
    if (s === 'idle' && b.idleDormantMs !== undefined && clock.now() - lastActivity >= b.idleDormantMs) return 'idle_stale';
    if (s === 'waiting_approval' || s === 'waiting_input') return s;
    if (requireSubscription && !attached) return 'not_loaded';
    return undefined;
  }
  function liveness(): Liveness { const r = reason(); return r ? { live: false, reason: r } : { live: true }; }
  async function call<T = RecordValue>(method: Parameters<CodexRpc['call']>[0], params: unknown): Promise<T> {
    if (!rpc) throw new CodexRpcError(-1, 'unreachable', true);
    return rpc.call<T>(method, params, timeout);
  }
  function runHook(args: string[], cleanup = false): Promise<unknown> {
    if (!running && !cleanup) return Promise.reject(new Error('engine_stopped'));
    return new Promise((resolve, reject) => {
      const child = spawn(d.vecnetBin, [...baseArgs, 'hook', ...args], { env, stdio: ['ignore', 'pipe', 'ignore'] });
      children.add(child); let text = '', tooLarge = false;
      child.stdout!.setEncoding('utf8');
      child.stdout!.on('data', (part: string) => { text += part; if (Buffer.byteLength(text) > 1_048_576) { tooLarge = true; child.kill(); } });
      const timer = clock.timer(timeout, () => child.kill());
      child.once('error', () => { timer.cancel(); children.delete(child); reject(new Error('hook_failed')); });
      child.once('close', code => {
        timer.cancel(); children.delete(child);
        if (code !== 0 || tooLarge) { reject(new Error('hook_failed')); return; }
        try { resolve(JSON.parse(text)); } catch { reject(new Error('hook_invalid_output')); }
      });
    });
  }
  async function release(claim: string) {
    // Releasing known-unsent claims must remain possible while stop drains
    // the delivery lane, after new claims and turn RPCs have been disabled.
    await runHook(['--release', '--claim', claim], true);
  }
  function scheduleClaimRecovery() {
    if (!running || claimRecovery) return;
    claimRecovery = clock.timer(1000, () => {
      claimRecovery = undefined;
      enqueue(async () => { await recoverClaims(); arm(0); });
    });
  }
  async function releaseUnsentClaim(claim: string) {
    try { await release(claim); kv.delete('claiming:' + claim); }
    catch { log.warn('codex_claim_release_failed', { threadId: b.threadId }); scheduleClaimRecovery(); }
  }
  async function releaseInflight(r: InflightRow) {
    // A definite rejection (or proven absence) needs cleanup, not another
    // history scan. Persist that fact before invoking the fallible child.
    kv.set('releasing:' + r.claim, JSON.stringify(r));
    waitingRetry.delete(r.key); waitForIdle.delete(r.key);
    try { await release(r.claim); clear(r); }
    catch { log.warn('codex_claim_release_failed', { threadId: b.threadId }); scheduleClaimRecovery(); }
  }
  async function releaseDormantRetries() {
    const dormant = reason(false);
    if (!dormant || dormant === 'waiting_approval' || dormant === 'waiting_input') return;
    // Only these batches are known unsent or definitely rejected. Pending and
    // ambiguous attempts still require receipt/checkpoint recovery while dormant.
    for (const r of rows()) if (waitingRetry.has(r.key)) await releaseInflight(r);
  }
  async function recoverClaims() {
    for (const key of kv.keys('releasing:')) {
      const r = JSON.parse(kv.get(key)!) as InflightRow;
      if (r.threadId === b.threadId) await releaseInflight(r);
    }
    const owned = new Set(rows().map(r => r.claim));
    for (const key of kv.keys('claiming:')) {
      const r = obj(JSON.parse(kv.get(key)!)); if (r.threadId !== b.threadId) continue;
      const claim = key.slice('claiming:'.length);
      if (owned.has(claim)) kv.delete(key);
      else await releaseUnsentClaim(claim);
    }
  }
  async function unsubscribe() {
    if (!attached && !attaching) return;
    attached = false; attaching = false;
    try { await call('thread/unsubscribe', buildThreadUnsubscribe(b.threadId)); }
    catch { const previous = rpc; rpc = undefined; previous?.close(); retry(); }
  }
  function retry() {
    if (!running || reconnect || connectionState === 'refused_version') return;
    const ms = [1000, 2000, 4000, 8000, 15000][Math.min(retryIndex++, 4)];
    reconnect = clock.timer(ms, () => { reconnect = undefined; enqueue(connect); });
  }
  function disconnected(connection: CodexRpc) {
    if (rpc !== connection) return;
    rpc = undefined; attached = false; attaching = false; connectionState = 'unreachable'; retry();
  }
  function arm(ms = d.debounceMs ?? 500) {
    if (!running) return;
    debounce?.cancel();
    debounce = clock.timer(ms, () => { debounce = undefined; enqueue(async () => { await reconcile(); await push(); }); });
  }
  function scheduleHistoryRetry() {
    if (!running || historyRetry) return;
    const ms = [1000, 2000, 4000, 8000, 15000][Math.min(historyRetryIndex++, 4)];
    historyRetry = clock.timer(ms, () => { historyRetry = undefined; enqueue(push); });
  }
  function scheduleWarmCheck(ms = Math.max(1, resumeAt + 2001 - clock.now())) {
    if (!running || warmCheck) return;
    warmCheck = clock.timer(ms, () => {
      warmCheck = undefined;
      enqueue(async () => {
        if (!rpc || !cold || rejectedResume) return;
        if (clock.now() - resumeAt <= 2000) { scheduleWarmCheck(); return; }
        const epoch = closedEpoch;
        try {
          const read = await call('thread/read', { threadId: b.threadId });
          if (closedEpoch !== epoch || rejectedResume) return;
          const next = obj(read.thread).status, s = mapStatus(next);
          if (s !== 'not_loaded' && s !== 'unknown') cold = false;
          updateStatus(next); archived = obj(read.thread).archived === true;
          await reconcile(); arm(0);
        } catch { scheduleWarmCheck(1000); }
      });
    });
  }
  function updateStatus(value: unknown) {
    const before = mapStatus(status); status = value; lastActivity = clock.now();
    const s = mapStatus(status);
    if (s === 'not_loaded') {
      closedEpoch++; attached = false; independentResumeActivity = false;
      if (!attaching) rejectedResume = false;
    }
    // Retain independent activity through the entire resume guard, including
    // after unsubscribe. A delayed startup must not erase this evidence, and
    // a cold resume's own idle broadcast must never supply it.
    if (s === 'turn_running' || s === 'waiting_approval' || s === 'waiting_input') independentResumeActivity = true;
    if (!attaching && s !== 'not_loaded' && s !== 'unknown') {
      // A later external unload/load cycle invalidates the old policy verdict.
      // Resume still checks the current policy before allowing any delivery.
      if (before === 'not_loaded') policyDenied = false;
      // A cold resume can emit its own idle broadcast even after the guard.
      // Active status (including waiting flags), or turn/started mapped to
      // active, proves independent activity and permits a fresh warm attach.
      if (s === 'turn_running' || s === 'waiting_approval' || s === 'waiting_input') rejectedResume = false;
      if (!rejectedResume) {
        if (clock.now() - resumeAt > 2000) cold = false;
        else if (cold) scheduleWarmCheck();
      }
    }
    if (s === 'waiting_approval' || s === 'waiting_input') {
      if (waitingSince === undefined) { waitingSince = clock.now(); blockedLogged = false; }
    } else { waitingSince = undefined; blockedLogged = false; }
    if (s === 'idle' || (before !== s && (s === 'turn_running'))) arm(0);
  }
  function notice(method: string, raw: unknown) {
    const p = obj(raw);
    if (method === 'vecnet/serverRequest') {
      if ((typeof p.requestId === 'string' || typeof p.requestId === 'number') && typeof p.method === 'string' && !blockedRequests.has(p.requestId)) {
        blockedRequests.set(p.requestId, { method: p.method, at: clock.now(), logged: false });
      }
      return;
    }
    if (method === 'serverRequest/resolved') blockedRequests.delete(p.requestId);
    if (p.threadId !== b.threadId && obj(p.thread).id !== b.threadId) return;
    if (method === 'thread/status/changed') updateStatus(p.status);
    if (method === 'thread/closed') { updateStatus({ type: 'notLoaded' }); cold = true; }
    if (method === 'thread/archived') archived = true;
    if (method === 'turn/started') { lastActivity = clock.now(); updateStatus({ type: 'active', activeFlags: [] }); }
    if (method === 'mcpServer/startupStatus/updated' && (attaching || clock.now() - resumeAt <= 2000)) {
      cold = true; rejectedResume = !independentResumeActivity;
      // A delayed startup notice still rejects the subscription, but must not
      // erase independent activity consumed during or after its pending resume.
      if (!rejectedResume) scheduleWarmCheck();
    }
    if ((method === 'item/started' || method === 'item/completed') && obj(p.item).type === 'userMessage' && typeof p.item.clientId === 'string') {
      const key = p.item.clientId;
      // A receipt may arrive before the RPC's acceptance response.
      if (kv.get('inflight:' + key)) {
        receipts.add(key);
        const r = JSON.parse(kv.get('inflight:' + key)!) as InflightRow;
        if (r.state !== 'sending') clear(r);
      }
    }
    enqueue(async () => {
      await reconcile();
      if (method === 'turn/completed') {
        const id = obj(p.turn).id ?? p.turnId;
        for (const r of rows()) if (!waitingRetry.has(r.key) && (r.attemptTurnId === id || r.state === 'unresolved')) await scan(r);
      }
    });
  }
  async function reconcile() {
    if (!running) return;
    await releaseDormantRetries();
    if (!rpc) return;
    let r = reason(false);
    if (r === 'not_loaded' && b.origin === 'launcher' && b.ensureLoaded && clock.now() - lastEnsure >= 60_000 &&
      mapStatus(status) === 'not_loaded') {
      lastEnsure = clock.now();
      if (await b.ensureLoaded()) {
        const read = await call('thread/read', { threadId: b.threadId });
        // The launcher owns loading; this fresh read establishes a new warm
        // status checkpoint even when an earlier subscription was cold.
        if (mapStatus(obj(read.thread).status) !== 'not_loaded' && mapStatus(obj(read.thread).status) !== 'unknown') {
          cold = false; rejectedResume = false;
        }
        updateStatus(obj(read.thread).status); archived = obj(read.thread).archived === true;
      }
      r = reason(false);
    }
    if (r && r !== 'waiting_approval' && r !== 'waiting_input') { await unsubscribe(); return; }
    if (!attached) {
      const s = mapStatus(status);
      if (!['idle', 'turn_running', 'waiting_approval', 'waiting_input'].includes(s)) return;
      const epoch = closedEpoch; attaching = true; independentResumeActivity = false;
      try {
        const result = await call('thread/resume', buildThreadResume(b.threadId));
        resumeAt = clock.now(); attached = true; attaching = false;
        if (closedEpoch !== epoch || cold) {
          cold = true; rejectedResume = !independentResumeActivity;
          await unsubscribe();
          // Re-read only after the rejected resume's guard. Independent active
          // status/turn activity permits a fresh warm attach, even if no later
          // broadcast arrives; the cold resume's own idle never permits it.
          if (!rejectedResume) scheduleWarmCheck();
          return;
        }
        if (parseResumePolicy(result).asks && !b.attended) { policyDenied = true; await unsubscribe(); return; }
      } catch (e) {
        attaching = false;
        if (e instanceof CodexRpcError && /thread (?:is )?closing|thread not found/.test(e.message)) { cold = true; status = { type: 'notLoaded' }; }
        else throw e;
      }
    }
    if (cold) await unsubscribe();
    if (waitingSince !== undefined && clock.now() - waitingSince >= blockedMs) {
      if (!blockedRequests.size && !blockedLogged) {
        blockedLogged = true; log.warn('codex_binding_blocked', { threadId: b.threadId, method: 'unknown' });
      }
      for (const request of blockedRequests.values()) if (!request.logged && clock.now() - request.at >= blockedMs) {
        request.logged = true; log.warn('codex_binding_blocked', { threadId: b.threadId, method: request.method });
      }
    }
    for (const row of rows()) if (row.acceptedAt !== null && clock.now() - row.acceptedAt >= blockedMs && !pendingLogged.has(row.key)) {
      pendingLogged.add(row.key); log.warn('codex_receipt_pending', { threadId: b.threadId });
    }
  }
  async function latest(): Promise<RecordValue | undefined> {
    const result = await call('thread/turns/list', { threadId: b.threadId, limit: 1 });
    return Array.isArray(result.data) ? result.data[0] : undefined;
  }
  async function scan(r: InflightRow) {
    if (!kv.get('inflight:' + r.key) || kv.get('releasing:' + r.claim)) return;
    let cursor: string | undefined, reached = false, inProgress = false;
    try {
      for (let page = 0; page < 20; page++) {
        const result = await call('thread/turns/list', buildTurnsPage(b.threadId, cursor));
        // A subscription receipt can settle this row while the scan awaits I/O.
        if (!kv.get('inflight:' + r.key) || receipts.has(r.key)) { clear(r); return; }
        if (!Array.isArray(result.data)) break;
        for (const turn of result.data) {
          if (Array.isArray(turn.items) && turn.items.some((item: RecordValue) => item.type === 'userMessage' && item.clientId === r.key)) { clear(r); return; }
          // A start can race into the turn returned by latest(). Include that
          // prior turn: an accepted start/steer may still record its item there.
          if (turn.status === 'inProgress') inProgress = true;
          if (!r.attemptTurnId && r.priorTurnId !== null && turn.id === r.priorTurnId) { reached = true; break; }
          if (r.attemptTurnId !== null && turn.id === r.attemptTurnId) { reached = true; break; }
        }
        if (reached) break;
        if (result.nextCursor === null || result.nextCursor === undefined) {
          reached = r.attemptTurnId === null && r.priorTurnId === null; break;
        }
        if (typeof result.nextCursor !== 'string') break;
        cursor = result.nextCursor;
      }
    } catch { /* A partial scan never proves absence. */ }
    if (!kv.get('inflight:' + r.key) || receipts.has(r.key)) { clear(r); return; }
    if (reached && !inProgress) { await releaseInflight(r); return; }
    r.state = 'unresolved'; write(r);
    if (!unresolvedLogged.has(r.key) && !kv.get('diagnostic:unresolved:' + r.key)) {
      unresolvedLogged.add(r.key); kv.set('diagnostic:unresolved:' + r.key, '1'); log.warn('codex_delivery_unresolved', { threadId: b.threadId });
    }
  }
  async function recover() {
    await recoverClaims();
    for (const r of rows()) if (!waitingRetry.has(r.key)) await scan(r);
  }
  async function connect() {
    if (!running || rpc) return;
    connectionState = 'connecting'; policyDenied = false; cold = rejectedResume;
    try {
      const connection = await d.openRpc({ codexHome: b.codexHome, euid: d.euid, log, timeoutMs: timeout });
      if (!running) { connection.close(); return; }
      rpc = connection;
      connection.onClose(() => disconnected(connection));
      connection.onNotification(notice);
      const read = await call('thread/read', { threadId: b.threadId });
      updateStatus(obj(read.thread).status); archived = obj(read.thread).archived === true;
      const turn = await latest();
      const at = turn?.completedAt ?? turn?.startedAt;
      if (typeof at === 'number' && Number.isFinite(at)) lastActivity = at * 1000;
      else if (typeof at === 'string' && Number.isFinite(Date.parse(at))) lastActivity = Date.parse(at);
      await reconcile(); await recover();
      if (!rpc) return;
      connectionState = 'ready'; retryIndex = 0; arm(0);
    } catch (e) {
      const connection = rpc; rpc = undefined; attached = false; attaching = false; connection?.close();
      connectionState = e instanceof CodexRpcError && e.message === 'refused_version' ? 'refused_version' : 'unreachable';
      retry();
    }
  }
  async function transmit(r: InflightRow, batch: Batch) {
    let mismatches = 0, fallback = false;
    while (running && rpc) {
      if (!liveness().live) {
        waitingRetry.set(r.key, batch);
        // An idle claim can contain wake_idle. Deferral must preserve its
        // eligibility even if a waiting flag subsequently clears to active.
        if (r.method === 'turn/start') waitForIdle.add(r.key);
        await releaseDormantRetries(); return;
      }
      const entries = fallback ? [] : batch.entries;
      const pointer = `vecnet: messages remain available. Run vecnet --net ${b.profile}${b.as ? ' --as ' + b.as : ''} inbox.`;
      const input = { threadId: b.threadId, stub: fallback ? pointer : batch.stub, entries, key: r.key };
      write(r);
      try {
        lastPush = clock.now();
        const result = r.method === 'turn/steer' ? await call('turn/steer', buildTurnSteer({ ...input, expectedTurnId: r.attemptTurnId! })) :
          await call('turn/start', buildTurnStart(input));
        r.acceptedAt = clock.now(); r.state = 'pending';
        if (r.method === 'turn/start') {
          const id = obj(result.turn).id;
          if (typeof id === 'string') {
            if (id === r.priorTurnId) log.warn('codex_start_steered', { threadId: b.threadId });
            r.attemptTurnId = id;
          }
        }
        waitingRetry.delete(r.key);
        if (receipts.has(r.key)) clear(r); else write(r);
        return;
      } catch (e) {
        if (!(e instanceof CodexRpcError) || e.ambiguous) {
          // This attempt may have been accepted. Only checkpoint recovery may
          // settle it; a previous definite-rejection retry is no longer safe.
          waitingRetry.delete(r.key); waitForIdle.delete(r.key);
          r.state = 'unresolved'; write(r);
          const connection = rpc; rpc = undefined; attached = false; connection?.close(); connectionState = 'unreachable'; retry(); return;
        }
        if (r.method === 'turn/steer' && e.code === -32600 && /mismatch|expectedTurnId|expected\s+active\s+turn\s+id\b/i.test(e.message)) {
          if (mismatches++ === 0) {
            try {
              const turn = await latest();
              if (turn?.status === 'inProgress' && typeof turn.id === 'string') { r.attemptTurnId = turn.id; continue; }
            } catch { /* The steer was rejected; retain this batch for idle. */ }
          }
          waitingRetry.set(r.key, batch); waitForIdle.add(r.key); await releaseDormantRetries(); return;
        }
        if (/thread (?:is )?closing|thread not found/.test(e.message)) {
          cold = true; status = { type: 'notLoaded' }; closedEpoch++;
          await unsubscribe(); await releaseInflight(r); return;
        }
        if (!fallback && /additionalContext/.test(e.message)) { fallback = true; continue; }
        await releaseInflight(r); return;
      }
    }
    // The hook or a definite-rejection retry may finish after stop disabled
    // this lane. No ambiguous or accepted attempt falls through to here.
    waitingRetry.set(r.key, batch);
  }
  async function push() {
    if (connectionState !== 'ready' || !liveness().live) return;
    const wait = Math.max(lastPush + (d.minGapMs ?? 5000), resumeAt + 2000) - clock.now();
    if (wait > 0) {
      if (!gap) gap = clock.timer(wait, () => { gap = undefined; enqueue(push); });
      return;
    }
    for (const r of rows()) {
      const batch = waitingRetry.get(r.key);
      if (batch) {
        const state = mapStatus(status);
        if (waitForIdle.has(r.key) && state !== 'idle') return;
        let turn: RecordValue | undefined;
        try { turn = await latest(); }
        catch {
          // This batch is unsent or definitely rejected. Keep its claim and
          // key, and retry the checkpoint read even if no new event arrives.
          log.warn('codex_engine_operation_failed', { threadId: b.threadId });
          scheduleHistoryRetry(); return;
        }
        historyRetry?.cancel(); historyRetry = undefined; historyRetryIndex = 0;
        if (!liveness().live) return;
        if (state === 'turn_running' && turn?.status !== 'inProgress') return;
        r.method = state === 'idle' ? 'turn/start' : 'turn/steer';
        r.attemptTurnId = state === 'idle' ? null : turn!.id; r.priorTurnId = state === 'idle' ? turn?.id ?? null : null;
        await transmit(r, batch); return;
      }
    }
    const s = mapStatus(status);
    if (s !== 'idle' && s !== 'turn_running') return;
    const action = s === 'idle' ? 'start' : 'steer';
    const priorities = (['wake', 'wake_idle'] as const).filter(p => planInput(p, s, { steer: true, queue: 'launcher' }) === action);
    let turn: RecordValue | undefined;
    try { turn = await latest(); }
    catch {
      // No claim exists yet. Retry independently so the original wake stays
      // eligible even when the watch and thread status remain quiet.
      log.warn('codex_engine_operation_failed', { threadId: b.threadId });
      scheduleHistoryRetry(); return;
    }
    historyRetry?.cancel(); historyRetry = undefined; historyRetryIndex = 0;
    if (!liveness().live || (action === 'steer' && (turn?.status !== 'inProgress' || typeof turn.id !== 'string'))) return;
    const nonce = randomBytes(12).toString('hex');
    kv.set('claiming:' + nonce, JSON.stringify({ threadId: b.threadId, at: clock.now() }));
    let output: RecordValue;
    try {
      output = obj(await runHook(['--event', 'app-server', '--claim', nonce, '--priorities', priorities.join(','), '--expect-hub', b.hub, '--expect-caller', b.caller]));
    } catch {
      // Go may have committed the claim before its output was lost. No turn
      // RPC can have happened yet, so this exact nonce is safe to release.
      log.warn('codex_hook_failed', { threadId: b.threadId });
      await releaseUnsentClaim(nonce); arm(1000); return;
    }
    if (output.refused === 'identity_changed') {
      kv.set(identityKey, generation); identityChanged = true;
      kv.delete('claiming:' + nonce); await unsubscribe();
      log.warn('codex_binding_identity_changed', { threadId: b.threadId }); return;
    }
    if (!Array.isArray(output.deliveries) || !output.deliveries.every((id: unknown) => typeof id === 'string') ||
      !Array.isArray(output.entries ?? []) || (output.entries ?? []).some((e: unknown) => typeof obj(e).key !== 'string' || typeof obj(e).value !== 'string' || Buffer.byteLength(obj(e).value) > 1000)) {
      await releaseUnsentClaim(nonce); return;
    }
    const batch: Batch = { deliveries: output.deliveries, stub: typeof output.stub === 'string' ? output.stub : '', entries: output.entries ?? [] };
    if (!batch.deliveries.length && !batch.entries.some(e => e.key === 'vecnet_summary')) { kv.delete('claiming:' + nonce); return; }
    const key = 'vn.' + createHash('sha256').update(JSON.stringify([b.hub, b.threadId, [...batch.deliveries].sort()])).digest('hex').slice(0, 24);
    const row: InflightRow = { threadId: b.threadId, key, claim: nonce, deliveries: batch.deliveries, method: action === 'start' ? 'turn/start' : 'turn/steer',
      attemptTurnId: action === 'steer' ? turn!.id : null, priorTurnId: action === 'start' ? turn?.id ?? null : null, acceptedAt: null, state: 'sending' };
    // Do not overwrite an unresolved attempt if a summary produces its key again.
    if (kv.get('inflight:' + key)) { await releaseUnsentClaim(nonce); return; }
    write(row); kv.delete('claiming:' + nonce); await transmit(row, batch);
  }
  function configSignature(): string {
    if (configPath === undefined) return 'unlocatable';
    try { const st = statSync(configPath); return [st.dev, st.ino, st.size, st.mtimeMs, st.ctimeMs].join(':'); }
    catch { return 'missing'; }
  }
  function startWatch() {
    if (!running) return;
    const child = spawn(d.vecnetBin, [...baseArgs, 'watch', '--json'], { env, stdio: ['ignore', 'pipe', 'ignore'] });
    watch = child;
    // Set from the watch's own last line: it hit 401/403/204 (or another
    // terminal 4xx) and will not reconnect (PROTOCOL §13.1).
    let stoppedStatus: number | undefined;
    const reader = createInterface({ input: child.stdout! });
    reader.on('line', line => {
      if (Buffer.byteLength(line) > 1_048_576) return;
      try {
        const event = obj(JSON.parse(line)), p = obj(obj(event.data).message).priority;
        if (event.type === 'delivery' && (p === 'wake' || p === 'wake_idle')) arm();
        const status = obj(event.data).status;
        if (event.type === 'watch_stopped' && event.cursor === '' && Number.isInteger(status) && status >= 200 && status < 500) stoppedStatus = status;
      } catch { /* The watch stream is a trigger, never turn input. */ }
    });
    child.on('error', () => { log.warn('codex_watch_unreachable', { threadId: b.threadId }); });
    // 'close' follows the end of stdout, so the stop line has been read.
    child.once('close', () => {
      reader.close();
      if (watch === child) watch = undefined;
      if (!running) return;
      if (stoppedStatus !== undefined) {
        // Respawning would only repeat the refused request every second.
        // Resume on a credential/config change or a binding renewal.
        watchStopped = { config: configSignature(), expiresAt: b.expiresAt };
        log.warn('codex_watch_stopped', { threadId: b.threadId, status: stoppedStatus });
        return;
      }
      const timer = clock.timer(1000, () => { watchRetry = undefined; startWatch(); }); watchRetry = timer;
    });
  }
  let watchRetry: { cancel(): void } | undefined;
  let watchStopped: { config: string; expiresAt: number } | undefined;
  function resumeWatch() {
    if (!running || !watchStopped || watch || watchRetry) return;
    if (configSignature() === watchStopped.config && b.expiresAt === watchStopped.expiresAt) return;
    watchStopped = undefined;
    log.info('codex_watch_resumed', { threadId: b.threadId });
    startWatch();
  }
  function heartbeat() {
    minute = clock.timer(60_000, () => { enqueue(reconcile); resumeWatch(); if (running) heartbeat(); });
  }
  return {
    start() { if (running) { enqueue(async () => { await recover(); arm(0); }); return; }
      running = true; reconnect = undefined; gap = undefined; claimRecovery = undefined; warmCheck = undefined; historyRetry = undefined; historyRetryIndex = 0; watchStopped = undefined; startWatch(); heartbeat();
      // Unsent claims need only Go, including when Codex is unreachable or
      // refused by the version gate. Release them before any RPC recovery.
      enqueue(async () => { await recoverClaims(); await connect(); }); },
    async stop() {
      if (!running) return;
      running = false; debounce?.cancel(); gap?.cancel(); reconnect?.cancel(); minute?.cancel(); watchRetry?.cancel(); claimRecovery?.cancel(); warmCheck?.cancel(); historyRetry?.cancel();
      const child = watch; watch = undefined;
      const exits = [...children, ...(child ? [child] : [])].map(c => new Promise<void>(resolve => {
        if (c.exitCode !== null || c.signalCode !== null) { resolve(); return; }
        c.once('close', () => resolve()); c.kill();
      }));
      await queue; await Promise.all(exits);
      // Drain the killed claim before releasing its nonce: Go may still be
      // committing it. Keep accepted/ambiguous inflight rows for recovery.
      await recoverClaims();
      for (const r of rows()) if (waitingRetry.has(r.key)) await releaseInflight(r);
      await unsubscribe(); const connection = rpc; rpc = undefined; connection?.close();
    },
    state: () => connectionState, liveness, subscribed: () => attached,
    setOverLimit(value) { overLimit = value; enqueue(async () => { await reconcile(); if (!value) arm(0); }); },
  };
}
