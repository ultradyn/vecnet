import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
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
for(const signal of ['SIGTERM','SIGKILL']) test(`cancelling ${signal} budget supervisor kills and reaps its live detached tree`, { timeout: 8000 }, async t => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'ci-cancel-'));t.after(() => rm(dir,{recursive:true,force:true}));
  const marker=path.join(dir,'pid');
  const payload=`require('node:fs').writeFileSync(${JSON.stringify(marker)},String(process.pid));setInterval(()=>{},1000)`;
  const leader=`require('node:child_process').spawn(process.execPath,['-e',${JSON.stringify(payload)}],{detached:true,stdio:'inherit'});setInterval(()=>{},1000)`;
  const runner=spawn(process.execPath,[budget,'5000',process.execPath,'-e',leader],{stdio:['ignore','pipe','pipe']});runner.stdout.resume();runner.stderr.resume();
  let pid;
  for(let i=0;i<200;i++){try{pid=Number(await readFile(marker,'utf8'));break}catch{}await sleep(10)}
  assert.ok(pid,'grandchild started');t.after(()=>{try{process.kill(pid,'SIGKILL')}catch{}});
  runner.kill(signal);await Promise.race([new Promise(resolve=>runner.once('close',resolve)),sleep(1000)]);
  for(let i=0;i<100 && !await gone(pid);i++)await sleep(10);
  assert.equal(await gone(pid),true,'cancellation orphaned owned grandchild');
});
