// DOM helpers. Peer-supplied strings become text nodes and attribute values
// here; nothing in the UI parses a string into markup (PROTOCOL §14.2). Text
// and titles pass through clean(), so a control or bidi override in a peer's
// name cannot reorder the words around it.
import { clean } from './format.js';

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
// its colour across renames. With ctx.avatars (avatar-model.js) and
// ctx.avatarOf(id) naming a hash, the peer's image replaces the letter once
// its bytes arrive; the letter stays while it loads and whenever it fails.
export function avatar(id, ctx) {
  const name = ctx.nameOf(id) || '?', kind = ctx.kindOf(id);
  let x = 0;
  for (const c of id || '') x = (x * 31 + c.charCodeAt(0)) >>> 0;
  const letter = name[0].toUpperCase();
  const el = h('div', { class: 'av ' + (kind === 'operator' ? 'op' : 'k' + (x % 6)), text: letter, 'aria-hidden': 'true' });
  const cache = ctx.avatars, hash = cache && ctx.avatarOf ? ctx.avatarOf(id) : null;
  if (!hash) return el;
  const show = st => {
    if (st.state !== 'ok') return;
    const img = h('img', { src: st.url, alt: '', decoding: 'async', draggable: 'false' });
    img.addEventListener('error', () => { cache.broken(hash); el.classList.remove('pic'); el.textContent = letter; });
    el.classList.add('pic');
    el.replaceChildren(img);
  };
  const st = cache.state(id, hash);
  if (st.state === 'loading') st.ready.then(() => show(cache.state(id, hash)));
  else show(st);
  return el;
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
