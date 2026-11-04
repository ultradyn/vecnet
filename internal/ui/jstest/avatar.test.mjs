import test from 'node:test';
import assert from 'node:assert/strict';
import { AVATAR_MAX, avatarHash, createAvatarCache } from '../static/avatar-model.js';

const sha = c => c.repeat(64);
const png = (size = 900) => ({ type: 'image/png', size });

// fakeApi: every avatar(peerId) call parks until the test settles it.
function fakeApi() {
  const calls = [], waiting = [];
  return {
    calls,
    avatar(id) {
      calls.push(id);
      return new Promise((resolve, reject) => waiting.push({ id, resolve, reject }));
    },
    settle(i, blob) { waiting[i].resolve(blob); },
    fail(i, err) { waiting[i].reject(err); },
  };
}

function fakeURL() {
  let n = 0;
  const created = [], revoked = [];
  return {
    created, revoked,
    createObjectURL() { const u = 'blob:fake/' + ++n; created.push(u); return u; },
    revokeObjectURL(u) { revoked.push(u); },
  };
}

function setup() {
  const api = fakeApi(), URL = fakeURL();
  let ready = 0;
  const cache = createAvatarCache({ api, URL, onReady: () => { ready++; } });
  return { api, URL, cache, ready: () => ready };
}

test('avatarHash accepts only a lower-hex sha256', () => {
  assert.equal(avatarHash({ avatar: sha('a') }), sha('a'));
  for (const p of [null, {}, { avatar: null }, { avatar: 'abc' }, { avatar: sha('A') }, { avatar: sha('a') + 'x' }, { avatar: 7 }]) {
    assert.equal(avatarHash(p), null, JSON.stringify(p));
  }
});

test('one fetch per hash, shared by peers with the same image', async () => {
  const { api, URL, cache, ready } = setup();
  const first = cache.state('peer_a', sha('a'));
  assert.equal(first.state, 'loading');
  assert.equal(cache.state('peer_b', sha('a')).state, 'loading');
  assert.deepEqual(api.calls, ['peer_a']);
  api.settle(0, png());
  await first.ready;
  const st = cache.state('peer_b', sha('a'));
  assert.equal(st.state, 'ok');
  assert.equal(st.url, URL.created[0]);
  assert.equal(api.calls.length, 1);
  assert.equal(ready(), 1);
});

test('no hash, a bad hash or no peer is no fetch', () => {
  const { api, cache } = setup();
  for (const [id, h] of [['peer_a', null], ['peer_a', 'nope'], ['', sha('a')]]) assert.equal(cache.state(id, h).state, 'none');
  assert.equal(api.calls.length, 0);
});

test('a failed fetch, a non-PNG or an oversized body falls back to the letter', async () => {
  const { api, URL, cache } = setup();
  const a = cache.state('p1', sha('a')), b = cache.state('p2', sha('b')), c = cache.state('p3', sha('c')), d = cache.state('p4', sha('d'));
  api.fail(0, new Error('404'));
  api.settle(1, { type: 'image/svg+xml', size: 10 });
  api.settle(2, png(AVATAR_MAX + 1));
  api.settle(3, png(0));
  await Promise.all([a.ready, b.ready, c.ready, d.ready]);
  for (const h of ['a', 'b', 'c', 'd']) assert.equal(cache.state('p', sha(h)).state, 'failed', h);
  assert.equal(URL.created.length, 0);
  assert.equal(api.calls.length, 4, 'a failure is not retried on every draw');
});

test('broken revokes and stays on the letter', async () => {
  const { api, URL, cache } = setup();
  const st = cache.state('p', sha('a'));
  api.settle(0, png());
  await st.ready;
  cache.broken(sha('a'));
  assert.deepEqual(URL.revoked, [URL.created[0]]);
  assert.equal(cache.state('p', sha('a')).state, 'failed');
});

test('prune revokes hashes the directory no longer names; a returning hash fetches again', async () => {
  const { api, URL, cache } = setup();
  const a = cache.state('p1', sha('a')), b = cache.state('p2', sha('b'));
  api.settle(0, png());
  api.settle(1, png());
  await Promise.all([a.ready, b.ready]);
  cache.prune([sha('b')]);
  assert.deepEqual(URL.revoked, [URL.created[0]]);
  assert.equal(cache.state('p2', sha('b')).state, 'ok');
  assert.equal(cache.state('p1', sha('a')).state, 'loading');
  assert.equal(api.calls.length, 3);
});

test('a fetch that settles after prune or release makes no URL', async () => {
  const { api, URL, cache } = setup();
  const a = cache.state('p1', sha('a'));
  cache.prune([]);
  api.settle(0, png());
  await a.ready;
  const b = cache.state('p2', sha('b'));
  cache.release();
  api.settle(1, png());
  await b.ready;
  assert.equal(URL.created.length, 0);
  assert.equal(cache.state('p2', sha('b')).state, 'none', 'a released cache fetches nothing');
});

test('release revokes every URL once', async () => {
  const { api, URL, cache } = setup();
  const a = cache.state('p1', sha('a')), b = cache.state('p2', sha('b'));
  api.settle(0, png());
  api.settle(1, png());
  await Promise.all([a.ready, b.ready]);
  cache.release();
  cache.release();
  assert.deepEqual(URL.revoked.sort(), URL.created.slice().sort());
  assert.equal(cache.stats.revoked, 2);
});
