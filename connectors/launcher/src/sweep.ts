import type { KeyValue, SessionHooks, SessionRef } from './backend.ts';
import type { LaunchDeps } from './launch.ts';

export interface SweepReport {
  settled: SessionRef[]; workspacesRemoved: string[];
  skipped: Array<{ref?: SessionRef; path?: string; reason: string}>;
}
const orphanHooks: SessionHooks = {
  onEvent() {},
  async onPermissionRequest(req) { return {optionId:req.defaultOnExpiry,decidedBy:'expired'}; },
  async onQuestion() { return {answers:{},answeredBy:'timeout'}; },
};
export async function sweep(deps: LaunchDeps & {graceMs: number; retentionMs: number; firstSeen: KeyValue}): Promise<SweepReport> {
  const report: SweepReport = {settled:[],workspacesRemoved:[],skipped:[]};
  for (const backend of deps.backends.values()) {
    let refs: SessionRef[];
    try { refs = await backend.listOwned(); }
    catch { report.skipped.push({reason:'unreachable'}); continue; }
    for (const ref of refs) {
      const tag = ref.extra?.tag;
      if (typeof tag !== 'string' || !tag.startsWith('vl-')) { report.skipped.push({ref,reason:'untagged'}); continue; }
      // Re-read after network calls so a concurrently admitted intent or a
      // freshly recorded ref is protected as soon as it reaches the ledger.
      if (deps.ledger.allJobRefs().some(job => job.backend === backend.id &&
          (job.tag === tag || (job.ref?.id === ref.id && job.ref.host === ref.host)))) {
        report.skipped.push({ref,reason:'ledger_job'}); continue;
      }
      const key = JSON.stringify([backend.id,ref.host,ref.id]);
      const raw = deps.firstSeen.get(key);
      if (raw === undefined) deps.firstSeen.set(key,String(deps.clock.now()));
      const seenAt = raw === undefined ? deps.clock.now() : Number(raw);
      if (!Number.isFinite(seenAt) || deps.clock.now()-seenAt < deps.graceMs) { report.skipped.push({ref,reason:'grace'}); continue; }
      try {
        const attached = await backend.reattach(ref,orphanHooks);
        if (attached.found === 'none') { report.skipped.push({ref,reason:'gone'}); continue; }
        // Recheck before the destructive action after reattach's await.
        if (deps.ledger.allJobRefs().some(job => job.backend === backend.id && (job.tag === tag || (job.ref?.id === ref.id && job.ref.host === ref.host)))) {
          report.skipped.push({ref,reason:'ledger_job'}); continue;
        }
        await attached.handle.settle('lost'); report.settled.push(ref); deps.firstSeen.delete(key);
      } catch { report.skipped.push({ref,reason:'unreachable'}); }
    }
    if (!backend.cleanupWorkspace) continue;
    try {
      if ((await backend.capabilities()).workspace !== 'launcher-dir') continue;
    } catch { report.skipped.push({reason:'unreachable'}); continue; }
    for (const job of deps.ledger.allJobRefs()) {
      if (job.backend !== backend.id || job.state !== 'ended' || job.endedAt === null ||
          deps.clock.now()-job.endedAt <= deps.retentionMs) continue;
      // A launch that failed before the backend returned a session leaves no
      // ref: the plugin finds the worktree it allocated by the job's tag.
      const ref: SessionRef | null = job.ref ?? (job.tag.startsWith('vl-') ? {backend:backend.id,host:deps.host,id:'',extra:{tag:job.tag}} : null);
      if (!ref || (job.ref && !job.workspace)) continue;
      const path = job.workspace ?? job.tag;
      if (typeof ref.extra?.tag !== 'string' || !ref.extra.tag.startsWith('vl-')) {
        report.skipped.push({ref,path,reason:'untagged'}); continue;
      }
      const done = deps.ledger.kv('sweep:workspace-done');
      if (!job.ref && done.get(job.jobId)) continue;
      try {
        const result = await backend.cleanupWorkspace(ref);
        if (result.removed) report.workspacesRemoved.push(path);
        else report.skipped.push({ref,path,reason:result.reason ?? 'kept'});
        // Without a ref there is nothing else to retry once the plugin has no worktree for the tag.
        if (!job.ref && (result.removed || result.reason === 'missing_workspace_metadata')) done.set(job.jobId,'1');
      } catch { report.skipped.push({ref,path,reason:'unreachable'}); }
    }
  }
  return report;
}
