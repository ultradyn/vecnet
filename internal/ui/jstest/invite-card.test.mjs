import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createApi } from '../static/api.js';
import { invite, reinvite, slug } from '../static/invite-model.js';
import { inviteCard, peerFacts } from '../static/peer-model.js';
import { invitePanel } from '../static/invite.js';

// The peer page's Invite card (#67): New invite hands the agent the very
// paste block Add agent and `vecnet peer invite <peer>` do, pinned by the CLI
// golden; the card shows only for a peer that enrols with a token and holds
// no live credential; and the token is never in an attribute, a URL or a
// request other than the one answer that carries it.
const fx = JSON.parse(readFileSync(new URL('../../cli/testdata/invite.json', import.meta.url), 'utf8'));

// answer is the POST /v1/admin/peers/{id}/enrol-token answer that golden
// block c came from: the hub's name for the peer slugs to c.profile.
function answer(c) {
  const named = fx.slugs.find(s => s.profile === c.profile);
  const name = c.open && c.name ? c.name : named ? named.name : c.profile;
  assert.equal(slug(name), c.profile, 'fixture name for ' + c.profile);
  return { peer_id: 'peer_01EXAMPLE', name, name_open: c.open, principal_id: 'prn_1', enrol_token: c.token, enrol_expires_at: c.expires_at, hub_url: c.hub_url, hub_owner: c.owner };
}

test('a re-issued invite is byte-for-byte the CLI golden and the Add agent text', () => {
  assert.ok(fx.blocks.some(c => c.open && c.name) && fx.blocks.some(c => c.open && !c.name) && fx.blocks.some(c => !c.open));
  for (const c of fx.blocks) {
    const res = answer(c);
    const card = reinvite(res, 'http://page.example');
    assert.equal(card.text, c.block, c.profile);
    // Add agent renders the same answer the same way: the wizard's
    // "suggested" is the operator having typed a name, which for a re-issue
    // is an open name that is not the hub's invited-… placeholder.
    const wizard = invite(res, 'http://page.example', !!(c.open && c.name));
    assert.equal(Buffer.compare(Buffer.from(card.text), Buffer.from(wizard.text)), 0, c.profile);
    assert.equal(card.profile, c.profile);
    assert.equal(card.peerId, 'peer_01EXAMPLE');
    assert.equal(card.expires, c.expires_at);
  }
  // An older hub without hub_url: the page's origin, as in Add agent.
  const c = fx.blocks[0];
  assert.ok(reinvite({ ...answer(c), hub_url: '' }, 'http://h:1').text.includes('--hub http://h:1 '));
});

const row = (peer, extra = {}) => ({ peer: { peer_id: 'peer_1', name: 'scout', kind: 'session', ...peer }, credential: 'none', disabled: false, ...extra });
const st = (state, extra = {}) => ({ peer_id: 'peer_1', state, expires_at: '', revoked_at: '', ...extra });

test('the card shows only while a token-enrolling peer holds no live credential', () => {
  const f = peerFacts(row({}));
  assert.deepEqual(inviteCard(f, null, []), { state: 'loading', canRevoke: false, expiresAt: null, revokedAt: null, disabled: false });
  assert.deepEqual(inviteCard(f, st('pending', { expires_at: '2026-11-08T00:00:00.000Z' }), []),
    { state: 'pending', canRevoke: true, expiresAt: '2026-11-08T00:00:00.000Z', revokedAt: null, disabled: false });
  assert.deepEqual(inviteCard(f, st('expired', { expires_at: '2026-11-06T00:00:00.000Z' }), []),
    { state: 'expired', canRevoke: false, expiresAt: '2026-11-06T00:00:00.000Z', revokedAt: null, disabled: false });
  assert.deepEqual(inviteCard(f, st('revoked', { revoked_at: '2026-11-06T01:00:00.000Z' }), []),
    { state: 'revoked', canRevoke: false, expiresAt: null, revokedAt: '2026-11-06T01:00:00.000Z', disabled: false });
  assert.equal(inviteCard(f, st('none'), []).state, 'none');
  assert.equal(inviteCard(peerFacts(row({}, { disabled: true })), st('pending'), []).disabled, true);

  // Enrolled (a live credential, the peer's or its principal's), claimed or
  // OAuth-linked: no card.
  assert.equal(inviteCard(peerFacts(row({}, { credential: 'active' })), st('pending'), []), null);
  assert.equal(inviteCard(f, st('claimed'), []), null);
  assert.equal(inviteCard(f, st('linked'), []), null);
  // A status for another peer, or an unknown state, is not this card's.
  assert.equal(inviteCard(f, { ...st('pending'), peer_id: 'peer_2' }, []).state, 'loading');
  assert.equal(inviteCard(f, st('mystery'), []), null);

  // Kinds that never hold an enrol token: no card, whatever the status says.
  for (const kind of ['dot', 'operator', 'source']) assert.equal(inviteCard(peerFacts(row({ kind })), st('pending'), []), null, kind);
  // A Grok Bot reached by its route links instead; one without a route may
  // still enrol by token (CLI-bearer mode). Routes not loaded yet: wait.
  const grok = peerFacts(row({ kind: 'grokbot' }));
  assert.equal(inviteCard(grok, st('pending'), [{ kind: 'grokbot' }]), null);
  assert.equal(inviteCard(grok, st('pending'), null), null);
  assert.equal(inviteCard(grok, st('pending'), [{ kind: 'http' }]).state, 'pending');
  assert.equal(inviteCard(null, st('pending'), []), null);
});

// A minimal document, installed only while a test renders (md-dom.test.mjs
// keeps its own shim for the rest of the run).
class El {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; this.on = {}; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { this.on[type] = fn; }
  focus() {}
  append(...xs) { for (const x of xs) this.kids.push(x); }
  replaceChildren(...xs) { this.kids = xs; }
}
const walk = (el, out = []) => { if (el instanceof El) { out.push(el); el.kids.forEach(k => walk(k, out)); } return out; };

test('the shared copy panel shows the text as is and keeps the token out of every attribute', () => {
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag) };
  try {
    for (const c of fx.blocks) {
      const inv = reinvite(answer(c), 'http://page.example');
      let done = 0;
      const p = invitePanel(inv, { onDone: () => { done++; } });
      const els = p.nodes.flatMap(n => walk(n));
      const pre = els.find(e => e.tag === 'pre');
      // Set as is: clean() would alter what the agent runs.
      assert.equal(pre.textContent, c.block, c.profile);
      assert.equal(pre.className, 'inv-block mono');
      assert.ok(els.some(e => e.className === 'inv-warn' && /Shown once/.test(e.textContent)));
      const meta = els.find(e => e.className === 'inv-meta mono');
      assert.ok(meta.textContent.startsWith('profile ' + c.profile + (c.open ? ' · name chosen at enrol' : '') + ' · peer peer_01EXAMPLE · expires '));
      for (const e of els) for (const [k, v] of Object.entries(e.attrs)) assert.ok(!v.includes(c.token), `token in ${e.tag}[${k}]`);
      const buttons = els.filter(e => e.tag === 'button').map(e => e.textContent);
      assert.deepEqual(buttons, ['Done', 'Copy invite']);
      assert.equal(p.copy.textContent, 'Copy invite');
      els.find(e => e.tag === 'button' && e.textContent === 'Done').on.click();
      assert.equal(done, 1);
    }
  } finally { globalThis.document = prev; }
});

test('invite status, re-issue and revoke: one route each, the id escaped, no token in any URL', async () => {
  const calls = [];
  const api = createApi({ secret: 'vn_secret', fetch: async (url, init) => { calls.push({ url, init }); return new Response(JSON.stringify({ peer_id: 'peer_1', state: 'pending' }), { status: 200, headers: { 'Content-Type': 'application/json' } }); } });
  await api.inviteStatus('peer_1');
  await api.reissueInvite('peer_1', 3600);
  await api.reissueInvite('peer_1');
  await api.revokeInvite('peer/1');
  assert.deepEqual(calls.map(c => [c.init.method, c.url]), [
    ['GET', '/v1/admin/peers/peer_1/enrol-token'],
    ['POST', '/v1/admin/peers/peer_1/enrol-token'],
    ['POST', '/v1/admin/peers/peer_1/enrol-token'],
    ['DELETE', '/v1/admin/peers/peer%2F1/enrol-token'],
  ]);
  assert.deepEqual(JSON.parse(calls[1].init.body), { enrol_ttl_seconds: 3600 });
  assert.equal(calls[2].init.body, undefined);
  assert.equal(calls[3].init.body, undefined);
  for (const c of calls) assert.equal(c.init.cache, 'no-store');
});
