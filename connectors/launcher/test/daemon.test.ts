import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile, execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { BackendCapabilities, BackendHealth } from '../src/backend.ts';
import { createHubClient } from '../../hub-client/src/client.ts';
import type { HubApi } from '../../hub-client/src/ports.ts';
import { cursorPurgedHook, DaemonError, RECOVERY_EXIT, startDaemon, startFailure } from '../src/daemon.ts';
import { HubError } from '../../hub-client/src/errors.ts';
import { createFakeBackend } from './fake-backend.ts';
import { harness, launch, launched, message, settle, until, LAUNCHER, REQUESTER, T0 } from './daemon-harness.ts';
import { directoryEntry, fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';

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

test('daemon idle clock parks at 2 s and settles at 4 s of logical time', async t => {
  const h = await harness(t, { cfg: {
    idle: { park_after: '2s', settle_opaque: '4s', settle_observable: '4s', check_every: '250ms', deadline_margin: '1s' },
  } });
  const { jobId } = await launched(h);
  h.backend.finishTurn(jobId, 'all done');
  await until(() => h.inspect(l => l.job(jobId)!.sessionKind === 'idle'), 'persisted idle turn');
  const start = h.clock.now();
  assert.equal(h.inspect(l => l.job(jobId)!.lastActivityAt), start);

  // Real I/O may take time, but the injected Clock stays fixed while we wait.
  // Unlike the wall-clock E2E ceiling, a park at 5 s or settle at 7 s cannot pass.
  h.clock.advance(0);
  await settle();
  h.clock.advance(1999);
  await settle();
  assert.equal(h.backend.sessions()[0].parks, 0, 'does not park before 2 s');
  assert.equal(h.backend.sessions()[0].settles, 0);
  h.clock.advance(1);
  await until(() => h.inspect(l => l.job(jobId)!.sessionKind === 'parked'), 'park at exactly 2 s');
  assert.equal(h.clock.now() - start, 2000);
  assert.equal(h.backend.sessions()[0].parks, 1);
  assert.equal(h.inspect(l => l.job(jobId)!.parkedAt), start + 2000);
  await settle();

  h.clock.advance(1999);
  await settle();
  assert.equal(h.backend.sessions()[0].settles, 0, 'does not settle before 4 s');
  h.clock.advance(1);
  await until(() => h.inspect(l => l.job(jobId)!.state === 'ended'), 'settle at exactly 4 s');
  assert.equal(h.clock.now() - start, 4000);
  assert.equal(h.backend.sessions()[0].settles, 1);
  assert.equal(h.backend.sessions()[0].parks, 1);
  await until(() => h.statuses(jobId).some(s => s.state === 'succeeded'), 'idle terminal report');
  const done = h.statuses(jobId).find(s => s.state === 'succeeded')!;
  const out = done.output as { ended: string; quiescence: string; turns: number };
  assert.equal(out.ended, 'idle_wrapup');
  assert.equal(out.quiescence, 'proven');
  assert.equal(out.turns, 1);
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

test('deliver.push_branch pushes only the launch branch to the operator checkout\'s origin, running none of the workspace\'s hooks or remotes', async t => {
  const root = mkdtempSync(join(tmpdir(), 'launcher-push-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const git = (...args: string[]) => execFileSync('git', ['-c', 'user.name=t', '-c', 'user.email=t@example.invalid', '-c', 'commit.gpgsign=false', ...args], { encoding: 'utf8' }).trim();
  const bare = join(root, 'origin.git'), decoy = join(root, 'decoy.git'), repo = join(root, 'repo'), marker = join(root, 'hook-ran');
  git('init', '-q', '--bare', bare); git('init', '-q', '--bare', decoy);
  git('init', '-q', '-b', 'master', repo); writeFileSync(join(repo, 'f'), 'x'); git('-C', repo, 'add', 'f'); git('-C', repo, 'commit', '-qm', 'base');
  git('-C', repo, 'remote', 'add', 'origin', bare);
  const backend = createFakeBackend({ clock: fakeClock(T0) });
  const launchOriginal = backend.launch.bind(backend);
  backend.launch = async (spec, hooks, signal) => {
    const handle = await launchOriginal(spec, hooks, signal);
    const ws = join(root, `ws-${spec.jobId}`);
    git('clone', '-q', repo, ws); git('-C', ws, 'checkout', '-qb', spec.branch);
    // What an agent could plant: a hook, and a remote of its choosing.
    git('-C', ws, 'remote', 'set-url', 'origin', decoy);
    writeFileSync(join(ws, '.git', 'hooks', 'pre-push'), `#!/bin/sh\ntouch ${marker}\n`, { mode: 0o755 });
    (handle.ref.extra as Record<string, unknown>).workspace = ws;
    return handle;
  };
  const h = await harness(t, { backend, cfg: { projects: { repo: { repo_root: repo, base_ref: 'master' } } } });
  const { jobId, thread } = await launched(h, { deliver: { push_branch: true } });
  const control = message(thread, REQUESTER, 'settle');
  control.message.metadata['launch.control'] = 'settle';
  await h.push('delivery', control);
  await until(() => h.statuses(jobId).some(s => s.state === 'succeeded'), 'succeeded', 20_000);
  assert.equal((h.statuses(jobId).at(-1)!.output as { pushed: boolean }).pushed, true);
  assert.match(git('--git-dir', bare, 'show-ref'), new RegExp(`refs/heads/launch/${jobId}`));
  assert.equal(git('--git-dir', decoy, 'for-each-ref'), '');
  assert.throws(() => readFileSync(marker), /ENOENT/);
});

test('a purged event cursor is a durable recovery condition: the hook rejects, restarts refuse, and only --accept-cursor-gap resumes from the oldest cursor', async t => {
  const h = await harness(t);
  const log = memoryLog();
  const kv = new Map<string, string>();
  await assert.rejects(cursorPurgedHook({ set: (k, v) => { kv.set(k, v); } }, log, h.clock)({ oldestCursor: 'e1.500' }),
    (err: unknown) => err instanceof DaemonError && err.code === 'cursor_purged' && err.exitCode === RECOVERY_EXIT);
  assert.equal(JSON.parse(kv.get('recovery')!).oldest_cursor, 'e1.500');
  // The real daemon records it in its ledger; a restart then refuses to start.
  h.daemon.crash();
  h.inspect(l => l.kv('daemon').set('recovery', kv.get('recovery')!));
  await assert.rejects(h.restart(), (err: unknown) => err instanceof DaemonError && err.code === 'cursor_purged' && /--accept-cursor-gap/.test(err.message));
  await h.restart({ acceptCursorGap: true });
  h.inspect(l => { assert.equal(l.kv('daemon').get('recovery'), undefined); assert.equal(l.cursor(), 'e1.500'); });
  assert.match(startFailure(new DaemonError('cursor_purged', 'cursor_purged: fixed text', RECOVERY_EXIT)).line, /fixed text/);
  assert.equal(startFailure(new DaemonError('x', 'm', RECOVERY_EXIT)).code, RECOVERY_EXIT);
  assert.deepEqual(startFailure(new Error('/home/secret/path: vn_peer_x')), { line: 'launcher failed to start', code: 1 });
  const unit = readFileSync(new URL('../systemd/vecnet-launcher.service', import.meta.url), 'utf8');
  assert.match(unit, new RegExp(`^RestartPreventExitStatus=${RECOVERY_EXIT}$`, 'm'));
});

test('a pinned identity survives a hub 5xx or 429 at startup, but not a 401 or a different peer', async t => {
  const h = await harness(t);
  for (const status of [502, 503, 429]) await h.restart({ me: async () => { throw new HubError(status, 'whoami_failed'); } });
  await assert.rejects(h.restart({ me: async () => { throw new HubError(401, 'whoami_failed'); } }), (err: unknown) => err instanceof HubError && err.status === 401);
  await assert.rejects(h.restart({ me: async () => ({ kind: 'peer', peer_id: 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8E' }) }),
    (err: unknown) => err instanceof DaemonError && err.code === 'identity_changed');
});

test('requesters retired while the daemon was down are found by the first successful refresh after a failed startup refresh', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  h.hub.setPeers([
    directoryEntry({ peer_id: LAUNCHER, name: 'launcher-test', kind: 'launcher' }),
    directoryEntry({ peer_id: 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8D', name: 'operator', kind: 'operator' }),
  ]);
  h.hub.failNext('/v1/peers', 401);
  await h.restart();
  await settle();
  assert.equal(h.backend.sessions()[0].interrupts, 0);
  await h.push('directory_updated', { change: 'updated', peer_id: LAUNCHER });
  await until(() => h.backend.sessions()[0].interrupts === 1, 'interrupt after the first good refresh');
  await until(() => h.replies(thread).some(r => /retired/.test(r.content.text)), 'retirement note');
  h.clock.advance(10 * 60_000);
  await until(() => h.statuses(jobId).some(s => s.state === 'cancelled'), 'cancelled requester_retired');
});
