import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
const budget = fileURLToPath(new URL('./budget.mjs', import.meta.url));
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function gone(pid) { try { process.kill(pid, 0); return false; } catch { return true; } }
for (const exit of [0, 7]) for (const stdio of ['ignore', 'inherit']) {
  test(`early exit ${exit}, detached grandchild ${stdio} pipes is killed and reaped`, { timeout: 8000 }, async t => {
    const dir = await mkdtemp(path.join(os.tmpdir(), 'ci-owned-')); t.after(() => rm(dir, { recursive: true, force: true }));
    const marker = path.join(dir, 'pid');
    const payload = `require('node:fs').writeFileSync(${JSON.stringify(marker)},String(process.pid));setInterval(()=>{},1000)`;
    // Wait for readiness before the leader exits: prove the escaped process
    // actually existed, rather than testing a failed spawn.
    const leader = `const fs=require('node:fs');const c=require('node:child_process').spawn(process.execPath,['-e',${JSON.stringify(payload)}],{detached:true,stdio:${JSON.stringify(stdio)}});c.unref();const timer=setInterval(()=>{if(fs.existsSync(${JSON.stringify(marker)})){clearInterval(timer);process.exit(${exit})}},5)`;
    const runner = spawn(process.execPath, [budget, '2000', process.execPath, '-e', leader], { stdio: ['ignore', 'pipe', 'pipe'] });
    runner.stdout.resume();runner.stderr.resume();
    let watchdog;
    const result = await Promise.race([new Promise(resolve => runner.once('close', code => resolve(code))), new Promise(resolve => {watchdog=setTimeout(()=>{runner.kill('SIGTERM');resolve('hung');},3500);})]);
    clearTimeout(watchdog);
    const pid = Number(await readFile(marker, 'utf8'));t.after(() => {try {process.kill(pid,'SIGKILL')} catch {}});
    assert.equal(result, exit, 'leader exit must not turn into an inherited-pipe timeout');
    for(let i=0;i<100 && !await gone(pid);i++)await sleep(10);
    assert.equal(await gone(pid), true, 'owned detached descendant remains alive or unreaped');
  });
}
async function readPid(marker, signal) {
  try {
    const text = await readFile(marker, { encoding: 'utf8', signal });
    return /^[1-9]\d*\n$/.test(text) ? Number(text) : undefined;
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
}

async function waitForPid(marker, signal) {
  for (;;) {
    const pid = await readPid(marker, signal);
    if (pid) return pid;
    await delay(10, undefined, { signal });
  }
}

async function closeWithinOneSecond(closed) {
  let timer;
  try {
    return await Promise.race([closed, new Promise(resolve => { timer = setTimeout(resolve, 1000); })]);
  } finally {
    clearTimeout(timer);
  }
}

for (const signal of ['SIGTERM', 'SIGKILL']) test(`cancelling ${signal} budget supervisor kills and reaps its live detached tree`, { timeout: 8000 }, async t => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'ci-cancel-'));
  const marker = path.join(dir, 'pid');
  const payload = `require('node:fs').writeFileSync(${JSON.stringify(marker)},String(process.pid)+'\\n');setInterval(()=>{},1000)`;
  const leader = `require('node:child_process').spawn(process.execPath,['-e',${JSON.stringify(payload)}],{detached:true,stdio:'inherit'});setInterval(()=>{},1000)`;
  const runner = spawn(process.execPath, [budget, '5000', process.execPath, '-e', leader], { stdio: ['ignore', 'pipe', 'pipe'] });
  const active = () => runner.exitCode === null && runner.signalCode === null;
  let pid, runnerError;
  const closed = new Promise(resolve => {
    runner.on('error', error => { runnerError = error; });
    runner.once('close', (code, signal) => resolve({ code, signal, error: runnerError }));
  });
  t.after(async () => {
    try {
      // Killing the supervisor closes the owner's control pipe, even if
      // startup failed. Drain its inherited pipes before removing the marker.
      if (active()) runner.kill('SIGKILL');
      const result = await closeWithinOneSecond(closed);
      pid ??= await readPid(marker);
      if (pid && !await gone(pid)) {
        try { process.kill(pid, 'SIGKILL'); } catch (error) { if (!await gone(pid)) throw error; }
        for (let i = 0; i < 100 && !await gone(pid); i++) await sleep(10);
        assert.ok(await gone(pid), `fixture cleanup did not reap grandchild ${pid}`);
      }
      assert.ok(result, 'fixture cleanup did not drain supervisor');
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });
  runner.stdout.resume();
  runner.stderr.resume();

  // Wait for the real child, not a second startup deadline. Stop polling on
  // supervisor close (including the owner's deadline) or the test timeout.
  const startup = new AbortController();
  try {
    const ready = await Promise.race([
      waitForPid(marker, AbortSignal.any([startup.signal, t.signal])),
      closed.then(result => { assert.fail(`supervisor closed before grandchild readiness: ${JSON.stringify(result)}`); }),
    ]);
    pid = ready;
  } finally {
    startup.abort();
  }
  assert.ok(process.kill(pid, 0), 'detached grandchild must be live before cancellation');
  assert.ok(active(), 'supervisor must still be running before cancellation');
  assert.ok(runner.kill(signal), `could not send ${signal} to supervisor`);
  const result = await closeWithinOneSecond(closed);
  assert.ok(result, `supervisor did not close within 1s of ${signal}`);
  assert.ifError(result.error);
  // The owners return 143 when cancelled via the control pipe. If the
  // supervisor itself is terminated, Node reports a signal instead.
  assert.ok(result.code === 143 || result.signal === signal,
    `expected cancellation, not natural exit: ${JSON.stringify(result)}`);
  for (let i = 0; i < 100 && !await gone(pid); i++) await sleep(10);
  assert.ok(await gone(pid), 'cancellation orphaned owned grandchild');
});
