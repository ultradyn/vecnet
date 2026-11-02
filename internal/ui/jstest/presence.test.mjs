import test from 'node:test';
import assert from 'node:assert/strict';
import { HEARTBEAT_MS, heartbeatAction, navLabel, toggleTarget, wireTime } from '../static/presence-model.js';

const me = presence => ({ peer_id: 'peer_op', name: 'max', presence, presence_at: '2026-11-02T10:00:00.000Z' });

test('heartbeatAction: working → repost working; unknown → nothing; offline → nothing', () => {
  // The heartbeat re-posts what the hub says, never a local choice (PROTOCOL §8).
  assert.equal(heartbeatAction(me('working')), 'working');
  assert.equal(heartbeatAction(me('idle')), 'idle');
  assert.equal(heartbeatAction(me('blocked')), 'blocked');
  // Stale presence reads unknown and is never re-asserted; offline is a choice
  // the heartbeat must not keep alive either.
  assert.equal(heartbeatAction(me('unknown')), null);
  assert.equal(heartbeatAction(me('offline')), null);
  assert.equal(heartbeatAction(me('')), null);
  assert.equal(heartbeatAction(me(undefined)), null);
  assert.equal(heartbeatAction(me('Working')), null);
  assert.equal(heartbeatAction(null), null);
  assert.equal(heartbeatAction(undefined), null);
  // Often enough to beat the 1 h presence TTL twice over.
  assert.equal(HEARTBEAT_MS, 20 * 60 * 1000);
});

test('toggleTarget: idle ↔ working; blocked or unknown → working', () => {
  assert.equal(toggleTarget('idle'), 'working');
  assert.equal(toggleTarget('working'), 'idle');
  assert.equal(toggleTarget('blocked'), 'working');
  assert.equal(toggleTarget('unknown'), 'working');
  assert.equal(toggleTarget('offline'), 'working');
  assert.equal(toggleTarget(undefined), 'working');
});

test('navLabel never renders unknown as offline', () => {
  assert.equal(navLabel('max', 'working'), 'max is working');
  assert.equal(navLabel('max', 'idle'), 'max is idle');
  assert.equal(navLabel('max', 'offline'), 'max is offline');
  assert.equal(navLabel('max', 'unknown'), 'max · presence unknown');
  assert.equal(navLabel('max', ''), 'max');
  assert.equal(navLabel('max', undefined), 'max');
});

test('wireTime is the hub timestamp form: UTC with milliseconds', () => {
  assert.equal(wireTime(Date.parse('2026-11-02T10:04:05.678Z')), '2026-11-02T10:04:05.678Z');
  assert.match(wireTime(Date.parse('2026-11-02T10:04:05Z')), /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/);
});
