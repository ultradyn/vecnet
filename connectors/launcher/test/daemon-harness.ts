import assert from 'node:assert/strict';
import type { TestContext } from 'node:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHubClient } from '../../hub-client/src/client.ts';
import type { EventStream, HubApi } from '../../hub-client/src/ports.ts';
import type { Delivery, DirectoryEntry, EventType, JobStatusBody, ReceiptBody, SendRequest, VecnetEvent } from '../../hub-client/src/wire.ts';
import { startFakeHub } from '../../hub-client/testing/fake-hub.ts';
import type { FakeHub } from '../../hub-client/testing/fake-hub.ts';
import { directoryEntry, fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { FakeClock } from '../../hub-client/testing/fixtures.ts';
import type { Backend } from '../src/backend.ts';
import { parseConfig } from '../src/config.ts';
import type { LauncherConfig } from '../src/config.ts';
import { startDaemon } from '../src/daemon.ts';
import { openLedger } from '../src/ledger.ts';
import type { Ledger } from '../src/ledger.ts';
import { createFakeBackend } from './fake-backend.ts';
import type { FakeBackend } from './fake-backend.ts';
import { launchDelivery, threadDelivery } from './fixtures.ts';

// The fixture Delivery is addressed to …8A and sent by …8C.
export const LAUNCHER = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A';
export const REQUESTER = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8C';
export const OPERATOR = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8D';
export const STRANGER = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8E';
export const T0 = Date.parse('2026-11-05T00:00:00.000Z');

export function testConfig(overrides: Record<string, unknown> = {}): LauncherConfig {
  return parseConfig({
    host: 'test-host', hub: { url: 'http://127.0.0.1:9', credential_file: '/nonexistent/credential' }, state_dir: '/nonexistent',
    backends: [{ id: 'fake', module: '../test/fake-backend-plugin.ts', config: {} }],
    projects: { repo: { repo_root: '/repo', base_ref: 'master' } },
    policies: { default: { backends: ['fake'], permission_modes: ['read-only', 'ask', 'auto'], projects: ['repo'], max_sessions: 3, max_open_sessions: 5, launches_per_hour: 30 } },
    idle: { park_after_ms: '30m', settle_opaque_ms: '1h', settle_observable_ms: '2h', check_every_ms: '60s', deadline_margin_ms: '5m' },
    presence: { heartbeat_ms: '5m' },
    ...overrides,
  });
}
export const input = (extra: Record<string, unknown> = {}) => ({ backend: 'fake', project: 'repo', preset: 'test', permission_mode: 'ask', ...extra });

/** A launch Delivery to the launcher from the requester, expiring a day after `now`. */
export function launch(now: number, extra: Record<string, unknown> = {}, overrides: Partial<Delivery> = {}): Delivery {
  const d = launchDelivery(input(extra), overrides);
  d.recipient = LAUNCHER;
  d.message.sender = REQUESTER;
  d.message.loop = { ...d.message.loop, expires_at: new Date(now + 86_400_000).toISOString() };
  return d;
}
export function message(thread: string, sender: string, text: string, overrides: Partial<Delivery> = {}): Delivery {
  const d = threadDelivery(thread, sender, text, 'wake', overrides);
  d.recipient = LAUNCHER;
  return d;
}

/** An in-memory EventStream: each push is handled in order, like the real stream. */
export interface MemoryStream extends EventStream { push(type: EventType, data: unknown): Promise<void> }
export function memoryStream(counter = { n: 0 }): MemoryStream {
  const queue: Array<{ ev: VecnetEvent; done(): void; fail(e: unknown): void }> = [];
  let wake: (() => void) | undefined;
  return {
    async run(handler, signal) {
      while (!signal.aborted) {
        const next = queue.shift();
        if (!next) {
          await new Promise<void>(resolve => { wake = resolve; signal.addEventListener('abort', () => resolve(), { once: true }); });
          continue;
        }
        try { await handler(next.ev); next.done(); } catch (e) { next.fail(e); }
      }
      return { reason: 'aborted' };
    },
    push(type, data) {
      return new Promise<void>((done, fail) => {
        queue.push({ ev: { cursor: `e1.${++counter.n}`, type, data, at: new Date(0).toISOString() }, done, fail });
        wake?.(); wake = undefined;
      });
    },
  };
}

export async function until(cond: () => boolean, what: string, ms = 5000): Promise<void> {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) assert.fail(`timed out waiting for ${what}`);
    await new Promise(resolve => setTimeout(resolve, 5));
  }
}
/** Let pending I/O and microtasks settle without waiting on a condition. */
export async function settle(ms = 60): Promise<void> { await new Promise(resolve => setTimeout(resolve, ms)); }

export interface Harness {
  hub: FakeHub; clock: FakeClock; log: ReturnType<typeof memoryLog>; backend: FakeBackend; cfg: LauncherConfig; dir: string;
  stream: MemoryStream; daemon: Awaited<ReturnType<typeof startDaemon>>; launches: () => number;
  sends(): SendRequest[]; statuses(jobId?: string): JobStatusBody[]; receipts(): ReceiptBody[]; replies(thread?: string): SendRequest[];
  presence(): Array<Record<string, unknown>>; peersCalls(): number;
  inspect<T>(fn: (ledger: Ledger) => T): T;
  push(type: EventType, data: unknown): Promise<void>;
  restart(opts?: { backends?: Map<string, Backend>; beforeRecordRef?: (jobId: string) => void }): Promise<void>;
}

export async function harness(t: TestContext, opts: { cfg?: Record<string, unknown>; peers?: DirectoryEntry[]; backend?: FakeBackend;
  beforeRecordRef?: (jobId: string) => void } = {}): Promise<Harness> {
  const dir = mkdtempSync(join(tmpdir(), 'launcher-daemon-'));
  const clock = fakeClock(T0), log = memoryLog();
  const hub = await startFakeHub();
  hub.setPeers(opts.peers ?? [
    directoryEntry({ peer_id: LAUNCHER, name: 'launcher-test', kind: 'launcher' }),
    directoryEntry({ peer_id: REQUESTER, name: 'requester', kind: 'session' }),
    directoryEntry({ peer_id: OPERATOR, name: 'operator', kind: 'operator' }),
    directoryEntry({ peer_id: STRANGER, name: 'stranger', kind: 'session' }),
  ]);
  const cfg = testConfig({ state_dir: dir, ...opts.cfg });
  const backend = opts.backend ?? createFakeBackend({ clock });
  let launches = 0;
  const launchOriginal = backend.launch.bind(backend);
  backend.launch = (...args) => { launches++; return launchOriginal(...args); };
  // The fake hub has no GET /v1/peers/me; `me` answers it, as the real hub would.
  const client: HubApi & { me(): Promise<unknown> } = { ...createHubClient({ baseUrl: hub.url, credential: hub.bearer }), me: async () => ({ kind: 'peer', peer_id: LAUNCHER }) };
  const counter = { n: 0 };
  let stream = memoryStream(counter);
  let daemon = await startDaemon({ cfg, hub: client, stream, clock, log, backends: new Map([['fake', backend]]), beforeRecordRef: opts.beforeRecordRef });
  const bodies = (path: string) => hub.requests.filter(r => r.path === path && r.method === 'POST').map(r => r.body);
  const sends = () => bodies('/v1/messages') as SendRequest[];
  const h: Harness = {
    hub, clock, log, backend, cfg, dir, launches: () => launches,
    get stream() { return stream; }, get daemon() { return daemon; },
    sends,
    statuses: jobId => sends().filter(s => s.job_status && (jobId === undefined || s.job_status.job_id === jobId)).map(s => s.job_status!),
    receipts: () => bodies('/v1/receipts') as ReceiptBody[],
    replies: thread => sends().filter(s => !s.job_status && (thread === undefined || s.thread_id === thread)),
    presence: () => bodies('/v1/presence') as Array<Record<string, unknown>>,
    peersCalls: () => hub.requests.filter(r => r.path === '/v1/peers' && r.method === 'GET').length,
    inspect(fn) {
      const ledger = openLedger(join(dir, 'ledger.sqlite'), clock);
      try { return fn(ledger); } finally { ledger.close(); }
    },
    push: (type, data) => stream.push(type, data),
    async restart(o = {}) {
      daemon.crash();
      stream = memoryStream(counter);
      daemon = await startDaemon({ cfg, hub: client, stream, clock, log, backends: o.backends ?? new Map([['fake', backend]]), beforeRecordRef: o.beforeRecordRef });
    },
  };
  t.after(async () => {
    await daemon.stop();
    await hub.close();
    rmSync(dir, { recursive: true, force: true });
  });
  return h;
}

/** Push a launch and wait for its accepted and running reports. */
export async function launched(h: Harness, extra: Record<string, unknown> = {}): Promise<{ d: Delivery; jobId: string; thread: string }> {
  const d = launch(h.clock.now(), extra);
  const jobId = d.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', d);
  await until(() => h.statuses(jobId).some(s => s.state === 'running'), 'running job_status');
  return { d, jobId, thread: d.message.thread_id };
}
