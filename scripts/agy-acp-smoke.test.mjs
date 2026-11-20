// Unit tests for the agy ACP smoke's pure helpers (scripts/agy-acp-smoke.mjs).
// No real agy, model or network is used here; the live smoke itself runs the
// real AcpBackend against `agy acp` with a loopback Gemini stub.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import plugin from '../connectors/launcher/src/backends/acp/index.ts';
import {
  MARKERS, textParts, functionCallPart, lastUserText, hasFunctionResponse, routeGeminiRequest,
  geminiResponse, sseFrame, scrub, receiptIsSanitized, evaluateReceipt, wrapperScript, findProcessesUnder,
} from './agy-acp-smoke.mjs';

const body = (contents, extra = {}) => ({ contents, ...extra });
const withTools = { tools: [{ functionDeclarations: [{ name: 'run_command' }] }] };

test('stub routing keys on request content, never on request order', () => {
  // agy also issues tool-less side requests (titles); those must never consume a scripted turn.
  assert.deepEqual(routeGeminiRequest(body([{ role: 'user', parts: [{ text: 'title this: ACP_PING' }] }])), { kind: 'side', parts: textParts('side') });
  assert.deepEqual(routeGeminiRequest(body([{ role: 'user', parts: [{ text: 'title this: ACP_RUN' }] }])), { kind: 'side', parts: textParts('side') });
  const turnContents = marker => [
    { role: 'user', parts: [{ text: 'earlier ACP_PING turn' }] },
    { role: 'model', parts: [{ text: 'done' }] },
    { role: 'user', parts: [{ text: `please do ${marker} now` }] },
  ];
  assert.equal(routeGeminiRequest(body(turnContents(MARKERS.ping), withTools)).kind, 'ping');
  assert.equal(routeGeminiRequest(body(turnContents(MARKERS.run), withTools)).kind, 'run');
  assert.equal(routeGeminiRequest(body(turnContents(MARKERS.slow), withTools)).kind, 'slow');
  // Same bodies in a different arrival order route identically: order-independence is structural.
  assert.equal(routeGeminiRequest(body(turnContents(MARKERS.run), withTools)).kind, 'run');
  assert.equal(routeGeminiRequest(body([{ role: 'user', parts: [{ text: 'unrelated' }] }], withTools)).kind, 'slow');
});

test('after-tool responses are detected from the last content before user-text markers', () => {
  const afterTool = body([
    { role: 'user', parts: [{ text: 'ACP_RUN please' }] },
    { role: 'model', parts: [{ functionCall: { name: 'run_command', args: {} } }] },
    { role: 'user', parts: [{ functionResponse: { name: 'run_command', response: { ok: true } } }] },
  ], withTools);
  assert.equal(hasFunctionResponse(afterTool), true);
  const routed = routeGeminiRequest(afterTool);
  assert.deepEqual(routed, { kind: 'after_tool', parts: textParts(MARKERS.afterTool) });
});

test('hasFunctionResponse inspects only the last content and lastUserText only the last user role', () => {
  const earlier = body([
    { role: 'user', parts: [{ functionResponse: { name: 'x', response: {} } }] },
    { role: 'model', parts: [{ text: 'moved on' }] },
    { role: 'user', parts: [{ text: 'ACP_RUN' }] },
  ], withTools);
  assert.equal(hasFunctionResponse(earlier), false);
  assert.equal(lastUserText(earlier), 'ACP_RUN');
  assert.equal(lastUserText(body([])), '');
  const multiPart = body([{ role: 'user', parts: [{ text: 'a ' }, { text: 'b' }] }]);
  assert.equal(lastUserText(multiPart), 'a  b'); // join semantics identical to the proven harness stub
});

test('run routing emits the exact run_command permission probe call proven against agy 1.5.13', () => {
  const routed = routeGeminiRequest(body([{ role: 'user', parts: [{ text: 'ACP_RUN' }] }], withTools));
  assert.deepEqual(routed, { kind: 'run', parts: functionCallPart('run_command', {
    CommandLine: MARKERS.permCommand, WaitMsBeforeAsync: 3000, toolSummary: 'Permission probe' }) });
});

test('SSE chunk encoding matches the Gemini streamGenerateContent frame the stub served', () => {
  const response = geminiResponse(textParts('hi'));
  assert.deepEqual(response, {
    candidates: [{ content: { role: 'model', parts: [{ text: 'hi' }] }, finishReason: 'STOP' }],
    usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 1, totalTokenCount: 2 },
  });
  assert.equal(sseFrame(response), 'data: ' + JSON.stringify(response) + '\r\n\r\n');
});

test('scrub removes UUIDs, timestamps, absolute paths and loopback URLs', () => {
  const dirty = 'job 3f2504e0-4f89-41d3-9a0c-0305e82c3301 at 2026-11-21T10:11:12.333Z under ' +
    '/home/xertrov/.cache/vecnet-agy-acp-smoke/run-x/home and /tmp/other plus http://127.0.0.1:41234/v1beta';
  const clean = scrub(dirty);
  assert.equal(clean, 'job <id> at <ts> under <path> and <path> plus <url>/v1beta');
  assert.equal(receiptIsSanitized(clean), true);
});

test('receipt sanitation check flags every forbidden value class', () => {
  assert.equal(receiptIsSanitized(JSON.stringify({ ok: true, note: 'modes seen' })), true);
  for (const bad of ['/home/x/leak', 'id 21f7d37b-9c8c-4c0d-99f5-0d1e2f3a4b5c', 'at 2026-11-20T01:02:03Z', 'stub http://127.0.0.1:9/x', '/tmp/leak'])
    assert.equal(receiptIsSanitized(bad), false, bad);
});

test('evaluateReceipt passes a complete receipt and names every failed check', () => {
  const good = {
    preset_guard: { accept_edits_rejected: true },
    initialize: { ok: true, protocol_version: 1 },
    modes_seen: ['default', 'accept-edits', 'plan'],
    launch_turn: { finished: true, outcome: 'completed', marker_text_seen: true },
    permission: { request_seen: true, kind: 'exec', answered: true },
    followup_turn: { finished: true, outcome: 'completed', marker_text_seen: true },
    cancel: { turn_outcome: 'interrupted', settle_ok: true, end_state: 'settled' },
    teardown: { survivors: 0 },
  };
  assert.deepEqual(evaluateReceipt(good), { pass: true, failures: [] });
  const bad = structuredClone(good);
  bad.preset_guard.accept_edits_rejected = false;
  bad.initialize.ok = false;
  bad.initialize.protocol_version = 2;
  bad.modes_seen = ['plan'];
  bad.launch_turn.finished = false;
  bad.launch_turn.outcome = 'failed';
  bad.launch_turn.marker_text_seen = false;
  bad.permission.request_seen = false;
  bad.permission.kind = 'other';
  bad.permission.answered = false;
  bad.followup_turn.finished = false;
  bad.followup_turn.outcome = 'failed';
  bad.followup_turn.marker_text_seen = false;
  bad.cancel.turn_outcome = 'completed';
  bad.cancel.settle_ok = false;
  bad.cancel.end_state = 'lost';
  bad.teardown.survivors = 2;
  const { pass, failures } = evaluateReceipt(bad);
  assert.equal(pass, false);
  assert.equal(failures.length, 17);
});

const ctx = { host: 'test', now: () => new Date(), redact: s => s, log: { info() {}, warn() {}, error() {} },
  store: { get: () => undefined, set() {}, delete() {}, keys: () => [] },
  checkRoute: async () => { throw new Error('prohibited'); }, readSecretFile: async () => { throw new Error('prohibited'); },
  sleep: () => new Promise(() => {}), exec: async () => { throw new Error('prohibited'); } };

test('the ACP preset guard rejects an agy accept-edits preset and admits default', async () => {
  const base = { argv: ['/usr/bin/env'], provider: 'agy' };
  await assert.rejects(plugin.create({ presets: { agy: { ...base, mode_id: 'accept-edits' } } }, ctx), /acp_invalid_preset/);
  const backend = await plugin.create({ presets: { agy: { ...base, mode_id: 'default' } } }, ctx);
  assert.equal(backend.id, 'acp');
});

test('wrapper script exports the stub env and disposable HOME, then execs agy acp', () => {
  const script = wrapperScript({ agy: '/home/xertrov/.local/bin/agy', home: '/root-x/home', baseUrl: 'http://127.0.0.1:41234' });
  assert.match(script, /^#!\/bin\/sh\n/);
  assert.ok(script.includes(`export HOME='/root-x/home'`));
  assert.ok(script.includes(`export GEMINI_API_KEY='dummy-loopback-key'`));
  assert.ok(script.includes(`export GOOGLE_GEMINI_BASE_URL='http://127.0.0.1:41234'`));
  assert.ok(script.includes(`exec '/home/xertrov/.local/bin/agy' acp`));
  assert.equal(script.includes('\n'), true);
});

test('findProcessesUnder finds a live child by environ marker and clears after exit', async () => {
  const marker = '/nonexistent-agy-smoke-proc-marker-' + randomUUID();
  assert.deepEqual(await findProcessesUnder(marker), []);
  const child = spawn('/bin/sleep', ['8'], { env: { PATH: '/usr/bin:/bin', SMOKE_ROOT_MARKER: marker } });
  const closed = new Promise(resolve => child.once('close', resolve));
  await new Promise((resolve, reject) => { child.once('spawn', resolve); child.once('error', reject); });
  try {
    const found = await findProcessesUnder(marker);
    assert.ok(found.some(f => f.pid === child.pid && f.via === 'environ'), 'child found by environ marker');
  } finally {
    child.kill('SIGKILL');
    await closed;
  }
  const after = await findProcessesUnder(marker);
  assert.equal(after.some(f => f.pid === child.pid), false);
});
