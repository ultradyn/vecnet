import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { cp, link, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, stat, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { ownerFixture } from './windows-owner.test-fixture.mjs';
import { consumeBundle } from './go-bundle.mjs';
import { planGo, nativeGoLanes } from './schedule.mjs';
const entry=fileURLToPath(new URL('./go-bundle.mjs',import.meta.url));
test('real compiled bundle conserves platform packages and rejects wrong source/corruption',async t=>{
 const root=await mkdtemp(path.join(os.tmpdir(),'ci-bundle-'));t.after(()=>rm(root,{recursive:true,force:true}));
 await writeFile(path.join(root,'go.mod'),'module fixture\n\ngo 1.26\n');
 await writeFile(path.join(root,'one_test.go'),'package fixture\nimport ("testing";"os")\nfunc TestOne(t *testing.T){if _,err:=os.Stat("one_test.go"); err!=nil {t.Fatal(err)}; if want:=os.Getenv("CI_EXPECT_CLI_FIXTURE"); want!="" && os.Getenv("CI_CLI_FIXTURE_HELPER")!=want {t.Fatal("CLI fixture environment missing")}}\nfunc TestNew(t *testing.T){if os.Getenv("CI_BUNDLE_FAIL")=="1" {t.Fatal("new root assertion")}}\n');
 await mkdir(path.join(root,'internal/empty'),{recursive:true});await writeFile(path.join(root,'internal/empty/empty.go'),'package empty\n');
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
 if(process.platform!=='win32')assert.equal(result.cliFixture,undefined);
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
  Object.assign(winReceipt,await ownerFixture(winDirectory,winReceipt.platform));
  winReceipt.ciCLIFixture=(await ownerFixture(winDirectory,winReceipt.platform,'ci-cli-fixture.exe')).ciWindowsOwner;
  await writeFile(path.join(winDirectory,'bundle.json'),JSON.stringify(winReceipt));
  const winOptions={...options,platform:'win32-x64',takeOwnership:true};
  for(const [name,pattern,alter] of [
   ['missing CLI fixture metadata',/CLI fixture metadata/,async (dir,receipt)=>{delete receipt.ciCLIFixture;}],
   ['CLI fixture metadata path',/CLI fixture metadata/,async (dir,receipt)=>{receipt.ciCLIFixture.file='../other.exe';}],
   ['missing CLI fixture image',/ENOENT/,async dir=>rm(path.join(dir,'ci-cli-fixture.exe'))],
   ['CLI fixture hash',/CLI fixture hash/,async dir=>writeFile(path.join(dir,'ci-cli-fixture.exe'),'corrupt')],
   ['CLI fixture architecture',/CLI fixture PE architecture/,async (dir,receipt)=>{receipt.ciCLIFixture=(await ownerFixture(dir,'win32-arm64','ci-cli-fixture.exe')).ciWindowsOwner;}],
   ['CLI fixture alias',/unsafe CLI fixture/,async dir=>link(path.join(dir,'ci-cli-fixture.exe'),path.join(dir,'cli-alias.exe'))],
   ['CLI fixture symlink',/unsafe CLI fixture/,async dir=>{const f=path.join(dir,'ci-cli-fixture.exe');await rename(f,f+'-alias');await symlink(f+'-alias',f);}],
   ['missing metadata',/owner metadata/,async (dir,receipt)=>{delete receipt.ciWindowsOwner;}],
   ['metadata path',/owner metadata/,async (dir,receipt)=>{receipt.ciWindowsOwner.file='../other.exe';}],
   ['owner hash',/owner hash/,async dir=>writeFile(path.join(dir,'ci-windows-owner.exe'),'corrupt')],
   ['helper from wrong architecture',/PE architecture/,async (dir,receipt)=>Object.assign(receipt,await ownerFixture(dir,'win32-arm64'))],
   ['wrong source',/source\/platform/,async (dir,receipt)=>{receipt.revision='0'.repeat(40);}],
   ['wrong platform',/source\/platform/,async (dir,receipt)=>{receipt.platform='win32-arm64';}],
  ])await t.test(`Windows bundle rejects ${name} before executing a test binary`,async()=>{
   const dir=await mkdtemp(path.join(root,'win-invalid-'));
   await cp(winDirectory,dir,{recursive:true});
   const receipt=structuredClone(winReceipt);
   await alter(dir,receipt);
   await writeFile(path.join(dir,'bundle.json'),JSON.stringify(receipt));
   await assert.rejects(consumeBundle(dir,winOptions),pattern);
  });
  const win=await consumeBundle(winDirectory,winOptions);
  assert.equal(path.dirname(win.cliFixture),win.temporary);
  assert.deepEqual(await readFile(win.cliFixture),await readFile(path.join(winDirectory,'ci-cli-fixture.exe')));
  assert.equal(path.dirname(win.owner),win.temporary);
  assert.deepEqual(await readFile(win.owner),await readFile(path.join(winDirectory,'ci-windows-owner.exe')));
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
 const env={...process.env,GITHUB_SHA:sha,GITHUB_ENV:path.join(root,'actions-env')};
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
  const owner=path.join(executed.temporary,'ci-windows-owner.exe');
  assert.equal((await readFile(env.GITHUB_ENV,'utf8')).trim(),`CI_WINDOWS_OWNER=${owner}`);
  assert.deepEqual(await readFile(owner),await readFile(path.join(directory,'ci-windows-owner.exe')));
  assert.notDeepEqual([staged.dev,staged.ino],[original.dev,original.ino]);
  assert.deepEqual(await readFile(nativeBinary),binaryBytes,'copying must preserve the artifact executable');
 }
 const stagedTool=await readFile(path.join(root,'.ci/test2json'),'utf8');
 const stagedEvents=execFileSync(stagedTool,['-t','-p',executed.pkg,executed.binary,'-test.v=test2json','-test.run','^TestOne$'],
  {cwd:executed.directory,encoding:'utf8'}).trim().split('\n').map(JSON.parse);
 assert.deepEqual(stagedEvents.filter(e=>e.Action==='pass'&&e.Test).map(e=>e.Test),['TestOne']);

 // On Linux no helper artifact is required. An explicit portable fixture
 // still exercises the same suite-to-child environment handoff as Windows.
 const helper=path.join(executed.temporary,'helper with spaces');
 await writeFile(helper,'fixture');await writeFile(path.join(root,'.ci/cli-fixture'),helper);
 const placement=planGo(consumed,{}, {lanes:nativeGoLanes(process.platform)}).findIndex(rows=>rows.some(row=>row.pkg===executed.pkg));
 const child=spawnSync(process.execPath,[fileURLToPath(new URL('./suite.mjs',import.meta.url)),'go',String(placement%nativeGoLanes(process.platform)),String(Math.floor(placement/nativeGoLanes(process.platform)))],{cwd:root,env:{...env,CI_EXPECT_CLI_FIXTURE:helper},encoding:'utf8',timeout:45000});
 assert.equal(child.status,0,child.stdout+'\n'+child.stderr);
 assert.match(child.stdout,/"Action":"pass"/);
});

test('standalone CLI fixture preserves real process contracts',async t=>{
 const dir=await mkdtemp(path.join(os.tmpdir(),'cli fixture '));t.after(()=>rm(dir,{recursive:true,force:true}));
 const binary=path.join(dir,'helper'+(process.platform==='win32'?'.exe':''));
 execFileSync('go',['build','-p=2','-trimpath','-ldflags=-s -w','-o',binary,'./scripts/ci/_cli-fixture'],{cwd:fileURLToPath(new URL('../../',import.meta.url)),stdio:'pipe',timeout:45000});
 const run=(spec,args=[],input='')=>{
  return (async()=>{await writeFile(binary+'.cli-fixture.json',JSON.stringify(spec),{mode:0o600});return spawnSync(binary,args,{input,encoding:'utf8',timeout:2000});})();
 };
 for(const exit of [0,2,128]){
  const discovery=await run({Kind:'git-tracking',Exit:exit},['-C',dir,'rev-parse','--show-toplevel']);
  assert.equal(discovery.status,0);assert.equal(discovery.stdout.trim(),dir.split(path.sep).join('/'));
  assert.equal((await run({Kind:'git-tracking',Exit:exit},['-C',dir,'ls-files','--error-unmatch','--','.codex/hooks.json'])).status,exit);
 }
 const log=path.join(dir,'argv.json');
 assert.equal((await run({Kind:'argv',Log:log},['literal two words','--flag'])).status,0);
 assert.deepEqual(JSON.parse(await readFile(log,'utf8')),['literal two words','--flag']);
 const alias=path.join(dir,'second'+(process.platform==='win32'?'.exe':''));await link(binary,alias);
 const otherLog=path.join(dir,'other.json');await writeFile(alias+'.cli-fixture.json',JSON.stringify({Kind:'argv',Log:otherLog}));
 assert.equal(spawnSync(alias,['second'],{timeout:2000}).status,0);
 assert.deepEqual(JSON.parse(await readFile(otherLog,'utf8')),['second']);
 const clipboard=path.join(dir,'clipboard'),calls=path.join(dir,'calls');
 assert.equal((await run({Kind:'clipboard',Log:clipboard,Calls:calls},[],Buffer.from('\ufeffhello 🌏','utf16le'))).status,0);
 assert.equal(await readFile(clipboard,'utf8'),'hello 🌏');assert.equal(await readFile(calls,'utf8'),'called\n');
 const claudeLog=path.join(dir,'claude');assert.equal((await run({Kind:'claude',Log:claudeLog},['one','two words'])).status,0);
 assert.match(await readFile(claudeLog,'utf8'),/argv=one two words/);
 const marker=path.join(dir,'marker');assert.equal((await run({Kind:'never-run',Marker:marker,Exit:99})).status,99);assert.equal((await readFile(marker)).length,0);
 const notRepo=await run({Kind:'git-non-repository',Exit:128});assert.equal(notRepo.status,128);assert.match(notRepo.stderr,/not a git repository/);
 assert.equal((await run({Kind:'git-failure',Exit:2})).status,2);
 const sleeping=await run({Kind:'git-timeout'});assert.equal(sleeping.error?.code,'ETIMEDOUT');
 assert.notEqual((await run({Kind:'unknown'})).status,0);
 await writeFile(binary+'.cli-fixture.json','broken');assert.notEqual(spawnSync(binary,[],{timeout:2000}).status,0);
 await rm(binary+'.cli-fixture.json');assert.notEqual(spawnSync(binary,[],{timeout:2000}).status,0);
});
