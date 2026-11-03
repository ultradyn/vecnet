// Reaction chips, the react button and its picker (PROTOCOL §17). The thread
// rebuilds every .msg on each update, so the picker is one node outside the
// message list and the open message lives here, not in the DOM.
import { h } from './dom.js';
import { prioritySeg } from './prio.js';
import { chips, errorText, nextOp, reactBody, REACTIONS } from './react-model.js';

const SAYS = { wake: 'wakes now', wake_idle: 'wakes if idle', hold: 'no wake' };
const FOCUSABLE = 'a,button,input,textarea,select,[tabindex]';

export function createReactions({ api, me, nameOf, onDone, toast }) {
  let open = '', cur = null, override = null, busy = false;
  const seg = prioritySeg(p => { override = override === p ? null : p; paint(); });
  const emo = REACTIONS.map(r => h('button', { type: 'button', class: 'rx-e', onclick: () => send(cur, r.emoji, true) }, r.emoji, h('i')));
  const picker = h('div', { class: 'rx-pick', role: 'dialog', 'aria-label': 'React', hidden: true },
    h('div', { class: 'rx-es' }, emo), seg.el);

  function paint() {
    REACTIONS.forEach((r, i) => {
      const b = emo[i], p = override || r.prio, mine = !!cur && nextOp(cur.reactions, me, r.emoji) === 'remove';
      b.dataset.p = p;
      b.disabled = busy;
      b.setAttribute('aria-pressed', mine);
      b.setAttribute('aria-label', r.name + ', ' + (mine ? 'remove yours' : SAYS[p]));
      b.title = b.getAttribute('aria-label');
    });
    for (const b of seg.seg.children) { b.className = b.value === override ? 'on' : ''; b.disabled = busy; }
  }

  // place pins the picker under the button (above it near the bottom), right
  // edges aligned, in the coordinates of the picker's positioned parent.
  function place(btn) {
    const box = picker.offsetParent;
    if (!box || !btn.isConnected) return;
    const a = btn.getBoundingClientRect(), r = box.getBoundingClientRect();
    let top = a.bottom - r.top + 6;
    if (top + picker.offsetHeight > box.clientHeight && a.top - r.top > picker.offsetHeight + 6) top = a.top - r.top - picker.offsetHeight - 6;
    picker.style.setProperty('--rx-top', top + 'px');
    picker.style.setProperty('--rx-right', Math.max(0, r.right - a.right) + 'px');
  }

  function show(m, btn) {
    close(false);
    open = m.message_id;
    cur = m;
    picker.hidden = false;
    btn.setAttribute('aria-expanded', 'true');
    paint();
    place(btn);
    emo[0].focus({ preventScroll: true });
  }

  // close hides the picker and, when refocus, returns focus to the react
  // button of the re-rendered message.
  function close(refocus = true) {
    if (!open) return;
    const b = document.getElementById('rx-btn-' + open);
    open = '';
    cur = null;
    override = null;
    picker.hidden = true;
    if (b) { b.setAttribute('aria-expanded', 'false'); if (refocus) b.focus({ preventScroll: true }); }
  }

  async function send(m, emoji, picked) {
    if (busy || !m) return;
    busy = true;
    paint();
    try {
      await api.react(reactBody({ messageId: m.message_id, emoji, op: nextOp(m.reactions, me, emoji), override: picked ? override : null }));
      if (picked && open === m.message_id) close();
      onDone();
    } catch (e) {
      if (e.status !== 401) toast(errorText(e, Date.now()), true);
    } finally {
      busy = false;
      paint();
    }
  }

  // keep: a rebuild that detaches the focused chip or button focuses its
  // last replacement (same data-rx) on the next frame, after every rebuild in
  // it, instead of dropping focus to the body.
  let want = '', wantEl = null;
  function keep(b) {
    const f = document.activeElement, k = b.dataset.rx;
    if (f && f.dataset && f.dataset.rx === k) {
      if (!want) requestAnimationFrame(() => {
        const a = document.activeElement;
        if (wantEl && wantEl.isConnected && (!a || a === document.body)) wantEl.focus({ preventScroll: true });
        want = '';
        wantEl = null;
      });
      want = k;
    }
    if (want === k) wantEl = b;
    return b;
  }

  const onKey = e => { if (open && e.key === 'Escape') { e.preventDefault(); close(); } };
  const onClick = e => {
    const t = e.target;
    if (!open || picker.contains(t) || (t.closest && t.closest('.rx-btn'))) return;
    close(!(t.closest && t.closest(FOCUSABLE)));
  };
  document.addEventListener('keydown', onKey);
  document.addEventListener('click', onClick);

  return {
    picker,
    close,
    chipsRow(m) {
      const list = chips(m.reactions, me, nameOf);
      if (!list.length) return null;
      return h('div', { class: 'rx-row' }, list.map(c => keep(h('button', {
        type: 'button', class: 'rx-chip' + (c.mine ? ' mine' : ''), 'aria-pressed': c.mine ? 'true' : 'false', title: c.title,
        'data-rx': m.message_id + c.emoji, onclick: () => send(m, c.emoji, false),
      }, c.emoji, h('b', { text: String(c.count) })))));
    },
    button(m) {
      const id = m.message_id, on = open === id;
      const b = h('button', {
        type: 'button', class: 'rx-btn', id: 'rx-btn-' + id, 'data-rx': id, text: 'react', title: 'React',
        'aria-haspopup': 'dialog', 'aria-expanded': on ? 'true' : 'false',
        onclick: () => (open === id ? close() : show(m, b)),
      });
      if (on) {
        cur = m; // fresh reactions for the open picker
        paint();
        queueMicrotask(() => place(b));
      }
      return keep(b);
    },
    destroy() {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('click', onClick);
      picker.remove();
    },
  };
}
