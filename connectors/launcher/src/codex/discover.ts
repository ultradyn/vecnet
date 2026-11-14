import { lstatSync, readlinkSync, realpathSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

export { CODEX_MIN_APPSERVER_VERSION, CODEX_VERIFIED_APPSERVER_VERSIONS, codexVersionCompatibility, isCodexVersionSupported, parseServerVersion } from '../backends/codex-app-server/compatibility.ts';
export function socketPathFor(codexHome: string): string {
  return join(codexHome, 'app-server-control', 'app-server-control.sock');
}
export type SocketCheck = { ok: true; target: string } | {
  ok: false; reason: 'missing' | 'not_symlink' | 'bad_dir_mode' | 'foreign_owner';
};

/** Inspect only the supplied rendezvous path; never start or probe a daemon. */
export function verifySocket(path: string, euid: number): SocketCheck {
  try {
    if (!lstatSync(path).isSymbolicLink()) return { ok: false, reason: 'not_symlink' };
    // Resolve relative and chained symlinks before checking the actual socket's
    // parent, so a safe-looking intermediate directory cannot hide a public one.
    const target = realpathSync(resolve(dirname(path), readlinkSync(path)));
    const dir = statSync(dirname(target)), socket = statSync(target);
    if (dir.uid !== euid || socket.uid !== euid) return { ok: false, reason: 'foreign_owner' };
    if (!dir.isDirectory() || (dir.mode & 0o7777) !== 0o700) return { ok: false, reason: 'bad_dir_mode' };
    if (!socket.isSocket()) return { ok: false, reason: 'missing' };
    return { ok: true, target };
  } catch {
    // Missing, dangling and inaccessible rendezvous paths all fail closed.
    return { ok: false, reason: 'missing' };
  }
}
