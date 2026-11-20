// Hub update indicator in the sidebar foot (#hub-update). It shows only when
// the hub reports a newer vecnet on npm; on a local npm install it offers a
// confirmed one-click update, otherwise how to update by hand. Text goes in
// through dom.h only, so as textContent.
import { confirmButton, copyButton, h } from './dom.js';
import { buttonLabel, pollDelay, trackUpdate } from './update-model.js';

// renderUpdate draws v (updateView's result) into root; null hides it.
// The button is kept across redraws of the same state so a first (arming)
// click survives a poll.
export function renderUpdate(root, v, { onUpdate = () => {} } = {}) {
  const st = root.__upd ??= {};
  if (!v) { st.sig = ''; root.replaceChildren(); root.hidden = true; return; }
  const sig = JSON.stringify(v);
  if (st.sig === sig) return;
  st.sig = sig;
  const kids = [
    h('div', { class: 'uh' }, h('i', { 'aria-hidden': 'true' }), h('b', { text: v.title })),
    v.sub ? h('div', { class: 'us', text: v.sub }) : null,
  ].filter(Boolean);
  if (v.release) kids.push(h('a', { class: 'ul', href: v.release, target: '_blank', rel: 'noopener noreferrer', text: 'Release notes' }));
  if (v.progress) kids.push(h('div', { class: 'up', role: 'status', text: v.progress }));
  if (v.note) kids.push(h('div', { class: 'up', text: v.note }));
  if (v.error) kids.push(h('div', { class: 'ue', role: 'alert', text: v.error }));
  if (v.oneClick && !v.busy && !v.note) {
    const label = buttonLabel(v);
    const btn = st.btn && st.btnLabel === label ? st.btn : confirmButton(label, 'Confirm ' + label.toLowerCase(), () => onUpdate());
    btn.title = v.restart ? 'Install ' + v.latest + ' from npm, then restart the hub' : 'Install ' + v.latest + ' from npm';
    st.btn = btn; st.btnLabel = label;
    kids.push(btn);
  } else if (!v.oneClick && v.manual) {
    kids.push(h('details', { class: 'um' },
      h('summary', { text: 'How to update' }),
      v.reason ? h('div', { text: v.reason }) : null,
      h('div', { class: 'uc' }, h('code', { text: v.manual }), copyButton(v.manual, 'command')),
      h('div', { text: 'Then restart vecnet-hub.' })));
  }
  root.replaceChildren(...kids);
  root.hidden = false;
}

// createUpdate polls GET /v1/admin/update and draws the indicator. Errors
// (a hub without the route, a network blip) hide it silently.
export function createUpdate({ api, root, toast = () => {}, setTimer = setTimeout, clearTimer = clearTimeout, now = Date.now }) {
  let timer = 0, closed = false, view = null, loading = null, pending = null;
  const draw = () => renderUpdate(root, view, { onUpdate: start });
  // One read at a time, so overlapping callers never leave two poll timers.
  function load() {
    clearTimer(timer);
    if (closed) return Promise.resolve();
    return loading ??= (async () => {
      let st = null;
      try { st = await api.updateStatus(); } catch { /* draining, restarting, or no route */ }
      loading = null;
      if (closed) return;
      ({ view, pending } = trackUpdate(pending, st, now()));
      draw();
      clearTimer(timer);
      timer = setTimer(load, pollDelay(view, !!pending));
    })();
  }
  async function start() {
    try {
      await api.startUpdate();
      toast(view && view.restart ? 'Updating; the hub will restart when the install finishes.' : 'Installing the update…');
    } catch (e) {
      toast('Update not started: ' + ((e && e.message) || 'error'), true);
    }
    load();
  }
  load();
  return {
    reload: load,
    close() { closed = true; clearTimer(timer); root.replaceChildren(); root.hidden = true; },
  };
}
