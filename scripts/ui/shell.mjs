// Task 7: sign-in, the shell, the event pump and reply, in a real browser;
// also the favicon set and brand mark.
import { assert, cleanBuild, networkQuiet, shotPath, withDemo } from './lib.mjs';

const OPERATOR_INBOX = { name: 'Operator inbox', exact: true };

try {
  await withDemo({ signedIn: false }, async ctx => {
    const { page, shot, demo, asPeer, hold, allowConsole, countRequests, fail, quiet } = ctx;
    // Chromium logs every non-2xx fetch as a console error. These are the ones
    // this script provokes: the bad secret, and the views other tasks fill in.
    allowConsole(/status of 401/);
    allowConsole(/status of 501/);
    allowConsole(/status of 503/);

    await page.getByRole('heading', OPERATOR_INBOX).waitFor();
    await quiet();
    await shot('07-shell-initial');

    // Each shell regression owns a tab: a failed check cannot mask the other
    // failures, and signing out never resets another check's module memory.
    const regressions = [];
    async function regression(name, signedIn, run) {
      const context = await page.context().browser().newContext();
      try {
        await context.addInitScript(() => addEventListener('securitypolicyviolation', e => { window.__shellCSP = e.violatedDirective; }));
        if (signedIn) await context.addInitScript(secret => localStorage.setItem('vecnet.ui.secret', secret), demo.operator_secret);
        const p = await context.newPage(), errors = [], q = networkQuiet(p).quiet;
        p.setDefaultTimeout(12000);
        p.on('pageerror', e => errors.push(e.message));
        await p.goto(demo.url + '/ui/');
        await run(p, q);
        assert(!errors.length, errors.join('; '));
        assert(!await p.evaluate(() => window.__shellCSP), 'shell regression violated CSP');
        console.log(`Shell regression passed: ${name}`);
      } catch (e) { regressions.push(`${name}: ${e.message}`); }
      finally { await context.close(); }
    }
    const login = async p => {
      await p.locator('#secret').fill(demo.operator_secret);
      await p.locator('#login-form button').click();
      await p.locator('#conn.live').waitFor();
    };
    await regression('sign-out forgets drafts after unmount', true, async (p, q) => {
      await p.locator('#conn.live').waitFor();
      await p.evaluate(id => { location.hash = '#thread/' + id; }, demo.threads.hark);
      await p.locator('.composer textarea').fill('private unsent reply');
      // Keep both an unmounted compose draft and a mounted reply draft.
      await p.locator('#nav-new').click();
      await p.locator('#c-title').fill('private unsent title');
      await p.locator('#c-text').fill('private unsent message');
      await p.evaluate(id => { location.hash = '#thread/' + id; }, demo.threads.hark);
      await p.locator('.composer textarea').waitFor();
      await p.locator('#signout').click();
      assert(await p.title() === 'vecnet · operator inbox', 'sign-out kept the page title');
      await login(p);
      await p.locator('.composer textarea').waitFor();
      await q();
      assert(await p.locator('.composer textarea').inputValue() === '', 'reply draft crossed sign-out');
      await p.locator('#nav-new').click();
      assert(await p.locator('#c-title').inputValue() === '' && await p.locator('#c-text').inputValue() === '', 'compose draft crossed sign-out');
      // This time compose is still mounted when session.close() saves it.
      await p.locator('#c-title').fill('mounted title');
      await p.locator('#c-text').fill('mounted text');
      await p.locator('#signout').click();
      await login(p);
      await p.locator('#c-text').waitFor();
      assert(await p.locator('#c-title').inputValue() === '' && await p.locator('#c-text').inputValue() === '', 'mounted compose draft crossed sign-out');
    });
    await regression('stored-secret outage and both recovery paths', true, async (p, q) => {
      await p.locator('#conn.live').waitFor();
      for (const manual of [false, true]) {
        await p.route('**/v1/**', r => r.abort('connectionrefused'));
        await p.reload();
        const notice = p.locator('#view .empty.error.hub-down[role=alert]');
        await notice.waitFor({ timeout: 5000 });
        assert(await notice.locator('b').textContent() === 'Hub unreachable', 'missing outage heading');
        assert((await notice.textContent()).includes('hub unreachable. Trying again automatically.'), 'outage did not explain automatic retry');
        const retry = notice.getByRole('button', { name: 'Retry now', exact: true });
        await retry.focus();
        // Another resource notification/route redraw must keep this node.
        await p.evaluate(() => { window.__shellRetry = document.activeElement; location.hash = '#inbox'; });
        await q();
        assert(await retry.evaluate(el => el === window.__shellRetry && el === document.activeElement), 'redraw replaced focused Retry');
        // Keep the event pump offline for the manual path: only reloading
        // peers can name the operator and replace this notice.
        if (manual) await p.route('**/v1/events*', r => r.abort('connectionrefused'));
        await p.unroute('**/v1/**');
        if (manual) await retry.click();
        await p.getByRole('heading', { name: 'Inbox', exact: true }).waitFor({ timeout: 45000 });
        assert(await notice.count() === 0, 'recovery kept outage notice');
        if (manual) await p.unroute('**/v1/events*');
        await p.locator('#conn.live').waitFor();
      }
      // A stored but rejected secret must bypass the outage view entirely.
      await p.route('**/v1/peers', r => r.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated', message: 'rejected' } }) }));
      await p.reload();
      await p.locator('#login:not([hidden])').waitFor();
      assert(await p.locator('.hub-down').count() === 0, '401 became hub-down');
    });
    await regression('route titles and live unread', true, async (p, q) => {
      await p.locator('#conn.live').waitFor();
      async function titleIs(label) {
        await p.waitForFunction(label => {
          const n = Number(document.getElementById('inbox-count').textContent) || 0;
          return document.title === (n > 0 ? `(${n}) ` : '') + label + ' · vecnet';
        }, label, { timeout: 5000 }).catch(async e => { throw new Error(`${label}: got ${await p.title()} (${e.message})`); });
      }
      for (const [hash, label] of [['#inbox', 'Inbox'], ['#new', 'New message'], ['#dash', 'Dashboard'], ['#settings', 'Settings'], ['#peer/' + demo.peers.dot.id, 'dot-research']]) {
        await p.evaluate(hash => { location.hash = hash; }, hash);
        await q();
        await titleIs(label);
      }
      const hostile = p.locator('#threads a', { hasText: '<img src=x onerror=alert(1)>' });
      await hostile.click();
      await p.locator('.thead h1', { hasText: '<img src=x onerror=alert(1)>' }).waitFor();
      await titleIs(await p.locator('.thead h1').textContent());
      assert((await p.title()).includes('<img src=x onerror=alert(1)>'), 'hostile title was not literal text');
      assert(await p.locator('img').count() === 0, 'title parsed hostile markup');
      await p.locator('#nav-inbox').click();
      await q();
      const before = Number(await p.locator('#inbox-count').textContent());
      const sent = await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: 'shell-title-unread', thread_id: demo.threads.hark, priority: 'wake', content: { text: 'Unread title regression' } });
      assert(sent.ok, `title send failed: ${sent.status}`);
      await p.waitForFunction(n => Number(document.getElementById('inbox-count').textContent) === n + 1, before);
      await titleIs('Inbox');
      await p.locator('#signout').click();
      assert(await p.title() === 'vecnet · operator inbox', 'login title not reset');
    });
    await regression('sign-in distinguishes refusal and connection failures', false, async p => {
      assert(await p.title() === 'vecnet · operator inbox', 'sign-in title changed');
      for (const status of [503, 0, 401]) {
        await p.route('**/v1/peers', r => status ? r.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'hub is starting' } }) }) : r.abort('connectionrefused'));
        await p.locator('#secret').fill(demo.operator_secret);
        await p.locator('#login-form button').click();
        const expected = status === 503 ? 'The hub answered 503: hub is starting. Try again shortly.' : status === 0 ? 'Could not reach the hub (hub unreachable). Check your connection, then try again.' : 'The hub rejected that secret.';
        await p.waitForFunction(text => document.getElementById('login-err').textContent === text, expected, { timeout: 5000 });
        const field = await p.locator('#secret').evaluate(el => ({ value: el.value, start: el.selectionStart, end: el.selectionEnd, focus: el === document.activeElement }));
        assert(field.value === (status === 401 ? '' : demo.operator_secret), `status ${status} lost or kept the wrong secret`);
        assert(field.focus && field.start === 0 && field.end === field.value.length, `status ${status} did not select the secret`);
        assert(await p.evaluate(() => localStorage.getItem('vecnet.ui.secret')) === null, 'failed sign-in stored secret');
        await p.unroute('**/v1/peers');
      }
      // Preserve the secret on one last transient error, then Enter retries it.
      await p.route('**/v1/peers', r => r.abort('connectionrefused'));
      await p.locator('#secret').fill(demo.operator_secret);
      await p.locator('#login-form button').click();
      await p.locator('#login-err', { hasText: 'Check your connection' }).waitFor();
      await p.unroute('**/v1/peers');
      await p.keyboard.press('Enter');
      await p.locator('#conn.live').waitFor();
    });
    assert(!regressions.length, regressions.join('\n'));

    // A blocked storage getter or method must not prevent a memory-only session.
    for (const mode of ['getter', 'getItem', 'setItem', 'both', 'removeItem', 'normal']) {
      const context = await page.context().browser().newContext();
      try {
        await context.addInitScript(mode => {
          const blocked = () => { throw new DOMException('Storage blocked', 'SecurityError'); };
          if (mode === 'getter' || mode === 'both') Object.defineProperty(window, 'localStorage', { get: blocked });
          else if (mode !== 'normal') Storage.prototype[mode] = blocked;
          if (mode === 'both') Object.defineProperty(window, 'sessionStorage', { get: blocked });
        }, mode);
        const p = await context.newPage(), errors = [], q = networkQuiet(p).quiet;
        p.on('pageerror', e => errors.push(e.message));
        await p.goto(demo.url + '/ui/');
        await p.locator('#login:not([hidden])').waitFor();
        await p.locator('#secret').fill(demo.operator_secret);
        await p.locator('#login-form button').click();
        if (['getter', 'setItem', 'both'].includes(mode)) {
          await p.locator('#toast.on', { hasText: 'not saved in this browser' }).waitFor();
        }
        await p.locator('#conn.live').waitFor();
        await p.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
        await q();
        await p.locator('#signout').click();
        await p.locator('#login:not([hidden])').waitFor();
        if (mode === 'normal') assert(await p.evaluate(() => localStorage.getItem('vecnet.ui.secret')) === null, 'signed-out secret stayed stored');
        assert(!errors.length, `${mode}: ${errors.join('; ')}`);
      } finally { await context.close(); }
    }

    // A fresh stored session starts with the skip link first in the tab order.
    const keyboard = await page.context().browser().newContext();
    try {
      await keyboard.addInitScript(secret => localStorage.setItem('vecnet.ui.secret', secret), demo.operator_secret);
      const p = await keyboard.newPage(), q = networkQuiet(p).quiet;
      await p.goto(demo.url + '/ui/');
      await p.locator('#conn.live').waitFor();
      await q();
      const hash = await p.evaluate(() => location.hash);
      await p.keyboard.press('Tab');
      assert(await p.locator('.skip').evaluate(el => el === document.activeElement), 'skip link is not first');
      await p.keyboard.press('Enter');
      assert(await p.evaluate(() => document.activeElement.id === 'main' && location.hash === '') && hash === '', 'skip changed hash or missed main');
      await p.locator('#nav-dash').click();
      await p.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
      assert(await p.evaluate(() => document.activeElement.id === 'main'), 'route focus stayed in sidebar');
      await p.keyboard.press('Tab');
      assert(await p.evaluate(() => document.getElementById('view').contains(document.activeElement)), 'Tab did not enter view');
      await p.locator('#nav-new').click();
      await p.locator('#c-search').waitFor();
      assert(await p.locator('#c-search').evaluate(el => el === document.activeElement), 'compose mount focus lost');
      await p.evaluate(() => { location.hash = '#inbox'; });
      await p.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
      await p.setViewportSize({ width: 390, height: 844 });
      await p.locator('.skip').focus();
      await p.keyboard.press('Enter');
      await q();
      const heading = await p.getByRole('heading', { name: 'Inbox', exact: true }).boundingBox();
      assert(heading && heading.y >= 0 && heading.y + heading.height <= 844, `mobile skip missed heading: ${JSON.stringify(heading)}`);
      assert(await p.evaluate(() => document.activeElement.id === 'main' && location.hash === '#inbox'), 'mobile skip changed hash or missed main');
    } finally { await keyboard.close(); }

    // The favicon set: linked (SVG first choice, PNG fallbacks), served as
    // images, the brand mark on the card, and each rendered at tab sizes on
    // a dark and a light tab strip, plus the home-screen tile.
    const icons = await page.locator('link[rel=icon], link[rel=apple-touch-icon]').evaluateAll(ls => ls.map(l => ({ rel: l.rel, href: l.href, sizes: l.getAttribute('sizes') || '' })));
    assert(icons.map(i => `${i.rel}:${i.href.split('/').pop()}:${i.sizes}`).join() === 'icon:favicon-32.png:32x32,icon:favicon.svg:,apple-touch-icon:apple-touch-icon.png:180x180', `icon links ${JSON.stringify(icons)}`);
    for (const i of icons) {
      const r = await fetch(i.href);
      const want = i.href.endsWith('.svg') ? 'image/svg+xml' : 'image/png';
      assert(r.ok && r.headers.get('content-type') === want, `${i.href}: ${r.status} ${r.headers.get('content-type')}`);
    }
    assert(/logo\.svg/.test(await page.locator('#login .logo').evaluate(el => getComputedStyle(el).backgroundImage)), 'the brand mark is not the logo');
    const sheet = await page.context().newPage();
    try {
      const u = name => `${demo.url}/ui/${name}`;
      const tab = (bg, fg) => `<div class="tab" style="background:${bg};color:${fg}">` +
        ['favicon.svg', 'favicon-32.png'].map(n => `<span><img src="${u(n)}" width="16" height="16">vecnet · operator inbox</span>`).join('') +
        `<span><img src="${u('favicon.svg')}" width="32" height="32"><img src="${u('favicon-32.png')}" width="32" height="32"></span></div>`;
      await sheet.setContent(`<body style="margin:0;font:12px system-ui,sans-serif"><div id="sheet" style="display:inline-flex;gap:18px;align-items:center;padding:18px;background:#0b0f19">` +
        `<div style="display:flex;flex-direction:column;gap:10px">${tab('#202124', '#e8eaed')}${tab('#dee1e6', '#3c4043')}</div>` +
        `<img src="${u('apple-touch-icon.png')}" width="90" height="90" style="border-radius:20px">` +
        `<img src="${u('favicon.svg')}" width="128" height="128"></div>` +
        `<style>.tab{display:flex;gap:16px;align-items:center;padding:8px 12px;border-radius:8px}.tab span{display:flex;gap:8px;align-items:center}</style></body>`);
      await sheet.waitForFunction(() => [...document.images].every(i => i.complete && i.naturalWidth > 0));
      await sheet.locator('#sheet').screenshot({ path: await shotPath('07-shell-favicon') });
    } finally { await sheet.close(); }

    // A wrong secret stays on the card, is not stored, and starts no loop.
    await page.locator('#secret').fill('vn_operator_wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await page.locator('#login-err', { hasText: 'rejected' }).waitFor();
    assert(await page.locator('#login').isVisible(), 'bad secret left the login card');
    assert(await page.locator('#secret').inputValue() === '', 'wrong secret was not cleared');
    assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.secret')) === null, 'bad secret was stored');
    assert(countRequests('/v1/events') === 0, 'bad secret started the event loop');
    await quiet();
    await shot('07-shell-error');

    // A stale read list from the old page is removed on load.
    await page.evaluate(secret => {
      localStorage.setItem('vecnet.ui.secret', secret);
      localStorage.setItem('vecnet.ui.read', '["dly_old"]');
    }, demo.operator_secret);
    // lib.mjs hold() unroutes as it releases. If that empties the page's route
    // list, Playwright drops interception and continues the request itself, so
    // the handler's own continue throws "Route is already handled". A second
    // route keeps interception on.
    await page.route('**/__keep-interception__', route => route.continue());
    const release = await hold('**/v1/peers');
    await page.reload({ waitUntil: 'load' });
    await page.locator('nav.side').waitFor();
    assert(await page.locator('#conn').textContent() === 'connecting', 'shell is not connecting while the directory is held');
    assert(countRequests('/v1/events') === 0, 'pump started before the directory answered');
    assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.read')) === null, 'vecnet.ui.read survived');
    // The thread lists and inbox count load beside the held directory: the
    // shot waits for them (and for nothing but /v1/peers to be in flight).
    await page.locator('#threads a', { hasText: 'hark B196' }).waitFor();
    await quiet();
    await shot('07-shell-progress');
    await release();

    await page.locator('#threads a', { hasText: 'hark B196' }).waitFor();
    await page.locator('#conn.live').waitFor();
    for (const name of ['Inbox', 'New message', 'Dashboard']) await page.locator('nav.side a', { hasText: name }).first().waitFor();
    await page.locator('#signout').waitFor();

    // Review Focus 4: the hub picks the start. No cursor is ever invented.
    const eventCalls = () => page.evaluate(() => performance.getEntriesByType('resource').map(e => e.name).filter(u => u.includes('/v1/events')));
    const first = (await eventCalls())[0];
    assert(first && !first.includes('after=') && first.includes('wait=0'), `first events call was ${first}`);
    assert(countRequests('after=e0.0') === 0, 'a client sent e0.0');

    // Routes set body[data-route] and swap the screen.
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    assert(await page.evaluate(() => document.body.dataset.route) === 'dash', 'route not on body');
    assert(await page.locator('#nav-dash.on').count() === 1, 'dashboard not active in nav');
    // The active link is also the current page for assistive technology, and alone in that.
    const current = await page.locator('nav.side [aria-current]').evaluateAll(els => els.map(e => e.id + '=' + e.getAttribute('aria-current')).join());
    assert(current === 'nav-dash=page', `aria-current in the sidebar: ${current}`);
    await quiet();
    await page.locator('#main').evaluate(el => { el.scrollTop = 300; });
    assert(await page.locator('#main').evaluate(el => el.scrollTop > 0), 'dashboard did not scroll');
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    await quiet();
    assert(await page.locator('#main').evaluate(el => el.scrollTop === 0), 'inbox retained dashboard scroll');
    await page.evaluate(() => { location.hash = '#new'; });
    await page.getByRole('heading', { name: 'New message', exact: true }).waitFor();
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();

    // Phone (one column, the document scrolls): the thread lists fold behind
    // Threads, so the page starts on the first screen; a sidebar route link
    // brings the new page's top into view. Above 720 px neither applies.
    const tog = page.locator('#side-tog'), lists = page.locator('#lists'), inboxH = page.getByRole('heading', { name: 'Inbox', exact: true });
    const expanded = () => tog.getAttribute('aria-expanded');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => scrollTo(0, 0));
    assert(await tog.getAttribute('aria-controls') === 'lists' && await expanded() === 'false' && await lists.isHidden(), 'phone thread lists are not folded behind Threads');
    // Its dot (an unread thread in Your Threads) is in its accessible name too.
    const mineUnread = await page.locator('#threads a.unread').count();
    assert(mineUnread > 0 && await page.getByRole('button', { name: 'Threads, unread', exact: true }).isVisible(), `Threads does not name its unread dot (${mineUnread} unread rows)`);
    const folded = await inboxH.boundingBox();
    assert(folded && folded.y >= 0 && folded.y + folded.height <= 844, `phone Inbox heading is not on the first screen: ${JSON.stringify(folded)}`);
    await tog.click();
    assert(await expanded() === 'true' && await page.locator('#threads a').first().isVisible(), 'Threads did not open the lists');
    await tog.click();
    assert(await expanded() === 'false' && await lists.isHidden(), 'Threads did not close the lists');
    await tog.click();
    await page.locator('#nav-dash').click();
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    assert(await expanded() === 'false' && await lists.isHidden(), 'a route change left Threads open');
    await quiet();
    await page.evaluate(() => scrollTo(0, 0));
    await page.locator('#nav-inbox').click();
    await inboxH.waitFor();
    const shown = await page.evaluate(() => ({ y: scrollY, main: document.getElementById('main').getBoundingClientRect().top }));
    const revealed = await inboxH.boundingBox();
    assert(shown.y > 0 && Math.abs(shown.main) <= 1, `the Inbox link did not bring the page's top into view: ${JSON.stringify(shown)}`);
    assert(revealed && revealed.y >= 0 && revealed.y + revealed.height <= 844, `phone Inbox heading off screen after the link: ${JSON.stringify(revealed)}`);
    await page.setViewportSize({ width: 1000, height: 900 });
    const wide = await page.evaluate(() => ({ tog: getComputedStyle(document.getElementById('side-tog')).display, lists: getComputedStyle(document.getElementById('lists')).display }));
    assert(wide.tog === 'none' && wide.lists !== 'none', `at 1000 px Threads shows or the lists hide: ${JSON.stringify(wide)}`);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.locator('#nav-dash').click();
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await page.locator('#nav-inbox').click();
    await inboxH.waitFor();
    assert(await page.evaluate(() => scrollY) === 0, 'a desktop route link scrolled the document');

    // A thread reached through #matrix is the same mounted thread. The real
    // matrix (Task 11) is a modal <dialog>, so the thread under it is inert:
    // the draft is typed on #thread and must survive #thread → #matrix → #thread.
    await page.evaluate(id => { location.hash = `#matrix/${id}`; }, demo.threads.hark);
    await page.locator('.composer textarea').waitFor();
    await page.locator('dialog.mx[open]').waitFor();
    assert(await page.evaluate(() => document.body.dataset.route) === 'matrix', 'matrix route not on body');
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await page.locator('dialog.mx').waitFor({ state: 'detached' });
    await page.locator('.msg').first().waitFor();
    await page.locator('.composer textarea').fill('kept across routes');
    assert(await page.locator('.composer textarea').inputValue() === 'kept across routes', 'the draft did not take');
    await page.evaluate(() => { document.querySelector('.composer textarea').dataset.mounted = 'once'; });
    await page.evaluate(id => { location.hash = `#matrix/${id}`; }, demo.threads.hark);
    await page.locator('dialog.mx[open]').waitFor();
    assert(await page.locator('.composer textarea[data-mounted="once"]').inputValue() === 'kept across routes', 'moving from #thread to #matrix remounted the view');
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await page.locator('dialog.mx').waitFor({ state: 'detached' });
    await page.locator('.msg').first().waitFor();
    assert(await page.locator('.composer textarea[data-mounted="once"]').inputValue() === 'kept across routes', 'moving between #matrix and #thread remounted the view');
    await page.locator('.composer textarea').fill('');

    // Bodies are untrusted: the hostile title is inert text wherever it lands.
    const hostile = page.locator('#threads a', { hasText: '<img src=x onerror=alert(1)>' });
    await hostile.waitFor();
    await hostile.click();
    await page.locator('.msg .txt', { hasText: '<img src=x onerror=alert(1)>' }).first().waitFor();
    await page.locator('.thead h1', { hasText: '<img src=x onerror=alert(1)>' }).waitFor();
    assert(await page.locator('img').count() === 0, 'a body became an element');

    // Reply, with × dismissing the default target and a click restoring one.
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await page.locator('.msg', { hasText: 'Classifier checks passed.' }).waitFor();
    await page.locator('.composer .to').waitFor();
    // Hold an event-driven history reload across dismissal. Its fresh snapshot
    // must not silently select the default target again after the click.
    const historyURL = `**/v1/threads/${demo.threads.hark}/messages?*`;
    let releaseHistory;
    const historyGate = new Promise(resolve => { releaseHistory = resolve; });
    await page.route(historyURL, async route => { await historyGate; await route.continue(); });
    const reloading = page.waitForRequest(req => req.url().includes(`/v1/threads/${demo.threads.hark}/messages?`));
    const freshText = 'History reload across reply dismissal.';
    const fresh = await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: 'shell-dismiss-reload', thread_id: demo.threads.hark, priority: 'hold', content: { text: freshText } });
    assert(fresh.ok, `reload trigger failed: ${fresh.status}`);
    await reloading;
    try {
      await page.locator('.composer .to button').click();
      await page.locator('.composer .to').waitFor({ state: 'hidden' });
    } finally { releaseHistory(); }
    await page.locator('.msg .txt', { hasText: freshText }).waitFor();
    await page.unroute(historyURL);
    assert(await page.locator('.composer .to').isHidden(), 'history reload restored the dismissed reply target');
    await page.locator('.msg', { hasText: 'Classifier checks passed.' }).getByRole('button', { name: 'reply' }).click();
    await page.locator('.composer .to', { hasText: 'Classifier checks passed.' }).waitFor();
    const reply = 'Thanks, shipping the shell.';
    await page.locator('.composer textarea').fill(reply);
    // The toast lasts 2.5 s from before the thread reloads, so it is awaited from
    // before the press: a slow reload must not outlast it.
    await Promise.all([
      page.locator('#toast.on', { hasText: 'Sent' }).waitFor(),
      page.keyboard.press('Enter'),
    ]);
    await page.locator('.msg .txt', { hasText: reply }).waitFor();
    const history = await (await asPeer('operator', 'GET', `/v1/threads/${demo.threads.hark}/messages?limit=200`)).json();
    const sent = history.messages.find(m => m.content.text === reply);
    assert(sent, 'reply did not reach the hub');
    assert(countRequests(req => req.method() === 'POST' && req.url().endsWith('/v1/messages') && req.headers()['x-vecnet-ingress'] === 'operator_ui') === 1, 'reply not marked operator_ui');
    assert(sent.in_reply_to, 'reply lost its target');
    // The thread redraws its whole list on each snapshot (the send's reload and
    // then the pump's event), so find and scroll in one page-side step rather
    // than through a locator handle the next redraw can detach.
    await page.waitForFunction(text => {
      const el = [...document.querySelectorAll('.msg')].find(m => m.textContent.includes(text));
      if (el) el.scrollIntoView({ block: 'nearest' });
      return !!el;
    }, reply);
    // The toast sits over the top message, so the shot waits for it to slide out.
    await page.locator('#toast.on').waitFor({ state: 'hidden' });
    await page.evaluate(() => Promise.all(document.getElementById('toast').getAnimations().map(a => a.finished)));
    await quiet();
    await shot('07-shell-success');

    // A peer's message wakes the parked long-poll and reaches the open thread
    // through the event pump, not a reload.
    const pushed = 'Pump check: release is tagged.';
    const posted = await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: 'shell-pump', thread_id: demo.threads.hark, priority: 'wake', content: { text: pushed } });
    assert(posted.ok, `peer send failed: ${posted.status}`);
    await page.locator('.msg .txt', { hasText: pushed }).waitFor();

    // dom.js contract, in the browser it runs in.
    const dom = await page.evaluate(async () => {
      const { confirmButton, h } = await import('/ui/dom.js');
      let confirmed = 0;
      const btn = confirmButton('Discard', 'Really?', () => { confirmed++; }, 60);
      const labels = [btn.textContent];
      btn.click();
      labels.push(btn.textContent);
      btn.click();
      labels.push(btn.textContent);
      btn.click();
      labels.push(btn.textContent);
      await new Promise(resolve => { new MutationObserver(resolve).observe(btn, { childList: true }); });
      labels.push(btn.textContent);
      const text = h('div', { text: '<b>x</b>' });
      return { confirmed, labels, children: text.children.length };
    });
    assert(dom.confirmed === 1, `confirmButton fired ${dom.confirmed} times`);
    assert(dom.labels.join() === 'Discard,Really?,Discard,Really?,Discard', `confirmButton labels ${dom.labels}`);
    assert(dom.children === 0, 'h text parsed markup');

    // A hub restart across a page load. The log is fully acked first, so no
    // event can prompt the refetch: recovery alone has to bring the views back.
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    await page.waitForFunction(async secret => {
      const r = await fetch('/v1/events?wait=0', { headers: { Authorization: `Bearer ${secret}` } });
      return (await r.json()).events.length === 0;
    }, demo.operator_secret, { polling: 100 });
    await fail('**/v1/**', 503, 'unavailable', 'Hub restarting');
    await page.reload({ waitUntil: 'load' });
    // A planned restart (503) reads as reconnecting, not offline.
    await page.locator('#conn.retry', { hasText: 'reconnecting' }).waitFor();
    await page.locator('#threads .none', { hasText: 'Threads unavailable' }).waitFor();
    await page.unroute('**/v1/**');
    await page.locator('#conn.live').waitFor();
    await page.locator('#threads a', { hasText: 'hark B196' }).waitFor();
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    await page.locator('#view', { hasText: 'Hub restarting' }).waitFor({ state: 'hidden' });

    // Review Focus 4: a rotated secret returns to sign-in with no retry storm.
    await fail('**/v1/events*', 401, 'unauthenticated', 'Secret rotated');
    const wake = await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: 'shell-401', thread_id: demo.threads.hark, priority: 'wake', content: { text: 'one more' } });
    assert(wake.ok, `peer send failed: ${wake.status}`);
    await page.getByRole('heading', OPERATOR_INBOX).waitFor();
    assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.secret')) === null, 'rejected secret stayed stored');
    const after401 = countRequests('/v1/events');
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    assert(countRequests('/v1/events') === after401, 'event loop kept polling after 401');
  });
  console.log('Shell passed: sign-in, routes, pump, reply, hostile text, outage recovery, 401 stop, no CSP violations');
} finally { await cleanBuild(); }
