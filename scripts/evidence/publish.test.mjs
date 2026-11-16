import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, writeFile, readFile, readdir, lstat, symlink, access } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const exec = promisify(execFile);
const cli = fileURLToPath(new URL('./publish.mjs', import.meta.url));
const commit = '0123456789abcdef0123456789abcdef01234567';
const createdAt = '2026-11-16T01:02:03.000Z';
const expiresAt = '2026-12-16T01:02:03.000Z';
const config = { accountId: '375a8abfb0c42fd0940edbef36fcd1e9', bucket: 'vecnet-evidence', publicUrl: 'https://evidence.example.test', retentionDays: 30 };
const load = () => import('./publish.mjs');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const json = async path => JSON.parse(await readFile(path, 'utf8'));
const absent = async path => assert.rejects(access(path), { code: 'ENOENT' });

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'vecnet-evidence-'));
  t.after(async () => {
    const { rm } = await import('node:fs/promises');
    await rm(root, { recursive: true, force: true });
  });
  const directory = join(root, 'input');
  await mkdir(directory);
  await mkdir(join(directory, 'nested'));
  await writeFile(join(directory, 'one.PNG'), 'abc');
  await writeFile(join(directory, 'nested', 'clip.mov'), 'clip bytes');
  await writeFile(join(directory, 'notes.txt'), 'ordinary ignored file');
  const receipt = join(root, 'receipt.json');
  return { root, directory, receipt, options: { directory, commit, config, outputRoot: join(root, 'output'), receipt, clock: () => new Date(createdAt) } };
}

function transport(f) {
  const objects = new Map();
  const events = [];
  return {
    objects, events,
    upload: async object => {
      await absent(f.receipt);
      objects.set(object.key, await readFile(object.path));
      events.push(`upload:${object.key}`);
    },
    readPublic: async url => {
      await absent(f.receipt);
      events.push(`read:${url}`);
      const key = decodeURIComponent(new URL(url).pathname.slice(1));
      assert.ok(objects.has(key), `unpublished object ${key}`);
      return objects.get(key);
    },
  };
}

test('ET1: dry-run selects nested media, hashes bytes and does not use transport or mutate inputs', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  const result = await publishEvidence({ ...f.options, dryRun: true, upload: () => assert.fail('upload in dry-run'), readPublic: () => assert.fail('network in dry-run') });
  assert.equal(result.receipt, null);
  await absent(f.receipt);
  const manifest = await json(join(result.runDirectory, 'manifest.json'));
  assert.deepEqual(manifest.files.map(x => x.name).sort(), ['nested/clip.mov', 'one.PNG']);
  const png = manifest.files.find(x => x.name === 'one.PNG');
  assert.equal(png.sha256, 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
  assert.equal(png.size, 3);
  assert.equal(png.contentType, 'image/png');
  assert.match(png.key, new RegExp(`^runs/${commit}/[^/]+/media/one\\.PNG$`));
  assert.equal(manifest.sourceCommit, commit);
  assert.equal(await readFile(join(f.directory, 'one.PNG'), 'utf8'), 'abc');
  assert.deepEqual((await readdir(f.directory)).sort(), ['nested', 'notes.txt', 'one.PNG']);
  assert.match(await readFile(join(result.runDirectory, 'index.html'), 'utf8'), /<video/);
});

test('ET2: publishes media then indexes, verifies every public byte before a receipt', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  const remote = transport(f);
  const result = await publishEvidence({ ...f.options, ...remote });
  const receipt = await json(f.receipt);
  assert.equal(remote.objects.size, 4);
  assert.equal(receipt.galleryUrl, result.galleryUrl);
  assert.equal(receipt.manifestUrl, result.manifestUrl);
  assert.equal(receipt.sourceCommit, commit);
  assert.equal(receipt.expiresAt, expiresAt);
  assert.equal(receipt.objects.length, 4);
  assert.match(receipt.galleryUrl, /\/index\.html$/);
  assert.match(receipt.manifestUrl, /\/manifest\.json$/);
  const firstRead = remote.events.findIndex(x => x.startsWith('read:'));
  assert.equal(firstRead, 4);
  assert.ok(remote.events.slice(0, 2).every(x => !/\/(manifest\.json|index\.html)$/.test(x)));
  for (const object of receipt.objects) {
    assert.equal(object.sha256, hash(remote.objects.get(object.key)));
    assert.equal(object.size, remote.objects.get(object.key).length);
    assert.ok(remote.events.includes(`read:${object.url}`));
  }
});

test('R6 media namespace supports input directories named index.html and manifest.json', async t => {
  const { publishEvidence } = await load();
  for (const dryRun of [true, false]) {
    await t.test(dryRun ? 'dry-run' : 'publish', async t => {
      const f = await fixture(t);
      for (const directory of ['index.html', 'manifest.json']) {
        await mkdir(join(f.directory, directory));
        await writeFile(join(f.directory, directory, 'shot.png'), `${directory} media bytes`);
      }
      const remote = transport(f);
      const result = await publishEvidence({ ...f.options, ...remote, dryRun });
      for (const index of ['index.html', 'manifest.json']) {
        assert.ok((await lstat(join(result.runDirectory, index))).isFile());
      }
      const manifest = await json(join(result.runDirectory, 'manifest.json'));
      assert.deepEqual(manifest.files.map(file => file.name).sort(), ['index.html/shot.png', 'manifest.json/shot.png', 'nested/clip.mov', 'one.PNG']);
      const gallery = await readFile(join(result.runDirectory, 'index.html'), 'utf8');
      for (const file of manifest.files) {
        const relativePath = `media/${file.name}`;
        assert.equal(file.key, manifest.prefix + relativePath);
        assert.equal(file.url, new URL(relativePath, result.galleryUrl).href);
        assert.ok(gallery.includes(`src="${relativePath}"`));
        assert.ok(gallery.includes(`href="${relativePath}">${file.name}</a>`));
        const source = await readFile(join(f.directory, file.name));
        assert.deepEqual(await readFile(join(result.runDirectory, relativePath)), source);
        assert.equal(file.sha256, hash(source));
        if (!dryRun) {
          assert.deepEqual(remote.objects.get(file.key), source);
          assert.ok(remote.events.includes(`read:${file.url}`));
        }
      }
      if (dryRun) {
        assert.deepEqual(remote.events, []);
        assert.equal(result.receipt, null);
        await absent(f.receipt);
      } else {
        const receipt = await json(f.receipt);
        assert.equal(receipt.objects.length, 6);
        assert.equal(remote.events.findIndex(event => event.startsWith('read:')), 6);
        for (const object of receipt.objects) {
          assert.ok(remote.events.includes(`read:${object.url}`));
          assert.equal(hash(remote.objects.get(object.key)), object.sha256);
        }
      }
    });
  }
});

test('ET3: failed uploads and corrupt public bytes do not write success receipts', async t => {
  const { publishEvidence } = await load();
  for (const mode of ['upload', 'media', 'manifest.json', 'index.html', 'length', 'read']) {
    await t.test(mode, async t => {
      const f = await fixture(t);
      const remote = transport(f);
      const upload = mode === 'upload' ? async () => { throw Error('synthetic-private-token'); } : remote.upload;
      const readPublic = async url => {
        if (mode === 'read') throw Error('synthetic-private-token');
        const bytes = await remote.readPublic(url);
        if (mode === 'length') return Buffer.concat([bytes, Buffer.from('extra')]);
        if (mode === 'media' && /one\.PNG$/.test(url) || url.endsWith(`/${mode}`)) {
          const corrupt = Buffer.from(bytes);
          corrupt[0] ^= 1;
          return corrupt;
        }
        return bytes;
      };
      await assert.rejects(publishEvidence({ ...f.options, upload, readPublic }), error => {
        assert.doesNotMatch(String(error), /synthetic-private-token/);
        return true;
      });
      await absent(f.receipt);
    });
  }
});

test('ET4: routine expiry is 30 days and kept evidence has no automatic expiry', async t => {
  const { publishEvidence } = await load();
  for (const keep of [false, true]) {
    const f = await fixture(t);
    const result = await publishEvidence({ ...f.options, ...transport(f), keep });
    const receipt = await json(f.receipt);
    const manifest = await json(join(result.runDirectory, 'manifest.json'));
    for (const item of [receipt, manifest]) {
      assert.equal(item.createdAt, createdAt);
      assert.equal(item.expiresAt, keep ? null : expiresAt);
      assert.equal(item.kept, keep);
    }
    assert.ok(manifest.files.every(x => x.key.startsWith(`${keep ? 'kept' : 'runs'}/${commit}/`)));
    assert.match(await readFile(join(result.runDirectory, 'index.html'), 'utf8'), keep ? /no automatic expir/i : /2026-12-16/);
  }
});

test('rejects invalid source commits and symlink inputs before upload', async t => {
  const { publishEvidence } = await load();
  for (const invalid of ['HEAD', 'abc', 'g'.repeat(40), `${commit}\n`, `${commit};bad`]) {
    const f = await fixture(t);
    await assert.rejects(publishEvidence({ ...f.options, commit: invalid, upload: () => assert.fail('upload before validation') }), /commit/i);
    await absent(f.receipt);
  }
  for (const kind of ['root', 'file', 'directory', 'ignored']) {
    const f = await fixture(t);
    let directory = f.directory;
    if (kind === 'root') {
      directory = join(f.root, 'linked');
      await symlink(f.directory, directory);
    } else {
      await symlink(kind === 'directory' ? join(f.directory, 'nested') : join(f.directory, 'one.PNG'), join(f.directory, kind === 'ignored' ? 'linked.txt' : 'linked'));
    }
    await assert.rejects(publishEvidence({ ...f.options, directory, upload: () => assert.fail('upload symlink') }), /symlink/i);
    await absent(f.receipt);
  }
});

test('rejects unsupported special files before upload, including ignored extensions', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  await exec('mkfifo', [join(f.directory, 'pipe.txt')]);
  await assert.rejects(publishEvidence({ ...f.options, upload: () => assert.fail('upload special file') }), /special|regular/i);
  await absent(f.receipt);
});

test('encodes media URLs and escapes gallery labels', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  const name = `a #?%&'\"<script>.png`;
  await writeFile(join(f.directory, 'nested', name), 'hostile name');
  const result = await publishEvidence({ ...f.options, dryRun: true, publicUrl: 'https://evidence.example.test/base/' });
  const item = result.manifest.files.find(x => x.name.endsWith(name));
  assert.ok(item.key.endsWith(`nested/${name}`));
  assert.ok(item.url.startsWith('https://evidence.example.test/base/runs/'));
  assert.ok(item.url.endsWith('/nested/a%20%23%3F%25%26%27%22%3Cscript%3E.png'));
  const gallery = await readFile(join(result.runDirectory, 'index.html'), 'utf8');
  assert.doesNotMatch(gallery, /<script>/);
  assert.match(gallery, /&lt;script&gt;/);
  assert.match(gallery, /&amp;/);
  assert.match(gallery, /&#39;/);
  assert.match(gallery, /&quot;/);
});

test('ignores ordinary files, accepts exactly the selected media types case insensitively', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  const selected = ['image.jpg', 'image.JPEG', 'image.gif', 'image.webp', 'clip.mp4', 'clip.WEBM'];
  for (const name of [...selected, 'not.svg', 'not.avi', 'not.mkv', '.env']) await writeFile(join(f.directory, name), 'bytes');
  const result = await publishEvidence({ ...f.options, dryRun: true });
  assert.deepEqual(result.manifest.files.map(x => x.name).sort(), [...selected, 'nested/clip.mov', 'one.PNG'].sort());
});

test('empty inputs fail before uploads or receipt creation', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  const directory = join(f.root, 'empty');
  await mkdir(directory);
  await writeFile(join(directory, 'notes.txt'), 'not media');
  await assert.rejects(publishEvidence({ ...f.options, directory, upload: () => assert.fail('empty upload') }), /no .*media|empty/i);
  await absent(f.receipt);
});

test('uploads at most two objects concurrently and waits for every upload before reading', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  for (let i = 0; i < 5; i++) await writeFile(join(f.directory, `${i}.png`), 'bytes');
  const remote = transport(f);
  let active = 0;
  let maximum = 0;
  await publishEvidence({ ...f.options, upload: async object => {
    active++;
    maximum = Math.max(maximum, active);
    await new Promise(resolve => setTimeout(resolve, 5));
    await remote.upload(object);
    active--;
  }, readPublic: async url => {
    assert.equal(active, 0);
    assert.equal(remote.objects.size, 9);
    return remote.readPublic(url);
  } });
  assert.equal(maximum, 2);
});

test('run identifiers are unique even with an identical injected clock', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  const a = await publishEvidence({ ...f.options, dryRun: true });
  const b = await publishEvidence({ ...f.options, dryRun: true });
  assert.notEqual(a.manifest.prefix, b.manifest.prefix);
  assert.match(a.manifest.prefix, /2026-11-16T01-02-03-000Z-[a-f0-9]{16,}\/$/);
});

test('invalid public origins and invalid config fail without transport', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  for (const publicUrl of ['http://example.test', 'https://user:synthetic-private-token@example.test', 'https://example.test?token=synthetic-private-token', 'https://example.test/#fragment', 'bad-url',
    'https://host?', 'https://host#', 'https://host/?', 'https://host/#', 'https://host/base/?', 'https://host/base/#']) {
    await t.test(publicUrl.replace('synthetic-private-token', '[sentinel]'), async () => {
      let uploads = 0;
      let reads = 0;
      await assert.rejects(publishEvidence({ ...f.options, publicUrl,
        upload: async () => { uploads++; }, readPublic: async () => { reads++; return Buffer.alloc(0); },
      }), error => {
        assert.match(String(error), /Invalid public HTTPS URL/);
        assert.doesNotMatch(String(error), /synthetic-private-token/);
        return true;
      });
      assert.equal(uploads, 0);
      assert.equal(reads, 0);
      await absent(f.receipt);
    });
  }
  for (const invalid of [{ accountId: 'bad' }, { bucket: '../bad' }, { retentionDays: 0 }]) {
    await assert.rejects(publishEvidence({ ...f.options, config: { ...config, ...invalid }, dryRun: true }));
  }
});

test('I1 preserves percent-encoded query and fragment characters in base paths', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  const result = await publishEvidence({ ...f.options, dryRun: true, publicUrl: 'https://evidence.example.test/base%3F%23/' });
  for (const file of result.manifest.files) {
    const url = new URL(file.url);
    assert.ok(url.pathname.startsWith('/base%3F%23/runs/'));
    assert.equal(url.search, '');
    assert.equal(url.hash, '');
    assert.equal(decodeURIComponent(url.pathname).split('/').at(-1), file.name.split('/').at(-1));
  }
});

test('generated output cannot mutate the source tree and existing receipts are preserved', async t => {
  const { publishEvidence } = await load();
  const f = await fixture(t);
  await assert.rejects(publishEvidence({ ...f.options, outputRoot: join(f.directory, 'output'), dryRun: true }), /output/i);
  await writeFile(f.receipt, 'existing receipt');
  await assert.rejects(publishEvidence({ ...f.options, ...transport(f) }), /receipt/i);
  assert.equal(await readFile(f.receipt, 'utf8'), 'existing receipt');
});

test('Wrangler transport uses remote argv and captured subprocess errors never expose a sentinel credential', async t => {
  const { createWranglerUploader } = await load();
  const f = await fixture(t);
  const executable = join(f.root, 'fake-wrangler');
  const log = join(f.root, 'argv.json');
  await writeFile(executable, `#!${process.execPath}\nimport('node:fs').then(fs => { fs.writeFileSync(process.env.ARGV_LOG, JSON.stringify(process.argv.slice(2))); process.stdout.write('synthetic-private-token'); process.stderr.write('synthetic-private-token'); process.exit(Number(process.env.FAIL)); });\n`, { mode: 0o700 });
  const env = { PATH: dirname(process.execPath), HOME: f.root, ARGV_LOG: log, FAIL: '0' };
  const object = { accountId: config.accountId, bucket: config.bucket, key: `runs/${commit}/run/name ; $(bad).png`, path: join(f.directory, 'one.PNG'), contentType: 'image/png' };
  await createWranglerUploader({ executable, env })(object);
  const argv = await json(log);
  assert.deepEqual(argv, ['r2', 'object', 'put', `${config.bucket}/${object.key}`, '--remote', '--file', object.path, '--content-type', 'image/png', '--cache-control', 'public, max-age=300, no-transform', '--force']);
  assert.ok(!argv.includes('--expires'));
  await assert.rejects(createWranglerUploader({ executable, env: { ...env, FAIL: '1' } })(object), error => {
    assert.doesNotMatch(String(error), /synthetic-private-token/);
    assert.equal(error.cause, undefined);
    assert.doesNotMatch(JSON.stringify(error), /synthetic-private-token/);
    return true;
  });
});

test('R7 public artifact uploads request no-transform', async t => {
  const { publishEvidence, createWranglerUploader } = await load();
  const f = await fixture(t);
  const executable = join(f.root, 'fake-wrangler');
  const uploaded = join(f.root, 'uploaded');
  await mkdir(uploaded);
  await writeFile(executable, `#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const argv = process.argv.slice(2);
const bytes = fs.readFileSync(argv[argv.indexOf('--file') + 1]).toString('base64');
fs.writeFileSync(path.join(process.env.UPLOADED, encodeURIComponent(argv[3]) + '.json'), JSON.stringify({ argv, bytes }));
`, { mode: 0o700 });
  const upload = createWranglerUploader({ executable, env: { PATH: dirname(process.execPath), HOME: f.root, UPLOADED: uploaded } });
  const result = await publishEvidence({ ...f.options, upload, readPublic: async url => {
    await absent(f.receipt);
    const key = `${config.bucket}/${decodeURIComponent(new URL(url).pathname.slice(1))}`;
    return Buffer.from((await json(join(uploaded, `${encodeURIComponent(key)}.json`))).bytes, 'base64');
  } });
  const records = await Promise.all((await readdir(uploaded)).map(name => json(join(uploaded, name))));
  assert.equal(records.length, 4);
  assert.equal(result.receipt.objects.length, 4);
  assert.deepEqual(records.map(({ argv }) => argv[argv.indexOf('--content-type') + 1]).sort(), ['application/json', 'image/png', 'text/html; charset=utf-8', 'video/quicktime']);
  for (const { argv } of records) {
    assert.equal(argv[argv.indexOf('--cache-control') + 1], 'public, max-age=300, no-transform');
  }
});

test('CLI help and dry-run work offline in isolated directories without operator config', async t => {
  await load();
  const f = await fixture(t);
  const env = { PATH: dirname(process.execPath), HOME: f.root };
  const help = await exec(process.execPath, [cli, '--help'], { cwd: f.root, env });
  assert.match(help.stdout, /--dry-run/);
  assert.match(help.stdout, /--keep/);
  const result = await exec(process.execPath, [cli, f.directory, '--commit', commit, '--dry-run', '--receipt', f.receipt], { cwd: f.root, env });
  assert.match(result.stdout, /dry.run/i);
  await absent(f.receipt);
  const runs = await readdir(join(f.root, '.scratch/evidence-publish'));
  assert.equal(runs.length, 1);
  assert.ok((await lstat(join(f.root, '.scratch/evidence-publish', runs[0], 'index.html'))).isFile());
  await assert.rejects(exec(process.execPath, [cli, f.directory, '--bogus'], { cwd: f.root, env }), error => error.code === 1);
});

test('CLI upload failure does not print captured Wrangler output or write a receipt', async t => {
  await load();
  const f = await fixture(t);
  const bin = join(f.root, 'bin');
  await mkdir(bin);
  await writeFile(join(bin, 'wrangler'), `#!${process.execPath}\nprocess.stdout.write('synthetic-private-token'); process.stderr.write('synthetic-private-token'); process.exit(1);\n`, { mode: 0o700 });
  await assert.rejects(exec(process.execPath, [cli, f.directory, '--commit', commit, '--receipt', f.receipt], { cwd: f.root, env: { PATH: bin, HOME: f.root } }), error => {
    assert.equal(error.code, 1);
    assert.doesNotMatch(error.stdout + error.stderr, /synthetic-private-token/);
    assert.match(error.stderr, /upload failed/i);
    return true;
  });
  await absent(f.receipt);
});
