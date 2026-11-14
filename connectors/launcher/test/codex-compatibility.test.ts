import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { startFakeCodexAppServer } from './fake-codex-appserver.ts';
import { openCodexRpc as openDelivery } from '../src/codex/rpc.ts';
import { openCodexRpc as openLauncher } from '../src/backends/codex-app-server/rpc.ts';
import { codexVersionCompatibility, isCodexVersionSupported, parseServerVersion } from '../src/codex/discover.ts';

test('distinguishes historical verification from provisional admission', () => {
  assert.equal(codexVersionCompatibility('0.166.0'), 'verified');
  assert.equal(codexVersionCompatibility('0.169.0'), 'tentative');
  assert.equal(codexVersionCompatibility('1.0.0'), 'tentative');
  assert.equal(codexVersionCompatibility('0.165.99999999999999999999'), 'unsupported');
  assert.equal(codexVersionCompatibility('0.166.0-alpha.1'), 'unsupported');
  assert.equal(codexVersionCompatibility('0.166.0+build.1'), 'tentative');
  assert.equal(isCodexVersionSupported('99999999999999999999.0.0'), true);
  for (const value of ['', '0.169.0oops', '0.169.0\n', '0.169.0 ', '0.169.0-01', '0.169.0+']) {
    assert.equal(isCodexVersionSupported(value), false, JSON.stringify(value));
  }
  assert.equal(parseServerVersion('codex-tui/0.169.0-alpha.1+build.2 (Linux)'), '0.169.0-alpha.1+build.2');
  assert.equal(parseServerVersion('codex-tui/0.169.0.1 (Linux)'), null);
});

for (const [name, open] of [['delivery', openDelivery], ['launcher', openLauncher]] as const) {
  for (const version of ['0.166.0', '0.167.2', '0.169.0', '0.200.0', '1.0.0', '12.0.0', '0.169.0-alpha.1', '0.169.0+build.2']) {
    test(`${name} admits ${version} and still checks RPC capabilities`, { timeout: 5000 }, async t => {
      const dir = await mkdtemp('/tmp/vn-compat-');
      t.after(() => rm(dir, { recursive: true, force: true }));
      const fake = await startFakeCodexAppServer({ dir, clock: fakeClock() });
      t.after(() => fake.close()); fake.setVersion(version);
      fake.setThread({ id: 'owned', status: { type: 'idle' } });
      const log = memoryLog();
      const rpc = await open({ codexHome: join(dir, 'home'), euid: process.geteuid!(), log, timeoutMs: 1000 });
      t.after(() => rpc.close());
      assert.equal(rpc.version, version);
      assert.deepEqual(log.entries.filter(e => e.msg === 'codex_version_tentative').map(e => e.meta),
        codexVersionCompatibility(version) === 'tentative' ? [{ version }] : []);
      assert.equal((await rpc.call<{ thread: { id: string } }>('thread/read', { threadId: 'owned' }, 1000)).thread.id, 'owned');
      fake.failNext('thread/read', { code: -32601, message: 'method unavailable' });
      await assert.rejects(rpc.call('thread/read', { threadId: 'owned' }, 1000), { code: -32601, ambiguous: false });
      await assert.rejects(rpc.call('not/allowed' as never, {}, 1000), /forbidden_method/);
      assert.equal(fake.requests.some(r => r.method === 'not/allowed'), false);
    });
  }
  for (const version of ['0.165.9', '0.16.9', '0.166.0-alpha.1', '0.169', '0.169.0oops', '0.169.0.1', '00.169.0', '0.169.0-alpha.01', '0.169.0-', '0.169.0+']) {
    test(`${name} refuses old or malformed ${version} before any thread access`, { timeout: 5000 }, async t => {
      const dir = await mkdtemp('/tmp/vn-compat-');
      t.after(() => rm(dir, { recursive: true, force: true }));
      const fake = await startFakeCodexAppServer({ dir, clock: fakeClock() });
      t.after(() => fake.close()); fake.setVersion(version);
      await assert.rejects(open({ codexHome: join(dir, 'home'), euid: process.geteuid!(), log: memoryLog(), timeoutMs: 1000 }), /refused_version/);
      assert.deepEqual(fake.requests.map(r => r.method), ['initialize']);
    });
  }
}
