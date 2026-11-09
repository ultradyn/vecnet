package httpapi_test

// GET/HEAD of exactly /favicon.ico publicly redirects to
// the existing embedded SVG. Other methods and neighboring paths stay private.

import (
	"io"
	"io/fs"
	"net/http"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
	"github.com/XertroV/vecnet/internal/ui"
)

func TestOAuthFaviconPublicRead(t *testing.T) {
	env := testhub.Start(t)
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	want, err := fs.ReadFile(ui.Files(), "favicon.svg")
	if err != nil {
		t.Fatal("embedded favicon missing")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, env.URL+"/favicon.ico", nil)
			if err != nil {
				t.Fatal("construct favicon request")
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal("request favicon")
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal("read favicon response")
			}
			if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/favicon.svg" {
				t.Fatalf("favicon HTTP %d; want 302 to public embedded SVG", resp.StatusCode)
			}
			if resp.Header.Get("WWW-Authenticate") != "" {
				t.Error("favicon challenged for authentication")
			}
			if len(resp.Cookies()) != 0 {
				t.Error("favicon changed browser cookies")
			}
			if method == http.MethodHead && len(body) != 0 {
				t.Error("HEAD favicon returned a body")
			}
			asset, got := env.Do("", method, resp.Header.Get("Location"), nil)
			if asset.StatusCode != http.StatusOK || asset.Header.Get("Content-Type") != "image/svg+xml" {
				t.Fatalf("public icon asset HTTP %d with unexpected status or media type", asset.StatusCode)
			}
			if method == http.MethodGet && string(got) != string(want) {
				t.Error("favicon target did not serve the existing embedded icon")
			}
			if method == http.MethodHead && len(got) != 0 {
				t.Error("HEAD icon asset returned a body")
			}
		})
	}
}

func TestOAuthFaviconDoesNotExpandPublicSurface(t *testing.T) {
	env := testhub.Start(t)
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/favicon.ico"},
		{http.MethodOptions, "/favicon.ico"},
		{http.MethodGet, "/favicon.ico.bak"},
		{http.MethodGet, "/favicon.ico/anything"},
		{http.MethodGet, "/favicon.ico/../v1/admin/peers"},
		{http.MethodGet, "/favicon.ico%2f..%2fv1/admin/peers"},
		{http.MethodGet, "/v1/peers"},
		{http.MethodGet, "/v1/admin/peers"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, env.URL+tc.path, nil)
			if err != nil {
				t.Fatal("construct private-surface request")
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal("request private surface")
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("HTTP %d; want unchanged 401", resp.StatusCode)
			}
		})
	}
}
