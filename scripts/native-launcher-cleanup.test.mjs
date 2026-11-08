import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { cleanupNativeSmoke } from './native-launcher-cleanup.mjs';
const confirmed = { installationAttempted: true, installationConfirmed: true, launchAttempted: true, jobResolved: true };
async function fixture(t, fail) {
  const dir = await mkdtemp(join(tmpdir(), 'native-cleanup-fixture-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const credential = join(dir, 'credential'); await writeFile(credential, 'fixture-only', {mode: 0o600});
  const calls = [], ops = Object.fromEntries(['settleJob','verifyUnit','stopUnit','verifyStopped','removeUnit','reloadManager','removePrivateFiles','retain'].map(name => [name, async () => {
    calls.push(name); if (fail === name) throw new Error('injected ' + name);
    if (name === 'removePrivateFiles') await rm(credential);
  }]));
  return {ops, calls, preserved: async () => assert.equal(await readFile(credential, 'utf8'), 'fixture-only')};
}
for (const fail of ['settleJob','verifyUnit','stopUnit','verifyStopped','removeUnit','reloadManager']) {
  test('retains private recovery files when owned teardown fails at ' + fail, async t => {
    const f = await fixture(t, fail);
    await assert.rejects(cleanupNativeSmoke(confirmed, f.ops), /injected/);
    await f.preserved(); assert.ok(!f.calls.includes('removePrivateFiles')); assert.equal(f.calls.at(-1), 'retain');
  });
}
for (const [name, state] of [['partial installation', {...confirmed, installationConfirmed: false, launchAttempted: false, jobResolved: false}],
  ['lost accepted launch response', {...confirmed, jobResolved: false}]]) {
  test('retains private recovery files after ' + name, async t => {
    const f = await fixture(t);
    await assert.rejects(cleanupNativeSmoke(state, f.ops), /unresolved|unconfirmed/);
    await f.preserved(); assert.deepEqual(f.calls, ['retain']);
  });
}
test('removes credentials only after job settlement and proven complete owned service teardown', async t => {
  const f = await fixture(t);
  assert.equal(await cleanupNativeSmoke(confirmed, f.ops), true);
  assert.deepEqual(f.calls, ['settleJob','verifyUnit','stopUnit','verifyStopped','removeUnit','reloadManager','removePrivateFiles']);
});

test('F-HI2 cleanup checks host retirement before cancellation and again before host teardown', async t => {
  for (const boundary of ['before cancellation', 'after settlement']) await t.test(boundary, async t => {
    const f = await fixture(t); let held = boundary === 'before cancellation';
    f.ops.assertHostRetired = () => { if (held) throw new Error('host child retirement unresolved'); };
    const settle = f.ops.settleJob;
    f.ops.settleJob = async () => { await settle(); held = true; };
    await assert.rejects(cleanupNativeSmoke(confirmed, f.ops), /retirement unresolved/);
    await f.preserved();
    assert.deepEqual(f.calls, boundary === 'before cancellation' ? ['retain'] : ['settleJob', 'retain']);
  });
});
