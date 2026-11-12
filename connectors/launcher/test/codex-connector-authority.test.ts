import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { readBindings } from '../src/codex/connector-authority.ts';

test('authority parser requires complete typed records and preserves canonical identity', t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-ca-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const config = join(dir, 'config'), home = join(dir, 'home'), path = join(dir, 'bindings');
  writeFileSync(config, ''); mkdirSync(home);
  symlinkSync(config, join(dir, 'config-alias')); symlinkSync(home, join(dir, 'home-alias'));
  const row = { config_path: join(dir, 'config-alias'), codex_home: join(dir, 'home-alias'),
    generation: 'abc123'.padEnd(32, '0'), thread_id: '00000000-0000-4000-8000-000000000001',
    profile: 'p', caller: 'peer', as: '', hub: 'https://HUB.invalid:443/',
    created_at: '2026-11-01T00:00:00Z', expires_at: '2026-12-01T00:00:00Z', idle_dormant_hours: 0, attended: true };
  const read = (value: unknown) => {
    writeFileSync(path, JSON.stringify(value), { mode: 0o600 });
    return readBindings(path, process.geteuid!());
  };
  const parsed = read({ version: 1, bindings: [row] });
  assert.equal(parsed.state, 'ok');
  if (parsed.state !== 'ok') return;
  assert.deepEqual(parsed.bindings, [{ configPath: config, codexHome: home, generation: row.generation,
    threadId: row.thread_id, profile: 'p', caller: 'peer', hub: 'https://hub.invalid',
    createdAt: Date.parse(row.created_at), expiresAt: Date.parse(row.expires_at), attended: true, origin: 'explicit' }]);
  const secondDir = join(dir, 'second'); mkdirSync(secondDir);
  const secondConfig = join(secondDir, 'config'); writeFileSync(secondConfig, '');
  const other = { ...row, config_path: secondConfig, caller: 'other-peer', thread_id: '00000000-0000-4000-8000-000000000002' };
  assert.equal(read({ version: 1, bindings: [row, other] }).state, 'ok', 'same profile in separate config directories is allowed');
  const collidingConfig = join(dir, 'other-config'); writeFileSync(collidingConfig, '');
  assert.equal(read({ version: 1, bindings: [row, { ...other, config_path: collidingConfig }] }).state, 'invalid', 'different config filenames share the same delivery state directory');
  assert.equal(read({ version: 1, bindings: [row, { ...other, caller: 'peer' }] }).state, 'invalid', 'recipient uniqueness crosses configs');
  for (const extra of [{ as: null }, { generation: null }, { attended: null }, { idle_dormant_hours: null },
    { expires_at: row.created_at }, { created_at: '2026-02-30T00:00:00Z' }]) {
    assert.equal(read({ version: 1, bindings: [{ ...row, ...extra }] }).state, 'invalid', JSON.stringify(extra));
  }
  for (const file of [null, [], { version: 2, bindings: [row] }, { version: 1, bindings: {} },
    { version: 1, bindings: [row, null] }, { version: 1, bindings: Array(1025).fill(row) }]) {
    assert.equal(read(file).state, 'invalid');
  }
  writeFileSync(path, ' '.repeat(1_048_577));
  assert.equal(readBindings(path, process.geteuid!()).state, 'invalid');
});


test('authority rejects duplicate JSON properties including escaped names at every depth', t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-ca-')), path = join(dir, 'bindings');
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  for (const raw of [
    '{"version":2,"version":1,"bindings":[]}',
    '{"version":1,"bindings":null,"bindings":[]}',
    '{"version":1,"bindings":[],"meta":{"owner":"old","owner":"new"}}',
    '{"version":1,"bindings":[],"meta":[{"owner":"old","own\\u0065r":"new"}]}',
    '{"version":1,"bindings":[],"meta":{"__proto__":1,"__proto__":2}}',
  ]) {
    writeFileSync(path, raw, { mode: 0o600 });
    assert.equal(readBindings(path, process.geteuid!()).state, 'invalid', raw);
  }
  writeFileSync(path, JSON.stringify({ version: 1, bindings: [], meta: [
    { name: 'braces {} [] and \"quotes\" : in strings' }, { name: 'same key in different object' },
  ] }));
  assert.equal(readBindings(path, process.geteuid!()).state, 'ok');
});
