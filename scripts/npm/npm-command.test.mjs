import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { npmInvocation } from './npm-command.mjs';

test('Unix npm stays shell-free and preserves the argument vector', () => {
  const args = ['install', '--prefix', '/tmp/prefix with spaces & symbols', 'package.tgz'];
  assert.deepEqual(npmInvocation(args, { platform: 'linux' }), { file: 'npm', args });
});

test('Windows invokes npm-cli.js without npm.cmd or a shell, even for hostile arguments', async t => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'vecnet npm command '));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const entry = path.join(temporary, 'npm cli.cjs');
  await writeFile(entry, 'console.log(JSON.stringify(process.argv.slice(2)));\n');
  const args = ['install', '--prefix', path.join(temporary, 'prefix & echo injected'),
    'a b', '" & echo injected', '%COMSPEC%', '!PATH!', '$(echo injected)', 'a|b', 'a^b', 'a>b', 'a;b'];
  const invocation = npmInvocation(args, { platform: 'win32', env: { npm_execpath: entry } });
  assert.equal(invocation.file, process.execPath);
  assert.deepEqual(invocation.args, [entry, ...args]);
  assert.equal(invocation.shell, undefined);
  const result = spawnSync(invocation.file, invocation.args, { encoding: 'utf8', windowsHide: true });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), args, 'arguments are data, not shell syntax');
});

test('Windows resolves the npm JS entry shipped with Node', { skip: process.platform !== 'win32' }, () => {
  const invocation = npmInvocation(['--version'], { env: { PATH: process.env.PATH } });
  assert.equal(invocation.file, process.execPath);
  assert.match(invocation.args[0], /npm-cli\.js$/);
  const result = spawnSync(invocation.file, invocation.args, { encoding: 'utf8', windowsHide: true });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /^\d+\.\d+\.\d+/);
});

test('Windows refuses a batch entry instead of silently enabling a shell', () => {
  assert.throws(() => npmInvocation(['--version'], {
    platform: 'win32', execPath: path.join(os.tmpdir(), 'no-node-here', 'node.exe'),
    env: { PATH: '', npm_execpath: 'npm.cmd' },
  }), /Cannot find npm-cli\.js/);
});

test('Windows npm resolution honors a pinned PATH version before the Node fallback', async t => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'vecnet npm path '));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const pinned = path.join(temporary, 'pinned npm', 'node_modules', 'npm', 'bin', 'npm-cli.js');
  const bundled = path.join(temporary, 'node', 'node_modules', 'npm', 'bin', 'npm-cli.js');
  for (const entry of [pinned, bundled]) {
    await mkdir(path.dirname(entry), { recursive: true });
    await writeFile(entry, '// fixture\n');
  }
  const invocation = npmInvocation(['--version'], {
    platform: 'win32', execPath: path.join(temporary, 'node', 'node.exe'),
    env: { pAtH: path.join(temporary, 'pinned npm') },
  });
  assert.equal(invocation.args[0], pinned);
});
