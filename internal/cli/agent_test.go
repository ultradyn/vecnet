package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/XertroV/vecnet/internal/onboarding"
)

// Task 5 acceptance (docs/plans/2026-11-06-agent-onboarding-kinds.md).
// The CLI talks only to the HTTP service. These tests use an in-process
// server, never a hub or SQLite.

const agentSecretHeader = "Bearer vn_operator_test"

func TestAgentCLIParity(t *testing.T) {
	t.Run("list filter and prompt JSON", func(t *testing.T) {
		specs := onboarding.Specs()
		h := newAgentHub(t, func() []onboarding.KindSpec { return specs })
		u := agentOperator(t, h.srv.URL)

		code, out, errs := u.run("--json", "agent", "kinds")
		if code != 0 {
			t.Fatalf("agent kinds --json: exit %d\n%s", code, errs)
		}
		var listed struct {
			Kinds []onboarding.KindSpec `json:"kinds"`
		}
		if err := json.Unmarshal([]byte(out), &listed); err != nil {
			t.Fatalf("kinds JSON: %v", err)
		}
		if len(listed.Kinds) != 6 {
			t.Fatalf("kinds = %d, want 6", len(listed.Kinds))
		}
		for i, spec := range specs {
			got := listed.Kinds[i]
			if got.Kind != spec.Kind || got.SpecVersion != spec.SpecVersion || got.SubmitAction != spec.SubmitAction {
				t.Fatalf("kind[%d] = %s %s %s, want %s %s %s", i, got.Kind, got.SpecVersion, got.SubmitAction, spec.Kind, spec.SpecVersion, spec.SubmitAction)
			}
			if got.PromptTemplate == "" || got.PromptTemplate != spec.PromptTemplate {
				t.Fatalf("kind %s prompt was not the hub template", spec.Kind)
			}
			if len(got.Fields) != len(spec.Fields) {
				t.Fatalf("kind %s fields = %d, want %d", spec.Kind, len(got.Fields), len(spec.Fields))
			}
		}
		if listed.Kinds[4].Kind != "grokbot" || listed.Kinds[4].SendDefaults["priority"] != "wake_idle" {
			t.Fatalf("grokbot send_defaults = %v", listed.Kinds[4].SendDefaults)
		}
		if got := h.paths("/v1/admin/agent-kinds"); len(got) != 1 || got[0].Method != http.MethodGet {
			t.Fatalf("kinds endpoint = %+v", got)
		}

		code, out, errs = u.run("--json", "agent", "kinds", "--kind", "pagchat-bot")
		if code != 0 {
			t.Fatalf("filter: exit %d\n%s", code, errs)
		}
		var one onboarding.KindSpec
		if err := json.Unmarshal([]byte(out), &one); err != nil {
			t.Fatalf("filter JSON: %v", err)
		}
		if one.Kind != "pagchat-bot" || one.PromptTemplate == "" || strings.Contains(out, "grokbot") {
			t.Fatalf("filter did not select one spec")
		}
		code, _, errs = u.run("--json", "agent", "kinds", "--kind", "operator")
		if code == 0 || !strings.Contains(errs, "operator") {
			t.Fatalf("unknown kind: exit %d %s", code, errs)
		}
	})

	t.Run("payload version request id and legacy flags", func(t *testing.T) {
		version := "spec-from-hub"
		h := newAgentHub(t, func() []onboarding.KindSpec { return specsVersion(version) })
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_session", ""))
		}
		u := agentOperator(t, h.srv.URL)
		before := mustRead(t, u.conf)
		code, out, errs := u.run("--json", "agent", "add", "--kind", "session", "--name", "Scout",
			"--ttl", "1h", "--principal", "prn_test", "--request-id", "req-session-1")
		if code != 0 {
			t.Fatalf("session add: exit %d\n%s\n%s", code, out, errs)
		}
		posts := h.posts()
		if len(posts) != 1 {
			t.Fatalf("posts = %d, want 1", len(posts))
		}
		body := posts[0]
		if body["kind"] != "session" || body["spec_version"] != version || body["request_id"] != "req-session-1" {
			t.Fatalf("payload kind/spec/request = %v %v %v", body["kind"], body["spec_version"], body["request_id"])
		}
		in, _ := body["inputs"].(map[string]any)
		if in["name"] != "Scout" || in["principal_id"] != "prn_test" || in["enrol_ttl_seconds"] != float64(3600) {
			t.Fatalf("session inputs = %#v", in)
		}
		if _, ok := in["name_open"]; ok {
			t.Fatal("name_open was sent without --open-name")
		}
		if _, ok := in["skip_mcp"]; ok {
			t.Fatal("session submit carried skip_mcp")
		}
		if strings.Contains(out, "human_output") && strings.Contains(out, "vn_enrol_") {
			// human_output is allowed on a first created submit; this fixture has none.
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("add JSON: %v\n%s", err, out)
		}
		if got["request_id"] != "req-session-1" || got["peer_id"] != "peer_session" {
			t.Fatalf("result ids = %v", got)
		}
		if mustRead(t, u.conf) != before {
			t.Fatal("agent add rewrote the local profile")
		}
		if len(h.paths("/v1/enrol")) != 0 || len(h.paths("/v1/admin/dot-links")) != 0 {
			t.Fatal("session add enrolled or linked")
		}

		h2 := newAgentHub(t, func() []onboarding.KindSpec { return specsVersion(version) })
		h2.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_open", ""))
		}
		u = agentOperator(t, h2.srv.URL)
		code, _, errs = u.run("agent", "add", "--kind", "launcher", "--open-name", "--name", "helper bot", "--request-id", "req-open-1")
		if code != 0 {
			t.Fatalf("open add: exit %d %s", code, errs)
		}
		in, _ = h2.posts()[0]["inputs"].(map[string]any)
		if in["name"] != "helper bot" || in["name_open"] != true {
			t.Fatalf("open inputs = %#v", in)
		}
	})

	t.Run("request id retained across retry", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		var mu sync.Mutex
		var seen []string
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			id, _ := body["request_id"].(string)
			mu.Lock()
			seen = append(seen, id)
			attempt := len(seen)
			mu.Unlock()
			if attempt == 1 {
				writeHubProblem(w, http.StatusServiceUnavailable, "unavailable", "try again")
				return
			}
			writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_retry", ""))
		}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("--json", "agent", "add", "--kind", "session", "--name", "Retry", "--request-id", "req-retry-1")
		if code != 0 {
			t.Fatalf("retry add: exit %d\n%s\n%s", code, out, errs)
		}
		mu.Lock()
		got := append([]string(nil), seen...)
		mu.Unlock()
		if len(got) != 2 || got[0] != "req-retry-1" || got[1] != "req-retry-1" {
			t.Fatalf("request ids across retry = %v", got)
		}
		if !strings.Contains(out, "req-retry-1") {
			t.Fatal("success output dropped the request id")
		}

		h = newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			id, _ := body["request_id"].(string)
			mu.Lock()
			seen = append(seen, id)
			attempt := len(seen)
			mu.Unlock()
			if id == "" || strings.Contains(id, " ") {
				writeHubProblem(w, http.StatusBadRequest, "invalid_value", "request_id")
				return
			}
			if attempt == 1 {
				writeHubProblem(w, http.StatusBadGateway, "unavailable", "lost")
				return
			}
			writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_gen", ""))
		}
		seen = nil
		u = agentOperator(t, h.srv.URL)
		code, out, errs = u.run("--json", "agent", "add", "--kind", "session", "--name", "Generated")
		if code != 0 {
			t.Fatalf("generated retry: exit %d\n%s\n%s", code, out, errs)
		}
		mu.Lock()
		got = append([]string(nil), seen...)
		mu.Unlock()
		if len(got) != 2 || got[0] == "" || got[0] != got[1] || !strings.Contains(out, got[0]) {
			t.Fatalf("generated request ids = %v", got)
		}
	})

	t.Run("stale spec refreshes without resubmit", func(t *testing.T) {
		var n int
		h := newAgentHub(t, func() []onboarding.KindSpec {
			n++
			if n == 1 {
				return specsVersion("v1")
			}
			return specsVersion("v2-refreshed")
		})
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			writeHubProblem(w, http.StatusConflict, "stale_spec", "spec_version does not match the kind contract")
		}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("agent", "add", "--kind", "session", "--name", "Stale", "--request-id", "req-stale")
		if code == 0 {
			t.Fatal("stale spec succeeded")
		}
		if len(h.posts()) != 1 {
			t.Fatalf("stale spec resubmitted: %d posts", len(h.posts()))
		}
		if h.posts()[0]["spec_version"] != "v1" {
			t.Fatalf("submitted spec = %v", h.posts()[0]["spec_version"])
		}
		text := out + errs
		if !strings.Contains(text, "v2-refreshed") || !strings.Contains(text, "not resubmitted") {
			t.Fatalf("stale refresh output:\n%s", text)
		}
		if len(h.paths("/v1/admin/agent-kinds")) != 2 {
			t.Fatalf("kinds fetches = %d, want the original and one refresh", len(h.paths("/v1/admin/agent-kinds")))
		}

		n = 0
		h = newAgentHub(t, func() []onboarding.KindSpec {
			n++
			if n == 1 {
				return specsVersion("v1")
			}
			return specsVersion("v2-should-not-appear")
		})
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			writeHubProblem(w, http.StatusConflict, "idempotency_conflict", "request_id was already used with a different request")
		}
		u = agentOperator(t, h.srv.URL)
		code, _, errs = u.run("agent", "add", "--kind", "session", "--name", "Conflict", "--request-id", "req-conflict")
		if code == 0 || !strings.Contains(errs, "idempotency_conflict") {
			t.Fatalf("conflict: exit %d %s", code, errs)
		}
		if len(h.posts()) != 1 || len(h.paths("/v1/admin/agent-kinds")) != 1 || strings.Contains(errs, "v2-should-not-appear") {
			t.Fatal("idempotency conflict refreshed or resubmitted")
		}
	})

	t.Run("unknown field type fails with the selected spec", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec {
			specs := specsVersion("2099-session")
			for i := range specs {
				if specs[i].Kind == "session" {
					specs[i].Fields = append(specs[i].Fields, onboarding.FieldSpec{Name: "attachment", Type: "blob"})
				}
			}
			return specs
		})
		u := agentOperator(t, h.srv.URL)
		code, _, errs := u.run("agent", "add", "--kind", "session", "--name", "Scout", "--request-id", "req-blob")
		if code == 0 {
			t.Fatal("unknown field type was accepted")
		}
		if !strings.Contains(errs, "2099-session") || !strings.Contains(errs, "blob") {
			t.Fatalf("error %q does not name the spec and type", errs)
		}
		if len(h.posts()) != 0 {
			t.Fatal("unknown field type was submitted")
		}
	})

	t.Run("safe status explains queued versus connected", func(t *testing.T) {
		const token = "vn_enrol_statusSecretToken"
		const routine = "https://api2.cursor.sh/automations/webhook/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		const key = "crsr_statusSecretKey"
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.onStatus = func(id string, w http.ResponseWriter) {
			switch id {
			case "req-queued":
				writeJSONMap(w, http.StatusOK, map[string]any{
					"request_id": id, "kind": "grokbot", "peer_id": "peer_q", "principal_id": "prn_q",
					"route_id": "rte_q", "connector_name": "vecnet-helper",
					"auth_state": "unlinked", "status": "created",
					"human_output": token, "webhook_url": routine, "webhook_key": key,
					"setup": map[string]string{"thread_id": "thr_q", "message_id": "msg_q", "delivery_id": "dly_q", "state": "queued"},
				})
			case "req-connected":
				writeJSONMap(w, http.StatusOK, map[string]any{
					"request_id": id, "kind": "grokbot", "peer_id": "peer_c", "principal_id": "prn_c",
					"route_id": "rte_c", "auth_state": "verified", "status": "replayed",
					"human_output": token,
					"setup":        map[string]string{"thread_id": "thr_c", "message_id": "msg_c", "delivery_id": "dly_c", "state": "processed"},
				})
			case "req-accepted":
				writeJSONMap(w, http.StatusOK, map[string]any{
					"request_id": id, "kind": "grokbot", "peer_id": "peer_a", "principal_id": "prn_a",
					"auth_state": "pending", "status": "replayed",
					"setup": map[string]string{"thread_id": "thr_a", "message_id": "msg_a", "delivery_id": "dly_a", "state": "accepted"},
				})
			default:
				writeHubProblem(w, http.StatusNotFound, "not_found", "no such request")
			}
		}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("agent", "status", "req-queued")
		if code != 0 {
			t.Fatalf("queued status: exit %d %s", code, errs)
		}
		text := out + errs
		for _, want := range []string{"req-queued", "peer_q", "rte_q", "dly_q", "outcome: queued, not connected"} {
			if !strings.Contains(text, want) {
				t.Fatalf("queued status missing %q\n%s", want, text)
			}
		}
		if strings.Contains(text, token) || strings.Contains(text, key) || strings.Contains(text, routine) {
			t.Fatal("status printed secret or human output")
		}
		if linePresent(text, "outcome: connected") {
			t.Fatal("queued status said connected")
		}
		got := h.paths("/v1/admin/agents/onboarding/req-queued")
		if len(got) != 1 || got[0].Method != http.MethodGet {
			t.Fatalf("status endpoint = %+v", got)
		}

		code, out, errs = u.run("--json", "agent", "status", "req-connected")
		if code != 0 {
			t.Fatalf("connected status: exit %d %s", code, errs)
		}
		if strings.Contains(out, token) || strings.Contains(out, "human_output") {
			t.Fatal("status JSON kept human_output")
		}
		var safe map[string]any
		if err := json.Unmarshal([]byte(out), &safe); err != nil {
			t.Fatalf("status JSON: %v", err)
		}
		if safe["request_id"] != "req-connected" || safe["peer_id"] != "peer_c" || safe["auth_state"] != "verified" {
			t.Fatalf("connected JSON = %v", safe)
		}
		setup, _ := safe["setup"].(map[string]any)
		if setup["state"] != "processed" || setup["delivery_id"] != "dly_c" {
			t.Fatalf("setup = %v", setup)
		}

		code, out, errs = u.run("agent", "status", "req-accepted")
		if code != 0 {
			t.Fatalf("accepted status: exit %d %s", code, errs)
		}
		if !strings.Contains(out+errs, "outcome: not connected") || linePresent(out+errs, "outcome: connected") {
			t.Fatalf("accepted was treated as connected:\n%s%s", out, errs)
		}
	})

	t.Run("skip-mcp is CLI bearer and does not request a link", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_grok": "none"}
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			res := createdResult(body, "peer_grok", "")
			res["route_id"] = "rte_grok"
			res["connector_name"] = "vecnet-helper"
			res["auth_state"] = "unlinked"
			res["setup"] = map[string]string{"thread_id": "thr_g", "message_id": "msg_g", "delivery_id": "dly_g", "state": "queued"}
			writeAgentResult(w, http.StatusCreated, res)
		}
		u := agentOperator(t, h.srv.URL)
		before := mustRead(t, u.conf)
		code, out, errs := u.run("agent", "add", "--kind", "grokbot", "--name", "Helper",
			"--url", "https://api2.cursor.sh/automations/webhook/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"--key-file", writeModeFile(t, "key", "crsr_skipmcp\n", 0o600),
			"--connector-name", "vecnet-helper", "--skip-mcp", "--request-id", "req-skip")
		text := out + errs
		if code != 0 {
			t.Fatalf("skip-mcp: exit %d\n%s", code, text)
		}
		if strings.Contains(text, "crsr_skipmcp") {
			t.Fatal("skip-mcp printed the routine key")
		}
		in, _ := h.posts()[0]["inputs"].(map[string]any)
		if in["skip_mcp"] != true || in["connector_name"] != "vecnet-helper" || in["webhook_key"] != "crsr_skipmcp" {
			t.Fatalf("skip-mcp inputs = %#v", redactInputs(in))
		}
		if !connectorPattern.MatchString("vecnet-helper") {
			t.Fatal("test connector does not match the locked pattern")
		}
		if len(h.paths("/v1/admin/dot-links")) != 0 {
			t.Fatal("--skip-mcp also requested a link")
		}
		if !strings.Contains(text, "CLI bearer") || !strings.Contains(text, "no link") {
			t.Fatalf("CLI bearer explanation missing:\n%s", text)
		}
		if strings.Contains(text, "hosted OAuth") {
			t.Fatal("CLI bearer output also described hosted OAuth")
		}
		if !strings.Contains(text, "outcome: queued, not connected") || !strings.Contains(text, "req-skip") || !strings.Contains(text, "peer_grok") {
			t.Fatalf("queued explanation missing:\n%s", text)
		}
		if mustRead(t, u.conf) != before || len(h.paths("/v1/enrol")) != 0 {
			t.Fatal("skip-mcp enrolled a profile or rewrote config")
		}

		h = newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_grok": "none"}
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			res := createdResult(body, "peer_grok", "")
			res["setup"] = map[string]string{"thread_id": "thr_g", "message_id": "msg_g", "delivery_id": "dly_g", "state": "queued"}
			writeAgentResult(w, http.StatusCreated, res)
		}
		u = agentOperator(t, h.srv.URL)
		code, out, errs = u.run("agent", "add", "--kind", "grokbot", "--name", "Helper",
			"--url", "https://api2.cursor.sh/automations/webhook/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"--key-file", writeModeFile(t, "key", "crsr_hosted\n", 0o600),
			"--request-id", "req-hosted")
		if code != 0 {
			t.Fatalf("hosted add: exit %d\n%s%s", code, out, errs)
		}
		in, _ = h.posts()[0]["inputs"].(map[string]any)
		if _, ok := in["skip_mcp"]; ok && in["skip_mcp"] != false {
			t.Fatalf("hosted inputs set skip_mcp = %#v", in["skip_mcp"])
		}
		if len(h.paths("/v1/admin/dot-links")) != 1 {
			t.Fatalf("hosted OAuth link calls = %d", len(h.paths("/v1/admin/dot-links")))
		}
		if !strings.Contains(out+errs, "hosted OAuth") || strings.Contains(out+errs, "CLI bearer") {
			t.Fatalf("hosted explanation:\n%s%s", out, errs)
		}
	})

	t.Run("connector override uses the locked pattern", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		u := agentOperator(t, h.srv.URL)
		code, _, errs := u.run("agent", "add", "--kind", "grokbot", "--name", "Helper",
			"--url", "https://api2.cursor.sh/automations/webhook/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"--key-file", writeModeFile(t, "key", "crsr_pattern\n", 0o600),
			"--connector-name", "not-vecnet", "--skip-mcp", "--request-id", "req-bad-name")
		if code == 0 || !strings.Contains(errs, connectorPattern.String()) {
			t.Fatalf("bad connector: exit %d %s", code, errs)
		}
		if len(h.posts()) != 0 || strings.Contains(errs, "crsr_pattern") {
			t.Fatal("bad connector was submitted or echoed the key")
		}
	})

	t.Run("invitation golden output is unchanged", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("testdata", "invite.json"))
		if err != nil {
			t.Fatal(err)
		}
		var fix struct {
			Blocks []struct {
				Owner     string `json:"owner"`
				HubURL    string `json:"hub_url"`
				Profile   string `json:"profile"`
				Token     string `json:"token"`
				ExpiresAt string `json:"expires_at"`
				Name      string `json:"name"`
				Block     string `json:"block"`
				Open      bool   `json:"open"`
			} `json:"blocks"`
		}
		if err := json.Unmarshal(raw, &fix); err != nil {
			t.Fatal(err)
		}
		for _, idx := range []int{0, 3} {
			c := fix.Blocks[idx]
			name := c.Name
			if name == "" {
				name = "Scout"
			}
			h := newAgentHub(t, func() []onboarding.KindSpec { return nil })
			h.onAny = func(r *http.Request, w http.ResponseWriter, body []byte) bool {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/peers" {
					return false
				}
				writeJSONMap(w, http.StatusCreated, map[string]any{
					"peer_id": "peer_golden", "name": name, "name_open": c.Open, "principal_id": "prn_golden",
					"enrol_token": c.Token, "enrol_expires_at": c.ExpiresAt, "hub_url": c.HubURL, "hub_owner": c.Owner,
				})
				return true
			}
			u := agentOperator(t, h.srv.URL)
			args := []string{"peer", "invite", "--name", name}
			if c.Open {
				args = append(args, "--open-name")
			}
			code, out, errs := u.run(args...)
			if code != 0 {
				t.Fatalf("peer invite %s: exit %d %s", c.Profile, code, errs)
			}
			if out != c.Block {
				t.Fatalf("invite block for %s changed\n got: %s\nwant: %s", c.Profile, out, c.Block)
			}
		}
	})

	t.Run("priority stays required", func(t *testing.T) {
		u := agentOperator(t, "http://127.0.0.1:9")
		code, _, errs := u.run("send", "peer_x", "hello")
		if code != 1 || !strings.Contains(errs, "no default") {
			t.Fatalf("send without priority: exit %d %s", code, errs)
		}
		code, _, errs = u.run("reply", "msg_x", "hello")
		if code != 1 || !strings.Contains(errs, "no default") {
			t.Fatalf("reply without priority: exit %d %s", code, errs)
		}
	})

	t.Run("help lists the agent commands", func(t *testing.T) {
		code, out, errs := newUser(t).run("help")
		if code != 0 {
			t.Fatalf("help: exit %d %s", code, errs)
		}
		for _, s := range []string{"agent kinds", "agent add", "agent status", "agent link", "--skip-mcp", "--key-file", "--confirm-revoke", "--connector-name"} {
			if !strings.Contains(out, s) {
				t.Fatalf("help missing %q", s)
			}
		}
		if strings.Contains(out, "keep-bearer") {
			t.Fatal("help offers a keep-bearer flag")
		}
	})
}

func TestAgentCLIKeyFileBoundary(t *testing.T) {
	const secret = "crsr_boundarySecretValue"
	const routine = "https://api2.cursor.sh/automations/webhook/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	refused := func(t *testing.T, args []string, env map[string]string) {
		t.Helper()
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		u := agentOperator(t, h.srv.URL)
		for k, v := range env {
			u.env[k] = v
		}
		code, out, errs := u.run(args...)
		if strings.Contains(errs, "unknown command") {
			t.Fatalf("command not implemented: %s", errs)
		}
		if code == 0 {
			t.Fatal("key boundary succeeded")
		}
		if len(h.posts()) != 0 {
			t.Fatal("key boundary reached the hub")
		}
		if strings.Contains(out+errs, secret) || strings.Contains(out+errs, "crsr_") {
			t.Fatal("key boundary printed secret material")
		}
	}
	base := func(extra ...string) []string {
		args := []string{"agent", "add", "--kind", "grokbot", "--name", "Helper", "--url", routine, "--skip-mcp", "--request-id", "req-key"}
		return append(args, extra...)
	}

	t.Run("empty", func(t *testing.T) {
		refused(t, base("--key-file", writeModeFile(t, "empty", "\n", 0o600)), nil)
	})
	t.Run("whitespace", func(t *testing.T) {
		refused(t, base("--key-file", writeModeFile(t, "blank", "  \t\n", 0o600)), nil)
	})
	t.Run("oversized", func(t *testing.T) {
		body := secret + strings.Repeat("a", 5000)
		refused(t, base("--key-file", writeModeFile(t, "big", body, 0o600)), nil)
	})
	t.Run("missing file", func(t *testing.T) {
		refused(t, base("--key-file", filepath.Join(t.TempDir(), "missing")), nil)
	})
	t.Run("unreadable directory", func(t *testing.T) {
		refused(t, base("--key-file", t.TempDir()), nil)
	})
	t.Run("nested bearer", func(t *testing.T) {
		refused(t, base("--key-file", writeModeFile(t, "nest", "Bearer Bearer "+secret+"\n", 0o600)), nil)
	})
	t.Run("no secret flag", func(t *testing.T) {
		refused(t, append(base(), "--key", secret), nil)
		refused(t, append(base(), "--webhook-key="+secret), nil)
	})
	t.Run("no secret env", func(t *testing.T) {
		refused(t, base(), map[string]string{"VECNET_WEBHOOK_KEY": secret, "VECNET_KEY": secret})
	})

	for _, c := range []struct {
		name, body string
	}{
		{"raw", secret + "\n"},
		{"header", "Bearer " + secret + "\n"},
		{"header case and space", "  bearer " + secret + " \r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
			h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
				res := createdResult(body, "peer_key", "")
				res["setup"] = map[string]string{"thread_id": "thr_k", "message_id": "msg_k", "delivery_id": "dly_k", "state": "queued"}
				writeAgentResult(w, http.StatusCreated, res)
			}
			u := agentOperator(t, h.srv.URL)
			code, out, errs := u.run(base("--key-file", writeModeFile(t, "key", c.body, 0o600))...)
			if code != 0 {
				t.Fatalf("normalized add: exit %d\n%s%s", code, out, errs)
			}
			if strings.Contains(out+errs, secret) || strings.Contains(out+errs, "Bearer") {
				t.Fatal("normalized add printed the key")
			}
			in, _ := h.posts()[0]["inputs"].(map[string]any)
			if in["webhook_key"] != secret || in["skip_mcp"] != true {
				t.Fatal("file was not normalized to the raw CLI-bearer key")
			}
			if len(h.paths("/v1/admin/dot-links")) != 0 {
				t.Fatal("CLI bearer requested a link")
			}
		})
	}

	t.Run("mode warning still sends", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_mode", ""))
		}
		u := agentOperator(t, h.srv.URL)
		path := writeModeFile(t, "open", secret+"\n", 0o644)
		code, out, errs := u.run(base("--key-file", path)...)
		if code != 0 {
			t.Fatalf("0644 key: exit %d %s", code, errs)
		}
		if !strings.Contains(errs, "0600") && !strings.Contains(errs, "600") {
			t.Fatalf("mode warning missing: %s", errs)
		}
		if strings.Contains(out+errs, secret) {
			t.Fatal("mode warning printed the key")
		}
	})

	t.Run("hub error does not echo the secret", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprintf(w, `{"error":{"code":"invalid_value","message":"rejected %s at %s","details":{"field":"webhook_key","echo":"%s"}}}`, secret, routine, secret)
		}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run(base("--key-file", writeModeFile(t, "key", "Bearer "+secret+"\n", 0o600))...)
		if code == 0 {
			t.Fatal("hub error was success")
		}
		if strings.Contains(out+errs, secret) || strings.Contains(out+errs, routine) || strings.Contains(out+errs, "Bearer") {
			t.Fatal("hub error output contained the routine key or URL")
		}
		if len(h.posts()) != 1 {
			t.Fatalf("posts = %d", len(h.posts()))
		}
	})
}

func TestAgentCLIBearerRevoke(t *testing.T) {
	const routine = "https://api2.cursor.sh/automations/webhook/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	const key = "crsr_revokeBoundary"
	grokArgs := func(peerFile string, extra ...string) []string {
		args := []string{"agent", "add", "--kind", "grokbot", "--name", "Helper", "--url", routine,
			"--key-file", peerFile, "--request-id", "req-revoke"}
		return append(args, extra...)
	}

	t.Run("agent link refuses a live bearer without confirmation", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_live": "active", "peer_fresh": "none", "peer_conductor": "none", "peer_other": "active"}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("agent", "link", "peer_live")
		if code == 0 {
			t.Fatal("link without confirmation succeeded")
		}
		if len(h.paths("/v1/admin/dot-links")) != 0 {
			t.Fatal("unconfirmed link called peer link")
		}
		if !strings.Contains(out+errs, "revoked") || !strings.Contains(out+errs, "peer_live") {
			t.Fatalf("warning missing:\n%s%s", out, errs)
		}
		if strings.Contains(out+errs, "ABCD-EFGH-JKMN") {
			t.Fatal("unconfirmed link printed a code")
		}
	})

	t.Run("confirmation links and there is no keep-bearer flag", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_live": "active"}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("agent", "link", "peer_live", "--confirm-revoke")
		if code != 0 {
			t.Fatalf("confirmed link: exit %d %s", code, errs)
		}
		links := h.paths("/v1/admin/dot-links")
		if len(links) != 1 || !strings.Contains(links[0].Body, "peer_live") {
			t.Fatalf("confirmed link body = %+v", links)
		}
		if !strings.Contains(out+errs, "ABCD-EFGH-JKMN") || !strings.Contains(out+errs, "revoked") {
			t.Fatalf("confirmed link output:\n%s%s", out, errs)
		}
		code, _, errs = u.run("agent", "link", "peer_live", "--keep-bearer")
		if code == 0 || len(h.paths("/v1/admin/dot-links")) != 1 {
			t.Fatalf("keep-bearer: exit %d links %d %s", code, len(h.paths("/v1/admin/dot-links")), errs)
		}
	})

	t.Run("no live bearer links without the flag", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_fresh": "none"}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("agent", "link", "peer_fresh")
		if code != 0 || len(h.paths("/v1/admin/dot-links")) != 1 {
			t.Fatalf("fresh link: exit %d calls %d %s", code, len(h.paths("/v1/admin/dot-links")), errs)
		}
		if !strings.Contains(out, "ABCD-EFGH-JKMN") {
			t.Fatalf("fresh link output: %s%s", out, errs)
		}
	})

	t.Run("peer id is not special", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_conductor": "none", "peer_other": "active"}
		u := agentOperator(t, h.srv.URL)
		code, _, errs := u.run("agent", "link", "peer_conductor")
		if code != 0 || len(h.paths("/v1/admin/dot-links")) != 1 {
			t.Fatalf("peer_conductor with no bearer: exit %d calls %d %s", code, len(h.paths("/v1/admin/dot-links")), errs)
		}
		code, _, errs = u.run("agent", "link", "peer_other")
		if code == 0 || len(h.paths("/v1/admin/dot-links")) != 1 {
			t.Fatalf("other live bearer was linked: exit %d calls %d %s", code, len(h.paths("/v1/admin/dot-links")), errs)
		}
	})

	t.Run("agent add confirms before its guided link", func(t *testing.T) {
		path := writeModeFile(t, "key", key+"\n", 0o600)
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_grok": "active"}
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			res := createdResult(body, "peer_grok", "")
			res["setup"] = map[string]string{"thread_id": "thr_r", "message_id": "msg_r", "delivery_id": "dly_r", "state": "queued"}
			writeAgentResult(w, http.StatusCreated, res)
		}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run(grokArgs(path)...)
		if code == 0 {
			t.Fatal("add linked a live bearer without confirmation")
		}
		if len(h.posts()) != 1 || len(h.paths("/v1/admin/dot-links")) != 0 {
			t.Fatalf("unconfirmed add posts %d links %d", len(h.posts()), len(h.paths("/v1/admin/dot-links")))
		}
		if strings.Contains(out+errs, key) || strings.Contains(out+errs, "ABCD-EFGH-JKMN") {
			t.Fatal("unconfirmed add printed the key or a link code")
		}
		if !strings.Contains(out+errs, "revoked") || !strings.Contains(out+errs, "peer_grok") {
			t.Fatalf("add warning missing:\n%s%s", out, errs)
		}

		h = newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_grok": "active"}
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_grok", ""))
		}
		u = agentOperator(t, h.srv.URL)
		code, out, errs = u.run(grokArgs(path, "--confirm-revoke")...)
		if code != 0 || len(h.paths("/v1/admin/dot-links")) != 1 {
			t.Fatalf("confirmed add: exit %d links %d %s", code, len(h.paths("/v1/admin/dot-links")), errs)
		}
		if !strings.Contains(h.paths("/v1/admin/dot-links")[0].Body, "peer_grok") {
			t.Fatal("confirmed add linked a different peer")
		}
		if strings.Contains(out+errs, key) {
			t.Fatal("confirmed add printed the key")
		}

		h = newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_grok": "active"}
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_grok", ""))
		}
		u = agentOperator(t, h.srv.URL)
		code, _, errs = u.run(grokArgs(path, "--skip-mcp")...)
		if code != 0 || len(h.paths("/v1/admin/dot-links")) != 0 {
			t.Fatalf("skip-mcp with a live bearer: exit %d links %d %s", code, len(h.paths("/v1/admin/dot-links")), errs)
		}

		code, _, errs = u.run(append(grokArgs(path), "--keep-bearer")...)
		if code == 0 {
			t.Fatalf("keep-bearer on add was accepted: %s", errs)
		}
	})

	t.Run("direct peer link still revokes without a new flag", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.peers = map[string]string{"peer_live": "active"}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("peer", "link", "peer_live")
		if code != 0 {
			t.Fatalf("peer link: exit %d %s", code, errs)
		}
		if len(h.paths("/v1/admin/dot-links")) != 1 || len(h.paths("/v1/admin/peers")) != 0 {
			t.Fatalf("peer link calls: links %d peer-list %d", len(h.paths("/v1/admin/dot-links")), len(h.paths("/v1/admin/peers")))
		}
		if !strings.Contains(out, "ABCD-EFGH-JKMN") {
			t.Fatalf("peer link output: %s", out)
		}
	})

	for _, name := range []string{"agent.go", "commands.go", "invite.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(raw)), "conductor") {
			t.Fatalf("%s special-cases a peer", name)
		}
	}
}

var connectorPattern = regexp.MustCompile(`^vecnet-[a-z0-9-]{1,48}$`)

func agentOperator(t *testing.T, hubURL string) *user {
	t.Helper()
	u := newUser(t)
	u.env["VECNET_PROFILE"] = "p"
	if err := os.MkdirAll(filepath.Dir(u.conf), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[profiles.p]\nhub = \"" + hubURL + "\"\nsecret = \"vn_operator_test\"\nhub_pubkey = \"ed25519:test\"\n"
	if err := os.WriteFile(u.conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return u
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeModeFile(t *testing.T, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func specsVersion(v string) []onboarding.KindSpec {
	specs := onboarding.Specs()
	for i := range specs {
		specs[i].SpecVersion = v
	}
	return specs
}

func createdResult(body map[string]any, peer, human string) map[string]any {
	id, _ := body["request_id"].(string)
	kind, _ := body["kind"].(string)
	res := map[string]any{
		"request_id": id, "kind": kind, "peer_id": peer, "principal_id": "prn_created",
		"auth_state": "unlinked", "status": "created",
	}
	if human != "" {
		res["human_output"] = human
	}
	return res
}

func redactInputs(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if k == "webhook_key" || k == "webhook_url" {
			out[k] = "redacted"
			continue
		}
		out[k] = v
	}
	return out
}

func linePresent(s, line string) bool {
	for _, l := range strings.Split(s, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

type agentHit struct {
	Method, Path, Auth, Body string
}

type agentHub struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	hits     []agentHit
	kinds    func() []onboarding.KindSpec
	peers    map[string]string
	onPost   func(n int, body map[string]any, w http.ResponseWriter)
	onStatus func(id string, w http.ResponseWriter)
	onAny    func(r *http.Request, w http.ResponseWriter, body []byte) bool
	postsN   int
}

func newAgentHub(t *testing.T, kinds func() []onboarding.KindSpec) *agentHub {
	t.Helper()
	h := &agentHub{t: t, kinds: kinds, peers: map[string]string{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *agentHub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	h.mu.Lock()
	h.hits = append(h.hits, agentHit{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: string(body)})
	h.mu.Unlock()
	if r.Header.Get("Authorization") != agentSecretHeader {
		writeHubProblem(w, http.StatusUnauthorized, "unauthenticated", "missing credential")
		return
	}
	if h.onAny != nil && h.onAny(r, w, body) {
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/agent-kinds":
		writeJSONMap(w, http.StatusOK, map[string]any{"kinds": h.kinds()})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/agents":
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			writeHubProblem(w, http.StatusBadRequest, "invalid_body", "body must be a JSON object")
			return
		}
		h.mu.Lock()
		h.postsN++
		n := h.postsN
		h.mu.Unlock()
		if h.onPost != nil {
			h.onPost(n, payload, w)
			return
		}
		writeAgentResult(w, http.StatusCreated, createdResult(payload, "peer_created", ""))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/admin/agents/onboarding/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/admin/agents/onboarding/")
		if h.onStatus != nil {
			h.onStatus(id, w)
			return
		}
		writeHubProblem(w, http.StatusNotFound, "not_found", "no such request")
	case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/peers":
		rows := []map[string]any{}
		for id, cred := range h.peers {
			rows = append(rows, map[string]any{
				"credential": cred,
				"peer":       map[string]any{"peer_id": id, "name": id, "kind": "grokbot"},
			})
		}
		writeJSONMap(w, http.StatusOK, map[string]any{"peers": rows})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/dot-links":
		writeJSONMap(w, http.StatusCreated, map[string]string{"code": "ABCD-EFGH-JKMN", "expires_at": "2026-11-06T00:10:00.000Z"})
	default:
		writeHubProblem(w, http.StatusNotFound, "not_found", "no such path")
	}
}

func (h *agentHub) paths(path string) []agentHit {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []agentHit
	for _, hit := range h.hits {
		if hit.Path == path {
			out = append(out, hit)
		}
	}
	return out
}

func (h *agentHub) posts() []map[string]any {
	var out []map[string]any
	for _, hit := range h.paths("/v1/admin/agents") {
		var body map[string]any
		if json.Unmarshal([]byte(hit.Body), &body) != nil {
			continue
		}
		out = append(out, body)
	}
	return out
}

func writeJSONMap(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAgentResult(w http.ResponseWriter, status int, v map[string]any) {
	writeJSONMap(w, status, v)
}

func writeHubProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSONMap(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
