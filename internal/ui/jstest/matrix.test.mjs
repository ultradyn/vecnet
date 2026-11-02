import test from 'node:test';
import assert from 'node:assert/strict';
import { grid, merge } from '../static/matrix-model.js';

const cell = (recipient, state, extra = {}) => ({
  recipient, delivery_id: 'dly_' + recipient, state, display: state, updated_at: '2026-11-02T06:12:00.000Z',
  attempt: 1, last_status: null, read_at: null, ...extra,
});

test('grid aligns cells to recipients; missing → none; sender → self', () => {
  const read = cell('peer_c', 'accepted', { display: 'read', read_at: '2026-11-02T06:13:00.000Z' });
  const rows = grid({
    recipients: ['peer_a', 'peer_b', 'peer_c', 'peer_d'],
    rows: [
      // Cells arrive out of recipient order and one recipient has none.
      { message_id: 'msg_1', created_at: '2026-11-02T06:12:00.000Z', sender: 'peer_b', excerpt: 'first', cells: [read, cell('peer_a', 'processed')] },
      { message_id: 'msg_2', created_at: '2026-11-02T06:14:00.000Z', sender: 'peer_x', excerpt: 'second', cells: [cell('peer_d', 'dead_letter'), cell('peer_b', 'queued')] },
    ],
    next_after: null,
  });
  assert.equal(rows.length, 2);
  assert.deepEqual(rows.map(r => r.cells.length), [4, 4]);
  assert.deepEqual(rows[0].cells, [
    { state: 'processed', cell: cell('peer_a', 'processed') },
    { state: 'self' },
    { state: 'read', cell: read },
    { state: 'none' },
  ]);
  // A sender that is not a recipient has no self cell; gaps are none.
  assert.deepEqual(rows[1].cells, [
    { state: 'none' },
    { state: 'queued', cell: cell('peer_b', 'queued') },
    { state: 'none' },
    { state: 'dead_letter', cell: cell('peer_d', 'dead_letter') },
  ]);
  // The row keeps what the first column shows.
  assert.equal(rows[0].message_id, 'msg_1');
  assert.equal(rows[0].sender, 'peer_b');
  assert.equal(rows[0].excerpt, 'first');
  assert.equal(rows[1].created_at, '2026-11-02T06:14:00.000Z');
  // display wins over state, and a cell without display falls back to state.
  const noDisplay = { ...cell('peer_a', 'pushed'), display: undefined };
  assert.equal(grid({ recipients: ['peer_a'], rows: [{ message_id: 'msg_3', sender: 'peer_b', cells: [noDisplay] }] })[0].cells[0].state, 'pushed');
});

test('grid keeps zero-cell hub messages as a row', () => {
  const rows = grid({
    recipients: ['peer_a', 'peer_b'],
    rows: [
      { message_id: 'msg_1', created_at: '2026-11-02T06:12:00.000Z', sender: 'peer_a', excerpt: 'hello', cells: [cell('peer_b', 'accepted')] },
      { message_id: 'msg_hub', created_at: '2026-11-02T06:20:00.000Z', sender: 'hub', excerpt: 'approval expired', cells: [] },
    ],
  });
  assert.equal(rows.length, 2);
  assert.equal(rows[1].message_id, 'msg_hub');
  assert.deepEqual(rows[1].cells, [{ state: 'none' }, { state: 'none' }]);
  // An empty or missing matrix is no rows, never a throw.
  assert.deepEqual(grid(null), []);
  assert.deepEqual(grid({ recipients: [], rows: [] }), []);
});

test('merge appends a page, unions recipients in order and takes its next_after', () => {
  const a = { recipients: ['peer_a', 'peer_b'], rows: [{ message_id: 'msg_1', cells: [] }, { message_id: 'msg_2', cells: [] }], next_after: 'msg_2' };
  const b = { recipients: ['peer_b', 'peer_c'], rows: [{ message_id: 'msg_2', cells: [cell('peer_c', 'queued')] }, { message_id: 'msg_3', cells: [] }], next_after: null };
  const m = merge(a, b);
  assert.deepEqual(m.recipients, ['peer_a', 'peer_b', 'peer_c']);
  // A row seen on both pages keeps the newer copy, once, in place.
  assert.deepEqual(m.rows.map(r => r.message_id), ['msg_1', 'msg_2', 'msg_3']);
  assert.equal(m.rows[1].cells.length, 1);
  assert.equal(m.next_after, null);
  assert.deepEqual(merge(null, a), { recipients: ['peer_a', 'peer_b'], rows: a.rows, next_after: 'msg_2' });
});
