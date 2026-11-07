import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import {
  APPEARANCE_KEY, DEFAULT_APPEARANCE, applyAppearance, createAppearance, normalizeAppearance, parseAppearance,
  readAppearance, resolveTheme, writeAppearance,
} from '../static/appearance.js';
import { memStorage } from './settings-fixture.mjs';

function fakeRoot() {
  const attrs = new Map();
  return { attrs, setAttribute: (k, v) => attrs.set(k, String(v)), getAttribute: k => attrs.get(k) ?? null };
}
function fakeWindow({ light = false } = {}) {
  const listeners = { storage: new Set() }, media = new Set();
  const mql = { get matches() { return light; }, addEventListener: (_, fn) => media.add(fn), removeEventListener: (_, fn) => media.delete(fn) };
  return {
    listeners, media,
    setLight(v) { light = v; for (const fn of media) fn({ matches: v }); },
    matchMedia: q => (q === '(prefers-color-scheme: light)' ? mql : { matches: false, addEventListener() {}, removeEventListener() {} }),
    addEventListener: (t, fn) => listeners[t] && listeners[t].add(fn),
    removeEventListener: (t, fn) => listeners[t] && listeners[t].delete(fn),
    fire(key) { for (const fn of listeners.storage) fn({ key }); },
  };
}

test('stored appearance is validated field by field; corrupt or blocked storage gives the defaults', () => {
  assert.deepEqual(DEFAULT_APPEARANCE, { theme: 'system', density: 'comfortable' });
  assert.deepEqual(parseAppearance('{"theme":"light","density":"compact"}'), { theme: 'light', density: 'compact' });
  assert.deepEqual(parseAppearance('{"theme":"neon","density":"compact"}'), { theme: 'system', density: 'compact' });
  assert.deepEqual(parseAppearance('{"theme":"dark","density":7}'), { theme: 'dark', density: 'comfortable' });
  for (const bad of ['not json', '[]', 'null', '"dark"', '{"theme":{"x":1}}']) assert.deepEqual(parseAppearance(bad), DEFAULT_APPEARANCE, bad);
  assert.deepEqual(normalizeAppearance({ theme: 'light', secret: 'vn_operator_x', draft: { a: 1 } }), { theme: 'light', density: 'comfortable' });
  assert.deepEqual(readAppearance(memStorage({ blocked: true })), DEFAULT_APPEARANCE);
  assert.deepEqual(readAppearance(memStorage()), DEFAULT_APPEARANCE);
  assert.deepEqual(readAppearance(undefined), DEFAULT_APPEARANCE);
});

test('writes only the two enum scalars under the versioned key', () => {
  const s = memStorage();
  assert.equal(writeAppearance(s, { theme: 'light', density: 'compact', token: 'tok', etag: '"prefs-1"' }), true);
  assert.deepEqual([...s.map.keys()], [APPEARANCE_KEY]);
  assert.deepEqual(JSON.parse(s.map.get(APPEARANCE_KEY)), { theme: 'light', density: 'compact' });
  assert.equal(writeAppearance(memStorage({ blocked: true }), { theme: 'dark' }), false);
});

test('system follows the browser colour scheme', () => {
  const w = fakeWindow({ light: true });
  assert.equal(resolveTheme('system', q => w.matchMedia(q)), 'light');
  assert.equal(resolveTheme('dark', q => w.matchMedia(q)), 'dark');
  assert.equal(resolveTheme('system', undefined), 'dark');
  assert.equal(resolveTheme('system', () => { throw new Error('no'); }), 'dark');
  const root = fakeRoot();
  applyAppearance(root, { theme: 'system', density: 'compact' }, q => w.matchMedia(q));
  assert.deepEqual(Object.fromEntries(root.attrs), { 'data-theme': 'light', 'data-theme-pref': 'system', 'data-density': 'compact' });
});

test('the controller applies, persists, follows the scheme and other tabs, and makes no request', () => {
  const origFetch = globalThis.fetch;
  let fetched = 0;
  globalThis.fetch = () => { fetched++; return Promise.reject(new Error('no')); };
  try {
    const storage = memStorage({ init: { [APPEARANCE_KEY]: '{"theme":"system","density":"compact"}' } });
    const root = fakeRoot(), win = fakeWindow({ light: false });
    const a = createAppearance({ storage, root, win });
    assert.equal(root.getAttribute('data-theme'), 'dark');
    assert.equal(root.getAttribute('data-density'), 'compact');
    const heard = [];
    a.subscribe(v => heard.push(v));
    win.setLight(true);
    assert.equal(root.getAttribute('data-theme'), 'light', 'system theme follows the OS');

    assert.deepEqual(a.set({ theme: 'dark', density: 'comfortable' }), { persisted: true });
    assert.equal(root.getAttribute('data-theme'), 'dark');
    win.setLight(false);
    win.setLight(true);
    assert.equal(root.getAttribute('data-theme'), 'dark', 'an explicit theme ignores the OS');

    // Another tab writes the key: this tab follows, with enum validation.
    storage.map.set(APPEARANCE_KEY, '{"theme":"light","density":"bogus"}');
    win.fire(APPEARANCE_KEY);
    assert.deepEqual(a.get(), { theme: 'light', density: 'comfortable' });
    assert.equal(root.getAttribute('data-theme'), 'light');
    win.fire('vecnet.ui.secret');
    storage.map.set(APPEARANCE_KEY, 'corrupt');
    win.fire(APPEARANCE_KEY);
    assert.deepEqual(a.get(), DEFAULT_APPEARANCE);
    assert.equal(heard.length, 3);

    a.close();
    assert.equal(win.listeners.storage.size, 0);
    assert.equal(win.media.size, 0);
    assert.equal(fetched, 0);
  } finally { globalThis.fetch = origFetch; }
});

test('blocked storage still applies the choice for this page', () => {
  const root = fakeRoot();
  const a = createAppearance({ storage: memStorage({ blocked: true }), root, win: fakeWindow() });
  assert.deepEqual(a.set({ theme: 'light', density: 'compact' }), { persisted: false });
  assert.equal(root.getAttribute('data-theme'), 'light');
  assert.deepEqual(a.get(), { theme: 'light', density: 'compact' });
});

// The <head> boot script: same key and enums, before first paint.
function boot({ stored, light = false, blocked = false } = {}) {
  const root = fakeRoot();
  const ctx = {
    document: { documentElement: root },
    localStorage: { getItem: () => { if (blocked) throw new Error('denied'); return stored ?? null; } },
    window: { matchMedia: () => ({ matches: light }) },
  };
  vm.runInNewContext(readFileSync(new URL('../static/appearance-boot.js', import.meta.url), 'utf8'), ctx);
  return Object.fromEntries(root.attrs);
}

test('the boot script sets theme and density before the app loads', () => {
  assert.deepEqual(boot(), { 'data-theme': 'dark', 'data-theme-pref': 'system', 'data-density': 'comfortable' });
  assert.deepEqual(boot({ light: true }), { 'data-theme': 'light', 'data-theme-pref': 'system', 'data-density': 'comfortable' });
  assert.deepEqual(boot({ stored: '{"theme":"light","density":"compact"}' }), { 'data-theme': 'light', 'data-theme-pref': 'light', 'data-density': 'compact' });
  assert.deepEqual(boot({ stored: '{"theme":"dark"}', light: true })['data-theme'], 'dark');
  assert.deepEqual(boot({ stored: '{"theme":"<b>","density":"x"}' }), boot());
  assert.deepEqual(boot({ stored: '{{{' }), boot());
  assert.deepEqual(boot({ blocked: true, light: true })['data-theme'], 'light');
});

test('index.html loads the boot script in <head>, before any stylesheet paints', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const head = html.slice(0, html.indexOf('</head>'));
  const bootAt = head.indexOf('<script src="appearance-boot.js"></script>');
  assert.ok(bootAt > 0, 'a classic (blocking) script in head');
  assert.ok(bootAt < head.indexOf('<link rel="stylesheet"'), 'before the first stylesheet');
});
