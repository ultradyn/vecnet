package oauth_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/mcpdiag"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/testhub"
)

type oauthLogBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *oauthLogBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *oauthLogBuffer) String() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }

func lastOAuthSequence() uint64 {
	records := mcpdiag.Snapshot(mcpdiag.Filter{})
	if len(records) == 0 {
		return 0
	}
	return records[len(records)-1].Sequence
}

// Both destinations receive the same allowlisted facts. Response bodies and
// authorize form/redirect details never pass through a diagnostic buffer.
func TestOAuthEndpointDiagnosticsRedacted(t *testing.T) {
	env := testhub.Start(t)
	var logs oauthLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	start := lastOAuthSequence()
	check := func(kind, reason string, run func()) {
		t.Helper()
		after := lastOAuthSequence()
		run()
		records := mcpdiag.Snapshot(mcpdiag.Filter{After: after})
		found := []mcpdiag.Record{}
		for _, rec := range records {
			if strings.HasPrefix(rec.Kind, "oauth.") {
				found = append(found, rec)
			}
		}
		if len(found) != 1 {
			t.Fatalf("OAuth request emitted %d records, want one", len(found))
		}
		if found[0].Kind != kind || found[0].Fields["reason"] != reason {
			t.Fatalf("OAuth diagnostic kind=%s reason=%v", found[0].Kind, found[0].Fields["reason"])
		}
	}
	check("oauth.metadata", "ok", func() { get(t, env.URL+"/.well-known/oauth-protected-resource/mcp?token=vat_token-canary", nil) })
	check("oauth.register", "invalid_redirect_uri", func() {
		env.Do("", "POST", "/oauth/register", map[string]any{"redirect_uris": []string{"https://other.example/private-callback-canary?secret=whsec_secret-canary"}, "client_name": "message-text-canary"})
	})
	check("oauth.token", "invalid_grant", func() {
		token(t, env, url.Values{"grant_type": {"authorization_code"}, "code": {"vac_code-canary"}, "client_id": {"client-canary"}, "redirect_uri": {"https://chatgpt.com/private-callback-canary"}, "code_verifier": {"verifier-canary"}})
	})
	check("oauth.refresh", "invalid_grant", func() {
		token(t, env, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"vrt_refresh-canary"}})
	})
	check("oauth.consent", "invalid_request", func() {
		postForm(t, env, "/oauth/authorize", url.Values{"request": {"sealed-form-canary"}, "approve": {"1"}, "link_code": {"link-code-canary"}}, nil)
	})
	b := newBrowser(t, env)
	q := b.query(verifierA)
	q.Set("state", "state-canary")
	check("oauth.authorize", "consent_required", func() { b.authorize(q) })
	check("oauth.authorize", "invalid_scope", func() { q.Set("scope", "scope-canary"); b.authorize(q) })
	// A full consent and refresh include only scope names, never issued values.
	loggedIn, _, tok := login(t, env)
	check("oauth.refresh", "ok", func() { loggedIn.refresh(tok.RefreshToken) })
	data, err := json.Marshal(mcpdiag.Snapshot(mcpdiag.Filter{After: start}))
	if err != nil {
		t.Fatal(err)
	}
	both := string(data) + logs.String()
	for index, canary := range []string{"vat_token-canary", "private-callback-canary", "whsec_secret-canary", "message-text-canary", "vac_code-canary", "client-canary", "verifier-canary", "vrt_refresh-canary", "sealed-form-canary", "link-code-canary", "state-canary", "scope-canary", tok.AccessToken, tok.RefreshToken} {
		if strings.Contains(both, canary) {
			t.Errorf("OAuth diagnostics leaked canary %d", index)
		}
	}
}

func TestCIMDFetchDiagnosticsRedacted(t *testing.T) {
	env := testhub.Start(t)
	path := "/private-cimd-path-canary.json"
	srv, _ := cimdServer(t, env, map[string]string{path: `{"client_id":"{self}","client_name":"message-cimd-canary","redirect_uris":["` + chatgptRedirect + `"]}`, "/private-invalid-cimd-canary.json": `{"client_secret":"secret-cimd-canary"}`})
	b := &browser{t: t, env: env, client: srv.URL + path, redirect: chatgptRedirect}
	start := lastOAuthSequence()
	resp, _ := b.authorize(b.query(verifierA))
	if resp.StatusCode != http.StatusOK {
		t.Fatal("CIMD authorization failed")
	}
	// A cached document does not produce another fetch record.
	b.authorize(b.query(verifierA))
	b.client = srv.URL + "/private-invalid-cimd-canary.json"
	b.authorize(b.query(verifierA))
	records := mcpdiag.Snapshot(mcpdiag.Filter{After: start})
	cimd := []mcpdiag.Record{}
	for _, rec := range records {
		if rec.Kind == "oauth.cimd" {
			cimd = append(cimd, rec)
		}
	}
	if len(cimd) != 2 {
		t.Fatalf("CIMD emitted %d fetch records, want 2", len(cimd))
	}
	if cimd[0].Fields["reason"] != "ok" || cimd[0].Fields["http_status"] != http.StatusOK || cimd[0].Fields["cimd_host"] != "127.0.0.1" {
		t.Fatalf("successful CIMD diagnostic = %v", cimd[0].Fields)
	}
	if cimd[1].Fields["reason"] != "invalid_metadata" {
		t.Fatalf("refused CIMD reason = %v", cimd[1].Fields["reason"])
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{path, "private-invalid-cimd-canary", "message-cimd-canary", "secret-cimd-canary"} {
		if strings.Contains(string(data), canary) {
			t.Errorf("CIMD ring leaked %q", canary)
		}
	}
}

// Outer observation survives middleware rejections, and a nested endpoint
// wrapper updates that record instead of adding a second completion line.
func TestOAuthObserveHTTPBeforeMiddleware(t *testing.T) {
	cases := []struct {
		name, method, path, kind, reason string
		status                           int
	}{
		{"nested metadata", "GET", "/.well-known/oauth-protected-resource/mcp", "oauth.metadata", "ok", 0},
		{"large registration", "POST", "/oauth/register", "oauth.register", "payload_too_large", 413},
		{"limited token", "POST", "/oauth/token", "oauth.token", "rate_limited", 429},
		{"limited discovery", "GET", "/.well-known/oauth-authorization-server", "oauth.metadata", "rate_limited", 429},
		{"link creation", "POST", "/v1/admin/dot-links", "oauth.link", "linked", 201},
		{"link forbidden", "POST", "/v1/admin/dot-links", "oauth.link", "forbidden", 403},
		{"unknown OAuth path", "GET", "/oauth/unknown-private-path-canary", "oauth.request", "not_found", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			(&oauth.Server{Issuer: "https://hub.example"}).Mount(mux)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("response-code-secret-canary"))
					return
				}
				mux.ServeHTTP(w, r)
			})
			handler := oauth.ObserveHTTP(next)
			start := lastOAuthSequence()
			req := httptest.NewRequest(tc.method, tc.path+"?secret=query-secret-canary", nil)
			req.Header.Set("Authorization", "Bearer vat_outer-token-canary")
			handler.ServeHTTP(httptest.NewRecorder(), req)
			records := mcpdiag.Snapshot(mcpdiag.Filter{After: start})
			if len(records) != 1 {
				t.Fatalf("completion records=%d, want one", len(records))
			}
			rec := records[0]
			status := tc.status
			if status == 0 {
				status = 200
			}
			if rec.Kind != tc.kind || rec.Fields["reason"] != tc.reason || rec.Fields["http_status"] != status {
				t.Fatalf("completion kind=%s fields=%v", rec.Kind, rec.Fields)
			}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			for _, canary := range []string{"unknown-private-path-canary", "response-code-secret-canary", "query-secret-canary", "vat_outer-token-canary"} {
				if strings.Contains(string(data), canary) {
					t.Error("outer completion leaked private canary")
				}
			}
		})
	}
}
