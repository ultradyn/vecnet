import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import plugin from '../../src/backends/t3/index.ts';
import type { T3Config } from '../../src/backends/t3/index.ts';
import type { LaunchSpec, PluginContext, SessionHooks, TurnEvent, TurnInput, TurnCursor } from '../../src/backend.ts';
import { redactText } from '../../src/redact.ts';
import { checkRoute } from '../../src/route-check.ts';
import { fakeClock } from '../../../hub-client/testing/fixtures.ts';

const time = '2026-11-05T00:00:00Z';
function catalog() {
  return [{ providerInstanceId:'provider', enabled:true, source:'live', stale:false,
    runtimeModes:['approval-required','auto','full-access'],
    models:[{id:'model',hidden:false,options:[{id:'effort',type:'select',options:[{id:'high'},{id:'low'}]}]}] }];
}
interface FakeThread {
  thread: {threadId:string; projectId:string; title:string; status:string; activeRunId:string | null; latestRunId:string; pendingRequestCount:number; branch:string; worktreePath:string; archived:boolean; settled:boolean; updatedAt:string};
  recentRuns: Array<{runId:string; ordinal:number; status:string; completedAt?:string}>;
  items: Record<string,unknown>[];
  completeNextWait?: {status:string; items:Record<string,unknown>[]};
}
function thread(id = 'existing', title = 'vl-job_test task', projectId = 'project'): FakeThread {
  return { thread:{ threadId:id, projectId, title, status:'running', activeRunId:'run-1', latestRunId:'run-1',
    pendingRequestCount:0, branch:'observed-branch', worktreePath:'/observed/worktree', archived:false, settled:false, updatedAt:time },
    recentRuns:[{runId:'run-1',ordinal:1,status:'running',completedAt:time}], items:[] as Record<string,unknown>[] };
}
function spec(overrides: Partial<LaunchSpec> = {}): LaunchSpec {
  return {jobId:'job_test',tag:'vl-job_test',title:'vl-job_test task',prompt:'Do this task.',
    project:{alias:'repo',repoRoot:'/repo',baseRef:'master'},branch:'launch/job_test',preset:'good',permissionMode:'ask',attachments:[],env:{},...overrides};
}
const input = (text = 'next'): TurnInput & { batchId: string } => ({batchId:'batch-stored',items:[{deliveryId:'dly_1',messageId:'msg_1',from:'peer_1',text,attachments:[]}]});
async function fixture(t: TestContext) {
  const dir = await mkdtemp(join(tmpdir(),'vl-t3-'));
  t.after(() => rm(dir,{recursive:true,force:true}));
  const statePath = join(dir,'state.json'), logPath = join(dir,'argv.jsonl');
  const initial = {models:catalog(),projects:[{id:'project',name:'repo',cwd:'/repo'}],threads:[thread()]};
  await writeFile(statePath,JSON.stringify(initial));
  const events: TurnEvent[] = [];
  const hooks: SessionHooks = {onEvent:event => events.push(event),onPermissionRequest:async () => {throw new Error('UI only');},onQuestion:async () => {throw new Error('text only');}};
  const clock = fakeClock(Date.parse(time));
  const store = new Map<string,string>();
  const ctx: PluginContext & {clock:typeof clock} = {
    clock, host:'test-host',now:() => new Date(clock.now()),redact:s => redactText(s).text,
    checkRoute:route => checkRoute(route,{readFile:async () => 'route-key',fetch:async () => new Response('',{status:503})}),
    log:{info(){},warn(){},error(){}},store:{get:k => store.get(k),set:(k,v) => {store.set(k,v);},delete:k => {store.delete(k);},keys:(prefix='') => [...store.keys()].filter(k => k.startsWith(prefix))},
    readSecretFile:async () => { throw new Error('plugin must not read credentials'); },
    exec:async (argv,opts) => new Promise((resolve,reject) => {
      execFile(argv[0],argv.slice(1),{signal:opts.signal,timeout:opts.timeoutMs,maxBuffer:2*1024*1024,
        env:{PATH:process.env.PATH,FAKE_T3_STATE:statePath,FAKE_T3_LOG:logPath,...opts.env,...env}},(err,stdout,stderr) => {
          if (err && typeof err.code !== 'number') {reject(err);return;}
          resolve({code:err?.code as number ?? 0,stdout,stderr});
        });
    }),
  };
  const env: Record<string,string> = {};
  const config: T3Config = {bridge_bin:fileURLToPath(new URL('../fixtures/fake-t3-bridge.mjs',import.meta.url)),caller:'vecnet-launcher',
    presets:{good:{instance:'provider',model:'model',runtime_modes:['ask','auto','full-access'],options:[{id:'effort',value:'high'}]}},projects:{repo:{project_id:'project'}}};
  const backend = () => plugin.create(config,ctx);
  const state = async () => JSON.parse(await readFile(statePath,'utf8')) as typeof initial & Record<string,any>;
  const update = async (fn:(value: Awaited<ReturnType<typeof state>>) => void) => {const value=await state();fn(value);await writeFile(statePath,JSON.stringify(value));};
  const calls = async (): Promise<string[][]> => (await readFile(logPath,'utf8')).trim().split('\n').filter(Boolean).map(x => JSON.parse(x));
  const live = async () => { const b=await backend(); const found=await b.reattach({backend:'t3',host:'test-host',id:'existing',extra:{tag:'vl-job_test'}},hooks); assert.equal(found.found,'live'); return found.handle; };
  return {config,ctx,backend,live,hooks,events,env,state,update,calls,clock};
}

test('launch calls t3_thread_launch with workspaceStrategy worktree branch launch/<job> and the vl- title; never the launch subcommand; ref.extra branch/workspace come from a read',async t => {
  const f=await fixture(t); const b=await f.backend(); const h=await b.launch(spec(),f.hooks);
  const calls=await f.calls(); const launch=calls.find(x => x[3]==='t3_thread_launch'); assert.ok(launch);
  assert.deepEqual(JSON.parse(launch[5]),{projectId:'project',title:'vl-job_test task',message:'Do this task.',runtimeMode:'approval-required',
    modelSelection:{instanceId:'provider',model:'model',options:[{id:'effort',value:'high'}]},
    workspaceStrategy:{type:'worktree',baseRef:'master',branch:'launch/job_test',startFromOrigin:false}});
  assert.equal(calls.some(x => x[2]==='launch'),false);
  assert.deepEqual(h.ref,{backend:'t3',host:'test-host',id:'thread-2',extra:{tag:'vl-job_test',branch:'observed-branch',workspace:'/observed/worktree',link:'t3-thread://v1/thread-2'}});
  delete f.config.projects;
  await b.launch(spec({jobId:'job_second',tag:'vl-job_second',title:'vl-job_second task'}),f.hooks);
  assert.ok((await f.calls()).some(x => x[2]==='projects'));
});

test('every exec carries --as vecnet-launcher; argv never contains a vecnet credential',async t => {
  const f=await fixture(t); const b=await f.backend(); await b.capabilities(); await b.health(); await b.listOwned(); await b.findByTag('vl-job_test');
  const h=await b.launch(spec({env:{VECNET_CREDENTIAL:'vn_prn_verysecret'}}),f.hooks);
  await h.send(input(),'start'); await h.activity(); await h.interrupt('requested'); await h.settle('requested');
  for (const argv of await f.calls()) {assert.deepEqual(argv.slice(0,2),['--as','vecnet-launcher']);assert.equal(argv.join(' ').includes('vn_prn_verysecret'),false);}
});

test('send modes map start→auto, steer→steer, queue→queue; clientRequestId is vl-<batchId>; a serialised message over 120 000 chars throws input_too_large',async t => {
  const f=await fixture(t); const h=await f.live();
  for (const mode of ['start','steer','queue'] as const) {await h.send(input(),mode);}
  const sends=(await f.calls()).filter(x => x[3]==='t3_thread_send').map(x => JSON.parse(x[5]));
  assert.deepEqual(sends.map(x => x.mode),['auto','steer','queue']);
  for (const send of sends) assert.equal(send.clientRequestId,'vl-batch-stored');
  assert.equal((await f.state()).sends.length,1);
  await assert.rejects(h.send(input('"'.repeat(60000)),'start'),/input_too_large/);
  await assert.rejects(h.send(input('x'.repeat(60001)),'start'),/input_too_large/);
  assert.equal((await f.calls()).filter(x => x[3]==='t3_thread_send').length,3);
});

test('turns yields one turn_finished per terminal run with the hydrated final agent text; cancelled→interrupted, rolled_back→failed; resuming from the cursor repeats nothing',async t => {
  const f=await fixture(t); const h=await f.live();
  await f.update(s => {s.chunkSize=3;s.threads[0].completeNextWait={status:'completed',items:[
    {itemId:'draft',position:1,runId:'run-1',createdBy:'agent',type:'assistant_message',text:'draft'},
    {itemId:'tool',position:2,runId:'run-1',createdBy:'agent',type:'tool_result',text:'not final'},
    {itemId:'final',position:3,runId:'run-1',createdBy:'agent',type:'ASSISTANT_message',text:'hydrated final'},
    {itemId:'user',position:4,runId:'run-1',createdBy:'user',type:'assistant_message',text:'not agent'},
  ]};});
  let cursor: TurnCursor | undefined;
  const first=h.turns()[Symbol.asyncIterator]();
  const one=await first.next(); assert.equal(one.value?.event.type,'turn_finished');
  if (!one.value || one.value.event.type!=='turn_finished') throw new Error('missing result');
  assert.deepEqual({...one.value.event,at:time},{type:'turn_finished',turnId:'run-1',at:time,outcome:'completed',finalText:'hydrated final',consumed:[]});
  cursor=one.value.cursor; assert.equal(JSON.parse(one.value.cursor.value).r,'run-1'); await first.return?.();
  for (const [i,status,outcome] of [[2,'cancelled','interrupted'],[3,'rolled_back','failed'],[4,'interrupted','interrupted'],[5,'failed','failed']] as const) {
    await f.update(s => {const e=s.threads[0];e.thread.latestRunId=`run-${i}`;e.thread.status=status;e.recentRuns.push({runId:`run-${i}`,ordinal:i,status,completedAt:time});e.items.push({itemId:`item-${i}`,position:i+4,runId:`run-${i}`,createdBy:'agent',type:'assistant',text:`text ${i}`});});
    const it=h.turns(cursor)[Symbol.asyncIterator](); const result=await it.next();
    assert.equal(result.value?.event.type,'turn_finished');
    if (!result.value || result.value.event.type!=='turn_finished') throw new Error('missing result');
    assert.equal(result.value.event.turnId,`run-${i}`);assert.equal(result.value.event.outcome,outcome);assert.equal(result.value.event.finalText,`text ${i}`);
    cursor=result.value.cursor; await it.return?.();
  }
  let notifySleep!: () => void;const sleeping=new Promise<void>(resolve => {notifySleep=resolve;});const sleep=f.clock.sleep;
  f.clock.sleep=(ms,signal) => {notifySleep();return sleep(ms,signal);};
  const abort=new AbortController(); const resumed=h.turns(cursor,abort.signal)[Symbol.asyncIterator](); const pending=resumed.next();
  // Reach the idle poll through the injected clock before aborting. A replayed
  // event resolves pending first and fails, without a race against a subprocess.
  await Promise.race([sleeping,pending.then(result => assert.equal(result.done,true))]);
  abort.abort(); assert.equal((await pending).done,true);
  assert.ok((await f.calls()).filter(x => x[3]==='t3_thread_read').some(x => JSON.parse(x[5]).textOffset===3));
});

test('pendingRequestCount > 0 → waiting_approval and one progress note',async t => {
  const f=await fixture(t);await f.update(s => {s.threads[0].thread.pendingRequestCount=2;}); const h=await f.live();
  assert.equal(h.state().kind,'waiting_approval');
  const a=await h.activity();await h.activity();
  assert.equal(a.openApprovals,2); assert.equal(a.turnRunning,true);assert.deepEqual(a.backgroundTasks,{known:0,observable:false});
  assert.deepEqual(f.events.filter(x => x.type==='progress'),[{type:'progress',turnId:'run-1',summary:'waiting for approval in T3'}]);
  await f.update(s => {s.threads[0].thread.pendingRequestCount=0;s.threads[0].thread.status='waiting';});await h.activity();assert.equal(h.state().kind,'waiting_approval');
  assert.equal(f.events.filter(x => x.type==='progress').length,1);
});

test('resuming an evicted run cursor delivers newer empty failed and cancelled runs exactly once',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='idle';e.thread.activeRunId=null;e.thread.latestRunId='old';
    e.recentRuns=[{runId:'old',ordinal:50,status:'completed',completedAt:time}];
    e.items=[{itemId:'old-final',position:10,runId:'old',createdBy:'agent',type:'assistant',text:'old result'}];
  });
  const first=(await f.live()).turns({value:JSON.stringify({p:0,r:'previous',o:49})})[Symbol.asyncIterator]();
  const old=await first.next();assert.ok(old.value);const cursor=old.value.cursor;await first.return?.();
  await f.update(s => {const e=s.threads[0];e.thread.latestRunId='cancelled';
    e.recentRuns=[{runId:'cancelled',ordinal:52,status:'cancelled',completedAt:time},{runId:'failed',ordinal:51,status:'failed',completedAt:time}];
    e.items=[];
  });
  // Recreate the backend: ordering must survive in the persisted cursor,
  // independently of message items or an in-memory session cache.
  const h=await f.live();const abort=new AbortController();
  f.clock.sleep=async () => {abort.abort();};
  const delivered=[];
  for await (const value of h.turns(cursor,abort.signal)) delivered.push(value);
  assert.deepEqual(delivered.map(x => x.event),[
    {type:'turn_finished',turnId:'failed',at:time,outcome:'failed',finalText:'',consumed:[]},
    {type:'turn_finished',turnId:'cancelled',at:time,outcome:'interrupted',finalText:'',consumed:[]},
  ]);
  assert.deepEqual(JSON.parse(delivered[1].cursor.value),{p:10,r:'cancelled',o:52});
  // Even a historical run carrying timeline items must not be replayed.
  await f.update(s => {const e=s.threads[0];e.recentRuns.push({runId:'historical',ordinal:49,status:'failed',completedAt:time});
    e.items.push({itemId:'historical-final',position:11,runId:'historical',createdBy:'agent',type:'assistant',text:'already delivered'});
    e.recentRuns=e.recentRuns.filter(x => x.runId!=='cancelled');
  });
  const resumeAbort=new AbortController();f.clock.sleep=async () => {resumeAbort.abort();};
  const replay=[];
  for await (const value of (await f.live()).turns(delivered[1].cursor,resumeAbort.signal)) replay.push(value);
  assert.deepEqual(replay,[]);
});

test('restart refuses a bounded run-history gap even when omitted finals survive timeline pagination',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='idle';e.thread.activeRunId=null;
    e.recentRuns[0].status='completed';
    e.items=[{itemId:'first',position:1,runId:'run-1',createdBy:'agent',type:'assistant',text:'first result'}];
  });
  const first=(await f.live()).turns()[Symbol.asyncIterator]();
  const delivered=await first.next();assert.ok(delivered.value);
  const cursor=delivered.value.cursor;const saved=cursor.value;await first.return?.();
  await f.update(s => {const e=s.threads[0];e.thread.latestRunId='run-52';
    e.recentRuns=Array.from({length:52},(_,i) => ({runId:`run-${i+1}`,ordinal:i+1,status:'completed',completedAt:time}));
    // The run-2 final survives beyond the first transcript page, but its item
    // cannot establish the run outcome (a failed run may also have final text).
    e.items.push(...Array.from({length:55},(_,i) => ({itemId:`tool-${i}`,position:i+2,runId:'run-2',createdBy:'agent',type:'tool_result',text:'tool'})),
      {itemId:'omitted-final',position:57,runId:'run-2',createdBy:'agent',type:'assistant',text:'surviving result'});
  });
  for (let restart=0;restart<2;restart++) {
    const resumed=(await f.live()).turns(cursor)[Symbol.asyncIterator]();
    await assert.rejects(resumed.next(),error => {
      assert.equal((error as Error).message,'t3-bridge history_gap');
      assert.equal((error as Error & {unreachable:boolean}).unreachable,true);return true;
    });
    assert.equal(cursor.value,saved,'a history gap must preserve the durable cursor');
  }
  const reads=(await f.calls()).filter(x => x[3]==='t3_thread_read').map(x => JSON.parse(x[5]));
  assert.ok(reads.some(x => x.afterPosition===51),'the surviving final must be read from a later page');
});

test('restart can deliver exactly 50 retained undelivered runs in order',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='idle';e.thread.activeRunId=null;e.thread.latestRunId='run-51';
    e.recentRuns=Array.from({length:51},(_,i) => ({runId:`run-${i+1}`,ordinal:i+1,status:'failed',completedAt:time}));e.items=[];
  });
  const it=(await f.live()).turns({value:JSON.stringify({p:1,r:'run-1',o:1})})[Symbol.asyncIterator]();
  for (let ordinal=2;ordinal<=51;ordinal++) {
    const value=await it.next();assert.ok(value.value);assert.equal(value.value.event.type,'turn_finished');
    assert.equal(value.value.event.turnId,`run-${ordinal}`);
    assert.deepEqual(JSON.parse(value.value.cursor.value),{p:1,r:`run-${ordinal}`,o:ordinal});
  }
  await it.return?.();
});

test('an initial read with evicted run history fails instead of starting past the gap',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='idle';e.thread.activeRunId=null;e.thread.latestRunId='run-51';
    e.recentRuns=Array.from({length:51},(_,i) => ({runId:`run-${i+1}`,ordinal:i+1,status:'failed',completedAt:time}));
  });
  await assert.rejects((await f.live()).turns()[Symbol.asyncIterator]().next(),/history_gap/);
});

test('a gap after a recoverable run preserves that run as the last delivered cursor',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='idle';e.thread.activeRunId=null;e.thread.latestRunId='run-4';
    e.recentRuns=[{runId:'run-2',ordinal:2,status:'failed',completedAt:time},{runId:'run-4',ordinal:4,status:'cancelled',completedAt:time}];
  });
  const it=(await f.live()).turns({value:JSON.stringify({p:1,r:'run-1',o:1})})[Symbol.asyncIterator]();
  const next=await it.next();assert.ok(next.value);
  assert.equal(next.value.event.turnId,'run-2');
  assert.deepEqual(JSON.parse(next.value.cursor.value),{p:1,r:'run-2',o:2});
  await assert.rejects(it.next(),/history_gap/);
  await assert.rejects((await f.live()).turns(next.value.cursor)[Symbol.asyncIterator]().next(),/history_gap/);
});

test('a legacy cursor recovers run order while its last run is still available',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='idle';e.thread.activeRunId=null;
    e.recentRuns=[{runId:'old',ordinal:50,status:'completed',completedAt:time},{runId:'new',ordinal:51,status:'failed',completedAt:time}];
  });
  const it=(await f.live()).turns({value:JSON.stringify({p:10,r:'old'})})[Symbol.asyncIterator]();
  const result=await it.next();assert.ok(result.value);
  assert.equal(result.value.event.turnId,'new');assert.deepEqual(JSON.parse(result.value.cursor.value),{p:10,r:'new',o:51});
  await it.return?.();
});

test('an evicted legacy cursor fails explicitly instead of guessing the delivered run order',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='idle';e.thread.activeRunId=null;
    e.recentRuns=[{runId:'new',ordinal:51,status:'failed',completedAt:time}];
  });
  const it=(await f.live()).turns({value:JSON.stringify({p:10,r:'old'})})[Symbol.asyncIterator]();
  await assert.rejects(it.next(),error => {
    assert.equal((error as Error).message,'t3-bridge cursor_run_unavailable');
    assert.equal((error as Error & {unreachable:boolean}).unreachable,true);return true;
  });
});

test('turn cursors reject invalid durable run ordinals',async t => {
  const f=await fixture(t);const h=await f.live();
  for (const o of [-1,0.5,'51',null,Number.MAX_SAFE_INTEGER+1]) {
    await assert.rejects(h.turns({value:JSON.stringify({p:10,r:'old',o})})[Symbol.asyncIterator]().next(),/invalid_t3_cursor/);
  }
  await assert.rejects(h.turns({value:JSON.stringify({p:10,r:null,o:51})})[Symbol.asyncIterator]().next(),/invalid_t3_cursor/);
});

test('capabilities presets exclude a disabled instance, a hidden model, a stale catalog, a runtime mode the provider lacks and an option value the model does not offer',async t => {
  const f=await fixture(t);const good=f.config.presets.good;
  f.config.presets={good,disabled:{...good,instance:'disabled'},hidden:{...good,model:'hidden'},stale:{...good,instance:'stale'},mode:{...good,instance:'limited',runtime_modes:['full-access']},option:{...good,options:[{id:'effort',value:'impossible'}]},missing:{...good,model:'absent'},readonly:{...good,runtime_modes:['read-only']}};
  await f.update(s => {const p=s.models[0];p.models.push({...p.models[0],id:'hidden',hidden:true});s.models.push({...p,providerInstanceId:'disabled',enabled:false},{...p,providerInstanceId:'stale',stale:true},{...p,providerInstanceId:'limited',runtimeModes:['auto']});});
  const b=await f.backend(); const c=await b.capabilities();
  assert.deepEqual(c,{steer:true,queue:'native',interrupt:true,approvals:'ui-only',questions:'text-only',resume:'transcript',backgroundTasks:'opaque',attachments:false,workspace:'backend-worktree',branchName:'settable',maxInputBytes:60000,permissionModes:['ask','auto','full-access'],presets:[{id:'good',provider:'provider',model:'model'}]});
  await assert.rejects(b.launch(spec({preset:'option'}),f.hooks),/preset/);
  await assert.rejects(b.launch(spec({permissionMode:'read-only'}),f.hooks),/permission_mode/);
});

test('health degraded when a preset route fails checkRoute; models --json failure → not ok; version t3-bridge',async t => {
  const f=await fixture(t);f.config.presets.good.route={base_url:'http://127.0.0.1:1234/v1'};f.config.presets.absent={...f.config.presets.good,model:'absent'};
  const b=await f.backend(); const health=await b.health();assert.equal(health.version,'t3-bridge');assert.equal(health.ok,true);
  assert.ok(health.degraded?.some(x => x.includes('good') && x.includes('route')));assert.ok(health.degraded?.some(x => x.includes('absent')));
  assert.ok((await f.calls()).some(x => x[3]==='t3_project_list'));
  f.env.FAKE_T3_FAIL='models';assert.equal((await b.health()).ok,false);
  f.env.FAKE_T3_FAIL='t3_project_list';assert.equal((await b.health()).ok,false);
  delete f.env.FAKE_T3_FAIL;await f.update(s => {s.models[0].stale=true;});assert.equal((await b.health()).ok,false);
});

test('reattach: settled or archived → none; server down → throws (not none)',async t => {
  const f=await fixture(t);const b=await f.backend(); const ref={backend:'t3',host:'test-host',id:'existing'};
  for (const key of ['settled','archived'] as const) {await f.update(s => {s.threads[0].thread.settled=false;s.threads[0].thread.archived=false;s.threads[0].thread[key]=true;});assert.deepEqual(await b.reattach(ref,f.hooks),{found:'none'});}
  await f.update(s => {s.threads[0].thread.archived=false;s.threads[0].thread.status='idle';s.threads[0].thread.activeRunId=null;});
  assert.equal((await b.reattach(ref,f.hooks)).found,'live');assert.deepEqual(await b.reattach({...ref,id:'absent'},f.hooks),{found:'none'});
  f.env.FAKE_T3_FAIL='t3_thread_read';await assert.rejects(b.reattach(ref,f.hooks),/server_down/);
});

test('reattach throws for generic HTTP 404 not_found',async t => {
  const f=await fixture(t);const b=await f.backend();
  f.env.FAKE_T3_FAIL='t3_thread_read';f.env.FAKE_T3_ERROR='t3-bridge: t3 HTTP 404: {"code":"not_found"}';
  await assert.rejects(b.reattach({backend:'t3',host:'test-host',id:'existing'},f.hooks),error => {
    assert.equal((error as Error & {unreachable:boolean}).unreachable,true);
    assert.match((error as Error).message,/HTTP 404/);return true;
  });
});

test('reattach throws for generic RPC not_found',async t => {
  const f=await fixture(t);const b=await f.backend();
  f.env.FAKE_T3_FAIL='t3_thread_read';f.env.FAKE_T3_ERROR='t3-bridge: RPC error: {"code":"not_found"}';
  await assert.rejects(b.reattach({backend:'t3',host:'test-host',id:'existing'},f.hooks),/RPC error/);
});

test('reattach returns none for explicit thread_not_found',async t => {
  const f=await fixture(t);const b=await f.backend();
  f.env.FAKE_T3_FAIL='t3_thread_read';f.env.FAKE_T3_ERROR='t3-bridge: {"code":"thread_not_found"}';
  assert.deepEqual(await b.reattach({backend:'t3',host:'test-host',id:'existing'},f.hooks),{found:'none'});
});

test('findByTag/listOwned follow nextCursor, pass settled false, and see only vl- titles',async t => {
  const f=await fixture(t);f.config.projects!.other={project_id:'other'};
  await f.update(s => {s.pageSize=1;s.threads=[thread('first'),thread('foreign','human vl-tag'),thread('third','vl-job_third task'),thread('other','vl-job_other task','other'),{...thread('settled'),thread:{...thread('settled').thread,settled:true}}];});
  const b=await f.backend();const refs=await b.listOwned();assert.deepEqual(refs.map(x => x.id),['first','third','other']);
  assert.equal((await b.findByTag('vl-job_third'))?.id,'third');assert.equal(await b.findByTag('vl-job'),null);
  const lists=(await f.calls()).filter(x => x[3]==='t3_thread_list').map(x => JSON.parse(x[5]));
  assert.ok(lists.some(x => x.cursor===1));for (const x of lists) {assert.equal(x.settled,false);assert.equal(x.titleContains,'vl-');assert.equal(x.limit,100);}
  assert.equal(refs[0].extra?.branch,'observed-branch');
});

test('settle interrupts a running thread then organizes settle',async t => {
  const f=await fixture(t);const h=await f.live();assert.equal(h.park,undefined);
  assert.deepEqual(await h.settle('requested'),{branch:'observed-branch',workspaceKept:true});
  const calls=(await f.calls()).filter(x => x[2]==='call').map(x => x[3]);
  const interruptIndex=calls.indexOf('t3_thread_interrupt'),organizeIndex=calls.indexOf('t3_thread_organize');
  assert.ok(interruptIndex>=0,'settle must interrupt the running thread');
  assert.ok(organizeIndex>=0,'settle must organize the thread');assert.ok(interruptIndex<organizeIndex);
  const entry=(await f.state()).threads[0];
  assert.equal(entry.thread.activeRunId,null);assert.equal(entry.recentRuns.find(x => x.runId==='run-1')?.status,'interrupted');
  assert.equal(entry.thread.settled,true);assert.equal(h.state().kind,'ended');
});

test('runtime_mode_escalation_denied surfaces as that error code',async t => {
  const f=await fixture(t);const h=await f.live();await f.update(s => {s.escalationDenied=true;});
  await assert.rejects(h.send(input(),'start'),{message:'runtime_mode_escalation_denied'});
});

test('exec failure surfaces the exit code and a redacted stderr line',async t => {
  const f=await fixture(t);const h=await f.live();f.env.FAKE_T3_FAIL='t3_thread_send';
  await assert.rejects(h.send(input(),'start'),error => {assert.equal((error as Error & {exitCode:number}).exitCode,1);assert.equal((error as Error).message,'server_down Bearer [redacted]');return true;});
});


test('omitted runtime_modes offers the live provider modes and a default auto preset can launch',async t => {
  const f=await fixture(t);delete f.config.presets.good.runtime_modes;
  await f.update(s => {s.models[0].runtimeModes=['auto'];});
  const b=await f.backend();assert.deepEqual((await b.capabilities()).permissionModes,['auto']);
  await b.launch(spec({permissionMode:'auto'}),f.hooks);
});

test('turn replay follows timeline pages and emits historical terminal runs once in ordinal order',async t => {
  const f=await fixture(t);
  await f.update(s => {const e=s.threads[0];e.thread.status='completed';e.thread.activeRunId=null;e.thread.latestRunId='run-2';
    e.recentRuns=[{runId:'run-2',ordinal:2,status:'completed',completedAt:time},{runId:'run-1',ordinal:1,status:'completed',completedAt:time}];
    e.items=Array.from({length:65},(_,i) => ({itemId:`item-${i}`,position:i+1,runId:i<55?'run-1':'run-2',createdBy:'agent',type:'assistant',text:`text-${i}`}));
  });
  const h=await f.live();const it=h.turns()[Symbol.asyncIterator]();
  for (const [id,text] of [['run-1','text-54'],['run-2','text-64']]) {const item=await it.next();assert.equal(item.value?.event.type,'turn_finished');if (item.value?.event.type!=='turn_finished') throw new Error('missing terminal run');assert.equal(item.value.event.turnId,id);assert.equal(item.value.event.finalText,text);}
  await it.return?.();
  const reads=(await f.calls()).filter(x => x[3]==='t3_thread_read').map(x => JSON.parse(x[5]));assert.ok(reads.some(x => x.afterPosition===50));
});


test('path-resolved projects remain discoverable after backend recreation with a partial project map',async t => {
  const f=await fixture(t);f.config.projects={other:{project_id:'other-project'}};
  await f.update(s => {s.threads=[];});
  const first=await f.backend();const h=await first.launch(spec(),f.hooks);
  const restarted=await f.backend();assert.equal((await restarted.findByTag('vl-job_test'))?.id,h.ref.id);
});

test('exec rejection cannot expose argv bodies in an exception',async t => {
  const f=await fixture(t);const h=await f.live();
  f.ctx.exec=async () => {throw new Error('Command failed: prompt-private-body Bearer raw-secret');};
  await assert.rejects(h.send(input(),'start'),{message:'t3-bridge execution_failed'});
});
