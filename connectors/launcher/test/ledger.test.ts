import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { delivery, fakeClock } from '../../hub-client/testing/fixtures.ts';
import { openLedger } from '../src/ledger.ts';
import type { ApprovalRow, QuestionRow, JobRow } from '../src/ledger.ts';

function jobRow(jobId = 'job_one', patch: Partial<JobRow> = {}) {
  return {
    jobId, threadId: `thr_${jobId}`, launchMessageId: `msg_${jobId}`, requester: 'peer_requester', policyKey: 'prn_owner',
    backend: 't3', preset: 'default', permissionMode: 'ask' as const, project: 'repo', title: 'A job',
    input: { backend: 't3', preset: 'default', project: 'repo', permission_mode: 'ask' as const },
    tag: `vl-${jobId}`, state: 'accepted' as const, sessionRef: null, sessionKind: null, turnCursor: null,
    branch: null, workspace: null, lastActivityAt: 100, parkedAt: null, deadlineAt: 100000, createdAt: 100, ...patch,
  };
}
function disk() {
  const dir = mkdtempSync(join(tmpdir(), 'vl-ledger-'));
  return { path: join(dir, 'ledger.db'), cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

test('admit dedupes on delivery_id and survives reopen', t => {
  const d = disk();
  const clock = fakeClock(100);
  let ledger = openLedger(d.path, clock); t.after(() => { ledger.close(); d.cleanup(); });
  const row = { deliveryId: 'dly_one', cursor: 'e1.1', threadId: 'thr_one', messageId: 'msg_one', sender: 'peer_one', delivery: delivery(), receivedAt: clock.now() };
  assert.equal(ledger.admit(row), true);
  ledger.setCursor('e1.1'); ledger.close(); ledger = openLedger(d.path, clock);
  assert.equal(ledger.admit(row), false);
  assert.equal(ledger.cursor(), 'e1.1');
  assert.deepEqual(ledger.queued(1), [{ ...row, state: 'queued', note: null }]);
  ledger.finishIngress(row.deliveryId, 'done', 'processed');
  assert.deepEqual(ledger.queued(1), []);
});

test('createJob is unique on job_id; jobByThread and jobByEscalationThread find it', t => {
  const l = openLedger(':memory:', fakeClock(100)); t.after(() => l.close());
  assert.equal(l.createJob(jobRow()), true);
  assert.equal(l.createJob(jobRow()), false);
  l.setEscalationThread('job_one', 'thr_operator');
  assert.equal(l.escalationThread('job_one'), 'thr_operator');
  assert.deepEqual(l.jobByThread('thr_job_one'), l.job('job_one'));
  assert.deepEqual(l.jobByEscalationThread('thr_operator'), l.job('job_one'));
  assert.equal(l.jobByThread('absent'), undefined);
  assert.equal(l.jobByEscalationThread('absent'), undefined);
});

test('counts: parked jobs count toward open, not unparked; byKey aggregates a principal key', t => {
  const l = openLedger(':memory:', fakeClock(100)); t.after(() => l.close());
  l.createJob(jobRow('a', { sessionKind: 'parked' })); l.createJob(jobRow('b', { sessionKind: 'idle' }));
  l.createJob(jobRow('c', { policyKey: 'peer_other' })); l.createJob(jobRow('d', { state: 'ended' }));
  assert.deepEqual(l.counts(), { unparked: 2, open: 3, byKey: { prn_owner: { unparked: 1, open: 2 }, peer_other: { unparked: 1, open: 1 } } });
  assert.equal(l.jobsByRequester('peer_requester').length, 4);
  assert.equal(l.allJobRefs().length, 4);
});

test('recordTurn is once per (job, turn); nextCounter increments per (job, purpose)', t => {
  const l = openLedger(':memory:', fakeClock()); t.after(() => l.close());
  assert.equal(l.recordTurn('a', 'turn1', 'done'), true); assert.equal(l.recordTurn('a', 'turn1', 'done'), false);
  assert.equal(l.recordTurn('b', 'turn1', 'done'), true); assert.equal(l.recordTurn('a', 'turn2', 'done'), true);
  l.tx(() => {
    assert.equal(l.nextCounter('a', 'note'), 1); assert.equal(l.nextCounter('a', 'note'), 2);
    assert.equal(l.nextCounter('a', 'other'), 1); assert.equal(l.nextCounter('b', 'note'), 1);
  });
});

test('tx rolls back on throw; nested tx is a savepoint', t => {
  const l = openLedger(':memory:', fakeClock()); t.after(() => l.close());
  assert.throws(() => l.tx(() => { l.createJob(jobRow()); l.setCursor('e1.1'); throw new Error('rollback'); }), /rollback/);
  assert.equal(l.job('job_one'), undefined); assert.equal(l.cursor(), null);
  l.tx(() => {
    l.createJob(jobRow());
    assert.throws(() => l.tx(() => { l.setCursor('bad'); l.nextStatusSeq('job_one'); throw new Error('inner'); }), /inner/);
    assert.equal(l.cursor(), null); assert.equal(l.job('job_one')?.statusSeq, 0); l.setCursor('good');
  });
  assert.equal(l.cursor(), 'good'); assert.ok(l.job('job_one'));
  assert.throws(() => l.tx(() => { l.tx(() => l.setCursor('nested')); throw new Error('outer'); }), /outer/);
  assert.equal(l.cursor(), 'good');
});

test('enqueue is idempotent on key', t => {
  const l = openLedger(':memory:', fakeClock(100)); t.after(() => l.close());
  const payload = { delivery_id: 'dly_one', state: 'processed' as const };
  const id = l.enqueue('receipt', 'vl.rc.one.processed', payload, 'receipt');
  assert.equal(l.enqueue('receipt', 'vl.rc.one.processed', { ...payload, note: 'changed' }, 'changed'), id);
  assert.equal(l.pendingOutbox(10, 100).length, 1);
  assert.deepEqual(l.pendingOutbox(10, 100)[0].payload, payload);
  l.markOutbox(id, 'sent'); assert.equal(l.enqueue('receipt', 'vl.rc.one.processed', payload, 'receipt'), id);
  assert.deepEqual(l.pendingOutbox(10, 100), []);
});

test('inputs, approvals, questions, references, counters, rate history and namespaced kv survive reopen', t => {
  const d = disk(); const clock = fakeClock(100);
  let l = openLedger(d.path, clock); t.after(() => { l.close(); d.cleanup(); }); l.createJob(jobRow());
  const ref = {backend: 't3', host: 'host', id: 'session', extra: {tag: 'vl-job_one'}};
  l.updateJob('job_one', {sessionRef: ref, reattachPending: true, workspace: '/workspace', turnCursor: 'turn-cursor'});
  const input = {deliveryId: 'dly_input', jobId: 'job_one', messageId: 'msg_input', sender: 'peer_one', text: 'queued text', priority: 'wake_idle' as const, createdAt: 100};
  assert.equal(l.addInput(input), true); assert.equal(l.addInput(input), false);
  l.markInputs(['dly_input'], {state: 'sending', mode: 'queue', batchId: '0123456789abcdef01234567', turnSeq: 7});
  const approval: ApprovalRow = {
    approvalId: 'apr_one', jobId: 'job_one', route: 'requester', requestMessageId: null, threadId: null,
    addressee: 'peer_one', backendRequestId: 'backend_request', options: [{label: 'Deny', optionId: 'deny', grants: 'deny'}],
    defaultOnExpiry: 'deny', permission: JSON.parse(readFileSync(new URL('./fixtures/permission/valid/exec.json', import.meta.url), 'utf8')),
    state: 'open', decidedBy: null, decidedVia: null, decision: null, escalatedTo: null, createdAt: 100, expiresAt: 1000, decidedAt: null,
  };
  const question: QuestionRow = {questionId: 'q_one', jobId: 'job_one', messageId: null, blocking: true,
    questions: [{id: 'q1', text: 'Which?', allowOther: true, secret: false}], state: 'open', answeredBy: null, createdAt: 100, expiresAt: 1000};
  l.putApproval(approval); l.updateApproval('apr_one', {requestMessageId: 'msg_approval', threadId: 'thr_approval'});
  l.putQuestion(question); l.updateQuestion('q_one', {messageId: 'msg_question'});
  l.setEscalationThread('job_one', 'thr_escalation');
  l.noteLaunch('prn_owner', 99); l.noteLaunch('prn_owner', 100); l.noteLaunch('other', 100);
  l.kv('one').set('a_%', 'literal'); l.kv('one').set('abc', 'other'); l.kv('unicode').set('😀one', 'unicode'); l.kv('two').set('a_%', 'separate');
  l.tx(() => { assert.equal(l.nextStatusSeq('job_one'), 1); assert.equal(l.nextCounter('job_one', 'note'), 1); l.recordTurn('job_one', 'turn_one', 'completed'); });
  l.close(); l = openLedger(d.path, clock);
  assert.deepEqual(l.job('job_one')?.sessionRef, ref); assert.equal(l.job('job_one')?.reattachPending, true);
  assert.equal(l.job('job_one')?.turnCursor, 'turn-cursor'); assert.equal(l.jobByEscalationThread('thr_escalation')?.jobId, 'job_one');
  assert.deepEqual(l.allJobRefs(), [{jobId: 'job_one', backend: 't3', ref, tag: 'vl-job_one', state: 'accepted', endedAt: null, workspace: '/workspace'}]);
  assert.deepEqual(l.inputs('job_one', ['sending']), [{...input, state: 'sending', mode: 'queue', batchId: '0123456789abcdef01234567', turnSeq: 7}]);
  assert.deepEqual(l.inputs('other', ['sending']), []); assert.deepEqual(l.inputs('job_one', []), []);
  assert.deepEqual(l.approvalByRequestMessage('msg_approval'), {...approval, requestMessageId: 'msg_approval', threadId: 'thr_approval'});
  assert.deepEqual(l.openApprovals('job_one'), [l.approval('apr_one')]); assert.equal(l.openApprovals().length, 1); assert.deepEqual(l.openApprovals('other'), []);
  assert.deepEqual(l.questionByMessage('msg_question'), {...question, messageId: 'msg_question'});
  assert.deepEqual(l.openQuestions('job_one'), [l.question('q_one')]); assert.equal(l.openQuestions().length, 1); assert.deepEqual(l.openQuestions('other'), []);
  l.updateApproval('apr_one', {state: 'decided', decision: 'deny', decidedBy: 'peer_one', decidedAt: 200});
  l.updateQuestion('q_one', {state: 'answered', answeredBy: 'peer_one'});
  assert.deepEqual(l.openApprovals(), []); assert.deepEqual(l.openQuestions(), []);
  assert.equal(l.launchesSince('prn_owner', 100), 1); assert.equal(l.launchesSince('prn_owner', 99), 2);
  assert.equal(l.launchesSince('other', 100), 1); assert.equal(l.launchesSince('other', 101), 0);
  assert.equal(l.kv('one').get('a_%'), 'literal'); assert.equal(l.kv('two').get('a_%'), 'separate');
  assert.deepEqual(l.kv('one').keys('a_%'), ['a_%']); l.kv('one').delete('abc'); assert.deepEqual(l.kv('one').keys(), ['a_%']);
  assert.deepEqual(l.kv('unicode').keys('😀'), ['😀one']);
  assert.equal(l.kv('one').get('abc'), undefined); assert.equal(l.nextStatusSeq('job_one'), 2); assert.equal(l.nextCounter('job_one', 'note'), 2);
  assert.equal(l.recordTurn('job_one', 'turn_one', 'completed'), false);
  clock.advance(25); l.updateJob('job_one', {state: 'running'}); assert.equal(l.job('job_one')?.updatedAt, 125);
});

test('turn dedupe, counters and outbox roll back together', t => {
  const l = openLedger(':memory:', fakeClock()); t.after(() => l.close()); l.createJob(jobRow());
  assert.throws(() => l.tx(() => {
    l.recordTurn('job_one', 'turn_one', 'completed'); l.nextCounter('job_one', 'note'); l.nextStatusSeq('job_one');
    l.enqueue('receipt', 'receipt', {delivery_id: 'dly_one', state: 'processed'}, 'receipt');
    throw new Error('rollback');
  }), /rollback/);
  assert.equal(l.recordTurn('job_one', 'turn_one', 'completed'), true); assert.equal(l.nextCounter('job_one', 'note'), 1);
  assert.equal(l.nextStatusSeq('job_one'), 1); assert.deepEqual(l.pendingOutbox(10, 0), []);
});
