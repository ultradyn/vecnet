import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { cliEnvironment, commandInvocation, terminateProcess } from '../../../hub-client/src/platform.ts';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { KeyValue } from '../backend.ts';
import { claudeConfigPathValid, spawnClaudeSnapshot } from './claude-status.ts';
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
// disposition (0 write-completed — never presentation proof, 3/5 refused,
// 4 uncertain). Nothing here ever acknowledges the hub: observed dispositions
// and the hub-acked position stay separate, tombstones survive reconcile, and
// `hold`/`wake_idle` never enter a native batch.

/** Spec: start with 128 unresolved native reservations per scope; a reached
 * cap blocks additional native sends, not explicit inbox reads. */
export const CLAUDE_PENDING_LIMIT = 128;
const CLAIM_PREFIX = 'claude-claims:claim:', SCOPE_PREFIX = 'claude-claims:scope:';
const RELOAD_MS = 10_000, CHILD_TIMEOUT = 5000, FETCH_TIMEOUT = 8000, STAGE_BATCH = 32;
/** Ack observation: one read per admitted scope with unretired claims, at
 * most every ACK_OBSERVE_MS, of an ACK_WINDOW-event window from the hub's
 * acked cursor. */
export const ACK_OBSERVE_MS = 60_000, ACK_WINDOW = 200;
/** Settled states a confirmed hub ack may retire. `uncertain` waits for an
 * operator; `reserved`/`attempting` are live. */
const RETIRABLE = new Set<ClaudeClaimState>(['released', 'refused', 'held', 'expired']);
const BACKOFF = [1000, 2000, 4000, 8000, 15_000];
const KEY = /^[0-9a-f]{64}$/, GENERATION = /^[0-9a-f]{32}$/, CURSOR = /^e\d{1,19}\.\d{1,19}$/, HUB_ID = /^[\x21-\x7e]{1,256}$/, DELIVERY = /^[A-Za-z0-9_-]{1,128}$/, NATIVE = /^[A-Za-z0-9._:-]{1,128}$/, THREAD = /^thr_[A-HJKMNP-TV-Z0-9]{26}$/, MESSAGE = /^msg_[A-HJKMNP-TV-Z0-9]{26}$/;
/** Known message kinds. Only `message` is ever a native candidate; the others
 * are known non-instructions and are staged past, while an unlisted kind
 * stalls the window (fail closed). */
const MESSAGE_KINDS = ['message', 'approval_request', 'approval_response', 'job_status', 'reaction'] as const;
/** Known stream event types (the hub log's closed set: every type any hub
 * code appends to a peer log). Only `delivery` carries a native candidate;
 * every other known type advances the window — reaction, hub_notice and
 * operator_view are peer-visible rows, and one of them ahead of a wake must
 * never wedge the scope — while an unlisted type stalls it. */
const EVENT_TYPES = ['delivery', 'receipt', 'presence', 'thread_updated', 'directory_updated', 'approval_expired', 'reaction', 'hub_notice', 'operator_view'] as const;
const PRIORITIES = ['wake', 'wake_idle', 'hold'] as const;
const STATES = ['reserved', 'attempting', 'held', 'refused', 'expired', 'uncertain', 'released'] as const;

export type ClaudeClaimState = typeof STATES[number];
/** The Go helper's private exit contract as the transport reports it:
 * 0 full write plus EOF flush (never proof of presentation), 3 connect
 * failed (zero bytes), 5 refused before any write (zero bytes), 4 write
 * failure, partial write, or any unknown outcome (bytes may have left). The
 * helper's own exit 2 is flag parsing only; for this adapter's fixed argv it
 * can only mean a fatal runtime panic, so the transport reports it as 4. */
export type DeliverExit = 0 | 3 | 4 | 5;
/** The transport's result: the helper's exit code, or the exit code plus the
 * helper's captured stderr (bounded at capture) when any was written — the
 * adapter surfaces that text, trimmed to one line, only on a pre-write
 * refusal (exit 5). It is a log-line diagnostic: never a claim-record field,
 * never the status sidecar. */
export type DeliverResult = DeliverExit | { exit: DeliverExit; stderr: string };

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

/** A scope's child-spawn coordinates. `config` is the binding's own absolute
 * config path: every scoped child (inbox, watch) runs with `VECNET_CONFIG`
 * set to it, never the daemon's default (Phase 2 task 5). */
export interface ClaudeDeliveryScopeCoordinates { profile: string; as?: string; config: string; hubEndpoint: string; caller: string; recipient: string }

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
/** One inbox page. `nextCursor` is the CLI's `next_cursor`: after a
 * `--since head` read it is the hub head the scope baselines at. */
export interface ClaudeCandidatePage {
  events: ClaudeDeliveryCandidate[];
  nextCursor?: string;
  /** The hub refused the `--since` cursor itself (purged below retention or
   * from another epoch, or malformed): the scope rebaselines at the head.
   * Every other failure is transient and keeps the stored cursor. */
  cursorRejected?: true;
}

/** One ack-observation window: the delivery ids the hub still holds unacked
 * from the scope's acked cursor. `complete` only when the window was not
 * truncated, so absence proves the hub acked (or expired) the delivery. */
export interface ClaudeAckWindow { deliveryIds: Set<string>; complete: boolean }

export interface ClaudeDeliveryAdapterDeps {
  pool: WatchPool;
  clock: Clock;
  log: Log;
  kv: KeyValue;
  env: NodeJS.ProcessEnv;
  transport?: (frame: ClaudeDeliveryFrame) => Promise<DeliverResult>;
  fetchSnapshot?: () => Promise<ClaudeSnapshot | undefined>;
  fetchCandidates?: (coordinates: ClaudeDeliveryScopeCoordinates, cursor: string | undefined) => Promise<ClaudeCandidatePage | undefined>;
  /** The read-only ack-observation read. Defaults to the pinned inbox child
   * with no `--since` (the hub's acked cursor) and a 200-event limit. */
  fetchAckWindow?: (coordinates: ClaudeDeliveryScopeCoordinates) => Promise<ClaudeAckWindow | undefined>;
  /** Process port for the scoped inbox and watch children (node's spawn
   * unless a test injects one). */
  spawnChild?: typeof spawn;
}

/** The child environment for one scope: the daemon's CLI environment with
 * `VECNET_CONFIG` pinned to the binding's own config. */
export function claudeScopeEnv(base: Record<string, string>, coordinates: ClaudeDeliveryScopeCoordinates): Record<string, string> {
  return { ...base, VECNET_CONFIG: coordinates.config };
}

/** The host kill switch for the pointer-only canary: `=0` constructs no
 * delivery adapter at all; anything else allows it. It is never an enable:
 * the durable per-profile record (`vecnet --net P claude canary enable`,
 * surfaced as `admission.canary`) is the gate, and `claude enable` alone can
 * never turn native delivery on. */
export function claudeDeliveryCanaryAllowed(env: NodeJS.ProcessEnv, log?: Log): boolean {
  const value = env.VECNET_CLAUDE_DELIVERY_CANARY;
  // Only the exact '0' is the kill switch (plan-mandated). Any other set
  // value is surfaced once per process, so a mistyped "off" or "false" is
  // never silently mistaken for a disable.
  if (log && value !== undefined && value !== '' && value !== '0' && value !== '1' && !warnedCanaryValue) {
    warnedCanaryValue = true;
    log.warn('claude_delivery_canary_env_unrecognized', { allowed: true, killSwitch: '0' });
  }
  return value !== '0';
}
let warnedCanaryValue = false;

/** The hosted delivery adapter plus its current native admissions (binding
 * key -> generation): at most one entry, empty whenever the last pass could
 * not prove a single canary scope. The status sidecar marks rows from it. */
export interface ClaudeDeliveryAdapter extends HostedAdapter {
  admitted(): ReadonlyMap<string, string>;
}

/** The host's inbox read: bounded, strictly `--since` the durable staging
 * cursor (or `head` for a first-admission baseline), never `--ack`, and `--no-display-proof` — hub read/accepted/processed
 * semantics stay untouched, the adapter never claims cursor ownership, and the
 * machine read never records legacy hook display state, so a later
 * UserPromptSubmit hook still shows what the daemon merely observed. */
export function claudeInboxArgs(coordinates: ClaudeDeliveryScopeCoordinates, cursor: string | undefined, limit = STAGE_BATCH): string[] {
  return ['--net', coordinates.profile, ...(coordinates.as ? ['--as', coordinates.as] : []),
    'inbox', '--json', '--limit', String(limit), '--no-display-proof', ...(cursor ? ['--since', cursor] : [])];
}

/** The CLI's stderr for a hub refusal of the `--since` cursor itself: a
 * purged/foreign-epoch cursor (409 `cursor_purged`, internal/httpapi
 * errors.go) or a malformed one (422 `invalid_value`, "cursor must look like
 * e<epoch>.<seq>", internal/httpapi events.go). The CLI prints a hub error
 * bare as `<code>: <message>` (hubError.format, no `vecnet: ` prefix) and
 * exits 2. The exact lines are pinned against a loopback hub by
 * internal/cli TestInboxCursorRefusalStderr. */
const CURSOR_REJECTED = /^(?:cursor_purged: |invalid_value: cursor must look like )/m;

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
 * Stdout is never read; up to 512 bytes of stderr are captured so a refusal
 * can say why, and are surfaced (trimmed to one line) only in the adapter's
 * `refused_before_write` log line — never in a claim record or the status
 * sidecar. Exit 2, a signal, an unmapped code or a timeout is uncertain (4:
 * bytes may have left). An `error` event is a definite pre-write refusal (5)
 * only when the child never spawned; Node also emits `error` for a running
 * child (a failed kill on the timeout), and that child may be mid-write, so
 * it is uncertain (4). `spawnChild` is the process port (node's spawn
 * unless a test injects one). */
export function spawnClaudeDeliver(env: NodeJS.ProcessEnv, clock: Clock, spawnChild: typeof spawn = spawn): (frame: ClaudeDeliveryFrame) => Promise<DeliverResult> {
  const childEnv = cliEnvironment(Object.fromEntries(Object.entries(env).filter((e): e is [string, string] => typeof e[1] === 'string')));
  const bin = childEnv.VECNET_BIN || 'vecnet';
  return frame => new Promise(resolve => {
    let command;
    try { command = commandInvocation([bin, ...claudeDeliverArgs(frame)], childEnv); }
    catch { resolve(5); return; }
    const child = spawnChild(command.file, command.args, { ...command, windowsHide: true, env: childEnv, stdio: ['ignore', 'ignore', 'pipe'] });
    let err = '', settled = false;
    const finish = (exit: DeliverExit) => {
      if (settled) return;
      settled = true;
      timer.cancel();
      resolve(err !== '' ? { exit, stderr: err } : exit);
    };
    const timer = clock.timer(CHILD_TIMEOUT, () => terminateProcess(child));
    child.stderr?.setEncoding('utf8').on('data', (chunk: string) => { err = (err + chunk).slice(0, 512); });
    // No pid: the child never ran and no byte could leave. A pid: it ran and
    // may still be writing, so the outcome is unknown.
    child.once('error', () => finish(child.pid === undefined ? 5 : 4));
    child.once('close', code => {
      if (code === 0 || code === 3 || code === 4 || code === 5) finish(code);
      else finish(4); // Exit 2 (fatal panic), a signal or an unmapped death: uncertain.
    });
  });
}

/** The default candidate source: the profile-scoped CLI inbox read. */
export function spawnClaudeInbox(env: NodeJS.ProcessEnv, clock: Clock, spawnChild: typeof spawn = spawn): (coordinates: ClaudeDeliveryScopeCoordinates, cursor: string | undefined, limit?: number) => Promise<ClaudeCandidatePage | undefined> {
  const baseEnv = cliEnvironment(Object.fromEntries(Object.entries(env).filter((e): e is [string, string] => typeof e[1] === 'string')));
  const bin = baseEnv.VECNET_BIN || 'vecnet';
  return (coordinates, cursor, limit) => new Promise(resolve => {
    if (!claudeConfigPathValid(coordinates.config)) { resolve(undefined); return; } // Never the daemon default.
    const childEnv = claudeScopeEnv(baseEnv, coordinates);
    let command;
    try { command = commandInvocation([bin, ...claudeInboxArgs(coordinates, cursor, limit)], childEnv); }
    catch { resolve(undefined); return; }
    const child = spawnChild(command.file, command.args, { ...command, windowsHide: true, env: childEnv, stdio: ['ignore', 'pipe', 'pipe'] });
    // stderr is read only to classify a cursor refusal; it is never logged.
    let out = '', err = '', settled = false;
    const finish = (value: ClaudeCandidatePage | undefined) => { if (!settled) { settled = true; timer.cancel(); resolve(value); } };
    const timer = clock.timer(FETCH_TIMEOUT, () => terminateProcess(child));
    child.once('error', () => finish(undefined));
    child.stdout.setEncoding('utf8').on('data', (chunk: string) => { if (out.length < 2_097_152) out += chunk; });
    child.stderr.setEncoding('utf8').on('data', (chunk: string) => { if (err.length < 4096) err += chunk; });
    child.once('close', code => {
      // A cursor refusal only means something for an explicit cursor read.
      if (code === 2 && !!cursor && cursor !== 'head' && CURSOR_REJECTED.test(err)) {
        finish({ events: [], cursorRejected: true });
        return;
      }
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
        finish(typeof parsed.next_cursor === 'string' && parsed.next_cursor !== '' ? { events, nextCursor: parsed.next_cursor } : { events });
      } catch { finish(undefined); }
    });
  });
}

type ClaimRow = { claim: ClaudeClaimRecord; raw: string };
/** One pass's in-memory claim view (see createClaudeDeliveryAdapter). */
interface ClaimIndex {
  byScope: Map<string, ClaimRow[]>;
  ids: Set<string>;
  pending(scope: string): number;
  put(claim: ClaudeClaimRecord, raw: string): void;
  remove(deliveryId: string): void;
}
/** The fence sweep's lane key: never a binding key (those are 64 hex). */
const FENCE_LANE = 'fence';

function buildIndex(rows: ClaimRow[]): ClaimIndex {
  const byScope = new Map<string, ClaimRow[]>();
  const byId = new Map<string, ClaimRow>();
  const add = (row: ClaimRow) => {
    byId.set(row.claim.deliveryId, row);
    const list = byScope.get(row.claim.scope);
    if (list) list.push(row); else byScope.set(row.claim.scope, [row]);
  };
  const drop = (deliveryId: string) => {
    const row = byId.get(deliveryId);
    if (!row) return;
    byId.delete(deliveryId);
    const list = byScope.get(row.claim.scope) ?? [];
    const at = list.indexOf(row);
    if (at >= 0) list.splice(at, 1);
    if (list.length === 0) byScope.delete(row.claim.scope);
  };
  for (const row of rows) add(row);
  const ids = new Set(byId.keys());
  return {
    byScope, ids,
    pending: scope => (byScope.get(scope) ?? []).filter(({ claim: c }) =>
      c.state === 'reserved' || c.state === 'attempting' || c.state === 'uncertain').length,
    put(claim, raw) { drop(claim.deliveryId); add({ claim: { ...claim }, raw }); ids.add(claim.deliveryId); },
    remove(deliveryId) { drop(deliveryId); ids.delete(deliveryId); },
  };
}

export function createClaudeDeliveryAdapter(deps: ClaudeDeliveryAdapterDeps): ClaudeDeliveryAdapter {
  const { clock, log, kv } = deps;
  const env = { ...deps.env };
  const fetchSnapshot = deps.fetchSnapshot ?? spawnClaudeSnapshot(env);
  const spawnChild = deps.spawnChild ?? spawn;
  const inbox = spawnClaudeInbox(env, clock, spawnChild);
  const fetchCandidates = deps.fetchCandidates ?? inbox;
  // The ack read is the same pinned inbox child (the binding's VECNET_CONFIG,
  // the invalid-config refusal): no --since, so it reads from the hub's
  // acked cursor, and never --ack.
  const fetchAckWindow = deps.fetchAckWindow ?? (async (coordinates: ClaudeDeliveryScopeCoordinates): Promise<ClaudeAckWindow | undefined> => {
    const page = await inbox(coordinates, undefined, ACK_WINDOW);
    if (!page || page.cursorRejected === true || !Array.isArray(page.events)) return undefined;
    const deliveryIds = new Set<string>();
    for (const event of page.events) {
      if (event.type !== 'delivery') continue;
      if (!DELIVERY.test(event.deliveryId)) return undefined; // An unreadable row proves nothing.
      deliveryIds.add(event.deliveryId);
    }
    return { deliveryIds, complete: page.events.length < ACK_WINDOW };
  });
  const transport = deps.transport ?? spawnClaudeDeliver(env, clock);
  const baseEnv = cliEnvironment(Object.fromEntries(Object.entries(env).filter((e): e is [string, string] => typeof e[1] === 'string')));
  const watchBin = baseEnv.VECNET_BIN || 'vecnet';

  let running = false, passQueued = false, passRunning = false, failStreak = 0, scopeFailures = 0;
  let backoff: { cancel(): void } | undefined, reload: { cancel(): void } | undefined;
  let chain: Promise<void> = Promise.resolve(); // Drains in-flight work on stop.
  const chains = new Map<string, Promise<void>>(); // One attempt lane per scope.
  const releases = new Map<string, () => void>(); // Watch subscriptions by binding key.
  const opened = new Map<string, string>(); // The generation and config each watch was opened under.
  const watchStopped = new Map<string, string>(); // Terminal watch end, by generation.
  const inFlight = new Set<string>(); // Delivery ids attempted by this run.
  const blockedLogged = new Set<string>();
  // The last pass's admission (at most one key). Replaced by every pass that
  // reads a snapshot, cleared by a pass that cannot and on stop: unknown
  // state never marks native.
  let admissions: ReadonlyMap<string, string> = new Map();
  let ambiguousLogged = false;
  /** Binding keys this adapter process has admitted AND established at a
   * head baseline on an earlier pass (in-memory only). Native pointers are
   * for arrivals while the scope is admitted and this process is running, so
   * every admission that was not admitted-and-baselined on the previous pass
   * — the first pass after construction (daemon start, restart, kill-switch
   * recovery), a canary or policy flap, a witness lapse — re-baselines at
   * the head before any transport instead of resuming a stored cursor that
   * predates the gap. A key enters only once its baseline read has persisted
   * (a failed read leaves it out, so the next pass retries the head rather
   * than replaying the gap from a stale cursor); a pass with a proven
   * snapshot removes every key it did not admit; stop clears the set. */
  const established = new Set<string>();

  /** One pass's claim view: built by a single claimRows() scan, then kept
   * current in memory by every save and delete this adapter makes, so no
   * pass rescans the kv. Index rows are never mutated in place: a change is
   * built on a copy and lands in the index only with its own durable write.
   * A foreign write (an operator resolution) is still caught — every write
   * and every retirement re-reads that one row against the exact bytes last
   * seen — and the index is then refreshed from the kv. */
  let index: ClaimIndex = buildIndex([]);
  const save = (claim: ClaudeClaimRecord) => {
    const raw = JSON.stringify(claim);
    kv.set(CLAIM_PREFIX + claim.deliveryId, raw);
    index.put(claim, raw);
    return raw;
  };
  /** Reload one row into the index from the kv (or drop it when absent or
   * unreadable): the durable row, never the stale in-memory copy, wins. */
  const refresh = (deliveryId: string, current: string | undefined) => {
    let claim: ClaudeClaimRecord | undefined;
    try { claim = current === undefined ? undefined : claimValid(JSON.parse(current)); } catch { claim = undefined; }
    if (claim && current !== undefined) index.put(claim, current); else index.remove(deliveryId);
  };
  const ackObserved = new Map<string, number>(); // Last ack observation per scope.
  /** True when the durable row moved past the exact bytes the adapter last
   * read or wrote. An operator resolution (`claude resolve --keep-inbox` /
   * `--retry`) may land between the read and the write; that foreign row wins,
   * the stale in-memory copy is dropped, never saved over it, and the index
   * follows the durable row. */
  const superseded = (claim: ClaudeClaimRecord, baseline: string | undefined): boolean => {
    if (baseline === undefined) return false;
    const current = kv.get(CLAIM_PREFIX + claim.deliveryId);
    if (current === baseline) return false;
    log.warn('claude_delivery_claim_superseded', { deliveryId: claim.deliveryId, scope: claim.scope });
    refresh(claim.deliveryId, current);
    return true;
  };
  /** Delete a settled tombstone only if the durable row is still exactly the
   * bytes this pass saw: a resolution that landed meanwhile is never lost. */
  const retire = (row: ClaimRow): boolean => {
    if (superseded(row.claim, row.raw)) return false;
    kv.delete(CLAIM_PREFIX + row.claim.deliveryId);
    index.remove(row.claim.deliveryId);
    return true;
  };
  const saveIfCurrent = (claim: ClaudeClaimRecord, baseline: string | undefined): boolean => {
    if (superseded(claim, baseline)) return false;
    save(claim);
    return true;
  };
  const claimRows = (): ClaimRow[] => {
    const rows: ClaimRow[] = [];
    for (const key of kv.keys(CLAIM_PREFIX)) {
      const raw = kv.get(key)!;
      try {
        const claim = claimValid(JSON.parse(raw));
        if (claim) rows.push({ claim, raw });
        else log.warn('claude_delivery_claim_corrupt', { deliveryId: key.slice(CLAIM_PREFIX.length) });
      } catch { log.warn('claude_delivery_claim_corrupt', { deliveryId: key.slice(CLAIM_PREFIX.length) }); }
    }
    return rows;
  };

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
      if (!snapshot || !Array.isArray(snapshot.bindings)) throw new Error('snapshot_unavailable');
      let admitted = snapshot.bindings.filter(admittedRow);
      if (admitted.length > 1) {
        // The first canary is a single scope. More than one qualifying row
        // means the record cannot be trusted: admit none (fail closed).
        if (!ambiguousLogged) {
          ambiguousLogged = true;
          log.warn('claude_delivery_canary_ambiguous', { count: admitted.length });
        }
        admitted = [];
      } else ambiguousLogged = false;
      admissions = new Map(admitted.map(row => [row.binding_key, row.generation]));
      // A proven snapshot ends any previous admission this pass does not
      // repeat: the next pass that admits the key again re-baselines.
      for (const key of [...established]) if (!admissions.has(key)) established.delete(key);
      subscribeWatches(admitted);
      // The snapshot is proven, so the admitted set is known (possibly
      // empty): one scan builds this pass's index, then the fence sweep
      // drains every claim of a scope that is no longer admitted, in its
      // own lane, even when nothing is admitted at all.
      index = buildIndex(claimRows());
      const keys = new Set(admitted.map(row => row.binding_key));
      await enqueue(FENCE_LANE, async () => fenceSweep(keys));
      if (admitted.length) {
        await Promise.all(admitted.map(row => enqueue(row.binding_key, () =>
          {
            const rebaseline = !established.has(row.binding_key);
            return recoverScope(row, rebaseline).then(() => observeAcks(row)).then(() => stageScope(row, rebaseline));
          })));
      }
    } catch {
      failed = true;
      admissions = new Map(); // An unproven pass marks nothing native.
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

  /** The admitted row's child-spawn coordinates (admittedRow proved them). */
  function scopeCoordinates(row: ClaudeSnapshotBinding): ClaudeDeliveryScopeCoordinates {
    return {
      profile: row.scope!.profile, as: row.scope!.as, config: row.scope!.config!, hubEndpoint: row.scope!.hub_endpoint,
      caller: row.scope!.caller, recipient: row.scope!.recipient,
    };
  }

  /** Admission: a live, policy-enabled, version-qualified (build check
   * parked), canary-opted binding of a peer credential's own inbox (no `as`;
   * the `peer_` prefix is defence in depth behind the CLI's strict id check)
   * whose stored scope is still witnessed by the config at its own path, and
   * carrying the child-spawn coordinates including that absolute config.
   * Every flag must be exactly `true`: a snapshot from an older CLI without
   * `canary` or `witness` admits nothing. Anything else stays legacy. */
  function admittedRow(row: ClaudeSnapshotBinding): boolean {
    const admission = obj(row?.admission), scope = obj(row?.scope);
    return row.state === 'registered' && typeof row.binding_key === 'string' && KEY.test(row.binding_key) &&
      typeof row.generation === 'string' && GENERATION.test(row.generation) &&
      admission.live === true && admission.policy === true && admission.version_qualified === true &&
      admission.canary === true && admission.witness === true && row.scope !== undefined && scope.as === undefined &&
      claudeConfigPathValid(scope.config) && HUB_ID.test(scope.hub_identity ?? '') &&
      HUB_ID.test(scope.profile ?? '') && HUB_ID.test(scope.hub_endpoint ?? '') &&
      HUB_ID.test(scope.caller ?? '') && scope.caller.startsWith('peer_') && scope.recipient === scope.caller;
  }

  function subscribeWatches(admitted: ClaudeSnapshotBinding[]): void {
    // A watch is pinned to the generation and config it was opened under: a
    // change to either reopens it, so it never outlives its binding config.
    const wanted = new Map(admitted.map(row => [row.binding_key, row.generation + '\0' + row.scope!.config!]));
    for (const [key, release] of [...releases]) {
      if (wanted.get(key) !== opened.get(key)) { release(); releases.delete(key); opened.delete(key); }
    }
    for (const row of admitted) {
      if (releases.has(row.binding_key) || watchStopped.get(row.binding_key) === row.generation) continue;
      const coordinates = scopeCoordinates(row);
      const watchEnv = claudeScopeEnv(baseEnv, coordinates);
      let invocation;
      try {
        invocation = commandInvocation([watchBin, '--net', coordinates.profile,
          ...(coordinates.as ? ['--as', coordinates.as] : []), 'watch', '--json'], watchEnv);
      } catch { log.warn('claude_delivery_watch_start_failed', { scope: row.binding_key }); continue; }
      // The CLI watch check proves only the pinned hub and caller, so this
      // stream stays unproved: the exclusive discriminator never widens.
      const request: WatchRequest = {
        scope: {
          hubIdentity: coordinates.hubEndpoint, endpoint: coordinates.hubEndpoint, streamOwner: coordinates.caller,
          credentialSlot: 'claude', scope: 'exclusive:claude:' + row.binding_key,
        },
        revision: row.generation,
        start: () => spawnWatchChild({ command: invocation, env: watchEnv, log, context: { scope: row.binding_key }, spawnChild }),
      };
      const release = deps.pool.subscribe(request, event => {
        // wake only: wake_idle stays on the legacy path, never degraded.
        if (event.priority === 'wake') schedulePass();
      }, end => {
        if (releases.get(row.binding_key) !== release) return; // A reopened watch replaced this one.
        releases.delete(row.binding_key);
        opened.delete(row.binding_key);
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
      opened.set(row.binding_key, wanted.get(row.binding_key)!);
      log.info('claude_delivery_scope_admitted', { scope: row.binding_key, generation: row.generation });
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

  /** The disable drain: every claim whose scope is not admitted this pass
   * (a disabled, opted-out, unwitnessed or vanished scope) and is not in
   * flight is fenced, never resent. A `reserved` claim was proven unwritten
   * and is released; an `attempting` one may have written bytes and becomes
   * `uncertain`. Settled tombstones are left as they are. */
  function fenceSweep(admitted: Set<string>): void {
    for (const [scope, rows] of [...index.byScope]) {
      if (admitted.has(scope)) continue;
      for (const { claim, raw } of [...rows]) {
        if (inFlight.has(claim.deliveryId)) continue;
        let next: ClaudeClaimRecord;
        if (claim.state === 'reserved') {
          next = { ...claim, state: 'released', disposition: 'released', dispositionReason: 'fenced_scope_not_admitted', updatedAt: clock.now() };
        } else if (claim.state === 'attempting') {
          next = { ...claim, state: 'uncertain', disposition: 'uncertain', dispositionReason: 'restart_may_have_written', updatedAt: clock.now() };
        } else continue;
        if (saveIfCurrent(next, raw)) log.info('claude_delivery_claim_fenced', { deliveryId: next.deliveryId, scope, state: next.state });
      }
    }
  }

  /** Read-only hub ack observation for one admitted scope, at most every
   * ACK_OBSERVE_MS and only while the scope has unretired claims: the only
   * writer of `hubAckObserved`. The flag records that the delivery was
   * absent from a complete (not truncated) window read from the hub's acked
   * cursor — acked, or expired or purged unacked; it is an inference from
   * absence, not a confirmation the hub sent. An incomplete or failed read
   * changes nothing. Never posts an ack or a receipt. Then retires, in this
   * scope only, settled claims carrying the flag; `uncertain` ones wait for
   * an operator. */
  async function observeAcks(row: ClaudeSnapshotBinding): Promise<void> {
    const scope = row.binding_key;
    if (!index.byScope.get(scope)?.length) return;
    const last = ackObserved.get(scope);
    if (last === undefined || clock.now() - last >= ACK_OBSERVE_MS) {
      ackObserved.set(scope, clock.now());
      const window = await bounded(fetchAckWindow(scopeCoordinates(row)), FETCH_TIMEOUT, clock);
      if (window && window.complete === true && window.deliveryIds instanceof Set) {
        for (const { claim, raw } of [...(index.byScope.get(scope) ?? [])]) {
          if (claim.hubAckObserved === true || window.deliveryIds.has(claim.deliveryId) || inFlight.has(claim.deliveryId)) continue;
          if (claim.state === 'reserved' || claim.state === 'attempting') continue; // Live work is never marked.
          // Mark a copy: a refused (superseded) write leaves no flag behind.
          const marked: ClaudeClaimRecord = { ...claim, hubAckObserved: true, updatedAt: clock.now() };
          if (saveIfCurrent(marked, raw)) log.info('claude_delivery_absent_from_acked_window', { deliveryId: claim.deliveryId, scope });
        }
      }
    }
    for (const entry of [...(index.byScope.get(scope) ?? [])]) {
      const { claim } = entry;
      if (claim.hubAckObserved !== true || !RETIRABLE.has(claim.state) || inFlight.has(claim.deliveryId)) continue;
      // A settled tombstone absent from the acked window retires, but only
      // if its durable bytes are still exactly the ones this pass saw.
      if (retire(entry)) log.info('claude_delivery_claim_retired', { deliveryId: claim.deliveryId, scope });
    }
  }

  /** Restart recovery for one admitted scope, from this pass's index. A
   * foreign `reserved` claim is proven unwritten by its exact predecessor:
   * fence it against the current generation, then either reschedule it with
   * the current owner or release it fenced. On a rebaseline pass (the first
   * admission this process established for the key) it is always released
   * fenced: it was staged before a not-admitted gap — a stop, crash or
   * kill-switch outage of any length — and its message has been on the
   * legacy path since. A foreign `attempting` claim may have written bytes:
   * retain and surface `uncertain`, never resend. */
  async function recoverScope(row: ClaudeSnapshotBinding, rebaseline: boolean): Promise<void> {
    for (const { claim, raw } of [...(index.byScope.get(row.binding_key) ?? [])]) {
      if (inFlight.has(claim.deliveryId)) continue;
      if (claim.state === 'attempting') {
        const next: ClaudeClaimRecord = { ...claim, state: 'uncertain', disposition: 'uncertain', dispositionReason: 'restart_may_have_written', updatedAt: clock.now() };
        if (saveIfCurrent(next, raw)) log.warn('claude_delivery_uncertain_restart', { scope: claim.scope });
        continue;
      }
      if (claim.state === 'reserved') {
        if (claim.generation !== row.generation) {
          saveIfCurrent({ ...claim, state: 'released', disposition: 'released', dispositionReason: 'fenced_generation_changed', updatedAt: clock.now() }, raw);
          continue;
        }
        if (rebaseline) {
          if (saveIfCurrent({ ...claim, state: 'released', disposition: 'released', dispositionReason: 'fenced_scope_not_admitted', updatedAt: clock.now() }, raw)) {
            log.info('claude_delivery_claim_fenced', { deliveryId: claim.deliveryId, scope: row.binding_key, state: 'released' });
          }
          continue;
        }
        await attempt(row, { ...claim }, raw); // Reschedule with the current owner, on a copy.
      }
    }
  }

  /** New arrivals only, no tail: read the hub head, persist it as the
   * scope's cursor, and stage nothing this pass. Earlier unacked deliveries
   * stay on the legacy hook/inbox path. Triggered by a first admission in
   * this process (daemon start, restart, kill-switch recovery), every
   * re-admission after a pass that did not admit the scope (canary or policy
   * flap, witness lapse), a new generation, an empty cursor or a
   * hub-rejected cursor. A failed read persists nothing and retries on
   * backoff. */
  async function baseline(row: ClaudeSnapshotBinding, coordinates: ClaudeDeliveryScopeCoordinates, record: RecordValue): Promise<void> {
    const head = await bounded(fetchCandidates(coordinates, 'head'), FETCH_TIMEOUT, clock);
    // The hub resolves `head` and reads the page in separate snapshots: an
    // arrival in between comes back on this page with next_cursor past it,
    // and baselining there would skip it. Only an empty head page is a
    // baseline; anything else retries.
    if (!head || head.cursorRejected === true || !Array.isArray(head.events) || head.events.length !== 0 ||
      typeof head.nextCursor !== 'string' || !CURSOR.test(head.nextCursor)) throw new Error('baseline_unavailable');
    kv.set(SCOPE_PREFIX + row.binding_key, JSON.stringify({ v: 1, generation: row.generation, cursor: head.nextCursor,
      blockedPendingLimit: record.blockedPendingLimit === true && record.generation === row.generation, updatedAt: clock.now() }));
    log.info('claude_delivery_scope_baselined', { scope: row.binding_key, generation: row.generation });
  }

  /** Stage one bounded ordered window, then attempt the staged wakes in
   * cursor order. `rebaseline` is set whenever this admission is the first
   * this process established for the key (see `established`): the window
   * then starts at a fresh head baseline, never at a stored cursor that
   * predates a not-admitted gap. The durable checkpoint advances only past
   * fully handled events; an unknown or corrupt event stalls the window for
   * this pass. */
  async function stageScope(row: ClaudeSnapshotBinding, rebaseline: boolean): Promise<void> {
    const coordinates = scopeCoordinates(row);
    const scopeKey = SCOPE_PREFIX + row.binding_key;
    const record = obj(JSON.parse(kv.get(scopeKey) ?? '{}'));
    const stored = typeof record.generation === 'string' && typeof record.cursor === 'string';
    if (rebaseline || !stored || record.generation !== row.generation || record.cursor === '' || !CURSOR.test(record.cursor)) {
      await baseline(row, coordinates, record);
      // Only once the baseline persisted, and never after stop(): a baseline
      // still in flight at stop must not mark the key for a later start().
      if (running) established.add(row.binding_key);
      return;
    }
    const progress = {
      v: 1 as const,
      generation: row.generation,
      cursor: record.cursor as string,
      blockedPendingLimit: record.blockedPendingLimit === true,
      updatedAt: clock.now(),
    };
    const page = await bounded(fetchCandidates(coordinates, progress.cursor), FETCH_TIMEOUT, clock);
    if (page?.cursorRejected === true) {
      // The hub refused the stored cursor itself (purged, another epoch, or
      // malformed): it can never succeed again, so rebaseline at the head
      // rather than retry forever. Anything else stays a transient failure.
      log.warn('claude_delivery_cursor_rejected', { scope: row.binding_key });
      await baseline(row, coordinates, record);
      return;
    }
    if (!page || !Array.isArray(page.events)) throw new Error('candidates_unavailable');
    const existing = index.ids;
    let cursor = progress.cursor;
    const staged: Array<{ claim: ClaudeClaimRecord; raw: string }> = [];
    let stalled = false;
    for (const input of page.events.slice(0, STAGE_BATCH)) {
      const event = obj(input);
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
        log.info('claude_delivery_claim_held', { deliveryId: event.deliveryId, scope: row.binding_key, reason: 'inbox_only' });
        cursor = event.cursor;
        continue;
      }
      if (event.priority !== 'wake') { cursor = event.cursor; continue; } // wake_idle stays legacy.
      if (index.pending(row.binding_key) >= CLAUDE_PENDING_LIMIT) {
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
      const raw = save(claim); // Durable reservation before anything else can happen.
      log.info('claude_delivery_claim_reserved', { deliveryId: claim.deliveryId, scope: claim.scope });
      staged.push({ claim, raw });
      cursor = event.cursor;
    }
    if (index.pending(row.binding_key) < CLAUDE_PENDING_LIMIT) {
      progress.blockedPendingLimit = false;
      blockedLogged.delete(row.binding_key);
    }
    progress.cursor = cursor;
    kv.set(scopeKey, JSON.stringify(progress));
    // Fully staged work is attempted even when later staging stalled: the
    // stall stops the window, it does not strand already-reserved claims.
    for (const { claim, raw } of staged) await attempt(row, claim, raw);
    if (stalled) throw new Error('stalled_unknown_event');
  }

  /** One bounded transport attempt: commit `attempting` before any byte can
   * leave, await the transport (no guards held across it), then record the
   * disposition. Exit 0 is write-completed and never presentation proof. The
   * durable row is re-read before every write: an operator resolution that
   * landed mid-attempt supersedes this copy and is never overwritten. */
  async function attempt(row: ClaudeSnapshotBinding, claim: ClaudeClaimRecord, baseline?: string): Promise<void> {
    if (!running) return;
    if (superseded(claim, baseline)) return;
    claim.attemptId = randomBytes(16).toString('hex');
    claim.msgId = randomBytes(16).toString('hex');
    claim.state = 'attempting';
    claim.updatedAt = clock.now();
    const committed = save(claim);
    log.info('claude_delivery_claim_attempting', { deliveryId: claim.deliveryId, scope: claim.scope, attemptId: claim.attemptId });
    // Canary test seam: SIGKILL this process after `attempting` is durable
    // but before any byte can leave, so a harness can replay the crash
    // window deterministically. Only a canary-opted scope ever reaches here,
    // and this var is never set in production.
    if (env.VECNET_CLAUDE_DELIVERY_CRASH_BEFORE_WRITE === '1') process.kill(process.pid, 'SIGKILL');
    inFlight.add(claim.deliveryId);
    try {
      const result = await transport({ bindingKey: row.binding_key, generation: claim.generation,
        msgId: claim.msgId, threadId: claim.threadId, messageId: claim.messageId });
      const exit: DeliverExit = typeof result === 'number' ? result : result.exit;
      if (exit === 0) {
        claim.state = 'released';
        claim.disposition = 'attempted';
        claim.dispositionReason = 'write_completed_no_presentation_proof';
      } else if (exit === 3 || exit === 5) {
        claim.state = 'refused';
        claim.disposition = 'refused';
        claim.dispositionReason = exit === 5 ? 'refused_before_write' : 'connect_refused_or_timeout';
      } else {
        // 4, or anything an injected transport returns outside the contract.
        claim.state = 'uncertain';
        claim.disposition = 'uncertain';
        claim.dispositionReason = 'write_failed_or_outcome_unknown';
      }
      // The helper's own words for why it refused, trimmed to one line — a
      // log diagnostic only: the claim keeps its fixed reason.
      const helperStderr = typeof result === 'number' || result.exit !== 5 ? undefined
        : result.stderr.replace(/\s+/g, ' ').trim();
      log.info('claude_delivery_disposition', { deliveryId: claim.deliveryId, scope: claim.scope, exit, state: claim.state, disposition: claim.disposition, reason: claim.dispositionReason, ...(helperStderr !== undefined && helperStderr !== '' ? { helperStderr } : {}) });
    } catch {
      claim.state = 'uncertain';
      claim.disposition = 'uncertain';
      claim.dispositionReason = 'transport_outcome_unknown';
      log.warn('claude_delivery_disposition', { deliveryId: claim.deliveryId, scope: claim.scope, exit: null, state: claim.state, disposition: claim.disposition, reason: claim.dispositionReason });
    } finally {
      inFlight.delete(claim.deliveryId);
      claim.updatedAt = clock.now();
      saveIfCurrent(claim, committed);
    }
  }

  return {
    admitted: () => admissions,
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
      admissions = new Map();
      established.clear(); // A restart of this adapter re-baselines first.
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
