import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { cliEnvironment, commandInvocation, terminateProcess } from '../../../hub-client/src/platform.ts';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { KeyValue } from '../backend.ts';
import { spawnClaudeSnapshot } from './claude-status.ts';
import type { ClaudeSnapshot, ClaudeSnapshotBinding } from './claude-status.ts';
import type { HostedAdapter } from './host.ts';
import { spawnWatchChild } from './watch-pool.ts';
import type { WatchPool, WatchRequest } from './watch-pool.ts';

// Task 7 (claude delivery chain): the hosted native pointer-delivery adapter.
// Notification-driven only: a pooled watch WakeEvent asks for one bounded,
// coalesced catch-up pass. Every pass re-reads the private runtime snapshot,
// recovers the durable claim ledger, then stages a bounded ordered window of
// inbox candidates per admitted scope. A wake candidate is committed
// `reserved` then `attempting` in the canonical per-user kv BEFORE the single
// bounded transport invocation; the helper's exit contract decides the
// disposition (0 write-completed — never presentation proof, 2/3 refused,
// 4 uncertain). Nothing here ever acknowledges the hub: observed dispositions
// and the hub-acked position stay separate, tombstones survive reconcile, and
// `hold`/`wake_idle` never enter a native batch.

/** Spec: start with 128 unresolved native reservations per scope; a reached
 * cap blocks additional native sends, not explicit inbox reads. */
export const CLAUDE_PENDING_LIMIT = 128;
const CLAIM_PREFIX = 'claude-claims:claim:', SCOPE_PREFIX = 'claude-claims:scope:';
const RELOAD_MS = 10_000, CHILD_TIMEOUT = 5000, FETCH_TIMEOUT = 8000, STAGE_BATCH = 32;
const BACKOFF = [1000, 2000, 4000, 8000, 15_000];
const KEY = /^[0-9a-f]{64}$/, GENERATION = /^[0-9a-f]{32}$/, CURSOR = /^.{1,256}$/, HUB_ID = /^[\x21-\x7e]{1,256}$/, DELIVERY = /^[A-Za-z0-9_-]{1,128}$/, NATIVE = /^[A-Za-z0-9._:-]{1,128}$/, THREAD = /^thr_[A-HJKMNP-TV-Z0-9]{26}$/, MESSAGE = /^msg_[A-HJKMNP-TV-Z0-9]{26}$/;
/** Known message kinds. Only `message` is ever a native candidate; the others
 * are known non-instructions and are staged past, while an unlisted kind
 * stalls the window (fail closed). */
const MESSAGE_KINDS = ['message', 'approval_request', 'approval_response', 'job_status', 'reaction'] as const;
/** Known stream event types (the envelope's closed set). Only `delivery`
 * carries a native candidate; every other known type advances the window,
 * while an unlisted type stalls it. */
const EVENT_TYPES = ['delivery', 'receipt', 'presence', 'thread_updated', 'directory_updated', 'approval_expired'] as const;
const PRIORITIES = ['wake', 'wake_idle', 'hold'] as const;
const STATES = ['reserved', 'attempting', 'held', 'refused', 'expired', 'uncertain', 'released'] as const;

export type ClaudeClaimState = typeof STATES[number];
/** The Go helper's private exit contract: 0 confirmed full write (never proof
 * of presentation), 2 usage/validation, 3 connect refused or timeout,
 * 4 write failure or partial write (bytes may have left). */
export type DeliverExit = 0 | 2 | 3 | 4;

/** Durable claim record under `claude-claims:claim:<deliveryId>`: opaque ids,
 * cursors, states and bounded timestamps only — no body, no credentials, no
 * raw path tokens. `disposition: attempted` records a completed write and is
 * deliberately never a positive presentation proof. */
export interface ClaudeClaimRecord {
  v: 1;
  deliveryId: string;
  messageId: string;
  threadId: string;
  /** Opaque native correlation id minted per attempt by the adapter. */
  msgId: string;
  /** The admitted binding key (opaque recipient scope hash). */
  scope: string;
  generation: string;
  cursor: string;
  predecessor: string;
  priority: 'wake' | 'wake_idle' | 'hold';
  state: ClaudeClaimState;
  attemptId: string;
  disposition?: 'attempted' | 'held' | 'refused' | 'uncertain' | 'released';
  dispositionReason?: string;
  /** Never inferred by this adapter; only an independently confirmed hub ack
   * observed by a future surface may set it. */
  hubAckObserved?: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface ClaudeDeliveryFrame {
  bindingKey: string;
  generation: string;
  msgId: string;
  threadId: string;
  messageId: string;
}

export interface ClaudeDeliveryScopeCoordinates { profile: string; as?: string; hubEndpoint: string; caller: string; recipient: string }

/** One staged inbox event in the adapter's post-parse shape. */
export interface ClaudeDeliveryCandidate {
  cursor: string;
  type: string;
  deliveryId: string;
  messageId: string;
  threadId: string;
  priority: string;
  kind: string;
}
export interface ClaudeCandidatePage { events: ClaudeDeliveryCandidate[] }

export interface ClaudeDeliveryAdapterDeps {
  pool: WatchPool;
  clock: Clock;
  log: Log;
  kv: KeyValue;
  env: NodeJS.ProcessEnv;
  transport?: (frame: ClaudeDeliveryFrame) => Promise<DeliverExit>;
  fetchSnapshot?: () => Promise<ClaudeSnapshot | undefined>;
  fetchCandidates?: (coordinates: ClaudeDeliveryScopeCoordinates, cursor: string | undefined) => Promise<ClaudeCandidatePage | undefined>;
}

/** The explicit experimental opt-in for the pointer-only canary. `claude
 * enable` alone can never turn the gated adapter on. */
export function claudeDeliveryCanaryEnabled(env: NodeJS.ProcessEnv): boolean {
  return env.VECNET_CLAUDE_DELIVERY_CANARY === '1';
}

/** The host's inbox read: bounded, strictly `--since` the durable staging
 * cursor, and never `--ack` — hub read/accepted/processed semantics stay
 * untouched and the adapter never claims cursor ownership. */
export function claudeInboxArgs(coordinates: ClaudeDeliveryScopeCoordinates, cursor: string | undefined): string[] {
  return ['--net', coordinates.profile, ...(coordinates.as ? ['--as', coordinates.as] : []),
    'inbox', '--json', '--limit', String(STAGE_BATCH), ...(cursor ? ['--since', cursor] : [])];
}

/** The single-shot transport child: binding-key mode, so the endpoint and the
 * expected kernel peer identity are resolved from the live registry inside
 * the helper — connect, credential check and write stay one bounded unit. */
export function claudeDeliverArgs(frame: ClaudeDeliveryFrame): string[] {
  return ['claude', 'deliver', '--binding-key', frame.bindingKey, '--expect-generation', frame.generation,
    '--msg-id', frame.msgId, '--thread-id', frame.threadId, '--message-id', frame.messageId];
}

type RecordValue = Record<string, any>;
const obj = (x: unknown): RecordValue => x !== null && typeof x === 'object' && !Array.isArray(x) ? x as RecordValue : {};

function claimValid(input: unknown): ClaudeClaimRecord | undefined {
  const r = obj(input);
  if (r.v !== 1 || !DELIVERY.test(r.deliveryId ?? '') || !MESSAGE.test(r.messageId ?? '') || !THREAD.test(r.threadId ?? '') ||
    !NATIVE.test(r.msgId ?? '') || !KEY.test(r.scope ?? '') || !GENERATION.test(r.generation ?? '') ||
    typeof r.cursor !== 'string' || !CURSOR.test(r.cursor) || (r.predecessor !== undefined && typeof r.predecessor !== 'string') ||
    !PRIORITIES.includes(r.priority) || !STATES.includes(r.state) ||
    !NATIVE.test(r.attemptId ?? '') || !Number.isFinite(r.createdAt) || !Number.isFinite(r.updatedAt)) return;
  return r as ClaudeClaimRecord;
}

/** Run a promise under a clock-bounded timeout; failures resolve undefined. */
function bounded<T>(p: Promise<T>, ms: number, clock: Clock): Promise<T | undefined> {
  return new Promise(resolve => {
    const timer = clock.timer(ms, () => resolve(undefined));
    p.then(value => { timer.cancel(); resolve(value); }, () => { timer.cancel(); resolve(undefined); });
  });
}

/** The default transport: one bounded `claude deliver` child per attempt.
 * Child output is never logged. A signal, an unmapped code or a timeout is
 * uncertain (bytes may have left); a spawn failure is definitely unwritten. */
export function spawnClaudeDeliver(env: NodeJS.ProcessEnv, clock: Clock): (frame: ClaudeDeliveryFrame) => Promise<DeliverExit> {
  const childEnv = cliEnvironment(Object.fromEntries(Object.entries(env).filter((e): e is [string, string] => typeof e[1] === 'string')));
  const bin = childEnv.VECNET_BIN || 'vecnet';
  return frame => new Promise(resolve => {
    let command;
    try { command = commandInvocation([bin, ...claudeDeliverArgs(frame)], childEnv); }
    catch { resolve(3); return; }
    const child = spawn(command.file, command.args, { ...command, windowsHide: true, env: childEnv, stdio: ['ignore', 'ignore', 'ignore'] });
    let settled = false;
    const finish = (exit: DeliverExit) => { if (!settled) { settled = true; timer.cancel(); resolve(exit); } };
    const timer = clock.timer(CHILD_TIMEOUT, () => terminateProcess(child));
    child.once('error', () => finish(3)); // The child never ran: no byte could leave.
    child.once('close', code => {
      if (code === 0 || code === 2 || code === 3 || code === 4) finish(code);
      else finish(4); // Signal or unmapped death: fail closed as uncertain.
    });
  });
}

/** The default candidate source: the profile-scoped CLI inbox read. */
export function spawnClaudeInbox(env: NodeJS.ProcessEnv, clock: Clock): (coordinates: ClaudeDeliveryScopeCoordinates, cursor: string | undefined) => Promise<ClaudeCandidatePage | undefined> {
  const childEnv = cliEnvironment(Object.fromEntries(Object.entries(env).filter((e): e is [string, string] => typeof e[1] === 'string')));
  const bin = childEnv.VECNET_BIN || 'vecnet';
  return (coordinates, cursor) => new Promise(resolve => {
    let command;
    try { command = commandInvocation([bin, ...claudeInboxArgs(coordinates, cursor)], childEnv); }
    catch { resolve(undefined); return; }
    const child = spawn(command.file, command.args, { ...command, windowsHide: true, env: childEnv, stdio: ['ignore', 'pipe', 'ignore'] });
    let out = '', settled = false;
    const finish = (value: ClaudeCandidatePage | undefined) => { if (!settled) { settled = true; timer.cancel(); resolve(value); } };
    const timer = clock.timer(FETCH_TIMEOUT, () => terminateProcess(child));
    child.once('error', () => finish(undefined));
    child.stdout.setEncoding('utf8').on('data', (chunk: string) => { if (out.length < 2_097_152) out += chunk; });
    child.once('close', code => {
      if (code !== 0) { finish(undefined); return; }
      try {
        // Project the CLI's inbox rows (`data` is one hub Delivery) into the
        // adapter's flat candidate shape; staging re-validates every field.
        const parsed = obj(JSON.parse(out));
        if (!Array.isArray(parsed.events)) { finish(undefined); return; }
        const events: ClaudeDeliveryCandidate[] = parsed.events.map((raw: unknown) => {
          const event = obj(raw), delivery = obj(event.data), message = obj(delivery.message);
          return {
            cursor: typeof event.cursor === 'string' ? event.cursor : '',
            type: typeof event.type === 'string' ? event.type : '',
            deliveryId: typeof delivery.delivery_id === 'string' ? delivery.delivery_id : '',
            messageId: typeof message.message_id === 'string' ? message.message_id : '',
            threadId: typeof message.thread_id === 'string' ? message.thread_id : '',
            priority: typeof message.priority === 'string' ? message.priority : '',
            kind: typeof message.kind === 'string' ? message.kind : '',
          };
        });
        finish({ events });
      } catch { finish(undefined); }
    });
  });
}

export function createClaudeDeliveryAdapter(deps: ClaudeDeliveryAdapterDeps): HostedAdapter {
  const { clock, log, kv } = deps;
  const env = { ...deps.env };
  const fetchSnapshot = deps.fetchSnapshot ?? spawnClaudeSnapshot(env);
  const fetchCandidates = deps.fetchCandidates ?? spawnClaudeInbox(env, clock);
  const transport = deps.transport ?? spawnClaudeDeliver(env, clock);
  const watchEnv = cliEnvironment(Object.fromEntries(Object.entries(env).filter((e): e is [string, string] => typeof e[1] === 'string')));
  const watchBin = watchEnv.VECNET_BIN || 'vecnet';

  let running = false, passQueued = false, passRunning = false, failStreak = 0, scopeFailures = 0;
  let backoff: { cancel(): void } | undefined, reload: { cancel(): void } | undefined;
  let chain: Promise<void> = Promise.resolve(); // Drains in-flight work on stop.
  const chains = new Map<string, Promise<void>>(); // One attempt lane per scope.
  const releases = new Map<string, () => void>(); // Watch subscriptions by binding key.
  const watchStopped = new Map<string, string>(); // Terminal watch end, by generation.
  const inFlight = new Set<string>(); // Delivery ids attempted by this run.
  const blockedLogged = new Set<string>();

  const save = (claim: ClaudeClaimRecord) => kv.set(CLAIM_PREFIX + claim.deliveryId, JSON.stringify(claim));
  const claims = (): ClaudeClaimRecord[] => {
    const rows: ClaudeClaimRecord[] = [];
    for (const key of kv.keys(CLAIM_PREFIX)) {
      try {
        const claim = claimValid(JSON.parse(kv.get(key)!));
        if (claim) rows.push(claim);
        else log.warn('claude_delivery_claim_corrupt', { deliveryId: key.slice(CLAIM_PREFIX.length) });
      } catch { log.warn('claude_delivery_claim_corrupt', { deliveryId: key.slice(CLAIM_PREFIX.length) }); }
    }
    return rows;
  };
  const pending = (scope: string) => claims().filter(c => c.scope === scope &&
    (c.state === 'reserved' || c.state === 'attempting' || c.state === 'uncertain')).length;

  function schedulePass(): void {
    if (!running) return;
    passQueued = true;
    if (passRunning || backoff !== undefined) return; // A later pass picks it up.
    passQueued = false;
    chain = chain.then(runPass);
  }

  async function runPass(): Promise<void> {
    passRunning = true;
    scopeFailures = 0;
    let failed = false;
    try {
      const snapshot = await bounded(fetchSnapshot(), CHILD_TIMEOUT, clock);
      if (!running) return;
      if (!snapshot) throw new Error('snapshot_unavailable');
      const admitted = snapshot.bindings.filter(admittedRow);
      subscribeWatches(admitted);
      if (admitted.length) {
        const keys = new Set(admitted.map(row => row.binding_key));
        await Promise.all(admitted.map(row => enqueue(row.binding_key, () =>
          recoverScope(row, keys).then(() => stageScope(row)))));
      }
    } catch {
      failed = true;
    } finally {
      passRunning = false;
      if (failed || scopeFailures > 0) {
        failStreak++;
        log.warn('claude_delivery_reconcile_failed', { streak: failStreak });
        if (running && backoff === undefined) scheduleBackoff();
      } else failStreak = 0;
      if (running && passQueued && backoff === undefined) schedulePass();
    }
  }

  function scheduleBackoff(): void {
    const ms = BACKOFF[Math.min(failStreak - 1, BACKOFF.length - 1)];
    backoff = clock.timer(ms, () => {
      backoff = undefined;
      if (running) schedulePass();
    });
  }

  /** Admission: a live, policy-enabled, version-qualified binding carrying
   * the child-spawn coordinates. Anything else stays legacy/inbox-only. */
  function admittedRow(row: ClaudeSnapshotBinding): boolean {
    return row.state === 'registered' && row.admission !== undefined && row.admission.live &&
      row.admission.policy && row.admission.version_qualified && row.scope !== undefined &&
      HUB_ID.test(row.scope.profile) && HUB_ID.test(row.scope.hub_endpoint) &&
      HUB_ID.test(row.scope.caller) && HUB_ID.test(row.scope.recipient) &&
      (row.scope.as === undefined || HUB_ID.test(row.scope.as));
  }

  function subscribeWatches(admitted: ClaudeSnapshotBinding[]): void {
    const live = new Set(admitted.map(row => row.binding_key));
    for (const [key, release] of [...releases]) {
      if (!live.has(key)) { release(); releases.delete(key); }
    }
    for (const row of admitted) {
      if (releases.has(row.binding_key) || watchStopped.get(row.binding_key) === row.generation) continue;
      const coordinates: ClaudeDeliveryScopeCoordinates = {
        profile: row.scope!.profile, as: row.scope!.as, hubEndpoint: row.scope!.hub_endpoint,
        caller: row.scope!.caller, recipient: row.scope!.recipient,
      };
      let invocation;
      try {
        invocation = commandInvocation([watchBin, '--net', coordinates.profile,
          ...(coordinates.as ? ['--as', coordinates.as] : []), 'watch', '--json'], watchEnv);
      } catch { log.warn('claude_delivery_watch_start_failed', { scope: row.binding_key }); return; }
      // The CLI watch check proves only the pinned hub and caller, so this
      // stream stays unproved: the exclusive discriminator never widens.
      const request: WatchRequest = {
        scope: {
          hubIdentity: coordinates.hubEndpoint, endpoint: coordinates.hubEndpoint, streamOwner: coordinates.caller,
          credentialSlot: 'claude', scope: 'exclusive:claude:' + row.binding_key,
        },
        revision: row.generation,
        start: () => spawnWatchChild({ command: invocation, env: watchEnv, log, context: { scope: row.binding_key } }),
      };
      const release = deps.pool.subscribe(request, event => {
        // wake only: wake_idle stays on the legacy path, never degraded.
        if (event.priority === 'wake') schedulePass();
      }, end => {
        releases.delete(row.binding_key);
        if (!running) return;
        if (end.kind === 'terminal') {
          // The watch's own refusal: resubscribing would repeat it every
          // pass; resume only on a generation change.
          watchStopped.set(row.binding_key, row.generation);
          log.warn('claude_delivery_watch_stopped', { scope: row.binding_key, status: end.status });
          return;
        }
        clock.timer(1000, () => { if (running) schedulePass(); });
      });
      releases.set(row.binding_key, release);
    }
  }

  function enqueue(scope: string, run: () => Promise<void>): Promise<void> {
    const next = (chains.get(scope) ?? Promise.resolve()).then(run).catch(() => {
      scopeFailures++;
      log.warn('claude_delivery_scope_failed', { scope });
    });
    chains.set(scope, next);
    return next;
  }

  /** Restart recovery. A foreign `reserved` claim is proven unwritten by its
   * exact predecessor: fence it against the current admission, then either
   * reschedule it with the current owner or release it fenced. A foreign
   * `attempting` claim may have written bytes: retain and surface `uncertain`,
   * never resend. Tombstones of every other state survive untouched. */
  async function recoverScope(row: ClaudeSnapshotBinding, admitted: Set<string>): Promise<void> {
    for (const claim of claims()) {
      if (claim.scope !== row.binding_key) {
        // Another admitted scope recovers its own rows in its own lane; a
        // scope that vanished from the snapshot is fenced, never resent.
        if (claim.state === 'reserved' && !admitted.has(claim.scope) && !inFlight.has(claim.deliveryId)) {
          claim.state = 'expired'; claim.disposition = 'released'; claim.dispositionReason = 'scope_absent';
          claim.updatedAt = clock.now(); save(claim);
        }
        continue;
      }
      if (inFlight.has(claim.deliveryId)) continue;
      if (claim.state === 'attempting') {
        claim.state = 'uncertain'; claim.disposition = 'uncertain'; claim.dispositionReason = 'restart_may_have_written';
        claim.updatedAt = clock.now(); save(claim);
        log.warn('claude_delivery_uncertain_restart', { scope: claim.scope });
        continue;
      }
      if (claim.state === 'reserved') {
        if (claim.generation !== row.generation) {
          claim.state = 'released'; claim.disposition = 'released'; claim.dispositionReason = 'fenced_generation_changed';
          claim.updatedAt = clock.now(); save(claim);
          continue;
        }
        await attempt(row, claim); // Reschedule with the current owner.
      }
    }
  }

  /** Stage one bounded ordered window, then attempt the staged wakes in
   * cursor order. The durable checkpoint advances only past fully handled
   * events; an unknown or corrupt event stalls the window for this pass. */
  async function stageScope(row: ClaudeSnapshotBinding): Promise<void> {
    const coordinates: ClaudeDeliveryScopeCoordinates = {
      profile: row.scope!.profile, as: row.scope!.as, hubEndpoint: row.scope!.hub_endpoint,
      caller: row.scope!.caller, recipient: row.scope!.recipient,
    };
    const scopeKey = SCOPE_PREFIX + row.binding_key;
    const record = obj(JSON.parse(kv.get(scopeKey) ?? '{}'));
    const progress = {
      v: 1 as const,
      generation: typeof record.generation === 'string' ? record.generation : row.generation,
      cursor: typeof record.cursor === 'string' ? record.cursor : '',
      blockedPendingLimit: record.blockedPendingLimit === true,
      updatedAt: clock.now(),
    };
    if (progress.generation !== row.generation) { progress.generation = row.generation; progress.cursor = ''; }
    const page = await bounded(fetchCandidates(coordinates, progress.cursor || undefined), FETCH_TIMEOUT, clock);
    if (!page || !Array.isArray(page.events)) throw new Error('candidates_unavailable');
    const existing = new Set(claims().map(claim => claim.deliveryId));
    let cursor = progress.cursor;
    const staged: ClaudeClaimRecord[] = [];
    let stalled = false;
    for (const raw of page.events.slice(0, STAGE_BATCH)) {
      const event = obj(raw);
      const shape = typeof event.cursor === 'string' && CURSOR.test(event.cursor) &&
        typeof event.type === 'string' && EVENT_TYPES.includes(event.type as typeof EVENT_TYPES[number]);
      if (!shape) { stalled = true; break; }
      if (event.type !== 'delivery') { cursor = event.cursor; continue; } // Receipts and presence are not candidates.
      if (!DELIVERY.test(event.deliveryId ?? '') || !MESSAGE.test(event.messageId ?? '') || !THREAD.test(event.threadId ?? '') ||
        typeof event.kind !== 'string' || !MESSAGE_KINDS.includes(event.kind as typeof MESSAGE_KINDS[number]) ||
        typeof event.priority !== 'string' || !PRIORITIES.includes(event.priority as ClaudeClaimRecord['priority'])) { stalled = true; break; }
      if (event.kind !== 'message') { cursor = event.cursor; continue; } // Never a native instruction.
      if (existing.has(event.deliveryId)) { cursor = event.cursor; continue; }
      const predecessor = cursor;
      const now = clock.now();
      if (event.priority === 'hold') {
        // Inbox-only marker: quarantined in the window, never transported,
        // never acked, tombstone stays visible to the scope.
        save({ v: 1, deliveryId: event.deliveryId, messageId: event.messageId, threadId: event.threadId,
          msgId: randomBytes(16).toString('hex'), scope: row.binding_key, generation: row.generation,
          cursor: event.cursor, predecessor, priority: 'hold', state: 'held', attemptId: randomBytes(16).toString('hex'),
          disposition: 'held', dispositionReason: 'inbox_only', hubAckObserved: false,
          createdAt: now, updatedAt: now });
        existing.add(event.deliveryId);
        cursor = event.cursor;
        continue;
      }
      if (event.priority !== 'wake') { cursor = event.cursor; continue; } // wake_idle stays legacy.
      if (pending(row.binding_key) >= CLAUDE_PENDING_LIMIT) {
        progress.blockedPendingLimit = true;
        if (!blockedLogged.has(row.binding_key)) {
          blockedLogged.add(row.binding_key);
          log.warn('claude_delivery_blocked_pending_limit', { scope: row.binding_key, limit: CLAUDE_PENDING_LIMIT });
        }
        break; // Fail closed: the unstaged event stays reachable for later.
      }
      const claim: ClaudeClaimRecord = {
        v: 1, deliveryId: event.deliveryId, messageId: event.messageId, threadId: event.threadId,
        msgId: randomBytes(16).toString('hex'), scope: row.binding_key, generation: row.generation,
        cursor: event.cursor, predecessor, priority: 'wake', state: 'reserved', attemptId: randomBytes(16).toString('hex'),
        hubAckObserved: false, createdAt: now, updatedAt: now,
      };
      save(claim); // Durable reservation before anything else can happen.
      existing.add(claim.deliveryId);
      staged.push(claim);
      cursor = event.cursor;
    }
    if (pending(row.binding_key) < CLAUDE_PENDING_LIMIT) {
      progress.blockedPendingLimit = false;
      blockedLogged.delete(row.binding_key);
    }
    progress.cursor = cursor;
    kv.set(scopeKey, JSON.stringify(progress));
    // Fully staged work is attempted even when later staging stalled: the
    // stall stops the window, it does not strand already-reserved claims.
    for (const claim of staged) await attempt(row, claim);
    if (stalled) throw new Error('stalled_unknown_event');
  }

  /** One bounded transport attempt: commit `attempting` before any byte can
   * leave, await the transport (no guards held across it), then record the
   * disposition. Exit 0 is write-completed and never presentation proof. */
  async function attempt(row: ClaudeSnapshotBinding, claim: ClaudeClaimRecord): Promise<void> {
    if (!running) return;
    claim.attemptId = randomBytes(16).toString('hex');
    claim.msgId = randomBytes(16).toString('hex');
    claim.state = 'attempting';
    claim.updatedAt = clock.now();
    save(claim);
    inFlight.add(claim.deliveryId);
    try {
      const exit = await transport({ bindingKey: row.binding_key, generation: claim.generation,
        msgId: claim.msgId, threadId: claim.threadId, messageId: claim.messageId });
      if (exit === 0) {
        claim.state = 'released';
        claim.disposition = 'attempted';
        claim.dispositionReason = 'write_completed_no_presentation_proof';
      } else if (exit === 2 || exit === 3) {
        claim.state = 'refused';
        claim.disposition = 'refused';
        claim.dispositionReason = exit === 2 ? 'validation_refused_before_write' : 'connect_refused_or_timeout';
      } else {
        claim.state = 'uncertain';
        claim.disposition = 'uncertain';
        claim.dispositionReason = 'write_failed_or_partial';
      }
    } catch {
      claim.state = 'uncertain';
      claim.disposition = 'uncertain';
      claim.dispositionReason = 'transport_outcome_unknown';
    } finally {
      inFlight.delete(claim.deliveryId);
      claim.updatedAt = clock.now();
      save(claim);
    }
  }

  return {
    start() {
      if (running) return;
      running = true;
      schedulePass();
      const tick = () => {
        reload = running ? clock.timer(RELOAD_MS, tick) : undefined;
        schedulePass(); // Periodic snapshot refresh: generations rotate silently.
      };
      reload = clock.timer(RELOAD_MS, tick);
    },
    stop(): Promise<void> {
      if (!running) return chain;
      // Fence new work synchronously: no pass, watch or attempt may start.
      running = false;
      backoff?.cancel(); backoff = undefined;
      reload?.cancel(); reload = undefined;
      passQueued = false;
      for (const release of releases.values()) release();
      releases.clear();
      // Await every in-flight transport; dispositions settle before return.
      return chain.then(() => Promise.all([...chains.values()])).then(() => { chains.clear(); });
    },
  };
}
