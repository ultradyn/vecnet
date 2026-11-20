// One source-bound acceptance policy shared by pre-publication and completed-
// run audit. Includes queue time, runner setup, actions and automatic post steps.
import { execFileSync } from 'node:child_process';
import { appendFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { releaseJobCount } from './schedule.mjs';
// R5 acceptance audit took4s and post checkout rounded to0s; artifact
// verification was skipped (failed preflight), so8s is headroom, not a
// measured completion guarantee. Tags retain40s npm +14s GitHub caps plus7s
// verification/cleanup. The completed-run audit still enforces actual <300s.
export const acceptanceReserve = (event, ref) => event === 'push' && ref?.startsWith('refs/tags/v') ? 61 : 8;
// The <300s budget belongs to one attempt. GitHub keeps a run's created_at
// across "Re-run failed jobs", so measuring from it could never admit a rerun.
// run_started_at is the audited attempt's own start; on a first attempt it is
// the run's creation time, so single-attempt semantics are unchanged.
export const attemptStart = run => Date.parse(run?.run_started_at);
export const runDeadline = run => attemptStart(run) + 299000;
export function audit(run,jobs,{sha,now=new Date().toISOString(),preflight=false,reserve=0,currentJob,expectedJobs,attempt}={}) {
 const violations=[];const instant=value=>Date.parse(value);
 if(!sha||run.head_sha!==sha)violations.push('source SHA mismatch');
 if(!preflight&&(run.status!=='completed'||run.conclusion!=='success'))violations.push('run not completed successfully');
 if(!Number.isInteger(run.run_attempt)||run.run_attempt<1)violations.push('missing run attempt');
 else if(attempt!==undefined&&run.run_attempt!==attempt)violations.push(`run attempt ${run.run_attempt} is not audited attempt ${attempt}`);
 if(!Number.isFinite(attemptStart(run)))violations.push('missing attempt start timestamp');
 const included=jobs.filter(j=>j.id!==currentJob);
 if(!included.length)violations.push('missing jobs');
 if(expectedJobs && included.length!==expectedJobs)violations.push(`missing/unexpected jobs: ${included.length}, expected ${expectedJobs}`);
 // Jobs carried into a "Re-run failed jobs" attempt keep their original
 // times, before this attempt started, so they cannot extend its elapsed time.
 const ends=included.map(j=>instant(j.completed_at)).filter(Number.isFinite);
 const elapsed=((preflight?instant(now):Math.max(...ends))-attemptStart(run))/1000;
 if(!Number.isFinite(elapsed)||elapsed>=300)violations.push(`end-to-end duration ${elapsed} must be <300s`);
 if(preflight&&elapsed+reserve>=300)violations.push(`insufficient remaining reserve ${reserve}s at ${elapsed}s`);
 if(preflight&&currentJob){
  const own=jobs.find(j=>j.id===currentJob);
  for(const step of own?.steps||[]){
   if(step.conclusion==='skipped'||step.status!=='completed')continue;
   const seconds=(instant(step.completed_at)-instant(step.started_at))/1000;
   if(!Number.isFinite(seconds)||seconds<0||seconds>=60||step.conclusion!=='success')violations.push(`accept/${step.name}: invalid completed step ${seconds}s`);
  }
 }
 for(const job of included){
  // Every job, including one carried from an earlier attempt, must be listed
  // under the audited attempt; never mix in another attempt's records.
  if(job.run_attempt!==run.run_attempt)violations.push(`job ${job.name} belongs to attempt ${job.run_attempt}`);
  // Only explicitly conditional publish can be absent on non-tag runs.
  if(job.conclusion==='skipped'&&job.name==='publish')continue;
  if(job.status!=='completed'||job.conclusion!=='success'||!job.completed_at)violations.push(`job ${job.name} incomplete/failed`);
  if(!job.steps?.length)violations.push(`job ${job.name} missing steps`);
  for(const s of job.steps||[]){
   if(s.conclusion==='skipped')continue;
   const seconds=(instant(s.completed_at)-instant(s.started_at))/1000;
   if(!Number.isFinite(seconds)||seconds<0||seconds>=60)violations.push(`${job.name}/${s.name}: ${seconds}s must be <60s`);
   if(s.conclusion!=='success')violations.push(`${job.name}/${s.name}: ${s.conclusion}`);
  }
 }
 return {pass:!violations.length,elapsed,violations};
}
export function parseAttempt(value){
 const attempt=typeof value==='number'?value:/^\d+$/.test(value??'')?Number(value):NaN;
 if(!Number.isInteger(attempt)||attempt<1)throw new Error('invalid run attempt');
 return attempt;
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 const [id,sha,mode,attemptArg]=process.argv.slice(2);const repo=process.env.GITHUB_REPOSITORY;
 if(!['preflight','completed'].includes(mode))throw new Error('audit mode required');
 if(!/^\d+$/.test(id||'')||!repo)throw new Error('run id and repository required');
 const api=p=>JSON.parse(execFileSync('gh',['api',`repos/${repo}/${p}`],{encoding:'utf8',timeout:10000,maxBuffer:16*1024*1024}));
 // Audit exactly one attempt: an explicit one (the completed-run listener
 // passes its triggering attempt), preflight's own, or else the latest.
 const attempt=parseAttempt(attemptArg??(mode==='preflight'?process.env.GITHUB_RUN_ATTEMPT:api(`actions/runs/${id}`).run_attempt));
 const run=api(`actions/runs/${id}/attempts/${attempt}`);const jobs=[];
 for(let page=1;;page++){const data=api(`actions/runs/${id}/attempts/${attempt}/jobs?per_page=100&page=${page}`);jobs.push(...data.jobs);if(data.jobs.length<100)break;}
 // Preflight excludes only its own live job, located by its exact Actions name.
 const current=mode==='preflight'?jobs.find(j=>j.name==='accept'&&j.status==='in_progress'):undefined;
 if(mode==='preflight'&&!current)throw new Error('missing current acceptance job');
 const result=audit(run,jobs,{sha,attempt,preflight:mode==='preflight',reserve:acceptanceReserve(process.env.GITHUB_EVENT_NAME,process.env.GITHUB_REF),currentJob:current?.id,expectedJobs:releaseJobCount()-(mode==='preflight'?1:0)});
 console.log(JSON.stringify({attempt,...result},null,2));await writeFile('budget-audit.json',JSON.stringify({attempt,...result},null,2));
 if(!result.pass)process.exitCode=1;
 else if(mode==='preflight'&&process.env.GITHUB_ENV)await appendFile(process.env.GITHUB_ENV,`CI_RUN_DEADLINE=${runDeadline(run)}\n`);
}
