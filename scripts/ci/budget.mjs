// Own the child tree for its whole lifetime, including early leader exit.
// Linux: kernel subreaper. Windows: suspended start inside kill-on-close Job.
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { performance } from 'node:perf_hooks';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { npmInvocation } from '../npm/npm-command.mjs';

export async function run(file, args, { ms = 53_000, cwd, env = process.env, stdio = 'inherit' } = {}) {
  if (!Number.isInteger(ms) || ms <= 0 || ms > 55_000) throw new Error('deadline must be 1..55000 ms');
  if (file === 'npm') ({ file, args } = npmInvocation(args, { env }));
  // The acceptance preflight exports the absolute remaining overall deadline.
  if (env.CI_RUN_DEADLINE) ms = Math.min(ms, Number(env.CI_RUN_DEADLINE) - Date.now() - 3000);
  if (!Number.isFinite(ms) || ms <= 0) throw new Error('overall run budget exhausted');
  const started = performance.now();
  const windows = process.platform === 'win32';
  if (!windows && process.platform !== 'linux') throw new Error('CI ownership requires Linux or Windows');
  // Windows CreateProcess requires an application path; resolve PATH without
  // a shell. npm has already resolved its JS entry using the existing helper.
  if (windows && !path.isAbsolute(file)) {
    const search = Object.entries(env).find(([key]) => key.toUpperCase() === 'PATH')?.[1] || '';
    file = search.split(path.delimiter).flatMap(dir => [path.resolve(cwd ?? process.cwd(), dir, file), path.resolve(cwd ?? process.cwd(), dir, file + '.exe')]).find(candidate => existsSync(candidate));
    if (!file) throw new Error('command not found');
  }
  let owner, implementation;
  if (windows) {
    // This is a job-local path exported by the artifact stager. A bad
    // configured owner must fail; only an unset value permits bootstrap.
    const native = env.CI_WINDOWS_OWNER;
    if (native !== undefined && !path.isAbsolute(native)) throw new Error('CI_WINDOWS_OWNER must be an absolute staged path');
    const request = Buffer.from(JSON.stringify({ file, args, ms: Math.min(Math.floor(ms),51500) })).toString('base64');
    [owner, implementation] = native === undefined
      ? [['pwsh', ['-NoProfile', '-NonInteractive', '-File', fileURLToPath(new URL('./owned-windows.ps1', import.meta.url)), request]], 'windows-powershell']
      : [[native, [request]], 'windows-native'];
  } else {
    owner = ['python3', [fileURLToPath(new URL('./owned-linux.py', import.meta.url)), String(Math.floor(ms)), file, ...args]];
    implementation = 'linux';
  }
  const child = spawn(owner[0], owner[1], { cwd, env, windowsHide: true,
    stdio: ['pipe', stdio === 'inherit' ? 'inherit' : 'pipe', stdio === 'inherit' ? 'inherit' : 'pipe'] });
  const cancel = () => { if(!child.stdin.destroyed) child.stdin.end('cancel\n'); };
  process.on('SIGTERM', cancel);process.on('SIGINT', cancel);
  try {
    const code = await new Promise((resolve, reject) => {
      child.once('error', reject);
      child.stdin.on('error', error => { if(error.code !== 'EPIPE') reject(error); });
      child.once('close', (code, signal) => resolve(signal ? 1 : code ?? 1));
    });
    console.error(`ci-command elapsed_ms=${Math.round(performance.now() - started)} exit=${code} owner=${implementation}`);
    return code;
  } finally { process.off('SIGTERM', cancel);process.off('SIGINT', cancel);child.stdin.destroy(); }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [, , ms, file, ...args] = process.argv;
    if (!file) throw new Error('usage: budget.mjs milliseconds command [args...]');
    process.exitCode = await run(file, args, { ms: Number(ms) });
  } catch (error) { console.error(error.message);process.exitCode = 1; }
}
