import test from 'node:test';
import assert from 'node:assert/strict';
import { OTHER_MAX, OTHER_PAGE, nextLimit, otherThreads, peopleLine, yourThreads } from '../static/sidebar-model.js';

const NOW = Date.parse('2026-11-04T10:00:00.000Z');
const back = ms => new Date(NOW - ms).toISOString();
const NAMES = { peer_me: 'max', peer_a: 'codex-hark', peer_b: 'grok-reviewer', peer_c: 'pag-sleuth', peer_d: 'claw-ops', peer_e: 'dot-research' };
const nameOf = id => NAMES[id] || id;

function row(id, { title = null, participants = ['peer_a', 'peer_b'], count = 1, at = back(60_000) } = {}) {
  return { thread: { thread_id: id, title, participants, message_count: count }, last_message_at: at, created_at: at, created_by: participants[0], cut: null };
}
const view = (page, extra = {}) => otherThreads({ page, error: null, mine: [], me: 'peer_me', nameOf, now: NOW, limit: OTHER_PAGE, ...extra });

test('peopleLine names up to three participants and counts the rest', () => {
  assert.equal(peopleLine([], nameOf), '');
  assert.equal(peopleLine(['peer_a'], nameOf), 'codex-hark');
  assert.equal(peopleLine(['peer_a', 'peer_b', 'peer_c'], nameOf), 'codex-hark, grok-reviewer, pag-sleuth');
  assert.equal(peopleLine(['peer_a', 'peer_b', 'peer_c', 'peer_d', 'peer_e'], nameOf), 'codex-hark, grok-reviewer, pag-sleuth +2');
  // nameOf falls back to an id for a peer the directory does not list.
  assert.equal(peopleLine(['peer_zz'], id => NAMES[id] || 'peer_…zz'), 'peer_…zz');
  assert.equal(peopleLine(null, nameOf), '');
});

test('otherThreads sorts by last activity, newest first, ties by id descending', () => {
  const v = view({ threads: [
    row('thr_old', { at: back(3 * 3_600_000) }),
    row('thr_new', { at: back(5_000) }),
    row('thr_mid1', { at: back(5 * 60_000) }),
    row('thr_mid2', { at: back(5 * 60_000) }),
  ], next: null });
  assert.equal(v.state, 'ok');
  assert.deepEqual(v.rows.map(r => r.id), ['thr_new', 'thr_mid2', 'thr_mid1', 'thr_old']);
  assert.deepEqual(v.rows.map(r => r.active), ['5s ago', '5m ago', '5m ago', '3h ago']);
});

test('otherThreads excludes Your Threads and any thread the operator is in', () => {
  const v = view({ threads: [
    row('thr_listed'),
    row('thr_joined', { participants: ['peer_a', 'peer_me'] }),
    row('thr_other'),
  ], next: null }, { mine: [{ thread_id: 'thr_listed' }] });
  assert.deepEqual(v.rows.map(r => r.id), ['thr_other']);
});

test('otherThreads row: title, participants line, count, relative and full time', () => {
  const [r] = view({ threads: [row('thr_1', { title: 'Release plan', participants: ['peer_a', 'peer_b', 'peer_c', 'peer_d'], count: 42, at: back(12 * 60_000) })], next: null }).rows;
  assert.equal(r.id, 'thr_1');
  assert.equal(r.href, '#thread/thr_1');
  assert.equal(r.title, 'Release plan');
  assert.equal(r.untitled, false);
  assert.equal(r.people, 'codex-hark, grok-reviewer, pag-sleuth +1');
  assert.equal(r.count, '42');
  assert.equal(r.active, '12m ago');
  assert.match(r.tip, /^Release plan\ncodex-hark, grok-reviewer, pag-sleuth, claw-ops\n42 messages · last active /);
});

test('otherThreads falls back for a missing count or time', () => {
  const t = row('thr_01JBCDEFGHJKMNPQRS', { title: 'Plan', count: undefined, at: '' });
  delete t.thread.message_count;
  const [r] = view({ threads: [t], next: null }).rows;
  assert.equal(r.count, '');
  assert.equal(r.active, '');
  assert.equal(r.tip, 'Plan\ncodex-hark, grok-reviewer');
});

test('an untitled other thread names its participants; the second line counts them', () => {
  const [pair, group] = view({ threads: [
    row('thr_2', { at: back(1000) }),
    row('thr_1', { participants: ['peer_a', 'peer_b', 'peer_c', 'peer_d', 'peer_e'], at: back(2000) }),
  ], next: null }).rows;
  assert.equal(pair.title, 'codex-hark ↔ grok-reviewer');
  assert.equal(pair.untitled, true);
  assert.equal(pair.people, '2 participants');
  assert.equal(group.title, 'codex-hark, grok-reviewer +3');
  assert.equal(group.people, '5 participants');
  // The tip still lists everyone.
  assert.match(group.tip, /^codex-hark, grok-reviewer \+3\ncodex-hark, grok-reviewer, pag-sleuth, claw-ops, dot-research\n1 message · last active /);
});

const mineView = extra => yourThreads({ page: null, summaries: null, error: null, me: 'peer_me', nameOf, now: NOW, ...extra });
const summary = (id, { title = null, participants = ['peer_me', 'peer_a'], count = 1 } = {}) => ({ thread_id: id, title, participants, message_count: count });

test('yourThreads uses the shared row: title, others, count, last active', () => {
  const v = mineView({ page: { threads: [
    row('thr_b', { title: 'Release plan', participants: ['peer_me', 'peer_a', 'peer_b', 'peer_c', 'peer_d'], count: 7, at: back(3 * 60_000) }),
    row('thr_a', { participants: ['peer_a', 'peer_me'], count: 2, at: back(2 * 3_600_000) }),
  ], next: null } });
  assert.equal(v.state, 'ok');
  const [titled, oneToOne] = v.rows;
  // The operator is in every one of these, so the participants line leaves it out.
  assert.deepEqual({ ...titled, tip: undefined }, {
    id: 'thr_b', href: '#thread/thr_b', title: 'Release plan', untitled: false,
    people: 'codex-hark, grok-reviewer, pag-sleuth +1', count: '7', active: '3m ago', tip: undefined,
  });
  // The tip names the operator as you.
  assert.match(titled.tip, /^Release plan\nyou, codex-hark, grok-reviewer, pag-sleuth, claw-ops\n7 messages · last active /);
  assert.equal(oneToOne.title, 'codex-hark');
  assert.equal(oneToOne.untitled, true);
  assert.equal(oneToOne.people, '2 participants');
  assert.equal(oneToOne.active, '2h ago');
});

test('yourThreads keeps the hub order', () => {
  const v = mineView({ page: { threads: [row('thr_a', { at: back(9000) }), row('thr_z', { at: back(1000) })], next: null } });
  assert.deepEqual(v.rows.map(r => r.id), ['thr_a', 'thr_z']);
});

test('yourThreads shows the plain summaries until the timed page arrives', () => {
  const v = mineView({ summaries: [summary('thr_1', { title: 'Hosting', count: 4 }), summary('thr_2', { participants: ['peer_me', 'peer_a', 'peer_b'] })] });
  assert.equal(v.state, 'ok');
  assert.deepEqual(v.rows.map(r => [r.title, r.people, r.count, r.active]), [
    ['Hosting', 'codex-hark', '4', ''],
    ['codex-hark, grok-reviewer', '3 participants', '1', ''],
  ]);
  // A page, once there, wins.
  const both = mineView({ summaries: [summary('thr_1')], page: { threads: [row('thr_9', { participants: ['peer_me', 'peer_b'] })], next: null } });
  assert.deepEqual(both.rows.map(r => r.id), ['thr_9']);
});

test('yourThreads states: loading, error, empty', () => {
  assert.deepEqual(mineView({}), { state: 'loading', note: '…', rows: [] });
  assert.deepEqual(mineView({ error: new Error('boom') }), { state: 'error', note: 'Threads unavailable', rows: [] });
  assert.deepEqual(mineView({ summaries: [] }), { state: 'empty', note: 'No threads yet', rows: [] });
  assert.deepEqual(mineView({ page: { threads: [], next: null } }), { state: 'empty', note: 'No threads yet', rows: [] });
  // Rows already shown stay shown through a failed reload.
  assert.equal(mineView({ summaries: [summary('thr_1')], error: new Error('boom') }).state, 'ok');
});

test('otherThreads states: loading, error, empty, empty after exclusion', () => {
  assert.equal(view(undefined).state, 'loading');
  assert.equal(view(undefined).note, '…');
  const failed = view(undefined, { error: new Error('boom') });
  assert.equal(failed.state, 'error');
  assert.equal(failed.note, 'Other threads unavailable');
  assert.deepEqual(failed.rows, []);
  const empty = view({ threads: [], next: null });
  assert.equal(empty.state, 'empty');
  assert.equal(empty.note, 'No other threads');
  assert.equal(view({ threads: [row('thr_x', { participants: ['peer_me'] })], next: null }).state, 'empty');
  // A failed reload keeps the last rows, as the Your Threads list does.
  const stale = view({ threads: [row('thr_y')], next: null }, { error: new Error('boom') });
  assert.equal(stale.state, 'ok');
  assert.equal(stale.rows.length, 1);
});

test('otherThreads bounds the list and says how to see the rest', () => {
  const many = Array.from({ length: 60 }, (_, i) => row('thr_' + String(i).padStart(3, '0'), { at: back(i * 1000) }));
  const first = view({ threads: many.slice(0, OTHER_PAGE), next: 'tok' });
  assert.equal(first.rows.length, OTHER_PAGE);
  assert.equal(first.more, 'show');
  // A page longer than the limit (e.g. after exclusion races a reload) is cut.
  assert.equal(view({ threads: many, next: null }).rows.length, OTHER_PAGE);
  assert.equal(view({ threads: many, next: null }).more, 'show');
  assert.equal(view({ threads: many.slice(0, 10), next: null }).more, '');
  // At the hub's page cap the rest are on the dashboard.
  assert.equal(view({ threads: many.slice(0, 10), next: 'tok' }, { limit: OTHER_MAX }).more, 'dash');
});

test('nextLimit grows a page at a time up to the cap', () => {
  assert.equal(nextLimit(OTHER_PAGE), OTHER_PAGE * 2);
  assert.equal(nextLimit(OTHER_MAX - 10), OTHER_MAX);
  assert.equal(nextLimit(OTHER_MAX), OTHER_MAX);
});
