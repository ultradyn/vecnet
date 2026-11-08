// Sidebar search, phase A: one box above Your Threads. Typing shows peers
// whose name contains the text (the shell's directory, matched here) and
// thread titles the hub matched (GET /v1/threads?q=, PROTOCOL §2.8) in place
// of the thread lists; Esc or the clear button restores them. A peer's
// Threads button narrows the thread results to that peer's threads
// (participant=). "/" focuses the box from anywhere but a text field. Peer
// strings become text through h() only.
import { avatar, h } from './dom.js';
import { kindShort, kindTitle } from './format.js';
import { peerHref } from './peer-model.js';
import { createSearch, peerResults, threadResults } from './search-model.js';

const editable = el => !!(el && el.closest && el.closest('input, textarea, select, [contenteditable=""], [contenteditable=true]'));

// createSearchBox draws the box into `field` and returns the search state
// (createSearch) with focus() and close(). onChange runs on every change, so
// the shell can redraw the sidebar. nameOf names the picked peer's chip.
// Esc in `results` (a focused result) clears as it does in the box.
// refresh() repaints directory names without changing the query or notifying.
export function createSearchBox(field, { api, nameOf, onChange, results = null, options = {} }) {
  const search = createSearch({ api, ...options });
  const input = h('input', {
    // No maxlength: it counts UTF-16 units, the hub counts characters
    // (Q_MAX); past Q_MAX the model sends nothing and says why.
    id: 'search-q', type: 'search', placeholder: 'Search peers, threads',
    autocomplete: 'off', spellcheck: 'false', enterkeyhint: 'search', 'aria-label': 'Search peers by name and your threads by title',
    'aria-keyshortcuts': '/',
  });
  // The picked peer, on its own line under the box so the text keeps the
  // box's width: the thread results are that peer's threads.
  const scope = h('div', { class: 'sr-scope', hidden: true });
  const clear = h('button', { type: 'button', class: 'sr-clear', 'aria-label': 'Clear search', title: 'Clear search (Esc)', hidden: true });
  const hint = h('kbd', { class: 'sr-key', text: '/', 'aria-hidden': 'true', title: 'Press / to search' });
  field.replaceChildren(h('div', { class: 'sr-box' }, h('span', { class: 'sr-ico', 'aria-hidden': 'true' }), input, hint, clear), scope);

  const reset = () => { input.value = ''; search.clear(); input.focus(); };
  const unpick = () => { search.pick(''); input.focus(); };
  let scopeId = '', scopeName = '';
  const draw = () => {
    const st = search.state();
    if (input.value !== st.text) input.value = st.text;
    clear.hidden = !st.open;
    hint.hidden = st.open || document.activeElement === input;
    scope.hidden = !st.participant;
    if (st.participant) {
      const name = nameOf(st.participant);
      if (scopeId !== st.participant || scopeName !== name) scope.replaceChildren(h('span', { class: 'lb', text: 'Threads with' }),
        h('span', { class: 'sr-with' }, h('span', { class: 'nm', text: name, title: name + ' · ' + st.participant }),
          h('button', { type: 'button', class: 'sr-unpick', 'aria-label': 'Stop narrowing to ' + name, title: 'Back to your threads (Backspace in the empty box)', onclick: unpick })));
      scopeName = name;
      input.placeholder = 'Search their thread titles';
    } else {
      scope.replaceChildren();
      scopeName = '';
      input.placeholder = 'Search peers, threads';
    }
    scopeId = st.participant;
  };
  const off = search.subscribe(() => { draw(); onChange(); });

  input.addEventListener('input', () => search.setText(input.value));
  input.addEventListener('focus', () => { hint.hidden = true; });
  input.addEventListener('blur', () => { hint.hidden = search.state().open; });
  input.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      // Esc restores the unfiltered sidebar; an empty box lets focus go.
      e.preventDefault();
      if (search.state().open) reset();
      else input.blur();
    } else if (e.key === 'Backspace' && !input.value && search.state().participant) {
      e.preventDefault();
      unpick();
    }
  });
  clear.addEventListener('click', reset);
  const escResults = e => { if (e.key === 'Escape' && search.state().open) { e.preventDefault(); reset(); } };
  if (results) results.addEventListener('keydown', escResults);
  const slash = e => {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented || editable(e.target)) return;
    if (document.querySelector('dialog[open]') || field.closest('[hidden]')) return;
    e.preventDefault();
    input.focus();
    input.select();
  };
  document.addEventListener('keydown', slash);
  draw();

  return {
    ...search,
    refresh: draw,
    focus: () => input.focus(),
    close() {
      off();
      search.close();
      document.removeEventListener('keydown', slash);
      if (results) results.removeEventListener('keydown', escResults);
      field.replaceChildren();
    },
  };
}

// renderResults draws the search results into el: the Peers group (absent
// while a peer is picked, when the text searches that peer's thread titles
// only) and the Threads group. o: { box, directory, me, nameOf, now, unread,
// lastAt(id), avatarCtx (or null before the shell is ready), threadLink(row) }.
// Focus on a control that is redrawn moves to its replacement, or to the box
// when the control is gone (for example after a successful Retry).
export function renderResults(el, o) {
  const st = o.box.state();
  const focused = el.contains(document.activeElement) ? document.activeElement.dataset.key : '';
  const parts = [];
  if (!st.participant) {
    const pv = peerResults({ directory: o.directory, text: st.text, me: o.me });
    parts.push(h('div', { class: 'grp sr-grp' }, h('span', { text: 'Peers' }), pv.rows.length ? h('span', { class: 'n', text: String(pv.rows.length + pv.more) }) : null));
    const rows = pv.rows.map(r => peerRow(r, o));
    if (pv.more) rows.push(h('div', { class: 'none', text: '+' + pv.more + ' more: type more of the name' }));
    parts.push(h('div', { class: 'nav sr-peers' }, rows.length ? rows : h('div', { class: 'none', text: pv.note })));
  }
  const tv = threadResults(st, { me: o.me, nameOf: o.nameOf, now: o.now, unread: o.unread, lastAt: o.lastAt });
  parts.push(h('div', { class: 'grp sr-grp' }, h('span', { text: tv.heading }), tv.rows.length ? h('span', { class: 'n', text: String(tv.rows.length) + (tv.more ? '+' : '') }) : null));
  const list = tv.rows.map(r => { const a = o.threadLink(r); a.dataset.key = 't:' + r.id; return a; });
  if (tv.note) list.push(h('div', { class: 'none' + (tv.state === 'error' ? ' bad' : ''), text: tv.note, 'aria-busy': tv.state === 'loading' ? 'true' : null }));
  if (tv.retry) list.push(h('button', { type: 'button', class: 'linkbtn more', 'data-key': 'retry', text: tv.busy ? 'Retrying…' : 'Retry', 'aria-disabled': tv.busy ? 'true' : null, onclick: () => o.box.retry() }));
  // While the next page loads the button stays (aria-disabled, not disabled,
  // so it keeps focus); more() ignores a press meanwhile.
  if (tv.more) list.push(h('button', { type: 'button', class: 'linkbtn more', 'data-key': 'more', text: tv.busy ? 'Loading more…' : 'More results', 'aria-disabled': tv.busy ? 'true' : null, onclick: () => o.box.more() }));
  parts.push(h('div', { class: 'nav rows sr-threads' }, list));
  el.replaceChildren(...parts);
  if (focused) {
    const again = el.querySelector('[data-key="' + CSS.escape(focused) + '"]');
    if (again) again.focus({ preventScroll: true });
    else o.box.focus();
  }
}

// One peer match: the existing #peer route, and Threads to search its
// threads. avatar() shares the session cache (GET /v1/peers/{id}/avatar,
// PROTOCOL §8.1); peer names come from the shell's directory (§2.10).
function peerRow(r, o) {
  const href = peerHref(r.id);
  const link = h(href ? 'a' : 'span', { class: 'sr-pl', href, title: [r.name, r.me ? 'you' : kindShort(r.kind), r.id].filter(Boolean).join(' · '), 'data-key': 'p:' + r.id },
    o.avatarCtx ? avatar(r.id, o.avatarCtx) : null,
    h('span', { class: 'tt', text: r.name }),
    r.me ? h('span', { class: 'kd', text: 'you' }) : h('span', { class: 'kd', text: kindShort(r.kind), title: r.kind ? kindTitle(r.kind) : null }));
  const pick = r.pick ? h('button', {
    type: 'button', class: 'sr-pick', 'data-key': 'k:' + r.id, text: 'Threads',
    title: 'Search threads with ' + r.name, 'aria-label': 'Search threads with ' + r.name,
    onclick: () => { o.box.pick(r.id); o.box.focus(); },
  }) : null;
  return h('div', { class: 'sr-peer' }, link, pick);
}
