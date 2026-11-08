import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, rm, realpath } from 'node:fs/promises';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { fileURLToPath } from 'node:url';
import plugin from '../src/backends/acp/index.ts';
import type { AcpLaunchSpec } from '../src/backends/acp/workspace.ts';
import type { PluginContext, SessionHandle } from '../src/backend.ts';
import { openLedger } from '../src/ledger.ts';
import { createOutbox } from '../src/outbox.ts';
import { createParentBridge } from '../src/parent-bridge.ts';
import { parseConfig } from '../src/config.ts';
import { routeApproval } from '../src/policy.ts';
import type { Directory } from '../src/directory.ts';
import type { SendRequest } from '../../hub-client/src/wire.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import { threadDelivery } from './fixtures.ts';
import { until } from './daemon-harness.ts';

for (const scenario of ['reuse-privileged', 'reuse-scope']) test(`C1 real parent bridge refuses completed RPC ID reuse: ${scenario}`, async t => {
  const parent = process.env.ACP_TEST_ROOT ?? join(process.env.XDG_CACHE_HOME ?? join(homedir(), '.cache'), 'vecnet-launcher', 'acp-tests');
  assert.ok(parent.startsWith('/') && parent !== '/tmp' && !parent.startsWith('/tmp/'));
  await mkdir(parent, { recursive: true });
  const canonicalParent = await realpath(parent);
  assert.ok(canonicalParent !== '/tmp' && !canonicalParent.startsWith('/tmp/'));
  const dir = await mkdtemp(join(parent, 'acp-fix1-bridge-')), workspace = join(dir, 'workspace');
  await mkdir(workspace);
  const clock = fakeClock(Date.parse('2026-11-08T00:00:00Z')), log = memoryLog(), ledger = openLedger(':memory:', clock);
  const outbox = createOutbox({ ledger, clock, log, hub: { async send() { throw new Error('no hub writes'); }, async receipt() {} } });
  const cfg = parseConfig({ host: 'test-host', hub: { url: 'http://127.0.0.1:9', credential_file: '/synthetic' }, state_dir: dir,
    backends: [], projects: {}, approvals: { to_operator: { presets: ['privileged', 'outside-workspace'] } } });
  const directory: Directory = { operatorPeerId: () => 'peer_operator', isOperator: id => id === 'peer_operator', name: id => id,
    createdBy: () => undefined, get: () => undefined, kindOf: () => undefined, async refresh() {}, async ensureKnown() { return false; }, async onDirectoryUpdated() {}, onRemoved() {} };
  const jobId = 'job_00000000000000000000000000';
  ledger.createJob({ jobId, threadId: 'thr_synthetic', launchMessageId: 'msg_launch', requester: 'peer_parent', policyKey: 'default',
    backend: 'acp', preset: 'fake', permissionMode: 'ask', project: 'scratch', title: 'C1 test', instructions: 'synthetic',
    input: { backend: 'acp', project: 'scratch', preset: 'fake', permission_mode: 'ask' }, tag: `vl-${jobId}`, state: 'running',
    sessionRef: null, sessionKind: 'idle', turnCursor: null, branch: null, workspace, lastActivityAt: clock.now(), parkedAt: null, deadlineAt: clock.now() + 100000, createdAt: clock.now() });
  const routes: string[] = [];
  const bridge = createParentBridge({ ledger, outbox, clock, log, cfg, self: 'peer_launcher', home: dir, dir: directory,
    route(permission, job) { const route = routeApproval(permission, job, directory, cfg, dir); routes.push(route.to); return route; } });
  const wirePath = join(dir, 'wire.jsonl');
  const ctx: PluginContext = { host: 'test-host', now: () => new Date(clock.now()), log, store: ledger.kv('plugin:acp'), redact: s => s,
    sleep: (ms, signal) => new Promise((resolve, reject) => {
      const timer = setTimeout(done, ms); function done() { signal?.removeEventListener('abort', abort); resolve(); }
      function abort() { clearTimeout(timer); signal?.removeEventListener('abort', abort); reject(new Error('aborted')); }
      signal?.addEventListener('abort', abort, { once: true }); if (signal?.aborted) abort();
    }), exec: async () => { throw new Error('no commands'); }, checkRoute: async () => { throw new Error('no routes'); }, readSecretFile: async () => { throw new Error('no auth reads'); } };
  const backend = await plugin.create({ workspace_root: workspace, presets: { fake: { provider: 'fixture', mode_id: 'ask', argv: [process.execPath,
    fileURLToPath(new URL('./fixtures/fake-acp-agent.mjs', import.meta.url)), scenario, wirePath] } } }, ctx);
  let h: SessionHandle | undefined;
  t.after(async () => { bridge.abandonAll(jobId, 'session_ended'); if (h) await h.settle('cancel'); ledger.close(); await rm(dir, { recursive: true, force: true }); });
  const spec: AcpLaunchSpec = { jobId, tag: `vl-${jobId}`, title: 'C1', prompt: 'Synthetic permissions', branch: '', preset: 'fake', permissionMode: 'ask',
    project: { alias: 'scratch', repoRoot: '', baseRef: '', scratch: true }, workspaceMode: 'temp', env: {}, attachments: [] };
  h = await backend.launch(spec, bridge.hooksFor(ledger.job(jobId)!, () => String(h?.ref.extra?.workspace ?? workspace), () => {}));
  await until(() => ledger.openApprovals().length === 1, 'first requester approval');
  const first = ledger.openApprovals()[0]; assert.equal(first.addressee, 'peer_parent'); assert.equal(first.permission.scope, 'once');
  const answer = threadDelivery(first.threadId!, 'peer_parent', '', 'wake');
  answer.message.kind = 'approval_response'; answer.message.approval_response = { approval_id: first.approvalId, decision: 'Allow once' };
  assert.equal(await bridge.handleApprovalResponse(answer), 'consumed');
  // Wait until the fixture either receives the second cached decision (BASE)
  // or is rejected/disconnected before a second hook (the required behavior).
  let wire: Array<Record<string, any>> = [];
  await until(() => h!.state().kind === 'ended' || routes.length === 2, 'reused permission processed');
  await new Promise(resolve => setTimeout(resolve, 30));
  wire = (await readFile(wirePath, 'utf8')).trim().split('\n').filter(Boolean).map(s => JSON.parse(s));
  const grants = wire.filter(m => m.id === 1001 && m.result?.outcome?.outcome === 'selected' && m.result.outcome.optionId === 'yes');
  assert.equal(grants.length, 1, 'a completed RPC identity must never receive a second cached grant');
  assert.deepEqual(routes, ['requester'], 'reused ID must be rejected before invoking the real cached parent bridge');
  assert.equal(ledger.approval(first.approvalId)?.decision, 'yes');
  assert.equal(ledger.openApprovals().length, 0);
});
