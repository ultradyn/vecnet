import assert from 'node:assert/strict';
import test from 'node:test';
import { checkPublished, checkTag, releaseInfo } from './release.mjs';

test('stable and prerelease versions select separate npm channels', () => {
  assert.deepEqual(releaseInfo({ name: 'vecnet', version: '0.1.1' }), {
    version: '0.1.1', tag: 'v0.1.1', npmTag: 'latest', tarball: 'vecnet-0.1.1.tgz',
  });
  assert.equal(releaseInfo({ name: 'vecnet', version: '1.0.0-rc.2' }).npmTag, 'next');
  assert.equal(releaseInfo({ name: 'vecnet', version: '1.0.0-beta-01' }).npmTag, 'next');
});

test('release guards reject wrong packages, malformed versions and mismatched tags', () => {
  for (const version of ['v0.1.1', '01.1.1', '1.0', '1.0.0-01', '1.0.0-rc..1', '1.0.0+build', '1.0.0\n', '1.0.0/../other', '9007199254740992.0.0']) {
    assert.throws(() => releaseInfo({ name: 'vecnet', version }), undefined, version);
  }
  assert.throws(() => releaseInfo({ name: 'other', version: '1.0.0' }));
  const info = releaseInfo({ name: 'vecnet', version: '0.1.1' });
  assert.doesNotThrow(() => checkTag('v0.1.1', info));
  for (const tag of ['v0.1.0', '0.1.1', 'v0.1.1-rc.1', 'v0.1.1\n']) assert.throws(() => checkTag(tag, info));
});

test('release recovery accepts only the exact artifact already published to npm', () => {
  const info = releaseInfo({ name: 'vecnet', version: '0.1.1' });
  const published = { name: 'vecnet', version: '0.1.1', dist: { integrity: 'sha512-matching' } };
  assert.doesNotThrow(() => checkPublished(published, info, 'sha512-matching'));
  assert.throws(() => checkPublished(published, info, 'sha512-different'));
  assert.throws(() => checkPublished({ ...published, version: '0.1.0' }, info, 'sha512-matching'));
  assert.throws(() => checkPublished({ ...published, name: 'other' }, info, 'sha512-matching'));
  assert.throws(() => checkPublished({ ...published, dist: undefined }, info, 'sha512-matching'));
});
