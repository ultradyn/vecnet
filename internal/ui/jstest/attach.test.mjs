import test from 'node:test';
import assert from 'node:assert/strict';
import { APIError, createApi } from '../static/api.js';
import {
  MAX_ATTACHMENTS, MAX_BYTES, SPILL_SLOT, attachmentName, canAdd, errorText, mediaTypeOf, ready, sendRefusal, signature, toAttachments,
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

test('api.upload: PUT /v1/blobs?ttl=86400 with bearer and Content-Type, reports progress, resolves {sha256,size}', async () => {
  const { made, XHR } = fakeXHR();
  const api = createApi({ secret: 'vn_secret', fetch: async () => { throw new Error('upload must not use fetch'); }, XHR });
  const f = new File(['hello world'], 'shot.png', { type: 'image/png' });
  const seen = [];
  const p = api.upload(f, { onProgress: (loaded, total) => seen.push([loaded, total]) });
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
  assert.equal(made[1].headers['Content-Type'], 'application/octet-stream');
  made[1].reply(200, { sha256: sha('d'), size: 1 });
  assert.deepEqual(await q, { sha256: sha('d'), size: 1 });

  // A 2xx with an unreadable body is a lost reply.
  const r = api.upload(new File(['z'], 'z'));
  made[2].reply(200, '<html>');
  const lost = await r.catch(e => e);
  assert.ok(lost instanceof APIError);
  assert.equal(lost.status, 0);
  assert.equal(lost.code, 'network');
});

test('api.upload: 413 → APIError blob_too_large with details.limit; abort → aborted; 401 fires onUnauthorized once', async () => {
  const { made, XHR } = fakeXHR();
  let signedOut = 0;
  const api = createApi({ secret: 's', XHR, onUnauthorized: () => { signedOut++; } });

  const big = api.upload(new File(['x'], 'big.iso'));
  made[0].reply(413, { error: { code: 'blob_too_large', message: 'blob exceeds the per-blob cap', details: { limit: 25 * MiB } } }, 'Payload Too Large');
  const e413 = await big.catch(e => e);
  assert.ok(e413 instanceof APIError);
  assert.equal(e413.status, 413);
  assert.equal(e413.code, 'blob_too_large');
  assert.equal(e413.message, 'blob exceeds the per-blob cap');
  assert.deepEqual(e413.details, { limit: 25 * MiB });

  // A proxy page: the status still says what happened, as request() does.
  const proxied = api.upload(new File(['x'], 'p'));
  made[1].reply(502, '<html>Bad gateway</html>', 'Bad Gateway');
  const e502 = await proxied.catch(e => e);
  assert.equal(e502.status, 502);
  assert.equal(e502.code, 'http_502');
  assert.equal(e502.message, 'Bad Gateway');
  assert.deepEqual(e502.details, {});

  // Abort through the signal → status 0 code aborted, and the XHR is aborted.
  const ctrl = new AbortController();
  const gone = api.upload(new File(['x'], 'a'), { signal: ctrl.signal });
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
  made[3].fail();
  const eNet = await off.catch(e => e);
  assert.equal(eNet.status, 0);
  assert.equal(eNet.code, 'network');

  // 401 from uploads and from fetch requests shares one once-flag.
  const denied = { error: { code: 'unauthenticated', message: 'no', details: {} } };
  const u1 = api.upload(new File(['x'], '1'));
  const u2 = api.upload(new File(['x'], '2'));
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
  const u3 = shared.upload(new File(['x'], '3'));
  made[6].reply(401, denied);
  await u3.catch(() => {});
  await tick();
  assert.equal(fetched, 1);
});

test('errorText: 429 shows the en-AU time from retry_after_seconds; 413 without details.limit says 25 MiB', () => {
  const nowMs = new Date(2026, 10, 3, 10, 0, 0).getTime(); // 10:00 local
  const quota = new APIError(429, 'upload_quota_exceeded', 'daily upload quota exceeded', { limit: 500 * MiB, retry_after_seconds: 5400 });
  const t429 = errorText(quota, nowMs);
  assert.ok(t429.includes('11:30'), t429);
  assert.ok(!/am|pm/i.test(t429), t429);
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
