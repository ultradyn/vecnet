import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { npmInvocation } from '../npm/npm-command.mjs';
const runner = new URL('./budget.mjs', import.meta.url);
function run(args, options = {}) {
  return spawnSync(process.execPath, [fileURLToPath(runner), ...args], { encoding: 'utf8', timeout: 10_000, ...options });
}
test('propagates failure and preserves literal argv without a shell', () => {
  const args = ['space & pipe|', '%PATH%', '!x!', '"quoted"', 'trailing\\'];
  const result = run(['5000', process.execPath, '-e', 'console.log(JSON.stringify(process.argv.slice(1))); process.exitCode=7', ...args]);
  assert.equal(result.status, 7, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), args);
});
test('deadline kills a stubborn grandchild as well as its launcher', async t => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'ci budget '));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const pidFile = path.join(dir, 'pid');
  const childCode = `require('node:fs').writeFileSync(${JSON.stringify(pidFile)},String(process.pid)); process.on('SIGTERM',()=>{}); setInterval(()=>{},1000)`;
  const code = `require('node:child_process').spawn(process.execPath,['-e',${JSON.stringify(childCode)}],{stdio:'ignore',detached:true}); setInterval(()=>{},1000)`;
  const result = run(['1200', process.execPath, '-e', code]);
  const pid = Number(await readFile(pidFile, 'utf8'));
  t.after(() => { try { process.kill(pid, 'SIGKILL'); } catch {} });
  assert.equal(result.status, 124, result.stderr);
  let alive = true;
  for (let i = 0; i < 100; i++) {
    try { process.kill(pid, 0); } catch { alive = false; break; }
    // On Linux a killed orphan can briefly remain a zombie until init reaps it.
    if (process.platform === 'linux') {
      try { if ((await readFile(`/proc/${pid}/stat`, 'utf8')).split(' ')[2] === 'Z') { alive = false; break; } } catch { alive = false; break; }
    }
    await new Promise(r => setTimeout(r, 20));
  }
  assert.equal(alive, false, `descendant ${pid} survived`);
});
test('rejects deadlines outside the strict step budget without executing', () => {
  for (const ms of ['0', '-1', '60000', 'NaN']) {
    const result = run([ms, process.execPath, '-e', 'console.log("EXECUTED")']);
    assert.notEqual(result.status, 0);
    assert.doesNotMatch(result.stdout, /EXECUTED/);
  }
});

test('npm commands use the existing Windows-safe JS entry and propagate status', () => {
  const expected = npmInvocation(['--version']);
  const result = run(['5000', 'npm', '--version']);
  assert.equal(result.status, 0, `${expected.file}: ${result.stderr}`);
  assert.match(result.stdout.trim(), /^\d+\.\d+\.\d+$/);
});
