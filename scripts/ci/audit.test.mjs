import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { audit, acceptanceReserve, attemptStart, parseAttempt, runDeadline } from './audit.mjs';
const at = seconds => new Date(Date.UTC(2026,0,1)+seconds*1000).toISOString();
// A first attempt: GitHub reports run_started_at equal to created_at.
const run={head_sha:'abc',run_attempt:1,created_at:at(0),run_started_at:at(0),status:'completed',conclusion:'success'};
const job={id:1,name:'tests',run_attempt:1,status:'completed',conclusion:'success',completed_at:at(100),steps:[{name:'setup',conclusion:'success',started_at:at(40),completed_at:at(50)}]};
// "Re-run failed jobs" keeps created_at; attempt 2 starts 600s later. The
// passing job is carried with its original times; the rerun job is new.
const rerun={...run,run_attempt:2,run_started_at:at(600)};
const carried={...job,id:11,name:'carried',run_attempt:2};
const retried={...job,id:12,name:'retried',run_attempt:2,completed_at:at(700),steps:[{name:'setup',conclusion:'success',started_at:at(640),completed_at:at(650)}]};
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
 assert.equal(acceptanceReserve('push','refs/tags/v1.2.3'),61);
});

test('tag preflight admits238s but refuses239s with the full publication reserve',()=>{
 const options={sha:'abc',preflight:true,reserve:acceptanceReserve('push','refs/tags/v1.2.3')};
 assert.deepEqual(audit(run,[job],{...options,now:at(238)}).violations,[]);
 assert.deepEqual(audit(run,[job],{...options,now:at(239)}).violations,['insufficient remaining reserve 61s at 239s']);
});

test('actual publication command caps fit the tag reserve and remain below60s',()=>{
 const workflow=readFileSync(new URL('../../.github/workflows/release.yml',import.meta.url),'utf8');
 const checkout=workflow.match(/steps: &node-steps[\s\S]*?sparse-checkout: \|\n((?:            .+\n)+)/);
 assert.ok(checkout?.[1].trim().split(/\s+/).some(folder=>'.github/workflows/release.yml'.startsWith(folder+'/')),'Node test checkout must include the workflow audited here');
 const accept=workflow.slice(workflow.indexOf('\n  accept:'));
 const npm=accept.match(/run: node scripts\/ci\/budget\.mjs (\d+) node scripts\/npm\/release\.mjs publish\b/);
 const github=accept.match(/run: node scripts\/ci\/budget\.mjs (\d+) bash scripts\/ci\/github-release\.sh\b/);
 assert.ok(npm,'accept must own the real npm publication command');
 assert.ok(github,'accept must own GitHub Release creation');
 assert.equal(Number(npm[1]),40000,'npm upload needs more than the failed14s cap');
 assert.equal(Number(github[1]),14000);
 assert.equal(acceptanceReserve('push','refs/tags/v1.2.3'),(Number(npm[1])+Number(github[1]))/1000+7);
 for(const [,ms] of workflow.matchAll(/scripts\/ci\/budget\.mjs (\d+)/g)) assert.ok(Number(ms)>0&&Number(ms)<60000,`command cap ${ms} must be <60s`);
});

test('a re-run-failed-jobs attempt is measured from its own start, not run creation',()=>{
 // Measured from created_at this would be 700s and could never pass.
 const result=audit(rerun,[carried,retried],{sha:'abc',attempt:2});
 assert.deepEqual(result.violations,[]);
 assert.equal(result.elapsed,100);
 assert.equal(attemptStart(rerun),Date.parse(at(600)));
 assert.equal(runDeadline(rerun),Date.parse(at(600))+299000);
});

test('rerun preflight keeps the same <300s attempt budget and publication reserve',()=>{
 const options={sha:'abc',attempt:2,preflight:true,reserve:acceptanceReserve('push','refs/tags/v1.2.3')};
 const live={...rerun,status:'in_progress',conclusion:null};
 assert.deepEqual(audit(live,[carried,retried],{...options,now:at(838)}).violations,[]);
 assert.deepEqual(audit(live,[carried,retried],{...options,now:at(839)}).violations,['insufficient remaining reserve 61s at 239s']);
 assert.ok(audit(live,[carried,retried],{...options,now:at(900)}).violations.some(x=>x.includes('must be <300s')));
});

test('an attempt exceeding 300s after its own start still fails',()=>{
 const slow={...retried,completed_at:at(900)};
 assert.ok(audit(rerun,[carried,slow],{sha:'abc',attempt:2}).violations.some(x=>x.includes('300')));
});

test('first-attempt semantics are unchanged: run_started_at equals created_at',()=>{
 assert.equal(attemptStart(run),Date.parse(run.created_at));
 assert.equal(audit(run,[job],{sha:'abc',attempt:1}).elapsed,100);
});

test('fails closed on missing or mixed attempt evidence',()=>{
 assert.ok(audit({...rerun,run_started_at:undefined},[carried,retried],{sha:'abc'}).violations.includes('missing attempt start timestamp'));
 assert.ok(audit({...rerun,run_attempt:undefined},[carried,retried],{sha:'abc'}).violations.includes('missing run attempt'));
 // The audited attempt must be the one the API returned.
 assert.ok(audit(rerun,[carried,retried],{sha:'abc',attempt:3}).violations.some(x=>x.includes('not audited attempt 3')));
 // A job record from another attempt (a failed original) must not count.
 const stale={...retried,id:13,name:'stale',run_attempt:1,conclusion:'failure'};
 const violations=audit(rerun,[carried,stale],{sha:'abc',attempt:2}).violations;
 assert.ok(violations.includes('job stale belongs to attempt 1'));
 assert.ok(violations.includes('job stale incomplete/failed'));
 // Every other check still applies to carried jobs.
 assert.ok(audit(rerun,[{...carried,steps:[]},retried],{sha:'abc',attempt:2}).violations.includes('job carried missing steps'));
 assert.ok(audit({...rerun,head_sha:'other'},[carried,retried],{sha:'abc',attempt:2}).violations.includes('source SHA mismatch'));
});

test('attempt selection accepts only positive integers',()=>{
 assert.equal(parseAttempt('2'),2);
 assert.equal(parseAttempt(3),3);
 for(const bad of [undefined,'','0','1.5','x',0,-1])assert.throws(()=>parseAttempt(bad),/invalid run attempt/);
});

test('audit reads only the audited attempt and binds the overall deadline to it',()=>{
 const source=readFileSync(new URL('./audit.mjs',import.meta.url),'utf8');
 assert.match(source,/actions\/runs\/\$\{id\}\/attempts\/\$\{attempt\}`\)/);
 assert.match(source,/actions\/runs\/\$\{id\}\/attempts\/\$\{attempt\}\/jobs\?/);
 assert.doesNotMatch(source,/filter=latest/);
 assert.match(source,/CI_RUN_DEADLINE=\$\{runDeadline\(run\)\}/);
 const status=readFileSync(new URL('./status.mjs',import.meta.url),'utf8');
 assert.match(status,/'completed',String\(run\.run_attempt\)\]/,'completed-run audit must name its triggering attempt');
});
