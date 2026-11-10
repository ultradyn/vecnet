// Task 15: start a new thread from #new, in a real browser.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const isSend = req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/messages';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, allowConsole } = ctx;
    // Exercise the directory's normal timer callback without a minute-long wait.
    await page.addInitScript(() => {
      const interval = window.setInterval;
      window.setInterval = (fn, ms, ...args) => {
        if (ms === 60000) window.refreshDirectory = fn;
        return interval(fn, ms, ...args);
      };
    });
    await page.reload();
    // Chromium logs every non-2xx fetch as a console error: the two rejections
    // this script stages, and the views other tasks have yet to fill in.
    allowConsole(/status of 403/);
    allowConsole(/status of 429/);
    allowConsole(/status of 501/);
    allowConsole(/status of 503/);
    const sends = [];
    page.on('request', req => { if (isSend(req)) sends.push(req.postDataJSON()); });

    const id = key => demo.peers[key].id;
    const view = page.locator('.compose');
    const send = view.getByRole('button', { name: /^(Send|Sending…)$/ });
    const search = view.getByLabel('Search peers');
    const text = view.locator('#c-text');
    const opt = peerId => view.locator(`.opts .opt[title="${peerId}"]`);
    const pick = async peerId => { await opt(peerId).click(); await view.locator(`.pick[title="${peerId}"]`).waitFor(); };

    await page.evaluate(() => { location.hash = '#new'; });
    await page.getByRole('heading', { name: 'New message', exact: true }).waitFor();
    await opt(id('codex')).waitFor();

    // Eligible peers: never the operator, never a webhook source.
    assert(await opt(demo.operator_peer).count() === 0, 'the operator is offered as a recipient');
    assert(await opt(id('gh')).count() === 0, 'a source peer is offered as a recipient');
    for (const key of ['grok', 'sleuth', 'digest', 'claw', 'dot', 'triage', 'hostile']) assert(await opt(id(key)).count() === 1, `${key} is not offered`);
    // The hostile peer name is inert text.
    assert(await page.locator('img').count() === 0, 'a peer name became an element');
    assert(await send.isDisabled(), 'Send enabled on an empty form');
    await shot('15-compose-initial');

    // Selecting an option and a background refresh keep its keyboard position.
    for (const key of ['codex', 'sleuth']) {
      await opt(id(key)).focus();
      await page.keyboard.press('Enter');
      assert(await opt(id(key)).evaluate(el => el === document.activeElement && el.getAttribute('aria-pressed') === 'true'), 'recipient option lost focus on Enter');
    }
    assert(await view.locator('.picked .pick').count() === 2, 'keyboard did not pick both peers');
    for (const control of [opt(id('codex')), view.locator(`.pick[title="${id('codex')}"] button`)]) {
      await control.focus();
      const refreshed = page.waitForResponse(r => new URL(r.url()).pathname === '/v1/peers');
      await page.evaluate(() => window.refreshDirectory());
      await refreshed;
      await ctx.quiet();
      assert(await control.evaluate(el => el === document.activeElement), 'directory refresh lost recipient control focus');
    }
    for (const key of ['codex', 'sleuth']) await view.locator(`.pick[title="${id(key)}"] button`).click();
    assert(await search.evaluate(el => el === document.activeElement), 'removing a chip did not focus search');

    // A peer-page handoff replaces a typed draft, not just its recipients.
    await pick(id('codex'));
    await view.locator('#c-title').fill('Superseded draft');
    await text.fill('Do not carry this into a peer-page message.');
    await view.getByRole('button', { name: 'hold', exact: true }).click();
    await page.evaluate(peer => { location.hash = '#peer/' + peer; }, id('grok'));
    await page.locator('.pv-acts a.pv-new').click();
    await opt(id('grok')).waitFor();
    assert(await view.locator('.pick').count() === 1 && await view.locator(`.pick[title="${id('grok')}"]`).count() === 1, 'handoff did not replace kept recipients');
    assert(await text.inputValue() === '' && await view.locator('#c-title').inputValue() === '', 'handoff kept the old title or text');
    assert(await view.locator('.seg button.on').getAttribute('value') === 'wake_idle', 'handoff kept the old priority');
    assert(await text.evaluate(el => el === document.activeElement), 'handoff did not focus the message');
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await page.locator('#nav-new').click();
    await opt(id('codex')).waitFor();
    assert(await view.locator('.pick').count() === 0, 'recipient-only handoff resurrected the replaced draft');
    assert(await view.locator('.seg button.on').getAttribute('value') === 'wake', 'recipient-only handoff kept its priority');

    // Named assertion: empty text or no recipient keeps Send disabled.
    await text.fill('Text but nobody to send it to.');
    assert(await send.isDisabled(), 'Send enabled with no recipient');
    await pick(id('codex'));
    await page.waitForFunction(() => !document.querySelector('.compose button[type=submit]').disabled);
    await text.fill('');
    assert(await send.isDisabled(), 'Send enabled with empty text');
    await text.fill('   \n  ');
    assert(await send.isDisabled(), 'Send enabled with blank text');
    await view.getByRole('button', { name: 'Remove codex-hark' }).click();
    await text.fill('Text again, recipient removed.');
    assert(await send.isDisabled(), 'Send enabled after the last recipient was removed');
    assert(sends.length === 0, 'a disabled form sent something');

    // Search filters by name; Enter picks the first match.
    await search.fill('GROK');
    await view.locator('.opts .opt').first().waitFor();
    const shown = await view.locator('.opts .opt .nm').allTextContents();
    assert(shown.length === 2 && shown.every(n => n.startsWith('grok-')), `search showed ${shown}`);
    await search.fill('nobody-by-this-name');
    await view.locator('.opts .none', { hasText: 'No peer matches' }).waitFor();
    await search.fill('codex');
    await search.press('Enter');
    await view.locator(`.pick[title="${id('codex')}"]`).waitFor();
    assert(await search.inputValue() === '', 'Enter did not clear the search');
    await search.fill('sleuth');
    await search.press('Enter');
    await view.locator(`.pick[title="${id('sleuth')}"]`).waitFor();

    // A full form: title, priority, advanced fields.
    const body = 'Release checklist: tag, notes, deploy.';
    await view.locator('#c-title').fill('Release checklist');
    await view.getByRole('button', { name: 'wake_idle', exact: true }).click();
    await text.fill(body);
    await view.locator('.bytes', { hasText: `${body.length} / 4096 B` }).waitFor();
    await view.locator('summary', { hasText: 'Advanced' }).click();
    await view.locator('#c-history').selectOption('full');
    await view.locator('#c-locked').check();

    // Leaving a typed draft keeps its fields in memory and resumes at Message.
    await page.locator('#nav-dash').click();
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await page.locator('#nav-new').click();
    await opt(id('codex')).waitFor();
    assert(JSON.stringify(await view.locator('.pick').evaluateAll(els => els.map(el => el.title))) === JSON.stringify([id('codex'), id('sleuth')]), 'navigation lost draft recipients');
    assert(await view.locator('#c-title').inputValue() === 'Release checklist', 'navigation lost draft title');
    assert(await text.inputValue() === body, 'navigation lost draft message');
    assert(await text.evaluate(el => el === document.activeElement), 'restored draft did not focus Message');
    assert(await view.locator('.seg button.on').getAttribute('value') === 'wake_idle', 'navigation lost manual priority');
    assert(await view.locator('.bytes').textContent() === `${body.length} / 4096 B`, 'restored counter is stale');
    assert(await send.isEnabled(), 'restored draft cannot send');
    await view.locator('summary', { hasText: 'Advanced' }).click();
    assert(await view.locator('#c-history').inputValue() === 'full' && await view.locator('#c-locked').isChecked(), 'navigation lost advanced fields');

    // Progress: the send is held at the network.
    // lib.mjs hold() unroutes as it releases; a second route keeps Playwright's
    // interception on so the held request is continued by the handler.
    await page.route('**/__keep-interception__', route => route.continue());
    const release = await hold('**/v1/messages');
    await send.click();
    await view.getByRole('button', { name: 'Sending…' }).waitFor();
    assert(await text.isDisabled() && await search.isDisabled(), 'form editable while the send is held');
    await shot('15-compose-progress');
    const answered = page.waitForResponse(res => isSend(res.request()));
    await release();
    const res = await answered;
    assert(res.status() === 202, `send answered ${res.status()}`);
    const sent = await res.json();

    // Named assertion: send opens the new thread.
    await page.waitForFunction(thr => location.hash === `#thread/${thr}`, sent.thread_id);
    await page.locator('.msg .txt', { hasText: body }).waitFor();
    await page.locator('.thead h1', { hasText: 'Release checklist' }).waitFor();
    assert(sends.length === 1, `expected one send, saw ${sends.length}`);
    const req = sends[0];
    assert(JSON.stringify(req.recipients) === JSON.stringify([id('codex'), id('sleuth')]), `recipients were ${req.recipients}`);
    assert(req.recipients.every(r => /^peer_/.test(r)), 'a recipient was sent by name');
    assert(!('thread_id' in req), 'a new message named a thread');
    assert(req.thread_title === 'Release checklist' && req.priority === 'wake_idle' && req.history_access === 'full' && req.locked_participants === true, `body was ${JSON.stringify(req)}`);
    assert(/^ui-[0-9a-f]{32}$/.test(req.idempotency_key), 'no idempotency key');
    const mine = await (await asPeer('operator', 'GET', '/v1/threads?limit=200')).json();
    const summary = (mine.threads || []).find(t => t.thread_id === sent.thread_id);
    assert(summary, 'the new thread is not in the operator\'s list');
    assert(summary.title === 'Release checklist', `hub title ${summary.title}`);
    assert(summary.history_access === 'full' && summary.locked_participants === true, `hub thread ${JSON.stringify(summary)}`);
    for (const key of ['operator', 'codex', 'sleuth']) assert(summary.participants.includes(key === 'operator' ? demo.operator_peer : id(key)), `${key} is not a participant`);
    await shot('15-compose-success');

    // Error: scope_denied names the recipient and keeps the form.
    await page.evaluate(() => { location.hash = '#new'; });
    await opt(id('grok')).waitFor();
    assert(await text.inputValue() === '' && await view.locator('#c-title').inputValue() === '' && await view.locator('.pick').count() === 0, 'a successful send left a kept draft');
    assert(await view.locator('.seg button.on').getAttribute('value') === 'wake', 'a successful send left its priority');
    await pick(id('codex'));
    await pick(id('grok'));
    await view.locator('#c-title').fill('Review the hosting plan');
    const draft = 'Please review the hosting plan before Friday.';
    await text.fill(draft);
    await page.route('**/v1/messages', route => route.fulfill({ status: 403, contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'scope_denied', message: 'send scopes forbid this recipient', details: { recipient: id('grok') } } }) }));
    await send.click();
    await view.locator('.err', { hasText: 'grok-reviewer' }).waitFor();
    await view.locator('.pick.denied', { hasText: 'grok-reviewer' }).waitFor();
    assert(await page.evaluate(() => location.hash) === '#new', 'a rejected send left the form');
    assert(await text.inputValue() === draft, 'the draft was lost');
    assert(await view.locator('#c-title').inputValue() === 'Review the hosting plan', 'the title was lost');
    assert(await view.locator('.pick').count() === 2, 'recipients were lost');
    assert(await send.isEnabled(), 'Send stayed disabled after the rejection');
    await shot('15-compose-error');

    // rate_limited shows the retry time. A definite 403 drops the key; a 429
    // keeps it, so the retry that lands is the same send.
    await page.unroute('**/v1/messages');
    await page.route('**/v1/messages', route => route.fulfill({ status: 429, contentType: 'application/json', headers: { 'Retry-After': '120' },
      body: JSON.stringify({ error: { code: 'rate_limited', message: 'threads rate limit exceeded', details: { limit: 'threads_per_hour', max: 20, retry_after_seconds: 120 } } }) }));
    await send.click();
    await view.locator('.err', { hasText: /Rate limited\. Try again at \d\d:\d\d:\d\d \(in 120 s\)/ }).waitFor();
    await page.unroute('**/v1/messages');
    await send.click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/thr_'));
    await page.locator('.msg .txt', { hasText: draft }).waitFor();
    const [denied, limited, landed] = sends.slice(1);
    assert(sends.length === 4, `expected four sends, saw ${sends.length}`);
    assert(denied.idempotency_key !== limited.idempotency_key, 'a key survived a definite rejection');
    assert(limited.idempotency_key === landed.idempotency_key, 'the key changed across a 429 retry');
    assert(limited.priority === landed.priority && limited.content.text === landed.content.text, 'the 429 retry changed priority or body');

    // Grok recipient defaults (Decisions §6). A fresh composer: non-grokbot
    // keeps wake; any grokbot preselects wake_idle; a manual choice sticks;
    // a failed send retries the same body and priority.
    // The chosen priority is .on for the eye and aria-pressed for assistive
    // technology; every read checks that the two agree.
    const on = async () => {
      await view.locator('.seg button.on').waitFor();
      const v = await view.locator('.seg button').evaluateAll(bs => ({ on: bs.filter(b => b.classList.contains('on')).map(b => b.value), pressed: bs.filter(b => b.getAttribute('aria-pressed') === 'true').map(b => b.value) }));
      assert(v.on.length === 1 && v.on.join() === v.pressed.join(), `priority marks disagree: ${JSON.stringify(v)}`);
      return v.on[0];
    };
    const quota = view.locator('#c-quota');
    const center = sel => view.locator(sel).evaluate(el => el.scrollIntoView({ block: 'center' }));
    const quotaLine = 'Preselected for Grok Bot · each run spends Grok quota';
    const plainLine = 'Grok Bot: each run spends Grok quota';
    const quotaDetail = 'wake_idle pushes now if the Grok Bot is idle or its presence is unknown, otherwise once it goes idle; hold never starts a run. A run that also needs you in chat can start a second one.';
    // Where the form below the Title/Priority row sits, measured from that
    // row, so a chip wrapping in To does not count: picking or removing a
    // Grok Bot must not move it (the note's line is always kept).
    const formTop = () => page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => {
      const row = document.querySelector('.compose .row').getBoundingClientRect().bottom;
      const y = sel => document.querySelector(sel).getBoundingClientRect().top - row;
      r({ text: y('#c-text'), adv: y('.compose .adv'), send: y('.compose .actions') });
    }))));
    const sameTop = (a, b, what) => assert(['text', 'adv', 'send'].every(k => Math.abs(a[k] - b[k]) < 0.5), `${what} moved the form: ${JSON.stringify(a)} -> ${JSON.stringify(b)}`);
    // The note is one line under the control, ending under its right edge,
    // never clipped at this width, and named by the control's description.
    const noteFits = async what => {
      const m = await quota.evaluate(el => {
        const r = el.getBoundingClientRect(), seg = document.querySelector('.compose .seg').getBoundingClientRect();
        const lh = parseFloat(getComputedStyle(el).lineHeight);
        return { h: r.height, lh, sw: el.scrollWidth, cw: el.clientWidth, right: r.right, top: r.top, segRight: seg.right, segBottom: seg.bottom,
          described: document.querySelector('.compose .seg').getAttribute('aria-describedby'), more: document.getElementById('c-quota-more').textContent };
      });
      assert(m.h <= m.lh + 1, `${what}: the note is ${m.h}px tall, more than one ${m.lh}px line`);
      assert(m.sw <= m.cw, `${what}: the note is clipped (${m.sw} > ${m.cw})`);
      assert(Math.abs(m.right - m.segRight) <= 1 && m.top >= m.segBottom, `${what}: the note does not end under the priority control ${JSON.stringify(m)}`);
      assert(m.described === 'c-quota c-quota-more' && m.more === quotaDetail, `${what}: describedby ${m.described}`);
    };
    await page.evaluate(() => { location.hash = '#new'; });
    await opt(id('codex')).waitFor();
    await pick(id('codex'));
    assert(await on() === 'wake', 'a non-grokbot recipient changed the initial priority');
    assert(await quota.isHidden(), 'quota note shows with no Grok Bot');
    assert(await view.locator('.seg').getAttribute('aria-describedby') === null, 'the control names a hidden note');
    await center('.seg');
    const noGrok = await formTop();
    await shot('t10-compose-nongrok');

    await pick(id('grok'));
    assert(await on() === 'wake_idle', 'one grokbot among the recipients did not preselect wake_idle');
    await quota.waitFor();
    const mixedNote = await quota.textContent();
    assert(mixedNote === quotaLine, mixedNote);
    assert(await quota.getAttribute('title') === quotaDetail, await quota.getAttribute('title'));
    assert(await quota.evaluate(el => el.classList.contains('qnote') && !el.closest('.row') && !el.closest('.wrap')), 'quota note is not its own line under the priority row');
    await noteFits('mixed');
    sameTop(noGrok, await formTop(), 'adding a Grok Bot');
    await center('#c-quota');
    await shot('t10-compose-mixed');

    await view.getByRole('button', { name: 'Remove grok-reviewer' }).click();
    assert(await on() === 'wake', 'removing the grokbot left wake_idle selected');
    assert(await quota.isHidden(), 'quota note stayed after the grokbot was removed');
    sameTop(noGrok, await formTop(), 'removing the Grok Bot');
    await shot('t10-compose-removed');

    await view.getByRole('button', { name: 'Remove codex-hark' }).click();
    await pick(id('grok'));
    assert(await on() === 'wake_idle', 'a grokbot-only recipient did not preselect wake_idle');
    await center('#c-quota');
    await shot('t10-compose-grok');

    await view.getByRole('button', { name: 'wake', exact: true }).click();
    assert(await on() === 'wake', 'explicit wake did not stick');
    assert(!/preselected/i.test(await quota.textContent()), `after an explicit wake the note says: ${await quota.textContent()}`);
    assert(await quota.textContent() === plainLine, await quota.textContent());
    await view.getByRole('button', { name: 'Remove grok-reviewer' }).click();
    await pick(id('grok'));
    assert(await on() === 'wake', 'adding the grokbot again overwrote explicit wake');
    assert(await quota.textContent() === plainLine, `explicit wake, Grok Bot added again: ${await quota.textContent()}`);
    await noteFits('explicit wake');
    await page.mouse.move(0, 0);
    await page.evaluate(() => { const el = document.activeElement; if (el && el.blur) el.blur(); });
    await view.locator('.ptip[data-p="wake"]').waitFor({ state: 'hidden' });
    await center('.seg');
    await shot('t10-compose-explicit-wake');

    await view.getByRole('button', { name: 'hold', exact: true }).click();
    assert(await on() === 'hold', 'explicit hold did not stick');
    await view.getByRole('button', { name: 'Remove grok-reviewer' }).click();
    await pick(id('sleuth'));
    await pick(id('grok'));
    assert(await on() === 'hold', 'explicit hold was overwritten when recipients changed');
    assert(await quota.textContent() === plainLine, `after an explicit hold the note says: ${await quota.textContent()}`);
    await page.mouse.move(0, 0);
    await page.evaluate(() => { const el = document.activeElement; if (el && el.blur) el.blur(); });
    await view.locator('.ptip[data-p="hold"]').waitFor({ state: 'hidden' });
    await center('.seg');
    await shot('t10-compose-explicit-hold');

    await view.locator('.seg button[value="wake"]').focus();
    await page.keyboard.press('Tab');
    assert(await page.evaluate(() => document.activeElement && document.activeElement.value) === 'wake_idle', 'Tab did not reach wake_idle');
    await view.locator('.ptip[data-p="wake_idle"]').waitFor({ state: 'visible' });
    await shot('t10-compose-keyboard');
    await page.keyboard.press('Enter');
    assert(await on() === 'wake_idle', 'keyboard activation did not select wake_idle');
    await view.getByRole('button', { name: 'Remove grok-reviewer' }).click();
    assert(await on() === 'wake_idle', 'a keyboard choice was not sticky');
    await pick(id('grok'));

    const retryText = 'Retry this to the Grok Bot without changing priority.';
    await text.fill(retryText);
    const before = sends.length;
    await page.route('**/v1/messages', route => route.fulfill({
      status: 503, contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'unavailable', message: 'try again' } }),
    }));
    await send.click();
    await view.locator('.err', { hasText: 'unavailable: try again' }).waitFor();
    assert(await text.inputValue() === retryText, 'the failed send changed the body');
    assert(await on() === 'wake_idle', 'the failed send changed the priority');
    assert(await quota.textContent() === plainLine, `a keyboard wake_idle is still a manual choice: ${await quota.textContent()}`);
    await center('#c-quota');
    await shot('t10-compose-retry');
    await page.unroute('**/v1/messages');
    await send.click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/thr_'));
    const [firstTry, secondTry] = sends.slice(before);
    assert(firstTry && secondTry, `retry sent ${sends.length - before} times`);
    assert(firstTry.priority === 'wake_idle' && secondTry.priority === 'wake_idle', `priorities ${firstTry && firstTry.priority}, ${secondTry && secondTry.priority}`);
    assert(firstTry.content.text === retryText && secondTry.content.text === retryText, 'retry changed the body');
    assert(firstTry.idempotency_key === secondTry.idempotency_key, 'retry minted a new key');

    // A 503 to a session peer, then swapping in a Grok Bot, is a new draft:
    // wake_idle is chosen before the new key, and the note agrees with the control.
    await page.evaluate(() => { location.hash = '#new'; });
    await opt(id('codex')).waitFor();
    await pick(id('codex'));
    assert(await on() === 'wake', 'a fresh composer did not start on wake');
    const edited = 'Failed to a session peer, then addressed to a Grok Bot.';
    await text.fill(edited);
    const at = sends.length;
    await page.route('**/v1/messages', route => route.fulfill({
      status: 503, contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'unavailable', message: 'try again' } }),
    }));
    await send.click();
    await view.locator('.err', { hasText: 'unavailable: try again' }).waitFor();
    assert(await on() === 'wake', 'the failed session send changed priority');
    assert(await quota.isHidden(), 'quota note showed for a session peer');
    await view.getByRole('button', { name: 'Remove codex-hark' }).click();
    await pick(id('grok'));
    assert(await on() === 'wake_idle', 'swapping in a Grok Bot after the failure left wake selected');
    assert(await quota.isHidden() === false, 'the note did not follow the Grok Bot');
    assert(await quota.textContent() === quotaLine, `the automatic wake_idle note: ${await quota.textContent()}`);
    await page.unroute('**/v1/messages');
    await send.click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/thr_'));
    const [failedSend, swappedSend] = sends.slice(at);
    assert(failedSend && swappedSend, `recipient edit sent ${sends.length - at} times`);
    assert(failedSend.priority === 'wake' && swappedSend.priority === 'wake_idle', `priorities ${failedSend && failedSend.priority}, ${swappedSend && swappedSend.priority}`);
    assert(failedSend.content.text === edited && swappedSend.content.text === edited, 'the edited send changed the body');
    assert(failedSend.idempotency_key !== swappedSend.idempotency_key, 'a changed draft reused the failed key');

    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => { location.hash = '#new'; });
    await opt(id('grok')).waitFor();
    await pick(id('codex'));
    const narrowNoGrok = await formTop();
    await pick(id('grok'));
    await quota.waitFor();
    assert(await quota.textContent() === quotaLine, await quota.textContent());
    await noteFits('narrow');
    sameTop(narrowNoGrok, await formTop(), 'adding a Grok Bot at 390');
    await view.getByRole('button', { name: 'Remove codex-hark' }).click();
    await center('#c-quota');
    const narrowWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    assert(narrowWidth <= 390, `compose scrolls sideways at 390 (${narrowWidth})`);
    await shot('t10-compose-narrow');
    await view.locator('.seg button[value="wake"]').focus();
    await page.keyboard.press('Tab');
    const narrowTip = view.locator('.ptip[data-p="wake_idle"]');
    await narrowTip.waitFor({ state: 'visible' });
    const tipBox = await narrowTip.boundingBox();
    assert(tipBox && tipBox.x >= 0 && tipBox.x + tipBox.width <= 390, `narrow tip off screen ${JSON.stringify(tipBox)}`);
    await shot('t10-compose-keyboard-narrow');
    await page.setViewportSize({ width: 1440, height: 900 });

    // Automatic Grok priority and its note survive a round trip too.
    await text.fill('Resume the automatic Grok draft.');
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await page.locator('#nav-new').click();
    await opt(id('grok')).waitFor();
    assert(await on() === 'wake_idle' && await quota.textContent() === quotaLine, 'restoration made automatic priority manual');
    assert(await text.evaluate(el => el === document.activeElement), 'automatic draft did not focus Message');

    // A recipient that disappeared from the directory cannot be restored.
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await page.route('**/v1/peers', async route => {
      const res = await route.fetch(), body = await res.json();
      body.peers = body.peers.filter(p => p.peer_id !== id('grok'));
      await route.fulfill({ response: res, json: body });
    });
    const updated = page.waitForResponse(r => new URL(r.url()).pathname === '/v1/peers');
    await page.evaluate(() => window.refreshDirectory());
    await updated;
    await ctx.quiet();
    await page.locator('#nav-new').click();
    await opt(id('codex')).waitFor();
    assert(await view.locator('.pick').count() === 0, 'restored an ineligible recipient');
    assert(await text.inputValue() === 'Resume the automatic Grok draft.', 'directory filtering lost the message');
    assert(await on() === 'wake' && await quota.isHidden() && await send.isDisabled(), 'restored gate or Grok note is stale');
    await page.unroute('**/v1/peers');

    // The sign-out hook can forget a kept draft without any browser storage.
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await page.evaluate(async () => { (await import('./compose.js')).forgetDrafts(); });
    await page.locator('#nav-new').click();
    await opt(id('codex')).waitFor();
    assert(await text.inputValue() === '' && await view.locator('.pick').count() === 0, 'forgetDrafts kept typed work');

    // A lost reply may already be sent: navigation must keep its retry key.
    allowConsole(/net::ERR_FAILED/);
    await pick(id('codex'));
    await text.fill('Lost reply across navigation must not send twice.');
    const replies = [];
    await page.route('**/v1/messages', async route => {
      const response = await route.fetch();
      replies.push({ status: response.status(), body: await response.json() });
      if (replies.length === 1) await route.abort('failed');
      else await route.fulfill({ response });
    });
    const beforeRetry = sends.length;
    await send.click();
    await view.locator('.err', { hasText: 'network' }).waitFor();
    await page.locator('#nav-dash').click();
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await page.locator('#nav-new').click();
    await opt(id('codex')).waitFor();
    await send.click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/thr_'));
    await page.unroute('**/v1/messages');
    const retrySends = sends.slice(beforeRetry);
    assert(retrySends.length === 2 && retrySends[0].idempotency_key === retrySends[1].idempotency_key, 'navigation lost the pending key and sent twice');
    assert(replies.length === 2 && replies[0].status === 202 && replies[1].status === 200 && replies[0].body.message_id === replies[1].body.message_id, 'restored retry created another message');
  });
  console.log('Compose passed: eligible peers, Send gating, search, ids not names, held send, new thread opens, scope_denied and rate_limited keep the form, stable key, grokbot wake_idle preselect stays sticky and retries the same priority, in-memory drafts restore fields and focus, handoff replaces and sends clear drafts, removed recipients and forgetDrafts clear safely, lost replies replay once across navigation');
} finally { await cleanBuild(); }
