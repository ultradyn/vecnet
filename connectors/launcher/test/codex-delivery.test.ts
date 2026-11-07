import { test } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { startFakeCodexAppServer } from './fake-codex-appserver.ts';

const uuid = (n: number) => `00000000-0000-4000-8000-${n.toString(16).padStart(12, '0')}`;
const entry = fileURLToPath(new URL('../src/codex/delivery-main.ts', import.meta.url));
async function eventually(fn: () => boolean, what: string) {
  for (let i = 0; i < 1000; i++) { if (fn()) return; await delay(10); }
  assert.fail(`timed out waiting for ${what}`);
}
async function fixture(t: TestContext, unsubscribeDelayMs = 0) {
  const dir = mkdtempSync(join(tmpdir(), 'vn-cd-'));
  const fake = await startFakeCodexAppServer({ dir: join(dir, 'fake'), unsubscribeDelayMs });
  const home = join(dir, 'fake', 'home'), bin = join(dir, 'bin'), trap = join(dir, 'codex-ran');
  for (const name of ['bin', 'home', 'config', 'runtime', 'data', 'state']) mkdirSync(join(dir, name), { mode: 0o700 });
  const trace = join(dir, 'trace');
  writeFileSync(trace, '');
  writeFileSync(join(bin, 'codex'), `#!/bin/sh\necho forbidden > '${trap}'\nexit 99\n`, { mode: 0o700 });
  writeFileSync(join(bin, 'vecnet'), `#!/bin/sh
printf '%s\\n' "$*" >> '${trace}'
case " $* " in
  *" watch "*) exec sleep 3600 ;;
  *" --release "*) echo '{"version":1,"released":true}' ;;
  *" hook "*) echo '{"version":1,"deliveries":[]}' ;;
  *) exit 2 ;;
esac
`, { mode: 0o700 });
  const registry = join(dir, 'registry.json'), bindings = join(dir, 'bindings.json'), config = join(dir, 'config', 'never-read.toml');
  execFileSync('mkfifo', ['-m', '600', config]);
  writeFileSync(registry, JSON.stringify({ version: 1, sources: [{ config, bindings }] }), { mode: 0o600 });
  writeFileSync(bindings, JSON.stringify({ version: 1, bindings: [] }), { mode: 0o600 });
  const env: NodeJS.ProcessEnv = { HOME: join(dir, 'home'), XDG_CONFIG_HOME: join(dir, 'config'),
    XDG_RUNTIME_DIR: join(dir, 'runtime'), XDG_DATA_HOME: join(dir, 'data'), CODEX_HOME: home,
    PATH: `${bin}:/usr/bin:/bin`, VECNET_BIN: join(bin, 'vecnet') };
  t.after(async () => {
    await fake.close();
    assert.equal(existsSync(trap), false, 'Codex must never be spawned');
    assert.deepEqual(fake.overrides, []);
    assert.deepEqual(fake.replies, []);
    rmSync(dir, { recursive: true, force: true });
  });
  const bind = (id: string) => writeFileSync(bindings, JSON.stringify({ version: 1, bindings: [{
    profile: 'fixture', as: '', hub: 'https://hub.invalid', caller: 'peer_fixture', codex_home: home, thread_id: id,
    created_at: new Date(Date.now() - 1000).toISOString(), expires_at: new Date(Date.now() + 3_600_000).toISOString(),
  }] }), { mode: 0o600 });
  return { dir, fake, home, env, registry, bindings, trace, bind, stateDir: join(dir, 'state') };
}
function collect(child: ChildProcess) {
  let stdout = '', stderr = '';
  child.stdout?.setEncoding('utf8').on('data', s => { stdout += s; });
  child.stderr?.setEncoding('utf8').on('data', s => { stderr += s; });
  const done = new Promise<number | null>((resolve, reject) => {
    child.once('error', reject); child.once('close', resolve);
  });
  return { done, stdout: () => stdout, stderr: () => stderr };
}

test('probe never resumes or starts Codex', async t => {
  const f = await fixture(t), id = uuid(1), previous = uuid(2);
  f.fake.setThread({ id, status: { type: 'idle' } });
  f.fake.setThread({ id: previous, status: { type: 'active', activeFlags: [] } });
  const child = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', entry, 'probe', '--codex-home', f.home,
    '--thread', id, '--previous-thread', previous], { env: f.env, stdio: ['ignore', 'pipe', 'pipe'], timeout: 15000 });
  const output = collect(child);
  assert.equal(await output.done, 0, output.stderr());
  assert.equal(output.stderr(), '');
  assert.deepEqual(JSON.parse(output.stdout()), { loaded: true, previous: 'live' });
  assert.deepEqual(f.fake.requests.map(r => r.method), ['initialize', 'initialized', 'thread/read', 'thread/read']);
  assert.deepEqual(f.fake.subscribers(id), []);
  assert.deepEqual(f.fake.subscribers(previous), []);
});

test('probe marks an unloaded previous thread gone and unknown failures unknown', async t => {
  const { probeCodexDelivery } = await import('../src/codex/delivery.ts');
  const f = await fixture(t), id = uuid(3), previous = uuid(4);
  f.fake.setThread({ id, status: { type: 'notLoaded' } });
  f.fake.setThread({ id: previous, status: { type: 'notLoaded' } });
  const options = { codexHome: f.home, thread: id, previousThread: previous };
  assert.deepEqual(await probeCodexDelivery(options), { loaded: false, previous: 'gone' });
  // A valid loaded current thread must not turn an unrecognized status into a takeover.
  f.fake.setThread({ id, status: { type: 'idle' } });
  f.fake.setThread({ id: previous, status: { type: 'futureUnknownStatus' } });
  assert.deepEqual(await probeCodexDelivery(options), { loaded: true, previous: 'unknown' });
  assert.deepEqual(await probeCodexDelivery({ ...options, previousThread: uuid(99) }), { loaded: true, previous: 'unknown' });
  f.fake.setVersion('0.0.1');
  assert.deepEqual(await probeCodexDelivery(options), { loaded: false, previous: 'unknown' });
  assert.deepEqual(await probeCodexDelivery({ ...options, codexHome: join(f.dir, 'missing') }), { loaded: false, previous: 'unknown' });
  assert.ok(f.fake.requests.every(r => ['initialize', 'initialized', 'thread/read'].includes(r.method)));
});

test('delivery runner retains KV across reopen', async t => {
  const { startDeliveryRunner } = await import('../src/codex/delivery.ts');
  const f = await fixture(t), clock = fakeClock(Date.now()), log = memoryLog();
  const options = { hub: 'https://hub.invalid', stateDir: f.stateDir, registry: f.registry, env: f.env, clock, log,
    fixture: { allowMissingLeaseFd: true as const } };
  const first = startDeliveryRunner(options);
  first.kv.set('hub:"https://hub.invalid":inflight:accepted', '{"state":"pending"}');
  first.kv.set('hub:"https://hub.invalid":inflight:ambiguous', '{"state":"unresolved"}');
  await first.stop();
  const second = startDeliveryRunner(options);
  t.after(() => second.stop());
  assert.equal(second.kv.get('hub:"https://hub.invalid":inflight:accepted'), '{"state":"pending"}');
  assert.equal(second.kv.get('hub:"https://hub.invalid":inflight:ambiguous'), '{"state":"unresolved"}');
  assert.equal(statSync(join(f.stateDir, 'ledger.sqlite')).mode & 0o777, 0o600);
  assert.equal(statSync(f.stateDir).mode & 0o777, 0o700);
});

test('SIGTERM drains engines before closing the inherited lease', async t => {
  // This checks a real OS flock shared with FD3, not a PID file or a mock lock.
  const f = await fixture(t, 1200), id = uuid(5), lease = join(f.dir, 'lease');
  f.fake.setThread({ id, status: { type: 'idle' } }); f.fake.attachOther(id); f.bind(id);
  writeFileSync(lease, '', { mode: 0o600 });
  const child = spawn('bash', ['-c', 'exec 3>"$1"; flock -x 3; exec "$2" --disable-warning=ExperimentalWarning "$3" run --hub https://hub.invalid --state-dir "$4" --registry "$5"',
    'fixture-holder', lease, process.execPath, entry, f.stateDir, f.registry], { env: f.env, stdio: ['ignore', 'pipe', 'pipe', 'ignore'] });
  const output = collect(child);
  t.after(async () => { if (child.exitCode === null) child.kill('SIGKILL'); await output.done; });
  await eventually(() => f.fake.subscribers(id).length === 1 || child.exitCode !== null, 'runner subscription');
  assert.equal(child.exitCode, null, output.stderr());
  const locked = () => {
    try { execFileSync('flock', ['-n', lease, 'true'], { stdio: 'ignore' }); return false; }
    catch { return true; }
  };
  assert.ok(locked(), 'lease held while the worker is running');
  child.kill('SIGTERM');
  await eventually(() => f.fake.requests.some(r => r.method === 'thread/unsubscribe'), 'drain begins after SIGTERM');
  assert.ok(locked(), 'lease remains held during delayed unsubscribe');
  assert.equal(await output.done, 0, output.stderr());
  assert.deepEqual(f.fake.subscribers(id), []);
  assert.equal(locked(), false, 'lease released only after engine shutdown');
  const count = f.fake.requests.length;
  await delay(30);
  assert.equal(f.fake.requests.length, count);
});
