import test, { beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { buildTurnStart, buildTurnSteer, buildThreadResume, buildThreadUnsubscribe,
  buildTurnsPage, mapStatus, parseResumePolicy, CODEX_OPT_OUT_NOTIFICATIONS } from '../src/codex/protocol.ts';

beforeEach(async t => {
  if (!('after' in t)) throw new Error('expected a test context');
  const dir = await mkdtemp(join(tmpdir(), 'vn-protocol-'));
  const names = ['VECNET_CONFIG', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'CODEX_HOME', 'VECNET_CODEX_BINDINGS'];
  const saved = names.map(n => process.env[n]);
  names.forEach((n, i) => { process.env[n] = join(dir, String(i)); });
  t.after(async () => {
    names.forEach((n, i) => { if (saved[i] === undefined) delete process.env[n]; else process.env[n] = saved[i]; });
    await rm(dir, { recursive: true, force: true });
  });
});
const args = { threadId: 'thread', stub: 'vecnet: read your inbox', key: 'vn.test',
  entries: [{ key: 'delivery', value: '<vecnet-inbox untrusted="true">peer text</vecnet-inbox>' }] };
const forbidden = ['sandboxPolicy', 'approvalPolicy', 'approvalsReviewer', 'permissions', 'model', 'serviceTier',
  'serviceTierForTurn', 'effort', 'summary', 'personality', 'cwd', 'environments', 'runtimeWorkspaceRoots',
  'collaborationMode', 'outputSchema', 'disabledPluginIds', 'toolOutput'];
const schema = async () => JSON.parse(await readFile(new URL('./fixtures/codex-0.166.0-schema-subset.json', import.meta.url), 'utf8')).definitions;

test('buildTurnStart emits only allowlisted keys', () => {
  // Extra keys at the JS boundary must never leak through a spread.
  const contaminated = { ...args, ...Object.fromEntries(forbidden.map(k => [k, 'forbidden'])) };
  const start = buildTurnStart(contaminated), steer = buildTurnSteer({ ...contaminated, expectedTurnId: 'turn' });
  const expected = { threadId: 'thread', clientUserMessageId: 'vn.test', input: [{ type: 'text', text: args.stub }],
    additionalContext: { delivery: { value: args.entries[0].value, kind: 'untrusted', name: 'vecnet_inbox' } } };
  assert.deepEqual(start, expected);
  assert.deepEqual(steer, { ...expected, expectedTurnId: 'turn' });
  for (const p of [start, steer]) for (const key of forbidden) assert.equal(Object.hasOwn(p, key), false, key);
  assert.deepEqual(buildTurnStart({ ...args, entries: [] }), { threadId: 'thread', clientUserMessageId: 'vn.test', input: expected.input });
  const special = buildTurnStart({ ...args, entries: [{ key: '__proto__', value: 'inert' }] });
  assert.deepEqual(Object.keys(special.additionalContext as object), ['__proto__']);
  assert.equal(Object.hasOwn(special.additionalContext as object, '__proto__'), true);
});

test('payload fields exist in the 0.166.0 schema subset', async () => {
  const d = await schema();
  assert.deepEqual(Object.keys(d).sort(), ['TurnStartParams', 'TurnSteerParams', 'AdditionalContextEntry', 'UserInput',
    'ThreadStatus', 'ThreadActiveFlag', 'ThreadResumeParams', 'ThreadResumeResponse', 'ThreadUnsubscribeParams',
    'ThreadTurnsListParams', 'ThreadTurnsListResponse'].sort());
  for (const [name, payload] of [['TurnStartParams', buildTurnStart(args)], ['TurnSteerParams', buildTurnSteer({ ...args, expectedTurnId: 'turn' })]] as const) {
    for (const key of Object.keys(payload)) assert.ok(Object.hasOwn(d[name].properties, key), `${name}.${key}`);
    for (const key of d[name].required) assert.ok(Object.hasOwn(payload, key));
    const input = (payload.input as Record<string, unknown>[])[0];
    const text = d.UserInput.oneOf.find((v: any) => v.properties?.type?.enum?.includes('text'));
    for (const key of Object.keys(input)) assert.ok(Object.hasOwn(text.properties, key));
    for (const entry of Object.values(payload.additionalContext as Record<string, object>)) {
      for (const key of Object.keys(entry)) assert.ok(Object.hasOwn(d.AdditionalContextEntry.properties, key));
    }
  }
  assert.deepEqual(d.ThreadActiveFlag.enum, ['waitingOnApproval', 'waitingOnUserInput']);
  assert.ok(Object.hasOwn(d.ThreadTurnsListResponse.properties, 'nextCursor'));
});

test('mapStatus covers idle, active, active+waitingOnApproval, active+waitingOnUserInput, both flags, notLoaded, garbage', () => {
  const cases: [unknown, string][] = [
    [{ type: 'idle' }, 'idle'], [{ type: 'active', activeFlags: [] }, 'turn_running'],
    [{ type: 'active', activeFlags: ['waitingOnApproval'] }, 'waiting_approval'],
    [{ type: 'active', activeFlags: ['waitingOnUserInput'] }, 'waiting_input'],
    [{ type: 'active', activeFlags: ['waitingOnUserInput', 'waitingOnApproval'] }, 'waiting_approval'],
    [{ type: 'notLoaded' }, 'not_loaded'], [{ type: 'systemError' }, 'unknown'],
    [null, 'unknown'], [undefined, 'unknown'], [42, 'unknown'], ['idle', 'unknown'], [[], 'unknown'],
    [{}, 'unknown'], [{ type: 'active' }, 'unknown'], [{ type: 'active', activeFlags: 'waitingOnApproval' }, 'unknown'],
    [{ type: 'active', activeFlags: [null] }, 'unknown'],
  ];
  for (const [input, expected] of cases) assert.equal(mapStatus(input), expected, JSON.stringify(input));
});

test("buildTurnsPage uses the fixture's paging keys and itemsView full", async () => {
  const d = await schema(), token = 'opaque/+?= 世界\u0000';
  assert.deepEqual(buildTurnsPage('thread'), { threadId: 'thread', itemsView: 'full', limit: 5 });
  for (const cursor of [token, '']) {
    const page = buildTurnsPage('thread', cursor);
    assert.deepEqual(page, { threadId: 'thread', itemsView: 'full', limit: 5, cursor });
    for (const key of Object.keys(page)) assert.ok(Object.hasOwn(d.ThreadTurnsListParams.properties, key));
  }
});

test('buildThreadResume emits exactly threadId and excludeTurns', async () => {
  const d = await schema(), result = buildThreadResume('thread');
  assert.deepEqual(Object.keys(result), ['threadId', 'excludeTurns']);
  assert.deepEqual(result, { threadId: 'thread', excludeTurns: true });
  for (const key of Object.keys(result)) assert.ok(Object.hasOwn(d.ThreadResumeParams.properties, key));
  assert.deepEqual(buildThreadUnsubscribe('thread'), { threadId: 'thread' });
});

test('parseResumePolicy: never and auto_review do not ask; on-request, untrusted, granular, missing do', () => {
  assert.deepEqual(parseResumePolicy({ approvalPolicy: 'never', approvalsReviewer: 'user' }), { approvalPolicy: 'never', approvalsReviewer: 'user', asks: false });
  assert.equal(parseResumePolicy({ approvalPolicy: 'on-request', approvalsReviewer: 'auto_review' }).asks, false);
  for (const approvalPolicy of ['on-request', 'untrusted', { granular: { mcp_elicitations: true, rules: false, sandbox_approval: true } }]) {
    assert.equal(parseResumePolicy({ approvalPolicy, approvalsReviewer: 'user' }).asks, true);
  }
  for (const resp of [null, undefined, [], 'never', {}, { approvalPolicy: 'never' },
    { approvalPolicy: 42, approvalsReviewer: 'auto_review' }, { approvalPolicy: 'never', approvalsReviewer: false }]) {
    assert.equal(parseResumePolicy(resp).asks, true, JSON.stringify(resp));
  }
});

test('parseResumePolicy fails closed for malformed policies even with auto_review', () => {
  const granular = { mcp_elicitations: true, rules: false, sandbox_approval: true };
  const malformed: unknown[] = [undefined, null, false, 42, [], {}, 'on-failure', 'garbage',
    { granular: null }, { granular: false }, { granular: [] }, { granular: 'garbage' },
    { granular: {} }, { granular: { rules: 'garbage' } }, { granular, extra: true }];
  for (const key of Object.keys(granular)) {
    const missing: Record<string, unknown> = { ...granular }; delete missing[key];
    malformed.push({ granular: missing });
    for (const value of [undefined, null, 0, 1, 'true', [], {}]) {
      malformed.push({ granular: { ...granular, [key]: value } });
    }
  }
  for (const key of ['request_permissions', 'skill_approval']) {
    for (const value of [null, 1, 'false', [], {}]) malformed.push({ granular: { ...granular, [key]: value } });
  }
  for (const approvalPolicy of malformed) {
    assert.equal(parseResumePolicy({ approvalPolicy, approvalsReviewer: 'auto_review' }).asks, true,
      JSON.stringify(approvalPolicy));
  }
});

test('parseResumePolicy accepts schema policies and compatibility reviewer with the never-or-auto_review rule', () => {
  const policies = ['never', 'on-request', 'untrusted'];
  for (let flags = 0; flags < 8; flags++) {
    const granular = { mcp_elicitations: !!(flags & 1), rules: !!(flags & 2), sandbox_approval: !!(flags & 4) };
    for (const approvalPolicy of [{ granular },
      { granular: { ...granular, request_permissions: false, skill_approval: true } },
      { granular: { ...granular, request_permissions: true, skill_approval: false } }]) {
      for (const approvalsReviewer of ['user', 'auto_review', 'guardian_subagent']) {
        assert.equal(parseResumePolicy({ approvalPolicy, approvalsReviewer }).asks, approvalsReviewer !== 'auto_review',
          JSON.stringify({ approvalPolicy, approvalsReviewer }));
      }
    }
  }
  for (const approvalPolicy of policies) for (const approvalsReviewer of ['user', 'auto_review', 'guardian_subagent']) {
    assert.deepEqual(parseResumePolicy({ approvalPolicy, approvalsReviewer }),
      { approvalPolicy, approvalsReviewer, asks: !(approvalPolicy === 'never' || approvalsReviewer === 'auto_review') });
  }
  for (const approvalsReviewer of [undefined, null, false, {}, 'garbage']) {
    assert.equal(parseResumePolicy({ approvalPolicy: 'never', approvalsReviewer }).asks, true);
  }
});

test('notification opt-outs match the reviewed protocol list', () => {
  assert.deepEqual(CODEX_OPT_OUT_NOTIFICATIONS, ['item/agentMessage/delta', 'item/plan/delta', 'item/reasoning/textDelta',
    'item/reasoning/summaryTextDelta', 'item/commandExecution/outputDelta', 'item/fileChange/outputDelta',
    'hook/started', 'hook/completed', 'thread/tokenUsage/updated', 'turn/moderationMetadata']);
});
