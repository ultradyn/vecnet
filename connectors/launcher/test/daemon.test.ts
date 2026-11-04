import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { BackendCapabilities, BackendHealth } from '../src/backend.ts';
import { createHubClient } from '../../hub-client/src/client.ts';
import type { HubApi } from '../../hub-client/src/ports.ts';
import { startDaemon } from '../src/daemon.ts';
import { createFakeBackend } from './fake-backend.ts';
import { harness, launch, launched, message, settle, until, LAUNCHER, REQUESTER, T0 } from './daemon-harness.ts';
import { fakeClock } from '../../hub-client/testing/fixtures.ts';

test('a crash after the accept tx and before startLaunch → restart launches exactly once', async t => {
  const h = await harness(t);
  // After startup, hang the launch path's capability read: the accept tx has
  // committed, startLaunch has not claimed the job, and the process then dies.
  const caps = h.backend.capabilities.bind(h.backend);
  let hang = true;
  h.backend.capabilities = () => hang ? new Promise<BackendCapabilities>(() => {}) : caps();
  const d = launch(h.clock.now());
  const jobId = d.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', d);
  await until(() => h.statuses(jobId).some(s => s.state === 'accepted'), 'accepted job_status');
  await settle();
  h.inspect(l => {
    assert.equal(l.job(jobId)!.state, 'accepted');
    assert.equal(l.queued(10).length, 0);
  });
  assert.equal(h.launches(), 0);
  hang = false;
  await h.restart();
  await until(() => h.statuses(jobId).some(s => s.state === 'running'), 'running after restart');
  await settle();
  assert.equal(h.launches(), 1);
  assert.equal(h.backend.sessions().length, 1);
  assert.deepEqual(h.statuses(jobId).map(s => [s.sequence, s.state]), [[1, 'accepted'], [2, 'running']]);
  // The relaunched session's turn posts once, through the new daemon.
  h.backend.finishTurn(jobId, 'done after restart');
  await until(() => h.replies(d.message.thread_id).length === 1, 'turn reply');
  await settle();
  assert.equal(h.replies(d.message.thread_id).length, 1);
});

test('a kill between launch and recording the ref adopts the session by tag on restart', async t => {
  let die = true;
  const h = await harness(t, { beforeRecordRef: () => { if (die) throw new Error('process died'); } });
  const d = launch(h.clock.now());
  const jobId = d.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', d);
  await until(() => h.launches() === 1, 'launch call');
  await settle();
  assert.equal(h.inspect(l => l.job(jobId)!.sessionRef), null);
  die = false;
  await h.restart();
  await until(() => h.statuses(jobId).some(s => s.state === 'running'), 'adopted running');
  assert.equal(h.launches(), 1);
  assert.equal(h.inspect(l => l.job(jobId)!.sessionRef!.id), jobId);
});

test('crash() then a new daemon on the same ledger: open job reattached, no duplicate launch; a backend down at start keeps the job running and reattaches when it returns', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  await h.restart();
  await settle();
  assert.equal(h.launches(), 1);
  assert.equal(h.backend.sessions().length, 1);
  h.backend.finishTurn(jobId, 'turn one');
  await until(() => h.replies(thread).length === 1, 'reply after reattach');
  assert.equal(h.statuses(jobId).length, 2);

  // The backend is down when the next daemon starts.
  const health = h.backend.health.bind(h.backend);
  let down = true;
  h.backend.health = async (): Promise<BackendHealth> => down ? { ok: false, version: 'fake-1' } : health();
  h.backend.failReattach(1);
  await h.restart();
  await settle();
  h.inspect(l => {
    const job = l.job(jobId)!;
    assert.equal(job.state, 'running'); assert.equal(job.terminal, null); assert.equal(job.reattachPending, true);
  });
  assert.equal(h.statuses(jobId).some(s => s.state === 'failed'), false);
  down = false;
  h.clock.advance(30_000);
  await until(() => h.inspect(l => !l.job(jobId)!.reattachPending), 'reattached after the backend returned');
  h.backend.startTurn(jobId);
  h.backend.finishTurn(jobId, 'turn two');
  await until(() => h.replies(thread).length === 2, 'reply after the backend returned');
  assert.equal(h.launches(), 1);
  assert.equal(h.backend.sessions().length, 1);
  assert.deepEqual(h.statuses(jobId).map(s => s.state), ['accepted', 'running']);
});

test('a backend whose capabilities() fails at restart: the lane keeps its last known input cap and splits relayed input at it', async t => {
  const cap = 2000;
  const h = await harness(t, { backend: createFakeBackend({ caps: { maxInputBytes: cap } }) });
  const { jobId, thread } = await launched(h);
  h.backend.finishTurn(jobId, 'turn one');
  await until(() => h.replies(thread).length === 1, 'first turn reply');

  // A slow backend start: no capabilities and no reattach when the daemon starts.
  const caps = h.backend.capabilities.bind(h.backend);
  let down = true;
  h.backend.capabilities = async (): Promise<BackendCapabilities> => { if (down) throw new Error('bridge not up'); return caps(); };
  h.backend.failReattach(1);
  await h.restart();
  await until(() => h.inspect(l => l.job(jobId)!.reattachPending), 'reattach pending');
  down = false;
  h.clock.advance(30_000);
  await until(() => h.inspect(l => !l.job(jobId)!.reattachPending), 'reattached after the backend returned');

  const big = message(thread, REQUESTER, 'x'.repeat(5 * cap));
  await h.push('delivery', big);
  await until(() => h.backend.sessions()[0].sent.length >= 1, 'relayed input');
  const items = h.backend.sessions()[0].sent.flatMap(s => s.items);
  assert.equal(items[0].deliveryId, `${big.delivery_id}.part.1`);
  for (const item of items) assert.ok(Buffer.byteLength(item.text) <= cap, `${Buffer.byteLength(item.text)} bytes exceeds the backend cap`);
});

test('a fatal event stream end stops the daemon: stopped settles, exit code 1, offline presence', async t => {
  const h = await harness(t);
  await h.daemon.stop();
  const previous = process.exitCode;
  t.after(() => { process.exitCode = previous; });
  const client: HubApi & { me(): Promise<unknown> } = { ...createHubClient({ baseUrl: h.hub.url, credential: h.hub.bearer }), me: async () => ({ kind: 'peer', peer_id: LAUNCHER }) };
  const daemon = await startDaemon({ cfg: h.cfg, hub: client, clock: h.clock, log: h.log, backends: new Map([['fake', h.backend]]),
    stream: { run: async () => ({ reason: 'auth', status: 401 }) } });
  t.after(() => daemon.stop());
  await daemon.stopped;
  assert.equal(process.exitCode, 1);
  process.exitCode = previous;
  // One offline report from the harness daemon's stop, one from this daemon stopping itself.
  assert.equal(h.presence().filter(p => p.state === 'offline').length, 2);
});

test('stop() posts offline presence and leaves open jobs for the next start', async t => {
  const h = await harness(t);
  const { jobId } = await launched(h);
  await until(() => h.presence().some(p => p.state === 'working'), 'working presence');
  const first = h.presence()[0];
  assert.equal(first.state, 'idle');
  assert.ok((first.capabilities as string[]).includes('launch:fake'));
  assert.ok((first.capabilities as string[]).includes('preset:fake/test'));
  assert.ok((first.capabilities as string[]).includes('host:test-host'));
  await h.daemon.stop();
  assert.equal(h.presence().at(-1)!.state, 'offline');
  assert.equal(h.inspect(l => l.job(jobId)!.state), 'running');
  assert.equal(h.backend.sessions()[0].settles, 0);
});

function run(args: string[]): Promise<{ code: number; stdout: string; stderr: string }> {
  const main = fileURLToPath(new URL('../src/main.ts', import.meta.url));
  return new Promise(resolve => {
    execFile(process.execPath, ['--disable-warning=ExperimentalWarning', main, ...args], { timeout: 60_000 }, (err, stdout, stderr) => {
      const code = err ? (typeof (err as { code?: unknown }).code === 'number' ? (err as { code: number }).code : 1) : 0;
      resolve({ code, stdout, stderr });
    });
  });
}

test('main check prints health JSON and exits 1 when a backend is unhealthy', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'launcher-main-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const credential = join(dir, 'credential');
  writeFileSync(credential, 'vn_test_secret\n', { mode: 0o600 });
  const sick = join(dir, 'sick-plugin.mjs');
  writeFileSync(sick, `export default { id: 'sick', configSchema: {}, async create() { return { id: 'sick',
    async capabilities() { return { presets: [{ id: 'p1', provider: 'x', model: 'y' }], permissionModes: ['ask'] }; },
    async health() { return { ok: false, version: 'sick-2', degraded: ['bridge down'] }; } }; } };\n`);
  const base = { host: 'test-host', hub: { url: 'http://127.0.0.1:9', credential_file: credential }, state_dir: dir, projects: {} };
  const healthy = join(dir, 'healthy.json'), unhealthy = join(dir, 'unhealthy.json');
  writeFileSync(healthy, JSON.stringify({ ...base, backends: [{ id: 'fake', module: '../test/fake-backend-plugin.ts', config: {} }] }));
  writeFileSync(unhealthy, JSON.stringify({ ...base, backends: [{ id: 'fake', module: '../test/fake-backend-plugin.ts', config: {} }, { id: 'sick', module: sick, config: {} }] }));

  const ok = await run(['check', '--config', healthy]);
  assert.equal(ok.code, 0, ok.stderr);
  const okReport = JSON.parse(ok.stdout);
  assert.deepEqual(okReport.backends.map((b: { id: string; ok: boolean }) => [b.id, b.ok]), [['fake', true]]);
  assert.deepEqual(okReport.backends[0].presets, ['test']);

  const bad = await run(['check', '--config', unhealthy]);
  assert.equal(bad.code, 1);
  const report = JSON.parse(bad.stdout);
  assert.deepEqual(report.backends.map((b: { id: string; ok: boolean }) => [b.id, b.ok]), [['fake', true], ['sick', false]]);
  assert.equal(report.backends[1].version, 'sick-2');
  assert.deepEqual(report.backends[1].degraded, ['bridge down']);
  assert.deepEqual(report.backends[1].presets, ['p1']);
  assert.doesNotMatch(bad.stdout + bad.stderr, /vn_test_secret/);

  // A credential readable by others refuses to start, and usage errors exit 2.
  writeFileSync(join(dir, 'loose'), 'x', { mode: 0o644 });
  writeFileSync(join(dir, 'loose.json'), JSON.stringify({ ...base, hub: { ...base.hub, credential_file: join(dir, 'loose') }, backends: [] }));
  const loose = await run(['check', '--config', join(dir, 'loose.json')]);
  assert.equal(loose.code, 2);
  assert.match(loose.stderr, /credential/);
  assert.equal((await run(['bogus'])).code, 2);
});

test('the systemd unit runs main.ts from the installed tree with the planned limits', () => {
  const unit = readFileSync(new URL('../systemd/vecnet-launcher.service', import.meta.url), 'utf8');
  assert.match(unit, /^ExecStart=\/usr\/bin\/node --disable-warning=ExperimentalWarning %h\/\.local\/share\/vecnet-launcher\/current\/launcher\/src\/main\.ts run --config %h\/\.config\/vecnet-launcher\/config\.json$/m);
  for (const line of ['Restart=on-failure', 'RestartSec=10', 'UMask=0077', 'MemoryMax=1G', 'After=network-online.target t3code.service', 'WantedBy=default.target']) {
    assert.match(unit, new RegExp(`^${line.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`, 'm'));
  }
});

test('permission prompts are classified against the session worktree recorded at launch, not the project repo root', async t => {
  const backend = createFakeBackend({ clock: fakeClock(T0) });
  const launch = backend.launch.bind(backend);
  backend.launch = async (spec, hooks, signal) => {
    const handle = await launch(spec, hooks, signal);
    (handle.ref.extra as Record<string, unknown>).workspace = `/worktrees/${spec.jobId}`;
    return handle;
  };
  const h = await harness(t, { backend });
  const { jobId } = await launched(h);
  const options = [{ id: 'allow', label: 'Allow once', grants: 'once' as const }, { id: 'deny', label: 'Deny', grants: 'deny' as const }];
  void h.backend.requestPermission(jobId, { requestId: 'p1', summary: 'Write in the main checkout', options, defaultOnExpiry: 'deny',
    permission: { kind: 'edit', backend_method: 'file/edit', scope: 'once', options, paths: [{ path: '/repo/file', access: 'write' }],
      fidelity: { kind: 'enforced', command: 'none', paths: 'enforced', host: 'none' }, backend_raw: {} } });
  await until(() => h.sends().some(s => s.kind === 'approval_request'), 'approval_request');
  const permission = h.sends().find(s => s.kind === 'approval_request')!.metadata!['launch.permission'] as
    { workspace_root: string; outside_workspace: unknown; paths: Array<{ in_workspace: boolean }> };
  assert.equal(permission.workspace_root, `/worktrees/${jobId}`);
  assert.equal(permission.outside_workspace, true);
  assert.equal(permission.paths[0].in_workspace, false);
});

test('a plugin approval wait reported through hooks.onEvent reaches the requester as a note', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  h.backend.hookEvent(jobId, { type: 'progress', turnId: 'run-1', summary: 'waiting for approval in T3' });
  await until(() => h.replies(thread).some(r => /waiting for approval in T3/.test(r.content.text)), 'approval wait note');
});

test('the backend-unreachable note is sent after five minutes with no other event to wake the outbox', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  h.backend.failReattach(1000);
  await h.restart();
  await settle();
  const before = h.sends().length;
  for (let i = 0; i < 11; i++) { h.clock.advance(30_000); await settle(20); }
  await until(() => h.replies(thread).some(r => r.content.text.startsWith('backend unreachable')), 'unreachable note sent');
  assert.equal(h.replies(thread).filter(r => r.content.text.startsWith('backend unreachable')).length, 1);
  assert.ok(h.sends().length > before);
  h.inspect(l => assert.equal(l.job(jobId)!.terminal, null));
});
