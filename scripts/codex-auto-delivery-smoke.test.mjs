// Controller-only harness contract. Run only through a newly permitted x-boss lane.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:net';
import { createHash } from 'node:crypto';
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { validateManifest, planHarness, proveActor, proveDelivery, sanitizeOutcome, createHarness, runBounded, treeHash }
  from './codex-auto-delivery-smoke.mjs';

const cli = fileURLToPath(new URL('./codex-auto-delivery-smoke.mjs', import.meta.url));
const hash = 'a'.repeat(64), hashB = 'b'.repeat(64);
const uuid = n => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'vn-auto-smoke-'));
  chmodSync(root, 0o700);
  t.after(() => rmSync(root, { recursive: true, force: true }));
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
  return { root, m, manifest, save, env };
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
 const row={Id:unit,MainPID:'0',LoadState:loaded?'loaded':'not-found',ActiveState:'inactive',SubState:'dead',UnitFileState:loaded?'disabled':'not-found',DropInPaths:''};
 if(args.includes('--property=ExecStart')){const m=JSON.parse(fs.readFileSync(manifest)),slug=unit.slice('vecnet-delivery@'.length,-'.service'.length);
 row.ExecStart='{ path='+m.binary+' ; argv[]='+m.binary+' codex deliver --hub-id '+slug+' ; ignore_errors=no ; }';row.UMask='0077';
 row.Environment='HOME='+home+' XDG_DATA_HOME='+p.join(home,'.local/share')+' XDG_RUNTIME_DIR='+process.env.FIXTURE_CLIENT_RUNTIME+' VECNET_BIN='+m.binary+' VECNET_CODEX_DELIVERY_ENTRY='+m.entry+' PATH='+m.path;}
 process.stdout.write(Object.entries(row).map(([k,v])=>k+'='+v).join('\\n')+'\\n\\n');}
`;
  for (const name of ['codex', 'tmux', 'systemctl', 'script']) writeFileSync(f.m.tools[name], name === 'systemctl' ? managerProgram : `#!${process.execPath}\nprocess.exit(0);`, { mode: 0o700 });
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
test('fake TUI, CLI and separate manager processes rehearse the full sequence without native acceptance claims', async t => {
  const f = rehearsal(t), flow = join(f.m.runRoot, 'flow.json'), plan = planHarness(f.m);
  const socket = createServer(); await new Promise(resolve => socket.listen(join(f.m.runRoot, 'tmux.sock'), resolve));
  t.after(() => new Promise(resolve => socket.close(resolve)));
  writeFileSync(flow, JSON.stringify({ peers: f.m.peers, sessions: {}, turns: {}, counter: 0 }), { mode: 0o600 });
  const prologue = `#!${process.execPath}
const fs=require('node:fs'),path=require('node:path'),cp=require('node:child_process'),crypto=require('node:crypto');
const file=process.env.FIXTURE_FLOW,args=process.argv.slice(2),read=()=>JSON.parse(fs.readFileSync(file)),save=s=>fs.writeFileSync(file,JSON.stringify(s));
const urls={fly:'https://vecnet.xk.io',dev:'http://127.0.0.1:45752'},slugs=${JSON.stringify(Object.fromEntries(plan.units.map((unit, i) => [i ? 'dev' : 'fly', unit.slice(16, -8)])))};
const put=(p,v)=>{fs.mkdirSync(path.dirname(p),{recursive:true,mode:448});fs.writeFileSync(p,JSON.stringify(v),{mode:384});};
`;
  const tmuxProgram = prologue + `
const s=read(),session=args[args.indexOf('-t')+1],part=()=>s.sessions[session];
if(args.includes('new-session')){const name=args[args.indexOf('-s')+1];s.sessions[name]={id:name==='a'?${JSON.stringify(uuid(1))}:${JSON.stringify(uuid(2))},cwd:args[args.indexOf('-c')+1]};s.turns[s.sessions[name].id]=[];save(s);}
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
if(!b.disabled.some(d=>d.profile===p.profile)){b.bindings=b.bindings.filter(r=>r.profile!==p.profile);b.bindings.push({profile:p.profile,thread_id:process.env.CODEX_THREAD_ID,hub:urls[p.hub],caller:p.peerId});put(binding,b);
const reg=path.join(data,'registry.json'),r=fs.existsSync(reg)?JSON.parse(fs.readFileSync(reg)):{version:1,sources:[]};if(!r.sources.some(r=>r.config===p.config))r.sources.push({config:p.config,bindings:binding});put(reg,r);owner(p.hub);}}
if(args.includes('delivery-refresh'))owner(Object.keys(slugs).find(h=>slugs[h]===args.at(-1)));
if(args.includes('send')){const target=s.peers.find(p=>p.peerId===args[args.indexOf('send')+1]),body=fs.readFileSync(0,'utf8'),nonce=/codeword (NONCE-[a-f0-9]+)/.exec(body)[1],d='dly_'+(++s.counter),thread=s.sessions[target.session].id;
const key='vn.'+crypto.createHash('sha256').update(JSON.stringify([urls[target.hub],thread,[d]])).digest('hex').slice(0,24);
s.turns[thread].push({id:'t'+s.counter,status:'completed',items:[{type:'userMessage',clientId:key},{type:'agentMessage',text:'FACT '+d+' '+nonce}]});save(s);put(path.join(root,'state',target.profile+'.json'),{hub:urls[target.hub],caller:target.peerId,displayed:[d],claims:[]});process.stdout.write(JSON.stringify({deliveries:[{recipient:target.peerId,delivery_id:d}]}));}
else if(args.includes('unbind')){const file=path.join(root,'codex/bindings.json'),b=JSON.parse(fs.readFileSync(file));b.bindings=b.bindings.filter(r=>r.profile!==p.profile);b.disabled.push({profile:p.profile});put(file,b);process.stdout.write('{}');}
else if(args.includes('inbox'))process.stdout.write(JSON.stringify({events:s.turns[s.sessions[p.session].id].flatMap(t=>t.items??[]).filter(i=>i.type==='agentMessage'&&i.text.startsWith('FACT dly_')).map(i=>({type:'delivery',data:{delivery_id:i.text.split(' ')[1]}}))}));
else if(args.includes('rotate'))process.stdout.write(JSON.stringify({secret:'vn_fixture_rotated'}));
else if(args.includes('disable')){const peers=JSON.parse(fs.readFileSync(process.env.FIXTURE_PEERS));peers[args.at(-1)]=true;put(process.env.FIXTURE_PEERS,peers);process.stdout.write('{}');}
else if(!args.includes('hook'))process.stdout.write('{}');
`;
  for (const [name, g] of Object.entries(f.m.generations)) {
    writeFileSync(g.binarySource, binaryProgram + '\n// generation ' + name, { mode: 0o700 });
    g.binaryHash = createHash('sha256').update(readFileSync(g.binarySource)).digest('hex');
  }
  for (const p of f.m.peers.filter(p => p.role === 'receiver')) {
    const current = existsSync(p.config) ? readFileSync(p.config, 'utf8') : '';
    writeFileSync(p.config, current.replace('PRIVATE-CREDENTIAL-CANARY', '') + `\n[profiles.${p.profile}]\nsecret = "vn_fixture_old"\nhub = "${p.hub}"\npeer_id = "${p.peerId}"\n`, { mode: 0o600 });
  }
  let managerProgram = readFileSync(f.m.tools.systemctl, 'utf8');
  managerProgram = managerProgram.replace("if(args.includes('show'))", "if(args.includes('stop'))for(const unit of args.filter(a=>a.endsWith('.service'))){const flag=p.join(process.env.FIXTURE_CLIENT_RUNTIME,'vecnet',unit.slice(16,-8)+'.active');if(fs.existsSync(flag))fs.unlinkSync(flag);}\nif(args.includes('show'))");
  managerProgram = managerProgram.replace("const row={Id:unit", "const active=fs.existsSync(p.join(process.env.FIXTURE_CLIENT_RUNTIME,'vecnet',unit.slice(16,-8)+'.active'));const row={Id:unit").replace("ActiveState:'inactive',SubState:'dead'", "ActiveState:active?'active':'inactive',SubState:active?'running':'dead'");
  managerProgram = managerProgram.replace("MainPID:'0'", "MainPID:active?'12345':'0'");
  writeFileSync(f.m.tools.systemctl, managerProgram, { mode: 0o700 });
  const run = f.ports.run; f.ports.run = (command, args, options) => run(command, args, { ...options, env: { ...options.env, FIXTURE_FLOW: flow, FIXTURE_TUI_PID: String(process.pid) } });
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
  f.save(); const h = f.harness(); await h.install();
  assert.equal((await h.run()).code, 'evidence_complete');
  const journal = JSON.parse(readFileSync(join(f.m.runRoot,'journal.json')));
  for (const step of ['initial-a','initial-b','initial-a-dev','upgrade-binary','upgrade-assets','rotation','opt-out','diagnostics']) assert.ok(journal.completed.includes(step), step);
  assert.equal(Object.keys(journal.owners).length, 2);
  await h.cleanup(); const rolled = await h.rollback(); assert.equal(rolled.code, 'rollback_residuals');
  // Owner FD verification and diagnostics are explicitly fixture ports. No real native/managed/model proof follows.
});
