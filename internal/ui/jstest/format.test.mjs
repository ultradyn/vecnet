import test from 'node:test';
import assert from 'node:assert/strict';
import { ago, bytes, quotaLabel, short, title, when } from '../static/format.js';

test('when is 24-hour local today, with the date on other days, and survives midnight', () => {
  const noon = new Date(2026, 10, 2, 14, 5, 0).getTime();
  assert.equal(when(new Date(2026, 10, 2, 14, 5, 0).toISOString(), noon), '14:05');
  assert.equal(when(new Date(2026, 10, 2, 0, 7, 0).toISOString(), noon), '00:07');
  assert.equal(when(new Date(2026, 9, 31, 9, 30, 0).toISOString(), noon), '31 Oct 09:30');
  assert.equal(when('not a time', noon), '');
  assert.equal(when(null, noon), '');
});

test('ago counts back from the supplied clock', () => {
  const now = Date.parse('2026-11-02T10:00:00.000Z');
  const back = ms => new Date(now - ms).toISOString();
  assert.equal(ago(back(0), now), 'now');
  assert.equal(ago(back(-5000), now), 'now');
  assert.equal(ago(back(42_000), now), '42s ago');
  assert.equal(ago(back(5 * 60_000), now), '5m ago');
  assert.equal(ago(back(3 * 3_600_000), now), '3h ago');
  assert.equal(ago(back(2 * 86_400_000), now), '2d ago');
  assert.equal(ago('nope', now), '');
  assert.equal(ago(null, now), '');
});

test('bytes uses binary units', () => {
  assert.equal(bytes(0), '0 B');
  assert.equal(bytes(1023), '1023 B');
  assert.equal(bytes(1536), '1.5 KiB');
  assert.equal(bytes(5 << 20), '5.0 MiB');
});

test('short keeps the type prefix and the tail', () => {
  assert.equal(short('peer_01JD3Q2K8M4N6P8R0T2V4X6Z8B'), 'peer_…X6Z8B');
  assert.equal(short('thr_1'), 'thr_1');
  assert.equal(short(''), '?');
  assert.equal(short(undefined), '?');
});

test('title prefers the thread title, then the other participants, then the id', () => {
  const nameOf = id => ({ peer_me: 'max', peer_a: 'codex', peer_b: 'grok' })[id];
  assert.equal(title({ title: 'Hosting', participants: ['peer_a'], thread_id: 'thr_1' }, 'peer_me', nameOf), 'Hosting');
  assert.equal(title({ title: '', participants: ['peer_me', 'peer_a', 'peer_b'], thread_id: 'thr_1' }, 'peer_me', nameOf), 'codex, grok');
  assert.equal(title({ title: null, participants: ['peer_me'], thread_id: 'thr_01JD3S2C4E6G8J0M2P4R6T8V0X' }, 'peer_me', nameOf), 'thr_…T8V0X');
  assert.equal(title(null, 'peer_me', nameOf), '');
});

test('quotaLabel reads a *_pct value as a percentage, an empty quota as none, anything else as k=v', () => {
  assert.equal(quotaLabel({ weekly_remaining_pct: 61 }), '61%');
  assert.equal(quotaLabel({ weekly_remaining_pct: 0 }), '0%');
  assert.equal(quotaLabel({ weekly_remaining_pct: 60.6 }), '61%');
  assert.equal(quotaLabel({ weekly_remaining_pct: 3, runs_this_hour: '0/200' }), '3%');
  assert.equal(quotaLabel({ daily_used_pct: 12, burst: 4 }), '12%');
  assert.equal(quotaLabel({ a_pct: 9, weekly_remaining_pct: 40 }), '40%');
  assert.equal(quotaLabel({}), 'none');
  assert.equal(quotaLabel(null), 'none');
  assert.equal(quotaLabel(undefined), 'none');
  assert.equal(quotaLabel({ runs_this_hour: '0/200', tier: 2 }), 'runs_this_hour=0/200 tier=2');
  assert.equal(quotaLabel({ weekly_remaining_pct: 'lots' }), 'weekly_remaining_pct=lots');
});
