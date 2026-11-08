import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chromium, networkQuiet, scrollMessageIntoView } from './lib.mjs';
import { EventEmitter } from 'node:events';
import { mkdtemp, writeFile, readFile, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// Evaluate the real lifecycle against a synthetic process: signal/exit tests
// never signal this test runner or an OS child and never launch a browser.
async function lifecyclePorts() {
  const url = new URL('./lib.mjs', import.meta.url), hooks = {}, bindings = {};
  let source = await readFile(url, 'utf8');
  const matches = [...source.matchAll(/^import\s+\{([^}]+)\}\s+from\s+'([^']+)';/gm)];
  for (const [, names, spec] of matches) {
    const module = await import(spec);
    for (const name of names.split(',').map(x => x.trim())) bindings[name] = module[name];
  }
  source = source.replace(/^import\s+\{[^}]+\}\s+from\s+'[^']+';/gm, '').replace(/\bexport /g, '')
    .replaceAll('import.meta.url', JSON.stringify(url.href));
  bindings.process = { on: (event, fn) => { hooks[event] = fn; }, once: (event, fn) => { hooks[event] = fn; },
    kill(_pid, signal) { if (signal === 0) throw Object.assign(new Error('synthetic exited'), { code: 'ESRCH' }); }, exit() { hooks.exit(); } };
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  return { hooks, ...await new AsyncFunction(...Object.keys(bindings), source + '\nreturn {demoCloser};')(...Object.values(bindings)) };
}

for (const event of ['close', 'child-signal', 'exit', 'SIGTERM']) {
  test('H1 opt-in recovery retains demo data and authority through ' + event, async t => {
    const dir = await mkdtemp(join(tmpdir(), 'vecnet-ui-data-'));
    t.after(() => rm(dir, { recursive: true, force: true }));
    await writeFile(join(dir, 'authority'), 'synthetic-private-authority', { mode: 0o600 });
    const { demoCloser, hooks } = await lifecyclePorts();
    const child = new EventEmitter(); child.pid = 123456789; child.exitCode = null; child.signalCode = null;
    const closer = demoCloser(child, dir, { recovery: { canDispose: () => false }, killAfterMs: 5,
      signal() { child.exitCode = 0; child.emit('exit', 0); child.emit('close', 0); } });
    if (event === 'child-signal') { child.signalCode = 'SIGKILL'; child.emit('exit', null, 'SIGKILL'); child.emit('close', null, 'SIGKILL'); }
    if (event === 'exit' || event === 'SIGTERM') hooks[event]();
    else await closer.close();
    assert.equal(await readFile(join(dir, 'authority'), 'utf8'), 'synthetic-private-authority');
  });
}

test('H1 default demo disposal remains unchanged; opt-in disposal needs explicit proof', async t => {
  const { demoCloser } = await lifecyclePorts();
  for (const recovery of [undefined, { canDispose: () => true }]) {
    const dir = await mkdtemp(join(tmpdir(), 'vecnet-ui-data-'));
    t.after(() => rm(dir, { recursive: true, force: true }));
    const child = new EventEmitter(); child.pid = 123456789; child.exitCode = null; child.signalCode = null;
    const closer = demoCloser(child, dir, { recovery, signal() { child.exitCode = 0; child.emit('exit', 0); child.emit('close', 0); } });
    await closer.close();
    await assert.rejects(stat(dir), { code: 'ENOENT' });
  }
});

test('message scroll reaches the viewport while live updates replace message nodes', async () => {
  const browser = await (await chromium()).launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    page.setDefaultTimeout(3000);
    await page.setContent('<div style="height:2000px"></div><article id="m-target" style="height:300px">message</article>');
    // Thread rendering replaces .msg nodes on resource notifications. Replace
    // at every animation frame so the detached-node race is deterministic.
    await page.evaluate(() => {
      window.rebuilds = 0;
      const rebuild = () => {
        const message = document.getElementById('m-target');
        message.replaceWith(message.cloneNode(true));
        window.rebuilds++;
        if (window.rebuilds < 24) requestAnimationFrame(rebuild);
      };
      requestAnimationFrame(rebuild);
    });
    await page.waitForFunction(() => window.rebuilds >= 2);
    await scrollMessageIntoView(page, 'target');
    assert.ok(await page.evaluate(() => {
      const r = document.getElementById('m-target').getBoundingClientRect();
      return r.width > 0 && r.height > 0 && r.top < innerHeight && r.bottom > 0;
    }), 'the current message node is outside the viewport');
  } finally {
    await browser.close();
  }
});

test('quiet waits for a slow snapshot and the request its render starts, not for streams or held requests', async () => {
  const browser = await (await chromium()).launch({ headless: true });
  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    const net = networkQuiet(page);
    let releaseHeld;
    const held = new Promise(resolve => { releaseHeld = resolve; });
    await page.route('http://vn.test/**', async route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/v1/admin/activity' || path === '/held') { await held; return route.fulfill({ body: '' }); }
      await new Promise(resolve => setTimeout(resolve, path === '/snapshot' ? 400 : 200));
      await route.fulfill({ body: path, headers: { 'Access-Control-Allow-Origin': '*' } });
    });
    await page.goto('http://vn.test/');
    const unignore = net.ignore(req => req.url().endsWith('/held'));
    await page.evaluate(() => {
      fetch('/v1/admin/activity');
      fetch('/v1/events?wait=25');
      fetch('/held');
      // The snapshot's render asks for one more resource, as a thread's first
      // draw asks for its aside.
      fetch('/snapshot').then(r => r.text()).then(t => {
        document.body.textContent = t;
        return fetch('/avatar').then(r => r.text()).then(a => { document.body.textContent += ' ' + a; });
      });
    });
    await net.quiet({ timeout: 5000 });
    assert.equal(await page.evaluate(() => document.body.textContent), '/snapshot /avatar');
    unignore();
    await assert.rejects(net.quiet({ timeout: 600 }), /never went quiet: GET \/held/);
    releaseHeld();
    await net.quiet({ timeout: 5000 });
  } finally {
    await browser.close();
  }
});
