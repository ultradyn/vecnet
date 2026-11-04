package oauth_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestParseAllowedRedirects(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "https://connector.example/callback?tenant=one",
		" https://connector.example/callback, https://connector.example/cb/{callback_id} ",
		"http://localhost:*,http://127.0.0.1:*,http://[::1]:*",
		"https://connector.example:*,http://127.0.0.2:8080/callback",
	} {
		got, err := oauth.ParseAllowedRedirects(raw)
		if err != nil {
			t.Errorf("valid patterns %q: %v", raw, err)
			continue
		}
		if !slices.Equal(got[:len(oauth.DefaultRedirects)], oauth.DefaultRedirects) {
			t.Errorf("defaults lost: %v", got)
		}
		if strings.TrimSpace(raw) != "" && got[len(oauth.DefaultRedirects)] != strings.TrimSpace(strings.Split(raw, ",")[0]) {
			t.Errorf("extension not appended: %v", got)
		}
	}
	for _, raw := range []string{
		"https://good.example/cb,", "*", "https://*", "https://*.example/cb",
		"https://example.*/cb", "https://example.com/cb/*", "https://example.com:*evil",
		"http://public.example/cb", "http://localhost.evil/cb", "http://192.168.0.1/cb",
		"cursor://anysphere.cursor-mcp/oauth/callback", "ftp://localhost/cb",
		"https://user:secret@example.com/cb", "https://example.com/cb#secret", "https://example.com/cb#",
		"https:///callback", "/callback", "https://example.com:0/cb", "https://example.com:65536/cb",
		"https://example.com:/cb", "https://example.com:abc/cb", "https://example.com:443:*",
		"https://example.com/cb/{unknown}", "https://example.com/{callback_id}/more",
		"https://example.com/cb?/{callback_id}", "https://example.com/cb:*",
	} {
		if _, err := oauth.ParseAllowedRedirects(raw); err == nil {
			t.Errorf("accepted invalid pattern %q", raw)
		}
	}
	// A caller owns its returned defaults; it cannot change the next startup.
	got, _ := oauth.ParseAllowedRedirects("")
	got[0] = "changed"
	if got, _ := oauth.ParseAllowedRedirects(""); got[0] == "changed" {
		t.Fatal("returned defaults alias the global list")
	}
}

func TestConfiguredRedirectPatterns(t *testing.T) {
	env := testhub.Start(t)
	var err error
	env.OAuth.AllowedRedirects, err = oauth.ParseAllowedRedirects("https://connector.example/exact,https://connector.example/cb/{callback_id},http://[::1]:*")
	if err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{
		chatgptRedirect, "https://www.cursor.com/agents/mcp/oauth/callback",
		"https://connector.example/exact", "https://connector.example/cb/abc-123",
		"http://[::1]:54321/callback",
	} {
		b := &browser{t: t, env: env, client: register(t, env, uri), redirect: uri}
		if resp, body := b.authorize(b.query(verifierA)); resp.StatusCode != http.StatusOK {
			t.Fatalf("authorize %s: %d %s", uri, resp.StatusCode, body)
		}
	}
	for _, uri := range []string{
		"https://connector.example/exact/extra", "https://connector.example/cb/a/b",
		"https://connector.example.evil/exact", "http://[::1]/callback",
		"http://[::1]:0/callback", "http://[::1]:54321/callback#fragment",
	} {
		if resp, _ := env.Do("", "POST", "/oauth/register", map[string]any{"redirect_uris": []string{uri}}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("accepted %s: %d", uri, resp.StatusCode)
		}
	}
}

func TestRedirectDiagnosticsRedactURLs(t *testing.T) {
	env := testhub.Start(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	name := strings.Repeat("n", 100)
	for _, uri := range []string{
		"https://user:USERINFO_SECRET@refused.example/callback?QUERY_SECRET=one#FRAGMENT_SECRET",
		"https://refused.example/callback?QUERY_SECRET=%zz#FRAGMENT_SECRET",
		"https://refused.example/%zz?QUERY_SECRET=two",
	} {
		if resp, _ := env.Do("", "POST", "/oauth/register", map[string]any{"client_name": name, "redirect_uris": []string{uri}}); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("refusal status: %d", resp.StatusCode)
		}
	}
	// Successful DCR only records hosts and a client ID prefix, even when an
	// explicitly allowed exact URI has a query string.
	good := "https://accepted.example/callback?QUERY_SECRET=three"
	env.OAuth.AllowedRedirects = append(env.OAuth.AllowedRedirects, good)
	id := register(t, env, good)
	b := &browser{t: t, env: env, client: id, redirect: good}
	q := b.query(verifierA)
	q.Set("redirect_uri", "https://other.example/callback?QUERY_SECRET=four#FRAGMENT_SECRET")
	if resp, _ := b.authorize(q); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("authorize refusal: %d", resp.StatusCode)
	}
	for _, secret := range []string{"QUERY_SECRET", "FRAGMENT_SECRET", "USERINFO_SECRET", id, name} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("diagnostic leaked %q", secret)
		}
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d diagnostics, want 5: %s", len(lines), logs.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["level"] != "INFO" {
			t.Fatalf("diagnostic level: %v", record)
		}
		if i == 3 {
			if record["client_id_prefix"] != id[:12] || !strings.Contains(line, "accepted.example") || strings.Contains(line, "/callback") {
				t.Fatalf("registration diagnostic: %v", record)
			}
			continue
		}
		endpoint := "/oauth/register"
		if i == 4 {
			endpoint = "/oauth/authorize"
		}
		if record["msg"] != "oauth: redirect refused" || record["endpoint"] != endpoint {
			t.Fatalf("refusal diagnostic: %v", record)
		}
		if i < 2 && (record["scheme"] != "https" || record["host"] != "refused.example" || record["path"] != "/callback" || record["client_name"] != name[:80]) {
			t.Fatalf("missing redirect components or name truncation: %v", record)
		}
	}
}
