// Hub notices Task 9: the web UI's notice banner and restart banner against a
// real demo hub, at desktop and phone width. Every wait is on page or hub
// state, never on elapsed time; the unreachable state comes from the page's
// clock jumping forward.
import { assert, cleanBuild, withDemo } from './lib.mjs';

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

const moving = { kind: 'moving', message: 'Moving to Fly tonight, follow automatically', new_url: 'https://vecnet.xk.io' };
const maintenance = { kind: 'maintenance', message: 'Database vacuum, expect slow reads for a few minutes' };
const hostile = '<img src=x onerror=alert(1)> stays text';
const RANK = { moving: 0, restart: 1, maintenance: 2 };

try {
  await withDemo({}, async ({ page, shot, asPeer, restart, hold, fail, allowConsole, countRequests }) => {
    // Chromium logs each refused fetch: the 503s of the outage below.
    allowConsole(/status of 503/);
    // The page's clock flows in real time until the outage jumps it forward.
    await page.clock.install();
    await page.reload({ waitUntil: 'load' });
    // lib.mjs hold() unroutes as it releases; this route keeps Playwright's
    // interception on so each held request is continued exactly once.
    await page.route('**/__keep-interception__', route => route.continue());

    const banner = page.locator('#notices');
    const rows = banner.locator('.ntc');
    const conn = page.locator('#conn');
    const post = async body => {
      const response = await asPeer('operator', 'POST', '/v1/admin/notices', body);
      assert(response.status === 201, `post ${body.kind} notice: ${response.status}`);
      return (await response.json()).notice;
    };
    // The connection state stays on screen beside the banner (sticky footer).
    const connInView = async name => {
      const box = await conn.evaluate(el => { const r = el.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, h: innerHeight }; });
      assert(box.top >= 0 && box.bottom <= box.h, `${name}: #conn is off screen: ${JSON.stringify(box)}`);
    };
    const phone = async name => {
      await page.setViewportSize({ width: 390, height: 844 });
      const width = await page.evaluate(() => document.documentElement.scrollWidth);
      assert(width <= 390, `${name}: page scrolls sideways at 390 px (${width})`);
      const lines = await banner.locator('.nb').evaluateAll(els => els.map(el => el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight)));
      assert(lines.every(n => n <= 3.05), `${name}: a banner row wraps past 3 lines: ${lines}`);
      await shot(name);
      await page.setViewportSize({ width: 1440, height: 900 });
    };

    // Initial: signed in, live, the notices fetched, and no banner.
    await conn.locator('span', { hasText: /^live$/ }).waitFor();
    await page.locator('#me .who').waitFor();
    await until('GET /v1/hub/notices at sign-in', async () => countRequests('/v1/hub/notices') > 0);
    assert(await banner.isHidden(), 'banner shown with no notice');
    await shot('nt09-initial');

    // A hostile message renders literally: no element, the exact text.
    const at = hours => new Date(Date.now() + hours * 3600e3).toISOString();
    await post({ kind: 'maintenance', message: hostile });
    await rows.filter({ hasText: 'onerror' }).waitFor();
    assert(await banner.locator('img, script, iframe').count() === 0, 'hostile message made an element');
    assert(await banner.locator('.tx').first().textContent() === hostile, 'hostile message not shown literally');

    // Moving and maintenance (which replaces the hostile one): the banner
    // shows exactly the API's notices, moving first.
    await post({ ...maintenance, at: at(2) });
    await post({ ...moving, at: at(3) });
    await rows.filter({ hasText: maintenance.message }).waitFor();
    await rows.filter({ hasText: moving.message }).waitFor();
    const api = (await (await asPeer('operator', 'GET', '/v1/hub/notices')).json()).notices;
    const want = api.slice().sort((a, b) => RANK[a.kind] - RANK[b.kind]).map(n => n.message);
    const got = await banner.locator('.tx').allTextContents();
    assert(JSON.stringify(got) === JSON.stringify(want), `banner ${JSON.stringify(got)} != API ${JSON.stringify(want)}`);
    const link = banner.locator('.ntc.k-moving a');
    assert(await link.textContent() === 'vecnet.xk.io', 'moving link text is not the host');
    assert(await link.getAttribute('href') === 'https://vecnet.xk.io', 'moving link href');
    assert((await link.getAttribute('rel')).split(' ').includes('noopener'), 'moving link lacks rel=noopener');
    await connInView('nt09-success-desktop');
    await shot('nt09-success-desktop');
    await phone('nt09-success-phone');

    // Restart: the reconnects are held, so no ready arrives until released.
    const releaseActivity = await hold('**/v1/admin/activity');
    const releaseEvents = await hold('**/v1/events*');
    await until('a connected stream to take the restart', async () => (await restart(5000)).streams > 0);
    const restarting = rows.filter({ hasText: /^Hub restarting: reconnecting/ });
    await restarting.waitFor();
    await conn.locator('span', { hasText: /^restarting$/ }).waitFor();
    assert(await page.locator('#conn.warn').count() === 1, 'conn is not amber while restarting');
    assert(await rows.count() === 3, 'notices hidden by the restart row');
    await connInView('nt09-progress-desktop');
    await shot('nt09-progress-desktop');
    await phone('nt09-progress-phone');
    await releaseActivity();
    await releaseEvents();
    await restarting.waitFor({ state: 'detached' });
    await conn.locator('span', { hasText: /^live$/ }).waitFor();
    assert(await rows.count() === 2, 'notices lost across the restart');

    // An outage after the restart: no ready by retry + 30 s is unreachable.
    await fail('**/v1/**', 503);
    await until('a connected stream to take the second restart', async () => (await restart(5000)).streams > 0);
    await restarting.waitFor();
    await page.clock.fastForward('00:40');
    const unreachable = rows.filter({ hasText: /^Hub unreachable since \d\d:\d\d$/ });
    await unreachable.waitFor();
    assert(await page.locator('.ntc.bad').count() === 1, 'unreachable row is not red');
    // master's quiet reconnect: an outage reads as reconnecting (#conn.retry), not offline.
    await conn.locator('span', { hasText: /^reconnecting$/ }).waitFor();
    assert(await page.locator('#conn.retry').count() === 1, 'conn is not reconnecting after the restart grace');
    await connInView('nt09-error-desktop');
    await shot('nt09-error-desktop');
  });
  console.log('notice banner passed: none, hostile text, moving + maintenance (desktop, phone), restarting, unreachable');
} finally { await cleanBuild(); }
