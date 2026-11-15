// Settings screen (#settings, plan 2026-11-05 Task 6): Max's selected design,
// List (variant A, Ledger) by default with a header toggle to Policy (variant
// D). Both views draw one settings-model store, so edits, errors, source
// badges, Save/Undo and confirmations carry across a switch. Every hub string
// goes in as text through dom.h; nothing here parses markup.
//
// Hub rows whose consumer adapter has not landed are drawn as unavailable,
// never as working controls. Appearance is this browser's own and has its own
// Apply; it never sends a request.
import { h } from './dom.js';
import {
  BLOB_NEW_ONLY, GROUPS, LANES, TICKS, blobOffText, bytesText, createSettingsStore, durationText, enumLabel, eventRetentionNote, formatValue, laneView, lanePosition, lockOf,
  formatDuration, grokPresencePolicy, previewRefusal, rangeText, storageView, timingOf, unitOf, visibleRows,
} from './settings-model.js';

let current = null;

const SOURCE_TIP = {
  default: 'Built into the hub.',
  flag: 'Set by a hub flag or environment variable at start-up. Read-only here.',
  stored: 'Saved by an operator, here or with vecnet prefs.',
  edited: 'Changed here, not saved yet.',
};
const LOCK_CHIP = { unavailable: 'unavailable', readonly: 'read-only' };
const DANGER_NOTE = {
  presence_shortened: 'Shortening asks for confirmation first: it may release held wakes.',
  archive_disabled: 'Turning it off asks for confirmation. Existing history stays protected unless you approve that separately.',
  retention_shortened: 'A new or shorter limit asks for confirmation and applies to new rows. Existing data needs a separate approval.',
  blob_expiry_off: 'Off asks for confirmation with the hub\'s storage figures.',
};
const GROUP_NOTE = {
  retention_storage: 'Archive mode protects message history from expiry. Scheduled acknowledged-event pruning also requires its rollout gate; archive mode does not pause that policy. Set it to 0 to keep them too. Archive mode is not an export or a backup: see Backups in docs/DEPLOY.md.',
  files: 'An attachment expires at its recorded deadline, checked every 30 seconds; the storage provider may remove the bytes later. Existing attachments keep their deadlines.',
  agents_defaults: 'These apply to newly created agents, messages and threads. Existing ones, and explicit per-agent limits, are unchanged.',
};

const safeStorage = () => { try { return window.localStorage; } catch { return null; } };
const sid = key => 'st-' + key.replace(/[^A-Za-z0-9]/g, '-');
const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;

const presenceOverride = row => row && ['presence.default_ttl', 'presence.grokbot_ttl'].includes(row.key) && !lockOf(row);

function chip(source, row) {
  if (!source) return null;
  const tip = source === 'flag' && presenceOverride(row) ? 'Set when the hub started. A stored override takes precedence.' : SOURCE_TIP[source];
  return h('span', { class: 'st-src s-' + source, title: tip, text: source });
}
function lockChip(lock) {
  return lock && LOCK_CHIP[lock.kind] ? h('span', { class: 'st-src s-' + lock.kind, text: LOCK_CHIP[lock.kind] }) : null;
}
const bytes = bytesText;

// storagePanel: the hub's R2 attachment storage, operator-only figures from
// the Settings snapshot (retained + reserved = used, against a finite limit).
// Full or over the limit stops new uploads; nothing is deleted to make room.
function storagePanel(storage) {
  const v = storageView(storage);
  if (!v) return null;
  const share = n => Math.max(0, Math.min(100, (n / Math.max(v.limit_bytes, v.used_bytes)) * 100)) + '%';
  const ret = h('i', { class: 'ret' }), res = h('i', { class: 'res' });
  ret.style.width = share(v.retained_bytes);
  res.style.width = share(v.reserved_bytes);
  return h('div', { class: 'st-sto t-' + v.tone, 'data-storage': v.tone },
    h('div', { class: 'st-sto-h' }, h('b', { text: 'Attachment storage' }), h('span', { class: 'st-sto-t', text: `${v.text} · ${v.percent}%` })),
    h('div', { class: 'st-sto-bar', role: 'meter', 'aria-label': 'Attachment storage used', 'aria-valuemin': '0', 'aria-valuemax': '100', 'aria-valuenow': String(v.percent), 'aria-valuetext': v.text }, ret, res),
    h('dl', { class: 'st-sto-f' }, ...v.parts.flatMap(p => [h('dt', { text: p.label }), h('dd', { text: p.value })])),
    h('p', { class: 'st-sto-n', text: v.note, hidden: !v.note }),
    h('p', { class: 'st-dim st-sto-d', text: 'Counts every retention class, kept files included. Reserved is space held for uploads in flight. Purging an attachment (POST /v1/admin/blobs/{sha256}/purge) frees its space; nothing is deleted to make room.' }));
}

// A meter-only refresh must leave controls (including an open native select)
// attached. Re-focusing a replacement cannot preserve that browser state.
function updateStoragePanel(panel, storage) {
  const v = storageView(storage);
  if (!panel || !v) return;
  panel.className = 'st-sto t-' + v.tone;
  panel.setAttribute('data-storage', v.tone);
  panel.querySelector('.st-sto-t').textContent = `${v.text} · ${v.percent}%`;
  const meter = panel.querySelector('.st-sto-bar');
  meter.setAttribute('aria-valuenow', String(v.percent));
  meter.setAttribute('aria-valuetext', v.text);
  const share = n => Math.max(0, Math.min(100, (n / Math.max(v.limit_bytes, v.used_bytes)) * 100)) + '%';
  meter.querySelector('.ret').style.width = share(v.retained_bytes);
  meter.querySelector('.res').style.width = share(v.reserved_bytes);
  const figures = panel.querySelector('.st-sto-f').children;
  v.parts.forEach((p, i) => { figures[i * 2 + 1].textContent = p.value; });
  const note = panel.querySelector('.st-sto-n');
  note.textContent = v.note;
  note.hidden = !v.note;
}

// control: the editor for one hub row, shared by List rows and Policy drawers.
function control(store, row, id) {
  const st = store.state, lock = lockOf(row), off = !!lock || !!st.busy;
  const err = store.errors()[row.key], v = store.value(row.key);
  const described = [id + '-h', err ? id + '-e' : '', lock ? id + '-l' : ''].filter(Boolean).join(' ');
  if (row.type === 'bool') {
    return h('button', { type: 'button', class: 'st-tg', role: 'switch', id, 'aria-checked': String(!!v), 'aria-describedby': described, 'data-fk': id, disabled: off, onclick: () => store.edit(row.key, !v) });
  }
  if (row.type === 'enum') {
    const sel = h('select', { class: 'st-sel', id, 'aria-describedby': described, 'data-fk': id, disabled: off, onchange: e => store.edit(row.key, e.target.value) },
      ...(row.allowed_values || []).map(o => h('option', { value: o, text: enumLabel(o) })));
    sel.value = v;
    return sel;
  }
  const input = h('input', {
    type: 'text', id, class: 'st-in', inputmode: row.type === 'int' ? 'numeric' : 'text', spellcheck: 'false', autocomplete: 'off',
    'aria-invalid': err ? 'true' : 'false', 'aria-describedby': described, 'data-fk': id, disabled: off,
    oninput: e => store.edit(row.key, e.target.value),
  });
  input.value = store.input(row.key);
  const dirty = store.source(row.key) === 'edited';
  const hint = row.type === 'duration' ? (dirty && !err ? '= ' + formatValue(row, v) : unitOf(row)) : unitOf(row);
  return h('span', { class: 'st-num' + (err ? ' is-bad' : '') }, input, hint ? h('span', { class: 'u', text: hint }) : null);
}

// resetLink: Revert drops an unsaved edit; Reset turns a stored override into
// a pending reset whose fallback shows before Save.
function resetLink(store, row) {
  const st = store.state, d = st.draft.get(row.key);
  if (lockOf(row) || st.busy) return null;
  if (d && d.op === 'reset') return h('button', { type: 'button', class: 'linkbtn st-rst', 'data-fk': 'rst-' + row.key, text: 'Keep stored value', onclick: () => store.revert(row.key) });
  // Under a stored override the page cannot see the start-up layer (built-in
  // or a hub flag), so the label names the layer, not a value.
  if (row.source === 'stored') return h('button', { type: 'button', class: 'linkbtn st-rst', 'data-fk': 'rst-' + row.key, text: 'Reset to start-up value', title: presenceOverride(row) ? 'Removes only the stored override. The hub reply reveals the starting value and Grok inheritance.' : `Removes the stored override: built-in ${formatValue(row, row.default)} unless a hub flag sets it`, onclick: () => store.resetKey(row.key) });
  if (d) return h('button', { type: 'button', class: 'linkbtn st-rst', 'data-fk': 'rst-' + row.key, text: 'Revert to ' + formatValue(row, row.value), onclick: () => store.revert(row.key) });
  return null;
}

function storeOverride(store, row) {
  if (!presenceOverride(row) || !['default', 'flag'].includes(row.source)) return null;
  return h('button', {
    type: 'button', class: 'linkbtn', 'data-fk': 'store-' + row.key, text: 'Store override', disabled: !!store.state.busy,
    onclick: () => store.edit(row.key, store.input(row.key)),
  });
}

function grokPolicy(store) {
  const policy = grokPresencePolicy(store.state.rows);
  if (!policy) return null;
  const inheritance = policy.key === 'presence.default_ttl' ? 'inherited from' : 'controlled by';
  return h('p', { class: 'st-meta', 'data-fk': 'grok-current', text:
    `Current Grok presence: ${formatDuration(policy.seconds)}; ${inheritance} ${policy.key} (${policy.source}${policy.origin ? ', ' + policy.origin : ''}). Reports older than this TTL are unknown; never-reported presence is already unknown.` });
}

function metaText(row) {
  const parts = [timingOf(row)];
  if (row.type === 'int' || row.type === 'duration') parts.push(rangeText(row).replace(/^./, c => c.toUpperCase()));
  parts.push('built-in ' + formatValue(row, row.default));
  return parts.join(' · ');
}

// details: the explanatory lines shared by a List row and a Policy drawer.
function details(store, row, id) {
  const st = store.state, lock = lockOf(row), err = store.errors()[row.key], d = st.draft.get(row.key);
  const danger = (row.danger || []).map(c => DANGER_NOTE[c]).filter(Boolean)[0];
  return [
    h('p', { class: 'st-h', id: id + '-h', text: row.description || '' }),
    row.key === 'retention.ack_events' ? retentionNote(st.eventRetentionEnabled) : null,
    row.key === 'blobs.expiry' ? h('p', { class: 'st-blob-new', text: BLOB_NEW_ONLY }) : null,
    h('p', { class: 'st-meta', text: metaText(row) }),
    presenceOverride(row) ? h('p', { class: 'st-meta', text:
      (row.source === 'flag' ? `Starting value from ${row.origin || 'a hub flag or environment variable'}. ` : '') +
      'A stored value takes precedence. Store override stages the displayed value; Save applies it after any required confirmation. Reset removes only a stored override and restores the hub starting value. An unset Grok TTL inherits an explicit default presence TTL.' }) : null,
    row.key === 'presence.grokbot_ttl' && st.view === 'list' ? grokPolicy(store) : null,
    lock ? h('p', { class: 'st-lock ' + lock.kind, id: id + '-l', text: lock.text }) : null,
    d && d.op === 'reset' ? h('p', { class: 'st-pend', text: presenceOverride(row)
      ? "Removes the stored override on Save. The hub's start-up value returns; its reply reveals that value and Grok inheritance. Current policy stays unchanged until the hub accepts Save."
      : `Removes the stored override on Save. The hub's start-up value returns: built-in ${formatValue(row, row.default)}, unless a hub flag or environment variable sets it.` }) : null,
    danger && !lock ? h('p', { class: 'st-danger', text: danger }) : null,
    err ? h('p', { class: 'st-err', id: id + '-e', text: err }) : null,
  ];
}

function listRow(store, row, flash) {
  const id = sid(row.key), lock = lockOf(row), src = store.source(row.key), err = store.errors()[row.key];
  const cls = ['st-row', src === 'edited' && 'is-dirty', err && 'is-err', lock && 'is-locked', flash && flash.has(row.key) && 'is-flash'].filter(Boolean).join(' ');
  return h('div', { class: cls, 'data-row': row.key },
    h('div', { class: 'st-l' },
      h('div', { class: 'st-t' }, h('label', { for: id, class: 'st-lbl', text: row.label }), chip(src, row), lockChip(lock)),
      ...details(store, row, id)),
    h('div', { class: 'st-c' }, control(store, row, id), storeOverride(store, row), resetLink(store, row), h('span', { class: 'st-key', text: row.key })),
    // The storage meter spans the row, under the label and its selector.
    row.key === 'blobs.expiry' ? storagePanel(store.state.storage) : null);
}

// ---------- appearance (local) ----------

const THEME_OPTS = [['system', 'System'], ['dark', 'Dark'], ['light', 'Light']];
const DENSITY_OPTS = [['comfortable', 'Comfortable'], ['compact', 'Compact']];
const LOOK_HELP = {
  theme: 'System follows this device\'s light or dark setting. Light inverts the dark palette until a dedicated light palette exists.',
  density: 'Compact tightens spacing in the sidebar, lists and Settings.',
};
function lookControl(store, field, opts, label) {
  const a = store.state.appearance;
  return h('span', { class: 'st-seg', role: 'group', 'aria-label': label },
    ...opts.map(([v, l]) => h('button', {
      type: 'button', class: a.draft[field] === v ? 'on' : '', 'aria-pressed': String(a.draft[field] === v), 'data-fk': `look-${field}-${v}`,
      onclick: () => store.setAppearance(field, v), text: l,
    })));
}
function lookBar(store) {
  const a = store.state.appearance, dirty = store.appearanceDirty();
  return h('div', { class: 'st-local' + (dirty ? ' is-dirty' : '') },
    h('span', { class: 'st-local-m', text: dirty ? 'Not applied yet. Hub Save does not include appearance.' : 'Applied to this browser.' }),
    h('span', { class: 'st-acts' },
      h('button', { type: 'button', class: 'btn', 'data-fk': 'look-undo', disabled: !dirty, text: 'Undo', onclick: () => store.undoAppearance() }),
      h('button', { type: 'button', class: 'btn pri', 'data-fk': 'look-apply', disabled: !dirty, text: 'Apply to this browser', onclick: () => store.applyAppearance() })),
    a.note ? h('p', { class: 'st-lock', text: a.note }) : null);
}
function lookRow(store, field, label, opts) {
  const a = store.state.appearance, dirty = a.draft[field] !== a.saved[field];
  return h('div', { class: 'st-row' + (dirty ? ' is-dirty' : ''), 'data-row': 'look.' + field },
    h('div', { class: 'st-l' },
      h('div', { class: 'st-t' }, h('span', { class: 'st-lbl', text: label }), h('span', { class: 'st-src s-' + (dirty ? 'edited' : 'local'), text: dirty ? 'edited' : 'this browser' })),
      h('p', { class: 'st-h', text: LOOK_HELP[field] })),
    h('div', { class: 'st-c' }, lookControl(store, field, opts, label), h('span', { class: 'st-key', text: 'vecnet.ui.appearance.v1' })));
}
function appearanceSection(store) {
  const g = GROUPS.find(x => x.id === 'appearance');
  return h('section', { class: 'st-sec', id: 'sec-appearance', 'aria-labelledby': 'sec-appearance-h' },
    h('h2', { id: 'sec-appearance-h' }, g.title, h('span', { class: 'st-you', text: 'only this browser' })),
    h('p', { class: 'st-blurb', text: g.blurb }),
    h('div', { class: 'st-rows' }, lookRow(store, 'theme', 'Theme', THEME_OPTS), lookRow(store, 'density', 'Density', DENSITY_OPTS)),
    lookBar(store));
}

// ---------- List view ----------

function listBody(store, flash) {
  const st = store.state, shown = new Set(visibleRows(store).map(r => r.key));
  const nCustom = st.rows.filter(r => r.source === 'stored' || store.source(r.key) === 'edited').length;
  const nFlag = st.rows.filter(r => r.source === 'flag').length;
  const filtering = st.q.trim() || st.only !== 'all';
  const archiveKeys = new Set(['agents.auto_archive_after', 'agents.auto_archive_all_kinds', 'threads.auto_archive_share']);
  const groups = GROUPS.filter(g => !g.local).flatMap(g => g.id === 'agents_defaults' ? [g, { id: 'automatic_archiving', title: 'Automatic archiving', blurb: 'Live rules act on existing agents and threads hourly. Agent archive revokes credentials; after unarchiving, issue a new invite. Thread archive only tidies the operator view.' }] : [g]);
  const sections = groups.map(g => {
    const rows = st.rows.filter(r => (g.id === 'automatic_archiving' ? archiveKeys.has(r.key) : r.group === g.id && !archiveKeys.has(r.key)) && shown.has(r.key));
    if (!rows.length && filtering) return null;
    return h('section', { class: 'st-sec', id: 'sec-' + g.id, 'aria-labelledby': `sec-${g.id}-h` },
      h('h2', { id: `sec-${g.id}-h`, text: g.title }),
      h('p', { class: 'st-blurb', text: g.blurb }),
      GROUP_NOTE[g.id] && !filtering ? h('p', { class: 'st-gnote', text: GROUP_NOTE[g.id] }) : null,
      g.id === 'notifications_presence' && !filtering ? h('p', { class: 'st-gnote' }, 'Webhook subscriptions have their own health view on the ', h('a', { href: '#dash', text: 'Dashboard' }), '.') : null,
      rows.length ? h('div', { class: 'st-rows' }, ...rows.map(r => listRow(store, r, flash))) : h('p', { class: 'st-none', text: 'This hub exposes no settings in this group.' }));
  }).filter(Boolean);
  const lookShown = !st.q.trim() || /appear|theme|dens|dark|light|compact/i.test(st.q);
  return h('div', { class: 'st-list' },
    h('div', { class: 'st-tools' },
      h('input', { class: 'st-q', type: 'search', placeholder: 'Search settings', 'aria-label': 'Search settings', 'data-fk': 'q', value: st.q, oninput: e => store.setFilter({ q: e.target.value }) }),
      h('span', { class: 'st-seg st-only', role: 'group', 'aria-label': 'Show' },
        ...[['all', 'All'], ['custom', `Customised ${nCustom}`], ['flags', `Flags ${nFlag}`]].map(([k, l]) => h('button', {
          type: 'button', class: st.only === k ? 'on' : '', 'aria-pressed': String(st.only === k), 'data-fk': 'only-' + k, text: l, onclick: () => store.setFilter({ only: k }),
        }))),
      h('button', { type: 'button', class: 'btn st-reload', 'data-fk': 'reload', disabled: !!st.busy, text: 'Reload', onclick: () => store.refresh() })),
    ...sections,
    filtering && !sections.length ? h('div', { class: 'empty' }, h('b', { text: 'No settings match.' }), 'Try another word, or clear the search.') : null,
    st.only === 'all' && lookShown ? appearanceSection(store) : null);
}

function listAside(store) {
  const st = store.state, errs = store.errors(), dirty = new Set(store.dirtyKeys());
  const toc = GROUPS.map(g => {
    const keys = g.local ? [] : st.rows.filter(r => r.group === g.id).map(r => r.key);
    const e = keys.filter(k => errs[k]).length, n = g.local ? (store.appearanceDirty() ? 1 : 0) : keys.filter(k => dirty.has(k)).length;
    return h('button', { type: 'button', class: 'st-toc-a', onclick: () => { const el = document.getElementById('sec-' + g.id); if (el) { el.scrollIntoView({ block: 'start' }); const f = el.querySelector('input,select,button'); if (f) f.focus({ preventScroll: true }); } } },
      h('span', { text: g.title }), e ? h('span', { class: 't-e', text: String(e), 'aria-label': plural(e, 'error') }) : n ? h('span', { class: 't-n', text: String(n), 'aria-label': plural(n, 'unsaved change') }) : null);
  });
  const legend = ['default', 'flag', 'stored', 'edited'].flatMap(s => [h('dt', {}, chip(s)), h('dd', { text: SOURCE_TIP[s] + (s === 'flag' ? ' Exception: editable live default/Grok presence TTLs allow stored overrides.' : '') })]);
  return [
    h('nav', { class: 'st-toc', 'aria-label': 'On this page' }, h('div', { class: 'st-toc-h', text: 'On this page' }), ...toc),
    h('div', { class: 'st-toc-h', text: 'Where values come from' }),
    h('dl', { class: 'st-legend' }, ...legend,
      h('dt', {}, h('span', { class: 'st-src s-unavailable', text: 'unavailable' })), h('dd', { text: 'Its consumer is not installed on this hub yet; it cannot be changed.' }),
      h('dt', {}, h('span', { class: 'st-src s-readonly', text: 'read-only' })), h('dd', { text: 'Fixed when the hub starts.' })),
  ];
}

// ---------- Policy view ----------

function retentionNote(enabled) {
  return h('p', { class: 'st-meta', 'data-event-retention': enabled === false ? 'disabled' : enabled === true ? 'enabled' : 'not-reported', text: eventRetentionNote(enabled) });
}

function laneEl(store, lane, focusFor) {
  const st = store.state, v = laneView(store, lane);
  if (!v) return null;
  const src = store.source(lane.key), err = store.errors()[lane.key], off = !!v.lock || v.paused || !!st.busy;
  const pick = s => store.edit(lane.key, v.row.type === 'enum' ? s.value : s.value === 0 ? '0' : durationText(s.value));
  const fill = h('div', { class: 'st-fill' });
  fill.style.width = v.position + '%';
  const knob = h('span', { class: 'st-knob', 'aria-hidden': 'true' });
  knob.style.left = v.position + '%';
  const stops = v.stops.map((s, i) => {
    const b = h('button', {
      type: 'button', class: 'st-stop' + (s.on ? ' on' : ''), role: 'radio', 'aria-checked': String(s.on), tabindex: s.on || (!v.stops.some(x => x.on) && i === 0) ? '0' : '-1',
      'aria-label': `${lane.label}: ${s.label}`, title: s.label, 'data-fk': `lane-${lane.key}-${s.value}`, disabled: off,
      onclick: () => pick(s),
      onkeydown: e => {
        const at = v.stops.findIndex(x => x.on), last = v.stops.length - 1;
        const to = { ArrowRight: at + 1, ArrowUp: at + 1, ArrowLeft: at - 1, ArrowDown: at - 1, Home: 0, End: last }[e.key];
        if (to === undefined) return;
        e.preventDefault();
        const next = v.stops[Math.max(0, Math.min(last, to))];
        focusFor.key = `lane-${lane.key}-${next.value}`;
        pick(next);
      },
    });
    b.style.left = s.position + '%';
    return b;
  });
  const track = h('div', { class: 'st-track', role: 'radiogroup', 'aria-label': `${lane.label}: how long the hub keeps them`, 'aria-disabled': off ? 'true' : null, 'aria-describedby': v.lock ? sid(lane.key) + '-ll' : null }, fill, ...stops, knob);
  const cls = ['st-lane', v.paused && 'is-paused', v.seconds === Infinity && 'is-inf', src === 'edited' && 'is-dirty', v.lock && 'is-locked', lane.key === 'blobs.expiry' && v.value === 'off' && 'is-warn'].filter(Boolean).join(' ');
  return h('div', { class: cls, 'data-row': lane.key },
    h('div', { class: 'st-ll' }, h('b', { text: lane.label }), chip(src), lockChip(v.lock), h('span', { class: 'st-val', text: v.text })),
    track,
    lane.key === 'retention.ack_events' ? retentionNote(st.eventRetentionEnabled) : null,
    v.lock ? h('p', { class: 'st-lock-s ' + v.lock.kind, id: sid(lane.key) + '-ll', text: v.lock.text }) : null,
    err ? h('p', { class: 'st-err', text: err }) : null);
}

function archivePill(store) {
  const row = store.rowOf('retention.archive_mode');
  if (!row) return null;
  const on = store.value(row.key) === true, lock = lockOf(row), src = store.source(row.key);
  return h('button', {
    type: 'button', class: 'st-arch' + (on ? ' on' : '') + (lock ? ' is-locked' : ''), role: 'switch', 'aria-checked': String(on), id: sid(row.key),
    'data-fk': sid(row.key), disabled: !!lock || !!store.state.busy, onclick: () => store.edit(row.key, !on),
    'aria-label': 'Archive mode', 'aria-describedby': lock ? sid(row.key) + '-al' : null,
  },
  h('span', { class: 'st-inf', 'aria-hidden': 'true', text: '∞' }),
  h('span', {}, h('b', { text: `Archive mode ${on ? 'on' : 'off'}` }), h('small', { text: on ? 'History kept; acknowledged-event policy unchanged' : 'History follows its retention' })),
  chip(src), lockChip(lock));
}

function forecast(store) {
  // With R2 figures, the hub's own storage; otherwise no estimate at all.
  const panel = storagePanel(store.state.storage);
  if (panel) return h('div', { class: 'st-fc has-sto' }, panel);
  return h('div', { class: 'st-fc' },
    h('div', { class: 'st-fc-h' }, h('span', { text: 'Storage forecast' }), h('span', { class: 'st-src s-unavailable', text: 'unavailable' })),
    h('div', { class: 'st-fc-bar', 'aria-hidden': 'true' }),
    h('p', { class: 'st-dim', text: 'The hub does not report storage use to Settings yet, so no estimate is shown. Figures appear here once the retention and storage adapters report them; they will always be labelled as estimates.' }));
}

function token(store, key) {
  const row = store.rowOf(key);
  if (!row) return h('span', { class: 'st-tok lk', text: 'n/a' });
  const st = store.state, src = store.source(key), err = store.errors()[key], lock = lockOf(row), v = store.value(key);
  const text = key === 'blobs.expiry' && v === 'off' ? 'never' : formatValue(row, v);
  return h('button', {
    type: 'button', class: ['st-tok', 't-' + (src || 'default'), err && 'is-bad', st.open === key && 'is-open', lock && 'lk'].filter(Boolean).join(' '),
    'aria-expanded': String(st.open === key), 'aria-controls': st.open === key ? 'drawer-' + sid(key) : null, 'data-fk': 'tok-' + key,
    'aria-label': `${row.label}: ${text}${lock ? ', ' + (LOCK_CHIP[lock.kind] || 'read-only') : src === 'edited' ? ', edited' : ''}`,
    onclick: () => store.setOpen(key), text,
  });
}
function lookToken(store, field, opts) {
  const st = store.state, a = st.appearance, key = 'look.' + field, dirty = a.draft[field] !== a.saved[field];
  const label = opts.find(([v]) => v === a.draft[field])[1].toLowerCase();
  return h('button', {
    type: 'button', class: 'st-tok ' + (dirty ? 't-edited' : 't-local') + (st.open === key ? ' is-open' : ''), 'aria-expanded': String(st.open === key),
    'data-fk': 'tok-' + key, onclick: () => store.setOpen(key), text: label,
  });
}

function drawer(store, keys) {
  const st = store.state, key = st.open;
  if (!keys.includes(key)) return null;
  if (key.startsWith('look.')) {
    const field = key.slice(5), opts = field === 'theme' ? THEME_OPTS : DENSITY_OPTS;
    return h('div', { class: 'st-drawer', id: 'drawer-' + sid(key) },
      h('div', { class: 'st-dr-h' }, h('b', { text: field === 'theme' ? 'Theme' : 'Density' }), h('span', { class: 'st-src s-local', text: 'this browser' }),
        h('button', { type: 'button', class: 'st-x', 'aria-label': 'Close', 'data-fk': 'close-' + key, text: '×', onclick: () => store.setOpen('') })),
      h('p', { class: 'st-h', text: LOOK_HELP[field] }),
      h('div', { class: 'st-dr-c' }, lookControl(store, field, opts, field === 'theme' ? 'Theme' : 'Density')),
      lookBar(store));
  }
  const row = store.rowOf(key), id = sid(key);
  return h('div', { class: 'st-drawer', id: 'drawer-' + id, 'data-row': key },
    h('div', { class: 'st-dr-h' }, h('label', { for: id, class: 'st-lbl', text: row.label }), chip(store.source(key), row), lockChip(lockOf(row)), h('span', { class: 'st-key', text: key }),
      h('button', { type: 'button', class: 'st-x', 'aria-label': 'Close', 'data-fk': 'close-' + key, text: '×', onclick: () => store.setOpen('') })),
    h('div', { class: 'st-dr-c' }, control(store, row, id), storeOverride(store, row), resetLink(store, row)),
    ...details(store, row, id));
}

// sentence: text and token keys, as one paragraph with its drawer.
function para(store, n, title, parts, extra) {
  const keys = parts.filter(p => typeof p === 'object').map(p => p.k);
  return h('div', { class: 'st-p' }, h('span', { class: 'st-n', text: String(n), 'aria-hidden': 'true' }),
    h('div', {}, h('h3', {}, title, extra || null),
      h('p', { class: 'st-sent' }, ...parts.map(p => (typeof p === 'string' ? p : p.look ? lookToken(store, p.look, p.opts) : token(store, p.k)))),
      drawer(store, keys)));
}
const t = k => ({ k });

function policyBody(store, focusFor) {
  const st = store.state, off = store.value('blobs.expiry') === 'off';
  const axis = h('div', { class: 'st-axis', 'aria-hidden': 'true' }, ...TICKS.map(([label, s]) => { const e = h('span', { text: label }); e.style.left = lanePosition(s) + '%'; return e; }));
  return h('div', { class: 'st-policy' },
    h('section', { class: 'st-time', 'aria-labelledby': 'st-time-h' },
      h('div', { class: 'st-th' }, h('h2', { id: 'st-time-h', text: 'What the hub keeps, and for how long' }), archivePill(store)),
      store.rowOf('retention.archive_mode') && lockOf(store.rowOf('retention.archive_mode')) ? h('p', { class: 'st-lock-s st-arch-why', id: sid('retention.archive_mode') + '-al', text: 'Archive mode is unavailable: ' + lockOf(store.rowOf('retention.archive_mode')).text }) : null,
      axis,
      ...LANES.map(l => laneEl(store, l, focusFor)),
      h('p', { class: 'st-dim st-lane-note', text: 'Archive mode pauses the Messages lane only. Scheduled acknowledged-event pruning also requires its rollout gate; archive mode does not pause that policy. Set their retention to 0 to keep them too.' }),
      forecast(store)),
    h('section', { class: 'st-words', 'aria-labelledby': 'st-words-h' },
      h('h2', { id: 'st-words-h', text: 'Everything else, in plain words' }),
      para(store, 1, 'Files', off
        ? ['New attachments ', t('blobs.expiry'), ' expire. Each peer may change its avatar ', t('rates.avatar_changes_per_hour'), ', and legacy file read links last ', t('files.read_token_ttl'), '.']
        : ['New attachments expire after ', t('blobs.expiry'), '. Each peer may change its avatar ', t('rates.avatar_changes_per_hour'), ', and legacy file read links last ', t('files.read_token_ttl'), '.']),
      para(store, 2, 'Notifications & presence', ['MCP clients re-read their Events every ', t('events.refresh_interval'), '. Warn on successful sends when the recipient was last seen longer ago than ', t('presence.send_warning_after'), ' (0 disables the warning); open streams count as active. Never-reported presence is already unknown. Reports turn unknown when older than their TTL: other kinds ', t('presence.default_ttl'), ', sessions ', t('presence.session_ttl'), ', launchers ', t('presence.launcher_ttl'), '. Grok override value ', t('presence.grokbot_ttl'), '; see its current policy below.']),
      grokPolicy(store),
      para(store, 3, 'Agents defaults', ['A new message may nest ', t('agents.default_max_depth'), ' deep and lives ', t('agents.default_ttl'), ', within a cap of ', t('agents.max_expiry'), '. A new thread gets a budget of ', t('agents.thread_budget'), '. A newly created agent may send ', t('rates.sends_per_minute'), ' and start threads at ', t('rates.threads_per_hour'), '. Provisioning policies without their own limits create up to ', t('provisioning.creates_per_hour'), ' and hold ', t('provisioning.max_peers'), '. Anonymous clients get bursts of ', t('rates.anonymous_burst'), ', refilled every ', t('rates.anonymous_refill'), '. Message text past ', t('agents.text_cap'), ' spills to an attachment.']),
      para(store, 4, 'Automatic archiving', ['Archive idle agents after ', t('agents.auto_archive_after'), ' (0 disables). Include every eligible agent kind: ', t('agents.auto_archive_all_kinds'), '. Archive threads when ', t('threads.auto_archive_share'), ' of their agents are archived (0 disables). Agent archive revokes credentials; thread archive only tidies the operator view.']),
      para(store, 5, 'Appearance', ['In this browser, show the ', { k: 'look.theme', look: 'theme', opts: THEME_OPTS }, ' theme at ', { k: 'look.density', look: 'density', opts: DENSITY_OPTS }, ' density.'], h('span', { class: 'st-you', text: 'only this browser' })),
      h('p', { class: 'st-leg' }, ...['default', 'stored', 'flag', 'edited'].map(s => h('span', { class: 'st-tok t-' + s + (s === 'flag' ? ' lk' : ''), text: s })),
        h('span', { class: 'st-tok lk t-unavailable', text: 'unavailable' }))));
}

// ---------- shared chrome ----------

function head(store) {
  const st = store.state;
  return [
    h('div', { class: 'st-head' }, h('h1', { text: 'Settings' }),
      h('div', { class: 'st-seg st-view', role: 'group', 'aria-label': 'View' }, ...[['list', 'List'], ['policy', 'Policy']].map(([v, l]) => h('button', {
        type: 'button', class: st.view === v ? 'on' : '', 'aria-pressed': String(st.view === v), 'data-fk': 'view-' + v, onclick: () => store.setView(v),
      }, h('i', { class: 'pv-' + v, 'aria-hidden': 'true' }), l)))),
    h('p', { class: 'lede', text: st.view === 'list'
      ? `Hub settings apply to every peer on ${location.host}. Appearance is this browser's alone.`
      : 'The same settings, read as a policy. Pick a stop on a lane, or open any underlined value to change it.' }),
  ];
}

const latestOf = n => new Map(((n.body || {}).preferences || []).map(r => [r.key, r]));

// noteSig names what the notices say, apart from the operator's own values;
// the notices are rebuilt (and so announced) only when it changes.
function noteSig(store) {
  const st = store.state, n = st.phase === 'ready' ? st.newer : null;
  const latest = n ? latestOf(n) : null;
  return JSON.stringify([
    n ? n.reason : '', n ? n.changed.map(k => { const r = latest.get(k); return r ? [k, r.value, r.source] : [k]; }) : [],
    st.phase === 'ready' && st.saveError ? st.saveError.text : '',
  ]);
}

// diffList sits outside the alert so typing in a conflicting field updates
// "yours" without re-announcing the notice.
function diffList(store) {
  const n = store.state.newer;
  if (!n || !n.changed.length) return null;
  const latest = latestOf(n), dirty = new Set(store.dirtyKeys());
  return h('ul', { class: 'st-diff' }, ...n.changed.map(k => {
    const r = latest.get(k), mine = store.rowOf(k);
    if (!r) return null;
    return h('li', {}, h('b', { text: r.label }), ` · hub now ${formatValue(r, r.value)} (${r.source})`, dirty.has(k) && mine ? ` · yours ${formatValue(mine, store.value(k))}` : '');
  }));
}

function notes(store) {
  const st = store.state, out = [];
  if (st.phase !== 'ready') return out;
  if (st.newer) {
    const n = st.newer;
    out.push(h('div', { class: 'st-note is-warn', 'data-note': n.reason },
      h('p', { role: 'alert' }, h('b', { text: n.reason === 'conflict' ? 'Not saved: someone else changed settings first.' : 'Settings changed on the hub while you were editing.' }),
        ' ', n.reason === 'conflict' ? 'Your edits are kept. Compare them with the latest values, then choose below.' : 'Your edits are kept until you choose.'),
      diffList(store),
      h('p', { class: 'st-acts' },
        h('button', { type: 'button', class: 'btn pri', 'data-fk': 'adopt', text: 'Use latest values, keep my edits', onclick: () => store.adoptNewer() }),
        h('button', { type: 'button', class: 'btn', 'data-fk': 'discard', text: 'Discard my edits', onclick: () => store.undo() }))));
  }
  if (st.saveError) out.push(h('div', { class: 'st-note is-bad', role: 'alert', 'data-note': 'error', text: st.saveError.text }));
  return out;
}

function saveBar(store) {
  const st = store.state, n = store.dirtyKeys().length;
  if (!n && !st.busy) return null;
  const errs = Object.keys(store.errors()).filter(k => st.draft.has(k) || st.serverErrors[k]);
  const local = errs.filter(k => !st.serverErrors[k]);
  // After a 412 the operator chooses in the notice first; Save would only
  // send the stale If-Match again.
  const conflict = !!st.newer && st.newer.reason === 'conflict';
  let msg, cls = '';
  if (st.busy === 'saving') { msg = [h('span', { class: 'st-spin', 'aria-hidden': 'true' }), `Saving ${plural(n, 'change')}…`]; cls = 'is-saving'; }
  else if (st.busy === 'previewing') { msg = [h('span', { class: 'st-spin', 'aria-hidden': 'true' }), 'Checking the change with the hub…']; cls = 'is-saving'; }
  else if (st.busy === 'confirming') msg = [h('b', { text: 'Waiting for your confirmation' })];
  else if (errs.length) {
    msg = [h('b', { text: `${plural(errs.length, 'setting')} need${errs.length === 1 ? 's' : ''} fixing` }), h('button', { type: 'button', class: 'linkbtn st-jump', text: 'Show', onclick: () => store.showField(errs.sort()[0]) })];
    cls = 'is-bad';
  } else msg = [h('b', { text: `${plural(n, 'unsaved change')}` }), h('span', { class: 'st-dim', text: 'to hub settings' })];
  return h('div', { class: 'st-bar ' + cls, role: 'region', 'aria-label': 'Unsaved hub changes' },
    h('span', { class: 'st-bar-m' }, ...msg),
    h('span', { class: 'st-acts' },
      h('button', { type: 'button', class: 'btn', 'data-fk': 'undo', disabled: !!st.busy, text: 'Undo', onclick: () => store.undo() }),
      h('button', { type: 'button', class: 'btn pri', 'data-fk': 'save', disabled: !!st.busy || !!local.length || conflict, title: conflict ? 'Choose in the notice above first' : null, text: st.busy === 'saving' ? 'Saving…' : 'Save changes', onclick: () => store.save() })));
}

// ---------- confirmation dialog ----------

const RISK_TITLE = {
  presence_shortened: 'Shorten presence timeouts?', archive_disabled: 'Turn off archive mode?', retention_shortened: 'Shorten retention?',
  blob_expiry_off: 'Keep new attachments forever?', retention_apply_existing: 'Apply to data already on the hub?',
};
function riskText(store, risk, impact) {
  const val = k => { const r = store.rowOf(k); return r ? formatValue(r, store.value(k)) : ''; };
  switch (risk.code) {
    case 'presence_shortened': return 'Agents turn unknown sooner after their last presence report. Wakes held for an agent whose presence is now stale may be released.';
    case 'archive_disabled': return `Message history becomes eligible for expiry under History retention (${val('retention.history')}) for messages from now on. Messages already here stay protected unless you also approve applying the policy to existing data. Scheduled acknowledged-event pruning also requires its rollout gate; archive mode does not pause that policy.`;
    case 'retention_shortened': {
      const labels = (Array.isArray(risk.keys) ? risk.keys : []).map(k => store.rowOf(k)?.label).filter(Boolean).join(', ');
      return (labels ? labels + ': ' : '') + 'The new limit applies to rows created from now on. Existing rows keep their previous rule unless you also approve applying it to existing data.';
    }
    case 'blob_expiry_off': return blobOffText((impact && impact.storage) || store.state.storage) + ' Figures are estimates. Turning expiry back on later does not delete kept files.';
    case 'retention_apply_existing': return 'Existing rows older than the fixed cutoff become eligible at the next nightly sweep, not now. Pinned rows are kept. Deleted data cannot be restored by Undo; take a backup first (docs/DEPLOY.md, Backups).';
  }
  return 'This page cannot describe this risk, so the change cannot be confirmed here.';
}
function impactList(impact) {
  if (!impact) return null;
  const rows = [
    ['Cutoff', impact.cutoff_at], ['Not before', impact.not_before], ['High-water', impact.high_water],
    ['Eligible rows (estimate)', impact.eligible], ['Pinned rows kept (estimate)', impact.pinned], ['Bytes (estimate)', impact.estimated_bytes !== undefined ? bytes(impact.estimated_bytes) : undefined],
  ].filter(([, v]) => v !== undefined && v !== null && v !== '');
  return rows.length ? h('dl', { class: 'st-cf-impact' }, ...rows.flatMap(([k, v]) => [h('dt', { text: k }), h('dd', { text: String(v) })])) : null;
}

function paintDialog(store, dialog, ui) {
  const c = store.state.confirm, risks = (c.preview.risks || []).map(r => (r && typeof r === 'object' ? r : {}));
  // The store refuses a preview it cannot describe before this opens; the
  // dialog holds the same line, so Confirm stays off whatever it is shown.
  const refusal = previewRefusal(c.preview);
  // The hub takes apply_existing only when every key in the patch is a
  // retention key, so the choice is offered only then.
  const retention = c.keys.length > 0 && c.keys.every(k => k.startsWith('retention.'));
  const title = refusal ? 'This change cannot be confirmed here' : risks.length === 1 ? RISK_TITLE[risks[0].code] || 'Confirm this change?' : c.applyExisting ? RISK_TITLE.retention_apply_existing : `Confirm ${risks.length} risky changes?`;
  const ok = h('button', { type: 'button', class: 'btn st-dgr', 'data-cf': 'ok' });
  const typed = h('input', { id: 'st-cf-typed', autocomplete: 'off', spellcheck: 'false', 'aria-describedby': 'st-cf-type-l' });
  const existing = h('input', { type: 'checkbox', id: 'st-cf-existing' });
  const sync = () => {
    ok.disabled = !!refusal || (c.applyExisting && typed.value.trim().toLowerCase() !== 'trim');
    ok.textContent = existing.checked && !c.applyExisting ? 'Preview existing data' : c.applyExisting ? 'Apply to existing data' : 'Confirm and save';
  };
  typed.addEventListener('input', sync);
  existing.addEventListener('change', sync);
  ok.addEventListener('click', () => store.confirm({ applyExisting: existing.checked && !c.applyExisting }));
  const cancel = h('button', { type: 'button', class: 'btn', 'data-cf': 'no', text: 'Cancel', onclick: () => store.cancelConfirm() });
  const changes = h('table', { class: 'st-cf-t' }, h('thead', {}, h('tr', {}, h('th', { text: 'Setting' }), h('th', { text: 'Now' }), h('th', { text: 'After saving' }))),
    h('tbody', {}, ...c.keys.map(k => {
      const r = store.rowOf(k), d = store.state.draft.get(k);
      return h('tr', {}, h('td', { text: r.label }), h('td', { text: formatValue(r, r.value) }), h('td', { text: d && d.op === 'reset' ? `start-up value (built-in ${formatValue(r, r.default)} unless a flag sets it)` : formatValue(r, store.value(k)) }));
    })));
  // No method=dialog: Enter in the typed field must not close the dialog
  // behind the store's back. Only Cancel/Escape/confirm change state.
  dialog.replaceChildren(h('form', { class: 'st-cf-in', onsubmit: e => e.preventDefault() },
    h('div', { class: 'st-cf-ic', 'aria-hidden': 'true', text: '!' }),
    h('h2', { id: 'st-cf-title', text: title }),
    changes,
    ...risks.map(r => h('div', { class: 'st-cf-risk', 'data-risk': typeof r.code === 'string' && r.code ? r.code : 'unnamed' }, h('p', { text: riskText(store, r, c.preview.impact) }), h('p', { class: 'st-dim', text: 'Hub: ' + String(r.message || r.code || 'a risk with no name') }))),
    refusal ? h('p', { class: 'st-cf-note', role: 'alert', text: refusal }) : null,
    impactList(c.preview.impact),
    !refusal && retention && !c.applyExisting ? h('label', { class: 'st-cf-ch dg' }, existing, h('span', {}, h('b', { text: 'Also apply to data already on the hub' }), h('small', { text: 'Off by default. Needs a second preview with a fixed cutoff, then deletes at the next nightly sweep. Back up first.' }))) : null,
    c.applyExisting ? h('label', { class: 'st-cf-type' }, h('span', { id: 'st-cf-type-l' }, 'Type ', h('code', { text: 'trim' }), ' to delete existing data at the next nightly sweep'), typed) : null,
    h('p', { class: 'st-cf-note', text: 'Cancel keeps your edits and writes nothing. Confirming saves every change listed above in one step.' }),
    h('div', { class: 'st-cf-acts' }, cancel, ok)));
  sync();
  ui.cancel = cancel;
}

// ---------- mount ----------

export function mount(root, ctx) {
  const store = createSettingsStore({ api: ctx.api, appearance: ctx.appearance, storage: safeStorage() });
  const page = h('div', { class: 'st-page' });
  const live = h('div', { class: 'st-sr', role: 'status', 'aria-live': 'polite' });
  const dialog = h('dialog', { class: 'st-cf', 'aria-labelledby': 'st-cf-title' });
  // notesEl stays attached across renders so its alerts are announced once,
  // when what they say changes, not on every keystroke.
  const notesEl = h('div', { class: 'st-notes' });
  const ui = { cancel: null, sig: '', said: 0, seenSaved: null, flash: null, backFk: '', noteSig: null, parked: '', closing: false };
  const focusFor = { key: '' };
  root.classList.add('st-col');
  root.replaceChildren(page, live);
  page.append(notesEl);
  document.body.append(dialog);
  const usable = k => { const n = k ? page.querySelector(`[data-fk="${CSS.escape(k)}"]`) : null; return n && !n.disabled ? n : null; };
  // fallbackFor: where focus goes when the focused control is gone or
  // disabled after a render (drawer closed, link reverted, Apply done).
  const fallbackFor = fk => {
    if (fk.startsWith('close-')) return usable('tok-' + fk.slice(6));
    if (fk.startsWith('rst-')) return usable(sid(fk.slice(4)));
    if (fk === 'look-apply' || fk === 'look-undo') return page.querySelector('.st-seg [data-fk^="look-"][aria-pressed="true"]');
    return null;
  };
  const viewToggle = () => usable('view-' + store.state.view);
  // place focuses the control named by key. One disabled only while the
  // store is busy (Save while previewing, fields while saving) is parked and
  // focused again once it is enabled; otherwise focus falls back nearby.
  const place = key => {
    const el = key ? page.querySelector(`[data-fk="${CSS.escape(key)}"]`) : null;
    if (el && !el.disabled) { el.focus({ preventScroll: true }); return el; }
    if (el && store.state.busy) { ui.parked = key; return null; }
    const n = (key && fallbackFor(key)) || usable('save') || viewToggle();
    if (n) n.focus({ preventScroll: true });
    return n;
  };
  dialog.addEventListener('cancel', e => { e.preventDefault(); store.cancelConfirm(); });
  dialog.addEventListener('close', () => {
    // Anything that closes the dialog while a confirmation is pending
    // cancels it, so the store and the screen agree. A close the page made
    // itself (the store cleared the confirmation) is skipped: its event
    // arrives a task later, possibly after a second preview has opened a
    // new confirmation.
    if (!ui.closing && store.state.confirm) store.cancelConfirm();
    ui.closing = false;
    if (!dialog.open) place(ui.backFk || 'save');
  });
  const me = { store, aside: null, render: () => render() };
  current = me;

  function render() {
    const st = store.state;
    document.body.dataset.stView = st.view;
    root.classList.toggle('st-wide', st.view === 'policy');
    // Focus a field the store asked for: in Policy, open its drawer first.
    const want = store.takeFocus();
    if (want && st.view === 'policy' && !LANES.some(l => l.key === want) && want !== 'retention.archive_mode') st.open = want;
    if (st.saved !== ui.seenSaved) { ui.seenSaved = st.saved; ui.flash = st.saved ? new Set(st.saved.keys) : null; if (st.saved) ctx.toast(`Saved ${plural(st.saved.keys.length, 'change')}.`); } else ui.flash = null;

    const active = document.activeElement, fk = active && page.contains(active) ? active.getAttribute('data-fk') : null;
    const sel = fk && typeof active.selectionStart === 'number' ? [active.selectionStart, active.selectionEnd] : null;
    const before = [...head(store)], after = [];
    if (st.phase === 'loading') before.push(h('div', { class: 'loading', role: 'status', text: 'Loading hub settings…' }));
    else if (st.phase === 'error') before.push(h('div', { class: 'empty error', 'data-note': 'load' }, h('b', { text: 'Hub settings could not load.' }), st.loadError.text + ' ',
      h('button', { type: 'button', class: 'btn', 'data-fk': 'retry', text: 'Try again', onclick: () => store.load() })));
    if (st.phase === 'ready') {
      if (!st.rows.length) after.push(h('div', { class: 'empty' }, h('b', { text: 'This hub exposes no settings.' }), 'Appearance below still applies to this browser.'));
      after.push(st.view === 'list' ? listBody(store, ui.flash) : policyBody(store, focusFor));
    } else after.push(appearanceSection(store));
    after.push(saveBar(store));
    const sig = noteSig(store);
    if (sig !== ui.noteSig) { ui.noteSig = sig; notesEl.replaceChildren(...notes(store)); }
    else { const ul = notesEl.querySelector('.st-diff'); if (ul) ul.replaceWith(diffList(store)); }
    for (const c of [...page.children]) if (c !== notesEl) c.remove();
    notesEl.before(...before.filter(Boolean));
    notesEl.after(...after.filter(Boolean));

    // A parked key applies only while nothing else holds focus, and lapses
    // once the store is idle and no dialog is open.
    const parked = ui.parked;
    ui.parked = '';
    const idle = !active || active === document.body;
    const target = focusFor.key || fk || (idle && !dialog.open ? parked : '');
    focusFor.key = '';
    if (target) {
      const n = place(target);
      if (n && sel && n.setSelectionRange && n.getAttribute('data-fk') === fk) { try { n.setSelectionRange(...sel); } catch { /* not a text field */ } }
    } else if (parked && (st.busy || dialog.open)) ui.parked = parked;
    if (want) {
      const n = document.getElementById(sid(want)) || page.querySelector(`[data-row="${CSS.escape(want)}"] .st-stop.on`);
      if (n) { n.scrollIntoView({ block: 'center' }); n.focus({ preventScroll: true }); }
    }
    if (me.aside) {
      me.aside.classList.add('st-aside');
      me.aside.replaceChildren(...(st.view === 'list' ? listAside(store) : []));
    }
    syncDialog();
    if (st.announce.n !== ui.said) { ui.said = st.announce.n; live.textContent = st.announce.text; }
    const count = document.getElementById('settings-count'), n = store.dirtyKeys().length;
    if (count) { count.textContent = n ? String(n) : ''; count.className = 'cnt' + (n ? ' dirty' : ''); }
  }

  function syncDialog() {
    const c = store.state.confirm;
    if (!c) { ui.sig = ''; if (dialog.open) { ui.closing = true; dialog.close(); } return; }
    const sig = JSON.stringify([c.keys, c.applyExisting, (c.preview.risks || []).map(r => (r || {}).code)]);
    if (sig !== ui.sig) { ui.sig = sig; paintDialog(store, dialog, ui); }
    if (!dialog.open) {
      const a = document.activeElement;
      ui.backFk = (a && page.contains(a) ? a.getAttribute('data-fk') : '') || ui.parked || '';
      dialog.showModal();
      if (ui.cancel) ui.cancel.focus();
    }
  }

  const off = store.subscribe((_st, change) => {
    if (change === 'storage') updateStoragePanel(page.querySelector('.st-sto'), store.state.storage);
    else render();
  });
  const onFocus = () => store.refresh();
  const onVisible = () => { if (!document.hidden) store.refresh(); };
  const onBefore = e => { if (store.dirtyKeys().length) { e.preventDefault(); e.returnValue = ''; } };
  // Focus and visibility are not a reconnect. While this screen is mounted,
  // the shared connection's transition to live re-reads through refresh(),
  // including the meter-only path when a confirmation or save is busy.
  let offConn = () => {};
  if (ctx.store && typeof ctx.store.onConn === 'function') {
    offConn = ctx.store.onConn(state => { if (state === 'live') store.refresh(); });
  }
  window.addEventListener('focus', onFocus);
  document.addEventListener('visibilitychange', onVisible);
  window.addEventListener('beforeunload', onBefore);
  if (ctx.setLeaveGuard) ctx.setLeaveGuard(() => { const n = store.dirtyKeys().length; return n ? `Leave Settings? ${plural(n, 'unsaved change')} to hub settings will be lost.` : ''; });
  render();
  store.load();

  return () => {
    off();
    offConn();
    store.destroy();
    window.removeEventListener('focus', onFocus);
    document.removeEventListener('visibilitychange', onVisible);
    window.removeEventListener('beforeunload', onBefore);
    if (ctx.setLeaveGuard) ctx.setLeaveGuard(null);
    if (dialog.open) dialog.close();
    dialog.remove();
    root.classList.remove('st-col', 'st-wide');
    delete document.body.dataset.stView;
    const count = document.getElementById('settings-count');
    if (count) { count.textContent = ''; count.className = 'cnt'; }
    if (current === me) current = null;
  };
}

// mountAside fills #aside with the List view's rail and source legend.
export function mountAside(root) {
  const me = current;
  if (!me) return () => {};
  me.aside = root;
  me.render();
  return () => { root.classList.remove('st-aside'); root.replaceChildren(); if (me.aside === root) me.aside = null; };
}
