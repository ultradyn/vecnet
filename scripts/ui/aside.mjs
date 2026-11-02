// Task 10: the thread side panel (participants, files, budget, matrix button)
// in a real browser, with its named assertions and four shots.
import { readFile } from 'node:fs/promises';
import { assert, cleanBuild, withDemo } from './lib.mjs';

const LOG = 'hark-listen.log';
const LOG_BYTES = 'radio: received over\nradio: finalize missing\n';

// Waits for the toast to finish sliding in, shoots, and reports whether the
// same toast was still up afterwards: if it was up before and after, it is in
// the shot.
async function shotWithToast(page, shot, name, text) {
  await page.locator('#toast.on', { hasText: text }).waitFor();
  await page.evaluate(() => Promise.all(document.getElementById('toast').getAnimations().map(a => a.finished)));
  await shot(name);
  return page.evaluate(t => { const el = document.getElementById('toast'); return el.classList.contains('on') && el.textContent.includes(t); }, text);
}

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, allowConsole, countRequests } = ctx;
    // Chromium logs every non-2xx fetch as a console error: the views other
    // tasks fill in, and the purged blob this script provokes.
    allowConsole(/status of 501/);
    allowConsole(/status of 503/);
    allowConsole(/status of 410/);
    // Keeps interception on when hold() unroutes (see shell.mjs).
    await page.route('**/__keep-interception__', route => route.continue());

    const aside = page.locator('#aside');
    const fileBtn = name => aside.locator('button.file', { hasText: name });

    // Inbox first, for the chat column's width outside the thread layout.
    await page.locator('#conn.live').waitFor();
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    const inboxCol = await page.locator('#view').evaluate(el => el.getBoundingClientRect().width);

    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await aside.getByRole('heading', { name: 'Participants' }).waitFor();
    for (const name of ['codex-hark', 'grok-reviewer', 'pag-hark-sleuth', 'claw-ops']) await aside.locator('.pp', { hasText: name }).first().waitFor();
    await fileBtn(LOG).waitFor();
    await fileBtn('message.md').waitFor();
    await aside.locator('.budget', { hasText: /\d+ of 500 left/ }).waitFor();
    // Sections in order.
    const heads = await aside.locator('h3').allTextContents();
    assert(heads.map(t => t.replace(/\d+$/, '')).join() === 'Participants,Files,Thread', `sections ${heads}`);
    assert(await aside.locator('.matrix-btn').count() === 1, 'no delivery matrix button');
    // Spilled files are dashed; nothing is purged yet.
    // Found and measured in one page-side step: a redraw between resolving a
    // handle and reading its style would measure a detached node.
    assert(await aside.evaluate(root => {
      const el = [...root.querySelectorAll('button.file')].find(b => b.textContent.includes('message.md'));
      return !!el && el.classList.contains('spill') && getComputedStyle(el).borderTopStyle === 'dashed';
    }), 'spilled message.md is not dashed');
    assert(await aside.locator('button.file.purged').count() === 0, 'a live file reads purged');

    // Presence and reachability are two signals; unknown presence never reads offline.
    const peers = await aside.locator('.pp').evaluateAll(rows => rows.map(r => ({
      text: r.textContent, dot: r.querySelector('.dot').className, ring: r.querySelector('.sig').className,
    })));
    assert(peers.every(p => /\br-(connected|reachable|unreachable|unknown)\b/.test(p.ring)), `rings ${JSON.stringify(peers)}`);
    const unknown = peers.filter(p => !/\b(idle|working|blocked|offline)\b/.test(p.dot));
    assert(unknown.length > 0, 'the demo has no participant with unknown presence');
    for (const p of unknown) assert(!/offline/.test(p.text) && !/\boffline\b/.test(p.dot), `unknown presence read offline: ${p.text}`);
    assert(await page.locator('img').count() === 0, 'a peer string became an element');

    // Named: side panel is ≥ 360 px at 1440 wide and the chat column widens.
    const asideW = await aside.evaluate(el => el.getBoundingClientRect().width);
    const threadCol = await page.locator('#view').evaluate(el => el.getBoundingClientRect().width);
    assert(asideW >= 360, `side panel is ${asideW} px at 1440 wide`);
    assert(threadCol > inboxCol, `chat column ${threadCol} px did not widen from ${inboxCol} px`);
    await page.locator('.msg').first().waitFor();
    await shot('10-aside-initial');

    // Named: file download fires a browser download and no request carries the
    // secret in a URL. Held at the blob first, for the progress shot.
    await page.evaluate(() => {
      const create = URL.createObjectURL.bind(URL);
      window.__downloadTypes = [];
      URL.createObjectURL = blob => { window.__downloadTypes.push(blob.type); return create(blob); };
      const revoke = URL.revokeObjectURL.bind(URL);
      window.__revoked = [];
      URL.revokeObjectURL = url => { window.__revoked.push(url); revoke(url); };
    });
    const release = await hold('**/v1/blobs/**');
    const downloaded = page.waitForEvent('download');
    await fileBtn(LOG).click();
    await aside.locator('button.file.busy', { hasText: 'downloading…' }).waitFor();
    assert(await fileBtn(LOG).isDisabled(), 'a held download left its button live');
    await shot('10-aside-progress');
    await release();
    const dl = await downloaded;
    assert(dl.suggestedFilename() === LOG, `saved as ${dl.suggestedFilename()}`);
    assert(dl.url().startsWith('blob:'), `download url ${dl.url()}`);
    assert(await readFile(await dl.path(), 'utf8') === LOG_BYTES, 'downloaded bytes differ from the blob');
    const downloadTypes = await page.evaluate(() => window.__downloadTypes);
    assert(downloadTypes.length === 1 && downloadTypes[0] === 'application/octet-stream', `Files-panel download MIME types: ${JSON.stringify(downloadTypes)}`);
    await aside.locator('button.file.busy').waitFor({ state: 'detached' });
    await page.waitForFunction(() => !document.querySelector('a[download]'));
    // The object URL is revoked afterwards.
    await page.waitForFunction(url => window.__revoked.includes(url), dl.url());
    assert(countRequests(req => req.url().includes('/v1/blobs/') && req.headers().authorization === `Bearer ${demo.operator_secret}`) >= 1, 'blob fetch did not carry the bearer header');
    assert(countRequests(req => req.url().includes(demo.operator_secret)) === 0, 'a request carried the secret in its URL');
    assert(countRequests(req => /[?&]t=/.test(req.url()) && req.url().includes('/v1/blobs/')) === 0, 'the operator UI used a blob token URL');

    // Success shot with the toast in frame: a toast that left during the shot
    // gets another download.
    let ok = await shotWithToast(page, shot, '10-aside-success', `Downloaded ${LOG}`);
    for (let i = 0; !ok && i < 3; i++) {
      await page.locator('#toast.on').waitFor({ state: 'hidden' });
      const again = page.waitForEvent('download');
      await fileBtn(LOG).click();
      await again;
      ok = await shotWithToast(page, shot, '10-aside-success', `Downloaded ${LOG}`);
    }
    assert(ok, 'success toast never stayed up for the shot');

    // Named: matrix button navigates to #matrix/<id>.
    await aside.locator('.matrix-btn').click();
    await page.waitForFunction(id => location.hash === `#matrix/${id}`, demo.threads.hark);
    // The hash changes before its hashchange handler runs, so wait for the
    // router to have handled it (the matrix dialog it opens) before reading the body.
    await page.locator('dialog.mx[open]').waitFor();
    assert(await page.evaluate(() => document.body.dataset.route) === 'matrix', 'matrix route not on body');
    assert(await aside.evaluate(el => el.getBoundingClientRect().width) >= 360, 'panel narrowed on the matrix route');
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await page.waitForFunction(() => document.body.dataset.route === 'thread');

    // Named: at 1000 wide the panel is behind its toggle.
    await page.setViewportSize({ width: 1000, height: 900 });
    const toggle = aside.locator('.aside-toggle');
    await toggle.waitFor({ state: 'visible' });
    await aside.locator('.aside-body').waitFor({ state: 'hidden' });
    assert(!(await aside.getByRole('heading', { name: 'Participants' }).isVisible()), 'panel visible at 1000 wide without the toggle');
    assert(await toggle.getAttribute('aria-expanded') === 'false', 'toggle reads expanded while closed');
    await toggle.click();
    await aside.getByRole('heading', { name: 'Participants' }).waitFor({ state: 'visible' });
    assert(await toggle.getAttribute('aria-expanded') === 'true', 'toggle does not read expanded');
    await fileBtn(LOG).waitFor({ state: 'visible' });
    await toggle.click();
    await aside.locator('.aside-body').waitFor({ state: 'hidden' });
    await page.setViewportSize({ width: 1440, height: 900 });
    await aside.getByRole('heading', { name: 'Participants' }).waitFor({ state: 'visible' });
    assert(await toggle.isHidden(), 'toggle shows at 1440 wide');

    // Error: the hub purges the blob while the list is stale. The click gets
    // 410 blob_purged, toasts, reloads, and the file reads purged and disabled.
    const sha = await fileBtn(LOG).getAttribute('data-sha');
    const purge = await asPeer('operator', 'POST', `/v1/admin/blobs/${sha}/purge`);
    assert(purge.status === 204, `purge returned ${purge.status}`);
    assert(await fileBtn(LOG).isEnabled(), 'the list refreshed before the click; nothing is stale');
    let late = 0;
    page.on('download', () => { late++; });
    await fileBtn(LOG).click();
    await page.locator('#toast.on.bad', { hasText: 'purged' }).waitFor();
    const purged = aside.locator('button.file.purged', { hasText: LOG });
    await purged.waitFor();
    assert(await purged.isDisabled(), 'purged file is clickable');
    assert(await purged.locator('.tag.purged', { hasText: 'purged' }).count() === 1, 'purged file is not labelled');
    assert(late === 0, 'a purged file started a download');
    await page.evaluate(() => Promise.all(document.getElementById('toast').getAnimations().map(a => a.finished)));
    await shot('10-aside-error');
    assert(await page.locator('#toast.on.bad').count() === 1, 'error toast left during the shot');
    assert(countRequests(req => req.url().includes(demo.operator_secret)) === 0, 'a request carried the secret in its URL');

    // A quiet participant's presence crosses its TTL with the thread open: no
    // event says so, and the shell's minute directory reload must show it as
    // unknown (PROTOCOL §8), not the admin snapshot's fresh-looking state.
    const fresh = aside.locator('.pp').filter({ has: page.locator('.dot.idle, .dot.working, .dot.blocked') }).first();
    const freshId = await fresh.getAttribute('data-peer');
    assert(freshId, 'the demo has no participant with a fresh presence');
    await ctx.advance('2h');
    await aside.locator(`.pp[data-peer="${freshId}"] .dot.unknown`).waitFor({ timeout: 90000 });
  });
  console.log('Aside passed: download without the secret in a URL, ≥ 360 px panel and wider chat at 1440, toggle at 1000, matrix button, purged 410');
} finally { await cleanBuild(); }
