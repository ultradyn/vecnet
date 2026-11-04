package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

func pinTestEnv(t *testing.T, body string) (string, func(string) string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"VECNET_CONFIG": path, "XDG_CONFIG_HOME": dir, "XDG_CACHE_HOME": filepath.Join(dir, "cache"), "HOME": dir, "VECNET_PROFILE": "p"}
	return path, func(k string) string { return env[k] }
}
func pinRun(env func(string) string, args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := Run(args, strings.NewReader(`{}`), &out, &errs, env)
	return code, out.String(), errs.String()
}
func pinRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func testIdentityKey(t *testing.T, digit string) hubid.Key {
	t.Helper()
	k, e := hubid.KeyFromSeedHex(strings.Repeat(digit, 64))
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func identityStub(t *testing.T, k hubid.Key, mutate func(*envelope.HubIdentity)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hub/identity" {
			if r.URL.Path == "/v1/peers" {
				w.Write([]byte(`{"peers":[]}`))
				return
			}
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Vecnet-Client") != "" {
			t.Error("credential on identity request")
		}
		sig, e := k.SignChallenge(srv.URL, r.URL.Query().Get("challenge"))
		if e != nil {
			t.Error(e)
		}
		doc := envelope.HubIdentity{Pubkey: k.Pubkey(), PublicURL: srv.URL, ChallengeSig: sig}
		if mutate != nil {
			mutate(&doc)
		}
		json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}
func TestTOFUNeverWritesConfig(t *testing.T) {
	k := testIdentityKey(t, "1")
	srv, _ := identityStub(t, k, nil)
	body := "# keep\n[profiles.p]\nhub = \"" + srv.URL + "/\"\nsecret = \"secret\"\n"
	path, env := pinTestEnv(t, body)
	if code, _, err := pinRun(env, "--net", "p", "peers"); code != 0 {
		t.Fatalf("%d %s", code, err)
	}
	if string(pinRead(t, path)) != body {
		t.Fatal("TOFU rewrote config")
	}
	pp, _ := pinsPath(env)
	var records map[string]pinRecord
	if err := json.Unmarshal(pinRead(t, pp), &records); err != nil {
		t.Fatal(err)
	}
	r := records["p"]
	if r.Hub != srv.URL || r.Pubkey != k.Pubkey() || r.How != "tofu" || r.PinnedAt == "" {
		t.Fatalf("bad pin: %+v", r)
	}
	for p, mode := range map[string]os.FileMode{pp: 0600, filepath.Dir(pp): 0700} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm() != mode {
			t.Fatalf("permissions %s: %v %v", p, info, e)
		}
	}
}
func TestTOFUOldHub404(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hub/identity" {
			calls.Add(1)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"peers":[]}`))
	}))
	defer srv.Close()
	_, env := pinTestEnv(t, "[profiles.p]\nhub = \""+srv.URL+"\"\nsecret = \"secret\"\n")
	for i := 0; i < 2; i++ {
		if code, _, err := pinRun(env, "peers"); code != 0 {
			t.Fatalf("%d %s", code, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("identity requests %d", calls.Load())
	}
	a := &app{getenv: env}
	if pub, _ := a.effectivePin("p", Profile{Hub: srv.URL}); pub != "" {
		t.Fatal("404 pinned")
	}
}
func TestHookDoesNotTOFU(t *testing.T) {
	var calls, polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/hub/identity" {
			calls.Add(1)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/v1/events" {
			polls.Add(1)
			w.Write([]byte(`{"events":[],"next_cursor":""}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, env := pinTestEnv(t, "[profiles.p]\nhub = \""+srv.URL+"\"\nsecret = \"secret\"\n")
	if code, _, _ := pinRun(env, "hook", "--event", "user-prompt"); code != 0 {
		t.Fatalf("hook exit %d", code)
	}
	if polls.Load() == 0 {
		t.Fatal("hook did not reach the hub")
	}
	pp, _ := pinsPath(env)
	if calls.Load() != 0 {
		t.Fatal("hook tried identity")
	}
	if _, e := os.Stat(pp); !os.IsNotExist(e) {
		t.Fatalf("hook wrote pins: %v", e)
	}
}
func TestVerifyHubChecks(t *testing.T) {
	old, k := testIdentityKey(t, "1"), testIdentityKey(t, "2")
	for _, tc := range []struct {
		name, pin   string
		mutate      func(*envelope.HubIdentity)
		ok, rotated bool
	}{
		{name: "same", pin: k.Pubkey(), ok: true},
		{name: "other", pin: old.Pubkey()},
		{name: "relay", pin: k.Pubkey(), mutate: func(d *envelope.HubIdentity) { d.PublicURL = "https://relay.example" }},
		{name: "bad challenge", pin: k.Pubkey(), mutate: func(d *envelope.HubIdentity) { d.ChallengeSig = "broken" }},
		{name: "rotation", pin: old.Pubkey(), ok: true, rotated: true, mutate: func(d *envelope.HubIdentity) {
			at := "2026-11-05T07:00:00.000Z"
			d.Previous = &envelope.HubKeyPrevious{Pubkey: old.Pubkey(), RotatedAt: at, Endorsement: old.Endorse(k.Pubkey(), at)}
		}},
		{name: "bad endorsement", pin: old.Pubkey(), mutate: func(d *envelope.HubIdentity) {
			d.Previous = &envelope.HubKeyPrevious{Pubkey: old.Pubkey(), RotatedAt: "2026-11-05T07:00:00.000Z", Endorsement: "bad"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := identityStub(t, k, tc.mutate)
			doc, rotated, e := verifyHub(context.Background(), srv.URL, tc.pin, false)
			if tc.ok {
				if e != nil || rotated != tc.rotated || doc.Pubkey != k.Pubkey() {
					t.Fatalf("%+v %t %v", doc, rotated, e)
				}
			} else {
				var re *relocationError
				if !errors.As(e, &re) || re.Code != "relocation_unverified" {
					t.Fatalf("wanted relocation_unverified: %v", e)
				}
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	originalTransport := http.DefaultTransport
	var publicRequests atomic.Int32
	http.DefaultTransport = pinRoundTripper(func(r *http.Request) (*http.Response, error) {
		publicRequests.Add(1)
		return nil, errors.New("unexpected public request")
	})
	_, _, publicErr := verifyHub(ctx, "http://public.example", k.Pubkey(), false)
	http.DefaultTransport = originalTransport
	if publicErr == nil || publicRequests.Load() != 0 {
		t.Fatalf("public http was not refused before a request: %v, %d requests", publicErr, publicRequests.Load())
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer relay.Close()
	if _, _, e := verifyHub(ctx, relay.URL, k.Pubkey(), false); e == nil || redirected.Load() != 0 {
		t.Fatalf("followed identity redirect: %v %d", e, redirected.Load())
	}
}

type pinRoundTripper func(*http.Request) (*http.Response, error)

func (f pinRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEffectivePinScopeAndPrecedence(t *testing.T) {
	_, env := pinTestEnv(t, "")
	a := &app{ctx: context.Background(), getenv: env}
	if err := a.updatePins(func(m map[string]pinRecord) error {
		m["p"] = pinRecord{Hub: "https://hub.example", Pubkey: "sidecar", How: "tofu"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p        Profile
		pub, how string
	}{
		{Profile{Hub: "https://HUB.example:443/"}, "sidecar", "tofu"},
		{Profile{Hub: "https://elsewhere.example"}, "", ""},
		{Profile{Hub: "https://hub.example", HubPubkey: "explicit"}, "explicit", "config"},
	} {
		pub, how := a.effectivePin("p", tc.p)
		if pub != tc.pub || how != tc.how {
			t.Fatalf("%+v: %s %s", tc.p, pub, how)
		}
	}
}

func TestTOFURetryAfter24Hours(t *testing.T) {
	k := testIdentityKey(t, "1")
	srv, calls := identityStub(t, k, nil)
	_, env := pinTestEnv(t, "")
	a := &app{ctx: context.Background(), getenv: env}
	if err := a.updatePins(func(m map[string]pinRecord) error {
		m["p"] = pinRecord{Hub: srv.URL, TofuTriedAt: time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339Nano)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.ensurePin("p", Profile{Hub: srv.URL})
	pub, how := a.effectivePin("p", Profile{Hub: srv.URL})
	if pub != k.Pubkey() || how != "tofu" || calls.Load() != 1 {
		t.Fatalf("retry %s %s %d", pub, how, calls.Load())
	}
}
