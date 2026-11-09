// Candidate internal/ui/jstest/settings-presence.test.mjs; source-only draft.
import test from 'node:test';
import assert from 'node:assert/strict';
import * as model from '../static/settings-model.js';
import { memStorage } from './settings-fixture.mjs';

const { buildPatch, createSettingsStore, lockOf } = model;
const G = 'presence.grokbot_ttl', D = 'presence.default_ttl';
const defaults = { [G]: 600, [D]: 3600, 'presence.session_ttl': 900, 'presence.launcher_ttl': 900, 'events.refresh_interval': 1800 };
const ttl = (key, value, source = 'default', origin = '', extra = {}) => ({
  key, type: 'duration', default: defaults[key] ?? 3600, value, source, origin,
  label: key, description: 'Explicit stored overrides take precedence; reset restores the starting layer.',
  min: 60, max: 86400, unit: 'seconds', group: 'notifications_presence',
  editable: true, settable_by: 'operator', runtime: 'live', danger: ['presence_shortened'], ...extra,
});
const proposal = (key, input) => new Map([[key, { op: 'set', input }]]);
const snapshot = (preferences, revision = 7, etag = '"p7"') => ({ status: 200, etag, body: { revision, preferences } });
const failure = (status, code, details = {}) => Object.assign(new Error(code), { status, code, details });
// Scripted wire replies only: no fake TTL resolution, risk calculation or persistence.
async function ready(t, preferences, { gets = [], previews = [], patches = [] } = {}) {
  const calls = [], queues = { get: [snapshot(preferences), ...gets], preview: [...previews], patch: [...patches] };
  const answer = async (op, patch, etag) => {
    calls.push({ op, ...(patch === undefined ? {} : { patch: structuredClone(patch) }), etag });
    assert.ok(queues[op].length, 'unexpected ' + op);
    const next = queues[op].shift();
    if (next instanceof Error) throw next;
    return structuredClone(next);
  };
  const store = createSettingsStore({ storage: memStorage(), api: {
    prefs: opts => answer('get', undefined, opts.etag),
    previewPrefs: (patch, tag) => answer('preview', patch, tag),
    patchPrefs: (patch, tag) => answer('patch', patch, tag),
  } });
  t.after(() => store.destroy());
  await store.load();
  assert.equal(store.state.phase, 'ready');
  return { store, calls };
}

// These call today's exported buildPatch, independent of any future helper.
for (const [key, value, source, origin, input] of [
  [D, 3600, 'default', '', '1h'], [G, 600, 'default', '', '10m'],
  [D, 3600, 'flag', 'VECNET_PRESENCE_TTL', '1h'], [G, 600, 'flag', '--grok-ttl', '10m'],
  [D, 7200, 'flag', '--presence-ttl', '2h'], [G, 1200, 'flag', 'GROK_TTL', '20m'],
]) {
  test(`existing buildPatch retains deliberate equal SET: ${key}/${source}/${value}`, () => {
    const row = ttl(key, value, source, origin), before = structuredClone(row);
    assert.deepEqual(buildPatch([row], proposal(key, input)), { set: { [key]: value }, reset: [] });
    assert.deepEqual(row, before, 'draft construction never changes hub provenance');
  });
}

test('stored equality and unrelated numeric equality stay clean; generic changed values still SET', () => {
  for (const row of [ttl(D, 3600, 'stored'), ttl(G, 600, 'stored'), ttl('presence.session_ttl', 900), ttl('presence.launcher_ttl', 900), ttl('events.refresh_interval', 1800)]) {
    assert.deepEqual(buildPatch([row], proposal(row.key, String(row.value))), { set: {}, reset: [] }, row.key);
  }
  const ordinary = ttl('events.refresh_interval', 1800);
  assert.deepEqual(buildPatch([ordinary], proposal(ordinary.key, '10m')), { set: { 'events.refresh_interval': 600 }, reset: [] });
  const stored = ttl(G, 1200, 'stored');
  assert.deepEqual(buildPatch([stored], proposal(G, '10m')), { set: { [G]: 600 }, reset: [] }, 'different stored value still changes');
});

test('only exact editable/live D and G flag rows unlock; unavailable/bootstrap remain locked', () => {
  for (const key of [D, G]) {
    assert.equal(lockOf(ttl(key, key === G ? 600 : 3600, 'flag', '--ttl')), null);
    for (const source of ['default', 'flag', 'stored']) for (const extra of [{ runtime: 'unavailable', unavailable_reason: 'Consumer absent' }, { runtime: 'bootstrap' }, { editable: false }]) {
      const row = ttl(key, 600, source, '--ttl', extra);
      assert.ok(lockOf(row), key + source + JSON.stringify(extra));
      assert.deepEqual(buildPatch([row], proposal(key, '20m')), { set: {}, reset: [] });
    }
  }
  for (const key of ['events.refresh_interval', 'presence.session_ttl', 'presence.launcher_ttl', 'presence.grokbot_ttl_extra']) {
    const row = ttl(key, 900, 'flag', 'VECNET_OTHER');
    assert.equal(lockOf(row).kind, 'flag', key);
    assert.match(lockOf(row).text, /VECNET_OTHER/);
    assert.deepEqual(buildPatch([row], proposal(key, '20m')), { set: {}, reset: [] });
  }
});

test('SET and RESET remain distinct; only stored reset intent is sent and unknown drafts are ignored', () => {
  const rows = [ttl(D, 7200, 'stored'), ttl(G, 600), ttl('events.refresh_interval', 900, 'flag')];
  const draft = new Map([[D, { op: 'reset' }], [G, { op: 'reset' }], ['events.refresh_interval', { op: 'reset' }], ['unknown', { op: 'set', input: '10m' }]]);
  assert.deepEqual(buildPatch(rows, draft), { set: {}, reset: [D] });
  assert.deepEqual(buildPatch(rows, proposal(G, '10m')), { set: { [G]: 600 }, reset: [] });
  for (const source of ['default', 'flag']) assert.deepEqual(buildPatch([ttl(D, 3600, source)], new Map([[D, { op: 'reset' }]])), { set: {}, reset: [] });
});

for (const source of ['default', 'flag']) {
  test(`equal SET stays draft across views and refresh; reply alone assigns stored source: ${source}`, async t => {
    const original = ttl(D, 3600, source, source === 'flag' ? '--presence-ttl' : '');
    const { store, calls } = await ready(t, [original], {
      gets: [{ status: 304, etag: '"p7"', body: null }],
      previews: [{ base_revision: 7, risks: [], confirm_token: 'unused' }],
      patches: [snapshot([ttl(D, 3600, 'stored')], 8, '"p8"')],
    });
    store.edit(D, '1h');
    assert.deepEqual(store.dirtyKeys(), [D]);
    for (const view of ['policy', 'list']) {
      store.setView(view);
      assert.equal(store.input(D), '1h');
      assert.equal(store.source(D), 'edited');
      assert.equal(store.rowOf(D).source, source);
      assert.equal(store.rowOf(D).origin, original.origin);
    }
    await store.refresh();
    assert.deepEqual(store.dirtyKeys(), [D]);
    assert.equal(calls.filter(c => c.op === 'patch').length, 0);
    await store.save();
    assert.deepEqual(calls.slice(2), [
      { op: 'preview', patch: { set: { [D]: 3600 }, reset: [] }, etag: '"p7"' },
      { op: 'patch', patch: { set: { [D]: 3600 }, reset: [] }, etag: '"p7"' },
    ]);
    assert.equal(store.source(D), 'stored');
    assert.equal(store.state.revision, 8);
    assert.equal(store.state.etag, '"p8"');
    store.edit(D, '1h');
    assert.deepEqual(store.dirtyKeys(), []);
  });
}

const shortened = { base_revision: 7, risks: [{ code: 'presence_shortened', keys: [G], message: 'Shorter presence TTL may release held wakes: grokbot 2h0m0s -> 10m0s.' }], confirm_token: 'server-issued-grok-token', expires_at: '2026-11-10T03:05:00Z' };
test('raw-equal G600 under stored D7200 previews, cancels without write, and confirms exact server token', async t => {
  const initial = [ttl(D, 7200, 'stored'), ttl(G, 600)];
  const { store, calls } = await ready(t, initial, {
    previews: [shortened, shortened], patches: [snapshot([ttl(D, 7200, 'stored'), ttl(G, 600, 'stored')], 8, '"p8"')],
  });
  store.edit(G, '10m');
  assert.equal(store.source(G), 'edited');
  assert.equal(store.rowOf(G).source, 'default');
  await store.save();
  assert.equal(store.state.busy, 'confirming');
  assert.deepEqual(calls.slice(1), [{ op: 'preview', patch: { set: { [G]: 600 }, reset: [] }, etag: '"p7"' }]);
  assert.deepEqual(store.state.confirm.preview.risks, shortened.risks);
  assert.equal(store.state.revision, 7);
  store.cancelConfirm();
  assert.equal(calls.filter(c => c.op === 'patch').length, 0);
  assert.deepEqual(store.dirtyKeys(), [G]);
  assert.equal(store.state.etag, '"p7"');
  await store.save();
  await store.confirm();
  assert.deepEqual(calls.at(-1), { op: 'patch', patch: { set: { [G]: 600 }, reset: [], confirm: ['server-issued-grok-token'] }, etag: '"p7"' });
  assert.equal(store.source(G), 'stored');
  assert.equal(store.state.revision, 8);
  assert.equal(store.state.etag, '"p8"');
  assert.deepEqual(store.dirtyKeys(), []);
});

test('RESET keeps committed policy until reply and exposes the hub starting layer rather than guessed factory', async t => {
  const { store, calls } = await ready(t, [ttl(D, 7200, 'stored'), ttl(G, 600, 'stored')], {
    previews: [{ base_revision: 7, risks: [] }],
    patches: [snapshot([ttl(D, 7200, 'stored'), ttl(G, 1200, 'flag', '--grok-ttl')], 8, '"p8"')],
  });
  store.resetKey(G);
  assert.equal(store.source(G), 'edited');
  assert.equal(store.rowOf(G).source, 'stored');
  assert.equal(store.rowOf(G).value, 600, 'committed raw row is unchanged while RESET is pending');
  assert.deepEqual(buildPatch(store.state.rows, store.state.draft), { set: {}, reset: [G] });
  await store.save();
  assert.deepEqual(calls.at(-1), { op: 'patch', patch: { set: {}, reset: [G] }, etag: '"p7"' });
  assert.equal(store.value(G), 1200);
  assert.equal(store.source(G), 'flag');
  assert.equal(store.rowOf(G).origin, '--grok-ttl');
  store.resetKey(G);
  assert.deepEqual(store.dirtyKeys(), [], 'there is no stored row to RESET after the reply');
});

test('invalid D/G input stays coupled to the field and blocks every save request', async t => {
  const { store, calls } = await ready(t, [ttl(D, 3600), ttl(G, 600)]);
  for (const [key, input] of [[G, '30s'], [D, '1.5h']]) {
    store.edit(key, input);
    assert.ok(store.errors()[key]);
    await store.save();
    assert.equal(store.takeFocus(), key);
    assert.deepEqual(calls.map(c => c.op), ['get']);
    store.undo();
  }
});

for (const [status, code, details] of [[503, 'prefs_storage_unavailable', {}], [403, 'forbidden', {}], [409, 'prefs_policy_locked', { field: G }]]) {
  test(`failed equal SET keeps draft and authoritative source: ${code}`, async t => {
    const { store, calls } = await ready(t, [ttl(G, 600)], { previews: [failure(status, code, details)] });
    store.edit(G, '10m');
    await store.save();
    assert.deepEqual(store.dirtyKeys(), [G]);
    assert.equal(store.source(G), 'edited');
    assert.equal(store.rowOf(G).source, 'default');
    assert.equal(store.state.revision, 7);
    assert.equal(store.state.etag, '"p7"');
    assert.equal(calls.filter(c => c.op === 'patch').length, 0);
    assert.ok(store.state.saveError);
    if (status === 403) assert.match(store.state.saveError.text, /Only the operator/);
    if (details.field) assert.match(store.errors()[G], /policy/);
  });
}

test('storage failure after accepted preview keeps equal draft and server source', async t => {
  const { store, calls } = await ready(t, [ttl(D, 7200, 'stored'), ttl(G, 600)], {
    previews: [shortened], patches: [failure(503, 'prefs_storage_unavailable')],
  });
  store.edit(G, '10m');
  await store.save();
  await store.confirm();
  assert.equal(calls.filter(c => c.op === 'patch').length, 1);
  assert.deepEqual(store.dirtyKeys(), [G]);
  assert.equal(store.rowOf(G).source, 'default');
  assert.equal(store.state.revision, 7);
  assert.equal(store.state.etag, '"p7"');
  assert.equal(store.state.confirm, null);
  assert.ok(store.state.saveError);
});

test('412 during confirmed PATCH retains equal draft, reconciles ETag and requires a fresh preview', async t => {
  const latest = snapshot([ttl(D, 7200, 'stored'), ttl(G, 600)], 8, '"p8"');
  const { store, calls } = await ready(t, [ttl(D, 7200, 'stored'), ttl(G, 600)], {
    gets: [latest], previews: [shortened, { ...shortened, base_revision: 8, confirm_token: 'fresh-token' }],
    patches: [failure(412, 'prefs_revision_conflict'), snapshot([ttl(D, 7200, 'stored'), ttl(G, 600, 'stored')], 9, '"p9"')],
  });
  store.edit(G, '10m');
  await store.save();
  await store.confirm();
  assert.equal(store.state.newer.reason, 'conflict');
  assert.deepEqual(store.dirtyKeys(), [G]);
  assert.equal(store.rowOf(G).source, 'default');
  assert.equal(store.state.etag, '"p7"');
  assert.equal(store.state.confirm, null);
  store.adoptNewer();
  assert.equal(store.state.etag, '"p8"');
  assert.deepEqual(store.dirtyKeys(), [G]);
  await store.save();
  await store.confirm();
  assert.deepEqual(calls.slice(-2), [
    { op: 'preview', patch: { set: { [G]: 600 }, reset: [] }, etag: '"p8"' },
    { op: 'patch', patch: { set: { [G]: 600 }, reset: [], confirm: ['fresh-token'] }, etag: '"p8"' },
  ]);
  assert.equal(store.state.revision, 9);
  assert.equal(store.state.etag, '"p9"');
});

// Future API prerequisite only; absence must not mask existing buildPatch RED above.
// grokPresencePolicy(rows) -> committed { seconds, key, source, origin }, no draft input.
for (const [name, rows, want] of [
  ['factory', [ttl(D, 3600), ttl(G, 600)], { seconds: 600, key: G, source: 'default', origin: '' }],
  ['explicit D flag', [ttl(D, 7200, 'flag', '--presence-ttl'), ttl(G, 600)], { seconds: 7200, key: D, source: 'flag', origin: '--presence-ttl' }],
  ['factory-equal D flag', [ttl(D, 3600, 'flag', 'VECNET_PRESENCE_TTL'), ttl(G, 600)], { seconds: 3600, key: D, source: 'flag', origin: 'VECNET_PRESENCE_TTL' }],
  ['factory-equal D stored', [ttl(D, 3600, 'stored'), ttl(G, 600)], { seconds: 3600, key: D, source: 'stored', origin: '' }],
  ['D stored', [ttl(D, 7200, 'stored'), ttl(G, 600)], { seconds: 7200, key: D, source: 'stored', origin: '' }],
  ['G flag beats stored D', [ttl(D, 7200, 'stored'), ttl(G, 600, 'flag', 'GROK_TTL')], { seconds: 600, key: G, source: 'flag', origin: 'GROK_TTL' }],
  ['G stored beats flag D', [ttl(D, 7200, 'flag', '--presence-ttl'), ttl(G, 600, 'stored')], { seconds: 600, key: G, source: 'stored', origin: '' }],
  ['G flag nonfactory', [ttl(G, 1200, 'flag', '--grok-ttl'), ttl(D, 7200, 'stored')], { seconds: 1200, key: G, source: 'flag', origin: '--grok-ttl' }],
  ['G stored nonfactory', [ttl(G, 1800, 'stored'), ttl(D, 3600)], { seconds: 1800, key: G, source: 'stored', origin: '' }],
]) {
  test(`API prerequisite: committed Grok policy literal ${name}`, () => {
    assert.equal(typeof model.grokPresencePolicy, 'function', 'future pure helper prerequisite, not behavior RED');
    const before = structuredClone(rows);
    rows.forEach(Object.freeze);
    assert.deepEqual(model.grokPresencePolicy(Object.freeze(rows)), want);
    assert.deepEqual(rows, before);
  });
}

test('API prerequisite: committed resolution ignores SET/RESET draft badges until server replies', async t => {
  assert.equal(typeof model.grokPresencePolicy, 'function', 'future pure helper prerequisite, not behavior RED');
  const { store } = await ready(t, [ttl(D, 7200, 'stored'), ttl(G, 600)]);
  store.edit(G, '10m');
  assert.equal(store.source(G), 'edited');
  assert.deepEqual(model.grokPresencePolicy(store.state.rows), { seconds: 7200, key: D, source: 'stored', origin: '' });
  store.undo();
  store.resetKey(D);
  assert.equal(store.source(D), 'edited');
  assert.deepEqual(model.grokPresencePolicy(store.state.rows), { seconds: 7200, key: D, source: 'stored', origin: '' });
});
