// "Add agent" (the sidebar's nav button, and the dashboard's Peers heading):
// creates a peer and shows the paste-ready invite once, or for a ChatGPT dot
// its link code and setup steps. The token and the code live only in this
// dialog's DOM: never in storage or the URL, and closing removes the dialog,
// so they are gone with it.
import { copyText, h, toast } from './dom.js';
import { kindLabel, when } from './format.js';
import { CODE_STEP, DEFAULT_TTL, DOT_NOTE, DOT_STEPS, dotLink, invite, KINDS, TTLS } from './invite-model.js';

const LEDE = 'Creates a peer and a one-time enrolment token, then gives you a block to paste to the agent in its repository.';
const DOT_LEDE = 'Creates a dot peer and a one-time link code, then shows how to add vecnet to ChatGPT.';

function errText(x) {
  return (x && x.code && x.code !== 'network' ? x.code + ': ' : '') + ((x && x.message) || String(x));
}

// copier is a button that copies value, saying so for a moment.
function copier(label, value, cls) {
  const b = h('button', { type: 'button', class: 'btn' + (cls ? ' ' + cls : ''), text: label, onclick: async () => {
    if (await copyText(value)) { b.textContent = 'Copied'; setTimeout(() => { b.textContent = label; }, 1500); }
    else toast('Could not copy. Select the text and copy it instead.', true);
  } });
  return b;
}

let open = null; // the dialog's close(), while one is shown

export function closeInvite() { if (open) open(); }

// inviteButton opens the dialog; onCreated runs after a peer is created.
export function inviteButton(ctx, onCreated) {
  return h('button', { type: 'button', class: 'btn inv-open', text: 'Add agent', onclick: e => openInvite(ctx, onCreated, e.currentTarget) });
}

// openInvite shows the dialog over whatever view is mounted; focus returns
// to opener when it closes. One dialog at a time: opening closes any other.
export function openInvite(ctx, onCreated, opener) {
  closeInvite();
  const name = h('input', { id: 'inv-name', 'aria-required': 'true', maxlength: '80', spellcheck: 'false', autocomplete: 'off', placeholder: 'e.g. web-repo claude' });
  const kind = h('select', { id: 'inv-kind' }, KINDS.map(k => h('option', { value: k, text: kindLabel(k) })));
  const ttl = h('select', { id: 'inv-ttl' }, TTLS.map(([label, s]) => h('option', { value: String(s), text: label, selected: s === DEFAULT_TTL })));
  const openName = h('input', { id: 'inv-open', type: 'checkbox' });
  const nameLabel = h('label', { for: 'inv-name', text: 'Name' });
  // An open name: the agent picks it at enrol and the field is a suggestion.
  openName.addEventListener('change', () => {
    nameLabel.textContent = openName.checked ? 'Suggested name (optional)' : 'Name';
    name.setAttribute('aria-required', openName.checked ? 'false' : 'true');
  });
  const err = h('p', { class: 'err', role: 'alert' });
  name.addEventListener('input', () => { err.textContent = ''; });
  const createLabel = () => kind.value === 'dot' ? 'Create and link' : 'Create invite';
  const create = h('button', { type: 'submit', class: 'btn pri', text: 'Create invite' });
  const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => close() });
  const openRow = h('label', { class: 'inv-chk' }, openName, 'Let the agent choose its name at enrol');
  const ttlCell = h('span', null, h('label', { for: 'inv-ttl', text: 'Token valid for' }), ttl);
  const form = h('form', { class: 'inv-form', autocomplete: 'off' },
    nameLabel, name, openRow,
    h('div', { class: 'inv-row' },
      h('span', null, h('label', { for: 'inv-kind', text: 'Kind' }), kind), ttlCell),
    err, h('div', { class: 'inv-acts' }, cancel, create));
  const body = h('div', { class: 'inv-body' }, form);
  const lede = h('p', { class: 'inv-lede', text: LEDE });
  const dialog = h('dialog', { class: 'inv', 'aria-labelledby': 'inv-title' },
    h('h2', { id: 'inv-title', text: 'Add agent' }), lede, body);
  // A dot has no enrolment token and always has its name: no open name, no TTL.
  kind.addEventListener('change', () => {
    const dot = kind.value === 'dot';
    if (dot && openName.checked) { openName.checked = false; openName.dispatchEvent(new Event('change')); }
    openRow.hidden = ttlCell.hidden = dot;
    lede.textContent = dot ? DOT_LEDE : LEDE;
    create.textContent = createLabel();
  });

  form.addEventListener('submit', async e => {
    e.preventDefault();
    const n = name.value.trim();
    if (!n && !openName.checked) { err.textContent = 'A name is required.'; name.focus(); return; }
    err.textContent = '';
    create.disabled = true;
    create.textContent = 'Creating…';
    const dot = kind.value === 'dot';
    const body = dot ? { kind: 'dot' } : { kind: kind.value, enrol_ttl_seconds: Number(ttl.value) };
    if (n) body.name = n;
    if (openName.checked && !dot) body.name_open = true;
    try {
      const res = await ctx.api.addPeer(body);
      if (!dialog.isConnected) return; // closed while it ran
      if (dot) showDot(dotLink(res, location.origin));
      else showResult(invite(res, location.origin, !!n));
      if (onCreated) onCreated();
    } catch (x) {
      err.textContent = errText(x);
      create.disabled = false;
      create.textContent = createLabel();
    }
  });

  // showDot replaces the form once the dot peer exists, then asks for its
  // link code; a refused code keeps the panel and offers another try, since
  // the peer is already made.
  function showDot(d) {
    const slot = h('div', { class: 'inv-code', 'aria-live': 'polite' });
    async function linkCode() {
      const refocus = slot.contains(document.activeElement); // Try again, New code
      slot.replaceChildren(h('span', { class: 'inv-pending', text: 'Requesting a link code…' }));
      try {
        const link = await ctx.api.dotLink(d.peerId);
        if (!dialog.isConnected) return;
        const copy = copier('Copy code', link.code);
        slot.replaceChildren(
          h('div', { class: 'inv-code-row' },
            h('code', { class: 'inv-code-val mono', tabindex: '0', 'aria-label': 'Link code', text: link.code }), copy),
          h('p', { class: 'inv-meta mono' }, 'single use · expires ', h('time', { datetime: link.expires_at, title: link.expires_at, text: when(link.expires_at) }), ' · ',
            h('button', { type: 'button', class: 'inv-link', text: 'New code', onclick: linkCode })));
        if (refocus) copy.focus();
      } catch (x) {
        if (!dialog.isConnected) return;
        const retry = h('button', { type: 'button', class: 'btn', text: 'Try again', onclick: linkCode });
        slot.replaceChildren(h('p', { class: 'err', role: 'alert', text: 'No link code: ' + errText(x) }), retry);
        if (refocus) retry.focus();
      }
    }
    const steps = DOT_STEPS.map((parts, i) => h('li', null,
      h('span', null, parts.map(p => typeof p === 'string' ? p : h('b', { text: p.b }))),
      i === CODE_STEP ? slot : null));
    const url = copier('Copy URL', d.mcpURL, 'pri');
    lede.textContent = 'Connect ' + (d.name || 'the dot') + ' from ChatGPT.';
    body.replaceChildren(
      h('p', { class: 'inv-warn', role: 'note', text: 'Shown once — the link code binds a ChatGPT login to this dot. Give it only to whoever sets up the plugin.' }),
      h('label', { class: 'inv-sub', for: 'inv-mcp', text: 'MCP server URL' }),
      h('div', { class: 'inv-url-row' }, h('input', { id: 'inv-mcp', class: 'inv-url mono', readonly: true, value: d.mcpURL, spellcheck: 'false', onfocus: e => e.target.select() }), url),
      h('ol', { class: 'inv-steps' }, steps),
      h('p', { class: 'inv-note', text: DOT_NOTE }),
      h('p', { class: 'inv-meta mono', text: 'ChatGPT dot · peer ' + d.peerId }),
      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() })));
    url.focus();
    linkCode();
  }

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
      h('p', { class: 'inv-meta mono' }, 'profile ' + inv.profile + (inv.open ? ' · name chosen at enrol' : '') + ' · peer ' + inv.peerId + ' · expires ', h('time', { text: inv.expires })),
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
