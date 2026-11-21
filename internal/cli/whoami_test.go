package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const whoamiStalePeer = "peer_00000000000000000000000002"
const whoamiAsPeer = "peer_00000000000000000000000003"

func whoamiUser(t *testing.T, hub, secret string) *user {
	t.Helper()
	u := profileUser(t, hub, secret)
	f, err := os.OpenFile(u.conf, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, "peer_id = \""+whoamiStalePeer+"\"\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("write stale profile: %v, close: %v", err, closeErr)
	}
	return u
}

func TestWhoamiAuthenticatedIdentity(t *testing.T) {
	for _, tc := range []struct {
		kind, secret, response, human string
		want                          map[string]string
	}{
		{"peer", "vn_peer_whoami_fixture", `{"kind":"peer","peer_id":"peer_00000000000000000000000001"}`, "kind peer\npeer_id peer_00000000000000000000000001\n", map[string]string{"kind": "peer", "peer_id": "peer_00000000000000000000000001"}},
		{"principal", "vn_principal_whoami_fixture", `{"kind":"principal","principal_id":"prn_00000000000000000000000001"}`, "kind principal\nprincipal_id prn_00000000000000000000000001\n", map[string]string{"kind": "principal", "principal_id": "prn_00000000000000000000000001"}},
		{"operator", "vn_operator_whoami_fixture", `{"kind":"operator"}`, "kind operator\n", map[string]string{"kind": "operator"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/hub/identity" {
					http.NotFound(w, r) // Existing best-effort TOFU convention.
					return
				}
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || r.Method != http.MethodGet || r.URL.Path != "/v1/peers/me" || r.URL.RawQuery != "" || len(body) != 0 || r.Header.Get("Authorization") != "Bearer "+tc.secret {
					t.Errorf("identity request: %s %s body=%q err=%v", r.Method, r.URL, body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.response)
			}))
			defer srv.Close()
			u := whoamiUser(t, srv.URL, tc.secret)
			before, err := os.ReadFile(u.conf)
			if err != nil {
				t.Fatal(err)
			}
			for _, as := range []bool{false, true} {
				for _, jsonOutput := range []bool{false, true} {
					args := []string{"--net", "p", "whoami"}
					if as {
						args = append(args, "--as", whoamiAsPeer)
					}
					if jsonOutput {
						args = append(args, "--json")
					}
					previous := calls.Load()
					code, out, stderr := u.run(args...)
					if code != 0 || stderr != "" || calls.Load() != previous+1 {
						t.Fatalf("%v: exit=%d stdout=%q stderr=%q requests=%d", args, code, out, stderr, calls.Load()-previous)
					}
					if jsonOutput {
						var got map[string]string
						if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, tc.want) || strings.Count(out, "\n") != 1 {
							t.Fatalf("JSON identity = %q (%v), want %v", out, err, tc.want)
						}
					} else if out != tc.human {
						t.Fatalf("human identity = %q, want %q", out, tc.human)
					}
				}
			}
			after, err := os.ReadFile(u.conf)
			if err != nil || string(after) != string(before) {
				t.Fatalf("identity read changed profile: %v", err)
			}
		})
	}
}

func TestWhoamiErrorsAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, body, diagnostic string
		status                 int
	}{
		{"unauthorized", `{"error":{"code":"unauthorized","message":"invalid credential"}}`, "unauthorized: invalid credential", http.StatusUnauthorized},
		{"forbidden", `{"error":{"code":"forbidden","message":"credential disabled"}}`, "forbidden: credential disabled", http.StatusForbidden},
		{"hub failure", `{"error":{"code":"unavailable","message":"try later"}}`, "unavailable: try later", http.StatusServiceUnavailable},
		{"invalid JSON", `not JSON`, "bad_response:", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/hub/identity" {
					http.NotFound(w, r)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/v1/peers/me" || r.URL.RawQuery != "" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			u := whoamiUser(t, srv.URL, "vn_peer_whoami_fixture")
			for _, args := range [][]string{{"whoami"}, {"whoami", "--json", "--as", whoamiAsPeer}} {
				code, out, stderr := u.run(args...)
				if code != 2 || out != "" || !strings.Contains(stderr, tc.diagnostic) {
					t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, code, out, stderr)
				}
			}
		})
	}
	t.Run("bad arguments before HTTP", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}))
		defer srv.Close()
		u := whoamiUser(t, srv.URL, "vn_peer_whoami_fixture")
		for _, args := range [][]string{{"whoami", "extra"}, {"whoami", "--unknown"}, {"whoami", "--", "--json"}} {
			code, out, stderr := u.run(args...)
			if code != 1 || out != "" || !strings.Contains(stderr, "whoami") || calls.Load() != 0 {
				t.Fatalf("%v: exit=%d stdout=%q stderr=%q requests=%d", args, code, out, stderr, calls.Load())
			}
		}
	})
	t.Run("help without configuration", func(t *testing.T) {
		code, out, stderr := newUser(t).run("whoami", "--help")
		if code != 0 || stderr != "" || !strings.Contains(out, "whoami [--json]") {
			t.Fatalf("help: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
}
