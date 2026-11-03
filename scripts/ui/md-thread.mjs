// Issues 21, 23 and 25 in a real browser: markdown message bodies (links,
// autolinks, code, lists, emphasis, headings), a hostile body that must stay
// inert, the delta and relative-time labels in each header, and the priority
// tips on hover and keyboard focus in both composers. Desktop and narrow.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const HUB_URL = 'https://xsm.hornbill-boa.ts.net:29044';
const RICH = [
  '### Deploy notes for B196',
  '',
  'The **radio end-phrase** fix is live on staging; the *old classifier* stays behind a flag.',
  'Runbook: [the runbook](' + HUB_URL + '/runbook), dashboard at ' + HUB_URL + ' and the PR <https://github.com/clankercode/vecnet/pull/31>.',
  '',
  '- restart `hark-radio` after the deploy',
  '- watch the **finalize** counter for _ten minutes_',
  '- page me if it drops',
  '',
  '1. pull master',
  '2. run the migration',
  '',
  '```',
  '$ just deploy staging --service hark-radio --wait-healthy --timeout 300s --log-level debug --tag b196-radio-end-phrase',
  'ok: 3/3 healthy',
  '```',
  '',
  '> The finalize step was skipped twice last night.',
].join('\n');
const HOSTILE = [
  'Totally normal update <script>window.__pwned = 1</script>',
  '<img src=x onerror="window.__pwned = 2">',
  'Sign in at [github.com](https://evil.example/login) to approve.',
  'Or [click here](javascript:window.__pwned=3) or <javascript:window.__pwned=4> or [data](data:text/html,hi).',
  '![tracking pixel](https://evil.example/pixel.png)',
].join('\n');
const REL = 'noopener noreferrer nofollow';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, advance, countRequests } = ctx;
    let dialogs = 0;
    page.on('dialog', d => { dialogs++; d.dismiss().catch(() => {}); });
    const api = async (method, path, body, as = 'operator') => {
      const r = await asPeer(as, method, path, body);
      assert(r.ok, `${as} ${method} ${path}: ${r.status}`);
      return r.json();
    };
    const msgEl = id => page.locator(`[id="m-${id}"]`);
    // Any resource notify rebuilds the .msg nodes, so measure the live node by
    // selector, once it has a layout box.
    const measure = sel => page.waitForFunction(sel => {
      const el = document.querySelector(sel), r = el && el.getBoundingClientRect();
      return r && r.width > 0 && { sw: el.scrollWidth, cw: el.clientWidth, x: getComputedStyle(el).overflowX, right: r.right };
    }, sel).then(h => h.jsonValue());
    const help = await page.evaluate(async () => (await import('./format.js')).PRIORITY_HELP);

    // A fresh thread with known gaps: +4m 12s, then +42s (the demo hub clock).
    const first = await api('POST', '/v1/messages', {
      idempotency_key: 'md-first', recipients: [demo.peers.codex.id, demo.peers.claw.id], thread_title: 'Markdown and timing',
      priority: 'wake', content: { text: 'Can you post the deploy notes for **B196**?' },
    });
    const thr = first.thread_id;
    await advance('4m12s');
    const rich = await api('POST', '/v1/messages', { idempotency_key: 'md-rich', thread_id: thr, priority: 'wake_idle', content: { text: RICH } }, 'codex');
    await advance('42s');
    const hostile = await api('POST', '/v1/messages', { idempotency_key: 'md-hostile', thread_id: thr, priority: 'hold', content: { text: HOSTILE } }, 'claw');

    // created_at as the hub stamped it (a send's answer does not carry it).
    const stamped = new Map((await api('GET', `/v1/threads/${thr}/messages?limit=10`)).messages.map(m => [m.message_id, m.created_at]));
    for (const m of [first, rich, hostile]) m.created_at = stamped.get(m.message_id);
    assert(first.created_at && rich.created_at && hostile.created_at, 'the thread history lacks a created_at');

    await page.locator('#conn.live').waitFor();
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, thr);
    const richEl = msgEl(rich.message_id), hostEl = msgEl(hostile.message_id), firstEl = msgEl(first.message_id);
    await hostEl.locator('.txt .md').waitFor();
    await richEl.locator('.txt .md pre.md-pre').waitFor();

    // Markdown: every construct renders as its element.
    const md = richEl.locator('.txt .md');
    assert(await md.locator('h5.md-h').textContent() === 'Deploy notes for B196', 'the ### heading did not render small');
    assert(await md.locator('strong', { hasText: 'radio end-phrase' }).count() === 1, 'bold did not render');
    assert(await md.locator('em', { hasText: 'old classifier' }).count() === 1, 'italic did not render');
    assert(await md.locator('ul > li').count() === 3 && await md.locator('ol > li').count() === 2, 'the lists did not render');
    assert(await md.locator('li code', { hasText: 'hark-radio' }).count() === 1, 'inline code did not render');
    assert(await md.locator('blockquote', { hasText: 'finalize step was skipped' }).count() === 1, 'the quote did not render');
    assert((await md.locator('pre.md-pre code').textContent()).startsWith('$ just deploy staging'), 'the code block lost its text');
    const links = await md.locator('a').evaluateAll(as => as.map(a => ({ href: a.getAttribute('href'), text: a.textContent, rel: a.rel, target: a.target, title: a.title, host: a.nextElementSibling && a.nextElementSibling.matches('.md-host') ? a.nextElementSibling.textContent : '' })));
    const byHref = h => links.find(l => l.href === h);
    assert(links.length === 3, `rich message has ${links.length} links, not 3: ${JSON.stringify(links)}`);
    assert(byHref(HUB_URL + '/runbook')?.text === 'the runbook' && byHref(HUB_URL + '/runbook').host === ' (xsm.hornbill-boa.ts.net:29044)', 'the labelled link does not name its host');
    assert(byHref(HUB_URL + '/')?.text === HUB_URL && !byHref(HUB_URL + '/').host, 'the bare URL did not autolink');
    assert(byHref('https://github.com/clankercode/vecnet/pull/31')?.text === 'https://github.com/clankercode/vecnet/pull/31', 'the <url> autolink did not render');
    for (const l of links) assert(l.rel === REL && l.target === '_blank' && l.title === l.href, `link ${l.href} lacks rel, target or title`);
    // The long command scrolls inside its box, never the page.
    const pre = await measure(`[id="m-${rich.message_id}"] pre.md-pre`);
    assert(pre.sw > pre.cw && pre.x === 'auto', 'the code block does not scroll horizontally: ' + JSON.stringify(pre));

    // Hostile: literal text, no elements, no script, inert links.
    const hostText = await hostEl.locator('.txt').textContent();
    assert(hostText.includes('<script>window.__pwned = 1</script>') && hostText.includes('<img src=x onerror='), 'hostile markup did not stay literal');
    assert(await page.locator('#view script, #view img, #view iframe').count() === 0, 'a hostile body became an element');
    assert(await page.evaluate(() => window.__pwned) === undefined && dialogs === 0, 'a hostile body ran script');
    assert(await page.locator('a[href^="javascript:"], a[href^="data:"]').count() === 0, 'a javascript: or data: link became a link');
    assert(hostText.includes('[click here](javascript:window.__pwned=3)') && hostText.includes('<javascript:window.__pwned=4>'), 'refused links are not shown as text');
    const lure = hostEl.locator('a', { hasText: 'github.com' });
    assert(await lure.getAttribute('href') === 'https://evil.example/login', 'the lure link lost its real target');
    assert(await lure.evaluate(a => a.nextElementSibling?.matches('.md-host') && a.nextElementSibling.textContent) === ' (evil.example)', 'the .md-host disclosure is missing');
    const pixel = hostEl.locator('a.md-img');
    assert(await pixel.getAttribute('href') === 'https://evil.example/pixel.png', 'the image did not become a link');
    for (const l of await page.locator('.md a').evaluateAll(as => as.map(a => [a.rel, a.target]))) assert(l[0] === REL && l[1] === '_blank', 'a markdown link lacks rel or target');
    assert(countRequests('evil.example') === 0, 'the page fetched from evil.example');

    // Timing: the delta beside the timestamp, the relative time beside reply.
    assert(await firstEl.locator('header .dt').count() === 0, 'the first message has a delta');
    const dt = async el => ({ text: await el.locator('header .dt').textContent(), title: await el.locator('header .dt').getAttribute('title') });
    const d1 = await dt(richEl), d2 = await dt(hostEl);
    // The hub stamps created_at on the demo clock plus the real time the
    // requests took, so the gaps are 4m 12s and 42s give or take a second.
    const want = await page.evaluate(async ([a, b, c]) => {
      const f = await import('./format.js');
      return [[f.deltaLabel(a, b), f.deltaTitle(a, b)], [f.deltaLabel(b, c), f.deltaTitle(b, c)]];
    }, [first.created_at, rich.created_at, hostile.created_at]);
    assert(/^\+4m 1[234]s$/.test(d1.text) && d1.text === want[0][0] && d1.title === want[0][1] && d1.title === d1.text.slice(1) + ' after the previous message', `rich delta is ${d1.text} / ${d1.title}, want ${JSON.stringify(want)} from ${first.created_at} ${rich.created_at}`);
    assert(/^\+4[234]s$/.test(d2.text) && d2.text === want[1][0] && d2.title === d2.text.slice(1, -1) + ' s after the previous message', `hostile delta is ${d2.text} / ${d2.title}`);
    const agoOf = el => el.locator('header .acts time.ago');
    assert(/^4m ago$/.test(await agoOf(firstEl).textContent()), `first message reads ${await agoOf(firstEl).textContent()}`);
    const expectTitle = await page.evaluate(iso => new Date(iso).toLocaleString('en-AU', { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }), first.created_at);
    assert(await agoOf(firstEl).getAttribute('title') === expectTitle && /^\w{3},? \d{1,2} \w{3} \d{4},? \d\d:\d\d:\d\d$/.test(expectTitle), `the ago title is not the full local time: ${expectTitle}`);
    assert(await agoOf(firstEl).getAttribute('datetime') === first.created_at, 'the ago label lost its datetime');
    // The tick rewrites a label only when it changes: watch the last one tick
    // over while the first (minutes old) is left alone.
    // A rebuild (any resource notify) replaces the nodes; watch again if so.
    const quiet = await page.evaluate(async ([fid, hid]) => {
      for (let round = 0; round < 20; round++) {
        const f = document.querySelector(`[id="m-${fid}"] time.ago`), l = document.querySelector(`[id="m-${hid}"] time.ago`);
        const start = l.textContent;
        let rewrites = 0;
        const mo = new MutationObserver(ms => { rewrites += ms.length; });
        mo.observe(f, { childList: true, characterData: true, subtree: true });
        const outcome = await new Promise(resolve => {
          const poll = () => {
            if (!f.isConnected || !l.isConnected) resolve('rebuilt');
            else if (l.textContent !== start) resolve('ticked');
            else requestAnimationFrame(poll);
          };
          poll();
        });
        mo.disconnect();
        if (outcome === 'ticked') return { rewrites, start, now: l.textContent };
      }
      return null;
    }, [first.message_id, hostile.message_id]);
    assert(quiet, 'the newest ago label never ticked over');
    assert(quiet.rewrites === 0, `an unchanged ago label was rewritten ${quiet.rewrites} times while ${quiet.start} became ${quiet.now}`);
    await page.mouse.move(0, 0);
    await firstEl.evaluate(el => el.scrollIntoView({ block: 'start' }));
    await shot('md-thread-desktop');
    await hostEl.scrollIntoViewIfNeeded();
    await shot('md-hostile-desktop');

    // Priority tips in the reply box: hover, then keyboard focus, then Escape.
    const comp = page.locator('.composer');
    // The visible tip is aria-hidden; the button's description is a hidden
    // element with the same words, so a screen reader hears them once.
    const tipFor = (scope, p) => scope.locator(`.ptip[data-p="${p}"]`);
    const checkTips = async scope => {
      for (const p of ['wake', 'wake_idle', 'hold']) {
        const btn = scope.locator(`.seg button[value="${p}"]`);
        const id = await btn.getAttribute('aria-describedby');
        const desc = page.locator(`[id="${id}"]`);
        assert(await desc.count() === 1 && await desc.textContent() === help[p] && await desc.evaluate(el => el.hidden), `the ${p} description is not a hidden PRIORITY_HELP`);
        const tip = tipFor(scope, p);
        assert(await tip.textContent() === p + help[p] && await tip.getAttribute('aria-hidden') === 'true', `the ${p} tip is not its name and PRIORITY_HELP`);
        assert(!(await tip.isVisible()), `the ${p} tip shows before hover or focus`);
        assert(await btn.getAttribute('title') === null, `the ${p} button has a competing title`);
      }
    };
    await checkTips(comp);
    const idleTip = tipFor(comp, 'wake_idle');
    await comp.locator('.seg button[value="wake_idle"]').hover();
    await idleTip.waitFor({ state: 'visible' });
    await page.mouse.move(0, 0);
    await idleTip.waitFor({ state: 'hidden' });
    await comp.locator('.seg button[value="wake"]').focus();
    await page.keyboard.press('Tab');
    assert(await page.evaluate(() => document.activeElement.value) === 'wake_idle', 'Tab did not reach wake_idle');
    await idleTip.waitFor({ state: 'visible' });
    // The tip sits above the reply box and inside the viewport.
    const tb = await idleTip.boundingBox(), vw = page.viewportSize().width;
    assert(tb.x >= 0 && tb.x + tb.width <= vw, 'the tip runs off the screen');
    await shot('md-tip-thread-desktop');
    await page.keyboard.press('Escape');
    await idleTip.waitFor({ state: 'hidden' });
    await page.keyboard.press('Tab');
    await tipFor(comp, 'hold').waitFor({ state: 'visible' });

    // The new-message form has the same tips.
    await page.evaluate(() => { location.hash = '#new'; });
    const form = page.locator('.compose');
    await form.locator('.seg button[value="hold"]').waitFor();
    await checkTips(form);
    const holdTip = tipFor(form, 'hold');
    await form.locator('.seg button[value="hold"]').hover();
    await holdTip.waitFor({ state: 'visible' });
    await shot('md-tip-compose-desktop');
    await page.mouse.move(0, 0);
    await holdTip.waitFor({ state: 'hidden' });
    // Narrow compose: the tip still fits on screen.
    await page.setViewportSize({ width: 390, height: 844 });
    await form.locator('.seg button[value="wake"]').focus();
    await page.keyboard.press('Tab');
    const formIdle = tipFor(form, 'wake_idle');
    await formIdle.waitFor({ state: 'visible' });
    await form.locator('.seg').scrollIntoViewIfNeeded();
    await shot('md-tip-compose-narrow');
    const fb = await formIdle.boundingBox();
    assert(fb.x >= 0 && fb.x + fb.width <= 390, 'the narrow compose tip runs off the screen: ' + JSON.stringify(fb));
    await page.setViewportSize({ width: 1440, height: 900 });

    // Narrow: the relative time keeps the reply button on its line, and nothing
    // overflows the page.
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, thr);
    await richEl.locator('.txt .md pre.md-pre').waitFor();
    const rows = await page.locator('.msg header').evaluateAll(hs => hs.map(h => {
      const a = h.querySelector('time.ago').getBoundingClientRect(), b = h.querySelector('.acts button').getBoundingClientRect();
      return { same: Math.abs(a.top + a.height / 2 - (b.top + b.height / 2)) < 4, right: b.right };
    }));
    assert(rows.length >= 3 && rows.every(r => r.same && r.right <= 390), 'a narrow header split the relative time from reply, or ran off screen');
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'the narrow thread scrolls sideways');
    const preN = await measure(`[id="m-${rich.message_id}"] pre.md-pre`);
    assert(preN.sw > preN.cw && preN.right <= 390, 'the narrow code block overflows the page');
    await richEl.scrollIntoViewIfNeeded();
    await shot('md-thread-narrow');
    await page.locator('.composer textarea').focus();
    await page.locator('.composer .seg button[value="wake"]').focus();
    await page.keyboard.press('Tab');
    const narrowTip = tipFor(comp, 'wake_idle');
    await narrowTip.waitFor({ state: 'visible' });
    const nb = await narrowTip.boundingBox();
    assert(nb.x >= 0 && nb.x + nb.width <= 390, 'the narrow tip runs off the screen');
    await shot('md-tip-thread-narrow');
    assert(await page.evaluate(() => window.__pwned) === undefined && dialogs === 0, 'a hostile body ran script');
  });
  console.log('ok - md-thread: markdown, hostile body, timing labels and priority tips');
} finally {
  await cleanBuild();
}
