// Settings fixtures: rows shaped like GET /v1/admin/prefs (internal/prefs
// registry), plus a fake hub that answers prefs/patch/preview the way
// internal/hub/preferences.go does: If-Match checks, default normalization,
// presence-shortening risks and HMAC-free fake tokens. Not a test file.

const n = (key, type, def, min, max, group, label, extra = {}) => ({
  key, type, default: def, value: def, source: 'default', origin: '', min, max, unit: type === 'duration' ? 'seconds' : '',
  label, description: label + ' help.', group, settable_by: 'operator', editable: true, runtime: 'live', danger: [], ...extra,
});
const gated = (row, why) => ({ ...row, editable: false, runtime: 'unavailable', unavailable_reason: why });
const later = (row, why) => ({ ...row, editable: false, runtime: 'bootstrap', unavailable_reason: why });

export function rows({ installed = false } = {}) {
  const g = installed ? r => r : gated;
  const list = [
    g(n('retention.ack_events', 'duration', 604800, 3600, 31536000, 'retention_storage', 'Acknowledged event retention', { disabled_value: 0, danger: ['retention_shortened'] }), 'DB retention adapter is not installed.'),
    g(n('retention.history', 'duration', 0, 86400, 315360000, 'retention_storage', 'History retention', { disabled_value: 0, danger: ['retention_shortened'] }), 'DB retention adapter is not installed.'),
    g({ ...n('retention.archive_mode', 'bool', false, undefined, undefined, 'retention_storage', 'Archive mode', { danger: ['archive_disabled'] }), unit: '' }, 'DB retention adapter is not installed.'),
    g({ ...n('blobs.expiry', 'enum', '1d', undefined, undefined, 'files', 'Attachment expiry', { danger: ['blob_expiry_off'], allowed_values: ['1h', '6h', '12h', '1d', '7d', '30d', 'off'] }) }, 'R2 expiry adapter is not installed.'),
    n('events.refresh_interval', 'duration', 1800, 60, 86400, 'notifications_presence', 'Events refresh interval'),
    n('presence.default_ttl', 'duration', 3600, 60, 86400, 'notifications_presence', 'Default presence TTL', { danger: ['presence_shortened'] }),
    n('presence.session_ttl', 'duration', 900, 60, 86400, 'notifications_presence', 'Session presence TTL', { danger: ['presence_shortened'] }),
    n('agents.default_max_depth', 'int', 32, 1, 64, 'agents_defaults', 'Default maximum depth'),
    n('agents.default_ttl', 'duration', 86400, 60, 604800, 'agents_defaults', 'Default message TTL'),
    later(n('agents.max_expiry', 'duration', 604800, 86400, 604800, 'agents_defaults', 'Maximum message expiry'), 'Explicit loop expiry is a protocol policy cap; runtime editing is deferred.'),
    n('rates.sends_per_minute', 'int', 60, 1, 600, 'agents_defaults', 'Default sends per minute'),
    later(n('provisioning.max_peers', 'int', 0, 0, 10000, 'agents_defaults', 'Default maximum peers', { disabled_value: 0 }), 'Provisioning policy migration is deferred.'),
  ];
  return list.sort((a, b) => (a.key < b.key ? -1 : 1));
}

// storage, when set, is the operator-only R2 figures the hub adds beside the
// preferences (internal/httpapi/blob_capabilities.go writePreferencesSnapshot).
export function fakeHub({ preferences = rows(), revision = 3, storage = null } = {}) {
  const hub = { revision, preferences: preferences.map(r => ({ ...r })), calls: [], fail: null, gen: 'g1', storage };
  const etag = () => `"prefs-${hub.revision}-${hub.gen}"`;
  const snapshot = () => ({ revision: hub.revision, preferences: hub.preferences.map(r => ({ ...r })), ...(hub.storage ? { storage: { ...hub.storage } } : {}) });
  const err = (status, code, details = {}) => Object.assign(new Error(code), { status, code, details });
  // Risks compare before and after, as preferenceRisks does: a reset that
  // shortens a presence TTL is as risky as a set.
  const risks = patch => {
    const out = [];
    const after = key => {
      const row = hub.preferences.find(r => r.key === key);
      if (Object.hasOwn(patch.set || {}, key)) return patch.set[key];
      if ((patch.reset || []).includes(key)) return row.default;
      return row.value;
    };
    for (const row of hub.preferences) {
      const v = after(row.key);
      if (v === row.value) continue;
      if (row.key.startsWith('presence.') && v < row.value) out.push({ code: 'presence_shortened', keys: [row.key], message: 'Shorter presence TTL may release held wakes.' });
      if (row.key === 'retention.archive_mode' && row.value === true && v === false) out.push({ code: 'archive_disabled', keys: [row.key], message: 'Disabling archive protection permits future history expiry.' });
      if (row.key === 'blobs.expiry' && v === 'off') out.push({ code: 'blob_expiry_off', keys: [row.key], message: 'New attachments have no disaster expiry; retained and reserved storage incurs cost within the verified finite quota.' });
    }
    if (patch.apply_existing) out.push({ code: 'retention_apply_existing', keys: [...Object.keys(patch.set || {}), ...(patch.reset || [])], message: 'Apply to existing data.' });
    return out.concat(hub.extraRisks ? hub.extraRisks(patch) : []);
  };
  const digest = patch => JSON.stringify({ set: patch.set || {}, reset: [...(patch.reset || [])].sort(), apply_existing: !!patch.apply_existing });
  hub.api = {
    async prefs({ etag: tag } = {}) {
      hub.calls.push({ op: 'get', tag });
      if (hub.fail && hub.fail.get) throw hub.fail.get;
      if (tag && tag === etag()) return { status: 304, etag: etag(), body: null };
      return { status: 200, etag: etag(), body: snapshot() };
    },
    async previewPrefs(patch, tag) {
      hub.calls.push({ op: 'preview', patch: structuredClone(patch), tag });
      if (hub.fail && hub.fail.preview) throw hub.fail.preview;
      if (tag !== etag()) throw err(412, 'prefs_revision_conflict');
      const r = risks(patch);
      const impact = r.some(x => x && x.code === 'blob_expiry_off') && hub.storage ? { storage: { ...hub.storage } } : {};
      return { base_revision: hub.revision, risks: r, impact, confirm_token: 'tok:' + digest(patch), expires_at: '2026-11-06T10:00:00.000Z' };
    },
    async patchPrefs(patch, tag) {
      hub.calls.push({ op: 'patch', patch: structuredClone(patch), tag });
      if (hub.fail && hub.fail.patch) { const f = hub.fail.patch; if (!hub.fail.sticky) hub.fail = null; throw f; }
      if (!tag) throw err(428, 'prefs_precondition_required');
      if (tag !== etag()) throw err(412, 'prefs_revision_conflict');
      const r = risks(patch);
      if (r.length && !(patch.confirm || []).includes('tok:' + digest(patch))) throw err(409, (patch.confirm || []).length ? 'prefs_confirmation_stale' : 'prefs_confirmation_required');
      let changed = false;
      for (const [key, v] of Object.entries(patch.set || {})) {
        const row = hub.preferences.find(x => x.key === key);
        if (!row.editable) throw err(409, 'prefs_unavailable', { field: key });
        if (v === row.default && row.source !== 'flag') { if (row.source === 'stored') { Object.assign(row, { value: v, source: 'default' }); changed = true; } continue; }
        if (row.value !== v || row.source !== 'stored') { Object.assign(row, { value: v, source: 'stored' }); changed = true; }
      }
      for (const key of patch.reset || []) {
        const row = hub.preferences.find(x => x.key === key);
        if (row.source === 'stored') { Object.assign(row, { value: row.default, source: 'default' }); changed = true; }
      }
      if (changed || patch.apply_existing) hub.revision++;
      return { status: 200, etag: etag(), body: snapshot() };
    },
  };
  // another operator's write: bumps the revision behind the page's back.
  hub.competing = (key, value) => {
    const row = hub.preferences.find(r => r.key === key);
    Object.assign(row, { value, source: 'stored' });
    hub.revision++;
  };
  return hub;
}

// memStorage: a localStorage stand-in; blocked throws like a denied browser.
export function memStorage({ blocked = false, init = {} } = {}) {
  const m = new Map(Object.entries(init));
  const deny = () => { throw Object.assign(new Error('denied'), { name: 'SecurityError' }); };
  return {
    map: m,
    getItem: k => (blocked ? deny() : m.has(k) ? m.get(k) : null),
    setItem: (k, v) => (blocked ? deny() : void m.set(k, String(v))),
    removeItem: k => (blocked ? deny() : void m.delete(k)),
  };
}
