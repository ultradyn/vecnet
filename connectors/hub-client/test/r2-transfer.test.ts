import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createHubClient } from '../src/client.ts';
import { HubError } from '../src/errors.ts';
import { startFakeHub } from '../testing/fake-hub.ts';
import { fakeClock } from '../testing/fixtures.ts';

const classes = ['1h', '6h', '12h', '1d', '7d', '30d', 'keep'] as const;

test('connector follows only a hub-issued transfer grant', async t => {
  const clock = fakeClock(Date.parse('2026-11-06T00:00:00Z'));
  const hub = await startFakeHub({ clock, signedURLs: true });
  t.after(() => hub.close());
  const client = createHubClient({ baseUrl: hub.url, credential: hub.bearer, clock });
  const bytes = new Uint8Array([9, 8, 7]);
  const ref = await client.putBlob(bytes, 'application/octet-stream');
  assert.equal(ref.sha256, createHash('sha256').update(bytes).digest('hex'));
  assert.equal(ref.sha256_verified, true);
  assert.equal(ref.integrity?.verified, true);
  assert.equal('url' in ref, false);
  assert.equal(hub.s3Requests.filter(r => r.method === 'PUT').length, 1);
  assert.deepEqual(hub.s3Requests[0]?.body, Buffer.from(bytes));
  assert.equal(hub.s3Requests[0]?.headers.authorization, undefined);
  assert.equal(hub.requests.some(r => r.method === 'PUT' && r.path === '/v1/blobs'), false);
  await assert.rejects(client.fetchBlob('http://foreign.invalid/object', { maxBytes: 3 }), e => e instanceof HubError && e.code === 'foreign_origin');
  assert.equal(hub.s3Requests.filter(r => r.method === 'GET').length, 0);
  const legacy = `${hub.url}/v1/blobs/${ref.sha256}?b=${ref.blob_id}`;
  assert.deepEqual(await client.fetchBlob(legacy, { maxBytes: 3 }), bytes);
  assert.equal(hub.requests.some(r => r.path === `/v1/blobs/${ref.sha256}/download`), true);
  assert.equal(hub.requests.some(r => r.path === `/v1/blobs/${ref.sha256}` && r.method === 'GET'), false);
  hub.failNextS3(403);
  assert.deepEqual(await client.fetchBlob({ sha256: ref.sha256, blob_id: ref.blob_id! }, { maxBytes: 3 }), bytes);
  assert.equal(hub.downloads.length, 3);
  assert.notEqual(hub.downloads[1]?.url, hub.downloads[2]?.url);
  clock.advance(601_000);
  const stale = await fetch(hub.downloads[0]!.url);
  assert.equal(stale.status, 403);
  assert.deepEqual(await client.fetchBlob({ sha256: ref.sha256, blob_id: ref.blob_id! }, { maxBytes: 3 }), bytes);
  hub.markExpired(ref.blob_id!);
  await assert.rejects(client.fetchBlob({ sha256: ref.sha256, blob_id: ref.blob_id! }, { maxBytes: 3 }), e => e instanceof HubError && e.code === 'blob_expired' && !String(e).includes('X-Amz-') && !e.stack?.includes('X-Amz-'));
  const fresh = await client.putBlob(new Uint8Array([1]), 'text/plain');
  hub.markPurged(fresh.blob_id!);
  await assert.rejects(client.fetchBlob({ sha256: fresh.sha256, blob_id: fresh.blob_id! }, { maxBytes: 1 }), e => e instanceof HubError && e.code === 'blob_purged' && !String(e).includes('http'));
});

test('signed transfer propagates retention, quota, header and mint failures without URLs', async t => {
  const clock = fakeClock();
  const hub = await startFakeHub({ clock, signedURLs: true });
  t.after(() => hub.close());
  const client = createHubClient({ baseUrl: hub.url, credential: hub.bearer, clock });
  for (const retention of classes) {
    hub.setRetention(retention);
    const ref = await client.putBlob(new Uint8Array([4]), 'application/octet-stream');
    assert.equal(ref.retention_class, retention);
    assert.equal(ref.expires_at == null, retention === 'keep');
    assert.equal(ref.sha256_verified, true);
    await client.fetchBlob({ sha256: ref.sha256, blob_id: ref.blob_id! }, { maxBytes: 1 });
    const grant = hub.downloads.at(-1)!;
    assert.equal(grant.object_expires_at == null, retention === 'keep');
    assert.equal(grant.retention_class, retention);
  }
  hub.omitUploadHeader('If-None-Match');
  const before = hub.s3Requests.length;
  await assert.rejects(client.putBlob(new Uint8Array([5]), 'text/plain'), e => e instanceof HubError && e.code === 'blob_unavailable' && !String(e).includes('X-Amz-'));
  assert.equal(hub.s3Requests.length, before);
  hub.failNext('/v1/blobs/uploads', 429, { error: { code: 'blob_storage_quota_exceeded', message: 'full', details: {} } });
  await assert.rejects(client.putBlob(new Uint8Array([6]), 'text/plain'), e => e instanceof HubError && e.code === 'blob_storage_quota_exceeded' && !String(e).includes('http'));
});

const origin = 'https://allowed.example';
const evil = 'https://evil.example';
const blobID = 'blo_00000000000000000000000001';
const startedAt = Date.parse('2026-11-06T00:00:00.000Z');
const uploadHeaders = {
  'Content-Length': '3', 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment',
  'Cache-Control': 'private, no-store', 'If-None-Match': '*', 'X-Amz-Checksum-Sha256': 'required',
};
function json(body: unknown, status = 200, extra: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...extra } });
}
function caps(over: Record<string, unknown> = {}) {
  return { backend: 'r2', upload_mode: 'direct', mcp_transfer_mode: 'signed_urls', checksum_profile: 'sha256', download_origins: [origin], ...over };
}
function proof(sha: string, size: number) {
  return { blob_id: blobID, sha256: sha, sha256_verified: true, integrity: { algorithm: 'sha256', value: sha, verified: true }, size, retention_class: '1d', expires_at: '2026-11-07T00:00:00.000Z', media_type: 'application/octet-stream' };
}
function downloadGrant(sha: string, size: number) {
  return { ...proof(sha, size), method: 'GET', url: `${origin}/o?X-Amz-Signature=download-canary`, expires_at: '2026-11-06T00:10:00.000Z', object_expires_at: '2026-11-07T00:00:00.000Z', headers: {} };
}
interface Seen { url: string; method: string; authorization: string | null; cookie: string | null; bodyBytes: number }
function scripted(handler: (url: URL, init: RequestInit | undefined, seen: Seen[]) => Promise<Response> | Response) {
  const seen: Seen[] = [];
  const fetchImpl: typeof fetch = async (input, init) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
    const headers = new Headers(init?.headers);
    const body = init?.body;
    const bodyBytes = body == null ? 0 : typeof body === 'string' ? Buffer.byteLength(body) : ArrayBuffer.isView(body) ? body.byteLength : 1;
    seen.push({ url: url.href, method: init?.method ?? 'GET', authorization: headers.get('authorization'), cookie: headers.get('cookie'), bodyBytes });
    return handler(url, init, seen);
  };
  return { fetchImpl, seen };
}

test('malicious complete_url does not receive the hub bearer', async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const clock = fakeClock(startedAt);
  for (const complete_url of [`${evil}/steal`, '//evil.example/steal', '/v1/admin/steal', `https://hub.invalid/v1/blobs/uploads/${blobID}/complete`]) {
    const { fetchImpl, seen } = scripted(url => {
      if (url.origin === 'http://hub.invalid' && url.pathname === '/v1/blob-capabilities') return json(caps());
      if (url.origin === 'http://hub.invalid' && url.pathname === '/v1/blobs/uploads') return json({
        blob_id: blobID, sha256: sha, size: bytes.length, retention_class: '1d', expires_at: '2026-11-07T00:00:00.000Z',
        upload: { method: 'PUT', url: `${origin}/o/${blobID}`, headers: { ...uploadHeaders, 'Content-Length': String(bytes.length) }, expires_at: '2026-11-06T00:05:00.000Z' },
        complete_url,
      }, 201);
      if (url.origin === origin) return new Response(null, { status: 200 });
      return json(proof(sha, bytes.length));
    });
    const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock });
    await assert.rejects(client.putBlob(bytes, 'application/octet-stream'), e => e instanceof HubError && e.code === 'bad_response' && !String(e).includes('evil') && !String(e).includes('Bearer'));
    assert.equal(seen.some(call => call.url.startsWith(evil) || call.url.includes('evil.example')), false, complete_url);
    assert.equal(seen.some(call => call.authorization === 'Bearer hub-bearer-secret' && new URL(call.url).origin !== 'http://hub.invalid'), false, complete_url);
    assert.equal(seen.some(call => call.method === 'POST' && call.url.includes('/complete')), false, complete_url);
  }
});

test('direct upload and download reject unlisted origins, bad metadata, and credential headers', async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const clock = fakeClock(startedAt);
  const grant = (upload: Record<string, unknown>, complete = `/v1/blobs/uploads/${blobID}/complete`) => json({
    blob_id: blobID, sha256: sha, size: bytes.length, retention_class: '1d', expires_at: '2026-11-07T00:00:00.000Z', upload, complete_url: complete,
  }, 201);
  const cases: Array<{ name: string; upload?: Record<string, unknown>; download?: Record<string, unknown> }> = [
    { name: 'unlisted upload', upload: { method: 'PUT', url: `${evil}/o`, headers: uploadHeaders, expires_at: '2026-11-06T00:05:00.000Z' } },
    { name: 'userinfo upload', upload: { method: 'PUT', url: 'https://user:pass@allowed.example/o', headers: uploadHeaders, expires_at: '2026-11-06T00:05:00.000Z' } },
    { name: 'http upload', upload: { method: 'PUT', url: 'http://allowed.example/o', headers: uploadHeaders, expires_at: '2026-11-06T00:05:00.000Z' } },
    { name: 'upload method', upload: { method: 'POST', url: `${origin}/o`, headers: uploadHeaders, expires_at: '2026-11-06T00:05:00.000Z' } },
    { name: 'upload deadline', upload: { method: 'PUT', url: `${origin}/o`, headers: uploadHeaders, expires_at: '2026-11-06T00:05:01.000Z' } },
    { name: 'upload credentials', upload: { method: 'PUT', url: `${origin}/o`, headers: { ...uploadHeaders, Authorization: 'Bearer stolen', Cookie: 'sid=1' }, expires_at: '2026-11-06T00:05:00.000Z' } },
    { name: 'unlisted download', download: { method: 'GET', url: `${evil}/o`, expires_at: '2026-11-06T00:10:00.000Z', object_expires_at: '2026-11-07T00:00:00.000Z' } },
    { name: 'download credentials', download: { method: 'GET', url: `${origin}/o`, headers: { Authorization: 'Bearer stolen', Cookie: 'sid=1' }, expires_at: '2026-11-06T00:10:00.000Z', object_expires_at: '2026-11-07T00:00:00.000Z' } },
    { name: 'download deadline', download: { method: 'GET', url: `${origin}/o`, expires_at: '2026-11-06T00:10:01.000Z', object_expires_at: '2026-11-07T00:00:00.000Z' } },
    { name: 'download userinfo', download: { method: 'GET', url: 'https://user:pass@allowed.example/o', expires_at: '2026-11-06T00:10:00.000Z', object_expires_at: '2026-11-07T00:00:00.000Z' } },
  ];
  for (const item of cases) {
    const { fetchImpl, seen } = scripted(url => {
      if (url.pathname === '/v1/blob-capabilities') return json(caps());
      if (url.pathname === '/v1/blobs/uploads') return grant(item.upload ?? { method: 'PUT', url: `${origin}/o`, headers: uploadHeaders, expires_at: '2026-11-06T00:05:00.000Z' });
      if (url.pathname.includes('/download')) return json({ ...downloadGrant(sha, bytes.length), ...(item.download ?? {}) });
      if (url.origin === origin) return new Response(Buffer.from(bytes), { status: 200, headers: { 'Content-Length': '3' } });
      return new Response('foreign', { status: 200 });
    });
    const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock });
    const run = item.download
      ? client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes: 3 })
      : client.putBlob(bytes, 'application/octet-stream');
    await assert.rejects(run, e => e instanceof HubError && e.code === 'blob_unavailable' && !String(e).includes('stolen') && !String(e).includes(evil), item.name);
    assert.equal(seen.some(call => call.url.startsWith(evil) || call.url.includes('user:pass') || call.authorization === 'Bearer stolen' || call.cookie === 'sid=1'), false, item.name);
    if (!item.download) assert.equal(seen.filter(call => call.method === 'PUT').reduce((n, call) => n + call.bodyBytes, 0), 0, item.name);
  }
});

test('malformed R2 discovery never falls back to legacy PUT, and a disk client refetches after the hub becomes R2', async () => {
  const bytes = new Uint8Array([9]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const clock = fakeClock(startedAt);
  const bad = [
    caps({ mcp_transfer_mode: 'base64' }),
    caps({ checksum_profile: 'md5' }),
    caps({ backend: 's3' }),
    caps({ upload_mode: 'legacy' }),
    { backend: 'disk', mcp_transfer_mode: 'signed_urls', download_origins: [] },
  ];
  for (const discovery of bad) {
    const { fetchImpl, seen } = scripted(url => url.pathname === '/v1/blob-capabilities' ? json(discovery) : json({ sha256: sha, size: 1 }, 201));
    const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock });
    await assert.rejects(client.putBlob(bytes, 'text/plain'), e => e instanceof HubError && e.code === 'bad_response');
    assert.equal(seen.filter(call => call.method === 'PUT').reduce((n, call) => n + call.bodyBytes, 0), 0);
    assert.equal(seen.some(call => call.url.includes('/v1/blobs/uploads')), false);
  }
  let mode: 'disk' | 'r2' = 'disk';
  const { fetchImpl, seen } = scripted(url => {
    if (url.pathname === '/v1/blob-capabilities') {
      return json(mode === 'disk'
        ? { backend: 'disk', upload_mode: 'legacy', mcp_transfer_mode: 'base64', download_origins: [] }
        : caps());
    }
    if (url.pathname === '/v1/blobs' && url.search === '') return json({ sha256: sha, size: bytes.length }, 201);
    if (url.pathname === '/v1/blobs/uploads') return json({
      blob_id: blobID, sha256: sha, size: bytes.length, retention_class: 'keep', expires_at: null,
      upload: { method: 'PUT', url: `${origin}/o/${blobID}`, headers: { ...uploadHeaders, 'Content-Length': '1' }, expires_at: '2026-11-06T00:05:00.000Z' },
      complete_url: `/v1/blobs/uploads/${blobID}/complete`,
    }, 201);
    if (url.pathname.endsWith('/complete')) return json({ ...proof(sha, 1), retention_class: 'keep', expires_at: null });
    if (url.origin === origin) return new Response(null, { status: 200 });
    return json({ error: { code: 'not_found' } }, 404);
  });
  const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock });
  const legacy = await client.putBlob(bytes, 'text/plain');
  assert.equal(legacy.sha256, sha);
  assert.equal(legacy.blob_id, undefined);
  const legacyPuts = seen.filter(call => call.method === 'PUT' && call.url.includes('/v1/blobs')).length;
  mode = 'r2';
  const direct = await client.putBlob(bytes, 'text/plain');
  assert.equal(direct.sha256_verified, true);
  assert.equal(seen.filter(call => call.method === 'PUT' && new URL(call.url).pathname === '/v1/blobs').length, legacyPuts);
  assert.equal(seen.filter(call => call.method === 'PUT' && call.url.startsWith(origin)).length, 1);
});

test('direct_upload_required refetches discovery once and does not repeat a legacy PUT', async () => {
  const bytes = new Uint8Array([4]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  let discoveries = 0;
  const { fetchImpl, seen } = scripted(url => {
    if (url.pathname === '/v1/blob-capabilities') {
      discoveries += 1;
      return json(discoveries === 1 ? { backend: 'disk', upload_mode: 'legacy', mcp_transfer_mode: 'base64', download_origins: [] } : caps());
    }
    if (url.pathname === '/v1/blobs') return json({ error: { code: 'direct_upload_required', message: 'direct' } }, 409);
    if (url.pathname === '/v1/blobs/uploads') return json({
      blob_id: blobID, sha256: sha, size: 1, retention_class: '1d', expires_at: '2026-11-07T00:00:00.000Z',
      upload: { method: 'PUT', url: `${origin}/o`, headers: { ...uploadHeaders, 'Content-Length': '1' }, expires_at: '2026-11-06T00:05:00.000Z' },
      complete_url: `/v1/blobs/uploads/${blobID}/complete`,
    }, 201);
    if (url.pathname.endsWith('/complete')) return json(proof(sha, 1));
    if (url.origin === origin) return new Response(null, { status: 200 });
    return json({ error: { code: 'not_found' } }, 404);
  });
  const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
  const ref = await client.putBlob(bytes, 'text/plain');
  assert.equal(ref.sha256_verified, true);
  assert.equal(seen.filter(call => call.method === 'PUT' && new URL(call.url).pathname === '/v1/blobs').length, 1);
  assert.equal(discoveries, 2);
});

test('an old hub 404 still uses one legacy PUT', async () => {
  const bytes = new Uint8Array([8]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const { fetchImpl, seen } = scripted(url => {
    if (url.pathname === '/v1/blob-capabilities') return json({ error: { code: 'not_found' } }, 404);
    if (url.pathname === '/v1/blobs') return json({ sha256: sha, size: 1 }, 201);
    return json({ error: { code: 'not_found' } }, 404);
  });
  const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
  const ref = await client.putBlob(bytes, 'text/plain');
  assert.equal(ref.sha256, sha);
  assert.equal(seen.filter(call => call.method === 'PUT' && call.url.endsWith('/v1/blobs')).length, 1);
});

test('a stalled direct PUT is cancelled by the injected clock and does not complete', async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const clock = fakeClock(startedAt);
  let aborted = false;
  let started!: () => void;
  const uploadStarted = new Promise<void>(resolve => { started = resolve; });
  let completed = false;
  const { fetchImpl } = scripted((url, init) => {
    if (url.pathname === '/v1/blob-capabilities') return json(caps());
    if (url.pathname === '/v1/blobs/uploads') return json({
      blob_id: blobID, sha256: sha, size: bytes.length, retention_class: '1d', expires_at: '2026-11-07T00:00:00.000Z',
      upload: { method: 'PUT', url: `${origin}/o`, headers: uploadHeaders, expires_at: '2026-11-06T00:05:00.000Z' },
      complete_url: `/v1/blobs/uploads/${blobID}/complete`,
    }, 201);
    if (url.origin === origin) {
      started();
      return new Promise((_resolve, reject) => {
        const signal = init?.signal;
        const abort = () => { aborted = true; reject(new DOMException('aborted', 'AbortError')); };
        if (signal?.aborted) abort();
        else signal?.addEventListener('abort', abort, { once: true });
      });
    }
    completed = true;
    return json(proof(sha, bytes.length));
  });
  const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock, timeoutMs: 10 });
  let outcome: HubError | 'pending' | 'completed' = 'pending';
  const pending = client.putBlob(bytes, 'application/octet-stream').then(() => { outcome = 'completed'; }, err => { outcome = err as HubError; });
  const label = (value: HubError | 'pending' | 'completed') => value === 'pending' || value === 'completed' ? value : `${value.code} ${String(value)}`;
  await uploadStarted;
  clock.advance(299_999);
  await Promise.resolve();
  assert.equal(aborted, false);
  assert.equal(label(outcome), 'pending');
  clock.advance(2);
  await Promise.race([pending, new Promise(resolve => setTimeout(resolve, 100))]);
  assert.equal(aborted, true);
  const settled = label(outcome);
  assert.equal(settled.startsWith('blob_unavailable'), true, settled);
  assert.equal(settled.includes(origin), false);
  assert.equal(completed, false);
});

test('a large provider 403 is reminted once and a persistent 403 stops after that remint', async () => {
  const bytes = new Uint8Array([7]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const xml = `<Error><Code>AccessDenied</Code><Message>${'x'.repeat(2048)}</Message></Error>`;
  for (const persistent of [false, true]) {
    let mints = 0;
    let downloads = 0;
    const { fetchImpl } = scripted(url => {
      if (url.pathname === '/v1/blob-capabilities') return json(caps());
      if (url.pathname.endsWith('/download')) {
        mints += 1;
        return json({ ...downloadGrant(sha, bytes.length), url: `${origin}/o?n=${mints}` });
      }
      downloads += 1;
      if (persistent || downloads === 1) return new Response(xml, { status: 403, headers: { 'Content-Type': 'application/xml', 'Content-Length': String(Buffer.byteLength(xml)) } });
      return new Response(Buffer.from(bytes), { status: 200, headers: { 'Content-Length': String(bytes.length) } });
    });
    const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
    if (!persistent) {
      assert.deepEqual(await client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes: 1 }), bytes);
      assert.equal(mints, 2);
      assert.equal(downloads, 2);
    } else {
      await assert.rejects(client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes: 1 }), e => e instanceof HubError && e.status === 403 && e.code === 'blob_unavailable');
      assert.equal(mints, 2);
      assert.ok(mints <= 2);
      assert.equal(downloads, 2);
    }
  }
});

test('a reminted download grant must still match the requested reference and proof', async () => {
  const bytes = new Uint8Array([7]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  let mints = 0;
  const { fetchImpl, seen } = scripted(url => {
    if (url.pathname === '/v1/blob-capabilities') return json(caps());
    if (url.pathname.endsWith('/download')) {
      mints += 1;
      return json({ ...downloadGrant(sha, bytes.length), sha256_verified: mints === 1 });
    }
    return new Response('denied', { status: 403 });
  });
  const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
  await assert.rejects(client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes: 1 }), e => e instanceof HubError && e.code === 'blob_unavailable');
  assert.equal(mints, 2);
  assert.equal(seen.filter(call => new URL(call.url).origin === origin).length, 1);
});

test('direct downloads reject malformed requested references and byte caps before making requests', async () => {
  const sha = 'ab'.repeat(32);
  const { fetchImpl, seen } = scripted(() => json(caps()));
  const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
  for (const ref of [{ sha256: '../other', blob_id: blobID }, { sha256: sha, blob_id: 'other' }]) {
    await assert.rejects(client.fetchBlob(ref, { maxBytes: 1 }), e => e instanceof HubError && e.code === 'blob_unavailable');
  }
  for (const maxBytes of [-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
    await assert.rejects(client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes }), e => e instanceof HubError && e.code === 'too_large');
  }
  assert.equal(seen.length, 0);
});

test('direct downloads reject missing or mismatched grant identity, size and proof before contacting storage', async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const wrongSha = 'ab'.repeat(32);
  const valid = downloadGrant(sha, bytes.length);
  const cases: Array<{ name: string; grant: unknown }> = [
    { name: 'null grant', grant: null },
    { name: 'missing reference and proof', grant: { method: valid.method, url: valid.url, expires_at: valid.expires_at } },
    { name: 'missing blob ID', grant: { ...valid, blob_id: undefined } },
    { name: 'wrong blob ID', grant: { ...valid, blob_id: 'blo_00000000000000000000000002' } },
    { name: 'missing SHA', grant: { ...valid, sha256: undefined } },
    { name: 'wrong SHA', grant: { ...valid, sha256: wrongSha } },
    { name: 'missing verified SHA', grant: { ...valid, sha256_verified: undefined } },
    { name: 'unverified SHA', grant: { ...valid, sha256_verified: false } },
    { name: 'truthy verified SHA', grant: { ...valid, sha256_verified: 'true' } },
    { name: 'missing integrity', grant: { ...valid, integrity: undefined } },
    { name: 'null integrity', grant: { ...valid, integrity: null } },
    { name: 'missing verified integrity', grant: { ...valid, integrity: { algorithm: 'sha256', value: sha } } },
    { name: 'unverified integrity', grant: { ...valid, integrity: { ...valid.integrity, verified: false } } },
    { name: 'truthy verified integrity', grant: { ...valid, integrity: { ...valid.integrity, verified: 'true' } } },
    { name: 'wrong integrity algorithm', grant: { ...valid, integrity: { ...valid.integrity, algorithm: 'md5' } } },
    { name: 'missing integrity digest', grant: { ...valid, integrity: { algorithm: 'sha256', verified: true } } },
    { name: 'wrong integrity digest', grant: { ...valid, integrity: { ...valid.integrity, value: wrongSha } } },
    { name: 'missing size', grant: { ...valid, size: undefined } },
    { name: 'zero size', grant: { ...valid, size: 0 } },
    { name: 'negative size', grant: { ...valid, size: -1 } },
    { name: 'fractional size', grant: { ...valid, size: 1.5 } },
    { name: 'string size', grant: { ...valid, size: '3' } },
    { name: 'unsafe size', grant: { ...valid, size: Number.MAX_SAFE_INTEGER + 1 } },
    { name: 'size over caller cap', grant: { ...valid, size: bytes.length + 1 } },
  ];
  for (const item of cases) {
    const { fetchImpl, seen } = scripted(url => {
      if (url.pathname === '/v1/blob-capabilities') return json(caps());
      if (url.pathname.endsWith('/download')) return json(item.grant);
      return new Response(Buffer.from(bytes));
    });
    const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
    await assert.rejects(client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes: bytes.length }), e => e instanceof HubError && e.code === 'blob_unavailable' && !JSON.stringify(e).includes('download-canary') && !String(e).includes(origin), item.name);
    assert.equal(seen.length, 2, item.name);
    assert.equal(seen.some(call => new URL(call.url).origin === origin), false, item.name);
  }
});

test('direct full downloads require HTTP 200 and the recorded byte count and SHA', async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  const cases: Array<{ name: string; status: number; body: number[] | null; length?: number; error?: string }> = [
    { name: 'matching body with length', status: 200, body: [1, 2, 3], length: 3 },
    { name: 'matching body without length', status: 200, body: [1, 2, 3] },
    { name: 'wrong digest at matching size', status: 200, body: [3, 2, 1], length: 3, error: 'blob_proof_mismatch' },
    { name: 'truncated body with recorded length', status: 200, body: [1, 2], length: 3, error: 'blob_proof_mismatch' },
    { name: 'truncated body with short length', status: 200, body: [1, 2], length: 2, error: 'blob_proof_mismatch' },
    { name: 'truncated body without length', status: 200, body: [1, 2], error: 'blob_proof_mismatch' },
    { name: 'empty body', status: 200, body: null, error: 'blob_proof_mismatch' },
    { name: 'body over recorded size', status: 200, body: [1, 2, 3, 4], error: 'too_large' },
    { name: 'length over recorded size', status: 200, body: [1, 2, 3], length: 4, error: 'too_large' },
    { name: 'unsolicited partial body', status: 206, body: [1, 2], length: 2, error: 'blob_unavailable' },
    { name: 'unsolicited 206 with matching whole body', status: 206, body: [1, 2, 3], length: 3, error: 'blob_unavailable' },
    { name: 'unsolicited no content', status: 204, body: null, error: 'blob_unavailable' },
    { name: 'unsolicited created', status: 201, body: [1, 2, 3], error: 'blob_unavailable' },
  ];
  for (const item of cases) {
    const { fetchImpl, seen } = scripted((url, init) => {
      if (url.pathname === '/v1/blob-capabilities') return json(caps());
      if (url.pathname.endsWith('/download')) return json(downloadGrant(sha, bytes.length));
      assert.equal(new URL(url).origin, origin);
      assert.equal(init?.redirect, 'error');
      assert.equal(new Headers(init?.headers).get('range'), null);
      return new Response(item.body === null ? null : Buffer.from(item.body), { status: item.status, headers: item.length === undefined ? {} : { 'Content-Length': String(item.length) } });
    });
    const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
    // A larger caller cap must not let an oversized object bypass its recorded size.
    const pending = client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes: bytes.length + 1 });
    if (item.error) {
      await assert.rejects(pending, e => e instanceof HubError && e.code === item.error && !JSON.stringify(e).includes('download-canary') && !String(e).includes(origin), item.name);
    } else {
      assert.deepEqual(await pending, bytes, item.name);
    }
    assert.equal(seen.filter(call => call.url.includes('/download')).length, 1, item.name);
    const providerCalls = seen.filter(call => new URL(call.url).origin === origin);
    assert.equal(providerCalls.length, 1, item.name);
    assert.equal(providerCalls[0]?.authorization, null, item.name);
    assert.equal(providerCalls[0]?.cookie, null, item.name);
  }
});

test('direct downloads bound chunked reads and cancel after exceeding the recorded size', async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const sha = createHash('sha256').update(bytes).digest('hex');
  for (const oversized of [false, true]) {
    let reads = 0;
    let cancelled = false;
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        reads += 1;
        if (reads === 1) controller.enqueue(bytes.subarray(0, 1));
        else if (reads === 2) controller.enqueue(oversized ? new Uint8Array([2, 3, 4]) : bytes.subarray(1));
        else if (oversized) throw new Error('must stop reading');
        else controller.close();
      },
      cancel() { cancelled = true; },
    }, { highWaterMark: 0 });
    const { fetchImpl } = scripted(url => {
      if (url.pathname === '/v1/blob-capabilities') return json(caps());
      if (url.pathname.endsWith('/download')) return json(downloadGrant(sha, bytes.length));
      return new Response(body);
    });
    const client = createHubClient({ baseUrl: 'http://hub.invalid', credential: 'hub-bearer-secret', fetch: fetchImpl, clock: fakeClock(startedAt) });
    const pending = client.fetchBlob({ sha256: sha, blob_id: blobID }, { maxBytes: 100 });
    if (oversized) {
      await assert.rejects(pending, e => e instanceof HubError && e.code === 'too_large');
      assert.equal(reads, 2);
      assert.equal(cancelled, true);
    } else {
      assert.deepEqual(await pending, bytes);
      assert.equal(reads, 3);
    }
    assert.equal(body.locked, false);
  }
});

test('the 31st mint returns blob_url_rate_limited and no URL', async t => {
  const hub = await startFakeHub({ clock: fakeClock(), signedURLs: true });
  t.after(() => hub.close());
  const client = createHubClient({ baseUrl: hub.url, credential: hub.bearer, clock: fakeClock() });
  const ref = await client.putBlob(new Uint8Array([1, 2]), 'text/plain');
  for (let i = 0; i < 30; i++) await client.fetchBlob({ sha256: ref.sha256, blob_id: ref.blob_id! }, { maxBytes: 2 });
  const s3Gets = hub.s3Requests.filter(r => r.method === 'GET').length;
  await assert.rejects(client.fetchBlob({ sha256: ref.sha256, blob_id: ref.blob_id! }, { maxBytes: 2 }), e => e instanceof HubError && e.code === 'blob_url_rate_limited' && e.retryAfterMs === 2000 && e.details.retry_after_seconds === 2 && !String(e).includes('X-Amz-') && !JSON.stringify(e.details).includes('http'));
  assert.equal(hub.s3Requests.filter(r => r.method === 'GET').length, s3Gets);
});
