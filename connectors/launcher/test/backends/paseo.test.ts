import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, mkdir, rm, access, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, delimiter, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { nodeCommand, symlink } from '../fixtures/platform.ts';
import { commandInvocation, envValue } from '../../../hub-client/src/platform.ts';
import plugin from '../../src/backends/paseo/index.ts';
import type { PluginContext, SessionHooks, LaunchSpec, TurnEvent, SessionHandle, TurnCursor } from '../../src/backend.ts';
const fixture = fileURLToPath(new URL('../fixtures/fake-paseo-bridge.mjs', import.meta.url));
async function run(argv: string[], env?: Record<string, string>, signal?: AbortSignal, timeout = 10_000, cwd?: string) {
  return new Promise<{code:number; stdout:string; stderr:string}>((resolve, reject) => {
    const child = spawn(argv[0], argv.slice(1), {env: {...process.env, ...env}, signal, timeout, cwd, windowsHide: true});
    let stdout = '', stderr = '';
    child.stdout.on('data', x => stdout += x); child.stderr.on('data', x => stderr += x);
    child.on('error', reject); child.on('close', code => resolve({code:code ?? 1, stdout, stderr}));
  });
}
async function harness(t: TestContext) {
  const root = await mkdtemp(join(tmpdir(), 'paseo-test-')); t.after(() => rm(root, {recursive:true, force:true}));
  const repo = join(root, 'repo'), ws = join(root, 'ws'); await mkdir(repo); await mkdir(ws);
  async function git(...args:string[]) { const r = await run(['git', '-C', repo, ...args]); assert.equal(r.code, 0, r.stderr); return r.stdout.trim(); }
  await git('init', '-b', 'main'); await git('config', 'user.name', 'Test'); await git('config', 'user.email', 'test@example.invalid');
  await writeFile(join(repo, 'file'), 'base'); await git('add', 'file'); await git('-c', 'commit.gpgsign=false', 'commit', '-m', 'base');
  const base = await git('rev-parse', 'HEAD');
  const stateFile = join(root, 'state.json'), logFile = join(root, 'log.jsonl');
  const state = {presets: [
    {name:'sol', provider_model:'codex/gpt', mode:'full-access'},
    {name:'claude', provider_model:'claude/opus', mode:'bypassPermissions'},
    {name:'muse', provider_model:'muse/muse', mode:'allowAll'},
    {name:'cursor', provider_model:'cursor/auto', mode:'auto_accept'},
    {name:'unknown', provider_model:'x/x', mode:null},
  ], agents:[] as Array<Record<string,unknown>>, waits:[] as Array<Record<string,unknown>>, reads:[] as string[][],
    doctor:{ok:true, checks:[] as Array<{name:string; ok:boolean; detail:string}>}, launch:undefined as Record<string,unknown>|undefined,
    ambiguousLaunch:undefined as 'null'|'malformed'|'nonzero'|undefined, sendCode:0};
  await writeFile(logFile, '');
  const saved = new Map<string,string>(), calls:Array<{argv:string[]; timeoutMs:number; env?:Record<string,string>}> = [], events:TurnEvent[] = [], logs:string[] = [];
  let failed = [] as string[], botActive = true;
  const ctx:PluginContext = {
    host:'test-host', redact:s => s.replaceAll('SECRET', '[redacted]'), now:() => new Date('2026-11-05T00:00:00Z'),
    store:{get:k=>saved.get(k), set:(k,v)=>{saved.set(k,v);}, delete:k=>{saved.delete(k);}, keys:(p='')=>[...saved.keys()].filter(k=>k.startsWith(p))},
    log:{info:s=>{logs.push(s);}, warn:s=>{logs.push(s);}, error:s=>{logs.push(s);}},
    checkRoute:async()=>({ok:true, models:['gpt']}), readSecretFile:async()=>{throw new Error('unexpected secret read');}, sleep:async()=>{throw new Error('unexpected sleep');},
    exec:async(argv, opts)=>{
      calls.push({argv, timeoutMs:opts.timeoutMs, env:opts.env});
      if (argv[0] === 'systemctl') {
        if (argv.includes('is-active')) return {code:botActive?0:3, stdout:botActive?'active\n':'inactive\n', stderr:''};
        return {code:failed.length?0:1, stdout:['paseo-bot.service','paseo-bridge-watch.service','paseo-cache-prune.service'].map(x=>failed.includes(x)?'failed':'active').join('\n'), stderr:''};
      }
      if (argv[0] === 'fake-paseo-bridge') return run([process.execPath, fixture, ...argv.slice(1)], {FAKE_PASEO_STATE:stateFile, FAKE_PASEO_LOG:logFile, ...opts.env}, opts.signal, opts.timeoutMs, opts.cwd);
      return run(argv, opts.env, opts.signal, opts.timeoutMs, opts.cwd);
    },
  };
  const config = {bridge_bin:'fake-paseo-bridge', workspace_root:ws, presets:Object.fromEntries(state.presets.map(p=>[p.name,{preset:p.name, service_tier:'priority'}])), wait_s:2};
  const hooks:SessionHooks = {onEvent:e=>events.push(e), onQuestion:async()=>{throw new Error('unexpected question');}, onPermissionRequest:async()=>{throw new Error('unexpected permission');}};
  const spec:LaunchSpec = {jobId:'job_test', tag:'vl-job_test', title:'test job', prompt:'the prompt', project:{alias:'test', repoRoot:repo, baseRef:'main'}, branch:'launch/job_test', preset:'sol', permissionMode:'full-access', attachments:[], env:{SAFE:'yes'}};
  async function save() { await writeFile(stateFile, JSON.stringify(state)); }
  async function entries() { return (await readFile(logFile,'utf8')).trim().split('\n').filter(Boolean).map(s=>JSON.parse(s) as {args:string[]; cwd:string; messageFile?:string; mode?:number; prompt?:string}); }
  async function create() { await save(); return plugin.create(config, ctx); }
  return {root, repo, ws, base, state, ctx, config, spec, hooks, calls, saved, events, logs, git, save, entries, create, healthUnits:(units:string[], active=true)=>{failed=units; botActive=active;}};
}
async function take(handle:SessionHandle, count:number, after?:TurnCursor) {
  const it = handle.turns(after)[Symbol.asyncIterator](); const out=[];
  try { for(let n=0;n<count;n++) { const item=await it.next(); assert.equal(item.done,false); out.push(item.value!); } } finally { await it.return?.(); }
  return out;
}
test('launch creates worktree launch/<job> from base_ref and passes --cwd <ws> --no-worktree and a 0600 message file that is removed', async t => {
  const h=await harness(t);
  await h.git('branch','selected-base',h.base);
  await writeFile(join(h.repo,'file'),'newer main'); await h.git('add','file');
  await h.git('-c','commit.gpgsign=false','commit','-m','advance main');
  assert.notEqual(await h.git('rev-parse','HEAD'),h.base);
  h.spec.project.baseRef='selected-base';
  const b=await h.create(), s=await b.launch(h.spec,h.hooks);
  const ws=await realpath(join(h.ws,h.spec.jobId)); assert.equal(s.ref.backend,'paseo'); assert.equal(s.ref.host,'test-host'); assert.equal(s.ref.extra?.workspace,ws); assert.equal(s.ref.extra?.branch,'launch/job_test'); assert.equal(s.ref.extra?.tag,h.spec.tag);
  assert.equal(await h.git('-C',ws,'rev-parse','HEAD'),h.base); assert.equal(await h.git('-C',ws,'branch','--show-current'),'launch/job_test');
  assert.equal(await readFile(join(ws,'file'),'utf8'),'base'); assert.equal(s.ref.extra?.base,h.base);
  const call=(await h.entries()).find(x=>x.args[1]==='launch')!; assert.ok(call.args.includes('--no-worktree')); assert.equal(call.args[call.args.indexOf('--cwd')+1],ws); assert.equal(call.args[call.args.indexOf('--service-tier')+1],'priority'); assert.ok(call.args[call.args.indexOf('--title')+1].startsWith('vl-')); if (process.platform !== 'win32') assert.equal(call.mode,0o600); assert.equal(call.prompt,'the prompt'); await assert.rejects(access(call.messageFile!));
  assert.ok(h.calls.find(x=>x.argv.includes('launch'))!.timeoutMs>=300_000); assert.equal(s.ref.extra?.preset,'sol'); assert.equal(s.ref.extra?.mode,'full-access');
});
test('launch with agent_id null fails; a returned preset different from the request settles and fails preset_changed', async t => {
  for(const result of [{agent_id:null,preset:'sol',mode:'full-access'},{agent_id:'a1',preset:'cursor-auto-fallback',mode:'auto_accept'},{agent_id:'a1',preset:'sol',mode:null}]) {
    const h=await harness(t); h.state.launch=result; const b=await h.create();
    await assert.rejects(b.launch(h.spec,h.hooks),result.agent_id?/preset_changed/:/launch_failed/);
    await access(join(h.ws,h.spec.jobId)); const calls=await h.entries(); const launch=calls.find(x=>x.args[1]==='launch')!; await assert.rejects(access(launch.messageFile!));
    if(result.agent_id) assert.equal(calls.filter(x=>x.args[1]==='settle').length,1,'preset mismatch settles exactly once');
  }
});
test('ambiguous launch discovers and settles only the newly created tagged agent and preserves work', async t => {
  for (const mode of ['null','malformed','nonzero'] as const) {
    const h=await harness(t); h.state.ambiguousLaunch=mode;
    const unrelated=[{id:'personal',name:'personal agent',status:'running',archived:false},
      {id:'other-job',name:'vl-job_test_other other job',status:'running',archived:false}];
    h.state.agents=structuredClone(unrelated);
    h.state.launch={agent_id:'lost-response-agent',preset:'sol',mode:'full-access'};
    const b=await h.create();
    await assert.rejects(b.launch(h.spec,h.hooks),mode==='null'?/launch_failed/:mode==='malformed'?/invalid_paseo_response/:/paseo_launch_failed/);
    const calls=await h.entries(), commands=calls.map(x=>x.args[1]);
    const launch=commands.indexOf('launch'), stop=commands.indexOf('stop'), wait=commands.indexOf('wait'), settle=commands.indexOf('settle');
    assert.ok(calls.slice(launch+1,stop).some(x=>x.args[1]==='ls' && !x.args.includes('--all')),'recover via live tag lookup');
    assert.ok(launch<stop && stop<wait && wait<settle,'discover, stop, wait and settle before launch rejects');
    for (const command of ['stop','wait','settle']) {
      const matching=calls.filter(x=>x.args[1]===command);
      assert.equal(matching.length,1); assert.equal(matching[0].args[2],'lost-response-agent');
    }
    assert.ok(!calls.some(x=>x.args.includes('--force')));
    const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
    assert.deepEqual(state.agents.filter((a:{id:string})=>a.id!=='lost-response-agent'),unrelated);
    assert.equal(state.agents.find((a:{id:string})=>a.id==='lost-response-agent').archived,true);
    const ws=join(h.ws,h.spec.jobId); await access(ws);
    assert.equal(await h.git('-C',ws,'rev-parse','HEAD'),h.base);
    assert.equal(await h.git('-C',ws,'branch','--show-current'),'launch/job_test');
    await assert.rejects(access(calls[launch].messageFile!));
  }
});
test('permissionModes map Paseo mode ids to full-access; a null-mode preset is not offered', async t => {
  const h=await harness(t), caps=await (await h.create()).capabilities();
  assert.deepEqual(caps.permissionModes,['full-access']); assert.deepEqual(caps.presets.map(x=>x.id),['sol','claude','muse','cursor']);
  assert.deepEqual({...caps, presets:[], permissionModes:[]},{steer:false,queue:'launcher',interrupt:true,approvals:'none',questions:'text-only',resume:'transcript',backgroundTasks:'opaque',attachments:false,workspace:'launcher-dir',branchName:'settable',presets:[],permissionModes:[]});
});
test('health: doctor exit 1 is parsed; a failed paseo-bridge-watch unit shows in degraded', async t => {
  const h=await harness(t); await rm(h.ws,{recursive:true}); h.state.doctor={ok:false, checks:[{name:'auth',ok:false,detail:'SECRET'}]}; h.healthUnits(['paseo-bridge-watch.service']); const b=await h.create(); const health=await b.health();
  assert.equal(health.ok,false); assert.ok(health.degraded?.some(x=>x.includes('auth'))); if (process.platform === 'linux') assert.ok(health.degraded?.some(x=>x.includes('paseo-bridge-watch.service'))); assert.ok(!JSON.stringify(health).includes('SECRET'));
  assert.equal((await h.entries()).find(x=>x.args[1]==='doctor')?.cwd,h.ws); assert.ok(h.calls.find(x=>x.argv.includes('doctor'))!.timeoutMs>=300_000);
  h.state.doctor={ok:true,checks:[]}; await h.save(); h.healthUnits([],false); assert.equal((await b.health()).ok,process.platform !== 'linux'); h.healthUnits([]); assert.equal((await b.health()).ok,true);
});
test('turns: a wait with inner idle yields one turn_finished with the best-effort final block and launch.final_text best_effort; timeout waits again; error → failed turn', async t => {
  const h=await harness(t); h.state.waits=[{agentId:'a1',status:'timeout'},{agentId:'a1',status:'permission'},{agentId:'a1',status:'permission'},{agentId:'a1',status:'idle'},{agentId:'a1',status:'error',message:'SECRET failure'}]; h.state.reads=[['[User] question','thinking','[Thought] hidden','[Tool] command','final answer','second line']]; const b=await h.create(), s=await b.launch(h.spec,h.hooks); const rows=await take(s,3);
  assert.equal(rows[0].event.type,'progress'); assert.match(JSON.stringify(rows[0].event),/waiting for permission in Paseo/);
  const e=rows[1].event; assert.equal(e.type,'turn_finished'); if(e.type!=='turn_finished') throw Error(); assert.equal(e.finalText,'final answer\nsecond line'); assert.equal(e.usage?.best_effort,1); assert.equal(e.outcome,'completed'); assert.deepEqual(e.consumed,[]); assert.equal(JSON.parse(rows[1].cursor.value).line,6); assert.match(JSON.parse(rows[1].cursor.value).last,/^[a-f0-9]{64}$/);
  const failure=rows[2].event; assert.equal(failure.type,'turn_finished'); if(failure.type!=='turn_finished') throw Error(); assert.equal(failure.outcome,'failed'); assert.equal(failure.finalText,'[redacted] failure');
  assert.equal((await h.entries()).filter(x=>x.args[1]==='wait').length,5); assert.ok(h.calls.filter(x=>x.argv.includes('wait')).every(x=>x.timeoutMs===32_000)); assert.equal(h.logs.length,1);
});
test('turns: merged lines and a re-read never yield the same turn twice (hash dedupe)', async t => {
  const h=await harness(t); h.state.waits=[{status:'idle'},{status:'idle'},{status:'error',message:'end'}]; h.state.reads=[['[User] x','same answer'],['same answer']]; const b=await h.create(), s=await b.launch(h.spec,h.hooks); const first=(await take(s,1))[0]; const rows=await take(s,1,first.cursor);
  assert.equal(rows[0].event.type,'turn_finished'); assert.equal('outcome' in rows[0].event && rows[0].event.outcome,'failed');
  const reads=(await h.entries()).filter(x=>x.args[1]==='read'); assert.equal(reads[1].args.at(-1),'2');
  for (const message of ['fallback', undefined]) {
    const fresh=await harness(t); fresh.state.waits=[{status:'idle',message}]; fresh.state.reads=[['No activity to display.']];
    const session=await (await fresh.create()).launch(fresh.spec,fresh.hooks), row=(await take(session,1))[0];
    assert.equal('finalText' in row.event && row.event.finalText,message ?? '(no text captured)');
  }
});
test('turns: an agentId handoff is adopted, the cursor resets, a note is emitted', async t => {
  const h=await harness(t); h.state.agents=[{id:'a2',name:'vl-job_test',status:'idle',archived:false}]; h.state.waits=[{agentId:'a2',status:'idle'}]; h.state.reads=[['new answer']]; const b=await h.create(), s=await b.launch(h.spec,h.hooks); const rows=await take(s,2,{value:JSON.stringify({line:99,last:'old'})});
  assert.equal(rows[0].event.type,'progress'); assert.equal(s.ref.id,'a2'); assert.equal((await h.entries()).find(x=>x.args[1]==='read')?.args.at(-1),'0'); assert.ok([...h.saved.values()].some(x=>x.includes('a2'))); assert.equal((await b.findByTag(h.spec.tag))?.id,'a2');
});
test('distinct raw completions that redact identically finish both turns and dedupe replay', async t => {
  const h=await harness(t);
  h.ctx.redact=s=>s.replace(/https:\/\/example\.invalid\/\S+/g,'https://example.invalid/[redacted]');
  h.state.waits=[{status:'idle'},{status:'idle'},{status:'idle'},{status:'error',message:'sentinel'}];
  h.state.reads=[['See https://example.invalid/first'],['See https://example.invalid/second'],['See https://example.invalid/second']];
  const s=await (await h.create()).launch(h.spec,h.hooks), first=(await take(s,1))[0];
  await s.send({items:[{deliveryId:'next',messageId:'next',from:'peer',text:'next turn',attachments:[]}]},'start');
  const second=(await take(s,1,first.cursor))[0];
  for (const row of [first,second]) {
    assert.equal(row.event.type,'turn_finished'); if(row.event.type!=='turn_finished') throw Error();
    assert.equal(row.event.outcome,'completed'); assert.equal(row.event.finalText,'See https://example.invalid/[redacted]');
  }
  assert.notEqual(JSON.parse(first.cursor.value).last,JSON.parse(second.cursor.value).last);
  assert.notEqual('turnId' in first.event && first.event.turnId,'turnId' in second.event && second.event.turnId);
  const replay=(await take(s,1,second.cursor))[0];
  assert.equal(replay.event.type,'turn_finished'); if(replay.event.type!=='turn_finished') throw Error();
  assert.equal(replay.event.outcome,'failed'); assert.equal(replay.event.finalText,'sentinel');
  assert.equal((await h.entries()).filter(x=>x.args[1]==='read').length,3);
});
test('send is at most once: a recorded batch is not resent after a restart', async t => {
  const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks);
  const input={items:['dlv_1','dlv_extra'].map(deliveryId=>({deliveryId,messageId:`msg_${deliveryId}`,from:'peer_1',text:'send me',attachments:[]}))};
  const batchId=createHash('sha256').update(JSON.stringify(input.items.map(i=>i.deliveryId))).digest('hex');
  let boundary:Map<string,string>|undefined;
  const original=h.ctx.exec; h.ctx.exec=async(argv,opts)=>{
    if(argv.includes('send')) {
      assert.equal(h.saved.get(`batch:${h.spec.tag}:${batchId}`),'attempted','specific batch persisted before dispatch');
      for (const item of input.items) assert.equal(h.saved.get(`delivery:${h.spec.tag}:${item.deliveryId}`),batchId,'each delivery persisted before dispatch');
      boundary=new Map(h.saved);
      await original(argv,opts); throw Error('simulated crash at dispatch');
    }
    return original(argv,opts);
  };
  await assert.rejects(s.send(input,'start'),/paseo_bridge_unreachable/); assert.ok(boundary);
  h.ctx.exec=original;
  // Restart with exactly the durable snapshot from before the bridge dispatch.
  const persisted=boundary, ctx={...h.ctx,store:{get:(k:string)=>persisted.get(k),set:(k:string,v:string)=>{persisted.set(k,v);},delete:(k:string)=>{persisted.delete(k);},keys:(p='')=>[...persisted.keys()].filter(k=>k.startsWith(p))}};
  const restarted=await plugin.create(h.config,ctx); const attached=await restarted.reattach(s.ref,h.hooks); assert.equal(attached.found,'live'); if(attached.found!=='live') throw Error(); await attached.handle.send(input,'start');
  assert.equal((await h.entries()).filter(x=>x.args[1]==='send').length,1);
  h.state.sendCode=1; h.state.agents=[{id:'a1',name:h.spec.tag,archived:false,status:'idle'}]; await h.save(); const failedInput={items:[{...input.items[0],deliveryId:'dlv_2'}]}; await assert.rejects(attached.handle.send(failedInput,'start')); await attached.handle.send(failedInput,'start'); assert.equal((await h.entries()).filter(x=>x.args[1]==='send').length,2);
});
test('settle stops, waits for idle, records head sha and commits, reports dirty work, never passes --force, keeps the workspace', async t => {
  const h=await harness(t); h.state.waits=[{agentId:'a1',status:'idle'}]; const b=await h.create(), s=await b.launch(h.spec,h.hooks), ws=join(h.ws,h.spec.jobId);
  await writeFile(join(ws,'file'),'commit'); await h.git('-C',ws,'add','file'); await h.git('-C',ws,'-c','commit.gpgsign=false','commit','-m','work'); const head=await h.git('-C',ws,'rev-parse','HEAD'); await writeFile(join(ws,'dirty'),'left');
  const result=await s.settle('requested'); assert.equal(result.branch,'launch/job_test'); assert.equal(result.headSha,head); assert.deepEqual(result.commits,[head]); assert.equal(result.workspaceKept,true); assert.match(result.summary!,/uncommitted changes left in/); await access(ws);
  const cmds=(await h.entries()).map(x=>x.args[1]); assert.ok(cmds.indexOf('stop')<cmds.indexOf('wait')); assert.ok(cmds.indexOf('wait')<cmds.indexOf('settle')); assert.ok(!(await h.entries()).some(x=>x.args.includes('--force'))); assert.ok(h.calls.find(x=>x.argv.includes('settle'))!.timeoutMs>=300_000);
});
test('findByTag/listOwned read name and see only vl- names', async t => {
  const h=await harness(t); h.state.agents=[{id:'a1',name:'vl-job_test title',archived:false,cwd:join(h.ws,'job_test'),status:'idle'},{id:'a2',name:'unrelated',archived:false},{id:'a3',name:'vl-old',archived:true}]; const b=await h.create(); assert.deepEqual((await b.listOwned()).map(x=>x.id),['a1']); assert.equal((await b.findByTag('vl-job_test'))?.id,'a1'); assert.equal(await b.findByTag('unrelated'),null); assert.equal(await b.findByTag('vl-job'),null);
  assert.equal((await b.reattach({backend:'paseo',host:'test-host',id:'a3'},h.hooks)).found,'none'); assert.equal((await b.reattach({backend:'paseo',host:'test-host',id:'missing'},h.hooks)).found,'none');
  h.ctx.exec=async()=>{throw Error('unreachable');}; await assert.rejects(b.reattach({backend:'paseo',host:'test-host',id:'a1'},h.hooks),/unreachable/);
});
test('cleanupWorkspace refuses outside workspace_root, a dirty tree or a missing branch; removes a clean one', async t => {
  const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks), ws=join(h.ws,h.spec.jobId); assert.ok(b.cleanupWorkspace);
  const clean=b.cleanupWorkspace!.bind(b); assert.equal((await clean({...s.ref,extra:{...s.ref.extra,workspace:h.repo}})).removed,false);
  await symlink(h.repo,join(h.ws,'escape')); assert.equal((await clean({...s.ref,extra:{...s.ref.extra,workspace:join(h.ws,'escape')}})).removed,false);
  await writeFile(join(ws,'dirty'),'dirty'); assert.equal((await clean(s.ref)).removed,false); await rm(join(ws,'dirty'));
  assert.equal((await clean({...s.ref,extra:{...s.ref.extra,branch:'launch/missing'}})).removed,false);
  assert.equal((await clean(s.ref)).removed,true); await assert.rejects(access(ws)); assert.equal(await h.git('rev-parse','launch/job_test'),h.base);
});

test('handoff survives restart before its reset cursor is saved', async t => {
  const h=await harness(t); h.state.agents=[{id:'a2',name:h.spec.tag,status:'idle',archived:false}];
  h.state.waits=[{agentId:'a2',status:'idle'},{agentId:'a2',status:'idle'}]; h.state.reads=[['new answer']];
  const b=await h.create(), s=await b.launch(h.spec,h.hooks), oldRef=structuredClone(s.ref);
  const oldCursor={value:JSON.stringify({line:99,last:'old'})};
  const note=await take(s,1,oldCursor); assert.equal(note[0].event.type,'progress');
  const restarted=await plugin.create(h.config,h.ctx), attached=await restarted.reattach(oldRef,h.hooks);
  assert.equal(attached.found,'live'); if(attached.found!=='live') throw Error();
  const rows=await take(attached.handle,1,oldCursor); assert.equal(attached.handle.ref.id,'a2');
  assert.equal((await h.entries()).find(x=>x.args[1]==='read')?.args.at(-1),'0');
  assert.equal(rows[0].event.type,'turn_finished');
});

test('send dedupes deliveries even if a retry regroups or reorders a batch', async t => {
  const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks);
  const item=(id:string)=>({deliveryId:id,messageId:id,from:'peer',text:id,attachments:[]});
  await s.send({items:[item('d1'),item('d2')]},'start');
  await s.send({items:[item('d2'),item('d1')]},'start');
  await s.send({items:[item('d2'),item('d3')]},'start');
  const sends=(await h.entries()).filter(x=>x.args[1]==='send'); assert.equal(sends.length,2);
  assert.equal(sends[1].args.at(-1),'d3');
});

test('malformed wait output and exec failures never expose agent text in thrown errors', async t => {
  const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks);
  const original=h.ctx.exec;
  h.ctx.exec=async(argv,opts)=>argv.includes('wait')?{code:0,stdout:JSON.stringify({stdout:'SECRET malformed'}),stderr:''}:original(argv,opts);
  await assert.rejects(take(s,1),error=>error instanceof Error && !error.message.includes('SECRET'));
});

test('settle refuses a non-idle wait without archiving or removing work', async t => {
  const h=await harness(t); h.state.waits=[{agentId:'a1',status:'timeout'}]; const b=await h.create(), s=await b.launch(h.spec,h.hooks);
  await assert.rejects(s.settle('requested'),/paseo_settle_not_idle/);
  assert.ok(!(await h.entries()).some(x=>x.args[1]==='settle')); await access(join(h.ws,h.spec.jobId));
});

test('cleanup refuses a branch checked out in another worktree', async t => {
  const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks), elsewhere=join(h.root,'elsewhere');
  await h.git('worktree','add','--force',elsewhere,'launch/job_test');
  assert.equal((await b.cleanupWorkspace!(s.ref)).reason,'branch_checked_out_elsewhere');
  await access(join(h.ws,h.spec.jobId)); await access(elsewhere);
});

test('idle rereads with no new text emit no phantom completion and do not reset activity', async t => {
  const h=await harness(t); h.state.waits=[{status:'idle',message:'fallback'},{status:'idle',message:'fallback'},{status:'idle'}]; h.state.reads=[['answer'],[],['answer']];
  const s=await (await h.create()).launch(h.spec,h.hooks), abort=new AbortController(), original=h.ctx.exec;
  let waits=0;
  h.ctx.exec=async(argv,opts)=>{
    if(argv.includes('wait')) {
      waits++;
      if(waits===2) h.ctx.now=()=>new Date('2026-11-05T05:00:00Z');
      if(waits===4) {abort.abort(); return {code:0,stdout:JSON.stringify({stdout:JSON.stringify({status:'timeout'})}),stderr:''};}
    }
    return original(argv,opts);
  };
  const rows=[]; for await(const row of s.turns(undefined,abort.signal)) rows.push(row);
  assert.equal(rows.length,1); assert.equal((await s.activity()).lastActivityAt,'2026-11-05T00:00:00.000Z');
});

test('different completions A B A have distinct turn ids while replay keeps the same id', async t => {
  const h=await harness(t); h.state.waits=[{status:'idle'},{status:'idle'},{status:'idle'}]; h.state.reads=[['A'],['B'],['A']];
  const s=await (await h.create()).launch(h.spec,h.hooks), rows=await take(s,3);
  assert.equal(new Set(rows.map(x=>'turnId' in x.event && x.event.turnId)).size,3);
  const replayCursor=rows[1].cursor; h.state.waits=[{status:'idle'}]; h.state.reads=[['A']]; await h.save();
  const replay=(await take(s,1,replayCursor))[0];
  assert.equal('turnId' in replay.event && replay.event.turnId,'turnId' in rows[2].event && rows[2].event.turnId);
});

test('handoff cursor generations survive crashes before adoption and after equal-text completions', async t => {
  for (const crashBeforeAdopt of [true,false]) {
    const h=await harness(t); h.state.agents=[{id:'a2',name:h.spec.tag,status:'idle',archived:false}];
    h.state.waits=[{agentId:'a1',status:'idle'},{agentId:'a2',status:'idle'}]; h.state.reads=[['A'],['A']];
    const b=await h.create(), s=await b.launch(h.spec,h.hooks), first=(await take(s,1))[0];
    let ref=structuredClone(s.ref), cursor=first.cursor;
    if(crashBeforeAdopt) {
      const set=h.ctx.store.set;
      h.ctx.store.set=(key,value)=>{set(key,value); if(key.startsWith('cursor-reset:')) throw Error('crash before adoption');};
      await assert.rejects(take(s,1,cursor),/crash before adoption/); h.ctx.store.set=set;
    } else {
      const rows=await take(s,2,cursor); cursor=rows[1].cursor; ref=structuredClone(s.ref);
    }
    h.state.agents=[{id:'a1',name:h.spec.tag,status:'idle',archived:false},{id:'a2',name:h.spec.tag,status:'idle',archived:false}];
    h.state.waits=[{agentId:ref.id,status:'idle'},{agentId:ref.id,status:'error',message:'end'}]; h.state.reads=[['A']]; await h.save();
    const attached=await (await plugin.create(h.config,h.ctx)).reattach(ref,h.hooks); assert.equal(attached.found,'live'); if(attached.found!=='live') throw Error();
    const rows=await take(attached.handle,1,cursor), reads=(await h.entries()).filter(x=>x.args[1]==='read');
    assert.equal(reads.at(-1)?.args.at(-1),'1'); assert.equal('outcome' in rows[0].event && rows[0].event.outcome,'failed');
  }
});


test('failed turn with the preceding completed text is emitted from a saved cursor and dedupes replay', async t => {
  const h=await harness(t), message='agent a1 failed: same';
  h.state.waits=[{agentId:'a1',status:'idle'}]; h.state.reads=[[message]];
  const b=await h.create(), s=await b.launch(h.spec,h.hooks), first=(await take(s,1))[0];
  assert.equal(first.event.type,'turn_finished');
  if(first.event.type!=='turn_finished') throw Error();
  assert.equal(first.event.outcome,'completed'); assert.equal(first.event.finalText,message);
  await s.send({items:[{deliveryId:'next',messageId:'next',from:'peer',text:'next turn',attachments:[]}]},'start');
  const original=h.ctx.exec;
  // The real BridgeError writes a single JSON object to stderr, not stdout.
  h.ctx.exec=async(argv,opts)=>argv.includes('wait')
    ? {code:1,stdout:'',stderr:JSON.stringify({ok:false,error:message})} : original(argv,opts);
  const failure=(await take(s,1,first.cursor))[0];
  assert.equal(failure.event.type,'turn_finished');
  if(failure.event.type!=='turn_finished') throw Error();
  assert.equal(failure.event.outcome,'failed'); assert.equal(failure.event.finalText,message);
  assert.notEqual(failure.cursor.value,first.cursor.value); assert.notEqual(failure.event.turnId,first.event.turnId);
  // A restart and replay of the same failed boundary must not post it again.
  const restarted=await plugin.create(h.config,h.ctx), attached=await restarted.reattach(s.ref,h.hooks);
  assert.equal(attached.found,'live'); if(attached.found!=='live') throw Error();
  const iterator=attached.handle.turns(failure.cursor)[Symbol.asyncIterator]();
  assert.equal((await iterator.next()).done,true);
});

test('a second failure with the same text after fresh input is its own failed turn; a re-report without new input stays deduped', async t => {
  const h=await harness(t), message='agent a1 failed: same failure';
  h.state.waits=[{agentId:'a1',status:'idle'}]; h.state.reads=[['first answer']];
  const b=await h.create(), s=await b.launch(h.spec,h.hooks), first=(await take(s,1))[0];
  const original=h.ctx.exec;
  h.ctx.exec=async(argv,opts)=>argv.includes('wait')
    ? {code:1,stdout:'',stderr:JSON.stringify({ok:false,error:message})} : original(argv,opts);
  await s.send({items:[{deliveryId:'d2',messageId:'d2',from:'peer',text:'second turn',attachments:[]}]},'start');
  const fail1=(await take(s,1,first.cursor))[0];
  assert.equal(fail1.event.type,'turn_finished'); if(fail1.event.type!=='turn_finished') throw Error();
  assert.equal(fail1.event.outcome,'failed');
  // The bridge accepts another send to an error agent; the same error text is a new failure.
  await s.send({items:[{deliveryId:'d3',messageId:'d3',from:'peer',text:'third turn',attachments:[]}]},'start');
  const fail2=(await take(s,1,fail1.cursor))[0];
  assert.equal(fail2.event.type,'turn_finished'); if(fail2.event.type!=='turn_finished') throw Error();
  assert.equal(fail2.event.outcome,'failed'); assert.notEqual(fail2.event.turnId,fail1.event.turnId); assert.notEqual(fail2.cursor.value,fail1.cursor.value);
  // With no new input, the re-reported failure is the same one.
  const iterator=s.turns(fail2.cursor)[Symbol.asyncIterator]();
  assert.equal((await iterator.next()).done,true);
});

test('launch persistence failure after receiving the agent ID stops, waits and settles without losing work', async t => {
  const h=await harness(t), b=await h.create(), original=h.ctx.store.set;
  let faults=0;
  h.ctx.store.set=(key,value)=>{
    if(key===`ref:${h.spec.tag}` && JSON.parse(value).id) { faults++; throw Error('disk full'); }
    original(key,value);
  };
  await assert.rejects(b.launch(h.spec,h.hooks),/disk full/); assert.equal(faults,1);
  const calls=await h.entries(), commands=calls.map(x=>x.args[1]);
  assert.equal(commands.filter(x=>x==='settle').length,1,'known agent must be settled after persistence fails');
  assert.ok(commands.indexOf('launch')<commands.indexOf('stop'));
  assert.ok(commands.indexOf('stop')<commands.indexOf('wait'));
  assert.ok(commands.indexOf('wait')<commands.indexOf('settle'));
  for (const command of ['stop','wait','settle']) {
    const matching=calls.filter(x=>x.args[1]===command);
    assert.equal(matching.length,1); assert.equal(matching[0].args[2],'a1');
  }
  assert.ok(!calls.some(x=>x.args.includes('--force')));
  const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
  assert.equal(state.agents.find((a:{id:string})=>a.id==='a1').archived,true);
  const ws=join(h.ws,h.spec.jobId); await access(ws);
  assert.equal(await h.git('-C',ws,'rev-parse','HEAD'),h.base);
  assert.equal(await h.git('-C',ws,'branch','--show-current'),'launch/job_test');
  await assert.rejects(access(calls.find(x=>x.args[1]==='launch')!.messageFile!));
});

test('activity treats an error-status agent as inactive after a failed turn and on reattach', async t => {
  const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks);
  const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
  state.agents[0].status='error'; state.waits=[{agentId:'a1',status:'error',message:'failed'}];
  await writeFile(join(h.root,'state.json'),JSON.stringify(state));
  const failure=(await take(s,1))[0];
  assert.equal(failure.event.type,'turn_finished');
  if(failure.event.type!=='turn_finished') throw Error();
  assert.equal(failure.event.outcome,'failed'); assert.equal(s.state().kind,'idle');
  assert.equal((await s.activity()).turnRunning,false);
  const restarted=await plugin.create(h.config,h.ctx), attached=await restarted.reattach(s.ref,h.hooks);
  assert.equal(attached.found,'live'); if(attached.found!=='live') throw Error();
  assert.equal((await attached.handle.activity()).turnRunning,false);
  state.waits=[{agentId:'a1',status:'permission'}];
  await writeFile(join(h.root,'state.json'),JSON.stringify(state));
  await take(attached.handle,1);
  const activity=await attached.handle.activity();
  assert.equal(activity.turnRunning,false); assert.equal(activity.openApprovals,1);
  assert.deepEqual(activity.backgroundTasks,{known:0,observable:false});
  for (const status of ['running','unknown',undefined]) {
    state.agents[0].status=status; await writeFile(join(h.root,'state.json'),JSON.stringify(state));
    assert.equal((await attached.handle.activity()).turnRunning,true,'unknown states remain conservatively busy');
  }
});

test('failed turns with inactive error agents settle in every mode and retain dirty work without force', async t => {
  for (const mode of ['wrapup','requested','cancel','lost'] as const) {
    const h=await harness(t), b=await h.create(), launched=await b.launch(h.spec,h.hooks);
    const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
    state.agents[0].status='error'; state.waits=[{agentId:'a1',status:'error',message:'agent a1 failed: broken'}];
    await writeFile(join(h.root,'state.json'),JSON.stringify(state));
    const attached=await b.reattach(launched.ref,h.hooks);
    assert.equal(attached.found,'live'); if(attached.found!=='live') throw Error();
    const s=attached.handle, failure=(await take(s,1))[0];
    assert.equal(failure.event.type,'turn_finished'); if(failure.event.type!=='turn_finished') throw Error();
    assert.equal(failure.event.outcome,'failed'); assert.equal(failure.event.finalText,'agent a1 failed: broken');
    assert.equal((await s.activity()).turnRunning,false);
    if(mode==='cancel') await s.interrupt('cancel');
    const ws=join(h.ws,h.spec.jobId); await writeFile(join(ws,'dirty'),'retained failed-turn work');
    const result=await s.settle(mode);
    assert.equal(result.branch,'launch/job_test'); assert.equal(result.headSha,h.base);
    assert.deepEqual(result.commits,[]); assert.equal(result.workspaceKept,true);
    assert.match(result.summary!,/uncommitted changes left in/);
    assert.equal(await readFile(join(ws,'dirty'),'utf8'),'retained failed-turn work');
    assert.equal(await h.git('-C',ws,'branch','--show-current'),'launch/job_test');
    assert.equal(s.state().kind,'ended');
    const calls=await h.entries(); assert.equal(calls.filter(x=>x.args[1]==='settle').length,1);
    assert.ok(!calls.some(x=>x.args.includes('--force')));
    const settled=JSON.parse(await readFile(join(h.root,'state.json'),'utf8')).agents[0];
    assert.equal(settled.status,'error','stop cannot turn an inactive error into idle'); assert.equal(settled.archived,true);
  }
});

test('settle accepts an error after stop only with fresh inactive directory evidence', async t => {
  for (const status of ['error','running','unknown',undefined]) {
    const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks), original=h.ctx.exec;
    h.ctx.exec=async(argv,opts)=>{
      // A wait failure alone is insufficient evidence that the agent is inactive.
      if(argv.includes('wait')) return {code:1,stdout:'',stderr:JSON.stringify({ok:false,error:'agent a1 failed: stopped in error'})};
      const result=await original(argv,opts);
      if(argv.includes('stop')) {
        const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
        state.agents[0].status=status;
        await writeFile(join(h.root,'state.json'),JSON.stringify(state));
      }
      return result;
    };
    if(status==='error') {
      const result=await s.settle('requested'); assert.equal(result.workspaceKept,true);
    } else await assert.rejects(s.settle('requested'),/paseo_settle_not_idle/);
    const calls=await h.entries();
    assert.equal(calls.filter(x=>x.args[1]==='settle').length,status==='error'?1:0);
    assert.ok(!calls.some(x=>x.args.includes('--force'))); await access(join(h.ws,h.spec.jobId));
  }
});

test('failed-launch cleanup settles an inactive error agent without force and preserves its workspace', async t => {
  const h=await harness(t); h.state.launch={agent_id:'a1',preset:'cursor-auto-fallback',mode:'auto_accept'};
  const b=await h.create(), original=h.ctx.exec;
  h.ctx.exec=async(argv,opts)=>{
    const result=await original(argv,opts);
    if(argv.includes('launch')) {
      const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
      state.agents[0].status='error'; await writeFile(join(h.root,'state.json'),JSON.stringify(state));
    }
    return result;
  };
  await assert.rejects(b.launch(h.spec,h.hooks),/preset_changed/);
  const calls=await h.entries(); assert.equal(calls.filter(x=>x.args[1]==='settle').length,1);
  assert.ok(!calls.some(x=>x.args.includes('--force')));
  const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
  assert.equal(state.agents[0].archived,true); assert.equal(state.agents[0].status,'error');
  const ws=join(h.ws,h.spec.jobId); await access(ws);
  assert.equal(await h.git('-C',ws,'rev-parse','HEAD'),h.base);
  assert.equal(await h.git('-C',ws,'branch','--show-current'),'launch/job_test');
  await assert.rejects(access(calls.find(x=>x.args[1]==='launch')!.messageFile!));
});

test('inactive error settlement keeps the bridge final guard against an agent becoming active', async t => {
  const h=await harness(t), b=await h.create(), s=await b.launch(h.spec,h.hooks), original=h.ctx.exec;
  const state=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
  state.agents[0].status='error'; await writeFile(join(h.root,'state.json'),JSON.stringify(state));
  h.ctx.exec=async(argv,opts)=>{
    if(argv.includes('settle')) {
      const latest=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
      latest.agents[0].status='running'; await writeFile(join(h.root,'state.json'),JSON.stringify(latest));
    }
    return original(argv,opts);
  };
  await assert.rejects(s.settle('requested'),/paseo_settle_failed/);
  const latest=JSON.parse(await readFile(join(h.root,'state.json'),'utf8'));
  assert.equal(latest.agents[0].archived,false);
  assert.ok(!(await h.entries()).some(x=>x.args.includes('--force'))); await access(join(h.ws,h.spec.jobId));
});

test('two turns that end with identical text after separate sends both finish, and replay keeps the second turn id', async t => {
  const h=await harness(t); h.state.waits=[{status:'idle'},{status:'idle'}]; h.state.reads=[['[User] first','Done'],['[User] second','Done']];
  const b=await h.create(), s=await b.launch(h.spec,h.hooks);
  const first=(await take(s,1))[0];
  await s.send({items:[{deliveryId:'d2',messageId:'d2',from:'peer',text:'next',attachments:[]}]},'start');
  const second=(await take(s,1,first.cursor))[0];
  for (const row of [first,second]) { assert.equal(row.event.type,'turn_finished'); if(row.event.type!=='turn_finished') throw Error(); assert.equal(row.event.outcome,'completed'); assert.equal(row.event.finalText,'Done'); }
  assert.notEqual('turnId' in second.event && second.event.turnId,'turnId' in first.event && first.event.turnId);
  // A restart replays the same turn from the saved cursor with the same id.
  h.state.agents=[{id:s.ref.id,name:h.spec.tag,status:'idle',archived:false}]; h.state.waits=[{status:'idle'}]; h.state.reads=[['[User] second','Done']]; await h.save();
  const attached=await (await plugin.create(h.config,h.ctx)).reattach(s.ref,h.hooks); assert.equal(attached.found,'live'); if(attached.found!=='live') throw Error();
  const replay=(await take(attached.handle,1,first.cursor))[0];
  assert.equal('turnId' in replay.event && replay.event.turnId,'turnId' in second.event && second.event.turnId);
});
test('a launch that returned agent_id null leaves a worktree cleanupWorkspace can reclaim by the job tag alone', async t => {
  const h=await harness(t); h.state.launch={agent_id:null,preset:'sol',mode:'full-access'}; const b=await h.create();
  await assert.rejects(b.launch(h.spec,h.hooks),/launch_failed/);
  const ws=join(h.ws,h.spec.jobId); await access(ws);
  assert.deepEqual(await b.cleanupWorkspace!({backend:'paseo',host:'test-host',id:'',extra:{tag:h.spec.tag}}),{removed:true});
  await assert.rejects(access(ws)); assert.equal(await h.git('rev-parse','launch/job_test'),h.base);
  assert.deepEqual(await b.cleanupWorkspace!({backend:'paseo',host:'test-host',id:'',extra:{tag:'vl-job_other'}}),{removed:false,reason:'missing_workspace_metadata'});
});

test('launch through a configured root alias stores canonical provenance and preserves the prior bridge context', async t => {
  const h=await harness(t), alias=join(h.root,'configured-root');
  await symlink(h.ws,alias); h.config.workspace_root=alias;
  const root=await realpath(h.ws), workspace=join(root,h.spec.jobId);
  assert.equal(await realpath(alias),root); assert.notEqual(alias,root);
  const b=await h.create(), s=await b.launch(h.spec,h.hooks);
  assert.equal(h.config.workspace_root,alias,'canonical provenance does not rewrite the trusted configuration');
  assert.equal(s.ref.extra?.workspace,workspace);
  for(const key of [`ref:${h.spec.tag}`,`id:${s.ref.id}`]) assert.deepEqual(JSON.parse(h.saved.get(key)!),s.ref);
  const launch=(await h.entries()).find(x=>x.args[1]==='launch')!;
  assert.equal(launch.args[launch.args.indexOf('--cwd')+1],workspace);
  assert.equal(launch.cwd,process.cwd(),'the bridge keeps its prior execution context; --cwd selects the agent workspace');
  const add=h.calls.find(x=>x.argv[0]==='git' && x.argv.includes('worktree') && x.argv.includes('add'))!;
  assert.equal(add.argv.at(-2),workspace,'real Git creates the canonical path saved in the ref');
  await access(join(alias,h.spec.jobId,'file'));
  assert.deepEqual(await b.cleanupWorkspace!(s.ref),{removed:true});
  await assert.rejects(access(workspace)); assert.equal(await h.git('rev-parse','launch/job_test'),h.base);
});

test('cleanup through a configured root alias still refuses distinct roots, junction escapes, dirty trees, missing branches and other worktrees', async t => {
  const h=await harness(t), alias=join(h.root,'configured-root');
  await symlink(h.ws,alias); h.config.workspace_root=alias;
  const b=await h.create(), s=await b.launch(h.spec,h.hooks), workspace=await realpath(join(h.ws,h.spec.jobId));
  const clean=b.cleanupWorkspace!.bind(b);
  const distinct=join(h.root,'ws-other'), foreign=join(distinct,h.spec.jobId);
  await mkdir(foreign,{recursive:true}); await writeFile(join(foreign,'keep'),'unrelated work');
  assert.deepEqual(await clean({...s.ref,extra:{...s.ref.extra,workspace:await realpath(foreign)}}),{removed:false,reason:'outside_workspace_root'});
  const escape=join(h.ws,'escape'); await symlink(distinct,escape);
  assert.deepEqual(await clean({...s.ref,extra:{...s.ref.extra,workspace:join(escape,h.spec.jobId)}}),{removed:false,reason:'outside_workspace_root'});
  // Configured root resolution does not authorize arbitrary aliases in refs,
  // even an alias of this same owned workspace beneath the configured root.
  const insideAlias=join(h.ws,'owned-alias'); await symlink(workspace,insideAlias);
  for(const path of [join(alias,h.spec.jobId),insideAlias]) {
    assert.deepEqual(await clean({...s.ref,extra:{...s.ref.extra,workspace:path}}),{removed:false,reason:'outside_workspace_root'});
  }
  await access(join(workspace,'file')); assert.equal(await readFile(join(foreign,'keep'),'utf8'),'unrelated work');
  await writeFile(join(workspace,'dirty'),'owned dirty work');
  assert.deepEqual(await clean(s.ref),{removed:false,reason:'dirty_workspace'});
  assert.equal(await readFile(join(workspace,'dirty'),'utf8'),'owned dirty work'); await rm(join(workspace,'dirty'));
  assert.deepEqual(await clean({...s.ref,extra:{...s.ref.extra,branch:'launch/missing'}}),{removed:false,reason:'workspace_unverifiable'});
  const elsewhere=join(h.root,'elsewhere'); await h.git('worktree','add','--force',elsewhere,'launch/job_test');
  assert.deepEqual(await clean(s.ref),{removed:false,reason:'branch_checked_out_elsewhere'});
  await access(workspace); await access(elsewhere);
  await h.git('worktree','remove',elsewhere);
  await rm(alias); await symlink(distinct,alias);
  assert.deepEqual(await clean(s.ref),{removed:false,reason:'outside_workspace_root'},'retargeting the configured alias cannot authorize the old workspace');
  await access(workspace); await rm(alias); await symlink(h.ws,alias);
  assert.deepEqual(await clean(s.ref),{removed:true});
  assert.equal(await readFile(join(foreign,'keep'),'utf8'),'unrelated work');
});

test('a failed launch through a configured root alias preserves canonical provenance for exact job-tag cleanup only', async t => {
  const h=await harness(t), alias=join(h.root,'configured-root');
  await symlink(h.ws,alias); h.config.workspace_root=alias;
  h.state.launch={agent_id:null,preset:'sol',mode:'full-access'};
  const workspace=join(await realpath(h.ws),h.spec.jobId), b=await h.create();
  await assert.rejects(b.launch(h.spec,h.hooks),/launch_failed/);
  const stored=JSON.parse(h.saved.get(`ref:${h.spec.tag}`)!);
  assert.equal(stored.extra.workspace,workspace); assert.equal(stored.id,'');
  const launch=(await h.entries()).find(x=>x.args[1]==='launch')!;
  assert.equal(launch.args[launch.args.indexOf('--cwd')+1],workspace); assert.equal(launch.cwd,process.cwd());
  const ref={backend:'paseo',host:'test-host',id:''};
  for(const tag of ['vl-job_other',h.spec.tag+'_other',h.spec.tag.slice(0,-1)]) {
    assert.deepEqual(await b.cleanupWorkspace!({...ref,extra:{tag}}),{removed:false,reason:'missing_workspace_metadata'});
    await access(workspace);
  }
  assert.deepEqual(await b.cleanupWorkspace!({...ref,extra:{tag:h.spec.tag}}),{removed:true});
  await assert.rejects(access(workspace)); assert.equal(await h.git('rev-parse','launch/job_test'),h.base);
});

test('launch creates a missing configured root before resolving canonical workspace provenance', async t => {
  const h=await harness(t); await rm(h.ws,{recursive:true});
  const b=await h.create(), s=await b.launch(h.spec,h.hooks), workspace=join(await realpath(h.ws),h.spec.jobId);
  assert.equal(s.ref.extra?.workspace,workspace);
  assert.equal((await h.entries()).find(x=>x.args[1]==='launch')!.cwd,process.cwd());
  assert.deepEqual(await b.cleanupWorkspace!(s.ref),{removed:true});
});


for (const kind of ['relative', 'PATH'] as const) test(`launch preserves real ${kind} bridge executable resolution and its prior context`, async t => {
  const h=await harness(t), runDir=join(h.root,'run'), binDir=join(h.root,'bin');
  await mkdir(runDir); await mkdir(binDir);
  const priorContext=await realpath(runDir);
  const executable=nodeCommand(join(binDir,'paseo-bridge'), `import(${JSON.stringify(new URL('../fixtures/fake-paseo-bridge.mjs', import.meta.url).href)});`);
  h.config.bridge_bin=kind==='relative' ? '../bin/'+basename(executable) : basename(executable);
  const originalExec=h.ctx.exec;
  // Use the production invocation adapter and a real executable, without the
  // harness's absolute-Node bridge substitution or changing the test process cwd.
  h.ctx.exec=async (argv,opts) => {
    if (argv[0]==='git' || argv[0]==='systemctl') return originalExec(argv,opts);
    assert.equal(argv[0],h.config.bridge_bin);
    const cwd=opts.cwd ?? priorContext;
    const env={...process.env, FAKE_PASEO_STATE:join(h.root,'state.json'), FAKE_PASEO_LOG:join(h.root,'log.jsonl'), ...opts.env,
      PATH:binDir+delimiter+(envValue(process.env,'PATH') ?? '')};
    const command=commandInvocation(argv,env,cwd);
    return new Promise((resolve,reject) => {
      const child=spawn(command.file,command.args,{...command,env,cwd,signal:opts.signal,timeout:opts.timeoutMs,windowsHide:true});
      let stdout='', stderr='';
      child.stdout.on('data',x=>stdout+=x); child.stderr.on('data',x=>stderr+=x);
      child.on('error',error=>{
        t.diagnostic(`bridge=${kind} operation=${argv[2]} errno=${(error as NodeJS.ErrnoException).code ?? 'unknown'} stdoutBytes=${Buffer.byteLength(stdout)} stderrBytes=${Buffer.byteLength(stderr)}`);
        reject(new Error('test bridge spawn failed'));
      });
      child.on('close',code=>resolve({code:code ?? 1,stdout,stderr}));
      child.stdin.end(opts.stdin ?? '');
    });
  };
  const b=await h.create();
  assert.ok((await b.capabilities()).presets.some(p=>p.id==='sol'),'configured executable works before launch');
  assert.equal((await b.health()).ok,true,'doctor resolves the sibling executable from the configured root');
  const s=await b.launch(h.spec,h.hooks), workspace=await realpath(join(h.ws,h.spec.jobId));
  const launch=(await h.entries()).find(x=>x.args[1]==='launch')!;
  assert.equal(launch.cwd,priorContext,'launch retains the bridge execution context');
  assert.equal(launch.args[launch.args.indexOf('--cwd')+1],workspace);
  assert.equal(s.ref.extra?.workspace,workspace);
  for(const key of [`ref:${h.spec.tag}`,`id:${s.ref.id}`]) assert.deepEqual(JSON.parse(h.saved.get(key)!),s.ref);
  assert.equal(await h.git('-C',workspace,'branch','--show-current'),'launch/job_test');
  assert.equal(await h.git('-C',workspace,'rev-parse','HEAD'),h.base);
  await s.settle('requested');
  assert.deepEqual(await b.cleanupWorkspace!(s.ref),{removed:true});
  await assert.rejects(access(workspace)); await assert.rejects(access(launch.messageFile!));
});
