import test from 'node:test';
import assert from 'node:assert/strict';
import { HubError } from '../../../hub-client/src/errors.ts';
import type { Delivery, SendRequest } from '../../../hub-client/src/wire.ts';
import type { AgentQuestion } from '../../src/backend.ts';
import { DAY, WAIT, sleep, until, setup, launchRequest, launch, status, running, delivered, turnReplies, waitReply, inputsOf } from './harness.ts';

test('1. launch: accepted, running with session_ref, one turn reply to the launch message, the launch processed', async t => {
  const env = await setup(t);
  const l = await launch(env);
  const accepted = await status(env, l.jobId, 'accepted');
  assert.equal(accepted.job_status!.sequence, 1);
  const run = await status(env, l.jobId, 'running');
  assert.equal(run.job_status!.sequence, 2);
  const output = run.job_status!.output as { session_ref: { backend: string; id: string; extra: { tag: string } } };
  assert.equal(output.session_ref.backend, 'fake');
  assert.equal(output.session_ref.extra.tag, `vl-${l.jobId}`);

  env.fake().finishTurn(l.jobId, 'turn one done');
  const reply = await waitReply(env, l.thread);
  assert.equal(reply.in_reply_to, l.launchId);
  assert.equal(reply.content.text, 'turn one done');
  assert.equal(reply.metadata['launch.turn'], 'completed');
  assert.equal(reply.metadata['launch.job_id'], l.jobId);
  // The launch message is turn one's input.
  assert.deepEqual(reply.metadata['launch.consumed'], [l.launchId]);
  // The launch Delivery to the launcher is marked processed (with turn one).
  await env.requester.waitFor(e => e.type === 'receipt' && (e.data as { message_id: string; state: string }).message_id === l.launchId
    && (e.data as { state: string }).state === 'processed' ? true : undefined, WAIT, 'processed receipt for the launch');
  await sleep(300);
  assert.equal(turnReplies(env, l.thread).length, 1);
  // Every launcher post replies to the launch message (depth stays 1).
  const history = await env.requester.history(l.thread);
  const posts = history.filter(m => m.sender === env.launcher.peerId);
  assert.ok(posts.length >= 3);
  for (const m of posts) { assert.equal(m.in_reply_to, l.launchId); assert.equal(m.loop.depth, 1); }
});

test('2. expiry cap and lifetime gate: 8 days refused by the hub, 3 s refused as expires_too_soon', async t => {
  const env = await setup(t);
  await assert.rejects(env.requester.send(launchRequest(env, { expiresInMs: 8 * DAY })), (err: unknown) =>
    err instanceof HubError && err.status === 422 && err.code === 'invalid_value');
  const l = await launch(env, { expiresInMs: 3000 });
  const failed = await status(env, l.jobId, 'failed', 2500);
  const out = failed.job_status!.output as { error: string; reason: string; details: { needed_ms: number } };
  assert.equal(out.error, 'policy_denied');
  assert.equal(out.reason, 'expires_too_soon');
  assert.equal(out.details.needed_ms, 4000 + 600_000);
  assert.equal(env.fake().sessions().length, 0);
  assert.equal(env.inspect(ledger => ledger.openJobs().length), 0);
});

test('3. steer, queue, buffer; a bystander added by the requester is not relayed', async t => {
  const env = await setup(t);
  const l = await running(env);
  const say = (text: string, priority: SendRequest['priority'], extra: Partial<SendRequest> = {}) =>
    env.requester.send({ thread_id: l.thread, in_reply_to: l.launchId, kind: 'message', priority, content: { text }, ...extra });

  const steer = await say('steer this turn', 'wake');
  await until(() => env.fake().sessions()[0].sent.length === 1, 'steer');
  const first = env.fake().sessions()[0].sent[0];
  assert.equal(first.mode, 'steer');
  assert.match(first.items[0].text, /steer this turn/);

  const hold = await say('held context', 'hold');
  await until(() => inputsOf(env, l.jobId, ['buffered']).length === 1, 'hold buffered');
  const queued = await say('next turn please', 'wake_idle');
  await until(() => inputsOf(env, l.jobId, ['buffered']).length === 2, 'wake_idle queued in the launcher');
  assert.equal(env.fake().sessions()[0].sent.length, 1);

  env.fake().finishTurn(l.jobId, 'turn one done');
  const reply = await waitReply(env, l.thread);
  assert.deepEqual(reply.metadata['launch.consumed'], [l.launchId, steer.message_id]);
  await until(() => env.fake().sessions()[0].sent.length === 2, 'next turn started');
  const next = env.fake().sessions()[0].sent[1];
  assert.equal(next.mode, 'start');
  assert.deepEqual(next.items.map(i => i.messageId), [hold.message_id, queued.message_id]);
  assert.match(next.items[0].text, /held context/);

  // The requester, the locked thread's creator, adds a bystander.
  await say('bystander, meet the session', 'hold', { recipients: [env.launcher.peerId, env.bystander.peerId] });
  await env.bystander.waitFor(e => delivered(e)?.thread_id === l.thread ? true : undefined, WAIT, 'bystander joined');
  const outsider = await env.bystander.send({ thread_id: l.thread, kind: 'message', priority: 'wake', content: { text: 'bystander says hi' } });
  const receipt = await env.bystander.waitFor(e => {
    const r = e.data as { message_id?: string; state?: string; note?: string };
    return e.type === 'receipt' && r.message_id === outsider.message_id && r.state === 'processed' ? r : undefined;
  }, WAIT, 'bystander receipt');
  assert.equal(receipt.note, 'not_relayed');
  await sleep(500);
  const relayed = env.fake().sessions()[0].sent.flatMap(s => s.items.map(i => i.text));
  assert.ok(!relayed.some(text => text.includes('bystander says hi')));
  const history = await env.requester.history(l.thread);
  assert.ok(!history.some(m => m.sender === env.launcher.peerId && m.in_reply_to === outsider.message_id));
  assert.equal(turnReplies(env, l.thread).length, 1);
});

test('4. question: a structured question reaches the requester and its reply answers it; a secret one never enters the thread', async t => {
  const env = await setup(t);
  const l = await running(env);
  const question: AgentQuestion = { requestId: 'q-colour', blocking: true,
    questions: [{ id: 'colour', text: 'Which colour should the button be?', options: [{ label: 'red' }, { label: 'blue' }], allowOther: false, secret: false }] };
  const answer = env.fake().ask(l.jobId, question);
  const asked = await env.requester.waitFor(e => {
    const m = delivered(e);
    return m?.thread_id === l.thread && m.metadata['launch.question'] ? m : undefined;
  }, WAIT, 'launch.question');
  assert.equal(asked.kind, 'message');
  assert.equal(asked.priority, 'wake');
  assert.equal(asked.in_reply_to, l.launchId);
  const meta = asked.metadata['launch.question'] as { v: number; blocking: boolean; questions: Array<{ id: string; options: string[] }> };
  assert.equal(meta.v, 1);
  assert.deepEqual(meta.questions.map(q => [q.id, q.options]), [['colour', ['red', 'blue']]]);
  await env.requester.reply(asked, 'Blue');
  assert.deepEqual(await answer, { answers: { colour: 'blue' }, answeredBy: env.requester.peerId });

  const secretText = 'What is the production database password?';
  const secret = await env.fake().ask(l.jobId, { requestId: 'q-secret', blocking: true,
    questions: [{ id: 'pw', text: secretText, allowOther: true, secret: true }] });
  assert.deepEqual(secret.answers, { pw: 'secrets cannot be provided through vecnet' });
  await env.requester.waitFor(e => {
    const m = delivered(e);
    return m?.thread_id === l.thread && m.content.text.includes('secrets cannot be provided through vecnet') ? true : undefined;
  }, WAIT, 'secret note');
  const history = await env.requester.history(l.thread);
  assert.ok(!history.some(m => m.content.text.includes(secretText) || JSON.stringify(m.metadata).includes(secretText)));
});