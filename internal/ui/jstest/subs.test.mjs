import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { chip, expiresIn, TONES } from '../static/subs-model.js';

// The hub's health values, read from internal/opapi so a tenth value added
// there fails here until the web learns it (PROTOCOL §2.13).
function opapiHealthValues() {
  const src = readFileSync(new URL('../../opapi/types.go', import.meta.url), 'utf8');
  return [...src.matchAll(/^\s*Health[A-Z]\w*\s*=\s*"([a-z_]+)"/gm)].map(m => m[1]);
}

test('chip covers every opapi health value; unknown value falls back to neutral', () => {
  const values = opapiHealthValues();
  assert.deepEqual([...values].sort(), [
    'expired', 'failing', 'late', 'ok', 'paused_auth', 'paused_failed', 'paused_operator', 'paused_token', 'unverified',
  ]);
  const labels = new Set();
  for (const health of values) {
    const c = chip(health);
    assert.equal(typeof c.label, 'string', health);
    assert.ok(c.label.length > 0, `${health} has no label`);
    assert.ok(TONES.includes(c.tone), `${health} tone ${c.tone}`);
    // A known value is never the unknown fallback: it must not read as neutral.
    assert.notEqual(c.tone, 'neutral', `${health} fell back to neutral`);
    labels.add(c.label);
  }
  assert.equal(labels.size, values.length, 'two health values share a label');

  // Review Focus 5: every paused state reads as paused, never as healthy.
  for (const health of values.filter(v => v.startsWith('paused_'))) {
    assert.match(chip(health).label, /^paused\b/, `${health} does not read as paused`);
    assert.notEqual(chip(health).tone, 'ok', `${health} reads as healthy`);
  }
  assert.equal(chip('ok').tone, 'ok');
  for (const health of values.filter(v => v !== 'ok')) assert.notEqual(chip(health).tone, 'ok', `${health} reads as healthy`);

  // Unknown, empty and missing values stay neutral: neither healthy nor broken.
  for (const health of ['paused_martian', 'OK', '', undefined, null]) {
    assert.equal(chip(health).tone, 'neutral', `unknown ${health}`);
  }
  assert.equal(chip('paused_martian').label, 'paused_martian');
  assert.equal(chip('').label, 'unknown');
  assert.equal(chip(undefined).label, 'unknown');
  assert.equal(chip('<img src=x onerror=alert(1)>').label, '<img src=x onerror=alert(1)>');
});

test('expiresIn uses hub clock; past → "expired 3m ago"', () => {
  const hub = Date.parse('2026-11-02T10:00:00.000Z');
  const at = ms => new Date(hub + ms).toISOString();

  assert.equal(expiresIn(at(-3 * 60_000), hub), 'expired 3m ago');
  assert.equal(expiresIn(at(-42_000), hub), 'expired 42s ago');
  assert.equal(expiresIn(at(-2 * 3_600_000), hub), 'expired 2h ago');
  assert.equal(expiresIn(at(-3 * 86_400_000), hub), 'expired 3d ago');
  // The hub calls a subscription expired at its expiry instant.
  assert.equal(expiresIn(at(0), hub), 'expired just now');

  assert.equal(expiresIn(at(30_000), hub), 'in 30s');
  assert.equal(expiresIn(at(2 * 60_000), hub), 'in 2m');
  assert.equal(expiresIn(at(59 * 60_000 + 59_000), hub), 'in 59m');
  assert.equal(expiresIn(at(60 * 60_000), hub), 'in 1h 0m');
  assert.equal(expiresIn(at(5 * 3_600_000 + 7 * 60_000), hub), 'in 5h 7m');
  assert.equal(expiresIn(at(2 * 86_400_000 + 3_600_000), hub), 'in 2d 1h');

  // The client's own clock plays no part: a client minutes out still agrees
  // with the hub (Review Focus 3).
  const skewed = hub + 7 * 60_000;
  assert.equal(expiresIn(at(5 * 60_000), hub), 'in 5m');
  assert.equal(expiresIn(at(5 * 60_000), skewed), 'expired 2m ago');

  assert.equal(expiresIn(null, hub), '');
  assert.equal(expiresIn(undefined, hub), '');
  assert.equal(expiresIn('not a time', hub), '');
  assert.equal(expiresIn(at(60_000), NaN), '');
});
