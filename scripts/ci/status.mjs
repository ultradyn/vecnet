// A workflow_run job belongs to default-branch HEAD. Its explicit commit status
// belongs instead to the triggering tested SHA. Policy stays on trusted master.
import { execFileSync, spawnSync } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
export async function publishAcceptance({repo,event,api,evaluate}) {
 if(!/^[\w.-]+\/[\w.-]+$/.test(repo)||!/^[a-f0-9]{40}$/.test(event?.head_sha)||!Number.isInteger(event.id)||!Number.isInteger(event.run_attempt))throw new Error('invalid triggering run');
 const route=`repos/${repo}/statuses/${event.head_sha}`;
 const target=`https://github.com/${repo}/actions/runs/${event.id}/attempts/${event.run_attempt}`;
 const report=(state,description)=>api('POST',route,{state,context:'Release budget acceptance',target_url:target,description:description.slice(0,140)});
 try {
  await report('pending',`Auditing run ${event.id}, attempt ${event.run_attempt}`);
  const run=await api('GET',`repos/${repo}/actions/runs/${event.id}`);
  if(run.head_sha!==event.head_sha||run.id!==event.id||run.run_attempt!==event.run_attempt)throw new Error('triggering source/run/attempt mismatch');
  const result=await evaluate(run);
  const latest=await api('GET',`repos/${repo}/actions/runs/${event.id}`);
  if(latest.head_sha!==event.head_sha||latest.run_attempt!==event.run_attempt)throw new Error('source run changed during audit');
  if(typeof result?.pass!=='boolean'||!Array.isArray(result.violations))throw new Error('invalid audit result');
  await report(result.pass?'success':'failure',result.pass?`Run ${event.id}/${event.run_attempt}: <300s run, every step <60s`:`Run ${event.id}/${event.run_attempt}: budget or test failure`);
  return result;
 }catch(error){
  // If GitHub itself is unavailable this write may fail too. The pending or
  // absent required status remains non-success, and the audit job fails.
  try{await report('failure',`Run ${event.id}/${event.run_attempt}: audit/API error`);}catch{}
  throw error;
 }
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 const event=JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH,'utf8')).workflow_run;
 const repo=process.env.GITHUB_REPOSITORY;
 const api=async(method,route,body)=>JSON.parse(execFileSync('gh',['api','--method',method,route,...(body?['--input','-']:[])],{input:body?JSON.stringify(body):undefined,encoding:'utf8',timeout:10000,maxBuffer:16*1024*1024}));
 const result=await publishAcceptance({repo,event,api,evaluate:async run=>{
  // Audit the exact triggering attempt, never whichever attempt is latest.
  const command=spawnSync(process.execPath,[fileURLToPath(new URL('./audit.mjs',import.meta.url)),String(run.id),run.head_sha,'completed',String(run.run_attempt)],{encoding:'utf8',timeout:35000});
  if(command.error||command.signal)throw command.error||new Error('audit terminated');
  console.log(command.stdout);if(command.stderr)console.error(command.stderr);
  if(command.status!==0&&command.status!==1)throw new Error('audit command failed');
  return JSON.parse(command.stdout);
 }});
 if(!result.pass)process.exitCode=1;
}
