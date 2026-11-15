// The peer page's Routes section (#peer/<id>): the push routes that reach
// this peer, and the operator's controls to set them up or fix them. A Grok
// Bot that enrolled without its routine URL and key gets a callout and the
// Add route form opened on the Grok Bot kind.
//
// Writes: POST /v1/admin/routes (add), PATCH /v1/admin/routes/{id} (edit
// the URL; a Grok Bot's key), POST …/verify, POST …/rotate (a new signing
// secret) and DELETE …/{id}. A route's URL is never read back (the hub
// lists its host only) and a key is never shown: form fields are cleared
// once sent. A new whsec_ is shown once, in the section, until Done, its
// route goes away or the page is left; it lives only in that text and its
// copy closure. Error copy is fixed sentences (routeError), never the hub's.
//
// Focus: while a write is in flight its controls are aria-disabled, not
// disabled, so the focused one keeps focus; after any redraw focus returns
// to the same control in the same row, else the row's Edit, Add route or
// the heading. The table is rebuilt only when what it shows changes, so a
// reload or an unrelated notify never disarms a Confirm button.
import { confirmButton, copyText, h, idCopy, toast } from './dom.js';
import { ROUTE_KINDS, routeError, routeForm, routeSetup } from './peer-model.js';

const KIND_LABEL = { http: 'Webhook (signed)', grokbot: 'Grok Bot routine' };
const URL_HINT = {
  http: 'https://… — the receiver must answer a verification challenge before pushes start.',
  grokbot: 'https://api2.cursor.sh/automations/webhook/… from the routine\'s webhook trigger.',
};
const BUSY = 'aria-disabled';

let seq = 0;

// routesSection returns { el, draw(facts, routes, error), openAdd(),
// unmount }. o: { api, id, time(iso), reload() }.
export function routesSection(o) {
  const uid = 'pvr' + (++seq);
  const count = h('span', { class: 'n' });
  const heading = h('h2', { tabindex: '-1' }, 'Routes ', count);
  const callout = h('div', { class: 'pv-rt-missing', role: 'note', hidden: true });
  const body = h('div', { class: 'body' });
  const msg = h('p', { class: 'pv-rt-msg', role: 'status' });
  const err = h('p', { class: 'err', role: 'alert' });
  const fresh = h('div', { class: 'pv-rt-fresh', hidden: true });
  const addBtn = h('button', { type: 'button', class: 'btn pv-rt-add', text: 'Add route', onclick: () => { if (!busy) openForm(null); } });
  const addRow = h('div', { class: 'pv-rt-acts' }, addBtn);
  const formBox = h('div', { class: 'pv-rt-form', hidden: true });
  const el = h('section', { class: 'dash-sec pv-routes', id: 'pv-routes' }, heading, callout, body, addRow, formBox, msg, err, fresh);

  let facts = null, routes = null, loadErr = null, setup = { editable: false, missing: false, suggest: 'http', canAdd: false };
  let editing = undefined; // undefined: no form; null: Add; else the route being edited
  let busy = '', gone = false, tableKey = null, secretRoute = '';
  let want = null;         // focus owed to a row not drawn yet (a new route)
  let formErr = null;      // the open form's error line, while it is open

  // Messages go to the open form's error line when the form asked, else
  // under the section.
  function say(text) { msg.textContent = text || ''; err.textContent = ''; }
  function fail(text) {
    msg.textContent = '';
    if (formErr && formErr.isConnected) { formErr.textContent = text || ''; err.textContent = ''; }
    else err.textContent = text || '';
  }

  // refocus puts focus back after a redraw: the same control in the same
  // row, else that row's Edit, else Add route, else the heading.
  function refocus(where) {
    if (gone) return;
    const row = where && where.route ? [...body.querySelectorAll('tr[data-route]')].find(tr => tr.dataset.route === where.route) : null;
    const pick = (row && where.cls && row.querySelector('.' + where.cls)) || (row && row.querySelector('.pv-rt-edit'))
      || (!addRow.hidden && addBtn) || body.querySelector('.pv-rt-edit') || heading;
    pick.focus({ preventScroll: true });
  }
  // place is where focus is now, as refocus takes it; null if outside.
  function place() {
    const a = document.activeElement;
    if (!a || !el.contains(a)) return null;
    const tr = a.closest && a.closest('tr[data-route]');
    return { route: tr ? tr.dataset.route : '', cls: [...(a.classList || [])].find(c => c.startsWith('pv-rt-')) || '' };
  }

  // A secret shown once: the warning, the value, Copy and Done.
  function showSecret(whsec, lede, routeId) {
    secretRoute = routeId || '';
    const val = h('code', { class: 'pv-rt-secret mono', tabindex: '0', 'aria-label': 'Signing secret' });
    val.textContent = whsec;
    const copy = h('button', { type: 'button', class: 'btn pri', text: 'Copy secret', onclick: async () => {
      if (await copyText(whsec)) { copy.textContent = 'Copied'; setTimeout(() => { copy.textContent = 'Copy secret'; }, 1500); }
      else toast('Could not copy. Select the secret and copy it instead.', true);
    } });
    fresh.replaceChildren(
      h('p', { class: 'pv-rt-warn', text: lede }),
      val,
      h('p', { class: 'note', text: 'Shown once. The receiver checks each push\'s signature with it. Then press Verify so pushes start.' }),
      h('div', { class: 'pv-rt-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => closeSecret(true) }), copy));
    fresh.hidden = false;
    copy.focus();
  }
  function closeSecret(back) {
    const had = !fresh.hidden;
    fresh.replaceChildren();
    fresh.hidden = true;
    secretRoute = '';
    if (had && back) refocus({ route: '', cls: '' });
  }

  // run performs one write; errors become fixed sentences. It answers the
  // call's result (true for a 204), false on failure, or 'busy' when
  // another write is in flight (nothing sent).
  async function run(label, fallback, fn, opts) {
    if (busy) return 'busy';
    const at = place();
    busy = label; err.textContent = ''; msg.textContent = '';
    paint(); refocus(at);
    try {
      const r = await fn();
      if (gone) return false;
      busy = '';
      paint(); refocus(place() || at);
      o.reload();
      return r ?? true; // a 204 answers null
    } catch (x) {
      if (gone) return false;
      busy = '';
      fail(routeError(fallback, x, opts));
      paint(); refocus(place() || at);
      o.reload();
      return false;
    }
  }

  function openForm(route) {
    editing = route;
    say('');
    paint();
    const u = formBox.querySelector('input[type=url]');
    if (u) u.focus();
  }
  function closeForm(back, where) {
    editing = undefined;
    formErr = null;
    formBox.replaceChildren();
    formBox.hidden = true;
    paint();
    if (back) refocus(where || null);
  }

  function drawForm() {
    const route = editing;
    const isEdit = !!route;
    let kind = isEdit ? route.kind : setup.suggest;
    const kindSel = h('select', { id: uid + '-kind', disabled: isEdit || null },
      ROUTE_KINDS.map(k => h('option', { value: k, text: KIND_LABEL[k], selected: k === kind })));
    const url = h('input', { id: uid + '-url', type: 'url', class: 'mono', autocomplete: 'off', spellcheck: 'false',
      placeholder: isEdit ? 'Leave blank to keep the current URL' : 'https://…' });
    const key = h('input', { id: uid + '-key', type: 'password', class: 'mono', autocomplete: 'off', spellcheck: 'false',
      placeholder: isEdit ? 'Leave blank to keep the current key' : 'crsr_…' });
    const hint = h('p', { class: 'note pv-rt-hint' });
    const keyRow = h('div', { class: 'pv-rt-row' }, h('label', { for: uid + '-key', text: 'Routine key' }), key);
    const ferr = h('p', { class: 'err', role: 'alert' });
    const save = h('button', { type: 'submit', class: 'btn pri pv-rt-save', text: isEdit ? 'Save' : 'Add route' });
    const where = isEdit ? { route: route.id, cls: 'pv-rt-edit' } : null;
    const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => { if (!busy) closeForm(true, where); } });
    const sync = () => {
      kind = kindSel.value;
      keyRow.hidden = kind !== 'grokbot';
      hint.textContent = (isEdit && kind === 'http' ? 'A new URL stops pushes until it is verified again. ' : '') + URL_HINT[kind];
    };
    kindSel.addEventListener('change', sync);
    const form = h('form', { class: 'pv-rt-f', 'aria-label': isEdit ? 'Edit route' : 'Add route', novalidate: true, onsubmit: async e => {
      e.preventDefault();
      ferr.textContent = '';
      if (busy) { ferr.textContent = 'Wait for the current change to finish, then try again.'; return; }
      const f = routeForm({ kind, url: url.value, key: key.value, editing: isEdit });
      if (f.error) {
        ferr.textContent = f.error;
        (f.field === 'key' ? key : url).focus();
        return;
      }
      const sent = f.body;
      // The key leaves the field as the request goes out; never shown again.
      const ok = await run(isEdit ? 'edit' : 'add', isEdit ? 'Could not update the route.' : 'Could not add the route.', () => {
        key.value = '';
        return isEdit ? o.api.updateRoute(route.id, sent) : o.api.addRoute({ owner: o.id, kind, ...sent });
      });
      if (gone || ok === 'busy') return;
      if (!ok) {
        // The route went away meanwhile: the form closed; say so below it.
        if (!form.isConnected && formErr !== ferr) err.textContent = ferr.textContent || routeError('Could not update the route.', { code: 'not_found' });
        return;
      }
      url.value = '';
      const added = !isEdit && ok && typeof ok === 'object' ? ok.route_id : '';
      closeForm(false);
      if (isEdit) {
        say(kind === 'http' && sent.url ? 'Route updated. Press Verify so pushes start again.' : 'Route updated.');
        refocus(where);
      } else if (ok.whsec) {
        say('');
        showSecret(ok.whsec, 'Route added. Give the receiver this signing secret:', added);
      } else {
        say(kind === 'grokbot' ? 'Grok Bot route added. Queued deliveries push now.' : 'Route added.');
        // Its row lands with the reload: focus moves there when it does.
        want = added ? { route: added, cls: 'pv-rt-edit' } : null;
        refocus(want);
      }
    } },
    h('div', { class: 'pv-rt-row' }, h('label', { for: uid + '-kind', text: 'Kind' }), kindSel),
    h('div', { class: 'pv-rt-row' }, h('label', { for: uid + '-url', text: isEdit ? 'New URL' : 'URL' }), url),
    keyRow, hint, ferr,
    h('div', { class: 'pv-rt-acts' }, cancel, save));
    sync();
    formErr = ferr;
    formBox.replaceChildren(h('h3', { text: isEdit ? 'Edit route' : 'Add a push route' }), form);
    formBox.hidden = false;
  }

  // busyAware: a click while a write is in flight does nothing; the control
  // is aria-disabled so it keeps focus and still reads as unavailable.
  const guard = fn => () => { if (!busy) fn(); };

  function rowActions(r) {
    if (!setup.editable || r.via !== 'peer') return h('td', { class: 'sm muted', text: r.via === 'peer' ? '' : 'on the principal' });
    const out = [h('button', { type: 'button', class: 'btn sm pv-rt-edit', text: 'Edit', onclick: guard(() => openForm(r)) })];
    if (r.kind === 'http') {
      out.push(h('button', { type: 'button', class: 'btn sm pv-rt-verify', text: r.verified ? 'Re-verify' : 'Verify', onclick: guard(async () => {
        if (await run('verify', 'Could not verify the route.', () => o.api.verifyRoute(r.id), { verifying: true }) === true) say('Verified: pushes go through this route.');
      }) }));
      const rot = confirmButton('New secret', 'Confirm new secret', async () => {
        const res = await run('rotate', 'Could not issue a new secret.', () => o.api.rotateRoute(r.id));
        if (res && typeof res === 'object' && res.whsec) showSecret(res.whsec, 'New signing secret. The old one keeps working for 24 hours:', r.id);
      });
      rot.classList.add('sm', 'pv-rt-rotate');
      out.push(rot);
    }
    const rm = confirmButton('Remove', 'Confirm remove', async () => {
      if (await run('remove', 'Could not remove the route.', () => o.api.removeRoute(r.id)) === true) {
        if (secretRoute === r.id) closeSecret(false);
        say('Route removed. Its pending deliveries wait to be pulled.');
        refocus(null);
      }
    });
    rm.classList.add('sm', 'bad', 'pv-rt-remove');
    out.push(rm);
    return h('td', { class: 'pv-rt-cell' }, h('div', { class: 'pv-rt-btns' }, out));
  }

  function drawTable() {
    const list = routes || [];
    const kids = [];
    if (loadErr) kids.push(h('div', { class: 'empty error' }, h('b', { text: 'Routes unavailable' }), (loadErr && loadErr.message) || String(loadErr)));
    if (!routes && !loadErr) kids.push(h('div', { class: 'loading', text: 'Loading…' }));
    if (routes) {
      if (!list.length) kids.push(h('p', { class: 'none', text: setup.missing ? 'No push routes.' : 'No push routes. This peer pulls its deliveries.' }));
      else {
        kids.push(h('table', { class: 'pv-rt-table' },
          h('thead', null, h('tr', null, ['Route', 'Owner', 'Kind', 'Host', 'Verified', 'Created', ''].map(t => h('th', { text: t })))),
          h('tbody', null, list.map(r => h('tr', { 'data-route': r.id },
            h('td', null, idCopy(r.id, 'route id')),
            h('td', { class: 'sm nw', text: r.via === 'peer' ? 'this peer' : 'its principal' }),
            h('td', { class: 'mono sm nw', text: r.kind + (r.kind === 'grokbot' && r.runsPerHour ? ' · ' + r.runsPerHour + '/h' : '') }),
            h('td', { class: 'mono sm', text: r.host || '—' }),
            h('td', { class: 'nw' }, r.verified ? o.time(r.verified) : h('span', { class: 'bad', text: 'unverified' })),
            h('td', { class: 'nw' }, o.time(r.created)),
            rowActions(r))))));
      }
    }
    body.replaceChildren(...kids);
  }

  // paint brings the section up to date. The table is rebuilt only when
  // what it shows changed; busy only flips aria-disabled on what is there.
  function paint() {
    const list = routes || [];
    count.textContent = routes ? String(list.length) : '';
    callout.hidden = !setup.missing;
    if (setup.missing && !callout.firstChild) {
      callout.replaceChildren(
        h('b', { text: 'No Grok Bot routine is set up.' }),
        ' Messages to this bot wait undelivered: a routine cannot pull them. Add its routine webhook URL and key (from the routine\'s webhook trigger) so the hub can push.');
    }
    const key = JSON.stringify([routes, loadErr ? String(loadErr.message || loadErr) : '', setup.editable, setup.missing]);
    if (key !== tableKey) {
      const at = place();
      tableKey = key;
      drawTable();
      const owed = want && body.querySelector(`tr[data-route="${CSS.escape(want.route)}"]`);
      // Focus owed to a new row moves there only if it still sits where
      // refocus left it (the user has not moved on).
      if (owed && (!at || at.route === '' )) { const w = want; want = null; refocus(w); }
      else if (at && !el.contains(document.activeElement)) refocus(at);
    }
    for (const b of el.querySelectorAll('.pv-rt-btns button, .pv-rt-add, .pv-rt-save')) {
      if (busy) b.setAttribute(BUSY, 'true'); else b.removeAttribute(BUSY);
    }
    // The hub pushes through one route per peer, so a second route of the
    // peer's own would quietly take over: Add is offered only while it has
    // none (or a Grok Bot still lacks its routine); otherwise Edit.
    addBtn.hidden = !setup.canAdd || editing !== undefined;
    addRow.hidden = addBtn.hidden;
    addBtn.textContent = setup.missing ? 'Set up Grok Bot routine' : 'Add route';
    if (setup.missing) addBtn.classList.add('pri'); else addBtn.classList.remove('pri');
    if (editing !== undefined && setup.editable) {
      if (formBox.hidden) drawForm(); // kept as typed across redraws
    } else if (!formBox.hidden) {
      formErr = null;
      formBox.replaceChildren();
      formBox.hidden = true;
    }
  }

  return {
    el,
    // draw(facts, routes, error): routes is peerRoutes' list, or null before
    // it loads; error is the routes read's error, if any.
    draw(f, list, error) {
      facts = f;
      routes = list;
      loadErr = error || null;
      setup = routeSetup(facts, routes);
      // An edited route that went away closes its form; an Add form closes
      // once Add is no longer offered (a route appeared elsewhere), so it
      // cannot quietly take over that route.
      if (routes && editing && !routes.some(r => r.id === editing.id)) {
        closeForm(false);
        fail('That route was removed elsewhere.');
      } else if (routes && editing === null && !setup.canAdd) {
        closeForm(false);
        say('A route was added elsewhere. Edit it instead.');
      }
      // A shown secret whose route is gone goes too.
      if (secretRoute && routes && !routes.some(r => r.id === secretRoute)) closeSecret(false);
      paint();
    },
    // openAdd opens Add route (the header's "set up" link) and brings it
    // into view; with a form already open it brings that form into view.
    openAdd() {
      if (!setup.editable) return;
      if (editing === undefined && setup.canAdd && !busy) openForm(null);
      el.scrollIntoView({ block: 'start' });
      const u = formBox.querySelector('input[type=url]');
      if (u) u.focus({ preventScroll: true });
    },
    unmount() { gone = true; closeSecret(false); },
  };
}
