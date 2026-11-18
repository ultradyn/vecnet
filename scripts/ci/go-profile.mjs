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
  if(e.Action==='start') {
   const batch=active.get(e.Package);
   if(batch){batch.count++;batch.overlapped=true;}
   else active.set(e.Package,{count:1,overlapped:false,pending:new Set(),rootElapsed:0});
  }
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
    run.rootElapsed+=e.Elapsed;
   } else censored.push({pkg:e.Package,test:e.Test,action:e.Action});
  } else {
   if(e.Action==='pass') {
    if(!Number.isFinite(e.Elapsed)||e.Elapsed<0)throw new Error('invalid package elapsed');
    maximum(packageElapsed,e.Package,positive(e.Elapsed));
    // Only package PASS contributes process elapsed. Root fallbacks are
    // derived below from complete root observations, not process/skip ratios.
    // test2json identifies the package, not the process. Do not guess which
    // root timings belong to a package completion when its packets overlap.
    const extra=e.Elapsed-run.rootElapsed;
    if(!run.overlapped&&extra>=0)overhead.push(extra);
   } else censored.push({pkg:e.Package,action:e.Action});
   if(--run.count===0){
    for(const test of run.pending)censored.push({pkg:e.Package,test,action:'unfinished'});
    active.delete(e.Package);
   }
  }
 }
 for(const [pkg,run] of active) {
  for(const test of run.pending)censored.push({pkg,test,action:'unfinished'});
  for(let i=0;i<run.count;i++)censored.push({pkg,action:'unfinished'});
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
 const readJSON=async name=>JSON.parse(await readFile(path.join(directory,name),'utf8'));
 const index=await readJSON('index.json');
 const {jobs,total_count}=await readJSON('jobs.json');
 const goJobs=rows=>rows.filter(row=>row.name.startsWith('test-go-'));
 const selected=goJobs(index),metadata=goJobs(jobs);
 // Explicit archive geometries, independent of the current scheduler policy.
 const names=linux=>['linux','windows-x64','windows-arm'].flatMap(platform=>
  Array.from({length:platform==='linux'?linux:6},(_,lane)=>`test-go-${platform} (${lane})`));
 const exact=(rows,expected)=>rows.length===expected.length&&
  new Set(rows.map(row=>row.name)).size===expected.length&&
  new Set(rows.map(row=>row.id)).size===expected.length&&
  rows.every(row=>Number.isSafeInteger(row.id)&&row.id>0&&expected.includes(row.name));
 const geometry=[2,6].map(names).find(expected=>exact(selected,expected)&&exact(metadata,expected));
 if(!geometry||(total_count!==undefined&&total_count!==jobs.length))throw new Error('expected exact full geometry (14 or 18 native Go logs)');
 const current=geometry.length===14;
 // Historical 18-job indexes carry conclusions, but no run receipt or hashes.
 // The current archive also binds the separate run receipt and raw log bytes.
 if(current){
  const receipt=await readJSON('run.json');
  if(receipt.id!==run||receipt.head_sha!==sha||receipt.run_attempt!==attempt||receipt.status!=='completed')throw new Error('source/run binding mismatch');
 }
 for(const row of selected) {
  const job=metadata.find(job=>job.id===row.id);
  if(!job||job.name!==row.name||job.head_sha!==sha||job.run_id!==run||job.run_attempt!==attempt||job.status!=='completed'||
   !['success','failure'].includes(job.conclusion)||((!current||row.conclusion!==undefined)&&job.conclusion!==row.conclusion))throw new Error('source/run/job binding mismatch');
 }
 const logs=await Promise.all(selected.map(async row=>{
  const data=await readFile(path.join(directory,`${row.id}.log`));
  const sha256=createHash('sha256').update(data).digest('hex');
  if((current||row.sha256!==undefined||row.bytes!==undefined)&&(row.bytes!==data.length||row.sha256!==sha256))throw new Error(`native Go log hash/length mismatch: ${row.name}`);
  return {id:row.id,name:row.name,sha256,log:data.toString('utf8')};
 }));
 const result=buildGoProfile(logs);
 result.source={sha,run,attempt,logs:logs.map(({id,name,sha256})=>({id,name,sha256}))};
 result.evidence=`Archived native run${run} attempt${attempt} source${sha}, all${geometry.length} native Go logs. Complete PASS root maxima (Windows maxx64/ARM); complete package slice elapsed maxima and median complete-root fallbacks. Failed/unfinished roots and package executions explicitly censored. Zero-rounded PASS costs floored0.01s. Startup median from nonnegative package PASS elapsed minus summed roots, excluding overlapping same-package processes. No postchange native measurements or predicted gains; discovery supplies coverage. Unobserved roots use positive package fallback or0.1s.`;
 await writeFile(output,JSON.stringify(result,null,2)+'\n');
 console.log(JSON.stringify({roots:Object.fromEntries(['linux','windows'].map(p=>[p,Object.values(result[p].roots).reduce((n,r)=>n+Object.keys(r).length,0)])),censored:result.censored.length}));
}
