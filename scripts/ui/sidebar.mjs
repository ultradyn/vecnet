// Sidebar: You above Your Threads, then Other Threads (threads the operator
// is not in, newest activity first), in a real browser. Four shots: empty,
// populated, error, and an other thread opened from the list.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const HOSTILE = '<img src=x onerror=alert(1)> ‮pwn';
const isOthers = url => { const u = new URL(url); return u.pathname === '/v1/admin/threads' && u.searchParams.has('exclude_participant'); };

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, advance, allowConsole, countRequests } = ctx;
    // Chromium logs each non-2xx fetch; this is the failure the script provokes.
    allowConsole(/status of 500/);
    const others = page.locator('#others');
    const rowsOf = () => others.locator('a[href^="#thread/"]').evaluateAll(as => as.map(a => ({
      id: a.getAttribute('href').slice('#thread/'.length),
      title: a.querySelector('.tt').textContent, count: a.querySelector('.cnt').textContent,
      people: a.querySelector('.pp').textContent, ago: a.querySelector('.ago').textContent, on: a.classList.contains('on'),
    })));
    let serial = 0;
    const send = async (peer, body) => {
      const r = await asPeer(peer, 'POST', '/v1/messages', { idempotency_key: `sidebar-${++serial}`, priority: 'hold', ...body });
      const body2 = await r.text();
      assert(r.status === 202, `${peer} send: ${r.status} ${body2}`);
      return JSON.parse(body2);
    };
    // Shown only once a fresh page of other threads has drawn.
    const drawn = async () => { await page.waitForResponse(r => isOthers(r.url()) && r.ok()); await page.evaluate(() => new Promise(requestAnimationFrame)); };

    await page.locator('#conn.live').waitFor();
    await page.locator('#threads a').first().waitFor();

    // Order: nav, You, Your Threads, Other Threads, then connection.
    const groups = await page.locator('nav.side .grp').allTextContents();
    assert(groups.join() === 'You,Your Threads,Other Threads', `sidebar groups ${groups}`);
    assert(await page.evaluate(() => {
      const me = document.getElementById('me'), mine = document.getElementById('threads'), oth = document.getElementById('others');
      return !!(me.compareDocumentPosition(mine) & Node.DOCUMENT_POSITION_FOLLOWING) && !!(mine.compareDocumentPosition(oth) & Node.DOCUMENT_POSITION_FOLLOWING);
    }), 'You, Your Threads, Other Threads out of order');

    // Empty: every seeded thread includes the operator.
    await others.locator('.none', { hasText: 'No other threads' }).waitFor();
    assert(countRequests(req => isOthers(req.url()) && new URL(req.url()).searchParams.get('exclude_participant') === demo.operator_peer) >= 1, 'Other Threads did not exclude the operator');
    await shot('sb-others-empty');

    // Four threads between peers, oldest first; advance spaces their activity.
    const p = name => demo.peers[name].id;
    const release = await send('codex', { thread_title: 'Release train: hark 0.9', recipients: [p('sleuth'), p('grok'), p('claw'), p('digest'), p('triage')], content: { text: 'Cutting 0.9 tonight.' } });
    await send('sleuth', { thread_id: release.thread_id, content: { text: 'Classifier fix is in.' } });
    await send('grok', { thread_id: release.thread_id, content: { text: 'Reviewed.' } });
    await advance('50m');
    const untitled = await send('grok', { recipients: [p('claw')], content: { text: 'Can you look at the push route?' } });
    await advance('25m');
    const hostile = await send('hostile', { thread_title: HOSTILE, recipients: [p('codex')], content: { text: 'hello' } });
    await advance('4m');
    const rollout = await send('sleuth', { thread_title: 'Classifier rollout plan', recipients: [p('grok'), p('codex')], content: { text: 'Staged rollout, 10% first.' } });
    await send('codex', { thread_id: rollout.thread_id, content: { text: 'Agreed.' } });
    const { now } = await advance('2m');
    // The jump outlives the seeded presence; report it again at the new hub time.
    const pres = await asPeer('operator', 'POST', '/v1/presence', { state: 'working', at: now });
    assert(pres.ok, `operator presence: ${pres.status}`);

    // None of that reaches the operator as an Event. An operator send does
    // (operator_view), and re-reads both lists and the hub clock.
    let fresh = drawn();
    await send('operator', { thread_id: demo.threads.ticket18, content: { text: 'Looking at the sidebar now.' } });
    await fresh;
    // The inbox reload in the same batch carries the advanced hub clock.
    await others.locator('a', { hasText: 'Classifier rollout plan' }).locator('.ago', { hasText: '2m ago' }).waitFor();
    let rows = await rowsOf();
    assert(rows.map(r => r.id).join() === [rollout, hostile, untitled, release].map(t => t.thread_id).join(), `order ${JSON.stringify(rows)}`);
    const mine = await page.locator('#threads a').evaluateAll(as => as.map(a => a.getAttribute('href')));
    assert(rows.every(r => !mine.includes('#thread/' + r.id)), 'a thread is in both lists');
    const [ro, ho, un, re] = rows;
    assert(ro.count === '2' && ro.people === 'pag-hark-sleuth, grok-reviewer, codex-hark' && ro.ago === '2m ago', `rollout row ${JSON.stringify(ro)}`);
    assert(re.count === '3' && re.people === 'codex-hark, pag-hark-sleuth, grok-reviewer +3' && re.ago === '1h ago', `release row ${JSON.stringify(re)}`);
    assert(un.title === 'Untitled thread' && un.people === 'grok-reviewer, claw-ops', `untitled row ${JSON.stringify(un)}`);
    // Peer text is text: the hostile title neither makes an element nor keeps its bidi override.
    assert(ho.title === '<img src=x onerror=alert(1)> pwn', `hostile title ${JSON.stringify(ho.title)}`);
    assert(await page.locator('nav.side img').count() === 0, 'a peer string became an element');
    await shot('sb-others-populated');

    // Live: new activity in the oldest thread moves it to the top on the next
    // poll. The poll also runs when the tab is shown again.
    await send('claw', { thread_id: release.thread_id, content: { text: 'Push route is fine.' } });
    fresh = drawn();
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await fresh;
    await page.waitForFunction(id => document.querySelector('#others a')?.getAttribute('href') === '#thread/' + id, release.thread_id);
    rows = await rowsOf();
    assert(rows[0].count === '4' && /^(now|\d+s ago)$/.test(rows[0].ago), `re-sorted top row ${JSON.stringify(rows[0])}`);

    // Bounded: a page with more behind it offers one more page per click.
    const paged = async route => {
      const r = await route.fetch();
      await route.fulfill({ response: r, json: { ...(await r.json()), next: 'more-behind' } });
    };
    await page.route(isOthers, paged);
    fresh = drawn();
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await fresh;
    const more = others.locator('button.more', { hasText: 'Show more' });
    await more.waitFor();
    const grown = page.waitForResponse(r => isOthers(r.url()) && new URL(r.url()).searchParams.get('limit') === '100');
    await more.click();
    await grown;
    await page.unroute(isOthers, paged);
    // The real hub has nothing behind this page, so the tail goes.
    fresh = drawn();
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await fresh;
    await more.waitFor({ state: 'detached' });

    // Opened: the thread view for a thread the operator is not in, and its row lit.
    await others.locator('a', { hasText: 'Classifier rollout plan' }).click();
    await page.waitForFunction(id => location.hash === '#thread/' + id, rollout.thread_id);
    await page.locator('#view .msg', { hasText: 'Staged rollout, 10% first.' }).waitFor();
    await page.locator('.composer .join:not([hidden])').waitFor();
    await others.locator('a.on', { hasText: 'Classifier rollout plan' }).waitFor();
    assert(await page.locator('#threads a.on').count() === 0, 'Your Threads lit a thread the operator is not in');
    await page.locator('#aside .pp', { hasText: 'pag-hark-sleuth' }).first().waitFor();
    await shot('sb-others-thread');

    // Error: the first load fails, so there is nothing stale to show.
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    const failing = route => route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'demo_failure', message: 'Demo failure', details: {} } }) });
    await page.route(isOthers, failing);
    await page.reload({ waitUntil: 'load' });
    await page.locator('#conn.live').waitFor();
    await others.locator('.none', { hasText: 'Other threads unavailable' }).waitFor();
    await page.locator('#threads a').first().waitFor();
    assert(await others.locator('a[href^="#thread/"]').count() === 0, 'error state still lists threads');
    await shot('sb-others-error');

    // Recovery on the next poll.
    await page.unroute(isOthers, failing);
    fresh = drawn();
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await fresh;
    await others.locator('a', { hasText: 'Release train: hark 0.9' }).waitFor();
  });
  console.log('Sidebar passed: You above Your Threads; Other Threads excludes the operator, sorts by activity, re-sorts live, pages, opens read-only, recovers from an error');
} finally {
  await cleanBuild();
}
