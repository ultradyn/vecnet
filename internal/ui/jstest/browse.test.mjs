import test from 'node:test';
import assert from 'node:assert/strict';
import { filterPeers, readPeerFilters, savePeerFilters } from '../static/peers-model.js';
import { threadQuery, createThreadList, bulkArchive, participantID } from '../static/threads-model.js';
const row = (id, extra = {}) => ({ peer: { peer_id: id, name: id, kind: 'session', presence: 'idle', last_seen: null }, ...extra });
test('peer browse hides disabled and archived independently, searches IDs and sorts with a stable bound', () => {
 const rows = [row('peer_z'), row('peer_disabled', { disabled: true }), row('peer_old', { disabled: true, archived_at: 'now' }), row('peer_a')];
 assert.deepEqual(filterPeers(rows).shown.map(p => p.id), ['peer_a', 'peer_z']);
 assert.equal(filterPeers(rows).disabled, 1); assert.equal(filterPeers(rows).archived, 1);
 assert.equal(filterPeers(rows, { showArchived: true }).shown.length, 3);
 assert.equal(filterPeers(rows, { showDisabled: true }).shown.length, 3);
 assert.equal(filterPeers(rows, { q: 'Z', kind: 'session', presence: 'idle', limit: 1 }).shown[0].id, 'peer_z');
 assert.equal(filterPeers(rows, { kind: 'source' }).shown.length, 0);
});
test('peer visibility persists safely and ignores malformed storage', () => {
 let value; const storage = { getItem: () => value, setItem: (_, v) => { value = v; } };
 savePeerFilters(storage, { showDisabled: true, showArchived: false });
 assert.deepEqual(readPeerFilters(storage), { showDisabled: true, showArchived: false });
 value = '{'; assert.deepEqual(readPeerFilters(storage), { showDisabled: false, showArchived: false });
});
test('thread query follows server contract and mine wins over participant', () => {
 assert.deepEqual(threadQuery({ q: 'Hi', peer: 'peer_x', state: 'open' }, 'peer_me'), { limit: 50, archived: false, q: 'Hi', participant: 'peer_x' });
 assert.deepEqual(threadQuery({ state: 'all', mine: true }, 'peer_me', 'cursor'), { limit: 50, participant: 'peer_me', cursor: 'cursor' });
 assert.equal(threadQuery({ state: 'archived' }, '').archived, true);
});
test('thread list drops stale responses and appends cursor pages without duplicates', async () => {
 const pending = []; const api = { threads: q => new Promise(resolve => pending.push({ q, resolve })) };
 const list = createThreadList(api, 'peer_me');
 const first = list.load({ q: 'old' }); const second = list.load({ q: 'new' });
 pending[1].resolve({ threads: [{ thread: { thread_id: 'new' } }], next: '2' }); await second;
 pending[0].resolve({ threads: [{ thread: { thread_id: 'old' } }], next: null }); await first;
 assert.equal(list.state().rows[0].thread.thread_id, 'new');
 const more = list.more(); assert.equal(pending[2].q.cursor, '2');
 pending[2].resolve({ threads: [{ thread: { thread_id: 'new' } }, { thread: { thread_id: 'next' } }], next: null }); await more;
 assert.equal(list.state().rows.length, 2); list.close();
});
test('bulk archive is sequential and reports failures per thread', async () => {
 const calls = []; const failures = await bulkArchive({ archiveThread: async id => { calls.push(id); if (id === 'bad') throw Error('denied'); } }, ['a', 'bad', 'b'], true);
 assert.deepEqual(calls, ['a', 'bad', 'b']); assert.deepEqual(failures, [{ id: 'bad', error: 'denied' }]);
});

test('participant resolution reads the latest directory and rejects unknown text', () => {
 assert.equal(participantID('Alice', [{ peer_id: 'peer_a', name: 'Alice' }]), 'peer_a');
 assert.equal(participantID('peer_a', [{ peer_id: 'peer_a', name: 'Alice' }]), 'peer_a');
 assert.equal(participantID('nobody', []), null); assert.equal(participantID('', []), '');
});
test('recent peers compare timestamps numerically and ignore future self-reported presence', () => {
 const rows = [row('a'), row('b'), row('future')];
 rows[0].peer.last_seen = '2026-11-15T01:00:00Z'; rows[0].presence_at = '2099-01-01T00:00:00Z';
 rows[1].peer.last_seen = '2026-11-15T01:30:00+00:00'; rows[2].presence_at = '2099-01-01T00:00:00Z';
 assert.deepEqual(filterPeers(rows, { now: Date.parse('2026-11-15T02:00:00Z') }).shown.map(p => p.id), ['b', 'a', 'future']);
});
