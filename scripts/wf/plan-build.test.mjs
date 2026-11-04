import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

// Run the real workflow with an in-memory harness. Every selected task has
// already passed, so no real agent, worktree, build or network call is made.
const source = await readFile(new URL('../../docs/plans/vecnet-core-build.workflow.mjs', import.meta.url), 'utf8');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const execute = new AsyncFunction('args', 'agent', 'parallel', 'phase', 'log',
  source.replace('export const meta =', 'const meta ='));

async function gatePrompts({ startTask = 1, endTask = 20, failFirst = false } = {}) {
  const prompts = [];
  const taskNumbers = Array.from({ length: endTask - startTask + 1 }, (_, i) => startTask + i);
  const result = await execute({
    startTask, endTask, finalReview: true, runStamp: 'fixture-hub',
    statusDir: '/fixture/status', parallelGroups: [],
  }, async (prompt, { label }) => {
    if (label === 'Preflight') return {
      ok: true, problems: [], head_sha: 'a'.repeat(40),
      passed_tasks: taskNumbers, dirty_files: [], baseline_suite: 'passed',
    };
    if (label.startsWith('Lens: ')) return { findings: [] };
    if (label === 'Gate repair') return { commits: [], notes: 'fixture repair' };
    if (label === 'Gate' || label === 'Gate (re-run)') {
      prompts.push(prompt);
      const pass = !failFirst || prompts.length > 1;
      return {
        pass, head_sha: 'b'.repeat(40), fix_diff_concerns: [],
        checks: [{ name: 'coverage', command: 'fixture', ok: pass, output_tail: '' }],
      };
    }
    assert.fail(`unexpected agent dispatch: ${label}`);
  }, jobs => Promise.all(jobs.map(job => job())), () => {}, () => {});
  assert.equal(result.status, 'complete');
  assert.deepEqual(result.completed.map(task => task.task), taskNumbers);
  return prompts;
}

function assertCoverage(prompt, start, end) {
  const check = prompt.split('\n').find(line => line.startsWith('8. '));
  assert.ok(check, 'task coverage remains a required gate check');
  assert.match(check, new RegExp(`Every task ${start}-${end}\\b`));
  assert.match(check, /\/fixture\/status\/task-NN\.json/);
  assert.match(check, /status.*passed/);
  assert.match(check, /run_stamp.*fixture-hub/);
  assert.match(check, /task.*number/);
  assert.match(check, /git cat-file -e.*head_sha.*\^\{commit\}/);
  assert.match(check, /git merge-base --is-ancestor.*head_sha.*HEAD/);
  assert.match(check, /git show -s --format=%B.*head_sha/);
  assert.match(check, /Plan-Task: NN/);
  assert.match(check, /missing.*fail/i);
  assert.doesNotMatch(check, /master\.\.HEAD|grep -c/);
}

test('hub-only final review gates exactly tasks 1-5 using run-specific ancestor commits', async () => {
  const prompts = await gatePrompts({ endTask: 5 });
  assert.equal(prompts.length, 1);
  assertCoverage(prompts[0], 1, 5);
});

test('a full-plan run still requires every task 1-20', async () => {
  const prompts = await gatePrompts();
  assertCoverage(prompts[0], 1, 20);
});

test('a later slice gates its selected tasks with passed status and matching trailers', async () => {
  const prompts = await gatePrompts({ startTask: 6, endTask: 20 });
  assertCoverage(prompts[0], 6, 20);
});

test('the repair retry retains the same strict coverage check', async () => {
  const prompts = await gatePrompts({ endTask: 5, failFirst: true });
  assert.equal(prompts.length, 2);
  for (const prompt of prompts) assertCoverage(prompt, 1, 5);
});
