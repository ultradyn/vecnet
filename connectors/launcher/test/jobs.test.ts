import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeClock } from '../../hub-client/testing/fixtures.ts';
import type { SendRequest } from '../../hub-client/src/wire.ts';
import { openLedger } from '../src/ledger.ts';
import { createOutbox } from '../src/outbox.ts';
import { createJobReporter } from '../src/jobs.ts';
import { memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { JobRow } from '../src/ledger.ts';

function jobRow(jobId = 'job_one', patch: Partial<JobRow> = {}) {
  return {
    jobId, threadId: `thr_${jobId}`, launchMessageId: `msg_${jobId}`, requester: 'peer_requester', policyKey: 'prn_owner',
    backend: 't3', preset: 'default', permissionMode: 'ask' as const, project: 'repo', title: 'A job',
    input: { backend: 't3', preset: 'default', project: 'repo', permission_mode: 'ask' as const },
    tag: `vl-${jobId}`, state: 'accepted' as const, sessionRef: null, sessionKind: null, turnCursor: null,
    branch: null, workspace: null, lastActivityAt: 100, parkedAt: null, deadlineAt: 100000, createdAt: 100, ...patch,
  };
}

function setup() {
  const clock = fakeClock(100), ledger = openLedger(':memory:', clock);
  const outbox = createOutbox({ ledger, clock, log: memoryLog(), hub: { async send() { throw new Error('not running'); }, async receipt() {} } });
  return { clock, ledger, reporter: createJobReporter({ ledger, outbox }) };
}
test('job reporter: sequences 1,2,3 per job; keys vl.js.<job>.<seq>; in_reply_to the launch message; priorities hold/hold/wake_idle; output over 8 KiB truncated', t => {
  const { ledger: l, reporter, clock } = setup(); t.after(() => l.close()); l.createJob(jobRow()); l.createJob(jobRow('other'));
  l.tx(() => { reporter.report('job_one', 'accepted'); reporter.report('job_one', 'running', { ref: 'one' }, 'Started'); reporter.report('other', 'accepted'); });
  clock.advance(50);
  l.tx(() => reporter.report('job_one', 'succeeded', { summary: '😀'.repeat(3000) }));
  const rows = l.pendingOutbox(10, clock.now()).filter(r => r.tag === 'job_one');
  assert.deepEqual(rows.map(r => r.key), ['vl.js.job_one.1', 'vl.js.job_one.2', 'vl.js.job_one.3']);
  const payloads = rows.map(r => r.payload as SendRequest);
  assert.deepEqual(payloads.map(p => p.job_status?.sequence), [1, 2, 3]);
  assert.deepEqual(payloads.map(p => p.priority), ['hold', 'hold', 'wake_idle']);
  assert.deepEqual(payloads.map(p => p.in_reply_to), ['msg_job_one', 'msg_job_one', 'msg_job_one']);
  assert.ok(payloads.every(p => p.kind === 'job_status' && p.thread_id === 'thr_job_one'));
  assert.deepEqual(payloads.map(p => p.content.text), ['accepted', 'Started', 'succeeded']);
  assert.equal(payloads[1].job_status?.output && (payloads[1].job_status.output as {ref: string}).ref, 'one');
  const output = payloads[2].job_status?.output as {output_truncated: boolean; summary: string};
  assert.equal(output.output_truncated, true); assert.ok(Buffer.byteLength(output.summary) <= 1024);
  assert.ok(output.summary.startsWith('{"summary":"😀')); assert.ok(!output.summary.includes('�'));
  assert.ok(Buffer.byteLength(JSON.stringify(output)) <= 8192);
  assert.equal(l.job('job_one')?.terminal, 'succeeded'); assert.equal(l.job('job_one')?.state, 'ended');
  assert.equal(l.job('job_one')?.endedAt, 150); assert.equal(l.job('other')?.statusSeq, 1);
});
test('a second terminal report is a no-op', t => {
  const { ledger: l, reporter, clock } = setup(); t.after(() => l.close()); l.createJob(jobRow());
  l.tx(() => reporter.report('job_one', 'failed', { error: 'backend_error' }));
  clock.advance(10); l.tx(() => reporter.report('job_one', 'cancelled'));
  assert.equal(l.pendingOutbox(10, 110).length, 1); assert.equal(l.job('job_one')?.statusSeq, 1);
  assert.equal(l.job('job_one')?.terminal, 'failed'); assert.equal(l.job('job_one')?.endedAt, 100);
});
