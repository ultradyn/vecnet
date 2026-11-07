// Opt-in, coordinator-run live check. Preflight runs before any state is made.
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { chmodSync, constants, rmSync, writeFileSync } from 'node:fs';
import { access, chmod, lstat, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { homedir, tmpdir } from 'node:os';
import { delimiter, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(import.meta.url);
const root = resolve(dirname(script), '../..');
const bin = join(root, 'bin/vecnet');
const hint = 'codex exec may not run hooks: use the manual interactive check in docs/connectors/codex.md';
const children = new Set();
process.on('exit', () => {
  for (const child of children) {
    try { process.kill(-child.pid, 'SIGKILL'); } catch {}
  }
});

function childEnv(extra = {}) {
  const env = { ...process.env, ...extra };
  for (const name of ['CLAUDE_PID', 'CLAUDE_CODE_SESSION_ID', 'CLAUDE_PROJECT_DIR']) delete env[name];
  return env;
}

function run(command, args, { input = '', timeout = 30000, env = childEnv(), inherit = false, forwardSignals = false } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd: root, env, detached: true, stdio: ['pipe', inherit ? 'inherit' : 'pipe', inherit ? 'inherit' : 'pipe'] });
    children.add(child);
    child.once('exit', () => children.delete(child));
    const signals = ['SIGINT', 'SIGTERM', 'SIGHUP'];
    const forward = signal => { try { process.kill(-child.pid, signal); } catch {} };
    const handlers = signals.map(signal => () => forward(signal));
    if (forwardSignals) signals.forEach((signal, i) => process.on(signal, handlers[i]));
    const done = () => {
      clearTimeout(timer);
      children.delete(child);
      if (forwardSignals) signals.forEach((signal, i) => process.removeListener(signal, handlers[i]));
    };
    let stdout = '', stderr = '', timedOut = false;
    child.stdout?.on('data', data => { stdout += data; });
    child.stderr?.on('data', data => { stderr += data; });
    child.stdin.on('error', () => {});
    child.stdin.end(input);
    const timer = timeout && setTimeout(() => {
      timedOut = true;
      try { process.kill(-child.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') reject(error); }
    }, timeout);
    child.once('error', error => { done(); reject(error); });
    child.once('close', (code, signal) => { done(); resolve({ code, signal, stdout, stderr, timedOut }); });
  });
}

async function statIfPresent(path) {
  try { return await lstat(path); } catch (error) { if (error.code !== 'ENOENT') throw error; }
}

async function preflight() {
  let codex;
  for (const dir of (process.env.PATH || '').split(delimiter)) {
    const candidate = resolve(dir, 'codex');
    try { await access(candidate, constants.X_OK); if ((await lstat(candidate)).isDirectory()) continue; codex = candidate; break; }
    catch (error) { if (!['ENOENT', 'EACCES', 'ENOTDIR'].includes(error.code)) throw error; }
  }
  if (!codex) return { skip: 'codex is not on PATH' };
  const help = await run(codex, ['exec', '--help']);
  if (!help.stdout.includes('--dangerously-bypass-hook-trust')) return { skip: 'codex exec --help lacks --dangerously-bypass-hook-trust' };
  if (!process.env.CODEX_SMOKE_HOME) return { skip: 'CODEX_SMOKE_HOME is unset; prepare a disposable Codex home with model access' };

  const home = resolve(process.env.CODEX_SMOKE_HOME);
  // Compare lexically first: refusing the default must not even stat it.
  const personal = join(homedir(), '.codex');
  const canonicalPersonal = join(await realpath(homedir()), '.codex');
  if (home === personal || home === canonicalPersonal) return { refuse: 'CODEX_SMOKE_HOME must not be $HOME/.codex' };
  const canonicalHome = await realpath(home);
  if (canonicalHome === personal || canonicalHome === canonicalPersonal) return { refuse: 'CODEX_SMOKE_HOME resolves to $HOME/.codex' };
  for (const name of ['hooks.json', '.vecnet-smoke']) {
    const stat = await statIfPresent(join(canonicalHome, name));
    if (stat && !stat.isFile()) return { refuse: `${name} must be a regular file, not a symlink or directory` };
  }
  if (await statIfPresent(join(canonicalHome, 'hooks.json')) && !await statIfPresent(join(canonicalHome, '.vecnet-smoke'))) {
    return { refuse: 'existing hooks.json has no .vecnet-smoke marker' };
  }
  return { home: canonicalHome, codex };
}

async function snapshot(path) {
  const stat = await statIfPresent(path);
  return stat ? { bytes: await readFile(path), mode: stat.mode & 0o7777 } : undefined;
}

async function restore(path, saved) {
  if (!saved) return rm(path, { force: true });
  await chmod(path, 0o600).catch(error => { if (error.code !== 'ENOENT') throw error; });
  await writeFile(path, saved.bytes);
  await chmod(path, saved.mode);
}

function restoreSync(path, saved) {
  if (!saved) return rmSync(path, { force: true });
  try { chmodSync(path, 0o600); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  writeFileSync(path, saved.bytes);
  chmodSync(path, saved.mode);
}

async function jsonIfPresent(path) {
  try { return JSON.parse(await readFile(path, 'utf8')); } catch (error) { if (error.code !== 'ENOENT') throw error; }
}

async function smoke(home, codex, dir) {
  const hooksPath = join(home, 'hooks.json'), markerPath = join(home, '.vecnet-smoke');
  const hooks = await snapshot(hooksPath), marker = await snapshot(markerPath);
  let demoEnv, unregisterCleanup;
  try {
    // This process was launched with a private environment, so bootDemo's
    // own build and hub children inherit it too, without mutating the caller.
    const { bootDemo, registerExitCleanup } = await import('../ui/lib.mjs');
    // The harness exits directly on signals. Keep a synchronous backstop
    // for that path; ordinary success and failure use the finally below.
    unregisterCleanup = registerExitCleanup(() => {
      try { restoreSync(hooksPath, hooks); } finally { restoreSync(markerPath, marker); }
    });
    demoEnv = await bootDemo();
    const { demo, asPeer } = demoEnv;
    await writeFile(join(dir, 'config.toml'), `[profiles.smoke]\nhub = ${JSON.stringify(demo.url)}\nsecret = ${JSON.stringify(demo.peers.codex.secret)}\npeer_id = ${JSON.stringify(demo.peers.codex.id)}\n`, { mode: 0o600 });
    await writeFile(markerPath, 'vecnet disposable hook smoke\n', { mode: 0o600 });
    const cli = async (args, options) => {
      const result = await run(bin, args, options);
      if (result.code !== 0) throw new Error(`vecnet ${args[0]} exited ${result.code}: ${result.stderr}`);
      return result;
    };
    await cli(['hook', 'install', '--client', 'codex', '--profile', 'smoke', '--bin', bin, '--settings', hooksPath, '--write']);
    await cli(['hook', '--profile', 'smoke'], { input: '{"hook_event_name":"UserPromptSubmit"}\n' });
    const statePath = join(dir, 'state/smoke.json'), bindingPath = join(dir, 'state/smoke+session.json');
    const seed = await jsonIfPresent(statePath), seedBinding = await jsonIfPresent(bindingPath);
    if (!seed?.poll_cursor || !seed.last_post_tool_poll) throw new Error('no-session seed did not establish the hook cursor');

    const token = `VXC-${randomBytes(6).toString('hex')}`;
    const sent = await asPeer('claw', 'POST', '/v1/messages', {
      idempotency_key: token, recipients: [demo.peers.codex.id], priority: 'wake', content: { text: token },
    });
    if (!sent.ok) throw new Error(`send canary: HTTP ${sent.status}`);
    const message = await sent.json();
    const delivery = message.deliveries?.find(item => item.recipient === demo.peers.codex.id)?.delivery_id;
    if (!delivery) throw new Error('send canary returned no codex delivery');

    const result = await run(codex, ['exec', '--dangerously-bypass-hook-trust', '--skip-git-repo-check', '--ephemeral', '-C', join(dir, 'ws'), '-o', join(dir, 'last.txt'),
      'Reply with only the VXC- token from the vecnet block in your context, or NONE.'], { timeout: 180000 });
    const last = await readFile(join(dir, 'last.txt'), 'utf8').catch(error => { if (error.code === 'ENOENT') return ''; throw error; });
    const state = await jsonIfPresent(statePath), binding = await jsonIfPresent(bindingPath);
    console.log(`INFO: handler poll changed during exec: ${state?.last_post_tool_poll !== seed.last_post_tool_poll}; seed=${seed.last_post_tool_poll}; final=${state?.last_post_tool_poll ?? 'absent'}`);
    console.log(`INFO: separate session binding: ${JSON.stringify({ seed: seedBinding ?? null, final: binding ?? null })}`);
    console.log(`INFO: start/prompt order: seed session=${seedBinding?.session ?? 'none'}, final binding=${binding?.session_how ?? 'none'} at ${binding?.session_at ?? 'absent'}, final poll=${state?.last_post_tool_poll ?? 'absent'}. A fresh SessionStart cannot bind; UserPromptSubmit must bind before session polling. Subagents are not asserted (B1).`);
    const inbox = await run(bin, ['--net', 'smoke', '--json', 'inbox']);
    const unacked = inbox.code === 0 && JSON.parse(inbox.stdout).events?.some(event => event.type === 'delivery' && event.data.delivery_id === delivery && event.data.message.message_id === message.message_id);
    if (result.code === 0 && !result.timedOut && last.includes(token) && unacked) {
      console.log('PASS: canary reached Codex context and its delivery remains unacked');
      return 0;
    }
    console.error(`FAIL: codex exit code=${result.code}, signal=${result.signal}, timed_out=${result.timedOut}; canary unacked=${!!unacked}`);
    console.error(`last.txt: ${last}`);
    if (inbox.code !== 0) console.error(`inbox exited ${inbox.code}: ${inbox.stderr}`);
    console.error(hint);
    return 1;
  } finally {
    // Attempt every cleanup even if an earlier one fails.
    const cleaned = await Promise.allSettled([restore(hooksPath, hooks), restore(markerPath, marker), demoEnv?.close()]);
    const failed = cleaned.find(item => item.status === 'rejected');
    if (failed) throw failed.reason;
    unregisterCleanup?.();
  }
}

async function main() {
  const checked = await preflight();
  if (checked.skip) { console.log(`SKIP: ${checked.skip}`); return 0; }
  if (checked.refuse) { console.error(`REFUSE: ${checked.refuse}`); return 2; }
  if (process.argv[2] === '--isolated') return smoke(checked.home, checked.codex, process.env.CODEX_SMOKE_DIR);

  const dir = await mkdtemp(join(tmpdir(), 'vecnet-codex-smoke-'));
  try {
    await mkdir(join(dir, 'ws'), { mode: 0o700 });
    await mkdir(join(dir, 'tmp'), { mode: 0o700 });
    const result = await run(process.execPath, [script, '--isolated'], { timeout: 0, inherit: true, forwardSignals: true, env: childEnv({
      CODEX_SMOKE_HOME: checked.home, CODEX_HOME: checked.home, CODEX_SMOKE_DIR: dir,
      VECNET_CONFIG: join(dir, 'config.toml'), VECNET_PROFILE: 'smoke', XDG_CONFIG_HOME: join(dir, 'xdg'), XDG_CACHE_HOME: join(dir, 'cache'), TMPDIR: join(dir, 'tmp'),
    }) });
    return result.code ?? 1;
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

try { process.exitCode = await main(); }
catch (error) { console.error(`FAIL: ${error.message}`); console.error(hint); process.exitCode = 1; }
