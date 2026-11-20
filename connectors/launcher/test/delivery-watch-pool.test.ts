import { test } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { createWatchPool, spawnWatchChild } from '../src/delivery/watch-pool.ts';
import type { WakeEvent, WatchChild, WatchEnd, WatchScope } from '../src/delivery/watch-pool.ts';
import { memoryLog } from '../../hub-client/testing/fixtures.ts';

/** A controllable child standing in for a spawned `watch --json` process. */
type FakeChild = WatchChild & { emit(event: WakeEvent): void; finish(end: WatchEnd): void; readonly closeCount: number };
function fakeChild(): FakeChild {
  const listeners = new Set<(event: WakeEvent) => void>();
  let resolveEnded!: (end: WatchEnd) => void;
  const ended = new Promise<WatchEnd>(resolve => { resolveEnded = resolve; });
  let closeCount = 0;
  const child: FakeChild = {
    get closeCount() { return closeCount; },
    close() { closeCount++; listeners.clear(); },
    ended,
    onEvent(f) { listeners.add(f); return () => { listeners.delete(f); }; },
    emit(event) { for (const f of [...listeners]) f(event); },
    finish(end) { listeners.clear(); resolveEnded(end); },
  };
  return child;
}
const scope = (patch: Partial<WatchScope> = {}): WatchScope => ({
  hubIdentity: 'hub:h1', endpoint: 'https://hub.invalid/hub', streamOwner: 'caller-1', credentialSlot: 'config:1', scope: 'default', ...patch,
});
const wake = { priority: 'wake' } as const;

test('identical verified scopes and revisions share one child; every distinct scope component isolates', () => {
  const pool = createWatchPool({ log: memoryLog() });
  const started: FakeChild[] = [];
  const start = () => { const child = fakeChild(); started.push(child); return child; };
  const a: WakeEvent[] = [], b: WakeEvent[] = [];
  const release1 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => a.push(e), () => assert.fail('stopped without an end'));
  assert.equal(started.length, 1);
  const release2 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => b.push(e), () => assert.fail('stopped without an end'));
  assert.equal(started.length, 1, 'identical scope and revision start a single shared child');
  started[0].emit(wake);
  assert.deepEqual(a, [wake]);
  assert.deepEqual(b, [wake], 'both subscribers receive the shared child\'s events');
  // Each component of the scope key isolates: hub identity, endpoint, stream
  // owner, credential slot and the default/narrowed discriminator.
  const variants = [
    { hubIdentity: 'hub:h2' }, { endpoint: 'https://other.invalid/hub' }, { streamOwner: 'caller-2' },
    { credentialSlot: 'config:2' }, { scope: 'as:peer' },
  ] as const;
  const isolated: WakeEvent[][] = variants.map(() => []);
  variants.forEach((patch, i) => {
    pool.subscribe({ scope: scope(patch), revision: 'r1', start }, e => isolated[i].push(e), () => assert.fail('stopped without an end'));
    assert.equal(started.length, 2 + i, 'a distinct scope component starts its own child');
  });
  started[1].emit({ priority: 'wake_idle' });
  assert.deepEqual(isolated[0], [{ priority: 'wake_idle' }]);
  assert.deepEqual(a, [wake], 'an isolated hub identity never reaches the shared subscriber');
  assert.deepEqual(isolated[1], [], 'a different subscriber\'s child never reaches this one');
  release1(); release2();
  void pool.close();
});

test('releasing one subscriber keeps the shared child; releasing the last stops it', () => {
  const pool = createWatchPool({ log: memoryLog() });
  const started: FakeChild[] = [];
  const start = () => { const child = fakeChild(); started.push(child); return child; };
  const a: WakeEvent[] = [], b: WakeEvent[] = [], stopA: WatchEnd[] = [], stopB: WatchEnd[] = [];
  const release1 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => a.push(e), end => stopA.push(end));
  const release2 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => b.push(e), end => stopB.push(end));
  const child = started[0];
  release1();
  assert.equal(child.closeCount, 0, 'one remaining subscriber keeps the shared child');
  child.emit(wake);
  assert.equal(a.length, 0);
  assert.deepEqual(b, [wake], 'the remaining subscriber still receives events');
  release2();
  assert.equal(child.closeCount, 1, 'releasing the last subscriber stops the shared child');
  // The orderly stop reaches no former subscriber.
  child.finish({ kind: 'reconnect' });
  assert.deepEqual(stopA, []);
  assert.deepEqual(stopB, []);
  void pool.close();
});

test('credential revision rotation replaces the child, detaches old-revision subscribers and fences late events', () => {
  const pool = createWatchPool({ log: memoryLog() });
  const started: FakeChild[] = [];
  const start = () => { const child = fakeChild(); started.push(child); return child; };
  const a: WakeEvent[] = [], b: WakeEvent[] = [], c: WakeEvent[] = [];
  const stopA: WatchEnd[] = [], stopB: WatchEnd[] = [], stopC: WatchEnd[] = [];
  const release1 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => a.push(e), end => stopA.push(end));
  const release2 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => b.push(e), end => stopB.push(end));
  const first = started[0];
  // A verified credential change re-subscribes the same scope at a new revision.
  const release3 = pool.subscribe({ scope: scope(), revision: 'r2', start }, e => c.push(e), end => stopC.push(end));
  assert.equal(started.length, 2, 'rotation replaces the scope\'s child with one start');
  assert.equal(first.closeCount, 1, 'the outdated child is closed');
  assert.deepEqual(stopA, [{ kind: 'reconnect' }], 'old-revision subscribers detach, they are not migrated');
  assert.deepEqual(stopB, [{ kind: 'reconnect' }]);
  assert.deepEqual(stopC, [], 'the rotating subscriber keeps its own subscription');
  // Late events from the retired source generation reach no listener.
  first.emit(wake);
  assert.equal(a.length, 0); assert.equal(b.length, 0); assert.equal(c.length, 0);
  // A detached subscriber re-verifies and rejoins the replacement child.
  release1();
  const release1b = pool.subscribe({ scope: scope(), revision: 'r2', start }, e => a.push(e), end => stopA.push(end));
  assert.equal(started.length, 2, 'rejoining the current child starts nothing');
  started[1].emit({ priority: 'wake_idle' });
  assert.deepEqual(a, [{ priority: 'wake_idle' }]);
  assert.deepEqual(c, [{ priority: 'wake_idle' }]);
  release1b(); release2(); release3();
  void pool.close();
});

test('a rotation naming a different scope detaches rather than migrating existing subscribers', () => {
  const pool = createWatchPool({ log: memoryLog() });
  const started: FakeChild[] = [];
  const start = () => { const child = fakeChild(); started.push(child); return child; };
  const a: WakeEvent[] = [], b: WakeEvent[] = [], stopA: WatchEnd[] = [];
  const release1 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => a.push(e), end => stopA.push(end));
  const release2 = pool.subscribe({ scope: scope(), revision: 'r1', start }, e => b.push(e), () => assert.fail('stopped without an end'));
  const original = started[0];
  // One engine learns the actual stream identity changed and re-subscribes under
  // a different verified scope, releasing its old subscription first.
  release2();
  const b2: WakeEvent[] = [];
  const release2b = pool.subscribe({ scope: scope({ hubIdentity: 'hub:h2', endpoint: 'https://hub2.invalid/hub' }), revision: 'r1', start },
    e => b2.push(e), () => assert.fail('stopped without an end'));
  assert.equal(started.length, 2, 'the new scope opens its own child');
  assert.equal(original.closeCount, 0, 'the old scope\'s remaining subscriber keeps its verified child');
  assert.deepEqual(stopA, [], 'the old scope\'s subscriber is never detached or migrated by another scope\'s rotation');
  original.emit(wake);
  assert.deepEqual(a, [wake]);
  assert.deepEqual(b2, [], 'the old child never reaches the migrated subscriber');
  started[1].emit({ priority: 'wake_idle' });
  assert.deepEqual(b2, [{ priority: 'wake_idle' }]);
  assert.deepEqual(a, [wake], 'the new child never reaches the old scope\'s subscriber');
  release1(); release2b();
  void pool.close();
});

test('a terminal refusal ends subscriptions without looping reconnects', async () => {
  const pool = createWatchPool({ log: memoryLog() });
  const started: FakeChild[] = [];
  const start = () => { const child = fakeChild(); started.push(child); return child; };
  const stopA: WatchEnd[] = [];
  const release1 = pool.subscribe({ scope: scope(), revision: 'r1', start }, () => assert.fail('no wake after a terminal end'), end => stopA.push(end));
  started[0].finish({ kind: 'terminal', status: 401 });
  await delay(1); // The end is delivered once the child's exit is observed.
  assert.deepEqual(stopA, [{ kind: 'terminal', status: 401 }]);
  release1(); // Releasing an ended subscription stays a no-op.
  await delay(1);
  assert.equal(started.length, 1, 'the pool itself never respawns after a terminal refusal');
  await pool.close();
  assert.equal(started.length, 1);
});

test('pool close closes every owned child and waits for their exit', async () => {
  const pool = createWatchPool({ log: memoryLog() });
  const started: FakeChild[] = [];
  const start = () => { const child = fakeChild(); started.push(child); return child; };
  const stopA: WatchEnd[] = [], stopB: WatchEnd[] = [];
  pool.subscribe({ scope: scope(), revision: 'r1', start }, () => {}, end => stopA.push(end));
  pool.subscribe({ scope: scope({ hubIdentity: 'hub:h2' }), revision: 'r1', start }, () => {}, end => stopB.push(end));
  const closing = pool.close();
  let settled = false;
  void closing.then(() => { settled = true; });
  assert.equal(started[0].closeCount, 1);
  assert.equal(started[1].closeCount, 1);
  await delay(20);
  assert.equal(settled, false, 'close waits for the owned children to exit');
  started[0].finish({ kind: 'reconnect' });
  started[1].finish({ kind: 'terminal', status: 403 });
  await closing;
  assert.deepEqual(stopA, [{ kind: 'reconnect' }]);
  assert.deepEqual(stopB, [{ kind: 'terminal', status: 403 }]);
  await pool.close(); // Idempotent.
  const before = started.length;
  pool.subscribe({ scope: scope(), revision: 'r1', start }, () => assert.fail(), () => assert.fail());
  assert.equal(started.length, before, 'a closed pool starts no child');
});

test('watch child parsing is bounded and notification-only', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-wp-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const script = join(dir, 'watch.mjs');
  writeFileSync(script, `
const write = (s) => new Promise((resolve) => process.stdout.write(s, resolve));
const out = async (o) => { await write(JSON.stringify(o) + '\\n'); };
await write('not json\\n');
await out({ type: 'delivery', data: { delivery_id: 'w1', message: { priority: 'wake', text: 'secret body' } } });
await write('x'.repeat(1048577) + '\\n');            // oversized newline-terminated line
await write('y'.repeat(3 * 1048576) + '\\n');         // oversized line across many pipe chunks
await out({ type: 'delivery', data: { delivery_id: 'w2', message: { priority: 'wake_idle' } } });
await out({ type: 'delivery', data: { delivery_id: 'w3', message: { priority: 'hold' } } });
await out({ type: 'watch_stopped', cursor: 'stale', data: { status: 401 } });
await out({ type: 'watch_stopped', cursor: '', data: { status: 401, text: 'not reconnecting: hub answered HTTP 401' } });
process.exit(2);
`);
  const child = spawnWatchChild({ command: { file: process.execPath, args: [script] }, env: {}, log: memoryLog(), context: { threadId: 't1' } });
  const events: WakeEvent[] = [];
  child.onEvent(e => events.push(e));
  assert.deepEqual(await child.ended, { kind: 'terminal', status: 401 });
  assert.deepEqual(events, [{ priority: 'wake' }, { priority: 'wake_idle' }],
    'malformed and oversized lines yield no wake and no message body is ever emitted');
});

test('closing a watch child synchronously fences its events and terminates the process', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-wp-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const script = join(dir, 'watch.mjs');
  writeFileSync(script, `
const out = (o) => process.stdout.write(JSON.stringify(o) + '\\n');
setInterval(() => out({ type: 'delivery', data: { delivery_id: 'w1', message: { priority: 'wake' } } }), 20);
`);
  const child = spawnWatchChild({ command: { file: process.execPath, args: [script] }, env: {}, log: memoryLog(), context: { threadId: 't1' } });
  const events: WakeEvent[] = [];
  child.onEvent(e => events.push(e));
  for (let i = 0; i < 500 && events.length === 0; i++) await delay(10);
  assert.ok(events.length >= 1, 'events flow before the close');
  child.close();
  const atClose = events.length;
  await delay(150);
  assert.equal(events.length, atClose, 'no event callback fires after the synchronous close fence');
  assert.deepEqual(await child.ended, { kind: 'reconnect' });
});
