import test from 'node:test';
import assert from 'node:assert/strict';
import { directoryEntry, event, fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { openLedger } from '../src/ledger.ts';
import { createIngress } from '../src/ingress.ts';
import { harness, launch, launched, message, settle, until, LAUNCHER, OPERATOR, REQUESTER, STRANGER } from './daemon-harness.ts';

test('ingress: duplicate delivery admitted once; cursor saved then acked; receipt/presence/approval_expired dropped', async t => {
  const clock = fakeClock(1000), log = memoryLog(), ledger = openLedger(':memory:', clock);
  t.after(() => ledger.close());
  const acks: Array<{ cursor: string; saved: string | null }> = [];
  const directory: unknown[] = [];
  const ingress = createIngress({ ledger, log, self: LAUNCHER, clock, onDirectory: data => { directory.push(data); },
    hub: { async ack(cursor) { acks.push({ cursor, saved: ledger.cursor() }); } } });
  const d = launch(clock.now());
  await ingress.handler(event('delivery', d, 'e1.1'));
  await ingress.handler(event('delivery', d, 'e1.2'));
  assert.equal(ledger.queued(10).length, 1);
  assert.equal(ledger.queued(10)[0].deliveryId, d.delivery_id);
  assert.equal(ledger.queued(10)[0].cursor, 'e1.1');
  assert.equal(ledger.cursor(), 'e1.2');
  // The first ack goes at once and only after its cursor was saved; later ones are batched to at most one a second.
  assert.deepEqual(acks, [{ cursor: 'e1.1', saved: 'e1.1' }]);
  for (const [i, type] of (['receipt', 'presence', 'approval_expired', 'thread_updated'] as const).entries()) {
    await ingress.handler(event(type, { delivery_id: d.delivery_id }, `e1.${i + 3}`));
  }
  assert.equal(ledger.queued(10).length, 1);
  assert.deepEqual(directory, []);
  assert.equal(ledger.cursor(), 'e1.6');
  assert.equal(acks.length, 1);
  clock.advance(1000);
  await until(() => acks.length === 2, 'batched ack');
  assert.deepEqual(acks.at(-1), { cursor: 'e1.6', saved: 'e1.6' });
  assert.equal(acks.length, 2);
  await ingress.handler(event('directory_updated', { change: 'removed', peer_id: REQUESTER }, 'e1.7'));
  assert.deepEqual(directory, [{ change: 'removed', peer_id: REQUESTER }]);
  // A Delivery addressed to another peer is never admitted.
  const other = launch(clock.now()); other.recipient = STRANGER;
  await ingress.handler(event('delivery', other, 'e1.8'));
  assert.equal(ledger.queued(10).length, 1);
  // cursors is the CursorStore the real stream resumes from.
  assert.equal(await ingress.cursors.load(), 'e1.8');
  await ingress.cursors.save('e1.9');
  assert.equal(ledger.cursor(), 'e1.9');
});

test('launch delivery → accepted → running job_status pair; redelivery after cursor reset launches nothing new', async t => {
  const h = await harness(t);
  const { d, jobId, thread } = await launched(h);
  const statuses = h.statuses(jobId);
  assert.deepEqual(statuses.map(s => [s.sequence, s.state]), [[1, 'accepted'], [2, 'running']]);
  assert.deepEqual(statuses[0].output, { host: 'test-host', backend: 'fake', preset: 'test', permission_mode: 'ask' });
  const running = statuses[1].output as Record<string, unknown>;
  assert.equal(running.branch, `launch/${jobId}`);
  for (const s of h.sends().filter(s => s.job_status)) {
    assert.equal(s.thread_id, thread); assert.equal(s.in_reply_to, d.message.message_id); assert.equal(s.priority, 'hold');
  }
  assert.equal(h.inspect(l => l.job(jobId)!.state), 'running');
  // Replayed after a cursor reset: the same delivery_id, then a hub redelivery of the same job.
  await h.push('delivery', d);
  await h.push('delivery', { ...structuredClone(d), delivery_id: 'dly_01JD3Q5H2A4C6E8G0J2M4N6P9Z', attempt: 2 });
  await settle();
  assert.equal(h.launches(), 1);
  assert.equal(h.backend.sessions().length, 1);
  assert.equal(h.statuses(jobId).length, 2);
  assert.equal(h.inspect(l => l.queued(10).length), 0);
});

test('refusal paths: policy_denied, cap_reached, invalid_input, unsupported operation each give one failed job_status committed with finishIngress', async t => {
  const h = await harness(t, { cfg: { policies: {
    default: { backends: ['fake'], permission_modes: ['read-only', 'ask'], projects: ['repo'], max_sessions: 3, max_open_sessions: 5, launches_per_hour: 30 },
    [STRANGER]: { max_sessions: 0 },
  } } });
  const denied = launch(h.clock.now(), { permission_mode: 'full-access' });
  const capped = launch(h.clock.now()); capped.message.sender = STRANGER;
  const invalid = launch(h.clock.now(), { preset: undefined });
  delete (invalid.message.job_request!.input as Record<string, unknown>).preset;
  const unsupported = launch(h.clock.now()); unsupported.message.job_request!.operation = 'review';
  const cases = [
    [denied, { error: 'policy_denied', reason: 'permission_mode' }],
    [capped, { error: 'cap_reached', details: { limit: 'max_sessions', running: 0, open: 0 } }],
    [invalid, { error: 'invalid_input', reason: '/preset: required' }],
    [unsupported, { error: 'unsupported', reason: 'operation is not supported' }],
  ] as const;
  for (const [d] of cases) await h.push('delivery', d);
  for (const [d, output] of cases) {
    const jobId = d.message.metadata['vecnet.job_id'] as string;
    await until(() => h.statuses(jobId).length > 0, `failed job_status for ${output.error}`);
    await settle(20);
    assert.deepEqual(h.statuses(jobId).map(s => ({ seq: s.sequence, state: s.state, output: s.output })), [{ seq: 1, state: 'failed', output }]);
    // The refusal, its terminal job row and the finished ingress row were one commit.
    h.inspect(l => {
      const job = l.job(jobId)!;
      assert.equal(job.state, 'ended'); assert.equal(job.terminal, 'failed');
      assert.equal(job.instructions, undefined);
      assert.equal(l.queued(10).some(r => r.deliveryId === d.delivery_id), false);
      assert.equal(l.counts().open, 0);
    });
  }
  assert.equal(h.launches(), 0);
});

test('a second job_request in a job thread → failed one job per thread', async t => {
  const h = await harness(t);
  const first = await launched(h);
  const second = launch(h.clock.now(), {}, {});
  second.thread.thread_id = first.thread; second.message.thread_id = first.thread;
  const secondId = second.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', second);
  await until(() => h.statuses(secondId).length > 0, 'one-job-per-thread refusal');
  const [status] = h.statuses(secondId);
  assert.equal(status.state, 'failed'); assert.equal(status.sequence, 1);
  assert.deepEqual(status.output, { error: 'unsupported', reason: 'one job per thread' });
  const sent = h.sends().find(s => s.job_status?.job_id === secondId)!;
  assert.equal(sent.thread_id, first.thread); assert.equal(sent.in_reply_to, second.message.message_id);
  assert.equal(h.launches(), 1);
  assert.equal(h.inspect(l => l.jobByThread(first.thread)!.jobId), first.jobId);
  assert.equal(h.inspect(l => l.job(first.jobId)!.state), 'running');
});

test('unknown sender: the directory refreshes once, then a just-created bot launches', async t => {
  const h = await harness(t);
  const bot = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8F';
  const before = h.peersCalls();
  h.hub.setPeers([
    directoryEntry({ peer_id: LAUNCHER, name: 'launcher-test', kind: 'launcher' }),
    directoryEntry({ peer_id: REQUESTER, name: 'requester', kind: 'session' }),
    directoryEntry({ peer_id: OPERATOR, name: 'operator', kind: 'operator' }),
    directoryEntry({ peer_id: bot, name: 'new bot', kind: 'pagchat-bot', created_by: 'prn_01JD3Q2K8M4N6P8R0T2V4X6Z8A' }),
  ]);
  const d = launch(h.clock.now()); d.message.sender = bot;
  const jobId = d.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', d);
  await until(() => h.statuses(jobId).some(s => s.state === 'running'), 'bot launch running');
  assert.equal(h.peersCalls() - before, 1);
  assert.equal(h.inspect(l => l.job(jobId)!.requester), bot);
  // A known sender does not refresh again.
  await launched(h);
  assert.equal(h.peersCalls() - before, 1);
});

test('requester job_status cancelled → lane.cancel → cancelled', async t => {
  const h = await harness(t);
  const { jobId, thread, d } = await launched(h);
  const cancel = message(thread, REQUESTER, 'cancel');
  cancel.message.kind = 'job_status';
  cancel.message.in_reply_to = d.message.message_id;
  cancel.message.job_status = { job_id: jobId, sequence: 1, state: 'cancelled' };
  await h.push('delivery', cancel);
  await until(() => h.statuses(jobId).some(s => s.state === 'cancelled'), 'cancelled job_status');
  const final = h.statuses(jobId).at(-1)!;
  assert.equal(final.state, 'cancelled'); assert.equal(final.sequence, 3);
  assert.equal((final.output as Record<string, unknown>).workspace_kept, true);
  assert.equal((final.output as Record<string, unknown>).branch, `launch/${jobId}`);
  const session = h.backend.sessions()[0];
  assert.ok(session.interrupts >= 1); assert.equal(session.settles, 1);
  assert.equal(h.inspect(l => l.job(jobId)!.terminal), 'cancelled');
  // A cancel from anyone else is not a cancel.
  const other = await launched(h);
  const spoof = message(other.thread, STRANGER, 'cancel');
  spoof.message.kind = 'job_status';
  spoof.message.job_status = { job_id: other.jobId, sequence: 1, state: 'cancelled' };
  await h.push('delivery', spoof);
  await until(() => h.receipts().some(r => r.delivery_id === spoof.delivery_id), 'receipt for a spoofed cancel');
  assert.deepEqual(h.receipts().find(r => r.delivery_id === spoof.delivery_id), { delivery_id: spoof.delivery_id, state: 'processed', note: 'not_relayed' });
  assert.equal(h.inspect(l => l.job(other.jobId)!.state), 'running');
});

test('message in an unknown thread → processed note not_a_job_thread, no reply', async t => {
  const h = await harness(t);
  const d = message('thr_01JD3Q5G9Z1B3D5F7H9K1M3P5Z', REQUESTER, 'hello?');
  await h.push('delivery', d);
  await until(() => h.receipts().length > 0, 'receipt');
  await settle();
  assert.deepEqual(h.receipts(), [{ delivery_id: d.delivery_id, state: 'processed', note: 'not_a_job_thread' }]);
  assert.equal(h.sends().length, 0);
  assert.equal(h.inspect(l => l.queued(10).length), 0);
});

test('outbox 422 expired on a job-thread send settles that session as failed expired', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  h.hub.failNext('/v1/messages', 422, { error: { code: 'expired', message: 'expired', details: {} } });
  h.backend.finishTurn(jobId, 'turn text');
  await until(() => h.statuses(jobId).some(s => s.state === 'failed'), 'failed expired job_status');
  assert.deepEqual(h.statuses(jobId).at(-1)!.output, { error: 'expired' });
  assert.equal(h.backend.sessions()[0].settles, 1);
  h.inspect(l => { assert.equal(l.job(jobId)!.terminal, 'failed'); assert.equal(l.counts().open, 0); });
  // The refused turn reply was the only job-thread send that did not land.
  assert.equal(h.replies(thread).length, 1);
});

test('directory_updated removing the requester → interrupt, note, cancelled requester_retired after 10 min', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  h.hub.setPeers([
    directoryEntry({ peer_id: LAUNCHER, name: 'launcher-test', kind: 'launcher' }),
    directoryEntry({ peer_id: OPERATOR, name: 'operator', kind: 'operator' }),
  ]);
  await h.push('directory_updated', { change: 'removed', peer_id: REQUESTER });
  await until(() => h.backend.sessions()[0].interrupts === 1, 'interrupt');
  await until(() => h.replies(thread).some(r => /retired/.test(r.content.text)), 'retirement note');
  const note = h.replies(thread).find(r => /retired/.test(r.content.text))!;
  assert.match(note.idempotency_key, new RegExp(`^vl\\.nt\\.${jobId}\\.requester_retired\\.1$`));
  h.clock.advance(9 * 60_000);
  await settle();
  assert.equal(h.statuses(jobId).some(s => s.state === 'cancelled'), false);
  h.clock.advance(60_000);
  await until(() => h.statuses(jobId).some(s => s.state === 'cancelled'), 'cancelled requester_retired');
  assert.deepEqual(h.statuses(jobId).at(-1)!.output, { reason: 'requester_retired' });
  assert.equal(h.backend.sessions()[0].settles, 1);
});

test('a requester retired while the daemon was down → after restart: interrupt, note, cancelled requester_retired after 10 min', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  h.hub.setPeers([
    directoryEntry({ peer_id: LAUNCHER, name: 'launcher-test', kind: 'launcher' }),
    directoryEntry({ peer_id: OPERATOR, name: 'operator', kind: 'operator' }),
  ]);
  await h.restart();
  await until(() => h.backend.sessions()[0].interrupts === 1, 'interrupt');
  await until(() => h.replies(thread).some(r => /retired/.test(r.content.text)), 'retirement note');
  h.clock.advance(10 * 60_000);
  await until(() => h.statuses(jobId).some(s => s.state === 'cancelled'), 'cancelled requester_retired');
  assert.deepEqual(h.statuses(jobId).at(-1)!.output, { reason: 'requester_retired' });
  assert.equal(h.replies(thread).filter(r => /retired/.test(r.content.text)).length, 1);
});

test('requester message relays to the agent; a stranger gets not_relayed', async t => {
  const h = await harness(t);
  const { jobId, thread } = await launched(h);
  h.backend.finishTurn(jobId, 'first');
  await until(() => h.replies(thread).length === 1, 'first turn reply');
  const stranger = message(thread, STRANGER, 'do something else');
  await h.push('delivery', stranger);
  await until(() => h.receipts().some(r => r.delivery_id === stranger.delivery_id), 'stranger receipt');
  assert.deepEqual(h.receipts().find(r => r.delivery_id === stranger.delivery_id), { delivery_id: stranger.delivery_id, state: 'processed', note: 'not_relayed' });
  const relayed = message(thread, REQUESTER, 'please continue');
  await h.push('delivery', relayed);
  await until(() => h.backend.sessions()[0].sent.length === 1, 'relayed input');
  assert.match(h.backend.sessions()[0].sent[0].items[0].text, /please continue/);
  assert.equal(h.backend.sessions()[0].sent.some(s => s.items.some(i => /something else/.test(i.text))), false);
});

test('an unknown launch-input key holding a synthetic credential never reaches the failed report', async t => {
  const h = await harness(t);
  const secret = 'vn_peer_' + 'A'.repeat(43);
  const d = launch(h.clock.now(), { [secret]: true });
  const jobId = d.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', d);
  await until(() => h.statuses(jobId).length > 0, 'failed job_status');
  const sent = h.sends().filter(s => s.job_status?.job_id === jobId);
  assert.deepEqual(sent.map(s => s.job_status!.output), [{ error: 'invalid_input', reason: '/*: unknown key' }]);
  assert.ok(!JSON.stringify(sent).includes(secret) && !JSON.stringify(sent).includes('A'.repeat(20)));
});

test('two default requesters each get the default per-requester session cap', async t => {
  const h = await harness(t, { cfg: { policies: { default: { backends: ['fake'], permission_modes: ['ask'], projects: ['repo'], max_sessions: 1, max_open_sessions: 1, launches_per_hour: 1 } } } });
  const first = await launched(h);
  const other = launch(h.clock.now()); other.message.sender = STRANGER;
  const otherId = other.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', other);
  await until(() => h.statuses(otherId).some(s => s.state === 'running' || s.state === 'failed'), 'second requester result');
  assert.deepEqual(h.statuses(otherId).map(s => s.state), ['accepted', 'running']);
  h.inspect(l => { assert.equal(l.job(first.jobId)!.policyKey, REQUESTER); assert.equal(l.job(otherId)!.policyKey, STRANGER); });
  const again = launch(h.clock.now());
  const againId = again.message.metadata['vecnet.job_id'] as string;
  await h.push('delivery', again);
  await until(() => h.statuses(againId).length > 0, 'refusal');
  assert.equal(h.statuses(againId)[0].state, 'failed');
  assert.equal((h.statuses(againId)[0].output as { error: string }).error, 'cap_reached');
});

test('the launch Delivery is turn one\'s input: processed with turn one, listed in its launch.consumed; failed if the session ends first', async t => {
  const h = await harness(t);
  const { d, jobId, thread } = await launched(h);
  await settle(20);
  assert.equal(h.receipts().some(r => r.delivery_id === d.delivery_id), false, 'not processed before the first turn finishes');
  h.backend.finishTurn(jobId, 'turn one');
  await until(() => h.replies(thread).length === 1 && h.receipts().some(r => r.delivery_id === d.delivery_id), 'turn one reply and receipt');
  assert.deepEqual(h.replies(thread)[0].metadata!['launch.consumed'], [d.message.message_id]);
  assert.deepEqual(h.receipts().filter(r => r.delivery_id === d.delivery_id), [{ delivery_id: d.delivery_id, state: 'processed' }]);

  const lost = await launched(h);
  h.backend.exitProcess(lost.jobId, false);
  await until(() => h.statuses(lost.jobId).some(s => s.state === 'failed'), 'session_exited');
  await until(() => h.receipts().some(r => r.delivery_id === lost.d.delivery_id), 'launch delivery receipt');
  assert.deepEqual(h.receipts().filter(r => r.delivery_id === lost.d.delivery_id), [{ delivery_id: lost.d.delivery_id, state: 'failed', note: 'session_ended' }]);
});
