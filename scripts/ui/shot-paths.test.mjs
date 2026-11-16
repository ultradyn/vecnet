import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFile, mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';

const scratch = fileURLToPath(new URL('../../.superpowers/sdd/2026-11-16-public-evidence/scratch/', import.meta.url));

async function fixture(t) {
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, 'vecnet-shot-paths-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(join(root, 'scripts/ui'), { recursive: true });
  await mkdir(join(root, 'docs/plans/shots/oui'), { recursive: true });
  for (const name of ['lib.mjs', 'shots-index.mjs', 'shot-paths.mjs']) {
    await copyFile(new URL(name, import.meta.url), join(root, 'scripts/ui', name));
  }
  await copyFile(new URL('../../.gitignore', import.meta.url), join(root, '.gitignore'));
  await writeFile(join(root, 'docs/plans/shots/oui/README.md'), '[expected.png](expected.png)\n');
  await writeFile(join(root, 'docs/plans/shots/oui/expected.png'), 'historical image');
  return root;
}

test('screenshot directory resolves defaults and SHOTS_DIR against the repository root', async () => {
  const { shotsDirectory } = await import('./shot-paths.mjs');
  const root = fileURLToPath(new URL('../../', import.meta.url));
  assert.equal(shotsDirectory({}), join(root, '.scratch/evidence/ui'));
  assert.equal(shotsDirectory({ SHOTS_DIR: '' }), join(root, '.scratch/evidence/ui'));
  assert.equal(shotsDirectory({ SHOTS_DIR: 'relative/captures' }), join(root, 'relative/captures'));
  assert.equal(shotsDirectory({ SHOTS_DIR: '/tmp/absolute-captures' }), '/tmp/absolute-captures');
});

test('default screenshot paths and index checks use ignored output', async t => {
  const root = await fixture(t);
  const { shotPath } = await import(pathToFileURL(join(root, 'scripts/ui/lib.mjs')));
  const { checkShotsIndex } = await import(pathToFileURL(join(root, 'scripts/ui/shots-index.mjs')));
  const previous = process.env.SHOTS_DIR;
  delete process.env.SHOTS_DIR;
  try {
    const path = await shotPath('expected');
    assert.equal(path, join(root, '.scratch/evidence/ui/expected.png'));
    await writeFile(path, 'generated image');
    assert.equal(await checkShotsIndex(), 1);
    assert.equal(await readFile(join(root, 'docs/plans/shots/oui/expected.png'), 'utf8'), 'historical image');
    await writeFile(join(dirname(path), 'late.png'), 'extra generated image');
    await assert.rejects(checkShotsIndex(), /late\.png/);
    await rm(join(dirname(path), 'late.png'));
    await rm(path);
    await assert.rejects(checkShotsIndex(), /expected\.png/);
    const init = spawnSync('git', ['init', '--quiet'], { cwd: root, encoding: 'utf8' });
    assert.equal(init.status, 0, init.stderr);
    const ignored = spawnSync('git', ['check-ignore', '--', '.scratch/evidence/ui/expected.png'], { cwd: root, encoding: 'utf8' });
    assert.equal(ignored.status, 0, ignored.stderr);
  } finally {
    if (previous === undefined) delete process.env.SHOTS_DIR;
    else process.env.SHOTS_DIR = previous;
  }
});

test('relative SHOTS_DIR is shared by captures and the index from another working directory', async t => {
  const root = await fixture(t);
  const code = `import { shotPath } from ${JSON.stringify(pathToFileURL(join(root, 'scripts/ui/lib.mjs')).href)};
    import { checkShotsIndex } from ${JSON.stringify(pathToFileURL(join(root, 'scripts/ui/shots-index.mjs')).href)};
    import { writeFile } from 'node:fs/promises';
    const path = await shotPath('expected'); await writeFile(path, 'generated');
    console.log(JSON.stringify({ path, count: await checkShotsIndex() }));`;
  const result = spawnSync(process.execPath, ['--input-type=module', '-e', code], {
    cwd: join(root, 'docs'), encoding: 'utf8', env: { ...process.env, SHOTS_DIR: '.scratch/evidence/custom' },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), { path: join(root, '.scratch/evidence/custom/expected.png'), count: 1 });
});

test('generated evidence media is ignored while receipts and application assets stay visible', async t => {
  const root = await fixture(t);
  const init = spawnSync('git', ['init', '--quiet'], { cwd: root, encoding: 'utf8' });
  assert.equal(init.status, 0, init.stderr);
  for (const path of ['.scratch/evidence-publish/run/manifest.json', '.scratch/evidence-other/run/gallery.html',
    '.scratch/mine/clip.MKV', 'docs/plans/nested/capture.PNG', 'docs/prototypes/clip.MP4', 'docs/design/clip.WeBm']) {
    const result = spawnSync('git', ['check-ignore', '--', path], { cwd: root, encoding: 'utf8' });
    assert.equal(result.status, 0, `${path}: ${result.stderr}`);
  }
  for (const path of ['docs/plans/receipt.md', 'docs/design/manifest.json', 'internal/ui/static/icon.png', 'testdata/fixture.mp4']) {
    assert.equal(spawnSync('git', ['check-ignore', '--', path], { cwd: root }).status, 1, path);
  }
});
