import test from 'node:test';
import assert from 'node:assert/strict';
import { installDom, snapshot } from './activity-dom-fixture.mjs';
const module = await import('../static/sparkline.js').catch(e => { if (e.code !== 'ERR_MODULE_NOT_FOUND') throw e; return {}; });

test('sparkline uses namespaced SVG and one keyboard plot stop with exact table alternative', () => {
  assert.equal(typeof module.createSparkline, 'function', 'sparkline renderer is missing');
  const restore = installDom();
  try {
    const c = module.createSparkline(), s = snapshot(); c.update(s, s, { label: '<img src=x>', max: 5 });
    const svg = c.el.querySelector('svg');
    assert.equal(svg.namespaceURI, 'http://www.w3.org/2000/svg');
    assert.equal(svg.attrs.tabindex, '0');
    assert.equal(svg.attrs['data-max'], '5');
    assert.equal(c.el.querySelectorAll('img').length, 0);
    assert.equal(c.el.querySelector('tbody').children.length, 60);
    const first = c.el.querySelector('tbody').children[0].textContent;
    assert.match(first, /2026-11-09T11:00:30.000Z/); assert.match(first, /2026-11-09T11:01:30.000Z/);
    assert.match(first, /1$/);
    assert.match(svg.attrs['aria-label'], /4 messages.*hour/);
    const row = c.el.querySelector('tbody').children[0];
    c.update(s, s, { label: '<img src=x>', max: 5 });
    assert.equal(c.el.querySelector('tbody').children[0], row, 'unchanged snapshots retain table rows');
  } finally { restore(); }
});

test('keyboard and pointer select exact minute, clamp, dismiss and retain selection on update', () => {
  assert.equal(typeof module.createSparkline, 'function', 'sparkline renderer is missing');
  const restore = installDom();
  try {
    const c = module.createSparkline(), s = snapshot(); c.update(s, s, { label: 'Hub' });
    const svg = c.el.querySelector('svg'), read = c.el.querySelector('.spark-readout');
    const key = key => svg.on.keydown({ key, preventDefault() {} });
    key('Home'); assert.match(read.textContent, /1 messages.*11:00:30/);
    key('ArrowRight'); assert.match(read.textContent, /3 messages.*11:01:30/);
    key('End'); key('ArrowRight'); assert.match(read.textContent, /0 messages.*11:59:30/);
    key('Escape'); assert.equal(read.hidden, true);
    svg.on.pointermove({ clientX: 4 }); assert.match(read.textContent, /1 messages.*11:00:30/);
    assert.equal(read.hidden, false);
    svg.on.pointerleave(); assert.equal(read.hidden, true);
    key('Home'); const next = snapshot({ peer_a: [9] }); c.update(next, next, { label: 'Hub' });
    assert.match(read.textContent, /9 messages/);
    c.update(null, null, { label: 'Hub' }); assert.equal(svg.getAttribute('hidden'), '');
    assert.doesNotMatch(c.el.textContent, /9 messages/);
  } finally { restore(); }
});
