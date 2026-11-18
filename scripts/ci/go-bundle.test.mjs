import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { cp, link, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, stat, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { consumeBundle } from './go-bundle.mjs';
import { planGo } from './schedule.mjs';
const entry=fileURLToPath(new URL('./go-bundle.mjs',import.meta.url));
test('real compiled bundle conserves platform packages and rejects wrong source/corruption',async t=>{
 const root=await mkdtemp(path.join(os.tmpdir(),'ci-bundle-'));t.after(()=>rm(root,{recursive:true,force:true}));
 await writeFile(path.join(root,'go.mod'),'module fixture\n\ngo 1.26\n');
 await writeFile(path.join(root,'one_test.go'),'package fixture\nimport ("testing";"os")\nfunc TestOne(t *testing.T){if _,err:=os.Stat("one_test.go"); err!=nil {t.Fatal(err)}}\nfunc TestNew(t *testing.T){if os.Getenv("CI_BUNDLE_FAIL")=="1" {t.Fatal("new root assertion")}}\n');
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
  const win=await consumeBundle(winDirectory,{...options,platform:'win32-x64',takeOwnership:true});
  assert.ok(win.inventory.find(p=>p.binary).binary.endsWith('.test.exe'),'Windows relocation must preserve test-executable identity');
 }

 await assert.rejects(consumeBundle(directory,{...options,sha:'bad'}),/source/);
 const nativeTool=path.join(directory,'test2json'+(process.platform==='win32'?'.exe':''));
 const toolBytes=await readFile(nativeTool);await writeFile(nativeTool,'corrupt');
 await assert.rejects(consumeBundle(directory,options),/test2json hash mismatch/);
 await writeFile(nativeTool,toolBytes);

 const receipt=JSON.parse(await readFile(path.join(directory,'bundle.json'),'utf8'));
 const row=receipt.groups.flat().find(p=>p.file);
 const nativeBinary=path.join(directory,String(row.group),row.file);
 const binaryBytes=await readFile(nativeBinary);await writeFile(nativeBinary,'corrupt');
 await assert.rejects(consumeBundle(directory,options),/hash/);
 await writeFile(nativeBinary,binaryBytes);

 for(const [name,pattern,alter] of [
  ['wrong source',/source\/platform/,async ({receipt})=>{receipt.revision='bad';}],
  ['wrong platform',/source\/platform/,async ({receipt})=>{receipt.platform='wrong';}],
  ['binary traversal',/unsafe binary path/,async ({row})=>{row.file=`../${row.file}`;}],
  ['binary alternate stream',/unsafe binary path/,async ({row})=>{row.file=`alias:${row.file}`;}],
  ['wrong executable suffix',/unsafe binary path/,async ({row,file})=>{
   row.file+='-other';await rename(file,file+'-other');
  }],
  ['source alias',/aliased bundle binary/,async ({receipt,row})=>{
   receipt.groups[row.group].push({...row,pkg:'other/fixture'});
  }],
  ['destination alias',/aliased bundle binary/,async ({receipt,row,file,input})=>{
   const group=(row.group+1)%6,alias='FIXTURE'+row.file.slice('fixture'.length);
   await mkdir(path.join(input,String(group)),{recursive:true});
   await cp(file,path.join(input,String(group),alias));
   receipt.groups[group].push({...row,group,pkg:'FIXTURE',file:alias});
  }],
  ['cwd traversal',/unsafe package directory/,async ({row})=>{row.directory='..';}],
  ['binary hash',/test binary hash mismatch/,async ({file})=>writeFile(file,'corrupt')],
  ['tool hash',/test2json hash mismatch/,async ({tool})=>writeFile(tool,'corrupt')],
  ['binary hard link',/unsafe bundle file/,async ({file})=>link(file,`${file}-alias`)],
  ['tool hard link',/unsafe bundle file/,async ({tool})=>link(tool,`${tool}-alias`)],
  ['receipt hard link',/unsafe bundle file/,async ({receiptPath})=>link(receiptPath,`${receiptPath}-alias`)],
  ['binary symlink',/unsafe bundle file/,async ({file})=>{await rename(file,`${file}-alias`);await symlink(`${file}-alias`,file);}],
  ['group symlink',/unsafe bundle group directory/,async ({file})=>{
   const dir=path.dirname(file);await rename(dir,`${dir}-alias`);await symlink(`${dir}-alias`,dir,'junction');
  }],
  ['bundle symlink',/unsafe bundle directory/,async ({input})=>{
   await rename(input,`${input}-alias`);await symlink(`${input}-alias`,input,'junction');
  }],
 ]){
  await t.test(`owned staging rejects ${name} and leaves no private output`,{skip:process.platform==='win32'&&name==='binary symlink'},async()=>{
   const parent=await mkdtemp(path.join(root,'invalid-')),input=path.join(parent,'artifact');
   await cp(directory,input,{recursive:true});
   const receiptPath=path.join(input,'bundle.json');
   const receipt=JSON.parse(await readFile(receiptPath,'utf8')),row=receipt.groups.flat().find(p=>p.file);
   await alter({receipt,row,input,receiptPath,file:path.join(input,String(row.group),row.file),tool:path.join(input,path.basename(nativeTool))});
   await writeFile(receiptPath,JSON.stringify(receipt));
   await assert.rejects(consumeBundle(input,{...options,takeOwnership:true}),pattern);
   assert.ok((await readdir(parent)).every(entry=>!entry.startsWith('vecnet-ci-go-native-')));
  });
 }

 const original=await stat(nativeBinary,{bigint:true});
 const env={...process.env,GITHUB_SHA:sha};
 const copyTemp=path.join(root,'system-temp');
 if(process.platform==='linux'){
  // Linux staging must also work without runner-specific TMP vars.
  delete env.TMPDIR;delete env.TEMP;delete env.TMP;
 }else{
  await mkdir(copyTemp);
  Object.assign(env,{TMPDIR:copyTemp,TEMP:copyTemp,TMP:copyTemp});
 }
 execFileSync(process.execPath,[fileURLToPath(new URL('./suite.mjs',import.meta.url)),'consume-go'],{cwd:root,env});
 const consumed=(await Promise.all(Array.from({length:6},(_,i)=>readFile(path.join(root,'.ci',`go-${i}.json`),'utf8')))).flatMap(JSON.parse);
 assert.deepEqual(consumed.flatMap(p=>p.names).sort(),['TestNew','TestOne']);
 t.after(()=>Promise.all([...new Set(consumed.map(p=>p.temporary))].map(dir=>rm(dir,{recursive:true,force:true}))));
 const executed=consumed.find(p=>p.binary);
 assert.equal(executed.directory,root);
 assert.equal(executed.binary.endsWith(process.platform==='win32'?'.test.exe':'.test'),true);
 if(process.platform!=='win32')assert.equal((await stat(executed.temporary)).mode&0o777,0o700);
 const staged=await stat(executed.binary,{bigint:true});
 if(process.platform==='linux'){
  await assert.rejects(stat(nativeBinary),{code:'ENOENT'});
  assert.deepEqual([staged.dev,staged.ino],[original.dev,original.ino],
   'the Linux consumer must stage the existing executable, not write a copy');
 }else{
  assert.equal(path.dirname(executed.binary),executed.temporary);
  assert.equal(path.dirname(executed.temporary),await realpath(copyTemp),
   'the copied executable must live under system temp, not the artifact workspace');
  assert.notDeepEqual([staged.dev,staged.ino],[original.dev,original.ino]);
  assert.deepEqual(await readFile(nativeBinary),binaryBytes,'copying must preserve the artifact executable');
 }
 const stagedTool=await readFile(path.join(root,'.ci/test2json'),'utf8');
 const stagedEvents=execFileSync(stagedTool,['-t','-p',executed.pkg,executed.binary,'-test.v=test2json','-test.run','^TestOne$'],
  {cwd:executed.directory,encoding:'utf8'}).trim().split('\n').map(JSON.parse);
 assert.deepEqual(stagedEvents.filter(e=>e.Action==='pass'&&e.Test).map(e=>e.Test),['TestOne']);
});
