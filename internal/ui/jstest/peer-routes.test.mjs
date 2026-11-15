import test from 'node:test';
import assert from 'node:assert/strict';
import { GROK_URL, normalizeKey, ROUTE_KINDS, routeError, routeForm, routeSetup } from '../static/peer-model.js';

// The peer page's route controls (peer-routes.js): which peers may edit
// routes, when a Grok Bot's missing routine is called out, the form checks
// that run before anything is sent, and the fixed error copy.

const ROUTINE = 'https://api2.cursor.sh/automations/webhook/123e4567-e89b-12d3-a456-426614174000';
const KEY = 'crsr_0123456789abcdef';
const facts = (kind, extra = {}) => ({ id: 'peer_A', kind, archivedAt: null, ...extra });
const route = (kind, via = 'peer') => ({ id: 'route_' + kind + via, kind, via });

test('routeSetup: a Grok Bot no grokbot route reaches is missing setup; other kinds pull, so no route is fine', () => {
  assert.deepEqual(routeSetup(facts('grokbot'), []), { editable: true, missing: true, suggest: 'grokbot', canAdd: true });
  // An http route does not run the routine: still missing, and Add is
  // offered though it takes over (the routine is what it needs).
  assert.deepEqual(routeSetup(facts('grokbot'), [route('http')]), { editable: true, missing: true, suggest: 'grokbot', canAdd: true });
  // Its principal's grokbot route reaches it (send falls back to it).
  assert.deepEqual(routeSetup(facts('grokbot'), [route('grokbot', 'principal')]), { editable: true, missing: false, suggest: 'grokbot', canAdd: true });
  assert.deepEqual(routeSetup(facts('grokbot'), [route('grokbot')]), { editable: true, missing: false, suggest: 'grokbot', canAdd: false });
  // Archived: no callout (it cannot be reached anyway; Unarchive first).
  assert.equal(routeSetup(facts('grokbot', { archivedAt: '2026-11-15T00:00:00Z' }), []).missing, false);
  for (const kind of ['session', 'dot', 'openclaw', 'pagchat-bot', 'launcher']) {
    assert.deepEqual(routeSetup(facts(kind), []), { editable: true, missing: false, suggest: 'http', canAdd: true }, kind);
    // One route of its own: Edit, not Add (a second would take over).
    assert.equal(routeSetup(facts(kind), [route('http')]).canAdd, false, kind);
    // Only its principal's: its own may be added (it takes precedence).
    assert.equal(routeSetup(facts(kind), [route('http', 'principal')]).canAdd, true, kind);
  }
});

test('routeSetup: before routes load or when the read fails, nothing is called missing or offered', () => {
  assert.deepEqual(routeSetup(facts('grokbot'), null), { editable: true, missing: false, suggest: 'grokbot', canAdd: false });
  assert.deepEqual(routeSetup(facts('session'), undefined), { editable: true, missing: false, suggest: 'http', canAdd: false });
});

test('routeSetup: the operator, a source, or no row: nothing to edit', () => {
  for (const f of [facts('operator'), facts('source'), null, { id: '', kind: 'session' }]) {
    assert.deepEqual(routeSetup(f, []), { editable: false, missing: false, suggest: 'http', canAdd: false });
  }
});

test('routeError: a verify raced by a URL change says to verify again', () => {
  assert.match(routeError('Could not verify the route.', { code: 'route_changed', details: {} }, { verifying: true }), /changed while it was being verified/);
});

test('routeForm: add needs a URL, and a Grok Bot also its key on the routine pattern', () => {
  assert.deepEqual(ROUTE_KINDS, ['http', 'grokbot']);
  assert.ok(GROK_URL.test(ROUTINE));
  assert.deepEqual(routeForm({ kind: 'http', url: '  https://hooks.example.com/vecnet  ' }), { body: { url: 'https://hooks.example.com/vecnet' } });
  assert.equal(routeForm({ kind: 'http', url: '' }).field, 'url');
  assert.deepEqual(routeForm({ kind: 'grokbot', url: ROUTINE, key: KEY }), { body: { url: ROUTINE, key: KEY } });
  // One leading Bearer is dropped, as onboarding accepts.
  assert.deepEqual(routeForm({ kind: 'grokbot', url: ROUTINE, key: '  Bearer ' + KEY + '\n' }).body.key, KEY);
  assert.equal(routeForm({ kind: 'grokbot', url: ROUTINE, key: '' }).field, 'key');
  assert.equal(routeForm({ kind: 'grokbot', url: 'https://example.com/hook', key: KEY }).field, 'url');
  // An http route never sends a key, even if one was typed.
  assert.deepEqual(routeForm({ kind: 'http', url: 'https://h.example/x', key: KEY }), { body: { url: 'https://h.example/x' } });
});

test('routeForm: refuses what the hub would, without quoting the input', () => {
  const bad = [
    ['not a url', 'url'], ['ftp://h.example/x', 'url'], ['https://user:pw@h.example/x', 'url'], ['https://h.example/x#frag', 'url'],
  ];
  for (const [url, field] of bad) {
    const r = routeForm({ kind: 'http', url });
    assert.equal(r.field, field, url);
    assert.ok(!r.error.includes(url) && !r.error.includes('user:pw'), r.error);
  }
  for (const key of ['crsr_', 'crsr_has space', 'sk-123', 'Bearer Bearer ' + KEY]) {
    const r = routeForm({ kind: 'grokbot', url: ROUTINE, key });
    assert.equal(r.field, 'key', key);
    assert.ok(!r.error.includes(key.slice(5)) || key === 'crsr_', r.error);
  }
});

test('routeForm: an edit leaves blank fields unchanged but must change something', () => {
  assert.equal(routeForm({ kind: 'http', url: '', editing: true }).field, 'url');
  assert.equal(routeForm({ kind: 'grokbot', url: '', key: '', editing: true }).field, 'url');
  assert.deepEqual(routeForm({ kind: 'grokbot', url: '', key: KEY, editing: true }), { body: { key: KEY } });
  assert.deepEqual(routeForm({ kind: 'grokbot', url: ROUTINE, key: '', editing: true }), { body: { url: ROUTINE } });
  assert.deepEqual(routeForm({ kind: 'http', url: 'https://h.example/new', editing: true }), { body: { url: 'https://h.example/new' } });
});

test('normalizeKey: trims and drops one Bearer, case-insensitively', () => {
  assert.equal(normalizeKey('  bEaReR   ' + KEY + ' '), KEY);
  assert.equal(normalizeKey(KEY), KEY);
  assert.equal(normalizeKey(undefined), '');
});

test('routeError: fixed sentences; the hub message and unknown codes stay off the page', () => {
  const e = (code, details = {}, message = 'https://secret.example/hook?token=abc crsr_leak') => ({ code, details, message });
  assert.match(routeError('Could not add the route.', e('invalid_url')), /refused that URL/);
  assert.match(routeError('x', e('invalid_key')), /refused that key/);
  assert.match(routeError('x', e('invalid_value', { field: 'key' })), /Only a Grok Bot route takes a key/);
  assert.match(routeError('x', e('not_found')), /no longer exists/);
  assert.equal(routeError('Could not add the route.', e('weird_code')), 'Could not add the route.');
  assert.equal(routeError('Could not add the route.', new Error('boom crsr_leak')), 'Could not add the route.');
  for (const code of ['invalid_url', 'invalid_key', 'not_found', 'weird_code']) {
    const s = routeError('fallback', e(code));
    assert.ok(!s.includes('secret.example') && !s.includes('crsr_leak'), s);
  }
});

test('routeError: a failed verify says why for known reasons only', () => {
  const v = reason => routeError('Could not verify the route.', { code: 'invalid_value', details: { field: 'url', reason } }, { verifying: true });
  assert.match(v('timeout'), /did not answer in time\.$/);
  assert.match(v('connection_refused'), /connection was refused/);
  assert.match(v('challenge_failed: HTTP 404'), /answered HTTP 404\.$/);
  assert.match(v('challenge_failed: wrong or missing echo'), /did not echo the challenge/);
  assert.equal(v('something new: https://leak.example'), 'The receiver did not answer its verification challenge.');
  // Not a verify: the same code is the generic key/field rule.
  assert.equal(routeError('Could not verify the route.', { code: 'not_found' }, { verifying: true }), 'This route no longer exists. Reload the page.');
});
