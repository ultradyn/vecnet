// Variant A, "Ledger": a modest, Cursor-like long page inside the real app shell.
// Label and help on the left, the control on the right, a filter at the top and
// an "on this page" rail in the aside.
import { bindControls, bySec, dirtyIds, errors, esc, fmt, gb, HUB, isDirty, lockReason, mobileTop, P, PREFS, rerender, S, saveBar, SECTIONS, shellSide, srcChip, srcOf, STORAGE, usedBytes } from './shared.js';

let el, filter = '', only = 'all';

export function control(p, errs) {
  const lock = !!lockReason(p) || S.status === 'saving';
  const v = S.draft[p.id];
  if (p.type === 'toggle') return `<button class="tg" role="switch" aria-checked="${!!v}" aria-label="${esc(p.label)}" data-tog="${p.id}" data-fk="${p.id}" ${lock ? 'disabled' : ''}></button>`;
  if (p.type === 'num') return `<span class="numi ${errs[p.id] ? 'bad' : ''}"><input inputmode="decimal" aria-label="${esc(p.label)}" aria-invalid="${!!errs[p.id]}" data-num="${p.id}" data-fk="${p.id}" value="${esc(v)}" ${lock ? 'disabled' : ''}><span class="u">${p.unit}</span></span>`;
  if (p.opts.length <= 3) return `<span class="seg ${p.id === 'prio' ? 'prio' : ''}" role="radiogroup" aria-label="${esc(p.label)}">${p.opts.map(o => `<button type="button" role="radio" aria-checked="${o.v === v}" class="${o.v === v ? 'on' : ''}" data-set="${p.id}" data-v="${o.v}" data-fk="${p.id}-${o.v}" ${lock ? 'disabled' : ''}>${o.l}</button>`).join('')}</span>`;
  return `<select class="selx" aria-label="${esc(p.label)}" data-sel="${p.id}" data-fk="${p.id}" ${lock ? 'disabled' : ''}>${p.opts.map(o => `<option value="${o.v}" ${o.v === v ? 'selected' : ''}>${o.l}</option>`).join('')}</select>`;
}

function row(p, errs) {
  const lock = lockReason(p);
  const notDef = String(S.draft[p.id]) !== String(p.def);
  const warn = p.id === 'expiry' && S.draft.expiry === 'off' && HUB.hosted
    ? `<p class="a-warn">Files never expire on this hosted hub. Storage past 10 GB bills at ${HUB.overage}.</p>` : '';
  return `<div class="a-row ${isDirty(p.id) ? 'dirty' : ''} ${errs[p.id] ? 'err' : ''} ${lock ? 'locked' : ''} ${S.justSaved.has(p.id) ? 'flash' : ''}" data-row="${p.id}">
    <div class="a-l"><div class="a-t"><span class="a-lbl">${p.label}</span>${srcChip(p.id)}</div>
      <p class="a-h">${p.help}</p>
      ${lock ? `<p class="a-lock">${lock}</p>` : ''}${errs[p.id] ? `<p class="a-err" role="alert">${errs[p.id]}</p>` : ''}${warn}
    </div>
    <div class="a-c">${control(p, errs)}
      ${notDef && !lock ? `<button class="linkbtn a-rst" data-reset="${p.id}" data-fk="rst-${p.id}">Reset to ${esc(fmt(p, p.def))}</button>` : `<span class="a-key">${p.key}</span>`}
    </div></div>`;
}

function visible(p) {
  if (only === 'changed' && !isDirty(p.id) && S.src[p.id] !== 'saved') return false;
  if (only === 'flag' && S.src[p.id] !== 'flag') return false;
  if (!filter) return true;
  const q = filter.toLowerCase();
  return [p.label, p.help, p.key].some(s => s.toLowerCase().includes(q));
}

function storage() {
  const used = usedBytes();
  return `<div class="a-store"><div class="a-st-h"><span>Storage used</span><b>${gb(used)}</b><span class="a-st-of">of ${gb(HUB.quota)} · ${HUB.plan}</span></div>
    <div class="meter" role="img" aria-label="${gb(used)} of ${gb(HUB.quota)} used">${STORAGE.map(s => `<i style="width:${(s.bytes / HUB.quota) * 100}%;background:${s.color}"></i>`).join('')}</div>
    <div class="legend">${STORAGE.map(s => `<span><i style="background:${s.color}"></i>${s.label} <b>${gb(s.bytes)}</b></span>`).join('')}</div></div>`;
}

function body() {
  const errs = errors();
  const nChanged = PREFS.filter(p => isDirty(p.id) || S.src[p.id] === 'saved').length;
  const nFlag = PREFS.filter(p => S.src[p.id] === 'flag').length;
  const secs = SECTIONS.map(s => {
    const rows = bySec(s.id).filter(visible);
    if (!rows.length && !(s.id === 'retention' && !filter && only === 'all')) return '';
    return `<section class="a-sec" id="sec-${s.id}"><h2>${s.title}${s.id === 'appearance' ? '<span class="a-you">only you</span>' : ''}</h2><p class="a-blurb">${s.blurb}</p>
      ${s.id === 'retention' && only === 'all' && !filter ? storage() : ''}
      <div class="a-rows">${rows.map(p => row(p, errs)).join('')}</div></section>`;
  }).join('');
  return `<div class="a-tools"><input class="a-q" type="search" placeholder="Search settings" aria-label="Search settings" data-fk="q" value="${esc(filter)}">
      <span class="seg a-only">${[['all', 'All'], ['changed', `Customised ${nChanged}`], ['flag', `Flags ${nFlag}`]].map(([k, l]) => `<button type="button" data-only="${k}" class="${only === k ? 'on' : ''}">${l}</button>`).join('')}</span></div>
    ${secs || '<div class="empty"><b>No settings match.</b>Try another word, or clear the search.</div>'}`;
}

function aside() {
  return `<div class="a-toc"><div class="a-toc-h">On this page</div>${SECTIONS.map(s => {
    const n = bySec(s.id).filter(p => isDirty(p.id)).length;
    const e = bySec(s.id).filter(p => errors()[p.id]).length;
    return `<a href="#sec-${s.id}" data-toc="${s.id}">${s.title}${e ? `<span class="t-e">${e}</span>` : n ? `<span class="t-n">${n}</span>` : ''}</a>`;
  }).join('')}</div>
  <div class="a-toc-h">Where values come from</div>
  <dl class="a-legend"><dt><span class="src s-default">default</span></dt><dd>Built into the hub.</dd>
  <dt><span class="src s-flag">flag</span></dt><dd>Set on the hub command line. Read-only here.</dd>
  <dt><span class="src s-saved">saved</span></dt><dd>Saved by an operator on this page.</dd>
  <dt><span class="src s-edited">edited</span></dt><dd>Changed here, not saved yet.</dd></dl>`;
}

export const lede = `Hub settings apply to every peer on <span class="mono">${HUB.host}</span>. Appearance is yours alone.`;
export { body, aside };
export function bind(root, again) {
  root.addEventListener('input', e => { if (e.target.matches('.a-q')) { filter = e.target.value; again(); } });
  root.addEventListener('click', e => {
    const b = e.target.closest('[data-only]'); if (b) { only = b.dataset.only; again(); }
    const t = e.target.closest('[data-toc]'); if (t) { e.preventDefault(); root.querySelector(`#sec-${t.dataset.toc}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' }); }
  });
}
