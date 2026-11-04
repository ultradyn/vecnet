import { HubError } from '../../hub-client/src/errors.ts';
import type { Clock, Log } from '../../hub-client/src/ports.ts';
import type { Delivery, SendRequest } from '../../hub-client/src/wire.ts';
import type { BackendCapabilities, BackendHealth, PermissionMode } from './backend.ts';
import type { LauncherConfig } from './config.ts';
import { parseLaunchRequest } from './contract/launch-input.ts';
import type { LaunchInput } from './contract/launch-input.ts';
import type { Directory } from './directory.ts';
import type { JobReporter } from './jobs.ts';
import type { IngressRow, JobRow, Ledger } from './ledger.ts';
import type { Outbox } from './outbox.ts';
import type { ParentBridge } from './parent-bridge.ts';
import { accountKeyFor, gateLaunch, policyKeyFor } from './policy.ts';
import type { GateResult } from './policy.ts';
import { relayDecision } from './relay.ts';
import type { SessionLane } from './session.ts';

export interface DispatchDeps {
  ledger: Ledger; outbox: Outbox; jobs: JobReporter; dir: Directory; cfg: LauncherConfig; clock: Clock; log: Log;
  host: string; self: string;
  bridge: Pick<ParentBridge, 'handleApprovalResponse' | 'handleThreadReply'>;
  /** Cached capabilities and health per configured backend, for the gate. */
  backends(): Map<string, { caps: BackendCapabilities; health: BackendHealth }>;
  /** The lane of an open job (created on demand); undefined once the job has ended. */
  lane(jobId: string): SessionLane | undefined;
  /** Called after an accept has committed: create the lane, launch, attach and track. */
  accepted(jobId: string): void;
  /** Called after each row, so presence can report a changed load. */
  changed?(): void;
}
type Failure = { error: string; reason?: string; details?: Record<string, unknown> };

const BATCH = 500;
const JOB_ID = /^job_[0-9A-HJKMNP-TV-Z]{26}$/;

export function createDispatcher(deps: DispatchDeps): { run(signal: AbortSignal): Promise<void>; kick(): void } {
  const { ledger, outbox, jobs, dir, cfg, clock, log } = deps;
  let wake: (() => void) | undefined;
  let kicked = false;
  function kick() { kicked = true; const w = wake; wake = undefined; w?.(); }

  function receipt(deliveryId: string, state: 'processed' | 'failed', note?: string) {
    outbox.enqueue('receipt', `vl.rc.${deliveryId}.${state}`, { delivery_id: deliveryId, state, ...(note ? { note } : {}) }, 'ingress');
  }
  /** One commit: the row's receipt and its finished ingress state. */
  function finish(row: IngressRow, state: 'processed' | 'failed' | null, note?: string) {
    ledger.tx(() => {
      if (state) receipt(row.deliveryId, state, note);
      ledger.finishIngress(row.deliveryId, 'done', note);
    });
  }
  function done(row: IngressRow) { ledger.finishIngress(row.deliveryId, 'done'); }
  function failureText(f: Failure) { return `failed: ${f.error}${f.reason ? ` (${f.reason})` : ''}`; }

  // A refusal without a job row of its own: the thread already belongs to
  // another job (the thread index is unique), so report against the hub's job id.
  function refuseInThread(row: IngressRow, jobId: string, f: Failure) {
    const m = row.delivery.message, key = `vl.js.${jobId}.1`;
    const req: SendRequest = {
      idempotency_key: key, kind: 'job_status', thread_id: m.thread_id, in_reply_to: m.message_id, priority: 'wake_idle',
      content: { text: failureText(f) }, job_status: { job_id: jobId, sequence: 1, state: 'failed', output: f },
    };
    ledger.tx(() => { outbox.enqueue('send', key, req, jobId); ledger.finishIngress(row.deliveryId, 'done', f.error); });
  }
  function jobFields(d: Delivery, jobId: string, policyKey: string, input: Partial<LaunchInput>): Omit<JobRow, 'statusSeq' | 'terminal' | 'turns' | 'updatedAt' | 'reattachPending' | 'endedAt' | 'state' | 'deadlineAt' | 'input' | 'instructions'> {
    const m = d.message, now = clock.now();
    const text = (x: unknown) => typeof x === 'string' ? x : '';
    return {
      jobId, threadId: m.thread_id, launchMessageId: m.message_id, requester: m.sender, policyKey,
      backend: text(input.backend), preset: text(input.preset), permissionMode: (text(input.permission_mode) || 'read-only') as PermissionMode,
      project: text(input.project), title: text(input.title) || d.thread.title || 'session', tag: `vl-${jobId}`,
      sessionRef: null, sessionKind: null, turnCursor: null, branch: null, workspace: null,
      lastActivityAt: now, parkedAt: null, createdAt: now,
    };
  }
  /** A refused launch: a terminal job row and its failed report, in the tx that finishes the row. */
  function refuse(row: IngressRow, jobId: string | undefined, f: Failure, policyKey = 'default') {
    const d = row.delivery, m = d.message;
    if (!jobId || !JOB_ID.test(jobId)) {
      // No job id to report against: the receipt carries the refusal.
      finish(row, 'processed', f.error);
      return;
    }
    const owner = ledger.jobByThread(m.thread_id);
    if (owner && owner.jobId !== jobId) { refuseInThread(row, jobId, f); return; }
    const raw = m.job_request?.input;
    const input = (raw && typeof raw === 'object' ? raw : {}) as Partial<LaunchInput>;
    ledger.tx(() => {
      if (!ledger.job(jobId)) {
        // The refused input is kept only as its routing fields, never the prompt.
        const kept: LaunchInput = { backend: String(input.backend ?? ''), project: String(input.project ?? ''), preset: String(input.preset ?? ''),
          permission_mode: (typeof input.permission_mode === 'string' ? input.permission_mode : 'read-only') as PermissionMode };
        ledger.createJob({ ...jobFields(d, jobId, policyKey, input), state: 'ended', deadlineAt: clock.now(), input: kept });
        jobs.report(jobId, 'failed', { ...f }, failureText(f));
      }
      ledger.finishIngress(row.deliveryId, 'done', f.error);
    });
  }
  function gateFailure(g: Exclude<GateResult, { ok: true }>): Failure {
    if (g.error === 'cap_reached') return { error: g.error, details: g.details };
    if (g.error === 'policy_denied') return { error: g.error, reason: g.reason, ...(g.details ? { details: g.details } : {}) };
    return { error: g.error, reason: g.reason };
  }

  async function jobRequest(row: IngressRow): Promise<void> {
    const d = row.delivery, m = d.message;
    const rawId = m.metadata['vecnet.job_id'];
    const jobId = typeof rawId === 'string' ? rawId : undefined;
    if (jobId && ledger.job(jobId)) { done(row); return; }     // a redelivery never launches again
    const owner = ledger.jobByThread(m.thread_id);
    if (owner && jobId && owner.jobId !== jobId) { refuseInThread(row, jobId, { error: 'unsupported', reason: 'one job per thread' }); return; }
    const parsed = parseLaunchRequest(d);
    if (!parsed.ok) { refuse(row, jobId, { error: parsed.error, reason: parsed.reason }); return; }
    const req = parsed.req;
    await dir.ensureKnown(req.requester);
    // No await between the gate and its commit: concurrent lanes cannot both take the last slot.
    const account = accountKeyFor(policyKeyFor(req.requester, dir, cfg), req.requester), now = clock.now();
    const gate = gateLaunch({ sender: req.requester, req, dir, cfg, counts: ledger.counts(), launchesLastHour: ledger.launchesSince(account, now - 3_600_000),
      backends: deps.backends(), now });
    if (!gate.ok) { refuse(row, req.jobId, gateFailure(gate), account); return; }
    const accepted = ledger.tx(() => {
      if (ledger.job(req.jobId)) { ledger.finishIngress(row.deliveryId, 'done'); return false; }
      // policyKey is the accounting key (Ledger.counts groups by it).
      ledger.createJob({ ...jobFields(d, req.jobId, gate.account, req.input), state: 'accepted', deadlineAt: gate.deadlineAt,
        instructions: req.instructions, input: req.input });
      ledger.noteLaunch(gate.account, now);
      jobs.report(req.jobId, 'accepted', { host: deps.host, backend: req.input.backend, preset: req.input.preset, permission_mode: req.input.permission_mode });
      // The launch Delivery has been acted on: its instructions are the session's first input.
      receipt(row.deliveryId, 'processed');
      ledger.finishIngress(row.deliveryId, 'done');
      return true;
    });
    if (accepted) deps.accepted(req.jobId);
  }

  async function route(row: IngressRow): Promise<void> {
    const d = row.delivery, m = d.message;
    if (m.job_request) {
      if (m.job_request.operation !== 'launch') {
        const id = m.metadata['vecnet.job_id'];
        const jobId = typeof id === 'string' ? id : undefined;
        if (jobId && ledger.job(jobId)) { done(row); return; }
        refuse(row, jobId, { error: 'unsupported', reason: 'operation is not supported' });
        return;
      }
      await jobRequest(row); return;
    }
    if (m.kind === 'job_status' && m.job_status?.state === 'cancelled') {
      const job = ledger.job(m.job_status.job_id);
      if (job && job.threadId === m.thread_id && job.requester === m.sender) {
        const lane = job.state === 'ended' ? undefined : deps.lane(job.jobId);
        if (lane) await lane.cancel(m.sender);
        finish(row, 'processed');
        return;
      }
    }
    if (m.kind === 'approval_response' && await deps.bridge.handleApprovalResponse(d) === 'consumed') { done(row); return; }
    const job = ledger.jobByThread(m.thread_id);
    if (job) {
      if (await deps.bridge.handleThreadReply(d) === 'consumed') { done(row); return; }
      const lane = job.state === 'ended' ? undefined : deps.lane(job.jobId);
      // The lane runs controls (launch.control) through its durable control path,
      // which also posts the processed/not_relayed receipt and fences a redelivery.
      if (lane) { await lane.handleMessage(d); done(row); return; }
      const relay = relayDecision(m.sender, job, deps.self, id => dir.isOperator(id));
      if (relay.relay) finish(row, 'failed', 'session_ended');
      else finish(row, 'processed', 'not_relayed');
      return;
    }
    if (ledger.jobByEscalationThread(m.thread_id)) {
      if (await deps.bridge.handleThreadReply(d) === 'consumed') { done(row); return; }
      finish(row, 'processed', 'not_relayed');
      return;
    }
    finish(row, 'processed', 'not_a_job_thread');
  }

  async function processRow(row: IngressRow): Promise<void> {
    try { await route(row); }
    catch (err) {
      const code = err instanceof HubError ? err.code : 'internal_error';
      // Never log the error or the body: either can quote message text.
      log.warn('ingress row failed', { deliveryId: row.deliveryId, code });
      try {
        ledger.tx(() => {
          receipt(row.deliveryId, 'failed', code);
          ledger.finishIngress(row.deliveryId, 'failed', code);
        });
      } catch { log.warn('ingress row failure not recorded', { deliveryId: row.deliveryId }); }
    }
    try { deps.changed?.(); } catch { /* presence is best effort */ }
  }

  return {
    kick,
    async run(signal) {
      const threads = new Set<string>();
      const running = new Set<Promise<void>>();
      signal.addEventListener('abort', kick, { once: true });
      while (!signal.aborted) {
        kicked = false;
        let rows: IngressRow[];
        try { rows = ledger.queued(BATCH); }
        catch { if (signal.aborted) break; throw new Error('ingress queue unavailable'); }
        const blocked = new Set<string>();
        for (const row of rows) {
          // One row at a time per thread, in admission order; threads run in parallel.
          if (blocked.has(row.threadId) || threads.has(row.threadId)) { blocked.add(row.threadId); continue; }
          blocked.add(row.threadId);
          threads.add(row.threadId);
          const work: Promise<void> = processRow(row).finally(() => { threads.delete(row.threadId); running.delete(work); kick(); });
          running.add(work);
        }
        if (kicked || signal.aborted) continue;
        await new Promise<void>(resolve => { wake = resolve; });
      }
      await Promise.allSettled([...running]);
    },
  };
}
