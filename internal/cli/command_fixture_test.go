package cli_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommandFixtureLeavesProjectInventoryEmpty(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/peers" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"peers":[{"peer_id":"peer_fixture","kind":"session","name":"Fixture","presence":"unknown","reachability":"unknown"}]}`)
	}))
	t.Cleanup(hub.Close)
	u := profileUser(t, hub.URL, "vn_peer_fixture")
	if out := u.mustRun("peers"); !strings.Contains(out, "peer_fixture session Fixture") {
		t.Fatalf("peers did not list the fixture peer: %q", out)
	}
	// Observe through the CLI, not through the fixture's environment map or
	// the private layout of the project registry.
	if out := strings.TrimSpace(u.mustRun("projects", "list", "--json")); out != "[]" {
		t.Fatalf("command fixture populated the local project inventory: %s", out)
	}
}
