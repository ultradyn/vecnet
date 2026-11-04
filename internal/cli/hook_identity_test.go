package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

// identityHub is a fake hub whose GET /v1/peers/me answers peer, and whose
// event stream is scripted by the after= value of each poll.
type identityHub struct {
	peer   atomic.Value // string
	whoami atomic.Int32
	pages  map[string][]envelope.Event // after= -> events
	next   map[string]string           // after= -> next_cursor
}

func newIdentityHub(peer string) *identityHub {
	h := &identityHub{pages: map[string][]envelope.Event{}, next: map[string]string{}}
	h.peer.Store(peer)
	return h
}

func (h *identityHub) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/peers/me":
		h.whoami.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": h.peer.Load().(string)})
	case "/v1/events":
		after := r.URL.Query().Get("after")
		hookPage(w, h.pages[after], h.next[after])
	default:
		http.NotFound(w, r)
	}
}

// identityFixture serves h for profile p (secret vn_peer_test) and returns
// the env, p's state path and the hub's normalized URL.
func identityFixture(t *testing.T, h *identityHub) (map[string]string, string, string) {
	t.Helper()
	env, path := hookFixtureRaw(t, h.serve)
	conf, err := os.ReadFile(env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	hub := regexp.MustCompile(`hub = "([^"]+)"`).FindSubmatch(conf)
	return env, path, normalizeHub(string(hub[1]))
}

func seedState(t *testing.T, path string, s hookState) {
	t.Helper()
	if err := (stateFile{path}).update(context.Background(), func(cur *hookState) error { *cur = s; return nil }); err != nil {
		t.Fatal(err)
	}
}

// A state bound to another hub or another peer must not leak its cursor or
// shown ids into this identity's stream: the hook takes the first-run path.
func TestHookStateIdentityMismatchResets(t *testing.T) {
	for _, tc := range []struct{ name, hub, caller string }{
		{"different hub", "http://old-hub.example:8080", "peer_A"},
		{"different peer", "", "peer_OLD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIdentityHub("peer_A")
			h.pages["head"], h.next["head"] = nil, "e1.100"
			h.pages[""], h.next[""] = []envelope.Event{hookEvent(1, "backlog one"), hookEvent(2, "backlog two")}, "e1.100"
			h.pages["e1.5"], h.next["e1.5"] = []envelope.Event{hookEvent(6, "old stream event")}, "e1.6"
			env, path, hub := identityFixture(t, h)
			stateHub := tc.hub
			if stateHub == "" {
				stateHub = hub
			}
			// A different credential: re-pointing a profile changes its secret.
			seedState(t, path, hookState{Hub: stateHub, Caller: tc.caller, Cred: "0123456789abcdef", PollCursor: "e1.5", Shown: []string{"dly_9", "dly_1"}})
			out, _ := runHook(t, env, "--event", "user-prompt")
			ctx := contextOf(t, out)
			if strings.Contains(ctx, "old stream event") || !strings.Contains(ctx, "2 unacked deliveries waiting") {
				t.Fatalf("mismatch did not take the first-run path: %q", ctx)
			}
			s := readHookState(t, path)
			want := hookState{Hub: hub, Caller: "peer_A", Cred: hookCredential("vn_peer_test"), PollCursor: "e1.100"}
			if s.Hub != want.Hub || s.Caller != want.Caller || s.Cred != want.Cred || s.As != "" || s.PollCursor != want.PollCursor || len(s.Shown) != 0 {
				t.Fatalf("state not rebound: %+v, want %+v", s, want)
			}
		})
	}
}

// Rotating the same peer's credential keeps its stream position.
func TestHookStateCredentialRotationKeeps(t *testing.T) {
	h := newIdentityHub("peer_A")
	h.pages["e1.5"], h.next["e1.5"] = []envelope.Event{hookEvent(6, "after rotation")}, "e1.6"
	env, path, hub := identityFixture(t, h)
	seedState(t, path, hookState{Hub: hub, Caller: "peer_A", Cred: "0123456789abcdef", PollCursor: "e1.5", Shown: []string{"dly_9"}})
	out, _ := runHook(t, env, "--event", "user-prompt")
	if ctx := contextOf(t, out); !strings.Contains(ctx, "after rotation") {
		t.Fatalf("rotation lost the stream: %q", ctx)
	}
	s := readHookState(t, path)
	if s.Caller != "peer_A" || s.Cred != hookCredential("vn_peer_test") || s.PollCursor != "e1.6" || !reflect.DeepEqual(s.Shown, []string{"dly_9", "dly_6"}) {
		t.Fatalf("rotation state=%+v", s)
	}
	if h.whoami.Load() != 1 {
		t.Fatalf("whoami calls=%d, want 1 after a credential change", h.whoami.Load())
	}
	// An unchanged binding is trusted without asking the hub again.
	runHook(t, env, "--event", "user-prompt")
	if h.whoami.Load() != 1 {
		t.Fatalf("whoami calls=%d, want no call for an unchanged binding", h.whoami.Load())
	}
}

// A legacy state (no identity) adopts the current identity once, keeping its
// cursor and shown ids.
func TestHookStateLegacyAdopts(t *testing.T) {
	h := newIdentityHub("peer_A")
	h.pages["e1.5"], h.next["e1.5"] = []envelope.Event{hookEvent(9, "already shown"), hookEvent(6, "legacy continues")}, "e1.6"
	env, path, hub := identityFixture(t, h)
	seedHook(t, path, "e1.5", "dly_9")
	out, _ := runHook(t, env, "--event", "user-prompt")
	ctx := contextOf(t, out)
	if !strings.Contains(ctx, "legacy continues") || strings.Contains(ctx, "already shown") {
		t.Fatalf("legacy state not continued: %q", ctx)
	}
	s := readHookState(t, path)
	if s.Hub != hub || s.Caller != "peer_A" || s.Cred != hookCredential("vn_peer_test") || s.PollCursor != "e1.6" || !reflect.DeepEqual(s.Shown, []string{"dly_9", "dly_6"}) {
		t.Fatalf("legacy state=%+v", s)
	}
}

// Every model-visible command names the profile (and --as), so an agent on a
// non-default profile never acts as the default identity.
func TestHookHintsQualifiedWithProfile(t *testing.T) {
	h := newIdentityHub("peer_A")
	h.pages["head"], h.next["head"] = nil, "e1.10"
	h.pages[""], h.next[""] = []envelope.Event{hookEvent(1, "backlog")}, "e1.10"
	events := make([]envelope.Event, 12)
	for i := range events {
		events[i] = hookEvent(11+i, "new")
	}
	h.pages["e1.10"], h.next["e1.10"] = events, "e1.22"
	env, _, _ := identityFixture(t, h)
	run := func(env map[string]string) string {
		t.Helper()
		var out, errs bytes.Buffer
		code := RunContext(context.Background(), []string{"--as", "peer_1", "hook", "--profile", "q", "--event", "user-prompt"}, strings.NewReader(""), &out, &errs, func(k string) string { return env[k] })
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, errs.String())
		}
		return contextOf(t, out.String())
	}
	const cmd = "vecnet --net q --as peer_1 "
	bare := regexp.MustCompile(`vecnet (reply|ack|inbox)`)
	check := func(ctx string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(ctx, want) {
				t.Fatalf("missing %q in %q", want, ctx)
			}
		}
		if bare.MatchString(ctx) {
			t.Fatalf("unqualified command hint in %q", ctx)
		}
		if !strings.Contains(ctx, "VECNET_CONFIG (v.toml)") || strings.Contains(ctx, filepath.Dir(env["VECNET_CONFIG"])) {
			t.Fatalf("config clause must name only the basename: %q", ctx)
		}
	}
	check(run(env), "`"+cmd+"inbox`")
	check(run(env), cmd+`reply <msg> "text"`, cmd+"ack <cursor>", "or run "+cmd+"inbox)")

	// Without an explicit VECNET_CONFIG there is no config clause.
	xdg := filepath.Join(t.TempDir(), "xdg")
	conf, err := os.ReadFile(env["VECNET_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(xdg, "vecnet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "vecnet", "config.toml"), conf, 0o600); err != nil {
		t.Fatal(err)
	}
	plain := map[string]string{"XDG_CONFIG_HOME": xdg, "XDG_CACHE_HOME": env["XDG_CACHE_HOME"], "HOME": env["HOME"]}
	if ctx := run(plain); strings.Contains(ctx, "VECNET_CONFIG") || !strings.Contains(ctx, "`"+cmd+"inbox`") {
		t.Fatalf("implicit config: %q", ctx)
	}
}
