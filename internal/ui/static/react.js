// Reaction chips, the react button and picker (PROTOCOL §17). Each update
// rebuilds every .msg, so the picker is one node outside the list and the
// open message lives here.
import { h } from './dom.js';
import { markPriority, prioritySeg } from './prio.js';
import { chips, errorText, nextOp, reactBody, REACTIONS } from './react-model.js';

const SAYS = { wake: 'wakes now', wake_idle: 'wakes if idle', hold: 'no wake' };
// main takes focus only from script (the shell's route handoff): not a control.
const FOCUSABLE = 'a,button,input,textarea,select,[tabindex]:not(main)';

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
    markPriority(seg.seg, override);
    for (const b of seg.seg.children) b.disabled = busy;
  }

  // place pins the picker under the button (above it near the bottom), left
  // edges aligned while it fits.
  function place(btn) {
    const box = picker.offsetParent;
    if (!box || !btn.isConnected) return;
    const a = btn.getBoundingClientRect(), r = box.getBoundingClientRect();
    let top = a.bottom - r.top + 6;
    if (top + picker.offsetHeight > box.clientHeight && a.top - r.top > picker.offsetHeight + 6) top = a.top - r.top - picker.offsetHeight - 6;
    picker.style.setProperty('--rx-top', top + 'px');
    picker.style.setProperty('--rx-left', Math.max(0, Math.min(a.left - r.left, box.clientWidth - picker.offsetWidth)) + 'px');
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

  // close hides the picker; refocus focuses the rebuilt react button.
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

  // keep: when a rebuild detaches the focused chip or button, its last
  // replacement (same data-rx) takes focus on the next frame.
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
  // An outside click refocuses the react button; a click on a control keeps
  // its focus.
  const onClick = e => {
    const t = e.target;
    if (!open || picker.contains(t) || (t.closest && t.closest('.rx-btn'))) return;
    close(!(t.closest && t.closest(FOCUSABLE)));
  };
  document.addEventListener('keydown', onKey);
  document.addEventListener('click', onClick);
  // Tabbing out closes it in place.
  picker.addEventListener('focusout', e => {
    const t = e.relatedTarget;
    if (open && !busy && t && t.matches(FOCUSABLE) && !picker.contains(t) && !t.closest('.rx-btn')) close(false);
  });

  function button(m) {
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
  }

  return {
    picker,
    close,
    button,
    // Every message has a row, react button first: chips never change its
    // height.
    chipsRow(m) {
      return h('div', { class: 'rx-row' }, button(m), chips(m.reactions, me, nameOf).map(c => keep(h('button', {
        type: 'button', class: 'rx-chip' + (c.mine ? ' mine' : ''), 'aria-pressed': c.mine ? 'true' : 'false', title: c.title,
        'data-rx': m.message_id + c.emoji, onclick: () => send(m, c.emoji, false),
      }, c.emoji, h('b', { text: String(c.count) })))));
    },
    destroy() {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('click', onClick);
      picker.remove();
    },
  };
}
