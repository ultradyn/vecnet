import { symlinkSync, writePrivate, makePrivateDirectory } from './fixtures/platform.ts';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
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
  publisher.publish(0, []);
  writePrivate(path + '.replacement', 'replacement');
  renameSync(path + '.replacement', path + '.status.json');
  await publisher.close(); await publisher.close();
  assert.equal(readFileSync(path + '.status.json', 'utf8'), 'replacement');
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
