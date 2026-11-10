import test from 'node:test';
import assert from 'node:assert/strict';
import { PRIORITIES, markPriority } from '../static/prio.js';

// Just the parts of a button markPriority touches.
const button = value => ({ value, className: 'stale', attrs: {}, setAttribute(k, v) { this.attrs[k] = v; } });
const marks = seg => seg.children.map(b => [b.className, b.attrs['aria-pressed']]);

test('markPriority marks the chosen priority for the eye and for assistive technology', () => {
  const seg = { children: PRIORITIES.map(button) };
  markPriority(seg, 'wake_idle');
  assert.deepEqual(marks(seg), [['', 'false'], ['on', 'true'], ['', 'false']]);
  markPriority(seg, 'hold');
  assert.deepEqual(marks(seg), [['', 'false'], ['', 'false'], ['on', 'true']]);
});

test('markPriority with no choice leaves every button unpressed, never unmarked', () => {
  const seg = { children: PRIORITIES.map(button) };
  markPriority(seg, 'wake');
  markPriority(seg, null); // the reaction picker's override, cleared
  assert.deepEqual(marks(seg), [['', 'false'], ['', 'false'], ['', 'false']]);
});
