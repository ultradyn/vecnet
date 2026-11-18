// Test-only subprocess boundary. Native security/LockFileEx helpers are NEVER
// replaced: only the hub CLI and binary WS transport use disposable fixtures.
import childProcess from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import { writeFileSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';

export function fixtureSpawn(spawn, { pipe, onLock = () => {} }) {
  const proxy = fileURLToPath(new URL('./fake-codex-proxy.mjs', import.meta.url));
  const vecnet = new URL('../fake-vecnet.ts', import.meta.url).href;
  const cli = `
    import { readFileSync, appendFileSync } from 'node:fs';
    process.env.FAKE_VECNET_PLAN = process.env.VECNET_CONFIG;
    if (process.argv.includes('report-status')) {
      const plan = JSON.parse(readFileSync(process.env.VECNET_CONFIG, 'utf8'));
      appendFileSync(plan.trace, JSON.stringify({ report: JSON.parse(readFileSync(0, 'utf8')) }) + '\\n');
    } else await import(${JSON.stringify(vecnet)});
  `;
  let proxyIndex = 0;
  return function (...args) {
    const argv = args[1], options = args[2];
    if (argv[0] === 'app-server') {
      if (argv[1] !== 'proxy' || argv[2] !== '--sock' || argv.length !== 4) throw new Error('standalone app-server forbidden');
      const child = spawn(process.execPath, [proxy], { ...options, env: { ...options.env, FAKE_CODEX_PROXY_PIPE: pipe } });
      const index = proxyIndex++;
      const snapshot = event => console.log('RECOVERY_PROXY_STREAM', JSON.stringify({index, event, exitCode: child.exitCode, signalCode: child.signalCode, streams: child.stdio.slice(0, 3).map(stream => stream && Object.fromEntries(['destroyed', 'readableEnded', 'writableFinished', 'closed'].map(key => [key, Reflect.get(stream, key) ?? null])))}));
      for (const [streamIndex, stream] of child.stdio.slice(0, 3).entries()) if (stream) {
        for (const event of ['close', 'end', 'finish']) stream.on(event, () => snapshot(String(streamIndex) + ':' + event));
      }
      child.on('exit', () => snapshot('exit')); child.on('close', () => snapshot('close'));
      return child;
    }
    if (argv[0] === '--net') return spawn(process.execPath, ['--input-type=module', '-e', cli, '--', 'vecnet-fixture', ...argv], options);
    const child = Reflect.apply(spawn, childProcess, args);
    if (argv[0] === 'local-platform' && argv[1] === 'lock') onLock(child);
    return child;
  };
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
  const pidFile = process.env.FAKE_LOCK_PID_FILE;
  childProcess.spawn = fixtureSpawn(childProcess.spawn, { pipe: process.env.FAKE_CODEX_PROXY_PIPE,
    onLock: child => writeFileSync(pidFile, String(child.pid)) });
  syncBuiltinESMExports();
  // Speed up only the connector's reload tick, not timeouts or engine clocks.
  const { systemClock } = await import('../../src/daemon.ts');
  const timer = systemClock.timer;
  systemClock.timer = (ms, fn) => timer(ms === 10_000 ? 200 : ms, fn);
  // IPC does not keep a fatally stopped runner alive. Request a real graceful
  // JS signal for the replacement (Windows TerminateProcess is not SIGINT).
  process.on('message', message => {
    if (message === 'stop') { process.disconnect(); process.emit('SIGINT'); }
  });
  process.channel?.unref();
  await import('../../src/codex/delivery-main.ts');
}
