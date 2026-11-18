import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { npmInvocation } from '../npm/npm-command.mjs';

const helper = fileURLToPath(new URL('./npm-tool.mjs', import.meta.url));
const producer = { skip: process.platform !== 'linux' && 'Linux build stages the global npm' };

async function fixture(t) {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'npm tool & '));
  t.after(() => rm(cwd, { recursive: true, force: true }));
  const envFile = path.join(cwd, 'github-env');
  const env = { ...process.env, TMPDIR: cwd, GITHUB_ENV: envFile };
  const run = (mode, version, extraEnv = {}) => spawnSync(process.execPath, [helper, mode, version], {
    cwd, env: { ...env, ...extraEnv }, encoding: 'utf8', windowsHide: true,
  });
  await writeFile(envFile, '');
  return { cwd, env, envFile, run };
}

test('a staged global npm still runs when relocated and wins Windows npm resolution', producer, async t => {
  const { cwd, env, envFile, run } = await fixture(t);
  const options = { cwd, env, encoding: 'utf8' };
  const version = execFileSync('npm', ['--version'], options).trim();
  const source = path.join(execFileSync('npm', ['root', '--global'], options).trim(), 'npm', 'bin', 'npm-cli.js');
  const staged = run('stage', version);
  assert.equal(staged.status, 0, staged.stderr);
  assert.ok((await stat(path.join(cwd, 'dist/npm-tool/npm.tgz'))).isFile());
  const used = run('use', version);
  assert.equal(used.status, 0, used.stderr);
  assert.equal(used.stdout.trim(), `Using staged npm ${version}`);
  const cli = /^npm_execpath=(.+)\r?\n$/.exec(await readFile(envFile, 'utf8'))?.[1];
  assert.ok(cli && path.isAbsolute(cli));
  assert.notEqual(cli, source);
  assert.ok((await readFile(cli)).equals(await readFile(source)), 'the installed CLI bytes are preserved');
  const other = path.join(cwd, 'other', 'node_modules', 'npm', 'bin', 'npm-cli.js');
  await mkdir(path.dirname(other), { recursive: true });
  await writeFile(other, 'process.exit(91);\n');
  const invocation = npmInvocation(['--version'], {
    platform: 'win32', env: { PATH: path.join(cwd, 'other'), npm_execpath: cli },
  });
  assert.equal(invocation.args[0], cli);
  assert.equal(execFileSync(invocation.file, invocation.args, options).trim(), version);
});

test('stage rejects a different installed npm version without creating an archive', producer, async t => {
  const { cwd, run } = await fixture(t);
  const result = run('stage', '0.0.0');
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /expected npm 0\.0\.0/i);
  await assert.rejects(stat(path.join(cwd, 'dist/npm-tool/npm.tgz')), { code: 'ENOENT' });
});

test('stage rejects a matching manifest without npm-cli.js', producer, async t => {
  const { cwd, run } = await fixture(t);
  const prefix = path.join(cwd, 'empty global prefix');
  const source = path.join(prefix, 'lib', 'node_modules', 'npm');
  await mkdir(source, { recursive: true });
  await writeFile(path.join(source, 'package.json'), '{"name":"npm","version":"12.0.1"}\n');
  const result = run('stage', '12.0.1', { npm_config_prefix: prefix });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /npm-cli\.js/);
  await assert.rejects(stat(path.join(cwd, 'dist/npm-tool/npm.tgz')), { code: 'ENOENT' });
});

test('use rejects an npm CLI reporting the wrong version before exporting npm_execpath', async t => {
  const { cwd, envFile, run } = await fixture(t);
  const fake = path.join(cwd, 'bad npm');
  await mkdir(path.join(fake, 'bin'), { recursive: true });
  await writeFile(path.join(fake, 'package.json'), '{"name":"npm","version":"12.0.1"}\n');
  await writeFile(path.join(fake, 'bin', 'npm-cli.js'), 'console.log("12.0.0");\n');
  const archive = path.join(cwd, 'dist/npm-tool/npm.tgz');
  await mkdir(path.dirname(archive), { recursive: true });
  execFileSync('tar', ['-czf', archive, '-C', fake, '.']);
  const result = run('use', '12.0.1');
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /expected npm 12\.0\.1.*12\.0\.0/i);
  assert.equal(await readFile(envFile, 'utf8'), '');
});

test('use cannot fall back to a global npm when the artifact is missing', async t => {
  const { envFile, run } = await fixture(t);
  const result = run('use', '12.0.1');
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /npm-tool[/\\]npm\.tgz/);
  assert.equal(await readFile(envFile, 'utf8'), '');
});
