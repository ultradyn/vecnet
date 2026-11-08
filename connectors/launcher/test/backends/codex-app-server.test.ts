import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, mkdir, readFile, writeFile, rm, stat, lstat, readlink } from 'node:fs/promises';
import { join } from 'node:path';
import plugin from '../../src/backends/codex-app-server/index.ts';
import type { CodexConfig } from '../../src/backends/codex-app-server/index.ts';
import { CodexRpcError } from '../../src/backends/codex-app-server/rpc.ts';
import type { LaunchSpec, PluginContext, SessionHooks, SessionHandle, TurnCursor, TurnInput, PermissionMode, PermissionRequest, AgentQuestion } from '../../src/backend.ts';
import { fakeClock, memoryLog } from '../../../hub-client/testing/fixtures.ts';
import { startFakeNativeCodex } from '../fake-native-codex.ts';

const timeout = { timeout: 10000 };
const input = (ids = ['delivery-1', 'delivery-2']): TurnInput => ({ items: ids.map((deliveryId, i) => ({
  deliveryId, messageId: `message-${i}`, from: 'peer-requester', text: `Follow-up ${i}`, attachments: [],
})) });
const final = (text: string) => ({ type: 'agentMessage', phase: 'final_answer', text });

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
    for (const key of ['model', 'config', 'modelProvider', 'effort']) assert.equal(Object.hasOwn(params, key), false, key);
  }
  assert.equal(await readFile(join(f.daemon.home, 'config.toml'), 'utf8'), configText);
  await backend.launch(f.spec('explicit', { preset: 'explicit' }), f.hooks);
  const explicit = f.methods('thread/start').at(-1)!.params;
  assert.equal(explicit.model, 'explicit-model'); assert.deepEqual(explicit.config, { model_reasoning_effort: 'high' });
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
  const f = await fixture(t), backend = await f.create();
  const spec = f.spec('temporary', { workspaceMode: 'temp',
    project: { alias: 'temporary', repoRoot: f.repo, baseRef: 'unused-base-ref' } });
  const handle = await backend.launch(spec, f.hooks), workspace = join(f.workspaceRoot, spec.jobId);
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

test('invalid workspace modes and temporary-alias combinations reject before Git or workspace mutation', timeout, async t => {
  const f = await fixture(t), backend = await f.create();
  const choices: [LaunchSpec['workspaceMode'], string][] = [
    ['invalid' as LaunchSpec['workspaceMode'], 'isolated'], ['temp', 'isolated'],
    ['project-root', 'temporary'], ['worktree', 'temporary'], [undefined, 'temporary'],
  ];
  for (const [index, [workspaceMode, alias]] of choices.entries()) {
    await assert.rejects(backend.launch(f.spec(`invalid_workspace_${index}`, { workspaceMode,
      project: { alias, repoRoot: f.repo, baseRef: 'base' } }), f.hooks), /Invalid workspace choice/);
  }
  assert.deepEqual(f.calls, []);
  assert.equal(f.store.size, 0); assert.equal(f.methods('thread/start').length, 0);
  await assert.rejects(stat(f.workspaceRoot), { code: 'ENOENT' });
  assert.equal(await f.git('-C', f.repo, 'branch', '--show-current'), 'base');
  assert.equal(await f.git('-C', f.repo, 'rev-parse', 'HEAD'), f.head);
  assert.equal(await readFile(join(f.repo, 'tracked.txt'), 'utf8'), 'base contents\n');
});

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
    await assert.rejects(restarted.findByTag(spec.tag), /project-root launch outcome unknown/);
    await assert.rejects(restarted.launch(spec, f.hooks), /project-root launch outcome unknown/);
    assert.deepEqual(await restarted.listOwned(), []);
  }
  assert.equal(f.methods('thread/start').length, 1);
  for (const method of ['thread/list', 'thread/resume', 'thread/name/set', 'turn/start', 'thread/archive']) {
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
  assert.equal(Object.hasOwn(f.methods('thread/start')[0].params, 'config'), false);
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
  for (let i = 0; i < 2; i++) await assert.rejects((await f.create()).launch(f.spec(), f.hooks), /outcome unknown/);
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
  const edit = f.daemon.serverRequest('item/fileChange/requestApproval', { threadId: handle.ref.id });
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
