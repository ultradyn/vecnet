import type { Clock, HubApi, Log } from '../../hub-client/src/ports.ts';
import type { PresenceBody } from '../../hub-client/src/wire.ts';
import type { PermissionMode, WorkspaceMode } from './backend.ts';

export interface PresenceSnapshot {
  running: number; open: number; max: number; perRequester: number;
  backends: Record<string, 'ok' | 'degraded' | 'down'>; modes: PermissionMode[]; presets: string[]; projects?: string[]; scratchProjects?: string[];
  backendModes?: Record<string, PermissionMode[]>;
  backendWorkspaces?: Record<string, WorkspaceMode[]>;
}

export function presenceBody(s: PresenceSnapshot, host: string): Omit<PresenceBody, 'at'> {
  const ids = Object.keys(s.backends);
  const allDown = ids.every(id => s.backends[id] === 'down');
  const state: PresenceBody['state'] = allDown || s.running >= s.max ? 'blocked' : s.open > 0 || s.running > 0 ? 'working' : 'idle';
  return {
    state,
    // Self-reported tags: requesters use them to choose, they authorise nothing (PROTOCOL 14.1).
    capabilities: [...ids.map(id => `launch:${id}`), ...s.presets.map(p => `preset:${p}`), `host:${host}`, ...s.modes.map(m => `mode:${m}`),
      ...ids.flatMap(id => (s.backendModes?.[id] ?? []).map(m => `mode:${id}/${m}`)),
      ...ids.flatMap(id => (s.backendWorkspaces?.[id] ?? []).map(m => `workspace:${id}/${m}`)), ...(s.projects ?? []).map(p => `project:${p}`), ...(s.scratchProjects ?? []).filter(p => s.projects?.includes(p)).map(p => `scratch:${p}`)],
    quota: { max_sessions: s.max, running: s.running, open: s.open, per_requester: s.perRequester, backends: { ...s.backends } },
  };
}

export function createPresence(deps: { hub: Pick<HubApi, 'presence'>; clock: Clock; log: Log; host: string; heartbeatMs: number;
  snapshot(): PresenceSnapshot }): { start(): void; changed(): void; stop(): Promise<void> } {
  const { clock, log } = deps;
  let timer: { cancel(): void } | undefined;
  let started = false, stopped = false;
  let last: string | undefined;
  let chain: Promise<void> = Promise.resolve();

  function post(force: boolean, offline = false): Promise<void> {
    const run = chain.then(async () => {
      let body: Omit<PresenceBody, 'at'>;
      try { body = offline ? { ...presenceBody(deps.snapshot(), deps.host), state: 'offline' } : presenceBody(deps.snapshot(), deps.host); }
      catch { if (!offline) { log.warn('presence snapshot failed'); return; } body = { state: 'offline' }; }
      const key = JSON.stringify(body);
      if (!force && key === last) return;
      try {
        await deps.hub.presence({ ...body, at: new Date(clock.now()).toISOString() });
        last = key;
      } catch {
        // Never log the error itself: a transport error can quote request data.
        last = undefined;
        log.warn('presence report failed; retrying on the next change or heartbeat');
      }
    });
    chain = run;
    return run;
  }
  function arm() {
    timer?.cancel();
    timer = clock.timer(deps.heartbeatMs, () => {
      timer = undefined;
      if (stopped) return;
      void post(true);
      arm();
    });
  }
  return {
    start() {
      if (started || stopped) return;
      started = true;
      void post(true);
      arm();
    },
    changed() { if (started && !stopped) void post(false); },
    async stop() {
      if (stopped) { await chain; return; }
      stopped = true;
      timer?.cancel(); timer = undefined;
      await post(true, true);
    },
  };
}
