// Variant D, "Policy": the surprise. Retention is about time, so it is drawn on a
// log time axis (now -> 1 h -> 1 d -> 1 y -> forever) with a draggable lane per
// thing the hub keeps, and a 90-day storage forecast that reacts. Everything
// else reads as plain sentences with the values as clickable tokens.
import { bindControls, errors, esc, fmt, gb, HUB, isDirty, lockReason, mobileTop, P, rerender, S, saveBar, set, shellSide, srcChip, srcOf, STORAGE, usedBytes } from './shared.js';
import { control } from './a.js';

let el, open = null, drag = null;
const LO = Math.log(1 / 48), HI = Math.log(365);
const pos = d => (d === Infinity ? 96 : d === 0 ? 0 : 5 + ((Math.log(d) - LO) / (HI - LO)) * 82);
const TICKS = [['now', 0], ['1h', 1 / 24], ['6h', 0.25], ['1d', 1], ['7d', 7], ['30d', 30], ['90d', 90], ['1y', 365], ['∞', Infinity]];
const LANES = [
  { id: 'history', label: 'Messages', end: 'deleted' },
  { id: 'acked', label: 'Acknowledged events', end: 'deleted' },
  { id: 'expiry', label: 'Files', end: 'deleted after last download' },
  { id: 'ttl', label: 'Undelivered messages', end: 'dead-lettered' },
];
const days = id => P[id].opts.find(o => o.v === S.draft[id])?.days ?? 0;

function lane(l) {
  const p = P[l.id], paused = (l.id === 'history' || l.id === 'acked') && S.draft.archive;
  const d = paused ? Infinity : days(l.id), x = pos(d), e = errors()[l.id];
  const cls = [paused && 'paused', d === Infinity && 'inf', isDirty(l.id) && 'dirty', S.justSaved.has(l.id) && 'flash', l.id === 'expiry' && S.draft.expiry === 'off' && 'warn'].filter(Boolean).join(' ');
  return `<div class="d-lane ${cls}" data-row="${l.id}">
    <div class="d-ll"><b>${l.label}</b>${srcChip(l.id)}<span class="d-val">${paused ? 'forever · archive' : d === Infinity ? 'forever' : `${fmt(p)}, then ${l.end}`}</span></div>
    <div class="d-track" data-lane="${l.id}" ${paused ? 'aria-disabled="true"' : ''}>
      <div class="d-fill" style="width:${x}%"></div>
      ${p.opts.map(o => `<button type="button" class="d-stop ${o.v === S.draft[l.id] ? 'on' : ''}" style="left:${pos(o.days)}%" data-set="${l.id}" data-v="${o.v}" data-fk="${l.id}-${o.v}" title="${o.l}" aria-label="${l.label}: ${o.l}" ${paused || S.status === 'saving' ? 'disabled' : ''}></button>`).join('')}
      <span class="d-knob" style="left:${x}%"></span>
    </div>${e ? `<p class="d-err">${e}</p>` : ''}</div>`;
}

function forecast() {
  const now = usedBytes();
  const add = { files: S.draft.expiry === 'off' ? 90 : 0, msgs: S.draft.archive || S.draft.history === 'forever' ? 90 : 0, events: S.draft.archive ? 90 : 0, audit: 90 };
  const later = STORAGE.reduce((a, s) => a + s.bytes + s.perDay * add[s.k], 0);
  const over = later > HUB.quota;
  return `<div class="d-fc ${over ? 'over' : ''}"><div class="d-fc-h"><span>Storage</span><b>${gb(now)}</b><span class="d-dim">today</span><span class="d-arrow">&rarr;</span><b class="d-later">${gb(later)}</b><span class="d-dim">in 90 days, with these settings</span></div>
    <div class="d-fc-bar"><i class="now" style="width:${(now / HUB.quota) * 100}%"></i><i class="later" style="width:${Math.min(100, (later / HUB.quota) * 100)}%"></i><span class="cap">10 GB plan</span></div>
    ${over ? `<p class="d-fc-w">That passes the plan. Hosted storage over 10 GB bills at ${HUB.overage}.</p>` : '<p class="d-dim d-fc-n">Expiring files and trimmed history keep this flat.</p>'}</div>`;
}

const tok = id => {
  const p = P[id], s = srcOf(id), e = errors()[id];
  return `<button type="button" class="tok t-${s} ${e ? 'bad' : ''} ${open === id ? 'open' : ''} ${lockReason(p) ? 'lk' : ''}" data-tok="${id}" data-fk="tok-${id}" aria-expanded="${open === id}">${esc(id === 'quota' ? `${S.draft.quota} GB` : id === 'expiry' && S.draft.expiry === 'off' ? 'never expire' : fmt(p))}</button>`;
};
function drawer(ids) {
  if (!ids.includes(open)) return '';
  const p = P[open], lock = lockReason(p), e = errors()[open];
  const notDef = String(S.draft[p.id]) !== String(p.def);
  return `<div class="d-drawer" data-row="${p.id}"><div class="d-dr-h"><b>${p.label}</b>${srcChip(p.id)}<span class="d-key">${p.key}</span><button type="button" class="d-x" data-close aria-label="Close">&times;</button></div>
    <p class="d-dr-help">${p.help}</p><div class="d-dr-c">${control(p, errors())}${notDef && !lock ? `<button class="linkbtn" data-reset="${p.id}">Reset to ${esc(fmt(p, p.def))}</button>` : ''}</div>
    ${lock ? `<p class="d-lock">${lock}</p>` : ''}${e ? `<p class="d-err" role="alert">${e}</p>` : ''}</div>`;
}
function para(n, title, html, ids) {
  return `<div class="d-p"><span class="d-n">${n}</span><div><h3>${title}</h3><p>${html}</p>${drawer(ids)}</div></div>`;
}

function body() {
  const a = S.draft.archive;
  return `<section class="d-time"><div class="d-th"><h2>What the hub keeps, and for how long</h2>
      <button type="button" class="d-arch ${a ? 'on' : ''}" data-tog="archive" data-fk="archive" aria-pressed="${a}" ${S.status === 'saving' ? 'disabled' : ''}><span class="d-inf">&infin;</span><span><b>Archive mode ${a ? 'on' : 'off'}</b><small>${a ? 'Messages and events kept forever' : 'History trims nightly'}</small></span>${srcChip('archive')}</button></div>
      <div class="d-axis">${TICKS.map(([t, d]) => `<span style="left:${pos(d)}%">${t}</span>`).join('')}</div>
      ${LANES.map(lane).join('')}
      ${S.trim ? '<p class="d-err">Existing history older than the new limit is trimmed at 02:00.</p>' : ''}
      ${forecast()}</section>
    <section class="d-policy"><h2>Everything else, in plain words</h2>
      ${para(1, 'Files', `${S.draft.expiry === 'off' ? `Files ${tok('expiry')}.` : `Files expire ${tok('expiry')} after their last download.`} Each may be up to ${tok('maxfile')}, and each peer may upload ${tok('quota')} a day.`, ['expiry', 'maxfile', 'quota'])}
      ${para(2, 'Delivery & events', `When a routine wakes an agent, its messages go out as ${tok('prio')}. MCP clients re-read Events every ${tok('refresh')}. A reply chain is cut after ${tok('depth')}, and an undelivered message expires after ${tok('ttl')}.`, ['prio', 'refresh', 'depth', 'ttl'])}
      ${para(3, 'Agents', `Each peer may send ${tok('rate')}, with bursts of ${tok('burst')}. The hub holds up to ${tok('maxpeers')} and ${tok('invites')}.`, ['rate', 'burst', 'maxpeers', 'invites'])}
      ${para(4, 'Appearance <span class="d-you">only you</span>', `Show me times as ${tok('ts')}, in the ${tok('theme')} theme, at ${tok('density')} density.`, ['ts', 'theme', 'density'])}
      <p class="d-leg"><span class="tok t-default">default</span><span class="tok t-saved">saved</span><span class="tok t-flag lk">flag</span><span class="tok t-edited">edited</span></p>
    </section>`;
}

function nearest(track, clientX) {
  const id = track.dataset.lane, r = track.getBoundingClientRect(), x = ((clientX - r.left) / r.width) * 100;
  let best = null, bd = 1e9;
  for (const o of P[id].opts) { const dd = Math.abs(pos(o.days) - x); if (dd < bd) { bd = dd; best = o.v; } }
  return [id, best];
}

export const lede = 'The same settings, read as a policy. Drag along a lane, or click any underlined value to change it.';
export { body };
export function bind(root, again) {
  open = null;
  root.addEventListener('click', e => {
    const t = e.target.closest('[data-tok]');
    if (t && t.closest('.d-policy p:not(.d-leg)')) { open = open === t.dataset.tok ? null : t.dataset.tok; again(); return; }
    if (e.target.closest('[data-close]')) { open = null; again(); }
    if (e.target.closest('[data-act=jump]') && root.querySelector('.d-policy')) {
      const first = Object.keys(errors())[0]; if (!first) return;
      open = first; again();
      root.querySelector('.d-drawer')?.scrollIntoView({ block: 'center' });
      root.querySelector('.d-drawer input')?.focus({ preventScroll: true });
    }
  });
  root.addEventListener('pointerdown', e => {
    const tr = e.target.closest('.d-track'); if (!tr || tr.getAttribute('aria-disabled') || S.status === 'saving') return;
    drag = tr.dataset.lane; const [id, v] = nearest(tr, e.clientX); if (v !== S.draft[id]) set(id, v);
    e.preventDefault();
  });
  addEventListener('pointermove', e => {
    if (!drag || S.confirm) return;
    const tr = root.querySelector(`.d-track[data-lane="${drag}"]`); if (!tr) return;
    const [id, v] = nearest(tr, e.clientX); if (v !== S.draft[id]) set(id, v);
  });
  addEventListener('pointerup', () => { drag = null; });
}
