#!/usr/bin/env node
// Qualifies `agy acp` (Google Antigravity CLI, verified 1.5.13) on the Vecnet
// launcher ACP backend (connectors/launcher/src/backends/acp/) end to end:
// initialize, modes, launch prompt turn, typed permission round-trip,
// follow-up turn, mid-turn cancel, settle and process teardown.
//
// The ACP backend's child environment deliberately drops env names matching
// *_API_KEY / *_BASE_URL, so GEMINI_API_KEY and GOOGLE_GEMINI_BASE_URL can
// never reach a directly-spawned agy. This smoke therefore presets
// argv[0] = a 0700 wrapper inside the disposable root that re-exports the
// loopback-stub env plus the disposable HOME and execs `agy acp` — the same
// wrapper an API-key production setup needs (see docs/connectors/launcher.md).
//
// The Gemini "model" is a loopback stub only: it routes each request by
// CONTENT (tool-less side requests vs scripted turns), never by request
// index, because agy interleaves tool-less title-generation requests. The
// shapes port scripts/agy-canary-probe.py's Stub and its proven `acp` script.
//
// Usage: node --disable-warning=ExperimentalWarning scripts/agy-acp-smoke.mjs
// Prints a sanitized JSON receipt (no absolute paths, UUIDs, timestamps or
// env values) and exits 0 only when every qualification check passes.

import { execFile, spawn } from 'node:child_process';
import { access, chmod, constants, mkdir, mkdtemp, readdir, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { resolve } from 'node:path';
import { createServer } from 'node:http';
import { pathToFileURL } from 'node:url';
import plugin from '../connectors/launcher/src/backends/acp/index.ts';

const AGY_DEFAULT = '/home/xertrov/.local/bin/agy';
const DUMMY_KEY = 'dummy-loopback-key';

export const MARKERS = Object.freeze({
  ping: 'ACP_PING', pong: 'ACP_PONG_OK', run: 'ACP_RUN', afterTool: 'ACP_AFTER_TOOL', slow: 'ACP_SLOW',
  permCommand: 'echo acp-perm',
});

// ------------------------------------------------------------ pure helpers

export function textParts(text) { return [{ text }]; }
export function functionCallPart(name, args) { return [{ functionCall: { name, args } }]; }

export function lastUserText(body) {
  const contents = Array.isArray(body?.contents) ? body.contents : [];
  for (let i = contents.length - 1; i >= 0; i--) {
    const c = contents[i];
    if (c?.role !== 'user') continue;
    return (Array.isArray(c.parts) ? c.parts : []).map(p => (typeof p?.text === 'string' ? p.text : '')).join(' ');
  }
  return '';
}

export function hasFunctionResponse(body) {
  const contents = Array.isArray(body?.contents) ? body.contents : [];
  if (!contents.length) return false;
  const parts = Array.isArray(contents[contents.length - 1]?.parts) ? contents[contents.length - 1].parts : [];
  return parts.some(p => p !== null && typeof p === 'object' && 'functionResponse' in p);
}

/** Route a loopback Gemini request by content only. agy interleaves tool-less
 * side requests (titles), so request order must never select a response. */
export function routeGeminiRequest(body) {
  if (!Array.isArray(body?.tools) || !body.tools.length) return { kind: 'side', parts: textParts('side') };
  if (hasFunctionResponse(body)) return { kind: 'after_tool', parts: textParts(MARKERS.afterTool) };
  const user = lastUserText(body);
  if (user.includes(MARKERS.ping)) return { kind: 'ping', parts: textParts(MARKERS.pong) };
  if (user.includes(MARKERS.run)) {
    // Exact permission-probe call proven against agy 1.5.13: run_command needs
    // approval and carries toolSummary alongside its own args.
    return { kind: 'run', parts: functionCallPart('run_command', {
      CommandLine: MARKERS.permCommand, WaitMsBeforeAsync: 3000, toolSummary: 'Permission probe' }) };
  }
  return { kind: 'slow', parts: textParts(MARKERS.slow) };
}

export function geminiResponse(parts) {
  return {
    candidates: [{ content: { role: 'model', parts }, finishReason: 'STOP' }],
    usageMetadata: { promptTokenCount: 1, candidatesTokenCount: 1, totalTokenCount: 2 },
  };
}

export function sseFrame(response) { return 'data: ' + JSON.stringify(response) + '\r\n\r\n'; }

const UUID_RE = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi;
const TS_RE = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?/g;
const PATH_RE = /(?:file:\/\/)?\/(?:tmp|home|run|var|proc|etc|opt|usr|srv|mnt|media)\/[^\s"'\\]*/g;
const URL_RE = /https?:\/\/[0-9a-z.-]+(?::\d+)?/gi;

/** Replace conversation/task ids, timestamps, absolute paths and loopback
 * URLs so any excerpt is safe to commit (port of the harness scrub()). */
export function scrub(value) {
  const text = String(value ?? '');
  return text.replace(UUID_RE, '<id>').replace(TS_RE, '<ts>').replace(PATH_RE, '<path>').replace(URL_RE, '<url>');
}

export function receiptIsSanitized(text) {
  return ![UUID_RE, TS_RE, PATH_RE, URL_RE].some(re => {
    re.lastIndex = 0;
    return re.test(text);
  });
}

/** Every qualification fact the receipt must establish. */
export function evaluateReceipt(receipt) {
  const checks = [
    ['preset_guard.accept_edits_rejected', receipt.preset_guard?.accept_edits_rejected === true],
    ['initialize.ok', receipt.initialize?.ok === true],
    ['initialize.protocol_version', receipt.initialize?.protocol_version === 1],
    ['modes_seen includes default', Array.isArray(receipt.modes_seen) && receipt.modes_seen.includes('default')],
    ['launch_turn.finished', receipt.launch_turn?.finished === true],
    ['launch_turn.outcome completed', receipt.launch_turn?.outcome === 'completed'],
    ['launch_turn.marker_text_seen', receipt.launch_turn?.marker_text_seen === true],
    ['permission.request_seen', receipt.permission?.request_seen === true],
    ['permission.kind exec', receipt.permission?.kind === 'exec'],
    ['permission.answered', receipt.permission?.answered === true],
    ['followup_turn.finished', receipt.followup_turn?.finished === true],
    ['followup_turn.outcome completed', receipt.followup_turn?.outcome === 'completed'],
    ['followup_turn.marker_text_seen', receipt.followup_turn?.marker_text_seen === true],
    ['cancel.turn_outcome interrupted', receipt.cancel?.turn_outcome === 'interrupted'],
    ['cancel.settle_ok', receipt.cancel?.settle_ok === true],
    ['cancel.end_state settled', receipt.cancel?.end_state === 'settled'],
    ['teardown.survivors zero', receipt.teardown?.survivors === 0],
  ];
  const failures = checks.filter(([, ok]) => !ok).map(([name]) => name);
  return { pass: failures.length === 0, failures };
}

/** 0700 wrapper that restores the loopback stub env and disposable HOME and
 * execs `agy acp`; the ACP backend's env filter strips *_API_KEY/_BASE_URL. */
export function wrapperScript({ agy, home, baseUrl }) {
  return [
    '#!/bin/sh',
    '# vecnet agy ACP smoke wrapper: restores stub env the ACP backend filters out.',
    `export HOME='${home}'`,
    `export GEMINI_API_KEY='${DUMMY_KEY}'`,
    `export GOOGLE_GEMINI_BASE_URL='${baseUrl}'`,
    'export TERM=dumb',
    'export LANG=C.UTF-8',
    `exec '${agy}' acp`,
    '',
  ].join('\n');
}

/** Processes whose cwd-agnostic footprint (environ or cmdline) names a path:
 * used to prove no agy child survives settle. Scans /proc; foreign or racing
 * processes are skipped, unexpected errors propagate. */
export async function findProcessesUnder(root) {
  const found = [];
  let entries;
  try { entries = await readdir('/proc'); } catch { return found; }
  for (const entry of entries) {
    if (!/^\d+$/.test(entry)) continue;
    for (const [via, file] of [['cmdline', 'cmdline'], ['environ', 'environ']]) {
      let data;
      try { data = await readFile(`/proc/${entry}/${file}`); }
      catch (error) {
        if (['ENOENT', 'ESRCH', 'EACCES', 'EPERM', 'EBUSY', 'EMFILE', 'ENFILE', 'EIO'].includes(error?.code)) break;
        throw error;
      }
      if (data.includes(root)) { found.push({ pid: Number(entry), via }); break; }
    }
  }
  return found;
}

// ---------------------------------------------------------- runtime helpers

const sleep = ms => new Promise(resolve => { const t = setTimeout(resolve, ms); if (typeof t.unref === 'function') t.unref(); });
const withTimeout = (promise, ms, label) => Promise.race([promise, sleep(ms).then(() => { throw new Error('timeout: ' + label); })]);

function makeContext() {
  const values = new Map();
  return {
    host: 'agy-smoke-host', now: () => new Date(), redact: s => s,
    log: { info() {}, warn() {}, error() {} },
    store: {
      get: k => values.get(k), set: (k, v) => { values.set(k, v); }, delete: k => { values.delete(k); },
      keys: p => [...values.keys()].filter(k => k.startsWith(p ?? '')),
    },
    checkRoute: async () => { throw new Error('route mutation prohibited'); },
    readSecretFile: async () => { throw new Error('secret access prohibited'); },
    sleep: (ms, signal) => new Promise((resolve, reject) => {
      if (signal?.aborted) { reject(new Error('aborted')); return; }
      const done = () => { clearTimeout(timer); signal?.removeEventListener('abort', abort); resolve(); };
      const abort = () => { clearTimeout(timer); signal?.removeEventListener('abort', abort); reject(new Error('aborted')); };
      const timer = setTimeout(done, ms); signal?.addEventListener('abort', abort, { once: true });
    }),
    exec: (argv, opts) => new Promise((resolve, reject) => {
      execFile(argv[0], argv.slice(1), { cwd: opts.cwd, env: opts.env ?? process.env, timeout: opts.timeoutMs, signal: opts.signal },
        (error, stdout, stderr) => {
          if (error && typeof error.code !== 'number') { reject(error); return; }
          resolve({ code: error?.code ?? 0, stdout: String(stdout ?? ''), stderr: String(stderr ?? '') });
        });
    }),
  };
}

export function createGate(timeoutMs) {
  let release, timer;
  const promise = new Promise(resolve => {
    release = () => { clearTimeout(timer); resolve(); };
    timer = setTimeout(resolve, timeoutMs);
    if (typeof timer.unref === 'function') timer.unref();
  });
  return { promise, release: () => release() };
}

/** Loopback Gemini stub: SSE streamGenerateContent chunks routed by content. */
export async function startStub() {
  const requests = [];
  const gate = createGate(45000); // holds the cancel turn open; also a safety cap
  const server = createServer((req, res) => {
    res.on('error', () => {});
    const chunks = [];
    req.on('data', c => chunks.push(c));
    req.on('error', () => res.destroy());
    req.on('end', async () => {
      let bodyJson;
      try { bodyJson = JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}'); }
      catch { res.destroy(); return; }
      requests.push({ path: req.url, body: bodyJson });
      const route = routeGeminiRequest(bodyJson);
      try {
        if (route.kind === 'slow') await gate.promise;
        res.writeHead(200, { 'Content-Type': 'text/event-stream' });
        res.end(sseFrame(geminiResponse(route.parts)));
      } catch { res.destroy(); }
    });
  });
  server.on('clientError', (_err, socket) => socket.destroy());
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', resolve).once('error', reject));
  const url = `http://127.0.0.1:${server.address().port}`;
  return { url, requests, gate, close: async () => { gate.release(); server.closeAllConnections?.(); await new Promise(r => server.close(r)); } };
}

async function stopChild(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const closed = new Promise(resolve => child.once('close', resolve));
  child.kill('SIGTERM');
  if (!await Promise.race([closed.then(() => true), sleep(2000).then(() => false)])) {
    child.kill('SIGKILL');
    await closed;
  }
}

/** Direct minimal ACP handshake through the wrapper to record initialize and
 * the session's advertised modes (facts the backend keeps internal). The
 * child gets only {PATH, HOME} — the wrapper restores the loopback stub env
 * the ACP backend filters out — never the full ambient environment. */
export async function probeAcpHandshake({ command, cwd, home }) {
  const child = spawn(command, [], { cwd, env: { PATH: process.env.PATH ?? '/usr/bin:/bin', HOME: home }, stdio: ['pipe', 'pipe', 'pipe'] });
  child.stderr.resume();
  const pending = new Map();
  let nextId = 0, buffer = '';
  const call = (method, params, timeoutMs = 30000) => new Promise((resolve, reject) => {
    const id = ++nextId;
    const timer = setTimeout(() => { pending.delete(id); reject(new Error('timeout: ' + method)); }, timeoutMs);
    if (typeof timer.unref === 'function') timer.unref();
    pending.set(id, { resolve: value => { clearTimeout(timer); resolve(value); }, reject: error => { clearTimeout(timer); reject(error); } });
    child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
  });
  child.stdout.on('data', chunk => {
    buffer += chunk;
    let end;
    while ((end = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, end); buffer = buffer.slice(end + 1);
      if (!line.trim()) continue;
      let message;
      try { message = JSON.parse(line); } catch { continue; }
      if (message?.jsonrpc !== '2.0' || message.method !== undefined || message.id === undefined) continue;
      pending.get(message.id)?.[message.error ? 'reject' : 'resolve'](message.error ?? message.result);
    }
  });
  try {
    const init = await call('initialize', { protocolVersion: 1, clientCapabilities: {}, clientInfo: { name: 'vecnet-agy-acp-smoke', version: '1' } });
    const created = await call('session/new', { cwd, mcpServers: [] });
    const modes = Array.isArray(created?.modes?.availableModes) ? created.modes.availableModes : [];
    const ids = modes.map(m => (m && typeof m.id === 'string' ? m.id : undefined)).filter(id => id !== undefined);
    try { await call('session/close', { sessionId: created?.sessionId }, 5000); } catch { /* process teardown below is still enforced */ }
    return { protocol_version: init?.protocolVersion, modes_seen: ids };
  } finally {
    child.stdin.end();
    await stopChild(child);
  }
}

async function eventsUntil(handle, predicate, timeoutMs = 60000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    for await (const { event } of handle.turns(undefined, controller.signal)) if (predicate(event)) return event;
    if (controller.signal.aborted) throw new Error('timeout waiting for turn event');
    throw new Error('event stream ended before the expected event');
  } finally { clearTimeout(timer); controller.abort(); }
}

async function waitFor(predicate, timeoutMs, what) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await sleep(100);
  }
  throw new Error('timeout waiting for ' + what);
}

// ------------------------------------------------------------------- smoke

async function smokeBaseDir() {
  const cache = process.env.XDG_CACHE_HOME && process.env.XDG_CACHE_HOME.startsWith('/') ? process.env.XDG_CACHE_HOME : join(homedir(), '.cache');
  const base = join(cache, 'vecnet-agy-acp-smoke');
  await mkdir(base, { recursive: true, mode: 0o700 });
  return realpath(base); // workspace roots must be canonical and outside /tmp
}

async function runSmoke() {
  const agy = process.env.AGY_BIN || AGY_DEFAULT;
  await access(agy, constants.X_OK);
  const base = await smokeBaseDir();
  const root = await mkdtemp(join(base, 'run-')); // literal path; the only thing cleanup removes
  const home = join(root, 'home');
  const ctx = makeContext();
  let stub, handle, backend;
  const facts = {
    preset_guard: {}, initialize: {}, modes_seen: undefined,
    launch_turn: {}, permission: {}, followup_turn: {}, cancel: {}, teardown: {},
  };
  try {
    await mkdir(home, { mode: 0o700 });
    const agyDir = join(home, '.gemini', 'antigravity-cli');
    await mkdir(agyDir, { recursive: true });
    await writeFile(join(agyDir, 'settings.json'), JSON.stringify({ modelProvider: 'gemini' }));
    stub = await startStub();
    const wrapper = join(root, 'agy-acp-wrapper.sh');
    await writeFile(wrapper, wrapperScript({ agy, home, baseUrl: stub.url }));
    await chmod(wrapper, 0o700);
    const version = (await withTimeout(new Promise((resolve, reject) =>
      execFile(agy, ['--version'], { env: { PATH: process.env.PATH ?? '/usr/bin:/bin', HOME: home }, timeout: 30000 },
        (error, stdout) => error ? reject(error) : resolve(String(stdout)))), 35000, 'agy --version')).trim();

    // Preset guard documents itself for agy: accept-edits is refused, default admitted.
    try {
      await plugin.create({ presets: { agy: { argv: [wrapper], provider: 'agy', mode_id: 'accept-edits' } } }, ctx);
      facts.preset_guard.accept_edits_rejected = false;
    } catch (error) {
      facts.preset_guard.accept_edits_rejected = /acp_invalid_preset/.test(String(error?.message));
      if (!facts.preset_guard.accept_edits_rejected) throw error;
    }

    const probeWs = join(root, 'probe-ws');
    await mkdir(probeWs, { mode: 0o700 });
    const handshake = await probeAcpHandshake({ command: wrapper, cwd: probeWs, home });
    facts.initialize.ok = handshake.protocol_version === 1;
    facts.initialize.protocol_version = handshake.protocol_version;
    facts.modes_seen = handshake.modes_seen;

    backend = await plugin.create({ workspace_root: join(root, 'workspaces'), request_timeout_ms: 30000,
      presets: { agy: { argv: [wrapper], provider: 'agy', mode_id: 'default' } } }, ctx);
    const health = await backend.health();
    if (!health.ok) throw new Error('acp backend health failed: ' + health.degraded?.join(', '));

    const permission = { seen: undefined, chosen: undefined };
    const hooks = {
      onEvent() {},
      onQuestion: async () => { throw new Error('questions not advertised'); },
      onPermissionRequest: async req => {
        permission.seen = req;
        const allow = req.options.find(o => o.grants === 'once') ?? req.options.find(o => o.grants !== 'cancel');
        permission.chosen = allow?.id;
        return { optionId: permission.chosen ?? req.defaultOnExpiry, decidedBy: 'peer_smoke' };
      },
    };
    const spec = {
      jobId: 'job_agy_acp_smoke', tag: 'vl-job_agy_acp_smoke', title: 'agy acp smoke', prompt: MARKERS.ping,
      project: { alias: 'agy-smoke', repoRoot: '/not/preallocated', baseRef: '', scratch: true },
      branch: 'launch/job_agy_acp_smoke', workspaceMode: 'temp', preset: 'agy', permissionMode: 'ask',
      attachments: [], env: {},
    };
    handle = await backend.launch(spec, hooks);

    // Turn 1: the launch prompt finishes with the marker text.
    const first = await eventsUntil(handle, e => e.type === 'turn_finished');
    facts.launch_turn = { finished: true, outcome: first.outcome, marker_text_seen: String(first.finalText ?? '').includes(MARKERS.pong) };

    // Turn 2: a tool that needs approval; the typed permission round-trip answers it.
    const run = await handle.send({ items: [{ deliveryId: 'dly_run', messageId: 'msg_run', from: 'peer_smoke', text: MARKERS.run, attachments: [] }] }, 'start');
    const second = await eventsUntil(handle, e => e.type === 'turn_finished' && e.turnId === run.turnId);
    facts.permission = {
      request_seen: permission.seen !== undefined,
      kind: permission.seen?.permission?.kind,
      options: permission.seen?.options?.map(o => o.grants),
      answered: permission.chosen !== undefined,
    };
    facts.followup_turn = { finished: true, outcome: second.outcome, marker_text_seen: String(second.finalText ?? '').includes(MARKERS.afterTool) };

    // Turn 3: a gated (held-open) model call, cancelled mid-turn.
    const slow = await handle.send({ items: [{ deliveryId: 'dly_slow', messageId: 'msg_slow', from: 'peer_smoke', text: MARKERS.slow, attachments: [] }] }, 'start');
    await waitFor(() => stub.requests.some(r => routeGeminiRequest(r.body).kind === 'slow'), 15000, 'the gated model request');
    await handle.interrupt('smoke cancel');
    const third = await eventsUntil(handle, e => e.type === 'turn_finished' && e.turnId === slow.turnId);
    stub.gate.release();
    const settle = await handle.settle('cancel');
    const state = handle.state();
    facts.cancel = { turn_outcome: third.outcome, settle_ok: settle.workspaceKept === true,
      end_state: state.kind === 'ended' ? state.reason : state.kind };

    // Teardown: no process whose environ or cmdline names the disposable root.
    await sleep(500);
    const survivors = await findProcessesUnder(root);
    facts.teardown = { survivors: survivors.length };
    if (survivors.length) console.error('survivor details (scrubbed):', scrub(JSON.stringify(survivors)));

    const receipt = { smoke: 'agy-acp', agy_version: version, backend: 'acp', model_requests: stub.requests.length, ...facts };
    return receipt;
  } finally {
    stub?.gate.release();
    try { await handle?.settle('cancel'); } catch { /* launch may have failed first */ }
    await stub?.close().catch(() => {});
    await rm(root, { recursive: true, force: true }); // only the literal mkdtemp path
  }
}

async function main() {
  let receipt;
  try { receipt = await runSmoke(); }
  catch (error) {
    console.error('agy ACP smoke failed:', scrub(String(error?.message ?? error)));
    process.exitCode = 1;
    return;
  }
  const evaluation = evaluateReceipt(receipt);
  const text = scrub(JSON.stringify({ ...receipt, failures: evaluation.failures, pass: evaluation.pass }, null, 2));
  const sanitized = receiptIsSanitized(text);
  console.log(text);
  process.exitCode = evaluation.pass && sanitized ? 0 : 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main();
