import { definePlugin } from '../../backend.ts';
import type { Backend, BackendCapabilities, BackendHealth, LaunchSpec, PermissionMode, PluginContext, SessionHandle,
  SessionHooks, SessionRef, SessionState, TurnCursor, TurnInput, SendMode, SendResult, ActivityReport, SettleMode, SettleResult, TurnEvent } from '../../backend.ts';
import { createHash } from 'node:crypto';
import { resolve } from 'node:path';
import { Bridge, BridgeError } from './bridge.ts';
import type { T3Context } from './bridge.ts';
import { offeredModes, runtimeMode, optionAllowed, outcome, stateOf, refOf, parseCursor } from './map.ts';
import type { Provider, Snapshot, T3Thread, T3Item, T3Run } from './map.ts';

export interface T3Preset {
  instance: string; model: string; runtime_modes?: PermissionMode[];
  options: Array<{id: string; value: string}>;
  route?: {base_url: string; key_file?: string};
}
/** The auto token cannot send to full-access threads. Allowlisting caller in
 * t3-bridge full_access.callers routes ALL its calls through token-full. This
 * plugin reports runtime_mode_escalation_denied and never changes that policy. */
export interface T3Config {
  bridge_bin: string; caller: string; presets: Record<string, T3Preset>;
  projects?: Record<string, {project_id: string}>; poll_ms?: number; wait_ms?: number;
}
// Optional lane hook until TurnInput grows the stored batch id. Old callers
// retain stable dedupe by hashing their ordered delivery ids.
export type T3TurnInput = TurnInput & {batchId?: string};
interface Project { id: string; cwd: string | null }

class T3Session implements SessionHandle {
  readonly ref: SessionRef;
  private readonly bridge: Bridge;
  private readonly ctx: PluginContext;
  private readonly config: T3Config;
  private readonly hooks: SessionHooks;
  private snapshot: Snapshot;
  private waiting = false;
  constructor(ref: SessionRef, snapshot: Snapshot, bridge: Bridge, ctx: T3Context, config: T3Config, hooks: SessionHooks) {
    this.ref=ref; this.snapshot=snapshot; this.bridge=bridge; this.ctx=ctx; this.config=config; this.hooks=hooks;
    this.update(snapshot);
  }
  private update(snapshot: Snapshot): void {
    this.snapshot=snapshot;
    const state=this.state();
    if (state.kind === 'waiting_approval' && !this.waiting) {
      this.hooks.onEvent({type:'progress',turnId:snapshot.thread.activeRunId ?? snapshot.thread.latestRunId ?? '',summary:'waiting for approval in T3'});
    }
    this.waiting=state.kind === 'waiting_approval';
  }
  state(): SessionState { return stateOf(this.snapshot.thread,this.ctx.now().toISOString()); }
  private async read(afterPosition = 0, signal?: AbortSignal): Promise<Snapshot> {
    const snapshot=await this.bridge.call<Snapshot>('t3_thread_read',{threadId:this.ref.id,view:'messages',afterPosition,limit:50},signal);
    this.update(snapshot); return snapshot;
  }
  async send(input: T3TurnInput, mode: SendMode): Promise<SendResult> {
    const message=input.items.map(x => x.text).join('\n\n');
    if (Buffer.byteLength(message,'utf8') > 60_000) throw new Error('input_too_large');
    const batchId=input.batchId ?? createHash('sha256').update(JSON.stringify(input.items.map(x => x.deliveryId))).digest('hex');
    if (!batchId || input.items.length === 0) throw new Error('invalid_batch');
    const response=await this.bridge.call<{messageId?: string; runId?: string}>('t3_thread_send',{
      threadId:this.ref.id,message,mode:mode === 'start' ? 'auto' : mode,clientRequestId:`vl-${batchId}`,
    });
    // Refresh state through read: send's status can describe queued work.
    await this.read();
    return {mode,backendInputId:response.messageId,turnId:response.runId};
  }
  private async hydrate(item: T3Item, signal?: AbortSignal): Promise<string> {
    let text=item.text ?? '';
    let offset=item.nextTextOffset;
    if (item.textTruncated && offset == null) throw new Error('t3-bridge missing_text_offset');
    while (offset != null) {
      const page=await this.bridge.call<Snapshot>('t3_thread_read',{threadId:this.ref.id,itemId:item.itemId,textOffset:offset},signal);
      const chunk=page.items.find(x => x.itemId === item.itemId);
      if (!chunk || (chunk.nextTextOffset != null && chunk.nextTextOffset <= offset)) throw new Error('t3-bridge invalid_text_page');
      text+=chunk.text ?? '';
      offset=chunk.nextTextOffset;
      if (chunk.textTruncated && offset == null) throw new Error('t3-bridge missing_text_offset');
    }
    return text;
  }
  async *turns(after?: TurnCursor, signal?: AbortSignal): AsyncIterable<{cursor:TurnCursor; event:TurnEvent}> {
    let cursor=parseCursor(after?.value);
    try {
      while (!signal?.aborted) {
        let page=await this.read(cursor.p,signal);
        const items=[...page.items];
        const runs=new Map<string,T3Run>(page.recentRuns.map(x => [x.runId,x]));
        while (page.hasMore) {
          const next=page.nextPosition;
          if (next == null || next <= (items.at(-1)?.position ?? cursor.p) && page.items.length === 0) throw new Error('t3-bridge invalid_page');
          const previous=page.nextPosition;
          page=await this.read(next,signal);
          if (page.hasMore && (page.nextPosition ?? 0) <= (previous ?? 0)) throw new Error('t3-bridge invalid_page');
          items.push(...page.items);
          for (const run of page.recentRuns) runs.set(run.runId,run);
        }
        const ordered=[...runs.values()].sort((a,b) => a.ordinal-b.ordinal);
        const last=ordered.find(x => x.runId === cursor.r);
        // Upgrade a legacy {p,r} cursor while its run remains available. If
        // already evicted, neither empty items nor opaque run ids prove age;
        // fail explicitly rather than guessing which results were delivered.
        if (cursor.r && cursor.o === undefined) {
          if (!last) throw new BridgeError(-1,'t3-bridge cursor_run_unavailable');
          cursor={...cursor,o:last.ordinal};
        }
        for (const run of ordered) {
          if (run.runId === cursor.r || (cursor.o !== undefined && run.ordinal <= cursor.o)) continue;
          // recentRuns retains at most 50 runs independently of transcript
          // pagination. Items can survive eviction, but cannot prove a run's
          // terminal outcome (or reveal an empty failed/cancelled run). The
          // supported read API cannot page run history. Require consecutive
          // ordinals before advancing; an unrecoverable gap is unreachable,
          // leaving the last yielded cursor intact for retry/operator recovery.
          if (run.ordinal !== (cursor.o ?? 0)+1) throw new BridgeError(-1,'t3-bridge history_gap');
          const result=outcome(run.status);
          // A later terminal run must not consume a still-open earlier run.
          if (!result) break;
          const own=items.filter(x => x.runId === run.runId);
          const final=own.filter(x => x.createdBy === 'agent' && /assistant/i.test(x.type)).at(-1);
          const finalText=final ? await this.hydrate(final,signal) : '';
          cursor={p:own.reduce((p,item) => Math.max(p,item.position),cursor.p),r:run.runId,o:run.ordinal};
          yield {cursor:{value:JSON.stringify(cursor)},event:{type:'turn_finished',turnId:run.runId,
            at:run.completedAt ?? this.ctx.now().toISOString(),outcome:result,finalText,consumed:[]}};
          if (signal?.aborted) return;
        }
        if (this.state().kind === 'ended') return;
        const started=this.ctx.now().getTime();
        await this.bridge.call('t3_thread_wait',{threadId:this.ref.id,timeoutMs:this.config.wait_ms ?? 25_000},signal,(this.config.wait_ms ?? 25_000)+5_000);
        // Idle waits return immediately. Throttle those, but read completed
        // running work immediately so no result is held behind the poll timer.
        if (this.state().kind !== 'turn_running') {
          const remaining=(this.config.poll_ms ?? 3_000)-(this.ctx.now().getTime()-started);
          if (remaining > 0) await this.ctx.sleep(remaining,signal);
        }
      }
    } catch (error) { if (!signal?.aborted) throw error; }
  }
  async interrupt(reason: string): Promise<void> {
    await this.bridge.call('t3_thread_interrupt',{threadId:this.ref.id,reason,clientRequestId:`vl-interrupt-${createHash('sha256').update(this.ref.id+'\0'+(this.snapshot.thread.activeRunId ?? this.snapshot.thread.latestRunId ?? '')+'\0'+reason).digest('hex')}`});
    await this.read();
  }
  async activity(): Promise<ActivityReport> {
    const {thread}=await this.read();
    return {turnRunning:!!thread.activeRunId || this.state().kind === 'turn_running',openApprovals:thread.pendingRequestCount ?? 0,
      queuedInputs:thread.queuedInputCount ?? 0,backgroundTasks:{known:0,observable:false},lastActivityAt:thread.updatedAt ?? this.ctx.now().toISOString()};
  }
  async settle(mode: SettleMode): Promise<SettleResult> {
    const {thread}=await this.read();
    if (thread.activeRunId || this.state().kind === 'turn_running' || this.state().kind === 'waiting_approval') await this.interrupt(mode);
    await this.bridge.call('t3_thread_organize',{threadId:this.ref.id,action:'settle'});
    this.update({...this.snapshot,thread:{...this.snapshot.thread,settled:true}});
    return {branch:thread.branch ?? undefined,workspaceKept:true};
  }
}

class T3Backend implements Backend {
  readonly id='t3';
  private readonly config: T3Config;
  private readonly ctx: T3Context;
  private readonly bridge: Bridge;
  // Persist path-resolved aliases before launching so crash recovery can
  // discover a launch even when only some aliases have explicit T3 ids.
  private readonly resolvedProjects=new Set<string>();
  constructor(config: T3Config, ctx: T3Context) {
    this.config=config; this.ctx=ctx; this.bridge=new Bridge(config.bridge_bin,config.caller,ctx);
    for (const key of ctx.store.keys('resolved-project:')) {
      const id=ctx.store.get(key);
      if (id) this.resolvedProjects.add(id);
    }
  }
  private async catalog(signal?: AbortSignal): Promise<Provider[]> {
    const providers=await this.bridge.exec<Provider[]>(['models','--json'],signal);
    if (!Array.isArray(providers)) throw new Error('t3-bridge invalid_catalog');
    return providers;
  }
  private modes(preset: T3Preset, catalog: Provider[]): PermissionMode[] {
    const provider=catalog.find(x => x.providerInstanceId === preset.instance && x.enabled && x.source === 'live' && x.stale === false);
    const model=provider?.models.find(x => x.id === preset.model && !x.hidden);
    if (!provider || !model || !preset.options.every(x => optionAllowed(model.options,x.id,x.value))) return [];
    const requested=preset.runtime_modes ?? offeredModes.filter(x => provider.runtimeModes.includes(runtimeMode(x)));
    // A configured unsupported mode invalidates that preset, rather than
    // silently widening or changing the operator's requested contract.
    if (requested.length === 0 || requested.some(x => !offeredModes.includes(x) || !provider.runtimeModes.includes(runtimeMode(x)))) return [];
    return offeredModes.filter(x => requested.includes(x));
  }
  async capabilities(): Promise<BackendCapabilities> {
    const catalog=await this.catalog();
    const valid=Object.entries(this.config.presets).filter(([,p]) => this.modes(p,catalog).length > 0);
    return {steer:true,queue:'native',interrupt:true,approvals:'ui-only',questions:'text-only',resume:'transcript',backgroundTasks:'opaque',
      attachments:false,workspace:'backend-worktree',branchName:'settable',maxInputBytes:process.platform === 'win32' ? 5_000 : 60_000,
      permissionModes:offeredModes.filter(mode => valid.some(([,p]) => this.modes(p,catalog).includes(mode))),
      presets:valid.map(([id,p]) => ({id,provider:p.instance,model:p.model}))};
  }
  async health(signal?: AbortSignal): Promise<BackendHealth> {
    const degraded: string[]=[];
    let catalog: Provider[]=[]; let ok=true;
    try { catalog=await this.catalog(signal); if (!catalog.length || catalog.some(x => x.stale || x.source !== 'live')) ok=false; }
    catch { ok=false; degraded.push('models unavailable'); }
    try { await this.bridge.call('t3_project_list',{},signal); }
    catch { ok=false; degraded.push('projects unavailable'); }
    for (const [id,preset] of Object.entries(this.config.presets)) {
      if (this.modes(preset,catalog).length === 0) degraded.push(this.ctx.redact(`preset ${id} unavailable`));
      if (preset.route) {
        try { if (!(await this.ctx.checkRoute(preset.route,signal)).ok) degraded.push(this.ctx.redact(`preset ${id} route unavailable`)); }
        catch { degraded.push(this.ctx.redact(`preset ${id} route unavailable`)); }
      }
    }
    return {ok,version:'t3-bridge',...(degraded.length ? {degraded} : {})};
  }
  private read(id: string, signal?: AbortSignal): Promise<Snapshot> {
    return this.bridge.call('t3_thread_read',{threadId:id,view:'messages',afterPosition:0,limit:50},signal);
  }
  async launch(spec: LaunchSpec, hooks: SessionHooks, signal?: AbortSignal): Promise<SessionHandle> {
    if (!spec.title.startsWith(spec.tag) || !spec.tag.startsWith('vl-')) throw new Error('invalid_tag');
    const preset=this.config.presets[spec.preset];
    if (!preset) throw new Error('preset');
    if (spec.permissionMode === 'read-only') throw new Error('permission_mode');
    const modes=this.modes(preset,await this.catalog(signal));
    if (!modes.length) throw new Error('preset');
    if (!modes.includes(spec.permissionMode)) throw new Error('permission_mode');
    if (Buffer.byteLength(spec.prompt,'utf8') > 60_000) throw new Error('input_too_large');
    let projectId=this.config.projects?.[spec.project.alias]?.project_id;
    if (!projectId) {
      const projects=await this.bridge.exec<Project[]>(['projects','--json'],signal);
      projectId=projects.find(x => x.cwd && resolve(x.cwd) === resolve(spec.project.repoRoot))?.id;
    }
    if (!projectId) throw new Error('project_not_found');
    this.ctx.store.set(`resolved-project:${spec.project.alias}`,projectId);
    this.resolvedProjects.add(projectId);
    const launched=await this.bridge.call<{threadId: string}>('t3_thread_launch',{
      projectId,title:spec.title,message:spec.prompt,runtimeMode:runtimeMode(spec.permissionMode),
      modelSelection:{instanceId:preset.instance,model:preset.model,options:preset.options},
      workspaceStrategy:{type:'worktree',baseRef:spec.project.baseRef,branch:spec.branch,startFromOrigin:false},
    },signal);
    const snapshot=await this.read(launched.threadId,signal);
    return new T3Session(refOf(snapshot.thread,this.ctx.host,spec.tag),snapshot,this.bridge,this.ctx,this.config,hooks);
  }
  async reattach(ref: SessionRef, hooks: SessionHooks) {
    if (ref.backend !== 't3' || ref.host !== this.ctx.host) throw new Error('session_ref_mismatch');
    let snapshot: Snapshot;
    try { snapshot=await this.read(ref.id); }
    catch (error) { if (error instanceof BridgeError && error.notFound) return {found:'none' as const}; throw error; }
    if (snapshot.thread.archived || snapshot.thread.settled) return {found:'none' as const};
    if (snapshot.thread.projectId) this.resolvedProjects.add(snapshot.thread.projectId);
    return {found:'live' as const,handle:new T3Session(refOf(snapshot.thread,this.ctx.host,ref.extra?.tag as string | undefined),snapshot,this.bridge,this.ctx,this.config,hooks)};
  }
  async listOwned(): Promise<SessionRef[]> {
    const ids=new Set([...Object.values(this.config.projects ?? {}).map(x => x.project_id),...this.resolvedProjects]);
    // After a crash path-resolved aliases have no in-memory mapping. Enumerate
    // registered projects so an ambiguous launch remains discoverable by tag.
    if (!this.config.projects || Object.keys(this.config.projects).length === 0) {
      for (const project of await this.bridge.exec<Project[]>(['projects','--json'])) ids.add(project.id);
    }
    const refs: SessionRef[]=[];
    for (const projectId of ids) {
      let cursor: number | null=null;
      const seen=new Set<number>();
      do {
        const page: {threads:T3Thread[]; nextCursor:number | null}=await this.bridge.call('t3_thread_list',{
          projectId,titleContains:'vl-',settled:false,limit:100,...(cursor == null ? {} : {cursor}),
        });
        for (const thread of page.threads) {
          if (!thread.title.startsWith('vl-') || thread.settled || thread.archived) continue;
          // List records do not contain workspace binding; hydrate through read.
          const snapshot=await this.read(thread.threadId);
          if (!snapshot.thread.settled && !snapshot.thread.archived) refs.push(refOf(snapshot.thread,this.ctx.host));
        }
        cursor=page.nextCursor;
        if (cursor != null) { if (seen.has(cursor)) throw new Error('t3-bridge invalid_list_cursor'); seen.add(cursor); }
      } while (cursor != null);
    }
    return refs;
  }
  async findByTag(tag: string): Promise<SessionRef | null> {
    if (!tag.startsWith('vl-')) return null;
    return (await this.listOwned()).find(x => x.extra?.tag === tag) ?? null;
  }
}

const presetSchema={type:'object',additionalProperties:false,required:['instance','model','options'],properties:{
  instance:{type:'string',minLength:1},model:{type:'string',minLength:1},runtime_modes:{type:'array',minItems:1,uniqueItems:true,items:{enum:['ask','auto','full-access']}},
  options:{type:'array',items:{type:'object',additionalProperties:false,required:['id','value'],properties:{id:{type:'string'},value:{type:'string'}}}},
  route:{type:'object',additionalProperties:false,required:['base_url'],properties:{base_url:{type:'string'},key_file:{type:'string'}}},
}};
export default definePlugin<T3Config>({
  id:'t3',configSchema:{type:'object',additionalProperties:false,required:['bridge_bin','caller','presets'],properties:{
    bridge_bin:{type:'string',minLength:1},caller:{type:'string',minLength:1},presets:{type:'object',additionalProperties:presetSchema},
    projects:{type:'object',additionalProperties:{type:'object',additionalProperties:false,required:['project_id'],properties:{project_id:{type:'string',minLength:1}}}},
    poll_ms:{type:'integer',minimum:1},wait_ms:{type:'integer',minimum:1},
  }},
  create:async (config,ctx) => new T3Backend(config,ctx),
});
