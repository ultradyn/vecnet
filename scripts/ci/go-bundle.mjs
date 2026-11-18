// Cross-compile complete test packages on existing Linux target producers;
// discover names and execute ONLY on the native consumer. No platform skips.
import { execFileSync, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { bounded, goCompileJobs, goCompileEnvironment } from './schedule.mjs';
const exec = promisify(execFile);
import { createHash } from 'node:crypto';
import { chmod, copyFile, lstat, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { nativeTargets } from '../npm/platforms.mjs';
import { partition } from './partition.mjs';
import { stageSource } from './source-archive.mjs';
import { buildWindowsOwner, readWindowsOwner, stageWindowsOwner } from './windows-owner.mjs';
import {buildCLIFixture,readCLIFixture,stageCLIFixture} from './cli-fixture.mjs';
const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
const go=args=>execFileSync('go',args,{env:goCompileEnvironment(process.env),encoding:'utf8',maxBuffer:16*1024*1024});
function packages(){return go(['list','-mod=readonly','-f','{{.ImportPath}}\t{{.Dir}}\t{{len .TestGoFiles}}\t{{len .XTestGoFiles}}','./...']).trim().split(/\r?\n/).map(line=>{const[pkg,dir,internal,external]=line.split('\t');return{pkg,dir,tests:Number(internal)+Number(external)>0};});}
export async function compileGroup(platform,index){
 const jobs=goCompileJobs(process.env.CI_GO_COMPILE_JOBS);
 const target=nativeTargets.find(t=>t.platform===platform);if(!target)throw new Error('unknown bundle target');
 const host=go(['env','GOOS','GOARCH']).trim().split(/\r?\n/);if(host[0]!==target.goos||host[1]!==target.goarch)throw new Error('bundle target environment mismatch');
 const all=packages(),chosen=partition(all.map(p=>p.pkg),index,6),out=path.resolve('dist/go-tests',platform,String(index));await mkdir(out,{recursive:true});
 const names=chosen.map(pkg=>path.basename(pkg));
 if(new Set(names).size!==names.length)throw new Error('test package basename collision');
 if(chosen.length)go(['test','-mod=readonly','-p',jobs,'-trimpath','-ldflags','-s -w','-c','-o',out+path.sep,...chosen]);
 const rows=[];
 for(const pkg of chosen){const meta=all.find(p=>p.pkg===pkg),file=path.basename(pkg)+'.test'+target.suffix;rows.push({pkg,directory:path.relative(process.cwd(),meta.dir).split(path.sep).join('/'),file:meta.tests?file:null,sha256:meta.tests?hash(await readFile(path.join(out,file))):null});}
 await writeFile(path.join(out,'packages.json'),JSON.stringify(rows));
}
// The job owns this artifact exclusively; no other process may write it.
// Only transfer plain, unaliased files into the new directory, never sidecars
// or the entire artifact tree. Validate the bytes at their execution path.
async function requireUnaliasedFile(file){
 const info=await lstat(file);
 if(!info.isFile()||info.nlink!==1)throw new Error('unsafe bundle file');
}
async function takeFile(source,destination){
 await requireUnaliasedFile(source);
 await rename(source,destination);
}
const validGroup=group=>Number.isInteger(group)&&group>=0&&group<6;
// takeOwnership consumes the artifact's executables. On success the caller
// owns the returned temporary directory for the remaining native steps.
export async function consumeBundle(directory,{sha,platform,root=process.cwd(),temporary,takeOwnership=false}){
 directory=path.resolve(directory);
 if(takeOwnership){
  const info=await lstat(directory);
  if(!info.isDirectory()||path.relative(directory,await realpath(directory))!=='')throw new Error('unsafe bundle directory');
 }
 const manifest=path.join(directory,'bundle.json');
 if(takeOwnership)await requireUnaliasedFile(manifest);
 const receipt=JSON.parse(await readFile(manifest,'utf8'));
 if(receipt.revision!==sha||receipt.platform!==platform||!/^[a-f0-9]{40}$/.test(sha??'')||(!takeOwnership&&!temporary))throw new Error('bundle source/platform mismatch');
 if(!Array.isArray(receipt.groups)||receipt.groups.length!==6)throw new Error('incomplete bundle groups');
 const targets=receipt.groups.flat();
 if(new Set(targets.map(t=>t.pkg)).size!==targets.length)throw new Error('duplicate bundle package');
 const windows=platform.startsWith('win32-');
 const ownerBytes=windows?await readWindowsOwner(directory,receipt,platform):null;
 const cliFixtureBytes=windows?await readCLIFixture(directory,receipt,platform):null;
 const suffix=windows?'.test.exe':'.test';
 const tool='test2json'+(windows?'.exe':'');
 const files=await readdir(directory);if(!files.includes(tool))throw new Error('missing native test2json');
 const outputName=row=>row.pkg.replace(/[^A-Za-z0-9_-]/g,'_')+suffix;
 if(takeOwnership){
  const sources=new Set(),outputs=new Set(),groups=new Set();
  for(const row of targets){
   if(!row.file)continue;
   if(!validGroup(row.group)||!/^[-\w]+\.test(?:\.exe)?$/.test(row.file)||row.file!==path.posix.basename(row.pkg)+suffix)throw new Error('unsafe binary path');
   const source=`${row.group}/${row.file.toLowerCase()}`,output=outputName(row).toLowerCase();
   if(sources.has(source)||outputs.has(output))throw new Error('aliased bundle binary');
   sources.add(source);outputs.add(output);groups.add(row.group);
  }
  for(const group of groups)if(!(await lstat(path.join(directory,String(group)))).isDirectory())throw new Error('unsafe bundle group directory');
  // Sibling placement keeps rename on the artifact volume (including Windows).
  temporary=await mkdtemp(path.join(path.dirname(directory),'vecnet-ci-go-native-'));
 }
 const test2json=path.join(temporary,tool);
 try{
  if(takeOwnership)await takeFile(path.join(directory,tool),test2json);
  if(hash(await readFile(takeOwnership?test2json:path.join(directory,tool)))!==receipt.toolHash)throw new Error('test2json hash mismatch');
  if(!takeOwnership){await mkdir(temporary,{recursive:true});await copyFile(path.join(directory,tool),test2json);}
  await chmod(test2json,0o755);
  const inventory=await bounded(targets,async row=>{
   const cwd=path.resolve(root,row.directory),relative=path.relative(root,cwd);
   if(relative.startsWith('..')||path.isAbsolute(relative)||(takeOwnership&&path.isAbsolute(row.directory)))throw new Error('unsafe package directory');
   if(!row.file)return {pkg:row.pkg,names:[],directory:cwd,binary:null};
   if(!validGroup(row.group)||path.basename(row.file)!==row.file)throw new Error('unsafe binary path');
   const source=path.join(directory,String(row.group),row.file),binary=path.join(temporary,outputName(row));
   if(takeOwnership)await takeFile(source,binary);
   const bytes=await readFile(takeOwnership?binary:source);if(hash(bytes)!==row.sha256)throw new Error('test binary hash mismatch');
   if(!takeOwnership)await writeFile(binary,bytes,{mode:0o700});
   await chmod(binary,0o700);
   const {stdout:listing}=await exec(binary,['-test.list','.'],{cwd,encoding:'utf8'});
   const names=listing.trim().split(/\r?\n/).filter(Boolean);
   if(names.some(name=>!/^(?:Test\w+|Example\w*|Fuzz\w+)$/.test(name)))throw new Error('bad native test list');
   return {pkg:row.pkg,names,directory:cwd,binary};
  });
  const owner=ownerBytes?await stageWindowsOwner(ownerBytes,temporary):undefined;
  const cliFixture=cliFixtureBytes?await stageCLIFixture(cliFixtureBytes,temporary):undefined;
  return {...(takeOwnership?{inventory,test2json,temporary}:{inventory,test2json}),...(owner?{owner}:{}),...(cliFixture?{cliFixture}:{})};
 }catch(error){
  if(takeOwnership)await rm(temporary,{recursive:true,force:true});
  throw error;
 }
}
async function finalize(platform){
 const jobs=goCompileJobs(process.env.CI_GO_COMPILE_JOBS);
 const directory=path.resolve('dist/go-tests',platform);const groups=[];
 for(let index=0;index<6;index++)groups.push(JSON.parse(await readFile(path.join(directory,String(index),'packages.json'),'utf8')).map(row=>({...row,group:index})));
 const expected=packages().map(p=>p.pkg).sort(),actual=groups.flat().map(p=>p.pkg).sort();if(JSON.stringify(expected)!==JSON.stringify(actual))throw new Error('incomplete bundle packages');
 const tool='test2json'+(platform.startsWith('win32')?'.exe':'');go(['build','-p',jobs,'-trimpath','-ldflags','-s -w','-o',path.join(directory,tool),'cmd/test2json']);
 const revision=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim();
 const helper=await buildWindowsOwner(directory,platform,goCompileEnvironment(process.env));
 const cliHelper=await buildCLIFixture(directory,platform,goCompileEnvironment(process.env));
 await writeFile(path.join(directory,'bundle.json'),JSON.stringify({revision,platform,groups,toolHash:hash(await readFile(path.join(directory,tool))),...helper,...cliHelper},null,2));
 await stageSource(process.cwd(),path.resolve('dist/go-tests'));
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 const [command,platform,index]=process.argv.slice(2);
 if(command==='compile')await compileGroup(platform,Number(index));
 else if(command==='finalize')await finalize(platform);
 else throw new Error('bundle compile|finalize required');
}
