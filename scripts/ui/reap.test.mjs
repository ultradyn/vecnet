import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { copyFile, mkdtemp, mkdir, rm, utimes } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const reaper = new URL('reap-demos.sh', import.meta.url).pathname;
async function setup(t) {
  const dir = await mkdtemp(join(tmpdir(), 'vecnet-reap-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  // Regex punctuation in a listed root must still be compared literally.
  const roots = [join(dir, 'root[1]'), join(dir, 'root.2')];
  const foreign = join(dir, 'root1');
  const tmuxRoot = join(dir, 'sockets');
  for (const root of [...roots, foreign, tmuxRoot]) await mkdir(root);
  const env = { ...process.env, REAP_ROOTS: roots.join(':'), TMUX_TMPDIR: tmuxRoot };
  async function reap(age) {
    const child = spawn('bash', [reaper], { env: { ...env, REAP_MIN_AGE: String(age) } });
    let out = '', err = '';
    child.stdout.on('data', d => { out += d; });
    child.stderr.on('data', d => { err += d; });
    const [code] = await once(child, 'close');
    assert.equal(code, 0, err);
    assert.match(out, /^reaped \d+ demo\(s\), \d+ tmux server\(s\), \d+ dir\(s\)\n$/);
    return out;
  }
  async function demo(root, suffix, shell = false, data) {
    const dir = join(root, `vecnet-ui-build-${suffix}`);
    await mkdir(dir);
    const bin = join(dir, 'demo');
    await copyFile(shell ? '/bin/sh' : '/usr/bin/sleep', bin);
    const child = spawn(bin, shell ? ['-c', 'sleep 300; :', '--data-dir', data] : ['300'], { detached: true, stdio: 'ignore' });
    const closed = once(child, 'close');
    t.after(async () => {
      try { process.kill(-child.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
      await closed;
    });
    await once(child, 'spawn');
    return { child, dir, closed };
  }
  return { roots, foreign, tmuxRoot, env, reap, demo };
}
function alive(pid) {
  try { process.kill(pid, 0); return true; }
  catch (error) { assert.equal(error.code, 'ESRCH'); return false; }
}

test('reaps old demos in every root, and only those', async t => {
  const { roots, foreign, reap, demo } = await setup(t);
  const a = await demo(roots[0], 'abc123');
  const b = await demo(roots[1], 'def456');
  const other = await demo(foreign, 'abc123');
  const invalid = await demo(roots[0], 'x.y');
  assert.match(await reap(7200), /^reaped 0 demo/);
  for (const { child } of [a, b, other, invalid]) assert.ok(alive(child.pid));
  assert.match(await reap(0), /^reaped 2 demo/);
  await Promise.all([a.closed, b.closed]);
  assert.equal(alive(a.child.pid), false);
  assert.equal(alive(b.child.pid), false);
  assert.ok(alive(other.child.pid), 'an unlisted literal root survives');
  assert.ok(alive(invalid.child.pid), 'a non-alnum build suffix survives');
});

test('keeps dirs in use and removes stale ones', async t => {
  const { roots, reap, demo } = await setup(t);
  const data = join(roots[1], 'vecnet-ui-data-active123');
  const orphan = join(roots[0], 'vecnet-ui-data-zzz111');
  const orphanBuild = join(roots[1], 'vecnet-ui-build-orphan123');
  const invalid = join(roots[0], 'vecnet-ui-data-x.y');
  for (const dir of [data, orphan, orphanBuild, invalid]) {
    await mkdir(dir);
    await utimes(dir, new Date(0), new Date(0));
  }
  const active = await demo(roots[0], 'active123', true, data);
  await reap(0);
  assert.ok(existsSync(active.dir), 'a recorded live demo owns its build dir for this pass');
  assert.ok(existsSync(data), 'a recorded live demo owns its --data-dir for this pass');
  assert.equal(existsSync(orphan), false);
  assert.equal(existsSync(orphanBuild), false);
  assert.ok(existsSync(invalid), 'bad suffix is never removed');
  // A fresh orphan survives the ordinary age threshold.
  await mkdir(orphan);
  await reap(7200);
  assert.ok(existsSync(orphan));
});

test('reaps a tmux server whose owner pid is dead', async t => {
  const { tmuxRoot, env, reap } = await setup(t);
  const owner = spawn('/usr/bin/true');
  const dead = owner.pid;
  await once(owner, 'close');
  const socket = `oui-${dead}`;
  const socketPath = join(tmuxRoot, `tmux-${process.getuid()}`, socket);
  const tmux = (...args) => spawnSync('tmux', ['-L', socket, ...args], { env: { ...env, TMUX: '' }, encoding: 'utf8' });
  t.after(() => tmux('kill-server'));
  const started = tmux('-f', '/dev/null', 'new-session', '-d', 'sleep 300');
  assert.equal(started.status, 0, started.stderr);
  const liveSocket = `oui-${process.pid}`;
  const liveTmux = (...args) => spawnSync('tmux', ['-L', liveSocket, ...args], { env: { ...env, TMUX: '' }, encoding: 'utf8' });
  t.after(() => liveTmux('kill-server'));
  const liveStarted = liveTmux('-f', '/dev/null', 'new-session', '-d', 'sleep 300');
  assert.equal(liveStarted.status, 0, liveStarted.stderr);
  assert.ok(existsSync(socketPath));
  assert.equal(tmux('has-session').status, 0);
  assert.match(await reap(0), /, 1 tmux server\(s\),/);
  assert.notEqual(tmux('has-session').status, 0);
  assert.equal(existsSync(socketPath), false);
  assert.equal(liveTmux('has-session').status, 0, 'a live owner keeps its private server');
  assert.ok(existsSync(join(tmuxRoot, `tmux-${process.getuid()}`, liveSocket)));
});
