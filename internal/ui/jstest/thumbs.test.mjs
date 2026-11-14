import test from 'node:test';
import assert from 'node:assert/strict';
import { THUMB_MAX, createThumbCache } from '../static/thumbs-model.js';

const MiB = 1 << 20;
const sha = c => c.repeat(64);
const att = (c, extra = {}) => ({ sha256: sha(c), size: 1234, media_type: 'image/png', name: c + '.png', ...extra });

// fakeApi: every blob(sha) call parks until the test settles it.
function fakeApi() {
  const calls = [], waiting = new Map();
  return {
    calls,
    blob(s) {
      calls.push(s);
      return new Promise((resolve, reject) => waiting.set(s, { resolve, reject }));
    },
    settle(s, blob) { waiting.get(s).resolve(blob); },
    fail(s, err) { waiting.get(s).reject(err); },
  };
}

function fakeURL() {
  let n = 0;
  const created = [], revoked = [];
  return {
    created, revoked,
    createObjectURL(blob) { const u = 'blob:fake/' + ++n; created.push({ url: u, type: blob.type, size: blob.size }); return u; },
    revokeObjectURL(u) { revoked.push(u); },
  };
}

const tick = () => new Promise(resolve => setTimeout(resolve, 0));

function setup(rasterize) {
  const api = fakeApi(), URL = fakeURL();
  let ready = 0;
  const cache = createThumbCache({ api, URL, rasterize, onReady: () => { ready++; } });
  return { api, URL, cache, ready: () => ready };
}

test('unsupported declared types → not eligible, no fetch', async () => {
  const { api, URL, cache, ready } = setup();
  for (const media_type of ['text/html', 'image/bmp', 'application/octet-stream', '', undefined]) {
    const a = att('a', { media_type });
    assert.equal(cache.eligible(a), false, String(media_type));
    assert.deepEqual(cache.state(a), { state: 'none', url: null }, String(media_type));
  }
  // The allowlist matches the declared essence: lowercased, before any ';'.
  for (const media_type of ['image/png', 'IMAGE/JPEG', 'image/gif; x=1', ' image/webp ', 'image/svg+xml', 'IMAGE/SVG+XML', 'image/svg+xml; charset=utf-8']) assert.equal(cache.eligible(att('b', { media_type })), true, media_type);
  // No sha256 to fetch by, or a malformed one: not eligible either.
  assert.equal(cache.eligible(att('c', { sha256: undefined })), false);
  assert.equal(cache.eligible(att('c', { sha256: '../../v1/admin' })), false);
  await tick();
  assert.equal(api.calls.length, 0);
  assert.equal(URL.created.length, 0);
  assert.equal(ready(), 0);
  assert.equal(cache.stats.fetches, 0);
});

test('declared png but served text/html → chip', async () => {
  const { api, URL, cache, ready } = setup();
  const a = att('a');
  assert.equal(cache.state(a).state, 'loading');
  api.settle(sha('a'), new Blob(['<html><script>alert(1)</script></html>'], { type: 'text/html' }));
  await tick();
  assert.deepEqual(cache.state(a), { state: 'chip', url: null });
  assert.equal(URL.created.length, 0, 'an object URL was made for a hostile type');
  assert.equal(ready(), 1);

  // Served as SVG, served over 10 MiB, served untyped, or a failed fetch: chip, and no URL.
  const cases = [
    ['b', new Blob(['<svg xmlns="http://www.w3.org/2000/svg"/>'], { type: 'image/svg+xml' })],
    ['c', { type: 'image/png', size: THUMB_MAX + 1 }],
    ['d', new Blob(['x'], { type: '' })],
    ['e', null],
  ];
  for (const [c, blob] of cases) { assert.equal(cache.state(att(c)).state, 'loading'); api.settle(sha(c), blob); }
  assert.equal(cache.state(att('f')).state, 'loading');
  api.fail(sha('f'), Object.assign(new Error('gone'), { status: 410, code: 'blob_purged' }));
  await tick();
  for (const c of ['b', 'c', 'd', 'e', 'f']) assert.deepEqual(cache.state(att(c)), { state: 'chip', url: null }, c);
  assert.equal(URL.created.length, 0);
  assert.equal(ready(), 6);
});

test('declared over 10 MiB → no fetch', async () => {
  const { api, cache } = setup();
  assert.equal(THUMB_MAX, 10 * MiB);
  const big = att('a', { size: 10 * MiB + 1 });
  assert.equal(cache.eligible(big), false);
  assert.deepEqual(cache.state(big), { state: 'none', url: null });
  // Exactly 10 MiB is still eligible; a missing or negative size is not.
  assert.equal(cache.eligible(att('b', { size: 10 * MiB })), true);
  assert.equal(cache.eligible(att('c', { size: undefined })), false);
  assert.equal(cache.eligible(att('c', { size: -1 })), false);
  await tick();
  assert.deepEqual(api.calls, []);
});

test('origin_url only → no fetch', async () => {
  const { api, cache } = setup();
  // A stored Message carries no blob_url, so an origin_url says the bytes may
  // be reference-only (PROTOCOL §11): no fetch.
  const ref = att('a', { origin_url: 'https://example.com/shot.png' });
  assert.equal(cache.eligible(ref), false);
  assert.deepEqual(cache.state(ref), { state: 'none', url: null });
  // A hub-set blob_url says the bytes are hosted.
  assert.equal(cache.eligible(att('b', { origin_url: 'https://example.com/shot.png', blob_url: 'https://hub/v1/blobs/' + sha('b') + '?t=x' })), true);
  await tick();
  assert.deepEqual(api.calls, []);
});

test('hosted, from the operator history page: hosted bytes with an origin_url fetch; the hub saying not hosted never fetches', async () => {
  const { api, cache } = setup();
  const origin = { origin_url: 'https://example.com/shot.png' };
  // The page's hosted_blobs says these bytes are on the hub, origin_url or not.
  assert.equal(cache.eligible(att('a', origin), true), true);
  assert.deepEqual(cache.state(att('a', origin), true), { state: 'loading', url: null });
  // Reference-only (or purged): listed nowhere on a page that has hosted_blobs.
  assert.equal(cache.eligible(att('b', origin), false), false);
  assert.equal(cache.eligible(att('c'), false), false);
  assert.deepEqual(cache.state(att('c'), false), { state: 'none', url: null });
  // A page without hosted_blobs leaves today's rule.
  assert.equal(cache.eligible(att('d', origin), undefined), false);
  assert.equal(cache.eligible(att('e'), undefined), true);
  // The declared checks still apply to hosted bytes.
  assert.equal(cache.eligible(att('f', { ...origin, media_type: 'text/html' }), true), false);
  await tick();
  assert.deepEqual(api.calls, [sha('a')]);
});

test('one fetch per sha256 across repeated state() calls', async () => {
  const { api, URL, cache, ready } = setup();
  // Two messages may attach the same bytes under different names.
  for (let i = 0; i < 5; i++) {
    assert.equal(cache.state(att('a')).state, 'loading');
    assert.equal(cache.state(att('a', { name: 'copy.png' })).state, 'loading');
  }
  assert.deepEqual(api.calls, [sha('a')]);
  api.settle(sha('a'), new Blob(['png'], { type: 'image/png' }));
  await tick();
  for (let i = 0; i < 5; i++) assert.equal(cache.state(att('a')).state, 'ok');
  assert.deepEqual(api.calls, [sha('a')], 'a settled sha256 was fetched again');
  assert.equal(URL.created.length, 1);
  assert.equal(cache.stats.fetches, 1);
  assert.equal(ready(), 1);
  // A failed fetch is not retried for the life of the cache either.
  cache.state(att('b'));
  api.fail(sha('b'), Object.assign(new Error('unreachable'), { status: 0, code: 'network' }));
  await tick();
  cache.state(att('b'));
  assert.deepEqual(api.calls, [sha('a'), sha('b')]);
});

test('a fetch that settles after two state() calls ends ok and calls onReady once', async () => {
  const { api, URL, cache, ready } = setup();
  const a = att('a', { media_type: 'image/webp' });
  assert.deepEqual(cache.state(a), { state: 'loading', url: null });
  assert.deepEqual(cache.state(a), { state: 'loading', url: null });
  assert.equal(ready(), 0);
  api.settle(sha('a'), new Blob(['webp'], { type: 'image/webp; q=1' }));
  await tick();
  const st = cache.state(a);
  assert.equal(st.state, 'ok');
  assert.equal(st.url, URL.created[0].url);
  assert.equal(URL.created[0].type, 'image/webp; q=1');
  assert.equal(ready(), 1);
  cache.state(a);
  await tick();
  assert.equal(ready(), 1, 'onReady fired again without a new fetch');
});

test('release revokes every URL created', async () => {
  const { api, URL, cache, ready } = setup();
  for (const c of ['a', 'b', 'c']) cache.state(att(c));
  cache.state(att('d'));
  api.settle(sha('a'), new Blob(['1'], { type: 'image/png' }));
  api.settle(sha('b'), new Blob(['2'], { type: 'image/gif' }));
  api.settle(sha('c'), new Blob(['3'], { type: 'image/jpeg' }));
  await tick();
  assert.equal(URL.created.length, 3);
  // An <img> that fails to decode is marked chip, and its URL goes at once.
  cache.broken(sha('c'));
  assert.deepEqual(cache.state(att('c')), { state: 'chip', url: null });
  assert.deepEqual(URL.revoked, [URL.created[2].url]);

  cache.release();
  assert.deepEqual([...URL.revoked].sort(), URL.created.map(c => c.url).sort(), 'not every URL was revoked exactly once');
  assert.equal(cache.stats.created, 3);
  assert.equal(cache.stats.revoked, 3);
  const readyBefore = ready();
  // A fetch that lands after release makes no URL and wakes nobody.
  api.settle(sha('d'), new Blob(['4'], { type: 'image/png' }));
  await tick();
  assert.equal(URL.created.length, 3);
  assert.equal(ready(), readyBefore);
  // A released cache fetches nothing more.
  assert.deepEqual(cache.state(att('e')), { state: 'none', url: null });
  assert.equal(api.calls.length, 4);
  cache.release();
  assert.equal(URL.revoked.length, 3, 'a second release revoked again');
});

const svgAtt = (c, extra = {}) => att(c, { media_type: 'image/svg+xml', name: 'diagram.svg', ...extra });
const svgBlob = () => new Blob(['<svg xmlns="http://www.w3.org/2000/svg"/>'], { type: 'image/svg+xml; charset=utf-8' });

test('SVG declared → eligible and fetched, only rasterized PNG handed out and revoked', async () => {
  const input = svgBlob(), png = new Blob(['png'], { type: 'image/png' });
  const { api, URL, cache, ready } = setup(async blob => { assert.equal(blob, input); return png; });
  const a = svgAtt('a');
  assert.equal(cache.eligible(a), true);
  assert.deepEqual(cache.state(a), { state: 'loading', url: null });
  assert.deepEqual(api.calls, [sha('a')]);
  api.settle(sha('a'), input);
  await tick();
  assert.deepEqual(cache.state(a), { state: 'ok', url: 'blob:fake/1' });
  assert.deepEqual(URL.created, [{ url: 'blob:fake/1', type: 'image/png', size: 3 }]);
  assert.equal(ready(), 1);
  assert.equal(cache.stats.created, 1);
  cache.release();
  assert.deepEqual(URL.revoked, ['blob:fake/1']);
  assert.equal(cache.stats.revoked, 1);
});

test('SVG over 2 MiB declared → no fetch; exactly the cap is eligible', () => {
  const { api, cache } = setup();
  assert.equal(cache.eligible(svgAtt('a', { size: 2 * MiB })), true);
  assert.deepEqual(cache.state(svgAtt('b', { size: 2 * MiB + 1 })), { state: 'none', url: null });
  assert.deepEqual(api.calls, []);
});

test('SVG served with a different type, untyped or over cap → chip without rasterizing', async () => {
  const { api, URL, cache } = setup(() => { assert.fail('invalid SVG reached rasterizer'); });
  for (const [c, blob] of [
    ['a', new Blob(['png'], { type: 'image/png' })],
    ['b', new Blob(['html'], { type: 'text/html' })],
    ['c', { type: 'image/svg+xml', size: 2 * MiB + 1 }],
    ['d', new Blob(['svg'])],
  ]) {
    assert.equal(cache.state(svgAtt(c)).state, 'loading');
    api.settle(sha(c), blob);
  }
  await tick();
  for (const c of ['a', 'b', 'c', 'd']) assert.deepEqual(cache.state(svgAtt(c)), { state: 'chip', url: null });
  assert.deepEqual(URL.created, []);
});

test('rasterize failure, null or non-PNG result → chip with no URL leaked', async () => {
  for (const rasterize of [
    async () => { throw new Error('decode or tainted canvas'); },
    async () => null,
    async () => svgBlob(),
    undefined,
  ]) {
    const { api, URL, cache, ready } = setup(rasterize);
    assert.equal(cache.state(svgAtt('a')).state, 'loading');
    api.settle(sha('a'), svgBlob());
    await tick();
    assert.deepEqual(cache.state(svgAtt('a')), { state: 'chip', url: null });
    assert.deepEqual(URL.created, []);
    assert.equal(ready(), 1);
  }
});

test('release during SVG rasterization creates no PNG URL and wakes nobody', async () => {
  let finish;
  const { api, URL, cache, ready } = setup(() => new Promise(resolve => { finish = resolve; }));
  assert.equal(cache.state(svgAtt('a')).state, 'loading');
  api.settle(sha('a'), svgBlob());
  await tick();
  assert.equal(typeof finish, 'function');
  cache.release();
  finish(new Blob(['png'], { type: 'image/png' }));
  await tick();
  assert.deepEqual(URL.created, []);
  assert.equal(ready(), 0);
});
