import { test } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { startCodexConnector } from '../src/codex/connector.ts';
import { startFakeCodexAppServer } from './fake-codex-appserver.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { KeyValue } from '../src/backend.ts';
import { harness } from './daemon-harness.ts';

// Every path below is private to the test: no real config, cache, Codex home,
// daemon socket or vecnet binary is ever named, and a `codex` on PATH is a trap.
const T = Date.parse('2026-11-06T00:00:00.000Z'), hour = 3_600_000;
const uuid = (n: number) => `00000000-0000-4000-8000-${n.toString(16).padStart(12, '0')}`;
type FileBinding = Record<string, unknown>;
type Connector = ReturnType<typeof startCodexConnector>;

async function eventually(fn: () => boolean, what: string, ms = 8000) {
  const end = Date.now() + ms;
  while (!fn()) {
    if (Date.now() > end) assert.fail(`timed out waiting for ${what}`);
    await delay(10);
  }
}

async function setup(t: TestContext, o: { threads?: { id: string; status?: unknown; approvalPolicy?: string }[]; maxBindings?: string; startAt?: number } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'vn-cc-'));
  const fake = await startFakeCodexAppServer({ dir: join(dir, 'fake') });
  const home = join(dir, 'fake', 'home');
  for (const th of o.threads ?? []) {
    fake.setThread({ id: th.id, status: th.status ?? { type: 'idle' }, ...(th.approvalPolicy ? { approvalPolicy: th.approvalPolicy } : {}) });
    fake.attachOther(th.id);
  }
  const bin = join(dir, 'bin'), trace = join(dir, 'trace.log'), trap = join(dir, 'codex-spawned');
  mkdirSync(bin, { mode: 0o700 });
  writeFileSync(trace, '');
  // A shell stand-in for the Go CLI: watch idles until killed; hooks claim nothing.
  writeFileSync(join(bin, 'vecnet'), `#!/bin/sh
printf '%s\\n' "$*" >> '${trace}'
case " $* " in
  *" watch "*) exec sleep 3600 ;;
  *" --release "*) echo '{"version":1,"released":true}' ;;
  *" hook "*) echo '{"version":1,"deliveries":[]}' ;;
  *) exit 2 ;;
esac
`, { mode: 0o700 });
  writeFileSync(join(bin, 'codex'), `#!/bin/sh\necho forbidden > '${trap}'\nexit 99\n`, { mode: 0o700 });
  for (const p of ['home', 'config', 'cache', 'runtime', 'codex-home-unused', 'bindings']) mkdirSync(join(dir, p), { mode: 0o700 });
  const bindingsPath = join(dir, 'bindings', 'bindings.json');
  const env: NodeJS.ProcessEnv = {
    HOME: join(dir, 'home'), PATH: `${bin}:/usr/bin:/bin`, VECNET_CONFIG: join(dir, 'config', 'config.toml'),
    XDG_CONFIG_HOME: join(dir, 'config'), XDG_CACHE_HOME: join(dir, 'cache'), XDG_RUNTIME_DIR: join(dir, 'runtime'),
    CODEX_HOME: join(dir, 'codex-home-unused'), VECNET_CODEX_BINDINGS: bindingsPath, VECNET_BIN: join(bin, 'vecnet'),
    ...(o.maxBindings ? { VECNET_CODEX_MAX_BINDINGS: o.maxBindings } : {}),
  };
  const map = new Map<string, string>();
  const kv: KeyValue = { get: k => map.get(k), set: (k, v) => { map.set(k, v); }, delete: k => { map.delete(k); }, keys: (p = '') => [...map.keys()].filter(k => k.startsWith(p)) };
  const clock = fakeClock(o.startAt ?? T), log = memoryLog();
  const connectors: Connector[] = [];
  const binding = (id: string, i: number, extra: FileBinding = {}): FileBinding => ({
    profile: `p${i}`, as: '', hub: 'https://hub.invalid', caller: `peer_${i}`, codex_home: home, thread_id: id,
    created_at: new Date(T + i * 1000).toISOString(), expires_at: new Date(T + 24 * hour).toISOString(), ...extra,
  });
  const writeBindings = (list: FileBinding[], mode = 0o600) => {
    writeFileSync(bindingsPath, JSON.stringify({ version: 1, bindings: list }, null, 2), { mode });
    chmodSync(bindingsPath, mode);
  };
  const start = () => { const c = startCodexConnector({ kv, clock, log, env }); connectors.push(c); return c; };
  const resumed = (id: string) => fake.requests.some(r => r.method === 'thread/resume' && (r.params as { threadId?: string }).threadId === id);
  const unsubscribed = (id: string) => fake.requests.some(r => r.method === 'thread/unsubscribe' && (r.params as { threadId?: string }).threadId === id);
  const subscribed = (id: string) => fake.subscribers(id).length === 1;
  const logged = (msg: string) => log.entries.filter(e => e.msg === msg);
  /** One 10 s reload of logical time, then a moment for its I/O to settle. */
  const reload = async () => { clock.advance(10_000); await delay(150); };
  t.after(async () => {
    for (const c of connectors) await c.stop();
    await fake.close();
    assert.equal(existsSync(trap), false, 'a codex binary was spawned');
    assert.equal(fake.overrides.length, 0);
    assert.deepEqual(fake.replies, []);
    rmSync(dir, { recursive: true, force: true });
  });
  return { dir, fake, home, env, kv, clock, log, bindingsPath, binding, writeBindings, start, resumed, unsubscribed, subscribed, logged, reload, trace };
}

test('connector starts one engine per binding and stops removed ones', async t => {
  const [a, b, c] = [uuid(1), uuid(2), uuid(3)];
  const s = await setup(t, { threads: [{ id: a }, { id: b }, { id: c }] });
  s.writeBindings([s.binding(a, 1), s.binding(b, 2), s.binding(c, 3)]);
  s.start();
  await eventually(() => [a, b, c].every(s.subscribed), 'all three bound threads subscribed');
  // One engine (one connection) per binding.
  assert.equal(s.fake.requests.filter(r => r.method === 'initialize').length, 3);
  const connOf = (id: string) => s.fake.subscribers(id)[0];
  const [ca, cc] = [connOf(a), connOf(c)];
  s.writeBindings([s.binding(a, 1), s.binding(c, 3)]);
  await s.reload();
  await eventually(() => !s.subscribed(b) && s.unsubscribed(b), 'removed binding unsubscribed');
  assert.ok(s.subscribed(a) && s.subscribed(c), 'kept bindings stay subscribed');
  // The kept engines were not restarted: same connections, no new initialize.
  assert.equal(connOf(a), ca); assert.equal(connOf(c), cc);
  assert.equal(s.fake.requests.filter(r => r.method === 'initialize').length, 3);
  // A renewal (expires_at only) keeps its engine too.
  s.writeBindings([s.binding(a, 1, { expires_at: new Date(T + 48 * hour).toISOString() }), s.binding(c, 3)]);
  await s.reload();
  assert.equal(connOf(a), ca);
  assert.equal(s.fake.requests.filter(r => r.method === 'initialize').length, 3);
  // A fresh binding of the same thread (new created_at) is a new engine.
  s.writeBindings([s.binding(a, 1), s.binding(c, 9)]);
  await s.reload();
  await eventually(() => s.subscribed(c) && connOf(c) !== cc, 're-bound thread subscribed by a new engine');
  assert.equal(s.fake.requests.filter(r => r.method === 'initialize').length, 4);
});

test('limit groups symlinked codex homes together', async t => {
  const ids = Array.from({ length: 9 }, (_, i) => uuid(10 + i));
  const s = await setup(t, { threads: ids.map(id => ({ id })) });
  const alias = join(s.dir, 'alias-home');
  symlinkSync(s.home, alias);
  // The third-oldest binding names the home through a symlink; the ninth (by
  // created_at) is the one over the default limit of 8 for that home.
  s.writeBindings(ids.map((id, i) => s.binding(id, i + 1, i === 2 ? { codex_home: alias } : {})).reverse());
  s.start();
  await eventually(() => ids.slice(0, 8).every(s.subscribed), 'oldest eight subscribed');
  await s.reload();
  assert.ok(s.subscribed(ids[2]), 'the symlinked binding counts within the limit');
  assert.equal(s.resumed(ids[8]), false, 'the ninth binding is never subscribed');
  assert.deepEqual(s.fake.subscribers(ids[8]), []);
  assert.deepEqual(s.logged('codex_binding_over_limit').map(e => e.meta?.threadId), [ids[8]]);
});

test('absent bindings file disables quietly; 0644 file is refused', async t => {
  const id = uuid(20);
  const s = await setup(t, { threads: [{ id }] });
  s.start();
  await delay(100);
  await s.reload(); await s.reload();
  assert.equal(s.logged('codex_connector_disabled').length, 1, 'disabled is logged once');
  assert.equal(s.log.entries.filter(e => e.level === 'error').length, 0);
  assert.deepEqual(s.fake.requests, [], 'no engine runs without a bindings file');
  s.writeBindings([s.binding(id, 1)], 0o644);
  await s.reload(); await s.reload();
  assert.equal(s.logged('codex_bindings_refused').length, 1, 'refusal is logged once');
  assert.deepEqual(s.fake.requests, [], 'a group- or world-readable file starts nothing');
  chmodSync(s.bindingsPath, 0o600);
  await s.reload();
  await eventually(() => s.subscribed(id), 'a private file is accepted');
  // Becoming readable again stops the engine.
  chmodSync(s.bindingsPath, 0o640);
  await s.reload();
  await eventually(() => !s.subscribed(id) && s.unsubscribed(id), 'refused again: engine stopped');
  assert.equal(s.logged('codex_bindings_refused').length, 2);
});

test('daemon start and stop drive the connector', async t => {
  const id = uuid(30);
  const s = await setup(t, { threads: [{ id }] });
  // No bindings file: the daemon starts, the connector logs once and starts no engine.
  const quiet = await harness(t, { env: s.env });
  await eventually(() => quiet.log.entries.some(e => e.msg === 'codex_connector_disabled'), 'connector disabled at daemon start');
  assert.deepEqual(s.fake.requests, []);
  await quiet.daemon.stop();
  // With a binding, the daemon's connector subscribes; stop() unsubscribes it.
  s.writeBindings([s.binding(id, 1, { expires_at: new Date(Date.parse('2026-11-05T00:00:00.000Z') + 24 * hour).toISOString() })]);
  const h = await harness(t, { env: s.env });
  await eventually(() => s.subscribed(id), 'daemon connector subscribed the bound thread');
  await h.daemon.stop();
  assert.equal(s.subscribed(id), false);
  assert.ok(s.unsubscribed(id), 'daemon stop sends thread/unsubscribe');
  const n = s.fake.requests.length;
  await delay(100);
  assert.equal(s.fake.requests.length, n, 'nothing is sent after the daemon stops');
  // The engine's ledger rows live in the daemon's own ledger, in the codex namespace.
  assert.deepEqual(h.inspect(l => l.kv('codex').keys('inflight:')), []);
});

test('launcher bindings live until expiresAt and are never written to the file', async t => {
  const [explicit, launched, removed, unloaded] = [uuid(40), uuid(41), uuid(42), uuid(43)];
  const s = await setup(t, { threads: [{ id: explicit }, { id: launched, approvalPolicy: 'on-request' }, { id: removed }, { id: unloaded, status: { type: 'notLoaded' } }] });
  s.writeBindings([s.binding(explicit, 1)]);
  const before = readFileSync(s.bindingsPath), listing = readdirSync(join(s.dir, 'bindings')).sort();
  const c = s.start();
  await eventually(() => s.subscribed(explicit), 'explicit binding subscribed');
  let ensured = 0;
  const lb = (threadId: string, ms: number) => ({ profile: 'job', hub: 'https://hub.invalid', caller: 'peer_job', codexHome: s.home, threadId,
    createdAt: s.clock.now(), expiresAt: s.clock.now() + ms, attended: false, ensureLoaded: async () => { ensured++; return false; } });
  c.addLauncherBinding(lb(launched, hour));
  c.addLauncherBinding(lb(removed, hour));
  c.addLauncherBinding(lb(unloaded, hour));
  // Launcher bindings are attended: an asking policy keeps the subscription.
  await eventually(() => s.subscribed(launched) && s.subscribed(removed), 'launcher bindings subscribed');
  // A notLoaded launcher thread is the launcher's to load, never the connector's.
  await eventually(() => ensured === 1, 'ensureLoaded called for the notLoaded launcher thread');
  assert.equal(s.resumed(unloaded), false);
  // Reloads of the file never drop or write launcher bindings.
  await s.reload(); await s.reload();
  assert.ok(s.subscribed(launched) && s.subscribed(removed));
  assert.deepEqual(readFileSync(s.bindingsPath), before);
  assert.deepEqual(readdirSync(join(s.dir, 'bindings')).sort(), listing);
  c.removeLauncherBinding(removed);
  await eventually(() => !s.subscribed(removed) && s.unsubscribed(removed), 'removed launcher binding unsubscribed');
  assert.ok(s.subscribed(launched));
  // At expiresAt the launcher binding stops; the explicit one is untouched.
  s.clock.advance(hour);
  await eventually(() => !s.subscribed(launched) && s.unsubscribed(launched), 'expired launcher binding unsubscribed');
  assert.ok(s.subscribed(explicit));
  assert.deepEqual(readFileSync(s.bindingsPath), before);
  assert.deepEqual(readdirSync(join(s.dir, 'bindings')).sort(), listing);
  // An already-expired launcher binding never starts.
  const late = uuid(44);
  s.fake.setThread({ id: late, status: { type: 'idle' } }); s.fake.attachOther(late);
  c.addLauncherBinding({ ...lb(late, 0), expiresAt: s.clock.now() - 1 });
  await delay(150);
  assert.equal(s.resumed(late), false);
});

test('connector subscribes at most the limit, oldest first', async t => {
  const ids = Array.from({ length: 10 }, (_, i) => uuid(50 + i)), job = uuid(70);
  const s = await setup(t, { threads: [...ids, job].map(id => ({ id })) });
  // Written newest first: order comes from created_at, not file order.
  s.writeBindings(ids.map((id, i) => s.binding(id, i + 1)).reverse());
  const c = s.start();
  c.addLauncherBinding({ profile: 'job', hub: 'https://hub.invalid', caller: 'peer_job', codexHome: s.home, threadId: job,
    createdAt: T - hour, expiresAt: T + hour, attended: false, ensureLoaded: async () => false });
  await eventually(() => ids.slice(0, 8).every(s.subscribed), 'oldest eight subscribed');
  await eventually(() => s.subscribed(job), 'launcher binding subscribed, not counted');
  await s.reload();
  assert.equal(s.resumed(ids[8]), false); assert.equal(s.resumed(ids[9]), false);
  assert.deepEqual(s.logged('codex_binding_over_limit').map(e => e.meta?.threadId).sort(), [ids[8], ids[9]].sort());
  // Unbinding one subscribes the ninth within one reload; the tenth stays out.
  s.writeBindings(ids.filter((_, i) => i !== 3).map(id => s.binding(id, ids.indexOf(id) + 1)));
  await s.reload();
  await eventually(() => s.subscribed(ids[8]) && !s.subscribed(ids[3]), 'ninth subscribed after an unbind');
  await s.reload();
  assert.equal(s.resumed(ids[9]), false);
  // Expired bindings do not hold a place in the limit.
  s.writeBindings(ids.filter((_, i) => i !== 3).map(id => s.binding(id, ids.indexOf(id) + 1, id === ids[0] ? { expires_at: new Date(T - 1).toISOString() } : {})));
  await s.reload();
  await eventually(() => s.subscribed(ids[9]) && !s.subscribed(ids[0]), 'tenth subscribed once an older binding expired');
  // VECNET_CODEX_MAX_BINDINGS is honoured.
  const small = await setup(t, { threads: ids.slice(0, 3).map(id => ({ id })), maxBindings: '2' });
  small.writeBindings(ids.slice(0, 3).map((id, i) => small.binding(id, i + 1)));
  small.start();
  await eventually(() => small.subscribed(ids[0]) && small.subscribed(ids[1]), 'two subscribed under a limit of 2');
  await small.reload();
  assert.equal(small.resumed(ids[2]), false);
});

test('connector stop unsubscribes every engine', async t => {
  const ids = [uuid(80), uuid(81), uuid(82)], job = uuid(83);
  const s = await setup(t, { threads: [...ids, job].map(id => ({ id })) });
  s.writeBindings(ids.map((id, i) => s.binding(id, i + 1)));
  const c = s.start();
  c.addLauncherBinding({ profile: 'job', hub: 'https://hub.invalid', caller: 'peer_job', codexHome: s.home, threadId: job,
    createdAt: T, expiresAt: T + hour, attended: false, ensureLoaded: async () => false });
  await eventually(() => [...ids, job].every(s.subscribed), 'all subscribed');
  await c.stop();
  for (const id of [...ids, job]) {
    assert.deepEqual(s.fake.subscribers(id), [], `${id} still subscribed`);
    assert.ok(s.unsubscribed(id), `${id} got no thread/unsubscribe`);
  }
  const n = s.fake.requests.length;
  await s.reload();
  assert.equal(s.fake.requests.length, n, 'no reload or engine runs after stop');
  // Every watch child was started and is gone with its engine.
  const watches = readFileSync(s.trace, 'utf8').split('\n').filter(l => / watch --json$/.test(l));
  assert.equal(watches.length, 4);
});

// The live smoke is coordinator-run on xsm. Only its skip paths run here,
// against stubs: no real codex, daemon socket or Codex home is reachable.
const smokeScript = fileURLToPath(new URL('../../../scripts/codex-appserver-smoke.mjs', import.meta.url));
// Asynchronous: the fake app-server answers from this process's event loop.
function smoke(env: NodeJS.ProcessEnv): Promise<{ status: number | null; stdout: string; stderr: string }> {
  return new Promise(resolve => {
    const child = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', smokeScript], { env, stdio: ['ignore', 'pipe', 'pipe'], timeout: 20_000 });
    let stdout = '', stderr = '';
    child.stdout.setEncoding('utf8').on('data', d => { stdout += d; });
    child.stderr.setEncoding('utf8').on('data', d => { stderr += d; });
    child.once('close', status => resolve({ status, stdout, stderr }));
  });
}
async function smokeFixture(t: TestContext, o: { codex?: boolean; tmux?: boolean } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'vn-cas-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = join(dir, 'bin'), trap = join(dir, 'codex-ran');
  mkdirSync(bin); mkdirSync(join(dir, 'home')); mkdirSync(join(dir, 'codex-home'));
  symlinkSync(process.execPath, join(bin, 'node'));
  if (o.codex !== false) writeFileSync(join(bin, 'codex'), `#!/bin/sh\necho "$*" >> '${trap}'\nexit 99\n`, { mode: 0o755 });
  if (o.tmux !== false) writeFileSync(join(bin, 'tmux'), `#!/bin/sh\necho "tmux $*" >> '${trap}'\nexit 99\n`, { mode: 0o755 });
  const env: NodeJS.ProcessEnv = { PATH: bin, HOME: join(dir, 'home'), CODEX_HOME: join(dir, 'codex-home'), VECNET_CODEX_APPSERVER_SMOKE: '1',
    VECNET_CONFIG: join(dir, 'do-not-touch.toml'), XDG_CONFIG_HOME: join(dir, 'do-not-touch-xdg'), XDG_CACHE_HOME: join(dir, 'do-not-touch-cache') };
  return { dir, env, trap, bin };
}

test('smoke skips without opt-in, without codex, and without an allowlisted daemon', async t => {
  const f = await smokeFixture(t);
  const optOut = await smoke({ ...f.env, VECNET_CODEX_APPSERVER_SMOKE: undefined });
  assert.equal(optOut.status, 0, optOut.stderr);
  assert.match(optOut.stdout, /^skipped: /);
  const noCodex = await smokeFixture(t, { codex: false });
  const r1 = await smoke(noCodex.env);
  assert.equal(r1.status, 0, r1.stderr);
  assert.match(r1.stdout, /^skipped: codex is not on PATH/);
  // A stub codex but no daemon socket under the private CODEX_HOME.
  const r2 = await smoke(f.env);
  assert.equal(r2.status, 0, r2.stderr);
  assert.match(r2.stdout, /^skipped: no Codex app-server daemon/);
  // A daemon whose version is not allowlisted.
  const fake = await startFakeCodexAppServer({ dir: join(f.dir, 'fake') });
  t.after(() => fake.close());
  fake.setVersion('0.0.1');
  const r3 = await smoke({ ...f.env, CODEX_HOME: join(f.dir, 'fake', 'home') });
  assert.equal(r3.status, 0, r3.stderr);
  assert.match(r3.stdout, /^skipped: .*not allowlisted/);
  // Preflight never runs codex or tmux and never writes outside its checks.
  assert.equal(existsSync(f.trap), false);
  assert.equal(existsSync(noCodex.trap), false);
  assert.deepEqual(fake.requests.map(r => r.method), ['initialize']);
});

const handshakeOnly = (fake: { requests: { method: string }[] }) => fake.requests.map(r => r.method).filter(m => m !== 'initialized');

test('smoke refuses a CODEX_HOME that is not a directory and a busy state directory', async t => {
  const f = await smokeFixture(t);
  const file = join(f.dir, 'not-a-dir');
  writeFileSync(file, '');
  const r1 = await smoke({ ...f.env, CODEX_HOME: file });
  assert.equal(r1.status, 2, r1.stdout);
  assert.match(r1.stderr, /^refused: /);
  const fake = await startFakeCodexAppServer({ dir: join(f.dir, 'fake') });
  t.after(() => fake.close());
  const state = join(f.dir, 'state');
  mkdirSync(join(state, 'leftover'), { recursive: true });
  const r2 = await smoke({ ...f.env, CODEX_HOME: join(f.dir, 'fake', 'home'), VECNET_CODEX_APPSERVER_SMOKE_DIR: state });
  assert.equal(r2.status, 2, r2.stdout);
  assert.match(r2.stderr, /^refused: .*leftover/);
  assert.equal(existsSync(f.trap), false);
  // A refusal after the version check still sent nothing but the handshake.
  assert.deepEqual(handshakeOnly(fake), ['initialize']);
});
