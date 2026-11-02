// Task 7: sign-in, the shell, the event pump and reply, in a real browser.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const OPERATOR_INBOX = { name: 'Operator inbox', exact: true };

try {
  await withDemo({ signedIn: false }, async ctx => {
    const { page, shot, demo, asPeer, hold, allowConsole, countRequests, fail } = ctx;
    // Chromium logs every non-2xx fetch as a console error. These are the ones
    // this script provokes: the bad secret, and the views other tasks fill in.
    allowConsole(/status of 401/);
    allowConsole(/status of 501/);
    allowConsole(/status of 503/);

    await page.getByRole('heading', OPERATOR_INBOX).waitFor();
    await shot('07-shell-initial');

    // A wrong secret stays on the card, is not stored, and starts no loop.
    await page.locator('#secret').fill('vn_operator_wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await page.locator('#login-err', { hasText: 'rejected' }).waitFor();
    assert(await page.locator('#login').isVisible(), 'bad secret left the login card');
    assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.secret')) === null, 'bad secret was stored');
    assert(countRequests('/v1/events') === 0, 'bad secret started the event loop');
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
    await page.evaluate(() => { location.hash = '#new'; });
    await page.getByRole('heading', { name: 'New message', exact: true }).waitFor();
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();

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
    await page.locator('#conn.off').waitFor();
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
