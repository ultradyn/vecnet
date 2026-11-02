import test from 'node:test';
import assert from 'node:assert/strict';
import { deadLetterLine, heldWhy, sections, withOlder } from '../static/inbox-model.js';

const delivery = (id, section, priority, extra = {}) => ({
  delivery_id: id, message_id: 'msg_' + id, thread_id: 'thr_1', thread_title: 't', sender: 'peer_a',
  kind: 'message', priority, section, state: 'accepted', created_at: '2026-11-02T01:00:00.000Z',
  excerpt: 'x', read_at: null, attachments: 0, ...extra,
});
const approval = id => ({ approval_id: id, thread_id: 'thr_1', message_id: 'msg_' + id, options: ['yes', 'no'], expires_at: '2026-11-02T02:00:00.000Z' });
const dead = id => ({ delivery_id: id, message_id: 'msg_' + id, thread_id: 'thr_2', recipient: 'peer_b', sender: 'peer_c', excerpt: 'e', class: 'terminal', updated_at: '2026-11-02T01:00:00.000Z', attempt: 1, last_status: 410, last_error: 'gone' });
const attention = id => ({ kind: 'subscription', id, peer_id: 'peer_d', health: 'paused_failed', summary: 's', action: 'resume', at: '2026-11-02T01:00:00.000Z' });

test('sections keeps order and hides empty ones; approvals first', () => {
  const full = {
    now: '2026-11-02T01:30:00.000Z',
    approvals: [approval('apr_2'), approval('apr_1')],
    dead_letters: [dead('dly_d1'), dead('dly_d2'), dead('dly_d3')],
    attention: [attention('sub_1')],
    // Interleaved on the wire (newest first); each section keeps that order.
    deliveries: [delivery('dly_5', 'held', 'hold'), delivery('dly_4', 'woke', 'wake'), delivery('dly_3', 'held', 'wake_idle'), delivery('dly_2', 'woke', 'wake')],
    unread: 4, next_before: null,
  };
  const all = sections(full);
  assert.deepEqual(all.map(s => s.key), ['approvals', 'dead_letters', 'attention', 'woke', 'held']);
  assert.deepEqual(all.map(s => s.title), ['Needs a decision', 'Failed deliveries', 'Subscriptions', 'Woke you', 'Held']);
  assert.deepEqual(all.map(s => s.count), [2, 3, 1, 2, 2]);
  assert.deepEqual(all.map(s => s.label), ['2 open', '3 dead letters', '1', '2', '2']);
  // Items are the wire objects themselves, in wire order.
  assert.deepEqual(all[0].items.map(a => a.approval_id), ['apr_2', 'apr_1']);
  assert.deepEqual(all[1].items.map(d => d.delivery_id), ['dly_d1', 'dly_d2', 'dly_d3']);
  assert.equal(all[2].items[0], full.attention[0]);
  assert.deepEqual(all[3].items.map(d => d.delivery_id), ['dly_4', 'dly_2']);
  assert.deepEqual(all[4].items.map(d => d.delivery_id), ['dly_5', 'dly_3']);

  // Empty sections are hidden; the order of the rest does not change.
  const sparse = { ...full, approvals: [], attention: [], deliveries: [delivery('dly_9', 'held', 'hold')] };
  assert.deepEqual(sections(sparse).map(s => s.key), ['dead_letters', 'held']);
  assert.equal(sections({ ...full, dead_letters: [dead('dly_d1')] })[1].label, '1 dead letter');
  // Approvals come first even when they are the only thing left.
  assert.deepEqual(sections({ ...full, dead_letters: [], attention: [], deliveries: [] }).map(s => s.key), ['approvals']);

  // Nothing at all, or no snapshot yet, is no sections.
  assert.deepEqual(sections({ approvals: [], dead_letters: [], attention: [], deliveries: [] }), []);
  assert.deepEqual(sections(null), []);
  assert.deepEqual(sections(undefined), []);
});

test('deadLetterLine covers 503, 410 and null status', () => {
  // Local 04:11 on the same local day as the clock: the time alone, 24-hour.
  const nowMs = new Date(2026, 10, 2, 12, 0, 0).getTime();
  const at = new Date(2026, 10, 2, 4, 11, 47).toISOString();
  const base = { message_id: 'msg_1', thread_id: 'thr_1', recipient: 'peer_t', sender: 'peer_d', excerpt: 'x', updated_at: at };

  assert.equal(
    deadLetterLine({ ...base, delivery_id: 'dly_01JD3SB4T6W8Y0A2C4E6G8J0M2', class: 'retries_exhausted', attempt: 8, last_status: 503, last_error: 'HTTP 503' }, nowMs),
    '503 · retries exhausted · attempts 8/8 · 04:11 · dly_…8J0M2');
  assert.equal(
    deadLetterLine({ ...base, delivery_id: 'dly_01JD3SC5V7X9Z1B3D5F7H9K1M3', class: 'terminal', attempt: 1, last_status: 410, last_error: 'Gone' }, nowMs),
    '410 · terminal 4xx · attempts 1/8 · 04:11 · dly_…9K1M3');
  // No HTTP status: the network class speaks for it, and no "null" leaks out.
  const network = deadLetterLine({ ...base, delivery_id: 'dly_01JD3S5H9K1M3P5R7T9W1Y3A5C', class: 'network', attempt: 8, last_status: null, last_error: 'connection refused' }, nowMs);
  assert.equal(network, 'network error · attempts 8/8 · 04:11 · dly_…Y3A5C');
  assert.doesNotMatch(network, /null|undefined/);

  // An operator retry continues the attempt counter with a fresh budget of 8
  // (PROTOCOL §5.5), so the denominator grows with it.
  assert.match(deadLetterLine({ ...base, delivery_id: 'dly_x', class: 'retries_exhausted', attempt: 16, last_status: 503 }, nowMs), / · attempts 16\/16 · /);
  // On another day the date leads the time.
  const older = new Date(2026, 9, 31, 22, 5, 0).toISOString();
  assert.match(deadLetterLine({ ...base, updated_at: older, delivery_id: 'dly_x', class: 'terminal', attempt: 1, last_status: 404 }, nowMs), / · 31 Oct 22:05 · dly_x$/);
});

test('heldWhy explains hold vs wake_idle', () => {
  const hold = heldWhy(delivery('dly_1', 'held', 'hold'));
  const idle = heldWhy(delivery('dly_2', 'held', 'wake_idle'));
  assert.match(hold, /\bhold\b/);
  assert.match(hold, /never pushed/);
  assert.match(idle, /\bwake_idle\b/);
  // PROTOCOL §5.2: for the operator peer, wake_idle is inbox only.
  assert.match(idle, /never notifies the operator/);
  assert.notEqual(hold, idle);
  // Only held deliveries need a reason.
  assert.equal(heldWhy(delivery('dly_3', 'woke', 'wake')), '');
  assert.equal(heldWhy(null), '');
});

test('withOlder appends older pages once and pages on from the last one', () => {
  const head = { approvals: [], dead_letters: [], attention: [], deliveries: [delivery('dly_9', 'woke', 'wake'), delivery('dly_8', 'held', 'hold')], next_before: 'dly_8' };
  assert.equal(withOlder(head, []), head);
  // The head reloaded with a newer row since the older page was read, so the
  // older page repeats dly_8; the head's copy wins.
  const fresh = { ...head, deliveries: [delivery('dly_10', 'woke', 'wake'), delivery('dly_9', 'woke', 'wake'), delivery('dly_8', 'held', 'hold', { read_at: '2026-11-02T01:10:00.000Z' })], next_before: 'dly_8' };
  const page = { deliveries: [delivery('dly_8', 'held', 'hold'), delivery('dly_7', 'woke', 'wake')], next_before: 'dly_7' };
  const merged = withOlder(fresh, [page]);
  assert.deepEqual(merged.deliveries.map(d => d.delivery_id), ['dly_10', 'dly_9', 'dly_8', 'dly_7']);
  assert.equal(merged.deliveries[2].read_at, '2026-11-02T01:10:00.000Z');
  assert.equal(merged.next_before, 'dly_7');
  assert.equal(withOlder(fresh, [page, { deliveries: [], next_before: null }]).next_before, null);
  assert.equal(withOlder(null, [page]), null);
});
