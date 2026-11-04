import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { SendRequest, ReceiptBody } from '../../hub-client/src/wire.ts';
import { openLedger } from '../src/ledger.ts';
import type { JobRow } from '../src/ledger.ts';
import { createOutbox } from '../src/outbox.ts';
import { createJobReporter } from '../src/jobs.ts';
import { createLane, turnReply } from '../src/session.ts';
import type { LaneDeps } from '../src/session.ts';
import { redactText } from '../src/redact.ts';
import type { BackendCapabilities, SessionHandle, SessionState, TurnEvent, TurnInput, SendMode, SettleMode } from '../src/backend.ts';
import { threadDelivery } from './fixtures.ts';
import { createFakeBackend } from './fake-backend.ts';

const caps: BackendCapabilities = {steer:true,queue:'launcher',interrupt:true,approvals:'none',questions:'text-only',resume:'transcript',backgroundTasks:'observable',attachments:false,workspace:'backend-worktree',branchName:'settable',permissionModes:['ask'],presets:[]};
const finished = (id = 'turn-1', text = 'done', consumed: string[] = []): Extract<TurnEvent,{type:'turn_finished'}> => ({type:'turn_finished',turnId:id,at:new Date(0).toISOString(),outcome:'completed',finalText:text,consumed});
const tick = async () => { for (let i = 0; i < 20; i++) await new Promise<void>(resolve => setImmediate(resolve)); };
async function setup(t: TestContext, opts: {attach?: boolean; kind?: SessionState['kind']; caps?: Partial<BackendCapabilities>; input?: Partial<JobRow['input']>} = {}) {
  const clock = fakeClock(100), log = memoryLog(), ledger = openLedger(':memory:',clock);
  ledger.createJob({jobId:'job_one',threadId:'thr_one',launchMessageId:'msg_launch',requester:'req',policyKey:'req',backend:'fake',preset:'test',permissionMode:'ask',project:'repo',title:'job',input:{backend:'fake',preset:'test',project:'repo',permission_mode:'ask',...opts.input},tag:'vl-job_one',state:opts.attach === false ? 'launching':'running',sessionRef:null,sessionKind:opts.kind ?? 'idle',turnCursor:null,branch:'launch/job_one',workspace:'/repo',lastActivityAt:100,parkedAt:null,deadlineAt:100000,createdAt:100});
  const job = ledger.job('job_one')!;
  const outbox = createOutbox({ledger,clock,log,hub:{async send(){throw new Error('not dispatched');},async receipt(){}}});
  let kind = opts.kind ?? 'idle';
  const sends: Array<{input:TurnInput;mode:SendMode;batchIds:Array<string|null>}> = [], calls: string[] = [], events: Array<{cursor:{value:string};event:TurnEvent}> = [];
  let wake: (() => void) | undefined;
  let next = 0;
  const handle: SessionHandle = {
    ref:{backend:'fake',host:'test',id:'one'},state:() => ({kind} as SessionState),
    async send(input,mode){sends.push({input:structuredClone(input),mode,batchIds:ledger.inputs(job.jobId,['sending']).map(r=>r.batchId)}); kind='turn_running'; return {mode};},
    async *turns(after,signal){ let i=Number(after?.value ?? 0); while (!signal?.aborted) { if(i<events.length) yield events[i++]; else await new Promise<void>(resolve=>{wake=resolve;signal?.addEventListener('abort',()=>resolve(),{once:true});if(signal?.aborted)resolve();}); } },
    async interrupt(){calls.push('interrupt');},async activity(){return {turnRunning:kind==='turn_running',openApprovals:0,queuedInputs:0,backgroundTasks:{known:0,observable:true},lastActivityAt:new Date(clock.now()).toISOString()};},
    async park(){calls.push('park');kind='parked';},async settle(mode:SettleMode){calls.push(`settle:${mode}`);kind='ended';return {branch:'launch/job_one',headSha:'abc',commits:['abc'],workspaceKept:true,summary:'Bearer hidden-secret'};},
  };
  let delivered=0, activities=0, ended=0;
  const blobs: Array<{bytes:Uint8Array;mediaType:string}> = [], fetched:string[]=[];
  const deps: LaneDeps = {ledger,outbox,jobs:createJobReporter({ledger,outbox}),dir:{name:id=>id,isOperator:id=>id==='op',operatorPeerId:()=> 'op'},hub:{async fetchBlob(url,options){fetched.push(url);assert.equal(options.maxBytes,256*1024);return Buffer.from('full spilled text');},async putBlob(bytes,mediaType){blobs.push({bytes,mediaType});return {sha256:'a'.repeat(64),size:bytes.length};}},clock,log,self:'self',caps:{...caps,...opts.caps},async deliver(){delivered++;return {pushed:true};},onActivity(){activities++;},onEnded(){ended++;}};
  const lane=createLane(job,deps);
  if(opts.attach!==false)lane.attach(handle);
  t.after(async()=>{await lane.stop();ledger.close();});
  const rows=()=>ledger.pendingOutbox(1000,clock.now());
  const replies=()=>rows().filter(r=>r.kind==='send' && !(r.payload as SendRequest).job_status).map(r=>r.payload as SendRequest);
  const receipts=()=>rows().filter(r=>r.kind==='receipt').map(r=>r.payload as ReceiptBody);
  const statuses=()=>rows().map(r=>r.payload as SendRequest).filter(p=>p.job_status).map(p=>p.job_status!);
  return {lane,ledger,job,deps,clock,log,handle,sends,calls,blobs,fetched,rows,replies,receipts,statuses,delivered:()=>delivered,activities:()=>activities,ended:()=>ended,
    message:(text:string,priority:'wake'|'wake_idle'|'hold'='wake',sender='req')=>threadDelivery(job.threadId,sender,text,priority),
    async emit(event:TurnEvent){if(event.type==='turn_finished')kind='idle';events.push({cursor:{value:String(++next)},event});wake?.();await tick();},
  };
}

test("wake mid-turn steers; wake_idle mid-turn queues and is sent as one turn after it; hold is prepended to the next turn's input",async t=>{
  const s=await setup(t,{kind:'turn_running'});
  const steer=s.message('steer'),hold=s.message('hold','hold'),q1=s.message('q1','wake_idle'),q2=s.message('q2','wake_idle');
  assert.equal(await s.lane.handleMessage(steer),'relayed');assert.equal(s.sends[0].mode,'steer');
  assert.equal(await s.lane.handleMessage(hold),'buffered');await s.lane.handleMessage(q1);await s.lane.handleMessage(q2);assert.equal(s.sends.length,1);
  await s.emit(finished());assert.equal(s.sends.length,2);assert.equal(s.sends[1].mode,'start');
  assert.deepEqual(s.sends[1].input.items.map(i=>i.deliveryId),[hold.delivery_id,q1.delivery_id,q2.delivery_id]);
  assert.equal((await s.lane.activity()).launcherBuffered,0);
});
test('non-relayed message → one processed receipt note not_relayed (key vl.rc.<dly>.processed), no reply, no backend send',async t=>{
  const s=await setup(t),d=s.message('attack','wake','other');assert.equal(await s.lane.handleMessage(d),'not_relayed');await s.lane.handleMessage(d);
  assert.deepEqual(s.receipts(),[{delivery_id:d.delivery_id,state:'processed',note:'not_relayed'}]);assert.equal(s.rows()[0].key,`vl.rc.${d.delivery_id}.processed`);assert.equal(s.replies().length,0);assert.equal(s.sends.length,0);
});
test('message to an ended session → failed receipt note session_ended',async t=>{
  const s=await setup(t,{kind:'ended'}),d=s.message('late');assert.equal(await s.lane.handleMessage(d),'refused');assert.deepEqual(s.receipts(),[{delivery_id:d.delivery_id,state:'failed',note:'session_ended'}]);
});
test('turn finished posts exactly one reply in_reply_to the launch message with launch.* metadata; consumed comes from ledger turnSeq; a replayed turn event posts nothing',async t=>{
  const s=await setup(t,{input:{reply_priority:'hold'}});
  for(const [id,mode,turnSeq] of [['start','start',0],['steer','steer',0],['oldqueue','queue',-1],['queue','queue',0]] as const){s.ledger.addInput({deliveryId:id,jobId:s.job.jobId,messageId:`msg_${id}`,sender:'req',text:id,priority:'wake',createdAt:100});s.ledger.markInputs([id],{state:'sent',mode,turnSeq});}
  await s.emit(finished('one','Bearer private',['spoof']));assert.equal(s.replies().length,1);
  const r=s.replies()[0];assert.equal(r.in_reply_to,'msg_launch');assert.equal(r.thread_id,'thr_one');assert.equal(r.recipients,undefined);assert.equal(r.priority,'hold');assert.equal(r.content.text,'Bearer [redacted]');
  assert.deepEqual(r.metadata,{'launch.turn':'completed','launch.turn_id':'one','launch.job_id':'job_one','launch.consumed':['msg_start','msg_steer','msg_oldqueue']});
  assert.deepEqual(s.receipts().map(r=>r.delivery_id),['start','steer','oldqueue']);assert.equal(s.ledger.inputs('job_one',['sent'])[0].deliveryId,'queue');
  assert.match(s.log.text(),/consumed.*mismatch/);await s.emit(finished('one'));assert.equal(s.replies().length,1);assert.equal(s.ledger.job('job_one')!.turns,1);assert.equal(s.ledger.job('job_one')!.turnCursor,'2');
});
test('a 100 KiB final text is capped at 60 KiB with the marker and the full text attached via putBlob',async t=>{
  const s=await setup(t);await s.emit(finished('long','x'.repeat(100*1024)));const r=s.replies()[0];assert.ok(Buffer.byteLength(r.content.text)<=60*1024);assert.match(r.content.text,/\[truncated: \d+ bytes; full text attached\]$/);assert.equal(s.blobs[0].bytes.length,100*1024);assert.equal(s.blobs[0].mediaType,'text/markdown');assert.deepEqual(r.attachments,[{sha256:'a'.repeat(64),size:100*1024,media_type:'text/markdown',name:'turn-1.md'}]);
});
test('launch.consumed over 100 ids is capped with launch.consumed_truncated',async t=>{
  const s=await setup(t);const ids=Array.from({length:103},(_,i)=>`msg_${i}`);const r=turnReply(s.job,1,finished(),ids,redactText).req;assert.deepEqual(r.metadata!['launch.consumed'],ids.slice(0,100));assert.equal(r.metadata!['launch.consumed_truncated'],3);
});
test('a crash between marking sending and send: attach resends the same batch with the same batchId',async t=>{
  const s=await setup(t,{attach:false});const ds=[s.message('first'),s.message('second')];for(const d of ds)await s.lane.handleMessage(d);
  const ids=ds.map(d=>d.delivery_id),batchId=createHash('sha256').update([...ids].sort().join('\n')).digest('hex').slice(0,24);
  s.ledger.markInputs(ids,{state:'sending',mode:'start',batchId,turnSeq:0});s.lane.attach(s.handle);await tick();
  assert.equal(s.sends.length,1);assert.deepEqual(s.sends[0].input.items.map(i=>i.deliveryId),ids);assert.equal((s.sends[0].input as TurnInput & { batchId?: string }).batchId,batchId);assert.deepEqual(s.sends[0].batchIds,[batchId,batchId]);assert.equal(s.ledger.inputs('job_one',['sent']).length,2);
});
test('spilled inbound text is rehydrated before relay',async t=>{
  const s=await setup(t),d=s.message('preview');d.message.content.truncated=true;d.message.metadata['vecnet.spilled']=true;d.message.attachments=[{name:'message.md',sha256:'b'.repeat(64),size:40,media_type:'text/markdown',blob_url:'https://hub/blob?t=secret'}];await s.lane.handleMessage(d);assert.deepEqual(s.fetched,['https://hub/blob?t=secret']);assert.match(s.sends[0].input.items[0].text,/> full spilled text/);assert.ok(!s.sends[0].input.items[0].text.includes('preview'));
});
test('merged inputs over the cap split: the overflow is queued for the next turn; caps.maxInputBytes lowers the cap',async t=>{
  for(const limit of [96*1024,400]){const s=await setup(t,{attach:false,caps:{maxInputBytes:limit}});const ds=[s.message('a'.repeat(Math.floor(limit*.55))),s.message('b'.repeat(Math.floor(limit*.55)))];for(const d of ds)await s.lane.handleMessage(d);s.lane.attach(s.handle);await tick();assert.equal(s.sends.length,1);assert.ok(s.sends[0].input.items.reduce((n,i)=>n+Buffer.byteLength(i.text),0)<=limit);assert.equal(s.ledger.inputs('job_one',['buffered']).length,1);await s.emit(finished());assert.equal(s.sends.length,2);assert.deepEqual(s.sends.map(send=>send.input.items[0].deliveryId),ds.map(d=>d.delivery_id));}
});
for (const maxInputBytes of [4, 5]) {
  test(`maxInputBytes ${maxInputBytes} is rejected before Unicode fragmentation can stall`, async t => {
    const s = await setup(t, { attach: false });
    assert.throws(() => createLane(s.job, {
      ...s.deps, caps: { ...caps, maxInputBytes },
    }), /invalid backend input cap/);
    assert.equal(s.ledger.inputs(s.job.jobId, ['buffered', 'sending', 'sent']).length, 0);
    assert.equal(s.sends.length, 0);
  });
}
test('the minimum six-byte input cap consumes four-byte Unicode across quoted fragments', async t => {
  const s = await setup(t, { caps: { maxInputBytes: 6 } });
  const d = s.message('😀😀');
  await s.lane.handleMessage(d);
  const formatted = s.ledger.inputs(s.job.jobId, ['sent'])[0].text;
  let turns = 0;
  while (s.ledger.inputs(s.job.jobId, ['consumed']).length === 0 && turns < 100) {
    await s.emit(finished(`unicode-${++turns}`));
  }
  assert.ok(turns > 1 && turns < 100);
  const fragments = s.sends.flatMap(send => send.input.items.map(item => item.text));
  assert.ok(fragments.every(text => Buffer.byteLength(text) <= 6 && !text.includes('\uFFFD')));
  assert.equal(fragments.map((text, i) => i === 0 ? text : text.slice(2)).join(''), formatted);
  assert.ok(fragments.includes('> 😀'));
  assert.deepEqual(s.receipts(), [{ delivery_id: d.delivery_id, state: 'processed' }]);
});
test('control settle from requester → succeeded ended settled; control from a non-relayed peer ignored',async t=>{
  const s=await setup(t);const d=s.message('do not relay','wake','other');d.message.metadata['launch.control']='settle';await s.lane.handleMessage(d);await s.lane.control('settle','other');assert.equal(s.calls.length,0);assert.equal(s.sends.length,0);assert.equal(s.receipts()[0].note,'not_relayed');await s.lane.control('settle','req');assert.equal(s.statuses()[0].state,'succeeded');assert.equal((s.statuses()[0].output as Record<string,unknown>).ended,'settled');
});
test('cancel → interrupt then settle(cancel) then cancelled with branch and workspace_kept',async t=>{
  const s=await setup(t);await s.lane.cancel('other');assert.deepEqual(s.calls,[]);await s.lane.cancel('req');assert.deepEqual(s.calls,['interrupt','settle:cancel']);assert.equal(s.statuses()[0].state,'cancelled');assert.deepEqual(s.statuses()[0].output,{branch:'launch/job_one',head_sha:'abc',workspace_kept:true});assert.equal(s.ended(),1);
});
test('settle table: deadline → succeeded ended deadline; requester_retired → cancelled reason requester_retired',async t=>{
  for(const reason of ['deadline','requester_retired'] as const){const s=await setup(t);await s.lane.settle('requested',reason);const p=s.statuses()[0];assert.equal(p.state,reason==='deadline'?'succeeded':'cancelled');assert.equal((p.output as Record<string,unknown>)[reason==='deadline'?'ended':'reason'],reason);assert.equal(s.ended(),1);}
});
test('non-resumable process exit → failed session_exited, pending inputs get failed receipts',async t=>{
  const s=await setup(t,{kind:'turn_running'}),d=s.message('pending','hold');await s.lane.handleMessage(d);await s.emit({type:'process_exited',at:new Date(0).toISOString(),code:1,resumable:false});assert.equal(s.statuses()[0].state,'failed');assert.deepEqual(s.statuses()[0].output,{error:'session_exited'});assert.deepEqual(s.calls,['settle:lost']);assert.deepEqual(s.receipts(),[{delivery_id:d.delivery_id,state:'failed',note:'session_ended'}]);assert.equal(s.ledger.inputs('job_one',['failed']).length,1);
});
test('deliver.push_branch calls deliver and reports pushed; summary is redacted',async t=>{
  const s=await setup(t,{input:{deliver:{push_branch:true}}});await s.lane.settle('requested','settled');assert.equal(s.delivered(),1);assert.deepEqual(s.statuses()[0].output,{ended:'settled',turns:0,branch:'launch/job_one',head_sha:'abc',commits:['abc'],pushed:true,summary:'Bearer [redacted]'});
});

test('oversized spilled input continues across capped sends and is processed only after its last fragment',async t=>{
  const s=await setup(t,{caps:{maxInputBytes:400}}),d=s.message('a'.repeat(1100));
  await s.lane.handleMessage(d);
  assert.equal(s.sends.length,1);assert.equal(s.receipts().length,0);
  let turns=0;
  while(s.ledger.inputs('job_one',['consumed']).length===0 && turns<10){await s.emit(finished(`part-${++turns}`));}
  assert.ok(turns>1 && turns<10);assert.equal(s.receipts().length,1);assert.equal(s.receipts()[0].delivery_id,d.delivery_id);
  assert.ok(s.sends.every(send=>Buffer.byteLength(send.input.items.map(i=>i.text).join('\n\n'))<=400));
  assert.equal(s.sends.flatMap(send=>send.input.items).map((item,i)=>i===0 ? item.text.slice(item.text.indexOf('\n')+3) : item.text.slice(2)).join(''),'a'.repeat(1100));
  assert.equal(new Set(s.sends.flatMap(send=>send.input.items).map(i=>i.deliveryId)).size,s.sends.length);
});
test('a sending batch preserves prepended hold order across restart and reuses the stored batch id',async t=>{
  const s=await setup(t,{kind:'turn_running'});const queued=s.message('queued','wake_idle'),hold=s.message('hold','hold');
  await s.lane.handleMessage(queued);await s.lane.handleMessage(hold);
  const original=s.handle.send;let attempted:TurnInput|undefined,attemptedMode:SendMode|undefined;
  s.handle.send=async (input,mode)=>{attemptedMode=mode;attempted=structuredClone(input);throw new Error('crash before send');};
  await s.emit(finished());assert.ok(attempted);assert.equal(s.ledger.inputs('job_one',['sending']).length,2);
  const batch=createHash('sha256').update([hold.delivery_id,queued.delivery_id].sort().join('\n')).digest('hex').slice(0,24);
  const expected = { batchId: batch, items: [hold, queued].map(d => ({deliveryId:d.delivery_id,messageId:d.message.message_id,from:'req',text:s.ledger.inputs('job_one',['sending']).find(r=>r.deliveryId===d.delivery_id)!.text,attachments:[]})) };
  assert.equal(attemptedMode,'start');assert.deepEqual(attempted,expected);
  assert.equal(s.ledger.inputs('job_one',['sending'])[0].mode,'start');
  await s.lane.stop();s.handle.send=original;
  const resumed=createLane(s.ledger.job('job_one')!,s.deps);resumed.attach(s.handle);await tick();
  assert.equal(s.sends[0].mode,'start');assert.deepEqual(s.sends[0].input,expected);assert.deepEqual(s.sends[0].input,attempted);assert.deepEqual(s.sends[0].input.items.map(i=>i.deliveryId),[hold.delivery_id,queued.delivery_id]);assert.deepEqual(s.sends[0].batchIds,[batch,batch]);await resumed.stop();
});
test('native queued input belongs to the following turn; hold alone never starts; controls and stop retain their boundaries',async t=>{
  const s=await setup(t,{kind:'turn_running',caps:{queue:'native',steer:false}}),q=s.message('queued','wake_idle');
  assert.equal(await s.lane.handleMessage(q),'buffered');assert.equal(s.sends.length,0);await s.emit(finished('initial'));assert.equal(s.sends[0].mode,'start');assert.equal(s.receipts().length,0);
  await s.emit(finished('queued'));assert.equal(s.receipts()[0].delivery_id,q.delivery_id);
  const hold=s.message('hold','hold');await s.lane.handleMessage(hold);assert.equal(s.sends.length,1);
  s.clock.advance(25);await s.lane.control('extend','op');assert.equal(s.ledger.job('job_one')!.lastActivityAt,125);assert.ok(s.activities()>0);
  await s.lane.control('interrupt','self');assert.deepEqual(s.calls,['interrupt']);await s.lane.stop();assert.deepEqual(s.calls,['interrupt']);assert.equal(s.ledger.job('job_one')!.terminal,null);
});
test('resumable exit parks; expired records failure; blob upload errors preserve bounded text without secrets',async t=>{
  const s=await setup(t);await s.emit({type:'process_exited',at:new Date(0).toISOString(),code:0,resumable:true});assert.equal(s.ledger.job('job_one')!.sessionKind,'parked');assert.equal(s.statuses().length,0);
  s.deps.hub.putBlob=async()=>{throw new Error('Bearer never-log-me');};await s.emit(finished('long-secret','Bearer never-log-me\n'+'😀'.repeat(30000)));
  assert.equal(s.replies().length,1);assert.ok(Buffer.byteLength(s.replies()[0].content.text)<=60*1024);assert.ok(!s.replies()[0].content.text.includes('�'));assert.ok(!s.log.text().includes('never-log-me'));assert.match(s.log.text(),/upload failed/);
  await s.lane.settle('lost','expired');assert.deepEqual(s.statuses()[0].output,{error:'expired'});assert.equal(s.statuses()[0].state,'failed');
});

test('reattaching a transcript-resumable exited handle accepts a wake as parked input',async t=>{
  const s=await setup(t,{attach:false});
  s.handle.state=()=>({kind:'ended',reason:'exited',resumable:true});
  s.lane.attach(s.handle);await tick();
  const d=s.message('resume');assert.equal(await s.lane.handleMessage(d),'relayed');assert.equal(s.sends[0].mode,'start');
});
test('backend send failures never expose an exception containing the relayed body',async t=>{
  const s=await setup(t);s.handle.send=async()=>{throw new Error('secret body of user message');};
  await assert.rejects(s.lane.handleMessage(s.message('secret body of user message')),error=>error instanceof Error && !error.message.includes('secret body'));
  assert.ok(!s.log.text().includes('secret body'));assert.equal(s.ledger.inputs('job_one',['sending']).length,1);
});


test('restart reconciles an unread completion before dispatching buffered future input', async t => {
  const s = await setup(t, { attach: false, kind: 'turn_running' });
  const future = s.message('future input');
  await s.lane.handleMessage(future);
  await s.emit(finished('before-restart'));
  // The process is idle, while the durable lane still says its turn is running.
  s.ledger.updateJob(s.job.jobId, { state: 'running', sessionKind: 'turn_running' });
  s.lane.attach(s.handle);
  await tick();
  assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], []);
  assert.equal(s.receipts().length, 0);
  assert.equal(s.sends.length, 1);
  assert.equal(s.ledger.inputs(s.job.jobId, ['sent'])[0].turnSeq, 1);
  await s.emit(finished('after-restart'));
  assert.deepEqual(s.replies()[1].metadata!['launch.consumed'], [future.message.message_id]);
  assert.equal(s.receipts()[0].delivery_id, future.delivery_id);
});

test('restart recovery survives a reattach refresh of the job session kind', async t => {
  const s = await setup(t, { kind: 'turn_running' });
  await tick(); await s.lane.stop();
  await s.emit(finished('unread-before-reattach'));
  // Task 10 refreshes sessionKind from the backend before handing off a handle.
  s.ledger.updateJob(s.job.jobId, { sessionKind: 'idle' });
  const resumed = createLane(s.ledger.job(s.job.jobId)!, s.deps), future = s.message('after refresh');
  try {
    await resumed.handleMessage(future); resumed.attach(s.handle); await tick();
    assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], []);
    assert.equal(s.receipts().length, 0);
    assert.equal(s.ledger.inputs(s.job.jobId, ['sent'])[0].turnSeq, 1);
    await s.emit(finished('new-after-reattach'));
    assert.deepEqual(s.replies()[1].metadata!['launch.consumed'], [future.message.message_id]);
  } finally { await resumed.stop(); }
});

test('a wake restarts a completed resumable turn iterator from its saved cursor', async t => {
  const s = await setup(t, { attach: false });
  const turns = s.handle.turns;
  let streams = 0, active = 0, maxActive = 0;
  const cursors: Array<string | undefined> = [];
  s.handle.turns = async function* (after, signal) {
    streams++; cursors.push(after?.value); active++; maxActive = Math.max(maxActive, active);
    try {
      for await (const item of turns(after, signal)) {
        yield item;
        if (item.event.type === 'process_exited') return;
      }
    } finally { active--; }
  };
  s.lane.attach(s.handle);
  await s.emit({ type: 'process_exited', at: new Date(100).toISOString(), code: 0, resumable: true });
  assert.equal(active, 0);
  const wake = s.message('resume after exit');
  await s.lane.handleMessage(wake);
  await s.emit(finished('resumed'));
  assert.equal(s.replies().length, 1);
  assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], [wake.message.message_id]);
  assert.deepEqual(s.receipts(), [{ delivery_id: wake.delivery_id, state: 'processed' }]);
  assert.equal(streams, 2); assert.deepEqual(cursors, [undefined, '1']); assert.equal(maxActive, 1);
  await s.lane.stop(); assert.equal(active, 0);
});

test('launcher queued and held inputs preceding a wake stay out of the current steer', async t => {
  const s = await setup(t, { kind: 'turn_running' });
  const queued = s.message('next turn', 'wake_idle'), hold = s.message('held', 'hold'), wake = s.message('urgent');
  await s.lane.handleMessage(queued); await s.lane.handleMessage(hold); await s.lane.handleMessage(wake);
  assert.equal(s.sends.length, 1); assert.equal(s.sends[0].mode, 'steer');
  assert.deepEqual(s.sends[0].input.items.map(i => i.deliveryId), [wake.delivery_id]);
  await s.emit(finished('current'));
  assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], [wake.message.message_id]);
  assert.equal(s.sends[1].mode, 'start');
  assert.deepEqual(s.sends[1].input.items.map(i => i.deliveryId), [hold.delivery_id, queued.delivery_id]);
});

test('extend delivery commits its clock, dedupe decision and receipt atomically and survives restart', async t => {
  const s = await setup(t), d = s.message('extend'); d.message.metadata['launch.control'] = 'extend';
  // Model a crash before the enclosing ingress completion with a real reopened DB.
  const root = mkdtempSync(join(tmpdir(), 'vl-control-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  await s.lane.stop();
  let ledger = openLedger(join(root, 'ledger.sqlite'), s.clock);
  ledger.createJob(s.job);
  const makeLane = () => {
    const outbox = createOutbox({ ledger, clock: s.clock, log: s.log, hub: { async send() { throw new Error('unused'); }, async receipt() {} } });
    return createLane(ledger.job(s.job.jobId)!, { ...s.deps, ledger, outbox, jobs: createJobReporter({ ledger, outbox }) });
  };
  let lane = makeLane();
  const tx = ledger.tx.bind(ledger);
  let atomic = false;
  ledger.tx = fn => tx(() => {
    const result = fn();
    if (ledger.job(s.job.jobId)!.lastActivityAt === 125) {
      atomic = ledger.kv(`lane:${s.job.jobId}`).keys('control:').length === 1 &&
        ledger.pendingOutbox(100, s.clock.now()).some(r => r.key === `vl.rc.${d.delivery_id}.processed`);
    }
    return result;
  });
  s.clock.advance(25); await lane.handleMessage(d); assert.equal(atomic, true);
  await lane.stop(); ledger.close();
  ledger = openLedger(join(root, 'ledger.sqlite'), s.clock); lane = makeLane();
  try {
    s.clock.advance(100); await lane.handleMessage(d);
    assert.equal(ledger.job(s.job.jobId)!.lastActivityAt, 125);
    assert.equal(ledger.pendingOutbox(100, s.clock.now()).filter(r => r.kind === 'receipt').length, 1);
  } finally { await lane.stop(); ledger.close(); }
});

test('interrupt delivery replay cannot interrupt a later turn, even after a lane restart', async t => {
  const s = await setup(t, { kind: 'turn_running' }), d = s.message('interrupt');
  d.message.metadata['launch.control'] = 'interrupt';
  await s.lane.handleMessage(d); assert.deepEqual(s.calls, ['interrupt']);
  await s.lane.stop();
  const resumed = createLane(s.ledger.job(s.job.jobId)!, s.deps); resumed.attach(s.handle); await tick();
  try {
    await resumed.handleMessage(s.message('later work')); await resumed.handleMessage(d);
    assert.deepEqual(s.calls, ['interrupt']);
    assert.equal(s.receipts().filter(r => r.delivery_id === d.delivery_id).length, 1);
  } finally { await resumed.stop(); }
});

test('settle control replay after the session ends retains its original processed receipt', async t => {
  const s = await setup(t), d = s.message('settle'); d.message.metadata['launch.control'] = 'settle';
  await s.lane.handleMessage(d); await s.lane.handleMessage(d);
  assert.deepEqual(s.calls, ['settle:requested']);
  assert.deepEqual(s.receipts(), [{ delivery_id: d.delivery_id, state: 'processed' }]);
});


test('an uncertain interrupt retry is fenced to its original backend turn identity', async t => {
  const s = await setup(t, { kind: 'turn_running' }), d = s.message('interrupt original');
  d.message.metadata['launch.control'] = 'interrupt';
  let turnId = 'original';
  s.handle.state = () => ({ kind: 'turn_running', turnId, since: new Date(100).toISOString() });
  s.handle.interrupt = async () => { s.calls.push(`interrupt:${turnId}`); throw new Error('crash after interrupt'); };
  await assert.rejects(s.lane.handleMessage(d), /session lane operation failed/);
  await s.lane.stop();
  turnId = 'later';
  const resumed = createLane(s.ledger.job(s.job.jobId)!, s.deps); resumed.attach(s.handle); await tick();
  try {
    await resumed.handleMessage(d);
    assert.deepEqual(s.calls, ['interrupt:original']);
    assert.deepEqual(s.receipts(), [{ delivery_id: d.delivery_id, state: 'processed' }]);
  } finally { await resumed.stop(); }
});

test('a wake racing the exited iterator closing starts only one replacement pump', async t => {
  const s = await setup(t, { attach: false });
  const turns = s.handle.turns;
  let closing!: () => void;
  const closed = new Promise<void>(resolve => { closing = resolve; });
  let streams = 0, active = 0, maxActive = 0;
  s.handle.turns = async function* (after, signal) {
    streams++; active++; maxActive = Math.max(maxActive, active);
    try {
      for await (const item of turns(after, signal)) {
        yield item;
        if (item.event.type === 'process_exited') { await closed; return; }
      }
    } finally { active--; }
  };
  s.lane.attach(s.handle);
  await s.emit({ type: 'process_exited', at: new Date(100).toISOString(), code: 0, resumable: true });
  const wake = s.message('wake while old iterator closes');
  await s.lane.handleMessage(wake); assert.equal(streams, 1);
  closing(); await tick();
  await s.emit(finished('raced-resume'));
  assert.equal(s.replies().length, 1); assert.equal(streams, 2); assert.equal(maxActive, 1);
  await s.lane.stop(); assert.equal(active, 0);
});


async function nativeSession(t: TestContext, overrides: Partial<BackendCapabilities> = {}) {
  const s = await setup(t, { attach: false, kind: 'turn_running', caps: { queue: 'native', ...overrides } });
  const backend = createFakeBackend({ clock: s.clock, caps: { queue: 'native', ...overrides } });
  const handle = await backend.launch({
    jobId: s.job.jobId, tag: s.job.tag, title: s.job.title, prompt: 'initial',
    project: { alias: 'repo', repoRoot: '/repo', baseRef: 'main' }, branch: 'launch/job_one',
    preset: 'test', permissionMode: 'ask', attachments: [], env: {},
  }, { onEvent() {}, async onQuestion() { throw new Error('unused'); }, async onPermissionRequest() { throw new Error('unused'); } });
  s.lane.attach(handle); await tick();
  return { ...s, backend, nativeHandle: handle };
}

// Preserve coverage for native batches already persisted by earlier lane versions.
async function seedNativeBatch(s: Awaited<ReturnType<typeof nativeSession>>, d: ReturnType<typeof s.message>, targetTurnSeq: number) {
  await s.lane.handleMessage(d);
  const row = s.ledger.inputs(s.job.jobId,['buffered']).find(r=>r.deliveryId===d.delivery_id)!;
  const batchId = createHash('sha256').update(d.delivery_id).digest('hex').slice(0,24);
  const input = { batchId, items: [{deliveryId:row.deliveryId,messageId:row.messageId,from:row.sender,text:row.text,attachments:[]}] };
  s.ledger.kv(`lane:${s.job.jobId}`).set(`batch:${batchId}`,JSON.stringify({input,parts:{},targetTurnSeq}));
  s.ledger.markInputs([row.deliveryId],{state:'sending',mode:'queue',batchId,turnSeq:0});
  await s.nativeHandle.send(input,'queue');
  s.ledger.markInputs([row.deliveryId],{state:'sent'});
}

test('restart steer targets the active native successor before unread history replays', async t => {
  const s = await nativeSession(t), queued = s.message('next turn', 'wake_idle');
  await seedNativeBatch(s,queued,2); await s.lane.stop();
  s.backend.finishTurn(s.job.jobId, 'offline first turn');
  assert.equal(s.nativeHandle.state().kind, 'turn_running');
  const resumed = createLane(s.ledger.job(s.job.jobId)!, s.deps), urgent = s.message('urgent successor steer');
  try {
    resumed.attach(s.nativeHandle);
    // Deliberately enqueue ingress before the pump can reconcile its first event.
    await resumed.handleMessage(urgent); await tick();
    assert.equal(s.backend.sessions()[0].sent.at(-1)!.mode, 'steer');
    assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], []);
    assert.ok(!s.receipts().some(r => r.delivery_id === urgent.delivery_id));
    s.backend.finishTurn(s.job.jobId, 'successor done'); await tick();
    assert.deepEqual(s.replies()[1].metadata!['launch.consumed'], [queued.message.message_id, urgent.message.message_id]);
    assert.equal(s.receipts().filter(r => r.delivery_id === urgent.delivery_id && r.state === 'processed').length, 1);
  } finally { await resumed.stop(); }
});

test('persisted separate native queue batches receive receipts only from their own finished turn across restart', async t => {
  const s = await nativeSession(t), q1 = s.message('queue one', 'wake_idle'), q2 = s.message('queue two', 'wake_idle');
  await seedNativeBatch(s,q1,2); await seedNativeBatch(s,q2,3);
  assert.equal(s.backend.sessions()[0].sent.length, 2);
  await s.lane.stop();
  const resumed = createLane(s.ledger.job(s.job.jobId)!, s.deps); resumed.attach(s.nativeHandle); await tick();
  try {
    s.backend.finishTurn(s.job.jobId, 'initial'); await tick();
    assert.equal(s.receipts().length, 0);
    s.backend.finishTurn(s.job.jobId, 'queue one done'); await tick();
    assert.deepEqual(s.replies()[1].metadata!['launch.consumed'], [q1.message.message_id]);
    assert.deepEqual(s.receipts(), [{ delivery_id: q1.delivery_id, state: 'processed' }]);
    assert.equal(s.nativeHandle.state().kind, 'turn_running');
    s.backend.finishTurn(s.job.jobId, 'queue two done'); await tick();
    assert.deepEqual(s.replies()[2].metadata!['launch.consumed'], [q2.message.message_id]);
    assert.equal(s.receipts().filter(r => r.delivery_id === q2.delivery_id && r.state === 'processed').length, 1);
  } finally { await resumed.stop(); }
});

test('hold precedes a native wake_idle batch and both are processed by the next turn', async t => {
  const s = await nativeSession(t), hold = s.message('held', 'hold'), queued = s.message('wake next', 'wake_idle');
  await s.lane.handleMessage(hold);
  assert.equal(s.backend.sessions()[0].sent.length, 0);
  await s.lane.handleMessage(queued);
  assert.equal(s.backend.sessions()[0].sent.length, 0);
  s.backend.finishTurn(s.job.jobId, 'initial'); await tick();
  assert.deepEqual(s.backend.sessions()[0].sent[0].items.map(i => i.deliveryId), [hold.delivery_id, queued.delivery_id]);
  assert.equal(s.receipts().length, 0);
  s.backend.finishTurn(s.job.jobId, 'next'); await tick();
  assert.deepEqual(s.replies()[1].metadata!['launch.consumed'], [hold.message.message_id, queued.message.message_id]);
  assert.deepEqual(s.receipts().map(r => r.delivery_id), [hold.delivery_id, queued.delivery_id]);
});

test('ordinary delivery retry after consumption and settle preserves its receipt with reopened queued ingress', async t => {
  const s = await setup(t, { attach: false }), d = s.message('ordinary input');
  const root = mkdtempSync(join(tmpdir(), 'vl-input-retry-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  let ledger = openLedger(join(root, 'ledger.sqlite'), s.clock);
  ledger.createJob(s.job);
  ledger.admit({ deliveryId: d.delivery_id, cursor: 'ingress-1', threadId: s.job.threadId,
    messageId: d.message.message_id, sender: d.message.sender, delivery: d, receivedAt: s.clock.now() });
  const makeLane = () => {
    const outbox = createOutbox({ ledger, clock: s.clock, log: s.log, hub: { async send() { throw new Error('unused'); }, async receipt() {} } });
    return createLane(ledger.job(s.job.jobId)!, { ...s.deps, ledger, outbox, jobs: createJobReporter({ ledger, outbox }) });
  };
  let lane = makeLane(); lane.attach(s.handle); await tick();
  try {
    await lane.handleMessage(d); await s.emit(finished('ordinary-done'));
    await lane.settle('requested', 'settled');
    assert.equal(await lane.handleMessage(d), 'relayed');
    await lane.stop(); ledger.close();
    ledger = openLedger(join(root, 'ledger.sqlite'), s.clock); lane = makeLane();
    assert.equal(ledger.queued(10).length, 1);
    assert.equal(await lane.handleMessage(ledger.queued(10)[0].delivery), 'relayed');
    assert.deepEqual(ledger.pendingOutbox(100, s.clock.now()).filter(r => r.kind === 'receipt').map(r => r.payload),
      [{ delivery_id: d.delivery_id, state: 'processed' }]);
    const fresh = s.message('fresh late input');
    assert.equal(await lane.handleMessage(fresh), 'refused');
    assert.deepEqual(ledger.pendingOutbox(100, s.clock.now()).filter(r => r.kind === 'receipt').at(-1)!.payload,
      { delivery_id: fresh.delivery_id, state: 'failed', note: 'session_ended' });
  } finally { await lane.stop(); ledger.close(); }
});


test('attach retries a rejected durable resend before replaying completions or dispatching fresh input', async t => {
  const s = await setup(t), original = s.message('durable original');
  const send = s.handle.send;
  s.handle.send = async (input, mode) => { await send(input, mode); throw new Error('crash after backend accepted'); };
  await assert.rejects(s.lane.handleMessage(original));
  await s.lane.stop();
  await s.emit(finished('original-offline'));
  const attempted: Array<{input: TurnInput; mode: SendMode}> = [];
  let streams = 0;
  const turns = s.handle.turns;
  s.handle.turns = async function* (after, signal) { streams++; yield* turns(after, signal); };
  s.handle.send = async (input, mode) => {
    attempted.push({ input: structuredClone(input), mode });
    if (attempted.length === 1) throw new Error('backend temporarily unavailable');
    if (input.items[0].deliveryId === original.delivery_id) return { mode }; // idempotent backend replay
    return send(input, mode);
  };
  const resumed = createLane(s.ledger.job(s.job.jobId)!, s.deps);
  try {
    resumed.attach(s.handle); await tick();
    assert.equal(attempted.length, 1);
    assert.equal(await resumed.handleMessage(original), 'relayed');
    const fresh = s.message('future input');
    assert.equal(await resumed.handleMessage(fresh), 'buffered');
    assert.equal(attempted.length, 1); assert.equal(streams, 0);
    s.clock.advance(1000); await tick();
    assert.equal(attempted.length, 3);
    assert.deepEqual(attempted[1], attempted[0]);
    assert.equal((attempted[0].input as TurnInput & {batchId?: string}).batchId,
      createHash('sha256').update(original.delivery_id).digest('hex').slice(0,24));
    assert.equal(streams, 1);
    assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], [original.message.message_id]);
    assert.deepEqual(s.receipts(), [{delivery_id:original.delivery_id,state:'processed'}]);
    assert.equal(s.ledger.inputs(s.job.jobId,['sending']).length, 0);
    await s.emit(finished('fresh-finished'));
    assert.deepEqual(s.replies()[1].metadata!['launch.consumed'], [fresh.message.message_id]);
    assert.equal(s.receipts().length, 2);
    await resumed.handleMessage(original);
    s.clock.advance(1000); await tick();
    assert.equal(attempted.length, 3); assert.equal(s.replies().length, 2);
  } finally { await resumed.stop(); }
});

test('a native wake_idle followed by hold includes the hold ahead of the wake in the next turn', async t => {
  const s = await nativeSession(t), queued = s.message('wake next','wake_idle'), hold = s.message('later hold','hold');
  await s.lane.handleMessage(queued); await s.lane.handleMessage(hold);
  s.backend.finishTurn(s.job.jobId,'initial'); await tick();
  assert.equal(s.receipts().length,0);
  assert.deepEqual(s.backend.sessions()[0].sent[0].items.map(i=>i.deliveryId),[hold.delivery_id,queued.delivery_id]);
  s.backend.finishTurn(s.job.jobId,'next'); await tick();
  assert.deepEqual([...(s.replies()[1].metadata!['launch.consumed'] as string[])].sort(),[hold.message.message_id,queued.message.message_id].sort());
  assert.deepEqual(s.receipts().map(r=>r.delivery_id).sort(),[hold.delivery_id,queued.delivery_id].sort());
});


test('native next-turn input stays amendable across restart and splits at the byte cap', async t => {
  const s = await nativeSession(t,{maxInputBytes:400});
  const q1=s.message('a'.repeat(60),'wake_idle'),q2=s.message('b'.repeat(160),'wake_idle'),hold=s.message('held','hold');
  await s.lane.handleMessage(q1);await s.lane.handleMessage(q2);await s.lane.handleMessage(hold);
  assert.equal(s.backend.sessions()[0].sent.length,0);
  await s.lane.stop();
  const resumed=createLane(s.ledger.job(s.job.jobId)!,s.deps);
  try {
    resumed.attach(s.nativeHandle);await tick();
    s.backend.finishTurn(s.job.jobId,'initial');await tick();
    assert.deepEqual(s.backend.sessions()[0].sent[0].items.map(i=>i.deliveryId),[hold.delivery_id,q1.delivery_id]);
    assert.equal(s.receipts().length,0);
    s.backend.finishTurn(s.job.jobId,'first batch');await tick();
    assert.deepEqual(s.backend.sessions()[0].sent[1].items.map(i=>i.deliveryId),[q2.delivery_id]);
    assert.ok(!s.receipts().some(r=>r.delivery_id===q2.delivery_id));
    s.backend.finishTurn(s.job.jobId,'overflow');await tick();
    assert.equal(s.receipts().filter(r=>r.state==='processed').length,3);
    assert.ok(s.backend.sessions()[0].sent.every(batch=>Buffer.byteLength(batch.items.map(i=>i.text).join('\n\n'))<=400));
    await resumed.handleMessage(s.message('hold alone','hold'));await tick();
    assert.equal(s.backend.sessions()[0].sent.length,2);
  } finally {await resumed.stop();}
});

test('stopping a lane cancels its pending attach retry', async t => {
  const s=await setup(t,{attach:false}),d=s.message('pending');
  await s.lane.handleMessage(d);
  const batchId=createHash('sha256').update(d.delivery_id).digest('hex').slice(0,24);
  s.ledger.markInputs([d.delivery_id],{state:'sending',mode:'start',batchId,turnSeq:0});
  let attempts=0;
  s.handle.send=async()=>{attempts++;throw new Error('unreachable');};
  s.lane.attach(s.handle);await tick();assert.equal(attempts,1);
  s.clock.advance(1000);await tick();assert.equal(attempts,2);
  await s.lane.stop();s.clock.advance(5000);await tick();assert.equal(attempts,2);
  assert.equal(s.ledger.job(s.job.jobId)!.state,'running');
  assert.equal(s.ledger.inputs(s.job.jobId,['sending']).length,1);
});

const batchOf = (...ids: string[]) => createHash('sha256').update([...ids].sort().join('\n')).digest('hex').slice(0,24);

test('an attach flush accepted remotely but rejected locally is reconciled before its completion commits', async t => {
  const s = await setup(t, { attach: false }), d = s.message('initial input');
  assert.equal(await s.lane.handleMessage(d), 'buffered');
  const send = s.handle.send, attempted: Array<{input: TurnInput; mode: SendMode}> = [];
  s.handle.send = async (input, mode) => {
    attempted.push({ input: structuredClone(input), mode });
    if (attempted.length === 1) { await send(input, mode); throw new Error('lost ack'); }
    return { mode }; // idempotent replay of an accepted batch
  };
  s.lane.attach(s.handle); await tick();
  assert.equal(s.sends.length, 1);
  // The completion arrives before the Clock retry; it must wait for the batch.
  await s.emit(finished('accepted-turn'));
  assert.equal(s.replies().length, 0); assert.equal(s.receipts().length, 0);
  s.clock.advance(1000); await tick();
  assert.equal(attempted.length, 2); assert.deepEqual(attempted[1], attempted[0]);
  assert.equal(attempted[0].mode, 'start');
  assert.equal((attempted[0].input as TurnInput & {batchId?: string}).batchId, batchOf(d.delivery_id));
  assert.equal(s.replies().length, 1);
  assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], [d.message.message_id]);
  assert.deepEqual(s.receipts(), [{ delivery_id: d.delivery_id, state: 'processed' }]);
  assert.equal(s.ledger.inputs(s.job.jobId, ['sending', 'sent']).length, 0);
});

test('a rejected ordinary send recovers through the Clock without recreating the lane', async t => {
  const s = await setup(t), d = s.message('retry me');
  const send = s.handle.send, attempted: Array<{input: TurnInput; mode: SendMode}> = [];
  s.handle.send = async (input, mode) => {
    attempted.push({ input: structuredClone(input), mode });
    if (attempted.length === 1) throw new Error('offline before acceptance');
    return send(input, mode);
  };
  await assert.rejects(s.lane.handleMessage(d));
  assert.equal(s.ledger.inputs(s.job.jobId, ['sending']).length, 1);
  s.clock.advance(1000); await tick();
  assert.equal(attempted.length, 2); assert.deepEqual(attempted[1], attempted[0]);
  assert.equal((attempted[1].input as TurnInput & {batchId?: string}).batchId, batchOf(d.delivery_id));
  assert.equal(s.sends.length, 1); assert.equal(s.ledger.inputs(s.job.jobId, ['sent']).length, 1);
  // The ingress retry finds the durable decision and sends nothing new.
  assert.equal(await s.lane.handleMessage(d), 'relayed'); assert.equal(attempted.length, 2);
  await s.emit(finished('recovered'));
  assert.equal(s.replies().length, 1);
  assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], [d.message.message_id]);
  assert.deepEqual(s.receipts(), [{ delivery_id: d.delivery_id, state: 'processed' }]);
  s.clock.advance(60_000); await tick(); assert.equal(attempted.length, 2);
});

test('a rejected flush after a finished turn recovers through the Clock in order', async t => {
  const s = await setup(t, { kind: 'turn_running' }), hold = s.message('held', 'hold'), queued = s.message('next turn', 'wake_idle');
  await s.lane.handleMessage(hold); await s.lane.handleMessage(queued);
  const send = s.handle.send, attempted: Array<{input: TurnInput; mode: SendMode}> = [];
  s.handle.send = async (input, mode) => {
    attempted.push({ input: structuredClone(input), mode });
    if (attempted.length <= 2) throw new Error('offline');
    return send(input, mode);
  };
  await s.emit(finished('current'));
  assert.equal(attempted.length, 1); assert.equal(s.ledger.inputs(s.job.jobId, ['sending']).length, 2);
  s.clock.advance(1000); await tick(); assert.equal(attempted.length, 2);
  s.clock.advance(1000); await tick(); assert.equal(attempted.length, 2); // backoff doubles
  s.clock.advance(1000); await tick(); assert.equal(attempted.length, 3);
  assert.ok(attempted.every(a => JSON.stringify(a) === JSON.stringify(attempted[0])));
  assert.deepEqual(s.sends[0].input.items.map(i => i.deliveryId), [hold.delivery_id, queued.delivery_id]);
  assert.equal(s.sends[0].mode, 'start');
  await s.emit(finished('next'));
  assert.deepEqual(s.replies()[1].metadata!['launch.consumed'], [hold.message.message_id, queued.message.message_id]);
  assert.deepEqual(s.receipts().map(r => r.delivery_id), [hold.delivery_id, queued.delivery_id]);
});

test('a transient turns() failure retries on the Clock from the committed cursor with one iterator', async t => {
  const s = await setup(t, { attach: false });
  s.ledger.addInput({ deliveryId: 'dly_unread', jobId: s.job.jobId, messageId: 'msg_unread', sender: 'req', text: 'before', priority: 'wake', createdAt: 100 });
  s.ledger.markInputs(['dly_unread'], { state: 'sent', mode: 'start', turnSeq: 0 });
  const turns = s.handle.turns, cursors: Array<string | undefined> = [];
  let streams = 0, active = 0, maxActive = 0;
  s.handle.turns = async function* (after, signal) {
    const n = ++streams; cursors.push(after?.value); active++; maxActive = Math.max(maxActive, active);
    try {
      if (n === 1) throw new Error('offline');
      for await (const item of turns(after, signal)) { yield item; throw new Error('stream reset'); }
    } finally { active--; }
  };
  s.lane.attach(s.handle); await tick();
  assert.equal(streams, 1);
  await s.emit(finished('unread'));
  assert.equal(s.replies().length, 0);
  s.clock.advance(1000); await tick();
  assert.equal(streams, 2);
  assert.deepEqual(s.replies()[0].metadata!['launch.consumed'], ['msg_unread']);
  assert.deepEqual(s.receipts(), [{ delivery_id: 'dly_unread', state: 'processed' }]);
  await s.emit(finished('second'));
  s.clock.advance(1000); await tick();
  assert.equal(streams, 3); assert.deepEqual(cursors, [undefined, undefined, '1']);
  assert.equal(s.replies().length, 2); assert.equal(s.receipts().length, 1); assert.equal(maxActive, 1);
  // The third iterator failed too; stopping the lane cancels its retry.
  await s.lane.stop(); s.clock.advance(60_000); await tick();
  assert.equal(streams, 3); assert.equal(active, 0);
});
test('a backend input_too_large refusal fails just that input, splits its batch and never retries forever',async t=>{
  const s=await setup(t,{kind:'turn_running'});
  const send=s.handle.send.bind(s.handle);
  s.handle.send=async(input,mode)=>{ if(input.items.some(i=>i.text.includes('HUGE'))) throw new Error('input_too_large'); return send(input,mode); };
  const huge=s.message('HUGE \u0001'.repeat(10),'wake_idle'),small=s.message('small','wake_idle');
  await s.lane.handleMessage(huge);await s.lane.handleMessage(small);
  await s.emit(finished());
  assert.deepEqual(s.sends.map(x=>x.input.items.map(i=>i.deliveryId)),[[small.delivery_id]]);
  assert.deepEqual(s.receipts().filter(r=>r.delivery_id===huge.delivery_id),[{delivery_id:huge.delivery_id,state:'failed',note:'input_too_large'}]);
  assert.equal(s.ledger.inputs('job_one',['sending']).length,0);
  // The completion of the small input's turn is not fenced, and nothing retries the refused input.
  await s.emit(finished('turn-2'));
  assert.ok(s.receipts().some(r=>r.delivery_id===small.delivery_id && r.state==='processed'));
  s.clock.advance(120_000);await tick();
  assert.equal(s.sends.length,1);
  // A later lone input still reaches the backend.
  const next=s.message('later');await s.lane.handleMessage(next);
  assert.deepEqual(s.sends.at(-1)!.input.items.map(i=>i.deliveryId),[next.delivery_id]);
});
