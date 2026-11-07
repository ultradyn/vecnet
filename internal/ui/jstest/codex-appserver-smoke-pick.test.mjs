import test from 'node:test';
import assert from 'node:assert/strict';
import { pickSmokeThread } from '../../../scripts/codex-appserver-smoke-pick.mjs';

const marker = 'SMOKE-RUN-123-unique';
const user = text => ({ type: 'userMessage', content: [{ type: 'text', text }] });
const candidate = (id, ...items) => ({ id, turns: [{ items }] });

test('smoke selects the thread whose first user input contains the run marker', () => {
  assert.equal(pickSmokeThread([candidate('smoke', user(`${marker}. This is a test session.`))], marker), 'smoke');
});

test('smoke ignores unmarked same-cwd helper threads in either order', () => {
  const smoke = candidate('smoke', user(marker));
  const helper = candidate('helper', user('Generate a title for this session.'));
  assert.equal(pickSmokeThread([helper, smoke], marker), 'smoke');
  assert.equal(pickSmokeThread([smoke, helper], marker), 'smoke');
});

test('smoke keeps polling until a first user input carries this run marker', () => {
  const unmarked = [
    { id: 'startup', turns: [] },
    candidate('empty'),
    candidate('helper', user('Another prompt')),
    candidate('stale', user('SMOKE-RUN-122-older')),
    candidate('assistant', { type: 'agentMessage', text: marker }),
    candidate('later-input', user('Original prompt'), user(marker)),
    { id: 'later-turn', turns: [{ items: [user('Original prompt')] }, { items: [user(marker)] }] },
    candidate('image', { type: 'userMessage', content: [{ type: 'image', text: marker }] }),
  ];
  assert.equal(pickSmokeThread([], marker), undefined);
  assert.equal(pickSmokeThread(unmarked, marker), undefined);
});

test('smoke aborts fatally when two first user inputs carry the marker', () => {
  assert.throws(() => pickSmokeThread([
    candidate('smoke-a', user(`${marker} private prompt`)),
    candidate('smoke-b', user(marker)),
  ], marker), error => error.fatal === true && /ambiguous: 2/.test(error.message)
    && error.message.includes('smoke-a') && error.message.includes('smoke-b')
    && !error.message.includes(marker) && !error.message.includes('private prompt'));
});

test('smoke supports direct user text and split text content', () => {
  assert.equal(pickSmokeThread([candidate('direct', { type: 'userMessage', text: marker })], marker), 'direct');
  assert.equal(pickSmokeThread([candidate('split', {
    type: 'userMessage', content: [{ type: 'text', text: 'SMOKE-RUN-' }, { type: 'text', text: '123-unique' }],
  })], marker), 'split');
});

test('smoke rejects an empty marker rather than matching every prompt', () => {
  assert.throws(() => pickSmokeThread([candidate('unmarked', user('Hello'))], ''), /missing smoke prompt marker/);
});
