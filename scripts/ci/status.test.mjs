import assert from 'node:assert/strict';
import test from 'node:test';
import { publishAcceptance } from './status.mjs';
const sha='a'.repeat(40),defaultSha='b'.repeat(40);
const event={id:123,run_attempt:2,head_sha:sha,html_url:'https://github.com/acme/repo/actions/runs/123'};
function harness({run=event,failAudit=false,failAPI=false}={}){
 const requests=[];
 const api=async(method,route,body)=>{
  requests.push({method,route,body});
  if(failAPI)throw new Error('API denied');
  if(method==='GET')return run;
  return {};
 };
 return {requests,execute:()=>publishAcceptance({repo:'acme/repo',event,api,evaluate:async()=>{if(failAudit)throw new Error('audit unavailable');return{pass:true,violations:[]};},defaultSha})};
}
test('reports success only on triggering tested SHA, links exact source run and attempt',async()=>{
 const h=harness();await h.execute();
 const posts=h.requests.filter(x=>x.method==='POST');
 assert.equal(posts.length,2);
 for(const p of posts){assert.equal(p.route,`repos/acme/repo/statuses/${sha}`);assert.equal(p.body.context,'Release budget acceptance');assert.equal(p.body.target_url,event.html_url+'/attempts/2');assert.doesNotMatch(p.route,new RegExp(defaultSha));}
 assert.deepEqual(posts.map(p=>p.body.state),['pending','success']);
});
test('wrong SHA/run attempt never receives success and triggering SHA gets failure',async()=>{
 for(const run of [{...event,head_sha:defaultSha},{...event,run_attempt:3},{...event,id:124}]){
  const h=harness({run});await assert.rejects(h.execute());
  assert.equal(h.requests.at(-1).body.state,'failure');assert.equal(h.requests.at(-1).route,`repos/acme/repo/statuses/${sha}`);
 }
});
test('audit and API errors fail closed; no successful result leaks',async()=>{
 for(const options of [{failAudit:true},{failAPI:true}]){
  const h=harness(options);await assert.rejects(h.execute());assert.ok(!h.requests.some(x=>x.body?.state==='success'));
 }
 const requests=[];await publishAcceptance({repo:'acme/repo',event,api:async(method,route,body)=>{requests.push({method,route,body});return method==='GET'?event:{};},evaluate:async()=>({pass:false,violations:['step reached60s']})});
 assert.equal(requests.at(-1).body.state,'failure');
});
test('a new attempt appearing during audit cannot inherit previous success',async()=>{
 const calls=[];let reads=0;
 await assert.rejects(publishAcceptance({repo:'acme/repo',event,api:async(method,route,body)=>{calls.push({method,route,body});return method==='GET'?{...event,run_attempt:++reads===1?2:3}:{};},evaluate:async()=>({pass:true,violations:[]})}));
 assert.equal(calls.at(-1).body.state,'failure');assert.ok(!calls.some(c=>c.body?.state==='success'));
});
