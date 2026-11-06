// "Add agent" (the sidebar's nav button, the dashboard's Peers heading, and
// #add-agent?kind=&name=&request_id=). The hub registry supplies kinds,
// prompts and submit actions. Human output stays in this dialog: the token,
// the link code and the routine key are never written to storage, the URL,
// the clipboard (except the invite and the link code, on purpose) or the
// console. PROTOCOL §§2.12, 4.5–4.6, 12, 13.3 and 20.1.
import { copyText, h, toast } from './dom.js';
import { kindLabel, when } from './format.js';
import {
  BEARER_CONFIRM, BEARER_WARN, CODE_STEP, DEFAULT_TTL, DOT_NOTE, DOT_STEPS, GATEWAY_NOTE, QUOTA_NOTE,
  SKIP_MCP_HELP, SKIP_MCP_LABEL, TTLS, acceptCompletion, authorizePrompt, beginLoad, applyLoad, clientIssues,
  connectorDefault, connectorOverrideError, emptyRegistry, failLoad, freshRequestId, humanOutput, kindsFrom,
  linkPanel, mustConfirmBearerRevoke, outcomeOf, presentError, specIssue, statusSafe, submitBody, submitGate,
  usableRequestId,
} from './invite-model.js';

const LEDE = 'Creates a peer and a one-time enrolment token, then gives you a block to paste to the agent in its repository.';
const DOT_LEDE = 'Creates a dot peer and a one-time link code, then shows how to add vecnet to ChatGPT.';
const GROK_LEDE = 'Show this prompt to the bot first. Then submit the routine URL and key here. Nothing is sent until you submit.';
const NOT_CONNECTED = 'Queued or accepted does not mean the bot is connected. A webhook response is not a connection.';

let open = null; // the dialog's close(), while one is shown

export function closeInvite() { if (open) open(); }

export function inviteButton(ctx, onCreated) {
  return h('button', { type: 'button', class: 'btn inv-open', text: 'Add agent', onclick: e => openInvite(ctx, onCreated, e.currentTarget) });
}

function copier(label, value, cls) {
  const b = h('button', { type: 'button', class: 'btn' + (cls ? ' ' + cls : ''), text: label, onclick: async () => {
    if (await copyText(value)) { b.textContent = 'Copied'; setTimeout(() => { b.textContent = label; }, 1500); }
    else toast('Could not copy. Select the text and copy it instead.', true);
  } });
  return b;
}

// openInvite shows the dialog over whatever view is mounted. prefill comes
// from #add-agent and is not submitted. One dialog at a time.
export function openInvite(ctx, onCreated, opener, prefill) {
  closeInvite();
  const ac = new AbortController();
  let life = 1;
  let registry = emptyRegistry();
  let hubURL = '';
  let gate = { busy: false };
  let linkBusy = false;
  let usedPrefill = false;
  let rendering = false;
  let cancelBearer = () => {};
  const requestId = usableRequestId(prefill && prefill.requestId) || freshRequestId();

  const name = h('input', { id: 'inv-name', 'data-field': 'name', 'aria-required': 'true', maxlength: '128', spellcheck: 'false', autocomplete: 'off', placeholder: 'e.g. web-repo claude' });
  const kind = h('select', { id: 'inv-kind' });
  const ttl = h('select', { id: 'inv-ttl', 'data-field': 'enrol_ttl_seconds' }, TTLS.map(([label, s]) => h('option', { value: String(s), text: label, selected: s === DEFAULT_TTL })));
  const openName = h('input', { id: 'inv-open', type: 'checkbox', 'data-field': 'name_open' });
  const principal = h('input', { id: 'inv-principal', 'data-field': 'principal_id', spellcheck: 'false', autocomplete: 'off', placeholder: 'optional' });
  const url = h('input', { id: 'inv-url', 'data-field': 'webhook_url', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', placeholder: 'https://api2.cursor.sh/automations/webhook/…' });
  const key = h('input', { id: 'inv-key', type: 'password', 'data-field': 'webhook_key', 'data-secret': 'true', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', maxlength: '4096' });
  const connector = h('input', { id: 'inv-connector', 'data-field': 'connector_name', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', placeholder: 'vecnet-…' });
  const skip = h('input', { id: 'inv-skip', type: 'checkbox', 'data-field': 'skip_mcp' });
  const err = h('p', { class: 'err', role: 'alert' });
  const fieldsHost = h('div', { class: 'inv-fields' });
  const create = h('button', { type: 'submit', class: 'btn pri', text: 'Create invite', disabled: true });
  const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => close() });
  const form = h('form', { class: 'inv-form', autocomplete: 'off' },
    fieldsHost, err, h('div', { class: 'inv-acts' }, cancel, create));
  const body = h('div', { class: 'inv-body' }, form);
  const lede = h('p', { class: 'inv-lede', text: LEDE });
  const dialog = h('dialog', { class: 'inv', 'aria-labelledby': 'inv-title' },
    h('h2', { id: 'inv-title', text: 'Add agent' }), lede, body);

  function currentSpec() {
    return (registry.specs || []).find(s => s && s.kind === kind.value) || null;
  }
  function createLabel() {
    const action = currentSpec() && currentSpec().submit_action;
    if (action === 'oauth_link') return 'Create and link';
    if (action === 'grokbot_setup') return 'Create Grok bot';
    return 'Create invite';
  }
  function hubOrigin() {
    return (hubURL || location.origin).replace(/\/+$/, '');
  }
  function shownConnector() {
    const typed = connector.value.trim();
    if (typed && !connectorOverrideError(typed)) return typed;
    return connectorDefault(name.value);
  }
  function readValues() {
    const out = {};
    for (const el of dialog.querySelectorAll('[data-field]')) {
      if (!el.isConnected) continue;
      out[el.dataset.field] = el.type === 'checkbox' ? el.checked : el.value;
    }
    return out;
  }
  function hidden() {
    const found = [];
    const live = readValues();
    for (const v of [live.webhook_key, live.webhook_url, key.value, url.value]) {
      if (typeof v === 'string' && v.trim().length >= 8) found.push(v);
    }
    return found;
  }
  function clearSecrets() {
    key.value = '';
    url.value = '';
    for (const el of dialog.querySelectorAll('[data-secret]')) el.value = '';
  }
  function focusName() {
    if (name.isConnected) name.focus();
  }
  // The loading render and the registry render share the same inputs. Moving
  // them drops focus onto the body or the opener; put it back on the name
  // unless the person has already moved into the dialog.
  function restoreOpeningFocus() {
    const active = document.activeElement;
    if (!active || active === document.body || active === dialog || !dialog.contains(active)) focusName();
  }
  function dropRetry() {
    const row = dialog.querySelector('.inv-retry');
    if (row) row.remove();
    for (const note of dialog.querySelectorAll('.inv-field-err')) note.remove();
  }

  function renderFields() {
    rendering = true;
    try { renderFieldsBody(); }
    finally { rendering = false; }
  }

  function renderFieldsBody() {
    const kinds = kindsFrom(registry.specs);
    const previous = kind.value;
    kind.replaceChildren(...kinds.map(k => {
      const s = registry.specs.find(x => x.kind === k);
      return h('option', { value: k, text: (s && s.label) || kindLabel(k) });
    }));
    if (!usedPrefill && kinds.length) {
      usedPrefill = true;
      if (prefill && prefill.name && !name.value) name.value = prefill.name;
      if (prefill && prefill.kind && kinds.includes(prefill.kind)) kind.value = prefill.kind;
      else if (prefill && prefill.kind) err.textContent = 'That kind is not in the hub registry.';
      else kind.value = kinds[0];
    } else if (previous && kinds.includes(previous)) kind.value = previous;
    else if (kinds[0]) kind.value = kinds[0];

    if (!kinds.length) {
      const retry = registry.phase === 'error'
        ? h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => loadKinds() }) : null;
      fieldsHost.replaceChildren(
        h('p', { class: 'inv-loading', text: registry.error || 'Loading kinds…' }),
        h('label', { for: 'inv-name', text: 'Name' }), name, retry);
      create.disabled = true;
      return;
    }
    const spec = currentSpec();
    const issue = spec && specIssue(spec);
    const action = spec ? spec.submit_action : '';
    if (action !== 'invite' && openName.checked) openName.checked = false;
    lede.textContent = action === 'oauth_link' ? DOT_LEDE : action === 'grokbot_setup' ? GROK_LEDE : LEDE;
    create.textContent = gate.busy ? 'Creating…' : createLabel();
    create.disabled = !!gate.busy;
    if (!spec || issue) {
      const why = issue && issue.code === 'unsupported_field'
        ? 'This page cannot enter the field ' + issue.name + '.'
        : 'This page cannot run that submit action.';
      fieldsHost.replaceChildren(h('p', { class: 'err', role: 'alert', text: why }));
      create.disabled = true;
      return;
    }

    const kids = [];
    const kindSlot = h('span', null, h('label', { for: 'inv-kind', text: 'Kind' }), kind);
    const nameLabel = h('label', { for: 'inv-name', text: action === 'invite' && openName.checked ? 'Suggested name (optional)' : 'Name' });
    name.setAttribute('aria-required', action === 'invite' && openName.checked ? 'false' : 'true');
    if (action === 'grokbot_setup') {
      const prompt = h('pre', { id: 'inv-prompt', class: 'inv-prompt mono', tabindex: '0', 'aria-label': 'Authorization prompt' });
      prompt.textContent = authorizePrompt(spec.prompt_template, hubOrigin(), shownConnector());
      kids.push(prompt,
        h('p', { id: 'inv-gateway', class: 'inv-note', text: GATEWAY_NOTE }),
        h('p', { id: 'inv-quota', class: 'inv-note', text: QUOTA_NOTE }));
    }
    kids.push(nameLabel, name);
    if (action === 'invite' && (spec.fields || []).some(f => f.name === 'name_open')) {
      kids.push(h('label', { class: 'inv-chk' }, openName, 'Let the agent choose its name at enrol'));
    }
    if (action === 'grokbot_setup') kids.push(...grokFields());
    const ttlCell = h('span', null, h('label', { for: 'inv-ttl', text: 'Token valid for' }), ttl);
    if (action === 'invite' && (spec.fields || []).some(f => f.name === 'enrol_ttl_seconds')) {
      kids.push(h('div', { class: 'inv-row' }, kindSlot, ttlCell));
    } else kids.push(h('div', { class: 'inv-row' }, kindSlot));
    if ((spec.fields || []).some(f => f.name === 'principal_id') && action !== 'grokbot_setup') {
      kids.push(h('label', { for: 'inv-principal', text: 'Existing principal (optional)' }), principal);
    }
    fieldsHost.replaceChildren(...kids);
  }

  function grokFields() {
    const derived = h('p', { id: 'inv-derived', class: 'inv-derived mono', text: 'Connector ' + shownConnector() });
    return [
      h('label', { for: 'inv-url', text: 'Routine URL' }), url,
      h('p', { class: 'inv-note', text: 'Copied from the routine page by a person. It is not put in the prompt or the setup message.' }),
      h('label', { for: 'inv-key', text: 'Routine key' }), key,
      h('p', { class: 'inv-note', text: 'Masked. A raw key or one leading Bearer prefix. Cleared when you switch kind, finish, or close. Not saved.' }),
      h('label', { for: 'inv-connector', text: 'Connector name' }), connector, derived,
      h('label', { class: 'inv-chk', for: 'inv-skip' }, skip, SKIP_MCP_LABEL),
      h('p', { class: 'inv-note', text: SKIP_MCP_HELP }),
    ];
  }

  function refreshGrokCopy() {
    const prompt = fieldsHost.querySelector('#inv-prompt');
    const derived = fieldsHost.querySelector('#inv-derived');
    const spec = currentSpec();
    if (prompt && spec) prompt.textContent = authorizePrompt(spec.prompt_template, hubOrigin(), shownConnector());
    if (derived) derived.textContent = 'Connector ' + shownConnector();
  }

  name.addEventListener('input', () => { err.textContent = ''; dropRetry(); refreshGrokCopy(); });
  connector.addEventListener('input', refreshGrokCopy);
  url.addEventListener('input', () => { err.textContent = ''; dropRetry(); });
  key.addEventListener('input', () => { err.textContent = ''; dropRetry(); });
  openName.addEventListener('change', () => {
    const label = fieldsHost.querySelector('label[for="inv-name"]');
    if (label) label.textContent = openName.checked ? 'Suggested name (optional)' : 'Name';
    name.setAttribute('aria-required', openName.checked ? 'false' : 'true');
  });
  kind.addEventListener('change', () => {
    if (rendering) return;
    clearSecrets();
    err.textContent = '';
    dropRetry();
    renderFields();
  });

  async function loadKinds() {
    const ticket = life;
    registry = beginLoad(registry);
    renderFields();
    restoreOpeningFocus();
    try {
      const body = await ctx.api.agentKinds({ signal: ac.signal });
      let ident = null;
      try { ident = await ctx.api.hubIdentity({ signal: ac.signal }); }
      catch { ident = null; }
      if (!acceptCompletion(life, ticket, ac.signal.aborted)) return;
      if (ident && ident.public_url) hubURL = String(ident.public_url).replace(/\/+$/, '');
      registry = applyLoad(registry, registry.generation, Array.isArray(body.kinds) ? body.kinds : []);
    } catch (e) {
      if (!acceptCompletion(life, ticket, ac.signal.aborted)) return;
      registry = failLoad(registry, registry.generation, presentError(e, hidden()).text);
    }
    if (!acceptCompletion(life, ticket, ac.signal.aborted)) return;
    renderFields();
    restoreOpeningFocus();
  }

  function markField(field, text) {
    if (!field || !/^[A-Za-z0-9_]+$/.test(field)) return;
    const input = fieldsHost.querySelector('[data-field="' + field + '"]');
    if (!input) { focusName(); return; }
    const note = h('p', { class: 'inv-field-err', role: 'alert', text });
    input.after(note);
    input.focus();
  }

  async function send() {
    const next = submitGate(gate);
    if (!next.accepted) return;
    gate = next;
    dropRetry();
    const ticket = life;
    const spec = currentSpec();
    if (!spec || specIssue(spec)) {
      gate = { busy: false };
      err.textContent = 'This page cannot submit that kind.';
      create.disabled = true;
      return;
    }
    const vals = readValues();
    const issues = clientIssues(spec, vals);
    const first = Object.keys(issues)[0];
    if (first) {
      gate = { busy: false };
      err.textContent = issues[first];
      markField(first, issues[first]);
      return;
    }
    let bodyReq;
    try { bodyReq = submitBody(spec, vals, requestId); }
    catch (e) {
      gate = { busy: false };
      err.textContent = presentError(e, hidden()).text;
      return;
    }
    err.textContent = '';
    create.disabled = true;
    create.textContent = 'Creating…';
    try {
      const res = await ctx.api.agentAdd(bodyReq, { signal: ac.signal });
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      const submitted = vals;
      clearSecrets();
      if (spec.submit_action === 'oauth_link') showDot(res, submitted);
      else if (spec.submit_action === 'grokbot_setup') showGrok(res, submitted);
      else showInvite(res, submitted);
      if (onCreated) onCreated();
    } catch (x) {
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      gate = { busy: false };
      create.disabled = false;
      create.textContent = createLabel();
      if (outcomeOf(x) === 'stale') {
        clearSecrets();
        err.textContent = 'The kind contract changed. Review the form and submit again.';
        await loadKinds();
        return;
      }
      const shown = presentError(x, hidden());
      err.textContent = shown.text;
      markField(shown.field, shown.text);
      if (outcomeOf(x) === 'uncertain') {
        err.after(h('div', { class: 'inv-retry' },
          h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => send() }),
          h('button', { type: 'button', class: 'btn', text: 'Check status', onclick: () => checkStatus() })));
      }
    }
  }

  async function checkStatus() {
    const ticket = life;
    try {
      const res = await ctx.api.onboardingStatus(requestId, { signal: ac.signal });
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      showSafe(statusSafe(res), 'Status for this request. One-time text and routine secrets are not included.');
    } catch (e) {
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      err.textContent = e && e.code === 'not_found' ? 'No committed request for this id yet.' : presentError(e, hidden()).text;
    }
  }

  function safeMeta(safe) {
    const setup = safe.setup && safe.setup.state;
    const parts = ['request ' + (safe.request_id || requestId)];
    if (safe.peer_id) parts.push('peer ' + safe.peer_id);
    if (safe.auth_state) parts.push('auth ' + safe.auth_state);
    if (setup) parts.push('setup ' + setup);
    if (safe.connector_name) parts.push('connector ' + safe.connector_name);
    return h('p', { class: 'inv-meta mono inv-status', text: parts.join(' · ') });
  }

  function showSafe(safe, note) {
    lede.textContent = 'Status for this request. One-time text is not shown again.';
    clearSecrets();
    body.replaceChildren(
      h('p', { class: 'inv-note', text: note }),
      h('p', { class: 'inv-warn', role: 'note', text: NOT_CONNECTED }),
      safeMeta(safe),
      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() })));
  }

  function showInvite(res, vals) {
    const output = humanOutput(res);
    if (!output) { showSafe(statusSafe(res), 'This request already completed. The one-time invite is not shown again.'); return; }
    const text = h('pre', { class: 'inv-block mono', tabindex: '0', 'aria-label': 'Invite text' });
    text.textContent = output;
    const prof = (output.match(/--profile ([^ \n]+)/) || [])[1] || '';
    const exp = (output.match(/\(invite expires ([^)]+)\)/) || [])[1] || '';
    const copy = h('button', { type: 'button', class: 'btn pri', text: 'Copy invite', onclick: async () => {
      if (await copyText(output)) { copy.textContent = 'Copied'; setTimeout(() => { copy.textContent = 'Copy invite'; }, 1500); }
      else toast('Could not copy. Select the text and copy it instead.', true);
    } });
    body.replaceChildren(
      h('p', { class: 'inv-warn', role: 'note', text: 'Shown once — it grants a new identity on your hub. Paste it only to the agent it is for.' }),
      text,
      h('p', { class: 'inv-meta mono' },
        'profile ' + prof + (vals.name_open ? ' · name chosen at enrol' : '') + ' · peer ' + (res.peer_id || ''),
        exp ? [' · expires ', h('time', { text: exp })] : null),
      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() }), copy));
    copy.focus();
  }

  function showDot(res, vals) {
    const d = linkPanel({ ...res, name: vals.name }, location.origin, hubURL);
    const slot = h('div', { class: 'inv-code', 'aria-live': 'polite' });
    if (d.code) fillCode(slot, { code: d.code }, d.peerId, false);
    else slot.append(h('p', { class: 'inv-note', text: 'This request already completed. The one-time link code is not shown again.' }));
    paintDot(d, slot);
  }

  function paintDot(d, slot) {
    const steps = DOT_STEPS.map((parts, i) => h('li', null,
      h('span', null, parts.map(p => typeof p === 'string' ? p : h('b', { text: p.b }))),
      i === CODE_STEP ? slot : null));
    const urlBtn = copier('Copy URL', d.mcpURL, 'pri');
    lede.textContent = 'Connect ' + (d.name || 'the dot') + ' from ChatGPT.';
    body.replaceChildren(
      h('p', { class: 'inv-warn', role: 'note', text: 'Shown once — the link code binds a ChatGPT login to this dot. Give it only to whoever sets up the plugin.' }),
      h('label', { class: 'inv-sub', for: 'inv-mcp', text: 'MCP server URL' }),
      h('div', { class: 'inv-url-row' }, h('input', { id: 'inv-mcp', class: 'inv-url mono', readonly: true, value: d.mcpURL, spellcheck: 'false', onfocus: e => e.target.select() }), urlBtn),
      h('ol', { class: 'inv-steps' }, steps),
      h('p', { class: 'inv-note', text: DOT_NOTE }),
      h('p', { class: 'inv-meta mono', text: 'ChatGPT dot · peer ' + d.peerId }),
      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() })));
    if (!slot.querySelector('.inv-code-val')) urlBtn.focus();
    else urlBtn.focus();
  }

  function fillCode(slot, link, peerId, refocus) {
    const copy = copier('Copy code', link.code);
    const row = h('p', { class: 'inv-meta mono' }, 'single use',
      link.expires_at ? [' · expires ', h('time', { datetime: link.expires_at, title: link.expires_at, text: when(link.expires_at) })] : null,
      ' · ',
      h('button', { type: 'button', class: 'inv-link', text: 'New code', onclick: () => guardedLink(slot, peerId) }));
    slot.replaceChildren(h('div', { class: 'inv-code-row' },
      h('code', { class: 'inv-code-val mono', tabindex: '0', 'aria-label': 'Link code', text: link.code }), copy), row);
    if (refocus) copy.focus();
  }

  function askBearer() {
    return new Promise(resolve => {
      let settled = false;
      const done = ok => { if (settled) return; settled = true; box.remove(); resolve(ok); };
      cancelBearer = () => done(false);
      const box = h('div', { class: 'inv-bearer', role: 'note' },
        h('p', { text: BEARER_WARN }),
        h('div', { class: 'inv-acts' },
          h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => done(false) }),
          h('button', { type: 'button', class: 'btn pri', text: BEARER_CONFIRM, onclick: () => done(true) })));
      body.append(box);
      const first = box.querySelector('button');
      if (first) first.focus();
    });
  }

  async function guardedLink(slot, peerId) {
    if (linkBusy || !peerId) return;
    linkBusy = true;
    const ticket = life;
    try {
      let cred = '';
      try {
        const page = await ctx.api.adminPeers({ signal: ac.signal });
        if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
        const row = ((page && page.peers) || []).find(r => r && r.peer && r.peer.peer_id === peerId);
        cred = (row && row.credential) || '';
      } catch (e) {
        if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
        const retry = h('button', { type: 'button', class: 'btn', text: 'Try again', onclick: () => guardedLink(slot, peerId) });
        slot.replaceChildren(h('p', { class: 'err', role: 'alert', text: 'Could not check for a live bearer: ' + presentError(e, hidden()).text }), retry);
        retry.focus();
        return;
      }
      if (mustConfirmBearerRevoke(cred)) {
        const ok = await askBearer();
        if (!ok || !dialog.isConnected || !acceptCompletion(life, ticket, ac.signal.aborted)) return;
      }
      await linkCode(slot, peerId, ticket);
    } finally { linkBusy = false; }
  }

  async function linkCode(slot, peerId, ticket) {
    const refocus = slot.contains(document.activeElement);
    slot.replaceChildren(h('span', { class: 'inv-pending', text: 'Requesting a link code…' }));
    try {
      const link = await ctx.api.dotLink(peerId);
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      fillCode(slot, link, peerId, refocus);
    } catch (x) {
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      const retry = h('button', { type: 'button', class: 'btn', text: 'Try again', onclick: () => guardedLink(slot, peerId) });
      slot.replaceChildren(h('p', { class: 'err', role: 'alert', text: 'No link code: ' + presentError(x, hidden()).text }), retry);
      if (refocus) retry.focus();
    }
  }

  function showGrok(res, vals) {
    lede.textContent = 'The bot is not connected yet. Whoami is the proof, after the setup message names this peer.';
    const safe = statusSafe(res);
    const skipMCP = vals.skip_mcp === true;
    const slot = h('div', { class: 'inv-code', 'aria-live': 'polite' });
    const mode = skipMCP
      ? 'Credential mode: CLI bearer. Use the webhook and this bot\'s own verified CLI profile. Hosted OAuth is not used for this bot.'
      : 'Credential mode: hosted OAuth. Create a link code only after confirming that a live bearer would be revoked.';
    const acts = [h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() })];
    if (!skipMCP && safe.peer_id) {
      acts.push(h('button', { type: 'button', class: 'btn pri', id: 'inv-start-link', text: 'Create link code', onclick: () => guardedLink(slot, safe.peer_id) }));
    }
    body.replaceChildren(
      h('p', { class: 'inv-warn', role: 'note', text: NOT_CONNECTED }),
      h('p', { class: 'inv-note', text: mode }),
      h('p', { id: 'inv-quota', class: 'inv-note', text: QUOTA_NOTE }),
      h('p', { id: 'inv-gateway', class: 'inv-note', text: GATEWAY_NOTE }),
      safeMeta(safe),
      slot,
      h('div', { class: 'inv-acts' }, acts));
  }

  form.addEventListener('submit', e => { e.preventDefault(); send(); });

  function close() {
    if (open !== close) return;
    open = null;
    life += 1;
    ac.abort();
    cancelBearer();
    clearSecrets();
    if (dialog.open) dialog.close();
    dialog.remove();
    if (opener && opener.isConnected) opener.focus();
  }
  dialog.addEventListener('close', close);
  open = close;
  document.body.append(dialog);
  renderFields();
  dialog.showModal();
  focusName();
  loadKinds();
}
