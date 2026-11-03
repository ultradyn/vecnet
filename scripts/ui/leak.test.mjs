import test, { before } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { createInterface } from 'node:readline';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import * as harness from './lib.mjs';

async function until(check, timeout = 8000) {
  const deadline = Date.now() + timeout;
  while (!check()) {
    assert.ok(Date.now() < deadline, 'demo did not disappear within 8s');
    await new Promise(resolve => setTimeout(resolve, 50));
  }
}
function gone(pid) {
  try { process.kill(pid, 0); return false; }
  catch (error) { assert.equal(error.code, 'ESRCH'); return true; }
}
function safeDir(dir, kind) {
  const prefix = join(tmpdir(), `vecnet-ui-${kind}-`);
  return typeof dir === 'string' && dir.startsWith(prefix) && /^[A-Za-z0-9]+$/.test(dir.slice(prefix.length));
}
// Resolve Go's cache before replacing XDG_CACHE_HOME for vecnet isolation.
// Without an explicit GOCACHE, every fixture recompiles all dependencies.
const cacheResult = spawnSync('build2', ['go', 'env', 'GOCACHE'], { encoding: 'utf8' });
assert.equal(cacheResult.status, 0, cacheResult.stderr);
const goCache = cacheResult.stdout.trim();
assert.ok(goCache && goCache !== 'off', 'leak fixtures require a reusable Go build cache');
function fixtureEnv(privateRoot) {
  return { ...process.env, GOCACHE: goCache, TMUX_TMPDIR: privateRoot, TMUX: '', VECNET_CONFIG: join(privateRoot, 'config.toml'), XDG_CONFIG_HOME: join(privateRoot, 'config'), XDG_CACHE_HOME: join(privateRoot, 'cache') };
}

// Budget dependency compilation separately from demo/TUI readiness and the
// eight-second teardown assertions. Child fixtures still own their binaries.
before(async () => {
  const privateRoot = await mkdtemp(join(tmpdir(), 'vecnet-leak-test-'));
  try {
    const binDir = join(privateRoot, 'bin');
    await mkdir(binDir);
    await harness.run('build2', ['go', 'build', '-p', '2', '-o', binDir, './internal/opseed/demo', './cmd/vecnet'], {
      env: { ...fixtureEnv(privateRoot), CGO_ENABLED: '0' },
    });
  } finally { await rm(privateRoot, { recursive: true, force: true }); }
}, { timeout: 600000 });

test('fixture keeps Go builds out of its private XDG cache', async t => {
  const privateRoot = await mkdtemp(join(tmpdir(), 'vecnet-leak-test-'));
  t.after(() => rm(privateRoot, { recursive: true, force: true }));
  const inherited = spawnSync('build2', ['go', 'env', 'GOCACHE'], { encoding: 'utf8' });
  assert.equal(inherited.status, 0, inherited.stderr);
  const env = fixtureEnv(privateRoot);
  const isolated = spawnSync('build2', ['go', 'env', 'GOCACHE'], { env, encoding: 'utf8' });
  assert.equal(isolated.status, 0, isolated.stderr);
  assert.equal(isolated.stdout.trim(), inherited.stdout.trim(), 'fixture reuses the parent Go build cache');
  assert.equal(env.GOCACHE, inherited.stdout.trim(), 'Go cache is explicit before overriding XDG_CACHE_HOME');
  for (const name of ['VECNET_CONFIG', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME']) {
    assert.ok(env[name].startsWith(`${privateRoot}/`), `${name} stays private`);
  }
});

async function fixture(t, { tui = false } = {}) {
  const privateRoot = await mkdtemp(join(tmpdir(), 'vecnet-leak-test-'));
  const child = spawn(process.execPath, [new URL('fixtures/boot-and-hang.mjs', import.meta.url).pathname, ...(tui ? ['--tui'] : [])], {
    env: fixtureEnv(privateRoot),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const closed = once(child, 'close');
  const socket = `oui-${child.pid}`;
  const socketPath = join(privateRoot, `tmux-${process.getuid()}`, socket);
  const tmux = (...args) => spawnSync('tmux', ['-L', socket, ...args], { env: { ...process.env, TMUX_TMPDIR: privateRoot, TMUX: '' }, encoding: 'utf8' });
  let stderr = '';
  child.stderr.on('data', d => { stderr += d; });
  const lines = createInterface({ input: child.stdout });
  let env, timer;
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    await closed;
    if (env?.pid && !gone(env.pid)) process.kill(env.pid, 'SIGKILL');
    if (tui) tmux('kill-server');
    for (const kind of ['data', 'build']) {
      const dir = env?.[kind === 'data' ? 'dir' : 'buildDir'];
      if (safeDir(dir, kind)) await rm(dir, { recursive: true, force: true });
    }
    await rm(privateRoot, { recursive: true, force: true });
  });
  try {
    const line = await Promise.race([
      once(lines, 'line').then(([line]) => line),
      closed.then(() => { throw new Error(`fixture exited before readiness: ${stderr}`); }),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('fixture startup timeout')), tui ? 240000 : 120000); }),
    ]);
    env = JSON.parse(line);
    assert.ok(Number.isInteger(env.pid) && env.pid > 0, 'fixture reports its demo pid');
    assert.ok(safeDir(env.dir, 'data'), 'fixture reports a private data dir');
    assert.ok(safeDir(env.buildDir, 'build'), 'fixture reports a private build dir');
    return { ...env, child, closed, socketPath, tmux };
  } finally { clearTimeout(timer); lines.close(); child.stdout.resume(); }
}

test('kill -9 of a screen script leaves no demo', { timeout: 150000 }, async t => {
  const env = await fixture(t);
  const deadline = Date.now() + 8000;
  env.child.kill('SIGKILL');
  await env.closed;
  await until(() => gone(env.pid), deadline - Date.now());
  assert.ok(Date.now() <= deadline, 'demo gone within 8s of signalling its holder');
});

test('SIGTERM of a screen script removes demo, data dir, build dir and tmux server', { timeout: 270000 }, async t => {
  const env = await fixture(t, { tui: true });
  assert.ok(existsSync(env.socketPath), 'withTui created its private socket');
  assert.equal(env.tmux('has-session', '-t', 'ui').status, 0, 'withTui session is ready before SIGTERM');
  const deadline = Date.now() + 8000;
  env.child.kill('SIGTERM');
  const [code] = await env.closed;
  assert.equal(code, 143, 'fixture exits through its SIGTERM cleanup handler');
  await until(() => gone(env.pid), deadline - Date.now());
  assert.ok(Date.now() <= deadline, 'demo gone within 8s of signalling its holder');
  assert.equal(existsSync(env.dir), false);
  assert.equal(existsSync(env.buildDir), false);
  await until(() => env.tmux('has-session', '-t', 'ui').status !== 0 && !existsSync(env.socketPath), deadline - Date.now());
  assert.notEqual(env.tmux('has-session', '-t', 'ui').status, 0, 'interrupted withTui server has no session');
  assert.equal(existsSync(env.socketPath), false, 'interrupted withTui socket is removed');
  assert.ok(Date.now() <= deadline, 'demo and tmux cleanup complete within 8s of SIGTERM');
});

test('closed demos leave the live map', { timeout: 150000 }, async t => {
  assert.equal(typeof harness.liveDemoCount, 'function', 'liveDemoCount accessor exists');
  t.after(() => harness.cleanBuild());
  const first = await harness.bootDemo();
  t.after(() => first.close());
  assert.equal(harness.liveDemoCount(), 1);
  await first.close();
  assert.equal(harness.liveDemoCount(), 0);
  const second = await harness.bootDemo();
  t.after(() => second.close());
  assert.equal(harness.liveDemoCount(), 1);
  process.kill(second.pid, 'SIGKILL');
  await until(() => harness.liveDemoCount() === 0);
  await second.close();
});
