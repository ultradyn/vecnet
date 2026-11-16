package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/onboarding"
	"github.com/XertroV/vecnet/internal/platform"
	"github.com/XertroV/vecnet/internal/testhub"
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
			t.Fatalf("kinds endpoint = %s", agentHitDiag(got))
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
		if in["name"] != "Scout" {
			t.Fatal("session inputs: name")
		}
		if in["principal_id"] != "prn_test" {
			t.Fatal("session inputs: principal_id")
		}
		if in["enrol_ttl_seconds"] != float64(3600) {
			t.Fatal("session inputs: enrol_ttl_seconds")
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
		if in["name"] != "helper bot" {
			t.Fatal("open inputs: name")
		}
		if in["name_open"] != true {
			t.Fatal("open inputs: name_open")
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
			t.Fatalf("status endpoint = %s", agentHitDiag(got))
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
		if in["skip_mcp"] != true {
			t.Fatal("skip-mcp inputs: skip_mcp")
		}
		if in["connector_name"] != "vecnet-helper" {
			t.Fatal("skip-mcp inputs: connector_name")
		}
		if in["webhook_key"] != "crsr_skipmcp" {
			t.Fatal("skip-mcp inputs: webhook_key")
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
			t.Fatal(linkFailureDiag("hosted add", out, errs))
		}
		in, _ = h.posts()[0]["inputs"].(map[string]any)
		if _, ok := in["skip_mcp"]; ok && in["skip_mcp"] != false {
			t.Fatal("hosted inputs set skip_mcp")
		}
		if len(h.paths("/v1/admin/dot-links")) != 1 {
			t.Fatalf("hosted OAuth link calls = %d", len(h.paths("/v1/admin/dot-links")))
		}
		if !strings.Contains(out+errs, "hosted OAuth") || strings.Contains(out+errs, "CLI bearer") {
			t.Fatal("hosted explanation missing hosted OAuth or also described CLI bearer")
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
				t.Fatal(inviteBlockDiag(c.Profile, out, c.Block))
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

// (t5 I1) A generated request id has to be visible before the submit POST
// and again in the exhausted-retry instructions. Two lost responses after a
// commit must still recover through status and a same-id retry, without a
// second id on the automatic redispatch.
func TestAgentCLIRequestIDRecovery(t *testing.T) {
	t.Run("(t5 I1) committed but unanswered submit recovers with the displayed id", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		var mu sync.Mutex
		var posted []string
		var committed string
		var stderrAtPost string
		lose := true
		errb := &snapBuf{}
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			id, _ := body["request_id"].(string)
			seen := errb.String()
			mu.Lock()
			if stderrAtPost == "" {
				stderrAtPost = seen
			}
			posted = append(posted, id)
			if committed == "" {
				committed = id
			}
			nposts := len(posted)
			stillLose := lose
			mu.Unlock()
			if stillLose {
				writeHubProblem(w, http.StatusServiceUnavailable, "unavailable", "lost response")
				return
			}
			if id != committed || nposts < 3 {
				writeAgentResult(w, http.StatusCreated, createdResult(body, "peer_second", ""))
				return
			}
			res := createdResult(body, "peer_committed", "")
			res["status"] = "replayed"
			writeAgentResult(w, http.StatusOK, res)
		}
		h.onStatus = func(id string, w http.ResponseWriter) {
			mu.Lock()
			want := committed
			mu.Unlock()
			if id == "" || id != want {
				writeHubProblem(w, http.StatusNotFound, "not_found", "no such request")
				return
			}
			writeJSONMap(w, http.StatusOK, map[string]any{
				"request_id": id, "kind": "session", "peer_id": "peer_committed",
				"principal_id": "prn_committed", "auth_state": "unlinked", "status": "replayed",
			})
		}
		u := agentOperator(t, h.srv.URL)
		var out bytes.Buffer
		code := cli.Run([]string{"agent", "add", "--kind", "session", "--name", "Lost"}, strings.NewReader(""), &out, errb, u.getenv)
		mu.Lock()
		gotPosted := append([]string(nil), posted...)
		id := committed
		seenBefore := stderrAtPost
		mu.Unlock()
		if code != 2 {
			t.Fatalf("lost submit: exit %d, want 2", code)
		}
		if len(gotPosted) != 2 || gotPosted[0] == "" || gotPosted[0] != gotPosted[1] {
			t.Fatalf("automatic redispatch ids = %v, want one id on two attempts", gotPosted)
		}
		if id != gotPosted[0] {
			t.Fatalf("committed id = %q, posted %q", id, gotPosted[0])
		}
		if !strings.Contains(seenBefore, "request_id "+id) {
			t.Fatal("generated request id was not exposed before POST")
		}
		if strings.Contains(seenBefore, "was not answered") {
			t.Fatal("recovery instructions were printed before the submit attempt")
		}
		errs := errb.String()
		if out.Len() != 0 {
			t.Fatal("lost submit wrote a result on stdout")
		}
		for _, want := range []string{
			"request_id " + id,
			"was not answered",
			"may already be committed",
			"vecnet --net 'p' agent status '" + id + "'",
			"--request-id '" + id + "'",
		} {
			if !strings.Contains(errs, want) {
				t.Fatalf("recovery instructions missing %q", want)
			}
		}

		mu.Lock()
		lose = false
		mu.Unlock()
		code, statusOut, statusErr := u.run("agent", "status", id)
		if code != 0 {
			t.Fatalf("status recovery: exit %d %s", code, statusErr)
		}
		if !strings.Contains(statusOut, id) || !strings.Contains(statusOut, "peer_committed") {
			t.Fatal("status recovery did not return the committed receipt")
		}
		mu.Lock()
		postsAfterStatus := len(posted)
		mu.Unlock()
		if postsAfterStatus != 2 {
			t.Fatalf("status recovery issued another submit (%d posts)", postsAfterStatus)
		}

		code, replayOut, replayErr := u.run("--json", "agent", "add", "--kind", "session", "--name", "Lost", "--request-id", id)
		if code != 0 {
			t.Fatalf("same-id retry: exit %d\n%s\n%s", code, replayOut, replayErr)
		}
		var replay map[string]any
		if err := json.Unmarshal([]byte(replayOut), &replay); err != nil {
			t.Fatalf("replay JSON: %v", err)
		}
		if replay["request_id"] != id || replay["peer_id"] != "peer_committed" || replay["status"] != "replayed" {
			t.Fatal("same-id retry did not recover the committed receipt")
		}
		mu.Lock()
		gotPosted = append([]string(nil), posted...)
		mu.Unlock()
		if len(gotPosted) != 3 {
			t.Fatalf("posts after recovery = %d, want 3", len(gotPosted))
		}
		for _, postedID := range gotPosted {
			if postedID != id {
				t.Fatalf("recovery invented another request id: %v", gotPosted)
			}
		}
	})

	// A 503 followed by a malformed HTTP 200 is not retryable on the second
	// attempt, and a deadline is not retried at all. Both can follow a commit,
	// so both still need the same-id status instructions.
	t.Run("(t5 I1 r2 mixed) malformed success after a retry still prints recovery", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		var mu sync.Mutex
		var posted []string
		h.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			id, _ := body["request_id"].(string)
			mu.Lock()
			posted = append(posted, id)
			attempt := len(posted)
			mu.Unlock()
			if attempt == 1 {
				writeHubProblem(w, http.StatusServiceUnavailable, "unavailable", "lost response")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"request_id":`)
		}
		u := agentOperator(t, h.srv.URL)
		code, out, errs := u.run("agent", "add", "--kind", "session", "--name", "Mixed", "--request-id", "req-mixed-1")
		mu.Lock()
		got := append([]string(nil), posted...)
		mu.Unlock()
		if len(got) != 2 || got[0] != "req-mixed-1" || got[1] != "req-mixed-1" {
			t.Fatalf("posts = %v, want two attempts with one id", got)
		}
		if code == 0 || out != "" {
			t.Fatalf("mixed submit: exit %d stdout %q", code, out)
		}
		for _, want := range []string{
			"request_id req-mixed-1",
			"was not answered",
			"may already be committed",
			"agent status",
			"--request-id",
			"req-mixed-1",
		} {
			if !strings.Contains(errs, want) {
				t.Fatalf("recovery instructions missing %q\n%s", want, errs)
			}
		}
	})

	t.Run("(t5 I1 r2 deadline) a request deadline still prints recovery", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		var mu sync.Mutex
		var posted []string
		h.onAny = func(r *http.Request, w http.ResponseWriter, body []byte) bool {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/agents" {
				return false
			}
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			id, _ := payload["request_id"].(string)
			mu.Lock()
			posted = append(posted, id)
			mu.Unlock()
			<-r.Context().Done()
			return true
		}
		u := agentOperator(t, h.srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		var out, errb bytes.Buffer
		code := cli.RunContext(ctx, []string{
			"agent", "add", "--kind", "session", "--name", "Deadline", "--request-id", "req-deadline-1",
		}, strings.NewReader(""), &out, &errb, u.getenv)
		mu.Lock()
		got := append([]string(nil), posted...)
		mu.Unlock()
		if len(got) != 1 || got[0] != "req-deadline-1" {
			t.Fatalf("posts = %v, want one attempt with the exposed id", got)
		}
		if code == 0 || out.Len() != 0 {
			t.Fatalf("deadline submit: exit %d stdout %q", code, out.String())
		}
		errs := errb.String()
		for _, want := range []string{
			"request_id req-deadline-1",
			"was not answered",
			"may already be committed",
			"agent status",
			"--request-id",
			"req-deadline-1",
		} {
			if !strings.Contains(errs, want) {
				t.Fatalf("recovery instructions missing %q\n%s", want, errs)
			}
		}
	})

	// Cancellation after the hub has accepted the POST is not retried, and it
	// can still follow a commit, so the same-id instructions have to be printed.
	t.Run("(t5 I1 r3 canceled) canceling an accepted submit still prints recovery", func(t *testing.T) {
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		var mu sync.Mutex
		var posted []string
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.onAny = func(r *http.Request, w http.ResponseWriter, body []byte) bool {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/agents" {
				return false
			}
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			id, _ := payload["request_id"].(string)
			mu.Lock()
			posted = append(posted, id)
			mu.Unlock()
			cancel()
			<-r.Context().Done()
			return true
		}
		u := agentOperator(t, h.srv.URL)
		var out, errb bytes.Buffer
		code := cli.RunContext(ctx, []string{
			"agent", "add", "--kind", "session", "--name", "Canceled", "--request-id", "req-canceled-1",
		}, strings.NewReader(""), &out, &errb, u.getenv)
		mu.Lock()
		got := append([]string(nil), posted...)
		mu.Unlock()
		if len(got) != 1 || got[0] != "req-canceled-1" {
			t.Fatalf("posts = %v, want one attempt and no automatic retry", got)
		}
		if code == 0 || out.Len() != 0 {
			t.Fatalf("canceled submit: exit %d stdout %q", code, out.String())
		}
		errs := errb.String()
		for _, want := range []string{
			"request_id req-canceled-1",
			"was not answered",
			"may already be committed",
			"agent status",
			"--request-id",
			"req-canceled-1",
		} {
			if !strings.Contains(errs, want) {
				t.Fatalf("recovery instructions missing %q\n%s", want, errs)
			}
		}
	})
}

// (t5 I1 r3 relocation) A committed submit whose response is lost, then a
// same-id retry that receives a signed 410, must keep that generated id
// when the verified follow redispatches the command. A second id creates
// a second peer on the hub that was moved to.
func TestAgentCLIRelocationRequestID(t *testing.T) {
	t.Run("(t5 I1 r3 relocation) lost response then signed move keeps one id and one peer", func(t *testing.T) {
		origin := testhub.Start(t)
		key := origin.Hub.IdentityKey()
		var (
			mu      sync.Mutex
			posts   int
			movedTo *testhub.Env
			front   *httptest.Server
		)
		front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if r.Method == http.MethodGet && r.URL.Path == "/v1/hub/identity" {
				sig, err := key.SignChallenge(front.URL, r.URL.Query().Get("challenge"))
				if err != nil {
					writeHubProblem(w, http.StatusBadRequest, "invalid_value", "challenge")
					return
				}
				writeJSONMap(w, http.StatusOK, envelope.HubIdentity{
					Pubkey: key.Pubkey(), PublicURL: front.URL, ChallengeSig: sig,
				})
				return
			}
			if r.Method == http.MethodPost && r.URL.Path == "/v1/admin/agents" {
				mu.Lock()
				posts++
				n := posts
				mu.Unlock()
				if n == 1 {
					status, hdr, raw, err := proxyHub(origin.URL, r, body)
					if err != nil || status < 200 || status > 299 {
						writeHubProblem(w, http.StatusBadGateway, "unavailable", "origin did not commit")
						return
					}
					_ = hdr
					_ = raw
					movedTo = origin.CloneDB(t)
					writeHubProblem(w, http.StatusServiceUnavailable, "unavailable", "lost response")
					return
				}
				if movedTo == nil {
					writeHubProblem(w, http.StatusBadGateway, "unavailable", "no destination")
					return
				}
				notice := envelope.HubNotice{
					NoticeID:  "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV",
					Kind:      "moving",
					Hub:       front.URL,
					NewURL:    movedTo.URL,
					Message:   "move",
					IssuedAt:  envelope.FormatTime(time.Now().Add(-time.Second)),
					ExpiresAt: envelope.FormatTime(time.Now().Add(time.Hour)),
				}
				if err := key.SignNotice(&notice); err != nil {
					writeHubProblem(w, http.StatusBadGateway, "unavailable", "notice")
					return
				}
				writeJSONMap(w, http.StatusGone, map[string]any{
					"error": map[string]any{
						"code":    "hub_moved",
						"message": "this hub moved to " + movedTo.URL,
						"details": map[string]any{"notice": notice},
					},
				})
				return
			}
			status, hdr, raw, err := proxyHub(origin.URL, r, body)
			if err != nil {
				writeHubProblem(w, http.StatusBadGateway, "unavailable", "origin unreachable")
				return
			}
			if hdr.Get("Content-Type") != "" {
				w.Header().Set("Content-Type", hdr.Get("Content-Type"))
			}
			w.WriteHeader(status)
			_, _ = w.Write(raw)
		}))
		t.Cleanup(front.Close)

		u := newUser(t)
		u.env["VECNET_PROFILE"] = "p"
		if err := cliFixtureMkdirAll(filepath.Dir(u.conf), 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\nhub_pubkey = %q\n", front.URL, origin.OperatorSecret, key.Pubkey())
		if err := cliFixtureWriteFile(u.conf, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errs := u.run("agent", "add", "--kind", "session", "--open-name")
		if code != 0 {
			t.Fatalf("relocated add: exit %d\n%s", code, errs)
		}
		ids := exposedRequestIDs(errs)
		if len(ids) < 2 || ids[0] == "" {
			t.Fatalf("exposed request ids = %v\n%s", ids, errs)
		}
		for _, id := range ids {
			if id != ids[0] {
				t.Fatalf("relocation redispatch changed the request id: %v", ids)
			}
		}
		if movedTo == nil {
			t.Fatal("move did not clone the committed hub")
		}
		peers := sessionPeerIDs(t, movedTo)
		if len(peers) != 1 {
			t.Fatalf("session peers after verified follow = %d, want 1 (%v)", len(peers), peers)
		}
		if !strings.Contains(out, peers[0]) || !strings.Contains(out, ids[0]) {
			t.Fatal("result did not show the one peer and the original request id")
		}
	})
}

func proxyHub(dest string, r *http.Request, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequest(r.Method, dest+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw, err
}

func exposedRequestIDs(errs string) []string {
	var ids []string
	for _, line := range strings.Split(errs, "\n") {
		const prefix = "request_id "
		if strings.HasPrefix(line, prefix) {
			id, _, _ := strings.Cut(strings.TrimPrefix(line, prefix), " ")
			if id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func sessionPeerIDs(t *testing.T, env *testhub.Env) []string {
	t.Helper()
	resp, raw := env.Do(env.OperatorSecret, http.MethodGet, "/v1/admin/peers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list peers: %d", resp.StatusCode)
	}
	var body struct {
		Peers []struct {
			Peer struct {
				PeerID string `json:"peer_id"`
				Kind   string `json:"kind"`
			} `json:"peer"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range body.Peers {
		if row.Peer.Kind == "session" {
			ids = append(ids, row.Peer.PeerID)
		}
	}
	return ids
}

// (t5 M-profile) The recovery command has to carry the profile that submitted,
// quoted so a shell copies it as one word. A bare follow-up must not refuse
// in an agent run or query the human operator profile.
func TestAgentCLIRecoveryProfile(t *testing.T) {
	t.Run("(t5 M-profile) recovery command keeps the selected profile", func(t *testing.T) {
		const profile = "sec'ond"
		const reqID = "req-profile-1"
		secondary := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		operator := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		var mu sync.Mutex
		var posted []string
		secondary.onPost = func(n int, body map[string]any, w http.ResponseWriter) {
			id, _ := body["request_id"].(string)
			mu.Lock()
			posted = append(posted, id)
			mu.Unlock()
			writeHubProblem(w, http.StatusServiceUnavailable, "unavailable", "lost response")
		}
		secondary.onStatus = func(id string, w http.ResponseWriter) {
			if id != reqID {
				writeHubProblem(w, http.StatusNotFound, "not_found", "no such request")
				return
			}
			writeJSONMap(w, http.StatusOK, map[string]any{
				"request_id": id, "kind": "session", "peer_id": "peer_secondary",
				"principal_id": "prn_secondary", "auth_state": "unlinked", "status": "replayed",
			})
		}
		u := newUser(t)
		u.env["VECNET_AGENT"] = "1"
		writeAgentProfiles(t, u, "op", map[string]string{
			"op":    operator.srv.URL,
			profile: secondary.srv.URL,
		})
		code, out, errs := u.run("--net", profile, "agent", "add", "--kind", "session", "--name", "Kept", "--request-id", reqID)
		mu.Lock()
		gotPosted := append([]string(nil), posted...)
		mu.Unlock()
		if len(gotPosted) != 2 || gotPosted[0] != reqID || gotPosted[1] != reqID {
			t.Fatalf("posts = %v, want two attempts with one id", gotPosted)
		}
		if code == 0 || out != "" {
			t.Fatalf("profile add: exit %d stdout %q", code, out)
		}
		statusCmd := firstBacktick(errs)
		words := shellSplit(t, statusCmd)
		if len(words) == 0 || words[0] != "vecnet" {
			t.Fatalf("status command = %q", statusCmd)
		}
		if got := flagValue(words, "--net"); got != profile {
			t.Fatalf("status command profile = %q, want %q\n%s", got, profile, errs)
		}
		if !containsArg(words, reqID) {
			t.Fatalf("status command dropped the request id: %q", statusCmd)
		}
		if strings.Count(errs, "--net") < 2 {
			t.Fatal("same-id add recovery dropped --net")
		}
		code, statusOut, statusErr := u.run(words[1:]...)
		if code != 0 {
			t.Fatalf("copied status command: exit %d\n%s", code, statusErr)
		}
		if !strings.Contains(statusOut, reqID) || !strings.Contains(statusOut, "peer_secondary") {
			t.Fatal("copied status command did not read the selected profile")
		}
		if n := len(operator.paths("/v1/admin/agents/onboarding/" + reqID)); n != 0 {
			t.Fatalf("copied status command queried the operator hub %d times", n)
		}
		if n := len(secondary.paths("/v1/admin/agents/onboarding/" + reqID)); n != 1 {
			t.Fatalf("selected profile status calls = %d, want 1", n)
		}
	})
}

func writeAgentProfiles(t *testing.T, u *user, operator string, profiles map[string]string) {
	t.Helper()
	if err := cliFixtureMkdirAll(filepath.Dir(u.conf), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if operator != "" {
		fmt.Fprintf(&b, "operator_profile = %q\n", operator)
	}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "[profiles.%s]\nhub = %q\nsecret = %q\nhub_pubkey = \"ed25519:test\"\n", tomlProfileKey(name), profiles[name], "vn_operator_test")
	}
	if err := cliFixtureWriteFile(u.conf, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tomlProfileKey(name string) string {
	if profileKeyRE.MatchString(name) {
		return name
	}
	return strconv.Quote(name)
}

var profileKeyRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func firstBacktick(s string) string {
	i := strings.IndexByte(s, '`')
	if i < 0 {
		return ""
	}
	rest := s[i+1:]
	j := strings.IndexByte(rest, '`')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func shellSplit(t *testing.T, cmdline string) []string {
	t.Helper()
	if cmdline == "" {
		t.Fatal("recovery command was empty")
	}
	script := "set -- " + cmdline + "\nprintf '%s\\0' \"$@\""
	out, err := exec.Command(cliFixtureShell(t), "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("recovery command is not safe shell: %v\n%s", err, out)
	}
	parts := strings.Split(string(out), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func flagValue(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// (t5 M1) Exact assertions stay. Failure output may name the method, path
// and count, and it must not print Authorization values or enrolment tokens.
func TestAgentCLIDiagnosticRedaction(t *testing.T) {
	t.Run("(t5 M1) failure diagnostics omit authorization and enrolment tokens", func(t *testing.T) {
		hits := []agentHit{{
			Method: http.MethodPost,
			Path:   "/v1/admin/agents",
			Auth:   "Bearer vn_operator_test",
			Body:   `{"webhook_key":"crsr_diagSecret"}`,
		}}
		diag := agentHitDiag(hits)
		if !strings.Contains(diag, http.MethodPost) || !strings.Contains(diag, "/v1/admin/agents") || !strings.Contains(diag, "count 1") {
			t.Error("diagnostic lost method, path, or count")
		}
		if strings.Contains(diag, "Bearer") || strings.Contains(diag, "vn_operator_test") || strings.Contains(diag, "crsr_diagSecret") || strings.Contains(diag, "webhook_key") {
			t.Error("diagnostic leaked captured request material")
		}
		token := "vn_enrol_Zm9vYmFyYmF6cXV4MDEyMzQ1Njc4OWFiY2RlZmdoaWo"
		dashed := "vn_enrol_example-token_with-dash"
		diff := inviteBlockDiag("scout", "got "+token+" "+dashed, "want "+token)
		if strings.Contains(diff, token) || strings.Contains(diff, dashed) || strings.Contains(diff, "vn_enrol_") {
			t.Error("invite diagnostic leaked an enrolment token")
		}
		if !strings.Contains(diff, "scout") {
			t.Error("invite diagnostic lost the profile")
		}
		raw, err := os.ReadFile("agent_test.go")
		if err != nil {
			t.Fatal(err)
		}
		call := "t.Fatal" + "f"
		block := "c." + "Block"
		dump := "%+" + "v"
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, call) && (strings.Contains(line, dump) || strings.Contains(line, block)) {
				t.Errorf("diagnostic at line %d still prints a fixture", i+1)
			}
		}
	})

	// (t5 M1 link) Confirmed-link and hosted-mode failures must not print the
	// streams. Those streams hold the fake hub's link code whenever linking
	// succeeded, so an unrelated assertion would disclose it.
	t.Run("(t5 M1 link)", func(t *testing.T) {
		raw, err := os.ReadFile("agent_test.go")
		if err != nil {
			t.Fatal(err)
		}
		call := "t.Fatal" + "f"
		streams := "\\n%" + "s%" + "s"
		hosted := "hosted explanation"
		confirmed := "confirmed link output"
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, call) && strings.Contains(line, streams) &&
				(strings.Contains(line, hosted) || strings.Contains(line, confirmed)) {
				t.Errorf("line %d prints raw stdout and stderr from a link", i+1)
			}
		}
	})

	// (t5 M1 inputs) Exact field checks stay. Failure text must not dump the
	// captured input map, including a copy that only redacts URL and key.
	t.Run("(t5 M1 inputs)", func(t *testing.T) {
		raw, err := os.ReadFile("agent_test.go")
		if err != nil {
			t.Fatal(err)
		}
		verb := "%#" + "v"
		if strings.Contains(string(raw), verb) {
			t.Error("diagnostics still dump values with a map format")
		}
	})

	// (t5 M1 link r3) A link code other than the fixture must not appear in
	// the fresh-link or direct peer-link failure text.
	t.Run("(t5 M1 link r3) varied link codes stay out of failure diagnostics", func(t *testing.T) {
		const code = "WXYZ-2345-6789"
		h := newAgentHub(t, func() []onboarding.KindSpec { return onboarding.Specs() })
		h.linkCode = code
		h.peers = map[string]string{"peer_fresh": "none", "peer_live": "active"}
		u := agentOperator(t, h.srv.URL)
		exit, out, errs := u.run("agent", "link", "peer_fresh")
		if exit != 0 {
			t.Fatalf("fresh link: exit %d", exit)
		}
		if !strings.Contains(out, code) {
			t.Fatal("fresh link did not return the varied code")
		}
		if strings.Contains(linkFailureDiag("fresh link output", out, errs), code) {
			t.Fatal("fresh link diagnostic disclosed the varied link code")
		}
		exit, out, errs = u.run("peer", "link", "peer_live")
		if exit != 0 {
			t.Fatalf("peer link: exit %d", exit)
		}
		if !strings.Contains(out, code) {
			t.Fatal("peer link did not return the varied code")
		}
		if strings.Contains(linkFailureDiag("peer link output", out, errs), code) {
			t.Fatal("peer link diagnostic disclosed the varied link code")
		}
		raw, err := os.ReadFile("agent_test.go")
		if err != nil {
			t.Fatal(err)
		}
		fresh := "fresh link output: %" + "s%" + "s"
		peer := "peer link output: %" + "s\""
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, fresh) || strings.Contains(line, peer) {
				t.Errorf("line %d still prints raw link output", i+1)
			}
		}
	})
}

// linkFailureDiag is the failure text for a link command. The streams are
// accepted so callers can keep the assertion inputs, and they are not
// formatted: stdout and stderr carry the link code whenever linking worked.
func linkFailureDiag(label, _, _ string) string {
	return label
}

func agentHitDiag(hits []agentHit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "count %d\n", len(hits))
	for _, hit := range hits {
		fmt.Fprintf(&b, "%s %s\n", hit.Method, hit.Path)
	}
	return b.String()
}

var diagSecretRE = regexp.MustCompile(`(?:Bearer\s+)?vn_[A-Za-z0-9_-]+`)

func redactDiagSecrets(s string) string {
	return diagSecretRE.ReplaceAllString(s, "[redacted]")
}

func inviteBlockDiag(profile, got, want string) string {
	return fmt.Sprintf("invite block for %s changed\n got: %s\nwant: %s", profile, redactDiagSecrets(got), redactDiagSecrets(want))
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
	for _, c := range []struct{ name, gap string }{
		{"space", " "}, {"tab", "\t"}, {"newline", "\n"},
		{"carriage return", "\r"}, {"nul", "\x00"}, {"delete", "\x7f"},
		{"unicode space", "\u00a0"},
	} {
		t.Run("header gap "+c.name, func(t *testing.T) {
			raw := "Bearer " + c.gap + secret
			_, err := onboarding.Normalize(onboarding.SubmitRequest{
				Kind: "grokbot", SpecVersion: onboarding.SpecVersion, RequestID: "key-parity",
				Inputs: map[string]any{"name": "Gateway", "webhook_url": routine, "webhook_key": raw},
			})
			if err == nil {
				t.Fatal("hub accepted embedded header whitespace or control")
			}
			refused(t, base("--key-file", writeModeFile(t, "gap", raw, 0o600)), nil)
		})
	}
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
		warned := strings.Contains(errs, "0600") || strings.Contains(errs, "600")
		if runtime.GOOS == "windows" {
			warned = strings.Contains(errs, "warning") && strings.Contains(errs, "Windows permissions") && strings.Contains(errs, "readable by others")
		}
		if !warned {
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
			t.Fatal(linkFailureDiag("unconfirmed link warning", out, errs))
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
			t.Fatalf("confirmed link body = %s", agentHitDiag(links))
		}
		if !strings.Contains(out+errs, "ABCD-EFGH-JKMN") || !strings.Contains(out+errs, "revoked") {
			t.Fatal("confirmed link output missing the link code or the revoke warning")
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
			t.Fatal(linkFailureDiag("fresh link output", out, errs))
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
			t.Fatal(linkFailureDiag("unconfirmed add warning", out, errs))
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
			t.Fatal(linkFailureDiag("peer link output", out, errs))
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
	if err := cliFixtureMkdirAll(filepath.Dir(u.conf), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[profiles.p]\nhub = \"" + hubURL + "\"\nsecret = \"vn_operator_test\"\nhub_pubkey = \"ed25519:test\"\n"
	if err := cliFixtureWriteFile(u.conf, []byte(body), 0o600); err != nil {
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
	if err := cliFixtureWriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureChmod(path, mode); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && mode&0077 != 0 {
		if err := platform.CheckPath(path, false, true); err == nil {
			t.Fatal("public-read DACL fixture is still private")
		}
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

// snapBuf is a stderr buffer the hub handler can read while the CLI is
// blocked in a request. The mutex keeps that cross-goroutine read safe.
type snapBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *snapBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *snapBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
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
	linkCode string
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
		code := h.linkCode
		if code == "" {
			code = "ABCD-EFGH-JKMN"
		}
		writeJSONMap(w, http.StatusCreated, map[string]string{"code": code, "expires_at": "2026-11-06T00:10:00.000Z"})
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
