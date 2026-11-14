import test from 'node:test';
import assert from 'node:assert/strict';
import { permissionRequest, CANCEL_OPTION } from '../src/backends/acp/permission.ts';
import { finishPermission, validateLaunchPermission } from '../src/contract/permission.ts';

function request(toolCall: Record<string, unknown> = {}) {
  return { sessionId: 'session', toolCall: { toolCallId: 'tool', ...toolCall }, options: [
    { optionId: 'one', name: 'Allow once', kind: 'allow_once' }, { optionId: 'always', name: 'Allow always', kind: 'allow_always' },
    { optionId: 'deny', name: 'Reject once', kind: 'reject_once' }, { optionId: 'deny-always', name: 'Reject always', kind: 'reject_always' },
  ] };
}
test('ACP grant mapping preserves scope and supplies a local cancel default', () => {
  const r = permissionRequest(request(), 'request');
  assert.deepEqual(r.options.map(o => o.grants), ['once', 'always', 'deny', 'deny_always', 'cancel']);
  assert.equal(r.permission.scope, 'always'); assert.equal(r.defaultOnExpiry, CANCEL_OPTION);
});
test('ACP command argv is only accepted when supplied as an array; kind/path/host remain reported', () => {
  const p = permissionRequest(request({ kind: 'execute', rawInput: { argv: ['git', 'status'], cwd: '/workspace' }, locations: [{ path: '/workspace/file' }] }), 'request').permission;
  assert.deepEqual(p.command?.argv, ['git', 'status']); assert.equal(p.fidelity.command, 'argv'); assert.equal(p.fidelity.kind, 'reported');
  assert.equal(p.fidelity.paths, 'reported');
  assert.equal(permissionRequest(request({ kind: 'execute', rawInput: { command: 'git status' } }), 'r').permission.command?.argv, undefined);
});
for (const [toolKind, kind, access] of [['read', 'other', 'read'], ['edit', 'edit', 'write'], ['delete', 'edit', 'delete'], ['move', 'edit', 'move']]) {
  test(`ACP ${toolKind} reports ${kind}/${access} without claiming enforcement`, () => {
    const p = permissionRequest(request({ kind: toolKind, locations: [{ path: '/workspace/file' }] }), 'request').permission;
    assert.equal(p.kind, kind); assert.equal(p.paths?.[0].access, access); assert.equal(p.fidelity.paths, 'reported');
  });
}
test('ACP network/MCP mapping and bounded canonical permission use existing daemon contract', () => {
  const p = permissionRequest(request({ kind: 'fetch', rawInput: { url: 'https://example.invalid:8443/path' } }), 'request').permission;
  assert.deepEqual(p.host, { name: 'example.invalid', protocol: 'https', port: 8443 }); assert.equal(p.fidelity.host, 'reported');
  const mcp = permissionRequest(request({ name: 'mcp__server__tool', kind: 'read' }), 'request').permission;
  assert.equal(mcp.kind, 'mcp_tool');
  const canonical = finishPermission(p, { machineId: 'test', jobId: 'job_00000000000000000000000000', permissionMode: 'ask', preset: 'fake', backend: 'acp', workspaceRoot: process.cwd(), home: process.cwd() });
  assert.deepEqual(validateLaunchPermission(canonical), []); assert.equal(canonical.outside_workspace, 'unknown');
});
test('ACP malformed or future permission options fail closed, including local cancel id collisions', () => {
  for (const options of [[], [{ optionId: 'x', name: 'Future', kind: 'future' }], [{ optionId: CANCEL_OPTION, name: 'Allow', kind: 'allow_once' }]]) {
    assert.throws(() => permissionRequest({ ...request(), options }, 'request'), /permission_options/);
  }
  assert.throws(() => permissionRequest(request({ locations: [{ path: '../escape' }] }), 'request'), /location/);
});
