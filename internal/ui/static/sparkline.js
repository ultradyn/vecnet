// Persistent, CSP-safe rolling-minute plot. Geometry/labels stay in the reviewed model.
import { h } from './dom.js';
import { clean } from './format.js';
import { bucketReadout, sparklineGeometry } from './sparkline-model.js';

const WIDTH = 240, HEIGHT = 48;
function svgNode(tag, attrs) {
  const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [key, value] of Object.entries(attrs || {})) el.setAttribute(key, value);
  return el;
}

export function createSparkline() {
  let snapshot = null, series = null, index = null, lastRender;
  const area = svgNode('path', { class: 'spark-area' });
  const baseline = svgNode('line', { class: 'spark-baseline', x1: 4, x2: WIDTH - 4, y1: HEIGHT - 4, y2: HEIGHT - 4 });
  const line = svgNode('polyline', { class: 'spark-line' });
  const cursor = svgNode('line', { class: 'spark-cursor', y1: 4, y2: HEIGHT - 4 });
  const svg = svgNode('svg', { viewBox: `0 0 ${WIDTH} ${HEIGHT}`, preserveAspectRatio: 'none', tabindex: 0, role: 'img' });
  const point = svgNode('circle', { class: 'spark-point', r: 4 });
  svg.append(baseline, area, line, cursor, point);
  const readout = h('p', { class: 'spark-readout', 'aria-live': 'polite', hidden: true });
  const axis = h('div', { class: 'spark-axis', 'aria-hidden': 'true' }, h('span', { text: '−60 m' }), h('span', { class: 'spark-peak', text: '−30 m' }), h('span', { text: 'now' }));
  const el = h('div', { class: 'sparkline' }, svg, readout, axis);

  function show() {
    const label = bucketReadout(snapshot, index);
    const visible = !!(label && series);
    cursor.hidden = !visible;
    // SVG hidden presentation is handled explicitly in CSS, including older engines.
    cursor.setAttribute('visibility', visible ? 'visible' : 'hidden');
    point.setAttribute('visibility', visible ? 'visible' : 'hidden');
    readout.hidden = !visible;
    if (!visible) { readout.textContent = ''; return; }
    const x = 4 + index / 59 * (WIDTH - 8);
    cursor.setAttribute('x1', x); cursor.setAttribute('x2', x);
    const max = Number(svg.getAttribute('data-max'));
    point.setAttribute('cx', x); point.setAttribute('cy', HEIGHT - 4 - (max ? Math.min(series.buckets[index] / max, 1) : 0) * (HEIGHT - 8));
    readout.textContent = `${series.buckets[index]} messages · ${label.text}`;
    readout.setAttribute('title', label.label);
    readout.setAttribute('aria-label', `${series.buckets[index]} messages · ${label.label}`);
  }
  svg.addEventListener('keydown', e => {
    if (!series || !['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Escape'].includes(e.key)) return;
    e.preventDefault();
    if (e.key === 'Escape') index = null;
    else if (e.key === 'Home') index = 0;
    else if (e.key === 'End') index = 59;
    else index = Math.max(0, Math.min(59, (index ?? (e.key === 'ArrowLeft' ? 60 : -1)) + (e.key === 'ArrowLeft' ? -1 : 1)));
    show();
  });
  svg.addEventListener('focus', () => { if (series && index === null) { index = 59; show(); } });
  svg.addEventListener('blur', () => { index = null; show(); });
  svg.addEventListener('pointermove', e => {
    if (!series) return;
    const r = svg.getBoundingClientRect();
    index = Math.max(0, Math.min(59, Math.round(((e.clientX - r.left) / r.width * WIDTH - 4) / (WIDTH - 8) * 59)));
    show();
  });
  svg.addEventListener('pointerleave', () => { if (document.activeElement !== svg) { index = null; show(); } });

  function update(nextSnapshot, nextSeries, { label = 'Messages', max } = {}) {
    const key = JSON.stringify([nextSnapshot?.window_start, nextSeries?.buckets, label, max]);
    snapshot = nextSnapshot; series = nextSeries;
    if (key === lastRender) { show(); return; }
    lastRender = key;
    if (series) svg.removeAttribute('hidden'); else svg.setAttribute('hidden', '');
    axis.hidden = !series;
    if (!series) { show(); return; }
    const geometry = sparklineGeometry(series.buckets, { max });
    line.setAttribute('points', geometry.points); area.setAttribute('d', geometry.area);
    svg.setAttribute('data-max', geometry.max);
    axis.setAttribute('title', `Peak ${Math.max(...series.buckets)} messages per minute`);
    axis.querySelector('.spark-peak').textContent = `−30 m · peak ${Math.max(...series.buckets)}`;
    svg.setAttribute('aria-label', clean(`${label}: ${series.messages} messages in the last hour; minute counts, zero to ${geometry.max}. Use arrows, Home or End to inspect.`));
    svg.setAttribute('title', 'Hover or use arrows, Home / End to inspect each minute; Esc to dismiss.');
    show();
  }
  update(null, null);
  return { el, update };
}
