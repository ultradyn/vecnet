import { createHash } from 'node:crypto';
import { newApprovalId } from '../../hub-client/src/ids.ts';
import type { Clock, Log } from '../../hub-client/src/ports.ts';
import type { Delivery, SendRequest } from '../../hub-client/src/wire.ts';
import type { AgentQuestion, LaunchPermission, PermissionDecision, PermissionRequest, QuestionAnswer, SessionHooks, TurnEvent } from './backend.ts';
import type { LauncherConfig } from './config.ts';
import type { Directory } from './directory.ts';
import type { ApprovalRow, JobRow, Ledger, OutboxRow, QuestionRow } from './ledger.ts';
import type { Outbox } from './outbox.ts';
import type { ApprovalRoute } from './policy.ts';
import { approvalOptions, canonicalPermission, finishPermission } from './contract/permission.ts';
import { buildApprovalClosed, buildQuestion, parseAnswers, parseDecidedVia, parseDecision } from './contract/metadata.ts';
import type { LaunchQuestion } from './contract/metadata.ts';
import { redactText } from './redact.ts';

export interface ParentBridgeDeps {
  ledger: Ledger; outbox: Outbox; clock: Clock; log: Log; cfg: LauncherConfig; self: string; home: string;
  dir: Pick<Directory, 'operatorPeerId' | 'isOperator' | 'name'>;
  route(p: LaunchPermission, job: JobRow): ApprovalRoute;
}
export interface ParentBridge {
  /** workspaceRoot may be a function: it is read when each prompt arrives, so a session's worktree recorded after launch applies. */
  hooksFor(job: JobRow, workspaceRoot: string | (() => string), onEvent: (ev: TurnEvent) => void): SessionHooks;
  handleApprovalResponse(d: Delivery): Promise<'consumed' | 'unrelated'>;
  handleThreadReply(d: Delivery): Promise<'consumed' | 'unrelated'>;
  open(jobId: string): { approvals: number; questions: number };
  abandonAll(jobId: string, reason: 'session_ended' | 'restart'): void;
}
const TIMEOUT = 'no answer from requester; use your best judgement and say what you assumed';
const SECRET = 'secrets cannot be provided through vecnet';
const hash = (id: string) => createHash('sha256').update(id).digest('hex').slice(0,24);
type Alarm = { cancel(): void };
interface LiveQuestion { promise: Promise<QuestionAnswer>; finish(answer: QuestionAnswer): void; request: AgentQuestion }
interface LiveApproval {
  promise: Promise<PermissionDecision>; finish(answer: PermissionDecision): void;
  job: JobRow; summary: string; route: ApprovalRoute; activeId: string;
}

function boundedQuestion(question: LaunchQuestion): { metadata: Record<string, unknown>; content: { text: string } } | undefined {
  function pack(q: LaunchQuestion) {
    return {metadata:buildQuestion(q),content:{text:q.questions.map(q=>[q.header,q.text,...q.options.map(label=>`- ${label}`)]
      .filter(v=>v!==undefined).join('\n')).join('\n\n')}};
  }
  function fits(message: ReturnType<typeof pack>) {
    return Buffer.byteLength(JSON.stringify(message.metadata))<=8192 && Buffer.byteLength(message.content.text)<=65536;
  }
  const full=pack(question);
  if (fits(full)) return full;
  // Only shorten prose. IDs and option labels retain their exact answer mapping.
  // If those alone exceed the budget, the caller resolves safely without posting.
  function shortened(limit: number) {
    function text(value: string): string {
      return value.length<=limit ? value : `${value.slice(0,limit).replace(/[\uD800-\uDBFF]$/,'')}… [truncated]`;
    }
    return pack({...question,questions:question.questions.map(q=>({...q,text:text(q.text),
      ...(q.header===undefined ? {} : {header:text(q.header)})}))});
  }
  if (!fits(shortened(0))) return;
  let lo=0,hi=question.questions.reduce((max,q)=>Math.max(max,q.text.length,q.header?.length ?? 0),0);
  while (lo<hi) {
    const mid=Math.ceil((lo+hi)/2);
    if (fits(shortened(mid))) lo=mid;else hi=mid-1;
  }
  return shortened(lo);
}

export function createParentBridge(deps: ParentBridgeDeps): ParentBridge {
  const { ledger, outbox, clock, cfg, dir } = deps;
  const questions = new Map<string, LiveQuestion>();
  const approvals = new Map<string, LiveApproval>();
  // An operator opener owns this slot until its onSent supplies the thread id.
  const opening = new Map<string, string>();
  const waiting = new Map<string, string[]>();
  const kv = ledger.kv('parent-bridge');

  function send(req: SendRequest, tag: string) { outbox.enqueue('send',req.idempotency_key,req,tag); outbox.kick(); }
  function jobMessage(job: JobRow): Pick<SendRequest, 'thread_id' | 'in_reply_to' | 'recipients' | 'priority'> {
    return {thread_id:job.threadId,in_reply_to:job.launchMessageId,recipients:[job.requester],priority:'wake'};
  }
  function note(job: JobRow, purpose: string, text: string) {
    ledger.tx(() => {
      const key = `vl.nt.${job.jobId}.${purpose}.${ledger.nextCounter(job.jobId,purpose)}`;
      send({...jobMessage(job),kind:'message',idempotency_key:key,content:{text:redactText(text).text}},job.jobId);
    });
  }
  function receipt(d: Delivery, note?: string) {
    outbox.enqueue('receipt',`vl.rc.${d.delivery_id}.processed`,{delivery_id:d.delivery_id,state:'processed',...(note ? {note} : {})},'parent-bridge');
    outbox.kick();
  }
  function authorized(job: JobRow, sender: string) {
    return sender === job.requester || dir.isOperator(sender) || job.input.relay_from?.includes(sender) === true;
  }
  function questionAnswer(row: QuestionRow, text: string, answeredBy = 'timeout'): QuestionAnswer {
    return {answers:Object.fromEntries(row.questions.map(q=>[q.id,text])),answeredBy};
  }
  function finishQuestion(row: QuestionRow, answer: QuestionAnswer, state: QuestionRow['state']) {
    if (ledger.question(row.questionId)?.state !== 'open') return;
    ledger.updateQuestion(row.questionId,{state,answeredBy:answer.answeredBy});
    questions.get(row.questionId)?.finish(answer);
    questions.delete(row.questionId);
  }
  function timeoutQuestion(id: string) {
    const row = ledger.question(id); if (!row || row.state !== 'open') return;
    finishQuestion(row,questionAnswer(row,TIMEOUT),'timed_out');
    const job = ledger.job(row.jobId); if (job) note(job,'question_timeout',TIMEOUT);
  }
  function ask(job: JobRow, request: AgentQuestion, signal: AbortSignal): Promise<QuestionAnswer> {
    if (request.questions.some(q=>q.secret)) {
      note(job,'secret_question',SECRET);
      return Promise.resolve({answers:Object.fromEntries(request.questions.map(q=>[q.id,SECRET])),answeredBy:'timeout'});
    }
    const questionId = `q.${job.jobId}.${hash(request.requestId)}`;
    const live = questions.get(questionId); if (live) return live.promise;
    const prior = ledger.question(questionId);
    if (prior) return Promise.resolve(questionAnswer(prior,TIMEOUT));
    const published = request.questions.map(q=>({id:q.id,...(q.header === undefined ? {} : {header:redactText(q.header).text}),
      text:redactText(q.text).text,options:(q.options ?? []).map(o=>redactText(o.label).text),allow_other:q.allowOther}));
    const message = boundedQuestion({v:1,question_id:questionId,blocking:request.blocking,questions:published});
    if (!message) {
      note(job,'question_oversized',`Question identifiers or options exceed the message budget. ${TIMEOUT}`);
      return Promise.resolve({answers:Object.fromEntries(request.questions.map(q=>[q.id,TIMEOUT])),answeredBy:'timeout'});
    }
    let resolve!: (answer: QuestionAnswer) => void;
    const promise = new Promise<QuestionAnswer>(done=>{resolve=done;});
    let alarm: Alarm | undefined;
    const abort = () => timeoutQuestion(questionId);
    questions.set(questionId,{request,promise,finish(answer) {alarm?.cancel();signal.removeEventListener('abort',abort);resolve(answer);}});
    const row: QuestionRow = {questionId,jobId:job.jobId,messageId:null,blocking:request.blocking,questions:request.questions,
      state:'open',answeredBy:null,createdAt:clock.now(),expiresAt:clock.now()+cfg.questions.blocking_timeout_ms};
    ledger.tx(() => {
      ledger.putQuestion(row);
      send({...jobMessage(job),kind:'message',idempotency_key:`vl.q.${job.jobId}.${hash(request.requestId)}`,...message},job.jobId);
    });
    if (request.blocking) alarm = clock.timer(cfg.questions.blocking_timeout_ms,abort);
    signal.addEventListener('abort',abort,{once:true});
    if (signal.aborted) abort();
    return promise;
  }

  function finishApproval(row: ApprovalRow, optionId: string, decidedBy: string, state: ApprovalRow['state'], decidedVia: string | null = null, reason?: string) {
    if (ledger.approval(row.approvalId)?.state !== 'open') return;
    ledger.updateApproval(row.approvalId,{state,decision:optionId,decidedBy,decidedVia,decidedAt:clock.now()});
    const live = approvals.get(row.approvalId);
    live?.finish({optionId,decidedBy,...(reason ? {reason} : {})});
    approvals.delete(row.approvalId);
  }
  function overrideFamily(row: ApprovalRow, decision: string, decidedBy: string, decidedVia: string | null) {
    // A parent card can still be overridden while its operator card is open.
    // Persist the actual choice on both cards before resolving the live hook.
    const seen = new Set<string>();
    const family: ApprovalRow[] = [];
    let current: ApprovalRow | undefined = row;
    while (current && !seen.has(current.approvalId)) {
      seen.add(current.approvalId);
      family.push(current);
      if (!current.escalatedTo) break;
      if (current.state !== 'escalated') return false;
      current = current.escalatedTo ? ledger.approval(current.escalatedTo) : undefined;
    }
    // Validate the whole family before writing any decisions: an escalated
    // ancestor is no longer live once its active descendant has terminated.
    if (!current || current.escalatedTo || current.state !== 'open') return false;
    for (const member of family) {
      ledger.updateApproval(member.approvalId,{state:'superseded',decision,decidedBy,decidedVia,decidedAt:clock.now()});
      approvals.get(member.approvalId)?.finish({optionId:decision,decidedBy});
      approvals.delete(member.approvalId);
    }
    return true;
  }
  function dispatchOperator(id: string) {
    const row = ledger.approval(id), live = approvals.get(id);
    if (!row || row.state !== 'open' || !live) return;
    const thread = ledger.escalationThread(row.jobId);
    if (!thread && opening.has(row.jobId)) {
      const queue = waiting.get(row.jobId) ?? []; queue.push(id); waiting.set(row.jobId,queue); return;
    }
    if (!thread) opening.set(row.jobId,id);
    ledger.updateApproval(id,{threadId:thread});
    const parentId = kv.get(`parent.${id}`);
    send({idempotency_key:`vl.ar.${id}`,kind:'approval_request',priority:'wake',recipients:[row.addressee],
      ...(thread ? {thread_id:thread,...(kv.get(`opener.${row.jobId}`) ? {in_reply_to:kv.get(`opener.${row.jobId}`)} : {})} : {
        thread_title:[...redactText(`vl escalation: ${live.job.title}`).text].slice(0,200).join(''),
        expires_at:new Date(Math.min(live.job.deadlineAt,clock.now()+7*86400000)).toISOString()}),
      content:{text:live.summary},metadata:{'launch.permission':row.permission,'launch.job_id':row.jobId,...(parentId ? {'launch.escalated_from':parentId} : {})},
      approval_request:{approval_id:id,question:live.summary,options:row.options.map(o=>o.label),expires_at:new Date(row.expiresAt).toISOString(),escalate_to_operator:false}},row.jobId);
  }
  function releaseOperator(jobId: string, id: string) {
    if (opening.get(jobId) !== id) return;
    opening.delete(jobId);
    const queue = waiting.get(jobId) ?? []; waiting.delete(jobId);
    for (const next of queue) dispatchOperator(next);
  }
  function expire(id: string) {
    const row = ledger.approval(id), live = approvals.get(id);
    if (!row || row.state !== 'open') return;
    if (row.route !== 'operator' && live?.route.onParentExpiry === 'escalate') escalate(row,live);
    else finishApproval(row,row.defaultOnExpiry,'expired','expired');
  }
  function trackTimer(row: ApprovalRow, live: LiveApproval): Alarm {
    return clock.timer(Math.max(0,row.expiresAt-clock.now()),()=>{
      if (approvals.get(row.approvalId) === live) expire(row.approvalId);
    });
  }
  const alarms = new Map<string, Alarm>();
  function operatorPrompt(parent: ApprovalRow, live: LiveApproval) {
    const id = newApprovalId(clock.now()), operator = dir.operatorPeerId();
    if (!operator) {finishApproval(parent,parent.defaultOnExpiry,'expired','expired');return;}
    const row: ApprovalRow = {...parent,approvalId:id,route:'operator',requestMessageId:null,threadId:null,addressee:operator,
      options:parent.options.slice(0,parent.permission.options.length),state:'open',escalatedTo:null,createdAt:clock.now(),expiresAt:clock.now()+cfg.approvals.operator_expires_ms,
      decidedAt:null,decidedBy:null,decidedVia:null,decision:null};
    ledger.tx(() => {
      ledger.updateApproval(parent.approvalId,{state:'escalated',escalatedTo:id,decidedAt:clock.now()});
      ledger.putApproval(row);kv.set(`parent.${id}`,parent.approvalId);
      approvals.delete(parent.approvalId);alarms.get(parent.approvalId)?.cancel();alarms.delete(parent.approvalId);
      live.activeId=id;approvals.set(id,live);alarms.set(id,trackTimer(row,live));dispatchOperator(id);
    });
  }
  function escalate(row: ApprovalRow, live: LiveApproval) { operatorPrompt(row,live); }
  function requestPermission(job: JobRow, workspaceRoot: string, req: PermissionRequest, signal: AbortSignal): Promise<PermissionDecision> {
    const context = {machineId:cfg.host,jobId:job.jobId,permissionMode:job.permissionMode,preset:job.preset,backend:job.backend,workspaceRoot,home:deps.home};
    let permission = finishPermission({...req.permission,options:req.options},context);
    // Route on the complete request, never the published copy: shortening to
    // the 6 KiB cap can drop a privileged command suffix or a sensitive path.
    const route = deps.route(canonicalPermission({...req.permission,options:req.options},context),job);
    const withEscalate = route.to !== 'operator' && route.escalateOption;
    // Normalize labels using local surrogate IDs, then restore the opaque IDs.
    // The launcher control gets a distinct ID even if a backend uses 'escalate'.
    const backendOptions = permission.options;
    const ids = new Set(backendOptions.map(o=>o.id));
    let escalationId='escalate';
    while (ids.has(escalationId)) escalationId+=':';
    const options = approvalOptions(backendOptions.map((o,i)=>({...o,id:`option-${i}`})),withEscalate)
      .map((o,i)=>({...o,optionId:i<backendOptions.length ? backendOptions[i].id : escalationId}));
    const normalized = options.slice(0,backendOptions.length).map(o=>({id:o.optionId,label:o.label,grants:o.grants}));
    // Re-finalize the original draft after reserved-label normalization so the
    // serialized byte cap includes every published suffix. Escalation reuses it.
    const wasRedacted=permission.redacted;
    permission = finishPermission({...req.permission,options:normalized},context);
    permission.redacted ||= wasRedacted;
    if (!options.some(o=>o.optionId===req.defaultOnExpiry && ['deny','deny_always','cancel'].includes(o.grants))) throw new Error('invalid permission expiry option');
    const addressee = route.to === 'operator' ? dir.operatorPeerId() : job.requester;
    if (!addressee) return Promise.resolve({optionId:req.defaultOnExpiry,decidedBy:'expired'});
    const requestKey = `request.${job.jobId}.${hash(req.requestId)}`;
    const priorId = kv.get(requestKey), prior = priorId ? ledger.approval(priorId) : undefined;
    if (prior) {
      let row = prior;
      while (row.escalatedTo) {const next=ledger.approval(row.escalatedTo);if (!next) break;row=next;}
      const live=approvals.get(row.approvalId);if (live) return live.promise;
      return Promise.resolve({optionId:row.decision ?? row.defaultOnExpiry,decidedBy:row.decidedBy ?? 'expired'});
    }
    const id = newApprovalId(clock.now());
    let resolve!: (answer: PermissionDecision) => void;
    const promise = new Promise<PermissionDecision>(done=>{resolve=done;});
    const abort = () => {
      const row=ledger.approval(live.activeId);if (row) finishApproval(row,row.defaultOnExpiry,'expired','abandoned');
    };
    const live: LiveApproval = {job,summary:redactText(req.summary).text.replace(/[\r\n]+/g,' ').slice(0,512),route,activeId:id,promise,
      finish(answer) {alarms.get(live.activeId)?.cancel();alarms.delete(live.activeId);signal.removeEventListener('abort',abort);resolve(answer);}};
    const row: ApprovalRow = {approvalId:id,jobId:job.jobId,route:route.to,requestMessageId:null,threadId:route.to==='operator' ? null : job.threadId,
      addressee,backendRequestId:req.requestId,options,defaultOnExpiry:req.defaultOnExpiry,permission,state:'open',decidedBy:null,decidedVia:null,decision:null,escalatedTo:null,
      createdAt:clock.now(),expiresAt:clock.now()+(route.to==='operator' ? cfg.approvals.operator_expires_ms : route.parentExpiresMs),decidedAt:null};
    ledger.tx(() => {
      ledger.putApproval(row);kv.set(requestKey,id);approvals.set(id,live);alarms.set(id,trackTimer(row,live));
      if (route.to==='operator') dispatchOperator(id);
      else send({...jobMessage(job),idempotency_key:`vl.ar.${id}`,kind:'approval_request',content:{text:live.summary},metadata:{'launch.permission':permission},
        approval_request:{approval_id:id,question:live.summary,options:options.map(o=>o.label),expires_at:new Date(row.expiresAt).toISOString(),escalate_to_operator:route.escalateToOperator}},job.jobId);
    });
    signal.addEventListener('abort',abort,{once:true});if(signal.aborted) abort();
    return promise;
  }

  outbox.onSent((row: OutboxRow, result: unknown) => {
    if (row.kind !== 'send' || !result || typeof result !== 'object') return;
    const res = result as {message_id?: unknown; thread_id?: unknown};
    if (typeof res.message_id !== 'string' || typeof res.thread_id !== 'string') return;
    const req = row.payload as SendRequest;
    const questionId = (req.metadata?.['launch.question'] as {question_id?: string} | undefined)?.question_id;
    if (questionId && ledger.question(questionId)) ledger.updateQuestion(questionId,{messageId:res.message_id});
    const id=req.approval_request?.approval_id, approval=id ? ledger.approval(id) : undefined;
    if (id && approval) {
      ledger.tx(() => {
        ledger.updateApproval(id,{requestMessageId:res.message_id as string,threadId:res.thread_id as string});
        if (approval.route==='operator') {
          if (!ledger.escalationThread(approval.jobId)) {
            ledger.setEscalationThread(approval.jobId,res.thread_id as string);kv.set(`opener.${approval.jobId}`,res.message_id as string);
          }
          releaseOperator(approval.jobId,id);
        }
      });
    }
  });
  outbox.onRejected((row) => {
    const req = row.payload as SendRequest, id=req.approval_request?.approval_id;
    if (id) {
      const approval=ledger.approval(id);
      if (approval) {finishApproval(approval,approval.defaultOnExpiry,'expired','abandoned');releaseOperator(approval.jobId,id);}
    }
    const questionId=(req.metadata?.['launch.question'] as {question_id?:string} | undefined)?.question_id;
    if (questionId) timeoutQuestion(questionId);
  });

  function matchesThread(d: Delivery, row: ApprovalRow): boolean {
    return row.threadId !== null && d.message.thread_id===row.threadId;
  }
  function superseded(row: ApprovalRow, d: Delivery) {
    const job=ledger.job(row.jobId);if (job) note(job,'superseded','This approval was superseded.');receipt(d);
  }
  const bridge: ParentBridge = {
    hooksFor(job,workspaceRoot,onEvent) {
      return {onQuestion:(q,s)=>ask(job,q,s),onPermissionRequest:(r,s)=>requestPermission(job,typeof workspaceRoot === 'function' ? workspaceRoot() : workspaceRoot,r,s),
        onEvent(ev) {try {onEvent(ev);} catch {deps.log.warn('parent bridge event handler failed');}}};
    },
    async handleApprovalResponse(d) {
      const response=d.message.approval_response;
      if (d.message.kind!=='approval_response' || !response) return 'unrelated';
      const row=ledger.approval(response.approval_id);
      if (!row || !matchesThread(d,row)) return 'unrelated';
      const sender=d.message.sender;
      if (!(sender===row.addressee || (sender==='hub' && response.decision==='expired'))) {receipt(d,'not_relayed');return 'consumed';}
      if (row.state!=='open') {
        if (row.state==='superseded' || row.state==='escalated') superseded(row,d);else receipt(d);
        return 'consumed';
      }
      if (response.decision==='expired' && sender==='hub') expire(row.approvalId);
      else {
        const option=row.options.find(o=>o.label===response.decision);
        if (!option) {receipt(d,'invalid_option');return 'consumed';}
        const live=approvals.get(row.approvalId);
        if (option.label==='escalate' && !row.permission.options.some(o=>o.id===option.optionId) && row.route!=='operator' && live?.route.escalateOption) escalate(row,live);
        else finishApproval(row,option.optionId,sender,'decided',parseDecidedVia(d.message.metadata) ?? null);
      }
      receipt(d);return 'consumed';
    },
    async handleThreadReply(d) {
      if (d.message.kind!=='message') return 'unrelated';
      const parent=d.message.in_reply_to;
      const row=parent ? ledger.approvalByRequestMessage(parent) : undefined;
      if (row && matchesThread(d,row)) {
        const decision=parseDecision(d.message.metadata);
        if (dir.isOperator(d.message.sender) && decision) {
          const option=row.options.find(o=>o.label===decision && row.permission.options.some(p=>p.id===o.optionId));
          if (!option) {receipt(d,'invalid_option');return 'consumed';}
          if (row.state==='open' || row.state==='escalated') {
            const job=ledger.job(row.jobId);
            const parentId = kv.get(`parent.${row.approvalId}`);
            const original = (parentId ? ledger.approval(parentId) : undefined) ?? row;
            const overridden=ledger.tx(() => {
              if (!overrideFamily(original,option.optionId,d.message.sender,parseDecidedVia(d.message.metadata) ?? null)) return false;
              if(job) send({...jobMessage(job),idempotency_key:`vl.ac.${original.approvalId}`,kind:'message',content:{text:'Approval closed by operator.'},
                metadata:buildApprovalClosed({approval_id:original.approvalId,superseded_by:'operator',decision:option.label})},row.jobId);
              return true;
            });
            if (!overridden) {superseded(row,d);return 'consumed';}
          } else {superseded(row,d);return 'consumed';}
          receipt(d);return 'consumed';
        }
        if (row.state!=='open' && d.message.sender===row.addressee) {superseded(row,d);return 'consumed';}
        receipt(d,'not_relayed');return 'consumed';
      }
      const q=parent ? ledger.questionByMessage(parent) : undefined;
      if (q) {
        const job=ledger.job(q.jobId);
        if (!job || d.message.thread_id!==job.threadId) return 'unrelated';
        if (!authorized(job,d.message.sender)) {receipt(d,'not_relayed');return 'consumed';}
        if (q.state!=='open') {receipt(d);return 'consumed';}
        const structured=parseAnswers(d.message.metadata);
        function answers(values: Record<string,string>): Record<string,string> | undefined {
          if (Object.keys(values).length!==q!.questions.length) return;
          const result: Record<string,string> = {};
          const published=(questions.get(q!.questionId)?.request.questions ?? q!.questions);
          for (const question of published) {
            if (!Object.hasOwn(values,question.id)) return;
            const value=values[question.id],label=question.options?.find(o=>redactText(o.label).text.toLowerCase()===value.trim().toLowerCase())?.label;
            if (label===undefined && !question.allowOther) return;
            Object.defineProperty(result,question.id,{value:label ?? value,enumerable:true});
          }
          return result;
        }
        const resolved=(structured?.question_id===q.questionId ? answers(structured.answers) : undefined) ??
          answers(Object.fromEntries(q.questions.map(question=>[question.id,d.message.content.text])));
        if (resolved) finishQuestion(q,{answers:resolved,answeredBy:d.message.sender},'answered');
        receipt(d,resolved ? undefined : 'invalid_answer');return 'consumed';
      }
      if (ledger.jobByEscalationThread(d.message.thread_id)) {receipt(d,'not_relayed');return 'consumed';}
      return 'unrelated';
    },
    open(jobId) {return {approvals:ledger.openApprovals(jobId).length,questions:ledger.openQuestions(jobId).length};},
    abandonAll(jobId,reason) {
      for (const row of ledger.openApprovals(jobId)) finishApproval(row,row.defaultOnExpiry,'expired','abandoned',null,reason);
      for (const row of ledger.openQuestions(jobId)) finishQuestion(row,questionAnswer(row,TIMEOUT),'abandoned');
      waiting.delete(jobId);
    },
  };
  return bridge;
}
