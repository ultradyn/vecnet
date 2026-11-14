import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { privateSmokeEnvironment } from './launcher-disposable-smoke.mjs';

test('disposable controller defaults and import never dispatch a model or service', () => {
  const file = new URL('./launcher-disposable-smoke.mjs', import.meta.url);
  const output = execFileSync(process.execPath, [file.pathname], { encoding: 'utf8' });
  assert.match(output, /^SKIP: --run/);
  assert.equal(execFileSync(process.execPath, ['--input-type=module', '-e', `await import(${JSON.stringify(file.href)})`, '--', '--run'], { encoding: 'utf8' }), '');
});

test('private child environment removes host routing without changing parent HOME or exposing provider values', () => {
  const parent = { HOME: '/real/home', PATH: '/usr/bin:/bin', CODEX_HOME: '/real/codex', CODEX_THREAD_ID: 'old', VECNET_CONFIG: '/real/config',
    XDG_DATA_HOME: '/real/data', GIT_CONFIG_GLOBAL: '/real/git', BASH_ENV: '/real/rc', ENV: '/real/env', NODE_OPTIONS: '--require=/real/hook', LD_PRELOAD: '/real/preload',
    ANTHROPIC_API_KEY: 'synthetic-provider-only', PLAYWRIGHT_MODULE: '/cache/playwright/index.mjs', PLAYWRIGHT_BROWSERS_PATH: '/cache/browsers' };
  const before = structuredClone(parent), root = '/var/tmp/disposable-fixture';
  const env = privateSmokeEnvironment(parent, root);
  assert.deepEqual(parent, before);
  assert.equal(env.HOME, root + '/home');
  assert.equal(env.CODEX_HOME, root + '/home/.codex');
  assert.equal(env.XDG_DATA_HOME, root + '/home/.local/share');
  assert.ok(env.PATH.startsWith(root + '/bin:'));
  assert.equal(env.ANTHROPIC_API_KEY, parent.ANTHROPIC_API_KEY);
  for (const key of ['CODEX_THREAD_ID', 'VECNET_CONFIG', 'GIT_CONFIG_GLOBAL', 'BASH_ENV', 'ENV', 'NODE_OPTIONS', 'LD_PRELOAD']) assert.equal(env[key], undefined);
  assert.equal(env.PLAYWRIGHT_MODULE, '/cache/playwright/index.mjs');
});


test('ACP inherited log routing is rebound to the private run root', () => {
 const root='/var/tmp/disposable-log-fixture';
 const env=privateSmokeEnvironment({HOME:'/real/home',CLAUDE_AGENT_LOGS:'/real/home/logs',CLAUDE_CONFIG_DIR:'/real/home/claude'},root);
 assert.equal(env.CLAUDE_AGENT_LOGS,root+'/logs/acp');
 assert.equal(env.CLAUDE_CONFIG_DIR,undefined);
});

for (const failure of ['identity', 'journal']) test(`post-spawn ${failure} failure exits promptly and retains child, starting intent and root`, async t => {
 const {mkdtemp,mkdir,writeFile,readFile,rm}=await import('node:fs/promises');
 const {tmpdir}=await import('node:os');
 const {join}=await import('node:path');
 const {promisify}=await import('node:util');
 const {execFile}=await import('node:child_process');
 const root=await mkdtemp(join(tmpdir(),'disposable-child-failure-'));
 await mkdir(join(root,'logs'),{mode:0o700});
 await mkdir(join(root,'home'),{mode:0o700});
 const file=new URL('./launcher-disposable-smoke.mjs',import.meta.url).href;
 const support=new URL('./native-launcher-smoke-support.mjs',import.meta.url).href;
 const timer=join(root,'timer.mjs');
 await writeFile(timer,'setInterval(()=>{},1000);',{mode:0o600});
 const controller=join(root,'controller.mjs');
 await writeFile(controller,`
 import {ownedChild} from ${JSON.stringify(file)};
 import {createSmokeJournal} from ${JSON.stringify(support)};
 import {readFile,writeFile} from 'node:fs/promises';
 const root=${JSON.stringify(root)}, journal=createSmokeJournal(root,{test:true});
 const wrapped={update(value){if(${JSON.stringify(failure)}==='journal'&&value.testchild?.phase==='running')throw Error('injected journal failure');journal.update(value);}};
 try { await ownedChild(process.execPath,[${JSON.stringify(timer)}],{HOME:root+'/home'},root,'testchild',wrapped,{identify:async(pid,argv,home)=>{
  const raw=await readFile('/proc/'+pid+'/stat','utf8');const start=raw.slice(raw.lastIndexOf(')')+2).split(' ')[19];
  await writeFile(root+'/owner.json',JSON.stringify({pid,start,argv,home}),{mode:0o600});
  if(${JSON.stringify(failure)}==='identity')throw Error('injected identity failure');
  return {pid,start,argv,home};
 }});process.exitCode=2;}catch{console.log('EXPECTED_RETAINED_FAILURE');}
 `,{mode:0o600});
 let owner;
 t.after(async()=>{
  // Test-only cleanup after retention proof; the tested controller sends no signals.
  try {
   owner ||= JSON.parse(await readFile(join(root,'owner.json'),'utf8'));
   const raw=await readFile('/proc/'+owner.pid+'/stat','utf8');
   const start=raw.slice(raw.lastIndexOf(')')+2).split(' ')[19];
   const cmd=(await readFile('/proc/'+owner.pid+'/cmdline','utf8')).split('\0').filter(Boolean);
   if(start===owner.start&&JSON.stringify(cmd)===JSON.stringify(owner.argv))process.kill(owner.pid,'SIGTERM');
  }catch(e){if(e.code!=='ENOENT'&&e.code!=='ESRCH')throw e;}
  await rm(root,{recursive:true,force:true});
 });
 const result=await promisify(execFile)(process.execPath,[controller],{timeout:2500});
 assert.equal(result.stdout.trim(),'EXPECTED_RETAINED_FAILURE');
 owner=JSON.parse(await readFile(join(root,'owner.json'),'utf8'));
 const raw=await readFile('/proc/'+owner.pid+'/stat','utf8');
 assert.equal(raw.slice(raw.lastIndexOf(')')+2).split(' ')[19],owner.start);
 assert.deepEqual(JSON.parse(await readFile(join(root,'recovery.json'),'utf8')).testchild,{phase:'starting',argv:owner.argv,home:owner.home});
 assert.equal(await readFile(timer,'utf8'),'setInterval(()=>{},1000);');
});


test('native listener readiness accepts Codex owned private symlink publication and refuses unsafe parents', async t => {
 const {nativeListenerReady}=await import('./launcher-disposable-smoke.mjs');
 const {mkdtemp,mkdir,chmod,symlink,rm}=await import('node:fs/promises');
 const {createServer}=await import('node:net');
 const {tmpdir}=await import('node:os');const {join}=await import('node:path');
 const root=await mkdtemp(join(tmpdir(),'native-ready-'));
 await mkdir(join(root,'socket'),{mode:0o700});
 const target=join(root,'socket/actual.sock'), link=join(root,'requested.sock');
 const server=createServer();
 t.after(async()=>{await new Promise(r=>server.close(r));await rm(root,{recursive:true,force:true});});
 await new Promise((yes,no)=>{server.once('error',no);server.listen(target,yes);});
 await symlink(target,link);
 assert.equal(await nativeListenerReady(link),true);
 await chmod(join(root,'socket'),0o755);
 await assert.rejects(nativeListenerReady(link),/private.*socket/);
});
