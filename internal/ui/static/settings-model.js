// Settings (docs/plans/2026-11-05-hub-preferences.md Task 6), pure: the one
// draft store that both views draw. List (variant A, Ledger) is the default
// view and Policy (variant D) the alternate; switching views keeps every edit,
// error, source badge and confirmation, because both read this store.
//
// The hub owns values, sources, bounds and risk. This module never invents a
// source badge: after Save the rows are the hub's reply. Drafts and preview
// tokens live in memory only, never in storage or the URL. Appearance is
// local to this browser and has its own Apply; it never reaches the hub.

export const VIEW_KEY = 'vecnet.ui.settingsView';
export const VIEWS = ['list', 'policy'];

// loadView: the remembered view, List when nothing valid is stored or the
// browser blocks storage.
export function loadView(storage) {
  try { return storage && storage.getItem(VIEW_KEY) === 'policy' ? 'policy' : 'list'; } catch { return 'list'; }
}
// rememberView stores only `list` or `policy`; false when storage is blocked.
export function rememberView(storage, view) {
  try {
    if (!storage) return false;
    storage.setItem(VIEW_KEY, view === 'policy' ? 'policy' : 'list');
    return true;
  } catch { return false; }
}

// The four hub groups (wire ids) and Appearance, which is client-only.
export const GROUPS = [
  { id: 'retention_storage', title: 'Retention & storage', blurb: 'How long the hub keeps what it has delivered. Saving never deletes existing data on its own.' },
  { id: 'files', title: 'Files', blurb: 'Attachments that agents and you upload.' },
  { id: 'notifications_presence', title: 'Notifications & presence', blurb: 'How often MCP clients refresh, and when presence goes stale.' },
  { id: 'agents_defaults', title: 'Agents defaults', blurb: 'Defaults for new agents, messages and threads. Existing ones keep what they have.' },
  { id: 'appearance', title: 'Appearance', blurb: 'This browser only. Other operators, devices and agents are unaffected.', local: true },
];

// ---------- durations ----------

const UNIT = { s: 1, m: 60, h: 3600, d: 86400, w: 604800 };
const ALIAS = {
  s: 's', sec: 's', secs: 's', second: 's', seconds: 's',
  m: 'm', min: 'm', mins: 'm', minute: 'm', minutes: 'm',
  h: 'h', hr: 'h', hrs: 'h', hour: 'h', hours: 'h',
  d: 'd', day: 'd', days: 'd',
  w: 'w', wk: 'w', week: 'w', weeks: 'w',
};

// parseDuration reads "90s", "30m", "1h30m", "2 days" or bare seconds into
// whole seconds. Fractions, negatives and unknown units are refused rather
// than rounded, and so is anything past Number.MAX_SAFE_INTEGER.
export function parseDuration(text) {
  const t = String(text ?? '').trim().toLowerCase();
  if (!t) return { error: 'empty' };
  if (/^\d+$/.test(t)) {
    const n = Number(t);
    return Number.isSafeInteger(n) ? { seconds: n } : { error: 'overflow' };
  }
  const re = /(\d+)\s*([a-z]+)\s*/y;
  let total = 0;
  for (let pos = 0; pos < t.length;) {
    re.lastIndex = pos;
    const m = re.exec(t);
    if (!m || !ALIAS[m[2]]) return { error: 'format' };
    total += Number(m[1]) * UNIT[ALIAS[m[2]]];
    if (!Number.isSafeInteger(total)) return { error: 'overflow' };
    pos = re.lastIndex;
  }
  return { seconds: total };
}

const PARTS = [['d', 86400], ['h', 3600], ['min', 60], ['s', 1]];
// formatDuration: 1800 → "30 min", 5400 → "1 h 30 min", 86400 → "1 d".
export function formatDuration(sec) {
  if (!sec) return '0 s';
  const out = [];
  let rest = sec;
  for (const [u, n] of PARTS) {
    if (rest >= n) { const q = Math.floor(rest / n); out.push(q + ' ' + u); rest -= q * n; }
  }
  return out.join(' ');
}
// durationText is the editable form of a duration: 5400 → "1h30m".
export function durationText(sec) {
  if (!sec) return '0';
  let out = '', rest = sec;
  for (const [u, n] of [['d', 86400], ['h', 3600], ['m', 60], ['s', 1]]) {
    if (rest >= n) { const q = Math.floor(rest / n); out += q + u; rest -= q * n; }
  }
  return out;
}

// ---------- rows ----------

const ENUM_LABEL = { '1h': '1 hour', '6h': '6 hours', '12h': '12 hours', '1d': '1 day', '7d': '7 days', '30d': '30 days', off: 'Off (keep files)' };
const INT_UNIT = {
  'threads.auto_archive_share': '%',
  'agents.default_max_depth': 'hops', 'agents.thread_budget': 'messages', 'agents.text_cap': 'bytes',
  'rates.sends_per_minute': 'per minute', 'rates.threads_per_hour': 'per hour', 'rates.avatar_changes_per_hour': 'per hour',
  'rates.anonymous_burst': 'requests', 'provisioning.creates_per_hour': 'per hour', 'provisioning.max_peers': 'peers',
};
// What zero means, for the keys whose registry declares zero a disabled value.
const ZERO = { 'retention.ack_events': 'Never pruned', 'retention.history': 'Forever', 'provisioning.max_peers': 'No peers' };

export const unitOf = row => (row.type === 'duration' ? 'e.g. 30m, 2h, 1d' : INT_UNIT[row.key] || '');

export function formatValue(row, value) {
  if (value === null || value === undefined) return '—';
  if (row.type === 'bool') return value ? 'On' : 'Off';
  if (row.type === 'enum') return ENUM_LABEL[value] || String(value);
  if (value === 0 && ZERO[row.key]) return ZERO[row.key];
  if (row.type === 'duration') return formatDuration(value);
  const u = INT_UNIT[row.key];
  return u ? value + ' ' + u : String(value);
}
export const enumLabel = value => ENUM_LABEL[value] || String(value);
// inputText is the text an editor shows for a value.
export const inputText = (row, value) => (row.type === 'duration' ? durationText(value) : String(value));

export function rangeText(row) {
  const f = v => (row.type === 'duration' ? formatDuration(v) : String(v));
  const span = `between ${f(row.min)} and ${f(row.max)}`;
  return row.disabled_value === 0 && row.min > 0 ? `0 (${(ZERO[row.key] || 'off').toLowerCase()}) or ${span}` : span;
}

function bounded(row, n) {
  if (!Number.isSafeInteger(n)) return { error: 'That number is too large.' };
  if (n === 0 && row.disabled_value === 0) return { value: 0 };
  if ((row.min != null && n < row.min) || (row.max != null && n > row.max)) return { error: `Must be ${rangeText(row)}.` };
  return { value: n };
}

// parseField checks one editor's input against the hub's own bounds:
// { value } or { error }. The hub checks again on Save.
export function parseField(row, input) {
  switch (row.type) {
    case 'bool': return typeof input === 'boolean' ? { value: input } : { error: 'Choose on or off.' };
    case 'enum': return (row.allowed_values || []).includes(input) ? { value: input } : { error: 'Choose one of the listed values.' };
    case 'int': {
      const t = String(input ?? '').trim();
      if (!/^\d+$/.test(t)) return { error: t ? 'Enter a whole number.' : 'Enter a number.' };
      return bounded(row, Number(t));
    }
    case 'duration': {
      const p = parseDuration(input);
      if (p.error === 'empty') return { error: 'Enter a duration, like 30m or 2h.' };
      if (p.error === 'overflow') return { error: 'That duration is too long.' };
      if (p.error) return { error: 'Use whole units, like 90s, 30m, 2h or 1d.' };
      return bounded(row, p.seconds);
    }
  }
  return { error: 'This setting type is not supported here.' };
}

// Only live default/Grok TTLs preserve explicit overrides of starting values.
const presenceIntent = row => ['presence.default_ttl', 'presence.grokbot_ttl'].includes(row.key) && row.editable && row.runtime === 'live';

// lockOf says why a row cannot be edited here, or null. Other flag values,
// unavailable consumers and start-up-only settings remain read-only.
export function lockOf(row) {
  if (!row) return { kind: 'unavailable', text: 'Unknown setting.' };
  if (row.runtime === 'unavailable') return { kind: 'unavailable', text: row.unavailable_reason || 'Its consumer is not installed on this hub.' };
  if (!row.editable || row.runtime !== 'live') return { kind: 'readonly', text: row.unavailable_reason || 'Not editable while the hub runs.' };
  if (row.source === 'flag' && !presenceIntent(row)) {
    const where = row.origin ? (row.origin.startsWith('--') ? `the ${row.origin} flag` : `the ${row.origin} environment variable`) : 'a hub flag or environment variable';
    return { kind: 'flag', text: `Set by ${where} when the hub started. Change it there; it is read-only here.` };
  }
  return null;
}

// Resolve committed policy only; draft badges and provisional RESET values
// cannot reveal the hub's hidden starting layer. Incomplete older snapshots
// have no policy to display, rather than an invented factory value.
export function grokPresencePolicy(rows) {
  const grok = rows.find(r => r.key === 'presence.grokbot_ttl');
  const fallback = rows.find(r => r.key === 'presence.default_ttl');
  if (!grok || !fallback) return null;
  const row = grok.source === 'default' && ['flag', 'stored'].includes(fallback.source) ? fallback : grok;
  return { seconds: row.value, key: row.key, source: row.source, origin: row.origin };
}

// timingOf: when a saved value takes effect, per the hub's runtime field.
export function timingOf(row) {
  if (row.runtime === 'unavailable') return 'Unavailable on this hub';
  if (row.runtime === 'bootstrap' || !row.editable) return 'Fixed when the hub starts';
  return 'Applies when saved, without a restart';
}

const same = (a, b) => a === b;

// entryDirty: whether a draft entry changes anything on Save.
function entryDirty(row, d) {
  if (!row || !d) return false;
  if (d.op === 'reset') return row.source === 'stored';
  const p = parseField(row, d.input);
  const intent = presenceIntent(row) && ['default', 'flag'].includes(row.source);
  return !!p.error || intent || !same(p.value, row.value);
}

// buildPatch turns the dirty draft into one atomic PATCH body. Explicit
// default/Grok TTL SETs preserve intent even at equality; ordinary settings
// retain numeric no-op handling. The hub alone determines saved provenance.
// RESET removes a stored override and reveals the hub's starting layer.
export function buildPatch(rows, draft) {
  const byKey = new Map(rows.map(r => [r.key, r]));
  const set = {}, reset = [];
  for (const key of [...draft.keys()].sort()) {
    const row = byKey.get(key), d = draft.get(key);
    if (!row || lockOf(row) || !entryDirty(row, d)) continue;
    if (d.op === 'reset') { reset.push(key); continue; }
    const p = parseField(row, d.input);
    if (p.error) return { error: p.error, key };
    set[key] = p.value;
  }
  reset.sort();
  return { set, reset };
}
export const patchKeys = patch => [...Object.keys(patch.set || {}), ...(patch.reset || [])].sort();

// validSnapshot: the GET/PATCH body has the shape this page draws.
export function validSnapshot(body) {
  if (!body || typeof body !== 'object' || !Array.isArray(body.preferences)) return false;
  return body.preferences.every(p => p && typeof p.key === 'string' && typeof p.type === 'string' && typeof p.label === 'string');
}

// Runtime metadata is an optional report, never a policy value or lock.
export function eventRetentionEnabledOf(body) {
  const runtime = body && body.runtime;
  return runtime && typeof runtime === 'object' && !Array.isArray(runtime) && typeof runtime.event_retention_enabled === 'boolean'
    ? runtime.event_retention_enabled : null;
}
export function eventRetentionNote(enabled) {
  if (enabled === false) return 'Acknowledged-event pruning gate: disabled (last reported by hub). Saving this policy does not enable pruning.';
  if (enabled === true) return 'Acknowledged-event pruning gate: enabled (last reported by hub).';
  return 'Acknowledged-event pruning gate: not reported by this hub.';
}

// ---------- Policy lanes ----------

// Lanes on a log time axis, one per thing the hub keeps. Archive mode pauses
// message history only: scheduled acknowledged-event pruning also requires
// its rollout gate; retention.ack_events 0 keeps them too.
export const LANES = [
  { key: 'retention.history', label: 'Messages', then: 'then deleted at the nightly sweep', archive: true },
  { key: 'retention.ack_events', label: 'Acknowledged events', then: 'then pruned if the pruning gate is enabled' },
  { key: 'blobs.expiry', label: 'Files', then: 'then the object expires' },
  { key: 'agents.default_ttl', label: 'Undelivered messages', then: 'then they expire' },
];
const ENUM_SECONDS = { '1h': 3600, '6h': 21600, '12h': 43200, '1d': 86400, '7d': 604800, '30d': 2592000, off: Infinity };
const PRESETS = [3600, 21600, 86400, 259200, 604800, 2592000, 7776000, 31536000];
export const TICKS = [['now', 0], ['1h', 3600], ['6h', 21600], ['1d', 86400], ['7d', 604800], ['30d', 2592000], ['90d', 7776000], ['1y', 31536000], ['∞', Infinity]];

export const secondsOf = (row, value) => (row.type === 'enum' ? ENUM_SECONDS[value] ?? 0 : value === 0 && row.disabled_value === 0 ? Infinity : value);

// lanePosition: percent along the axis. 0 is now, 96 is forever; finite
// values past a year sit just short of forever.
export function lanePosition(seconds) {
  if (seconds === Infinity) return 96;
  if (!seconds) return 0;
  const lo = Math.log(1800), hi = Math.log(31536000);
  return Math.min(90, Math.max(2, 5 + ((Math.log(seconds) - lo) / (hi - lo)) * 82));
}

// laneStops: the values a lane offers, within the row's bounds, plus the
// current value when it is not one of them.
export function laneStops(row, current, saved) {
  let values;
  if (row.type === 'enum') values = [...(row.allowed_values || [])];
  else {
    values = PRESETS.filter(s => s >= (row.min ?? 0) && s <= (row.max ?? Infinity));
    if (row.disabled_value === 0) values.push(0);
    for (const v of [current, saved]) if (v !== undefined && v !== null && !values.includes(v)) values.push(v);
  }
  return values.map(value => ({ value, seconds: secondsOf(row, value), label: formatValue(row, value) }))
    .sort((a, b) => a.seconds - b.seconds);
}

// ---------- attachment retention and storage (R2 blobs Task 7) ----------

// BLOB_NEW_ONLY sits beside the Attachment expiry row in both views.
export const BLOB_NEW_ONLY = 'Applies to new uploads only. Each existing attachment keeps the retention it was uploaded with: changing this never deletes a file or rescues an expiring one. Download links stay short-lived (up to 10 minutes) whatever is chosen.';

// bytesText: decimal units, as the hub's storage figures are read.
export function bytesText(n) {
  if (typeof n !== 'number' || !Number.isFinite(n)) return '';
  for (const [u, s] of [['GB', 1e9], ['MB', 1e6], ['KB', 1e3]]) if (n >= s) return (n / s).toFixed(n >= 10 * s ? 0 : 1) + ' ' + u;
  return n + ' B';
}

const FIGURES = ['retained_bytes', 'reserved_bytes', 'used_bytes', 'limit_bytes'];
// validStorage: the operator-only storage figures beside a snapshot, or null.
// Each is a whole, non-negative byte count and the limit is finite and set.
export function validStorage(s) {
  if (!s || typeof s !== 'object') return null;
  if (!FIGURES.every(k => Number.isSafeInteger(s[k]) && s[k] >= 0) || s.limit_bytes <= 0) return null;
  return Object.fromEntries(FIGURES.map(k => [k, s[k]]));
}

// storageView: the meter both views draw. near at 90%, full at the limit;
// a limit lowered below use reads over the limit, and nothing is deleted.
export function storageView(storage) {
  // A failed live read is not "no adapter": both views already draw whatever
  // this returns, and the Policy forecast's placeholder is the other case.
  if (storage && storage.unavailable === true) {
    return {
      unavailable: true, tone: 'unavailable', percent: 0, text: 'Storage unavailable',
      note: 'Storage figures could not be read just now. Nothing was changed; reload to try again.',
      retained_bytes: 0, reserved_bytes: 0, used_bytes: 0, limit_bytes: 1,
      parts: ['Retained', 'Reserved', 'Used', 'Limit'].map(label => ({ label, value: 'unavailable' })),
    };
  }
  const s = validStorage(storage);
  if (!s) return null;
  const used = s.used_bytes, limit = s.limit_bytes, ratio = used / limit;
  const tone = used >= limit ? 'full' : ratio >= 0.9 ? 'near' : 'ok';
  const note = used > limit
    ? 'Storage is over the limit: new uploads are refused until files expire or are purged. Nothing already uploaded is deleted.'
    : tone === 'full' ? 'Storage is full: new uploads are refused until files expire or are purged. Nothing already uploaded is deleted.'
      : tone === 'near' ? 'Storage is near its limit: new uploads are refused once it is full.' : '';
  return {
    ...s, tone, note, percent: Math.min(100, Math.round(ratio * 100)),
    text: `${bytesText(used)} used of ${bytesText(limit)}` + (used > limit ? `, over by ${bytesText(used - limit)}` : ''),
    parts: [['Retained', s.retained_bytes], ['Reserved', s.reserved_bytes], ['Used', used], ['Limit', limit]].map(([label, n]) => ({ label, value: bytesText(n) })),
  };
}

// blobOffText: what turning expiry off costs, with the hub's own figures.
export function blobOffText(storage) {
  const s = validStorage(storage);
  return 'New attachments get no automatic expiry, and the storage provider has no disaster expiry for them: storage grows, and costs, until files are purged or their last message reference goes. Existing attachments keep their deadlines.' +
    (s ? ` The hub reports ${bytesText(s.retained_bytes)} retained and ${bytesText(s.reserved_bytes)} reserved: ${bytesText(s.used_bytes)} used of a ${bytesText(s.limit_bytes)} limit.` : '');
}

// ---------- the store ----------

const aborted = e => !!e && (e.code === 'aborted' || e.name === 'AbortError');

// describeLoad: a load failure as the page explains it.
export function describeLoad(e) {
  if (e && e.status === 403) return { code: 'forbidden', text: 'Only the operator credential can read hub settings.' };
  if (e && e.status === 404) return { code: 'missing', text: 'This hub does not serve Settings yet. Upgrade the hub to edit settings here.' };
  if (e && e.status === 0) return { code: 'network', text: 'Could not reach the hub.' };
  return { code: (e && e.code) || 'error', text: (e && e.message) || 'The hub could not load settings.' };
}

// The risk codes this page can describe (internal/hub/preferences_preview.go).
// A preview naming any other risk, or a risk without a code, is refused
// whole: the operator cannot approve a consequence the page cannot explain.
export const RISK_CODES = ['archive_disabled', 'blob_expiry_off', 'presence_shortened', 'retention_apply_existing', 'retention_shortened'];

// unknownRisks: the names of a preview's risks this page cannot describe;
// `unnamed` stands for a risk with no code. Empty when every risk is known.
export function unknownRisks(preview) {
  const risks = preview && Array.isArray(preview.risks) ? preview.risks : [];
  return risks.filter(r => !r || typeof r !== 'object' || !RISK_CODES.includes(r.code))
    .map(r => (r && typeof r.code === 'string' && r.code ? r.code : 'unnamed'));
}

// previewRefusal: why a risky preview cannot be confirmed here, or ''.
export function previewRefusal(preview) {
  const unknown = [...new Set(unknownRisks(preview))];
  if (unknown.length) return `Not saved: the hub flagged ${unknown.length === 1 ? 'a risk' : 'risks'} this page cannot describe (${unknown.join(', ')}), so the change cannot be confirmed here. Nothing was saved; your edits are kept. This page may be older than the hub: reload it, or review the change with vecnet prefs.`;
  if (!preview || typeof preview.confirm_token !== 'string' || !preview.confirm_token) return 'Not saved: the hub asked for a confirmation without a token to confirm it with. Nothing was saved; your edits are kept.';
  return '';
}

const FIELD_ERROR = {
  prefs_invalid_value: 'The hub refused this value.',
  prefs_unknown_key: 'The hub does not know this setting.',
  prefs_unavailable: 'Unavailable on this hub right now.',
  prefs_policy_locked: 'Locked by this deployment\'s policy.',
};
const CONSTRAINT = {
  range: 'Out of range.', type: 'Wrong type.', enum: 'Not an allowed value.', zero_not_allowed: 'Zero is not allowed.',
  nonnegative: 'Must not be negative.', max_expiry: 'Cannot exceed the maximum message expiry.', scalar: 'Not a valid value.',
};

// createSettingsStore: api is { prefs, patchPrefs, previewPrefs } from
// api.js; appearance is the browser controller from appearance.js (get, set,
// subscribe). Every async result is dropped once destroy() has run.
export function createSettingsStore({ api, appearance, storage }) {
  const subs = new Set();
  const ctl = new AbortController();
  let dead = false, offAppearance = () => {};
  let requestGeneration = 0, runtimeGeneration = 0;
  const look = appearance ? appearance.get() : { theme: 'system', density: 'comfortable' };
  const st = {
    phase: 'loading', loadError: null, etag: '', revision: 0, rows: [], draft: new Map(), storage: null, eventRetentionEnabled: null,
    busy: '', confirm: null, newer: null, saveError: null, serverErrors: {}, focus: '',
    saved: null, announce: { text: '', n: 0 }, view: loadView(storage), q: '', only: 'all', open: '',
    appearance: { saved: { ...look }, draft: { ...look }, note: '' },
  };
  const emit = (change = 'settings') => { if (!dead) for (const fn of [...subs]) fn(st, change); };
  const say = text => { st.announce = { text, n: st.announce.n + 1 }; };
  const rowOf = key => st.rows.find(r => r.key === key);
  const live = () => !dead;
  // The prefs ETag does not cover blob usage. Once a snapshot has shown the
  // meter, later re-reads ask the live counter even when prefs answers 304.
  let sawStorage = false;
  const storageSig = s => !s ? '' : s.unavailable ? 'unavailable' : FIGURES.map(k => s[k]).join(',');

  // Only accepted network responses may update this report. A 304 advances
  // the barrier without supplying metadata; cached adoption never enters here.
  function acceptRuntime(body, generation) {
    if (generation < runtimeGeneration) return false;
    runtimeGeneration = generation;
    if (!body) return false;
    const enabled = eventRetentionEnabledOf(body);
    const changed = enabled !== st.eventRetentionEnabled;
    st.eventRetentionEnabled = enabled;
    return changed;
  }

  function adopt(body, etag) {
    st.rows = body.preferences.slice();
    st.revision = body.revision || 0;
    st.etag = etag || '';
    // Storage figures come only with an operator's R2 snapshot.
    const storage = validStorage(body.storage);
    st.storage = storage;
    if (storage) sawStorage = true;
    // A draft for a row that became locked or vanished cannot be saved.
    for (const key of [...st.draft.keys()]) {
      const row = rowOf(key);
      if (!row || lockOf(row) || !entryDirty(row, st.draft.get(key))) st.draft.delete(key);
    }
  }

  const dirtyKeys = () => [...st.draft.keys()].filter(k => entryDirty(rowOf(k), st.draft.get(k))).sort();

  // value: the draft value a view shows; a pending reset shows the built-in
  // fallback, and an unparsable input keeps showing the saved value.
  function value(key) {
    const row = rowOf(key);
    if (!row) return undefined;
    const d = st.draft.get(key);
    if (!d) return row.value;
    if (d.op === 'reset') return row.default;
    const p = parseField(row, d.input);
    return p.error ? row.value : p.value;
  }
  // input: the text an editor shows (what the operator typed, if anything).
  function input(key) {
    const row = rowOf(key), d = st.draft.get(key);
    if (d && d.op === 'set' && typeof d.input === 'string') return d.input;
    return inputText(row, value(key));
  }

  // errors: field → message, from local checks and the hub's last answer.
  function errors() {
    const out = {};
    for (const key of dirtyKeys()) {
      const d = st.draft.get(key);
      if (d.op !== 'set') continue;
      const p = parseField(rowOf(key), d.input);
      if (p.error) out[key] = p.error;
    }
    const ttl = rowOf('agents.default_ttl'), cap = rowOf('agents.max_expiry');
    if (ttl && cap && !out['agents.default_ttl'] && st.draft.has(ttl.key) && value(ttl.key) > value(cap.key)) {
      out['agents.default_ttl'] = `Cannot exceed the maximum message expiry (${formatValue(cap, value(cap.key))}).`;
    }
    for (const [k, v] of Object.entries(st.serverErrors)) if (!out[k]) out[k] = v;
    return out;
  }

  // source: the badge a row shows. `edited` is a draft state; otherwise it
  // is exactly the hub's wire source.
  const source = key => (entryDirty(rowOf(key), st.draft.get(key)) ? 'edited' : (rowOf(key) || {}).source);

  async function load() {
    if (!st.rows.length) st.phase = 'loading';
    st.loadError = null;
    emit();
    try {
      const generation = ++requestGeneration;
      const r = await api.prefs({ signal: ctl.signal });
      if (!live()) return;
      if (!validSnapshot(r.body)) throw { status: 0, code: 'network', message: 'The hub sent settings this page cannot read.' };
      acceptRuntime(r.body, generation);
      adopt(r.body, r.etag);
      st.phase = 'ready';
    } catch (e) {
      if (!live() || aborted(e)) return;
      st.phase = 'error';
      st.loadError = describeLoad(e);
    }
    emit();
  }

  // refreshMeter reads retained/reserved/used/limit on their own. A miss
  // shows unavailable and does not touch the prefs ETag, the draft or a
  // confirmation that is already open.
  async function refreshMeter() {
    if (!sawStorage || typeof api.blobStorage !== 'function') return;
    try {
      const body = await api.blobStorage({ signal: ctl.signal });
      if (!live()) return;
      st.storage = validStorage(body && body.storage) || { unavailable: true };
    } catch (e) {
      if (!live() || aborted(e)) return;
      st.storage = { unavailable: true };
    }
  }

  // refresh re-reads on focus, visibility, Reload, or a live reconnect
  // while Settings is mounted. A clean page takes new preference values; a
  // dirty one keeps every typed edit and shows a notice instead. Prefs 304
  // is quiet for the rows, and the storage meter is read either way. A busy
  // confirm or save reads only the meter.
  // quiet: a failed re-read leaves the current message alone.
  async function refresh({ quiet = false } = {}) {
    if (st.phase !== 'ready') return load();
    if (st.busy) {
      const before = storageSig(st.storage);
      await refreshMeter();
      if (live() && storageSig(st.storage) !== before) emit('storage');
      return;
    }
    let r;
    const generation = ++requestGeneration;
    try { r = await api.prefs({ etag: st.etag, signal: ctl.signal }); } catch (e) {
      if (!live() || aborted(e) || quiet) return;
      st.saveError = { code: 'reload', text: 'Could not reload settings: ' + describeLoad(e).text };
      emit();
      return;
    }
    if (!live()) return;
    // A confirmation/save may have begun while this read was in flight.
    // Discard only the new report/barrier, preserving row and meter behavior.
    let runtimeChanged = false;
    if (!st.busy) {
      if (r.status === 304) acceptRuntime(null, generation);
      else if (validSnapshot(r.body)) runtimeChanged = acceptRuntime(r.body, generation);
    }
    const unchanged = r.status === 304 || !r.body || r.etag === st.etag || !validSnapshot(r.body);
    if (!unchanged) {
      if (st.busy) return;
      if (!dirtyKeys().length) {
        adopt(r.body, r.etag);
        st.newer = null;
        say('Settings reloaded from the hub.');
      } else {
        // A pending conflict stays a conflict: Save stays blocked until the
        // operator adopts the latest values or discards the draft.
        const reason = st.newer && st.newer.reason === 'conflict' ? 'conflict' : 'update';
        st.newer = { reason, body: r.body, etag: r.etag, changed: changedKeys(st.rows, r.body.preferences) };
        say('Settings changed on the hub. Your edits are kept until you choose.');
      }
    }
    const before = storageSig(st.storage);
    await refreshMeter();
    if (!live()) return;
    if (!unchanged || runtimeChanged) emit();
    else if (storageSig(st.storage) !== before) emit('storage');
  }

  function edit(key, value) {
    const row = rowOf(key);
    if (st.busy || !row || lockOf(row)) return;
    const d = { op: 'set', input: value };
    if (entryDirty(row, d)) st.draft.set(key, d); else st.draft.delete(key);
    delete st.serverErrors[key];
    st.saved = null;
    st.saveError = null;
    emit();
  }
  // resetKey: back to the start-up value. A stored override becomes a
  // pending reset (shown before Save); anything else just drops its draft.
  function resetKey(key) {
    const row = rowOf(key);
    if (st.busy || !row || lockOf(row)) return;
    if (row.source === 'stored') st.draft.set(key, { op: 'reset' }); else st.draft.delete(key);
    delete st.serverErrors[key];
    st.saved = null;
    emit();
  }
  function revert(key) {
    if (st.busy) return;
    st.draft.delete(key);
    delete st.serverErrors[key];
    emit();
  }
  // undo restores the last fetched or saved values. No request is made.
  function undo() {
    if (st.busy) return;
    const n = dirtyKeys().length;
    st.draft.clear();
    st.serverErrors = {};
    st.saveError = null;
    st.confirm = null;
    const newer = st.newer;
    st.newer = null;
    if (newer && newer.body) adopt(newer.body, newer.etag);
    if (n) say(`Undid ${n} unsaved change${n === 1 ? '' : 's'}.`);
    emit();
    // A conflict whose re-read failed has no values to adopt: read them now.
    if (newer && !newer.body) load();
  }

  function applySaved(r, keys) {
    adopt(r.body, r.etag);
    for (const key of keys) st.draft.delete(key);
    st.busy = '';
    st.confirm = null;
    st.newer = null;
    st.serverErrors = {};
    st.saveError = null;
    st.saved = { keys, revision: st.revision };
    say(`Saved ${keys.length} change${keys.length === 1 ? '' : 's'}. The hub is at revision ${st.revision}.`);
  }

  async function failSave(e, patch, previewed = false) {
    st.busy = '';
    st.confirm = null;
    const d = (e && e.details) || {};
    const code = e && e.code;
    if (code === 'prefs_revision_conflict' || code === 'prefs_precondition_required') {
      // Keep the draft; fetch the latest values for manual reconciliation.
      st.saveError = null;
      try {
        const generation = ++requestGeneration;
        const r = await api.prefs({ signal: ctl.signal });
        if (!live()) return;
        if (validSnapshot(r.body)) {
          acceptRuntime(r.body, generation);
          st.newer = { reason: 'conflict', body: r.body, etag: r.etag, changed: changedKeys(st.rows, r.body.preferences) };
        }
      } catch (err) {
        if (!live() || aborted(err)) return;
        st.newer = { reason: 'conflict', body: null, etag: '', changed: [] };
      }
      say('Not saved: someone else changed settings first. Your edits are kept; compare them with the latest values, then choose.');
      return;
    }
    if (code === 'prefs_confirmation_required' && !previewed) return preview(patch);
    if (code === 'prefs_confirmation_required') {
      st.saveError = { code, text: 'Not saved: the hub asked for a confirmation it did not describe. Reload and try again; your edits are kept.' };
      say(st.saveError.text);
      return;
    }
    const field = typeof d.field === 'string' && rowOf(d.field) ? d.field : '';
    if (field && FIELD_ERROR[code]) {
      st.serverErrors = { ...st.serverErrors, [field]: (CONSTRAINT[d.constraint] && code === 'prefs_invalid_value' ? CONSTRAINT[d.constraint] + ' ' : '') + FIELD_ERROR[code] };
      st.focus = field;
      st.saveError = { code, text: 'Not saved: fix the highlighted setting. Nothing was changed.' };
    } else if (code === 'prefs_confirmation_stale') {
      st.saveError = { code, text: 'The confirmation expired or no longer matches. Nothing was saved; Save again to review it.' };
    } else if (e && e.status === 0) {
      // The request may have committed before its reply was lost.
      st.saveError = { code: 'network', text: 'The hub did not answer, so this change may or may not have been saved. Your edits are kept; the page re-reads the hub\'s values.' };
      say(st.saveError.text);
      refresh({ quiet: true });
      return;
    } else if (e && e.status === 403) {
      st.saveError = { code: 'forbidden', text: 'Only the operator credential can change hub settings.' };
    } else {
      st.saveError = { code: code || 'error', text: 'Not saved: ' + String((e && e.message) || 'the hub refused the change').replace(/[.\s]+$/, '') + '. Your edits are kept.' };
    }
    say(st.saveError.text);
  }

  // preview asks the hub what a risky patch means. No write happens; the
  // token stays in memory until Confirm or Cancel.
  async function preview(patch, applyExisting = false) {
    st.busy = 'previewing';
    emit();
    const body = applyExisting ? { ...patch, apply_existing: true } : patch;
    let p;
    try { p = await api.previewPrefs(body, st.etag, { signal: ctl.signal }); } catch (e) {
      if (!live() || aborted(e)) return;
      return failSave(e, patch);
    }
    if (!live()) return;
    if (!p || !Array.isArray(p.risks) || !p.risks.length) return commit(patch, null, applyExisting, true);
    // Checked before the token is exposed: a risk this page cannot describe
    // is never offered for confirmation.
    const refusal = previewRefusal(p);
    if (refusal) return refuse(refusal);
    st.busy = 'confirming';
    st.confirm = { patch, preview: p, applyExisting, keys: patchKeys(patch) };
    say('Confirmation needed before saving.');
  }

  // refuse drops a preview that cannot be confirmed here, with its token.
  // The draft stays for the operator to change or keep.
  function refuse(text) {
    st.busy = '';
    st.confirm = null;
    st.saveError = { code: 'prefs_risk_unknown', text };
    say(text);
  }

  async function commit(patch, token, applyExisting = false, previewed = !!token) {
    const keys = patchKeys(patch);
    st.busy = 'saving';
    st.confirm = null;
    say(`Saving ${keys.length} change${keys.length === 1 ? '' : 's'}…`);
    emit();
    const body = { ...patch };
    if (token) body.confirm = [token];
    if (applyExisting) body.apply_existing = true;
    try {
      const generation = ++requestGeneration;
      const r = await api.patchPrefs(body, st.etag, { signal: ctl.signal });
      if (!live()) return;
      if (!validSnapshot(r.body)) throw { status: 0, code: 'network', message: 'unreadable reply' };
      acceptRuntime(r.body, generation);
      applySaved(r, keys);
    } catch (e) {
      if (!live() || aborted(e)) return;
      await failSave(e, patch, previewed);
    }
  }

  // save: one atomic PATCH of every dirty hub key. A key the hub marks
  // dangerous is previewed first, so a risk is confirmed before any write.
  async function save() {
    if (st.busy || st.phase !== 'ready') return;
    const errs = errors();
    const keys = dirtyKeys();
    if (!keys.length) return;
    const local = Object.keys(errs).filter(k => !st.serverErrors[k]);
    if (local.length) { st.focus = local.sort()[0]; say('Fix the highlighted setting before saving.'); emit(); return; }
    const patch = buildPatch(st.rows, st.draft);
    if (patch.error) { st.focus = patch.key; emit(); return; }
    st.serverErrors = {};
    st.saveError = null;
    st.saved = null;
    const risky = patchKeys(patch).some(k => (rowOf(k).danger || []).length);
    if (risky) await preview(patch);
    else await commit(patch, null);
    emit();
  }

  // confirm commits the previewed patch with its token. applyExisting, when
  // the operator ticked it, needs its own second preview first.
  async function confirm({ applyExisting = false } = {}) {
    const c = st.confirm;
    if (!c || st.busy !== 'confirming') return;
    // The same check as preview, again before the token is redeemed.
    const refusal = previewRefusal(c.preview);
    if (refusal) { refuse(refusal); emit(); return; }
    if (applyExisting && !c.applyExisting) { st.confirm = null; await preview(c.patch, true); emit(); return; }
    await commit(c.patch, c.preview.confirm_token, c.applyExisting);
    emit();
  }
  function cancelConfirm() {
    if (!st.confirm) return;
    st.confirm = null;
    st.busy = '';
    say('Cancelled. Nothing was saved; your edits are kept.');
    emit();
  }

  // adoptNewer takes the hub's latest values as the base and keeps the
  // operator's edits on top: they are dirty again and need another Save.
  function adoptNewer() {
    const n = st.newer;
    if (!n || st.busy) return;
    if (!n.body) { st.newer = null; load(); return; }
    adopt(n.body, n.etag);
    st.newer = null;
    st.serverErrors = {};
    st.saveError = null;
    say('Loaded the latest settings. Your edits are on top; review them and Save again.');
    emit();
  }

  function setView(view) {
    st.view = view === 'policy' ? 'policy' : 'list';
    rememberView(storage, st.view);
    emit();
  }
  function setFilter({ q, only } = {}) {
    if (typeof q === 'string') st.q = q;
    if (only === 'all' || only === 'custom' || only === 'flags') st.only = only;
    emit();
  }
  function setOpen(key) { st.open = st.open === key ? '' : key || ''; emit(); }
  // showField asks the view to bring one field into view and focus it.
  function showField(key) {
    if (!rowOf(key)) return;
    st.focus = key;
    emit();
  }
  // takeFocus hands the view the field to focus, once. In List, a field the
  // search or the All / Customised / Flags filter hides is revealed first by
  // clearing both, so Show and Save can always reach the field to fix.
  function takeFocus() {
    const f = st.focus;
    st.focus = '';
    if (f && st.view === 'list' && rowOf(f) && !visibleRows(self).some(r => r.key === f)) {
      st.q = '';
      st.only = 'all';
      say('Cleared the search and filter to show the setting to fix.');
    }
    return f;
  }

  // ---------- appearance: local to this browser ----------
  function setAppearance(field, v) {
    const ok = (field === 'theme' && ['system', 'dark', 'light'].includes(v)) || (field === 'density' && ['comfortable', 'compact'].includes(v));
    if (!ok) return;
    st.appearance = { ...st.appearance, draft: { ...st.appearance.draft, [field]: v }, note: '' };
    emit();
  }
  const appearanceDirty = () => ['theme', 'density'].some(k => st.appearance.draft[k] !== st.appearance.saved[k]);
  function applyAppearance() {
    if (!appearance || !appearanceDirty()) return;
    const r = appearance.set(st.appearance.draft);
    const now = appearance.get();
    st.appearance = { saved: { ...now }, draft: { ...now }, note: r && r.persisted === false ? 'This browser blocks storage, so this look lasts until you reload.' : '' };
    say('Appearance applied to this browser only.');
    emit();
  }
  function undoAppearance() {
    st.appearance = { ...st.appearance, draft: { ...st.appearance.saved }, note: '' };
    emit();
  }
  if (appearance && appearance.subscribe) {
    offAppearance = appearance.subscribe(next => {
      const was = st.appearance;
      const clean = !appearanceDirty();
      st.appearance = { saved: { ...next }, draft: clean ? { ...next } : was.draft, note: was.note };
      emit();
    });
  }

  function destroy() {
    dead = true;
    ctl.abort();
    offAppearance();
    subs.clear();
    st.confirm = null;
    st.draft.clear();
  }

  const self = {
    state: st, load, refresh, edit, resetKey, revert, undo, save, confirm, cancelConfirm, adoptNewer,
    setView, setFilter, setOpen, showField, takeFocus, setAppearance, applyAppearance, undoAppearance, appearanceDirty,
    value, input, errors, source, dirtyKeys, rowOf, destroy,
    subscribe(fn) { subs.add(fn); return () => subs.delete(fn); },
  };
  return self;
}

// changedKeys: keys whose value or source differs between two row lists.
export function changedKeys(before, after) {
  const old = new Map(before.map(r => [r.key, r]));
  return after.filter(r => { const o = old.get(r.key); return !o || o.value !== r.value || o.source !== r.source; }).map(r => r.key).sort();
}

// visibleRows: the List view's search and All / Customised / Flags filter.
// Customised means a stored override or an unsaved edit.
export function visibleRows(store) {
  const st = store.state, q = st.q.trim().toLowerCase();
  return st.rows.filter(r => {
    if (st.only === 'custom' && r.source !== 'stored' && store.source(r.key) !== 'edited') return false;
    if (st.only === 'flags' && r.source !== 'flag') return false;
    if (!q) return true;
    return [r.label, r.description || '', r.key].some(s => s.toLowerCase().includes(q));
  });
}

// laneView: one Policy lane, from the shared draft. With archive mode on,
// the history lane is dormant (forever) while its configured value stays
// visible; the acknowledged-events lane is never paused by archive mode.
export function laneView(store, lane) {
  const row = store.rowOf(lane.key);
  if (!row) return null;
  const value = store.value(lane.key);
  const archive = store.rowOf('retention.archive_mode');
  const paused = !!lane.archive && !!archive && store.value('retention.archive_mode') === true;
  const seconds = paused ? Infinity : secondsOf(row, value);
  return {
    lane, row, value, paused, seconds, position: lanePosition(seconds), lock: lockOf(row),
    text: paused ? `Forever while archive mode is on (configured: ${formatValue(row, value)})` : seconds === Infinity ? formatValue(row, value) : `${formatValue(row, value)}, ${lane.then}`,
    stops: laneStops(row, value, row.value).map(s => ({ ...s, position: lanePosition(s.seconds), on: s.value === value })),
  };
}
