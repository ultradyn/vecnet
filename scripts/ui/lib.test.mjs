import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chromium, networkQuiet, scrollMessageIntoView } from './lib.mjs';
import { paneText } from './tmux.mjs';

test('tmux pane matching accepts tab-compressed status spacing', () => {
  // capture-pane can return live\tapr even though the rendered cells are spaces.
  for (const output of [' local  live\tapr 2 ', ' local  live  apr 2 ', ' local \x1b[32m live\x1b[0m\tapr 2 ']) {
    assert.ok(paneText(output).includes(' live '), 'a visibly live pane must satisfy the live-status wait');
    assert.ok(paneText(output).includes('apr 2'));
  }
});

test('tmux pane matching preserves status boundaries and line breaks', () => {
  for (const status of ['reconnecting', 'offline', 'lively']) {
    assert.equal(paneText(` local \x1b[31m ${status} \x1b[0m apr 2\nnext line`).includes(' live '), false);
  }
  assert.equal(paneText('\x1b[31m offline \x1b[0m\nnext line'), ' offline \nnext line');
});

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

test('quiet waits for a slow snapshot and the request its render starts, not for streams or held requests', async () => {
  const browser = await (await chromium()).launch({ headless: true });
  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    const net = networkQuiet(page);
    let releaseHeld;
    const held = new Promise(resolve => { releaseHeld = resolve; });
    await page.route('http://vn.test/**', async route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/v1/admin/activity' || path === '/held') { await held; return route.fulfill({ body: '' }); }
      await new Promise(resolve => setTimeout(resolve, path === '/snapshot' ? 400 : 200));
      await route.fulfill({ body: path, headers: { 'Access-Control-Allow-Origin': '*' } });
    });
    await page.goto('http://vn.test/');
    const unignore = net.ignore(req => req.url().endsWith('/held'));
    await page.evaluate(() => {
      fetch('/v1/admin/activity');
      fetch('/v1/events?wait=25');
      fetch('/held');
      // The snapshot's render asks for one more resource, as a thread's first
      // draw asks for its aside.
      fetch('/snapshot').then(r => r.text()).then(t => {
        document.body.textContent = t;
        return fetch('/avatar').then(r => r.text()).then(a => { document.body.textContent += ' ' + a; });
      });
    });
    await net.quiet({ timeout: 5000 });
    assert.equal(await page.evaluate(() => document.body.textContent), '/snapshot /avatar');
    unignore();
    await assert.rejects(net.quiet({ timeout: 600 }), /never went quiet: GET \/held/);
    releaseHeld();
    await net.quiet({ timeout: 5000 });
  } finally {
    await browser.close();
  }
});
