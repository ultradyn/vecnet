// Task 15: start a new thread from #new, in a real browser.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const isSend = req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/messages';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, allowConsole } = ctx;
    // Chromium logs every non-2xx fetch as a console error: the two rejections
    // this script stages, and the views other tasks have yet to fill in.
    allowConsole(/status of 403/);
    allowConsole(/status of 429/);
    allowConsole(/status of 501/);
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
  });
  console.log('Compose passed: eligible peers, Send gating, search, ids not names, held send, new thread opens, scope_denied and rate_limited keep the form, stable key');
} finally { await cleanBuild(); }
