package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// All credentials and URLs in this file are synthetic. Tests use RunContext,
// a fresh pinTestEnv profile, and a real local HTTP response. The recording
// transport permits only that source fixture; it never dials a received URL.
// Like the other DefaultTransport fixtures in this package, do not parallelize.
const (
	inviteURLPeer   = "peer_00000000000000000000000047"
	inviteURLPrn    = "prn_00000000000000000000000048"
	inviteURLToken  = "vn_enrol_issue47_SYNTHETIC_ONLY"
	inviteURLSecret = "vn_operator_issue47_SYNTHETIC_ONLY"
	inviteURLExpiry = "2030-01-10T12:00:00.000Z"
	inviteURLCanary = "I47_RECEIVED_URL_CANARY"
	inviteURLPrefix = "/selected-prefix"
)

type inviteURLMode struct {
	name       string
	json, copy bool
}

var inviteURLModes = []inviteURLMode{
	{name: "human"},
	{name: "json", json: true},
	{name: "copy", copy: true},
	{name: "json-copy", json: true, copy: true},
}

type inviteURLPolicy struct {
	name    string
	profile bool
	env     string
}

var inviteURLPolicies = []inviteURLPolicy{
	{name: "no-opt-in"},
	{name: "profile-opt-in", profile: true},
	{name: "environment-opt-in", env: "1"},
}

type inviteURLResponseFixture struct {
	getenv         func(string) string
	hub            string
	config         string
	configBody     string
	clipboard      string
	clipboardCalls string

	issuances, completed, probes, unexpected, destinations atomic.Int32
	pathLookups                                            atomic.Int32
}

func newInviteURLResponseFixture(t *testing.T, verb, received, profileHub string, policy inviteURLPolicy) *inviteURLResponseFixture {
	t.Helper()
	f := &inviteURLResponseFixture{}
	issuancePath := inviteURLPrefix + "/v1/admin/peers"
	if verb == "reissue" {
		issuancePath += "/" + inviteURLPeer + "/enrol-token"
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == inviteURLPrefix+"/v1/hub/identity" {
			f.probes.Add(1)
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Vecnet-Client") != "" {
				t.Error("identity preflight carried a credential or client label")
			}
			// Existing best-effort TOFU permits an older hub's identity 404.
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && (r.URL.Path == inviteURLPrefix+"/v1/admin/peers" ||
			r.URL.Path == inviteURLPrefix+"/v1/admin/peers/"+inviteURLPeer+"/enrol-token") {
			f.issuances.Add(1)
		}
		if r.Method != http.MethodPost || r.URL.Path != issuancePath || r.URL.RawQuery != "" {
			f.unexpected.Add(1)
			t.Error("unexpected source request; invitation must not be repaired/retried")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+inviteURLSecret || r.Header.Get("Content-Type") != "application/json" {
			t.Error("issuance did not use the selected synthetic operator profile")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("issuance body was not JSON")
		} else if body["enrol_ttl_seconds"] != float64(7200) {
			t.Error("issuance changed the requested token TTL")
		}
		if verb == "create" && (body["kind"] != "session" || body["name"] != "Scout") {
			t.Error("create did not preserve invitation kind/name")
		}
		if verb == "create" {
			w.WriteHeader(http.StatusCreated)
		}
		answer := map[string]any{
			"peer_id": inviteURLPeer, "principal_id": inviteURLPrn,
			"name": "Scout", "name_open": false, "hub_owner": "Fixture Owner",
			"enrol_token": inviteURLToken, "enrol_expires_at": inviteURLExpiry,
		}
		if received != "" {
			answer["hub_url"] = received
		}
		if err := json.NewEncoder(w).Encode(answer); err != nil {
			t.Error("could not complete the synthetic issuance response")
			return
		}
		f.completed.Add(1)
	}))
	t.Cleanup(source.Close)
	f.hub = source.URL + inviteURLPrefix
	if profileHub == "" {
		profileHub = f.hub
	}
	f.configBody = fmt.Sprintf("[profiles.p]\nhub = %q\nsecret = %q\nallow_insecure_http = %t\n", profileHub, inviteURLSecret, policy.profile)
	var baseEnv func(string) string
	f.config, baseEnv = pinTestEnv(t, f.configBody)
	bin := filepath.Join(filepath.Dir(f.config), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	f.clipboard = filepath.Join(filepath.Dir(f.config), "clipboard.txt")
	f.clipboardCalls = filepath.Join(filepath.Dir(f.config), "clipboard-calls.txt")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	// Only shell builtins: this fixture PATH cannot find a real clipboard tool.
	// Invitation blocks end in a newline; read/printf preserves their bytes.
	script := "#!/bin/sh\nprintf 'called\\n' >> " + quote(f.clipboardCalls) + "\n" +
		"while IFS= read -r line; do printf '%s\\n' \"$line\"; done > " + quote(f.clipboard) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "wl-copy"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f.getenv = func(k string) string {
		switch k {
		case "PATH":
			f.pathLookups.Add(1)
			return bin
		case "VECNET_CODEX_AUTO_DELIVERY":
			return "0"
		case insecureEnv:
			return policy.env
		default:
			return baseEnv(k)
		}
	}
	// No ambient proxy or non-fixture destination is reachable, even if a
	// regression tries to follow/verify the URL supplied in the response.
	wire := &http.Transport{}
	t.Cleanup(wire.CloseIdleConnections)
	previous := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(source.URL, "http://") {
			f.destinations.Add(1)
			return nil, errors.New("invitation fixture blocked a received destination request")
		}
		return wire.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return f
}

func (f *inviteURLResponseFixture) run(verb string, mode inviteURLMode) (int, string, string) {
	args := []string{"--net", "p"}
	if mode.json {
		args = append(args, "--json")
	}
	args = append(args, "peer", "invite")
	if verb == "reissue" {
		args = append(args, inviteURLPeer)
	} else {
		args = append(args, "--name", "Scout")
	}
	args = append(args, "--profile", "issued", "--ttl", "2h")
	if mode.copy {
		args = append(args, "--copy")
	}
	var out, errs bytes.Buffer
	code := RunContext(context.Background(), args, strings.NewReader(""), &out, &errs, f.getenv)
	return code, out.String(), errs.String()
}

func (f *inviteURLResponseFixture) checkRequests(t *testing.T, want int32) {
	t.Helper()
	if f.issuances.Load() != want || f.completed.Load() != want {
		t.Errorf("issuance requests/completed responses = %d/%d, want %d/%d (identity probes counted separately: %d)",
			f.issuances.Load(), f.completed.Load(), want, want, f.probes.Load())
	}
	if f.destinations.Load() != 0 || f.unexpected.Load() != 0 {
		t.Errorf("received-destination/unexpected source requests = %d/%d, want 0/0", f.destinations.Load(), f.unexpected.Load())
	}
	if string(pinRead(t, f.config)) != f.configBody {
		t.Error("invitation rewrote the selected synthetic profile")
	}
}

func (f *inviteURLResponseFixture) checkNoCopy(t *testing.T) {
	t.Helper()
	if f.pathLookups.Load() != 0 {
		t.Error("invitation looked up a clipboard tool before refusal or in a no-copy mode")
	}
	for _, path := range []string{f.clipboardCalls, f.clipboard} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Error("clipboard invocation/output artifact exists or could not be checked")
		}
	}
}

// Expected new-behavior RED on the pinned base: received nonempty hub_url is
// rendered/emitted/copied without checkHubURL. This is not a guard unit test.
func TestInviteHubURLResponseRefusesUnsafe(t *testing.T) {
	cases := []struct {
		name, hub  string
		publicHTTP bool
	}{
		{"userinfo-password", "https://alice:" + inviteURLCanary + "@returned.example/vecnet", false},
		{"userinfo-username-only", "https://" + inviteURLCanary + "@returned.example/vecnet", false},
		{"query", "https://returned.example/vecnet?token=" + inviteURLCanary, false},
		{"empty-query", "https://returned.example/" + inviteURLCanary + "?", false},
		{"fragment", "https://returned.example/vecnet#" + inviteURLCanary, false},
		{"empty-fragment", "https://returned.example/" + inviteURLCanary + "#", false},
		{"malformed-authority", "https://returned.example:" + inviteURLCanary + "/vecnet", false},
		{"malformed-ipv6", "https://[2001:db8::" + inviteURLCanary + "/vecnet", false},
		{"opaque", "https:" + inviteURLCanary, false},
		{"relative", inviteURLCanary + "/vecnet", false},
		{"scheme-relative", "//" + inviteURLCanary + ".example/vecnet", false},
		{"invalid-scheme", "ftp://returned.example/" + inviteURLCanary, false},
		{"public-http", "http://returned.example/" + inviteURLCanary, true},
	}
	for _, verb := range []string{"create", "reissue"} {
		for _, tc := range cases {
			for _, policy := range inviteURLPolicies {
				if tc.publicHTTP && policy.name != "no-opt-in" {
					continue // Explicit HTTP opt-ins are preservation controls below.
				}
				for _, mode := range inviteURLModes {
					t.Run(verb+"/"+tc.name+"/"+policy.name+"/"+mode.name, func(t *testing.T) {
						f := newInviteURLResponseFixture(t, verb, tc.hub, "", policy)
						code, out, errs := f.run(verb, mode)
						if code != exitUsage || errs != "vecnet: invalid invitation hub URL\n" {
							t.Errorf("want fixed invalid invitation hub URL refusal/exit 1; got exit %d, stderr %q", code, errs)
						}
						if out != "" {
							t.Error("refused invitation emitted stdout/JSON/a paste block")
						}
						for _, banned := range []string{tc.hub, inviteURLCanary, inviteURLToken, "You're invited to vecnet", "vecnet enrol"} {
							if strings.Contains(out+errs, banned) {
								t.Error("rejected URL/canary/token/invitation reached output")
							}
						}
						f.checkRequests(t, 1) // Refusal cannot undo or repeat issuance.
						f.checkNoCopy(t)
					})
				}
			}
		}
	}
}

// Existing controls: accepted strings, token fields, old-hub fallback and
// JSON's early return before --copy must survive the new response guard.
func TestInviteHubURLResponsePreservesSafeOutput(t *testing.T) {
	cases := []struct {
		name, hub string
		policy    inviteURLPolicy
	}{
		{"https-prefix", "https://returned.example/Vecnet/%2Fexact/", inviteURLPolicies[0]},
		{"https-ipv6-prefix", "https://[2001:db8::7]:8443/Vecnet/%2Fexact/", inviteURLPolicies[0]},
		{"public-http-profile-opt-in", "http://returned.example/Vecnet/%2Fexact/", inviteURLPolicies[1]},
		{"public-http-environment-opt-in", "http://returned.example/Vecnet/%2Fexact/", inviteURLPolicies[2]},
		{"older-hub-empty-fallback", "", inviteURLPolicies[0]},
	}
	for _, verb := range []string{"create", "reissue"} {
		for _, tc := range cases {
			for _, mode := range inviteURLModes {
				t.Run(verb+"/"+tc.name+"/"+mode.name, func(t *testing.T) {
					f := newInviteURLResponseFixture(t, verb, tc.hub, "", tc.policy)
					code, out, errs := f.run(verb, mode)
					if code != exitOK {
						t.Errorf("safe response failed: exit %d, stderr %q", code, errs)
					}
					wantHub, block := tc.hub, out
					if wantHub == "" {
						wantHub = f.hub
					}
					if mode.json {
						var result struct {
							PeerID         string `json:"peer_id"`
							PrincipalID    string `json:"principal_id"`
							HubURL         string `json:"hub_url"`
							EnrolToken     string `json:"enrol_token"`
							EnrolExpiresAt string `json:"enrol_expires_at"`
							Profile        string `json:"profile"`
							Invite         string `json:"invite"`
						}
						if err := json.Unmarshal([]byte(out), &result); err != nil {
							t.Errorf("safe response did not emit one complete JSON object: %v", err)
						}
						if result.HubURL != wantHub || result.EnrolToken != inviteURLToken || result.EnrolExpiresAt != inviteURLExpiry ||
							result.PeerID != inviteURLPeer || result.PrincipalID != inviteURLPrn || result.Profile != "issued" {
							t.Error("JSON changed accepted hub URL bytes or issuance/token fields")
						}
						block = result.Invite
						if errs != "" {
							t.Errorf("JSON mode printed a human/copy diagnostic: %q", errs)
						}
					} else if !strings.Contains(errs, "invited peer "+inviteURLPeer+" (profile issued); the token works once and expires "+inviteURLExpiry+"\n") {
						t.Error("human output lost the issuance/expiry summary")
					}
					if !strings.Contains(block, "--hub "+wantHub+" --token - --profile issued\n") ||
						strings.Count(block, "printf %s '"+inviteURLToken+"'") != 1 || !strings.HasSuffix(block, "\n") {
						t.Error("paste block changed the exact accepted hub or one-time token command")
					}
					if mode.copy && !mode.json {
						if f.pathLookups.Load() != 1 || string(pinRead(t, f.clipboardCalls)) != "called\n" || string(pinRead(t, f.clipboard)) != block {
							t.Error("--copy did not invoke exactly the fixture tool with the full paste block")
						}
						if !strings.Contains(errs, "copied to the clipboard (wl-copy)\n") {
							t.Error("--copy did not report the fixture clipboard tool")
						}
					} else {
						f.checkNoCopy(t) // Includes --json --copy: no lookup or invocation.
					}
					f.checkRequests(t, 1)
				})
			}
		}
	}
}

// With an unchanged selected profile, an unsafe fallback is unreachable
// after issuance: connect/useStoredHub rejects it first. This control proves
// that earlier public boundary, not post-response fallback validation; do not
// bypass it by injecting an app/client or mutating config during a response.
func TestInviteHubURLResponseUnsafeFallbackStopsBeforeIssuance(t *testing.T) {
	for _, hub := range []struct{ name, url string }{
		{"userinfo", "https://" + inviteURLCanary + "@returned.example/vecnet"},
		{"public-http", "http://returned.example/" + inviteURLCanary},
	} {
		for _, verb := range []string{"create", "reissue"} {
			for _, mode := range inviteURLModes {
				t.Run(verb+"/"+hub.name+"/"+mode.name, func(t *testing.T) {
					f := newInviteURLResponseFixture(t, verb, "", hub.url, inviteURLPolicies[0])
					code, out, errs := f.run(verb, mode)
					if code != exitUsage || out != "" || !strings.Contains(errs, "profile \"p\" is unusable; edit the config:") {
						t.Errorf("want earlier selected-profile refusal, got exit %d, stdout %q, stderr %q", code, out, errs)
					}
					if strings.Contains(out+errs, inviteURLCanary) || strings.Contains(out+errs, inviteURLToken) {
						t.Error("earlier fallback/profile refusal exposed URL or token bytes")
					}
					if f.probes.Load() != 0 {
						t.Error("unsafe selected profile attempted identity preflight")
					}
					f.checkRequests(t, 0)
					f.checkNoCopy(t)
				})
			}
		}
	}
}
