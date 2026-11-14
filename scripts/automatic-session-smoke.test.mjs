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
