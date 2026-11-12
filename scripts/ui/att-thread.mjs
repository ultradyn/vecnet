// Attachments Task 4: reply attachments and inline images in the thread, in a
// real browser. A peer's PNG shows inline; a fake PNG keeps a deferred opener,
// while SVG/text stay chips. The operator pastes a screenshot, the
// reply is lost and retried under the same key, and opens the image full size.
import { assert, cleanBuild, hex, png, scrollIntoViewStable, withDemo } from './lib.mjs';

// A terminal window with coloured log lines: the peer's "failing run".
const terminal = png(480, 300, (x, y) => {
  if (y < 26) {
    for (const [cx, c] of [[18, '#fb7185'], [36, '#fbbf24'], [54, '#34d399']]) if ((x - cx) ** 2 + (y - 13) ** 2 < 26) return hex(c);
    return hex('#1a2139');
  }
  const row = Math.floor((y - 38) / 18), inRow = (y - 38) % 18;
  if (row >= 0 && row < 14 && inRow >= 4 && inRow < 11) {
    const width = 120 + ((row * 97) % 300), colour = row === 9 || row === 10 ? '#fb7185' : row % 4 === 0 ? '#a78bfa' : row === 13 ? '#34d399' : '#64748b';
    if (x >= 16 && x < 16 + 24) return hex('#334155');
    if (x >= 48 && x < 48 + width) return hex(colour);
  }
  return hex('#0b1020');
});
// A chart the operator pastes back: bars on a dark panel.
const chart = png(420, 260, (x, y) => {
  if (x < 28 || y > 236) return hex('#151c33');
  const bar = Math.floor((x - 40) / 46), inBar = (x - 40) % 46;
  const heights = [60, 110, 90, 170, 140, 200, 120, 180];
  if (bar >= 0 && bar < 8 && inBar < 30 && y > 236 - heights[bar]) {
    const t = (236 - y) / 200;
    return [Math.round(99 + (168 - 99) * t), Math.round(102 + (85 - 102) * t), Math.round(241 + (247 - 241) * t)];
  }
  return y % 40 === 0 ? hex('#1a2139') : hex('#0e1426');
});

const SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" onload="alert(1)"><script>alert(2)</script><rect width="64" height="64" fill="red"/></svg>';
const FAKE_PNG = '<html><body><script>alert(3)</script><img src=x onerror=alert(4)></body></html>';
const HOSTILE_NAME = 'é'.repeat(75) + '\u202Egnp.exe' + 'é'.repeat(75);
const ALLOWED = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, allowConsole, countRequests } = ctx;
    const thr = demo.threads.ticket18;
    // Chromium logs every non-2xx fetch and every aborted request; these are provoked on purpose.
    allowConsole(/status of 429/);
    allowConsole(/net::ERR_FAILED/);
    let dialogs = 0, downloads = 0;
    page.on('dialog', async d => { dialogs++; await d.dismiss(); });
    page.on('download', () => { downloads++; });

    const api = async (method, path, body, as = 'operator') => {
      const r = await asPeer(as, method, path, body);
      assert(r.ok, `${as} ${method} ${path}: ${r.status}`);
      return r.status === 204 ? null : r.json();
    };
    // A peer's raw upload: asPeer JSON-encodes bodies, so this is a plain fetch.
    const upload = async (body, type) => {
      const r = await fetch(demo.url + '/v1/blobs?ttl=3600', { method: 'PUT', headers: { Authorization: 'Bearer ' + demo.peers.codex.secret, 'Content-Type': type }, body });
      assert(r.ok, `upload ${type}: ${r.status}`);
      return r.json();
    };
    const blobGets = sha => countRequests(req => req.method() === 'GET' && new URL(req.url()).pathname === '/v1/blobs/' + sha);
    const isUpload = url => url.pathname === '/v1/blobs';
    const msgEl = id => page.locator(`[id="m-${id}"]`);
    const urls = () => page.evaluate(() => {
      const s = window.__urls;
      return { made: s.made, revoked: s.revoked, invalid: s.invalid,
        detachViolations: s.detachViolations, live: [...s.live.values()] };
    });
    const demand = selector => scrollIntoViewStable(page, selector);
    const focusOccurrence = occurrence => {
      assert(occurrence, 'the thumbnail has no occurrence identity');
      return page.waitForFunction(key => document.activeElement?.matches('button.thumb') &&
        document.activeElement.dataset.occurrence === key, occurrence, { timeout: 5000 });
    };
    const imgLoaded = (loc, failure = null) => loc.evaluate((img, selector) => {
      const finish = loaded => {
        if (loaded || !selector) return loaded;
        const snapshot = n => {
          if (!n) return null;
          const button = n.closest('button'), images = [...(button?.querySelectorAll('img') || [])];
          return { connected: n.isConnected, complete: n.complete, hasNaturalWidth: n.naturalWidth > 0,
            parentButtonClass: (button?.className || '').slice(0, 64),
            images: images.length, liveImages: images.filter(image => image.isConnected).length };
        };
        const currentButton = document.querySelector(selector), current = currentButton?.querySelector('img') || null;
        return { resolved: snapshot(img), current: snapshot(current), same: current === img,
          currentButtonClass: (currentButton?.className || '').slice(0, 64),
          matches: document.querySelectorAll(selector + ' img').length };
      };
      return img.complete ? finish(img.naturalWidth > 0) : new Promise(resolve => {
        img.addEventListener('load', () => resolve(finish(img.naturalWidth > 0)), { once: true });
        img.addEventListener('error', () => resolve(finish(false)), { once: true });
      });
    }, failure?.selector || null).then(result => {
      if (typeof result === 'boolean') return result;
      failure.snapshot = result;
      return false;
    });

    await page.locator('#threads a', { hasText: 'Ticket 18' }).waitFor();
    await page.locator('#conn.live').waitFor();
    // withDemo already loaded the app. Reload with context hooks installed
    // before module capture; keep only scalars and live URL metadata.
    await page.context().addInitScript(() => {
      const make = URL.createObjectURL, drop = URL.revokeObjectURL;
      const s = window.__urls = { made: 0, revoked: 0, live: new Map(), invalid: false, detachViolations: 0 };
      URL.createObjectURL = function (b) {
        const u = Reflect.apply(make, this, [b]);
        s.made++;
        const type = typeof b.type === 'string' ? b.type : '';
        if (!['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(type.split(';')[0].trim().toLowerCase())) s.invalid = true;
        if (s.live.size >= 4096 || !Number.isFinite(b.size)) s.invalid = true;
        else s.live.set(u, { type, size: b.size });
        return u;
      };
      URL.revokeObjectURL = function (u) {
        const attached = [...document.querySelectorAll('img[src]')].some(img => img.getAttribute('src') === u);
        const result = Reflect.apply(drop, this, [u]);
        // A native failure must leave counts and metadata unchanged.
        s.revoked++;
        if (attached) s.detachViolations++;
        s.live.delete(u);
        return result;
      };
    });
    await page.reload({ waitUntil: 'load' });
    await page.locator('#threads a', { hasText: 'Ticket 18' }).waitFor();
    await page.locator('#conn.live').waitFor();
    await page.evaluate(() => { window.__noReload = true; });

    // The peer's message: a real PNG and three hostile attachments.
    const pngUp = await upload(terminal, 'image/png');
    const svgUp = await upload(SVG, 'image/svg+xml');
    const fakeUp = await upload(FAKE_PNG, 'image/png');
    const nameUp = await upload('radio: received over\nradio: finalize skipped\n', 'text/plain');
    const peerText = 'Screenshot of the failing run, plus what the bot attached.';
    const peerMsg = await api('POST', '/v1/messages', {
      idempotency_key: 'att-thread-peer', thread_id: thr, priority: 'wake', content: { text: peerText },
      attachments: [
        { sha256: pngUp.sha256, size: pngUp.size, media_type: 'image/png', name: 'run-failure.png' },
        { sha256: svgUp.sha256, size: svgUp.size, media_type: 'image/svg+xml', name: 'diagram.svg' },
        { sha256: fakeUp.sha256, size: fakeUp.size, media_type: 'image/png', name: 'preview.png' },
        { sha256: nameUp.sha256, size: nameUp.size, media_type: 'text/plain', name: HOSTILE_NAME },
      ],
    }, 'codex');

    await page.evaluate(id => { location.hash = `#thread/${id}`; }, thr);
    const peer = msgEl(peerMsg.message_id);
    await peer.locator('.txt', { hasText: peerText }).waitFor();
    const peerSelector = `[id="m-${peerMsg.message_id}"]`;
    const pngSelector = `${peerSelector} button.thumb[data-sha="${pngUp.sha256}"]`;
    await demand(pngSelector);
    // The fake PNG is briefly an <img> too, until its decode error: pick the real one by hash.
    const thumb = peer.locator(`button.thumb[data-sha="${pngUp.sha256}"] img`);
    await thumb.waitFor();
    const peerDecode = { selector: pngSelector };
    assert(await imgLoaded(thumb, peerDecode), 'the peer PNG did not decode inline: ' + JSON.stringify(peerDecode.snapshot));
    assert(await thumb.getAttribute('alt') === 'run-failure.png', 'the thumbnail alt is not the cleaned name');
    // The SVG never fetches; the failed raster keeps its safe deferred opener.
    await peer.locator('.att', { hasText: 'diagram.svg' }).waitFor();
    const fakeSelector = `${peerSelector} button.thumb[data-sha="${fakeUp.sha256}"]`;
    const fake = page.locator(fakeSelector);
    await demand(fakeSelector);
    await fake.filter({ hasText: 'Image unavailable. Retry in viewer or use Files.' }).waitFor();
    assert(await fake.getAttribute('aria-label') === 'Open preview.png', 'the failed raster lost its accessible opener');
    const fakeState = await page.evaluate(selector => {
      // Resolve and snapshot the current synthetic fixture in one turn.
      const snapshot = n => n && ({
        connected: n.isConnected, deferred: n.matches('.thumb.deferred'),
        className: n.className.slice(0, 64), text: n.textContent.slice(0, 96),
        label: (n.getAttribute('aria-label') || '').slice(0, 64),
        images: n.querySelectorAll('img').length, sources: n.querySelectorAll('[src]').length,
      });
      const matches = document.querySelectorAll(selector);
      return { current: snapshot(matches[0] || null), matches: matches.length };
    }, fakeSelector);
    const s = fakeState.current;
    assert(fakeState.matches === 1 && s?.connected && s.deferred &&
      s.text === 'Image unavailable. Retry in viewer or use Files.' && s.label === 'Open preview.png' &&
      s.images === 0 && s.sources === 0,
      'the failed raster kept an image/source or lost its deferred opener: ' + JSON.stringify(fakeState));
    const named = peer.locator('.att', { hasText: 'gnp.exe' });
    await named.waitFor();
    assert(await peer.locator('button.thumb').count() === 2, 'the two raster openers were not retained');
    assert(await peer.locator('button.thumb img').count() === 1, 'the failed raster retained an image');
    assert(await peer.locator('.att').count() === 2, 'SVG/text did not stay two chips');
    assert(blobGets(svgUp.sha256) === 0, 'the declared SVG was fetched');
    assert(blobGets(nameUp.sha256) === 0, 'a text attachment was fetched for a thumbnail');
    // Read the live node in one go: every update rebuilds the message, and a
    // detached node has no computed style.
    const nm = await page.evaluate(id => {
      const el = [...document.querySelectorAll(`[id="m-${id}"] .att .nm`)].find(n => n.textContent.includes('gnp.exe'));
      return el && { text: el.textContent, bidi: getComputedStyle(el).unicodeBidi };
    }, peerMsg.message_id);
    assert(nm, 'the bidi-named chip has no name');
    assert(!nm.text.includes('\u202E') && nm.text === HOSTILE_NAME.replace('\u202E', ''), 'the name was not cleaned of its bidi override');
    assert(nm.bidi === 'isolate', `the attachment name is not bidi-isolated: ${nm.bidi}`);
    assert(await page.locator('iframe, object, embed, a[href^="blob:"]').count() === 0, 'an attachment became a frame, an object or a blob link');
    // Every object URL is an allowed raster type; the fake PNG's URL is revoked on its decode error.
    await page.waitForFunction(() => window.__urls.revoked >= 1);
    let u = await urls();
    assert(!u.invalid && !u.detachViolations && u.live.every(m => ALLOWED.includes(m.type.split(';')[0].trim().toLowerCase())), 'unsafe URL type, overflow or revoke before source detachment');
    assert(u.made === 2 && u.revoked === 1 && u.live.length === 1, `initial URL accounting: ${JSON.stringify(u)}`);
    assert(!u.live.some(m => m.size === fakeUp.size), 'the failed raster kept a live URL');
    assert(dialogs === 0, 'an attachment ran script');
    assert(downloads === 0, 'a hostile attachment downloaded automatically');
    assert(blobGets(pngUp.sha256) === 1, 'initial demand fetched the real PNG more than once');
    await shot('att04-thread-initial');

    // Open may make another finite attempt. Observe its actual decode failure,
    // then one explicit Retry; no timer substitutes for a terminal state.
    const fakeOccurrence = await fake.getAttribute('data-occurrence');
    const hostileViewer = page.getByRole('dialog', { name: 'preview.png', exact: true });
    const hostileError = async () => {
      await hostileViewer.getByRole('status').filter({ hasText: 'Image unavailable. Retry in viewer or use Files.' }).waitFor();
      assert(await hostileViewer.locator('img, [src]').count() === 0, 'the hostile viewer retained an image/source');
      for (const name of ['Retry', 'Files', 'Cancel', 'Close']) await hostileViewer.getByRole('button', { name, exact: true }).waitFor();
      const state = await urls();
      assert(!state.invalid && !state.detachViolations && !state.live.some(m => m.size === fakeUp.size), 'the hostile viewer leaked a URL or revoked an attached source');
    };
    await fake.click();
    await hostileError();
    const beforeRetry = { gets: blobGets(fakeUp.sha256), urls: await urls() };
    const [retried] = await Promise.all([
      page.waitForResponse(r => r.request().method() === 'GET' && new URL(r.url()).pathname === '/v1/blobs/' + fakeUp.sha256),
      hostileViewer.getByRole('button', { name: 'Retry', exact: true }).click(),
    ]);
    assert(retried.ok() && await retried.finished() === null, 'the explicit Retry did not complete its native request');
    await page.waitForFunction(({ made, revoked, size }) => window.__urls.made > made &&
      window.__urls.revoked > revoked && ![...window.__urls.live.values()].some(m => m.size === size),
    { made: beforeRetry.urls.made, revoked: beforeRetry.urls.revoked, size: fakeUp.size });
    await hostileError();
    assert(blobGets(fakeUp.sha256) === beforeRetry.gets + 1, 'Retry did not make exactly one finite request');
    await hostileViewer.getByRole('button', { name: 'Files', exact: true }).click();
    await hostileViewer.waitFor({ state: 'detached' });
    await page.waitForFunction(() => {
      const f = document.querySelector('#aside .files');
      return f && f.getBoundingClientRect().height > 0 && document.activeElement === f;
    });
    assert(blobGets(fakeUp.sha256) === beforeRetry.gets + 1, 'the failed viewer retried automatically');
    await demand(fakeSelector);
    await fake.click();
    await hostileError();
    const afterReopen = blobGets(fakeUp.sha256);
    await hostileViewer.getByRole('button', { name: 'Cancel', exact: true }).click();
    await hostileViewer.waitFor({ state: 'detached' });
    await focusOccurrence(fakeOccurrence);
    assert(blobGets(fakeUp.sha256) === afterReopen, 'Cancel started another hostile request');
    assert(dialogs === 0 && downloads === 0, 'the hostile viewer ran script or downloaded automatically');
    assert(await page.locator('iframe, object, embed, a[href^="blob:"]').count() === 0, 'the hostile viewer embedded the body');

    // Explicit viewer attempts change totals. Re-demand the real raster and
    // snapshot only after that subcase, before testing redraw/cache reuse.
    await demand(pngSelector);
    await thumb.waitFor();
    assert(await imgLoaded(thumb), 'the real PNG did not survive the hostile viewer');
    const redrawURLs = (await urls()).made;
    // Redraws read the cache: a new message does not fetch again or leak URLs.
    const pngGets = blobGets(pngUp.sha256);
    assert(pngGets >= 1, 'the real PNG was never fetched');
    const nudge = 'Rerunning with the fix now.';
    await api('POST', '/v1/messages', { idempotency_key: 'att-thread-nudge', thread_id: thr, priority: 'hold', content: { text: nudge } }, 'codex');
    await page.locator('.msg .txt', { hasText: nudge }).waitFor();
    await thumb.waitFor();
    assert(blobGets(pngUp.sha256) === pngGets, 'a redraw fetched the thumbnail again');
    assert((await urls()).made === redrawURLs, 'a redraw made a new object URL');

    // A near miss: a file dropped just outside the composer neither navigates nor loses the draft.
    const ta = page.locator('.composer textarea');
    const draft = 'Draft that must survive a near miss.';
    await ta.fill(draft);
    const missed = await page.evaluate(() => {
      const dt = new DataTransfer();
      dt.items.add(new File(['near miss'], 'near-miss.txt', { type: 'text/plain' }));
      const target = document.querySelector('.thead'), opts = { bubbles: true, cancelable: true, dataTransfer: dt };
      target.dispatchEvent(new DragEvent('dragenter', opts));
      const over = target.dispatchEvent(new DragEvent('dragover', opts));
      const drop = target.dispatchEvent(new DragEvent('drop', opts));
      return { over, drop, hash: location.hash };
    });
    assert(!missed.over && !missed.drop, 'the near-miss drop kept its default action');
    assert(missed.hash === '#thread/' + thr, 'the near-miss drop navigated');
    assert(await page.evaluate(() => window.__noReload === true), 'the page reloaded');
    assert(await ta.inputValue() === draft, 'the near-miss drop lost the draft');
    assert(await page.locator('.composer .up-chip').count() === 0, 'a near-miss drop attached the file');

    // Leaving the thread revokes its URLs; returning fetches again.
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.locator('.composer').waitFor({ state: 'detached' });
    u = await urls();
    assert(u.live.length === 0 && !u.invalid && !u.detachViolations, 'leaving the thread leaked URLs or revoked attached sources');
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, thr);
    await demand(pngSelector);
    await thumb.waitFor();
    assert(await imgLoaded(thumb), 'the thumbnail did not come back');
    assert(blobGets(pngUp.sha256) === pngGets + 1, 'returning to the thread did not fetch the thumbnail again');

    // Paste a screenshot into the reply; the upload is held to show progress.
    const reply = 'Here is what I see on my side.';
    await ta.fill(reply);
    // A local gate rather than ctx.hold, whose unroute races its own handler's
    // continue ('Route is already handled'; see thread.mjs).
    let releaseUpload;
    const uploadGate = new Promise(resolve => { releaseUpload = resolve; });
    const held = async route => { await uploadGate; await route.continue(); };
    await page.route(isUpload, held);
    await page.evaluate(bytes => {
      const dt = new DataTransfer();
      dt.items.add(new File([new Uint8Array(bytes)], 'image.png', { type: 'image/png' }));
      document.querySelector('.composer textarea').dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    }, [...chart]);
    const chip = page.locator('.composer .up-chip', { hasText: /pasted-\d{8}-\d{6}\.png/ });
    await chip.and(page.locator('.up-uploading')).waitFor();
    await page.locator('.composer .send:disabled').waitFor();
    assert(await ta.inputValue() === reply, 'pasting an image changed the text');
    await shot('att04-thread-progress');
    releaseUpload();
    await chip.and(page.locator('.up-done')).waitFor();
    await page.unroute(isUpload, held);
    await page.locator('.composer .send:enabled').waitFor();

    // The first send lands but its reply is lost; the retry keeps the key.
    const posts = [];
    page.on('request', req => { if (req.method() === 'POST' && req.url().endsWith('/v1/messages')) posts.push(req.postDataJSON()); });
    let attempt = 0;
    const lose = async route => { if (attempt++ === 0) { await route.fetch(); await route.abort('failed'); } else await route.continue(); };
    await page.route('**/v1/messages', lose);
    await page.locator('.composer .send').click();
    await page.locator('.composer .cbanner', { hasText: 'network:' }).waitFor();
    assert(await ta.inputValue() === reply, 'the text was lost when the send failed');
    assert(await chip.and(page.locator('.up-done')).count() === 1, 'the chip was lost when the send failed');
    await page.locator('.msg .txt', { hasText: reply }).waitFor();
    await Promise.all([page.locator('#toast.on', { hasText: 'Sent' }).waitFor(), page.locator('.composer .send').click()]);
    await page.unroute('**/v1/messages', lose);
    assert(posts.length === 2, `the reply went out ${posts.length} times`);
    assert(posts[0].idempotency_key === posts[1].idempotency_key, 'the retry minted a new idempotency key');
    const sentAtt = posts[0].attachments && posts[0].attachments[0];
    assert(sentAtt && sentAtt.media_type === 'image/png' && /^pasted-\d{8}-\d{6}\.png$/.test(sentAtt.name), 'the reply did not carry the pasted screenshot');
    const mine = (await api('GET', `/v1/threads/${thr}/messages?limit=200`)).messages.filter(m => m.content.text === reply);
    assert(mine.length === 1, `the retried reply landed ${mine.length} times`);
    assert(await ta.inputValue() === '', 'the composer kept the sent text');
    assert(await page.locator('.composer .up-chip').count() === 0, 'the sent chip stayed in the composer');

    // The operator's screenshot shows inline and opens full size.
    const ownThumb = msgEl(mine[0].message_id).locator('button.thumb');
    await demand(`[id="m-${mine[0].message_id}"] button.thumb`);
    await ownThumb.locator('img').waitFor();
    assert(await imgLoaded(ownThumb.locator('img')), 'the pasted screenshot did not decode inline');
    const ownOccurrence = await ownThumb.getAttribute('data-occurrence');
    await ownThumb.click();
    const viewer = page.locator('dialog.viewer[open]');
    await viewer.waitFor();
    assert(await imgLoaded(viewer.locator('img')), 'the full-size image did not decode');
    await page.locator('#toast.on').waitFor({ state: 'hidden' });
    await shot('att04-thread-success');
    await page.keyboard.press('Escape');
    await viewer.waitFor({ state: 'detached' });
    // Focus returns in the dialog's close event, a task after [open] goes; a
    // redraw meanwhile (a read receipt reloads the thread) replaces the
    // thumbnail, and the handler then focuses its replacement.
    await focusOccurrence(ownOccurrence);
    await ownThumb.click();
    await viewer.waitFor();
    await page.mouse.click(8, 450);
    await viewer.waitFor({ state: 'detached' });
    await focusOccurrence(ownOccurrence);
    await ownThumb.click();
    await viewer.waitFor();
    await viewer.getByRole('button', { name: 'Close', exact: true }).click();
    await viewer.waitFor({ state: 'detached' });
    await focusOccurrence(ownOccurrence);

    // Pin local browser time so the 90-minute retry always crosses midnight.
    // setFixedTime leaves timers running for page updates and locators.
    await page.clock.setFixedTime(await page.evaluate(() => new Date(2026, 10, 3, 23, 0, 0).getTime()));
    // A faked daily quota refusal on the reply chip names the retry time.
    const quota = route => route.fulfill({ status: 429, contentType: 'application/json', body: JSON.stringify({ error: { code: 'upload_quota_exceeded', message: 'daily upload quota exceeded', details: { retry_after_seconds: 5400 } } }) });
    await page.route(isUpload, quota);
    const quotaText = 'Logs from the second run attached.';
    await ta.fill(quotaText);
    const [chooser] = await Promise.all([page.waitForEvent('filechooser'), page.locator('.composer .attach').click()]);
    await chooser.setFiles({ name: 'second-run.log', mimeType: 'text/plain', buffer: Buffer.from('radio: finalize called\nradio: ok\n') });
    const failed = page.locator('.composer .up-chip.up-failed', { hasText: 'second-run.log' });
    await failed.waitFor();
    const errLine = await failed.locator('.up-err > span').textContent();
    assert(errLine === 'Daily upload quota reached; try again at 4 Nov 00:30.', `the quota error does not name the en-AU next-day retry time: ${errLine}`);
    await failed.getByRole('button', { name: 'Retry', exact: true }).waitFor();
    await failed.getByRole('button', { name: 'Remove', exact: true }).waitFor();
    await page.locator('.composer .send:disabled').waitFor();
    assert(await ta.inputValue() === quotaText, 'the text was lost on the quota error');
    await shot('att04-thread-error');
    await failed.getByRole('button', { name: 'Remove', exact: true }).click();
    await page.locator('.composer .send:enabled').waitFor();
    await page.unroute(isUpload, quota);

    // Hosted bytes that also name an informational origin_url show inline: the
    // operator's history page says which hashes the hub hosts. A reference-only
    // PNG (origin_url, nothing uploaded) stays a chip and is never fetched.
    const originUp = await upload(chart, 'image/png');
    const REF_ONLY = 'f'.repeat(64);
    const originText = 'The chart from the dashboard, and a link to the live one.';
    const originMsg = await api('POST', '/v1/messages', {
      idempotency_key: 'att-thread-origin', thread_id: thr, priority: 'hold', content: { text: originText },
      attachments: [
        { sha256: originUp.sha256, size: originUp.size, media_type: 'image/png', name: 'chart-hosted.png', origin_url: 'https://dash.example/chart.png' },
        { sha256: REF_ONLY, size: 2048, media_type: 'image/png', name: 'chart-live.png', origin_url: 'https://dash.example/live.png' },
      ],
    }, 'codex');
    const originEl = msgEl(originMsg.message_id);
    await originEl.locator('.txt', { hasText: originText }).waitFor();
    await demand(`[id="m-${originMsg.message_id}"] button.thumb[data-sha="${originUp.sha256}"]`);
    const originThumb = originEl.locator(`button.thumb[data-sha="${originUp.sha256}"] img`);
    await originThumb.waitFor();
    assert(await imgLoaded(originThumb), 'hosted bytes with an origin_url did not show inline');
    await originEl.locator('.att', { hasText: 'chart-live.png' }).waitFor();
    assert(await originEl.locator('button.thumb').count() === 1, 'the reference-only PNG rendered as an image');
    assert(blobGets(REF_ONLY) === 0, 'the reference-only PNG was fetched');

    // Review Focus 3: a file attached while a reply is in flight (picker, drop
    // or paste) is not in that request, so the reply's success must leave it.
    const heldText = 'Held reply with one log.';
    await ta.fill(heldText);
    {
      const [ch] = await Promise.all([page.waitForEvent('filechooser'), page.locator('.composer .attach').click()]);
      await ch.setFiles({ name: 'carried.log', mimeType: 'text/plain', buffer: Buffer.from('carried with the reply\n') });
    }
    await page.locator('.composer .up-chip.up-done', { hasText: 'carried.log' }).waitFor();
    const heldPosts = [];
    let releaseSend;
    const sendGate = new Promise(resolve => { releaseSend = resolve; });
    const holdSend = async route => { heldPosts.push(route.request().postDataJSON()); await sendGate; await route.continue(); };
    await page.route('**/v1/messages', holdSend);
    await Promise.all([
      page.waitForRequest(req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/messages'),
      page.locator('.composer .send').click(),
    ]);
    await page.waitForFunction(() => document.querySelector('.composer .send').disabled);
    {
      const [ch] = await Promise.all([page.waitForEvent('filechooser'), page.locator('.composer .attach').click()]);
      await ch.setFiles({ name: 'picked-later.log', mimeType: 'text/plain', buffer: Buffer.from('picked while sending\n') });
    }
    const dt = await page.evaluateHandle(() => { const d = new DataTransfer(); d.items.add(new File(['dropped while sending\n'], 'dropped-later.log', { type: 'text/plain' })); return d; });
    for (const ev of ['dragenter', 'dragover', 'drop']) await page.locator('.composer .wrap').dispatchEvent(ev, { dataTransfer: dt });
    await page.evaluate(bytes => {
      const d = new DataTransfer();
      d.items.add(new File([new Uint8Array(bytes)], 'image.png', { type: 'image/png' }));
      document.querySelector('.composer textarea').dispatchEvent(new ClipboardEvent('paste', { clipboardData: d, bubbles: true, cancelable: true }));
    }, [...terminal]);
    await page.waitForFunction(() => document.querySelectorAll('.composer .up-chip.up-done').length === 4);
    releaseSend();
    await page.locator('.msg .txt', { hasText: heldText }).waitFor();
    await page.unroute('**/v1/messages', holdSend);
    await page.locator('.composer .send:enabled').waitFor();
    assert(heldPosts.length === 1 && heldPosts[0].attachments.map(a => a.name).join() === 'carried.log', `the held reply carried ${JSON.stringify(heldPosts.map(p => p.attachments))}`);
    const left = await page.locator('.composer .up-chip .up-nm').allTextContents();
    assert(left.length === 3 && left[0] === 'picked-later.log' && left[1] === 'dropped-later.log' && /^pasted-\d{8}-\d{6}\.png$/.test(left[2]), `after the held reply the draft holds ${JSON.stringify(left)}`);
    assert(await page.locator('.composer .up-chip.up-done').count() === 3, 'a file added during the send lost its upload');
    for (let n = 3; n > 0; n--) await page.locator('.composer .up-chip .up-x').first().click();
    await page.waitForFunction(() => document.querySelectorAll('.composer .up-chip').length === 0);

    // Same bytes under a new name after a lost reply: a different body, so a
    // new key (the old one would be 409 idempotency_conflict); an unchanged
    // retry keeps the key and is the hub's replay.
    const sameBytes = Buffer.from('identical bytes, two names\n');
    const pickAs = async name => {
      const [ch] = await Promise.all([page.waitForEvent('filechooser'), page.locator('.composer .attach').click()]);
      await ch.setFiles({ name, mimeType: 'text/plain', buffer: sameBytes });
      await page.locator('.composer .up-chip.up-done', { hasText: name }).waitFor();
    };
    await ta.fill('');
    await pickAs('first-name.log');
    const renamePosts = [], renameReplies = [];
    let lostOnce = false;
    const loseFirst = async route => {
      renamePosts.push(route.request().postDataJSON());
      const r = await route.fetch();
      renameReplies.push(r.status());
      if (!lostOnce) { lostOnce = true; await route.abort('failed'); } else await route.fulfill({ response: r });
    };
    await page.route('**/v1/messages', loseFirst);
    await page.locator('.composer .send').click();
    await page.locator('.composer .cbanner', { hasText: 'network:' }).waitFor();
    await page.locator('.composer .up-chip .up-x').first().click();
    await pickAs('second-name.log');
    await page.locator('#toast.on').waitFor({ state: 'hidden' }); // the held reply's toast
    await Promise.all([page.locator('#toast.on', { hasText: 'Sent' }).waitFor(), page.locator('.composer .send').click()]);
    await page.waitForFunction(() => document.querySelectorAll('.composer .up-chip').length === 0);
    await page.unroute('**/v1/messages', loseFirst);
    assert(renamePosts.length === 2 && renamePosts[1].attachments[0].name === 'second-name.log', `renamed posts ${JSON.stringify(renamePosts.map(p => p.attachments))}`);
    assert(renamePosts[0].attachments[0].sha256 === renamePosts[1].attachments[0].sha256, 'the two names did not carry the same bytes');
    assert(renamePosts[1].idempotency_key !== renamePosts[0].idempotency_key, 'the same bytes under a new name kept the key');
    assert(renameReplies.join() === '202,202', `the hub answered ${renameReplies}, not two new messages`);
    await dt.dispose();
    assert(dialogs === 0, 'an attachment ran script');
    assert(downloads === 0, 'an attachment downloaded without an explicit original-download action');
    assert(blobGets(svgUp.sha256) === 0 && blobGets(nameUp.sha256) === 0 && blobGets(REF_ONLY) === 0, 'an ineligible attachment was fetched');
    assert(await page.locator('iframe, object, embed, a[href^="blob:"]').count() === 0, 'an attachment became embedded content');
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.locator('.composer').waitFor({ state: 'detached' });
    u = await urls();
    assert(u.live.length === 0 && !u.invalid && !u.detachViolations, 'final route exit leaked URLs or revoked attached sources');
  });
  console.log('Attachments thread passed: inline PNG, hostile PNG deferred error/Retry/Files/Cancel, SVG/text chips, exact occurrence focus, no refetch on redraw, URLs revoked on leave, near-miss drop, pasted reply sent once under one key, viewer, quota error, hosted+origin_url inline and reference-only chip, files added during a held reply kept, same bytes renamed mint a new key');
} finally { await cleanBuild(); }
