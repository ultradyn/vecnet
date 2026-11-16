import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, readdir, writeFile, rm, access } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { publishEvidence } from '../evidence/publish.mjs';

const scratch = fileURLToPath(new URL('../../.superpowers/sdd/2026-11-16-public-evidence/scratch/', import.meta.url));
const wrapper = fileURLToPath(new URL('./run.sh', import.meta.url));
async function fixture(t) {
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, 'bb-handoff-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const path of ['bin', 'runtime', 'remote']) await mkdir(join(root, path));
  await writeFile(join(root, 'go.mod'), 'module github.com/XertroV/vecnet\n');
  // Exercise the real wrapper, but replace every SSH/rsync boundary. No host is contacted.
  await writeFile(join(root, 'bin/ssh'), `#!${process.execPath}
const fs = require('node:fs'); const path = require('node:path');
const cmd = process.argv.at(-1);
if (cmd === 'printf %s "$HOME"') { process.stdout.write(process.env.REMOTE_FIXTURE); process.exit(0); }
// Model the old wrapper's remote cleanup accurately when testing its baseline.
if (cmd.startsWith('rm -rf -- ')) {
  const directory = cmd.match(/'([^']*)'/)[1];
  if (!directory.startsWith(process.env.REMOTE_FIXTURE + '/')) process.exit(90);
  fs.rmSync(directory, { recursive: true, force: true }); fs.mkdirSync(directory, { recursive: true });
}
if (cmd.includes('/remote.sh')) {
  const quoted = [...cmd.matchAll(/'([^']*)'/g)].map(x => x[1]);
  const env = Object.fromEntries(Buffer.from(quoted.at(-2), 'base64').toString().split('\\0').filter(Boolean).map(s => { const i = s.indexOf('='); return [s.slice(0,i),s.slice(i+1)]; }));
  const directory = env.SHOTS_DIR;
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, process.env.CAPTURE_NAME), 'fresh capture');
  process.exit(Number(process.env.REMOTE_STATUS || 0));
}
`, { mode: 0o700 });
  await writeFile(join(root, 'bin/rsync'), `#!${process.execPath}
const fs = require('node:fs'); const path = require('node:path'); const cp = require('node:child_process');
const [source, destination] = process.argv.slice(-2);
if (source.startsWith('fixture-host:')) {
  fs.mkdirSync(destination, { recursive: true });
  if (process.env.PULL_FAIL === '1') { fs.writeFileSync(path.join(destination, 'partial.png'), 'partial'); process.exit(23); }
  process.exit(cp.spawnSync('/usr/bin/rsync', ['-rlt', source.slice('fixture-host:'.length), destination], { stdio: 'inherit' }).status);
}
`, { mode: 0o700 });
  const run = (destination, extra = {}) => spawnSync('bash', [wrapper, 'true'], {
    cwd: root, encoding: 'utf8', env: { ...process.env, PATH: `${join(root, 'bin')}:${process.env.PATH}`,
      BB_ROOT: root, BB_LANE: 'fixture', BB_HOST: 'fixture-host', BB_PULLBACK: '0', BB_JOBS: '2',
      XDG_RUNTIME_DIR: join(root, 'runtime'), REMOTE_FIXTURE: join(root, 'remote'), SHOTS_DIR: destination,
      CAPTURE_NAME: 'current.png', ...extra },
  });
  return { root, run };
}

test('build-box refuses an old destination rather than merging captures (Plan 4 final I1)', async t => {
  const { root, run } = await fixture(t);
  const destination = join(root, 'old output');
  await mkdir(destination);
  await writeFile(join(destination, 'obsolete.png'), 'old');
  const result = run(destination);
  assert.notEqual(result.status, 0, 'existing destination must not merge old media into a completed capture');
  assert.deepEqual(await readdir(destination), ['obsolete.png']);
});

test('build-box transfer failure propagates and offers no completed directory (Plan 4 final I1)', async t => {
  const { root, run } = await fixture(t);
  const destination = join(root, 'failed transfer');
  const result = run(destination, { PULL_FAIL: '1' });
  assert.equal(result.status, 23, result.stderr);
  assert.doesNotMatch(result.stderr, /captures:|shots: \d/);
  await assert.rejects(access(destination), { code: 'ENOENT' });
});

test('two build-box generations hand exactly fresh bytes to publication (Plan 4 final I1)', async t => {
  const { root, run } = await fixture(t);
  const outcomes = [];
  for (const [generation, name] of [[1, 'obsolete.png'], [2, 'current.png']]) {
    const directory = join(root, `generation ${generation}`);
    const result = run(directory, { CAPTURE_NAME: name });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(await readdir(directory), [name]);
    const published = await publishEvidence({ directory, dryRun: true, commit: '1'.repeat(40), outputRoot: join(root, 'publish'),
      config: { accountId: '1'.repeat(32), bucket: 'test-evidence', publicUrl: 'https://example.invalid', retentionDays: 30 } });
    assert.deepEqual(published.manifest.files.map(file => file.name), [name]);
    outcomes.push([result, directory]);
  }
  for (const [result, directory] of outcomes) assert.ok(result.stderr.includes(`captures: ${directory}`));
  assert.equal(await readFile(join(root, 'generation 1/obsolete.png'), 'utf8'), 'fresh capture');
});

test('failed remote captures are not handed off as complete (Plan 4 final I1)', async t => {
  const { root, run } = await fixture(t);
  const directory = join(root, 'failed capture');
  const result = run(directory, { REMOTE_STATUS: '19' });
  assert.equal(result.status, 19, result.stderr);
  await assert.rejects(access(directory), { code: 'ENOENT' });
  assert.doesNotMatch(result.stderr, /captures:/);
});
