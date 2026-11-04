// Provisional read-only dashboard (operator UI Task 12). It performs no writes:
// dead-letter retry and discard and subscription resume live in the inbox.
// Each section loads and fails on its own. The page reloads on the `peers` tag
// and every 30 s while mounted. Ages use the hub clock, never the client's.
import { activityAction } from './activity-model.js';
import { allThreads, blockedLine, cutSummary, peerRows } from './dash-model.js';
import { principalRows } from './principals-model.js';
import { chip, h, idCopy, idText, peerLink } from './dom.js';
import { ago, short, title as threadTitle, when } from './format.js';
import * as subs from './subs.js';

const RELOAD_MS = 30000;
const AUDIT_PAGE = 50;
const THREAD_PAGE = 200;
const COUNTERS = ['loop_cut', 'rate_limited', 'dead_letters', 'push_conflicts', 'push_auth_failed'];
// The hub's counters are running totals kept in its database, not the current
// state: dead_letters counts deliveries ever dead-lettered, while the Dead
// letters section lists the ones still waiting.
const COUNTER_LABELS = { dead_letters: 'dead-lettered' };

const errText = e => (e && e.message) || String(e);

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
  const nowMs = () => store.hubNow();
  const age = iso => ago(iso, nowMs());
  const time = iso => h('time', { text: when(iso, nowMs()), title: iso || '' });

  const res = {
    counters: store.resource('peers', () => api.counters()),
    peers: store.resource('peers', () => api.adminPeers()),
    // Created peers emit directory_updated, so the peers tag refreshes this.
    principals: store.resource('peers', () => api.principals()),
    threads: store.resource('threads', () => allThreads(api, THREAD_PAGE)),
    health: store.resource('peers', () => api.deliveryHealth()),
    dead: store.resource('peers', () => api.deadLetters()),
    cuts: store.resource('peers', () => api.loopCuts(24)),
    audit: store.resource('peers', () => api.audit({ limit: AUDIT_PAGE })),
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

  // The audit tail keeps every entry it has shown, so "Load older" pages on
  // from the oldest one and a reload of the head does not drop them.
  const audit = { seen: new Map(), olderNext: undefined, busy: false, err: null, alive: true };

  const sections = {};
  function section(key, id, heading) {
    const count = h('span', { class: 'n' });
    const body = h('div', { class: 'body' });
    const el = h('section', { class: 'dash-sec dash-' + key, id }, h('h2', null, heading + ' ', count), body);
    sections[key] = { el, count, body };
    return el;
  }

  const counterLine = h('p', { class: 'dash-counters mono' });
  // subs.js owns the whole section, heading and count included, so the
  // dashboard shows one "Subscriptions" heading, not two.
  const subsSlot = h('section', { class: 'dash-sec dash-subs subs', id: 'd-subs' });
  root.replaceChildren(
    h('div', { class: 'dash' },
      h('h1', { text: 'Dashboard' }),
      h('p', { class: 'lede', text: 'Read-only view of the hub. Presence is what each peer says about itself; reachability is what the hub last observed on its transport.' }),
      counterLine,
      section('peers', 'd-peers', 'Peers'),
      section('principals', 'd-principals', 'Principals'),
      section('threads', 'd-threads', 'Threads'),
      section('health', 'd-health', 'Delivery health'),
      section('dead', 'd-dead', 'Dead letters'),
      section('loop', 'd-loop', 'Loop control'),
      subsSlot,
      section('audit', 'd-audit', 'Audit tail')));
  subs.renderSection(subsSlot, ctx);

  // fill renders one section from its resource: its own error, then whatever
  // it last loaded, or a loading line before the first answer.
  function fill(key, r, label, draw) {
    const s = sections[key], value = r.get(), err = r.error();
    const kids = [];
    if (err) kids.push(failure(label, err));
    if (value) kids.push(...draw(value, s));
    else if (!err) { kids.push(loading()); s.count.textContent = ''; }
    s.body.replaceChildren(...kids);
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

  function drawPeers(value, s) {
    const rows = peerRows(value.peers);
    s.count.textContent = String(rows.length);
    if (!rows.length) return [none('No peers enrolled.')];
    return [table(['Peer', 'Kind', 'Presence', 'Reachability', 'Credential', 'Routes', 'Quota', 'Location'], rows.map(p => {
      const presenceAge = p.presence === 'n/a' ? '' : p.presenceAt ? age(p.presenceAt) : 'never reported';
      const reachSub = [p.via, p.seen ? age(p.seen) : ''].filter(Boolean).join(', ');
      return h('tr', { class: p.disabled ? 'off' : null },
        h('td', null, peerLink(p.id, p.name, { cls: 'pn' }), p.disabled ? [' ', chip('disabled', 'bad')] : null, idCell(p.id, 'peer id')),
        h('td', null, h('span', { class: 'pill' }, h('i', { class: 'kd k-' + p.kind }), p.kind)),
        h('td', null, h('span', { class: 'pill' }, h('i', { class: 'pd ' + p.presence.replace('/', '') }), p.presence),
          h('span', { class: 'sub mono', text: presenceAge })),
        h('td', null, h('span', { class: 'pill' }, h('i', { class: 'rch ' + p.reach }), p.reach),
          h('span', { class: 'sub mono', text: reachSub })),
        h('td', { class: 'cred ' + p.credential, text: p.credential }),
        h('td', { class: 'mono sm', text: p.hosts.length ? p.hosts.join(', ') : 'none' }),
        h('td', { class: 'mono sm', text: p.quota }),
        h('td', { class: 'mono sm', text: p.location }));
    }))];
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
        ? p.kinds.map(k => h('span', { class: 'pill' }, h('i', { class: 'kd k-' + k }), k))
        : h('span', { class: 'mono sm none', text: 'none' })),
      h('td', { class: 'mono sm' + (p.unscopedRisk ? ' warn' : ''), text: p.scopes + (p.unscopedRisk ? ' · others scoped' : ''), title: p.unscopedRisk ? 'created peers get no scopes, while other peers of this principal are scoped' : null }))))];
  }

  function drawThreads(value, s) {
    const rows = value.threads || [];
    s.count.textContent = String(rows.length);
    if (!rows.length) return [none('No threads yet.')];
    return [table(['Thread', 'Participants', 'Messages', 'Budget left', 'Last activity', 'Cut'], rows.map(r => {
      const t = r.thread || {}, parts = t.participants || [];
      return h('tr', null,
        h('td', null, h('a', { href: '#thread/' + t.thread_id, class: 'tl' + (t.title ? '' : ' un'), text: threadTitle(t, ctx.me, peerName) }), idCell(t.thread_id, 'thread id')),
        h('td', null, h('span', { text: String(parts.length) }), h('span', { class: 'sub' }, parts.flatMap((id, i) => [i ? ', ' : '', peerCell(id)]))),
        h('td', { text: String(t.message_count ?? 0) }),
        h('td', { text: t.budget_remaining === null || t.budget_remaining === undefined ? 'unlimited' : String(t.budget_remaining), class: t.budget_remaining === 0 ? 'bad' : null }),
        h('td', { text: age(r.last_message_at), title: r.last_message_at || '' }),
        h('td', null, r.cut ? h('span', { class: 'bad', text: r.cut.reason, title: r.cut.at }) : ''));
    }))];
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
    for (const e of value.entries || []) audit.seen.set(e.id, e);
    const entries = [...audit.seen.values()].sort((a, b) => b.id - a.id);
    const next = audit.olderNext === undefined ? value.next : audit.olderNext;
    s.count.textContent = entries.length + ' newest, no bodies or secrets';
    const out = [];
    out.push(entries.length
      ? h('div', { class: 'audit mono' }, entries.map(e => h('div', { class: 'ln' },
        h('time', { class: 't', text: when(e.at, nowMs()), title: e.at }),
        h('span', { class: 'actor' }, peerLink(e.actor, peerName(e.actor))),
        h('span', { class: 'act', text: e.action }),
        h('span', { class: 't' }, (e.target_ids || []).map(id => idText(id, 'tg'))))))
      : none('No audit entries.'));
    if (audit.err) out.push(h('p', { class: 'err', text: 'Could not load older entries: ' + errText(audit.err) }));
    if (next) out.push(h('button', { type: 'button', class: 'btn older', disabled: audit.busy, text: audit.busy ? 'Loading…' : 'Load older', onclick: loadOlder }));
    return out;
  }

  async function loadOlder() {
    if (audit.busy) return;
    const ids = [...audit.seen.keys()];
    if (!ids.length) return;
    audit.busy = true;
    audit.err = null;
    render();
    try {
      const page = await api.audit({ limit: AUDIT_PAGE, before: Math.min(...ids) });
      for (const e of page.entries || []) audit.seen.set(e.id, e);
      audit.olderNext = page.next || null;
    } catch (e) {
      audit.err = e;
    } finally {
      audit.busy = false;
      if (audit.alive) render();
    }
  }

  function render() {
    renderCounters();
    fill('peers', res.peers, 'Peers', drawPeers);
    const pv = res.principals.get();
    sections.principals.el.hidden = !res.principals.error() && !principalRows(pv && pv.principals).length;
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
    clearInterval(timer);
    for (const off of offs) off();
  };
}
