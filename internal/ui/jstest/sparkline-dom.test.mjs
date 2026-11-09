import test from 'node:test';
import assert from 'node:assert/strict';
import { installDom, snapshot } from './activity-dom-fixture.mjs';
const module = await import('../static/sparkline.js').catch(e => { if (e.code !== 'ERR_MODULE_NOT_FOUND') throw e; return {}; });

test('sparkline provides a named keyboard plot without raw-value disclosures or hidden tables', () => {
  assert.equal(typeof module.createSparkline, 'function', 'sparkline renderer is missing');
  const restore = installDom();
  try {
    const c = module.createSparkline(), s = snapshot(); c.update(s, s, { label: '<img src=x>', max: 5 });
    const svg = c.el.querySelector('svg');
    assert.equal(svg.namespaceURI, 'http://www.w3.org/2000/svg');
    assert.equal(svg.attrs.tabindex, '0');
    assert.equal(svg.attrs['data-max'], '5');
    assert.equal(c.el.querySelectorAll('img').length, 0);
    for (const tag of ['details', 'summary', 'table']) assert.equal(c.el.querySelectorAll(tag).length, 0, `no ${tag} in a chart`);
    assert.match(svg.attrs['aria-label'], /4 messages.*hour/);
    assert.match(svg.attrs['aria-label'], /arrows.*Home.*End/);
    assert.equal(c.el.querySelector('.spark-axis').textContent, '1 hour agoNow');
    c.update(s, s, { label: '<img src=x>', max: 5 });
    assert.equal(c.el.querySelector('svg'), svg, 'unchanged snapshots retain the keyboard plot');
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

test('SVG accessible labels strip hostile bidi and controls while preserving Unicode and chart context', () => {
  const restore = installDom();
  try {
    const c = module.createSparkline(), s = snapshot(), svg = c.el.querySelector('svg');
    for (const name of [
      'Zoë\u202e 東京\u2066 / café\u2069\u0000\u0007\u001b\u007f\u0085\u009f 42',
      'Zoë 東京 / café 42',
    ]) {
      c.update(s, s, { label: name, max: 5 });
      const label = svg.getAttribute('aria-label');
      assert.equal(label, 'Zoë 東京 / café 42: 4 messages in the last hour; minute counts, zero to 5. Use arrows, Home or End to inspect.');
      for (const control of ['\u202e', '\u2066', '\u2069', '\u0000', '\u0007', '\u001b', '\u007f', '\u0085', '\u009f']) {
        assert.equal(label.includes(control), false, `accessible name retained U+${control.charCodeAt(0).toString(16)}`);
      }
    }
  } finally { restore(); }
});
