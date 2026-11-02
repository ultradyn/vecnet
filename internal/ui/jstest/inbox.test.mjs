import test from 'node:test';
import assert from 'node:assert/strict';
import { chainBreak, deadLetterLine, follow, heldWhy, sections, withOlder } from '../static/inbox-model.js';

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

// A hub that pages like opview/inbox.go: newest first, before is exclusive,
// and next_before names the last row of a full page.
function fakeHub(n) {
  const id = i => 'dly_' + String(i).padStart(3, '0');
  let top = n;
  const all = () => Array.from({ length: top }, (_, i) => id(top - i));
  return {
    all,
    deliver: (count = 1) => { top += count; },
    page: (limit, before) => {
      let rows = all();
      if (before) rows = rows.slice(rows.indexOf(before) + 1);
      const more = rows.length > limit;
      rows = rows.slice(0, limit);
      return { approvals: [], dead_letters: [], attention: [], deliveries: rows.map(r => delivery(r, 'held', 'hold')), next_before: more ? rows[limit - 1] : null };
    },
  };
}
const ids = inbox => inbox.deliveries.map(d => d.delivery_id);
// Whatever the page shows must be the newest rows with none skipped.
const assertNoGap = (hub, inbox) => assert.deepEqual(ids(inbox), hub.all().slice(0, inbox.deliveries.length));

test('older pages follow the head as new deliveries shift it, and no row goes missing', async () => {
  const hub = fakeHub(100);
  const head = hub.page(50);
  assert.equal(withOlder(head, []), head);
  const pages = [{ ...hub.page(50, head.next_before), before: head.next_before }];
  assert.equal(chainBreak(head, pages), -1);
  const merged = withOlder(head, pages);
  assert.equal(merged.deliveries.length, 100);
  assertNoGap(hub, merged);
  assert.equal(merged.next_before, null);

  // One delivery arrives and the head reloads: dly_051 dropped off the head
  // and is not in the older page, which starts below it.
  hub.deliver();
  const head2 = hub.page(50);
  assert.equal(chainBreak(head2, pages), 0);
  // Until the pages follow, only the head shows, paging on from its tail.
  const interim = withOlder(head2, pages);
  assertNoGap(hub, interim);
  assert.equal(interim.next_before, head2.next_before);

  const reads = [];
  const fetchPage = before => { reads.push(before); return hub.page(50, before); };
  const { pages: followed, error } = await follow(head2, pages, fetchPage);
  assert.equal(error, null);
  assert.deepEqual(reads, ['dly_052']);
  const after = withOlder(head2, followed);
  assertNoGap(hub, after);
  assert.ok(ids(after).includes('dly_051'), 'dly_051 went missing');
  assert.equal(after.deliveries.length, 100);
  assert.equal(after.next_before, 'dly_002');

  // A burst longer than a page, with two older pages loaded.
  const deep = [...followed, { ...hub.page(50, 'dly_002'), before: 'dly_002' }];
  hub.deliver(60);
  const head3 = hub.page(50);
  const again = await follow(head3, deep, before => hub.page(50, before));
  assert.equal(again.pages.length, 2);
  assert.equal(chainBreak(head3, again.pages), -1);
  const shown = withOlder(head3, again.pages);
  assertNoGap(hub, shown);
  assert.equal(shown.deliveries.length, 150);

  // Pages that still follow are kept without a read.
  const steady = await follow(head3, again.pages, () => { throw new Error('read a page that still follows'); });
  assert.equal(steady.pages, again.pages);
});

test('follow keeps the pages that joined when a read fails', async () => {
  const hub = fakeHub(200);
  const head = hub.page(50);
  const p1 = { ...hub.page(50, head.next_before), before: head.next_before };
  const p2 = { ...hub.page(50, p1.next_before), before: p1.next_before };
  hub.deliver();
  const head2 = hub.page(50);
  let n = 0;
  const failing = before => { if (n++) throw Object.assign(new Error('hub down'), { code: 'unavailable' }); return hub.page(50, before); };
  const { pages, error } = await follow(head2, [p1, p2], failing);
  assert.equal(error.code, 'unavailable');
  assert.equal(pages.length, 1);
  const shown = withOlder(head2, pages);
  assertNoGap(hub, shown);
  assert.equal(shown.next_before, pages[0].next_before);
  // A page that does not follow is never merged, whatever it holds.
  assert.equal(withOlder(head2, [p1, p2]).deliveries.length, 50);
  assert.equal(withOlder(null, [p1]), null);
});

test('withOlder keeps the first copy of a row two pages share', () => {
  const head = { approvals: [], dead_letters: [], attention: [], deliveries: [delivery('dly_9', 'woke', 'wake'), delivery('dly_8', 'held', 'hold', { read_at: '2026-11-02T01:10:00.000Z' })], next_before: 'dly_8' };
  const page = { before: 'dly_8', deliveries: [delivery('dly_8', 'held', 'hold'), delivery('dly_7', 'woke', 'wake')], next_before: null };
  const merged = withOlder(head, [page]);
  assert.deepEqual(ids(merged), ['dly_9', 'dly_8', 'dly_7']);
  assert.equal(merged.deliveries[1].read_at, '2026-11-02T01:10:00.000Z');
  assert.equal(merged.next_before, null);
});
