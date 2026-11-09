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

// BEGIN WEB-MEM-3 T1 — source draft; execution belongs to the parent.
{
  const bytes = s => new TextEncoder().encode(s);
  const tick = async () => { for (let i = 0; i < 40; i++) await Promise.resolve(); };
  const gate = () => {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    promise.catch(() => {}); // A cleanup rejection is observed even after an earlier assertion fails.
    return { promise, resolve, reject };
  };
  const observe = promise => {
    const state = { settled: false };
    state.done = Promise.resolve(promise).then(
      value => { state.settled = true; state.value = value; },
      error => { state.settled = true; state.error = error; });
    return state;
  };
  const apiError = (e, status, code) => {
    assert.ok(e instanceof APIError);
    assert.equal(e.status, status);
    if (code !== undefined) assert.equal(e.code, code);
  };
  let identity = 0;
  // Real Response/ReadableStream and native reader lock; only platform seams are controlled.
  // A held wrapper read models a fetch implementation whose outstanding read ignores abort.
  function wire({ status = 200, type = 'image/png', length, chunks = [bytes('abc'), null],
    readGate, holdReadAt = 1, cancelGate, noBody = false, unsupported = false } = {}) {
    const id = ++identity, events = [], fallback = [], calls = [];
    let reads = 0, cancels = 0, releases = 0, pending = 0, raw, ctl;
    const steps = [...chunks];
    const body = new ReadableStream({
      start(c) { ctl = c; },
      pull(c) {
        if (!steps.length) return; // Deliberately incomplete, drained explicitly in finally.
        const step = steps.shift();
        if (step === null) c.close();
        else if (step instanceof Error) c.error(step);
        else c.enqueue(step);
      },
      cancel() { events.push('source-cancel'); return cancelGate?.promise; },
    }, { highWaterMark: 0 });
    const headers = { 'Retry-After': '17' };
    if (type !== null) headers['Content-Type'] = type;
    if (length !== undefined) headers['Content-Length'] = String(length);
    const response = new Response(noBody ? null : body, { status, statusText: status >= 400 ? 'Denied' : 'OK', headers });
    let statusObserved = false;
    Object.defineProperty(response, 'status', { get() {
      if (!statusObserved) {
        statusObserved = true; events.push('status-observed');
        queueMicrotask(() => events.push('after-status-turn'));
      }
      return status;
    } });
    if (unsupported) Object.defineProperty(response, 'body', { value: unsupported === 'throw'
      ? { getReader() { throw new TypeError('reader unavailable'); } } : {} });
    else if (!noBody) {
      const acquire = body.getReader.bind(body);
      body.getReader = () => {
        events.push('lock'); raw = acquire(); raw.closed.catch(() => {});
        return {
          get closed() { return raw.closed; },
          read() {
            const n = ++reads; pending++; events.push(`read:${n}`);
            const wait = n === holdReadAt && readGate ? readGate.promise : Promise.resolve();
            return raw.read().then(
              value => wait.then(() => value), error => wait.then(() => { throw error; })
            ).finally(() => { pending--; events.push(`read-done:${n}`); });
          },
          cancel(reason) { cancels++; events.push('cancel'); return raw.cancel(reason); },
          releaseLock() { releases++; events.push(`release:${pending}`); raw.releaseLock(); },
        };
      };
    }
    for (const name of ['blob', 'json', 'text', 'arrayBuffer']) {
      response[name] = () => { fallback.push(name); throw new Error(`unbounded ${name}`); };
    }
    const fetch = async (url, init) => { calls.push({ id, url, init }); events.push('headers'); return response; };
    const check = ({ cancel = false } = {}) => {
      assert.deepEqual(fallback, [], `call ${id}: no unbounded Response reader`);
      assert.equal(cancels, cancel ? 1 : 0, `call ${id}: cancellation exactly once when needed`);
      assert.equal(releases, raw ? 1 : 0, `call ${id}: every acquired lock released`);
      assert.equal(body.locked, false);
      assert.ok(!events.some(e => e.startsWith('release:') && e !== 'release:0'), 'read settled before unlock');
    };
    const drain = async () => {
      readGate?.resolve(); cancelGate?.resolve();
      try { ctl.close(); } catch { /* Already closed, errored or cancelled. */ }
      await tick();
    };
    return { id, response, body, events, fallback, calls, fetch, check, drain,
      get reads() { return reads; }, get cancels() { return cancels; }, get releases() { return releases; } };
  }
  const client = (w, extra = {}) => createApi({ secret: 'vn_t1', fetch: w.fetch, ...extra });
  const trackAbort = signal => {
    const listeners = new Set(), add = signal.addEventListener.bind(signal), remove = signal.removeEventListener.bind(signal);
    signal.addEventListener = (name, fn, options) => { if (name === 'abort') listeners.add(fn); add(name, fn, options); };
    signal.removeEventListener = (name, fn, options) => { if (name === 'abort') listeners.delete(fn); remove(name, fn, options); };
    return listeners;
  };
  const invoke = (api, options = { maxBytes: 4 }) => api.thumbnailBlob('sha/../x?origin=https://elsewhere.invalid', options);
  const generic = (e, status) => {
    apiError(e, status, `http_${status}`);
    assert.equal(e.message, 'Denied');
    assert.deepEqual(e.details, { retry_after_seconds: 17 });
  };

  test('WEB-MEM-3 T1 availability: real createApi exposes independent thumbnailBlob', () => {
    assert.equal(typeof createApi({ secret: 's', fetch: () => { throw new Error('unexpected fetch'); } }).thumbnailBlob, 'function');
  });

  test('WEB-MEM-3 T1 authenticated escaped hub route, signal, raster Blob and EOF lock release', async () => {
    for (const type of ['image/png', 'image/jpeg', 'image/gif', 'image/webp', ' IMAGE/PNG ; charset=binary']) {
      const w = wire({ type }), controller = new AbortController();
      const listeners = trackAbort(controller.signal);
      try {
        const blob = await invoke(client(w), { maxBytes: 3, signal: controller.signal });
        assert.ok(blob instanceof Blob);
        assert.equal(blob.size, 3);
        assert.equal(blob.type.split(';')[0].trim().toLowerCase(), type.split(';')[0].trim().toLowerCase());
        assert.equal(await blob.text(), 'abc');
        assert.equal(w.calls.length, 1);
        const { url, init } = w.calls[0];
        assert.equal(url, '/v1/blobs/sha%2F..%2Fx%3Forigin%3Dhttps%3A%2F%2Felsewhere.invalid');
        assert.equal(init.headers.Authorization, 'Bearer vn_t1');
        assert.equal(init.method, 'GET'); assert.equal(init.cache, 'no-store');
        assert.equal(init.credentials, 'omit'); assert.equal(init.signal, controller.signal);
        assert.equal(init.body, undefined);
        assert.equal(listeners.size, 0, 'remove abort listeners on successful EOF');
        w.check();
      } finally { await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 maxBytes required positive safe integer capped at 10 MiB before fetch', async () => {
    let fetched = 0;
    const api = createApi({ secret: 's', fetch: async () => { fetched++; throw new Error('unexpected fetch'); } });
    for (const options of [undefined, {}, { maxBytes: undefined }, ...[null, 0, -1, 1.5, NaN, Infinity, '4', true, 1n, Symbol('cap'), {}, [], 10 * 1024 * 1024 + 1, Number.MAX_SAFE_INTEGER + 1].map(maxBytes => ({ maxBytes }))]) {
      await assert.rejects(async () => api.thumbnailBlob('a', options), RangeError);
      assert.equal(fetched, 0);
    }
    const w = wire();
    try { assert.equal((await invoke(client(w), { maxBytes: 10 * 1024 * 1024 })).size, 3); w.check(); }
    finally { await w.drain(); }
  });

  test('WEB-MEM-3 T1 preaborted signal never fetches; fetch abort and network failures normalize', async () => {
    const controller = new AbortController(); controller.abort();
    let fetched = 0;
    const api = createApi({ secret: 's', fetch: async () => { fetched++; throw new Error('must not fetch'); } });
    await assert.rejects(async () => invoke(api, { maxBytes: 4, signal: controller.signal }), e => { apiError(e, 0, 'aborted'); return true; });
    assert.equal(fetched, 0);
    for (const [cause, code] of [[new DOMException('stop', 'AbortError'), 'aborted'], [new TypeError('offline'), 'network']]) {
      const failed = createApi({ secret: 's', fetch: async () => { throw cause; } });
      await assert.rejects(async () => invoke(failed), e => { apiError(e, 0, code); return true; });
    }
  });

  test('WEB-MEM-3 T1 actual byte M and M+1 with absent or lying length and oversized first chunk', async () => {
    for (const length of [undefined, 1, 4]) for (const chunks of [[bytes('ab'), bytes('cd'), null], [bytes('ab'), bytes('cde'), null], [new Uint8Array(65), null]]) {
      const w = wire({ length, chunks });
      try {
        const result = await invoke(client(w)).then(value => ({ value }), error => ({ error }));
        if (chunks[0].length === 2 && chunks[1].length === 2) {
          assert.ok(result.value instanceof Blob); assert.equal(await result.value.text(), 'abcd'); w.check();
        } else {
          apiError(result.error, 0); assert.notEqual(result.error.code, 'aborted');
          assert.equal(w.reads, chunks[0].length > 4 ? 1 : 2, 'stop at first overflowing chunk');
          w.check({ cancel: true });
        }
      } finally { await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 oversized length and non-raster headers refuse before consuming bytes', async () => {
    for (const config of [{ length: 5 }, ...[null, '', 'image/svg+xml', 'text/html', 'application/octet-stream', 'image/avif'].map(type => ({ type }))]) {
      const w = wire(config);
      try {
        await assert.rejects(async () => invoke(client(w)), e => { apiError(e, 0); return true; });
        assert.equal(w.reads, 0, 'reject ineligible headers before read');
        // Early rejection may cancel an unlocked body instead of acquiring a reader.
        assert.equal(w.events.filter(e => e === 'source-cancel').length, 1);
        w.check({ cancel: w.events.includes('lock') });
      } finally { await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 success requires Blob and bounded stream capability; 204 is not an image', async () => {
    for (const config of [{ noBody: true }, { status: 204, noBody: true }, { unsupported: true }]) {
      const w = wire(config);
      try {
        await assert.rejects(async () => invoke(client(w)), e => {
          apiError(e, 0); assert.ok(e.message);
          if (config.unsupported) assert.match(e.message, /stream|support|capab|unavailable/i);
          return true;
        });
        w.check();
      } finally { await w.drain(); }
    }
    const w = wire(), OriginalBlob = globalThis.Blob;
    try {
      globalThis.Blob = undefined;
      await assert.rejects(async () => invoke(client(w)), e => { apiError(e, 0); assert.match(e.message, /blob|support|capab|unavailable/i); return true; });
      assert.deepEqual(w.fallback, []);
      assert.equal(w.body.locked, false);
    } finally { globalThis.Blob = OriginalBlob; await w.drain(); }
  });

  test('WEB-MEM-3 T1 Blob assembly owns bounded backing views and final raster type is rechecked', async () => {
    const OriginalBlob = globalThis.Blob;
    for (const forceType of [null, 'image/svg+xml']) {
      const storage = new Uint8Array(128); storage.set(bytes('abc'), 37);
      const w = wire({ chunks: [storage.subarray(37, 40), null] }), constructions = [];
      try {
        globalThis.Blob = class extends OriginalBlob {
          constructor(parts, options) {
            const owned = Array.from(parts);
            constructions.push(owned.map(p => ArrayBuffer.isView(p) ? p.buffer.byteLength : p instanceof ArrayBuffer ? p.byteLength : p instanceof OriginalBlob ? p.size : bytes(String(p)).length));
            super(owned, forceType ? { ...options, type: forceType } : options);
          }
        };
        const result = await invoke(client(w)).then(value => ({ value }), error => ({ error }));
        assert.ok(constructions.length > 0, 'exercise final Blob seam');
        assert.ok(constructions.flat().every(n => n <= 4), 'do not retain oversized backing buffers in Blob parts');
        if (forceType) apiError(result.error, 0);
        else { assert.equal(result.value.type, 'image/png'); assert.equal(await result.value.text(), 'abc'); }
        w.check({ cancel: Boolean(forceType) });
      } finally { globalThis.Blob = OriginalBlob; await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 valid error envelope at K preserves normalized code message details and Retry-After', async () => {
    for (const status of [401, 503]) for (const maxBytes of [160, 9000]) {
      const k = Math.min(8192, maxBytes);
      for (const details of [{ marker: 'bounded' }, { retry_after_seconds: 9 }]) {
        const encoded = bytes(JSON.stringify({ error: { code: 'thumb_denied', message: 'specific', details }, ignored: 'field' }));
        const padded = new Uint8Array(k).fill(32); padded.set(encoded);
        const w = wire({ status, type: 'text/plain', length: 1, chunks: [padded.subarray(0, 7), padded.subarray(7), null] });
        const notices = [];
        try {
          const e = await invoke(client(w, { onUnauthorized: e => notices.push(e) }), { maxBytes }).catch(e => e);
          apiError(e, status, 'thumb_denied'); assert.equal(e.message, 'specific');
          assert.deepEqual(e.details, { ...details, retry_after_seconds: details.retry_after_seconds || 17 });
          assert.equal(notices.length, status === 401 ? 1 : 0); w.check();
        } finally { await w.drain(); }
      }
    }
  });

  test('WEB-MEM-3 T1 malformed JSON UTF-8 and envelope schema normalize with null envelope', async () => {
    const malformed = ['{', 'null', '[]', '{}', '{"error":null}', '{"error":[]}', '{"error":"bad"}', '{"error":{"code":7}}', '{"error":{"code":null}}', '{"error":{"message":false}}', '{"error":{"details":null}}', '{"error":{"details":[]}}', '{"error":{"details":"bad"}}'];
    const invalidInside = new Uint8Array([...bytes('{"error":{"code":"'), 0xff, ...bytes('"}}')]);
    for (const status of [401, 503]) for (const chunk of [...malformed.map(bytes), new Uint8Array([0xff, 0xfe]), invalidInside]) {
      const w = wire({ status, type: 'application/json', chunks: [chunk, null] });
      try { generic(await invoke(client(w), { maxBytes: 128 }).catch(e => e), status); w.check(); }
      finally { await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 error K+1 and huge first chunks preserve HTTP status with absent or lying lengths', async () => {
    for (const status of [401, 503]) for (const maxBytes of [4, 9000]) for (const length of [undefined, 1]) {
      const k = Math.min(8192, maxBytes);
      for (const chunks of [[new Uint8Array(k), bytes('!'), null], [new Uint8Array(k + 65), null]]) {
        const w = wire({ status, length, chunks });
        try {
          generic(await invoke(client(w), { maxBytes }).catch(e => e), status);
          assert.equal(w.reads, chunks.length - 1, 'no EOF or further read after overflow');
          w.check({ cancel: true });
        } finally { await w.drain(); }
      }
    }
  });

  test('WEB-MEM-3 T1 known HTTP status survives absent unsupported or unreadable error body', async () => {
    for (const status of [401, 503]) for (const config of [{ noBody: true }, { unsupported: true }, { unsupported: 'throw' }, { chunks: [bytes('{'), new TypeError('lost')] }]) {
      const w = wire({ status, ...config });
      try {
        generic(await invoke(client(w)).catch(e => e), status);
        w.check({ cancel: Boolean(config.chunks) });
      } finally { await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 first 401 callback precedes reads and awaits; detailed result never renotifies', async () => {
    const held = gate(), notices = [], encoded = bytes('{"error":{"code":"specific","details":{"x":1}}}');
    const first = wire({ status: 401, chunks: [encoded, null], readGate: held });
    const second = wire({ status: 401, chunks: [encoded, null] });
    const replies = [first, second];
    const api = createApi({ secret: 's', fetch: (url, init) => replies.shift().fetch(url, init), onUnauthorized: e => {
      notices.push(e); first.events.push('unauthorized'); assert.equal(first.reads, 0);
      assert.ok(!first.events.includes('after-status-turn'), 'notify without awaiting after confirmed headers');
      queueMicrotask(() => first.events.push('notification-turn'));
    } });
    let result;
    try {
      result = observe(invoke(api, { maxBytes: 128 })); await tick();
      assert.equal(notices.length, 1); generic(notices[0], 401);
      assert.ok(first.events.indexOf('unauthorized') < first.events.indexOf('read:1'));
      assert.ok(first.events.indexOf('unauthorized') < first.events.indexOf('notification-turn'));
      assert.equal(result.settled, false); assert.equal(first.releases, 0);
      held.resolve(); await result.done; apiError(result.error, 401, 'specific');
      apiError(await invoke(api, { maxBytes: 128 }).catch(e => e), 401, 'specific');
      assert.equal(notices.length, 1); first.check(); second.check();
    } finally { await first.drain(); await second.drain(); if (result) await result.done; }
  });

  test('WEB-MEM-3 T1 synchronous unauthorized abort selects aborted and waits for cancellation', async () => {
    const controller = new AbortController(), cancelled = gate(), notices = [];
    const w = wire({ status: 401, cancelGate: cancelled }); let result;
    try {
      result = observe(invoke(client(w, { onUnauthorized: e => { notices.push(e); assert.equal(w.reads, 0); controller.abort(); } }), { maxBytes: 4, signal: controller.signal }));
      await tick(); assert.equal(notices.length, 1); assert.equal(w.reads, 0);
      assert.equal(w.events.filter(e => e === 'source-cancel').length, 1);
      assert.equal(result.settled, false); assert.equal(w.releases, 0);
      cancelled.resolve(); await result.done; apiError(result.error, 0, 'aborted');
      w.check({ cancel: w.events.includes('lock') });
    } finally { await w.drain(); if (result) await result.done; }
  });

  test('WEB-MEM-3 T1 incomplete 401 and 503 remain owned until explicit abort and eventual drain', async () => {
    for (const status of [401, 503]) for (const length of [undefined, 1]) {
      const held = gate(), cancelled = gate(), controller = new AbortController(), notices = [];
      const w = wire({ status, length, chunks: [bytes('{')], readGate: held, holdReadAt: 2, cancelGate: cancelled }); let result;
      try {
        result = observe(invoke(client(w, { onUnauthorized: e => notices.push(e) }), { maxBytes: 4, signal: controller.signal }));
        await tick(); assert.equal(w.reads, 2); assert.equal(result.settled, false);
        assert.equal(notices.length, status === 401 ? 1 : 0); assert.equal(w.releases, 0);
        controller.abort(); await tick(); assert.equal(w.cancels, 1); assert.equal(result.settled, false);
        cancelled.resolve(); await tick(); assert.equal(result.settled, false); assert.equal(w.releases, 0);
        held.resolve(); await result.done; apiError(result.error, 0, 'aborted'); w.check({ cancel: true });
      } finally { await w.drain(); if (result) await result.done; }
    }
  });

  test('WEB-MEM-3 T1 mid-read abort keeps unfinished read and cancel owned in either settlement order', async () => {
    for (const cancelFirst of [true, false]) for (const rejectCancel of [false, true]) {
      const held = gate(), cancelled = gate(), controller = new AbortController();
      const settleCancel = () => rejectCancel ? cancelled.reject(new Error('abort cleanup failed')) : cancelled.resolve();
      const w = wire({ chunks: [bytes('ab'), bytes('cd'), null], readGate: held, holdReadAt: 2, cancelGate: cancelled }); let result;
      try {
        result = observe(invoke(client(w), { maxBytes: 4, signal: controller.signal })); await tick();
        assert.equal(w.reads, 2); controller.abort(); await tick();
        assert.equal(w.cancels, 1); assert.equal(result.settled, false); assert.equal(w.releases, 0);
        if (cancelFirst) settleCancel(); else held.resolve(); await tick();
        assert.equal(result.settled, false); assert.equal(w.releases, 0);
        if (cancelFirst) held.resolve(); else settleCancel(); await result.done;
        apiError(result.error, 0, 'aborted'); w.check({ cancel: true });
      } finally { await w.drain(); if (result) await result.done; }
    }
  });

  test('WEB-MEM-3 T1 abort before headers waits for ignored fetch then cancels returned body', async () => {
    const headers = gate(), cancelled = gate(), controller = new AbortController();
    const w = wire({ cancelGate: cancelled }); let result, fetches = 0;
    const api = createApi({ secret: 's', fetch: async (url, init) => { fetches++; await headers.promise; return w.fetch(url, init); } });
    try {
      result = observe(invoke(api, { maxBytes: 4, signal: controller.signal })); await tick();
      assert.equal(fetches, 1); controller.abort(); await tick(); assert.equal(result.settled, false);
      headers.resolve(); await tick(); assert.equal(w.reads, 0);
      assert.equal(w.events.filter(e => e === 'source-cancel').length, 1); assert.equal(result.settled, false);
      cancelled.resolve(); await result.done; apiError(result.error, 0, 'aborted');
      w.check({ cancel: w.events.includes('lock') });
    } finally { headers.resolve(); await w.drain(); if (result) await result.done; }
  });

  test('WEB-MEM-3 T1 read failure and AbortError release locks and cancellation failure preserves cause', async () => {
    for (const status of [200, 401, 503]) for (const abort of [false, true]) {
      const cancelled = gate(), cause = abort ? new DOMException('stop', 'AbortError') : new TypeError('lost');
      const w = wire({ status, chunks: [bytes('a'), cause], cancelGate: cancelled }); let result;
      try {
        result = observe(invoke(client(w))); await tick();
        assert.equal(w.cancels, 1); cancelled.reject(new Error('cleanup failed'));
        await result.done;
        if (abort) apiError(result.error, 0, 'aborted');
        else if (status === 200) apiError(result.error, 0, 'network');
        else generic(result.error, status);
        w.check({ cancel: true });
      } finally { await w.drain(); if (result) await result.done; }
    }
  });

  test('WEB-MEM-3 T1 overflow cleanup stalls or rejects without replacing selected error', async () => {
    for (const status of [200, 401, 503]) for (const reject of [false, true]) {
      const cancelled = gate(), controller = new AbortController(), listeners = trackAbort(controller.signal);
      const w = wire({ status, chunks: [bytes('abcde'), null], cancelGate: cancelled }); let result;
      try {
        result = observe(invoke(client(w), { maxBytes: 4, signal: controller.signal })); await tick();
        assert.equal(w.cancels, 1); assert.equal(result.settled, false); assert.equal(w.releases, 0);
        if (reject) cancelled.reject(new Error('cleanup failed')); else cancelled.resolve();
        await result.done;
        if (status === 200) { apiError(result.error, 0); assert.notEqual(result.error.code, 'aborted'); assert.notEqual(result.error.message, 'cleanup failed'); }
        else generic(result.error, status);
        assert.equal(listeners.size, 0, 'remove abort listeners after failed transport cleanup');
        assert.equal(w.reads, 1); w.check({ cancel: true });
      } finally { await w.drain(); if (result) await result.done; }
    }
  });

  test('WEB-MEM-3 T1 invalid header cleanup waits for stalled cancellation and observes rejection', async () => {
    for (const config of [{ type: 'image/svg+xml' }, { length: 5 }]) {
      const cancelled = gate(), w = wire({ ...config, cancelGate: cancelled }); let result;
      try {
        result = observe(invoke(client(w))); await tick();
        assert.equal(w.reads, 0); assert.equal(result.settled, false); assert.equal(w.releases, 0);
        assert.equal(w.events.filter(e => e === 'source-cancel').length, 1);
        cancelled.reject(new Error('cleanup failed')); await result.done;
        apiError(result.error, 0); assert.notEqual(result.error.message, 'cleanup failed');
        w.check({ cancel: w.events.includes('lock') });
      } finally { await w.drain(); if (result) await result.done; }
    }
  });

  test('WEB-MEM-3 T1 unsuccessful transports never assemble retained chunks into a Blob', async () => {
    const OriginalBlob = globalThis.Blob;
    for (const abort of [false, true]) {
      const held = gate(), controller = new AbortController();
      const w = wire({ chunks: [bytes('abc'), bytes('de'), null], ...(abort ? { readGate: held, holdReadAt: 2 } : {}) });
      let constructions = 0, result;
      try {
        globalThis.Blob = class extends OriginalBlob { constructor(parts, options) { super(parts, options); constructions++; } };
        result = observe(invoke(client(w), { maxBytes: 4, signal: controller.signal })); await tick();
        if (abort) { controller.abort(); await tick(); assert.equal(result.settled, false); held.resolve(); }
        await result.done; apiError(result.error, 0, abort ? 'aborted' : undefined);
        assert.equal(constructions, 0, 'discard accumulated success chunks on failure');
        w.check({ cancel: true });
      } finally { globalThis.Blob = OriginalBlob; held.resolve(); await w.drain(); if (result) await result.done; }
    }
  });

  test('WEB-MEM-3 T1 unauthorized once flag is shared with existing JSON methods in either order', async () => {
    for (const jsonFirst of [false, true]) {
      const wires = [wire({ status: 401 }), wire({ status: 401 })], replies = [...wires], notices = [];
      const api = createApi({ secret: 's', fetch: (url, init) => url === '/v1/peers'
        ? json(401, { error: { code: 'unauthenticated' } }) : replies.shift().fetch(url, init), onUnauthorized: e => notices.push(e) });
      try {
        if (jsonFirst) apiError(await api.peers().catch(e => e), 401, 'unauthenticated');
        generic(await invoke(api).catch(e => e), 401);
        apiError(await api.peers().catch(e => e), 401, 'unauthenticated');
        generic(await invoke(api).catch(e => e), 401);
        assert.equal(notices.length, 1); wires.forEach(w => w.check());
      } finally { for (const w of wires) await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 bounded error ignores oversized length hint and uses existing Retry-After normalization', async () => {
    for (const retry of [' 12 ', '0', '-1', 'tomorrow']) {
      const w = wire({ status: 503, length: 99999, chunks: [bytes('{"error":{"code":"busy"}}'), null] });
      w.response.headers.set('Retry-After', retry);
      try {
        const e = await invoke(client(w), { maxBytes: 64 }).catch(e => e);
        apiError(e, 503, 'busy');
        assert.deepEqual(e.details, retry.trim() === '12' ? { retry_after_seconds: 12 } : {});
        w.check();
      } finally { await w.drain(); }
    }
  });

  // BEGIN WEB-MEM-3 T1 fix1 — additive regressions, reusing the original local helpers.
  test('WEB-MEM-3 T1 fix1 I1 non-coercible retry details preserve bounded envelopes and ordinary JSON errors', async () => {
    for (const route of ['thumbnail', 'json']) for (const status of [401, 503]) for (const retryAfter of [undefined, '17']) {
      const details = { marker: 1, retry_after_seconds: { toString: 0 } };
      const envelope = { error: { code: 'busy', message: 'specific', details } };
      const w = route === 'thumbnail' ? wire({ status, type: 'application/json', chunks: [bytes(JSON.stringify(envelope)), null] }) : null;
      const response = w ? w.response : json(status, envelope), notices = [], calls = [];
      if (retryAfter === undefined) response.headers.delete('Retry-After');
      else response.headers.set('Retry-After', retryAfter);
      const api = createApi({ secret: 'vn_fix1', fetch: w ? w.fetch : async (url, init) => { calls.push({ url, init }); return response; }, onUnauthorized: e => notices.push(e) });
      try {
        const error = await (w ? invoke(api, { maxBytes: 256 }) : api.peers()).catch(e => e);
        apiError(error, status, 'busy'); assert.equal(error.message, 'specific');
        assert.deepEqual(error.details, { marker: 1, retry_after_seconds: retryAfter === undefined ? { toString: 0 } : 17 });
        assert.equal(notices.length, status === 401 ? 1 : 0, 'normalization notifies once, including detailed completion');
        assert.equal(response.bodyUsed, true);
        if (w) {
          assert.equal(w.calls.length, 1); w.check();
          if (status === 401) {
            apiError(notices[0], 401, 'http_401');
            assert.deepEqual(notices[0].details, retryAfter === undefined ? {} : { retry_after_seconds: 17 });
          }
        } else {
          assert.equal(calls.length, 1); assert.equal(calls[0].url, '/v1/peers');
          if (status === 401) apiError(notices[0], 401, 'busy');
        }
      } finally { if (w) await w.drain(); }
    }
  });

  test('WEB-MEM-3 T1 fix1 I2 native cancellable body without getReader retains cleanup ownership', async () => {
    for (const status of [401, 503]) for (const rejectCancel of [false, true]) {
      const cancelled = gate(), w = wire({ status, cancelGate: cancelled }), notices = [];
      const nativeCancel = w.body.cancel.bind(w.body);
      let cancels = 0, result, probe;
      // Hide only reader acquisition on the actual native stream; keep native cancellation.
      Object.defineProperty(w.body, 'getReader', { value: undefined });
      w.body.cancel = reason => { cancels++; return nativeCancel(reason); };
      const api = client(w, { onUnauthorized: e => {
        notices.push(e); w.events.push('unauthorized');
        assert.equal(w.reads, 0); assert.equal(cancels, 0);
        assert.ok(!w.events.includes('after-status-turn'), '401 notifies without awaiting headers');
      } });
      try {
        assert.ok(w.response instanceof Response); assert.ok(w.response.body instanceof ReadableStream);
        assert.equal(w.response.body, w.body); assert.equal(typeof w.body.getReader, 'undefined');
        result = observe(invoke(api)); await tick();
        assert.equal(cancels, 1, 'request cancellation through the retained native cancel method');
        assert.equal(w.events.filter(e => e === 'source-cancel').length, 1, 'native underlying cancellation really ran');
        assert.equal(w.response.bodyUsed, true, 'native cancellation disturbed the real response body');
        assert.equal(w.reads, 0); assert.equal(w.body.locked, false);
        assert.equal(result.settled, false, 'API must retain the pending native cancellation');
        assert.equal(notices.length, status === 401 ? 1 : 0);
        if (status === 401) {
          generic(notices[0], 401);
          assert.ok(w.events.indexOf('unauthorized') < w.events.indexOf('source-cancel'));
        }
        if (rejectCancel) cancelled.reject(new Error('native cleanup rejected')); else cancelled.resolve();
        await result.done; generic(result.error, status);
        assert.equal(cancels, 1); assert.equal(notices.length, status === 401 ? 1 : 0); w.check();
        // Bypass the hidden instance method only after cleanup to inspect the native stream state.
        probe = ReadableStream.prototype.getReader.call(w.body);
        assert.deepEqual(await probe.read(), { done: true, value: undefined });
        probe.releaseLock(); probe = undefined; assert.equal(w.body.locked, false);
      } finally {
        await w.drain(); if (result) await result.done;
        if (probe) probe.releaseLock();
      }
    }
  });

  test('WEB-MEM-3 T1 fix1 M1 abort during selected-error cancellation wins after either cleanup outcome', async () => {
    const scenarios = [{ status: 200 }, { status: 200, type: 'image/svg+xml' }, { status: 401 }, { status: 503 }];
    for (const scenario of scenarios) for (const rejectCancel of [false, true]) {
      const cancelled = gate(), controller = new AbortController(), listeners = trackAbort(controller.signal), notices = [];
      const w = wire({ ...scenario, chunks: [bytes('abcde'), null], cancelGate: cancelled });
      const nativeCancel = w.body.cancel.bind(w.body);
      let bodyCancels = 0, result;
      w.body.cancel = reason => { bodyCancels++; return nativeCancel(reason); };
      try {
        result = observe(invoke(client(w, { onUnauthorized: e => notices.push(e) }), { maxBytes: 4, signal: controller.signal }));
        await tick();
        const hasReader = w.events.includes('lock');
        assert.equal(controller.signal.aborted, false, 'error cleanup starts before caller abort');
        assert.equal(w.cancels + bodyCancels, 1); assert.equal(w.events.filter(e => e === 'source-cancel').length, 1);
        assert.equal(result.settled, false); assert.equal(w.releases, 0); assert.equal(listeners.size, 1);
        assert.equal(w.reads, scenario.type ? 0 : 1);
        if (hasReader) assert.ok(w.events.includes('read-done:1'), 'overflow read settled before selected-error cleanup');
        assert.equal(notices.length, scenario.status === 401 ? 1 : 0);
        controller.abort(); await tick();
        assert.equal(result.settled, false, 'abort cannot release ownership of the held cancellation');
        assert.equal(w.cancels + bodyCancels, 1); assert.equal(w.releases, 0);
        if (rejectCancel) cancelled.reject(new Error('late cleanup rejected')); else cancelled.resolve();
        await result.done; apiError(result.error, 0, 'aborted');
        assert.equal(w.cancels + bodyCancels, 1); assert.equal(listeners.size, 0);
        assert.equal(notices.length, scenario.status === 401 ? 1 : 0);
        w.check({ cancel: hasReader });
      } finally { await w.drain(); if (result) await result.done; }
    }
  });
  // END WEB-MEM-3 T1 fix1
}
// END WEB-MEM-3 T1
