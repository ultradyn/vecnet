import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm, copyFile, readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const cli = fileURLToPath(new URL('./check.mjs', import.meta.url));
const scratch = fileURLToPath(new URL('../../.superpowers/sdd/2026-11-16-public-evidence/scratch/', import.meta.url));
const gitEnv = { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' };
for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_COMMON_DIR']) delete gitEnv[key];

async function repository(t, { initialCommit = true, name = '.' } = {}) {
  await mkdir(scratch, { recursive: true });
  const parent = await mkdtemp(join(scratch, 'vecnet-evidence-guard-'));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const cwd = join(parent, name);
  await mkdir(cwd, { recursive: true });
  const git = (...args) => {
    const result = spawnSync('git', ['-c', 'user.name=Evidence Test', '-c', 'user.email=evidence@example.invalid',
      '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', ...args], { cwd, env: gitEnv, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    return result.stdout.trim();
  };
  git('init', '--quiet');
  const put = async (path, content = 'synthetic media') => {
    await mkdir(dirname(join(cwd, path)), { recursive: true });
    await writeFile(join(cwd, path), content);
  };
  const commit = () => { git('add', '--all'); git('commit', '--quiet', '--allow-empty', '-m', 'test fixture'); return git('rev-parse', 'HEAD'); };
  if (initialCommit) { await put('README.md', 'initial'); commit(); }
  const check = (args = [], directory = cwd) => spawnSync(process.execPath, [cli, ...args], { cwd: directory, env: gitEnv, encoding: 'utf8' });
  return { cwd, git, put, commit, check };
}

function allowed(result) { assert.equal(result.status, 0, result.stderr); }
function blocked(result, paths) {
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /evidence media/i);
  for (const path of paths) assert.ok(result.stderr.includes(JSON.stringify(path)), `missing rejected path ${JSON.stringify(path)}: ${result.stderr}`);
}

test('evidence guard handles additions changes deletions and application assets', async t => {
  const repo = await repository(t);
  await repo.put('docs/plans/historical.png');
  await repo.put('docs/design/deleted.webm');
  const base = repo.commit();
  await repo.put('README.md', 'unrelated change');
  await repo.put('internal/ui/static/icon.png');
  await repo.put('testdata/fixture.mp4');
  await repo.put('docs/plans/receipt.md', 'public receipt');
  repo.git('rm', 'docs/design/deleted.webm');
  repo.git('add', '--all');
  allowed(repo.check());
  repo.commit();
  allowed(repo.check(['--base', base]));
  await repo.put('docs/plans/historical.png', 'modified evidence');
  await repo.put('docs/prototypes/new.MP4');
  repo.git('add', '--all');
  blocked(repo.check(), ['docs/plans/historical.png', 'docs/prototypes/new.MP4']);
  repo.commit();
  blocked(repo.check(['--base', base]), ['docs/plans/historical.png', 'docs/prototypes/new.MP4']);
});

test('guard covers every evidence root and media extension case-insensitively', async t => {
  const repo = await repository(t);
  const roots = ['.scratch', 'docs/plans', 'docs/prototypes', 'docs/design'];
  const extensions = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'mp4', 'webm', 'mov', 'avi', 'mkv'];
  const paths = [];
  for (const root of roots) for (const extension of extensions) {
    const path = `${root}/nested/capture.${extension.toUpperCase()}`;
    paths.push(path);
    await repo.put(path);
  }
  repo.git('add', '--all');
  blocked(repo.check(), paths);
});

test('default guard checks only staged changes, including the staged version of a path', async t => {
  const repo = await repository(t);
  await repo.put('docs/plans/unstaged.png');
  allowed(repo.check());
  repo.git('add', '--all');
  await rm(join(repo.cwd, 'docs/plans/unstaged.png'));
  blocked(repo.check(), ['docs/plans/unstaged.png']);
});

test('base comparison checks HEAD rather than staged or untracked changes', async t => {
  const repo = await repository(t);
  const base = repo.git('rev-parse', 'HEAD');
  await repo.put('docs/design/staged.gif');
  repo.git('add', '--all');
  allowed(repo.check(['--base', base]));
  repo.commit();
  blocked(repo.check(['--base', base]), ['docs/design/staged.gif']);
  allowed(repo.check());
});

test('renames are checked at their destination, including changed and unusual names', async t => {
  const repo = await repository(t);
  await repo.put('internal/ui/static/source.png');
  await repo.put('docs/plans/outbound.png');
  const base = repo.commit();
  const destination = 'docs/plans/nested/space tab\tline\nbreak.PNG';
  await mkdir(dirname(join(repo.cwd, destination)), { recursive: true });
  repo.git('mv', 'internal/ui/static/source.png', destination);
  repo.git('mv', 'docs/plans/outbound.png', 'internal/ui/static/archived.png');
  blocked(repo.check(), [destination]);
  await repo.put(destination, 'changed after rename');
  repo.git('add', '--all');
  blocked(repo.check(), [destination]);
  repo.commit();
  blocked(repo.check(['--base', base]), [destination]);
  repo.git('mv', destination, 'internal/ui/static/returned.png');
  allowed(repo.check());
  repo.commit();
  blocked(repo.check(['--base', base]), [destination]);
});

test('guard handles unborn repositories and reads root paths from a subdirectory', async t => {
  const repo = await repository(t, { initialCommit: false });
  await repo.put('docs/design/fresh.png');
  repo.git('add', '--all');
  blocked(repo.check([], join(repo.cwd, 'docs/design')), ['docs/design/fresh.png']);
});

for (const suffix of [' ', '\t', '\r', '\n']) {
  test(`guard preserves root suffix ${JSON.stringify(suffix)} instead of checking a clean sibling`, async t => {
    const repo = await repository(t, { initialCommit: false, name: `repo${suffix}` });
    const sibling = join(dirname(repo.cwd), 'repo');
    await mkdir(sibling);
    repo.git('-C', sibling, 'init', '--quiet');
    repo.git('-C', sibling, 'commit', '--quiet', '--allow-empty', '-m', 'clean sibling');
    assert.equal(repo.git('-C', sibling, 'status', '--porcelain'), '');
    allowed(repo.check([], sibling));
    await repo.put('docs/plans/new.PNG');
    repo.git('add', '--all');
    // check() invokes the actual CLI by absolute path, regardless of cwd.
    for (const cwd of [repo.cwd, join(repo.cwd, 'docs/plans')]) {
      blocked(repo.check([], cwd), ['docs/plans/new.PNG']);
    }
  });
}

test('initial zero and empty-tree bases check all introduced history (Plan 4 final I2)', async t => {
  const repo = await repository(t);
  await repo.put('docs/plans/initial.png');
  repo.commit();
  blocked(repo.check(['--base', '0'.repeat(40)]), ['docs/plans/initial.png']);
  repo.git('rm', 'docs/plans/initial.png');
  repo.commit();
  const empty = repo.git('hash-object', '-t', 'tree', '/dev/null');
  for (const base of ['0'.repeat(40), empty]) blocked(repo.check(['--base', base]), ['docs/plans/initial.png']);
});

test('history rejects add/delete while staged mode stays local (Plan 4 final I2)', async t => {
  const repo = await repository(t);
  await repo.put('docs/plans/historical.png', 'grandfathered');
  const base = repo.commit();
  await repo.put('docs/plans/transient.PNG');
  repo.commit();
  repo.git('rm', 'docs/plans/transient.PNG');
  repo.commit();
  allowed(repo.check());
  blocked(repo.check(['--base', base]), ['docs/plans/transient.PNG']);
});

test('history rejects modified then restored grandfathered media (Plan 4 final I2)', async t => {
  const repo = await repository(t);
  await repo.put('docs/plans/historical.png', 'grandfathered');
  const beforeModify = repo.commit();
  await repo.put('docs/plans/historical.png', 'changed');
  repo.commit();
  await repo.put('docs/plans/historical.png', 'grandfathered');
  repo.commit();
  blocked(repo.check(['--base', beforeModify]), ['docs/plans/historical.png']);
  allowed(repo.check());
});

test('history examines merged side commits and merge-created evidence (Plan 4 final I2)', async t => {
  const repo = await repository(t);
  const base = repo.git('rev-parse', 'HEAD');
  const cleanTree = repo.git('rev-parse', 'HEAD^{tree}');
  await repo.put('docs/design/side.png');
  const sideAdd = repo.commit();
  const sideDelete = repo.git('commit-tree', cleanTree, '-p', sideAdd, '-m', 'side deletion');
  const main = repo.git('commit-tree', cleanTree, '-p', base, '-m', 'main');
  const merge = repo.git('commit-tree', cleanTree, '-p', main, '-p', sideDelete, '-m', 'merge');
  repo.git('update-ref', 'HEAD', merge);
  blocked(repo.check(['--base', base]), ['docs/design/side.png']);
  await repo.put('docs/design/merge.png');
  repo.git('add', '--all');
  const tree = repo.git('write-tree');
  const novelMerge = repo.git('commit-tree', tree, '-p', main, '-p', sideDelete, '-m', 'merge creates media');
  repo.git('update-ref', 'HEAD', novelMerge);
  blocked(repo.check(['--base', base]), ['docs/design/merge.png', 'docs/design/side.png']);
});

test('merge preserves unchanged grandfathered media present on only one parent (Plan 4 final I2)', async t => {
  const repo = await repository(t);
  const ancestor = repo.git('rev-parse', 'HEAD');
  const cleanTree = repo.git('rev-parse', 'HEAD^{tree}');
  await repo.put('docs/plans/grandfathered.png');
  const base = repo.commit();
  const oldTree = repo.git('rev-parse', 'HEAD^{tree}');
  const side = repo.git('commit-tree', cleanTree, '-p', ancestor, '-m', 'old side');
  const merge = repo.git('commit-tree', oldTree, '-p', base, '-p', side, '-m', 'keep historical image');
  repo.git('update-ref', 'HEAD', merge);
  allowed(repo.check(['--base', base]));
  repo.git('rm', 'docs/plans/grandfathered.png');
  repo.commit();
  allowed(repo.check(['--base', base]));
});

test('invalid arguments and missing base history fail closed', async t => {
  const repo = await repository(t);
  for (const args of [['--base'], ['--unknown'], ['--base', 'missing-history'], ['--base', '--all'], ['--base', 'HEAD', 'extra']]) {
    const result = repo.check(args);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /usage|base|git/i);
    assert.doesNotMatch(result.stderr, /MODULE_NOT_FOUND/);
  }
  const tree = repo.git('rev-parse', 'HEAD^{tree}');
  assert.notEqual(repo.check(['--base', tree]).status, 0, 'nonempty trees cannot delimit commit history');
});

test('CI uses event base SHAs and a read-only full-history checkout', async t => {
  const workflow = await readFile(new URL('../../.github/workflows/evidence.yml', import.meta.url), 'utf8');
  assert.match(workflow, /pull_request:/);
  assert.match(workflow, /push:\s*\n\s+branches: \[master\]/);
  assert.doesNotMatch(workflow, /pull_request_target|secrets\./);
  assert.match(workflow, /contents: read/);
  assert.match(workflow, /actions\/checkout@v5/);
  assert.match(workflow, /fetch-depth: 0/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /node-version: ['"]?22/);
  assert.match(workflow, /PR_BASE_SHA: \$\{\{ github.event.pull_request.base.sha \}\}/);
  assert.match(workflow, /PUSH_BEFORE_SHA: \$\{\{ github.event.before \}\}/);
  const body = workflow.match(/run: \|\n((?: {10}.*\n|\n)+)/)?.[1];
  assert.ok(body, 'base selection step is executable');
  const script = body.replace(/^ {10}/gm, '');
  const repo = await repository(t);
  await mkdir(join(repo.cwd, 'scripts/evidence'), { recursive: true });
  await copyFile(cli, join(repo.cwd, 'scripts/evidence/check.mjs'));
  const prBase = repo.commit();
  await repo.put('docs/plans/branch.png');
  const pushBase = repo.commit();
  await repo.put('README.md', 'unrelated push');
  repo.commit();
  const run = (event, before) => spawnSync('bash', ['-euo', 'pipefail', '-c', script], {
    cwd: repo.cwd, encoding: 'utf8',
    env: { ...gitEnv, GITHUB_EVENT_NAME: event, PR_BASE_SHA: prBase, PUSH_BEFORE_SHA: before },
  });
  blocked(run('pull_request', pushBase), ['docs/plans/branch.png']);
  allowed(run('push', pushBase));
  blocked(run('push', '0'.repeat(40)), ['docs/plans/branch.png']);
});
