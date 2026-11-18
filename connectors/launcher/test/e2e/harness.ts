import { writePrivate } from '../fixtures/platform.ts';
// End to end: the launcher daemon in-process, with the real hub client and
// event stream, against a real bin/vecnet-hub child and the fake backend
// loaded by module path. No T3, no Paseo. Run through `just ln-e2e`.
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { newUlid } from '../../../hub-client/src/ids.ts';
import type { Clock } from '../../../hub-client/src/ports.ts';
import type { Delivery, Message, SendRequest, VecnetEvent } from '../../../hub-client/src/wire.ts';
import { memoryLog } from '../../../hub-client/testing/fixtures.ts';
import { startRealHub } from '../../../hub-client/testing/real-hub.ts';
import type { RealHub } from '../../../hub-client/testing/real-hub.ts';
import type { AgentQuestion, PermissionRequest } from '../../src/backend.ts';
import { parseConfig } from '../../src/config.ts';
import type { LauncherConfig } from '../../src/config.ts';
import type { Schema } from '../../src/contract/metadata.ts';
import { startDaemon } from '../../src/daemon.ts';
import { openLedger } from '../../src/ledger.ts';
import type { Ledger } from '../../src/ledger.ts';
import type { FakeBackend } from '../fake-backend.ts';
import { fakeFor } from './fake-plugin.ts';
import { connectPeer } from './requester.ts';
import type { Requester } from './requester.ts';

const DAY = 86_400_000;
const WAIT = 15_000;
const PLUGIN = fileURLToPath(new URL('./fake-plugin.ts', import.meta.url));
const PERMISSION_SCHEMA = JSON.parse(readFileSync(new URL('../../schema/launch-permission.v1.schema.json', import.meta.url), 'utf8')) as Schema;

/**
 * A real-time Clock whose reading can be moved. Timers are kept by their
 * target reading, so `shift` fires every timer it passes and re-times the rest.
 * Only the deadline and reattach scenarios move it; elsewhere it is real time.
 */
interface ShiftClock extends Clock { shift(ms: number): void }
function shiftClock(offsetMs = 0): ShiftClock {
  let offset = offsetMs;
  type Entry = { at: number; fn: () => void; handle?: ReturnType<typeof setTimeout> };
  const pending = new Set<Entry>();
  const now = () => Date.now() + offset;
  function schedule(e: Entry) {
    e.handle = setTimeout(() => { pending.delete(e); e.fn(); }, Math.min(2_147_483_647, Math.max(0, e.at - now())));
  }
  function timer(ms: number, fn: () => void) {
    const e: Entry = { at: now() + Math.max(0, ms), fn };
    pending.add(e); schedule(e);
    return { cancel() { clearTimeout(e.handle); pending.delete(e); } };
  }
  return {
    now, timer,
    sleep(ms, signal) {
      return new Promise<void>((resolve, reject) => {
        if (signal?.aborted) { reject(signal.reason); return; }
        const alarm = timer(ms, () => { signal?.removeEventListener('abort', abort); resolve(); });
        function abort() { alarm.cancel(); reject(signal?.reason); }
        signal?.addEventListener('abort', abort, { once: true });
      });
    },
    shift(ms) {
      offset += ms;
      for (const e of [...pending]) { clearTimeout(e.handle); schedule(e); }
    },
  };
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
async function until(cond: () => boolean, what: string, ms = WAIT): Promise<void> {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) assert.fail(`timed out waiting for ${what}`);
    await sleep(20);
  }
}

type Daemon = Awaited<ReturnType<typeof startDaemon>>;
interface Env {
  hub: RealHub; dir: string; stateFile: string; repo: string; cfg: LauncherConfig; clock: ShiftClock;
  log: ReturnType<typeof memoryLog>;
  launcher: { peerId: string; secret: string };
  requester: Requester; bystander: Requester; operator: Requester;
  daemon: Daemon;
  fake(): FakeBackend;
  start(o?: { beforeRecordRef?: (jobId: string) => void }): Promise<void>;
  inspect<T>(fn: (ledger: Ledger) => T): T;
}

async function setup(t: TestContext, o: { idle?: Record<string, string>; clockOffsetMs?: number; noStart?: boolean } = {}): Promise<Env> {
  const dir = mkdtempSync(join(tmpdir(), 'launcher-e2e-'));
  const peers: Requester[] = [];
  let daemon: Daemon | undefined;
  let hub: RealHub | undefined;
  t.after(async () => {
    if (daemon) await daemon.stop().catch(() => {});
    for (const p of peers) await p.close().catch(() => {});
    if (hub) await hub.stop();
    if (process.env.LAUNCHER_E2E_DEBUG) process.stderr.write(`${env.log.text()}\n`);
    rmSync(dir, { recursive: true, force: true });
  });
  hub = await startRealHub();
  const launcher = await hub.addPeer('launcher', 'launcher-e2e');
  const requesterPeer = await hub.addPeer('session', 'requester');
  const bystanderPeer = await hub.addPeer('session', 'bystander');
  const connect = async (p: { peerId: string; secret: string }) => { const r = await connectPeer({ url: hub!.url, ...p }); peers.push(r); return r; };
  const requester = await connect(requesterPeer);
  const bystander = await connect(bystanderPeer);
  const operator = await connect(hub.operator);

  // The project alias names a real git repository; the requester never sends a path.
  const repo = join(dir, 'repo');
  mkdirSync(repo);
  const git = (...args: string[]) => execFileSync('git', args, { cwd: repo, stdio: 'ignore',
    env: { ...process.env, GIT_AUTHOR_NAME: 'e2e', GIT_AUTHOR_EMAIL: 'e2e@example.invalid', GIT_COMMITTER_NAME: 'e2e', GIT_COMMITTER_EMAIL: 'e2e@example.invalid' } });
  git('init', '-q', '-b', 'master');
  writeFileSync(join(repo, 'README.md'), 'demo\n');
  git('add', 'README.md');
  git('commit', '-q', '-m', 'init');

  const credential = join(dir, 'credential');
  writePrivate(credential, `${launcher.secret}\n`);
  const stateFile = join(dir, 'fake-state.json');
  const clock = shiftClock(o.clockOffsetMs ?? 0);
  const cfg = parseConfig({
    host: 'e2e-host',
    hub: { url: hub.url, credential_file: credential },
    state_dir: join(dir, 'state'),
    backends: [{ id: 'fake', module: PLUGIN, config: { stateFile, clock } }],
    projects: { demo: { repo_root: repo, base_ref: 'master' } },
    policies: { default: { backends: ['fake'], projects: ['demo'], permission_modes: ['read-only', 'ask', 'auto'] } },
    approvals: { parent_expires: '2s' },
    questions: { blocking_timeout: '2s' },
    idle: { park_after: '2s', settle_opaque: '4s', settle_observable: '4s', check_every: '250ms', deadline_margin: '1s', ...o.idle },
  });
  const log = memoryLog();
  const env: Env = {
    hub, dir, stateFile, repo, cfg, clock, log, launcher, requester, bystander, operator,
    get daemon() { return daemon!; },
    fake: () => fakeFor(stateFile),
    async start(s = {}) { daemon = await startDaemon({ cfg, clock, log, env: { VECNET_CODEX_BINDINGS: join(dir, 'codex-bindings.json') }, beforeRecordRef: s.beforeRecordRef }); },
    inspect(fn) {
      const ledger = openLedger(join(cfg.state_dir, 'ledger.sqlite'), clock);
      try { return fn(ledger); } finally { ledger.close(); }
    },
  };
  if (!o.noStart) await env.start();
  return env;
}

interface Launched { jobId: string; thread: string; launchId: string; expiresAt: string }
let jobKeys = 0;
function launchRequest(env: Env, o: { expiresInMs?: number; input?: Record<string, unknown>; instructions?: string } = {}): Omit<SendRequest, 'idempotency_key'> {
  return {
    recipients: [env.launcher.peerId], kind: 'message', priority: 'wake', locked_participants: true,
    expires_at: new Date(Date.now() + (o.expiresInMs ?? 3 * DAY)).toISOString(),
    content: { text: 'launch request' },
    job_request: { version: 1, job_key: `launch-${++jobKeys}-${newUlid(Date.now()).toLowerCase()}`, operation: 'launch',
      instructions: o.instructions ?? 'Do the end-to-end work.',
      input: { backend: 'fake', project: 'demo', preset: 'test', permission_mode: 'ask', ...o.input } },
  };
}
async function launch(env: Env, o: Parameters<typeof launchRequest>[1] = {}): Promise<Launched> {
  const req = launchRequest(env, o);
  const res = await env.requester.send(req);
  assert.ok(res.job_id, 'the hub created a job');
  return { jobId: res.job_id, thread: res.thread_id, launchId: res.message_id, expiresAt: req.expires_at! };
}
/** The requester's copy of the launcher's job_status for a state. */
function status(env: Env, jobId: string, state: string, ms = WAIT): Promise<Message> {
  return env.requester.waitFor(e => {
    const m = delivered(e);
    return m?.kind === 'job_status' && m.job_status?.job_id === jobId && m.job_status.state === state ? m : undefined;
  }, ms, `job_status ${state}`);
}
async function running(env: Env, o: Parameters<typeof launchRequest>[1] = {}): Promise<Launched> {
  const l = await launch(env, o);
  await status(env, l.jobId, 'running');
  return l;
}
function delivered(e: VecnetEvent): Message | undefined {
  return e.type === 'delivery' ? (e.data as Delivery).message : undefined;
}
const turnReplies = (env: Env, thread: string) => env.requester.deliveries()
  .map(d => d.message).filter(m => m.thread_id === thread && m.sender === env.launcher.peerId && m.metadata['launch.turn'] !== undefined);
function waitReply(env: Env, thread: string, pred: (m: Message) => boolean = () => true): Promise<Message> {
  return env.requester.waitFor(e => {
    const m = delivered(e);
    return m && m.thread_id === thread && m.sender === env.launcher.peerId && m.metadata['launch.turn'] !== undefined && pred(m) ? m : undefined;
  }, WAIT, 'turn reply');
}
function permissionRequest(env: Env, id: string): PermissionRequest {
  const options = [{ id: 'opt-allow', label: 'allow once', grants: 'once' as const }, { id: 'opt-deny', label: 'deny', grants: 'deny' as const }];
  return {
    requestId: id, summary: `edit README.md (${id})`, defaultOnExpiry: 'opt-deny', options,
    permission: { kind: 'edit', backend_method: 'fake/requestPermission', scope: 'once', options,
      paths: [{ path: join(env.repo, 'README.md'), access: 'write' }], backend_raw: { path: 'README.md' },
      fidelity: { kind: 'enforced', command: 'none', paths: 'enforced', host: 'none' } },
  } as PermissionRequest;
}
/** The launcher's current log head, read without acking (GET /v1/events?after=head, a plain poll). */
async function headCursor(url: string, secret: string): Promise<string> {
  const res = await fetch(new URL('/v1/events?after=head&wait=0', url), { headers: { Authorization: `Bearer ${secret}`, Accept: 'application/json' } });
  assert.equal(res.status, 200);
  const body = await res.json() as { next_cursor: string };
  return body.next_cursor;
}
const inputsOf = (env: Env, jobId: string, states: Parameters<Ledger['inputs']>[1]) => env.inspect(l => l.inputs(jobId, states));

export { DAY, WAIT, PERMISSION_SCHEMA, sleep, until, setup, launchRequest, launch, status, running, delivered, turnReplies, waitReply, permissionRequest, headCursor, inputsOf };
