import test from 'node:test';
import assert from 'node:assert/strict';
import { peerActions, peerFacts } from '../static/peer-model.js';
import { messageActions } from '../static/peer.js';

// The peer header's message actions as the browser gets them: which element,
// where it goes, and what a click hands the composer. A minimal document,
// installed only while a test renders: md-dom.test.mjs keeps its own shim on
// globalThis.document for the rest of the run.
class El {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; this.on = {}; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { this.on[type] = fn; }
  append(...xs) {
    for (const x of xs) {
      if (typeof x !== 'string' && !(x instanceof El)) throw new Error('append got ' + Object.prototype.toString.call(x));
      this.kids.push(x);
    }
  }
}
function render(acts) {
  const calls = { prefill: [], nav: [] };
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag) };
  try {
    const els = messageActions(acts, {
      name: 'grok-reviewer', threadName: t => t.title, age: () => '4m ago',
      prefill: d => calls.prefill.push(d), nav: h => calls.nav.push(h),
    });
    return { els, calls };
  } finally { globalThis.document = prev; }
}
// click runs the element's click listener as a browser would, and says
// whether it kept the link's own navigation (no listener, or no preventDefault).
function click(el, mods = {}) {
  let prevented = false;
  const ev = { button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, ...mods, preventDefault: () => { prevented = true; } };
  if (el.on.click) el.on.click(ev);
  return { followed: !prevented };
}
const label = el => el.kids.find(k => k instanceof El && k.className === 'lb').textContent;

const PEER = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8B', ME = 'peer_01JD3Q2K8M4N6P8R0T2V4X60PS';
const facts = kind => peerFacts({ peer: { peer_id: PEER, name: 'grok-reviewer', kind } });
const shared = { threads: [{ thread: { thread_id: 'thr_hark', title: 'hark B196', participants: [ME, PEER] }, last_message_at: '2026-11-04T02:00:00.000Z' }] };

test('has a thread: the quick link goes to it, and New message is beside it', () => {
  const { els: [open, compose], calls } = render(peerActions(facts('session'), shared, ME, null));
  assert.equal(open.tag, 'a');
  assert.equal(open.attrs.href, '#thread/thr_hark');
  assert.equal(label(open), 'Open thread');
  assert.match(open.textContent, /hark B196/);
  assert.match(open.textContent, /4m ago/);
  assert.equal(click(open).followed, true, 'the link navigates itself');
  assert.deepEqual(calls, { prefill: [], nav: [] });
  assert.equal(compose.tag, 'a');
  assert.equal(compose.attrs.href, '#new');
  assert.equal(label(compose), 'New message');
});

test('no thread: the quick link opens New message prefilled to the peer', () => {
  const { els: [open, compose], calls } = render(peerActions(facts('session'), { threads: [] }, ME, null));
  assert.equal(open.attrs.href, '#new');
  assert.equal(label(open), 'Start thread');
  assert.equal(click(open).followed, false);
  assert.deepEqual(calls.prefill, [{ recipients: [PEER], priority: 'wake' }]);
  assert.deepEqual(calls.nav, ['#new']);
  // New message is still there, and does the same.
  assert.equal(label(compose), 'New message');
  click(compose);
  assert.deepEqual(calls.prefill[1], { recipients: [PEER], priority: 'wake' });
  assert.deepEqual(calls.nav, ['#new', '#new']);
});

test('New message is always present: with a thread, without one, loading, and failed', () => {
  const states = [shared, { threads: [] }, null, null];
  const errs = [null, null, null, new Error('down')];
  states.forEach((page, i) => {
    const { els, calls } = render(peerActions(facts('session'), page, ME, errs[i]));
    assert.equal(els.length, 2, 'state ' + i);
    const compose = els[1];
    assert.equal(label(compose), 'New message');
    assert.equal(click(compose).followed, false);
    assert.deepEqual(calls.prefill, [{ recipients: [PEER], priority: 'wake' }], 'state ' + i);
    assert.deepEqual(calls.nav, ['#new']);
  });
  // While the page loads or failed, the quick link is a disabled button.
  const loading = render(peerActions(facts('session'), null, ME, null)).els[0];
  assert.equal(loading.tag, 'button');
  assert.equal(loading.attrs.disabled, '');
  assert.equal(loading.attrs['aria-busy'], 'true');
  const failed = render(peerActions(facts('session'), null, ME, new Error('down'))).els[0];
  assert.equal(failed.attrs.disabled, '');
  assert.match(failed.attrs.title, /Threads unavailable: down/);
});

test('a Grok Bot opens New message at wake_idle, from either action', () => {
  const none = render(peerActions(facts('grokbot'), { threads: [] }, ME, null));
  click(none.els[0]);
  click(none.els[1]);
  assert.deepEqual(none.calls.prefill, [{ recipients: [PEER], priority: 'wake_idle' }, { recipients: [PEER], priority: 'wake_idle' }]);
  assert.match(none.els[1].textContent, /wake_idle/, 'the button says which priority it opens at');
  assert.match(none.els[1].attrs.title, /at wake_idle/);
  // With a thread, the quick link still opens it; New message keeps wake_idle.
  const has = render(peerActions(facts('grokbot'), shared, ME, null));
  assert.equal(has.els[0].attrs.href, '#thread/thr_hark');
  click(has.els[1]);
  assert.deepEqual(has.calls.prefill, [{ recipients: [PEER], priority: 'wake_idle' }]);
  // Any other kind says nothing extra.
  assert.doesNotMatch(render(peerActions(facts('session'), shared, ME, null)).els[1].textContent, /wake/);
});

test('a modified click (new tab) leaves the link alone and hands over no draft', () => {
  for (const mods of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }]) {
    const { els: [open, compose], calls } = render(peerActions(facts('session'), { threads: [] }, ME, null));
    assert.equal(click(open, mods).followed, true, JSON.stringify(mods));
    assert.equal(click(compose, mods).followed, true, JSON.stringify(mods));
    assert.deepEqual(calls, { prefill: [], nav: [] }, JSON.stringify(mods));
  }
});

test('nothing is drawn for a peer the operator cannot message', () => {
  assert.deepEqual(render(peerActions(facts('source'), { threads: [] }, ME, null)).els, []);
  assert.deepEqual(render(null).els, []);
});
