// vecnet operator UI shell: sign-in, nav, routing. Screens are modules that
// export mount(root, ctx) and return an unmount function. Beyond the plan's
// list, ctx carries threadId on a thread route and the session's shared
// `peers`, `threads` and `inboxHead` resources, so a screen need not refetch
// them. The operator secret stays in localStorage and travels only in
// Authorization (PROTOCOL §2).
import { createApi } from './api.js';
import * as compose from './compose.js';
import * as dash from './dash.js';
import { h, toast } from './dom.js';
import { short, title } from './format.js';
import * as inbox from './inbox.js';
import * as matrix from './matrix.js';
import * as presence from './presence.js';
import { createStore } from './store.js';
import * as threadAside from './thread-aside.js';
import * as thread from './thread.js';

const SECRET_KEY = 'vecnet.ui.secret';
const PEERS_REFRESH = 60000;
const CONN_CLASS = { live: 'live', offline: 'off', stopped: 'off' };
const $ = id => document.getElementById(id);

const SCREENS = {
  inbox: { screen: inbox, aside: inbox.mountAside },
  thread: { screen: thread, aside: threadAside.mountAside },
  dash: { screen: dash },
  new: { screen: compose },
};

function parseRoute(hash) {
  if (hash === '#dash') return { name: 'dash', view: 'dash' };
  if (hash === '#new') return { name: 'new', view: 'new' };
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
  const count = $('inbox-count'), unread = (s.inboxHead.get() || {}).unread;
  count.textContent = unread ? String(unread) : '';
  count.className = 'cnt' + (unread ? ' hot' : '');
  $('nav-inbox').className = route.name === 'inbox' ? 'on' : '';
  $('nav-new').className = route.name === 'new' ? 'on' : '';
  $('nav-dash').className = route.name === 'dash' ? 'on' : '';
  const rows = threads.map(t => {
    const name = title(t, s.me, s.nameOf);
    return h('a', { href: '#thread/' + t.thread_id, class: t.thread_id === route.id ? 'on' : '', title: name },
      h('span', { class: 'tt', text: name }), h('span', { class: 'cnt', text: String(t.message_count ?? '') }));
  });
  const none = s.threads.error() ? 'Threads unavailable' : s.threads.get() ? 'No threads yet' : '…';
  $('threads').replaceChildren(...(rows.length ? rows : [h('div', { class: 'none', text: none })]));
}

function renderConn(store) {
  const state = store.conn();
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
    const entry = SCREENS[route.view], ctx = { ...s.ctx, threadId: route.id };
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

  const s = {
    threads, inboxHead, nameOf,
    me: '', ctx: null, mounted: '', matrixHash: '', unmount: () => {}, offs: [],
    close() {
      stop.abort();
      this.unmount();
      for (const off of this.offs) if (typeof off === 'function') off();
      $('view').replaceChildren();
      $('aside').replaceChildren();
      $('me').replaceChildren();
    },
  };
  session = s;

  const ready = () => {
    if (s.ctx) return;
    const op = ((peers.get() || {}).peers || []).find(p => p.kind === 'operator');
    if (!op) return;
    s.me = op.peer_id;
    s.ctx = { api, store, me: s.me, nameOf, kindOf, peers, threads, inboxHead, toast, nav: hash => { location.hash = hash; } };
    s.offs.push(presence.renderNav($('me'), s.ctx), presence.startHeartbeat(s.ctx));
  };
  const redraw = () => { if (session === s) { ready(); renderRoute(s); } };

  $('login').hidden = true;
  $('app').hidden = false;
  $('hubname').textContent = location.host;
  $('view').replaceChildren(h('div', { class: 'loading', text: 'Connecting to the hub…' }));
  renderConn(store);
  s.offs.push(
    store.onConn(state => {
      renderConn(store);
      if (state === 'live' && !peers.get()) peers.reload();
    }),
    threads.subscribe(redraw),
    inboxHead.subscribe(redraw),
    peers.subscribe(redraw),
  );
  const refresh = setInterval(() => peers.reload(), PEERS_REFRESH);
  s.offs.push(() => clearInterval(refresh));
  redraw();

  // The pump waits for the directory so conn stays "connecting" until the
  // shell can show something, and a rejected secret never reaches the loop.
  peers.reload().then(() => {
    if (session === s) store.pump(stop.signal);
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

// The local read list was replaced by the hub's read state.
localStorage.removeItem('vecnet.ui.read');

const stored = localStorage.getItem(SECRET_KEY);
if (stored) start(stored);
else showLogin('');
