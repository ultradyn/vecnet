package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestRouteTransportExplicitCLI(t *testing.T) {
	env := testhub.Start(t)
	u := profileUser(t, env.URL, env.OperatorSecret)
	peer, _ := env.NewPeer("grokbot", "Transport Route")
	keyFile := filepath.Join(t.TempDir(), "synthetic-key")
	if err := os.WriteFile(keyFile, []byte("crsr_SYNTHETIC_route"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"delivery_v1", "recipient_drain_v1"} {
		out := u.mustRun("route", "add", "--owner", peer, "--kind", "grokbot", "--url", routineURL, "--key-file", keyFile, "--grokbot-transport", selected)
		id := field(t, out, "route_id")
		var rows []struct {
			RouteID   string `json:"route_id"`
			Transport string `json:"grokbot_transport"`
		}
		if err := json.Unmarshal([]byte(u.mustRun("--json", "route", "list")), &rows); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r.RouteID == id {
				found = r.Transport == selected
			}
		}
		if !found {
			t.Errorf("route list lost %s", selected)
		}
		u.mustRun("route", "remove", id)
	}
	// A transport-only update consumes PATCH204 and makes exactly that call.
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if r.Method != http.MethodPatch || len(b) != 1 || b["grokbot_transport"] != "delivery_v1" {
			t.Errorf("unexpected request %s %v", r.Method, b)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	stub := agentOperator(t, srv.URL)
	out := stub.mustRun("route", "update", "route_synthetic", "--grokbot-transport", "delivery_v1")
	if !strings.Contains(out, "updated route_synthetic") || len(calls) != 1 {
		t.Errorf("PATCH acknowledgement/calls %q %v", out, calls)
	}
}

func TestAgentTransportExplicitCLI(t *testing.T) {
	env := testhub.Start(t)
	u := profileUser(t, env.URL, env.OperatorSecret)
	keyFile := filepath.Join(t.TempDir(), "synthetic-key")
	if err := os.WriteFile(keyFile, []byte("crsr_SYNTHETIC_agent"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := u.mustRun("--json", "agent", "add", "--kind", "grokbot", "--name", "Transport Agent", "--url", routineURL, "--key-file", keyFile, "--skip-mcp", "--request-id", "cli-transport", "--grokbot-transport", "recipient_drain_v1")
	var res onboarding.SubmitResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if res.GrokbotTransport != "recipient_drain_v1" {
		t.Errorf("create transport %q", res.GrokbotTransport)
	}
	for _, jsonMode := range []bool{false, true} {
		args := []string{"agent", "status", "cli-transport"}
		if jsonMode {
			args = append([]string{"--json"}, args...)
		}
		status := u.mustRun(args...)
		if !strings.Contains(status, "recipient_drain_v1") || strings.Contains(status, "crsr_") || strings.Contains(status, routineURL) {
			t.Error("unsafe or missing transport status")
		}
	}
}

func TestAgentTransportBindingsClosedAndExplicit(t *testing.T) {
	for _, selected := range []string{"", "delivery_v1", "recipient_drain_v1"} {
		t.Run("selection-"+selected, func(t *testing.T) {
			h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
			u := agentOperator(t, h.srv.URL)
			keyFile := writeModeFile(t, "synthetic-key", "crsr_SYNTHETIC_binding", 0600)
			args := []string{"agent", "add", "--kind", "grokbot", "--name", "Binding Bot", "--url", routineURL, "--key-file", keyFile, "--skip-mcp", "--request-id", "binding-transport"}
			if selected != "" {
				args = append(args, "--grokbot-transport", selected)
			}
			u.mustRun(args...)
			posts := h.posts()
			if len(posts) != 1 {
				t.Fatalf("submits=%d", len(posts))
			}
			top := posts[0]
			if len(top) != 4 {
				t.Fatal("SubmitRequest top-level changed")
			}
			for _, k := range []string{"kind", "spec_version", "request_id", "inputs"} {
				if _, ok := top[k]; !ok {
					t.Error("closed submit missing " + k)
				}
			}
			inputs := top["inputs"].(map[string]any)
			got, present := inputs["grokbot_transport"]
			if selected == "" {
				if present {
					t.Error("omission became implicit selection")
				}
			} else if !present || got != selected {
				t.Error("explicit selection lost")
			}
		})
	}
	t.Run("old registry refuses explicit flag", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec {
			specs := onboarding.Specs()
			for i := range specs {
				fields := specs[i].Fields[:0]
				for _, f := range specs[i].Fields {
					if f.Name != "grokbot_transport" {
						fields = append(fields, f)
					}
				}
				specs[i].Fields = fields
			}
			return specs
		})
		u := agentOperator(t, h.srv.URL)
		code, _, _ := u.run("agent", "add", "--kind", "grokbot", "--name", "Old", "--url", routineURL, "--key-file", "unused", "--grokbot-transport", "recipient_drain_v1")
		if code == 0 || len(h.posts()) != 0 {
			t.Fatal("explicit selection silently ignored by old registry")
		}
	})
	t.Run("invalid and nonGrok refuse locally", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		u := agentOperator(t, h.srv.URL)
		for _, args := range [][]string{{"agent", "add", "--kind", "session", "--name", "Other", "--grokbot-transport", "delivery_v1"}, {"agent", "add", "--kind", "grokbot", "--name", "Invalid", "--url", routineURL, "--key-file", "unused", "--grokbot-transport", "unknown-secret-canary"}, {"route", "add", "--owner", "peer_other", "--url", "https://example.com", "--grokbot-transport", "delivery_v1"}, {"route", "update", "route_other", "--grokbot-transport", "unknown-secret-canary"}} {
			code, out, errs := u.run(args...)
			if code == 0 || strings.Contains(out+errs, "unknown-secret-canary") {
				t.Fatal("invalid selection accepted/echoed")
			}
		}
		if len(h.posts()) != 0 || len(h.paths("/v1/admin/routes/route_other")) != 0 {
			t.Fatal("invalid selection reached mutation")
		}
	})
}

func TestAgentHistoricalTransportDisplay(t *testing.T) {
	h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
	u := agentOperator(t, h.srv.URL)
	h.onStatus = func(id string, w http.ResponseWriter) {
		writeJSONMap(w, 200, map[string]any{"request_id": id, "kind": "grokbot", "peer_id": "peer_historical", "auth_state": "unlinked"})
	}
	for _, args := range [][]string{{"agent", "status", "historical-display"}, {"--json", "agent", "status", "historical-display"}} {
		out := u.mustRun(args...)
		if !strings.Contains(out, "delivery_v1") {
			t.Error("historical Grok omission did not display effective legacy")
		}
	}
}
