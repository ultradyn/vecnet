import { existsSync } from 'node:fs';
import path from 'node:path';

// Windows cannot exec npm.cmd without cmd.exe. Do not turn shell:true on or
// interpolate package/prefix paths into a command string: invoke npm's JS entry
// with Node instead, preserving every argument (including shell metacharacters).
export function npmInvocation(args, { platform = process.platform, env = process.env, execPath = process.execPath } = {}) {
  if (platform !== 'win32') return { file: 'npm', args };
  const searchPath = Object.entries(env).find(([key]) => key.toUpperCase() === 'PATH')?.[1] || '';
  // Honor PATH first so a pinned/global npm upgrade wins over Node's bundled
  // npm. Fall back to the installation beside Node when PATH omits it.
  const directories = [...searchPath.split(path.delimiter).filter(Boolean), path.dirname(execPath)];
  const candidates = [
    env.npm_execpath,
    ...directories.map(directory => path.join(directory, 'node_modules', 'npm', 'bin', 'npm-cli.js')),
  ];
  const entry = candidates.find(candidate => candidate && /\.(?:c?js|mjs)$/i.test(candidate) && existsSync(candidate));
  if (!entry) throw new Error('Cannot find npm-cli.js; install npm alongside Node or set npm_execpath to its JS entry point');
  return { file: execPath, args: [entry, ...args] };
}
