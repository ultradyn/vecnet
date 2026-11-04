import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { SendRequest } from '../../hub-client/src/wire.ts';
import { createFakeBackend } from './fake-backend.ts';
import { openLedger } from '../src/ledger.ts';
import { createJobReporter } from '../src/jobs.ts';
import { createOutbox } from '../src/outbox.ts';
import { parseConfig } from '../src/config.ts';
import { buildLaunchSpec } from '../src/prompt.ts';
import { startLaunch } from '../src/launch.ts';
import type { LaunchDeps } from '../src/launch.ts';
import type { SessionHooks } from '../src/backend.ts';

const hooks: SessionHooks = { onEvent() {}, async onPermissionRequest(r) { return {optionId:r.defaultOnExpiry,decidedBy:'expired'}; }, async onQuestion() { return {answers:{},answeredBy:'timeout'}; } };
function setup(t: { after(fn: () => void): void }) {
  const clock = fakeClock(100), ledger = openLedger(':memory:',clock), log = memoryLog();
  t.after(() => ledger.close());
  const backend = createFakeBackend({clock});
  const outbox = createOutbox({ledger,clock,log,hub:{async send() { throw new Error('unused'); },async receipt() {}}});
  const jobs = createJobReporter({ledger,outbox});
  const cfg = parseConfig({host:'test',hub:{url:'http://localhost',credential_file:'/fake'},state_dir:'/fake',backends:[],projects:{repo:{repo_root:'/repo',base_ref:'master'}}});
  const deps: LaunchDeps = {ledger,clock,log,jobs,cfg,host:'test',backends:new Map([['fake',backend]]),hooks:() => hooks,projects:() => ({alias:'repo',repoRoot:'/repo',baseRef:'master'})};
  const row = {jobId:'job_one',threadId:'thr_one',launchMessageId:'msg_one',requester:'peer_requester',policyKey:'prn_owner',backend:'fake',preset:'test',permissionMode:'ask' as const,project:'repo',title:'A job',instructions:'Fix the launch race.',input:{backend:'fake',project:'repo',preset:'test',permission_mode:'ask' as const},tag:'vl-job_one',state:'accepted' as const,sessionRef:null,sessionKind:null,turnCursor:null,branch:null,workspace:null,lastActivityAt:100,parkedAt:null,deadlineAt:1000000,createdAt:100};
  ledger.createJob(row); jobs.report(row.jobId,'accepted');
  const job = () => ledger.job(row.jobId)!;
  const reports = () => ledger.pendingOutbox(100,clock.now()).map(r => r.payload as SendRequest).filter(r => r.kind === 'job_status').map(r => r.job_status!);
  return {deps,ledger,clock,backend,job,reports};
}
import { sweep } from '../src/sweep.ts';

test('sweep settles an orphan only after 24 h grace; skips untagged refs and refs of ended ledger jobs', async t => {
  const {deps,clock,job,backend,ledger} = setup(t);
  await startLaunch(job(),deps); deps.jobs.report(job().jobId,'succeeded');
  const orphan = await backend.launch({...buildLaunchSpec(job(),deps.projects(job()),await backend.capabilities(),{}),jobId:'job_orphan',tag:'vl-job_orphan'},hooks);
  const untagged = {backend:'fake',host:'fake-host',id:'personal',extra:{tag:'pag-personal'}};
  const listOwned = backend.listOwned.bind(backend); backend.listOwned = async () => [...await listOwned(),untagged];
  const sweepDeps = {...deps,graceMs:86400000,retentionMs:604800000,firstSeen:ledger.kv('sweep')};
  const first = await sweep(sweepDeps); assert.deepEqual(first.settled,[]);
  assert.ok(first.skipped.some(s => s.ref?.id === 'personal' && s.reason === 'untagged'));
  clock.advance(86399999); assert.deepEqual((await sweep(sweepDeps)).settled,[]);
  clock.advance(1); const final = await sweep({...sweepDeps});
  assert.deepEqual(final.settled,[orphan.ref]); assert.equal(backend.sessions().find(s => s.spec.jobId === 'job_orphan')!.settles,1);
  assert.equal(backend.sessions().find(s => s.spec.jobId === 'job_one')!.settles,0);
});

test('sweep calls cleanupWorkspace only for launcher-dir backends and ended jobs older than retention', async t => {
  const {deps,clock,job,backend,ledger} = setup(t);
  await startLaunch(job(),deps); const ref = job().sessionRef!;
  const removed: string[] = []; backend.cleanupWorkspace = async r => { removed.push(r.id); return {removed:true}; };
  const sweepDeps = {...deps,graceMs:86400000,retentionMs:604800000,firstSeen:ledger.kv('sweep')};
  deps.jobs.report(job().jobId,'succeeded'); clock.advance(604800001);
  assert.deepEqual((await sweep(sweepDeps)).workspacesRemoved,[]); assert.deepEqual(removed,[]);
  const caps = await backend.capabilities(); backend.capabilities = async () => ({...caps,workspace:'launcher-dir'});
  ledger.createJob({...job(),jobId:'job_recent',threadId:'thr_recent',state:'ended',sessionRef:{...ref,id:'recent'}});
  ledger.updateJob('job_recent',{endedAt:clock.now()-1000});
  ledger.createJob({...job(),jobId:'job_open',threadId:'thr_open',state:'running',sessionRef:{...ref,id:'open'}});
  assert.deepEqual((await sweep(sweepDeps)).workspacesRemoved,['/repo']); assert.deepEqual(removed,['job_one']);
  backend.cleanupWorkspace = async () => ({removed:false,reason:'dirty'});
  assert.ok((await sweep(sweepDeps)).skipped.some(s => s.path === '/repo' && s.reason === 'dirty'));
});
