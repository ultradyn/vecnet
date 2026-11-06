package httpapi_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// TestHubRootBrowserRedirect: a browser that opens the hub root is sent to
// the UI. API clients, a bearer, POST and any other path keep the JSON error.
func TestHubRootBrowserRedirect(t *testing.T) {
	env := testhub.Start(t)

	t.Run("accept html", func(t *testing.T) {
		resp, body := rootDo(t, env, http.MethodGet, "/", "", map[string]string{
			"Accept": "text/html,application/xhtml+xml",
		})
		mustRedirectUI(t, resp, body)
	})

	t.Run("mozilla ua", func(t *testing.T) {
		resp, body := rootDo(t, env, http.MethodGet, "/", "", map[string]string{
			"User-Agent": "Mozilla/5.0 (compatible; vecnet-test)",
		})
		mustRedirectUI(t, resp, body)
	})

	t.Run("head", func(t *testing.T) {
		resp, body := rootDo(t, env, http.MethodHead, "/", "", map[string]string{
			"Accept":     "text/html",
			"User-Agent": "Mozilla/5.0",
		})
		mustRedirectUI(t, resp, body)
	})

	t.Run("curl", func(t *testing.T) {
		resp, body := rootDo(t, env, http.MethodGet, "/", "", map[string]string{
			"User-Agent": "curl/8.7.1",
		})
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Fatalf("curl GET / Location = %q, want no redirect", loc)
		}
		mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("bearer", func(t *testing.T) {
		resp, body := rootDo(t, env, http.MethodGet, "/", env.OperatorSecret, map[string]string{
			"Accept":     "text/html",
			"User-Agent": "Mozilla/5.0",
		})
		if resp.StatusCode == http.StatusFound || resp.Header.Get("Location") == "/ui/" {
			t.Fatalf("bearer GET / = %d Location %q, want no redirect", resp.StatusCode, resp.Header.Get("Location"))
		}
		mustErr(t, resp, body, http.StatusNotFound, "not_found")
	})

	t.Run("post", func(t *testing.T) {
		resp, body := rootDo(t, env, http.MethodPost, "/", "", map[string]string{
			"Accept":     "text/html",
			"User-Agent": "Mozilla/5.0",
		})
		if resp.StatusCode == http.StatusFound || resp.Header.Get("Location") != "" {
			t.Fatalf("POST / = %d Location %q, want no redirect", resp.StatusCode, resp.Header.Get("Location"))
		}
		mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("other path", func(t *testing.T) {
		resp, body := rootDo(t, env, http.MethodGet, "/foo", "", map[string]string{
			"Accept":     "text/html",
			"User-Agent": "Mozilla/5.0",
		})
		if resp.StatusCode == http.StatusFound || resp.Header.Get("Location") != "" {
			t.Fatalf("GET /foo = %d Location %q, want no redirect", resp.StatusCode, resp.Header.Get("Location"))
		}
		mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
	})
}

func mustRedirectUI(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302; body %s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/" {
		t.Fatalf("Location = %q, want /ui/", loc)
	}
}

// rootDo sends one request and does not follow redirects.
func rootDo(t *testing.T, env *testhub.Env, method, path, secret string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, env.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}
