// The preferences routes in api.js: ETag in, If-Match out, structured errors.
import test from 'node:test';
import assert from 'node:assert/strict';
import { APIError, createApi } from '../static/api.js';

const reply = (status, body, headers = {}) => new Response(body === null ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
function harness(fn) {
  const calls = [];
  const fetch = async (url, init) => { calls.push({ url, init }); return fn(url, init); };
  return { calls, api: createApi({ secret: 'vn_secret', fetch }) };
}
const snap = { revision: 2, preferences: [{ key: 'events.refresh_interval', type: 'duration', value: 1800, source: 'default', label: 'Events refresh interval' }] };

test('GET returns the body with its ETag; If-None-Match turns into a quiet 304', async () => {
  const { api, calls } = harness((url, init) => (init.headers['If-None-Match'] === '"prefs-2-x"' ? reply(304, null, { ETag: '"prefs-2-x"' }) : reply(200, snap, { ETag: '"prefs-2-x"' })));
  const r = await api.prefs();
  assert.deepEqual(r, { status: 200, etag: '"prefs-2-x"', body: snap });
  assert.equal(calls[0].url, '/v1/admin/prefs');
  assert.equal(calls[0].init.method, 'GET');
  assert.equal(calls[0].init.cache, 'no-store');
  assert.equal(calls[0].init.headers['If-None-Match'], undefined);
  assert.equal(calls[0].init.headers.Authorization, 'Bearer vn_secret');
  assert.deepEqual(await api.prefs({ etag: '"prefs-2-x"' }), { status: 304, etag: '"prefs-2-x"', body: null });
  assert.equal(calls[1].init.headers['If-None-Match'], '"prefs-2-x"');
});

test('PATCH sends one If-Match and the JSON patch; the reply carries the new ETag', async () => {
  const { api, calls } = harness(() => reply(200, { ...snap, revision: 3 }, { ETag: '"prefs-3-x"' }));
  const patch = { set: { 'events.refresh_interval': 600 }, reset: [] };
  const r = await api.patchPrefs(patch, '"prefs-2-x"');
  assert.equal(r.etag, '"prefs-3-x"');
  assert.equal(r.body.revision, 3);
  assert.equal(calls[0].url, '/v1/admin/prefs');
  assert.equal(calls[0].init.method, 'PATCH');
  assert.equal(calls[0].init.headers['If-Match'], '"prefs-2-x"');
  assert.equal(calls[0].init.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(calls[0].init.body), patch);
});

test('preview POSTs with If-Match; a confirmation token travels only in a JSON body', async () => {
  const preview = { base_revision: 2, risks: [{ code: 'presence_shortened', keys: ['presence.default_ttl'], message: 'm' }], impact: {}, confirm_token: 'abc.def', expires_at: '2026-11-06T10:00:00.000Z' };
  const { api, calls } = harness(url => (url.endsWith('/preview') ? reply(200, preview, { ETag: '"prefs-2-x"' }) : reply(200, snap, { ETag: '"prefs-3-x"' })));
  const patch = { set: { 'presence.default_ttl': 600 }, reset: [] };
  assert.deepEqual(await api.previewPrefs(patch, '"prefs-2-x"'), preview);
  assert.equal(calls[0].url, '/v1/admin/prefs/preview');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.headers['If-Match'], '"prefs-2-x"');
  await api.patchPrefs({ ...patch, confirm: ['abc.def'] }, '"prefs-2-x"');
  for (const c of calls) assert.ok(!c.url.includes('abc'), 'no token in a URL');
  assert.deepEqual(JSON.parse(calls[1].init.body).confirm, ['abc.def']);
});

test('structured errors keep status, code and details', async () => {
  const cases = [
    [412, 'prefs_revision_conflict', {}],
    [428, 'prefs_precondition_required', {}],
    [422, 'prefs_invalid_value', { field: 'agents.default_ttl', constraint: 'max_expiry' }],
    [409, 'prefs_confirmation_required', {}],
    [409, 'prefs_unavailable', { field: 'blobs.expiry' }],
    [503, 'prefs_storage_unavailable', {}],
  ];
  for (const [status, code, details] of cases) {
    const { api } = harness(() => reply(status, { error: { code, message: 'm', details } }));
    const e = await api.patchPrefs({ set: {}, reset: ['x'] }, '"e"').catch(x => x);
    assert.ok(e instanceof APIError, code);
    assert.equal(e.status, status);
    assert.equal(e.code, code);
    assert.deepEqual(e.details, details);
  }
  const { api } = harness(() => { throw new TypeError('Failed to fetch'); });
  const e = await api.prefs().catch(x => x);
  assert.equal(e.status, 0);
});

test('other routes still resolve to bare bodies', async () => {
  const { api } = harness(() => reply(200, { peers: [] }, { ETag: '"p"' }));
  assert.deepEqual(await api.peers(), { peers: [] });
});
