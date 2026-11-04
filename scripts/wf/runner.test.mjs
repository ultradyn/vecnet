import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

// The workflow's `runner` arg: run the real workflow with an in-memory harness
// (every selected task already passed, so only the lenses and the gate are
// dispatched) and read the commands its prompts give.
const source = await readFile(new URL('../../docs/plans/vecnet-core-build.workflow.mjs', import.meta.url), 'utf8');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const execute = new AsyncFunction('args', 'agent', 'parallel', 'phase', 'log',
  source.replace('export const meta =', 'const meta ='));

async function prompts(extra = {}) {
  const seen = [];
  const tasks = [1, 2, 3, 4, 5];
  await execute({ startTask: 1, endTask: 5, finalReview: true, runStamp: 'fixture', statusDir: '/fixture/status', parallelGroups: [], ...extra },
    async (prompt, { label }) => {
      seen.push(prompt);
      if (label === 'Preflight') return { ok: true, problems: [], head_sha: 'a'.repeat(40), passed_tasks: tasks, dirty_files: [], baseline_suite: 'passed' };
      if (label.startsWith('Lens: ')) return { findings: [] };
      if (label.startsWith('Gate')) return { pass: true, head_sha: 'b'.repeat(40), fix_diff_concerns: [], checks: [] };
      assert.fail(`unexpected agent dispatch: ${label}`);
    }, jobs => Promise.all(jobs.map(job => job())), () => {}, () => {});
  return seen.join('\n');
}

test('without runner the commands are the local build2 ones', async () => {
  const text = await prompts();
  assert.match(text, /`build2 go test -p 2 -count=1 \.\/\.\.\.`/);
  assert.match(text, /`CGO_ENABLED=1 CC=\/usr\/bin\/gcc build2 go test -race -p 2 \.\/\.\.\.`/);
  assert.doesNotMatch(text, /just bb/);
});

test('runner prefixes every test, vet and mod command, and leaves the local ones alone', async () => {
  const text = await prompts({ runner: 'just bb ' });
  assert.match(text, /`just bb build2 go test -p 2 -count=1 \.\/\.\.\.`/);
  assert.match(text, /`just bb build2 go vet \.\/\.\.\.`/);
  assert.match(text, /`just bb build2 go mod tidy -diff`/);
  assert.match(text, /`just bb CGO_ENABLED=1 CC=\/usr\/bin\/gcc build2 go test -race -p 2 \.\/\.\.\.`/);
  assert.doesNotMatch(text, /`build2 go (test|vet|mod)/, 'an unprefixed test/vet/mod command remains');
  // The ISA check reads a local binary, so its build stays local.
  assert.match(text, /`CGO_ENABLED=0 build2 go build -p 2 -o bin\/ \.\/cmd\/\.\.\.` then `readelf/);
  assert.match(text, /prefix them with `just bb`/);
});
