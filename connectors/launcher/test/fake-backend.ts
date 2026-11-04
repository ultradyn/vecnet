import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs';
import { dirname } from 'node:path';
import type { Clock } from '../../hub-client/src/ports.ts';
import type {
  Backend, BackendCapabilities, LaunchSpec, SessionRef, SessionState, SessionHooks,
  SessionHandle, TurnInput, SendMode, SendResult, TurnEvent, TurnCursor,
  ActivityReport, AgentQuestion, QuestionAnswer, PermissionRequest, PermissionDecision,
} from '../src/backend.ts';

export interface FakeBackendOptions { id?: string; caps?: Partial<BackendCapabilities>; stateFile?: string; clock?: Clock }
export interface FakeSession {
  ref: SessionRef;
  spec: LaunchSpec;
  state: SessionState;
  sent: {mode: SendMode; items: TurnInput['items']}[];
  interrupts: number;
  parks: number;
  settles: number;
}
type Finished = Extract<TurnEvent, {type:'turn_finished'}>;
interface StoredSession extends FakeSession {
  events: {cursor: TurnCursor; event: TurnEvent}[];
  turnCounter: number;
  lastActivityAt: string;
  activeConsumed: string[];
  queued: TurnInput['items'][];
  seen: Record<string, SendResult>;
  activityOverride: Partial<ActivityReport>;
}
export interface FakeBackend extends Backend {
  sessions(): FakeSession[];
  finishTurn(jobId: string, text: string, outcome?: Finished['outcome']): void;
  startTurn(jobId: string): void;
  ask(jobId: string, q: AgentQuestion): Promise<QuestionAnswer>;
  requestPermission(jobId: string, r: PermissionRequest): Promise<PermissionDecision>;
  setActivity(jobId: string, partial: Partial<ActivityReport>): void;
  exitProcess(jobId: string, resumable: boolean): void;
  lose(jobId: string): void;
  hideTagOnce(tag: string): void;
  failReattach(times: number): void;
  failNextLaunch(err: Error): void;
  throwAfterLaunch(err: Error): void;
}

export function createFakeBackend(opts: FakeBackendOptions = {}): FakeBackend {
  const id = opts.id ?? 'fake';
  const caps: BackendCapabilities = {
    steer:true,queue:'launcher',interrupt:true,approvals:'typed',questions:'typed',resume:'transcript',
    backgroundTasks:'observable',attachments:false,workspace:'backend-worktree',branchName:'settable',
    permissionModes:['read-only','ask','auto','full-access'],presets:[{id:'test',provider:'fake',model:'fake'}],...opts.caps,
  };
  // This fake schedules no wall-clock timers. An injected clock controls timestamps.
  const now = () => new Date(opts.clock?.now() ?? Date.now()).toISOString();
  const sessions = new Map<string, StoredSession>();
  const hooks = new Map<string, SessionHooks>();
  const waiters = new Map<string, Set<() => void>>();
  const hidden = new Set<string>();
  let unavailable = 0;
  let beforeLaunch: Error | undefined;
  let afterLaunch: Error | undefined;
  if (opts.stateFile && existsSync(opts.stateFile)) {
    for (const session of JSON.parse(readFileSync(opts.stateFile,'utf8')) as StoredSession[]) sessions.set(session.spec.jobId,session);
  }
  function persist() {
    if (!opts.stateFile) return;
    mkdirSync(dirname(opts.stateFile),{recursive:true});
    writeFileSync(`${opts.stateFile}.tmp`, JSON.stringify([...sessions.values()]), {mode:0o600});
    renameSync(`${opts.stateFile}.tmp`, opts.stateFile);
  }
  function session(jobId: string): StoredSession {
    const value = sessions.get(jobId);
    if (!value) throw new Error('fake session missing');
    return value;
  }
  function notify(s: StoredSession) { for (const wake of [...(waiters.get(s.spec.jobId) ?? [])]) wake(); }
  function emit(s: StoredSession, event: TurnEvent) {
    s.lastActivityAt = now();
    s.events.push({cursor:{value:String(s.events.length + 1)},event:structuredClone(event)});
    persist();
    hooks.get(s.spec.jobId)?.onEvent(structuredClone(event));
    notify(s);
  }
  function start(s: StoredSession, consumed: string[] = []) {
    if (s.state.kind === 'turn_running') return;
    if (s.state.kind === 'ended' && !s.state.resumable) throw new Error('fake session ended');
    const turnId = `turn-${++s.turnCounter}`;
    s.state = {kind:'turn_running',turnId,since:now()};
    s.activeConsumed = consumed;
    emit(s,{type:'turn_started',turnId,at:now(),consumed:[...consumed]});
  }
  function finish(s: StoredSession, text: string, outcome: Finished['outcome'] = 'completed') {
    if (s.state.kind !== 'turn_running') throw new Error('fake turn is not running');
    const turnId = s.state.turnId;
    const consumed = [...s.activeConsumed];
    s.activeConsumed = [];
    s.state = {kind:'idle',since:now()};
    emit(s,{type:'turn_finished',turnId,at:now(),outcome,finalText:text,consumed});
    const queued = s.queued.shift();
    if (queued) start(s,queued.map(item => item.deliveryId));
  }
  function checkReachable() { if (unavailable > 0) { unavailable--; throw new Error('fake backend unreachable'); } }
  function handle(s: StoredSession): SessionHandle {
    return {
      ref:structuredClone(s.ref),
      state: () => structuredClone(s.state),
      async send(input, mode) {
        if (s.state.kind === 'ended' && !s.state.resumable) throw new Error('fake session ended');
        const items = input.items.filter(item => !Object.hasOwn(s.seen,item.deliveryId));
        if (!items.length) return structuredClone(s.seen[input.items[0]?.deliveryId] ?? {mode});
        s.sent.push({mode,items:structuredClone(items)});
        if (mode === 'queue' && s.state.kind === 'turn_running') s.queued.push(structuredClone(items));
        else if (s.state.kind !== 'turn_running') start(s,items.map(item => item.deliveryId));
        else s.activeConsumed.push(...items.map(item => item.deliveryId));
        const result: SendResult = {mode,backendInputId:`input-${s.sent.length}`,...(s.state.kind === 'turn_running' ? {turnId:s.state.turnId} : {})};
        for (const item of items) s.seen[item.deliveryId] = result;
        persist(); return structuredClone(result);
      },
      async *turns(after, signal) {
        let index = Number(after?.value ?? 0);
        if (!Number.isSafeInteger(index) || index < 0 || index > s.events.length) throw new Error('invalid fake turn cursor');
        while (!signal?.aborted) {
          while (index < s.events.length) {
            if (signal?.aborted) return;
            yield structuredClone(s.events[index++]);
          }
          if (s.state.kind === 'ended') return;
          await new Promise<void>(resolve => {
            const pending = waiters.get(s.spec.jobId) ?? new Set<() => void>();
            function wake() { pending.delete(wake); signal?.removeEventListener('abort',wake); resolve(); }
            pending.add(wake); waiters.set(s.spec.jobId,pending);
            signal?.addEventListener('abort',wake,{once:true});
            if (signal?.aborted) wake();
          });
        }
      },
      async interrupt(_reason) { s.interrupts++; if (s.state.kind === 'turn_running') finish(s,'','interrupted'); else persist(); },
      async activity() {
        return structuredClone({turnRunning:s.state.kind === 'turn_running',openApprovals:0,queuedInputs:s.queued.length,
          backgroundTasks:{known:0,observable:caps.backgroundTasks === 'observable'},lastActivityAt:s.lastActivityAt,...s.activityOverride});
      },
      async park() { s.parks++; s.state = {kind:'parked',since:now()}; emit(s,{type:'state',state:s.state}); },
      async settle(_mode) {
        s.settles++; s.state = {kind:'ended',reason:'settled',resumable:false}; s.queued = [];
        emit(s,{type:'state',state:s.state});
        return {branch:s.spec.branch,workspaceKept:true,summary:'fake session settled'};
      },
    };
  }
  const backend: FakeBackend = {
    id,
    capabilities: async () => structuredClone(caps),
    health: async () => ({ok:true,version:'fake-1'}),
    async launch(spec, sessionHooks, signal) {
      signal?.throwIfAborted();
      if (beforeLaunch) { const err = beforeLaunch; beforeLaunch = undefined; throw err; }
      const previous = sessions.get(spec.jobId);
      if (previous) { hooks.set(spec.jobId,sessionHooks); return handle(previous); }
      const ref: SessionRef = {backend:id,host:'fake-host',id:spec.jobId,extra:{tag:spec.tag,branch:spec.branch,workspace:spec.project.repoRoot,link:`fake:${spec.jobId}`}};
      const s: StoredSession = {ref,spec:structuredClone(spec),state:{kind:'starting'},sent:[],interrupts:0,parks:0,settles:0,
        events:[],turnCounter:0,lastActivityAt:now(),activeConsumed:[],queued:[],seen:{},activityOverride:{}};
      sessions.set(spec.jobId,s); hooks.set(spec.jobId,sessionHooks); start(s);
      if (afterLaunch) { const err = afterLaunch; afterLaunch = undefined; throw err; }
      return handle(s);
    },
    async reattach(ref, sessionHooks) {
      checkReachable(); const s = sessions.get(ref.id);
      if (!s || s.ref.backend !== ref.backend || (s.state.kind === 'ended' && !s.state.resumable)) return {found:'none'};
      hooks.set(s.spec.jobId,sessionHooks);
      return {found:s.state.kind === 'parked' || s.state.kind === 'ended' ? 'parked' : 'live',handle:handle(s)};
    },
    async findByTag(tag) {
      checkReachable();
      if (hidden.delete(tag)) return null;
      const found = [...sessions.values()].find(s => s.ref.extra?.tag === tag);
      return found ? structuredClone(found.ref) : null;
    },
    async listOwned() { return [...sessions.values()].filter(s => s.state.kind !== 'ended' && String(s.ref.extra?.tag).startsWith('vl-')).map(s => structuredClone(s.ref)); },
    sessions: () => [...sessions.values()].map(({ref,spec,state,sent,interrupts,parks,settles}) => structuredClone({ref,spec,state,sent,interrupts,parks,settles})),
    finishTurn(jobId,text,outcome) { finish(session(jobId),text,outcome); },
    startTurn(jobId) { start(session(jobId)); },
    async ask(jobId,q) {
      session(jobId); const sessionHooks = hooks.get(jobId); if (!sessionHooks) throw new Error('fake session has no hooks');
      return sessionHooks.onQuestion(q,new AbortController().signal);
    },
    async requestPermission(jobId,r) {
      const s = session(jobId); const sessionHooks = hooks.get(jobId); if (!sessionHooks) throw new Error('fake session has no hooks');
      s.activityOverride.openApprovals = (s.activityOverride.openApprovals ?? 0) + 1; persist();
      try { return await sessionHooks.onPermissionRequest(r,new AbortController().signal); }
      finally { s.activityOverride.openApprovals!--; persist(); }
    },
    setActivity(jobId,partial) { Object.assign(session(jobId).activityOverride,structuredClone(partial)); persist(); },
    exitProcess(jobId,resumable) {
      const s = session(jobId); s.state = {kind:'ended',reason:'exited',resumable};
      emit(s,{type:'process_exited',at:now(),code:0,resumable});
    },
    lose(jobId) { const s = session(jobId); s.state = {kind:'ended',reason:'lost',resumable:false}; persist(); notify(s); sessions.delete(jobId); persist(); },
    hideTagOnce(tag) { hidden.add(tag); },
    failReattach(times) { unavailable = times; },
    failNextLaunch(err) { beforeLaunch = err; },
    throwAfterLaunch(err) { afterLaunch = err; },
  };
  return backend;
}
