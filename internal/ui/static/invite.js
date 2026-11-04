// "Add agent" (dashboard, Peers): creates a peer and shows the paste-ready
// invite once. The token lives only in this dialog's DOM: never in storage,
// and closing removes the dialog, so it is gone with it.
import { copyText, h, toast } from './dom.js';
import { DEFAULT_TTL, invite, KINDS, TTLS } from './invite-model.js';

let open = null; // the dialog's close(), while one is shown

export function closeInvite() { if (open) open(); }

// inviteButton opens the dialog; onCreated runs after a peer is created.
export function inviteButton(ctx, onCreated) {
  return h('button', { type: 'button', class: 'btn inv-open', text: 'Add agent', onclick: e => openInvite(ctx, onCreated, e.currentTarget) });
}

function openInvite(ctx, onCreated, opener) {
  closeInvite();
  const name = h('input', { id: 'inv-name', 'aria-required': 'true', maxlength: '80', spellcheck: 'false', autocomplete: 'off', placeholder: 'e.g. web-repo claude' });
  const kind = h('select', { id: 'inv-kind' }, KINDS.map(k => h('option', { value: k, text: k })));
  const ttl = h('select', { id: 'inv-ttl' }, TTLS.map(([label, s]) => h('option', { value: String(s), text: label, selected: s === DEFAULT_TTL })));
  const err = h('p', { class: 'err', role: 'alert' });
  const create = h('button', { type: 'submit', class: 'btn pri', text: 'Create invite' });
  const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => close() });
  const form = h('form', { class: 'inv-form', autocomplete: 'off' },
    h('label', { for: 'inv-name', text: 'Name' }), name,
    h('div', { class: 'inv-row' },
      h('span', null, h('label', { for: 'inv-kind', text: 'Kind' }), kind),
      h('span', null, h('label', { for: 'inv-ttl', text: 'Token valid for' }), ttl)),
    err, h('div', { class: 'inv-acts' }, cancel, create));
  const body = h('div', { class: 'inv-body' }, form);
  const dialog = h('dialog', { class: 'inv', 'aria-labelledby': 'inv-title' },
    h('h2', { id: 'inv-title', text: 'Add agent' }),
    h('p', { class: 'inv-lede', text: 'Creates a peer and a one-time enrolment token, then gives you a block to paste to the agent in its repository.' }),
    body);

  form.addEventListener('submit', async e => {
    e.preventDefault();
    const n = name.value.trim();
    if (!n) { err.textContent = 'A name is required.'; name.focus(); return; }
    err.textContent = '';
    create.disabled = true;
    create.textContent = 'Creating…';
    try {
      const res = await ctx.api.addPeer({ kind: kind.value, name: n, enrol_ttl_seconds: Number(ttl.value) });
      if (!dialog.isConnected) return; // closed while it ran
      showResult(invite(n, res, location.origin));
      if (onCreated) onCreated();
    } catch (x) {
      err.textContent = (x && x.code && x.code !== 'network' ? x.code + ': ' : '') + ((x && x.message) || String(x));
      create.disabled = false;
      create.textContent = 'Create invite';
    }
  });

  function showResult(inv) {
    const text = h('pre', { class: 'inv-block mono', tabindex: '0', 'aria-label': 'Invite text' });
    text.textContent = inv.text; // as is: clean() would alter what the agent runs
    const copy = h('button', { type: 'button', class: 'btn pri', text: 'Copy invite', onclick: async () => {
      if (await copyText(inv.text)) { copy.textContent = 'Copied'; setTimeout(() => { copy.textContent = 'Copy invite'; }, 1500); }
      else toast('Could not copy. Select the text and copy it instead.', true);
    } });
    body.replaceChildren(
      h('p', { class: 'inv-warn', role: 'note', text: 'Shown once — it grants a new identity on your hub. Paste it only to the agent it is for.' }),
      text,
      h('p', { class: 'inv-meta mono' }, 'profile ' + inv.profile + ' · peer ' + inv.peerId + ' · expires ', h('time', { text: inv.expires })),
      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() }), copy));
    copy.focus();
  }

  function close() {
    if (open !== close) return;
    open = null;
    if (dialog.open) dialog.close();
    dialog.remove();
    if (opener && opener.isConnected) opener.focus();
  }
  dialog.addEventListener('close', close); // Esc
  open = close;
  document.body.append(dialog);
  dialog.showModal();
  name.focus();
}
