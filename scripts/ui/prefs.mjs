// Hub preferences Task 6: the Settings page against a real demo hub, both
// views (List, the default, and Policy) at desk (1440×900) and phone
// (390×844). Every state comes from real requests: holds for loading and
// saving, a competing operator PATCH for the 412, a fulfilled 503 for the
// failed save, and the hub's own preview for the dangerous change. The demo
// starts with events.refresh_interval set by VECNET_EVENTS_REFRESH_INTERVAL,
// so the read-only flag row is real. DB retention consumers are installed;
// attachment expiry still needs R2, absent on the disk-backend demo.
//
// Shots: prefs-<list|policy>-<state>-<desk|phone>.png (docs/plans/shots/oui).
import { assert, cleanBuild, withDemo } from './lib.mjs';

const SIZES = { desk: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };
const VIEWS = ['list', 'policy'];
const RETENTION = ['retention.ack_events', 'retention.archive_mode', 'retention.history'];
const sid = key => '#st-' + key.replace(/[^A-Za-z0-9]/g, '-');

try {
  await withDemo({ demoArgs: ['--pref-flag', 'events.refresh_interval=900@VECNET_EVENTS_REFRESH_INTERVAL'] }, async ctx => {
    const { page, shot, demo, hold, allowConsole, countRequests, quiet } = ctx;
    // Chromium logs each refused fetch: the 412 and 503 this script provokes.
    allowConsole(/status of 412/);
    allowConsole(/status of 503/);
    const patches = [];
    page.on('request', req => {
      if (new URL(req.url()).pathname === '/v1/admin/prefs' && req.method() === 'PATCH') patches.push({ body: JSON.parse(req.postData() || '{}'), ifMatch: req.headers()['if-match'] });
    });
    const operator = async (method, path, { body, headers = {} } = {}) => fetch(demo.url + path, {
      method, headers: { Authorization: `Bearer ${demo.operator_secret}`, 'Content-Type': 'application/json', ...headers }, ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    const hubPrefs = async () => {
      const r = await operator('GET', '/v1/admin/prefs');
      assert(r.ok, `GET prefs ${r.status}`);
      const body = await r.json();
      return { etag: r.headers.get('etag'), revision: body.revision, runtime: body.runtime, rows: new Map(body.preferences.map(p => [p.key, p])) };
    };
    const view = async v => {
      await page.locator(`.st-view button[data-fk=view-${v}]`).click();
      await page.locator(`body[data-st-view=${v}]`).waitFor();
    };
    const open = async key => {
      const tok = page.locator(`[data-fk="tok-${key}"]`);
      if (await tok.getAttribute('aria-expanded') !== 'true') await tok.click();
      await page.locator(`#drawer-${sid(key).slice(1)}`).waitFor();
    };
    const into = async selector => {
      await page.locator(selector).first().evaluate(el => el.scrollIntoView({ block: 'center' }));
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
    };
    const noSideways = async name => {
      const w = await page.evaluate(() => document.documentElement.scrollWidth);
      const vw = page.viewportSize().width;
      assert(w <= vw, `${name}: the page scrolls sideways at ${vw} px (${w})`);
    };
    // both: for each view and size, run that view's prep, then shoot.
    const both = async (state, prep, { views = VIEWS } = {}) => {
      for (const v of views) {
        await view(v);
        for (const [size, vp] of Object.entries(SIZES)) {
          await page.setViewportSize(vp);
          if (prep[v]) await prep[v]();
          await noSideways(`${state} ${v} ${size}`);
          await shot(`prefs-${v}-${state}-${size}`);
        }
      }
      await page.setViewportSize(SIZES.desk);
    };
    // The page's own top: on a phone the sidebar (thread lists folded behind
    // Threads) stacks above it.
    const top = () => page.evaluate(() => { document.getElementById('main').scrollTop = 0; if (innerWidth <= 720) document.querySelector('.st-page').scrollIntoView({ block: 'start' }); else scrollTo(0, 0); });
    const section = id => () => page.locator('#' + id).evaluate(el => el.scrollIntoView({ block: 'start' }));
    const bar = () => into('.st-bar');
    // A save's toast lasts 2.5 s; states that are not a save wait it out.
    const toastGone = () => page.waitForFunction(() => { const t = document.getElementById('toast'); return !t.classList.contains('on') && t.getBoundingClientRect().bottom <= 0; });
    // Compare every wire row in both presentations, including Policy's lanes
    // and drawers. Availability comes from the hub, independently of source.
    const rowsMatchHub = async hub => {
      for (const v of VIEWS) {
        await view(v);
        for (const [key, row] of hub.rows) {
          const unavailable = row.runtime === 'unavailable';
          const ttlOverride = ['presence.default_ttl', 'presence.grokbot_ttl'].includes(key) && row.editable && row.runtime === 'live';
          const locked = unavailable || !row.editable || (row.source === 'flag' && !ttlOverride);
          const lane = v === 'policy' && ['retention.history', 'retention.ack_events', 'blobs.expiry', 'agents.default_ttl'].includes(key);
          if (v === 'policy' && !lane && key !== 'retention.archive_mode') await open(key);
          const container = page.locator(v === 'policy' && key === 'retention.archive_mode' ? '.st-arch' : `[data-row="${key}"]`).first();
          assert(await container.locator('.st-src').first().textContent() === row.source, `${key}: wrong source in ${v}`);
          assert(await container.locator('.st-src.s-unavailable').count() === Number(unavailable), `${key}: availability badge disagrees with hub in ${v}`);
          const controls = lane ? container.locator('.st-stop') : page.locator(sid(key));
          assert(await controls.count() > 0, `${key}: no control in ${v}`);
          const paused = lane && key === 'retention.history' && hub.rows.get('retention.archive_mode').value;
          if (lane) assert(await container.evaluate(el => el.classList.contains('is-paused')) === !!paused, `${key}: archive pause disagrees with hub`);
          for (const control of await controls.all()) assert(await control.isDisabled() === (locked || !!paused), `${key}: editability disagrees with hub in ${v}`);
          if (unavailable) {
            const reason = v === 'policy' && key === 'retention.archive_mode' ? page.locator('.st-arch-why') : container;
            assert((await reason.textContent()).includes(row.unavailable_reason), `${key}: hub's unavailable reason missing in ${v}`);
            assert(await container.locator('.st-sto, [role=meter]').count() === 0, `${key}: unavailable row claims storage figures in ${v}`);
          }
          // Some lanes also have a Policy sentence/drawer editor.
          if (lane && await page.locator(`[data-fk="tok-${key}"]`).count()) {
            await open(key);
            const drawer = page.locator('#drawer-' + sid(key).slice(1));
            assert(await drawer.locator('.st-src').first().textContent() === row.source, `${key}: drawer source disagrees with hub`);
            assert(await drawer.locator('.s-unavailable').count() === Number(unavailable), `${key}: drawer availability disagrees with hub`);
            assert(await page.locator(sid(key)).isDisabled() === locked, `${key}: drawer editability disagrees with hub`);
          }
        }
      }
    };

    // lib.mjs hold() unroutes as it releases; this route keeps Playwright's
    // interception on so each held request is continued exactly once.
    await page.route('**/__keep-interception__', route => route.continue());
    await page.locator('#conn.live').waitFor();
    // Nav: Settings sits between Dashboard and Add agent.
    const navIds = await page.locator('nav.side > .nav').first().locator(':scope > a, :scope > button').evaluateAll(els => els.map(e => e.id));
    assert(navIds.join() === 'nav-inbox,nav-new,nav-dash,nav-threads,nav-peers,nav-settings,nav-add', `sidebar nav ${navIds}`);
    // Appearance is set before first paint; dark here.
    assert(await page.evaluate(() => document.documentElement.dataset.theme) === 'dark', 'theme not set on <html>');

    // ---------- loading ----------
    const release = await hold('**/v1/admin/prefs');
    await page.locator('#nav-settings').click();
    await page.locator('#nav-settings.on').waitFor();
    await page.locator('.st-page .loading').waitFor();
    assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.settingsView')) === null, 'a view was stored before any choice');
    assert(await page.locator('body[data-st-view=list]').count() === 1, 'List is not the default view');
    await both('loading', { list: top, policy: top });
    await release();
    await page.locator('.st-row').first().waitFor();
    await view('list');
    await quiet();

    // ---------- initial: List default, six sections, wire sources ----------
    const headings = await page.locator('.st-sec h2').allTextContents();
    assert(headings.join('|') === 'Retention & storage|Files|Notifications & presence|Agents defaults|Automatic archiving|Appearanceonly this browser', `sections ${headings}`);
    assert((await page.locator('#sec-automatic_archiving').textContent()).includes('existing agents and threads hourly'), 'archive rules have new-only copy');
    const hub0 = await hubPrefs();
    assert(hub0.runtime?.event_retention_enabled === false, 'real demo must report the default-off pruning gate');
    const gateOff = async () => {
      const ack = page.locator('[data-row="retention.ack_events"]').first();
      const note = ack.locator('[data-event-retention="disabled"]');
      assert(await note.textContent() === 'Acknowledged-event pruning gate: disabled (last reported by hub). Saving this policy does not enable pruning.', 'last-reported disabled note missing beside ack retention');
      await into('[data-row="retention.ack_events"]');
      assert(await note.isVisible(), 'disabled note is not visible beside centered ack row');
    };
    await both('gate-off', { list: gateOff, policy: gateOff });
    await rowsMatchHub(hub0);
    await both('initial', { list: top, policy: top });

    // ---------- unavailable adapters: shown, locked, never fake ----------
    for (const key of RETENTION) assert(hub0.rows.get(key).runtime === 'live' && hub0.rows.get(key).editable, `${key}: production DB retention consumer not installed`);
    assert(hub0.rows.get('blobs.expiry').runtime === 'unavailable' && !hub0.rows.get('blobs.expiry').editable, 'disk demo unexpectedly has an attachment expiry consumer');
    assert(await page.locator('.st-sto, [role=meter]').count() === 0, 'disk demo claims storage figures');
    assert(await page.locator('.st-fc .s-unavailable').count() === 1, 'the storage forecast claims figures');
    await both('unavailable', { list: () => into('[data-row="blobs.expiry"]'), policy: async () => { await open('blobs.expiry'); await into('#drawer-st-blobs-expiry'); } });

    // ---------- read-only flag/env row ----------
    const flag = hub0.rows.get('events.refresh_interval');
    assert(flag.source === 'flag' && flag.origin === 'VECNET_EVENTS_REFRESH_INTERVAL' && flag.value === 900, 'demo flag row missing');
    for (const v of VIEWS) {
      await view(v);
      if (v === 'policy') await open('events.refresh_interval');
      assert(await page.locator(sid('events.refresh_interval')).isDisabled(), `flag row editable in ${v}`);
      assert((await page.locator(`[data-row="events.refresh_interval"] .st-lock.flag`).textContent()).includes('VECNET_EVENTS_REFRESH_INTERVAL'), `flag origin missing in ${v}`);
    }
    await both('flag', { list: () => into('[data-row="events.refresh_interval"]'), policy: async () => { await open('events.refresh_interval'); await into('#drawer-st-events-refresh-interval'); } });

    // ---------- dirty: edits in List carry into Policy ----------
    await view('list');
    await page.locator(sid('agents.default_max_depth')).fill('24');
    await page.locator(sid('agents.thread_budget')).fill('800');
    await page.locator(sid('rates.sends_per_minute')).fill('90');
    await view('policy');
    await open('agents.default_ttl');
    await page.locator(sid('agents.default_ttl')).fill('12h');
    assert(await page.locator('#settings-count').textContent() === '4', 'nav badge is not the dirty count');
    assert(await page.locator('[data-fk="tok-agents.thread_budget"].t-edited').count() === 1, 'a List edit is not shown in Policy');
    await view('list');
    assert(await page.locator(sid('agents.default_ttl')).inputValue() === '12h', 'a Policy edit is not shown in List');
    assert(await page.locator('[data-row="agents.default_ttl"] .st-src').first().textContent() === 'edited', 'edited badge missing');
    await both('dirty', { list: section('sec-agents_defaults'), policy: () => into('.st-p:nth-of-type(3)') });
    // At phone width the document scrolls: the save bar pins to the viewport
    // from the page's top, not only at the end of the form.
    await page.setViewportSize(SIZES.phone);
    await top();
    const stBar = await page.locator('.st-bar').evaluate(el => { const r = el.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, vh: innerHeight, y: scrollY }; });
    assert(stBar.top >= 0 && stBar.bottom <= stBar.vh, `dirty save bar outside the phone viewport: ${JSON.stringify(stBar)}`);
    await page.setViewportSize(SIZES.desk);

    // ---------- invalid input: beside the field, Save blocked ----------
    await view('list');
    await page.locator(sid('agents.default_max_depth')).fill('99');
    await page.locator('[data-row="agents.default_max_depth"] .st-err', { hasText: 'between 1 and 64' }).waitFor();
    assert(await page.locator('.st-bar [data-fk=save]').isDisabled(), 'Save enabled with an invalid field');
    assert(await page.locator(sid('agents.default_max_depth')).getAttribute('aria-invalid') === 'true', 'aria-invalid missing');
    await page.locator('.st-bar .st-jump').click();
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'st-agents-default-max-depth', 'Show did not focus the invalid field');
    // Show reveals the invalid row when the search, the Flags filter or both hide it.
    const filters = {
      search: () => page.locator('.st-q').fill('presence'),
      flags: () => page.locator('[data-fk=only-flags]').click(),
      'search and flags': async () => { await page.locator('[data-fk=only-flags]').click(); await page.locator('.st-q').fill('refresh'); },
    };
    for (const [name, hide] of Object.entries(filters)) {
      await hide();
      assert(await page.locator('[data-row="agents.default_max_depth"]').count() === 0, `${name}: the invalid row is not hidden`);
      await page.locator('.st-bar .st-jump').click();
      assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'st-agents-default-max-depth', `${name}: Show did not reveal and focus the invalid field`);
      assert(await page.locator('.st-q').inputValue() === '' && await page.locator('[data-fk=only-all][aria-pressed=true]').count() === 1, `${name}: Show left the filter on`);
    }
    await both('invalid', { list: () => into('[data-row="agents.default_max_depth"]'), policy: async () => { await open('agents.default_max_depth'); await into('#drawer-st-agents-default-max-depth'); } });
    await view('list');
    await page.locator(sid('agents.default_max_depth')).fill('24');

    // ---------- saving (held) → saved ----------
    assert(patches.length === 0, 'a PATCH before Save');
    const releaseSave = await hold('**/v1/admin/prefs');
    await page.locator('.st-bar [data-fk=save]').click();
    await page.locator('.st-bar.is-saving').waitFor();
    assert(await page.locator(sid('agents.thread_budget')).isDisabled(), 'fields editable while saving');
    await both('saving', { list: bar, policy: bar });
    await releaseSave();
    await page.locator('.st-bar').waitFor({ state: 'detached' });
    await view('list');
    assert(patches.length === 1, `expected one atomic PATCH, saw ${patches.length}`);
    assert(patches[0].ifMatch === hub0.etag, 'PATCH If-Match is not the GET ETag');
    assert(JSON.stringify(patches[0].body) === JSON.stringify({ set: { 'agents.default_max_depth': 24, 'agents.default_ttl': 43200, 'agents.thread_budget': 800, 'rates.sends_per_minute': 90 }, reset: [] }), `patch ${JSON.stringify(patches[0].body)}`);
    const hub1 = await hubPrefs();
    for (const key of ['agents.default_max_depth', 'agents.default_ttl', 'agents.thread_budget', 'rates.sends_per_minute']) assert(hub1.rows.get(key).source === 'stored', `${key} not stored on the hub`);
    assert(await page.locator('[data-row="agents.thread_budget"] .st-src').first().textContent() === 'stored', 'stored badge missing after save');
    assert((await page.locator('.st-sr').textContent()).startsWith('Saved 4 changes'), 'save not announced');
    await both('saved', { list: section('sec-agents_defaults'), policy: () => into('.st-p:nth-of-type(3)') });

    // ---------- save back to the built-in default: the hub drops the override ----------
    await view('list');
    await page.locator(sid('agents.thread_budget')).fill('500');
    assert(await page.locator('[data-row="agents.thread_budget"] .st-src').first().textContent() === 'edited', 'default shown before the hub said so');
    await page.locator('.st-bar [data-fk=save]').click();
    await page.locator('.st-bar').waitFor({ state: 'detached' });
    assert(JSON.stringify(patches.at(-1).body) === JSON.stringify({ set: { 'agents.thread_budget': 500 }, reset: [] }), `default save ${JSON.stringify(patches.at(-1).body)}`);
    assert((await hubPrefs()).rows.get('agents.thread_budget').source === 'default', 'hub did not drop the override');
    assert(await page.locator('[data-row="agents.thread_budget"] .st-src').first().textContent() === 'default', 'default badge missing');
    await both('default', { list: () => into('[data-row="agents.thread_budget"]'), policy: async () => { await open('agents.thread_budget'); await into('#drawer-st-agents-thread-budget'); } });

    // ---------- dangerous change: preview, cancel writes nothing, confirm ----------
    await view('list');
    const before = patches.length;
    await page.locator(sid('presence.default_ttl')).fill('30m');
    const previewed = page.waitForResponse(r => new URL(r.url()).pathname === '/v1/admin/prefs/preview');
    await page.locator('.st-bar [data-fk=save]').click();
    const token = (await (await previewed).json()).confirm_token;
    assert(typeof token === 'string' && token.length > 20, 'no preview token');
    const dialog = page.locator('dialog.st-cf[open]');
    await dialog.waitFor();
    await toastGone();
    assert(patches.length === before, 'a PATCH before confirmation');
    assert(await dialog.locator('[data-risk=presence_shortened]').count() === 1, 'presence risk not shown');
    assert(await page.evaluate(() => document.activeElement && document.activeElement.dataset.cf) === 'no', 'focus is not on Cancel');
    // The hub's token and the draft never reach storage or the URL.
    const kept = await page.evaluate(() => [location.href, JSON.stringify({ ...localStorage }), JSON.stringify({ ...sessionStorage }), document.cookie].join('\n'));
    assert(!kept.includes(token) && !kept.includes('30m') && !kept.includes('1800'), 'token or draft leaked to storage or the URL');
    assert(await page.evaluate(() => Object.keys(localStorage).sort().join()) === 'vecnet.ui.secret,vecnet.ui.settingsView', `storage keys ${await page.evaluate(() => Object.keys(localStorage).join())}`);
    for (const [size, vp] of Object.entries(SIZES)) { await page.setViewportSize(vp); await shot(`prefs-list-confirm-${size}`); }
    await page.setViewportSize(SIZES.desk);
    await page.keyboard.press('Escape');
    await page.locator('dialog.st-cf[open]').waitFor({ state: 'detached' });
    await page.waitForFunction(() => document.activeElement && document.activeElement.dataset.fk === 'save', null, { timeout: 2000 });
    assert(patches.length === before, 'Escape wrote');
    assert(await page.locator(sid('presence.default_ttl')).inputValue() === '30m', 'cancel dropped the draft');
    await view('policy');
    await page.locator('.st-bar [data-fk=save]').click();
    await page.locator('dialog.st-cf[open]').waitFor();
    for (const [size, vp] of Object.entries(SIZES)) { await page.setViewportSize(vp); await shot(`prefs-policy-confirm-${size}`); }
    await page.setViewportSize(SIZES.desk);
    await page.locator('dialog.st-cf [data-cf=ok]').click();
    await page.locator('.st-bar').waitFor({ state: 'detached' });
    assert(patches.length === before + 1 && patches.at(-1).body.confirm.length === 1, 'confirmed PATCH missing its one token');
    assert((await hubPrefs()).rows.get('presence.default_ttl').value === 1800, 'confirmed value not on the hub');

    // ---------- live retention: edit all three keys in each view ----------
    await toastGone();
    const retentionPrep = { list: section('sec-retention_storage'), policy: () => into('.st-time') };
    await both('retention-available', retentionPrep);
    const confirmShots = async (v, state) => {
      for (const [size, vp] of Object.entries(SIZES)) {
        await page.setViewportSize(vp);
        // The retrospective risk list is taller than a phone. Keep its
        // bounds, typed approval and final action in the confirmation shot.
        if (state === 'retention-existing-confirm') await dialog.locator('.st-cf-acts').scrollIntoViewIfNeeded();
        await noSideways(`${state} ${v} ${size}`);
        await shot(`prefs-${v}-${state}-${size}`);
      }
      await page.setViewportSize(SIZES.desk);
    };
    for (const v of VIEWS) {
      await view(v);
      const base = await hubPrefs(), start = patches.length;
      const values = v === 'list'
        ? { 'retention.ack_events': 259200, 'retention.archive_mode': true, 'retention.history': 2592000 }
        : { 'retention.ack_events': 86400, 'retention.archive_mode': false, 'retention.history': 604800 };
      if (v === 'list') {
        await page.locator(sid('retention.ack_events')).fill('3d');
        await page.locator(sid('retention.history')).fill('30d');
        await page.locator(sid('retention.archive_mode')).click();
      } else {
        await page.locator('.st-arch').click();
        for (const key of ['retention.ack_events', 'retention.history']) await page.locator(`[data-fk="lane-${key}-${values[key]}"]`).click();
      }
      assert(await page.locator('#settings-count').textContent() === '3', `${v}: retention dirty count`);
      // Switching views preserves every edited value and its source badge.
      for (const otherView of VIEWS) {
        await view(otherView);
        for (const key of RETENTION) {
          const row = page.locator(otherView === 'policy' && key === 'retention.archive_mode' ? '.st-arch' : `[data-row="${key}"]`).first();
          assert(await row.locator('.st-src').first().textContent() === 'edited', `${key}: edited badge missing in ${otherView}`);
          if (key === 'retention.archive_mode') assert(await page.locator(sid(key)).getAttribute('aria-checked') === String(values[key]), `${otherView}: archive edit lost`);
          else if (otherView === 'policy') assert(await row.locator(`[data-fk="lane-${key}-${values[key]}"]`).getAttribute('aria-checked') === 'true', `${key}: lane edit lost`);
          else assert(await page.locator(sid(key)).inputValue() === (values[key] / 86400) + 'd', `${key}: List edit lost`);
        }
      }
      // Each view gets its own edit and dialog shots, without overwriting the
      // other view's states from its separate, real retention transaction.
      await view(v);
      for (const [size, vp] of Object.entries(SIZES)) {
        await page.setViewportSize(vp);
        await retentionPrep[v]();
        await noSideways(`retention-edited ${v} ${size}`);
        await shot(`prefs-${v}-retention-edited-${size}`);
      }
      await page.setViewportSize(SIZES.desk);
      const preview = async action => {
        const response = page.waitForResponse(r => new URL(r.url()).pathname === '/v1/admin/prefs/preview');
        await action();
        const r = await response;
        assert(r.ok(), `${v}: retention preview ${r.status()}`);
        const request = r.request(), body = request.postDataJSON(), result = await r.json();
        assert(request.headers()['if-match'] === base.etag && result.base_revision === base.revision, `${v}: preview not bound to base revision`);
        assert(JSON.stringify(body.set) === JSON.stringify(values) && body.reset.length === 0, `${v}: preview changed retention draft`);
        assert(typeof result.confirm_token === 'string' && result.confirm_token.length > 20, `${v}: retention preview has no token`);
        assert(patches.length === start && (await hubPrefs()).etag === base.etag, `${v}: preview wrote preferences`);
        await page.locator('dialog.st-cf[open]').waitFor();
        return { body, result };
      };
      const prospective = await preview(() => page.locator('.st-bar [data-fk=save]').click());
      assert(!prospective.body.apply_existing, `${v}: retrospective expiry enabled by default`);
      assert(await dialog.locator('[data-risk=retention_shortened]').count() === 2, `${v}: history or acknowledged-event shortening risk missing`);
      if (v === 'policy') assert(await dialog.locator('[data-risk=archive_disabled]').count() === 1, 'archive exit risk missing');
      assert(!await page.locator('#st-cf-existing').isChecked(), `${v}: existing-data choice not opt-in`);
      assert(await page.evaluate(() => document.activeElement?.dataset.cf) === 'no', `${v}: retention Cancel not focused`);
      await confirmShots(v, 'retention-confirm');
      await page.keyboard.press('Escape');
      await dialog.waitFor({ state: 'detached' });
      assert(patches.length === start && (await hubPrefs()).etag === base.etag, `${v}: cancelling retention preview wrote`);
      assert(await page.locator('#settings-count').textContent() === '3', `${v}: cancelling lost retention edits`);
      await preview(() => page.locator('.st-bar [data-fk=save]').click());
      await page.locator('#st-cf-existing').check();
      const existing = await preview(() => dialog.locator('[data-cf=ok]').click());
      assert(existing.body.apply_existing === true && existing.result.confirm_token !== prospective.result.confirm_token, `${v}: existing-data preview not separate`);
      assert(await dialog.locator('[data-risk=retention_apply_existing]').count() === 1, `${v}: retrospective risk missing`);
      const impact = existing.result.impact;
      assert(Number.isFinite(Date.parse(impact.cutoff_at)) && Number.isFinite(Date.parse(impact.not_before)) && Date.parse(impact.not_before) > Date.parse(impact.cutoff_at), `${v}: missing fixed cutoff or scheduled sweep`);
      for (const key of ['high_water', 'eligible', 'pinned', 'estimated_bytes']) assert(Number.isSafeInteger(impact[key]) && impact[key] >= 0, `${v}: invalid impact ${key}`);
      const shown = await dialog.locator('.st-cf-impact').textContent();
      for (const field of ['Cutoff', 'Not before', 'High-water', 'Eligible rows (estimate)', 'Pinned rows kept (estimate)', 'Bytes (estimate)', impact.cutoff_at, impact.not_before]) assert(shown.includes(field), `${v}: impact omitted ${field}`);
      assert((await dialog.textContent()).includes('docs/DEPLOY.md, Backups'), `${v}: backup guidance missing`);
      assert(await dialog.locator('[data-cf=ok]').isDisabled(), `${v}: trim confirmation enabled without typing`);
      await page.locator('#st-cf-typed').fill('trim');
      assert(await dialog.locator('[data-cf=ok]').isEnabled(), `${v}: typed trim did not enable confirmation`);
      const stored = await page.evaluate(() => [location.href, JSON.stringify({ ...localStorage }), JSON.stringify({ ...sessionStorage }), document.cookie].join('\n'));
      assert(!stored.includes(existing.result.confirm_token) && !stored.includes(prospective.result.confirm_token), `${v}: retention token leaked to storage or URL`);
      await confirmShots(v, 'retention-existing-confirm');
      await dialog.locator('[data-cf=ok]').click();
      await page.locator('.st-bar').waitFor({ state: 'detached' });
      assert(patches.length === start + 1, `${v}: retention confirmation did not send one PATCH`);
      const sent = patches.at(-1);
      assert(sent.ifMatch === base.etag && sent.body.apply_existing === true && sent.body.confirm.length === 1 && sent.body.confirm[0] === existing.result.confirm_token, `${v}: PATCH not bound to confirmed retrospective preview`);
      assert(JSON.stringify(sent.body.set) === JSON.stringify(values) && sent.body.reset.length === 0, `${v}: PATCH changed approved retention draft`);
      const saved = await hubPrefs();
      assert(saved.revision === base.revision + 1 && saved.etag !== base.etag, `${v}: retention revision did not advance once`);
      for (const [key, value] of Object.entries(values)) assert(saved.rows.get(key).value === value && saved.rows.get(key).source === (value === saved.rows.get(key).default ? 'default' : 'stored'), `${v}: confirmed ${key} missing from hub`);
      await rowsMatchHub(saved);
      const replay = await operator('PATCH', '/v1/admin/prefs', { body: sent.body, headers: { 'If-Match': base.etag } });
      assert(replay.status === 412 && (await replay.json()).error.code === 'prefs_revision_conflict', `${v}: old revision accepted confirmation replay`);
      assert((await hubPrefs()).etag === saved.etag, `${v}: rejected replay changed revision`);
      await toastGone();
      await view(v);
      for (const [size, vp] of Object.entries(SIZES)) {
        await page.setViewportSize(vp);
        await retentionPrep[v]();
        await noSideways(`retention-saved ${v} ${size}`);
        await shot(`prefs-${v}-retention-saved-${size}`);
      }
      await page.setViewportSize(SIZES.desk);
    }

    // ---------- 412: another operator saved first ----------
    await view('list');
    await page.locator(sid('agents.default_ttl')).fill('2d');
    await page.locator(sid('rates.threads_per_hour')).fill('30');
    const fresh = await hubPrefs();
    const other = await operator('PATCH', '/v1/admin/prefs', { body: { set: { 'agents.default_ttl': 7200 } }, headers: { 'If-Match': fresh.etag } });
    assert(other.ok, `competing PATCH ${other.status}`);
    await page.locator('.st-bar [data-fk=save]').click();
    await page.locator('[data-note=conflict]').waitFor();
    await toastGone();
    assert(await page.locator(sid('agents.default_ttl')).inputValue() === '2d', 'conflict replaced the draft');
    assert((await hubPrefs()).rows.get('agents.default_ttl').value === 7200, 'the competing value was overwritten');
    assert((await page.locator('[data-note=conflict] .st-diff').textContent()).includes('hub now 2 h'), 'conflict does not show the latest value');
    assert(await page.locator('.st-bar [data-fk=save]').isDisabled(), 'Save enabled before the conflict is reconciled');
    // Typing must not rebuild (and so re-announce) the alert.
    await page.evaluate(() => { window.__note = document.querySelector('[data-note=conflict] [role=alert]'); });
    await page.locator(sid('agents.default_ttl')).fill('3d');
    await page.locator(sid('agents.default_ttl')).fill('2d');
    assert(await page.evaluate(() => window.__note === document.querySelector('[data-note=conflict] [role=alert]') && window.__note.isConnected), 'the conflict alert was rebuilt by typing');
    assert((await page.locator('[data-note=conflict] .st-diff').textContent()).includes('yours 2 d'), 'the diff does not follow the draft');
    await both('conflict', { list: () => into('[data-note=conflict]'), policy: () => into('[data-note=conflict]') });
    await view('list');
    await page.locator('[data-fk=adopt]').click();
    await page.locator('[data-note=conflict]').waitFor({ state: 'detached' });
    assert(await page.locator(sid('agents.default_ttl')).inputValue() === '2d', 'adopting dropped the draft');
    await page.locator('.st-bar [data-fk=save]').click();
    await page.locator('.st-bar').waitFor({ state: 'detached' });
    assert((await hubPrefs()).rows.get('agents.default_ttl').value === 172800, 'reconciled save missing');

    // ---------- failed save: the hub's storage is down ----------
    await page.locator(sid('rates.threads_per_hour')).fill('40');
    const failPatch = route => (route.request().method() === 'PATCH'
      ? route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'prefs_storage_unavailable', message: 'Preference storage is unavailable.', details: {} } }) })
      : route.continue());
    await page.route('**/v1/admin/prefs', failPatch);
    await page.locator('.st-bar [data-fk=save]').click();
    await page.locator('[data-note=error]').waitFor();
    await toastGone();
    assert(await page.locator(sid('rates.threads_per_hour')).inputValue() === '40', 'failed save dropped the draft');
    await both('error', { list: top, policy: top });
    await page.unroute('**/v1/admin/prefs', failPatch);
    await view('list');
    await page.locator('.st-bar [data-fk=undo]').click();
    assert(await page.locator('.st-bar').count() === 0 && await page.locator(sid('rates.threads_per_hour')).inputValue() === '30', 'Undo did not restore the saved value');

    // ---------- empty search ----------
    await page.locator('.st-q').fill('no setting is called this');
    await page.locator('.empty', { hasText: 'No settings match.' }).waitFor();
    await both('empty', { list: top }, { views: ['list'] });
    await page.locator('.st-q').fill('');

    // ---------- leave guard: a dirty page asks before another route ----------
    await page.locator(sid('agents.default_max_depth')).fill('30');
    let asked = '';
    page.once('dialog', d => { asked = d.message(); d.dismiss(); });
    await page.locator('#nav-inbox').click();
    await page.waitForFunction(() => location.hash === '#settings');
    assert(asked.includes('1 unsaved change'), `leave prompt: ${asked}`);
    assert(await page.locator(sid('agents.default_max_depth')).inputValue() === '30', 'staying lost the draft');
    // A resource landing after the hash changes but before its hashchange
    // event runs redraws the route too; it must ask, not unmount the draft.
    // Injected order: pushState moves the hash without a hashchange event
    // (the state a click leaves until its queued event runs), then a real
    // Other Threads reload lands and redraws; the late event is sent after.
    let prompts = 0;
    asked = '';
    const onLeave = d => { prompts++; asked = d.message(); d.dismiss(); };
    page.on('dialog', onLeave);
    const others = page.waitForResponse(r => { const u = new URL(r.url()); return u.pathname === '/v1/admin/threads' && u.searchParams.has('exclude_participant'); });
    await page.evaluate(() => history.pushState(null, '', '#inbox'));
    assert(prompts === 0 && await page.locator('body[data-route=settings]').count() === 1, 'the hash move alone ran the route');
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await others;
    await page.waitForFunction(() => location.hash === '#settings' || document.body.dataset.route !== 'settings');
    assert(await page.locator('body[data-route=settings]').count() === 1, 'a background redraw unmounted dirty Settings without asking');
    assert(prompts === 1 && asked.includes('1 unsaved change'), `background redraw leave prompt: ${prompts} ${asked}`);
    assert(await page.evaluate(() => location.hash) === '#settings', 'staying did not restore #settings');
    await page.evaluate(() => dispatchEvent(new HashChangeEvent('hashchange')));
    await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
    page.off('dialog', onLeave);
    assert(prompts === 1, `the late hashchange asked again (${prompts} prompts)`);
    assert(await page.locator('body[data-route=settings]').count() === 1 && await page.locator(sid('agents.default_max_depth')).inputValue() === '30', 'staying lost the Settings mount or its draft');
    await page.locator('.st-bar [data-fk=undo]').click();

    // ---------- appearance: local Apply, no hub request, other tabs follow ----------
    await page.locator('#sec-appearance').evaluate(el => el.scrollIntoView({ block: 'start' }));
    const prefsCalls = countRequests(req => new URL(req.url()).pathname.startsWith('/v1/admin/prefs'));
    await page.locator('[data-fk=look-theme-light]').click();
    await page.locator('[data-fk=look-density-compact]').click();
    assert(await page.evaluate(() => document.documentElement.dataset.theme) === 'dark', 'appearance applied before Apply');
    const other2 = await page.context().newPage();
    await other2.goto(`${demo.url}/ui/#inbox`);
    await other2.locator('#conn').waitFor();
    await page.locator('[data-fk=look-apply]').click();
    assert(await page.evaluate(() => [document.documentElement.dataset.theme, document.documentElement.dataset.density].join()) === 'light,compact', 'appearance not applied');
    assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.appearance.v1')) === '{"theme":"light","density":"compact"}', 'appearance not stored');
    assert(countRequests(req => new URL(req.url()).pathname.startsWith('/v1/admin/prefs')) === prefsCalls, 'appearance Apply called the hub');
    await other2.waitForFunction(() => document.documentElement.dataset.theme === 'light' && document.documentElement.dataset.density === 'compact');
    await other2.close();
    await toastGone();
    await both('appearance', { list: section('sec-appearance'), policy: () => into('.st-p:nth-of-type(4)') });
    // Back to the default look, from Policy this time.
    await view('policy');
    await open('look.theme');
    await page.locator('[data-fk=look-theme-system]').click();
    await page.locator('[data-fk=look-apply]').click();
    await open('look.density');
    await page.locator('[data-fk=look-density-comfortable]').click();
    await page.locator('[data-fk=look-apply]').click();
    assert(await page.evaluate(() => document.documentElement.dataset.theme) === 'dark', 'system theme is not dark under a dark scheme');

    // ---------- the view is remembered; 320 px has no sideways scroll ----------
    await page.reload({ waitUntil: 'load' });
    await page.locator('.st-lane').first().waitFor();
    assert(await page.locator('body[data-st-view=policy]').count() === 1, 'Policy not remembered across reload');
    await page.setViewportSize({ width: 320, height: 700 });
    await noSideways('policy 320');
    await view('list');
    await noSideways('list 320');
    await page.setViewportSize(SIZES.desk);
    assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.settingsView')) === 'list', 'view not stored as list');

    // Equal raw G600 is deliberate intent under inherited stored D7200.
    const G = 'presence.grokbot_ttl', D = 'presence.default_ttl';
    const startTTL = await hubPrefs();
    const seededTTL = await operator('PATCH', '/v1/admin/prefs', { body: { set: { [D]: 7200 } }, headers: { 'If-Match': startTTL.etag } });
    assert(seededTTL.ok, 'real stored D7200 setup failed');
    await page.reload({ waitUntil: 'load' });
    await page.locator('.st-row, .st-lane').first().waitFor();
    await quiet();
    const currentG = async (duration, key, inherited) => {
      const copy = page.locator('[data-fk=grok-current]');
      assert(await copy.isVisible(), 'committed Grok policy copy is not visible');
      const text = await copy.textContent();
      assert(text.includes('Current Grok presence: ' + duration) && text.includes(key) && text.includes('stored'), 'wrong committed duration/controller/source: ' + text);
      assert(!inherited || /inherit/i.test(text), 'inheritance omitted: ' + text);
    };
    for (const v of VIEWS) for (const [size, vp] of Object.entries(SIZES)) {
      await page.setViewportSize(vp);
      await view(v);
      if (v === 'policy') await open(G);
      const base = await hubPrefs(), start = patches.length;
      assert(base.rows.get(D).value === 7200 && base.rows.get(D).source === 'stored' && base.rows.get(G).value === 600 && base.rows.get(G).source === 'default', 'real inherited setup/provenance missing');
      assert(await page.locator(sid(G)).inputValue() === '10m', 'raw G editor replaced with inherited D');
      await currentG('2 h', D, true);
      const presenceWords = await page.locator('.st-page').textContent();
      assert(!/no presence report turns unknown after/i.test(presenceWords), 'never-reported presence copy is false');
      assert(/never.reported.*unknown|no presence report.*unknown/i.test(presenceWords), 'never-reported presence must be explained as unknown');
      assert(/older than|more than|greater than/i.test(presenceWords), 'reported-presence staleness must describe a strict boundary');
      const row = () => page.locator(`[data-row="${G}"]`).first();
      const action = row().getByRole('button', { name: 'Store override', exact: true });
      assert(await action.isEnabled() && await action.getAttribute('data-fk') === 'store-' + G, 'missing accessible override action/focus key');
      assert(await row().getByRole('button', { name: /^Reset/ }).count() === 0, 'nonstored G offers RESET');
      const previewsBefore = countRequests(req => new URL(req.url()).pathname === '/v1/admin/prefs/preview');
      await action.click();
      for (const otherView of VIEWS) {
        await view(otherView);
        if (otherView === 'policy') await open(G);
        assert(await page.locator(sid(G)).inputValue() === '10m' && await row().locator('.st-src').first().textContent() === 'edited', 'equal override draft lost across views');
        await currentG('2 h', D, true);
      }
      assert(await page.locator('#settings-count').textContent() === '1', 'equal intent not counted as unsaved');
      assert(patches.length === start && countRequests(req => new URL(req.url()).pathname === '/v1/admin/prefs/preview') === previewsBefore && (await hubPrefs()).etag === base.etag, 'Store override auto-saved or previewed');
      let leave = '';
      page.once('dialog', d => { leave = d.message(); d.dismiss(); });
      await page.locator('#nav-inbox').click();
      await page.waitForFunction(() => location.hash === '#settings');
      assert(leave.includes('1 unsaved change') && await page.evaluate(() => location.hash) === '#settings', 'equal intent bypassed navigation guard');
      await view(v);
      if (v === 'policy') await open(G);
      await page.locator(sid(G)).fill('30s');
      assert(await page.locator(sid(G)).getAttribute('aria-invalid') === 'true' && await page.locator('.st-bar [data-fk=save]').isDisabled(), 'G bounds/error not coupled to Save');
      assert((await row().locator('.st-err').textContent()).includes('between'), 'G field error not rendered');
      await page.locator(sid(G)).fill('10m');
      await noSideways('grok-draft ' + v + ' ' + size);
      await shot(`prefs-${v}-grok-draft-${size}`);
      const previewG = async () => {
        const response = page.waitForResponse(r => new URL(r.url()).pathname === '/v1/admin/prefs/preview');
        await page.locator('.st-bar [data-fk=save]').click();
        const r = await response, req = r.request(), result = await r.json();
        assert(r.ok() && req.headers()['if-match'] === base.etag && result.base_revision === base.revision, 'G preview authority/revision mismatch');
        assert(JSON.stringify(req.postDataJSON()) === JSON.stringify({ set: { [G]: 600 }, reset: [] }), 'raw-equal G SET missing from preview');
        assert(result.risks.length === 1 && result.risks[0].code === 'presence_shortened' && result.risks[0].keys.join() === G && result.risks[0].message.includes('grokbot 2h0m0s -> 10m0s'), 'resolved Grok shortening risk missing');
        assert(typeof result.confirm_token === 'string' && result.confirm_token.length > 20, 'no real G preview token');
        await page.locator('dialog.st-cf[open]').waitFor();
        assert((await page.locator('dialog.st-cf[open]').textContent()).includes('grokbot 2h0m0s -> 10m0s'), 'hub risk message missing from dialog');
        assert(patches.length === start && (await hubPrefs()).etag === base.etag, 'G preview wrote state');
        return result;
      };
      await previewG();
      await shot(`prefs-${v}-grok-confirm-${size}`);
      await page.keyboard.press('Escape');
      await page.locator('dialog.st-cf[open]').waitFor({ state: 'detached' });
      const cancelled = await hubPrefs();
      assert(cancelled.etag === base.etag && cancelled.revision === base.revision && cancelled.rows.get(G).source === 'default' && patches.length === start, 'cancel changed current policy');
      assert(await page.locator(sid(G)).inputValue() === '10m' && await row().locator('.st-src').first().textContent() === 'edited', 'cancel dropped equal draft');
      await currentG('2 h', D, true);
      const approved = await previewG();
      await page.locator('dialog.st-cf [data-cf=ok]').click();
      await page.locator('.st-bar').waitFor({ state: 'detached' });
      assert(patches.length === start + 1 && patches.at(-1).ifMatch === base.etag && JSON.stringify(patches.at(-1).body) === JSON.stringify({ set: { [G]: 600 }, reset: [], confirm: [approved.confirm_token] }), 'G PATCH bypassed exact confirmed proposal/token');
      const savedG = await hubPrefs();
      assert(savedG.revision === base.revision + 1 && savedG.etag !== base.etag && savedG.rows.get(G).source === 'stored' && savedG.rows.get(G).value === 600, 'G600 not stored by hub');
      assert(await row().locator('.st-src').first().textContent() === 'stored', 'source not adopted from reply');
      await currentG('10 min', G, false);
      await shot(`prefs-${v}-grok-stored-${size}`);
      await row().getByRole('button', { name: 'Reset to start-up value', exact: true }).click();
      assert(/hub.*start.up|start.up.*hub/i.test(await row().locator('.st-pend').textContent()), 'pending RESET must describe unknown starting layer');
      await currentG('10 min', G, false);
      assert((await hubPrefs()).etag === savedG.etag, 'RESET staged a write');
      await page.locator('.st-bar [data-fk=save]').click();
      await page.locator('.st-bar').waitFor({ state: 'detached' });
      const resetG = await hubPrefs();
      assert(JSON.stringify(patches.at(-1).body) === JSON.stringify({ set: {}, reset: [G] }) && patches.at(-1).ifMatch === savedG.etag, 'RESET became SET or lost ETag');
      assert(resetG.revision === savedG.revision + 1 && resetG.rows.get(G).source === 'default' && resetG.rows.get(G).value === 600 && resetG.rows.get(D).source === 'stored' && resetG.rows.get(D).value === 7200, 'RESET did not reveal inherited D7200');
      await currentG('2 h', D, true);
      await noSideways('grok-reset ' + v + ' ' + size);
      await shot(`prefs-${v}-grok-reset-${size}`);
    }
    await page.setViewportSize(SIZES.desk);
  });
  // Real bootstrap origins, narrow flag exception, and stored override/reset in both views/sizes.
  await withDemo({ demoArgs: ['--pref-flag', 'events.refresh_interval=900@VECNET_EVENTS_REFRESH_INTERVAL', '--pref-flag', 'presence.default_ttl=3600@VECNET_PRESENCE_TTL', '--pref-flag', 'presence.grokbot_ttl=600@--grok-ttl'] }, async ({ page, demo, shot, quiet }) => {
    const read = async () => {
      const r = await fetch(demo.url + '/v1/admin/prefs', { headers: { Authorization: `Bearer ${demo.operator_secret}` } });
      assert(r.ok, 'flag fixture GET failed');
      return { etag: r.headers.get('etag'), ...(await r.json()) };
    };
    await page.locator('#nav-settings').click();
    await page.locator('.st-row').first().waitFor();
    for (const v of VIEWS) for (const [size, vp] of Object.entries(SIZES)) {
      await page.setViewportSize(vp);
      await page.locator(`[data-fk=view-${v}]`).click();
      const base = await read();
      for (const [key, origin, raw] of [['presence.default_ttl', 'VECNET_PRESENCE_TTL', '1h'], ['presence.grokbot_ttl', '--grok-ttl', '10m']]) {
        if (v === 'policy') await page.locator(`[data-fk="tok-${key}"]`).click();
        const row = page.locator(`[data-row="${key}"]`).first(), wire = base.preferences.find(r => r.key === key);
        assert(wire.source === 'flag' && wire.origin === origin, 'real flag origin fixture missing');
        assert(await page.locator(sid(key)).isEnabled() && await page.locator(sid(key)).inputValue() === raw, 'editable/live D/G flag not editable at raw value');
        assert((await row.textContent()).includes(origin) && /stored.*precedence|override.*precedence/i.test(await row.textContent()), 'unlocked flag lacks visible origin/precedence copy');
        assert(!/read.only/i.test(await row.locator('.st-src').first().getAttribute('title') || ''), 'D/G flag tooltip still claims read-only');
        if (key === 'presence.grokbot_ttl') {
          const text = await page.locator('[data-fk=grok-current]').textContent();
          assert(text.includes('Current Grok presence: 10 min') && text.includes(key) && text.includes('flag') && text.includes(origin), 'committed G flag duration/controller/origin missing from policy copy');
        }
        assert(await row.getByRole('button', { name: /^Reset/ }).count() === 0, 'flag row offers RESET before stored');
        const action = row.getByRole('button', { name: 'Store override', exact: true });
        assert(await action.getAttribute('data-fk') === 'store-' + key, 'flag override focus key missing');
        await action.click();
        assert(await row.locator('.st-src').first().textContent() === 'edited', 'flag equal override not a draft');
      }
      if (v === 'policy') await page.locator('[data-fk="tok-events.refresh_interval"]').click();
      assert(await page.locator(sid('events.refresh_interval')).isDisabled() && (await page.locator('[data-row="events.refresh_interval"]').textContent()).includes('VECNET_EVENTS_REFRESH_INTERVAL'), 'other flag lost lock/origin');
      assert(/read.only here/i.test(await page.locator('[data-row="events.refresh_interval"] .st-src').first().getAttribute('title')), 'other flag lost read-only copy');
      assert((await read()).etag === base.etag, 'flag actions auto-saved');
      await page.locator('.st-bar [data-fk=save]').click();
      await page.locator('.st-bar').waitFor({ state: 'detached' });
      const stored = await read();
      assert(stored.revision === base.revision + 1 && stored.preferences.filter(r => ['presence.default_ttl', 'presence.grokbot_ttl'].includes(r.key)).every(r => r.source === 'stored'), 'equal flag SETs not stored');
      assert(stored.preferences.find(r => r.key === 'presence.default_ttl').value === 3600 && stored.preferences.find(r => r.key === 'presence.grokbot_ttl').value === 600, 'flag action changed displayed numeric values');
      for (const key of ['presence.default_ttl', 'presence.grokbot_ttl']) {
        if (v === 'policy') await page.locator(`[data-fk="tok-${key}"]`).click();
        await page.locator(`[data-row="${key}"]`).getByRole('button', { name: 'Reset to start-up value', exact: true }).click();
      }
      await page.locator('.st-bar [data-fk=save]').click();
      await page.locator('.st-bar').waitFor({ state: 'detached' });
      const reset = await read();
      assert(reset.revision === stored.revision + 1 && reset.preferences.find(r => r.key === 'presence.default_ttl').origin === 'VECNET_PRESENCE_TTL' && reset.preferences.find(r => r.key === 'presence.grokbot_ttl').origin === '--grok-ttl', 'RESET did not restore real flag origins');
      assert(reset.preferences.find(r => r.key === 'presence.default_ttl').source === 'flag' && reset.preferences.find(r => r.key === 'presence.default_ttl').value === 3600 && reset.preferences.find(r => r.key === 'presence.grokbot_ttl').source === 'flag' && reset.preferences.find(r => r.key === 'presence.grokbot_ttl').value === 600, 'RESET did not restore flag values/provenance');
      await quiet();
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'flag view scrolls sideways');
      await shot(`prefs-${v}-ttl-flags-${size}`);
    }
  });
  console.log('settings passed: loading, initial, real default-off pruning report and four centered gate-off captures, hub availability for every row, unavailable, flag, dirty, invalid, saving, saved, default, confirm, retention-available, retention-edited, retention-confirm, retention-existing-confirm, retention-saved, revision replay rejected, conflict, error, empty and appearance; List and Policy; desk and phone');
} finally { await cleanBuild(); }

