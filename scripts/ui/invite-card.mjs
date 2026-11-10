// The peer page's Invite card (#67) in a real browser, at 1440 and 390 px: a
// peer that has not enrolled shows its invite state; New invite re-issues
// (the previous token stops enrolling) and opens the same paste block as Add
// agent, byte for byte, which Copy invite puts on the clipboard; Revoke
// invite (two clicks) withdraws it so the token no longer enrols; a refused
// re-issue shows its error; an expired invite offers only New invite. The
// token is in the copy block's text alone: never an attribute, a URL, web
// storage or a request, and gone from the page after Done, after a revoke and
// once the peer enrols (the card closes). Enrolled peers, dots and sources
// have no card and cost no invite read.
//
// Shots never show a real token: the re-issue answer's token is swapped for
// FAKE before the page sees it. The real one stays in this process, only to
// prove it is refused once revoked or replaced, and is never printed.
import { assert, cleanBuild, scrollIntoViewStable, withDemo } from './lib.mjs';
import { inviteBlock, reinvite, slug } from '../../internal/ui/static/invite-model.js';

const FAKE = 'vn_enrol_EXAMPLE-not-a-real-token-0000000000';
const SIZES = { desk: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, advance, allowConsole, countRequests, quiet } = ctx;
    allowConsole(/status of 500/);
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    const id = key => demo.peers[key].id;
    const route = async hash => {
      await page.evaluate(h => { location.hash = h; }, hash);
      await page.waitForFunction(h => location.hash === h, hash);
    };
    const addPeer = async body => {
      const r = await asPeer('operator', 'POST', '/v1/admin/peers', body);
      assert(r.status === 201, `add peer: ${r.status}`);
      return r.json();
    };
    const enrol = token => fetch(demo.url + '/v1/enrol', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) });
    const status = async peer => (await (await asPeer('operator', 'GET', `/v1/admin/peers/${peer}/enrol-token`)).json());

    // The re-issue answer reaches the page with FAKE in place of its token.
    let real = null, answer = null;
    const swap = async r => {
      if (r.request().method() !== 'POST') return r.continue();
      const res = await r.fetch();
      const body = await res.json();
      real = body.enrol_token;
      answer = { ...body, enrol_token: FAKE };
      await r.fulfill({ response: res, json: answer });
    };
    await page.route('**/v1/admin/peers/*/enrol-token', swap);

    // No token anywhere but the copy block's text: attributes, URL, storage.
    const leaks = async () => page.evaluate(toks => {
      const hits = [];
      for (const t of toks) {
        for (const el of document.querySelectorAll('*')) for (const a of el.attributes) if (a.value.includes(t)) hits.push(el.tagName + '[' + a.name + ']');
        if (location.href.includes(t)) hits.push('url');
        for (const s of [localStorage, sessionStorage]) for (let i = 0; i < s.length; i++) { const k = s.key(i); if (k.includes(t) || String(s.getItem(k)).includes(t)) hits.push('storage'); }
      }
      return hits;
    }, [FAKE, real].filter(Boolean));
    const inRequests = () => countRequests(req => req.url().includes(FAKE) || (real && req.url().includes(real)) || (real && (req.postData() || '').includes(real)));
    const gone = async () => {
      const html = await page.content();
      assert(!html.includes(FAKE) && !(real && html.includes(real)), 'a token is still in the page');
    };

    // Peers that have not enrolled: two that expire in a minute, then two live.
    const expired = { desk: await addPeer({ kind: 'session', name: 'late claude', enrol_ttl_seconds: 60 }), phone: await addPeer({ kind: 'session', name: 'late phone', enrol_ttl_seconds: 60 }) };
    await advance('2m');
    const pending = { desk: await addPeer({ kind: 'session', name: 'web-repo claude' }), phone: await addPeer({ kind: 'session', name: 'phone-repo claude' }) };

    await page.locator('#conn.live').waitFor();
    const pv = page.locator('.pv');
    const card = pv.locator('section.pv-invite');
    const newBtn = card.locator('.pv-inv-new'), revokeBtn = card.locator('.pv-inv-revoke');

    for (const [size, viewport] of Object.entries(SIZES)) {
      await page.setViewportSize(viewport);
      const p = pending[size];
      const fits = async name => {
        if (size !== 'phone') return;
        const width = await page.evaluate(() => document.documentElement.scrollWidth);
        assert(width <= viewport.width, `${name}: the page scrolls sideways at ${viewport.width} px (${width})`);
      };
      const snap = async (name, sel = 'section.pv-invite') => {
        await quiet();
        await scrollIntoViewStable(page, sel);
        await fits(name);
        await page.mouse.move(5, 5);
        await shot(`${name}-${size}`);
      };

      // ---- initial: the invite is pending ----
      await route('#peer/' + p.peer_id);
      await card.locator('.inv-st.pending').waitFor();
      assert(await card.evaluate(el => el.classList.contains('st-pending')), 'the card is not pending');
      assert((await card.locator('.pv-inv-line').innerText()).startsWith('Not enrolled yet.'), 'pending line');
      await pv.locator('.pv-identity dd', { hasText: 'not yet · added' }).waitFor();
      assert(await card.locator('.pv-inv-line time').getAttribute('datetime') === p.enrol_expires_at, 'pending expiry');
      assert(await newBtn.isVisible() && await revokeBtn.isVisible(), 'a pending invite offers New invite and Revoke invite');
      assert(await card.locator('#pv-inv-ttl').inputValue() === '86400', 'New invite defaults to 24 hours');
      assert(!(await leaks()).length, 'leak before any re-issue');
      await snap('pvi-1-pending');

      // ---- New invite, then Copy: the shared panel, the same text as Add agent ----
      const issued = page.waitForResponse(r => r.url().endsWith(`/v1/admin/peers/${p.peer_id}/enrol-token`) && r.request().method() === 'POST');
      await newBtn.click();
      await issued;
      const pre = card.locator('.pv-inv-fresh pre.inv-block');
      await pre.waitFor();
      const text = await pre.textContent();
      assert(text === reinvite(answer, demo.url).text, `card text:\n${text}`);
      assert(text === inviteBlock({ owner: answer.hub_owner, hubURL: answer.hub_url, profile: slug(p.name), token: FAKE, expires: answer.enrol_expires_at }), 'the card text is not Add agent\'s');
      assert(real && real !== p.enrol_token, 'the re-issue gave no new token');
      await card.locator('.pv-inv-fresh .pv-inv-msg', { hasText: 'The previous invite no longer works.' }).waitFor();
      await card.locator('.pv-inv-fresh .inv-warn', { hasText: 'Shown once' }).waitFor();
      assert((await enrol(p.enrol_token)).status === 401, 'the replaced token still enrols');
      await card.locator(`.pv-inv-line time[datetime="${answer.enrol_expires_at}"]`).waitFor(); // the new expiry, re-read
      await card.getByRole('button', { name: 'Copy invite' }).click();
      await card.getByRole('button', { name: 'Copied' }).waitFor();
      assert(await page.evaluate(() => navigator.clipboard.readText()) === text, 'the clipboard does not hold the invite');
      const hits = await leaks();
      assert(!hits.length, `the token leaked: ${hits.join(', ')}`);
      assert(inRequests() === 0, 'a request carried the token');
      await snap('pvi-2-copied', '.pv-inv-fresh .inv-acts');

      // Done drops it: out of the page, focus back on New invite.
      await card.getByRole('button', { name: 'Done' }).click();
      await card.locator('.pv-inv-fresh').waitFor({ state: 'hidden' });
      await gone();
      assert(await newBtn.evaluate(el => el === document.activeElement), 'Done did not return focus to New invite');

      // ---- Revoke: two clicks; the live token no longer enrols ----
      await revokeBtn.click();
      await card.locator('.pv-inv-revoke.armed', { hasText: 'Confirm revoke' }).waitFor();
      await revokeBtn.click();
      await card.locator('.inv-st.revoked').waitFor();
      await card.locator('.pv-inv-msg', { hasText: 'Invite revoked.' }).waitFor();
      assert((await card.locator('.pv-inv-line').innerText()).includes('its token no longer enrols'), 'revoked line');
      assert(!(await revokeBtn.isVisible()), 'a revoked invite still offers Revoke');
      assert((await enrol(real)).status === 401, 'the revoked token still enrols');
      assert((await status(p.peer_id)).state === 'revoked', 'the hub does not say revoked');
      await gone();
      await snap('pvi-3-revoked');

      // ---- A refused re-issue: a fixed sentence, never the hub's code or message ----
      const refuse = async r => r.request().method() === 'POST'
        ? r.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'demo_failure', message: 'Demo failure', details: {} } }) })
        : r.continue();
      await page.unroute('**/v1/admin/peers/*/enrol-token', swap);
      await page.route('**/v1/admin/peers/*/enrol-token', refuse);
      await newBtn.click();
      await card.locator(':scope > .err', { hasText: /^Could not issue a new invite\.$/ }).waitFor();
      const refused = await card.locator(':scope > .err').innerText();
      assert(!refused.includes('demo_failure') && !refused.includes('Demo failure'), `refused re-issue shows hub text: ${refused}`);
      assert(await newBtn.isEnabled() && (await newBtn.innerText()) === 'New invite', 'New invite stays busy after an error');
      assert(await card.locator('.pv-inv-fresh').isHidden(), 'an error opened the copy panel');
      await snap('pvi-4-error');
      await page.unroute('**/v1/admin/peers/*/enrol-token', refuse);
      await page.route('**/v1/admin/peers/*/enrol-token', swap);

      // ---- No live invite: expired, so New invite only ----
      const e = expired[size];
      await route('#peer/' + e.peer_id);
      await card.locator('.inv-st.expired').waitFor();
      assert((await card.locator('.pv-inv-line').innerText()).startsWith('Not enrolled. Its invite expired'), 'expired line');
      assert(await newBtn.isVisible() && !(await revokeBtn.isVisible()), 'an expired invite offers Revoke');
      await snap('pvi-5-none');
    }
    // A very long hostile name (and its Open thread quote) wraps or ellipsizes
    // inside the one column at 390 px: the page never scrolls sideways.
    await route('#peer/' + id('hostile'));
    await pv.locator('.pv-head h1 .nm').waitFor();
    await quiet();
    const wide = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, out: [...document.querySelectorAll('.pv-head, .pv-head *')].filter(e => e.getBoundingClientRect().right > innerWidth + 0.5).map(e => e.className || e.tagName) }));
    assert(wide.doc <= SIZES.phone.width && !wide.out.length, `hostile peer page scrolls sideways at 390 px: ${JSON.stringify(wide)}`);
    await page.setViewportSize(SIZES.desk);

    // ---- The peer enrols while its invite is open: the card closes and the
    // token goes with it ----
    const joining = await addPeer({ kind: 'session', name: 'joining claude' });
    await route('#peer/' + joining.peer_id);
    await card.locator('.inv-st.pending').waitFor();
    await newBtn.click();
    await card.locator('.pv-inv-fresh pre.inv-block').waitFor();
    assert((await enrol(real)).status === 200, 'the new invite does not enrol');
    await card.waitFor({ state: 'hidden' });
    await gone();
    assert((await status(joining.peer_id)).state === 'claimed', 'the hub does not say claimed');

    // ---- No card, and no invite read, where no token can apply ----
    const reads = () => countRequests(req => new URL(req.url()).pathname.endsWith('/enrol-token') && req.method() === 'GET');
    const dot = await addPeer({ kind: 'dot', name: 'dot-fresh' });
    await route('#peer/' + id('codex'));
    await pv.locator('.pv-head h1', { hasText: 'codex-hark' }).waitFor();
    await quiet();
    const before = reads();
    for (const [pid, name] of [[id('codex'), 'codex-hark'], [id('dot'), 'dot-research'], [dot.peer_id, 'dot-fresh'], [id('gh'), 'gh-ultradyn'], [demo.operator_peer, null]]) {
      await route('#peer/' + pid);
      if (name) await pv.locator('.pv-head h1', { hasText: name }).waitFor();
      await pv.locator('.pv-identity dl').waitFor();
      await quiet();
      assert(await card.isHidden(), `${name || 'the operator'} shows an Invite card`);
      assert(await pv.locator('.pv-identity dd', { hasText: 'not yet' }).count() === 0, `${name || 'the operator'} reads as not enrolled`);
    }
    assert(reads() === before, 'an enrolled peer, a dot, a source or the operator cost an invite read');
    assert(inRequests() === 0, 'a request carried the token');
  });
  console.log('Invite card passed: pending, New invite with the Add agent text copied, Done, two-click revoke refusing the token, a refused re-issue, expired with New invite only, at 1440 and 390 px; the card closes when the peer enrols; no card or read for enrolled peers, dots, sources or the operator; the token never in an attribute, URL, storage or request');
} finally { await cleanBuild(); }
