import assert from 'node:assert/strict';
import test from 'node:test';
import {execFileSync} from 'node:child_process';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {planNode} from './node-schedule.mjs';

test('weighted dynamic files and case shards conserve new files without duplicating a file per group',()=>{
 const files=['a','b','c','new'];
 const profile={a:{seconds:80,shards:4},b:{seconds:30},c:{seconds:20}};
 const plan=planNode(files,profile);
 assert.equal(plan.length,4);
 assert.equal(plan.flat().filter(x=>x.file==='new').length,1);
 assert.deepEqual(plan.flat().filter(x=>x.file==='a').map(x=>x.shard).sort(),[0,1,2,3]);
 for(const tasks of plan)assert.equal(new Set(tasks.map(x=>x.file)).size,tasks.length);
 assert.deepEqual(planNode([...files].reverse(),profile),plan);
 assert.throws(()=>planNode(['a','a'],profile),/duplicate/);
 assert.throws(()=>planNode(['a'],{a:{seconds:1,shards:5}}),/invalid/);
});

test('actual Node registration shards execute dynamic roots, nested assertions and hooks exactly once, and propagate failure',async t=>{
 const cwd=await mkdtemp(path.join(os.tmpdir(),'node conserved '));t.after(()=>rm(cwd,{recursive:true,force:true}));
 const file=path.join(cwd,'cases.test.mjs');
 await writeFile(file,`import test, {it as named, before, after} from 'node:test';
 import assert from 'node:assert/strict';
 before(()=>console.log('HOOK'));after(()=>console.log('AFTER'));
 for(let i=0;i<13;i++)(i%2?test:named)('dynamic '+i,async t=>{console.log('ROOT '+i);await t.test('nested',()=>{assert.notEqual(process.env.CI_FAIL_ROOT,String(i));});});
 test.skip('original skip',()=>{throw Error('original skip ran')});
 `);
 const roots=[];
 const env={...process.env};delete env.NODE_TEST_CONTEXT;
 const preload=fileURLToPath(new URL('./node-shard-register.mjs',import.meta.url));
 for(let shard=0;shard<4;shard++){
  const output=execFileSync(process.execPath,['--import',preload,'--test','--test-reporter=tap',file],{env:{...env,CI_NODE_SHARD:String(shard),CI_NODE_SHARDS:'4'},encoding:'utf8'});
  roots.push(...[...output.matchAll(/# ROOT (\d+)/g)].map(x=>Number(x[1])));
  assert.match(output,/# HOOK/);assert.match(output,/# AFTER/);
 }
 assert.deepEqual(roots.sort((a,b)=>a-b),Array.from({length:13},(_,i)=>i));
 assert.throws(()=>execFileSync(process.execPath,['--import',preload,'--test',file],{env:{...env,CI_NODE_SHARD:'1',CI_NODE_SHARDS:'4',CI_FAIL_ROOT:'5'},encoding:'utf8',stdio:'pipe'}),e=>{assert.match(e.stdout,/not ok/);return true;});
 await writeFile(file,`import {describe,it} from 'node:test';describe('new suite',()=>it('must not disappear',()=>{}));`);
 assert.throws(()=>execFileSync(process.execPath,['--import',preload,'--test',file],{env:{...env,CI_NODE_SHARD:'0',CI_NODE_SHARDS:'4'},encoding:'utf8',stdio:'pipe'}),e=>{assert.match(e.stdout,/added a suite/);return true;});
});

test('suite dynamically discovers every file and executes all shard roots exactly once',async t=>{
 const cwd=await mkdtemp(path.join(os.tmpdir(),'node suite conservation '));t.after(()=>rm(cwd,{recursive:true,force:true}));
 const {mkdir}=await import('node:fs/promises');
 const directory=path.join(cwd,'connectors/launcher/test/backends');await mkdir(directory,{recursive:true});
 await writeFile(path.join(directory,'paseo.test.ts'),`import test from 'node:test';for(let i=0;i<17;i++)test('case '+i,()=>console.log('CONSERVE '+i));`);
 await writeFile(path.join(directory,'new.test.ts'),`import test from 'node:test';test('new discovered file',()=>console.log('NEW_FILE'));`);
 const suite=fileURLToPath(new URL('./suite.mjs',import.meta.url));
 const env={...process.env};delete env.NODE_TEST_CONTEXT;
 const outputs=[];
 for(let group=0;group<4;group++)outputs.push(execFileSync(process.execPath,[suite,'node','0',String(group)],{cwd,env,encoding:'utf8'}));
 const all=outputs.join('\n');
 assert.deepEqual([...all.matchAll(/# CONSERVE (\d+)/g)].map(m=>Number(m[1])).sort((a,b)=>a-b),Array.from({length:17},(_,i)=>i));
 assert.equal([...all.matchAll(/# NEW_FILE/g)].length,1);
 assert.equal([...all.matchAll(/ci-node-shard registered=17 selected=/g)].length,4);
});
