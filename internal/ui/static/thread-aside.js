// The side panel beside a thread. ctx.threadId names the thread (see app.js).
// Sections: Participants, Files, Thread, and the delivery matrix button.
// Presence and reachability stay two signals (PROTOCOL §8). Downloads go
// through api.blob, so the bearer never enters a URL (§2, §2.7). An R2 file
// is exchanged for a fresh short-lived link on every click (api.download):
// the link lives only for that request, the bytes are checked against the
// verified SHA-256, and the file is saved, never shown inline.
import { budgetBar, fileRows, participantEntry, presenceLabel, reachLabel, THREAD_BUDGET } from './aside-model.js';
import { downloadErrorText, nextExpiry } from './attach-model.js';
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
  const S = { busy: new Set(), open: false, ended: new Map(), meta: new Map() };
  // A metadata miss is not a cached answer. Refresh and a live reconnect
  // each ask once more, including after earlier failures, and an in-flight
  // read is not asked twice. The rejection itself never starts another read.
  let retryMeta = false;
  const urls = new Set();
  let gone = false, expiryTimer = 0;
  // The thread view's R2 references (thread.js), when it is mounted beside us.
  const refsFeed = ctx.blobRefs && typeof ctx.blobRefs.subscribe === 'function' ? ctx.blobRefs : null;
  const refsNow = () => {
    const out = new Map((refsFeed && refsFeed.get()) || []);
    // A file outside the loaded history: its own metadata-only status.
    // Pending and failed reads are a status too. Omitting them makes the
    // row look available, with a download and no deadline.
    for (const [id, entry] of S.meta) {
      if (!entry || out.has(id)) continue;
      if (entry.phase === 'ready' && entry.value && typeof entry.value === 'object') {
        const st = entry.value;
        out.set(id, { att: { retention_class: st.retention_class, expires_at: st.expires_at === undefined ? null : st.expires_at, sha256_verified: st.sha256_verified, integrity: st.integrity }, state: st });
      } else if (entry.phase === 'inflight') {
        out.set(id, { att: {}, state: { state: 'pending' } });
      } else {
        out.set(id, { att: {}, state: { state: 'unknown' } });
      }
    }
    // A 410 seen here wins until the hub's next page says the same.
    for (const [id, state] of S.ended) {
      const r = out.get(id);
      out.set(id, { att: r ? r.att : {}, state: { ...(r && r.state), state } });
    }
    return out;
  };
  const keyOf = f => f.blobId || f.sha256;
  // statusFor asks the hub for an R2 file the loaded history does not
  // reference (older than its pages): metadata only, no link minted.
  // In flight, a recorded answer and a terminal expiry are not asked again.
  // A failure waits for the next refresh or live reconnect: one attempt
  // per signal, with no ceiling that sticks for the rest of the mount,
  // and the rejection itself does not count as that signal.
  function statusFor(files) {
    if (refsFeed && !refsFeed.loaded()) return;
    const known = (refsFeed && refsFeed.get()) || new Map();
    const retry = retryMeta;
    retryMeta = false;
    for (const f of files) {
      if (!f.blob_id || known.has(f.blob_id)) continue;
      const cur = S.meta.get(f.blob_id);
      if (cur && (cur.phase === 'inflight' || cur.phase === 'ready')) continue;
      if (cur && cur.phase === 'failed' && !retry) continue;
      const nextTries = ((cur && cur.tries) || 0) + 1;
      S.meta.set(f.blob_id, { phase: 'inflight', tries: nextTries });
      api.blobStatus(f.sha256, f.blob_id).then(st => {
        if (gone) return;
        S.meta.set(f.blob_id, { phase: 'ready', tries: nextTries, value: st && typeof st === 'object' ? st : null });
        render(true);
      }, e => {
        if (gone) return;
        if (e && (e.code === 'blob_expired' || e.code === 'blob_purged')) {
          S.meta.set(f.blob_id, { phase: 'ready', tries: nextTries, value: { state: e.code === 'blob_expired' ? 'expired' : 'purged' } });
        } else {
          S.meta.set(f.blob_id, { phase: 'failed', tries: nextTries });
        }
        render(true);
      });
    }
  }
  function askAgain() {
    retryMeta = true;
    render(true);
  }

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
    const key = keyOf(f);
    if (S.busy.has(key) || f.disabled) return;
    S.busy.add(key);
    render(true);
    try {
      const blob = f.blobId ? await api.download({ sha256: f.sha256, blob_id: f.blobId }) : await api.blob(f.sha256);
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
      if (f.blobId && (e.code === 'blob_expired' || e.code === 'blob_purged')) {
        // Expired and purged are different ends: say which, and refresh the
        // thread's projection so its chips agree.
        S.ended.set(f.blobId, e.code === 'blob_expired' ? 'expired' : 'purged');
        toast(f.label + ': ' + downloadErrorText(e, store.hubNow(), f.cls), true);
        if (store.invalidate) store.invalidate('thread:' + threadId);
        detail.reload();
      } else if (f.blobId) {
        toast(f.label + ': ' + downloadErrorText(e, store.hubNow(), f.cls), true);
      } else if (e.status === 410 || e.code === 'blob_purged') {
        // The list was stale: the hub purged the blob since it loaded.
        toast(f.label + ' was purged from the hub', true);
        detail.reload();
      } else {
        toast('Download failed: ' + e.code + ': ' + e.message, true);
      }
    } finally {
      S.busy.delete(key);
      render(true);
    }
  }

  function file(f) {
    const busy = S.busy.has(keyOf(f));
    if (f.blobId) {
      // An R2 file: its life by recorded class, and its checksum trust.
      const cls = 'file r2 s-' + f.state + (f.purged ? ' purged' : '') + (busy ? ' busy' : '');
      return h('button', {
        type: 'button', class: cls, title: f.sha256 + (f.trust ? ' · ' + f.trust : ''), disabled: f.disabled || busy,
        'aria-busy': busy ? 'true' : null, 'data-sha': f.sha256, 'data-blob': f.blobId, onclick: () => download(f),
      },
        h('span', { class: 'top' },
          h('span', { class: 'nm', text: f.label }),
          h('span', { class: 'sz', text: busy ? 'downloading…' : f.size })),
        h('span', { class: 'meta' },
          h('span', { text: nameOf(f.sender) + ' · ' + when(f.at) }),
          f.note ? h('span', { class: 'tag r2 s-' + f.state + (f.kept ? ' kept' : ''), text: f.note }) : null),
        f.trust && f.state === 'available' && !f.verified ? h('span', { class: 'meta' }, h('span', { class: 'tag unv', text: 'checksum not verified' })) : null);
    }
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
      statusFor(d.files || []);
      const now = store.hubNow(), t = d.row.thread, files = fileRows(d.files, refsNow(), now);
      // Redraw when the next file reaches its recorded deadline.
      clearTimeout(expiryTimer);
      const at = nextExpiry(files.filter(f => f.blobId).map(f => ({ state: f.state, expiresAt: f.expiresAt })), now);
      if (at !== null) expiryTimer = setTimeout(() => render(true), Math.min(at - now + 250, 6 * 3600 * 1000));
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
  // A new thread snapshot is a refresh: failed metadata may be asked again.
  // render() itself still skips a notification that carries no new value, so
  // a load's start does not rebuild a focused file button.
  const off = [detail.subscribe(() => { retryMeta = true; render(); }), admin.subscribe(redraw), ctx.peers.subscribe(redraw)];
  if (store.onConn) off.push(store.onConn(state => { if (state === 'live') askAgain(); }));
  // New history: the projection is current again, so local 410s give way.
  if (refsFeed) off.push(refsFeed.subscribe(() => { S.ended.clear(); render(true); }));
  return () => {
    gone = true;
    clearTimeout(expiryTimer);
    off.forEach(fn => fn());
    for (const url of urls) URL.revokeObjectURL(url);
    urls.clear();
    root.classList.remove('open');
  };
}
