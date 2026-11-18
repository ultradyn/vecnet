import test from 'node:test';
import assert from 'node:assert/strict';
import { onNextCreate } from './fake-plugin.ts';
import { sleep, until, setup, launch, status, running, turnReplies, waitReply, headCursor } from './harness.ts';

test('10d. restart: an unreachable backend keeps the job running and resumes when reattach succeeds', async t => {
  const env = await setup(t, { idle: { park_after: '10m', settle_opaque: '1h', settle_observable: '1h' } });
  const l = await running(env);
  const before = env.fake();
  env.daemon.crash();
  let reattaches = 0;
  onNextCreate(env.stateFile, b => {
    b.failReattach(2);
    const original = b.reattach.bind(b);
    b.reattach = (...args) => { reattaches++; return original(...args); };
  });
  await env.start();
  assert.notEqual(env.fake(), before);
  assert.equal(reattaches, 1);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.reattachPending), true);
  env.clock.shift(30_001);
  await until(() => reattaches === 2, 'second reattach attempt');
  await sleep(100);
  assert.equal(env.inspect(ledger => ledger.job(l.jobId)!.reattachPending), true);
  env.clock.shift(30_001);
  await until(() => reattaches === 3, 'third reattach attempt');
  await until(() => !env.inspect(ledger => ledger.job(l.jobId)!.reattachPending), 'reattached');
  const job = env.inspect(ledger => ledger.job(l.jobId)!);
  assert.equal(job.state, 'running');
  assert.equal(job.terminal, null);
  env.fake().finishTurn(l.jobId, 'resumed after reattach');
  await waitReply(env, l.thread, m => m.content.text === 'resumed after reattach');
  const history = await env.requester.history(l.thread);
  assert.deepEqual(history.filter(m => m.kind === 'job_status').map(m => m.job_status!.state), ['accepted', 'running']);
});

test('11. a launch that throws after creating its session is settled at once: failed backend_error, settled_orphan', async t => {
  const env = await setup(t);
  env.fake().throwAfterLaunch(new Error('lost the launch response'));
  const l = await launch(env);
  const failed = await status(env, l.jobId, 'failed');
  const out = failed.job_status!.output as { error: string; settled_orphan: boolean };
  assert.equal(out.error, 'backend_error');
  assert.equal(out.settled_orphan, true);
  assert.equal(env.fake().sessions().length, 1);
  assert.equal(env.fake().sessions()[0].settles, 1);
});

test('12. redelivery after a cursor reset: no second session, no duplicate replies', async t => {
  const env = await setup(t, { idle: { park_after: '60s', settle_opaque: '120s', settle_observable: '120s' } });
  const first = await headCursor(env.hub.url, env.launcher.secret);
  const l = await running(env);
  env.fake().finishTurn(l.jobId, 'turn one done');
  await waitReply(env, l.thread);
  const before = (await env.requester.history(l.thread)).map(m => m.message_id);
  await env.daemon.stop();
  env.inspect(ledger => ledger.setCursor(first));
  await env.start();
  // The replay re-admits every event after the first cursor, the launch
  // included: wait until the ledger reaches the hub's head as of the restart
  // (later events may move it further) with no ingress row left to process.
  const head = await headCursor(env.hub.url, env.launcher.secret);
  const seq = (c: string | null) => Number(/\.(\d+)$/.exec(c ?? '')?.[1] ?? NaN);
  assert.ok(Number.isFinite(seq(head)) && seq(head) > seq(first));
  await until(() => env.inspect(ledger => seq(ledger.cursor()) >= seq(head) && ledger.queued(10).length === 0), 'replay caught up');
  await sleep(1500);
  assert.equal(env.fake().sessions().length, 1);
  assert.equal(env.fake().sessions()[0].sent.length, 0);
  assert.equal(turnReplies(env, l.thread).length, 1);
  assert.deepEqual((await env.requester.history(l.thread)).map(m => m.message_id), before);
});

test('13. no credential leaks: the agent env has no VECNET_*, the daemon logs hold no secret or message text', async t => {
  const previous = process.env.VECNET_E2E_LEAK;
  process.env.VECNET_E2E_LEAK = 'must-not-reach-the-agent';
  t.after(() => { if (previous === undefined) delete process.env.VECNET_E2E_LEAK; else process.env.VECNET_E2E_LEAK = previous; });
  const env = await setup(t);
  const instructions = 'Instructions that must never be logged';
  // A backend error quoting a secret and the prompt: the daemon logs that it failed, never what it said.
  const fake = env.fake(), capabilities = fake.capabilities.bind(fake);
  let thrown = false;
  fake.capabilities = async () => {
    if (!thrown) { thrown = true; throw new Error(`${env.launcher.secret} ${instructions}`); }
    return capabilities();
  };
  const l = await running(env, { instructions });
  await env.requester.send({ thread_id: l.thread, in_reply_to: l.launchId, kind: 'message', priority: 'wake', content: { text: 'relayed text that must never be logged' } });
  await until(() => env.fake().sessions()[0].sent.length === 1, 'relayed message');
  env.fake().finishTurn(l.jobId, 'turn text that must never be logged');
  await waitReply(env, l.thread);
  await sleep(300);
  const spec = env.fake().sessions()[0].spec;
  assert.deepEqual(Object.keys(spec.env).filter(k => /^VECNET_/i.test(k)), []);
  assert.match(spec.prompt, /Instructions that must never be logged/);
  const logs = env.log.text();
  assert.ok(thrown);
  assert.ok(env.log.text().includes('backend capabilities unavailable'), 'the failure was logged');
  for (const secret of [env.launcher.secret, env.requester.secret, env.operator.secret, env.bystander.secret]) assert.ok(!logs.includes(secret), 'a secret was logged');
  for (const text of ['Instructions that must never be logged', 'relayed text that must never be logged', 'turn text that must never be logged']) assert.ok(!logs.includes(text), 'message text was logged');
});
