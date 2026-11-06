// Sidebar search, phase A (CoS ruling): the box above Your Threads. Peer
// names are matched in the browser over the directory the shell already
// holds; thread titles are matched by the hub, GET /v1/threads?q= (PROTOCOL
// §2.8), once typing pauses, and never by filtering admin pages here. A
// peer's Threads button narrows the titles to that peer's threads
// (participant=). Esc or the clear button restores the thread lists. Shots:
// the idle box, a peer hit, a title hit (no peer), a miss, searching, a
// failed search, participant + q, the 220 px and phone sidebars, and the
// cleared box.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const isSearch = url => { const u = new URL(url); return u.pathname === '/v1/threads' && (u.searchParams.has('q') || u.searchParams.has('participant')); };
// Route matchers for one title query.
const titled = q => u => u.pathname === '/v1/threads' && u.searchParams.get('q') === q;

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, fail, allowConsole, countRequests, quiet } = ctx;
    allowConsole(/status of 503/);
    // Keeps interception on when hold() unroutes (see shell.mjs).
    await page.route('**/__keep-interception__', route => route.continue());
    const api = async (as, method, path, body) => {
      const r = await asPeer(as, method, path, body);
      const text = await r.text();
      return { status: r.status, body: text ? JSON.parse(text) : null };
    };
    let serial = 0;
    const send = async (as, body) => {
      const r = await api(as, 'POST', '/v1/messages', { idempotency_key: `search-${++serial}`, priority: 'hold', ...body });
      assert(r.status === 202, `${as} send: ${r.status} ${JSON.stringify(r.body)}`);
      return r.body;
    };
    const p = name => demo.peers[name].id;
    const box = page.locator('#search-q');
    const results = page.locator('#results');
    const lists = page.locator('#lists');
    // What the results show: peer rows, the thread group heading and rows, notes.
    const shown = () => page.evaluate(() => {
      const r = document.getElementById('results');
      return {
        hidden: r.hidden,
        peers: [...r.querySelectorAll('.sr-peer .sr-pl')].map(a => a.querySelector('.tt').textContent),
        picks: [...r.querySelectorAll('.sr-peer .sr-pick')].length,
        heads: [...r.querySelectorAll('.sr-grp > span:first-child')].map(s => s.textContent),
        threads: [...r.querySelectorAll('.sr-threads a[href^="#thread/"]')].map(a => a.getAttribute('href').slice('#thread/'.length)),
        notes: [...r.querySelectorAll('.none')].map(n => n.textContent),
      };
    });
    const asked = [];
    page.on('request', req => { if (isSearch(req.url())) asked.push(new URL(req.url())); });
    const searchUrls = () => asked.slice();
    const typeSlowly = async text => { for (const ch of text) { await box.press(ch === ' ' ? 'Space' : ch); await page.waitForTimeout(60); } };
    const settled = async () => { await quiet(); await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))); };
    // Shots are taken with the pointer off the sidebar, so no row reads as hovered.
    const snap = async name => { const vp = page.viewportSize(); await page.mouse.move(vp.width - 2, vp.height - 2); await shot(name); };
    const top = () => page.evaluate(() => { document.querySelector('nav.side').scrollTop = 0; window.scrollTo(0, 0); });
    const fitsSidebar = async width => {
      const m = await page.evaluate(() => {
        const side = document.querySelector('nav.side'), b = document.querySelector('.sr-box').getBoundingClientRect();
        const s = side.getBoundingClientRect();
        const out = [...document.querySelectorAll('#results .sr-peer, #results .sr-threads a, .sr-box > *')].filter(el => {
          const r = el.getBoundingClientRect();
          return r.width && (r.right > s.right + 0.5 || r.left < s.left - 0.5);
        }).map(el => el.className || el.tagName);
        return { sideScroll: side.scrollWidth - side.clientWidth, docScroll: document.documentElement.scrollWidth - innerWidth, box: b.right <= s.right + 0.5 && b.left >= s.left - 0.5, out };
      });
      assert(m.sideScroll <= 0 && m.docScroll <= 0 && m.box && !m.out.length, `search overflows at ${width}px: ${JSON.stringify(m)}`);
    };

    await page.locator('#conn.live').waitFor();
    await page.locator('#threads a').first().waitFor();

    // Titles to find: two of the operator's threads, and one between two
    // peers the operator is not in (found only by picking a peer).
    const plan = await send('codex', { thread_title: 'Release plan: hark 0.9', recipients: [demo.operator_peer, p('sleuth')], content: { text: 'Plan below.' } });
    const notes = await send('grok', { thread_title: 'Release notes review', recipients: [demo.operator_peer], content: { text: 'Notes attached.' } });
    const theirs = await send('grok', { thread_title: 'Release checklist for claw-ops', recipients: [p('claw')], content: { text: 'Checklist?' } });
    await page.locator(`#threads a[href="#thread/${notes.thread_id}"]`).waitFor();
    await settled();

    // Idle: the box above Your Threads, with its / hint.
    assert(await box.getAttribute('maxlength') === '128', 'the box does not cap q at 128');
    assert(await results.isHidden() && await lists.isVisible(), 'results shown with an empty box');
    await snap('sr-idle');

    // "/" from the page focuses the box.
    await page.locator('#view h1').first().click();
    await page.keyboard.press('/');
    assert(await box.evaluate(el => el === document.activeElement), '/ did not focus the search box');

    // Peer hit: typing "hark" asks the hub once, after the pause, for q=hark
    // on /v1/threads; peers are matched in the page.
    const before = searchUrls().length;
    await typeSlowly('hark');
    await page.waitForResponse(r => isSearch(r.url()) && new URL(r.url()).searchParams.get('q') === 'hark');
    await settled();
    const typed = searchUrls().slice(before);
    assert(typed.length === 1 && typed[0].searchParams.get('q') === 'hark' && !typed[0].searchParams.has('participant'), `typing asked ${typed.map(String).join(', ')}`);
    assert(countRequests(req => { const u = new URL(req.url()); return u.pathname.startsWith('/v1/admin/threads') && u.searchParams.has('q'); }) === 0, 'searched the admin thread list');
    const dir = (await api('operator', 'GET', '/v1/peers')).body.peers;
    const want = dir.filter(x => x.name.toLowerCase().includes('hark')).map(x => x.name).sort();
    let v = await shown();
    assert(!v.hidden && JSON.stringify([...v.peers].sort()) === JSON.stringify(want) && want.length >= 2, `peers ${JSON.stringify(v.peers)} vs directory ${JSON.stringify(want)}`);
    const hub = async (q, participant) => (await api('operator', 'GET', '/v1/threads?' + new URLSearchParams({ q, ...(participant ? { participant } : {}), limit: '50' }))).body.threads.map(t => t.thread_id);
    assert(JSON.stringify(v.threads) === JSON.stringify(await hub('hark')) && v.threads.length >= 2, `threads ${JSON.stringify(v.threads)}`);
    assert(v.heads[1] === 'Your Threads', `heading ${JSON.stringify(v.heads)}`);
    assert(await lists.isHidden(), 'thread lists still shown under results');
    await top();
    await snap('sr-peer-hit');

    // Title hit, no peer: "release".
    await box.fill('release');
    await page.waitForResponse(r => isSearch(r.url()) && new URL(r.url()).searchParams.get('q') === 'release');
    await settled();
    v = await shown();
    assert(v.peers.length === 0 && v.notes.includes('No peer name contains “release”'), `peer miss ${JSON.stringify(v)}`);
    assert(v.threads.includes(plan.thread_id) && v.threads.includes(notes.thread_id) && !v.threads.includes(theirs.thread_id), `title hit ${JSON.stringify(v.threads)}`);
    assert(JSON.stringify(v.threads) === JSON.stringify(await hub('release')), 'title hits differ from the hub');
    await snap('sr-thread-hit');

    // Miss on both.
    await box.fill('zebra crossing');
    await page.waitForResponse(r => isSearch(r.url()) && new URL(r.url()).searchParams.get('q') === 'zebra crossing');
    await settled();
    v = await shown();
    assert(!v.peers.length && !v.threads.length && v.notes.includes('No peer name contains “zebra crossing”') && v.notes.includes('No thread title contains “zebra crossing”'), `miss ${JSON.stringify(v)}`);
    await snap('sr-miss');

    // Searching: the hub has not answered yet.
    const release = await hold(titled('digest'));
    const held = page.waitForRequest(req => titled('digest')(new URL(req.url())));
    await box.fill('digest');
    await held;
    await page.locator('#results .none[aria-busy=true]', { hasText: 'Searching…' }).waitFor();
    await page.locator('#results .sr-pl', { hasText: 'pag-digest' }).waitFor();
    await snap('sr-loading');
    await release();
    await page.locator(`#results .sr-threads a[href="#thread/${demo.threads.digest}"]`).waitFor();

    // A failed search says why and offers Retry.
    const quota = titled('quota');
    await fail(quota, 503, 'unavailable', 'Hub busy, try again');
    await box.fill('quota');
    await page.locator('#results .none.bad', { hasText: 'Search failed: Hub busy, try again' }).waitFor();
    await page.locator('#results button', { hasText: 'Retry' }).waitFor();
    await settled();
    await snap('sr-error');
    await page.unroute(quota);
    await page.locator('#results button', { hasText: 'Retry' }).click();
    await page.locator(`#results .sr-threads a[href="#thread/${demo.threads.quota}"]`).waitFor();

    // Participant + q: pick grok-reviewer from the peer results, then search
    // its threads by title; the peer-only thread is found this way.
    await box.fill('grok');
    await page.locator('#results .sr-pl', { hasText: 'grok-reviewer' }).waitFor();
    await page.locator('#results .sr-peer', { hasText: 'grok-reviewer' }).locator('.sr-pick').click();
    await page.locator('.sr-scope .sr-with', { hasText: 'grok-reviewer' }).waitFor();
    assert(await box.evaluate(el => el === document.activeElement), 'focus left the box after a pick');
    const picked = searchUrls().length;
    await box.fill('release');
    await page.waitForResponse(r => isSearch(r.url()) && new URL(r.url()).searchParams.get('q') === 'release' && new URL(r.url()).searchParams.get('participant') === p('grok'));
    await settled();
    const combined = searchUrls().slice(picked).at(-1);
    assert(combined.searchParams.get('participant') === p('grok') && combined.searchParams.get('q') === 'release', `combined asked ${combined}`);
    v = await shown();
    assert(v.heads.length === 1 && v.heads[0] === 'Threads with grok-reviewer' && !v.peers.length, `participant heading ${JSON.stringify(v)}`);
    assert(v.threads.includes(theirs.thread_id) && v.threads.includes(notes.thread_id) && !v.threads.includes(plan.thread_id), `participant + q ${JSON.stringify(v.threads)}`);
    assert(JSON.stringify(v.threads) === JSON.stringify(await hub('release', p('grok'))), 'participant results differ from the hub');
    await snap('sr-participant');
    // A result opens its thread; the results stay for the next one.
    await page.locator(`#results a[href="#thread/${theirs.thread_id}"] .tt`).click();
    await page.locator('#view .msg', { hasText: 'Checklist?' }).waitFor();
    assert(await page.locator(`#results a.on[href="#thread/${theirs.thread_id}"]`).count() === 1, 'opened result not marked');
    // Backspace in the empty box drops the peer; the text stays a title search.
    await box.fill('');
    await box.press('Backspace');
    await page.locator('.sr-scope').waitFor({ state: 'hidden' });
    assert(await results.isHidden(), 'results stayed with no text and no peer');

    // The hub's 128-character cap: the box takes no more, and a longer q is
    // refused by the hub, so the UI never sends one.
    await box.fill('x'.repeat(200));
    assert((await box.inputValue()).length === 128, 'the box took more than 128 characters');
    const refused = await api('operator', 'GET', '/v1/threads?q=' + 'x'.repeat(129));
    assert(refused.status === 422 && refused.body.error.code === 'invalid_value' && refused.body.error.details.field === 'q', `hub took a 129-character q: ${JSON.stringify(refused)}`);
    await settled();
    assert(searchUrls().every(u => [...(u.searchParams.get('q') || '')].length <= 128), 'the UI sent q over 128 characters');

    // Narrow: the 220 px sidebar (1000 px) and a phone, on the inbox (the
    // thread view's Details button floats over the top of a phone page).
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    await box.fill('hark');
    await page.waitForResponse(r => isSearch(r.url()) && new URL(r.url()).searchParams.get('q') === 'hark');
    await settled();
    await page.setViewportSize({ width: 1000, height: 900 });
    await top();
    await fitsSidebar(1000);
    await snap('sr-narrow');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator('#search').evaluate(el => el.scrollIntoView({ block: 'start' }));
    await fitsSidebar(390);
    await snap('sr-phone');
    await page.setViewportSize({ width: 1440, height: 900 });
    await top();

    // Esc restores the thread lists and empties the box; no request follows.
    const quietCount = searchUrls().length;
    await box.focus();
    await box.press('Escape');
    assert(await box.inputValue() === '' && await results.isHidden() && await lists.isVisible(), 'Esc did not restore the lists');
    await page.locator('#threads a').first().waitFor();
    // The clear button does the same.
    await box.fill('plan');
    await page.locator('.sr-clear').click();
    assert(await box.inputValue() === '' && await results.isHidden(), 'clear did not restore the lists');
    await settled();
    assert(searchUrls().length <= quietCount + 1, 'clearing asked the hub');
    await box.blur();
    await settled();
    await snap('sr-cleared');
    assert(countRequests('/v1/admin/threads?q') === 0, 'q was sent to the admin list');
  });
  console.log('Search passed: peer names match the loaded directory in the page; thread titles come from GET /v1/threads?q= once typing pauses, equal to the hub, with participant= when a peer is picked; misses, searching and a failure have their own states; the box caps q at 128; Esc and clear restore the lists; nothing overflows at 1000 or 390 px');
} finally {
  await cleanBuild();
}
