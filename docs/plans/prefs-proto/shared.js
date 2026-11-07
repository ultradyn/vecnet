// PROTOTYPE (throwaway): Settings / Preferences page for the vecnet operator UI.
// Mock prefs + an in-memory store shared by every variant. Nothing persists:
// reload and everything resets. No network calls.

export const GB = 1e9, MB = 1e6;
export const HUB = { host: 'max.vecnet.ai', hosted: true, plan: 'Hosted · 10 GB included', quota: 10 * GB, overage: '$0.15 / GB-month' };
export const STORAGE = [
  { k: 'files', label: 'Files', bytes: 1.62 * GB, color: '#a855f7', perDay: 120 * MB },
  { k: 'msgs', label: 'Messages', bytes: 0.61 * GB, color: '#6366f1', perDay: 9 * MB },
  { k: 'events', label: 'Events', bytes: 0.18 * GB, color: '#38bdf8', perDay: 6 * MB },
  { k: 'audit', label: 'Audit log', bytes: 0.07 * GB, color: '#8391ad', perDay: 1 * MB },
];
export const usedBytes = () => STORAGE.reduce((a, s) => a + s.bytes, 0);
export const TRIM = { msgs: 4210, events: 18902, bytes: 612 * MB };

export const SECTIONS = [
  { id: 'retention', title: 'Retention & storage', blurb: 'How long the hub keeps what it has delivered.' },
  { id: 'files', title: 'Files', blurb: 'Attachments that agents and you upload.' },
  { id: 'delivery', title: 'Delivery & events', blurb: 'How messages wake agents, and how often MCP clients refresh.' },
  { id: 'agents', title: 'Agents', blurb: 'Limits that apply to every peer on this hub.' },
  { id: 'appearance', title: 'Appearance', blurb: 'Just for you. Other operators keep their own.' },
];

const D = (v, l, days) => ({ v, l, days });
export const PREFS = [
  { id: 'archive', sec: 'retention', key: 'retention.archive_mode', label: 'Archive mode', type: 'toggle', def: false, value: true, src: 'saved',
    help: 'Keep every message and acknowledged event forever. Files still follow File expiry. Turning it off never deletes anything without asking first.' },
  { id: 'history', sec: 'retention', key: 'retention.message_history', label: 'Message history', type: 'enum', def: '90d', value: '90d', src: 'default',
    opts: [D('30d', '30 days', 30), D('90d', '90 days', 90), D('forever', 'Forever', Infinity)],
    help: 'Messages older than this are removed at the nightly sweep (02:00).' },
  { id: 'acked', sec: 'retention', key: 'retention.acked_events', label: 'Acknowledged-event retention', type: 'enum', def: '7d', value: '7d', src: 'default',
    opts: [D('1d', '1 day', 1), D('3d', '3 days', 3), D('7d', '7 days', 7), D('14d', '14 days', 14), D('30d', '30 days', 30)],
    help: 'How long an event stays after its peer acknowledges it. Unacknowledged events are always kept.' },
  { id: 'expiry', sec: 'files', key: 'files.expiry', label: 'File expiry', type: 'enum', def: '24h', value: '24h', src: 'default',
    opts: [D('1h', '1 hour', 1 / 24), D('24h', '24 hours', 1), D('7d', '7 days', 7), D('30d', '30 days', 30), D('off', 'Off (keep files)', Infinity)],
    help: 'Uploaded files are deleted this long after their last download.' },
  { id: 'maxfile', sec: 'files', key: 'files.max_size_mb', label: 'Max file size', type: 'num', unit: 'MB', min: 1, max: 100, def: 25, value: 50, src: 'saved',
    help: 'Largest single upload. Hosted hubs cap this at 100 MB.' },
  { id: 'quota', sec: 'files', key: 'files.daily_quota_gb', label: 'Daily upload quota', type: 'num', unit: 'GB / peer / day', min: 0.1, max: 50, def: 5, value: 2, src: 'flag', flag: '--upload-quota=2GB',
    help: 'Uploads past the quota are refused until midnight UTC.' },
  { id: 'refresh', sec: 'delivery', key: 'events.mcp_refresh', label: 'MCP Events refresh', type: 'enum', def: '30m', value: '30m', src: 'default',
    opts: [D('5m', '5 min'), D('15m', '15 min'), D('30m', '30 min'), D('60m', '60 min')],
    help: 'How often MCP clients that cannot hold a stream re-read their Events resource.' },
  { id: 'prio', sec: 'delivery', key: 'delivery.routine_priority', label: 'Routine-woken priority', type: 'enum', def: 'hold', value: 'wake_idle', src: 'saved',
    opts: [D('hold', 'hold'), D('wake_idle', 'wake_idle')],
    help: 'Default priority for messages sent by an agent a routine woke. hold waits for the next check-in; wake_idle wakes idle agents only.' },
  { id: 'depth', sec: 'delivery', key: 'loop.max_depth', label: 'Loop depth', type: 'num', unit: 'hops', min: 1, max: 32, def: 8, value: 8, src: 'default',
    help: 'A reply chain longer than this is cut and reported on the dashboard.' },
  { id: 'ttl', sec: 'delivery', key: 'loop.ttl', label: 'Message TTL', type: 'enum', def: '24h', value: '24h', src: 'default',
    opts: [D('1h', '1 hour', 1 / 24), D('6h', '6 hours', 0.25), D('24h', '24 hours', 1), D('72h', '72 hours', 3)],
    help: 'An undelivered message expires after this and is dead-lettered.' },
  { id: 'rate', sec: 'agents', key: 'peers.rate_per_min', label: 'Rate limit per peer', type: 'num', unit: 'msgs / min', min: 1, max: 600, def: 60, value: 60, src: 'default',
    help: 'Sustained sends per peer. Over the limit, sends get 429 and the peer backs off.' },
  { id: 'burst', sec: 'agents', key: 'peers.burst', label: 'Burst allowance', type: 'num', unit: 'msgs', min: 1, max: 200, def: 20, value: 20, src: 'default',
    help: 'Extra sends a quiet peer may make at once. Cannot exceed the per-minute rate.' },
  { id: 'maxpeers', sec: 'agents', key: 'peers.max', label: 'Max peers', type: 'num', unit: 'peers', min: 1, max: 500, def: 100, value: 50, src: 'flag', flag: '--max-peers=50',
    help: 'Add agent is refused once the hub holds this many peers.' },
  { id: 'invites', sec: 'agents', key: 'peers.open_invites', label: 'Open invites', type: 'num', unit: 'invites', min: 1, max: 50, def: 10, value: 10, src: 'default',
    help: 'Unclaimed invites allowed at once. Expired invites do not count.' },
  { id: 'theme', sec: 'appearance', key: 'ui.theme', label: 'Theme', type: 'enum', def: 'dark', value: 'dark', src: 'default', personal: true,
    opts: [D('dark', 'Dark'), D('light', 'Light'), D('system', 'System')], help: 'Light is a preview: the light palette is not built yet.' },
  { id: 'density', sec: 'appearance', key: 'ui.density', label: 'Density', type: 'enum', def: 'comfortable', value: 'comfortable', src: 'default', personal: true,
    opts: [D('comfortable', 'Comfortable'), D('compact', 'Compact')], help: 'Compact fits more rows in the inbox and the dashboard.' },
  { id: 'ts', sec: 'appearance', key: 'ui.timestamps', label: 'Timestamps', type: 'enum', def: 'relative', value: 'relative', src: 'saved', personal: true,
    opts: [D('relative', '2m ago'), D('clock', '14:02'), D('iso', '2026-11-05T14:02Z')], help: 'How times show in lists. Hover always shows the full time.' },
];
export const P = Object.fromEntries(PREFS.map(p => [p.id, p]));
export const bySec = id => PREFS.filter(p => p.sec === id);

// ---------- store ----------
export const S = {
  saved: Object.fromEntries(PREFS.map(p => [p.id, p.value])),
  draft: Object.fromEntries(PREFS.map(p => [p.id, p.value])),
  src: Object.fromEntries(PREFS.map(p => [p.id, p.src])),
  status: 'idle', // idle | saving
  confirm: null, // {id, v, choice, typed}
  trim: false, // operator chose to trim existing history at next sweep
  justSaved: new Set(),
  freeze: false,
};
const subs = new Set();
export const subscribe = fn => (subs.add(fn), () => subs.delete(fn));
export const emit = () => subs.forEach(fn => fn());

const same = (a, b) => String(a) === String(b);
export const isDirty = id => !same(S.draft[id], S.saved[id]);
export const dirtyIds = () => PREFS.filter(p => isDirty(p.id)).map(p => p.id);
export function lockReason(p) {
  if (S.src[p.id] === 'flag') return `Set by ${p.flag} when the hub started. Remove the flag to edit it here.`;
  if ((p.id === 'history' || p.id === 'acked') && S.draft.archive === true) return 'Paused while archive mode is on.';
  return '';
}
export function errors() {
  const e = {};
  for (const p of PREFS) {
    if (p.type !== 'num' || lockReason(p)) continue;
    const raw = String(S.draft[p.id]).trim();
    const n = Number(raw);
    if (raw === '' || !Number.isFinite(n)) e[p.id] = 'Enter a number.';
    else if (p.id === 'maxfile' && n > 100) e[p.id] = 'Hosted hubs cap uploads at 100 MB.';
    else if (n < p.min || n > p.max) e[p.id] = `Must be between ${p.min} and ${p.max}.`;
  }
  if (!e.burst && !e.rate && Number(S.draft.burst) > Number(S.draft.rate)) e.burst = `Cannot exceed the rate limit (${S.draft.rate} / min).`;
  return e;
}
// source shown for a pref: edited (unsaved) wins, then saved/flag/default
export const srcOf = id => (isDirty(id) ? 'edited' : S.src[id]);
export const isDanger = (id, v) => (id === 'archive' && v === false && S.draft.archive === true) || (id === 'expiry' && v === 'off' && S.draft.expiry !== 'off');

export function set(id, v) {
  const p = P[id];
  if (!p || lockReason(p) || S.status === 'saving') return;
  if (p.type === 'num') v = String(v);
  if (isDanger(id, v)) { S.confirm = { id, v, choice: 'new', typed: '' }; emit(); return; }
  S.draft[id] = v;
  if (id === 'archive' && v === true) S.trim = false;
  S.justSaved.clear();
  emit();
}
export function cycle(id, dir = 1) {
  const p = P[id];
  if (p.type === 'toggle') return set(id, !S.draft[id]);
  if (p.type === 'enum') {
    const i = p.opts.findIndex(o => o.v === S.draft[id]);
    return set(id, p.opts[(i + dir + p.opts.length) % p.opts.length].v);
  }
  const step = p.max > 100 ? 10 : 1;
  const n = Number(S.draft[id]);
  set(id, String(Math.max(p.min, Math.min(p.max, (Number.isFinite(n) ? n : p.def) + dir * step))));
}
export const resetDefault = id => set(id, P[id].def);
export function confirmOk() {
  const c = S.confirm; if (!c) return;
  if (c.id === 'archive' && c.choice === 'trim' && c.typed.trim().toLowerCase() !== 'trim') return;
  S.draft[c.id] = c.v;
  if (c.id === 'archive') S.trim = c.choice === 'trim';
  S.confirm = null; emit();
}
export function confirmCancel() { S.confirm = null; emit(); }
export function undo() { if (S.status === 'saving') return; S.draft = { ...S.saved }; S.trim = false; emit(); }
export function save() {
  const ids = dirtyIds();
  if (!ids.length || Object.keys(errors()).length || S.status === 'saving') return;
  S.status = 'saving'; emit();
  if (S.freeze) return;
  setTimeout(() => finishSave(ids), 1100);
}
export function finishSave(ids = dirtyIds()) {
  for (const id of ids) {
    S.saved[id] = S.draft[id];
    S.src[id] = same(S.draft[id], P[id].def) ? 'default' : 'saved';
  }
  S.justSaved = new Set(ids);
  const trimmed = S.trim; S.trim = false;
  S.status = 'idle';
  emit();
  toast(`Saved ${ids.length} change${ids.length === 1 ? '' : 's'}.` + (trimmed ? ' Existing history is trimmed at 02:00.' : ' Retention changes apply at the next sweep.'));
}

// ---------- formatting ----------
export function fmt(p, v = S.draft[p.id]) {
  if (p.type === 'toggle') return v ? 'On' : 'Off';
  if (p.type === 'num') return `${v} ${p.unit}`;
  return p.opts.find(o => o.v === v)?.l ?? String(v);
}
export const fmtShort = (p, v = S.draft[p.id]) => (p.type === 'num' ? String(v) : p.type === 'toggle' ? (v ? 'on' : 'off') : String(v));
export const gb = b => (b >= GB ? `${(b / GB).toFixed(2)} GB` : `${Math.round(b / MB)} MB`);
export const esc = s => String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
export const SRC_LABEL = { default: 'default', flag: 'flag', saved: 'saved', edited: 'edited' };
export const srcChip = id => {
  const s = srcOf(id);
  const t = s === 'flag' ? `title="${esc(P[id].flag)}"` : '';
  return `<span class="src s-${s}" ${t}>${SRC_LABEL[s]}</span>`;
};

// ---------- toast ----------
let toastTimer;
export function toast(msg, { bad = false, sticky = false } = {}) {
  const t = document.getElementById('toast');
  t.textContent = msg; t.classList.toggle('bad', bad); t.classList.add('on');
  clearTimeout(toastTimer);
  if (!sticky) toastTimer = setTimeout(() => t.classList.remove('on'), 3600);
}

// ---------- save bar (GUI variants) ----------
export function saveBar() {
  const ids = dirtyIds(), errs = Object.keys(errors());
  if (!ids.length && S.status !== 'saving') return '<div class="savebar" hidden></div>';
  const n = ids.length;
  let msg, cls = '';
  if (S.status === 'saving') { msg = `<span class="spin"></span>Saving ${n} change${n === 1 ? '' : 's'}…`; cls = 'saving'; }
  else if (errs.length) { msg = `<b>${errs.length} setting${errs.length === 1 ? '' : 's'} need${errs.length === 1 ? 's' : ''} fixing</b> <button class="linkbtn jump" data-act="jump">Show</button>`; cls = 'bad'; }
  else msg = `<b>${n} unsaved change${n === 1 ? '' : 's'}</b>` + (S.trim ? `<span class="sb-warn">Trims ${TRIM.msgs.toLocaleString('en-AU')} messages at 02:00</span>` : '');
  const busy = S.status === 'saving';
  return `<div class="savebar ${cls}" role="region" aria-label="Unsaved changes"><span class="sb-msg">${msg}</span>
    <span class="sb-acts"><button class="btn" data-act="undo" ${busy ? 'disabled' : ''}>Undo</button>
    <button class="btn pri" data-act="save" ${busy || errs.length ? 'disabled' : ''}>${busy ? 'Saving…' : 'Save changes'}</button></span></div>`;
}

// ---------- shared control wiring (data-attributes) ----------
export function bindControls(root) {
  root.addEventListener('click', e => {
    const b = e.target.closest('[data-set],[data-tog],[data-act],[data-reset],[data-step]');
    if (!b || b.disabled) return;
    if (b.dataset.set) set(b.dataset.set, b.dataset.v === 'true' ? true : b.dataset.v === 'false' ? false : b.dataset.v);
    else if (b.dataset.tog) set(b.dataset.tog, !S.draft[b.dataset.tog]);
    else if (b.dataset.reset) resetDefault(b.dataset.reset);
    else if (b.dataset.step) cycle(b.dataset.step, Number(b.dataset.dir));
    else if (b.dataset.act === 'save') save();
    else if (b.dataset.act === 'undo') undo();
    else if (b.dataset.act === 'jump') {
      const id = Object.keys(errors())[0];
      const el = root.querySelector(`[data-row="${id}"]`);
      el?.scrollIntoView({ block: 'center', behavior: 'smooth' });
      el?.querySelector('input')?.focus({ preventScroll: true });
    }
  });
  root.addEventListener('input', e => { const i = e.target.closest('[data-num]'); if (i) set(i.dataset.num, i.value); });
  root.addEventListener('change', e => { const s = e.target.closest('select[data-sel]'); if (s) set(s.dataset.sel, s.value); });
}

// Re-render without losing focus or the caret in a number input.
export function rerender(root, html) {
  const a = document.activeElement;
  const key = a && root.contains(a) ? a.dataset.fk : null;
  const sel = key && a.selectionStart != null ? [a.selectionStart, a.selectionEnd] : null;
  root.innerHTML = html;
  if (key) {
    const n = root.querySelector(`[data-fk="${CSS.escape(key)}"]`);
    if (n) { n.focus({ preventScroll: true }); if (sel && n.setSelectionRange) try { n.setSelectionRange(...sel); } catch {} }
  }
}

// ---------- confirm dialog (GUI) ----------
export function confirmBody(c) {
  if (c.id === 'archive') {
    const h = P.history, a = P.acked;
    return {
      title: 'Turn off archive mode?',
      lede: `Archive mode keeps every message and acknowledged event. With it off, the hub keeps messages for <b>${fmt(h)}</b> and acknowledged events for <b>${fmt(a)}</b>.`,
      choices: [
        { v: 'new', t: 'Only items that arrive from now on', d: 'Nothing already on the hub is deleted. Recommended.' },
        { v: 'trim', t: 'Also trim what is already here', d: `Deletes ${TRIM.msgs.toLocaleString('en-AU')} messages and ${TRIM.events.toLocaleString('en-AU')} events (${gb(TRIM.bytes)}) at the 02:00 sweep. This cannot be undone.`, danger: true },
      ],
      ok: 'Turn off archive mode',
      note: 'Takes effect when you save. You can turn archive mode back on at any time.',
    };
  }
  const files = STORAGE.find(s => s.k === 'files');
  const days = Math.round((HUB.quota - usedBytes()) / files.perDay);
  return {
    title: 'Keep files forever?',
    lede: `Files stop expiring. They use <b>${gb(files.bytes)}</b> now and grow about <b>${gb(files.perDay)} a day</b>, so this hub passes its 10 GB plan in about <b>${days} days</b>.`,
    warn: `Hosted hub: storage over the plan is billed at ${HUB.overage} (estimate).`,
    ok: 'Keep files',
    note: 'Takes effect when you save. Turning expiry back on does not delete files until each one passes the new expiry.',
  };
}
let dlg;
export function syncConfirm() {
  const c = S.confirm;
  if (!dlg) {
    dlg = document.createElement('dialog'); dlg.className = 'cf'; document.body.append(dlg);
    dlg.addEventListener('cancel', e => { e.preventDefault(); confirmCancel(); });
    dlg.addEventListener('click', e => {
      const b = e.target.closest('button'); if (!b) return;
      if (b.dataset.cf === 'ok') confirmOk(); else if (b.dataset.cf === 'no') confirmCancel();
    });
    dlg.addEventListener('change', e => { if (e.target.name === 'cf-choice') { S.confirm.choice = e.target.value; paint(); } });
    dlg.addEventListener('input', e => { if (e.target.id === 'cf-typed') { S.confirm.typed = e.target.value; okState(); } });
  }
  const tui = document.body.dataset.variant === 'C';
  dlg.classList.toggle('cftui', tui);
  if (!c) { if (dlg.open) dlg.close(); dlg.dataset.for = ''; return; }
  if (dlg.dataset.for !== c.id) { dlg.dataset.for = c.id; paint(); }
  if (!dlg.open) dlg.showModal();
}
function okState() {
  const c = S.confirm, ok = dlg.querySelector('[data-cf=ok]');
  if (ok) ok.disabled = c.id === 'archive' && c.choice === 'trim' && c.typed.trim().toLowerCase() !== 'trim';
}
function paint() {
  const c = S.confirm, b = confirmBody(c);
  const ch = b.choices ? `<div class="cf-choices">${b.choices.map(x => `<label class="cf-ch ${x.danger ? 'dg' : ''} ${c.choice === x.v ? 'on' : ''}"><input type="radio" name="cf-choice" value="${x.v}" ${c.choice === x.v ? 'checked' : ''}><span><b>${x.t}</b><small>${x.d}</small></span></label>`).join('')}</div>` : '';
  const typed = c.choice === 'trim' ? `<label class="cf-type"><span>Type <code>trim</code> to confirm</span><input id="cf-typed" autocomplete="off" spellcheck="false" value="${esc(c.typed)}"></label>` : '';
  dlg.innerHTML = `<form method="dialog" class="cf-in"><div class="cf-ic" aria-hidden="true">!</div><h2>${b.title}</h2><p class="cf-lede">${b.lede}</p>
    ${b.warn ? `<p class="cf-warn">${b.warn}</p>` : ''}${ch}${typed}<p class="cf-note">${b.note}</p>
    <div class="cf-acts"><button type="button" class="btn" data-cf="no">Cancel</button><button type="button" class="btn dgr" data-cf="ok">${b.ok}</button></div></form>`;
  okState();
  const t = dlg.querySelector('#cf-typed'); if (t) t.focus();
}

// ---------- app shell (the real sidebar, static) ----------
export function shellSide() {
  const th = [['Delivery health 5', 'grok-triage', 'now', 2], ['Quota exhausted', 'grok-triage', '1s ago', 1], ['Daily digest', 'pag-digest', '1s ago', 0], ['Ticket 18: operator UI', 'codex-hark', '1s ago', 0], ['Vecnet hosting', 'codex-hark', '1s ago', 0]];
  return `<nav class="side" aria-label="Inbox and threads">
    <div class="brand"><span class="logo"></span>vecnet<span class="hub">${HUB.host}</span></div>
    <div class="nav">
      <a href="#"><span class="tt">Inbox</span><span class="cnt hot">11</span></a>
      <a href="#"><span class="tt">New message</span></a>
      <a href="#"><span class="tt">Dashboard</span></a>
      <a href="#" class="on"><span class="tt">Settings</span>${dirtyIds().length ? `<span class="cnt dirty">${dirtyIds().length}</span>` : ''}</a>
      <button type="button" class="nav-add"><span class="plus" aria-hidden="true"></span><span class="tt">Add agent</span></button>
    </div>
    <div class="grp">You</div><div class="me"><i class="dot working"></i>max is working</div>
    <div class="grp">Your Threads</div>
    <div class="nav rows">${th.map(([t, p, a, n]) => `<a href="#" class="${n ? 'unread' : ''}"><span class="tt">${t}</span><span class="nums">${n ? `<span class="ur">${n}</span>` : '<span class="cnt">1</span>'}</span><span class="pp">${p}</span><span class="ago">${a}</span></a>`).join('')}</div>
    <div class="conn live"><i></i><span>live</span></div>
  </nav>`;
}
export const mobileTop = () => `<header class="mtop"><span class="logo"></span><b>vecnet</b><span class="crumb">Settings</span><span class="mhub">${HUB.host}</span></header>`;

// ---------- forced demo states (?state=) ----------
export function resetStore() {
  for (const p of PREFS) { S.saved[p.id] = p.value; S.draft[p.id] = p.value; S.src[p.id] = p.src; }
  S.status = 'idle'; S.confirm = null; S.trim = false; S.justSaved = new Set(); S.freeze = false;
  document.getElementById('toast')?.classList.remove('on');
}
export const STATES = ['initial', 'dirty', 'saving', 'saved', 'error', 'confirm-archive', 'confirm-trim', 'confirm-files'];
function edits() { S.draft.expiry = '7d'; S.draft.refresh = '15m'; S.draft.maxfile = '64'; S.draft.prio = 'hold'; S.draft.density = 'compact'; }
export function applyState(st) {
  resetStore();
  if (st === 'dirty') edits();
  else if (st === 'saving') { edits(); S.freeze = true; save(); return; }
  else if (st === 'saved') { edits(); finishSave(); stickyToast(); }
  else if (st === 'error') { edits(); S.draft.maxfile = '500'; S.draft.burst = '90'; }
  else if (st === 'confirm-archive') { set('archive', false); return; }
  else if (st === 'confirm-trim') { set('archive', false); S.confirm.choice = 'trim'; S.confirm.typed = 'tri'; const d = document.querySelector('dialog.cf'); if (d) d.dataset.for = ''; }
  else if (st === 'confirm-files') { set('expiry', 'off'); return; }
  emit();
}
function stickyToast() { const t = document.getElementById('toast'); clearTimeout(toastTimer); t.classList.add('on'); }
