import { spawn, spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, realpathSync, renameSync, unlinkSync, writeFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { cliEnvironment, commandInvocation, envValue } from '../../../hub-client/src/platform.ts';
import type { Environment } from '../../../hub-client/src/platform.ts';
import { localPlatform } from '../private-files.ts';
import type { Binding, Engine } from './engine.ts';

export interface StatusPublisher {
  publish(now: number, entries: Iterable<{ binding: Binding; engine: Engine }>): void;
  valid(): boolean;
  close(): Promise<void>;
}
interface HeldLock { valid(): boolean; close(): Promise<void> }

/** The helper owns LockFileEx, NOT a stale PID/file sentinel. Stdin EOF (also
 * when Node crashes) releases the lock. Readiness is bounded and exact; an old
 * CLI or malformed output must never make a connector appear ready. */
function windowsLock(path: string, env: Environment, onLost: () => void): Promise<HeldLock | undefined> {
  return new Promise(resolve => {
    let command;
    try { command = commandInvocation([envValue(env, 'VECNET_BIN') || 'vecnet', 'local-platform', 'lock', path], env); }
    catch { resolve(undefined); return; }
    const child = spawn(command.file, command.args, { ...command, env: cliEnvironment(env), windowsHide: true, stdio: ['pipe', 'pipe', 'ignore'] });
    let ready = false, live = true, closing = false, output = '';
    const timeout = setTimeout(() => { child.kill(); resolve(undefined); }, 5000);
    const closed = new Promise<void>(done => {
      child.once('close', () => {
        live = false; clearTimeout(timeout);
        if (!ready) resolve(undefined);
        else if (!closing) onLost();
        done();
      });
    });
    child.once('error', () => { live = false; clearTimeout(timeout); resolve(undefined); });
    child.stdin.on('error', () => {});
    child.stdout.setEncoding('utf8').on('data', (chunk: string) => {
      output += chunk;
      if (output.length > 7 || !'locked\n'.startsWith(output)) { live = false; child.kill(); resolve(undefined); return; }
      if (!ready && output === 'locked\n') {
        ready = true; clearTimeout(timeout);
        resolve({ valid: () => live && !closing, async close() {
          if (closing) { await closed; return; }
          closing = true; child.stdin.end();
          const deadline = setTimeout(() => child.kill(), 2000);
          try { await closed; } finally { clearTimeout(deadline); }
        } });
      }
    });
  });
}

/** Unix retains flock on the inherited open file description between
 * heartbeats. Never unlink the lock inode. Windows uses a held native helper;
 * there is deliberately no insecure lock-file/PID fallback on either OS. */
export async function openStatusPublisher(bindingsPath: string, euid: number, options: { env?: Environment; onLost?: () => void } = {}): Promise<StatusPublisher | undefined> {
  let fd: number | undefined, lock: HeldLock | undefined;
  const env = options.env ?? process.env;
  try {
    if (process.platform === 'win32') localPlatform('mkdir-private', dirname(bindingsPath), { env });
    else mkdirSync(dirname(bindingsPath), { recursive: true, mode: 0o700 });
    let authority: string;
    try { authority = realpathSync(bindingsPath); }
    catch { authority = join(realpathSync(dirname(bindingsPath)), basename(bindingsPath)); }
    const path = authority + '.status.json';
    if (process.platform === 'win32') {
      lock = await windowsLock(path + '.lock', env, options.onLost ?? (() => {}));
      if (!lock) return;
    } else {
      fd = openSync(path + '.lock', constants.O_CREAT | constants.O_RDWR | constants.O_NOFOLLOW | constants.O_NONBLOCK, 0o600);
      const st = fstatSync(fd);
      if (!st.isFile() || st.uid !== euid || (st.mode & 0o077) !== 0) throw new Error('unsafe_lock');
      const result = spawnSync('flock', ['--exclusive', '--nonblock', '3'], { stdio: ['ignore', 'ignore', 'ignore', fd], timeout: 2000 });
      if (result.error || result.status !== 0) throw new Error('lock_unavailable');
      const held = fd;
      lock = { valid: () => true, async close() { closeSync(held); } };
    }
    const held = lock;
    let closed = false, owned: { dev: number; ino: number } | undefined;
    return {
      valid: () => !closed && held.valid(),
      publish(now, entries) {
        if (closed) return;
        if (!held.valid()) throw new Error('status_lock_lost');
        const bindings = [...entries].map(({ binding: b, engine }) => ({
          config_path: b.configPath, profile: b.profile, as: b.as ?? '', codex_home: b.codexHome,
          thread_id: b.threadId, generation: b.generation, state: engine.state(), liveness: engine.liveness(),
        }));
        const data = JSON.stringify({ version: 1, updated_at: new Date(now).toISOString(), bindings });
        if (Buffer.byteLength(data) > 1_048_576) throw new Error('status_too_large');
        if (process.platform === 'win32') localPlatform('write-private', path, { env, input: data + '\n' });
        else {
          const temp = path + '.' + randomUUID() + '.tmp';
          try {
            writeFileSync(temp, data + '\n', { mode: 0o600, flag: 'wx' });
            renameSync(temp, path);
          } finally { try { unlinkSync(temp); } catch {} }
        }
        owned = lstatSync(path);
      },
      async close() {
        if (closed) return;
        closed = true;
        try {
          const current = lstatSync(path);
          // Once ownership is lost another publisher may have replaced it.
          if (held.valid() && owned && current.dev === owned.dev && current.ino === owned.ino) unlinkSync(path);
        } catch {} finally { await held.close(); }
      },
    };
  } catch {
    if (lock) await lock.close();
    else if (fd !== undefined) closeSync(fd);
    return;
  }
}
