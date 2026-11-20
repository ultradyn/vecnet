import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { ownerFixture } from './windows-owner.test-fixture.mjs';
import { stageGoOwner } from './windows-owner.mjs';

for (const platform of ['win32-x64', 'win32-arm64']) {
  test(`stages ${platform} owner from the real downloaded Go bundle layout`, async t => {
    const root = await mkdtemp(path.join(os.tmpdir(), 'go owner download '));
    t.after(() => rm(root, {recursive: true, force: true}));
    const directory = path.join(root, 'dist', 'go-tests', platform);
    await mkdir(directory, {recursive: true});
    const sha = 'a'.repeat(40);
    const receipt = {revision: sha, platform, ...await ownerFixture(directory, platform)};
    const manifest = path.join(directory, 'bundle.json');
    await writeFile(manifest, JSON.stringify(receipt));
    await assert.rejects(readFile(path.join(root, 'dist/go-tests/bundle.json')), {code: 'ENOENT'});
    const envFile = path.join(root, 'environment');
    const previous = process.env.GITHUB_ENV;
    process.env.GITHUB_ENV = envFile;
    t.after(() => {
      if (previous === undefined) delete process.env.GITHUB_ENV;
      else process.env.GITHUB_ENV = previous;
    });
    await stageGoOwner({root, sha, platform});
    const exported = await readFile(envFile, 'utf8');
    assert.match(exported, /^CI_WINDOWS_OWNER=.+\n$/);
    const staged = exported.slice('CI_WINDOWS_OWNER='.length).trimEnd();
    t.after(() => rm(path.dirname(staged), {recursive: true, force: true}));
    assert.ok(path.isAbsolute(staged));
    assert.equal(path.basename(staged), 'ci-windows-owner.exe');
    assert.deepEqual(await readFile(staged), await readFile(path.join(directory, receipt.ciWindowsOwner.file)));

    for (const [name, changed, expected] of [
      ['source', {...receipt, revision: 'b'.repeat(40)}, /source mismatch/],
      ['hash', {...receipt, ciWindowsOwner: {...receipt.ciWindowsOwner, sha256: '0'.repeat(64)}}, /hash mismatch/],
      ['architecture', {...receipt, platform: platform === 'win32-x64' ? 'win32-arm64' : 'win32-x64'}, /metadata/],
    ]) {
      await writeFile(manifest, JSON.stringify(changed));
      await assert.rejects(stageGoOwner({root, sha, platform}), expected, name);
      assert.equal(await readFile(envFile, 'utf8'), exported, `${name} refusal must not publish another owner`);
    }

    const other = platform === 'win32-x64' ? 'win32-arm64' : 'win32-x64';
    const mismatched = await ownerFixture(directory, other);
    await writeFile(manifest, JSON.stringify({...receipt, ...mismatched}));
    await assert.rejects(stageGoOwner({root, sha, platform}), /PE architecture mismatch/);
    assert.equal(await readFile(envFile, 'utf8'), exported);

    if (process.platform === 'win32' && platform === `win32-${process.arch}`) {
      const native = await ownerFixture(directory, platform);
      await writeFile(manifest, JSON.stringify({...receipt, ...native}));
      const result = spawnSync(process.execPath, [fileURLToPath(new URL('./windows-owner.mjs', import.meta.url)), 'use-go'], {
        cwd: root, env: {...process.env, GITHUB_SHA: sha}, encoding: 'utf8', timeout: 10_000, windowsHide: true,
      });
      const paths = (await readFile(envFile, 'utf8')).trimEnd().split('\n').map(line => line.slice('CI_WINDOWS_OWNER='.length));
      for (const file of paths.slice(1)) t.after(() => rm(path.dirname(file), {recursive: true, force: true}));
      assert.equal(result.status, 0, result.stderr || result.error?.message);
      assert.equal(paths.length, 2, 'actual CLI exports one staged owner');
      assert.deepEqual(await readFile(paths[1]), await readFile(path.join(directory, native.ciWindowsOwner.file)));
    }
  });
}
