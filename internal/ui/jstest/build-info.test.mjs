import test from 'node:test';
import assert from 'node:assert/strict';
import { buildInfoLine, timeAgo } from '../static/build-info.js';

test('timeAgo humanizes boundaries and invalid dates', () => {
  const now = Date.parse('2026-11-07T12:00:00Z');
  assert.equal(timeAgo('2026-11-07T11:59:57Z', now), 'just now');
  assert.equal(timeAgo('2026-11-07T11:57:00Z', now), '3 minutes ago');
  assert.equal(timeAgo('2026-11-07T09:00:00Z', now), '3 hours ago');
  assert.equal(timeAgo('not-a-date', now), 'unknown');
});

test('buildInfoLine includes dirty version, hash, date, and age', () => {
  const line = buildInfoLine({
    version: '2026.1106.2049+8877c73.dirty', commit: '8877c73',
    commit_date: '2026-11-06T20:49:00Z',
  }, Date.parse('2026-11-06T23:49:00Z'));
  assert.equal(line, 'vecnet 2026.1106.2049+8877c73.dirty · 8877c73 · 2026-11-06 · 3 hours ago');
  assert.equal(buildInfoLine(null, 0), 'vecnet dev · unknown · unknown · unknown');
});
