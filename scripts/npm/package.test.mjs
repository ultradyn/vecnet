import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));
const manifest = JSON.parse(await readFile(path.join(root, 'npm', 'package.json'), 'utf8'));
const tarball = path.join(root, 'dist', `vecnet-${manifest.version}.tgz`);
const platforms = ['linux-arm64', 'linux-x64'];
const commands = ['vecnet', 'vecnet-hub'];

function deadline(promise, ms, label) {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(label)), ms); }),
  ]).finally(() => clearTimeout(timer));
}

test('release contains only runtime files and complete native binaries', async t => {
  const packed = execFileSync('tar', ['-tzf', tarball], { encoding: 'utf8' }).trim().split('\n').sort();
  const binaries = platforms.flatMap(platform => commands.map(command => `native/${platform}/${command}`));
  const expected = [
    'package.json', 'README.md', 'BUILD_INFO.json', 'THIRD_PARTY_NOTICES.txt',
    'bin/run.cjs', 'bin/vecnet.cjs', 'bin/vecnet-hub.cjs', ...binaries,
  ].map(file => `package/${file}`).sort();
  const runtime = packed.filter(file => file.startsWith('package/connectors/'));
  for (const file of runtime) assert.match(file, /^package\/connectors\/(launcher|hub-client)\/(package\.json|src\/[A-Za-z0-9_./-]+\.ts|schema\/launch-(input|permission|question|answers|approval-closed)\.v1\.schema\.json|systemd\/vecnet-launcher-profile\.service\.in)$/);
  assert.deepEqual(packed.filter(file => !file.startsWith('package/connectors/')), expected);
  const extracted = await mkdtemp(path.join(os.tmpdir(), 'vecnet-npm-inspect-'));
  t.after(() => rm(extracted, { recursive: true, force: true }));
  execFileSync('tar', ['-xzf', tarball, '-C', extracted]);
  const contents = path.join(extracted, 'package');
  const packedManifest = JSON.parse(await readFile(path.join(contents, 'package.json'), 'utf8'));
  assert.equal(packedManifest.name, 'vecnet');
  assert.equal(packedManifest.version, manifest.version);
  assert.deepEqual(packedManifest.os, ['linux']);
  assert.deepEqual(packedManifest.cpu, ['x64', 'arm64']);
  const metadata = JSON.parse(await readFile(path.join(contents, 'BUILD_INFO.json'), 'utf8'));
  assert.equal(metadata.version, manifest.version);
  assert.deepEqual(Object.keys(metadata.binaries).sort(), binaries.sort());
  for (const file of binaries) {
    const bytes = await readFile(path.join(contents, file));
    assert.equal(createHash('sha256').update(bytes).digest('hex'), metadata.binaries[file], file);
    assert.ok((await stat(path.join(contents, file))).mode & 0o111, `${file} is executable`);
    const signature = file.includes('/linux-') ? '7f454c46' : 'cffaedfe';
    assert.equal(bytes.subarray(0, 4).toString('hex'), signature, `${file} native file format`);
  }
  assert.equal(packedManifest.scripts, undefined, 'installation needs no lifecycle scripts');
  assert.equal(packedManifest.dependencies, undefined, 'runtime has no npm dependencies');
  const notices = await readFile(path.join(contents, 'THIRD_PARTY_NOTICES.txt'), 'utf8');
  assert.match(notices, /modernc\.org\/sqlite/);
  assert.match(notices, /copyright/i);
});

test('packed npm installation runs both commands and exchanges messages through its hub', { timeout: 90_000 }, async t => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'vecnet-npm-'));
  let stopHub;
  t.after(async () => {
    try { if (stopHub) await stopHub(); }
    finally { await rm(temporary, { recursive: true, force: true }); }
  });
  const prefix = path.join(temporary, 'prefix');
  const env = { ...process.env };
  for (const key of Object.keys(env)) if (key.startsWith('VECNET_')) delete env[key];
  for (const [key, sub] of Object.entries({HOME:'home', XDG_CONFIG_HOME:'config', CODEX_HOME:'codex', XDG_RUNTIME_DIR:'runtime', XDG_DATA_HOME:'data'})) {
    env[key] = path.join(temporary, sub); await mkdir(env[key], {mode:0o700});
  }
  env.XDG_CACHE_HOME = path.join(temporary, 'cache');
  env.VECNET_CONFIG = path.join(temporary, 'unused-config.toml');
  execFileSync('npm', [
    'install', '--global', '--prefix', prefix, '--offline', '--ignore-scripts',
    '--no-audit', '--no-fund', tarball,
  ], { cwd: temporary, env, encoding: 'utf8', timeout: 45_000 });

  const cli = path.join(prefix, 'bin', 'vecnet');
  const hubBin = path.join(prefix, 'bin', 'vecnet-hub');
  function run(binary, args, extra = {}) {
    return spawnSync(binary, args, { cwd: temporary, env, encoding: 'utf8', timeout: 10_000, ...extra });
  }
  function mustRun(binary, args, extra) {
    const result = run(binary, args, extra);
    assert.equal(result.status, 0, `${path.basename(binary)} ${args[0]}: ${result.stderr || result.error || 'failed'}`);
    return result.stdout;
  }
  for (const [binary, command] of [[cli, 'vecnet'], [hubBin, 'vecnet-hub']]) {
    assert.equal(mustRun(binary, ['--version']).trim(), `${command} ${manifest.version}`);
  }
  assert.match(mustRun(cli, ['help']), /usage: vecnet/);
  assert.equal(run(cli, ['--definitely-invalid-flag']).status, 1, 'native usage exit code passes through');
  assert.match(run(hubBin, ['--help']).stderr, /data-dir/, 'hub arguments reach the native binary');

  const dataDir = path.join(temporary, 'hub-data');
  const initialised = mustRun(hubBin, ['init', '--data-dir', dataDir, '--operator-name', 'npm-smoke']);
  const operatorSecret = initialised.match(/operator secret:\s+(\S+)/)?.[1];
  assert.ok(operatorSecret, 'hub init returned an operator credential');
  const hub = spawn(hubBin, ['--listen', '127.0.0.1:0', '--data-dir', dataDir], {
    cwd: temporary, env, detached: true, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const exited = new Promise(resolve => hub.once('exit', (code, signal) => resolve({ code, signal })));
  let stderr = '';
  hub.stderr.on('data', chunk => { stderr += chunk; });
  stopHub = async () => {
    if (hub.exitCode === null && hub.signalCode === null) hub.kill('SIGTERM');
    try {
      const result = await deadline(exited, 15_000, 'hub wrapper failed to forward SIGTERM');
      assert.equal(result.code, 0, 'hub shutdown exits cleanly through the npm wrapper');
    } finally {
      try { process.kill(-hub.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
    }
  };
  const base = await deadline(new Promise((resolve, reject) => {
    let output = '';
    hub.once('error', reject);
    hub.once('exit', () => reject(new Error(`hub stopped before listening: ${stderr}`)));
    hub.stdout.on('data', chunk => {
      output += chunk;
      const match = output.match(/listening on (http:\/\/127\.0\.0\.1:\d+)/);
      if (match) resolve(match[1]);
    });
  }), 10_000, 'hub did not start');
  const health = await fetch(`${base}/healthz`, { signal: AbortSignal.timeout(5000) });
  assert.equal(health.status, 200);
  await health.text();
  const ui = await fetch(`${base}/ui/`, { signal: AbortSignal.timeout(5000) });
  assert.equal(ui.status, 200);
  assert.match(await ui.text(), /<html/);

  const operatorConfig = path.join(temporary, 'operator.toml');
  await writeFile(operatorConfig, `[profiles.op]\nhub = ${JSON.stringify(base)}\nsecret = ${JSON.stringify(operatorSecret)}\n`, { mode: 0o600 });
  const users = [];
  for (const name of ['sender', 'receiver']) {
    const added = JSON.parse(mustRun(cli, ['--net', 'op', '--json', 'peer', 'add', '--kind', 'session', '--name', name], {
      env: { ...env, VECNET_CONFIG: operatorConfig },
    }));
    const userEnv = { ...env, VECNET_CONFIG: path.join(temporary, `${name}.toml`) };
    mustRun(cli, ['enrol', '--hub', base, '--token', '-', '--profile', name], {
      env: userEnv, input: `${added.enrol_token}\n`,
    });
    users.push({ name, id: added.peer_id, env: userEnv });
  }
  const [sender, receiver] = users;
  const sent = JSON.parse(mustRun(cli, ['--net', sender.name, '--json', 'send', receiver.id, 'hello from npm', '--hold'], { env: sender.env }));
  const inbox = JSON.parse(mustRun(cli, ['--net', receiver.name, '--json', 'inbox'], { env: receiver.env }));
  assert.ok(inbox.events.some(event => event.data?.message?.message_id === sent.message_id && event.data.message.content.text === 'hello from npm'));

  // Bootstrap using only the actually npm-installed CLI and its assets, outside
  // the source checkout. Only systemctl is faked: Node must load real modules.
  const added = JSON.parse(mustRun(cli, ['--net','op','--json','peer','add','--kind','launcher','--name','packed-launcher'], {env:{...env,VECNET_CONFIG:operatorConfig}}));
  const launcherEnv = {...env,VECNET_CONFIG:path.join(temporary,'launcher.toml')};
  const managerDir = path.join(temporary,'fake-manager'), managerLog = path.join(temporary,'manager.log');
  await mkdir(managerDir,{mode:0o700});
  await writeFile(path.join(managerDir,'systemctl'),'#!/bin/sh\nprintf \'%s\\n\' "$*" >> "$LAUNCHER_MANAGER_LOG"\n',{mode:0o700});
  launcherEnv.PATH = managerDir + path.delimiter + path.dirname(process.execPath) + path.delimiter + env.PATH;
  launcherEnv.LAUNCHER_MANAGER_LOG = managerLog;
  const repo = path.join(temporary,'launcher-repo');
  execFileSync('git',['init','-q',repo],{cwd:temporary,env:launcherEnv});
  execFileSync('git',['-C',repo,'-c','user.name=Package test','-c','user.email=package@example.invalid','commit','--allow-empty','-qm','initial'],{cwd:temporary,env:launcherEnv});
  mustRun(cli,['enrol','--hub',base,'--token','-','--profile','packed-launcher'],{env:launcherEnv,input:added.enrol_token+'\n'});
  mustRun(cli,['--net','packed-launcher','launcher','setup','--project','packed','--repo',repo,'--base-ref','HEAD','--write'],{env:launcherEnv});
  const installArgs = ['--net','packed-launcher','launcher','install','--node',process.execPath];
  const plan = JSON.parse(mustRun(cli,installArgs,{env:launcherEnv}));
  const installedRoot = path.join(prefix,'lib','node_modules','vecnet');
  assert.equal(plan.runtime.source,installedRoot,'installed executable discovers its own package outside the checkout');
  await assert.rejects(readFile(managerLog),{code:'ENOENT'},'preview must not touch the manager');
  const installed = JSON.parse(mustRun(cli,[...installArgs,'--write','--enable','--start'],{env:launcherEnv}));
  assert.ok(installed.runtime.entry.startsWith(launcherEnv.XDG_DATA_HOME+path.sep));
  assert.match(await readFile(managerLog,'utf8'),/--user daemon-reload\n--user enable vecnet-launcher-.*\.service\n--user restart vecnet-launcher-.*\.service\n/);
  const unit = await readFile(installed.unit_path,'utf8');
  assert.ok(unit.includes(installed.runtime.entry),'service executes the private installed snapshot');
  assert.ok(!unit.includes(root),'service must not reference the source checkout');
  const builtins = JSON.parse(await readFile(path.join(installedRoot,'BUILD_INFO.json'),'utf8')).launcher.backends;
  for (const id of ['t3','paseo','codex-app-server']) assert.ok(builtins.includes(id));
  const graph = execFileSync(process.execPath,['--disable-warning=ExperimentalWarning','--input-type=module','-e',
    'import {pathToFileURL} from "node:url"; import path from "node:path"; const ids=JSON.parse(process.argv[2]); for (const id of ids) { const m=await import(pathToFileURL(path.join(process.argv[1],"connectors/launcher/src/backends",id,"index.ts"))); const plugin=m.default ?? Object.values(m).find(p=>p?.id===id); if(plugin?.id!==id) throw new Error("wrong backend"); } console.log(JSON.stringify(ids));',
    installed.runtime.snapshot,JSON.stringify(builtins)],{cwd:temporary,env:launcherEnv,encoding:'utf8',timeout:10000});
  assert.deepEqual(JSON.parse(graph),builtins);
  const checked = run(cli,['--net','packed-launcher','launcher','check'],{env:launcherEnv});
  assert.equal(checked.status,1,'missing private Codex socket is an unhealthy backend, not a module/config failure');
  const report = JSON.parse(checked.stdout);
  assert.equal(report.backends.length,1);
  assert.equal(report.backends[0].id,'codex-app-server');
  assert.equal(report.backends[0].ok,false);
  assert.deepEqual(report.backends[0].presets,['default']);
  assert.deepEqual(report.backends[0].permission_modes,['read-only','ask','auto','full-access']);
  const managerBefore = await readFile(managerLog,'utf8'), unitBefore = await readFile(installed.unit_path,'utf8');
  const changedModule = path.join(installedRoot,'connectors/launcher/src/backends/codex-app-server/index.ts');
  await writeFile(changedModule,(await readFile(changedModule,'utf8'))+'\n// changed after packaging\n');
  const refused = run(cli,[...installArgs,'--write','--enable','--start'],{env:launcherEnv});
  assert.notEqual(refused.status,0,'changed packaged source must not become an installed runtime');
  assert.match(refused.stderr,/packaged launcher asset digest mismatch/);
  assert.equal(await readFile(managerLog,'utf8'),managerBefore,'digest refusal must not invoke the manager');
  assert.equal(await readFile(installed.unit_path,'utf8'),unitBefore,'digest refusal must not publish a unit');
});

test('wrapper preserves failure when a native process is killed by SIGPIPE', async t => {
  const fixture = await mkdtemp(path.join(os.tmpdir(), 'vecnet-npm-signal-'));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  execFileSync('tar', ['-xzf', tarball, '-C', fixture]);
  const binary = path.join(fixture, 'package', 'native', `linux-${process.arch}`, 'vecnet');
  await writeFile(binary, '#!/bin/sh\nkill -PIPE "$$"\n', { mode: 0o755 });
  const result = spawnSync(process.execPath, [path.join(fixture, 'package', 'bin', 'vecnet.cjs')], {
    encoding: 'utf8', timeout: 5000,
  });
  assert.ok(result.signal === 'SIGPIPE' || result.status === 141, `unexpected wrapper exit ${result.status}/${result.signal}: ${result.stderr}`);
});


test('npm package includes automatic delivery assets', async t => {
  const extracted = await mkdtemp(path.join(os.tmpdir(), 'vecnet-npm-assets-'));
  t.after(() => rm(extracted, {recursive:true,force:true}));
  execFileSync('tar', ['-xzf',tarball,'-C',extracted]);
  const contents = path.join(extracted,'package');
  const entry = 'connectors/launcher/src/codex/delivery-main.ts';
  assert.ok((await stat(path.join(contents,entry))).isFile(), 'packed release needs the delivery entry');
  for (const project of ['launcher','hub-client']) assert.equal(JSON.parse(await readFile(path.join(contents,'connectors',project,'package.json'),'utf8')).type,'module');
  const metadata = JSON.parse(await readFile(path.join(contents,'BUILD_INFO.json'),'utf8'));
  assert.equal(metadata.delivery.entry,entry);
  for (const [file,digest] of Object.entries(metadata.delivery.assets)) {
    const bytes = await readFile(path.join(contents,file));
    assert.equal(createHash('sha256').update(bytes).digest('hex'),digest,file);
    if (file.endsWith('.ts')) assert.deepEqual(bytes,await readFile(path.join(root,file)));
  }
  assert.equal(JSON.parse(await readFile(path.join(contents,'package.json'),'utf8')).engines.node,'>=20');
});

test('npm package includes complete launcher runtime and dynamic builtins', async t => {
  const extracted = await mkdtemp(path.join(os.tmpdir(),'vecnet-npm-launcher-assets-'));
  t.after(()=>rm(extracted,{recursive:true,force:true}));
  execFileSync('tar',['-xzf',tarball,'-C',extracted]);
  const contents = path.join(extracted,'package');
  const metadata = JSON.parse(await readFile(path.join(contents,'BUILD_INFO.json'),'utf8'));
  assert.equal(metadata.launcher?.entry,'connectors/launcher/src/main.ts');
  for (const file of ['src/main.ts','src/daemon.ts','src/plugins.ts','src/backends/t3/index.ts','src/backends/paseo/index.ts','src/backends/codex-app-server/index.ts','systemd/vecnet-launcher-profile.service.in']) {
    const key='connectors/launcher/'+file;
    assert.ok(metadata.launcher.assets[key],'missing launcher asset '+key);
    assert.deepEqual(await readFile(path.join(contents,key)),await readFile(path.join(root,key)),'exact shipped source '+key);
  }
  for (const [file,digest] of Object.entries(metadata.launcher.assets)) {
    assert.equal(createHash('sha256').update(await readFile(path.join(contents,file))).digest('hex'),digest,file);
  }
  const packed = execFileSync('tar',['-tzf',tarball],{encoding:'utf8'}).trim().split('\n');
  const runtime = packed.filter(file=>file.startsWith('package/connectors/')).map(file=>file.slice('package/'.length)).sort();
  assert.deepEqual(runtime,[...new Set([...Object.keys(metadata.delivery.assets),...Object.keys(metadata.launcher.assets)])].sort(),'every runtime artifact has recorded provenance');
});

test('extracted npm delivery entry executes', {timeout:15000}, async t => {
  const extracted = await mkdtemp(path.join(os.tmpdir(),'vecnet-npm-delivery-'));
  let child, exited;
  t.after(async () => {
    try {
      if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
      if (exited) await deadline(exited,5000,'delivery did not drain');
    } finally { await rm(extracted,{recursive:true,force:true}); }
  });
  execFileSync('tar',['-xzf',tarball,'-C',extracted]);
  const contents = path.join(extracted,'package');
  const binary = path.join(contents,'native',`linux-${process.arch}`,'vecnet');
  const env = {PATH:path.dirname(process.execPath)};
  for (const [key,sub] of Object.entries({HOME:'home',XDG_CONFIG_HOME:'config',CODEX_HOME:'codex',XDG_RUNTIME_DIR:'runtime',XDG_DATA_HOME:'data'})) {
    env[key]=path.join(extracted,sub); await mkdir(env[key],{mode:0o700});
  }
  const hub='https://hub.invalid';
  const slug=createHash('sha256').update(hub).digest('hex');
  const stateRoot=path.join(env.XDG_DATA_HOME,'vecnet/codex-delivery');
  const state=path.join(stateRoot,slug);
  await mkdir(state,{recursive:true,mode:0o700});
  await writeFile(path.join(state,'descriptor.json'),JSON.stringify({version:1,hub}),{mode:0o600});
  await writeFile(path.join(stateRoot,'registry.json'),JSON.stringify({version:1,sources:[{config:path.join(env.XDG_CONFIG_HOME,'fixture.toml'),bindings:path.join(env.XDG_CONFIG_HOME,'absent-bindings.json')}]}),{mode:0o600});
  child=spawn(binary,['codex','deliver','--hub',hub],{env,stdio:['ignore','pipe','pipe']});
  exited=new Promise(resolve=>child.once('exit',(code,signal)=>resolve({code,signal})));
  let stderr=''; child.stderr.on('data',chunk=>{stderr+=chunk;});
  await deadline((async()=>{
    const readyBy=Date.now()+4500;
    while (Date.now()<readyBy) {
      if (stderr.includes('codex_connector_disabled')) return;
      assert.ok(child.exitCode===null && child.signalCode===null,`extracted delivery stopped: ${stderr}`);
      await new Promise(resolve=>setTimeout(resolve,20));
    }
    assert.fail('extracted delivery did not reconcile its fixture source: '+stderr);
  })(),5000,'extracted delivery did not create its ledger');
  assert.deepEqual(JSON.parse(await readFile(path.join(state,'hub.json'),'utf8')),{version:1,hub});
  const status=spawnSync(binary,['codex','delivery-status','--hub',hub],{env,encoding:'utf8',timeout:2000});
  assert.equal(status.status,0,status.stderr);
  assert.deepEqual(JSON.parse(status.stdout),{hub,running:true});
  const owner=JSON.parse(await readFile(path.join(env.XDG_RUNTIME_DIR,'vecnet',`delivery-${slug}.json`),'utf8'));
  assert.equal(owner.launcher,path.join(contents,'connectors/launcher/src/codex/delivery-main.ts'));
  child.kill('SIGTERM'); assert.equal((await deadline(exited,5000,'delivery did not stop')).code,0,stderr);
});
