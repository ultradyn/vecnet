import test from 'node:test';
import assert from 'node:assert/strict';
import { idParts, short } from '../static/format.js';
import { findPeer, peerFacts, peerHealth, peerHref, peerRoutes, peerSubs, peerThreads, principalFacts, routeUsage, scopeTarget, usageSummary } from '../static/peer-model.js';

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
  assert.equal(got[0].usage, null);
});

test('routeUsage and usageSummary keep body and latency stats, and a quiet route', () => {
  assert.equal(routeUsage(null), null);
  assert.equal(routeUsage({ requests: 'nope' }), null);
  const u = routeUsage({
    requests: 3, successes: 2, failures: 1,
    last_error: 'HTTP 500', last_error_at: '2026-11-06T00:00:00.000Z', last_status: 500,
    last_used_at: '2026-11-06T00:01:00.000Z', last_success_at: '2026-11-06T00:00:30.000Z',
    body_bytes: { avg: 1200.4, p95: 4000, p99: 8000, samples: 3 },
    latency_ms: { avg: 1.25, p95: 40, p99: 90, samples: 3 },
  });
  assert.equal(u.failures, 1);
  assert.equal(u.body.p99, 8000);
  assert.equal(u.latency.p95, 40);
  assert.deepEqual(usageSummary(null), ['No requests sent yet.']);
  const lines = usageSummary(u);
  assert.equal(lines[0], '3 sent · 2 ok · 1 failed');
  assert.match(lines[1], /body avg 1\.2 KiB · p95 3\.9 KiB · p99 7\.8 KiB · 3 recent/);
  assert.match(lines[2], /latency avg 1\.3 ms · p95 40 ms · p99 90 ms/);
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
