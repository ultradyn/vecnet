// A persistent, keyed section: background reads update facts without replacing
// focused controls. Removal owns its exact ID, and confirmed IDs stay hidden
// even if an older in-flight snapshot lands after the write.
import { h, idCopy } from './dom.js';
import { ago, clean, fullWhen } from './format.js';
import { chip, expiresIn, subscriptionHost, subscriptionItems, subscriptionRemoveError } from './subs-model.js';

let sequence = 0;

// o: { api, id, now(), reload(), changed(removedId?) }. changed remembers a
// confirmed ID in shared views even after unmount; undefined only reconciles.
export function subscriptionsSection(o) {
  const uid = 'pvs-' + (++sequence);
  const count = h('span', { class: 'n' });
  const heading = h('h2', { id: uid, tabindex: '-1' }, 'Subscriptions ', count);
  const summary = h('p', { class: 'pv-sub-summary' });
  const retry = h('button', { type: 'button', class: 'btn sm', text: 'Retry', onclick: () => o.reload() });
  const readErrorText = h('span', { text: 'Subscriptions could not be refreshed. ' });
  const readError = h('div', { class: 'pv-sub-read-error', role: 'status', hidden: true },
    readErrorText, retry);
  const listEl = h('div', { class: 'pv-sub-list' });
  const status = h('p', { class: 'pv-sub-status', role: 'status', 'aria-live': 'polite', 'aria-atomic': 'true' });
  const el = h('section', { class: 'dash-sec pv-subs', id: 'pv-subs', 'aria-labelledby': uid },
    h('div', { class: 'pv-sub-heading' }, heading, h('span', { class: 'pv-sub-kind', text: 'Callback delivery' })),
    summary, readError, listEl, status);
  let raw, loadError, gone = false, modal = null;
  const rows = new Map(), removed = new Set();
  const items = () => subscriptionItems(raw, o.id).filter(s => !removed.has(s.sub_id));
  const text = (node, value) => { const s = clean(value); if (node.textContent !== s) node.textContent = s; };

  const displayed = () => Array.from(listEl.children, el => el.getAttribute('data-sub'));
  function neighbors(id, order = displayed()) {
    const at = order.indexOf(id);
    return at < 0 ? null : [...order.slice(at + 1), ...order.slice(0, at).reverse()];
  }

  function focusRow(id, adjacent = []) {
    if (gone) return;
    const next = rows.get(id) || adjacent.map(id => rows.get(id)).find(Boolean);
    (next ? next.remove : heading).focus({ preventScroll: true });
  }

  function newRow(s) {
    const name = h('h3', { class: 'pv-sub-name' });
    const health = h('span', { class: 'chip health' });
    const host = h('span', { class: 'pv-sub-host mono' });
    const note = h('p', { class: 'pv-sub-note' });
    const expiry = h('dd'), last = h('dd'), attempts = h('dd');
    const remove = h('button', { type: 'button', class: 'btn pv-sub-remove', text: 'Remove',
      'aria-label': 'Remove subscription ' + s.sub_id, onclick: () => openRemove(s.sub_id) });
    const row = h('article', { class: 'pv-sub-card', 'data-sub': s.sub_id, 'aria-label': 'Subscription ' + s.sub_id },
      h('div', { class: 'pv-sub-top' }, h('div', { class: 'pv-sub-title' }, name, host), health),
      note,
      h('dl', { class: 'pv-sub-facts' }, h('div', null, h('dt', { text: 'Expires' }), expiry),
        h('div', null, h('dt', { text: 'Last delivery' }), last), h('div', null, h('dt', { text: 'Attempts' }), attempts)),
      h('div', { class: 'pv-sub-footer' }, idCopy(s.sub_id, 'subscription id'), remove));
    return { el: row, name, health, host, note, expiry, last, attempts, remove };
  }

  function updateRow(r, s) {
    const c = chip(s.health);
    r.el.setAttribute('data-health', s.health || 'unknown');
    r.el.setAttribute('data-attention', String(s.attention));
    text(r.name, s.name || 'Event subscription');
    text(r.host, subscriptionHost(s.url));
    text(r.health, c.label);
    r.health.className = 'chip health h-' + c.tone;
    text(r.note, s.health_note || (s.health === 'ok' ? 'Receiving events normally.' : 'No further health details reported.'));
    text(r.expiry, expiresIn(s.expires_at, o.now()) || 'No expiry reported');
    r.expiry.title = fullWhen(s.expires_at);
    const http = Number.isInteger(s.last_status) ? 'HTTP ' + s.last_status : '';
    text(r.last, [s.last_delivery_at ? ago(s.last_delivery_at, o.now()) : 'Not yet delivered', http].filter(Boolean).join(' · '));
    r.last.title = fullWhen(s.last_delivery_at);
    text(r.attempts, String(s.attempts ?? 0));
  }

  function paint() {
    if (gone) return;
    const list = items();
    const active = document.activeElement;
    const ready = Array.isArray(raw);
    count.textContent = ready ? String(list.length) : '';
    const attention = list.filter(s => s.attention).length;
    text(summary, !ready ? (loadError ? 'Subscription status is unavailable.' : 'Loading subscriptions…')
      : !list.length ? 'No subscriptions. This peer has no callback subscriptions to manage.'
        : (attention ? attention + ' need' + (attention === 1 ? 's' : '') + ' attention. '
          : list.every(s => s.health === 'ok') ? 'All subscriptions are healthy. ' : 'Some subscription health is unknown. ')
          + 'Remove callbacks you no longer use.');
    summary.setAttribute('data-attention', String(attention > 0));
    readError.hidden = !loadError;
    if (loadError) text(readErrorText, ready ? 'Showing the last loaded subscriptions. Could not refresh. ' : 'Subscriptions could not be loaded. ');
    // Remove vanished rows with an explicit keyboard destination; normal
    // refreshes leave every surviving button and its focus intact.
    const before = displayed();
    if (modal) modal.adjacent = neighbors(modal.id, before) ?? modal.adjacent;
    let lost = null;
    const ids = new Set(list.map(s => s.sub_id));
    for (const [id, row] of rows) {
      if (!ids.has(id)) {
        if (row.el.contains(active)) lost = neighbors(id, before);
        row.el.remove(); rows.delete(id);
      }
    }
    for (const [index, s] of list.entries()) {
      let row = rows.get(s.sub_id);
      if (!row) { row = newRow(s); rows.set(s.sub_id, row); }
      updateRow(row, s);
      if (listEl.children[index] !== row.el) listEl.insertBefore(row.el, listEl.children[index] || null);
    }
    if (active && listEl.contains(active) && document.activeElement !== active) active.focus({ preventScroll: true });
    if (lost && !modal) focusRow('', lost);
    if (modal) modal.adjacent = neighbors(modal.id) ?? modal.adjacent;
    if (modal && ready && !list.some(s => s.sub_id === modal.id) && !modal.busy) {
      text(status, 'This subscription was removed elsewhere.');
      closeModal(true);
    }
  }

  function closeModal(back) {
    if (!modal) return;
    const m = modal;
    modal = null;
    if (m.el.open) m.el.close();
    m.el.remove();
    if (back) focusRow(m.id, m.adjacent);
  }

  function openRemove(id) {
    if (gone || modal) return;
    const list = items(), s = list.find(s => s.sub_id === id);
    if (!s) return;
    const label = uid + '-confirm';
    const detail = uid + '-impact';
    const error = h('p', { class: 'pv-sub-error', role: 'alert' });
    const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => { if (!modal?.busy) closeModal(true); } });
    const submit = h('button', { type: 'button', class: 'btn pv-sub-confirm-remove', text: 'Remove subscription', onclick: () => remove() });
    const c = chip(s.health);
    const dialog = h('dialog', { class: 'pv-sub-confirm', 'aria-labelledby': label, 'aria-describedby': detail },
      h('div', { class: 'pv-sub-confirm-body' },
        h('span', { class: 'pv-sub-eyebrow', text: 'Subscription settings' }),
        h('h2', { id: label, text: 'Remove subscription?' }),
        h('p', { class: 'pv-sub-confirm-lede', text: 'Stop sending events to this callback.' }),
        h('div', { class: 'pv-sub-target' },
          h('div', null, h('b', { text: subscriptionHost(s.url) }), h('span', { class: 'chip h-' + c.tone, text: c.label })),
          h('code', { text: s.sub_id }), h('p', { text: s.name || 'Event subscription' })),
        h('div', { class: 'pv-sub-impact', id: detail },
          h('p', { text: 'This removes the subscription and its queued callback attempts. A request already in flight may still finish.' }),
          h('p', { text: 'The peer, inbox messages and thread history stay available.' })),
        h('p', { class: 'pv-sub-resubscribe', text: 'An active client can subscribe again. To keep it removed, stop that client from subscribing.' }),
        error,
        h('div', { class: 'pv-sub-confirm-actions' }, cancel, submit)));
    modal = { id, adjacent: neighbors(id), el: dialog, error, cancel, submit, busy: false };
    dialog.addEventListener('cancel', e => { e.preventDefault(); if (!modal?.busy) closeModal(true); });
    dialog.addEventListener('close', () => { if (modal?.el === dialog) closeModal(true); });
    document.body.append(dialog);
    dialog.showModal();
    cancel.focus();
  }

  async function remove() {
    const m = modal;
    if (!m || m.busy || gone) return;
    m.busy = true;
    text(m.error, ''); text(m.submit, 'Removing…');
    m.submit.setAttribute('aria-disabled', 'true');
    m.cancel.setAttribute('aria-disabled', 'true');
    m.el.setAttribute('aria-busy', 'true');
    let absent = false;
    try { await o.api.removeSubscription(m.id); }
    catch (e) {
      if (e?.status === 404) absent = true;
      else {
        if (gone) { o.changed(); return; }
        m.busy = false;
        text(m.error, subscriptionRemoveError(e)); text(m.submit, 'Remove subscription');
        m.submit.removeAttribute('aria-disabled'); m.cancel.removeAttribute('aria-disabled');
        m.el.removeAttribute('aria-busy');
        return;
      }
    }
    removed.add(m.id);
    // Invalidate even after navigation, but never steal focus or write into
    // the page that replaced this one.
    if (!gone) {
      m.adjacent = neighbors(m.id) ?? m.adjacent;
      closeModal(false);
      text(status, absent ? 'Subscription already removed.' : 'Subscription removed.');
      paint();
      focusRow('', m.adjacent);
    }
    o.changed(m.id);
  }

  return { el,
    draw(list, error) { raw = list; loadError = error; paint(); },
    unmount() { gone = true; closeModal(false); rows.clear(); },
  };
}
