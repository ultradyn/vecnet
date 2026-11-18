import assert from 'node:assert/strict';
import test from 'node:test';
import { observeGo, buildGoProfile } from './go-profile.mjs';
const event = e => `2026-11-18T00:00:00Z ${JSON.stringify({Time:'2026-11-18T00:00:00Z', ...e})}`;
test('only completed PASS roots and packages are measurements; failed and unfinished work is censored', () => {
 const log = [
  {Action:'start',Package:'p'}, {Action:'run',Package:'p',Test:'TestA'},
  {Action:'pass',Package:'p',Test:'TestA/sub',Elapsed:90},
  {Action:'pass',Package:'p',Test:'TestA',Elapsed:2},
  {Action:'run',Package:'p',Test:'TestB'}, {Action:'fail',Package:'p',Test:'TestB',Elapsed:50},
  {Action:'fail',Package:'p',Elapsed:53},
  {Action:'start',Package:'q'}, {Action:'run',Package:'q',Test:'TestPending'},
  {Action:'start',Package:'r'}, {Action:'run',Package:'r',Test:'TestZero'},
  {Action:'pass',Package:'r',Test:'TestZero',Elapsed:0}, {Action:'pass',Package:'r',Elapsed:0.02},
 ].map(event).join('\n');
 const obs=observeGo(log);
 assert.deepEqual(obs.roots,{p:{TestA:2},r:{TestZero:0.01}});
 assert.deepEqual(obs.packageElapsed,{r:0.02});
 assert.deepEqual(obs.censored.map(x=>[x.pkg,x.test??null,x.action]),[
  ['p','TestB','fail'],['p',null,'fail'],['q','TestPending','unfinished'],['q',null,'unfinished']
 ]);
});
test('Windows combines native complete maxima, never stale or subtest durations',()=>{
 const sample=(n)=>[{Action:'start',Package:'p'},{Action:'run',Package:'p',Test:'TestA'},
  {Action:'pass',Package:'p',Test:'TestA',Elapsed:n},{Action:'pass',Package:'p',Elapsed:n+0.1}].map(event).join('\n');
 const profile=buildGoProfile([{name:'test-go-windows-x64 (0)',log:sample(2)},
  {name:'test-go-windows-arm (0)',log:sample(4)}, {name:'test-go-linux (0)',log:sample(1)}]);
 assert.equal(profile.windows.roots.p.TestA,4);
 assert.equal(profile.windows.packageElapsed.p,4.1);
 assert.equal(profile.linux.roots.p.TestA,1);
 assert.equal(profile.windows.packages.p,4);
 assert.throws(()=>buildGoProfile([{name:'test-node (x)',log:sample(1)}]),/Go job/);
});
test('unfinished roots and package executions never become zero-time observations',()=>{
 const log=[{Action:'start',Package:'p'},{Action:'run',Package:'p',Test:'TestPending'}].map(event).join('\n');
 const obs=observeGo(log);
 assert.deepEqual(obs.roots,{});assert.deepEqual(obs.packageElapsed,{});
 assert.deepEqual(obs.censored,[{pkg:'p',test:'TestPending',action:'unfinished'},{pkg:'p',action:'unfinished'}]);
});

test('archive binding uses explicit source/run/attempt and exact native job/log set',async t=>{
 const {mkdtemp,writeFile,rm,readFile}=await import('node:fs/promises');
 const {execFileSync}=await import('node:child_process');
 const path=await import('node:path'),os=await import('node:os');
 const {fileURLToPath}=await import('node:url');
 const dir=await mkdtemp(path.join(os.tmpdir(),'go-profile-'));t.after(()=>rm(dir,{recursive:true,force:true}));
 const index=[],jobs=[];
 for(const platform of ['linux','windows-x64','windows-arm'])for(let lane=0;lane<6;lane++){
  const id=jobs.length+1,name=`test-go-${platform} (${lane})`;
  index.push({id,name,conclusion:'success'});jobs.push({id,name,head_sha:'a'.repeat(40),run_id:123,run_attempt:2,status:'completed',conclusion:'success'});
  await writeFile(path.join(dir,`${id}.log`),[{Action:'start',Package:'p'},{Action:'run',Package:'p',Test:'TestObserved'},{Action:'pass',Package:'p',Test:'TestObserved',Elapsed:id},{Action:'pass',Package:'p',Elapsed:id+0.1}].map(event).join('\n'));
 }
 const save=async()=>{await writeFile(path.join(dir,'index.json'),JSON.stringify(index));await writeFile(path.join(dir,'jobs.json'),JSON.stringify({jobs}));};await save();
 const out=path.join(dir,'out.json'),entry=fileURLToPath(new URL('./go-profile.mjs',import.meta.url));
 const run=()=>execFileSync(process.execPath,[entry,dir,out,'a'.repeat(40),'123','2'],{encoding:'utf8',stdio:'pipe'});
 run();const result=JSON.parse(await readFile(out,'utf8'));
 assert.equal(result.windows.roots.p.TestObserved,18);assert.equal(result.linux.roots.p.TestObserved,6);
 assert.equal(result.source.run,123);assert.equal(result.source.attempt,2);assert.equal(result.source.logs.length,18);
 assert.ok(result.source.logs.every(row=>/^[a-f0-9]{64}$/.test(row.sha256)));
 jobs[0].run_attempt=1;await save();assert.throws(run,/binding mismatch/);jobs[0].run_attempt=2;
 jobs[0].head_sha='b'.repeat(40);await save();assert.throws(run,/binding mismatch/);jobs[0].head_sha='a'.repeat(40);
 jobs[0].run_id=124;await save();assert.throws(run,/binding mismatch/);jobs[0].run_id=123;
 index[1].id=index[0].id;await save();assert.throws(run,/native Go logs|binding mismatch/);
});
