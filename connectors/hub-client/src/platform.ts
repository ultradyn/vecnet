import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import { statSync } from 'node:fs';
import { delimiter, isAbsolute, join, resolve, win32 } from 'node:path';

export type Environment = Readonly<Record<string, string | undefined>>;
export function envValue(env: Environment, name: string, platform = process.platform): string | undefined {
  return platform === 'win32' ? env[name] ?? Object.entries(env).reverse().find(([key]) => key.toUpperCase() === name.toUpperCase())?.[1] : env[name];
}

/** Match the CLI: explicit HOME, then USERPROFILE on Windows. Never
 * substitute the service account's real home for a caller's missing setting. */
export function userHome(env: Environment, platform = process.platform): string | undefined {
  const home = platform === 'win32' ? envValue(env, 'HOME', platform) || envValue(env, 'USERPROFILE', platform) : env.HOME;
  return home && (platform === 'win32' ? win32.isAbsolute(home) : isAbsolute(home)) ? home : undefined;
}

/** Keep Windows' loader, temp and user paths even in a tightly allowlisted CLI
 * environment. No auth/session variables are admitted by this list. */
export const CLI_ENV_KEYS = ['HOME', 'USERPROFILE', 'HOMEDRIVE', 'HOMEPATH', 'PATH', 'PATHEXT', 'SystemRoot', 'WINDIR', 'COMSPEC',
  'TEMP', 'TMP', 'APPDATA', 'LOCALAPPDATA', 'XDG_CONFIG_HOME', 'XDG_RUNTIME_DIR', 'VECNET_CONFIG', 'VECNET_BIN'];
export function cliEnvironment(env: Environment): Record<string, string> {
  return Object.fromEntries(CLI_ENV_KEYS.flatMap(key => { const value = envValue(env, key); return value === undefined ? [] : [[key, value]]; }));
}

function executable(command: string, env: Environment, cwd: string): string {
  const explicit = /[\\/]/.test(command) || win32.isAbsolute(command);
  const paths = explicit ? [''] : (envValue(env, 'PATH') ?? '').split(delimiter).map(p => p.replace(/^"(.*)"$/, '$1'));
  const extensions = (envValue(env, 'PATHEXT') ?? '.COM;.EXE;.BAT;.CMD').split(';').filter(e => /^\.(exe|com|cmd|bat)$/i.test(e));
  const suffixes = /\.(exe|com|cmd|bat)$/i.test(command) ? [''] : ['', ...extensions];
  for (const path of paths) for (const suffix of suffixes) {
    if (!explicit && !path) continue; // Do not implicitly execute a cwd replacement.
    const candidate = resolve(cwd, path, command + suffix);
    try { if (statSync(candidate).isFile()) return candidate; } catch { /* next PATH entry */ }
  }
  return command;
}

// cmd parses metacharacters twice when invoking a batch/npm shim. Quote for
// CommandLineToArgvW first, then escape cmd's syntax on each parse, with delayed
// expansion disabled. Never concatenate unquoted user text or enable shell:true.
const cmdMeta = /([()%!^"<>&|,; *?])/g;
function batchArgument(value: string): string {
  const quoted = '"' + value.replace(/(\\*)"/g, '$1$1\\"').replace(/(\\*)$/, '$1$1') + '"';
  return quoted.replace(cmdMeta, '^$1').replace(cmdMeta, '^$1');
}
export function commandInvocation(argv: readonly string[], env: Environment = process.env, cwd = process.cwd()): {
  file: string; args: string[]; windowsVerbatimArguments?: boolean;
} {
  if (!argv[0] || argv.some(a => a.includes('\0'))) throw new Error('invalid_command');
  if (process.platform !== 'win32') return { file: argv[0], args: argv.slice(1) };
  const file = executable(argv[0], env, cwd);
  if (!/\.(cmd|bat)$/i.test(file)) return { file, args: argv.slice(1) };
  // cmd treats CR/LF as command separators even through some batch wrappers.
  // Protocol payloads belong on stdin or in JSON-escaped argv, never shell lines.
  if (argv.some(arg => /[\r\n]/.test(arg))) throw new Error('batch_argument_newline');
  const shell = join(envValue(env, 'SystemRoot') || 'C:\\Windows', 'System32', 'cmd.exe');
  const line = [file.replace(cmdMeta, '^$1'), ...argv.slice(1).map(batchArgument)].join(' ');
  if (line.length > 8000) throw new Error('input_too_large'); // cmd's 8191-character limit includes its wrapper.
  return { file: shell, args: ['/d', '/s', '/v:off', '/c', '"' + line + '"'], windowsVerbatimArguments: true };
}

/** Kill the owned process tree, not just a cmd/npm wrapper. Unix callers that
 * created a detached group retain their negative-PID termination semantics. */
export function terminateProcess(child: ChildProcess, signal: NodeJS.Signals = 'SIGKILL', group = false): void {
  if (!child.pid) return;
  const exited = child.exitCode !== null || child.signalCode !== null;
  // A Unix process group can outlive its leader. Preserve negative-PID cleanup
  // on the parent's exit event, before descendants release inherited pipes.
  if (exited && !group) return;
  const fallback = () => { if (!exited) try { child.kill(signal); } catch { /* already exited */ } };
  if (process.platform === 'win32') {
    const killer = spawn(join(envValue(process.env, 'SystemRoot') || 'C:\\Windows', 'System32', 'taskkill.exe'),
      ['/pid', String(child.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    killer.once('error', fallback);
    killer.once('close', code => { if (code !== 0) fallback(); });
  } else if (group) {
    try { process.kill(-child.pid, signal); } catch { fallback(); }
  } else fallback();
}
