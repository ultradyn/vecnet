import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
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

 await assert.rejects(consumeBundle(directory,{...options,sha:'bad'}),/source/);
 const receipt=JSON.parse(await readFile(path.join(directory,'bundle.json'),'utf8'));
 const row=receipt.groups.flat().find(p=>p.file);
 await writeFile(path.join(directory,String(row.group),row.file),'corrupt');
 await assert.rejects(consumeBundle(directory,options),/hash/);
});
