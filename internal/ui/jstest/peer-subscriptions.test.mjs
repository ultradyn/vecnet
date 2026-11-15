import test from 'node:test';
import assert from 'node:assert/strict';
import { createApi } from '../static/api.js';
import * as model from '../static/subs-model.js';
import { createStore } from '../static/store.js';
import { subscriptionRemoved, subscriptionsResource } from '../static/subs.js';

test('removeSubscription deletes only the selected ID using the authenticated admin endpoint', async () => {
  let request;
  const api = createApi({ secret: 'vn_fixture', fetch: async (url, init) => {
    request = { url, init }; return new Response(null, { status: 204 });
  } });
  assert.equal(typeof api.removeSubscription, 'function');
  assert.equal(await api.removeSubscription('sub_a/b?x=1'), null);
  assert.equal(request.url, '/v1/admin/subscriptions/sub_a%2Fb%3Fx%3D1');
  assert.equal(request.init.method, 'DELETE');
  assert.equal(request.init.headers.Authorization, 'Bearer vn_fixture');
  assert.equal(request.init.body, undefined);
});

test('peer subscription cards prioritize known problems, preserve stable order and exclude other peers', () => {
  assert.equal(typeof model.subscriptionItems, 'function');
  const raw = [
    { sub_id: 'sub_ok', peer_id: 'peer_a', health: 'ok' },
    { sub_id: 'sub_other', peer_id: 'peer_b', health: 'failing' },
    { sub_id: 'sub_failed', peer_id: 'peer_a', health: 'paused_failed' },
    { sub_id: 'sub_future', peer_id: 'peer_a', health: 'future_status' },
    { sub_id: 'sub_due', peer_id: 'peer_a', health: 'late' },
  ];
  const before = structuredClone(raw);
  const items = model.subscriptionItems(raw, 'peer_a');
  assert.deepEqual(items.map(x => x.sub_id), ['sub_failed', 'sub_due', 'sub_ok', 'sub_future']);
  assert.deepEqual(items.map(x => x.attention), [true, true, false, false]);
  assert.deepEqual(raw, before, 'drawing cannot reorder the shared resource');
  assert.deepEqual(model.subscriptionItems(undefined, 'peer_a'), []);
});

test('callback host display never exposes callback paths, query credentials or user info', () => {
  assert.equal(typeof model.subscriptionHost, 'function');
  for (const [value, want] of [
    ['hooks.example:8443', 'hooks.example:8443'],
    ['https://user:password@hooks.example/private/secret?token=hidden', 'hooks.example'],
    ['[::1]:1234', '[::1]:1234'],
    ['2606:4700:4700::1111', '2606:4700:4700::1111'],
    ['::1', '::1'],
    ['', 'Unknown host'], ['not a host', 'Unknown host'],
  ]) assert.equal(model.subscriptionHost(value), want);
});

test('confirmed removals survive remount, older reads and a failed follow-up without hiding fresh IDs', async () => {
  let finish, reads = 0;
  const api = { subscriptions: async () => {
    reads++;
    if (reads === 1) return new Promise(resolve => { finish = resolve; });
    if (reads === 2) throw new Error('offline');
    return [{ sub_id: 'sub_fresh' }, { sub_id: 'sub_kept' }, { sub_id: 'sub_removed' }];
  } };
  const ctx = { api, store: createStore({ api }) };
  const first = subscriptionsResource(ctx);
  const off = first.subscribe(() => {});
  await new Promise(resolve => setImmediate(resolve));
  off();
  const remounted = subscriptionsResource(ctx);
  assert.equal(remounted, first, 'peer and dashboard reuse the store-scoped resource');
  const again = remounted.subscribe(() => {});
  subscriptionRemoved(ctx, 'sub_removed');
  finish([{ sub_id: 'sub_removed' }, { sub_id: 'sub_kept' }]);
  await ctx.store.settled();
  assert.deepEqual(remounted.get(), [{ sub_id: 'sub_kept' }]);
  assert.match(remounted.error().message, /offline/);
  await remounted.reload();
  assert.deepEqual(remounted.get(), [{ sub_id: 'sub_fresh' }, { sub_id: 'sub_kept' }]);
  again();
  const other = { api, store: createStore({ api }) };
  const separate = subscriptionsResource(other);
  await separate.reload();
  assert.ok(separate.get().some(s => s.sub_id === 'sub_removed'), 'removals never leak to another store');
});

test('removal errors offer recovery without rendering arbitrary server text', () => {
  assert.equal(typeof model.subscriptionRemoveError, 'function');
  const secret = 'https://secret.example/callback?key=hidden';
  assert.match(model.subscriptionRemoveError({ status: 403, message: secret }), /operator/i);
  assert.match(model.subscriptionRemoveError({ status: 0, code: 'network', message: secret }), /confirm.*remov/i);
  assert.match(model.subscriptionRemoveError({ status: 500, message: secret }), /try again/i);
  assert.ok(!model.subscriptionRemoveError(new Error(secret)).includes(secret));
});
