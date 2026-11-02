// One thread: history with receipts, spilled text and inline approvals, and
// the reply composer. ctx.threadId names the thread (see app.js). The pure
// rules live in thread-model.js.
import { answerBody, countdown } from './approvals.js';
import { avatar, h } from './dom.js';
import { bytes, short, title, when } from './format.js';
import { canShowFull, excerpt, FULL_TEXT_CAP, items, sendError, SPILL_BYTES, TEXT_CAP } from './thread-model.js';

const PAGE = 200, MAX_PAGES = 50, PIN_SLACK = 80;
const enc = new TextEncoder();

function randKey() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return 'ui-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

async function loadMessages(api, id) {
  const all = [];
  let after;
  for (let i = 0; i < MAX_PAGES; i++) {
    const page = await api.messages(id, { limit: PAGE, after });
    all.push(...(page.messages || []));
    if (!page.next_after || !(page.messages || []).length) break;
    after = page.next_after;
  }
  return all;
}

async function loadMatrix(api, id) {
  const out = { recipients: [], rows: [] };
  let after;
  for (let i = 0; i < MAX_PAGES; i++) {
    const page = await api.matrix(id, { limit: PAGE, after });
    for (const r of page.recipients || []) if (!out.recipients.includes(r)) out.recipients.push(r);
    out.rows.push(...(page.rows || []));
    if (!page.next_after || !(page.rows || []).length) break;
    after = page.next_after;
  }
  return out;
}

function jobBox(m) {
  const box = (k, ...rest) => h('div', { class: 'box' }, h('span', { class: 'k', text: k + ' ' }), ...rest);
  if (m.approval_response) return box('decision', m.approval_response.decision + (m.approval_response.reason ? ' · ' + m.approval_response.reason : ''));
  if (m.job_request) return box('job', m.job_request.operation + ' · ' + m.job_request.job_key);
  if (m.job_status) return box('job ' + short(m.job_status.job_id), h('span', { class: 'js ' + m.job_status.state, text: m.job_status.state }), ' · sequence ' + m.job_status.sequence);
  return null;
}

export function mount(root, ctx) {
  const { api, store, me, nameOf, kindOf, toast, threadId } = ctx;
  const tag = 'thread:' + threadId;
  // Three views share the thread's tag, so one event reloads all of them.
  const history = store.resource(tag, () => loadMessages(api, threadId));
  const detail = store.resource(tag, () => api.thread(threadId));
  const matrix = store.resource(tag, () => loadMatrix(api, threadId));
  const scroller = root.closest('main') || root;
  const S = {
    replyTo: '',
    prio: 'wake',
    pending: null,          // unsent reply: keeps its idempotency_key across retries
    sending: false,
    banner: null,           // { kind, text } from the last failed send
    answering: new Set(),   // approval ids with a response in flight
    answered: new Map(),    // approval id -> decision this client sent
    closed: new Set(),      // approval ids the hub called closed (422 approval_closed)
    expired: new Set(),     // approval ids whose countdown reached zero here
    full: new Map(),        // message id -> { state: loading|ok|error, text }
    flash: '',
    pinned: true,
  };
  let gone = false, ticks = [];

  const summary = () => ((ctx.threads.get() || {}).threads || []).find(t => t.thread_id === threadId);
  const thread = () => { const d = detail.get(); return (d && d.row && d.row.thread) || summary() || null; };
  const msgs = () => history.get() || [];

  function lastFromOther(list) {
    for (let i = list.length - 1; i >= 0; i--) if (list[i].sender !== me && list[i].sender !== 'hub') return list[i].message_id;
    return list.length ? list[list.length - 1].message_id : '';
  }

  // Reading: the hub marks the operator's deliveries in this thread read.
  let owedRead = false;
  function markRead() {
    if (document.visibilityState !== 'visible') { owedRead = true; return; }
    owedRead = false;
    api.readThread(threadId).then(r => {
      if (!gone && r && r.marked) store.invalidate('inbox', 'threads', tag);
    }, () => {});
  }
  const onVisible = () => { if (owedRead && document.visibilityState === 'visible') markRead(); };

  async function send(ta, btn) {
    if (S.sending) return;
    const text = ta.value;
    if (!text.trim()) return;
    if (enc.encode(text).length > TEXT_CAP) {
      S.banner = { kind: 'error', text: 'payload_too_large: the message is over 64 KiB' };
      render();
      return;
    }
    const p = S.pending;
    if (!p || p.text !== text || p.prio !== S.prio || p.replyTo !== S.replyTo) {
      S.pending = { key: randKey(), text, prio: S.prio, replyTo: S.replyTo };
    }
    const body = { idempotency_key: S.pending.key, thread_id: threadId, priority: S.prio, content: { text } };
    if (S.replyTo) body.in_reply_to = S.replyTo;
    S.sending = true;
    S.banner = null;
    btn.disabled = true;
    render();
    try {
      const r = await api.send(body);
      S.pending = null;
      ta.value = '';
      counter(ta);
      S.replyTo = '';
      S.pinned = true;
      toast(r.spilled ? 'Sent (spilled into message.md)' : 'Sent');
      store.invalidate(tag, 'threads', 'inbox');
    } catch (e) {
      if (e.status === 401) return;
      const err = sendError(e, thread());
      if (!err.keepKey) S.pending = null;
      S.banner = err; // the text stays in the box on every failure
    } finally {
      S.sending = false;
      btn.disabled = false;
      render();
    }
  }

  async function answer(a, index) {
    if (S.answering.has(a.approval_id) || S.answered.has(a.approval_id) || S.closed.has(a.approval_id)) return;
    let body;
    try { body = answerBody({ ...a, thread_id: threadId }, index); } catch { return; }
    S.answering.add(a.approval_id);
    render();
    try {
      await api.send(body);
      S.answered.set(a.approval_id, body.approval_response.decision);
      toast('Answered: ' + body.approval_response.decision);
      store.invalidate(tag, 'inbox', 'threads');
    } catch (e) {
      if (e.status === 401) return;
      if (e.code === 'approval_closed') {
        // Review Focus 2: show it closed, never retry, never claim it landed.
        S.closed.add(a.approval_id);
        toast('Already closed', true);
        store.invalidate(tag, 'inbox');
      } else {
        toast(e.code + ': ' + e.message, true);
      }
    } finally {
      S.answering.delete(a.approval_id);
      render();
    }
  }

  async function showFull(id, att) {
    if (!canShowFull(att)) return;
    S.full.set(id, { state: 'loading' });
    render();
    let next;
    try {
      const blob = await api.blob(att.sha256);
      if (!blob || !/^text\//i.test(blob.type) || blob.size > FULL_TEXT_CAP) {
        next = { state: 'error', text: 'The hub returned ' + (blob ? (blob.type || 'untyped') + ', ' + bytes(blob.size) : 'nothing') + '; only text up to 1 MiB is shown.' };
      } else {
        next = { state: 'ok', text: await blob.text() };
      }
    } catch (e) {
      if (e.status === 401) return;
      next = { state: 'error', text: (e.code || 'error') + ': ' + e.message };
    }
    if (S.full.has(id)) S.full.set(id, next);
    render();
  }

  function jump(id) {
    const el = document.getElementById('m-' + id);
    if (!el) return;
    S.flash = id;
    el.classList.remove('flash');
    void el.offsetWidth; // restart the animation on a second click
    el.classList.add('flash');
    el.scrollIntoView({ block: 'center' });
  }

  function counter(ta) {
    const n = enc.encode(ta.value).length, el = comp.querySelector('.bytes');
    el.textContent = n > SPILL_BYTES ? bytes(n) + ' · spills to message.md' : n + ' / ' + SPILL_BYTES + ' B';
    el.className = 'bytes' + (n > TEXT_CAP ? ' over' : n > SPILL_BYTES ? ' spill' : '');
  }

  function composer() {
    const ta = h('textarea', { 'aria-label': 'Reply', rows: '3' });
    const btn = h('button', { type: 'button', class: 'btn pri send', text: 'Send' });
    btn.addEventListener('click', () => send(ta, btn));
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(ta, btn); } });
    ta.addEventListener('input', () => counter(ta));
    const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Priority' },
      ['wake', 'wake_idle', 'hold'].map(p => h('button', { type: 'button', value: p, text: p, onclick: () => { S.prio = p; render(); } })));
    return h('div', { class: 'composer' },
      h('div', { class: 'cbanner', role: 'alert', hidden: true }),
      h('div', { class: 'wrap' },
        h('div', { class: 'join', hidden: true, text: 'You are not a participant in this thread. Sending adds you to it.' }),
        h('div', { class: 'to', hidden: true }, h('span'), h('button', { type: 'button', title: 'Reply to the thread instead', text: '×', onclick: () => { S.replyTo = ''; render(); } })),
        ta,
        h('div', { class: 'bar' }, seg, h('span', { class: 'bytes', text: '0 / ' + SPILL_BYTES + ' B' }), h('span', { class: 'hint', text: 'Enter to send · Shift+Enter for a new line' }), btn)));
  }

  function approvalCard(a) {
    const local = S.closed.has(a.approval_id) ? 'closed' : S.answered.has(a.approval_id) ? 'answered' : '';
    const live = a.open && !local && !S.expired.has(a.approval_id);
    const card = h('div', { class: 'apr' + (live ? ' open' : ' shut') + (live && a.actionable ? ' act' : '') });
    card.append(h('div', { class: 'q', text: a.question }));
    const opts = h('div', { class: 'opts' });
    const buttons = [];
    if (live && a.actionable) {
      a.options.forEach((o, i) => {
        const b = h('button', { type: 'button', class: 'btn opt' + (i === 0 ? ' pri' : ''), text: o, onclick: () => answer(a, i) });
        b.disabled = S.answering.has(a.approval_id);
        buttons.push(b);
        opts.append(b);
      });
    } else {
      for (const o of a.options) opts.append(h('span', { class: 'chip' + (o === (S.answered.get(a.approval_id) || a.decision) ? ' on' : ''), text: o }));
    }
    let state;
    if (local === 'closed') state = h('span', { class: 'st', text: 'closed' });
    else if (local === 'answered') state = h('span', { class: 'st', text: 'answered: ' + S.answered.get(a.approval_id) });
    else if (live) {
      state = h('span', { class: 'cd' });
      ticks.push({ el: state, approval: a, buttons });
    } else if (a.decision === 'expired' || S.expired.has(a.approval_id)) state = h('span', { class: 'st', text: 'expired' });
    else state = h('span', { class: 'st', text: a.decision ? 'closed: ' + a.decision : 'closed' });
    if (live && !a.actionable) opts.append(h('span', { class: 'ro', text: 'not addressed to you' }));
    opts.append(state);
    card.append(opts);
    return card;
  }

  function tick() {
    const now = store.hubNow();
    for (const t of ticks) {
      const c = countdown(t.approval.expires_at, now);
      if (c.expired) {
        t.el.textContent = 'expired';
        t.el.className = 'st';
        for (const b of t.buttons) b.disabled = true;
        if (!S.expired.has(t.approval.approval_id)) {
          S.expired.add(t.approval.approval_id);
          store.invalidate(tag, 'inbox');
        }
      } else {
        t.el.textContent = 'expires in ' + c.text;
        t.el.className = 'cd' + (c.urgent ? ' urgent' : '');
      }
    }
  }

  function systemLine(it) {
    const m = it.message;
    return h('div', { class: 'sys', id: 'm-' + m.message_id, title: (m.content && m.content.text) || '' },
      h('span', { text: it.text }),
      it.parent ? h('span', { class: 'of', text: it.parent.excerpt }) : null,
      h('span', { class: 't', text: when(m.created_at), title: m.created_at }));
  }

  function replyQuote(p) {
    const label = '↳ reply to ' + (p.sender ? nameOf(p.sender) : short(p.message_id));
    if (!p.loaded) {
      return h('div', { class: 'reply far', title: p.message_id + ' is not in the loaded history' },
        h('b', { text: label }), p.excerpt ? h('span', { text: p.excerpt }) : null);
    }
    const go = () => jump(p.message_id);
    return h('div', { class: 'reply', role: 'link', tabindex: '0', title: 'Show ' + p.message_id, onclick: go,
      onkeydown: e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); go(); } } },
    h('b', { text: label }), h('span', { text: p.excerpt }));
  }

  function spillBlock(m, att) {
    const f = S.full.get(m.message_id);
    const toggle = h('button', { type: 'button', class: 'linkbtn showfull' });
    if (!f) toggle.textContent = 'Show full text';
    else if (f.state === 'loading') { toggle.textContent = 'Loading full text…'; toggle.disabled = true; }
    else toggle.textContent = 'Hide full text';
    toggle.addEventListener('click', () => {
      if (S.full.has(m.message_id)) { S.full.delete(m.message_id); render(); } else showFull(m.message_id, att);
    });
    if (!canShowFull(att)) { toggle.disabled = true; toggle.title = 'Only text up to 1 MiB is shown here'; }
    return [
      h('span', { class: 'trunc', text: 'Preview only. The hub spilled the full text into message.md.' }),
      h('div', { class: 'atts' },
        h('span', { class: 'att spill', title: att.sha256 }, att.name || 'message.md', h('span', { class: 'sz', text: bytes(att.size) })),
        toggle),
      f && f.state === 'ok' ? h('pre', { class: 'full', text: f.text }) : null,
      f && f.state === 'error' ? h('div', { class: 'full-err', text: f.text }) : null,
    ];
  }

  function message(it) {
    const m = it.message;
    if (it.system) return systemLine(it);
    const others = (m.recipients || []).map(nameOf).join(', ');
    const atts = (m.attachments || []).filter(a => a !== it.spill).map(a => h('span', { class: 'att', title: a.sha256 },
      a.name || a.media_type, h('span', { class: 'sz', text: bytes(a.size) })));
    const full = S.full.get(m.message_id), showTxt = m.content && m.content.text && !(full && full.state === 'ok');
    const cls = 'msg' + (m.message_id === S.replyTo ? ' target' : '') + (m.message_id === S.flash ? ' flash' : '');
    return h('div', { class: cls, id: 'm-' + m.message_id, onanimationend: e => { if (e.animationName === 'flash') { S.flash = ''; e.currentTarget.classList.remove('flash'); } } },
      avatar(m.sender, ctx),
      h('div', {},
        h('header', {},
          h('b', { text: m.sender === me ? nameOf(m.sender) + ' (you)' : nameOf(m.sender) }),
          h('span', { class: 'kind', text: kindOf(m.sender) }),
          h('span', { class: 't', text: when(m.created_at), title: m.created_at }),
          h('span', { class: 'prio ' + m.priority, text: m.priority }),
          others ? h('span', { class: 'via', text: 'to ' + others }) : null,
          h('span', { class: 'acts' }, h('button', { type: 'button', text: 'reply', onclick: () => { S.replyTo = m.message_id; render(); comp.querySelector('textarea').focus(); } }))),
        it.parent ? replyQuote(it.parent) : null,
        showTxt ? h('div', { class: 'txt', text: m.content.text }) : null,
        it.spill ? spillBlock(m, it.spill) : (m.content && m.content.truncated ? h('span', { class: 'trunc', text: 'Preview only. The hub truncated this text.' }) : null),
        it.approval ? approvalCard(it.approval) : jobBox(m),
        atts.length ? h('div', { class: 'atts' }, atts) : null,
        it.receipts.length ? h('div', { class: 'rc' }, it.receipts.map(r => h('span', { class: 'st-' + r.display, title: r.delivery_id + ' · ' + r.state }, h('i'), nameOf(r.recipient) + ' ' + r.display))) : null));
  }

  const head = h('div', { class: 'thead' }), list = h('div', { class: 'msgs' }), comp = composer();
  root.replaceChildren(head, list, comp);

  function render() {
    if (gone) return;
    const t = thread(), all = msgs(), d = detail.get();
    head.replaceChildren(h('h1', { text: t ? title(t, me, nameOf) : short(threadId) }),
      h('div', { class: 'ids', text: threadId + (t ? ' · ' + t.message_count + ' messages' : '') }));
    ticks = [];
    if (history.error() && !history.get()) {
      list.replaceChildren(h('div', { class: 'empty error' }, h('b', { text: 'Thread unavailable' }), history.error().message));
    } else if (history.loading() && !all.length) {
      list.replaceChildren(h('div', { class: 'loading', text: 'Loading thread…' }));
    } else {
      list.replaceChildren(...items(all, matrix.get() || null, (d && d.open_approvals) || []).map(message));
    }
    tick();

    const banner = comp.querySelector('.cbanner');
    banner.hidden = !S.banner;
    banner.className = 'cbanner' + (S.banner ? ' ' + S.banner.kind : '');
    banner.textContent = S.banner ? S.banner.text : '';
    const participants = t && t.participants;
    comp.querySelector('.join').hidden = !participants || participants.includes(me);
    const to = comp.querySelector('.to'), target = all.find(m => m.message_id === S.replyTo);
    to.hidden = !target;
    if (target) to.firstElementChild.textContent = '↳ replying to ' + nameOf(target.sender) + ': ' + excerpt(target).slice(0, 60);
    comp.querySelector('textarea').placeholder = 'Message ' + (participants ? participants.filter(p => p !== me).map(nameOf).join(', ') : 'this thread');
    comp.querySelector('.send').disabled = S.sending;
    for (const b of comp.querySelectorAll('.seg button')) b.className = b.value === S.prio ? 'on' : '';
    if (S.pinned) scroller.scrollTop = scroller.scrollHeight;
  }

  const onScroll = () => { S.pinned = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < PIN_SLACK; };

  // The default reply target is picked per load, not per render, or dismissing
  // it with × would bring it straight back. A load that brings another peer's
  // message marks the thread read again.
  let loaded, seen = null;
  const onHistory = () => {
    if (history.get() !== loaded) {
      loaded = history.get();
      const list = msgs();
      if (!S.replyTo || !list.some(m => m.message_id === S.replyTo)) S.replyTo = lastFromOther(list);
      if (seen && list.some(m => !seen.has(m.message_id) && m.sender !== me)) markRead();
      if (loaded) seen = new Set(list.map(m => m.message_id));
    }
    render();
  };
  render();
  markRead();
  scroller.addEventListener('scroll', onScroll, { passive: true });
  document.addEventListener('visibilitychange', onVisible);
  const timer = setInterval(tick, 1000);
  const off = [history.subscribe(onHistory), detail.subscribe(render), matrix.subscribe(render), ctx.threads.subscribe(render), ctx.peers.subscribe(render)];
  return () => {
    gone = true;
    clearInterval(timer);
    scroller.removeEventListener('scroll', onScroll);
    document.removeEventListener('visibilitychange', onVisible);
    off.forEach(fn => fn());
  };
}
