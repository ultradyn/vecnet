// vecnet operator UI shell: sign-in, nav, routing. Screens are modules that
// export mount(root, ctx) and return an unmount function. Beyond the plan's
// list, ctx carries threadId on a thread route and the session's shared
// `peers`, `threads` and `inboxHead` resources, so a screen need not refetch
// them. The sidebar's thread lists are the shell's own (see renderNav).
// The operator secret stays in localStorage and travels only in
// Authorization (PROTOCOL §2).
import { activityAction } from './activity-model.js';
import { createActivity } from './activity.js';
import { createApi } from './api.js';
import { avatarHash, createAvatarCache } from './avatar-model.js';
import * as compose from './compose.js';
import * as dash from './dash.js';
import { h, peerLink, toast } from './dom.js';
import { short } from './format.js';
import * as inbox from './inbox.js';
import * as matrix from './matrix.js';
import { createNotices } from './notice.js';
import { peerHref } from './peer-model.js';
import * as peer from './peer.js';
import * as presence from './presence.js';
import { OTHER_PAGE, nextLimit, otherThreads, yourThreads } from './sidebar-model.js';
import { createStore } from './store.js';
import * as threadAside from './thread-aside.js';
import * as thread from './thread.js';

const SECRET_KEY = 'vecnet.ui.secret';
const PEERS_REFRESH = 60000;
const CONN_CLASS = { live: 'live', offline: 'off', stopped: 'off', restarting: 'warn' };
const $ = id => document.getElementById(id);

const SCREENS = {
  inbox: { screen: inbox, aside: inbox.mountAside },
  thread: { screen: thread, aside: threadAside.mountAside },
  dash: { screen: dash },
  peer: { screen: peer },
  new: { screen: compose },
};

function parseRoute(hash) {
  if (hash === '#dash') return { name: 'dash', view: 'dash' };
  if (hash === '#new') return { name: 'new', view: 'new' };
  const p = hash.match(/^#peer\/([A-Za-z0-9_]+)$/);
  if (p) return { name: 'peer', view: 'peer', id: p[1] };
  const m = hash.match(/^#(thread|matrix)\/([A-Za-z0-9_]+)$/);
  // #matrix is the thread with its matrix open, so both share one mounted view.
  if (m) return { name: m[1], view: 'thread', id: m[2] };
  return { name: 'inbox', view: 'inbox' };
}

let session = null;

function showLogin(msg) {
  document.body.dataset.route = 'login';
  $('app').hidden = true;
  $('login').hidden = false;
  $('login-err').textContent = msg || '';
  $('secret').value = '';
  $('secret').focus();
}

function signOut(msg) {
  if (session) session.close();
  session = null;
  localStorage.removeItem(SECRET_KEY);
  showLogin(msg);
}

function renderNav(s, route) {
  const threads = (s.threads.get() || {}).threads || [];
  const head = s.inboxHead.get() || {}, count = $('inbox-count'), unread = head.unread;
  count.textContent = unread ? String(unread) : '';
  count.className = 'cnt' + (unread ? ' hot' : '');
  $('nav-inbox').className = route.name === 'inbox' ? 'on' : '';
  $('nav-new').className = route.name === 'new' ? 'on' : '';
  $('nav-dash').className = route.name === 'dash' ? 'on' : '';
  const now = s.store.hubNow(), mineRes = s.mine;
  const yours = yourThreads({
    page: mineRes && mineRes.get(), summaries: s.threads.get() ? threads : null,
    error: s.threads.error() || (mineRes && mineRes.error()), me: s.me, nameOf: s.nameOf, now,
    unread: head.unread_by_thread,
  });
  $('threads').replaceChildren(...(yours.rows.length ? yours.rows.map(r => threadLink(r, route, s.nameOf)) : [h('div', { class: 'none', text: yours.note })]));
  renderOthers(s, route, threads, now);
}

// One sidebar thread row, two lines (sidebar-model threadRow). Peer strings
// go in as text only (dom.h). A titled row's participants are peer links.
// An unread count is a tinted pill that never reads as the inbox badge, and
// takes the message count's place (base.css); zero shows nothing.
function threadLink(r, route, nameOf) {
  const cls = [r.id === route.id ? 'on' : '', r.unread ? 'unread' : ''].filter(Boolean).join(' ');
  return h('a', { href: r.href, class: cls, title: r.tip },
    h('span', { class: 'tt' + (r.untitled ? ' un' : ''), text: r.title }),
    h('span', { class: 'nums' }, h('span', { class: 'cnt', text: r.count }), ...(r.unread ? [h('span', { class: 'ur', text: r.unreadText, 'aria-label': r.unread + ' unread' })] : [])),
    r.ids && r.ids.length ? h('span', { class: 'pp' }, peopleLinks(r, nameOf)) : h('span', { class: 'pp', text: r.people }),
    h('span', { class: 'ago', text: r.active }));
}

// Other Threads: the newest threads the operator is not in.
function renderOthers(s, route, mine, now) {
  const res = s.others;
  const v = otherThreads({
    page: res && res.get(), error: res && res.error(), mine, me: s.me,
    nameOf: s.nameOf, now, limit: s.otherLimit,
  });
  const rows = v.rows.map(r => threadLink(r, route, s.nameOf));
  const tail = v.more === 'show'
    ? h('button', { type: 'button', class: 'linkbtn more', text: 'Show more', onclick: () => { s.otherLimit = nextLimit(s.otherLimit); res.reload(); } })
    : v.more === 'dash' ? h('a', { href: '#dash', class: 'more', text: 'All threads on the dashboard' }) : null;
  $('others').replaceChildren(...(rows.length ? rows : [h('div', { class: 'none', text: v.note })]), ...(tail ? [tail] : []));
}

// The participants line inside a sidebar row: each shown name opens that
// peer (a span, as the row is already a link; see the click handler below).
function peopleLinks(r, nameOf) {
  const ids = r.ids || [];
  const shown = ids.slice(0, 3);
  return [...shown.flatMap((id, i) => [i ? ', ' : '', peerLink(id, nameOf(id), { nested: true })]), ids.length > shown.length ? ' +' + (ids.length - shown.length) : ''];
}

// An announced restart reads as restarting, not offline, until it runs out.
function renderConn(store, notices) {
  const state = notices && notices.phase() === 'restarting' ? 'restarting' : store.conn();
  $('conn').className = 'conn ' + (CONN_CLASS[state] || '');
  $('conn').lastElementChild.textContent = state;
}

function renderRoute(s) {
  const route = parseRoute(location.hash);
  document.body.dataset.route = route.name;
  renderNav(s, route);
  if (!s.ctx) return; // the directory has not named the operator yet
  const key = route.view + (route.id ? '/' + route.id : '');
  if (key !== s.mounted) {
    s.unmount();
    const entry = SCREENS[route.view], ctx = { ...s.ctx, threadId: route.view === 'thread' ? route.id : undefined, peerId: route.view === 'peer' ? route.id : undefined };
    $('view').replaceChildren();
    $('aside').replaceChildren();
    const offView = entry.screen.mount($('view'), ctx);
    const offAside = entry.aside ? entry.aside($('aside'), ctx) : () => {};
    $('main').scrollTop = 0;
    s.unmount = () => { offView(); offAside(); s.unmount = () => {}; };
    s.mounted = key;
  }
  if (route.name === 'matrix' && location.hash !== s.matrixHash) matrix.openMatrix(route.id, s.ctx);
  s.matrixHash = route.name === 'matrix' ? location.hash : '';
}

function start(secret) {
  const stop = new AbortController();
  const api = createApi({
    secret,
    onUnauthorized: () => { if (session === s) signOut('The hub rejected that secret.'); },
  });
  const store = createStore({ api });
  // The operator activity feed (PROTOCOL §2.13): one stream per tab, shared
  // by the sidebar and the dashboard. Activity in threads the operator is not
  // in reaches it only this way.
  const activity = createActivity({ api, signal: stop.signal });
  const notices = createNotices({ api, store, root: $('notices'), onPhase: () => renderConn(store, notices) });
  const peers = store.resource('peers', () => api.peers());
  const threads = store.resource('threads', () => api.myThreads({ limit: 200 }));
  // limit pages deliveries; the unread count and `me` do not depend on it.
  const inboxHead = store.resource('inbox', () => api.inbox({ limit: 1 }));

  let directory, indexed = new Map();
  const byId = () => {
    if (peers.get() !== directory) {
      directory = peers.get();
      indexed = new Map(((directory || {}).peers || []).map(p => [p.peer_id, p]));
    }
    return indexed;
  };
  const nameOf = id => { const p = byId().get(id); return p ? p.name : short(id); };
  const kindOf = id => { const p = byId().get(id); return p ? p.kind : (id || '').split('_')[0]; };
  // Avatar images, one fetch per hash for the session; a hash the directory
  // stops naming has its object URL revoked.
  const avatars = createAvatarCache({ api, URL });
  const avatarOf = id => avatarHash(byId().get(id));
  const pruneAvatars = () => { const d = peers.get(); if (d) avatars.prune((d.peers || []).map(avatarHash).filter(Boolean)); };

  const s = {
    threads, inboxHead, nameOf, store, mine: null, others: null, otherLimit: OTHER_PAGE,
    me: '', ctx: null, mounted: '', matrixHash: '', unmount: () => {}, offs: [],
    close() {
      stop.abort();
      this.unmount();
      for (const off of this.offs) if (typeof off === 'function') off();
      avatars.release();
      $('view').replaceChildren();
      $('aside').replaceChildren();
      $('me').replaceChildren();
      $('threads').replaceChildren();
      $('others').replaceChildren();
    },
  };
  session = s;

  const ready = () => {
    if (s.ctx) return;
    const op = ((peers.get() || {}).peers || []).find(p => p.kind === 'operator');
    if (!op) return;
    s.me = op.peer_id;
    s.ctx = { api, store, activity, me: s.me, nameOf, kindOf, avatars, avatarOf, peers, threads, inboxHead, toast, nav: hash => { location.hash = hash; } };
    s.offs.push(presence.renderNav($('me'), s.ctx), presence.startHeartbeat(s.ctx));
    // Tagged threads: the operator's own sends and reads re-sort it at once.
    // The activity feed keeps it live otherwise; Your Threads' own threads
    // are left to its delivery Events.
    s.others = store.resource('threads', () => api.threads({ excludeParticipant: s.me, limit: s.otherLimit }));
    s.offs.push(s.others.subscribe(() => { if (session === s) renderRoute(s); }));
    // Your Threads as ThreadRows, for their last activity; the shell's own
    // list (GET /v1/threads) carries no times and stands in until this loads.
    s.mine = store.resource('threads', () => api.threads({ participant: s.me, limit: 200 }));
    s.offs.push(s.mine.subscribe(() => { if (session === s) renderRoute(s); }));
    s.offs.push(activity.subscribe(ev => {
      const mine = new Set(((threads.get() || {}).threads || []).map(t => t.thread_id));
      const act = activityAction(s.others.get(), ev, mine);
      if (act && act.reload) s.others.reload();
      else if (act && act.page) s.others.set(act.page);
    }));
  };
  const redraw = () => { if (session === s) { ready(); renderRoute(s); } };

  $('login').hidden = true;
  $('app').hidden = false;
  $('hubname').textContent = location.host;
  $('view').replaceChildren(h('div', { class: 'loading', text: 'Connecting to the hub…' }));
  renderConn(store, notices);
  s.offs.push(
    store.onConn(() => renderConn(store, notices)),
    store.onRestart(ms => notices.poll(ms)),
    activity.subscribe(ev => notices.activity(ev)),
    () => notices.close(),
    threads.subscribe(redraw),
    inboxHead.subscribe(redraw),
    peers.subscribe(redraw),
    peers.subscribe(pruneAvatars),
  );
  const refresh = setInterval(() => peers.reload(), PEERS_REFRESH);
  // A tab shown again re-reads Other Threads, so its relative times are fresh.
  const reloadOthers = () => { if (s.others && !document.hidden) s.others.reload(); };
  document.addEventListener('visibilitychange', reloadOthers);
  s.offs.push(() => clearInterval(refresh), () => document.removeEventListener('visibilitychange', reloadOthers));
  redraw();

  // The pump waits for the directory so conn stays "connecting" until the
  // shell can show something, and a rejected secret never reaches the loop.
  // If the hub is down, the pump's first answered poll retries all three views.
  peers.reload().then(() => {
    if (session !== s) return;
    store.pump(stop.signal);
    activity.start();
  });
}

// A typed secret is proved before the shell opens, so a wrong one stays on the
// login card. A stored one opens the shell at once.
async function signIn(secret) {
  const submit = $('login-form').querySelector('button');
  submit.disabled = true;
  $('login-err').textContent = '';
  try {
    await createApi({ secret }).peers();
    localStorage.setItem(SECRET_KEY, secret);
    start(secret);
  } catch (e) {
    showLogin(e.status === 401 ? 'The hub rejected that secret.' : 'Could not reach the hub: ' + e.message);
  } finally { submit.disabled = false; }
}

$('login-form').addEventListener('submit', e => {
  e.preventDefault();
  const secret = $('secret').value.trim();
  if (secret) signIn(secret);
});
$('signout').addEventListener('click', () => signOut(''));
window.addEventListener('hashchange', () => { if (session) renderRoute(session); });
// A peer named inside another link (an inbox row, a sidebar row) is a span
// with data-peer: a plain click on it opens the peer instead of the row.
// Modified clicks fall through to the row's own link.
document.addEventListener('click', e => {
  const el = e.target instanceof Element ? e.target.closest('.plink[data-peer]') : null;
  if (!el || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const href = peerHref(el.dataset.peer);
  if (!href) return;
  e.preventDefault();
  e.stopPropagation();
  location.hash = href;
}, true);

// The local read list was replaced by the hub's read state.
localStorage.removeItem('vecnet.ui.read');

const stored = localStorage.getItem(SECRET_KEY);
if (stored) start(stored);
else showLogin('');
