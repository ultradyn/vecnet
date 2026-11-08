import test from 'node:test';
import assert from 'node:assert/strict';
import { activitySnapshot, peerActivity, sparklineGeometry, bucketLabel } from '../static/sparkline-model.js';

const zeros = () => Array(60).fill(0);
function stats(entries = []) {
  const buckets = zeros();
  for (const [index, count] of entries) buckets[index] = count;
  const messages = buckets.reduce((a, b) => a + b, 0);
  return { messages, messages_per_minute: messages / 60, buckets };
}
function fixture() {
  return {
    now: '2026-11-09T12:34:56.789Z',
    window_start: '2026-11-09T11:34:56.789Z',
    window_end: '2026-11-09T12:34:56.789Z', bucket_seconds: 60,
    ...stats([[0, 3], [59, 2]]), active_peers: 1,
    peers: { peer_a: stats([[0, 2]]), source_x: stats([[0, 1]]), hub_x: stats([[59, 2]]) },
  };
}

// Catches dropping source/hub counts or deriving activity from presence/names.
test('snapshot preserves the complete rolling-hour contract and copies data', () => {
  const input = fixture();
  const snapshot = activitySnapshot(input);
  assert.ok(snapshot);
  assert.equal(snapshot.messages, 5);
  assert.equal(snapshot.messages_per_minute, 1 / 12);
  assert.equal(snapshot.active_peers, 1);
  assert.equal(snapshot.bucket_seconds, 60);
  assert.equal(snapshot.now, '2026-11-09T12:34:56.789Z');
  assert.equal(snapshot.window_start, '2026-11-09T11:34:56.789Z');
  assert.equal(snapshot.window_end, snapshot.now);
  assert.equal(snapshot.buckets.length, 60);
  assert.equal(snapshot.buckets[0], 3);
  assert.equal(snapshot.buckets[59], 2);
  assert.equal(snapshot.peers.source_x.messages, 1);
  assert.equal(snapshot.peers.hub_x.messages, 2);
  input.buckets[0] = 999;
  input.peers.peer_a.buckets[0] = 999;
  assert.equal(snapshot.buckets[0], 3);
  assert.equal(snapshot.peers.peer_a.buckets[0], 2);
});

test('a successful empty snapshot is zero, but missing or invalid data is null', () => {
  const input = { ...fixture(), ...stats(), active_peers: 0, peers: {} };
  assert.ok(activitySnapshot(input));
  assert.deepEqual(activitySnapshot(input).buckets, zeros());
  for (const value of [null, undefined, false, 0, '', [], {}, { ...input, peers: null }]) {
    assert.equal(activitySnapshot(value), null);
  }
});

// Each mutation breaks a separate integrity constraint; none may become invented zero.
test('snapshot rejects invalid time, counts, rates and sender aggregates', () => {
  const cases = [
    v => { v.now = 'not a date'; },
    v => {
      v.now = v.window_end = '2026-02-30T12:34:56.789Z';
      v.window_start = '2026-02-30T11:34:56.789Z';
    },
    v => { v.window_start = '2026-11-09T11:34:56.788Z'; },
    v => { v.window_end = '2026-11-09T12:34:56.790Z'; },
    v => { v.now = 1794227696789; },
    v => { v.bucket_seconds = 59; },
    v => { v.buckets.pop(); },
    v => { v.buckets.push(0); },
    v => { v.buckets = new Array(2 ** 32 - 1); },
    v => { delete v.buckets[5]; },
    v => { v.buckets[5] = -1; },
    v => { v.buckets[5] = 0.5; },
    v => { v.buckets[5] = Infinity; },
    v => { v.buckets[5] = '0'; },
    v => { v.messages = 6; },
    v => { v.messages = NaN; },
    v => { v.messages_per_minute = 5; },
    v => { v.messages_per_minute = -1; },
    v => { v.messages_per_minute = Infinity; },
    v => { v.active_peers = 2; },
    v => { v.active_peers = 0.5; },
    v => { delete v.peers.hub_x; },
    v => { v.peers.peer_a.messages = 3; },
    v => { v.peers.peer_a.messages_per_minute = 2; },
    v => { v.peers.peer_a.buckets[0] = -2; },
    v => { v.peers.peer_a.buckets = []; },
    v => { v.peers.peer_a = null; },
    v => { v.peers = []; },
    v => { v.peers.peer_a.buckets[0] = 0; v.peers.peer_a.buckets[1] = 2; },
  ];
  for (const mutate of cases) {
    const input = fixture();
    mutate(input);
    assert.equal(activitySnapshot(input), null, mutate.toString());
  }
});

test('snapshot accepts JSON rate rounding without accepting a materially wrong rate', () => {
  const input = fixture();
  input.messages_per_minute = 0.08333333333333334;
  input.peers.source_x.messages_per_minute = 0.01666666666666667;
  assert.ok(activitySnapshot(JSON.parse(JSON.stringify(input))));
  input.messages_per_minute += 0.0001;
  assert.equal(activitySnapshot(input), null);
});

test('snapshot does not throw on throwing accessors or revoked proxies', () => {
  const throwing = Object.defineProperty({}, 'now', { get() { throw new Error('hostile'); } });
  const { proxy, revoke } = Proxy.revocable({}, {});
  revoke();
  for (const value of [throwing, proxy, Symbol('bad'), 1n]) assert.equal(activitySnapshot(value), null);
});

test('peer lookup joins exact immutable IDs, never display names or prototype keys', () => {
  const input = fixture();
  input.peers = JSON.parse(JSON.stringify({ peer_a: stats([[0, 2]]), source_x: stats([[0, 1]]), hub_x: stats([[59, 2]]) }));
  Object.defineProperty(input.peers, '__proto__', { value: stats(), enumerable: true });
  input.peers.constructor = stats();
  const snapshot = activitySnapshot(input);
  assert.ok(snapshot);
  assert.equal(Object.getPrototypeOf(snapshot.peers), null);
  assert.equal(peerActivity(snapshot, 'peer_a'), snapshot.peers.peer_a);
  assert.equal(peerActivity(snapshot, '__proto__'), snapshot.peers.__proto__);
  assert.equal(peerActivity(snapshot, 'constructor'), snapshot.peers.constructor);
  for (const id of ['Same display name', 'peer_A', 'toString', 'peer_archived']) {
    const absent = peerActivity(snapshot, id);
    assert.equal(absent.messages, 0);
    assert.equal(absent.messages_per_minute, 0);
    assert.deepEqual(absent.buckets, zeros());
    absent.buckets[0] = 999;
    assert.equal(peerActivity(snapshot, id).buckets[0], 0);
  }
  assert.equal(peerActivity(null, 'peer_a'), null);
});

const coordinates = result => result.points ? result.points.split(' ').map(pair => pair.split(',').map(Number)) : [];

test('geometry handles empty, singleton and 60 zero buckets on the zero baseline', () => {
  assert.deepEqual(sparklineGeometry([]), { points: '', area: '', baseline: 44, max: 0 });
  assert.deepEqual(sparklineGeometry([0], { width: 28, height: 18 }), {
    points: '4,14', area: 'M 4,14 L 4,14 L 4,14 Z', baseline: 14, max: 0,
  });
  const result = sparklineGeometry(zeros());
  const points = coordinates(result);
  assert.equal(points.length, 60);
  assert.deepEqual(points[0], [4, 44]);
  assert.deepEqual(points[59], [236, 44]);
  assert.ok(points.every(([, y]) => y === 44));
});

test('geometry is linear, baseline-anchored and closed for constant and burst values', () => {
  const constant = sparklineGeometry([5, 5, 5], { width: 28, height: 18 });
  assert.equal(constant.points, '4,4 14,4 24,4');
  assert.equal(constant.area, 'M 4,14 L 4,4 L 14,4 L 24,4 L 24,14 Z');
  const burst = sparklineGeometry([0, 5, 0], { width: 28, height: 18 });
  assert.equal(burst.points, '4,14 14,4 24,14');
  assert.equal(burst.max, 5);
});

test('a shared explicit maximum keeps peer magnitudes comparable and clamps larger values', () => {
  assert.equal(sparklineGeometry([0, 5], { max: 10 }).points, '4,44 236,24');
  assert.equal(sparklineGeometry([0, 10], { max: 10 }).points, '4,44 236,4');
  assert.equal(sparklineGeometry([0, 100], { max: 10 }).points, '4,44 236,4');
  assert.equal(sparklineGeometry([5], { max: 0 }).points, '4,44');
});

test('geometry sanitizes nonnumeric, nonfinite and negative values without coercion', () => {
  const input = [NaN, Infinity, -1, '5', null, Symbol('bad'), {}, 5];
  const result = sparklineGeometry(input);
  assert.deepEqual(coordinates(result).map(([, y]) => y), [44, 44, 44, 44, 44, 44, 44, 4]);
  assert.equal(input[2], -1);
  for (const value of [null, undefined, {}, 'bad']) assert.equal(sparklineGeometry(value).points, '');
});

test('huge finite values and invalid dimensions never produce nonfinite coordinates', () => {
  for (const options of [{}, { width: Number.MAX_VALUE, height: Number.MAX_VALUE }, { width: -1, height: NaN, max: Infinity }, { width: 0, height: 0 }]) {
    const result = sparklineGeometry([0, Number.MAX_VALUE / 2, Number.MAX_VALUE], options);
    assert.ok(coordinates(result).flat().every(Number.isFinite));
    assert.ok(Number.isFinite(result.baseline));
    assert.ok(!/NaN|Infinity/.test(result.area));
    assert.equal(coordinates(result).length, 3);
  }
  assert.deepEqual(coordinates(sparklineGeometry([0, Number.MAX_VALUE / 2, Number.MAX_VALUE])), [[4, 44], [120, 24], [236, 4]]);
});

test('bucket labels use exact half-open UTC minute intervals from hub time, not client time', () => {
  const snapshot = activitySnapshot(fixture());
  assert.deepEqual(bucketLabel(snapshot, 0), {
    start: '2026-11-09T11:34:56.789Z', end: '2026-11-09T11:35:56.789Z',
    label: '[2026-11-09T11:34:56.789Z, 2026-11-09T11:35:56.789Z) UTC — messages in this minute',
  });
  assert.equal(bucketLabel(snapshot, 59).start, '2026-11-09T12:33:56.789Z');
  assert.equal(bucketLabel(snapshot, 59).end, snapshot.window_end);
  for (const index of [-1, 60, 0.5, '0', NaN]) assert.equal(bucketLabel(snapshot, index), null);
  assert.equal(bucketLabel(null, 0), null);
});
