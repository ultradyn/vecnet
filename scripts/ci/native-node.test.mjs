import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { selectNativeNode } from './native-node.mjs';

const entry = fileURLToPath(new URL('./native-node.mjs', import.meta.url));
const linuxOnly = { skip: process.platform !== 'linux' && 'requires executable POSIX fixtures' };
const originalPath = 'existing-path-entry\n';

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'native node & path '));
  t.after(() => rm(root, { recursive: true, force: true }));
  const cache = path.join(root, 'tool cache');
  const githubPath = path.join(root, 'github path');
  await writeFile(githubPath, originalPath);
  return { root, cache, githubPath };
}

async function executableFixture(cache, slot, identity, platform = 'linux') {
  const dir = path.join(cache, 'node', slot);
  const bin = platform === 'win32' ? dir : path.join(dir, 'bin');
  const executable = path.join(bin, platform === 'win32' ? 'node.exe' : 'node');
  await mkdir(bin, { recursive: true });
  if (identity) {
    // The real child reports a fixed identity; this does not measure Node 24.
    await writeFile(executable, `#!/bin/sh\n[ "$1" = -p ] || exit 7\nprintf '%s\\n' '${JSON.stringify(identity)}'\n`, { mode: 0o755 });
  }
  return { bin, executable };
}

function cli(env) {
  return spawnSync(process.execPath, [entry], { env, encoding: 'utf8', timeout: 15_000 });
}

test('CLI publishes highest available canonical Node 24 without invoking npm or yarn', linuxOnly, async t => {
  const { root, cache, githubPath } = await fixture(t);
  const arch = 'x64';
  const { bin } = await executableFixture(cache, '24.10.10/x64', { version: 'v24.10.10', arch, platform: 'linux' });
  for (const slot of ['24.9.99/x64', '24.10.9/x64', '24.010.11/x64', '24.10.011/x64', '24.88.1-rc.1/x64', '25.0.0/x64']) {
    await executableFixture(cache, slot, { version: 'v22.23.3', arch, platform: 'linux' });
  }
  // The newer canonical version exists, but only for the other architecture.
  await executableFixture(cache, '24.11.0/arm64');
  const invocations = path.join(root, 'unexpected invocation');
  for (const dir of [root, bin]) {
    for (const tool of ['npm', 'yarn']) {
      await writeFile(path.join(dir, tool), '#!/bin/sh\nprintf "%s\\n" "$0" >> "$UNEXPECTED_INVOCATION"\nexit 8\n', { mode: 0o755 });
    }
  }
  const result = cli({ RUNNER_TOOL_CACHE: cache, RUNNER_ARCH: 'X64', GITHUB_PATH: githubPath,
    PATH: `${root}${path.delimiter}${bin}`, UNEXPECTED_INVOCATION: invocations });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(githubPath, 'utf8'), `${originalPath}${bin}\n`);
  await assert.rejects(readFile(invocations), { code: 'ENOENT' });
});

test('selects the Windows node.exe layout without a bin segment', linuxOnly, async t => {
  const context = await fixture(t);
  // A POSIX executable under the Windows filename tests layout, not Windows execution.
  const { bin, executable } = await executableFixture(context.cache, '24.10.2/arm64',
    { version: 'v24.10.2', arch: 'arm64', platform: 'win32' }, 'win32');
  assert.equal(await selectNativeNode({ ...context, arch: 'arm64', platform: 'win32' }), executable);
  assert.equal(await readFile(context.githubPath, 'utf8'), `${originalPath}${bin}\n`);
});

for (const [field, identity] of [
  ['architecture', { version: 'v24.10.2', arch: 'arm64', platform: 'linux' }],
  ['version', { version: 'v24.10.1', arch: 'x64', platform: 'linux' }],
  ['platform', { version: 'v24.10.2', arch: 'x64', platform: 'win32' }],
]) {
  test(`rejects the wrong ${field} without publishing PATH or falling back`, linuxOnly, async t => {
    const context = await fixture(t);
    await executableFixture(context.cache, '24.9.0/x64', { version: 'v24.9.0', arch: 'x64', platform: 'linux' });
    await executableFixture(context.cache, '24.10.2/x64', identity);
    await assert.rejects(selectNativeNode({ ...context, arch: 'x64', platform: 'linux' }), /identity mismatch/);
    assert.equal(await readFile(context.githubPath, 'utf8'), originalPath);
  });
}

test('probes an actual Node binary and rejects it under a different version', linuxOnly, async t => {
  const context = await fixture(t);
  const slot = process.version === 'v24.0.1' ? '24.0.0' : '24.0.1';
  const { executable } = await executableFixture(context.cache, `${slot}/${process.arch}`);
  await symlink(process.execPath, executable);
  await assert.rejects(selectNativeNode({ ...context, arch: process.arch, platform: 'linux' }), /identity mismatch/);
  assert.equal(await readFile(context.githubPath, 'utf8'), originalPath);
});

test('fails closed for an absent cache and for a cache with no Node 24', async t => {
  const context = await fixture(t);
  const input = { ...context, arch: 'x64', platform: 'linux' };
  await assert.rejects(selectNativeNode(input), /native Node 24 missing from hosted cache/);
  await mkdir(path.join(context.cache, 'node', '22.23.3', 'x64'), { recursive: true });
  await assert.rejects(selectNativeNode(input), /native Node 24 missing from hosted cache/);
  assert.equal(await readFile(context.githubPath, 'utf8'), originalPath);
});

test('CLI fails closed when runner context is missing', async t => {
  const { cache, githubPath } = await fixture(t);
  const env = { ...process.env, RUNNER_TOOL_CACHE: cache, RUNNER_ARCH: 'X64', GITHUB_PATH: githubPath };
  for (const key of ['RUNNER_TOOL_CACHE', 'RUNNER_ARCH', 'GITHUB_PATH']) {
    const missing = { ...env };
    delete missing[key];
    const result = cli(missing);
    assert.ifError(result.error);
    assert.equal(result.status, 1, key);
    assert.match(result.stderr, /native Node cache context required/);
  }
  assert.equal(await readFile(githubPath, 'utf8'), originalPath);
});
