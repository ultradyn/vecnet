import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createInterface } from 'node:readline';
import { existsSync } from 'node:fs';
import { rm } from 'node:fs/promises';
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
async function fixture(t) {
  const child = spawn(process.execPath, [new URL('fixtures/boot-and-hang.mjs', import.meta.url).pathname], { stdio: ['ignore', 'pipe', 'pipe'] });
  const closed = once(child, 'close');
  let stderr = '';
  child.stderr.on('data', d => { stderr += d; });
  const lines = createInterface({ input: child.stdout });
  let env, timer;
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    await closed;
    if (env?.pid && !gone(env.pid)) process.kill(env.pid, 'SIGKILL');
    for (const kind of ['data', 'build']) {
      const dir = env?.[kind === 'data' ? 'dir' : 'buildDir'];
      if (safeDir(dir, kind)) await rm(dir, { recursive: true, force: true });
    }
  });
  try {
    const line = await Promise.race([
      once(lines, 'line').then(([line]) => line),
      closed.then(() => { throw new Error(`fixture exited before readiness: ${stderr}`); }),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('fixture startup timeout')), 120000); }),
    ]);
    env = JSON.parse(line);
    assert.ok(Number.isInteger(env.pid) && env.pid > 0, 'fixture reports its demo pid');
    assert.ok(safeDir(env.dir, 'data'), 'fixture reports a private data dir');
    assert.ok(safeDir(env.buildDir, 'build'), 'fixture reports a private build dir');
    return { ...env, child, closed };
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

test('SIGTERM of a screen script removes demo, data dir and build dir', { timeout: 150000 }, async t => {
  const env = await fixture(t);
  const deadline = Date.now() + 8000;
  env.child.kill('SIGTERM');
  await env.closed;
  await until(() => gone(env.pid), deadline - Date.now());
  assert.ok(Date.now() <= deadline, 'demo gone within 8s of signalling its holder');
  assert.equal(existsSync(env.dir), false);
  assert.equal(existsSync(env.buildDir), false);
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
