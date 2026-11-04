import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { invite, inviteBlock, KINDS, slug, TTLS } from '../static/invite-model.js';

// The golden the Go CLI is tested against too (internal/cli/invite_internal_test.go).
const cases = JSON.parse(readFileSync(new URL('../../cli/testdata/invite.json', import.meta.url), 'utf8'));

test('slug and inviteBlock match the CLI golden exactly', () => {
  assert.ok(cases.length >= 5);
  for (const c of cases) {
    assert.equal(slug(c.name), c.profile, c.name);
    assert.equal(inviteBlock(c.hub_url, c.profile, c.token, c.expires_at), c.block, c.name);
  }
});

test('invite prefers the hub public URL over the page origin', () => {
  const c = cases[0];
  const res = { peer_id: 'peer_1', enrol_token: c.token, enrol_expires_at: c.expires_at, hub_url: c.hub_url };
  const got = invite(c.name, res, 'http://127.0.0.1:9');
  assert.equal(got.text, c.block);
  assert.equal(got.peerId, 'peer_1');
  assert.ok(invite(c.name, { ...res, hub_url: '' }, 'http://h:1').text.includes('--hub http://h:1 '));
});

test('the form never offers operator or dot, and TTLs stay within 7 days', () => {
  assert.ok(!KINDS.includes('operator') && !KINDS.includes('dot') && KINDS[0] === 'session');
  assert.ok(TTLS.every(([, s]) => s >= 60 && s <= 604800));
});
