// Task 9: the thread view — reply links, spilled text, receipts, inline
// approvals, the composer's idempotency key and budget banner, in a real browser.
import { assert, cleanBuild, withDemo } from './lib.mjs';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, allowConsole, countRequests } = ctx;
    const hark = demo.threads.hark, quota = demo.threads.quota;
    // Chromium logs every non-2xx fetch and every aborted request as a console
    // error. These are the ones this script provokes on purpose.
    allowConsole(/status of 422/);
    allowConsole(/net::ERR_FAILED/);
    allowConsole(/status of 501/);
    let dialogs = 0;
    page.on('dialog', d => { dialogs++; d.dismiss().catch(() => {}); });

    const api = async (method, path, body, as = 'operator') => {
      const r = await asPeer(as, method, path, body);
      assert(r.ok, `${as} ${method} ${path}: ${r.status}`);
      return r.status === 204 ? null : r.json();
    };
    const unreadIn = async thread => (await api('GET', '/v1/admin/inbox?unread=1&limit=200')).deliveries.filter(d => d.thread_id === thread).length;
    const isRead = req => req.method() === 'POST' && req.url().endsWith(`/v1/admin/threads/${hark}/read`);
    const msgEl = id => page.locator(`[id="m-${id}"]`);
    // Every resource notify replaces the .msg nodes, so measure the live node
    // by id, and only once two frames apart agree: a detached node measures as
    // all zeros and would read as on screen.
    const inView = async id => {
      const seen = await page.evaluate(async id => {
        const at = () => {
          const el = document.getElementById('m-' + id);
          if (!el || !el.isConnected) return null;
          const r = el.getBoundingClientRect(), m = document.getElementById('main').getBoundingClientRect();
          return r.height > 0 && r.top >= m.top - 1 && r.top < m.bottom;
        };
        const frame = () => new Promise(resolve => requestAnimationFrame(resolve));
        for (let i = 0; i < 600; i++) {
          const a = at();
          await frame(); await frame();
          if (a !== null && a === at()) return a;
        }
        return null;
      }, id);
      assert(seen !== null, `message ${id} never settled in the DOM`);
      return seen;
    };

    await page.locator('#threads a', { hasText: 'hark B196' }).waitFor();
    await page.locator('#conn.live').waitFor();

    // opening a thread clears its unread count via one POST
    const before = await unreadIn(hark);
    assert(before > 0, 'the seeded hark thread has no unread deliveries');
    const totalBefore = (await api('GET', '/v1/admin/inbox?limit=1')).unread;
    await page.locator('#inbox-count', { hasText: String(totalBefore) }).waitFor();
    const readDone = page.waitForResponse(r => isRead(r.request()));
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, hark);
    const readReply = await readDone;
    assert(readReply.ok(), `read POST answered ${readReply.status()}`);
    // marked also counts the open approval's delivery, which the inbox lists apart.
    assert((await readReply.json()).marked >= before, 'read POST did not mark every unread hark delivery');
    assert(await unreadIn(hark) === 0, 'hark still has unread deliveries after opening it');
    await page.locator('#inbox-count', { hasText: totalBefore - before ? String(totalBefore - before) : /^$/ }).waitFor();

    // The view as a-chat.png shows it: history to the bottom, receipts, the open approval.
    const approval = page.locator('.apr.act', { hasText: 'Push classifier fix to hark master and release?' });
    await approval.locator('.cd', { hasText: 'expires in' }).waitFor();
    for (const name of ['yes', 'no', 'yes but no release']) await approval.getByRole('button', { name, exact: true }).waitFor();
    await page.locator('.msg .rc span').first().waitFor();
    await page.locator('.sys', { hasText: 'approval expired' }).waitFor();
    await page.locator('.msg .att.spill', { hasText: 'message.md' }).first().waitFor();
    assert(await page.locator('.apr.shut', { hasText: 'Run the reproduction?' }).count() === 1, 'answered approval is not read-only');
    assert(await page.locator('.apr.shut', { hasText: 'An earlier review window' }).locator('.st', { hasText: 'expired' }).count() === 1, 'expired approval does not read expired');
    // The composer and the column fill the space up to the 980 px measure.
    const col = await page.locator('#view').boundingBox();
    assert(col.width > 760, `reading column is only ${col.width}px`);
    assert(countRequests(isRead) === 1, `opening the thread sent ${countRequests(isRead)} read POSTs`);
    await shot('09-thread-initial');

    // live delivery from asPeer appears without reload
    await page.evaluate(() => { window.__noReload = true; });
    const first = (await api('GET', `/v1/threads/${hark}/messages?limit=1`)).messages[0];
    const live = 'Found it: the finalize call is skipped after over.';
    const readAgain = page.waitForResponse(r => isRead(r.request()) && r.ok());
    const sentLive = await api('POST', '/v1/messages', { idempotency_key: 'thread-live', thread_id: hark, in_reply_to: first.message_id, priority: 'wake', content: { text: live } }, 'sleuth');
    await page.locator('.msg .txt', { hasText: live }).waitFor();
    assert(await page.evaluate(() => window.__noReload === true), 'the page reloaded to show the delivery');
    // It is a new delivery while the tab is visible, so the thread is read again.
    await readAgain;
    assert(countRequests(isRead) === 2, `the live delivery led to ${countRequests(isRead) - 1} read POSTs`);
    assert(await unreadIn(hark) === 0, 'the live delivery stayed unread while the thread was open');

    // reply link jumps to parent
    assert(!(await inView(first.message_id)), 'the first message was already on screen');
    // Hold back the jump's scroll event so a hub event re-renders the thread
    // first: the jump must survive that render, not snap back to the bottom.
    await page.evaluate(() => { window.__holdScroll = e => e.stopImmediatePropagation(); window.addEventListener('scroll', window.__holdScroll, true); });
    await msgEl(sentLive.message_id).locator('.reply').click();
    await page.locator(`[id="m-${first.message_id}"].flash`).waitFor();
    const aside = 'Checking the finalize path now.';
    await api('POST', '/v1/messages', { idempotency_key: 'thread-rerender', thread_id: hark, priority: 'hold', content: { text: aside } }, 'codex');
    await page.locator('.msg .txt', { hasText: aside }).waitFor();
    assert(await inView(first.message_id), 'the reply link did not bring its parent into view');
    await page.evaluate(() => window.removeEventListener('scroll', window.__holdScroll, true));

    // Show full text renders the spilled body literally
    const hostileLong = Array.from({ length: 160 }, (_, i) => `<img src=x onerror=alert(${i})> line ${i}`).join('\n');
    const spilled = await api('POST', '/v1/messages', { idempotency_key: 'thread-spill', thread_id: hark, priority: 'hold', content: { text: hostileLong } }, 'codex');
    assert(spilled.spilled === true, 'the long body did not spill');
    const spillMsg = msgEl(spilled.message_id);
    await spillMsg.locator('.att.spill', { hasText: 'message.md' }).waitFor();
    await spillMsg.getByRole('button', { name: 'Show full text' }).click();
    const pre = spillMsg.locator('pre.full');
    await pre.waitFor();
    assert(await pre.textContent() === hostileLong, 'the full text is not the spilled body, byte for byte');
    assert(await pre.evaluate(el => el.children.length) === 0, 'the full text became elements');
    assert(await page.locator('img').count() === 0, 'a spilled body became an image');
    assert(await spillMsg.locator('.txt').count() === 0, 'the preview still shows beside the full text');
    await spillMsg.getByRole('button', { name: 'Hide full text' }).click();
    await pre.waitFor({ state: 'detached' });

    // send keeps its idempotency key across a held retry: the first attempt
    // reaches the hub, but its reply is lost on the way back.
    const posts = [];
    page.on('request', req => { if (req.method() === 'POST' && req.url().endsWith('/v1/messages')) posts.push(req.postDataJSON()); });
    let release, attempt = 0;
    const gate = new Promise(resolve => { release = resolve; });
    await page.route('**/v1/messages', async route => {
      if (attempt++ === 0) {
        await gate;
        await route.fetch();
        await route.abort('failed');
      } else await route.continue();
    });
    const reply = 'Release notes drafted; tagging after review.';
    const ta = page.locator('.composer textarea');
    await page.locator('.composer .to button').click();
    await ta.fill(reply);
    await ta.press('Enter');
    await page.locator('.composer .send:disabled').waitFor();
    await ta.press('Enter'); // a second press while held sends nothing
    await shot('09-thread-progress');
    release();
    await page.locator('.composer .cbanner', { hasText: 'network:' }).waitFor();
    assert(await ta.inputValue() === reply, 'the text was lost when the send failed');
    assert(await page.locator('.composer .send:disabled').count() === 0, 'Send stayed disabled after the failure');
    // The first attempt did land: wait for its message (and the reload that
    // re-picks the reply target) before retrying, so the same-key assertion
    // below pins the kept target every run, not only when the event is early.
    await page.locator('.msg .txt', { hasText: reply }).waitFor();
    await Promise.all([page.locator('#toast.on', { hasText: 'Sent' }).waitFor(), page.locator('.composer .send').click()]);
    await page.locator('.msg .txt', { hasText: reply }).waitFor();
    await page.unroute('**/v1/messages');
    assert(posts.length === 2, `the held send went out ${posts.length} times`);
    assert(posts[0].idempotency_key === posts[1].idempotency_key, 'the retry minted a new idempotency key');
    const after = (await api('GET', `/v1/threads/${hark}/messages?limit=200`)).messages.filter(m => m.content.text === reply);
    assert(after.length === 1, `the retried send landed ${after.length} times`);
    assert(await ta.inputValue() === '', 'the composer kept the sent text');

    // Receipt markers move when the recipients report.
    const row = (await api('GET', `/v1/admin/threads/${hark}/deliveries?limit=200`)).rows.find(r => r.message_id === after[0].message_id);
    assert(row, 'no matrix row for the sent message');
    for (const key of ['codex', 'sleuth']) {
      const cell = row.cells.find(c => c.recipient === demo.peers[key].id);
      assert(cell, `no delivery to ${key}`);
      await api('POST', '/v1/receipts', { delivery_id: cell.delivery_id, state: 'processed' }, key);
    }
    const sentMsg = msgEl(after[0].message_id);
    await sentMsg.locator('.rc .st-processed', { hasText: 'codex-hark processed' }).waitFor();
    await sentMsg.locator('.rc .st-processed', { hasText: 'pag-hark-sleuth processed' }).waitFor();
    assert(await sentMsg.locator('.rc span', { hasText: 'max' }).count() === 0, 'the sender has a receipt marker on its own message');
    await page.locator('#toast.on').waitFor({ state: 'hidden' });
    await page.evaluate(() => Promise.all(document.getElementById('toast').getAnimations().map(a => a.finished)));
    await shot('09-thread-success');

    // The quota thread's budget is exhausted: a banner names it, the text stays.
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, quota);
    await page.locator('.msg .txt', { hasText: 'exhausted its message budget' }).waitFor();
    const ask = 'Can we raise the budget on this one?';
    await ta.fill(ask);
    await ta.press('Enter');
    const banner = page.locator('.composer .cbanner.budget');
    await banner.waitFor();
    assert(/budget/i.test(await banner.textContent()), 'the banner does not name the budget');
    assert(await ta.inputValue() === ask, 'the text was lost on thread_budget_exhausted');
    assert(dialogs === 0, 'a body opened a dialog');
    await shot('09-thread-error');

    // An open approval is answered inline with one POST (Task 8 rules).
    const hosting = demo.threads.hosting;
    const answersTo = id => posts.filter(p => p.approval_response && p.approval_response.approval_id === id).length;
    const answersIn = thread => posts.filter(p => p.approval_response && p.thread_id === thread).length;
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, hosting);
    const plan = page.locator('.apr', { hasText: 'Proceed with the hosting plan?' });
    await plan.getByRole('button', { name: 'yes', exact: true }).waitFor();
    assert(await page.locator('.composer .join').isHidden(), 'the join notice shows in a thread the operator is in');
    // A local gate rather than ctx.hold: here hold()'s unroute raced its own
    // handler's continue and failed with 'Route is already handled'.
    let releaseAnswer;
    const answerGate = new Promise(resolve => { releaseAnswer = resolve; });
    await page.route('**/v1/messages', async route => { await answerGate; await route.continue(); });
    await plan.getByRole('button', { name: 'yes', exact: true }).click();
    await plan.locator('button.opt:disabled', { hasText: 'no' }).waitFor();
    releaseAnswer();
    await plan.locator('.st', { hasText: 'answered: yes' }).waitFor();
    await page.unroute('**/v1/messages');
    assert(answersIn(hosting) === 1, `answering inline sent ${answersIn(hosting)} responses`);

    // An approval closed elsewhere answers 422 approval_closed: shown closed, not retried.
    const closedId = 'apr_01JC00000000000000000000ZZ';
    const rotate = 'Rotate the backup key tonight?';
    await api('POST', '/v1/messages', { idempotency_key: 'thread-apr-closed', thread_id: hosting, kind: 'approval_request', priority: 'wake', recipients: [demo.operator_peer], content: { text: rotate },
      approval_request: { approval_id: closedId, question: rotate, options: ['yes', 'no'], expires_at: new Date(Date.now() + 30 * 60000).toISOString() } }, 'codex');
    const card = page.locator('.apr', { hasText: rotate });
    await card.getByRole('button', { name: 'yes', exact: true }).waitFor();
    await page.route('**/v1/messages', async route => {
      await api('POST', '/v1/messages', { idempotency_key: 'thread-apr-elsewhere', thread_id: hosting, kind: 'approval_response', priority: 'wake', content: { text: 'no' }, approval_response: { approval_id: closedId, decision: 'no' } });
      await route.continue();
    }, { times: 1 });
    await card.getByRole('button', { name: 'yes', exact: true }).click();
    await page.locator('#toast.on', { hasText: 'Already closed' }).waitFor();
    await card.locator('.st', { hasText: /^closed$/ }).waitFor();
    assert(answersTo(closedId) === 1, `the closed approval was sent ${answersTo(closedId)} times`);
    await page.unroute('**/v1/messages');

    // A thread the operator is not in says that sending adds them.
    const side = await api('POST', '/v1/messages', { idempotency_key: 'thread-side', recipients: [demo.peers.sleuth.id], thread_title: 'Side channel', priority: 'hold', content: { text: 'Sleuth, a quick side question.' } }, 'codex');
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, side.thread_id);
    await page.locator('.msg .txt', { hasText: 'a quick side question' }).waitFor();
    await page.locator('.composer .join', { hasText: 'Sending adds you' }).waitFor();
  });
  console.log('Thread passed: read POST, receipts, live delivery, reply jump, literal full text, held retry key, budget banner, inline approvals, join notice');
} finally { await cleanBuild(); }
