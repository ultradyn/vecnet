import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';

test('WEB-MEM-3 T3 actual_app_entry isolated child', { timeout: 65000 }, async t => {
  const env = { ...process.env };
  delete env.NODE_TEST_CONTEXT;
  const casesPath = fileURLToPath(new URL('./app-teardown-cases.mjs', import.meta.url));
  const { error, stdout, stderr } = await new Promise(resolve => {
    execFile(process.execPath, [casesPath], {
      env, encoding: 'utf8', timeout: 60000,
      killSignal: 'SIGKILL', maxBuffer: 1048576,
    }, (error, stdout = '', stderr = '') => resolve({ error, stdout, stderr }));
  });

  const expected = { tests: 30, suites: 0, pass: 30, fail: 0, cancelled: 0, skipped: 0, todo: 0 };
  const totals = Object.fromEntries(Object.keys(expected).map(key => {
    const matches = [...stdout.matchAll(new RegExp(`^# ${key}\\b(.*)$`, 'gm'))];
    const value = matches.length === 1 ? matches[0][1] : '';
    const number = Number(value);
    return [key, /^ \d+$/.test(value) && Number.isSafeInteger(number) ? number : null];
  }));
  const exit = error ? (Number.isInteger(error.code) ? error.code : null) : 0;
  const signal = error?.signal ?? null;
  t.diagnostic('child stdout:\n' + (stdout || '(empty)'));
  t.diagnostic('child stderr:\n' + (stderr || '(empty)'));
  t.diagnostic(JSON.stringify({
    exit, signal, error: error?.code ?? null, detail: error?.stack ?? null,
    incomplete: !!error, totals, unavailable: Object.keys(totals).filter(key => totals[key] === null),
  }));

  assert.ifError(error);
  assert.equal(exit, 0, 'child exit');
  assert.equal(signal, null, 'child signal');
  assert.deepEqual(totals, expected, 'unique decimal child totals');
  const plans = [...stdout.matchAll(/^\d+\.\..*$/gm)].map(match => match[0]);
  assert.deepEqual(plans, ['1..30'], 'one complete child plan');
  const names = [...stdout.matchAll(/^ok \d+ - (WEB-MEM-3 T3 actual_app_entry .+)$/gm)]
    .map(match => match[1]);
  assert.equal(names.length, 30, 'all actual app cases passed');
  assert.equal(new Set(names).size, 30, 'all actual app case names distinct');
  assert.doesNotMatch(stdout, /^\s*(?:not ok\b|Bail out!)/m, 'no child failure or bailout');
});
