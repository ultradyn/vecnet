import { accessSync, closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, realpathSync, statSync } from 'node:fs';
import { isAbsolute, join } from 'node:path';
import { stderrLog, systemClock } from '../daemon.ts';
import { openLedger } from '../ledger.ts';
import { startCodexConnector } from './connector.ts';
import { codexBindingsPath } from './connector.ts';

class DeliveryError extends Error {}
const unsafe = () => new DeliveryError('unsafe state path');

/** Check every descendant before SQLite can open its database or sidecars.
 * HOME is canonicalized, but state-directory symlinks are never followed. */
function statePath(home: string, uid: number): string {
  const checkDir = (path: string, privateDir: boolean) => {
    const st = lstatSync(path);
    if (!st.isDirectory() || st.isSymbolicLink() || st.uid !== uid || (st.mode & (privateDir ? 0o077 : 0o022)) !== 0) throw unsafe();
  };
  checkDir(home, false);
  let path = home;
  for (const component of ['.local', 'state', 'vecnet', 'codex']) {
    path = join(path, component);
    try { mkdirSync(path, { mode: 0o700 }); }
    catch (e) { if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e; }
    checkDir(path, component === 'codex');
  }
  const database = join(path, 'delivery.sqlite');
  for (const file of [database, database + '-wal', database + '-shm', database + '-journal']) {
    try {
      const st = lstatSync(file);
      if (!st.isFile() || st.isSymbolicLink() || st.uid !== uid || (st.mode & 0o077) !== 0 || st.nlink !== 1) throw unsafe();
    } catch (e) { if ((e as NodeJS.ErrnoException).code !== 'ENOENT') throw e; }
  }
  const fd = openSync(database, constants.O_CREAT | constants.O_RDWR | constants.O_NOFOLLOW | constants.O_NONBLOCK, 0o600);
  try {
    const st = fstatSync(fd);
    if (!st.isFile() || st.uid !== uid || (st.mode & 0o077) !== 0 || st.nlink !== 1) throw unsafe();
  } finally { closeSync(fd); }
  return database;
}

async function main(): Promise<void> {
  if (process.argv.length !== 2) throw new DeliveryError('no command-line arguments are supported');
  const uid = process.geteuid?.();
  if (uid === undefined || !process.env.HOME || !isAbsolute(process.env.HOME)) throw unsafe();
  let bin: string;
  try {
    if (!process.env.VECNET_BIN || !isAbsolute(process.env.VECNET_BIN)) throw new Error();
    bin = realpathSync(process.env.VECNET_BIN);
    const st = statSync(bin);
    if (!st.isFile() || (st.uid !== uid && st.uid !== 0) || (st.mode & 0o022) !== 0) throw new Error();
    accessSync(bin, constants.X_OK);
  } catch { throw new DeliveryError('VECNET_BIN must be an absolute executable owned by this user or root and not writable by others'); }
  process.umask(0o077);
  let home: string, database: string;
  try {
    home = realpathSync(process.env.HOME);
    database = statePath(home, uid);
  } catch { throw unsafe(); }
  const env = { ...process.env, HOME: home, VECNET_BIN: bin };
  if (!codexBindingsPath(env)) throw unsafe();
  const ledger = openLedger(database, systemClock);
  let stop!: () => void;
  const interrupted = new Promise<void>(resolve => { stop = resolve; });
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
  let connector: ReturnType<typeof startCodexConnector> | undefined;
  try {
    connector = startCodexConnector({ env, clock: systemClock, log: stderrLog(), kv: ledger.kv('codex') });
    await interrupted;
  } finally {
    try { await connector?.stop(); }
    finally { ledger.close(); process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop); }
  }
}

main().then(() => { process.exitCode = 0; }, error => {
  process.stderr.write(`codex delivery: ${error instanceof DeliveryError ? error.message : 'startup or shutdown failed'}\n`);
  process.exitCode = 1;
});
