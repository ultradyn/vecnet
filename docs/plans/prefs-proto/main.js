// PROTOTYPE entry. ?view=list|policy (or legacy ?variant=A|D) picks the view for
// screenshots; otherwise the remembered choice. ?state= forces a demo state.
// Variants B (Bento) and C (prefs.conf TUI) were eliminated: see eliminated/.
import { applyState, STATES, subscribe, syncConfirm } from './shared.js';
import { mount, render, savedView, setView } from './page.js';

const params = new URLSearchParams(location.search);
const urlView = params.get('view') || { A: 'list', D: 'policy' }[(params.get('variant') || '').toUpperCase()];
let state = STATES.includes(params.get('state')) ? params.get('state') : 'initial';

subscribe(() => { render(); syncConfirm(); });
mount(document.getElementById('root'));
setView(urlView || savedView() || 'list', { persist: false });
syncConfirm();
applyState(state);

const bar = document.getElementById('pbar');
bar.innerHTML = `<span class="pb-tag">Prototype</span><label class="pb-st"><span>demo state</span><select aria-label="Demo state">${STATES.map(s => `<option ${s === state ? 'selected' : ''}>${s}</option>`).join('')}</select></label>`;
bar.addEventListener('change', e => {
  state = e.target.value;
  const u = new URL(location.href);
  if (state === 'initial') u.searchParams.delete('state'); else u.searchParams.set('state', state);
  history.replaceState(null, '', u);
  applyState(state);
});
