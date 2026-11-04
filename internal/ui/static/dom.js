// DOM helpers. Peer-supplied strings become text nodes and attribute values
// here; nothing in the UI parses a string into markup (PROTOCOL §14.2). Text
// and titles pass through clean(), so a control or bidi override in a peer's
// name cannot reorder the words around it.
import { clean, idParts } from './format.js';
import { peerHref } from './peer-model.js';

// attrs: `class`, `text` (textContent), on<event> listeners, or an attribute.
// null, undefined and false leave an attribute off.
export function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = clean(v);
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'title' || k === 'aria-label') el.setAttribute(k, clean(v));
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids.flat()) {
    if (c === null || c === undefined || c === false) continue;
    el.append(typeof c === 'string' ? clean(c) : c);
  }
  return el;
}

// ctx supplies nameOf and kindOf. The tint is a hash of the id, so a peer keeps
// its colour across renames.
export function avatar(id, ctx) {
  const name = ctx.nameOf(id) || '?', kind = ctx.kindOf(id);
  let x = 0;
  for (const c of id || '') x = (x * 31 + c.charCodeAt(0)) >>> 0;
  return h('div', { class: 'av ' + (kind === 'operator' ? 'op' : 'k' + (x % 6)), text: name[0].toUpperCase(), 'aria-hidden': 'true' });
}

export function chip(text, tone) {
  return h('span', { class: 'chip' + (tone ? ' ' + tone : ''), text });
}

let toastTimer = 0;
export function toast(msg, bad) {
  const el = document.getElementById('toast');
  el.textContent = clean(msg);
  el.className = 'on' + (bad ? ' bad' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.className = bad ? 'bad' : ''; }, bad ? 6000 : 2500);
}

// Two clicks within ms confirm; otherwise the button falls back to label.
export function confirmButton(label, confirmLabel, onConfirm, ms = 3000) {
  let timer = 0;
  const btn = h('button', { type: 'button', class: 'btn', text: label });
  const disarm = () => {
    clearTimeout(timer);
    timer = 0;
    btn.textContent = label;
    btn.classList.remove('armed');
  };
  btn.addEventListener('click', () => {
    if (timer) { disarm(); onConfirm(); return; }
    btn.textContent = confirmLabel;
    btn.classList.add('armed');
    timer = setTimeout(disarm, ms);
  });
  return btn;
}

// idText shows an id as short() does (peer_…X6Z8B) but keeps the whole id in
// the text: the middle is a zero-size run (.idv .im) and the ellipsis is CSS
// generated content, which is never selected or copied. So any selection, a
// drag across a table row or select-all, copies the full id with no "…", and
// find-in-page matches it. user-select: all makes one click select the id.
export function idText(id, cls) {
  const p = idParts(id);
  return h('span', { class: 'idv' + (cls ? ' ' + cls : ''), title: id || null, translate: 'no' },
    h('span', { class: 'ih', text: p.head }),
    p.mid ? h('span', { class: 'im', text: p.mid }) : null,
    p.mid ? h('span', { class: 'it', text: p.tail }) : null);
}

// copyText puts text on the clipboard. The async Clipboard API needs a secure
// context, and a hub on the LAN is plain http, so a hidden textarea and
// execCommand stand in. Focus goes back where it was.
export async function copyText(text) {
  try {
    if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch { /* fall through to the textarea */ }
  const back = document.activeElement;
  const ta = h('textarea', { class: 'copybuf', readonly: true, 'aria-hidden': 'true', tabindex: '-1' });
  ta.value = text;
  document.body.append(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  ta.remove();
  if (back && back.focus) back.focus();
  return ok;
}

// copyButton copies text; it shows a check for a moment once it has. what
// names the thing for the label ("Copy peer id").
export function copyButton(text, what = 'id') {
  const label = 'Copy ' + what;
  let timer = 0;
  const btn = h('button', { type: 'button', class: 'cpy', title: label, 'aria-label': label });
  btn.addEventListener('click', async e => {
    e.preventDefault();
    e.stopPropagation();
    const ok = await copyText(text);
    clearTimeout(timer);
    if (!ok) { toast('Could not copy. Select the id and copy it instead.', true); return; }
    btn.classList.add('done');
    btn.setAttribute('aria-label', 'Copied');
    btn.title = 'Copied';
    timer = setTimeout(() => { btn.classList.remove('done'); btn.setAttribute('aria-label', label); btn.title = label; }, 1500);
  });
  return btn;
}

// idCopy is idText with its copy button, shown on hover or keyboard focus.
// Never put it inside a link: use idText there.
export function idCopy(id, what, cls) {
  if (!id) return idText(id, cls);
  return h('span', { class: 'idc' }, idText(id, cls), copyButton(id, what));
}

// peerLink names a peer and opens #peer/<id>. Inside another link (an inbox
// row, a sidebar row) an <a> cannot nest, so nested gives a span that the
// shell's one click handler routes (app.js); the row stays one tab stop, and
// the peer stays a link everywhere else. text defaults to the peer's name.
export function peerLink(id, text, { nested = false, cls = '' } = {}) {
  const href = peerHref(id);
  const label = text === undefined || text === null ? id || '?' : text;
  const c = 'plink' + (cls ? ' ' + cls : '');
  if (!href) return h('span', { class: cls || null, text: label });
  const title = label === id ? id : label + ' · ' + id;
  return nested
    ? h('span', { class: c, 'data-peer': id, title, text: label })
    : h('a', { class: c, href, title, text: label });
}
