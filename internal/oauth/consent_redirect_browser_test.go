//go:build oauthbrowser

package oauth_test

// This opt-in browser regression requires the preinstalled pinned Playwright
// fixture. The oauthbrowser tag keeps ordinary tests independent of Chromium.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestConsentRedirectBrowser(t *testing.T) {
	if os.Getenv("PLAYWRIGHT_MODULE") == "" {
		t.Fatal("explicit oauthbrowser gate requires preinstalled PLAYWRIGHT_MODULE; no implicit install")
	}
	for _, mode := range []string{"direct", "canonical", "forbidden"} {
		for _, action := range []string{"approve", "deny"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				env := testhub.Start(t)
				const redirect = "https://www.cursor.com/agents/mcp/oauth/callback"
				b := &browser{t: t, env: env, client: register(t, env, redirect), redirect: redirect}
				link := ""
				if action == "approve" {
					link = linkCode(t, env, newDot(t, env, "browser-draft"))
				}
				input, err := json.Marshal(map[string]string{
					"hub": env.URL, "authorize": env.URL + "/oauth/authorize?" + b.query(verifierA).Encode(),
					"redirect": redirect, "client": b.client, "verifier": verifierA,
					"action": action, "link": link, "mode": mode,
				})
				if err != nil {
					t.Fatal("encode synthetic fixture")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", consentBrowserScript)
				cmd.Stdin = bytes.NewReader(input)
				// Browser exception strings can contain authorization URLs.
				// Emit counts/stages and sanitized policy/origin/path diagnostics.
				cmd.Stderr = io.Discard
				out, runErr := cmd.Output()
				var result struct {
					OK               bool     `json:"ok"`
					Stage            string   `json:"stage"`
					Violations       int      `json:"violations"`
					BHits            int      `json:"bHits"`
					CHits            int      `json:"cHits"`
					DHits            int      `json:"dHits"`
					ProxyAttempts    int      `json:"proxyAttempts"`
					Policies         []string `json:"policies"`
					ViolationDetails []struct {
						Policy string `json:"policy"`
						Origin string `json:"origin"`
						Path   string `json:"path"`
					} `json:"violationDetails"`
				}
				if json.Unmarshal(out, &result) != nil {
					t.Fatal("browser fixture unavailable or returned no structured result; do not log raw output")
				}
				// Only sanitized policy and origin/path diagnostics cross into logs.
				t.Logf("consent policies=%q violation details=%+v proxy attempts=%d", result.Policies, result.ViolationDetails, result.ProxyAttempts)
				if runErr != nil || !result.OK || result.ProxyAttempts != 0 {
					t.Fatalf("browser stage=%s form violations=%d www=%d apex=%d forbidden=%d",
						result.Stage, result.Violations, result.BHits, result.CHits, result.DHits)
				}
			})
		}
	}
}

const consentBrowserScript = `
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
import { createServer } from 'node:net';
const f = JSON.parse(readFileSync(0, 'utf8'));
const B = 'https://www.cursor.com', C = 'https://cursor.com', D = 'https://unrelated.example';
const path = '/agents/mcp/oauth/callback';
const marker = '<p id="finished">Complete</p>';
const r = { ok: false, stage: 'module', violations: 0, bHits: 0, cHits: 0, dHits: 0, proxyAttempts: 0, policies: [], violationDetails: [] };
let browser, denyProxy, code = '', callbackOK = false, hub302 = false, violationSeen, plumbingFailed = false;
const inflight = new Set();
function safeLocation(raw) {
  try {
    const u = new URL(raw);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return { origin: 'non-url', path: '' };
    return { origin: u.origin, path: u.pathname };
  }
  catch { return { origin: 'non-url', path: '' }; }
}
function safePolicy(raw) {
  return raw.split(';').map(directive => directive.trim().split(/\s+/).map((token, i) => {
    if (i === 0) return /^[a-z-]+$/.test(token) ? token : '<omitted>';
    if (/^https?:\/\//.test(token)) {
      const u = safeLocation(token);
      return u.origin + (u.path === '/' ? '' : u.path);
    }
    return /^(?:'(?:self|none|unsafe-inline|unsafe-eval)'|\*|https?:)$/.test(token) ? token : '<omitted>';
  }).join(' ')).join('; ');
}
try {
  const { chromium } = await import(pathToFileURL(resolve(process.env.PLAYWRIGHT_MODULE)).href);
  r.stage = 'loopback-isolation';
  const hub = new URL(f.hub);
  if (hub.protocol !== 'http:' || hub.hostname !== '127.0.0.1' || !hub.port || hub.origin !== f.hub || hub.username || hub.password) throw new Error('hub-fixture');
  // Routing alone does not isolate redirect chains: Playwright can continue
  // later URLs without invoking the user route handler. An independent local
  // deny proxy prevents such a request from reaching any external destination.
  denyProxy = createServer(socket => { r.proxyAttempts++; socket.destroy(); });
  await new Promise((resolve, reject) => {
    denyProxy.once('error', reject);
    denyProxy.listen(0, '127.0.0.1', resolve);
  });
  r.stage = 'launch';
  browser = await chromium.launch({ headless: true, args: ['--disable-background-networking'], proxy: {
    server: 'http://127.0.0.1:' + denyProxy.address().port, bypass: '127.0.0.1'
  } });
  const context = await browser.newContext({ serviceWorkers: 'block' });
  const violation = new Promise(resolve => { violationSeen = resolve; });
  await context.exposeBinding('recordFormViolation', (_, directive, policy, blockedURI) => {
    if (directive !== 'form-action') return;
    r.violations++;
    r.violationDetails.push({ policy: safePolicy(policy), ...safeLocation(blockedURI) });
    violationSeen();
  });
  await context.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', e => window.recordFormViolation(e.effectiveDirective, e.originalPolicy, e.blockedURI));
  });
  const page = await context.newPage();
  // Use Fetch directly, without Playwright routes: its user-routing layer
  // deliberately skips redirectedFrom requests, even after a fulfilled hop.
  // Chromium still performs the native form navigation and CSP enforcement.
  const cdp = await context.newCDPSession(page);
  const reply = (requestId, status, headers, body) => cdp.send('Fetch.fulfillRequest', {
    requestId, responseCode: status, responseHeaders: headers,
    body: Buffer.from(body).toString('base64')
  });
  const html = requestId => reply(requestId, 200, [{ name: 'content-type', value: 'text/html' }], marker);
  async function intercept(event) {
    const request = event.request, u = new URL(request.url);
    if (u.origin === f.hub) {
      if (request.method === 'POST' && request.postData === undefined) throw new Error('missing-native-post-data');
      // Only the exact known loopback hub may receive an HTTP request. Never
      // let the API fetch follow its real authorization response off-host.
      const response = await context.request.fetch(request.url, {
        method: request.method, headers: request.headers, maxRedirects: 0, timeout: 5000,
        ...(request.postData === undefined ? {} : { data: request.postData })
      });
      return reply(event.requestId, response.status(), response.headersArray(), await response.body());
    }
    if (u.origin === B && u.pathname === path && request.method === 'GET') {
      r.bHits++;
      const q = u.searchParams;
      const hasReferrer = Object.entries(request.headers).some(([key, value]) => key.toLowerCase() === 'referer' && value);
      callbackOK = q.get('state') === 'st-123' && q.get('iss') === f.hub && !hasReferrer;
      if (f.action === 'approve') {
        code = q.get('code') || '';
        callbackOK &&= !!code && !q.has('error');
      } else callbackOK &&= q.get('error') === 'access_denied' && !q.has('code');
      if (f.mode === 'direct') return html(event.requestId);
      // Query preservation is synthetic, not established by the public probe.
      const location = f.mode === 'canonical' ? C + path + u.search : D + '/no-auth-data';
      return reply(event.requestId, 308, [{ name: 'location', value: location }], '');
    }
    if (u.origin === C && u.pathname === path && request.method === 'GET') {
      r.cHits++;
      return html(event.requestId);
    }
    if (u.origin === D) {
      r.dHits++;
      return html(event.requestId);
    }
    return cdp.send('Fetch.failRequest', { requestId: event.requestId, errorReason: 'BlockedByClient' });
  }
  cdp.on('Fetch.requestPaused', event => {
    const operation = intercept(event).catch(async () => {
      plumbingFailed = true;
      await cdp.send('Fetch.failRequest', { requestId: event.requestId, errorReason: 'Failed' }).catch(() => {});
    });
    inflight.add(operation);
    void operation.finally(() => inflight.delete(operation));
  });
  await cdp.send('Fetch.enable', { patterns: [{ urlPattern: '*', requestStage: 'Request' }] });
  page.on('response', response => {
    const request = response.request(), u = new URL(response.url());
    if (request.method() === 'POST' && u.origin === f.hub && u.pathname === '/oauth/authorize') hub302 = response.status() === 302;
  });
  r.stage = 'consent';
  const consent = await page.goto(f.authorize);
  if (consent.status() !== 200) throw new Error('consent');
  r.policies = (await consent.headersArray()).filter(h => h.name.toLowerCase() === 'content-security-policy').map(h => safePolicy(h.value));
  if (f.action === 'approve') await page.locator('#link_code').fill(f.link);
  r.stage = 'native-submit';
  await page.getByRole('button', { name: f.action === 'approve' ? 'Approve' : 'Deny', exact: true }).click({ noWaitAfter: true });
  r.stage = 'automatic-completion';
  if (f.mode === 'forbidden') {
    await Promise.race([violation, new Promise((_, reject) => setTimeout(() => reject(new Error('missing-violation')), 5000))]);
    if (!r.violations || r.dHits || r.cHits || await page.locator('#finished').count()) throw new Error('forbidden-destination');
  } else {
    // No extra click, script injection navigation, or manual goto is allowed.
    await page.locator('#finished').waitFor({ timeout: 5000 });
    const at = new URL(page.url()), expected = f.mode === 'direct' ? B : C;
    if (at.origin !== expected || at.pathname !== path || r.violations || r.dHits) throw new Error('automatic-destination');
    if (f.mode === 'canonical' && r.cHits !== 1) throw new Error('apex-count');
    if (f.mode === 'direct' && r.cHits !== 0) throw new Error('direct-count');
  }
  if (!hub302 || !callbackOK || r.bHits !== 1) throw new Error('callback-contract');
  if (f.action === 'approve') {
    r.stage = 'pkce-exchange';
    const token = await context.request.post(f.hub + '/oauth/token', { maxRedirects: 0, timeout: 5000, form: {
      grant_type: 'authorization_code', code, client_id: f.client,
      redirect_uri: f.redirect, code_verifier: f.verifier, resource: f.hub + '/mcp'
    } });
    const body = await token.json();
    if (token.status() !== 200 || !body.access_token) throw new Error('token');
  }
  if (r.proxyAttempts || plumbingFailed) throw new Error('fixture-network-plumbing');
  r.stage = 'complete'; r.ok = true;
} catch {
  process.exitCode = 1;
} finally {
  await browser?.close().catch(() => {});
  await Promise.allSettled([...inflight]);
  if (plumbingFailed) { r.ok = false; r.stage = 'fixture-plumbing'; process.exitCode = 1; }
  if (denyProxy?.listening) await new Promise(resolve => denyProxy.close(resolve));
  process.stdout.write(JSON.stringify(r));
}
`
