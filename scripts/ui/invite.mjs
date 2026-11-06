// Add agent (dashboard, Peers; the sidebar) in a real browser: the button, the form, a
// server error and a client one, and the success panel whose invite text
// matches the CLI's (invite-model.js, pinned by internal/cli/testdata/
// invite.json), enrols for real, and leaves no token behind once closed. The
// kind select shows human labels; a ChatGPT dot gets its MCP URL, setup
// steps and a link code instead, which is kept nowhere either.
import { assert, cleanBuild, scrollIntoViewStable, withDemo } from './lib.mjs';
import { inviteBlock } from '../../internal/ui/static/invite-model.js';
import { kindLabel } from '../../internal/ui/static/format.js';

const CODE = /^[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}$/;
const KIND_ORDER = ['session', 'launcher', 'openclaw', 'pagchat-bot', 'grokbot', 'dot'];
// A fake routine key and a syntactically shaped URL. Grok submits are mocked,
// so this script never posts them to the demo hub or a live routine.
const FAKE_KEY = 'crsr_fakekeyvalue';
const ROUTINE = 'https://api2.cursor.sh/automations/webhook/01234567-89ab-cdef-0123-456789abcdef';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, allowConsole, quiet, countRequests } = ctx;
    allowConsole(/status of 422/);
    allowConsole(/status of 503/);
    allowConsole(/status of 409/);
    allowConsole(/status of 404/);
    const shotSafe = async name => {
      const filled = await page.locator('input[type="password"]').evaluateAll(els => els.some(el => el.value));
      assert(!filled, 'refusing to shoot a filled secret');
      await shot(name);
    };
    const formReady = async () => {
      await page.waitForFunction(() => document.querySelectorAll('dialog.inv #inv-kind option').length > 0);
      await page.waitForFunction(() => {
        const b = document.querySelector('dialog.inv button[type="submit"]');
        return b && !b.disabled;
      });
    };
    const shownInvite = text => {
      const token = (text.match(/printf %s '([^']+)'/) || [])[1] || '';
      const expires = (text.match(/\(invite expires ([^)]+)\)/) || [])[1] || '';
      const profile = (text.match(/--profile ([^ \n]+)/) || [])[1] || '';
      const hubURL = (text.match(/--hub (\S+)/) || [])[1] || '';
      const named = (text.match(/--name '([^']*)'/) || [])[1] || '';
      const ownerLine = (text.match(/^You're invited to vecnet, (.*) \(invite expires /) || [])[1] || '';
      const suffix = "'s private agent message hub";
      const owner = ownerLine.endsWith(suffix) ? ownerLine.slice(0, -suffix.length) : '';
      return { token, expires, profile, hubURL, owner, open: named !== '', name: named === 'NAME' ? '' : named };
    };
    const agentPosts = () => countRequests(r => r.method() === 'POST' && r.url().endsWith('/v1/admin/agents'));
    const linkPosts = () => countRequests(r => r.method() === 'POST' && r.url().endsWith('/v1/admin/dot-links'));
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.locator('#conn.live').waitFor();
    await page.evaluate(() => { location.hash = '#dash'; });
    const peers = page.locator('.dash-peers');
    await peers.locator('tbody tr').first().waitFor();
    const before = await peers.locator('tbody tr').count();
    const open = peers.getByRole('button', { name: 'Add agent' });
    await open.waitFor();
    await scrollIntoViewStable(page, '.dash-peers h2');
    await shot('inv-1-button');

    // The form: kind defaults to session, offers a ChatGPT dot but never
    // operator or source, and labels each kind for people over its raw value.
    await open.click();
    const dlg = page.locator('dialog.inv[open]');
    await dlg.waitFor();
    await formReady();
    const kinds = await dlg.locator('#inv-kind option').evaluateAll(os => os.map(o => [o.value, o.textContent]));
    assert(kinds[0][0] === 'session' && kinds.some(([v]) => v === 'dot') && !kinds.some(([v]) => v === 'operator' || v === 'source'), `kinds ${kinds}`);
    assert(kinds.map(([v]) => v).join() === KIND_ORDER.join(), `kind order ${JSON.stringify(kinds)}`);
    assert(kinds.every(([v, t]) => t === kindLabel(v)), `kind labels ${JSON.stringify(kinds)}`);
    assert(kinds[0][1] === 'Agent session (Claude Code, Codex, CLI)' && kinds.some(([v, t]) => v === 'grokbot' && t === 'Grok Bot'), `kind labels ${JSON.stringify(kinds)}`);
    // A native select's popup is outside the page's pixels, so the shot shows
    // the options as an open list box for a moment.
    await dlg.locator('#inv-kind').evaluate(el => { el.size = el.options.length; });
    await shot('inv-2a-kinds');
    await dlg.locator('#inv-kind').evaluate(el => { el.removeAttribute('size'); });
    assert(await dlg.locator('#inv-ttl').inputValue() === '86400', 'default TTL is not 24 h');
    // A blank name is refused before any request.
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    await dlg.locator('.err', { hasText: 'A name is required.' }).waitFor();
    await dlg.locator('#inv-name').fill('web-repo Claude');
    await dlg.locator('#inv-ttl').selectOption('3600');
    await shot('inv-2-form');

    // A server refusal shows its code and message and keeps the form.
    const refuse = async route => route.request().method() === 'POST'
      ? route.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ error: { code: 'invalid_value', message: 'principal limit reached', details: { field: 'name' } } }) })
      : route.continue();
    await page.route('**/v1/admin/agents', refuse);
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    await dlg.locator('.err', { hasText: 'invalid_value: principal limit reached' }).waitFor();
    assert(await dlg.getByRole('button', { name: 'Create invite' }).isEnabled(), 'create stays disabled after an error');
    await shot('inv-3-error');
    await page.unroute('**/v1/admin/agents', refuse);

    // Success: the panel shows the CLI's text for the hub's answer.
    const answer = page.waitForResponse(r => r.url().endsWith('/v1/admin/agents') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    const answered = await answer;
    const sent = answered.request().postDataJSON();
    const res = await answered.json();
    assert(sent.kind === 'session' && sent.spec_version && sent.request_id && !('submit_action' in sent), `submit ${JSON.stringify(sent)}`);
    assert(sent.inputs.name === 'web-repo Claude' && sent.inputs.enrol_ttl_seconds === 3600 && !('name_open' in sent.inputs) && !sent.inputs.webhook_key, `inputs ${JSON.stringify(sent.inputs)}`);
    const pre = dlg.locator('pre.inv-block');
    await pre.waitFor();
    const text = await pre.textContent();
    const shown = shownInvite(text);
    assert(text === res.human_output && shown.hubURL === demo.url && shown.profile === 'web-repo-claude' && /^vn_enrol_/.test(shown.token), `invite text:\n${text}`);
    assert(text === inviteBlock({ owner: shown.owner, hubURL: shown.hubURL, profile: shown.profile, token: shown.token, expires: shown.expires }), `invite text:\n${text}`);
    assert(text.split('\n').length <= 6 && !/[$;]/.test(text), 'the invite is not a short plain block');
    await dlg.locator('.inv-warn', { hasText: 'Shown once' }).waitFor();
    await dlg.locator('.inv-meta time', { hasText: shown.expires }).waitFor();
    await dlg.getByRole('button', { name: 'Copy invite' }).click();
    await dlg.getByRole('button', { name: 'Copied' }).waitFor();
    assert(await page.evaluate(() => navigator.clipboard.readText()) === text, 'clipboard does not hold the invite');
    await shot('inv-4-success');

    // Nothing kept client-side; closing removes the token from the page.
    const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
    assert(!stored.includes(shown.token), 'the token reached web storage');
    await dlg.getByRole('button', { name: 'Done' }).click();
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    assert(!(await page.content()).includes(shown.token), 'the token is still in the page');
    await page.waitForFunction(n => document.querySelectorAll('.dash-peers tbody tr').length > n, before);

    // The pasted token enrols, once.
    const enrol = () => fetch(demo.url + '/v1/enrol', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: shown.token }) });
    assert((await enrol()).status === 200, 'the invite token does not enrol');
    assert((await enrol()).status === 401, 'the invite token enrolled twice');

    // Esc closes a fresh dialog too, with an empty form.
    await open.click();
    await dlg.waitFor();
    assert(await dlg.locator('#inv-name').inputValue() === '', 'the form kept the last name');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });

    // An open name: the agent picks it at enrol; the field is a suggestion.
    await open.click();
    await dlg.waitFor();
    await formReady();
    await dlg.getByLabel('Let the agent choose its name at enrol').check();
    await dlg.locator('label', { hasText: 'Suggested name (optional)' }).waitFor();
    await dlg.locator('#inv-name').fill('helper bot');
    const openAnswer = page.waitForResponse(r => r.url().endsWith('/v1/admin/agents') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    const openAnswered = await openAnswer;
    const osent = openAnswered.request().postDataJSON();
    assert(osent.inputs.name_open === true && osent.inputs.name === 'helper bot', `open inputs ${JSON.stringify(osent.inputs)}`);
    const otext = await dlg.locator('pre.inv-block').textContent();
    const oshown = shownInvite(otext);
    assert(otext === (await openAnswered.json()).human_output, 'open invite is not the human output');
    assert(otext === inviteBlock({ owner: oshown.owner, hubURL: oshown.hubURL, profile: 'helper-bot', token: oshown.token, expires: oshown.expires, open: true, name: 'helper bot' }), `open invite text:\n${otext}`);
    await dlg.locator('.inv-meta', { hasText: 'profile helper-bot · name chosen at enrol' }).waitFor();
    await shot('inv-5-open');
    await dlg.getByRole('button', { name: 'Done' }).click();
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    const named = await fetch(demo.url + '/v1/enrol', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: oshown.token, name: 'Helper Bot 2' }) });
    assert(named.status === 200 && (await named.json()).name === 'Helper Bot 2', `enrol with a chosen name: ${named.status}`);
    await peers.locator('tbody tr', { hasText: 'Helper Bot 2' }).waitFor();

    // A ChatGPT dot: no open name and no token lifetime, since it links with
    // a code; it still needs a name.
    await open.click();
    await dlg.waitFor();
    await formReady();
    await dlg.getByLabel('Let the agent choose its name at enrol').check();
    await dlg.locator('#inv-kind').selectOption('dot');
    assert(!(await dlg.getByLabel('Let the agent choose its name at enrol').isVisible()) && !(await dlg.locator('#inv-ttl').isVisible()), 'a dot still offers an open name or a TTL');
    await dlg.locator('label', { hasText: /^Name$/ }).waitFor();
    await dlg.locator('.inv-lede', { hasText: 'one-time link code' }).waitFor();
    await dlg.getByRole('button', { name: 'Create and link' }).click();
    await dlg.locator('.err', { hasText: 'A name is required.' }).waitFor();
    await dlg.locator('#inv-name').fill('scout');
    await shot('inv-10-dot-form');

    // The first code comes back with the peer. A later New code can fail on
    // its own and retry without creating another peer.
    const dotAnswer = page.waitForResponse(r => r.url().endsWith('/v1/admin/agents') && r.request().method() === 'POST');
    const peersBeforeDot = agentPosts();
    await dlg.getByRole('button', { name: 'Create and link' }).click();
    const dotAnswered = await dotAnswer;
    const dsent = dotAnswered.request().postDataJSON();
    const dres = await dotAnswered.json();
    assert(dsent.kind === 'dot' && dsent.inputs.name === 'scout' && !('enrol_ttl_seconds' in dsent.inputs) && !('name_open' in dsent.inputs), `dot inputs ${JSON.stringify(dsent.inputs)}`);
    assert(!dres.enrol_token && CODE.test(dres.human_output) && /^peer_/.test(dres.peer_id), `dot answer ${JSON.stringify({ ...dres, human_output: CODE.test(dres.human_output || '') ? '…' : dres.human_output })}`);
    assert(agentPosts() === peersBeforeDot + 1, 'creating a dot posted more than once');
    const mcp = dlg.locator('#inv-mcp');
    await mcp.waitFor();
    assert(await mcp.inputValue() === demo.url + '/mcp', `MCP URL ${await mcp.inputValue()}`);
    assert(await dlg.locator('.inv-code-val').textContent() === dres.human_output, 'the first code is not the human output');
    let failLink = true;
    const linkRoute = async route => failLink
      ? route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'the hub is busy', details: {} } }) })
      : route.continue();
    await page.route('**/v1/admin/dot-links', linkRoute);
    await dlg.getByRole('button', { name: 'New code' }).click();
    await dlg.locator('.inv-code .err', { hasText: 'No link code: unavailable: the hub is busy' }).waitFor();
    await shot('inv-11-dot-link-error');
    failLink = false;
    const linkAnswer = page.waitForResponse(r => r.url().endsWith('/v1/admin/dot-links') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'Try again' }).click();
    const link = await (await linkAnswer).json();
    await page.unroute('**/v1/admin/dot-links', linkRoute);
    assert(CODE.test(link.code) && link.expires_at, `link answer ${JSON.stringify({ ...link, code: CODE.test(link.code) ? '…' : link.code })}`);
    const code = dlg.locator('.inv-code-val');
    await code.waitFor();
    assert(await code.textContent() === link.code, 'the panel does not show the hub\'s code');
    await dlg.locator('.inv-code time[datetime="' + link.expires_at + '"]').waitFor();
    const steps = await dlg.locator('ol.inv-steps > li').allTextContents();
    assert(steps.length === 4 && /Plugins/.test(steps[0]) && /Add custom MCP server/.test(steps[0]) && /OAuth/.test(steps[1]) && steps[2].includes(link.code)
      && /trigger vecnet, event vecnet\.message and no settings/.test(steps[3]), `steps ${JSON.stringify(steps.map(t => t.replace(link.code, '…')))}`);
    await dlg.locator('.inv-note', { hasText: 'start a new chat or rescan the plugin' }).waitFor();
    await dlg.locator('.inv-meta', { hasText: 'peer ' + dres.peer_id }).waitFor();
    const focused = () => page.evaluate(() => document.activeElement && document.activeElement.textContent);
    assert(await focused() === 'Copy code', 'focus did not move to Copy code after Try again');
    await shot('inv-12-dot');
    await dlg.getByRole('button', { name: 'Copy URL' }).click();
    assert(await page.evaluate(() => navigator.clipboard.readText()) === demo.url + '/mcp', 'clipboard does not hold the MCP URL');
    await dlg.getByRole('button', { name: 'Copy code' }).click();
    assert(await page.evaluate(() => navigator.clipboard.readText()) === link.code, 'clipboard does not hold the link code');
    await page.evaluate(() => navigator.clipboard.writeText(''));

    // A live bearer warns before another code. Nothing is posted until the
    // operator confirms, and there is no keep-bearer control.
    const patchBearer = async route => {
      if (route.request().method() !== 'GET') return route.continue();
      const upstream = await route.fetch();
      const json = await upstream.json();
      for (const row of json.peers || []) {
        if (row.peer && row.peer.peer_id === dres.peer_id) row.credential = 'active';
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(json) });
    };
    await page.route('**/v1/admin/peers', patchBearer);
    const linksAtWarn = linkPosts();
    await dlg.getByRole('button', { name: 'New code' }).click();
    await dlg.locator('.inv-bearer').waitFor();
    assert(/revok/i.test(await dlg.locator('.inv-bearer').innerText()) && /no keep-bearer option/i.test(await dlg.locator('.inv-bearer').innerText()), 'the bearer warning is missing');
    await dlg.getByRole('button', { name: 'Revoke bearer and link' }).waitFor();
    await page.waitForTimeout(300);
    assert(linkPosts() === linksAtWarn, 'the warning posted a link before confirmation');
    await shotSafe('inv-14-bearer');
    await dlg.getByRole('button', { name: 'Cancel' }).click();
    await dlg.locator('.inv-bearer').waitFor({ state: 'detached' });
    await dlg.locator('.inv-code-val', { hasText: link.code }).waitFor();
    assert(linkPosts() === linksAtWarn, 'cancelling the warning posted a link');

    // Confirming mints another code; neither reaches the URL or web storage.
    const again = page.waitForResponse(r => r.url().endsWith('/v1/admin/dot-links') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'New code' }).click();
    await dlg.getByRole('button', { name: 'Revoke bearer and link' }).click();
    const link2 = await (await again).json();
    await page.unroute('**/v1/admin/peers', patchBearer);
    await dlg.locator('.inv-code-val', { hasText: link2.code }).waitFor();
    assert(link2.code !== link.code, 'New code repeated the code');
    const kept = await page.evaluate(() => location.href + JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
    assert(!kept.includes(link.code) && !kept.includes(link2.code), 'a link code reached the URL or web storage');
    await dlg.getByRole('button', { name: 'Done' }).click();
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    const html = await page.content();
    assert(!html.includes(link.code) && !html.includes(link2.code), 'a link code is still in the page');
    // The directory row reads as prose: the human label, the raw kind kept
    // for the colour and the hover.
    const dotPill = peers.locator('tbody tr', { hasText: 'scout' }).locator('span.pill', { has: page.locator('i.kd.k-dot') });
    await dotPill.waitFor();
    assert(await dotPill.textContent() === 'ChatGPT dot' && await dotPill.getAttribute('title') === 'ChatGPT dot · dot', 'the dot row does not show its kind label');

    // The sidebar's Add agent, under the route links: the same dialog from
    // any view, opened by keyboard over a thread that stays mounted (its
    // draft survives), with focus back on the button when it closes.
    const navAdd = page.locator('#nav-add');
    const navIds = await page.locator('nav.side > .nav').first().locator(':scope > a, :scope > button').evaluateAll(els => els.map(e => e.id));
    assert(navIds.join() === 'nav-inbox,nav-new,nav-dash,nav-add', `sidebar nav ${navIds}`);
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await page.locator('.msg').first().waitFor();
    const draft = page.locator('.composer textarea');
    await draft.fill('draft kept under the dialog');
    await page.evaluate(() => { document.querySelector('.composer textarea').dataset.mounted = 'nav-add'; });
    assert(await navAdd.isEnabled(), 'sidebar Add agent is disabled once signed in');
    // The thread's detail (aside, open approvals) and receipts load apart from
    // its first message: wait for all of them, and for the list's last redraw
    // to pin it to the bottom.
    await page.locator('#aside h3', { hasText: 'Participants' }).waitFor();
    await quiet();
    await shot('inv-6-nav');
    await navAdd.focus();
    await page.keyboard.press('Enter');
    await dlg.waitFor();
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'inv-name', 'focus is not in the name field (' + await page.evaluate(() => {
      const el = document.activeElement;
      return el ? (el.id || el.tagName) : 'none';
    }) + ')');
    await formReady();
    assert(await page.evaluate(() => location.hash.startsWith('#thread/')), 'opening Add agent changed the route');
    await dlg.locator('#inv-name').fill('nav agent');
    await shot('inv-7-nav-thread');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'nav-add', 'focus did not return to the sidebar button');
    assert(await page.locator('.composer textarea[data-mounted="nav-add"]').inputValue() === 'draft kept under the dialog', 'the thread remounted or lost its draft');

    // Created from the sidebar: the session's api, the same invite, and the
    // directory picks the peer up.
    await navAdd.click();
    await formReady();
    await dlg.locator('#inv-name').fill('nav agent');
    const navAnswer = page.waitForResponse(r => r.url().endsWith('/v1/admin/agents') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    const navAnswered = await navAnswer;
    const nres = await navAnswered.json();
    const ntext = await dlg.locator('pre.inv-block').textContent();
    const nshown = shownInvite(ntext);
    assert(ntext === nres.human_output && ntext === inviteBlock({ owner: nshown.owner, hubURL: nshown.hubURL, profile: 'nav-agent', token: nshown.token, expires: nshown.expires }), `sidebar invite text:\n${ntext}`);
    await dlg.getByRole('button', { name: 'Done' }).click();
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    assert(!(await page.content()).includes(nshown.token), 'the sidebar invite token is still in the page');
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'nav-add', 'focus did not return after Done');
    await page.waitForFunction(async ({ secret, id }) => {
      const r = await fetch('/v1/peers', { headers: { Authorization: `Bearer ${secret}` } });
      return ((await r.json()).peers || []).some(p => p.peer_id === id);
    }, { secret: demo.operator_secret, id: nres.peer_id }, { polling: 200 });

    // Narrow: the sidebar stacks above the view; the button and dialog fit.
    await page.setViewportSize({ width: 390, height: 844 });
    await draft.fill('');
    await page.evaluate(() => { document.getElementById('main').scrollTop = 0; window.scrollTo(0, 0); });
    const box = await navAdd.boundingBox();
    assert(box && box.x >= 0 && box.x + box.width <= 390, `sidebar Add agent off screen at 390 px: ${JSON.stringify(box)}`);
    await shot('inv-8-nav-narrow');
    await navAdd.click();
    await dlg.waitFor();
    await formReady();
    const dbox = await dlg.boundingBox();
    assert(dbox && dbox.x >= 0 && dbox.x + dbox.width <= 390, `dialog off screen at 390 px: ${JSON.stringify(dbox)}`);
    await shot('inv-9-nav-narrow-dialog');

    // The dot panel fits a phone too: URL row, steps and code inside 390 px.
    await dlg.locator('#inv-kind').selectOption('dot');
    await dlg.locator('#inv-name').fill('pocket dot');
    await dlg.getByRole('button', { name: 'Create and link' }).click();
    await dlg.locator('.inv-code-val').waitFor();
    assert(await page.evaluate(() => document.activeElement && document.activeElement.textContent) === 'Copy URL', 'focus is not on Copy URL');
    const wide = await dlg.evaluate(el => [...el.querySelectorAll('*')].filter(e => { const r = e.getBoundingClientRect(); return r.width && (r.left < 0 || r.right > 390); }).map(e => e.className || e.tagName));
    assert(!wide.length, `dot panel overflows at 390 px: ${wide}`);
    const codeBox = await dlg.locator('.inv-code-val').boundingBox();
    assert(codeBox && codeBox.height < 60, `the link code wraps at 390 px: ${JSON.stringify(codeBox)}`);
    await shot('inv-13-dot-narrow');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });

    // Registry states and the Grok form. Submits are answered here so a
    // routine URL is never posted to the demo hub.
    await page.setViewportSize({ width: 1440, height: 900 });
    let releaseKinds = () => {};
    const kindsGate = new Promise(resolve => { releaseKinds = resolve; });
    const holdKinds = async route => {
      await kindsGate;
      try { await route.continue(); } catch { /* the dialog moved on */ }
    };
    await page.route('**/v1/admin/agent-kinds', holdKinds);
    await navAdd.click();
    await dlg.locator('.inv-loading', { hasText: 'Loading kinds' }).waitFor();
    assert(await dlg.locator('#inv-kind option').count() === 0, 'kinds rendered before the registry answered');
    await shotSafe('inv-g-loading');
    releaseKinds();
    await page.unroute('**/v1/admin/agent-kinds', holdKinds);
    await formReady();
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });

    const kindsDown = async route => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'the hub is busy', details: {} } }) });
    await page.route('**/v1/admin/agent-kinds', kindsDown);
    await navAdd.click();
    await dlg.locator('.inv-loading', { hasText: 'unavailable: the hub is busy' }).waitFor();
    assert(await dlg.locator('#inv-kind option').count() === 0, 'a failed registry fell back to a hardcoded list');
    await dlg.getByRole('button', { name: 'Retry' }).waitFor();
    await shotSafe('inv-g-kinds-down');
    await page.unroute('**/v1/admin/agent-kinds', kindsDown);
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });

    const grokPosts = [];
    let grokMode = 'ok';
    let releaseGrok = () => {};
    const fulfillJSON = async (route, status, body) => {
      try { await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) }); }
      catch { /* the dialog aborted the request */ }
    };
    await page.route('**/v1/admin/agents**', async route => {
      const req = route.request();
      if (req.method() === 'GET' && req.url().includes('/onboarding/')) {
        if (grokMode === 'missing') return fulfillJSON(route, 404, { error: { code: 'not_found', message: 'not found', details: {} } });
        const last = grokPosts.at(-1) || {};
        return fulfillJSON(route, 200, {
          request_id: last.request_id || 'req_status', kind: 'grokbot', peer_id: 'peer_grok_test', status: 'created',
          auth_state: 'unlinked', setup: { state: 'queued' }, human_output: 'SHOULD NOT SHOW', webhook_key: FAKE_KEY, webhook_url: ROUTINE,
        });
      }
      if (req.method() !== 'POST') return route.continue();
      let data = null;
      try { data = req.postDataJSON(); } catch { data = null; }
      if (!data || data.kind !== 'grokbot') return route.continue();
      grokPosts.push(data);
      if (grokMode === 'hold') await new Promise(resolve => { releaseGrok = resolve; });
      if (grokMode === 'slow') await new Promise(resolve => setTimeout(resolve, 400));
      if (grokMode === 'field') return fulfillJSON(route, 422, { error: { code: 'invalid_value', message: 'bad ' + FAKE_KEY + ' at ' + ROUTINE, details: { field: 'webhook_key' } } });
      if (grokMode === 'down') return fulfillJSON(route, 503, { error: { code: 'unavailable', message: 'the hub is busy', details: {} } });
      if (grokMode === 'stale') return fulfillJSON(route, 409, { error: { code: 'stale_spec', message: 'spec changed', details: {} } });
      return fulfillJSON(route, 201, {
        request_id: data.request_id, kind: 'grokbot', peer_id: 'peer_grok_test', principal_id: 'prn_grok_test',
        route_id: 'route_grok_test', connector_name: (data.inputs && data.inputs.connector_name) || 'vecnet-helper-bot',
        auth_state: 'unlinked', status: 'created',
        setup: { thread_id: 'thr_setup', message_id: 'msg_setup', delivery_id: 'dly_setup', state: 'queued' },
        send_defaults: { priority: 'wake_idle' },
      });
    });

    await navAdd.click();
    await formReady();
    await dlg.locator('#inv-kind').selectOption('grokbot');
    await dlg.locator('#inv-prompt').waitFor();
    const promptBox = await dlg.locator('#inv-prompt').boundingBox();
    const keyBox = await dlg.locator('#inv-key').boundingBox();
    assert(promptBox && keyBox && promptBox.y + promptBox.height <= keyBox.y, 'the authorization prompt is not above the key');
    assert(await dlg.locator('#inv-key').getAttribute('type') === 'password', 'the routine key is not masked');
    const promptText = await dlg.locator('#inv-prompt').innerText();
    assert(/few gateway/.test(promptText) && /Hosted OAuth/.test(promptText) && !/stdio|mcp-remote/i.test(promptText), 'the prompt is missing the gateway policy');
    assert(/few gateway/.test(await dlg.locator('#inv-gateway').innerText()), 'gateway copy is missing');
    const quota = await dlg.locator('#inv-quota').innerText();
    assert(/wake_idle/.test(quota) && /not a price quote/i.test(quota), 'quota copy is missing');
    assert(!/stdio|mcp-remote/i.test(await dlg.innerText()), 'the form offers stdio');
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.locator('.err', { hasText: 'A name is required.' }).waitFor();
    await dlg.locator('#inv-name').fill('Helper Bot');
    assert((await dlg.locator('#inv-derived').innerText()).includes('vecnet-helper-bot'), 'the connector was not derived');
    assert((await dlg.locator('#inv-prompt').innerText()).includes('vecnet-helper-bot'), 'the prompt did not pick up the connector');
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.locator('.err', { hasText: 'A routine URL is required.' }).waitFor();
    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.locator('.err', { hasText: 'A routine key is required.' }).waitFor();
    await dlg.locator('#inv-url').fill('');
    await shotSafe('inv-g-form');
    await dlg.locator('#inv-skip').check();
    await dlg.locator('.inv-note', { hasText: 'CLI bearer' }).waitFor();
    await shotSafe('inv-g-skip');
    await dlg.locator('#inv-skip').uncheck();

    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.locator('#inv-key').fill(FAKE_KEY);
    await dlg.locator('#inv-kind').selectOption('session');
    await dlg.locator('#inv-kind').selectOption('grokbot');
    assert(await dlg.locator('#inv-key').inputValue() === '' && await dlg.locator('#inv-url').inputValue() === '', 'kind switch kept the routine secret');

    await dlg.locator('#inv-connector').fill('nope');
    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.locator('#inv-key').fill(FAKE_KEY);
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.locator('.err', { hasText: 'connector_name must match ^vecnet-[a-z0-9-]{1,48}$' }).waitFor();
    assert(!(await dlg.locator('.err').innerText()).includes(FAKE_KEY) && !(await dlg.locator('.err').innerText()).includes(ROUTINE), 'a client error quoted a secret');
    await dlg.locator('#inv-connector').fill('');
    await dlg.locator('#inv-key').fill('');
    await dlg.locator('#inv-url').fill('');

    grokMode = 'field';
    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.locator('#inv-key').fill(FAKE_KEY);
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.locator('.err', { hasText: 'invalid_value:' }).waitFor();
    const fieldErr = await dlg.locator('.err').innerText();
    assert(!fieldErr.includes(FAKE_KEY) && !fieldErr.includes(ROUTINE) && fieldErr.includes('[redacted]'), `field error leaked a secret: ${fieldErr}`);
    assert(grokPosts.at(-1).inputs.webhook_key === FAKE_KEY && grokPosts.at(-1).inputs.skip_mcp === false && !grokPosts.at(-1).inputs.connector_name, `grok inputs ${JSON.stringify(grokPosts.at(-1).inputs)}`);
    const stableId = grokPosts.at(-1).request_id;
    await dlg.locator('#inv-key').evaluate(el => { el.value = ''; });
    await dlg.locator('#inv-url').evaluate(el => { el.value = ''; });
    await shotSafe('inv-g-error');

    grokMode = 'down';
    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.locator('#inv-key').fill('Bearer ' + FAKE_KEY);
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.getByRole('button', { name: 'Retry' }).waitFor();
    await dlg.getByRole('button', { name: 'Check status' }).waitFor();
    assert(grokPosts.at(-1).request_id === stableId && grokPosts.at(-1).inputs.webhook_key === 'Bearer ' + FAKE_KEY, 'an uncertain retry minted a new id or rewrote the key');
    grokMode = 'missing';
    await dlg.getByRole('button', { name: 'Check status' }).click();
    await dlg.locator('.err', { hasText: 'No committed request for this id yet.' }).waitFor();
    assert(!(await dlg.innerText()).includes('SHOULD NOT SHOW') && !(await dlg.innerText()).includes(FAKE_KEY), 'status revealed human output');

    grokMode = 'slow';
    const postsBeforeSlow = grokPosts.length;
    await dlg.locator('#inv-name').press('Enter');
    await dlg.locator('#inv-name').press('Enter');
    await dlg.locator('.inv-status', { hasText: 'setup queued' }).waitFor();
    assert(grokPosts.length === postsBeforeSlow + 1, `double submit posted ${grokPosts.length - postsBeforeSlow} times`);
    assert(grokPosts.at(-1).request_id === stableId, 'the successful retry changed the request id');
    assert(await dlg.locator('.inv-connected').count() === 0, 'queued rendered a connected badge');
    assert(/does not mean the bot is connected/.test(await dlg.innerText()), 'the queued warning is missing');
    assert(/hosted OAuth/.test(await dlg.innerText()) && await dlg.locator('#inv-start-link').count() === 1, 'hosted OAuth did not offer a link');
    assert(!/stdio|mcp-remote/i.test(await dlg.innerText()), 'the result offers stdio');
    const keptGrok = await page.evaluate(() => location.href + JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
    assert(!keptGrok.includes(FAKE_KEY) && !keptGrok.includes(ROUTINE) && !(await page.content()).includes(FAKE_KEY), 'the routine secret was kept');
    await shotSafe('inv-g-queued');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });

    await navAdd.click();
    await formReady();
    await dlg.locator('#inv-kind').selectOption('grokbot');
    await dlg.locator('#inv-name').fill('Helper Bot');
    const kindsBefore = countRequests(r => r.method() === 'GET' && r.url().includes('/v1/admin/agent-kinds'));
    grokMode = 'stale';
    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.locator('#inv-key').fill(FAKE_KEY);
    const postsAtStale = grokPosts.length;
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.locator('.err', { hasText: 'The kind contract changed' }).waitFor();
    assert(await dlg.locator('#inv-key').inputValue() === '' && await dlg.locator('#inv-url').inputValue() === '', 'stale_spec kept the routine secret');
    const kindsDeadline = Date.now() + 5000;
    while (countRequests(r => r.method() === 'GET' && r.url().includes('/v1/admin/agent-kinds')) <= kindsBefore) {
      if (Date.now() > kindsDeadline) throw new Error('stale_spec did not refresh kinds');
      await page.waitForTimeout(50);
    }
    assert(grokPosts.length === postsAtStale + 1, 'stale_spec submitted again on its own');
    await shotSafe('inv-g-stale');

    grokMode = 'hold';
    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.locator('#inv-key').fill(FAKE_KEY);
    const postsAtHold = grokPosts.length;
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    const holdDeadline = Date.now() + 5000;
    while (grokPosts.length === postsAtHold) {
      if (Date.now() > holdDeadline) throw new Error('the in-flight submit did not reach the mock');
      await page.waitForTimeout(50);
    }
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    grokMode = 'ok';
    releaseGrok();
    await page.waitForTimeout(400);
    assert(await page.locator('dialog.inv').count() === 0, 'a late response reopened the dialog');
    assert(!(await page.content()).includes(FAKE_KEY) && !(await page.content()).includes(ROUTINE), 'a late response left a secret on the page');

    const postsAtLink = agentPosts();
    await page.evaluate(() => { location.hash = '#add-agent?kind=grokbot&name=Helper%20Bot&request_id=req-deep-1'; });
    await dlg.waitFor();
    await formReady();
    await page.waitForFunction(() => document.querySelector('#inv-kind')?.value === 'grokbot' && document.querySelector('#inv-name')?.value === 'Helper Bot');
    await page.waitForTimeout(400);
    assert(agentPosts() === postsAtLink, 'the deep link submitted on its own');
    assert(await dlg.locator('#inv-key').inputValue() === '' && await dlg.locator('#inv-url').inputValue() === '', 'the deep link filled a secret');
    await shotSafe('inv-g-deeplink');
    await page.setViewportSize({ width: 390, height: 844 });
    const grokWide = await dlg.evaluate(el => [...el.querySelectorAll('*')].filter(e => { const r = e.getBoundingClientRect(); return r.width && (r.left < 0 || r.right > 390); }).map(e => e.className || e.tagName));
    assert(!grokWide.length, `grok form overflows at 390 px: ${grokWide}`);
    await shotSafe('inv-g-narrow');
    await page.setViewportSize({ width: 1440, height: 900 });
    grokMode = 'ok';
    await dlg.locator('#inv-url').fill(ROUTINE);
    await dlg.locator('#inv-key').fill(FAKE_KEY);
    await dlg.locator('#inv-skip').check();
    const postsAtSkip = grokPosts.length;
    await dlg.getByRole('button', { name: 'Create Grok bot' }).click();
    await dlg.locator('.inv-status', { hasText: 'setup queued' }).waitFor();
    assert(grokPosts.length === postsAtSkip + 1 && grokPosts.at(-1).request_id === 'req-deep-1' && grokPosts.at(-1).inputs.skip_mcp === true, `deep link submit ${JSON.stringify(grokPosts.at(-1))}`);
    assert(/CLI bearer/.test(await dlg.innerText()) && await dlg.locator('#inv-start-link').count() === 0, 'Skip MCP still offered a link code');
    assert(await dlg.locator('.inv-connected').count() === 0, 'skip rendered a connected badge');
    assert(!(await page.content()).includes(FAKE_KEY), 'the skip result kept the key');
    await shotSafe('inv-g-skip-result');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
  });
  console.log('Invite passed: registry kinds, form, client and server errors, CLI-identical text, copy, no token kept, enrol once, open name, ChatGPT dot (code, retry, bearer warning, new code), sidebar, narrow, Grok prompt, skip, queued, deep link');
} finally { await cleanBuild(); }
