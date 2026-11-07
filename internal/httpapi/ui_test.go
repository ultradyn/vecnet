package httpapi_test

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/ui"
)

// uiMaxBytes caps the sum of every file in the UI, which ships with no build
// step (raised from 60 KiB by operator UI Task 7; to 320 KiB on 2026-11-04
// when the sidebar, activity feed and peer view took it past 256 KiB; to
// 336 KiB on 2026-11-05 for the favicon set, about 9 KiB of it images; to
// 368 KiB on 2026-11-05 when hub-notices merged with master: its hub notice and
// restart banners (about 13 KiB) on top of master's sidebar threads, unread
// counts and composer growth put the merged UI at about 354 KB; to 480 KiB on
// 2026-11-06 for hub preferences Task 6: the Settings page, its draft model,
// stylesheet and local appearance, about 106 KB, put the UI at about 483 KB;
// to 512 KiB on 2026-11-07 for the peer page's Invite card (#67), about 13 KB,
// which put the UI at about 496 KB; to 544 KiB on 2026-11-07 for the
// merge of build/agent-onboarding with master, measured at 553,752 bytes).
const uiMaxBytes = 544 << 10

var (
	scriptTag   = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	srcAttr     = regexp.MustCompile(`(?i)\bsrc\s*=\s*"([^"]+)"`)
	styleTag    = regexp.MustCompile(`(?i)<style\b`)
	styleAttr   = regexp.MustCompile(`(?i)<[^>]+\sstyle\s*=`)
	handlerAttr = regexp.MustCompile(`(?i)<[^>]+\son[a-z]+\s*=`)
	linkTag     = regexp.MustCompile(`(?i)<link\b[^>]*\bhref\s*=\s*"([^"]+)"`)
	stylesheet  = regexp.MustCompile(`(?i)<link\b[^>]*\brel\s*=\s*"stylesheet"[^>]*\bhref\s*=\s*"([^"]+)"`)
	iconLink    = regexp.MustCompile(`(?i)<link\b[^>]*\brel\s*=\s*"(?:icon|apple-touch-icon)"[^>]*\bhref\s*=\s*"([^"]+)"`)
	moduleType  = regexp.MustCompile(`(?i)\btype\s*=\s*"module"`)
	// Bodies are untrusted (PROTOCOL §14.2), so nothing may parse a string into
	// markup or code.
	htmlSinks = []*regexp.Regexp{
		regexp.MustCompile(`\binnerHTML\b`), regexp.MustCompile(`\bouterHTML\b`),
		regexp.MustCompile(`\binsertAdjacentHTML\b`), regexp.MustCompile(`\bdocument\s*\.\s*write`),
		regexp.MustCompile(`\beval\s*\(`), regexp.MustCompile(`\bnew\s+Function\b`),
	}
)

// mustCSP fails unless resp carries a Content-Security-Policy that confines
// every fetch and script to the hub's own origin and allows nothing inline.
func mustCSP(t *testing.T, resp *http.Response, what string) {
	t.Helper()
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatalf("%s: no Content-Security-Policy header", what)
	}
	for _, want := range []string{"default-src 'self'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("%s: CSP %q lacks %q", what, csp, want)
		}
	}
	for _, bad := range []string{"'unsafe-inline'", "'unsafe-eval'", "*"} {
		if strings.Contains(csp, bad) {
			t.Fatalf("%s: CSP %q allows %s", what, csp, bad)
		}
	}
}

func TestUIServedAndCSPNoInline(t *testing.T) {
	env := testhub.Start(t)

	// The page is public: no bearer.
	resp, body := env.Do("", "GET", "/ui/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/ = %d, want 200; body %.200s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET /ui/ Content-Type = %q, want text/html", ct)
	}
	page := string(body)
	if !strings.Contains(page, "vecnet") {
		t.Fatalf("GET /ui/ body does not mention vecnet: %.300s", page)
	}
	mustCSP(t, resp, "GET /ui/")

	// Nothing inline: every script is an external file (app.js), there is no
	// <style> element, no style= attribute and no on*= handler, all of which
	// the CSP would block.
	scripts := scriptTag.FindAllStringSubmatch(page, -1)
	if len(scripts) == 0 {
		t.Fatal("GET /ui/ loads no script")
	}
	assets := []string{}
	for _, s := range scripts {
		m := srcAttr.FindStringSubmatch(s[1])
		if m == nil {
			t.Fatalf("inline <script> without src: %.200s", s[0])
		}
		if strings.TrimSpace(s[2]) != "" {
			t.Fatalf("<script src=%q> has an inline body", m[1])
		}
		assets = append(assets, m[1])
	}
	if !contains(assets, "app.js") {
		t.Fatalf("scripts %v do not include app.js", assets)
	}
	for _, re := range []*regexp.Regexp{styleTag, styleAttr, handlerAttr} {
		if loc := re.FindStringIndex(page); loc != nil {
			t.Fatalf("inline markup the CSP blocks (%s): %.120s", re, page[loc[0]:])
		}
	}
	for _, m := range linkTag.FindAllStringSubmatch(page, -1) {
		assets = append(assets, m[1])
	}

	// Every asset is same-origin, served under /ui/ without auth, and carries
	// the CSP.
	for _, a := range assets {
		if strings.Contains(a, "//") || strings.HasPrefix(a, "/") {
			t.Fatalf("asset %q is not relative to /ui/", a)
		}
		resp, _ := env.Do("", "GET", "/ui/"+a, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /ui/%s = %d, want 200", a, resp.StatusCode)
		}
		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.HasSuffix(a, ".js") && !strings.HasPrefix(ct, "text/javascript"):
			t.Fatalf("GET /ui/%s Content-Type = %q, want text/javascript", a, ct)
		case strings.HasSuffix(a, ".css") && !strings.HasPrefix(ct, "text/css"):
			t.Fatalf("GET /ui/%s Content-Type = %q, want text/css", a, ct)
		case strings.HasSuffix(a, ".svg") && ct != "image/svg+xml":
			t.Fatalf("GET /ui/%s Content-Type = %q, want image/svg+xml", a, ct)
		case strings.HasSuffix(a, ".png") && ct != "image/png":
			t.Fatalf("GET /ui/%s Content-Type = %q, want image/png", a, ct)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET /ui/%s lacks X-Content-Type-Options: nosniff", a)
		}
		mustCSP(t, resp, "GET /ui/"+a)
	}

	// The operator UI never takes a secret in the URL: api.js sends it only
	// as a bearer header (PROTOCOL §2: a secret MUST NOT appear in a URL).
	_, js := env.Do("", "GET", "/ui/api.js", nil)
	if !strings.Contains(string(js), "Authorization") || !strings.Contains(string(js), "X-Vecnet-Ingress") {
		t.Fatal("api.js does not send Authorization and X-Vecnet-Ingress headers")
	}

	// /ui without the slash redirects to the page.
	req, _ := http.NewRequest("GET", env.URL+"/ui", nil)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r2, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode/100 != 3 || r2.Header.Get("Location") != "/ui/" {
		t.Fatalf("GET /ui = %d Location %q, want a redirect to /ui/", r2.StatusCode, r2.Header.Get("Location"))
	}

	// An unknown asset is a 404, not the page.
	if resp, _ := env.Do("", "GET", "/ui/nope.js", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /ui/nope.js = %d, want 404", resp.StatusCode)
	}
}

// With Deps.UIDir set (vecnet-hub --dev-ui-dir), the hub serves the UI from
// disk, so an edit shows on refresh without a rebuild; headers are unchanged.
func TestUIDevDirServesFromDisk(t *testing.T) {
	env := testhub.Start(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>vecnet dev copy v1</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.Configure(func(d *httpapi.Deps) { d.UIDir = dir })

	resp, body := env.Do("", "GET", "/ui/", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "dev copy v1") {
		t.Fatalf("GET /ui/ = %d %q, want the on-disk copy", resp.StatusCode, body)
	}
	mustCSP(t, resp, "GET /ui/ (dev dir)")

	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>vecnet dev copy v2</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, body := env.Do("", "GET", "/ui/", nil); !strings.Contains(string(body), "dev copy v2") {
		t.Fatalf("after editing the file GET /ui/ = %q, want v2", body)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestUINotFoundIsErrorEnvelope: a missing file, subdirectory or dotfile
// under /ui/ answers 404 with the §2 error envelope (R01), not plain text.
func TestUINotFoundIsErrorEnvelope(t *testing.T) {
	env := testhub.Start(t)
	for _, p := range []string{"/ui/nope.js", "/ui/sub/app.js", "/ui/.hidden"} {
		resp, raw := env.Do("", "GET", p, nil)
		mustErr(t, resp, raw, http.StatusNotFound, "not_found")
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("GET %s Content-Type = %q, want application/json", p, ct)
		}
	}
}

// staticFiles returns every embedded UI file by name.
func staticFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := fs.WalkDir(ui.Files(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(ui.Files(), p)
		files[p] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no embedded UI files")
	}
	return files
}

// TestUIStaticNoHTMLSinks: a peer-supplied string can only reach the page as
// text, because no module holds a way to turn a string into markup or code.
func TestUIStaticNoHTMLSinks(t *testing.T) {
	seen := 0
	for name, b := range staticFiles(t) {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		seen++
		for _, re := range htmlSinks {
			if loc := re.FindIndex(b); loc != nil {
				t.Errorf("%s holds %q: %.80s", name, b[loc[0]:loc[1]], b[loc[0]:])
			}
		}
	}
	if seen == 0 {
		t.Fatal("no .js files checked")
	}
}

// TestUIStaticNoInline: the page holds no markup the CSP would block, its one
// script is an ES module loaded by src, and each target is served with the
// right type.
func TestUIStaticNoInline(t *testing.T) {
	env := testhub.Start(t)
	page := string(staticFiles(t)["index.html"])
	if page == "" {
		t.Fatal("index.html is empty or missing")
	}
	for _, re := range []*regexp.Regexp{styleTag, styleAttr, handlerAttr} {
		if loc := re.FindStringIndex(page); loc != nil {
			t.Fatalf("inline markup the CSP blocks (%s): %.120s", re, page[loc[0]:])
		}
	}
	// Two scripts, both by src with no inline body: the blocking classic
	// appearance-boot.js, which sets theme and density on <html> before first
	// paint (hub preferences Task 6: no theme flash on load; a module would
	// run too late), then the one entry module.
	scripts := scriptTag.FindAllStringSubmatch(page, -1)
	want := []struct {
		src    string
		module bool
	}{{"appearance-boot.js", false}, {"app.js", true}}
	if len(scripts) != len(want) {
		t.Fatalf("index.html has %d scripts, want the appearance boot script then one entry module", len(scripts))
	}
	for i, w := range want {
		m := srcAttr.FindStringSubmatch(scripts[i][1])
		if m == nil || m[1] != w.src {
			t.Fatalf("script %q does not load %s by src", scripts[i][0], w.src)
		}
		if moduleType.MatchString(scripts[i][1]) != w.module {
			t.Fatalf("script %q: type=module is %v, want %v", scripts[i][0], !w.module, w.module)
		}
		if strings.TrimSpace(scripts[i][2]) != "" {
			t.Fatalf("script %q has an inline body", scripts[i][0])
		}
	}
	sheets := stylesheet.FindAllStringSubmatch(page, -1)
	if len(sheets) == 0 {
		t.Fatal("index.html links no stylesheet")
	}
	targets := map[string]string{"app.js": "text/javascript"}
	for _, s := range sheets {
		targets[s[1]] = "text/css"
	}
	// A module is only reachable through an import, so check all of them too:
	// a browser refuses a module served with the wrong type.
	for name := range staticFiles(t) {
		switch {
		case strings.HasSuffix(name, ".js"):
			targets[name] = "text/javascript"
		case strings.HasSuffix(name, ".css"):
			if _, linked := targets[name]; !linked {
				t.Errorf("%s is not linked from index.html", name)
			}
		}
	}
	for target, want := range targets {
		resp, _ := env.Do("", "GET", "/ui/"+target, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /ui/%s = %d, want 200", target, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, want) {
			t.Errorf("GET /ui/%s Content-Type = %q, want %s", target, ct, want)
		}
	}
}

// TestUIIcons: the page links an SVG favicon, a 32 px PNG fallback and a
// 180 px apple-touch-icon, and base.css draws logo.svg as the brand mark;
// each is served with its image type, every image in static/ is one of them,
// and the SVGs are inert (no script, no external refs).
func TestUIIcons(t *testing.T) {
	env := testhub.Start(t)
	files := staticFiles(t)
	page := string(files["index.html"])
	want := map[string]string{"favicon.svg": "image/svg+xml", "favicon-32.png": "image/png", "apple-touch-icon.png": "image/png"}
	linked := map[string]bool{}
	for _, m := range iconLink.FindAllStringSubmatch(page, -1) {
		linked[m[1]] = true
	}
	if !strings.Contains(string(files["base.css"]), "url(logo.svg)") {
		t.Error("base.css does not draw logo.svg")
	}
	linked["logo.svg"] = true
	want["logo.svg"] = "image/svg+xml"
	for name, ct := range want {
		if !linked[name] {
			t.Errorf("index.html does not link icon %s", name)
		}
		resp, body := env.Do("", "GET", "/ui/"+name, nil)
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("GET /ui/%s = %d (%d bytes), want 200", name, resp.StatusCode, len(body))
		}
		if got := resp.Header.Get("Content-Type"); got != ct {
			t.Errorf("GET /ui/%s Content-Type = %q, want %s", name, got, ct)
		}
		mustCSP(t, resp, "GET /ui/"+name)
	}
	for name := range files {
		if (strings.HasSuffix(name, ".svg") || strings.HasSuffix(name, ".png")) && want[name] == "" {
			t.Errorf("image %s is not one of the linked icons", name)
		}
	}
	for _, name := range []string{"favicon.svg", "logo.svg"} {
		svg := strings.ReplaceAll(string(files[name]), `xmlns="http://www.w3.org/2000/svg"`, "")
		for _, bad := range []string{"<script", "foreignObject", "http://", "https://", "xlink:href", "<style"} {
			if strings.Contains(svg, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
	for _, name := range []string{"favicon-32.png", "apple-touch-icon.png"} {
		if !strings.HasPrefix(string(files[name]), "\x89PNG\r\n\x1a\n") {
			t.Errorf("%s is not a PNG", name)
		}
	}
}

// TestUIUnderSizeCap sums every file, not just what the page links.
func TestUIUnderSizeCap(t *testing.T) {
	total := 0
	for _, b := range staticFiles(t) {
		total += len(b)
	}
	if total > uiMaxBytes {
		t.Fatalf("UI is %d bytes, want at most %d", total, uiMaxBytes)
	}
}

// Pin the complete policy: blob URLs are allowed only for image thumbnails.
func TestCSPAllowsBlobImagesOnly(t *testing.T) {
	env := testhub.Start(t)
	resp, _ := env.Do("", "GET", "/ui/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/ = %d, want 200", resp.StatusCode)
	}
	const want = "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data: blob:; font-src 'self'; object-src 'none'; base-uri 'none'; " +
		"form-action 'self'; frame-ancestors 'none'"
	if got := resp.Header.Get("Content-Security-Policy"); got != want {
		t.Fatalf("CSP = %q, want %q", got, want)
	}
}
