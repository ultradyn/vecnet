// The operator inbox (PROTOCOL §2.13): decisions, failed deliveries, the
// subscription attention slot, then what woke the operator and what was held.
// Inbox and chat stay separate: every item links to #thread/<id>. Peer text
// reaches the page only through h() (§14.2).
import { answerBody, countdown } from './approvals.js';
import { avatar, confirmButton, h, idCopy, idText, peerLink } from './dom.js';
import { ago, when } from './format.js';
import { deadLetterParts, heldWhy, olderPager, sections, withOlder } from './inbox-model.js';
import * as presence from './presence.js';
import * as subs from './subs.js';

const PAGE = 50;
const LEDE = 'Everything addressed to you as a peer. Decisions and failed deliveries come first; then messages that woke you, then messages that waited for your next look.';

// mount and mountAside get the same ctx, so the aside reads the page's
// snapshot instead of loading its own.
const pages = new WeakMap();

const sectionId = key => 'inbox-' + key;
const label = option => option ? option[0].toUpperCase() + option.slice(1) : option;
const describe = e => (e && e.code ? e.code + ': ' : '') + ((e && e.message) || String(e));

export function mount(root, ctx) {
  const { api, store } = ctx;
  const inbox = store.resource('inbox', () => api.inbox({ limit: PAGE }));
  pages.set(ctx, inbox);

  // Per-item state outlives a re-render, so a snapshot that lands while a
  // POST is in flight cannot re-enable a card, and an armed Discard stays armed.
  const answering = new Map();  // approval_id → sending | answered | closed | expired
  const cards = new Map();      // approval_id → { a, buttons, exp } as rendered
  const dlControls = new Map(); // delivery_id → { retry, discard }
  const dlBusy = new Set();
  // "Load older" pages, each with the cursor it was read with. They follow
  // the head: every new head re-reads them before the page redraws, because a
  // reload that shifts it moves their cursors and a read elsewhere changes
  // read_at (inbox-model.js olderPager).
  const older = olderPager(before => alive ? api.inbox({ limit: PAGE, before }) : Promise.reject(new Error('inbox closed')));
  let shown = {}, alive = true;

  const attention = h('div', { class: 'attention', id: sectionId('attention') });
  const banner = h('div', { class: 'banner error', role: 'alert', hidden: true });
  const body = h('div', { class: 'inbox' });
  root.replaceChildren(h('h1', { text: 'Inbox' }), h('p', { class: 'lede', text: LEDE }), banner, body);

  const reload = async () => { store.invalidate('inbox'); await store.settled(); };

  // ---- approvals ----

  function paintCard(id) {
    const card = cards.get(id);
    if (!card) return;
    const state = answering.get(id);
    let c = countdown(card.a.expires_at, store.hubNow());
    // The hub's clock wins: a snapshot that still lists the approval after the
    // countdown hit zero means the hub says it is open, so offer it again.
    if (state === 'expired' && !c.expired) answering.delete(id);
    if (!answering.has(id) && c.expired) {
      answering.set(id, 'expired');
      store.invalidate('inbox');
    }
    const now = answering.get(id);
    for (const b of card.buttons) b.disabled = !!now;
    card.el.classList.toggle('closed', now === 'closed' || now === 'expired');
    c = countdown(card.a.expires_at, store.hubNow());
    card.exp.textContent = now === 'closed' ? 'closed' : c.expired ? 'expired' : 'expires in ' + c.text;
    card.exp.classList.toggle('urgent', c.urgent && !c.expired);
    card.exp.classList.toggle('over', now === 'closed' || c.expired);
  }

  // Review Focus 2: one click, one POST. Every option in the card is disabled
  // before the request leaves and stays so until it settles.
  async function answer(a, index) {
    const id = a.approval_id;
    if (answering.has(id)) return;
    let req;
    try { req = answerBody(a, index); } catch { return; }
    answering.set(id, 'sending');
    paintCard(id);
    try {
      await api.send(req);
      answering.set(id, 'answered');
      paintCard(id);
      await reload();
      ctx.toast('Answered “' + req.approval_response.decision + '”');
    } catch (e) {
      if (e && e.code === 'approval_closed') {
        // Answered elsewhere or expired: never retried, never reported as landed.
        answering.set(id, 'closed');
        paintCard(id);
        await reload();
        ctx.toast('Already closed', true);
      } else {
        answering.delete(id);
        paintCard(id);
        ctx.toast(describe(e), true);
      }
    }
  }

  function approvalCard(a) {
    const buttons = (a.options || []).map((option, i) => h('button', {
      type: 'button', class: 'btn' + (i === 0 ? ' pri' : ''), text: label(option), title: option,
      onclick: () => answer(a, i),
    }));
    const exp = h('span', { class: 'exp', title: 'expires ' + a.expires_at });
    const question = a.question || a.text;
    const el = h('div', { class: 'item apr', 'data-id': a.approval_id },
      avatar(a.requester, ctx),
      h('div', { class: 'main' },
        h('div', { class: 'who' }, peerLink(a.requester, ctx.nameOf(a.requester)), ' ', h('span', { class: 'muted', text: 'asks' })),
        h('div', { class: 'q', text: question }),
        a.text && a.text !== question ? h('div', { class: 'ex', text: a.text }) : null,
        h('div', { class: 'opts' }, buttons)),
      h('div', { class: 'rs' }, exp,
        a.thread_title ? h('a', { class: 'meta tl', href: '#thread/' + a.thread_id, title: a.thread_id, text: a.thread_title }) : h('a', { class: 'meta tl', href: '#thread/' + a.thread_id }, idText(a.thread_id))));
    cards.set(a.approval_id, { a, el, buttons, exp });
    paintCard(a.approval_id);
    return el;
  }

  // ---- dead letters ----

  function paintDead(id) {
    const ctl = dlControls.get(id);
    if (!ctl) return;
    ctl.retry.disabled = ctl.discard.disabled = dlBusy.has(id);
  }

  async function act(id, run, done) {
    if (dlBusy.has(id)) return;
    dlBusy.add(id);
    paintDead(id);
    try {
      await run(id);
      await reload();
      ctx.toast(done);
    } catch (e) {
      ctx.toast(describe(e), true);
    } finally {
      dlBusy.delete(id);
      paintDead(id);
    }
  }

  function controls(id) {
    let ctl = dlControls.get(id);
    if (!ctl) {
      ctl = {
        retry: h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => act(id, api.retryDeadLetter, 'Retry queued') }),
        discard: confirmButton('Discard', 'Confirm discard', () => act(id, api.discardDeadLetter, 'Discarded')),
      };
      dlControls.set(id, ctl);
    }
    paintDead(id);
    return ctl;
  }

  function deadCard(dl, nowMs) {
    const ctl = controls(dl.delivery_id);
    const parts = deadLetterParts(dl, nowMs).flatMap((p, i) => [i ? ' · ' : '', p.id ? idCopy(p.id, 'delivery id', p.cls) : h('span', { class: p.cls || null, text: p.text })]);
    return h('div', { class: 'item dl', 'data-id': dl.delivery_id },
      avatar(dl.recipient, ctx),
      h('div', { class: 'main' },
        h('div', { class: 'who' }, 'To ', peerLink(dl.recipient, ctx.nameOf(dl.recipient)), ' ', h('span', { class: 'muted' }, 'from ', peerLink(dl.sender, ctx.nameOf(dl.sender)))),
        h('div', { class: 'meta', title: dl.delivery_id }, parts),
        dl.excerpt ? h('a', { class: 'ex', href: '#thread/' + dl.thread_id, title: dl.thread_id, text: dl.excerpt }) : h('a', { class: 'ex', href: '#thread/' + dl.thread_id, title: dl.thread_id }, idText(dl.message_id)),
        dl.last_error && dl.last_error !== dl.excerpt ? h('div', { class: 'why', text: dl.last_error }) : null),
      h('div', { class: 'rs' }, h('div', { class: 'opts' }, ctl.retry, ctl.discard)));
  }

  // ---- deliveries ----

  function deliveryRow(d, nowMs) {
    const meta = [when(d.created_at, nowMs) + ', ' + ago(d.created_at, nowMs)];
    if (d.kind && d.kind !== 'message') meta.push(d.kind.replace(/_/g, ' '));
    if (d.attachments) meta.push(d.attachments === 1 ? '1 file' : d.attachments + ' files');
    const why = heldWhy(d);
    if (why) meta.push(why);
    return h('a', { class: 'item' + (d.read_at ? ' read' : ''), href: '#thread/' + d.thread_id, 'data-id': d.delivery_id, title: d.delivery_id },
      avatar(d.sender, ctx),
      h('div', { class: 'main' },
        h('div', { class: 'who' }, peerLink(d.sender, ctx.nameOf(d.sender), { nested: true }), ' ', h('span', { class: 'muted', text: 'in' }), ' ', d.thread_title ? h('span', { class: 'tl', text: d.thread_title }) : h('span', { class: 'tl' }, idText(d.thread_id))),
        h('div', { class: 'meta', text: meta.join(' · ') }),
        h('div', { class: 'ex', text: d.excerpt })),
      h('div', { class: 'rs' },
        h('span', { class: 'prio ' + d.priority, text: d.priority }),
        d.read_at ? h('span', { class: 'meta', text: 'read' }) : h('span', { class: 'new', title: 'unread' })));
  }

  // "Load older" asks the pager for one more page and redraws once it is in
  // line with the head. A head that reloads meanwhile does not drop the click:
  // the pass under the new head reads every page asked for.
  function loadOlder(btn) {
    btn.disabled = true;
    if (older.request()) render(true);
  }

  // Bring the older pages in line with the head: on a new head every loaded
  // page is read again from its tail (so no row goes missing between them and
  // their read state is current), and any page asked for is read on. One
  // redraw at the end, so the page does not shrink and regrow meanwhile. A
  // newer head cancels this pass and starts its own.
  async function syncOlder(value) {
    const done = await older.sync(value);
    if (!done || !alive) return;
    if (done.error) ctx.toast(describe(done.error), true);
    render(true);
  }

  // ---- page ----

  function heading(s) {
    return h('h2', { id: sectionId(s.key) }, h('span', { text: s.title }), h('span', { class: 'n', text: s.label }));
  }

  function render(force) {
    if (!alive) return;
    const value = inbox.get(), err = inbox.error();
    // The store notifies on every load start and end; redraw only on news.
    if (!force && value === shown.value && err === shown.err) return;
    shown = { value, err };

    banner.hidden = !(value && err);
    if (value && err) {
      banner.replaceChildren(
        h('span', { text: 'Showing the last inbox: ' + describe(err) }),
        h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => store.invalidate('inbox') }));
    }
    if (!value) {
      subs.renderAttention(attention, [], ctx);
      body.replaceChildren(err
        ? h('div', { class: 'empty error' }, h('b', { text: 'Inbox unavailable' }), describe(err), ' ',
          h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => store.invalidate('inbox') }))
        : h('div', { class: 'loading', text: 'Loading your inbox…' }));
      return;
    }

    if (older.pending(value)) { syncOlder(value); return; }
    const data = withOlder(value, older.pages()), nowMs = store.hubNow();
    const byKey = new Map(sections(data).map(s => [s.key, s]));
    const live = new Set((data.approvals || []).map(a => a.approval_id));
    for (const [id, state] of answering) if (!live.has(id) && state !== 'sending') answering.delete(id);
    const deadIds = new Set((data.dead_letters || []).map(d => d.delivery_id));
    for (const id of dlControls.keys()) if (!deadIds.has(id) && !dlBusy.has(id)) dlControls.delete(id);
    cards.clear();

    const out = [];
    const apr = byKey.get('approvals');
    if (apr) out.push(heading(apr), ...apr.items.map(approvalCard));
    const dead = byKey.get('dead_letters');
    if (dead) out.push(heading(dead), ...dead.items.map(dl => deadCard(dl, nowMs)));
    // The slot stays in place even when empty; subs.js owns what it shows.
    subs.renderAttention(attention, data.attention || [], ctx);
    out.push(attention);
    for (const key of ['woke', 'held']) {
      const s = byKey.get(key);
      if (s) out.push(heading(s), ...s.items.map(d => deliveryRow(d, nowMs)));
    }
    if (data.next_before) {
      const btn = h('button', { type: 'button', class: 'btn more', text: 'Load older' });
      btn.addEventListener('click', () => loadOlder(btn));
      out.push(btn);
    }
    if (!byKey.size) out.push(h('div', { class: 'empty' }, h('b', { text: 'Nothing needs you' }), 'No decisions, failed deliveries or messages are waiting.'));
    body.replaceChildren(...out);
  }

  // Countdowns tick from the hub's clock, never the browser's alone.
  const tick = setInterval(() => { for (const id of [...cards.keys()]) paintCard(id); }, 1000);
  const off = inbox.subscribe(() => render(false));
  render(false);
  return () => {
    alive = false;
    clearInterval(tick);
    off();
    if (pages.get(ctx) === inbox) pages.delete(ctx);
  };
}

export function mountAside(root, ctx) {
  const page = pages.get(ctx) || ctx.inboxHead;
  const fallback = { peer_id: ctx.me, name: ctx.nameOf(ctx.me), presence: 'unknown', presence_at: null };
  const slot = h('div', { class: 'presence-slot' });
  const nav = h('nav', { class: 'onpage', 'aria-label': 'On this page' });
  root.replaceChildren(slot, nav);
  let shownMe, shownKeys = '';
  const render = () => {
    const value = page.get() || ctx.inboxHead.get();
    const me = (value || {}).me || fallback;
    if (me !== shownMe) { shownMe = me; presence.renderAside(slot, me, ctx); }
    const list = sections(value);
    const keys = list.map(s => s.key).join();
    if (keys === shownKeys) return;
    shownKeys = keys;
    nav.replaceChildren(...(list.length ? [h('h3', { text: 'On this page' }), ...list.map(s => h('a', {
      href: '#inbox', text: s.title,
      onclick: e => {
        e.preventDefault();
        const target = document.getElementById(sectionId(s.key));
        if (target) target.scrollIntoView({ block: 'start' });
      },
    }))] : []));
  };
  render();
  return page.subscribe(render);
}
