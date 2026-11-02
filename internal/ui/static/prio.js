// The priority picker both composers share (issue 21): a segmented control
// whose buttons each point aria-describedby at a hidden element holding
// PRIORITY_HELP, so a screen reader hears what a sighted operator reads. The
// visible tip (the priority's name and the same words) is aria-hidden, stays
// display:none until CSS shows it on hover or keyboard focus (base.css
// .ptips), and Escape hides it until focus or the pointer moves on (WCAG
// 1.4.13).
import { h } from './dom.js';
import { PRIORITY_HELP } from './format.js';

export const PRIORITIES = ['wake', 'wake_idle', 'hold'];
let seq = 0;

// prioritySeg returns the wrapper to place and the .seg whose buttons the
// caller marks .on.
export function prioritySeg(onPick) {
  const id = 'ptip' + (++seq) + '-';
  const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Priority' },
    PRIORITIES.map(p => h('button', { type: 'button', value: p, text: p, 'aria-describedby': id + p, onclick: () => onPick(p) })));
  const el = h('div', { class: 'ptips' }, seg,
    PRIORITIES.map(p => h('span', { class: 'ptip', 'data-p': p, 'aria-hidden': 'true' }, h('b', { text: p }), PRIORITY_HELP[p])),
    PRIORITIES.map(p => h('span', { id: id + p, hidden: true, text: PRIORITY_HELP[p] })));
  const calm = () => el.classList.remove('quiet');
  el.addEventListener('keydown', e => { if (e.key === 'Escape') el.classList.add('quiet'); });
  el.addEventListener('focusin', calm);
  el.addEventListener('pointerleave', calm);
  return { el, seg };
}
