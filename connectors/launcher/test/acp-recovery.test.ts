import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import { mkdir, readFile, stat, realpath } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import plugin from '../src/backends/acp/index.ts';
import type { PluginContext, SessionHandle, SessionHooks, TurnEvent } from '../src/backend.ts';
import { openLedger } from '../src/ledger.ts';
import { createOutbox } from '../src/outbox.ts';
import { createJobReporter } from '../src/jobs.ts';
import { parseConfig } from '../src/config.ts';
import { startLaunch, recoverFailedLaunch } from '../src/launch.ts';
import type { LaunchDeps } from '../src/launch.ts';
import { createLane } from '../src/session.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { SendRequest, ReceiptBody } from '../../hub-client/src/wire.ts';
import { threadDelivery } from './fixtures.ts';
import { until } from './daemon-harness.ts';
import { acpTestDir } from './fixtures/acp-test-root.ts';

const hooks: SessionHooks = { onEvent() {}, async onPermissionRequest(r) { return { optionId: r.defaultOnExpiry, decidedBy: 'expired' }; },
  async onQuestion() { return { answers: {}, answeredBy: 'timeout' }; } };
async function fixture(t: TestContext, scenario: string, allocationFailure = false) {
  let handle: SessionHandle | undefined, closeResources: () => unknown | Promise<unknown> = () => {};
  const dir = await acpTestDir(t, 'acp-fix1-recovery-', () => closeResources());
  const clock = fakeClock(Date.parse('2026-11-08T00:00:00Z')), log = memoryLog(), ledger = openLedger(':memory:', clock);
  closeResources = async () => { try { if (handle) await handle.settle('cancel'); } finally { ledger.close(); } };
  const outbox = createOutbox({ ledger, clock, log, hub: { async send() { throw new Error('no hub'); }, async receipt() {} } });
  const jobs = createJobReporter({ ledger, outbox });
  const cfg = parseConfig({ host: 'test-host', hub: { url: 'http://127.0.0.1:9', credential_file: '/synthetic' }, state_dir: dir, backends: [], projects: {} });
  const jobId = 'job_00000000000000000000000000';
  ledger.createJob({ jobId, threadId: 'thr_test', launchMessageId: 'msg_launch', requester: 'peer_parent', policyKey: 'default', backend: 'acp',
    preset: 'fake', permissionMode: 'ask', project: 'repo', title: 'Recovery', instructions: 'synthetic',
    input: { backend: 'acp', project: 'repo', preset: 'fake', permission_mode: 'ask' }, tag: `vl-${jobId}`, state: 'accepted',
    sessionRef: null, sessionKind: null, turnCursor: null, branch: null, workspace: null, lastActivityAt: clock.now(), parkedAt: null, deadlineAt: clock.now() + 1000000, createdAt: clock.now() });
  jobs.report(jobId, 'accepted');
  const wirePath = join(dir, 'wire.jsonl'), repo = join(dir, 'repo'); await mkdir(repo);
  const ctx: PluginContext = { host: 'test-host', now: () => new Date(clock.now()), log, store: ledger.kv('plugin:acp'), redact: s => s,
    sleep: (ms, signal) => new Promise((resolve, reject) => {
      const timer = setTimeout(done, ms); function done() { signal?.removeEventListener('abort', abort); resolve(); }
      function abort() { clearTimeout(timer); signal?.removeEventListener('abort', abort); reject(new Error('aborted')); }
      signal?.addEventListener('abort', abort, { once: true }); if (signal?.aborted) abort();
    }),
    // Allocation is not the seam under test: emulate only Git's host-side port,
    // with a physical cwd. Launcher recovery, plugin, process and ledger are real.
    exec: async argv => {
      if (allocationFailure) return { code: 1, stdout: '', stderr: '' };
      if (argv.includes('worktree')) await mkdir(argv.at(-2)!, { recursive: true });
      return { code: 0, stdout: 'a'.repeat(40), stderr: '' };
    }, checkRoute: async () => { throw new Error('no routes'); }, readSecretFile: async () => { throw new Error('no auth'); } };
  const config = { workspace_root: join(dir, 'workspaces'), request_timeout_ms: 1000, presets: { fake: { provider: 'fixture', mode_id: 'ask', argv: [process.execPath,
    fileURLToPath(new URL('./fixtures/fake-acp-agent.mjs', import.meta.url)), scenario, wirePath] } } };
  const backend = await plugin.create(config, ctx);
  const deps: LaunchDeps = { ledger, jobs, backends: new Map([['acp', backend]]), hooks: () => hooks, clock, log, host: 'test-host', cfg,
    projects: () => ({ alias: 'repo', repoRoot: repo, baseRef: 'master' }) };
  const job = () => ledger.job(jobId)!;
  const calls = async (): Promise<Array<Record<string, any>>> => { try { return (await readFile(wirePath, 'utf8')).trim().split('\n').filter(Boolean).map(s => JSON.parse(s)); } catch { return []; } };
  const rows = () => ledger.pendingOutbox(1000, clock.now());
  // Drive only the launch lookup backoff after real I/O has progressed. No
  // global watchdog or other slot/job is used by these synthetic fixtures.
  async function launch() {
    let done = false; const pending = startLaunch(job(), deps);
    void pending.finally(() => { done = true; }).catch(() => {});
    while (!done) { await new Promise(resolve => setTimeout(resolve, 5)); clock.advance(30000); }
    const result = await pending; if (result.ok) handle = result.handle; return result;
  }
  return { dir, repo, ledger, clock, log, jobs, outbox, ctx, config, backend, deps, job, calls, rows, launch };
}

for (const scenario of ['v2', 'auth', 'no-mode', 'allocation-failure']) test(`I1 real startLaunch releases capacity after known failure: ${scenario}`, async t => {
  const f = await fixture(t, scenario, scenario === 'allocation-failure');
  assert.deepEqual(await f.launch(), { ok: false, error: 'backend_error' });
  assert.equal(f.job().terminal, 'failed', 'confirmed launch failure must terminate rather than remain unreachable');
  assert.equal(f.job().reattachPending, false); assert.deepEqual(f.ledger.counts().open, 0);
  const owned = JSON.parse(f.ctx.store.get(`session:${f.job().tag}`)!);
  assert.equal(owned.phase, 'lost');
  if (scenario !== 'allocation-failure') assert.ok((await stat(owned.workspace.path)).isDirectory());
  assert.equal(await f.backend.findByTag(f.job().tag), null);
  if (owned.id) assert.equal((await f.backend.reattach({ backend: 'acp', host: 'test-host', id: owned.id, extra: { tag: owned.tag } }, hooks)).found, 'none');
  // Recreate the same launch spec, not a different input fingerprint.
  const { buildLaunchSpec } = await import('../src/prompt.ts');
  await assert.rejects(f.backend.launch(buildLaunchSpec(f.job(), f.deps.projects(f.job()), await f.backend.capabilities(), Object.fromEntries(Object.entries(process.env).filter((e): e is [string,string] => e[1] !== undefined))), hooks), /outcome_unknown/);
  const count = (await f.calls()).filter(c => c.method === 'initialize').length;
  assert.equal(count, scenario === 'allocation-failure' ? 0 : 1);
  assert.equal(f.ctx.store.get(`session:${f.job().tag}`) !== undefined, true, 'backend intent fence retained');
});

test('I1 real recovery preserves uncertain previous-owner outcomes without spawning', async t => {
  const f = await fixture(t, 'hold'); const started = await f.launch(); assert.ok(started.ok);
  const restarted = await plugin.create(f.config, f.ctx);
  f.ledger.kv('launch-failure').set(f.job().jobId, JSON.stringify({ reason: 'uncertain owner' }));
  assert.equal(await recoverFailedLaunch(f.job(), restarted, { ...f.deps, backends: new Map([['acp', restarted]]) }, false), 'unreachable');
  assert.equal(f.job().terminal, null); assert.equal(f.ledger.counts().open, 1);
  assert.equal((await f.calls()).filter(c => c.method === 'session/new').length, 1);
});

test('I2 real lane drains delayed authoritative process exit after lost state, fails pending inputs and releases capacity', async t => {
  const originalSpawn = childProcess.spawn;
  let releaseClose: (() => void) | undefined, closeHeld = false;
  const mocked = t.mock.method(childProcess, 'spawn', (...args: Parameters<typeof childProcess.spawn>) => {
    const child = originalSpawn(...args);
    // taskkill is an implementation detail on Windows, not the ACP process
    // whose authoritative close this test intentionally delays.
    if (args[0] !== process.execPath) return child;
    const emit = child.emit;
    child.emit = function (name: string | symbol, ...values: any[]): boolean {
      if (name === 'close') { closeHeld = true; releaseClose = () => { Reflect.apply(emit, child, [name, ...values]); }; return true; }
      return Reflect.apply(emit, child, [name, ...values]);
    };
    return child;
  });
  syncBuiltinESMExports();
  t.after(() => { releaseClose?.(); mocked.mock.restore(); syncBuiltinESMExports(); });
  const f = await fixture(t, 'bad-stdout'); const started = await f.launch(); assert.ok(started.ok);
  const handle = started.handle, observed: TurnEvent[] = [];
  const wrapped: SessionHandle = { ref: handle.ref, state: () => handle.state(), send: (i,m) => handle.send(i,m), interrupt: r => handle.interrupt(r),
    activity: () => handle.activity(), settle: m => handle.settle(m),
    async *turns(after, signal) { for await (const item of handle.turns(after, signal)) { observed.push(item.event); yield item; } } };
  const pending = threadDelivery(f.job().threadId, 'peer_parent', 'pending input', 'hold');
  f.ledger.addInput({ deliveryId: pending.delivery_id, jobId: f.job().jobId, messageId: pending.message.message_id, sender: 'peer_parent', text: 'pending input', priority: 'hold', createdAt: f.clock.now() });
  const lane = createLane(f.job(), { ledger: f.ledger, outbox: f.outbox, jobs: f.jobs, clock: f.clock, log: f.log, self: 'peer_launcher', caps: await f.backend.capabilities(),
    dir: { name: id => id, isOperator: id => id === 'peer_operator', operatorPeerId: () => 'peer_operator' },
    hub: { async fetchBlob() { throw new Error('no blobs'); }, async putBlob() { throw new Error('no blobs'); } }, async deliver() { return { pushed: false }; }, onActivity() {}, onEnded() {} });
  t.after(async () => { releaseClose?.(); await lane.stop(); });
  lane.attach(wrapped);
  await until(() => observed.some(e => e.type === 'state' && e.state.kind === 'ended') && closeHeld, 'lost state consumed before held close');
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.equal(f.job().terminal, null, 'lost state alone must not fabricate process exit');
  releaseClose!(); releaseClose = undefined;
  await until(() => observed.some(e => e.type === 'process_exited'), 'authoritative process exit delivered');
  assert.equal(observed.filter(e => e.type === 'process_exited').length, 1, 'iterator must deliver one authoritative exit after the earlier lost state');
  await until(() => f.job().terminal === 'failed', 'terminal lane failure');
  assert.equal(f.ledger.counts().open, 0); assert.equal(f.ledger.inputs(f.job().jobId, ['failed']).length, 1);
  const receipt = f.rows().filter(r => r.kind === 'receipt').map(r => r.payload as ReceiptBody).find(r => r.delivery_id === pending.delivery_id);
  assert.deepEqual(receipt, { delivery_id: pending.delivery_id, state: 'failed', note: 'session_ended' });
  assert.equal(observed.some(e => e.type === 'turn_finished'), false);
  const status = f.rows().map(r => r.payload as SendRequest).find(r => r.job_status?.state === 'failed');
  assert.equal((status?.job_status?.output as Record<string, unknown> | undefined)?.error, 'session_exited');
});


test('fix2 real launcher recovery fixture launches through a persistent cache symlink with canonical paths', async t => {
  const f = await fixture(t, 'normal');
  assert.equal(f.dir, await realpath(f.dir), 'recovery fixture must supply canonical cwd/workspace paths');
  const result = await f.launch(); assert.ok(result.ok);
  assert.equal(result.handle.ref.extra?.workspace, await realpath(String(result.handle.ref.extra?.workspace)));
});
