// Safety checks only: every Codex executable is a disposable stub.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmod, lstat, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const script = fileURLToPath(new URL('./hook-smoke.mjs', import.meta.url));

async function fixture(t, help = '--dangerously-bypass-hook-trust') {
  const dir = await mkdtemp(join(tmpdir(), 'vecnet-codex-preflight-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const home = join(dir, 'home'), path = join(dir, 'bin'), temporary = join(dir, 'tmp');
  await mkdir(home); await mkdir(path); await mkdir(temporary);
  // A PATH with Node alone guarantees we cannot discover a real Codex.
  await symlink(process.execPath, join(path, 'node'));
  if (help !== null) {
    await writeFile(join(path, 'codex'), `#!/bin/sh\n[ "$#" = 2 ] && [ "$1" = exec ] && [ "$2" = --help ] || exit 99\nprintf '%s\\n' '${help}'\n`, { mode: 0o755 });
  }
  const env = { PATH: path, HOME: home, TMPDIR: temporary,
    // Poisoned inherited state must never be consulted by these paths.
    CODEX_HOME: join(dir, 'do-not-touch-codex'), VECNET_CONFIG: join(dir, 'do-not-touch.toml'),
    XDG_CONFIG_HOME: join(dir, 'do-not-touch-xdg'), XDG_CACHE_HOME: join(dir, 'do-not-touch-cache'),
    CLAUDE_PID: '12345', CLAUDE_CODE_SESSION_ID: 'inherited-session', CLAUDE_PROJECT_DIR: '/inherited-project' };
  return { dir, home, env, run: smokeHome => spawnSync(process.execPath, [script], {
    env: { ...env, ...(smokeHome === undefined ? {} : { CODEX_SMOKE_HOME: smokeHome }) }, encoding: 'utf8', timeout: 10000,
  }) };
}

// Include directory listings and mtimes, plus regular file bytes and modes.
async function tree(path) {
  const stat = await lstat(path);
  if (stat.isSymbolicLink()) return { mode: stat.mode, mtime: stat.mtimeMs };
  if (!stat.isDirectory()) return { mode: stat.mode, mtime: stat.mtimeMs, bytes: (await readFile(path)).toString('hex') };
  const children = {};
  for (const name of (await readdir(path)).sort()) children[name] = await tree(join(path, name));
  return { mode: stat.mode, mtime: stat.mtimeMs, children };
}

function exit(result, code, output) {
  assert.ifError(result.error);
  assert.equal(result.status, code, result.stderr);
  assert.equal(result.signal, null);
  assert.match(result.stdout + result.stderr, output);
  if (code === 0) {
    assert.equal(result.stderr, '');
    assert.equal(result.stdout.trim().split('\n').length, 1);
  }
}

test('no codex on PATH skips without touching state', async t => {
  const f = await fixture(t, null), before = await tree(f.dir);
  exit(f.run(), 0, /^SKIP: codex is not on PATH\n$/);
  assert.deepEqual(await tree(f.dir), before);
});

test('missing hook-trust flag skips before looking at the configured home', async t => {
  const f = await fixture(t, 'no hook flags'), before = await tree(f.dir);
  exit(f.run(join(f.home, '.codex')), 0, /^SKIP: .*lacks --dangerously-bypass-hook-trust\n$/);
  assert.deepEqual(await tree(f.dir), before);
});

test('supported stub with CODEX_SMOKE_HOME unset skips without touching state', async t => {
  const f = await fixture(t), before = await tree(f.dir);
  exit(f.run(), 0, /^SKIP: CODEX_SMOKE_HOME is unset.*\n$/);
  assert.deepEqual(await tree(f.dir), before);
});

test('personal Codex home fixture is refused with its listing and mtime unchanged', async t => {
  const f = await fixture(t), personal = join(f.home, '.codex');
  await mkdir(personal);
  await writeFile(join(personal, 'hooks.json'), '{"keep":"personal"}\n', { mode: 0o640 });
  await writeFile(join(personal, '.vecnet-smoke'), 'a marker cannot authorize this home\n');
  const before = await tree(f.dir);
  exit(f.run(personal), 2, /^REFUSE: .*must not be \$HOME\/\.codex\n$/);
  assert.deepEqual(await tree(f.dir), before);
});

test('symlink alias of the personal Codex home fixture is refused', async t => {
  const f = await fixture(t), personal = join(f.home, '.codex'), alias = join(f.dir, 'alias');
  await mkdir(personal); await symlink(personal, alias);
  const before = await tree(f.dir);
  exit(f.run(alias), 2, /^REFUSE: .*resolves to \$HOME\/\.codex\n$/);
  assert.deepEqual(await tree(f.dir), before);
});

test('hooks without a smoke marker are refused unchanged', async t => {
  const f = await fixture(t), disposable = join(f.dir, 'disposable');
  await mkdir(disposable);
  await writeFile(join(disposable, 'hooks.json'), '{"keep":"unmarked"}\n');
  await chmod(join(disposable, 'hooks.json'), 0o640);
  const before = await tree(f.dir);
  exit(f.run(disposable), 2, /^REFUSE: existing hooks.json has no .vecnet-smoke marker\n$/);
  assert.deepEqual(await tree(f.dir), before);
});
