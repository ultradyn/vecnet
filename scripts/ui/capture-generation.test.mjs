import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFile, mkdtemp, mkdir, readFile, readdir, writeFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { publishEvidence } from '../evidence/publish.mjs';

const scratch = fileURLToPath(new URL('../../.superpowers/sdd/2026-11-16-public-evidence/scratch/', import.meta.url));
async function fixture(t) {
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, 'capture-generation-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const path of ['scripts/ui', 'bin', 'docs/plans/shots/oui', '.scratch/evidence/ui']) await mkdir(join(root, path), { recursive: true });
  for (const name of ['shot-paths.mjs', 'shots-index.mjs', 'capture-generation.mjs']) {
    try { await copyFile(new URL(name, import.meta.url), join(root, 'scripts/ui', name)); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  await writeFile(join(root, 'scripts/ui/reap-demos.sh'), 'exit 0\n');
  await writeFile(join(root, 'scripts/ui/screen.mjs'), '');
  await writeFile(join(root, 'docs/plans/shots/oui/README.md'), '[expected.png](expected.png)\n');
  await writeFile(join(root, 'docs/plans/shots/oui/expected.png'), 'historical');
  await writeFile(join(root, '.scratch/evidence/ui/expected.png'), 'old capture');
  await writeFile(join(root, 'bin/build2'), `#!/usr/bin/env bash
if [[ "$2" == --test ]]; then exit 0; fi
if [[ "$2" == scripts/ui/shots-index.mjs ]]; then exec "$NODE" "$2"; fi
if [[ "$2" == scripts/ui/screen.mjs && "$EMIT" == 1 ]]; then
  mkdir -p "\${SHOTS_DIR:-.scratch/evidence/ui}"
  printf current >"\${SHOTS_DIR:-.scratch/evidence/ui}/expected.png"
fi
`, { mode: 0o700 });
  const justfile = await readFile(new URL('../../justfile', import.meta.url), 'utf8');
  const body = justfile.match(/^ui-shots:\n((?:[ \t].*\n|\n)+)/m)?.[1];
  assert.ok(body);
  const recipe = join(root, 'recipe.sh');
  await writeFile(recipe, body.replace(/^ {4}/gm, ''));
  const run = (extra = {}) => {
    const env = { ...process.env, NODE: process.execPath, PATH: `${join(root, 'bin')}:${process.env.PATH}`, EMIT: '0', ...extra };
    if (!Object.hasOwn(extra, 'SHOTS_DIR')) delete env.SHOTS_DIR;
    return spawnSync('bash', [recipe], { cwd: root, env, encoding: 'utf8' });
  };
  return { root, run };
}

test('old default captures cannot satisfy an omitted current screenshot (Plan 4 final I1)', async t => {
  const { root, run } = await fixture(t);
  const result = run();
  assert.notEqual(result.status, 0, 'old expected.png must not make an empty generation pass');
  assert.match(result.stderr, /expected\.png|ENOENT/);
  assert.equal(await readFile(join(root, '.scratch/evidence/ui/expected.png'), 'utf8'), 'old capture');
  assert.equal(await readFile(join(root, 'docs/plans/shots/oui/expected.png'), 'utf8'), 'historical');
});

test('explicit capture destination refuses reuse without deleting caller files (Plan 4 final I1)', async t => {
  const { root, run } = await fixture(t);
  const result = run({ SHOTS_DIR: '.scratch/evidence/ui', EMIT: '1' });
  assert.notEqual(result.status, 0, 'existing output must not be reused');
  assert.equal(await readFile(join(root, '.scratch/evidence/ui/expected.png'), 'utf8'), 'old capture');
});

test('successful generations publish exactly the completed fresh directory (Plan 4 final I1)', async t => {
  const { root, run } = await fixture(t);
  const completed = [];
  for (const extra of [{ EMIT: '1' }, { EMIT: '1', SHOTS_DIR: '.scratch/evidence/explicit fresh' }]) {
    const result = run(extra);
    assert.equal(result.status, 0, result.stderr);
    const directory = result.stdout.match(/^captures: (.+)$/m)?.[1];
    assert.ok(directory, 'recipe offers its exact completed directory after the index gate');
    assert.notEqual(directory, join(root, '.scratch/evidence/ui'));
    assert.deepEqual(await readdir(directory), ['expected.png']);
    assert.equal(await readFile(join(directory, 'expected.png'), 'utf8'), 'current');
    const published = await publishEvidence({ directory, dryRun: true, commit: '1'.repeat(40), outputRoot: join(root, 'publish'),
      config: { accountId: '1'.repeat(32), bucket: 'test-evidence', publicUrl: 'https://example.invalid', retentionDays: 30 } });
    assert.deepEqual(published.manifest.files.map(file => file.name), ['expected.png']);
    completed.push(directory);
  }
  assert.notEqual(...completed);
});
