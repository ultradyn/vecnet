import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { SendRequest } from '../../hub-client/src/wire.ts';
import { createFakeBackend } from './fake-backend.ts';
import { openLedger } from '../src/ledger.ts';
import { createJobReporter } from '../src/jobs.ts';
import { createOutbox } from '../src/outbox.ts';
import { parseConfig } from '../src/config.ts';
import { buildPrompt, buildLaunchSpec, PROMPT_FOOTER } from '../src/prompt.ts';
import { startLaunch, findByTagWithRetry } from '../src/launch.ts';
import type { LaunchDeps } from '../src/launch.ts';
import type { SessionHooks } from '../src/backend.ts';

const hooks: SessionHooks = { onEvent() {}, async onPermissionRequest(r) { return {optionId:r.defaultOnExpiry,decidedBy:'expired'}; }, async onQuestion() { return {answers:{},answeredBy:'timeout'}; } };
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return {promise,resolve};
}
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
async function drain<T>(promise: Promise<T>, clock: ReturnType<typeof fakeClock>): Promise<T> {
  let done = false; void promise.then(() => { done = true; },() => { done = true; });
  for (let i = 0; i < 100 && !done; i++) { await Promise.resolve(); clock.advance(30000); }
  return promise;
}

test('buildPrompt appends the footer once; buildLaunchSpec sets tag, prefixed title, branch launch/<job> only when settable, scrubbed env without VECNET_*', async t => {
  const {deps,job,backend} = setup(t);
  const prompt = buildPrompt('Fix the launch race.',job());
  assert.ok(prompt.startsWith('Fix the launch race.\n'));
  assert.ok(prompt.includes('If you need a decision from your requester, end your turn with one clear question; your requester will answer in this conversation.'));
  assert.ok(prompt.includes('Work on branch launch/job_one; do not push.'));
  assert.equal(buildPrompt(prompt,job()),prompt);
  assert.equal(prompt.split(PROMPT_FOOTER).length,2);
  const caps = await backend.capabilities();
  const spec = buildLaunchSpec(job(),deps.projects(job()),caps,{PATH:'/bin',VECNET_TOKEN:'secret',VECNET_URL:'secret',OPENAI_API_KEY:'secret',OTHER_TOKEN:'secret'});
  assert.deepEqual({tag:spec.tag,title:spec.title,branch:spec.branch,prompt:spec.prompt,env:spec.env}, {tag:'vl-job_one',title:'vl-job_one A job',branch:'launch/job_one',prompt,env:{PATH:'/bin'}});
  assert.equal(spec.permissionMode,'ask'); assert.equal(spec.preset,'test'); assert.equal(spec.project.repoRoot,'/repo');
  const chosen = buildLaunchSpec(job(),deps.projects(job()),{...caps,branchName:'backend-chosen'},{});
  assert.equal(chosen.branch,'');
  assert.ok(!chosen.prompt.includes('Work on branch launch/job_one; do not push.'));
});

test('startLaunch: accepted → launching → running with session_ref, branch and link from ref.extra; job_status sequence 2', async t => {
  const {deps,ledger,job,backend,reports} = setup(t);
  const launch = backend.launch.bind(backend);
  backend.launch = async (...args) => { assert.equal(job().state,'launching'); return launch(...args); };
  const result = await startLaunch(job(),deps);
  assert.equal(result.ok,true);
  assert.equal(job().state,'running'); assert.equal(job().branch,'launch/job_one'); assert.equal(job().workspace,'/repo');
  assert.equal(job().sessionRef?.id,'job_one');
  assert.deepEqual(reports().map(r => [r.state,r.sequence]),[['accepted',1],['running',2]]);
  assert.deepEqual(reports()[1].output,{session_ref:job().sessionRef,branch:'launch/job_one',workspace:'/repo',link:'fake:job_one'});
  await startLaunch(job(),deps); assert.equal(backend.sessions().length,1); assert.equal(ledger.job('job_one')!.statusSeq,2);
});

for (const terminal of ['cancelled','failed'] as const) {
  test(`a launch resolving after ${terminal} keeps the job ended and settles the late handle`, async t => {
    const {deps,ledger,job,backend,reports} = setup(t);
    const entered = deferred(), release = deferred();
    const launch = backend.launch.bind(backend);
    backend.launch = async (...args) => {
      const handle = await launch(...args);
      entered.resolve(); await release.promise;
      return handle;
    };
    const pending = startLaunch(job(),deps);
    await entered.promise;
    deps.jobs.report(job().jobId,terminal);
    const endedAt = job().endedAt;
    assert.equal(ledger.counts().open,0);
    release.resolve();
    const result = await pending;
    assert.equal(job().state,'ended'); assert.equal(job().terminal,terminal);
    assert.equal(job().endedAt,endedAt); assert.equal(ledger.counts().open,0);
    assert.equal(result.ok,false); assert.ok(!('handle' in result));
    assert.equal(backend.sessions()[0].settles,1); assert.equal(backend.sessions()[0].state.kind,'ended');
    assert.deepEqual(job().sessionRef,backend.sessions()[0].ref);
    assert.deepEqual(reports().map(r => [r.state,r.sequence]),[['accepted',1],[terminal,2]]);
  });
}

test('startLaunch failure with no session → failed backend_error, no retry', async t => {
  const {deps,clock,job,backend,reports} = setup(t);
  let calls = 0; const launch = backend.launch.bind(backend);
  backend.launch = (...args) => { calls++; return launch(...args); };
  backend.failNextLaunch(new Error('Bearer abc123 '+ 'x'.repeat(700)));
  const result = await drain(startLaunch(job(),deps),clock);
  assert.deepEqual(result,{ok:false,error:'backend_error'});
  assert.equal(calls,1); assert.equal(backend.sessions().length,0); assert.equal(job().terminal,'failed');
  const output = reports().at(-1)!.output as Record<string,unknown>;
  assert.equal(output.error,'backend_error'); assert.ok(String(output.reason).length <= 512); assert.ok(!String(output.reason).includes('abc123'));
});

test('startLaunch failure after the session was created (throwAfterLaunch) → found by tag, settled at once, failed backend_error settled_orphan', async t => {
  const {deps,job,backend,reports} = setup(t);
  backend.throwAfterLaunch(new Error('launch response lost'));
  const result = await startLaunch(job(),deps);
  assert.deepEqual(result,{ok:false,error:'backend_error'});
  assert.equal(backend.sessions().length,1); assert.equal(backend.sessions()[0].settles,1);
  assert.equal(backend.sessions()[0].state.kind,'ended'); assert.equal(job().sessionRef?.id,'job_one');
  assert.deepEqual(reports().at(-1)!.output,{error:'backend_error',reason:'launch response lost',settled_orphan:true});
  assert.ok(!reports().some(r => r.state === 'running'));
});

test('findByTagWithRetry uses the Clock, retries misses and errors, and distinguishes unreachable', async t => {
  const {deps,clock,backend} = setup(t); const times: number[] = [];
  backend.findByTag = async () => { times.push(clock.now()); throw new Error('secret transport body'); };
  const pending = findByTagWithRetry(backend,'vl-job_one',deps);
  for (let i=0;i<10;i++) await Promise.resolve();
  assert.equal(times.length,1); clock.advance(1999); await Promise.resolve(); assert.equal(times.length,1);
  clock.advance(1); for (let i=0;i<10;i++) await Promise.resolve(); assert.equal(times.length,2);
  assert.equal(await drain(pending,clock),'unreachable'); assert.equal(times.length,5);
  assert.ok(!deps.log || !('text' in deps.log) || !String((deps.log as ReturnType<typeof memoryLog>).text()).includes('secret transport body'));
});

test('lookup retries a transient tag miss and finds the one existing session', async t => {
  const {deps,clock,backend,job} = setup(t);
  const launched = await startLaunch(job(),deps); assert.ok(launched.ok);
  backend.hideTagOnce(job().tag);
  assert.deepEqual(await drain(findByTagWithRetry(backend,job().tag,deps),clock),launched.handle.ref);
  assert.equal(backend.sessions().length,1);
});

test('backend-chosen branch leaves original instructions intact; missing instructions fail closed', async t => {
  const {job,backend,deps} = setup(t); const caps = await backend.capabilities();
  const instructions = 'Work on branch launch/job_one; do not push.\nThese words are quoted input.';
  const withInstructions = {...job(),instructions};
  const spec = buildLaunchSpec(withInstructions,deps.projects(job()),{...caps,branchName:'backend-chosen'},{});
  assert.ok(spec.prompt.startsWith(instructions)); assert.ok(spec.prompt.endsWith('Do not push.')); assert.equal(spec.branch,'');
  const withoutInstructions = {...job(),instructions:undefined};
  assert.throws(() => buildLaunchSpec(withoutInstructions,deps.projects(job()),caps,{}),/instructions missing/);
});
