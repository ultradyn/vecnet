package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

var dotLinkCode = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}(-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}){2}$`)

func TestAdminDotLinks(t *testing.T) {
	env := testhub.Start(t)
	dot, _ := env.NewPeer("dot", "my-dot")
	sess, sessSecret := env.NewPeer("session", "s1")

	resp, body := env.Do(env.OperatorSecret, "POST", "/v1/admin/dot-links", map[string]string{"peer_id": dot})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("dot-links: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	var out struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	if !dotLinkCode.MatchString(out.Code) {
		t.Fatalf("code = %q", out.Code)
	}
	if want := envelope.FormatTime(env.Now.Now().Add(10 * time.Minute)); out.ExpiresAt != want {
		t.Fatalf("expires_at = %s, want %s", out.ExpiresAt, want)
	}

	// Audited, without the code.
	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/admin/audit?limit=5", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"dot_link"`) || !strings.Contains(string(body), dot) {
		t.Fatalf("audit: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), out.Code) {
		t.Fatalf("audit holds the code: %s", body)
	}

	for name, c := range map[string]struct {
		secret string
		body   any
		status int
		code   string
	}{
		"not operator": {sessSecret, map[string]string{"peer_id": dot}, http.StatusForbidden, "forbidden"},
		"no bearer":    {"", map[string]string{"peer_id": dot}, http.StatusUnauthorized, "unauthenticated"},
		"not a dot":    {env.OperatorSecret, map[string]string{"peer_id": sess}, http.StatusUnprocessableEntity, "invalid_value"},
		"unknown peer": {env.OperatorSecret, map[string]string{"peer_id": "peer_01ARZ3NDEKTSV4RRFFQ69G5FAV"}, http.StatusNotFound, "not_found"},
		"no peer_id":   {env.OperatorSecret, map[string]string{}, http.StatusBadRequest, "invalid_body"},
		"extra field":  {env.OperatorSecret, map[string]any{"peer_id": dot, "x": 1}, http.StatusBadRequest, "invalid_body"},
	} {
		resp, body := env.Do(c.secret, "POST", "/v1/admin/dot-links", c.body)
		if resp.StatusCode != c.status || !strings.Contains(string(body), `"code":"`+c.code+`"`) {
			t.Errorf("%s: %d %s, want %d %s", name, resp.StatusCode, body, c.status, c.code)
		}
	}
}

func TestDotLinkConsumption(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	dot, _ := env.NewPeer("dot", "my-dot")

	code, err := env.Hub.DotLink(ctx, dot)
	if err != nil {
		t.Fatal(err)
	}
	if !dotLinkCode.MatchString(code) {
		t.Fatalf("code = %q", code)
	}

	// Case and surrounding space are forgiven: a person types this.
	got, err := env.Hub.ConsumeDotLink(ctx, "  "+strings.ToLower(code)+"\n")
	if err != nil || got != dot {
		t.Fatalf("ConsumeDotLink = %q, %v", got, err)
	}
	if _, err := env.Hub.ConsumeDotLink(ctx, code); !errors.Is(err, hub.ErrDotLink) {
		t.Fatalf("second use: %v", err)
	}

	for _, bad := range []string{"", "ABC", "ZZZZZZZZZZ", "ABCDEFGH"} {
		if _, err := env.Hub.ConsumeDotLink(ctx, bad); !errors.Is(err, hub.ErrDotLink) {
			t.Fatalf("code %q: %v", bad, err)
		}
	}

	expiring, err := env.Hub.DotLink(ctx, dot)
	if err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(10 * time.Minute)
	if _, err := env.Hub.ConsumeDotLink(ctx, expiring); !errors.Is(err, hub.ErrDotLink) {
		t.Fatalf("expired code: %v", err)
	}

	// A disabled peer's code is dead too.
	live, err := env.Hub.DotLink(ctx, dot)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Hub.SetPeerDisabled(ctx, dot, true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.ConsumeDotLink(ctx, live); !errors.Is(err, hub.ErrDotLink) {
		t.Fatalf("disabled peer: %v", err)
	}
	if _, err := env.Hub.DotLink(ctx, dot); err == nil {
		t.Fatal("DotLink for a disabled peer succeeded")
	}
}

// The OAuth endpoints are public: they answer without a bearer.
func TestOAuthPathsArePublic(t *testing.T) {
	env := testhub.Start(t)
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/.well-known/oauth-authorization-server", http.StatusOK},
		{"GET", "/.well-known/oauth-protected-resource", http.StatusOK},
		{"GET", "/oauth/consent.css", http.StatusOK},
		{"GET", "/oauth/authorize", http.StatusBadRequest},
		{"POST", "/oauth/token", http.StatusBadRequest},
		{"POST", "/oauth/register", http.StatusBadRequest},
	} {
		resp, body := env.Do("", c.method, c.path, nil)
		if resp.StatusCode != c.status {
			t.Errorf("%s %s: %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}
	// Other paths still need a bearer.
	if resp, _ := env.Do("", "GET", "/v1/peers", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /v1/peers without a bearer: %d", resp.StatusCode)
	}
}
