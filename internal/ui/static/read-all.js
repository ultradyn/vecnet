// Bulk read for thread spam: a button that marks the operator's unread
// deliveries read (all of them, or one sender's) after a second click, then
// says how many. POST /v1/admin/inbox/read pages 500 at a time; the inbox
// and sidebar counts follow through the store's inbox tag. Open approvals
// stay: they need an answer, not a read.
//
// While it runs the button is aria-disabled, not disabled, so keyboard
// focus stays on it; a click then does nothing.
import { h, toast } from './dom.js';
import { clean } from './format.js';

const CONFIRM_MS = 3000;

// readAllButton: label shown idle, confirm the armed label, sender the
// peer to limit to (none: everyone). onDone(total) runs after a success.
export function readAllButton(api, store, { label, confirm, sender, cls, onDone }) {
  label = clean(label);
  confirm = clean(confirm);
  let busy = false, armed = 0;
  const btn = h('button', { type: 'button', class: 'btn read-all' + (cls ? ' ' + cls : ''), text: label });
  const disarm = () => { clearTimeout(armed); armed = 0; btn.classList.remove('armed'); if (!busy) btn.textContent = label; };
  btn.addEventListener('click', async () => {
    if (busy) return;
    if (!armed) {
      btn.textContent = confirm;
      btn.classList.add('armed');
      armed = setTimeout(disarm, CONFIRM_MS);
      return;
    }
    disarm();
    busy = true;
    btn.setAttribute('aria-disabled', 'true');
    btn.textContent = 'Marking…';
    try {
      const total = await api.markAllRead({ sender, onPage: n => { if (n) btn.textContent = 'Marking… ' + n; } });
      toast(total ? 'Marked ' + total + ' read.' : 'Nothing unread.');
      if (onDone) onDone(total);
    } catch (e) {
      if (e && e.code === 'read_all_limit') toast('Marked ' + e.marked + ' read; more remain. Run again.');
      else toast('Could not mark read' + (e && e.code ? ' (' + e.code + ')' : '') + '. Try again.', true);
    } finally {
      busy = false;
      btn.removeAttribute('aria-disabled');
      btn.textContent = label;
      store.invalidate('inbox');
    }
  });
  // running: a mark-read is in flight (callers keep the button shown).
  btn.running = () => busy;
  return btn;
}
