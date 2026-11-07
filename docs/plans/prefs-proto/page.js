// PROTOTYPE: the chosen Settings page. One page, two views over one store:
// "List" (variant A, Ledger) is the default; "Policy" (variant D) is the
// alternate. The view choice is remembered in localStorage.
import * as A from './a.js';
import * as D from './d.js';
import { bindControls, mobileTop, rerender, S, saveBar, shellSide } from './shared.js';

const KEY = 'vecnet.ui.settingsView';
const VIEWS = { list: { label: 'List', mod: A, v: 'A' }, policy: { label: 'Policy', mod: D, v: 'D' } };
export function savedView() { try { return localStorage.getItem(KEY); } catch { return null; } }
function remember(v) { try { localStorage.setItem(KEY, v); } catch {} }

let el, view = 'list';
export function setView(v, { persist = true } = {}) {
  if (!VIEWS[v]) v = 'list';
  view = v; if (persist) remember(v);
  render();
}
export const currentView = () => view;

function head() {
  return `<div class="pg-head"><h1>Settings</h1>
    <div class="seg pg-view" role="radiogroup" aria-label="View">${Object.entries(VIEWS).map(([k, x]) => `<button type="button" role="radio" aria-checked="${k === view}" class="${k === view ? 'on' : ''}" data-view="${k}" data-fk="view-${k}"><i class="pv-${k}" aria-hidden="true"></i>${x.label}</button>`).join('')}</div></div>
    <p class="lede">${VIEWS[view].mod.lede}</p>`;
}

export function mount(root) {
  el = root;
  root.innerHTML = `<div class="va"><div class="side-slot"></div><main><div class="mt-slot"></div><div class="col pg-col"></div></main><aside class="a-aside"></aside></div>`;
  bindControls(root);
  A.bind(root, render);
  D.bind(root, render);
  root.addEventListener('click', e => { const b = e.target.closest('[data-view]'); if (b) setView(b.dataset.view); });
}
export function render() {
  if (!el) return;
  const x = VIEWS[view];
  document.body.dataset.variant = x.v;
  document.body.classList.toggle('compact', S.draft.density === 'compact');
  const va = el.querySelector('.va');
  va.className = `va ${view === 'list' ? 'a' : 'noaside d'}`;
  el.querySelector('.side-slot').innerHTML = shellSide();
  el.querySelector('.mt-slot').innerHTML = mobileTop();
  const col = el.querySelector('.pg-col');
  col.className = `col pg-col ${view === 'list' ? 'a-col' : 'd-col'}`;
  rerender(col, head() + x.mod.body() + saveBar());
  const aside = el.querySelector('.a-aside');
  aside.hidden = view !== 'list';
  aside.innerHTML = view === 'list' ? A.aside() : '';
}
