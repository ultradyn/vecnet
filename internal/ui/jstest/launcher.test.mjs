import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { buildLaunch, launcherChoices, launcherSetupCommands, launcherSetupIssue } from '../static/launcher-model.js';
import { inviteBlock, submitBody } from '../static/invite-model.js';
import { launcherSection } from '../static/launcher.js';

const now = Date.parse('2026-11-08T00:00:00Z');
const peer = 'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8B';
const row = () => ({ credential: 'active', peer: { peer_id: peer, kind: 'launcher', presence: 'idle',
  capabilities: ['launch:codex-app-server', 'launch:t3', 'project:vecnet', 'project:../../etc', 'preset:codex-app-server/default', 'preset:t3/sol', 'mode:ask', 'mode:auto', 'mode:garbage'],
  quota: { backends: { 'codex-app-server': 'ok', t3: 'down' } } } });
const values = () => ({ project: 'vecnet', backend: 'codex-app-server', preset: 'default', mode: 'ask', title: 'Fix launcher', prompt: 'Implement and verify the launcher.', deadline: '2026-11-08T01:00:00Z' });

// Minimal DOM for the real launcher form, including its default field values.
class FormEl {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.on = {}; this.value = ''; }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'id') this.id = String(v); if (k === 'value') this.value = String(v); }
  getAttribute(k) { return this.attrs[k]; }
  addEventListener(k, fn) { this.on[k] = fn; }
  append(...xs) { this.kids.push(...xs); }
  replaceChildren(...xs) { this.kids = xs; }
}
const formNodes = el => [el, ...(el.kids || []).flatMap(x => typeof x === 'object' ? formNodes(x) : [])];

test('rendered default deadline gives native launch at least its 2 h 10 min admission window', () => {
  const previous = globalThis.document;
  globalThis.document = { createElement: tag => new FormEl(tag) };
  try {
    const hubNow = now + 45_000; // include minute rounding in datetime-local
    const section = launcherSection({ store: { hubNow: () => hubNow } });
    section.draw(mixedRow());
    const deadline = formNodes(section.el).find(x => x.id === 'launch-deadline').value;
    const body = buildLaunch(mixedRow(), { ...values(), deadline }, 'launch-default-deadline', hubNow);
    const remaining = Date.parse(body.expires_at) - hubNow;
    const nativeAdmissionWindow = (2 * 60 + 10) * 60 * 1000;
    assert.ok(remaining >= nativeAdmissionWindow, 'native opaque settle + margin must fit before expiry');
    assert.ok(remaining <= 24 * 60 * 60 * 1000 && remaining > 24 * 60 * 60 * 1000 - 60_000,
      'default is 24 hours on the hub clock, rounded to the input’s minute precision');
  } finally { globalThis.document = previous; }
});

test('launcher discovery keeps aliases and native Codex route default; health and lifecycle gate launching', () => {
  const c = launcherChoices(row());
  assert.equal(c.reason, '');
  assert.deepEqual(c.projects, ['vecnet']);
  assert.deepEqual(c.modes, ['ask', 'auto']);
  assert.deepEqual(c.backends[0], { id: 'codex-app-server', health: 'ok', presets: ['default'], modes: ['ask', 'auto'], workspaces: ['worktree'] });
  for (const presence of ['unknown', 'offline', 'blocked']) {
    const r = row(); r.peer.presence = presence;
    assert.match(launcherChoices(r).reason, /Wait for a healthy presence/);
  }
  for (const modify of [r => r.disabled = true, r => r.archived_at = '2026-11-08', r => r.credential = 'none', r => r.peer.capabilities = [], r => r.peer.quota.backends['codex-app-server'] = 'degraded']) {
    const r = row(); modify(r);
    assert.ok(launcherChoices(r).reason);
    assert.throws(() => buildLaunch(r, values(), 'launch-123', now));
  }
});

function mixedRow() {
  const r = row();
  r.peer.quota.backends.t3 = 'ok';
  r.peer.capabilities.push('mode:read-only', 'mode:full-access',
    'mode:codex-app-server/read-only', 'mode:codex-app-server/ask', 'mode:codex-app-server/auto', 'mode:codex-app-server/full-access',
    'mode:t3/ask', 'mode:t3/auto', 'mode:t3/full-access');
  return r;
}

test('native and T3 permissions stay scoped at discovery and submit; global fallback is legacy only', () => {
  const r = mixedRow(), c = launcherChoices(r);
  assert.deepEqual(c.backends[0].modes, ['read-only', 'ask', 'auto', 'full-access']);
  assert.deepEqual(c.backends[1].modes, ['ask', 'auto', 'full-access']);
  assert.equal(buildLaunch(r, { ...values(), mode: 'read-only' }, 'launch-native', now).job_request.input.permission_mode, 'read-only');
  assert.throws(() => buildLaunch(r, { ...values(), backend: 't3', preset: 'sol', mode: 'read-only' }, 'launch-t3', now), /selected backend/);
  assert.equal(buildLaunch(r, { ...values(), backend: 't3', preset: 'sol' }, 'launch-t3', now).job_request.input.permission_mode, 'ask');
  r.peer.capabilities = r.peer.capabilities.filter(x => !x.startsWith('mode:t3/'));
  assert.deepEqual(launcherChoices(r).backends[1].modes, [], 'a modern peer never inherits another backend’s modes');
  r.peer.capabilities = r.peer.capabilities.filter(x => !(x.startsWith('mode:') && x.includes('/')));
  assert.deepEqual(launcherChoices(r).backends[1].modes, ['ask', 'auto', 'read-only', 'full-access'], 'legacy peers keep global fallback');
  r.peer.capabilities = r.peer.capabilities.filter(x => !['mode:ask', 'mode:auto', 'mode:read-only', 'mode:full-access'].includes(x));
  r.peer.capabilities.push('mode:t3/ask');
  assert.equal(launcherChoices(r).reason, '', 'scoped tags work without the legacy global union');
});

test('changing native to T3 refreshes rendered permissions and replaces read-only with ask', () => {
  // Minimal DOM for the real launcher form. Exercise its change listener and
  // presence refresh rather than duplicating the option selection logic.
  const previous = globalThis.document;
  globalThis.document = { createElement: tag => new FormEl(tag) };
  try {
    const section = launcherSection({ store: { hubNow: () => now } });
    const r = mixedRow();
    section.draw(r);
    const nodes = formNodes(section.el), backend = nodes.find(x => x.id === 'launch-backend'), mode = nodes.find(x => x.id === 'launch-mode');
    const options = () => mode.kids.map(x => x.value);
    assert.equal(mode.value, 'ask');
    mode.value = 'read-only';
    backend.value = 't3'; backend.on.change();
    assert.deepEqual(options(), ['ask', 'auto', 'full-access']);
    assert.equal(mode.value, 'ask');
    backend.value = 'codex-app-server'; backend.on.change();
    assert.ok(options().includes('read-only'));
    mode.value = 'read-only';
    r.peer.capabilities = r.peer.capabilities.filter(x => x !== 'mode:codex-app-server/read-only');
    section.draw(r);
    assert.ok(!options().includes('read-only'), 'presence refresh removes a withdrawn backend mode');
    assert.equal(mode.value, 'ask');
  } finally { globalThis.document = previous; }
});

test('UI launch payload uses existing message API, one daemon recipient, launch job and message deadline', () => {
  const body = buildLaunch(row(), values(), 'launch-123', now);
  assert.deepEqual(body, {
    idempotency_key: 'launch-123', recipients: [peer], kind: 'message', priority: 'wake',
    content: { text: 'Implement and verify the launcher.' }, thread_title: 'Fix launcher', expires_at: '2026-11-08T01:00:00.000Z',
    job_request: { version: 1, job_key: 'launch-123', operation: 'launch', instructions: 'Implement and verify the launcher.',
      input: { backend: 'codex-app-server', project: 'vecnet', workspace_mode: 'worktree', preset: 'default', permission_mode: 'ask', title: 'Fix launcher' } },
  });
  assert.equal('model' in body.job_request.input, false, 'existing Codex route is inherited');
  for (const patch of [{ project: '/tmp/repo' }, { project: 'other' }, { backend: 't3', preset: 'sol' }, { preset: 'unadvertised' }, { mode: 'full-access' }, { deadline: 'bad' }, { deadline: '2026-11-07' }, { prompt: '' }, { prompt: '🌍'.repeat(5000) }, { title: 'x'.repeat(201) }]) {
    assert.throws(() => buildLaunch(row(), { ...values(), ...patch }, 'launch-123', now), JSON.stringify(patch).slice(0, 100));
  }
  const changed = row(); changed.peer.capabilities = changed.peer.capabilities.filter(x => x !== 'project:vecnet');
  assert.throws(() => buildLaunch(changed, values(), 'launch-123', now), /project aliases/);
});

function workspaceRow() {
  const r = mixedRow();
  r.peer.capabilities.push('project:temporary', 'workspace:codex-app-server/worktree',
    'workspace:codex-app-server/project-root', 'workspace:codex-app-server/temp', 'workspace:t3/worktree');
  return r;
}

test('workspace choices use backend tags, keep legacy worktree, and hide the reserved temporary project', () => {
  const r = workspaceRow(), c = launcherChoices(r);
  assert.deepEqual(c.projects, ['vecnet']);
  assert.equal(c.temporary, true);
  assert.deepEqual(c.backends[0].workspaces, ['worktree', 'project-root', 'temp']);
  assert.deepEqual(c.backends[1].workspaces, ['worktree']);
  assert.deepEqual(launcherChoices(row()).backends[0].workspaces, ['worktree']);
  r.peer.capabilities = r.peer.capabilities.filter(x => x !== 'project:temporary');
  assert.deepEqual(launcherChoices(r).backends[0].workspaces, ['worktree', 'project-root']);
  r.peer.capabilities = r.peer.capabilities.filter(x => !x.startsWith('workspace:t3/'));
  assert.deepEqual(launcherChoices(r).backends[1].workspaces, [], 'modern peers do not inherit another backend’s workspace modes');
});

test('workspace payload contains only an advertised alias and mode; temp routes to reserved configured alias', () => {
  const r = workspaceRow();
  for (const workspace of ['worktree', 'project-root', 'temp']) {
    const input = buildLaunch(r, { ...values(), workspace, ...(workspace === 'temp' ? { project: '' } : {}),
      path: '/untrusted/path', repo: '/untrusted/repo' }, 'launch-workspace', now).job_request.input;
    assert.equal(input.workspace_mode, workspace);
    assert.equal(input.project, workspace === 'temp' ? 'temporary' : 'vecnet');
    assert.deepEqual(Object.keys(input).sort(), ['backend', 'permission_mode', 'preset', 'project', 'title', 'workspace_mode']);
  }
  for (const patch of [{ workspace: 'project-root', backend: 't3', preset: 'sol' }, { workspace: 'temp', backend: 't3', preset: 'sol' },
    { workspace: '/tmp/custom' }, { workspace: 'worktree', project: '' }, { workspace: 'project-root', project: 'temporary' }, { project: '/tmp/custom' }]) {
    assert.throws(() => buildLaunch(r, { ...values(), ...patch }, 'launch-invalid', now));
  }
  r.peer.capabilities = r.peer.capabilities.filter(x => x !== 'project:temporary');
  assert.throws(() => buildLaunch(r, { ...values(), workspace: 'temp', project: '' }, 'launch-temp', now), /workspace mode/);
  const tempOnly = workspaceRow();
  tempOnly.peer.capabilities = tempOnly.peer.capabilities.filter(x => x !== 'project:vecnet');
  assert.equal(launcherChoices(tempOnly).reason, '');
  assert.deepEqual(launcherChoices(tempOnly).backends[0].workspaces, ['temp']);
});

test('rendered workspace controls default to worktree, map No project to temp, and refresh on backend changes', () => {
  const previous = globalThis.document;
  globalThis.document = { createElement: tag => new FormEl(tag) };
  try {
    const section = launcherSection({ store: { hubNow: () => now } });
    section.draw(workspaceRow());
    const nodes = formNodes(section.el), field = id => nodes.find(x => x.id === id);
    const project = field('launch-project'), workspace = field('launch-workspace'), backend = field('launch-backend');
    assert.equal(workspace.value, 'worktree');
    assert.equal(project.value, 'vecnet');
    assert.ok(!project.kids.some(x => x.value === 'temporary'));
    assert.ok(project.kids.some(x => x.value === '' && x.textContent === 'No project'));
    project.value = ''; project.on.change();
    assert.equal(workspace.value, 'temp');
    assert.equal(project.disabled, true);
    workspace.value = 'project-root'; workspace.on.change();
    assert.equal(project.disabled, false);
    assert.equal(project.value, 'vecnet');
    backend.value = 't3'; backend.on.change();
    assert.deepEqual(workspace.kids.map(x => x.value), ['worktree']);
    assert.equal(workspace.value, 'worktree');
    assert.ok(!project.kids.some(x => x.value === ''), 'No project requires backend temp support');
    const tempOnly = workspaceRow();
    tempOnly.peer.capabilities = tempOnly.peer.capabilities.filter(x => x !== 'project:vecnet');
    section.draw(tempOnly);
    assert.equal(workspace.value, 'temp');
    assert.equal(project.value, '');
    assert.equal(project.disabled, true);
  } finally { globalThis.document = previous; }
});

test('launcher installation commands quote host paths and use the confirmed setup/install split', () => {
  const output = inviteBlock({ owner: 'Max', expires: 'tomorrow', token: 'fake-token', profile: 'launch-host', hubURL: 'https://hub.example' });
  const setup = { project: 'vecnet', repo: "/tmp/it's a repo $(false)", baseRef: 'master', source: '/tmp/vecnet checkout' };
  assert.equal(launcherSetupIssue(setup), '');
  const commands = launcherSetupCommands(output, setup);
  const lines = commands.split('\n');
  assert.equal(lines.length, 3);
  assert.match(lines[1], /launcher setup --project 'vecnet'/);
  assert.match(lines[2], /launcher install --source '\/tmp\/vecnet checkout' --write --enable --start$/);
  assert.doesNotMatch(commands, /guide|setup .*--start/);
  // A shell stub returns the arguments without executing any generated tool.
  const args = JSON.parse(execFileSync('bash', ['-c', "vecnet() { python3 -c 'import json,sys; print(json.dumps(sys.argv[1:]))' \"$@\"; }; " + lines[1]], { encoding: 'utf8' }));
  assert.deepEqual(args, ['--net', 'launch-host', 'launcher', 'setup', '--project', 'vecnet', '--repo', setup.repo, '--base-ref', 'master', '--write']);
  for (const patch of [{ repo: 'relative' }, { repo: '/tmp/a\nnext' }, { project: '../../tmp' }, { source: '' }, { baseRef: '' }]) {
    assert.ok(launcherSetupIssue({ ...setup, ...patch }));
  }
  assert.throws(() => launcherSetupCommands('missing', setup), /enrolment/);
});

test('host setup inputs never become onboarding API fields', () => {
  const spec = { kind: 'launcher', spec_version: 1, submit_action: 'invite', fields: [{ name: 'name', type: 'string', required: true }] };
  const body = submitBody(spec, { name: 'launcher', launcher_setup: { repo: '/host/private', source: '/host/checkout' } }, 'request-1');
  assert.deepEqual(body.inputs, { name: 'launcher' });
});
