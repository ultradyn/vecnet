import { writePrivate, makePublicReadable, makeOwnerPrivate } from './fixtures/platform.ts';
import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { PluginContext } from '../src/backend.ts';
import { ConfigError } from '../src/config.ts';
import { openLedger } from '../src/ledger.ts';
import { loadBackends, pluginContext } from '../src/plugins.ts';
import { testConfig } from './daemon-harness.ts';

function tmp(t: { after(fn: () => void): void }) {
  const dir = mkdtempSync(join(tmpdir(), 'launcher-plugins-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

test('loadBackends imports the fake plugin by module path, awaits create, and refuses a config missing a required key', async t => {
  const dir = tmp(t);
  const clock = fakeClock(0), log = memoryLog(), ledger = openLedger(':memory:', clock);
  t.after(() => ledger.close());
  const contexts: string[] = [];
  const ctxFor = (id: string): PluginContext => { contexts.push(id); return pluginContext({ host: 'h', log, ledger, id, clock }); };
  // Relative module paths resolve against src/.
  const loaded = await loadBackends(testConfig(), ctxFor);
  assert.deepEqual([...loaded.keys()], ['fake']);
  assert.equal(loaded.get('fake')!.id, 'fake');
  assert.equal((await loaded.get('fake')!.health()).ok, true);
  assert.deepEqual(contexts, ['fake']);

  // An absolute module whose create resolves later: the Map holds the created backend.
  const slow = join(dir, 'slow-plugin.mjs');
  writeFileSync(slow, `export default { id: 'slow', configSchema: { type: 'object', additionalProperties: false, required: ['bin'], properties: { bin: { type: 'string', minLength: 1 } } },
    async create(config, ctx) { await new Promise(r => setTimeout(r, 20)); globalThis.__slowCreated = (globalThis.__slowCreated ?? 0) + 1;
      return { id: 'slow', bin: config.bin, host: ctx.host, async health() { return { ok: true, version: '1' }; } }; } };\n`);
  const created = () => (globalThis as { __slowCreated?: number }).__slowCreated ?? 0;
  const good = await loadBackends(testConfig({ backends: [{ id: 'slow', module: slow, config: { bin: '/bin/true' } }] }), ctxFor);
  assert.equal(created(), 1);
  assert.equal((good.get('slow') as unknown as { bin: string; host: string }).bin, '/bin/true');
  assert.equal((good.get('slow') as unknown as { host: string }).host, 'h');

  await assert.rejects(loadBackends(testConfig({ backends: [{ id: 'slow', module: slow, config: {} }] }), ctxFor),
    (err: unknown) => err instanceof ConfigError && err.pointer === '/backends/0/config/bin');
  await assert.rejects(loadBackends(testConfig({ backends: [{ id: 'slow', module: slow, config: { bin: '/bin/true', extra: 1 } }] }), ctxFor),
    (err: unknown) => err instanceof ConfigError && err.pointer === '/backends/0/config/extra');
  // A real plugin's schema is enforced before create.
  await assert.rejects(loadBackends(testConfig({ backends: [{ id: 't3', module: './backends/t3/index.ts', config: { bridge_bin: '/bin/t3-bridge', presets: {} } }] }), ctxFor),
    (err: unknown) => err instanceof ConfigError && err.pointer === '/backends/0/config/caller');
  await assert.rejects(loadBackends(testConfig({ backends: [{ id: 'other', module: slow, config: { bin: 'x' } }] }), ctxFor),
    (err: unknown) => err instanceof ConfigError && err.pointer === '/backends/0/id');
  await assert.rejects(loadBackends(testConfig({ backends: [{ id: 'gone', module: join(dir, 'missing.mjs'), config: {} }] }), ctxFor),
    (err: unknown) => err instanceof ConfigError && err.pointer === '/backends/0/module');
  assert.equal(created(), 1);
});

test('pluginContext: exec scrubs VECNET_* and *_API_KEY from the child env; redact and checkRoute are wired; readSecretFile refuses mode 0644', async t => {
  const dir = tmp(t);
  const clock = fakeClock(Date.parse('2026-11-05T00:00:00Z')), log = memoryLog(), ledger = openLedger(':memory:', clock);
  t.after(() => ledger.close());
  const ctx = pluginContext({ host: 'xsm', log, ledger, id: 'fake', clock });
  const saved = { ...process.env };
  t.after(() => { for (const k of ['VECNET_CREDENTIAL', 'OPENAI_API_KEY', 'LAUNCHER_PLAIN']) { if (saved[k] === undefined) delete process.env[k]; else process.env[k] = saved[k]; } });
  process.env.VECNET_CREDENTIAL = 'vn_secret';
  process.env.OPENAI_API_KEY = 'sk-secret';
  process.env.LAUNCHER_PLAIN = 'plain';
  const result = await ctx.exec([process.execPath, '-e', 'process.stdin.pipe(process.stdout); process.stdin.on("end", () => process.stderr.write(JSON.stringify(process.env)))'],
    { timeoutMs: 30_000, stdin: 'hello', env: { EXTRA_API_KEY: 'x', EXTRA_OK: '1' } });
  assert.equal(result.code, 0);
  assert.equal(result.stdout, 'hello');
  const env = JSON.parse(result.stderr) as Record<string, string>;
  assert.equal(env.VECNET_CREDENTIAL, undefined);
  assert.equal(env.OPENAI_API_KEY, undefined);
  assert.equal(env.EXTRA_API_KEY, undefined);
  assert.equal(env.LAUNCHER_PLAIN, 'plain');
  assert.equal(env.EXTRA_OK, '1');
  assert.equal((await ctx.exec([process.execPath, '-e', 'process.exit(3)'], { timeoutMs: 30_000 })).code, 3);
  await assert.rejects(ctx.exec([join(dir, 'no-such-binary')], { timeoutMs: 30_000 }), /exec failed/);

  assert.equal(ctx.redact('Authorization: Bearer abc.def'), 'Authorization: Bearer [redacted]');
  assert.deepEqual(await ctx.checkRoute({ base_url: 'http://example.com:8080/v1' }), { ok: false, models: [], detail: 'not_loopback' });
  const seen: string[] = [];
  const server = createServer((req, res) => {
    seen.push(`${req.url} ${req.headers.authorization}`);
    res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ data: [{ id: 'model-a' }] }));
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise<void>(resolve => server.close(() => resolve())));
  const port = (server.address() as { port: number }).port;
  const key = join(dir, 'route.key');
  writePrivate(key, 'route-key\n');
  assert.deepEqual(await ctx.checkRoute({ base_url: `http://127.0.0.1:${port}/v1`, key_file: key }), { ok: true, models: ['model-a'] });
  assert.deepEqual(seen, ['/v1/models Bearer route-key']);

  const secret = join(dir, 'secret');
  writePrivate(secret, 'token-value');
  makePublicReadable(secret);
  await assert.rejects(ctx.readSecretFile(secret), /readable only by its owner/);
  if (process.platform !== 'win32') chmodSync(secret, 0o640);
  await assert.rejects(ctx.readSecretFile(secret), /readable only by its owner/);
  makeOwnerPrivate(secret);
  assert.equal(await ctx.readSecretFile(secret), 'token-value');

  ctx.store.set('k', 'v');
  assert.equal(ledger.kv('plugin:fake').get('k'), 'v');
  assert.equal(ctx.now().toISOString(), '2026-11-05T00:00:00.000Z');
  ctx.log.warn('note', { token: 'Bearer zzz', detail: 'Bearer zzz' });
  assert.doesNotMatch(log.text(), /zzz/);
  assert.equal(ctx.host, 'xsm');
});

test('pluginContext.sleep runs on the daemon Clock, never a real timer', async t => {
  const clock = fakeClock(Date.parse('2026-11-05T00:00:00Z')), ledger = openLedger(':memory:', clock);
  t.after(() => ledger.close());
  const ctx = pluginContext({ host: 'xsm', log: memoryLog(), ledger, id: 't3', clock });
  let woke = false;
  const sleeping = ctx.sleep(3000).then(() => { woke = true; });
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(woke, false);
  clock.advance(3000);
  await sleeping;
  assert.equal(woke, true);
});
