// One thread: history with receipts, spilled text and inline approvals, and
// the reply composer. ctx.threadId names the thread (see app.js). The pure
// rules live in thread-model.js.
import { answerBody, countdown } from './approvals.js';
import { createAttachments } from './attach.js';
import { sendRefusal } from './attach-model.js';
import { avatar, h, idText, peerLink } from './dom.js';
import { ago, bytes, clean, deltaLabel, deltaTitle, fullWhen, plural, short, title, when } from './format.js';
import { markdown } from './md.js';
import { prioritySeg } from './prio.js';
import { createReactions } from './react.js';
import { budgetBanner, budgetOut, canShowFull, excerpt, FULL_TEXT_CAP, items, replyKey, replySig, sendError, SPILL_BYTES, TEXT_CAP } from './thread-model.js';
import { createThumbs } from './thumbs.js';

const PAGE = 200, MAX_PAGES = 50, PIN_SLACK = 80;
const enc = new TextEncoder();

function randKey() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return 'ui-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

// loadMessages returns the thread's messages and, from the operator's pages'
// hosted_blobs (PROTOCOL §2.8), message id -> the hashes the hub hosts for
// it; hosted is null when a page did not say, so thumbnails keep the
// declared-only rule.
async function loadMessages(api, id) {
  const messages = [];
  let after, hosted = new Map();
  for (let i = 0; i < MAX_PAGES; i++) {
    const page = await api.messages(id, { limit: PAGE, after });
    messages.push(...(page.messages || []));
    const hb = page.hosted_blobs;
    if (hb && typeof hb === 'object' && hosted) {
      for (const [msgId, shas] of Object.entries(hb)) if (Array.isArray(shas)) hosted.set(msgId, new Set(shas));
    } else {
      hosted = null;
    }
    if (!page.next_after || !(page.messages || []).length) break;
    after = page.next_after;
  }
  return { messages, hosted };
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
  if (m.job_status) return box('job', idText(m.job_status.job_id), ' ', h('span', { class: 'js ' + m.job_status.state, text: m.job_status.state }), ' · sequence ' + m.job_status.sequence);
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
    replyTo: null,          // null picks a default; '' is an explicit thread-level reply
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
  // ticks: live approval countdowns; agos: the relative times on the right
  // of each header. The one-second tick refreshes both.
  let gone = false, ticks = [], agos = [];
  // Reply attachments, and the thread's inline thumbnails: a settled fetch or
  // an upload's change draws again.
  const atts = createAttachments({ api, onChange: () => render() });
  const thumbs = createThumbs({ api, onReady: () => render() });
  // Reaction chips and the picker; a reaction's event reloads the thread too.
  const R = createReactions({ api, me, nameOf, onDone: () => store.invalidate(tag), toast });

  const summary = () => ((ctx.threads.get() || {}).threads || []).find(t => t.thread_id === threadId);
  const thread = () => { const d = detail.get(); return (d && d.row && d.row.thread) || summary() || null; };
  const msgs = () => (history.get() || {}).messages || [];
  // hostedOf is the history page's word on one attachment: true, false, or
  // undefined when the hub did not say.
  const hostedOf = (msgId, sha) => {
    const hosted = (history.get() || {}).hosted;
    return hosted ? !!(hosted.get(msgId) || new Set()).has(sha) : undefined;
  };

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
  const onVisible = () => {
    if (document.visibilityState !== 'visible') return;
    if (owedRead) markRead();
    tick(); // relative times caught up at once, not on the next second
  };

  async function send(ta, btn) {
    if (S.sending || !atts.ready()) return;
    const files = atts.toAttachments();
    // The chips this request carries: ready() means every chip is done, and a
    // file added while it is in flight is not among them, so it stays.
    const carried = atts.items().map(it => it.id);
    // Empty text sends when attachments carry the message.
    const text = (ta.value.trim() || !files.length) ? ta.value : '';
    if (!text.trim() && !files.length) return;
    if (enc.encode(text).length > TEXT_CAP) {
      S.banner = { kind: 'error', text: 'payload_too_large: the message is over 64 KiB' };
      render();
      return;
    }
    const refusal = sendRefusal(atts.items(), text);
    if (refusal) {
      S.banner = { kind: 'error', text: refusal };
      render();
      return;
    }
    S.pending = replyKey(S.pending, { text, prio: S.prio, replyTo: S.replyTo, sig: replySig(files) }, randKey);
    const body = { idempotency_key: S.pending.key, thread_id: threadId, priority: S.prio, content: { text } };
    if (S.replyTo) body.in_reply_to = S.replyTo;
    if (files.length) body.attachments = files;
    S.sending = true;
    S.banner = null;
    btn.disabled = true;
    render();
    try {
      const r = await api.send(body);
      S.pending = null;
      ta.value = '';
      atts.clear(carried);
      counter(ta);
      S.replyTo = null;
      S.pinned = true;
      toast(r.spilled ? 'Sent (spilled into message.md)' : 'Sent');
      store.invalidate(tag, 'threads', 'inbox');
    } catch (e) {
      if (e.status === 401) return;
      const err = sendError(e);
      if (!err.keepKey) S.pending = null;
      S.banner = err; // the text and the chips stay on every failure
      if (err.kind === 'budget') store.invalidate(tag, 'threads'); // refresh the stale budget
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
    // Unpin first: a render before the scroll event would snap back to the
    // bottom. onScroll re-pins once the operator returns there.
    S.pinned = false;
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

  // gate: Send waits for every upload, and for a spill slot when the text is long.
  function gate() {
    const ta = comp.querySelector('textarea'), refusal = sendRefusal(atts.items(), ta.value);
    const btn = comp.querySelector('.send');
    btn.disabled = S.sending || !atts.ready() || !!refusal;
    btn.title = refusal || (!atts.ready() ? 'Waiting for attachments to finish or be removed' : '');
  }

  function composer() {
    const ta = h('textarea', { 'aria-label': 'Reply', rows: '3' });
    const btn = h('button', { type: 'button', class: 'btn pri send', text: 'Send' });
    const pick = h('button', { type: 'button', class: 'btn attach', text: 'Attach', title: 'Attach files (or drop or paste them here)' });
    btn.addEventListener('click', () => send(ta, btn));
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(ta, btn); } });
    ta.addEventListener('input', () => { counter(ta); gate(); });
    const prio = prioritySeg(p => { S.prio = p; render(); });
    const wrap = h('div', { class: 'wrap' },
      h('div', { class: 'join', hidden: true, text: 'You are not a participant in this thread. Sending adds you to it.' }),
      h('div', { class: 'to', hidden: true }, h('span'), h('button', { type: 'button', title: 'Reply to the thread instead', text: '×', onclick: () => { S.replyTo = ''; render(); } })),
      ta,
      atts.el,
      h('div', { class: 'bar' }, prio.el, h('span', { class: 'bytes', text: '0 / ' + SPILL_BYTES + ' B' }), h('span', { class: 'hint', text: 'Drop or paste files · Enter to send · Shift+Enter for a new line' }), pick, btn));
    atts.wire(wrap, ta, pick);
    return h('div', { class: 'composer' }, h('div', { class: 'cbanner', role: 'alert', hidden: true }), wrap);
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

  // agoLabel is a message's relative time, refreshed by tick while the page
  // is visible; its title is the full local date and time.
  function agoLabel(iso) {
    const el = h('time', { class: 'ago', datetime: iso, title: fullWhen(iso), text: ago(iso, store.hubNow()) });
    agos.push({ el, iso });
    return el;
  }

  function tick() {
    const now = store.hubNow();
    if (document.visibilityState === 'visible') {
      for (const a of agos) {
        const s = ago(a.iso, now);
        if (a.el.textContent !== s) a.el.textContent = s;
      }
    }
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
      f && f.state === 'ok' ? h('div', { class: 'full' }, markdown(f.text)) : null,
      f && f.state === 'error' ? h('div', { class: 'full-err', text: f.text }) : null,
    ];
  }

  // message draws one item; prev is the message before it in the thread, for
  // the delta beside the timestamp (issue 25).
  function message(it, prev) {
    const m = it.message;
    if (it.system) return systemLine(it);
    const delta = prev ? deltaLabel(prev.created_at, m.created_at) : '';
    const others = (m.recipients || []).map(nameOf).join(', ');
    // Images show inline from the thumbnail cache; everything else is a chip.
    const files = (m.attachments || []).filter(a => a !== it.spill).map(a => thumbs.render(a, hostedOf(m.message_id, a.sha256)));
    const full = S.full.get(m.message_id), showTxt = m.content && m.content.text && !(full && full.state === 'ok');
    const cls = 'msg' + (m.message_id === S.replyTo ? ' target' : '') + (m.message_id === S.flash ? ' flash' : '');
    return h('div', { class: cls, id: 'm-' + m.message_id, onanimationend: e => { if (e.animationName === 'flash') { S.flash = ''; e.currentTarget.classList.remove('flash'); } } },
      avatar(m.sender, ctx),
      h('div', {},
        h('header', {},
          h('b', null, peerLink(m.sender, nameOf(m.sender)), m.sender === me ? ' (you)' : null),
          h('span', { class: 'kind', text: kindOf(m.sender) }),
          h('span', { class: 't', text: when(m.created_at), title: m.created_at }),
          delta ? h('span', { class: 'dt', text: delta, title: deltaTitle(prev.created_at, m.created_at) }) : null,
          h('span', { class: 'prio ' + m.priority, text: m.priority }),
          others ? h('span', { class: 'via' }, 'to ', (m.recipients || []).flatMap((r, i) => [i ? ', ' : '', peerLink(r, nameOf(r))])) : null,
          h('span', { class: 'acts' }, agoLabel(m.created_at), h('button', { type: 'button', text: 'reply', onclick: () => { S.replyTo = m.message_id; render(); comp.querySelector('textarea').focus(); } }))),
        it.parent ? replyQuote(it.parent) : null,
        // A spilled preview is clamped to a few lines; Show full text opens it.
        showTxt ? h('div', { class: 'txt' + (it.spill ? ' clamp' : '') }, markdown(m.content.text)) : null,
        it.spill ? spillBlock(m, it.spill) : (m.content && m.content.truncated ? h('span', { class: 'trunc', text: 'Preview only. The hub truncated this text.' }) : null),
        it.approval ? approvalCard(it.approval) : jobBox(m),
        files.length ? h('div', { class: 'atts' }, files) : null,
        R.chipsRow(m),
        it.receipts.length ? h('div', { class: 'rc' }, it.receipts.map(r => h('span', { class: 'st-' + r.display, title: r.delivery_id + ' · ' + r.state }, h('i'), nameOf(r.recipient) + ' ' + r.display))) : null));
  }

  const head = h('div', { class: 'thead' }), list = h('div', { class: 'msgs' }), comp = composer();
  root.replaceChildren(head, list, comp, R.picker);

  function render() {
    if (gone) return;
    const t = thread(), all = msgs(), d = detail.get();
    head.replaceChildren(t ? h('h1', { class: t.title ? null : 'un', text: title(t, me, nameOf) }) : h('h1', null, idText(threadId)),
      h('div', { class: 'ids', text: threadId + (t ? ' · ' + plural(t.message_count, 'message') : '') }));
    ticks = [];
    agos = [];
    if (history.error() && !history.get()) {
      list.replaceChildren(h('div', { class: 'empty error' }, h('b', { text: 'Thread unavailable' }), history.error().message));
    } else if (history.loading() && !all.length) {
      list.replaceChildren(h('div', { class: 'loading', text: 'Loading thread…' }));
    } else {
      // A rebuild replaces every node: a focused thumbnail keeps its focus.
      const f = document.activeElement, keep = f && list.contains(f) && f.matches('button.thumb') && f.closest('.msg');
      list.replaceChildren(...items(all, matrix.get() || null, (d && d.open_approvals) || []).map((it, i, all) => message(it, i ? all[i - 1].message : null)));
      const again = keep && document.getElementById(keep.id);
      const thumb = again && again.querySelector('button.thumb[data-sha="' + f.dataset.sha + '"]');
      if (thumb) thumb.focus({ preventScroll: true });
    }
    tick();

    const banner = comp.querySelector('.cbanner');
    const shown = S.banner || (budgetOut(t) ? budgetBanner() : null);
    banner.hidden = !shown;
    banner.className = 'cbanner' + (shown ? ' ' + shown.kind : '');
    banner.textContent = shown ? shown.text : '';
    const participants = t && t.participants;
    comp.querySelector('.join').hidden = !participants || participants.includes(me);
    const to = comp.querySelector('.to'), target = all.find(m => m.message_id === S.replyTo);
    to.hidden = !target;
    if (target) to.firstElementChild.textContent = clean('↳ replying to ' + nameOf(target.sender) + ': ' + excerpt(target).slice(0, 60));
    comp.querySelector('textarea').placeholder = 'Message ' + (participants ? participants.filter(p => p !== me).map(nameOf).join(', ') : 'this thread');
    gate();
    for (const b of comp.querySelectorAll('.seg button')) b.className = b.value === S.prio ? 'on' : '';
    if (S.pinned) scroller.scrollTop = scroller.scrollHeight;
  }

  const onScroll = () => { S.pinned = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < PIN_SLACK; };

  // Pick a default on first load or if a selected message disappears. An
  // explicit dismissal ('') survives reloads as well as renders. A load that
  // brings another peer's message marks the thread read again.
  let loaded, seen = null;
  const onHistory = () => {
    if (history.get() !== loaded) {
      loaded = history.get();
      const list = msgs();
      // An unsent reply keeps the target it was sent with: a load (often the
      // event for that very send, which landed though its answer was lost)
      // must not re-pick it, or the retry would mint a new idempotency key.
      if (!S.pending && (S.replyTo === null || (S.replyTo && !list.some(m => m.message_id === S.replyTo)))) S.replyTo = lastFromOther(list) || null;
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
    atts.destroy();
    thumbs.release();
    R.destroy();
  };
}
