// The side panel beside a thread. ctx.threadId names the thread (see app.js).
// Sections: Participants, Files, Thread, and the delivery matrix button.
// Presence and reachability stay two signals (PROTOCOL §8). Downloads go
// through api.blob, so the bearer never enters a URL (§2, §2.7).
import { budgetBar, fileRows, participantEntry, presenceLabel, reachLabel, THREAD_BUDGET } from './aside-model.js';
import { h, idCopy, peerLink } from './dom.js';
import { kindShort, kindTitle, when } from './format.js';

// Long enough for the browser to take the object URL into its download.
const REVOKE_AFTER = 1000;

function dl(rows) {
  return h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', { text: k }), h('dd', {}, v)]));
}

export function mountAside(root, ctx) {
  const { api, store, threadId, nameOf, kindOf, me, toast } = ctx;
  const detail = store.resource('thread:' + threadId, () => api.thread(threadId));
  const admin = store.resource('peers', () => api.adminPeers());
  const S = { busy: new Set(), open: false };
  const urls = new Set();
  let gone = false;

  const toggle = h('button', { type: 'button', class: 'aside-toggle btn', 'aria-expanded': 'false', 'aria-controls': 'aside-body', text: 'Details' });
  const body = h('div', { class: 'aside-body', id: 'aside-body' });
  toggle.addEventListener('click', () => {
    S.open = !S.open;
    root.classList.toggle('open', S.open);
    toggle.setAttribute('aria-expanded', String(S.open));
  });
  root.replaceChildren(toggle, body);
  // One button for the life of the panel, outside the part that redraws: the
  // matrix returns focus to the element that opened it, so a redraw must
  // neither replace it nor move it (moving a focused node drops its focus).
  const content = h('div', { class: 'aside-content' });
  const matrixBtn = h('button', { type: 'button', class: 'btn matrix-btn', text: 'Delivery matrix', onclick: () => ctx.nav('#matrix/' + threadId) });
  body.replaceChildren(content, matrixBtn);

  const entryOf = id => participantEntry((admin.get() || {}).peers, (ctx.peers.get() || {}).peers, id);

  function participant(id) {
    const e = entryOf(id);
    const pres = presenceLabel(e && e.peer.presence), reach = reachLabel(e && e.peer.reachability);
    return h('div', { class: 'pp', 'data-peer': id },
      h('span', { class: 'sig r-' + reach, title: 'reachability: ' + reach },
        h('span', { class: 'dot ' + pres, title: 'presence: ' + pres })),
      h('span', { class: 'who' },
        h('span', { class: 'nm' }, peerLink(id, nameOf(id)), id === me ? ' (you)' : null),
        h('span', { class: 'sub', text: pres + ' · ' + reach })),
      e && e.disabled ? h('span', { class: 'chip bad', text: 'disabled' }) : null,
      h('span', { class: 'k', text: kindShort(kindOf(id)), title: kindTitle(kindOf(id)) }));
  }

  async function download(f) {
    if (S.busy.has(f.sha256) || f.disabled) return;
    S.busy.add(f.sha256);
    render(true);
    try {
      const blob = await api.blob(f.sha256);
      if (gone) return;
      const url = URL.createObjectURL(new Blob([blob], { type: 'application/octet-stream' }));
      urls.add(url);
      const a = h('a', { href: url, download: f.download, hidden: true });
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => { if (urls.delete(url)) URL.revokeObjectURL(url); }, REVOKE_AFTER);
      toast('Downloaded ' + f.label);
    } catch (e) {
      if (e.status === 401 || gone) return;
      if (e.status === 410 || e.code === 'blob_purged') {
        // The list was stale: the hub purged the blob since it loaded.
        toast(f.label + ' was purged from the hub', true);
        detail.reload();
      } else {
        toast('Download failed: ' + e.code + ': ' + e.message, true);
      }
    } finally {
      S.busy.delete(f.sha256);
      render(true);
    }
  }

  function file(f) {
    const busy = S.busy.has(f.sha256);
    const cls = 'file' + (f.spilled ? ' spill' : '') + (f.purged ? ' purged' : '') + (busy ? ' busy' : '');
    return h('button', {
      type: 'button', class: cls, title: f.sha256, disabled: f.disabled || busy,
      'aria-busy': busy ? 'true' : null, 'data-sha': f.sha256, onclick: () => download(f),
    },
      h('span', { class: 'top' },
        h('span', { class: 'nm', text: f.label }),
        h('span', { class: 'sz', text: busy ? 'downloading…' : f.size })),
      h('span', { class: 'meta' },
        h('span', { text: nameOf(f.sender) + ' · ' + when(f.at) }),
        f.note ? h('span', { class: 'tag ' + f.note, text: f.note }) : null));
  }

  function budget(row) {
    const b = budgetBar(row, THREAD_BUDGET);
    const bar = h('span', { class: 'bar ' + b.tone });
    if (b.fraction !== null) {
      const fill = h('i');
      fill.style.setProperty('--f', String(b.fraction));
      bar.append(fill);
    }
    return h('span', { class: 'budget' }, h('span', { class: 'bt', text: b.text }), b.fraction === null ? null : bar);
  }

  // The store notifies on every load start and end; redraw only on news, so
  // a focused file button is not replaced under the operator's keyboard.
  let shown = {};
  function render(force) {
    if (gone) return;
    const d = detail.get(), err = detail.error(), a = admin.get(), p = ctx.peers.get();
    if (force !== true && d === shown.d && err === shown.err && a === shown.a && p === shown.p) return;
    shown = { d, err, a, p };
    const kids = [];
    if (!d) {
      kids.push(detail.error()
        ? h('div', { class: 'empty error' }, h('b', { text: 'Details unavailable' }), detail.error().message)
        : h('div', { class: 'loading', text: 'Loading details…' }));
    } else {
      const t = d.row.thread, files = fileRows(d.files);
      kids.push(h('h3', { text: 'Participants' }), h('div', { class: 'pps' }, (t.participants || []).map(participant)));
      kids.push(h('h3', {}, 'Files', h('span', { class: 'n', text: String(files.length) })),
        files.length ? h('div', { class: 'files' }, files.map(file)) : h('div', { class: 'none', text: 'No files in this thread.' }));
      kids.push(h('h3', { text: 'Thread' }), dl([
        ['id', idCopy(t.thread_id, 'thread id')],
        ['group', t.group || '—'],
        ['budget', budget(d.row)],
        ['history', t.history_access || 'from_join'],
        ['locked', t.locked_participants ? 'yes' : 'no'],
        ['cut', d.row.cut ? d.row.cut.reason + ' · ' + when(d.row.cut.at) : 'no'],
      ]));
    }
    content.replaceChildren(...kids);
  }

  render(true);
  const redraw = () => render();
  const off = [detail.subscribe(redraw), admin.subscribe(redraw), ctx.peers.subscribe(redraw)];
  return () => {
    gone = true;
    off.forEach(fn => fn());
    for (const url of urls) URL.revokeObjectURL(url);
    urls.clear();
    root.classList.remove('open');
  };
}
