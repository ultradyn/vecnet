// R2 blobs Task 7: direct uploads and downloads, attachment expiry and trust,
// retention in Settings and the storage quota, in a real browser against
// fixture services only. The demo hub (disk) serves the app, history and
// Settings; the R2 hub surfaces (capabilities, upload grants, completion,
// download grants) are page routes over an in-script grant table; and the
// provider is a real local HTTP server that checks what R2 checks: CORS for
// the hub origin only, the signed checksum, create-only PUT and the URL
// deadline. The page's CSP gains exactly that fixture origin, as
// internal/ui.Policy adds a configured R2 endpoint. No live hub, no cloud.
//
// Shots: r2-<state>-<1440|390>.png in docs/plans/shots/oui: compose-progress
// (queued, uploading, verifying), compose-done (ready, failed), thread-states
// and files (available, expired, kept, purged), fulltext (a spilled message
// still expands, and an R2 attachment's deadline does not disable that
// expansion), settings-retention, settings-off-confirm, settings-quota-full.
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { assert, cleanBuild, scrollIntoViewStable, withDemo } from './lib.mjs';

const SIZES = { 1440: { width: 1440, height: 900 }, 390: { width: 390, height: 844 } };
const GiB = 2 ** 30;
const CLASS_S = { '1h': 3600, '6h': 21600, '12h': 43200, '1d': 86400, '7d': 604800, '30d': 2592000 };
const sha = b => createHash('sha256').update(b).digest('hex');
const b64 = hex => Buffer.from(hex, 'hex').toString('base64');
let seq = 0;
const blobId = () => 'blo_0' + String(++seq).padStart(25, '0');
const deferred = () => { let open; const p = new Promise(r => { open = r; }); return { p, open }; };
// The demo hub's clock runs ahead of this machine's (internal/opseed/demo).
// The page judges every deadline on the hub's clock, so the routed hub
// surfaces and the provider issue and check deadlines on it too.
let skew = 0;
const hubNow = () => Date.now() + skew;

// fakeS3: the provider. Refused keys answer with no CORS headers, as a
// bucket whose CORS policy does not list the origin.
async function fakeS3(hubOrigin) {
  const objects = new Map(), log = [], refuse = new Set(), held = new Map();
  const srv = createServer((req, res) => {
    const u = new URL(req.url, 'http://s3.invalid'), key = decodeURIComponent(u.pathname.slice(1));
    const cors = req.headers.origin === hubOrigin && !refuse.has(key) ? {
      'Access-Control-Allow-Origin': hubOrigin, 'Vary': 'Origin', 'Access-Control-Max-Age': '5',
      'Access-Control-Allow-Methods': 'GET, PUT, HEAD',
      'Access-Control-Allow-Headers': 'content-type, content-disposition, cache-control, if-none-match, x-amz-checksum-sha256, x-amz-meta-vecnet-checksum-profile, x-amz-meta-vecnet-checksum',
      'Access-Control-Expose-Headers': 'ETag, Content-Length, Content-Range',
    } : {};
    const chunks = [];
    req.on('data', c => chunks.push(c));
    req.on('end', async () => {
      const body = Buffer.concat(chunks);
      const entry = { method: req.method, key, headers: { ...req.headers }, length: body.length };
      log.push(entry);
      res.on('finish', () => { entry.status = res.statusCode; });
      if (req.method === 'OPTIONS') { res.writeHead(cors['Access-Control-Allow-Origin'] ? 204 : 403, cors); res.end(); return; }
      if (Number(u.searchParams.get('exp')) <= hubNow()) { res.writeHead(403, cors); res.end('<Error><Code>AccessDenied</Code></Error>'); return; }
      if (req.method === 'PUT') {
        const gate = held.get(sha(body));
        if (gate) await gate.p;
        const sum = createHash('sha256').update(body).digest('base64');
        if (req.headers['x-amz-checksum-sha256'] !== sum || Number(req.headers['content-length']) !== body.length || req.headers['if-none-match'] !== '*') { res.writeHead(400, cors); res.end('<Error><Code>BadDigest</Code></Error>'); return; }
        if (objects.has(key)) { res.writeHead(412, cors); res.end(); return; }
        objects.set(key, body);
        res.writeHead(200, { ...cors, ETag: '"' + sum + '"' });
        res.end();
        return;
      }
      if (req.method === 'GET') {
        const b = objects.get(key);
        if (!b) { res.writeHead(404, cors); res.end(); return; }
        res.writeHead(200, { ...cors, 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment', 'Cache-Control': 'private, no-store', 'Content-Length': b.length });
        res.end(b);
        return;
      }
      res.writeHead(405, cors);
      res.end();
    });
  });
  srv.listen(0, '127.0.0.1');
  await once(srv, 'listening');
  return {
    origin: `http://127.0.0.1:${srv.address().port}`, objects, log, refuse,
    // hold(hex) parks the PUT of exactly those bytes until the release runs.
    hold(hex) { const g = deferred(); held.set(hex, g); return () => { held.delete(hex); g.open(); }; },
    close: () => new Promise(r => srv.close(r)),
  };
}

let s3;
try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, allowConsole, quiet, ignore } = ctx;
    // Drain the initial document before installing interception or reloading;
    // requests left behind by that navigation cannot prepare these shots.
    await page.locator('#conn.live').waitFor();
    await quiet();
    // Refusals this script provokes on purpose: storage full, a CORS-refused
    // bucket, held completions, expired and purged files, the mint limit.
    // 412 is the provider's create-only answer to a repeated PUT of bytes that
    // already landed, which the page treats as landed and the hub verifies.
    allowConsole(/status of (403|409|410|412|429)/);
    allowConsole(/CORS policy|net::ERR_FAILED/);
    const hubOrigin = new URL(demo.url).origin;
    const inbox = await fetch(`${demo.url}/v1/admin/inbox?limit=1`, { headers: { Authorization: 'Bearer ' + demo.operator_secret } });
    assert(inbox.ok, `inbox for the hub clock: ${inbox.status}`);
    const hubAt = Date.parse((await inbox.json()).now);
    assert(Number.isFinite(hubAt), 'the hub reports its clock');
    skew = hubAt - Date.now();
    s3 = await fakeS3(hubOrigin);
    const grants = new Map(), byKey = new Map();
    const R = { retention: '1d', storage: { retained_bytes: 1932735283, reserved_bytes: 107374182, used_bytes: 2040109465, limit_bytes: 5 * GiB }, full: new Set(), blocked: new Set(), rate: 0, completeGate: null, durable: false };
    const json = (route, status, body, headers = {}) => route.fulfill({ status, contentType: 'application/json', headers: { 'Cache-Control': 'no-store', ...headers }, body: JSON.stringify(body) });
    const err = (route, status, code, details = {}, headers = {}) => json(route, status, { error: { code, message: code.replace(/_/g, ' '), details } }, headers);
    const keyFor = (cls, id) => 'attachments/v1/' + (cls === 'keep' ? 'keep/ns/' : `exp/${cls}/ns/`) + id;
    const integrity = (hex, verified) => ({ sha256_verified: verified, integrity: { algorithm: 'sha256', value: hex, verified } });
    const authed = route => route.request().headers().authorization === 'Bearer ' + demo.operator_secret;

    // ---------- the page: CSP connect-src gains exactly the fixture origin ----------
    await page.route(u => new URL(u).pathname === '/ui/', async route => {
      const response = await route.fetch();
      const headers = { ...response.headers() };
      headers['content-security-policy'] = headers['content-security-policy'].replace("connect-src 'self';", `connect-src 'self' ${s3.origin};`);
      await route.fulfill({ response, headers });
    });

    // ---------- R2 hub surfaces ----------
    await page.route(`${demo.url}/v1/blob-capabilities`, route => {
      assert(authed(route), 'capabilities without the operator bearer');
      return json(route, 200, { backend: 'r2', upload_mode: 'direct', max_size: 25 << 20, daily_quota: 500 << 20, mcp_transfer_mode: 'signed_urls', mcp_remote_bytes: false, checksum_profile: 'sha256', download_origins: [s3.origin], retention: { value: R.retention, source: 'default', revision: 4, allowed_values: Object.keys(CLASS_S).concat('off'), seconds: CLASS_S[R.retention] ?? null } });
    });
    await page.route(`${demo.url}/v1/blobs/uploads`, route => {
      const req = route.request(), body = JSON.parse(req.postData()), idem = req.headers()['idempotency-key'];
      assert(authed(route) && idem, 'grant without bearer or Idempotency-Key');
      if (byKey.has(idem)) return issue(route, byKey.get(idem));
      if (R.full.has(body.sha256)) return err(route, 429, 'blob_storage_quota_exceeded', {});
      const id = blobId(), cls = R.retention === 'off' ? 'keep' : R.retention;
      grants.set(id, { id, sha256: body.sha256, size: body.size, media: body.media_type, cls, expires: cls === 'keep' ? null : new Date(hubNow() + CLASS_S[cls] * 1000).toISOString(), key: keyFor(cls, id), complete: false });
      if (R.blocked.has(body.sha256)) s3.refuse.add(keyFor(cls, id));
      byKey.set(idem, id);
      return issue(route, id);
    });
    function issue(route, id) {
      const g = grants.get(id), exp = hubNow() + 300000, sum = b64(g.sha256);
      return json(route, 201, {
        blob_id: id, sha256: g.sha256, ...integrity(g.sha256, false), size: g.size, retention_class: g.cls, expires_at: g.expires,
        upload: { method: 'PUT', url: `${s3.origin}/${g.key}?X-Amz-Expires=300&X-Amz-Signature=fixture&exp=${exp}`, expires_at: new Date(exp).toISOString(), headers: { 'Content-Length': String(g.size), 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment', 'Cache-Control': 'private, no-store', 'If-None-Match': '*', 'x-amz-checksum-sha256': sum, 'x-amz-meta-vecnet-checksum-profile': 'sha256', 'x-amz-meta-vecnet-checksum': sum } },
        complete_url: `/v1/blobs/uploads/${id}/complete`,
      });
    }
    await page.route(u => /^\/v1\/blobs\/uploads\/[^/]+\/complete$/.test(new URL(u).pathname), async route => {
      const id = new URL(route.request().url()).pathname.split('/')[4], g = grants.get(id);
      if (R.completeGate) await R.completeGate.p;
      if (!g || !s3.objects.has(g.key)) return err(route, 409, 'upload_incomplete');
      g.complete = true;
      return json(route, 200, { blob_id: id, sha256: g.sha256, ...integrity(g.sha256, true), size: g.size, retention_class: g.cls, expires_at: g.expires });
    });
    await page.route(u => /^\/v1\/blobs\/[0-9a-f]{64}\/download$/.test(new URL(u).pathname), route => {
      assert(authed(route), 'download grant without the operator bearer');
      const u = new URL(route.request().url()), g = grants.get(u.searchParams.get('blob_id'));
      if (!g || g.sha256 !== u.pathname.split('/')[3]) return err(route, 422, 'unknown_blob');
      if (g.state === 'purged') return err(route, 410, 'blob_purged');
      if (g.state === 'expired' || (g.expires && Date.parse(g.expires) <= hubNow())) return err(route, 410, 'blob_expired');
      if (R.rate) return err(route, 429, 'blob_url_rate_limited', { retry_after_seconds: R.rate }, { 'Retry-After': String(R.rate) });
      const exp = Math.min(hubNow() + 600000, g.expires ? Date.parse(g.expires) : Infinity);
      return json(route, 200, { method: 'GET', url: `${s3.origin}/${g.key}?X-Amz-Expires=600&X-Amz-Signature=fixture&exp=${exp}`, expires_at: new Date(exp).toISOString(), object_expires_at: g.expires, blob_id: g.id, sha256: g.sha256, ...integrity(g.sha256, true), size: g.size, media_type: g.media, retention_class: g.cls });
    });

    // ---------- a delivered message with four R2 files (fixture history) ----------
    const thr = demo.threads.ticket18;
    const seedFile = (name, media, content, cls, state, offsetMs) => {
      const id = blobId(), buf = Buffer.from(content), hex = sha(buf);
      const expires = cls === 'keep' ? null : new Date(hubNow() + offsetMs).toISOString();
      grants.set(id, { id, sha256: hex, size: buf.length, media, cls, expires, key: keyFor(cls, id), complete: true, state, name });
      if (state !== 'purged') s3.objects.set(keyFor(cls, id), buf);
      return grants.get(id);
    };
    const files = [
      seedFile('nightly-build.log', 'text/plain', 'build 2026-11-06: 1,204 tests, 0 failures\n'.repeat(40), '1d', 'available', 6 * 3600 * 1000),
      seedFile('coverage-report.html', 'text/html', '<html><script>alert("coverage")</script></html>', '1d', 'expired', -3 * 3600 * 1000),
      seedFile('diagram‮gvs.png', 'image/svg+xml', '<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(2)</script></svg>', 'keep', 'available', 0),
      seedFile('heap-dump.bin', 'application/octet-stream', 'x'.repeat(4096), '7d', 'purged', 4 * 86400 * 1000),
    ];
    const att = g => ({ blob_id: g.id, sha256: g.sha256, size: g.size, media_type: g.media, name: g.name, retention_class: g.cls, expires_at: g.expires, ...integrity(g.sha256, true) });
    const stateOf = g => ({ state: g.state, retention_class: g.cls, expires_at: g.expires, ...integrity(g.sha256, true) });
    const MSG = 'msg_01R2FIXTURE000000000000001';
    let sender = '';
    await page.route(u => new URL(u).pathname === `/v1/threads/${thr}/messages`, async route => {
      const response = await route.fetch(), j = await response.json();
      if (!j.next_after && Array.isArray(j.messages)) {
        const tpl = j.messages.find(m => m.sender !== demo.operator_peer && m.kind === 'message' && !m.approval_request && !m.job_request && !m.job_status) || j.messages[0];
        sender = tpl.sender;
        j.messages.push({ ...tpl, message_id: MSG, created_at: new Date(hubNow() - 120000).toISOString(), content: { text: 'Nightly artefacts: the log is fresh, coverage hit the default 24-hour retention, the diagram is kept, and the heap dump was purged.' }, attachments: files.map(att), reactions: undefined, in_reply_to: undefined, metadata: undefined });
        j.blob_states = { ...(j.blob_states || {}), ...Object.fromEntries(files.map(g => [g.id, stateOf(g)])) };
      }
      await route.fulfill({ response, json: j });
    });
    await page.route(u => new URL(u).pathname === `/v1/admin/threads/${thr}`, async route => {
      const response = await route.fetch(), j = await response.json();
      if (Array.isArray(j.files)) j.files.push(...files.map(g => ({ blob_id: g.id, sha256: g.sha256, name: g.name, media_type: g.media, message_id: MSG, sender: sender || j.files[0]?.sender || '', created_at: new Date(hubNow() - 120000).toISOString(), size: g.size, spilled: false, purged: g.state === 'purged' })));
      await route.fulfill({ response, json: j });
    });

    // A durable spill (SQLite text, not an R2 object) beside an expiring R2
    // attachment. The deadline is stamped when the page actually loads it,
    // after the disk-spill shot, so that shot is unchanged.
    const DURABLE_MSG = 'msg_01R2DURABLE00000000000001';
    const durableBody = 'Durable full text stays readable after the attachment expires.\n' + 'kept line\n'.repeat(40);
    const durableSha = sha(Buffer.from(durableBody));
    const held = {};
    const expiringGrant = () => {
      if (!held.g) held.g = seedFile('session-trace.txt', 'text/plain', 'session trace bytes\n'.repeat(12), '1h', 'available', 12000);
      return held.g;
    };
    // Metadata for the file if the panel asks before history publishes it,
    // so the real disk hub is never asked for a fixture id (that would 404).
    await page.route(u => R.durable && held.g && new URL(u).pathname === `/v1/blobs/${held.g.sha256}/status`, route => json(route, 200, stateOf(held.g)));
    await page.route(u => R.durable && new URL(u).pathname === `/v1/blobs/${durableSha}`, route => {
      return route.fulfill({ status: 200, contentType: 'text/markdown', body: durableBody, headers: { 'Cache-Control': 'private, no-store' } });
    });
    await page.route(u => R.durable && new URL(u).pathname === `/v1/threads/${demo.threads.hark}/messages`, async route => {
      const response = await route.fetch();
      const j = await response.json();
      const g = expiringGrant();
      if (!j.next_after && Array.isArray(j.messages) && !j.messages.some(m => m.message_id === DURABLE_MSG)) {
        const tpl = j.messages.find(m => m.kind === 'message' && !m.approval_request && !m.job_request && !m.job_status) || j.messages[0];
        j.messages.push({
          ...tpl, message_id: DURABLE_MSG, created_at: new Date(hubNow() - 60000).toISOString(),
          content: { text: 'Preview only. The rest is durable.', truncated: true },
          metadata: { 'vecnet.spilled': true },
          attachments: [att(g), { sha256: durableSha, size: Buffer.byteLength(durableBody), media_type: 'text/markdown', name: 'message.md' }],
          approval_request: undefined, approval_response: undefined, job_request: undefined, job_status: undefined,
          reactions: undefined, in_reply_to: undefined,
        });
        j.blob_states = { ...(j.blob_states || {}), [g.id]: stateOf(g) };
      }
      await route.fulfill({ response, json: j });
    });
    await page.route(u => R.durable && new URL(u).pathname === `/v1/admin/threads/${demo.threads.hark}`, async route => {
      const response = await route.fetch();
      const j = await response.json();
      const g = expiringGrant();
      if (Array.isArray(j.files) && !j.files.some(f => f.blob_id === g.id)) {
        j.files.push({ blob_id: g.id, sha256: g.sha256, name: g.name, media_type: g.media, message_id: DURABLE_MSG, sender: j.files[0]?.sender || '', created_at: new Date(hubNow() - 60000).toISOString(), size: g.size, spilled: false, purged: false });
      }
      await route.fulfill({ response, json: j });
    });

    // ---------- Settings: the R2 expiry adapter installed, with storage ----------
    await page.route(u => new URL(u).pathname === '/v1/admin/blobs/storage', route => {
      assert(authed(route), 'storage without the operator bearer');
      return json(route, 200, { storage: { ...R.storage } });
    });
    await page.route(u => new URL(u).pathname === '/v1/admin/prefs', async route => {
      if (route.request().method() !== 'GET') return route.continue();
      const response = await route.fetch();
      if (response.status() !== 200) return route.fulfill({ response });
      const j = await response.json();
      for (const p of j.preferences) if (p.key === 'blobs.expiry') { Object.assign(p, { editable: true, runtime: 'live', value: R.retention, source: R.retention === '1d' ? 'default' : 'stored' }); delete p.unavailable_reason; }
      j.storage = { ...R.storage };
      await route.fulfill({ response, json: j });
    });
    await page.route(u => new URL(u).pathname === '/v1/admin/prefs/preview', async route => {
      const body = JSON.parse(route.request().postData() || '{}');
      if (!body.set || body.set['blobs.expiry'] !== 'off') return route.continue();
      return json(route, 200, { base_revision: 4, risks: [{ code: 'blob_expiry_off', keys: ['blobs.expiry'], message: 'New attachments have no disaster expiry; retained and reserved storage incurs cost within the verified finite quota shown as retained, reserved, used and limit bytes. Estimates are not guarantees.' }], impact: { storage: { ...R.storage } }, confirm_token: 'fixture-preview-token', expires_at: new Date(hubNow() + 300000).toISOString() });
    });

    // On a failure, say where the transfer stopped (no URLs: keys and states only).
    const consoleLines = [];
    page.on('console', m => consoleLines.push(m.type() + ': ' + m.text().replace(/X-Amz-Signature=[^&\s]*/g, 'X-Amz-Signature=…')));
    ctx.diagnose = async () => {
      const chipStates = await page.locator('.up-chip').evaluateAll(els => els.map(e => e.className + ' :: ' + e.textContent)).catch(() => []);
      console.error('chips:', chipStates);
      console.error('provider:', s3.log.map(r => `${r.method} ${r.key} ${r.headers.origin || ''}`));
      console.error('console:', consoleLines.slice(-20));
    };
    const frames = () => page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
    const both = async (name, prep) => {
      for (const [w, vp] of Object.entries(SIZES)) {
        await page.setViewportSize(vp);
        await quiet();
        if (prep) await prep(w);
        await quiet();
        await frames();
        const sideways = await page.evaluate(() => document.documentElement.scrollWidth);
        assert(sideways <= vp.width, `${name}: scrolls sideways at ${vp.width} px (${sideways})`);
        await shot(`r2-${name}-${w}`);
      }
      await page.setViewportSize(SIZES[1440]);
    };
    const go = async hash => { await page.evaluate(h => { location.hash = h; }, hash); };

    // The fixture provider listens on loopback; a routed document is not, so
    // Chromium's Local Network Access would refuse it. Real R2 is a public
    // https origin, where this permission never applies; grant it to the hub
    // page alone, for the fixture.
    await page.context().grantPermissions(['local-network-access'], { origin: hubOrigin });
    await page.reload({ waitUntil: 'load' });
    const csp = await page.evaluate(async () => {
      const r = await fetch('/ui/', { cache: 'no-store' });
      await r.text();
      return r.headers.get('content-security-policy');
    });
    assert(csp.includes(`connect-src 'self' ${s3.origin};`) && !csp.includes('*'), 'CSP carries exactly the fixture origin: ' + csp);
    await page.locator('#conn.live').waitFor();

    // ---------- compose: queued, uploading, verifying ----------
    await go(`#thread/${thr}`);
    await page.locator(`[id="m-${MSG}"]`).waitFor();
    const picker = page.locator('.composer input[type=file]');
    const fileOf = (name, mimeType, text) => ({ name, mimeType, buffer: Buffer.from(text) });
    const upA = fileOf('release-notes.md', 'text/markdown', '# Release 0.9\n\n- Direct uploads to R2\n'.repeat(30));
    const upB = fileOf('screenshot-1440.png', 'image/png', 'PNG-ish bytes for the fixture '.repeat(200));
    const upC = fileOf('logs.tar.zst', 'application/zstd', 'zstd frames '.repeat(500));
    const upD = fileOf('bucket-blocked.csv', 'text/csv', 'a,b,c\n'.repeat(200));
    R.full.add(sha(upC.buffer));
    R.blocked.add(sha(upD.buffer));
    // Two at a time: A's PUT lands and its completion is held (verifying),
    // B's PUT is held at the provider (uploading), C and D wait (queued).
    R.completeGate = deferred();
    const releaseB = s3.hold(sha(upB.buffer));
    // These transfers are deliberately parked for the progress shots. All
    // snapshots still need to settle before either viewport is captured.
    const watchUploads = ignore(req => (req.method() === 'PUT' && new URL(req.url()).origin === s3.origin) || /^\/v1\/blobs\/uploads\/[^/]+\/complete$/.test(new URL(req.url()).pathname));
    await picker.setInputFiles([upA, upB, upC, upD]);
    await page.locator('.up-chip.up-verifying', { hasText: 'release-notes.md' }).waitFor({ timeout: 20000 }).catch(async e => { await ctx.diagnose(); throw e; });
    await page.locator('.up-chip.up-uploading', { hasText: 'screenshot-1440.png' }).waitFor();
    await page.locator('.up-chip.up-queued').nth(1).waitFor();
    await both('compose-progress', () => scrollIntoViewStable(page, '.composer'));
    // Then: A and B ready; C refused (storage full), D refused by the bucket's CORS.
    releaseB();
    R.completeGate.open();
    R.completeGate = null;
    watchUploads();
    await page.locator('.up-chip.up-failed').nth(1).waitFor();
    await page.locator('.up-chip.up-done').nth(1).waitFor();
    const chips = await page.locator('.up-chip').allTextContents();
    assert(chips.some(t => /expires in 24 h/.test(t)), 'a ready chip says how long its file lives: ' + chips.join(' | '));
    assert(chips.some(t => /Hub storage is full/.test(t)), 'storage full failure shown');
    assert(chips.some(t => /CORS/.test(t)), 'CORS refusal shown');
    await both('compose-done', () => scrollIntoViewStable(page, '.composer'));

    // The provider saw the browser's own requests: no bearer, no cookies, exact lengths.
    const puts = s3.log.filter(r => r.method === 'PUT');
    assert(puts.length >= 2, 'PUTs reached the provider');
    for (const p of puts) {
      assert(!p.headers.authorization && !p.headers.cookie, 'credentials leaked to the provider');
      assert(Number(p.headers['content-length']) === p.length, 'Content-Length is the exact body length');
      assert(p.headers.origin === hubOrigin, 'the PUT came from the hub page');
    }
    // A retry of the CORS-refused chip keeps its grant (no second reservation).
    const grantsBefore = grants.size;
    s3.refuse.clear();
    await page.locator('.up-chip.up-failed', { hasText: 'bucket-blocked.csv' }).getByRole('button', { name: 'Retry' }).click();
    await page.locator('.up-chip.up-done', { hasText: 'bucket-blocked.csv' }).waitFor();
    assert(grants.size === grantsBefore, 'a retry after a refused PUT reuses its grant');

    // ---------- thread and Files: available, expired, kept, purged ----------
    const msg = page.locator(`[id="m-${MSG}"]`);
    await msg.locator('.att.r2').nth(3).waitFor();
    const chipText = await msg.locator('.atts').innerText();
    for (const want of ['Expires in 6 h', 'Attachment expired (24-hour retention)', 'Kept while referenced', 'Attachment purged']) assert(chipText.includes(want), `thread chip "${want}" missing: ${chipText}`);
    assert(await msg.locator('img').count() === 0, 'no R2 attachment renders inline, the SVG included');
    await both('thread-states', async w => {
      if (w === '390') await page.locator('.aside-toggle').evaluate(b => { if (b.getAttribute('aria-expanded') === 'true') b.click(); });
      await scrollIntoViewStable(page, `[id="m-${MSG}"]`);
    });
    const filesPanel = page.locator('aside .files');
    await filesPanel.locator('.file.r2').nth(3).waitFor();
    const rowText = await filesPanel.innerText();
    for (const want of ['Attachment expired (24-hour retention)', 'Kept while referenced', 'Attachment purged']) assert(rowText.includes(want), `Files row "${want}" missing`);
    assert(await filesPanel.locator('.file.r2.s-expired').isDisabled(), 'an expired file has no download action');
    assert(await filesPanel.locator('.file.r2.s-purged').isDisabled(), 'a purged file has no download action');

    // Download: a fresh grant, a credential-free GET, a saved file.
    const gets = () => s3.log.filter(r => r.method === 'GET');
    const before = gets().length;
    const [download] = await Promise.all([page.waitForEvent('download'), filesPanel.locator('.file.r2', { hasText: 'nightly-build.log' }).click()]);
    assert(download.suggestedFilename() === 'nightly-build.log', 'saved under its cleaned name');
    const got = gets().slice(before);
    assert(got.length === 1 && !got[0].headers.authorization && !got[0].headers.cookie && !got[0].headers.referer, 'the provider GET carried no credentials or referrer');
    // The kept SVG downloads as a file under a cleaned name; it never renders.
    const [svg] = await Promise.all([page.waitForEvent('download'), filesPanel.locator('.file.r2.s-available', { hasText: 'diagram' }).click()]);
    assert(!/[‪-‮⁦-⁩]/.test(svg.suggestedFilename()), 'bidi controls stripped from the saved name');
    // The mint limit: a retry delay, no URL, nothing fetched.
    R.rate = 23;
    const beforeRate = gets().length;
    await filesPanel.locator('.file.r2', { hasText: 'nightly-build.log' }).click();
    await page.locator('#toast.on', { hasText: 'try again in 23 s' }).waitFor();
    assert(gets().length === beforeRate, 'a rate-limited click fetched nothing');
    R.rate = 0;
    await both('files', async w => {
      if (w === '390') {
        const t = page.locator('.aside-toggle');
        if (await t.getAttribute('aria-expanded') !== 'true') await t.click();
      }
      await scrollIntoViewStable(page, 'aside .files');
    });

    // A refusal toast stays 6 s; later screens are shot without it.
    await page.waitForFunction(() => !document.getElementById('toast').classList.contains('on'), null, { timeout: 15000 });

    // ---------- a spilled message still expands (durable text, not R2) ----------
    await go(`#thread/${demo.threads.hark}`);
    const show = page.locator('button.showfull').first();
    await show.waitFor();
    await show.click();
    await page.locator('.full').first().waitFor();
    await both('fulltext', () => scrollIntoViewStable(page, '.full'));

    // The same thread, reloaded with a durable spill plus an R2 attachment
    // whose deadline is a few seconds out. After it crosses, download is
    // disabled and Show full text still expands the durable body.
    R.durable = true;
    await page.reload({ waitUntil: 'load' });
    await page.locator('#conn.live').waitFor();
    await go(`#thread/${demo.threads.hark}`);
    const durable = page.locator(`[id="m-${DURABLE_MSG}"]`);
    await durable.waitFor();
    const chip = durable.locator('.att.r2', { hasText: 'session-trace.txt' });
    await chip.waitFor();
    assert(!/expired/i.test(await chip.innerText()), 'the attachment is live when the thread opens: ' + await chip.innerText());
    const fileBtn = page.locator(`aside .file.r2[data-blob="${held.g.id}"]`);
    await fileBtn.waitFor();
    assert(await fileBtn.isEnabled(), 'download is enabled before the deadline');
    await page.waitForFunction(({ id, msg }) => {
      const b = document.querySelector(`aside .file.r2[data-blob="${id}"]`);
      const chip = document.querySelector(`[id="m-${msg}"] .att.r2`);
      return b && b.disabled && b.className.includes('s-expired') && chip && /expired/i.test(chip.textContent);
    }, { id: held.g.id, msg: DURABLE_MSG }, { timeout: 20000 });
    await durable.locator('button.showfull').click();
    const full = durable.locator('.full');
    await full.waitFor();
    assert((await full.innerText()).includes('Durable full text stays readable'), 'durable full text still expands');
    assert(await fileBtn.isDisabled(), 'the expired attachment stays disabled while the text is open');

    // ---------- Settings: retention selector, off confirmation, quota full ----------
    await go('#settings');
    const row = '[data-row="blobs.expiry"]';
    await page.locator(row).first().waitFor();
    await quiet();
    const opts = await page.locator(row).first().locator('select option').allTextContents();
    assert(opts.join(',') === '1 hour,6 hours,12 hours,1 day,7 days,30 days,Off (keep files)', 'six durations and off: ' + opts);
    assert(/new uploads only/i.test(await page.locator(row).first().innerText()), 'new-uploads-only explained');
    await both('settings-retention', () => scrollIntoViewStable(page, row));
    await page.locator(row).first().locator('select').selectOption('off');
    await page.locator('[data-fk=save]').click();
    const dlg = page.locator('dialog.st-cf[open]');
    await dlg.waitFor();
    const dlgText = await dlg.innerText();
    assert(/no automatic expiry/i.test(dlgText) && /used of a 5\.4 GB limit/.test(dlgText), 'off confirmation shows the hub figures');
    await both('settings-off-confirm');
    await dlg.locator('[data-cf=no]').click();
    await page.locator('dialog.st-cf[open]').waitFor({ state: 'detached' }).catch(() => {});
    await page.locator('[data-fk=undo]').click();
    R.storage = { retained_bytes: 5 * GiB - 52428800, reserved_bytes: 104857600, used_bytes: 5 * GiB + 52428800, limit_bytes: 5 * GiB };
    await page.reload({ waitUntil: 'load' });
    await page.locator('#conn.live').waitFor();
    await quiet();
    await page.locator('[data-storage=full]').first().waitFor();
    assert(/over the limit/i.test(await page.locator('[data-storage=full]').first().innerText()), 'quota over the limit explained');
    await both('settings-quota-full', () => scrollIntoViewStable(page, row));
  });
  console.log('r2-blobs: ok; provider saw ' + s3.log.map(r => r.method + ' ' + r.status).join(', '));
} finally {
  await s3?.close();
  await cleanBuild();
}
