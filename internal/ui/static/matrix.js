// The delivery matrix: an extra view the operator opens, never always-on. A
// native modal <dialog> over the thread, messages down, recipients across,
// each cell coloured by the hub's `display` (PROTOCOL §2.13). It reloads on
// the thread's tag while open and is removed from the page when it closes.
import { h } from './dom.js';
import { plural, short, title, when } from './format.js';
import { grid, merge } from './matrix-model.js';

const PAGE = 200;
const LEGEND = [
  ['queued', 'queued'], ['pushed', 'pushed'], ['accepted', 'accepted'], ['read', 'read'],
  ['processed', 'processed'], ['failed', 'failed'], ['expired', 'expired'], ['dead_letter', 'dead letter'],
  ['self', 'sender'], ['none', 'no delivery'],
];
const SKELETON_ROWS = 6, SKELETON_COLS = 5;

let current = null;

// openMatrix shows the matrix for threadId. Esc or × closes it, returns focus
// to whatever opened it and, on the #matrix/<id> route, goes back to the
// thread. Leaving the route closes it too.
export function openMatrix(threadId, ctx) {
  if (current && current.threadId === threadId) return;
  if (current) current.finish({ nav: false, focus: false });

  const { api, store, nameOf } = ctx;
  const route = '#matrix/' + threadId;
  const opener = document.activeElement;
  const subs = [];
  let pages = 1, shown, selected = '', done = false;

  // Every page loaded so far is fetched again, so a refresh updates them all.
  const res = store.resource('thread:' + threadId, async () => {
    let acc = null, after;
    for (let i = 0; i < pages; i++) {
      const page = await api.matrix(threadId, { after, limit: PAGE });
      acc = merge(acc, page);
      if (!page.next_after) break;
      after = page.next_after;
    }
    return acc;
  });

  const heading = h('h2', { id: 'mx-title' });
  const closeBtn = h('button', { type: 'button', class: 'mx-x', 'aria-label': 'Close', title: 'Close (Esc)', text: '×' });
  const scroll = h('div', { class: 'mx-scroll', tabindex: '0', 'aria-label': 'Deliveries by message and recipient' });
  const detail = h('div', { class: 'mx-detail', 'aria-live': 'polite' });
  const status = h('span', { class: 'mx-status', role: 'status' });
  const more = h('button', { type: 'button', class: 'btn mx-more', text: 'Load more', hidden: true });
  const legend = h('div', { class: 'mx-legend', 'aria-label': 'Legend' },
    LEGEND.map(([st, label]) => h('span', { class: 'st-' + st }, h('i'), label)));
  const dialog = h('dialog', { class: 'mx', 'aria-labelledby': 'mx-title' },
    h('header', { class: 'mx-top' }, heading, h('span', { class: 'mx-sub', text: 'message × recipient' }), closeBtn),
    scroll, detail,
    h('footer', { class: 'mx-foot' }, legend, status, more));

  function finish({ nav, focus }) {
    if (done) return;
    done = true;
    for (const fn of subs.splice(0)) fn();
    window.removeEventListener('hashchange', onHash);
    if (dialog.open) dialog.close();
    dialog.remove();
    if (current === self) current = null;
    if (nav && location.hash === route) ctx.nav('#thread/' + threadId);
    if (focus && opener && opener.isConnected && typeof opener.focus === 'function') opener.focus();
  }
  const self = { threadId, finish };
  // The shell signing out (a 401 on any request, or Sign out) hides #app and
  // aborts the pump, so nothing would ever reload this matrix. The modal sits
  // on <body>, outside #app, and would stay over an inert sign-in card; close
  // it and hand focus to the field the sign-in card tried to focus.
  function endSession() {
    finish({ nav: false, focus: false });
    const login = document.getElementById('login');
    const input = login && !login.hidden && login.querySelector('input');
    if (input) input.focus();
  }
  const shell = document.getElementById('app');
  const signedOut = () => !!shell && shell.hidden;
  const onHash = () => { if (location.hash !== route) finish({ nav: false, focus: true }); };

  function cellLabel(id) { return nameOf(id) + ' · ' + id; }

  function renderDetail(rows, recipients) {
    for (const row of rows) {
      const i = row.cells.findIndex(c => c.cell && c.cell.delivery_id === selected);
      if (i < 0) continue;
      const c = row.cells[i].cell, now = store.hubNow();
      const kv = [
        ['delivery', c.delivery_id], ['state', row.cells[i].state], ['attempt', String(c.attempt)],
        ['last status', c.last_status === null || c.last_status === undefined ? '—' : String(c.last_status)],
        ['updated', when(c.updated_at, now) || '—', c.updated_at],
      ];
      if (c.read_at) kv.push(['read', when(c.read_at, now), c.read_at]);
      detail.replaceChildren(
        h('div', { class: 'mx-who' },
          h('b', { text: 'to ' + nameOf(recipients[i]), title: recipients[i] }),
          h('span', { text: 'from ' + nameOf(row.sender) + ': ' + (row.excerpt || '(no text)'), title: row.message_id })),
        h('dl', {}, kv.flatMap(([k, v, t]) => [h('dt', { text: k }), h('dd', { text: v, title: t || null })])));
      return;
    }
    selected = '';
    detail.replaceChildren(h('span', { class: 'mx-hint', text: 'Select a cell for its delivery id, attempt, last status and update time.' }));
  }

  function select(id) {
    selected = id;
    for (const b of scroll.querySelectorAll('.mx-b')) b.classList.toggle('on', b.dataset.dly === id);
    const value = res.get();
    if (value) renderDetail(grid(value), value.recipients);
  }

  function renderGrid(value) {
    const recipients = value.recipients || [], rows = grid(value), now = store.hubNow();
    if (!recipients.length) {
      scroll.replaceChildren(h('div', { class: 'empty', text: 'No deliveries in this thread yet.' }));
      renderDetail([], []);
      return;
    }
    const table = h('div', { class: 'mx-grid', role: 'table', 'aria-labelledby': 'mx-title' },
      h('div', { class: 'mx-tr', role: 'row' },
        h('div', { class: 'mx-corner', role: 'columnheader', text: 'message' }),
        recipients.map(r => h('div', { class: 'mx-h', role: 'columnheader', title: cellLabel(r), text: nameOf(r) }))),
      rows.map(row => h('div', { class: 'mx-tr', role: 'row' },
        h('div', { class: 'mx-m', role: 'rowheader', title: row.message_id },
          h('span', { class: 'mx-t', text: when(row.created_at, now), title: row.created_at }),
          h('b', { text: nameOf(row.sender), title: row.sender }),
          h('span', { class: 'mx-ex', text: row.excerpt || '' })),
        row.cells.map((c, i) => h('div', { class: 'mx-c', role: 'cell' },
          c.cell
            ? h('button', {
              type: 'button', class: 'mx-b st-' + c.state + (c.cell.delivery_id === selected ? ' on' : ''),
              'data-dly': c.cell.delivery_id, 'data-state': c.state,
              'aria-label': nameOf(recipients[i]) + ': ' + c.state, title: nameOf(recipients[i]) + ' · ' + c.state,
              onclick: () => select(c.cell.delivery_id),
            })
            : h('span', { class: 'mx-b st-' + c.state, 'data-state': c.state, title: c.state === 'self' ? 'sender' : 'no delivery' }))))));
    table.style.setProperty('--cols', String(recipients.length));
    scroll.replaceChildren(table);
    renderDetail(rows, recipients);
  }

  function skeleton() {
    const blocks = [];
    for (let r = 0; r < SKELETON_ROWS; r++) {
      blocks.push(h('div', { class: 'sk sk-m' }));
      for (let c = 0; c < SKELETON_COLS; c++) blocks.push(h('div', { class: 'sk' }));
    }
    const box = h('div', { class: 'mx-sk', 'aria-hidden': 'true' }, blocks);
    box.style.setProperty('--cols', String(SKELETON_COLS));
    return box;
  }

  function render() {
    if (done) return;
    const t = ((ctx.threads && ctx.threads.get()) || {}).threads || [];
    const summary = t.find(x => x.thread_id === threadId);
    heading.replaceChildren('Delivery matrix', h('span', { class: 'mx-thr', text: summary ? title(summary, ctx.me, nameOf) : short(threadId), title: threadId }));
    const value = res.get(), err = res.error(), loading = res.loading();
    // A rotated secret signs the shell out; the dialog must not linger over it.
    if (signedOut() || (err && err.status === 401)) { endSession(); return; }
    if (value) {
      if (value !== shown) { shown = value; renderGrid(value); }
    } else if (err) {
      shown = undefined;
      scroll.replaceChildren(h('div', { class: 'empty error', role: 'alert' },
        h('b', { text: 'Delivery matrix unavailable' }), h('span', { text: err.code + ': ' + err.message }),
        h('div', {}, h('button', { type: 'button', class: 'btn mx-retry', text: 'Retry', onclick: () => res.reload() }))));
      detail.replaceChildren();
    } else if (!scroll.querySelector('.mx-sk')) {
      scroll.replaceChildren(h('div', { class: 'mx-loading', text: 'Loading deliveries…' }), skeleton());
      detail.replaceChildren();
    }
    more.hidden = !(value && value.next_after);
    more.disabled = loading;
    more.textContent = loading && value ? 'Loading…' : 'Load more';
    if (value && err) {
      status.replaceChildren(h('span', { class: 'bad', text: 'Refresh failed: ' + err.message }), ' ',
        h('button', { type: 'button', class: 'linkbtn mx-retry', text: 'Retry', onclick: () => res.reload() }));
    } else {
      status.textContent = value ? plural(value.rows.length, 'message') + ' · ' + plural((value.recipients || []).length, 'recipient') + (loading ? ' · refreshing' : '') : '';
    }
  }

  more.addEventListener('click', () => { pages++; res.reload(); });
  closeBtn.addEventListener('click', () => finish({ nav: true, focus: true }));
  // Esc fires cancel and then close; both land here.
  dialog.addEventListener('close', () => finish({ nav: true, focus: true }));
  window.addEventListener('hashchange', onHash);

  current = self;
  document.body.append(dialog);
  render();
  subs.push(res.subscribe(render));
  if (ctx.threads) subs.push(ctx.threads.subscribe(render));
  // A renamed peer redraws its column and rows.
  if (ctx.peers) subs.push(ctx.peers.subscribe(() => { shown = undefined; render(); }));
  if (shell) {
    const watch = new MutationObserver(() => { if (signedOut()) endSession(); });
    watch.observe(shell, { attributes: true, attributeFilter: ['hidden'] });
    subs.push(() => watch.disconnect());
  }
  if (done) return;
  dialog.showModal();
  closeBtn.focus();
}
