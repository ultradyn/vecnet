import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  BEARER_CONFIRM, BEARER_WARN, CODE_STEP, CONNECTOR_PATTERN, DOT_NOTE, DOT_STEPS, GATEWAY_NOTE, QUOTA_NOTE,
  SKIP_MCP_HELP, SKIP_MCP_LABEL, SUPPORTED_ACTIONS, TTLS, acceptCompletion, addAgentPrefill, applyLoad,
  authorizePrompt, beginLoad, clearedValues, clientIssues, connectedBadge, connectorDefault, connectorOverrideError,
  dotLink, emptyRegistry, failLoad, formSnapshot, freshRequestId, humanOutput, invite, inviteBlock, keepRequestId, kindsFrom,
  linkPanel, mustConfirmBearerRevoke, offersStdio, outcomeOf, presentError, redactText,
  slug, specIssue, statusSafe, submitBody, submitGate, usableRequestId,
} from '../static/invite-model.js';
import { KIND_LABELS, kindLabel } from '../static/format.js';

// The golden the Go CLI is tested against too (internal/cli/invite_internal_test.go).
const fx = JSON.parse(readFileSync(new URL('../../cli/testdata/invite.json', import.meta.url), 'utf8'));
const asInput = c => ({ owner: c.owner, hubURL: c.hub_url, profile: c.profile, token: c.token, expires: c.expires_at, open: c.open, name: c.name });

test('slug and inviteBlock match the CLI golden exactly', () => {
  for (const c of fx.slugs) assert.equal(slug(c.name), c.profile, c.name);
  assert.ok(fx.blocks.some(c => c.open) && fx.blocks.some(c => !c.open));
  for (const c of fx.blocks) {
    const got = inviteBlock(asInput(c));
    assert.equal(got, c.block, c.profile);
    // Short, and one plain pipeline: shell guards refuse $, ; and friends.
    assert.ok(got.split('\n').length <= 6, c.profile);
    assert.ok(!/[$;<>&]/.test(got), c.profile);
  }
});

test('invite renders the panel from the hub answer, preferring its public URL', () => {
  const c = fx.blocks.find(x => !x.open && x.owner);
  const res = { peer_id: 'peer_1', name: 'Scout', name_open: false, enrol_token: c.token, enrol_expires_at: c.expires_at, hub_url: c.hub_url, hub_owner: c.owner };
  const got = invite(res, 'http://127.0.0.1:9', false);
  assert.equal(got.text, c.block);
  assert.equal(got.profile, 'scout');
  assert.equal(got.peerId, 'peer_1');
  assert.ok(invite({ ...res, hub_url: '' }, 'http://h:1', false).text.includes('--hub http://h:1 '));
  const o = fx.blocks.find(x => x.open && x.name);
  const open = invite({ ...res, name: o.name, name_open: true, enrol_token: o.token, enrol_expires_at: o.expires_at, hub_url: o.hub_url, hub_owner: o.owner }, 'http://x', true);
  assert.equal(open.text, o.block);
  assert.ok(open.open);
});

const SPEC = '2026-11-06';
const ROUTINE = 'https://api2.cursor.sh/automations/webhook/01234567-89ab-cdef-0123-456789abcdef';
const FAKE_KEY = 'crsr_fakekeyvalue';

const field = (name, extra) => ({ name, type: 'string', required: false, ...extra });
const spec = (kind, action, extra) => ({
  kind, label: extra && extra.label || kind, spec_version: (extra && extra.spec_version) || SPEC,
  prompt_template: (extra && extra.prompt_template) || '', submit_action: action, fields: (extra && extra.fields) || [],
});

const catalogue = [
  spec('session', 'invite', { label: 'Agent session (Claude Code, Codex, CLI)', fields: [field('name'), field('name_open', { type: 'boolean', default: false }), field('enrol_ttl_seconds', { type: 'integer', default: 86400, minimum: 60, maximum: 604800 }), field('principal_id')] }),
  spec('launcher', 'invite', { label: 'Launcher' }),
  spec('operator', 'invite', { label: 'Operator' }),
  spec('openclaw', 'invite', { label: 'OpenClaw' }),
  spec('source', 'invite', { label: 'Webhook source' }),
  spec('pagchat-bot', 'invite', { label: 'pagchat bot' }),
  spec('grokbot', 'grokbot_setup', {
    label: 'Grok Bot',
    prompt_template: 'Hub origin: {{.HubURL}}\nThis bot\'s connector name is {{.ConnectorName}}.\nUse a few gateway bots for a user or a team.\n',
    fields: [
      field('name', { required: true }),
      field('webhook_url', { required: true, human_only: true, pattern: '^https://api2\\.cursor\\.sh/automations/webhook/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' }),
      field('webhook_key', { type: 'secret', required: true, human_only: true }),
      field('connector_name', { pattern: '^vecnet-[a-z0-9-]{1,48}$' }),
      field('skip_mcp', { type: 'boolean', default: false }),
    ],
  }),
  spec('dot', 'oauth_link', { label: 'ChatGPT dot', fields: [field('name', { required: true }), field('principal_id')] }),
];

test('kinds come from the registry, never operator or source, and TTLs stay within 7 days', () => {
  const src = readFileSync(new URL('../static/invite-model.js', import.meta.url), 'utf8');
  assert.doesNotMatch(src, /session',\s*'launcher',\s*'openclaw'/);
  assert.deepEqual(kindsFrom(undefined), []);
  assert.deepEqual(kindsFrom([]), []);
  assert.deepEqual(kindsFrom(catalogue), ['session', 'launcher', 'openclaw', 'pagchat-bot', 'grokbot', 'dot']);
  assert.ok(TTLS.every(([, s]) => s >= 60 && s <= 604800));
});

test('registry labels stay the human names; the raw kind is what the API receives', () => {
  const kinds = kindsFrom(catalogue);
  assert.deepEqual(kinds.map(k => catalogue.find(s => s.kind === k).label), ['Agent session (Claude Code, Codex, CLI)', 'Launcher', 'OpenClaw', 'pagchat bot', 'Grok Bot', 'ChatGPT dot']);
  for (const k of kinds) assert.equal(catalogue.find(s => s.kind === k).label, kindLabel(k));
  for (const k of kinds) assert.ok(Object.hasOwn(KIND_LABELS, k), k);
});

test('loading failure and version refresh never invent a kind catalogue', () => {
  let state = emptyRegistry();
  assert.equal(state.phase, 'idle');
  assert.deepEqual(state.specs, []);
  const loading = beginLoad(state);
  const failed = failLoad(loading, loading.generation, 'hub unreachable');
  assert.equal(failed.phase, 'error');
  assert.deepEqual(failed.specs, []);
  assert.match(failed.error, /hub unreachable/);

  let ready = applyLoad(loading, loading.generation, catalogue);
  assert.equal(ready.phase, 'ready');
  assert.equal(kindsFrom(ready.specs)[0], 'session');
  const refresh = beginLoad(ready);
  assert.equal(refresh.phase, 'loading');
  assert.equal(refresh.specs[0].kind, 'session');
  const ignored = applyLoad(refresh, loading.generation, [spec('session', 'invite', { spec_version: 'stale-should-drop' })]);
  assert.equal(ignored.generation, refresh.generation);
  assert.notEqual(ignored.specs[0].spec_version, 'stale-should-drop');
  const next = catalogue.map(s => ({ ...s, spec_version: '2026-11-07' }));
  ready = applyLoad(refresh, refresh.generation, next);
  assert.equal(ready.specs[0].spec_version, '2026-11-07');
  const kept = failLoad(beginLoad(ready), ready.generation + 1, 'refresh failed');
  assert.equal(kept.phase, 'ready');
  assert.equal(kept.specs[0].spec_version, '2026-11-07');
  assert.match(kept.error, /refresh failed/);
});

test('unsupported field types and submit actions are refused', () => {
  assert.deepEqual(SUPPORTED_ACTIONS, ['invite', 'oauth_link', 'grokbot_setup']);
  assert.equal(specIssue(catalogue.find(s => s.kind === 'session')), null);
  assert.deepEqual(specIssue(spec('future', 'shell', { fields: [field('name')] })), { code: 'unsupported_action', action: 'shell' });
  assert.deepEqual(specIssue(spec('future', 'invite', { fields: [field('blob', { type: 'file' })] })), { code: 'unsupported_field', name: 'blob', type: 'file' });
  assert.throws(() => submitBody(spec('future', 'shell', { fields: [] }), {}, 'req_1'), /unsupported_action/);
});

test('the fragment route prefills kind, name and request id and does not submit', () => {
  assert.deepEqual(addAgentPrefill('#add-agent?kind=grokbot&name=Helper%20Bot&request_id=req-deep-1'), {
    kind: 'grokbot', name: 'Helper Bot', requestId: 'req-deep-1',
  });
  assert.deepEqual(addAgentPrefill('#add-agent'), { kind: '', name: '', requestId: '' });
  assert.equal(addAgentPrefill('#thread/thr_1'), null);
  const bad = addAgentPrefill('#add-agent?kind=grokbot&request_id=bad%20id');
  assert.equal(bad.kind, 'grokbot');
  assert.equal(usableRequestId(bad.requestId), '');
  assert.equal(usableRequestId('req-deep-1'), 'req-deep-1');
  assert.equal(typeof addAgentPrefill('#add-agent?kind=grokbot').submit, 'undefined');
});

test('dotLink points ChatGPT at the hub public /mcp, else the page origin', () => {
  const res = { peer_id: 'peer_dot', name: 'scout', enrol_token: '', enrol_expires_at: '', hub_url: 'https://vecnet.example', hub_owner: 'Max' };
  assert.deepEqual(dotLink(res, 'http://127.0.0.1:9'), { peerId: 'peer_dot', name: 'scout', mcpURL: 'https://vecnet.example/mcp' });
  assert.equal(dotLink({ ...res, hub_url: 'https://h.example//' }, 'x').mcpURL, 'https://h.example/mcp');
  assert.equal(dotLink({ ...res, hub_url: '' }, 'http://h:1').mcpURL, 'http://h:1/mcp');
});

test('the dot steps say where to click, how to link, and how to follow events', () => {
  const text = DOT_STEPS.map(parts => parts.map(p => typeof p === 'string' ? p : p.b).join(''));
  assert.equal(text.length, 4);
  assert.match(text[0], /Plugins.*\+.*Add custom MCP server/);
  assert.match(text[1], /OAuth.*plugin/);
  assert.match(text[CODE_STEP], /link code/);
  assert.match(text[3], /trigger vecnet, event vecnet\.message and no settings/);
  assert.match(text[3], /plugin page.*click into its card.*Rescan/);
  assert.match(text[3], /Advanced.*Runs in: Same chat for every run/);
  assert.match(text[3], /vecnet_guide.*recommended task instruction/);
  assert.match(text[3], /One trigger block.*whole inbox.*ORed/);
  assert.match(DOT_NOTE, /new chat or rescan/);
});

test('grok onboarding identity and skip policy', () => {
  const grok = catalogue.find(s => s.kind === 'grokbot');
  const prompt = authorizePrompt(grok.prompt_template, 'https://hub.example', connectorDefault('Helper Bot'));
  assert.match(prompt, /Hub origin: https:\/\/hub\.example/);
  assert.match(prompt, /vecnet-helper-bot/);
  assert.match(prompt, /few gateway bots/i);
  assert.match(GATEWAY_NOTE, /few gateway bots/i);
  assert.match(GATEWAY_NOTE, /vecnet-<slug>/);
  assert.match(QUOTA_NOTE, /spends quota/i);
  assert.match(QUOTA_NOTE, /wake_idle/);
  assert.match(QUOTA_NOTE, /not a price quote/i);
  assert.equal(offersStdio(grok), false);
  assert.equal(SKIP_MCP_LABEL, 'Skip MCP');
  assert.match(SKIP_MCP_HELP, /CLI bearer/);
  assert.match(SKIP_MCP_HELP, /Hosted OAuth/);
  assert.doesNotMatch(SKIP_MCP_HELP + GATEWAY_NOTE + QUOTA_NOTE + prompt, /stdio|mcp-remote/i);

  const missing = clientIssues(grok, { name: '  ', webhook_url: '', webhook_key: '   ', connector_name: '', skip_mcp: false });
  assert.ok(missing.name && missing.webhook_url && missing.webhook_key);
  assert.equal(JSON.stringify(missing).includes(FAKE_KEY), false);

  const badConnector = clientIssues(grok, { name: 'Helper Bot', webhook_url: ROUTINE, webhook_key: FAKE_KEY, connector_name: 'nope', skip_mcp: false });
  assert.match(badConnector.connector_name, /vecnet-\[a-z0-9-\]\{1,48\}/);
  assert.equal(connectorOverrideError('vecnet-custom-1'), '');
  assert.equal(connectorOverrideError(''), '');
  assert.ok(connectorOverrideError('vecnet-'));
  assert.ok(CONNECTOR_PATTERN.test('vecnet-custom-1'));

  const raw = submitBody(grok, { name: 'Helper Bot', webhook_url: '  ' + ROUTINE + ' ', webhook_key: FAKE_KEY, connector_name: '', skip_mcp: false }, 'req_raw');
  assert.equal(raw.kind, 'grokbot');
  assert.equal(raw.spec_version, SPEC);
  assert.equal(raw.request_id, 'req_raw');
  assert.equal(raw.inputs.webhook_key, FAKE_KEY);
  assert.equal(raw.inputs.webhook_url, ROUTINE);
  assert.equal(raw.inputs.skip_mcp, false);
  assert.equal(raw.inputs.connector_name, undefined);
  assert.equal(raw.submit_action, undefined);

  const header = submitBody(grok, { name: 'Helper Bot', webhook_url: ROUTINE, webhook_key: 'Bearer ' + FAKE_KEY, connector_name: 'vecnet-custom-1', skip_mcp: true }, 'req_hdr');
  assert.equal(header.inputs.webhook_key, 'Bearer ' + FAKE_KEY);
  assert.equal(header.inputs.connector_name, 'vecnet-custom-1');
  assert.equal(header.inputs.skip_mcp, true);

  const echoed = presentError({ code: 'invalid_value', message: 'bad ' + FAKE_KEY + ' at ' + ROUTINE, details: { field: 'webhook_key' } }, [FAKE_KEY, ROUTINE]);
  assert.equal(echoed.field, 'webhook_key');
  assert.match(echoed.text, /invalid_value/);
  assert.equal(echoed.text.includes(FAKE_KEY), false);
  assert.equal(echoed.text.includes(ROUTINE), false);

  assert.equal(mustConfirmBearerRevoke('active'), true);
  assert.equal(mustConfirmBearerRevoke('none'), false);
  assert.equal(mustConfirmBearerRevoke('revoked'), false);
  assert.equal(mustConfirmBearerRevoke(''), false);
  assert.equal(mustConfirmBearerRevoke.length, 1);
  assert.match(BEARER_WARN, /revok/i);
  assert.match(BEARER_WARN, /no keep-bearer option/i);
  assert.equal(BEARER_CONFIRM, 'Revoke bearer and link');

  assert.equal(connectedBadge({ auth_state: 'verified', setup: { state: 'queued' } }), false);
  assert.equal(connectedBadge({ auth_state: 'verified', setup: { state: 'accepted' } }), false);
  assert.equal(connectedBadge({ auth_state: 'unlinked', setup: { state: 'processed' } }), false);
  assert.equal(connectedBadge({ auth_state: 'verified', setup: { state: 'processed' } }), false);

  const session = catalogue.find(s => s.kind === 'session');
  const legacy = submitBody(session, { name: 'web-repo Claude', name_open: false, enrol_ttl_seconds: '3600', principal_id: '' }, 'req_legacy');
  assert.equal(legacy.inputs.name, 'web-repo Claude');
  assert.equal(legacy.inputs.enrol_ttl_seconds, 3600);
  assert.equal(legacy.inputs.name_open, undefined);
  const opened = submitBody(session, { name: 'helper bot', name_open: true, enrol_ttl_seconds: 86400 }, 'req_open');
  assert.equal(opened.inputs.name_open, true);
  const dot = submitBody(catalogue.find(s => s.kind === 'dot'), { name: 'scout' }, 'req_dot');
  assert.equal(dot.kind, 'dot');
  assert.equal(dot.inputs.enrol_ttl_seconds, undefined);
  assert.equal(dot.inputs.name_open, undefined);
  const panel = linkPanel({ peer_id: 'peer_dot', name: 'scout', human_output: 'ABCD-EFGH-JKMN' }, 'http://127.0.0.1:9', 'https://vecnet.example');
  assert.equal(panel.mcpURL, 'https://vecnet.example/mcp');
  assert.equal(panel.code, 'ABCD-EFGH-JKMN');
  assert.equal(humanOutput({ human_output: 'ABCD-EFGH-JKMN' }), 'ABCD-EFGH-JKMN');
});

test('key clearing, no persistence, stable request id, no double-submit, stale completion', () => {
  const secret = FAKE_KEY;
  const values = { name: 'Helper Bot', webhook_url: ROUTINE, webhook_key: secret };
  const cleared = clearedValues(values, ['webhook_key']);
  assert.equal(cleared.webhook_key, '');
  assert.equal(cleared.name, 'Helper Bot');
  const snap = formSnapshot(values, ['webhook_key', 'webhook_url']);
  const stored = JSON.stringify({ local: snap, url: '#add-agent?kind=grokbot&name=Helper%20Bot', clipboard: '', console: '' });
  assert.equal(stored.includes(secret), false);
  assert.equal(stored.includes(ROUTINE), false);

  const leaked = 'invalid_value: Bearer ' + secret;
  assert.equal(redactText(leaked, [secret]).includes(secret), false);
  assert.doesNotMatch(redactText(leaked, []), /Bearer\s+crsr_/);

  const id = freshRequestId(bytes => bytes.fill(0xab));
  assert.equal(id, 'req_' + 'ab'.repeat(16));
  assert.equal(usableRequestId(id), id);
  assert.equal(outcomeOf({ code: 'network', status: 0 }), 'uncertain');
  assert.equal(outcomeOf({ code: 'http_503', status: 503 }), 'uncertain');
  assert.equal(outcomeOf({ code: 'aborted', status: 0 }), 'uncertain');
  assert.equal(outcomeOf({ code: 'stale_spec', status: 409 }), 'stale');
  assert.equal(outcomeOf({ code: 'idempotency_conflict', status: 409 }), 'conflict');
  assert.equal(outcomeOf({ code: 'invalid_value', status: 422 }), 'invalid');
  for (const outcome of ['uncertain', 'stale', 'invalid', 'conflict']) assert.equal(keepRequestId(outcome), true);
  assert.equal(keepRequestId('ok'), false);

  let gate = submitGate({ busy: false });
  assert.equal(gate.accepted, true);
  gate = submitGate(gate);
  assert.equal(gate.accepted, false);
  assert.equal(gate.busy, true);

  assert.equal(acceptCompletion(2, 2, false), true);
  assert.equal(acceptCompletion(2, 1, false), false);
  assert.equal(acceptCompletion(2, 2, true), false);

  const safe = statusSafe({ request_id: 'req_1', peer_id: 'peer_1', auth_state: 'unlinked', setup: { state: 'queued' }, human_output: 'vn_enrol_secret', webhook_key: secret });
  assert.equal(JSON.stringify(safe).includes('vn_enrol'), false);
  assert.equal(JSON.stringify(safe).includes(secret), false);
  assert.equal(safe.peer_id, 'peer_1');
  assert.equal(safe.setup.state, 'queued');
  assert.equal(humanOutput(safe), '');
});
