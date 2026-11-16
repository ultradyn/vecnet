import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { constants } from 'node:os';
import test from 'node:test';
import vm from 'node:vm';
import { nativeTargets } from './platforms.mjs';

const source = await readFile(new URL('../../npm/bin/run.cjs', import.meta.url), 'utf8');
function fixture(platform, arch, args = ['help'], spawnError) {
  const process = Object.assign(new EventEmitter(), { platform, arch, argv: ['node', 'vecnet.cjs', ...args], pid: 12345 });
  const child = new EventEmitter(), calls = [], kills = [], messages = [], errors = [];
  child.kill = signal => kills.push(['child', signal]);
  process.kill = (pid, signal) => kills.push([pid, signal]);
  const module = { exports: {} };
  const directory = path.resolve('package with spaces', 'bin');
  vm.runInNewContext(source, {
    module, process, __dirname: directory,
    console: { log: message => messages.push(message), error: message => errors.push(message) },
    require(name) {
      if (name === 'node:child_process') return { spawn(...args) { calls.push(args); if (spawnError) throw spawnError; return child; } };
      if (name === 'node:os') return { constants };
      if (name === 'node:path') return path;
      if (name === '../package.json') return { version: '1.2.3' };
      throw new Error('unexpected import: ' + name);
    },
  });
  module.exports('vecnet');
  return { process, child, calls, kills, messages, errors, directory };
}

for (const target of nativeTargets) test(`wrapper selects ${target.platform} and forwards arguments without a shell`, () => {
  const [platform, arch] = target.platform.split('-');
  const args = ['send', 'recipient', 'message with spaces & | ^ %PATH% "quotes"'];
  const f = fixture(platform, arch, args);
  assert.equal(f.calls.length, 1);
  const [file, passed, options] = f.calls[0];
  assert.equal(file, path.join(f.directory, '..', 'native', target.platform, 'vecnet' + target.suffix));
  assert.deepEqual([...passed], args);
  assert.equal(options.shell, undefined);
  assert.equal(options.stdio, 'inherit');
  assert.equal(options.windowsHide, true);
  f.child.emit('exit', 37, null);
  assert.equal(f.process.exitCode, 37);
  assert.equal(f.process.listenerCount('SIGINT'), 0);
});

test('wrapper reports the bundled version without launching a process', () => {
  const f = fixture('win32', 'arm64', ['--version']);
  assert.deepEqual(f.messages, ['vecnet 1.2.3']);
  assert.equal(f.calls.length, 0);
});

for (const [platform, arch] of [['darwin', 'x64'], ['win32', 'ia32'], ['linux', 'riscv64']]) test(`wrapper reports unsupported ${platform}-${arch}`, () => {
  const f = fixture(platform, arch);
  assert.equal(f.process.exitCode, 1);
  assert.equal(f.calls.length, 0);
  assert.match(f.errors[0], new RegExp(`unsupported platform ${platform}-${arch}`));
  assert.match(f.errors[0], /Linux and Windows on x64 and arm64/);
});

for (const platform of ['linux', 'win32']) test(`${platform} spawn failure is actionable and removes handlers`, () => {
  const f = fixture(platform, 'x64');
  f.child.emit('error', Object.assign(new Error('missing'), { code: 'ENOENT' }));
  assert.equal(f.process.exitCode, 1);
  assert.match(f.errors[0], /ENOENT.*reinstall vecnet with npm/);
  assert.equal(f.process.listenerCount('SIGINT'), 0);
});

test('Unix forwards termination signals and preserves signal failure', () => {
  const f = fixture('linux', 'x64');
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) f.process.emit(signal);
  assert.deepEqual(f.kills, [['child', 'SIGINT'], ['child', 'SIGTERM'], ['child', 'SIGHUP']]);
  f.child.emit('exit', null, 'SIGPIPE');
  assert.equal(f.process.exitCode, 128 + (constants.signals.SIGPIPE ?? 1));
  assert.deepEqual(f.kills.at(-1), [12345, 'SIGPIPE']);
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) assert.equal(f.process.listenerCount(signal), 0);
});

test('Windows Ctrl+C waits for the console child and never uses POSIX re-raise', () => {
  const f = fixture('win32', 'x64');
  assert.equal(f.process.listenerCount('SIGTERM'), 0);
  assert.equal(f.process.listenerCount('SIGHUP'), 0);
  f.process.emit('SIGINT');
  assert.deepEqual(f.kills, [], 'Ctrl+C must not turn into TerminateProcess');
  f.child.emit('exit', 0, null);
  assert.equal(f.process.exitCode, 0);
  const killed = fixture('win32', 'x64');
  killed.child.emit('exit', null, 'SIGTERM');
  assert.notEqual(killed.process.exitCode, 0);
  assert.deepEqual(killed.kills, [], 'Windows must not try process.kill(self, signal)');
});

test('synchronous Windows spawn failure has the same actionable diagnostic', () => {
  const f = fixture('win32', 'x64', ['help'], Object.assign(new Error('blocked'), { code: 'UNKNOWN' }));
  assert.equal(f.process.exitCode, 1);
  assert.match(f.errors[0], /UNKNOWN.*reinstall vecnet with npm/);
  assert.equal(f.process.listenerCount('SIGINT'), 0);
});

test('spawn diagnostics include numeric errno but never caller arguments or raw error messages', () => {
  const f = fixture('win32', 'x64', ['--token', 'secret-token'], Object.assign(new Error('spawn --token secret-token'), { code: 'UNKNOWN', errno: -4094 }));
  assert.match(f.errors[0], /UNKNOWN; errno=-4094/);
  assert.doesNotMatch(f.errors[0], /secret-token|--token/);
});
