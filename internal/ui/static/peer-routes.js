// The peer page's Routes section (#peer/<id>): the push routes that reach
// this peer, and the operator's controls to set them up or fix them. A Grok
// Bot that enrolled without its routine URL and key gets a callout and the
// Add route form opened on the Grok Bot kind.
//
// Writes: POST /v1/admin/routes (add), PATCH /v1/admin/routes/{id} (edit
// the URL; a Grok Bot's key), POST …/verify, POST …/rotate (a new signing
// secret) and DELETE …/{id}. A route's URL is never read back (the hub
// lists its host only) and a key is never shown: form fields are cleared
// once sent. A new whsec_ is shown once, in the section, until Done or the
// page is left; it lives only in that text and its copy closure. Error copy
// is fixed sentences (routeError), never the hub's message.
import { confirmButton, copyText, h, idCopy, toast } from './dom.js';
import { ROUTE_KINDS, routeError, routeForm, routeSetup } from './peer-model.js';

const KIND_LABEL = { http: 'Webhook (signed)', grokbot: 'Grok Bot routine' };
const URL_HINT = {
  http: 'https://… — the receiver must answer a verification challenge before pushes start.',
  grokbot: 'https://api2.cursor.sh/automations/webhook/… from the routine\'s webhook trigger.',
};

let seq = 0;

// routesSection returns { el, draw(facts, routes, error), openAdd(),
// unmount }. o: { api, id, time(iso), reload() }.
export function routesSection(o) {
  const uid = 'pvr' + (++seq);
  const count = h('span', { class: 'n' });
  const callout = h('div', { class: 'pv-rt-missing', role: 'note', hidden: true });
  const body = h('div', { class: 'body' });
  const msg = h('p', { class: 'pv-rt-msg', role: 'status' });
  const err = h('p', { class: 'err', role: 'alert' });
  const fresh = h('div', { class: 'pv-rt-fresh', hidden: true });
  const addBtn = h('button', { type: 'button', class: 'btn pv-rt-add', text: 'Add route', onclick: () => openForm(null) });
  const addRow = h('div', { class: 'pv-rt-acts' }, addBtn);
  const formBox = h('div', { class: 'pv-rt-form', hidden: true });
  const el = h('section', { class: 'dash-sec pv-routes', id: 'pv-routes' },
    h('h2', null, 'Routes ', count), callout, body, addRow, formBox, msg, err, fresh);

  let facts = null, routes = null, loadErr = null, setup = { editable: false, missing: false, suggest: 'http' };
  let editing = undefined; // undefined: no form; null: Add; else the route being edited
  let busy = '', gone = false, lastKey = '';

  function say(text) { msg.textContent = text || ''; err.textContent = ''; }
  function fail(text) { err.textContent = text || ''; msg.textContent = ''; }

  // A secret shown once: the warning, the value, Copy and Done.
  function showSecret(whsec, lede) {
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
  function closeSecret(refocus) {
    const had = !fresh.hidden;
    fresh.replaceChildren();
    fresh.hidden = true;
    if (had && refocus) addBtn.focus({ preventScroll: true });
  }

  // run performs one write; errors become fixed sentences.
  async function run(label, fallback, fn, opts) {
    if (busy) return false;
    busy = label; err.textContent = ''; msg.textContent = '';
    paint();
    try {
      const r = await fn();
      if (gone) return false;
      busy = '';
      paint();
      o.reload();
      return r ?? true; // a 204 answers null
    } catch (x) {
      if (gone) return false;
      busy = '';
      fail(routeError(fallback, x, opts));
      paint();
      o.reload();
      return false;
    }
  }

  function openForm(route) {
    editing = route;
    say('');
    paint(true);
  }
  function closeForm(refocus) {
    editing = undefined;
    formBox.replaceChildren();
    formBox.hidden = true;
    paint();
    if (refocus) addBtn.focus({ preventScroll: true });
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
    const save = h('button', { type: 'submit', class: 'btn pri', text: isEdit ? 'Save' : 'Add route' });
    const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => closeForm(true) });
    const sync = () => {
      kind = kindSel.value;
      keyRow.hidden = kind !== 'grokbot';
      hint.textContent = (isEdit && kind === 'http' ? 'A new URL stops pushes until it is verified again. ' : '') + URL_HINT[kind];
    };
    kindSel.addEventListener('change', sync);
    const form = h('form', { class: 'pv-rt-f', 'aria-label': isEdit ? 'Edit route' : 'Add route', novalidate: true, onsubmit: async e => {
      e.preventDefault();
      ferr.textContent = '';
      const f = routeForm({ kind, url: url.value, key: key.value, editing: isEdit });
      if (f.error) {
        ferr.textContent = f.error;
        (f.field === 'key' ? key : url).focus();
        return;
      }
      save.disabled = cancel.disabled = true;
      const body = f.body;
      // The key leaves the field as it is sent; it is never shown again.
      key.value = '';
      const ok = await run(isEdit ? 'edit' : 'add', isEdit ? 'Could not update the route.' : 'Could not add the route.', async () => {
        if (isEdit) {
          await o.api.updateRoute(route.id, body);
          return { edited: true };
        }
        return o.api.addRoute({ owner: o.id, kind, ...body });
      });
      if (gone) return;
      save.disabled = cancel.disabled = false;
      if (!ok) {
        ferr.textContent = err.textContent;
        err.textContent = '';
        return;
      }
      url.value = '';
      closeForm(false);
      if (isEdit) {
        say(kind === 'http' && body.url ? 'Route updated. Press Verify so pushes start again.' : 'Route updated.');
      } else if (ok.whsec) {
        say('');
        showSecret(ok.whsec, 'Route added. Give the receiver this signing secret:');
      } else {
        say(kind === 'grokbot' ? 'Grok Bot route added. Queued deliveries push now.' : 'Route added.');
        addBtn.focus({ preventScroll: true });
      }
    } },
    h('div', { class: 'pv-rt-row' }, h('label', { for: uid + '-kind', text: 'Kind' }), kindSel),
    h('div', { class: 'pv-rt-row' }, h('label', { for: uid + '-url', text: isEdit ? 'New URL' : 'URL' }), url),
    keyRow, hint, ferr,
    h('div', { class: 'pv-rt-acts' }, cancel, save));
    sync();
    formBox.replaceChildren(h('h3', { text: isEdit ? 'Edit route' : 'Add a push route' }), form);
    formBox.hidden = false;
    return url;
  }

  function rowActions(r) {
    if (!setup.editable || r.via !== 'peer') return h('td', { class: 'sm muted', text: r.via === 'peer' ? '' : 'on the principal' });
    const edit = h('button', { type: 'button', class: 'btn sm pv-rt-edit', text: 'Edit', onclick: () => openForm(r) });
    const out = [edit];
    if (r.kind === 'http') {
      out.push(h('button', { type: 'button', class: 'btn sm pv-rt-verify', text: r.verified ? 'Re-verify' : 'Verify', onclick: async () => {
        if (await run('verify', 'Could not verify the route.', () => o.api.verifyRoute(r.id), { verifying: true })) say('Verified: pushes go through this route.');
      } }));
      const rot = confirmButton('New secret', 'Confirm new secret', async () => {
        const res = await run('rotate', 'Could not issue a new secret.', () => o.api.rotateRoute(r.id));
        if (res && res.whsec) showSecret(res.whsec, 'New signing secret. The old one keeps working for 24 hours:');
      });
      rot.classList.add('sm', 'pv-rt-rotate');
      out.push(rot);
    }
    const rm = confirmButton('Remove', 'Confirm remove', async () => {
      if (await run('remove', 'Could not remove the route.', () => o.api.removeRoute(r.id))) say('Route removed. Its pending deliveries wait to be pulled.');
    });
    rm.classList.add('sm', 'bad', 'pv-rt-remove');
    out.push(rm);
    for (const b of out) b.disabled = !!busy;
    return h('td', { class: 'pv-rt-btns' }, out);
  }

  function paint(focusForm) {
    // Rows are rebuilt; a focused row button gets its replacement's focus.
    const a = document.activeElement;
    const had = a && body.contains(a) && a.closest('tr[data-route]') ? [a.closest('tr[data-route]').dataset.route, [...a.classList].find(c => c.startsWith('pv-rt-'))] : null;
    const list = routes || [];
    count.textContent = routes ? String(list.length) : '';
    // The hub pushes through one route per peer, so a second route of the
    // peer's own would quietly take over: Add is offered only while it has
    // none (or a Grok Bot still lacks its routine); otherwise Edit.
    callout.hidden = !setup.missing;
    if (setup.missing) {
      callout.replaceChildren(
        h('b', { text: 'No Grok Bot routine is set up.' }),
        ' Messages to this bot wait undelivered: a routine cannot pull them. Add its routine webhook URL and key (from the routine\'s webhook trigger) so the hub can push.');
    }
    const kids = [];
    if (loadErr) kids.push(h('div', { class: 'empty error' }, h('b', { text: 'Routes unavailable' }), (loadErr && loadErr.message) || String(loadErr)));
    if (!routes && !loadErr) kids.push(h('div', { class: 'loading', text: 'Loading…' }));
    if (routes) {
      if (!list.length) kids.push(h('p', { class: 'none', text: setup.missing ? 'No push routes.' : 'No push routes. This peer pulls its deliveries.' }));
      else {
        kids.push(h('table', null,
          h('thead', null, h('tr', null, ['Route', 'Owner', 'Kind', 'Host', 'Verified', 'Created', ''].map(t => h('th', { text: t })))),
          h('tbody', null, list.map(r => h('tr', { 'data-route': r.id },
            h('td', null, idCopy(r.id, 'route id')),
            h('td', { class: 'sm', text: r.via === 'peer' ? 'this peer' : 'its principal' }),
            h('td', { class: 'mono sm', text: r.kind + (r.kind === 'grokbot' && r.runsPerHour ? ' · ' + r.runsPerHour + '/h' : '') }),
            h('td', { class: 'mono sm', text: r.host || '—' }),
            h('td', null, r.verified ? o.time(r.verified) : h('span', { class: 'bad', text: 'unverified' })),
            h('td', null, o.time(r.created)),
            rowActions(r))))));
      }
    }
    body.replaceChildren(...kids);
    if (had && had[1]) {
      const next = [...body.querySelectorAll('tr[data-route]')].find(tr => tr.dataset.route === had[0]);
      const btn = next && next.querySelector('.' + had[1]);
      if (btn && !btn.disabled) btn.focus({ preventScroll: true });
    }
    const canAdd = setup.editable && !!routes && (setup.missing || !list.some(r => r.via === 'peer'));
    addBtn.hidden = !canAdd || editing !== undefined;
    addRow.hidden = addBtn.hidden;
    addBtn.disabled = !!busy;
    addBtn.textContent = setup.missing ? 'Set up Grok Bot routine' : 'Add route';
    if (setup.missing) addBtn.classList.add('pri'); else addBtn.classList.remove('pri');
    if (editing !== undefined && setup.editable) {
      // Keep a form being filled in: redraw it only when it opens.
      if (formBox.hidden || focusForm) {
        const first = drawForm();
        if (focusForm) first.focus();
      }
    } else if (!formBox.hidden) {
      formBox.replaceChildren();
      formBox.hidden = true;
    }
  }

  return {
    el,
    // draw(facts, routes, error): routes is peerRoutes' list, or null before
    // it loads. Only a change in what is offered redraws the table, so the
    // 30 s reload never takes focus off a button or arms a confirm anew.
    draw(f, list, error) {
      facts = f;
      routes = list;
      loadErr = error || null;
      setup = routeSetup(facts, routes);
      // An edited route that went away closes its form.
      if (editing && routes && !routes.some(r => r.id === editing.id)) closeForm(false);
      const key = JSON.stringify([setup, routes, busy, editing ? editing.id : editing === null ? '+' : '', loadErr ? String(loadErr.message || loadErr) : '']);
      if (key === lastKey && el.contains(document.activeElement)) return;
      lastKey = key;
      paint();
    },
    // openAdd opens Add route (the header's "set up" link) and brings it into view.
    openAdd() {
      if (!setup.editable) return;
      if (editing === undefined) openForm(null);
      el.scrollIntoView({ block: 'start' });
      const u = formBox.querySelector('input[type=url]');
      if (u) u.focus({ preventScroll: true });
    },
    unmount() { gone = true; closeSecret(false); },
  };
}
