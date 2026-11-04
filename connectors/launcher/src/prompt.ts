import type { BackendCapabilities, LaunchSpec, ResolvedProject } from './backend.ts';
import type { JobRow } from './ledger.ts';
import { scrubEnv } from './redact.ts';

export const PROMPT_FOOTER = 'If you need a decision from your requester, end your turn with one clear question; your requester will answer in this conversation.';
function appendFooter(instructions: string, footer: string): string {
  return instructions.endsWith(footer) ? instructions : `${instructions}\n\n${footer}`;
}
export function buildPrompt(instructions: string, job: Pick<JobRow, 'jobId' | 'title'>): string {
  return appendFooter(instructions,`${PROMPT_FOOTER}\nWork on branch launch/${job.jobId}; do not push.`);
}

export function buildLaunchSpec(job: JobRow, project: ResolvedProject, caps: BackendCapabilities, env: Record<string, string>): LaunchSpec {
  // The gate copies job_request.instructions into the durable intent. Fail
  // closed on legacy rows rather than silently launch with the job's title.
  if (typeof job.instructions !== 'string') throw new Error('launch instructions missing');
  const prompt = caps.branchName === 'settable' ? buildPrompt(job.instructions,job) :
    appendFooter(job.instructions,`${PROMPT_FOOTER}\nDo not push.`);
  return {
    jobId:job.jobId,tag:`vl-${job.jobId}`,title:`vl-${job.jobId} ${job.title}`,prompt,
    project,branch:caps.branchName === 'settable' ? `launch/${job.jobId}` : '',
    preset:job.preset,permissionMode:job.permissionMode,attachments:[],env:scrubEnv(env,[]),
  };
}
