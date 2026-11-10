// Task 9: the thread view — reply links, spilled text, receipts, inline
// approvals, the composer's idempotency key and budget banner, in a real browser.
// Reading is per message (CoS unread ruling; thread-unread.mjs covers it in
// full): the thread opens at its first unread under New and each message is
// read by its own receipt, never by marking the whole thread.
import { assert, cleanBuild, readThrough, withDemo } from './lib.mjs';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, allowConsole, countRequests, quiet, ignore } = ctx;
    const hark = demo.threads.hark, quota = demo.threads.quota;
    await page.addInitScript(() => {
      const set = window.setInterval;
      window.__directoryRefresh = [];
      window.setInterval = (fn, ms, ...args) => {
        if (ms === 60000) window.__directoryRefresh.push(() => fn(...args));
        return set(fn, ms, ...args);
      };
    });
    await page.reload();
    const keepsFocus = async control => {
      await quiet();
      await control.focus();
      await page.evaluate(() => {
        if (!window.__directoryRefresh.length) throw new Error('directory timer not captured');
        window.__beforeRefresh = document.activeElement;
        window.__directoryRefresh.forEach(fn => fn());
      });
      await quiet();
      assert(await page.evaluate(() => !window.__beforeRefresh.isConnected), 'directory refresh did not rebuild messages');
      assert(await control.evaluate(el => document.activeElement === el), 'directory refresh lost focus on ' + await control.textContent());
    };
    // Chromium logs every non-2xx fetch and every aborted request as a console
    // error. These are the ones this script provokes on purpose.
    allowConsole(/status of 422/);
    allowConsole(/net::ERR_FAILED/);
    allowConsole(/status of 501/);
    allowConsole(/status of 503/);
    let dialogs = 0;
    page.on('dialog', d => { dialogs++; d.dismiss().catch(() => {}); });

    const api = async (method, path, body, as = 'operator') => {
      const r = await asPeer(as, method, path, body);
      assert(r.ok, `${as} ${method} ${path}: ${r.status}`);
      return r.status === 204 ? null : r.json();
    };
    const unreadIn = async thread => (await api('GET', '/v1/admin/inbox?unread=1&limit=200')).deliveries.filter(d => d.thread_id === thread).length;
    // isRead is the retired mark-all route: the view must never post it.
    const isRead = req => req.method() === 'POST' && /\/v1\/admin\/threads\/[^/]+\/read$/.test(new URL(req.url()).pathname);
    const isReceipt = req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/receipts';
    const msgEl = id => page.locator(`[id="m-${id}"]`);
    const comp = page.locator('.composer');
    // The chosen priority is .on for the eye and aria-pressed for assistive
    // technology; every read checks that the two agree.
    const selected = async () => {
      await comp.locator('.seg button.on').waitFor();
      const v = await comp.locator('.seg button').evaluateAll(bs => ({ on: bs.filter(b => b.classList.contains('on')).map(b => b.value), pressed: bs.filter(b => b.getAttribute('aria-pressed') === 'true').map(b => b.value) }));
      assert(v.on.length === 1 && v.on.join() === v.pressed.join(), `priority marks disagree: ${JSON.stringify(v)}`);
      return v.on[0];
    };
    const note = comp.locator('#r-quota');
    const quotaLine = 'Preselected for Grok Bot · each run spends Grok quota';
    const plainLine = 'Grok Bot: each run spends Grok quota';
    const quotaDetail = 'wake_idle pushes now if the Grok Bot is idle or its presence is unknown, otherwise once it goes idle; hold never starts a run. A run that also needs you in chat can start a second one.';
    // The note is its own line under the controls: one line, never clipped,
    // left-aligned under the priority control and below every bar control,
    // and the composer grows by that line only. hint: the keyboard hint is
    // still shown beside it (fitHint may drop it only where it never fits).
    const noteFits = async (what, { hint = false } = {}) => {
      const m = await note.evaluate(el => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => {
        const box = e => e.getBoundingClientRect();
        const bar = el.closest('.wrap').querySelector('.bar'), seg = bar.querySelector('.seg'), c = el.closest('.composer');
        const kids = [...bar.children].filter(k => getComputedStyle(k).display !== 'none').map(box);
        const n = box(el), cs = getComputedStyle(el), withNote = box(c).height;
        el.hidden = true;
        const without = box(c).height;
        el.hidden = false;
        const hintEl = bar.querySelector('.hint');
        r({
          h: n.height, lh: parseFloat(cs.lineHeight), mt: parseFloat(cs.marginTop), sw: el.scrollWidth, cw: el.clientWidth,
          left: n.left, top: n.top, segLeft: box(seg).left, barBottom: Math.max(...kids.map(k => k.bottom)), inBar: !!el.closest('.bar'),
          grow: withNote - without, hint: getComputedStyle(hintEl).display !== 'none' && box(hintEl).width > 0,
          described: seg.getAttribute('aria-describedby'), more: (document.getElementById('r-quota-more') || {}).textContent,
          docW: document.documentElement.scrollWidth, vw: innerWidth,
        });
      }))));
      assert(!m.inBar, `${what}: the note is inside the controls row`);
      assert(m.h <= m.lh + 1, `${what}: the note is ${m.h}px tall, more than one ${m.lh}px line`);
      assert(m.sw <= m.cw, `${what}: the note is clipped (${m.sw} > ${m.cw})`);
      assert(Math.abs(m.left - m.segLeft) <= 1, `${what}: the note is not under the priority control (${m.left} vs ${m.segLeft})`);
      assert(m.top >= m.barBottom - 0.5, `${what}: the note overlaps the controls (${m.top} < ${m.barBottom})`);
      assert(m.grow <= m.lh + m.mt + 1, `${what}: the note grew the composer by ${m.grow}px`);
      assert(m.described === 'r-quota r-quota-more' && m.more === quotaDetail, `${what}: describedby ${m.described}`);
      assert(m.docW <= m.vw, `${what}: scrolls sideways (${m.docW} > ${m.vw})`);
      if (hint) assert(m.hint, `${what}: the keyboard hint is hidden while the note shows`);
      return m;
    };
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

    // Opening lands on the first unread under New; reading the thread through
    // clears its count with one read receipt per unread message.
    const before = await unreadIn(hark);
    assert(before > 0, 'the seeded hark thread has no unread deliveries');
    const totalBefore = (await api('GET', '/v1/admin/inbox?limit=1')).unread;
    await page.locator('#inbox-count', { hasText: String(totalBefore) }).waitFor();
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, hark);
    await page.locator('#view .newdiv').waitFor();
    await readThrough(page, () => unreadIn(hark));
    await page.locator('#inbox-count', { hasText: totalBefore - before ? String(totalBefore - before) : /^$/ }).waitFor();
    // One receipt per message the inbox called unread: the view and the inbox agree.
    assert(countRequests(isReceipt) === before, `reading hark sent ${countRequests(isReceipt)} receipts for ${before} unread`);

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
    assert(countRequests(isRead) === 0, `opening the thread sent ${countRequests(isRead)} mark-all POSTs`);
    // hark includes grok-reviewer, so the reply composer preselects wake_idle.
    assert(await selected() === 'wake_idle', 'a thread with a grokbot participant did not preselect wake_idle');
    await note.waitFor();
    const harkNote = await note.textContent();
    assert(harkNote === quotaLine, harkNote);
    assert(await note.getAttribute('title') === quotaDetail, await note.getAttribute('title'));
    assert(await note.evaluate(el => el.classList.contains('qnote') && el.parentElement.classList.contains('wrap') && el.previousElementSibling.classList.contains('bar')), 'quota note is not the line under the controls');
    await noteFits('hark', { hint: true });
    await quiet();
    await shot('09-thread-initial');
    // Directory polling must not discard a keyboard user's message action.
    await keepsFocus(page.locator('.msg .acts button').first());
    await keepsFocus(approval.getByRole('button', { name: 'no', exact: true }));

    // live delivery from asPeer appears without reload
    await page.evaluate(() => { window.__noReload = true; });
    const first = (await api('GET', `/v1/threads/${hark}/messages?limit=1`)).messages[0];
    const live = 'Found it: the finalize call is skipped after over.';
    let liveDly = null;
    const readAgain = page.waitForResponse(r => isReceipt(r.request()) && r.ok() && r.request().postDataJSON().delivery_id === liveDly);
    const sentLive = await api('POST', '/v1/messages', { idempotency_key: 'thread-live', thread_id: hark, in_reply_to: first.message_id, priority: 'wake', content: { text: live } }, 'sleuth');
    liveDly = sentLive.deliveries.find(d => d.recipient === demo.operator_peer).delivery_id;
    await page.locator('.msg .txt', { hasText: live }).waitFor();
    assert(await page.evaluate(() => window.__noReload === true), 'the page reloaded to show the delivery');
    // It lands at the bottom the operator is watching, so it is read once seen.
    await readAgain;
    assert(countRequests(isRead) === 0, `the live delivery led to ${countRequests(isRead)} mark-all POSTs`);
    for (const deadline = Date.now() + 15000; await unreadIn(hark) !== 0; await page.waitForTimeout(100)) {
      assert(Date.now() < deadline, 'the live delivery stayed unread while the thread was open');
    }

    // Live messages follow the actual scroll owner: main on desktop, the
    // document on phones, whether or not the composer is sticky.
    await quiet();
    assert(await page.locator('#main').evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight <= 1), 'desktop main did not follow the live delivery to the bottom');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(async () => {
      document.scrollingElement.scrollTop = document.scrollingElement.scrollHeight;
      await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)));
    });
    await quiet();
    const phoneLive = await api('POST', '/v1/messages', { idempotency_key: 'thread-phone-live', thread_id: hark, priority: 'hold', content: { text: 'Phone follow: finalize now runs after over.' } }, 'sleuth');
    await msgEl(phoneLive.message_id).waitFor();
    await quiet();
    const phoneFollow = await msgEl(phoneLive.message_id).evaluate(el => {
      const doc = document.scrollingElement;
      return { gap: doc.scrollHeight - doc.scrollTop - doc.clientHeight, max: doc.scrollHeight - doc.clientHeight, top: el.getBoundingClientRect().top, vh: innerHeight };
    });
    assert(phoneFollow.max > 0 && phoneFollow.gap <= 1, `phone did not follow the live delivery to the page bottom: ${JSON.stringify(phoneFollow)}`);
    assert(phoneFollow.top >= 0 && phoneFollow.top < phoneFollow.vh, `phone live message is below the viewport: ${JSON.stringify(phoneFollow)}`);
    await shot('t10-thread-phone-follow');
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.locator('#main').evaluate(el => { el.scrollTop = el.scrollHeight; });
    await quiet();

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

    // Show full text renders the spilled body as markdown: its markup stays text
    const hostileLong = Array.from({ length: 160 }, (_, i) => `<img src=x onerror=alert(${i})> line ${i}`).join('\n');
    const spilled = await api('POST', '/v1/messages', { idempotency_key: 'thread-spill', thread_id: hark, priority: 'hold', content: { text: hostileLong } }, 'codex');
    assert(spilled.spilled === true, 'the long body did not spill');
    const spillMsg = msgEl(spilled.message_id);
    await spillMsg.locator('.att.spill', { hasText: 'message.md' }).waitFor();
    await spillMsg.getByRole('button', { name: 'Show full text' }).click();
    const pre = spillMsg.locator('div.full > .md');
    await pre.waitFor();
    // One paragraph whose lines are hard breaks: the text, line for line, and
    // no element but p and br.
    assert(await pre.evaluate(el => [...el.querySelectorAll('p')].map(p => [...p.childNodes].map(n => n.nodeName === 'BR' ? '\n' : n.textContent).join('')).join('\n')) === hostileLong, 'the full text is not the spilled body, line for line');
    assert(await pre.evaluate(el => el.querySelectorAll(':not(p):not(br)').length) === 0, 'the full text became elements other than paragraphs and breaks');
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
    const unheld = ignore(req => req.method() === 'POST' && req.url().endsWith('/v1/messages'));
    await quiet();
    unheld();
    await shot('09-thread-progress');
    release();
    await page.locator('.composer .cbanner', { hasText: 'network:' }).waitFor();
    assert(await ta.inputValue() === reply, 'the text was lost when the send failed');
    assert(await selected() === 'wake_idle', 'the failed send changed priority');
    await comp.locator('.seg').evaluate(el => el.scrollIntoView({ block: 'center' }));
    await shot('t10-thread-retry');
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
    assert(posts[0].priority === 'wake_idle' && posts[1].priority === 'wake_idle', `retry priorities ${posts.map(p => p.priority)}`);
    assert(posts[0].content.text === reply && posts[1].content.text === reply, 'the retry changed the body');
    const after = (await api('GET', `/v1/threads/${hark}/messages?limit=200`)).messages.filter(m => m.content.text === reply);
    assert(after.length === 1, `the retried send landed ${after.length} times`);
    assert(await ta.inputValue() === '', 'the composer kept the sent text');

    // A new draft typed while a successful send is pending is not its payload.
    const nextDraft = 'Second draft typed while the first is sending.';
    const firstDraft = 'First held successful send.';
    let releaseNext;
    const nextGate = new Promise(resolve => { releaseNext = resolve; });
    await page.route('**/v1/messages', async route => { await nextGate; await route.continue(); });
    await ta.fill(firstDraft);
    await ta.press('Enter');
    await comp.locator('.send:disabled').waitFor();
    await ta.fill(nextDraft);
    // Also select a new reply target while the previous draft is in flight.
    await msgEl(first.message_id).locator('.acts button', { hasText: 'reply' }).click();
    releaseNext();
    await comp.locator('.send:enabled').waitFor();
    await page.locator('.msg .txt', { hasText: firstDraft }).waitFor();
    await quiet();
    assert(await ta.inputValue() === nextDraft, 'successful send erased the next draft');
    assert(await comp.locator('.to').isVisible(), 'successful send erased the next reply target');
    assert(await comp.locator('.bytes').textContent() === nextDraft.length + ' / 4096 B', 'byte count lost the next draft');
    const heldMessages = () => api('GET', `/v1/threads/${hark}/messages?limit=200`);
    assert((await heldMessages()).messages.filter(m => m.content.text === firstDraft).length === 1, 'first held message did not land exactly once');
    await page.unroute('**/v1/messages');
    await ta.press('Enter');
    await comp.locator('.send:enabled').waitFor();
    await page.locator('.msg .txt', { hasText: nextDraft }).waitFor();
    const nextMessages = (await heldMessages()).messages.filter(m => m.content.text === nextDraft);
    assert(nextMessages.length === 1 && nextMessages[0].in_reply_to === first.message_id, 'second draft did not send once to its selected target');
    assert(await ta.inputValue() === '', 'unchanged second draft did not clear');

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
    await quiet();
    await shot('09-thread-success');

    // The quota thread's budget is exhausted: a banner names it, the text stays.
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, quota);
    await page.locator('.msg .txt', { hasText: 'exhausted its message budget' }).waitFor();
    // quota's sender is a grokbot, so the reply preselects wake_idle here too.
    assert(await selected() === 'wake_idle', 'the quota thread, which a grokbot is in, kept wake');
    await note.waitFor();
    // Its one message is on screen, so it is read a second later: the receipt
    // moves from accepted to read once the receipt lands and the matrix reloads.
    await page.locator('.msg .rc .st-read', { hasText: 'max read' }).waitFor();
    // The snapshot already says the budget is spent: the banner shows before a send.
    await page.locator('.composer .cbanner.budget', { hasText: 'you raise the budget' }).waitFor();
    const ask = 'Can we raise the budget on this one?';
    await ta.fill(ask);
    // The banner is already up, so it proves nothing about the send: wait for
    // the hub's refusal and Send to come back.
    const refused = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/v1/messages'));
    await ta.press('Enter');
    assert(!(await refused).ok(), 'the send into a spent thread was not refused');
    const banner = page.locator('.composer .cbanner.budget');
    await banner.waitFor();
    await page.locator('.composer .send:enabled').waitFor();
    assert(/budget/i.test(await banner.textContent()), 'the banner does not name the budget');
    assert(await ta.inputValue() === ask, 'the text was lost on thread_budget_exhausted');
    assert(dialogs === 0, 'a body opened a dialog');
    await quiet();
    await shot('09-thread-error');

    // An open approval is answered inline with one POST (Task 8 rules).
    const hosting = demo.threads.hosting;
    const answersTo = id => posts.filter(p => p.approval_response && p.approval_response.approval_id === id).length;
    const answersIn = thread => posts.filter(p => p.approval_response && p.thread_id === thread).length;
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, hosting);
    await page.locator('.msg .txt', { hasText: 'Scope the hub hosting cost' }).waitFor();
    assert(await selected() === 'wake', 'a thread with no grokbot changed the initial priority');
    assert(await note.isHidden(), 'quota note shows with no Grok Bot');
    await comp.locator('.seg').evaluate(el => el.scrollIntoView({ block: 'center' }));
    await shot('t10-thread-nongrok');
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
    assert(await selected() === 'wake', 'the side channel, with no grokbot, preselected wake_idle');
    await page.locator('#toast.on').waitFor({ state: 'hidden' });

    // Participants arrive after the composer has painted: until then the
    // reply keeps wake. An explicit hold made in that gap stays hold when
    // the grokbot participant becomes known. A fresh open of the setup
    // thread then preselects wake_idle.
    let releaseFacts;
    const factsGate = new Promise(resolve => { releaseFacts = resolve; });
    const stall = async route => { await factsGate; await route.continue(); };
    await page.route('**/v1/threads?*', stall);
    await page.route('**/v1/admin/threads?*', stall);
    const setupBody = 'Setup: this thread is you and the Grok Bot.';
    const setup = await api('POST', '/v1/messages', {
      idempotency_key: 't10-setup', recipients: [demo.peers.grok.id], thread_title: 'Grok setup', priority: 'wake_idle', content: { text: setupBody },
    });
    const triageBody = 'Triage: choose a priority before this Grok Bot is known.';
    const triage = await api('POST', '/v1/messages', {
      idempotency_key: 't10-triage', recipients: [demo.peers.triage.id], thread_title: 'Grok triage', priority: 'wake_idle', content: { text: triageBody },
    });
    const detailStall = async route => {
      const path = new URL(route.request().url()).pathname;
      if (route.request().method() === 'GET' && (path === `/v1/admin/threads/${setup.thread_id}` || path === `/v1/admin/threads/${triage.thread_id}`)) await factsGate;
      await route.continue();
    };
    await page.route('**/v1/admin/threads/**', detailStall);
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, setup.thread_id);
    await page.locator('.msg .txt', { hasText: setupBody }).waitFor();
    assert(await selected() === 'wake', `setup reply preselected ${await selected()} before its participants arrived`);
    assert(await note.isHidden(), 'quota note showed before a grokbot participant was known');
    await comp.locator('.seg').evaluate(el => el.scrollIntoView({ block: 'center' }));
    await shot('t10-thread-delayed');

    await page.evaluate(id => { location.hash = `#thread/${id}`; }, triage.thread_id);
    await page.locator('.msg .txt', { hasText: triageBody }).waitFor();
    assert(await selected() === 'wake', 'triage reply changed priority before its participants arrived');
    await comp.getByRole('button', { name: 'hold', exact: true }).click();
    assert(await selected() === 'hold', 'explicit hold did not stick');
    await page.mouse.move(0, 0);
    await page.evaluate(() => { const el = document.activeElement; if (el && el.blur) el.blur(); });
    await comp.locator('.ptip[data-p="hold"]').waitFor({ state: 'hidden' });
    await shot('t10-thread-explicit-hold');
    releaseFacts();
    await note.waitFor();
    assert(await selected() === 'hold', 'participant data overwrote explicit hold');
    assert(await note.textContent() === plainLine, `after an explicit hold the note says: ${await note.textContent()}`);
    await comp.locator('#r-quota').evaluate(el => el.scrollIntoView({ block: 'center' }));
    await shot('t10-thread-sticky');
    await page.unroute('**/v1/threads?*', stall);
    await page.unroute('**/v1/admin/threads?*', stall);
    await page.unroute('**/v1/admin/threads/**', detailStall);

    // Failed reply while the grokbot is still unknown, then the directory
    // arrives. The unchanged text keeps wake and would retry the same key.
    // Editing it preselects wake_idle under a new key.
    let releaseLate;
    const lateGate = new Promise(resolve => { releaseLate = resolve; });
    const stallLate = async route => { await lateGate; await route.continue(); };
    await page.route('**/v1/threads?*', stallLate);
    await page.route('**/v1/admin/threads?*', stallLate);
    const lateBody = 'Late directory: reply before the Grok Bot is named.';
    const late = await api('POST', '/v1/messages', {
      idempotency_key: 't10-late', recipients: [demo.peers.grok.id], thread_title: 'Late grok', priority: 'wake_idle', content: { text: lateBody },
    });
    const detailLate = async route => {
      const path = new URL(route.request().url()).pathname;
      if (route.request().method() === 'GET' && path === `/v1/admin/threads/${late.thread_id}`) await lateGate;
      await route.continue();
    };
    await page.route('**/v1/admin/threads/**', detailLate);
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, late.thread_id);
    await page.locator('.msg .txt', { hasText: lateBody }).waitFor();
    assert(await selected() === 'wake', `late reply preselected ${await selected()} before its participants arrived`);
    const failedReply = 'Failed before the bot was known.';
    await ta.fill(failedReply);
    const posted = posts.length;
    await page.route('**/v1/messages', route => route.fulfill({
      status: 503, contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'unavailable', message: 'try again' } }),
    }));
    await comp.locator('.send').click();
    await comp.locator('.cbanner', { hasText: 'unavailable: try again' }).waitFor();
    assert(await selected() === 'wake', 'the failed reply changed priority');
    assert(await ta.inputValue() === failedReply, 'the failed reply changed the body');
    releaseLate();
    await note.waitFor();
    assert(await selected() === 'wake', 'an unchanged failed reply adopted wake_idle when the grokbot became known');
    assert(await note.textContent() === plainLine, `a kept wake retry is described as: ${await note.textContent()}`);
    const editedReply = failedReply + ' Edited.';
    await ta.fill(editedReply);
    assert(await selected() === 'wake_idle', 'editing the failed reply left wake selected');
    assert(await note.textContent() === quotaLine, `the edited reply's automatic wake_idle note: ${await note.textContent()}`);
    await page.unroute('**/v1/messages');
    await comp.locator('.send').click();
    await page.locator('.msg .txt', { hasText: editedReply }).waitFor();
    assert(posts.length === posted + 2, `the edited reply sent ${posts.length - posted} times`);
    assert(posts[posted].priority === 'wake' && posts[posted].content.text === failedReply, `failed body ${JSON.stringify(posts[posted])}`);
    assert(posts[posted + 1].priority === 'wake_idle' && posts[posted + 1].content.text === editedReply, `edited body ${JSON.stringify(posts[posted + 1])}`);
    assert(posts[posted].idempotency_key !== posts[posted + 1].idempotency_key, 'the edited reply reused the failed key');
    await page.locator('#toast.on', { hasText: 'Sent' }).waitFor();
    await page.locator('#toast.on').waitFor({ state: 'hidden' });
    await page.unroute('**/v1/threads?*', stallLate);
    await page.unroute('**/v1/admin/threads?*', stallLate);
    await page.unroute('**/v1/admin/threads/**', detailLate);

    await page.evaluate(id => { location.hash = `#thread/${id}`; }, setup.thread_id);
    await page.locator('.msg .txt', { hasText: setupBody }).waitFor();
    await note.waitFor();
    assert(await selected() === 'wake_idle', `setup reply priority is ${await selected()}`);
    const setupNote = await note.textContent();
    assert(setupNote === quotaLine, setupNote);
    await comp.locator('#r-quota').evaluate(el => el.scrollIntoView({ block: 'center' }));
    await noteFits('setup', { hint: true });
    await shot('t10-thread-setup');
    await page.mouse.move(0, 0);
    await page.evaluate(() => { const el = document.activeElement; if (el && el.blur) el.blur(); });
    // Mid widths: half-screen desktop windows, tablets and phones in
    // landscape. The note stays one line under the controls at each.
    for (const w of [900, 800, 600, 480]) {
      await page.setViewportSize({ width: w, height: 900 });
      await comp.locator('#r-quota').evaluate(el => el.scrollIntoView({ block: 'center' }));
      await noteFits(`setup at ${w}`);
      await shot(`t10-thread-w${w}`);
    }
    await page.setViewportSize({ width: 390, height: 844 });
    await comp.locator('#r-quota').evaluate(el => el.scrollIntoView({ block: 'center' }));
    const setupNarrow = await page.evaluate(() => document.documentElement.scrollWidth);
    assert(setupNarrow <= 390, `setup reply scrolls sideways at 390 (${setupNarrow})`);
    await noteFits('setup at 390');
    await shot('t10-thread-setup-narrow');
    await page.setViewportSize({ width: 1440, height: 900 });
    await comp.getByRole('button', { name: 'wake', exact: true }).click();
    assert(await selected() === 'wake', 'explicit wake did not stick on the setup thread');
    const wakeNote = await note.textContent();
    assert(!/preselected/i.test(wakeNote), `after an explicit wake the note says: ${wakeNote}`);
    assert(wakeNote === plainLine, wakeNote);
    await noteFits('explicit wake', { hint: true });
    await page.mouse.move(0, 0);
    await comp.locator('.ptip[data-p="wake"]').waitFor({ state: 'hidden' });
    await shot('t10-thread-explicit-wake');
    await comp.locator('.seg button[value="wake"]').focus();
    await page.keyboard.press('Tab');
    assert(await page.evaluate(() => document.activeElement && document.activeElement.value) === 'wake_idle', 'Tab did not reach wake_idle');
    await comp.locator('.ptip[data-p="wake_idle"]').waitFor({ state: 'visible' });
    await shot('t10-thread-keyboard');
    await page.keyboard.press('Escape');
    await page.evaluate(() => { const el = document.activeElement; if (el && el.blur) el.blur(); });
    await comp.locator('.ptip[data-p="wake_idle"]').waitFor({ state: 'hidden' });
    await page.setViewportSize({ width: 390, height: 844 });
    await comp.locator('#r-quota').evaluate(el => el.scrollIntoView({ block: 'center' }));
    const narrowWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    assert(narrowWidth <= 390, `thread scrolls sideways at 390 (${narrowWidth})`);
    assert(await selected() === 'wake' && await note.textContent() === plainLine, `narrow explicit wake: ${await selected()}, ${await note.textContent()}`);
    await noteFits('explicit wake at 390');
    await shot('t10-thread-narrow');
    await page.setViewportSize({ width: 1440, height: 900 });
    // Reply drafts survive a real unmount, but belong to just one thread.
    const openThread = async id => {
      await page.evaluate(id => { location.hash = `#thread/${id}`; }, id);
      await page.locator('#view .thead .ids', { hasText: id }).waitFor();
      await quiet();
    };
    const inbox = async () => {
      await page.evaluate(() => { location.hash = '#inbox'; });
      await page.locator('#view h1', { hasText: /^Inbox$/ }).waitFor();
    };
    await openThread(hosting);
    assert(await comp.isVisible(), 'a real thread hid its composer');
    const savedDraft = 'Navigation draft: café 漢字.';
    await ta.fill(savedDraft);
    await inbox();
    await openThread(hark);
    assert(await ta.inputValue() === '', 'a draft leaked into another thread');
    await openThread(hosting);
    assert(await ta.inputValue() === savedDraft, 'navigation lost the reply draft');
    assert(await comp.locator('.bytes').textContent() === new TextEncoder().encode(savedDraft).length + ' / 4096 B', 'restored draft byte count is wrong');
    await ta.press('Enter');
    await page.locator('.msg .txt', { hasText: savedDraft }).waitFor();
    await comp.locator('.send:enabled').waitFor();
    assert(await ta.inputValue() === '', 'the restored successful draft did not clear');
    await inbox();
    await openThread(hosting);
    assert(await ta.inputValue() === '', 'navigation restored an already sent draft');

    // A send from an old mount must not erase a newer mount's saved draft.
    const oldDraft = 'Sending while away from this thread.';
    const newerDraft = 'New draft saved before the old send finishes.';
    let releaseOld;
    const oldGate = new Promise(resolve => { releaseOld = resolve; });
    const holdOld = async route => { await oldGate; await route.continue(); };
    await page.route('**/v1/messages', holdOld);
    const stopIgnoring = ignore(req => req.method() === 'POST' && req.url().endsWith('/v1/messages'));
    await ta.fill(oldDraft);
    await ta.press('Enter');
    await comp.locator('.send:disabled').waitFor();
    await inbox();
    await openThread(hosting);
    assert(await ta.inputValue() === '', 'an in-flight send was restored as a draft');
    await ta.fill(newerDraft);
    await inbox();
    const oldResponse = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/v1/messages'));
    releaseOld();
    await oldResponse;
    stopIgnoring();
    await quiet();
    await page.unroute('**/v1/messages', holdOld);
    await openThread(hosting);
    assert(await ta.inputValue() === newerDraft, 'an old send erased a newer saved draft');
    const oldMessages = (await api('GET', `/v1/threads/${hosting}/messages?limit=200`)).messages.filter(m => m.content.text === oldDraft);
    assert(oldMessages.length === 1, 'the send completing after unmount did not land exactly once');
    await ta.fill('');

    // Run last: an unknown id must offer an exit, not a working Send button.
    allowConsole(/status of 404/);
    await page.evaluate(() => { location.hash = '#thread/thr_01ZZZZZZZZZZZZZZZZZZZZZZZZ'; });
    await page.locator('#view .empty.error', { hasText: 'Thread unavailable' }).waitFor();
    assert(await comp.isHidden(), 'unknown thread shows a live composer');
    await page.getByRole('link', { name: 'Back to the inbox', exact: true }).click();
    await page.locator('#view h1', { hasText: /^Inbox$/ }).waitFor();
    assert(await page.evaluate(() => location.hash) === '#inbox', 'unknown thread exit did not open the inbox');
    await openThread(hosting);
    assert(await comp.isVisible(), 'returning to a real thread hid its composer');
    assert(countRequests(isRead) === 0, 'the thread view posted mark-all');
  });
  console.log('Thread passed: opens at New and reads by receipt, receipts, live delivery, reply jump, literal full text, held retry key, budget banner, inline approvals, join notice, grokbot wake_idle preselect stays sticky');
} finally { await cleanBuild(); }
