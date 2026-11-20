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
export function audit(run,jobs,{sha,now=new Date().toISOString(),preflight=false,reserve=0,currentJob,expectedJobs}={}) {
 const violations=[];const instant=value=>Date.parse(value);
 if(!sha||run.head_sha!==sha)violations.push('source SHA mismatch');
 if(!preflight&&(run.status!=='completed'||run.conclusion!=='success'))violations.push('run not completed successfully');
 if(!Number.isFinite(instant(run.created_at)))violations.push('missing run creation timestamp');
 const included=jobs.filter(j=>j.id!==currentJob);
 if(!included.length)violations.push('missing jobs');
 if(expectedJobs && included.length!==expectedJobs)violations.push(`missing/unexpected jobs: ${included.length}, expected ${expectedJobs}`);
 const ends=included.map(j=>instant(j.completed_at)).filter(Number.isFinite);
 const elapsed=((preflight?instant(now):Math.max(...ends))-instant(run.created_at))/1000;
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
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 const [id,sha,mode]=process.argv.slice(2);const repo=process.env.GITHUB_REPOSITORY;
 if(!['preflight','completed'].includes(mode))throw new Error('audit mode required');
 if(!/^\d+$/.test(id||'')||!repo)throw new Error('run id and repository required');
 const api=p=>JSON.parse(execFileSync('gh',['api',`repos/${repo}/${p}`],{encoding:'utf8',timeout:10000,maxBuffer:16*1024*1024}));
 const run=api(`actions/runs/${id}`);const jobs=[];
 for(let page=1;;page++){const data=api(`actions/runs/${id}/jobs?filter=latest&per_page=100&page=${page}`);jobs.push(...data.jobs);if(data.jobs.length<100)break;}
 // Preflight excludes only its own live job, located by its exact Actions name.
 const current=mode==='preflight'?jobs.find(j=>j.name==='accept'&&j.status==='in_progress'):undefined;
 if(mode==='preflight'&&!current)throw new Error('missing current acceptance job');
 const result=audit(run,jobs,{sha,preflight:mode==='preflight',reserve:acceptanceReserve(process.env.GITHUB_EVENT_NAME,process.env.GITHUB_REF),currentJob:current?.id,expectedJobs:releaseJobCount()-(mode==='preflight'?1:0)});
 console.log(JSON.stringify(result,null,2));await writeFile('budget-audit.json',JSON.stringify(result,null,2));
 if(!result.pass)process.exitCode=1;
 else if(mode==='preflight'&&process.env.GITHUB_ENV)await appendFile(process.env.GITHUB_ENV,`CI_RUN_DEADLINE=${Date.parse(run.created_at)+299000}\n`);
}
