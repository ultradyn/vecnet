// Protocol fixture only. Never claims to be a real model or runtime canary.
import { createInterface } from 'node:readline';
import { isAbsolute } from 'node:path';
import { appendFileSync } from 'node:fs';
const scenario = process.argv[2] ?? 'normal';
const log = process.argv[3];
const sessionId = 'fake-acp-session';
let initialized = false, session = false, modeSet = false, active, permissionId = 1000, prompts = 0;
const permissions = new Map();
const clientChecks = new Map();
let reuseRound = 0;
let budgetCount = 0, budgetPrompt;
function budgetRequest() {
  send({ id: 'budget-' + (++budgetCount), method: 'terminal/create', params: { sessionId, command: 'never-execute' } });
}
function reusedPermission(prompt) {
  const second = reuseRound++ > 0;
  permissions.set(1001, prompt);
  send({ id: 1001, method: 'session/request_permission', params: { sessionId,
    toolCall: { toolCallId: second ? 'second-operation' : 'first-operation', kind: second && scenario === 'reuse-privileged' ? 'edit' : 'execute',
      title: second ? 'Second operation' : 'Ordinary operation', rawInput: { command: second && scenario === 'reuse-privileged' ? 'sudo touch /outside/file' : 'git status', cwd: process.cwd() },
      ...(second && scenario === 'reuse-privileged' ? { locations: [{ path: '/outside/file' }] } : {}) },
    options: [{ optionId: 'yes', name: second && scenario === 'reuse-scope' ? 'Allow always' : 'Allow once', kind: second && scenario === 'reuse-scope' ? 'allow_always' : 'allow_once' },
      { optionId: 'no', name: 'Reject', kind: 'reject_once' }] } });
}
const send = value => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...value }) + '\n');
const result = (id, value) => send({ id, result: value });
const error = (id, code = -32600) => send({ id, error: { code, message: 'fixture rejection' } });
const update = update => send({ method: 'session/update', params: { sessionId, update } });
function finish(id, reason = 'end_turn') { if (active === id) active = undefined; result(id, { stopReason: reason }); }
function reply(id) {
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'private thought' } });
  update({ sessionUpdate: 'agent_message_chunk', messageId: 'commentary-' + id, content: { type: 'text', text: 'Working' } });
  update({ sessionUpdate: 'tool_call', toolCallId: 'tool-' + id, kind: 'read', title: 'Read file', status: 'completed' });
  // Split a multi-byte UTF-8 character across stdout writes.
  const frame = Buffer.from(JSON.stringify({ jsonrpc: '2.0', method: 'session/update', params: { sessionId,
    update: { sessionUpdate: 'agent_message_chunk', messageId: 'final-' + id, content: { type: 'text', text: 'Hi 🌊' } } } }) + '\n');
  const split = frame.indexOf(Buffer.from('🌊')) + 1;
  process.stdout.write(frame.subarray(0, split)); process.stdout.write(frame.subarray(split));
  update({ sessionUpdate: 'agent_message_chunk', messageId: 'final-' + id, content: { type: 'text', text: ' there' } });
  const reason = scenario.startsWith('stop:') ? scenario.slice(5) : 'end_turn';
  finish(id, reason);
}
function permission(id) {
  const request = ++permissionId; permissions.set(request, id);
  send({ id: request, method: 'session/request_permission', params: { sessionId,
    toolCall: { toolCallId: 'edit-1', title: 'Change a file', name: 'write_file', kind: 'edit', locations: [{ path: '/outside/file' }], rawInput: { command: 'touch /outside/file' } },
    options: scenario === 'unknown-option' ? [{ optionId: 'future', name: 'Future', kind: 'future_kind' }] :
      [{ optionId: 'yes', name: 'Allow once', kind: 'allow_once' }, { optionId: 'no', name: 'Reject', kind: 'reject_once' }] } });
}
const lines = createInterface({ input: process.stdin });
lines.on('line', line => {
  const m = JSON.parse(line);
  if (log) appendFileSync(log, JSON.stringify(m) + '\n');
  if (!m.method) {
    if (scenario === 'request-budget' && typeof m.id === 'string' && m.id.startsWith('budget-')) {
      if (budgetCount < 4097) budgetRequest(); else finish(budgetPrompt);
      return;
    }
    if (clientChecks.has(m.id)) { const prompt = clientChecks.get(m.id); clientChecks.delete(m.id); reply(prompt); return; }
    const prompt = permissions.get(m.id);
    if (prompt !== undefined) {
      permissions.delete(m.id);
      if (scenario.startsWith('reuse-') && reuseRound === 1) {
        // Schedule after the first response has completed, so only tracking
        // pending requests cannot accidentally make this regression pass.
        setTimeout(() => reusedPermission(prompt), 10);
      } else if (m.result?.outcome?.outcome === 'selected' && m.result.outcome.optionId === 'yes') reply(prompt);
      else finish(prompt, 'cancelled');
    }
    return;
  }
  if (m.method === 'initialize') {
    if (scenario === 'init-hold') return;
    initialized = true;
    result(m.id, { protocolVersion: scenario === 'v2' ? 2 : 1, agentCapabilities: {}, agentInfo: { name: 'fake-acp', version: 'fixture' }, authMethods: [] });
  } else if (m.method === 'session/new') {
    if (!initialized || !isAbsolute(m.params.cwd) || !Array.isArray(m.params.mcpServers)) { error(m.id); return; }
    if (scenario === 'auth') { error(m.id, -32000); return; }
    if (scenario === 'new-exit') { process.exit(23); }
    session = true;
    result(m.id, { sessionId, modes: { currentModeId: 'ask', availableModes: [{ id: scenario === 'no-mode' ? 'bypassPermissions' : 'ask', name: 'Ask' }] } });
  } else if (m.method === 'session/set_mode') {
    if (!session || m.params.modeId !== 'ask' || m.params.sessionId !== sessionId) { error(m.id); return; }
    modeSet = true; result(m.id, {});
  } else if (m.method === 'session/prompt') {
    if (!modeSet || active !== undefined || m.params.sessionId !== sessionId || m.params.prompt[0].type !== 'text') { error(m.id); return; }
    active = m.id; prompts++;
    if (scenario === 'bad-stdout') { process.stdout.write('not JSON\n'); return; }
    if (scenario === 'exit') { process.exit(24); }
    if (scenario === 'foreign') {
      send({ method: 'session/update', params: { sessionId: 'foreign', update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'foreign-text' } } } });
      const request = ++permissionId; clientChecks.set(request, m.id);
      send({ id: request, method: 'session/request_permission', params: { sessionId: 'foreign' } });
      return;
    }
    if (scenario === 'mode-change') { update({ sessionUpdate: 'current_mode_update', currentModeId: 'bypassPermissions' }); return; }
    if (scenario === 'unsupported-client') {
      const request = ++permissionId; clientChecks.set(request, m.id);
      send({ id: request, method: 'terminal/create', params: { sessionId, command: 'false' } }); return;
    }
    if (scenario === 'request-budget') { budgetPrompt = m.id; budgetRequest(); return; }
    if (scenario === 'env-oracle') {
      const keys = ['HOME', 'PATH', 'LANG', 'TMPDIR', 'SAFE_CONFIG', 'VECNET_CREDENTIAL', 'VECNET_URL', 'OPENAI_API_KEY', 'ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_BASE_URL', 'OTHER_SECRET', 'lower_password'];
      update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: JSON.stringify(Object.fromEntries(keys.map(k => [k, process.env[k] ?? null]))) } });
      finish(m.id); return;
    }
    if (scenario.startsWith('interleave-')) {
      const ids = scenario.includes('same-id') ? { messageId: 'one-message' } : {};
      update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'private thought' } });
      update({ sessionUpdate: 'agent_message_chunk', ...ids, content: { type: 'text', text: 'First half ' } });
      update({ sessionUpdate: scenario.includes('plan') ? 'plan' : 'tool_call_update', toolCallId: 'late-status', status: 'completed', entries: [] });
      update({ sessionUpdate: 'agent_message_chunk', ...ids, content: { type: 'text', text: 'second half' } });
      finish(m.id); return;
    }
    if (scenario.startsWith('reuse-')) { reusedPermission(m.id); return; }
    if (scenario === 'hold' || scenario === 'hold-after-first' && prompts > 1) return;
    if (scenario === 'permission' || scenario === 'unknown-option') { permission(m.id); return; }
    reply(m.id);
    if (scenario === 'mode-change-idle') setTimeout(() => update({ sessionUpdate: 'current_mode_update', currentModeId: 'bypassPermissions' }), 5);
  } else if (m.method === 'session/cancel') {
    if (m.id !== undefined) { error(m.id); return; }
    if (active !== undefined) {
      update({ sessionUpdate: 'tool_call_update', toolCallId: 'tool-cancel', status: 'failed' });
      if (scenario !== 'permission' && scenario !== 'unknown-option') finish(active, 'cancelled');
    }
  } else { error(m.id, -32601); }
});
lines.on('close', () => process.exit(0));
