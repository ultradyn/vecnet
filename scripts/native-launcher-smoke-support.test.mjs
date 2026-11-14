import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import * as fs from 'node:fs/promises';
import { join, basename, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { createHash } from 'node:crypto';
import { cleanupNativeSmoke } from './native-launcher-cleanup.mjs';
import { demoCloser } from './ui/lib.mjs';

const smokeURL = new URL('./native-launcher-live-smoke.mjs', import.meta.url);
const sha = x => createHash('sha256').update(x).digest('hex');
const exists = async path => fs.stat(path).then(() => true, () => false);
const job = { job_id: 'job_01M6TEST0000000000000000000', thread_id: 'thr_smoke', message_id: 'msg_launch' };
const launcher = 'peer_smoke';

// Execute the production script with synthetic ports, never an OS child,
// browser, provider, real config, credential, or service. Imports are rebound;
// all other control flow and assertions come from the unmodified source file.
async function fixture(t, options = {}) {
  const base = await fs.mkdtemp(join(tmpdir(), 'native-harness-fixture-'));
  t.after(() => fs.rm(base, { recursive: true, force: true }));
  const owned = join(base, 'owned'), hub = join(base, 'hub'), source = join(base, 'source');
  await fs.mkdir(owned, { mode: 0o700 }); await fs.mkdir(hub, { mode: 0o700 }); await fs.mkdir(source);
  await fs.writeFile(join(hub, 'hub.db'), 'synthetic-hub-history');
  const files = {
    'connectors/launcher/src/main.ts': 'fixture main',
    'connectors/launcher/src/backends/codex-app-server/index.ts': 'fixture native',
    'connectors/launcher/schema/input.json': '{}', 'connectors/launcher/package.json': '{}',
    'connectors/hub-client/src/client.ts': 'fixture client', 'connectors/hub-client/package.json': '{}',
    'connectors/launcher/systemd/vecnet-launcher-profile.service.in': 'fixture unit',
  };
  const hash = createHash('sha256');
  for (const name of Object.keys(files).sort()) {
    await fs.mkdir(join(source, name, '..'), { recursive: true }); await fs.writeFile(join(source, name), files[name]);
    hash.update(`${Buffer.byteLength(name)}:${name}:${Buffer.byteLength(files[name])}:`); hash.update(files[name]);
  }
  const snapshotHash = hash.digest('hex'), snapshot = join(base, 'snapshots', snapshotHash);
  const cli = join(base, 'vecnet'), demoBin = join(base, 'demo'), runtime = join(base, 'runtime');
  const packagePath = join(base, 'adapter-package.json'), entry = join(base, 'adapter-entry.js'), node = join(base, 'node');
  await fs.writeFile(cli, 'synthetic CLI'); await fs.writeFile(demoBin, 'synthetic demo');
  await fs.writeFile(runtime, 'synthetic runtime'); await fs.writeFile(packagePath, JSON.stringify({ name: '@agentclientprotocol/claude-agent-acp', version: '0.101.0' }));
  await fs.writeFile(entry, 'synthetic entry'); await fs.writeFile(node, 'synthetic Node');
  const approval = {
    source_revision: 'a'.repeat(40), snapshot_sha256: snapshotHash, cli_sha256: sha('synthetic CLI'), demo_sha256: sha('synthetic demo'),
    native: { executable: runtime, executable_sha256: sha('synthetic runtime'), version: '0.134.0' },
    acp: { executable: runtime, executable_sha256: sha('synthetic runtime'), package_json: packagePath, package_name: '@agentclientprotocol/claude-agent-acp',
      package_version: '0.101.0', entry, entry_sha256: sha('synthetic entry'), node_executable: node, node_sha256: sha('synthetic Node'), agent_name: 'claude-agent-acp', agent_version: '0.101.0', protocol_version: 1 },
  };
  options.approval?.(approval);
  const manifest = join(base, 'approval.json');
  if (!options.missingApproval) await fs.writeFile(manifest, JSON.stringify(approval));
  const unit = join(base, 'fixture.service'), unitBytes = 'synthetic exact owned unit';
  const configPath = join(owned, 'launcher', 'config.json');
  const backend = options.backend || 'codex-app-server', mode = options.mode || 'worktree';
  const workspace = mode === 'project-root' ? join(owned, 'repo') : join(owned, 'workspaces', job.job_id);
  const parent = { branch: 'master', head: 'b'.repeat(40), status: ' M README.md\0?? sentinel.untracked\0', tracked: 'dirty sentinel\n', untracked: 'untracked sentinel\n' };
  const after = { ...parent, ...options.parentAfter };
  const body = { idempotency_key: 'launch-synthetic', recipients: [launcher], kind: 'message', priority: 'wake',
    content: { text: 'synthetic marker prompt' }, expires_at: '2026-11-10T00:00:00.000Z',
    job_request: { version: 1, job_key: 'launch-synthetic', operation: 'launch', instructions: 'synthetic marker prompt',
      input: { backend, project: mode === 'temp' ? 'temporary' : 'canary', workspace_mode: mode, preset: 'default', permission_mode: backend === 'acp' ? 'ask' : 'read-only' } } };
  const state = { trace: [], logs: [], errors: [], dispatches: [], beforeDispatch: null, beforeRepeat: null, source, owned, hub, cli, manifest, body, approval, exit: 0 };
  let shot = false, followup = false, cancelled = false;
  const session = { backend, id: 'session_synthetic', extra: { workspace, workspace_mode: mode,
    branch: options.sessionBranch ?? (mode === 'worktree' || (mode === 'temp' && backend === 'codex-app-server') ? 'launch/' + job.job_id : mode === 'project-root' ? 'master' : undefined),
    permission_mode: backend === 'acp' ? 'ask' : 'read-only', ...(backend === 'acp' ? {} : { model: 'fixture-model', model_provider: 'fixture-provider', effort: 'high' }) } };
  const message = (message_id, fields) => ({ message_id, thread_id: job.thread_id, sender: launcher, ...fields });
  const history = () => [
    message('msg_accepted', { kind: 'job_status', job_status: { job_id: job.job_id, state: 'accepted' } }),
    message('msg_running', { kind: 'job_status', job_status: { job_id: job.job_id, state: 'running', output: { session_ref: session } } }),
    message('msg_first', { kind: 'message', content: { text: 'VN_WEB_LAUNCH_OK' } }),
    ...(followup ? [message('msg_second', { kind: 'message', content: { text: 'VN_WEB_FOLLOWUP_OK' }, metadata: { 'launch.consumed': ['msg_followup'] } })] : []),
    ...(shot && options.terminal ? [message('msg_terminal', { kind: 'job_status', job_status: { job_id: job.job_id, state: options.terminal } })] : []),
    ...(cancelled ? [message('msg_cancelled', { kind: 'job_status', job_status: { job_id: options.wrongCancelJob ? 'job_foreign' : job.job_id, state: 'cancelled' } })] : []),
  ];
  async function journal() { return fs.readFile(join(owned, 'recovery.json'), 'utf8').then(JSON.parse, () => null); }
  state.journal = journal;
  function spawn(command, args) {
    const child = new EventEmitter(); child.stdout = new EventEmitter(); child.stderr = new EventEmitter(); child.stdin = new EventEmitter();
    child.kill = signal => { child.emit('close', null, signal); };
    child.stdin.end = input => queueMicrotask(async () => {
      state.trace.push(['command', basename(command), ...args]);
      let output = '', code = 0, signal = null;
      try {
        if (command === cli && args.includes('setup')) {
          await fs.mkdir(join(configPath, '..'), { recursive: true });
          await fs.writeFile(join(owned, 'config.toml'), 'synthetic-only credential', { mode: 0o600 });
          await fs.writeFile(configPath, JSON.stringify({ backends: [{ config: { workspace_root: join(owned, 'workspaces') } }] }));
          output = JSON.stringify({ config_path: configPath });
        } else if (command === cli && args.includes('install')) {
          if (args.includes('--write')) {
            if (options.fault === 'partial') code = 1;
            else { await fs.writeFile(unit, unitBytes); await fs.cp(source, snapshot, { recursive: true }); }
          }
          output = JSON.stringify({ unit_path: unit, unit: unitBytes, config_path: configPath,
            runtime: { source, snapshot, entry: join(snapshot, 'connectors/launcher/src/main.ts'), vecnet_bin: cli, node: 'synthetic-node' } });
        } else if (command === 'git') {
          const where = args[1], last = args.at(-1), current = shot ? after : parent;
          if (args.includes('rev-parse')) output = args.includes('--abbrev-ref') ? (where === workspace && mode !== 'project-root' ? session.extra.branch || '' : current.branch) + '\n' : (where === source ? 'a'.repeat(40) : current.head) + '\n';
          else if (args.includes('symbolic-ref')) output = (where === workspace && mode !== 'project-root' ? session.extra.branch || '' : current.branch) + '\n';
          else if (args.includes('status')) output = current.status;
          else if (args.includes('ls-files')) output = 'README.md\0sentinel.untracked\0';
          else if (args.includes('show')) output = 'initial\n';
          else if (last === '--is-inside-work-tree') output = mode === 'temp' ? '' : 'true\n';
        } else if (command === 'systemctl') {
          if (args.includes('show')) output = options.fault === 'verifyStopped' ? 'ActiveState=active\nMainPID=99\n' : 'ActiveState=inactive\nMainPID=0\n';
          if (args.includes('is-active')) output = 'active\n';
          if (args.includes('stop') && options.fault === 'stopUnit') code = 1;
          if (args.includes('daemon-reload') && options.fault === 'reloadManager') code = 1;
          if (args.includes('daemon-reload') && options.killedReload) signal = 'SIGKILL', code = null;
        } else if (command === runtime) {
          output = JSON.stringify({ jsonrpc: '2.0', id: 1, result: { protocolVersion: options.incompatibleAgent ? 99 : 1,
            agentInfo: { name: 'claude-agent-acp', version: options.agentVersion || '0.101.0' } } }) + '\n';
        }
        child.stdout.emit('data', output); child.emit('close', code, signal);
      } catch (error) { child.emit('error', error); }
    });
    return child;
  }
  let requestReady, responseReady, routeHandler;
  const request = { url: () => 'http://synthetic.invalid/v1/messages', method: () => 'POST', postDataJSON: () => structuredClone(body) };
  const response = { ok: () => true, status: () => 200, json: async () => { if (options.lostResponse) throw new Error('synthetic lost accepted response'); return job; } };
  async function dispatch() {
    state.beforeDispatch = await journal(); state.dispatches.push(structuredClone(body));
    await fs.mkdir(workspace, { recursive: true });
    if (mode === 'temp' && backend === 'codex-app-server') await fs.mkdir(join(workspace, '.git'));
    await fs.writeFile(join(hub, 'hub.db'), JSON.stringify({ request: body, accepted: job }));
    requestReady?.(request); responseReady?.(response);
  }
  state.recover = async original => {
    const ledger = JSON.parse(await fs.readFile(join(hub, 'hub.db'), 'utf8'));
    assert.equal(JSON.stringify(original), JSON.stringify(ledger.request), 'recovery must replay the accepted bytes');
    return ledger.accepted;
  };
  const locator = { locator() { return this; }, getByRole(_role, opts) { return { ...this, async click() {
    if (opts.name === 'Create launcher setup') return;
    if (opts.name === 'Launch job') {
      if (routeHandler) await routeHandler({ request: () => request, continue: dispatch, fetch: async () => { await dispatch(); return response; }, fulfill: async () => {} });
      else await dispatch();
      if (options.interruptDispatch) throw new Error('synthetic interruption after dispatch');
    }
  } }; }, async fill() {}, async selectOption() {}, async waitFor() {}, async textContent() { return "printf %s 'synthetic-token' | vecnet enrol"; } };
  const page = { ...locator, async goto() {}, async waitForFunction() {}, getByText() { return { first: () => locator }; },
    async route(_glob, handler) { routeHandler = handler; },
    waitForRequest() { return new Promise(r => { requestReady = r; }); },
    waitForResponse(predicate) {
      const admin = { url: () => 'http://synthetic.invalid/v1/admin/agents', request: () => ({ method: () => 'POST' }), json: async () => ({ peer_id: launcher }) };
      try { if (predicate(admin)) return Promise.resolve(admin); } catch {}
      return new Promise(r => { responseReady = r; });
    } };
  async function withDemo(demoOptions, fn) {
    const demo = { url: 'http://synthetic.invalid', operator_secret: 'synthetic-operator-only', operator_peer: 'peer_synthetic_operator' };
    const child = new EventEmitter(); child.pid = 123456789; child.exitCode = null; child.signalCode = null;
    const close = demoCloser(child, hub, { recovery: demoOptions.recovery, killAfterMs: 5, signal: () => { child.exitCode = 0; child.emit('exit', 0); child.emit('close', 0); } }).close;
    try {
      await demoOptions.recovery?.onReady?.({ demo, dir: hub, pid: child.pid });
      await fn({ page, demo, async asPeer(_peer, method, path, value) {
        let result = {};
        if (path === '/v1/admin/peers') result = { peers: [{ peer: { peer_id: launcher, capabilities: ['project:canary'], quota: { backends: { [backend]: 'ok' } } } }] };
        else if (method === 'GET') result = { messages: history() };
        else if (value.job_request) {
          state.beforeRepeat = await journal(); if (options.interruptAccepted) throw new Error('synthetic interruption after acceptance'); result = job;
        } else if (value.job_status) { if (options.fault === 'settleJob') throw new Error('synthetic cancellation failure'); cancelled = true; result = { message_id: 'msg_cancel_request' }; }
        else { if (options.unavailableProvider) throw new Error('synthetic unavailable provider'); followup = true; result = { message_id: 'msg_followup' }; }
        return { ok: true, status: 200, json: async () => result };
      }, async shot() {
        shot = true;
        await fs.writeFile(join(owned, 'repo', 'README.md'), after.tracked);
        await fs.writeFile(join(owned, 'repo', 'sentinel.untracked'), after.untracked);
      } });
    } finally { await close(); }
  }
  const imports = {
    'node:child_process': { spawn }, 'node:fs/promises': { ...fs, mkdtemp: async () => owned,
      readFile: async (path, ...args) => options.fault === 'verifyUnit' && shot && path === unit ? 'changed unit' : fs.readFile(path, ...args),
      rm: async (path, ...args) => { state.trace.push(['remove', path]); if (options.fault === 'removeUnit' && path === unit) throw new Error('synthetic remove failure'); return fs.rm(path, ...args); },
      writeFile: async (path, ...args) => { state.trace.push(['write', path]); return fs.writeFile(path, ...args); } },
    './ui/lib.mjs': { withDemo, assert: (ok, msg) => assert.ok(ok, msg), root: source },
    '../connectors/launcher/src/codex/rpc.ts': { openCodexRpc: async () => ({ version: options.nativeVersion || '0.134.0', call: async () => ({ thread: { cwd: workspace } }), close() {} }) },
    './native-launcher-cleanup.mjs': { cleanupNativeSmoke },
  };
  const vars = {}, original = await fs.readFile(smokeURL, 'utf8');
  const sourceText = original.replace(/^import\s+\{([^}]+)\}\s+from\s+'([^']+)';/gm, (_line, names, spec) => {
    vars[spec] = names.trim().split(',').map(x => x.trim()); return '';
  });
  const bindings = { process: { env: { VECNET_NATIVE_LAUNCHER_SMOKE: '1', VECNET_BIN: cli, VECNET_UI_DEMO_BIN: demoBin,
    VECNET_SMOKE_APPROVAL: options.missingApproval ? undefined : manifest, VECNET_SMOKE_BACKEND: backend, VECNET_SMOKE_WORKSPACE: mode, VECNET_ACP_COMMAND: runtime, CODEX_HOME: join(base, 'synthetic-codex-home') },
    geteuid: () => process.geteuid(), exitCode: 0 }, console: { log: x => state.logs.push(x), error: x => state.errors.push(x) } };
  for (const [spec, names] of Object.entries(vars)) {
    const module = imports[spec] || await import(new URL(spec, smokeURL));
    for (const name of names) bindings[name] = module[name];
  }
  // Expire only polling waits; command watchdogs remain cancellable and do not
  // spawn timers/children. This makes missing acknowledgements fail promptly.
  let clock = 0;
  bindings.Date = class extends Date { static now() { return clock; } };
  bindings.setTimeout = (fn, ms) => { if (ms <= 250) { clock += 1_000_000; queueMicrotask(fn); } return {}; };
  bindings.clearTimeout = () => {};
  state.run = async () => {
    const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
    await new AsyncFunction(...Object.keys(bindings), sourceText)(...Object.values(bindings));
    state.exit = bindings.process.exitCode || 0;
    state.receipt = await fs.readFile(join(owned, 'receipt.json'), 'utf8').then(JSON.parse, () => null);
    return state;
  };
  return state;
}

for (const fault of ['partial', 'settleJob', 'verifyUnit', 'stopUnit', 'verifyStopped', 'removeUnit', 'reloadManager']) {
  test('H1 outer retains hub and private authority after ' + fault, async t => {
    const f = await fixture(t, { fault }); await f.run();
    assert.equal(f.exit, 1); assert.equal(await exists(join(f.owned, 'config.toml')), true);
    assert.equal(await exists(join(f.hub, 'hub.db')), true, 'outer lifecycle must retain the recovery hub');
    const authority = JSON.parse(await fs.readFile(join(f.owned, 'demo-recovery.json'), 'utf8'));
    assert.equal(authority.origin, 'http://synthetic.invalid'); assert.equal(authority.operator_secret, 'synthetic-operator-only');
    assert.equal(authority.sender_peer, 'peer_synthetic_operator');
    assert.equal((await fs.stat(join(f.owned, 'demo-recovery.json'))).mode & 0o777, 0o600);
    assert.equal((await fs.stat(f.owned)).mode & 0o777, 0o700);
    assert.equal(f.receipt?.pass, undefined);
  });
}

test('H2 a signalled reload never authorizes disposal or PASS', async t => {
  const f = await fixture(t, { killedReload: true }); await f.run();
  assert.equal(f.exit, 1); assert.equal(await exists(join(f.hub, 'hub.db')), true);
  assert.equal(await exists(join(f.owned, 'config.toml')), true); assert.equal(f.receipt?.pass, undefined);
});

for (const outcome of ['interruptDispatch', 'lostResponse', 'interruptAccepted']) {
  test('H3 durable request and authority survive ' + outcome, async t => {
    const f = await fixture(t, { [outcome]: true }); await f.run();
    assert.deepEqual(f.beforeDispatch?.request, f.body, 'exact body/key/deadline must precede dispatch');
    assert.deepEqual((await f.journal()).request, f.body);
    if (outcome === 'interruptAccepted') assert.deepEqual(f.beforeRepeat?.accepted, job, 'accepted ids precede the next await/retry');
    assert.equal(await exists(join(f.hub, 'hub.db')), true); assert.equal(f.exit, 1);
    assert.deepEqual(await f.recover((await f.journal()).request), job, 'recovered replay resolves the original job/thread/message');
    assert.equal((await fs.stat(join(f.owned, 'recovery.json'))).mode & 0o777, 0o600);
  });
}

for (const terminal of ['succeeded', 'failed']) {
  test('H4 ' + terminal + ' permits safe teardown but cannot pass cancellation', async t => {
    const f = await fixture(t, { terminal }); await f.run();
    assert.equal(f.exit, 1); assert.equal(f.receipt?.pass, undefined);
    assert.equal(await exists(join(f.owned, 'config.toml')), false, 'a proven terminal job still permits safe teardown');
  });
}
test('H4 cancellation for another job cannot pass', async t => {
  const f = await fixture(t, { wrongCancelJob: true }); await f.run();
  assert.equal(f.exit, 1); assert.equal(f.receipt?.pass, undefined);
  assert.equal(await exists(join(f.hub, 'hub.db')), true);
});
test('H4 successful receipt records matching cancellation only after teardown', async t => {
  const f = await fixture(t); await f.run(); assert.equal(f.exit, 0);
  assert.deepEqual(f.receipt.launch, job);
  assert.equal(f.receipt.cancellation.state, 'cancelled');
  assert.equal(f.receipt.cancellation.acknowledgement.message_id, 'msg_cancelled');
  assert.equal(f.receipt.cancellation.acknowledgement.job_id, job.job_id);
  assert.equal(f.receipt.cancellation.acknowledgement.sender, launcher);
  assert.equal(f.receipt.cancellation.acknowledgement.thread_id, job.thread_id);
  assert.equal(f.receipt.teardown.complete, true);
  const written = f.trace.findIndex(x => x[0] === 'write' && x[1].endsWith('/receipt.json'));
  const reloaded = f.trace.findIndex(x => x.includes('daemon-reload'));
  assert.ok(written > reloaded && reloaded >= 0); assert.equal(await exists(join(f.hub, 'hub.db')), false);
});

for (const [name, options] of [
  ['missing approval', { missingApproval: true }],
  ['wrong CLI digest', { approval: a => { a.cli_sha256 = '0'.repeat(64); } }],
  ['wrong source revision', { approval: a => { a.source_revision = 'f'.repeat(40); } }],
  ['wrong demo digest', { approval: a => { a.demo_sha256 = '0'.repeat(64); } }],
  ['wrong snapshot digest', { approval: a => { a.snapshot_sha256 = '0'.repeat(64); } }],
  ['wrong executable digest', { approval: a => { a.acp.executable_sha256 = '0'.repeat(64); }, backend: 'acp', mode: 'temp' }],
  ['wrong package version', { approval: a => { a.acp.package_version = 'wrong'; }, backend: 'acp', mode: 'temp' }],
  ['wrong adapter entry digest', { approval: a => { a.acp.entry_sha256 = '0'.repeat(64); }, backend: 'acp', mode: 'temp' }],
  ['wrong Node digest', { approval: a => { a.acp.node_sha256 = '0'.repeat(64); }, backend: 'acp', mode: 'temp' }],
  ['wrong native version', { nativeVersion: 'unapproved' }],
  ['wrong agent version', { backend: 'acp', mode: 'temp', agentVersion: 'unapproved' }],
  ['incompatible agent', { backend: 'acp', mode: 'temp', incompatibleAgent: true }],
]) {
  test('H5 rejects ' + name + ' before a launch despite matching markers', async t => {
    const f = await fixture(t, options); await f.run();
    assert.equal(f.exit, 1); assert.equal(f.dispatches.length, 0); assert.equal(f.receipt?.pass, undefined);
  });
}
test('H5 provider failure cannot pass even with approved ACP identity', async t => {
  const f = await fixture(t, { backend: 'acp', mode: 'temp', unavailableProvider: true }); await f.run();
  assert.equal(f.exit, 1); assert.equal(f.receipt?.pass, undefined);
});
test('H5 ACP receipt binds artifacts and states provider/model attestation limits', async t => {
  const f = await fixture(t, { backend: 'acp', mode: 'temp' }); await f.run(); assert.equal(f.exit, 0);
  const p = f.receipt.provenance;
  assert.equal(p.source_revision, f.approval.source_revision); assert.equal(p.snapshot_sha256, f.approval.snapshot_sha256);
  assert.equal(p.cli_sha256, f.approval.cli_sha256); assert.equal(p.runtime.package_version, '0.101.0');
  assert.equal(p.runtime.executable_sha256, f.approval.acp.executable_sha256); assert.equal(p.runtime.agent_version, '0.101.0');
  assert.equal(p.runtime.protocol_version, 1); assert.equal(p.provider_availability, 'model-turns-observed');
  assert.equal(p.effective_provider, null); assert.equal(p.effective_model, null); assert.equal(p.effective_effort, null);
  assert.match(p.attestation_limit, /not.*attest/i);
  assert.equal(f.receipt.workspace_proof.git_expected, false);
});

for (const [name, options] of [
  ['wrong project-root branch', { mode: 'project-root', sessionBranch: 'launch/foreign' }],
  ['wrong worktree branch', { sessionBranch: 'master' }],
  ['parent checkout mutation', { parentAfter: { branch: 'launch/foreign' } }],
  ['parent HEAD mutation', { parentAfter: { head: 'c'.repeat(40) } }],
  ['tracked sentinel mutation', { mode: 'project-root', parentAfter: { tracked: 'changed\n' } }],
  ['untracked sentinel mutation', { parentAfter: { untracked: 'changed\n' } }],
]) {
  test('H6 rejects ' + name + ' despite the correct cwd/session/markers', async t => {
    const f = await fixture(t, options); await f.run(); assert.equal(f.exit, 1); assert.equal(f.receipt?.pass, undefined);
  });
}
test('H6 dirty project-root preserves branch/HEAD and tracked/untracked sentinels', async t => {
  const f = await fixture(t, { mode: 'project-root' }); await f.run(); assert.equal(f.exit, 0);
  assert.deepEqual(f.receipt.workspace_proof.before, f.receipt.workspace_proof.after);
  assert.equal(f.receipt.workspace_proof.before.branch, 'master');
  assert.equal(f.receipt.provenance.runtime.version, '0.134.0');
});

test('H6 native temp has its owned Git branch while ACP temp remains without Git', async t => {
  const f = await fixture(t, { mode: 'temp' }); await f.run(); assert.equal(f.exit, 0);
  assert.equal(f.receipt.workspace_proof.git_expected, true);
  assert.equal(f.receipt.branch, 'launch/' + job.job_id);
  assert.deepEqual(f.receipt.workspace_proof.before, f.receipt.workspace_proof.after);
});

test('H2 host command accepts only normal zero, rejects both signals, nonzero and timeout', async t => {
  const source = await fs.readFile(smokeURL, 'utf8');
  let runner;
  if (source.includes('runHost')) runner = (await import('./native-launcher-smoke-support.mjs')).runHost;
  else {
    const original = source.slice(source.indexOf('async function run('), source.indexOf('\nconst cli ='));
    runner = async (command, args, opts) => {
      const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
      return new AsyncFunction('spawn', 'env', 'basename', 'dir', 'setTimeout', original + '\nreturn run("synthetic", [], {});')(opts.spawn, {}, basename, 'synthetic-private', fn => setTimeout(fn, 1));
    };
  }
  for (const [code, signal, timeout] of [[0, null, false], [1, null, false], [null, 'SIGKILL', false], [null, 'SIGTERM', false], [0, null, true]]) {
    await t.test(JSON.stringify({ code, signal, timeout }), async () => {
    let child;
    const spawn = () => {
      child = new EventEmitter(); child.stdout = new EventEmitter(); child.stderr = new EventEmitter(); child.stdin = new EventEmitter();
      child.kill = () => child.emit('close', 0, null);
      child.stdin.end = () => { if (!timeout) queueMicrotask(() => child.emit('close', code, signal)); };
      return child;
    };
    const promise = runner('synthetic', [], { spawn, timeoutMs: 1 });
    if (code === 0 && !signal && !timeout) await promise;
    else await assert.rejects(promise, /command|failed|signal|timeout/i);
    });
  }
});
