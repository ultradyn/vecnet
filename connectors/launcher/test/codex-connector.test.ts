import { test } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, statSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { CODEX_RELOAD_MS, codexBindingsPath, startCodexConnector } from '../src/codex/connector.ts';
import type { CodexRpc } from '../src/codex/rpc.ts';
import { openCodexRpc } from '../src/codex/rpc.ts';
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

async function setup(t: TestContext, o: { threads?: { id: string; status?: unknown; approvalPolicy?: string }[]; maxBindings?: string; startAt?: number; unsubscribeDelayMs?: number; source?: string; blockHooks?: boolean; holdReadyReport?: boolean; onResume?: () => Promise<void> } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'vn-cc-'));
  const fake = await startFakeCodexAppServer({ dir: join(dir, 'fake'), unsubscribeDelayMs: o.unsubscribeDelayMs });
  const home = join(dir, 'fake', 'home');
  for (const th of o.threads ?? []) {
    fake.setThread({ id: th.id, status: th.status ?? { type: 'idle' }, ...(th.approvalPolicy ? { approvalPolicy: th.approvalPolicy } : {}) });
    fake.attachOther(th.id);
  }
  const bin = join(dir, 'bin'), trace = join(dir, 'trace.log'), trap = join(dir, 'codex-spawned');
  mkdirSync(bin, { mode: 0o700 });
  writeFileSync(trace, '');
  const configTrace = join(dir, 'config-trace.log'); writeFileSync(configTrace, '');
  const reportTrace = join(dir, 'report-trace.log'); writeFileSync(reportTrace, '');
  const reports = () => readFileSync(reportTrace, 'utf8').trim().split('\n').filter(Boolean).map(line => JSON.parse(line));
  const reportCaptured = join(dir, 'report-captured'), reportGate = join(dir, 'report-gate');
  if (o.holdReadyReport) execFileSync('mkfifo', [reportGate]);
  // Hold the first ready report after its body is captured, before the CLI exits.
  // stop() must cancel this real child before sending the final offline report.
  const holdReport = o.holdReadyReport ? `
    case "$(tail -n 1 '${reportTrace}')" in
      *'"delivery":"ready"'*)
        if mkdir '${dir}/report-held' 2>/dev/null; then
          : > '${reportCaptured}'
          read released < '${reportGate}'
        fi ;;
    esac;` : '';
  // Buffer each bounded metadata body through EOF and append its newline in the
  // same printf: cancellation must not strand a body for the next report to join.
  // A shell stand-in for the Go CLI: watch idles until killed; hooks claim nothing.
  writeFileSync(join(bin, 'vecnet'), `#!/bin/sh
printf '%s\\n' "$*" >> '${trace}'
printf '%s\\n' "$VECNET_CONFIG" >> '${configTrace}'
case " $* " in
  *" codex report-status "*) report=$(cat); printf '%s\\n' "$report" >> '${reportTrace}'; ${holdReport} exit 0 ;;
  *" codex verify "*) echo '{"verified":true}'; exit 0 ;;
  *" codex guard "*) echo '{"ready":true}'; read outcome; exit 0 ;;
  *" watch "*) exec sleep 3600 ;;
  *" --release "*) echo '{"version":1,"released":0}' ;;
  *" hook "*) ${o.blockHooks ? 'exec sleep 3600' : `echo '{"version":1,"deliveries":[]}'`} ;;
  *) exit 2 ;;
esac
`, { mode: 0o700 });
  writeFileSync(join(bin, 'codex'), `#!/bin/sh\necho forbidden > '${trap}'\nexit 99\n`, { mode: 0o700 });
  for (const p of ['home', 'config', 'cache', 'runtime', 'codex-home-unused', 'bindings']) mkdirSync(join(dir, p), { mode: 0o700 });
  const bindingsDir = join(dir, 'home', '.local', 'state', 'vecnet', 'codex');
  mkdirSync(bindingsDir, { recursive: true, mode: 0o700 });
  const bindingsPath = join(bindingsDir, 'bindings.json');
  const env: NodeJS.ProcessEnv = {
    HOME: join(dir, 'home'), PATH: `${bin}:/usr/bin:/bin`, VECNET_CONFIG: join(dir, 'config', 'config.toml'),
    XDG_CONFIG_HOME: join(dir, 'config'), XDG_CACHE_HOME: join(dir, 'cache'), XDG_RUNTIME_DIR: join(dir, 'runtime'),
    CODEX_HOME: join(dir, 'codex-home-unused'), VECNET_CODEX_BINDINGS: bindingsPath, VECNET_BIN: join(bin, 'vecnet'),
    ...(o.maxBindings ? { VECNET_CODEX_MAX_BINDINGS: o.maxBindings } : {}),
  };
  writeFileSync(env.VECNET_CONFIG!, '', { mode: 0o600 });
  const map = new Map<string, string>();
  const kv: KeyValue = { get: k => map.get(k), set: (k, v) => { map.set(k, v); }, delete: k => { map.delete(k); }, keys: (p = '') => [...map.keys()].filter(k => k.startsWith(p)) };
  const clock = fakeClock(o.startAt ?? T), log = memoryLog();
  // Count the connector's reload timers. It arms the next one only after a
  // reload (including engine shutdowns) has finished, so a new registration
  // is the completion barrier. No engine timer uses CODEX_RELOAD_MS.
  let reloadsArmed = 0, reloadsPending = 0, reloadsFired = 0;
  const baseTimer = clock.timer;
  clock.timer = (ms, fn) => {
    if (ms !== CODEX_RELOAD_MS) return baseTimer(ms, fn);
    reloadsArmed++; reloadsPending++;
    let live = true;
    const settle = () => { if (live) { live = false; reloadsPending--; } };
    const alarm = baseTimer(ms, () => { settle(); reloadsFired++; fn(); });
    return { cancel() { settle(); alarm.cancel(); } };
  };
  const connectors: Connector[] = [];
  let live = 0;
  const binding = (id: string, i: number, extra: FileBinding = {}): FileBinding => ({
    config_path: env.VECNET_CONFIG, generation: i.toString(16).padStart(32, '0'),
    profile: `p${i}`, as: '', hub: 'https://hub.invalid', caller: `peer_${i}`, codex_home: home, thread_id: id,
    created_at: new Date(T + i * 1000).toISOString(), expires_at: new Date(T + 24 * hour).toISOString(), ...extra,
  });
  const writeBindings = (list: FileBinding[], mode = 0o600) => {
    writeFileSync(bindingsPath, JSON.stringify({ version: 1, bindings: list }, null, 2), { mode });
    chmodSync(bindingsPath, mode);
  };
  const start = (extraEnv: NodeJS.ProcessEnv = {}) => {
    const c = startCodexConnector({ kv, clock, log, env: { ...env, ...extraEnv }, openRpc: async options => {
      const rpc = await openCodexRpc(options);
      return { ...rpc, async call<T>(method: Parameters<CodexRpc['call']>[0], params: unknown, timeout: number): Promise<T> {
        const response = await rpc.call<any>(method, params, timeout);
        if (method === 'thread/read') response.thread.source = o.source ?? 'cli';
        if (method === 'thread/resume' && o.onResume) await o.onResume();
        return response;
      } };
    } }), stop = c.stop;
    let stopped = false;
    c.stop = () => { if (!stopped) { stopped = true; live--; } return stop(); };
    live++; connectors.push(c); return c;
  };
  const resumed = (id: string) => fake.requests.some(r => r.method === 'thread/resume' && (r.params as { threadId?: string }).threadId === id);
  const unsubscribed = (id: string) => fake.requests.some(r => r.method === 'thread/unsubscribe' && (r.params as { threadId?: string }).threadId === id);
  const subscribed = (id: string) => fake.subscribers(id).length === 1;
  const logged = (msg: string) => log.entries.filter(e => e.msg === msg);
  /** The first reload has run: its successor timer is armed. */
  const ready = () => eventually(() => live === 0 || reloadsPending > 0, 'connector reload timer armed');
  /**
   * One 10 s reload of logical time, waited for by completion, not by a sleep:
   * every reload that fires must re-arm its timer (it does so only after
   * retiring and starting engines) before the clock may move on.
   */
  const reload = async () => {
    await ready();
    const armed = reloadsArmed, fired = reloadsFired;
    clock.advance(CODEX_RELOAD_MS);
    const ran = reloadsFired - fired;
    await eventually(() => live === 0 || reloadsArmed >= armed + ran, 'connector reload completed and re-armed');
  };
  t.after(async () => {
    for (const c of connectors) await c.stop();
    await fake.close();
    assert.equal(existsSync(trap), false, 'a codex binary was spawned');
    assert.equal(fake.overrides.length, 0);
    assert.deepEqual(fake.replies, []);
    rmSync(dir, { recursive: true, force: true });
  });
  const runtimeDiagnostics = () => ({ now: clock.now(), reloadsArmed, reloadsPending, reloadsFired,
    trace: readFileSync(trace, 'utf8'), reports: readFileSync(reportTrace, 'utf8'), requests: fake.requests, log: log.entries,
    readiness: existsSync(bindingsPath + '.status.json') ? readFileSync(bindingsPath + '.status.json', 'utf8') : undefined });
  // Server membership precedes the client's resume/recovery/watch completion.
  // Sample runtime status once, after that client's verified watch has started.
  const reloadForRuntime = async (profile: string) => {
    try {
      await eventually(() => readFileSync(trace, 'utf8').split('\n').includes(`--net ${profile} watch --json`), `verified client watch for ${profile}`);
      await reload();
    } catch (error) { t.diagnostic(JSON.stringify(runtimeDiagnostics())); throw error; }
  };
  return { dir, fake, home, env, kv, clock, log, bindingsPath, binding, writeBindings, start, resumed, unsubscribed, subscribed, logged, reload, ready, trace, configTrace, reportTrace, reports, reportCaptured, runtimeDiagnostics, reloadForRuntime };
}

test('only the singleton reports bound runtime status and shutdown offline, never removed owners', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  await t.test('runtime report capture preserves complete records when shutdown interrupts a reporting child', async t => {
    const id = uuid(599), s = await setup(t, { threads: [{ id }], holdReadyReport: true });
    s.writeBindings([s.binding(id, 1)]);
    const c = s.start();
    await eventually(() => s.subscribed(id), 'bound session ready');
    await s.reloadForRuntime('p1');
    await eventually(() => existsSync(s.reportCaptured), 'ready report captured and child held');
    const duringRaw = readFileSync(s.reportTrace, 'utf8');
    const during = s.reports();
    await c.stop();
    assert.deepEqual(await c.stopped, { reason: 'stopped' });
    assert.deepEqual(s.reports(), [
      { state: 'offline', delivery: 'unavailable', reason: 'connecting' },
      { state: 'idle', delivery: 'ready', version: '0.166.0' },
      { state: 'offline', delivery: 'unavailable', reason: 'stopped', version: '0.166.0' },
    ]);
    assert.deepEqual(during, [
      { state: 'offline', delivery: 'unavailable', reason: 'connecting' },
      { state: 'idle', delivery: 'ready', version: '0.166.0' },
    ]);
    assert.ok(duringRaw.endsWith('\n'), 'the live ready report is already newline-framed');
    const lines = readFileSync(s.reportTrace, 'utf8').split('\n');
    assert.equal(lines.length, 4, 'three complete newline-framed reports');
    assert.equal(lines.at(-1), '', 'the final offline report is newline-framed');
  });

  await t.test('runtime report sampling waits for client readiness before advancing the clock', async t => {
    let releaseResume!: () => void, resumeReceived = false;
    const held = new Promise<void>(resolve => { releaseResume = resolve; });
    const id = uuid(598), s = await setup(t, { threads: [{ id }], onResume: async () => { resumeReceived = true; await held; } });
    try {
      s.writeBindings([s.binding(id, 1)]);
      const c = s.start();
      await eventually(() => resumeReceived && s.subscribed(id), 'server subscribed before client resume return');
      await eventually(() => s.reports().length === 1, 'initial connecting report captured');
      const before = s.clock.now(), observation = s.reloadForRuntime('p1');
      try {
        await s.ready();
        assert.equal(s.clock.now(), before, 'server subscription alone must not advance the runtime sampling clock');
      } finally { releaseResume(); await observation; }
      assert.equal(s.clock.now(), before + CODEX_RELOAD_MS, 'one reload after client readiness');
      await eventually(() => s.reports().some(r => r.state === 'idle' && r.delivery === 'ready'), 'ready status report');
      assert.deepEqual(s.reports(), [
        { state: 'offline', delivery: 'unavailable', reason: 'connecting' },
        { state: 'idle', delivery: 'ready', version: '0.166.0' },
      ]);
      await c.stop();
      assert.deepEqual(await c.stopped, { reason: 'stopped' });
      assert.deepEqual(s.reports().at(-1), { state: 'offline', delivery: 'unavailable', reason: 'stopped', version: '0.166.0' });
    } catch (error) { t.diagnostic(JSON.stringify(s.runtimeDiagnostics())); throw error; }
    finally { releaseResume(); }
  });

  const id = uuid(600), s = await setup(t, { threads: [{ id }] });
  const reports = s.reports;
  s.writeBindings([s.binding(id, 1)]);
  const first = s.start();
  await eventually(() => s.subscribed(id), 'bound session ready');
  await s.reloadForRuntime('p1');
  try { await eventually(() => reports().some(r => r.state === 'idle' && r.delivery === 'ready'), 'ready status report'); }
  catch (error) { t.diagnostic(JSON.stringify(s.runtimeDiagnostics())); throw error; }
  assert.deepEqual(reports().at(-1), { state: 'idle', delivery: 'ready', version: '0.166.0' });
  const duplicate = s.start();
  await eventually(() => s.logged('codex_connector_duplicate').length > 0, 'duplicate reporter excluded');
  const before = reports().length;
  await duplicate.stop(); assert.equal(reports().length, before);
  s.writeBindings([]); await s.reload();
  assert.equal(reports().length, before, 'removed owner cannot publish offline or heartbeat');
  await first.stop(); assert.deepEqual(await first.stopped, { reason: 'stopped' }); assert.equal(reports().length, before);

  s.writeBindings([s.binding(id, 2)]);
  const next = s.start(); await eventually(() => s.subscribed(id), 'replacement ready');
  await s.reloadForRuntime('p2'); await next.stop();
  assert.equal(reports().at(-1).state, 'offline'); assert.equal(reports().at(-1).reason, 'stopped');
  assert.match(readFileSync(s.trace, 'utf8'), /codex report-status .*--expect-generation 00000000000000000000000000000002/);
});

test('connector starts one engine per binding and stops removed ones', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
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

test('reload waits for a slow engine shutdown before the next reload', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  // Retiring an engine waits for thread/unsubscribe. With that answer slower
  // than any fixed settling sleep, a removal, a renewal and a re-bind must
  // still each take effect at their own reload.
  const [a, b, c] = [uuid(5), uuid(6), uuid(7)];
  const s = await setup(t, { threads: [{ id: a }, { id: b }, { id: c }], unsubscribeDelayMs: 400 });
  s.writeBindings([s.binding(a, 1), s.binding(b, 2), s.binding(c, 3)]);
  s.start();
  await eventually(() => [a, b, c].every(s.subscribed), 'all three bound threads subscribed');
  const connOf = (id: string) => s.fake.subscribers(id)[0];
  const cc = connOf(c);
  s.writeBindings([s.binding(a, 1), s.binding(c, 3)]);
  const started = Date.now();
  await s.reload();
  assert.ok(Date.now() - started >= 300, 'the reload waited for the slow unsubscribe');
  assert.ok(!s.subscribed(b) && s.unsubscribed(b), 'removed binding unsubscribed by the end of its reload');
  s.writeBindings([s.binding(a, 1, { expires_at: new Date(T + 48 * hour).toISOString() }), s.binding(c, 3)]);
  await s.reload();
  s.writeBindings([s.binding(a, 1), s.binding(c, 9)]);
  await s.reload();
  try { await eventually(() => s.subscribed(c) && connOf(c) !== cc, 're-bound thread subscribed by a new engine'); }
  catch (error) { t.diagnostic(JSON.stringify({ log: s.log.entries, trace: readFileSync(s.trace, 'utf8') })); throw error; }
  assert.equal(s.fake.requests.filter(r => r.method === 'initialize').length, 4);
});

test('reload releases an interrupted claim before replacing its owner', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(8);
  const s = await setup(t, { threads: [{ id }], blockHooks: true });
  s.writeBindings([s.binding(id, 1)]);
  s.start();
  await eventually(() => s.subscribed(id) && / watch --json\n/.test(readFileSync(s.trace, 'utf8')), 'verified engine started');
  const previous = s.fake.subscribers(id)[0];
  s.clock.advance(2500);
  await eventually(() => readFileSync(s.trace, 'utf8').includes('--event app-server'), 'hook blocked with a durable claim intent');
  assert.equal(s.kv.keys('claiming:').length, 1);
  s.writeBindings([s.binding(id, 2)]);
  await s.reload();
  assert.deepEqual(s.kv.keys('claiming:'), [], 'shutdown released the interrupted claim');
  await eventually(() => s.subscribed(id) && s.fake.subscribers(id)[0] !== previous, 'replacement owner subscribed');
  assert.deepEqual(s.logged('codex_claim_release_failed'), []);
  assert.deepEqual(s.logged('codex_recovery_quarantined'), []);
});

test('limit groups symlinked codex homes together', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
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

test('absent bindings file disables quietly; 0644 file is refused', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(20);
  const s = await setup(t, { threads: [{ id }] });
  s.start();
  await s.ready();
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

test('a FIFO at the bindings path is refused without blocking', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(25);
  const s = await setup(t, { threads: [{ id }] });
  execFileSync('mkfifo', ['-m', '600', s.bindingsPath]);
  s.start();
  await s.ready();
  await s.reload();
  assert.deepEqual(s.logged('codex_bindings_refused').map(e => e.meta), [{ reason: 'not_file' }]);
  assert.deepEqual(s.fake.requests, [], 'a FIFO starts nothing');
});

test('daemon start and stop drive the connector', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(30);
  const s = await setup(t, { threads: [{ id }] });
  // No bindings file: the daemon starts, the connector logs once and starts no engine.
  const quiet = await harness(t, { env: s.env });
  await eventually(() => quiet.log.entries.some(e => e.msg === 'codex_connector_disabled'), 'connector disabled at daemon start');
  assert.deepEqual(s.fake.requests, []);
  await quiet.daemon.stop();
  // With a binding the daemon inspects the exact thread. This raw fake lacks
  // root source metadata, so it must remain inactive until stop().
  s.writeBindings([s.binding(id, 1, { created_at: '2026-11-04T00:00:00.000Z', expires_at: '2026-11-06T00:00:00.000Z' })]);
  const h = await harness(t, { env: s.env });
  await eventually(() => s.fake.requests.some(r => r.method === 'thread/read'), 'daemon connector inspects bound thread');
  await h.daemon.stop();
  assert.equal(s.subscribed(id), false);
  assert.equal(s.resumed(id), false, 'a host without verified root source stays inactive');
  const n = s.fake.requests.length;
  await delay(100);
  assert.equal(s.fake.requests.length, n, 'nothing is sent after the daemon stops');
  // The engine's ledger rows live in the daemon's own ledger, in the codex namespace.
  assert.deepEqual(h.inspect(l => l.kv('codex').keys('inflight:')), []);
});

test('memory-only launcher registration cannot authorize a thread or bypass recipient ownership', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const [explicit, job] = [uuid(40), uuid(41)];
  const s = await setup(t, { threads: [{ id: explicit }, { id: job }] });
  s.writeBindings([s.binding(explicit, 1)]);
  const before = readFileSync(s.bindingsPath);
  const c = s.start();
  await s.ready();
  let ensured = 0;
  c.addLauncherBinding({ configPath: s.env.VECNET_CONFIG, generation: 'a'.repeat(32),
    profile: 'alias', hub: 'https://HUB.invalid:443/', caller: 'peer_1', codexHome: s.home, threadId: job,
    createdAt: T, expiresAt: T + hour, attended: false, ensureLoaded: async () => { ensured++; return true; } });
  await s.reload();
  assert.equal(s.resumed(job), false);
  assert.equal(ensured, 0);
  assert.deepEqual(readFileSync(s.bindingsPath), before);
  assert.ok(s.logged('codex_launcher_binding_unauthorized').length);
});

test('connector subscribes at most the limit, oldest first', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const ids = Array.from({ length: 10 }, (_, i) => uuid(50 + i)), job = uuid(70);
  const s = await setup(t, { threads: [...ids, job].map(id => ({ id })) });
  // Written newest first: order comes from created_at, not file order.
  s.writeBindings(ids.map((id, i) => s.binding(id, i + 1)).reverse());
  const c = s.start();
  c.addLauncherBinding({ profile: 'job', hub: 'https://hub.invalid', caller: 'peer_job', codexHome: s.home, threadId: job,
    createdAt: T - hour, expiresAt: T + hour, attended: false, ensureLoaded: async () => false });
  await eventually(() => ids.slice(0, 8).every(s.subscribed), 'oldest eight subscribed');
  assert.equal(s.resumed(job), false, 'unregistered launcher binding stays inactive');
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
  s.writeBindings(ids.filter((_, i) => i !== 3).map(id => s.binding(id, ids.indexOf(id) + 1, id === ids[0] ? { expires_at: new Date(T + 1500).toISOString() } : {})));
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

test('connector stop unsubscribes every engine', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const ids = [uuid(80), uuid(81), uuid(82)], job = uuid(83);
  const s = await setup(t, { threads: [...ids, job].map(id => ({ id })) });
  s.writeBindings(ids.map((id, i) => s.binding(id, i + 1)));
  const c = s.start();
  c.addLauncherBinding({ profile: 'job', hub: 'https://hub.invalid', caller: 'peer_job', codexHome: s.home, threadId: job,
    createdAt: T, expiresAt: T + hour, attended: false, ensureLoaded: async () => false });
  await eventually(() => ids.every(s.subscribed), 'all authoritative threads subscribed');
  await eventually(() => readFileSync(s.trace, 'utf8').split('\n').filter(l => / watch --json$/.test(l)).length === 3, 'all verified watches started');
  await c.stop();
  for (const id of ids) {
    assert.deepEqual(s.fake.subscribers(id), [], `${id} still subscribed`);
    assert.ok(s.unsubscribed(id), `${id} got no thread/unsubscribe`);
  }
  const n = s.fake.requests.length;
  await s.reload();
  assert.equal(s.fake.requests.length, n, 'no reload or engine runs after stop');
  // Every watch child was started and is gone with its engine.
  const watches = readFileSync(s.trace, 'utf8').split('\n').filter(l => / watch --json$/.test(l));
  assert.equal(watches.length, 3);
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

test('smoke skips without opt-in, without codex, and below the minimum daemon version', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
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
  // A daemon below the compatibility minimum.
  const fake = await startFakeCodexAppServer({ dir: join(f.dir, 'fake') });
  t.after(() => fake.close());
  fake.setVersion('0.0.1');
  const r3 = await smoke({ ...f.env, CODEX_HOME: join(f.dir, 'fake', 'home') });
  assert.equal(r3.status, 0, r3.stderr);
  assert.match(r3.stdout, /^skipped: .*below the minimum supported version \(0\.166\.0\)/);
  // Preflight never runs codex or tmux and never writes outside its checks.
  assert.equal(existsSync(f.trap), false);
  assert.equal(existsSync(noCodex.trap), false);
  assert.deepEqual(fake.requests.map(r => r.method), ['initialize']);
});

const handshakeOnly = (fake: { requests: { method: string }[] }) => fake.requests.map(r => r.method).filter(m => m !== 'initialized');

test('smoke refuses a CODEX_HOME that is not a directory and a busy state directory', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
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


test('complete authority rejects every legacy, malformed, or unresolved row without partial admission', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const [a, b] = [uuid(100), uuid(101)];
  const s = await setup(t, { threads: [{ id: a }, { id: b }] });
  s.start();
  for (const extra of [
    { generation: undefined }, { generation: 'bad' }, { config_path: undefined },
    { config_path: 'relative' }, { config_path: join(s.dir, 'missing') },
    { codex_home: join(s.dir, 'missing') }, { caller: '' }, { hub: 'not-a-url' },
    { profile: '  ' }, { thread_id: 'bad' }, { created_at: 'November 1, 2026' },
    { expires_at: 'bad' }, { idle_dormant_hours: -1 }, { attended: 'yes' },
  ]) {
    s.writeBindings([s.binding(a, 1), s.binding(b, 2, extra)]);
    await s.reload();
    assert.deepEqual(s.fake.requests, [], JSON.stringify(extra));
  }
  s.writeBindings([s.binding(a, 1)]);
  await s.reload();
  await eventually(() => s.subscribed(a), 'valid authority starts');
  writeFileSync(s.bindingsPath, '{broken');
  await s.reload();
  assert.equal(s.subscribed(a), false, 'corruption revokes existing admission');
});

test('recipient aliases, delegated recipients, and canonical thread conflicts reject the whole authority', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const [a, b] = [uuid(110), uuid(111)];
  const s = await setup(t, { threads: [{ id: a }, { id: b }] });
  const alias = join(s.dir, 'home-alias');
  symlinkSync(s.home, alias);
  s.start();
  for (const extra of [
    { caller: 'peer_1', hub: 'https://HUB.invalid:443/' },
    { as: 'peer_1', caller: 'operator' },
    { thread_id: a, codex_home: alias },
    { profile: 'p1' },
  ]) {
    s.writeBindings([s.binding(a, 1), s.binding(b, 2, extra)]);
    await s.reload();
    assert.deepEqual(s.fake.requests, [], JSON.stringify(extra));
  }
});

test('same thread id in distinct canonical homes has independent engines and status', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(120);
  const s = await setup(t, { threads: [{ id }] });
  const other = await startFakeCodexAppServer({ dir: join(s.dir, 'other') });
  t.after(() => other.close());
  other.setThread({ id, status: { type: 'idle' } }); other.attachOther(id);
  const otherHome = join(s.dir, 'other', 'home');
  s.writeBindings([s.binding(id, 1), s.binding(id, 2, { codex_home: otherHome })]);
  s.start();
  await eventually(() => s.subscribed(id) && other.subscribers(id).length === 1, 'both homes subscribed');
  await s.reload();
  const status = JSON.parse(readFileSync(s.bindingsPath + '.status.json', 'utf8'));
  assert.deepEqual(status.bindings.map((b: any) => b.codex_home).sort(), [s.home, otherHome].sort());
  s.writeBindings([s.binding(id, 2, { codex_home: otherHome })]);
  await s.reload();
  assert.equal(s.subscribed(id), false);
  assert.equal(other.subscribers(id).length, 1);
});

test('readiness is private, exact-owner, refreshed, and removed only by its connector', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(130);
  const s = await setup(t, { threads: [{ id }] });
  s.writeBindings([s.binding(id, 1)]);
  const first = s.start();
  const path = s.bindingsPath + '.status.json';
  await s.ready();
  assert.ok(existsSync(path), 'startup publishes status');
  await eventually(() => s.subscribed(id), 'first subscribed');
  await eventually(() => / watch --json\n/.test(readFileSync(s.trace, 'utf8')), 'verified watch started');
  await s.reload();
  const raw = readFileSync(path, 'utf8'), status = JSON.parse(raw);
  assert.equal(statSync(path).mode & 0o777, 0o600);
  assert.deepEqual(status, { version: 1, updated_at: new Date(s.clock.now()).toISOString(), bindings: [{
    config_path: s.env.VECNET_CONFIG, profile: 'p1', as: '', codex_home: s.home,
    thread_id: id, generation: '0'.repeat(31) + '1', state: 'ready', liveness: { live: true },
  }] });
  const otherConfig = join(s.dir, 'second-config'); writeFileSync(otherConfig, '');
  const second = s.start({ VECNET_CONFIG: otherConfig, XDG_CONFIG_HOME: join(s.dir, 'other-xdg') });
  await eventually(() => s.logged('codex_connector_duplicate').length > 0, 'duplicate refused');
  assert.equal(s.fake.requests.filter(r => r.method === 'initialize').length, 1);
  await second.stop();
  assert.equal(readFileSync(path, 'utf8'), raw, 'non-owner stop preserves status');
  await s.reload();
  assert.notEqual(JSON.parse(readFileSync(path, 'utf8')).updated_at, status.updated_at);
  await first.stop();
  assert.equal(existsSync(path), false);
  const third = s.start();
  await eventually(() => s.subscribed(id), 'replacement after stop');
  await third.stop();
});


test('authority path is per canonical HOME and rejects cross-config overrides', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const s = await setup(t);
  const homeAlias = join(s.dir, 'user-home-alias'); symlinkSync(s.env.HOME!, homeAlias);
  assert.equal(codexBindingsPath({ HOME: homeAlias, VECNET_CONFIG: '/different/config', XDG_CONFIG_HOME: '/different/xdg' }), s.bindingsPath);
  assert.equal(codexBindingsPath({ HOME: s.env.HOME }), s.bindingsPath);
  assert.equal(codexBindingsPath({ ...s.env, VECNET_CODEX_BINDINGS: join(s.dir, 'other-bindings') }), undefined);
  assert.equal(codexBindingsPath({ VECNET_CONFIG: s.env.VECNET_CONFIG }), undefined);
  assert.equal(codexBindingsPath({ HOME: '.' }), undefined);
  s.start({ VECNET_CODEX_BINDINGS: join(s.dir, 'other-bindings') });
  await s.ready();
  assert.equal(existsSync(s.bindingsPath + '.status.json'), false, 'rejected override starts no publisher');
});


test('generation-only replacement restarts the engine and publishes the new exact owner', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(140), s = await setup(t, { threads: [{ id }] });
  s.writeBindings([s.binding(id, 1)]); s.start();
  await eventually(() => s.subscribed(id), 'initial owner');
  const first = s.fake.subscribers(id)[0];
  s.writeBindings([s.binding(id, 1, { generation: 'b'.repeat(32) })]);
  await s.reload();
  await eventually(() => s.subscribed(id) && s.fake.subscribers(id)[0] !== first, 'replacement generation');
  assert.equal(JSON.parse(readFileSync(s.bindingsPath + '.status.json', 'utf8')).bindings[0].generation, 'b'.repeat(32));
});

test('launcher callbacks require exact current authority and disappear on revocation', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(150), s = await setup(t, { threads: [{ id, status: { type: 'notLoaded' } }] });
  s.writeBindings([s.binding(id, 1, { attended: true })]);
  const c = s.start(); await s.ready();
  let ensured = 0;
  c.addLauncherBinding({ configPath: s.env.VECNET_CONFIG, generation: '0'.repeat(31) + '1', profile: 'p1',
    hub: 'https://hub.invalid', caller: 'peer_1', codexHome: s.home, threadId: id,
    createdAt: T + 1000, expiresAt: T + 24 * hour, attended: true, ensureLoaded: async () => { ensured++; return false; } });
  await eventually(() => ensured > 0, 'authoritative launcher callback');
  s.writeBindings([]); await s.reload();
  const before = ensured; await s.reload();
  assert.equal(ensured, before);
  assert.deepEqual(JSON.parse(readFileSync(s.bindingsPath + '.status.json', 'utf8')).bindings, []);
});


test('readiness exposes approval refusal and corruption removes ready owners', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(160), s = await setup(t, { threads: [{ id, approvalPolicy: 'on-request' }] });
  s.writeBindings([s.binding(id, 1)]); s.start();
  await eventually(() => / watch --json\n/.test(readFileSync(s.trace, 'utf8')), 'ownership verification completed');
  await s.reload();
  const statusPath = s.bindingsPath + '.status.json';
  const row = JSON.parse(readFileSync(statusPath, 'utf8')).bindings[0];
  assert.equal(row.state, 'ready');
  assert.deepEqual(row.liveness, { live: false, reason: 'approval_policy' });
  writeFileSync(s.bindingsPath, '{broken'); await s.reload();
  assert.deepEqual(JSON.parse(readFileSync(statusPath, 'utf8')).bindings, []);
});


test('global authority runs every config namespace using each row config', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const [a, b] = [uuid(170), uuid(171)], s = await setup(t, { threads: [{ id: a }, { id: b }] });
  const otherConfig = join(s.dir, 'other-config.toml'); writeFileSync(otherConfig, '');
  s.writeBindings([s.binding(a, 1), s.binding(b, 2, { config_path: otherConfig, profile: 'p1' })]);
  s.start();
  await eventually(() => s.subscribed(a) && s.subscribed(b), 'both namespaces subscribed');
  await eventually(() => readFileSync(s.configTrace, 'utf8').includes(otherConfig), 'row-specific config reaches CLI guard');
  assert.ok(readFileSync(s.configTrace, 'utf8').includes(s.env.VECNET_CONFIG!));
  await s.reload();
  assert.deepEqual(JSON.parse(readFileSync(s.bindingsPath + '.status.json', 'utf8')).bindings.map((b: any) => b.config_path).sort(),
    [s.env.VECNET_CONFIG, otherConfig].sort());
});

test('unverified root source never publishes live readiness or starts a watch', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const id = uuid(180), s = await setup(t, { threads: [{ id }], source: 'unknown' });
  s.writeBindings([s.binding(id, 1)]); s.start();
  await eventually(() => s.logged('codex_thread_source_unverified').length > 0, 'source refused');
  await s.reload();
  const row = JSON.parse(readFileSync(s.bindingsPath + '.status.json', 'utf8')).bindings[0];
  assert.deepEqual(row.liveness, { live: false, reason: 'unverified_thread' });
  assert.equal(s.subscribed(id), false);
  assert.ok(readFileSync(s.trace, 'utf8').trim().split('\n').every(line => line.includes(' codex report-status ')), 'only metadata reports may run');
  const reports = readFileSync(s.reportTrace, 'utf8').trim().split('\n').filter(Boolean).map(line => JSON.parse(line));
  assert.ok(reports.every(r => r.delivery !== 'ready'));
  await eventually(() => readFileSync(s.reportTrace, 'utf8').includes('unverified_thread'), 'unverified runtime report');
});
