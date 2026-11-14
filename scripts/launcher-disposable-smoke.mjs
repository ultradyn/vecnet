// Controller for the existing live smoke; --run is mandatory. No compiler,
// real systemctl, daemon manager, user profile or inherited HOME mutation.
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, copyFile, lstat, mkdir, mkdtemp, open, readFile, readdir, realpath, symlink, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createSmokeJournal, runHost, smokeSnapshotDigest } from './native-launcher-smoke-support.mjs';
const check = (ok, why) => { if (!ok) throw new Error(why); };
const sleep = ms => new Promise(r => setTimeout(r, ms));
const hash = async path => createHash('sha256').update(await readFile(path)).digest('hex');
export function privateSmokeEnvironment(parent, root) {
  const env = Object.fromEntries(Object.entries(parent).filter(([k]) => !/^(CODEX_|VECNET_|XDG_|GIT_|BASH_ENV$|ENV$|NODE_OPTIONS$|LD_PRELOAD$|LD_LIBRARY_PATH$)/.test(k)));
  return Object.assign(env, { HOME: join(root, 'home'), CODEX_HOME: join(root, 'home/.codex'),
    XDG_CONFIG_HOME: join(root, 'home/.config'), XDG_DATA_HOME: join(root, 'home/.local/share'),
    XDG_STATE_HOME: join(root, 'home/.local/state'), XDG_CACHE_HOME: join(root, 'home/.cache'),
    XDG_RUNTIME_DIR: join(root, 'runtime'), TMPDIR: join(root, 'tmp'),
    PATH: join(root, 'bin') + ':' + (parent.PATH || '/usr/bin:/bin'), VECNET_LOCAL_MAINTENANCE: '0',
    PLAYWRIGHT_MODULE: parent.PLAYWRIGHT_MODULE || join(parent.HOME, '.llm-general/skills/headless-browser-screenshots/node_modules/playwright/index.mjs'),
    PLAYWRIGHT_BROWSERS_PATH: parent.PLAYWRIGHT_BROWSERS_PATH || join(parent.HOME, '.cache/ms-playwright') });
}
async function stamp(pid) {
  try { const raw = await readFile(`/proc/${pid}/stat`, 'utf8'), f = raw.slice(raw.lastIndexOf(')') + 2).split(' '); return f[0] === 'Z' ? null : f[19]; }
  catch (e) { if (['ENOENT', 'ESRCH'].includes(e.code)) return null; throw e; }
}
async function identity(pid, argv, home) {
  const start = await stamp(pid); check(start, 'child identity unavailable');
  const actual = (await readFile(`/proc/${pid}/cmdline`, 'utf8')).split('\0').filter(Boolean);
  const env = (await readFile(`/proc/${pid}/environ`, 'utf8')).split('\0');
  check(JSON.stringify(actual) === JSON.stringify(argv) && env.includes('HOME=' + home) &&
    (await lstat(`/proc/${pid}`)).uid === process.geteuid() && await realpath(`/proc/${pid}/exe`) === await realpath(argv[0]), 'child ownership mismatch');
  return { pid, start, argv, home };
}
async function ownedChild(command, args, env, root, name, journal) {
  journal.update({ [name]: { phase: 'starting', argv: [command, ...args], home: env.HOME } });
  const log = await open(join(root, 'logs', name + '.log'), 'wx', 0o600);
  try {
    const child = spawn(command, args, { cwd: root, env, detached: true, stdio: ['ignore', log.fd, log.fd] });
    // Register completion before waiting for spawn: an immediate exit cannot be lost.
    const done = new Promise((yes, no) => { child.once('error', no); child.once('close', (code, signal) => yes({ code, signal })); });
    done.catch(() => {}); // Spawn failure is also consumed by the explicit spawn gate.
    await new Promise((yes, no) => { child.once('spawn', yes); child.once('error', no); });
    const owner = await identity(child.pid, [command, ...args], env.HOME);
    journal.update({ [name]: { phase: 'running', ...owner } });
    return { child, done, owner };
  } finally { await log.close(); }
}
async function stopOwned(handle, journal, name) {
  const current = await identity(handle.owner.pid, handle.owner.argv, handle.owner.home);
  check(current.start === handle.owner.start, 'child PID reused; retained');
  process.kill(current.pid, 'SIGTERM');
  for (let i = 0; i < 200 && await stamp(current.pid) === current.start; i++) await sleep(25);
  check(await stamp(current.pid) !== current.start, 'child did not stop; retained');
  // No broad process-group signals: orphaned descendants retain ambiguity.
  for (const entry of await readdir('/proc')) {
    if (!/^\d+$/.test(entry)) continue;
    try { const raw = await readFile(`/proc/${entry}/stat`, 'utf8'), f = raw.slice(raw.lastIndexOf(')') + 2).split(' ');
      check(f[0] === 'Z' || Number(f[2]) !== current.pid, 'child descendants remain; retained');
    } catch (e) { if (!['ENOENT', 'ESRCH'].includes(e.code)) throw e; }
  }
  journal.update({ [name]: { phase: 'stopped', ...handle.owner } });
}
async function privateSeed(source, target) {
  const st = await lstat(source); check(st.isFile() && !st.isSymbolicLink() && st.uid === process.geteuid() && !(st.mode & 0o077), 'provider seed is not private');
  await copyFile(source, target); await chmod(target, 0o600);
}
export async function runDisposableScenario({ source, cli, demo, backend = 'codex-app-server', mode = 'worktree', nativeSeed, acpSettings, parent = process.env }) {
  check(['codex-app-server', 'acp'].includes(backend) && ['worktree', 'project-root', 'temp'].includes(mode), 'invalid scenario');
  process.umask(0o077);
  const root = await mkdtemp('/var/tmp/vecnet-launcher-accept-');
  const journal = createSmokeJournal(root, { kind: 'disposable-launcher-controller', backend, workspace_mode: mode,
    activation: 'private generated-unit argv adapter; not real systemd', complete: false });
  console.log('Private acceptance evidence:', root);
  try {
    source = await realpath(source);
    for (const dir of ['home', 'bin', 'runtime', 'tmp', 'logs', 'shots', 'home/.codex', 'home/.config', 'home/.cache', 'home/.local/share', 'home/.local/state']) await mkdir(join(root, dir), { recursive: true, mode: 0o700 });
    for (const [from, name] of [[cli, 'vecnet'], [demo, 'demo']]) { check((await lstat(from)).isFile(), 'candidate binary missing'); await copyFile(from, join(root, 'bin', name)); await chmod(join(root, 'bin', name), 0o700); }
    await writeFile(join(root, '.automatic-session-fixture'), 'fixture-v1', { mode: 0o600 });
    const adapter = pathToFileURL(join(source, 'scripts/automatic-session-smoke.mjs')).href;
    await writeFile(join(root, 'bin/systemctl'), `#!${process.execPath}\nimport {createFixtureSystemctl} from ${JSON.stringify(adapter)};\ntry{process.stdout.write(await createFixtureSystemctl(${JSON.stringify({root, home:join(root,'home'), kind:'launcher'})})(process.argv.slice(2)));}catch{process.stderr.write('private adapter refused operation\\n');process.exitCode=1;}\n`, { mode: 0o700, flag: 'wx' });
    const env = privateSmokeEnvironment(parent, root);
    await lstat(env.PLAYWRIGHT_MODULE); await lstat(env.PLAYWRIGHT_BROWSERS_PATH);
    const approved = { source_revision: (await runHost('git', ['-C', source, 'rev-parse', 'HEAD'])).trim(),
      snapshot_sha256: await smokeSnapshotDigest(source), cli_sha256: await hash(join(root, 'bin/vecnet')), demo_sha256: await hash(join(root, 'bin/demo')) };
    let listener;
    if (backend === 'codex-app-server') {
      await privateSeed(nativeSeed, join(env.CODEX_HOME, 'config.toml'));
      const executable = '/home/xertrov/.codex/packages/app-server-daemon/releases/0.166.0-x86_64-unknown-linux-musl/bin/codex';
      approved.native = { executable, executable_sha256: await hash(executable), version: '0.166.0' };
      listener = await ownedChild(executable, ['app-server', '--listen', 'unix://' + join(root, 'runtime/native.sock')], env, root, 'native', journal);
      listener.child.unref(); // Failure retains the detached listener without trapping this controller.
      await mkdir(join(env.CODEX_HOME, 'app-server-control'), { mode: 0o700 });
      await symlink(join(root, 'runtime/native.sock'), join(env.CODEX_HOME, 'app-server-control/app-server-control.sock'));
      let ready = false;
      for (let i = 0; i < 200; i++) { try { ready = (await lstat(join(root, 'runtime/native.sock'))).isSocket(); } catch (e) { if (e.code !== 'ENOENT') throw e; } if (ready) break; check(await stamp(listener.owner.pid) === listener.owner.start, 'native listener exited'); await sleep(50); }
      check(ready, 'native listener socket unavailable');
    } else {
      const st = await lstat(acpSettings); check(st.isFile() && !st.isSymbolicLink() && st.uid === process.geteuid() && !(st.mode & 0o077), 'ACP settings not private');
      const settings = JSON.parse(await readFile(acpSettings, 'utf8'));
      const allowed = new Set(['ANTHROPIC_API_KEY', 'ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_BASE_URL', 'ANTHROPIC_MODEL', 'ANTHROPIC_DEFAULT_HAIKU_MODEL', 'ANTHROPIC_DEFAULT_SONNET_MODEL', 'ANTHROPIC_DEFAULT_OPUS_MODEL']);
      const minimal = { env: Object.fromEntries(Object.entries(settings.env || {}).filter(([k,v]) => allowed.has(k) && typeof v === 'string' && v)) };
      check(minimal.env.ANTHROPIC_API_KEY || minimal.env.ANTHROPIC_AUTH_TOKEN, 'ACP has no self-contained private provider seed; no model dispatched');
      if (typeof settings.model === 'string') minimal.model = settings.model;
      await mkdir(join(env.HOME, '.claude'), { mode: 0o700 }); await writeFile(join(env.HOME, '.claude/settings.json'), JSON.stringify(minimal), { mode: 0o600 });
      const base = '/work/package-cache/xertrov/fleet/vecnet-acp/claude-agent-acp-0.101.0';
      const executable = join(base, 'bin/claude-agent-acp'), pkg = join(base, 'node_modules/@agentclientprotocol/claude-agent-acp');
      const entry = join(pkg, 'dist/index.js'), node = '/home/xertrov/.local/opt/node-v22.23.3-linux-x64/bin/node';
      approved.acp = { executable, executable_sha256: await hash(executable), package_json: join(pkg, 'package.json'), package_name: '@agentclientprotocol/claude-agent-acp', package_version: '0.101.0',
        entry, entry_sha256: await hash(entry), node_executable: node, node_sha256: await hash(node), agent_name: '@agentclientprotocol/claude-agent-acp', agent_version: '0.101.0', protocol_version: 1 };
      env.VECNET_ACP_COMMAND = executable;
    }
    await writeFile(join(root, 'approval.json'), JSON.stringify(approved), { mode: 0o600 });
    Object.assign(env, { VECNET_BIN: join(root, 'bin/vecnet'), VECNET_UI_DEMO_BIN: join(root, 'bin/demo'), VECNET_SMOKE_APPROVAL: join(root, 'approval.json'),
      VECNET_NATIVE_LAUNCHER_SMOKE: '1', VECNET_SMOKE_BACKEND: backend, VECNET_SMOKE_WORKSPACE: mode, SHOTS_DIR: join(root, 'shots') });
    const smoke = await ownedChild(process.execPath, [join(source, 'scripts/native-launcher-live-smoke.mjs')], env, root, 'smoke', journal);
    const result = await smoke.done;
    journal.update({ smoke_result: result });
    check(result.code === 0 && !result.signal, 'smoke failed; all recovery material and listener retained');
    const dirs = (await readdir(join(root, 'tmp'))).filter(x => x.startsWith('vecnet-native-launcher-'));
    check(dirs.length === 1, 'unproved smoke receipt location');
    const receiptPath = join(root, 'tmp', dirs[0], 'receipt.json');
    const receipt = JSON.parse(await readFile(receiptPath, 'utf8'));
    check(receipt?.pass === true && receipt.teardown?.complete === true && receipt.cancellation?.state === 'cancelled' && receipt.workspace_mode === mode && receipt.backend === backend, 'unproved smoke receipt');
    journal.update({ receipt_path: receiptPath, receipt });
    if (listener) await stopOwned(listener, journal, 'native');
    journal.update({ complete: true });
    return { root, complete: true, receiptPath };
  } catch (error) {
    // Do not serialize error text: provider/package diagnostics may contain secrets.
    journal.update({ complete: false, failure: 'private gate failed; inspect private command logs and ownership journal' });
    throw new Error('disposable smoke incomplete; recovery retained at ' + root);
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (!process.argv.includes('--run')) console.log('SKIP: --run requires FINAL_SOURCE, FINAL_CLI, FINAL_DEMO; no services or models started');
  else {
    try { await runDisposableScenario({ source: process.env.FINAL_SOURCE, cli: process.env.FINAL_CLI, demo: process.env.FINAL_DEMO,
      backend: process.env.SMOKE_BACKEND || 'codex-app-server', mode: process.env.SMOKE_WORKSPACE || 'worktree',
      nativeSeed: process.env.NATIVE_PROVIDER_SEED || '/tmp/vecnet-auto-ClcUTt/home/.codex/config.toml',
      acpSettings: process.env.ACP_PROVIDER_SETTINGS || '/home/xertrov/.claude/settings.json' }); }
    catch (e) { console.error(e.message); process.exitCode = 1; }
  }
}
