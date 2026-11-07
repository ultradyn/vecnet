// Peer avatars (PROTOCOL §8.1) in a real browser. Three peers upload images
// (a grokbot icon, a pag-chat magnifier, a dot's pet), each a different
// non-square size (the dot's set by the operator); the others keep their
// letters. Checks: the image replaces
// the letter at the same size and shape, each hash is fetched once, with the
// bearer in the Authorization header and never in a URL, shown through a
// blob: object URL; a cleared avatar falls back to the letter live; a failed
// fetch, undecodable bytes or a fetch still in flight all show the letter.
// Shots: a thread with mixed avatars, the inbox, New message's peer list,
// and the fallback.
import { assert, cleanBuild, png, withDemo } from './lib.mjs';

const isAvatar = url => /^\/v1\/peers\/[^/]+\/avatar$/.test(new URL(url).pathname);

// Pixel-art fixtures, drawn at run time (no binaries are committed). Each is
// off-square with its subject centred, so the hub's center-crop keeps it.
const inBox = (x, y, x0, y0, x1, y1) => x >= x0 && x < x1 && y >= y0 && y < y1;
const near = (x, y, cx, cy, r) => (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
function grokIcon() { // 200×160: a robot head on slate
  const cx = 100, cy = 84;
  return png(200, 160, (x, y) => {
    if (inBox(x, y, cx - 3, cy - 64, cx + 3, cy - 44) || near(x, y, cx, cy - 66, 7)) return [251, 191, 36];
    if (inBox(x, y, cx - 50, cy - 44, cx + 50, cy + 48)) {
      if (near(x, y, cx - 22, cy - 6, 12) || near(x, y, cx + 22, cy - 6, 12)) return [14, 165, 233];
      if (inBox(x, y, cx - 26, cy + 22, cx + 26, cy + 30)) return [31, 41, 55];
      return [229, 231, 235];
    }
    return [30, 41, 59];
  });
}
function sleuthIcon() { // 150×220: a magnifier on teal
  const cx = 70, cy = 100;
  return png(150, 220, (x, y) => {
    const d = Math.hypot(x - cx, y - cy);
    if (d >= 30 && d <= 42) return [240, 253, 250];
    if (d < 30) return [94, 234, 212];
    const t = (x - cx) + (y - cy);
    if (x > cx + 26 && y > cy + 26 && Math.abs((x - cx) - (y - cy)) < 10 && t < 140) return [251, 146, 60];
    return [15, 118, 110];
  });
}
function dotPet() { // 260×180: a cat face on butter
  const cx = 130, cy = 98;
  return png(260, 180, (x, y) => {
    const ear = (ex, dir) => y < cy - 20 && y > cy - 72 && Math.abs(x - ex) < (y - (cy - 72)) * 0.55 && (x - ex) * dir < 22;
    if (near(x, y, cx - 18, cy - 4, 7) || near(x, y, cx + 18, cy - 4, 7)) return [17, 24, 39];
    if (near(x, y, cx, cy + 14, 5)) return [244, 114, 182];
    if (near(x, y, cx, cy, 52) || ear(cx - 34, -1) || ear(cx + 34, 1)) return [249, 115, 22];
    return [253, 230, 138];
  });
}

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, allowConsole } = ctx;
    // Chromium logs each non-2xx fetch: the avatar 500 the fallback provokes.
    allowConsole(/status of 500/);
    const avatarReqs = [], allUrls = [];
    page.on('request', req => {
      allUrls.push(req.url());
      if (isAvatar(req.url())) avatarReqs.push({ url: req.url(), auth: req.headers().authorization || '' });
    });
    // A peer with a bearer sets its own; a dot has none here, so the operator
    // sets its avatar through the admin route.
    const avatarPath = name => demo.peers[name].secret ? '/v1/peers/me/avatar' : `/v1/admin/peers/${demo.peers[name].id}/avatar`;
    const bearerFor = name => demo.peers[name].secret || demo.operator_secret;
    const putImage = async (name, bytes) => {
      const r = await fetch(demo.url + avatarPath(name), { method: 'PUT', headers: { Authorization: `Bearer ${bearerFor(name)}`, 'Content-Type': 'image/png' }, body: bytes });
      const body = await r.json();
      assert(r.status === 200 && /^[0-9a-f]{64}$/.test(body.avatar), `${name} avatar PUT: ${r.status} ${JSON.stringify(body)}`);
      return body.avatar;
    };
    let serial = 0;
    const send = async (peer, text) => {
      const r = await asPeer(peer, 'POST', '/v1/messages', { idempotency_key: `avatar-${++serial}`, thread_id: demo.threads.hark, priority: 'hold', content: { text } });
      assert(r.status === 202, `${peer} send: ${r.status} ${await r.text()}`);
    };
    const pics = () => page.locator('.av.pic img').evaluateAll(imgs => imgs.map(i => ({ src: i.getAttribute('src'), w: i.naturalWidth, h: i.naturalHeight, box: i.parentElement.getBoundingClientRect().width })));

    const hashes = { grok: await putImage('grok', grokIcon()), sleuth: await putImage('sleuth', sleuthIcon()), dot: await putImage('dot', dotPet()) };
    assert(new Set(Object.values(hashes)).size === 3, 'distinct images share a hash');
    // The second grokbot uses the same icon: one stored row, one fetch.
    assert(await putImage('triage', grokIcon()) === hashes.grok, 'the same image hashed differently');
    await send('sleuth', 'Traced it: the end phrase never reaches finalize.');
    await send('grok', 'Reviewed the classifier diff: the finalize path looks right now.');

    // A thread with mixed image and letter avatars.
    await page.locator('#conn.live').waitFor();
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    const grokMsg = page.locator('.msg', { hasText: 'Reviewed the classifier diff' });
    // Images are inserted only after their individual downloads finish.
    // Checking the images already present can miss a peer still loading.
    const sleuthMsg = page.locator('.msg', { hasText: 'Traced it: the end phrase' });
    await Promise.all([grokMsg, sleuthMsg].map(msg => msg.locator('.av.pic img').waitFor()));
    await page.locator('.msg .av:not(.pic)').first().waitFor(); // codex keeps its letter
    await page.waitForFunction(() => [...document.querySelectorAll('.av.pic img')].every(i => i.complete && i.naturalWidth > 0));
    const shown = await pics();
    assert(shown.length >= 2, `only ${shown.length} avatar images in the thread`);
    for (const p of shown) {
      assert(p.src.startsWith('blob:'), `avatar src is not an object URL: ${p.src}`);
      assert(p.w === 128 && p.h === 128, `stored avatar is ${p.w}x${p.h}, want 128x128`);
    }
    // Same box and shape as the letter beside it.
    const [img, letter] = await Promise.all([
      grokMsg.locator('.av.pic').evaluate(el => { const r = el.getBoundingClientRect(), s = getComputedStyle(el); return [r.width, r.height, s.borderRadius]; }),
      page.locator('.msg .av:not(.pic)').first().evaluate(el => { const r = el.getBoundingClientRect(), s = getComputedStyle(el); return [r.width, r.height, s.borderRadius]; }),
    ]);
    assert(JSON.stringify(img) === JSON.stringify(letter), `image avatar box ${img} differs from the letter's ${letter}`);
    await page.evaluate(() => document.getElementById('main').scrollTo(0, 1e9));
    await shot('av-thread-mixed');

    // Redraws reuse the object URL: one fetch per hash, bearer in the header.
    const fetchesBefore = avatarReqs.length;
    await send('sleuth', 'Confirmed on the second radio as well.');
    await page.locator('.msg', { hasText: 'Confirmed on the second radio' }).locator('.av.pic img').waitFor();
    assert(avatarReqs.length === fetchesBefore, `a redraw fetched ${avatarReqs.length - fetchesBefore} avatars again`);

    // The inbox and New message's peer list use the same component.
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.locator('#view .av.pic img').first().waitFor(); // grok-triage's dead letters
    await page.waitForFunction(() => [...document.querySelectorAll('.av.pic img')].every(i => i.complete && i.naturalWidth > 0));
    await shot('av-inbox');
    await page.evaluate(() => { location.hash = '#new'; });
    await page.locator('.compose .opt', { hasText: 'dot-research' }).locator('.av.pic img').waitFor();
    await page.locator('.compose .opt', { hasText: 'codex-hark' }).locator('.av:not(.pic)').waitFor();
    await page.waitForFunction(() => [...document.querySelectorAll('.av.pic img')].every(i => i.complete && i.naturalWidth > 0));
    await shot('av-compose');

    const byHash = new Map();
    for (const r of avatarReqs) byHash.set(r.url, (byHash.get(r.url) || 0) + 1);
    for (const [url, n] of byHash) assert(n === 1, `${url} fetched ${n} times`);
    const grokFetches = avatarReqs.filter(r => r.url.includes(demo.peers.grok.id) || r.url.includes(demo.peers.triage.id)).length;
    assert(grokFetches === 1, `two grokbots with one image made ${grokFetches} fetches`);
    assert(avatarReqs.every(r => r.auth === `Bearer ${demo.operator_secret}`), 'an avatar fetch went without the bearer header');
    assert(!allUrls.some(u => u.includes(demo.operator_secret)), 'the bearer appeared in a URL');

    // Clearing an avatar reaches the page through directory_updated: the letter returns.
    const cleared = await fetch(demo.url + avatarPath('dot'), { method: 'DELETE', headers: { Authorization: `Bearer ${bearerFor('dot')}` } });
    assert(cleared.status === 204, `dot clear: ${cleared.status}`);
    await page.locator('.compose .opt', { hasText: 'dot-research' }).locator('.av:not(.pic)').waitFor();
    await putImage('dot', dotPet());

    // Fallback: a 500, bytes the browser cannot decode, and a fetch that never
    // answers all leave the letter in place.
    const ids = Object.fromEntries(Object.keys(hashes).map(k => [k, demo.peers[k].id]));
    const failed = [];
    await page.route('**/v1/peers/*/avatar', async route => {
      const id = new URL(route.request().url()).pathname.split('/')[3];
      failed.push(id);
      if (id === ids.grok) return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'internal', message: 'Demo failure', details: {} } }) });
      if (id === ids.sleuth) return route.fulfill({ status: 200, contentType: 'image/png', body: Buffer.from('\x89PNG\r\n\x1a\nnot really a png') });
      // dot: never answers while the shot is taken.
    });
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await page.reload({ waitUntil: 'load' });
    await page.locator('.msg', { hasText: 'Reviewed the classifier diff' }).locator('.av:not(.pic)').waitFor();
    await page.waitForFunction(() => document.querySelectorAll('.msg .av').length > 0 && !document.querySelector('.av.pic'));
    await page.waitForTimeout(300); // let the undecodable image's error event land
    assert(await page.locator('.av.pic').count() === 0, 'a failed avatar still shows an image');
    assert(failed.length >= 2, `the fallback page made ${failed.length} avatar fetches`);
    const grokLetter = await page.locator('.msg', { hasText: 'Reviewed the classifier diff' }).locator('.av').textContent();
    assert(grokLetter === 'G', `grok's fallback letter is ${JSON.stringify(grokLetter)}`);
    await page.evaluate(() => document.getElementById('main').scrollTo(0, 1e9));
    await shot('av-fallback');
    await page.unroute('**/v1/peers/*/avatar');
  });
  console.log('Avatars passed: images replace letters at the same size, one bearer fetch per hash via object URLs, live clear, letter fallback on failure');
} finally { await cleanBuild(); }
