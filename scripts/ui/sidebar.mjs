// Sidebar: You above Your Threads, then Other Threads (threads the operator
// is not in, newest activity first, kept live by the operator activity feed),
// in a real browser. Both lists share one two-line row; an untitled thread
// reads as its participants ("A ↔ B", "A, B +2") in dim italic. Shots: Other
// Threads empty, populated, error and opened; Your Threads; the whole
// sidebar; an untitled pair and an untitled group opened.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const HOSTILE = '<img src=x onerror=alert(1)> ‮pwn';
const isOthers = url => { const u = new URL(url); return u.pathname === '/v1/admin/threads' && u.searchParams.has('exclude_participant'); };

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, advance, hold, allowConsole, countRequests, quiet } = ctx;
    // Chromium logs each non-2xx fetch; this is the failure the script provokes.
    allowConsole(/status of 500/);
    const others = page.locator('#others');
    const mineList = page.locator('#threads');
    const rowsIn = list => list.locator('a[href^="#thread/"]').evaluateAll(as => as.map(a => ({
      id: a.getAttribute('href').slice('#thread/'.length),
      title: a.querySelector('.tt').textContent, un: a.querySelector('.tt').classList.contains('un'), count: a.querySelector('.cnt').textContent,
      people: a.querySelector('.pp').textContent, ago: a.querySelector('.ago').textContent, on: a.classList.contains('on'),
    })));
    const rowsOf = () => rowsIn(others);
    // The lists are longer than the viewport now that Your Threads has two lines a row.
    const showOthers = () => page.locator('nav.side .grp', { hasText: 'Other Threads' }).evaluate(el => el.scrollIntoView({ block: 'start' }));
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
    // One activity feed per tab, bearer in the header.
    if (!countRequests(req => new URL(req.url()).pathname === '/v1/admin/activity')) await page.waitForRequest(req => new URL(req.url()).pathname === '/v1/admin/activity');
    assert(countRequests(req => new URL(req.url()).pathname === '/v1/admin/activity' && req.headers().authorization === `Bearer ${demo.operator_secret}` && !req.url().includes(demo.operator_secret)) === 1, 'expected one activity feed carrying the bearer header');

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
    await quiet();
    await showOthers();
    await shot('sb-others-empty');

    // Your Threads: the operator's threads as ThreadRows, so each row has its last activity.
    await mineList.locator('a .ago', { hasText: /ago|now/ }).first().waitFor();
    assert(countRequests(req => { const u = new URL(req.url()); return u.pathname === '/v1/admin/threads' && u.searchParams.get('participant') === demo.operator_peer; }) >= 1, 'Your Threads did not read its ThreadRows');
    const seeded = await rowsIn(mineList);
    const hark = seeded.find(r => r.title.startsWith('hark B196'));
    assert(hark && !hark.un && hark.count === '10' && hark.people && !hark.people.includes('max') && hark.ago, `hark row ${JSON.stringify(hark)}`);

    // Four threads between peers, oldest first; advance spaces their activity.
    const p = name => demo.peers[name].id;
    const release = await send('codex', { thread_title: 'Release train: hark 0.9', recipients: [p('sleuth'), p('grok'), p('claw'), p('digest'), p('triage')], content: { text: 'Cutting 0.9 tonight.' } });
    await send('sleuth', { thread_id: release.thread_id, content: { text: 'Classifier fix is in.' } });
    await send('grok', { thread_id: release.thread_id, content: { text: 'Reviewed.' } });
    await advance('50m');
    const untitled = await send('grok', { recipients: [p('claw')], content: { text: 'Can you look at the push route?' } });
    await advance('25m');
    const hostile = await send('hostile', { thread_title: HOSTILE, recipients: [p('codex')], content: { text: 'hello' } });
    await advance('3m');
    // An untitled group: two names and a count. The hostile peer's name hides in the count.
    const group = await send('triage', { recipients: [p('digest'), p('dot'), p('hostile')], content: { text: 'Who owns the digest cron?' } });
    await advance('1m');
    const rollout = await send('sleuth', { thread_title: 'Classifier rollout plan', recipients: [p('grok'), p('codex')], content: { text: 'Staged rollout, 10% first.' } });
    await send('codex', { thread_id: rollout.thread_id, content: { text: 'Agreed.' } });
    // Untitled threads the operator is in: a 1:1 reads as the other peer, a group leaves the operator out.
    const solo = await send('codex', { recipients: [demo.operator_peer], content: { text: 'Quick one: is the gate green?' } });
    const huddle = await send('grok', { recipients: [demo.operator_peer, p('sleuth'), p('claw'), p('digest')], content: { text: 'Huddle on the push route.' } });
    const { now } = await advance('2m');
    // The jump also lapses the seeded subscriptions' tokens; the events worker
    // pauses them on its next step (expired and late read paused · token).
    // Wait for that before the presence report below re-reads the inbox the
    // shots show behind the sidebar, or a slow step leaves one reading expired.
    for (let tries = 0; ; tries++) {
      const r = await asPeer('operator', 'GET', '/v1/admin/subscriptions');
      assert(r.ok, `subscriptions: ${r.status}`);
      const subs = await r.json();
      if (!subs.some(sub => sub.health === 'expired' || sub.health === 'late')) break;
      assert(tries < 600, `the events worker never paused the lapsed subscriptions: ${subs.map(sub => sub.health).join()}`);
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    // The jump outlives the seeded presence; report it again at the new hub time.
    const pres = await asPeer('operator', 'POST', '/v1/presence', { state: 'working', at: now });
    assert(pres.ok, `operator presence: ${pres.status}`);

    // None of that reaches the operator as an Event; the activity feed does,
    // and an unknown thread refetches the list. No poll and no operator
    // action. The presence change re-reads the inbox, which carries the
    // advanced hub clock for the relative times.
    await others.locator('a', { hasText: 'Classifier rollout plan' }).locator('.ago', { hasText: '2m ago' }).waitFor();
    await page.waitForFunction(() => document.querySelectorAll('#others a[href^="#thread/"]').length === 5);
    // Resource redraws must not drop a keyboard user's current sidebar row.
    for (const list of ['threads', 'others']) {
      const link = page.locator('#' + list + ' a').first();
      const href = await link.getAttribute('href');
      await link.focus();
      const before = countRequests(req => isOthers(req.url()));
      const reloaded = page.waitForResponse(r => isOthers(r.url()));
      await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
      await reloaded;
      await quiet();
      assert(countRequests(req => isOthers(req.url())) > before, 'visibility did not reload threads');
      assert(await page.evaluate(({ list, href }) => document.activeElement.parentElement.id === list && document.activeElement.getAttribute('href') === href, { list, href }), `${list} redraw dropped focus`);
    }
    let rows = await rowsOf();
    assert(rows.map(r => r.id).join() === [rollout, group, hostile, untitled, release].map(t => t.thread_id).join(), `order ${JSON.stringify(rows)}`);
    const mine = await page.locator('#threads a').evaluateAll(as => as.map(a => a.getAttribute('href')));
    assert(rows.every(r => !mine.includes('#thread/' + r.id)), 'a thread is in both lists');
    const [ro, gr, ho, un, re] = rows;
    assert(ro.count === '2' && !ro.un && ro.people === 'pag-hark-sleuth, grok-reviewer, codex-hark' && ro.ago === '2m ago', `rollout row ${JSON.stringify(ro)}`);
    assert(re.count === '3' && re.people === 'codex-hark, pag-hark-sleuth, grok-reviewer +3' && re.ago === '1h ago', `release row ${JSON.stringify(re)}`);
    // Untitled: the stand-in names the participants, the second line counts them.
    assert(un.title === 'grok-reviewer ↔ claw-ops' && un.un && un.people === '2 participants' && un.ago === '31m ago', `untitled row ${JSON.stringify(un)}`);
    assert(gr.title === 'grok-triage, pag-digest +2' && gr.un && gr.people === '4 participants' && gr.ago === '3m ago', `group row ${JSON.stringify(gr)}`);
    // Peer text is text: the hostile title neither makes an element nor keeps its bidi override.
    assert(ho.title === '<img src=x onerror=alert(1)> pwn', `hostile title ${JSON.stringify(ho.title)}`);
    assert(await page.locator('nav.side img').count() === 0, 'a peer string became an element');
    // The clock jump outlived the seeded presence, so You and the inbox aside
    // read "presence unknown" until the directory is re-read after the report.
    await page.locator('#me .who', { hasText: /^max is working$/ }).waitFor();
    await quiet();
    await showOthers();
    await shot('sb-others-populated');

    // Your Threads: the same row. The operator is left out of stand-ins and participant lines.
    await mineList.locator('a', { hasText: 'grok-reviewer, pag-hark-sleuth +2' }).waitFor();
    const yours = await rowsIn(mineList);
    const hu = yours.find(r => r.id === huddle.thread_id), so = yours.find(r => r.id === solo.thread_id);
    assert(hu && so, `untitled threads missing from Your Threads ${JSON.stringify(yours)}`);
    assert(hu.un && hu.title === 'grok-reviewer, pag-hark-sleuth +2' && hu.people === '5 participants' && hu.count === '1' && hu.ago === '2m ago', `huddle row ${JSON.stringify(hu)}`);
    assert(so.un && so.title === 'codex-hark' && so.people === '2 participants' && so.ago === '2m ago', `1:1 row ${JSON.stringify(so)}`);
    assert(yours.filter(r => !r.un).every(r => r.ago && !r.people.split(', ').includes('max')), `titled rows ${JSON.stringify(yours)}`);
    await quiet();
    await page.locator('nav.side').evaluate(el => { el.scrollTop = 0; });
    await shot('sb-yours-rows');
    // The whole sidebar in one frame, so the two lists can be compared.
    const tall = await page.locator('nav.side').evaluate(el => el.scrollHeight);
    await page.setViewportSize({ width: 1440, height: tall });
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await shot('sb-threads-full');
    await page.setViewportSize({ width: 1440, height: 900 });

    // Live: a reply in the oldest (listed) thread arrives on the feed and is
    // folded in place. The thread list is held, so a re-sort can only come
    // from the feed, not from a refetch or a poll.
    const releaseList = await hold(isOthers);
    await send('claw', { thread_id: release.thread_id, content: { text: 'Push route is fine.' } });
    await page.waitForFunction(id => document.querySelector('#others a')?.getAttribute('href') === '#thread/' + id, release.thread_id);
    rows = await rowsOf();
    assert(rows[0].count === '4' && /^(now|\d+s ago)$/.test(rows[0].ago), `re-sorted top row ${JSON.stringify(rows[0])}`);
    assert(rows.slice(1).map(r => r.id).join() === [rollout, group, hostile, untitled].map(t => t.thread_id).join(), `rest ${JSON.stringify(rows)}`);
    await releaseList();
    let fresh;

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
    await quiet();
    assert(await more.evaluate(el => el === document.activeElement), 'Show more redraw dropped focus');
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
    const currentRows = await page.locator('nav.side [aria-current="page"]').evaluateAll(els => els.map(e => e.getAttribute('href')));
    assert(currentRows.length === 1 && currentRows[0] === '#thread/' + rollout.thread_id, `the open thread is not the one current row: ${currentRows}`);
    assert(await page.locator('#threads a.on').count() === 0, 'Your Threads lit a thread the operator is not in');
    await page.locator('#aside .pp', { hasText: 'pag-hark-sleuth' }).first().waitFor();
    await quiet();
    await showOthers();
    await shot('sb-others-thread');

    // Untitled, opened: the thread header takes the same stand-in and style.
    const opened = async (list, stand, text, name) => {
      await list.locator('a', { hasText: stand }).click();
      await page.locator('#view .msg', { hasText: text }).waitFor();
      await page.locator('.thead h1.un', { hasText: stand }).waitFor();
      assert(await page.locator('.thead h1').textContent() === stand, `header ${await page.locator('.thead h1').textContent()}`);
      await list.locator('a.on .tt.un', { hasText: stand }).waitFor();
      // Receipts come from the delivery matrix, a load of its own.
      await page.locator('#view .msg', { hasText: text }).locator('.rc span').first().waitFor();
      await quiet();
      await shot(name);
    };
    await opened(others, 'grok-reviewer ↔ claw-ops', 'Can you look at the push route?', 'sb-untitled-pair');
    await opened(mineList, 'grok-reviewer, pag-hark-sleuth +2', 'Huddle on the push route.', 'sb-untitled-group');

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
    await quiet();
    await shot('sb-others-error');

    // Recovery on the next poll.
    await page.unroute(isOthers, failing);
    fresh = drawn();
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await fresh;
    await others.locator('a', { hasText: 'Release train: hark 0.9' }).waitFor();
  });
  console.log('Sidebar passed: You above Your Threads; both lists share the two-line row; untitled threads read as their participants; Other Threads excludes the operator, sorts by activity, re-sorts live, pages, opens read-only, recovers from an error');
} finally {
  await cleanBuild();
}
