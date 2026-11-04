import type { SessionHandle } from './backend.ts';
import type { JobRow } from './ledger.ts';
import type { LaunchDeps } from './launch.ts';
import { clearPending, markPending, pendingDue, recordRunning, recoverFailedLaunch, startLaunch } from './launch.ts';

export type ReattachOutcome =
  | { job: JobRow; outcome: 'live' | 'parked' | 'adopted'; handle: SessionHandle }
  | { job: JobRow; outcome: 'launch_started'; result: Awaited<ReturnType<typeof startLaunch>> }
  | { job: JobRow; outcome: 'lost' }
  | { job: JobRow; outcome: 'unreachable' };

// Task 17 owns the event pump and its exactly-once posting transaction. Supply
// its handle with a default persisted cursor, without a competing iterator here.
function resumingHandle(handle: SessionHandle, job: JobRow, deps: LaunchDeps): SessionHandle {
  return {
    ref:handle.ref,state:() => handle.state(),send:(input,mode) => handle.send(input,mode),
    turns(after,signal) {
      const saved = deps.ledger.job(job.jobId)?.turnCursor;
      return handle.turns(after ?? (saved === null || saved === undefined ? undefined : {value:saved}),signal);
    },
    interrupt:reason => handle.interrupt(reason),activity:() => handle.activity(),
    ...(handle.park ? {park:() => handle.park!()} : {}),settle:mode => handle.settle(mode),
  };
}
function lost(job: JobRow, deps: LaunchDeps, reason?: string): ReattachOutcome {
  deps.ledger.tx(() => {
    clearPending(job.jobId,deps);
    deps.jobs.report(job.jobId,'failed',{error:'outcome_unknown',...(reason ? {reason} : {})});
  });
  return {job:deps.ledger.job(job.jobId)!,outcome:'lost'};
}
async function reattach(job: JobRow, deps: LaunchDeps): Promise<ReattachOutcome> {
  const backend = deps.backends.get(job.backend);
  if (!backend) return lost(job,deps,'backend_missing');
  if (job.state === 'accepted') return {job,outcome:'launch_started',result:await startLaunch(job,deps)};
  if (deps.ledger.kv('launch-failure').get(job.jobId)) {
    const outcome = await recoverFailedLaunch(job,backend,deps,false);
    return {job:deps.ledger.job(job.jobId)!,outcome};
  }
  try {
    const ref = job.sessionRef ?? await backend.findByTag(`vl-${job.jobId}`);
    if (!ref) return lost(job,deps);
    // Save a discovered ref before the next network call: if that call is
    // unreachable, subsequent retries address this exact session directly.
    if (!job.sessionRef) deps.ledger.updateJob(job.jobId,{sessionRef:ref});
    const result = await backend.reattach(ref,deps.hooks(deps.ledger.job(job.jobId)!));
    if (result.found === 'none') return lost(job,deps);
    recordRunning(job,result.handle,deps);
    return {job:deps.ledger.job(job.jobId)!,outcome:job.sessionRef ? result.found : 'adopted',handle:resumingHandle(result.handle,job,deps)};
  } catch {
    markPending(job.jobId,deps);
    return {job:deps.ledger.job(job.jobId)!,outcome:'unreachable'};
  }
}
export async function reattachAll(deps: LaunchDeps): Promise<ReattachOutcome[]> {
  const results: ReattachOutcome[] = [];
  for (const job of deps.ledger.openJobs()) results.push(await reattach(job,deps));
  return results;
}
export async function retryPending(deps: LaunchDeps): Promise<ReattachOutcome[]> {
  const results: ReattachOutcome[] = [];
  for (const job of deps.ledger.openJobs()) {
    if (job.reattachPending && pendingDue(job,deps)) results.push(await reattach(job,deps));
  }
  return results;
}
