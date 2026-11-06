// The attachment chip row for a compose box: pick, drop or paste files, upload
// each (two at a time) and hold the references for the send. Names come from
// the operator's own files, but they are still shown cleaned and
// bidi-isolated. The rules live in attach-model.js; this file is the DOM
// around them. On an R2 hub a chip goes queued → hashing → uploading (straight
// to storage) → verifying (the hub checks the stored object) → ready; Retry
// keeps the chip's grant key, so an upload that may have landed is not
// reserved twice.
import { h } from './dom.js';
import { bytes } from './format.js';
import * as model from './attach-model.js';

const PARALLEL = 2;

const ACTIVE = new Set(['hashing', 'uploading', 'verifying']);
const STATUS = { queued: 'queued', hashing: 'hashing…', uploading: 'uploading…', verifying: 'verifying…' };
const newKey = () => 'ui-blob-' + Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join('');

const hasFiles = dt => !!dt && (Array.from(dt.types || []).includes('Files') || !!(dt.files && dt.files.length));

export function createAttachments({ api, onChange }) {
  let list = [], seq = 0, dead = false, guarded = false, picking = false, notify = onChange || (() => {});
  const offs = [];
  const chips = h('div', { class: 'up-list' });
  const note = h('p', { class: 'up-note', role: 'status' });
  const picker = h('input', { type: 'file', multiple: true, hidden: true, tabindex: '-1', 'aria-hidden': 'true' });
  const el = h('div', { class: 'up', hidden: true }, chips, note, picker);

  const on = (target, type, fn, opts) => { target.addEventListener(type, fn, opts); offs.push(() => target.removeEventListener(type, fn, opts)); };
  const changed = () => { el.hidden = !list.length && !note.textContent; notify(); };
  const running = () => list.filter(it => ACTIVE.has(it.state)).length;

  // draw rebuilds one chip for its state; progress only moves the bar.
  function draw(it) {
    const kids = [h('span', { class: 'up-nm', text: it.name, title: it.name }), h('span', { class: 'up-sz', text: bytes(it.size) })];
    if (it.state === 'done') {
      kids.push(h('span', { class: 'up-ok', text: '✓', 'aria-label': 'uploaded' }));
      // A direct upload says how long its file lives, from its own grant.
      const v = model.blobView(it, null, Date.now());
      if (v && v.label) kids.push(h('span', { class: 'up-ret' + (v.keep ? ' keep' : ''), text: v.keep ? 'kept' : v.label.replace(/^Expires /, 'expires '), title: v.label + ' · ' + v.trust }));
    }
    if (it.state !== 'failed') kids.push(h('button', { type: 'button', class: 'up-x', text: '×', 'aria-label': 'Remove ' + it.name, onclick: () => remove(it) }));
    it.bar = it.label = null;
    if (STATUS[it.state]) {
      // A still cue beside the bar: the indeterminate sliver can be off the
      // chip in any one frame. Verifying has no byte count, so its bar stays
      // indeterminate.
      it.label = h('span', { class: 'up-st', text: STATUS[it.state] });
      const verb = it.state === 'hashing' ? 'Hashing ' : it.state === 'verifying' ? 'Verifying ' : 'Uploading ';
      it.bar = h('span', { class: 'up-bar' + (it.progressed && it.state !== 'verifying' ? '' : ' up-ind'), role: 'progressbar', 'aria-label': verb + it.name });
      kids.splice(2, 0, it.label);
      kids.push(it.bar);
      progress(it);
    }
    if (it.state === 'failed') {
      kids.push(h('div', { class: 'up-err' },
        h('span', { text: it.error }),
        h('button', { type: 'button', class: 'linkbtn', text: 'Retry', onclick: () => retry(it) }),
        h('button', { type: 'button', class: 'linkbtn', text: 'Remove', onclick: () => remove(it) })));
    }
    it.node.className = 'up-chip up-' + it.state;
    it.node.replaceChildren(...kids);
  }

  function progress(it) {
    if (!it.bar || !it.progressed || it.state === 'verifying') return;
    it.bar.classList.remove('up-ind');
    const pct = it.total > 0 ? Math.min(100, Math.round((it.loaded / it.total) * 100)) : 0;
    it.bar.style.setProperty('--p', pct + '%');
    it.bar.setAttribute('aria-valuenow', String(pct));
    if (it.label) it.label.textContent = (it.state === 'hashing' ? 'hashing ' : '') + pct + '%';
  }

  function start(it) {
    const ctrl = new AbortController();
    // One grant key per chip until the hub says it is spent (retryKey).
    if (!it.idem) it.idem = newKey();
    Object.assign(it, { state: 'uploading', ctrl, loaded: 0, total: it.size, progressed: false, error: '' });
    draw(it);
    const mine = () => !dead && it.ctrl === ctrl;
    const onPhase = phase => {
      if (!mine() || it.state === phase) return;
      Object.assign(it, { state: phase, loaded: 0, progressed: false });
      draw(it);
      changed();
    };
    const onProgress = (loaded, total, phase) => {
      if (!mine() || (phase && phase !== it.state)) return;
      Object.assign(it, { loaded, total: total || it.size, progressed: true });
      progress(it);
    };
    api.upload(it.file, { signal: ctrl.signal, onProgress, onPhase, idempotencyKey: it.idem }).then(r => {
      if (!mine()) return;
      // The reference a send carries; a direct one also names its grant,
      // recorded class, deadline and the hub's verification.
      const ref = r.blob_id
        ? { blob_id: r.blob_id, sha256: r.sha256, sha256_verified: r.sha256_verified, integrity: r.integrity, retention_class: r.retention_class, expires_at: r.expires_at }
        : { sha256: r.sha256 };
      Object.assign(it, { ctrl: null, state: 'done', ...ref, size: r.size ?? it.size });
      settled(it);
    }, err => {
      if (!mine()) return;
      Object.assign(it, { ctrl: null, state: 'failed', error: model.errorText(err, Date.now()), idem: model.retryKey(it, err) });
      settled(it);
    });
  }

  function settled(it) {
    draw(it);
    pump();
    changed();
  }

  function pump() {
    for (let next; running() < PARALLEL && (next = list.find(it => it.state === 'queued'));) start(next);
  }

  function retry(it) {
    if (dead || !list.includes(it)) return;
    Object.assign(it, { state: 'queued', error: '', progressed: false });
    draw(it);
    pump();
    changed();
  }

  function remove(it) {
    if (it.ctrl) it.ctrl.abort();
    it.ctrl = null;
    list = list.filter(x => x !== it);
    it.node.remove();
    pump();
    changed();
  }

  function add(files, { pasted = false } = {}) {
    if (dead) return [];
    const { files: ok, refused } = model.canAdd(list, files);
    const now = new Date();
    for (const file of ok) {
      const it = {
        id: 'up' + ++seq, file, name: model.attachmentName(file, now, { pasted }), size: file.size,
        media_type: model.mediaTypeOf(file), state: 'queued', node: h('div', { class: 'up-chip' }),
      };
      list.push(it);
      chips.append(it.node);
      draw(it);
    }
    note.textContent = '';
    if (refused.length) note.append(h('span', { text: 'Not added: ' + refused.map(model.refusalText).join('; ') }));
    pump();
    changed();
    return refused;
  }

  // clear removes every chip, or with ids only those chips (a sent reply
  // clears what it carried; a file added while it was in flight stays).
  function clear(ids) {
    const gone = ids ? list.filter(it => ids.includes(it.id)) : list;
    for (const it of gone) {
      if (it.ctrl) it.ctrl.abort();
      it.ctrl = null; // so its rejection is not mistaken for a live upload
      it.node.remove();
    }
    list = list.filter(it => !gone.includes(it));
    note.textContent = '';
    pump();
    changed();
  }

  // A snapshot for the model's rules: no File, node or controller.
  const items = () => list.map(({ id, name, size, media_type, state, loaded, total, sha256, error, blob_id, sha256_verified, integrity, retention_class, expires_at }) => ({ id, name, size, media_type, state, loaded, total, sha256, error, blob_id, sha256_verified, integrity, retention_class, expires_at }));

  // wire connects the compose box: any argument may be null.
  function wire(dropTarget, textarea, pickerButton) {
    if (dead) return;
    if (pickerButton) {
      pickerButton.type = 'button';
      on(pickerButton, 'click', () => picker.click());
    }
    if (!picking) {
      picking = true;
      on(picker, 'change', () => { add(picker.files); picker.value = ''; });
    }
    if (dropTarget) {
      let depth = 0;
      const off = () => { depth = 0; dropTarget.classList.remove('up-drop'); };
      on(dropTarget, 'dragenter', e => { if (hasFiles(e.dataTransfer)) { depth++; dropTarget.classList.add('up-drop'); e.preventDefault(); } });
      on(dropTarget, 'dragover', e => { if (hasFiles(e.dataTransfer)) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; dropTarget.classList.add('up-drop'); } });
      on(dropTarget, 'dragleave', () => { if (--depth <= 0) off(); });
      on(dropTarget, 'drop', e => {
        off();
        if (!hasFiles(e.dataTransfer)) return;
        e.preventDefault();
        add(e.dataTransfer.files);
      });
    }
    if (textarea) {
      // Only files are taken; a paste that also carries text keeps its text.
      on(textarea, 'paste', e => {
        const dt = e.clipboardData;
        if (!dt || !dt.files || !dt.files.length) return;
        if (!Array.from(dt.types || []).includes('text/plain')) e.preventDefault();
        add(dt.files, { pasted: true });
      });
    }
    if (!guarded) {
      // A near miss must not open the file as a page and lose the draft.
      guarded = true;
      on(document, 'dragover', e => { if (!e.defaultPrevented && hasFiles(e.dataTransfer)) { e.preventDefault(); e.dataTransfer.dropEffect = 'none'; } });
      on(document, 'drop', e => { if (hasFiles(e.dataTransfer)) e.preventDefault(); });
    }
  }

  function destroy() {
    for (const it of list) if (it.ctrl) it.ctrl.abort();
    dead = true;
    list = [];
    notify = () => {};
    for (const off of offs.splice(0)) off();
    el.remove();
  }

  return {
    el, add, clear, items, wire, destroy,
    ready: () => model.ready(list),
    toAttachments: () => model.toAttachments(list),
    signature: () => model.signature(list),
  };
}
