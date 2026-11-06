import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { CHUNK, hashFile, toBase64, toHex } from '../static/blob-hash.js';

const node = b => createHash('sha256').update(b).digest('hex');

test('SHA-256 known vectors through Web Crypto, hex and base64', async () => {
  const vectors = [
    ['', 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'],
    ['abc', 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'],
    ['abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq', '248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1'],
    ['a'.repeat(1000000), 'cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0'],
  ];
  for (const [text, want] of vectors) {
    const r = await hashFile(new Blob([text]));
    assert.equal(r.hex, want, JSON.stringify(text.slice(0, 20)));
    assert.equal(r.base64, Buffer.from(want, 'hex').toString('base64'));
    assert.equal(r.bytes.length, new TextEncoder().encode(text).length);
  }
  // The abc vector's base64 is the R2 x-amz-checksum-sha256 header value.
  assert.equal((await hashFile(new Blob(['abc']))).base64, 'ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=');
  assert.equal(toHex(new Uint8Array([0, 15, 255])), '000fff');
  assert.equal(toBase64(new Uint8Array([255, 254, 253])), '//79');
});

test('hashing matches Node crypto across chunk boundaries and returns the exact bytes', async () => {
  for (const size of [1, 7, 63, 64, 65, 1000, 4096, 4097, 3 * 4096 + 5]) {
    const data = randomBytes(size);
    const r = await hashFile(new Blob([data]), { chunk: 4096 });
    assert.equal(r.hex, node(data), 'size ' + size);
    assert.deepEqual(Buffer.from(r.bytes), data, 'exact body, size ' + size);
    assert.equal(r.bytes.length, size, 'exact length');
  }
});

test('an abort during hashing stops reading and rejects with AbortError', async () => {
  const ctrl = new AbortController();
  let reads = 0;
  const data = randomBytes(10 * 4096);
  const blob = new Blob([data]);
  const spy = { size: blob.size, slice: (a, b) => { reads++; if (reads === 3) ctrl.abort(); return blob.slice(a, b); } };
  const err = await hashFile(spy, { chunk: 4096, signal: ctrl.signal }).catch(e => e);
  assert.equal(err.name, 'AbortError');
  assert.ok(reads <= 4, 'stopped reading after the abort, read ' + reads);
  const pre = new AbortController();
  pre.abort();
  assert.equal((await hashFile(new Blob(['x']), { signal: pre.signal }).catch(e => e)).name, 'AbortError');
});

test('a 25 MiB file is hashed in bounded chunks that yield to the page between reads', async () => {
  const size = 25 << 20;
  const data = new Uint8Array(size);
  for (let i = 0; i < size; i += 4093) data[i] = i & 255;
  let turns = 0, stop = false;
  // A page timer: it can only run when hashing yields the thread.
  const spin = () => { if (stop) return; turns++; setTimeout(spin, 0); };
  setTimeout(spin, 0);
  const seen = [];
  const r = await hashFile(new Blob([data]), { onProgress: (loaded, total) => seen.push([loaded, total]) });
  stop = true;
  assert.equal(r.hex, node(data));
  const chunks = Math.ceil(size / CHUNK);
  assert.ok(CHUNK <= 4 << 20, 'chunks are bounded');
  assert.ok(turns >= chunks - 1, `the page ran ${turns} times across ${chunks} chunks`);
  assert.equal(seen.length, chunks);
  assert.deepEqual(seen.at(-1), [size, size]);
  for (let i = 1; i < seen.length; i++) assert.ok(seen[i][0] > seen[i - 1][0]);
});

test('a file that reads short or long is refused rather than hashed as something else', async () => {
  const liar = { size: 10, slice: () => new Blob(['abc']) };
  const err = await hashFile(liar).catch(e => e);
  assert.equal(err.name, 'NotReadableError');
});
