import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
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
  assert.equal(s.sends.length,1);assert.deepEqual(s.sends[0].input.items.map(i=>i.deliveryId),ids);assert.deepEqual(s.sends[0].batchIds,[batchId,batchId]);assert.equal(s.ledger.inputs('job_one',['sent']).length,2);
});
test('spilled inbound text is rehydrated before relay',async t=>{
  const s=await setup(t),d=s.message('preview');d.message.content.truncated=true;d.message.metadata['vecnet.spilled']=true;d.message.attachments=[{name:'message.md',sha256:'b'.repeat(64),size:40,media_type:'text/markdown',blob_url:'https://hub/blob?t=secret'}];await s.lane.handleMessage(d);assert.deepEqual(s.fetched,['https://hub/blob?t=secret']);assert.match(s.sends[0].input.items[0].text,/> full spilled text/);assert.ok(!s.sends[0].input.items[0].text.includes('preview'));
});
test('merged inputs over the cap split: the overflow is queued for the next turn; caps.maxInputBytes lowers the cap',async t=>{
  for(const limit of [96*1024,400]){const s=await setup(t,{attach:false,caps:{maxInputBytes:limit}});const ds=[s.message('a'.repeat(Math.floor(limit*.55))),s.message('b'.repeat(Math.floor(limit*.55)))];for(const d of ds)await s.lane.handleMessage(d);s.lane.attach(s.handle);await tick();assert.equal(s.sends.length,1);assert.ok(s.sends[0].input.items.reduce((n,i)=>n+Buffer.byteLength(i.text),0)<=limit);assert.equal(s.ledger.inputs('job_one',['buffered']).length,1);await s.emit(finished());assert.equal(s.sends.length,2);assert.deepEqual(s.sends.map(send=>send.input.items[0].deliveryId),ds.map(d=>d.delivery_id));}
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
  const original=s.handle.send;let attempted:TurnInput|undefined;
  s.handle.send=async input=>{attempted=structuredClone(input);throw new Error('crash before send');};
  await s.emit(finished());assert.ok(attempted);assert.equal(s.ledger.inputs('job_one',['sending']).length,2);
  const batch=s.ledger.inputs('job_one',['sending'])[0].batchId;
  await s.lane.stop();s.handle.send=original;
  const resumed=createLane(s.ledger.job('job_one')!,s.deps);resumed.attach(s.handle);await tick();
  assert.deepEqual(s.sends[0].input,attempted);assert.deepEqual(s.sends[0].input.items.map(i=>i.deliveryId),[hold.delivery_id,queued.delivery_id]);assert.deepEqual(s.sends[0].batchIds,[batch,batch]);await resumed.stop();
});
test('native queued input belongs to the following turn; hold alone never starts; controls and stop retain their boundaries',async t=>{
  const s=await setup(t,{kind:'turn_running',caps:{queue:'native',steer:false}}),q=s.message('queued','wake_idle');
  await s.lane.handleMessage(q);assert.equal(s.sends[0].mode,'queue');await s.emit(finished('initial'));assert.equal(s.receipts().length,0);
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
