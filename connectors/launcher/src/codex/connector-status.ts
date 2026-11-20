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
export interface DeliveryLease { valid(): boolean; close(): Promise<void> }

// Bind each real lease to the authority it locked, even if a pathname changes
// between election and publisher construction.
const leaseStatusPaths = new WeakMap<DeliveryLease, string>();

/** The helper owns LockFileEx, NOT a stale PID/file sentinel. Stdin EOF (also
 * when Node crashes) releases the lock. Readiness is bounded and exact; an old
 * CLI or malformed output must never make a connector appear ready. */
function windowsLock(path: string, env: Environment, onLost: () => void): Promise<DeliveryLease | undefined> {
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
export async function openDeliveryLease(bindingsPath: string, euid: number, options: { env?: Environment; onLost?: () => void; pinnedBindings?: boolean } = {}): Promise<DeliveryLease | undefined> {
  let fd: number | undefined, lock: DeliveryLease | undefined;
  const env = options.env ?? process.env;
  try {
    if (process.platform === 'win32') localPlatform('mkdir-private', dirname(bindingsPath), { env });
    else mkdirSync(dirname(bindingsPath), { recursive: true, mode: 0o700 });
    // The host already validated a canonical directory and fixed bindings leaf.
    // Never resolve that leaf again when deriving its lock and status paths.
    let authority = bindingsPath;
    if (!options.pinnedBindings) {
      try { authority = realpathSync(bindingsPath); }
      catch { authority = join(realpathSync(dirname(bindingsPath)), basename(bindingsPath)); }
    }
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
      let closed = false;
      lock = { valid: () => !closed, async close() { if (!closed) { closed = true; closeSync(held); } } };
    }
    leaseStatusPaths.set(lock, path);
    return lock;
  } catch {
    if (lock) await lock.close();
    else if (fd !== undefined) closeSync(fd);
    return;
  }
}

/** Internal writer for an elected connector. Closing it never releases the lease. */
export function createStatusPublisher(bindingsPath: string, lease: DeliveryLease, env: Environment): StatusPublisher {
  const path = leaseStatusPaths.get(lease) ?? bindingsPath + '.status.json';
  let closed = false, owned: { dev: bigint; ino: bigint } | undefined;
  return {
    valid: () => !closed && lease.valid(),
    publish(now, entries) {
      if (closed) return;
      if (!lease.valid()) throw new Error('status_lock_lost');
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
      owned = lstatSync(path, { bigint: true });
    },
    async close() {
      if (closed) return;
      closed = true;
      if (!owned || !lease.valid()) return;
      try {
        // NTFS file IDs are uint64; number Stats can alias distinct owners.
        const current = lstatSync(path, { bigint: true });
        if (current.dev === owned.dev && current.ino === owned.ino) unlinkSync(path);
      } catch {}
    },
  };
}

/** Compatibility entry for callers that own both the writer and the lease. */
export async function openStatusPublisher(bindingsPath: string, euid: number, options: { env?: Environment; onLost?: () => void } = {}): Promise<StatusPublisher | undefined> {
  const lease = await openDeliveryLease(bindingsPath, euid, options);
  if (!lease) return;
  const publisher = createStatusPublisher(bindingsPath, lease, options.env ?? process.env);
  let closing: Promise<void> | undefined;
  return { ...publisher, close: () => closing ??= publisher.close().finally(() => lease.close()) };
}
