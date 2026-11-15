import test from 'node:test';
import assert from 'node:assert/strict';
import { idParts, short } from '../static/format.js';
import { findPeer, messageDraft, peerActions, peerFacts, peerHealth, peerHref, peerRoutes, peerSubs, peerThreads, principalFacts, recentThread, scopeTarget } from '../static/peer-model.js';

const PEER = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8B';
const PRN = 'prn_01JD3Q2K8M4N6P8R0T2V4X6Z8C';

test('idParts splits an id into the visible head and tail and the hidden middle', () => {
  const p = idParts(PEER);
  assert.deepEqual(p, { head: 'peer_', mid: '01JD3Q2K8M4N6P8R0T2V4', tail: 'X6Z8B' });
  // The three parts are the whole id, so a selection copies it whole; the
  // visible parts with an ellipsis are what short() shows.
  assert.equal(p.head + p.mid + p.tail, PEER);
  assert.equal(p.head + '…' + p.tail, short(PEER));
  // Short ids are not elided at all.
  assert.deepEqual(idParts('thr_1'), { head: 'thr_1', mid: '', tail: '' });
  // No prefix: everything but the tail is hidden, as short() does.
  const bare = idParts('ABCDEFGHIJKLMNOPQRSTU');
  assert.equal(bare.head, '');
  assert.equal(bare.head + bare.mid + bare.tail, 'ABCDEFGHIJKLMNOPQRSTU');
  assert.equal(bare.head + '…' + bare.tail, short('ABCDEFGHIJKLMNOPQRSTU'));
  assert.deepEqual(idParts(''), { head: '?', mid: '', tail: '' });
  assert.deepEqual(idParts(undefined), { head: '?', mid: '', tail: '' });
});

test('peerHref routes only well-formed ids', () => {
  assert.equal(peerHref(PEER), '#peer/' + PEER);
  assert.equal(peerHref('src_01JD'), '#peer/src_01JD');
  assert.equal(peerHref(''), null);
  assert.equal(peerHref(undefined), null);
  assert.equal(peerHref('hub'), null);
  assert.equal(peerHref('thr_01JD'), null);
  assert.equal(peerHref('peer_x"><img>'), null);
  assert.equal(peerHref('peer x'), null);
});

const row = (over = {}) => ({
  peer: {
    peer_id: PEER, name: 'codex-hark', kind: 'session', capabilities: ['chat', 'review'], quota: { weekly_remaining_pct: 61 },
    location: 'Sydney', presence: 'working', reachability: 'reachable', last_seen: '2026-11-04T01:00:00.000Z',
    enrolled_at: '2026-11-01T00:00:00.000Z', teams: ['team_a'], created_by: PRN,
  },
  principal_id: PRN, credential: 'active', disabled: false, presence_at: '2026-11-04T00:59:00.000Z', reach_via: 'call',
  routes: [], scopes: { allow: ['peer_b'], deny: ['team_x'] }, limits: { sends_per_min: 60, threads_per_hour: 20 },
  ...over,
});

const runtimeNow = Date.parse('2026-11-15T01:00:30Z');
const runtime = (over = {}) => ({ runtime: 'codex', state: 'working', delivery: 'ready', version: '1.2.3',
  updated_at: '2026-11-15T01:00:00Z', expires_at: '2026-11-15T01:01:00Z', ...over });
const runtimeRow = status => row({ peer: { ...row().peer, presence: 'idle', runtime_status: status } });

test('runtime facts read the nested admin peer status without changing manual presence', () => {
  const f = peerFacts(runtimeRow(runtime()), runtimeNow);
  assert.equal(f.presence, 'idle');
  assert.deepEqual(f.runtimeStatus, { state: 'working', delivery: 'ready', reason: null, version: '1.2.3',
    updatedAt: '2026-11-15T01:00:00Z', stale: false });
});

test('runtime facts explain blocked and waiting reports using safe reason labels', () => {
  for (const [reason, label] of [['waiting_approval', 'Waiting for approval'], ['waiting_input', 'Waiting for input'],
    ['not_loaded', 'Waiting for runtime'], ['delivery_unresolved', 'Delivery outcome unresolved']]) {
    const f = peerFacts(runtimeRow(runtime({ state: 'blocked', delivery: 'unavailable', reason })), runtimeNow);
    assert.equal(f.runtimeStatus.state, 'blocked');
    assert.equal(f.runtimeStatus.delivery, 'unavailable');
    assert.equal(f.runtimeStatus.reason, label);
  }
});

test('runtime connection reasons explain waiting, offline and unsupported delivery', () => {
  for (const [reason, delivery, label] of [['connecting', 'unavailable', 'Waiting for runtime connection'],
    ['unreachable', 'unavailable', 'Runtime unreachable'], ['stopped', 'unavailable', 'Runtime stopped'],
    ['refused_version', 'unsupported', 'Unsupported runtime version']]) {
    const f = peerFacts(runtimeRow(runtime({ state: 'offline', delivery, reason })), runtimeNow).runtimeStatus;
    assert.equal(f.reason, label);
    assert.equal(f.delivery, delivery);
  }
});

test('a cached runtime report expires at the exact hub deadline without mutating the row', () => {
  const r = runtimeRow(runtime({ reason: 'waiting_input' }));
  const before = peerFacts(r, Date.parse('2026-11-15T01:00:59.999Z'));
  assert.equal(before.runtimeStatus.delivery, 'ready');
  const at = peerFacts(r, Date.parse('2026-11-15T01:01:00Z'));
  assert.equal(at.runtimeStatus.state, 'unknown');
  assert.equal(at.runtimeStatus.delivery, 'unknown');
  assert.equal(at.runtimeStatus.reason, 'Stale report');
  assert.equal(at.runtimeStatus.stale, true);
  assert.equal(at.runtimeStatus.updatedAt, '2026-11-15T01:00:00Z');
  assert.equal(r.peer.runtime_status.delivery, 'ready');
});

test('hub-projected stale reports remain stale even if the local clock is behind', () => {
  const f = peerFacts(runtimeRow(runtime({ state: 'unknown', delivery: 'unknown', reason: 'stale' })), runtimeNow);
  assert.equal(f.runtimeStatus.stale, true);
  assert.equal(f.runtimeStatus.reason, 'Stale report');
});

test('older hubs and never reported runtimes omit runtime facts', () => {
  for (const status of [undefined, null, false, 1, 'codex', [], {}, { runtime: 'other' }]) {
    assert.equal(peerFacts(runtimeRow(status), runtimeNow).runtimeStatus, null);
  }
  assert.equal(peerFacts(row(), runtimeNow).runtimeStatus, null);
  assert.equal(peerFacts(null, runtimeNow).runtimeStatus, null);
});

test('malformed runtime reports never claim delivery ready', () => {
  for (const over of [{ state: {} }, { delivery: ['ready'] }, { updated_at: null }, { updated_at: 'bad' },
    { expires_at: {} }, { expires_at: 'bad' }, { expires_at: '2026-11-15T00:59:59Z' },
    { updated_at: '2026-11-15T01:00:31Z' }]) {
    const f = peerFacts(runtimeRow(runtime(over)), runtimeNow).runtimeStatus;
    assert.equal(f.state, 'unknown', JSON.stringify(over));
    assert.equal(f.delivery, 'unknown', JSON.stringify(over));
  }
  const junk = peerFacts(runtimeRow(runtime({ reason: '/private/thread/secret', version: { secret: true } })), runtimeNow).runtimeStatus;
  assert.equal(junk.reason, null);
  assert.equal(junk.version, null);
  assert.equal(peerFacts(runtimeRow(runtime())).runtimeStatus.delivery, 'unknown', 'no trusted clock cannot promise ready');
});

test('findPeer still returns an archived peer, so a deep link can open it', () => {
  const at = '2026-11-07T01:00:00.000Z';
  const page = { peers: [row({ archived_at: at, disabled: true })] };
  const found = findPeer(page, PEER);
  assert.equal(found.peer.peer_id, PEER);
  const f = peerFacts(found);
  assert.equal(f.id, PEER);
  assert.equal(f.archivedAt, at);
  assert.equal(f.disabled, true);
});

test('findPeer finds a row by peer id, and tolerates a missing page', () => {
  const page = { peers: [row(), row({ peer: { peer_id: 'peer_other', name: 'o', kind: 'dot' } })] };
  assert.equal(findPeer(page, PEER).peer.name, 'codex-hark');
  assert.equal(findPeer(page, 'peer_nope'), null);
  assert.equal(findPeer(null, PEER), null);
  assert.equal(findPeer({ peers: [null, {}] }, PEER), null);
});

test('peerFacts flattens a PeerRow, keeping presence and reachability apart', () => {
  const f = peerFacts(row());
  assert.equal(f.id, PEER);
  assert.equal(f.name, 'codex-hark');
  assert.equal(f.kind, 'session');
  assert.equal(f.disabled, false);
  assert.equal(f.credential, 'active');
  assert.equal(f.principalId, PRN);
  assert.equal(f.createdBy, PRN);
  assert.equal(f.presence, 'working');
  assert.equal(f.presenceAt, '2026-11-04T00:59:00.000Z');
  assert.equal(f.reach, 'reachable');
  assert.equal(f.via, 'call');
  assert.equal(f.seen, '2026-11-04T01:00:00.000Z');
  assert.deepEqual(f.capabilities, ['chat', 'review']);
  assert.equal(f.quota, '61%');
  assert.deepEqual(f.teams, ['team_a']);
  assert.deepEqual(f.scopes, { allow: ['peer_b'], deny: ['team_x'], open: false });
  assert.deepEqual(f.limits, { sendsPerMin: 60, threadsPerHour: 20 });
});

test('peerFacts reads a source, an old hub and junk without inventing state', () => {
  const src = peerFacts({ peer: { peer_id: 'src_1', name: 'gh', kind: 'source' }, credential: 'none', limits: null });
  assert.equal(src.presence, 'n/a');
  assert.equal(src.reach, 'unknown');
  assert.equal(src.limits, null);
  assert.deepEqual(src.scopes, { allow: [], deny: [], open: true });
  // A hub from before scopes and limits were on PeerRow: unknown, not "allow all".
  const old = peerFacts({ peer: { peer_id: PEER, name: 'x', kind: 'session' } });
  assert.equal(old.scopes, null);
  assert.equal(old.limits, null);
  assert.equal(old.presence, 'unknown');
  assert.equal(old.credential, 'none');
  assert.equal(old.createdBy, '');
  // Strings that are not strings are dropped, never shown as [object Object].
  const junk = peerFacts({ peer: { peer_id: PEER, name: { x: 1 }, kind: 7, capabilities: ['ok', 3, null], teams: 'team_a' }, scopes: { allow: 'x', deny: [1] } });
  assert.equal(junk.name, '');
  assert.equal(junk.kind, '');
  assert.deepEqual(junk.capabilities, ['ok']);
  assert.deepEqual(junk.teams, []);
  assert.deepEqual(junk.scopes, { allow: [], deny: [], open: true });
});

test('principalFacts finds a principal and reads its limits', () => {
  const page = { principals: [{ principal_id: PRN, name: 'pagd', max_peers: 5, creates_per_hour: 10, kinds: ['pagchat-bot'], active: 2, disabled: 1, creates_last_hour: 0, scoped_peers: 1, default_allow: ['peer_b'], default_deny: [] }] };
  const p = principalFacts(page, PRN);
  assert.deepEqual(p, { id: PRN, name: 'pagd', maxPeers: 5, createsPerHour: 10, kinds: ['pagchat-bot'], active: 2, disabled: 1, createsLastHour: 0, defaultAllow: ['peer_b'], defaultDeny: [] });
  assert.equal(principalFacts(page, 'prn_nope'), null);
  assert.equal(principalFacts(null, PRN), null);
  assert.equal(principalFacts(page, ''), null);
});

test('scopeTarget names what a scope id points at', () => {
  assert.equal(scopeTarget('peer_1'), 'peer');
  assert.equal(scopeTarget('team_1'), 'team');
  assert.equal(scopeTarget('grp_1'), 'group');
  assert.equal(scopeTarget('zzz'), 'id');
});

test('peerRoutes keeps the peer\'s own routes and those it inherits from its principal', () => {
  const routes = { routes: [
    { route_id: 'rt_1', owner_kind: 'peer', owner_id: PEER, kind: 'http', host: 'a.example', verified_at: '2026-11-01T00:00:00.000Z', created_at: '2026-11-01T00:00:00.000Z', runs_per_hour: 0 },
    { route_id: 'rt_2', owner_kind: 'principal', owner_id: PRN, kind: 'grokbot', host: 'api2.cursor.sh', verified_at: null, created_at: '2026-11-02T00:00:00.000Z', runs_per_hour: 6 },
    { route_id: 'rt_3', owner_kind: 'peer', owner_id: 'peer_other', kind: 'http', host: 'b.example', verified_at: null, created_at: '', runs_per_hour: 0 },
  ] };
  const got = peerRoutes(routes, PEER, PRN);
  assert.deepEqual(got.map(r => [r.id, r.via, r.kind, r.host, r.verified, r.runsPerHour]), [
    ['rt_1', 'peer', 'http', 'a.example', '2026-11-01T00:00:00.000Z', 0],
    ['rt_2', 'principal', 'grokbot', 'api2.cursor.sh', null, 6],
  ]);
  // Without the principal (its row not loaded yet), only the peer's own.
  assert.deepEqual(peerRoutes(routes, PEER, '').map(r => r.id), ['rt_1']);
  assert.deepEqual(peerRoutes(null, PEER, PRN), []);
});

test('peerSubs, peerHealth and peerThreads keep only this peer', () => {
  const subs = [{ sub_id: 's1', peer_id: PEER }, { sub_id: 's2', peer_id: 'peer_other' }, null];
  assert.deepEqual(peerSubs(subs, PEER).map(s => s.sub_id), ['s1']);
  assert.deepEqual(peerSubs({ not: 'an array' }, PEER), []);
  const health = {
    blocked: [{ recipient: PEER, thread_id: 'thr_1' }, { recipient: 'peer_other', thread_id: 'thr_2' }],
    failed: [{ recipient: 'peer_other', delivery_id: 'dly_1' }, { recipient: PEER, delivery_id: 'dly_2' }],
  };
  const h = peerHealth(health, PEER);
  assert.deepEqual(h.blocked.map(b => b.thread_id), ['thr_1']);
  assert.deepEqual(h.failed.map(f => f.delivery_id), ['dly_2']);
  assert.deepEqual(peerHealth(null, PEER), { blocked: [], failed: [] });
  // The hub filters by participant; a row that slipped through is dropped.
  const page = { threads: [
    { thread: { thread_id: 'thr_a', participants: [PEER, 'peer_op'] }, last_message_at: '2026-11-04T00:00:00.000Z' },
    { thread: { thread_id: 'thr_b', participants: ['peer_op'] }, last_message_at: '2026-11-04T01:00:00.000Z' },
    { thread: { thread_id: 'thr_c', participants: [PEER] }, last_message_at: '2026-11-04T02:00:00.000Z' },
  ], next: 'c' };
  const t = peerThreads(page, PEER);
  assert.deepEqual(t.rows.map(r => r.thread.thread_id), ['thr_c', 'thr_a']);
  assert.equal(t.more, true);
  assert.deepEqual(peerThreads(null, PEER), { rows: [], more: false });
});

// The header's message actions (PROTOCOL §2.13 threads by participant).
const ME = 'peer_01JD3Q2K8M4N6P8R0T2V4X60PS';
const thr = (id, parts, at) => ({ thread: { thread_id: id, title: id, participants: parts }, last_message_at: at });
const facts = over => peerFacts(row({ peer: { ...row().peer, ...over } }));

test('recentThread is the newest thread both the peer and the operator are in', () => {
  const page = { threads: [
    thr('thr_old', [PEER, ME], '2026-11-04T00:00:00.000Z'),
    thr('thr_peers', [PEER, 'peer_b'], '2026-11-04T03:00:00.000Z'), // newer, but the operator is not in it
    thr('thr_new', [ME, 'peer_b', PEER], '2026-11-04T02:00:00.000Z'),
    thr('thr_stray', [ME], '2026-11-04T04:00:00.000Z'),            // slipped past the hub's filter
  ] };
  assert.equal(recentThread(page, PEER, ME).thread.thread_id, 'thr_new');
  assert.equal(recentThread({ threads: [thr('thr_peers', [PEER, 'peer_b'], 'x')] }, PEER, ME), null);
  assert.equal(recentThread(page, PEER, ''), null);
  assert.equal(recentThread(null, PEER, ME), null);
  assert.equal(recentThread({ threads: [{ thread: { participants: [PEER, ME] } }] }, PEER, ME), null, 'a row without an id is no link');
});

test('messageDraft picks the peer, at wake_idle for a Grok Bot and wake otherwise', () => {
  assert.deepEqual(messageDraft(facts({})), { recipients: [PEER], priority: 'wake' });
  assert.deepEqual(messageDraft(facts({ kind: 'grokbot' })), { recipients: [PEER], priority: 'wake_idle' });
  for (const kind of ['launcher', 'openclaw', 'pagchat-bot', 'dot', '']) assert.equal(messageDraft(facts({ kind })).priority, 'wake', kind);
});

test('peerActions: a shared thread is opened; New message is always offered', () => {
  const page = { threads: [thr('thr_x', [PEER, ME], '2026-11-04T02:00:00.000Z')] };
  const a = peerActions(facts({}), page, ME, null);
  assert.equal(a.open.state, 'thread');
  assert.equal(a.open.href, '#thread/thr_x');
  assert.equal(a.open.thread.thread_id, 'thr_x');
  assert.equal(a.open.at, '2026-11-04T02:00:00.000Z');
  assert.deepEqual(a.compose, { href: '#new', draft: { recipients: [PEER], priority: 'wake' } });
});

test('peerActions: no shared thread opens New message with the same draft', () => {
  const none = { threads: [thr('thr_peers', [PEER, 'peer_b'], '2026-11-04T02:00:00.000Z')] };
  const a = peerActions(facts({}), none, ME, null);
  assert.deepEqual(a.open, { state: 'compose', href: '#new', draft: { recipients: [PEER], priority: 'wake' }, more: false });
  assert.deepEqual(a.compose.draft, a.open.draft);
  // A Grok Bot defaults to wake_idle on both.
  const g = peerActions(facts({ kind: 'grokbot' }), { threads: [] }, ME, null);
  assert.equal(g.open.state, 'compose');
  assert.equal(g.open.draft.priority, 'wake_idle');
  assert.equal(g.compose.draft.priority, 'wake_idle');
  // A later page may hold one: "none" is only among the newest.
  assert.equal(peerActions(facts({}), { threads: [], next: 'c' }, ME, null).open.more, true);
});

test('peerActions waits for the thread page, shows its error, and keeps a page it already has', () => {
  const err = new Error('boom');
  assert.deepEqual(peerActions(facts({}), null, ME, null).open, { state: 'loading', error: null });
  const failed = peerActions(facts({}), null, ME, err);
  assert.equal(failed.open.state, 'error');
  assert.equal(failed.open.error, err);
  assert.equal(failed.compose.href, '#new', 'New message stays while threads fail');
  assert.equal(peerActions(facts({}), { threads: [thr('thr_x', [PEER, ME], 'x')] }, ME, err).open.state, 'thread');
});

test('peerActions offers nothing for a source, the operator, or no peer row', () => {
  assert.equal(peerActions(facts({ kind: 'source' }), { threads: [] }, ME, null), null);
  assert.equal(peerActions(facts({ kind: 'operator' }), { threads: [] }, ME, null), null);
  assert.equal(peerActions(facts({ peer_id: ME }), { threads: [] }, ME, null), null);
  assert.equal(peerActions(null, { threads: [] }, ME, null), null);
});
