import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chromium, scrollIntoViewStable } from './lib.mjs';

async function withPage(fn) {
  const browser = await (await chromium()).launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 800, height: 600 } });
    await fn(page);
  } finally {
    await browser.close();
  }
}

test('resolves only after the node stops being replaced, and leaves it in view', async () => {
  await withPage(async page => {
    await page.setContent('<body style="margin:0;height:3000px"><div style="height:2200px"></div><div id="t" style="height:100px">target</div></body>');
    const started = performance.now();
    await page.evaluate(() => {
      const until = performance.now() + 400;
      window.rebuilds = 0;
      const replace = () => {
        if (performance.now() >= until) return;
        const el = document.querySelector('#t');
        el.replaceWith(el.cloneNode(true));
        window.rebuilds++;
        requestAnimationFrame(replace);
      };
      requestAnimationFrame(replace);
    });
    await scrollIntoViewStable(page, '#t');
    assert.ok(performance.now() - started >= 400, 'resolved while the node was still being replaced');
    const result = await page.locator('#t').evaluate(el => {
      const r = el.getBoundingClientRect();
      return { connected: el.isConnected, top: r.top, bottom: r.bottom, viewport: innerHeight, rebuilds: window.rebuilds };
    });
    assert.ok(result.rebuilds > 0, 'the fixture replaced the target');
    assert.ok(result.connected, 'the current target is connected');
    assert.ok(result.top >= 0 && result.bottom <= result.viewport, 'the target fits fully inside the viewport');
  });
});

test('handles an element taller than the viewport', async () => {
  await withPage(async page => {
    await page.setContent('<div style="height:3000px"></div><div id="t" style="height:2000px">tall target</div>');
    await scrollIntoViewStable(page, '#t');
    const result = await page.locator('#t').evaluate(el => {
      const r = el.getBoundingClientRect();
      return { connected: el.isConnected, top: r.top, bottom: r.bottom, viewport: innerHeight };
    });
    assert.ok(result.connected && result.bottom > 0 && result.top < result.viewport, 'the tall target intersects the viewport');
    assert.ok(result.top <= result.viewport / 2, 'the tall target reaches the middle of the viewport');
  });
});

test('times out on a selector that never appears', async () => {
  await withPage(async page => {
    await page.setContent('<div>no target</div>');
    await assert.rejects(scrollIntoViewStable(page, '#missing', { timeout: 300 }), { name: 'TimeoutError' });
  });
});
