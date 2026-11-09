// Task 12: the provisional read-only dashboard, in a real browser.
import { assert, cleanBuild, scrollIntoViewStable, withDemo } from './lib.mjs';

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
      activity: '**/v1/admin/activity-stats', peers: '**/v1/admin/peers', threads: '**/v1/admin/threads*', health: '**/v1/admin/delivery-health',
      subs: '**/v1/admin/subscriptions',
      dead: '**/v1/admin/dead-letters', cuts: '**/v1/admin/loop-cuts*', counters: '**/v1/admin/counters', audit: '**/v1/admin/audit*',
    })) releases[key] = await hold(glob);

    const writesBefore = writes();
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    for (const name of ['Peers', 'Threads', 'Delivery health', 'Dead letters', 'Loop control', 'Subscriptions', 'Audit tail']) {
      await dash.locator(':scope > section > h2', { hasText: name }).waitFor();
    }
    // Keep subscriptions pending to exercise a slow response alongside the six held sections.
    await sec('subs').locator('.loading').waitFor();
    await releases.subs();
    await sec('subs').locator('.subs-table').waitFor();
    assert(await dash.locator('.loading').count() === 6, 'every section should be loading while its read is held');
    assert((await page.locator('.dash-counters').textContent()).includes('Loading counters'), 'counter line not loading');
    await shot('12-dash-initial');

    // Sections load independently: some answered, the rest still held.
    for (const key of ['activity', 'counters', 'dead', 'cuts', 'audit']) await releases[key]();
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
    const peerRow = name => sec('peers').locator('tbody tr', { has: page.locator('td:first-child > .pn', { hasText: new RegExp('^' + name + '$') }) });
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

    // Subscription health follows another client's pause and resume while the
    // dashboard stays mounted (operator_view "subscription" names peers).
    const subsList = await (await asPeer('operator', 'GET', '/v1/admin/subscriptions')).json();
    const okSub = subsList.find(x => x.health === 'ok');
    assert(okSub, 'the seed has no ok subscription');
    const subRow = sec('subs').locator(`tr.sub[data-sub="${okSub.sub_id}"]`);
    await subRow.and(page.locator('[data-health="ok"]')).waitFor();
    for (const [action, health] of [['pause', 'paused_operator'], ['resume', 'ok']]) {
      const r = await asPeer('operator', 'POST', `/v1/admin/subscriptions/${okSub.sub_id}/${action}`);
      assert(r.ok, `${action}: ${r.status}`);
      await sec('subs').locator(`tr.sub[data-sub="${okSub.sub_id}"][data-health="${health}"]`).waitFor();
    }

    // Mandatory bounded-page fixture: only audit replies are synthetic. Keep a
    // real seed entry's timestamp/actor/targets/detail and use exclusive cursors.
    const auditSeed = await asPeer('operator', 'GET', '/v1/admin/audit?limit=1');
    assert(auditSeed.ok, `audit fixture seed read: ${auditSeed.status}`);
    const seedEntry = (await auditSeed.json()).entries?.[0];
    assert(seedEntry?.at && seedEntry.actor, 'audit fixture needs a current server entry');
    let auditTop = 150, servedHead = 0;
    await page.route('**/v1/admin/audit*', async route => {
      const req = route.request(), url = new URL(req.url());
      assert(req.method() === 'GET' && url.searchParams.get('limit') === '50', 'audit fixture expects GET limit=50');
      const before = url.searchParams.get('before');
      assert(before === null || /^[1-9]\d*$/.test(before), 'audit fixture received invalid cursor');
      const top = before === null ? auditTop : Math.min(auditTop, Number(before) - 1);
      const entries = Array.from({ length: Math.min(50, top) }, (_, i) => ({
        ...seedEntry, id: top - i, action: `fixture-audit-${top - i}`,
      }));
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
        entries, next: entries.at(-1)?.id > 1 ? String(entries.at(-1).id) : null,
      }) });
      if (before === null) servedHead = top;
    });
    const auditButton = name => sec('audit').getByRole('button', { name, exact: true });
    const expectAudit = async (top, mode, label) => {
      try {
        await sec('audit').locator('.act', { hasText: new RegExp(`^fixture-audit-${top}$`) }).waitFor();
      } catch (error) {
        throw new Error(`${label}: audit fixture never rendered page ${top}: ${error.message}`);
      }
      await ctx.quiet();
      const actions = await sec('audit').locator('.audit .ln .act').allTextContents();
      const expected = Array.from({ length: 50 }, (_, i) => `fixture-audit-${top - i}`);
      assert(await sec('audit').locator('.audit .ln').count() === 50, `${label}: exactly 50 rows, never an appended union`);
      assert(JSON.stringify(actions) === JSON.stringify(expected), `${label}: wrong page/order: ${actions.join(',')}`);
      assert((await sec('audit').locator('h2 .n').textContent()).startsWith(`50 ${mode},`), `${label}: wrong selection label`);
      assert(await sec('audit').locator('.err, .error, [role="status"]').count() === 0, `${label}: audit error or pending navigation`);
    };
    const refreshAudit = async quota => {
      const [response, changed] = await Promise.all([
        page.waitForResponse(r => {
          const u = new URL(r.url());
          return u.pathname === '/v1/admin/audit' && !u.searchParams.has('before');
        }),
        asPeer('codex', 'POST', '/v1/presence', { state: 'working', at: new Date().toISOString(), quota: { weekly_remaining_pct: quota } }),
      ]);
      assert(changed.ok && response.ok(), 'audit fixture: directory update must refresh live head');
      await ctx.quiet();
      assert(servedHead === auditTop, `audit fixture: head ${auditTop} not served`);
      await peerRow('codex-hark').locator('td', { hasText: new RegExp(`^${quota}%$`) }).waitFor();
    };
    const auditShots = async name => {
      await scrollIntoViewStable(page, '.dash-audit h2');
      await page.locator('#main').evaluate(el => {
        el.scrollTop += document.querySelector('.dash-audit').getBoundingClientRect().top - el.getBoundingClientRect().top - 16;
      });
      await shot(name);
      await scrollIntoViewStable(page, '.dash-audit button.older');
      await shot(name + '-controls');
    };
    await refreshAudit(43);
    await expectAudit(150, 'newest', 'fixture initial head');
    assert(await auditButton('Newer').isDisabled() && await auditButton('Latest').isDisabled(), 'audit initial head: history controls disabled');
    await auditButton('Older').click();
    await expectAudit(100, 'historical', 'Older replaces head');
    await auditButton('Older').click();
    await expectAudit(50, 'historical', 'Older reaches third page');
    assert(await auditButton('Older').isDisabled(), 'audit fixture terminal page: Older disabled');
    auditTop = 200;
    await refreshAudit(42);
    await expectAudit(50, 'historical', 'fresh head preserves selected history');
    await auditShots('12-dash-audit-history');
    await auditButton('Newer').click();
    await expectAudit(100, 'historical', 'Newer refetches preceding history page');
    await auditButton('Latest').click();
    await expectAudit(200, 'newest', 'Latest selects refreshed head');
    assert(await auditButton('Newer').isDisabled() && await auditButton('Latest').isDisabled(), 'audit Latest: history controls disabled');
    await auditShots('12-dash-audit-head');
    await auditButton('Older').click();
    await expectAudit(150, 'historical', 'Older starts from refreshed head cursor');
    await auditButton('Newer').click();
    await expectAudit(200, 'newest', 'Newer returns to refreshed live head');

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
    await sec('subs').locator('.subs-table').waitFor();
    assert(await dash.locator('.empty.error').count() === 1, 'the failure spread beyond its section');
    assert(await dash.locator('.loading').count() === 0, 'a section is stuck loading');
    await scrollIntoViewStable(page, '.dash-threads h2');
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
