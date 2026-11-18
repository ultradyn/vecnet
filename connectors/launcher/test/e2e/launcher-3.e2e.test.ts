import test from 'node:test';
import assert from 'node:assert/strict';
import { sleep, until, setup, launch, status, running, turnReplies, waitReply } from './harness.ts';

test('9. long reply: 60 KiB text with the marker, the full text attached as turn-1.md', async t => {
  const env = await setup(t);
  const l = await running(env);
  const line = 'the agent wrote a long and careful reply line\n';
  const full = line.repeat(Math.ceil(100 * 1024 / line.length)).slice(0, 100 * 1024);
  env.fake().finishTurn(l.jobId, full);
  const reply = await waitReply(env, l.thread);
  // The hub spills text over 4 KiB; the spill holds the launcher's capped text.
  const spill = reply.content.truncated ? reply.attachments.findLast(a => a.name === 'message.md') : undefined;
  const text = spill ? new TextDecoder().decode(await env.requester.hub.fetchBlob(spill.blob_url!, { maxBytes: 256 * 1024 })) : reply.content.text;
  assert.ok(Buffer.byteLength(text) <= 60 * 1024, `${Buffer.byteLength(text)} bytes`);
  assert.ok(Buffer.byteLength(text) > 60 * 1024 - 64);
  assert.ok(text.endsWith(`\n[truncated: ${100 * 1024} bytes; full text attached]`));
  assert.ok(full.startsWith(text.slice(0, text.lastIndexOf('\n[truncated:'))));
  const attached = reply.attachments.find(a => a.name === 'turn-1.md');
  assert.ok(attached?.blob_url, 'turn-1.md attachment with a blob_url');
  assert.equal(attached.media_type, 'text/markdown');
  const bytes = await env.requester.hub.fetchBlob(attached.blob_url, { maxBytes: 256 * 1024 });
  assert.equal(new TextDecoder().decode(bytes), full);
});

test('10a. restart: a turn finished while the daemon was down is replied to exactly once', async t => {
  const env = await setup(t, { idle: { park_after: '60s', settle_opaque: '120s', settle_observable: '120s' } });
  const l = await running(env);
  const before = env.fake();
  env.daemon.crash();
  before.finishTurn(l.jobId, 'finished while down');
  await env.start();
  assert.notEqual(env.fake(), before, 'the restarted daemon loaded a new fake from the state file');
  await waitReply(env, l.thread, m => m.content.text === 'finished while down');
  await sleep(1000);
  assert.equal(turnReplies(env, l.thread).length, 1);
  const history = await env.requester.history(l.thread);
  assert.equal(history.filter(m => m.content.text === 'finished while down').length, 1);
  assert.equal(env.fake().sessions().length, 1);
});

test('10b. restart: a session lost while the daemon was down ends failed outcome_unknown', async t => {
  const env = await setup(t);
  const l = await running(env);
  const before = env.fake();
  env.daemon.crash();
  before.lose(l.jobId);
  await env.start();
  const failed = await status(env, l.jobId, 'failed');
  assert.equal((failed.job_status!.output as { error: string }).error, 'outcome_unknown');
  assert.equal(env.fake().sessions().length, 0);
});

test('10c. restart: a crash between launch and recording the ref yields exactly one session', async t => {
  const env = await setup(t, { noStart: true });
  await env.start({ beforeRecordRef: () => { throw new Error('process died'); } });
  const l = await launch(env);
  await status(env, l.jobId, 'accepted');
  await until(() => env.fake().sessions().length === 1, 'session launched');
  await sleep(200);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.sessionRef), null);
  env.daemon.crash();
  await env.start();
  await status(env, l.jobId, 'running');
  await sleep(500);
  assert.equal(env.fake().sessions().length, 1);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.sessionRef!.id), l.jobId);
  const history = await env.requester.history(l.thread);
  assert.equal(history.filter(m => m.job_status?.state === 'running').length, 1);
});