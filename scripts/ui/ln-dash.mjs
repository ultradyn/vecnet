// Launcher Task 4: the launcher kind colour (web and TUI) and the dashboard's
// Principals card, against a real demo hub. Fixtures are built at run time:
// the seed has no launcher and no principal with limits.
import { assert, cleanBuild, run, withDemo } from './lib.mjs';
import { withTui } from './tmux.mjs';
import { principalRows } from '../../internal/ui/static/principals-model.js';

const LAUNCHER = 'ln-xsm';
const YELLOW = 'rgb(250, 204, 21)'; // #facc15

async function call(url, secret, method, path, body) {
  const headers = { 'Content-Type': 'application/json', ...(secret ? { Authorization: `Bearer ${secret}` } : {}) };
  const response = await fetch(url + path, { method, headers, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
  const text = await response.text();
  assert(response.ok, `${method} ${path}: ${response.status}`);
  return text ? JSON.parse(text) : null;
}

// enrolLauncher adds a launcher peer as the operator, exchanges its token for
// a secret (asPeer knows only seeded peers) and reports working presence.
async function enrolLauncher(demo) {
  const op = (method, path, body) => call(demo.url, demo.operator_secret, method, path, body);
  const added = await op('POST', '/v1/admin/peers', { kind: 'launcher', name: LAUNCHER });
  const enrolled = await call(demo.url, '', 'POST', '/v1/enrol', { token: added.enrol_token });
  await call(demo.url, enrolled.secret, 'POST', '/v1/presence', { state: 'working', at: new Date().toISOString(), quota: { weekly_remaining_pct: 72 } });
  return added.peer_id;
}

try {
  await withDemo({}, async ({ page, shot, demo }) => {
    const op = (method, path, body) => call(demo.url, demo.operator_secret, method, path, body);
    const sec = key => page.locator('.dash-' + key);
    const card = page.locator('#d-principals');

    await page.locator('#conn.live').waitFor();
    const principalsRead = page.waitForResponse(r => new URL(r.url()).pathname === '/v1/admin/principals');
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    await sec('peers').locator('tbody tr').first().waitFor();
    const first = await principalsRead;
    assert(first.ok(), `principals read: ${first.status()}`);
    await first.finished();

    // Empty: the seed's principals have no limits and no created peers.
    assert(principalRows((await op('GET', '/v1/admin/principals')).principals).length === 0, 'the seed already has a limited principal');
    await card.and(page.locator('[hidden]')).waitFor({ state: 'attached' });
    assert(!await card.isVisible(), 'the principals card shows with no limited principals');
    await shot('ln04-dash-empty');

    // Initial: a launcher peer reporting working, its kind dot in yellow.
    await enrolLauncher(demo);
    const row = sec('peers').locator('tbody tr', { has: page.locator('td:first-child > span:first-child', { hasText: new RegExp('^' + LAUNCHER + '$') }) });
    await row.locator('td', { hasText: /^72%$/ }).waitFor();
    await row.locator('td').nth(2).filter({ hasText: /^working/ }).waitFor();
    const dot = row.locator('i.kd.k-launcher');
    await dot.waitFor();
    const colour = await dot.evaluate(el => getComputedStyle(el).backgroundColor);
    assert(colour === YELLOW, `launcher kind dot is ${colour}, want ${YELLOW}`);
    // Reloads replace table rows, so query, scroll and measure in one poll.
    await page.waitForFunction(name => {
      const tr = [...document.querySelectorAll('.dash-peers tbody tr')].find(el => el.querySelector('td:first-child > span')?.textContent === name);
      if (!tr) return false;
      tr.scrollIntoView({ block: 'center', behavior: 'instant' });
      const r = tr.getBoundingClientRect();
      return r.top > 0 && r.bottom < innerHeight;
    }, LAUNCHER);
    await shot('ln04-dash-initial');

    // Principals: one principal at its cap of two created peers, with empty
    // default scopes while an operator-enrolled peer of it is scoped.
    const prn = await op('POST', '/v1/admin/principals', { name: 'pagchat-host' });
    await op('PUT', `/v1/admin/principals/${prn.principal_id}/limits`, { max_peers: 2, creates_per_hour: 10, kinds: ['pagchat-bot'] });
    const scoped = await op('POST', '/v1/admin/peers', { kind: 'pagchat-bot', name: 'pagchat-ops', principal_id: prn.principal_id });
    await op('PUT', `/v1/admin/peers/${scoped.peer_id}/scopes`, { allow: [demo.operator_peer] });
    for (const name of ['seed-bot-1', 'seed-bot-2']) {
      const made = await call(demo.url, prn.secret, 'POST', '/v1/peers', { kind: 'pagchat-bot', name, idempotency_key: 'create-' + name });
      assert(made.peer && made.peer.created_by === prn.principal_id, `created ${name} without created_by`);
    }
    // directory_updated from the creates refreshes the card on the peers tag.
    const prow = card.locator('tbody tr', { hasText: 'pagchat-host' });
    const cap = prow.locator('td').nth(1);
    await cap.filter({ hasText: /^2\/2$/ }).waitFor();
    assert(await card.locator('tbody tr').count() === 1, 'the card lists unlimited principals');
    assert(await cap.evaluate(el => el.classList.contains('warn')), '2/2 is not amber');
    const scopes = prow.locator('td').nth(5);
    assert(await scopes.textContent() === 'none', 'default scopes are not "none"');
    assert(await scopes.evaluate(el => el.classList.contains('warn')), 'unscoped defaults are not amber');
    assert(await prow.locator('td').nth(4).textContent() === 'pagchat-bot', 'kinds column');
    assert(await prow.locator('td').nth(3).textContent() === '2/10', 'creates per hour column');
    const amber = await cap.evaluate(el => getComputedStyle(el).color);
    assert(amber === 'rgb(251, 191, 36)', `cap colour is ${amber}`);
    await page.locator('#main').evaluate(el => { el.scrollTop += document.getElementById('d-principals').getBoundingClientRect().top - 140; });
    await shot('ln04-dash-principals');
  });

  await withTui({}, async ({ demo, keys, waitFor, shot }) => {
    await waitFor(' live ');
    await keys('3');
    await waitFor('presence is self-reported');
    await enrolLauncher(demo);
    await waitFor(LAUNCHER);
    const raw = await run('tmux', ['-L', `oui-${process.pid}`, 'capture-pane', '-t', 'ui:0.0', '-e', '-p'], { env: { ...process.env, TMUX: '' } });
    assert(new RegExp(`\\x1b\\[[0-9;]*38;2;250;204;21[0-9;]*m${LAUNCHER}`).test(raw), 'the launcher row is not drawn in the launcher colour');
    await shot('ln04-tui-dash', LAUNCHER);
  });
  console.log('Launcher dash passed: launcher kind colour on web and TUI, principals card at cap with unscoped warning, absent when empty');
} finally { await cleanBuild(); }
