import test from 'node:test';
import assert from 'node:assert/strict';
import { APIError, createApi } from '../static/api.js';

const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function harness(reply = () => json(200, { ok: true }), extra = {}) {
  const calls = [];
  const fetch = async (url, init) => { calls.push({ url, init }); return reply(url, init); };
  return { calls, api: createApi({ secret: 'vn_secret', fetch, ...extra }) };
}

test('sends bearer and accepts JSON; 204 → null', async () => {
  const { api, calls } = harness(url => (url === '/v1/peers' ? json(200, { peers: [] }) : new Response(null, { status: 204 })));
  assert.deepEqual(await api.peers(), { peers: [] });
  assert.equal(calls[0].init.method, 'GET');
  assert.equal(calls[0].init.headers.Authorization, 'Bearer vn_secret');
  assert.equal(calls[0].init.headers.Accept, 'application/json');
  assert.equal(calls[0].init.body, undefined);
  assert.equal(await api.ack('e1.2'), null);
  assert.deepEqual(JSON.parse(calls[1].init.body), { cursor: 'e1.2' });
  assert.equal(calls[1].init.headers['Content-Type'], 'application/json');
});

test('error envelope → APIError with status, code, details; non-JSON body → code http_<status>', async () => {
  const envelope = { error: { code: 'cursor_purged', message: 'cursor is below retention', details: { oldest_cursor: 'e1.9' } } };
  const { api } = harness(() => json(409, envelope));
  const error = await api.events({ after: 'e1.1', wait: 25 }).catch(e => e);
  assert.ok(error instanceof APIError);
  assert.ok(error instanceof Error);
  assert.equal(error.status, 409);
  assert.equal(error.code, 'cursor_purged');
  assert.equal(error.message, 'cursor is below retention');
  assert.deepEqual(error.details, { oldest_cursor: 'e1.9' });

  const html = harness(() => new Response('<html>Bad gateway</html>', { status: 502 }));
  const raw = await html.api.peers().catch(e => e);
  assert.ok(raw instanceof APIError);
  assert.equal(raw.status, 502);
  assert.equal(raw.code, 'http_502');
  assert.deepEqual(raw.details, {});
});

test('401 calls onUnauthorized exactly once; network failure → status 0', async () => {
  let signedOut = 0;
  const { api } = harness(() => json(401, { error: { code: 'unauthenticated', message: 'no', details: {} } }), { onUnauthorized: () => { signedOut++; } });
  const results = await Promise.allSettled([api.peers(), api.inbox({}), api.events({ wait: 0 })]);
  await api.peers().catch(() => {});
  assert.equal(signedOut, 1);
  for (const { status, reason } of results) {
    assert.equal(status, 'rejected');
    assert.equal(reason.status, 401);
    assert.equal(reason.code, 'unauthenticated');
  }

  let unauthorized = 0;
  const offline = createApi({ secret: 's', fetch: async () => { throw new TypeError('Failed to fetch'); }, onUnauthorized: () => { unauthorized++; } });
  const error = await offline.peers().catch(e => e);
  assert.ok(error instanceof APIError);
  assert.equal(error.status, 0);
  assert.equal(error.code, 'network');
  assert.equal(unauthorized, 0);
});

// A body that starts and then dies, as a dropped connection does mid-read.
const lostBody = (error, status = 200) => new Response(new ReadableStream({
  start(controller) {
    controller.enqueue(new TextEncoder().encode('{"events":['));
    controller.error(error);
  },
}), { status });

test('a 2xx with no readable JSON is a network failure, never the 204 null', async () => {
  const replies = {
    'html page': () => new Response('<html>Sign in to this network</html>', { status: 200 }),
    'empty body': () => new Response('', { status: 200 }),
    'truncated JSON': () => new Response('{"events":[{"cur', { status: 200 }),
    'lost mid-read': () => lostBody(new TypeError('terminated')),
    'JSON null': () => new Response('null', { status: 200 }),
  };
  for (const [name, reply] of Object.entries(replies)) {
    let unauthorized = 0;
    const { api } = harness(reply, { onUnauthorized: () => { unauthorized++; } });
    const error = await api.events({ wait: 0 }).catch(e => e);
    assert.ok(error instanceof APIError, name);
    assert.equal(error.status, 0, name);
    assert.equal(error.code, 'network', name);
    assert.equal(unauthorized, 0, name);
  }
});

test('a body read that is aborted is status 0, code aborted', async () => {
  const { api } = harness(() => lostBody(new DOMException('stopped', 'AbortError')));
  const error = await api.events({ wait: 25 }).catch(e => e);
  assert.ok(error instanceof APIError);
  assert.equal(error.status, 0);
  assert.equal(error.code, 'aborted');
});

test('an error status keeps its status when its body is lost', async () => {
  const { api } = harness(() => lostBody(new TypeError('terminated'), 503));
  const error = await api.peers().catch(e => e);
  assert.ok(error instanceof APIError);
  assert.equal(error.status, 503);
  assert.equal(error.code, 'http_503');
});

test('every method calls its route with the secret only in the header', async () => {
  const { api, calls } = harness();
  const table = [
    [() => api.peers(), 'GET', '/v1/peers'],
    [() => api.adminPeers(), 'GET', '/v1/admin/peers'],
    [() => api.addPeer({ kind: 'dot', name: 'scout' }), 'POST', '/v1/admin/peers'],
    [() => api.dotLink('peer_1'), 'POST', '/v1/admin/dot-links'],
    [() => api.principals(), 'GET', '/v1/admin/principals'],
    [() => api.routes(), 'GET', '/v1/admin/routes'],
    [() => api.inbox({ limit: 50, before: 'dly_1', unread: true }), 'GET', '/v1/admin/inbox?limit=50&before=dly_1&unread=1'],
    [() => api.inbox({}), 'GET', '/v1/admin/inbox'],
    [() => api.inbox(), 'GET', '/v1/admin/inbox'],
    [() => api.myThreads({ limit: 200 }), 'GET', '/v1/threads?limit=200'],
    [() => api.threads({ participant: 'peer_1', limit: 5, cursor: 'c/1' }), 'GET', '/v1/admin/threads?participant=peer_1&limit=5&cursor=c%2F1'],
    [() => api.threads({ excludeParticipant: 'peer_op', limit: 50 }), 'GET', '/v1/admin/threads?exclude_participant=peer_op&limit=50'],
    [() => api.thread('thr_1'), 'GET', '/v1/admin/threads/thr_1'],
    [() => api.messages('thr_1', { after: 'msg_1', limit: 200 }), 'GET', '/v1/threads/thr_1/messages?after=msg_1&limit=200'],
    [() => api.matrix('thr_1', { after: 'msg_1', limit: 10 }), 'GET', '/v1/admin/threads/thr_1/deliveries?after=msg_1&limit=10'],
    [() => api.readThread('thr_1'), 'POST', '/v1/admin/threads/thr_1/read'],
    [() => api.events({ wait: 0 }), 'GET', '/v1/events?wait=0'],
    [() => api.events({ after: 'e1.2', wait: 25 }), 'GET', '/v1/events?after=e1.2&wait=25'],
    [() => api.ack('e1.2'), 'POST', '/v1/ack'],
    [() => api.send({ content: { text: 'hi' } }), 'POST', '/v1/messages'],
    [() => api.presence('idle', '2026-11-02T00:00:00.000Z'), 'POST', '/v1/presence'],
    [() => api.deadLetters(), 'GET', '/v1/admin/dead-letters'],
    [() => api.retryDeadLetter('dly_1'), 'POST', '/v1/admin/dead-letters/dly_1/retry'],
    [() => api.discardDeadLetter('dly_1'), 'POST', '/v1/admin/dead-letters/dly_1/discard'],
    [() => api.subscriptions(), 'GET', '/v1/admin/subscriptions'],
    [() => api.resumeSubscription('sub_1'), 'POST', '/v1/admin/subscriptions/sub_1/resume'],
    [() => api.loopCuts(24), 'GET', '/v1/admin/loop-cuts?hours=24'],
    [() => api.deliveryHealth(), 'GET', '/v1/admin/delivery-health'],
    [() => api.audit({ limit: 50, before: 7 }), 'GET', '/v1/admin/audit?limit=50&before=7'],
    [() => api.counters(), 'GET', '/v1/admin/counters'],
    [() => api.blob('abc'), 'GET', '/v1/blobs/abc'],
  ];
  for (const [call, method, url] of table) {
    await call();
    const last = calls.at(-1);
    assert.equal(last.url, url);
    assert.equal(last.init.method, method, url);
    assert.ok(!last.url.includes('vn_secret'), 'secret in URL');
    assert.equal(last.init.headers.Authorization, 'Bearer vn_secret', url);
    assert.equal(last.init.credentials, 'omit', url);
  }
});

test('send carries the operator_ui ingress mark; presence posts state and at', async () => {
  const { api, calls } = harness();
  await api.send({ idempotency_key: 'k', content: { text: 'hi' } });
  assert.equal(calls[0].init.headers['X-Vecnet-Ingress'], 'operator_ui');
  assert.deepEqual(JSON.parse(calls[0].init.body), { idempotency_key: 'k', content: { text: 'hi' } });
  await api.presence('working', '2026-11-02T00:00:00.000Z');
  assert.deepEqual(JSON.parse(calls[1].init.body), { state: 'working', at: '2026-11-02T00:00:00.000Z' });
});

test('POST with no request body sends no body and no Content-Type', async () => {
  const { api, calls } = harness();
  await api.readThread('thr_1');
  assert.equal(calls[0].init.body, undefined);
  assert.equal(calls[0].init.headers['Content-Type'], undefined);
});

test('a path id is encoded so it cannot change the route', async () => {
  const { api, calls } = harness();
  await api.thread('thr/../x?y');
  assert.equal(calls[0].url, '/v1/admin/threads/thr%2F..%2Fx%3Fy');
});

test('events passes the abort signal; an aborted fetch is status 0, code aborted', async () => {
  const controller = new AbortController();
  const { api, calls } = harness(() => { throw new DOMException('stopped', 'AbortError'); });
  const error = await api.events({ wait: 25, signal: controller.signal }).catch(e => e);
  assert.equal(calls[0].init.signal, controller.signal);
  assert.equal(calls[0].url, '/v1/events?wait=25');
  assert.equal(error.status, 0);
  assert.equal(error.code, 'aborted');
});

test('blob resolves to a Blob and errors like every other call', async () => {
  const { api } = harness(() => new Response('bytes', { status: 200, headers: { 'Content-Type': 'text/plain' } }));
  const blob = await api.blob('abc');
  assert.ok(blob instanceof Blob);
  assert.equal(await blob.text(), 'bytes');

  const lost = harness(() => lostBody(new TypeError('terminated')));
  const dropped = await lost.api.blob('abc').catch(e => e);
  assert.ok(dropped instanceof APIError);
  assert.equal(dropped.status, 0);
  assert.equal(dropped.code, 'network');

  const missing = harness(() => json(404, { error: { code: 'not_found', message: 'gone', details: {} } }));
  const error = await missing.api.blob('abc').catch(e => e);
  assert.equal(error.status, 404);
  assert.equal(error.code, 'not_found');
});

test('activity opens the feed with the bearer in a header, never the URL', async () => {
  const { api, calls } = harness(() => new Response('event: ready\ndata: {}\n\n', { status: 200, headers: { 'Content-Type': 'text/event-stream' } }));
  const r = await api.activity();
  assert.equal(await r.text(), 'event: ready\ndata: {}\n\n');
  assert.equal(calls[0].url, '/v1/admin/activity');
  assert.equal(calls[0].init.headers.Authorization, 'Bearer vn_secret');
  assert.equal(calls[0].init.headers.Accept, 'text/event-stream');
  assert.ok(!calls[0].url.includes('vn_secret'));
  let unauthorized = 0;
  const denied = harness(() => json(403, { error: { code: 'forbidden', message: 'operator credential required' } }), { onUnauthorized: () => { unauthorized++; } });
  const e = await denied.api.activity().catch(x => x);
  assert.ok(e instanceof APIError);
  assert.equal(e.status, 403);
  assert.equal(e.code, 'forbidden');
  assert.equal(unauthorized, 0);
});

test('dotLink asks for a link code for one peer, in the body', async () => {
  const { api, calls } = harness();
  await api.dotLink('peer_dot');
  assert.deepEqual(JSON.parse(calls.at(-1).init.body), { peer_id: 'peer_dot' });
});
