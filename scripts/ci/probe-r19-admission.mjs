// Diagnostic only: use the exact R18 test bundle and an unmodified whole root.
// Each window changes only simultaneous ordinary admission while its target runs.
import assert from 'node:assert/strict';
import {spawn, execFile} from 'node:child_process';
import {createHash} from 'node:crypto';
import {constants, createReadStream} from 'node:fs';
import {copyFile, mkdir, mkdtemp, readFile, realpath, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {createInterface} from 'node:readline';
import {fileURLToPath} from 'node:url';
import {promisify} from 'node:util';
import {restoreSource} from './source-archive.mjs';
import {bounded} from './schedule.mjs';

const sourceRun=44313480033, sourceAttempt=1;
const head='ef6268efaca191cadce4bbef4ca007eca195e400';
const revision='ea7f480ecd83a9ea7b1551bef096bc8c0dc643b3';
const artifacts={
  'win32-x64':{id:14432165338,sha256:'1832c22ed76c46aad10be3fae162757cdbfbba9eaf1003d7adb1984b520cb530'},
  'win32-arm64':{id:14431621105,sha256:'1d3a47ca783d87c2fe939d03b490fa39cbc53d1256614093b95677900acfe8df'},
};
const repo='github.com/XertroV/vecnet/', cli='internal/cli';
const driftNeighbors=[
  ['internal/cli','TestInviteHubURLResponseRefusesUnsafe'],
  ['internal/hub','TestR2OAuthCompletionRevalidatesInsideTransactionAfterHEAD'],
  ['internal/hub','TestBackfillSparseAndEmptyEligibilityUsesBoundedScan'],
  ['cmd/vecnet-hub','TestHubURLFlagOverridesEnvironment'],
  ['internal/localruntime','TestWindowsReenableDoesNotRestart'],
];
export const scenarios={
  enabled:{r18Jobs:[134834458994,134834485177],root:'TestLauncherWindowsRepairPendingDriftEnabled',neighbors:[
    ['cmd/vecnet-hub','TestEventsRefreshIntervalFlagAndEnv'],
    ['internal/cli','TestStateConcurrentWriters'],
    ['cmd/vecnet-hub','TestStorageSwitchesIndependent'],
    ['internal/httpapi','TestSSEReplaysLargeBacklogOnOneConnection'],
    ['cmd/vecnet-hub','TestMCPAllowedOriginsFlagAndEnv'],
  ]},
  intent:{r18Jobs:[134834485373],root:'TestLauncherWindowsRepairPendingDriftIntent',neighbors:[
    ['internal/opseed','TestSeedMany'],
    ['internal/opseed','TestSeedScale'],
    ['internal/store','TestR2MigrationVerificationEvidenceCheck'],
    ['internal/hub','TestR2OAuthDownloadRevalidatesAfterProviderWait'],
    ['internal/events','TestRouteAttemptEventsOutcomePopulation'],
  ]},
  drift:{r18Jobs:[134834459137],root:'TestLauncherWindowsRepairRejectsTaskAndPublicationDrift',neighbors:driftNeighbors},
  controller:{r18Jobs:[134834459137],root:'TestLauncherWindowsTaskControllerRejectsConcurrentChanges',neighbors:driftNeighbors},
};
const emit=(event,fields)=>console.log(`r19 ${JSON.stringify({event,...fields})}`);
const exec=promisify(execFile);
export async function verifyFile(file,expected) {
  assert.match(expected,/^[0-9a-f]{64}$/);
  const hash=createHash('sha256');
  for await(const bytes of createReadStream(file))hash.update(bytes);
  const digest=hash.digest('hex');
  assert.equal(digest,expected,`SHA256 mismatch: ${path.basename(file)}`);
  return digest;
}
export function verifyReceipt(receipt,platform) {
  assert.equal(receipt.revision,revision,'R18 tested merge revision mismatch');
  assert.equal(receipt.platform,platform,'R18 platform mismatch');
  assert.equal(receipt.groups?.length,6,'incomplete R18 bundle');
  return receipt;
}

// One target, up to cap-1 simultaneous original neighbors. After the target
// exits admit the rest. Retain every failure and drain before reporting.
export async function runWindow(cap,run) {
  assert(cap===3||cap===6,'invalid admission cap');
  const active=new Map(), settled=[];
  let next=0, width=cap;
  while(next<6||active.size){
    while(next<6&&active.size<width){
      const i=next++;
      active.set(i,Promise.resolve().then(()=>run(i)).then(value=>({i,value}),error=>({i,error})));
    }
    const result=await Promise.race(active.values());
    active.delete(result.i);
    settled[result.i]=result;
    if(result.i===0)width=6;
  }
  const error=settled.find(result=>Object.hasOwn(result,'error'));
  if(error)throw error.error;
  return settled.map(result=>result.value);
}

async function privateProcess(fn) {
  const dir=await mkdtemp(path.join(await realpath(os.tmpdir()),'vecnet-ci-go-process-'));
  try {
    const env={...process.env};
    for(const [name,leaf] of Object.entries({HOME:'home',USERPROFILE:'home',APPDATA:'config',LOCALAPPDATA:'data',TMPDIR:'tmp',TMP:'tmp',TEMP:'tmp',XDG_CONFIG_HOME:'config',XDG_CACHE_HOME:'cache',XDG_DATA_HOME:'data',XDG_STATE_HOME:'state',XDG_RUNTIME_DIR:'run'})){
      env[name]=path.join(dir,leaf);
      await mkdir(env[name],{recursive:true,mode:0o700});
    }
    return await fn(env);
  } finally {await rm(dir,{recursive:true,force:true});}
}

const work=()=>path.resolve('.r19-admission');
const json=async file=>JSON.parse(await readFile(file,'utf8'));
export function bundlePaths(folder,platform) {
  const sourceRoot=path.join(folder,'bundle');
  return {sourceRoot,binaryRoot:path.join(sourceRoot,platform)};
}
async function unpack(platform) {
  const folder=work(), artifact=artifacts[platform];
  const archive=path.join(folder,'bundle.zip'), source=path.join(folder,'source');
  const {sourceRoot,binaryRoot}=bundlePaths(folder,platform);
  await verifyFile(archive,artifact.sha256);
  await mkdir(sourceRoot);await mkdir(source);
  await exec('tar.exe',['-xf',archive,'-C',sourceRoot]);
  verifyReceipt(await json(path.join(binaryRoot,'bundle.json')),platform);
  const sourceReceipt=await json(path.join(sourceRoot,'source.json'));
  assert.equal(sourceReceipt.scope,'go');
  await restoreSource(sourceRoot,revision,source);
  emit('provenance',{sourceRun,sourceAttempt,head,revision,platform,artifact,sourceArchiveSha256:sourceReceipt.sha256,
    diagnosticRun:process.env.GITHUB_RUN_ID,diagnosticAttempt:process.env.GITHUB_RUN_ATTEMPT,diagnosticRevision:process.env.GITHUB_SHA,
    node:process.version,arch:process.arch});
}

async function discover(platform) {
  const folder=work(), source=path.join(folder,'source');
  const {binaryRoot}=bundlePaths(folder,platform);
  const receipt=verifyReceipt(await json(path.join(binaryRoot,'bundle.json')),platform);
  const pending=receipt.groups.flat();
  assert.equal(pending.length,45);assert.equal(new Set(pending.map(row=>row.pkg)).size,45);
  const staged=path.join(await realpath(os.tmpdir()),'vecnet-r19-native');await mkdir(staged);
  const tool=path.join(staged,'test2json.exe');
  await copyFile(path.join(binaryRoot,'test2json.exe'),tool);await verifyFile(tool,receipt.toolHash);
  const inventory=await bounded(pending,async row=>{
    const directory=path.resolve(source,row.directory);
    assert(!path.relative(source,directory).startsWith('..'),'outside archive');
    if(!row.file)return {pkg:row.pkg,names:[],directory,binary:null};
    assert(Number.isInteger(row.group)&&row.group>=0&&row.group<6&&path.basename(row.file)===row.file);
    const binary=path.join(staged,row.file);
    await copyFile(path.join(binaryRoot,String(row.group),row.file),binary,constants.COPYFILE_EXCL);
    await verifyFile(binary,row.sha256);
    const {stdout}=await privateProcess(env=>exec(binary,['-test.list','.'],{cwd:directory,env}));
    const names=stdout.trim().split(/\r?\n/).filter(Boolean);
    assert(names.every(name=>/^(?:Test\w+|Example\w*|Fuzz\w+)$/.test(name))&&new Set(names).size===names.length);
    return {pkg:row.pkg,names,directory,binary,sha256:row.sha256};
  },2);
  assert.equal(inventory.reduce((n,row)=>n+row.names.length,0),2595);
  const file=path.join(folder,'inventory.json');
  await writeFile(file,JSON.stringify({inventory,tool,toolHash:receipt.toolHash}));
  for(const row of inventory)emit('inventory',{pkg:row.pkg,roots:row.names,sha256:row.sha256});
  emit('discovery',{platform,packages:45,roots:2595,toolSha256:receipt.toolHash});
}

async function packet(tool,task,root,role) {
  const start=performance.now();
  let outcome, rootElapsed, admissionError=false;
  const code=await privateProcess(async env=>{
    const child=spawn(tool,['-t','-p',task.pkg,task.binary,'-test.v=test2json','-test.count=1','-test.timeout=50s','-test.run',`^(${root})$`],
      {cwd:task.directory,env,stdio:['ignore','pipe','inherit']});
    const done=new Promise(resolve=>{child.once('error',error=>resolve({error}));child.once('close',code=>resolve({code}));});
    emit('packet-start',{role,pkg:task.pkg,root,sha256:task.sha256});
    let malformed=false;
    for await(const line of createInterface({input:child.stdout,crlfDelay:Infinity})){
      console.log(line);
      let e;
      try {e=JSON.parse(line);} catch {malformed=true;continue;}
      if(e.Test===root&&['pass','fail','skip'].includes(e.Action)){outcome=e.Action;rootElapsed=e.Elapsed;}
      if(e.Output?.includes('launcher requires Node 22.18 or newer'))admissionError=true;
    }
    const result=await done;
    if(result.error)throw result.error;
    if(malformed)throw new Error(`invalid test2json evidence for ${root}`);
    return result.code;
  });
  const result={role,pkg:task.pkg,root,code,outcome:outcome??'incomplete',rootElapsed,wallMs:Math.round(performance.now()-start),admissionError};
  emit('packet-end',result);
  return code===0&&outcome==='pass';
}

async function sample(cap,name) {
  assert(Object.hasOwn(scenarios,name),'invalid scenario');
  const {inventory,tool}=await json(path.join(work(),'inventory.json'));
  const {root,neighbors,r18Jobs}=scenarios[name];
  const packets=[[cli,root],...neighbors].map(([pkg,root])=>{
    const task=inventory.find(row=>row.pkg===repo+pkg&&row.names.includes(root));
    assert(task?.binary,`missing original discovered root ${pkg}/${root}`);
    return {task,root};
  });
  emit('window',{scenario:name,cap,r18Jobs,packets:packets.map(({task,root})=>[task.pkg,root])});
  const passes=await runWindow(cap,i=>packet(tool,packets[i].task,packets[i].root,i===0?'target':'neighbor'));
  emit('window-end',{scenario:name,cap,targetPass:passes[0],neighborsPass:passes.slice(1)});
  if(!passes.every(Boolean))process.exitCode=1;
}

if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
  const [command,arg,name]=process.argv.slice(2);
  const platform=`${process.platform}-${process.arch}`;
  assert(Object.hasOwn(artifacts,platform)&&process.versions.node.split('.')[0]==='24'&&process.env.GOMAXPROCS==='1','requires native Windows Node 24 and GOMAXPROCS=1');
  if(command==='unpack'&&arg===platform)await unpack(platform);
  else if(command==='discover'&&arg===platform)await discover(platform);
  else if(command==='sample')await sample(Number(arg),name);
  else throw new Error('expected unpack|discover <native platform> or sample <cap> <scenario>');
}
