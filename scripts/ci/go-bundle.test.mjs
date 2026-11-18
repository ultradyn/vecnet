import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { cp, mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import * as bundle from './go-bundle.mjs';
const {consumeBundle}=bundle;
import { planGo } from './schedule.mjs';
const entry=fileURLToPath(new URL('./go-bundle.mjs',import.meta.url));
test('bundle discovery uses four capacity-capped hosted workers and two locally',()=>{
 assert.equal(typeof bundle.bundleDiscoveryJobs,'function');
 for(const value of [undefined,'false','1'])assert.equal(bundle.bundleDiscoveryJobs({GITHUB_ACTIONS:value},8),2);
 for(const available of [1,2,3,4,8])assert.equal(bundle.bundleDiscoveryJobs({GITHUB_ACTIONS:'true'},available),Math.min(4,available));
 assert.throws(()=>bundle.bundleDiscoveryJobs({GITHUB_ACTIONS:'true'},0),/parallelism/);
});
test('real compiled bundle conserves platform packages and rejects wrong source/corruption',async t=>{
 const root=await mkdtemp(path.join(os.tmpdir(),'ci-bundle-'));t.after(()=>rm(root,{recursive:true,force:true}));
 await writeFile(path.join(root,'go.mod'),'module fixture\n\ngo 1.26\n');
 await writeFile(path.join(root,'one_test.go'),'package fixture\nimport ("testing";"os")\nfunc TestOne(t *testing.T){}\nfunc TestNew(t *testing.T){if os.Getenv("CI_BUNDLE_FAIL")=="1" {t.Fatal("new root assertion")}}\n');
 await mkdir(path.join(root,'empty'));await writeFile(path.join(root,'empty/empty.go'),'package empty\n');
 execFileSync('git',['init','-q'],{cwd:root});execFileSync('git',['add','.'],{cwd:root});execFileSync('git',['-c','core.hooksPath=/dev/null','-c','commit.gpgsign=false','-c','user.name=CI','-c','user.email=ci@example.invalid','commit','-qm','fixture'],{cwd:root});
 const sha=execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim();
 const platform=`${process.platform}-${process.arch}`;
 for(let group=0;group<6;group++)execFileSync(process.execPath,[entry,'compile',platform,String(group)],{cwd:root});
 execFileSync(process.execPath,[entry,'finalize',platform],{cwd:root});
 const directory=path.join(root,'dist/go-tests',platform),options={sha,platform,root,temporary:path.join(root,'private')};
 const toolSymbols=spawnSync('go',['tool','nm',path.join(directory,'test2json'+(process.platform==='win32'?'.exe':''))],{encoding:'utf8'});
 assert.notEqual(toolSymbols.status,0,'native test2json must omit the linker symbol table');
 assert.match(toolSymbols.stderr,/no symbol/);

 const result=await consumeBundle(directory,options);
 assert.equal(result.inventory.length,2);assert.deepEqual(result.inventory.flatMap(p=>p.names).sort(),['TestNew','TestOne']);
 // Execute real copied binary via native test2json without any Go toolchain.
 const rowNative=result.inventory.find(p=>p.binary);
 const events=execFileSync(result.test2json,['-t','-p',rowNative.pkg,rowNative.binary,'-test.v=test2json','-test.count=1'],{cwd:rowNative.directory,encoding:'utf8'}).trim().split('\n').map(JSON.parse);
 assert.equal(events.filter(e=>e.Action==='pass'&&e.Test==='TestOne').length,1);
 // This small package is normally whole; measured expensive placement must
 // execute both real discovered roots exactly once, not just change the plan.
 const tasks=planGo(result.inventory,{packageElapsed:{fixture:42}}).flat().filter(row=>row.binary);
 assert.equal(tasks.length,2);assert.ok(tasks.every(row=>row.split));
 const passed=[];
 for(const task of tasks){
  const output=execFileSync(result.test2json,['-t','-p',task.pkg,task.binary,'-test.v=test2json','-test.count=1','-test.run',`^(${task.selected.join('|')})$`],{cwd:task.directory,encoding:'utf8'});
  passed.push(...output.trim().split('\n').map(JSON.parse).filter(e=>e.Action==='pass'&&e.Test).map(e=>e.Test));
 }
 assert.deepEqual(passed.sort(),['TestNew','TestOne']);
 const failing=tasks.find(row=>row.selected.includes('TestNew'));
 assert.throws(()=>execFileSync(result.test2json,['-t','-p',failing.pkg,failing.binary,'-test.v=test2json','-test.run','^TestNew$'],{
  cwd:failing.directory,env:{...process.env,CI_BUNDLE_FAIL:'1'},encoding:'utf8',stdio:'pipe',
 }),error=>{assert.match(error.stdout,/new root assertion/);return error.status!==0;});
 const binarySymbols=spawnSync('go',['tool','nm',rowNative.binary],{encoding:'utf8'});
 assert.notEqual(binarySymbols.status,0);assert.match(binarySymbols.stderr,/no symbol/);


 // The real CI entry point must also work without runner-specific TMP vars.
 const env={...process.env,GITHUB_SHA:sha};delete env.TMPDIR;delete env.TEMP;delete env.TMP;
 execFileSync(process.execPath,[fileURLToPath(new URL('./suite.mjs',import.meta.url)),'consume-go'],{cwd:root,env});
 const consumed=(await Promise.all(Array.from({length:6},(_,i)=>readFile(path.join(root,'.ci',`go-${i}.json`),'utf8')))).flatMap(JSON.parse);
 assert.deepEqual(consumed.flatMap(p=>p.names).sort(),['TestNew','TestOne']);
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
 const nativeTool=path.join(directory,'test2json'+(process.platform==='win32'?'.exe':''));
 const toolBytes=await readFile(nativeTool);await writeFile(nativeTool,'corrupt');
 await assert.rejects(consumeBundle(directory,options),/test2json hash mismatch/);
 await writeFile(nativeTool,toolBytes);

 const receipt=JSON.parse(await readFile(path.join(directory,'bundle.json'),'utf8'));
 const row=receipt.groups.flat().find(p=>p.file);
 await writeFile(path.join(directory,String(row.group),row.file),'corrupt');
 await assert.rejects(consumeBundle(directory,options),/hash/);
});
