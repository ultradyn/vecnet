// Real embedded app + isolated demo DB; deterministic intercepted metadata only.
import { assert, cleanBuild, withDemo } from './lib.mjs';

function snapshot(ids, empty = false) {
  const peers = Object.fromEntries(ids.map((id, j) => {
    const buckets = Array.from({ length: 60 }, (_, i) => empty ? 0 : j === 0 ? (i > 35 && i < 43 ? 18 + (i % 3) * 7 : i % 7 === 0 ? 8 : 2) : j === 1 && i % 9 === 0 ? 4 : 0);
    const messages = buckets.reduce((a, b) => a + b, 0);
    return [id, { messages, messages_per_minute: messages / 60, buckets }];
  }));
  const buckets = Array.from({ length: 60 }, (_, i) => Object.values(peers).reduce((sum, p) => sum + p.buckets[i], 0)), messages = buckets.reduce((a, b) => a + b, 0);
  return { now: '2026-11-09T12:00:30.000Z', window_start: '2026-11-09T11:00:30.000Z', window_end: '2026-11-09T12:00:30.000Z', bucket_seconds: 60, messages, messages_per_minute: messages / 60, active_peers: Object.entries(peers).filter(([id, p]) => id.startsWith('peer_') && p.messages).length, buckets, peers };
}

try {
  await withDemo({}, async ({ page, asPeer, shot, allowConsole, countRequests, demo }) => {
    allowConsole(/status of 500/);
    // Exercise the actual endpoint before installing the deterministic chart fixture.
    const real = await asPeer('operator', 'GET', '/v1/admin/activity-stats');
    assert(real.ok && real.headers.get('cache-control') === 'no-store', 'real activity endpoint/cache control');
    const actual = await real.json();
    assert(actual.buckets.length === 60 && actual.messages === actual.buckets.reduce((a, b) => a + b, 0), 'real snapshot totals');
    const directory = await (await asPeer('operator', 'GET', '/v1/admin/peers')).json();
    const ids = directory.peers.map(p => p.peer.peer_id).filter(id => id.startsWith('peer_'));
    const value = snapshot(ids);
    let mode = 'hold', release;
    let gate = new Promise(r => { release = r; });
    await page.route('**/v1/admin/activity-stats', async route => {
      if (mode === 'hold') await gate;
      if (mode === 'error') return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'internal', message: 'Activity query failed', details: {} } }) });
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(mode === 'empty' ? snapshot(ids, true) : value) });
    });
    // Keep 30s callbacks controllable without exposing test hooks in production.
    await page.addInitScript(() => {
      const set = window.setInterval;
      window.__activityTicks = new Map();
      window.setInterval = (fn, ms, ...args) => { const id = set(fn, ms, ...args); if (ms === 30000) window.__activityTicks.set(id, fn); return id; };
      const clear = window.clearInterval;
      window.clearInterval = id => { window.__activityTicks.delete(id); clear(id); };
    });
    const load = async () => { await page.goto(demo.url + '/ui/#dash', { waitUntil: 'load' }); await page.reload({ waitUntil: 'load' }); await page.locator('.dash-activity').waitFor(); };
    const ready = () => page.locator('.dash-activity[data-state=ready]').waitFor();
    const refresh = async () => { await page.evaluate(() => { for (const fn of window.__activityTicks.values()) fn(); }); };
    const status = page.locator('.activity-status');
    await load();
    await page.locator('.activity-count', { hasText: '—' }).waitFor();
    assert((await status.textContent()).includes('Loading activity'), 'initial unknown is not zero');
    assert(!await page.locator('.activity-chart svg').isVisible(), 'initial unknown must not draw a zero baseline');
    await page.locator('.dash-peers tbody tr').first().waitFor();
    await shot('activity-initial-loading-1440');
    mode = 'ready'; release(); await ready();
    assert(await page.locator('.activity-count').textContent() === value.messages.toLocaleString('en-US'), 'headline count');
    assert(await page.locator('.activity-active').textContent() === '2', 'active distinct peers, not directory count');
    const plot = page.locator('.activity-chart svg');
    assert(await plot.getAttribute('xmlns') === null || await plot.evaluate(el => el.namespaceURI === 'http://www.w3.org/2000/svg'), 'namespaced SVG');
    await shot('activity-dark-1440');

    await plot.focus(); await page.keyboard.press('Home');
    await page.locator('.activity-chart .spark-readout', { hasText: '11:00:30.000Z' }).waitFor();
    await page.keyboard.press('ArrowRight');
    await page.locator('.activity-chart .spark-readout', { hasText: '11:01:30.000Z' }).waitFor();
    await page.keyboard.press('End'); await page.keyboard.press('ArrowRight');
    await page.locator('.activity-chart .spark-readout', { hasText: '11:59:30.000Z' }).waitFor();
    await shot('activity-keyboard-1440');
    await page.keyboard.press('Escape');
    assert(!await page.locator('.activity-chart .spark-readout').isVisible(), 'Escape dismisses readout');
    const box = await plot.boundingBox(); await page.mouse.move(box.x + 4, box.y + box.height / 2);
    await page.locator('.activity-chart .spark-readout', { hasText: '11:00:30.000Z' }).waitFor();
    await shot('activity-pointer-1440');
    await page.mouse.move(5, 5);

    const detail = page.locator('.activity-chart details');
    await detail.locator('summary').click();
    assert(await detail.locator('tbody tr').count() === 60, 'exact minute table');
    assert((await detail.locator('tbody tr').first().textContent()).includes('2026-11-09T11:01:30.000Z'), 'exact table end');
    await shot('activity-table-1440');
    await detail.locator('summary').focus();
    await page.evaluate(() => { window.__chart = document.querySelector('.activity-chart'); window.__summary = document.activeElement; });
    await refresh(); await ready();
    assert(await page.evaluate(() => window.__chart === document.querySelector('.activity-chart') && window.__summary === document.activeElement && document.querySelector('.activity-chart details').open), 'table/focus retained during independent refresh');
    await detail.locator('summary').click();

    // Peer chart and keyboard cursor survive directory and activity refreshes.
    const peerPlot = page.locator('.peer-activity svg').first();
    await peerPlot.scrollIntoViewIfNeeded(); await peerPlot.focus(); await page.keyboard.press('Home');
    await page.evaluate(() => { window.__peerPlot = document.activeElement; });
    await refresh(); await ready();
    assert(await page.evaluate(() => document.activeElement === window.__peerPlot && window.__peerPlot.isConnected), 'peer plot identity/focus lost on refresh');
    const maxima = await page.locator('.peer-activity svg').evaluateAll(xs => xs.map(x => x.getAttribute('data-max')));
    assert(new Set(maxima).size === 1, 'peer plots do not share a common max');
    await page.locator('#main').evaluate(el => { el.scrollTop = 0; }); await plot.focus();

    mode = 'hold'; gate = new Promise(r => { release = r; });
    await refresh(); await status.filter({ hasText: 'Refreshing' }).waitFor();
    assert(await page.locator('.activity-count').textContent() === value.messages.toLocaleString('en-US'), 'refetch blanked count');
    await shot('activity-refreshing-1440');
    mode = 'error'; release();
    await page.locator('.dash-activity[data-state=stale]').waitFor();
    assert((await status.textContent()).includes('Stale'), 'stale not marked');
    assert((await page.locator('.peer-activity-summary').first().textContent()).includes('stale'), 'peer stale not marked');
    await shot('activity-stale-1440');
    mode = 'ready'; await refresh(); await ready();

    // Explicit light chart surface counteracts the app's legacy root inversion.
    await page.evaluate(() => document.documentElement.dataset.theme = 'light');
    const light = await page.locator('.dash-activity').evaluate(el => ({ surface: getComputedStyle(el).backgroundColor, trace: getComputedStyle(el.querySelector('.spark-line')).stroke, filter: getComputedStyle(el).filter }));
    assert(light.surface === 'rgb(255, 255, 255)' && light.trace === 'rgb(42, 120, 214)' && light.filter !== 'none', 'selected light chart tokens');
    console.log('Light chart computed tokens', JSON.stringify(light));
    await shot('activity-light-1440');
    await page.evaluate(() => document.documentElement.dataset.theme = 'dark');
    const dark = await page.locator('.dash-activity').evaluate(el => ({ surface: getComputedStyle(el).backgroundColor, trace: getComputedStyle(el.querySelector('.spark-line')).stroke }));
    assert(dark.surface === 'rgb(16, 22, 42)' && dark.trace === 'rgb(57, 135, 229)', 'selected dark chart tokens');
    console.log('Dark chart computed tokens', JSON.stringify(dark));
    await page.locator('body').click({ position: { x: 1000, y: 30 } });
    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      await page.locator('#main').evaluate(el => { el.scrollTop = 0; el.scrollLeft = 0; });
      await page.locator('.dash h1').evaluate(el => el.scrollIntoView({ block: 'start' }));
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth && document.body.scrollWidth <= innerWidth), `${width}: page overflow`);
      const bounds = await page.locator('.dash-activity').boundingBox();
      assert(bounds.x >= 0 && bounds.x + bounds.width <= width + 1, `${width}: activity overflow`);
      await shot(`activity-dark-${width}`);
      await page.locator('.activity-chart summary').click();
      assert(await page.locator('.activity-chart tbody tr').count() === 60, `${width}: missing minute table`);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${width}: table caused page overflow`);
      await shot(`activity-table-${width}`);
      await page.locator('.activity-chart summary').click();
      // The wide peers table scrolls within its region, not the document.
      await page.locator('.peer-table-scroll').evaluate(el => { el.scrollLeft = el.scrollWidth; });
      const narrowPeer = page.locator('.peer-activity svg').first();
      await narrowPeer.focus(); await page.keyboard.press('Home');
      assert(await page.locator('.peer-activity .spark-readout').first().isVisible(), `${width}: peer cursor inaccessible`);
      await shot(`activity-peer-${width}`);
      await page.keyboard.press('Escape');
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    mode = 'empty'; await refresh(); await ready();
    assert(await page.locator('.activity-count').textContent() === '0', 'empty is zero');
    assert((await plot.getAttribute('aria-label')).includes('0 messages'), 'empty chart accessible summary');
    await shot('activity-empty-1440');

    mode = 'error'; await load();
    await page.locator('.dash-activity[data-state=error]').waitFor();
    assert(await page.locator('.activity-count').textContent() === '—', 'initial error is fake zero');
    await page.locator('.dash-peers tbody tr').first().waitFor();
    assert(await page.locator('.dash-peers .empty.error').count() === 0, 'activity failure spread to peers');
    await shot('activity-isolated-error-1440');
    mode = 'ready'; await refresh(); await ready();
    const beforeLeave = countRequests('/v1/admin/activity-stats');
    await page.evaluate(() => { location.hash = '#settings'; });
    await page.locator('.dash').waitFor({ state: 'detached' });
    await page.evaluate(() => { for (const fn of window.__activityTicks.values()) fn(); });
    assert(countRequests('/v1/admin/activity-stats') === beforeLeave, 'activity resource leaked after unmount');
    console.log('Activity browser passed: real API; unknown/empty/error/stale/recovery; keyboard/pointer/table; peer shared scale and DOM/focus retention; refresh/unmount; 1440 dark/light, 390/320, no CSP/browser errors');
  });
} finally { await cleanBuild(); }
