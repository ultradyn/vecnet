import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { startCodexConnector } from '../src/codex/connector.ts';
import { openLedger } from '../src/ledger.ts';
import type { InflightRow } from '../src/codex/engine.ts';
import type { CodexRpc } from '../src/codex/rpc.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';

async function eventually(fn: () => boolean, what: string) {
  for (let i = 0; i < 500; i++) { if (fn()) return; await delay(10); }
  assert.ok(fn(), what);
}

test('launcher recovery survives a storage-mode switch and is not replayed from its retained ledger', async t => {
  const home = mkdtempSync(join(tmpdir(), 'vn-recovery-'));
  const clock = fakeClock(Date.now()), log = memoryLog();
  const state = join(home, '.local', 'state', 'vecnet', 'codex');
  mkdirSync(state, { recursive: true, mode: 0o700 });
  const database = join(state, 'delivery.sqlite'), authority = join(state, 'bindings.json');
  const config = join(home, 'config.toml'), bin = join(home, 'vecnet'), trace = join(home, 'calls');
  const owner = { configPath: config, profile: 'original', as: '', hub: 'https://hub.invalid', caller: 'peer',
    codexHome: home, threadId: '11111111-1111-4111-8111-111111111111', generation: 'a'.repeat(32) };
  writeFileSync(config, '', { mode: 0o600 });
  writeFileSync(bin, `#!/bin/sh
printf '%s\\n' "$*" >> '${trace}'
case " $* " in
  *" codex verify "*) echo '{"verified":true}' ;;
  *" codex settle "*) echo '{}' ;;
  *" watch "*) exit 0 ;;
  *) echo '{"version":1,"deliveries":[]}' ;;
esac
`, { mode: 0o700 });
  writeFileSync(authority, JSON.stringify({ version: 1, bindings: [{ config_path: config, profile: owner.profile,
    hub: owner.hub, caller: owner.caller, codex_home: home, thread_id: owner.threadId, generation: owner.generation,
    created_at: new Date(clock.now() - 1000).toISOString(), expires_at: new Date(clock.now() + 3_600_000).toISOString() }] }), { mode: 0o600 });
  const original = openLedger(join(home, 'launcher.sqlite'), clock);
  const row: InflightRow = { owner, threadId: owner.threadId, key: 'original-receipt', claim: 'b'.repeat(24),
    attempt: 'c'.repeat(32), deliveries: ['delivery-1'], method: 'turn/start', state: 'sending',
    attemptTurnId: null, priorTurnId: null, acceptedAt: null };
  const key = 'inflight:' + row.key, raw = JSON.stringify(row);
  original.kv('codex').set(key, raw);
  original.kv('daemon').set('self', 'launcher-peer');
  const connectors: ReturnType<typeof startCodexConnector>[] = [];
  let shared: ReturnType<typeof openLedger> | undefined;
  t.after(async () => {
    for (const c of connectors) await c.stop();
    shared?.close(); original.close();
    rmSync(home, { recursive: true, force: true });
  });
  const env = { HOME: home, PATH: process.env.PATH, VECNET_BIN: bin };
  let opens = 0;
  const first = startCodexConnector({ kv: original.kv('codex'), clock, log, env,
    openRpc: async () => { opens++; throw new Error('crashed endpoint remains unreachable'); } });
  connectors.push(first);
  await eventually(() => opens > 0, 'launcher engine attempted the original endpoint');
  assert.ok(existsSync(database), 'launcher must persist recovery in the standalone per-user database');
  shared = openLedger(database, clock);
  for (const path of [database, database + '-wal', database + '-shm']) {
    assert.equal(statSync(path).mode & 0o077, 0, 'shared database and sidecars remain private in launcher mode');
  }
  assert.equal(shared.kv('codex').get(key), raw, 'ambiguous original-owner send is retained');
  assert.equal(original.kv('codex').get(key), raw, 'migration preserves launcher recovery evidence');
  assert.equal(original.kv('daemon').get('self'), 'launcher-peer', 'other launcher namespaces are unchanged');
  await first.stop();

  // The standalone boundary supplies no launcher ledger.
  const methods: string[] = [];
  const rpc: CodexRpc = {
    version: '0.166.0', onNotification() {}, onClose() {}, close() {},
    async call<T>(method: Parameters<CodexRpc['call']>[0], params: unknown): Promise<T> {
      methods.push(method);
      assert.equal((params as { threadId: string }).threadId, owner.threadId, 'recovery stays on the original thread');
      if (method === 'thread/read') return { thread: { id: owner.threadId, source: 'cli', parentThreadId: null, status: { type: 'idle' } } } as T;
      if (method === 'thread/turns/list') return { data: [{ id: 'accepted-turn', status: 'completed', items: [{ type: 'userMessage', clientId: row.key }] }], nextCursor: null } as T;
      if (method === 'thread/resume') return { approvalPolicy: 'never' } as T;
      assert.equal(method, 'thread/unsubscribe', 'recovery never sends another turn');
      return {} as T;
    },
  };
  const second = startCodexConnector({ clock, log, env, openRpc: async () => rpc });
  connectors.push(second);
  await eventually(() => shared!.kv('codex').get(key) === undefined, 'replacement mode settles the exact-thread receipt');
  assert.ok(methods.includes('thread/turns/list'));
  assert.match(readFileSync(trace, 'utf8'), /codex settle --outcome accepted .*--claim b{24} --attempt c{32}/);
  assert.equal(original.kv('codex').get(key), raw, 'the offline launcher keeps its original evidence');
  await second.stop();

  const before = readFileSync(trace, 'utf8');
  const third = startCodexConnector({ kv: original.kv('codex'), clock, log, env, openRpc: async () => rpc });
  connectors.push(third);
  await eventually(() => methods.filter(m => m === 'thread/read').length >= 2, 'original launcher restarted');
  await third.stop();
  assert.equal(shared.kv('codex').get(key), undefined, 'retained source rows are imported only once');
  assert.equal(readFileSync(trace, 'utf8').slice(before.length).includes('codex settle'), false, 'settled recovery is not replayed');
  assert.equal(log.entries.some(e => e.msg === 'codex_recovery_quarantined'), false);
});


test('a conflicting legacy row rolls back the entire import and starts no engine', async t => {
  const home = mkdtempSync(join(tmpdir(), 'vn-recovery-conflict-'));
  const clock = fakeClock(Date.now()), log = memoryLog();
  const state = join(home, '.local', 'state', 'vecnet', 'codex');
  mkdirSync(state, { recursive: true, mode: 0o700 });
  const database = join(state, 'delivery.sqlite');
  // Create the file privately, just as the production opener does.
  writeFileSync(database, '', { mode: 0o600 });
  const shared = openLedger(database, clock), legacy = openLedger(join(home, 'launcher.sqlite'), clock);
  shared.kv('codex').set('inflight:conflict', 'shared-original-owner');
  legacy.kv('codex').set('claiming:before-conflict', 'legacy-unsent-evidence');
  legacy.kv('codex').set('inflight:conflict', 'legacy-different-owner');
  const config = join(home, 'config.toml'); writeFileSync(config, '', { mode: 0o600 });
  writeFileSync(join(state, 'bindings.json'), JSON.stringify({ version: 1, bindings: [{ config_path: config,
    profile: 'p', hub: 'https://hub.invalid', caller: 'peer', codex_home: home,
    thread_id: '11111111-1111-4111-8111-111111111111', generation: 'a'.repeat(32),
    created_at: new Date(clock.now() - 1000).toISOString(), expires_at: new Date(clock.now() + 3_600_000).toISOString() }] }), { mode: 0o600 });
  let opens = 0;
  const c = startCodexConnector({ kv: legacy.kv('codex'), clock, log, env: { HOME: home, PATH: process.env.PATH },
    openRpc: async () => { opens++; throw new Error('must not open'); } });
  t.after(async () => { await c.stop(); shared.close(); legacy.close(); rmSync(home, { recursive: true, force: true }); });
  await eventually(() => log.entries.some(e => e.msg === 'codex_recovery_refused'), 'conflict refused');
  assert.equal(shared.kv('codex').get('inflight:conflict'), 'shared-original-owner');
  assert.equal(legacy.kv('codex').get('inflight:conflict'), 'legacy-different-owner');
  assert.equal(legacy.kv('codex').get('claiming:before-conflict'), 'legacy-unsent-evidence');
  assert.equal(shared.kv('codex').get('claiming:before-conflict'), undefined, 'earlier copied rows roll back too');
  assert.deepEqual(shared.kv('codex:imports').keys(), [], 'a failed import is never marked complete');
  assert.equal(opens, 0, 'no engine runs after an import conflict');
  const status = JSON.parse(readFileSync(join(state, 'bindings.json.status.json'), 'utf8'));
  assert.deepEqual(status.bindings, [], 'a conflict cannot advertise ready delivery');
});
