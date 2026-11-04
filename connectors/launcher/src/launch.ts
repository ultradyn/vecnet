import type { Clock, Log } from '../../hub-client/src/ports.ts';
import type { Backend, ResolvedProject, SessionHandle, SessionHooks, SessionRef } from './backend.ts';
import type { LauncherConfig } from './config.ts';
import type { JobReporter } from './jobs.ts';
import type { JobRow, Ledger } from './ledger.ts';
import { buildLaunchSpec } from './prompt.ts';
import { redactText } from './redact.ts';

export interface LaunchDeps {
  ledger: Ledger; jobs: JobReporter; backends: Map<string, Backend>; hooks(job: JobRow): SessionHooks;
  clock: Clock; log: Log; host: string; cfg: LauncherConfig; projects(job: JobRow): ResolvedProject;
  beforeRecordRef?: (jobId: string) => void;
}

interface Pending { since: number; nextAt: number; noted: boolean }
export function markPending(jobId: string, deps: LaunchDeps): void {
  deps.ledger.tx(() => {
    const store = deps.ledger.kv('reattach');
    const old = store.get(jobId);
    const pending: Pending = old ? JSON.parse(old) : {since:deps.clock.now(),nextAt:0,noted:false};
    pending.nextAt = deps.clock.now() + (deps.clock.now()-pending.since < 3600000 ? 30000 : 600000);
    store.set(jobId,JSON.stringify(pending));
    deps.ledger.updateJob(jobId,{reattachPending:true});
  });
}
export function clearPending(jobId: string, deps: LaunchDeps): void {
  deps.ledger.kv('reattach').delete(jobId);
  deps.ledger.updateJob(jobId,{reattachPending:false});
}
export function pendingDue(job: JobRow, deps: LaunchDeps): boolean {
  const store = deps.ledger.kv('reattach'), raw = store.get(job.jobId);
  if (!raw) return true;
  const pending: Pending = JSON.parse(raw);
  if (job.state !== 'ended' && job.terminal === null && !pending.noted && deps.clock.now()-pending.since >= 300000) {
    deps.ledger.tx(() => {
      const n = deps.ledger.nextCounter(job.jobId,'unreachable');
      const key = `vl.nt.${job.jobId}.unreachable.${n}`;
      deps.ledger.enqueue('send',key,{
        idempotency_key:key,kind:'message',thread_id:job.threadId,in_reply_to:job.launchMessageId,
        recipients:[job.requester],priority:'wake_idle',
        content:{text:'backend unreachable; the session will be reattached when it returns'},
      },job.jobId);
      pending.noted = true; store.set(job.jobId,JSON.stringify(pending));
    });
  }
  return deps.clock.now() >= pending.nextAt;
}

export async function findByTagWithRetry(backend: Backend, tag: string, deps: Pick<LaunchDeps, 'clock' | 'log'>, attempts = 5): Promise<SessionRef | null | 'unreachable'> {
  let unreachable = false;
  for (let i=0;i<Math.max(1,attempts);i++) {
    if (i) await deps.clock.sleep(Math.min(30000,2000 * 2 ** (i-1)));
    try {
      const ref = await backend.findByTag(tag);
      if (ref) return ref;
    } catch {
      unreachable = true;
      deps.log.warn('launch lookup unavailable',{backend:backend.id});
    }
  }
  return unreachable ? 'unreachable' : null;
}

export function recordRunning(job: JobRow, handle: SessionHandle, deps: LaunchDeps, branch?: string): boolean {
  const ref = handle.ref;
  return deps.ledger.tx(() => {
    const current = deps.ledger.job(job.jobId)!;
    const resolvedBranch = typeof ref.extra?.branch === 'string' ? ref.extra.branch : branch ?? current.branch;
    const workspace = typeof ref.extra?.workspace === 'string' ? ref.extra.workspace : current.workspace;
    // Keep late refs for cleanup without reopening terminal jobs or their slots.
    if (current.terminal !== null || current.state === 'ended') {
      deps.ledger.updateJob(job.jobId,{sessionRef:ref,branch:resolvedBranch || null,workspace});
      markPending(job.jobId,deps);
      return false;
    }
    deps.ledger.updateJob(job.jobId,{sessionRef:ref,branch:resolvedBranch || null,workspace,state:current.state === 'settling' ? 'settling' : 'running',sessionKind:handle.state().kind});
    clearPending(job.jobId,deps);
    // Settling follows running; lifecycle state is not a reason to report it again.
    if (current.state === 'accepted' || current.state === 'launching') deps.jobs.report(job.jobId,'running',{
      session_ref:ref,branch:resolvedBranch || null,workspace,link:ref.extra?.link,
    });
    return true;
  });
}

export async function settleLateHandle(job: JobRow, handle: SessionHandle, deps: LaunchDeps): Promise<void> {
  try {
    await handle.settle(job.terminal === 'cancelled' ? 'cancel' : 'lost');
    clearPending(job.jobId,deps);
  } catch {
    // recordRunning saved the ref and retry schedule atomically. An ended job
    // stays out of capacity while retryPending finishes its backend cleanup.
    deps.log.warn('late session cleanup unavailable',{backend:job.backend,jobId:job.jobId});
  }
}

// A failure fence is durable and distinct from a crash before recording the ref.
// A session found for a failed call must be settled, never adopted as running.
interface FailureFence { reason: string; branch?: string }
export async function recoverFailedLaunch(job: JobRow, backend: Backend, deps: LaunchDeps, retryLookup: boolean): Promise<'lost' | 'unreachable'> {
  const store = deps.ledger.kv('launch-failure');
  const fence: FailureFence = JSON.parse(store.get(job.jobId)!);
  let ref = job.sessionRef;
  try {
    if (!ref) {
      const found = retryLookup ? await findByTagWithRetry(backend,`vl-${job.jobId}`,deps) : await backend.findByTag(`vl-${job.jobId}`);
      if (found === 'unreachable') { markPending(job.jobId,deps); return 'unreachable'; }
      ref = found;
    }
    let settled = false;
    if (ref) {
      deps.ledger.updateJob(job.jobId,{
        sessionRef:ref,branch:typeof ref.extra?.branch === 'string' ? ref.extra.branch : fence.branch ?? null,
        workspace:typeof ref.extra?.workspace === 'string' ? ref.extra.workspace : null,
      });
      const result = await backend.reattach(ref,deps.hooks(deps.ledger.job(job.jobId)!));
      if (result.found !== 'none') { await result.handle.settle('lost'); settled = true; }
    }
    deps.ledger.tx(() => {
      clearPending(job.jobId,deps);
      deps.jobs.report(job.jobId,'failed',{error:'backend_error',reason:fence.reason,...(settled ? {settled_orphan:true} : {})});
      store.delete(job.jobId);
    });
    return 'lost';
  } catch {
    markPending(job.jobId,deps);
    return 'unreachable';
  }
}

export async function startLaunch(job: JobRow, deps: LaunchDeps, signal?: AbortSignal): Promise<{ok:true; handle:SessionHandle} | {ok:false; error:string}> {
  // Re-read under the intent transaction: stale deliveries and overlapping
  // callers must never invoke launch a second time, even on an idempotent fake.
  const claimed = deps.ledger.tx(() => {
    const current = deps.ledger.job(job.jobId);
    if (!current || current.terminal !== null || current.state !== 'accepted') return false;
    deps.ledger.updateJob(job.jobId,{state:'launching'});
    return true;
  });
  if (!claimed) return {ok:false,error:'outcome_unknown'};
  const backend = deps.backends.get(job.backend);
  if (!backend) {
    deps.jobs.report(job.jobId,'failed',{error:'outcome_unknown',reason:'backend_missing'});
    return {ok:false,error:'outcome_unknown'};
  }
  let handle: SessionHandle;
  let branch: string | undefined;
  try {
    const spec = buildLaunchSpec(job,deps.projects(job),await backend.capabilities(),
      Object.fromEntries(Object.entries(process.env).filter((entry): entry is [string,string] => entry[1] !== undefined)));
    branch = spec.branch;
    // Keep the requested branch across a kill before recording a ref, including
    // plugins that cannot report the branch until their next backend read.
    deps.ledger.updateJob(job.jobId,{branch:branch || null});
    handle = await backend.launch(spec,deps.hooks(job),signal);
  } catch (err) {
    const reason = redactText(err instanceof Error ? err.message : 'backend launch failed').text.slice(0,512);
    deps.ledger.kv('launch-failure').set(job.jobId,JSON.stringify({reason,branch} satisfies FailureFence));
    await recoverFailedLaunch(deps.ledger.job(job.jobId)!,backend,deps,true);
    return {ok:false,error:'backend_error'};
  }
  // This seam represents process death, so it deliberately escapes the launch
  // error handler. Restart adoption, rather than failure cleanup, owns the gap.
  deps.beforeRecordRef?.(job.jobId);
  if (!recordRunning(job,handle,deps,branch)) {
    await settleLateHandle(deps.ledger.job(job.jobId)!,handle,deps);
    return {ok:false,error:'outcome_unknown'};
  }
  return {ok:true,handle};
}
