// Disposable producer experiment. Never merge this branch.
import {execFileSync} from 'node:child_process';
import {mkdir,readFile,rename,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {goCompileJobs,goCompileEnvironment} from './schedule.mjs';
const platform='win32-arm64',jobs=goCompileJobs(process.env.CI_GO_COMPILE_JOBS);
const go=args=>execFileSync('go',args,{encoding:'utf8',env:goCompileEnvironment(process.env),maxBuffer:16*1024*1024});
const all=go(['list','-mod=readonly','-f','{{.ImportPath}}\t{{.Dir}}\t{{len .TestGoFiles}}\t{{len .XTestGoFiles}}','./...']).trim().split(/\r?\n/).map(line=>{const[pkg,dir,a,b]=line.split('\t');return {pkg,dir,tests:Number(a)+Number(b)>0};});
if(new Set(all.map(p=>path.basename(p.pkg))).size!==all.length)throw new Error('basename collision');
const root=path.resolve('dist/go-tests',platform),flat=path.join(root,'all');await mkdir(flat,{recursive:true});
go(['test','-mod=readonly','-p',jobs,'-trimpath','-ldflags','-s -w','-c','-o',flat+path.sep,...all.map(p=>p.pkg)]);
for(let group=0;group<6;group++){
 const dir=path.join(root,String(group)),rows=[];await mkdir(dir,{recursive:true});
 for(const row of all.filter((_,i)=>i%6===group)){
  const file=path.basename(row.pkg)+'.test.exe';let digest=null;
  if(row.tests){const bytes=await readFile(path.join(flat,file));digest=createHash('sha256').update(bytes).digest('hex');await rename(path.join(flat,file),path.join(dir,file));}
  rows.push({pkg:row.pkg,directory:path.relative(process.cwd(),row.dir).split(path.sep).join('/'),file:row.tests?file:null,sha256:digest});
 }
 await writeFile(path.join(dir,'packages.json'),JSON.stringify(rows));
}
console.log(`compiled ${all.length} packages into the unchanged six receipt groups`);
