import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { SendRequest } from '../../hub-client/src/wire.ts';
import { createFakeBackend } from './fake-backend.ts';
import { openLedger } from '../src/ledger.ts';
import { createJobReporter } from '../src/jobs.ts';
import { createOutbox } from '../src/outbox.ts';
import { parseConfig } from '../src/config.ts';
import { startLaunch } from '../src/launch.ts';
import type { LaunchDeps } from '../src/launch.ts';
import type { SessionHooks } from '../src/backend.ts';

const hooks: SessionHooks = { onEvent() {}, async onPermissionRequest(r) { return {optionId:r.defaultOnExpiry,decidedBy:'expired'}; }, async onQuestion() { return {answers:{},answeredBy:'timeout'}; } };
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return {promise,resolve};
}
function setup(t: { after(fn: () => void): void }, persistent = false) {
  const dir = persistent ? mkdtempSync(join(tmpdir(),'launcher-reattach-')) : undefined;
  const path = dir ? join(dir,'ledger.sqlite') : ':memory:';
  const clock = fakeClock(100), log = memoryLog();
  let ledger = openLedger(path,clock);
  t.after(() => { ledger.close(); if (dir) rmSync(dir,{recursive:true,force:true}); });
  const backend = createFakeBackend({clock});
  const outbox = createOutbox({ledger,clock,log,hub:{async send() { throw new Error('unused'); },async receipt() {}}});
  const jobs = createJobReporter({ledger,outbox});
  const cfg = parseConfig({host:'test',hub:{url:'http://localhost',credential_file:'/fake'},state_dir:'/fake',backends:[],projects:{repo:{repo_root:'/repo',base_ref:'master'}}});
  const deps: LaunchDeps = {ledger,clock,log,jobs,cfg,host:'test',backends:new Map([['fake',backend]]),hooks:() => hooks,projects:() => ({alias:'repo',repoRoot:'/repo',baseRef:'master'})};
  const row = {jobId:'job_one',threadId:'thr_one',launchMessageId:'msg_one',requester:'peer_requester',policyKey:'prn_owner',backend:'fake',preset:'test',permissionMode:'ask' as const,project:'repo',title:'A job',instructions:'Fix the launch race.',input:{backend:'fake',project:'repo',preset:'test',permission_mode:'ask' as const},tag:'vl-job_one',state:'accepted' as const,sessionRef:null,sessionKind:null,turnCursor:null,branch:null,workspace:null,lastActivityAt:100,parkedAt:null,deadlineAt:1000000,createdAt:100};
  ledger.createJob(row); jobs.report(row.jobId,'accepted');
  const job = () => ledger.job(row.jobId)!;
  const reports = () => ledger.pendingOutbox(100,clock.now()).map(r => r.payload as SendRequest).filter(r => r.kind === 'job_status').map(r => r.job_status!);
  const restart = () => {
    assert.ok(persistent);
    ledger.close(); ledger = openLedger(path,clock); deps.ledger = ledger;
    deps.jobs = createJobReporter({ledger,outbox:createOutbox({ledger,clock,log,hub:{async send() { throw new Error('unused'); },async receipt() {}}})});
    return {...deps};
  };
  return {deps,ledger,clock,backend,job,reports,restart};
}
async function drain<T>(promise: Promise<T>, clock: ReturnType<typeof fakeClock>): Promise<T> {
  let done = false; void promise.then(() => { done = true; },() => { done = true; });
  for (let i = 0; i < 100 && !done; i++) { await Promise.resolve(); clock.advance(30000); }
  return promise;
}

import { reattachAll, retryPending } from '../src/reattach.ts';

for (const retry of [false,true]) {
  for (const terminal of ['cancelled','failed'] as const) {
    test(`a deferred ${retry ? 'pending retry' : 'reattach'} after ${terminal} keeps capacity released and returns no handle`, async t => {
      const {deps,ledger,clock,job,backend,reports} = setup(t);
      await startLaunch(job(),deps);
      if (retry) {
        backend.failReattach(1); await reattachAll(deps); clock.advance(30000);
      }
      const entered = deferred(), release = deferred();
      const reattach = backend.reattach.bind(backend);
      backend.reattach = async (...args) => {
        const result = await reattach(...args);
        entered.resolve(); await release.promise;
        return result;
      };
      const pending = retry ? retryPending(deps) : reattachAll(deps);
      await entered.promise;
      deps.jobs.report(job().jobId,terminal);
      const endedAt = job().endedAt;
      assert.equal(ledger.counts().open,0);
      release.resolve();
      const [result] = await pending;
      assert.equal(job().state,'ended'); assert.equal(job().terminal,terminal);
      assert.equal(job().endedAt,endedAt); assert.equal(ledger.counts().open,0);
      assert.equal(result.outcome,'lost'); assert.ok(!('handle' in result));
      assert.equal(backend.sessions()[0].settles,1); assert.equal(backend.sessions()[0].state.kind,'ended');
      assert.equal(job().reattachPending,false);
      assert.deepEqual(reports().map(r => [r.state,r.sequence]),[['accepted',1],['running',2],[terminal,3]]);
    });
  }
}

test('a late handle whose settle fails is retried durably without reopening the ended job or posting unreachable notes', async t => {
  const {deps,ledger,clock,job,backend,reports,restart} = setup(t,true);
  const launch = backend.launch.bind(backend);
  backend.launch = async (...args) => {
    const handle = await launch(...args);
    handle.settle = async () => { throw new Error('offline'); };
    return handle;
  };
  deps.beforeRecordRef = () => { deps.jobs.report(job().jobId,'cancelled'); };
  assert.equal((await startLaunch(job(),deps)).ok,false);
  assert.equal(job().state,'ended'); assert.equal(job().terminal,'cancelled');
  assert.equal(job().reattachPending,true); assert.equal(ledger.counts().open,0);
  assert.deepEqual(await retryPending({...deps}),[]);
  clock.advance(300000);
  const [result] = await retryPending(restart());
  assert.equal(result.outcome,'lost'); assert.ok(!('handle' in result));
  assert.equal(job().state,'ended'); assert.equal(job().terminal,'cancelled');
  assert.equal(job().reattachPending,false); assert.equal(deps.ledger.counts().open,0);
  assert.equal(backend.sessions()[0].settles,1); assert.equal(backend.sessions()[0].state.kind,'ended');
  assert.deepEqual(reports().map(r => [r.state,r.sequence]),[['accepted',1],['cancelled',2]]);
  assert.ok(!deps.ledger.pendingOutbox(100,clock.now()).some(r => (r.payload as SendRequest).kind === 'message'));
});

for (const sent of [false,true]) {
  test(`restart reattaches a settling job without duplicating its ${sent ? 'sent' : 'queued'} running status`, async t => {
    const {deps,ledger,clock,job,reports,restart} = setup(t,true);
    await startLaunch(job(),deps);
    const sequence = job().statusSeq;
    if (sent) for (const row of ledger.pendingOutbox(100,clock.now())) ledger.markOutbox(row.id,'sent');
    ledger.updateJob(job().jobId,{state:'settling'});
    const before = reports();
    const restarted = restart();
    const [result] = await reattachAll(restarted);
    assert.equal(result.outcome,'live'); assert.ok('handle' in result);
    assert.equal(job().state,'settling'); assert.equal(job().terminal,null);
    assert.equal(job().statusSeq,sequence); assert.deepEqual(reports(),before);
    assert.equal(restarted.ledger.counts().open,1);
  });
}

test('kill between launch and record (beforeRecordRef throws) → reattachAll adopts via findByTag; exactly one fake session exists', async t => {
  const {deps,job,backend,reports} = setup(t);
  deps.beforeRecordRef = () => { throw new Error('simulated kill'); };
  await assert.rejects(startLaunch(job(),deps),/simulated kill/);
  assert.equal(job().state,'launching'); assert.equal(job().sessionRef,null);
  deps.beforeRecordRef = undefined;
  const [result] = await reattachAll(deps);
  assert.equal(result.outcome,'adopted'); assert.equal(job().state,'running');
  assert.equal(backend.sessions().length,1); assert.equal(job().sessionRef?.id,'job_one');
  assert.deepEqual(reports().map(r => [r.state,r.sequence]),[['accepted',1],['running',2]]);
});

test('a crash while accepted → reattachAll starts the launch; exactly one session', async t => {
  const {deps,job,backend} = setup(t);
  const [result] = await reattachAll(deps);
  assert.equal(result.outcome,'launch_started');
  assert.ok(result.outcome === 'launch_started' && result.result.ok);
  assert.equal(job().state,'running'); assert.equal(backend.sessions().length,1);
  await reattachAll(deps); assert.equal(backend.sessions().length,1);
});

test('tag not found after the crash (hideTagOnce) → failed outcome_unknown; launch() never called again', async t => {
  const {deps,job,backend,reports} = setup(t);
  deps.beforeRecordRef = () => { throw new Error('kill'); };
  await assert.rejects(startLaunch(job(),deps));
  backend.hideTagOnce(job().tag);
  backend.launch = async () => { assert.fail('must never relaunch'); };
  const [result] = await reattachAll(deps);
  assert.equal(result.outcome,'lost'); assert.equal(job().terminal,'failed');
  assert.deepEqual(reports().at(-1)!.output,{error:'outcome_unknown'});
  assert.equal(backend.sessions().length,1);
});

test('reattach throws twice then returns live → job stays running (reattachPending, no outcome_unknown), then resumes turns from the stored cursor', async t => {
  const {deps,ledger,clock,job,backend,reports} = setup(t);
  await startLaunch(job(),deps); ledger.updateJob(job().jobId,{turnCursor:'1'});
  backend.finishTurn(job().jobId,'after cursor'); backend.failReattach(2);
  assert.equal((await reattachAll(deps))[0].outcome,'unreachable');
  assert.equal(job().reattachPending,true); assert.equal(job().state,'running'); assert.equal(job().terminal,null);
  assert.equal(ledger.counts().open,1); assert.deepEqual(await retryPending(deps),[]);
  clock.advance(30000); assert.equal((await retryPending(deps))[0].outcome,'unreachable');
  assert.ok(!reports().some(r => r.state === 'failed'));
  clock.advance(30000); const [result] = await retryPending(deps);
  assert.equal(result.outcome,'live'); assert.equal(job().reattachPending,false);
  assert.ok('handle' in result);
  const signal = new AbortController(); const iter = result.handle.turns(undefined,signal.signal)[Symbol.asyncIterator]();
  const next = await iter.next(); assert.equal(next.value?.cursor.value,'2'); assert.equal(next.value?.event.type,'turn_finished');
  signal.abort(); await iter.return?.();
});

test('reattach live resumes turns from the stored cursor; parked keeps running; none → outcome_unknown', async t => {
  const {deps,ledger,job,backend,reports} = setup(t);
  await startLaunch(job(),deps); ledger.updateJob(job().jobId,{turnCursor:'1'}); backend.finishTurn(job().jobId,'new result');
  const [live] = await reattachAll(deps); assert.equal(live.outcome,'live'); assert.ok('handle' in live);
  const signal = new AbortController(); const iter = live.handle.turns(undefined,signal.signal)[Symbol.asyncIterator]();
  assert.equal((await iter.next()).value?.cursor.value,'2'); signal.abort(); await iter.return?.();
  await live.handle.park!();
  assert.equal((await reattachAll(deps))[0].outcome,'parked'); assert.equal(job().state,'running'); assert.equal(job().sessionKind,'parked');
  backend.lose(job().jobId); assert.equal((await reattachAll(deps))[0].outcome,'lost');
  assert.deepEqual(reports().at(-1)!.output,{error:'outcome_unknown'});
});

test('pending reattach survives restart, notes once after 5 minutes, backs off after an hour, and never declares transport errors lost', async t => {
  const {deps,clock,job,backend,ledger,reports} = setup(t);
  await startLaunch(job(),deps); backend.failReattach(100);
  await reattachAll(deps);
  clock.advance(300000); await retryPending({...deps});
  const notes = () => ledger.pendingOutbox(100,clock.now()).map(r => r.payload as SendRequest).filter(p => p.kind === 'message');
  assert.equal(notes().length,1); assert.equal(notes()[0].content.text,'backend unreachable; the session will be reattached when it returns');
  assert.equal(notes()[0].in_reply_to,'msg_one'); assert.deepEqual(notes()[0].recipients,['peer_requester']);
  clock.advance(3300000); await retryPending({...deps});
  clock.advance(30000); assert.deepEqual(await retryPending(deps),[]);
  clock.advance(570000); assert.equal((await retryPending(deps))[0].outcome,'unreachable');
  assert.equal(notes().length,1); assert.equal(job().state,'running'); assert.equal(job().terminal,null);
  assert.ok(!reports().some(r => r.state === 'failed'));
});

test('failed launch with unreachable lookup remains fenced; retry finds and settles it instead of adopting or relaunching', async t => {
  const {deps,clock,job,backend,reports} = setup(t);
  backend.throwAfterLaunch(new Error('response lost')); backend.failReattach(5);
  assert.deepEqual(await drain(startLaunch(job(),deps),clock),{ok:false,error:'backend_error'});
  assert.equal(job().state,'launching'); assert.equal(job().reattachPending,true); assert.equal(job().terminal,null);
  clock.advance(30000); const result = await retryPending({...deps});
  assert.equal(result[0].outcome,'lost'); assert.equal(backend.sessions().length,1); assert.equal(backend.sessions()[0].settles,1);
  assert.deepEqual(reports().at(-1)!.output,{error:'backend_error',reason:'response lost',settled_orphan:true});
});

test('an orphan whose settle fails stays pending and is settled on retry; missing backend is outcome_unknown/backend_missing', async t => {
  const {deps,clock,job,backend,reports} = setup(t);
  backend.throwAfterLaunch(new Error('response lost'));
  const reattach = backend.reattach.bind(backend); let fail = true;
  backend.reattach = async (...args) => { const r = await reattach(...args); if (r.found !== 'none' && fail) { r.handle.settle = async () => { throw new Error('offline'); }; fail=false; } return r; };
  await startLaunch(job(),deps); assert.equal(job().reattachPending,true); assert.equal(job().terminal,null);
  clock.advance(30000); await retryPending(deps); assert.equal(backend.sessions()[0].settles,1); assert.equal(job().terminal,'failed');
  assert.equal((reports().at(-1)!.output as Record<string,unknown>).settled_orphan,true);
  const other = {...job(),jobId:'job_missing',threadId:'thr_missing',state:'running' as const,terminal:null,backend:'missing',sessionRef:null};
  deps.ledger.createJob(other); deps.jobs.report(other.jobId,'accepted');
  assert.equal((await reattachAll(deps))[0].outcome,'lost');
  assert.deepEqual(reports().at(-1)!.output,{error:'outcome_unknown',reason:'backend_missing'});
});
