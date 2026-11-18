import assert from 'node:assert/strict';
import test from 'node:test';
import {execFileSync,spawnSync} from 'node:child_process';
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
 const identities=plan.flat().map(({file,shard,shards})=>[file,shard,shards]);
 assert.deepEqual(identities.sort(),[
  ['a',0,4],['a',1,4],['a',2,4],['a',3,4],
  ['b',0,1],['c',0,1],['new',0,1],
 ].sort(),'complete file/shard/shards multiset');
 for(const tasks of plan)assert.equal(new Set(tasks.map(x=>x.file)).size,tasks.length);
 assert.deepEqual(planNode([...files].reverse(),profile),plan);
 assert.throws(()=>planNode(['a','a'],profile),/duplicate/);
 assert.throws(()=>planNode(['a'],{a:{seconds:1,shards:5}}),/invalid/);
});

test('actual Node registration shards execute dynamic roots, nested assertions and hooks exactly once, and propagate failure',async t=>{
 const cwd=await mkdtemp(path.join(os.tmpdir(),'node conserved '));t.after(()=>rm(cwd,{recursive:true,force:true}));
 const file=path.join(cwd,'cases.test.mjs');
 await writeFile(file,`import test, {it as named, before, after, beforeEach} from 'node:test';
 import assert from 'node:assert/strict';
 const record=(kind,id)=>console.log('EVENT '+JSON.stringify([kind,id]));
 before(()=>record('global-before','worker'));after(()=>record('global-after','worker'));
 beforeEach(async context=>{
  await Promise.resolve();
  record('before-each',context.name);
  context.after(async()=>{await Promise.resolve();record('hook-cleanup',context.name);});
 });
 for(let i=0;i<13;i++)(i%2?test:named)('dynamic '+i,async context=>{
  record('root',i);
  context.after(()=>record('root-cleanup',i));
  await context.test('nested '+i,async nested=>{
   nested.after(()=>record('nested-cleanup',i));
   assert.notEqual(process.env.CI_FAIL_ROOT,String(i));
   record('nested-assertion',i);
  });
 });
 test.skip('original skip',()=>{throw Error('original skip ran')});
 `);
 const events=[];
 const env={...process.env};delete env.NODE_TEST_CONTEXT;
 const preload=fileURLToPath(new URL('./node-shard-register.mjs',import.meta.url));
 for(let shard=0;shard<4;shard++){
  const output=execFileSync(process.execPath,['--import',preload,'--test','--test-reporter=tap',file],{env:{...env,CI_NODE_SHARD:String(shard),CI_NODE_SHARDS:'4'},encoding:'utf8'});
  const workerEvents=[...output.matchAll(/# EVENT (.+)/g)].map(m=>JSON.parse(m[1]));
  for(const kind of ['global-before','global-after']){
   assert.deepEqual(workerEvents.filter(([name])=>name===kind),[[kind,'worker']],`${kind} once per worker`);
  }
  events.push(...workerEvents.filter(([kind])=>!kind.startsWith('global-')));
 }
 const expected=[];
 for(let i=0;i<13;i++){
  for(const kind of ['root','nested-assertion','root-cleanup','nested-cleanup'])expected.push([kind,i]);
  for(const name of ['dynamic '+i,'nested '+i]){
   expected.push(['before-each',name],['hook-cleanup',name]);
  }
 }
 const identities=rows=>rows.map(row=>JSON.stringify(row)).sort();
 assert.deepEqual(identities(events),identities(expected),'every root, nested assertion and context cleanup exactly once');
 assert.throws(()=>execFileSync(process.execPath,['--import',preload,'--test','--test-reporter=tap',file],{env:{...env,CI_NODE_SHARD:'1',CI_NODE_SHARDS:'4',CI_FAIL_ROOT:'5'},encoding:'utf8',stdio:'pipe'}),e=>{assert.equal(e.status,1);assert.match(e.stdout,/not ok/);assert.match(e.stdout,/nested 5/);assert.match(e.stdout,/ERR_ASSERTION/);return true;});
 await writeFile(file,`import {describe,it} from 'node:test';describe('new suite',()=>it('must not disappear',()=>{}));`);
 assert.throws(()=>execFileSync(process.execPath,['--import',preload,'--test','--test-reporter=tap',file],{env:{...env,CI_NODE_SHARD:'0',CI_NODE_SHARDS:'4'},encoding:'utf8',stdio:'pipe'}),e=>{assert.match(e.stdout,/added a suite/);return true;});
});

test('imported nested test/it registration fails closed across shards, including after await',async t=>{
 const cwd=await mkdtemp(path.join(os.tmpdir(),'node imported nested '));t.after(()=>rm(cwd,{recursive:true,force:true}));
 const file=path.join(cwd,'cases.test.mjs');
 const env={...process.env};delete env.NODE_TEST_CONTEXT;
 const preload=fileURLToPath(new URL('./node-shard-register.mjs',import.meta.url));
 for(const registration of ['test','namedTest','namedIt'])for(const afterAwait of [false,true]){
  await t.test(registration+(afterAwait?' after await':' before await'),async()=>{
   await writeFile(file,`import test, {test as namedTest,it as namedIt} from 'node:test';
   import assert from 'node:assert/strict';
   ${registration}('parent',async()=>{
    ${afterAwait?'await Promise.resolve();':''}
    console.log('IMPORTED_PARENT');
    await ${registration}('child',()=>assert.fail('imported nested assertion must fail'));
   });`);
   const results=Array.from({length:4},(_,shard)=>spawnSync(process.execPath,
    ['--import',preload,'--test','--test-reporter=tap',file],
    {env:{...env,CI_NODE_SHARD:String(shard),CI_NODE_SHARDS:'4'},encoding:'utf8'}));
   for(const result of results){assert.ifError(result.error);assert.equal(result.signal,null);}
   assert.equal(results.reduce((n,r)=>n+[...r.stdout.matchAll(/# IMPORTED_PARENT/g)].length,0),1,'actual parent executes exactly once across all four shards');
   assert.deepEqual(results.map(r=>r.status),[1,0,0,0],'selected parent must fail, never silently lose the nested assertion');
   assert.match(results[0].stdout,/unsupported imported nested registration/);
   assert.match(results[0].stdout,/not ok/);
   for(let shard=0;shard<4;shard++)assert.match(results[shard].stdout,new RegExp('ci-node-shard registered=1 selected='+(shard===0?1:0)+' shard='+shard+'/4'));
  });
 }
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
