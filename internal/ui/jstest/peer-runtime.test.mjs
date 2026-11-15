import test from 'node:test';
import assert from 'node:assert/strict';
import { mount } from '../static/peer.js';
import { El } from './activity-dom-fixture.mjs';

// Exercise the real peer mount and its timers, with only DOM and reads replaced.
class PeerEl extends El {
  contains(el) { return el === this || this.children.some(k => k.contains(el)); }
  get classList() {
    return {
      contains: c => this.className.split(/\s+/).includes(c),
      add: (...cs) => { this.className = [...new Set([...this.className.split(/\s+/), ...cs])].join(' ').trim(); },
      remove: c => { this.className = this.className.split(/\s+/).filter(x => x !== c).join(' '); },
    };
  }
}
const PEER = 'peer_runtime';
const NOW = Date.parse('2026-11-15T01:00:30Z');
const report = (over = {}) => ({ runtime: 'codex', state: 'working', delivery: 'ready', version: '1.2.3',
  updated_at: '2026-11-15T01:00:00Z', expires_at: '2026-11-15T01:01:00Z', ...over });

function world(t, status, { id = PEER, kind = 'session', unread = 0 } = {}) {
  const previous = globalThis.document;
  globalThis.document = { createElement: tag => new PeerEl(tag), activeElement: null };
  t.after(() => { globalThis.document = previous; });
  t.mock.timers.enable({ apis: ['setInterval'] });
  let hubNow = NOW, reads = 0, admin = 0;
  const responses = [
    { peers: [{ peer: { peer_id: id, name: '<img src=x onerror=alert(1)>', kind, presence: 'idle', runtime_status: status }, credential: 'active' }] },
    { principals: [] }, { routes: [] }, { blocked: [], failed: [] }, [], { threads: [] },
  ];
  const api = { adminPeers: () => { reads++; return responses[0]; } };
  const store = { hubNow: () => hubNow, resource(_name, load) {
    const value = responses[admin++];
    return { get: () => value, error: () => null, subscribe: () => () => {}, reload: () => { load?.(); } };
  } };
  const root = new PeerEl('main');
  const off = mount(root, { api, store, peerId: id, me: 'peer_op', inboxHead: { get: () => ({ unread_by_sender: { [id]: unread } }), subscribe: () => () => {} }, nameOf: id => id, nav() {}, recoveryStorage: null });
  t.after(off);
  const facts = () => {
    const list = root.querySelector('.pv-presence').querySelector('dl');
    return Object.fromEntries(list.children.filter(e => e.tag === 'dt').map((dt, i) => [dt.textContent, list.children[i * 2 + 1].textContent]));
  };
  return { root, facts, reads: () => reads, tick(ms) { hubNow += ms; t.mock.timers.tick(ms); } };
}

test('peer detail shows Codex working, delivery readiness, version and hub-relative report age', t => {
  const w = world(t, report());
  const f = w.facts();
  assert.match(f['Codex runtime'], /working/);
  assert.match(f['Codex delivery'], /ready/);
  assert.equal(f['Codex version'], '1.2.3');
  assert.match(f['Last runtime report'], /30s ago/);
  assert.match(f.Presence, /^idle/);
});

test('the mounted peer expires a ready report while the admin read stays unchanged', t => {
  const w = world(t, report({ expires_at: '2026-11-15T01:00:32Z' }));
  assert.match(w.facts()['Codex delivery'], /ready/);
  w.tick(2000);
  assert.equal(w.reads(), 0, 'expiry happens before the 30-second network reload');
  assert.match(w.facts()['Codex runtime'], /unknown.*stale/i);
  assert.equal(w.facts()['Codex delivery'], 'unknown');
  assert.match(w.facts()['Last runtime report'], /32s ago/);
});

test('peer detail explains waiting and offline independently of ordinary presence', t => {
  const w = world(t, report({ state: 'blocked', delivery: 'unavailable', reason: 'waiting_approval' }));
  assert.match(w.facts()['Codex runtime'], /blocked.*waiting for approval/i);
  assert.equal(w.facts()['Codex delivery'], 'unavailable');
  assert.match(w.facts().Presence, /^idle/);
});

test('peer detail labels a fresh offline runtime clearly', t => {
  const w = world(t, report({ state: 'offline', delivery: 'unavailable' }));
  assert.match(w.facts()['Codex runtime'], /offline/);
  assert.equal(w.facts()['Codex delivery'], 'unavailable');
});

test('peer detail renders hostile strings as text and drops private or malformed runtime fields', t => {
  const version = '<svg onload=alert(1)>';
  const w = world(t, report({ version, reason: '/home/private/thread', thread_id: 'private-thread', binding: 'private-binding' }));
  assert.equal(w.facts()['Codex version'], version);
  assert.match(w.root.textContent, /<img src=x onerror=alert\(1\)>/);
  assert.equal(w.root.querySelectorAll('svg').length, 0);
  assert.equal(w.root.querySelectorAll('img').length, 0);
  assert.doesNotMatch(w.root.textContent, /\/home\/private|private-thread|private-binding/);
});

test('peer detail omits runtime fields for an older hub', t => {
  const w = world(t, undefined);
  assert.doesNotMatch(w.root.textContent, /Codex runtime|Codex delivery|Last runtime report/);
});

test('bulk read offer is only for other peers with unread, not directory sources', t => {
  const w = world(t, undefined, { id: 'src_webhook', kind: 'source', unread: 1 });
  assert.equal(w.root.querySelector('.pv-read-all'), null);
});

test('bulk read offer exists for an agent with unread', t => {
  const w = world(t, undefined, { unread: 1 });
  assert.match(w.root.querySelector('.pv-read-all').textContent, /^Mark all from /);
});

test('bulk read offer never appears for the operator itself', t => {
  const w = world(t, undefined, { id: 'peer_op', kind: 'operator', unread: 1 });
  assert.equal(w.root.querySelector('.pv-read-all'), null);
});
