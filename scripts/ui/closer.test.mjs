import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { existsSync } from 'node:fs';
import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { demoCloser, liveDemoCount } from './lib.mjs';

// The fake pid is above Linux's pid_max ceiling (2^22), so even a stray
// real signal could only fail with ESRCH; the recorder never sends one.
const fakePid = 2 ** 30;
function fakeChild() {
  const child = new EventEmitter();
  Object.assign(child, { pid: fakePid, exitCode: null, signalCode: null });
  child.exit = (code = 0) => { child.exitCode = code; child.emit('exit', code, null); child.emit('close', code, null); };
  return child;
}
async function fixture(options = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'vecnet-ui-data-'));
  const child = fakeChild();
  const signals = [];
  const { close } = demoCloser(child, dir, { signal: (pgid, signal) => signals.push([pgid, signal]), ...options });
  return { dir, child, signals, close };
}

test('close() after the demo exited sends no group signal and removes its dir', async () => {
  const { dir, child, signals, close } = await fixture();
  assert.equal(liveDemoCount(), 1);
  child.exit(0); // the exit listener removes the live entry
  assert.equal(liveDemoCount(), 0);
  await close();
  assert.deepEqual(signals, [], 'a stale PGID was signalled');
  assert.equal(existsSync(dir), false);
});

test('close() of a running demo sends TERM and cancels KILL once it exits', async () => {
  const { dir, child, signals, close } = await fixture({ killAfterMs: 50 });
  const closing = close();
  assert.deepEqual(signals, [[fakePid, 'SIGTERM']]);
  child.exit(0);
  await closing;
  await new Promise(resolve => setTimeout(resolve, 100));
  assert.deepEqual(signals, [[fakePid, 'SIGTERM']], 'KILL escalated after the demo exited');
  assert.equal(existsSync(dir), false);
  assert.equal(liveDemoCount(), 0);
});

test('KILL escalates only while the demo is still running', async () => {
  const { child, signals, close } = await fixture({ killAfterMs: 20 });
  const closing = close();
  await new Promise(resolve => setTimeout(resolve, 60));
  assert.deepEqual(signals, [[fakePid, 'SIGTERM'], [fakePid, 'SIGKILL']]);
  child.exit(137);
  await closing;
});

test('KILL is skipped when the demo exited but its streams are still closing', async () => {
  const { child, signals, close } = await fixture({ killAfterMs: 20 });
  const closing = close();
  child.exitCode = 0;
  child.emit('exit', 0, null); // 'close' waits on inherited stdio
  await new Promise(resolve => setTimeout(resolve, 60));
  assert.deepEqual(signals, [[fakePid, 'SIGTERM']]);
  child.emit('close', 0, null);
  await closing;
});
