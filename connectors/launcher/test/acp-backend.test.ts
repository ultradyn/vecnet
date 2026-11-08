import test from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, realpath, symlink, writeFile, readdir, lstat } from 'node:fs/promises';
import { join } from 'node:path';
import { homedir } from 'node:os';
import fsPromises from 'node:fs/promises';
import { syncBuiltinESMExports } from 'node:module';
import { fileURLToPath } from 'node:url';
import plugin from '../src/backends/acp/index.ts';
import type { AcpConfig } from '../src/backends/acp/index.ts';
import type { AcpLaunchSpec } from '../src/backends/acp/workspace.ts';
import type { PluginContext, SessionHandle, SessionHooks, TurnEvent, TurnInput } from '../src/backend.ts';
import { redactText } from '../src/redact.ts';
import { acpTestDir, acpTestParent } from './fixtures/acp-test-root.ts';

const fixturePath = fileURLToPath(new URL('./fixtures/fake-acp-agent.mjs', import.meta.url));
const input = (text = 'follow-up', id = 'dly_one'): TurnInput => ({ items: [{ deliveryId: id, messageId: 'msg_one', from: 'peer_one', text, attachments: [] }] });
async function fixture(t: TestContext, scenario = 'normal') {
  const handles = new Set<SessionHandle>();
  const dir = await acpTestDir(t, 'vecnet-acp-', async () => { for (const h of handles) await h.settle('cancel'); });
  const values = new Map<string, string>(), execs: string[][] = [], log = join(dir, 'wire.jsonl');
  const ctx: PluginContext = {
    host: 'test-host', now: () => new Date(), redact: text => redactText(text).text,
    log: { info() {}, warn() {}, error() {} },
    store: { get: k => values.get(k), set: (k, v) => { values.set(k, v); }, delete: k => { values.delete(k); }, keys: p => [...values.keys()].filter(k => k.startsWith(p ?? '')) },
    checkRoute: async () => { throw new Error('route mutation prohibited'); }, readSecretFile: async () => { throw new Error('secret access prohibited'); },
    sleep: (ms, signal) => new Promise((resolve, reject) => {
      if (signal?.aborted) { reject(new Error('aborted')); return; }
      const done = () => { clearTimeout(timer); signal?.removeEventListener('abort', abort); resolve(); };
      const abort = () => { clearTimeout(timer); signal?.removeEventListener('abort', abort); reject(new Error('aborted')); };
      const timer = setTimeout(done, ms); signal?.addEventListener('abort', abort, { once: true });
    }),
    exec: async argv => { execs.push(argv); return { code: 0, stdout: '', stderr: '' }; },
  };
  const config: AcpConfig = { workspace_root: join(dir, 'workspaces'), request_timeout_ms: 1000,
    presets: { fake: { argv: [process.execPath, fixturePath, scenario, log], provider: 'fixture', mode_id: 'ask' } } };
  const hooks: SessionHooks = { onEvent() {}, onPermissionRequest: async () => ({ optionId: 'no', decidedBy: 'test' }), onQuestion: async () => { throw new Error('not advertised'); } };
  const spec = (over: Partial<AcpLaunchSpec> = {}): AcpLaunchSpec => ({ jobId: 'job_test', tag: 'vl-job_test', title: 'test', prompt: 'Hello',
    project: { alias: 'any-scratch-name', repoRoot: '/not/preallocated', baseRef: '', scratch: true }, branch: 'launch/job_test', workspaceMode: 'temp',
    preset: 'fake', permissionMode: 'ask', attachments: [], env: { VECNET_CREDENTIAL: 'test-secret-must-be-scrubbed' }, ...over });
  const backend = await plugin.create(config, ctx);
  const launch = async (over: Partial<AcpLaunchSpec> = {}, useHooks = hooks) => { const h = await backend.launch(spec(over), useHooks); handles.add(h); return h; };
  const calls = async (): Promise<Array<Record<string, any>>> => (await readFile(log, 'utf8')).trim().split('\n').filter(Boolean).map(line => JSON.parse(line));
  return { dir, ctx, config, hooks, backend, spec, launch, calls, values, execs };
}
async function eventsUntil(h: SessionHandle, predicate: (e: TurnEvent) => boolean): Promise<TurnEvent[]> {
  const controller = new AbortController(), events: TurnEvent[] = [];
  const timeout = setTimeout(() => controller.abort(), 2000);
  try {
    for await (const { event } of h.turns(undefined, controller.signal)) { events.push(event); if (predicate(event)) return events; }
    throw new Error('expected event not received');
  } finally { clearTimeout(timeout); controller.abort(); }
}
const finished = (event: TurnEvent) => event.type === 'turn_finished';

test('ACP v1 initialize/new/set_mode/prompt order, streamed final text, no thoughts, no filesystem or terminal capability', async t => {
  const f = await fixture(t), h = await f.launch(), events = await eventsUntil(h, finished);
  const final = events.find(e => e.type === 'turn_finished');
  assert.equal(final?.type === 'turn_finished' && final.finalText, 'Hi 🌊 there');
  assert.equal(final?.type === 'turn_finished' && final.outcome, 'completed');
  assert.equal(JSON.stringify(events).includes('private thought'), false);
  assert.ok(events.some(e => e.type === 'progress' && e.summary.includes('🌊')));
  const calls = await f.calls();
  assert.deepEqual(calls.slice(0, 4).map(c => c.method), ['initialize', 'session/new', 'session/set_mode', 'session/prompt']);
  assert.deepEqual(calls[0].params.clientCapabilities, {});
  assert.equal(calls[1].params.cwd, join(f.dir, 'workspaces', 'job_test'));
  assert.deepEqual(calls[1].params.mcpServers, []);
  assert.equal(f.execs.length, 0);
  const caps = await f.backend.capabilities();
  assert.equal(caps.resume, 'none'); assert.equal(caps.steer, false); assert.equal(caps.queue, 'launcher');
  assert.equal(caps.approvals, 'typed'); assert.deepEqual(caps.permissionModes, ['ask']);
  assert.deepEqual((caps as typeof caps & { workspaceModes: string[] }).workspaceModes, ['worktree', 'project-root', 'temp']);
});

test('same-owner reattach and launch retries do not spawn or prompt twice; foreign ownership is rejected', async t => {
  const f = await fixture(t), h = await f.launch(); await eventsUntil(h, finished);
  assert.equal(await f.launch(), h);
  const attached = await f.backend.reattach(h.ref, f.hooks); assert.equal(attached.found, 'live');
  assert.equal(attached.handle, h);
  assert.deepEqual(await f.backend.findByTag('vl-job_test'), h.ref);
  assert.equal((await f.backend.listOwned()).length, 1);
  assert.equal((await f.backend.reattach({ ...h.ref, host: 'foreign' }, f.hooks)).found, 'none');
  assert.equal((await f.backend.reattach({ ...h.ref, id: 'foreign-session' }, f.hooks)).found, 'none');
  assert.equal((await f.calls()).filter(c => c.method === 'session/new').length, 1);
});

test('a new backend owner cannot adopt persisted stdio or respawn its job', async t => {
  const f = await fixture(t), h = await f.launch(); await eventsUntil(h, finished);
  const restarted = await plugin.create(f.config, f.ctx);
  await assert.rejects(restarted.reattach(h.ref, f.hooks), /outcome_unknown/);
  await assert.rejects(restarted.launch(f.spec(), f.hooks), /outcome_unknown/);
  await assert.rejects(restarted.findByTag('vl-job_test'), /outcome_unknown/);
  await assert.rejects(restarted.listOwned(), /ownership_unknown/);
  assert.equal((await f.calls()).filter(c => c.method === 'session/new').length, 1);
});

test('launch reserves tag before await; simultaneous calls share one spawn', async t => {
  const f = await fixture(t);
  const [a, b] = await Promise.all([f.launch(), f.launch()]);
  assert.equal(a, b); await eventsUntil(a, finished);
  assert.equal((await f.calls()).filter(c => c.method === 'session/new').length, 1);
});

test('delivery retries are scoped to session and payload, with no second prompt or partial-overlap replay', async t => {
  const f = await fixture(t), h = await f.launch(); await eventsUntil(h, finished);
  const r = await h.send(input(), 'start');
  assert.deepEqual(await h.send(input(), 'start'), r);
  await assert.rejects(h.send(input('changed'), 'start'), /conflict/);
  await assert.rejects(h.send({ items: [...input().items, ...input('new', 'dly_two').items] }, 'start'), /conflict/);
  await assert.rejects(h.send(input('new', 'dly_two'), 'queue'), /native_queue/);
  await assert.rejects(h.send(input('new', 'dly_two'), 'steer'), /no_steer/);
  await eventsUntil(h, e => e.type === 'turn_finished' && e.turnId === r.turnId);
  assert.equal((await f.calls()).filter(c => c.method === 'session/prompt').length, 2);
});

test('permission option mapping, reported fidelity, hook selection preserves exact ACP option id', async t => {
  const f = await fixture(t, 'permission');
  let seen: Parameters<SessionHooks['onPermissionRequest']>[0] | undefined;
  f.hooks.onPermissionRequest = async req => { seen = req; return { optionId: 'yes', decidedBy: 'peer_parent' }; };
  const h = await f.launch(); await eventsUntil(h, finished);
  assert.equal(seen?.permission.kind, 'edit'); assert.equal(seen?.permission.fidelity.paths, 'reported');
  assert.equal(seen?.permission.fidelity.kind, 'reported'); assert.equal(seen?.permission.command?.line, 'touch /outside/file');
  assert.equal(seen?.permission.paths?.[0].access, 'write');
  const response = (await f.calls()).find(c => c.id === 1001 && c.result);
  assert.deepEqual(response?.result, { outcome: { outcome: 'selected', optionId: 'yes' } });
});

test('cancel aborts a hanging permission hook, returns cancelled outcome, and awaits real prompt stop reason', async t => {
  const f = await fixture(t, 'permission');
  let received!: () => void, permissionSignal: AbortSignal | undefined;
  const requested = new Promise<void>(resolve => { received = resolve; });
  f.hooks.onPermissionRequest = async (_req, signal) => { permissionSignal = signal; received(); return new Promise(() => {}); };
  const h = await f.launch(); await requested;
  assert.equal(h.state().kind, 'waiting_approval');
  await h.interrupt('test'); assert.equal(permissionSignal?.aborted, true);
  const events = await eventsUntil(h, finished), final = events.find(finished);
  assert.equal(final?.type === 'turn_finished' && final.outcome, 'interrupted');
  const calls = await f.calls();
  assert.ok(calls.some(c => c.method === 'session/cancel' && !Object.hasOwn(c, 'id')));
  assert.deepEqual(calls.find(c => c.id === 1001 && c.result)?.result, { outcome: { outcome: 'cancelled' } });
});

for (const scenario of ['unknown-option', 'foreign']) test(`ACP refuses unsafe permission or foreign traffic: ${scenario}`, async t => {
  const f = await fixture(t, scenario), h = await f.launch(), events = await eventsUntil(h, finished);
  assert.equal(JSON.stringify(events).includes('foreign-text'), false);
  const response = (await f.calls()).find(c => c.id === 1001 && c.result);
  assert.deepEqual(response?.result, { outcome: { outcome: 'cancelled' } });
});

for (const [stop, outcome] of [['end_turn', 'completed'], ['max_tokens', 'max_tokens'], ['max_turn_requests', 'failed'], ['refusal', 'refused'], ['cancelled', 'interrupted']]) {
  test(`ACP stop reason ${stop} maps to ${outcome}`, async t => {
    const f = await fixture(t, 'stop:' + stop), h = await f.launch();
    const final = (await eventsUntil(h, finished)).find(finished);
    assert.equal(final?.type === 'turn_finished' && final.outcome, outcome);
  });
}

for (const scenario of ['v2', 'auth', 'no-mode', 'new-exit', 'init-hold']) test(`handshake rejects ${scenario} and persisted fence forbids retry spawn`, async t => {
  const f = await fixture(t, scenario);
  await assert.rejects(f.launch(), /acp_launch_failed/);
  await assert.rejects(f.launch(), /outcome_unknown/);
  assert.equal((await f.calls()).filter(c => c.method === 'initialize').length, 1);
});

for (const scenario of ['bad-stdout', 'exit', 'mode-change']) test(`transport or permission-policy loss ends unknown, without synthetic completed turn: ${scenario}`, async t => {
  const f = await fixture(t, scenario), h = await f.launch();
  const events = await eventsUntil(h, e => e.type === 'state' && e.state.kind === 'ended');
  assert.equal(events.some(e => e.type === 'turn_finished'), false);
  assert.equal(h.state().kind, 'ended');
  await eventsUntil(h, e => e.type === 'process_exited');
  assert.equal((await f.backend.reattach(h.ref, f.hooks)).found, 'none');
  await assert.rejects(f.launch(), /outcome_unknown/);
});

test('unknown client request receives JSON-RPC method-not-found; no terminal code executes', async t => {
  const f = await fixture(t, 'unsupported-client'), h = await f.launch(); await eventsUntil(h, finished);
  const calls = await f.calls(); assert.ok(calls.some(c => c.id === 1001 && c.error?.code === -32601));
  assert.equal(f.execs.length, 0);
});

test('turn cursors replay only unseen events; invalid/foreign cursors fail closed', async t => {
  const f = await fixture(t), h = await f.launch(); await eventsUntil(h, finished);
  const controller = new AbortController(); let cursor: { value: string } | undefined;
  for await (const entry of h.turns(undefined, controller.signal)) { cursor = entry.cursor; if (entry.event.type === 'turn_finished') break; }
  const result = await h.send(input(), 'start');
  const seen: TurnEvent[] = [];
  for await (const entry of h.turns(cursor, controller.signal)) {
    seen.push(entry.event); if (entry.event.type === 'turn_finished' && entry.event.turnId === result.turnId) break;
  }
  controller.abort();
  assert.equal(seen.filter(e => e.type === 'turn_finished').length, 1);
  const bad = h.turns({ value: JSON.stringify({ tag: 'foreign', n: 0 }) })[Symbol.asyncIterator]();
  await assert.rejects(bad.next(), /cursor_invalid/);
});

test('settle retains workspace, terminates process, excludes owned discovery and never cleans project files', async t => {
  const f = await fixture(t, 'hold'), h = await f.launch();
  const result = await h.settle('cancel'); assert.equal(result.workspaceKept, true);
  assert.equal(h.state().kind, 'ended'); assert.equal(await f.backend.findByTag('vl-job_test'), null);
  assert.deepEqual(await f.backend.listOwned(), []);
  assert.equal((await f.backend.reattach(h.ref, f.hooks)).found, 'none');
  await assert.rejects(h.send(input(), 'start'));
  assert.equal(f.execs.length, 0);
});

test('unsupported permission modes and attachment inputs fail before spawn', async t => {
  const f = await fixture(t);
  for (const permissionMode of ['read-only', 'auto', 'full-access'] as const) await assert.rejects(f.launch({ permissionMode }), /unsupported/);
  await assert.rejects(f.launch({ attachments: [{ path: '/file', name: 'file', mediaType: 'text/plain', sha256: '0' }] }), /unsupported/);
  assert.equal(f.values.size, 0);
});


test('interrupt stops a running turn and permits a follow-up; queued work remains launcher-owned', async t => {
  const f = await fixture(t, 'hold-after-first'), h = await f.launch(); await eventsUntil(h, finished);
  const result = await h.send(input(), 'start');
  assert.equal(h.state().kind, 'turn_running');
  await assert.rejects(h.send(input('second', 'dly_two'), 'start'), /busy/);
  await h.interrupt('requested');
  const final = (await eventsUntil(h, e => e.type === 'turn_finished' && e.turnId === result.turnId)).find(e => e.type === 'turn_finished' && e.turnId === result.turnId);
  assert.equal(final?.type === 'turn_finished' && final.outcome, 'interrupted');
  assert.equal(h.state().kind, 'idle');
  const again = await h.send(input('after interrupt', 'dly_three'), 'start');
  await h.interrupt('cleanup');
  await eventsUntil(h, e => e.type === 'turn_finished' && e.turnId === again.turnId);
});

test('different launch payload cannot attach to the same reserved job', async t => {
  const f = await fixture(t);
  const first = f.launch();
  await assert.rejects(f.launch({ prompt: 'a different task' }), /identity_conflict/);
  const h = await first; await eventsUntil(h, finished);
  await assert.rejects(f.launch({ prompt: 'a different task' }), /identity_conflict/);
});


test('permission mode change while idle also closes the session before any new input', async t => {
  const f = await fixture(t, 'mode-change-idle'), h = await f.launch();
  await eventsUntil(h, e => e.type === 'state' && e.state.kind === 'ended');
  await assert.rejects(h.send(input(), 'start'), /unsupported/);
  assert.equal((await f.calls()).filter(c => c.method === 'session/prompt').length, 1);
});


for (const scenario of ['interleave-same-id-plan', 'interleave-same-id-tool', 'interleave-no-id-plan', 'interleave-no-id-tool']) {
  test(`I3 preserves a complete assistant message across ${scenario}`, async t => {
    const f = await fixture(t, scenario), h = await f.launch();
    const final = (await eventsUntil(h, finished)).find(finished);
    assert.equal(final?.type === 'turn_finished' && final.finalText, 'First half second half');
    assert.equal(final?.type === 'turn_finished' && final.outcome, 'completed');
  });
}


test('I4 child environment preserves safe inputs and denies launcher/credential/route variables', async t => {
  const f = await fixture(t, 'env-oracle');
  const safe = { HOME: join(f.dir, 'synthetic-home'), PATH: '/synthetic/path', LANG: 'C.UTF-8', TMPDIR: f.dir, SAFE_CONFIG: 'fixture-only' };
  const denied = { VECNET_CREDENTIAL: 'fixture-only', VECNET_URL: 'fixture-only', OPENAI_API_KEY: 'fixture-only', ANTHROPIC_AUTH_TOKEN: 'fixture-only',
    ANTHROPIC_BASE_URL: 'fixture-only', OTHER_SECRET: 'fixture-only', lower_password: 'fixture-only' };
  const h = await f.launch({ env: { ...safe, ...denied } });
  const final = (await eventsUntil(h, finished)).find(finished); assert.ok(final?.type === 'turn_finished');
  assert.deepEqual(JSON.parse(final.finalText), { ...safe, ...Object.fromEntries(Object.keys(denied).map(k => [k, null])) });
});


test('I5 default test root uses the synthetic user persistent cache rather than a host-specific mount', async t => {
  if (process.env.ACP_TEST_ROOT) { t.skip('explicit root supplied'); return; }
  const f = await fixture(t);
  const expected = await acpTestParent();
  assert.ok(f.dir.startsWith(expected + '/'), 'default ACP fixtures must use the user persistent cache');
});


test('C1 completed inbound identity tracking has a finite connection budget and never evicts identities', async t => {
  const f = await fixture(t, 'request-budget'), h = await f.launch();
  const events = await eventsUntil(h, e => e.type === 'state' && e.state.kind === 'ended' || e.type === 'turn_finished');
  assert.equal(events.some(e => e.type === 'turn_finished'), false, 'request identity budget must close rather than evict and continue');
  await eventsUntil(h, e => e.type === 'process_exited');
  const responses = (await f.calls()).filter(c => typeof c.id === 'string' && c.id.startsWith('budget-'));
  assert.equal(responses.length, 4096);
  assert.ok(responses.every(c => c.error?.code === -32601));
});


// Each regression owns its outer sandbox independently of the allocator under
// test, so the RED run can clean a child even if fixture setup itself throws.
async function rootSandbox(t: TestContext) {
  const control = await realpath(process.env.ACP_FIX2_CONTROL_ROOT ?? homedir());
  assert.ok(control.startsWith('/') && control !== '/tmp' && !control.startsWith('/tmp/'));
  const dir = await mkdtemp(join(control, 'acp-fix2-root-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  return dir;
}
async function withRootEnv<T>(t: TestContext, root: string, explicit: boolean, run: () => Promise<T>): Promise<T> {
  const saved = { HOME: process.env.HOME, XDG_CACHE_HOME: process.env.XDG_CACHE_HOME, ACP_TEST_ROOT: process.env.ACP_TEST_ROOT };
  process.env.HOME = join(root, 'synthetic-home');
  process.env.XDG_CACHE_HOME = root;
  if (explicit) process.env.ACP_TEST_ROOT = join(root, 'override', 'missing'); else delete process.env.ACP_TEST_ROOT;
  try { return await run(); }
  finally { for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete process.env[key]; else process.env[key] = value; } }
}
for (const explicit of [false, true]) {
  test(`fix2 persistent symlink fixture launches from canonical child (${explicit ? 'override' : 'default'})`, async t => {
    const sandbox = await rootSandbox(t), target = join(sandbox, 'persistent-target'), link = join(sandbox, 'cache-link');
    await mkdir(target); await symlink(target, link);
    await withRootEnv(t, link, explicit, async () => {
      const f = await fixture(t);
      assert.equal(f.dir, await realpath(f.dir), 'fixture must supply canonical cwd/workspace paths');
      const h = await f.launch(); await eventsUntil(h, finished);
      assert.equal(h.ref.extra?.workspace, await realpath(String(h.ref.extra?.workspace)));
    });
  });
  test(`fix2 /tmp ancestor refused before mkdir (${explicit ? 'override' : 'default'})`, async t => {
    const sandbox = await rootSandbox(t), link = join(sandbox, 'cache-link');
    await symlink('/tmp', link);
    const forbiddenName = 'acp-fix2-never-create-' + sandbox.split('/').at(-1);
    const root = join(link, forbiddenName);
    const originalMkdir = fsPromises.mkdir;
    const attempted: string[] = [];
    const mock = t.mock.method(fsPromises, 'mkdir', async (...args: Parameters<typeof fsPromises.mkdir>) => {
      const path = String(args[0]);
      if (path === root || path.startsWith(root + '/') || path === '/tmp/' + forbiddenName || path.startsWith('/tmp/' + forbiddenName + '/')) {
        attempted.push(path); throw new Error('test intercepted forbidden mkdir before any /tmp artifact');
      }
      return Reflect.apply(originalMkdir, fsPromises, args);
    });
    syncBuiltinESMExports();
    try {
      await withRootEnv(t, root, explicit, async () => { await assert.rejects(fixture(t)); });
      assert.deepEqual(attempted, [], 'prospective canonical ancestry must be checked before any recursive mkdir');
      await assert.rejects(lstat('/tmp/' + forbiddenName), { code: 'ENOENT' });
    } finally { mock.mock.restore(); syncBuiltinESMExports(); }
  });
}
test('fix2 fixture cleanup owns only its unique child, preserving canonical parent and siblings', async t => {
  const sandbox = await rootSandbox(t), target = join(sandbox, 'persistent-target'), link = join(sandbox, 'cache-link');
  await mkdir(target); await symlink(target, link);
  const callbacks: Array<() => Promise<unknown> | unknown> = [];
  const scoped = { after(fn: () => Promise<unknown> | unknown) { callbacks.push(fn); } } as TestContext;
  await withRootEnv(t, target, false, async () => {
    const f = await fixture(scoped), parent = await realpath(join(f.dir, '..'));
    const sibling = join(parent, 'unrelated-sibling'); await mkdir(sibling); await writeFile(join(sibling, 'keep'), 'sibling');
    const marker = join(parent, 'keep-parent'); await writeFile(marker, 'parent');
    const h = await f.launch(); await eventsUntil(h, finished);
    for (const fn of callbacks.reverse()) await fn();
    await assert.rejects(lstat(f.dir), { code: 'ENOENT' });
    assert.equal(await readFile(marker, 'utf8'), 'parent'); assert.equal(await readFile(join(sibling, 'keep'), 'utf8'), 'sibling');
    assert.equal(await realpath(link), target);
  });
});
test('fix2 registers unique-child cleanup before a later awaited fixture initialization fails', async t => {
  const sandbox = await rootSandbox(t), callbacks: Array<() => Promise<unknown> | unknown> = [];
  const scoped = { after(fn: () => Promise<unknown> | unknown) { callbacks.push(fn); } } as TestContext;
  const mock = t.mock.method(plugin, 'create', async () => { throw new Error('synthetic later initialization failure'); });
  try {
    await withRootEnv(t, sandbox, false, async () => {
      await assert.rejects(fixture(scoped), /synthetic later initialization failure/);
      assert.ok(callbacks.length > 0, 'owned child cleanup must be registered before plugin.create can reject');
      for (const fn of callbacks.reverse()) await fn();
      assert.deepEqual(await readdir(join(sandbox, 'vecnet-launcher', 'acp-tests')), []);
    });
  } finally { mock.mock.restore(); }
});
