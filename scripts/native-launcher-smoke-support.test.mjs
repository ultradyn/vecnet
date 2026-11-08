import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { spawn as spawnProcess } from 'node:child_process';
import * as fs from 'node:fs/promises';
import { join, basename, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { createHash } from 'node:crypto';
import { cleanupNativeSmoke } from './native-launcher-cleanup.mjs';
import { demoCloser } from './ui/lib.mjs';
import * as support from './native-launcher-smoke-support.mjs';

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
  const state = { trace: [], logs: [], errors: [], dispatches: [], replays: [], cancellations: [], historyReads: [],
    beforeDispatch: null, beforeRepeat: null, source, owned, hub, cli, manifest, body, approval, unit, configPath, exit: 0 };
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
  // This injected bounded-command port creates no OS children. A synthetic
  // close proves retirement; process containment is tested with real children
  // below, through runHost's default supervisor rather than a fake close event.
  const runSynthetic = (command, args, opts) => new Promise((resolve, reject) => {
    const child = spawn(command, args); let stdout = '';
    child.stdout.on('data', data => { stdout += data; });
    child.once('error', () => reject(Object.assign(new Error('synthetic host error'), { code: 'child_failed', drained: true })));
    child.once('close', (code, signal) => {
      if (signal || code !== 0) reject(Object.assign(new Error('synthetic host failure'), { code: 'child_failed', drained: true }));
      else resolve({ code, stdout, stderr: '' });
    });
    child.stdin.end(opts.input);
  });
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
        else if (method === 'GET') { state.historyReads.push(path); result = { messages: history() }; }
        else if (value.job_request) {
          state.beforeRepeat = await journal(); state.replays.push(structuredClone(value));
          if (options.interruptAccepted) { state.replayFault = 'lost-response'; throw new Error('synthetic interruption after acceptance'); }
          result = { ...job, ...(options.replayID ? { [options.replayID]: 'foreign_' + options.replayID } : {}) };
          state.replayReply = structuredClone(result);
        } else if (value.job_status) { state.cancellations.push(structuredClone(value)); if (options.fault === 'settleJob') throw new Error('synthetic cancellation failure'); cancelled = true; result = { message_id: 'msg_cancel_request' }; }
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
    './native-launcher-smoke-support.mjs': { ...support, runHost: async (command, args, opts) => {
      if (options.unprovedChild && command === 'systemctl' && args.includes('restart')) {
        state.trace.push(['unproved-child', command, ...args]);
        throw Object.assign(new Error('child_drain_unproved'), { code: 'child_drain_unproved', ambiguous: true, drained: false });
      }
      return support.runHost(command, args, { ...opts, run: runSynthetic });
    } },
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
    if (outcome === 'interruptAccepted') {
      assert.equal(f.replayFault, 'lost-response', 'the first acceptance succeeded and the actual replay lost its response');
      await assertUnresolvedReplayRetained(f);
    }
  });
}

async function assertUnresolvedReplayRetained(f) {
  const journal = await f.journal();
  assert.equal(f.exit, 1); assert.equal(f.receipt, null);
  assert.deepEqual(f.replays, [f.body], 'one replay uses the immutable original body/key/deadline');
  assert.deepEqual(f.beforeRepeat.accepted, job); assert.deepEqual(journal.accepted, job);
  assert.deepEqual(journal.request, f.body);
  assert.equal(f.beforeRepeat.launch_unresolved, true, 'the uncertainty hold is durable before dispatch');
  assert.equal(journal.launch_unresolved, true, 'unvalidated replay leaves its durable hold');
  assert.equal(await exists(join(f.owned, 'config.toml')), true, 'unresolved replay retains launcher credentials');
  assert.equal(await fs.readFile(join(f.owned, 'config.toml'), 'utf8'), 'synthetic-only credential');
  assert.equal(await exists(f.configPath), true, 'launcher configuration remains recoverable');
  assert.equal(await fs.readFile(f.unit, 'utf8'), 'synthetic exact owned unit');
  assert.deepEqual(await f.recover(journal.request), job, 'original hub still resolves the original accepted identifiers');
  const authority = JSON.parse(await fs.readFile(join(f.owned, 'demo-recovery.json'), 'utf8'));
  assert.equal(authority.operator_secret, 'synthetic-operator-only');
  assert.deepEqual(f.cancellations, [], 'unresolved replay cannot authorize cancellation of the original job');
  assert.deepEqual(f.historyReads, [], 'an unresolved replay cannot select a thread for settlement');
  assert.equal(f.trace.some(row => row[0] === 'command' && row[1] === 'systemctl' && row.some(x => ['stop', 'show', 'daemon-reload'].includes(x))), false);
  assert.equal(f.trace.some(row => row[0] === 'remove'), false, 'no unit or private authority removal');
  assert.equal(f.beforeRepeat.phase, 'retry-dispatched', 'durable retry intent must precede the actual replay');
  assert.equal(journal.phase, 'retry-dispatched', 'the unresolved retry is never journaled as complete');
}

for (const replayID of ['job_id', 'thread_id', 'message_id']) {
  test('F-HI1 local replay changed ' + replayID + ' retains authority without teardown', async t => {
    const f = await fixture(t, { replayID }); await f.run();
    assert.deepEqual(f.replayReply, { ...job, [replayID]: 'foreign_' + replayID }, 'the intended disagreeing replay reached the acceptance boundary');
    await assertUnresolvedReplayRetained(f);
  });
}

test('F-HI2 local unproved host child blocks cancellation and service teardown', async t => {
  const f = await fixture(t, { unprovedChild: true }); await f.run();
  const boundary = f.trace.findIndex(row => row[0] === 'unproved-child');
  assert.ok(boundary >= 0, 'uncertain retirement occurred at the owned restart after exact acceptance');
  assert.deepEqual((await f.journal()).accepted, job); assert.equal(f.exit, 1); assert.equal(f.receipt, null);
  const child = (await f.journal()).host_command;
  assert.equal(child.state, 'unresolved'); assert.equal(child.drained, false);
  assert.equal(child.command, 'systemctl'); assert.match(child.arguments_sha256, /^[a-f0-9]{64}$/);
  assert.deepEqual(f.cancellations, [], 'no cancellation can race a possibly active host writer');
  assert.equal(f.trace.slice(boundary + 1).some(row => ['command', 'remove'].includes(row[0])), false, 'no conflicting host operation follows uncertain retirement');
  assert.equal(await fs.readFile(join(f.owned, 'config.toml'), 'utf8'), 'synthetic-only credential');
  assert.equal(await exists(f.configPath), true); assert.equal(await exists(f.unit), true);
  assert.equal(await exists(join(f.hub, 'hub.db')), true);
  assert.equal(await exists(join(f.owned, 'demo-recovery.json')), true);
});

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
  for (const [code, signal, timeout] of [[0, null, false], [1, null, false], [null, 'SIGKILL', false], [null, 'SIGTERM', false], [0, null, true]]) {
    await t.test(JSON.stringify({ code, signal, timeout }), async () => {
      const program = timeout ? 'setTimeout(()=>process.exit(0),3000)' : signal ? `process.kill(process.pid,${JSON.stringify(signal)})`
        : `process.stdout.write('host stdout\\n',()=>process.exit(${code}))`;
      const promise = support.runHost(process.execPath, ['-e', program], { timeoutMs: timeout ? 100 : 2000 });
      if (code === 0 && !signal && !timeout) assert.equal(await promise, 'host stdout\n');
      else await assert.rejects(promise, error => {
        assert.match(error.message, /command|failed|signal|timeout/i);
        assert.equal(error.drained, true, 'an ordinary finite command has proved retirement even when the gate fails');
        return true;
      });
    });
  }
});

test('F-HI2 host intent and refusal survive reopening the private journal', async t => {
  const dir = await fs.mkdtemp(join(tmpdir(), 'native-host-journal-fixture-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true })); // no OS children in this fixture
  const journal = support.createSmokeJournal(dir, { accepted: job });
  let calls = 0, rejectRun;
  const ports = { getJournal: () => journal, commandEnv: { FIXTURE: 'private' }, run: async (_command, _args, options) => {
    calls++; assert.equal(options.commandEnv.FIXTURE, 'private');
    return new Promise((_resolve, reject) => { rejectRun = reject; });
  } };
  const runner = support.createSmokeHostRunner(ports);
  const pending = runner('synthetic-command', ['PRIVATE_ARGUMENT'], { input: 'PRIVATE_INPUT' });
  const load = async () => JSON.parse(await fs.readFile(join(dir, 'recovery.json'), 'utf8'));
  const before = await load();
  assert.equal(before.host_command.state, 'pending'); assert.equal(before.host_command.drained, false);
  assert.deepEqual(before.accepted, job);
  // A new instance sees the fsynced hold, independent of the first runner's
  // memory, and cannot invoke its supplied command port at all.
  const reopened = support.createSmokeHostRunner({ ...ports, getJournal: () => ({ snapshot: () => before }) });
  await assert.rejects(reopened('conflicting-command', []), /retirement unresolved/);
  rejectRun(Object.assign(new Error('PRIVATE_OUTPUT'), { code: 'child_drain_unproved', drained: false }));
  await assert.rejects(pending, { code: 'child_drain_unproved' });
  const after = await load();
  assert.equal(after.host_command.id, before.host_command.id);
  assert.equal(after.host_command.state, 'unresolved'); assert.equal(after.host_command.drained, false);
  assert.deepEqual(after.accepted, job);
  const recovered = support.createSmokeHostRunner({ ...ports, getJournal: () => ({ snapshot: () => after }) });
  await assert.rejects(recovered('conflicting-command', []), /retirement unresolved/);
  await assert.rejects(runner('conflicting-command', []), /retirement unresolved/);
  assert.equal(calls, 1, 'neither in-memory nor reopened recovery can launch a conflicting operation');
  assert.doesNotMatch(JSON.stringify(after), /PRIVATE_ARGUMENT|PRIVATE_INPUT|PRIVATE_OUTPUT/);
});

test('F-HI2 an untyped host failure cannot clear the durable retirement hold', async t => {
  const dir = await fs.mkdtemp(join(tmpdir(), 'native-host-untyped-fixture-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true })); // synthetic port, no OS child
  const journal = support.createSmokeJournal(dir, { accepted: job });
  let calls = 0;
  const runner = support.createSmokeHostRunner({ getJournal: () => journal, commandEnv: {}, run: async () => {
    calls++; throw new Error('synthetic untyped failure');
  } });
  await assert.rejects(runner('synthetic-command', []), /synthetic untyped failure/);
  assert.equal(journal.snapshot().host_command.drained, false);
  assert.deepEqual(journal.snapshot().accepted, job);
  await assert.rejects(runner('conflicting-command', []), /retirement unresolved/);
  assert.equal(calls, 1);
});

const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function waitForFixture(predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  do { if (await predicate()) return true; await pause(20); } while (Date.now() < deadline);
  return false;
}
async function processIdentity(pid) {
  try {
    const raw = await fs.readFile(`/proc/${pid}/stat`, 'utf8'), fields = raw.slice(raw.lastIndexOf(')') + 2).split(' ');
    return { pid, birth: fields[19], state: fields[0] };
  } catch (error) { if (['ENOENT', 'ESRCH'].includes(error.code)) return null; throw error; }
}
async function retired(identity) {
  const current = await processIdentity(identity.pid);
  return !current || current.birth !== identity.birth || ['Z', 'X'].includes(current.state);
}

// Run the real public runner in a disposable worker. Its own stdio is ignored,
// so a broken runner cannot hold the test process's pipes open. Every fixture
// process self-exits; the guard signals only the directly spawned worker. Root
// removal depends on observed PID/birth retirement, never elapsed run age.
async function hostProcessFixture(t, { inherited = true, leaderStays = false, flood = false, detached = false } = {}) {
  const dir = await fs.mkdtemp(join(tmpdir(), 'native-host-child-fixture-'));
  const actors = join(dir, 'actors.json'), ready = join(dir, 'ready.json'), late = join(dir, 'late'), ended = join(dir, 'ended');
  const outcomePath = join(dir, 'outcome.json');
  await fs.writeFile(join(dir, 'authority'), 'fixture-only retained authority', { mode: 0o600 });
  const identify = `function identify(pid){const raw=fs.readFileSync('/proc/'+pid+'/stat','utf8'),f=raw.slice(raw.lastIndexOf(')')+2).split(' ');return {pid,birth:f[19]}}`;
  const descendant = `const fs=require('node:fs');${identify}
fs.writeFileSync(${JSON.stringify(ready)},JSON.stringify(identify(process.pid)));
process.stdout.on('error',()=>{});process.stderr.on('error',()=>{});
${flood ? `setTimeout(()=>{try{const chunk=Buffer.alloc(65536,'NATIVE_PRIVATE_OUTPUT');for(let n=0;n<192;n++)fs.writeSync(1,chunk)}catch{}},200);` : ''}
setTimeout(()=>fs.writeFileSync(${JSON.stringify(late)},'late writer'),3500);
setTimeout(()=>{fs.writeFileSync(${JSON.stringify(ended)},'finite writer ended');process.exit(0)},5000);`;
  const leader = `const fs=require('node:fs'),cp=require('node:child_process');${identify}
const child=cp.spawn(process.execPath,['-e',${JSON.stringify(descendant)}],{detached:${detached},stdio:${JSON.stringify(inherited ? ['ignore', 'inherit', 'inherit'] : 'ignore')}});child.unref();
fs.writeFileSync(${JSON.stringify(actors)},JSON.stringify([identify(process.pid),identify(child.pid)]));
${leaderStays ? '' : `const poll=setInterval(()=>{if(fs.existsSync(${JSON.stringify(ready)})){clearInterval(poll);process.exit(0)}},5);`}
setTimeout(()=>process.exit(0),5500)${leaderStays ? '' : '.unref()'};`;
  const worker = `import fs from 'node:fs';import {runHost} from ${JSON.stringify(new URL('./native-launcher-smoke-support.mjs', import.meta.url).href)};
const start=performance.now();let outcome;
try{const stdout=await runHost(process.execPath,['-e',${JSON.stringify(leader)}],{commandEnv:process.env,timeoutMs:1000});outcome={ok:true,bytes:Buffer.byteLength(stdout)}}
catch(e){outcome={ok:false,code:e.code,drained:e.drained,ambiguous:e.ambiguous,message:e.message}}
outcome.elapsedMs=performance.now()-start;fs.writeFileSync(${JSON.stringify(outcomePath)},JSON.stringify(outcome));process.exit(0);`;
  const child = spawnProcess(process.execPath, ['--input-type=module', '-e', worker], {
    stdio: 'ignore', env: { PATH: '/usr/bin:/bin', HOME: dir, TMPDIR: dir },
  });
  let spawnError, guarded = false, finish;
  const closed = new Promise(resolve => { finish = resolve; child.once('error', error => { spawnError = error; resolve(); }); child.once('close', resolve); });
  const workerIdentity = await processIdentity(child.pid);
  const guard = setTimeout(() => { guarded = true; child.kill('SIGKILL'); child.unref(); finish(); }, 3500);
  t.after(async () => {
    clearTimeout(guard);
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    const stopped = await waitForFixture(async () => {
      const identities = await fs.readFile(actors, 'utf8').then(JSON.parse, () => null);
      if (!workerIdentity || !identities || identities.length !== 2) return false;
      return (await Promise.all([workerIdentity, ...identities].map(retired))).every(Boolean);
    }, 7500);
    assert.ok(stopped, 'unproved fixture retirement; private root retained at ' + dir);
    await fs.rm(dir, { recursive: true, force: true });
  });
  await closed; clearTimeout(guard);
  if (spawnError) throw spawnError;
  const outcome = await fs.readFile(outcomePath, 'utf8').then(JSON.parse, () => null);
  const descendantIdentity = await fs.readFile(ready, 'utf8').then(JSON.parse, () => null);
  return { dir, outcome, guarded, descendantIdentity, late, ended };
}

for (const [name, options] of [
  ['exited leader with inherited pipes', { inherited: true }],
  ['timed-out leader with inherited pipes', { inherited: true, leaderStays: true }],
  ['exited leader with ignored-pipe late writer', { inherited: false }],
  ['exited leader with descendant output flood', { inherited: true, flood: true }],
  ['exited leader with separate-session late writer', { inherited: false, detached: true }],
]) {
  test('F-HI2 real runHost bounds ' + name, { skip: process.platform !== 'linux' }, async t => {
    const f = await hostProcessFixture(t, options);
    assert.ok(f.descendantIdentity, 'the actual finite descendant reached the intended boundary');
    assert.equal(f.guarded, false, 'runHost must settle before the independent 3500ms worker guard');
    assert.ok(f.outcome, 'runner produced an observable result');
    assert.equal(f.outcome.ok, false, 'a live descendant cannot be reported as successful retirement');
    assert.ok(f.outcome.elapsedMs < 2500, 'command plus pipe drain must be bounded: ' + f.outcome.elapsedMs + 'ms');
    assert.match(f.outcome.message, /child|command|timeout|drain|output/i);
    assert.doesNotMatch(f.outcome.message, /NATIVE_PRIVATE_OUTPUT/);
    if (options.flood) assert.equal(f.outcome.code, 'child_output_limit', '12 MiB of descendant output must hit the bounded output cap');
    if (f.outcome.drained === true) {
      assert.equal(await retired(f.descendantIdentity), true, 'a claimed drain proves the exact descendant stopped');
      assert.equal(await exists(f.late), false, 'a retired descendant cannot perform its scheduled late write');
    } else {
      assert.equal(f.outcome.drained, false, 'uncertain retirement must be explicit');
      assert.equal(f.outcome.ambiguous, true, 'unproved writers require a recovery hold');
      assert.equal(await fs.readFile(join(f.dir, 'authority'), 'utf8'), 'fixture-only retained authority');
    }
    if (options.detached) {
      assert.equal(f.outcome.drained, false, 'separate-session ownership is not permission to terminate it');
      assert.equal(await retired(f.descendantIdentity), false, 'unproved separate-session writer remains accounted for');
      assert.equal(await waitForFixture(() => exists(f.ended), 6500), true, 'finite writer ends before any root removal');
      assert.equal(await exists(f.late), true, 'the held writer really can write after bounded refusal');
    }
  });
}
