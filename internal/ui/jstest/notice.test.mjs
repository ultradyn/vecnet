import test from 'node:test';
import assert from 'node:assert/strict';
import { bannerItems, isNoticeEvent, restartLine, restartState } from '../static/notice-model.js';
import { renderBanner } from '../static/notice.js';

// A minimal document, installed only while a test renders: md-dom.test.mjs
// keeps its own shim on globalThis.document for the rest of the run.
class El {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; this.on = {}; this.hidden = false; }
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
  replaceChildren(...xs) { this.kids = []; this.append(...xs); }
}
function render(opts) {
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new El(tag) };
  try {
    const root = new El('div');
    renderBanner(root, opts);
    return root;
  } finally { globalThis.document = prev; }
}
const find = (el, pred, out = []) => {
  if (el instanceof El) { if (pred(el)) out.push(el); el.kids.forEach(k => find(k, pred, out)); }
  return out;
};

const NOW = Date.parse('2026-11-05T07:00:00.000Z');
const iso = ms => new Date(NOW + ms).toISOString();
const sig = 'A'.repeat(86);
const notice = (kind, extra = {}) => ({
  notice_id: 'ntc_' + kind.toUpperCase().padEnd(26, '0').slice(0, 26).replace(/[^0-9A-HJKMNP-TV-Z]/g, '0'),
  kind, message: kind + ' message', hub: 'https://hub.example', issued_at: iso(-60e3),
  expires_at: iso(86400e3), sig, ...(kind === 'moving' ? { new_url: 'https://vecnet.xk.io' } : {}), ...extra,
});

test('bannerItems orders moving, restart, maintenance and drops cleared', () => {
  const ns = [
    notice('maintenance', { at: iso(2 * 3600e3) }),
    notice('restart', { notice_id: 'ntc_CLEARED0000000000000000000', cleared_at: iso(-1000), cleared_how: 'operator' }),
    notice('restart'),
    notice('moving', { at: iso(3 * 3600e3) }),
    notice('maintenance', { notice_id: 'ntc_EXP1RED000000000000000000', expires_at: iso(-1000) }),
  ];
  const items = bannerItems(ns, NOW);
  assert.deepEqual(items.map(i => i.kind), ['moving', 'restart', 'maintenance']);
  assert.deepEqual(items.map(i => i.id), [ns[3].notice_id, ns[2].notice_id, ns[0].notice_id]);
  assert.deepEqual(items.map(i => i.label), ['Moving', 'Restart', 'Maintenance']);
  assert.deepEqual(items.map(i => i.text), ['moving message', 'restart message', 'maintenance message']);
  assert.equal(items[0].host, 'vecnet.xk.io');
  assert.equal(items[1].host, '');
  // "when" is relative and absolute: in 3h, then the local clock time.
  assert.match(items[0].when, /^in 3h · (\d{1,2} \w{3} )?\d\d:\d\d$/);
  assert.match(items[2].when, /^in 2h · (\d{1,2} \w{3} )?\d\d:\d\d$/);
  // Without `at` the notice dates from when it was posted.
  assert.match(items[1].when, /^1m ago · (\d{1,2} \w{3} )?\d\d:\d\d$/);
  assert.deepEqual(bannerItems(null, NOW), []);
  assert.deepEqual(bannerItems([null, 7, 'x'], NOW), []);
});

test('hostile message stays text', () => {
  const hostile = '<img src=x onerror=alert(1)>';
  const items = bannerItems([notice('maintenance', { message: hostile })], NOW);
  assert.equal(items[0].text, hostile);
  const root = render({ items, now: NOW });
  assert.equal(find(root, e => ['img', 'script', 'iframe'].includes(e.tag)).length, 0);
  const [tx] = find(root, e => e.className === 'tx');
  assert.ok(tx, 'message span');
  assert.deepEqual(tx.kids, [hostile]);
  assert.equal(tx.textContent, hostile);
  assert.equal(root.hidden, false);
  assert.equal(root.attrs.role, undefined); // the slot in index.html carries role="status"
});

test('restartState: hub_restarting → restarting; ready → none; tick past until+30s → unreachable', () => {
  const none = restartState(undefined, { tick: 1, at: NOW });
  assert.equal(none.phase, 'none');
  const r = restartState(none, { frame: 'hub_restarting', retryAfterMs: 5000, at: NOW });
  assert.deepEqual(r, { phase: 'restarting', until: NOW + 5000, since: NOW });
  assert.equal(restartState(r, { frame: 'ready' }).phase, 'none');
  // Still restarting up to and at until + 30 s; past it, unreachable since the frame.
  assert.equal(restartState(r, { tick: 1, at: NOW + 35000 }), r);
  const u = restartState(r, { tick: 1, at: NOW + 35001 });
  assert.deepEqual(u, { phase: 'unreachable', until: NOW + 5000, since: NOW });
  assert.equal(restartState(u, { frame: 'ready' }).phase, 'none');
  // A bad retry hint counts as zero.
  assert.equal(restartState(none, { frame: 'hub_restarting', retryAfterMs: 'soon', at: NOW }).until, NOW);
  // The lines the banner shows.
  assert.deepEqual(restartLine(r, NOW + 1200), { tone: 'warn', text: 'Hub restarting: reconnecting in 4 s', lead: 'Hub restarting: reconnecting', tail: ' in 4 s' });
  assert.match(restartLine(u, NOW + 40000).text, /^Hub unreachable since \d\d:\d\d$/);
  assert.equal(restartLine(u, NOW).tone, 'bad');
  assert.equal(restartLine(none, NOW), null);
  // Rendered: amber while restarting, red once unreachable, nothing at none.
  const warn = render({ items: [], restart: r, now: NOW });
  assert.equal(find(warn, e => /\bntc\b/.test(e.className) && /\bwarn\b/.test(e.className)).length, 1);
  assert.match(warn.textContent, /Hub restarting: reconnecting in 5 s/);
  const bad = render({ items: [], restart: u, now: NOW + 40000 });
  assert.equal(find(bad, e => /\bntc\b/.test(e.className) && /\bbad\b/.test(e.className)).length, 1);
  assert.equal(render({ items: [], restart: none, now: NOW }).hidden, true);
  assert.equal(isNoticeEvent({ type: 'hub_notice', data: {} }), true);
  assert.equal(isNoticeEvent({ type: 'presence' }), false);
  assert.equal(isNoticeEvent(null), false);
});

test('moving link uses new_url host as text and rel=noopener', () => {
  const items = bannerItems([notice('moving', { new_url: 'https://vecnet.xk.io/base' })], NOW);
  const dismissed = [];
  const root = render({ items, now: NOW, onDismiss: id => dismissed.push(id) });
  const links = find(root, e => e.tag === 'a');
  assert.equal(links.length, 1);
  const [a] = links;
  assert.equal(a.attrs.href, 'https://vecnet.xk.io/base');
  assert.equal(a.textContent, 'vecnet.xk.io');
  assert.ok(a.attrs.rel.split(' ').includes('noopener'), a.attrs.rel);
  // A non-http new_url is shown nowhere and linked nowhere.
  const js = bannerItems([notice('moving', { new_url: 'javascript:alert(1)' })], NOW);
  assert.equal(js[0].host, '');
  assert.equal(find(render({ items: js, now: NOW }), e => e.tag === 'a').length, 0);
  // Dismiss hides by id; it is the caller's (session) state, never the hub's.
  const [x] = find(root, e => e.tag === 'button');
  assert.equal(x.attrs['aria-label'], 'Dismiss notice');
  x.on.click();
  assert.deepEqual(dismissed, [items[0].id]);
});

test('banner updates in place: ticks keep rows, focus and the live text', () => {
  const prev = globalThis.document;
  const doc = { createElement: tag => new El(tag), activeElement: null };
  globalThis.document = doc;
  try {
    const root = new El('div');
    const r = restartState(undefined, { frame: 'hub_restarting', retryAfterMs: 5000, at: NOW });
    const ns = [notice('moving', { at: iso(3 * 3600e3) }), notice('maintenance', { at: iso(5 * 60e3) })];
    renderBanner(root, { items: bannerItems(ns, NOW), restart: r, now: NOW });
    const [restartRow, movingRow, maintRow] = root.kids;
    const [x] = find(movingRow, e => e.tag === 'button');
    doc.activeElement = x; // a keyboard user has tabbed to the dismiss button
    // One second later (countdown) and one minute later (relative times).
    renderBanner(root, { items: bannerItems(ns, NOW + 1000), restart: r, now: NOW + 1000 });
    renderBanner(root, { items: bannerItems(ns, NOW + 60e3), restart: r, now: NOW + 4000 });
    assert.deepEqual(root.kids, [restartRow, movingRow, maintRow], 'rows rebuilt on a tick');
    assert.equal(find(root, e => e.tag === 'button')[0], x);
    assert.equal(doc.activeElement, x);
    // The countdown changed, but only inside an aria-hidden span.
    assert.match(restartRow.textContent, /^Hub restarting: reconnecting in 1 s$/);
    const [tx] = find(restartRow, e => e.className === 'tx');
    assert.equal(tx.kids[0], 'Hub restarting: reconnecting');
    assert.equal(tx.kids[1].attrs['aria-hidden'], 'true');
    // The relative time moved on, the clock time is the same text node owner.
    assert.match(maintRow.textContent, /in 4m/);
    const [rel] = find(maintRow, e => e.attrs['aria-hidden'] === 'true' && /^in /.test(e.textContent));
    assert.ok(rel, 'relative time is aria-hidden');
    // A new notice adds a row and keeps the others; the focused button is refocused.
    let refocused = 0;
    x.focus = () => { refocused++; };
    x.contains = () => false;
    for (const el of [restartRow, movingRow, maintRow]) el.contains = node => find(el, e => e === node).length > 0;
    const more = [...ns, notice('restart')];
    renderBanner(root, { items: bannerItems(more, NOW + 60e3), restart: r, now: NOW + 4000 });
    assert.equal(root.kids.length, 4);
    assert.equal(root.kids[1], movingRow);
    assert.equal(refocused, 1);
    // Ready clears the restart row only.
    renderBanner(root, { items: bannerItems(more, NOW + 60e3), restart: restartState(r, { frame: 'ready' }), now: NOW + 5000 });
    assert.equal(root.kids[0], movingRow);
    assert.equal(root.kids.length, 3);
  } finally { globalThis.document = prev; }
});
