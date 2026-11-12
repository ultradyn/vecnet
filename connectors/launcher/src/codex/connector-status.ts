import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, realpathSync, renameSync, unlinkSync, writeFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import type { Binding, Engine } from './engine.ts';

export interface StatusPublisher {
  publish(now: number, entries: Iterable<{ binding: Binding; engine: Engine }>): void;
  close(): void;
}

/** Hold an OS flock on the inherited open file description, including between
 * heartbeats. Never unlink this lock inode: that would permit two owners. A crash
 * closes the descriptor; a following process can immediately acquire the lock.
 * A platform without flock cannot safely publish readiness or start engines. */
export function openStatusPublisher(bindingsPath: string, euid: number): StatusPublisher | undefined {
  let fd: number | undefined;
  try {
    mkdirSync(dirname(bindingsPath), { recursive: true, mode: 0o700 });
    let authority: string;
    try { authority = realpathSync(bindingsPath); }
    catch { authority = join(realpathSync(dirname(bindingsPath)), basename(bindingsPath)); }
    const path = authority + '.status.json';
    fd = openSync(path + '.lock', constants.O_CREAT | constants.O_RDWR | constants.O_NOFOLLOW | constants.O_NONBLOCK, 0o600);
    const st = fstatSync(fd);
    if (!st.isFile() || st.uid !== euid || (st.mode & 0o077) !== 0) throw new Error('unsafe_lock');
    const result = spawnSync('flock', ['--exclusive', '--nonblock', '3'], { stdio: ['ignore', 'ignore', 'ignore', fd], timeout: 2000 });
    if (result.error || result.status !== 0) throw new Error('lock_unavailable');
    const held = fd;
    let closed = false, owned: { dev: number; ino: number } | undefined;
    return {
      publish(now, entries) {
        if (closed) return;
        const bindings = [...entries].map(({ binding: b, engine }) => ({
          config_path: b.configPath, profile: b.profile, as: b.as ?? '', codex_home: b.codexHome,
          thread_id: b.threadId, generation: b.generation, state: engine.state(), liveness: engine.liveness(),
        }));
        const data = JSON.stringify({ version: 1, updated_at: new Date(now).toISOString(), bindings });
        if (Buffer.byteLength(data) > 1_048_576) throw new Error('status_too_large');
        const temp = path + '.' + randomUUID() + '.tmp';
        try {
          writeFileSync(temp, data + '\n', { mode: 0o600, flag: 'wx' });
          renameSync(temp, path);
          owned = lstatSync(path);
        } finally { try { unlinkSync(temp); } catch {} }
      },
      close() {
        if (closed) return;
        closed = true;
        try {
          const current = lstatSync(path);
          if (owned && current.dev === owned.dev && current.ino === owned.ino) unlinkSync(path);
        } catch {} finally { closeSync(held); }
      },
    };
  } catch {
    if (fd !== undefined) closeSync(fd);
    return;
  }
}
