import test from 'node:test';
import assert from 'node:assert/strict';
import { readAllButton } from '../static/read-all.js';
import { El } from './activity-dom-fixture.mjs';

class ButtonEl extends El {
  get classList() {
    return {
      add: c => { this.className += ' ' + c; },
      remove: c => { this.className = this.className.split(/\s+/).filter(x => x !== c).join(' '); },
    };
  }
}
function world(t, run, onDone) {
  const previous = globalThis.document, toast = new ButtonEl('div'), invalidated = [];
  globalThis.document = { createElement: tag => new ButtonEl(tag), getElementById: () => toast };
  t.after(() => { globalThis.document = previous; });
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const btn = readAllButton({ markAllRead: run }, { invalidate: tag => invalidated.push(tag) }, {
    label: 'Mark all from agent\u202e read', confirm: 'Confirm\u202e mark read', onDone,
  });
  return { btn, toast, invalidated, click: () => btn.on.click() };
}

test('bulk read labels stay sanitized after confirmation timeout and completion', async t => {
  const w = world(t, async () => 2);
  assert.equal(w.btn.textContent, 'Mark all from agent read');
  await w.click();
  assert.equal(w.btn.textContent, 'Confirm mark read');
  t.mock.timers.tick(3000);
  assert.equal(w.btn.textContent, 'Mark all from agent read');
  await w.click();
  await w.click();
  assert.equal(w.btn.textContent, 'Mark all from agent read');
  assert.equal(w.toast.textContent, 'Marked 2 read.');
  assert.deepEqual(w.invalidated, ['inbox']);
});

test('bulk read partial limit tells the operator to run again and never calls onDone', async t => {
  let done = false;
  const w = world(t, async () => { throw Object.assign(new Error('limit'), { code: 'read_all_limit', marked: 100000 }); }, () => { done = true; });
  await w.click();
  await w.click();
  assert.equal(w.toast.textContent, 'Marked 100000 read; more remain. Run again.');
  assert.equal(done, false);
  assert.equal(w.btn.running(), false);
  assert.deepEqual(w.invalidated, ['inbox']);
});
