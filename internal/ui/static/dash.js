// Provisional dashboard (operator UI Task 12). Its one write is Add agent
// (invite.js, under Peers), which only creates; dead-letter retry and discard
// and subscription resume live in the inbox.
// Each section loads and fails on its own. The page reloads on the `peers` tag
// and every 30 s while mounted. Ages use the hub clock, never the client's.
import { activityAction } from './activity-model.js';
import { blockedLine, cutSummary } from './dash-model.js';
import { filterPeers, readPeerFilters, savePeerFilters } from './peers-model.js';
import { principalRows } from './principals-model.js';
import { chip, h, idCopy, idText, peerLink } from './dom.js';
import { closeInvite, inviteButton } from './invite.js';
import { ago, kindShort, kindTitle, short, title as threadTitle, when } from './format.js';
import * as subs from './subs.js';
import { activitySnapshot, peerActivity } from './sparkline-model.js';
import { createSparkline } from './sparkline.js';

const RELOAD_MS = 30000;
const AUDIT_PAGE = 50;
const THREAD_PAGE = 10;
const COUNTERS = ['loop_cut', 'rate_limited', 'dead_letters', 'push_conflicts', 'push_auth_failed'];
// The hub's counters are running totals kept in its database, not the current
// state: dead_letters counts deliveries ever dead-lettered, while the Dead
// letters section lists the ones still waiting.
const COUNTER_LABELS = { dead_letters: 'dead-lettered' };

const errText = e => (e && e.message) || String(e);

// Keep each answer bounded and the hub's decimal cursor a string. A bad
// answer follows the resource's usual error/last-good-value path.
function auditPage(value, before = null) {
  const entries = value.entries ?? [], next = value.next ?? null;
  if (!Array.isArray(entries) || entries.length > AUDIT_PAGE
      || entries.some(e => !e || !Number.isInteger(e.id) || e.id < 1)
      || (next !== null && (typeof next !== 'string' || !/^[1-9]\d*$/.test(next)
        || !entries.length || (before !== null && BigInt(next) >= BigInt(before))))) {
    throw new Error('Invalid audit page');
  }
  return { entries: [...new Map(entries.map(e => [e.id, e])).values()].sort((a, b) => b.id - a.id), next };
}

// The cell shows the short form, but the whole id is in its text (a copy
// is whole) and its title, with a copy button on hover (dom.js idCopy).
function idCell(id, what) {
  return h('span', { class: 'sub mono' }, idCopy(id, what));
}

function table(heads, rows) {
  return h('table', null,
    h('thead', null, h('tr', null, heads.map(t => h('th', { text: t })))),
    h('tbody', null, rows));
}

function loading() { return h('div', { class: 'loading', text: 'Loading…' }); }

function failure(label, err) {
  return h('div', { class: 'empty error' }, h('b', { text: label + ' unavailable' }), errText(err));
}

function none(text) { return h('p', { class: 'none', text }); }

export function mount(root, ctx) {
  const { api, store } = ctx;
  const browse = !!ctx.peerBrowse;
  let storage; try { storage = globalThis.localStorage; } catch {}
  const filters = { ...readPeerFilters(storage), q: ctx.query?.get('q') || '', kind: '', presence: '', sort: 'recent', limit: browse ? 50 : 10 };
  const nowMs = () => store.hubNow();
  const age = iso => ago(iso, nowMs());
  const time = iso => h('time', { text: when(iso, nowMs()), title: iso || '' });

  const res = {
    activity: store.resource('dashboard-activity', async () => {
      const snapshot = activitySnapshot(await api.activityStats());
      if (!snapshot) throw new Error('Invalid activity snapshot');
      return snapshot;
    }),
    counters: store.resource('peers', () => api.counters()),
    peers: store.resource('peers', () => api.adminPeers()),
    // Created peers emit directory_updated, so the peers tag refreshes this.
    principals: store.resource('peers', () => api.principals()),
    threads: store.resource('threads', () => api.threads({ archived: false, limit: THREAD_PAGE })),
    stats: store.resource('threads', () => api.threadStats ? api.threadStats() : Promise.resolve(null)),
    health: store.resource('peers', () => api.deliveryHealth()),
    dead: store.resource('peers', () => api.deadLetters()),
    cuts: store.resource('peers', () => api.loopCuts(24)),
    audit: store.resource('peers', async () => {
      const page = await api.audit({ limit: AUDIT_PAGE });
      return audit.alive ? auditPage(page) : undefined;
    }),
    // The subscriptions table is subs.js's, under its own `subs` tag. This
    // `peers` resource reloads it whenever the rest of the page reloads: on
    // the peers tag (operator_view "subscription" names it) and every 30 s.
    subs: store.resource('peers', () => subs.reloadShared(ctx)),
  };

  // Names come from the admin directory, which also lists disabled peers and
  // sources, and fall back to the shell's directory.
  const peerName = id => {
    const row = ((res.peers.get() || {}).peers || []).find(r => r.peer && r.peer.peer_id === id);
    return row ? row.peer.name : ctx.nameOf(id);
  };
  const threadName = id => {
    const row = ((res.threads.get() || {}).threads || []).find(r => r.thread && r.thread.thread_id === id);
    return row ? threadTitle(row.thread, ctx.me, peerName) : short(id);
  };
  const threadLink = id => h('a', { href: '#thread/' + id, text: threadName(id), title: id });
  const peerCell = id => peerLink(id, peerName(id));

  // The resource owns one live head; page owns only the selected history page.
  // trail contains request cursors only, with null denoting the live head.
  const audit = { page: null, before: null, trail: [], busy: null, err: null, generation: 0, alive: true };
  // Archived peers stay out of the operator list until this is on. It is
  // local to the page: a #peer/<id> link still loads that peer from the
  // unfiltered admin directory.


  const sections = {};
  function section(key, id, heading) {
    const count = h('span', { class: 'n' });
    const body = h('div', { class: 'body' });
    const el = h('section', { class: 'dash-sec dash-' + key, id }, h('h2', null, heading + ' ', count), body);
    sections[key] = { el, count, body };
    return el;
  }

  const activityCount = h('strong', { class: 'activity-count', text: '—' });
  const activityRate = h('strong', { class: 'activity-rate', text: '—' });
  const activityActive = h('strong', { class: 'activity-active', text: '—' });
  const activityStatus = h('p', { class: 'activity-status', role: 'status' });
  const overviewChart = createSparkline();
  overviewChart.el.className += ' activity-chart';
  const activityPanel = h('section', { class: 'dash-activity', 'aria-label': 'Hourly activity' },
    h('div', { class: 'activity-overview' },
      h('div', { class: 'activity-volume' }, h('span', { text: 'Messages last hour' }), activityCount),
      overviewChart.el,
      h('div', { class: 'activity-metric' }, h('span', { text: 'Messages / min' }), activityRate, h('span', { text: 'Hourly average' })),
      h('div', { class: 'activity-metric' }, h('span', { text: 'Active peers' }), activityActive, h('span', { text: 'Sent in last hour, not presence' }))),
    activityStatus);
  const peerCharts = new Map();
  const rate = n => n.toLocaleString('en-US', { maximumFractionDigits: 2 });
  function renderActivity() {
    const value = res.activity.get(), err = res.activity.error();
    activityCount.textContent = value ? value.messages.toLocaleString('en-US') : '—';
    activityRate.textContent = value ? rate(value.messages_per_minute) : '—';
    activityActive.textContent = value ? String(value.active_peers) : '—';
    activityStatus.textContent = value
      ? `${err ? 'Stale · ' + errText(err) + ' · ' : res.activity.loading() ? 'Refreshing · ' : ''}Non-reactions, including sources and hub. Active peers includes operator and dots.`
      : err ? 'Activity unavailable: ' + errText(err) + ' · retries every 30 seconds.' : 'Loading activity…';
    activityPanel.setAttribute('data-state', err ? value ? 'stale' : 'error' : value ? 'ready' : 'loading');
    overviewChart.update(value, value, { label: 'Hub messages' });
    let max = 0;
    if (value) for (const p of Object.values(value.peers)) for (const n of p.buckets) max = Math.max(max, n);
    for (const [id, view] of peerCharts) {
      const p = peerActivity(value, id);
      view.summary.textContent = p ? `${p.messages.toLocaleString('en-US')} messages · ${rate(p.messages_per_minute)} / min avg${err ? ' · stale' : ''}` : err ? 'Activity unavailable' : 'Loading activity…';
      view.chart.update(value, p, { label: peerName(id), max });
    }
  }
  function peerActivityCell(id) {
    if (!peerCharts.has(id)) {
      const summary = h('p', { class: 'peer-activity-summary' }), chart = createSparkline();
      peerCharts.set(id, { summary, chart, cell: h('td', { class: 'peer-activity' }, summary, chart.el) });
    }
    return peerCharts.get(id).cell;
  }
  const counterLine = h('p', { class: 'dash-counters mono' });
  // subs.js owns the whole section, heading and count included, so the
  // dashboard shows one "Subscriptions" heading, not two.
  const subsSlot = h('section', { class: 'dash-sec dash-subs subs', id: 'd-subs' });
  root.replaceChildren(
    h('div', { class: 'dash' },
      h('h1', { text: browse ? 'Peers' : 'Dashboard' }),
      h('p', { class: 'lede', text: browse ? 'Find every agent, launcher and source. Presence is self-reported; reachability is observed by the hub.' : 'Hub activity, recent conversations and the agents behind them.' }),
      activityPanel,
      counterLine,
      section('peers', 'd-peers', 'Peers'),
      section('principals', 'd-principals', 'Principals'),
      section('threads', 'd-threads', 'Recent threads'),
      section('health', 'd-health', 'Delivery health'),
      section('dead', 'd-dead', 'Dead letters'),
      section('loop', 'd-loop', 'Loop control'),
      subsSlot,
      section('audit', 'd-audit', 'Audit tail')));
  const stopSubs = subs.renderSection(subsSlot, ctx);
  sections.peers.el.querySelector('h2').append(inviteButton(ctx, () => res.peers.reload()));

  const kpis = h('div', { class: 'dash-kpis', 'aria-label': 'Hub overview' });
  activityPanel.before(kpis);
  let auditOpen = false;
  try { auditOpen = storage?.getItem('vecnet.ui.audit-open') === 'true'; } catch {}
  const auditDetails = h('details', { class: 'dash-audit-fold', open: auditOpen, ontoggle: e => { try { storage?.setItem('vecnet.ui.audit-open', String(e.target.open)); } catch {} } }, h('summary', { text: 'Audit tail' }));
  sections.audit.el.before(auditDetails); auditDetails.append(sections.audit.el);
  const clearLine = h('p', { class: 'dash-clear', text: 'All clear · No blocked deliveries, dead letters or loop cuts.' });
  sections.health.el.before(clearLine);
  if (browse) {
    for (const el of [activityPanel, kpis, counterLine, clearLine, auditDetails, subsSlot, ...Object.values(sections).filter(s => s !== sections.peers).map(s => s.el)]) el.hidden = true;
  }
  function renderOverview() {
    if (browse) return;
    const peers = filterPeers(res.peers.get()?.peers).total;
    const activity = res.activity.get(), stats = res.stats.get(), health = res.health.get(), dead = res.dead.get(), cuts = res.cuts.get();
    const attention = (health?.blocked?.length || 0) + (health?.failed?.length || 0) + (dead?.dead_letters?.length || 0);
    const presence = {};
    for (const p of res.peers.get()?.peers || []) if (!p.disabled && !p.archived_at) presence[p.peer.presence || 'unknown'] = (presence[p.peer.presence || 'unknown'] || 0) + 1;
    const tile = (href, label, value, detail) => h('a', { class: 'dash-kpi', href }, h('span', { text: label }), h('strong', { text: value }), h('span', { text: detail }));
    kpis.replaceChildren(
      tile('#peers', 'Peers', `${activity?.active_peers ?? '—'} / ${peers || '—'}`, 'active last hour / total · ' + Object.entries(presence).map(([k, v]) => `${v} ${k}`).join(', ')),
      tile('#threads', 'Threads', stats ? String(stats.active_24h) : '—', stats ? `active in 24 h · ${stats.open} open · ${stats.archived} archived` : 'Loading thread totals…'),
      tile('#inbox', 'Needs attention', String(attention), `${cuts?.total ?? '—'} loop cuts in 24 h`));
    const clear = health && dead && cuts && !attention && !cuts.total && !res.health.error() && !res.dead.error() && !res.cuts.error();
    clearLine.hidden = !clear;
    for (const key of ['health', 'dead', 'loop']) sections[key].el.hidden = !!clear;
  }

  // fill renders one section from its resource: its own error, then whatever
  // it last loaded, or a loading line before the first answer.
  function fill(key, r, label, draw) {
    const s = sections[key], value = r.get(), err = r.error();
    const kids = [];
    if (err) kids.push(failure(label, err));
    if (value) kids.push(...draw(value, s));
    else if (!err) { kids.push(loading()); s.count.textContent = ''; }
    for (const old of [...s.body.childNodes]) if (!kids.includes(old)) old.remove();
    kids.forEach((node, i) => { if (s.body.childNodes[i] !== node) s.body.insertBefore(node, s.body.childNodes[i] || null); });
  }

  function renderCounters() {
    const value = res.counters.get(), err = res.counters.error();
    if (!value) {
      counterLine.className = 'dash-counters mono' + (err ? ' bad' : '');
      counterLine.textContent = err ? 'Counters unavailable: ' + errText(err) : 'Loading counters…';
      return;
    }
    const keys = [...COUNTERS.filter(k => Object.hasOwn(value, k)), ...Object.keys(value).filter(k => !COUNTERS.includes(k)).sort()];
    counterLine.className = 'dash-counters mono' + (err ? ' bad' : '');
    counterLine.textContent = 'All-time totals: ' + keys.map(k => (COUNTER_LABELS[k] || k.replace(/_/g, ' ')) + ' ' + value[k]).join(' · ') + (err ? ' · stale: ' + errText(err) : '');
  }

  const peerTable = table(['Peer', 'Kind', 'Presence', 'Reachability', 'Credential', 'Routes', 'Quota', 'Location', 'Activity · last hour'], []);
  const peerTableWrap = h('div', { class: 'peer-table-scroll', role: 'region', 'aria-label': 'Peers; activity charts share a common scale' }, peerTable);
  const peerRows = new Map();
  const archivedLabel = h('span'), disabledLabel = h('span');
  const changeFilters = () => { savePeerFilters(storage, filters); render(); };
  const archivedInput = h('input', { type: 'checkbox', 'aria-label': 'Show archived', onchange: e => { filters.showArchived = !!e.target.checked; changeFilters(); } });
  const disabledInput = h('input', { type: 'checkbox', 'aria-label': 'Show disabled', onchange: e => { filters.showDisabled = !!e.target.checked; changeFilters(); } });
  const searchInput = h('input', { type: 'search', placeholder: 'Find peers', 'aria-label': 'Find peers', value: filters.q, oninput: e => { filters.q = e.target.value; render(); } });
  const peerToolbar = h('div', { class: 'browse-toolbar' }, searchInput,
    h('label', { class: 'show-disabled' }, disabledInput, disabledLabel),
    h('label', { class: 'show-archived' }, archivedInput, archivedLabel));
  function selectFilter(label, key, values) {
    return h('select', { 'aria-label': label, onchange: e => { filters[key] = e.target.value; render(); } }, values.map(([value, text]) => h('option', { value, text })));
  }
  if (browse) peerToolbar.append(
    selectFilter('Kind', 'kind', [['', 'All kinds'], ...['session', 'launcher', 'operator', 'dot', 'source', 'grokbot', 'openclaw', 'pagchat-bot'].map(v => [v, v])]),
    selectFilter('Presence', 'presence', [['', 'All presence'], ...['idle', 'working', 'blocked', 'offline', 'unknown', 'n/a'].map(v => [v, v])]),
    selectFilter('Sort peers', 'sort', [['recent', 'Recent activity'], ['name', 'Name']]));
  const peerMore = h('button', { class: 'btn', text: 'Show more', onclick: () => { filters.limit += 50; render(); } });
  const peerFooter = h('a', { class: 'browse-footer', href: '#peers' });
  const peerEmpty = none('');
  function drawPeers(value, s) {
    const list = filterPeers(value.peers, filters);
    const rows = list.shown;
    s.count.textContent = String(rows.length) + (browse ? ' / ' + list.matched : '');
    archivedLabel.textContent = 'Show archived (' + list.archived + ')';
    disabledLabel.textContent = 'Show disabled (' + list.disabled + ')';
    for (const [input, on] of [[archivedInput, filters.showArchived], [disabledInput, filters.showDisabled]]) {
      if (on) input.setAttribute('checked', ''); else input.removeAttribute('checked');
    }
    peerEmpty.textContent = list.archived || list.disabled ? 'Disabled or archived peers are hidden. Adjust the filters to show them.' : 'No peers match.';
    peerFooter.textContent = 'All ' + list.total + ' peers →';
    peerMore.hidden = list.shown.length >= list.matched;
    // Hidden/removed peers release both owners; surviving views keep their identity.
    const shownIDs = new Set(rows.map(p => p.id));
    for (const id of peerRows.keys()) {
      if (!shownIDs.has(id)) {
        peerRows.delete(id);
        peerCharts.delete(id);
      }
    }
    const nextRows = rows.map(p => {
      const presenceAge = p.presence === 'n/a' ? '' : p.presenceAt ? age(p.presenceAt) : 'never reported';
      const reachSub = [p.via, p.seen ? age(p.seen) : ''].filter(Boolean).join(', ');
      const mark = p.archivedAt ? chip('archived', 'bad') : (p.disabled ? chip('disabled', 'bad') : null);
      const row = peerRows.get(p.id) || h('tr');
      row.className = p.disabled || p.archivedAt ? 'off' : '';
      const cells = h('tr', null,
        h('td', null, peerLink(p.id, p.name, { cls: 'pn' }), mark ? [' ', mark] : null, idCell(p.id, 'peer id'), p.kind === 'launcher' && !p.disabled && !p.archivedAt ? h('a', { class: 'sub', href: '#peer/' + p.id, text: 'Launch job →' }) : null),
        h('td', null, h('span', { class: 'pill', title: kindTitle(p.kind) }, h('i', { class: 'kd k-' + p.kind }), kindShort(p.kind))),
        h('td', null, h('span', { class: 'pill' }, h('i', { class: 'pd ' + p.presence.replace('/', '') }), p.presence),
          h('span', { class: 'sub mono', text: presenceAge })),
        h('td', null, h('span', { class: 'pill' }, h('i', { class: 'rch ' + p.reach }), p.reach),
          h('span', { class: 'sub mono', text: reachSub })),
        h('td', { class: 'cred ' + p.credential, text: p.credential }),
        h('td', { class: 'mono sm', text: p.hosts.length ? p.hosts.join(', ') : 'none' }),
        h('td', { class: 'mono sm', text: p.quota }),
        h('td', { class: 'mono sm', text: p.location }));
      const activityCell = peerActivityCell(p.id);
      if (activityCell.parentNode !== row) row.append(activityCell);
      for (const old of [...row.children]) if (old !== activityCell) old.remove();
      for (const cell of [...cells.children]) row.insertBefore(cell, activityCell);
      peerRows.set(p.id, row);
      return row;
    });
    const body = peerTable.querySelector('tbody');
    for (const old of [...body.children]) if (!nextRows.includes(old)) old.remove();
    nextRows.forEach((row, i) => { if (body.children[i] !== row) body.insertBefore(row, body.children[i] || null); });
    return [peerToolbar, rows.length ? peerTableWrap : peerEmpty, browse ? peerMore : peerFooter];
  }

  // Principals with limits or created peers (§18.4); absent when there are none.
  function drawPrincipals(value, s) {
    const rows = principalRows(value.principals);
    s.count.textContent = String(rows.length);
    if (!rows.length) return [];
    // Each warning says why in words, not by colour alone; the title adds detail.
    return [table(['Principal', 'Active/max', 'Disabled', 'Creates/h', 'Kinds', 'Default scopes'], rows.map(p => h('tr', null,
      h('td', null, h('span', { text: p.name }), idCell(p.id)),
      h('td', { class: 'mono sm' + (p.full ? ' warn' : ''), text: p.active + '/' + p.max + (p.full ? ' · full' : ''), title: p.full ? 'at the peer cap: a create or re-enable is refused' : null }),
      h('td', { class: 'mono sm', text: String(p.disabled) }),
      h('td', { class: 'mono sm', text: p.createsLastHour + '/' + p.createsPerHour }),
      h('td', { class: 'kinds' }, p.kinds.length
        ? p.kinds.map(k => h('span', { class: 'pill', title: kindTitle(k) }, h('i', { class: 'kd k-' + k }), kindShort(k)))
        : h('span', { class: 'mono sm none', text: 'none' })),
      h('td', { class: 'mono sm' + (p.unscopedRisk ? ' warn' : ''), text: p.scopes + (p.unscopedRisk ? ' · others scoped' : ''), title: p.unscopedRisk ? 'created peers get no scopes, while other peers of this principal are scoped' : null }))))];
  }

  function drawThreads(value, s) {
    const rows = value.threads || [];
    s.count.textContent = String(rows.length);
    const footer = h('a', { class: 'browse-footer', href: '#threads', text: 'Browse all threads →' });
    if (!rows.length) return [none('No open threads yet.'), footer];
    return [table(['Thread', 'Participants', 'Messages', 'Budget left', 'Last activity', 'Cut'], rows.map(r => {
      const t = r.thread || {}, parts = t.participants || [];
      return h('tr', null,
        h('td', null, h('a', { href: '#thread/' + t.thread_id, class: 'tl' + (t.title ? '' : ' un'), text: threadTitle(t, ctx.me, peerName) }), idCell(t.thread_id, 'thread id')),
        h('td', null, h('span', { text: String(parts.length) }), h('span', { class: 'sub' }, parts.flatMap((id, i) => [i ? ', ' : '', peerCell(id)]))),
        h('td', { text: String(t.message_count ?? 0) }),
        h('td', { text: t.budget_remaining === null || t.budget_remaining === undefined ? 'unlimited' : String(t.budget_remaining), class: t.budget_remaining === 0 ? 'bad' : null }),
        h('td', { text: age(r.last_message_at), title: r.last_message_at || '' }),
        h('td', null, r.cut ? h('span', { class: 'bad', text: r.cut.reason, title: r.cut.at }) : ''));
    })), footer];
  }

  function drawHealth(value, s) {
    const blocked = value.blocked || [], failed = value.failed || [];
    s.count.textContent = String(blocked.length + failed.length);
    if (!blocked.length && !failed.length) return [none('No blocked or failed deliveries.')];
    const names = { peer: peerName, thread: threadName };
    return [
      h('h3', null, 'Blocked on delivery ', h('span', { class: 'n', text: String(blocked.length) })),
      h('p', { class: 'note', text: 'A peer’s queue for a thread waits behind a delivery under retry, while its other threads flow (PROTOCOL §16.4).' }),
      blocked.length
        ? h('ul', { class: 'blocked' }, blocked.map(b => h('li', null,
          h('span', { text: blockedLine(b, names) }),
          b.next_attempt_at ? h('span', { class: 'sub mono', text: 'next attempt ' + when(b.next_attempt_at, nowMs()), title: b.next_attempt_at }) : null,
          h('a', { class: 'sub', href: '#thread/' + b.thread_id, text: 'open thread' }))))
        : none('Nothing blocked.'),
      h('h3', null, 'Failed ', h('span', { class: 'n', text: String(failed.length) })),
      h('p', { class: 'note', text: 'Failed deliveries are final: the protocol does not retry them.' }),
      failed.length
        ? table(['Delivery', 'To', 'Thread', 'Status', 'Error', 'When'], failed.map(f => h('tr', null,
          h('td', null, idCell(f.delivery_id, 'delivery id')),
          h('td', null, peerCell(f.recipient)),
          h('td', null, threadLink(f.thread_id)),
          h('td', { class: 'bad mono', text: f.last_status === null || f.last_status === undefined ? '—' : String(f.last_status) }),
          h('td', { class: 'sm', text: f.last_error || '' }),
          h('td', null, time(f.updated_at)))))
        : none('Nothing failed.'),
    ];
  }

  function drawDead(value, s) {
    const rows = value.dead_letters || [];
    s.count.textContent = String(rows.length);
    const note = h('p', { class: 'note' }, 'Read-only here. ', h('a', { href: '#inbox', text: 'Act on these in Inbox' }), '.');
    if (!rows.length) return [note, none('No dead letters.')];
    return [note, table(['Delivery', 'To', 'Thread', 'Attempts', 'Status', 'Error', 'When'], rows.map(d => h('tr', null,
      h('td', null, idCell(d.delivery_id, 'delivery id')),
      h('td', null, peerCell(d.recipient)),
      h('td', null, threadLink(d.thread_id)),
      h('td', { text: String(d.attempt ?? 0) }),
      h('td', { class: 'bad mono', text: d.last_status === null || d.last_status === undefined ? '—' : String(d.last_status) }),
      h('td', { class: 'sm', text: d.last_error || '' }),
      h('td', null, time(d.updated_at)))))];
  }

  function drawLoop(value, s) {
    const c = cutSummary(value);
    s.count.textContent = 'last ' + c.hours + ' h';
    const total = h('p', { class: 'total' }, h('b', { text: String(c.total) }), ' cut' + (c.total === 1 ? '' : 's') + ' in the last ' + c.hours + ' h');
    if (!c.reasons.length) return [total];
    return [total, table(['Reason', 'Cuts'], c.reasons.map(r => h('tr', null, h('td', { class: 'mono sm', text: r.reason }), h('td', { text: String(r.count) }))))];
  }

  function drawAudit(value, s) {
    const { entries, next } = audit.page || value;
    s.count.textContent = entries.length + (audit.page ? ' historical' : ' newest') + ', no bodies or secrets';
    const out = [];
    out.push(entries.length
      ? h('div', { class: 'audit mono' }, entries.map(e => h('div', { class: 'ln' },
        h('time', { class: 't', text: when(e.at, nowMs()), title: e.at }),
        h('span', { class: 'actor' }, peerLink(e.actor, peerName(e.actor))),
        h('span', { class: 'act', text: e.action }),
        h('span', { class: 't' }, (e.target_ids || []).map(id => idText(id, 'tg'))))))
      : none('No audit entries.'));
    if (audit.err) out.push(h('p', { class: 'err', text: 'Could not load audit entries: ' + errText(audit.err) }));
    if (audit.busy) out.push(h('p', { role: 'status', text: 'Loading ' + audit.busy + ' entries…' }));
    out.push(
      h('button', { type: 'button', class: 'btn older', disabled: !next || !!audit.busy, text: 'Older', onclick: loadOlder }),
      h('button', { type: 'button', class: 'btn', disabled: !audit.trail.length && !audit.busy, text: 'Newer', onclick: loadNewer }),
      h('button', { type: 'button', class: 'btn', disabled: !audit.page && !audit.busy, text: 'Latest', onclick: latestAudit }));
    return out;
  }

  function latestAudit() {
    if (!audit.alive) return;
    ++audit.generation;
    audit.page = null;
    audit.before = null;
    audit.trail.length = 0;
    audit.busy = null;
    audit.err = null;
    render();
  }

  function loadOlder() {
    if (!audit.alive || audit.busy) return;
    const next = (audit.page || res.audit.get())?.next;
    if (next) return loadAuditPage(next, 'older');
  }

  function loadNewer() {
    if (!audit.alive) return;
    const before = audit.trail.at(-1);
    // Also cancels a pending Older from the live head immediately.
    if (before == null) { latestAudit(); return; }
    return loadAuditPage(before, 'newer');
  }

  async function loadAuditPage(before, direction) {
    const generation = ++audit.generation;
    audit.busy = direction;
    audit.err = null;
    render();
    try {
      const value = await api.audit({ limit: AUDIT_PAGE, before });
      if (!audit.alive || generation !== audit.generation) return;
      const page = auditPage(value, before);
      if (direction === 'older') audit.trail.push(audit.before);
      else audit.trail.pop();
      audit.page = page;
      audit.before = before;
    } catch (e) {
      if (audit.alive && generation === audit.generation) audit.err = e;
    } finally {
      if (audit.alive && generation === audit.generation) {
        audit.busy = null;
        render();
      }
    }
  }

  function render() {
    if (!audit.alive) return;
    renderCounters();
    renderOverview();
    fill('peers', res.peers, 'Peers', drawPeers);
    renderActivity();
    const pv = res.principals.get();
    sections.principals.el.hidden = browse || !res.principals.error() && !principalRows(pv && pv.principals).length;
    if (!sections.principals.el.hidden) fill('principals', res.principals, 'Principals', drawPrincipals);
    fill('threads', res.threads, 'Threads', drawThreads);
    fill('health', res.health, 'Delivery health', drawHealth);
    fill('dead', res.dead, 'Dead letters', drawDead);
    fill('loop', res.cuts, 'Loop control', drawLoop);
    fill('audit', res.audit, 'Audit tail', drawAudit);
  }

  render();
  const offs = Object.values(res).map(r => r.subscribe(render));
  const timer = setInterval(() => { for (const r of Object.values(res)) r.reload(); }, RELOAD_MS);
  // The thread list follows the activity feed (PROTOCOL §2.13) between reloads.
  if (ctx.activity) {
    offs.push(ctx.activity.subscribe(ev => {
      const act = activityAction(res.threads.get(), ev);
      if (act && act.reload) res.threads.reload();
      else if (act && act.page) res.threads.set(act.page);
    }));
  }
  return () => {
    audit.alive = false;
    ++audit.generation;
    closeInvite();
    stopSubs();
    clearInterval(timer);
    for (const off of offs) off();
    audit.page = null;
    audit.before = null;
    audit.trail.length = 0;
    audit.busy = null;
    audit.err = null;
    res.audit.set(undefined);
    peerRows.clear();
    peerCharts.clear();
  };
}
