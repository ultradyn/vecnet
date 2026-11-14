// "Add agent" (the sidebar's nav button, the dashboard's Peers heading, and
// #add-agent?kind=&name=&request_id=). The hub registry supplies kinds,
// prompts and submit actions. Human output stays in this dialog: the token,
// the link code and the routine key are never written to storage, the URL or the
// console. Copy puts the invite, the link code or the authorization prompt
// on the clipboard, and nothing else. PROTOCOL §§2.12, 4.5–4.6, 12, 13.3 and 20.1.
import { copyText, h, toast } from './dom.js';
import { LAUNCHER_HOST_HELP, launcherHostRecovery, launcherSetupCommands, launcherSetupIssue } from './launcher-model.js';
import { kindLabel, when } from './format.js';
import {
  BEARER_CONFIRM, BEARER_WARN, DEFAULT_TTL, GATEWAY_NOTE, QUOTA_NOTE,
  SKIP_MCP_HELP, SKIP_MCP_LABEL, TTLS, acceptCompletion, authorizePrompt, beginLoad, applyLoad, clientIssues,
  connectorDefault, connectorOverrideError, dotInstructions, emptyRegistry, failLoad, firstLinkExpiry, freshRequestId, humanOutput, kindsFrom,
  linkPanel, mustConfirmBearerRevoke, outcomeOf, prefillValues, presentError, specIssue, statusSafe, submitBody, submitGate,
  usableRequestId,
} from './invite-model.js';

const LEDE = 'Creates a peer and a one-time enrolment token, then gives you a block to paste to the agent in its repository.';
const DOT_LEDE = 'Creates a dot peer and a one-time link code, then shows how to add vecnet to ChatGPT.';
const GROK_LEDE = 'Show this prompt to the bot first. Then submit the routine URL and key here. Nothing is sent until you submit.';
const NOT_CONNECTED = 'Queued or accepted does not mean the bot is connected. A webhook response is not a connection.';

// invitePanel is the paste-ready invite, shown once: the warning, the block
// as is, its profile, peer and expiry, then Done and Copy invite. Add agent
// and the peer page's Invite card (peer.js) both draw it, so what an agent is
// handed and the controls around it are the same. inv is invite() or
// reinvite() (invite-model.js). The token is only in the block's text and the
// copy closure, never in an attribute. Returns { nodes, copy }.
export function invitePanel(inv, { onDone }) {
  const text = h('pre', { class: 'inv-block mono', tabindex: '0', 'aria-label': 'Invite text' });
  text.textContent = inv.text; // as is: clean() would alter what the agent runs
  const copy = h('button', { type: 'button', class: 'btn pri', text: 'Copy invite', onclick: async () => {
    if (await copyText(inv.text)) { copy.textContent = 'Copied'; setTimeout(() => { copy.textContent = 'Copy invite'; }, 1500); }
    else toast('Could not copy. Select the text and copy it instead.', true);
  } });
  return {
    copy,
    nodes: [
      h('p', { class: 'inv-warn', role: 'note', text: 'Shown once — it grants a new identity on your hub. Paste it only to the agent it is for.' }),
      text,
      h('p', { class: 'inv-meta mono' }, 'profile ' + inv.profile + (inv.open ? ' · name chosen at enrol' : '') + ' · peer ' + inv.peerId + ' · expires ', h('time', { text: inv.expires })),
      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => onDone() }), copy),
    ],
  };
}

let open = null; // the dialog's close(), while one is shown
// Only safe reconciliation metadata survives a dialog. API identity scopes it
// to the signed-in caller; neither form values nor one-time output live here.
const recoveries = new WeakMap();
let moveRecovery = null;

// Called only when following the hub's moving notice. Keep the destination
// unchanged except for a safe fragment; following it never submits anything.
export function onboardingMoveURL(target) {
  if (!moveRecovery) return target;
  const dest = new URL(target);
  if (!/^https?:$/.test(dest.protocol) || dest.username || dest.password) return target;
  const q = new URLSearchParams({ kind: moveRecovery.kind, request_id: moveRecovery.requestId, recover: '1' });
  dest.hash = 'add-agent?' + q;
  return dest.href;
}

export function closeInvite() { if (open) open(); }

export function inviteButton(ctx, onCreated) {
  return h('button', { type: 'button', class: 'btn inv-open', text: 'Add agent', onclick: e => openInvite(ctx, onCreated, e.currentTarget) });
}

function copier(label, value, cls, feedback) {
  const b = h('button', { type: 'button', class: 'btn' + (cls ? ' ' + cls : ''), text: label, onclick: async () => {
    const text = typeof value === 'function' ? value() : value;
    if (feedback) { feedback.replaceChildren(); b.textContent = label; }
    if (await copyText(text)) { b.textContent = 'Copied'; setTimeout(() => { b.textContent = label; }, 1500); }
    else if (feedback) {
      feedback.append(h('p', { class: 'inv-note', text: 'Could not copy. Select the text and copy it instead.' }));
      feedback.scrollIntoView({ block: 'nearest' });
    } else toast('Could not copy. Select the text and copy it instead.', true);
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
  const saved = recoveries.get(ctx.api);
  const suppliedId = usableRequestId(prefill && prefill.requestId);
  const restored = saved && (!suppliedId || suppliedId === saved.requestId) ? saved : null;
  let recovering = !!restored || !!(suppliedId && prefill.recover);
  const requestId = suppliedId || (restored && restored.requestId) || freshRequestId();
  if (restored) prefill = { ...prefill, kind: restored.kind };
  moveRecovery = restored || null;
  function remember(kind) {
    const safe = { requestId, kind };
    recoveries.set(ctx.api, safe);
    moveRecovery = safe;
  }
  function forget() {
    if (recoveries.get(ctx.api)?.requestId === requestId) recoveries.delete(ctx.api);
    if (moveRecovery?.requestId === requestId) moveRecovery = null;
    recovering = false;
  }
  if (recovering) remember(prefill.kind);
  // pending is the uncertain (or conflicting) submission. Edits do not replace
  // it, and a newer submit waits until status says it never committed.
  let pending = null;
  let statusGen = 0;
  let statusAbort = new AbortController();
  let humanHeld = false;
  let createdNotified = false;

  const name = h('input', { id: 'inv-name', 'data-field': 'name', 'aria-required': 'true', maxlength: '128', spellcheck: 'false', autocomplete: 'off', placeholder: 'e.g. web-repo claude' });
  const kind = h('select', { id: 'inv-kind' });
  const ttl = h('select', { id: 'inv-ttl', 'data-field': 'enrol_ttl_seconds' }, TTLS.map(([label, s]) => h('option', { value: String(s), text: label, selected: s === DEFAULT_TTL })));
  const openName = h('input', { id: 'inv-open', type: 'checkbox', 'data-field': 'name_open' });
  const principal = h('input', { id: 'inv-principal', 'data-field': 'principal_id', spellcheck: 'false', autocomplete: 'off', placeholder: 'optional' });
  const url = h('input', { id: 'inv-url', 'data-field': 'webhook_url', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', placeholder: 'https://api2.cursor.sh/automations/webhook/…' });
  const key = h('input', { id: 'inv-key', type: 'password', 'data-field': 'webhook_key', 'data-secret': 'true', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', maxlength: '4096' });
  const connector = h('input', { id: 'inv-connector', 'data-field': 'connector_name', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', placeholder: 'vecnet-…' });
  const skip = h('input', { id: 'inv-skip', type: 'checkbox', 'data-field': 'skip_mcp' });
  // These are commands for the daemon host, kept in this dialog only. They
  // never become onboarding inputs or requester launch authority.
  const setupProject = h('input', { id: 'inv-launch-project', value: 'app', spellcheck: 'false' });
  const setupRepo = h('input', { id: 'inv-launch-repo', placeholder: '/home/you/src/app', spellcheck: 'false' });
  const setupBase = h('input', { id: 'inv-launch-base', value: 'master', spellcheck: 'false' });
  const setupSource = h('input', { id: 'inv-launch-source', placeholder: '/home/you/src/vecnet', spellcheck: 'false' });
  const readSetup = () => ({ project: setupProject.value.trim(), repo: setupRepo.value.trim(), baseRef: setupBase.value.trim(), source: setupSource.value.trim() });
  const err = h('p', { class: 'err', role: 'alert' });
  const kindSlot = h('div', { class: 'inv-kind', hidden: '' }, h('label', { for: 'inv-kind', text: 'Kind' }), kind);
  const fieldsHost = h('div', { class: 'inv-fields' });
  const create = h('button', { type: 'submit', class: 'btn pri', text: 'Create invite', disabled: true });
  const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => close() });
  const foot = h('div', { class: 'inv-foot' }, err, h('div', { class: 'inv-acts' }, cancel, create));
  const form = h('form', { class: 'inv-form', autocomplete: 'off' }, kindSlot, fieldsHost, foot);
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
    return currentSpec()?.kind === 'launcher' ? 'Create launcher setup' : 'Create invite';
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
    for (const el of dialog.querySelectorAll('[data-secret]')) {
      if (typeof el.value === 'string' && el.value.trim().length >= 8 && !found.includes(el.value)) found.push(el.value);
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
  // After a stale contract the routine URL and key are empty again. Focus the
  // first of those, otherwise the name.
  function focusReentry() {
    for (const id of ['inv-url', 'inv-key', 'inv-name']) {
      const el = dialog.querySelector('#' + id);
      if (el && el.isConnected && !String(el.value || '').trim()) { el.focus(); return; }
    }
    focusName();
  }
  // The loading render and the registry render share the same inputs. Moving
  // them drops focus onto the body or the opener; put it back on the name
  // unless the person has already moved into the dialog.
  function restoreOpeningFocus() {
    const active = document.activeElement;
    if (!active || active === document.body || active === dialog || !dialog.contains(active)) focusName();
  }
  function removeRetryRow() {
    const row = dialog.querySelector('.inv-retry');
    if (row) row.remove();
    for (const note of dialog.querySelectorAll('.inv-field-err')) note.remove();
  }
  // An unresolved request keeps Check status. Editing must not drop it.
  function dropRetry() {
    if (pending || recovering) return;
    removeRetryRow();
  }
  function invalidateStatus() {
    statusGen += 1;
    statusAbort.abort();
    statusAbort = new AbortController();
  }
  function finishCreated(render) {
    invalidateStatus();
    pending = null;
    forget();
    clearSecrets();
    render();
    if (!createdNotified) {
      createdNotified = true;
      if (onCreated) onCreated();
    }
  }
  function holdHuman() {
    humanHeld = true;
    invalidateStatus();
  }
  function statusSignal() {
    return statusAbort.signal;
  }

  function renderFields() {
    const refocusKind = document.activeElement === kind;
    rendering = true;
    try { renderFieldsBody(); }
    finally { rendering = false; }
    if (refocusKind && kind.isConnected) kind.focus();
  }

  function kindOptionLabel(k) {
    const s = (registry.specs || []).find(x => x && x.kind === k);
    return (s && s.label) || kindLabel(k);
  }

  // Rebuilding the options on every kind change re-parents the select and
  // drops the arrow keys. Leave them in place while the catalogue is the same.
  function paintKindOptions(kinds) {
    const opts = [...kind.querySelectorAll('option')];
    const same = opts.length === kinds.length && kinds.every((k, i) => opts[i].value === k && opts[i].textContent === kindOptionLabel(k));
    if (same) return false;
    kind.replaceChildren(...kinds.map(k => h('option', { value: k, text: kindOptionLabel(k) })));
    return true;
  }

  const LEGACY_FIELDS = new Set(['name', 'name_open', 'enrol_ttl_seconds', 'principal_id', 'webhook_url', 'webhook_key', 'connector_name', 'skip_mcp']);

  function renderFieldsBody() {
    const kinds = kindsFrom(registry.specs);
    const previous = kind.value;
    const rebuilt = paintKindOptions(kinds);
    if (kinds.length) kindSlot.removeAttribute('hidden');
    else kindSlot.setAttribute('hidden', '');
    const applyPrefill = !usedPrefill && kinds.length;
    if (applyPrefill) {
      usedPrefill = true;
      if (prefill && prefill.kind && kinds.includes(prefill.kind)) kind.value = prefill.kind;
      else kind.value = kinds[0];
    } else if (rebuilt && previous && kinds.includes(previous)) kind.value = previous;
    else if (rebuilt && kinds[0]) kind.value = kinds[0];

    if (!kinds.length) {
      const kids = [
        h('p', { class: 'inv-loading', text: registry.error || 'Loading kinds…' }),
        h('label', { for: 'inv-name', text: 'Name' }),
        name,
      ];
      if (registry.phase === 'error') kids.push(h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => loadKinds() }));
      fieldsHost.replaceChildren(...kids);
      create.disabled = true;
      return;
    }
    const spec = currentSpec();
    const issue = spec && specIssue(spec);
    const action = spec ? spec.submit_action : '';
    if (action !== 'invite' && openName.checked) openName.checked = false;
    lede.textContent = spec?.kind === 'launcher' ? 'Install a launcher daemon on a host you control. Create its identity, then run the generated setup commands on that host.' : action === 'oauth_link' ? DOT_LEDE : action === 'grokbot_setup' ? GROK_LEDE : LEDE;
    create.textContent = gate.busy ? 'Creating…' : createLabel();
    create.disabled = recovering || !!gate.busy || registry.phase === 'loading';
    if (!spec || issue) {
      const why = issue && issue.code === 'unsupported_field'
        ? 'This page cannot enter the field ' + issue.name + '.'
        : 'This page cannot run that submit action.';
      fieldsHost.replaceChildren(h('p', { class: 'err', role: 'alert', text: why }));
      create.disabled = true;
      return;
    }

    if (applyPrefill) {
      const controls = { name, name_open: openName, enrol_ttl_seconds: ttl, principal_id: principal, connector_name: connector, skip_mcp: skip };
      for (const [field, value] of Object.entries(prefillValues(spec, prefill))) {
        const control = controls[field];
        if (control.type === 'checkbox') control.checked = value;
        else if (field !== 'name' || !name.value) {
          // MCP accepts any lifetime within the hub bounds, including ones
          // outside the three convenient choices in the ordinary form.
          if (field === 'enrol_ttl_seconds' && ![...ttl.querySelectorAll('option')].some(o => o.value === String(value))) {
            ttl.append(h('option', { value: String(value), text: value + ' seconds' }));
          }
          control.value = String(value);
        }
      }
    }

    const kids = [];
    const nameLabel = h('label', { for: 'inv-name', text: action === 'invite' && openName.checked ? 'Suggested name (optional)' : 'Name' });
    name.setAttribute('aria-required', action === 'invite' && openName.checked ? 'false' : 'true');
    if (action === 'grokbot_setup') {
      const prompt = h('pre', { id: 'inv-prompt', class: 'inv-prompt mono', tabindex: '0', 'aria-label': 'Authorization prompt' });
      prompt.textContent = authorizePrompt(spec.prompt_template, hubOrigin(), shownConnector());
      kids.push(prompt,
        copier('Copy prompt', () => prompt.textContent),
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
      kids.push(h('div', { class: 'inv-row' }, ttlCell));
    }
    if ((spec.fields || []).some(f => f.name === 'principal_id') && action !== 'grokbot_setup') {
      kids.push(h('label', { for: 'inv-principal', text: 'Existing principal (optional)' }), principal);
    }
    for (const f of spec.fields || []) {
      if (LEGACY_FIELDS.has(f.name)) continue;
      kids.push(...renderExtra(f));
    }
    if (spec.kind === 'launcher') {
      kids.push(h('p', { class: 'inv-note', text: 'Host-side setup: use absolute directories on the machine running the daemon. Requires vecnet, Node.js 22.18+, Git, a supported Codex CLI with an authenticated route and a running background app-server daemon, and a Linux systemd user service. Backend codex-app-server · preset default (inherits that route). ' + LAUNCHER_HOST_HELP }));
      for (const [label, control] of [['Project alias', setupProject], ['Repository working directory on host', setupRepo], ['Base ref', setupBase], ['Custom source checkout on host (optional pin)', setupSource]]) {
        kids.push(h('label', { for: control.id, text: label }), control);
      }
      kids.push(h('p', { class: 'inv-note', text: 'Leave custom source empty for automatic managed runtime upgrades. A custom checkout pins the runtime source and may require manual repair.' }));
    }
    fieldsHost.replaceChildren(...kids.filter(c => c != null && c !== false));
  }

  function helpNote(f) {
    return f && f.help ? h('p', { class: 'inv-note', 'data-help': f.name, text: f.help }) : null;
  }

  // renderExtra is one registry field this page does not already lay out.
  // Defaults, enum options, bounds and help come from that field.
  function renderExtra(f) {
    const id = 'inv-x-' + f.name;
    if (f.type === 'boolean') {
      const box = h('input', { id, type: 'checkbox', 'data-field': f.name });
      box.checked = f.default === true;
      return [h('label', { class: 'inv-chk', for: id }, box, f.name), helpNote(f)];
    }
    let control;
    if (f.type === 'enum') {
      const options = Array.isArray(f.enum) ? f.enum : (Array.isArray(f.options) ? f.options : []);
      const hasDefault = f.default !== undefined && f.default !== null && String(f.default) !== '';
      const opts = [];
      if (!hasDefault) opts.push(h('option', { value: '', text: 'Choose…' }));
      for (const opt of options) {
        opts.push(h('option', {
          value: String(opt), text: String(opt),
          selected: hasDefault && String(opt) === String(f.default),
        }));
      }
      control = h('select', { id, 'data-field': f.name }, opts);
      if (hasDefault) control.value = String(f.default);
    } else if (f.type === 'integer') {
      control = h('input', {
        id, type: 'number', 'data-field': f.name, step: '1',
        min: f.minimum != null ? String(f.minimum) : false,
        max: f.maximum != null ? String(f.maximum) : false,
      });
      if (f.default != null && f.default !== '') control.value = String(f.default);
    } else {
      const secret = f.type === 'secret';
      control = h('input', {
        id, type: secret ? 'password' : 'text', 'data-field': f.name,
        'data-secret': secret || f.human_only ? 'true' : false,
        spellcheck: 'false', autocomplete: 'off',
        minlength: f.min_length != null ? String(f.min_length) : false,
        maxlength: f.max_length != null ? String(f.max_length) : false,
      });
      if (!secret && f.default != null && f.default !== '') control.value = String(f.default);
    }
    return [h('label', { for: id, text: f.name }), control, helpNote(f)];
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
    if (pending || recovering) {
      rendering = true;
      try { kind.value = pending ? pending.body.kind : prefill.kind; }
      finally { rendering = false; }
      return;
    }
    clearSecrets();
    err.textContent = '';
    dropRetry();
    renderFields();
  });

  async function loadKinds() {
    const ticket = life;
    registry = beginLoad(registry);
    const generation = registry.generation;
    renderFields();
    restoreOpeningFocus();
    try {
      const body = await ctx.api.agentKinds({ signal: ac.signal });
      let ident = null;
      try { ident = await ctx.api.hubIdentity({ signal: ac.signal }); }
      catch { ident = null; }
      if (!acceptCompletion(life, ticket, ac.signal.aborted)) return;
      if (ident && ident.public_url) hubURL = String(ident.public_url).replace(/\/+$/, '');
      registry = applyLoad(registry, generation, Array.isArray(body.kinds) ? body.kinds : []);
    } catch (e) {
      if (!acceptCompletion(life, ticket, ac.signal.aborted)) return;
      registry = failLoad(registry, generation, presentError(e, hidden()).text);
    }
    if (!acceptCompletion(life, ticket, ac.signal.aborted)) return;
    renderFields();
    if (recovering) {
      removeRetryRow();
      err.textContent = 'Check the previous request before creating another agent.';
      showReconciliation('recovery');
    }
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

  function showReconciliation(outcome) {
    const buttons = [];
    if (outcome === 'uncertain') {
      buttons.push(h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => send() }));
    }
    buttons.push(h('button', { type: 'button', class: 'btn', text: 'Check status', onclick: () => checkStatus() }));
    const row = h('div', { class: 'inv-retry' },
      h('p', { class: 'inv-req mono', text: 'Request ' + requestId }),
      ...buttons);
    err.after(row);
    const retry = row.querySelector('button');
    if (retry) retry.focus();
  }

  async function send() {
    if (recovering) return;
    const next = submitGate(gate);
    if (!next.accepted) return;
    gate = next;
    removeRetryRow();
    invalidateStatus();
    const ticket = life;
    const spec = pending
      ? pending.spec || (registry.specs || []).find(s => s && s.kind === pending.body.kind) || null
      : currentSpec();
    if (!spec || specIssue(spec)) {
      gate = { busy: false };
      err.textContent = 'This page cannot submit that kind.';
      create.disabled = true;
      return;
    }
    let vals = pending ? pending.values : readValues();
    let bodyReq = pending ? pending.body : null;
    if (!pending) {
      if (spec.kind === 'launcher') {
        vals.launcher_setup = readSetup();
        const issue = launcherSetupIssue(vals.launcher_setup);
        if (issue) { gate = { busy: false }; err.textContent = issue; return; }
      }
      const issues = clientIssues(spec, vals);
      const first = Object.keys(issues)[0];
      if (first) {
        gate = { busy: false };
        err.textContent = issues[first];
        markField(first, issues[first]);
        return;
      }
      try { bodyReq = submitBody(spec, vals, requestId); }
      catch (e) {
        gate = { busy: false };
        err.textContent = presentError(e, hidden()).text;
        return;
      }
    }
    err.textContent = '';
    create.disabled = true;
    create.textContent = 'Creating…';
    remember(bodyReq.kind);
    try {
      const res = await ctx.api.agentAdd(bodyReq, { signal: ac.signal });
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      const submitted = vals;
      finishCreated(() => {
        if (spec.submit_action === 'oauth_link') showDot(res, submitted, spec);
        else if (spec.submit_action === 'grokbot_setup') showGrok(res, submitted);
        else showInvite(res, submitted);
      });
    } catch (x) {
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      gate = { busy: false };
      create.disabled = registry.phase === 'loading' ? true : false;
      create.textContent = createLabel();
      const outcome = outcomeOf(x);
      if (outcome === 'stale') {
        forget();
        pending = null;
        clearSecrets();
        err.textContent = 'The kind contract changed. Review the form and submit again.';
        await loadKinds();
        focusReentry();
        return;
      }
      const shown = presentError(x, hidden());
      err.textContent = shown.text;
      markField(shown.field, shown.text);
      if (outcome === 'uncertain' || outcome === 'conflict') {
        if (!pending) pending = { body: bodyReq, values: vals, spec };
        showReconciliation(outcome);
        return;
      }
      forget();
      pending = null;
    }
  }

  async function checkStatus() {
    invalidateStatus();
    const ticket = life;
    const mine = statusGen;
    try {
      const res = await ctx.api.onboardingStatus(requestId, { signal: statusSignal() });
      if (mine !== statusGen || humanHeld) return;
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      finishCreated(() => showSafe(statusSafe(res), 'Status for this request. One-time text and routine secrets are not included.'));
    } catch (e) {
      if (mine !== statusGen || humanHeld) return;
      if (!acceptCompletion(life, ticket, ac.signal.aborted) || !dialog.isConnected) return;
      if (e && (e.name === 'AbortError' || e.code === 'aborted')) return;
      if (e && e.code === 'not_found') {
        // A closed POST may still be committing. Keep the id across another
        // close/reopen, and reuse it if the human re-enters the form.
        recovering = false;
        pending = null;
        create.disabled = false;
        removeRetryRow();
        err.textContent = 'No committed request for this id yet.';
        return;
      }
      err.textContent = presentError(e, hidden()).text;
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
    if (vals.launcher_setup) { showLauncher(res, output, vals.launcher_setup); return; }
    holdHuman();
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

  function showLauncher(res, output, setup) {
    holdHuman();
    lede.textContent = 'Run these commands in a terminal on the launcher host.';
    const block = h('pre', { class: 'inv-block mono', tabindex: '0', 'aria-label': 'Launcher installation commands' });
    try { block.textContent = launcherSetupCommands(output, setup); }
    catch (x) { block.textContent = output; err.textContent = x.message; }
    const profile = (output.match(/--profile ([A-Za-z0-9._-]+)/) || [])[1] || 'PROFILE';
    const recovery = h('pre', { class: 'inv-block mono', tabindex: '0', 'aria-label': 'Launcher host recovery commands' });
    recovery.textContent = launcherHostRecovery(profile);
    body.replaceChildren(
      h('p', { class: 'inv-warn', role: 'note', text: 'Shown once — the enrolment command grants this launcher identity. Copy only to the host you control.' }),
      h('p', { class: 'inv-note', text: 'First ensure the supported Codex background app-server daemon is running with the same configured CODEX_HOME and authenticated route. Then: 1. Enrol the launcher identity. 2. Write its private project configuration. 3. Install and start the launcher service.' }),
      block,
      h('p', { class: 'inv-note', text: LAUNCHER_HOST_HELP }),
      recovery,
      h('p', { class: 'inv-note', text: 'After launcher check passes and the launcher reports healthy presence, open its peer page to launch a job using its advertised project alias. Creating this identity alone does not start either daemon.' }),
      h('div', { class: 'inv-acts' },
        h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() }),
        h('a', { class: 'btn', href: '#peer/' + res.peer_id, text: 'Open launcher', onclick: () => close() }),
        copier('Copy setup commands', () => block.textContent, 'pri')));
  }

  function showDot(res, vals, submitted) {
    const d = linkPanel({ ...res, name: vals.name }, location.origin, hubURL);
    const slot = h('div', { class: 'inv-code', 'aria-live': 'polite' });
    if (d.code) {
      holdHuman();
      fillCode(slot, { code: d.code, ...firstLinkExpiry(res) }, d.peerId, false);
    } else slot.append(h('p', { class: 'inv-note', text: 'This request already completed. The one-time link code is not shown again.' }));
    paintDot(d, slot, submitted);
  }

  // Bold the words a person has to find on the ChatGPT screen, wherever the
  // hub prompt places them. The sentences themselves stay the hub's.
  function lineNodes(line) {
    const marks = ['Add custom MCP server', 'Same chat for every run', 'vecnet.message', 'vecnet_guide', 'Plugins', 'OAuth', '+'];
    const parts = [];
    let rest = String(line);
    while (rest) {
      let at = -1, which = '';
      for (const m of marks) {
        const i = rest.indexOf(m);
        if (i >= 0 && (at < 0 || i < at || (i === at && m.length > which.length))) { at = i; which = m; }
      }
      if (at < 0) { parts.push(rest); break; }
      if (at > 0) parts.push(rest.slice(0, at));
      parts.push(h('b', { text: which }));
      rest = rest.slice(at + which.length);
    }
    return parts;
  }

  function paintDot(d, slot, submitted) {
    // The steps belong to the spec that was submitted. A later kind change,
    // including one made while the request was in flight, does not replace them.
    const panel = dotInstructions(submitted && submitted.prompt_template);
    const lines = panel.steps.length ? panel.steps : [''];
    const codeStep = panel.steps.length ? panel.codeStep : 0;
    const steps = lines.map((line, i) => h('li', null,
      h('span', null, lineNodes(line)),
      i === codeStep ? slot : null));
    const urlBtn = copier('Copy URL', d.mcpURL, 'pri');
    const prompt = h('pre', { id: 'inv-dot-prompt', class: 'inv-prompt mono', tabindex: '0', 'aria-label': 'Dot setup prompt',
      text: 'Use vecnet_guide to finish setting up incoming messages in this chat. Create one task with trigger vecnet, event vecnet.message and no settings. Under Advanced, set Runs in: Same chat for every run. Use the guide\'s recommended task instruction.' });
    const feedback = h('div', { role: 'status', 'aria-live': 'polite', 'aria-atomic': 'true' });
    lede.textContent = 'Connect ' + (d.name || 'the dot') + ' from ChatGPT.';
    body.replaceChildren(...[
      h('p', { class: 'inv-warn', role: 'note', text: 'Shown once — the link code binds a ChatGPT login to this dot. Give it only to whoever sets up the plugin.' }),
      h('label', { class: 'inv-sub', for: 'inv-mcp', text: 'MCP server URL' }),
      h('div', { class: 'inv-url-row' }, h('input', { id: 'inv-mcp', class: 'inv-url mono', readonly: true, value: d.mcpURL, spellcheck: 'false', onfocus: e => e.target.select() }), urlBtn),
      h('ol', { class: 'inv-steps' }, steps),
      panel.note ? h('p', { class: 'inv-note', text: panel.note }) : null,
      h('p', { class: 'inv-note', text: 'After connecting the plugin, paste this prompt into the Dot:' }),
      prompt,
      h('p', { class: 'inv-meta mono', text: 'ChatGPT dot · peer ' + d.peerId }),
      h('div', { class: 'inv-acts' }, h('button', { type: 'button', class: 'btn', text: 'Done', onclick: () => close() }), copier('Copy prompt', () => prompt.textContent, 'pri', feedback)),
      feedback,
    ].filter(Boolean));
    urlBtn.focus();
  }

  function fillCode(slot, link, peerId, refocus) {
    const copy = copier('Copy code', link.code);
    const expiry = link.expires_at
      ? [' · expires ', h('time', { datetime: link.expires_at, title: link.expires_at, text: when(link.expires_at) })]
      : link.lifetime
        ? [' · expires in ', h('time', { text: link.lifetime })]
        : null;
    const row = h('p', { class: 'inv-meta mono' }, 'single use',
      expiry,
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

  // The sticky footer covers the bottom of the scrollport. scroll-padding-bottom
  // tells Tab to stop the focused field above it. The height follows the
  // footer, including the error and Retry row. This is a CSSOM write.
  let footObserver = null;
  function syncFootPadding() {
    if (!body.style) return;
    const rect = typeof foot.getBoundingClientRect === 'function' ? foot.getBoundingClientRect() : null;
    const height = rect && rect.height ? rect.height : (foot.offsetHeight || 0);
    body.style.scrollPaddingBottom = (height + 8) + 'px';
  }
  if (typeof ResizeObserver === 'function') {
    footObserver = new ResizeObserver(() => syncFootPadding());
    footObserver.observe(foot);
  }

  function close() {
    if (open !== close) return;
    open = null;
    if (footObserver) { footObserver.disconnect(); footObserver = null; }
    life += 1;
    ac.abort();
    statusAbort.abort();
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
  syncFootPadding();
  focusName();
  loadKinds();
}
