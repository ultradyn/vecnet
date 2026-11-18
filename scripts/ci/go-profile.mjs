// Replay native test2json logs. Timing is a placement hint, never an inventory.
const positive = n => Math.max(0.01, n);
const maximum = (map, key, n) => { map[key] = Math.max(map[key] ?? 0, n); };
export function observeGo(log) {
 const roots={}, packageElapsed={}, censored=[], active=new Map(), overhead=[];
 for(const line of log.split(/\r?\n/)) {
  const offset=line.indexOf('{"Time":');
  if(offset<0)continue;
  const e=JSON.parse(line.slice(offset));
  if(!e.Package)continue;
  if(e.Action==='start')active.set(e.Package,{pending:new Set(),roots:new Map()});
  const run=active.get(e.Package);
  if(!run)throw new Error(`Go event without package start: ${e.Package}`);
  if(e.Test?.includes('/'))continue;
  if(e.Action==='run' && e.Test)run.pending.add(e.Test);
  if(!['pass','fail','skip'].includes(e.Action))continue;
  if(e.Test) {
   run.pending.delete(e.Test);
   if(e.Action==='pass') {
    if(!Number.isFinite(e.Elapsed)||e.Elapsed<0)throw new Error('invalid root elapsed');
    maximum(roots[e.Package]??={},e.Test,positive(e.Elapsed));
    run.roots.set(e.Test,e.Elapsed);
   } else censored.push({pkg:e.Package,test:e.Test,action:e.Action});
  } else {
   if(e.Action==='pass') {
    if(!Number.isFinite(e.Elapsed)||e.Elapsed<0)throw new Error('invalid package elapsed');
    maximum(packageElapsed,e.Package,positive(e.Elapsed));
    // Only package PASS contributes process elapsed. Root fallbacks are
    // derived below from complete root observations, not process/skip ratios.
    const extra=e.Elapsed-[...run.roots.values()].reduce((a,b)=>a+b,0);
    if(extra>=0)overhead.push(extra);
   } else censored.push({pkg:e.Package,action:e.Action});
   for(const test of run.pending)censored.push({pkg:e.Package,test,action:'unfinished'});
   active.delete(e.Package);
  }
 }
 for(const [pkg,run] of active) {
  for(const test of run.pending)censored.push({pkg,test,action:'unfinished'});
  censored.push({pkg,action:'unfinished'});
 }
 return {roots,packageElapsed,censored,overhead};
}
export function buildGoProfile(logs) {
 const result={linux:{packages:{},roots:{},packageElapsed:{}},windows:{packages:{},roots:{},packageElapsed:{}}};
 const overhead={linux:[],windows:[]},censored=[];
 for(const {name,log} of logs) {
  if(!/^test-go-(linux|windows-x64|windows-arm) \(\d\)$/.test(name))throw new Error(`not a native Go job: ${name}`);
  const platform=name.includes('windows')?'windows':'linux',dest=result[platform],obs=observeGo(log);
  for(const field of ['packageElapsed'])for(const [pkg,n] of Object.entries(obs[field]))maximum(dest[field],pkg,n);
  for(const [pkg,names] of Object.entries(obs.roots))for(const [name,n] of Object.entries(names))maximum(dest.roots[pkg]??={},name,n);
  overhead[platform].push(...obs.overhead);
  censored.push(...obs.censored.map(row=>({job:name,...row})));
 }
 for(const platform of ['linux','windows']) {
  // Median complete PASS root cost is a robust positive unknown-root hint.
  // Keep actual package process elapsed separately for whole small packages.
  for(const [pkg,names] of Object.entries(result[platform].roots)) {
   const values=Object.values(names).sort((a,b)=>a-b);
   result[platform].packages[pkg]=values[Math.floor(values.length/2)];
  }
  const sorted=overhead[platform].sort((a,b)=>a-b);
  result[platform].startup=positive(sorted[Math.floor(sorted.length/2)]??0.01);
 }
 return {...result,censored};
}

// Usage: node scripts/ci/go-profile.mjs <native-log-directory> <output-json> <source-sha> <run-id> <attempt>
// Run only on archived, source-bound complete logs; never fetch/authenticate here.
import {createHash} from 'node:crypto';
import {readFile, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
if(process.argv[1] && path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
 const [directory,output,sha,runText,attemptText]=process.argv.slice(2);
 const run=Number(runText),attempt=Number(attemptText);
 if(!directory||!output||!/^[a-f0-9]{40}$/.test(sha??'')||!Number.isSafeInteger(run)||run<1||!Number.isSafeInteger(attempt)||attempt<1)throw new Error('expected log directory, output JSON, source SHA, run and attempt');
 const index=JSON.parse(await readFile(path.join(directory,'index.json'),'utf8'));
 const jobs=JSON.parse(await readFile(path.join(directory,'jobs.json'),'utf8')).jobs;
 const selected=index.filter(row=>row.name.startsWith('test-go-'));
 if(selected.length!==18 || new Set(selected.map(row=>row.name)).size!==18 || new Set(selected.map(row=>row.id)).size!==18 || selected.some(row=>!/^test-go-(linux|windows-x64|windows-arm) \([0-5]\)$/.test(row.name)) || jobs.filter(job=>job.name.startsWith('test-go-')).length!==18)throw new Error('expected all18 native Go logs');
 for(const row of selected) {
  const job=jobs.find(job=>job.id===row.id);
  if(!job||job.name!==row.name||job.head_sha!==sha||job.run_id!==run||job.run_attempt!==attempt||job.status!=='completed'||job.conclusion!==row.conclusion)throw new Error('source/run/job binding mismatch');
 }
 const logs=await Promise.all(selected.map(async row=>({id:row.id,name:row.name,log:await readFile(path.join(directory,`${row.id}.log`),'utf8')})));
 const result=buildGoProfile(logs);
 result.source={sha,run,attempt,logs:logs.map(({id,name,log})=>({id,name,sha256:createHash('sha256').update(log).digest('hex')}))};
 result.evidence=`Archived native run${run} attempt${attempt} source${sha}, all18 native Go logs. Complete PASS root maxima (Windows maxx64/ARM); complete package slice elapsed maxima and median complete-root fallbacks. Failed/unfinished roots and package executions explicitly censored. Zero-rounded PASS costs floored0.01s. Startup median of nonnegative completed package elapsed minus summed roots. No postchange native measurements or predicted gains; discovery supplies coverage. Unobserved roots use positive package fallback or0.1s.`;
 await writeFile(output,JSON.stringify(result,null,2)+'\n');
 console.log(JSON.stringify({roots:Object.fromEntries(['linux','windows'].map(p=>[p,Object.values(result[p].roots).reduce((n,r)=>n+Object.keys(r).length,0)])),censored:result.censored},null,2));
}
