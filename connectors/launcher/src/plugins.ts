import { spawn } from 'node:child_process';
import { open, readFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { pathToFileURL } from 'node:url';
import type { Clock, Log } from '../../hub-client/src/ports.ts';
import type { Backend, Plugin, PluginContext } from './backend.ts';
import { ConfigError } from './config.ts';
import type { LauncherConfig } from './config.ts';
import { validateSchema } from './contract/metadata.ts';
import type { Schema } from './contract/metadata.ts';
import type { Ledger } from './ledger.ts';
import { redactText, redactingLog, scrubEnv } from './redact.ts';
import { checkRoute } from './route-check.ts';

const MAX_EXEC_OUTPUT = 16 * 1024 * 1024;

function isPlugin(x: unknown): x is Plugin {
  return !!x && typeof x === 'object' && typeof (x as Plugin).id === 'string' && typeof (x as Plugin).create === 'function';
}
// Relative module paths resolve against src/ (this file's directory), so the
// shipped config's "./backends/t3/index.ts" works from any working directory.
function moduleUrl(spec: string): string {
  if (spec.startsWith('./') || spec.startsWith('../')) return new URL(spec, import.meta.url).href;
  if (isAbsolute(spec)) return pathToFileURL(spec).href;
  return spec;
}

export async function loadBackends(cfg: LauncherConfig, ctxFor: (id: string) => PluginContext): Promise<Map<string, Backend>> {
  const backends = new Map<string, Backend>();
  for (const [i, entry] of cfg.backends.entries()) {
    const p = `/backends/${i}`;
    let mod: Record<string, unknown>;
    try { mod = await import(moduleUrl(entry.module)) as Record<string, unknown>; }
    catch { throw new ConfigError(`${p}/module`, 'cannot load plugin module'); }
    const plugin = isPlugin(mod.default) ? mod.default : Object.values(mod).find(isPlugin);
    if (!plugin) throw new ConfigError(`${p}/module`, 'module exports no plugin');
    if (plugin.id !== entry.id) throw new ConfigError(`${p}/id`, 'plugin id does not match');
    if (plugin.configSchema && typeof plugin.configSchema === 'object') {
      const errors = validateSchema(plugin.configSchema as Schema, entry.config);
      if (errors.length) {
        // The validator's pointer names the field; never echo the value.
        const [pointer, rule] = errors[0].split(': ');
        throw new ConfigError(`${p}/config${pointer === '/' ? '' : pointer}`, rule ?? 'invalid field');
      }
    }
    const backend = await plugin.create(entry.config, ctxFor(entry.id));
    if (!backend || backend.id !== entry.id) throw new ConfigError(`${p}/id`, 'plugin created a backend with another id');
    backends.set(entry.id, backend);
  }
  return backends;
}

async function readSecretFile(path: string): Promise<string> {
  const file = await open(path, 'r');
  try {
    const stat = await file.stat();
    // Owner-only: a group- or world-readable secret is refused, never read.
    if (!stat.isFile() || (stat.mode & 0o077) !== 0) throw new Error('secret file must be a regular file readable only by its owner');
    return await file.readFile('utf8');
  } finally { await file.close(); }
}

export function pluginContext(a: { host: string; log: Log; ledger: Ledger; id: string; clock: Clock }): PluginContext {
  const inner = redactingLog(a.log);
  const log: Log = {
    info: (msg, meta) => inner.info(msg, { ...meta, plugin: a.id }),
    warn: (msg, meta) => inner.warn(msg, { ...meta, plugin: a.id }),
    error: (msg, meta) => inner.error(msg, { ...meta, plugin: a.id }),
  };
  return {
    host: a.host, log, store: a.ledger.kv(`plugin:${a.id}`), readSecretFile,
    now: () => new Date(a.clock.now()),
    sleep: (ms, signal) => a.clock.sleep(ms, signal),
    redact: s => redactText(s).text,
    checkRoute: (route, signal) => checkRoute(route, { readFile }, signal),
    exec(argv, opts) {
      return new Promise((resolve, reject) => {
        if (!argv.length || !argv[0]) { reject(new Error('exec needs a command')); return; }
        if (opts.signal?.aborted) { reject(new Error('exec aborted')); return; }
        const env = scrubEnv({ ...process.env, ...opts.env }, []);
        const child = spawn(argv[0], argv.slice(1), { env, stdio: ['pipe', 'pipe', 'pipe'] });
        const out: Buffer[] = [], err: Buffer[] = [];
        let size = 0, settled = false;
        const kill = () => { try { child.kill('SIGKILL'); } catch { /* already gone */ } };
        const timer = a.clock.timer(opts.timeoutMs, kill);
        const abort = () => kill();
        opts.signal?.addEventListener('abort', abort, { once: true });
        function finish(fn: () => void) {
          if (settled) return;
          settled = true; timer.cancel(); opts.signal?.removeEventListener('abort', abort); fn();
        }
        function collect(into: Buffer[]) {
          return (chunk: Buffer) => {
            size += chunk.length;
            if (size > MAX_EXEC_OUTPUT) { kill(); return; }
            into.push(chunk);
          };
        }
        child.stdout.on('data', collect(out));
        child.stderr.on('data', collect(err));
        // Never surface spawn errors verbatim: they can quote argv.
        child.on('error', () => finish(() => reject(new Error('exec failed'))));
        child.on('close', code => finish(() => resolve({
          code: code ?? -1, stdout: Buffer.concat(out).toString('utf8'), stderr: Buffer.concat(err).toString('utf8'),
        })));
        child.stdin.on('error', () => { /* the child may exit before reading stdin */ });
        child.stdin.end(opts.stdin ?? '');
      });
    },
  };
}
