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
