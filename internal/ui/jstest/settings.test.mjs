import test from 'node:test';
import assert from 'node:assert/strict';
import {
  VIEW_KEY, buildPatch, createSettingsStore, describeLoad, durationText, formatDuration, formatValue, laneStops, laneView,
  LANES, loadView, lockOf, parseDuration, parseField, rememberView, timingOf, visibleRows,
  BLOB_NEW_ONLY, blobOffText, storageView, validStorage,
} from '../static/settings-model.js';
import { mount as mountSettings } from '../static/settings.js';
import { fakeHub, memStorage, rows } from './settings-fixture.mjs';

const flush = () => new Promise(resolve => setImmediate(resolve));
async function ready(opts = {}) {
  const hub = fakeHub(opts.hub);
  const storage = opts.storage || memStorage();
  const look = { theme: 'system', density: 'comfortable' };
  const appearance = opts.appearance || { get: () => ({ ...look }), set: a => { Object.assign(look, a); return { persisted: true }; }, subscribe: () => () => {} };
  const store = createSettingsStore({ api: hub.api, storage, appearance });
  await store.load();
  return { hub, store, st: store.state, storage };
}
const row = (st, key) => st.rows.find(r => r.key === key);

// ---------- view memory ----------

test('List is the default view; Policy is remembered; corrupt or blocked storage falls back to List', () => {
  assert.equal(loadView(memStorage()), 'list');
  assert.equal(loadView(memStorage({ init: { [VIEW_KEY]: 'policy' } })), 'policy');
  assert.equal(loadView(memStorage({ init: { [VIEW_KEY]: '{"view":"policy"}' } })), 'list');
  assert.equal(loadView(memStorage({ init: { [VIEW_KEY]: 'bento' } })), 'list');
  assert.equal(loadView(memStorage({ blocked: true })), 'list');
  assert.equal(loadView(undefined), 'list');
  const s = memStorage();
  assert.equal(rememberView(s, 'policy'), true);
  assert.equal(s.map.get(VIEW_KEY), 'policy');
  rememberView(s, '<script>');
  assert.equal(s.map.get(VIEW_KEY), 'list', 'only list|policy is ever stored');
  assert.equal(rememberView(memStorage({ blocked: true }), 'policy'), false);
});

test('the header toggle switches views over one store, remembering only the view', async () => {
  const { store, st, storage } = await ready();
  assert.equal(st.view, 'list');
  store.edit('events.refresh_interval', '15m');
  store.setView('policy');
  assert.equal(st.view, 'policy');
  assert.deepEqual([...storage.map.keys()], [VIEW_KEY]);
  assert.equal(storage.map.get(VIEW_KEY), 'policy');
  assert.equal(store.source('events.refresh_interval'), 'edited', 'edits survive the switch');
  assert.equal(store.value('events.refresh_interval'), 900);
  store.setView('list');
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval']);
});

test('a blocked view store still switches views for this page', async () => {
  const { store, st } = await ready({ storage: memStorage({ blocked: true }) });
  assert.equal(st.view, 'list');
  store.setView('policy');
  assert.equal(st.view, 'policy');
});

// ---------- parsing and formatting ----------

test('durations: units in, whole seconds out; fractions and junk refused', () => {
  assert.deepEqual(parseDuration('90'), { seconds: 90 });
  assert.deepEqual(parseDuration('30m'), { seconds: 1800 });
  assert.deepEqual(parseDuration('1h30m'), { seconds: 5400 });
  assert.deepEqual(parseDuration(' 1 h 30 min '), { seconds: 5400 });
  assert.deepEqual(parseDuration('2 days'), { seconds: 172800 });
  assert.deepEqual(parseDuration('1w'), { seconds: 604800 });
  for (const bad of ['1.5h', '-5m', '5 fortnights', 'h', '30m!', '0x10']) assert.ok(parseDuration(bad).error, bad);
  assert.equal(parseDuration('').error, 'empty');
  assert.equal(parseDuration('99999999999999999999').error, 'overflow');
  assert.equal(parseDuration('9007199254740991d').error, 'overflow');
  assert.equal(formatDuration(1800), '30 min');
  assert.equal(formatDuration(5400), '1 h 30 min');
  assert.equal(formatDuration(604800), '7 d');
  assert.equal(durationText(5400), '1h30m');
  assert.equal(durationText(0), '0');
});

test('fields use the hub bounds: ranges, zero only where disabled, whole numbers only', () => {
  const rs = rows({ installed: true }), r = k => rs.find(x => x.key === k);
  assert.deepEqual(parseField(r('events.refresh_interval'), '10m'), { value: 600 });
  assert.match(parseField(r('events.refresh_interval'), '30s').error, /between 1 min and 1 d/);
  assert.match(parseField(r('events.refresh_interval'), '0').error, /between/);
  assert.deepEqual(parseField(r('retention.history'), '0'), { value: 0 }, 'zero means forever for history');
  assert.match(parseField(r('retention.history'), '1h').error, /0 \(forever\) or between 1 d/);
  assert.equal(parseField(r('agents.default_max_depth'), '2.5').error, 'Enter a whole number.');
  assert.equal(parseField(r('agents.default_max_depth'), '').error, 'Enter a number.');
  assert.match(parseField(r('agents.default_max_depth'), '65').error, /between 1 and 64/);
  assert.deepEqual(parseField(r('blobs.expiry'), 'off'), { value: 'off' });
  assert.ok(parseField(r('blobs.expiry'), '2d').error);
  assert.deepEqual(parseField(r('retention.archive_mode'), true), { value: true });
  assert.ok(parseField(r('retention.archive_mode'), 'true').error);
  assert.equal(formatValue(r('retention.history'), 0), 'Forever');
  assert.equal(formatValue(r('retention.ack_events'), 0), 'Never pruned');
  assert.equal(formatValue(r('blobs.expiry'), 'off'), 'Off (keep files)');
  assert.equal(formatValue(r('rates.sends_per_minute'), 60), '60 per minute');
});

// ---------- locks, sources, timing ----------

test('unavailable adapters stay unavailable; start-up-only rows are read-only; flags are locked in both views', async () => {
  const pre = rows();
  const flagged = pre.map(r => (r.key === 'events.refresh_interval' ? { ...r, value: 900, source: 'flag', origin: 'VECNET_EVENTS_REFRESH_INTERVAL' } : r));
  const { store, st } = await ready({ hub: { preferences: flagged } });
  for (const key of ['retention.ack_events', 'retention.history', 'retention.archive_mode', 'blobs.expiry']) {
    assert.equal(lockOf(row(st, key)).kind, 'unavailable', key);
    assert.equal(timingOf(row(st, key)), 'Unavailable on this hub');
  }
  assert.match(lockOf(row(st, 'blobs.expiry')).text, /R2 expiry adapter is not installed/);
  assert.equal(lockOf(row(st, 'agents.max_expiry')).kind, 'readonly');
  assert.equal(timingOf(row(st, 'agents.max_expiry')), 'Fixed when the hub starts');
  const flag = lockOf(row(st, 'events.refresh_interval'));
  assert.equal(flag.kind, 'flag');
  assert.match(flag.text, /VECNET_EVENTS_REFRESH_INTERVAL environment variable/);
  assert.equal(lockOf({ ...row(st, 'events.refresh_interval'), origin: '--events-refresh-interval' }).text.includes('--events-refresh-interval flag'), true);
  assert.equal(timingOf(row(st, 'presence.default_ttl')), 'Applies when saved, without a restart');

  for (const view of ['list', 'policy']) {
    store.setView(view);
    for (const key of ['events.refresh_interval', 'blobs.expiry', 'retention.archive_mode', 'agents.max_expiry']) {
      store.edit(key, key === 'retention.archive_mode' ? true : key === 'blobs.expiry' ? 'off' : '1h');
      store.resetKey(key);
      assert.equal(store.source(key), row(st, key).source, `${key} stays unedited in ${view}`);
    }
    assert.deepEqual(store.dirtyKeys(), []);
  }
  assert.equal(store.source('events.refresh_interval'), 'flag');
  assert.equal(store.source('presence.default_ttl'), 'default');
});

test('source badges are the hub\'s wire sources; edited is only a draft state', async () => {
  const pre = rows().map(r => (r.key === 'agents.default_max_depth' ? { ...r, value: 40, source: 'stored' } : r));
  const { store } = await ready({ hub: { preferences: pre } });
  assert.equal(store.source('agents.default_max_depth'), 'stored');
  assert.equal(store.source('agents.thread_budget'), undefined);
  store.edit('agents.default_max_depth', '41');
  assert.equal(store.source('agents.default_max_depth'), 'edited');
  store.edit('agents.default_max_depth', '40');
  assert.equal(store.source('agents.default_max_depth'), 'stored', 'typing the saved value back is clean');
  assert.deepEqual(store.dirtyKeys(), []);
});

// ---------- one store: edit, save, undo, errors across views ----------

test('atomic dirty patch: every dirty key in one PATCH with If-Match; Undo makes no request', async () => {
  const { hub, store, st } = await ready();
  store.edit('events.refresh_interval', '10m');
  store.edit('agents.default_max_depth', '20');
  store.setView('policy');
  store.edit('rates.sends_per_minute', '90');
  assert.deepEqual(buildPatch(st.rows, st.draft), { set: { 'agents.default_max_depth': 20, 'events.refresh_interval': 600, 'rates.sends_per_minute': 90 }, reset: [] });
  const before = hub.calls.length;
  store.undo();
  assert.equal(hub.calls.length, before, 'undo is local');
  assert.deepEqual(store.dirtyKeys(), []);
  assert.equal(store.value('events.refresh_interval'), 1800);

  store.edit('events.refresh_interval', '10m');
  store.edit('agents.default_max_depth', '20');
  await store.save();
  const patch = hub.calls.filter(c => c.op === 'patch');
  assert.equal(patch.length, 1);
  assert.deepEqual(patch[0].patch, { set: { 'agents.default_max_depth': 20, 'events.refresh_interval': 600 }, reset: [] });
  assert.equal(patch[0].tag, '"prefs-3-g1"');
  assert.equal(st.etag, '"prefs-4-g1"');
  assert.equal(st.revision, 4);
  assert.deepEqual(st.saved.keys, ['agents.default_max_depth', 'events.refresh_interval']);
  assert.equal(store.source('events.refresh_interval'), 'stored', 'the badge comes from the reply');
  assert.deepEqual(store.dirtyKeys(), []);
  assert.match(st.announce.text, /Saved 2 changes/);
});

test('saving a built-in default sends the typed value and shows the hub\'s default badge', async () => {
  const pre = rows().map(r => (r.key === 'agents.default_max_depth' ? { ...r, value: 40, source: 'stored' } : r));
  const { hub, store } = await ready({ hub: { preferences: pre } });
  store.edit('agents.default_max_depth', '32');
  assert.equal(store.source('agents.default_max_depth'), 'edited', 'not shown as default before the hub says so');
  await store.save();
  const p = hub.calls.find(c => c.op === 'patch').patch;
  // The page cannot see the start-up layer under a stored override, so it
  // sends the value and the hub drops the override when it equals the
  // inherited default (internal/hub/preferences.go).
  assert.deepEqual(p, { set: { 'agents.default_max_depth': 32 }, reset: [] });
  assert.equal(store.source('agents.default_max_depth'), 'default');
});

test('Reset to inherited is a dirty draft showing the fallback before Save', async () => {
  const pre = rows().map(r => (r.key === 'presence.session_ttl' ? { ...r, value: 1800, source: 'stored' } : r));
  const { hub, store } = await ready({ hub: { preferences: pre } });
  store.resetKey('presence.session_ttl');
  assert.equal(store.source('presence.session_ttl'), 'edited');
  assert.equal(store.value('presence.session_ttl'), 900, 'the fallback shows before Save');
  store.resetKey('events.refresh_interval');
  assert.deepEqual(store.dirtyKeys(), ['presence.session_ttl'], 'resetting a default row is a no-op');
  await store.save();
  // Shorter than 1800: a presence risk, previewed and awaiting confirmation.
  assert.equal(store.state.busy, 'confirming');
  assert.deepEqual(hub.calls.at(-1).patch, { set: {}, reset: ['presence.session_ttl'] });
});

test('invalid input stays beside its field, blocks Save and keeps valid edits', async () => {
  const { hub, store, st } = await ready();
  store.edit('events.refresh_interval', '10m');
  store.edit('agents.default_max_depth', '99');
  assert.match(store.errors()['agents.default_max_depth'], /between 1 and 64/);
  await store.save();
  assert.equal(hub.calls.filter(c => c.op !== 'get').length, 0);
  assert.equal(store.takeFocus(), 'agents.default_max_depth');
  store.setView('policy');
  assert.ok(store.errors()['agents.default_max_depth'], 'errors carry across views');
  assert.equal(store.input('agents.default_max_depth'), '99', 'typed text is kept');
  assert.equal(store.value('events.refresh_interval'), 600);
  assert.equal(st.view, 'policy');
});

test('a cross-field bound: default TTL cannot pass the maximum expiry', async () => {
  const { store } = await ready();
  store.edit('agents.default_ttl', '7d');
  assert.equal(store.errors()['agents.default_ttl'], undefined);
  store.state.rows = store.state.rows.map(r => (r.key === 'agents.max_expiry' ? { ...r, value: 172800 } : r));
  assert.match(store.errors()['agents.default_ttl'], /maximum message expiry \(2 d\)/);
});

test('server validation errors land on the field and keep the draft', async () => {
  const { hub, store, st } = await ready();
  hub.fail = { patch: Object.assign(new Error('Invalid preference value.'), { status: 422, code: 'prefs_invalid_value', details: { field: 'agents.default_ttl', constraint: 'max_expiry' } }) };
  store.edit('agents.default_ttl', '2d');
  store.edit('events.refresh_interval', '10m');
  await store.save();
  assert.match(store.errors()['agents.default_ttl'], /maximum message expiry/);
  assert.equal(st.focus, 'agents.default_ttl');
  assert.match(st.saveError.text, /Nothing was changed/);
  assert.deepEqual(store.dirtyKeys(), ['agents.default_ttl', 'events.refresh_interval']);
  store.setView('policy');
  assert.ok(store.errors()['agents.default_ttl'], 'the hub error survives a view switch');
  store.edit('agents.default_ttl', '1d1h');
  assert.equal(store.errors()['agents.default_ttl'], undefined, 'editing clears it');
});

test('a failed save (storage or network) keeps every edit and allows a retry', async () => {
  const { hub, store, st } = await ready();
  store.edit('events.refresh_interval', '10m');
  hub.fail = { patch: Object.assign(new Error('Preference storage is unavailable.'), { status: 503, code: 'prefs_storage_unavailable', details: {} }) };
  await store.save();
  assert.equal(st.busy, '');
  assert.match(st.saveError.text, /Preference storage is unavailable.*edits are kept/);
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval']);
  hub.fail = { patch: Object.assign(new Error('hub unreachable'), { status: 0, code: 'network', details: {} }) };
  const gets = hub.calls.filter(c => c.op === 'get').length;
  await store.save();
  // A lost reply may hide a committed write: say so, and re-read the hub.
  assert.match(st.saveError.text, /may or may not have been saved\. Your edits are kept/);
  await new Promise(r => setTimeout(r, 0));
  assert.equal(hub.calls.filter(c => c.op === 'get').length, gets + 1, 'the page re-reads after a lost reply');
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval']);
  await store.save();
  assert.equal(st.saveError, null);
  assert.equal(store.source('events.refresh_interval'), 'stored');
});

test('saving disables repeat submission', async () => {
  const { hub, store, st } = await ready();
  let release;
  const patch = hub.api.patchPrefs;
  hub.api.patchPrefs = (...a) => new Promise(resolve => { release = () => resolve(patch(...a)); });
  store.edit('events.refresh_interval', '10m');
  const first = store.save();
  await flush();
  assert.equal(st.busy, 'saving');
  await store.save();
  store.edit('events.refresh_interval', '20m');
  store.undo();
  assert.equal(store.value('events.refresh_interval'), 600, 'no edits or undo mid-save');
  release();
  await first;
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 1);
});

// ---------- 412: draft preserved, manual reconciliation ----------

test('412 keeps the draft, shows the latest values and needs another Save', async () => {
  const { hub, store, st } = await ready();
  store.edit('events.refresh_interval', '10m');
  store.edit('agents.default_max_depth', '20');
  hub.competing('events.refresh_interval', 1200);
  await store.save();
  assert.equal(st.newer.reason, 'conflict');
  assert.deepEqual(st.newer.changed, ['events.refresh_interval']);
  assert.deepEqual(store.dirtyKeys(), ['agents.default_max_depth', 'events.refresh_interval']);
  assert.equal(store.value('events.refresh_interval'), 600, 'never overwritten automatically');
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 1);
  store.setView('policy');
  assert.equal(st.newer.reason, 'conflict', 'the conflict survives a view switch');
  store.adoptNewer();
  assert.equal(st.newer, null);
  assert.equal(st.etag, '"prefs-4-g1"');
  assert.equal(row(st, 'events.refresh_interval').value, 1200);
  assert.deepEqual(store.dirtyKeys(), ['agents.default_max_depth', 'events.refresh_interval'], 'edits on top of the latest');
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 1, 'adopting does not save');
  await store.save();
  assert.equal(hub.calls.filter(c => c.op === 'patch').at(-1).tag, '"prefs-4-g1"');
  assert.equal(row(st, 'events.refresh_interval').value, 600);
});

test('Undo during a conflict takes the latest values and drops the draft', async () => {
  const { hub, store, st } = await ready();
  store.edit('events.refresh_interval', '10m');
  hub.competing('events.refresh_interval', 1200);
  await store.save();
  store.undo();
  assert.equal(st.newer, null);
  assert.equal(row(st, 'events.refresh_interval').value, 1200);
  assert.deepEqual(store.dirtyKeys(), []);
});

test('focus refresh: 304 is quiet; a clean page takes new values; a dirty page keeps edits and shows a notice', async () => {
  const { hub, store, st } = await ready();
  await store.refresh();
  assert.equal(hub.calls.at(-1).tag, '"prefs-3-g1"', 'conditional GET');
  assert.equal(st.newer, null);
  hub.competing('presence.default_ttl', 7200);
  await store.refresh();
  assert.equal(row(st, 'presence.default_ttl').value, 7200);
  store.edit('events.refresh_interval', '5m');
  hub.competing('agents.default_max_depth', 10);
  await store.refresh();
  assert.equal(st.newer.reason, 'update');
  assert.deepEqual(st.newer.changed, ['agents.default_max_depth']);
  assert.equal(row(st, 'agents.default_max_depth').value, 32, 'not replaced under typed edits');
  assert.equal(store.input('events.refresh_interval'), '5m');
});

// ---------- dangerous changes ----------

test('dangerous cancel writes nothing and keeps the draft; confirm sends the token once', async () => {
  const { hub, store, st } = await ready();
  store.edit('presence.default_ttl', '30m');
  store.edit('events.refresh_interval', '10m');
  await store.save();
  assert.equal(st.busy, 'confirming');
  assert.equal(st.confirm.preview.risks[0].code, 'presence_shortened');
  assert.deepEqual(st.confirm.keys, ['events.refresh_interval', 'presence.default_ttl']);
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0, 'preview before any write');
  store.setView('policy');
  assert.ok(st.confirm, 'the confirmation survives a view switch');
  store.cancelConfirm();
  assert.equal(st.confirm, null);
  assert.equal(st.busy, '');
  assert.match(st.announce.text, /Nothing was saved/);
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0);
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval', 'presence.default_ttl']);

  await store.save();
  const token = st.confirm.preview.confirm_token;
  await store.confirm();
  const patches = hub.calls.filter(c => c.op === 'patch');
  assert.equal(patches.length, 1);
  assert.deepEqual(patches[0].patch.confirm, [token]);
  assert.equal(st.confirm, null);
  assert.equal(store.source('presence.default_ttl'), 'stored');
});

test('a 409 confirmation_required from PATCH falls back to preview and the dialog', async () => {
  // The hub names a risk this page knows but did not predict: the row it
  // drew lists no danger.
  const pre = rows().map(r => (r.key === 'presence.default_ttl' ? { ...r, danger: [] } : r));
  const { hub, store, st } = await ready({ hub: { preferences: pre } });
  store.edit('presence.default_ttl', '30m');
  await store.save();
  assert.deepEqual(hub.calls.filter(c => c.op !== 'get').map(c => c.op), ['patch', 'preview']);
  assert.equal(st.busy, 'confirming');
  assert.equal(st.confirm.preview.risks[0].code, 'presence_shortened');
  await store.confirm();
  assert.equal(store.source('presence.default_ttl'), 'stored');
});

// ---------- risks this page cannot describe are refused, never confirmed ----------

const confirming = hub => hub.calls.filter(c => c.op === 'patch' && c.patch.confirm);
const futureRisk = key => ({ code: 'future_risk', keys: [key], message: 'A risk this page has no copy for.' });

test('a preview naming a risk this page cannot describe is refused and its token never sent', async () => {
  const { hub, store, st } = await ready();
  hub.extraRisks = patch => (patch.set && patch.set['events.refresh_interval'] ? [futureRisk('events.refresh_interval')] : []);
  store.edit('events.refresh_interval', '10m');
  await store.save();
  assert.deepEqual(hub.calls.filter(c => c.op !== 'get').map(c => c.op), ['patch', 'preview'], 'the unconfirmed PATCH is refused by the hub, then previewed');
  assert.equal(st.confirm, null, 'no confirmation is offered');
  assert.equal(st.busy, '');
  assert.match(st.saveError.text, /cannot describe \(future_risk\)/);
  assert.match(st.saveError.text, /Nothing was saved; your edits are kept/);
  assert.match(st.announce.text, /cannot describe/);
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval'], 'the draft is kept');
  await store.confirm();
  assert.equal(confirming(hub).length, 0, 'no confirming PATCH');
  assert.equal(row(st, 'events.refresh_interval').source, 'default');
});

test('one unknown risk among known ones refuses the whole proposal', async () => {
  const { hub, store, st } = await ready();
  hub.extraRisks = patch => (patch.set && patch.set['presence.default_ttl'] ? [futureRisk('presence.default_ttl')] : []);
  store.edit('presence.default_ttl', '30m');
  await store.save();
  assert.deepEqual(hub.calls.filter(c => c.op !== 'get').map(c => c.op), ['preview'], 'previewed before any write');
  assert.equal(st.confirm, null);
  assert.match(st.saveError.text, /\(future_risk\)/);
  assert.doesNotMatch(st.saveError.text, /presence_shortened/, 'only the unknown risk is named');
  await store.confirm();
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0, 'nothing is written');
  assert.deepEqual(store.dirtyKeys(), ['presence.default_ttl']);
});

test('a risk with no code, an empty or non-string code, or no object at all is refused', async () => {
  const bad = [{ keys: ['presence.default_ttl'], message: 'unnamed' }, { code: '', keys: [] }, { code: 42 }, null, 'presence_shortened'];
  for (const risk of bad) {
    const { hub, store, st } = await ready();
    hub.extraRisks = () => [risk];
    store.edit('presence.default_ttl', '30m');
    await store.save();
    const what = JSON.stringify(risk);
    assert.equal(st.confirm, null, what);
    assert.equal(st.busy, '', what);
    assert.match(st.saveError.text, /cannot describe \(unnamed\)/, what);
    await store.confirm();
    assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0, what);
    assert.deepEqual(store.dirtyKeys(), ['presence.default_ttl'], what);
  }
});

test('a preview with risks but no token is refused', async () => {
  const { hub, store, st } = await ready();
  const preview = hub.api.previewPrefs;
  hub.api.previewPrefs = async (...a) => ({ ...(await preview(...a)), confirm_token: '' });
  store.edit('presence.default_ttl', '30m');
  await store.save();
  assert.equal(st.confirm, null);
  assert.match(st.saveError.text, /Not saved/);
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0);
  assert.deepEqual(store.dirtyKeys(), ['presence.default_ttl']);
});

test('confirm checks the risks again before redeeming the token', async () => {
  const { hub, store, st } = await ready();
  store.edit('presence.default_ttl', '30m');
  await store.save();
  assert.equal(st.busy, 'confirming');
  // However the held preview came to name an unknown risk, Confirm refuses it.
  st.confirm.preview.risks.push(futureRisk('presence.default_ttl'));
  await store.confirm();
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0, 'no confirming PATCH');
  assert.equal(st.confirm, null);
  assert.equal(st.busy, '');
  assert.match(st.saveError.text, /\(future_risk\)/);
  assert.deepEqual(store.dirtyKeys(), ['presence.default_ttl']);
});

test('an unknown risk in the apply-existing preview is refused too', async () => {
  const pre = rows({ installed: true }).map(r => (r.key === 'retention.archive_mode' ? { ...r, value: true, source: 'stored' } : r));
  const { hub, store, st } = await ready({ hub: { preferences: pre } });
  hub.extraRisks = patch => (patch.apply_existing ? [futureRisk('retention.archive_mode')] : []);
  store.edit('retention.archive_mode', false);
  await store.save();
  assert.equal(st.confirm.preview.risks[0].code, 'archive_disabled');
  await store.confirm({ applyExisting: true });
  assert.equal(hub.calls.filter(c => c.op === 'preview').length, 2);
  assert.equal(st.confirm, null);
  assert.match(st.saveError.text, /\(future_risk\)/);
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0);
  assert.deepEqual(store.dirtyKeys(), ['retention.archive_mode']);
});

test('a hub that demands a confirmation it never describes cannot loop', async () => {
  const { hub, store, st } = await ready();
  hub.fail = { sticky: true, patch: Object.assign(new Error('confirm'), { status: 409, code: 'prefs_confirmation_required', details: {} }) };
  store.edit('events.refresh_interval', '10m');
  await store.save();
  assert.deepEqual(hub.calls.filter(c => c.op !== 'get').map(c => c.op), ['patch', 'preview', 'patch']);
  assert.equal(st.busy, '');
  assert.match(st.saveError.text, /confirmation/);
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval']);
});

test('apply-existing is a separate choice with a second preview', async () => {
  const pre = rows({ installed: true }).map(r => (r.key === 'retention.archive_mode' ? { ...r, value: true, source: 'stored' } : r));
  const { hub, store, st } = await ready({ hub: { preferences: pre } });
  store.edit('retention.archive_mode', false);
  await store.save();
  assert.equal(st.confirm.preview.risks[0].code, 'archive_disabled');
  assert.equal(st.confirm.applyExisting, false, 'prospective-only by default');
  await store.confirm({ applyExisting: true });
  assert.equal(hub.calls.filter(c => c.op === 'preview').length, 2);
  assert.equal(hub.calls.at(-1).patch.apply_existing, true);
  assert.ok(st.confirm.preview.risks.some(r => r.code === 'retention_apply_existing'));
  await store.confirm();
  const p = hub.calls.filter(c => c.op === 'patch').at(-1).patch;
  assert.equal(p.apply_existing, true);
  assert.equal(p.confirm.length, 1);
});

test('a stale confirmation keeps the draft and asks for a new Save', async () => {
  const { hub, store, st } = await ready();
  store.edit('presence.default_ttl', '30m');
  await store.save();
  hub.fail = { patch: Object.assign(new Error('stale'), { status: 409, code: 'prefs_confirmation_stale', details: {} }) };
  await store.confirm();
  assert.equal(st.confirm, null);
  assert.match(st.saveError.text, /expired or no longer matches/);
  assert.deepEqual(store.dirtyKeys(), ['presence.default_ttl']);
});

test('no draft or preview token ever reaches storage', async () => {
  const storage = memStorage();
  const { store, st } = await ready({ storage });
  store.edit('presence.default_ttl', '30m');
  store.setView('policy');
  await store.save();
  const token = st.confirm.preview.confirm_token;
  store.setView('list');
  const stored = JSON.stringify([...storage.map.entries()]);
  assert.ok(!stored.includes(token));
  assert.ok(!stored.includes('30m') && !stored.includes('1800'));
  assert.deepEqual([...storage.map.keys()], [VIEW_KEY]);
  store.cancelConfirm();
  assert.equal(JSON.stringify(st).includes('tok:'), false, 'cancel drops the token');
});

// ---------- archive and the Policy lanes ----------

test('archive mode leaves message history dormant but acknowledged events keep pruning', async () => {
  const pre = rows({ installed: true }).map(r => (r.key === 'retention.history' ? { ...r, value: 7776000, source: 'stored' } : r));
  const { store } = await ready({ hub: { preferences: pre } });
  const [history, acked] = LANES;
  let h = laneView(store, history), a = laneView(store, acked);
  assert.equal(h.paused, false);
  assert.equal(h.text, '90 d, then deleted at the nightly sweep');
  store.edit('retention.archive_mode', true);
  h = laneView(store, history);
  a = laneView(store, acked);
  assert.equal(h.paused, true);
  assert.equal(h.seconds, Infinity);
  assert.match(h.text, /Forever while archive mode is on \(configured: 90 d\)/);
  assert.equal(a.paused, false, 'acknowledged events are not paused by archive mode');
  assert.equal(a.seconds, 604800);
  assert.match(a.text, /7 d, then pruned/);
  store.edit('retention.ack_events', '0');
  assert.equal(laneView(store, acked).text, 'Never pruned', 'zero keeps events too');
});

test('Policy lanes offer stops inside the hub bounds and show unavailable adapters as locked', async () => {
  const { store } = await ready();
  const files = laneView(store, LANES[2]);
  assert.equal(files.lock.kind, 'unavailable');
  assert.deepEqual(files.stops.map(s => s.value), ['1h', '6h', '12h', '1d', '7d', '30d', 'off']);
  const ttl = laneView(store, LANES[3]);
  assert.equal(ttl.lock, null);
  assert.ok(ttl.stops.every(s => s.value >= 60 && s.value <= 604800));
  assert.ok(ttl.stops.find(s => s.on).value === 86400);
  const rs = rows({ installed: true });
  const hist = laneStops(rs.find(r => r.key === 'retention.history'), 0);
  assert.equal(hist.at(-1).label, 'Forever');
  assert.ok(!hist.some(s => s.value === 3600), 'one hour is below the history minimum');
  store.edit('agents.default_ttl', '2h');
  assert.ok(laneView(store, LANES[3]).stops.some(s => s.value === 7200 && s.on), 'an off-preset value gets its own stop');
});

test('a Policy lane keeps the saved off-preset stop while another is picked', async () => {
  const pre = rows().map(r => (r.key === 'agents.default_ttl' ? { ...r, value: 5400, source: 'stored' } : r));
  const { store } = await ready({ hub: { preferences: pre } });
  store.edit('agents.default_ttl', '2h');
  const stops = laneView(store, LANES[3]).stops.map(s => s.value);
  assert.ok(stops.includes(5400), 'the saved 1h30m stays reachable');
  assert.ok(stops.includes(7200));
});

test('Discard after a conflict whose re-read failed reloads instead of throwing', async () => {
  const { hub, store, st } = await ready();
  store.edit('events.refresh_interval', '10m');
  hub.competing('agents.default_max_depth', 20);
  hub.fail = { get: Object.assign(new Error('down'), { status: 0, code: 'network' }) };
  await store.save();
  assert.equal(st.newer && st.newer.reason, 'conflict');
  hub.fail = null;
  store.undo();
  await new Promise(r => setTimeout(r, 0));
  assert.equal(st.newer, null);
  assert.deepEqual(store.dirtyKeys(), []);
  assert.equal(store.value('agents.default_max_depth'), 20, 'the latest values were read');
});

test('a failed background reload says so without losing edits', async () => {
  const { hub, store, st } = await ready();
  store.edit('events.refresh_interval', '10m');
  hub.fail = { get: Object.assign(new Error('down'), { status: 0, code: 'network' }) };
  await store.refresh();
  assert.equal(st.saveError.code, 'reload');
  assert.match(st.saveError.text, /Could not reload settings/);
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval']);
});

// ---------- loading and the List filter ----------

test('load failures explain themselves', async () => {
  assert.equal(describeLoad({ status: 403 }).code, 'forbidden');
  assert.equal(describeLoad({ status: 404 }).code, 'missing');
  assert.equal(describeLoad({ status: 0 }).code, 'network');
  const hub = fakeHub();
  hub.fail = { get: Object.assign(new Error('nope'), { status: 403, code: 'forbidden' }) };
  const store = createSettingsStore({ api: hub.api, storage: memStorage() });
  assert.equal(store.state.phase, 'loading');
  await store.load();
  assert.equal(store.state.phase, 'error');
  assert.equal(store.state.loadError.code, 'forbidden');
});

test('List search and the All / Customised / Flags filter', async () => {
  const pre = rows().map(r => (r.key === 'events.refresh_interval' ? { ...r, source: 'flag', origin: 'VECNET_EVENTS_REFRESH_INTERVAL', value: 900 } : r.key === 'agents.default_max_depth' ? { ...r, source: 'stored', value: 12 } : r));
  const { store } = await ready({ hub: { preferences: pre } });
  store.setFilter({ only: 'flags' });
  assert.deepEqual(visibleRows(store).map(r => r.key), ['events.refresh_interval']);
  store.setFilter({ only: 'custom' });
  store.edit('rates.sends_per_minute', '61');
  assert.deepEqual(visibleRows(store).map(r => r.key), ['agents.default_max_depth', 'rates.sends_per_minute']);
  store.setFilter({ only: 'all', q: 'PRESENCE' });
  assert.deepEqual(visibleRows(store).map(r => r.key), ['presence.default_ttl', 'presence.send_warning_after', 'presence.session_ttl']);
  store.setFilter({ q: 'nothing like this' });
  assert.deepEqual(visibleRows(store), []);
});

test('Show reveals an invalid field that the List search or Flags filter hides', async () => {
  const pre = rows().map(r => (r.key === 'events.refresh_interval' ? { ...r, source: 'flag', origin: 'VECNET_EVENTS_REFRESH_INTERVAL', value: 900 } : r));
  const { store, st } = await ready({ hub: { preferences: pre } });
  const key = 'agents.default_max_depth';
  const shown = () => visibleRows(store).some(r => r.key === key);
  store.edit(key, '99');
  for (const filter of [{ q: 'presence' }, { only: 'flags' }, { q: 'refresh', only: 'flags' }]) {
    const what = JSON.stringify(filter);
    store.setFilter({ q: '', only: 'all', ...filter });
    assert.equal(shown(), false, `${what} hides the row`);
    store.showField(key);
    assert.equal(store.takeFocus(), key, what);
    assert.equal(shown(), true, `${what}: Show reveals the row`);
    assert.equal(st.q, '', what);
    assert.equal(st.only, 'all', what);
  }
  // Save's own focus request reveals the field the same way.
  store.setFilter({ only: 'flags' });
  await store.save();
  assert.equal(store.takeFocus(), key);
  assert.equal(shown(), true, 'Save reveals the field it asks to fix');
  // A filter that already shows the field is left alone.
  store.setFilter({ q: 'depth', only: 'all' });
  store.showField(key);
  store.takeFocus();
  assert.equal(st.q, 'depth');
  // Policy draws every row; its filter state is not the List's to clear.
  store.setFilter({ q: 'presence' });
  store.setView('policy');
  store.showField(key);
  assert.equal(store.takeFocus(), key);
  assert.equal(st.q, 'presence');
});

// ---------- lifecycle and appearance ----------

test('destroy aborts pending requests and ignores late replies', async () => {
  const hub = fakeHub();
  let signal;
  hub.api.prefs = opts => { signal = opts.signal; return new Promise(() => {}); };
  const store = createSettingsStore({ api: hub.api, storage: memStorage() });
  let renders = 0;
  store.subscribe(() => { renders++; });
  store.load();
  const before = renders;
  store.destroy();
  assert.equal(signal.aborted, true);
  store.edit('x', 1);
  assert.equal(renders, before);
});

test('appearance has its own Apply and Undo and never calls the hub', async () => {
  const look = { theme: 'system', density: 'comfortable' };
  const listeners = new Set();
  let persisted = true;
  const appearance = {
    get: () => ({ ...look }),
    set: a => { Object.assign(look, a); return { persisted }; },
    subscribe: fn => { listeners.add(fn); return () => listeners.delete(fn); },
  };
  const { hub, store, st } = await ready({ appearance });
  const calls = hub.calls.length;
  store.edit('events.refresh_interval', '10m');
  store.setAppearance('theme', 'light');
  store.setAppearance('density', 'roomy');
  assert.deepEqual(st.appearance.draft, { theme: 'light', density: 'comfortable' });
  assert.equal(store.appearanceDirty(), true);
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval'], 'appearance is not a hub change');
  store.undoAppearance();
  assert.equal(st.appearance.draft.theme, 'system');
  store.setAppearance('density', 'compact');
  store.applyAppearance();
  assert.deepEqual(look, { theme: 'system', density: 'compact' });
  assert.equal(store.appearanceDirty(), false);
  assert.match(st.announce.text, /this browser only/);
  assert.equal(hub.calls.length, calls, 'no hub request from appearance');
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval'], 'the hub draft is untouched');
  persisted = false;
  store.setAppearance('theme', 'dark');
  store.applyAppearance();
  assert.match(st.appearance.note, /blocks storage/);
  // Another tab changed the look: a clean draft follows it.
  for (const fn of listeners) fn({ theme: 'light', density: 'compact' });
  assert.deepEqual(st.appearance.draft, { theme: 'light', density: 'compact' });
  store.setAppearance('theme', 'dark');
  for (const fn of listeners) fn({ theme: 'system', density: 'compact' });
  assert.equal(st.appearance.draft.theme, 'dark', 'a pending local choice is kept');
  assert.equal(st.appearance.saved.theme, 'system');
});

// ---------- blob retention, hosted off and storage (R2 blobs Task 7) ----------

const GiB = 2 ** 30;
const r2Rows = (patch = {}) => rows({ installed: true }).map(r => (r.key === 'blobs.expiry' ? { ...r, ...patch } : r));
const storage = (used, limit = 5 * GiB, reserved = 0) => {
  [used, limit, reserved] = [used, limit, reserved].map(Math.round);
  return { retained_bytes: used - reserved, reserved_bytes: reserved, used_bytes: used, limit_bytes: limit };
};

test('blob expiry: all six durations and off, the built-in 1 day default and its source', async () => {
  const { st, store } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB) } });
  const r = row(st, 'blobs.expiry');
  assert.deepEqual(r.allowed_values, ['1h', '6h', '12h', '1d', '7d', '30d', 'off']);
  assert.deepEqual(r.allowed_values.map(v => formatValue(r, v)), ['1 hour', '6 hours', '12 hours', '1 day', '7 days', '30 days', 'Off (keep files)']);
  assert.equal(store.value('blobs.expiry'), '1d');
  assert.equal(store.source('blobs.expiry'), 'default');
  assert.equal(lockOf(r), null);
  assert.match(BLOB_NEW_ONLY, /new uploads only/i);
  assert.match(BLOB_NEW_ONLY, /keep/i);
});

test('blob expiry: a duration saves without confirmation and reloads as stored; an invalid value never saves', async () => {
  const { hub, st, store } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB) } });
  store.edit('blobs.expiry', '7d');
  await store.save();
  const patches = hub.calls.filter(c => c.op === 'patch');
  assert.deepEqual(patches.map(c => c.patch), [{ set: { 'blobs.expiry': '7d' }, reset: [] }]);
  // The row is danger-flagged, so the page asks first; a duration carries no
  // risk, so no confirmation opens and no token is sent.
  assert.equal(st.confirm, null, 'durations need no cost confirmation');
  assert.equal(patches[0].patch.confirm, undefined);
  assert.equal(store.source('blobs.expiry'), 'stored');
  // A fresh page reads the saved value back.
  const again = createSettingsStore({ api: hub.api, storage: memStorage(), appearance: null });
  await again.load();
  assert.equal(again.value('blobs.expiry'), '7d');
  assert.equal(again.source('blobs.expiry'), 'stored');
  store.edit('blobs.expiry', '2d');
  assert.ok(store.errors()['blobs.expiry']);
  await store.save();
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 1);
  assert.equal(st.focus, 'blobs.expiry');
});

test('blob expiry off: preview with the hub storage figures; cancel writes nothing; confirm sends the token once', async () => {
  const { hub, st, store } = await ready({ hub: { preferences: r2Rows(), storage: storage(3 * GiB, 5 * GiB, GiB / 2) } });
  store.edit('blobs.expiry', 'off');
  await store.save();
  assert.equal(st.busy, 'confirming');
  assert.equal(st.confirm.preview.risks[0].code, 'blob_expiry_off');
  assert.deepEqual(st.confirm.preview.impact.storage, storage(3 * GiB, 5 * GiB, GiB / 2));
  const text = blobOffText(st.confirm.preview.impact.storage);
  assert.match(text, /no automatic expiry/i);
  assert.match(text, /3\.2 GB used of a 5\.4 GB limit/);
  assert.match(text, /2\.7 GB retained/);
  assert.match(text, /537 MB reserved/);
  store.cancelConfirm();
  assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0);
  assert.equal(store.value('blobs.expiry'), 'off', 'the draft stays');
  await store.save();
  await store.confirm();
  const patches = hub.calls.filter(c => c.op === 'patch');
  assert.equal(patches.length, 1);
  assert.equal(patches[0].patch.confirm.length, 1);
  assert.equal(store.source('blobs.expiry'), 'stored');
  assert.equal(row(st, 'blobs.expiry').value, 'off');
});

test('blob expiry off: the fixture refuses an unconfirmed write as the hub does, and a failed confirm keeps the draft', async () => {
  const { hub, st, store } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB) } });
  // Fixture contract only: it mirrors the hub, whose server-side enforcement
  // for every writer is tested in Go (internal/hub/preferences_preview_test.go,
  // internal/httpapi/prefs_test.go). The page itself never sends this.
  const direct = await hub.api.patchPrefs({ set: { 'blobs.expiry': 'off' }, reset: [] }, `"prefs-${hub.revision}-g1"`).catch(e => e);
  assert.equal(direct.code, 'prefs_confirmation_required');
  store.edit('blobs.expiry', 'off');
  await store.save();
  hub.fail = { patch: Object.assign(new Error('x'), { status: 503, code: 'prefs_storage_unavailable', message: 'Preference storage is unavailable.' }) };
  await store.confirm();
  assert.equal(st.confirm, null);
  assert.match(st.saveError.text, /Not saved/);
  assert.equal(store.value('blobs.expiry'), 'off');
  assert.deepEqual(store.dirtyKeys(), ['blobs.expiry']);
});

test('blob expiry: a concurrent change is a conflict that keeps the draft; a clean page takes the change', async () => {
  const { hub, st, store } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB) } });
  store.edit('blobs.expiry', '12h');
  hub.competing('blobs.expiry', '30d');
  await store.save();
  assert.equal(st.newer.reason, 'conflict');
  assert.deepEqual(st.newer.changed, ['blobs.expiry']);
  assert.equal(store.value('blobs.expiry'), '12h');
  store.undo();
  assert.equal(store.value('blobs.expiry'), '30d');
  // Change notification: a re-read (focus, visibility, Reload) adopts it.
  hub.competing('blobs.expiry', '6h');
  await store.refresh();
  assert.equal(store.value('blobs.expiry'), '6h');
  assert.match(st.announce.text, /reloaded/);
});

test('blob expiry: a flag-set value is read-only; a non-operator gets no setting controls', async () => {
  const flagged = r2Rows({ source: 'flag', origin: 'VECNET_BLOBS_EXPIRY', value: '7d' });
  const { store, st } = await ready({ hub: { preferences: flagged } });
  assert.equal(lockOf(row(st, 'blobs.expiry')).kind, 'flag');
  store.edit('blobs.expiry', '1h');
  assert.deepEqual(store.dirtyKeys(), []);
  const hub = fakeHub();
  hub.fail = { get: Object.assign(new Error('x'), { status: 403, code: 'forbidden', message: 'Operator credential required.' }) };
  const peer = createSettingsStore({ api: hub.api, storage: memStorage(), appearance: null });
  await peer.load();
  assert.equal(peer.state.phase, 'error');
  assert.equal(peer.state.rows.length, 0, 'no rows, so no controls');
  assert.match(peer.state.loadError.text, /Only the operator/);
  assert.equal(peer.state.storage, null);
});

test('storage: retained, reserved, used and limit from the snapshot; near-limit and full states', async () => {
  const { st, store, hub } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB, 5 * GiB, GiB / 4) } });
  assert.deepEqual(st.storage, storage(GiB, 5 * GiB, GiB / 4));
  let v = storageView(st.storage);
  assert.equal(v.tone, 'ok');
  assert.equal(v.percent, 20);
  assert.match(v.text, /1\.1 GB used of 5\.4 GB/);
  assert.deepEqual(v.parts.map(p => p.label), ['Retained', 'Reserved', 'Used', 'Limit']);
  v = storageView(storage(4.7 * GiB, 5 * GiB));
  assert.equal(v.tone, 'near');
  assert.match(v.note, /near/i);
  v = storageView(storage(5 * GiB, 5 * GiB));
  assert.equal(v.tone, 'full');
  assert.match(v.note, /new uploads are refused/i);
  assert.match(v.note, /nothing already uploaded is deleted/i);
  v = storageView(storage(6 * GiB, 5 * GiB));
  assert.equal(v.tone, 'full');
  assert.equal(v.percent, 100, 'the bar never overflows');
  assert.match(v.note, /over the limit/i);
  assert.match(v.text, /, over by 1\.1 GB$/);
  // A refresh after a transition shows the new figures.
  hub.storage = storage(2 * GiB);
  hub.revision++;
  await store.refresh();
  assert.equal(st.storage.used_bytes, 2 * GiB);
  // Malformed figures are dropped, not drawn.
  for (const bad of [null, {}, { retained_bytes: -1, reserved_bytes: 0, used_bytes: 0, limit_bytes: 5 }, { retained_bytes: 1, reserved_bytes: 1, used_bytes: 2, limit_bytes: 0 }, { retained_bytes: '1', reserved_bytes: 0, used_bytes: 1, limit_bytes: 9 }]) {
    assert.equal(validStorage(bad), null, JSON.stringify(bad));
  }
});

test('storage meter updates on refresh when blob usage changes but the preference ETag does not', async () => {
  // Focus, visibility and a reconnect all re-read through refresh(). The prefs
  // ETag is the preference revision plus the bootstrap digest, so a 304 is
  // normal while retained/reserved/used move. The meter has to follow anyway.
  const { hub, st, store } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB, 5 * GiB, GiB / 4) } });
  const etag = st.etag, revision = hub.revision;
  store.edit('events.refresh_interval', '10m');
  const next = storage(2 * GiB, 5 * GiB, GiB / 8);
  hub.storage = next;
  await store.refresh();
  assert.equal(hub.revision, revision, 'preference revision stays put');
  assert.ok(hub.calls.some(c => c.op === 'get' && c.tag === etag), 'prefs re-read is conditional');
  assert.ok(hub.calls.some(c => c.op === 'storage'), 'the meter reads blob storage on its own');
  assert.equal(st.etag, etag, 'the prefs ETag is kept');
  assert.equal(store.input('events.refresh_interval'), '10m');
  assert.deepEqual(store.dirtyKeys(), ['events.refresh_interval']);
  assert.equal(st.confirm, null);
  assert.equal(st.newer, null);
  assert.deepEqual(st.storage, next);
  assert.equal(storageView(st.storage).tone, 'ok');
});

test('a failed storage refresh shows unavailable and leaves the prefs ETag, draft and confirmation', async () => {
  const { hub, st, store } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB) } });
  const etag = st.etag;
  store.edit('blobs.expiry', 'off');
  await store.save();
  assert.equal(st.busy, 'confirming');
  const confirm = st.confirm;
  const gets = hub.calls.filter(c => c.op === 'get').length;
  hub.storage = storage(3 * GiB);
  hub.fail = { storage: Object.assign(new Error('storage unavailable'), { status: 503, code: 'blob_unavailable' }), sticky: true };
  await store.refresh();
  assert.equal(st.etag, etag);
  assert.equal(st.confirm, confirm, 'the open confirmation is the same one');
  assert.equal(st.busy, 'confirming');
  assert.equal(store.value('blobs.expiry'), 'off');
  assert.deepEqual(store.dirtyKeys(), ['blobs.expiry']);
  assert.equal(hub.calls.filter(c => c.op === 'get').length, gets, 'a busy page does not re-read prefs');
  assert.equal(st.storage.unavailable, true);
  const v = storageView(st.storage);
  assert.equal(v.unavailable, true);
  assert.equal(v.tone, 'unavailable');
  assert.match(v.text, /unavailable/i);
});

// A document large enough for the mounted Settings screen. Installed only
// around that test; other files keep their own document shim.
class StEl {
  constructor(tag) {
    this.tag = tag;
    this.attrs = {};
    this.kids = [];
    this.className = '';
    this.on = {};
    this.id = '';
    this.value = '';
    this.disabled = false;
    this.checked = false;
    this.open = false;
    this.hidden = false;
    this.parent = null;
    this.style = {};
    this.dataset = {};
  }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) {
    const s = String(v);
    this.attrs[k] = s;
    if (k === 'id') this.id = s;
    if (k === 'class') this.className = s;
    if (k === 'disabled') this.disabled = true;
  }
  getAttribute(k) { return Object.hasOwn(this.attrs, k) ? this.attrs[k] : null; }
  addEventListener(type, fn) { (this.on[type] ||= []).push(fn); }
  append(...xs) {
    for (const x of xs) {
      if (x === null || x === undefined || x === false) continue;
      if (typeof x !== 'string') x.parent = this;
      this.kids.push(x);
    }
  }
  replaceChildren(...xs) {
    for (const k of this.kids) if (k && k.parent === this) k.parent = null;
    this.kids = [];
    this.append(...xs);
  }
  remove() {
    const p = this.parent;
    if (!p) return;
    if (this.contains(document.activeElement)) document.activeElement = document.body;
    p.kids = p.kids.filter(k => k !== this);
    this.parent = null;
  }
  get children() { return this.kids.filter(k => typeof k !== 'string'); }
  contains(node) {
    if (!node) return false;
    if (node === this) return true;
    return this.kids.some(k => k === node || (k && k.contains && k.contains(node)));
  }
  querySelector(sel) {
    const acc = [];
    const walk = el => {
      if (!(el instanceof StEl)) return;
      acc.push(el);
      for (const k of el.kids) walk(k);
    };
    for (const k of this.kids) walk(k);
    const cls = sel.startsWith('.') && !sel.includes(' ') && !sel.includes('[') ? sel.slice(1) : '';
    if (cls) return acc.find(el => el.className.split(/\s+/).includes(cls)) || null;
    const attr = sel.match(/^\[([^\s=~]+)="(.*)"\]$/);
    if (attr) {
      const val = attr[2].replace(/\\(.)/g, '$1');
      return acc.find(el => el.attrs[attr[1]] === val) || null;
    }
    return null;
  }
  before(...nodes) { this.insert(nodes.flat().filter(n => n != null), 0); }
  after(...nodes) { this.insert(nodes.flat().filter(n => n != null), 1); }
  insert(nodes, offset) {
    const p = this.parent;
    if (!p) throw new Error('insert without parent');
    const i = p.kids.indexOf(this);
    for (const n of nodes) if (typeof n !== 'string') n.parent = p;
    p.kids.splice(i + offset, 0, ...nodes);
  }
  replaceWith(node) {
    const p = this.parent;
    if (!p) return;
    const i = p.kids.indexOf(this);
    if (typeof node !== 'string') node.parent = p;
    p.kids.splice(i, 1, node);
    this.parent = null;
  }
  focus() { document.activeElement = this; }
  scrollIntoView() {}
  showModal() { this.open = true; }
  close() {
    if (!this.open) return;
    this.open = false;
    for (const fn of this.on.close || []) fn();
  }
  classList = {
    add: (...xs) => {
      const have = new Set(this.className.split(/\s+/).filter(Boolean));
      for (const x of xs) have.add(x);
      this.className = [...have].join(' ');
    },
    remove: (...xs) => {
      const drop = new Set(xs);
      this.className = this.className.split(/\s+/).filter(c => c && !drop.has(c)).join(' ');
    },
    toggle: (token, force) => {
      const has = this.className.split(/\s+/).includes(token);
      const on = force === undefined ? !has : !!force;
      if (on) this.classList.add(token); else this.classList.remove(token);
      return on;
    },
  };
}
const walkSt = (el, pred, out = []) => {
  if (el instanceof StEl) { if (pred(el)) out.push(el); for (const k of el.kids) walkSt(k, pred, out); }
  return out;
};

test('mounted settings keeps rows, meter and keyboard focus on storage-only refresh after reload', async () => {
  const prev = { document: globalThis.document, window: globalThis.window, CSS: globalThis.CSS, location: globalThis.location };
  const body = new StEl('body'), root = new StEl('div');
  const listeners = new Map(), conns = new Set();
  const add = (type, fn) => { if (!listeners.has(type)) listeners.set(type, new Set()); listeners.get(type).add(fn); };
  const remove = (type, fn) => listeners.get(type)?.delete(fn);
  globalThis.document = { body, activeElement: body, hidden: false, createElement: tag => new StEl(tag), getElementById: () => null, addEventListener: add, removeEventListener: remove };
  globalThis.window = { localStorage: memStorage(), addEventListener: add, removeEventListener: remove };
  globalThis.CSS = { escape: s => String(s).replace(/[^a-zA-Z0-9_-]/g, ch => '\\' + ch) };
  globalThis.location = { host: 'hub.test' };
  const hub = fakeHub({ preferences: r2Rows(), storage: storage(GiB) });
  const ctx = {
    api: hub.api, toast() {},
    store: { onConn(fn) { conns.add(fn); return () => conns.delete(fn); } },
    appearance: { get: () => ({ theme: 'system', density: 'comfortable' }), subscribe: () => () => {} },
  };
  const field = fk => walkSt(root, e => e.attrs['data-fk'] === fk)[0];
  const one = cls => walkSt(root, e => e.className.split(/\s+/).includes(cls))[0];
  let off = () => {};
  try {
    // Remount models a reload: it must discard the old connection listener.
    for (let reload = 0; reload < 2; reload++) {
      off = mountSettings(root, ctx);
      await flush();
      assert.equal(conns.size, 1);
      for (const view of ['list', 'policy']) {
        for (const fn of field('view-' + view).on.click || []) fn();
        const input = field(view === 'list' ? 'st-events-refresh-interval' : 'lane-blobs.expiry-1d');
        assert.ok(input, view + ' has a keyboard control');
        input.focus();
        if (view === 'list') { input.selectionStart = 1; input.selectionEnd = 3; }
        const row = walkSt(root, e => e.attrs['data-row'] === 'blobs.expiry')[0];
        const panel = one('st-sto'), meter = one('st-sto-bar'), page = root.children[0];
        const children = [...page.children];
        for (const [trigger, usage, tone] of [['live', 2 * GiB, 'ok'], ['focus', 4.75 * GiB, 'near'], ['visibilitychange', 6 * GiB, 'full'], ['live', null, 'unavailable'], ['focus', GiB, 'ok']]) {
          const marker = hub.calls.length;
          hub.storage = storage(usage || GiB);
          hub.fail = usage === null ? { storage: Object.assign(new Error('storage unavailable'), { status: 503 }) } : null;
          if (trigger === 'live') {
            for (const fn of conns) fn('reconnecting');
            await flush();
            assert.equal(hub.calls.length, marker, 'reconnecting does not refresh');
            for (const fn of conns) fn('live');
          } else for (const fn of listeners.get(trigger) || []) fn();
          await flush();
          await flush();
          assert.deepEqual(hub.calls.slice(marker).map(c => c.op), ['get', 'storage'], 'one refresh per ' + trigger);
          assert.ok(field(input.attrs['data-fk']) === input, view + ' keeps the focused node');
          assert.ok(document.activeElement === input, view + ' keeps keyboard focus');
          if (view === 'list') assert.deepEqual([input.selectionStart, input.selectionEnd], [1, 3], 'selection stays put');
          assert.ok(walkSt(root, e => e.attrs['data-row'] === 'blobs.expiry')[0] === row, 'row stays attached');
          assert.ok(one('st-sto') === panel, 'storage panel is updated in place');
          assert.ok(one('st-sto-bar') === meter, 'meter is updated in place');
          assert.ok(page.children.length === children.length && children.every((c, i) => c === page.children[i]), 'storage refresh does not rebuild the page');
          assert.equal(panel.attrs['data-storage'], tone);
          assert.equal(meter.attrs['aria-valuenow'], String(Math.min(100, Math.round((usage || 0) / (5 * GiB) * 100))));
          assert.equal(one('st-sto-n').hidden, tone === 'ok', 'storage warning follows the figures');
        }
      }
      off();
      off = () => {};
      assert.equal(conns.size, 0, 'reload removes the connection listener');
    }
  } finally {
    off();
    for (const [key, value] of Object.entries(prev)) globalThis[key] = value;
  }
});

test('mounted settings re-reads storage when the connection goes live, with no focus or visibility event', async () => {
  const hub = fakeHub({ preferences: r2Rows(), storage: storage(GiB, 5 * GiB, GiB / 4) });
  let previewToken;
  const previewPrefs = hub.api.previewPrefs;
  hub.api.previewPrefs = async (...args) => {
    const preview = await previewPrefs(...args);
    previewToken = preview.confirm_token;
    return preview;
  };
  const conns = [];
  const fired = { focus: 0, visibilitychange: 0 };
  const listeners = { focus: [], visibilitychange: [], beforeunload: [] };
  const listen = (type, fn) => {
    const wrapped = (...args) => { fired[type] = (fired[type] || 0) + 1; return fn(...args); };
    (listeners[type] ||= []).push(wrapped);
    return wrapped;
  };
  const unlisten = (type, fn) => {
    const bag = listeners[type] || [];
    const i = bag.indexOf(fn);
    if (i >= 0) bag.splice(i, 1);
  };
  const prevDoc = globalThis.document;
  const prevWin = globalThis.window;
  const prevCSS = globalThis.CSS;
  const prevLoc = globalThis.location;
  const body = new StEl('body');
  const doc = {
    hidden: true,
    activeElement: null,
    body,
    createElement: tag => new StEl(tag),
    addEventListener: listen,
    removeEventListener: unlisten,
    getElementById() { return null; },
  };
  const win = {
    localStorage: memStorage(),
    addEventListener: listen,
    removeEventListener: unlisten,
  };
  globalThis.document = doc;
  globalThis.window = win;
  globalThis.location = { host: 'hub.test' };
  if (!globalThis.CSS) globalThis.CSS = { escape: s => String(s).replace(/[^a-zA-Z0-9_-]/g, ch => '\\' + ch) };
  const root = new StEl('div');
  let off = () => {};
  try {
    const conn = {
      onConn(fn) {
        conns.push(fn);
        return () => { const i = conns.indexOf(fn); if (i >= 0) conns.splice(i, 1); };
      },
    };
    const ctx = {
      api: hub.api,
      store: conn,
      appearance: { get: () => ({ theme: 'system', density: 'comfortable' }), set: () => ({ persisted: true }), subscribe: () => () => {} },
      toast() {},
      setLeaveGuard() {},
    };
    off = mountSettings(root, ctx);
    await flush();
    assert.equal(conns.length, 1, 'the mount owns one connection subscription');
    const field = fk => walkSt(root, e => e.attrs['data-fk'] === fk)[0];
    const fire = (fk, type, value) => {
      const el = field(fk);
      assert.ok(el, fk);
      for (const fn of el.on[type] || []) fn({ target: { value } });
    };
    fire('st-events-refresh-interval', 'input', '10m');
    fire('st-blobs-expiry', 'change', 'off');
    for (const fn of field('save').on.click || []) fn();
    const dialog = () => walkSt(body, e => e.tag === 'dialog')[0];
    for (let i = 0; i < 10 && !(dialog() && dialog().open); i++) await flush();
    assert.equal(dialog().open, true, 'confirmation is open');
    assert.match(dialog().textContent, /Keep new attachments forever/);
    assert.equal(field('st-events-refresh-interval').value, '10m');
    assert.equal(field('st-blobs-expiry').value, 'off');
    const meter = () => walkSt(root, e => e.className.split(/\s+/).includes('st-sto-t'))[0];
    assert.match(meter().textContent, /1\.1 GB used of 5\.4 GB/);
    const gets = () => hub.calls.filter(c => c.op === 'get').length;
    const storages = () => hub.calls.filter(c => c.op === 'storage').length;
    const etag = `"prefs-${hub.revision}-g1"`;
    const token = previewToken;
    assert.ok(token, 'the hub issued a confirmation token');
    assert.ok(hub.calls.some(c => c.op === 'preview' && c.tag === etag), 'the confirmation is bound to the current ETag');
    const got = gets(), stored = storages(), revision = hub.revision;
    const marker = hub.calls.length;
    assert.equal(fired.focus, 0);
    assert.equal(fired.visibilitychange, 0);
    assert.ok(listeners.focus.length >= 1 && listeners.visibilitychange.length >= 1);
    hub.storage = storage(2 * GiB, 5 * GiB, GiB / 8);
    for (const fn of conns) fn('reconnecting');
    await flush();
    await flush();
    assert.equal(hub.calls.length, marker, 'reconnecting does not read');
    assert.match(meter().textContent, /1\.1 GB used of 5\.4 GB/);
    for (const fn of conns) fn('live');
    await flush();
    await flush();
    assert.deepEqual(hub.calls.slice(marker).map(c => c.op), ['storage'], 'a live transition reads the meter once and does not re-read prefs');
    assert.equal(gets(), got);
    assert.equal(storages(), stored + 1);
    assert.equal(hub.revision, revision);
    assert.match(meter().textContent, /2\.1 GB used of 5\.4 GB/);
    assert.equal(dialog().open, true, 'the confirmation stays open');
    assert.match(dialog().textContent, /Keep new attachments forever/);
    assert.equal(field('st-events-refresh-interval').value, '10m', 'the draft stays');
    assert.equal(field('st-blobs-expiry').value, 'off', 'the draft stays');
    assert.equal(fired.focus, 0, 'focus was not the trigger');
    assert.equal(fired.visibilitychange, 0, 'visibility was not the trigger');
    const confirm = walkSt(dialog(), e => e.attrs['data-cf'] === 'ok')[0];
    assert.ok(confirm && !confirm.disabled, 'the open dialog can still be confirmed');
    for (const fn of confirm.on.click || []) fn();
    await flush();
    const patches = hub.calls.filter(c => c.op === 'patch');
    assert.equal(patches.length, 1, 'confirmation saves once after reconnect');
    assert.equal(patches[0].tag, etag, 'patchPrefs receives the original If-Match ETag');
    assert.deepEqual(patches[0].patch.confirm, [token], 'patchPrefs receives the original preview token');
    assert.deepEqual(patches[0].patch.set, { 'blobs.expiry': 'off', 'events.refresh_interval': 600 });
    assert.equal(hub.calls.filter(c => c.op === 'preview').length, 1, 'reconnect and confirmation do not replace the preview');
    assert.equal(hub.revision, revision + 1, 'the hub accepted the confirmed draft');
    assert.equal(dialog().open, false, 'saving closes the confirmation');
    const beforeUnmount = hub.calls.length;
    off();
    off = () => {};
    assert.equal(conns.length, 0, 'unmount drops the connection subscription');
    for (const fn of conns) fn('live');
    await flush();
    assert.equal(hub.calls.length, beforeUnmount, 'a live event after unmount does not read again');
    off = mountSettings(root, ctx);
    await flush();
    assert.equal(conns.length, 1, 'remount owns exactly one active connection subscription');
    const remounted = hub.calls.length;
    const remountGets = gets(), remountStorages = storages();
    hub.storage = storage(3 * GiB, 5 * GiB, GiB / 8);
    for (const fn of conns) fn('reconnecting');
    await flush();
    assert.equal(hub.calls.length, remounted, 'reconnecting after remount does not read');
    for (const fn of conns) fn('live');
    await flush();
    await flush();
    assert.equal(conns.length, 1);
    assert.deepEqual(hub.calls.slice(remounted).map(c => c.op), ['get', 'storage'], 'the next live transition runs one refresh after remount');
    assert.equal(gets(), remountGets + 1);
    assert.equal(storages(), remountStorages + 1);
    assert.equal(hub.calls[remounted].tag, `"prefs-${hub.revision}-g1"`, 'the remounted refresh conditionally reads current prefs');
    assert.match(meter().textContent, /3\.2 GB used of 5\.4 GB/);
    assert.equal(fired.focus, 0);
    assert.equal(fired.visibilitychange, 0);
    off();
    off = () => {};
    assert.equal(conns.length, 0, 'the remounted subscription is also removed');
  } finally {
    off();
    globalThis.document = prevDoc;
    globalThis.window = prevWin;
    globalThis.CSS = prevCSS;
    globalThis.location = prevLoc;
  }
});


// ---------- last-reported acknowledged-event pruning gate ----------
const retentionRuntime = enabled => ({ event_retention_enabled: enabled });
const reportedReply = (enabled, etag = '"report"') => ({ status: 200, etag, body: {
  revision: 4, preferences: rows({ installed: true }),
  ...(enabled === undefined ? {} : { runtime: retentionRuntime(enabled) }),
} });
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

test('event retention report accepts booleans and treats missing or malformed metadata as not reported', async t => {
  for (const [name, runtime, want] of [
    ['false', retentionRuntime(false), false], ['true', retentionRuntime(true), true],
    ['legacy', undefined, null], ['null container', null, null], ['array', [], null],
    ['array with boolean', Object.assign([], retentionRuntime(true)), null],
    ['string container', 'true', null], ['number container', 1, null], ['missing member', {}, null],
    ['null member', retentionRuntime(null), null], ['string member', retentionRuntime('false'), null],
    ['number member', retentionRuntime(0), null], ['object member', retentionRuntime({}), null], ['array member', retentionRuntime([]), null],
  ]) await t.test(name, async () => {
    const { st } = await ready({ hub: { runtime } });
    assert.equal(st.phase, 'ready', 'older hubs remain readable');
    assert.equal(st.eventRetentionEnabled, want);
  });
  await t.test('accepted GET and PATCH replace a known report with not reported', async () => {
    const { hub, store, st } = await ready({ hub: { runtime: retentionRuntime(true) } });
    hub.runtime = undefined;
    hub.gen = 'g2';
    await store.refresh();
    assert.equal(st.eventRetentionEnabled, null);
    hub.runtime = retentionRuntime(false);
    store.edit('events.refresh_interval', '10m');
    await store.save();
    assert.equal(st.eventRetentionEnabled, false);
    hub.runtime = retentionRuntime('true');
    store.edit('events.refresh_interval', '15m');
    await store.save();
    assert.equal(st.eventRetentionEnabled, null);
    assert.equal(store.value('retention.ack_events'), 604800, 'duration cannot imply activation');
  });
});

test('event retention note appears in List and Policy without changing policy controls', async () => {
  const prev = { document: globalThis.document, window: globalThis.window, CSS: globalThis.CSS, location: globalThis.location };
  const body = new StEl('body'), root = new StEl('div'), listeners = new Map();
  const add = (type, fn) => { if (!listeners.has(type)) listeners.set(type, new Set()); listeners.get(type).add(fn); };
  const remove = (type, fn) => listeners.get(type)?.delete(fn);
  globalThis.document = { body, activeElement: body, hidden: false, createElement: tag => new StEl(tag), getElementById: () => null, addEventListener: add, removeEventListener: remove };
  globalThis.window = { localStorage: memStorage(), addEventListener: add, removeEventListener: remove };
  globalThis.CSS = { escape: s => String(s).replace(/[^a-zA-Z0-9_-]/g, ch => '\\' + ch) };
  globalThis.location = { host: 'hub.test' };
  const hub = fakeHub({ preferences: rows({ installed: true }), runtime: retentionRuntime(false) });
  const field = fk => walkSt(root, e => e.attrs['data-fk'] === fk)[0];
  const click = fk => { for (const fn of field(fk).on.click || []) fn(); };
  const off = mountSettings(root, { api: hub.api, toast() {}, store: { onConn: () => () => {} },
    appearance: { get: () => ({ theme: 'system', density: 'comfortable' }), subscribe: () => () => {} } });
  try {
    await flush();
    let tracks;
    for (const [enabled, attr, text] of [
      [false, 'disabled', 'Acknowledged-event pruning gate: disabled (last reported by hub). Saving this policy does not enable pruning.'],
      [true, 'enabled', 'Acknowledged-event pruning gate: enabled (last reported by hub).'],
      [undefined, 'not-reported', 'Acknowledged-event pruning gate: not reported by this hub.'],
    ]) {
      hub.runtime = enabled === undefined ? undefined : retentionRuntime(enabled);
      hub.gen += 'x';
      for (const fn of listeners.get('focus') || []) fn();
      await flush();
      for (const view of ['list', 'policy']) {
        click('view-' + view);
        const ack = walkSt(root, e => e.attrs['data-row'] === 'retention.ack_events')[0];
        const note = walkSt(ack, e => e.attrs['data-event-retention'] === attr)[0];
        assert.ok(note, view + ': note is beside the acknowledged-event row');
        assert.equal(note.textContent, text);
        assert.equal(walkSt(ack, e => e.className === 'st-src s-default').length, 1);
        assert.match(root.textContent, /Scheduled acknowledged-event pruning also requires its rollout gate; archive mode does not pause that policy\./);
        if (view === 'list') {
          assert.equal(field('st-retention-ack-events').disabled, false);
          const search = field('q');
          search.value = 'acknowledged';
          for (const fn of search.on.input || []) fn({ target: search });
          assert.ok(walkSt(root, e => e.attrs['data-event-retention'] === attr).length, 'filtered List retains the note');
          const current = field('q'); current.value = '';
          for (const fn of current.on.input || []) fn({ target: current });
        } else {
          const now = walkSt(ack, e => e.attrs.role === 'radio').map(e => [e.attrs['aria-label'], e.attrs['aria-checked'], e.disabled, e.style.left]);
          assert.ok(now.some(e => e[0].includes('Never pruned')));
          if (tracks) assert.deepEqual(now, tracks, 'gate reports do not alter tracks or controls');
          tracks = now;
          assert.match(ack.textContent, /7 d, then pruned if the pruning gate is enabled/);
        }
      }
    }
    assert.equal(hub.calls.filter(c => c.op !== 'get').length, 0, 'view/filter/report changes never write or preview');
    // The report stays informational through the existing confirmation flow.
    click('view-list');
    hub.runtime = retentionRuntime(false);
    for (const fn of listeners.get('focus') || []) fn();
    await flush();
    const input = field('st-presence-default_ttl') || field('st-presence-default-ttl');
    input.value = '10m'; for (const fn of input.on.input || []) fn({ target: input });
    click('save'); await flush();
    const cf = action => walkSt(body, e => e.attrs['data-cf'] === action)[0];
    assert.ok(cf('ok'), 'the existing preview still opens confirmation');
    for (const fn of cf('no').on.click || []) fn();
    assert.equal(hub.calls.filter(c => c.op === 'patch').length, 0, 'Cancel writes nothing');
    click('save'); await flush();
    for (const fn of cf('ok').on.click || []) fn();
    await flush();
    const patch = hub.calls.filter(c => c.op === 'patch');
    assert.equal(patch.length, 1);
    assert.deepEqual(patch[0].patch, { set: { 'presence.default_ttl': 600 }, reset: [], confirm: ['tok:{"set":{"presence.default_ttl":600},"reset":[],"apply_existing":false}'] });
    assert.equal(patch[0].tag, '"prefs-3-g1xxx"');
    assert.ok(walkSt(root, e => e.attrs['data-event-retention'] === 'disabled').length, 'saving does not enable pruning');
  } finally { off(); for (const [key, value] of Object.entries(prev)) globalThis[key] = value; }
});

test('event retention report follows accepted network generations, not cached conflict bodies', async t => {
  await t.test('dirty, same-tag, 304 and cached adoption', async () => {
    for (const adopt of ['undo', 'adoptNewer']) for (const latest of [false, undefined]) {
      const { hub, store, st } = await ready({ hub: { runtime: retentionRuntime(false) } });
      store.edit('events.refresh_interval', '10m');
      hub.runtime = retentionRuntime(true);
      hub.competing('agents.default_max_depth', 40);
      await store.refresh();
      assert.equal(st.eventRetentionEnabled, true, 'dirty refresh reports immediately');
      const tag = st.etag;
      hub.api.prefs = async () => reportedReply(latest, tag);
      const changes = []; store.subscribe((_, change) => changes.push(change));
      await store.refresh();
      assert.equal(st.eventRetentionEnabled, latest ?? null, 'same-tag 200 still reports');
      assert.deepEqual(changes, ['settings'], 'quiet report change uses normal notification');
      hub.api.prefs = async () => ({ status: 304, etag: tag });
      await store.refresh();
      assert.equal(st.eventRetentionEnabled, latest ?? null, '304 retains report');
      store[adopt]();
      assert.equal(st.eventRetentionEnabled, latest ?? null, adopt + ' cannot replay cached true');
    }
  });
  await t.test('conflict recovery reports before cached rows are adopted', async () => {
    const { hub, store, st } = await ready({ hub: { runtime: retentionRuntime(false) } });
    store.edit('events.refresh_interval', '10m');
    hub.competing('agents.default_max_depth', 40);
    hub.runtime = retentionRuntime(true);
    await store.save();
    assert.equal(st.newer.reason, 'conflict');
    assert.equal(st.eventRetentionEnabled, true);
    assert.equal(store.value('events.refresh_interval'), 600);
  });
  for (const latest of [false, undefined, '304']) await t.test('late GET cannot replace ' + latest, async () => {
    const { hub, store, st } = await ready({ hub: { runtime: retentionRuntime(false) } });
    const old = deferred(); hub.api.prefs = () => old.promise;
    const pending = store.refresh();
    hub.api.prefs = async () => latest === '304' ? { status: 304 } : reportedReply(latest);
    await store.refresh();
    old.resolve(reportedReply(true)); await pending;
    assert.equal(st.eventRetentionEnabled, latest === false || latest === '304' ? false : null);
  });
  for (const latest of [false, undefined]) await t.test('load ordering and superseded PATCH still acknowledge saved values: ' + latest, async () => {
    const { hub, store, st } = await ready({ hub: { runtime: retentionRuntime(false) } });
    const old = deferred(); hub.api.prefs = () => old.promise;
    const loading = store.load();
    hub.api.prefs = async () => reportedReply(undefined);
    await store.load(); old.resolve(reportedReply(true)); await loading;
    assert.equal(st.eventRetentionEnabled, null, 'older load cannot resurrect true');
    store.edit('events.refresh_interval', '10m');
    const patch = deferred(); hub.api.patchPrefs = () => patch.promise;
    const saving = store.save();
    hub.api.prefs = async () => reportedReply(latest);
    await store.load();
    const saved = reportedReply(true, '"saved"');
    Object.assign(saved.body.preferences.find(r => r.key === 'events.refresh_interval'), { value: 600, source: 'stored' });
    patch.resolve(saved); await saving;
    assert.equal(st.eventRetentionEnabled, latest ?? null, 'older PATCH status is superseded');
    assert.equal(store.value('events.refresh_interval'), 600, 'saved values are acknowledged');
    assert.deepEqual(store.dirtyKeys(), []);
    assert.deepEqual(st.saved.keys, ['events.refresh_interval']);
  });
});

test('busy and failed settings refresh retain the last event retention report', async t => {
  await t.test('busy entry, failure, unreadable reply and normal recovery', async () => {
    const { hub, store, st } = await ready({ hub: { preferences: r2Rows(), storage: storage(GiB), runtime: retentionRuntime(false) } });
    store.edit('presence.default_ttl', '10m'); await store.save();
    const confirm = st.confirm, before = hub.calls.length;
    hub.runtime = retentionRuntime(true); hub.storage = storage(2 * GiB);
    await store.refresh();
    assert.deepEqual(hub.calls.slice(before).map(c => c.op), ['storage']);
    assert.equal(st.confirm, confirm); assert.equal(st.storage.used_bytes, 2 * GiB);
    assert.equal(st.eventRetentionEnabled, false);
    store.cancelConfirm();
    hub.api.prefs = async () => { throw Object.assign(new Error('offline'), { status: 503 }); };
    await store.refresh(); assert.equal(st.eventRetentionEnabled, false);
    hub.api.prefs = async () => ({ status: 200, etag: '"invalid"', body: { runtime: retentionRuntime(true) } });
    await store.refresh(); assert.equal(st.eventRetentionEnabled, false);
    hub.api.prefs = async () => reportedReply(true);
    await store.refresh(); assert.equal(st.eventRetentionEnabled, true);
    hub.runtime = retentionRuntime(false);
    await store.save(); await store.confirm();
    assert.equal(st.eventRetentionEnabled, false, 'normal PATCH replaces last report');
  });
  for (const status of [200, 304]) await t.test('late same-tag ' + status + ' while confirming or saving', async () => {
    for (const busy of ['confirming', 'saving']) {
      const { hub, store, st } = await ready({ hub: { runtime: retentionRuntime(false) } });
      const old = deferred(), late = deferred(); let n = 0;
      hub.api.prefs = () => (++n === 1 ? old.promise : late.promise);
      const first = store.refresh(), second = store.refresh();
      let finish;
      if (busy === 'confirming') { store.edit('presence.default_ttl', '10m'); await store.save(); }
      else {
        store.edit('events.refresh_interval', '10m');
        const pending = deferred(); hub.api.patchPrefs = () => pending.promise;
        finish = { pending, saving: store.save() };
      }
      const confirm = st.confirm;
      late.resolve(status === 304 ? { status: 304 } : reportedReply(true, st.etag)); await second;
      assert.equal(st.eventRetentionEnabled, false, 'late busy reply supplies no report');
      assert.equal(st.confirm, confirm);
      if (finish) { finish.pending.reject(Object.assign(new Error('refused'), { status: 503 })); await finish.saving; }
      else store.cancelConfirm();
      old.resolve(reportedReply(true, st.etag)); await first;
      assert.equal(st.eventRetentionEnabled, true, 'discarded busy reply advances no barrier');
    }
  });
  await t.test('destroy discards load, refresh, PATCH and conflict recovery reports', async () => {
    for (const op of ['load', 'refresh', 'patch', 'conflict']) {
      const { hub, store, st } = await ready({ hub: { runtime: retentionRuntime(false) } });
      const pending = deferred();
      hub.api.prefs = () => pending.promise;
      hub.api.patchPrefs = op === 'conflict' ? async () => { throw { code: 'prefs_revision_conflict' }; } : () => pending.promise;
      let work;
      if (op === 'load' || op === 'refresh') work = store[op]();
      else { store.edit('events.refresh_interval', '10m'); work = store.save(); await flush(); }
      store.destroy(); pending.resolve(reportedReply(true)); await work;
      assert.equal(st.eventRetentionEnabled, false, op);
    }
  });
});

// Context R8/R9 use the same mounted DOM rather than source-text assertions.
async function withMountedSettings(hub, run) {
  const prev = { document: globalThis.document, window: globalThis.window, CSS: globalThis.CSS, location: globalThis.location };
  const body = new StEl('body'), root = new StEl('div');
  globalThis.document = { body, activeElement: body, hidden: false, createElement: tag => new StEl(tag), getElementById: () => null, addEventListener() {}, removeEventListener() {} };
  globalThis.window = { localStorage: memStorage(), addEventListener() {}, removeEventListener() {} };
  globalThis.CSS = { escape: s => String(s).replace(/[^a-zA-Z0-9_-]/g, ch => '\\' + ch) };
  globalThis.location = { host: 'hub.test' };
  const field = fk => walkSt(root, e => e.attrs['data-fk'] === fk)[0];
  const click = fk => {
    const el = field(fk); assert.ok(el, 'mounted control: ' + fk);
    for (const fn of el.on.click || []) fn();
  };
  const input = (fk, value) => {
    const el = field(fk); assert.ok(el, 'mounted input: ' + fk);
    el.value = value; for (const fn of el.on.input || []) fn({ target: el });
  };
  const off = mountSettings(root, { api: hub.api, toast() {}, store: { onConn: () => () => {} },
    appearance: { get: () => ({ theme: 'system', density: 'comfortable' }), subscribe: () => () => {} } });
  try { await flush(); await run({ root, field, click, input }); }
  finally { off(); for (const [key, value] of Object.entries(prev)) globalThis[key] = value; }
}

test('send warning threshold edits in List and Policy with wire bounds, zero disable and saved provenance', async t => {
  for (const view of ['list', 'policy']) await t.test(view, async () => {
    const hub = fakeHub();
    await withMountedSettings(hub, async ({ root, field, click, input }) => {
      const key = 'presence.send_warning_after', fk = 'st-presence-send-warning-after';
      click('view-' + view);
      if (view === 'policy') {
        assert.match(root.textContent, /Warn on successful sends/);
        assert.match(root.textContent, /0 disables the warning/);
        assert.match(root.textContent, /open streams count as active/);
        click('tok-' + key);
      }
      const descriptor = hub.preferences.find(r => r.key === key);
      assert.deepEqual([descriptor.type, descriptor.default, descriptor.min, descriptor.max, descriptor.disabled_value], ['duration', 3600, 60, 31536000, 0]);
      assert.equal(field(fk).value, '1h'); assert.equal(field(fk).disabled, false);
      const rowEl = () => walkSt(root, e => e.attrs['data-row'] === key)[0];
      assert.ok(walkSt(rowEl(), e => e.className === 'st-src s-default').length);
      input(fk, '30s'); click('save'); await flush();
      assert.match(root.textContent, /between 1 min and 365 d/);
      assert.equal(hub.calls.filter(c => c.op !== 'get').length, 0, 'out-of-range editor cannot write');
      input(fk, '2h');
      assert.ok(walkSt(rowEl(), e => e.className === 'st-src s-edited').length);
      click('save'); await flush();
      assert.equal(field(fk).value, '2h');
      assert.ok(walkSt(rowEl(), e => e.className === 'st-src s-stored').length);
      input(fk, '0'); click('save'); await flush();
      assert.equal(field(fk).value, '0', 'zero saves without confirmation');
      assert.equal(hub.preferences.find(r => r.key === key).value, 0);
      assert.deepEqual(hub.calls.filter(c => c.op === 'patch').map(c => [c.patch, c.tag]), [
        [{ set: { [key]: 7200 }, reset: [] }, '"prefs-3-g1"'],
        [{ set: { [key]: 0 }, reset: [] }, '"prefs-4-g1"'],
      ]);
      assert.equal(hub.calls.filter(c => c.op === 'preview').length, 0, 'send advisories do not shorten presence TTLs');
      click('view-' + (view === 'list' ? 'policy' : 'list'));
      if (view === 'list') click('tok-' + key);
      assert.equal(field(fk).value, '0', 'both views share the saved threshold');
    });
  });
});

test('archive-on pill keeps acknowledged-event policy independent of a disabled pruning gate', async () => {
  const hub = fakeHub({ preferences: rows({ installed: true }), runtime: retentionRuntime(false) });
  await withMountedSettings(hub, async ({ root, click }) => {
    click('view-policy');
    const lane = key => walkSt(root, e => e.attrs['data-row'] === key)[0];
    const mechanics = () => walkSt(lane('retention.ack_events'), e => e.attrs.role === 'radio').map(e => [e.attrs['aria-label'], e.attrs['aria-checked'], e.disabled, e.style.left]);
    const before = mechanics();
    click('st-retention-archive-mode');
    assert.match(root.textContent, /History kept; acknowledged-event policy unchanged/);
    assert.ok(lane('retention.history').className.includes('is-paused'));
    assert.ok(!lane('retention.ack_events').className.includes('is-paused'));
    assert.deepEqual(mechanics(), before, 'archive changes neither ack track nor its zero stop');
    assert.ok(walkSt(lane('retention.ack_events'), e => e.attrs['data-event-retention'] === 'disabled').length);
    assert.equal(hub.calls.filter(c => c.op !== 'get').length, 0, 'archive pill stages only a draft');
  });
});
