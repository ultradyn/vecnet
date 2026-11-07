// Controller-only harness contract. Run only through a newly permitted x-boss lane.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:net';
import { createHash } from 'node:crypto';
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, renameSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { validateManifest, planHarness, proveActor, proveDelivery, sanitizeOutcome, createHarness, runBounded, treeHash }
  from './codex-auto-delivery-smoke.mjs';

const cli = fileURLToPath(new URL('./codex-auto-delivery-smoke.mjs', import.meta.url));
test.before(t => {
  for (const p of [cli, fileURLToPath(import.meta.url), join(dirname(dirname(cli)), 'justfile')]) {
    t.diagnostic('source-sha256 ' + createHash('sha256').update(readFileSync(p)).digest('hex') + ' ' + p);
  }
});
const hash = 'a'.repeat(64), hashB = 'b'.repeat(64);
const uuid = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'vn-auto-smoke-'));
  chmodSync(root, 0o700);
  const beforeRemove = [];
  t.after(async () => { try { for (const close of beforeRemove) await close(); } finally { rmSync(root, { recursive: true, force: true }); } });
  const runRoot = join(root, 'run'), home = join(root, 'home');
  for (const path of [runRoot, home, join(root, 'runtime'), join(root, 'codex')]) mkdirSync(path, { mode: 0o700 });
  const m = { version: 1, runId: 'fixture', runRoot, base: '69df47b10bd0fb0d2137bf2c41c5bd91237b55af', scriptHash: createHash('sha256').update(readFileSync(cli)).digest('hex'),
    approval: { host: 'xsm', uid: process.geteuid(), home, codexHome: join(root, 'codex'), maxApproved: true },
    tools: Object.fromEntries(['node', 'codex', 'tmux', 'systemctl', 'flock', 'script'].map(k => [k, join(root, 'tool-' + k)])),
    generations: Object.fromEntries(['a', 'b'].map((g, i) => [g, { binarySource: join(runRoot, `artifact-${g}`),
      binary: join(home, `.local/share/vecnet-codex-acceptance/${g}/vecnet`), binaryHash: i ? hashB : hash,
      assetSource: join(runRoot, `assets-${g}`), root: join(home, `.local/share/vecnet-launcher/${g}`), treeHash: i ? hashB : hash }])),
    actor: { mode: 'owned-tui-command', uuidSource: 'status' }, rotationFile: join(runRoot, 'rotation.json'),
    operators: { fly: { profile: 'fly-operator', config: join(runRoot, 'fly-operator.toml'), credentialFile: join(runRoot, 'fly-operator.json') },
      dev: { profile: 'operator', config: join(runRoot, 'dev-operator.toml'), credentialFile: join(runRoot, 'dev-operator.json') } },
    peers: [ ['fly', 'a', 'receiver', 'a'], ['fly', 'b', 'receiver', 'b'], ['dev', 'a-dev', 'receiver', 'a'],
      ['fly', 'send-fly', 'sender', ''], ['dev', 'send-dev', 'sender', ''] ].map(([hub, profile, role, session], i) =>
      ({ hub, profile, role, session, peerId: `peer_${i}`, config: join(runRoot, hub === 'fly' && role === 'receiver' ? 'receiver.toml' : `${profile}.toml`) })) };
  for (const p of new Set([...m.peers.map(p => p.config), ...Object.values(m.operators).flatMap(p => [p.config, p.credentialFile]), m.rotationFile])) {
    writeFileSync(p, 'PRIVATE-CREDENTIAL-CANARY', { mode: 0o600 });
  }
  m.tools.node = process.execPath;
  const manifest = join(runRoot, 'manifest.json');
  const save = () => writeFileSync(manifest, JSON.stringify(m), { mode: 0o600 });
  save();
  const env = { PATH: '/usr/bin:/bin', HOME: home, XDG_CONFIG_HOME: join(home, '.config'),
    XDG_DATA_HOME: join(home, '.local/share'), XDG_RUNTIME_DIR: join(root, 'runtime'), CODEX_HOME: join(root, 'codex') };
  return { root, m, manifest, save, env, beforeRemove };
}
async function child(args, env) {
  const p = spawn(process.execPath, args, { env, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  p.stdout.on('data', b => { stdout += b; }); p.stderr.on('data', b => { stderr += b; });
  const timer = setTimeout(() => p.kill('SIGKILL'), 15000);
  const code = await new Promise((resolve, reject) => { p.once('error', reject); p.once('close', resolve); });
  clearTimeout(timer); return { code, stdout, stderr };
}
const snapshot = root => readdirSync(root, { recursive: true }).map(name => {
  const path = join(root, name), s = lstatSync(path);
  return [name, s.mode, s.ino, s.size, s.isFile() ? readFileSync(path).toString('hex') : ''];
});
test('import and no opt-in never read the supplied manifest or launch a command', async t => {
  const f = fixture(t), trap = join(f.root, 'trap.mjs');
  writeFileSync(trap, `import fs from 'node:fs'; import cp from 'node:child_process'; import net from 'node:net';
import {syncBuiltinESMExports} from 'node:module';
const read=fs.readFileSync,open=fs.openSync;fs.openSync=function(p,...a){if(String(p).endsWith('manifest.json')||String(p).endsWith('.toml'))throw Error('credential-open');return open.call(this,p,...a)};fs.readFileSync=function(p,...a){if(String(p).includes('manifest.json')||String(p).endsWith('.toml'))throw Error('credential-read');return read.call(this,p,...a)};
cp.spawn=()=>{throw Error('spawn')}; net.connect=()=>{throw Error('connect')}; syncBuiltinESMExports();`, { mode: 0o600 });
  const before = snapshot(f.root);
  const imported = await child(['--import', trap, '--input-type=module', '-e', `await import(${JSON.stringify(new URL('./codex-auto-delivery-smoke.mjs', import.meta.url).href)})`], f.env);
  assert.deepEqual(imported, { code: 0, stdout: '', stderr: '' });
  const result = await child(['--import', trap, cli, '--manifest', f.manifest, '--run'], f.env);
  assert.equal(result.code, 0); assert.match(result.stdout, /skipped/); assert.equal(result.stderr, '');
  assert.deepEqual(snapshot(f.root), before);
});
test('dry-run emits the exact two-hub actions and guarded cleanup without reading credentials or writing', async t => {
  const f = fixture(t), trap = join(f.root, 'dry-trap.mjs');
  writeFileSync(trap, `import fs from 'node:fs';import cp from 'node:child_process';import net from 'node:net';import {syncBuiltinESMExports} from 'node:module';
const read=fs.readFileSync,open=fs.openSync;fs.openSync=function(p,...a){if(String(p).endsWith('.toml')||String(p).endsWith('rotation.json'))throw Error('credential-open');return open.call(this,p,...a)};fs.readFileSync=function(p,...a){if(String(p).endsWith('.toml')||String(p).endsWith('rotation.json'))throw Error('credential-read');return read.call(this,p,...a)};
fs.writeFileSync=()=>{throw Error('write')};fs.mkdirSync=()=>{throw Error('mkdir')};cp.spawn=()=>{throw Error('spawn')};net.connect=()=>{throw Error('connect')};syncBuiltinESMExports();`, { mode: 0o600 });
  const before = snapshot(f.root);
  const result = await child(['--import', trap, cli, '--manifest', f.manifest, '--dry-run'], { ...f.env, VECNET_CODEX_AUTO_DELIVERY_SMOKE: '1' });
  assert.equal(result.code, 0, result.stderr); assert.equal(result.stderr, '');
  const out = JSON.parse(result.stdout);
  assert.deepEqual(out.units, [
    'vecnet-delivery@6c7bae100706c46624555c38874d38209a59615c1ae7a1536db5e8f43d09ce02.service',
    'vecnet-delivery@c88dc88d4625629c09a768cdc14f45201d679a762cbb352773dbfb3cee42ed91.service']);
  assert.equal(out.paths.registry, join(f.m.approval.home, '.local/share/vecnet/codex-delivery/registry.json'));
  assert.match(out.cleanup, /--cleanup/); assert.match(out.rollback, /--rollback/);
  assert.ok(!result.stdout.includes('PRIVATE-CREDENTIAL-CANARY'));
  assert.deepEqual(snapshot(f.root), before);
});
test('manifest refuses wrong types, extra keys, escaped paths, wrong actor and peer collisions', t => {
  const { m } = fixture(t); assert.equal(validateManifest(m).runId, 'fixture');
  for (const edit of [
    m => { m.version = '1'; }, m => { m.extra = true; }, m => { m.approval.maxApproved = 'yes'; },
    m => { m.peers[0].config = resolve(m.runRoot, '../escaped'); },
    m => { m.peers[0].config = `${m.runRoot}-other/file`; }, m => { m.peers[0].hub = 'https://foreign.invalid'; },
    m => { m.peers[1].peerId = m.peers[0].peerId; }, m => { m.generations.b.binaryHash = m.generations.a.binaryHash; },
    m => { m.actor.mode = 'controller-with-thread-env'; }, m => { m.tools.tmux = '/tmp/a\nExecStart=bad'; },
  ]) { const bad = structuredClone(m); edit(bad); assert.throws(() => validateManifest(bad)); }
});
test('credential mode and symlink collisions refuse before install', async t => {
  const f = fixture(t), p = f.m.peers[0].config;
  chmodSync(p, 0o644);
  await assert.rejects(createHarness(f.m).preflight(), { code: 'unsafe_path' });
  chmodSync(p, 0o600); const target = join(f.m.runRoot, 'safe.toml');
  writeFileSync(target, 'private', { mode: 0o600 }); rmSync(p); symlinkSync(target, p);
  await assert.rejects(createHarness(f.m).preflight(), { code: 'unsafe_path' });
});
test('actor proof requires an owned TUI command item, never a controller-supplied thread environment', () => {
  const command = '/fixture/vecnet --net a --json peers';
  const own = { id: uuid(1), cwd: '/fixture/ws-a', firstMarker: 'OWN-A', uuidFromOwnTui: true };
  const history = [{ id: 'turn-1', items: [{ type: 'commandExecution', command, cwd: own.cwd, exitCode: 0, status: 'completed' }] }];
  assert.equal(proveActor(own, history, command).kind, 'owned-tui-command');
  assert.throws(() => proveActor({ ...own, uuidFromOwnTui: false }, history, command), { code: 'actor_unproved' });
  assert.throws(() => proveActor(own, [], command), { code: 'actor_unproved' });
  assert.throws(() => proveActor(own, [{ items: [{ ...history[0].items[0], exitCode: 1 }] }], command), { code: 'actor_unproved' });
});
test('receipt proof requires complete owned history, exact client and model answer once, and Go display commit', () => {
  const p = { threadId: uuid(1), clientId: 'vn.exact', deliveryId: 'dly_1', nonce: 'NONCE-fixture',
    historyComplete: true, controlBeforeNonce: true, hookBytes: 0, trustPreserved: true, inboxUnacked: true,
    display: { hub: 'https://vecnet.xk.io', caller: 'peer_0', displayed: ['dly_1'], claims: [] },
    expectedHub: 'https://vecnet.xk.io', expectedCaller: 'peer_0',
    turns: [{ items: [{ type: 'userMessage', clientId: 'vn.exact' }, { type: 'agentMessage', text: 'FACT dly_1 NONCE-fixture' }] }] };
  assert.equal(proveDelivery(p).occurrences, 1);
  for (const edit of [
    p => { p.historyComplete = false; }, p => { p.controlBeforeNonce = false; }, p => { p.hookBytes = 1; }, p => { p.inboxUnacked = false; },
    p => { p.display.caller = 'foreign'; }, p => { p.display.displayed = []; }, p => { p.display.claims = [{ id: 'dly_1' }]; },
    p => { p.turns.push(p.turns[0]); }, p => { p.turns[0].items[0].clientId = 'vn.other'; },
    p => { p.turns[0].items[1].text = 'RPC accepted'; }, p => { p.trustPreserved = false; },
  ]) { const bad = structuredClone(p); edit(bad); assert.throws(() => proveDelivery(bad), { code: 'receipt_unproved' }); }
});
test('sanitization never includes body, environment, stderr, terminal or arbitrary error', () => {
  assert.deepEqual(sanitizeOutcome({ stage: 'cleanup', code: 'ownership_changed', count: 1, nonceHash: hash,
    body: 'PRIVATE', env: { SECRET: 'PRIVATE' }, stderr: 'PRIVATE', pane: 'PRIVATE', message: 'PRIVATE' }),
  { stage: 'cleanup', code: 'ownership_changed', count: 1, nonceHash: hash });
});
test('bounded subprocesses refuse output floods and deadlines without raw errors', async t => {
  const f = fixture(t);
  await assert.rejects(runBounded(process.execPath, ['-e', 'process.stdout.write("PRIVATE".repeat(10000))'], { env: f.env, maxBytes: 64 }), { code: 'child_output_limit' });
  await assert.rejects(runBounded(process.execPath, ['-e', 'setTimeout(()=>{},10000)'], { env: f.env, timeoutMs: 100 }), { code: 'child_timeout' });
});
test('tree identity excludes neither changed runtime bytes nor unsafe links', t => {
  const f = fixture(t), assets = join(f.root, 'tree'); mkdirSync(assets, { mode: 0o700 });
  writeFileSync(join(assets, 'one.ts'), 'export const a=1;'); const before = treeHash(assets);
  writeFileSync(join(assets, 'one.ts'), 'export const a=2;'); assert.notEqual(treeHash(assets), before);
  symlinkSync('/foreign', join(assets, 'two.ts')); assert.throws(() => treeHash(assets), { code: 'unsafe_path' });
});
function rehearsal(t) {
  const f = fixture(t), manager = join(f.root, 'manager'), peers = join(f.root, 'peer-state.json'), trace = join(f.root, 'commands.json');
  mkdirSync(manager, { mode: 0o700 }); writeFileSync(peers, '{}', { mode: 0o600 }); writeFileSync(trace, '[]', { mode: 0o600 });
  const urls = { fly: 'https://vecnet.xk.io', dev: 'http://127.0.0.1:45752' };
  for (const config of new Set(f.m.peers.map(p => p.config))) writeFileSync(config, f.m.peers.filter(p => p.config === config).map(p =>
    `[profiles.${p.profile}]\nhub = "${urls[p.hub]}"\npeer_id = "${p.peerId}"\nsecret = "vn_fixture_old"\n`).join('\n'), { mode: 0o600 });
  for (const [hub, op] of Object.entries(f.m.operators)) {
    writeFileSync(op.config, `[profiles.${op.profile}]\nhub = "${urls[hub]}"\nsecret = "vn_operator_fixture_${hub}"\n`, { mode: 0o600 });
    writeFileSync(op.credentialFile, JSON.stringify({ version: 1, hub: urls[hub], credential: 'vn_operator_fixture_' + hub }), { mode: 0o600 });
  }
  const digest = bytes => createHash('sha256').update(bytes).digest('hex');
  const binary = `#!${process.execPath}
const fs=require('node:fs'); const args=process.argv.slice(2), state=process.env.FIXTURE_PEERS;
const trace=process.env.FIXTURE_TRACE, calls=JSON.parse(fs.readFileSync(trace));calls.push(args);fs.writeFileSync(trace,JSON.stringify(calls));
if(args.includes('disable')){const peers=JSON.parse(fs.readFileSync(state));peers[args.at(-1)]=true;fs.writeFileSync(state,JSON.stringify(peers));}
process.stdout.write('{}\\n');\n`;
  for (const [name, g] of Object.entries(f.m.generations)) {
    writeFileSync(g.binarySource, binary + '// generation ' + name, { mode: 0o700 }); g.binaryHash = digest(readFileSync(g.binarySource));
    mkdirSync(join(g.assetSource, 'launcher/src/codex'), { recursive: true, mode: 0o700 });
    mkdirSync(join(g.assetSource, 'hub-client/src'), { recursive: true, mode: 0o700 });
    writeFileSync(join(g.assetSource, 'launcher/src/codex/delivery-main.ts'), `export const generation=${JSON.stringify(name)};`);
    writeFileSync(join(g.assetSource, 'hub-client/src/index.ts'), 'export {};'); g.treeHash = treeHash(g.assetSource);
  }
  const managerProgram = `#!${process.execPath}
const fs=require('node:fs'),p=require('node:path'); const home=process.env.FIXTURE_CLIENT_HOME, args=process.argv.slice(2);
if(process.env.CODEX_HOME||process.env.VECNET_CONFIG||process.env.HOME===home)process.exit(90);
fs.writeFileSync(p.join(process.env.HOME,'receipt.json'),JSON.stringify({home:process.env.HOME,runtime:process.env.XDG_RUNTIME_DIR}));
const share=p.join(home,'.local/share/vecnet-launcher'), manifest=p.join(share,'delivery.json');
if(args.includes('show')) for(const unit of args.filter(a=>a.endsWith('.service'))){
 const loaded=fs.existsSync(p.join(home,'.config/systemd/user/vecnet-delivery@.service'));
 const row={Id:unit,MainPID:'0',LoadState:loaded?'loaded':'not-found',ActiveState:'inactive',SubState:'dead',UnitFileState:loaded?'disabled':'',Job:'',DropInPaths:''};
 if(args.includes('--property=ExecStart')){const m=JSON.parse(fs.readFileSync(manifest)),slug=unit.slice('vecnet-delivery@'.length,-'.service'.length);
 row.ExecStart='{ path='+m.binary+' ; argv[]='+m.binary+' codex deliver --hub-id '+slug+' ; ignore_errors=no ; }';row.UMask='0077';
 row.Environment='HOME='+home+' XDG_DATA_HOME='+p.join(home,'.local/share')+' XDG_RUNTIME_DIR='+process.env.FIXTURE_CLIENT_RUNTIME+' VECNET_BIN='+m.binary+' VECNET_CODEX_DELIVERY_ENTRY='+m.entry+' PATH='+m.path;}
 process.stdout.write(Object.entries(row).map(([k,v])=>k+'='+v).join('\\n')+'\\n\\n');}
`;
  for (const name of ['codex', 'tmux', 'systemctl', 'script']) writeFileSync(f.m.tools[name], name === 'systemctl' ? managerProgram :
    name === 'tmux' ? `#!${process.execPath}\nconst fs=require('node:fs');if(process.argv.includes('display-message'))process.stdout.write(fs.readFileSync(${JSON.stringify(join(f.m.runRoot, 'fixture-server-pid'))},'utf8'));` : `#!${process.execPath}\nprocess.exit(0);`, { mode: 0o700 });
  f.m.tools.flock = '/usr/bin/flock';
  const runtime = join(f.m.runRoot, 'runtime/vecnet'); mkdirSync(runtime, { recursive: true, mode: 0o700 }); f.save();
  const ports = { runtime, machine: { host: 'xsm', uid: process.geteuid(), home: f.m.approval.home },
    run: (command, args, options) => runBounded(command, args, { ...options,
      env: command === f.m.tools.systemctl ? { HOME: manager, XDG_CONFIG_HOME: join(manager, 'config'), XDG_DATA_HOME: join(manager, 'data'), XDG_RUNTIME_DIR: join(manager, 'runtime'), PATH: '/usr/bin:/bin', FIXTURE_CLIENT_HOME: f.m.approval.home, FIXTURE_CLIENT_RUNTIME: join(f.m.runRoot, 'runtime') }
        : { ...options.env, FIXTURE_PEERS: peers, FIXTURE_TRACE: trace } }),
    peerDisabled: async p => JSON.parse(readFileSync(peers))[p.id] === true };
  return { ...f, ports, peers, trace, manager, harness: () => createHarness(f.m, ports) };
}
test('real fake-manager process installs exact contract and restores prior bytes, mode and current link', async t => {
  const f = rehearsal(t), p = planHarness(f.m).paths;
  mkdirSync(join(f.m.approval.home, '.config/systemd/user'), { recursive: true, mode: 0o700 });
  mkdirSync(p.share, { recursive: true, mode: 0o700 });
  writeFileSync(p.template, 'prior template\n', { mode: 0o640 });
  writeFileSync(p.installation, '{"prior":true}\n', { mode: 0o600 });
  symlinkSync('/prior/release', p.current);
  const h = f.harness();
  assert.equal((await h.install()).code, 'installed');
  assert.match(readFileSync(p.template, 'utf8'), /HOME=%h/); assert.match(readFileSync(p.template, 'utf8'), /UMask=0077/);
  assert.equal(JSON.parse(readFileSync(p.installation)).binary, f.m.generations.a.binary);
  const receipt = JSON.parse(readFileSync(join(f.manager, 'receipt.json')));
  assert.equal(receipt.home, f.manager); assert.notEqual(receipt.runtime, f.ports.runtime);
  assert.equal((await h.cleanup()).count, 5); assert.ok(existsSync(f.m.peers[0].config));
  await h.rollback();
  assert.equal(readFileSync(p.template, 'utf8'), 'prior template\n'); assert.equal(lstatSync(p.template).mode & 0o777, 0o640);
  assert.equal(readFileSync(p.installation, 'utf8'), '{"prior":true}\n');
  assert.equal(lstatSync(p.current).isSymbolicLink(), true);
  assert.equal(readlinkSync(p.current), '/prior/release');
  assert.equal(existsSync(f.m.generations.a.binary), false); assert.equal(existsSync(f.m.peers[0].config), false);
  // These real shim processes prove the harness contract, never default-account managed acceptance.
});
test('partial cleanup retries only pending peers and retains credentials after failed verification', async t => {
  const f = rehearsal(t); let failOnce = true;
  f.ports.peerDisabled = async p => { const disabled = JSON.parse(readFileSync(f.peers))[p.id] === true;
    if (disabled && p.id === 'peer_1' && failOnce) { failOnce = false; return false; } return disabled; };
  await f.harness().install();
  await assert.rejects(f.harness().cleanup(), { code: 'peer_verification_failed' });
  assert.ok(existsSync(f.m.peers[0].config));
  await f.harness().cleanup();
  const disables = JSON.parse(readFileSync(f.trace)).filter(args => args.includes('disable')).map(args => args.at(-1));
  assert.deepEqual(disables.sort(), ['peer_0', 'peer_1', 'peer_2', 'peer_3', 'peer_4']);
  await f.harness().rollback();
});
test('foreign current or registry change refuses rollback and preserves credentials and evidence', async t => {
  const f = rehearsal(t); await f.harness().install(); await f.harness().cleanup();
  const p = planHarness(f.m).paths;
  rmSync(p.current); symlinkSync('/foreign/new-release', p.current);
  await assert.rejects(f.harness().rollback(), { code: 'ownership_changed' });
  assert.ok(existsSync(f.m.generations.a.binary)); assert.ok(existsSync(f.m.peers[0].config));
  assert.ok(existsSync(join(f.m.runRoot, 'journal.json')));
});
test('foreign source arrival refuses cleanup before any peer disable', async t => {
  const f = rehearsal(t); await f.harness().install(); const p = planHarness(f.m).paths;
  mkdirSync(p.data, { recursive: true, mode: 0o700 });
  writeFileSync(p.registry, JSON.stringify({ version: 1, sources: [{ config: '/foreign/c', bindings: '/foreign/b' }] }), { mode: 0o600 });
  await assert.rejects(f.harness().cleanup(), { code: 'ownership_changed' });
  assert.deepEqual(JSON.parse(readFileSync(f.trace)), []); assert.ok(existsSync(f.m.peers[0].config));
});
test('cleanup and rollback preserve existing lock inodes and disclose remaining owned state', async t => {
  const f = rehearsal(t); await f.harness().install(); const p = planHarness(f.m).paths;
  mkdirSync(p.data, { recursive: true, mode: 0o700 }); const lock = join(p.data, 'registry.lock');
  writeFileSync(lock, '', { mode: 0o600 }); const ino = lstatSync(lock).ino;
  await f.harness().cleanup(); const result = await f.harness().rollback();
  assert.equal(lstatSync(lock).ino, ino); assert.equal(result.code, 'rollback_residuals');
});
async function flowRehearsal(t) {
  const f = rehearsal(t), flow = join(f.m.runRoot, 'flow.json'), plan = planHarness(f.m);
  f.beforeRemove.push(async () => {
    if (!existsSync(flow)) return; const server = JSON.parse(readFileSync(flow)).server; if (!server) return;
    const same = () => { try { const raw = readFileSync(`/proc/${server.pid}/stat`, 'utf8'), fields = raw.slice(raw.lastIndexOf(')') + 2).split(' '); return fields[19] === server.birth && !['Z', 'X'].includes(fields[0]); } catch (e) { if (e.code === 'ENOENT') return false; throw e; } };
    if (same()) process.kill(server.pid, 'SIGTERM');
    const deadline = Date.now() + 1000; while (same() && Date.now() < deadline) await new Promise(r => setTimeout(r, 10));
    if (same()) { process.kill(server.pid, 'SIGKILL'); while (same() && Date.now() < deadline + 1000) await new Promise(r => setTimeout(r, 10)); }
    assert.equal(same(), false, 'owned fixture server completely stopped');
  });
  writeFileSync(flow, JSON.stringify({ peers: f.m.peers, sessions: {}, turns: {}, counter: 0 }), { mode: 0o600 });
  const prologue = `#!${process.execPath}
const fs=require('node:fs'),path=require('node:path'),cp=require('node:child_process'),crypto=require('node:crypto');
const file=process.env.FIXTURE_FLOW,args=process.argv.slice(2),read=()=>JSON.parse(fs.readFileSync(file)),save=s=>fs.writeFileSync(file,JSON.stringify(s));
const urls={fly:'https://vecnet.xk.io',dev:'http://127.0.0.1:45752'},slugs=${JSON.stringify(Object.fromEntries(plan.units.map((unit, i) => [i ? 'dev' : 'fly', unit.slice(16, -8)])))};
const put=(p,v)=>{fs.mkdirSync(path.dirname(p),{recursive:true,mode:448});fs.writeFileSync(p,JSON.stringify(v),{mode:384});};
`;
  const tmuxProgram = prologue + `
const s=read(),session=args[args.indexOf('-t')+1],part=()=>s.sessions[session];
if(args.includes('new-session')){if(!s.server){const socket=args[args.indexOf('-S')+1],program="const net=require('node:net');const server=net.createServer();server.listen(process.argv[1]);process.on('SIGTERM',()=>server.close(()=>process.exit(0)));";
const server=cp.spawn(process.execPath,['-e',program,socket],{detached:true,stdio:'ignore'});server.unref();const deadline=Date.now()+2000;
while(!fs.existsSync(socket)&&Date.now()<deadline)Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,10);if(!fs.existsSync(socket))process.exit(91);
const stat=fs.readFileSync('/proc/'+server.pid+'/stat','utf8');s.server={pid:server.pid,birth:stat.slice(stat.lastIndexOf(')')+2).split(' ')[19]};}
const name=args[args.indexOf('-s')+1];s.sessions[name]={id:name==='a'?${JSON.stringify(uuid(1))}:${JSON.stringify(uuid(2))},cwd:args[args.indexOf('-c')+1]};s.turns[s.sessions[name].id]=[];save(s);}
else if(args.includes('display-message'))process.stdout.write(String(s.server.pid));
else if(args.includes('kill-server')){const raw=fs.readFileSync('/proc/'+s.server.pid+'/stat','utf8');if(raw.slice(raw.lastIndexOf(')')+2).split(' ')[19]!==s.server.birth)process.exit(92);process.kill(s.server.pid,'SIGTERM');}
else if(args.includes('capture-pane'))process.stdout.write('Thread ID: '+part().id+'\\n›');
else if(args.includes('list-panes'))process.stdout.write(process.env.FIXTURE_TUI_PID+'\\n');
else if(args.includes('list-sessions'))process.stdout.write(Object.keys(s.sessions).join('\\n')+'\\n');
else if(args.includes('send-keys')){if(args.includes('-l')){s.sessions[session].input=args.at(-1);save(s);}
else {const text=part().input;if(text==='/status'||text==='/quit')process.exit(0);
const turn={id:'t'+(++s.counter),status:'completed',items:[{type:'userMessage',text}]};
if(text.startsWith('Run exactly')){const command=text.split('approvals: ')[1];let code=0;try{cp.execFileSync('/bin/sh',['-c',command],{env:{...process.env,CODEX_THREAD_ID:part().id},stdio:'pipe'});}catch(e){code=e.status??1;}
turn.items.push({type:'commandExecution',command,cwd:part().cwd,exitCode:code,status:'completed'});
const fresh=read();fresh.counter=Math.max(fresh.counter,s.counter);fresh.turns[part().id].push(turn);save(fresh);
}else{turn.items.push({type:'agentMessage',text:'FACT none'});s.turns[part().id].push(turn);save(s);}}}
`;
  writeFileSync(f.m.tools.tmux, tmuxProgram, { mode: 0o700 });
  const binaryProgram = prologue + `
if(!process.env.FIXTURE_LOCKED){const r=cp.spawnSync('/usr/bin/flock',['--exclusive',file+'.lock',process.execPath,__filename,...args],{env:{...process.env,FIXTURE_LOCKED:'1'},stdio:'inherit'});process.exit(r.status??1);}
const s=read(),profile=args[args.indexOf('--net')+1],p=s.peers.find(p=>p.profile===profile),root=path.dirname(file),data=path.join(process.env.XDG_DATA_HOME,'vecnet/codex-delivery'),runtime=path.join(process.env.XDG_RUNTIME_DIR,'vecnet');
function owner(hub){const slug=slugs[hub];put(path.join(runtime,'delivery-'+slug+'.json'),{hub:urls[hub],state_root:data,registry:path.join(data,'registry.json'),managed:true,binary_digest:crypto.createHash('sha256').update(fs.readFileSync(__filename)).digest('hex'),launcher:process.env.VECNET_CODEX_DELIVERY_ENTRY,pid:12345,birth:'fixture'});fs.writeFileSync(path.join(runtime,'delivery-'+slug+'.lock'),'',{mode:384});fs.writeFileSync(path.join(runtime,slug+'.active'),'');
put(path.join(data,slug,'descriptor.json'),{version:1,hub:urls[hub]});put(path.join(data,slug,'hub.json'),{version:1,hub:urls[hub]});const ledger=path.join(data,slug,'ledger.sqlite');if(!fs.existsSync(ledger)){const db=new (require('node:sqlite').DatabaseSync)(ledger);db.exec('CREATE TABLE kv(ns TEXT,key TEXT,value TEXT)');db.close();fs.chmodSync(ledger,384);}}
if(args.includes('peers')&&process.env.VECNET_CODEX_AUTO_DELIVERY==='1'){
const binding=path.join(root,'codex/bindings.json'),b=fs.existsSync(binding)?JSON.parse(fs.readFileSync(binding)):{version:1,bindings:[],disabled:[]};
if(!b.disabled.some(d=>d.profile===p.profile)){b.bindings=b.bindings.filter(r=>r.profile!==p.profile);b.bindings.push({profile:p.profile,as:'',codex_home:process.env.CODEX_HOME,thread_id:process.env.CODEX_THREAD_ID,hub:urls[p.hub],caller:p.peerId,created_at:new Date().toISOString(),expires_at:new Date(Date.now()+3600000).toISOString()});put(binding,b);
const reg=path.join(data,'registry.json'),r=fs.existsSync(reg)?JSON.parse(fs.readFileSync(reg)):{version:1,sources:[]};if(!r.sources.some(r=>r.config===p.config))r.sources.push({config:p.config,bindings:binding});put(reg,r);owner(p.hub);}}
if(args.includes('delivery-refresh'))owner(Object.keys(slugs).find(h=>slugs[h]===args.at(-1)));
if(args.includes('send')){const target=s.peers.find(p=>p.peerId===args[args.indexOf('send')+1]),body=fs.readFileSync(0,'utf8'),nonce=/codeword (NONCE-[a-f0-9]+)/.exec(body)[1],d='dly_'+(++s.counter),thread=s.sessions[target.session].id;
const key='vn.'+crypto.createHash('sha256').update(JSON.stringify([urls[target.hub],thread,[d]])).digest('hex').slice(0,24);
s.turns[thread].push({id:'t'+s.counter,status:'completed',items:[{type:'userMessage',clientId:key},{type:'agentMessage',text:'FACT '+d+' '+nonce}]});save(s);put(path.join(root,'state',target.profile+'.json'),{hub:urls[target.hub],caller:target.peerId,displayed:[d],claims:[]});process.stdout.write(JSON.stringify({deliveries:[{recipient:target.peerId,delivery_id:d}]}));}
else if(args.includes('unbind')){const file=path.join(root,'codex/bindings.json'),b=JSON.parse(fs.readFileSync(file));b.bindings=b.bindings.filter(r=>r.profile!==p.profile);b.disabled.push({profile:p.profile,as:''});put(file,b);process.stdout.write('{}');}
else if(args.includes('inbox'))process.stdout.write(JSON.stringify({events:s.turns[s.sessions[p.session].id].flatMap(t=>t.items??[]).filter(i=>i.type==='agentMessage'&&i.text.startsWith('FACT dly_')).map(i=>({type:'delivery',data:{delivery_id:i.text.split(' ')[1]}}))}));
else if(args.includes('rotate'))process.stdout.write(JSON.stringify({secret:'vn_fixture_rotated'}));
else if(args.includes('disable')){const peers=JSON.parse(fs.readFileSync(process.env.FIXTURE_PEERS));peers[args.at(-1)]=true;put(process.env.FIXTURE_PEERS,peers);process.stdout.write('{}');}
else if(!args.includes('hook'))process.stdout.write('{}');
`;
  for (const [name, g] of Object.entries(f.m.generations)) {
    writeFileSync(g.binarySource, binaryProgram + '\n// generation ' + name, { mode: 0o700 });
    g.binaryHash = createHash('sha256').update(readFileSync(g.binarySource)).digest('hex');
  }
  let managerProgram = readFileSync(f.m.tools.systemctl, 'utf8');
  managerProgram = managerProgram.replace("if(args.includes('show'))", "if(args.includes('stop'))for(const unit of args.filter(a=>a.endsWith('.service'))){const flag=p.join(process.env.FIXTURE_CLIENT_RUNTIME,'vecnet',unit.slice(16,-8)+'.active');if(fs.existsSync(flag))fs.unlinkSync(flag);}\nif(args.includes('show'))");
  managerProgram = managerProgram.replace("const row={Id:unit", "const active=fs.existsSync(p.join(process.env.FIXTURE_CLIENT_RUNTIME,'vecnet',unit.slice(16,-8)+'.active'));const row={Id:unit").replace("ActiveState:'inactive',SubState:'dead'", "ActiveState:active?'active':'inactive',SubState:active?'running':'dead'");
  managerProgram = managerProgram.replace("MainPID:'0'", "MainPID:active?'12345':'0'");
  writeFileSync(f.m.tools.systemctl, managerProgram, { mode: 0o700 });
  const run = f.ports.run; f.ports.run = (command, args, options) => run(command, args, { ...options, env: { ...options.env, NODE_OPTIONS: '--disable-warning=ExperimentalWarning', FIXTURE_FLOW: flow, FIXTURE_TUI_PID: String(process.pid) } });
  f.ports.ownerProof = (o, s) => ({ pid: o.pid, birth: o.birth, dev: s.dev, ino: s.ino, digest: o.binary_digest, entry: o.launcher });
  f.ports.tuiClosed = async () => true; // Fake TUI shares the test PID; this attests no native TUI drain.
  f.ports.rpc = { async call(method, params) {
    const s = JSON.parse(readFileSync(flow)), own = Object.values(s.sessions).find(v => v.id === params.threadId);
    assert.ok(own, 'only owned UUIDs may be observed');
    if (method === 'thread/read') return { thread: { cwd: own.cwd, status: { type: 'idle' } } };
    if (method === 'turn/start') { s.turns[params.threadId].push({ id: 't'+(++s.counter),status:'completed',items:[{type:'userMessage',clientId:params.clientUserMessageId},{type:'agentMessage',text:'FACT none'}] }); writeFileSync(flow,JSON.stringify(s)); return {}; }
    assert.equal(method, 'thread/turns/list'); const all = s.turns[params.threadId], offset = Number(params.cursor ?? 0);
    return { data: all.slice(offset, offset + 5), nextCursor: offset + 5 < all.length ? String(offset + 5) : null };
  } };
  f.ports.diagnostics = async ({ journal }) => { journal.proofs.push({ stage: 'diagnostics', actor: 'fixture-only', notices: 1 }); };
  f.save(); return f;
}
test('fake TUI, CLI and separate manager processes rehearse the full sequence without native acceptance claims', async t => {
  const f = await flowRehearsal(t), h = f.harness(); await h.install();
  assert.equal((await h.run()).code, 'evidence_complete');
  const journal = JSON.parse(readFileSync(join(f.m.runRoot,'journal.json')));
  for (const step of ['initial-a','initial-b','initial-a-dev','upgrade-binary','upgrade-assets','rotation','opt-out','diagnostics']) assert.ok(journal.completed.includes(step), step);
  assert.equal(Object.keys(journal.owners).length, 2);
  await h.cleanup(); const rolled = await h.rollback(); assert.equal(rolled.code, 'rollback_residuals');
  // Owner FD verification and diagnostics are explicitly fixture ports. No real native/managed/model proof follows.
});

// Fix round 1: prepare only; execution needs the new fix1 build-box permit.
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const exceptLifecycle = root => snapshot(root).filter(row => row[0] !== 'run/lifecycle.lock').map(([name, mode, ino, , bytes]) => [name, mode, ino, bytes]);
test('fix1 I1 simultaneous Go Displayed and ID Claim never proves a receipt; malformed claims refuse', async t => {
  const p = { threadId: uuid(1), clientId: 'vn.exact', deliveryId: 'dly_1', nonce: 'NONCE-fixture', historyComplete: true,
    controlBeforeNonce: true, hookBytes: 0, trustPreserved: true, inboxUnacked: true, expectedHub: 'https://vecnet.xk.io', expectedCaller: 'peer_0',
    display: { hub: 'https://vecnet.xk.io', caller: 'peer_0', shown: ['dly_1'], displayed: ['dly_1'], claims: [] },
    turns: [{ items: [{ type: 'userMessage', clientId: 'vn.exact' }, { type: 'agentMessage', text: 'FACT dly_1 NONCE-fixture' }] }] };
  assert.equal(proveDelivery(p).occurrences, 1);
  const realClaim = { ID: 'dly_1', After: '', Priority: 'immediate', Claim: 'claim_1', Order: 1 };
  const cases = [[realClaim], [{ id: 'dly_1' }], [{}], [null], ['dly_1'], {}, 'bad', [{ ...realClaim, ID: 7 }]];
  for (const [i, claims] of cases.entries()) {
    await t.test('claim-shape-' + i, () => assert.throws(() => proveDelivery({ ...p, display: { ...p.display, claims } }), { code: 'receipt_unproved' }));
  }
  for (const [i, displayed] of ['dly_1', [null], {}].entries()) await t.test('display-shape-' + i, () => assert.throws(() => proveDelivery({ ...p, display: { ...p.display, displayed } }), { code: 'receipt_unproved' }));
});
test('fix1 I2 reserves the actual manifest and recovery/control trees against credentials and artifacts', async t => {
  const f = fixture(t), manifest = join(f.m.runRoot, 'approved.json');
  const reserved = ['journal.json', 'cleanup.sh', 'rollback.sh', 'lifecycle.lock', 'tmux.sock', 'ws-a', 'ws-b', 'xdg', 'codex', 'state'];
  const cases = [m => { m.rotationFile = manifest; }, ...reserved.map(name => m => { m.operators.fly.config = join(m.runRoot, name); }),
    m => { m.generations.a.assetSource = join(m.runRoot, 'ws-a/artifacts'); },
    m => { m.generations.a.assetSource = join(m.runRoot, 'codex'); },
    m => { m.peers[0].config = join(m.runRoot, 'state/a.json'); },
    m => { m.generations.a.assetSource = join(m.runRoot, 'configs'); m.peers[0].config = join(m.runRoot, 'configs/private.toml'); }];
  for (const [i, edit] of cases.entries()) await t.test('reserved-path-' + i, () => { const m = structuredClone(f.m); edit(m); assert.throws(() => planHarness(m, manifest), { code: 'path_collision' }); });
});
test('fix1 I2 cleanup.sh config collision refuses before mutation or secret-bearing journal backup', async t => {
  const f = rehearsal(t), target = join(f.m.runRoot, 'cleanup.sh'); f.m.operators.fly.config = target;
  writeFileSync(target, 'PRIVATE-CREDENTIAL-CANARY', { mode: 0o600 }); f.save(); const before = snapshot(f.root);
  await assert.rejects(async () => f.harness().install(), { code: 'path_collision' });
  assert.deepEqual(snapshot(f.root), before); assert.ok(!existsSync(join(f.m.runRoot, 'journal.json')));
});

// Runs only in the permitted private build-box fixture; these ports cannot activate real main().
function fixtureDriver(f) {
  const driver = join(f.m.runRoot, 'fixture-driver.mjs'), descriptor = join(f.m.runRoot, 'fixture-driver.json');
  writeFileSync(descriptor, JSON.stringify({ manifest: f.manifest, manager: f.manager, peers: f.peers, trace: f.trace, runtime: f.ports.runtime }), { mode: 0o600 });
  writeFileSync(driver, `import fs from 'node:fs'; import {syncBuiltinESMExports} from 'node:module'; import {join} from 'node:path';
const d=JSON.parse(fs.readFileSync(${JSON.stringify(descriptor)})),m=JSON.parse(fs.readFileSync(d.manifest)),tag=process.argv[3]??'single',journal=join(m.runRoot,'journal.json');
const read=fs.readFileSync,rename=fs.renameSync,unlink=fs.unlinkSync;
fs.readFileSync=function(p,...args){if(String(p)===journal)fs.writeFileSync(join(m.runRoot,'read-'+tag),'read',{mode:384});return read.call(this,p,...args)};
const crash=(p,edge)=>{if(String(p)===process.env.FIXTURE_CRASH_TARGET&&edge===process.env.FIXTURE_CRASH_EDGE){fs.writeFileSync(join(m.runRoot,'crash-checkpoint'),edge,{mode:384});process.exit(73)}};
fs.renameSync=function(a,b){crash(b,'before');const v=rename(a,b);crash(b,'after');return v};fs.unlinkSync=function(p){crash(p,'before');const v=unlink(p);crash(p,'after');return v};syncBuiltinESMExports();
const {createHarness,runBounded}=await import(${JSON.stringify(new URL('./codex-auto-delivery-smoke.mjs', import.meta.url).href)});
const ports={manifestPath:d.manifest,runtime:d.runtime,machine:{host:'xsm',uid:process.geteuid(),home:m.approval.home},
run:(command,args,options)=>runBounded(command,args,{...options,env:command===m.tools.systemctl?{HOME:d.manager,XDG_CONFIG_HOME:join(d.manager,'config'),XDG_DATA_HOME:join(d.manager,'data'),XDG_RUNTIME_DIR:join(d.manager,'runtime'),PATH:'/usr/bin:/bin',FIXTURE_CLIENT_HOME:m.approval.home,FIXTURE_CLIENT_RUNTIME:join(m.runRoot,'runtime')}:{...options.env,FIXTURE_PEERS:d.peers,FIXTURE_TRACE:d.trace}}),
peerDisabled:async p=>JSON.parse(fs.readFileSync(d.peers))[p.id]===true,
rpc:{async call(){fs.writeFileSync(join(m.runRoot,'rpc-'+tag),'entered',{mode:384});if(process.env.FIXTURE_HOLD==='1'){const end=Date.now()+10000;while(!fs.existsSync(join(m.runRoot,'release-owner'))&&Date.now()<end)await new Promise(r=>setTimeout(r,25));}throw Object.assign(Error('fixture_stop'),{code:'fixture_stop'})}}};
try{const value=await createHarness(m,ports)[process.argv[2]]();process.stdout.write(JSON.stringify(value)+'\\n')}catch(e){process.stderr.write(JSON.stringify({code:e.code??'fixture_error'})+'\\n');process.exitCode=2}
`, { mode: 0o600 });
  return (mode, tag = 'single', extraEnv = {}) => {
    const p = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', driver, mode, tag], { env: { ...f.env, ...extraEnv }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = ''; p.stdout.on('data', b => { stdout += b; }); p.stderr.on('data', b => { stderr += b; });
    const timer = setTimeout(() => p.kill('SIGKILL'), 15000);
    const done = new Promise((resolve, reject) => { p.once('error', reject); p.once('close', code => { clearTimeout(timer); resolve({ code, stdout, stderr }); }); });
    return { p, done };
  };
}
for (const mode of ['publication', 'restore']) for (const kind of ['new-binary', 'prior-template']) for (const edge of ['before', 'after']) {
  test(`fix1 I3 ${edge} ${mode} of ${kind} reconciles interruption and rolls back`, async t => {
    const f = rehearsal(t), paths = planHarness(f.m).paths;
    if (kind === 'prior-template') { mkdirSync(dirname(paths.template), { recursive: true, mode: 0o700 }); writeFileSync(paths.template, 'prior template\n', { mode: 0o640 }); }
    const invoke = fixtureDriver(f), target = kind === 'new-binary' ? f.m.generations.a.binary : paths.template;
    if (mode === 'restore') { assert.equal((await invoke('install').done).code, 0); assert.equal((await invoke('cleanup').done).code, 0); }
    const interrupted = await invoke(mode === 'publication' ? 'install' : 'rollback', 'crash', { FIXTURE_CRASH_TARGET: target, FIXTURE_CRASH_EDGE: edge }).done;
    assert.equal(interrupted.code, 73, interrupted.stderr); assert.equal(readFileSync(join(f.m.runRoot, 'crash-checkpoint'), 'utf8'), edge);
    if (mode === 'publication') { const cleaned = await invoke('cleanup').done; assert.equal(cleaned.code, 0, cleaned.stderr); }
    const recovered = await invoke('rollback').done; assert.equal(recovered.code, 0, recovered.stderr);
    if (kind === 'prior-template') { assert.equal(readFileSync(target, 'utf8'), 'prior template\n'); assert.equal(lstatSync(target).mode & 0o777, 0o640); }
    else assert.equal(existsSync(target), false);
    if (existsSync(dirname(target))) assert.ok(!readdirSync(dirname(target)).some(name => name.startsWith(target.slice(target.lastIndexOf('/') + 1) + '.')), 'no orphaned publication/restore temporary file');
    const again = await invoke('rollback').done; assert.equal(again.code, 0, again.stderr);
    assert.ok(!existsSync(f.m.peers[0].config));
  });
}
for (const competitor of ['run', 'cleanup']) test(`fix1 I4 competing run/${competitor} serializes before journal read and preserves the stable lock`, async t => {
  const f = rehearsal(t); await f.harness().install(); const jp = join(f.m.runRoot, 'journal.json'), j = JSON.parse(readFileSync(jp));
  const socket = createServer(), sp = join(f.m.runRoot, 'tmux.sock'); await new Promise(resolve => socket.listen(sp, resolve));
  f.beforeRemove.push(() => new Promise(resolve => socket.close(resolve)));
  const s = lstatSync(sp), raw = readFileSync(`/proc/${process.pid}/stat`, 'utf8');
  j.tmux = { dev: s.dev, ino: s.ino, pid: process.pid, birth: raw.slice(raw.lastIndexOf(')') + 2).split(' ')[19] };
  writeFileSync(join(f.m.runRoot, 'fixture-server-pid'), String(process.pid), { mode: 0o600 });
  j.threads.a = { id: uuid(1), cwd: join(f.m.runRoot, 'ws-a'), firstMarker: 'OWN-fixture', uuidFromOwnTui: true, pid: process.pid, birth: 'fixture' };
  writeFileSync(jp, JSON.stringify(j), { mode: 0o600 }); const invoke = fixtureDriver(f), owner = invoke('run', 'owner', { FIXTURE_HOLD: '1' });
  try {
    const deadline = Date.now() + 5000; while (!existsSync(join(f.m.runRoot, 'rpc-owner')) && Date.now() < deadline) await delay(25);
    assert.ok(existsSync(join(f.m.runRoot, 'rpc-owner')), 'first run reached its private RPC barrier');
    const before = readFileSync(jp, 'utf8'), lock = join(f.m.runRoot, 'lifecycle.lock'), inode = existsSync(lock) ? lstatSync(lock).ino : null;
    const result = await invoke(competitor, 'contender').done;
    assert.equal(result.code, 2); assert.match(result.stderr, /"code":"lock_(?:timeout|refused)"/);
    assert.equal(existsSync(join(f.m.runRoot, 'read-contender')), false, 'contender never reads or rewrites the journal');
    assert.equal(readFileSync(jp, 'utf8'), before); assert.deepEqual(JSON.parse(readFileSync(f.trace)), []);
    assert.ok(inode); assert.equal(lstatSync(lock).ino, inode);
  } finally { writeFileSync(join(f.m.runRoot, 'release-owner'), '', { mode: 0o600 }); await owner.done; }
});
test('fix1 I4 replaced lifecycle inode refuses a resumed mode without operator actions', async t => {
  const f = rehearsal(t); await f.harness().install(); const lock = join(f.m.runRoot, 'lifecycle.lock'), saved = lock + '.retained';
  renameSync(lock, saved); writeFileSync(lock, '', { mode: 0o600 });
  await assert.rejects(f.harness().cleanup(), { code: 'ownership_changed' }); assert.deepEqual(JSON.parse(readFileSync(f.trace)), []);
  rmSync(lock); renameSync(saved, lock); await f.harness().cleanup(); await f.harness().rollback();
});
for (const inheritPipe of [true, false]) test(`fix1 I5 supervises ${inheritPipe ? 'grandchild pipe hold' : 'late-writing descendant'} after leader exit`, async t => {
  const f = fixture(t), marker = join(f.root, 'late-write'), ready = join(f.root, 'descendant-started'), descendant = `const fs=require('node:fs');fs.writeFileSync(${JSON.stringify(ready)},'started');setTimeout(()=>fs.writeFileSync(${JSON.stringify(marker)},'late'),1500);setTimeout(()=>process.exit(0),2100)`;
  const leader = `const cp=require('node:child_process');const p=cp.spawn(process.execPath,['-e',${JSON.stringify(descendant)}],{stdio:${JSON.stringify(inheritPipe ? ['ignore', 'inherit', 'inherit'] : 'ignore')}});p.unref();`;
  const start = Date.now(); let outcome;
  try { await runBounded(process.execPath, ['-e', leader], { env: f.env, timeoutMs: 500 }); outcome = 'success'; } catch (e) { outcome = e.code; }
  const elapsed = Date.now() - start;
  // The baseline descendant self-exits in 2.1s even when assertions fail; keep its private root until then.
  await delay(Math.max(0, 2600 - elapsed)); assert.ok(existsSync(ready), 'a real descendant started before supervision');
  assert.match(outcome, /^child_(?:timeout|ambiguous|drain_unproved)$/); assert.ok(elapsed < 1300, `bounded completion took ${elapsed}ms`);
  assert.equal(existsSync(marker), false, 'no descendant writes after the operation deadline');
});
const sourceEdits = {
  'config hub substitution': (config, bindings, original) => writeFileSync(config, original.toString().replace('https://vecnet.xk.io', 'https://foreign.invalid')),
  'config caller substitution': (config, bindings, original) => writeFileSync(config, original.toString().replace('peer_0', 'foreign_peer')),
  'extra same-path binding': (config, bindings, original, b) => { b.bindings.push({ ...b.bindings[0], profile: 'foreign', thread_id: uuid(99) }); writeFileSync(bindings, JSON.stringify(b)); },
  'changed canonical home': (config, bindings, original, b) => { b.bindings[0].codex_home = '/foreign/home'; writeFileSync(bindings, JSON.stringify(b)); },
  'changed as identity': (config, bindings, original, b) => { b.bindings[0].as = 'foreign_as'; writeFileSync(bindings, JSON.stringify(b)); },
};
for (const [name, edit] of Object.entries(sourceEdits)) test('fix1 I6 ' + name + ' refuses before cleanup actions', async t => {
  const f = await flowRehearsal(t), h = f.harness(); await h.install(); assert.equal((await h.run()).code, 'evidence_complete');
  const config = f.m.peers[0].config, bindings = join(dirname(config), 'codex/bindings.json'), original = readFileSync(config), originalBindings = readFileSync(bindings);
  edit(config, bindings, original, JSON.parse(originalBindings)); const trace = readFileSync(f.trace, 'utf8'), disabled = readFileSync(f.peers, 'utf8');
  await assert.rejects(h.cleanup(), { code: 'ownership_changed' });
  assert.equal(readFileSync(f.trace, 'utf8'), trace); assert.equal(readFileSync(f.peers, 'utf8'), disabled);
  assert.ok(existsSync(f.m.generations.a.binary)); writeFileSync(config, original); writeFileSync(bindings, originalBindings);
  await h.cleanup(); await h.rollback();
});
for (const role of ['operator', 'sender']) test('fix1 I6 ' + role + ' config pins refuse substitution before any CLI action', async t => {
  const f = rehearsal(t); await f.harness().install(); const p = role === 'operator' ? f.m.operators.fly : f.m.peers.find(p => p.role === 'sender');
  writeFileSync(p.config, `[profiles.${p.profile}]\nhub = "https://foreign.invalid"\npeer_id = "foreign"\nsecret = "vn_fixture"\n`);
  await assert.rejects(f.harness().cleanup(), { code: 'ownership_changed' }); assert.deepEqual(JSON.parse(readFileSync(f.trace)), []);
});
test('fix1 I7 preexisting private tmux server refuses before install or new-session', async t => {
  const f = rehearsal(t), socket = createServer(); await new Promise(resolve => socket.listen(join(f.m.runRoot, 'tmux.sock'), resolve));
  t.after(() => new Promise(resolve => socket.close(resolve))); const before = exceptLifecycle(f.root);
  await assert.rejects(f.harness().install(), { code: 'preexisting_consumer' });
  assert.deepEqual(exceptLifecycle(f.root), before); assert.ok(!existsSync(join(f.m.runRoot, 'journal.json')));
});
test('fix1 I7 resume refuses substituted tmux identity before observing history or sending keys', async t => {
  const f = rehearsal(t); await f.harness().install(); const socket = createServer(), sp = join(f.m.runRoot, 'tmux.sock');
  await new Promise(resolve => socket.listen(sp, resolve)); t.after(() => new Promise(resolve => socket.close(resolve)));
  const jp = join(f.m.runRoot, 'journal.json'), j = JSON.parse(readFileSync(jp)), s = lstatSync(sp);
  j.tmux = { dev: s.dev, ino: s.ino + 1 }; j.threads.a = { id: uuid(1), uuidFromOwnTui: true, cwd: join(f.m.runRoot, 'ws-a'), firstMarker: 'OWN-fixture' };
  writeFileSync(jp, JSON.stringify(j), { mode: 0o600 }); const result = await fixtureDriver(f)('run', 'substitution').done;
  assert.equal(result.code, 2); assert.match(result.stderr, /"code":"ownership_changed"/);
  assert.ok(!existsSync(join(f.m.runRoot, 'rpc-substitution'))); assert.deepEqual(JSON.parse(readFileSync(f.trace)), []);
});
test('fix1 I8 M1 nondefault manifest dry-run and invoked recovery wrappers use identical arguments and targeted warning suppression', async t => {
  const f = rehearsal(t), approved = join(f.m.runRoot, 'approved.json'); writeFileSync(approved, JSON.stringify(f.m), { mode: 0o600 });
  const h = createHarness(f.m, { ...f.ports, manifestPath: approved }), plan = planHarness(f.m, approved); await h.install();
  const capture = join(f.m.runRoot, 'capture.mjs'), resultPath = join(f.m.runRoot, 'wrapper-argv.json');
  writeFileSync(capture, `import fs from 'node:fs';fs.writeFileSync(${JSON.stringify(resultPath)},JSON.stringify({argv:process.argv.slice(1),execArgv:process.execArgv}),{mode:384});process.exit(0);`, { mode: 0o600 });
  for (const mode of ['cleanup', 'rollback']) {
    const wrapper = join(f.m.runRoot, mode + '.sh');
    await t.test(mode + ' wrapper equals reviewed dry-run', () => assert.equal(readFileSync(wrapper, 'utf8'), plan[mode]));
    // Capture before main's real-account authorization; wrapper execution itself stays fixture-only.
    const result = await runBounded('/bin/sh', [wrapper], { env: { ...f.env, NODE_OPTIONS: '--import=' + capture } });
    assert.equal(result.stderr, ''); const actual = JSON.parse(readFileSync(resultPath));
    await t.test(mode + ' invoked manifest path', () => assert.deepEqual(actual.argv, [cli, '--manifest', approved, '--' + mode]));
    await t.test(mode + ' targeted warning option', () => assert.ok(actual.execArgv.includes('--disable-warning=ExperimentalWarning')));
  }
  await h.cleanup(); await h.rollback();
});
test('fix1 I9 actual just recipe forwards manifest and dry-run arguments without side effects', async t => {
  const f = fixture(t), before = snapshot(f.root), repo = dirname(dirname(cli));
  const result = await runBounded('/usr/bin/env', ['just', '--justfile', join(repo, 'justfile'), '--working-directory', repo,
    'codex-auto-delivery-smoke', '--manifest', f.manifest, '--dry-run'], { okay: [0, 1, 2, 127], env: { ...f.env, PATH: dirname(process.execPath) + ':' + process.env.PATH, VECNET_CODEX_AUTO_DELIVERY_SMOKE: '1' } });
  assert.equal(result.code, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).mode, 'dry-run'); assert.ok(JSON.parse(result.stdout).cleanup.includes(f.manifest));
  assert.deepEqual(snapshot(f.root), before); assert.doesNotMatch(result.stderr, /\(node:\d+\) ExperimentalWarning:/);
});
test('fix1 I10 existing share mode 0755 refuses before private metadata or manager changes', async t => {
  const f = rehearsal(t), share = planHarness(f.m).paths.share; mkdirSync(share, { recursive: true, mode: 0o755 }); chmodSync(share, 0o755);
  const before = exceptLifecycle(f.root); await assert.rejects(f.harness().install(), { code: 'unsafe_path' });
  assert.deepEqual(exceptLifecycle(f.root), before); assert.equal(lstatSync(share).mode & 0o777, 0o755);
  assert.ok(!existsSync(join(share, 'delivery.json'))); assert.ok(!existsSync(join(f.m.runRoot, 'journal.json')));
});

// R50 controller-xsm-manager-metadata.json (SHA256 143a2fa59304f3fe5cdb65896f3775e7b4bdb44dee46eb7eb30d8d7cf0cd4f9f):
// both selected delivery units have this exact five-property tuple. MainPID and
// DropInPaths below are explicit private-fixture additions, not new live evidence.
const absentManagerState = { LoadState: 'not-found', ActiveState: 'inactive', SubState: 'dead', UnitFileState: '', Job: '' };
function actualManagerFixture(f, edit = () => {}, duplicate = '') {
  const units = [...planHarness(f.m).units, 'vecnet-launcher.service'];
  const rows = units.map(Id => ({ Id, MainPID: '0', ...absentManagerState, DropInPaths: '' })); edit(rows);
  const calls = join(f.manager, 'selected-argv.json');
  writeFileSync(f.m.tools.systemctl, `#!${process.execPath}
const fs=require('node:fs'),args=process.argv.slice(2),rows=${JSON.stringify(rows)};
fs.writeFileSync(${JSON.stringify(calls)},JSON.stringify(args),{mode:384});
for(const unit of args.filter(a=>a.endsWith('.service'))){const row=rows.find(r=>r.Id===unit);if(!row)continue;
process.stdout.write(Object.entries(row).map(([k,v])=>k+'='+v).join('\\n')+${JSON.stringify(duplicate ? '\n' + duplicate : '')}+'\\n\\n');}
`, { mode: 0o700 });
  return calls;
}
test('fix1 N1 real absent manager unit accepts empty UnitFileState', async t => {
  const f = rehearsal(t), calls = actualManagerFixture(f), h = f.harness();
  assert.deepEqual(await h.preflight(), { stage: 'preflight', code: 'ready' });
  const argv = JSON.parse(readFileSync(calls));
  assert.ok(argv.includes('--property=Job')); assert.ok(argv.includes('--property=DropInPaths'));
  assert.deepEqual(argv.filter(a => a.endsWith('.service')), [...planHarness(f.m).units, 'vecnet-launcher.service']);
  assert.ok(!existsSync(join(f.m.runRoot, 'journal.json')));
  assert.ok(!existsSync(planHarness(f.m).paths.template)); assert.deepEqual(JSON.parse(readFileSync(f.trace)), []);
});
test('fix1 N1 absent-manager allowance refuses unknown live dropin job and ambiguous states', async t => {
  const cases = [
    ['unknown load', r => { r[0].LoadState = 'error'; }], ['missing load', r => { delete r[0].LoadState; }],
    ['loaded empty file state', r => { r[0].LoadState = 'loaded'; }], ['transient file state', r => { r[0].UnitFileState = 'transient'; }],
    ['invented not-found file state', r => { r[0].UnitFileState = 'not-found'; }], ['missing file state', r => { delete r[0].UnitFileState; }],
    ['live active', r => { r[0].ActiveState = 'active'; }], ['missing active', r => { delete r[0].ActiveState; }],
    ['unknown substate', r => { r[0].SubState = 'exited'; }], ['missing substate', r => { delete r[0].SubState; }],
    ['live PID', r => { r[0].MainPID = '12345'; }], ['dropin', r => { r[0].DropInPaths = '/fixture/foreign.conf'; }],
    ['missing dropins', r => { delete r[0].DropInPaths; }], ['queued job', r => { r[0].Job = '17 /org/freedesktop/systemd1/job/17'; }],
    ['missing job', r => { delete r[0].Job; }], ['missing selected unit', r => { r.splice(1, 1); }],
    ['loaded disabled queued job', r => { Object.assign(r[0], { LoadState: 'loaded', UnitFileState: 'disabled', Job: '17 /org/freedesktop/systemd1/job/17' }); }],
    ['loaded disabled unknown load', r => { Object.assign(r[0], { LoadState: 'unknown', UnitFileState: 'disabled' }); }],
    ['loaded disabled unknown substate', r => { Object.assign(r[0], { LoadState: 'loaded', UnitFileState: 'disabled', SubState: 'unknown' }); }],
  ];
  for (const [name, edit] of cases) await t.test(name, async t => {
    const f = rehearsal(t); actualManagerFixture(f, edit);
    await assert.rejects(f.harness().preflight(), { code: 'preexisting_consumer' });
    assert.ok(!existsSync(join(f.m.runRoot, 'journal.json'))); assert.deepEqual(JSON.parse(readFileSync(f.trace)), []);
  });
  await t.test('duplicate property is ambiguous', async t => {
    const f = rehearsal(t); actualManagerFixture(f, () => {}, 'Job=');
    await assert.rejects(f.harness().preflight(), { code: 'manager_unknown' });
  });
});
