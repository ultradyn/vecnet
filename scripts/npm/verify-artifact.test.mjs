import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { verifyChecksum } from './verify-artifact.mjs';

const bytes = Buffer.from('the exact tested npm artifact');
const digest = createHash('sha256').update(bytes).digest('hex');
const tarball = 'vecnet-1.2.3.tgz';

test('release checksums accept sha256sum text/binary markers and LF/CRLF', () => {
  for (const marker of [' ', '*']) for (const newline of ['\n', '\r\n']) {
    assert.equal(verifyChecksum(`${digest} ${marker}${tarball}${newline}`, tarball, bytes), digest);
  }
});

test('release checksum rejects any archive modification', () => {
  assert.throws(() => verifyChecksum(`${digest}  ${tarball}\n`, tarball, Buffer.from('changed bytes')), /checksum mismatch/);
});

test('release checksum inventory accepts only the exact manifest tarball', () => {
  for (const inventory of [
    `${digest}  vecnet-1.2.4.tgz\n`, `${digest}  ../${tarball}\n`,
    `${digest}  ${tarball}\n${digest}  another.tgz\n`, `invalid  ${tarball}\n`, '',
  ]) assert.throws(() => verifyChecksum(inventory, tarball, bytes), /Unexpected release checksum inventory/);
});
