import test from 'node:test';
import assert from 'node:assert/strict';
import type { Clock } from '../../../hub-client/src/ports.ts';
import { validateSchema } from '../../src/contract/metadata.ts';
import { WAIT, PERMISSION_SCHEMA, until, setup, launch, status, running, delivered, waitReply, permissionRequest } from './harness.ts';

test('5. approval: allow once, escalate to an operator thread, expiry denies', async t => {
  const env = await setup(t);
  const l = await running(env);
  const prompt = (id: string) => env.requester.waitFor(e => {
    const m = delivered(e);
    return m?.kind === 'approval_request' && m.approval_request?.question.includes(`(${id})`) ? m : undefined;
  }, WAIT, `approval_request ${id}`);

  // 1. Allowed once by the requester.
  const first = env.fake().requestPermission(l.jobId, permissionRequest(env, 'p1'));
  const card = await prompt('p1');
  assert.equal(card.thread_id, l.thread);
  assert.equal(card.in_reply_to, l.launchId);
  assert.equal(card.priority, 'wake');
  assert.deepEqual(card.approval_request!.options, ['allow once', 'deny', 'escalate']);
  const permission = card.metadata['launch.permission'];
  assert.deepEqual(validateSchema(PERMISSION_SCHEMA, permission), []);
  assert.ok(Buffer.byteLength(JSON.stringify(permission)) < 6 * 1024);
  await env.requester.respond(card, 'allow once');
  assert.deepEqual(await first, { optionId: 'opt-allow', decidedBy: env.requester.peerId });

  // 2. Escalated: an operator prompt in a new escalation thread; the operator answers there.
  const second = env.fake().requestPermission(l.jobId, permissionRequest(env, 'p2'));
  const card2 = await prompt('p2');
  await env.requester.respond(card2, 'escalate');
  const opCard = await env.operator.waitFor(e => {
    const m = delivered(e);
    return m?.kind === 'approval_request' && m.approval_request?.question.includes('(p2)') ? m : undefined;
  }, WAIT, 'operator approval_request');
  assert.notEqual(opCard.thread_id, l.thread);
  assert.equal(opCard.metadata['launch.escalated_from'], card2.approval_request!.approval_id);
  assert.equal(opCard.metadata['launch.job_id'], l.jobId);
  assert.deepEqual(opCard.metadata['launch.permission'], card2.metadata['launch.permission']);
  assert.deepEqual(opCard.approval_request!.options, ['allow once', 'deny']);
  await env.operator.respond(opCard, 'allow once');
  assert.deepEqual(await second, { optionId: 'opt-allow', decidedBy: env.operator.peerId });
  const jobThread = await env.requester.history(l.thread);
  assert.ok(jobThread.every(m => !m.recipients.includes(env.operator.peerId)), 'the locked job thread never gains the operator');

  // 3. Unanswered: the parent prompt expires to the backend's deny option.
  const third = env.fake().requestPermission(l.jobId, permissionRequest(env, 'p3'));
  await prompt('p3');
  assert.deepEqual(await third, { optionId: 'opt-deny', decidedBy: 'expired' });
});

test('6. cancel reaches the hub: interrupt and settle, a cancelled report the hub accepts', async t => {
  const env = await setup(t);
  const l = await running(env);
  await env.requester.send({ thread_id: l.thread, in_reply_to: l.launchId, kind: 'job_status', priority: 'wake',
    content: { text: 'cancel' }, job_status: { job_id: l.jobId, sequence: 100, state: 'cancelled' } });
  const cancelled = await status(env, l.jobId, 'cancelled');
  assert.equal(cancelled.sender, env.launcher.peerId);
  const s = env.fake().sessions()[0];
  assert.ok(s.interrupts >= 1);
  assert.equal(s.settles, 1);
  // The requester's sequence is not the job's: the launcher's report is accepted as sequence 3.
  const history = await env.requester.history(l.thread);
  const reports = history.filter(m => m.kind === 'job_status' && m.sender === env.launcher.peerId).map(m => [m.job_status!.sequence, m.job_status!.state]);
  assert.deepEqual(reports, [[1, 'accepted'], [2, 'running'], [3, 'cancelled']]);
});

test('7. idle park and settle: park at about 2 s, settle at about 4 s, succeeded idle_wrapup', async t => {
  const env = await setup(t);
  const l = await running(env);
  // The idle window starts when the backend turn ends, not when its reply
  // reaches the requester: a slow delivery must not shorten the measurement.
  const start = Date.now();
  env.fake().finishTurn(l.jobId, 'all done');
  await waitReply(env, l.thread);
  let parkedAt = 0, settledAt = 0;
  await until(() => {
    const s = env.fake().sessions()[0];
    if (!parkedAt && s.parks > 0) parkedAt = Date.now() - start;
    if (!settledAt && s.settles > 0) settledAt = Date.now() - start;
    return settledAt > 0;
  }, 'idle settle', 10_000);
  // daemon.test.ts pins the 2 s / 4 s boundaries with the injected Clock.
  // This real-hub smoke check also allows for build-box and HTTP scheduling.
  assert.ok(parkedAt >= 1500 && parkedAt <= 8000, `parked after ${parkedAt} ms`);
  assert.ok(settledAt >= 3500 && settledAt <= 12_000, `settled after ${settledAt} ms`);
  assert.ok(parkedAt < settledAt);
  const done = await status(env, l.jobId, 'succeeded');
  const out = done.job_status!.output as { ended: string; quiescence: string; turns: number };
  assert.equal(out.ended, 'idle_wrapup');
  assert.equal(out.quiescence, 'proven');
  assert.equal(out.turns, 1);
  assert.equal(env.fake().sessions()[0].parks, 1);
});

test('8. deadline: a launch that just passes the gate is settled at expires_at - 1 s, before the hub refuses posts', async t => {
  // The gate needs settle (raised to 30 s) + 10 min of life. The hub's clock
  // cannot be moved, so the daemon's clock starts behind real time by enough
  // to pass the gate with an expires_at 8 s away, then catches up.
  const lifeMs = 8000, behind = 30_000 + 600_000 + 2000 - lifeMs;
  const env = await setup(t, { idle: { settle_opaque: '30s', settle_observable: '30s' }, clockOffsetMs: -behind });
  const l = await running(env, { expiresInMs: lifeMs });
  env.clock.shift(behind);
  const done = await status(env, l.jobId, 'succeeded', lifeMs + 5000);
  const out = done.job_status!.output as { ended: string };
  assert.equal(out.ended, 'deadline');
  const expires = Date.parse(l.expiresAt);
  assert.ok(Date.parse(done.created_at) < expires, 'posted before the launch expired');
  assert.ok(Date.parse(done.created_at) >= expires - 1000 - 750, 'not settled long before expires_at - 1 s');
  assert.equal(env.fake().sessions()[0].settles, 1);
});