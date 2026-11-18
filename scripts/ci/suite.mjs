// Only CI scheduling lives here; assertions and platform selection remain in
// the original suites. Run this whole command under budget.mjs, not each child
// separately: discovery/compilation and all packages share the step deadline.
import { execFileSync, spawn } from 'node:child_process';
import { mkdir, mkdtemp, realpath, chmod, readFile, writeFile, access, rm } from 'node:fs/promises';
import { globSync } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { performance } from 'node:perf_hooks';
import { consumeBundle } from './go-bundle.mjs';
import { partition } from './partition.mjs';
import { bounded, planGo, nativeGoLanes, goGroups, goCompileJobs } from './schedule.mjs';
import {planNode} from './node-schedule.mjs';
import {fileURLToPath} from 'node:url';

const [kind, laneText = '0', groupText = '0'] = process.argv.slice(2);
const lane = Number(laneText), group = Number(groupText);
const go = args => execFileSync('go', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] });
async function privateTemporary(prefix) {
  const directory = await mkdtemp(path.join(await realpath(os.tmpdir()), prefix));
  await chmod(directory, 0o700);
  return directory;
}
if (kind === 'prepare-go') {
  const jobs=goCompileJobs(process.env.CI_GO_COMPILE_JOBS);
  const stages = [
    ['mod', 'download'],
    ['build','-mod=readonly','-p',jobs,'-trimpath','std'],
    ['build','-mod=readonly','-p',jobs,'-trimpath','modernc.org/sqlite'],
    ['build','-mod=readonly','-p',jobs,'-trimpath','charm.land/bubbletea/v2','github.com/santhosh-tekuri/jsonschema/v6','github.com/modelcontextprotocol/go-sdk/mcp'],
  ];
  if (!stages[lane]) throw new Error('invalid prepare stage');
  process.stdout.write(go(stages[lane]));
} else if (kind === 'consume-go') {
  const platform=`${process.platform}-${process.arch}`,temporary=await privateTemporary('vecnet-ci-go-native-');
  const bundle=await consumeBundle(path.resolve('dist/go-tests',platform),{sha:process.env.GITHUB_SHA,platform,temporary});
  await mkdir('.ci',{recursive:true});
  for(let i=0;i<6;i++)await writeFile(`.ci/go-${i}.json`,JSON.stringify(bundle.inventory.filter((_,n)=>n%6===i).map(row=>({...row,temporary}))));
  await writeFile('.ci/test2json',bundle.test2json);
  console.log(`Go native bundle: ${bundle.inventory.length} packages, ${bundle.inventory.reduce((n,p)=>n+p.names.length,0)} roots`);
} else if (kind === 'discover-go') {
  partition([], lane, 6);
  const metadata=go(['list','-mod=readonly','-f','{{.ImportPath}}\t{{.Dir}}\t{{len .TestGoFiles}}\t{{len .XTestGoFiles}}','./...']).trim().split(/\r?\n/).map(line=>{const [ImportPath,Dir,internal,external]=line.split('\t');return {ImportPath,Dir,hasTests:Number(internal)+Number(external)>0};});
  const packages=metadata.map(p=>p.ImportPath);
  const selected=partition(packages,lane,6);
  // The batch compile keeps Go's parallel package scheduler and avoids one
  // toolchain invocation/relink per package per root group.
  const output=selected.length ? await privateTemporary('vecnet-ci-go-discovery-') : undefined;
  const basenames=selected.map(pkg=>path.basename(pkg));
  const unique=basenames.filter((name,i)=>basenames.indexOf(name)===i);
  const batch=unique.length===selected.length;
  if(selected.length&&batch)go(['test','-mod=readonly','-p','2','-trimpath','-c','-o',output+path.sep,...selected]);
  const inventory=[];
  for(const pkg of selected){
    const meta=metadata.find(p=>p.ImportPath===pkg),directory=meta.Dir;
    const binary=path.join(output,(batch?path.basename(pkg):pkg.replace(/[^A-Za-z0-9_-]/g,'_'))+'.test'+(process.platform==='win32'?'.exe':''));
    if(!batch)go(['test','-mod=readonly','-p','2','-trimpath','-c','-o',binary,pkg]);
    const hasTests=meta.hasTests;
    if(!hasTests){inventory.push({pkg,names:[],directory,binary:null,temporary:output});continue;}
    // A missing binary for a package with test files must be RED, not a silent
    // empty-inventory success. Compilation has just returned successfully.
    await access(binary);
    const listing=execFileSync(binary,['-test.list','.'],{cwd:directory,encoding:'utf8'});
    const names=listing.trim().split(/\r?\n/).filter(Boolean);
    if(names.some(name=>!/^(?:Test\w+|Example\w*|Fuzz\w+)$/.test(name)))throw new Error('invalid binary discovery');
    inventory.push({pkg,names,directory,binary,temporary:output});
  }
  await mkdir('.ci', { recursive: true });
  await writeFile(`.ci/go-${lane}.json`, JSON.stringify(inventory));
  console.log(`Go inventory: ${inventory.length} packages, ${inventory.reduce((n, p) => n + p.names.length, 0)} root tests/examples/fuzz targets`);
} else if (kind === 'go') {
  const goLanes=nativeGoLanes(process.platform);
  partition([], lane, goLanes); partition([], group, goGroups);
  const inventory = (await Promise.all(Array.from({length:6},(_,i)=>readFile(`.ci/go-${i}.json`,'utf8')))).flatMap(JSON.parse);
  const timings=JSON.parse(await readFile(new URL('./go-timings.json',import.meta.url),'utf8'));
  const tasks=planGo(inventory,timings[process.platform==='win32'?'windows':'linux'],{lanes:goLanes})[group*goLanes+lane];
  let tool;try{tool=(await readFile('.ci/test2json','utf8')).trim();}catch(error){if(error.code!=='ENOENT')throw error;}
  await bounded(tasks,async ({pkg,selected,split,binary,directory,cost})=>{
    const filter=split?['-test.run',`^(${selected.join('|')})$`]:[];
    const started=performance.now();
    if(binary){
      const privateRoot=await privateTemporary('vecnet-ci-go-process-');
      try {
        const env={...process.env};
        for(const [key,leaf] of Object.entries({HOME:'home',USERPROFILE:'home',APPDATA:'config',LOCALAPPDATA:'data',TMPDIR:'tmp',TMP:'tmp',TEMP:'tmp',XDG_CONFIG_HOME:'config',XDG_CACHE_HOME:'cache',XDG_DATA_HOME:'data',XDG_STATE_HOME:'state',XDG_RUNTIME_DIR:'run'})){
          env[key]=path.join(privateRoot,leaf);await mkdir(env[key],{recursive:true,mode:0o700});
        }
        await new Promise((resolve,reject)=>{
          const child=spawn(tool||'go',[...(tool?[]:['tool','test2json']),'-t','-p',pkg,binary,'-test.v=test2json','-test.count=1','-test.timeout=50s',...filter],{cwd:directory,stdio:'inherit',env});
          child.once('error',reject);
          child.once('close',(code,signal)=>code===0?resolve():reject(new Error(`${pkg} failed: exit=${code} signal=${signal}`)));
        });
      } finally { await rm(privateRoot,{recursive:true,force:true}); }
    }
    console.log(`ci-package pkg=${pkg} roots=${selected.length} elapsed_ms=${Math.round(performance.now()-started)} estimated_s=${cost.toFixed(3)}`);
  },process.platform==='win32'?4:2);
  console.log(`Go lane=${lane}/${goLanes} group=${group}/${goGroups} packages=${tasks.length} roots=${tasks.reduce((n,t)=>n+t.selected.length,0)}`);
} else if (kind === 'node' || kind === 'e2e') {
  const patterns = kind === 'node' ? ['connectors/hub-client/test/*.test.ts', 'connectors/launcher/test/*.test.ts', 'connectors/launcher/test/backends/*.test.ts'] : ['connectors/launcher/test/e2e/*.test.ts'];
  const all = patterns.flatMap(pattern => globSync(pattern));
  if (!all.length) throw new Error('empty Node inventory');
  if(kind==='e2e'){
    const files=partition(all,group,4);
    if(!files.length)throw new Error('empty Node group');
    console.log(`${kind} group=${group}/4 files=${files.length}/${all.length}: ${files.join(', ')}`);
    execFileSync(process.execPath,['--disable-warning=ExperimentalWarning','--test','--test-reporter=tap','--test-concurrency=2',...files],{stdio:'inherit'});
  }else{
    partition([],group,4);
    const timings=JSON.parse(await readFile(new URL('./node-timings.json',import.meta.url),'utf8'));
    const tasks=planNode(all,timings.profiles[process.platform==='win32'?'windows':'linux'])[group];
    console.log(`node group=${group}/4 tasks=${tasks.length} discoveredFiles=${all.length}`);
    await bounded(tasks,async task=>{
      const started=performance.now();
      const preload=task.shards>1?['--import',new URL('./node-shard-register.mjs',import.meta.url).href]:[];
      await new Promise((resolve,reject)=>{
        const child=spawn(process.execPath,[...preload,'--disable-warning=ExperimentalWarning','--test','--test-reporter=tap',task.file],{
          stdio:'inherit',env:{...process.env,CI_NODE_SHARD:String(task.shard),CI_NODE_SHARDS:String(task.shards)},
        });
        child.once('error',reject);
        child.once('close',(code,signal)=>code===0?resolve():reject(new Error(`${task.file} failed: exit=${code} signal=${signal}`)));
      });
      console.log(`ci-node-file file=${task.file} shard=${task.shard}/${task.shards} elapsed_ms=${Math.round(performance.now()-started)}`);
    });
  }
} else throw new Error(`unknown suite: ${kind}`);
