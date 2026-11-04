// Launcher Task 4: the launcher kind colour (web and TUI) and the dashboard's
// Principals card, against a real demo hub. Fixtures are built at run time:
// the seed has no launcher and no principal with limits.
import { assert, cleanBuild, run, withDemo } from './lib.mjs';
import { withTui } from './tmux.mjs';
import { principalRows } from '../../internal/ui/static/principals-model.js';

const LAUNCHER = 'ln-xsm';
const BLUE = 'rgb(96, 165, 250)'; // #60a5fa, a hue no state token uses

async function call(url, secret, method, path, body) {
  const headers = { 'Content-Type': 'application/json', ...(secret ? { Authorization: `Bearer ${secret}` } : {}) };
  const response = await fetch(url + path, { method, headers, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
  const text = await response.text();
  assert(response.ok, `${method} ${path}: ${response.status}`);
  return text ? JSON.parse(text) : null;
}

// enrolLauncher adds a launcher peer as the operator, exchanges its token for
// a secret (asPeer knows only seeded peers) and reports working presence at
// the hub's own now, so its age reads like every other peer's.
async function enrolLauncher(demo) {
  const op = (method, path, body) => call(demo.url, demo.operator_secret, method, path, body);
  const added = await op('POST', '/v1/admin/peers', { kind: 'launcher', name: LAUNCHER });
  const enrolled = await call(demo.url, '', 'POST', '/v1/enrol', { token: added.enrol_token });
  const { now } = await op('GET', '/v1/admin/inbox');
  await call(demo.url, enrolled.secret, 'POST', '/v1/presence', { state: 'working', at: now, quota: { weekly_remaining_pct: 72 } });
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

    // Initial: a launcher peer reporting working, its kind dot in blue.
    await enrolLauncher(demo);
    const row = sec('peers').locator('tbody tr', { has: page.locator('td:first-child > span:first-child', { hasText: new RegExp('^' + LAUNCHER + '$') }) });
    await row.locator('td', { hasText: /^72%$/ }).waitFor();
    await row.locator('td').nth(2).filter({ hasText: /^working/ }).waitFor();
    // Reloads replace table rows, so each poll finds the live row and reads
    // its dot colour in the same callback; no fixed sleep.
    await page.waitForFunction(([name, want]) => {
      const tr = [...document.querySelectorAll('.dash-peers tbody tr')].find(el => el.querySelector('td:first-child > span')?.textContent === name);
      const dot = tr?.querySelector('i.kd.k-launcher');
      return !!dot && getComputedStyle(dot).backgroundColor === want;
    }, [LAUNCHER, BLUE], { timeout: 10000 }).catch(() => { throw new Error(`launcher kind dot never became ${BLUE}`); });
    // Keep the Peers heading and thead in view, with the launcher row below.
    await page.waitForFunction(name => {
      const main = document.getElementById('main'), sec = document.getElementById('d-peers');
      const tr = [...document.querySelectorAll('.dash-peers tbody tr')].find(el => el.querySelector('td:first-child > span')?.textContent === name);
      if (!main || !sec || !tr) return false;
      main.scrollTop += sec.getBoundingClientRect().top - main.getBoundingClientRect().top - 16;
      const head = sec.querySelector('thead')?.getBoundingClientRect(), r = tr.getBoundingClientRect();
      return !!head && head.top >= 0 && r.top > head.bottom && r.bottom < innerHeight;
    }, LAUNCHER, { timeout: 10000 }).catch(() => { throw new Error('the Peers thead and the launcher row do not fit one screen'); });
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
    await cap.filter({ hasText: /^2\/2 · full$/ }).waitFor();
    assert(await card.locator('tbody tr').count() === 1, 'the card lists unlimited principals');
    assert(await cap.evaluate(el => el.classList.contains('warn')), '2/2 is not amber');
    assert(await card.locator('thead th').nth(1).textContent() === 'Active/max', 'active column heading');
    const scopes = prow.locator('td').nth(5);
    assert(await scopes.textContent() === 'none · others scoped', 'default scopes do not say why they warn');
    assert(await scopes.evaluate(el => el.classList.contains('warn')), 'unscoped defaults are not amber');
    const kinds = prow.locator('td').nth(4);
    assert(await kinds.locator('span.pill > i.kd.k-pagchat-bot').count() === 1 && await kinds.textContent() === 'pagchat-bot', 'kinds column is not one kind pill');
    assert(await prow.locator('td').nth(2).evaluate(el => el.classList.contains('mono')), 'disabled column is not mono');
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
    assert(new RegExp(`\\x1b\\[[0-9;]*38;2;96;165;250[0-9;]*m${LAUNCHER}`).test(raw), 'the launcher row is not drawn in the launcher colour');
    await shot('ln04-tui-dash', LAUNCHER);
  });
  console.log('Launcher dash passed: launcher kind colour on web and TUI, principals card at cap with unscoped warning, absent when empty');
} finally { await cleanBuild(); }
