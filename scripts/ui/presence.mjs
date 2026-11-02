// Task 14: one-tap operator presence and its heartbeat, in a real browser.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const isPresencePost = req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/presence';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, fail, allowConsole, countRequests } = ctx;
    // Chromium logs every non-2xx fetch as a console error: the 500 below, and
    // the views other tasks have not filled in yet.
    allowConsole(/status of 500/);
    allowConsole(/status of 501/);

    const hubState = async () => (await (await asPeer('operator', 'GET', '/v1/admin/inbox?limit=1')).json()).me.presence;
    const me = page.locator('#me');
    const label = me.locator('.who');
    const toggle = me.locator('button.ptog');
    const posts = () => countRequests(isPresencePost);

    // The heartbeat runs on the page's clock, so this page gets a controllable
    // one. It still flows in real time until the test jumps it forward.
    await page.clock.install();
    await page.reload({ waitUntil: 'load' });
    // lib.mjs hold() unroutes as it releases; a second route keeps Playwright's
    // interception on so the held request is continued exactly once.
    await page.route('**/__keep-interception__', route => route.continue());

    // Initial: the seed's operator is working, in the nav and the inbox aside.
    await label.filter({ hasText: /^max is working$/ }).waitFor();
    await me.locator('.dot.working').waitFor();
    await toggle.filter({ hasText: 'Set idle' }).waitFor();
    const aside = page.locator('#aside');
    await aside.locator('.pres', { hasText: 'working since' }).waitFor();
    await aside.locator('.pnote', { hasText: 'Peers see this in the directory. Only wake messages fire your notifications.' }).waitFor();
    assert(!(await aside.textContent()).includes('wait while you work'), 'aside reuses the prototype\'s untrue copy');
    assert(posts() === 0, 'presence posted before any tap');
    await shot('14-presence-initial');

    // toggle posts once and the nav updates after 204.
    const release = await hold('**/v1/presence');
    await toggle.dblclick();
    await toggle.filter({ hasText: 'Setting…' }).waitFor();
    assert(await toggle.getAttribute('title') === 'Setting idle', 'busy toggle does not name its target');
    assert(await toggle.isDisabled(), 'toggle stays enabled while its POST is in flight');
    assert(await label.textContent() === 'max is working', 'nav changed before the hub answered');
    assert(await me.locator('.dot.working').count() === 1, 'dot changed before the hub answered');
    assert(posts() === 1, `double tap sent ${posts()} presence POSTs`);
    await shot('14-presence-progress');
    await release();
    await label.filter({ hasText: /^max is idle$/ }).waitFor();
    await toggle.filter({ hasText: 'Set working' }).waitFor();
    await aside.locator('.pres', { hasText: 'idle since' }).waitFor();
    assert(posts() === 1, `toggle sent ${posts()} presence POSTs`);
    assert(await hubState() === 'idle', 'hub did not record idle');
    await shot('14-presence-success');

    // a second tab's change wins: the first tab's heartbeat re-posts the hub's
    // state (Review Focus 4). The second client has its own real clock.
    const other = await page.context().browser().newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
    try {
      await other.addInitScript(secret => localStorage.setItem('vecnet.ui.secret', secret), demo.operator_secret);
      const tab2 = await other.newPage();
      tab2.setDefaultTimeout(60000);
      await tab2.goto(`${demo.url}/ui/`, { waitUntil: 'load' });
      await tab2.locator('#me .who', { hasText: /^max is idle$/ }).waitFor();
      await tab2.locator('#me button.ptog', { hasText: 'Set working' }).click();
      await tab2.locator('#me .who', { hasText: /^max is working$/ }).waitFor();
      assert(await hubState() === 'working', 'second tab did not set working');

      const beat = page.waitForRequest(isPresencePost);
      await page.clock.fastForward('20:00');
      const body = (await beat).postDataJSON();
      assert(body.state === 'working', `first tab's heartbeat re-posted ${body.state}, not the hub's working`);
      assert(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(body.at), `heartbeat at is ${body.at}`);
      await label.filter({ hasText: /^max is working$/ }).waitFor();
      assert(await hubState() === 'working', 'heartbeat overwrote the second tab\'s choice');
      assert(await tab2.locator('#me .who').textContent() === 'max is working', 'second tab changed');

      // A heartbeat that has read working and is still in flight when the
      // second tab sets idle must not undo that newer choice when it lands.
      const releaseBeat = await hold('**/v1/presence');
      const heldBeat = page.waitForRequest(isPresencePost);
      await page.clock.fastForward('20:00');
      const heldBody = (await heldBeat).postDataJSON();
      assert(heldBody.state === 'working', `held heartbeat re-posted ${heldBody.state}`);
      await tab2.locator('#me button.ptog', { hasText: 'Set idle' }).click();
      await tab2.locator('#me .who', { hasText: /^max is idle$/ }).waitFor();
      assert(await hubState() === 'idle', 'second tab did not set idle');
      const landed = page.waitForResponse(res => isPresencePost(res.request()));
      await releaseBeat();
      assert((await landed).ok(), 'held heartbeat failed');
      assert(await hubState() === 'idle', 'a held heartbeat overwrote the second tab\'s newer choice');
      await label.filter({ hasText: /^max is idle$/ }).waitFor();

      // Back to working for the failure check below.
      await tab2.locator('#me button.ptog', { hasText: 'Set working' }).click();
      await label.filter({ hasText: /^max is working$/ }).waitFor();
    } finally { await other.close(); }

    // failure leaves the old state with a toast.
    const before = posts();
    await fail('**/v1/presence', 500, 'internal', 'Hub write failed');
    await toggle.filter({ hasText: 'Set idle' }).click();
    await page.locator('#toast.on.bad', { hasText: 'Presence unchanged: Hub write failed' }).waitFor();
    await toggle.filter({ hasText: 'Set idle' }).waitFor();
    assert(!(await toggle.isDisabled()), 'toggle stayed disabled after a failure');
    assert(await label.textContent() === 'max is working', 'nav changed after a failed POST');
    assert(posts() === before + 1, `failed toggle sent ${posts() - before} POSTs`);
    assert(await hubState() === 'working', 'hub state changed after a 500');
    await page.evaluate(() => Promise.all(document.getElementById('toast').getAnimations().map(a => a.finished)));
    await shot('14-presence-error');
    await page.unroute('**/v1/presence');
  });
  console.log('Presence passed: one POST per tap, nav after 204, heartbeat re-posts the hub\'s state, failure keeps state with a toast');
} finally { await cleanBuild(); }
