// Cross-compile complete test packages on existing Linux target producers;
// discover names and execute ONLY on the native consumer. No platform skips.
import { execFileSync, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { bounded } from './schedule.mjs';
const exec = promisify(execFile);
import { createHash } from 'node:crypto';
import { chmod, copyFile, mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { nativeTargets } from '../npm/platforms.mjs';
import { partition } from './partition.mjs';
const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
const go=args=>execFileSync('go',args,{encoding:'utf8',maxBuffer:16*1024*1024});
function packages(){return go(['list','-mod=readonly','-f','{{.ImportPath}}\t{{.Dir}}\t{{len .TestGoFiles}}\t{{len .XTestGoFiles}}','./...']).trim().split(/\r?\n/).map(line=>{const[pkg,dir,internal,external]=line.split('\t');return{pkg,dir,tests:Number(internal)+Number(external)>0};});}
export async function compileGroup(platform,index){
 const target=nativeTargets.find(t=>t.platform===platform);if(!target)throw new Error('unknown bundle target');
 const host=go(['env','GOOS','GOARCH']).trim().split(/\r?\n/);if(host[0]!==target.goos||host[1]!==target.goarch)throw new Error('bundle target environment mismatch');
 const all=packages(),chosen=partition(all.map(p=>p.pkg),index,6),out=path.resolve('dist/go-tests',platform,String(index));await mkdir(out,{recursive:true});
 const names=chosen.map(pkg=>path.basename(pkg));
 if(new Set(names).size!==names.length)throw new Error('test package basename collision');
 if(chosen.length)go(['test','-mod=readonly','-p','2','-trimpath','-ldflags','-s -w','-c','-o',out+path.sep,...chosen]);
 const rows=[];
 for(const pkg of chosen){const meta=all.find(p=>p.pkg===pkg),file=path.basename(pkg)+'.test'+target.suffix;rows.push({pkg,directory:path.relative(process.cwd(),meta.dir).split(path.sep).join('/'),file:meta.tests?file:null,sha256:meta.tests?hash(await readFile(path.join(out,file))):null});}
 await writeFile(path.join(out,'packages.json'),JSON.stringify(rows));
}
export async function consumeBundle(directory,{sha,platform,root=process.cwd(),temporary}){
 const receipt=JSON.parse(await readFile(path.join(directory,'bundle.json'),'utf8'));
 if(receipt.revision!==sha||receipt.platform!==platform||!temporary)throw new Error('bundle source/platform mismatch');
 if(!Array.isArray(receipt.groups)||receipt.groups.length!==6)throw new Error('incomplete bundle groups');
 const targets=receipt.groups.flat();
 if(new Set(targets.map(t=>t.pkg)).size!==targets.length)throw new Error('duplicate bundle package');
 const files=await readdir(directory);if(!files.includes('test2json'+(platform.startsWith('win32')?'.exe':'')))throw new Error('missing native test2json');
 const tool='test2json'+(platform.startsWith('win32')?'.exe':'');
 if(hash(await readFile(path.join(directory,tool)))!==receipt.toolHash)throw new Error('test2json hash mismatch');
 await mkdir(temporary,{recursive:true});await copyFile(path.join(directory,tool),path.join(temporary,tool));await chmod(path.join(temporary,tool),0o755);
 const inventory=await bounded(targets,async row=>{
  const cwd=path.resolve(root,row.directory);if(path.relative(root,cwd).startsWith('..'))throw new Error('unsafe package directory');
  if(!row.file)return {pkg:row.pkg,names:[],directory:cwd,binary:null};
  if(!Number.isInteger(row.group)||row.group<0||row.group>=6||path.basename(row.file)!==row.file)throw new Error('unsafe binary path');
  const source=path.join(directory,String(row.group),row.file);
  const bytes=await readFile(source);if(hash(bytes)!==row.sha256)throw new Error('test binary hash mismatch');
  const suffix=platform.startsWith('win32')?'.test.exe':'.test';
  const binary=path.join(temporary,row.pkg.replace(/[^A-Za-z0-9_-]/g,'_')+suffix);await writeFile(binary,bytes,{mode:0o700});await chmod(binary,0o700);
  const {stdout:listing}=await exec(binary,['-test.list','.'],{cwd,encoding:'utf8'});
  const names=listing.trim().split(/\r?\n/).filter(Boolean);
  if(names.some(name=>!/^(?:Test\w+|Example\w*|Fuzz\w+)$/.test(name)))throw new Error('bad native test list');
  return {pkg:row.pkg,names,directory:cwd,binary};
 });
 return {inventory,test2json:path.join(temporary,tool)};
}
async function finalize(platform){
 const directory=path.resolve('dist/go-tests',platform);const groups=[];
 for(let index=0;index<6;index++)groups.push(JSON.parse(await readFile(path.join(directory,String(index),'packages.json'),'utf8')).map(row=>({...row,group:index})));
 const expected=packages().map(p=>p.pkg).sort(),actual=groups.flat().map(p=>p.pkg).sort();if(JSON.stringify(expected)!==JSON.stringify(actual))throw new Error('incomplete bundle packages');
 const tool='test2json'+(platform.startsWith('win32')?'.exe':'');go(['build','-p','2','-trimpath','-o',path.join(directory,tool),'cmd/test2json']);
 const revision=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim();
 await writeFile(path.join(directory,'bundle.json'),JSON.stringify({revision,platform,groups,toolHash:hash(await readFile(path.join(directory,tool)))},null,2));
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 const [command,platform,index]=process.argv.slice(2);
 if(command==='compile')await compileGroup(platform,Number(index));
 else if(command==='finalize')await finalize(platform);
 else throw new Error('bundle compile|finalize required');
}
