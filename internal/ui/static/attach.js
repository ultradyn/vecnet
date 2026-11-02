// The attachment chip row for a compose box: pick, drop or paste files, upload
// each (two at a time) and hold the hashes for the send. Names come from the
// operator's own files, but they are still shown cleaned and bidi-isolated.
// The rules live in attach-model.js; this file is the DOM around them.
import { h } from './dom.js';
import { bytes } from './format.js';
import * as model from './attach-model.js';

const PARALLEL = 2;

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
  const running = () => list.filter(it => it.state === 'uploading').length;

  // draw rebuilds one chip for its state; progress only moves the bar.
  function draw(it) {
    const kids = [h('span', { class: 'up-nm', text: it.name, title: it.name }), h('span', { class: 'up-sz', text: bytes(it.size) })];
    if (it.state === 'done') kids.push(h('span', { class: 'up-ok', text: '✓', 'aria-label': 'uploaded' }));
    if (it.state !== 'failed') kids.push(h('button', { type: 'button', class: 'up-x', text: '×', 'aria-label': 'Remove ' + it.name, onclick: () => remove(it) }));
    it.bar = it.label = null;
    if (it.state === 'uploading' || it.state === 'queued') {
      // A still cue beside the bar: the indeterminate sliver can be off the
      // chip in any one frame.
      it.label = h('span', { class: 'up-st', text: it.state === 'queued' ? 'queued' : 'uploading…' });
      it.bar = h('span', { class: 'up-bar' + (it.progressed ? '' : ' up-ind'), role: 'progressbar', 'aria-label': 'Uploading ' + it.name });
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
    if (!it.bar || !it.progressed) return;
    it.bar.classList.remove('up-ind');
    const pct = it.total > 0 ? Math.min(100, Math.round((it.loaded / it.total) * 100)) : 0;
    it.bar.style.setProperty('--p', pct + '%');
    it.bar.setAttribute('aria-valuenow', String(pct));
    if (it.label) it.label.textContent = pct + '%';
  }

  function start(it) {
    const ctrl = new AbortController();
    Object.assign(it, { state: 'uploading', ctrl, loaded: 0, total: it.size, progressed: false, error: '' });
    draw(it);
    const mine = () => !dead && it.ctrl === ctrl;
    const onProgress = (loaded, total) => {
      if (!mine()) return;
      Object.assign(it, { loaded, total: total || it.size, progressed: true });
      progress(it);
    };
    api.upload(it.file, { signal: ctrl.signal, onProgress }).then(r => {
      if (!mine()) return;
      Object.assign(it, { ctrl: null, state: 'done', sha256: r.sha256, size: r.size ?? it.size });
      settled(it);
    }, err => {
      if (!mine()) return;
      Object.assign(it, { ctrl: null, state: 'failed', error: model.errorText(err, Date.now()) });
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
  const items = () => list.map(({ id, name, size, media_type, state, loaded, total, sha256, error }) => ({ id, name, size, media_type, state, loaded, total, sha256, error }));

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
