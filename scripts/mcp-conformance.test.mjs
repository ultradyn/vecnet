import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { bootDemo, root } from './ui/lib.mjs';

async function conformance(origin, token) {
  const child = spawn(process.execPath, ['scripts/mcp-conformance.mjs', origin], {
    cwd: root,
    env: { ...process.env, VECNET_MCP_TOKEN: token },
    stdio: ['ignore', 'pipe', 'pipe'],
    timeout: 30000,
  });
  let stdout = '', stderr = '';
  child.stdout.on('data', data => { stdout += data; });
  child.stderr.on('data', data => { stderr += data; });
  const [code, signal] = await once(child, 'close');
  return { code, signal, stdout, stderr };
}

const checks = [
  'unauthenticated challenge',
  'protected-resource metadata',
  'authorization-server metadata',
  'server/discover',
  'initialize older revision',
  'initialize supported older revision',
  'tools/list',
  'setup guide and prompt',
  'events/list',
  'subscription status',
  'invalid Origin',
  'GET without a session',
];

test('conformance passes all twelve checks with a seeded peer bearer', { timeout: 120000 }, async () => {
  const env = await bootDemo();
  try {
    const token = Object.values(env.demo.peers).find(peer => peer.secret)?.secret;
    assert.ok(token, 'demo must seed a peer bearer');
    const result = await conformance(env.demo.url, token);
    assert.equal(result.stdout.includes(token), false, 'stdout must not contain the bearer');
    assert.equal(result.stderr.includes(token), false, 'stderr must not contain the bearer');
    assert.equal(result.signal, null);
    assert.equal(result.code, 0, result.stdout + result.stderr);
    assert.deepEqual(result.stdout.trim().split('\n'), checks.map(check => `PASS ${check}`));
    assert.equal(result.stderr, '');
  } finally {
    await env.close();
  }
});

test('conformance fails without revealing an invalid bearer', { timeout: 120000 }, async () => {
  const env = await bootDemo();
  try {
    const result = await conformance(env.demo.url, 'bogus');
    assert.equal(result.signal, null);
    assert.equal(result.code, 1);
    assert.equal(result.stdout.includes('bogus'), false);
    assert.equal(result.stderr.includes('bogus'), false);
    assert.equal(result.stdout.trim().split('\n').length, checks.length);
    assert.match(result.stdout, /^FAIL server\/discover$/m);
    assert.equal(result.stderr, '');
  } finally {
    await env.close();
  }
});
