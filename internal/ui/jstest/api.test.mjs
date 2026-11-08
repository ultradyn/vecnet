import test from 'node:test';
import assert from 'node:assert/strict';
import { APIError, createApi } from '../static/api.js';
import { fakeR2 } from './r2-fixture.mjs';

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
    [() => api.receipt('dly_1', 'read'), 'POST', '/v1/receipts'],
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
    [() => api.activityStats(), 'GET', '/v1/admin/activity-stats'],
    [() => api.blob('abc'), 'GET', '/v1/blobs/abc'],
    [() => api.agentKinds(), 'GET', '/v1/admin/agent-kinds'],
    [() => api.agentAdd({ kind: 'session', spec_version: '2026-11-06', request_id: 'req_1', inputs: { name: 'a' } }), 'POST', '/v1/admin/agents'],
    [() => api.onboardingStatus('req/1'), 'GET', '/v1/admin/agents/onboarding/req%2F1'],
    [() => api.hubIdentity(), 'GET', '/v1/hub/identity'],
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

test('activityStats reads an uncached authenticated snapshot and preserves API errors', async () => {
  const body = { now: '2026-11-09T12:34:56.789Z', window_start: '2026-11-09T11:34:56.789Z', window_end: '2026-11-09T12:34:56.789Z', bucket_seconds: 60, messages: 0, messages_per_minute: 0, active_peers: 0, buckets: Array(60).fill(0), peers: {} };
  const { api, calls } = harness(() => json(200, body));
  assert.deepEqual(await api.activityStats(), body);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, '/v1/admin/activity-stats');
  assert.equal(calls[0].init.method, 'GET');
  assert.equal(calls[0].init.cache, 'no-store');
  assert.equal(calls[0].init.credentials, 'omit');
  assert.equal(calls[0].init.headers.Authorization, 'Bearer vn_secret');
  assert.equal(calls[0].init.body, undefined);
  const denied = harness(() => json(403, { error: { code: 'forbidden', message: 'operator required' } }));
  await assert.rejects(denied.api.activityStats(), { status: 403, code: 'forbidden' });
});

test('send carries the operator_ui ingress mark; presence posts state and at', async () => {
  const { api, calls } = harness();
  await api.send({ idempotency_key: 'k', content: { text: 'hi' } });
  assert.equal(calls[0].init.headers['X-Vecnet-Ingress'], 'operator_ui');
  assert.deepEqual(JSON.parse(calls[0].init.body), { idempotency_key: 'k', content: { text: 'hi' } });
  await api.presence('working', '2026-11-02T00:00:00.000Z');
  assert.deepEqual(JSON.parse(calls[1].init.body), { state: 'working', at: '2026-11-02T00:00:00.000Z' });
  await api.receipt('dly_1', 'read');
  assert.deepEqual(JSON.parse(calls[2].init.body), { delivery_id: 'dly_1', state: 'read' });
  assert.equal(calls[2].init.headers['Content-Type'], 'application/json');
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

test('agent kinds, submit and status use the operator routes and an abort signal', async () => {
  const controller = new AbortController();
  const { api, calls } = harness();
  await api.agentKinds({ signal: controller.signal });
  await api.agentAdd({ kind: 'grokbot', spec_version: '2026-11-06', request_id: 'req_1', inputs: { name: 'Helper' } }, { signal: controller.signal });
  await api.onboardingStatus('req/1', { signal: controller.signal });
  assert.equal(calls[0].url, '/v1/admin/agent-kinds');
  assert.equal(calls[0].init.method, 'GET');
  assert.equal(calls[0].init.signal, controller.signal);
  assert.equal(calls[0].init.cache, 'no-store');
  assert.equal(calls[1].url, '/v1/admin/agents');
  assert.equal(calls[1].init.method, 'POST');
  assert.deepEqual(JSON.parse(calls[1].init.body), { kind: 'grokbot', spec_version: '2026-11-06', request_id: 'req_1', inputs: { name: 'Helper' } });
  assert.equal(calls[2].url, '/v1/admin/agents/onboarding/req%2F1');
  assert.equal(calls[2].init.signal, controller.signal);
  for (const call of calls) {
    assert.ok(!call.url.includes('vn_secret'), call.url);
    assert.equal(call.init.headers.Authorization, 'Bearer vn_secret');
  }
});

// ---------- R2 direct downloads (plan 2026-11-05-r2-blobs Task 7) ----------

const r2 = (f, extra = {}) => createApi({ secret: 'vn_op', fetch: f.fetch, XHR: f.XHR, now: f.now, ...extra });
const refOf = o => ({ sha256: o.sha256, blob_id: o.blob_id });

test('R2 download: discovery and a fresh grant by bearer, then a credential-free GET; bytes checked against the verified digest', async () => {
  const f = fakeR2();
  const o = f.seed('hello attachment', { cls: '1d' });
  const blob = await r2(f).download(refOf(o));
  assert.ok(blob instanceof Blob);
  assert.equal(blob.type, 'application/octet-stream', 'never the declared type: no inline rendering');
  assert.equal(await blob.text(), 'hello attachment');
  const [caps, grant] = f.hubCalls();
  assert.equal(caps.path, '/v1/blob-capabilities');
  assert.equal(grant.path, `/v1/blobs/${o.sha256}/download?blob_id=${o.blob_id}`);
  assert.equal(grant.headers.Authorization, 'Bearer vn_op');
  const [get] = f.s3Calls('GET');
  assert.equal(get.credentials, 'omit');
  assert.equal(get.redirect, 'error');
  assert.equal(get.referrerPolicy, 'no-referrer');
  assert.deepEqual(get.headers, {}, 'no bearer, no custom headers on the provider request');
  // blob(sha, id) is the same path; blob(sha) stays the hub read.
  const again = await r2(f).blob(o.sha256, o.blob_id);
  assert.equal(await again.text(), 'hello attachment');
});

test('R2 download: every click mints anew; an earlier URL expiring never uses up the right to get again', async () => {
  const f = fakeR2();
  const o = f.seed('keep me', { cls: 'keep' });
  const api = r2(f);
  await api.download(refOf(o));
  f.advance(601000);
  await api.download(refOf(o));
  f.advance(601000);
  await api.download(refOf(o));
  const grants = f.hubCalls('GET', '/v1/blobs/' + o.sha256 + '/download');
  assert.equal(grants.length, 3);
  const urls = f.s3Calls('GET').map(c => c.url);
  assert.equal(new Set(urls).size, 3, 'each download used its own fresh URL');
});

test('R2 download: a capability past its deadline or refused 403 is re-minted once, then fails without looping', async () => {
  let f = fakeR2();
  let o = f.seed('abc');
  // The first grant arrives already past its deadline on this clock.
  let first = true;
  f.tamper = () => (first ? (first = false, { deadline: now => now - 1000 }) : null);
  assert.equal(await (await r2(f).download(refOf(o))).text(), 'abc');
  assert.equal(f.hubCalls('GET', '/v1/blobs/' + o.sha256 + '/download').length, 2);

  f = fakeR2();
  o = f.seed('abc');
  f.nextGet.push(403);
  assert.equal(await (await r2(f).download(refOf(o))).text(), 'abc');
  assert.equal(f.hubCalls('GET', '/v1/blobs/' + o.sha256 + '/download').length, 2);

  f = fakeR2();
  o = f.seed('abc');
  f.nextGet.push(403, 403);
  const err = await r2(f).download(refOf(o)).catch(e => e);
  assert.equal(err.code, 'blob_capability_expired');
  assert.equal(f.hubCalls('GET', '/v1/blobs/' + o.sha256 + '/download').length, 2, 'bounded: one re-mint per click');
});

test('R2 download: digest, length and trust are checked before any bytes are handed out', async () => {
  let f = fakeR2();
  let o = f.seed('abc');
  f.nextGet.push({ body: 'abd' });
  assert.equal((await r2(f).download(refOf(o)).catch(e => e)).code, 'blob_checksum_mismatch');
  f = fakeR2();
  o = f.seed('abc');
  f.nextGet.push({ body: 'abcd' });
  assert.equal((await r2(f).download(refOf(o)).catch(e => e)).code, 'blob_checksum_mismatch');
  // A grant that does not claim provider verification is not trusted.
  f = fakeR2();
  o = f.seed('abc');
  f.tamper = () => ({ verified: false });
  assert.equal((await r2(f).download(refOf(o)).catch(e => e)).code, 'bad_response');
  assert.equal(f.s3Calls().length, 0);
});

test('R2 download: expired, purged, unavailable and rate-limited return no URL and fetch nothing', async () => {
  const f = fakeR2();
  const api = r2(f);
  const a = f.seed('a'), b = f.seed('b'), c = f.seed('c'), d = f.seed('d'), e = f.seed('e');
  f.download.set(a.blob_id, 'expired');
  f.download.set(b.blob_id, 'purged');
  f.download.set(c.blob_id, 'unavailable');
  f.download.set(d.blob_id, { rate: 17 });
  f.download.set(e.blob_id, { header: 9 });
  const ea = await api.download(refOf(a)).catch(x => x);
  const eb = await api.download(refOf(b)).catch(x => x);
  const ec = await api.download(refOf(c)).catch(x => x);
  const ed = await api.download(refOf(d)).catch(x => x);
  const ee = await api.download(refOf(e)).catch(x => x);
  assert.deepEqual([ea.status, ea.code], [410, 'blob_expired']);
  assert.deepEqual([eb.status, eb.code], [410, 'blob_purged']);
  assert.deepEqual([ec.status, ec.code], [503, 'blob_unavailable']);
  assert.deepEqual([ed.status, ed.code, ed.details.retry_after_seconds], [429, 'blob_url_rate_limited', 17]);
  assert.equal(ee.details.retry_after_seconds, 9, 'Retry-After fills in a missing detail');
  assert.equal(f.s3Calls().length, 0);
  // The logical deadline holds on the hub clock even before any ticker.
  const g = f.seed('soon', { cls: '1h' });
  f.advance(3600000);
  assert.equal((await api.download(refOf(g)).catch(x => x)).code, 'blob_expired');
});

test('R2 download: hostile grants are refused before the provider is touched', async () => {
  const cases = {
    'foreign origin': () => ({ url: 'https://evil.example/x' }),
    'userinfo': () => ({ url: 'https://u:p@0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/k' }),
    'javascript scheme': () => ({ url: 'javascript:alert(1)' }),
    'plain http': () => ({ url: 'http://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/k' }),
    'past 600s': () => ({ deadline: now => now + 3600000 }),
    'past the object deadline': g => ({ objectExpires: new Date(Date.parse(g.expires) - 3600000 * 23.99).toISOString() }),
    'size over the cap': () => ({ size: 26 << 20 }),
  };
  for (const [name, t] of Object.entries(cases)) {
    const f = fakeR2();
    const o = f.seed('abc');
    f.tamper = t;
    const err = await r2(f).download(refOf(o)).catch(e => e);
    assert.ok(err instanceof APIError, name);
    assert.equal(err.code, 'bad_response', name);
    assert.equal(f.s3Calls().length, 0, name);
  }
  // keep has no object deadline, yet its URL is still bounded by 600 s.
  const f = fakeR2();
  const k = f.seed('kept', { cls: 'keep' });
  f.tamper = () => ({ deadline: now => now + 601000 + 5000 });
  assert.equal((await r2(f).download(refOf(k)).catch(e => e)).code, 'bad_response');
});

test('R2 download: a provider CORS refusal or redirect is storage_unreachable; the URL never appears in an error', async () => {
  for (const hook of ['cors', 'redirect', 500]) {
    const f = fakeR2();
    const o = f.seed('abc');
    f.nextGet.push(hook);
    const err = await r2(f).download(refOf(o)).catch(e => e);
    assert.equal(err.code, hook === 500 ? 'blob_unavailable' : 'storage_unreachable', String(hook));
    assert.ok(!JSON.stringify({ m: err.message, d: err.details }).includes('Signature'));
  }
  // A legacy reference (no blob_id) still reads from the hub with the bearer.
  const f = fakeR2({ backend: 'disk' });
  f.legacy = new Map([['a'.repeat(64), { bytes: 'old bytes', type: 'text/plain' }]]);
  const blob = await r2(f).download({ sha256: 'a'.repeat(64) });
  assert.equal(await blob.text(), 'old bytes');
  assert.equal(f.s3Calls().length, 0);
});

test('blobStatus reads metadata only: no link and no mint', async () => {
  const f = fakeR2();
  const calls = [];
  const api = createApi({ secret: 'vn_op', now: f.now, fetch: async (url, init) => { calls.push(url); return new Response(JSON.stringify({ state: 'available', retention_class: 'keep', expires_at: null, sha256_verified: true, integrity: { algorithm: 'sha256', value: 'a'.repeat(64), verified: true } }), { status: 200, headers: { 'Content-Type': 'application/json' } }); } });
  const st = await api.blobStatus('a'.repeat(64), 'blo_00000000000000000000000001');
  assert.equal(st.state, 'available');
  assert.deepEqual(calls, ['/v1/blobs/' + 'a'.repeat(64) + '/status?blob_id=blo_00000000000000000000000001']);
});
