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
