import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chromium, scrollMessageIntoView } from './lib.mjs';

test('message scroll reaches the viewport while live updates replace message nodes', async () => {
  const browser = await (await chromium()).launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    page.setDefaultTimeout(3000);
    await page.setContent('<div style="height:2000px"></div><article id="m-target" style="height:300px">message</article>');
    // Thread rendering replaces .msg nodes on resource notifications. Replace
    // at every animation frame so the detached-node race is deterministic.
    await page.evaluate(() => {
      window.rebuilds = 0;
      const rebuild = () => {
        const message = document.getElementById('m-target');
        message.replaceWith(message.cloneNode(true));
        window.rebuilds++;
        if (window.rebuilds < 24) requestAnimationFrame(rebuild);
      };
      requestAnimationFrame(rebuild);
    });
    await page.waitForFunction(() => window.rebuilds >= 2);
    await scrollMessageIntoView(page, 'target');
    assert.ok(await page.evaluate(() => {
      const r = document.getElementById('m-target').getBoundingClientRect();
      return r.width > 0 && r.height > 0 && r.top < innerHeight && r.bottom > 0;
    }), 'the current message node is outside the viewport');
  } finally {
    await browser.close();
  }
});
