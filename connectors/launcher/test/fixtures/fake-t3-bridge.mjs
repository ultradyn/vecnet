#!/usr/bin/env node
import { readFileSync, writeFileSync, appendFileSync } from 'node:fs';

const argv = process.argv.slice(2);
appendFileSync(process.env.FAKE_T3_LOG, JSON.stringify(argv) + '\n');
const fail = message => { process.stderr.write(message + '\n'); process.exit(1); };
if (argv[0] !== '--as' || !argv[1]) { process.exit(2); }
const [command, tool, flag, json] = argv.slice(2);
const name = command === 'call' ? tool : command;
const supported = ['models','projects','t3_project_list','t3_thread_launch','t3_thread_list','t3_thread_read','t3_thread_send','t3_thread_wait','t3_thread_interrupt','t3_thread_organize'];
if (!supported.includes(name)) process.exit(2);
if (process.env.FAKE_T3_FAIL === name) fail('server_down Bearer fixture-secret\nprivate second line');
const state = JSON.parse(readFileSync(process.env.FAKE_T3_STATE, 'utf8'));
const now = '2026-11-05T00:00:00Z';
// Fill the fields required by the documented bridge output schemas. Tests
// choose behavior through state, while the executable preserves the wire shape.
state.models = state.models.map(provider => ({
  displayName:provider.providerInstanceId, driverKind:null, installed:null, status:null,
  constraints:[], canRunChildTask:null, canRunCrossProviderChildTask:null, ...provider,
  models:provider.models.map(model => ({label:model.id,isCustom:null,status:null,...model})),
}));
for (const entry of state.threads) {
  entry.thread = {createdBy:'agent',creationSource:'mcp',providerInstanceId:'provider',model:'model',
    runtimeMode:'auto',interactionMode:'default',linkedPullRequest:null,titleRegeneration:null,
    parentThreadId:null,relationshipToParent:null,runCount:entry.recentRuns.length,itemCount:entry.items.length,
    settledAt:null,snoozed:false,snoozedUntil:null,createdAt:now,...entry.thread};
  entry.recentRuns = entry.recentRuns.map(run => ({providerInstanceId:'provider',model:'model',requestedAt:now,startedAt:now,completedAt:null,...run}));
}
const save = () => writeFileSync(process.env.FAKE_T3_STATE, JSON.stringify(state));
const reply = value => { save(); process.stdout.write(JSON.stringify(value)); };
if (command === 'models' && tool === '--json') reply(state.models);
else if (command === 'projects' && tool === '--json') reply(state.projects.map(project => ({...project,default:{instance:null,model:null}})));
else if (command === 'call' && flag === '--json') {
  const args = JSON.parse(json);
  const entry = state.threads?.find(x => x.thread.threadId === args.threadId);
  if (name === 't3_project_list') reply({ projects: state.projects.map(x => ({ id:x.id, title:x.name, workspaceRoot:x.cwd })), nextCursor:null });
  else if (name === 't3_thread_launch') {
    const id = `thread-${state.threads.length + 1}`;
    state.threads.push({ thread: { threadId:id, projectId:args.projectId, title:args.title, status:'running', activeRunId:'run-1', latestRunId:'run-1', pendingRequestCount:0, archived:false, settled:false, branch:'observed-branch', worktreePath:'/observed/worktree', updatedAt:'2026-11-05T00:00:00Z' }, recentRuns:[{ runId:'run-1', ordinal:1, status:'running' }], items:[] });
    reply({ threadId:id, projectId:args.projectId, modelSelection:args.modelSelection, runId:'run-1', status:'running' });
  } else if (name === 't3_thread_list') {
    // Return a non-prefix match too: the client must enforce ownership.
    const all = state.threads.filter(x => x.thread.projectId === args.projectId && x.thread.settled === args.settled && x.thread.title.includes(args.titleContains));
    const start = args.cursor ?? 0;
    const page = all.slice(start, start + (state.pageSize ?? 100));
    reply({ projectId:args.projectId, currentThreadId:null, threads:page.map(x => x.thread), nextCursor:start+page.length < all.length ? start+page.length : null, total:all.length });
  } else if (!entry) fail('thread_not_found');
  else if (name === 't3_thread_read') {
    const allowed = args.itemId ? ['threadId','itemId','textOffset'] : ['threadId','view','afterPosition','limit'];
    if (Object.keys(args).some(k => !allowed.includes(k))) { process.exit(2); }
    const chunkSize = state.chunkSize ?? 50000;
    const chunk = (item, offset = 0) => {
      const text = item.text ?? '';
      const end = offset + chunkSize;
      return { visibility:'local',sourceThreadId:entry.thread.threadId,subagentId:null,messageId:null,creationSource:'provider',status:'completed',title:null,updatedAt:now,...item, text:text.slice(offset,end), textTruncated:end < text.length, nextTextOffset:end < text.length ? end : null };
    };
    const all = entry.items.filter(x => args.itemId ? x.itemId === args.itemId : x.position > (args.afterPosition ?? -1));
    const page = args.itemId ? all.map(x => chunk(x,args.textOffset ?? 0)) : all.slice(0,args.limit ?? 50).map(x => chunk(x));
    reply({ thread:entry.thread, recentRuns:entry.recentRuns, items:page, nextPosition:page.at(-1)?.position ?? args.afterPosition ?? 0, hasMore:!args.itemId && all.length > page.length });
  } else if (name === 't3_thread_send') {
    if (state.escalationDenied) fail('runtime_mode_escalation_denied');
    state.sends ??= [];
    if (!state.sends.some(x => x.clientRequestId === args.clientRequestId)) state.sends.push(args);
    reply({ threadId:args.threadId, messageId:`message-${state.sends.length}`, runId:entry.thread.activeRunId ?? 'run-next', status:'running', delivery:({auto:'started',queue:'queued',steer:'steered'})[args.mode] });
  } else if (name === 't3_thread_wait') {
    if (entry.completeNextWait) {
      const next = entry.completeNextWait;
      entry.thread.status = next.status;
      entry.thread.activeRunId = null;
      entry.recentRuns.find(x => x.runId === entry.thread.latestRunId).status = next.status;
      entry.items.push(...next.items);
      delete entry.completeNextWait;
    }
    reply({ threadId:args.threadId, runId:entry.thread.latestRunId, status:entry.thread.status, timedOut:entry.thread.activeRunId !== null });
  } else if (name === 't3_thread_interrupt') {
    entry.thread.status = 'interrupted'; entry.thread.activeRunId = null;
    const run = entry.recentRuns.find(x => x.runId === entry.thread.latestRunId);
    if (run) {run.status='interrupted';run.completedAt=now;}
    reply({ threadId:args.threadId, runId:entry.thread.latestRunId, status:'interrupted' });
  } else if (name === 't3_thread_organize' && args.action === 'settle') {
    entry.thread.settled = true; reply({ threadId:args.threadId, settled:true });
  } else { process.exit(2); }
} else { process.exit(2); }
