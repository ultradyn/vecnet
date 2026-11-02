// One thread: history, reply composer. ctx.threadId names the thread (see
// app.js). Operator UI Task 9 reworks this page.
import { avatar, h } from './dom.js';
import { bytes, short, title, when } from './format.js';

const SPILL = 4096, TEXT_CAP = 65536, PAGE = 200, MAX_PAGES = 50;
const RANK = { queued: 0, pushed: 1, accepted: 2, read: 3, processed: 4, failed: 5, expired: 5, dead_letter: 5 };
const enc = new TextEncoder();

function randKey() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return 'ui-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

function excerpt(m) {
  if (m.approval_request) return 'Approval: ' + m.approval_request.question;
  if (m.approval_response) return 'Decision: ' + m.approval_response.decision;
  if (m.job_status) return 'Job ' + m.job_status.state + (m.content.text ? ' · ' + m.content.text : '');
  return (m.content && m.content.text) || '(' + m.kind + ')';
}

function structured(m) {
  const box = (k, ...rest) => h('div', { class: 'box' }, h('span', { class: 'k', text: k + ' ' }), ...rest);
  if (m.approval_request) return box('approval', m.approval_request.question, ' · options: ' + (m.approval_request.options || []).join(' / ') + ' · expires ' + when(m.approval_request.expires_at));
  if (m.approval_response) return box('decision', m.approval_response.decision + (m.approval_response.reason ? ' · ' + m.approval_response.reason : ''));
  if (m.job_request) return box('job', m.job_request.operation + ' · ' + m.job_request.job_key);
  if (m.job_status) return box('job ' + short(m.job_status.job_id), m.job_status.state + ' · sequence ' + m.job_status.sequence);
  return null;
}

async function loadAll(api, id) {
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

export function mount(root, ctx) {
  const { api, store, me, nameOf, kindOf, toast, threadId } = ctx;
  const history = store.resource('thread:' + threadId, () => loadAll(api, threadId));
  const scroller = root.closest('main') || root;
  const S = {
    replyTo: '',
    prio: 'wake',
    pending: null,          // unsent reply: keeps its idempotency_key across retries
    receipts: new Map(),    // message_id -> Map(recipient -> state)
  };
  let gone = false;

  const summary = () => ((ctx.threads.get() || {}).threads || []).find(t => t.thread_id === threadId);
  const msgs = () => history.get() || [];

  function setReceipt(msg, who, state) {
    let m = S.receipts.get(msg);
    if (!m) S.receipts.set(msg, m = new Map());
    const old = m.get(who);
    if (old === undefined || (RANK[state] ?? 0) >= (RANK[old] ?? 0)) m.set(who, state);
  }
  function lastFromOther(list) {
    for (let i = list.length - 1; i >= 0; i--) if (list[i].sender !== me) return list[i].message_id;
    return list.length ? list[list.length - 1].message_id : '';
  }

  async function send(ta, btn) {
    const text = ta.value;
    if (!text.trim()) return;
    if (enc.encode(text).length > TEXT_CAP) { toast('Message is over 64 KiB', true); return; }
    const p = S.pending;
    if (!p || p.text !== text || p.prio !== S.prio || p.replyTo !== S.replyTo) {
      S.pending = { key: randKey(), text, prio: S.prio, replyTo: S.replyTo };
    }
    const body = { idempotency_key: S.pending.key, thread_id: threadId, priority: S.prio, content: { text } };
    if (S.replyTo) body.in_reply_to = S.replyTo;
    btn.disabled = true;
    try {
      const r = await api.send(body);
      S.pending = null;
      ta.value = '';
      counter(ta);
      for (const dl of r.deliveries || []) setReceipt(r.message_id, dl.recipient, dl.state);
      toast(r.spilled ? 'Sent (spilled into message.md)' : 'Sent');
      S.replyTo = '';
      store.invalidate('threads');
      await history.reload();
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

  function composer() {
    const ta = h('textarea', { 'aria-label': 'Reply', rows: '3' });
    const btn = h('button', { type: 'button', class: 'btn pri', text: 'Send' });
    btn.addEventListener('click', () => send(ta, btn));
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(ta, btn); } });
    ta.addEventListener('input', () => counter(ta));
    const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Priority' },
      ['wake', 'wake_idle', 'hold'].map(p => h('button', { type: 'button', value: p, text: p, onclick: () => { S.prio = p; render(); } })));
    return h('div', { class: 'composer' },
      h('div', { class: 'wrap' },
        h('div', { class: 'to', hidden: true }, h('span'), h('button', { type: 'button', title: 'Reply to the thread instead', text: '×', onclick: () => { S.replyTo = ''; render(); } })),
        ta,
        h('div', { class: 'bar' }, seg, h('span', { class: 'bytes', text: '0 / ' + SPILL + ' B' }), h('span', { class: 'hint', text: 'Enter to send · Shift+Enter for a new line' }), btn)));
  }

  function message(m, byId) {
    const parent = m.in_reply_to ? byId.get(m.in_reply_to) : null;
    const others = (m.recipients || []).map(nameOf).join(', ');
    const rc = S.receipts.get(m.message_id);
    const atts = (m.attachments || []).map(a => h('span', { class: 'att' + (m.metadata && m.metadata['vecnet.spilled'] ? ' spill' : ''), title: a.sha256 },
      a.name || a.media_type, h('span', { class: 'sz', text: bytes(a.size) })));
    return h('div', { class: 'msg' + (m.message_id === S.replyTo ? ' target' : ''), id: 'm-' + m.message_id },
      avatar(m.sender, ctx),
      h('div', {},
        h('header', {},
          h('b', { text: m.sender === me ? nameOf(m.sender) + ' (you)' : nameOf(m.sender) }),
          h('span', { class: 'kind', text: kindOf(m.sender) }),
          h('span', { class: 't', text: when(m.created_at), title: m.created_at }),
          h('span', { class: 'prio ' + m.priority, text: m.priority }),
          others ? h('span', { class: 'via', text: 'to ' + others }) : null,
          h('span', { class: 'acts' }, h('button', { type: 'button', text: 'reply', onclick: () => { S.replyTo = m.message_id; render(); comp.querySelector('textarea').focus(); } }))),
        m.in_reply_to ? h('div', { class: 'reply' }, h('b', { text: '↳ reply to ' + (parent ? nameOf(parent.sender) : short(m.in_reply_to)) }), parent ? h('span', { text: excerpt(parent) }) : null) : null,
        m.content && m.content.text ? h('div', { class: 'txt', text: m.content.text }) : null,
        m.content && m.content.truncated ? h('span', { class: 'trunc', text: 'Preview only. The hub spilled the full text into an attachment.' }) : null,
        structured(m),
        atts.length ? h('div', { class: 'atts' }, atts) : null,
        rc && rc.size ? h('div', { class: 'rc' }, [...rc].map(([who, st]) => h('span', { class: 'st-' + st }, h('i'), nameOf(who) + ' ' + st))) : null));
  }

  const head = h('div', { class: 'thead' }), list = h('div', { class: 'msgs' }), comp = composer();
  root.replaceChildren(head, list, comp);

  function render() {
    if (gone) return;
    const t = summary(), all = msgs();
    head.replaceChildren(h('h1', { text: t ? title(t, me, nameOf) : short(threadId) }),
      h('div', { class: 'ids', text: threadId + (t ? ' · ' + t.message_count + ' messages' : '') }));
    const atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
    const byId = new Map(all.map(m => [m.message_id, m]));
    if (history.error() && !history.get()) {
      list.replaceChildren(h('div', { class: 'empty error' }, h('b', { text: 'Thread unavailable' }), history.error().message));
    } else if (history.loading() && !all.length) {
      list.replaceChildren(h('div', { class: 'loading', text: 'Loading thread…' }));
    } else {
      list.replaceChildren(...all.map(m => message(m, byId)));
    }
    const to = comp.querySelector('.to'), target = byId.get(S.replyTo);
    to.hidden = !target;
    if (target) to.firstElementChild.textContent = '↳ replying to ' + nameOf(target.sender) + ': ' + excerpt(target).slice(0, 60);
    comp.querySelector('textarea').placeholder = 'Message ' + (t ? (t.participants || []).filter(p => p !== me).map(nameOf).join(', ') : 'this thread');
    for (const b of comp.querySelectorAll('.seg button')) b.className = b.value === S.prio ? 'on' : '';
    if (atBottom) scroller.scrollTop = scroller.scrollHeight;
  }

  // The default reply target is picked per load, not per render, or dismissing
  // it with × would bring it straight back.
  let loaded;
  const onHistory = () => {
    if (history.get() !== loaded) {
      loaded = history.get();
      if (!S.replyTo || !msgs().some(m => m.message_id === S.replyTo)) S.replyTo = lastFromOther(msgs());
    }
    render();
  };
  render();
  const off = [history.subscribe(onHistory), ctx.threads.subscribe(render), ctx.peers.subscribe(render)];
  return () => {
    gone = true;
    off.forEach(fn => fn());
  };
}
