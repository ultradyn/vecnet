#!/usr/bin/env node
import { execFile } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { constants } from 'node:fs';
import { lstat, mkdir, open, readFile, readdir, realpath, writeFile } from 'node:fs/promises';
import { dirname, extname, isAbsolute, join, relative, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';

const exec = promisify(execFile);
const types = new Map([
  ['.png', 'image/png'], ['.jpg', 'image/jpeg'], ['.jpeg', 'image/jpeg'],
  ['.gif', 'image/gif'], ['.webp', 'image/webp'], ['.mp4', 'video/mp4'],
  ['.webm', 'video/webm'], ['.mov', 'video/quicktime'],
]);

// Only fixed, publisher-owned messages cross the CLI boundary. In particular,
// never attach subprocess/fetch errors as causes: they can contain credentials.
class PublishError extends Error {}
const fail = message => { throw new PublishError(message); };
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const serialize = value => `${JSON.stringify(value, null, 2)}\n`;
const encode = value => encodeURIComponent(value).replace(/[!'()*]/g, c => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
const encodePath = path => path.split('/').map(encode).join('/');
const escape = text => String(text).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

function validateConfig(config, override) {
  if (!config || !/^[a-f0-9]{32}$/i.test(config.accountId ?? '')) fail('Invalid account ID in evidence config.');
  if (!/^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/.test(config.bucket ?? '')) fail('Invalid evidence bucket.');
  if (!Number.isSafeInteger(config.retentionDays) || config.retentionDays <= 0) fail('Invalid retention days.');
  let url;
  try { url = new URL(override ?? config.publicUrl); } catch { fail('Invalid public HTTPS URL.'); }
  // search/hash omit bare delimiters, but href retains them. Encoded path
  // characters (%3F/%23) remain valid because they contain no literal delimiter.
  if (url.protocol !== 'https:' || url.username || url.password || /[?#]/.test(url.href)) fail('Invalid public HTTPS URL.');
  return { ...config, publicUrl: url.href.replace(/\/+$/, '') };
}

async function loadConfig() {
  try { return JSON.parse(await readFile(new URL('./config.json', import.meta.url), 'utf8')); }
  catch { fail('Cannot read evidence config.'); }
}

function inside(parent, child) {
  const path = relative(parent, child);
  return path === '' || (!path.startsWith('..' + '/') && path !== '..' && !isAbsolute(path));
}

// Resolve existing ancestors too, so an output symlink cannot route writes back
// into the input tree. Missing output/receipt directories are allowed.
async function canonicalTarget(path) {
  try { return await realpath(path); }
  catch (error) {
    if (error.code !== 'ENOENT') throw error;
    const parent = dirname(path);
    if (parent === path) throw error;
    return join(await canonicalTarget(parent), relative(parent, path));
  }
}

async function scan(directory, name = '') {
  const path = join(directory, name);
  const stat = await lstat(path);
  if (stat.isSymbolicLink()) fail('Symlink inputs are not supported.');
  if (stat.isDirectory()) {
    const entries = (await readdir(path)).sort();
    const files = [];
    for (const entry of entries) files.push(...await scan(directory, name ? `${name}/${entry}` : entry));
    return files;
  }
  if (!stat.isFile()) fail('Only regular files and directories are supported; special file found.');
  if (!name) fail('Input must be a directory.');
  const contentType = types.get(extname(name).toLowerCase());
  return contentType ? [{ name, path, contentType }] : [];
}

function gallery(manifest) {
  const expiry = manifest.kept ? 'Kept evidence: no automatic expiration.'
    : `Expected expiry: approximately ${manifest.expiresAt}. Actual deletion follows the bucket lifecycle.`;
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; base-uri 'none'">
<title>vecnet public evidence</title><style>body{font:16px system-ui;margin:2rem auto;padding:0 1rem;max-width:80rem}main{display:grid;grid-template-columns:repeat(auto-fit,minmax(18rem,1fr));gap:1rem}figure{margin:0;overflow-wrap:anywhere}img,video{max-width:100%;height:auto}code{overflow-wrap:anywhere}</style></head>
<body><h1>vecnet public evidence</h1><p>Source: <code>${escape(manifest.sourceCommit)}</code></p>
<p>Created: ${escape(manifest.createdAt)}</p><p>${escape(expiry)}</p><p><a href="manifest.json">Integrity manifest</a></p>
<main>${manifest.files.map(file => {
    // Relative media URLs also make the staged gallery inspectable offline.
    const url = escape(encodePath(`media/${file.name}`));
    const name = escape(file.name);
    const media = file.contentType.startsWith('video/')
      ? `<video controls preload="none" src="${url}"></video>`
      : `<img loading="lazy" src="${url}" alt="${name}">`;
    return `<figure>${media}<figcaption><a href="${url}">${name}</a> (${file.size} bytes)<br><code>${file.sha256}</code></figcaption></figure>`;
  }).join('\n')}</main></body></html>\n`;
}

/** Wrangler owns authentication. No tokens are accepted in arguments or read here. */
export function createWranglerUploader({ executable = 'wrangler', env = process.env } = {}) {
  return async object => {
    try {
      await exec(executable, [
        'r2', 'object', 'put', `${object.bucket}/${object.key}`, '--remote',
        '--file', object.path, '--content-type', object.contentType,
        '--cache-control', 'public, max-age=300, no-transform', '--force',
      ], {
        env: { ...env, CLOUDFLARE_ACCOUNT_ID: object.accountId, WRANGLER_SEND_METRICS: 'false' },
        encoding: 'utf8', maxBuffer: 1024 * 1024, timeout: 300_000,
      });
    } catch { fail('Evidence upload failed (Wrangler output suppressed).'); }
  };
}

async function readPublicBytes(url) {
  // Redirects could route public verification into an authenticated/private host.
  const response = await fetch(url, { redirect: 'error', signal: AbortSignal.timeout(30_000), cache: 'no-store' });
  if (!response.ok) fail('Public download failed.');
  return Buffer.from(await response.arrayBuffer());
}

async function uploadBatch(objects, upload) {
  let next = 0;
  let failed = false;
  async function worker() {
    while (!failed && next < objects.length) {
      const object = objects[next++];
      try { await upload(object); }
      catch { failed = true; }
    }
  }
  // Wait for both workers, including on failure; no uploads continue after return.
  await Promise.all([worker(), worker()]);
  if (failed) fail('Evidence upload failed (transport output suppressed).');
}

/**
 * Offline seams: clock() -> Date, upload({accountId,bucket,key,path,contentType}),
 * readPublic(httpsUrl) -> Uint8Array. config/outputRoot avoid operator state in tests.
 * A dry run returns receipt:null and creates only staged assets, never a receipt.
 */
export async function publishEvidence(options = {}) {
  try { return await publish(options); }
  catch (error) {
    if (error instanceof PublishError) throw error;
    fail('Evidence publishing failed (internal details suppressed).');
  }
}

async function publish({ directory, commit, keep = false, dryRun = false, receipt,
  publicUrl, config, outputRoot = resolve('.scratch/evidence-publish'),
  clock = () => new Date(), upload, readPublic = readPublicBytes }) {
  config = validateConfig(config ?? await loadConfig(), publicUrl);
  if (!directory || typeof directory !== 'string') fail('An input directory is required.');
  if (commit === undefined) {
    try { commit = (await exec('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' })).stdout.trim(); }
    catch { fail('Cannot resolve source commit; use --commit with a full SHA.'); }
  }
  if (typeof commit !== 'string' || !/^[a-f0-9]{40}$/i.test(commit)) fail('Source commit must be a full 40-character hexadecimal SHA.');
  commit = commit.toLowerCase();
  directory = resolve(directory);
  const files = await scan(directory);
  if (!files.length) fail('No supported media in the input directory.');
  const sourceRoot = await realpath(directory);
  outputRoot = await canonicalTarget(resolve(outputRoot));
  if (inside(sourceRoot, outputRoot)) fail('Output directory must be outside the source tree.');
  if (receipt) {
    receipt = resolve(receipt);
    if (inside(sourceRoot, await canonicalTarget(receipt))) fail('Receipt must be outside the source tree.');
    try { await lstat(receipt); fail('Receipt already exists; choose a new path.'); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  const now = clock();
  if (!(now instanceof Date) || !Number.isFinite(now.getTime())) fail('Invalid publication clock.');
  const createdAt = now.toISOString();
  const expiry = new Date(now.getTime() + config.retentionDays * 86_400_000);
  if (!keep && !Number.isFinite(expiry.getTime())) fail('Invalid retention expiry.');
  const expiresAt = keep ? null : expiry.toISOString();
  const run = `${createdAt.replace(/[:.]/g, '-')}-${randomBytes(12).toString('hex')}`;
  const prefix = `${keep ? 'kept' : 'runs'}/${commit}/${run}/`;
  const runDirectory = join(outputRoot, run);
  const urlFor = key => `${config.publicUrl}/${encodePath(key)}`;
  await mkdir(outputRoot, { recursive: true });
  await mkdir(runDirectory);
  const manifest = { version: 1, sourceCommit: commit, createdAt, expiresAt, kept: keep, prefix, files: [] };
  const media = [];
  for (const file of files) {
    // Upload immutable staged bytes rather than re-reading a changing capture.
    const handle = await open(file.path, constants.O_RDONLY | constants.O_NOFOLLOW);
    let bytes;
    try {
      if (!(await handle.stat()).isFile()) fail('Media must remain a regular file.');
      bytes = await handle.readFile();
    } finally { await handle.close(); }
    const path = join(runDirectory, 'media', file.name);
    await mkdir(dirname(path), { recursive: true });
    await writeFile(path, bytes, { flag: 'wx' });
    const key = prefix + 'media/' + file.name;
    const entry = { name: file.name, key, sha256: sha256(bytes), size: bytes.length, contentType: file.contentType, url: urlFor(key) };
    manifest.files.push(entry);
    media.push({ ...entry, path, accountId: config.accountId, bucket: config.bucket });
  }
  const indexes = [];
  for (const [name, bytes, contentType] of [
    ['manifest.json', Buffer.from(serialize(manifest)), 'application/json'],
    ['index.html', Buffer.from(gallery(manifest)), 'text/html; charset=utf-8'],
  ]) {
    const path = join(runDirectory, name);
    await writeFile(path, bytes, { flag: 'wx' });
    const key = prefix + name;
    indexes.push({ key, path, sha256: sha256(bytes), size: bytes.length, contentType, url: urlFor(key), accountId: config.accountId, bucket: config.bucket });
  }
  const galleryUrl = urlFor(prefix + 'index.html');
  const manifestUrl = urlFor(prefix + 'manifest.json');
  const result = { runDirectory, manifest, galleryUrl, manifestUrl, receipt: null, receiptPath: null };
  if (dryRun) return result;
  upload ??= createWranglerUploader();
  await uploadBatch(media, upload);
  await uploadBatch(indexes, upload);
  const objects = [...media, ...indexes];
  for (const object of objects) {
    let bytes;
    try { bytes = Buffer.from(await readPublic(object.url)); }
    catch { fail('Public evidence verification failed (download details suppressed).'); }
    if (bytes.length !== object.size || sha256(bytes) !== object.sha256) fail('Public evidence verification failed: byte length or SHA-256 mismatch.');
  }
  const success = { version: 1, sourceCommit: commit, createdAt, expiresAt, kept: keep, galleryUrl, manifestUrl,
    objects: objects.map(({ key, url, sha256, size }) => ({ key, url, sha256, size })) };
  const receiptPath = receipt ?? join(runDirectory, 'receipt.json');
  await mkdir(dirname(receiptPath), { recursive: true });
  await writeFile(receiptPath, serialize(success), { flag: 'wx' });
  return { ...result, receipt: success, receiptPath };
}

const help = `Usage: node scripts/evidence/publish.mjs DIRECTORY [--commit SHA] [--keep]
       [--dry-run] [--receipt FILE] [--public-url HTTPS_URL]

Publish PNG, JPEG, GIF, WebP, MP4, WebM and MOV evidence using Wrangler.
--commit SHA       Full source SHA (default: git rev-parse HEAD).
--keep             Use kept/ with no automatic expiry (default: runs/, 30 days).
--dry-run          Stage manifest, gallery and media without external requests.
--receipt FILE     Write a new receipt only after public verification succeeds.
--public-url URL   Override the public HTTPS origin/base path.
Generated assets default to .scratch/evidence-publish/ under the working directory.
`;

async function main(args) {
  if (args.length === 1 && ['--help', '-h'].includes(args[0])) { process.stdout.write(help); return; }
  const options = {};
  const values = new Map([['--commit', 'commit'], ['--receipt', 'receipt'], ['--public-url', 'publicUrl']]);
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (values.has(arg)) {
      const value = args[++i];
      if (!value || value.startsWith('--') || options[values.get(arg)] !== undefined) fail('Invalid or repeated CLI option. Use --help.');
      options[values.get(arg)] = value;
    } else if (arg === '--keep') options.keep = true;
    else if (arg === '--dry-run') options.dryRun = true;
    else if (arg.startsWith('-') || options.directory) fail('Invalid CLI arguments. Use --help.');
    else options.directory = arg;
  }
  const result = await publishEvidence(options);
  if (options.dryRun) process.stdout.write(`Dry-run: staged assets in ${result.runDirectory}\nNo receipt emitted.\n`);
  else process.stdout.write(`${serialize(result.receipt)}Receipt: ${result.receiptPath}\n`);
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  main(process.argv.slice(2)).catch(error => {
    process.stderr.write(`${error instanceof PublishError ? error.message : 'Evidence publishing failed (details suppressed).'}\n`);
    process.exitCode = 1;
  });
}
