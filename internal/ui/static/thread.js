// One thread: history with receipts, spilled text and inline approvals, and
// the reply composer. ctx.threadId names the thread (see app.js). The pure
// rules live in thread-model.js; reading (per-message read receipts as the
// operator sees each unread message) in thread-read.js.
import { answerBody, countdown } from './approvals.js';
import { createAttachments } from './attach.js';
import { blobView, nextExpiry, sendRefusal } from './attach-model.js';
import { avatar, h, idText, peerLink } from './dom.js';
import { ago, bytes, clean, deltaLabel, deltaTitle, fullWhen, kindShort, kindTitle, plural, short, title, when } from './format.js';
import { markdown } from './md.js';
import { prioritySeg } from './prio.js';
import { createReactions } from './react.js';
import { budgetBanner, budgetOut, canShowFull, clientLabel, clientSuspect, excerpt, FULL_TEXT_CAP, items, nextDivider, opening, replyKey, replySig, sendError, SPILL_BYTES, TEXT_CAP, unreadOf } from './thread-model.js';
import { createReader, enough } from './thread-read.js';
import { createThumbs } from './thumbs.js';

const PAGE = 200, MAX_PAGES = 50, PIN_SLACK = 80;
// Observer steps: every 5%, geometric below, so even a message hundreds of
// views tall reports once it fills 60% of the view.
const STEPS = [0, ...Array.from({ length: 13 }, (_, i) => 0.002 * 1.5 ** i), ...Array.from({ length: 20 }, (_, i) => (i + 1) / 20)];
const enc = new TextEncoder();

function randKey() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return 'ui-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

// loadMessages returns the thread's messages and, from the operator's pages'
// hosted_blobs (PROTOCOL §2.8), message id -> the hashes the hub hosts for
// it; hosted is null when a page did not say, so thumbnails keep the
// declared-only rule. states is every page's blob_states (R2 blob id ->
// available/expired/purged/unavailable), the hub's projection on its clock.
async function loadMessages(api, id) {
  const messages = [], states = new Map();
  let after, hosted = new Map();
  for (let i = 0; i < MAX_PAGES; i++) {
    const page = await api.messages(id, { limit: PAGE, after });
    messages.push(...(page.messages || []));
    const bs = page.blob_states;
    if (bs && typeof bs === 'object') for (const [blobId, st] of Object.entries(bs)) if (st && typeof st === 'object') states.set(blobId, st);
    const hb = page.hosted_blobs;
    if (hb && typeof hb === 'object' && hosted) {
      for (const [msgId, shas] of Object.entries(hb)) if (Array.isArray(shas)) hosted.set(msgId, new Set(shas));
    } else {
      hosted = null;
    }
    if (!page.next_after || !(page.messages || []).length) break;
    after = page.next_after;
  }
  return { messages, hosted, states };
}

// r2Chip: an R2 attachment in a message. Never a thumbnail (its bytes are
// fetched only to be saved, from the Files panel); its line says how long it
// lives by its recorded class, and a pending checksum is never shown verified.
function r2Chip(att, v) {
  return h('span', { class: 'att r2 s-' + v.state + (v.verified ? '' : ' unv'), title: (att.sha256 || '') + ' · ' + v.trust, 'data-blob': att.blob_id },
    h('span', { class: 'nm', text: att.name || att.media_type || 'attachment' }),
    h('span', { class: 'sz', text: bytes(att.size) }),
    v.label ? h('span', { class: 'ret', text: v.label }) : null,
    !v.verified && v.state === 'available' ? h('span', { class: 'unv-tag', text: 'unverified' }) : null);
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
    divider: null,          // message id the New divider sits above, or null
  };
  // ticks: live approval countdowns; agos: the relative times on the right
  // of each header. The one-second tick refreshes both.
  let gone = false, ticks = [], agos = [];
  // Reply attachments, and the thread's inline thumbnails: a settled fetch or
  // an upload's change draws again.
  const atts = createAttachments({ api, onChange: () => render(), now: () => store.hubNow() });
  const thumbs = createThumbs({ api, onReady: () => render() });
  // R2 references for the Files panel beside this thread (thread-aside.js):
  // blob id -> { att, state } from the loaded history. Messages carry the
  // recorded class, deadline and trust; the hub's projection says the rest.
  const refSubs = new Set();
  let refs = new Map(), expiryTimer = 0;
  ctx.blobRefs = { get: () => refs, loaded: () => !!history.get(), subscribe: fn => { refSubs.add(fn); return () => refSubs.delete(fn); } };
  const stateOf = blobId => ((history.get() || {}).states || new Map()).get(blobId) || null;
  function collectRefs() {
    const next = new Map();
    for (const m of msgs()) for (const a of m.attachments || []) if (a && a.blob_id) next.set(a.blob_id, { att: a, state: stateOf(a.blob_id) });
    refs = next;
    for (const fn of [...refSubs]) fn();
  }
  // scheduleExpiry redraws at the earliest recorded deadline, then asks the
  // hub again: a chip turns expired on time even before the hub's ticker.
  function scheduleExpiry() {
    clearTimeout(expiryTimer);
    const now = store.hubNow();
    const at = nextExpiry([...refs.values()].map(r => blobView(r.att, r.state, now)), now);
    if (at === null) return;
    expiryTimer = setTimeout(() => { if (gone) return; render(); for (const fn of [...refSubs]) fn(); store.invalidate(tag); }, Math.min(at - now + 250, 6 * 3600 * 1000));
  }
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

  // Reading (CoS unread ruling): open at the first unread under New; each
  // unread message gets its own read receipt once seen (thread-read.js), never
  // a mark-all. Sidebar counts follow the receipts via the hub's events.
  // A modal (image viewer, matrix, invite) hides the thread: nothing is seen under it.
  const active = () => document.visibilityState === 'visible' && document.hasFocus() && !document.querySelector('dialog[open]');
  const styled = on => id => { const el = document.getElementById('m-' + id); if (el) el.classList.toggle('unread', on); };
  const reader = createReader({ api, active, onSeen: styled(false), onLost: styled(true) });
  // opened: the first full snapshot placed the view; considered: unread
  // messages already placed, so any other is a live arrival.
  let unread = [], opened = false, considered = new Set();
  const onActive = () => { reader.activeChanged(); if (io) observe(); };
  const onVisible = () => {
    onActive();
    if (document.visibilityState !== 'visible') return;
    tick(); // relative times caught up at once, not on the next second
  };

  // The observer's view is the viewport less the composer floating over its
  // bottom (nothing under it is seen); rebuilt when the composer resizes.
  let io = null, ioQueued = false;
  function onIntersect(entries) {
    for (const e of entries) {
      const el = e.target;
      if (!el.isConnected || !el.id.startsWith('m-')) continue;
      const root = e.rootBounds, view = root ? root.height : innerHeight;
      const middle = root ? (root.top + root.bottom) / 2 : innerHeight / 2;
      const rootTop = root ? root.top : 0;
      reader.view(el.id.slice(2), {
        intersecting: e.isIntersecting,
        enough: e.isIntersecting && enough(e.intersectionRatio, e.intersectionRect.height, view),
        above: e.boundingClientRect.bottom <= middle,
        // The top has left the root, so only a tail can still be in view.
        pastTop: e.boundingClientRect.top < rootTop - 0.5,
      });
    }
  }
  function observe() {
    if (io) io.disconnect();
    io = new IntersectionObserver(onIntersect, { threshold: STEPS, rootMargin: '0px 0px -' + Math.round(comp.offsetHeight) + 'px 0px' });
    watch();
  }
  // watch observes the unread messages not yet seen; the others need nothing.
  function watch() {
    if (!io) return;
    for (const u of unread) {
      if (reader.seen(u.message_id)) continue;
      const el = document.getElementById('m-' + u.message_id);
      if (el) io.observe(el);
    }
  }
  const coverChanged = () => {
    if (ioQueued) return;
    ioQueued = true;
    requestAnimationFrame(() => { ioQueued = false; if (!gone) observe(); });
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
    fitHint(); // the byte count's width changes the hint's room
  }

  // gate: Send waits for every upload, and for a spill slot when the text is long.
  function gate() {
    const ta = comp.querySelector('textarea'), refusal = sendRefusal(atts.items(), ta.value);
    const btn = comp.querySelector('.send');
    btn.disabled = S.sending || !atts.ready() || !!refusal;
    btn.title = refusal || (!atts.ready() ? 'Waiting for attachments to finish or be removed' : '');
  }

  // fitHint keeps the composer bar on one row: the keyboard hint may wrap to two
  // lines at its · breaks, and is dropped (bar.tight) when even that would clip it.
  // Measured, not a width breakpoint, so the byte count, fonts and zoom all count.
  let fitQueued = false;
  function fitHint() {
    if (fitQueued) return;
    fitQueued = true;
    requestAnimationFrame(() => {
      fitQueued = false;
      const bar = comp && comp.querySelector('.composer .bar'), hint = bar && bar.querySelector('.hint');
      if (!hint) return;
      bar.classList.remove('tight');
      const cs = getComputedStyle(hint), lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.35;
      const box = hint.getBoundingClientRect();
      const clipped = [...hint.children].some(s => { const r = s.getBoundingClientRect(); return r.right > box.right + 1 || r.left < box.left - 1; });
      if (cs.display === 'none' || hint.scrollHeight > lh * 2 + 1 || clipped) bar.classList.add('tight');
    });
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
      h('div', { class: 'bar' }, prio.el, h('span', { class: 'bytes', text: '0 / ' + SPILL_BYTES + ' B' }), h('span', { class: 'hint' }, h('span', { text: 'Drop or paste files' }), ' · ', h('span', { text: 'Enter to send' }), ' · ', h('span', { text: 'Shift+Enter for a new line' })), pick, btn));
    atts.wire(wrap, ta, pick);
    new ResizeObserver(fitHint).observe(wrap);
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
    const files = (m.attachments || []).filter(a => a !== it.spill).map(a => (a.blob_id ? r2Chip(a, blobView(a, stateOf(a.blob_id), store.hubNow())) : thumbs.render(a, hostedOf(m.message_id, a.sha256))));
    const full = S.full.get(m.message_id), showTxt = m.content && m.content.text && !(full && full.state === 'ok');
    const cls = 'msg' + (m.message_id === S.replyTo ? ' target' : '') + (m.message_id === S.flash ? ' flash' : '');
    return h('div', { class: cls, id: 'm-' + m.message_id, onanimationend: e => { if (e.animationName === 'flash') { S.flash = ''; e.currentTarget.classList.remove('flash'); } } },
      avatar(m.sender, ctx),
      h('div', {},
        h('header', {},
          h('b', null, peerLink(m.sender, nameOf(m.sender)), m.sender === me ? ' (you)' : null),
          h('span', { class: 'kind', text: kindShort(kindOf(m.sender)), title: kindTitle(kindOf(m.sender)) }),
          clientLabel(m) ? h('span', { class: 'cli' + (clientSuspect(m) ? ' sus' : ''), text: clientLabel(m), title: clientSuspect(m) ? 'Sent with the operator credential from the vecnet CLI, by a run that looked like an agent' : 'Sent with the operator credential from the vecnet CLI' }) : null,
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
  const cover = new ResizeObserver(coverChanged);
  cover.observe(comp);

  // place reads unread from a full snapshot. The first one places the view
  // and returns the message to open at; later ones may move New for arrivals.
  function place(all, d) {
    unread = unreadOf(all, matrix.get(), { me, now: store.hubNow(), openApprovals: (d && d.open_approvals) || [], answered: [...S.answered.keys()] });
    reader.setUnread(unread);
    if (!opened) {
      opened = true;
      considered = new Set(unread.map(u => u.message_id));
      const o = opening(unread);
      S.divider = o.divider;
      if (o.at === 'bottom') return null;
      S.pinned = false;
      return o.at;
    }
    const fresh = unread.filter(u => !considered.has(u.message_id));
    for (const u of fresh) considered.add(u.message_id);
    S.divider = nextDivider({ divider: S.divider, unread, fresh, seen: reader.seen, watching: active() && S.pinned });
    return null;
  }

  function newDivider() {
    return h('div', { class: 'newdiv', role: 'separator', 'aria-label': 'New messages' }, h('span', { text: 'New' }));
  }

  function render() {
    if (gone) return;
    const t = thread(), all = msgs(), d = detail.get();
    head.replaceChildren(t ? h('h1', { class: t.title ? null : 'un', text: title(t, me, nameOf) }) : h('h1', null, idText(threadId)),
      h('div', { class: 'ids', text: threadId + (t ? ' · ' + plural(t.message_count, 'message') : '') }));
    ticks = [];
    agos = [];
    // Unread needs my deliveries (the matrix); if it fails, history shows
    // and the view is placed once it loads.
    const ready = !!history.get() && !!matrix.get();
    const openAt = ready ? place(all, d) : null;
    if (io) io.disconnect(); // the old nodes are going; watch() takes the new ones
    if (history.error() && !history.get()) {
      list.replaceChildren(h('div', { class: 'empty error' }, h('b', { text: 'Thread unavailable' }), history.error().message));
    } else if ((history.loading() && !all.length) || (!opened && !history.error() && !matrix.error())) {
      // The first draw waits for unread, to open there, not jump from the bottom.
      list.replaceChildren(h('div', { class: 'loading', text: 'Loading thread…' }));
    } else {
      // A rebuild replaces every node: a focused thumbnail keeps its focus.
      const f = document.activeElement, keep = f && list.contains(f) && f.matches('button.thumb') && f.closest('.msg');
      const its = items(all, matrix.get() || null, (d && d.open_approvals) || []);
      const isUnread = new Set(unread.map(u => u.message_id));
      list.replaceChildren(...its.flatMap((it, i) => {
        const id = it.message.message_id, el = message(it, i ? its[i - 1].message : null);
        if (isUnread.has(id) && !reader.seen(id)) el.classList.add('unread');
        return id === S.divider ? [newDivider(), el] : [el];
      }));
      const again = keep && document.getElementById(keep.id);
      const thumb = again && again.querySelector('button.thumb[data-sha="' + f.dataset.sha + '"]');
      if (thumb) thumb.focus({ preventScroll: true });
      if (io) watch(); else observe();
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
    const at = openAt && (list.querySelector('.newdiv') || document.getElementById('m-' + openAt));
    if (at) { at.scrollIntoView({ block: 'start' }); onScroll(); }
    else if (S.pinned) scroller.scrollTop = scroller.scrollHeight;
  }

  // pinned: at the bottom of the column (of the page at phone width).
  function onScroll() {
    const el = scroller.scrollHeight > scroller.clientHeight + 1 ? scroller : document.scrollingElement;
    S.pinned = el.scrollHeight - el.scrollTop - el.clientHeight < PIN_SLACK;
  }

  // Pick a default on first load or if a selected message disappears. An
  // explicit dismissal ('') survives reloads as well as renders.
  let loaded;
  const onHistory = () => {
    if (history.get() !== loaded) {
      loaded = history.get();
      const list = msgs();
      // An unsent reply keeps the target it was sent with: a load (often the
      // event for that very send, which landed though its answer was lost)
      // must not re-pick it, or the retry would mint a new idempotency key.
      if (!S.pending && (S.replyTo === null || (S.replyTo && !list.some(m => m.message_id === S.replyTo)))) S.replyTo = lastFromOther(list) || null;
      collectRefs();
    }
    render();
    scheduleExpiry();
  };
  render();
  scroller.addEventListener('scroll', onScroll, { passive: true });
  window.addEventListener('scroll', onScroll, { passive: true });
  document.addEventListener('visibilitychange', onVisible);
  document.addEventListener('close', onActive, true); // a modal closed
  window.addEventListener('focus', onActive);
  window.addEventListener('blur', onActive);
  const timer = setInterval(tick, 1000);
  const off = [history.subscribe(onHistory), detail.subscribe(render), matrix.subscribe(render), ctx.threads.subscribe(render), ctx.peers.subscribe(render)];
  return () => {
    gone = true;
    clearInterval(timer);
    clearTimeout(expiryTimer);
    refSubs.clear();
    if (ctx.blobRefs && ctx.blobRefs.get() === refs) delete ctx.blobRefs;
    scroller.removeEventListener('scroll', onScroll);
    window.removeEventListener('scroll', onScroll);
    document.removeEventListener('visibilitychange', onVisible);
    document.removeEventListener('close', onActive, true);
    window.removeEventListener('focus', onActive);
    window.removeEventListener('blur', onActive);
    cover.disconnect();
    if (io) io.disconnect();
    reader.destroy();
    off.forEach(fn => fn());
    atts.destroy();
    thumbs.release();
    R.destroy();
  };
}
