import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createFakeBackend } from './fake-backend.ts';
import { fakeClock } from '../../hub-client/testing/fixtures.ts';
import { launchDelivery, threadDelivery } from './fixtures.ts';
import type { LaunchSpec, SessionHooks, PermissionRequest, AgentQuestion, TurnEvent } from '../src/backend.ts';

const spec: LaunchSpec = {jobId:'job_test',tag:'vl-job_test',title:'test',prompt:'work',project:{alias:'test',repoRoot:'/tmp/project',baseRef:'master'},branch:'launch/job_test',preset:'test',permissionMode:'ask',attachments:[],env:{}};
const hooks: SessionHooks = {onEvent:() => {},onQuestion:async () => ({answers:{a:'yes'},answeredBy:'peer_requester'}),onPermissionRequest:async () => ({optionId:'allow',decidedBy:'peer_requester'})};

test('fake backend: launch, send, turns resume from a cursor, reattach from stateFile; refs carry extra.tag', async (t) => {
  const dir=mkdtempSync(join(tmpdir(),'vecnet-fake-')); t.after(() => rmSync(dir,{recursive:true,force:true}));
  const stateFile=join(dir,'state.json'); const clock=fakeClock(); const events:TurnEvent[]=[];
  const backend=createFakeBackend({stateFile,clock}); const handle=await backend.launch(spec,{...hooks,onEvent:ev => events.push(ev)});
  assert.equal(handle.ref.extra!.tag,spec.tag);
  assert.deepEqual(await backend.findByTag(spec.tag),handle.ref);
  assert.deepEqual(await backend.listOwned(),[handle.ref]);
  const input={items:[{deliveryId:'dly_one',messageId:'msg_one',from:'peer_one',text:'hello',attachments:[]}]};
  const sent=await handle.send(input,'start'); await handle.send(input,'start');
  assert.equal(sent.mode,'start'); assert.equal(backend.sessions()[0].sent.length,1);
  const activeAt=(await handle.activity()).lastActivityAt; clock.advance(100); assert.equal((await handle.activity()).lastActivityAt,activeAt);
  backend.finishTurn(spec.jobId,'done'); assert.equal(handle.state().kind,'idle');
  const stream=handle.turns()[Symbol.asyncIterator](); const first=await stream.next();
  assert.equal(first.value!.event.type,'turn_started');
  const resumed=handle.turns(first.value!.cursor)[Symbol.asyncIterator](); const next=await resumed.next();
  assert.equal(next.value!.event.type,'turn_finished'); assert.equal(next.value!.event.finalText,'done');
  assert.ok(Number(next.value!.cursor.value)>Number(first.value!.cursor.value));
  await stream.return?.(); await resumed.return?.();
  const restarted=createFakeBackend({stateFile,clock}); const attached=await restarted.reattach(handle.ref,hooks); assert.equal(attached.found,'live');
  assert.equal(attached.handle.ref.extra!.tag,spec.tag);
  assert.deepEqual(await attached.handle.send(input,'start'),sent); assert.equal(restarted.sessions()[0].sent.length,1);
  const persisted=attached.handle.turns(first.value!.cursor)[Symbol.asyncIterator](); assert.equal((await persisted.next()).value!.event.type,'turn_finished'); await persisted.return?.();
  await attached.handle.interrupt('test'); assert.equal(restarted.sessions()[0].interrupts,1);
  await attached.handle.park!(); assert.equal((await restarted.reattach(handle.ref,hooks)).found,'parked'); assert.equal(restarted.sessions()[0].parks,1);
  const result=await attached.handle.settle('requested'); assert.equal(result.branch,spec.branch); assert.equal(restarted.sessions()[0].settles,1); assert.deepEqual(await restarted.listOwned(),[]);
  assert.equal(events.length,2);
  const launch=launchDelivery({backend:'fake'}); assert.equal(launch.message.job_request!.operation,'launch'); assert.equal(typeof launch.message.metadata['vecnet.job_id'],'string');
  const another=launchDelivery({backend:'fake'}); assert.notEqual(another.delivery_id,launch.delivery_id); assert.notEqual(another.message.message_id,launch.message.message_id); assert.notEqual(another.message.thread_id,launch.message.thread_id);
  const thread=threadDelivery('thr_test','peer_sender','hello','hold'); assert.equal(thread.message.thread_id,'thr_test'); assert.equal(thread.message.sender,'peer_sender'); assert.equal(thread.message.content.text,'hello'); assert.equal(thread.message.priority,'hold');
});

test('fake backend: failReattach throws N times then returns live; throwAfterLaunch leaves a findable session', async () => {
  const backend=createFakeBackend(); const handle=await backend.launch(spec,hooks);
  backend.failReattach(2); await assert.rejects(backend.reattach(handle.ref,hooks)); await assert.rejects(backend.findByTag(spec.tag));
  assert.equal((await backend.reattach(handle.ref,hooks)).found,'live');
  backend.hideTagOnce(spec.tag); assert.equal(await backend.findByTag(spec.tag),null); assert.deepEqual(await backend.findByTag(spec.tag),handle.ref);
  const other={...spec,jobId:'job_other',tag:'vl-job_other'};
  backend.throwAfterLaunch(new Error('lost response')); await assert.rejects(backend.launch(other,hooks),/lost response/);
  const ref=await backend.findByTag(other.tag); assert.ok(ref); assert.equal(ref.extra!.tag,other.tag);
  backend.failNextLaunch(new Error('before create')); await assert.rejects(backend.launch({...spec,jobId:'job_failed',tag:'vl-job_failed'},hooks),/before create/); assert.equal(await backend.findByTag('vl-job_failed'),null);
  backend.setActivity(spec.jobId,{backgroundTasks:{known:2,observable:true}}); assert.equal((await handle.activity()).backgroundTasks.known,2);
  backend.exitProcess(spec.jobId,true); assert.equal(handle.state().kind,'ended');
  backend.lose(spec.jobId); assert.deepEqual(await backend.reattach(handle.ref,hooks),{found:'none'});
});

test('fake backend: hooks round-trip a question and a permission draft', async () => {
  const backend=createFakeBackend(); let question:AgentQuestion|undefined; let permission:PermissionRequest|undefined;
  await backend.launch(spec,{...hooks,onQuestion:async q => {question=q; return {answers:{q:'yes'},answeredBy:'peer_one'};},onPermissionRequest:async r => {permission=r; return {optionId:'deny',decidedBy:'expired'};}});
  const q:AgentQuestion={requestId:'q1',blocking:true,questions:[{id:'q',text:'continue?',allowOther:true,secret:false}]};
  assert.deepEqual(await backend.ask(spec.jobId,q),{answers:{q:'yes'},answeredBy:'peer_one'}); assert.deepEqual(question,q);
  const r:PermissionRequest={requestId:'p1',summary:'edit file',defaultOnExpiry:'deny',options:[{id:'deny',label:'deny',grants:'deny'}],permission:{kind:'edit',backend_method:'edit',scope:'once',options:[{id:'deny',label:'deny',grants:'deny'}],paths:[{path:'/tmp/a',access:'write'}],backend_raw:{path:'/tmp/a'},fidelity:{kind:'enforced',command:'none',paths:'enforced',host:'none'}}};
  assert.deepEqual(await backend.requestPermission(spec.jobId,r),{optionId:'deny',decidedBy:'expired'}); assert.deepEqual(permission,r);
});
