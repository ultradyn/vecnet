import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmod, lstat, mkdir, mkdtemp, readFile, readdir, readlink, rm, stat, symlink, unlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runScenario, receiptKey, busyProof, childEnvironment, validateInputs, livePorts, restoreOwnedRendezvous } from './codex-busy-fly-smoke.mjs';

function fixture(fault = '') {
  const actions = [], snapshots = [], events = [], sent = {};
  const owner = { profile: 'busy', as: '', hub: 'https://fixture.invalid', caller: 'peer2', codexHome: '/private/home/.codex', threadId: 'root', configPath: '/private/profile', generation: 'a'.repeat(32) };
  const history = [{ id: 'bootstrap', status: 'completed', items: [] }];
  let clock = 0, released = false, lastJournal, n = 0;
  const peers = [{ peer_id: 'unrelated', name: 'existing', disabled: false }];
  const item = s => ({ type: 'userMessage', clientId: receiptKey(owner, s.delivery) });
  const agent = s => ({ type: 'agentMessage', text: `FACT ${s.delivery} ${s.nonce}` });
  const p = {
    cwd: '/private/work', releaseFile: '/private/release', events, now: () => clock,
    nonce: () => `random${++n}`, sleep: async ms => { clock += ms; },
    journal: async j => { lastJournal = structuredClone(j); snapshots.push(lastJournal); },
    start: async () => { actions.push('start'); },
    call: async (method, params) => {
      actions.push(method);
      if (method === 'thread/start') return { model: fault === 'model' ? '' : 'configured', thread: { id: 'root', parentThreadId: null } };
      if (method === 'turn/start') {
        assert.equal(params.threadId, 'root');
        if (history[0].items.length === 0) {
          history[0].items = [{ type: 'agentMessage', text: params.input[0].text.match(/READY_[A-Za-z0-9]+/)[0] }];
          return { turn: { id: 'bootstrap' } };
        }
        assert.match(params.input[0].text, /20/);
        history.push({ id: 'busy-turn', status: 'inProgress', items: [] });
        return { turn: { id: 'busy-turn' } };
      }
      throw Error('unexpected RPC');
    },
    turns: async () => { if (fault === 'history') throw Error('unavailable'); return structuredClone(history); },
    commandStarted: id => id === 'busy-turn',
    turnCompleted: id => id === 'bootstrap',
    api: async (method, path, auth, body) => {
      actions.push(`${method} ${path}`);
      if (method === 'GET') return { peers: structuredClone(peers) };
      if (path === '/v1/admin/peers') {
        assert.ok(lastJournal.peers.some(x => x.name === body.name && x.attempted));
        const id = `peer${peers.length}`;
        peers.push({ peer_id: id, name: body.name, disabled: false });
        if (fault === 'lost-create') throw Error('response lost');
        return { peer_id: id, enrol_token: `token-${id}` };
      }
      if (path === '/v1/enrol') return { peer_id: body.token.slice(6), secret: 'private-secret' };
      if (path.endsWith('/disable')) {
        assert.notEqual(path, '/v1/admin/peers/unrelated/disable');
        assert.ok(lastJournal.peers.some(x => x.id && path.includes(x.id) && x.disabling));
        if (fault === 'cleanup') throw Error('disable failed');
        peers.find(x => path.includes(x.peer_id)).disabled = true;
        return {};
      }
      assert.equal(path, '/v1/messages');
      const priority = body.priority, delivery = `delivery-${priority}`;
      const nonce = body.idempotency_key;
      sent[priority] = { delivery, nonce };
      const b = history.find(t => t.id === 'busy-turn');
      if (priority === 'wake') {
        const keyOwner = fault === 'wrong-owner' ? { ...owner, generation: 'b'.repeat(32) } : owner;
        const request = { phase: 1, seq: 1, at: clock, connectionId: 2, client: fault === 'observer' ? 'observer' : 'vecnet', id: 7, method: 'turn/steer', threadId: fault === 'wrong-thread' ? 'other' : 'root', expectedTurnId: 'busy-turn', receiptKey: receiptKey(keyOwner, delivery) };
        events.push({ ...request, kind: 'request' }, { ...request, seq: 2, kind: 'response', ok: fault !== 'rejected', ...(fault === 'missing-return' ? {} : { turnId: fault === 'wrong-return' ? 'other' : 'busy-turn' }) });
        if (fault === 'tap-failure') events.push({ kind: 'failure', reason: 'incomplete_trace' });
        const target = fault === 'prior-idle' ? history[0] : b;
        if (fault !== 'missing') target.items.push(item(sent.wake), agent(sent.wake));
        if (fault === 'late') b.status = 'completed';
      } else if (fault === `accepted-${priority}`) b.items.push(item(sent[priority]));
      return { deliveries: [{ recipient: body.recipients[0], delivery_id: delivery }] };
    },
    installRecipient: async () => { actions.push('profile'); },
    bind: async () => owner,
    launch: async () => { actions.push('launch'); },
    ready: async () => true, cursor: async () => true,
    release: async () => {
      assert.ok(events.some(e => e.kind === 'response'));
      released = true; actions.push('release');
      history.find(t => t.id === 'busy-turn').status = 'completed';
      history.push({ id: 'idle-turn', status: 'completed', items: [item(sent.wake_idle), agent(sent.wake_idle)] });
    },
    hooks: async () => { actions.push('hooks'); },
    restart: async () => {
      actions.push('restart');
      if (fault === 'urgent-duplicate') history.push({ id: 'duplicate', status: 'completed', items: [item(sent.wake), agent(sent.wake)] });
      if (fault === 'duplicate-receipt') history.find(t => t.id === 'busy-turn').items.push(item(sent.wake));
      if (fault === 'restart-active') history.push({ id: 'unsettled-replay', status: 'inProgress', items: [] });
    },
    cleanup: async () => { actions.push('cleanup'); if (fault === 'close-failure') events.push({ kind: 'failure', reason: 'incomplete_trace' }); },
    finish: async verified => { actions.push(verified ? 'deleted-secrets' : 'retained-secrets'); },
  };
  return { p, actions, peers, snapshots, released: () => released };
}

test('complete busy proof releases only after receipt, connector steer and active model recognition; restart checks urgent key', async () => {
  const f = fixture(); const r = await runScenario(f.p);
  assert.equal(r.pass, true, r.reason);
  assert.equal(f.peers.length, 3);
  assert.ok(f.peers.slice(1).every(x => x.disabled));
  assert.equal(f.peers[0].disabled, false);
  assert.ok(f.actions.indexOf('release') < f.actions.indexOf('restart'));
  assert.ok(f.actions.includes('hooks'));
  assert.equal(f.actions.at(-1), 'deleted-secrets');
});

for (const fault of ['prior-idle', 'late', 'missing', 'wrong-owner', 'wrong-thread', 'observer', 'rejected', 'missing-return', 'wrong-return', 'tap-failure', 'accepted-hold', 'accepted-wake_idle']) {
  test(`fails closed before release: ${fault}`, async () => {
    const f = fixture(fault), r = await runScenario(f.p);
    assert.equal(r.pass, false); assert.equal(f.released(), false);
    assert.equal(r.reason, fault.startsWith('accepted-') ? 'excluded_priority_accepted' : fault === 'late' ? 'busy_proof_late' : fault === 'tap-failure' ? 'rpc_trace_failed' : 'busy_proof_missing');
    assert.ok(f.actions.includes('cleanup'));
    assert.ok(f.peers.slice(1).every(x => x.disabled));
  });
}
for (const fault of ['urgent-duplicate', 'duplicate-receipt', 'restart-active', 'cleanup', 'close-failure']) test(`final acceptance rejects ${fault}`, async () => {
  const f = fixture(fault), r = await runScenario(f.p);
  assert.equal(r.pass, false);
  assert.equal(f.released(), true);
  if (fault === 'cleanup') {
    assert.equal(f.actions.at(-1), 'retained-secrets');
    assert.equal(f.actions.filter(x => x.endsWith('/disable')).length, 2);
  }
});
test('lost creation response is never retried and exact prejournaled name is reconciled and disabled', async () => {
  const f = fixture('lost-create'), r = await runScenario(f.p);
  assert.equal(r.pass, false);
  assert.equal(f.actions.filter(x => x === 'POST /v1/admin/peers').length, 1);
  assert.equal(f.peers[1].disabled, true);
  assert.equal(f.peers[0].disabled, false);
});
for (const fault of ['model', 'history']) test(`preflight ${fault} fails before Fly writes`, async () => {
  const f = fixture(fault); assert.equal((await runScenario(f.p)).pass, false);
  assert.ok(!f.actions.includes('POST /v1/admin/peers'));
});
test('bootstrap completion materializes the root before the first full history request', async () => {
  const f = fixture(); let observed = 0;
  f.p.turnCompleted = () => ++observed > 2;
  const turns = f.p.turns;
  f.p.turns = async id => { assert.ok(observed > 2); return turns(id); };
  const result = await runScenario(f.p);
  assert.equal(result.pass, true, result.reason);
});
test('wire request and response must correlate within one tap phase and connection', () => {
  const owner = { generation: 'a' }, sent = { delivery: 'd', nonce: 'n' };
  const common = { client: 'vecnet', method: 'turn/steer', threadId: 't', expectedTurnId: 'b', receiptKey: receiptKey(owner, 'd'), id: 1, phase: 1, connectionId: 1 };
  const turn = { id: 'b', status: 'inProgress', items: [{ type: 'userMessage', clientId: common.receiptKey }, { type: 'agentMessage', text: 'FACT d n' }] };
  const req = { ...common, kind: 'request', seq: 1 }, res = { ...common, kind: 'response', seq: 2, ok: true, turnId: 'b' };
  assert.equal(busyProof([turn], [req, res], owner, 't', 'b', sent), true);
  for (const change of [{ phase: 2 }, { connectionId: 2 }, { id: 2 }, { seq: 0 }]) assert.equal(busyProof([turn], [req, { ...res, ...change }], owner, 't', 'b', sent), false);
  assert.equal(busyProof([turn], [req, res, { kind: 'completed', threadId: 't', turnId: 'b' }], owner, 't', 'b', sent), false);
});
test('explicit environment drops inherited session, profile and injection variables', () => {
  const env = childEnvironment('/private', '/installed/vecnet', '/usr/bin');
  assert.equal(env.HOME, '/private/home'); assert.equal(env.VECNET_LOCAL_MAINTENANCE, '0');
  for (const k of ['NODE_OPTIONS', 'BASH_ENV', 'ENV', 'CODEX_THREAD_ID', 'VECNET_PROFILE', 'VECNET_CODEX_BINDINGS', 'HTTP_PROXY']) assert.equal(env[k], undefined);
});
test('default, partial opt-in and import do not read credentials or create files', async () => {
  const root = await mkdtemp(join(tmpdir(), 'busy-guard-'));
  const entry = fileURLToPath(new URL('./codex-busy-fly-smoke.mjs', import.meta.url));
  try {
    for (const args of [[entry], [entry, '--run'], ['--input-type=module', '-e', `await import(${JSON.stringify(new URL('./codex-busy-fly-smoke.mjs', import.meta.url).href)})`]]) {
      const r = spawnSync(process.execPath, args, { cwd: root, env: { PATH: process.env.PATH, HOME: root, CODEX_BUSY_CONFIG: '/must-not-read', VECNET_BUSY_AUTH: '/must-not-read' }, encoding: 'utf8', timeout: 5000 });
      assert.equal(r.status, 0, r.stderr);
      assert.deepEqual(await readdir(root), []);
      if (args[0] === entry) assert.match(r.stdout, /SKIPPED/); else assert.equal(r.stdout, '');
    }
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('private installed inputs are copied with safe modes; byte drift and unsafe inputs fail without starting children', async () => {
  const root = await mkdtemp(join(tmpdir(), 'busy-inputs-'));
  const release = join(root, '.local/share/vecnet-codex', 'a'.repeat(64));
  await mkdir(join(release, 'connectors/launcher/src/codex'), { recursive: true, mode: 0o700 });
  const native = join(root, 'native'), provider = join(root, 'provider'), auth = join(root, 'auth'), manifest = join(root, 'manifest');
  const write = (path, value, mode = 0o600) => writeFile(path, value, { mode });
  await write(native, Buffer.from([127, 69, 76, 70]), 0o700);
  await write(join(release, 'vecnet'), Buffer.from([127, 69, 76, 70]), 0o700);
  await write(join(release, 'connectors/launcher/src/codex/delivery-main.ts'), 'fixture');
  await write(provider, 'model = "fixture"\n');
  await write(auth, JSON.stringify({ hub: 'https://fixture.invalid', operatorSecret: 'fixture-secret' }));
  await write(manifest, JSON.stringify({ schema: 1, version: 'v0.2.6', release, node: native, unit: '/never-operate.service' }));
  const input = { CODEX_BIN: native, CODEX_BUSY_CONFIG: provider, VECNET_BUSY_AUTH: auth, VECNET_BUSY_INSTALL_MANIFEST: manifest };
  let p;
  const homeBefore = process.env.HOME;
  try {
    const validated = await validateInputs(input); p = await livePorts(validated);
    assert.equal(process.env.HOME, homeBefore);
    assert.equal((await stat(p.root)).mode & 0o777, 0o700);
    assert.equal((await stat(join(p.root, 'home/.codex/config.toml'))).mode & 0o777, 0o600);
    assert.equal((await stat(join(p.root, 'cleanup-auth.json'))).mode & 0o777, 0o600);
    assert.equal(await readFile(join(p.root, 'home/.codex/config.toml'), 'utf8'), 'model = "fixture"\n');
    await write(join(release, 'connectors/launcher/src/codex/delivery-main.ts'), 'changed');
    await assert.rejects(() => p.cleanup(), /local_cleanup_failed/);
    const evidence = JSON.parse(await readFile(join(p.root, 'evidence.json'), 'utf8'));
    assert.deepEqual(evidence.children, []);
    assert.notDeepEqual(evidence.before, evidence.after);
    await p.finish(false);
    assert.equal(await readFile(join(p.root, 'cleanup-auth.json'), 'utf8'), JSON.stringify(validated.auth));
    await chmod(auth, 0o644);
    await assert.rejects(() => validateInputs(input), /input_file_unsafe/);
    await chmod(auth, 0o600);
    await write(auth, JSON.stringify({ hub: 'http://fixture.invalid', operatorSecret: 'fixture-secret' }));
    await assert.rejects(() => validateInputs(input), /auth_input_invalid/);
  } finally { await p?.finish(true); if (p) await rm(p.root, { recursive: true, force: true }); await rm(root, { recursive: true, force: true }); }
});

test('rendezvous restoration preserves a replacement and restores only the unchanged owned symlink', async () => {
  const root = await mkdtemp(join(tmpdir(), 'busy-link-')), link = join(root, 'socket');
  try {
    await symlink('/owned-relay', link); const identity = await lstat(link);
    await restoreOwnedRendezvous(link, '/owned-relay', '/original', identity);
    assert.equal(await readlink(link), '/original');
    await unlink(link); await symlink('/owned-relay', link); const second = await lstat(link);
    await unlink(link); await symlink('/unrelated', link);
    await assert.rejects(() => restoreOwnedRendezvous(link, '/owned-relay', '/original', second), /rendezvous_changed/);
    assert.equal(await readlink(link), '/unrelated');
  } finally { await rm(root, { recursive: true, force: true }); }
});
