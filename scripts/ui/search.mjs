// Sidebar search, phase A (CoS ruling): the box above Your Threads. Peer
// names are matched in the browser over the directory the shell already
// holds; thread titles are matched by the hub, GET /v1/threads?q= (PROTOCOL
// §2.8), once typing pauses, and never by filtering admin pages here. A
// peer's Threads button narrows the titles to that peer's threads
// (participant=). Esc or the clear button restores the thread lists.
// Assert the idle box, hits, misses, loading, failure, participant + q,
// narrow layouts, clearing, held pagination and inert hostile text. Export
// screenshots through the shared harness to SHOTS_DIR.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const isSearch = url => { const u = new URL(url); return u.pathname === '/v1/threads' && (u.searchParams.has('q') || u.searchParams.has('participant')); };
// Route matchers for one title query.
const titled = q => u => u.pathname === '/v1/threads' && u.searchParams.get('q') === q;

const focusFailures = [];

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, fail, allowConsole, countRequests, quiet, ignore } = ctx;
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
    // Types text one character at a time inside the page, 40 ms apart, so
    // the gaps stay well under the debounce however slow the driver is.
    const typeInPage = text => box.evaluate(async (el, text) => {
      el.focus();
      for (let i = 1; i <= text.length; i++) {
        el.value = text.slice(0, i);
        el.dispatchEvent(new Event('input', { bubbles: true }));
        await new Promise(resolve => setTimeout(resolve, 40));
      }
    }, text);
    const settled = async () => { await quiet(); await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))); };
    const snap = async name => { const vp = page.viewportSize(); await page.mouse.move(vp.width - 2, vp.height - 2); await shot(name); };
    const top = () => page.evaluate(() => { document.querySelector('nav.side').scrollTop = 0; window.scrollTo(0, 0); });
    const fitsSidebar = async width => {
      const m = await page.evaluate(() => {
        const side = document.querySelector('nav.side'), b = document.querySelector('.sr-box').getBoundingClientRect();
        const s = side.getBoundingClientRect();
        const out = [...document.querySelectorAll('#results .sr-peer, #results .sr-peer .tt, #results .sr-threads a, .sr-box > *, .sr-scope, .sr-with, .sr-with > *')].filter(el => {
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
    // No maxlength: it would count UTF-16 units; the 128-character rule is the model's.
    assert(await box.getAttribute('maxlength') === null, 'the box has a maxlength');
    assert(await results.isHidden() && await lists.isVisible(), 'results shown with an empty box');
    await snap('sr-idle');

    // "/" from the page focuses the box.
    await page.locator('#view h1').first().click();
    await page.keyboard.press('/');
    assert(await box.evaluate(el => el === document.activeElement), '/ did not focus the search box');

    // Peer hit: typing "hark" asks the hub once, after the pause, for q=hark
    // on /v1/threads; peers are matched in the page.
    const before = searchUrls().length;
    await typeInPage('hark');
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
    // Keyboard Retry stays focusable while held and after another failure.
    const retry = page.locator('#results [data-key=retry]');
    const focusedKey = () => page.evaluate(() => document.activeElement?.dataset.key || document.activeElement?.id);
    let releaseRetry;
    const retryGate = new Promise(resolve => { releaseRetry = resolve; });
    const heldFailure = async route => {
      await retryGate;
      await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'Hub busy, try again', details: {} } }) });
    };
    await page.route(quota, heldFailure);
    const watchRetry = ignore(req => quota(new URL(req.url())));
    const retryAsked = page.waitForRequest(req => quota(new URL(req.url())));
    await retry.focus();
    await page.keyboard.press('Enter');
    await retryAsked;
    const duringRetry = await focusedKey();
    const beforeDuplicate = searchUrls().length;
    assert(await retry.getAttribute('aria-disabled') === 'true', 'held Retry is not marked busy');
    await page.keyboard.press('Enter');
    await settled();
    assert(searchUrls().length === beforeDuplicate, 'held Retry sent a duplicate request');
    releaseRetry();
    watchRetry();
    await page.unroute(quota, heldFailure);
    await page.locator('#results .none.bad', { hasText: 'Search failed: Hub busy, try again' }).waitFor();
    const afterRetry = await focusedKey();
    assert(duringRetry === 'retry' && afterRetry === 'retry', `Retry focus: held=${duringRetry}, repeated failure=${afterRetry}`);
    await page.unroute(quota);
    await page.keyboard.press('Enter');
    await page.locator(`#results .sr-threads a[href="#thread/${demo.threads.quota}"]`).waitFor();

    // Participant + q: pick grok-reviewer from the peer results, then search
    // its threads by title; the peer-only thread is found this way.
    await box.fill('grok');
    // Let the q=grok answer redraw the results before clicking in them.
    await page.waitForResponse(r => isSearch(r.url()) && new URL(r.url()).searchParams.get('q') === 'grok');
    await settled();
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

    // The hub's 128-character cap (a 129-character q is 422): past it the
    // UI sends nothing and says why; exactly 128 is sent. The long text
    // wraps inside the sidebar.
    const refused = await api('operator', 'GET', '/v1/threads?q=' + 'x'.repeat(129));
    assert(refused.status === 422 && refused.body.error.code === 'invalid_value' && refused.body.error.details.field === 'q', `hub took a 129-character q: ${JSON.stringify(refused)}`);
    const sentBefore = searchUrls().length;
    await box.fill('x'.repeat(129));
    await page.locator('#results .none.bad', { hasText: 'Title search takes at most 128 characters' }).waitFor();
    await settled();
    assert(searchUrls().length === sentBefore, 'a 129-character q was sent');
    await fitsSidebar(1440);
    await box.fill('x'.repeat(128));
    await page.waitForResponse(r => isSearch(r.url()) && r.ok() && new URL(r.url()).searchParams.get('q') === 'x'.repeat(128));
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

    // Esc empties the box and brings the thread lists back, from a focused
    // result as from the box; so does the clear button. None asks the hub.
    const listsBack = async how => {
      assert(await box.inputValue() === '' && await results.isHidden() && await lists.isVisible(), `${how} did not bring the lists back`);
      await page.locator('#threads a').first().waitFor();
    };
    const quietCount = searchUrls().length;
    await page.locator('#results .sr-threads a[href^="#thread/"]').first().focus();
    await page.keyboard.press('Escape');
    await listsBack('Esc on a result');
    for (const how of ['Esc in the box', 'the clear button']) {
      await box.fill('plan');
      await page.waitForResponse(r => isSearch(r.url()) && new URL(r.url()).searchParams.get('q') === 'plan');
      await settled();
      if (how === 'Esc in the box') await box.press('Escape');
      else await page.locator('.sr-clear').click();
      await listsBack(how);
    }
    await settled();
    assert(searchUrls().length === quietCount + 2, `clearing asked the hub: ${searchUrls().slice(quietCount).map(String).join(', ')}`);
    await box.blur();
    await settled();
    assert(countRequests(req => { const u = new URL(req.url()); return u.pathname.startsWith('/v1/admin/threads') && u.searchParams.has('q'); }) === 0, 'q was sent to the admin list');

    await snap('sr-cleared');

    // Exercise the real sidebar's pagination with hub summaries. Only the
    // HTTP boundary is controlled: a short page exposes cursors without
    // seeding fifty extra threads into every screenshot.
    const releaseQuery = titled('release');
    const pageQuery = u => releaseQuery(u) || titled('grok')(u);
    const summaries = (await api('operator', 'GET', '/v1/threads?q=release')).body.threads;
    const planRow = summaries.find(t => t.thread_id === plan.thread_id);
    const notesRow = summaries.find(t => t.thread_id === notes.thread_id);
    assert(planRow && notesRow, 'pagination fixture needs the real hub summaries');
    const hostile = '<img src=x onerror="window.searchExecuted=true">';
    const hostileTitle = hostile + '<script>window.searchExecuted=true</script>';
    const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    const more = page.locator('#results [data-key=more]');
    const pages = async route => {
      if (new URL(route.request().url()).searchParams.has('participant')) return route.continue();
      const cursor = new URL(route.request().url()).searchParams.get('cursor');
      if (cursor === 'fix1-error') return json(route, { error: { code: 'unavailable', message: hostile, details: {} } }, 503);
      if (cursor === 'fix1-next') return json(route, { threads: [notesRow], next: 'fix1-error' });
      return json(route, { threads: [{ ...planRow, title: hostileTitle }], next: 'fix1-next' });
    };
    await page.route(pageQuery, pages);
    await box.fill('release');
    await more.waitFor();
    await settled();
    assert(await page.locator('#results .sr-threads .tt').textContent() === hostileTitle, 'hostile title changed');
    const cursorQuery = cursor => u => pageQuery(u) && u.searchParams.get('cursor') === cursor;
    const nextPage = cursorQuery('fix1-next');
    let releaseMore;
    const moreGate = new Promise(resolve => { releaseMore = resolve; });
    const heldMore = async route => {
      await moreGate;
      await json(route, { threads: [notesRow], next: 'fix1-error' });
    };
    await page.route(nextPage, heldMore);
    const watchMore = ignore(req => nextPage(new URL(req.url())));
    const moreAsked = page.waitForRequest(req => nextPage(new URL(req.url())));
    await more.focus();
    await page.keyboard.press('Enter');
    await moreAsked;
    assert(await focusedKey() === 'more' && await more.getAttribute('aria-disabled') === 'true', 'More lost focus while loading');
    const beforeMoreDuplicate = searchUrls().length;
    await page.keyboard.press('Enter');
    await settled();
    assert(searchUrls().length === beforeMoreDuplicate, 'held More sent a duplicate request');
    releaseMore();
    watchMore();
    await page.unroute(nextPage, heldMore);
    await page.locator(`#results a[href="#thread/${notes.thread_id}"]`).waitFor();
    assert(await focusedKey() === 'more', 'More lost focus after append');
    assert(JSON.stringify((await shown()).threads) === JSON.stringify([plan.thread_id, notes.thread_id]), 'More did not append the hub summaries');
    await page.keyboard.press('Enter');
    await page.locator('#results .none.bad', { hasText: hostile }).waitFor();
    if (await focusedKey() !== 'more') focusFailures.push('More focus after hostile page error: ' + await focusedKey());
    assert(await page.locator('#results img:not(.av), #results script').count() === 0 && !await page.evaluate(() => window.searchExecuted), 'hostile title/error created markup or ran code');

    // Hold an actual More reply, change the query or participant, and release
    // the obsolete page only after the current result has rendered.
    for (const change of ['query', 'participant']) {
      await box.press('Escape');
      await box.fill(change === 'participant' ? 'grok' : 'release');
      await more.waitFor();
      let releasePage, arrived, delivered;
      const gate = new Promise(resolve => { releasePage = resolve; });
      const ready = new Promise(resolve => { arrived = resolve; });
      const done = new Promise(resolve => { delivered = resolve; });
      const heldPage = async route => {
        arrived();
        await gate;
        try { await json(route, { threads: [notesRow], next: null }); }
        finally { delivered(); }
      };
      await page.route(nextPage, heldPage);
      const watchPage = ignore(req => nextPage(new URL(req.url())));
      await more.click();
      await ready;
      if (change === 'query') {
        await box.fill('digest');
        await page.locator(`#results a[href="#thread/${demo.threads.digest}"]`).waitFor();
      } else {
        await page.locator('#results .sr-peer', { hasText: 'grok-reviewer' }).locator('.sr-pick').click();
        await page.locator('.sr-scope .nm', { hasText: 'grok-reviewer' }).waitFor();
        await settled();
      }
      const current = await shown();
      releasePage();
      await done;
      watchPage();
      await page.unroute(nextPage, heldPage);
      await settled();
      assert(JSON.stringify(await shown()) === JSON.stringify(current), `obsolete More changed ${change} results`);
    }
    await page.unroute(pageQuery, pages);
    await box.press('Escape');

    // A real directory reply supplies long hostile names. Reloading goes
    // through normal session bootstrap and the production name/chip renderer.
    const longName = 'grok-' + hostile + '-long-name'.repeat(30);
    const directoryPath = u => u.pathname === '/v1/peers';
    const hostileDirectory = async route => {
      const response = await route.fetch();
      const body = await response.json();
      body.peers = body.peers.map(peer => peer.peer_id === p('grok') ? { ...peer, name: longName } : peer);
      await route.fulfill({ response, json: body });
    };
    await page.route(directoryPath, hostileDirectory);
    await page.reload();
    await page.locator('#conn.live').waitFor();
    for (const width of [1440, 1000, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 });
      await box.fill('grok');
      await page.waitForResponse(r => titled('grok')(new URL(r.url())));
      await settled();
      assert((await shown()).peers.includes(longName), 'hostile peer name changed');
      await fitsSidebar(width);
      await page.locator('#results .sr-peer', { hasText: longName }).locator('.sr-pick').click();
      await page.locator('.sr-scope .nm', { hasText: longName }).waitFor();
      await settled();
      await fitsSidebar(width);
      assert(await page.locator('#results img:not(.av), #results script, .sr-scope img, .sr-scope script').count() === 0 && !await page.evaluate(() => window.searchExecuted), 'hostile peer/chip created markup or ran code');
      await box.press('Escape');
    }
    await page.unroute(directoryPath, hostileDirectory);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.reload();
    await page.locator('#conn.live').waitFor();
    console.log('G1 exercised: held More, append/error focus, query and participant stale release, hostile peer/title/error text, long names/chips at 1440/1000/390');

    // Current shell: a search result navigates through Settings' leave guard.
    // Typing in its own search field must also keep the slash shortcut there.
    await page.locator('#nav-settings').click();
    const depth = page.locator('#st-agents-default-max-depth');
    await depth.waitFor();
    const settingsSearch = page.getByRole('searchbox', { name: 'Search settings', exact: true });
    await settingsSearch.focus();
    await page.keyboard.press('/');
    assert(await settingsSearch.inputValue() === '/' && await box.inputValue() === '', 'slash left the Settings search field');
    await settingsSearch.fill('');
    const draft = await depth.inputValue() === '31' ? '30' : '31';
    await depth.fill(draft);
    await box.fill('release');
    await page.waitForResponse(r => titled('release')(new URL(r.url())));
    await settled();
    assert(await page.locator('#nav-settings.on').count() === 1, 'search lost the active Settings nav');
    let prompt = '';
    page.once('dialog', d => { prompt = d.message(); d.dismiss(); });
    await page.locator(`#results a[href="#thread/${plan.thread_id}"] .tt`).click();
    await page.waitForFunction(() => location.hash === '#settings');
    assert(prompt.includes('unsaved change') && await depth.inputValue() === draft, 'a search result bypassed the Settings leave guard');
    await box.press('Escape');
    await page.locator('.st-bar [data-fk=undo]').click();

    // Signing out tears down the search and its shortcut with the session.
    await box.fill('release');
    await page.waitForResponse(r => titled('release')(new URL(r.url())));
    await settled();
    await page.locator('#signout').click();
    await page.locator('#login').waitFor();
    assert(await page.locator('#search-q').count() === 0 && await results.isHidden(), 'sign-out kept the search UI');
  });
  assert(!focusFailures.length, focusFailures.join('; '));
  console.log('Search passed: peer names match the loaded directory in the page; thread titles come from GET /v1/threads?q= once typing pauses, equal to the hub, with participant= when a peer is picked; misses, searching and a failure have their own states; a q over 128 characters is never sent; Esc (in the box or on a result) and clear bring the lists back; nothing overflows at 1000 or 390 px; Settings keeps its leave guard and slash input; sign-out removes search');
} finally {
  await cleanBuild();
}
