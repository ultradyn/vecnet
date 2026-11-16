import { nodeCommand } from './fixtures/platform.ts';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { createRuntimeStatusReporter } from '../src/codex/runtime-status.ts';
import type { RuntimeSnapshot, RuntimeStatusPost } from '../src/codex/runtime-status.ts';
import type { Binding } from '../src/codex/engine.ts';
import { runtimeStatusPost } from '../src/codex/runtime-status.ts';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
function entry(n: number) {
  const binding: Binding = { profile: `p${n}`, as: '', caller: `peer_${n}`, hub: 'https://hub.invalid', codexHome: '/private/codex',
    threadId: `thread${n}`, configPath: '/private/config.toml', generation: n.toString(16).padStart(32, '0'),
    createdAt: 0, expiresAt: 1000000, attended: true, origin: 'explicit' };
  let snapshot: RuntimeSnapshot = { state: 'idle', delivery: 'ready', version: '0.169.0' };
  return { binding, engine: { runtimeStatus: () => snapshot }, set(s: RuntimeSnapshot) { snapshot = s; } };
}

test('runtime reporter sends changes, 30s heartbeats and retries only on later ticks', async () => {
  const clock = fakeClock(), log = memoryLog(), e = entry(1), reports: RuntimeSnapshot[] = [];
  let ok = true;
  const post: RuntimeStatusPost = async (_b, s) => { reports.push(s); return ok; };
  const r = createRuntimeStatusReporter({ clock, log, post });
  r.sync([e]); await flush(); assert.equal(reports.length, 1);
  clock.advance(10000); r.sync([e]); await flush(); assert.equal(reports.length, 1);
  clock.advance(20000); r.sync([e]); await flush(); assert.equal(reports.length, 2);
  ok = false; e.set({ state: 'working', delivery: 'ready', version: '0.169.0' });
  r.sync([e]); await flush(); assert.equal(reports.length, 3);
  await flush(); assert.equal(reports.length, 3, 'failure must not spin');
  ok = true; clock.advance(10000); r.sync([e]); await flush();
  assert.equal(reports.length, 4); assert.equal(reports[3].state, 'working');
  await r.stop(); assert.equal(reports.at(-1)?.reason, 'stopped');
});

test('runtime reporter bounds concurrency, coalesces queued snapshots and cancels removed owners', async () => {
  const clock = fakeClock(), log = memoryLog(), entries = [entry(1), entry(2), entry(3), entry(4)];
  const calls: { binding: Binding; snapshot: RuntimeSnapshot; signal: AbortSignal; finish(ok: boolean): void }[] = [];
  let active = 0, max = 0;
  const post: RuntimeStatusPost = (binding, snapshot, signal) => new Promise(resolve => {
    active++; max = Math.max(max, active);
    let done = false;
    const finish = (ok: boolean) => { if (!done) { done = true; active--; resolve(ok); } };
    signal.addEventListener('abort', () => finish(false), { once: true });
    calls.push({ binding, snapshot, signal, finish });
  });
  const r = createRuntimeStatusReporter({ clock, log, post });
  r.sync(entries); await flush(); assert.equal(calls.length, 2);
  entries[2].set({ state: 'working', delivery: 'ready' });
  r.sync(entries.slice(1, 3)); await flush();
  assert.equal(calls[0].signal.aborted, true);
  assert.equal(calls.length, 3); assert.equal(calls[2].binding, entries[2].binding);
  assert.equal(calls[2].snapshot.state, 'working', 'queued report reads latest status');
  assert.equal(calls.some(c => c.binding === entries[3].binding), false, 'removed queued owner never reports');
  calls[1].finish(true); calls[2].finish(true); await flush(); assert.equal(max, 2);
  r.sync([]); await r.stop();
});

test('runtime reporter stops replaced and expired owners and bounds total shutdown', async () => {
  const clock = fakeClock(), log = memoryLog(), e = entry(1), signals: AbortSignal[] = [];
  const post: RuntimeStatusPost = async (_b, _s, signal) => { signals.push(signal); return new Promise(() => {}); };
  const r = createRuntimeStatusReporter({ clock, log, post });
  r.sync([e]); await flush();
  const replacement = entry(1); replacement.binding.generation = 'f'.repeat(32);
  r.sync([replacement]); await flush(); assert.equal(signals[0].aborted, true);
  replacement.binding.expiresAt = clock.now(); r.sync([replacement]); await flush();
  assert.equal(signals[1].aborted, true);
  const stop = r.stop(); clock.advance(5000); await stop;
  const count = signals.length; r.sync([entry(2)]); await flush(); assert.equal(signals.length, count);
});

test('runtime reporter coalesces changes observed during an active report', async () => {
  const clock = fakeClock(), e = entry(1), calls: RuntimeSnapshot[] = [], finish: ((ok: boolean) => void)[] = [];
  const r = createRuntimeStatusReporter({ clock, log: memoryLog(), post: async (_b, snapshot) => {
    calls.push(snapshot); return new Promise<boolean>(resolve => finish.push(resolve));
  } });
  r.sync([e]); await flush();
  e.set({ state: 'working', delivery: 'ready' }); r.sync([e]);
  e.set({ state: 'blocked', delivery: 'unavailable', reason: 'waiting_input' }); r.sync([e]);
  assert.equal(calls.length, 1);
  finish[0](true); await flush();
  assert.equal(calls.length, 2); assert.equal(calls[1].reason, 'waiting_input');
  finish[1](true); await flush(); r.sync([]); await r.stop();
});

test('runtime POST helper pins owner/config, scrubs the environment and sends only status metadata', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-status-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const trace = join(dir, 'report.json');
  const bin = nodeCommand(join(dir, 'vecnet'), `const fs = require('node:fs');
fs.writeFileSync(${JSON.stringify(trace)}, JSON.stringify({ args: process.argv.slice(2), env: process.env, body: JSON.parse(fs.readFileSync(0, 'utf8')) }));
`);
  const e = entry(1); e.binding.as = 'peer_child';
  const post = runtimeStatusPost(bin, { ...Object.fromEntries(Object.entries(process.env).filter((v): v is [string,string] => v[1] !== undefined)), HOME: dir, PATH: process.env.PATH || '', VECNET_CONFIG: '/wrong/config',
    CODEX_HOME: '/private/secret', VECNET_TOKEN: 'private', UNRELATED: 'private' });
  const snapshot = { ...e.engine.runtimeStatus(), thread_id: 'private', note: 'private' };
  assert.equal(await post(e.binding, snapshot, new AbortController().signal), true);
  const got = JSON.parse(readFileSync(trace, 'utf8'));
  assert.deepEqual(got.body, e.engine.runtimeStatus());
  assert.equal(got.env.VECNET_CONFIG, e.binding.configPath);
  for (const key of ['CODEX_HOME', 'VECNET_TOKEN', 'UNRELATED']) assert.equal(got.env[key], undefined);
  assert.deepEqual(got.args, ['--net', 'p1', '--as', 'peer_child', 'codex', 'report-status', '--thread', 'thread1',
    '--codex-home', '/private/codex', '--expect-generation', '0'.repeat(31) + '1', '--expect-hub', 'https://hub.invalid', '--expect-caller', 'peer_1']);
});

test('runtime POST helper aborts a stuck child and treats failure as best effort', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-status-abort-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = nodeCommand(join(dir, 'vecnet'), 'setInterval(()=>{},1000);');
  const post = runtimeStatusPost(bin, { ...Object.fromEntries(Object.entries(process.env).filter((v): v is [string,string] => v[1] !== undefined)), HOME: dir, PATH: process.env.PATH || '' }), e = entry(1), controller = new AbortController();
  const pending = post(e.binding, e.engine.runtimeStatus(), controller.signal); controller.abort();
  assert.equal(await pending, false);
  assert.equal(await runtimeStatusPost(join(dir, 'missing'), {})(e.binding, e.engine.runtimeStatus(), new AbortController().signal), false);
});

for (const duringStop of [false, true]) {
  test(`runtime reporter fences all writes on singleton loss${duringStop ? ' during graceful stop' : ''}`, async () => {
    const clock = fakeClock(), e = entry(1), calls: { snapshot: RuntimeSnapshot; signal: AbortSignal }[] = [];
    const r = createRuntimeStatusReporter({ clock, log: memoryLog(), post: (_b, snapshot, signal) => new Promise(resolve => {
      calls.push({ snapshot, signal });
      signal.addEventListener('abort', () => resolve(false), { once: true });
    }) });
    r.sync([e]); await flush(); assert.equal(calls.length, 1);
    const stopping = duringStop ? r.stop() : undefined;
    r.fence(); // also before a queued offline post can invoke the adapter
    await (stopping ?? r.stop());
    assert.ok(calls.every(c => c.signal.aborted));
    assert.equal(calls.some(c => c.snapshot.reason === 'stopped'), false, 'no offline write after singleton loss');
    r.sync([e]); await flush(); assert.equal(calls.length, 1, 'fence cannot be reopened');
  });
}
