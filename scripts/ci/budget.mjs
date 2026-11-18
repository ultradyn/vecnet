// A command deadline is shorter than Actions' one-minute step ceiling so tree
// cleanup and log flushing fit too. No shell: argv is always data.
import { spawn, execFileSync } from 'node:child_process';
import { performance } from 'node:perf_hooks';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { npmInvocation } from '../npm/npm-command.mjs';

export async function run(file, args, { ms = 53_000, cwd, env = process.env, stdio = 'inherit' } = {}) {
  if (!Number.isInteger(ms) || ms <= 0 || ms > 55_000) throw new Error('deadline must be 1..55000 ms');
  if (file === 'npm') ({ file, args } = npmInvocation(args, { env }));
  const started = performance.now();
  const child = spawn(file, args, { cwd, env, stdio, windowsHide: true, detached: process.platform !== 'win32' });
  let expired = false;
  let cleanup;
  // taskkill must run while the Windows leader still exists so /T can find
  // descendants. On POSIX the owned process group survives its leader.
  const killTree = () => cleanup ??= new Promise((resolve, reject) => {
    if (!child.pid) { resolve(); return; }
    if (process.platform !== 'win32') {
      // Some test fixtures deliberately start detached sessions. A process
      // group alone misses those; collect the still-owned descendant tree
      // before killing its leader (and therefore losing ancestry).
      const tree = execFileSync('ps', ['-eo', 'pid=,ppid='], { encoding: 'utf8', timeout: 1000 }).trim().split('\n')
        .map(line => line.trim().split(/\s+/).map(Number));
      const owned = new Set([child.pid]);
      for (let size = -1; size !== owned.size;) {
        size = owned.size;
        for (const [pid, parent] of tree) if (owned.has(parent)) owned.add(pid);
      }
      for (const pid of [...owned].reverse()) {
        if (pid === child.pid) continue;
        try { process.kill(pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') { reject(error); return; } }
      }
      try { process.kill(-child.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') { reject(error); return; } }
      resolve(); return;
    }
    const killer = spawn(path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'taskkill.exe'),
      ['/pid', String(child.pid), '/T', '/F'], { stdio: 'ignore', windowsHide: true });
    const timer = setTimeout(() => { killer.kill(); reject(new Error('taskkill did not finish')); }, 3000);
    killer.once('error', error => { clearTimeout(timer); reject(error); });
    killer.once('close', code => { clearTimeout(timer); if (code !== 0 && child.exitCode === null) reject(new Error(`taskkill exit ${code}`)); else resolve(); });
  });
  const timer = setTimeout(() => { expired = true; killTree().catch(error => { console.error(error.message); child.kill('SIGKILL'); }); }, ms);
  try {
    const code = await new Promise((resolve, reject) => {
      child.once('error', reject);
      child.once('close', (code, signal) => resolve(signal ? 1 : code ?? 1));
    });
    if (cleanup) await cleanup;
    console.error(`ci-command elapsed_ms=${Math.round(performance.now() - started)} exit=${expired ? 124 : code}`);
    return expired ? 124 : code;
  } finally { clearTimeout(timer); }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [, , ms, file, ...args] = process.argv;
    if (!file) throw new Error('usage: budget.mjs milliseconds command [args...]');
    process.exitCode = await run(file, args, { ms: Number(ms) });
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
