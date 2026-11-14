// Inline image thumbnails for the thread, and a full-size viewer. The thread
// rebuilds every message node on each update, so render() builds a fresh node
// from the cache each time and onReady asks the thread to draw again; a node
// handed out is never changed afterwards. The rules live in thumbs-model.js.
import { h } from './dom.js';
import { bytes, clean } from './format.js';
import { createThumbCache } from './thumbs-model.js';

// chip is today's attachment chip: cleaned name (bidi-isolated by base.css),
// size, and the hash on hover. Downloads stay in the thread's side panel.
function chip(att) {
  return h('span', { class: 'att', title: att.sha256 || '' },
    h('span', { class: 'nm', text: att.name || att.media_type || 'attachment' }),
    h('span', { class: 'sz', text: bytes(att.size) }));
}

// An SVG blob URL inherits this UI's origin. It must NEVER enter the DOM:
// opening it as a document could execute same-origin scripts. Decode only in
// a detached Image (SVG image mode disables scripts/external resources), then
// hand the cache a freshly encoded PNG. No SVG markup is inserted anywhere.
export async function rasterizeSVG(blob, timeoutMs = 5000) {
  const img = new Image();
  let url = URL.createObjectURL(new Blob([blob], { type: 'image/svg+xml' }));
  let timer, active = true;
  const revoke = () => { if (url) { URL.revokeObjectURL(url); url = null; } };
  const work = async () => {
    img.src = url;
    await img.decode();
    if (!active) throw new Error('SVG timeout');
    let w = img.naturalWidth, h = img.naturalHeight;
    if (!(w > 0 && h > 0)) {
      // XML parsing is inert. Read root dimensions only, never attach nodes.
      const root = new DOMParser().parseFromString(await blob.text(), 'image/svg+xml').documentElement;
      if (!active) throw new Error('SVG timeout');
      const length = name => {
        const value = (root.getAttribute(name) || '').trim();
        return /^\d+(?:\.\d+)?(?:px)?$/.test(value) ? parseFloat(value) : 0;
      };
      const vb = (root.getAttribute('viewBox') || '').trim().split(/[\s,]+/).map(Number);
      w = length('width'); h = length('height');
      if (!(w > 0 && h > 0) && vb.length === 4 && vb.every(Number.isFinite) && vb[2] > 0 && vb[3] > 0) { w = vb[2]; h = vb[3]; }
    }
    if (!(Number.isFinite(w) && Number.isFinite(h) && w > 0 && h > 0)) { w = 512; h = 512; }
    const longest = Math.max(w, h), scale = Math.max(480, Math.min(2048, longest)) / longest;
    const canvas = document.createElement('canvas');
    canvas.width = Math.max(1, Math.round(w * scale));
    canvas.height = Math.max(1, Math.round(h * scale));
    const ctx = canvas.getContext('2d');
    if (!ctx) throw new Error('no canvas');
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
    revoke(); // SVG bytes are no longer needed, even while PNG encoding waits.
    return new Promise((resolve, reject) => {
      canvas.toBlob(png => png ? resolve(png) : reject(new Error('no PNG')), 'image/png');
    });
  };
  try {
    return await Promise.race([work(), new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('SVG timeout')), timeoutMs); })]);
  } finally {
    active = false;
    clearTimeout(timer);
    revoke();
    img.removeAttribute('src');
  }
}

export function createThumbs({ api, onReady = () => {} }) {
  let dead = false, viewer = null;
  const cache = createThumbCache({ api, URL: globalThis.URL, rasterize: rasterizeSVG, onReady: () => { if (!dead) onReady(); } });

  // A decode error is the last check: bytes typed as an image that are not one.
  function broken(sha) {
    if (dead) return;
    cache.broken(sha);
    onReady();
  }

  // render takes hosted as eligible() does: the history page's word on
  // whether the hub hosts this message's bytes.
  function render(att, hosted) {
    const st = cache.state(att, hosted);
    const label = clean(att.name || 'image');
    if (st.state === 'loading') {
      return h('span', { class: 'thumb wait', role: 'img', 'aria-label': 'Loading ' + label, title: label });
    }
    if (st.state !== 'ok') return chip(att);
    const img = h('img', { src: st.url, alt: label, decoding: 'async', draggable: 'false' });
    img.addEventListener('error', () => broken(att.sha256), { once: true });
    return h('button', { type: 'button', class: 'thumb', 'data-sha': att.sha256, title: label + ' · ' + bytes(att.size), 'aria-label': 'Open ' + label, onclick: e => open(att, e.currentTarget, hosted) }, img);
  }

  function close() {
    if (viewer && viewer.dlg.open) viewer.dlg.close();
  }

  // open shows the image full size in an in-page dialog. Escape and a click
  // outside the image close it; focus goes back to the thumbnail, or to the
  // same image's thumbnail when a redraw has replaced it meanwhile.
  function open(att, opener, hosted) {
    const st = cache.state(att, hosted);
    if (dead || st.state !== 'ok') return;
    close();
    const host = opener && opener.closest && opener.closest('[id]'), msgId = host ? host.id : '';
    const label = clean(att.name || 'image');
    const img = h('img', { src: st.url, alt: label, draggable: 'false' });
    img.addEventListener('error', () => { close(); broken(att.sha256); }, { once: true });
    const dlg = h('dialog', { class: 'viewer', 'aria-label': label },
      h('figure', {}, img, h('figcaption', {}, h('span', { class: 'nm', text: label }), h('span', { class: 'sz', text: bytes(att.size) }))),
      h('button', { type: 'button', class: 'btn vx', 'aria-label': 'Close', text: '×', onclick: () => close() }));
    // The dialog box fills the viewport, so a click on it rather than on its
    // contents is a click outside the image.
    dlg.addEventListener('click', e => { if (e.target === dlg || e.target === dlg.firstElementChild) close(); });
    dlg.addEventListener('close', () => {
      dlg.remove();
      if (viewer && viewer.dlg === dlg) viewer = null;
      if (dead) return;
      let back = opener && opener.isConnected ? opener : null;
      const again = !back && msgId && document.getElementById(msgId);
      if (again) back = again.querySelector('button.thumb[data-sha="' + att.sha256 + '"]');
      if (!back) back = document.querySelector('button.thumb[data-sha="' + att.sha256 + '"]');
      if (back) back.focus();
    });
    viewer = { dlg };
    document.body.append(dlg);
    dlg.showModal();
  }

  function release() {
    if (dead) return;
    dead = true;
    if (viewer) { const { dlg } = viewer; viewer = null; if (dlg.open) dlg.close(); dlg.remove(); }
    cache.release();
  }

  return { render, open, release, stats: cache.stats };
}
