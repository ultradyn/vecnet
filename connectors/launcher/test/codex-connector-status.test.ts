import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtempSync, writeFileSync, rmSync, readFileSync, existsSync, renameSync, statSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { openStatusPublisher } from '../src/codex/connector-status.ts';

const moduleUrl = new URL('../src/codex/connector-status.ts', import.meta.url).href;
test('readiness lock excludes another process and releases on process death', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-cs-')), path = join(dir, 'bindings');
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  writeFileSync(path, '{}', { mode: 0o600 });
  const child = spawn(process.execPath, ['--disable-warning=ExperimentalWarning', '--input-type=module', '-e', `
    import { openStatusPublisher } from ${JSON.stringify(moduleUrl)};
    const publisher = openStatusPublisher(process.argv[1], process.geteuid());
    if (!publisher) process.exit(2);
    publisher.publish(0, []);
    process.stdout.write('ready');
    process.stdin.resume();
  `, path], { stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => child.kill('SIGKILL'));
  const closed = once(child, 'close');
  const ready = await Promise.race([once(child.stdout, 'data'), closed.then(() => { throw new Error('publisher exited'); })]);
  assert.equal(String(ready[0]), 'ready');
  const alias = join(dir, 'alias'); symlinkSync(path, alias);
  assert.equal(openStatusPublisher(alias, process.geteuid!()), undefined, 'canonical authority aliases share the lock');
  const original = readFileSync(path + '.status.json', 'utf8');
  child.kill('SIGKILL'); await closed;
  const next = openStatusPublisher(path, process.geteuid!());
  assert.ok(next, 'OS releases crashed owner lock');
  try {
    next.publish(10_000, []);
    assert.notEqual(readFileSync(path + '.status.json', 'utf8'), original);
    assert.equal(statSync(path + '.status.json').mode & 0o777, 0o600);
  } finally { next.close(); }
  assert.equal(existsSync(path + '.status.json'), false);
});

test('stop never deletes a replacement status file owned by someone else', t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-cs-')), path = join(dir, 'bindings');
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  writeFileSync(path, '{}', { mode: 0o600 });
  const publisher = openStatusPublisher(path, process.geteuid!()); assert.ok(publisher);
  publisher.publish(0, []);
  writeFileSync(path + '.replacement', 'replacement', { mode: 0o600 });
  renameSync(path + '.replacement', path + '.status.json');
  publisher.close(); publisher.close();
  assert.equal(readFileSync(path + '.status.json', 'utf8'), 'replacement');
});

test('startup publishes empty readiness before the authority directory exists', t => {
  const dir = mkdtempSync(join(tmpdir(), 'vn-cs-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const path = join(dir, 'state', 'codex', 'bindings.json');
  const publisher = openStatusPublisher(path, process.geteuid!());
  assert.ok(publisher);
  try {
    publisher.publish(0, []);
    assert.deepEqual(JSON.parse(readFileSync(path + '.status.json', 'utf8')), {
      version: 1, updated_at: '1970-01-01T00:00:00.000Z', bindings: [],
    });
    assert.equal(existsSync(path), false, 'readiness must never create authority');
  } finally { publisher.close(); }
});
