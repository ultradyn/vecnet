// Real web and terminal clients exchange attachments through one disposable hub.
// No screenshots: the individual attachment screen checks cover those states.
import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { assert, cleanBuild, run, withDemo } from './lib.mjs';
import { withTui } from './tmux.mjs';

// A valid 1x1 PNG, built into this test rather than committed as a fixture file.
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j3ioAAAAASUVORK5CYII=', 'base64');

try {
  await withDemo({}, async ctx => {
    const { page, demo, asPeer, dir } = ctx;
    const file = join(dir, 'terminal.png');
    await writeFile(file, png);
    await page.locator('#conn.live').waitFor();
    const started = await asPeer('codex', 'POST', '/v1/messages', {
      idempotency_key: 'att-journey-start', recipients: [demo.operator_peer],
      thread_title: 'Attachment journey', priority: 'wake', content: { text: 'Share screenshots here.' },
    });
    assert(started.ok, `start journey thread: ${started.status}`);
    const { thread_id: thread } = await started.json();
    await page.evaluate(id => { location.hash = '#thread/' + id; window.__attachmentJourney = true; }, thread);
    await page.locator('.composer textarea').waitFor();
    const navigations = [];
    page.on('request', request => { if (request.isNavigationRequest() && request.frame() === page.mainFrame()) navigations.push(request.url()); });

    await withTui({ env: ctx }, async ({ demo: terminalDemo, keys, waitFor: waitForTui }) => {
      const waitFor = async text => {
        try { await waitForTui(text); }
        catch (error) {
          const frame = await run('tmux', ['-L', `oui-${process.pid}`, 'capture-pane', '-t', 'ui:0.0', '-p'], { env: { ...process.env, TMUX: '' } });
          throw new Error(`${error.message}: ${text}\n${frame}`);
        }
      };
      assert(terminalDemo.url === demo.url, 'both clients must share one demo hub');
      // tmux may capture the padding after "live" as a tab.
      await waitFor(/\slive\s/);
      await waitFor('Share screenshots here.');
      await keys(':');
      await waitFor('COMMAND');
      await keys('thread 1', 'Enter');
      await waitFor('━ Attachment journey');
      await waitFor('Share screenshots here.');

      // A synthetic clipboard image exercises the real paste handler and upload.
      const reply = 'Web screenshot.';
      await page.locator('.composer textarea').fill(reply);
      await page.evaluate(bytes => {
        const data = new DataTransfer();
        data.items.add(new File([new Uint8Array(bytes)], 'image.png', { type: 'image/png' }));
        document.querySelector('.composer textarea').dispatchEvent(new ClipboardEvent('paste', {
          clipboardData: data, bubbles: true, cancelable: true,
        }));
      }, [...png]);
      const chip = page.locator('.composer .up-chip.up-done');
      await chip.waitFor();
      const pasteName = await chip.locator('.up-nm').textContent();
      assert(/^pasted-\d{8}-\d{6}\.png$/.test(pasteName), `clipboard filename: ${pasteName}`);
      const sent = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname === '/v1/messages' && response.ok());
      await page.locator('.composer .send:enabled').click();
      const webResponse = await sent;
      const webBody = webResponse.request().postDataJSON();
      const webMessage = await webResponse.json();
      assert(webBody.thread_id === thread && webBody.attachments?.length === 1, 'web reply must attach one PNG to the shared thread');
      assert(webBody.attachments[0].media_type === 'image/png' && webBody.attachments[0].size === png.length, 'web attachment metadata must match the pasted PNG');
      await waitFor(reply);
      await waitFor(`file ${pasteName}`);
      console.log('ok - pasted web screenshot appears as a TUI attachment row');

      // The terminal uploads its PNG and sends it while the web stays open.
      await keys(':');
      await waitFor('COMMAND');
      await keys(`attach ${file}`, 'Enter');
      await waitFor('[+] terminal.png');
      await keys('i');
      await waitFor('INSERT');
      const terminalText = 'TUI screenshot.';
      await keys(terminalText);
      await waitFor(`${terminalText}█`);
      await keys('Enter');
      await waitFor(`<max> ${terminalText}`);
      await waitFor('file terminal.png');
      const message = page.locator('.msg').filter({ has: page.locator('.txt', { hasText: terminalText }) });
      await message.waitFor();
      const image = message.locator('button.thumb img[alt="terminal.png"]');
      await image.waitFor();
      await page.waitForFunction(() => [...document.querySelectorAll('button.thumb img')].some(img => img.alt === 'terminal.png' && img.complete && img.naturalWidth > 0));
      assert((await image.getAttribute('src')).startsWith('blob:'), 'terminal PNG must render through an object URL');
      const history = await asPeer('operator', 'GET', `/v1/threads/${thread}/messages?limit=200`);
      assert(history.ok, `read journey history: ${history.status}`);
      const { messages } = await history.json();
      assert(messages.filter(m => m.message_id === webMessage.message_id).length === 1, 'pasted reply must occur exactly once');
      const fromTerminal = messages.filter(m => m.content.text === terminalText);
      assert(fromTerminal.length === 1 && fromTerminal[0].attachments?.length === 1, 'terminal must send exactly one message with one attachment');
      assert(fromTerminal[0].attachments[0].name === 'terminal.png' && fromTerminal[0].attachments[0].media_type === 'image/png', 'terminal filename and declared image type must survive delivery');
      assert(navigations.length === 0 && await page.evaluate(() => window.__attachmentJourney === true), 'web must receive and decode the terminal PNG without reloading');
      assert(await page.locator('.composer textarea').inputValue() === '', 'sent web reply must clear its draft');
      console.log('ok - TUI PNG appears inline in the web thread without a reload');
    });
  });
  console.log('Attachment journey passed: web paste → TUI file row; TUI :attach PNG → live web thumbnail');
} finally { await cleanBuild(); }
