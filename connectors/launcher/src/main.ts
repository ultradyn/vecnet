import { ConfigError, loadConfig } from './config.ts';
import type { LauncherConfig } from './config.ts';
import { startDaemon, stderrLog, systemClock } from './daemon.ts';
import { openLedger } from './ledger.ts';
import { loadBackends, pluginContext } from './plugins.ts';
import { redactingLog } from './redact.ts';

const USAGE = 'usage: main.ts run --config PATH | main.ts check --config PATH';

function fail(message: string, code: number): number {
  process.stderr.write(`${message}\n`);
  return code;
}

/** Load the plugins, print each backend's health and presets as JSON; 1 if any is not ok. */
async function check(cfg: LauncherConfig): Promise<number> {
  const log = redactingLog(stderrLog());
  const ledger = openLedger(':memory:', systemClock);
  try {
    let backends;
    try { backends = await loadBackends(cfg, id => pluginContext({ host: cfg.host, log, ledger, id, clock: systemClock })); }
    catch (err) { return fail(err instanceof ConfigError ? err.message : 'cannot load backend plugins', 2); }
    const report: Array<Record<string, unknown>> = [];
    for (const [id, backend] of backends) {
      let health: { ok: boolean; version: string; degraded?: string[] } = { ok: false, version: 'unknown' };
      let presets: string[] = [], modes: string[] = [];
      try { const h = await backend.health(); health = { ok: h.ok === true, version: h.version, ...(h.degraded?.length ? { degraded: h.degraded } : {}) }; }
      catch { health = { ok: false, version: 'unknown', degraded: ['health check failed'] }; }
      try { const caps = await backend.capabilities(); presets = caps.presets.map(p => p.id); modes = [...caps.permissionModes]; }
      catch { /* reported as no presets */ }
      report.push({ id, ...health, presets, permission_modes: modes });
    }
    process.stdout.write(`${JSON.stringify({ host: cfg.host, backends: report }, null, 2)}\n`);
    return report.every(b => b.ok === true) ? 0 : 1;
  } finally { ledger.close(); }
}

async function main(argv: string[]): Promise<number> {
  const [command, ...rest] = argv;
  let path: string | undefined;
  for (let i = 0; i < rest.length; i++) {
    if (rest[i] === '--config' && i + 1 < rest.length) path = rest[++i];
    else if (rest[i].startsWith('--config=')) path = rest[i].slice('--config='.length);
    else return fail(USAGE, 2);
  }
  if ((command !== 'run' && command !== 'check') || !path) return fail(USAGE, 2);
  let cfg: LauncherConfig;
  // loadConfig refuses a credential file that is not mode 0600.
  try { cfg = await loadConfig(path); }
  catch (err) { return fail(err instanceof ConfigError ? err.message : 'cannot read config', 2); }
  if (command === 'check') return check(cfg);
  const daemon = await startDaemon({ cfg });
  await new Promise<void>(resolve => {
    process.once('SIGTERM', () => resolve());
    process.once('SIGINT', () => resolve());
  });
  await daemon.stop();
  return 0;
}

main(process.argv.slice(2)).then(code => { process.exitCode = code; }, () => {
  // Never print the error: it can quote config or credential paths' contents.
  process.stderr.write('launcher failed to start\n');
  process.exitCode = 1;
});
