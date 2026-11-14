import assert from 'node:assert/strict';
import test from 'node:test';
import { copyFile, mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { parseManagedUnit, fixtureSystemctl, stopFixtureDaemonHelpers } from './automatic-session-smoke.mjs';

const unit = (home, entry) => `# vecnet managed standalone delivery v1
[Service]
Environment=${JSON.stringify('HOME=' + home)}
Environment=${JSON.stringify('VECNET_BIN=' + join(home, '.local/share/vecnet-codex/release/vecnet'))}
Environment="PATH=/usr/bin:/bin"
Environment="VECNET_LOCAL_MAINTENANCE=0"
ExecStart=${JSON.stringify(process.execPath)} --disable-warning=ExperimentalWarning --experimental-strip-types ${JSON.stringify(entry)}
`;

test('unit parser decodes literal systemd escaping without shell expansion', () => {
 const home='/tmp/private home';
 const entry=join(home,'.local/share/vecnet-codex/release/connectors/launcher/src/codex/delivery-main.ts');
 const parsed=parseManagedUnit(unit(home,entry),home);
 assert.equal(parsed.executable,process.execPath);
 assert.deepEqual(parsed.args,['--disable-warning=ExperimentalWarning','--experimental-strip-types',entry]);
 assert.equal(parsed.env.HOME,home);
 assert.equal(parsed.env.VECNET_LOCAL_MAINTENANCE,'0');
 assert.equal(parseManagedUnit(unit(home,entry).replace('PATH=/usr/bin:/bin',()=> 'PATH=/tmp/%%cash$$'),home).env.PATH,'/tmp/%cash$$');
});

test('unit adapter refuses foreign HOME, executable, runtime path and extra directives', () => {
 const home='/tmp/private';const entry=join(home,'.local/share/vecnet-codex/release/connectors/launcher/src/codex/delivery-main.ts');
 for (const bad of [unit(home,entry).replace('HOME=/tmp/private','HOME=/real/home'),unit(home,entry).replace(JSON.stringify(process.execPath),'"/bin/sh"'),unit(home,'/tmp/foreign/delivery-main.ts'),unit(home,entry)+'ExecStartPost=/bin/true\n']) {
  assert.throws(()=>parseManagedUnit(bad,home));
 }
});

test('fixture adapter starts the actual private ExecStart, reports it and stops it', async () => {
 const root=await mkdtemp(join(tmpdir(),'automatic-adapter-test-'));
 const home=join(root,'home');const release=join(home,'.local/share/vecnet-codex/release');
 const entry=join(release,'connectors/launcher/src/codex/delivery-main.ts');
 await mkdir(join(home,'.config/systemd/user'),{recursive:true,mode:0o700});
 await mkdir(join(release,'connectors/launcher/src/codex'),{recursive:true,mode:0o700});
 await writeFile(join(root,'.automatic-session-fixture'),'fixture-v1',{mode:0o600});
 await writeFile(entry,"process.stdout.write('PRIVATE_RUNTIME '+process.env.HOME+'\\n');setInterval(()=>{},1000);",{mode:0o600});
 await writeFile(join(release,'vecnet'),'fixture',{mode:0o700});
 await writeFile(join(home,'.config/systemd/user/vecnet-codex-delivery.service'),unit(home,entry),{mode:0o600});
 const adapter={root,home};
 try {
  assert.equal(await fixtureSystemctl(adapter,['--user','show-environment']),'');
  assert.equal(await fixtureSystemctl(adapter,['--user','list-units','--all','--','vecnet-launcher.service']),'');
  await assert.rejects(fixtureSystemctl(adapter,['--user','restart','--','foreign.service']));
  await fixtureSystemctl(adapter,['--user','restart','--','vecnet-codex-delivery.service']);
  assert.match(await fixtureSystemctl(adapter,['--user','show','--','vecnet-codex-delivery.service']),/ActiveState=active/);
  for(let i=0;i<100;i++){if((await readFile(join(root,'runtime.log'),'utf8')).includes('PRIVATE_RUNTIME'))break;await new Promise(r=>setTimeout(r,20));}
  assert.match(await readFile(join(root,'runtime.log'),'utf8'),new RegExp('PRIVATE_RUNTIME '+home));
  await fixtureSystemctl(adapter,['--user','stop','--','vecnet-codex-delivery.service']);
  assert.match(await fixtureSystemctl(adapter,['--user','show','--','vecnet-codex-delivery.service']),/ActiveState=inactive/);
 } finally {await fixtureSystemctl(adapter,['--user','stop','--','vecnet-codex-delivery.service']).catch(()=>{});await rm(root,{recursive:true,force:true});}
});

test('default invocation and import with caller --run never enter live setup', () => {
 const path=new URL('./automatic-session-smoke.mjs',import.meta.url);
 const r=spawnSync(process.execPath,[path.pathname],{encoding:'utf8',env:{PATH:process.env.PATH}});
 assert.equal(r.status,0);assert.match(r.stdout,/SKIP/);
 const imp=spawnSync(process.execPath,['--input-type=module','-e',`process.argv.push('--run'); await import(${JSON.stringify(path.href)}); console.log('SAFE_IMPORT')`],{encoding:'utf8',env:{PATH:process.env.PATH}});
 assert.equal(imp.status,0);assert.equal(imp.stdout.trim(),'SAFE_IMPORT');
});

test('interruption gate marks the real capability boundary and releases without a service start', async () => {
 const root=await mkdtemp(join(tmpdir(),'automatic-gate-test-'));const home=join(root,'home');
 await mkdir(home,{mode:0o700});await writeFile(join(root,'.automatic-session-fixture'),'fixture-v1',{mode:0o600});
 await writeFile(join(root,'interrupt-next-probe'),'gate',{mode:0o600});
 try {
  const probing=fixtureSystemctl({root,home},['--user','show-environment']);
  for(let i=0;i<100;i++){try{await readFile(join(root,'probe-blocked'));break;}catch{await new Promise(r=>setTimeout(r,10));}}
  assert.equal(await readFile(join(root,'probe-blocked'),'utf8'),'blocked');
  await rm(join(root,'interrupt-next-probe'));assert.equal(await probing,'');
  assert.match(await fixtureSystemctl({root,home},['--user','show','--','vecnet-codex-delivery.service']),/ActiveState=inactive/);
 }finally{await rm(join(root,'interrupt-next-probe'),{force:true});await rm(root,{recursive:true,force:true});}
});

test('fixture operations refuse a different HOME before touching processes', async () => {
 const root=await mkdtemp(join(tmpdir(),'automatic-home-test-'));
 try {await writeFile(join(root,'.automatic-session-fixture'),'fixture-v1',{mode:0o600});await assert.rejects(fixtureSystemctl({root,home:tmpdir()},['--user','stop','--','vecnet-codex-delivery.service']),/not a private fixture/);}
 finally {await rm(root,{recursive:true,force:true});}
});

test('daemon helper cleanup stops only the exact private updater identity', async () => {
 const root=await mkdtemp(join(tmpdir(),'automatic-helper-test-')), home=join(root,'home');
 const executable=join(home,'.codex/packages/app-server-daemon/releases/fixture/bin/codex');
 await mkdir(join(executable,'..'),{recursive:true,mode:0o700});
 await copyFile(process.execPath,executable);
 await writeFile(join(root,'.automatic-session-fixture'),'fixture-v1',{mode:0o600});
 await writeFile(join(root,'app-server'),'setInterval(()=>{},1000);',{mode:0o600});
 const env={...process.env,HOME:home,CODEX_HOME:join(home,'.codex')};
 const child=spawn(executable,['app-server','daemon','pid-update-loop'],{cwd:root,env,stdio:'ignore'});
 const foreign=spawn(executable,['app-server','daemon','pid-update-loop'],{cwd:root,env:{...env,HOME:join(root,'foreign')},stdio:'ignore'});
 const exited=new Promise(resolve=>child.once('exit',resolve));
 try {
  await Promise.all([child,foreign].map(c=>new Promise((yes,no)=>{c.once('spawn',yes);c.once('error',no);} )));
  assert.equal(await stopFixtureDaemonHelpers({root,home}),1);
  await exited;assert.equal(child.signalCode,'SIGTERM');
  assert.equal(foreign.exitCode,null);assert.equal(foreign.signalCode,null);
  assert.equal(await stopFixtureDaemonHelpers({root,home}),0);
  await assert.rejects(stopFixtureDaemonHelpers({root,home:join(root,'foreign')}),/not a private fixture/);
 } finally {
  for(const c of [child,foreign]){if(c.exitCode===null&&c.signalCode===null)c.kill('SIGKILL');}
  await Promise.all([child,foreign].map(c=>c.exitCode!==null||c.signalCode!==null?null:new Promise(r=>c.once('exit',r))));
  await rm(root,{recursive:true,force:true});
 }
});

// Profile launcher acceptance reuses the private manager, never real systemd.
const profileUnitName = 'vecnet-launcher-p-0123456789abcdef.service';
const profileUnit = (root, home, entry, config) => `# Managed by vecnet launcher install. Rendered with absolute runtime paths.
[Service]
Type=simple
Environment=${JSON.stringify('HOME=' + home)}
Environment=${JSON.stringify('PATH=' + join(root, 'bin') + ':/usr/bin:/bin')}
Environment=${JSON.stringify('VECNET_BIN=' + join(root, 'bin/vecnet'))}
Environment=${JSON.stringify('VECNET_CONFIG=' + join(root, 'config.toml'))}
ExecStart=${JSON.stringify(process.execPath)} --disable-warning=ExperimentalWarning ${JSON.stringify(entry)} run --config ${JSON.stringify(config)}
Restart=on-failure
RestartSec=10
RestartPreventExitStatus=3
UMask=0077
MemoryMax=1G
`;

async function profileFixture(t) {
 const root = await mkdtemp(join(tmpdir(), 'automatic-launcher-adapter-')), home = join(root, 'home');
 const entry = join(home, '.local/share/vecnet-launcher/snapshots', 'a'.repeat(64), 'connectors/launcher/src/main.ts');
 const config = join(root, 'launcher/p-0123456789abcdef/config.json');
 const path = join(home, '.config/systemd/user', profileUnitName);
 for (const file of [entry, config, path, join(root, 'bin/vecnet')]) await mkdir(join(file, '..'), {recursive:true, mode:0o700});
 await writeFile(join(root, '.automatic-session-fixture'), 'fixture-v1', {mode:0o600});
 await writeFile(entry, 'setInterval(()=>{},1000);', {mode:0o600});
 await writeFile(config, '{}', {mode:0o600});
 await writeFile(join(root, 'config.toml'), '', {mode:0o600});
 await writeFile(join(root, 'bin/vecnet'), 'fixture', {mode:0o700});
 await writeFile(path, profileUnit(root, home, entry, config), {mode:0o600});
 let manager;
 t.after(async () => { if (manager) await manager(['--user','stop',profileUnitName]).catch(()=>{}); await rm(root,{recursive:true,force:true}); });
 const module = await import('./automatic-session-smoke.mjs');
 assert.equal(typeof module.createFixtureSystemctl, 'function', 'private launcher adapter factory is required');
 manager = module.createFixtureSystemctl({root, home, kind:'launcher'});
 return {root,home,entry,config,path,manager,module};
}

test('profile adapter parses the generated launcher command and preserves standalone parser refusal', async t => {
 const f=await profileFixture(t);
 const parsed=f.module.parseLauncherUnit(await readFile(f.path,'utf8'),{root:f.root,home:f.home});
 assert.equal(parsed.executable,process.execPath);
 assert.deepEqual(parsed.args,['--disable-warning=ExperimentalWarning',f.entry,'run','--config',f.config]);
 assert.equal(parsed.env.VECNET_CONFIG,join(f.root,'config.toml'));
 assert.throws(()=>parseManagedUnit(profileUnit(f.root,f.home,f.entry,f.config),f.home),/managed/);
});

test('profile adapter rejects foreign HOME, config, binary, entry and executable directives', async t => {
 const f=await profileFixture(t), text=await readFile(f.path,'utf8');
 for (const altered of [text.replace(f.home,'/foreign'),text.replace(f.config,'/foreign/config.json'),
  text.replace(join(f.root,'bin/vecnet'),'/usr/bin/vecnet'),text.replace(f.entry,'/foreign/main.ts'),
  text+'\nExecStartPost=/bin/true\n',text+'\nEnvironmentFile=/foreign\n',text+'\nWorkingDirectory=/foreign\n']) {
  assert.throws(()=>f.module.parseLauncherUnit(altered,{root:f.root,home:f.home}),/fixture|unexpected|unsupported|private/);
 }
 await assert.rejects(f.manager(['--user','restart','vecnet-launcher-p-ffffffffffffffff.service']),/missing|ENOENT/);
 await assert.rejects(f.manager(['--user','restart','foreign.service']),/foreign/);
});

test('profile adapter executes and restarts the generated command, then proves exact PID teardown', async t => {
 const f=await profileFixture(t);
 await f.manager(['--user','restart',profileUnitName]);
 const first=JSON.parse(await readFile(join(f.root,profileUnitName+'.pid.json'),'utf8'));
 assert.equal(first.phase,'running'); assert.ok(first.pid>1); assert.ok(first.stamp);
 assert.equal(await f.manager(['--user','is-active',profileUnitName]),'active\n');
 assert.match(await f.manager(['--user','show',profileUnitName,'-p','ActiveState','-p','MainPID']),new RegExp('MainPID='+first.pid));
 await f.manager(['--user','restart',profileUnitName]);
 const second=JSON.parse(await readFile(join(f.root,profileUnitName+'.pid.json'),'utf8'));
 assert.notEqual(second.pid,first.pid);
 await f.manager(['--user','stop',profileUnitName]);
 assert.match(await f.manager(['--user','show',profileUnitName,'-p','ActiveState','-p','MainPID']),/ActiveState=inactive\nMainPID=0/);
});

test('profile adapter unresolved spawn intent cannot imply nonexecution or authorize a replacement', async t => {
 const f=await profileFixture(t), path=join(f.root,profileUnitName+'.pid.json');
 const intent={phase:'starting',unit:profileUnitName};
 await writeFile(path,JSON.stringify(intent),{mode:0o600});
 for(const op of ['restart','stop','show']) await assert.rejects(f.manager(['--user',op,profileUnitName]),/unresolved|unproved/);
 assert.deepEqual(JSON.parse(await readFile(path,'utf8')),intent);
});

test('profile adapter PID metadata cannot authorize signalling a foreign process or symlinked unit', async t => {
 const f=await profileFixture(t), path=join(f.root,profileUnitName+'.pid.json');
 const forged={phase:'running',unit:profileUnitName,pid:process.pid,stamp:'wrong',argv:[process.execPath]};
 await writeFile(path,JSON.stringify(forged),{mode:0o600});
 await assert.rejects(f.manager(['--user','stop',profileUnitName]),/identity|foreign/);
 assert.deepEqual(JSON.parse(await readFile(path,'utf8')),forged);
 await rm(path);
 await rm(f.path);
 const {symlink}=await import('node:fs/promises'); await symlink(f.config,f.path);
 await assert.rejects(f.manager(['--user','restart',profileUnitName]),/symlink|regular|private/);
});


test('profile adapter missing projection and interrupted operation lock cannot authorize replacement', async t => {
 const f=await profileFixture(t);
 await writeFile(join(f.root,profileUnitName+'.started'),'',{mode:0o600});
 for(const op of ['restart','stop','show']) await assert.rejects(f.manager(['--user',op,profileUnitName]),/unresolved.*projection/);
 await rm(join(f.root,profileUnitName+'.started'));
 await mkdir(join(f.root,profileUnitName+'.lock'),{mode:0o700});
 await assert.rejects(f.manager(['--user','restart',profileUnitName]),/unresolved.*lock/);
});

test('profile adapter refuses changed argv even for the same live PID and start stamp', async t => {
 const f=await profileFixture(t);
 await f.manager(['--user','restart',profileUnitName]);
 const path=join(f.root,profileUnitName+'.pid.json'), raw=await readFile(path,'utf8');
 try {
  const state=JSON.parse(raw); state.argv=[process.execPath,'/foreign/main.ts'];
  await writeFile(path,JSON.stringify(state),{mode:0o600});
  await assert.rejects(f.manager(['--user','stop',profileUnitName]),/foreign.*identity/);
 } finally { await writeFile(path,raw,{mode:0o600}); }
 await f.manager(['--user','stop',profileUnitName]);
});
