package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

// errBody is the §2 error envelope.
type errBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// mustErr decodes body as the error envelope, failing unless status and code
// match and details is a JSON object (never null or absent).
func mustErr(t *testing.T, resp *http.Response, body []byte, status int, code string) errBody {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d; body %s", resp.StatusCode, status, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body is not an error envelope: %v; %s", err, body)
	}
	d, ok := raw["error"]["details"]
	if !ok || !bytes.HasPrefix(bytes.TrimSpace(d), []byte("{")) {
		t.Fatalf("error.details is not an object: %s", body)
	}
	var e errBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if e.Error.Code != code {
		t.Fatalf("error.code = %q, want %q; body %s", e.Error.Code, code, body)
	}
	if e.Error.Message == "" {
		t.Fatalf("error.message is empty: %s", body)
	}
	return e
}

func TestHealthzNoAuth(t *testing.T) {
	env := testhub.Start(t)
	resp, body := env.Do("", "GET", "/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200; body %s", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; %s", err, body)
	}
	if got["ok"] != true || len(got) != 1 {
		t.Fatalf("body = %s, want {\"ok\":true}", body)
	}
}

func TestMissingBearerIs401Envelope(t *testing.T) {
	env := testhub.Start(t)
	resp, body := env.Do("", "GET", "/v1/peers", nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")

	resp, body = env.Do("vn_peer_not-a-real-secret", "GET", "/v1/peers", nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
}

func TestOversizeBodyIs413(t *testing.T) {
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "big-sender")
	big := bytes.Repeat([]byte("a"), 300*1024)
	resp, body := env.Do(secret, "POST", "/v1/messages", big)
	mustErr(t, resp, body, http.StatusRequestEntityTooLarge, "payload_too_large")

	// The limit applies before auth: no credential, same answer.
	resp, body = env.Do("", "POST", "/v1/messages", big)
	mustErr(t, resp, body, http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestEnsureOperatorIdempotent(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	// testhub.Start already ran the first EnsureOperator.
	peer, secret, created, err := env.Hub.EnsureOperator(ctx, "Max")
	if err != nil {
		t.Fatalf("EnsureOperator: %v", err)
	}
	if created {
		t.Fatalf("second EnsureOperator created = true, want false")
	}
	if peer != env.OperatorPeer {
		t.Fatalf("peer = %q, want %q", peer, env.OperatorPeer)
	}
	if secret != "" {
		t.Fatalf("second EnsureOperator returned a secret")
	}
	if !strings.HasPrefix(env.OperatorSecret, "vn_operator_") {
		t.Fatalf("operator secret %q lacks vn_operator_ prefix", env.OperatorSecret)
	}
	c, err := env.Hub.Auth.Resolve(ctx, env.OperatorSecret)
	if err != nil {
		t.Fatalf("Resolve(operator secret): %v", err)
	}
	if c.Kind != "operator" || c.PeerID != env.OperatorPeer {
		t.Fatalf("caller = %+v, want operator with peer %s", c, env.OperatorPeer)
	}
}

func TestPublicPathsSkipAuth(t *testing.T) {
	env := testhub.Start(t)
	// Exempt paths reach the mux (404 until their routes exist), never 401.
	for _, p := range []string{"/ui/", "/ui/index.html", "/v1/ingress/src_01J0000000000000000000000A", "/v1/blobs/abc?t=rt_x"} {
		resp, body := env.Do("", "GET", p, nil)
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("GET %s = 401 without a bearer; body %s", p, body)
		}
	}
	resp, body := env.Do("", "POST", "/v1/enrol", map[string]any{"token": "x"})
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("POST /v1/enrol demanded a bearer before its route exists: %s", body)
	}
	// A blob read without ?t= still needs a bearer.
	resp, body = env.Do("", "GET", "/v1/blobs/abc", nil)
	mustErr(t, resp, body, http.StatusUnauthorized, "unauthenticated")
}

func TestUnknownRouteIs404Envelope(t *testing.T) {
	env := testhub.Start(t)
	resp, body := env.Do(env.OperatorSecret, "GET", "/v1/nope", nil)
	mustErr(t, resp, body, http.StatusNotFound, "not_found")
}

func TestAdminIsOperatorOnly(t *testing.T) {
	env := testhub.Start(t)
	_, peerSecret := env.NewPeer("session", "p1")
	resp, body := env.Do(peerSecret, "GET", "/v1/admin/audit", nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")

	_, prnSecret := env.NewPrincipal("acme")
	resp, body = env.Do(prnSecret, "GET", "/v1/admin/audit", nil)
	mustErr(t, resp, body, http.StatusForbidden, "forbidden")

	resp, body = env.Do(env.OperatorSecret, "GET", "/v1/admin/audit", nil)
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("operator on /v1/admin/audit = %d; body %s", resp.StatusCode, body)
	}
}

func TestNewPeerSecretAuthenticates(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("openclaw", "claw")
	if !strings.HasPrefix(peer, "peer_") || !strings.HasPrefix(secret, "vn_peer_") {
		t.Fatalf("NewPeer = %q, %q", peer, secret)
	}
	c, err := env.Hub.Auth.Resolve(context.Background(), secret)
	if err != nil || c.Kind != "peer" || c.PeerID != peer || c.PeerKind != "openclaw" {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
	// Authenticated, so past the middleware: 404 (no route yet), not 401.
	resp, body := env.Do(secret, "GET", "/v1/nope", nil)
	mustErr(t, resp, body, http.StatusNotFound, "not_found")
}

func TestAddPeerImplicitPrincipalAndBadKind(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	peer, tok, err := env.Hub.AddPeer(ctx, "session", "solo", "")
	if err != nil || !strings.HasPrefix(tok, "vn_enrol_") {
		t.Fatalf("AddPeer = %q, %q, %v", peer, tok, err)
	}
	var prn, prnName string
	if err := env.Hub.DB.QueryRowContext(ctx,
		`SELECT p.prn_id, r.name FROM peers p JOIN principals r ON r.prn_id = p.prn_id WHERE p.peer_id = ?`, peer).
		Scan(&prn, &prnName); err != nil {
		t.Fatalf("lookup principal: %v", err)
	}
	if prnName != "solo" {
		t.Fatalf("implicit principal name = %q, want \"solo\"", prnName)
	}

	explicit, _ := env.NewPrincipal("acme")
	peer2, _, err := env.Hub.AddPeer(ctx, "dot", "d1", explicit)
	if err != nil {
		t.Fatalf("AddPeer under principal: %v", err)
	}
	var got string
	if err := env.Hub.DB.QueryRowContext(ctx, `SELECT prn_id FROM peers WHERE peer_id = ?`, peer2).Scan(&got); err != nil || got != explicit {
		t.Fatalf("peer principal = %q, %v; want %q", got, err, explicit)
	}

	if _, _, err := env.Hub.AddPeer(ctx, "toaster", "t", ""); err == nil {
		t.Fatalf("AddPeer with unknown kind succeeded")
	}
	if _, _, err := env.Hub.AddPeer(ctx, "session", "x", "prn_01J0000000000000000000000A"); err == nil {
		t.Fatalf("AddPeer under unknown principal succeeded")
	}
}
