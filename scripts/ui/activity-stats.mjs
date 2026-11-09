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

    // The overview inspection must participate in layout, not cover the next
    // metric row on mobile or the explanatory status on desktop.
    const collisions = [];
    for (const theme of ['dark', 'light']) {
      await page.evaluate(theme => { document.activeElement?.blur(); document.documentElement.dataset.theme = theme; }, theme);
      for (const width of [390, 320, 1440]) {
        await page.setViewportSize({ width, height: 900 });
        await page.evaluate(() => document.activeElement?.blur());
        await page.locator('#main').evaluate(el => { el.scrollTop = 0; el.scrollLeft = 0; });
        await page.locator('.dash h1').evaluate(el => el.scrollIntoView({ block: 'start' }));
        await page.evaluate(() => document.fonts.ready);
        assert(!await page.locator('.activity-chart .spark-readout').isVisible(), `${theme}/${width}: blur did not restore quiet state`);
        await shot(`activity-layout-${theme}-${width}-quiet`);
        await plot.focus(); await page.keyboard.press('Home');
        await page.evaluate(() => { window.__inspectionPlot = document.activeElement; window.__inspectionChart = document.querySelector('.activity-chart'); });
        const snapshotResponse = page.waitForResponse(response => response.url().endsWith('/v1/admin/activity-stats'));
        await refresh(); await snapshotResponse; await ready();
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        assert(await page.evaluate(() => window.__inspectionPlot === document.activeElement && window.__inspectionChart === document.querySelector('.activity-chart')), `${theme}/${width}: snapshot replaced chart or lost focus`);
        const readout = await page.locator('.activity-chart .spark-readout').boundingBox();
        const regions = {
          plot: await plot.boundingBox(),
          axis: await page.locator('.activity-chart .spark-axis').boundingBox(),
          messagesPerMinute: await page.locator('.activity-metric').nth(0).boundingBox(),
          activePeers: await page.locator('.activity-metric').nth(1).boundingBox(),
          status: await status.boundingBox(),
        };
        console.log('Overview inspection bounds', JSON.stringify({ theme, width, readout, regions }));
        assert(readout && readout.width > 0 && readout.height > 0, `${theme}/${width}: inspection missing`);
        assert(readout.x >= 0 && readout.x + readout.width <= width + 1, `${theme}/${width}: readout overflow`);
        for (const [name, region] of Object.entries(regions)) {
          assert(region && region.width > 0 && region.height > 0, `${theme}/${width}: ${name} missing`);
          const intersects = readout.x < region.x + region.width && readout.x + readout.width > region.x
            && readout.y < region.y + region.height && readout.y + readout.height > region.y;
          if (intersects) collisions.push(`${theme}/${width}: readout overlaps entire ${name} region`);
        }
        assert((await page.locator('.activity-chart .spark-readout').textContent()).startsWith(`${value.buckets[0]} messages`), `${theme}/${width}: exact Home count`);
        assert(await page.locator('.activity-chart .spark-cursor').getAttribute('visibility') === 'visible', `${theme}/${width}: crosshair missing`);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth && document.body.scrollWidth <= innerWidth), `${theme}/${width}: page overflow`);
        await shot(`activity-layout-${theme}-${width}-home`);
        await page.keyboard.press('Escape');
        assert(!await page.locator('.activity-chart .spark-readout').isVisible(), `${theme}/${width}: Escape did not restore quiet state`);
        assert(await page.locator('.activity-chart .spark-cursor').getAttribute('visibility') === 'hidden', `${theme}/${width}: Escape left cursor visible`);
        await page.keyboard.press('End');
        await plot.evaluate(el => el.blur());
        assert(!await page.locator('.activity-chart .spark-readout').isVisible(), `${theme}/${width}: blur left inspection open`);
      }
    }
    assert(collisions.length === 0, `Overview inspection collisions:\n${collisions.join('\n')}`);
    await page.evaluate(() => { document.documentElement.dataset.theme = 'dark'; });
    await page.setViewportSize({ width: 1440, height: 900 });

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
    assert((await page.locator('.activity-chart .spark-readout').textContent()).startsWith(`${value.buckets[0]} messages`), 'exact pointer count');
    await shot('activity-pointer-1440');
    await page.mouse.move(5, 5);

    assert(await page.locator('.sparkline :is(details, summary, table)').count() === 0, 'raw minute disclosures must not exist, even hidden');
    assert((await page.locator('.activity-overview').textContent()).includes('Hourly average'), 'hourly average must not read as instantaneous rate');
    assert(!/Snapshot|\d{4}-\d\d-\d\dT/.test(await status.textContent()), 'routine metadata must not be an ISO timestamp wall');
    assert((await page.locator('.activity-chart .spark-axis').textContent()) === '1 hour agoNow', 'shared hour labels');
    await plot.focus(); await page.keyboard.press('Home');
    assert((await page.locator('.activity-chart .spark-readout').textContent()).startsWith(`${value.buckets[0]} messages`), 'exact keyboard count');
    await page.evaluate(() => { window.__chart = document.querySelector('.activity-chart'); window.__plot = document.activeElement; });
    await refresh(); await ready();
    assert(await page.evaluate(() => window.__chart === document.querySelector('.activity-chart') && window.__plot === document.activeElement), 'plot/focus retained during independent refresh');
    assert((await page.locator('.activity-chart .spark-readout').textContent()).startsWith(`${value.buckets[0]} messages`), 'exact keyboard count retained across refresh');
    assert(await plot.evaluate(el => getComputedStyle(el).outlineStyle !== 'none'), 'visible keyboard focus');
    assert(await page.locator('.activity-chart .spark-cursor').getAttribute('visibility') === 'visible', 'keyboard crosshair visible');

    // Peer chart and keyboard cursor survive directory and activity refreshes.
    const peerPlot = page.locator('.peer-activity svg').first();
    await peerPlot.scrollIntoViewIfNeeded(); await peerPlot.focus(); await page.keyboard.press('Home');
    await page.evaluate(() => { window.__peerPlot = document.activeElement; });
    await refresh(); await ready();
    assert(await page.evaluate(() => document.activeElement === window.__peerPlot && window.__peerPlot.isConnected), 'peer plot identity/focus lost on refresh');
    await shot('activity-peer-1440');
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
    await page.evaluate(() => { document.activeElement?.blur(); document.documentElement.dataset.theme = 'light'; });
    const light = await page.locator('.dash-activity').evaluate(el => ({ surface: getComputedStyle(el).backgroundColor, trace: getComputedStyle(el.querySelector('.spark-line')).stroke, filter: getComputedStyle(el).filter }));
    assert(light.surface === 'rgb(236, 240, 250)' && light.trace === 'rgb(42, 120, 214)' && light.filter !== 'none', 'selected light chart tokens match the rendered page surface');
    console.log('Light chart computed tokens', JSON.stringify(light));
    await shot('activity-light-1440');
    await page.evaluate(() => document.documentElement.dataset.theme = 'dark');
    const dark = await page.locator('.dash-activity').evaluate(el => ({ surface: getComputedStyle(el).backgroundColor, trace: getComputedStyle(el.querySelector('.spark-line')).stroke }));
    assert(dark.surface === 'rgb(11, 15, 25)' && dark.trace === 'rgb(57, 135, 229)', 'selected dark chart tokens');
    console.log('Dark chart computed tokens', JSON.stringify(dark));
    await page.locator('body').click({ position: { x: 1000, y: 30 } });
    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      await page.evaluate(() => document.activeElement?.blur());
      await page.locator('.peer-table-scroll').evaluate(el => { el.scrollLeft = 0; });
      await page.locator('#main').evaluate(el => { el.scrollTop = 0; el.scrollLeft = 0; });
      await page.locator('.dash h1').evaluate(el => el.scrollIntoView({ block: 'start' }));
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth && document.body.scrollWidth <= innerWidth), `${width}: page overflow`);
      const bounds = await page.locator('.dash-activity').boundingBox();
      assert(bounds.x >= 0 && bounds.x + bounds.width <= width + 1, `${width}: activity overflow`);
      await shot(`activity-dark-${width}`);
      assert(await page.locator('.sparkline :is(details, summary, table)').count() === 0, `${width}: raw disclosures returned`);
      await plot.focus(); await page.keyboard.press('Home');
      assert((await page.locator('.activity-chart .spark-readout').textContent()).startsWith(`${value.buckets[0]} messages`), `${width}: exact keyboard count`);
      const axis = await page.locator('.activity-chart .spark-axis').boundingBox(), plotBounds = await plot.boundingBox();
      assert(axis.y >= plotBounds.y + plotBounds.height, `${width}: axis collides with plot`);
      const readoutBounds = await page.locator('.activity-chart .spark-readout').boundingBox();
      assert(readoutBounds.y >= axis.y + axis.height, `${width}: readout hides the time axis`);
      await shot(`activity-keyboard-${width}`);
      await page.keyboard.press('Escape');
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
    console.log('Activity browser passed: real API; unknown/empty/error/stale/recovery; exact keyboard/pointer counts, no raw disclosures; peer shared scale and DOM/focus retention; refresh/unmount; 1440 dark/light, 390/320, no CSP/browser errors');
  });
} finally { await cleanBuild(); }
