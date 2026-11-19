import { mkdir } from 'node:fs/promises';
import { homedir } from 'node:os';
import { isAbsolute, join } from 'node:path';
import { createHubClient } from '../../hub-client/src/client.ts';
import { HubError } from '../../hub-client/src/errors.ts';
import { ID } from '../../hub-client/src/ids.ts';
import type { Clock, EventStream, HubApi, Log } from '../../hub-client/src/ports.ts';
import { createEventStream } from '../../hub-client/src/stream.ts';
import type { SendRequest } from '../../hub-client/src/wire.ts';
import type { Backend, BackendCapabilities, BackendHealth, PermissionMode, SessionHandle, SettleResult } from './backend.ts';
import { createClaudeStatus, spawnClaudeSnapshot } from './delivery/claude-status.ts';
import { startDeliveryHost } from './delivery/host.ts';
import { ConfigError, parseDuration } from './config.ts';
import type { LauncherConfig } from './config.ts';
import { createDirectory } from './directory.ts';
import { createDispatcher } from './dispatch.ts';
import { createIdleClock } from './idle.ts';
import type { IdleInput } from './idle.ts';
import { createIngress } from './ingress.ts';
import { createJobReporter } from './jobs.ts';
import { startLaunch } from './launch.ts';
import type { LaunchDeps } from './launch.ts';
import { openLedger } from './ledger.ts';
import type { JobRow } from './ledger.ts';
import { createOutbox } from './outbox.ts';
import { createParentBridge } from './parent-bridge.ts';
import { loadBackends, pluginContext } from './plugins.ts';
import { routeApproval } from './policy.ts';
import { createPresence } from './presence.ts';
import type { PresenceSnapshot } from './presence.ts';
import { reattachAll, retryPending } from './reattach.ts';
import type { ReattachOutcome } from './reattach.ts';
import { redactingLog } from './redact.ts';
import { userHome } from '../../hub-client/src/platform.ts';
import { localPlatform, readPrivateFile } from './private-files.ts';
import { createLane } from './session.ts';
import type { SessionLane } from './session.ts';
import { sweep } from './sweep.ts';

/** A refusal whose message is a fixed, safe operator instruction (main prints it). */
export class DaemonError extends Error {
  readonly code: string; readonly exitCode: number;
  constructor(code: string, message: string, exitCode = 1) { super(message); this.name = 'DaemonError'; this.code = code; this.exitCode = exitCode; }
}
/** main's startup failure line: only the daemon's own fixed messages and hub error codes, never arbitrary error text. */
export function startFailure(err: unknown): { line: string; code: number } {
  if (err instanceof DaemonError) return { line: `launcher failed to start: ${err.message}`, code: err.exitCode };
  if (err instanceof ConfigError) return { line: `launcher failed to start: ${err.message}`, code: 2 };
  if (err instanceof HubError) return { line: `launcher failed to start: hub error ${err.status} ${/^[a-z_]{1,64}$/.test(err.code) ? err.code : 'unknown'}`, code: 1 };
  // Anything else can quote config or credential paths' contents.
  return { line: 'launcher failed to start', code: 1 };
}
/** Exit status of a stop on a durable recovery condition; the unit does not restart on it. */
export const RECOVERY_EXIT = 3;
const CURSOR_GAP_HELP = 'cursor_purged: the hub no longer holds events this launcher missed, so deliveries in that gap (launches, controls, inputs) were never admitted. ' +
  'Check the open job threads and any launches sent to this launcher while it was down, then start once with --accept-cursor-gap.';
/**
 * The event stream's 409 cursor_purged hook. The launcher keeps no thread
 * history it could reconcile from, so it never advances past the gap: it
 * records a durable recovery condition and rejects, which stops the daemon.
 */
export function cursorPurgedHook(kv: { set(k: string, v: string): void }, log: Log, clock: Clock): (a: { oldestCursor: string }) => Promise<void> {
  return async ({ oldestCursor }) => {
    kv.set('recovery', JSON.stringify({ reason: 'cursor_purged', oldest_cursor: oldestCursor, at: clock.now() }));
    log.error('event cursor purged; stopping until the operator accepts the gap', { oldest_cursor: oldestCursor });
    throw new DaemonError('cursor_purged', CURSOR_GAP_HELP, RECOVERY_EXIT);
  };
}

const RETRY_EVERY_MS = 30_000;
const HOURLY_MS = 3_600_000;
const RETIRE_GRACE_MS = 600_000;
const SWEEP_GRACE_MS = 86_400_000;
const PUSH_TIMEOUT_MS = 300_000;

/** The production Clock: the only place real timers are created. */
export const systemClock: Clock = {
  now: () => Date.now(),
  timer(ms, fn) { const id = setTimeout(fn, ms); return { cancel: () => clearTimeout(id) }; },
  sleep(ms, signal) {
    return new Promise<void>((resolve, reject) => {
      if (signal?.aborted) { reject(signal.reason); return; }
      const id = setTimeout(() => { signal?.removeEventListener('abort', abort); resolve(); }, ms);
      function abort() { clearTimeout(id); reject(signal?.reason); }
      signal?.addEventListener('abort', abort, { once: true });
    });
  },
};
/** JSON lines on stderr; callers pass bodies and secrets through redactingLog. */
export function stderrLog(): Log {
  const write = (level: string) => (msg: string, meta?: Record<string, unknown>) => {
    process.stderr.write(`${JSON.stringify({ level, msg, ...meta, at: new Date().toISOString() })}\n`);
  };
  return { info: write('info'), warn: write('warn'), error: write('error') };
}
/** The credential, from a regular file readable only by its owner (mode 0600). */
export async function readCredential(path: string): Promise<string> {
  let text: string;
  try { text = await readPrivateFile(path, { exactMode: 0o600 }); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') throw error;
    throw new DaemonError('credential_mode', process.platform === 'win32'
      ? 'credential must be an owner-private regular file (native CLI security check required)'
      : 'credential must be a regular file with mode 0600');
  }
  const credential = text.trim();
  if (!credential || /\s/.test(credential)) throw new DaemonError('credential_malformed', 'credential file is malformed');
  return credential;
}

/** GET /v1/peers/me (PROTOCOL 2.10): the identity this credential proves. */
async function whoAmI(hub: HubApi, cfg: LauncherConfig, credential: string | undefined, clock: Clock): Promise<string> {
  const me = (hub as HubApi & { me?: () => Promise<unknown> }).me;
  let body: unknown;
  if (typeof me === 'function') body = await me.call(hub);
  else {
    if (credential === undefined) throw new Error('no credential to identify the launcher peer');
    const deadline = new AbortController();
    const timer = clock.timer(30_000, () => deadline.abort());
    try {
      const response = await fetch(new URL('/v1/peers/me', cfg.hub.url), { headers: { Authorization: `Bearer ${credential}` }, redirect: 'error', signal: deadline.signal });
      if (!response.ok) throw new HubError(response.status, 'whoami_failed');
      body = await response.json();
    } catch (err) { throw err instanceof HubError ? err : new HubError(0, 'network_error'); }
    finally { timer.cancel(); }
  }
  const b = body as { kind?: unknown; peer_id?: unknown } | null;
  if (!b || b.kind !== 'peer' || typeof b.peer_id !== 'string' || !ID.peer.test(b.peer_id)) throw new DaemonError('not_a_peer', 'the credential must authenticate a launcher peer');
  return b.peer_id;
}

type Info = { caps: BackendCapabilities; health: BackendHealth };
const downHealth: BackendHealth = { ok: false, version: 'unknown', detail: { error: 'unreachable' } };
const noCaps: BackendCapabilities = { steer: false, queue: 'launcher', interrupt: false, approvals: 'none', questions: 'text-only', resume: 'none',
  backgroundTasks: 'opaque', attachments: false, workspace: 'launcher-dir', branchName: 'backend-chosen', permissionModes: [], presets: [] };

export async function startDaemon(o: { cfg: LauncherConfig; hub?: HubApi; stream?: EventStream; clock?: Clock; log?: Log;
  backends?: Map<string, Backend>; beforeRecordRef?: (jobId: string) => void;
  /** The operator has checked the gap left by a purged event cursor: resume from the hub's oldest cursor. */
  acceptCursorGap?: boolean;
  /** Environment for the Codex app-server connector (its bindings file, vecnet binary); default process.env. */
  env?: NodeJS.ProcessEnv }): Promise<{ stop(): Promise<void>; crash(): void;
  /** Settles once the daemon has stopped, including when it stops itself on a fatal event stream end. */
  stopped: Promise<void> }> {
  const { cfg } = o;
  const log = redactingLog(o.log ?? stderrLog());
  const base = o.clock ?? systemClock;
  // Every component gets a fenced clock: after stop() or crash() no callback fires.
  let dead = false;
  const timers = new Set<{ cancel(): void }>();
  const clock: Clock = {
    now: () => base.now(),
    sleep: (ms, signal) => base.sleep(ms, signal),
    timer(ms, fn) {
      if (dead) return { cancel() {} };
      const entry = base.timer(ms, () => { timers.delete(entry); if (!dead) fn(); });
      timers.add(entry);
      return { cancel() { timers.delete(entry); entry.cancel(); } };
    },
  };

  // 1. Config and credential (the credential is needed only for real hub I/O).
  const credential = !o.hub || !o.stream ? await readCredential(cfg.hub.credential_file) : undefined;
  const realHub = o.hub ?? createHubClient({ baseUrl: cfg.hub.url, credential: credential!, clock });
  // After a crash the hub is unreachable to this instance, as for a dead process.
  const hub: HubApi = {
    send: (b, s) => dead ? Promise.reject(new HubError(0, 'network_error')) : realHub.send(b, s),
    ack: (c, s) => dead ? Promise.resolve() : realHub.ack(c, s),
    receipt: (b, s) => dead ? Promise.reject(new HubError(0, 'network_error')) : realHub.receipt(b, s),
    presence: (b, s) => dead ? Promise.resolve() : realHub.presence(b, s),
    putBlob: (b, t, s) => realHub.putBlob(b, t, s),
    peers: s => realHub.peers(s),
    groups: s => realHub.groups(s),
    fetchBlob: (u, x) => realHub.fetchBlob(u, x),
  };

  // 2. The ledger.
  if (process.platform === 'win32') localPlatform('mkdir-private', cfg.state_dir, { env: { ...process.env, ...o.env } });
  else await mkdir(cfg.state_dir, { recursive: true, mode: 0o700 });
  const ledger = openLedger(join(cfg.state_dir, 'ledger.sqlite'), clock);
  const daemonKv = ledger.kv('daemon');
  try {
    // The ledger belongs to the identity the credential proves (PROTOCOL 2.10, 13.9).
    const self = await (async () => {
      const stored = daemonKv.get('self');
      let proven: string;
      try { proven = await whoAmI(realHub, cfg, credential, clock); }
      catch (err) {
        // The hub being unreachable or briefly failing (a deploy's 502, a 429)
        // keeps the pinned identity; a refused credential never does.
        if (stored && err instanceof HubError && err.retryable) return stored;
        throw err;
      }
      if (stored && stored !== proven) throw new DaemonError('identity_changed', 'identity_changed: the ledger belongs to another launcher peer');
      daemonKv.set('self', proven);
      return proven;
    })();
    // A purged event cursor left a gap no one has checked: refuse to resume past it.
    const recovery = daemonKv.get('recovery');
    if (recovery) {
      if (!o.acceptCursorGap) throw new DaemonError('cursor_purged', CURSOR_GAP_HELP, RECOVERY_EXIT);
      const oldest = (JSON.parse(recovery) as { oldest_cursor?: unknown }).oldest_cursor;
      ledger.tx(() => {
        if (typeof oldest === 'string' && oldest) ledger.setCursor(oldest);
        daemonKv.delete('recovery');
      });
      log.warn('operator accepted the event cursor gap; resuming from the oldest retained cursor');
    }

    // 3. Backends and their health.
    const backends = o.backends ?? await loadBackends(cfg, id => pluginContext({ host: cfg.host, log, ledger, id, clock }));
    // A lane takes its backend's capabilities once, when it is created, and
    // splits buffered input at that cap. Lanes for reattached jobs are created
    // before a slow backend may answer, so the last capabilities each backend
    // reported are kept in the ledger and stand in until it answers again. The
    // gate accepts a job only against known capabilities (its preset must be
    // offered), so every open job's backend has a stored set.
    const info = new Map<string, Info>();
    for (const id of backends.keys()) {
      const stored = daemonKv.get(`caps:${id}`);
      if (!stored) continue;
      try { info.set(id, { caps: JSON.parse(stored) as BackendCapabilities, health: downHealth }); }
      catch { log.warn('stored backend capabilities unreadable', { backend: id }); }
    }
    function learned(id: string, caps: BackendCapabilities, health: BackendHealth) {
      info.set(id, { caps, health });
      if (!dead) daemonKv.set(`caps:${id}`, JSON.stringify(caps));
    }
    async function refreshHealth(): Promise<void> {
      await Promise.all([...backends].map(async ([id, backend]) => {
        let caps: BackendCapabilities | undefined, health = downHealth;
        try { caps = await backend.capabilities(); } catch { log.warn('backend capabilities unavailable', { backend: id }); }
        try { health = await backend.health(); } catch { log.warn('backend health unavailable', { backend: id }); }
        if (dead) return;
        if (caps) learned(id, caps, health);
        else info.set(id, { caps: info.get(id)?.caps ?? noCaps, health });
      }));
    }
    await refreshHealth();

    // 4. Directory, outbox, reports, parent bridge.
    const dir = createDirectory({ hub, log });
    let directoryLoaded = false;
    try { await dir.refresh(); directoryLoaded = true; } catch { log.warn('directory unavailable at startup; refreshing on demand'); }
    const outbox = createOutbox({ ledger, hub, clock, log });
    const jobs = createJobReporter({ ledger, outbox });
    const home = (userHome(o.env ?? process.env) ?? homedir());
    const bridge = createParentBridge({ ledger, outbox, clock, log, cfg, self, home, dir, route: (p, job) => routeApproval(p, job, dir, cfg, home) });

    const lanes = new Map<string, SessionLane>();
    const parkable = new Map<string, boolean>();
    const attached = new Set<string>();
    const background = new Set<Promise<unknown>>();
    function detach<T>(p: Promise<T>, what: string): void {
      const tracked = p.then(() => {}, () => { if (!dead) log.warn(`${what} failed`); });
      background.add(tracked);
      void tracked.finally(() => background.delete(tracked));
    }
    let presence: ReturnType<typeof createPresence> | undefined;
    const changed = () => { if (!dead) presence?.changed(); };

    const projects = (job: JobRow) => {
      const p = Object.hasOwn(cfg.projects, job.project) ? cfg.projects[job.project] : undefined;
      if (!p) throw new Error('project alias is not configured');
      return { alias: job.project, repoRoot: p.repo_root, baseRef: job.input.base_ref ?? p.base_ref, ...(p.scratch === undefined ? {} : {scratch:p.scratch}) };
    };
    const launchDeps: LaunchDeps = {
      ledger, jobs, backends, clock, log, host: cfg.host, cfg, projects, beforeRecordRef: o.beforeRecordRef, kick: () => outbox.kick(),
      // The session's own workspace, read per prompt from the ledger: the
      // worktree is recorded only after launch returns. Until then no path is
      // claimed to be inside it (a placeholder that contains nothing real).
      hooks: job => bridge.hooksFor(job, () => ledger.job(job.jobId)?.workspace ?? join(cfg.state_dir, 'workspace-pending', job.jobId), ev => {
        if (ev.type === 'state' || ev.type === 'process_exited') changed();
        // An approval only the backend UI can answer gets a note to the requester.
        if (ev.type === 'progress') { const lane = lanes.get(job.jobId); if (lane) void lane.notice(ev).catch(() => {}); }
      }),
    };
    const exec = pluginContext({ host: cfg.host, log, ledger, id: 'daemon', clock }).exec;
    // The launcher, never the agent, pushes the launch branch, and only that branch.
    async function deliver(job: JobRow, r: SettleResult): Promise<{ pushed: boolean }> {
      const branch = r.branch ?? job.branch;
      if (branch !== `launch/${job.jobId}` || !job.workspace || !isAbsolute(job.workspace)) return { pushed: false };
      try {
        // The agent can edit the workspace's hooks and config: run none of them,
        // and push to the remote configured in the operator's own checkout.
        const project = Object.hasOwn(cfg.projects, job.project) ? cfg.projects[job.project] : undefined;
        if (!project) return { pushed: false };
        const remote = await exec(['git', '-C', project.repo_root, 'remote', 'get-url', '--push', 'origin'], { timeoutMs: 30_000 });
        const url = remote.stdout.trim();
        if (remote.code !== 0 || !url || /\s/.test(url) || url.startsWith('-') || /^ext::/i.test(url)) { log.warn('launch branch push skipped: no usable origin', { jobId: job.jobId }); return { pushed: false }; }
        const res = await exec(['git', '-c', 'core.hooksPath=/dev/null', '-c', 'core.sshCommand=ssh', '-c', 'core.fsmonitor=false',
          '-c', 'protocol.ext.allow=never', '-C', job.workspace,
          'push', '--no-verify', url, `refs/heads/${branch}:refs/heads/${branch}`], { timeoutMs: PUSH_TIMEOUT_MS, env: { GIT_CONFIG_NOSYSTEM: '1' } });
        if (res.code !== 0) log.warn('launch branch push failed', { jobId: job.jobId, code: res.code });
        return { pushed: res.code === 0 };
      } catch { log.warn('launch branch push failed', { jobId: job.jobId }); return { pushed: false }; }
    }

    // 5. The idle clock.
    const idle = createIdleClock({
      clock, log, cfg: cfg.idle,
      async probe(jobId): Promise<IdleInput | undefined> {
        const job = ledger.job(jobId), lane = lanes.get(jobId);
        if (!job || job.state === 'ended' || job.reattachPending || !lane || !attached.has(jobId)) return undefined;
        const activity = await lane.activity();
        const open = bridge.open(jobId);
        const current = ledger.job(jobId)!;
        const wrapup = current.input.idle?.wrapup_after;
        return {
          sessionKind: current.sessionKind ?? 'starting', activity, launcherBuffered: activity.launcherBuffered,
          openParent: open.approvals + open.questions, lastActivityAt: current.lastActivityAt, lastRelayAt: activity.lastRelayAt,
          parkedAt: current.parkedAt, deadlineAt: current.deadlineAt, caps: info.get(current.backend)?.caps ?? noCaps,
          canPark: parkable.get(jobId) ?? false,
          ...(wrapup === undefined ? {} : { settleAfterMs: typeof wrapup === 'string' ? parseDuration(wrapup) : wrapup }),
        };
      },
      async park(jobId) { await lanes.get(jobId)?.park(); changed(); },
      async settle(jobId, why) {
        const lane = lanes.get(jobId);
        if (!lane) return;
        await lane.settle('wrapup', why.reason === 'deadline' ? 'deadline' : 'idle_wrapup', why.reason === 'idle' ? { quiescence: why.quiescence } : {});
      },
    });

    // 6. Lanes.
    function ended(jobId: string) {
      const lane = lanes.get(jobId);
      lanes.delete(jobId); attached.delete(jobId); parkable.delete(jobId);
      idle.untrack(jobId);
      try { bridge.abandonAll(jobId, 'session_ended'); } catch { log.warn('parent rows not abandoned', { jobId }); }
      if (lane) detach(lane.stop(), 'session lane stop');
      changed();
    }
    function laneFor(jobId: string): SessionLane | undefined {
      const existing = lanes.get(jobId);
      if (existing) return existing;
      const job = ledger.job(jobId);
      if (!job || job.state === 'ended' || dead) return undefined;
      const lane = createLane(job, {
        ledger, outbox, jobs, dir, hub, clock, log, self, caps: info.get(job.backend)?.caps ?? noCaps, deliver,
        onActivity: id => idle.touch(id), onEnded: ended,
      });
      lanes.set(jobId, lane);
      return lane;
    }
    function attach(jobId: string, handle: SessionHandle) {
      const lane = laneFor(jobId);
      if (!lane || attached.has(jobId)) return;
      try { lane.attach(handle); } catch { log.warn('session lane attach failed', { jobId }); return; }
      attached.add(jobId); parkable.set(jobId, typeof handle.park === 'function');
      idle.track(jobId);
      changed();
    }
    function outcome(r: ReattachOutcome) {
      if (r.outcome === 'live' || r.outcome === 'parked' || r.outcome === 'adopted') attach(r.job.jobId, r.handle);
      else if (r.outcome === 'launch_started' && r.result.ok) attach(r.job.jobId, r.result.handle);
      const job = ledger.job(r.job.jobId);
      if (job?.state === 'ended' && lanes.has(job.jobId)) ended(job.jobId);
    }
    async function startJob(jobId: string): Promise<void> {
      const job = ledger.job(jobId);
      if (!job) return;
      changed();
      const backend = backends.get(job.backend);
      if (backend) {
        // Refresh the capabilities before the lane is built from them.
        try {
          const caps = await backend.capabilities();
          if (!dead) learned(job.backend, caps, info.get(job.backend)?.health ?? downHealth);
        } catch { log.warn('backend capabilities unavailable', { backend: job.backend }); }
      }
      if (dead) return;
      laneFor(jobId);
      const result = await startLaunch(ledger.job(jobId)!, launchDeps);
      if (dead) return;
      if (result.ok) attach(jobId, result.handle);
      else if (ledger.job(jobId)?.state === 'ended') ended(jobId);
    }

    // 7. Outbox rejections: an expired job thread ends its session.
    outbox.onRejected((row, err) => {
      if (dead || row.kind !== 'send' || err.status !== 422 || err.code !== 'expired') return;
      const threadId = (row.payload as SendRequest).thread_id;
      const job = threadId ? ledger.jobByThread(threadId) : undefined;
      if (!job || job.state === 'ended') return;
      const lane = laneFor(job.jobId);
      if (lane) detach(lane.settle('wrapup', 'expired'), 'expired settle');
    });

    // 8. Retired requesters (F5 backstop): interrupt, note, cancel after a grace.
    const retireTimers = new Map<string, { cancel(): void }>();
    function armRetire(jobId: string, at: number) {
      retireTimers.get(jobId)?.cancel();
      retireTimers.set(jobId, clock.timer(Math.max(0, at - clock.now()), () => {
        retireTimers.delete(jobId);
        const job = ledger.job(jobId);
        daemonKv.delete(`retire:${jobId}`);
        if (!job || job.state === 'ended') return;
        if (dir.get(job.requester)) { log.info('requester returned within the grace; session kept', { jobId }); return; }
        const lane = laneFor(jobId);
        if (lane) detach(lane.settle('cancel', 'requester_retired'), 'requester_retired settle');
      }));
    }
    function retire(peer: string) {
      if (dead) return;
      for (const job of ledger.jobsByRequester(peer)) {
        if (job.state === 'ended' || daemonKv.get(`retire:${job.jobId}`)) continue;
        const at = clock.now() + RETIRE_GRACE_MS;
        ledger.tx(() => {
          daemonKv.set(`retire:${job.jobId}`, String(at));
          const key = `vl.nt.${job.jobId}.requester_retired.${ledger.nextCounter(job.jobId, 'requester_retired')}`;
          outbox.enqueue('send', key, { idempotency_key: key, kind: 'message', thread_id: job.threadId, in_reply_to: job.launchMessageId,
            priority: 'wake_idle', content: { text: 'The requester was retired. The session is interrupted and will be cancelled in 10 minutes.' } }, job.jobId);
        });
        const lane = laneFor(job.jobId);
        if (lane) detach(lane.control('interrupt', self), 'retired requester interrupt');
        armRetire(job.jobId, at);
      }
    }
    dir.onRemoved(retire);

    // 9. Reattach. Parent rows still open from before the restart have lost
    // their live hooks: abandon them first, so a prompt raised during
    // reattach is never mistaken for one of them.
    for (const job of ledger.openJobs()) {
      try { bridge.abandonAll(job.jobId, 'restart'); } catch { log.warn('parent rows not abandoned', { jobId: job.jobId }); }
      laneFor(job.jobId);
    }
    for (const r of await reattachAll(launchDeps)) outcome(r);
    for (const key of daemonKv.keys('retire:')) armRetire(key.slice('retire:'.length), Number(daemonKv.get(key)));
    // A requester retired while the daemon was down never leaves a later
    // snapshot: compare the open jobs with the directory loaded at startup.
    // If the startup refresh failed, the first later successful refresh does it.
    let retiredChecked = false;
    const checkRetired = () => {
      if (retiredChecked || dead) return;
      retiredChecked = true;
      for (const peer of new Set(ledger.openJobs().map(j => j.requester))) if (!dir.get(peer)) retire(peer);
    };
    if (directoryLoaded) checkRetired();
    dir.onRefreshed?.(checkRetired);

    // 10. Outbox, idle clock (tracked on attach), presence, stream, dispatcher.
    const controller = new AbortController();
    const signal = controller.signal;
    const loops = new Set<Promise<unknown>>();
    const outboxLoop = outbox.run(signal).catch(() => { if (!dead) log.error('outbox stopped'); });
    loops.add(outboxLoop);
    const modes = (): PermissionMode[] => {
      const offered = new Set([...info.values()].flatMap(i => i.caps.permissionModes));
      const allowed = new Set(Object.values(cfg.policies).flatMap(p => p.permission_modes ?? []));
      return [...offered].filter(m => allowed.has(m));
    };
    const snapshot = (): PresenceSnapshot => {
      const counts = ledger.counts();
      return {
        running: counts.unparked, open: counts.open, max: cfg.limits.max_sessions, perRequester: cfg.policies.default.max_sessions ?? 0,
        backends: Object.fromEntries([...backends.keys()].map(id => {
          const h = info.get(id)?.health;
          return [id, !h?.ok ? 'down' : h.degraded?.length ? 'degraded' : 'ok'] as const;
        })),
        modes: modes(), presets: [...info].flatMap(([id, i]) => i.caps.presets.map(p => `${id}/${p.id}`)),
        projects: Object.keys(cfg.projects),
        scratchProjects: Object.keys(cfg.projects).filter(alias => cfg.projects[alias].scratch === true),
        backendModes: Object.fromEntries([...info].map(([id, i]) => [id, i.caps.permissionModes.filter(m => modes().includes(m))])),
        backendWorkspaces: Object.fromEntries([...info].map(([id, i]) => [id, [...i.caps.workspaceModes ?? ['worktree']]])),
      };
    };
    presence = createPresence({ hub, clock, log, host: cfg.host, heartbeatMs: cfg.presence.heartbeat_ms, snapshot });
    presence.start();

    const dispatcher = createDispatcher({
      ledger, outbox, jobs, dir, cfg, clock, log, host: cfg.host, self, bridge,
      backends: () => info, lane: laneFor, changed,
      accepted: jobId => detach(startJob(jobId), 'launch'),
    });
    const ingress = createIngress({ ledger, hub, log, self, clock, onDirectory: data => { detach(dir.onDirectoryUpdated(data), 'directory refresh'); } });
    const stream = o.stream ?? createEventStream({
      baseUrl: cfg.hub.url, credential: credential!, cursors: ingress.cursors, clock, log,
      onCursorPurged: cursorPurgedHook(daemonKv, log, clock),
    });
    let stopping: Promise<void> | undefined;
    let markStopped!: () => void;
    const stopped = new Promise<void>(resolve => { markStopped = resolve; });
    loops.add(stream.run(async e => { await ingress.handler(e); dispatcher.kick(); }, signal).then(result => {
      if (dead || signal.aborted || result.reason === 'aborted') return;
      log.error('event stream stopped', { reason: result.reason });
      process.exitCode = 1;
      void stop();
    }, () => {
      if (dead || signal.aborted) return;
      log.error('event stream failed');
      process.exitCode = daemonKv.get('recovery') ? RECOVERY_EXIT : 1;
      void stop();
    }));
    loops.add(dispatcher.run(signal).catch(() => { if (!dead) log.error('dispatcher stopped'); }));
    dispatcher.kick();

    // 11. Timers.
    let retrying = false;
    const every = (ms: number, fn: () => Promise<void> | void) => {
      const tick = () => { clock.timer(ms, () => { void (async () => { try { await fn(); } catch { if (!dead) log.warn('periodic task failed'); } tick(); })(); }); };
      tick();
    };
    every(RETRY_EVERY_MS, async () => {
      if (retrying || (!ledger.openJobs().some(j => j.reattachPending) && ledger.kv('reattach').keys().length === 0)) return;
      retrying = true;
      try { for (const r of await retryPending(launchDeps)) { if (!dead) outcome(r); } }
      finally { retrying = false; }
    });
    every(HOURLY_MS, async () => {
      try { await dir.refresh(); } catch { /* logged by the directory */ }
      const report = await sweep({ ...launchDeps, graceMs: SWEEP_GRACE_MS, retentionMs: cfg.limits.workspace_retention_ms, firstSeen: ledger.kv('sweep:first-seen') });
      if (report.settled.length || report.workspacesRemoved.length) log.info('sweep', { settled: report.settled.length, workspacesRemoved: report.workspacesRemoved.length });
    });
    every(cfg.presence.heartbeat_ms, async () => { await refreshHealth(); changed(); });

    // Passive Claude status is the default auxiliary adapter: it rides the
    // elected host lease, never elects its own, and adds no stream or post.
    const deliveryEnv = o.env ?? process.env;
    const delivery = startDeliveryHost({
      kv: ledger.kv('codex'), clock, log, env: deliveryEnv,
      auxiliary: () => createClaudeStatus({ env: deliveryEnv, clock, log, fetchSnapshot: spawnClaudeSnapshot(deliveryEnv) }),
    });
    // A dead delivery host must fail the embedded service as well, not leave a
    // healthy-looking launcher process that can never publish readiness again.
    void delivery.stopped.then(outcome => {
      if (outcome.reason === 'stopped' || dead) return;
      log.error('codex connector stopped', { reason: outcome.reason });
      process.exitCode = 1;
      void stop();
    });

    function halt() {
      dead = true;
      controller.abort();
      for (const t of [...timers]) t.cancel();
      timers.clear();
      idle.stop();
    }
    function stop(): Promise<void> {
      if (stopping) return stopping;
      stopping = (async () => {
        controller.abort();
        idle.stop();
        await Promise.allSettled([...loops]);
        await Promise.allSettled([...lanes.values()].map(l => l.stop()));
        // Before the ledger closes: engines release unsent claims and unsubscribe.
        await delivery.stop().catch(() => {});
        await presence!.stop();
        halt();
        try { ledger.close(); } catch { /* already closed */ }
      })().finally(markStopped);
      return stopping;
    }
    return {
      stop,
      stopped,
      crash() {
        if (dead) return;
        // A dead process: no settle, no offline presence, nothing more written.
        halt();
        stopping = Promise.resolve();
        for (const lane of lanes.values()) void lane.stop().catch(() => {});
        void delivery.stop().catch(() => {});
        void presence?.stop().catch(() => {});
        try { ledger.close(); } catch { /* already closed */ }
        markStopped();
      },
    };
  } catch (err) {
    dead = true;
    for (const t of [...timers]) t.cancel();
    try { ledger.close(); } catch { /* already closed */ }
    throw err;
  }
}
