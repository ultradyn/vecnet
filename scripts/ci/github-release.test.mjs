import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const script = fileURLToPath(new URL('./github-release.sh', import.meta.url));
const linux = { skip: process.platform !== 'linux' && 'GitHub Release shell runs on Linux' };

async function release(t, { state = 'pending', exists = false, draft = true, npmTag = 'latest' } = {}) {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'vecnet-github-release-'));
  t.after(() => rm(cwd, { recursive: true, force: true }));
  const bin = path.join(cwd, 'bin');
  await mkdir(bin);
  await writeFile(path.join(bin, 'gh'), `#!/usr/bin/env node
const fs = require('node:fs');
const args = process.argv.slice(2);
fs.appendFileSync(process.env.CALLS, JSON.stringify(args) + '\\n');
if (args[1] === 'view') {
  if (process.env.EXISTS !== 'true') process.exit(1);
  console.log(process.env.DRAFT);
}
`, { mode: 0o755 });
  const result = spawnSync('bash', [script], {
    cwd, encoding: 'utf8', timeout: 5000,
    env: { ...process.env, HOME: cwd, XDG_CONFIG_HOME: cwd, XDG_CACHE_HOME: cwd, XDG_DATA_HOME: cwd,
      XDG_STATE_HOME: cwd, XDG_RUNTIME_DIR: cwd, PATH: `${bin}${path.delimiter}${process.env.PATH}`,
      TAG: 'v0.1.1', TARBALL: 'vecnet-0.1.1.tgz', NPM_TAG: npmTag, NPM_STATE: state,
      EXISTS: String(exists), DRAFT: String(draft), CALLS: path.join(cwd, 'calls') },
  });
  let calls = [];
  try { calls = (await readFile(path.join(cwd, 'calls'), 'utf8')).trim().split('\n').map(JSON.parse); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  return { result, calls };
}

test('pending npm upload creates a clearly described draft with tested assets', linux, async t => {
  const { result, calls } = await release(t);
  assert.equal(result.status, 0, result.stderr);
  const create = calls.find(args => args[1] === 'create');
  assert.ok(create?.includes('--draft'), 'pending npm upload must create a draft');
  assert.ok(create.includes('dist/vecnet-0.1.1.tgz') && create.includes('dist/SHA256SUMS'));
  const notes = create[create.indexOf('--notes') + 1];
  assert.match(notes, /not yet verified visible/i);
  assert.match(notes, /do not (?:reupload|republish)/i);
});

test('verified npm upload creates a normal release and preserves prerelease selection', linux, async t => {
  for (const npmTag of ['latest', 'next']) {
    const { result, calls } = await release(t, { state: 'verified', npmTag });
    assert.equal(result.status, 0, result.stderr);
    const create = calls.find(args => args[1] === 'create');
    assert.ok(create && !create.includes('--draft'));
    assert.equal(create.includes('--prerelease'), npmTag === 'next');
  }
});

test('pending recovery keeps an existing draft explicit and refreshes assets', linux, async t => {
  const { result, calls } = await release(t, { exists: true });
  assert.equal(result.status, 0, result.stderr);
  assert.ok(calls.some(args => args[1] === 'upload' && args.includes('--clobber')));
  const edit = calls.find(args => args[1] === 'edit');
  assert.ok(edit?.includes('--draft'));
  assert.match(edit[edit.indexOf('--notes') + 1], /not yet verified visible/i);
});

test('verified recovery leaves finalizing an existing draft to the controller', linux, async t => {
  for (const draft of [true, false]) {
    const { result, calls } = await release(t, { state: 'verified', exists: true, draft });
    assert.equal(result.status, 0, result.stderr);
    assert.ok(calls.some(args => args[1] === 'upload'));
    assert.ok(!calls.some(args => args[1] === 'create' || args[1] === 'edit'));
  }
});

test('pending state cannot overwrite an existing public release', linux, async t => {
  const { result, calls } = await release(t, { exists: true, draft: false });
  assert.notEqual(result.status, 0);
  assert.ok(calls.every(args => args[1] === 'view'));
});

test('missing or unknown npm state fails before touching GitHub', linux, async t => {
  for (const state of ['', 'success']) {
    const { result, calls } = await release(t, { state });
    assert.notEqual(result.status, 0);
    assert.deepEqual(calls, []);
  }
});
