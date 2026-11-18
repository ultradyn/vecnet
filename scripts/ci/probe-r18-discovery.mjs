// Disposable diagnostic only: never merge this workflow or script.
import {consumeBundle,bundleDiscoveryJobs} from './go-bundle.mjs';
import {restoreSource} from './source-archive.mjs';
import {mkdir,mkdtemp,readFile,rm,writeFile} from 'node:fs/promises';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {availableParallelism,tmpdir} from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
const width=Number(process.env.WIDTH),sha=process.env.BUNDLE_SHA;
assert([2,4].includes(width));assert.equal(process.platform,'win32');
const work=await mkdtemp(path.join(tmpdir(),'vecnet-discovery-probe-'));
const root=path.join(work,'source');await mkdir(root);
try {
 await restoreSource(path.resolve('dist/go-tests'),sha,root);
 for(const [key,leaf] of Object.entries({HOME:'home',USERPROFILE:'home',APPDATA:'config',LOCALAPPDATA:'data',TMPDIR:'tmp',TMP:'tmp',TEMP:'tmp',XDG_CONFIG_HOME:'config',XDG_CACHE_HOME:'cache',XDG_DATA_HOME:'data',XDG_STATE_HOME:'state',XDG_RUNTIME_DIR:'run'})){
  process.env[key]=path.join(work,leaf);await mkdir(process.env[key],{recursive:true});
 }
 process.env.GITHUB_ACTIONS=width===4?'true':'false';
 assert.equal(bundleDiscoveryJobs(process.env),width);
 const start=performance.now();
 const result=await consumeBundle(path.resolve('dist/go-tests',`win32-${process.arch}`),{sha,platform:`win32-${process.arch}`,root,temporary:path.join(work,'native')});
 const elapsedMs=performance.now()-start;
 const roots=result.inventory.flatMap(p=>p.names.map(n=>p.pkg+'/'+n)).sort();
 const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
 const receipt={helperRevision:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),helperHash:hash(await readFile(new URL('./go-bundle.mjs',import.meta.url))),bundleRevision:sha,width,cpus:availableParallelism(),platform:process.platform,arch:process.arch,node:process.version,GOMAXPROCS:process.env.GOMAXPROCS,packages:result.inventory.length,roots:roots.length,inventory:result.inventory.map(({pkg,names})=>({pkg,names})),rootHash:hash(JSON.stringify(roots)),elapsedMs};
 await writeFile('discovery-result.json',JSON.stringify(receipt,null,2));console.log(JSON.stringify(receipt));
} finally {await rm(work,{recursive:true,force:true});}
