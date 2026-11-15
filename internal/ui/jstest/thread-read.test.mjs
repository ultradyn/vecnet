import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { BATCH_MS, createReader, DWELL_MS, enough, RETRY_MS, SEEN_RATIO } from '../static/thread-read.js';
import { nextDivider, opening, unreadOf } from '../static/thread-model.js';

// The thread's read tracking under the CoS unread ruling: a message is seen
// when at least 60% of it is visible for a second or it is scrolled past
// going down; seen deliveries get read receipts in 500 ms batches, only while
// the page is visible and focused; a failed receipt is retried once, quietly.
// Opening a thread never marks the whole thread read.

const OP = 'peer_op', CODEX = 'peer_codex', SLEUTH = 'peer_sleuth';
const msg = (id, sender, extra = {}) => ({ message_id: id, thread_id: 'thr_1', sender, kind: 'message', priority: 'wake', created_at: '2026-11-06T08:00:00.000Z', recipients: [], content: { text: id }, ...extra });
const cell = (recipient, id, read) => ({ recipient, delivery_id: 'dly_' + id, state: 'accepted', display: read ? 'read' : 'accepted', read_at: read ? '2026-11-06T08:00:01.000Z' : null });
// A ten-message history whose first six the operator read on an earlier visit.
function history(readUpTo = 6, n = 10) {
  const messages = [], rows = [];
  for (let i = 1; i <= n; i++) {
    const id = 'msg_' + i, from = i % 2 ? CODEX : SLEUTH;
    messages.push(msg(id, from));
    rows.push({ message_id: id, sender: from, cells: [cell(OP, id, i <= readUpTo), cell(from === CODEX ? SLEUTH : CODEX, id + 'x', true)] });
  }
  return { messages, matrix: { recipients: [OP, CODEX, SLEUTH], rows } };
}

// A fake api: receipts resolve unless fail(delivery) says otherwise; the
// mark-all route is a spy that must never be called.
function fakeApi(fail = () => false) {
  const posts = [], markAll = [];
  return {
    posts, markAll,
    receipt: (dly, state) => { posts.push([dly, state]); return fail(dly, posts.filter(p => p[0] === dly).length) ? Promise.reject(Object.assign(new Error('hub unreachable'), { status: 0, code: 'network' })) : Promise.resolve(null); },
    readThread: id => { markAll.push(id); return Promise.resolve({ marked: 0 }); },
  };
}
// settle lets settled receipts' promise callbacks run (setImmediate is not mocked).
const settle = () => new Promise(resolve => setImmediate(resolve));
// run advances the mocked clock a millisecond at a time, settling promises
// between steps, so a timer set by another timer's callback (a dwell's batch,
// a failure's retry) runs on time. One long tick() would start such a timer
// from the end of the tick instead.
async function run(t, ms) {
  for (let done = 0; done < ms; done++) {
    t.mock.timers.tick(1);
    await settle();
  }
}
const visible = { intersecting: true, enough: true };
const sliver = { intersecting: true, enough: false };
const gone = above => ({ intersecting: false, enough: false, above });

const mocked = new WeakSet();
function setup(t, { fail, focus = true } = {}) {
  if (!mocked.has(t)) { t.mock.timers.enable({ apis: ['setTimeout'] }); mocked.add(t); }
  const api = fakeApi(fail), seenIds = [];
  const state = { active: focus };
  const lostIds = [];
  const reader = createReader({ api, active: () => state.active, onSeen: id => seenIds.push(id), onLost: id => lostIds.push(id) });
  const { messages, matrix } = history();
  const unread = unreadOf(messages, matrix, { me: OP, openApprovals: [] });
  reader.setUnread(unread);
  return { api, reader, state, seenIds, lostIds, unread, messages, matrix };
}

test('open with mid-history unreads: the view opens at the first unread under New; none unread opens at the bottom', () => {
  const { messages, matrix } = history(6);
  const unread = unreadOf(messages, matrix, { me: OP });
  assert.deepEqual(unread.map(u => u.message_id), ['msg_7', 'msg_8', 'msg_9', 'msg_10']);
  assert.deepEqual(unread[0], { message_id: 'msg_7', delivery_id: 'dly_msg_7' });
  assert.deepEqual(opening(unread), { divider: 'msg_7', at: 'msg_7' });
  // Everything read: no divider, and the thread opens at the bottom as before.
  const all = history(10);
  assert.deepEqual(unreadOf(all.messages, all.matrix, { me: OP }), []);
  assert.deepEqual(opening([]), { divider: null, at: 'bottom' });
  // Before the matrix lands nothing is known to be unread.
  assert.deepEqual(unreadOf(messages, null, { me: OP }), []);
});

test('unread counts as the inbox does: superseded, open, unanswered and my answered approvals, and my own sends are not unread', () => {
  const NOW = Date.parse('2026-11-06T08:30:00.000Z');
  const ask = (id, apr, expires = '2026-11-06T09:00:00.000Z') => msg(id, CODEX, { kind: 'approval_request', approval_request: { approval_id: apr, question: 'Ship?', options: ['yes', 'no'], expires_at: expires } });
  const answer = (id, from, apr, decision = 'yes') => msg(id, from, { kind: 'approval_response', approval_response: { approval_id: apr, decision } });
  const messages = [
    msg('msg_1', CODEX), ask('msg_2', 'apr_open'), ask('msg_3', 'apr_mine'), ask('msg_4', 'apr_theirs'),
    answer('msg_5', OP, 'apr_mine'), answer('msg_6', SLEUTH, 'apr_theirs'), msg('msg_7', CODEX),
    ask('msg_8', 'apr_lapsed', '2026-11-06T08:00:00.000Z'), answer('msg_9', 'hub', 'apr_lapsed', 'expired'),
    ask('msg_10', 'apr_due', '2026-11-06T08:29:00.000Z'), ask('msg_11', 'apr_new'), ask('msg_12', 'apr_here'),
  ];
  const rows = messages.map(m => ({ message_id: m.message_id, sender: m.sender, cells: m.sender === OP ? [cell(CODEX, m.message_id, false)] : [cell(OP, m.message_id, false)] }));
  rows[6].cells[0].state = 'superseded';
  const unread = unreadOf(messages, { rows }, { me: OP, now: NOW, openApprovals: [{ approval_id: 'apr_open' }], answered: ['apr_here'] });
  // msg_1 plain. apr_open is open (the inbox lists it apart). apr_mine I
  // answered; apr_theirs another answered: closed, so unread, and so is the
  // answer. msg_5 is mine. msg_7's delivery was withdrawn. apr_lapsed expired
  // and the hub said so; apr_due is past its expiry before the hub says so.
  // apr_new is open though a stale detail does not list it yet. apr_here was
  // just answered from this client.
  assert.deepEqual(unread.map(u => u.message_id), ['msg_1', 'msg_4', 'msg_6', 'msg_8', 'msg_9', 'msg_10']);
  // Without the hub clock an unanswered approval is never taken as expired.
  assert.deepEqual(unreadOf(messages, { rows }, { me: OP }).map(u => u.message_id), ['msg_1', 'msg_4', 'msg_6', 'msg_8', 'msg_9']);
});

test('a message 60% visible for a second is read; partial marking follows what was seen', async t => {
  const { api, reader, seenIds } = setup(t);
  reader.view('msg_7', visible);
  reader.view('msg_8', sliver);
  t.mock.timers.tick(DWELL_MS - 1);
  assert.deepEqual(seenIds, [], 'seen before a full second');
  t.mock.timers.tick(1);
  assert.deepEqual(seenIds, ['msg_7']);
  assert.ok(reader.seen('msg_7') && !reader.seen('msg_8'));
  // Batched: nothing goes out until the batch window closes.
  t.mock.timers.tick(BATCH_MS - 1);
  assert.deepEqual(api.posts, []);
  t.mock.timers.tick(1);
  await settle();
  assert.deepEqual(api.posts, [['dly_msg_7', 'read']]);
  // msg_8 was only a sliver: unread, however long it stays.
  t.mock.timers.tick(10 * DWELL_MS);
  await settle();
  assert.deepEqual(api.posts, [['dly_msg_7', 'read']]);
  assert.ok(!reader.seen('msg_8') && !reader.seen('msg_9') && !reader.seen('msg_10'));
  assert.deepEqual(api.markAll, []);
});

test('leaving before the second is up resets the dwell', async t => {
  const { api, reader } = setup(t);
  reader.view('msg_7', visible);
  t.mock.timers.tick(DWELL_MS - 100);
  reader.view('msg_7', sliver);
  t.mock.timers.tick(DWELL_MS);
  reader.view('msg_7', visible);
  t.mock.timers.tick(DWELL_MS - 1);
  assert.ok(!reader.seen('msg_7'));
  await run(t, 1 + BATCH_MS);
  assert.deepEqual(api.posts, [['dly_msg_7', 'read']]);
});

test('a tail that remains in view after the message was visible enough is read; a tail that was only a sliver is not', async t => {
  const { api, reader } = setup(t);
  // Short view, or a message with little room below it: scrolling to the end
  // leaves the top above the root and the rest under 60%. It never goes fully
  // out, so the old "left through the top" rule never fires and the dwell has
  // already been cancelled. A human did scroll past it.
  reader.view('msg_7', visible);
  reader.view('msg_7', { intersecting: true, enough: false, above: false, pastTop: true });
  assert.ok(reader.seen('msg_7'));
  // Still filling the view (top already above the root): that is the dwell, not a pass.
  reader.view('msg_8', { intersecting: true, enough: true, pastTop: true });
  t.mock.timers.tick(DWELL_MS - 1);
  assert.ok(!reader.seen('msg_8'));
  // Never enough, then only a tail: a sliver, not a read.
  reader.view('msg_9', { intersecting: true, enough: false, above: true, pastTop: true });
  t.mock.timers.tick(BATCH_MS);
  await settle();
  assert.ok(!reader.seen('msg_9'));
  assert.deepEqual(api.posts, [['dly_msg_7', 'read']]);
});

test('scrolled past going down counts as seen; leaving through the bottom, or a sliver swept off, does not', async t => {
  const { api, reader } = setup(t);
  reader.view('msg_7', visible);
  reader.view('msg_7', sliver);
  reader.view('msg_7', gone(true)); // on screen, then out through the top inside the second: scrolled past
  reader.view('msg_8', visible);
  reader.view('msg_8', gone(false)); // out through the bottom: scrolled back up
  // Never in view, then reported above (a first observation): not seen.
  reader.view('msg_9', gone(true));
  // Only ever a sliver at the edge, then jumped past (a send snaps to the bottom): not seen.
  reader.view('msg_10', sliver);
  reader.view('msg_10', gone(true));
  assert.ok(reader.seen('msg_7'));
  assert.ok(!reader.seen('msg_8') && !reader.seen('msg_9') && !reader.seen('msg_10'));
  t.mock.timers.tick(BATCH_MS);
  await settle();
  assert.deepEqual(api.posts, [['dly_msg_7', 'read']]);
});

test('receipts within 500 ms go out as one batch, each delivery once', async t => {
  const { api, reader } = setup(t);
  reader.view('msg_7', visible);
  reader.view('msg_8', visible);
  t.mock.timers.tick(DWELL_MS);
  t.mock.timers.tick(200);
  reader.view('msg_9', visible);
  reader.view('msg_9', gone(true));
  assert.deepEqual(api.posts, []);
  t.mock.timers.tick(BATCH_MS - 200);
  await settle();
  assert.deepEqual(api.posts.map(p => p[0]), ['dly_msg_7', 'dly_msg_8', 'dly_msg_9']);
  // Seen again, or a stale snapshot still calling them unread: no second receipt.
  reader.view('msg_7', sliver);
  reader.view('msg_7', visible);
  reader.setUnread(unreadOf(...Object.values(history()), { me: OP }));
  await run(t, DWELL_MS + BATCH_MS);
  assert.equal(api.posts.length, 3);
});

test('marking waits for a visible page with focus', async t => {
  const { api, reader, state } = setup(t, { focus: false });
  // Hidden or unfocused: on screen for minutes, scrolled past, nothing is read.
  reader.view('msg_7', visible);
  t.mock.timers.tick(60 * DWELL_MS);
  reader.view('msg_8', visible);
  reader.view('msg_8', gone(true));
  t.mock.timers.tick(BATCH_MS);
  await settle();
  assert.deepEqual(api.posts, []);
  // Focus returns: tracking starts over and the view re-observes; the dwell
  // starts then, not from when the message came on screen.
  state.active = true;
  reader.activeChanged();
  reader.view('msg_7', visible);
  t.mock.timers.tick(DWELL_MS - 1);
  assert.ok(!reader.seen('msg_7'));
  t.mock.timers.tick(1);
  assert.ok(reader.seen('msg_7'));
  assert.ok(!reader.seen('msg_8'), 'a pass while unfocused is not a read');
  // Focus leaves mid-dwell: the timer stops.
  reader.view('msg_9', visible);
  t.mock.timers.tick(DWELL_MS / 2);
  state.active = false;
  reader.activeChanged();
  reader.view('msg_9', visible);
  await run(t, DWELL_MS + BATCH_MS);
  assert.deepEqual(api.posts, [['dly_msg_7', 'read']]);
  assert.ok(!reader.seen('msg_9'));
});

test('a failed receipt is retried once, quietly, then left unread', async t => {
  const { api, reader, lostIds } = setup(t, { fail: dly => dly === 'dly_msg_7' || dly === 'dly_msg_8' });
  reader.view('msg_7', visible);
  await run(t, DWELL_MS + BATCH_MS);
  assert.deepEqual(api.posts, [['dly_msg_7', 'read']]);
  await run(t, RETRY_MS - 1);
  assert.equal(api.posts.length, 1, 'retried before RETRY_MS');
  await run(t, 1 + BATCH_MS);
  assert.deepEqual(api.posts, [['dly_msg_7', 'read'], ['dly_msg_7', 'read']]);
  assert.deepEqual(lostIds, ['msg_7']);
  // Gave up: no third attempt, however long it stays on screen.
  await run(t, 4 * (RETRY_MS + BATCH_MS));
  assert.equal(api.posts.length, 2);
  // Left unread: the view gets its unread style back.
  assert.ok(!reader.seen('msg_7'));
  // A retry that lands is the end of it.
  const second = setup(t, { fail: (dly, n) => n === 1 });
  second.reader.view('msg_9', visible);
  await run(t, DWELL_MS + BATCH_MS + RETRY_MS + BATCH_MS);
  await run(t, 4 * (RETRY_MS + BATCH_MS));
  assert.deepEqual(second.api.posts, [['dly_msg_9', 'read'], ['dly_msg_9', 'read']]);
});

test('a live arrival is unread until seen, and takes the divider only when not watched', async t => {
  const { api, reader, messages, matrix } = setup(t);
  // The operator read the four unread from the open; then msg_11 arrives.
  for (const id of ['msg_7', 'msg_8', 'msg_9', 'msg_10']) reader.view(id, visible);
  await run(t, DWELL_MS + BATCH_MS);
  const live = msg('msg_11', CODEX);
  messages.push(live);
  matrix.rows.push({ message_id: 'msg_11', sender: CODEX, cells: [cell(OP, 'msg_11', false)] });
  const unread = unreadOf(messages, matrix, { me: OP });
  reader.setUnread(unread);
  const fresh = unread.filter(u => u.message_id === 'msg_11');
  // Arriving while the operator watches the bottom: no New for it.
  assert.equal(nextDivider({ divider: 'msg_7', unread, fresh, seen: reader.seen, watching: true }), 'msg_7');
  // Arriving unwatched (unfocused, or scrolled up) once the old New is all read: New moves to it.
  assert.equal(nextDivider({ divider: 'msg_7', unread, fresh, seen: reader.seen, watching: false }), 'msg_11');
  assert.equal(nextDivider({ divider: null, unread, fresh, seen: reader.seen, watching: false }), 'msg_11');
  // No New yet, an earlier arrival still unseen: New goes above the oldest unseen, not the newest.
  const both = unreadOf(messages, matrix, { me: OP }).filter(u => u.message_id !== 'msg_11');
  assert.equal(nextDivider({ divider: null, unread: both.concat(fresh), fresh, seen: id => id !== 'msg_10', watching: false }), 'msg_10');
  // While the old New still marks unseen messages, it stays where it is.
  const unseen = unreadOf(...Object.values(history()), { me: OP }).concat(fresh);
  assert.equal(nextDivider({ divider: 'msg_7', unread: unseen, fresh, seen: () => false, watching: false }), 'msg_7');
  // Unread until seen: nothing is posted for it yet, then the same rule reads it.
  assert.ok(!reader.seen('msg_11'));
  assert.equal(api.posts.filter(p => p[0] === 'dly_msg_11').length, 0);
  reader.view('msg_11', visible);
  await run(t, DWELL_MS + BATCH_MS);
  assert.equal(api.posts.filter(p => p[0] === 'dly_msg_11').length, 1);
  assert.deepEqual(api.markAll, []);
});

test('a pass after the page was away does not count: tracking starts over on return', async t => {
  const { api, reader, state } = setup(t);
  reader.view('msg_7', visible);
  // Away before the second is up; meanwhile arrivals push msg_7 above the view.
  state.active = false;
  reader.activeChanged();
  state.active = true;
  reader.activeChanged();
  // The view re-observes: msg_7 is above, out of view. Nobody saw it leave.
  reader.view('msg_7', gone(true));
  await run(t, DWELL_MS + BATCH_MS);
  assert.ok(!reader.seen('msg_7'));
  assert.deepEqual(api.posts, []);
});

test('a message read elsewhere stops counting; leaving the thread sends what was earned', async t => {
  const { api, reader } = setup(t);
  reader.view('msg_7', visible);
  // Another client read msg_7 first: the next snapshot drops it, and so does the dwell.
  const after = history(7);
  reader.setUnread(unreadOf(after.messages, after.matrix, { me: OP }));
  await run(t, DWELL_MS + BATCH_MS);
  assert.deepEqual(api.posts, []);
  // Seen, then the thread is left inside the batch window: the receipt still goes.
  reader.view('msg_8', visible);
  reader.view('msg_8', gone(true));
  reader.destroy();
  await settle();
  assert.deepEqual(api.posts, [['dly_msg_8', 'read']]);
  reader.view('msg_9', visible);
  await run(t, DWELL_MS + BATCH_MS);
  assert.equal(api.posts.length, 1, 'a destroyed reader kept reading');
});

test('enough: 60% of the message, or 60% of the view for a tall one', () => {
  assert.equal(SEEN_RATIO, 0.6);
  assert.equal(DWELL_MS, 1000);
  assert.equal(BATCH_MS, 500);
  assert.equal(enough(0.6, 60, 800), true);
  assert.equal(enough(0.59, 59, 800), false);
  assert.equal(enough(0.3, 480, 800), true, 'a message three views tall');
  assert.equal(enough(0.3, 479, 800), false);
  assert.equal(enough(0, 0, 0), false);
});

test('opening a thread never calls the mark-all route; only the explicit button does', () => {
  // Reading follows per-delivery receipts. POST /v1/admin/threads/:id/read
  // is called in exactly one place: Mark thread read's confirmed click.
  const src = readFileSync(new URL('../static/thread.js', import.meta.url), 'utf8');
  const calls = src.match(/api\.readThread\(/g) || [];
  assert.equal(calls.length, 1, 'readThread is called from more than the button');
  const at = src.indexOf('api.readThread(');
  const handler = src.lastIndexOf("readBtn.addEventListener('click'", at);
  assert.ok(handler > 0 && at - handler < 800, 'readThread is not inside the Mark thread read click handler');
  assert.match(src.slice(handler, at), /if \(!readArmed\)/, 'the button marks read without a confirming second click');
  assert.doesNotMatch(src, /\/read['`]/);
  assert.match(src, /createReader\(/);
});

test('a thread opened with its first message unread reads that message at once, and only that one', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const api = fakeApi(), seenIds = [];
  const reader = createReader({ api, active: () => true, onSeen: id => seenIds.push(id) });
  // An agent's new thread: nothing read yet.
  const { messages, matrix } = history(0, 3);
  const unread = unreadOf(messages, matrix, { me: OP });
  reader.setUnread(unread);
  reader.readOnOpen('msg_1');
  await settle();
  // No dwell, no batch wait: the receipt is already out.
  assert.deepEqual(api.posts, [['dly_msg_1', 'read']]);
  assert.deepEqual(seenIds, ['msg_1']);
  assert.ok(reader.seen('msg_1') && !reader.seen('msg_2') && !reader.seen('msg_3'));
  // The rest still follow the dwell rule; msg_1 is never receipted again.
  reader.readOnOpen('msg_1');
  reader.view('msg_1', visible);
  reader.view('msg_2', visible);
  await run(t, DWELL_MS + BATCH_MS);
  assert.deepEqual(api.posts, [['dly_msg_1', 'read'], ['dly_msg_2', 'read']]);
  assert.deepEqual(api.markAll, []);
});

test('read on open waits for an active page, and drops a message read elsewhere meanwhile', async t => {
  const { api, reader, state, seenIds } = (() => {
    t.mock.timers.enable({ apis: ['setTimeout'] });
    const api = fakeApi(), seenIds = [], state = { active: false };
    const reader = createReader({ api, active: () => state.active, onSeen: id => seenIds.push(id) });
    const { messages, matrix } = history(0, 3);
    reader.setUnread(unreadOf(messages, matrix, { me: OP }));
    return { api, reader, state, seenIds };
  })();
  reader.readOnOpen('msg_1');
  await run(t, 10 * DWELL_MS);
  assert.deepEqual(api.posts, [], 'read while unfocused');
  // Focus returns: read at once, not after a dwell.
  state.active = true;
  reader.activeChanged();
  await settle();
  assert.deepEqual(api.posts, [['dly_msg_1', 'read']]);
  assert.deepEqual(seenIds, ['msg_1']);

  // A second thread: msg_1 is read in another tab before focus returns.
  const other = createReader({ api: fakeApi(), active: () => state.active });
  const fresh = history(0, 3);
  other.setUnread(unreadOf(fresh.messages, fresh.matrix, { me: OP }));
  state.active = false;
  other.readOnOpen('msg_1');
  const later = history(1, 3);
  other.setUnread(unreadOf(later.messages, later.matrix, { me: OP }));
  state.active = true;
  other.activeChanged();
  await run(t, DWELL_MS + BATCH_MS);
  assert.ok(!other.seen('msg_1'));
  // Not unread at all (a read thread, or a message that is not mine): no-op.
  const quiet = fakeApi();
  const idle = createReader({ api: quiet, active: () => true });
  const done = history(3, 3);
  idle.setUnread(unreadOf(done.messages, done.matrix, { me: OP }));
  idle.readOnOpen('msg_1');
  await run(t, DWELL_MS + BATCH_MS);
  assert.deepEqual(quiet.posts, []);
  // A destroyed reader reads nothing on return.
  const left = fakeApi();
  const gone = createReader({ api: left, active: () => state.active });
  gone.setUnread(unreadOf(fresh.messages, fresh.matrix, { me: OP }));
  state.active = false;
  gone.readOnOpen('msg_1');
  gone.destroy();
  state.active = true;
  gone.activeChanged();
  await run(t, DWELL_MS + BATCH_MS);
  assert.deepEqual(left.posts, []);
});

test('the view reads on open only a first message that is unread', () => {
  const src = readFileSync(new URL('../static/thread.js', import.meta.url), 'utf8');
  assert.match(src, /unread\[0\]\.message_id === all\[0\]\.message_id\) reader\.readOnOpen\(all\[0\]\.message_id\)/);
});
