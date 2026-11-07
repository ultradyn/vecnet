// Shared local-only screen harness. No npm install or repository package.json.
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, rm } from 'node:fs/promises';
import { rmSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createInterface } from 'node:readline';
import { crc32, deflateSync } from 'node:zlib';

export const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
export function assert(cond, msg = 'assertion failed') { if (!cond) throw new Error(msg); }

// Each demo owns its process group. Keep this registry at module scope so
// normal exit and uncaught ESM errors share the same synchronous backstop.
const live = new Map();
const exitCleanups = new Set();
export function registerExitCleanup(fn) {
  exitCleanups.add(fn);
  return () => exitCleanups.delete(fn);
}
export function liveDemoCount() { return live.size; }
function signalGroup(pgid, signal) {
  try { process.kill(-pgid, signal); } catch (error) { if (error.code !== 'ESRCH') throw error; }
}
process.on('exit', () => {
  for (const pgid of live.keys()) {
    try { signalGroup(pgid, 'SIGKILL'); } catch {}
  }
  for (const dir of live.values()) {
    const prefix = join(tmpdir(), 'vecnet-ui-data-');
    if (dir.startsWith(prefix) && /^[A-Za-z0-9]+$/.test(dir.slice(prefix.length))) {
      try { rmSync(dir, { recursive: true, force: true }); } catch {}
    }
  }
  for (const cleanup of exitCleanups) { try { cleanup(); } catch {} }
});
for (const [signal, number] of [['SIGINT', 2], ['SIGTERM', 15], ['SIGHUP', 1]]) {
  process.once(signal, () => {
    for (const pgid of live.keys()) { try { signalGroup(pgid, 'SIGTERM'); } catch {} }
    const deadline = Date.now() + 2000;
    const sleeper = new Int32Array(new SharedArrayBuffer(4));
    while (Date.now() < deadline) {
      const remaining = [...live.keys()].some(pgid => {
        try { process.kill(-pgid, 0); return true; }
        catch (error) { return error.code !== 'ESRCH'; }
      });
      if (!remaining) break;
      Atomics.wait(sleeper, 0, 0, Math.min(50, deadline - Date.now()));
    }
    process.exit(128 + number);
  });
}

export async function scrollIntoViewStable(page, selector, { frames = 3, timeout = 15000 } = {}) {
  // Resource notifications replace nodes. Re-query each frame and wait for
  // the same connected node to remain in view across consecutive frames.
  await page.waitForFunction(({ selector, frames }) => {
    const states = window.__vnStable ||= Object.create(null);
    const state = states[selector] ||= { node: null, count: 0 };
    const el = document.querySelector(selector);
    if (!el || el !== state.node) {
      state.node = el;
      state.count = 0;
    }
    if (!el) return false;
    let r = el.getBoundingClientRect();
    const intersects = r.bottom > 0 && r.top < innerHeight;
    const fits = r.height <= innerHeight;
    if (!intersects || (fits && (r.top < 0 || r.bottom > innerHeight))) {
      el.scrollIntoView({ block: 'center', inline: 'nearest' });
      r = el.getBoundingClientRect();
    }
    if (el.isConnected && r.bottom > 0 && r.top < innerHeight) state.count++;
    else state.count = 0;
    if (state.count < frames) return false;
    delete states[selector];
    return true;
  }, { selector, frames }, { polling: 'raf', timeout });
}
export async function scrollMessageIntoView(page, id) { return scrollIntoViewStable(page, `[id="m-${id}"]`); }

// readThrough reads an open thread as the operator would (CoS unread ruling:
// a message is read once 60% visible for a second, or scrolled past going
// down): it scrolls the thread from where it opened to the bottom in steps
// smaller than the view, so every message passes through it, then waits for
// unread() (the hub's unread count for the thread) to reach zero. Opening a
// thread no longer marks it read. Each position is held for STEP_MS so the
// thread's IntersectionObserver reports it: on a fast box, two frames per step
// could carry the first messages out of view before any report, and a message
// never reported in view is (correctly) not read by passing it.
export async function readThrough(page, unread, { timeout = 30000 } = {}) {
  await page.evaluate(async () => {
    const STEP_MS = 120;
    const main = document.getElementById('main');
    const frames = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const hold = () => frames().then(() => new Promise(resolve => setTimeout(resolve, STEP_MS)));
    await hold(); // where it opened
    for (let i = 0; i < 400; i++) {
      const before = main.scrollTop;
      main.scrollTop = before + Math.max(80, Math.floor(main.clientHeight * 0.4));
      await hold();
      if (main.scrollTop === before) break;
    }
  });
  const deadline = Date.now() + timeout;
  for (let left = await unread(); left > 0; left = await unread()) {
    assert(Date.now() < deadline, `the thread still has ${left} unread after reading through it`);
    await page.waitForTimeout(100);
  }
}

// png encodes a w×h 8-bit RGB image whose pixel colours come from px(x, y),
// for image fixtures built at run time (no binaries are committed).
export function png(w, h, px) {
  const raw = Buffer.alloc((w * 3 + 1) * h);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) raw.set(px(x, y), y * (w * 3 + 1) + 1 + x * 3);
  }
  const chunk = (type, data) => {
    const body = Buffer.concat([Buffer.from(type, 'latin1'), data]), len = Buffer.alloc(4), crc = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    crc.writeUInt32BE(crc32(body));
    return Buffer.concat([len, body, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; ihdr[9] = 2; // 8-bit RGB
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', ihdr), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0))]);
}
// hex is '#rrggbb' as the [r, g, b] png() wants.
export const hex = s => [parseInt(s.slice(1, 3), 16), parseInt(s.slice(3, 5), 16), parseInt(s.slice(5, 7), 16)];
export async function run(command, args, options = {}) {
  const child = spawn(command, args, { cwd: root, ...options, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', d => { stdout += d; });
  child.stderr.on('data', d => { stderr += d; });
  const [code] = await once(child, 'close');
  if (code !== 0) throw new Error(`${command} exited ${code}: ${stderr}`);
  return stdout;
}
export async function chromium() {
  process.env.PLAYWRIGHT_BROWSERS_PATH ||= join(homedir(), '.cache/ms-playwright');
  const module = process.env.PLAYWRIGHT_MODULE || join(homedir(), '.llm-general/skills/headless-browser-screenshots/node_modules/playwright/index.mjs');
  return (await import(isAbsolute(module) ? pathToFileURL(module).href : module)).chromium;
}
export async function shotPath(name) {
  assert(/^[a-zA-Z0-9_-]+$/.test(name), 'invalid shot name');
  const dir = resolve(process.env.SHOTS_DIR || join(root, 'docs/plans/shots/oui'));
  await mkdir(dir, { recursive: true });
  return join(dir, `${name}.png`);
}

// Build cache lives for this script process; each invocation gets a fresh DB.
let build;
async function binaries() {
  if (!build) build = (async () => {
    const dir = await mkdtemp(join(tmpdir(), 'vecnet-ui-build-'));
    process.once('exit', () => rmSync(dir, { recursive: true, force: true }));
    try {
      await run('build2', ['go', 'build', '-p', '2', '-o', join(dir, 'demo'), './internal/opseed/demo'], { env: { ...process.env, CGO_ENABLED: '0' } });
      return dir;
    } catch (error) { await rm(dir, { recursive: true, force: true }); throw error; }
  })();
  return build;
}
export async function cleanBuild() {
  if (build) { const pending = build; build = undefined; await rm(await pending, { recursive: true, force: true }); }
}
export async function buildCLI(dir) {
  await run('build2', ['go', 'build', '-p', '2', '-o', join(dir, 'vecnet'), './cmd/vecnet'], { env: { ...process.env, CGO_ENABLED: '0' } });
  return join(dir, 'vecnet');
}
// demoCloser registers a spawned demo (its own process group) and returns
// close(), which stops the group and removes its data dir. It signals only
// while the original ChildProcess is unreaped and still registered: once it
// has exited, its numeric PGID may already name an unrelated group, so a
// late close() or delayed KILL must not reuse it. Node sets exitCode or
// signalCode in the same callback that reaps the child, before any other JS.
export function demoCloser(child, dir, { signal = signalGroup, killAfterMs = 7000 } = {}) {
  live.set(child.pid, dir);
  child.once('exit', () => live.delete(child.pid));
  const closed = new Promise(resolve => child.once('close', resolve));
  const running = () => child.exitCode === null && child.signalCode === null && live.get(child.pid) === dir;
  let stopped = false;
  async function close() {
    if (stopped) return;
    stopped = true;
    if (running()) signal(child.pid, 'SIGTERM');
    let timer;
    await Promise.race([closed, new Promise(resolve => { timer = setTimeout(() => { if (running()) signal(child.pid, 'SIGKILL'); resolve(); }, killAfterMs); })]);
    clearTimeout(timer);
    await closed;
    if (live.get(child.pid) === dir) live.delete(child.pid);
    await rm(dir, { recursive: true, force: true });
  }
  return { close, closed };
}
// args are extra demo flags (e.g. --pref-flag for a flag-sourced preference).
export async function bootDemo({ scale = false, args = [] } = {}) {
  const binDir = await binaries();
  const dir = await mkdtemp(join(tmpdir(), 'vecnet-ui-data-'));
  const child = spawn(join(binDir, 'demo'), ['--listen', '127.0.0.1:0', '--data-dir', dir, '--exit-on-stdin-eof', ...(scale ? ['--scale'] : []), ...args], { cwd: root, detached: true, stdio: ['pipe', 'pipe', 'pipe'] });
  const { close, closed } = demoCloser(child, dir);
  child.stdin.on('error', () => {}); // The demo may close stdin before its holder.
  // Consume logs, but never echo peer text or credentials into a terminal.
  child.stderr.resume();
  const lines = createInterface({ input: child.stdout });
  let timer;
  try {
    const first = once(lines, 'line');
    const line = await Promise.race([first.then(([line]) => line), closed.then(() => { throw new Error('demo exited before readiness'); }), new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('demo startup timed out')), 60000); })]);
    const demo = JSON.parse(line);
    const control = async (name, body) => {
      const response = await fetch(`${demo.url}/__demo/${name}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      assert(response.ok, `demo ${name}: ${response.status}`);
      return response.json();
    };
    const asPeer = async (name, method, path, body) => {
      const secret = name === 'operator' ? demo.operator_secret : demo.peers[name]?.secret;
      assert(secret, `no bearer for peer ${name}`);
      return fetch(demo.url + path, { method, headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json', 'X-Vecnet-Ingress': 'operator_ui' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    };
    return { demo, dir, pid: child.pid, buildDir: binDir, close, asPeer, advance: by => control('advance', { by }), restart: ms => control('restart', { retry_after_ms: ms }), stall: duration => control('stall', { for: duration }), clear: name => control('clear', { control: name }), controlStatus: () => control('status', {}), fail: (prefix, status, duration) => control('fail', { prefix, status, for: duration }) };
  } catch (error) { await close(); throw error; }
  finally { clearTimeout(timer); lines.close(); child.stdout.resume(); }
}

// Requests that stay open by design: the operator activity stream, and the
// event pump's parked long-poll (any wait other than 0). quiet() ignores them.
export function longLived(url) {
  const u = new URL(url);
  if (u.pathname === '/v1/admin/activity') return true;
  return u.pathname === '/v1/events' && (u.searchParams.get('wait') || '0') !== '0';
}

// networkQuiet tracks the page's requests so a screen can wait for the data
// it shows to have landed: nothing in flight but long-lived streams and
// requests the script itself is holding, for idleMs and two frames after it
// (the response's render, and any request that render starts). A shot taken
// after it shows every snapshot the view had asked for.
export function networkQuiet(page) {
  const open = new Set(), ignored = [];
  const track = req => { if (!longLived(req.url())) open.add(req); };
  const done = req => open.delete(req);
  page.on('request', track);
  page.on('requestfinished', done);
  page.on('requestfailed', done);
  const busy = () => [...open].filter(req => !ignored.some(match => match(req)));
  const frames = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  async function quiet({ idleMs = 150, timeout = 60000 } = {}) {
    const deadline = Date.now() + timeout;
    for (let still = 0; ;) {
      if (Date.now() > deadline) throw new Error(`network never went quiet: ${busy().map(req => req.method() + ' ' + new URL(req.url()).pathname).join(', ')}`);
      if (busy().length) { still = 0; await new Promise(resolve => setTimeout(resolve, 25)); continue; }
      if (still >= idleMs) {
        await frames();
        if (!busy().length) return;
        still = 0;
        continue;
      }
      await new Promise(resolve => setTimeout(resolve, 25));
      still += 25;
    }
  }
  // ignore(match) leaves requests the script holds on purpose out of quiet();
  // the returned function puts them back.
  const ignore = match => { ignored.push(match); return () => { const i = ignored.indexOf(match); if (i >= 0) ignored.splice(i, 1); }; };
  return { quiet, ignore, busy };
}

// UI_CPU_THROTTLE=<n> slows the page's main thread n× (Chromium's DevTools
// throttle), so a screen's load races show up on an idle box too: a script
// that passes throttled does not depend on the browser keeping up.
async function throttle(context, page) {
  const rate = Number(process.env.UI_CPU_THROTTLE || 0);
  if (!(rate > 1)) return;
  const cdp = await context.newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate });
  console.log(`browser CPU throttled ${rate}x (UI_CPU_THROTTLE)`);
}

export async function withDemo({ scale = false, signedIn = true, demoArgs = [] } = {}, fn) {
  const env = await bootDemo({ scale, args: demoArgs });
  let browser;
  const errors = [], allowed = [], held = [];
  try {
    browser = await (await chromium()).launch({ headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
    const page = await context.newPage();
    await throttle(context, page);
    page.setDefaultTimeout(60000);
    const net = networkQuiet(page);
    page.on('console', msg => { if (msg.type() === 'error') errors.push(msg.text()); });
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(({ secret }) => {
      if (secret) localStorage.setItem('vecnet.ui.secret', secret);
      addEventListener('securitypolicyviolation', event => console.error(`CSP violation: ${event.violatedDirective}`));
    }, { secret: signedIn ? env.demo.operator_secret : '' });
    const requests = [];
    page.on('request', req => requests.push(req));
    const ctx = {
      page, dir: env.dir, demo: env.demo, advance: env.advance, restart: env.restart, asPeer: env.asPeer,
      allowConsole: regex => allowed.push(regex),
      countRequests: filter => requests.filter(req => typeof filter === 'function' ? filter(req) : filter instanceof RegExp ? filter.test(req.url()) : req.url().includes(filter)).length,
      shot: async name => { await page.evaluate(() => document.fonts.ready); await page.screenshot({ path: await shotPath(name) }); },
      // quiet() waits for every snapshot in flight to land and draw (see
      // networkQuiet); ignore(req => bool) exempts a request held on purpose.
      quiet: net.quiet, ignore: net.ignore,
      hold: async glob => {
        let release;
        const gate = new Promise(resolve => { release = resolve; });
        const gated = new Set();
        const handler = async route => { gated.add(route.request()); await gate; await route.continue(); };
        const unignore = net.ignore(req => gated.has(req));
        await page.route(glob, handler);
        let released = false;
        const unblock = async () => { if (!released) { released = true; unignore(); release(); await page.unroute(glob, handler); } };
        held.push(unblock);
        return unblock;
      },
      fail: async (glob, status = 500, code = 'demo_failure', message = 'Demo failure') => {
        await page.route(glob, route => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ error: { code, message, details: {} } }) }));
      },
    };
    await page.goto(`${env.demo.url}/ui/`, { waitUntil: 'load' });
    await fn(ctx);
    const unexpected = errors.filter(text => !allowed.some(regex => { regex.lastIndex = 0; return regex.test(text); }));
    assert(!unexpected.length, `browser errors: ${unexpected.join('\n')}`);
  } finally {
    await Promise.allSettled(held.map(release => release()));
    try { await browser?.close(); } finally { await env.close(); }
  }
}
