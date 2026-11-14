import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { privateSmokeEnvironment } from './launcher-disposable-smoke.mjs';

test('disposable controller defaults and import never dispatch a model or service', () => {
  const file = new URL('./launcher-disposable-smoke.mjs', import.meta.url);
  const output = execFileSync(process.execPath, [file.pathname], { encoding: 'utf8' });
  assert.match(output, /^SKIP: --run/);
  assert.equal(execFileSync(process.execPath, ['--input-type=module', '-e', `await import(${JSON.stringify(file.href)})`, '--', '--run'], { encoding: 'utf8' }), '');
});

test('private child environment removes host routing without changing parent HOME or exposing provider values', () => {
  const parent = { HOME: '/real/home', PATH: '/usr/bin:/bin', CODEX_HOME: '/real/codex', CODEX_THREAD_ID: 'old', VECNET_CONFIG: '/real/config',
    XDG_DATA_HOME: '/real/data', GIT_CONFIG_GLOBAL: '/real/git', BASH_ENV: '/real/rc', ENV: '/real/env', NODE_OPTIONS: '--require=/real/hook', LD_PRELOAD: '/real/preload',
    ANTHROPIC_API_KEY: 'synthetic-provider-only', PLAYWRIGHT_MODULE: '/cache/playwright/index.mjs', PLAYWRIGHT_BROWSERS_PATH: '/cache/browsers' };
  const before = structuredClone(parent), root = '/var/tmp/disposable-fixture';
  const env = privateSmokeEnvironment(parent, root);
  assert.deepEqual(parent, before);
  assert.equal(env.HOME, root + '/home');
  assert.equal(env.CODEX_HOME, root + '/home/.codex');
  assert.equal(env.XDG_DATA_HOME, root + '/home/.local/share');
  assert.ok(env.PATH.startsWith(root + '/bin:'));
  assert.equal(env.ANTHROPIC_API_KEY, parent.ANTHROPIC_API_KEY);
  for (const key of ['CODEX_THREAD_ID', 'VECNET_CONFIG', 'GIT_CONFIG_GLOBAL', 'BASH_ENV', 'ENV', 'NODE_OPTIONS', 'LD_PRELOAD']) assert.equal(env[key], undefined);
  assert.equal(env.PLAYWRIGHT_MODULE, '/cache/playwright/index.mjs');
});
