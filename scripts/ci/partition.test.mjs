import assert from 'node:assert/strict';
import test from 'node:test';
import { partition, goInventory } from './partition.mjs';

test('partitions every discovered item exactly once, including newly added cases', () => {
  const items = Array.from({ length: 101 }, (_, n) => `TestCase${n}`);
  const groups = Array.from({ length: 12 }, (_, n) => partition(items, n, 12));
  assert.deepEqual(groups.flat().sort(), items.sort());
  assert.equal(new Set(groups.flat()).size, 101);
  assert.ok(groups.every(group => group.length > 0));
  assert.deepEqual(partition(['only'], 0, 1), ['only']);
});
test('refuses invalid partitions and duplicate inventory instead of silently losing coverage', () => {
  for (const [index, count] of [[-1, 2], [2, 2], [0, 0], [0.5, 2], [0, NaN]]) assert.throws(() => partition(['a'], index, count));
  assert.throws(() => partition(['a', 'a'], 0, 2));
});
test('Go discovery retains tests, examples, fuzz seed targets, and no-test packages', () => {
  const listing = 'TestOne\nExampleTwo\nExample\nFuzzThree\nok  \tgithub.com/XertroV/vecnet/internal/a\t0.003s\n?   \tgithub.com/XertroV/vecnet/cmd/b\t[no test files]\n';
  assert.deepEqual(goInventory(listing), [
    { pkg: 'github.com/XertroV/vecnet/internal/a', names: ['TestOne', 'ExampleTwo', 'Example', 'FuzzThree'] },
    { pkg: 'github.com/XertroV/vecnet/cmd/b', names: [] },
  ]);
  assert.throws(() => goInventory('TestLost\n'), /inventory/);
  assert.throws(() => goInventory('FAIL\tx\n'), /inventory/);
});
