import { accessSync, constants, realpathSync, statSync } from 'node:fs';
import { isAbsolute } from 'node:path';
import { stderrLog, systemClock } from '../daemon.ts';
import { codexBindingsPath, codexRecoveryPath, startCodexConnector } from './connector.ts';

class DeliveryError extends Error {}
const unsafe = () => new DeliveryError('unsafe state path');

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
  let home: string;
  try {
    home = realpathSync(process.env.HOME);
    codexRecoveryPath({ ...process.env, HOME: home });
  } catch { throw unsafe(); }
  const env = { ...process.env, HOME: home, VECNET_BIN: bin };
  if (!codexBindingsPath(env)) throw unsafe();
  let stop!: () => void;
  const interrupted = new Promise<void>(resolve => { stop = resolve; });
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
  let connector: ReturnType<typeof startCodexConnector> | undefined;
  try {
    connector = startCodexConnector({ env, clock: systemClock, log: stderrLog() });
    await interrupted;
  } finally {
    try { await connector?.stop(); }
    finally { process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop); }
  }
}

main().then(() => { process.exitCode = 0; }, error => {
  process.stderr.write(`codex delivery: ${error instanceof DeliveryError ? error.message : 'startup or shutdown failed'}\n`);
  process.exitCode = 1;
});
