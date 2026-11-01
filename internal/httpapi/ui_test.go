package httpapi_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/testhub"
)

// uiMaxBytes is the page budget (plan Task 19): the HTML and every asset it
// loads, with no build step.
const uiMaxBytes = 60 << 10

var (
	scriptTag   = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	srcAttr     = regexp.MustCompile(`(?i)\bsrc\s*=\s*"([^"]+)"`)
	styleTag    = regexp.MustCompile(`(?i)<style\b`)
	styleAttr   = regexp.MustCompile(`(?i)<[^>]+\sstyle\s*=`)
	handlerAttr = regexp.MustCompile(`(?i)<[^>]+\son[a-z]+\s*=`)
	stylesheet  = regexp.MustCompile(`(?i)<link\b[^>]*\bhref\s*=\s*"([^"]+)"`)
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
	for _, m := range stylesheet.FindAllStringSubmatch(page, -1) {
		assets = append(assets, m[1])
	}

	// Every asset is same-origin, served under /ui/ without auth, carries the
	// CSP, and the whole page (HTML + assets) stays under 60 KB.
	total := len(body)
	for _, a := range assets {
		if strings.Contains(a, "//") || strings.HasPrefix(a, "/") {
			t.Fatalf("asset %q is not relative to /ui/", a)
		}
		resp, b := env.Do("", "GET", "/ui/"+a, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /ui/%s = %d, want 200", a, resp.StatusCode)
		}
		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.HasSuffix(a, ".js") && !strings.HasPrefix(ct, "text/javascript"):
			t.Fatalf("GET /ui/%s Content-Type = %q, want text/javascript", a, ct)
		case strings.HasSuffix(a, ".css") && !strings.HasPrefix(ct, "text/css"):
			t.Fatalf("GET /ui/%s Content-Type = %q, want text/css", a, ct)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET /ui/%s lacks X-Content-Type-Options: nosniff", a)
		}
		mustCSP(t, resp, "GET /ui/"+a)
		total += len(b)
	}
	if total >= uiMaxBytes {
		t.Fatalf("UI is %d bytes (HTML + %v), want < %d", total, assets, uiMaxBytes)
	}

	// The operator UI never takes a secret in the URL: app.js sends it only
	// as a bearer header (PROTOCOL §2: a secret MUST NOT appear in a URL).
	_, js := env.Do("", "GET", "/ui/app.js", nil)
	if !strings.Contains(string(js), "Authorization") || !strings.Contains(string(js), "X-Vecnet-Ingress") {
		t.Fatal("app.js does not send Authorization and X-Vecnet-Ingress headers")
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
