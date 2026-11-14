// Attachments Task 4: reply attachments and inline images in the thread, in a
// real browser. PNG and SVG (rasterized off-DOM) show inline; an HTML file
// typed as PNG and a bidi-named file stay chips. The operator pastes a screenshot, the
// reply is lost and retried under the same key, and opens the image full size.
import { assert, cleanBuild, hex, png, withDemo } from './lib.mjs';

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
const VIEWBOX_SVG = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 120"><rect width="240" height="120" rx="16" fill="#1a2139"/><circle cx="60" cy="60" r="36" fill="#34d399"/><path d="M120 40h80M120 60h60M120 80h80" stroke="#a78bfa" stroke-width="8"/></svg>';
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
    let dialogs = 0;
    page.on('dialog', d => { dialogs++; d.dismiss().catch(() => {}); });

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
    const urls = () => page.evaluate(() => window.__urls);
    const imgLoaded = loc => loc.evaluate(img => img.complete ? img.naturalWidth > 0 : new Promise(resolve => {
      img.addEventListener('load', () => resolve(img.naturalWidth > 0), { once: true });
      img.addEventListener('error', () => resolve(false), { once: true });
    }));

    await page.locator('#threads a', { hasText: 'Ticket 18' }).waitFor();
    await page.locator('#conn.live').waitFor();
    // Watch every object URL the page makes and revokes.
    await page.evaluate(() => {
      const make = URL.createObjectURL.bind(URL), drop = URL.revokeObjectURL.bind(URL);
      window.__urls = { made: [], revoked: [] };
      window.__urlBlobs = new Map();
      window.__domImageSources = [];
      const record = el => { if (el.matches?.('img[src^="blob:"]')) window.__domImageSources.push(el.src); };
      new MutationObserver(records => {
        for (const r of records) {
          if (r.type === 'attributes') record(r.target);
          for (const node of r.addedNodes) {
            record(node);
            node.querySelectorAll?.('img[src^="blob:"]').forEach(record);
          }
        }
      }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['src'] });
      URL.createObjectURL = b => { const u = make(b); window.__urlBlobs.set(u, b); window.__urls.made.push({ url: u, type: b.type, size: b.size }); return u; };
      URL.revokeObjectURL = u => { window.__urls.revoked.push(u); drop(u); };
      window.__noReload = true;
    });

    // The peer's message: a PNG, two SVGs, and two chip fallbacks.
    const pngUp = await upload(terminal, 'image/png');
    const svgUp = await upload(SVG, 'image/svg+xml');
    const viewboxUp = await upload(VIEWBOX_SVG, 'image/svg+xml');
    const fakeUp = await upload(FAKE_PNG, 'image/png');
    const nameUp = await upload('radio: received over\nradio: finalize skipped\n', 'text/plain');
    const peerText = 'Screenshot of the failing run, plus what the bot attached.';
    const peerMsg = await api('POST', '/v1/messages', {
      idempotency_key: 'att-thread-peer', thread_id: thr, priority: 'wake', content: { text: peerText },
      attachments: [
        { sha256: pngUp.sha256, size: pngUp.size, media_type: 'image/png', name: 'run-failure.png' },
        { sha256: svgUp.sha256, size: svgUp.size, media_type: 'image/svg+xml', name: 'diagram.svg' },
        { sha256: viewboxUp.sha256, size: viewboxUp.size, media_type: 'image/svg+xml', name: 'viewbox-only.svg' },
        { sha256: fakeUp.sha256, size: fakeUp.size, media_type: 'image/png', name: 'preview.png' },
        { sha256: nameUp.sha256, size: nameUp.size, media_type: 'text/plain', name: HOSTILE_NAME },
      ],
    }, 'codex');

    await page.evaluate(id => { location.hash = `#thread/${id}`; }, thr);
    const peer = msgEl(peerMsg.message_id);
    await peer.locator('.txt', { hasText: peerText }).waitFor();
    // The fake PNG is briefly an <img> too, until its decode error: pick the real one by hash.
    const thumb = peer.locator(`button.thumb[data-sha="${pngUp.sha256}"] img`);
    await thumb.waitFor();
    assert(await imgLoaded(thumb), 'the peer PNG did not decode inline');
    assert(await thumb.getAttribute('alt') === 'run-failure.png', 'the thumbnail alt is not the cleaned name');
    // Both SVGs show only a newly encoded PNG in the DOM, never an SVG URL.
    // connect-src 'self' forbids fetching blob: URLs, so inspect the Blob
    // captured at createObjectURL rather than provoking a CSP violation.
    for (const up of [svgUp, viewboxUp]) {
      const img = peer.locator(`button.thumb[data-sha="${up.sha256}"] img`);
      await img.waitFor();
      assert(await imgLoaded(img), 'SVG thumbnail did not decode');
      const info = await img.evaluate(async el => ({ src: el.src, type: window.__urlBlobs.get(el.src)?.type, w: el.naturalWidth, h: el.naturalHeight }));
      assert(info.src.startsWith('blob:') && info.type === 'image/png', 'SVG reached the DOM without rasterization: ' + JSON.stringify(info));
      assert(info.w > 0 && info.h > 0 && Math.max(info.w, info.h) >= 480 && Math.max(info.w, info.h) <= 2048, 'SVG raster dimensions not bounded: ' + JSON.stringify(info));
      assert(blobGets(up.sha256) === 1, 'SVG was not fetched exactly once');
    }
    const svgThumb = peer.locator(`button.thumb[data-sha="${svgUp.sha256}"]`);
    await svgThumb.click();
    const svgViewer = page.locator('dialog.viewer[open]');
    await svgViewer.waitFor();
    assert(await imgLoaded(svgViewer.locator('img')), 'SVG viewer did not decode');
    assert(await svgViewer.locator('img').evaluate(async el => window.__urlBlobs.get(el.src)?.type) === 'image/png', 'viewer exposed SVG bytes');
    await shot('att04-svg-viewer');
    await page.keyboard.press('Escape');
    await svgViewer.waitFor({ state: 'detached' });
    // HTML typed as PNG still fails decoding and falls back.
    await peer.locator('.att', { hasText: 'preview.png' }).waitFor();
    await peer.locator(`button.thumb[data-sha="${fakeUp.sha256}"]`).waitFor({ state: 'detached' });
    const named = peer.locator('.att', { hasText: 'gnp.exe' });
    await named.waitFor();
    assert(await peer.locator('button.thumb').count() === 3, 'PNG and both SVGs should be thumbnails');
    assert(await peer.locator('.att').count() === 2, 'fake PNG and text should remain chips');
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
    // Transient SVG URLs are private to detached Images and already revoked.
    // All DOM image sources are raster URLs; fake PNG is revoked on decode error.
    await page.waitForFunction(n => window.__urls.revoked.length >= n, 3);
    let u = await urls();
    assert(u.made.filter(m => m.type === 'image/svg+xml').length === 2, 'expected two transient SVG URLs');
    assert(u.made.filter(m => m.type === 'image/svg+xml').every(m => u.revoked.includes(m.url)), 'SVG URL survived rasterization');
    const imageSources = await page.locator('img[src^="blob:"]').evaluateAll(els => els.map(el => el.src));
    assert(imageSources.every(src => u.made.some(m => m.url === src && ALLOWED.includes(m.type))), 'a DOM image exposed an SVG URL');
    assert(u.made.length === 6 && u.revoked.length === 3, `made ${u.made.length} object URLs and revoked ${u.revoked.length}`);
    assert(dialogs === 0, 'an attachment ran script');
    await shot('att04-thread-initial');

    // Redraws read the cache: a new message does not fetch again or leak URLs.
    const pngGets = blobGets(pngUp.sha256);
    assert(pngGets === 1, `the PNG was fetched ${pngGets} times`);
    const nudge = 'Rerunning with the fix now.';
    await api('POST', '/v1/messages', { idempotency_key: 'att-thread-nudge', thread_id: thr, priority: 'hold', content: { text: nudge } }, 'codex');
    await page.locator('.msg .txt', { hasText: nudge }).waitFor();
    await thumb.waitFor();
    assert(blobGets(pngUp.sha256) === 1, 'a redraw fetched the thumbnail again');
    assert((await urls()).made.length === 6, 'a redraw made a new object URL');

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
    assert(u.made.every(m => u.revoked.includes(m.url)), 'leaving the thread left an object URL alive');
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, thr);
    await thumb.waitFor();
    assert(await imgLoaded(thumb), 'the thumbnail did not come back');
    assert(blobGets(pngUp.sha256) === 2, 'returning to the thread did not fetch the thumbnail again');

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
    await ownThumb.locator('img').waitFor();
    assert(await imgLoaded(ownThumb.locator('img')), 'the pasted screenshot did not decode inline');
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
    await page.waitForFunction(sha => document.activeElement && document.activeElement.matches(`button.thumb[data-sha="${sha}"]`), sentAtt.sha256, { timeout: 5000 }).catch(() => {});
    const focusInfo = await page.evaluate(sha => { const a = document.activeElement; return { ok: !!a && a.matches(`button.thumb[data-sha="${sha}"]`), tag: a && a.tagName, cls: a && a.className, n: document.querySelectorAll('button.thumb').length }; }, sentAtt.sha256);
    assert(focusInfo.ok, 'focus did not return to the thumbnail: ' + JSON.stringify(focusInfo));
    await ownThumb.click();
    await viewer.waitFor();
    await page.mouse.click(8, 450);
    await viewer.waitFor({ state: 'detached' });

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
    await page.locator('.composer .send').click();
    await page.waitForFunction(() => document.querySelector('.composer .send').disabled);
    while (!heldPosts.length) await new Promise(r => setTimeout(r, 20));
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
    // Exercise the browser conversion's failure/timeout cleanup as well as
    // the cache's unit-tested chip fallback. No raw SVG URL may survive.
    const conversion = await page.evaluate(async svg => {
      const { rasterizeSVG } = await import('/ui/thumbs.js');
      const blob = new Blob([svg], { type: 'image/svg+xml' });
      const toBlob = HTMLCanvasElement.prototype.toBlob, decode = Image.prototype.decode;
      const failures = [];
      try {
        for (const mode of ['invalid', 'null', 'security', 'timeout']) {
          HTMLCanvasElement.prototype.toBlob = mode === 'null' ? function (cb) { cb(null); }
            : mode === 'security' ? function () { throw new DOMException('tainted canvas', 'SecurityError'); } : toBlob;
          Image.prototype.decode = mode === 'timeout' ? () => new Promise(() => {}) : decode;
          try {
            await rasterizeSVG(mode === 'invalid' ? new Blob(['not SVG'], { type: 'image/svg+xml' }) : blob, 100);
            failures.push(false);
          } catch { failures.push(true); }
        }
      } finally { HTMLCanvasElement.prototype.toBlob = toBlob; Image.prototype.decode = decode; }
      const large = await rasterizeSVG(new Blob(['<svg xmlns="http://www.w3.org/2000/svg" width="8192" height="4096"><rect width="8192" height="4096" fill="red"/></svg>'], { type: 'image/svg+xml' }));
      const bitmap = await createImageBitmap(large);
      const size = [bitmap.width, bitmap.height];
      bitmap.close();
      return { failures, size, leaked: window.__urls.made.filter(m => m.type === 'image/svg+xml' && !window.__urls.revoked.includes(m.url)).length };
    }, SVG);
    assert(conversion.failures.every(Boolean), 'a failing SVG conversion did not reject: ' + JSON.stringify(conversion));
    assert(conversion.leaked === 0, 'failed/timed out SVG conversion leaked a private URL');
    assert(conversion.size.join() === '2048,1024', 'large SVG was not bounded with aspect preserved');
    assert(dialogs === 0, 'an attachment ran script');
    assert(await page.evaluate(() => window.__domImageSources.length > 0 && window.__domImageSources.every(src => window.__urlBlobs.get(src)?.type !== 'image/svg+xml')), 'an SVG object URL ever entered the DOM');
  });
  console.log('Attachments thread passed: inline PNG and rasterized hostile/viewBox-only SVGs, SVG viewer is PNG, fake PNG/bidi name as chips, no refetch on redraw, URLs revoked on leave, near-miss drop, pasted reply sent once under one key, viewer, quota error, hosted+origin_url inline and reference-only chip, files added during a held reply kept, same bytes renamed mint a new key');
} finally { await cleanBuild(); }
