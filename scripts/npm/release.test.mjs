import assert from 'node:assert/strict';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { checkPublished, checkTag, publish, releaseInfo } from './release.mjs';

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

const info = releaseInfo({ name: 'vecnet', version: '0.1.1' });
const published = {
  name: 'vecnet', version: '0.1.1',
  dist: { integrity: 'sha512-7HJ8RzKS/Zj5BkNLRSPwCLGwhP1dHbgltO3R2fs9pLeXJa5kzKOauZTdhDrvV3sSYZCXw9X97/yyb8HUgqF9yw==' },
};
const response = (status, body) => new Response(body && JSON.stringify(body), { status });

async function publication(t, { uploadMs = 0, lookup = () => response(404), uploadError } = {}) {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'vecnet-publish-'));
  t.after(() => rm(cwd, { recursive: true, force: true }));
  await mkdir(path.join(cwd, 'dist'));
  await writeFile(path.join(cwd, 'dist', info.tarball), 'tested tarball\n');
  let elapsed = 0;
  const uploads = [], lookups = [], waits = [];
  const options = {
    cwd, now: () => elapsed,
    sleep: async ms => { waits.push(ms); elapsed += ms; },
    fetch: async (url, options) => {
      assert.equal(options.headers['Cache-Control'], 'no-cache');
      assert.ok(options.signal instanceof AbortSignal);
      lookups.push(url);
      elapsed += 100;
      return lookup(lookups.length);
    },
    exec: (file, args, options) => {
      uploads.push({ file, args, options });
      elapsed += uploadMs;
      if (uploadError) throw uploadError;
    },
  };
  return { options, uploads, lookups, waits, elapsed: () => elapsed };
}

test('slow successful upload polls briefly for exact registry visibility without republishing', async t => {
  const context = await publication(t, { uploadMs: 24_000, lookup: n => n < 4 ? response(404) : response(200, published) });
  await publish(info, context.options);
  assert.ok(context.elapsed() < 40_000, `publication took ${context.elapsed()}ms`);
  assert.equal(context.uploads.length, 1);
  const { args, options } = context.uploads[0];
  assert.deepEqual(args.slice(-9), [
    'publish', path.join(context.options.cwd, 'dist', info.tarball), '--ignore-scripts', '--access', 'public',
    '--tag', 'latest', '--registry', 'https://registry.npmjs.org/',
  ]);
  assert.equal(options.cwd, context.options.cwd);
  assert.ok(options.timeout > 14_000 && options.timeout < 40_000, 'npm child must fit the publication deadline');
  assert.equal(context.lookups[0], 'https://registry.npmjs.org/vecnet/0.1.1');
  assert.ok(context.lookups.slice(1).every(url => url.startsWith('https://registry.npmjs.org/vecnet/0.1.1?release-check=')));
  assert.ok(context.waits.length > 0 && context.waits.every(ms => ms <= 1000));
});

test('accepted upload with an invisible version fails within40s without another upload', async t => {
  const context = await publication(t, { uploadMs: 35_000 });
  await assert.rejects(publish(info, context.options), /npm accepted the upload/);
  assert.ok(context.elapsed() < 40_000, `publication took ${context.elapsed()}ms`);
  assert.equal(context.uploads.length, 1);
  assert.ok(context.lookups.length > 1);
  assert.ok(context.waits.every(ms => ms <= 1000));
});

test('fast successful upload gets only a short registry visibility window', async t => {
  const context = await publication(t);
  await assert.rejects(publish(info, context.options), /npm accepted the upload/);
  assert.ok(context.elapsed() < 6000, `visibility polling took ${context.elapsed()}ms`);
  assert.equal(context.uploads.length, 1);
});

test('already published exact bytes recover without uploading; different bytes fail closed', async t => {
  for (const [metadata, error] of [[published, undefined], [{ ...published, dist: { integrity: 'different' } }, /different contents/]]) {
    const context = await publication(t, { lookup: () => response(200, metadata) });
    if (error) await assert.rejects(publish(info, context.options), error);
    else await publish(info, context.options);
    assert.equal(context.uploads.length, 0);
    assert.equal(context.lookups.length, 1);
  }
});

test('failed upload propagates without visibility polling or a duplicate publication', async t => {
  const failure = new Error('OIDC upload rejected');
  const context = await publication(t, { uploadError: failure });
  await assert.rejects(publish(info, context.options), error => error === failure);
  assert.equal(context.uploads.length, 1);
  assert.equal(context.lookups.length, 1);
  assert.deepEqual(context.waits, []);
});

test('registry errors and post-upload integrity mismatches fail without retrying publication', async t => {
  for (const [lookup, error, uploads] of [
    [() => response(503), /lookup failed with HTTP 503/, 0],
    [n => response(n === 1 ? 404 : 503), /verification failed with HTTP 503/, 1],
    [n => n === 1 ? response(404) : response(200, { ...published, dist: { integrity: 'different' } }), /different contents/, 1],
  ]) {
    const context = await publication(t, { lookup });
    await assert.rejects(publish(info, context.options), error);
    assert.equal(context.uploads.length, uploads);
    assert.deepEqual(context.waits, []);
  }
});
