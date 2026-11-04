import test from 'node:test';
import assert from 'node:assert/strict';
import type { TestContext } from 'node:test';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { SendRequest, Delivery } from '../../hub-client/src/wire.ts';
import { newUlid } from '../../hub-client/src/ids.ts';
import type { AgentQuestion, PermissionRequest } from '../src/backend.ts';
import { openLedger } from '../src/ledger.ts';
import type { Directory } from '../src/directory.ts';
import type { Outbox } from '../src/outbox.ts';
import { parseConfig } from '../src/config.ts';
import { routeApproval } from '../src/policy.ts';
import { finishPermission, validateLaunchPermission } from '../src/contract/permission.ts';
import { parseQuestion, parseApprovalClosed } from '../src/contract/metadata.ts';
import { createParentBridge } from '../src/parent-bridge.ts';
import { threadDelivery } from './fixtures.ts';

const timeoutAnswer = 'no answer from requester; use your best judgement and say what you assumed';
const secretAnswer = 'secrets cannot be provided through vecnet';
const signal = () => new AbortController().signal;
const question = (requestId = 'q1'): AgentQuestion => ({ requestId, blocking:true, questions:[
  {id:'color',text:'Choose a color',options:[{label:'Blue'},{label:'Green'}],allowOther:false,secret:false},
] });
function permission(requestId = 'p1'): PermissionRequest {
  const options: PermissionRequest['options'] = [{id:'allow-once',label:'Allow once',grants:'once'},{id:'deny',label:'Deny',grants:'deny'}];
  return {requestId,summary:'Write a file',options,defaultOnExpiry:'deny',permission:{kind:'edit',backend_method:'file/edit',scope:'once',options,
    paths:[{path:'file',access:'write'}],fidelity:{kind:'enforced',command:'none',paths:'enforced',host:'none'},backend_raw:{}}};
}
function setup(t: TestContext, config: Record<string, unknown> = {}) {
  const clock = fakeClock(1000), log = memoryLog(), ledger = openLedger(':memory:',clock);
  t.after(() => ledger.close());
  const sent: Parameters<Outbox['onSent']>[0][] = [];
  const rejected: Parameters<Outbox['onRejected']>[0][] = [];
  const outbox: Outbox = { enqueue:(...args) => { ledger.enqueue(...args); },kick(){},async run(){},onSent(fn){sent.push(fn);},onRejected(fn){rejected.push(fn);} };
  const cfg = parseConfig({host:'test-machine',hub:{url:'http://localhost',credential_file:'/fake'},state_dir:'/fake',backends:[],projects:{},...config});
  const dir: Directory = {operatorPeerId:()=> 'peer_operator',isOperator:(id:string)=>id==='peer_operator',name:(id:string)=>id,createdBy:()=>undefined,
    get:()=>undefined,kindOf:()=>undefined,async refresh(){},async ensureKnown(){return false;},async onDirectoryUpdated(){},onRemoved(){}};
  ledger.createJob({jobId:'job_00000000000000000000000000',threadId:'thr_job',launchMessageId:'msg_launch',requester:'peer_parent',policyKey:'default',backend:'fake',preset:'test',permissionMode:'ask',project:'repo',title:'A job',input:{backend:'fake',project:'repo',preset:'test',permission_mode:'ask',relay_from:['peer_relay']},tag:'vl-test',state:'running',sessionRef:null,sessionKind:'idle',turnCursor:null,branch:null,workspace:'/workspace',lastActivityAt:clock.now(),parkedAt:null,deadlineAt:999999999,createdAt:clock.now()});
  const job = ledger.openJobs()[0];
  const bridge = createParentBridge({ledger,outbox,clock,log,cfg,self:'peer_launcher',home:'/home/test',dir,route:(p,j)=>routeApproval(p,j,dir,cfg,'/home/test')});
  const events: unknown[] = [];
  const hooks = bridge.hooksFor(job,'/workspace',ev=>events.push(ev));
  const rows = () => ledger.pendingOutbox(1000,clock.now());
  const sends = () => rows().filter(r=>r.kind==='send').map(r=>r.payload as SendRequest);
  const prompts = () => sends().filter(r=>r.kind==='approval_request');
  function deliverSend(req: SendRequest, thread = req.thread_id ?? 'thr_operator') {
    const row = rows().find(r=>r.key===req.idempotency_key)!;
    assert.ok(row,'send was enqueued');
    const result = {message_id:`msg_${newUlid(clock.now())}`,thread_id:thread,deliveries:[],spilled:false};
    ledger.markOutbox(row.id,'sent',result);
    for (const fn of sent) fn(row,result);
    return result;
  }
  function reply(req: SendRequest, text: string, sender = 'peer_parent', meta: Record<string,unknown> = {}) {
    const result = deliverSend(req);
    const d = threadDelivery(result.thread_id,sender,text,'wake');
    d.message.in_reply_to = result.message_id; d.message.metadata = meta;
    return d;
  }
  function response(req: SendRequest, decision: string, sender = 'peer_parent', meta: Record<string,unknown> = {}): Delivery {
    const d = threadDelivery(req.thread_id ?? 'thr_operator',sender,'','wake');
    d.message.kind='approval_response';d.message.approval_response={approval_id:req.approval_request!.approval_id,decision};d.message.metadata=meta;
    return d;
  }
  const receipt = (d:Delivery) => rows().find(r=>r.key===`vl.rc.${d.delivery_id}.processed`)?.payload;
  return {clock,ledger,cfg,bridge,hooks,job,rows,sends,prompts,deliverSend,reply,response,receipt,events,log,rejected};
}

test('structured question → launch.question to requester in_reply_to the launch message; text reply matching an option label resolves; reply consumed, not relayed',async t=>{
  const s=setup(t), pending=s.hooks.onQuestion(question(),signal()), req=s.sends()[0];
  assert.equal(req.kind,'message');assert.equal(req.priority,'wake');assert.deepEqual(req.recipients,['peer_parent']);assert.equal(req.in_reply_to,'msg_launch');
  assert.deepEqual(parseQuestion(req.metadata)?.questions,[{id:'color',text:'Choose a color',options:['Blue','Green'],allow_other:false}]);
  assert.match(req.content.text,/Choose a color/);assert.match(req.content.text,/Blue/);assert.equal(s.bridge.open(s.job.jobId).questions,1);
  const d=s.reply(req,'bLuE');assert.equal(await s.bridge.handleThreadReply(d),'consumed');
  assert.deepEqual(await pending,{answers:{color:'Blue'},answeredBy:'peer_parent'});assert.deepEqual(s.receipt(d),{delivery_id:d.delivery_id,state:'processed'});
  assert.equal(s.bridge.open(s.job.jobId).questions,0);assert.deepEqual(s.ledger.inputs(s.job.jobId,['buffered','sent']),[]);
  const ev={type:'state' as const,state:{kind:'idle' as const,since:'now'}};s.hooks.onEvent(ev);assert.deepEqual(s.events,[ev]);
});
test('the same question asked twice gets two distinct keys (from requestId)',async t=>{
  const s=setup(t);const a=s.hooks.onQuestion(question('first'),signal()),b=s.hooks.onQuestion(question('second'),signal());
  const reqs=s.sends();assert.equal(reqs.length,2);assert.notEqual(reqs[0].idempotency_key,reqs[1].idempotency_key);
  assert.ok(reqs.every(r=>new RegExp(`^vl.q.${s.job.jobId}.[a-f0-9]{24}$`).test(r.idempotency_key)));
  s.bridge.abandonAll(s.job.jobId,'session_ended');await Promise.all([a,b]);
});
test('launch.answers with several questions resolves all; malformed falls back to text',async t=>{
  const s=setup(t), q=question();q.questions.push({id:'detail',text:'Detail?',allowOther:true,secret:false});
  const pending=s.hooks.onQuestion(q,signal()),req=s.sends()[0],qid=parseQuestion(req.metadata)!.question_id;
  const d=s.reply(req,'ignored','peer_relay',{'launch.answers':{question_id:qid,answers:{color:'green',detail:'Use patches'}}});
  assert.equal(await s.bridge.handleThreadReply(d),'consumed');assert.deepEqual(await pending,{answers:{color:'Green',detail:'Use patches'},answeredBy:'peer_relay'});
  const next=s.hooks.onQuestion(question('q2'),signal()),req2=s.sends().find(r=>r.metadata?.['launch.question'])!;
  await s.bridge.handleThreadReply(s.reply(req2,'Green','peer_operator',{'launch.answers':{question_id:'wrong',answers:[]}}));
  assert.deepEqual(await next,{answers:{color:'Green'},answeredBy:'peer_operator'});
});
test('reply from a non-relayed peer does not answer',async t=>{
  const s=setup(t),pending=s.hooks.onQuestion(question(),signal()),d=s.reply(s.sends()[0],'Blue','peer_intruder');
  assert.equal(await s.bridge.handleThreadReply(d),'consumed');assert.equal(s.bridge.open(s.job.jobId).questions,1);
  assert.deepEqual(s.receipt(d),{delivery_id:d.delivery_id,state:'processed',note:'not_relayed'});
  s.bridge.abandonAll(s.job.jobId,'restart');assert.equal((await pending).answeredBy,'timeout');
});
test('blocking question times out after 30 min with the best-judgement answer and a note',async t=>{
  const s=setup(t),pending=s.hooks.onQuestion(question(),signal());s.clock.advance(1800000-1);assert.equal(s.bridge.open(s.job.jobId).questions,1);
  s.clock.advance(1);assert.deepEqual(await pending,{answers:{color:timeoutAnswer},answeredBy:'timeout'});
  assert.equal(s.ledger.openQuestions().length,0);assert.ok(s.sends().some(r=>!r.metadata?.['launch.question']&&r.content.text.includes(timeoutAnswer)));
});
test('secret question is never posted and is refused at once; question text with a token is redacted',async t=>{
  const s=setup(t),q=question();q.questions[0].secret=true;q.questions[0].text='crsr_secretQuestion';
  assert.deepEqual(await s.hooks.onQuestion(q,signal()),{answers:{color:secretAnswer},answeredBy:'timeout'});
  assert.equal(s.sends().length,1);assert.ok(!s.sends()[0].metadata?.['launch.question']);assert.match(s.sends()[0].content.text,/secrets cannot/);
  const normal=question('normal');normal.questions[0].text='Choose crsr_tokenText';normal.questions[0].options=[{label:'crsr_optionText'}];normal.questions[0].header='Bearer headerSecret';
  const pending=s.hooks.onQuestion(normal,signal());const req=s.sends().at(-1)!;
  assert.ok(!JSON.stringify(req).includes('crsr_'));assert.ok(!JSON.stringify(req).includes('headerSecret'));assert.match(JSON.stringify(req),/redacted/);
  s.bridge.abandonAll(s.job.jobId,'restart');await pending;
});
test('permission prompt → approval_request to requester with launch.permission that validates, options + escalate, escalate_to_operator true, priority wake',async t=>{
  const s=setup(t),p=permission();p.summary='Write crsr_summaryToken\nnow '+ 'x'.repeat(600);
  const pending=s.hooks.onPermissionRequest(p,signal()),req=s.prompts()[0];
  assert.deepEqual(req.recipients,['peer_parent']);assert.equal(req.in_reply_to,'msg_launch');assert.equal(req.thread_id,'thr_job');assert.equal(req.priority,'wake');
  assert.deepEqual(req.approval_request!.options,['Allow once','Deny','escalate']);assert.equal(req.approval_request!.escalate_to_operator,true);
  assert.equal(Date.parse(req.approval_request!.expires_at),s.clock.now()+900000);assert.deepEqual(validateLaunchPermission(req.metadata!['launch.permission']),[]);
  assert.ok(req.approval_request!.question.length<=512);assert.ok(!req.approval_request!.question.includes('\n'));assert.ok(!JSON.stringify(req).includes('crsr_summaryToken'));
  s.bridge.abandonAll(s.job.jobId,'session_ended');await pending;
});
test('allow once resolves the backend option id; decided_by and latency recorded',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),req=s.prompts()[0];s.deliverSend(req);s.clock.advance(1234);
  const d=s.response(req,'Allow once');assert.equal(await s.bridge.handleApprovalResponse(d),'consumed');
  assert.deepEqual(await pending,{optionId:'allow-once',decidedBy:'peer_parent'});
  const row=s.ledger.approval(req.approval_request!.approval_id)!;assert.equal(row.decidedBy,'peer_parent');assert.equal(row.decidedAt!-row.createdAt,1234);assert.equal(row.decision,'allow-once');assert.equal(row.state,'decided');assert.ok(s.receipt(d));
});
test('escalate → the first operator prompt opens the escalation thread (no thread_id, title, recipients [operator]); onSent records it; a second operator prompt waits and then carries that thread_id',async t=>{
  const s=setup(t),a=s.hooks.onPermissionRequest(permission('one'),signal()),b=s.hooks.onPermissionRequest(permission('two'),signal());
  const parents=s.prompts();s.deliverSend(parents[0]);s.deliverSend(parents[1]);
  await s.bridge.handleApprovalResponse(s.response(parents[0],'escalate'));await s.bridge.handleApprovalResponse(s.response(parents[1],'escalate'));
  assert.equal(s.prompts().length,1);const opener=s.prompts()[0];assert.equal(opener.thread_id,undefined);assert.equal(opener.in_reply_to,undefined);assert.equal(opener.thread_title,'vl escalation: A job');assert.deepEqual(opener.recipients,['peer_operator']);assert.equal(opener.metadata!['launch.job_id'],s.job.jobId);
  s.deliverSend(opener,'thr_escalation');assert.equal(s.ledger.escalationThread(s.job.jobId),'thr_escalation');assert.equal(s.ledger.jobByEscalationThread('thr_escalation')?.jobId,s.job.jobId);
  assert.equal(s.prompts().length,1);assert.equal(s.prompts()[0].thread_id,'thr_escalation');assert.deepEqual(s.prompts()[0].recipients,['peer_operator']);
  s.bridge.abandonAll(s.job.jobId,'session_ended');await Promise.all([a,b]);
});
test('operator decision in the escalation thread resolves with identical launch.permission and launch.escalated_from',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),parent=s.prompts()[0];s.deliverSend(parent);
  await s.bridge.handleApprovalResponse(s.response(parent,'escalate'));const op=s.prompts()[0];
  assert.deepEqual(op.metadata!['launch.permission'],parent.metadata!['launch.permission']);assert.equal(op.metadata!['launch.escalated_from'],parent.approval_request!.approval_id);
  s.deliverSend(op);assert.equal(await s.bridge.handleApprovalResponse(s.response(op,'Allow once','peer_operator')),'consumed');
  assert.deepEqual(await pending,{optionId:'allow-once',decidedBy:'peer_operator'});assert.equal(s.bridge.open(s.job.jobId).approvals,0);
});
test('parent expiry by local timer → deny option; on_parent_expiry escalate → operator prompt instead',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),req=s.prompts()[0];s.clock.advance(900000);
  assert.deepEqual(await pending,{optionId:'deny',decidedBy:'expired'});assert.equal(s.ledger.approval(req.approval_request!.approval_id)?.state,'expired');
  const e=setup(t,{approvals:{on_parent_expiry:'escalate'}}),next=e.hooks.onPermissionRequest(permission(),signal()),parent=e.prompts()[0];e.clock.advance(900000);
  const op=e.prompts().find(r=>r.recipients?.includes('peer_operator'))!;assert.ok(op);assert.equal(op.metadata!['launch.escalated_from'],parent.approval_request!.approval_id);assert.equal(e.bridge.open(e.job.jobId).approvals,1);
  e.bridge.abandonAll(e.job.jobId,'restart');await next;
});
test('operator prompt expiry always denies, even with on_parent_expiry escalate',async t=>{
  const s=setup(t,{approvals:{on_parent_expiry:'escalate'}}),pending=s.hooks.onPermissionRequest(permission(),signal());s.clock.advance(900000);
  const count=s.prompts().length;s.clock.advance(3600000);assert.deepEqual(await pending,{optionId:'deny',decidedBy:'expired'});assert.equal(s.prompts().length,count);assert.equal(s.bridge.open(s.job.jobId).approvals,0);
});
test('hub expired approval_response before the local timer → same deny, once',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),req=s.prompts()[0];s.deliverSend(req);
  await s.bridge.handleApprovalResponse(s.response(req,'expired','hub'));const row=s.ledger.approval(req.approval_request!.approval_id)!;
  assert.deepEqual(await pending,{optionId:'deny',decidedBy:'expired'});s.clock.advance(900000);assert.deepEqual(s.ledger.approval(row.approvalId),row);assert.equal(s.prompts().length,0);
});
test('operator override with launch.decision → resolves, approval_closed notice to parent, later parent answer gets superseded note',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),req=s.prompts()[0],d=s.reply(req,'','peer_operator',{'launch.decision':'Allow once'});
  assert.equal(await s.bridge.handleThreadReply(d),'consumed');assert.deepEqual(await pending,{optionId:'allow-once',decidedBy:'peer_operator'});
  const notice=s.sends().find(r=>r.metadata?.['launch.approval_closed'])!;assert.deepEqual(parseApprovalClosed(notice.metadata),{approval_id:req.approval_request!.approval_id,superseded_by:'operator',decision:'Allow once'});
  assert.deepEqual(notice.recipients,['peer_parent']);assert.equal(notice.in_reply_to,'msg_launch');assert.equal(notice.idempotency_key,`vl.ac.${req.approval_request!.approval_id}`);assert.ok(s.receipt(d));
  const late=s.response(req,'Deny');assert.equal(await s.bridge.handleApprovalResponse(late),'consumed');assert.ok(s.sends().some(r=>r.idempotency_key.includes('.superseded.')&&r.content.text.includes('superseded')));assert.ok(s.receipt(late));
});
test('relay route (pagd): no escalate option, escalate_to_operator false, 35 min expiry; decided_via pag_card recorded',async t=>{
  const s=setup(t,{relays:{pagd:{principals:[],peers:['peer_parent'],parent_expires_ms:2100000,escalate_option:false,escalate_to_operator:false}}}),pending=s.hooks.onPermissionRequest(permission(),signal()),req=s.prompts()[0];
  assert.deepEqual(req.approval_request!.options,['Allow once','Deny']);assert.equal(req.approval_request!.escalate_to_operator,false);assert.equal(Date.parse(req.approval_request!.expires_at)-s.clock.now(),2100000);
  s.deliverSend(req);await s.bridge.handleApprovalResponse(s.response(req,'Allow once','peer_parent',{'launch.decided_via':'pag_card'}));await pending;
  assert.equal(s.ledger.approval(req.approval_request!.approval_id)?.route,'relay');assert.equal(s.ledger.approval(req.approval_request!.approval_id)?.decidedVia,'pag_card');
});
test('direct-to-operator category (privileged preset) skips the parent',async t=>{
  const s=setup(t,{approvals:{to_operator:{presets:['privileged']}}}),p=permission();p.permission.command={line:'sudo true'};
  const pending=s.hooks.onPermissionRequest(p,signal()),req=s.prompts()[0];assert.equal(s.prompts().length,1);assert.deepEqual(req.recipients,['peer_operator']);assert.equal(req.thread_id,undefined);assert.deepEqual(req.approval_request!.options,['Allow once','Deny']);
  s.bridge.abandonAll(s.job.jobId,'session_ended');await pending;
});
test('abandonAll resolves live hooks with deny and marks rows abandoned',async t=>{
  const s=setup(t),a=s.hooks.onPermissionRequest(permission(),signal()),q=s.hooks.onQuestion(question(),signal());
  const ar=s.ledger.openApprovals()[0].approvalId,qr=s.ledger.openQuestions()[0].questionId;
  s.bridge.abandonAll(s.job.jobId,'restart');assert.equal((await a).optionId,'deny');assert.deepEqual((await q).answers,{color:timeoutAnswer});
  assert.equal(s.ledger.approval(ar)?.state,'abandoned');assert.equal(s.ledger.question(qr)?.state,'abandoned');assert.deepEqual(s.bridge.open(s.job.jobId),{approvals:0,questions:0});
  s.clock.advance(9999999);assert.equal(s.ledger.approval(ar)?.state,'abandoned');assert.equal(s.ledger.question(qr)?.state,'abandoned');
});

test('a non-blocking question stays open until abort; free text and duplicate requestId use the same live question',async t=>{
  const s=setup(t),q=question();q.blocking=false;q.questions[0].allowOther=true;
  const controller=new AbortController(),a=s.hooks.onQuestion(q,controller.signal),b=s.hooks.onQuestion(q,signal());
  assert.equal(s.sends().length,1);s.clock.advance(1800000);assert.equal(s.bridge.open(s.job.jobId).questions,1);
  controller.abort();assert.deepEqual(await a,{answers:{color:timeoutAnswer},answeredBy:'timeout'});assert.deepEqual(await b,await a);
  const next=s.hooks.onQuestion({...q,requestId:'free'},signal());
  const prompt=s.sends().filter(r=>r.metadata?.['launch.question']).at(-1)!;
  await s.bridge.handleThreadReply(s.reply(prompt,'Purple'));assert.deepEqual(await next,{answers:{color:'Purple'},answeredBy:'peer_parent'});
});
test('only the authenticated addressee can decide an approval; unknown approvals and other threads are unrelated',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),req=s.prompts()[0];s.deliverSend(req);
  for (const [decision,sender] of [['Allow once','peer_intruder'],['expired','peer_intruder'],['Allow once','peer_relay'],['Allow once','hub']]) {
    const d=s.response(req,decision,sender);assert.equal(await s.bridge.handleApprovalResponse(d),'consumed');assert.equal(s.bridge.open(s.job.jobId).approvals,1);assert.equal((s.receipt(d) as {note:string}).note,'not_relayed');
  }
  const wrongThread=s.response(req,'Allow once');wrongThread.message.thread_id='thr_other';assert.equal(await s.bridge.handleApprovalResponse(wrongThread),'unrelated');
  const unknown=s.response(req,'Allow once');unknown.message.approval_response!.approval_id='apr_unknown';assert.equal(await s.bridge.handleApprovalResponse(unknown),'unrelated');
  s.bridge.abandonAll(s.job.jobId,'session_ended');await pending;
});
test('override of an escalated parent records the chosen backend decision on the operator card too',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),parent=s.prompts()[0],override=s.reply(parent,'','peer_operator',{'launch.decision':'Allow once'});
  await s.bridge.handleApprovalResponse(s.response(parent,'escalate'));const operator=s.prompts()[0];s.deliverSend(operator);
  await s.bridge.handleThreadReply(override);assert.deepEqual(await pending,{optionId:'allow-once',decidedBy:'peer_operator'});
  const child=s.ledger.approval(operator.approval_request!.approval_id)!;assert.equal(child.state,'superseded');assert.equal(child.decision,'allow-once');assert.equal(child.decidedBy,'peer_operator');
  s.clock.advance(3600000);assert.equal(s.ledger.approval(child.approvalId)?.decision,'allow-once');
});
test('direct operator prompts preserve a backend option whose opaque id happens to be escalate',async t=>{
  const s=setup(t,{approvals:{to_operator:{presets:['privileged']}}}),p=permission();p.permission.command={line:'sudo true'};p.options[0].id='escalate';
  const pending=s.hooks.onPermissionRequest(p,signal()),req=s.prompts()[0];
  const published=req.metadata!['launch.permission'] as {options:Array<{id:string}>};assert.deepEqual(published.options.map(o=>o.id),['escalate','deny']);
  s.deliverSend(req);await s.bridge.handleApprovalResponse(s.response(req,'Allow once','peer_operator'));assert.deepEqual(await pending,{optionId:'escalate',decidedBy:'peer_operator'});
});

test('operator launch.decision in the escalation thread closes the original parent card',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),parent=s.prompts()[0];s.deliverSend(parent);
  await s.bridge.handleApprovalResponse(s.response(parent,'escalate'));const operator=s.prompts()[0];
  const d=s.reply(operator,'','peer_operator',{'launch.decision':'Allow once'});
  assert.equal(await s.bridge.handleThreadReply(d),'consumed');assert.deepEqual(await pending,{optionId:'allow-once',decidedBy:'peer_operator'});
  const notice=s.sends().find(r=>r.metadata?.['launch.approval_closed'])!;
  assert.equal(parseApprovalClosed(notice.metadata)?.approval_id,parent.approval_request!.approval_id);
  const chat=threadDelivery(d.message.thread_id,'peer_operator','a normal message','wake');
  assert.equal(await s.bridge.handleThreadReply(chat),'consumed');assert.equal((s.receipt(chat) as {note:string}).note,'not_relayed');
});
test('a duplicate backend permission request shares one prompt and one decision',async t=>{
  const s=setup(t),a=s.hooks.onPermissionRequest(permission(),signal()),b=s.hooks.onPermissionRequest(permission(),signal());
  assert.equal(s.prompts().length,1);const req=s.prompts()[0];s.deliverSend(req);
  await s.bridge.handleApprovalResponse(s.response(req,'Allow once'));assert.deepEqual(await a,{optionId:'allow-once',decidedBy:'peer_parent'});assert.deepEqual(await b,await a);
  assert.deepEqual(await s.hooks.onPermissionRequest(permission(),signal()),await a);assert.equal(s.prompts().length,0);
});
test('incomplete or semantically invalid launch.answers falls back to text; invalid text leaves the question open',async t=>{
  const s=setup(t),pending=s.hooks.onQuestion(question(),signal()),req=s.sends()[0],d=s.reply(req,'orange','peer_parent');
  const qid=parseQuestion(req.metadata)!.question_id;
  d.message.metadata={'launch.answers':{question_id:qid,answers:{color:'orange'}}};
  await s.bridge.handleThreadReply(d);assert.equal(s.bridge.open(s.job.jobId).questions,1);
  const valid=structuredClone(d);valid.delivery_id='dly_second';valid.message.content.text='blue';valid.message.metadata={'launch.answers':{question_id:qid,answers:{}}};
  await s.bridge.handleThreadReply(valid);assert.deepEqual(await pending,{answers:{color:'Blue'},answeredBy:'peer_parent'});
});

for (const terminal of ['decided','expired'] as const) test(`override of an escalated parent after its child is ${terminal} cannot rewrite the family`,async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),parent=s.prompts()[0];
  const override=s.reply(parent,'','peer_operator',{'launch.decision':terminal==='decided' ? 'Deny' : 'Allow once'});
  await s.bridge.handleApprovalResponse(s.response(parent,'escalate'));
  const operator=s.prompts()[0];s.deliverSend(operator);
  if (terminal==='decided') await s.bridge.handleApprovalResponse(s.response(operator,'Allow once','peer_operator'));
  else s.clock.advance(s.cfg.approvals.operator_expires_ms);
  assert.deepEqual(await pending,{optionId:terminal==='decided' ? 'allow-once' : 'deny',decidedBy:terminal==='decided' ? 'peer_operator' : 'expired'});
  const before=[parent,operator].map(r=>s.ledger.approval(r.approval_request!.approval_id));
  assert.equal(await s.bridge.handleThreadReply(override),'consumed');
  assert.deepEqual([parent,operator].map(r=>s.ledger.approval(r.approval_request!.approval_id)),before);
  assert.equal(s.sends().filter(r=>r.metadata?.['launch.approval_closed']).length,0);
  assert.ok(s.sends().some(r=>r.idempotency_key.includes('.superseded.')));assert.ok(s.receipt(override));
});

test('operator opener lives to the bounded session deadline and roots a second prompt after 24 hours',async t=>{
  const s=setup(t,{approvals:{to_operator:{requesters:['peer_parent']}}});
  s.job.deadlineAt=s.clock.now()+3*86400000;
  const first=s.hooks.onPermissionRequest(permission('first'),signal()),opener=s.prompts()[0];
  assert.equal(Date.parse(opener.expires_at!),s.job.deadlineAt);
  const delivered=s.deliverSend(opener);
  await s.bridge.handleApprovalResponse(s.response(opener,'Allow once','peer_operator'));await first;
  s.clock.advance(25*3600000);
  const second=s.hooks.onPermissionRequest(permission('second'),signal()),later=s.prompts()[0];
  assert.equal(later.thread_id,delivered.thread_id);assert.equal(later.in_reply_to,delivered.message_id);
  assert.ok(Date.parse(opener.expires_at!)>s.clock.now(),'the hub can still accept a reply to this root');
  s.deliverSend(later);await s.bridge.handleApprovalResponse(s.response(later,'Deny','peer_operator'));await second;
});

test('operator opener expiry respects the seven-day envelope cap',async t=>{
  const s=setup(t,{approvals:{to_operator:{requesters:['peer_parent']}}});s.job.deadlineAt=s.clock.now()+9*86400000;
  const pending=s.hooks.onPermissionRequest(permission(),signal());
  assert.equal(Date.parse(s.prompts()[0].expires_at!),s.clock.now()+7*86400000);
  s.bridge.abandonAll(s.job.jobId,'restart');await pending;
});

for (const direct of [false,true]) for (const char of ['x','😀']) test(`complete escalation title is bounded to 200 Unicode code points (${direct ? 'direct' : 'escalated'}, ${char})`,async t=>{
  const s=setup(t,direct ? {approvals:{to_operator:{requesters:['peer_parent']}}} : {});s.job.title=char.repeat(200);
  const pending=s.hooks.onPermissionRequest(permission(),signal());
  if (!direct) {const parent=s.prompts()[0];s.deliverSend(parent);await s.bridge.handleApprovalResponse(s.response(parent,'escalate'));}
  const title=s.prompts()[0].thread_title!;
  assert.equal([...title].length,200);assert.equal(title,`vl escalation: ${char.repeat(185)}`);
  s.bridge.abandonAll(s.job.jobId,'restart');await pending;
});

test('reserved label normalization stays under 6 KiB and escalation preserves the finalized permission',async t=>{
  const s=setup(t),p=permission();p.options[0].label='escalate';p.permission.reason='';p.permission.backend_raw='x'.repeat(3000);
  const initial=finishPermission(p.permission,{machineId:s.cfg.host,jobId:s.job.jobId,permissionMode:s.job.permissionMode,preset:s.job.preset,backend:s.job.backend,workspaceRoot:'/workspace',home:'/home/test'});
  p.permission.reason='x'.repeat(6144-Buffer.byteLength(JSON.stringify(initial)));
  const pending=s.hooks.onPermissionRequest(p,signal()),parent=s.prompts()[0],metadata=parent.metadata!['launch.permission'];
  assert.ok(Buffer.byteLength(JSON.stringify(metadata))<=6144);assert.deepEqual(validateLaunchPermission(metadata),[]);
  assert.deepEqual(parent.approval_request!.options,['escalate(2)','Deny','escalate']);
  s.deliverSend(parent);await s.bridge.handleApprovalResponse(s.response(parent,'escalate'));const operator=s.prompts()[0];
  assert.deepEqual(operator.metadata!['launch.permission'],metadata);
  s.deliverSend(operator);await s.bridge.handleApprovalResponse(s.response(operator,'escalate(2)','peer_operator'));assert.equal((await pending).optionId,'allow-once');
});

for (const text of ['x'.repeat(9000),'😀\n"\\'.repeat(18000)]) test(`oversized question prose is bounded before enqueue and preserves answer mapping (${Buffer.byteLength(text)} input bytes)`,async t=>{
  const s=setup(t),q=question();q.questions[0].text=text;q.questions[0].header='header '.repeat(2000);
  q.questions.push({id:'detail',text,allowOther:true,secret:false});
  const pending=s.hooks.onQuestion(q,signal()),req=s.sends()[0];
  assert.ok(Buffer.byteLength(JSON.stringify(req.metadata))<=8192);assert.ok(Buffer.byteLength(req.content.text)<=65536);
  const wire=parseQuestion(req.metadata)!;assert.ok(wire);assert.deepEqual(wire.questions.map(q=>q.id),['color','detail']);
  assert.deepEqual(wire.questions[0].options,['Blue','Green']);assert.match(req.content.text,/truncated/);
  const d=s.reply(req,'','peer_parent',{'launch.answers':{question_id:wire.question_id,answers:{color:'green',detail:'Keep going'}}});
  await s.bridge.handleThreadReply(d);assert.deepEqual(await pending,{answers:{color:'Green',detail:'Keep going'},answeredBy:'peer_parent'});
});

test('question metadata exactly at 8 KiB remains unchanged; one byte over is shortened',async t=>{
  for (const extra of [0,1]) {
    const s=setup(t),q=question(`boundary-${extra}`);q.questions[0].allowOther=true;
    const qid=`q.${s.job.jobId}.${'a'.repeat(24)}`;
    const base={'launch.question':{v:1,question_id:qid,blocking:true,questions:[{id:'color',text:'',options:['Blue','Green'],allow_other:true}]}};
    q.questions[0].text='x'.repeat(8192-Buffer.byteLength(JSON.stringify(base))+extra);
    const pending=s.hooks.onQuestion(q,signal()),req=s.sends()[0];
    assert.ok(Buffer.byteLength(JSON.stringify(req.metadata))<=8192);
    if (!extra) assert.equal(parseQuestion(req.metadata)!.questions[0].text,q.questions[0].text);
    else assert.match(parseQuestion(req.metadata)!.questions[0].text,/truncated/);
    await s.bridge.handleThreadReply(s.reply(req,'ok'));assert.equal((await pending).answers.color,'ok');
  }
});

test('question IDs or option labels that cannot fit are safely answered without enqueueing oversized metadata',async t=>{
  const s=setup(t),q=question();q.questions[0].options=[{label:'x'.repeat(9000)}];q.questions[0].id='opaque-id';
  const pending=s.hooks.onQuestion(q,signal());
  assert.ok(s.sends().every(r=>!r.metadata?.['launch.question']));
  assert.deepEqual(await pending,{answers:{'opaque-id':timeoutAnswer},answeredBy:'timeout'});
  assert.deepEqual(s.bridge.open(s.job.jobId),{approvals:0,questions:0});
});

for (const relay of [false,true]) test(`opaque backend id escalate remains decidable beside a separate escalation control (${relay ? 'relay' : 'requester'})`,async t=>{
  const s=setup(t,relay ? {relays:{pagd:{principals:[],peers:['peer_parent'],parent_expires_ms:2100000,escalate_option:true,escalate_to_operator:true}}} : {});
  const p=permission();p.options[0].id='escalate';
  const pending=s.hooks.onPermissionRequest(p,signal()),parent=s.prompts()[0];
  assert.deepEqual((parent.metadata!['launch.permission'] as {options:Array<{id:string}>}).options.map(o=>o.id),['escalate','deny']);
  const row=s.ledger.approval(parent.approval_request!.approval_id)!;assert.equal(new Set(row.options.map(o=>o.optionId)).size,row.options.length);
  s.deliverSend(parent);await s.bridge.handleApprovalResponse(s.response(parent,'Allow once'));
  assert.deepEqual(await pending,{optionId:'escalate',decidedBy:'peer_parent'});assert.equal(s.prompts().length,0);
  const next=s.hooks.onPermissionRequest({...p,requestId:'next'},signal()),parent2=s.prompts()[0];s.deliverSend(parent2);
  await s.bridge.handleApprovalResponse(s.response(parent2,'escalate'));const operator=s.prompts()[0];assert.deepEqual(operator.recipients,['peer_operator']);
  assert.deepEqual(operator.metadata!['launch.permission'],parent2.metadata!['launch.permission']);
  s.deliverSend(operator);await s.bridge.handleApprovalResponse(s.response(operator,'Allow once','peer_operator'));assert.equal((await next).optionId,'escalate');
});

test('launch.decision is operator-only even for the requester and authorized relays',async t=>{
  const s=setup(t),pending=s.hooks.onPermissionRequest(permission(),signal()),req=s.prompts()[0],delivered=s.deliverSend(req);
  let resolved=false;void pending.then(()=>{resolved=true;});
  for (const sender of ['peer_parent','peer_relay','peer_intruder','peer_operator']) {
    const d=threadDelivery(delivered.thread_id,sender,'','wake');d.message.in_reply_to=delivered.message_id;d.message.metadata={'launch.decision':'Allow once'};
    assert.equal(await s.bridge.handleThreadReply(d),'consumed');
    if (sender!=='peer_operator') {
      assert.deepEqual(s.receipt(d),{delivery_id:d.delivery_id,state:'processed',note:'not_relayed'});
      assert.equal(s.bridge.open(s.job.jobId).approvals,1);assert.equal(resolved,false);
      assert.equal(s.ledger.approval(req.approval_request!.approval_id)?.decision,null);
    }
  }
  assert.deepEqual(await pending,{optionId:'allow-once',decidedBy:'peer_operator'});
});
