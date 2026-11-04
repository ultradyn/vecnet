import type { Clock, Log } from '../../hub-client/src/ports.ts';
import type { ActivityReport, BackendCapabilities, SessionState } from './backend.ts';
import type { LauncherConfig } from './config.ts';

export interface IdleInput {
  sessionKind: SessionState['kind'];
  activity: ActivityReport;
  launcherBuffered: number;
  openParent: number;
  lastActivityAt: number;
  lastRelayAt: number;
  parkedAt: number | null;
  deadlineAt: number;
  caps: Pick<BackendCapabilities, 'resume' | 'backgroundTasks'>;
  canPark: boolean;
  settleAfterMs?: number;
}
export type IdleVerdict =
  | { action: 'none'; nextCheckAt: number }
  | { action: 'park'; nextCheckAt: number }
  | { action: 'settle'; reason: 'idle'; quiescence: 'proven' | 'assumed' }
  | { action: 'settle'; reason: 'deadline' };

export function idleVerdict(i: IdleInput, now: number, cfg: LauncherConfig['idle']): IdleVerdict {
  const deadline = i.deadlineAt - cfg.deadline_margin_ms;
  if (now >= deadline) return { action: 'settle', reason: 'deadline' };

  const nextCheckAt = Math.min(now + cfg.check_every_ms, deadline);
  const a = i.activity;
  const busy = i.sessionKind === 'starting' || i.sessionKind === 'turn_running'
    || i.sessionKind === 'waiting_approval' || i.sessionKind === 'ended'
    || a.turnRunning || a.openApprovals > 0 || i.openParent > 0
    || a.queuedInputs > 0 || i.launcherBuffered > 0
    || ((i.caps.backgroundTasks === 'observable' || a.backgroundTasks.observable) && a.backgroundTasks.known > 0);
  if (busy) return { action: 'none', nextCheckAt };

  const since = Math.max(i.lastActivityAt, i.lastRelayAt);
  const settleMs = i.caps.backgroundTasks === 'observable'
    ? (i.sessionKind === 'parked' ? cfg.settle_observable_ms : cfg.settle_opaque_ms)
    : (i.settleAfterMs ?? cfg.settle_opaque_ms);
  const settleAt = since + settleMs;
  if (now >= settleAt) {
    return { action: 'settle', reason: 'idle', quiescence: i.caps.backgroundTasks === 'opaque' ? 'assumed' : 'proven' };
  }

  const nextSettleCheck = Math.min(nextCheckAt, settleAt);
  if (i.sessionKind === 'idle' && i.caps.resume === 'transcript' && i.canPark) {
    const parkAt = since + cfg.park_after_ms;
    if (now >= parkAt) return { action: 'park', nextCheckAt: nextSettleCheck };
    return { action: 'none', nextCheckAt: Math.min(nextSettleCheck, parkAt) };
  }
  return { action: 'none', nextCheckAt: nextSettleCheck };
}

export interface IdleClock {
  track(jobId: string): void;
  touch(jobId: string): void;
  untrack(jobId: string): void;
  stop(): void;
}

type SettleReason = { reason: 'idle'; quiescence: 'proven' | 'assumed' } | { reason: 'deadline' };
type Tracked = {
  timer?: { cancel(): void };
  running: boolean;
  touched: boolean;
  touchedAt: number;
  parkedSince?: number;
  deadlineCheckAt?: number;
};

export function createIdleClock(deps: {
  clock: Clock;
  log: Log;
  cfg: LauncherConfig['idle'];
  probe(jobId: string): Promise<IdleInput | undefined>;
  park(jobId: string): Promise<void>;
  settle(jobId: string, why: SettleReason): Promise<void>;
}): IdleClock {
  const { clock, cfg, log } = deps;
  const jobs = new Map<string, Tracked>();
  let stopped = false;
  const live = (jobId: string, job: Tracked) => !stopped && jobs.get(jobId) === job;

  function untrack(jobId: string) {
    jobs.get(jobId)?.timer?.cancel();
    jobs.delete(jobId);
  }
  function arm(jobId: string, job: Tracked, at: number) {
    job.timer?.cancel();
    // Node timers have a signed 32-bit delay even when a config allows longer windows.
    job.timer = clock.timer(Math.min(2_147_483_647, Math.max(0, at - clock.now())), () => {
      job.timer = undefined;
      if (live(jobId, job)) void check(jobId, job);
    });
  }
  async function check(jobId: string, job: Tracked) {
    if (job.running) return;
    job.running = true;
    job.touched = false;
    let nextAt: number | undefined;
    try {
      // The owner omits reattachPending jobs from track, or returns undefined here.
      const input = await deps.probe(jobId);
      if (!live(jobId, job) || job.touched) return;
      if (!input) { untrack(jobId); return; }
      job.deadlineCheckAt = input.deadlineAt - cfg.deadline_margin_ms;
      const lastRelayAt = Math.max(input.lastRelayAt, job.touchedAt);
      const since = Math.max(input.lastActivityAt, lastRelayAt);
      const verdict = idleVerdict({ ...input, lastRelayAt,
        canPark: input.canPark && job.parkedSince !== since }, clock.now(), cfg);
      if (verdict.action === 'settle') {
        const why: SettleReason = verdict.reason === 'deadline'
          ? { reason: 'deadline' } : { reason: 'idle', quiescence: verdict.quiescence };
        await deps.settle(jobId, why);
        if (live(jobId, job)) untrack(jobId);
      } else {
        if (verdict.action === 'park') {
          await deps.park(jobId);
          // A delayed probe snapshot must not issue a second park for the same idle window.
          job.parkedSince = since;
        }
        nextAt = verdict.nextCheckAt;
      }
    } catch {
      // A bridge exception may contain credentials or agent text. Never copy it to logs.
      if (live(jobId, job)) log.warn('idle clock check failed', { jobId });
    } finally {
      job.running = false;
      if (live(jobId, job)) {
        const now = clock.now();
        const retryAt = Math.min(now + cfg.check_every_ms,
          job.deadlineCheckAt !== undefined && job.deadlineCheckAt > now ? job.deadlineCheckAt : Infinity);
        // A slow action can consume the scheduled interval. Re-probe immediately
        // then, rather than adding another interval across the hard deadline.
        arm(jobId, job, job.touched ? now : nextAt !== undefined ? Math.max(now, nextAt) : retryAt);
      }
    }
  }
  return {
    track(jobId) {
      if (stopped || jobs.has(jobId)) return;
      const job: Tracked = { running: false, touched: false, touchedAt: -Infinity };
      jobs.set(jobId, job);
      arm(jobId, job, clock.now());
    },
    touch(jobId) {
      const job = jobs.get(jobId);
      if (!job || stopped) return;
      job.touchedAt = clock.now();
      job.touched = true;
      if (!job.running) arm(jobId, job, clock.now());
    },
    untrack,
    stop() {
      stopped = true;
      for (const jobId of jobs.keys()) untrack(jobId);
    },
  };
}
