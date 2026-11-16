import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { cliEnvironment, commandInvocation, envValue, terminateProcess, userHome } from '../src/platform.ts';

function directory(t: { after(fn: () => void): void }) {
  const dir = mkdtempSync(join(tmpdir(), 'vecnet-platform-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}
test('HOME/USERPROFILE match the native platform and Windows env lookup is case insensitive', () => {
  assert.equal(userHome({ HOME: '/unix/home', USERPROFILE: 'C:\\Users\\test' }, 'linux'), '/unix/home');
  assert.equal(userHome({ HOME: 'C:\\old', UserProfile: 'C:\\Users\\test' }, 'win32'), 'C:\\old');
  assert.equal(userHome({ UserProfile: 'C:\\Users\\test' }, 'win32'), 'C:\\Users\\test');
  assert.equal(userHome({ HOME: 'C:\\fallback' }, 'win32'), 'C:\\fallback');
  assert.equal(userHome({ HOME: 'relative' }, 'win32'), undefined);
  assert.equal(userHome({}, 'linux'), undefined);
  assert.equal(envValue({ Path: 'test' }, 'PATH', 'win32'), 'test');
  assert.equal(envValue({ Path: 'test' }, 'PATH', 'linux'), undefined);
});
test('CLI environment retains Windows loader/user paths but never auth or session variables', () => {
  const env = cliEnvironment({ SystemRoot: 'C:\\Windows', USERPROFILE: 'C:\\Users\\test', HOME: '/home/test', PATH: '/bin',
    VECNET_CONFIG: '/config', VECNET_TOKEN: 'secret', CODEX_HOME: '/different', OPENAI_API_KEY: 'secret', UNRELATED: 'secret' });
  assert.equal(env.SystemRoot, 'C:\\Windows'); assert.equal(env.USERPROFILE, 'C:\\Users\\test');
  for (const key of ['VECNET_TOKEN', 'OPENAI_API_KEY', 'CODEX_HOME', 'UNRELATED']) assert.equal(env[key], undefined);
});
test('native argv stays data and preserves cwd, UTF-8, metacharacters and empty arguments', t => {
  const dir = directory(t), script = join(dir, 'echo.mjs');
  writeFileSync(script, 'process.stdout.write(JSON.stringify({argv:process.argv.slice(2),cwd:process.cwd()}));');
  const args = ['', 'space here', '🌊', '"quoted"', 'a&b|c>file', '%PATH%', '!bang!', 'C:\\trailing\\'];
  const command = commandInvocation([process.execPath, script, ...args]);
  const result = spawnSync(command.file, command.args, { ...command, cwd: dir, encoding: 'utf8', windowsHide: true });
  assert.equal(result.status, 0, result.stderr); assert.deepEqual(JSON.parse(result.stdout), { argv: args, cwd: dir });
});
test('Windows PATH executable suffix and npm cmd shim preserve argv without shell injection', { skip: process.platform !== 'win32' }, t => {
  const dir = directory(t), bin = join(dir, 'space bin'); mkdirSync(bin);
  const script = join(dir, 'argv.mjs'), marker = join(dir, 'must-not-exist');
  writeFileSync(script, 'process.stdout.write(JSON.stringify(process.argv.slice(2)));');
  writeFileSync(join(bin, 'fixture.CMD'), `@echo off\r\n"${process.execPath}" "${script}" %*\r\n`);
  const env = { ...process.env, PATH: bin + ';' + dirname(process.execPath) };
  const args = ['', 'hello world', '🌊', '"quoted"', 'foo&bar', '%PATH%', '!test!', `& echo owned > "${marker}"`, '{"json":"x"}', 'C:\\tail\\'];
  assert.throws(() => commandInvocation(['fixture', 'text\r\nwhoami'], env), /newline/);
  assert.throws(() => commandInvocation(['fixture', 'x'.repeat(8192)], env), /input_too_large/);
  const command = commandInvocation(['fixture', ...args], env);
  const result = spawnSync(command.file, command.args, { ...command, env, encoding: 'utf8', windowsHide: true });
  assert.equal(result.status, 0, result.stderr); assert.deepEqual(JSON.parse(result.stdout), args);
  assert.throws(() => readFileSync(marker), { code: 'ENOENT' });
});
test('process termination kills an owned subprocess tree', { timeout: 10000 }, async t => {
  const dir = directory(t), heartbeat = join(dir, 'heartbeat');
  const grandchild = `setInterval(()=>require('node:fs').appendFileSync(${JSON.stringify(heartbeat)},'x'),20);`;
  const child = spawn(process.execPath, ['-e', `require('node:child_process').spawn(process.execPath,['-e',${JSON.stringify(grandchild)}],{stdio:'inherit'});setInterval(()=>{},1000);process.stdout.write('ready');`],
    { windowsHide: true, detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'] });
  const closed = once(child, 'close'); t.after(() => terminateProcess(child, 'SIGKILL', true));
  await once(child.stdout, 'data'); await new Promise(resolve => setTimeout(resolve, 250));
  terminateProcess(child, 'SIGKILL', true); await closed;
  const before = readFileSync(heartbeat, 'utf8'); await new Promise(resolve => setTimeout(resolve, 100));
  assert.equal(readFileSync(heartbeat, 'utf8'), before);
});
