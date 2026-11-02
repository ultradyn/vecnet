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
    await drop(page.locator('.compose h1'), PNG, 'near-miss.png', 'image/png');
    assert(await chips.count() === 2, 'a drop outside the form added a chip');
    assert(await text.inputValue() === body && await page.evaluate(() => location.hash) === '#new', 'a near-miss drop lost the draft');

    // Progress: a third file dropped on the form, its upload held at the network.
    // lib.mjs hold() unroutes as it releases; a second route keeps Playwright's
    // interception on so the held request is continued by the handler.
    await page.route('**/__keep-interception__', route => route.continue());
    const release = await hold('**/v1/blobs*');
    await drop(view.locator('form'), PNG, 'dashboard.png', 'image/png');
    await chip('dashboard.png').and(view.locator('.up-uploading')).waitFor();
    await chip('dashboard.png').locator('.up-bar').waitFor();
    assert(await send.isDisabled(), 'Send enabled while a chip uploads');
    assert(await view.locator('form.up-drop').count() === 0, 'the drop state stayed on after the drop');
    await shot('att03-compose-progress');
    await release();
    await doneCount(3);
    assert(await send.isEnabled(), 'Send stayed disabled after the upload finished');

    // Success: the send carries the three hashes in order and opens the thread.
    await send.click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/thr_'));
    const req = sends.at(-1);
    assert(sends.length === 1, `expected one send, saw ${sends.length}`);
    assert(req.content.text === body, 'the text changed');
    assert(Array.isArray(req.attachments) && req.attachments.length === 3, `attachments were ${JSON.stringify(req.attachments)}`);
    assert(req.attachments.map(a => a.name).join('|') === names.concat('dashboard.png').join('|'), `attachment order was ${req.attachments.map(a => a.name)}`);
    assert(req.attachments.every(a => /^[0-9a-f]{64}$/.test(a.sha256) && a.size > 0 && a.media_type), 'an attachment lacked its hash, size or type');
    await page.locator('.msg .txt', { hasText: body }).waitFor();
    for (const name of ['deploy-log.txt', 'dashboard.png']) await page.locator('.msg .att', { hasText: name }).waitFor();
    await page.locator('.msg .att', { hasText: /^pasted-/ }).waitFor();
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

    // Review Focus 1: a lost reply keeps the key; a removed chip mints a new one.
    await failed.getByRole('button', { name: 'Remove' }).click();
    await chipCount(1);
    await paste(PNG, 'image/png');
    await doneCount(2);
    await text.fill('');
    assert(await send.isEnabled(), 'Send disabled for attachments with empty text');
    const before = sends.length;
    await page.route('**/v1/messages', route => route.abort('failed'));
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
    await page.locator('.msg .att', { hasText: 'trimmed.png' }).waitFor();
    const thr = (await page.evaluate(() => location.hash)).slice('#thread/'.length);
    const res = await asPeer('operator', 'GET', `/v1/threads/${thr}/messages?limit=50`);
    assert(res.ok, `thread messages answered ${res.status}`);
    const listed = await res.json();
    assert(Array.isArray(listed.messages) && listed.messages.length === 1, `the thread holds ${listed.messages?.length} messages, not one`);
    assert(listed.messages[0].attachments.length === 1 && listed.messages[0].attachments[0].sha256 === landed.attachments[0].sha256, 'the hub message lost its attachment');
  });
  console.log('Attachments compose passed: picked and pasted chips upload with ttl and declared type, near-miss drop keeps the draft, held upload disables Send, send carries hashes in order and opens the thread, 413 chip error keeps the text, lost reply keeps the key, removed chip mints a new one');
} finally { await cleanBuild(); }
