// Disposable native measurement; exact original assertions, no CI acceptance claim.
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,realpath,mkdir,rm,readFile,copyFile,chmod} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import path from 'node:path';
import os from 'node:os';
const root=path.resolve('dist/fixture-probe'),receipt=JSON.parse(await readFile(path.join(root,'receipt.json'),'utf8'));
if(receipt.revision!==process.env.GITHUB_SHA||receipt.arch!==({x64:'amd64',arm64:'arm64'})[process.arch]||process.platform!=='win32')throw new Error('native source/architecture mismatch');
for(const [name,digest] of Object.entries(receipt.files))if(createHash('sha256').update(await readFile(path.join(root,name))).digest('hex')!==digest)throw new Error('binary hash mismatch');
console.log(JSON.stringify({source:receipt.revision,cpus:os.availableParallelism(),memory:os.totalmem(),mode:process.argv[2]}));
const image=await mkdtemp(path.join(await realpath(os.tmpdir()),'vecnet-r14-image-'));
for(const name of ['cli.test.exe','httpapi.test.exe','test2json.exe']){await copyFile(path.join(root,name),path.join(image,name));await chmod(path.join(image,name),0o700);}
const isolated=async task=>{
 const {pkg,filter}=typeof task==='string'?{pkg:'cli',filter:task}:task;
 const temporary=await mkdtemp(path.join(await realpath(os.tmpdir()),'vecnet-r14-probe-'));
 const env={...process.env,GOMAXPROCS:'2'};
 for(const [key,leaf] of Object.entries({HOME:'home',USERPROFILE:'home',APPDATA:'config',LOCALAPPDATA:'data',TMPDIR:'tmp',TMP:'tmp',TEMP:'tmp',XDG_CONFIG_HOME:'config',XDG_CACHE_HOME:'cache',XDG_DATA_HOME:'data',XDG_STATE_HOME:'state',XDG_RUNTIME_DIR:'run'})){
  env[key]=path.join(temporary,leaf);await mkdir(env[key],{recursive:true,mode:0o700});
 }
 const started=Date.now();
 try{
  await new Promise((resolve,reject)=>{
   const child=spawn(path.join(image,'test2json.exe'),['-t','-p','github.com/XertroV/vecnet/internal/'+pkg,path.join(image,pkg+'.test.exe'),'-test.v=test2json','-test.count=1','-test.timeout=50s','-test.run',filter],{cwd:path.resolve('internal',pkg),env,stdio:'inherit'});
   child.once('error',reject);child.once('close',(code,signal)=>code===0?resolve():reject(new Error(`filter=${filter} code=${code} signal=${signal}`)));
  });
 }finally{console.log(JSON.stringify({filter,elapsed_ms:Date.now()-started}));await rm(temporary,{recursive:true,force:true});}
};
const sse={pkg:'httpapi',filter:'^TestSlowSSEConsumerDoesNotBlockSend$'};
const modes={startup:[{pkg:'cli',filter:'^TestCodexHookWindowsInstalledCommandPreservesArgv$'}],sse:[sse],loaded:[sse,{pkg:'cli',filter:'^TestCodexHookWindowsInstalledCommandPreservesArgv$'},{pkg:'cli',filter:'^TestStateConcurrentWriters$'},{pkg:'cli',filter:'^TestRouteStatsRelocationRefusals$'}]};
const selected=modes[process.argv[2]];if(!selected)throw new Error('unknown diagnostic mode');
const result=[];
if(process.argv[2]==='neighbors2'){
 let next=0;await Promise.all(Array.from({length:2},async()=>{while(next<selected.length){const i=next++;try{await isolated(selected[i]);result[i]={status:'fulfilled'};}catch(reason){result[i]={status:'rejected',reason};}}}));
}else result.push(...await Promise.allSettled(selected.map(isolated)));
for(const r of result)if(r.status==='rejected'){console.error(r.reason);process.exitCode=1;}
await rm(image,{recursive:true,force:true});
