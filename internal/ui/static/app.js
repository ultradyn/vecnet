// vecnet operator inbox (plan Task 19). Plain JS, no build step, no inline
// script (the CSP forbids it). Talks to this hub only:
//   GET  /v1/events?after=  long-poll loop over the operator's durable log
//   POST /v1/ack            once a batch is shown (hold deliveries -> accepted)
//   GET  /v1/peers          names, kinds, presence
//   GET  /v1/threads        thread list
//   GET  /v1/threads/{id}/messages   thread history
//   POST /v1/messages       replies, marked X-Vecnet-Ingress: operator_ui
//   POST /v1/receipts       "read" once a delivery's thread is opened
// The operator secret lives in localStorage and only ever travels in the
// Authorization header, never in a URL (PROTOCOL §2).
'use strict';
(() => {
  const KEY = { secret: 'vecnet.ui.secret', read: 'vecnet.ui.read' };
  const SPILL = 4096, TEXT_CAP = 65536, WAIT = 25, READ_KEEP = 2000;
  const RANK = { queued: 0, pushed: 1, accepted: 2, read: 3, processed: 4, failed: 5, expired: 5, dead_letter: 5 };
  const $ = id => document.getElementById(id);
  const enc = new TextEncoder();

  const S = {
    secret: localStorage.getItem(KEY.secret) || '',
    gen: 0,                 // bumps on sign-in/out; stale loops exit
    peers: new Map(),       // peer_id -> directory entry
    me: '',                 // the operator's peer id
    inbox: new Map(),       // delivery_id -> {d: Delivery, at}
    threads: new Map(),     // thread_id -> ThreadSummary, newest activity first
    receipts: new Map(),    // message_id -> Map(recipient -> state)
    read: new Set(loadRead()),
    synced: false,          // first replay of the log finished
    conn: 'connecting',
    thread: '',             // open thread id
    msgs: [],
    loading: false,
    replyTo: '',
    prio: 'wake',
    pending: null,          // unsent reply: keeps its idempotency_key across retries
  };

  // ---------- helpers ----------
  function loadRead() {
    try { const a = JSON.parse(localStorage.getItem(KEY.read) || '[]'); return Array.isArray(a) ? a : []; } catch { return []; }
  }
  function saveRead() {
    const a = [...S.read].slice(-READ_KEEP);
    localStorage.setItem(KEY.read, JSON.stringify(a));
  }
  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === undefined || v === null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const c of kids.flat()) if (c !== null && c !== undefined && c !== false) el.append(c);
    return el;
  }
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  function randKey() {
    const b = crypto.getRandomValues(new Uint8Array(16));
    return 'ui-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
  }
  function short(id) { return id && id.length > 14 ? id.slice(0, id.indexOf('_') + 1) + '…' + id.slice(-5) : (id || '?'); }
  function name(id) { const p = S.peers.get(id); return p ? p.name : short(id); }
  function kind(id) { const p = S.peers.get(id); return p ? p.kind : (id || '').split('_')[0]; }
  function avatar(id) {
    const n = name(id), k = kind(id);
    let x = 0; for (const c of id || '') x = (x * 31 + c.charCodeAt(0)) >>> 0;
    return h('div', { class: 'av ' + (k === 'operator' ? 'op' : 'k' + (x % 6)), text: (n[0] || '?').toUpperCase(), 'aria-hidden': 'true' });
  }
  function when(iso) {
    const d = new Date(iso); if (isNaN(d)) return '';
    const t = d.toLocaleTimeString('en-AU', { hour: '2-digit', minute: '2-digit', hour12: false });
    return d.toDateString() === new Date().toDateString() ? t : d.toLocaleDateString('en-AU', { day: 'numeric', month: 'short' }) + ' ' + t;
  }
  function bytes(n) { return n < 1024 ? n + ' B' : n < 1 << 20 ? (n / 1024).toFixed(1) + ' KiB' : (n / 1048576).toFixed(1) + ' MiB'; }
  function title(t) {
    if (!t) return '';
    if (t.title) return t.title;
    const others = (t.participants || []).filter(p => p !== S.me).map(name);
    return others.length ? others.join(', ') : short(t.thread_id);
  }
  let toastT = 0;
  function toast(msg, bad) {
    const el = $('toast'); el.textContent = msg; el.className = 'on' + (bad ? ' bad' : '');
    clearTimeout(toastT); toastT = setTimeout(() => { el.className = bad ? 'bad' : ''; }, bad ? 6000 : 2500);
  }

  // ---------- API ----------
  class APIError extends Error {
    constructor(status, code, message, details) { super(message); this.status = status; this.code = code; this.details = details || {}; }
  }
  async function api(method, path, body, headers) {
    const hd = { Authorization: 'Bearer ' + S.secret, Accept: 'application/json', ...(headers || {}) };
    if (body !== undefined) hd['Content-Type'] = 'application/json';
    let r;
    try {
      r = await fetch(path, { method, headers: hd, body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store', credentials: 'omit' });
    } catch (e) { throw new APIError(0, 'network', 'hub unreachable'); }
    if (r.status === 204) return null;
    let j = null; try { j = await r.json(); } catch { /* non-JSON body */ }
    if (!r.ok) {
      const e = (j && j.error) || {};
      const err = new APIError(r.status, e.code || 'http_' + r.status, e.message || r.statusText, e.details);
      if (r.status === 401) signOut('The hub rejected that secret (' + err.code + ').');
      throw err;
    }
    return j;
  }

  // ---------- sign-in ----------
  function showLogin(msg) {
    $('app').hidden = true; $('login').hidden = false;
    $('login-err').textContent = msg || '';
    $('secret').value = ''; $('secret').focus();
  }
  function signOut(msg) {
    S.gen++; S.secret = ''; localStorage.removeItem(KEY.secret);
    showLogin(msg);
  }
  async function signIn(secret) {
    S.secret = secret;
    const gen = ++S.gen;
    reset();
    try {
      await loadPeers();
    } catch (e) {
      if (gen === S.gen) showLogin(e.status === 401 ? 'The hub rejected that secret.' : 'Could not reach the hub: ' + e.message);
      return;
    }
    if (gen !== S.gen) return;
    localStorage.setItem(KEY.secret, secret);
    $('login').hidden = true; $('app').hidden = false;
    $('hubname').textContent = location.host;
    route();
    loadThreads();
    pump(gen);
  }
  function reset() {
    S.inbox.clear(); S.threads.clear(); S.receipts.clear(); S.peers.clear();
    S.synced = false; S.thread = ''; S.msgs = []; S.replyTo = ''; S.pending = null; S.me = '';
  }

  // ---------- data ----------
  async function loadPeers() {
    const j = await api('GET', '/v1/peers');
    S.peers.clear();
    for (const p of j.peers || []) S.peers.set(p.peer_id, p);
    if (!S.me) { const op = (j.peers || []).find(p => p.kind === 'operator'); if (op) S.me = op.peer_id; }
  }
  // A peer enrolled after the last directory load: refresh it once, soon.
  let peersTimer = 0;
  function knowPeers(ids) {
    if (ids.some(id => id && id.startsWith('peer_') && !S.peers.has(id))) {
      clearTimeout(peersTimer);
      peersTimer = setTimeout(() => loadPeers().then(render, () => {}), 200);
    }
  }
  let threadsTimer = 0;
  function loadThreadsSoon() { clearTimeout(threadsTimer); threadsTimer = setTimeout(loadThreads, 250); }
  async function loadThreads() {
    try {
      const j = await api('GET', '/v1/threads?limit=200');
      S.threads = new Map((j.threads || []).map(t => [t.thread_id, t]));
      knowPeers([...S.threads.values()].flatMap(t => t.participants || []));
      render();
    } catch (e) { if (e.status !== 401) toast('Threads: ' + e.code, true); }
  }
  async function loadThread(id) {
    S.loading = true;
    const all = [];
    let after = '';
    try {
      for (let i = 0; i < 50; i++) {
        const q = '?limit=200' + (after ? '&after=' + encodeURIComponent(after) : '');
        const j = await api('GET', '/v1/threads/' + encodeURIComponent(id) + '/messages' + q);
        all.push(...(j.messages || []));
        if (!j.next_after || !(j.messages || []).length) break;
        after = j.next_after;
      }
    } catch (e) {
      if (S.thread === id && e.status !== 401) toast('Thread: ' + e.code + ' — ' + e.message, true);
    }
    if (S.thread !== id) return;
    S.loading = false; S.msgs = all;
    if (!S.replyTo || !all.some(m => m.message_id === S.replyTo)) S.replyTo = lastFromOther(all);
    render();
    markRead(id);
  }
  function lastFromOther(msgs) {
    for (let i = msgs.length - 1; i >= 0; i--) if (msgs[i].sender !== S.me) return msgs[i].message_id;
    return msgs.length ? msgs[msgs.length - 1].message_id : '';
  }

  // Every delivery in the open thread gets one "read" receipt, once.
  async function markRead(id) {
    if (document.visibilityState !== 'visible') return;
    for (const { d } of S.inbox.values()) {
      if (d.message.thread_id !== id || S.read.has(d.delivery_id)) continue;
      S.read.add(d.delivery_id);
      try { await api('POST', '/v1/receipts', { delivery_id: d.delivery_id, state: 'read' }); }
      catch (e) { if (e.status === 0 || e.status >= 500) S.read.delete(d.delivery_id); }
    }
    saveRead(); render();
  }

  // ---------- event loop ----------
  // Replays the whole retained log from the start (e0.0; a 409 names the
  // oldest valid cursor) so the inbox is complete on every load, then
  // long-polls. Each batch is acked once shown.
  async function pump(gen) {
    let cur = 'e0.0', wait = 0, backoff = 1000;
    while (gen === S.gen) {
      let j;
      try {
        j = await api('GET', '/v1/events?after=' + encodeURIComponent(cur) + '&wait=' + wait);
      } catch (e) {
        if (gen !== S.gen) return;
        if (e.code === 'cursor_purged' && e.details.oldest_cursor) {
          // Mid-session, events were purged while we were away: re-sync
          // thread history explicitly before resuming (PROTOCOL §2.3).
          if (cur !== 'e0.0') { loadThreads(); if (S.thread) loadThread(S.thread); }
          cur = e.details.oldest_cursor; continue;
        }
        if (e.status === 401) return;
        if (e.status === 403) { setConn('off', 'forbidden: ' + e.code); return; }
        setConn('off', 'offline, retrying'); await sleep(backoff); backoff = Math.min(backoff * 2, 30000); continue;
      }
      if (gen !== S.gen) return;
      if (j === null) { setConn('off', 'peer disabled'); return; } // 204: stop reconnecting
      backoff = 1000;
      const evs = j.events || [];
      for (const ev of evs) handle(ev);
      cur = j.next_cursor || cur;
      if (evs.length) ack(cur);
      if (!evs.length && !S.synced) { S.synced = true; }
      wait = evs.length ? 0 : WAIT;
      setConn('live', S.synced ? 'live' : 'catching up');
      render();
    }
  }
  async function ack(cursor) {
    try { await api('POST', '/v1/ack', { cursor }); } catch (e) { if (e.status !== 401) toast('Ack failed: ' + e.code, true); }
  }
  function handle(ev) {
    const d = ev.data || {};
    switch (ev.type) {
      case 'delivery':
        S.inbox.set(d.delivery_id, { d, at: ev.at });
        knowPeers([d.message.sender]);
        if (d.thread) S.threads.set(d.thread.thread_id, { ...(S.threads.get(d.thread.thread_id) || {}), ...d.thread });
        if (S.synced) {
          loadThreadsSoon();
          if (S.thread === d.message.thread_id) loadThreadSoon();
        }
        break;
      case 'receipt': setReceipt(d.message_id, d.by, d.state); break;
      case 'thread_updated': if (d.thread_id) { S.threads.set(d.thread_id, { ...(S.threads.get(d.thread_id) || {}), ...d }); } break;
      case 'presence': { const p = S.peers.get(d.peer_id); if (p) p.presence = d.state; break; }
    }
  }
  let threadTimer = 0;
  function loadThreadSoon() { clearTimeout(threadTimer); threadTimer = setTimeout(() => S.thread && loadThread(S.thread), 150); }
  function setReceipt(msg, who, state) {
    let m = S.receipts.get(msg); if (!m) S.receipts.set(msg, m = new Map());
    const old = m.get(who);
    if (old === undefined || (RANK[state] ?? 0) >= (RANK[old] ?? 0)) m.set(who, state);
  }
  function setConn(cls, label) {
    S.conn = label;
    const el = $('conn'); el.className = 'conn ' + cls; el.lastElementChild.textContent = label;
  }

  // ---------- send ----------
  async function send(ta, btn) {
    const text = ta.value;
    if (!text.trim() || !S.thread) return;
    const n = enc.encode(text).length;
    if (n > TEXT_CAP) { toast('Message is over 64 KiB', true); return; }
    const p = S.pending;
    if (!p || p.text !== text || p.prio !== S.prio || p.thread !== S.thread || p.replyTo !== S.replyTo) {
      S.pending = { key: randKey(), text, prio: S.prio, thread: S.thread, replyTo: S.replyTo };
    }
    const body = { idempotency_key: S.pending.key, thread_id: S.thread, priority: S.prio, content: { text } };
    if (S.replyTo) body.in_reply_to = S.replyTo;
    btn.disabled = true;
    try {
      const r = await api('POST', '/v1/messages', body, { 'X-Vecnet-Ingress': 'operator_ui' });
      S.pending = null; ta.value = ''; counter(ta);
      for (const dl of r.deliveries || []) setReceipt(r.message_id, dl.recipient, dl.state);
      toast(r.spilled ? 'Sent (spilled into message.md)' : 'Sent');
      S.replyTo = '';
      await loadThread(S.thread); loadThreads();
    } catch (e) {
      if (e.status === 401) return;
      // A definite rejection will not change on retry: mint a new key next time.
      if (e.status >= 400 && e.status < 500 && e.status !== 429 && e.status !== 409) S.pending = null;
      toast(e.code + ': ' + e.message, true);
    } finally { btn.disabled = false; }
  }
  function counter(ta) {
    const n = enc.encode(ta.value).length, el = ta.closest('.composer').querySelector('.bytes');
    el.textContent = n > SPILL ? bytes(n) + ' · spills to message.md' : n + ' / ' + SPILL + ' B';
    el.className = 'bytes' + (n > TEXT_CAP ? ' over' : n > SPILL ? ' spill' : '');
  }

  // ---------- routing + rendering ----------
  function route() {
    const m = location.hash.match(/^#thread\/([A-Za-z0-9_]+)$/);
    const id = m ? m[1] : '';
    if (id !== S.thread) {
      S.thread = id; S.msgs = []; S.replyTo = ''; S.loading = !!id;
      $('view').replaceChildren();
      if (id) loadThread(id);
    }
    render();
  }
  function unread() { let n = 0; for (const { d } of S.inbox.values()) if (!S.read.has(d.delivery_id)) n++; return n; }
  function threadUnread(id) { for (const { d } of S.inbox.values()) if (d.message.thread_id === id && !S.read.has(d.delivery_id)) return true; return false; }

  function render() {
    if ($('app').hidden) return;
    renderSide();
    if (S.thread) renderThread(); else renderInbox();
    renderAside();
  }
  function renderSide() {
    const n = unread(), c = $('inbox-count');
    c.textContent = n ? String(n) : ''; c.className = 'cnt' + (n ? ' hot' : '');
    $('nav-inbox').className = S.thread ? '' : 'on';
    const list = [...S.threads.values()].map(t => h('a', {
      href: '#thread/' + t.thread_id, class: (t.thread_id === S.thread ? 'on ' : '') + (threadUnread(t.thread_id) ? 'unread' : ''), title: title(t),
    }, h('span', { class: 'tt', text: title(t) }), h('span', { class: 'cnt', text: String(t.message_count ?? '') })));
    $('threads').replaceChildren(...(list.length ? list : [h('div', { class: 'none', text: S.synced ? 'No threads yet' : '…' })]));
    const me = S.peers.get(S.me);
    $('me').replaceChildren(h('span', { class: 'dot ' + (me ? me.presence : '') }), h('span', { text: me ? me.name + ' · ' + me.presence : 'operator' }));
  }

  function renderInbox() {
    const v = $('view');
    const items = [...S.inbox.values()].sort((a, b) => (a.at < b.at ? 1 : -1));
    const groups = [['wake', 'Woke you'], ['wake_idle', 'Waiting for idle'], ['hold', 'Held']];
    const kids = [h('h1', { text: 'Inbox' }), h('p', { class: 'lede', text: 'Messages addressed to you, newest first. Opening a thread marks its messages read; held messages are accepted once shown here.' })];
    if (!items.length) {
      kids.push(h('div', { class: 'empty' }, h('b', { text: S.synced ? 'Nothing here yet' : 'Loading your inbox…' }),
        S.synced ? 'When a peer sends you a message it appears here as it arrives. This page stays connected to the hub.' : 'Replaying your event log from the hub.'));
    }
    for (const [prio, label] of groups) {
      const g = items.filter(x => x.d.message.priority === prio);
      if (!g.length) continue;
      kids.push(h('h2', {}, label, h('span', { class: 'n', text: String(g.length) })));
      for (const { d, at } of g) {
        const m = d.message, isRead = S.read.has(d.delivery_id), t = S.threads.get(m.thread_id) || d.thread;
        kids.push(h('a', { class: 'item' + (isRead ? ' read' : ''), href: '#thread/' + m.thread_id },
          avatar(m.sender),
          h('div', {},
            h('div', { class: 'who', text: name(m.sender) }),
            h('div', { class: 'meta', text: [kind(m.sender), when(m.created_at || at), title(t)].filter(Boolean).join(' · ') }),
            h('div', { class: 'ex', text: excerpt(m) })),
          h('div', { class: 'rs' }, h('span', { class: 'prio ' + m.priority, text: m.priority }), isRead ? null : h('span', { class: 'new', title: 'unread' }))));
      }
    }
    v.replaceChildren(...kids);
  }
  function excerpt(m) {
    if (m.approval_request) return 'Approval: ' + m.approval_request.question;
    if (m.approval_response) return 'Decision: ' + m.approval_response.decision;
    if (m.job_status) return 'Job ' + m.job_status.state + (m.content.text ? ' · ' + m.content.text : '');
    return (m.content && m.content.text) || '(' + m.kind + ')';
  }

  function renderThread() {
    const v = $('view'), t = S.threads.get(S.thread);
    let head = v.querySelector('.thead'), list = v.querySelector('.msgs'), comp = v.querySelector('.composer');
    if (!comp || comp.dataset.thread !== S.thread) {
      head = h('div', { class: 'thead' }); list = h('div', { class: 'msgs' }); comp = composer();
      v.replaceChildren(head, list, comp);
    }
    head.replaceChildren(h('h1', { text: t ? title(t) : short(S.thread) }),
      h('div', { class: 'ids', text: S.thread + (t ? ' · ' + t.message_count + ' messages' : '') }));
    const atBottom = $('main').scrollHeight - $('main').scrollTop - $('main').clientHeight < 80;
    const byId = new Map(S.msgs.map(m => [m.message_id, m]));
    list.replaceChildren(...(S.loading && !S.msgs.length ? [h('div', { class: 'loading', text: 'Loading thread…' })] : S.msgs.map(m => message(m, byId))));
    const to = comp.querySelector('.to'), target = byId.get(S.replyTo);
    to.hidden = !target;
    if (target) to.firstElementChild.textContent = '↳ replying to ' + name(target.sender) + ': ' + excerpt(target).slice(0, 60);
    comp.querySelector('textarea').placeholder = 'Message ' + (t ? (t.participants || []).filter(p => p !== S.me).map(name).join(', ') : 'this thread');
    for (const b of comp.querySelectorAll('.seg button')) b.className = b.value === S.prio ? 'on' : '';
    if (atBottom) $('main').scrollTop = $('main').scrollHeight;
  }
  function message(m, byId) {
    const parent = m.in_reply_to ? byId.get(m.in_reply_to) : null;
    const others = (m.recipients || []).map(name).join(', ');
    const rc = S.receipts.get(m.message_id);
    const atts = (m.attachments || []).map(a => h('span', { class: 'att' + (m.metadata && m.metadata['vecnet.spilled'] ? ' spill' : ''), title: a.sha256 },
      a.name || a.media_type, h('span', { class: 'sz', text: bytes(a.size) })));
    return h('div', { class: 'msg' + (m.message_id === S.replyTo ? ' target' : ''), id: 'm-' + m.message_id },
      avatar(m.sender),
      h('div', {},
        h('header', {},
          h('b', { text: m.sender === S.me ? name(m.sender) + ' (you)' : name(m.sender) }),
          h('span', { class: 'kind', text: kind(m.sender) }),
          h('span', { class: 't', text: when(m.created_at), title: m.created_at }),
          h('span', { class: 'prio ' + m.priority, text: m.priority }),
          others ? h('span', { class: 'via', text: 'to ' + others }) : null,
          h('span', { class: 'acts' }, h('button', { type: 'button', text: 'reply', onclick: () => { S.replyTo = m.message_id; render(); document.querySelector('.composer textarea').focus(); } }))),
        m.in_reply_to ? h('div', { class: 'reply' }, h('b', { text: '↳ reply to ' + (parent ? name(parent.sender) : short(m.in_reply_to)) }), parent ? h('span', { text: excerpt(parent) }) : null) : null,
        m.content && m.content.text ? h('div', { class: 'txt', text: m.content.text }) : null,
        m.content && m.content.truncated ? h('span', { class: 'trunc', text: 'Preview only. The hub spilled the full text into an attachment.' }) : null,
        structured(m),
        atts.length ? h('div', { class: 'atts' }, atts) : null,
        rc && rc.size ? h('div', { class: 'rc' }, [...rc].map(([who, st]) => h('span', { class: 'st-' + st }, h('i'), name(who) + ' ' + st))) : null));
  }
  function structured(m) {
    const box = (k, ...rest) => h('div', { class: 'box' }, h('span', { class: 'k', text: k + ' ' }), ...rest);
    if (m.approval_request) return box('approval', m.approval_request.question, ' · options: ' + (m.approval_request.options || []).join(' / ') + ' · expires ' + when(m.approval_request.expires_at));
    if (m.approval_response) return box('decision', m.approval_response.decision + (m.approval_response.reason ? ' · ' + m.approval_response.reason : ''));
    if (m.job_request) return box('job', m.job_request.operation + ' · ' + m.job_request.job_key);
    if (m.job_status) return box('job ' + short(m.job_status.job_id), m.job_status.state + ' · sequence ' + m.job_status.sequence);
    return null;
  }
  function composer() {
    const ta = h('textarea', { 'aria-label': 'Reply', rows: '3' });
    const btn = h('button', { type: 'button', class: 'btn pri', text: 'Send' });
    btn.addEventListener('click', () => send(ta, btn));
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(ta, btn); } });
    ta.addEventListener('input', () => counter(ta));
    const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Priority' },
      ['wake', 'wake_idle', 'hold'].map(p => h('button', { type: 'button', value: p, text: p, onclick: () => { S.prio = p; render(); } })));
    const c = h('div', { class: 'composer' },
      h('div', { class: 'wrap' },
        h('div', { class: 'to', hidden: true }, h('span'), h('button', { type: 'button', title: 'Reply to the thread instead', text: '×', onclick: () => { S.replyTo = ''; render(); } })),
        ta,
        h('div', { class: 'bar' }, seg, h('span', { class: 'bytes', text: '0 / ' + SPILL + ' B' }), h('span', { class: 'hint', text: 'Enter to send · Shift+Enter for a new line' }), btn)));
    c.dataset.thread = S.thread;
    return c;
  }

  function renderAside() {
    const a = $('aside'), t = S.thread && S.threads.get(S.thread);
    const kids = [];
    if (t) {
      kids.push(h('h3', { text: 'Participants' }));
      for (const p of t.participants || []) {
        const e = S.peers.get(p);
        kids.push(h('div', { class: 'pp' }, h('span', { class: 'dot ' + (e ? e.presence : '') , title: e ? e.presence : 'unknown' }), h('span', { text: name(p) + (p === S.me ? ' (you)' : '') }), h('span', { class: 'k', text: kind(p) })));
      }
      kids.push(h('h3', { text: 'Thread' }), dl([
        ['id', t.thread_id], ['group', t.group || '—'], ['messages', String(t.message_count)],
        ['budget', t.budget_remaining === null || t.budget_remaining === undefined ? 'unlimited' : t.budget_remaining + ' left'],
        ['history', t.history_access || 'from_join'], ['locked', t.locked_participants ? 'yes' : 'no'],
      ]));
    } else {
      kids.push(h('h3', { text: 'Hub' }), dl([['origin', location.origin], ['stream', S.conn], ['you', S.me ? name(S.me) : '—'], ['peers', String(S.peers.size)]]));
    }
    kids.push(h('h3', { text: 'Receipts' }), ...['queued', 'accepted', 'read', 'processed', 'failed', 'dead_letter', 'expired'].map(s => h('div', { class: 'lg st-' + s }, h('i'), s)));
    a.replaceChildren(...kids);
  }
  function dl(rows) { return h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', { text: k }), h('dd', { text: v })])); }

  // ---------- wiring ----------
  $('login-form').addEventListener('submit', e => { e.preventDefault(); const v = $('secret').value.trim(); if (v) signIn(v); });
  $('signout').addEventListener('click', () => signOut(''));
  window.addEventListener('hashchange', route);
  document.addEventListener('visibilitychange', () => { if (S.thread && document.visibilityState === 'visible') markRead(S.thread); });
  setInterval(() => { if (!$('app').hidden) loadPeers().then(render, () => {}); }, 60000);
  if (S.secret) signIn(S.secret); else showLogin('');
})();
