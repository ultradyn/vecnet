// Task 12: the provisional read-only dashboard, in a real browser.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const WRITE = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);
// The shell owns the ack and the presence heartbeat; neither is the dashboard's.
const SHELL_WRITES = /\/v1\/(ack|presence)(\?|$)/;

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, fail, allowConsole, countRequests } = ctx;
    // Chromium logs each non-2xx fetch; this is the failure the script provokes.
    allowConsole(/status of 500/);
    const writes = () => countRequests(req => WRITE.has(req.method()) && !SHELL_WRITES.test(new URL(req.url()).pathname));
    const dash = page.locator('.dash');
    const sec = key => page.locator('.dash-' + key);

    await page.locator('#conn.live').waitFor();
    await page.locator('#threads a').first().waitFor();

    // See shell.mjs: a second route keeps interception on as holds unroute.
    await page.route('**/__keep-interception__', route => route.continue());
    const releases = {};
    for (const [key, glob] of Object.entries({
      peers: '**/v1/admin/peers', threads: '**/v1/admin/threads*', health: '**/v1/admin/delivery-health',
      dead: '**/v1/admin/dead-letters', cuts: '**/v1/admin/loop-cuts*', counters: '**/v1/admin/counters', audit: '**/v1/admin/audit*',
    })) releases[key] = await hold(glob);

    const writesBefore = writes();
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    for (const name of ['Peers', 'Threads', 'Delivery health', 'Dead letters', 'Loop control', 'Subscriptions', 'Audit tail']) {
      await dash.locator('h2', { hasText: name }).waitFor();
    }
    assert(await dash.locator('.loading').count() === 6, 'every section should be loading while its read is held');
    assert((await page.locator('.dash-counters').textContent()).includes('Loading counters'), 'counter line not loading');
    await shot('12-dash-initial');

    // Sections load independently: some answered, the rest still held.
    for (const key of ['counters', 'dead', 'cuts', 'audit']) await releases[key]();
    await sec('dead').locator('tbody tr').first().waitFor();
    await sec('loop').locator('.total').waitFor();
    await sec('audit').locator('.audit .ln').first().waitFor();
    await page.locator('.dash-counters', { hasText: 'loop cut 3' }).waitFor();
    for (const key of ['peers', 'threads', 'health']) {
      assert(await sec(key).locator('.loading').count() === 1, `${key} stopped loading while held`);
    }
    await shot('12-dash-progress');

    for (const key of ['peers', 'threads', 'health']) await releases[key]();
    await sec('peers').locator('tbody tr').first().waitFor();
    await sec('threads').locator('tbody tr').first().waitFor();
    await sec('health').locator('ul.blocked li').first().waitFor();
    assert(await dash.locator('.loading').count() === 0, 'a section is still loading');
    assert(await dash.locator('.empty.error').count() === 0, 'a section failed on a healthy hub');

    // Presence and reachability are separate; unknown is never offline (§8).
    const peerRow = name => sec('peers').locator('tbody tr', { has: page.locator('td:first-child > span:first-child', { hasText: new RegExp('^' + name + '$') }) });
    const dotRow = peerRow('dot-research');
    assert((await dotRow.locator('td').nth(2).textContent()).startsWith('unknown'), 'dot presence not unknown');
    assert((await dotRow.locator('td').nth(3).textContent()).startsWith('reachable'), 'dot not reachable');
    assert((await peerRow('gh-ultradyn').locator('td').nth(2).textContent()).startsWith('n/a'), 'source presence not n/a');
    assert((await peerRow('grok-triage').locator('td').nth(3).textContent()).startsWith('unreachable'), 'unreachable peer not shown');
    assert(await sec('peers').locator('td', { hasText: 'offline' }).count() === 0, 'a peer read as offline');
    // Bodies are untrusted: the hostile name is inert text.
    await sec('peers').locator('td', { hasText: '<img src=x onerror=alert(1)>' }).first().waitFor();
    assert(await page.locator('img').count() === 0, 'a peer name became an element');

    // Delivery health names the blocked recipient, delivery and queue (§16.4).
    const blocked = await sec('health').locator('ul.blocked li').first().textContent();
    assert(/grok-triage: thread .* blocked on delivery dly_/.test(blocked) && /\d+ waiting/.test(blocked), `blocked line: ${blocked}`);
    await sec('health').locator('td.bad', { hasText: /^409$/ }).waitFor();
    assert(await sec('dead').locator('tbody tr').count() === demo.dead_letters.length, 'dead letters do not match the hub');
    await sec('dead').getByRole('link', { name: 'Act on these in Inbox' }).waitFor();
    assert((await sec('loop').locator('.total').textContent()).startsWith('3 cuts'), 'loop cut total');
    assert(await sec('loop').locator('tbody tr').count() === 3, 'loop cut reasons');

    // thread rows link to #thread/<id>
    const hub = await (await asPeer('operator', 'GET', '/v1/admin/threads?limit=200')).json();
    const ids = hub.threads.map(r => r.thread.thread_id).sort();
    const hrefs = (await sec('threads').locator('tbody tr a.tl').evaluateAll(as => as.map(a => a.getAttribute('href')))).sort();
    assert(hrefs.length === ids.length, `thread rows ${hrefs.length}, hub threads ${ids.length}`);
    assert(hrefs.every((href, i) => href === '#thread/' + ids[i]), `thread links: ${hrefs.join(' ')}`);

    await page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    await shot('12-dash-success');

    // The peers tag reloads the dashboard: a quota change is directory_updated.
    const peerReads = countRequests('/v1/admin/peers');
    const posted = await asPeer('codex', 'POST', '/v1/presence', { state: 'working', at: new Date().toISOString(), quota: { weekly_remaining_pct: 44 } });
    assert(posted.ok, `presence for codex: ${posted.status}`);
    await peerRow('codex-hark').locator('td', { hasText: /^44%$/ }).waitFor();
    assert(countRequests('/v1/admin/peers') > peerReads, 'peers tag did not reload the dashboard');

    // Load older appears only when the tail has more; it is a read.
    const older = sec('audit').getByRole('button', { name: 'Load older' });
    if (await older.count()) {
      const lines = await sec('audit').locator('.audit .ln').count();
      await older.click();
      await page.waitForFunction(n => document.querySelectorAll('.dash-audit .audit .ln').length > n, lines);
    }

    // one failing section shows its own error while the others render
    await fail('**/v1/admin/delivery-health', 500, 'internal', 'Delivery health query failed');
    await page.reload({ waitUntil: 'load' });
    await sec('health').locator('.empty.error', { hasText: 'Delivery health unavailable' }).waitFor();
    await sec('health').locator('.empty.error', { hasText: 'Delivery health query failed' }).waitFor();
    await sec('peers').locator('tbody tr').first().waitFor();
    await sec('threads').locator('tbody tr').first().waitFor();
    await sec('dead').locator('tbody tr').first().waitFor();
    await sec('loop').locator('.total').waitFor();
    await sec('audit').locator('.audit .ln').first().waitFor();
    await page.locator('.dash-counters', { hasText: 'loop cut' }).waitFor();
    assert(await dash.locator('.empty.error').count() === 1, 'the failure spread beyond its section');
    assert(await dash.locator('.loading').count() === 0, 'a section is stuck loading');
    await sec('threads').locator('h2').scrollIntoViewIfNeeded();
    await page.locator('#main').evaluate(el => {
      const target = document.getElementById('d-health');
      el.scrollTop += target.getBoundingClientRect().top - 260;
    });
    await shot('12-dash-error');

    // no POST, PUT, PATCH or DELETE leaves the page in a full dashboard session
    assert(writes() === writesBefore, `dashboard sent ${writes() - writesBefore} write(s)`);
    assert(await page.getByRole('button', { name: /retry|discard|resume/i }).count() === 0, 'the dashboard offers a write');

    // A thread row opens the thread.
    const first = sec('threads').locator('tbody tr a.tl').first();
    const href = await first.getAttribute('href');
    await first.click();
    await page.waitForFunction(h => location.hash === h && document.body.dataset.route === 'thread', href);
  });
  console.log('Dashboard passed: independent sections, read-only session, peers-tag reload, thread links, isolated failure, no CSP violations');
} finally { await cleanBuild(); }
