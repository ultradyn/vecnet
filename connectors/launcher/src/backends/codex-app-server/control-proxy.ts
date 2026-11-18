import { spawn } from 'node:child_process';
import { readdirSync, realpathSync, statSync } from 'node:fs';
import { delimiter, isAbsolute, join } from 'node:path';
import { Duplex } from 'node:stream';
import { cliEnvironment, commandInvocation, localPlatform, terminateProcess } from '../../backend.ts';
import { connectStreamWebSocket } from './ws.ts';
import type { WsConn, WsOptions } from './ws.ts';

function value(env: NodeJS.ProcessEnv, key: string): string | undefined {
  return env[key] ?? Object.entries(env).reverse().find(([k]) => k.toUpperCase() === key.toUpperCase())?.[1];
}
/** Resolve a native binary, never a .cmd shim/shell or an arbitrary new server.
 * Prefer the running daemon's managed release; an explicit binary is useful
 * for installations outside the usual npm/managed layouts. */
export function nativeCodexBinary(home: string, env: NodeJS.ProcessEnv = process.env): string {
  const configured = value(env, 'VECNET_CODEX_BIN');
  const candidates: string[] = [];
  if (configured) {
    if (!isAbsolute(configured) || !/\.exe$/i.test(configured)) throw new Error('codex_native_binary_required');
    candidates.push(configured);
  } else {
    const roots = new Set([home]);
    // A custom CODEX_HOME selects state/socket, NOT the native installation.
    // Services intentionally have a minimal PATH and may omit APPDATA.
    for (const user of [value(env, 'HOME'), value(env, 'USERPROFILE')]) if (user && isAbsolute(user)) roots.add(join(user, '.codex'));
    for (const root of roots) {
      const releases = join(root, 'packages', 'app-server-daemon', 'releases');
      try {
        const architecture = process.arch === 'arm64' ? 'aarch64' : 'x86_64';
        const names = readdirSync(releases).filter(n => new RegExp(`^\\d+\\.\\d+\\.\\d+-${architecture}-pc-windows-msvc$`).test(n));
        names.sort((a, b) => { const x = a.split('-')[0].split('.').map(Number), y = b.split('-')[0].split('.').map(Number); return y[0] - x[0] || y[1] - x[1] || y[2] - x[2]; });
        for (const name of names) candidates.push(join(releases, name, 'bin', 'codex.exe'));
      } catch { /* no managed installation */ }
    }
    const arch = process.arch === 'arm64' ? 'arm64' : 'x64', triple = arch === 'arm64' ? 'aarch64' : 'x86_64';
    const roaming = new Set<string>();
    const appdata = value(env, 'APPDATA'); if (appdata) roaming.add(appdata);
    for (const user of [value(env, 'HOME'), value(env, 'USERPROFILE')]) if (user && isAbsolute(user)) roaming.add(join(user, 'AppData', 'Roaming'));
    for (const appdata of roaming) {
      const npm = join(appdata, 'npm', 'node_modules', '@openai');
      candidates.push(join(npm, 'codex', 'node_modules', '@openai', `codex-win32-${arch}`, 'vendor', `${triple}-pc-windows-msvc`, 'codex', 'codex.exe'));
      candidates.push(join(npm, 'codex', 'vendor', `${triple}-pc-windows-msvc`, 'codex', 'codex.exe'));
    }
    for (const dir of (value(env, 'PATH') ?? '').split(delimiter)) if (dir) candidates.push(join(dir.replace(/^"(.*)"$/, '$1'), 'codex.exe'));
  }
  for (const path of candidates) {
    try {
      if (!statSync(path).isFile()) continue;
      const binary = realpathSync(path);
      localPlatform('check-safe', binary, { env });
      return binary;
    } catch { /* missing or untrusted binaries cannot be executed */ }
  }
  throw new Error('codex_native_binary_unavailable');
}

/** Codex's built-in proxy connects to the EXISTING control socket. It does not
 * start a daemon and can attach to the exact loaded root. Never replace this
 * with `app-server --listen stdio://`, which owns a different server. */
export function connectControlProxy(home: string, env: NodeJS.ProcessEnv = process.env, options: WsOptions = { maxMessageBytes: 4 * 1024 * 1024, timeoutMs: 5000 }): Promise<WsConn> {
  const socket = join(home, 'app-server-control', 'app-server-control.sock');
  // Windows' AF_UNIX rendezvous is a special reparse entry (not a Node socket
  // or symlink). Validate its private container natively, not POSIX mode bits.
  localPlatform('check-safe', home, { env, directory: true });
  localPlatform('check-safe', join(home, 'app-server-control'), { env, directory: true });
  // Node lstat returns EACCES for Windows AF_UNIX reparse entries. Let the
  // native proxy open the rendezvous; it exits (never starts a server) if absent.
  // The validated parent and HTTP Upgrade/initialize remain the trust boundary.
  const binary = nativeCodexBinary(home, env);
  const childEnv = cliEnvironment(env);
  childEnv.CODEX_HOME = home;
  const command = commandInvocation([binary, 'app-server', 'proxy', '--sock', socket], childEnv);
  const child = spawn(command.file, command.args, { ...command, env: childEnv, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  // Proxy relays raw RFC6455 bytes, NOT JSONL. The same Upgrade, masked
  // frames, UTF-8 validation, ping/pong, limits and cancellation run on both OSes.
  const tunnel = Duplex.from({ readable: child.stdout, writable: child.stdin });
  child.stderr.resume();
  child.once('spawn', () => tunnel.emit('connect'));
  child.once('error', () => tunnel.destroy(new Error('codex_proxy_failed')));
  child.once('exit', () => {
    tunnel.destroy();
    // Duplex destruction alone can leave the Windows stdout pipe open after
    // process exit, preventing ChildProcess.close and the owner's exact join.
    // The transport is fenced now; release its underlying readable explicitly.
    child.stdout.destroy();
  });
  tunnel.once('close', () => terminateProcess(child));
  return connectStreamWebSocket(tunnel, options);
}
