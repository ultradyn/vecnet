import test from 'node:test';
import assert from 'node:assert/strict';
import { answerBody, countdown } from '../static/approvals.js';

const at = iso => Date.parse(iso);
const hub = at('2026-11-02T10:00:00.000Z');
const plus = ms => new Date(hub + ms).toISOString();

test('countdown mm:ss, hours form, urgent under 60 s, expired at 0, independent of client clock', t => {
  assert.deepEqual(countdown(plus(11 * 60_000 + 37_000), hub), { text: '11:37', urgent: false, expired: false });
  assert.deepEqual(countdown(plus(2 * 3_600_000 + 5 * 60_000 + 9_000), hub), { text: '2:05:09', urgent: false, expired: false });
  assert.deepEqual(countdown(plus(60_000), hub), { text: '01:00', urgent: false, expired: false });
  assert.deepEqual(countdown(plus(59_000), hub), { text: '00:59', urgent: true, expired: false });
  assert.deepEqual(countdown(plus(1), hub), { text: '00:01', urgent: true, expired: false });
  assert.deepEqual(countdown(plus(0), hub), { text: '00:00', urgent: true, expired: true });
  assert.deepEqual(countdown(plus(-90_000), hub), { text: '00:00', urgent: true, expired: true });

  // A client clock hours away changes nothing: only the two arguments count.
  t.mock.timers.enable({ apis: ['Date'], now: hub + 6 * 3_600_000 });
  assert.deepEqual(countdown(plus(30_000), hub), { text: '00:30', urgent: true, expired: false });
  t.mock.timers.reset();
});

test('countdown treats an unreadable expiry as expired, never as open', () => {
  assert.equal(countdown('whenever', hub).expired, true);
  assert.equal(countdown(undefined, hub).expired, true);
});

test('answerBody is deterministic per approval and option and matches the idempotency key pattern', () => {
  const approval = { approval_id: 'apr_01JD3Q', thread_id: 'thr_1', message_id: 'msg_1', options: ['yes', 'no', 'yes but no release'], expires_at: plus(1000) };
  const body = answerBody(approval, 2);
  assert.deepEqual(body, {
    idempotency_key: 'ui-apr-apr_01JD3Q-2',
    thread_id: 'thr_1',
    in_reply_to: 'msg_1',
    kind: 'approval_response',
    priority: 'wake',
    content: { text: 'yes but no release' },
    approval_response: { approval_id: 'apr_01JD3Q', decision: 'yes but no release' },
  });
  assert.deepEqual(answerBody(approval, 2), body);
  assert.notEqual(answerBody(approval, 0).idempotency_key, body.idempotency_key);
  // $defs/idempotencyKey in the envelope schema.
  assert.match(body.idempotency_key, /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/);
});

test('answerBody refuses an option the approval does not have', () => {
  const approval = { approval_id: 'apr_1', thread_id: 'thr_1', message_id: 'msg_1', options: ['yes'] };
  assert.throws(() => answerBody(approval, 1), RangeError);
  assert.throws(() => answerBody(approval, -1), RangeError);
});
