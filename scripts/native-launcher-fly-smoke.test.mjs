import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, cp, stat, rm, chmod } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { createHash } from 'node:crypto';
import { smokeSnapshotDigest } from './native-launcher-smoke-support.mjs';

const sha = x => createHash('sha256').update(x).digest('hex');
const launcher = 'peer_01M6TEST000000000000000001', sender = 'peer_01M6TEST000000000000000002';
const accepted = { job_id: 'job_01M6TEST000000000000000003', thread_id: 'thr_owned', message_id: 'msg_launch' };
const revision = 'a'.repeat(40), head = 'b'.repeat(40);
const exists = path => stat(path).then(() => true, () => false);

// All commands, network requests and RPC are synthetic. Real files are private
// fixtures, so the shared inventory, journal and teardown code still run.
async function fixture(t, fault) {
  const base = await mkdtemp(join(tmpdir(), 'fly-harness-test-'));
  t.after(() => rm(base, { recursive: true, force: true }));
  const dir = join(base, 'run'), source = join(base, 'source');
  await mkdir(dir, { mode: 0o700 }); await mkdir(source);
  const assets = {
    'connectors/launcher/src/main.ts': 'main', 'connectors/launcher/schema/input.json': '{}',
    'connectors/launcher/package.json': '{}', 'connectors/hub-client/src/client.ts': 'client',
    'connectors/hub-client/package.json': '{}', 'connectors/launcher/systemd/vecnet-launcher-profile.service.in': 'unit',
  };
  for (const [name, value] of Object.entries(assets)) { await mkdir(join(source, name, '..'), { recursive: true }); await writeFile(join(source, name), value); }
  const bin = join(base, 'vecnet'), demoBin = join(base, 'demo'), executable = join(base, 'codex');
  await writeFile(bin, 'CLI'); await writeFile(demoBin, 'demo'); await writeFile(executable, 'native');
  const digest = await smokeSnapshotDigest(source), snapshot = join(base, 'snapshots', digest);
  const approvalPath = join(base, 'approval.json');
  await writeFile(approvalPath, JSON.stringify({ source_revision: revision, snapshot_sha256: digest,
    cli_sha256: fault === 'identity' ? '0'.repeat(64) : sha('CLI'), demo_sha256: sha('demo'),
    native: { executable, executable_sha256: sha('native'), version: 'fixture-native-1', model: 'fixture-model', model_provider: 'fixture-provider', effort: 'high' } }), { mode: 0o600 });
  const handoffPath = join(base, 'handoff.json');
  const handoff = { version: 1, disposable: true, hub: 'https://vecnet.xk.io',
    launcher: { peer_id: fault === 'persistent' ? 'peer_01M6WG7VWA3M2CYPWPN49TNZTQ' : launcher, token: 'synthetic-launcher-token' },
    sender: { peer_id: sender, token: 'synthetic-sender-token' } };
  await writeFile(handoffPath, JSON.stringify(handoff), { mode: 0o600 });
  if (fault === 'authority-parent') await chmod(base, 0o755);
  const config = join(dir, 'config.toml'), unit = join(base, 'owned.service'), unitBytes = 'exact owned service';
  const setupPath = join(dir, 'launcher', 'config.json'), workspace = join(dir, 'workspaces', accepted.job_id);
  const trace = [], requests = [], journals = [], historyReads = [];
  let followup = false, cancelled = false, installed = false;
  const session = { backend: 'codex-app-server', id: 'native_owned', extra: { workspace, workspace_mode: 'worktree',
    branch: 'launch/' + accepted.job_id, permission_mode: 'read-only', model: 'fixture-model', model_provider: fault === 'provider' ? 'unapproved-provider' : 'fixture-provider', effort: 'high' } };
  const message = (message_id, extra) => ({ message_id, thread_id: accepted.thread_id, sender: launcher, ...extra });
  const history = () => [
    message('msg_accepted', { kind: 'job_status', job_status: { job_id: accepted.job_id, state: 'accepted' } }),
    message('msg_running', { kind: 'job_status', job_status: { job_id: accepted.job_id, state: 'running', output: { session_ref: session } } }),
    message('msg_first', { kind: 'message', content: { text: 'VN_FLY_LAUNCH_OK' } }),
    ...(followup ? [message('msg_second', { kind: 'message', content: { text: 'VN_FLY_FOLLOWUP_OK' }, metadata: { 'launch.consumed': ['msg_followup'] } })] : []),
    ...(cancelled ? [message('msg_cancelled', { thread_id: fault === 'foreign-terminal' ? 'thr_foreign' : accepted.thread_id,
      job_status: { job_id: fault === 'wrong-job' ? 'job_foreign' : accepted.job_id, state: fault === 'wrong-terminal' ? 'succeeded' : 'cancelled' } })] : []),
  ];
  const run = async (command, args, opts = {}) => {
    trace.push({ command, args, input: opts.input, env: opts.commandEnv });
    if (command === bin && args.includes('enrol')) {
      assert.ok(!args.some(arg => /synthetic-.*-token/.test(arg)), 'tokens must use stdin');
      assert.equal(opts.commandEnv.VECNET_CONFIG, config); assert.equal(opts.commandEnv.VECNET_CODEX_AUTO_DELIVERY, '0');
      await writeFile(config, 'synthetic credentials', { mode: 0o600 });
      return JSON.stringify({ peer_id: args[args.indexOf('--profile') + 1] === 'fly-launcher' ? handoff.launcher.peer_id : sender });
    }
    if (command === bin && args.includes('profile')) return JSON.stringify({ peer_id: args.includes('fly-launcher') ? handoff.launcher.peer_id : sender, hub: handoff.hub });
    if (command === bin && args.includes('setup')) {
      await mkdir(join(setupPath, '..'), { recursive: true });
      await writeFile(setupPath, JSON.stringify({ backends: [{ config: { workspace_root: join(dir, 'workspaces') } }] }));
      return JSON.stringify({ peer_id: launcher, config_path: setupPath });
    }
    if (command === bin && args.includes('install')) {
      if (args.includes('--write')) {
        if (fault === 'partial-install') throw new Error('synthetic partial install');
        await writeFile(unit, unitBytes); await cp(source, snapshot, { recursive: true }); installed = true;
      }
      return JSON.stringify({ profile: 'fly-launcher', config_path: setupPath, unit_path: unit, unit: unitBytes, runtime: { source, snapshot, vecnet_bin: bin, entry: join(snapshot, 'connectors/launcher/src/main.ts') } });
    }
    if (command === 'git') {
      if (args.includes('--abbrev-ref') || args.includes('symbolic-ref')) return args[1] === workspace ? session.extra.branch + '\n' : 'master\n';
      if (args.includes('rev-parse')) return (args[1] === source ? revision : head) + '\n';
      if (args.includes('status')) return ' M README.md\0?? sentinel.untracked\0';
      return '';
    }
    if (command === 'systemctl') {
      if (fault === 'signal' && args.includes('restart')) throw new Error('host command failed: signal');
      if (fault === 'stop' && args.includes('stop')) throw new Error('synthetic stop refused');
      if (args.includes('show')) return fault === 'active' ? 'ActiveState=active\nMainPID=99\n' : 'ActiveState=inactive\nMainPID=0\n';
      if (args.includes('is-active')) return 'active\n';
    }
    return '';
  };
  const journal = () => readFile(join(dir, 'recovery.json'), 'utf8').then(JSON.parse, () => null);
  const api = async (method, path, body) => {
    trace.push({ method, path });
    if (method === 'GET' && path === '/v1/peers') return { peers: [{ peer_id: launcher, capabilities: ['project:vecnet'], quota: { backends: { 'codex-app-server': 'ok' } } }] };
    if (method === 'GET') {
      assert.ok(path.startsWith('/v1/threads/' + accepted.thread_id + '/'), 'never read another thread');
      historyReads.push(path); return { messages: history() };
    }
    if (body.job_request) {
      journals.push(await journal()); requests.push(structuredClone(body));
      if (fault === 'lost-response') throw new Error('synthetic accepted response lost');
      return { ...accepted, ...(fault === 'retry-thread' && requests.length > 1 ? { thread_id: 'thr_foreign' } : {}) };
    }
    if (body.job_status) { cancelled = true; return { message_id: 'msg_cancel_request' }; }
    followup = true; return { message_id: 'msg_followup' };
  };
  const options = { dir, source, bin, demoBin, approvalPath, handoffPath, codexHome: join(base, 'synthetic-codex-home'), launcherProfile: 'fly-launcher', senderProfile: 'fly-sender' };
  const ports = { run, api, openRpc: async () => ({ version: fault === 'runtime-identity' ? 'unapproved-native' : 'fixture-native-1', close() {}, async call(method, params) {
    assert.equal(method, 'thread/read'); assert.equal(params.threadId, 'native_owned'); return { thread: { cwd: workspace } };
  } }), until: async (fn, label) => { for (let i = 0; i < 3; i++) { const value = await fn(); if (value) return value; } throw new Error('timed out: ' + label); } };
  const execute = async () => (await import('./native-launcher-fly-smoke.mjs')).runFlySmoke(options, ports);
  return { execute, trace, requests, journals, journal, dir, config, unit, historyReads, installed: () => installed };
}

test('Fly success binds owned identities, journals exact wire bytes and preserves authority for operator disable', async t => {
  const f = await fixture(t); const receipt = await f.execute();
  assert.equal(receipt.runtime_pass, true); assert.equal(receipt.pass, undefined);
  assert.deepEqual(receipt.operator_cleanup_pending, [launcher, sender]);
  assert.equal(receipt.cancellation.acknowledgement.sender, launcher);
  assert.equal(receipt.cancellation.acknowledgement.thread_id, accepted.thread_id);
  assert.equal(f.requests.length, 2); assert.deepEqual(f.requests[0], f.requests[1]);
  assert.deepEqual(f.journals[0].request, f.requests[0], 'durable before the first wire send');
  assert.deepEqual(f.journals[1].accepted, accepted, 'all accepted identifiers durable before retry');
  assert.equal(receipt.launch.message_id, accepted.message_id);
  assert.equal(await exists(f.unit), false); assert.equal(await exists(f.config), true);
  assert.equal((await stat(f.dir)).mode & 0o777, 0o700);
  assert.equal((await stat(join(f.dir, 'recovery.json'))).mode & 0o777, 0o600);
  assert.ok(f.trace.some(x => x.args?.includes('restart')));
});

for (const fault of ['lost-response', 'partial-install', 'signal', 'stop', 'active', 'retry-thread', 'wrong-job', 'foreign-terminal', 'provider']) {
  test('Fly retains run-owned recovery material after ' + fault, async t => {
    const f = await fixture(t, fault); await assert.rejects(f.execute(), error => error.code !== 'ERR_MODULE_NOT_FOUND');
    assert.equal(await exists(f.config), true); assert.equal(await exists(join(f.dir, 'recovery.json')), true);
    assert.equal(await exists(join(f.dir, 'receipt.json')), false);
    if (fault === 'lost-response') { assert.deepEqual((await f.journal()).request, f.requests[0]); assert.equal(f.historyReads.length, 0); }
    if (fault === 'retry-thread') assert.ok(f.historyReads.every(path => !path.includes('thr_foreign')));
  });
}
test('Fly wrong terminal allows exact service teardown but never claims cancellation PASS', async t => {
  const f = await fixture(t, 'wrong-terminal'); await assert.rejects(f.execute(), /cancel/i);
  assert.equal(await exists(f.unit), false); assert.equal(await exists(f.config), true);
  assert.equal(await exists(join(f.dir, 'receipt.json')), false);
});
for (const fault of ['identity', 'runtime-identity', 'persistent']) {
  test('Fly rejects ' + fault + ' before enrolment or network dispatch', async t => {
    const f = await fixture(t, fault); await assert.rejects(f.execute(), /approved|persistent/i);
    assert.equal(f.trace.some(x => x.args?.includes('enrol')), false); assert.equal(f.requests.length, 0);
  });
}
test('Fly refuses invitation authority in a nonprivate parent before enrolment', async t => {
  const f = await fixture(t, 'authority-parent');
  await assert.rejects(f.execute(), /private|authority/i);
  assert.equal(f.trace.some(x => x.args?.includes('enrol')), false);
});
