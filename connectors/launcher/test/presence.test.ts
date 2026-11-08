import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { PresenceBody } from '../../hub-client/src/wire.ts';
import type { PermissionMode } from '../src/backend.ts';
import { createPresence, presenceBody } from '../src/presence.ts';

const tick = async () => { for (let i = 0; i < 10; i++) await new Promise<void>(resolve => setImmediate(resolve)); };
type Snapshot = { running: number; open: number; max: number; perRequester: number; backends: Record<string, 'ok' | 'degraded' | 'down'>; modes: PermissionMode[]; presets: string[] };

test('presence: idle at start with capabilities and quota; working with a session; blocked at max_sessions; offline on stop', async () => {
  const clock = fakeClock(Date.parse('2026-11-05T00:00:00Z')), log = memoryLog();
  const posts: PresenceBody[] = [];
  let snap: Snapshot = { running: 0, open: 0, max: 2, perRequester: 1, backends: { t3: 'ok', paseo: 'degraded' }, modes: ['ask', 'auto'], presets: ['t3/gpt', 'paseo/sonnet'] };
  const presence = createPresence({ hub: { async presence(body) { posts.push(structuredClone(body)); } }, clock, log, host: 'xsm', heartbeatMs: 300_000, snapshot: () => snap });
  presence.start();
  await tick();
  assert.equal(posts.length, 1);
  assert.equal(posts[0].state, 'idle');
  assert.equal(posts[0].at, '2026-11-05T00:00:00.000Z');
  assert.deepEqual([...posts[0].capabilities!].sort(), ['host:xsm', 'launch:paseo', 'launch:t3', 'mode:ask', 'mode:auto', 'preset:paseo/sonnet', 'preset:t3/gpt']);
  assert.deepEqual(posts[0].quota, { max_sessions: 2, running: 0, open: 0, per_requester: 1, backends: { t3: 'ok', paseo: 'degraded' } });

  // Unchanged state is not re-posted on change, only on the heartbeat.
  presence.changed(); await tick();
  assert.equal(posts.length, 1);
  clock.advance(300_000); await tick();
  assert.equal(posts.length, 2);
  assert.equal(posts[1].state, 'idle');

  snap = { ...snap, running: 1, open: 1 };
  presence.changed(); await tick();
  assert.equal(posts.at(-1)!.state, 'working');
  assert.deepEqual(posts.at(-1)!.quota, { max_sessions: 2, running: 1, open: 1, per_requester: 1, backends: { t3: 'ok', paseo: 'degraded' } });
  // Parked sessions are open but not running: still working, never idle.
  snap = { ...snap, running: 0, open: 1 };
  presence.changed(); await tick();
  assert.equal(posts.at(-1)!.state, 'working');

  snap = { ...snap, running: 2, open: 2 };
  presence.changed(); await tick();
  assert.equal(posts.at(-1)!.state, 'blocked');
  snap = { ...snap, running: 0, open: 0, backends: { t3: 'down', paseo: 'down' } };
  presence.changed(); await tick();
  assert.equal(posts.at(-1)!.state, 'blocked');

  const count = posts.length;
  await presence.stop();
  assert.equal(posts.length, count + 1);
  assert.equal(posts.at(-1)!.state, 'offline');
  clock.advance(900_000); presence.changed(); await tick();
  assert.equal(posts.length, count + 1);
});

test('presence: a failed post is logged without a body and retried on the next change or heartbeat', async () => {
  const clock = fakeClock(0), log = memoryLog();
  let fail = true;
  const posts: PresenceBody[] = [];
  const presence = createPresence({ hub: { async presence(body) { if (fail) throw new Error('Bearer secret-token'); posts.push(body); } }, clock, log, host: 'h', heartbeatMs: 1000,
    snapshot: () => ({ running: 0, open: 0, max: 1, perRequester: 1, backends: { fake: 'ok' }, modes: ['ask'], presets: ['fake/test'] }) });
  presence.start(); await tick();
  assert.equal(posts.length, 0);
  assert.match(log.text(), /presence/);
  assert.doesNotMatch(log.text(), /secret-token/);
  fail = false;
  presence.changed(); await tick();
  assert.equal(posts.length, 1);
  await presence.stop();
});

test('presence advertises configured project aliases without repository paths', () => {
  const body = presenceBody({ running: 0, open: 0, max: 2, perRequester: 1,
    backends: { 'codex-app-server': 'ok' }, modes: ['ask'], presets: ['codex-app-server/default'], projects: ['vecnet', 'app'] }, 'host');
  assert.ok(body.capabilities?.includes('project:vecnet'));
  assert.ok(body.capabilities?.includes('project:app'));
  assert.ok(body.capabilities?.includes('preset:codex-app-server/default'));
  assert.deepEqual(body.quota?.backends, { 'codex-app-server': 'ok' });
});

test('presence scopes native and T3 permission modes while preserving legacy global tags', () => {
  const body = presenceBody({ running: 0, open: 0, max: 2, perRequester: 1,
    backends: { 'codex-app-server': 'ok', t3: 'ok' },
    modes: ['read-only', 'ask', 'auto', 'full-access'], presets: ['codex-app-server/default', 't3/sol'],
    backendModes: { 'codex-app-server': ['read-only', 'ask', 'auto', 'full-access'], t3: ['ask', 'auto', 'full-access'] } }, 'host');
  const scoped = body.capabilities!.filter(c => c.startsWith('mode:') && c.includes('/'));
  assert.deepEqual(scoped, ['mode:codex-app-server/read-only', 'mode:codex-app-server/ask', 'mode:codex-app-server/auto',
    'mode:codex-app-server/full-access', 'mode:t3/ask', 'mode:t3/auto', 'mode:t3/full-access']);
  assert.ok(body.capabilities!.includes('mode:read-only'));
  assert.ok(!body.capabilities!.includes('mode:t3/read-only'));
});


test('scratch discovery advertises only explicitly configured project aliases', () => {
  const body = presenceBody({running:0,open:0,max:2,perRequester:1,backends:{'codex-app-server':'ok'},modes:['ask'],presets:[],projects:['temporary','scratch'],scratchProjects:['scratch','not-configured']},'host');
  assert.ok(body.capabilities?.includes('project:temporary'));
  assert.ok(body.capabilities?.includes('scratch:scratch'));
  assert.ok(!body.capabilities?.includes('scratch:temporary'));
  assert.ok(!body.capabilities?.includes('scratch:not-configured'));
});
