// Peer detail (#peer/<id>): everything the operator can see about one peer,
// read-only like the dashboard. Each section has its own read and fails on its
// own; nothing here is fetched per row (one admin view per section, threads by
// participant). Peer-supplied strings become text through h(). Ages use the
// hub clock.
import { chip, copyButton, h, idCopy, idText, peerLink } from './dom.js';
import { ago, fullWhen, plural, title as threadTitle, when } from './format.js';
import { findPeer, peerFacts, peerHealth, peerRoutes, peerSubs, peerThreads, principalFacts, scopeTarget } from './peer-model.js';
import { chip as subChip, expiresIn } from './subs-model.js';

const RELOAD_MS = 30000;
const THREAD_PAGE = 25;
const errText = e => (e && e.message) || String(e);

function loading() { return h('div', { class: 'loading', text: 'Loading…' }); }
function failure(label, err) {
  return h('div', { class: 'empty error' }, h('b', { text: label + ' unavailable' }), errText(err));
}
function none(text) { return h('p', { class: 'none', text }); }
function table(heads, rows) {
  return h('table', null,
    h('thead', null, h('tr', null, heads.map(t => h('th', { text: t })))),
    h('tbody', null, rows));
}
// kv is a dense two-column fact list; a value may be a node or a string.
function kv(rows) {
  return h('dl', { class: 'pv-kv' }, rows.filter(Boolean).flatMap(([k, v]) => [h('dt', { text: k }), h('dd', null, v)]));
}
const dash = v => (v === null || v === undefined || v === '' ? '—' : String(v));

export function mount(root, ctx) {
  const { api, store } = ctx;
  const id = ctx.peerId;
  const nowMs = () => store.hubNow();
  const age = iso => ago(iso, nowMs());
  const time = iso => (iso ? h('time', { text: when(iso, nowMs()) + ' · ' + age(iso), title: fullWhen(iso) + '\n' + iso }) : '—');

  // A thread list by participant is for peer_ ids only (§2.13); a directory
  // source is never a participant.
  const isPeer = /^peer_/.test(id);
  const res = {
    peers: store.resource('peers', () => api.adminPeers()),
    principals: store.resource('peers', () => api.principals()),
    routes: store.resource('peers', () => api.routes()),
    health: store.resource('peers', () => api.deliveryHealth()),
    subs: store.resource('subs', () => api.subscriptions()),
    threads: isPeer ? store.resource('threads', () => api.threads({ participant: id, limit: THREAD_PAGE })) : null,
  };

  // Names come from the admin directory, which also lists disabled peers and
  // sources, then the shell's directory.
  const nameOf = pid => {
    const row = findPeer(res.peers.get(), pid);
    return row && typeof row.peer.name === 'string' ? row.peer.name : ctx.nameOf(pid);
  };
  const link = pid => peerLink(pid, nameOf(pid));
  // A thread by its title when this peer's thread page has it, else its id.
  const threadLink = tid => {
    const r = ((res.threads && res.threads.get()) || {}).threads || [];
    const t = r.find(x => x && x.thread && x.thread.thread_id === tid);
    return t ? h('a', { href: '#thread/' + tid, class: 'tl', title: tid, text: threadTitle(t.thread, ctx.me, nameOf) })
      : h('a', { href: '#thread/' + tid, class: 'tl' }, idText(tid));
  };
  const row = () => findPeer(res.peers.get(), id);
  const facts = () => { const r = row(); return r ? peerFacts(r) : null; };

  const sections = {};
  function section(key, heading) {
    const count = h('span', { class: 'n' });
    const body = h('div', { class: 'body' });
    const el = h('section', { class: 'dash-sec pv-' + key, id: 'pv-' + key }, h('h2', null, heading + ' ', count), body);
    sections[key] = { el, count, body };
    return el;
  }

  const head = h('header', { class: 'pv-head' });
  root.replaceChildren(
    h('div', { class: 'dash pv' },
      h('p', { class: 'crumb' }, h('a', { href: '#dash', text: 'Dashboard' }), ' / peer'),
      head,
      h('div', { class: 'pv-grid' }, section('identity', 'Identity'), section('presence', 'Presence'), section('access', 'Scopes and limits')),
      section('principal', 'Principal'),
      section('routes', 'Routes'),
      section('subs', 'Subscriptions'),
      section('health', 'Delivery health'),
      section('threads', 'Recent threads')));

  // fill draws one section: its own error, then whatever it last loaded, or
  // a loading line before the first answer. deps are every resource it reads.
  function fill(key, deps, label, draw) {
    const s = sections[key];
    const err = deps.map(r => r.error()).find(Boolean);
    const ready = deps.every(r => r.get());
    const kids = [];
    if (err) kids.push(failure(label, err));
    if (ready) kids.push(...draw(s));
    else if (!err) { kids.push(loading()); s.count.textContent = ''; }
    s.body.replaceChildren(...kids);
  }

  function renderHead() {
    const f = facts(), err = res.peers.error();
    if (!f) {
      const missing = res.peers.get() && !err;
      head.replaceChildren(...[
        h('h1', { text: missing ? 'Unknown peer' : nameOf(id) }),
        h('div', { class: 'pv-id' }, h('span', { class: 'mono pid', text: id }), copyButton(id, 'peer id')),
        missing ? h('div', { class: 'empty' }, h('b', { text: 'No peer with this id' }), 'The hub lists no peer or source with this id. It may have been removed, or the link is wrong.') : null,
      ].filter(Boolean));
      return;
    }
    head.className = 'pv-head' + (f.disabled ? ' off' : '');
    head.replaceChildren(...[
      h('h1', null, h('span', { class: 'nm', text: f.name || '(no name)' }),
        h('span', { class: 'pill' }, h('i', { class: 'kd k-' + f.kind }), f.kind || 'unknown'),
        f.disabled ? chip('disabled', 'bad') : null),
      h('div', { class: 'pv-id' }, h('span', { class: 'mono pid', text: f.id }), copyButton(f.id, 'peer id')),
      f.disabled ? h('p', { class: 'note bad', text: 'Disabled: this peer cannot use the hub until an operator enables it.' }) : null,
    ].filter(Boolean));
  }

  function principalCell(pid) {
    if (!pid) return '—';
    const p = principalFacts(res.principals.get(), pid);
    // The hash is the router's, so the link scrolls to the Principal section
    // itself; its href stays this peer's route.
    const jump = e => { e.preventDefault(); sections.principal.el.scrollIntoView({ block: 'start' }); };
    return h('span', null, p ? h('a', { href: '#peer/' + id, text: p.name || pid, onclick: jump }) : null, p ? ' ' : null, idCopy(pid, 'principal id', 'sub'));
  }
  function createdByCell(by) {
    if (!by) return h('span', { class: 'muted', text: 'the operator' });
    if (/^prn_/.test(by)) return principalCell(by);
    return h('span', null, link(by), ' ', idCopy(by, 'id', 'sub'));
  }

  function drawIdentity() {
    const f = facts();
    if (!f) return [none('No peer row.')];
    return [kv([
      ['Peer id', idCopy(f.id, 'peer id')],
      ['Kind', h('span', { class: 'pill' }, h('i', { class: 'kd k-' + f.kind }), f.kind || 'unknown')],
      ['Principal', principalCell(f.principalId)],
      ['Created by', createdByCell(f.createdBy)],
      ['Enrolled', time(f.enrolled)],
      ['Credential', h('span', { class: 'cred ' + f.credential, text: f.credential })],
      ['Status', f.disabled ? h('span', { class: 'bad', text: 'disabled' }) : 'enabled'],
      ['Teams', f.teams.length ? h('span', { class: 'ids' }, f.teams.map(t => idCopy(t, 'team id'))) : '—'],
    ])];
  }

  function drawPresence() {
    const f = facts();
    if (!f) return [none('No peer row.')];
    const reachSub = [f.via ? 'via ' + f.via : '', f.seen ? 'seen ' + age(f.seen) : ''].filter(Boolean).join(', ');
    return [kv([
      ['Presence', h('span', null, h('span', { class: 'pill' }, h('i', { class: 'pd ' + f.presence.replace('/', '') }), f.presence),
        f.presence === 'n/a' ? null : h('span', { class: 'sub mono', text: f.presenceAt ? 'since ' + age(f.presenceAt) : 'never reported', title: f.presenceAt || '' }))],
      ['Last heartbeat', time(f.presenceAt)],
      ['Reachability', h('span', null, h('span', { class: 'pill' }, h('i', { class: 'rch ' + f.reach }), f.reach), reachSub ? h('span', { class: 'sub mono', text: reachSub }) : null)],
      ['Last seen', time(f.seen)],
      ['Capabilities', f.capabilities.length ? h('span', { class: 'chips' }, f.capabilities.map(c => chip(c))) : '—'],
      ['Location', dash(f.location)],
      ['Quota', h('span', { class: 'mono sm', text: f.quota })],
    ])];
  }

  function scopeList(ids) {
    return h('span', { class: 'ids' }, ids.map(t => scopeTarget(t) === 'peer'
      ? h('span', { class: 'sc' }, link(t), ' ', idCopy(t, 'peer id', 'sub'))
      : h('span', { class: 'sc' }, h('span', { class: 'k', text: scopeTarget(t) }), ' ', idCopy(t, scopeTarget(t) + ' id'))));
  }

  function drawAccess() {
    const f = facts();
    if (!f) return [none('No peer row.')];
    const sc = f.scopes, lim = f.limits;
    const scopes = !sc ? [['Scopes', h('span', { class: 'muted', text: 'not reported by this hub' })]]
      : sc.open ? [['Scopes', h('span', { text: 'allow all (no scopes set)' })]]
        : [['Allow', sc.allow.length ? scopeList(sc.allow) : h('span', { class: 'muted', text: 'everyone not denied' })],
          ['Deny', sc.deny.length ? scopeList(sc.deny) : '—']];
    const limits = f.kind === 'source' ? [['Limits', h('span', { class: 'muted', text: 'n/a for a source' })]]
      : !lim ? [['Limits', h('span', { class: 'muted', text: 'not reported by this hub' })]]
        : [['Sends', h('span', { class: 'mono sm', text: dash(lim.sendsPerMin) + ' / min' })], ['New threads', h('span', { class: 'mono sm', text: dash(lim.threadsPerHour) + ' / hour' })]];
    return [kv([...scopes, ...limits])];
  }

  function drawPrincipal(s) {
    const f = facts();
    if (!f || !f.principalId) { s.count.textContent = ''; return [none(f && f.kind === 'source' ? 'A source has no principal.' : 'No principal.')]; }
    const p = principalFacts(res.principals.get(), f.principalId);
    if (!p) return [kv([['Principal id', idCopy(f.principalId, 'principal id')]]), none('This principal is not in the principal list.')];
    s.count.textContent = '';
    return [kv([
      ['Name', p.name || '—'],
      ['Principal id', idCopy(p.id, 'principal id')],
      ['Peers it created', (p.active ?? 0) + ' active · ' + (p.disabled ?? 0) + ' disabled'],
      ['Create limits', 'max ' + dash(p.maxPeers) + ' peers · ' + dash(p.createsPerHour) + ' / hour (' + dash(p.createsLastHour) + ' in the last hour)'],
      ['Kinds it may create', p.kinds.length ? h('span', { class: 'chips' }, p.kinds.map(k => chip(k))) : '—'],
      ['Default allow', p.defaultAllow.length ? scopeList(p.defaultAllow) : '—'],
      ['Default deny', p.defaultDeny.length ? scopeList(p.defaultDeny) : '—'],
    ])];
  }

  function drawRoutes(s) {
    const f = facts();
    const routes = peerRoutes(res.routes.get(), id, f ? f.principalId : '');
    s.count.textContent = String(routes.length);
    if (!routes.length) return [none('No push routes. This peer pulls its deliveries.')];
    return [table(['Route', 'Owner', 'Kind', 'Host', 'Verified', 'Created'], routes.map(r => h('tr', null,
      h('td', null, idCopy(r.id, 'route id')),
      h('td', { class: 'sm', text: r.via === 'peer' ? 'this peer' : 'its principal' }),
      h('td', { class: 'mono sm', text: r.kind + (r.kind === 'grokbot' && r.runsPerHour ? ' · ' + r.runsPerHour + '/h' : '') }),
      h('td', { class: 'mono sm', text: r.host || '—' }),
      h('td', null, r.verified ? time(r.verified) : h('span', { class: 'bad', text: 'unverified' })),
      h('td', null, time(r.created)))))];
  }

  function drawSubs(s) {
    const list = peerSubs(res.subs.get(), id);
    s.count.textContent = String(list.length);
    if (!list.length) return [none('No subscriptions.')];
    return [table(['Subscription', 'Host', 'Health', 'Expires', 'Last delivery', 'Attempts'], list.map(x => {
      const c = subChip(x.health);
      return h('tr', { title: x.health_note || null },
        h('td', null, h('span', { text: x.name || '—' }), idCopy(x.sub_id, 'subscription id', 'sub')),
        h('td', { class: 'mono sm', text: x.url || '—' }),
        h('td', null, h('span', { class: 'chip health h-' + c.tone, text: c.label })),
        h('td', { class: 'sm', text: expiresIn(x.expires_at, nowMs()) || '—', title: x.expires_at || '' }),
        h('td', { class: 'sm' }, x.last_status === null || x.last_status === undefined ? '—' : String(x.last_status), x.last_delivery_at ? ' · ' + age(x.last_delivery_at) : ''),
        h('td', { text: String(x.attempts ?? 0) }));
    }))];
  }

  function drawHealth(s) {
    const { blocked, failed } = peerHealth(res.health.get(), id);
    s.count.textContent = String(blocked.length + failed.length);
    if (!blocked.length && !failed.length) return [none('No blocked or failed deliveries to this peer.')];
    const out = [];
    if (blocked.length) {
      out.push(h('h3', null, 'Blocked ', h('span', { class: 'n', text: String(blocked.length) })),
        table(['Thread', 'Held by', 'Attempt', 'Waiting', 'Next attempt', 'Last'], blocked.map(b => h('tr', null,
          h('td', null, threadLink(b.thread_id)),
          h('td', null, idCopy(b.delivery_id, 'delivery id')),
          h('td', { text: String(b.attempt || 0) }),
          h('td', { text: String(b.waiting || 0) }),
          h('td', null, time(b.next_attempt_at)),
          h('td', { class: 'sm bad', text: [b.last_status, b.last_error].filter(v => v !== null && v !== undefined && v !== '').join(' · ') || '—' })))));
    }
    if (failed.length) {
      out.push(h('h3', null, 'Failed ', h('span', { class: 'n', text: String(failed.length) })),
        table(['Delivery', 'Thread', 'Status', 'Error', 'When'], failed.map(x => h('tr', null,
          h('td', null, idCopy(x.delivery_id, 'delivery id')),
          h('td', null, threadLink(x.thread_id)),
          h('td', { class: 'bad mono', text: x.last_status === null || x.last_status === undefined ? '—' : String(x.last_status) }),
          h('td', { class: 'sm', text: x.last_error || '' }),
          h('td', null, time(x.updated_at))))));
    }
    return out;
  }

  function drawThreads(s) {
    const { rows, more } = peerThreads(res.threads.get(), id);
    s.count.textContent = rows.length + (more ? '+' : '');
    if (!rows.length) return [none('No threads with this peer.')];
    const out = [table(['Thread', 'Participants', 'Messages', 'Last activity'], rows.map(r => {
      const t = r.thread, parts = t.participants || [];
      return h('tr', null,
        h('td', null, h('a', { href: '#thread/' + t.thread_id, class: 'tl', text: threadTitle(t, ctx.me, nameOf) }), idCopy(t.thread_id, 'thread id', 'sub')),
        h('td', { class: 'pps' }, parts.flatMap((p, i) => [i ? ', ' : '', link(p)])),
        h('td', { text: String(t.message_count ?? 0) }),
        h('td', { text: age(r.last_message_at), title: r.last_message_at || '' }));
    }))];
    if (more) out.push(h('p', { class: 'note' }, 'The ' + plural(rows.length, 'thread') + ' with the newest activity. ', h('a', { href: '#dash', text: 'Every thread is on the dashboard' }), '.'));
    return out;
  }

  function render() {
    renderHead();
    fill('identity', [res.peers], 'Identity', drawIdentity);
    fill('presence', [res.peers], 'Presence', drawPresence);
    fill('access', [res.peers], 'Scopes and limits', drawAccess);
    fill('principal', [res.peers, res.principals], 'Principal', drawPrincipal);
    fill('routes', [res.routes], 'Routes', drawRoutes);
    fill('subs', [res.subs], 'Subscriptions', drawSubs);
    fill('health', [res.health], 'Delivery health', drawHealth); // titles join in once threads load
    if (res.threads) fill('threads', [res.threads], 'Recent threads', drawThreads);
    else sections.threads.body.replaceChildren(none('A source is never a thread participant.'));
  }

  render();
  const live = Object.values(res).filter(Boolean);
  const offs = live.map(r => r.subscribe(render));
  const timer = setInterval(() => { for (const r of live) r.reload(); }, RELOAD_MS);
  return () => {
    clearInterval(timer);
    for (const off of offs) off();
  };
}
