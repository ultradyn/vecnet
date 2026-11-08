import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, mkdir, readFile, writeFile, rm, stat, lstat, readlink, symlink } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import plugin from '../../src/backends/codex-app-server/index.ts';
import type { CodexConfig } from '../../src/backends/codex-app-server/index.ts';
import { CodexRpcError, openCodexRpc } from '../../src/backends/codex-app-server/rpc.ts';
import { openLedger } from '../../src/ledger.ts';
import { parseConfig } from '../../src/config.ts';
import { createOutbox } from '../../src/outbox.ts';
import type { Outbox } from '../../src/outbox.ts';
import { createJobReporter } from '../../src/jobs.ts';
import { startLaunch } from '../../src/launch.ts';
import type { LaunchDeps } from '../../src/launch.ts';
import { reattachAll, retryPending } from '../../src/reattach.ts';
import { sweep } from '../../src/sweep.ts';
import { createParentBridge } from '../../src/parent-bridge.ts';
import { routeApproval } from '../../src/policy.ts';
import type { Directory } from '../../src/directory.ts';
import type { LaunchPermission } from '../../src/backend.ts';
import type { SessionRef, SettleMode } from '../../src/backend.ts';
import type { SendRequest } from '../../../hub-client/src/wire.ts';
import { threadDelivery } from '../fixtures.ts';
import type { LaunchSpec, PluginContext, SessionHooks, SessionHandle, TurnCursor, TurnInput, PermissionMode, PermissionRequest, AgentQuestion } from '../../src/backend.ts';
import { fakeClock, memoryLog } from '../../../hub-client/testing/fixtures.ts';
import { startFakeNativeCodex } from '../fake-native-codex.ts';

const timeout = { timeout: 10000 };
const input = (ids = ['delivery-1', 'delivery-2']): TurnInput => ({ items: ids.map((deliveryId, i) => ({
  deliveryId, messageId: `message-${i}`, from: 'peer-requester', text: `Follow-up ${i}`, attachments: [],
})) });
const final = (text: string) => ({ type: 'agentMessage', phase: 'final_answer', text });

async function persistentScratchRoot(t: TestContext) {
  const root = await mkdtemp(join(fileURLToPath(new URL('../../', import.meta.url)), '.native-scratch-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

async function fixture(t: TestContext) {
  const dir = await mkdtemp('/tmp/vl-native-');
  let fake: Awaited<ReturnType<typeof startFakeNativeCodex>> | undefined;
  t.after(async () => { await fake?.close(); await rm(dir, { recursive: true, force: true }); });
  fake = await startFakeNativeCodex(dir);
  const daemon = fake, repo = join(dir, 'repo'), workspaceRoot = join(dir, 'worktrees');
  await mkdir(repo);
  const calls: string[][] = [];
  const exec: PluginContext['exec'] = async (argv, opts) => {
    assert.equal(argv[0], 'git', 'the backend must never launch a Codex process');
    calls.push([...argv]);
    return new Promise((resolve, reject) => {
      execFile(argv[0], argv.slice(1), { timeout: opts.timeoutMs, signal: opts.signal, maxBuffer: 1024 * 1024,
        env: { PATH: process.env.PATH, HOME: daemon.home, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
          GIT_AUTHOR_NAME: 'Native test', GIT_AUTHOR_EMAIL: 'native@example.invalid',
          GIT_COMMITTER_NAME: 'Native test', GIT_COMMITTER_EMAIL: 'native@example.invalid' },
      }, (error, stdout, stderr) => {
        if (error && typeof error.code !== 'number') { reject(error); return; }
        resolve({ code: error?.code as number ?? 0, stdout, stderr });
      });
    });
  };
  async function git(...args: string[]) {
    const result = await exec(['git', ...args], { timeoutMs: 5000 });
    assert.equal(result.code, 0, result.stderr);
    return result.stdout.trim();
  }
  await git('init', '-b', 'base', repo);
  await writeFile(join(repo, 'tracked.txt'), 'base contents\n');
  await git('-C', repo, 'add', 'tracked.txt');
  await git('-C', repo, 'commit', '-m', 'isolated baseline');
  const head = await git('-C', repo, 'rev-parse', 'HEAD');
  calls.length = 0;
  const store = new Map<string, string>(), clock = fakeClock(Date.parse('2026-11-08T00:00:00Z')), log = memoryLog();
  const permissions: PermissionRequest[] = [], questions: AgentQuestion[] = [];
  const hooks: SessionHooks = {
    onEvent() {},
    async onPermissionRequest(req) { permissions.push(req); return { optionId: 'accept', decidedBy: 'peer-requester' }; },
    async onQuestion(q) { questions.push(q); return { answers: { choice: 'Blue' }, answeredBy: 'peer-requester' }; },
  };
  const ctx: PluginContext = { host: 'native-test-host', log, exec, now: () => new Date(clock.now()),
    sleep: (ms, signal) => clock.sleep(ms, signal), redact: value => value,
    checkRoute: async () => { throw new Error('native backend must not inspect routes'); },
    readSecretFile: async () => { throw new Error('native backend must not read credentials'); },
    store: { get: key => store.get(key), set: (key, value) => { store.set(key, value); },
      delete: key => { store.delete(key); }, keys: (prefix = '') => [...store.keys()].filter(key => key.startsWith(prefix)) },
  };
  const config: CodexConfig = { codex_home: daemon.home, workspace_root: workspaceRoot, poll_ms: 100,
    presets: { inherited: {}, explicit: { model: 'explicit-model', effort: 'high' } } };
  const spec = (suffix = 'native', overrides: Partial<LaunchSpec> = {}): LaunchSpec => ({
    jobId: `job_${suffix}`, tag: `vl-job_${suffix}`, branch: `launch/job_${suffix}`,
    title: `vl-job_${suffix} regression task`, prompt: 'Implement the requested fix.\nKeep the regression covered.',
    project: { alias: 'isolated', repoRoot: repo, baseRef: 'base' }, preset: 'inherited', permissionMode: 'ask',
    attachments: [], env: {}, ...overrides,
  });
  const create = () => plugin.create(config, ctx);
  const methods = (method: string) => daemon.requests.filter(request => request.method === method);
  const owned = (tag = spec().tag) => JSON.parse(store.get('session:' + tag)!);
  async function restart(handle: SessionHandle, nextHooks = hooks) {
    daemon.disconnect();
    const backend = await create(), result = await backend.reattach(handle.ref, nextHooks);
    assert.notEqual(result.found, 'none');
    if (result.found === 'none') throw new Error('owned session missing');
    return { backend, handle: result.handle };
  }
  return { dir, daemon, repo, workspaceRoot, calls, git, head, store, clock, log, hooks, permissions, questions,
    ctx, config, spec, create, methods, owned, restart };
}

async function take(handle: SessionHandle, count: number, after?: TurnCursor) {
  const iterator = handle.turns(after)[Symbol.asyncIterator]();
  const rows: { cursor: TurnCursor; event: import('../../src/backend.ts').TurnEvent }[] = [];
  try {
    for (let i = 0; i < count; i++) {
      const row = await iterator.next();
      assert.equal(row.done, false); assert.ok(row.value); rows.push(row.value);
    }
  } finally { await iterator.return?.(); }
  return rows;
}

test('native launch uses a private socket, real isolated worktree, branch, title and initial prompt', timeout, async t => {
  const f = await fixture(t), spec = f.spec(), backend = await f.create(), handle = await backend.launch(spec, f.hooks);
  assert.equal((await lstat(f.daemon.socketPath)).isSymbolicLink(), true);
  assert.equal(await readlink(f.daemon.socketPath), f.daemon.target);
  assert.equal((await stat(join(f.dir, 'd'))).mode & 0o777, 0o700);
  assert.deepEqual(f.daemon.paths, ['/']);
  assert.deepEqual(f.daemon.requests.slice(0, 2).map(r => r.method), ['initialize', 'initialized']);
  assert.equal(f.methods('initialize')[0].params.capabilities.experimentalApi, true);
  const workspace = join(f.workspaceRoot, spec.jobId);
  assert.equal(handle.ref.extra?.workspace, workspace);
  assert.equal(handle.ref.extra?.codex_home, f.daemon.home);
  assert.equal(handle.ref.extra?.tag, spec.tag);
  assert.equal(await f.git('-C', workspace, 'branch', '--show-current'), spec.branch);
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
  assert.equal(await f.git('-C', workspace, 'rev-parse', 'HEAD'), f.head);
  await writeFile(join(workspace, 'tracked.txt'), 'isolated worktree edit\n');
  assert.equal(await readFile(join(f.repo, 'tracked.txt'), 'utf8'), 'base contents\n');
  assert.equal(f.methods('thread/start')[0].params.cwd, workspace);
  assert.deepEqual(f.methods('thread/start')[0].params.runtimeWorkspaceRoots, [workspace]);
  assert.equal(f.methods('thread/start')[0].params.ephemeral, false);
  assert.deepEqual(f.methods('thread/name/set')[0].params, { threadId: handle.ref.id, name: spec.title });
  assert.deepEqual(f.methods('turn/start')[0].params, { threadId: handle.ref.id,
    clientUserMessageId: spec.tag + '-initial', input: [{ type: 'text', text: spec.prompt }] });
  assert.equal(f.owned().initialSent, true);
  assert.equal(handle.state().kind, 'turn_running');
  assert.deepEqual(await backend.health(), { ok: true, version: '0.166.0' });
});

test('all spawn modes send explicit native policy while inherited model configuration is omitted', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  const configText = 'model = "home-configured-model"\nmodel_reasoning_effort = "medium"\n';
  await writeFile(join(f.daemon.home, 'config.toml'), configText);
  const cases: [PermissionMode, string, string, string][] = [
    ['read-only', 'read-only', 'never', 'user'], ['ask', 'workspace-write', 'on-request', 'user'],
    ['auto', 'workspace-write', 'on-request', 'auto_review'], ['full-access', 'danger-full-access', 'never', 'user'],
  ];
  for (const [mode, sandbox, approvalPolicy, approvalsReviewer] of cases) {
    await backend.launch(f.spec(mode.replaceAll('-', '_'), { permissionMode: mode }), f.hooks);
    const params = f.methods('thread/start').at(-1)!.params;
    assert.equal(params.sandbox, sandbox); assert.equal(params.approvalPolicy, approvalPolicy);
    assert.equal(params.approvalsReviewer, approvalsReviewer);
    for (const key of ['model', 'modelProvider', 'effort']) assert.equal(Object.hasOwn(params, key), false, key);
    assert.equal(Object.hasOwn(params.config ?? {}, 'model_reasoning_effort'), false);
  }
  assert.equal(await readFile(join(f.daemon.home, 'config.toml'), 'utf8'), configText);
  await backend.launch(f.spec('explicit', { preset: 'explicit' }), f.hooks);
  const explicit = f.methods('thread/start').at(-1)!.params;
  assert.equal(explicit.model, 'explicit-model'); assert.equal(explicit.config.model_reasoning_effort, 'high');
  assert.deepEqual((await backend.capabilities()).permissionModes, cases.map(row => row[0]));
});

test('project-root launches in the exact existing cwd and branch, and cleanup always retains that root', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), spec = f.spec('root', { workspaceMode: 'project-root' });
  await writeFile(join(f.repo, 'tracked.txt'), 'existing project edit\n');
  const handle = await backend.launch(spec, f.hooks), launchCalls = [...f.calls];
  assert.equal(handle.ref.extra?.workspace, f.repo);
  assert.equal(handle.ref.extra?.workspace_mode, 'project-root');
  assert.equal(handle.ref.extra?.branch, 'base');
  assert.equal(f.owned(spec.tag).branch, 'base');
  assert.equal(f.methods('thread/start')[0].params.cwd, f.repo);
  assert.deepEqual(f.methods('thread/start')[0].params.runtimeWorkspaceRoots, [f.repo]);
  assert.equal(launchCalls.some(argv => argv.some(arg => ['worktree', 'init', 'checkout', 'switch', 'commit'].includes(arg))), false);
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
  assert.equal(await f.git('-C', f.repo, 'rev-parse', 'HEAD'), f.head);
  assert.equal(await readFile(join(f.repo, 'tracked.txt'), 'utf8'), 'existing project edit\n');
  assert.ok(backend.cleanupWorkspace);
  assert.deepEqual(await backend.cleanupWorkspace(handle.ref), { removed: false, reason: 'project_root_retained' });
  const settled = await handle.settle('requested');
  assert.equal(settled.branch, 'base'); assert.equal(settled.workspaceKept, true);
  await f.git('-C', f.repo, 'restore', 'tracked.txt');
  assert.deepEqual(await backend.cleanupWorkspace(handle.ref), { removed: false, reason: 'project_root_retained' });
  assert.equal((await stat(join(f.repo, '.git'))).isDirectory(), true);
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
  assert.equal(await readFile(join(f.repo, 'tracked.txt'), 'utf8'), 'base contents\n');
  await assert.rejects(stat(f.workspaceRoot), { code: 'ENOENT' });
});

test('temp with temporary alias creates a fresh Git repo and empty initial commit on its own branch', timeout, async t => {
  const f = await fixture(t);
  f.config.workspace_root = await persistentScratchRoot(t);
  const backend = await f.create();
  const spec = f.spec('temporary', { workspaceMode: 'temp',
    project: { alias: 'temporary', repoRoot: f.repo, baseRef: 'unused-base-ref', scratch: true } });
  const handle = await backend.launch(spec, f.hooks), workspace = join(f.config.workspace_root, spec.jobId);
  assert.deepEqual((await backend.capabilities()).workspaceModes, ['worktree', 'project-root', 'temp']);
  assert.equal(handle.ref.extra?.workspace, workspace);
  assert.equal(handle.ref.extra?.workspace_mode, 'temp');
  assert.equal(handle.ref.extra?.branch, spec.branch);
  assert.equal(f.methods('thread/start')[0].params.cwd, workspace);
  assert.deepEqual(f.methods('thread/start')[0].params.runtimeWorkspaceRoots, [workspace]);
  assert.equal((await stat(join(workspace, '.git'))).isDirectory(), true);
  assert.equal(await f.git('-C', workspace, 'rev-parse', '--show-toplevel'), workspace);
  assert.equal(await f.git('-C', workspace, 'branch', '--show-current'), spec.branch);
  assert.equal(await f.git('-C', workspace, 'rev-list', '--count', 'HEAD'), '1');
  assert.equal(await f.git('-C', workspace, 'ls-tree', '-r', '--name-only', 'HEAD'), '');
  assert.equal(await f.git('-C', workspace, 'status', '--porcelain'), '');
  assert.notEqual(await f.git('-C', workspace, 'rev-parse', 'HEAD'), f.head);
  await assert.rejects(stat(join(workspace, 'tracked.txt')), { code: 'ENOENT' });
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
  assert.equal(await f.git('-C', f.repo, 'branch', '--list', spec.branch), '');
  assert.equal(await readFile(join(f.repo, 'tracked.txt'), 'utf8'), 'base contents\n');
  const resumed = await f.restart(handle);
  assert.equal(resumed.handle.ref.extra?.workspace_mode, 'temp');
  await resumed.handle.settle('requested');
  assert.ok(resumed.backend.cleanupWorkspace);
  assert.deepEqual(await resumed.backend.cleanupWorkspace(resumed.handle.ref), { removed: true });
  await assert.rejects(stat(workspace), { code: 'ENOENT' });
  assert.equal(await f.git('-C', f.repo, 'rev-parse', 'HEAD'), f.head);
});

test('invalid workspace modes and scratch-metadata combinations reject before Git or workspace mutation', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  const choices: [LaunchSpec['workspaceMode'], boolean][] = [
    ['invalid' as LaunchSpec['workspaceMode'], false], ['temp', false],
    ['project-root', true], ['worktree', true], [undefined, true],
  ];
  for (const [index, [workspaceMode, scratch]] of choices.entries()) {
    await assert.rejects(backend.launch(f.spec(`invalid_workspace_${index}`, { workspaceMode,
      project: { alias: 'arbitrary-name', repoRoot: f.repo, baseRef: 'base', scratch } }), f.hooks));
  }
  assert.deepEqual(f.calls, []);
  assert.equal(f.store.size, 0); assert.equal(f.methods('thread/start').length, 0);
  await assert.rejects(stat(f.workspaceRoot), { code: 'ENOENT' });
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
  assert.equal(await f.git('-C', f.repo, 'rev-parse', 'HEAD'), f.head);
  assert.equal(await readFile(join(f.repo, 'tracked.txt'), 'utf8'), 'base contents\n');
});

test('an ordinary project named temporary retains the legacy default worktree behavior', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  const spec = f.spec('legacy_temporary', { project: { alias: 'temporary', repoRoot: f.repo, baseRef: 'base' } });
  const handle = await backend.launch(spec, f.hooks);
  assert.equal(handle.ref.extra?.workspace_mode, 'worktree');
  assert.equal(handle.ref.extra?.workspace, join(f.workspaceRoot, spec.jobId));
  assert.equal(await f.git('-C', String(handle.ref.extra!.workspace), 'branch', '--show-current'), spec.branch);
});

for (const variant of ['direct-tmp', 'symlink-to-tmp', 'ancestor-symlink-to-tmp'] as const) {
  test(`scratch rejects a canonical /tmp allocation before native dispatch: ${variant}`, timeout, async t => {
    const f = await fixture(t), persistent = await persistentScratchRoot(t);
    if (variant === 'direct-tmp') f.config.workspace_root = f.workspaceRoot;
    else {
      await mkdir(f.workspaceRoot);
      const link = join(persistent, 'into-tmp'); await symlink(f.workspaceRoot, link);
      f.config.workspace_root = variant === 'symlink-to-tmp' ? link : join(link, 'not-created', 'child');
    }
    const backend = await f.create(), spec = f.spec('forbidden_scratch', { workspaceMode: 'temp',
      project: { alias: 'scratch-entry', scratch: true, repoRoot: f.repo, baseRef: 'base' } });
    await assert.rejects(backend.launch(spec, f.hooks));
    assert.equal(f.methods('thread/start').length, 0); assert.deepEqual(f.calls, []); assert.equal(f.store.size, 0);
    await assert.rejects(stat(join(f.config.workspace_root, spec.jobId)), { code: 'ENOENT' });
  });
}

test('unknown project-root launch never adopts a foreign thread with the same cwd or starts a duplicate', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  const foreign = { id: 'foreign-project-root', cwd: f.repo, status: { type: 'idle' }, archived: false,
    name: 'foreign owner title', turns: [] };
  f.daemon.threads.set(foreign.id, foreign);
  const spec = f.spec('root_lost', { workspaceMode: 'project-root' });
  f.daemon.failNext('thread/start', 'drop');
  await assert.rejects(backend.launch(spec, f.hooks), error => error instanceof CodexRpcError && error.ambiguous);
  assert.equal(f.owned(spec.tag).id, undefined);
  assert.equal(f.owned(spec.tag).workspaceMode, 'project-root');
  for (let i = 0; i < 2; i++) {
    const restarted = await f.create();
    await assert.rejects(restarted.findByTag(spec.tag), /unknown|unresolved|ambiguous/);
    await assert.rejects(restarted.launch(spec, f.hooks), /unknown|unresolved|ambiguous/);
    await assert.rejects(restarted.listOwned());
  }
  assert.equal(f.methods('thread/start').length, 1);
  for (const method of ['thread/resume', 'thread/name/set', 'turn/start', 'thread/archive']) {
    assert.equal(f.methods(method).length, 0, method);
  }
  assert.equal(f.daemon.threads.size, 1); assert.equal(foreign.name, 'foreign owner title');
  assert.equal(f.owned(spec.tag).id, undefined);
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
  assert.equal(await f.git('-C', f.repo, 'rev-parse', 'HEAD'), f.head);
});

test('history hydrates final answers and consumed deliveries in chronological order across pages and restart cursors', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), handle = await backend.launch(f.spec(), f.hooks);
  const first = f.daemon.threads.get(handle.ref.id)!.turns[0];
  f.daemon.complete(first.id, [final('initial answer')]);
  const sent = await handle.send(input(), 'start');
  assert.ok(sent.turnId); assert.ok(sent.backendInputId);
  const params = f.methods('turn/start').at(-1)!.params;
  assert.equal(params.threadId, handle.ref.id);
  assert.deepEqual(params.additionalContext[sent.backendInputId], { kind: 'untrusted', name: 'vecnet_inbox',
    value: JSON.stringify(input().items.map(item => ({ delivery: item.deliveryId, message: item.messageId, from: item.from, text: item.text }))) });
  assert.ok(!JSON.stringify(params.input).includes('Follow-up 0'), 'requester content is separate from harness text');
  f.daemon.complete(sent.turnId, [
    { type: 'agentMessage', phase: 'commentary', text: 'draft' }, final('final one'),
    { type: 'commandExecution', text: 'tool output' }, final('final two'),
    { type: 'agentMessage', phase: 'commentary', text: 'later commentary' },
  ]);
  f.daemon.setPageSize(1);
  const rows = await take(handle, 2);
  assert.deepEqual(rows.map(row => row.event), [
    { type: 'turn_finished', turnId: first.id, at: new Date(1794096000000).toISOString(), outcome: 'completed', finalText: 'initial answer', consumed: [] },
    { type: 'turn_finished', turnId: sent.turnId, at: new Date(1794096000000).toISOString(), outcome: 'completed', finalText: 'final one\nfinal two', consumed: ['delivery-1', 'delivery-2'] },
  ]);
  assert.ok(f.methods('thread/turns/list').some(r => r.params.cursor === 'native-page:1'));
  assert.ok(f.methods('thread/turns/list').every(r => r.params.itemsView === 'full'));
  const resumed = await f.restart(handle), before = f.methods('turn/start').length;
  assert.deepEqual(await resumed.handle.send(input(), 'start'), sent);
  assert.equal(f.methods('turn/start').length, before);
  const abort = new AbortController();
  f.ctx.sleep = async () => { abort.abort(); };
  const replay = [];
  for await (const row of resumed.handle.turns(rows[1].cursor, abort.signal)) replay.push(row);
  assert.deepEqual(replay, []);
  await assert.rejects(resumed.handle.turns({ value: JSON.stringify({ last: 'missing-turn' }) })[Symbol.asyncIterator]().next(), /history gap/);
});

test('restart and reconnect resume owned threads without overriding model or permissions', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), handle = await backend.launch(f.spec(), f.hooks);
  f.config.presets.inherited = { model: 'changed-after-spawn', effort: 'low' };
  const resumed = await f.restart(handle);
  await resumed.handle.activity();
  await resumed.handle.park!(); assert.equal(resumed.handle.state().kind, 'parked');
  const beforeActivity = f.methods('thread/resume').length;
  await resumed.handle.activity();
  assert.equal(f.methods('thread/resume').length, beforeActivity);
  await resumed.handle.send(input(['unpark-reconnect']), 'start');
  assert.ok(f.methods('thread/resume').length >= 2);
  for (const r of f.methods('thread/resume')) assert.deepEqual(r.params, { threadId: handle.ref.id, excludeTurns: true });
  assert.ok(f.methods('thread/read').every(r => r.params.includeTurns === false));
  assert.deepEqual(await resumed.backend.reattach({ ...handle.ref, host: 'foreign-host' }, f.hooks), { found: 'none' });
  assert.deepEqual(await resumed.backend.reattach({ ...handle.ref, extra: { ...handle.ref.extra, tag: 'vl-job_foreign' } }, f.hooks), { found: 'none' });
  assert.deepEqual(await resumed.backend.listOwned(), [handle.ref]);
});

test('effective model, provider and effort from native spawn survive restart and stale ref metadata', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), spec = f.spec(), handle = await backend.launch(spec, f.hooks);
  const expected = { ...handle.ref.extra, model: 'native-default-model', model_provider: 'native-default-provider',
    effort: 'medium', permission_mode: 'ask' };
  assert.deepEqual(handle.ref.extra, expected);
  assert.equal(Object.hasOwn(f.methods('thread/start')[0].params, 'model'), false);
  assert.equal(Object.hasOwn(f.methods('thread/start')[0].params.config ?? {}, 'model_reasoning_effort'), false);
  assert.equal(f.owned().model, 'native-default-model');
  assert.equal(f.owned().provider, 'native-default-provider');
  assert.equal(f.owned().effort, 'medium');
  f.config.presets.inherited = { model: 'changed-preset', effort: 'low' };
  f.daemon.disconnect();
  const restarted = await f.create(), attached = await restarted.reattach({ ...handle.ref,
    extra: { ...handle.ref.extra, model: 'stale-ref-model', model_provider: 'stale-ref-provider', effort: 'low' } }, f.hooks);
  assert.notEqual(attached.found, 'none');
  if (attached.found === 'none') throw new Error('missing owned thread');
  assert.deepEqual(attached.handle.ref.extra, expected);
  assert.deepEqual((await restarted.findByTag(spec.tag))?.extra, expected);
  assert.deepEqual((await restarted.listOwned())[0].extra, expected);
  assert.equal(f.methods('thread/start').length, 1);
  for (const request of f.methods('thread/resume')) assert.deepEqual(request.params, { threadId: handle.ref.id, excludeTurns: true });
});

test('duplicate job launch after restart never creates another thread, worktree or initial input', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), spec = f.spec(), handle = await backend.launch(spec, f.hooks);
  assert.equal((await backend.launch(spec, f.hooks)).ref.id, handle.ref.id);
  const resumed = await f.restart(handle);
  assert.equal((await resumed.backend.launch(spec, f.hooks)).ref.id, handle.ref.id);
  assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('turn/start').length, 1);
  assert.equal(f.calls.filter(argv => argv.includes('worktree') && argv.includes('add')).length, 1);
  assert.deepEqual(await resumed.backend.findByTag(spec.tag), handle.ref);
});

test('accepted thread/start with a lost response recovers by workspace and starts the saved prompt once', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'accept-drop');
  await assert.rejects(backend.launch(f.spec(), f.hooks), error => error instanceof CodexRpcError && error.ambiguous);
  assert.equal(f.owned().id, undefined); assert.equal(f.daemon.threads.size, 1);
  const restarted = await f.create(), handle = await restarted.launch(f.spec(), f.hooks);
  assert.deepEqual(f.methods('thread/name/set').map(request => request.params), [{ threadId: handle.ref.id, name: f.spec().title }]);
  assert.equal(f.daemon.threads.get(handle.ref.id)!.name, f.spec().title);
  const abort = new AbortController();
  f.ctx.sleep = async () => { abort.abort(); };
  for await (const _ of handle.turns(undefined, abort.signal)) assert.fail('initial turn is still running');
  assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('turn/start').length, 1);
  assert.equal(f.methods('turn/start')[0].params.input[0].text, f.spec().prompt);
  assert.equal(f.methods('thread/list')[0].params.cwd, join(f.workspaceRoot, f.spec().jobId));
  assert.equal(f.owned().initialSent, true);
});

test('unresolved ambiguous thread launch refuses retries without duplicating a thread', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'drop');
  await assert.rejects(backend.launch(f.spec(), f.hooks), error => error instanceof CodexRpcError && error.ambiguous);
  for (let i = 0; i < 2; i++) await assert.rejects((await f.create()).launch(f.spec(), f.hooks), /unknown|unresolved|ambiguous/);
  assert.equal(f.methods('thread/start').length, 1); assert.equal(f.daemon.threads.size, 0);
});

test('accepted initial turn with a lost response is reconciled from its receipt after restart', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('turn/start', 'accept-drop');
  await assert.rejects(backend.launch(f.spec(), f.hooks), error => error instanceof CodexRpcError && error.ambiguous);
  assert.equal(f.owned().initialSending, true); assert.notEqual(f.owned().initialSent, true);
  const thread = [...f.daemon.threads.values()][0];
  f.daemon.complete(thread.turns[0].id, [final('recovered initial result')]);
  const restarted = await f.create(), ref = await restarted.findByTag(f.spec().tag);
  assert.ok(ref);
  const attached = await restarted.reattach(ref, f.hooks); assert.notEqual(attached.found, 'none');
  if (attached.found === 'none') throw new Error('missing owned thread');
  const [row] = await take(attached.handle, 1);
  assert.equal(row.event.type, 'turn_finished');
  assert.equal('finalText' in row.event && row.event.finalText, 'recovered initial result');
  assert.equal(f.methods('turn/start').length, 1); assert.equal(f.owned().initialSent, true);
  assert.equal(f.owned().initialSending, false);
});

test('missing initial receipt refuses another send even across repeated restarts', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('turn/start', 'drop');
  await assert.rejects(backend.launch(f.spec(), f.hooks), error => error instanceof CodexRpcError && error.ambiguous);
  for (let i = 0; i < 2; i++) {
    const restarted = await f.create(), ref = await restarted.findByTag(f.spec().tag); assert.ok(ref);
    const attached = await restarted.reattach(ref, f.hooks); assert.notEqual(attached.found, 'none');
    if (attached.found === 'none') throw new Error('missing thread');
    await assert.rejects(attached.handle.turns()[Symbol.asyncIterator]().next(), /initial input receipt unresolved/);
    f.daemon.disconnect();
  }
  assert.equal(f.methods('turn/start').length, 1); assert.equal(f.owned().initialSending, true);
});

test('restart before initial dispatch resumes saved instructions when turn polling begins', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/name/set', { code: -32000, message: 'temporary name failure' });
  await assert.rejects(backend.launch(f.spec(), f.hooks), /temporary name failure/);
  assert.equal(f.methods('turn/start').length, 0); assert.equal(f.owned().prompt, f.spec().prompt);
  f.daemon.disconnect();
  const restarted = await f.create(), ref = await restarted.findByTag(f.spec().tag); assert.ok(ref);
  const attached = await restarted.reattach(ref, f.hooks); assert.notEqual(attached.found, 'none');
  if (attached.found === 'none') throw new Error('missing thread');
  assert.equal(f.methods('turn/start').length, 0);
  f.ctx.sleep = async () => {
    const turn = f.daemon.threads.get(ref.id)!.turns[0];
    f.daemon.complete(turn.id, [final('resumed instructions')]);
  };
  const [row] = await take(attached.handle, 1);
  assert.equal('finalText' in row.event && row.event.finalText, 'resumed instructions');
  assert.equal(f.methods('turn/start').length, 1);
  assert.equal(f.methods('turn/start')[0].params.input[0].text, f.spec().prompt);
});

test('ambiguous delivery acceptance is reconciled without another turn/start or losing consumed IDs', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), handle = await backend.launch(f.spec(), f.hooks);
  const thread = f.daemon.threads.get(handle.ref.id)!;
  f.daemon.complete(thread.turns[0].id, [final('initial')]);
  f.daemon.failNext('turn/start', 'accept-drop');
  await assert.rejects(handle.send(input(), 'start'), error => error instanceof CodexRpcError && error.ambiguous);
  const resumed = await f.restart(handle), sent = await resumed.handle.send(input(), 'start');
  assert.equal(sent.turnId, thread.turns[1].id); assert.equal(f.methods('turn/start').length, 2);
  f.daemon.complete(thread.turns[1].id, [final('accepted follow-up')]);
  const rows = await take(resumed.handle, 2);
  assert.deepEqual('consumed' in rows[1].event && rows[1].event.consumed, ['delivery-1', 'delivery-2']);
});

test('steer falls back to start across an inactive turn boundary and queue stays with launcher', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), handle = await backend.launch(f.spec(), f.hooks);
  const thread = f.daemon.threads.get(handle.ref.id)!;
  const steered = await handle.send(input(['steered']), 'steer');
  assert.equal(steered.turnId, thread.turns[0].id);
  assert.equal(f.methods('turn/steer')[0].params.expectedTurnId, thread.turns[0].id);
  f.daemon.complete(thread.turns[0].id, [final('steered final')]);
  const sent = await handle.send(input(['retry-as-start']), 'steer'); assert.ok(sent.turnId);
  assert.equal(sent.mode, 'start'); assert.notEqual(sent.turnId, thread.turns[0].id);
  assert.equal((await handle.send(input(['retry-as-start']), 'steer')).turnId, sent.turnId);
  await assert.rejects(handle.send(input(['queued']), 'queue'), /queue belongs to launcher/);
  assert.equal(f.methods('turn/steer').length, 1); assert.equal(f.methods('turn/start').length, 2);
  await assert.rejects(handle.send({ items: [{ ...input().items[0], deliveryId: 'oversized', text: '界'.repeat(40000) }] }, 'start'), /input_too_large/);
  assert.equal(f.methods('turn/start').length, 2);
});

test('definite steer rejection clears reservations and the same delivery retries once after restart and turn completion', timeout, async t => {
  const f = await fixture(t), handle = await (await f.create()).launch(f.spec(), f.hooks);
  const thread = f.daemon.threads.get(handle.ref.id)!;
  f.daemon.failNext('turn/steer', { code: -32602, message: 'turn completed before steer acceptance' });
  await assert.rejects(handle.send(input(['boundary-retry']), 'steer'), error => error instanceof CodexRpcError && !error.ambiguous);
  const clientId = f.methods('turn/steer')[0].params.clientUserMessageId;
  for (const prefix of ['sending:', 'consumed:', 'sent:']) assert.equal(f.store.has(prefix + clientId), false);
  f.daemon.complete(thread.turns[0].id, [final('old turn finished')]);
  const resumed = await f.restart(handle), sent = await resumed.handle.send(input(['boundary-retry']), 'steer');
  assert.equal(sent.mode, 'start'); assert.equal(sent.backendInputId, clientId);
  assert.notEqual(sent.turnId, thread.turns[0].id);
  assert.equal((await resumed.handle.send(input(['boundary-retry']), 'steer')).turnId, sent.turnId);
  assert.equal(f.methods('turn/steer').length, 1); assert.equal(f.methods('turn/start').length, 2);
  assert.equal(thread.turns.flatMap(turn => turn.items).filter(item => item.clientId === clientId).length, 1);
});

test('definite start rejection clears reservations while ambiguous rejection retains them', timeout, async t => {
  const f = await fixture(t), handle = await (await f.create()).launch(f.spec(), f.hooks);
  f.daemon.complete(f.daemon.threads.get(handle.ref.id)!.turns[0].id, [final('initial')]);
  f.daemon.failNext('turn/start', { code: -32602, message: 'definite rejection' });
  await assert.rejects(handle.send(input(['start-retry']), 'start'), error => error instanceof CodexRpcError && !error.ambiguous);
  const rejected = f.methods('turn/start').at(-1)!.params.clientUserMessageId;
  for (const prefix of ['sending:', 'consumed:', 'sent:']) assert.equal(f.store.has(prefix + rejected), false);
  const sent = await handle.send(input(['start-retry']), 'start'); assert.ok(sent.turnId);
  assert.equal(sent.backendInputId, rejected);
  f.daemon.complete(sent.turnId, [final('retry accepted')]);
  f.daemon.failNext('turn/start', 'drop');
  await assert.rejects(handle.send(input(['ambiguous-no-receipt']), 'start'), error => error instanceof CodexRpcError && error.ambiguous);
  const ambiguous = f.methods('turn/start').at(-1)!.params.clientUserMessageId;
  assert.equal(f.store.get('sending:' + ambiguous), handle.ref.id);
  assert.deepEqual(JSON.parse(f.store.get('consumed:' + ambiguous)!), ['ambiguous-no-receipt']);
  const resumed = await f.restart(handle), count = f.methods('turn/start').length;
  await assert.rejects(resumed.handle.send(input(['ambiguous-no-receipt']), 'start'), /receipt unresolved/);
  assert.equal(f.methods('turn/start').length, count);
});

test('definite initial rejection permits exactly one accepted retry after restart', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('turn/start', { code: -32602, message: 'initial rejected' });
  await assert.rejects(backend.launch(f.spec(), f.hooks), error => error instanceof CodexRpcError && !error.ambiguous);
  assert.equal(f.owned().initialSending, false); assert.notEqual(f.owned().initialSent, true);
  f.daemon.disconnect();
  const restarted = await f.create(), handle = await restarted.launch(f.spec(), f.hooks);
  f.ctx.sleep = async () => {
    const turn = f.daemon.threads.get(handle.ref.id)!.turns[0];
    f.daemon.complete(turn.id, [final('initial retry accepted')]);
  };
  const [row] = await take(handle, 1);
  assert.equal('finalText' in row.event && row.event.finalText, 'initial retry accepted');
  assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('turn/start').length, 2);
  assert.equal(f.daemon.threads.get(handle.ref.id)!.turns.length, 1);
  assert.equal(f.owned().initialSent, true);
});

test('park survives activity and polling across restart until input explicitly resumes the thread', timeout, async t => {
  const f = await fixture(t), handle = await (await f.create()).launch(f.spec(), f.hooks);
  f.daemon.complete(f.daemon.threads.get(handle.ref.id)!.turns[0].id, [final('initial')]);
  const [row] = await take(handle, 1);
  await handle.park!(); assert.equal(f.owned().parked, true);
  const resumeCount = f.methods('thread/resume').length;
  assert.equal((await handle.activity()).turnRunning, false);
  const resumed = await f.restart(handle);
  assert.equal(resumed.handle.state().kind, 'parked');
  assert.equal((await resumed.handle.activity()).turnRunning, false);
  const abort = new AbortController();
  let announceSleep!: () => void;
  let sleeping = new Promise<void>(resolve => { announceSleep = resolve; });
  f.ctx.sleep = (ms, signal) => { const pending = f.clock.sleep(ms, signal); announceSleep(); return pending; };
  const iterator = resumed.handle.turns(row.cursor, abort.signal)[Symbol.asyncIterator](), pending = iterator.next();
  try {
    await sleeping;
    for (let i = 0; i < 2; i++) {
      sleeping = new Promise<void>(resolve => { announceSleep = resolve; });
      f.clock.advance(100);
      await Promise.race([sleeping, pending.then(() => assert.fail('parked poll unexpectedly emitted an event'))]);
    }
    assert.equal(f.methods('thread/resume').length, resumeCount);
    assert.equal(resumed.handle.state().kind, 'parked'); assert.equal(f.owned().parked, true);
  } finally { abort.abort(); await pending; await iterator.return?.(); }
  const sent = await resumed.handle.send(input(['wake-parked']), 'steer');
  assert.equal(sent.mode, 'start'); assert.ok(sent.turnId);
  assert.equal(f.methods('thread/resume').length, resumeCount + 1);
  assert.equal(f.owned().parked, false);
  assert.equal((await resumed.handle.activity()).turnRunning, true);
});

test('only owned thread requests reach typed hooks or receive protocol replies', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), handle = await backend.launch(f.spec(), f.hooks);
  const command = { threadId: handle.ref.id, command: 'git status', cwd: handle.ref.extra!.workspace, reason: 'inspect changes' };
  const own = f.daemon.serverRequest('item/commandExecution/requestApproval', command);
  const edit = f.daemon.serverRequest('item/fileChange/requestApproval', { threadId: handle.ref.id, grantRoot: handle.ref.extra!.workspace });
  const question = f.daemon.serverRequest('item/tool/requestUserInput', { threadId: handle.ref.id,
    questions: [{ id: 'choice', header: 'Color', question: 'Which color?', isOther: true, isSecret: false,
      options: [{ label: 'Blue', description: 'Use blue' }] }] });
  const unknown = f.daemon.serverRequest('item/unknown/requestApproval', { threadId: handle.ref.id });
  const foreign = ['item/commandExecution/requestApproval', 'item/tool/requestUserInput', 'item/unknown/requestApproval']
    .map(method => f.daemon.serverRequest(method, { ...command, threadId: 'foreign-thread' }));
  f.daemon.serverRequest('item/commandExecution/requestApproval', {});
  await handle.activity(); await handle.activity();
  assert.equal(f.permissions.length, 2); assert.equal(f.questions.length, 1);
  assert.deepEqual(f.permissions[0].permission.command, { line: 'git status', cwd: handle.ref.extra!.workspace });
  assert.equal(f.permissions[0].permission.kind, 'exec'); assert.equal(f.permissions[1].permission.kind, 'edit');
  assert.deepEqual(f.questions[0].questions[0], { id: 'choice', header: 'Color', text: 'Which color?', allowOther: true,
    secret: false, options: [{ label: 'Blue', description: 'Use blue' }] });
  for (const id of [own, edit]) assert.deepEqual(f.daemon.replies.find(r => r.id === id)?.result, { decision: 'accept' });
  assert.deepEqual(f.daemon.replies.find(r => r.id === question)?.result, { answers: { choice: { answers: ['Blue'] } } });
  assert.deepEqual(f.daemon.replies.find(r => r.id === unknown)?.result, { decision: 'decline' });
  assert.ok(f.daemon.replies.every(r => !foreign.includes(Number(r.id))));
  assert.equal(f.daemon.replies.length, 4);
});

test('disconnect aborts owned permission and question hooks and clears pending activity', timeout, async t => {
  const f = await fixture(t);
  let startedCount = 0, abortedCount = 0, started!: () => void, aborted!: () => void;
  const start = new Promise<void>(resolve => { started = resolve; });
  const abort = new Promise<void>(resolve => { aborted = resolve; });
  function pending(signal: AbortSignal): Promise<never> {
    if (++startedCount === 2) started();
    return new Promise((_, reject) => signal.addEventListener('abort', () => {
      if (++abortedCount === 2) aborted(); reject(new Error('connection closed'));
    }, { once: true }));
  }
  const hooks: SessionHooks = { onEvent() {}, onPermissionRequest: (_, signal) => pending(signal), onQuestion: (_, signal) => pending(signal) };
  const handle = await (await f.create()).launch(f.spec(), hooks);
  f.daemon.serverRequest('item/commandExecution/requestApproval', { threadId: handle.ref.id, command: 'git status' });
  f.daemon.serverRequest('item/tool/requestUserInput', { threadId: handle.ref.id, questions: [] });
  await start; assert.equal((await handle.activity()).openApprovals, 2);
  f.daemon.disconnect(); await abort;
  assert.equal((await handle.activity()).openApprovals, 0); assert.equal(f.daemon.replies.length, 0);
});

test('settle interrupts and archives once, removes ownership, keeps dirty workspace then cleans it safely', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), spec = f.spec(), handle = await backend.launch(spec, f.hooks);
  const workspace = String(handle.ref.extra!.workspace);
  await writeFile(join(workspace, 'tracked.txt'), 'keep dirty change\n');
  const result = await handle.settle('requested');
  assert.deepEqual(result, { branch: spec.branch, headSha: f.head, workspaceKept: true, transcriptRef: `codex:${handle.ref.id}` });
  assert.deepEqual(handle.state(), { kind: 'ended', reason: 'settled', resumable: false });
  assert.equal(f.methods('turn/interrupt').length, 1); assert.equal(f.methods('thread/archive').length, 1);
  assert.ok(f.daemon.requests.findIndex(r => r.method === 'turn/interrupt') < f.daemon.requests.findIndex(r => r.method === 'thread/archive'));
  assert.equal(f.daemon.threads.get(handle.ref.id)!.archived, true);
  await handle.settle('requested'); assert.equal(f.methods('thread/archive').length, 1);
  assert.deepEqual(await backend.listOwned(), []); assert.equal(await backend.findByTag(spec.tag), null);
  assert.deepEqual(await backend.reattach(handle.ref, f.hooks), { found: 'none' });
  await assert.rejects(handle.send(input(), 'start'), /settled/);
  assert.ok(backend.cleanupWorkspace);
  assert.equal((await backend.cleanupWorkspace(handle.ref)).removed, false);
  assert.equal(await readFile(join(workspace, 'tracked.txt'), 'utf8'), 'keep dirty change\n');
  await f.git('-C', workspace, 'restore', 'tracked.txt');
  assert.equal((await backend.cleanupWorkspace({ ...handle.ref, host: 'foreign-host' })).removed, false);
  assert.equal((await backend.cleanupWorkspace({ ...handle.ref, id: 'foreign-thread' })).removed, false);
  assert.equal((await backend.cleanupWorkspace({ ...handle.ref, extra: { ...handle.ref.extra, tag: 'vl-job_foreign' } })).removed, false);
  assert.deepEqual(await backend.cleanupWorkspace(handle.ref), { removed: true });
  await assert.rejects(stat(workspace), { code: 'ENOENT' });
  assert.equal(await f.git('-C', f.repo, 'rev-parse', spec.branch), f.head, 'cleanup preserves the result branch');
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
});

test('missing daemon and invalid job identities never create a workspace or launch a subprocess', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  await assert.rejects(backend.launch(f.spec('invalid', { branch: '../escape' }), f.hooks), /identity/);
  await assert.rejects(backend.launch(f.spec('preset', { preset: 'missing' }), f.hooks), /preset/);
  await rm(f.daemon.socketPath);
  assert.equal((await backend.health()).ok, false);
  await assert.rejects(backend.launch(f.spec(), f.hooks), /unreachable/);
  assert.deepEqual(f.calls, []); assert.deepEqual(f.daemon.requests, []);
  await assert.rejects(stat(f.workspaceRoot), { code: 'ENOENT' });
});

// This harness deliberately uses the native backend behind the real launcher
// failure fence, persisted SQLite ledger, restart reconciliation and sweeper.
async function launchHarness(t: TestContext, f: Awaited<ReturnType<typeof fixture>>) {
  const path = join(f.dir, 'launch-ledger.sqlite');
  let ledger = openLedger(path, f.clock);
  t.after(() => ledger.close());
  f.ctx.store = ledger.kv('native-backend');
  const cfg = parseConfig({ host: f.ctx.host, hub: { url: 'http://localhost', credential_file: '/unused' },
    state_dir: f.dir, backends: [], projects: { isolated: { repo_root: f.repo, base_ref: 'base' } } });
  const reporter = () => createJobReporter({ ledger, outbox: createOutbox({ ledger, clock: f.clock, log: f.log,
    hub: { async send() { throw new Error('synthetic tests never contact a hub'); }, async receipt() {} } }) });
  const deps: LaunchDeps = { ledger, cfg, jobs: reporter(), host: f.ctx.host, log: f.log,
    clock: { ...f.clock, async sleep(ms) { f.clock.advance(ms); } },
    backends: new Map([['codex-app-server', await f.create()]]), hooks: () => f.hooks,
    projects: () => f.spec().project };
  ledger.createJob({ jobId: 'job_native', threadId: 'request-thread', launchMessageId: 'launch-message',
    requester: 'peer-requester', policyKey: 'requester-principal', backend: 'codex-app-server', preset: 'inherited',
    permissionMode: 'ask', project: 'isolated', title: 'Native failure recovery', instructions: 'Fix the requested issue.',
    input: { backend: 'codex-app-server', project: 'isolated', preset: 'inherited', permission_mode: 'ask' },
    tag: 'vl-job_native', state: 'accepted', sessionRef: null, sessionKind: null, turnCursor: null,
    branch: null, workspace: null, lastActivityAt: f.clock.now(), parkedAt: null,
    deadlineAt: f.clock.now() + 86400000, createdAt: f.clock.now() });
  deps.jobs.report('job_native', 'accepted');
  return {
    deps, job: () => deps.ledger.job('job_native')!,
    backend: () => deps.backends.get('codex-app-server')!,
    owned: () => JSON.parse(deps.ledger.kv('native-backend').get('session:vl-job_native')!),
    reports: () => deps.ledger.pendingOutbox(100, f.clock.now()).map(row => row.payload as SendRequest)
      .filter(row => row.kind === 'job_status').map(row => row.job_status!.state),
    async restart() {
      f.daemon.disconnect(); ledger.close(); ledger = openLedger(path, f.clock);
      deps.ledger = ledger; f.ctx.store = ledger.kv('native-backend'); deps.jobs = reporter();
      deps.backends = new Map([['codex-app-server', await f.create()]]);
      return deps;
    },
    sweep: () => sweep({ ...deps, graceMs: 0, retentionMs: 1, firstSeen: deps.ledger.kv('native-sweep') }),
  };
}

test('an accepted but invisible native start stays unresolved through launch, restart, retries and retention sweep', timeout, async t => {
  const f = await fixture(t), h = await launchHarness(t, f);
  f.daemon.failNext('thread/start', 'accept-drop'); f.daemon.setListVisible(false);
  assert.equal((await startLaunch(h.job(), h.deps)).ok, false);
  assert.equal(h.job().state, 'launching'); assert.equal(h.job().terminal, null);
  assert.equal(h.job().reattachPending, true); assert.deepEqual(h.reports(), ['accepted']);
  const workspace = join(f.workspaceRoot, h.job().jobId);
  assert.equal(h.owned().phase, 'start-dispatched'); assert.equal(h.owned().id, undefined);
  await h.restart();
  assert.equal((await reattachAll(h.deps))[0].outcome, 'unreachable');
  f.clock.advance(8 * 86400000);
  assert.equal((await retryPending(h.deps))[0].outcome, 'unreachable');
  assert.equal(h.job().terminal, null); assert.equal(h.job().reattachPending, true);
  assert.deepEqual((await h.sweep()).workspacesRemoved, []);
  const unresolvedRef = { backend: 'codex-app-server', host: f.ctx.host, id: '', extra: { tag: h.job().tag } };
  assert.ok(h.backend().cleanupWorkspace);
  assert.equal((await h.backend().cleanupWorkspace!(unresolvedRef)).removed, false);
  assert.equal((await stat(workspace)).isDirectory(), true);
  assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('thread/archive').length, 0);
  // Even a locally ended ledger row is insufficient authority to delete a
  // workspace whose dispatched native start still has no known identity.
  h.deps.jobs.report(h.job().jobId, 'cancelled'); f.clock.advance(2);
  assert.deepEqual((await h.sweep()).workspacesRemoved, []);
  assert.equal((await stat(workspace)).isDirectory(), true);
  f.daemon.setListVisible(true); f.clock.advance(600001);
  await retryPending(h.deps);
  const thread = [...f.daemon.threads.values()][0];
  assert.equal(h.job().sessionRef?.id, thread.id); assert.equal(thread.archived, true);
  assert.deepEqual(f.methods('thread/archive').map(row => row.params.threadId), [thread.id]);
  assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('turn/start').length, 0);
});

test('restart without a caught failure fence keeps an invisible attempted native start recoverable', timeout, async t => {
  const f = await fixture(t), h = await launchHarness(t, f);
  f.daemon.failNext('thread/start', 'accept-drop');
  await assert.rejects(h.backend().launch(f.spec(), f.hooks), error => error instanceof CodexRpcError && error.ambiguous);
  h.deps.ledger.updateJob(h.job().jobId, { state: 'launching' });
  f.daemon.setListVisible(false); await h.restart();
  assert.equal((await reattachAll(h.deps))[0].outcome, 'unreachable');
  assert.equal(h.job().state, 'launching'); assert.equal(h.job().terminal, null);
  f.daemon.setListVisible(true); f.clock.advance(30001);
  const [result] = await retryPending(h.deps);
  assert.equal(result.outcome, 'adopted'); assert.ok('handle' in result);
  if (!('handle' in result)) throw new Error('expected exact owned thread');
  assert.equal(result.handle.ref.id, [...f.daemon.threads.keys()][0]);
  await result.handle.settle('requested');
  assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('thread/archive').length, 1);
});

test('a Git allocation failure before native dispatch is safely absent and its clean workspace is reclaimable', timeout, async t => {
  const f = await fixture(t), h = await launchHarness(t, f), exec = f.ctx.exec;
  f.ctx.exec = async (argv, opts) => {
    const result = await exec(argv, opts);
    return argv.includes('worktree') && argv.includes('add') ? { ...result, code: 1 } : result;
  };
  assert.equal((await startLaunch(h.job(), h.deps)).ok, false);
  assert.equal(h.owned().phase, 'allocated'); assert.equal(h.job().terminal, 'failed');
  assert.equal(f.methods('thread/start').length, 0); assert.equal(h.job().reattachPending, false);
  f.clock.advance(2);
  assert.equal((await h.sweep()).workspacesRemoved.length, 1);
  await assert.rejects(stat(join(f.workspaceRoot, h.job().jobId)), { code: 'ENOENT' });
});

for (const failure of ['retained-broader-policy', 'zero-input-no-rollout'] as const) {
  for (const archiveAvailable of [true, false]) {
    test(`fix2 ${failure}: exact owned cleanup ${archiveAvailable ? 'available' : 'unavailable then retried'} through launcher recovery`, timeout, async t => {
      const f = await fixture(t), h = await launchHarness(t, f);
      const workspace = join(f.workspaceRoot, h.job().jobId);
      const foreign = { id: 'foreign-fix2-thread', cwd: workspace, name: 'untouched foreign thread',
        threadSource: 'foreign-provenance', status: { type: 'idle' }, archived: false, turns: [] };
      f.daemon.threads.set(foreign.id, foreign);
      if (failure === 'retained-broader-policy') f.daemon.retainStartPolicy(policy => { policy.approvalPolicy = 'never'; });
      else {
        f.daemon.rejectZeroInputResume(true);
        // Lose naming's response before any input. This forces the old cleanup
        // path to reconnect, where a zero-input thread has no rollout to resume.
        f.daemon.failNext('thread/name/set', 'drop');
      }
      if (!archiveAvailable) f.daemon.setMethodUnavailable('thread/archive', { code: -32001, message: 'archive temporarily unavailable' });
      let approvalCalls = 0, questionCalls = 0, settlementChecks = 0;
      h.deps.hooks = () => ({ onEvent() {}, async onPermissionRequest() { approvalCalls++; throw new Error('no approval during cleanup'); },
        async onQuestion() { questionCalls++; throw new Error('no question during cleanup'); } });
      function guardTerminalReport() {
        const report = h.deps.jobs.report.bind(h.deps.jobs);
        h.deps.jobs.report = (jobId, state, output, text) => {
          if (state === 'failed') {
            assert.equal(h.owned().settled, true, 'persist exact backend settlement before reporting failure');
            assert.equal(h.owned().phase, 'settled'); settlementChecks++;
          }
          report(jobId, state, output, text);
        };
      }
      function assertNoInputOrForeignOperations() {
        assert.equal(f.methods('turn/start').length, 0); assert.equal(f.methods('turn/steer').length, 0);
        assert.equal(approvalCalls, 0); assert.equal(questionCalls, 0); assert.deepEqual(f.daemon.replies, []);
        assert.deepEqual(h.deps.ledger.openApprovals(), []); assert.deepEqual(h.deps.ledger.openQuestions(), []);
        assert.equal(foreign.archived, false); assert.equal(foreign.name, 'untouched foreign thread');
        assert.deepEqual(foreign.turns, []);
        for (const request of f.daemon.requests.filter(request => Object.hasOwn(request.params, 'threadId'))) {
          assert.equal(request.params.threadId, h.owned().id, request.method);
        }
      }
      guardTerminalReport();
      assert.deepEqual(await startLaunch(h.job(), h.deps), { ok: false, error: 'backend_error' });
      const id = h.owned().id; assert.equal(typeof id, 'string'); assert.notEqual(id, foreign.id);
      const thread = f.daemon.threads.get(id)!;
      assert.equal(thread.turns.length, 0);
      if (failure === 'retained-broader-policy') assert.equal(thread.effective!.approvalPolicy, 'never');
      if (!archiveAvailable) {
        assert.equal(h.job().state, 'launching'); assert.equal(h.job().terminal, null);
        assert.equal(h.job().reattachPending, true); assert.equal(h.deps.ledger.counts().open, 1);
        assert.notEqual(h.owned().settled, true); assert.equal(thread.archived, false);
        assert.deepEqual(h.reports(), ['accepted']);
        await h.restart(); guardTerminalReport();
        // Verify the refusal persists across a real backend/SQLite restart;
        // the fixture never supplies a successful policy-approved attachment.
        if (failure === 'zero-input-no-rollout') {
          await assert.rejects(h.backend().reattach(h.job().sessionRef!, f.hooks), error =>
            error instanceof CodexRpcError && error.code === -32600 && !error.ambiguous);
          assert.ok(f.daemon.noRolloutResumeFailures.includes(id));
          assert.deepEqual(f.daemon.acceptedResumes, []);
        }
        assert.equal((await reattachAll(h.deps))[0].outcome, 'unreachable');
        f.clock.advance(8 * 86400000);
        assert.equal((await retryPending(h.deps))[0].outcome, 'unreachable');
        assert.equal(h.job().terminal, null); assert.equal(h.job().reattachPending, true);
        assert.notEqual(h.owned().settled, true); assert.equal(thread.archived, false);
        assert.deepEqual((await h.sweep()).workspacesRemoved, []);
        assert.equal((await stat(workspace)).isDirectory(), true);
        assert.ok(h.backend().cleanupWorkspace);
        assert.equal((await h.backend().cleanupWorkspace!(h.job().sessionRef!)).removed, false);
        assertNoInputOrForeignOperations();
        f.daemon.setMethodUnavailable('thread/archive'); f.clock.advance(600001);
        assert.equal((await retryPending(h.deps))[0].outcome, 'lost');
      }
      assert.equal(h.job().terminal, 'failed'); assert.equal(h.job().state, 'ended');
      assert.equal(h.job().reattachPending, false); assert.equal(h.deps.ledger.counts().open, 0);
      assert.equal(h.deps.ledger.counts().unparked, 0); assert.deepEqual(h.reports(), ['accepted', 'failed']);
      assert.equal(settlementChecks, 1); assert.equal(h.owned().settled, true); assert.equal(h.owned().phase, 'settled');
      assert.equal(h.job().sessionRef?.id, id); assert.equal(thread.archived, true);
      const archiveCount = f.methods('thread/archive').length;
      assert.ok(archiveCount > 0);
      if (archiveAvailable) assert.equal(archiveCount, 1);
      assert.ok(f.methods('thread/archive').every(request => request.params.threadId === id));
      assertNoInputOrForeignOperations();
      await h.restart();
      assert.equal(h.owned().settled, true); assert.equal(h.owned().phase, 'settled');
      assert.equal(h.job().terminal, 'failed'); assert.deepEqual(await reattachAll(h.deps), []);
      assert.deepEqual(await retryPending(h.deps), []);
      f.clock.advance(2);
      assert.deepEqual((await h.sweep()).workspacesRemoved, [workspace]);
      await assert.rejects(stat(workspace), { code: 'ENOENT' });
      assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('thread/archive').length, archiveCount);
      assertNoInputOrForeignOperations();
    });
  }
}

test('fix2 settleOwned refuses tampered refs before RPC and settles only exact durable provenance without resume', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.retainStartPolicy(policy => { policy.approvalPolicy = 'never'; });
  f.daemon.rejectZeroInputResume(true);
  await assert.rejects(backend.launch(f.spec(), f.hooks));
  const ref = await backend.findByTag(f.spec().tag); assert.ok(ref);
  await assert.rejects(backend.reattach(ref, f.hooks), error => error instanceof CodexRpcError && error.code === -32600 && !error.ambiguous);
  const cleanup = (backend as typeof backend & { settleOwned?: (ref: SessionRef, mode: SettleMode) => Promise<void> }).settleOwned;
  assert.equal(typeof cleanup, 'function'); assert.ok(cleanup);
  const foreign = { id: 'foreign-fix2-direct', cwd: String(ref.extra!.workspace), name: 'foreign-direct',
    threadSource: 'foreign-provenance', status: { type: 'idle' }, archived: false, turns: [] };
  f.daemon.threads.set(foreign.id, foreign);
  const tampered: SessionRef[] = [
    { ...ref, backend: 'foreign-backend' }, { ...ref, host: 'foreign-host' }, { ...ref, id: foreign.id },
    { ...ref, extra: { ...ref.extra, tag: 'vl-job_foreign' } },
    { ...ref, extra: { ...ref.extra, workspace: f.repo } },
    { ...ref, extra: { ...ref.extra, branch: 'foreign-branch' } },
  ];
  const before = f.daemon.requests.length;
  for (const forged of tampered) await assert.rejects(cleanup.call(backend, forged, 'lost'));
  const saved = f.store.get('session:' + f.spec().tag)!;
  const missingProvenance = JSON.parse(saved); delete missingProvenance.threadSource;
  f.store.set('session:' + f.spec().tag, JSON.stringify(missingProvenance));
  try { await assert.rejects(cleanup.call(backend, ref, 'lost')); }
  finally { f.store.set('session:' + f.spec().tag, saved); }
  assert.equal(f.daemon.requests.length, before, 'invalid identity never reaches native RPC');
  const resumes = f.methods('thread/resume').length;
  await cleanup.call(backend, ref, 'lost');
  assert.equal(f.owned().settled, true); assert.equal(f.owned().phase, 'settled');
  await cleanup.call(backend, ref, 'lost');
  assert.deepEqual(f.methods('thread/archive').map(request => request.params.threadId), [ref.id]);
  assert.equal(f.methods('thread/resume').length, resumes); assert.deepEqual(f.daemon.acceptedResumes, []);
  assert.equal(f.methods('turn/start').length, 0); assert.equal(f.methods('turn/steer').length, 0);
  assert.deepEqual(f.permissions, []); assert.deepEqual(f.questions, []); assert.equal(foreign.archived, false);
  assert.equal(foreign.name, 'foreign-direct'); assert.deepEqual(foreign.turns, []);
});

test('the fake accepts duplicate clientUserMessageId starts as distinct turns like the real native API', timeout, async t => {
  const f = await fixture(t), handle = await (await f.create()).launch(f.spec(), f.hooks);
  const rpc = await openCodexRpc({ codexHome: f.daemon.home, euid: process.geteuid!(), log: f.log, timeoutMs: 1000 });
  t.after(() => rpc.close());
  const params = { threadId: handle.ref.id, clientUserMessageId: 'same-client-id', input: [{ type: 'text', text: 'same payload' }] };
  const first = await rpc.call<{ turn: { id: string } }>('turn/start', params, 1000);
  f.daemon.complete(first.turn.id, []);
  const second = await rpc.call<{ turn: { id: string } }>('turn/start', params, 1000);
  assert.notEqual(first.turn.id, second.turn.id);
  assert.equal(f.daemon.threads.get(handle.ref.id)!.turns.flatMap(turn => turn.items).filter(item => item.clientId === 'same-client-id').length, 2);
});

for (const invalid of [
  { name: 'missing data', page: { nextCursor: null } },
  { name: 'object data', page: { data: {}, nextCursor: null } },
  { name: 'null candidate', page: { data: [null], nextCursor: null } },
  { name: 'non-string cursor', page: { data: [], nextCursor: 7 } },
  { name: 'repeated cursor', page: { data: [], nextCursor: 'native-threads:0' } },
]) test(`attempted native recovery rejects ${invalid.name} and never treats malformed listing as absence`, timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'accept-drop');
  await assert.rejects(backend.launch(f.spec(), f.hooks));
  f.daemon.setThreadListPages([invalid.page]);
  await assert.rejects(backend.findByTag(f.spec().tag));
  assert.equal(f.owned().id, undefined);
  assert.equal(f.methods('thread/name/set').length, 0); assert.equal(f.methods('thread/resume').length, 0);
  assert.equal(f.methods('turn/start').length, 0); assert.equal(f.methods('thread/archive').length, 0);
  assert.ok(backend.cleanupWorkspace);
  assert.equal((await backend.cleanupWorkspace({ backend: backend.id, host: f.ctx.host, id: '', extra: { tag: f.spec().tag } })).removed, false);
});

test('native recovery proves global uniqueness across pages before adopting or mutating any candidate', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'accept-drop'); await assert.rejects(backend.launch(f.spec(), f.hooks));
  const actual = [...f.daemon.threads.values()][0];
  // Supply provenance even when running against the pre-fix adapter, so RED
  // here demonstrates early page adoption rather than only missing stamping.
  if (!actual.threadSource) {
    actual.threadSource = 'synthetic-pagination-owner';
    f.store.set('session:' + f.spec().tag, JSON.stringify({ ...f.owned(), threadSource: actual.threadSource }));
  }
  const foreign = { ...actual, id: 'second-same-cwd', turns: [], name: 'unmodified other client' };
  f.daemon.threads.set(foreign.id, foreign);
  // Both candidates carry the same provenance token: only the full listing
  // establishes ambiguity; a per-page ownership check is insufficient.
  f.daemon.setThreadListPages([{ data: [f.daemon.threadView(actual.id)] }, { data: [f.daemon.threadView(foreign.id)] }]);
  await assert.rejects(backend.findByTag(f.spec().tag));
  assert.deepEqual(f.methods('thread/list').map(row => row.params.cursor), [undefined, 'native-threads:1']);
  assert.equal(f.owned().id, undefined);
  for (const method of ['thread/name/set', 'thread/resume', 'turn/start', 'thread/archive']) assert.equal(f.methods(method).length, 0, method);
  assert.equal(foreign.name, 'unmodified other client');
});

test('native recovery traverses an empty first page and accepts only the unique exact provenance match', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'accept-drop'); await assert.rejects(backend.launch(f.spec(), f.hooks));
  const actual = [...f.daemon.threads.values()][0];
  const candidate = f.daemon.threadView(actual.id);
  const foreign = { ...candidate, id: 'different-owner', cwd: f.repo, threadSource: 'different-owner-token', turns: [] };
  f.daemon.setThreadListPages([{ data: [] }, { data: [foreign] }, { data: [candidate] }]);
  const ref = await backend.findByTag(f.spec().tag); assert.ok(ref); assert.equal(ref.id, actual.id);
  assert.equal(f.methods('thread/list').length, 3);
  assert.deepEqual(f.methods('thread/name/set').map(row => row.params.threadId), [actual.id]);
  assert.equal(f.owned().id, actual.id);
});

test('a unique same-cwd thread with different provenance is never adopted', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'drop'); await assert.rejects(backend.launch(f.spec(), f.hooks));
  const foreign = { id: 'foreign-inspector', cwd: join(f.workspaceRoot, f.spec().jobId), source: 'appServer', threadSource: 'foreign' };
  f.daemon.setThreadListPages([{ data: [foreign] }]);
  await assert.rejects(backend.findByTag(f.spec().tag));
  assert.equal(f.owned().id, undefined);
  for (const method of ['thread/name/set', 'thread/resume', 'turn/start', 'thread/archive']) assert.equal(f.methods(method).length, 0, method);
});

test('a malformed later thread-list page prevents adopting the valid first-page candidate', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'accept-drop'); await assert.rejects(backend.launch(f.spec(), f.hooks));
  const actual = [...f.daemon.threads.keys()][0];
  f.daemon.setThreadListPages([{ data: [f.daemon.threadView(actual)] }, { data: 'not-an-array' }]);
  await assert.rejects(backend.findByTag(f.spec().tag));
  assert.equal(f.owned().id, undefined);
  assert.equal(f.methods('thread/list').length, 2);
  for (const method of ['thread/name/set', 'thread/resume', 'turn/start', 'thread/archive']) assert.equal(f.methods(method).length, 0, method);
});

test('a legacy no-id native intent remains unresolved and cannot authorize workspace cleanup', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.failNext('thread/start', 'accept-drop'); await assert.rejects(backend.launch(f.spec(), f.hooks));
  const legacy = f.owned(); delete legacy.phase; delete legacy.threadSource;
  f.store.set('session:' + f.spec().tag, JSON.stringify(legacy));
  f.daemon.setListVisible(false);
  await assert.rejects(backend.findByTag(f.spec().tag));
  assert.ok(backend.cleanupWorkspace);
  assert.equal((await backend.cleanupWorkspace({ backend: backend.id, host: f.ctx.host, id: '', extra: { tag: f.spec().tag } })).removed, false);
  assert.equal((await stat(join(f.workspaceRoot, f.spec().jobId))).isDirectory(), true);
});

const invalidEffectivePolicies: Array<{ name: string; mode: PermissionMode; alter(result: Record<string, any>): void }> = [
  { name: 'read-only widened to workspace-write', mode: 'read-only', alter(r) { r.sandbox = { type: 'workspaceWrite', writableRoots: [r.cwd], networkAccess: false, excludeSlashTmp: true, excludeTmpdirEnvVar: true }; } },
  { name: 'read-only widened to full-access', mode: 'read-only', alter(r) { r.sandbox = { type: 'dangerFullAccess' }; } },
  { name: 'read-only network enabled', mode: 'read-only', alter(r) { r.sandbox.networkAccess = true; } },
  { name: 'missing sandbox', mode: 'ask', alter(r) { delete r.sandbox; } },
  { name: 'unknown sandbox', mode: 'ask', alter(r) { r.sandbox = { type: 'futureUnknown' }; } },
  { name: 'approvals disabled', mode: 'ask', alter(r) { r.approvalPolicy = 'never'; } },
  { name: 'automatic reviewer', mode: 'ask', alter(r) { r.approvalsReviewer = 'auto_review'; } },
  { name: 'missing approval policy', mode: 'ask', alter(r) { delete r.approvalPolicy; } },
  { name: 'missing reviewer', mode: 'ask', alter(r) { delete r.approvalsReviewer; } },
  { name: 'outside writable root', mode: 'ask', alter(r) { r.sandbox.writableRoots.push('/outside-authorized-job'); } },
  { name: 'malformed writable roots', mode: 'ask', alter(r) { r.sandbox.writableRoots = 'not-an-array'; } },
  { name: 'network enabled', mode: 'ask', alter(r) { r.sandbox.networkAccess = true; } },
  { name: 'additional slash-tmp write scope', mode: 'ask', alter(r) { r.sandbox.excludeSlashTmp = false; } },
  { name: 'additional TMPDIR write scope', mode: 'ask', alter(r) { r.sandbox.excludeTmpdirEnvVar = false; } },
  { name: 'missing runtime workspace roots', mode: 'ask', alter(r) { delete r.runtimeWorkspaceRoots; } },
  { name: 'extra runtime workspace root', mode: 'ask', alter(r) { r.runtimeWorkspaceRoots.push('/outside-authorized-job'); } },
  { name: 'different effective cwd', mode: 'ask', alter(r) { r.cwd = '/outside-authorized-job'; } },
];

for (const scenario of invalidEffectivePolicies) {
  test(`effective start policy blocks initial instructions: ${scenario.name}`, timeout, async t => {
    const f = await fixture(t), backend = await f.create(), spec = f.spec('policy', { permissionMode: scenario.mode });
    f.daemon.nextResponse('thread/start', result => { scenario.alter(result); return result; });
    await assert.rejects(backend.launch(spec, f.hooks));
    assert.equal(f.methods('thread/start').length, 1); assert.equal(f.methods('turn/start').length, 0);
    const nativeId = [...f.daemon.threads.keys()][0];
    assert.equal(f.owned(spec.tag).id, nativeId, 'retain exact ownership after a policy refusal');
    assert.ok(backend.cleanupWorkspace);
    assert.equal((await backend.cleanupWorkspace({ backend: backend.id, host: f.ctx.host, id: nativeId, extra: { tag: spec.tag } })).removed, false);
    assert.equal((await stat(join(f.workspaceRoot, spec.jobId))).isDirectory(), true);
  });

  for (const seam of ['reattach', 'reconnect'] as const) {
    test(`effective policy is fenced on ${seam}: ${scenario.name}`, timeout, async t => {
      const f = await fixture(t), backend = await f.create();
      const handle = await backend.launch(f.spec('policy_resume', { permissionMode: scenario.mode }), f.hooks);
      f.daemon.complete(f.daemon.threads.get(handle.ref.id)!.turns[0].id, [final('initial')]);
      if (seam === 'reconnect') {
        // The failed read is a transport barrier: the backend has observed the
        // close before a subsequent input attempts a fresh subscription.
        f.daemon.failNext('thread/read', 'drop'); await assert.rejects(handle.activity());
      } else f.daemon.disconnect();
      f.daemon.nextResponse('thread/resume', result => { scenario.alter(result); return result; });
      if (seam === 'reattach') await assert.rejects((await f.create()).reattach(handle.ref, f.hooks));
      else await assert.rejects(handle.send(input(['must-not-dispatch']), 'start'));
      assert.equal(f.methods('thread/resume').length, 1);
      assert.equal(f.methods('turn/start').length, 1, 'no instructions after a resume policy mismatch');
      assert.equal(f.methods('turn/steer').length, 0); assert.equal(f.methods('thread/archive').length, 0);
      assert.deepEqual(f.methods('thread/resume')[0].params, { threadId: handle.ref.id, excludeTurns: true });
    });
  }
}

test('same-backend reattach cannot return an unverified handle left by a rejected start policy', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  f.daemon.nextResponse('thread/start', result => { result.approvalPolicy = 'never'; return result; });
  await assert.rejects(backend.launch(f.spec(), f.hooks));
  const thread = [...f.daemon.threads.values()][0]; thread.effective!.approvalPolicy = 'never';
  const ref = await backend.findByTag(f.spec().tag); assert.ok(ref);
  await assert.rejects(backend.reattach(ref, f.hooks));
  assert.equal(f.methods('turn/start').length, 0); assert.equal(f.methods('thread/start').length, 1);
});

test('an already owned handle can settle its exact thread after reconnect policy refusal', timeout, async t => {
  const f = await fixture(t), backend = await f.create(), handle = await backend.launch(f.spec(), f.hooks);
  const thread = f.daemon.threads.get(handle.ref.id)!;
  f.daemon.failNext('thread/read', 'drop'); await assert.rejects(handle.activity());
  thread.effective!.approvalPolicy = 'never';
  await assert.rejects(handle.send(input(['blocked-after-policy-change']), 'start'));
  const result = await handle.settle('requested');
  assert.equal(result.transcriptRef, `codex:${thread.id}`);
  assert.deepEqual(f.methods('thread/archive').map(row => row.params.threadId), [thread.id]);
  assert.equal(f.methods('turn/start').length, 1); assert.equal(thread.archived, true);
});

async function nativeRoutingHarness(t: TestContext, config: { presets?: Array<'outside-workspace' | 'privileged'>; network?: boolean }) {
  const f = await fixture(t), h = await launchHarness(t, f);
  const spec = f.spec('00000000000000000000000000');
  h.deps.ledger.createJob({ ...h.job(), jobId: spec.jobId, tag: spec.tag, threadId: 'approval-job-thread' });
  const job = () => h.deps.ledger.job(spec.jobId)!;
  h.deps.cfg.approvals.to_operator = { presets: config.presets ?? [], categories: config.network ? ['network'] : [], requesters: [] };
  const callbacks: Parameters<Outbox['onSent']>[0][] = [];
  const outbox: Outbox = { enqueue: (...args) => { h.deps.ledger.enqueue(...args); }, kick() {}, async run() {},
    onSent(fn) { callbacks.push(fn); }, onRejected() {} };
  const dir: Directory = { operatorPeerId: () => 'peer_operator', isOperator: id => id === 'peer_operator', name: id => id,
    createdBy: () => undefined, get: () => undefined, kindOf: () => undefined, async refresh() {}, async ensureKnown() { return false; },
    async onDirectoryUpdated() {}, onRemoved() {} };
  const canonical: LaunchPermission[] = [];
  const bridge = createParentBridge({ ledger: h.deps.ledger, cfg: h.deps.cfg, outbox, clock: f.clock, log: f.log,
    self: 'peer_launcher', home: f.daemon.home, dir,
    route(permission, row) { canonical.push(permission); return routeApproval(permission, row, dir, h.deps.cfg, f.daemon.home); } });
  const hooks = bridge.hooksFor(job(), () => job().workspace ?? join(f.workspaceRoot, spec.jobId), () => {});
  const handle = await h.backend().launch(spec, hooks);
  h.deps.ledger.updateJob(spec.jobId, { sessionRef: handle.ref, workspace: String(handle.ref.extra!.workspace), state: 'running' });
  const prompts = () => h.deps.ledger.pendingOutbox(100, f.clock.now()).filter(row => row.kind === 'send')
    .map(row => row.payload as SendRequest).filter(row => row.kind === 'approval_request');
  return { f, h, handle, canonical, prompts, bridge,
    async drain() { await handle.activity(); await handle.activity(); },
    async decide(prompt: SendRequest, actor = 'peer_operator') {
      const row = h.deps.ledger.pendingOutbox(100, f.clock.now()).find(row => row.key === prompt.idempotency_key)!;
      const sent = { message_id: 'approval-card', thread_id: prompt.thread_id ?? 'operator-thread', deliveries: [], spilled: false };
      h.deps.ledger.markOutbox(row.id, 'sent', sent);
      for (const callback of callbacks) callback(row, sent);
      const response = threadDelivery(sent.thread_id, actor, '', 'wake');
      response.message.kind = 'approval_response';
      response.message.approval_response = { approval_id: prompt.approval_request!.approval_id, decision: prompt.approval_request!.options[0] };
      assert.equal(await bridge.handleApprovalResponse(response), 'consumed');
      await handle.activity(); await handle.activity();
    },
    finish() { bridge.abandonAll(spec.jobId, 'session_ended'); },
  };
}

for (const kind of ['outside-grant-root', 'sensitive-file-change', 'moved-sensitive-file', 'network-only'] as const) {
  test(`native ${kind} reaches the operator through real canonicalization, routing and parent bridge`, timeout, async t => {
    const s = await nativeRoutingHarness(t, kind === 'network-only' ? { network: true } :
      { presets: [kind === 'outside-grant-root' ? 'outside-workspace' : 'privileged'] });
    try {
      const threadId = s.handle.ref.id, turnId = s.f.daemon.threads.get(threadId)!.turns[0].id;
      let method = 'item/fileChange/requestApproval';
      const params: Record<string, any> = { threadId, turnId, itemId: 'approval-item' };
      const path = kind === 'outside-grant-root' ? join(s.f.dir, 'outside-workspace') : join(s.f.daemon.home, '.ssh', 'config');
      if (kind === 'outside-grant-root') params.grantRoot = path;
      else if (kind === 'sensitive-file-change' || kind === 'moved-sensitive-file') s.f.daemon.notify('item/started', { threadId, turnId,
        item: { id: params.itemId, type: 'fileChange', status: 'inProgress', changes: [{
          path: kind === 'moved-sensitive-file' ? join(String(s.handle.ref.extra!.workspace), 'tracked.txt') : path,
          kind: { type: 'update', ...(kind === 'moved-sensitive-file' ? { move_path: path } : {}) }, diff: '+sensitive change' }] } });
      else { method = 'item/commandExecution/requestApproval'; params.networkApprovalContext = { host: 'api.example.invalid', protocol: 'https' }; }
      const requestId = s.f.daemon.serverRequest(method, params);
      await s.drain();
      assert.equal(s.prompts().length, 1);
      const prompt = s.prompts()[0]; assert.deepEqual(prompt.recipients, ['peer_operator']);
      const permission = s.canonical[0]; assert.ok(permission);
      assert.equal(permission.backend_method, method);
      if (kind === 'network-only') {
        assert.equal(permission.kind, 'network'); assert.equal(permission.fidelity.host, 'enforced');
        assert.deepEqual(permission.host, { name: 'api.example.invalid', protocol: 'https' });
      } else {
        assert.equal(permission.kind, 'edit'); assert.equal(permission.fidelity.paths, 'enforced');
        assert.equal(permission.outside_workspace, true);
        assert.ok(permission.paths?.some(entry => entry.path === path && entry.access === (kind === 'moved-sensitive-file' ? 'move' : 'write')));
        if (kind === 'outside-grant-root') {
          assert.equal(permission.scope, 'session'); assert.equal(permission.options[0].grants, 'session');
        }
      }
      assert.equal(s.f.daemon.replies.some(reply => reply.id === requestId), false, 'wait for the actual operator decision');
      await s.decide(prompt);
      assert.deepEqual(s.f.daemon.replies.filter(reply => reply.id === requestId), [{ id: requestId, result: { decision: 'accept' } }]);
    } finally { s.finish(); }
  });
}

for (const variant of ['unknown-paths', 'mismatched-item', 'additional-permissions'] as const) {
  test(`native unrepresentable approval fails closed without a requester card: ${variant}`, timeout, async t => {
    const s = await nativeRoutingHarness(t, {});
    try {
      const threadId = s.handle.ref.id, turnId = s.f.daemon.threads.get(threadId)!.turns[0].id;
      const params: Record<string, any> = { threadId, turnId, itemId: 'unknown-item' };
      let method = 'item/fileChange/requestApproval';
      if (variant === 'mismatched-item') s.f.daemon.notify('item/started', { threadId, turnId: 'different-turn',
        item: { id: params.itemId, type: 'fileChange', changes: [{ path: join(s.f.dir, 'outside'), kind: { type: 'add' } }] } });
      if (variant === 'additional-permissions') {
        method = 'item/commandExecution/requestApproval'; params.command = 'git status';
        params.additionalPermissions = { fileSystem: { write: ['/outside'] } };
      }
      const id = s.f.daemon.serverRequest(method, params); await s.drain();
      assert.deepEqual(s.prompts(), []); assert.deepEqual(s.canonical, []);
      assert.deepEqual(s.f.daemon.replies.filter(reply => reply.id === id), [{ id, result: { decision: 'decline' } }]);
    } finally { s.finish(); }
  });
}


test('standalone native launcher check prints health and exits without leaving its probe socket open', timeout, async t => {
  const f = await fixture(t), config = join(f.dir, 'check.json');
  await writeFile(join(f.dir, 'credential'), 'fixture-only-not-a-live-secret\n', { mode: 0o600 });
  await writeFile(config, JSON.stringify({host: 'check', hub: {url: 'http://127.0.0.1:1', credential_file: join(f.dir, 'credential')}, state_dir: join(f.dir, 'state'),
    backends: [{id: 'codex-app-server', module: './backends/codex-app-server/index.ts', config: f.config}],
    projects: {test: {repo_root: f.repo, base_ref: 'base'}}, policies: {default: {backends: ['codex-app-server'], projects: ['test']}} }));
  const main = fileURLToPath(new URL('../../src/main.ts', import.meta.url));
  const stdout = await new Promise<string>((resolve, reject) => execFile(process.execPath, ['--disable-warning=ExperimentalWarning', main, 'check', '--config', config],
    {timeout: 5000}, (error, stdout) => error ? reject(error) : resolve(stdout)));
  const report = JSON.parse(stdout);
  assert.equal(report.backends[0].ok, true); assert.equal(report.backends[0].version, '0.166.0');
  assert.equal(f.methods('thread/resume').length, 0); assert.equal(f.methods('thread/start').length, 0);
});


test('native reused RPC id cannot replay a prior requester grant onto a changed operator scope', timeout, async t => {
  const s = await nativeRoutingHarness(t,{presets:['outside-workspace','privileged']});
  try {
    const threadId=s.handle.ref.id,turnId=s.f.daemon.threads.get(threadId)!.turns[0].id;
    const method='item/fileChange/requestApproval';
    const initial={threadId,turnId,itemId:'first-file-approval',grantRoot:String(s.handle.ref.extra!.workspace)};
    const id=s.f.daemon.serverRequest(method,initial);await s.drain();
    const first=s.prompts()[0];assert.ok(first);assert.deepEqual(first.recipients,[s.h.job().requester]);
    await s.decide(first,s.h.job().requester);
    assert.equal(s.f.daemon.replies.filter(r=>r.id===id).length,1);
    const changed={...initial,itemId:'second-file-approval',grantRoot:join(s.f.daemon.home,'.ssh')};
    s.f.daemon.serverRequest(method,changed,id);await s.drain();
    const next=s.prompts().find(prompt=>prompt.idempotency_key!==first.idempotency_key);
    assert.ok(next,'changed scope must open a distinct approval instead of replaying the old grant');
    assert.deepEqual(next.recipients,['peer_operator']);
    assert.equal(s.f.daemon.replies.filter(r=>r.id===id).length,1,'no cached acceptance before operator decision');
    await s.decide(next);
    assert.deepEqual(s.f.daemon.replies.filter(r=>r.id===id).map(r=>r.result),[{decision:'accept'},{decision:'accept'}]);
  } finally {s.finish();}
});
