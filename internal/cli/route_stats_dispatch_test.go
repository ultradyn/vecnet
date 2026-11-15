package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

const (
	t4RouteProfile = "stats-selected"
	t4RouteBearer  = "vn_operator_task4_selected_synthetic_canary"
	t4RoutePeer    = "peer_00000000000000000000000001"
	t4RouteID      = "route_00000000000000000000000011"
)

// Independent wire literals: a route with no current member peers can retain
// attempts, while a peer with no routes still has that peer in its context.
const t4RouteReport = `{
  "subject":{"kind":"route","id":"route_00000000000000000000000011"},
  "window":{"since":"2030-01-03T12:00:00.000Z","until":"2030-01-10T12:00:00.000Z","max_rows":1000,"max_age_hours":168},
  "retained_deliveries":{"peer_ids":[],"total":0,"with_push_evidence":0,"accepted_without_push_evidence":0},
  "routes":[{"route_id":"route_00000000000000000000000011","kind":"http","host":"stats.example","owner_kind":"principal","owner_id":"prn_00000000000000000000000010","attempt_scope":"route",
    "requests":2,"coalesced":0,"ok":1,"fail":1,"refused":0,"distinct_ids":1,
    "last_used_at":"2030-01-10T11:59:00.000Z","last_success_at":"2030-01-10T11:58:00.000Z","last_error":"network_error","last_error_at":"2030-01-10T11:59:00.000Z","last_status":null,
    "body_bytes":{"avg":0,"p95":0,"p99":0,"samples":2},"latency_ms":{"avg":3,"p95":4,"p99":4,"samples":2}}]
}`

const t4RouteIdleReport = `{
  "subject":{"kind":"route","id":"route_00000000000000000000000011"},
  "window":{"since":"2030-01-03T12:00:00.000Z","until":"2030-01-10T12:00:00.000Z","max_rows":1000,"max_age_hours":168},
  "retained_deliveries":{"peer_ids":[],"total":0,"with_push_evidence":0,"accepted_without_push_evidence":0},
  "routes":[{"route_id":"route_00000000000000000000000011","kind":"http","host":"stats.example","owner_kind":"principal","owner_id":"prn_00000000000000000000000010","attempt_scope":"route",
    "requests":0,"coalesced":0,"ok":0,"fail":0,"refused":0,"distinct_ids":0,
    "last_used_at":null,"last_success_at":null,"last_error":null,"last_error_at":null,"last_status":null,
    "body_bytes":{"avg":null,"p95":null,"p99":null,"samples":0},"latency_ms":{"avg":null,"p95":null,"p99":null,"samples":0}}]
}`

const t4RouteEmptyReport = `{
  "subject":{"kind":"peer","id":"peer_00000000000000000000000001"},
  "window":{"since":"2030-01-03T12:00:00.000Z","until":"2030-01-10T12:00:00.000Z","max_rows":1000,"max_age_hours":168},
  "retained_deliveries":{"peer_ids":["peer_00000000000000000000000001"],"total":0,"with_push_evidence":0,"accepted_without_push_evidence":0},
  "routes":[]
}`

// Reuse the ordinary private-profile fixture. The environment fallback points
// at a different receiver that rejects every request, including identity probes.
func t4RouteUser(t *testing.T, handle http.HandlerFunc) *user {
	t.Helper()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("explicit --net reached fallback profile: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(fallback.Close)
	key, err := hubid.KeyFromSeedHex(strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	var selected *httptest.Server
	selected = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// connect may perform this public TOFU probe before the command GET.
		// It is deliberately separate from each test's admin/stats counter.
		if r.URL.Path == "/v1/hub/identity" {
			q := r.URL.Query()
			if r.Method != http.MethodGet || len(q) != 1 || len(q["challenge"]) != 1 || q.Get("challenge") == "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Vecnet-Client") != "" {
				t.Error("unexpected startup identity request shape or credential")
				http.Error(w, "invalid fixture identity request", http.StatusBadRequest)
				return
			}
			sig, err := key.SignChallenge(selected.URL, q.Get("challenge"))
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: selected.URL, ChallengeSig: sig})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+t4RouteBearer || r.Header.Get("Accept") != "application/json" {
			t.Error("command did not use the explicitly selected operator profile")
		}
		handle(w, r)
	}))
	t.Cleanup(selected.Close)
	u := profileUser(t, fallback.URL, "vn_operator_task4_fallback_synthetic")
	u.env["VECNET_CODEX_AUTO_DELIVERY"] = "0"
	u.env["CODEX_HOME"] = filepath.Join(u.env["HOME"], ".codex")
	u.env["XDG_DATA_HOME"] = filepath.Join(u.env["HOME"], "data")
	u.env["XDG_RUNTIME_DIR"] = filepath.Join(u.env["HOME"], "runtime")
	body := fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\n\n[profiles.%s]\nhub = %q\nsecret = %q\n",
		fallback.URL, "vn_operator_task4_fallback_synthetic", t4RouteProfile, selected.URL, t4RouteBearer)
	if err := os.WriteFile(u.conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return u
}

func t4RouteRun(u *user, args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := cli.RunContext(context.Background(), args, strings.NewReader(""), &out, &errs, u.getenv)
	return code, out.String(), errs.String()
}

func t4RouteJSON(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("expected one complete JSON value: %v", err)
	}
	return value
}

func t4RouteStatsRequest(t *testing.T, r *http.Request, id string) {
	t.Helper()
	q := r.URL.Query()
	if r.Method != http.MethodGet || r.URL.Path != "/v1/admin/route-stats" || len(q) != 1 || len(q["id"]) != 1 || q.Get("id") != id || r.URL.RawQuery != (url.Values{"id": {id}}).Encode() {
		t.Error("stats dispatch changed method/path or the single encoded id query")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) != 0 {
		t.Error("stats GET must not have a body")
	}
}

func TestRouteStatsDispatchAndHelp(t *testing.T) {
	for _, tc := range []struct {
		name, id, report string
		args             []string
		json             bool
	}{
		{"human-selected-profile", t4RouteID, t4RouteReport, []string{"--net", t4RouteProfile, "route", "stats", t4RouteID}, false},
		{"json-before-verb", t4RouteID, t4RouteReport, []string{"--json", "--net", t4RouteProfile, "route", "stats", t4RouteID}, true},
		{"json-after-verb-and-no-as-query", t4RouteID, t4RouteIdleReport, []string{"--net", t4RouteProfile, "--as", t4RoutePeer, "route", "stats", t4RouteID, "--json"}, true},
		{"json-empty-routes", t4RoutePeer, t4RouteEmptyReport, []string{"--net", t4RouteProfile, "route", "stats", t4RoutePeer, "--json"}, true},
		{"json-route-separator-before-stats", t4RouteID, t4RouteReport, []string{"--net", t4RouteProfile, "--json", "route", "--", "stats", t4RouteID}, true},
		{"json-stats-separator-before-selector", t4RouteID, t4RouteReport, []string{"--net", t4RouteProfile, "--json", "route", "stats", "--", t4RouteID}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stats atomic.Int32
			u := t4RouteUser(t, func(w http.ResponseWriter, r *http.Request) {
				stats.Add(1)
				t4RouteStatsRequest(t, r, tc.id)
				_, _ = io.WriteString(w, tc.report)
			})
			code, out, errs := t4RouteRun(u, tc.args...)
			if code != 0 || errs != "" || stats.Load() != 1 {
				t.Fatalf("stats dispatch: exit=%d stats=%d stdout=%q stderr=%q", code, stats.Load(), out, errs)
			}
			if tc.json {
				if !reflect.DeepEqual(t4RouteJSON(t, out), t4RouteJSON(t, tc.report)) {
					t.Fatal("public stats JSON lost complete wire fields, owners, arrays, nulls or measured zero")
				}
			} else {
				for _, want := range []string{"sample: newest 1000 per route / 168h", "subject=route:" + t4RouteID, "owner=principal:prn_00000000000000000000000010", "route-wide", "retained delivery evidence", "last_status=-"} {
					if !strings.Contains(out, want) {
						t.Errorf("public human stats missing %q", want)
					}
				}
			}
		})
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
		help bool
	}{
		{"main-help", []string{"--help"}, 0, true},
		{"route-help", []string{"route", "--help"}, 0, true},
		{"route-usage", []string{"route"}, 1, true},
		{"missing-selector", []string{"route", "stats"}, 1, true},
		{"extra-selector", []string{"route", "stats", t4RouteID, t4RoutePeer}, 1, true},
		{"unknown-flag", []string{"route", "stats", t4RouteID, "--not-a-route-flag"}, 1, false},
		{"route-separator-missing-selector", []string{"route", "--", "stats"}, 1, true},
		{"route-separator-extra-selector", []string{"route", "--", "stats", t4RouteID, t4RoutePeer}, 1, true},
		{"stats-separator-missing-selector", []string{"route", "stats", "--"}, 1, true},
		{"stats-separator-extra-selector", []string{"route", "stats", "--", t4RouteID, t4RoutePeer}, 1, true},
		{"unknown-route-flag-before-separator", []string{"route", "--not-a-route-flag", "--", "stats", t4RouteID}, 1, false},
		{"unknown-stats-flag-before-separator", []string{"route", "stats", "--not-a-route-flag", "--", t4RouteID}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stats atomic.Int32
			u := t4RouteUser(t, func(w http.ResponseWriter, r *http.Request) {
				stats.Add(1)
				t.Errorf("help or invalid invocation made unexpected command request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			})
			args := append([]string{"--net", t4RouteProfile}, tc.args...)
			code, out, errs := t4RouteRun(u, args...)
			if code != tc.code || stats.Load() != 0 {
				t.Fatalf("help/usage: exit=%d stats=%d stdout=%q stderr=%q", code, stats.Load(), out, errs)
			}
			if tc.code == 1 && (out != "" || errs == "") {
				t.Fatal("invalid invocation must write only a local usage error")
			}
			if tc.code == 0 && errs != "" {
				t.Fatalf("help wrote stderr: %q", errs)
			}
			if tc.help && !strings.Contains(out+errs, "route stats <peer_id|route_id|sub_id>") {
				t.Fatal("help/usage must advertise route stats and its exact selector alternatives")
			}
		})
	}
	// A terminator keeps global-looking arguments literal at either level.
	// In particular, --help here must reach the service instead of main help.
	for _, tc := range []struct {
		name, id string
		args     []string
	}{
		{"route-separator-literal-json", "--json", []string{"route", "--", "stats", "--json"}},
		{"stats-separator-literal-json", "--json", []string{"route", "stats", "--", "--json"}},
		{"route-separator-literal-help", "--help", []string{"route", "--", "stats", "--help"}},
		{"stats-separator-literal-help", "--help", []string{"route", "stats", "--", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stats atomic.Int32
			u := t4RouteUser(t, func(w http.ResponseWriter, r *http.Request) {
				stats.Add(1)
				t4RouteStatsRequest(t, r, tc.id)
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(w, `{"error":{"code":"invalid_value","message":"literal-selector-remote-canary","details":{"field":"id"}}}`)
			})
			args := append([]string{"--net", t4RouteProfile}, tc.args...)
			code, out, errs := t4RouteRun(u, args...)
			if code != 2 || out != "" || stats.Load() != 1 || !strings.HasPrefix(errs, "invalid_value:") {
				t.Fatalf("literal selector: exit=%d stats=%d stdout=%q stderr=%q", code, stats.Load(), out, errs)
			}
			if strings.Contains(errs, tc.id) || strings.Contains(errs, "literal-selector-remote-canary") {
				t.Fatal("literal selector error echoed input or remote text")
			}
		})
	}
	t.Run("encoded-selector-error-is-private", func(t *testing.T) {
		const id = "selector-canary +&id=second?#/"
		const private = "https://private-canary.example/private-path?token-canary"
		var stats atomic.Int32
		u := t4RouteUser(t, func(w http.ResponseWriter, r *http.Request) {
			stats.Add(1)
			t4RouteStatsRequest(t, r, id)
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"code": "invalid_value", "message": private + id,
				"details": map[string]any{"private": t4RouteBearer},
			}})
		})
		code, out, errs := t4RouteRun(u, "--net", t4RouteProfile, "route", "stats", id, "--json")
		if code != 2 || out != "" || stats.Load() != 1 || !strings.HasPrefix(errs, "invalid_value:") {
			t.Fatalf("stats service error: exit=%d stats=%d stdout=%q stderr=%q", code, stats.Load(), out, errs)
		}
		for _, secret := range []string{id, "selector-canary", "private-canary", "private-path", "token-canary", t4RouteBearer, "http://", "https://"} {
			if strings.Contains(out+errs, secret) {
				t.Fatal("public stats error leaked selector, remote text, URL or bearer")
			}
		}
	})
}

func TestRouteStatsDispatchPreservesRouteVerbs(t *testing.T) {
	const key = "crsr_task4_rotate_synthetic_key"
	keyPath := filepath.Join(t.TempDir(), "routine-key")
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body, response, want string
		args                                     []string
	}{
		{"add", http.MethodPost, "/v1/admin/routes", `{"owner":"peer_00000000000000000000000001","url":"https://callback.example/route"}`, `{"route_id":"route_00000000000000000000000011","whsec":"whsec_task4_synthetic"}`, t4RouteID,
			[]string{"route", "add", "--owner", t4RoutePeer, "--url", "https://callback.example/route"}},
		{"list", http.MethodGet, "/v1/admin/routes", "", `{"routes":[{"route_id":"route_00000000000000000000000011","owner_kind":"peer","owner_id":"peer_00000000000000000000000001","kind":"http","host":"callback.example","verified_at":null,"runs_per_hour":0}]}`, t4RouteID,
			[]string{"route", "list"}},
		{"verify", http.MethodPost, "/v1/admin/routes/" + t4RouteID + "/verify", "", "", `"ok":true`,
			[]string{"route", "verify", t4RouteID}},
		{"rotate", http.MethodPost, "/v1/admin/routes/" + t4RouteID + "/rotate", `{"key":"crsr_task4_rotate_synthetic_key"}`, "", `"ok":true`,
			[]string{"route", "rotate", t4RouteID, "--key-file", keyPath}},
		{"remove", http.MethodDelete, "/v1/admin/routes/" + t4RouteID, "", "", `"ok":true`,
			[]string{"route", "remove", t4RouteID}},
		{"list-route-separator", http.MethodGet, "/v1/admin/routes", "", `{"routes":[{"route_id":"route_00000000000000000000000011","owner_kind":"peer","owner_id":"peer_00000000000000000000000001","kind":"http","host":"callback.example","verified_at":null,"runs_per_hour":0}]}`, t4RouteID,
			[]string{"route", "--", "list"}},
		{"verify-route-separator", http.MethodPost, "/v1/admin/routes/" + t4RouteID + "/verify", "", "", `"ok":true`,
			[]string{"route", "--", "verify", t4RouteID}},
		{"remove-route-separator", http.MethodDelete, "/v1/admin/routes/" + t4RouteID, "", "", `"ok":true`,
			[]string{"route", "--", "remove", t4RouteID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			u := t4RouteUser(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != "" {
					t.Error("legacy route dispatch changed method/path/query")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				} else if tc.body == "" {
					if len(raw) != 0 {
						t.Error("legacy route command unexpectedly sent a body")
					}
				} else {
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("legacy route body lost JSON content type")
					}
					var got, want any
					if json.Unmarshal(raw, &got) != nil || json.Unmarshal([]byte(tc.body), &want) != nil || !reflect.DeepEqual(got, want) {
						t.Error("legacy route command changed its complete JSON body")
					}
				}
				if tc.response == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_, _ = io.WriteString(w, tc.response)
			})
			args := append([]string{"--net", t4RouteProfile, "--json"}, tc.args...)
			code, out, errs := t4RouteRun(u, args...)
			if code != 0 || errs != "" || calls.Load() != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("legacy %s dispatch: exit=%d calls=%d stdout=%q stderr=%q", tc.name, code, calls.Load(), out, errs)
			}
			_ = t4RouteJSON(t, out)
			if strings.Contains(out+errs, key) {
				t.Fatal("legacy route command echoed its key-file contents")
			}
		})
	}
}
