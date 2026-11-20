import { accessSync, constants, realpathSync, statSync } from 'node:fs';
import { isAbsolute } from 'node:path';
import { stderrLog, systemClock } from '../daemon.ts';
import { userHome } from '../../../hub-client/src/platform.ts';
import { localPlatform } from '../private-files.ts';
import { codexAuthority, codexRecoveryPath } from './connector.ts';
import { claudeDeliveryCanaryAllowed } from '../delivery/claude-adapter.ts';
import { createClaudeStatus, spawnClaudeSnapshot } from '../delivery/claude-status.ts';
import { startDeliveryHost } from '../delivery/host.ts';
import type { DeliveryHost } from '../delivery/host.ts';

class DeliveryError extends Error {}
const unsafe = () => new DeliveryError('unsafe state path');

async function main(): Promise<void> {
  if (process.argv.length !== 2) throw new DeliveryError('no command-line arguments are supported');
  const uid = process.geteuid?.();
  const selectedHome = userHome(process.env);
  if (!selectedHome || process.platform !== 'win32' && uid === undefined) throw unsafe();
  let bin: string;
  try {
    if (!process.env.VECNET_BIN || !isAbsolute(process.env.VECNET_BIN)) throw new Error();
    bin = realpathSync(process.env.VECNET_BIN);
    const st = statSync(bin);
    if (!st.isFile()) throw new Error();
    if (process.platform === 'win32') {
      if (!/\.(exe|com)$/i.test(bin)) throw new Error();
      localPlatform('check-safe', bin, { env: { ...process.env, VECNET_BIN: bin } });
    } else if ((st.uid !== uid && st.uid !== 0) || (st.mode & 0o022) !== 0) throw new Error();
    accessSync(bin, constants.X_OK);
  } catch { throw new DeliveryError(process.platform === 'win32'
    ? 'VECNET_BIN must be an absolute native executable with a safe owner and DACL'
    : 'VECNET_BIN must be an absolute executable owned by this user or root and not writable by others'); }
  process.umask(0o077);
  let env: NodeJS.ProcessEnv;
  try {
    env = { ...process.env, HOME: realpathSync(selectedHome), VECNET_BIN: bin };
    const authority = codexAuthority(env);
    if (!authority) throw unsafe();
    codexRecoveryPath(env, authority);
  } catch { throw unsafe(); }
  let stop!: () => void;
  const interrupted = new Promise<void>(resolve => { stop = resolve; });
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
  let host: DeliveryHost | undefined;
  try {
    const log = stderrLog();
    host = startDeliveryHost({
      env, clock: systemClock, log,
      // Passive Claude status rides this host's lease; it never elects one.
      // The native-delivery adapter delivers only to the single scope holding
      // the durable per-profile canary record (`claude enable` cannot set
      // it); VECNET_CLAUDE_DELIVERY_CANARY=0 is the kill switch.
      claudeDeliveryCanary: claudeDeliveryCanaryAllowed(env),
      auxiliary: nativeAdmissions => createClaudeStatus({ env, clock: systemClock, log, fetchSnapshot: spawnClaudeSnapshot(env), nativeAdmissions }),
    });
    await Promise.race([interrupted, host.stopped]);
  } finally {
    try { await host?.stop(); }
    finally { process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop); }
  }
  // Recheck after stop so a signal racing lock loss cannot report success.
  if ((await host.stopped).reason === 'status_lock_lost') throw new DeliveryError('status lock lost');
}

main().then(() => { process.exitCode = 0; }, error => {
  process.stderr.write(`codex delivery: ${error instanceof DeliveryError ? error.message : 'startup or shutdown failed'}\n`);
  process.exitCode = 1;
});
