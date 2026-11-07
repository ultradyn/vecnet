// Peer detail (#peer/<id>): everything the operator can see about one peer,
// read-only like the dashboard. Each section has its own read and fails on its
// own; nothing here is fetched per row (one admin view per section, threads by
// participant). Peer-supplied strings become text through h(). Ages use the
// hub clock. The header's message actions open the operator's newest thread
// with the peer, or New message to it (compose.js prefill). A peer that has
// not enrolled gets the Invite card: its invite state, New invite (the same
// paste block as Add agent, shown once) and Revoke invite.
import { prefill } from './compose.js';
import { chip, confirmButton, copyButton, h, idCopy, idText, peerLink, toast } from './dom.js';
import { ago, fullWhen, kindShort, kindTitle, plural, title as threadTitle, when } from './format.js';
import { invitePanel } from './invite.js';
import { DEFAULT_TTL, reinvite, TTLS } from './invite-model.js';
import { findPeer, inviteCard, peerActions, peerFacts, peerHealth, peerRoutes, peerSubs, peerThreads, principalFacts, scopeTarget } from './peer-model.js';
import { chip as subChip, expiresIn } from './subs-model.js';

const RELOAD_MS = 30000;
const THREAD_PAGE = 25;
const errText = e => (e && e.message) || String(e);

function loading() { return h('div', { class: 'loading', text: 'Loading…' }); }
function failure(label, err) {
  return h('div', { class: 'empty error' }, h('b', { text: label + ' unavailable' }), errText(err));
}
function none(text) { return h('p', { class: 'none', text }); }
function kindPill(k) { return h('span', { class: 'pill', title: k ? kindTitle(k) : null }, h('i', { class: 'kd k-' + k }), k ? kindShort(k) : 'unknown'); }
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
// Invite-card errors are fixed sentences. The hub's message, an unknown
// code and a stringified error stay off the page: a credential or a proxy
// body in the reply must not render here. Only these codes have copy.
const INVITE_CODE_ERR = {
  already_enrolled: 'This peer has already enrolled.',
  not_found: 'No peer with this id.',
  forbidden: 'Only the operator can change an invite.',
  rate_limited: 'Too many invite changes. Wait and try again.',
};
function inviteFail(fallback, e) {
  const code = e && typeof e === 'object' && typeof e.code === 'string' ? e.code : '';
  return Object.hasOwn(INVITE_CODE_ERR, code) ? INVITE_CODE_ERR[code] : fallback;
}
const plain = e => e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;

// messageActions draws peerActions (peer-model): the quick link, to the
// operator's newest thread with the peer or else to New message, and New
// message beside it. A plain click on a New message link hands the composer
// its draft (prefill) and routes there; a modified click (a new tab) opens
// the empty composer. o: { name, threadName(thread), age(iso), prefill, nav }.
export function messageActions(acts, o) {
  if (!acts) return [];
  const toCompose = draft => e => {
    if (!plain(e)) return;
    e.preventDefault();
    o.prefill(draft);
    o.nav('#new');
  };
  const btn = (tag, attrs, label, sub, tail) => h(tag, attrs, h('span', { class: 'lb', text: label }), sub ? h('span', { class: 'qt', text: sub }) : null, tail || null);
  const q = acts.open;
  let open;
  if (q.state === 'thread') {
    const name = o.threadName(q.thread);
    open = btn('a', { class: 'btn pri pv-open', href: q.href, title: 'Open your newest thread with ' + o.name + ': ' + name },
      'Open thread', name, q.at ? h('span', { class: 'ago', text: o.age(q.at) }) : null);
  } else if (q.state === 'compose') {
    open = btn('a', { class: 'btn pri pv-open', href: q.href, onclick: toCompose(q.draft),
      title: (q.more ? 'None of the threads with the newest activity has you and ' : 'You have no thread with ') + o.name + (q.more ? '' : ' yet') + '. Opens New message to them.' },
    'Start thread', q.more ? 'none recent with you' : 'none with you yet');
  } else {
    open = btn('button', { type: 'button', class: 'btn pri pv-open', disabled: true, 'aria-busy': q.state === 'loading' ? 'true' : null,
      title: q.state === 'error' ? 'Threads unavailable: ' + errText(q.error) : 'Looking for your newest thread with ' + o.name },
    'Open thread', q.state === 'error' ? 'unavailable' : 'checking…');
  }
  const d = acts.compose.draft;
  const compose = btn('a', { class: 'btn pv-new', href: acts.compose.href, onclick: toCompose(d),
    title: 'New message to ' + o.name + (d.priority === 'wake' ? '' : ', at ' + d.priority) },
  'New message', null, d.priority === 'wake' ? null : h('span', { class: 'pr', text: d.priority }));
  return [open, compose];
}

// INVITE_CHIP labels the card's state chip.
const INVITE_CHIP = { pending: 'pending', expired: 'expired', revoked: 'revoked', none: 'no invite', loading: 'checking' };

// inviteSection is the Invite card (#67) for one peer. draw(card, err) shows
// inviteCard()'s answer (null hides the card) and the status read's error;
// the action buttons persist across draws, so the 30 s reload never takes
// focus off one. New invite re-issues (the previous token stops working) and
// opens the shared copy panel; that panel's token lives only in its text and
// this closure, and Done, a revoke, the peer enrolling or leaving the page
// drops both. o: { api, id, age, until, time, reload, set }.
export function inviteSection(o) {
  const chipEl = h('span', { class: 'chip inv-st' });
  const line = h('p', { class: 'pv-inv-line' });
  const off = h('p', { class: 'note bad pv-inv-off', hidden: true, text: 'Disabled: no invite can enrol this peer until an operator enables it.' });
  const ttl = h('select', { id: 'pv-inv-ttl' }, TTLS.map(([label, s]) => h('option', { value: String(s), text: label, selected: s === DEFAULT_TTL })));
  const ttlCell = h('span', { class: 'pv-inv-ttl' }, h('label', { for: 'pv-inv-ttl', text: 'Valid for' }), ttl);
  const newBtn = h('button', { type: 'button', class: 'btn pri pv-inv-new', text: 'New invite', onclick: () => reissue() });
  const revokeBtn = confirmButton('Revoke invite', 'Confirm revoke', () => revoke());
  revokeBtn.classList.add('pv-inv-revoke');
  const acts = h('div', { class: 'pv-inv-acts' }, ttlCell, newBtn, revokeBtn);
  const msg = h('p', { class: 'pv-inv-msg', role: 'status' });
  const err = h('p', { class: 'err', role: 'alert' });
  const fresh = h('div', { class: 'pv-inv-fresh', hidden: true });
  const el = h('section', { class: 'dash-sec pv-invite', id: 'pv-invite', hidden: true, 'aria-labelledby': 'pv-invite-h' },
    h('h2', { id: 'pv-invite-h' }, 'Invite ', chipEl), line, off, acts, msg, err, fresh);
  let card = null, busy = '', gone = false;

  // closeFresh drops the shown invite: its text leaves the DOM, the closure
  // goes with the panel's nodes, and nothing else ever held it.
  function closeFresh(refocus) {
    const had = !fresh.hidden;
    fresh.replaceChildren();
    fresh.hidden = true;
    if (had && refocus && !newBtn.hidden) newBtn.focus({ preventScroll: true });
  }

  async function reissue() {
    if (busy) return;
    busy = 'new'; err.textContent = ''; msg.textContent = '';
    const was = card && card.state;
    paint();
    try {
      const res = await o.api.reissueInvite(o.id, Number(ttl.value));
      if (gone) return;
      const inv = reinvite(res, location.origin);
      const panel = invitePanel(inv, { onDone: () => closeFresh(true) });
      fresh.replaceChildren(
        was === 'pending' ? h('p', { class: 'pv-inv-msg', text: 'The previous invite no longer works.' }) : null,
        ...panel.nodes);
      fresh.hidden = false;
      busy = '';
      paint();
      panel.copy.focus();
      o.reload();
    } catch (x) {
      if (gone) return;
      busy = '';
      err.textContent = inviteFail('Could not issue a new invite.', x);
      paint();
      o.reload();
    }
  }

  async function revoke() {
    if (busy) return;
    busy = 'revoke'; err.textContent = ''; msg.textContent = '';
    paint();
    try {
      const r = await o.api.revokeInvite(o.id);
      if (gone) return;
      busy = '';
      closeFresh(false);
      msg.textContent = r && r.revoked ? 'Invite revoked.' : 'Nothing to revoke: no invite is live.';
      // It enrolled meanwhile: the card is about to close, so say why.
      if (r && !r.revoked && r.state === 'claimed') toast('Nothing to revoke: this peer has enrolled.');
      if (r && r.peer_id) o.set({ peer_id: r.peer_id, state: r.state, expires_at: r.expires_at, revoked_at: r.revoked_at });
      paint();
      if (!newBtn.disabled) newBtn.focus({ preventScroll: true });
      o.reload(); // a read already in flight must not land over this answer
    } catch (x) {
      if (gone) return;
      busy = '';
      err.textContent = inviteFail('Could not revoke the invite.', x);
      paint();
      o.reload();
    }
  }

  function paint(statusErr) {
    if (!card) {
      el.hidden = true;
      closeFresh(false);
      return;
    }
    el.hidden = false;
    const st = card.state;
    el.className = 'dash-sec pv-invite st-' + st;
    chipEl.className = 'chip inv-st ' + st;
    chipEl.textContent = INVITE_CHIP[st] || st;
    off.hidden = !card.disabled;
    // The sentence, then when: "expires 8 Nov 02:04 · in 23h 59m".
    const at = (label, iso, rel) => (iso ? h('span', { class: 'pv-inv-when' }, label + ' ', o.time(iso, rel)) : null);
    if (st === 'loading') {
      line.replaceChildren(statusErr ? h('span', { class: 'bad', text: inviteFail('Invite status unavailable.', statusErr) }) : 'Checking this peer\'s invite…');
    } else if (st === 'pending') {
      line.replaceChildren('Not enrolled yet. Its invite is waiting to be used. ', at('expires', card.expiresAt, o.until(card.expiresAt)));
    } else if (st === 'expired') {
      line.replaceChildren('Not enrolled. Its invite expired, so no live token can enrol it. ', at('expired', card.expiresAt, o.age(card.expiresAt)));
    } else if (st === 'revoked') {
      line.replaceChildren('Not enrolled. Its invite was revoked, so its token no longer enrols. ', at('revoked', card.revokedAt, o.age(card.revokedAt)));
    } else {
      line.replaceChildren('Not enrolled, and no invite has been issued.');
    }
    // New invite needs a known state or a failed read (the hub refuses an
    // enrolled peer itself); Revoke only a live invite.
    const ready = st !== 'loading' || !!statusErr;
    acts.hidden = !ready;
    newBtn.disabled = revokeBtn.disabled = ttl.disabled = !!busy;
    newBtn.textContent = busy === 'new' ? 'Issuing…' : 'New invite';
    newBtn.setAttribute('aria-busy', busy === 'new' ? 'true' : 'false');
    revokeBtn.hidden = !card.canRevoke && busy !== 'revoke';
    if (busy === 'revoke') revokeBtn.textContent = 'Revoking…';
    else if (!revokeBtn.classList.contains('armed')) revokeBtn.textContent = 'Revoke invite';
  }

  return {
    el,
    draw(next, statusErr) {
      card = next;
      paint(statusErr);
    },
    // known: the card shows, so the peer has not enrolled.
    known: () => !!card,
    unmount() { gone = true; closeFresh(false); },
  };
}

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

  // The actions box stays in the header across redraws, and is redrawn only
  // when what it offers changes, so the 30 s reload never takes keyboard
  // focus off a button.
  const headMain = h('div', { class: 'pv-main' });
  const acts = h('div', { class: 'pv-acts', role: 'group', 'aria-label': 'Message this peer' });
  let actsKey = '';
  const head = h('header', { class: 'pv-head' }, headMain, acts);

  // The Invite card reads the invite status only once the peer row shows a
  // peer that could need one (no live credential, a kind that enrols by
  // token); every other peer page costs no extra request.
  let inviteRes = null, inviteOff = null;
  const invite = inviteSection({
    api, id, age,
    until: iso => expiresIn(iso, nowMs()),
    time: (iso, rel) => h('time', { datetime: iso, title: fullWhen(iso) + '\n' + iso, text: when(iso, nowMs()) + ' · ' + rel }),
    reload: () => { if (inviteRes) inviteRes.reload(); },
    set: v => { if (inviteRes) inviteRes.set(v); },
  });
  function renderInvite() {
    const f = facts();
    const routes = res.routes.get() ? peerRoutes(res.routes.get(), id, f ? f.principalId : '') : null;
    if (!inviteRes && inviteCard(f, null, routes)) {
      inviteRes = store.resource('peers', () => api.inviteStatus(id));
      inviteOff = inviteRes.subscribe(render);
    } else if (inviteRes && f && f.credential === 'active') {
      // It enrolled: no card from here on, so stop reading.
      inviteOff();
      inviteRes = inviteOff = null;
    }
    const status = inviteRes ? inviteRes.get() : null;
    invite.draw(inviteCard(f, status, routes), inviteRes && !status ? inviteRes.error() : null);
  }

  root.replaceChildren(
    h('div', { class: 'dash pv' },
      h('p', { class: 'crumb' }, h('a', { href: '#dash', text: 'Dashboard' }), ' / peer'),
      head,
      invite.el,
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

  function renderActs(f) {
    const a = f && res.threads ? peerActions(f, res.threads.get(), ctx.me, res.threads.error()) : null;
    const q = a && a.open;
    const key = JSON.stringify(a && [q.state, q.href || '', q.thread ? threadTitle(q.thread, ctx.me, nameOf) : '', !!q.at, !!q.more, errText(q.error || ''), f.name, a.compose.draft]);
    if (key === actsKey) {
      const ago = acts.querySelector('.ago');
      if (ago && q.at) ago.textContent = age(q.at);
      return;
    }
    actsKey = key;
    // A new offer (another thread became the newest, say) replaces the
    // buttons; focus on one moves to its replacement.
    const had = acts.contains(document.activeElement) ? (document.activeElement.classList.contains('pv-new') ? '.pv-new' : '.pv-open') : '';
    const life = [];
    if (f && f.kind !== 'operator' && f.kind !== 'source') {
      const run = async action => {
        try { await api.peerLifecycle(id, action); toast('Peer ' + (action === 'enable' ? 'enabled' : action + 'd') + '.'); res.peers.reload(); }
        catch (e) { toast('Could not ' + action + ' peer: ' + errText(e), true); }
      };
      if (f.archivedAt) life.push(h('button', {type: 'button', class: 'btn', text: 'Unarchive', onclick: () => run('unarchive')}));
      else {
        life.push(h('button', {type: 'button', class: 'btn', text: f.disabled ? 'Re-enable' : 'Disable', onclick: () => run(f.disabled ? 'enable' : 'disable')}));
        const archive = confirmButton('Archive', 'Confirm archive', () => run('archive'));
        archive.classList.add('bad'); life.push(archive);
      }
    }
    acts.replaceChildren(...messageActions(a, { name: (f && f.name) || id, threadName: t => threadTitle(t, ctx.me, nameOf), age, prefill, nav: ctx.nav }), ...life);
    const next = had && acts.querySelector(had);
    if (next && !next.disabled) next.focus({ preventScroll: true });
  }

  function renderHead() {
    const f = facts(), err = res.peers.error();
    renderActs(f);
    if (!f) {
      const missing = res.peers.get() && !err;
      headMain.replaceChildren(...[
        h('h1', { text: missing ? 'Unknown peer' : nameOf(id) }),
        h('div', { class: 'pv-id' }, h('span', { class: 'mono pid', text: id }), copyButton(id, 'peer id')),
        missing ? h('div', { class: 'empty' }, h('b', { text: 'No peer with this id' }), 'The hub lists no peer or source with this id. It may have been removed, or the link is wrong.') : null,
      ].filter(Boolean));
      return;
    }
    head.className = 'pv-head' + (f.disabled ? ' off' : '');
    headMain.replaceChildren(...[
      h('h1', null, h('span', { class: 'nm', text: f.name || '(no name)' }),
        kindPill(f.kind),
        f.archivedAt ? chip('archived', 'bad') : (f.disabled ? chip('disabled', 'bad') : null)),
      h('div', { class: 'pv-id' }, h('span', { class: 'mono pid', text: f.id }), copyButton(f.id, 'peer id')),
      f.archivedAt ? h('p', { class: 'note bad', text: 'Archived: credentials were revoked. Unarchive leaves this peer disabled and requires a new invite.' }) : (f.disabled ? h('p', { class: 'note bad', text: 'Disabled: this peer cannot use the hub until an operator enables it.' }) : null),
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
      ['Kind', kindPill(f.kind)],
      ['Principal', principalCell(f.principalId)],
      ['Created by', createdByCell(f.createdBy)],
      // enrolled_at is when the peer was added; one that has not enrolled
      // yet (the Invite card says so) reads as such.
      ['Enrolled', invite.known() ? h('span', null, h('span', { class: 'muted', text: 'not yet · added ' }), time(f.enrolled)) : time(f.enrolled)],
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
      ['Kinds it may create', p.kinds.length ? h('span', { class: 'chips' }, p.kinds.map(k => h('span', { class: 'chip', text: kindShort(k), title: kindTitle(k) }))) : '—'],
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
    renderInvite();
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
  const timer = setInterval(() => { for (const r of live) r.reload(); if (inviteRes) inviteRes.reload(); }, RELOAD_MS);
  return () => {
    clearInterval(timer);
    for (const off of offs) off();
    if (inviteOff) inviteOff();
    invite.unmount();
  };
}
