// Manual provider acceptance helper. Grants only enter browser memory through a
// local same-origin endpoint; never log console, errors, requests or traces.
let browser;
let stage = 'launch';
try {
  const { chromium } = await import(process.env.PLAYWRIGHT_MODULE);
  const [allowed, disallowed, path, resolve] = process.argv.slice(2);
  browser = await chromium.launch({ headless: true, args: [`--host-resolver-rules=${resolve}`] });
  const context = await browser.newContext({ serviceWorkers: 'block' });
  const page = await context.newPage();
  page.setDefaultTimeout(20000);
  stage = 'allowed_navigation';
  await page.goto(allowed, { waitUntil: 'load' });
  stage = 'allowed_fetch';
  const result = await page.evaluate(async (path) => {
    const g = await (await fetch(path, { cache: 'no-store' })).json();
    const options = { mode: 'cors', credentials: 'omit', redirect: 'error' };
    const blob = new Blob([g.body], { type: 'application/octet-stream' });
    const put = await fetch(g.put, { ...options, method: 'PUT', headers: g.headers, body: blob });
    const get = await fetch(g.get, options);
    const body = await get.text();
    const range = await fetch(g.get, { ...options, headers: { Range: 'bytes=0-5' } });
    const partial = await range.text();
    return {
      put_http: put.status,
      put_etag: put.headers.get('etag') === g.etag,
      put_checksum: put.headers.get('x-amz-checksum-sha256') === g.checksum,
      get_http: get.status,
      get_body: body === g.body,
      get_etag: get.headers.get('etag') === g.etag,
      get_checksum: get.headers.get('x-amz-checksum-sha256') === g.checksum,
      get_length: get.headers.get('content-length') === String(blob.size),
      range_http: range.status,
      range_body: partial === g.body.slice(0, 6),
      content_range: range.headers.get('content-range') === `bytes 0-5/${blob.size}`,
      accept_ranges: range.headers.get('accept-ranges') === 'bytes',
    };
  }, path);
  // New page/origin; use the same valid grants to distinguish CORS denial from
  // invalid signatures. A forbidden full-header PUT requires preflight.
  const blockedPage = await context.newPage();
  stage = 'disallowed_navigation';
  await blockedPage.goto(disallowed, { waitUntil: 'load' });
  stage = 'disallowed_fetch';
  Object.assign(result, await blockedPage.evaluate(async (path) => {
    const g = await (await fetch(path, { cache: 'no-store' })).json();
    const options = { mode: 'cors', credentials: 'omit', redirect: 'error' };
    const blocked = async (url, options) => {
      try { await fetch(url, options); return false; }
      catch (err) { return err instanceof TypeError; }
    };
    return {
      blocked_get: await blocked(g.get, options),
      blocked_put: await blocked(g.put, { ...options, method: 'PUT', headers: g.headers, body: new Blob([g.body]) }),
    };
  }, path));
  result.version = browser.version();
  stage = 'close';
  await browser.close();
  browser = null;
  process.stdout.write(JSON.stringify(result));
} catch {
  if (browser) await browser.close().catch(() => {});
  // Never include exception text: Playwright errors can contain presigned URLs.
  process.stdout.write(JSON.stringify({ failed_stage: stage }));
}
