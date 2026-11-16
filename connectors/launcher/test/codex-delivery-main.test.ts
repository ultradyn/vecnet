import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { openLedger } from '../src/ledger.ts';
import { systemClock } from '../src/daemon.ts';

const entry = fileURLToPath(new URL('../src/codex/delivery-main.ts', import.meta.url));
function launch(home: string, bin: string) {
  const child = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', entry], {
    env: { HOME: home, PATH: process.env.PATH, VECNET_BIN: bin }, stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stderr = ''; child.stderr.on('data', part => { stderr += part; });
  const exited = once(child, 'exit');
  return { child, exited, stderr: () => stderr };
}
async function eventually(fn: () => boolean, what: string) {
  for (let i = 0; i < 500; i++) { if (fn()) return; await delay(10); }
  assert.ok(fn(), what);
}

test('standalone delivery runner shuts down cleanly and recovers durable codex state on restart', { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
  const home = mkdtempSync(join(tmpdir(), 'vn-runner-'));
  const state = join(home, '.local', 'state', 'vecnet', 'codex'), db = join(state, 'delivery.sqlite');
  const authority = join(state, 'bindings.json'), status = authority + '.status.json', bin = join(home, 'vecnet'), trace = join(home, 'calls');
  writeFileSync(bin, `#!/bin/sh\nprintf '%s\\n' "$*" >> '${trace}'\nprintf '%s\\n' '{"released":1}'\n`, { mode: 0o700 });
  const children: ReturnType<typeof launch>[] = [];
  t.after(async () => { for (const c of children) { c.child.kill('SIGKILL'); await c.exited; } rmSync(home, { recursive: true, force: true }); });
  const first = launch(home, bin); children.push(first);
  await eventually(() => existsSync(status) || first.child.exitCode !== null, 'first start');
  assert.equal(first.child.exitCode, null, first.stderr());
  assert.equal(existsSync(authority), false, 'runner never creates bindings');
  assert.equal(statSync(state).mode & 0o777, 0o700);
  for (const path of [db, db + '-wal', db + '-shm', status]) assert.equal(statSync(path).mode & 0o077, 0, path);
  const stored = openLedger(db, systemClock);
  stored.kv('codex').set('audit:restart', 'persistent-owner-evidence'); stored.close();
  first.child.kill('SIGTERM'); assert.deepEqual(await first.exited, [0, null]);
  assert.equal(existsSync(status), false, 'connector stopped before process exit');
  const config = join(home, 'config.toml'), codexHome = join(home, 'codex-home');
  writeFileSync(config, '', { mode: 0o600 }); mkdirSync(codexHome, { mode: 0o700 });
  const owner = { configPath: config, profile: 'test', as: '', hub: 'https://hub.invalid', caller: 'peer', codexHome,
    threadId: '11111111-1111-4111-8111-111111111111', generation: 'a'.repeat(32) };
  writeFileSync(authority, JSON.stringify({ version: 1, bindings: [{ config_path: config, profile: owner.profile, hub: owner.hub,
    caller: owner.caller, codex_home: codexHome, thread_id: owner.threadId, generation: owner.generation,
    created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 3_600_000).toISOString() }] }), { mode: 0o600 });
  const seed = openLedger(db, systemClock), claim = 'b'.repeat(24);
  seed.kv('codex').set('claiming:' + claim, JSON.stringify({ owner, threadId: owner.threadId, at: Date.now() })); seed.close();
  const second = launch(home, bin); children.push(second);
  await eventually(() => existsSync(trace) || second.child.exitCode !== null, 'restart recovery');
  assert.equal(second.child.exitCode, null, second.stderr());
  const recovered = openLedger(db, systemClock);
  try {
    await eventually(() => recovered.kv('codex').get('claiming:' + claim) === undefined, 'original unsent claim recovered');
    assert.equal(recovered.kv('codex').get('audit:restart'), 'persistent-owner-evidence');
  } finally { recovered.close(); }
  assert.match(readFileSync(trace, 'utf8'), new RegExp('--release --claim ' + claim));
  second.child.kill('SIGINT'); assert.deepEqual(await second.exited, [0, null]);
  assert.equal(existsSync(status), false);
});

for (const unsafe of ['directory-symlink', 'database-symlink', 'wal-symlink', 'database-mode', 'directory-mode', 'relative-binary']) {
  test(`standalone runner refuses ${unsafe} without modifying the target`, { skip: process.platform === 'win32' && 'Unix socket/mode fixture; native Windows proxy covered in windows-platform.test.ts' }, async t => {
    const home = mkdtempSync(join(tmpdir(), 'vn-runner-refuse-'));
    t.after(() => rmSync(home, { recursive: true, force: true }));
    const state = join(home, '.local', 'state', 'vecnet', 'codex'), target = join(home, 'do-not-touch');
    writeFileSync(target, 'sentinel', { mode: 0o600 });
    if (unsafe === 'directory-symlink') {
      const other = join(home, 'other'); mkdirSync(other, { mode: 0o700 }); symlinkSync(other, join(home, '.local'));
    } else mkdirSync(state, { recursive: true, mode: 0o700 });
    if (unsafe === 'database-symlink') symlinkSync(target, join(state, 'delivery.sqlite'));
    if (unsafe === 'wal-symlink') symlinkSync(target, join(state, 'delivery.sqlite-wal'));
    if (unsafe === 'database-mode') writeFileSync(join(state, 'delivery.sqlite'), '', { mode: 0o644 });
    if (unsafe === 'directory-mode') chmodSync(state, 0o755);
    const run = launch(home, unsafe === 'relative-binary' ? 'vecnet' : process.execPath);
    t.after(() => run.child.kill('SIGKILL'));
    const [code] = await run.exited;
    assert.notEqual(code, 0);
    assert.match(run.stderr(), /codex delivery: (unsafe state path|VECNET_BIN must be an absolute executable)/);
    assert.equal(readFileSync(target, 'utf8'), 'sentinel');
  });
}
