import test from 'node:test';
import assert from 'node:assert/strict';
import { APIError, createApi } from '../static/api.js';
import { createAttachments } from '../static/attach.js';
import { fakeR2, fileOf, settle, until } from './r2-fixture.mjs';
import {
  MAX_ATTACHMENTS, MAX_BYTES, SPILL_SLOT, attachmentName, canAdd, errorText, mediaTypeOf, ready, retryKey, sendRefusal, signature, toAttachments,
} from '../static/attach-model.js';

const MiB = 1 << 20;
const sha = c => c.repeat(64);
const done = (c, extra = {}) => ({ id: c, name: c + '.txt', size: 3, media_type: 'text/plain', state: 'done', sha256: sha(c), ...extra });
// A file stand-in: the model reads only name, size and type.
const file = (name, size, type = 'application/octet-stream') => ({ name, size, type });

test('attachmentName: picked name cleaned and cut to 255 chars; pasted → pasted-YYYYMMDD-HHMMSS.png from local getters', () => {
  const now = new Date(2026, 10, 3, 9, 5, 7); // local time, 3 Nov 2026 09:05:07
  // A picked or dropped file keeps its own name.
  assert.equal(attachmentName(new File(['x'], 'report.pdf', { type: 'application/pdf' }), now), 'report.pdf');
  // Controls and bidi overrides are dropped; line breaks and tabs do not survive in a name.
  assert.equal(attachmentName(file('evil‮gnp.exe', 1), now), 'evilgnp.exe');
  assert.equal(attachmentName(file('a\nb\tc\u0007.txt', 1), now), 'a b c.txt');
  // Cut to 255 characters, counted as code points: 150 × é (2 UTF-8 bytes each) stays whole.
  assert.equal(attachmentName(file('é'.repeat(150), 1), now), 'é'.repeat(150));
  const long = attachmentName(file('x'.repeat(300) + '.png', 1), now);
  assert.equal([...long].length, 255);
  assert.equal(long, 'x'.repeat(255));
  // An astral character is never split in half at the cut.
  const astral = attachmentName(file('😀'.repeat(300), 1), now);
  assert.equal([...astral].length, 255);
  assert.equal(astral, '😀'.repeat(255));
  // A nameless picked file still gets a name.
  assert.ok(attachmentName(file('', 1), now).length > 0);

  // A pasted file is always named from local getters, ext from its type, whatever the browser called it.
  assert.equal(attachmentName(file('image.png', 10, 'image/png'), now, { pasted: true }), 'pasted-20261103-090507.png');
  assert.equal(attachmentName(file('image.png', 10, 'image/jpeg'), now, { pasted: true }), 'pasted-20261103-090507.jpg');
  assert.equal(attachmentName(file('image.png', 10, 'image/gif'), now.getTime(), { pasted: true }), 'pasted-20261103-090507.gif');
  assert.equal(attachmentName(file('image.png', 10, 'IMAGE/WEBP'), now, { pasted: true }), 'pasted-20261103-090507.webp');
  assert.equal(attachmentName(file('notes.txt', 10, 'text/plain'), now, { pasted: true }), 'pasted-20261103-090507.bin');
  assert.equal(attachmentName(file('x', 10, ''), new Date(2026, 0, 2, 23, 59, 58), { pasted: true }), 'pasted-20260102-235958.bin');

  // mediaTypeOf is the declared type, never sniffed, else application/octet-stream.
  assert.equal(mediaTypeOf(file('a.png', 1, 'image/png')), 'image/png');
  assert.equal(mediaTypeOf(file('a.txt', 1, 'text/plain;charset=utf-8')), 'text/plain;charset=utf-8');
  assert.equal(mediaTypeOf(file('a.png', 1, '')), 'application/octet-stream');
  assert.equal(mediaTypeOf(file('a.png', 1, 'not a type')), 'application/octet-stream');
});

test('canAdd: refuses the 17th, a 25 MiB + 1 file and an empty file, each with its reason', () => {
  assert.equal(MAX_ATTACHMENTS, 16);
  assert.equal(MAX_BYTES, 25 * MiB);
  const fifteen = Array.from({ length: 15 }, (_, i) => done(String.fromCharCode(97 + i)));
  const big = file('big.iso', 25 * MiB + 1), empty = file('empty.txt', 0), edge = file('edge.bin', 25 * MiB);
  const sixteenth = file('16.png', 10, 'image/png'), seventeenth = file('17.png', 10, 'image/png');
  const got = canAdd(fifteen, [big, empty, edge, sixteenth, seventeenth]);
  // Exactly 25 MiB fits; a refused file does not take a slot.
  assert.deepEqual(got.files, [edge]);
  assert.deepEqual(got.refused, [
    { name: 'big.iso', reason: 'too_large' },
    { name: 'empty.txt', reason: 'empty' },
    { name: '16.png', reason: 'too_many' },
    { name: '17.png', reason: 'too_many' },
  ]);

  // From empty: the 17th of seventeen is the one refused.
  const many = Array.from({ length: 17 }, (_, i) => file(i + '.txt', 1));
  const all = canAdd([], many);
  assert.deepEqual(all.files, many.slice(0, 16));
  assert.deepEqual(all.refused, [{ name: '16.txt', reason: 'too_many' }]);
  // Failed and uploading chips hold their slots too.
  const held = canAdd([...fifteen.slice(0, 14), { ...done('y'), state: 'failed' }, { ...done('z'), state: 'uploading' }], [file('n.txt', 1)]);
  assert.deepEqual(held, { files: [], refused: [{ name: 'n.txt', reason: 'too_many' }] });
  // A FileList-like input is accepted, and a refused name is cleaned.
  const listy = canAdd([], { length: 1, 0: file('bad‮name', 0), [Symbol.iterator]: Array.prototype[Symbol.iterator] });
  assert.deepEqual(listy, { files: [], refused: [{ name: 'badname', reason: 'empty' }] });
});

test('sendRefusal: 16 done chips and 5000-byte text → the spill-slot message; 15 chips → empty', () => {
  const sixteen = Array.from({ length: 16 }, (_, i) => done(String.fromCharCode(97 + i)));
  const text = 'a'.repeat(5000);
  assert.equal(SPILL_SLOT, 'Remove one attachment or shorten the text: the long text needs an attachment slot.');
  assert.equal(sendRefusal(sixteen, text), SPILL_SLOT);
  assert.equal(sendRefusal(sixteen.slice(0, 15), text), '');
  // The limit is 4096 UTF-8 bytes: 4096 fits, 4097 spills; é is two bytes.
  assert.equal(sendRefusal(sixteen, 'a'.repeat(4096)), '');
  assert.equal(sendRefusal(sixteen, 'a'.repeat(4097)), SPILL_SLOT);
  assert.equal(sendRefusal(sixteen, 'é'.repeat(2049)), SPILL_SLOT);
  assert.equal(sendRefusal(sixteen, 'é'.repeat(2048)), '');
  assert.equal(sendRefusal([], ''), '');
  assert.equal(sendRefusal(sixteen, undefined), '');
});

test('signature changes when a chip is removed or reordered, not when progress changes', () => {
  const a = done('a'), b = done('b'), up = { id: 'u', name: 'u.bin', size: 100, media_type: 'application/octet-stream', state: 'uploading', loaded: 10, total: 100 };
  const base = signature([a, b, up]);
  assert.equal(base, sha('a') + ',' + sha('b'));
  // Progress on an uploading chip leaves it alone.
  assert.equal(signature([a, b, { ...up, loaded: 60 }]), base);
  assert.equal(signature([{ ...a, loaded: 3, total: 3 }, b, up]), base);
  // Removing or reordering changes it.
  assert.notEqual(signature([a, up]), base);
  assert.notEqual(signature([b, a, up]), base);
  assert.equal(signature([]), '');
  // toAttachments: done chips in order, as the schema names them.
  assert.deepEqual(toAttachments([b, up, a]), [
    { sha256: sha('b'), size: 3, media_type: 'text/plain', name: 'b.txt' },
    { sha256: sha('a'), size: 3, media_type: 'text/plain', name: 'a.txt' },
  ]);
});

// FakeXHR records what api.upload asks of XMLHttpRequest and lets a test answer.
function fakeXHR() {
  const made = [];
  class FakeXHR {
    constructor() {
      this.headers = {};
      this.upload = {};
      this.status = 0;
      this.statusText = '';
      this.responseText = '';
      this.aborted = false;
      made.push(this);
    }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(k, v) { this.headers[k] = v; }
    send(body) { this.body = body; }
    abort() { this.aborted = true; if (this.onabort) this.onabort(); }
    progress(loaded, total) { this.upload.onprogress({ loaded, total, lengthComputable: true }); }
    reply(status, body, statusText = '') {
      this.status = status;
      this.statusText = statusText;
      this.responseText = typeof body === 'string' ? body : JSON.stringify(body);
      this.onload();
    }
    fail() { this.onerror(); }
  }
  return { made, XHR: FakeXHR };
}
const tick = () => new Promise(r => setImmediate(r));
// An old hub: no /v1/blob-capabilities, so uploads keep the legacy hub PUT.
const oldHub = async path => {
  if (String(path) === '/v1/blob-capabilities') return new Response(JSON.stringify({ error: { code: 'not_found', message: 'no such endpoint', details: {} } }), { status: 404, headers: { 'Content-Type': 'application/json' } });
  throw new Error('a legacy upload fetches nothing but discovery: ' + path);
};

test('api.upload on an old hub (capabilities 404): PUT /v1/blobs?ttl=86400 with bearer and Content-Type, reports progress, resolves {sha256,size}', async () => {
  const { made, XHR } = fakeXHR();
  const api = createApi({ secret: 'vn_secret', fetch: oldHub, XHR });
  const f = new File(['hello world'], 'shot.png', { type: 'image/png' });
  const seen = [];
  const p = api.upload(f, { onProgress: (loaded, total) => seen.push([loaded, total]) });
  await settle();
  assert.equal(made.length, 1);
  const x = made[0];
  assert.equal(x.method, 'PUT');
  assert.equal(x.url, '/v1/blobs?ttl=86400');
  assert.equal(x.headers.Authorization, 'Bearer vn_secret');
  assert.equal(x.headers['Content-Type'], 'image/png');
  assert.equal(x.body, f, 'the File itself is the body, so the browser sends its exact Content-Length');
  assert.ok(!x.url.includes('vn_secret'));
  x.progress(5, 11);
  x.progress(11, 11);
  assert.deepEqual(seen, [[5, 11], [11, 11]]);
  x.reply(201, { sha256: sha('c'), size: 11 });
  assert.deepEqual(await p, { sha256: sha('c'), size: 11 });

  // An untyped file goes up as application/octet-stream; no onProgress is fine.
  const q = api.upload(new File(['z'], 'blob'));
  await settle();
  assert.equal(made[1].headers['Content-Type'], 'application/octet-stream');
  made[1].reply(200, { sha256: sha('d'), size: 1 });
  assert.deepEqual(await q, { sha256: sha('d'), size: 1 });

  // A 2xx with an unreadable body is a lost reply.
  const r = api.upload(new File(['z'], 'z'));
  await settle();
  made[2].reply(200, '<html>');
  const lost = await r.catch(e => e);
  assert.ok(lost instanceof APIError);
  assert.equal(lost.status, 0);
  assert.equal(lost.code, 'network');
});

test('api.upload: 413 → APIError blob_too_large with details.limit; abort → aborted; 401 fires onUnauthorized once', async () => {
  const { made, XHR } = fakeXHR();
  let signedOut = 0;
  const api = createApi({ secret: 's', XHR, fetch: oldHub, onUnauthorized: () => { signedOut++; } });

  const big = api.upload(new File(['x'], 'big.iso'));
  await settle();
  made[0].reply(413, { error: { code: 'blob_too_large', message: 'blob exceeds the per-blob cap', details: { limit: 25 * MiB } } }, 'Payload Too Large');
  const e413 = await big.catch(e => e);
  assert.ok(e413 instanceof APIError);
  assert.equal(e413.status, 413);
  assert.equal(e413.code, 'blob_too_large');
  assert.equal(e413.message, 'blob exceeds the per-blob cap');
  assert.deepEqual(e413.details, { limit: 25 * MiB });

  // A proxy page: the status still says what happened, as request() does.
  const proxied = api.upload(new File(['x'], 'p'));
  await settle();
  made[1].reply(502, '<html>Bad gateway</html>', 'Bad Gateway');
  const e502 = await proxied.catch(e => e);
  assert.equal(e502.status, 502);
  assert.equal(e502.code, 'http_502');
  assert.equal(e502.message, 'Bad Gateway');
  assert.deepEqual(e502.details, {});

  // Abort through the signal → status 0 code aborted, and the XHR is aborted.
  const ctrl = new AbortController();
  const gone = api.upload(new File(['x'], 'a'), { signal: ctrl.signal });
  await settle();
  ctrl.abort();
  const eAbort = await gone.catch(e => e);
  assert.ok(made[2].aborted);
  assert.ok(eAbort instanceof APIError);
  assert.equal(eAbort.status, 0);
  assert.equal(eAbort.code, 'aborted');
  // An already-aborted signal never opens a request.
  const pre = new AbortController();
  pre.abort();
  const ePre = await api.upload(new File(['x'], 'a'), { signal: pre.signal }).catch(e => e);
  assert.equal(ePre.code, 'aborted');
  assert.equal(made.length, 3);

  // Unreached hub → status 0 code network.
  const off = api.upload(new File(['x'], 'n'));
  await settle();
  made[3].fail();
  const eNet = await off.catch(e => e);
  assert.equal(eNet.status, 0);
  assert.equal(eNet.code, 'network');

  // 401 from uploads and from fetch requests shares one once-flag.
  const denied = { error: { code: 'unauthenticated', message: 'no', details: {} } };
  const u1 = api.upload(new File(['x'], '1'));
  const u2 = api.upload(new File(['x'], '2'));
  await settle();
  made[4].reply(401, denied);
  made[5].reply(401, denied);
  for (const e of await Promise.all([u1.catch(e => e), u2.catch(e => e)])) {
    assert.equal(e.status, 401);
    assert.equal(e.code, 'unauthenticated');
  }
  assert.equal(signedOut, 1);

  let fetched = 0;
  const shared = createApi({
    secret: 's', XHR,
    fetch: async () => new Response(JSON.stringify(denied), { status: 401, headers: { 'Content-Type': 'application/json' } }),
    onUnauthorized: () => { fetched++; },
  });
  await shared.peers().catch(() => {});
  // Discovery is a fetch request too: its 401 shares the once-flag, and a
  // refused discovery never falls back to a hub PUT.
  const u3 = await shared.upload(new File(['x'], '3')).catch(e => e);
  assert.equal(u3.status, 401);
  await tick();
  assert.equal(fetched, 1);
  assert.equal(made.length, 6, 'no PUT after a refused discovery');
});

test('errorText: 429 shows the en-AU time from retry_after_seconds; 413 without details.limit says 25 MiB', () => {
  const nowMs = new Date(2026, 10, 3, 10, 0, 0).getTime(); // 10:00 local
  const quota = new APIError(429, 'upload_quota_exceeded', 'daily upload quota exceeded', { limit: 500 * MiB, retry_after_seconds: 5400 });
  const t429 = errorText(quota, nowMs);
  assert.ok(t429.includes('11:30'), t429);
  assert.ok(!/am|pm/i.test(t429), t429);
  const beforeMidnight = new Date(2026, 10, 3, 23, 0, 0).getTime();
  assert.equal(errorText(quota, beforeMidnight), 'Daily upload quota reached; try again at 4 Nov 00:30.');
  // 24-hour: 13:45, not 1:45.
  assert.ok(errorText(new APIError(429, 'upload_quota_exceeded', '', { retry_after_seconds: 13500 }), nowMs).includes('13:45'));
  // No retry time still reads as a 429.
  assert.ok(errorText(new APIError(429, 'upload_quota_exceeded', '', {}), nowMs).length > 0);

  const t413 = errorText(new APIError(413, 'blob_too_large', 'blob exceeds the per-blob cap', {}), nowMs);
  assert.ok(t413.includes('25 MiB'), t413);
  assert.ok(errorText(new APIError(413, 'blob_too_large', '', { limit: 10 * MiB }), nowMs).includes('10 MiB'));
  assert.ok(errorText(new APIError(413, 'blob_too_large', '', { limit: 2048 }), nowMs).includes('at most 2.0 KiB a file'));
  assert.equal(errorText(new APIError(413, 'payload_too_large', 'too many', { field: 'attachments', limit: 15 }), nowMs), SPILL_SLOT);
  // Only blob_too_large is a per-file size; other 413s say what the hub said.
  const meta = errorText(new APIError(413, 'payload_too_large', 'metadata has no room', { field: 'metadata', limit: 31 }), nowMs);
  assert.equal(meta, 'Upload failed: metadata has no room (413)');
  const body = errorText(new APIError(413, 'payload_too_large', 'request body exceeds 256 KiB', { limit: 262144 }), nowMs);
  assert.ok(!/MiB a file/.test(body) && body.includes('256 KiB'), body);

  const net = errorText(new APIError(0, 'network', 'hub unreachable'), nowMs);
  const abort = errorText(new APIError(0, 'aborted', 'request aborted'), nowMs);
  const other = errorText(new APIError(422, 'invalid_value', 'Content-Type is not a media type', { field: 'content_type' }), nowMs);
  assert.ok(/reach/i.test(net), net);
  assert.ok(/cancel/i.test(abort), abort);
  assert.ok(other.includes('Content-Type is not a media type'), other);
  assert.equal(new Set([t429, t413, net, abort, other]).size, 5);
  // A plain Error still gets a line.
  assert.ok(errorText(new Error('boom'), nowMs).includes('boom'));
});

test('ready is false while any chip uploads or failed', () => {
  const a = done('a'), b = done('b');
  assert.equal(ready([]), true);
  assert.equal(ready([a, b]), true);
  assert.equal(ready([a, { ...b, state: 'uploading', sha256: undefined }]), false);
  assert.equal(ready([a, { ...b, state: 'queued', sha256: undefined }]), false);
  assert.equal(ready([a, { ...b, state: 'failed', sha256: undefined }]), false);
  // Removing the failed chip makes it ready again.
  assert.equal(ready([a]), true);
});

// ---------- R2 direct uploads (plan 2026-11-05-r2-blobs Task 7) ----------

const r2Api = (f, extra = {}) => createApi({ secret: 'vn_op', fetch: f.fetch, XHR: f.XHR, now: f.now, ...extra });
const lower = h => Object.fromEntries(Object.entries(h).map(([k, v]) => [k.toLowerCase(), v]));

test('R2 upload: discover, hash, grant, PUT the exact hashed bytes with no credentials, complete; resolves the verified Ref', async () => {
  const f = fakeR2();
  const api = r2Api(f);
  const phases = [], seen = [];
  const ref = await api.upload(fileOf('abc', 'report.txt'), { onPhase: p => phases.push(p), onProgress: (l, t, phase) => seen.push([l, t, phase]) });
  assert.deepEqual(phases, ['hashing', 'uploading', 'verifying']);
  const [caps, grant, complete] = f.hubCalls();
  assert.equal(caps.path, '/v1/blob-capabilities');
  assert.equal(grant.path, '/v1/blobs/uploads');
  assert.deepEqual(grant.json, { size: 3, sha256: 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad', media_type: 'text/plain' });
  assert.match(grant.headers['Idempotency-Key'], /^ui-blob-[0-9a-f]{32}$/);
  assert.equal(complete.path, '/v1/blobs/uploads/' + ref.blob_id + '/complete');
  assert.equal(complete.body, '{}');
  for (const c of f.hubCalls()) assert.equal(c.headers.Authorization, 'Bearer vn_op');
  const [put] = f.s3Calls('PUT');
  assert.equal(put.via, 'xhr');
  assert.equal(put.withCredentials, false, 'no cookies on the provider request');
  const h = lower(put.headers);
  assert.equal(h.authorization, undefined, 'no bearer on the provider request');
  assert.equal(h['content-length'], undefined, 'the browser sets Content-Length from the fixed body');
  assert.equal(h['content-type'], 'application/octet-stream');
  assert.equal(h['x-amz-checksum-sha256'], 'ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=');
  assert.equal(h['if-none-match'], '*');
  assert.deepEqual([...put.bytes], [97, 98, 99], 'exact body');
  assert.equal(put.body.size, 3, 'exact length');
  assert.deepEqual(ref, {
    blob_id: ref.blob_id, sha256: 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad', sha256_verified: true,
    integrity: { algorithm: 'sha256', value: 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad', verified: true },
    size: 3, retention_class: '1d', expires_at: '2026-11-07T10:00:00.000Z',
  });
  assert.ok(seen.some(([, , p]) => p === 'hashing') && seen.some(([, , p]) => p === 'uploading'));
  // The signed URL is never part of the result.
  assert.ok(!JSON.stringify(ref).includes('Signature'));
});

test('R2 upload: a disk hub or an old hub keeps the hub PUT; a failed discovery never falls back', async () => {
  for (const backend of ['disk', 'old']) {
    const f = fakeR2({ backend });
    f.legacyPut = (x, body) => setImmediate(() => { x.status = 201; x.responseText = JSON.stringify({ sha256: sha('e'), size: body.size }); x.onload(); });
    const ref = await r2Api(f).upload(fileOf('hello'));
    assert.deepEqual(ref, { sha256: sha('e'), size: 5 });
    const put = f.calls.find(c => c.via === 'xhr');
    assert.equal(put.url, '/v1/blobs?ttl=86400');
    assert.equal(put.headers.Authorization, 'Bearer vn_op');
    assert.equal(f.hubCalls('POST').length, 0);
  }
  const f = fakeR2();
  f.capsStatus = 503;
  const err = await r2Api(f).upload(fileOf('x')).catch(e => e);
  assert.equal(err.status, 503);
  assert.equal(f.calls.filter(c => c.via === 'xhr').length, 0);
});

test('R2 upload: an untrusted grant is refused before any provider request', async () => {
  const cases = {
    'foreign origin': g => { g.upload.url = 'https://evil.example/put?x=1'; },
    'plain http': g => { g.upload.url = g.upload.url.replace('https://', 'http://'); },
    'userinfo': g => { g.upload.url = g.upload.url.replace('https://', 'https://user:pw@'); },
    'over-long deadline': g => { g.upload.expires_at = new Date(Date.parse(g.upload.expires_at) + 3600000).toISOString(); },
    'extra header': g => { g.upload.headers.Authorization = 'Bearer leak'; },
    'missing header': g => { delete g.upload.headers['If-None-Match']; },
    'wrong checksum header': g => { g.upload.headers['x-amz-checksum-sha256'] = 'AAAA'; },
    'wrong digest': g => { g.sha256 = sha('f'); },
    'wrong size': g => { g.size = 4; },
    'not a PUT': g => { g.upload.method = 'POST'; },
  };
  for (const [name, mutate] of Object.entries(cases)) {
    const f = fakeR2();
    const fetch = f.fetch;
    f.fetch = async (url, init) => {
      const r = await fetch(url, init);
      if (String(url) !== '/v1/blobs/uploads') return r;
      const g = await r.json();
      mutate(g);
      return new Response(JSON.stringify(g), { status: 201, headers: { 'Content-Type': 'application/json' } });
    };
    const err = await createApi({ secret: 'vn_op', fetch: f.fetch, XHR: f.XHR, now: f.now }).upload(fileOf('abc')).catch(e => e);
    assert.ok(err instanceof APIError, name);
    assert.equal(err.code, 'bad_response', name);
    assert.equal(f.s3Calls().length, 0, name + ': no provider request');
    assert.equal(f.hubCalls('POST', '/v1/blobs/uploads/').length, 0, name + ': no completion');
  }
});

test('R2 upload: abort while hashing, uploading or completing stops there', async () => {
  // Hashing: no grant is requested.
  let f = fakeR2(), ctrl = new AbortController();
  let p = r2Api(f).upload(fileOf('abc'), { signal: ctrl.signal, onPhase: ph => { if (ph === 'hashing') ctrl.abort(); } });
  let err = await p.catch(e => e);
  assert.equal(err.code, 'aborted');
  assert.equal(f.hubCalls('POST').length, 0);

  // Uploading: the PUT is aborted and nothing completes.
  f = fakeR2(); ctrl = new AbortController();
  f.nextPut.push('hold');
  p = r2Api(f).upload(fileOf('abc'), { signal: ctrl.signal });
  await until(() => f.s3Calls('PUT').length > 0);
  const put = f.s3Calls('PUT')[0];
  assert.ok(put, 'the PUT started');
  ctrl.abort();
  err = await p.catch(e => e);
  assert.equal(err.code, 'aborted');
  assert.ok(put.xhr.aborted);
  f.release();
  await settle();
  assert.equal(f.hubCalls('POST', '/v1/blobs/uploads/').length, 0, 'no completion after an aborted PUT');

  // Completing: the completion's own signal is aborted.
  f = fakeR2(); ctrl = new AbortController();
  const fetch = f.fetch;
  let completeSignal;
  f.fetch = async (url, init) => {
    if (String(url).endsWith('/complete')) { completeSignal = init.signal; ctrl.abort(); throw new DOMException('aborted', 'AbortError'); }
    return fetch(url, init);
  };
  err = await r2Api(f).upload(fileOf('abc'), { signal: ctrl.signal }).catch(e => e);
  assert.equal(err.code, 'aborted');
  assert.ok(completeSignal, 'completion carries the abort signal');
});

test('R2 upload: two concurrent uploads get their own keys, grants and bytes', async () => {
  const f = fakeR2();
  const api = r2Api(f);
  const [a, b] = await Promise.all([api.upload(fileOf('first file', 'a.txt')), api.upload(fileOf('second', 'b.txt'))]);
  assert.notEqual(a.blob_id, b.blob_id);
  const keys = f.hubCalls('POST', '/v1/blobs/uploads').filter(c => c.path === '/v1/blobs/uploads').map(c => c.headers['Idempotency-Key']);
  assert.equal(new Set(keys).size, 2);
  const puts = f.s3Calls('PUT');
  assert.equal(puts.length, 2);
  assert.deepEqual(puts.map(p => new TextDecoder().decode(p.bytes)).sort(), ['first file', 'second']);
  assert.equal(a.size, 10);
  assert.equal(b.size, 6);
});

test('R2 upload: a retry after an ambiguous PUT reuses the same grant; 412 means the bytes landed', async () => {
  const f = fakeR2();
  const api = r2Api(f);
  f.nextPut.push('lost'); // stored at the provider, reply lost
  const first = await api.upload(fileOf('abc'), { idempotencyKey: 'ui-blob-k1' }).catch(e => e);
  assert.equal(first.status, 0);
  assert.equal(first.code, 'storage_unreachable');
  assert.equal(f.hubCalls('POST', '/v1/blobs/uploads/').length, 0, 'an ambiguous PUT is never completed blind');
  const ref = await api.upload(fileOf('abc'), { idempotencyKey: 'ui-blob-k1' });
  const grants = f.hubCalls('POST').filter(c => c.path === '/v1/blobs/uploads');
  assert.deepEqual(grants.map(c => c.headers['Idempotency-Key']), ['ui-blob-k1', 'ui-blob-k1']);
  assert.equal(f.grants.size, 1, 'the same grant, not a second reservation');
  assert.equal(ref.sha256_verified, true);
  // A PUT that never landed is simply sent again under the same grant.
  const g = fakeR2();
  g.nextPut.push('network');
  const k = 'ui-blob-k2';
  assert.equal((await r2Api(g).upload(fileOf('xyz'), { idempotencyKey: k }).catch(e => e)).code, 'storage_unreachable');
  const again = await r2Api(g).upload(fileOf('xyz'), { idempotencyKey: k });
  assert.equal(again.sha256_verified, true);
  assert.equal(g.grants.size, 1);
});

test('R2 upload: provider refusals map to fixed codes; nothing completes', async () => {
  // The PUT body is the hashed bytes themselves, so a provider 400 is a
  // refused request, never "the file changed".
  const cases = [['cors', 0, 'storage_unreachable'], ['redirect', 0, 'storage_redirect'], [403, 403, 'blob_capability_expired'], [400, 400, 'storage_refused'], [500, 500, 'blob_unavailable']];
  for (const [hook, status, code] of cases) {
    const f = fakeR2();
    f.nextPut.push(hook);
    const err = await r2Api(f).upload(fileOf('abc')).catch(e => e);
    assert.equal(err.code, code, String(hook));
    assert.equal(err.status, status, String(hook));
    assert.ok(!String(err.message).includes('Signature') && !String(err.message).includes('<Error'), 'no URL or provider XML in the error');
    assert.equal(f.hubCalls('POST', '/v1/blobs/uploads/').length, 0, String(hook));
  }
});

test('R2 upload: completion must prove verification; quota and busy refusals carry their codes', async () => {
  let f = fakeR2();
  f.completeUnverified = true;
  assert.equal((await r2Api(f).upload(fileOf('abc')).catch(e => e)).code, 'bad_response');
  f = fakeR2();
  f.completeError = () => new Response(JSON.stringify({ error: { code: 'upload_incomplete', message: 'object missing', details: {} } }), { status: 409 });
  assert.equal((await r2Api(f).upload(fileOf('abc')).catch(e => e)).code, 'upload_incomplete');
  f = fakeR2();
  f.storage = { used: 100, limit: 101 };
  const full = await r2Api(f).upload(fileOf('abc')).catch(e => e);
  assert.equal(full.status, 429);
  assert.equal(full.code, 'blob_storage_quota_exceeded');
  assert.equal(f.s3Calls().length, 0);
});

test('R2 upload: new uploads take the retention in force at grant time; earlier refs keep theirs', async () => {
  const f = fakeR2({ retention: '1d' });
  const api = r2Api(f);
  const before = await api.upload(fileOf('one'));
  f.retention = 'off';
  const after = await api.upload(fileOf('two'));
  assert.equal(before.retention_class, '1d');
  assert.equal(before.expires_at, '2026-11-07T10:00:00.000Z');
  assert.equal(after.retention_class, 'keep');
  assert.equal(after.expires_at, null);
  // Each upload discovered capabilities afresh.
  assert.equal(f.hubCalls('GET', '/v1/blob-capabilities').length, 2);
});

test('R2 model: send references carry blob_id, class and deadline, never verification fields; ready waits for verification', () => {
  const ref = { blob_id: 'blo_00000000000000000000000001', sha256: sha('a'), sha256_verified: true, integrity: { algorithm: 'sha256', value: sha('a'), verified: true }, size: 3, retention_class: 'keep', expires_at: null };
  const r2 = { id: 'u1', name: 'a.txt', size: 3, media_type: 'text/plain', state: 'done', ...ref };
  const timed = { ...r2, id: 'u2', blob_id: 'blo_00000000000000000000000002', retention_class: '1d', expires_at: '2026-11-07T10:00:00.000Z' };
  assert.deepEqual(toAttachments([r2, timed, done('b')]), [
    { blob_id: ref.blob_id, sha256: sha('a'), size: 3, media_type: 'text/plain', name: 'a.txt', retention_class: 'keep', expires_at: null },
    { blob_id: timed.blob_id, sha256: sha('a'), size: 3, media_type: 'text/plain', name: 'a.txt', retention_class: '1d', expires_at: '2026-11-07T10:00:00.000Z' },
    { sha256: sha('b'), size: 3, media_type: 'text/plain', name: 'b.txt' },
  ]);
  assert.equal(ready([r2]), true);
  for (const state of ['hashing', 'verifying', 'uploading', 'queued']) assert.equal(ready([{ ...r2, state }]), false, state);
  assert.equal(ready([{ ...r2, sha256_verified: false, integrity: { ...ref.integrity, verified: false } }]), false, 'a pending grant is not sendable');
  // A completed draft stops being sendable at its recorded deadline. The
  // clock is the caller's (the hub clock), not whichever instant this runs.
  const deadline = '2026-11-06T10:00:00.000Z';
  const expiring = { ...timed, expires_at: deadline };
  const before = Date.parse(deadline) - 1, at = Date.parse(deadline);
  assert.equal(ready([expiring], before), true);
  assert.equal(toAttachments([expiring], before).length, 1);
  assert.equal(ready([expiring], at), false, 'at the deadline that ref is not sendable');
  assert.deepEqual(toAttachments([expiring], at), [], 'Send must not carry the expired ref');
  assert.equal(ready([{ ...expiring, retention_class: 'keep', expires_at: null }], at), true, 'keep has no deadline');
  // The same bytes under a new grant are a different attachment.
  assert.notEqual(signature([r2]), signature([{ ...r2, blob_id: 'blo_00000000000000000000000009' }]));
});

test('R2 model: retry keeps the grant key unless the hub says it is spent', () => {
  const it = { idem: 'ui-blob-1' };
  // Replaying an untrusted grant or a refused request reserves nothing new.
  for (const code of ['storage_unreachable', 'network', 'aborted', 'upload_incomplete', 'blob_unavailable', 'bad_response', 'storage_refused']) {
    assert.equal(retryKey(it, new APIError(0, code, '')), 'ui-blob-1', code);
  }
  // No bytes landed under an expired link; rejected bytes need a new grant.
  for (const code of ['upload_grant_expired', 'idempotency_conflict', 'blob_checksum_mismatch', 'blob_capability_expired', 'blob_proof_mismatch', 'blob_storage_quota_exceeded']) {
    assert.equal(retryKey(it, new APIError(409, code, '')), null, code);
  }
});

test('R2 errorText: storage full, busy, expired grant, CORS and integrity read as one line each', () => {
  const nowMs = new Date(2026, 10, 3, 10, 0, 0).getTime();
  const lines = [
    errorText(new APIError(429, 'blob_storage_quota_exceeded', '', {}), nowMs),
    errorText(new APIError(429, 'upload_busy', '', { retry_after_seconds: 30 }), nowMs),
    errorText(new APIError(410, 'upload_grant_expired', ''), nowMs),
    errorText(new APIError(0, 'storage_unreachable', ''), nowMs),
    errorText(new APIError(400, 'blob_checksum_mismatch', ''), nowMs),
    errorText(new APIError(403, 'blob_capability_expired', ''), nowMs),
    errorText(new APIError(409, 'direct_upload_required', ''), nowMs),
    errorText(new APIError(409, 'upload_incomplete', ''), nowMs),
    errorText(new APIError(400, 'storage_refused', ''), nowMs),
    errorText(new APIError(409, 'blob_proof_mismatch', ''), nowMs),
  ];
  assert.match(lines[0], /storage is full/i);
  assert.match(lines[1], /10:00|try again/i);
  assert.match(lines[2], /expired/i);
  assert.match(lines[3], /CORS|storage/i);
  assert.match(lines[4], /changed|checksum/i);
  assert.match(lines[5], /Retry/);
  assert.match(lines[6], /reload/i);
  assert.match(lines[7], /Retry/);
  assert.match(lines[8], /400/);
  assert.match(lines[9], /new upload/);
  assert.equal(new Set(lines).size, lines.length);
});

test('R2 upload: deadlines are judged on the injected hub clock, not the browser clock', async () => {
  // The fixture hub runs at 2026-11-06T10:00Z; this machine's clock does not.
  const f = fakeR2();
  const skewed = await createApi({ secret: 'vn_op', fetch: f.fetch, XHR: f.XHR, now: () => f.clock - 10000 }).upload(fileOf('abc')).catch(e => e);
  assert.equal(skewed.code, 'bad_response', 'a client 10 s behind sees an over-long capability');
  assert.equal(f.s3Calls().length, 0);
  // The same grant replays under the same key on the hub clock, with no new reservation.
  const ref = await createApi({ secret: 'vn_op', fetch: f.fetch, XHR: f.XHR, now: f.now }).upload(fileOf('abc'), { idempotencyKey: 'ui-blob-same' });
  assert.equal(ref.sha256_verified, true);
});

test('R2 upload: capabilities with no transfer origin are refused before any grant is reserved', async () => {
  const f = fakeR2();
  f.capsTamper = c => { c.download_origins = ['https://*.r2.cloudflarestorage.com', 'https://acct.r2.cloudflarestorage.com/path']; };
  const err = await r2Api(f).upload(fileOf('abc')).catch(e => e);
  assert.equal(err.code, 'bad_response');
  assert.equal(f.hubCalls('POST').length, 0, 'no grant, so no reservation');
});

// Enough of a document for attach.js to build chips. Installed only for this test.
class ChipEl {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; this.hidden = false; this.on = {}; this.style = { setProperty() {} }; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { (this.on[type] ||= []).push(fn); }
  append(...xs) { for (const x of xs) this.kids.push(x); }
  replaceChildren(...xs) { this.kids = []; this.append(...xs); }
  remove() {}
}
const walk = (el, pred, out = []) => {
  if (el instanceof ChipEl) { if (pred(el)) out.push(el); for (const k of el.kids) walk(k, pred, out); }
  return out;
};

test('a draft crosses its deadline on the hub clock: Send loses the ref and Re-upload starts a fresh grant', async () => {
  // The hub clock runs three minutes ahead of this machine. A check against
  // Date.now() would still see the file as live after the timer fires.
  const skew = 180000;
  const clock = () => Date.now() + skew;
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new ChipEl(tag) };
  const file = { name: 'notes.txt', size: 4, type: 'text/plain' };
  const keys = [], files = [];
  let n = 0;
  const sha = 'ab'.repeat(32);
  const verified = { sha256: sha, sha256_verified: true, integrity: { algorithm: 'sha256', value: sha, verified: true } };
  const api = {
    upload(body, opts) {
      n++;
      keys.push(opts.idempotencyKey);
      files.push(body);
      const life = n === 1 ? 250 : 60000;
      const id = 'blo_' + String(n).padStart(26, '0');
      return Promise.resolve({ blob_id: id, ...verified, size: body.size, retention_class: '1h', expires_at: new Date(clock() + life).toISOString() });
    },
  };
  let changes = 0;
  try {
    const atts = createAttachments({ api, now: clock, onChange: () => { changes++; } });
    atts.add([file]);
    await new Promise(r => setTimeout(r, 30));
    assert.equal(atts.ready(), true, 'the upload is sendable before its deadline');
    assert.equal(atts.toAttachments().length, 1);
    const sentBefore = changes;
    await new Promise(r => setTimeout(r, 900));
    assert.ok(changes > sentBefore, 'crossing the deadline redraws the composer');
    assert.equal(atts.ready(), false, 'Send is blocked for the expired ref');
    assert.deepEqual(atts.toAttachments(), [], 'the expired ref is not sent');
    const [again] = walk(atts.el, e => e.tag === 'button' && e.textContent === 'Re-upload');
    assert.ok(again, 'the chip offers re-upload from the file it still holds');
    again.on.click.forEach(fn => fn());
    await new Promise(r => setTimeout(r, 30));
    assert.equal(files.length, 2);
    assert.equal(files[0], files[1], 're-upload uses the retained File');
    assert.ok(keys[0] && keys[1] && keys[0] !== keys[1], 'the new grant gets a fresh idempotency key');
    assert.equal(atts.ready(), true);
    assert.equal(atts.toAttachments()[0].blob_id.endsWith('2'), true);
    atts.destroy();
  } finally {
    globalThis.document = prev;
  }
});
