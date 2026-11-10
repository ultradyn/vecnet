// The thread composer's control row at every width (Max, 2026-11-05: "the send
// button is on a new line"; "the small description text should collapse into
// 2 lines before anything moves onto different lines … make sure it looks good
// all the time"). Sweeps the viewport from 320 to 1600 px, short and spilling
// text, and asserts: Attach and Send share one row inside the composer, nothing
// overflows the bar, and the hint is either hidden or at most two unclipped lines.
import { assert, shotPath, withDemo } from './lib.mjs';

await withDemo({}, async ({ page, shot, demo, asPeer }) => {
  const sent = await asPeer('operator', 'POST', '/v1/messages', {
    idempotency_key: 'composer-fit-1', recipients: [demo.peers.codex.id], priority: 'hold', thread_title: 'Composer layout',
    content: { text: 'Checking the composer row at every width.' },
  });
  assert(sent.ok, `send ${sent.status}`);
  const { thread_id } = await sent.json();
  await page.locator('#conn.live').waitFor();
  await page.evaluate(id => { location.hash = `#thread/${id}`; }, thread_id);
  const bar = page.locator('.composer .bar');
  await bar.waitFor();

  const measure = () => page.evaluate(() => {
    const r = el => el.getBoundingClientRect();
    const bar = document.querySelector('.composer .bar');
    const q = s => bar.querySelector(s);
    const b = r(bar), send = r(q('.send')), attach = r(q('.attach')), bytes = q('.bytes'), hint = q('.hint');
    const shown = getComputedStyle(hint).display !== 'none';
    const cs = getComputedStyle(hint), lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.2;
    return {
      wrap: getComputedStyle(bar).flexWrap === 'wrap',
      bar: { left: b.left, right: b.right, top: b.top, bottom: b.bottom },
      send: { left: send.left, right: send.right, top: send.top, bottom: send.bottom },
      attach: { left: attach.left, right: attach.right, top: attach.top },
      kids: [...bar.children].filter(k => getComputedStyle(k).display !== 'none').map(k => ({ cls: k.className, left: r(k).left, right: r(k).right, top: r(k).top, bottom: r(k).bottom })),
      bytesClipped: bytes.scrollWidth > bytes.clientWidth + 1, bytesText: bytes.textContent,
      hint: shown ? { lines: Math.round(hint.scrollHeight / lh), clippedX: [...hint.children].some(s => r(s).right > r(hint).right + 1 || r(s).left < r(hint).left - 1) } : null,
    };
  });

  const check = (m, label) => {
    // One row: every control's vertical centre sits on Send's. Only at phone width,
    // after the hint has gone, does the priority control take a full-width row above.
    const mid = x => (x.top + x.bottom) / 2, row = mid(m.send);
    for (const k of m.kids) {
      if (m.wrap && k.cls === 'ptips') {
        assert(!m.hint, `${label}: the row wrapped while the hint is still shown`);
        assert(k.bottom <= m.send.top && Math.abs(k.left - m.bar.left) <= 1 && Math.abs(k.right - m.bar.right) <= 1, `${label}: phone priority row is not full width above Send`);
        continue;
      }
      assert(Math.abs(mid(k) - row) <= 3, `${label}: ${k.cls} is not on Send's row (centre ${mid(k)} vs ${row})`);
    }
    assert(Math.abs(m.send.right - m.bar.right) <= 1, `${label}: Send is not at the right edge`);
    assert(m.attach.right <= m.send.left, `${label}: Attach overlaps Send`);
    for (const k of m.kids) {
      assert(k.left >= m.bar.left - 1 && k.right <= m.bar.right + 1, `${label}: ${k.cls} [${k.left},${k.right}] leaves the bar [${m.bar.left},${m.bar.right}]`);
      assert(k.top >= m.bar.top - 1 && k.bottom <= m.bar.bottom + 1, `${label}: ${k.cls} leaves the bar vertically`);
    }
    if (m.hint) {
      assert(m.hint.lines <= 2, `${label}: hint wraps to ${m.hint.lines} lines`);
      assert(!m.hint.clippedX, `${label}: a hint segment is clipped`);
    }
  };

  const widths = [];
  for (let w = 320; w <= 1600; w += 20) widths.push(w);
  const ta = page.locator('.composer textarea');
  const summary = [];
  for (const [state, text] of [['short', ''], ['spill', 'x'.repeat(9000)]]) {
    await ta.fill(text);
    for (const w of widths) {
      await page.setViewportSize({ width: w, height: 900 });
      await page.waitForFunction(() => document.querySelector('.composer .bar').getBoundingClientRect().width > 0);
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))); // fitHint runs on a frame
      const m = await measure();
      check(m, `${state} @${w}px`);
      summary.push(`${state}@${w}: hint=${m.hint ? m.hint.lines + 'L' : 'hidden'} bytes=${m.bytesClipped ? 'ellipsized' : 'full'}`);
    }
  }
  console.log(summary.filter((_, i) => i % 8 === 0).join('\n'));

  // Pictures: the composer alone at the widths where its shape changes, and two pages.
  const comp = page.locator('.composer');
  for (const [state, text] of [['short', ''], ['spill', 'x'.repeat(9000)]]) {
    await ta.fill(text);
    for (const w of [1440, 1180, 1000, 860, 700, 390]) {
      await page.setViewportSize({ width: w, height: 900 });
      await page.evaluate(() => document.fonts.ready);
      await comp.screenshot({ path: await shotPath(`composer-fit-${state}-${w}`) });
    }
  }
  await ta.fill('');
  await page.setViewportSize({ width: 1180, height: 900 });
  await shot('composer-fit-page-1180');
  await page.setViewportSize({ width: 390, height: 844 });
  await shot('composer-fit-page-390');

  // One column, the document scrolls: mid-way through a long thread the reply
  // composer is pinned to the viewport's bottom, not left at the column's end.
  await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
  await page.locator('#view .msg').nth(5).waitFor();
  const pinned = await page.evaluate(async () => {
    const ms = document.querySelectorAll('#view .msg');
    ms[ms.length >> 1].scrollIntoView({ block: 'center' });
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)));
    const doc = document.scrollingElement, c = document.querySelector('.composer').getBoundingClientRect();
    return { y: doc.scrollTop, max: doc.scrollHeight - doc.clientHeight, bottom: c.bottom, top: c.top, vh: innerHeight };
  });
  assert(pinned.y > 0 && pinned.y < pinned.max - 1, `not mid-thread at 390: ${JSON.stringify(pinned)}`);
  assert(Math.abs(pinned.bottom - pinned.vh) <= 1 && pinned.top > 0, `composer not pinned to the viewport bottom at 390: ${JSON.stringify(pinned)}`);
});
