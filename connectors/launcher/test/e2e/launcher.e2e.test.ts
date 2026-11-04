// End to end: the launcher daemon in-process, with the real hub client and
// event stream, against a real bin/vecnet-hub child and the fake backend
// loaded by module path. No T3, no Paseo. Run through `just ln-e2e`.
import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { HubError } from '../../../hub-client/src/errors.ts';
import { newUlid } from '../../../hub-client/src/ids.ts';
import type { Clock } from '../../../hub-client/src/ports.ts';
import type { Delivery, Message, SendRequest, VecnetEvent } from '../../../hub-client/src/wire.ts';
import { memoryLog } from '../../../hub-client/testing/fixtures.ts';
import { startRealHub } from '../../../hub-client/testing/real-hub.ts';
import type { RealHub } from '../../../hub-client/testing/real-hub.ts';
import type { AgentQuestion, PermissionRequest } from '../../src/backend.ts';
import { parseConfig } from '../../src/config.ts';
import type { LauncherConfig } from '../../src/config.ts';
import { validateSchema } from '../../src/contract/metadata.ts';
import type { Schema } from '../../src/contract/metadata.ts';
import { startDaemon } from '../../src/daemon.ts';
import { openLedger } from '../../src/ledger.ts';
import type { Ledger } from '../../src/ledger.ts';
import type { FakeBackend } from '../fake-backend.ts';
import { fakeFor, onNextCreate } from './fake-plugin.ts';
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
  writeFileSync(credential, `${launcher.secret}\n`, { mode: 0o600 });
  chmodSync(credential, 0o600);
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
    async start(s = {}) { daemon = await startDaemon({ cfg, clock, log, beforeRecordRef: s.beforeRecordRef }); },
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

test('1. launch: accepted, running with session_ref, one turn reply to the launch message, the launch processed', async t => {
  const env = await setup(t);
  const l = await launch(env);
  const accepted = await status(env, l.jobId, 'accepted');
  assert.equal(accepted.job_status!.sequence, 1);
  const run = await status(env, l.jobId, 'running');
  assert.equal(run.job_status!.sequence, 2);
  const output = run.job_status!.output as { session_ref: { backend: string; id: string; extra: { tag: string } } };
  assert.equal(output.session_ref.backend, 'fake');
  assert.equal(output.session_ref.extra.tag, `vl-${l.jobId}`);

  env.fake().finishTurn(l.jobId, 'turn one done');
  const reply = await waitReply(env, l.thread);
  assert.equal(reply.in_reply_to, l.launchId);
  assert.equal(reply.content.text, 'turn one done');
  assert.equal(reply.metadata['launch.turn'], 'completed');
  assert.equal(reply.metadata['launch.job_id'], l.jobId);
  // The launch message is turn one's input.
  assert.deepEqual(reply.metadata['launch.consumed'], [l.launchId]);
  // The launch Delivery to the launcher is marked processed (with turn one).
  await env.requester.waitFor(e => e.type === 'receipt' && (e.data as { message_id: string; state: string }).message_id === l.launchId
    && (e.data as { state: string }).state === 'processed' ? true : undefined, WAIT, 'processed receipt for the launch');
  await sleep(300);
  assert.equal(turnReplies(env, l.thread).length, 1);
  // Every launcher post replies to the launch message (depth stays 1).
  const history = await env.requester.history(l.thread);
  const posts = history.filter(m => m.sender === env.launcher.peerId);
  assert.ok(posts.length >= 3);
  for (const m of posts) { assert.equal(m.in_reply_to, l.launchId); assert.equal(m.loop.depth, 1); }
});

test('2. expiry cap and lifetime gate: 8 days refused by the hub, 3 s refused as expires_too_soon', async t => {
  const env = await setup(t);
  await assert.rejects(env.requester.send(launchRequest(env, { expiresInMs: 8 * DAY })), (err: unknown) =>
    err instanceof HubError && err.status === 422 && err.code === 'invalid_value');
  const l = await launch(env, { expiresInMs: 3000 });
  const failed = await status(env, l.jobId, 'failed', 2500);
  const out = failed.job_status!.output as { error: string; reason: string; details: { needed_ms: number } };
  assert.equal(out.error, 'policy_denied');
  assert.equal(out.reason, 'expires_too_soon');
  assert.equal(out.details.needed_ms, 4000 + 600_000);
  assert.equal(env.fake().sessions().length, 0);
  assert.equal(env.inspect(ledger => ledger.openJobs().length), 0);
});

test('3. steer, queue, buffer; a bystander added by the requester is not relayed', async t => {
  const env = await setup(t);
  const l = await running(env);
  const say = (text: string, priority: SendRequest['priority'], extra: Partial<SendRequest> = {}) =>
    env.requester.send({ thread_id: l.thread, in_reply_to: l.launchId, kind: 'message', priority, content: { text }, ...extra });

  const steer = await say('steer this turn', 'wake');
  await until(() => env.fake().sessions()[0].sent.length === 1, 'steer');
  const first = env.fake().sessions()[0].sent[0];
  assert.equal(first.mode, 'steer');
  assert.match(first.items[0].text, /steer this turn/);

  const hold = await say('held context', 'hold');
  await until(() => inputsOf(env, l.jobId, ['buffered']).length === 1, 'hold buffered');
  const queued = await say('next turn please', 'wake_idle');
  await until(() => inputsOf(env, l.jobId, ['buffered']).length === 2, 'wake_idle queued in the launcher');
  assert.equal(env.fake().sessions()[0].sent.length, 1);

  env.fake().finishTurn(l.jobId, 'turn one done');
  const reply = await waitReply(env, l.thread);
  assert.deepEqual(reply.metadata['launch.consumed'], [l.launchId, steer.message_id]);
  await until(() => env.fake().sessions()[0].sent.length === 2, 'next turn started');
  const next = env.fake().sessions()[0].sent[1];
  assert.equal(next.mode, 'start');
  assert.deepEqual(next.items.map(i => i.messageId), [hold.message_id, queued.message_id]);
  assert.match(next.items[0].text, /held context/);

  // The requester, the locked thread's creator, adds a bystander.
  await say('bystander, meet the session', 'hold', { recipients: [env.launcher.peerId, env.bystander.peerId] });
  await env.bystander.waitFor(e => delivered(e)?.thread_id === l.thread ? true : undefined, WAIT, 'bystander joined');
  const outsider = await env.bystander.send({ thread_id: l.thread, kind: 'message', priority: 'wake', content: { text: 'bystander says hi' } });
  const receipt = await env.bystander.waitFor(e => {
    const r = e.data as { message_id?: string; state?: string; note?: string };
    return e.type === 'receipt' && r.message_id === outsider.message_id && r.state === 'processed' ? r : undefined;
  }, WAIT, 'bystander receipt');
  assert.equal(receipt.note, 'not_relayed');
  await sleep(500);
  const relayed = env.fake().sessions()[0].sent.flatMap(s => s.items.map(i => i.text));
  assert.ok(!relayed.some(text => text.includes('bystander says hi')));
  const history = await env.requester.history(l.thread);
  assert.ok(!history.some(m => m.sender === env.launcher.peerId && m.in_reply_to === outsider.message_id));
  assert.equal(turnReplies(env, l.thread).length, 1);
});

test('4. question: a structured question reaches the requester and its reply answers it; a secret one never enters the thread', async t => {
  const env = await setup(t);
  const l = await running(env);
  const question: AgentQuestion = { requestId: 'q-colour', blocking: true,
    questions: [{ id: 'colour', text: 'Which colour should the button be?', options: [{ label: 'red' }, { label: 'blue' }], allowOther: false, secret: false }] };
  const answer = env.fake().ask(l.jobId, question);
  const asked = await env.requester.waitFor(e => {
    const m = delivered(e);
    return m?.thread_id === l.thread && m.metadata['launch.question'] ? m : undefined;
  }, WAIT, 'launch.question');
  assert.equal(asked.kind, 'message');
  assert.equal(asked.priority, 'wake');
  assert.equal(asked.in_reply_to, l.launchId);
  const meta = asked.metadata['launch.question'] as { v: number; blocking: boolean; questions: Array<{ id: string; options: string[] }> };
  assert.equal(meta.v, 1);
  assert.deepEqual(meta.questions.map(q => [q.id, q.options]), [['colour', ['red', 'blue']]]);
  await env.requester.reply(asked, 'Blue');
  assert.deepEqual(await answer, { answers: { colour: 'blue' }, answeredBy: env.requester.peerId });

  const secretText = 'What is the production database password?';
  const secret = await env.fake().ask(l.jobId, { requestId: 'q-secret', blocking: true,
    questions: [{ id: 'pw', text: secretText, allowOther: true, secret: true }] });
  assert.deepEqual(secret.answers, { pw: 'secrets cannot be provided through vecnet' });
  await env.requester.waitFor(e => {
    const m = delivered(e);
    return m?.thread_id === l.thread && m.content.text.includes('secrets cannot be provided through vecnet') ? true : undefined;
  }, WAIT, 'secret note');
  const history = await env.requester.history(l.thread);
  assert.ok(!history.some(m => m.content.text.includes(secretText) || JSON.stringify(m.metadata).includes(secretText)));
});

test('5. approval: allow once, escalate to an operator thread, expiry denies', async t => {
  const env = await setup(t);
  const l = await running(env);
  const prompt = (id: string) => env.requester.waitFor(e => {
    const m = delivered(e);
    return m?.kind === 'approval_request' && m.approval_request?.question.includes(`(${id})`) ? m : undefined;
  }, WAIT, `approval_request ${id}`);

  // 1. Allowed once by the requester.
  const first = env.fake().requestPermission(l.jobId, permissionRequest(env, 'p1'));
  const card = await prompt('p1');
  assert.equal(card.thread_id, l.thread);
  assert.equal(card.in_reply_to, l.launchId);
  assert.equal(card.priority, 'wake');
  assert.deepEqual(card.approval_request!.options, ['allow once', 'deny', 'escalate']);
  const permission = card.metadata['launch.permission'];
  assert.deepEqual(validateSchema(PERMISSION_SCHEMA, permission), []);
  assert.ok(Buffer.byteLength(JSON.stringify(permission)) < 6 * 1024);
  await env.requester.respond(card, 'allow once');
  assert.deepEqual(await first, { optionId: 'opt-allow', decidedBy: env.requester.peerId });

  // 2. Escalated: an operator prompt in a new escalation thread; the operator answers there.
  const second = env.fake().requestPermission(l.jobId, permissionRequest(env, 'p2'));
  const card2 = await prompt('p2');
  await env.requester.respond(card2, 'escalate');
  const opCard = await env.operator.waitFor(e => {
    const m = delivered(e);
    return m?.kind === 'approval_request' && m.approval_request?.question.includes('(p2)') ? m : undefined;
  }, WAIT, 'operator approval_request');
  assert.notEqual(opCard.thread_id, l.thread);
  assert.equal(opCard.metadata['launch.escalated_from'], card2.approval_request!.approval_id);
  assert.equal(opCard.metadata['launch.job_id'], l.jobId);
  assert.deepEqual(opCard.metadata['launch.permission'], card2.metadata['launch.permission']);
  assert.deepEqual(opCard.approval_request!.options, ['allow once', 'deny']);
  await env.operator.respond(opCard, 'allow once');
  assert.deepEqual(await second, { optionId: 'opt-allow', decidedBy: env.operator.peerId });
  const jobThread = await env.requester.history(l.thread);
  assert.ok(jobThread.every(m => !m.recipients.includes(env.operator.peerId)), 'the locked job thread never gains the operator');

  // 3. Unanswered: the parent prompt expires to the backend's deny option.
  const third = env.fake().requestPermission(l.jobId, permissionRequest(env, 'p3'));
  await prompt('p3');
  assert.deepEqual(await third, { optionId: 'opt-deny', decidedBy: 'expired' });
});

test('6. cancel reaches the hub: interrupt and settle, a cancelled report the hub accepts', async t => {
  const env = await setup(t);
  const l = await running(env);
  await env.requester.send({ thread_id: l.thread, in_reply_to: l.launchId, kind: 'job_status', priority: 'wake',
    content: { text: 'cancel' }, job_status: { job_id: l.jobId, sequence: 100, state: 'cancelled' } });
  const cancelled = await status(env, l.jobId, 'cancelled');
  assert.equal(cancelled.sender, env.launcher.peerId);
  const s = env.fake().sessions()[0];
  assert.ok(s.interrupts >= 1);
  assert.equal(s.settles, 1);
  // The requester's sequence is not the job's: the launcher's report is accepted as sequence 3.
  const history = await env.requester.history(l.thread);
  const reports = history.filter(m => m.kind === 'job_status' && m.sender === env.launcher.peerId).map(m => [m.job_status!.sequence, m.job_status!.state]);
  assert.deepEqual(reports, [[1, 'accepted'], [2, 'running'], [3, 'cancelled']]);
});

test('7. idle park and settle: park at about 2 s, settle at about 4 s, succeeded idle_wrapup', async t => {
  const env = await setup(t);
  const l = await running(env);
  env.fake().finishTurn(l.jobId, 'all done');
  await waitReply(env, l.thread);
  const start = Date.now();
  let parkedAt = 0, settledAt = 0;
  await until(() => {
    const s = env.fake().sessions()[0];
    if (!parkedAt && s.parks > 0) parkedAt = Date.now() - start;
    if (!settledAt && s.settles > 0) settledAt = Date.now() - start;
    return settledAt > 0;
  }, 'idle settle', 10_000);
  assert.ok(parkedAt >= 1500 && parkedAt <= 3500, `parked after ${parkedAt} ms`);
  assert.ok(settledAt >= 3500 && settledAt <= 6000, `settled after ${settledAt} ms`);
  assert.ok(parkedAt < settledAt);
  const done = await status(env, l.jobId, 'succeeded');
  const out = done.job_status!.output as { ended: string; quiescence: string; turns: number };
  assert.equal(out.ended, 'idle_wrapup');
  assert.equal(out.quiescence, 'proven');
  assert.equal(out.turns, 1);
  assert.equal(env.fake().sessions()[0].parks, 1);
});

test('8. deadline: a launch that just passes the gate is settled at expires_at - 1 s, before the hub refuses posts', async t => {
  // The gate needs settle (raised to 30 s) + 10 min of life. The hub's clock
  // cannot be moved, so the daemon's clock starts behind real time by enough
  // to pass the gate with an expires_at 8 s away, then catches up.
  const lifeMs = 8000, behind = 30_000 + 600_000 + 2000 - lifeMs;
  const env = await setup(t, { idle: { settle_opaque: '30s', settle_observable: '30s' }, clockOffsetMs: -behind });
  const l = await running(env, { expiresInMs: lifeMs });
  env.clock.shift(behind);
  const done = await status(env, l.jobId, 'succeeded', lifeMs + 5000);
  const out = done.job_status!.output as { ended: string };
  assert.equal(out.ended, 'deadline');
  const expires = Date.parse(l.expiresAt);
  assert.ok(Date.parse(done.created_at) < expires, 'posted before the launch expired');
  assert.ok(Date.parse(done.created_at) >= expires - 1000 - 750, 'not settled long before expires_at - 1 s');
  assert.equal(env.fake().sessions()[0].settles, 1);
});

test('9. long reply: 60 KiB text with the marker, the full text attached as turn-1.md', async t => {
  const env = await setup(t);
  const l = await running(env);
  const line = 'the agent wrote a long and careful reply line\n';
  const full = line.repeat(Math.ceil(100 * 1024 / line.length)).slice(0, 100 * 1024);
  env.fake().finishTurn(l.jobId, full);
  const reply = await waitReply(env, l.thread);
  // The hub spills text over 4 KiB; the spill holds the launcher's capped text.
  const spill = reply.content.truncated ? reply.attachments.findLast(a => a.name === 'message.md') : undefined;
  const text = spill ? new TextDecoder().decode(await env.requester.hub.fetchBlob(spill.blob_url!, { maxBytes: 256 * 1024 })) : reply.content.text;
  assert.ok(Buffer.byteLength(text) <= 60 * 1024, `${Buffer.byteLength(text)} bytes`);
  assert.ok(Buffer.byteLength(text) > 60 * 1024 - 64);
  assert.ok(text.endsWith(`\n[truncated: ${100 * 1024} bytes; full text attached]`));
  assert.ok(full.startsWith(text.slice(0, text.lastIndexOf('\n[truncated:'))));
  const attached = reply.attachments.find(a => a.name === 'turn-1.md');
  assert.ok(attached?.blob_url, 'turn-1.md attachment with a blob_url');
  assert.equal(attached.media_type, 'text/markdown');
  const bytes = await env.requester.hub.fetchBlob(attached.blob_url, { maxBytes: 256 * 1024 });
  assert.equal(new TextDecoder().decode(bytes), full);
});

test('10a. restart: a turn finished while the daemon was down is replied to exactly once', async t => {
  const env = await setup(t, { idle: { park_after: '60s', settle_opaque: '120s', settle_observable: '120s' } });
  const l = await running(env);
  const before = env.fake();
  env.daemon.crash();
  before.finishTurn(l.jobId, 'finished while down');
  await env.start();
  assert.notEqual(env.fake(), before, 'the restarted daemon loaded a new fake from the state file');
  await waitReply(env, l.thread, m => m.content.text === 'finished while down');
  await sleep(1000);
  assert.equal(turnReplies(env, l.thread).length, 1);
  const history = await env.requester.history(l.thread);
  assert.equal(history.filter(m => m.content.text === 'finished while down').length, 1);
  assert.equal(env.fake().sessions().length, 1);
});

test('10b. restart: a session lost while the daemon was down ends failed outcome_unknown', async t => {
  const env = await setup(t);
  const l = await running(env);
  const before = env.fake();
  env.daemon.crash();
  before.lose(l.jobId);
  await env.start();
  const failed = await status(env, l.jobId, 'failed');
  assert.equal((failed.job_status!.output as { error: string }).error, 'outcome_unknown');
  assert.equal(env.fake().sessions().length, 0);
});

test('10c. restart: a crash between launch and recording the ref yields exactly one session', async t => {
  const env = await setup(t, { noStart: true });
  await env.start({ beforeRecordRef: () => { throw new Error('process died'); } });
  const l = await launch(env);
  await status(env, l.jobId, 'accepted');
  await until(() => env.fake().sessions().length === 1, 'session launched');
  await sleep(200);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.sessionRef), null);
  env.daemon.crash();
  await env.start();
  await status(env, l.jobId, 'running');
  await sleep(500);
  assert.equal(env.fake().sessions().length, 1);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.sessionRef!.id), l.jobId);
  const history = await env.requester.history(l.thread);
  assert.equal(history.filter(m => m.job_status?.state === 'running').length, 1);
});

test('10d. restart: an unreachable backend keeps the job running and resumes when reattach succeeds', async t => {
  const env = await setup(t, { idle: { park_after: '10m', settle_opaque: '1h', settle_observable: '1h' } });
  const l = await running(env);
  const before = env.fake();
  env.daemon.crash();
  let reattaches = 0;
  onNextCreate(env.stateFile, b => {
    b.failReattach(2);
    const original = b.reattach.bind(b);
    b.reattach = (...args) => { reattaches++; return original(...args); };
  });
  await env.start();
  assert.notEqual(env.fake(), before);
  assert.equal(reattaches, 1);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.reattachPending), true);
  env.clock.shift(30_001);
  await until(() => reattaches === 2, 'second reattach attempt');
  await sleep(100);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.reattachPending), true);
  env.clock.shift(30_001);
  await until(() => reattaches === 3, 'third reattach attempt');
  await until(() => !env.inspect(ledger => ledger.job(l.jobId)!.reattachPending), 'reattached');
  const job = env.inspect(ledger => ledger.job(l.jobId)!);
  assert.equal(job.state, 'running');
  assert.equal(job.terminal, null);
  env.fake().finishTurn(l.jobId, 'resumed after reattach');
  await waitReply(env, l.thread, m => m.content.text === 'resumed after reattach');
  const history = await env.requester.history(l.thread);
  assert.deepEqual(history.filter(m => m.kind === 'job_status').map(m => m.job_status!.state), ['accepted', 'running']);
});

test('11. a launch that throws after creating its session is settled at once: failed backend_error, settled_orphan', async t => {
  const env = await setup(t);
  env.fake().throwAfterLaunch(new Error('lost the launch response'));
  const l = await launch(env);
  const failed = await status(env, l.jobId, 'failed');
  const out = failed.job_status!.output as { error: string; settled_orphan: boolean };
  assert.equal(out.error, 'backend_error');
  assert.equal(out.settled_orphan, true);
  assert.equal(env.fake().sessions().length, 1);
  assert.equal(env.fake().sessions()[0].settles, 1);
});

test('12. redelivery after a cursor reset: no second session, no duplicate replies', async t => {
  const env = await setup(t, { idle: { park_after: '60s', settle_opaque: '120s', settle_observable: '120s' } });
  const first = await headCursor(env.hub.url, env.launcher.secret);
  const l = await running(env);
  env.fake().finishTurn(l.jobId, 'turn one done');
  await waitReply(env, l.thread);
  await sleep(1500);
  const before = (await env.requester.history(l.thread)).map(m => m.message_id);
  const cursor = env.inspect(ledger => ledger.cursor());
  assert.ok(cursor && cursor !== first);
  await env.daemon.stop();
  env.inspect(ledger => ledger.setCursor(first));
  await env.start();
  // The replay re-admits every event after the first cursor, the launch included.
  await until(() => env.inspect(ledger => ledger.cursor()) === cursor, 'replay caught up');
  await sleep(1500);
  assert.equal(env.fake().sessions().length, 1);
  assert.equal(env.fake().sessions()[0].sent.length, 0);
  assert.equal(turnReplies(env, l.thread).length, 1);
  assert.deepEqual((await env.requester.history(l.thread)).map(m => m.message_id), before);
});

test('13. no credential leaks: the agent env has no VECNET_*, the daemon logs hold no secret or message text', async t => {
  const previous = process.env.VECNET_E2E_LEAK;
  process.env.VECNET_E2E_LEAK = 'must-not-reach-the-agent';
  t.after(() => { if (previous === undefined) delete process.env.VECNET_E2E_LEAK; else process.env.VECNET_E2E_LEAK = previous; });
  const env = await setup(t);
  const instructions = 'Instructions that must never be logged';
  // A backend error quoting a secret and the prompt: the daemon logs that it failed, never what it said.
  const fake = env.fake(), capabilities = fake.capabilities.bind(fake);
  let thrown = false;
  fake.capabilities = async () => {
    if (!thrown) { thrown = true; throw new Error(`${env.launcher.secret} ${instructions}`); }
    return capabilities();
  };
  const l = await running(env, { instructions });
  await env.requester.send({ thread_id: l.thread, in_reply_to: l.launchId, kind: 'message', priority: 'wake', content: { text: 'relayed text that must never be logged' } });
  await until(() => env.fake().sessions()[0].sent.length === 1, 'relayed message');
  env.fake().finishTurn(l.jobId, 'turn text that must never be logged');
  await waitReply(env, l.thread);
  await sleep(300);
  const spec = env.fake().sessions()[0].spec;
  assert.deepEqual(Object.keys(spec.env).filter(k => /^VECNET_/i.test(k)), []);
  assert.match(spec.prompt, /Instructions that must never be logged/);
  const logs = env.log.text();
  assert.ok(thrown);
  assert.ok(env.log.text().includes('backend capabilities unavailable'), 'the failure was logged');
  for (const secret of [env.launcher.secret, env.requester.secret, env.operator.secret, env.bystander.secret]) assert.ok(!logs.includes(secret), 'a secret was logged');
  for (const text of ['Instructions that must never be logged', 'relayed text that must never be logged', 'turn text that must never be logged']) assert.ok(!logs.includes(text), 'message text was logged');
});
