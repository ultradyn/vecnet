// Attachments Task 3: attach files to a new message (#new), in a real browser.
// Fixtures are built here at run time: no binaries are committed.
import { assert, cleanBuild, withDemo } from './lib.mjs';

// A 1×1 PNG, the bytes of every image fixture below.
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64');
const isSend = req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/messages';
const isBlob = req => req.method() === 'PUT' && new URL(req.url()).pathname === '/v1/blobs';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, allowConsole } = ctx;
    // Chromium logs every faked rejection and aborted request as a console error.
    allowConsole(/status of 413/);
    allowConsole(/ERR_FAILED/);
    allowConsole(/status of 501/);
    allowConsole(/ERR_CONNECTION_RESET/);
    const sends = [], blobs = [];
    page.on('request', req => {
      if (isSend(req)) sends.push(req.postDataJSON());
      if (isBlob(req)) blobs.push({ url: new URL(req.url()), type: req.headers()['content-type'], auth: req.headers().authorization || '' });
    });

    const id = key => demo.peers[key].id;
    const view = page.locator('.compose');
    const send = view.getByRole('button', { name: /^(Send|Sending…)$/ });
    const attach = view.getByRole('button', { name: 'Attach', exact: true });
    const text = view.locator('#c-text');
    const chips = view.locator('.up-chip');
    const done = view.locator('.up-chip.up-done');
    const chip = name => view.locator('.up-chip', { has: page.locator('.up-nm', { hasText: name }) });
    const opt = peerId => view.locator(`.opts .opt[title="${peerId}"]`);
    const pick = async peerId => { await opt(peerId).click(); await view.locator(`.pick[title="${peerId}"]`).waitFor(); };
    const open = async () => {
      await page.evaluate(() => { location.hash = '#new'; });
      await page.getByRole('heading', { name: 'New message', exact: true }).waitFor();
      await opt(id('codex')).waitFor();
    };
    const chipCount = n => page.waitForFunction(n => document.querySelectorAll('.compose .up-chip').length === n, n);
    const doneCount = n => page.waitForFunction(n => document.querySelectorAll('.compose .up-chip.up-done').length === n, n);
    const inlineImage = async name => {
      const img = page.locator('.msg button.thumb').getByRole('img', { name, exact: true });
      await img.waitFor();
      assert(await img.evaluate(async img => { await img.decode(); return img.naturalWidth > 0; }), `${name} did not decode inline`);
    };
    // attempt clicks Send and waits until that request is answered or fails
    // and the form is editable again.
    const attempt = async () => {
      const sent = page.waitForRequest(isSend);
      await send.click();
      await sent;
      await page.waitForFunction(() => { const b = document.querySelector('.compose button[type=submit]'); return b && b.textContent === 'Send' && !document.querySelector('.compose fieldset').disabled; });
    };
    const pickFiles = async files => {
      const chooser = page.waitForEvent('filechooser');
      await attach.click();
      await (await chooser).setFiles(files);
    };
    const paste = async (bytes, type) => page.evaluate(([bytes, type]) => {
      const dt = new DataTransfer();
      dt.items.add(new File([new Uint8Array(bytes)], 'image.png', { type }));
      document.querySelector('#c-text').dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    }, [[...bytes], type]);
    const transfer = (bytes, name, type) => page.evaluateHandle(([bytes, name, type]) => {
      const dt = new DataTransfer();
      dt.items.add(new File([new Uint8Array(bytes)], name, { type }));
      return dt;
    }, [[...bytes], name, type]);
    const drop = async (target, bytes, name, type) => {
      const dataTransfer = await transfer(bytes, name, type);
      for (const ev of ['dragenter', 'dragover', 'drop']) await target.dispatchEvent(ev, { dataTransfer });
    };
    // cancelled fires cancelable drag events carrying one file at the element
    // selector names and reports, per event, whether a listener cancelled it.
    // A synthetic event never runs the browser's default (open the file), so
    // the guard is proven by the cancel, not by the page staying put.
    const cancelled = (selector, bytes, name, type) => page.evaluate(([selector, bytes, name, type]) => {
      const dt = new DataTransfer();
      dt.items.add(new File([new Uint8Array(bytes)], name, { type }));
      const target = document.querySelector(selector);
      const out = {};
      for (const ev of ['dragenter', 'dragover', 'drop']) out[ev] = !target.dispatchEvent(new DragEvent(ev, { dataTransfer: dt, bubbles: true, cancelable: true }));
      return out;
    }, [selector, [...bytes], name, type]);

    await open();
    await pick(id('codex'));
    const body = 'Screenshots of the failing deploy, as promised.';
    await text.fill(body);
    assert(await attach.getAttribute('type') === 'button', 'Attach is not type=button');

    // Initial: one picked file and one pasted screenshot, both uploaded.
    await pickFiles({ name: 'deploy-log.txt', mimeType: 'text/plain', buffer: Buffer.from('step 4/7: health check failed\n') });
    await paste(PNG, 'image/png');
    await doneCount(2);
    const names = await view.locator('.up-chip .up-nm').allTextContents();
    assert(names[0] === 'deploy-log.txt', `picked name was ${names[0]}`);
    assert(/^pasted-\d{8}-\d{6}\.png$/.test(names[1]), `pasted name was ${names[1]}`);
    assert(await text.inputValue() === body, 'the paste touched the text');
    assert(blobs.length === 2, `expected two uploads, saw ${blobs.length}`);
    assert(blobs.every(b => b.url.searchParams.get('ttl') === '86400' && /^Bearer /.test(b.auth)), 'an upload lacked ttl=86400 or the bearer');
    assert(blobs[0].type === 'text/plain' && blobs[1].type === 'image/png', `upload types were ${blobs.map(b => b.type)}`);
    assert(blobs.every(b => !b.url.search.includes(demo.operator_secret)), 'the bearer appeared in a URL');
    assert(await send.isEnabled(), 'Send disabled with every chip done');
    await shot('att03-compose-initial');

    // A near-miss drop outside the form must not open the file or lose the draft.
    const nearMiss = await cancelled('.compose h1', PNG, 'near-miss.png', 'image/png');
    assert(nearMiss.dragover && nearMiss.drop, `a near-miss drag was not cancelled, so the browser would open the file: ${JSON.stringify(nearMiss)}`);
    assert(await chips.count() === 2, 'a drop outside the form added a chip');
    assert(await text.inputValue() === body && await page.evaluate(() => location.hash) === '#new', 'a near-miss drop lost the draft');

    // Progress: a third file dropped on the form, its upload held at the network.
    // lib.mjs hold() unroutes as it releases; a second route keeps Playwright's
    // interception on so the held request is continued by the handler.
    await page.route('**/__keep-interception__', route => route.continue());
    const release = await hold('**/v1/blobs*');
    // A held upload reports no progress, so its bar is indeterminate; reduced
    // motion draws that bar full width instead of mid-slide, off the chip.
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await drop(view.locator('form'), PNG, 'dashboard.png', 'image/png');
    await chip('dashboard.png').and(view.locator('.up-uploading')).waitFor();
    await chip('dashboard.png').locator('.up-bar').waitFor();
    assert(await send.isDisabled(), 'Send enabled while a chip uploads');
    assert(await view.locator('form.up-drop').count() === 0, 'the drop state stayed on after the drop');
    await shot('att03-compose-progress');
    await page.emulateMedia({ reducedMotion: null });
    await release();
    await doneCount(3);
    assert(await send.isEnabled(), 'Send stayed disabled after the upload finished');

    // Success: the send carries the three hashes in order and opens the thread.
    // A file dropped while the send is in flight is refused, not silently
    // added and then cleared with the sent chips.
    const releaseSend = await hold('**/v1/messages');
    await send.click();
    await page.waitForFunction(() => document.querySelector('.compose fieldset').disabled);
    const late = await cancelled('.compose .wrap', PNG, 'late.png', 'image/png');
    assert(late.drop, `a drop during the send was not cancelled: ${JSON.stringify(late)}`);
    assert(await chips.count() === 3, 'a drop during the send added a chip');
    await releaseSend();
    await page.waitForFunction(() => location.hash.startsWith('#thread/thr_'));
    const req = sends.at(-1);
    assert(sends.length === 1, `expected one send, saw ${sends.length}`);
    assert(req.content.text === body, 'the text changed');
    assert(Array.isArray(req.attachments) && req.attachments.length === 3, `attachments were ${JSON.stringify(req.attachments)}`);
    assert(req.attachments.map(a => a.name).join('|') === names.concat('dashboard.png').join('|'), `attachment order was ${req.attachments.map(a => a.name)}`);
    assert(req.attachments.every(a => /^[0-9a-f]{64}$/.test(a.sha256) && a.size > 0 && a.media_type), 'an attachment lacked its hash, size or type');
    await page.locator('.msg .txt', { hasText: body }).waitFor();
    await page.locator('.msg .att', { hasText: 'deploy-log.txt' }).waitFor();
    await inlineImage('dashboard.png');
    await inlineImage(names[1]);
    await shot('att03-compose-success');

    // Error: one chip's upload refused with a faked 413 blob_too_large.
    await open();
    assert(await chips.count() === 0, 'chips survived a successful send');
    await pick(id('codex'));
    const draft = 'The full capture is attached; the trimmed one follows.';
    await text.fill(draft);
    await pickFiles({ name: 'trimmed.png', mimeType: 'image/png', buffer: PNG });
    await doneCount(1);
    await page.route('**/v1/blobs*', route => route.fulfill({ status: 413, contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'blob_too_large', message: 'blob exceeds the per-blob cap', details: { limit: 10 << 20 } } }) }));
    await pickFiles({ name: 'full-capture.mp4', mimeType: 'video/mp4', buffer: Buffer.alloc(4096, 7) });
    const failed = chip('full-capture.mp4');
    await failed.and(view.locator('.up-failed')).waitFor();
    await failed.locator('.up-err', { hasText: 'Too large: the hub takes at most 10 MiB a file.' }).waitFor();
    await failed.getByRole('button', { name: 'Retry' }).waitFor();
    await failed.getByRole('button', { name: 'Remove' }).waitFor();
    assert(await chip('trimmed.png').and(view.locator('.up-done')).count() === 1, 'the other chip did not stay done');
    assert(await send.isDisabled(), 'Send enabled with a failed chip');
    assert(await text.inputValue() === draft, 'the draft was lost');
    await shot('att03-compose-error');
    await page.unroute('**/v1/blobs*');

    // A dropped connection fails only its own chip; Retry then uploads it.
    await page.route('**/v1/blobs*', route => route.abort('connectionreset'));
    await pickFiles({ name: 'trace.txt', mimeType: 'text/plain', buffer: Buffer.from('trace: connect reset at step 4\n') });
    const reset = chip('trace.txt');
    await reset.and(view.locator('.up-failed')).waitFor();
    await reset.locator('.up-err', { hasText: 'Upload failed: the hub could not be reached.' }).waitFor();
    await reset.getByRole('button', { name: 'Remove' }).waitFor();
    assert(await chip('trimmed.png').and(view.locator('.up-done')).count() === 1, 'a dropped connection unsettled the done chip');
    assert(await failed.and(view.locator('.up-failed')).count() === 1, 'a dropped connection changed the 413 chip');
    assert(await send.isDisabled(), 'Send enabled with two failed chips');
    assert(await text.inputValue() === draft, 'the draft was lost after a dropped connection');
    await page.unroute('**/v1/blobs*');
    await reset.getByRole('button', { name: 'Retry' }).click();
    await reset.and(view.locator('.up-done')).waitFor();
    await reset.getByRole('button', { name: 'Remove trace.txt' }).click();
    await chipCount(2);

    // Review Focus 1: a lost reply keeps the key; a removed chip mints a new one.
    // Each lost attempt reaches the hub and only its reply is dropped, so the
    // retry under the same key must be the hub's idempotent replay.
    await failed.getByRole('button', { name: 'Remove' }).click();
    await chipCount(1);
    await paste(PNG, 'image/png');
    await doneCount(2);
    await text.fill('');
    assert(await send.isEnabled(), 'Send disabled for attachments with empty text');
    const before = sends.length;
    const replies = [];
    await page.route('**/v1/messages', async route => {
      const r = await route.fetch();
      replies.push({ status: r.status(), body: await r.json() });
      await route.abort('failed');
    });
    await attempt();
    await view.locator('.err', { hasText: 'network' }).waitFor();
    await attempt();
    await view.locator('.up-chip .up-x').last().click();
    await chipCount(1);
    await attempt();
    await page.unroute('**/v1/messages');
    await send.click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/thr_'));
    const [lost1, lost2, removed, landed] = sends.slice(before);
    assert(sends.length === before + 4, `expected four attempts, saw ${sends.length - before}`);
    assert(lost1.idempotency_key === lost2.idempotency_key, 'the key changed across a lost reply');
    assert(lost2.attachments.length === 2 && removed.attachments.length === 1, 'the removed chip was still sent');
    assert(removed.idempotency_key !== lost2.idempotency_key, 'a removed chip kept the key');
    assert(landed.idempotency_key === removed.idempotency_key, 'the key changed across an unchanged retry');
    assert(landed.content.text === '' && landed.attachments[0].name === 'trimmed.png', `landed body was ${JSON.stringify(landed)}`);
    assert(replies.length === 3, `expected three hub replies behind the lost attempts, saw ${replies.length}`);
    const [r1, r2, r3] = replies;
    assert(r1.status === 202 && r2.status === 200, `the lost send and its retry answered ${r1.status} then ${r2.status}, not 202 then a 200 replay`);
    assert(r2.body.message_id === r1.body.message_id && r2.body.thread_id === r1.body.thread_id, 'the retry under the same key made a second message');
    assert(r3.status === 202 && r3.body.message_id !== r1.body.message_id, 'the body without the removed chip did not make a new message');
    await inlineImage('trimmed.png');
    const thr = (await page.evaluate(() => location.hash)).slice('#thread/'.length);
    assert(thr === r3.body.thread_id, `the landed send opened ${thr}, not the replayed ${r3.body.thread_id}`);
    const messagesIn = async t => {
      const res = await asPeer('operator', 'GET', `/v1/threads/${t}/messages?limit=50`);
      assert(res.ok, `thread messages answered ${res.status}`);
      return (await res.json()).messages;
    };
    const first = await messagesIn(r1.body.thread_id);
    assert(Array.isArray(first) && first.length === 1 && first[0].attachments.length === 2, `the twice-sent thread holds ${first?.length} messages, not one with two attachments`);
    const listed = await messagesIn(thr);
    assert(Array.isArray(listed) && listed.length === 1, `the thread holds ${listed?.length} messages, not one`);
    assert(listed[0].attachments.length === 1 && listed[0].attachments[0].sha256 === landed.attachments[0].sha256, 'the hub message lost its attachment');
  });
  console.log('Attachments compose passed: picked and pasted chips upload with ttl and declared type, near-miss drop keeps the draft, held upload disables Send, send carries hashes in order and opens the thread, drop during a send is refused, 413 and dropped-connection chip errors keep the text, a lost reply keeps the key and the hub replays it, removed chip mints a new one');
} finally { await cleanBuild(); }
