import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { CODE_STEP, DOT_NOTE, DOT_STEPS, dotLink, invite, inviteBlock, KINDS, slug, TTLS } from '../static/invite-model.js';
import { KIND_LABELS, kindLabel } from '../static/format.js';

// The golden the Go CLI is tested against too (internal/cli/invite_internal_test.go).
const fx = JSON.parse(readFileSync(new URL('../../cli/testdata/invite.json', import.meta.url), 'utf8'));
const asInput = c => ({ owner: c.owner, hubURL: c.hub_url, profile: c.profile, token: c.token, expires: c.expires_at, open: c.open, name: c.name });

test('slug and inviteBlock match the CLI golden exactly', () => {
  for (const c of fx.slugs) assert.equal(slug(c.name), c.profile, c.name);
  assert.ok(fx.blocks.some(c => c.open) && fx.blocks.some(c => !c.open));
  for (const c of fx.blocks) {
    const got = inviteBlock(asInput(c));
    assert.equal(got, c.block, c.profile);
    // Short, and one plain pipeline: shell guards refuse $, ; and friends.
    assert.ok(got.split('\n').length <= 6, c.profile);
    assert.ok(!/[$;<>&]/.test(got), c.profile);
  }
});

test('invite renders the panel from the hub answer, preferring its public URL', () => {
  const c = fx.blocks.find(x => !x.open && x.owner);
  const res = { peer_id: 'peer_1', name: 'Scout', name_open: false, enrol_token: c.token, enrol_expires_at: c.expires_at, hub_url: c.hub_url, hub_owner: c.owner };
  const got = invite(res, 'http://127.0.0.1:9', false);
  assert.equal(got.text, c.block);
  assert.equal(got.profile, 'scout');
  assert.equal(got.peerId, 'peer_1');
  assert.ok(invite({ ...res, hub_url: '' }, 'http://h:1', false).text.includes('--hub http://h:1 '));
  const o = fx.blocks.find(x => x.open && x.name);
  const open = invite({ ...res, name: o.name, name_open: true, enrol_token: o.token, enrol_expires_at: o.expires_at, hub_url: o.hub_url, hub_owner: o.owner }, 'http://x', true);
  assert.equal(open.text, o.block);
  assert.ok(open.open);
});

test('the form offers dot but never operator or source, and TTLs stay within 7 days', () => {
  assert.ok(!KINDS.includes('operator') && !KINDS.includes('source') && KINDS[0] === 'session');
  assert.ok(KINDS.includes('dot'));
  assert.ok(TTLS.every(([, s]) => s >= 60 && s <= 604800));
});

test('every offered kind has a human label; the raw value is kept for the API', () => {
  assert.deepEqual(KINDS.map(kindLabel), ['Agent session (Claude Code, Codex, CLI)', 'Launcher', 'OpenClaw', 'pagchat bot', 'Grok Bot', 'ChatGPT dot']);
  for (const k of KINDS) assert.ok(Object.hasOwn(KIND_LABELS, k), k);
});

test('dotLink points ChatGPT at the hub public /mcp, else the page origin', () => {
  const res = { peer_id: 'peer_dot', name: 'scout', enrol_token: '', enrol_expires_at: '', hub_url: 'https://vecnet.example', hub_owner: 'Max' };
  assert.deepEqual(dotLink(res, 'http://127.0.0.1:9'), { peerId: 'peer_dot', name: 'scout', mcpURL: 'https://vecnet.example/mcp' });
  assert.equal(dotLink({ ...res, hub_url: 'https://h.example//' }, 'x').mcpURL, 'https://h.example/mcp');
  assert.equal(dotLink({ ...res, hub_url: '' }, 'http://h:1').mcpURL, 'http://h:1/mcp');
});

test('the dot steps say where to click, how to link, and how to follow events', () => {
  const text = DOT_STEPS.map(parts => parts.map(p => typeof p === 'string' ? p : p.b).join(''));
  assert.equal(text.length, 4);
  assert.match(text[0], /Plugins.*\+.*Add custom MCP server/);
  assert.match(text[1], /OAuth.*plugin/);
  assert.match(text[CODE_STEP], /link code/);
  assert.match(text[3], /trigger vecnet, event vecnet\.message and no settings/);
  assert.match(text[3], /plugin page.*click into its card.*Rescan/);
  assert.match(text[3], /Advanced.*Runs in: Same chat for every run/);
  assert.match(text[3], /vecnet_guide.*recommended task instruction/);
  assert.match(text[3], /One trigger block.*whole inbox.*ORed/);
  assert.match(DOT_NOTE, /new chat or rescan/);
});
