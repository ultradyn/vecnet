// Real local hub/browser journey. Simulate daemon presence, then prove the
// operator's request reaches its inbox; no model or external service is run.
import { assert, cleanBuild, shotPath, withDemo } from './lib.mjs';

try {
  await withDemo({ demoBinary: process.env.VECNET_UI_DEMO_BIN }, async ({ page, demo, shot, quiet, allowConsole }) => {
    allowConsole(/status of 503/);
    await page.goto(demo.url + '/ui/#add-agent?kind=launcher');
    const dialog = page.locator('dialog.inv');
    await dialog.locator('#inv-kind').selectOption('launcher');
    await dialog.locator('#inv-name').fill('native-codex-host');
    await dialog.locator('#inv-launch-project').fill('vecnet');
    await dialog.locator('#inv-launch-repo').fill('/home/operator/src/vecnet');
    await dialog.locator('#inv-launch-base').fill('master');
    await dialog.locator('#inv-launch-source').fill('/home/operator/src/vecnet');
    await shot('launcher-1-host-setup');
    await dialog.locator('.inv-body').evaluate(el => { el.scrollTop = 0; });
    await shot('launcher-1a-setup-top');
    const answer = page.waitForResponse(r => r.url().endsWith('/v1/admin/agents') && r.request().method() === 'POST');
    await dialog.getByRole('button', { name: 'Create launcher setup' }).click();
    const response = await answer, added = await response.json();
    assert(response.ok(), 'launcher onboarding failed');
    const request = response.request().postDataJSON();
    assert(!JSON.stringify(request).includes('/home/operator'), 'host directories reached onboarding API');
    const commands = await dialog.locator('[aria-label="Launcher installation commands"]').textContent();
    assert((await dialog.textContent()).includes('running') && (await dialog.textContent()).includes('CODEX_HOME'), 'running supported Codex daemon prerequisite missing');
    const hostRecovery = await dialog.locator('[aria-label="Launcher host recovery commands"]').textContent();
    assert(hostRecovery.includes('codex app-server daemon start') && hostRecovery.includes('launcher check') && hostRecovery.includes('launcher status'), 'host recovery commands missing');
    assert(commands.includes("launcher setup --project 'vecnet' --repo '/home/operator/src/vecnet' --base-ref 'master' --write"), 'setup interface changed');
    assert(commands.includes('launcher install --source') && commands.includes('--write --enable --start'), 'service install command missing');
    const token = commands.match(/printf %s '([^']+)'/)[1];
    const block = dialog.locator('[aria-label="Launcher installation commands"]');
    await block.evaluate((el, token) => { el.textContent = el.textContent.replace(token, '[one-time token hidden]'); }, token);
    await shot('launcher-1b-install-commands');
    await block.evaluate((el, commands) => { el.textContent = commands; }, commands);
    const storage = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
    assert(!storage.includes(token), 'launcher token persisted');
    await dialog.getByRole('link', { name: 'Open launcher' }).click();
    await dialog.waitFor({ state: 'detached' });
    assert(!(await page.locator('body').textContent()).includes(token), 'token left in page after close');
    await page.locator('#pv-launch', { hasText: 'Complete launcher installation' }).waitFor();
    const enrolResponse = await fetch(demo.url + '/v1/enrol', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) });
    assert(enrolResponse.ok, 'real launcher enrolment failed');
    const enrolled = await enrolResponse.json();
    const daemon = async (path, body) => fetch(demo.url + path, { method: 'POST', headers: { Authorization: 'Bearer ' + enrolled.secret, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    assert((await daemon('/v1/presence', { state: 'blocked', at: new Date().toISOString(), capabilities: ['project:vecnet', 'launch:codex-app-server', 'preset:codex-app-server/default', 'mode:ask'], quota: { backends: { 'codex-app-server': 'down' } } })).ok, 'blocked presence failed');
    await page.reload();
    await page.locator('#pv-launch', { hasText: 'codex app-server daemon start' }).waitFor();
    assert((await page.locator('#pv-launch').textContent()).includes('launcher check'), 'stopped daemon has no host recovery');
    await shot('launcher-0-unavailable');
    assert((await daemon('/v1/presence', { state: 'idle', at: new Date().toISOString(), capabilities: ['project:vecnet', 'launch:codex-app-server', 'preset:codex-app-server/default', 'mode:read-only', 'mode:ask', 'mode:auto', 'mode:full-access'], quota: { max_sessions: 2, running: 0, open: 0, backends: { 'codex-app-server': 'ok' } } })).ok, 'daemon presence failed');
    await page.reload();
    const launch = page.locator('#pv-launch');
    await launch.getByRole('button', { name: 'Launch job', exact: true }).waitFor();
    await page.waitForFunction(() => !document.querySelector('#pv-launch button[type="submit"]').disabled);
    assert(await page.locator('#launch-project option').count() === 1, 'project alias is not a closed choice');
    assert(await page.locator('#launch-backend').inputValue() === 'codex-app-server', 'native backend not selected');
    assert(await page.locator('#launch-preset').inputValue() === 'default', 'Codex route default missing');
    await page.locator('#launch-title').fill('Inspect launcher project');
    await page.locator('#launch-prompt').fill('Inspect the configured project and report the test results.');
    await quiet();
    await shot('launcher-2-ready');
    await launch.screenshot({ path: await shotPath('launcher-2b-ready-form') });
    await page.setViewportSize({ width: 390, height: 844 });
    await launch.scrollIntoViewIfNeeded();
    await launch.screenshot({ path: await shotPath('launcher-3-mobile') });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile page overflows');
    await page.setViewportSize({ width: 1440, height: 900 });

    // Navigate while the first accepted response is held, then Dashboard/back
    // and reload after a lost response. All remounts must retain the original
    // body/key/deadline and reconcile the same message, job and thread.
    const sends = [], replies = [];
    let accepted, release;
    const committed = new Promise(resolve => { accepted = resolve; });
    const held = new Promise(resolve => { release = resolve; });
    const retryReady = async () => {
      try { await launch.getByRole('button', { name: 'Retry launch', exact: true }).waitFor({ timeout: 10000 }); }
      catch (error) {
        await page.screenshot({ path: await shotPath('launcher-recovery-failure') });
        console.log('Recovery diagnostic:', await page.evaluate(() => ({ route: location.hash,
          mounted: document.body.dataset.route,
          notice: document.querySelector('#launch-recovery')?.textContent,
          button: document.querySelector('#pv-launch button[type="submit"]')?.textContent })));
        release();
        throw error;
      }
    };
    const dashboardBack = async () => {
      await page.locator('#nav-dash').click();
      await launch.waitFor({ state: 'detached' });
      await page.goBack();
      await retryReady();
    };
    await page.route('**/v1/messages', async route => {
      const body = route.request().postDataJSON();
      if (body.job_request?.operation !== 'launch') { await route.continue(); return; }
      sends.push(body);
      const result = await route.fetch();
      replies.push(await result.json());
      if (sends.length === 1) { accepted(); await held; }
      if (sends.length <= 2) await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'demo_response_lost', message: 'Response lost after acceptance', details: {} } }) });
      else await route.fulfill({ response: result });
    });
    await launch.getByRole('button', { name: 'Launch job', exact: true }).click();
    await committed;
    await dashboardBack();
    await launch.getByRole('button', { name: 'Retry launch', exact: true }).scrollIntoViewIfNeeded();
    await shot('launcher-3b-retry-recovery');
    release();
    await quiet();
    await page.reload();
    await retryReady();
    assert((await page.locator('#launch-recovery').textContent()).includes(sends[0].idempotency_key), 'reload lost original launch identity');
    assert(await page.locator('#launch-prompt').isDisabled(), 'uncertain launch draft can be edited');
    await launch.getByRole('button', { name: 'Retry launch', exact: true }).click();
    await launch.getByRole('alert').filter({ hasText: 'response' }).waitFor();
    await dashboardBack();
    await page.reload();
    await retryReady();
    await launch.getByRole('button', { name: 'Retry launch', exact: true }).click();
    await page.waitForURL('**/#thread/*');
    assert(sends.length === 3 && sends.every(body => JSON.stringify(body) === JSON.stringify(sends[0])), 'remount/reload retry changed request');
    assert(replies[0].job_id && replies.every(r => r.job_id === replies[0].job_id && r.thread_id === replies[0].thread_id && r.message_id === replies[0].message_id), 'retry duplicated message/job/thread');
    assert(sends[0].recipients[0] === added.peer_id && sends[0].job_request.input.project === 'vecnet', 'wrong daemon or project');
    assert(!('model' in sends[0].job_request.input) && !('repo' in sends[0].job_request.input), 'launch overrides host route or path');
    await quiet();
    await shot('launcher-4-result-thread');
    console.log('Launcher journey passed: host setup, real enrolment/discovery, unavailable host recovery, native default, mobile, held navigation and reload recovery, resulting job/thread.');
  });
} finally { await cleanBuild(); }
