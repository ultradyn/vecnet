import { symlinkSync, writePrivate, makePrivateDirectory } from './fixtures/platform.ts';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import { mkdtempSync, rmSync, readFileSync, existsSync, renameSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { openStatusPublisher } from '../src/codex/connector-status.ts';

const moduleUrl = new URL('../src/codex/connector-status.ts', import.meta.url).href;
test('readiness lock excludes another process and releases on process death', async t => {
  const root = mkdtempSync(join(tmpdir(), 'vn-cs-')), dir = join(root, 'private'), path = join(dir, 'bindings');
  t.after(() => rmSync(root, { recursive: true, force: true }));
  makePrivateDirectory(dir);
  writePrivate(path, '{}');
  const child = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', '--input-type=module', '-e', `
    import { openStatusPublisher } from ${JSON.stringify(moduleUrl)};
    const publisher = await openStatusPublisher(process.argv[1], (process.geteuid?.() ?? -1));
    if (!publisher) process.exit(2);
    publisher.publish(0, []);
    process.stdout.write('ready');
    process.stdin.resume();
  `, path], { stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => child.kill('SIGKILL'));
  const closed = once(child, 'close');
  const ready = await Promise.race([once(child.stdout, 'data'), closed.then(() => { throw new Error('publisher exited'); })]);
  assert.equal(String(ready[0]), 'ready');
  const alias = process.platform === 'win32' ? path.toUpperCase() : join(dir, 'alias');
  if (process.platform !== 'win32') symlinkSync(path, alias);
  assert.equal(await openStatusPublisher(alias, (process.geteuid?.() ?? -1)), undefined, 'canonical authority aliases share the lock');
  const original = readFileSync(path + '.status.json', 'utf8');
  child.kill('SIGKILL'); await closed;
  const next = await openStatusPublisher(path, (process.geteuid?.() ?? -1));
  assert.ok(next, 'OS releases crashed owner lock');
  try {
    next.publish(10_000, []);
    assert.notEqual(readFileSync(path + '.status.json', 'utf8'), original);
    if (process.platform !== 'win32') assert.equal(statSync(path + '.status.json').mode & 0o777, 0o600);
  } finally { await next.close(); }
  assert.equal(existsSync(path + '.status.json'), false);
});

test('stop never deletes a replacement status file owned by someone else', async t => {
  const root = mkdtempSync(join(tmpdir(), 'vn-cs-')), dir = join(root, 'private'), path = join(dir, 'bindings');
  t.after(() => rmSync(root, { recursive: true, force: true }));
  makePrivateDirectory(dir);
  writePrivate(path, '{}');
  const publisher = await openStatusPublisher(path, (process.geteuid?.() ?? -1)); assert.ok(publisher);
  try {
    publisher.publish(0, []);
    const original = statSync(path + '.status.json', { bigint: true });
    writePrivate(path + '.replacement', 'replacement');
    renameSync(path + '.replacement', path + '.status.json');
    const replacement = statSync(path + '.status.json', { bigint: true });
    assert.notEqual(replacement.ino, original.ino, 'replacement must have a distinct exact file ID');
    await publisher.close(); await publisher.close();
    assert.equal(readFileSync(path + '.status.json', 'utf8'), 'replacement');
  } finally { await publisher.close(); }
});

test('stop preserves a replacement whose 64-bit file ID rounds to the old owner ID', async t => {
  const root = mkdtempSync(join(tmpdir(), 'vn-cs-')), dir = join(root, 'private'), path = join(dir, 'bindings');
  t.after(() => rmSync(root, { recursive: true, force: true }));
  makePrivateDirectory(dir); writePrivate(path, '{}');
  // NTFS file IDs are uint64. Keep real files, locks, publication and rename;
  // model only IDs that cannot be represented exactly by number-valued Stats.
  const originalId = 2n ** 60n, replacementId = originalId + 1n;
  assert.equal(Number(originalId), Number(replacementId));
  let identity = originalId;
  const lstat = fs.lstatSync;
  const mock = t.mock.method(fs, 'lstatSync', (...args: Parameters<typeof lstat>) => {
    const st = Reflect.apply(lstat, fs, args);
    if (args[0] !== path + '.status.json') return st;
    return Object.assign(Object.create(Object.getPrototypeOf(st)), st,
      { ino: typeof args[1] === 'object' && args[1]?.bigint ? identity : Number(identity) });
  });
  syncBuiltinESMExports();
  t.after(() => { mock.mock.restore(); syncBuiltinESMExports(); });
  const publisher = await openStatusPublisher(path, (process.geteuid?.() ?? -1)); assert.ok(publisher);
  try {
    publisher.publish(0, []);
    writePrivate(path + '.replacement', 'replacement');
    renameSync(path + '.replacement', path + '.status.json'); identity = replacementId;
    await publisher.close(); await publisher.close();
    assert.equal(readFileSync(path + '.status.json', 'utf8'), 'replacement');
  } finally { await publisher.close(); }
});

test('startup publishes empty readiness before the authority directory exists', async t => {
  const root = mkdtempSync(join(tmpdir(), 'vn-cs-')), dir = join(root, 'private');
  t.after(() => rmSync(root, { recursive: true, force: true }));
  makePrivateDirectory(dir);
  const path = join(dir, 'state', 'codex', 'bindings.json');
  const publisher = await openStatusPublisher(path, (process.geteuid?.() ?? -1));
  assert.ok(publisher);
  try {
    publisher.publish(0, []);
    assert.deepEqual(JSON.parse(readFileSync(path + '.status.json', 'utf8')), {
      version: 1, updated_at: '1970-01-01T00:00:00.000Z', bindings: [],
    });
    assert.equal(existsSync(path), false, 'readiness must never create authority');
  } finally { await publisher.close(); }
});
