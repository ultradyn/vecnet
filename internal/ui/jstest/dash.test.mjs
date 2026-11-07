import test from 'node:test';
import assert from 'node:assert/strict';
import { allThreads, blockedLine, cutSummary, peerRows, visiblePeers } from '../static/dash-model.js';

function row(peer, extra = {}) {
  return {
    peer: {
      peer_id: 'peer_x', name: 'x', kind: 'session', capabilities: [], quota: {}, location: null,
      presence: 'idle', reachability: 'connected', last_seen: null, enrolled_at: null, teams: [], ...peer,
    },
    principal_id: 'prn_x', credential: 'active', disabled: false, presence_at: null, reach_via: null, routes: [],
    ...extra,
  };
}

test('peerRows: dot reachable + unknown presence; disabled flagged; source shows n/a presence', () => {
  const rows = peerRows([
    row({ peer_id: 'peer_dot', name: 'dot-research', kind: 'dot', presence: 'unknown', reachability: 'reachable', quota: { weekly_remaining_pct: 35 }, location: 'chatgpt', last_seen: '2026-11-02T09:42:00.000Z' },
      { reach_via: 'mcp-events', routes: [{ route_id: 'rte_1', kind: 'http', host: 'chatgpt.com', verified_at: null }, { route_id: 'rte_2', kind: 'http', host: 'chatgpt.com', verified_at: null }, { route_id: 'rte_3', kind: 'http', host: '', verified_at: null }] }),
    row({ peer_id: 'peer_off', name: 'retired', kind: 'session', presence: '', reachability: '' }, { disabled: true, credential: 'revoked', presence_at: '2026-11-01T00:00:00.000Z' }),
    row({ peer_id: 'src_gh', name: 'gh-ultradyn', kind: 'source', presence: 'unknown', reachability: 'unknown' }, { credential: 'none' }),
  ]);
  assert.equal(rows.length, 3);
  const [dot, off, src] = rows;

  // Presence and reachability are two signals; unknown presence is never offline (§8).
  assert.equal(dot.id, 'peer_dot');
  assert.equal(dot.name, 'dot-research');
  assert.equal(dot.kind, 'dot');
  assert.equal(dot.presence, 'unknown');
  assert.notEqual(dot.presence, 'offline');
  assert.equal(dot.reach, 'reachable');
  assert.equal(dot.via, 'mcp-events');
  assert.equal(dot.seen, '2026-11-02T09:42:00.000Z');
  assert.equal(dot.presenceAt, null);
  assert.equal(dot.quota, '35%');
  assert.equal(dot.location, 'chatgpt');
  assert.deepEqual(dot.hosts, ['chatgpt.com']);
  assert.equal(dot.credential, 'active');
  assert.equal(dot.disabled, false);

  // A missing value is shown as unknown, not invented.
  assert.equal(off.disabled, true);
  assert.equal(off.presence, 'unknown');
  assert.equal(off.reach, 'unknown');
  assert.equal(off.presenceAt, '2026-11-01T00:00:00.000Z');
  assert.equal(off.credential, 'revoked');
  assert.equal(off.quota, 'none');
  assert.equal(off.location, '');
  assert.deepEqual(off.hosts, []);

  // A source only pushes in and reports no presence.
  assert.equal(src.presence, 'n/a');
  assert.equal(src.kind, 'source');
  assert.equal(src.disabled, false);

  assert.deepEqual(peerRows(undefined), []);
});

test('visiblePeers hides archived peers unless show archived is on', () => {
  const raw = [
    row({ peer_id: 'peer_live', name: 'live' }),
    row({ peer_id: 'peer_off', name: 'paused' }, { disabled: true }),
    row({ peer_id: 'peer_old', name: 'retired' }, { disabled: true, archived_at: '2026-11-07T01:00:00.000Z' }),
    row({ peer_id: 'peer_junk', name: 'junk' }, { archived_at: 1 }),
  ];
  const rows = peerRows(raw);
  assert.equal(rows[0].archivedAt, null);
  assert.equal(rows[1].archivedAt, null);
  assert.equal(rows[2].archivedAt, '2026-11-07T01:00:00.000Z');
  // A non-string archived_at is not an archive stamp.
  assert.equal(rows[3].archivedAt, null);

  const hidden = visiblePeers(raw, false);
  assert.deepEqual(hidden.shown.map(p => p.id), ['peer_live', 'peer_off', 'peer_junk']);
  assert.equal(hidden.archived, 1);
  assert.equal(hidden.shown.some(p => p.archivedAt), false);

  const shown = visiblePeers(raw, true);
  assert.deepEqual(shown.shown.map(p => p.id), ['peer_live', 'peer_off', 'peer_old', 'peer_junk']);
  assert.equal(shown.shown[2].archivedAt, '2026-11-07T01:00:00.000Z');
  assert.equal(shown.archived, 1);

  // The toggle defaults off: omitting it hides archived rows.
  assert.deepEqual(visiblePeers(raw).shown.map(p => p.id), hidden.shown.map(p => p.id));
  assert.deepEqual(visiblePeers(undefined), { shown: [], archived: 0 });
});

test('blockedLine names recipient, delivery and waiting count', () => {
  const b = { recipient: 'peer_triage', thread_id: 'thr_q', delivery_id: 'dly_01JD3Q2K8M4N6P8R0T2V4X6Z8B', attempt: 2, waiting: 3, next_attempt_at: null, last_status: 503, last_error: 'retry pending' };
  const named = blockedLine(b, { peer: id => (id === 'peer_triage' ? 'grok-triage' : id), thread: id => (id === 'thr_q' ? 'Delivery health 5' : id) });
  assert.match(named, /grok-triage/);
  assert.match(named, /Delivery health 5/);
  assert.match(named, /blocked on delivery dly_01JD3Q2K8M4N6P8R0T2V4X6Z8B/);
  assert.match(named, /3 waiting/);
  assert.match(named, /attempt 2/);
  assert.match(named, /503/);

  // Without a directory it still names the recipient by id.
  const bare = blockedLine(b);
  assert.match(bare, /peer_triage/);
  assert.match(bare, /dly_01JD3Q2K8M4N6P8R0T2V4X6Z8B/);
  assert.match(bare, /3 waiting/);

  assert.match(blockedLine({ ...b, waiting: 1 }), /1 waiting/);
  assert.match(blockedLine({ ...b, waiting: 0 }), /0 waiting/);
});

test('cutSummary orders reasons by count', () => {
  const s = cutSummary({ since: '2026-11-01T10:00:00.000Z', hours: 24, total: 7, by_reason: { expired: 1, max_depth_exceeded: 3, thread_budget_exhausted: 2, rate_limited: 1 }, recent: [] });
  assert.equal(s.total, 7);
  assert.equal(s.hours, 24);
  assert.deepEqual(s.reasons, [
    { reason: 'max_depth_exceeded', count: 3 },
    { reason: 'thread_budget_exhausted', count: 2 },
    { reason: 'expired', count: 1 },
    { reason: 'rate_limited', count: 1 },
  ]);

  const empty = cutSummary({ total: 0, by_reason: {} });
  assert.deepEqual(empty, { total: 0, hours: 24, reasons: [] });
  assert.deepEqual(cutSummary(undefined), { total: 0, hours: 24, reasons: [] });
});

test('allThreads follows next to the end: a hub with more than ten pages shows every thread', async () => {
  const total = 2345, calls = [];
  const api = {
    threads: async ({ limit, cursor }) => {
      calls.push({ limit, cursor });
      const from = cursor ? Number(cursor) : 0;
      const threads = Array.from({ length: Math.min(limit, total - from) }, (_, i) => ({ thread: { thread_id: 'thr_' + (from + i) } }));
      const next = from + limit < total ? String(from + limit) : null;
      return { threads, next };
    },
  };
  const { threads } = await allThreads(api, 200);
  assert.equal(threads.length, total);
  assert.equal(calls.length, 12);
  assert.ok(calls.every(c => c.limit === 200), 'each request stays bounded');
  // A cursor the hub repeats ends the walk instead of looping.
  const stuck = { threads: async () => ({ threads: [{}], next: 'same' }) };
  assert.equal((await allThreads(stuck, 200)).threads.length, 2);
});
