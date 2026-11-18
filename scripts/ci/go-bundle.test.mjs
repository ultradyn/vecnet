import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { cp, mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { consumeBundle } from './go-bundle.mjs';
const entry=fileURLToPath(new URL('./go-bundle.mjs',import.meta.url));
test('real compiled bundle conserves platform packages and rejects wrong source/corruption',async t=>{
 const root=await mkdtemp(path.join(os.tmpdir(),'ci-bundle-'));t.after(()=>rm(root,{recursive:true,force:true}));
 await writeFile(path.join(root,'go.mod'),'module fixture\n\ngo 1.26\n');
 await writeFile(path.join(root,'one_test.go'),'package fixture\nimport "testing"\nfunc TestOne(t *testing.T){}\n');
 await mkdir(path.join(root,'empty'));await writeFile(path.join(root,'empty/empty.go'),'package empty\n');
 execFileSync('git',['init','-q'],{cwd:root});execFileSync('git',['add','.'],{cwd:root});execFileSync('git',['-c','core.hooksPath=/dev/null','-c','commit.gpgsign=false','-c','user.name=CI','-c','user.email=ci@example.invalid','commit','-qm','fixture'],{cwd:root});
 const sha=execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim();
 const platform=`${process.platform}-${process.arch}`;
 for(let group=0;group<6;group++)execFileSync(process.execPath,[entry,'compile',platform,String(group)],{cwd:root});
 execFileSync(process.execPath,[entry,'finalize',platform],{cwd:root});
 const directory=path.join(root,'dist/go-tests',platform),options={sha,platform,root,temporary:path.join(root,'private')};
 const result=await consumeBundle(directory,options);
 assert.equal(result.inventory.length,2);assert.deepEqual(result.inventory.flatMap(p=>p.names),['TestOne']);
 // Execute real copied binary via native test2json without any Go toolchain.
 const rowNative=result.inventory.find(p=>p.binary);
 const events=execFileSync(result.test2json,['-t','-p',rowNative.pkg,rowNative.binary,'-test.v=test2json','-test.count=1'],{cwd:rowNative.directory,encoding:'utf8'}).trim().split('\n').map(JSON.parse);
 assert.equal(events.filter(e=>e.Action==='pass'&&e.Test==='TestOne').length,1);

 // The real CI entry point must also work without runner-specific TMP vars.
 const env={...process.env,GITHUB_SHA:sha};delete env.TMPDIR;delete env.TEMP;delete env.TMP;
 execFileSync(process.execPath,[fileURLToPath(new URL('./suite.mjs',import.meta.url)),'consume-go'],{cwd:root,env});
 const consumed=(await Promise.all(Array.from({length:6},(_,i)=>readFile(path.join(root,'.ci',`go-${i}.json`),'utf8')))).flatMap(JSON.parse);
 assert.deepEqual(consumed.flatMap(p=>p.names),['TestOne']);
 t.after(()=>Promise.all([...new Set(consumed.map(p=>p.temporary))].map(dir=>rm(dir,{recursive:true,force:true}))));

 if(process.platform!=='win32'){
  // Linux can execute these real ELF fixtures under Windows-style filenames;
  // this checks relocation identity, not native Windows execution.
  const winDirectory=path.join(root,'windows-names');await cp(directory,winDirectory,{recursive:true});
  const winReceipt=JSON.parse(await readFile(path.join(winDirectory,'bundle.json'),'utf8'));
  winReceipt.platform='win32-x64';
  await rename(path.join(winDirectory,'test2json'),path.join(winDirectory,'test2json.exe'));
  for(const row of winReceipt.groups.flat())if(row.file){
   await rename(path.join(winDirectory,String(row.group),row.file),path.join(winDirectory,String(row.group),row.file+'.exe'));
   row.file+='.exe';
  }
  await writeFile(path.join(winDirectory,'bundle.json'),JSON.stringify(winReceipt));
  const win=await consumeBundle(winDirectory,{...options,platform:'win32-x64',temporary:path.join(root,'private-windows')});
  assert.ok(win.inventory.find(p=>p.binary).binary.endsWith('.test.exe'),'Windows relocation must preserve test-executable identity');
 }

 await assert.rejects(consumeBundle(directory,{...options,sha:'bad'}),/source/);
 const receipt=JSON.parse(await readFile(path.join(directory,'bundle.json'),'utf8'));
 const row=receipt.groups.flat().find(p=>p.file);
 await writeFile(path.join(directory,String(row.group),row.file),'corrupt');
 await assert.rejects(consumeBundle(directory,options),/hash/);
});
