import assert from 'node:assert/strict';
import test from 'node:test';
import { audit, acceptanceReserve } from './audit.mjs';
const at = seconds => new Date(Date.UTC(2026,0,1)+seconds*1000).toISOString();
const run={head_sha:'abc',created_at:at(0),status:'completed',conclusion:'success'};
const job={id:1,name:'tests',status:'completed',conclusion:'success',completed_at:at(100),steps:[{name:'setup',conclusion:'success',started_at:at(40),completed_at:at(50)}]};
test('accepts completed exact-source compliant run including queue and post steps',()=>{
 assert.deepEqual(audit(run,[job],{sha:'abc',now:at(101)}).violations,[]);
});
test('rejects queue+execution reaching300 and any action/post step reaching60',()=>{
 assert.ok(audit(run,[{...job,completed_at:at(300)}],{sha:'abc',now:at(300)}).violations.some(x=>x.includes('300')));
 assert.ok(audit(run,[{...job,steps:[{name:'Post cache',conclusion:'success',started_at:at(1),completed_at:at(61)}]}],{sha:'abc',now:at(101)}).violations.some(x=>x.includes('Post cache')));
});
test('fails closed on source mismatch, absent/missing steps, failed tests and insufficient publication reserve',()=>{
 for(const [r,j,o] of [[run,[],{}],[run,[{...job,steps:[]}],{}],[{...run,head_sha:'other'},[job],{}],[run,[{...job,conclusion:'failure'}],{}],[run,[{...job,completed_at:null}],{}]])
  assert.ok(audit(r,j,{sha:'abc',now:at(101),...o}).violations.length);
 assert.ok(audit({...run,status:'in_progress',conclusion:null},[job],{sha:'abc',now:at(260),preflight:true,reserve:45}).violations.some(x=>x.includes('reserve')));
});
test('preflight inspects its own already-completed setup and refuses missing matrix jobs',()=>{
 const own={id:2,name:'accept',status:'in_progress',steps:[{name:'setup',status:'completed',conclusion:'success',started_at:at(90),completed_at:at(150)}]};
 const result=audit({...run,status:'in_progress'},[job,own],{sha:'abc',preflight:true,reserve:45,currentJob:2,expectedJobs:1,now:at(151)});
 assert.ok(result.violations.some(x=>x.includes('accept/setup')));
 assert.ok(audit(run,[job],{sha:'abc',expectedJobs:2}).violations.some(x=>x.includes('missing/unexpected jobs')));
});

test('preflight reserves publication only for a pushed version tag',()=>{
 assert.equal(acceptanceReserve('pull_request','refs/pull/1/merge'),8);
 assert.equal(acceptanceReserve('push','refs/heads/master'),8);
 assert.equal(acceptanceReserve('workflow_dispatch','refs/tags/v1.2.3'),8);
 assert.equal(acceptanceReserve('push','refs/tags/v1.2.3'),35);
});
