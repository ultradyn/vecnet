package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

const bindT1 = "01234567-89ab-cdef-0123-456789abcdef"
const bindT2 = "11234567-89ab-cdef-0123-456789abcdef"

type bindFixture struct {
	t               *testing.T
	env             map[string]string
	path, home, hub string
	peer            atomic.Value
	now             time.Time
	server          *httptest.Server
}

func newBindFixture(t *testing.T) *bindFixture {
	t.Helper()
	d := t.TempDir()
	f := &bindFixture{t: t, home: filepath.Join(d, "codex-home"), path: filepath.Join(d, "codex", "bindings.json"), now: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)}
	f.peer.Store("peer_A")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/peers/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": f.peer.Load().(string)})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.server.Close)
	f.hub = f.server.URL
	f.env = map[string]string{"HOME": d, "VECNET_CONFIG": filepath.Join(d, "v.toml"), "XDG_CONFIG_HOME": filepath.Join(d, "config"), "XDG_CACHE_HOME": filepath.Join(d, "cache"), "CODEX_HOME": f.home, "VECNET_CODEX_BINDINGS": f.path, "VECNET_PROFILE": "p"}
	if err := os.Mkdir(f.home, 0700); err != nil {
		t.Fatal(err)
	}
	f.config(f.hub + "/")
	return f
}
func (f *bindFixture) config(hub string) {
	f.t.Helper()
	profiles := map[string]Profile{}
	for _, p := range []string{"p", "q", "r", "p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9"} {
		profiles[p] = Profile{Hub: hub, Secret: "vn_peer_test"}
	}
	if err := saveConfig(f.env["VECNET_CONFIG"], Config{Profiles: profiles}); err != nil {
		f.t.Fatal(err)
	}
}

// The clock is injected at the command boundary; all flags still pass through
// the CLI parser. The fixture's getenv has no fallback to the host environment.
func (f *bindFixture) run(args ...string) (int, string, string) {
	f.t.Helper()
	var out, errs bytes.Buffer
	a := &app{ctx: context.Background(), stdin: strings.NewReader(""), stdout: &out, stderr: &errs, getenv: func(k string) string { return f.env[k] }}
	rest, _, err := a.parseGlobal(args)
	if err == nil {
		err = a.cmdCodex(rest, func() time.Time { return f.now })
	}
	code := a.exit(err)
	return code, out.String(), errs.String()
}
func (f *bindFixture) ok(args ...string) string {
	f.t.Helper()
	code, out, errs := f.run(args...)
	if code != 0 {
		f.t.Fatalf("%v: exit %d: %s", args, code, errs)
	}
	return out
}
func (f *bindFixture) bind(profile, thread string, args ...string) string {
	f.t.Helper()
	return f.ok(append([]string{"--net", profile, "bind", "--thread", thread}, args...)...)
}
func (f *bindFixture) raw() []byte {
	f.t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}
func (f *bindFixture) entries() []map[string]json.RawMessage {
	f.t.Helper()
	var file struct {
		Version  int                          `json:"version"`
		Bindings []map[string]json.RawMessage `json:"bindings"`
	}
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		f.t.Fatal(err)
	}
	if file.Version != 1 {
		f.t.Fatalf("version=%d", file.Version)
	}
	return file.Bindings
}
func bindValue[T any](t *testing.T, entry map[string]json.RawMessage, key string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(entry[key], &v); err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return v
}
func (f *bindFixture) rejected(hint string, args ...string) {
	f.t.Helper()
	before := f.raw()
	code, _, errs := f.run(args...)
	if code != 1 || !strings.Contains(errs, hint) {
		f.t.Fatalf("%v: exit %d, stderr %q, want exit 1 with %q", args, code, errs, hint)
	}
	if !bytes.Equal(before, f.raw()) {
		f.t.Fatalf("rejected command changed file: %v", args)
	}
}
func bindID(i int) string { return fmt.Sprintf("%08x-89ab-cdef-0123-456789abcdef", i) }

func TestCodexBindWritesFile(t *testing.T) {
	f := newBindFixture(t)
	// Exercise the real dispatch entry point as well as the clock seam.
	var out, errs bytes.Buffer
	code := Run([]string{"codex", "bind", "--thread", bindT1}, strings.NewReader(""), &out, &errs, func(k string) string { return f.env[k] })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	entries := f.entries()
	if len(entries) != 1 {
		t.Fatalf("entries=%v", entries)
	}
	b := entries[0]
	if bindValue[string](t, b, "profile") != "p" || bindValue[string](t, b, "as") != "" || bindValue[string](t, b, "thread_id") != bindT1 || bindValue[string](t, b, "codex_home") != f.home {
		t.Fatalf("entry=%s", f.raw())
	}
	if !strings.Contains(out.String(), "bound p to Codex thread "+bindT1+"; run `vecnet --net 'p' codex status` to inspect delivery state") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestCodexBindStatusGuidance(t *testing.T) {
	for _, tc := range []struct {
		name, profile, envProfile, as string
	}{
		{"no environment profile", "p", "", ""},
		{"different environment hub", "p", "q", ""},
		{"selected as", "p", "q", "peer_x"},
		{"shell arguments", "p 'quoted' $HOME; `false`", "q", "peer 'quoted' $HOME; `false`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, other := newBindFixture(t), newBindFixture(t)
			f.env["VECNET_PROFILE"] = tc.envProfile
			if err := saveConfig(f.env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{
				tc.profile: {Hub: f.hub, Secret: "vn_peer_test"},
				"q":        {Hub: other.hub, Secret: "vn_peer_test"},
			}}); err != nil {
				t.Fatal(err)
			}
			// The default identity stays disabled when a selected --as row binds.
			f.ok("--net", tc.profile, "unbind")
			out := f.bind(tc.profile, bindT1, "--as", tc.as)
			_, suggestion, found := strings.Cut(out, "; run `")
			suggestion, _, ended := strings.Cut(suggestion, "` to inspect delivery state")
			if !found || !ended {
				t.Fatalf("missing status suggestion: %q", out)
			}
			// Let a real shell decode the emitted command into argv. The vecnet
			// function only prints arguments; no installed binary or host env is used.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "vecnet() { printf '%s\\000' \"$@\"; }; "+suggestion)
			cmd.Dir = f.env["HOME"]
			cmd.Env = []string{"HOME=" + f.env["HOME"], "PATH=/nonexistent"}
			raw, err := cmd.Output()
			if err != nil {
				t.Fatalf("status suggestion %q: %v", suggestion, err)
			}
			argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
			selected := &app{getenv: func(k string) string { return f.env[k] }}
			rest, _, err := selected.parseGlobal(argv)
			if err != nil {
				t.Fatal(err)
			}
			if selected.net != tc.profile || selected.as != tc.as || strings.Join(rest, " ") != "codex status" {
				t.Errorf("suggestion lost bound selectors: argv=%q net=%q as=%q", argv, selected.net, selected.as)
			}
			checkStatus := func(disabled bool) {
				t.Helper()
				var status, errs bytes.Buffer
				code := Run(argv, strings.NewReader(""), &status, &errs, func(k string) string { return f.env[k] })
				if code != 0 || !strings.Contains(status.String(), "Codex auto-delivery for "+f.hub+": ") || strings.Contains(status.String(), other.hub) || strings.Contains(status.String(), "disabled by explicit opt-out") != disabled {
					t.Errorf("following %q: exit=%d stdout=%q stderr=%q, want hub=%s disabled=%t", suggestion, code, status.String(), errs.String(), f.hub, disabled)
				}
			}
			checkStatus(false)
			if tc.as != "" {
				if out := f.ok("--net", tc.profile, "status"); runtime.GOOS == "linux" && !strings.Contains(out, "disabled by explicit opt-out") {
					t.Fatalf("default identity lost independent opt-out: %q", out)
				}
				f.ok("--net", tc.profile, "--as", tc.as, "unbind")
				checkStatus(runtime.GOOS == "linux")
			}
		})
	}
}

func TestCodexBindStatusGuidanceUnquotable(t *testing.T) {
	for _, tc := range []struct{ profile, as string }{{"p\nextra", ""}, {"p", "peer\nextra"}} {
		t.Run(fmt.Sprintf("%q@%q", tc.profile, tc.as), func(t *testing.T) {
			f := newBindFixture(t)
			if err := saveConfig(f.env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{
				tc.profile: {Hub: f.hub, Secret: "vn_peer_test"},
			}}); err != nil {
				t.Fatal(err)
			}
			out := f.bind(tc.profile, bindT1, "--as", tc.as)
			if !strings.Contains(out, "select the bound --net profile and --as identity explicitly before running `vecnet codex status`") {
				t.Fatalf("fallback must require explicit bound selectors: %q", out)
			}
		})
	}
}

func TestCodexBindUnique(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	before := f.raw()
	f.now = f.now.Add(time.Hour)
	f.bind("p", bindT1)
	if !bytes.Equal(before, f.raw()) {
		t.Fatal("no-op changed file")
	}
	f.rejected("already bound:", "bind", "--thread", bindT2)
	f.rejected("already bound:", "--net", "q", "bind", "--thread", bindT1)
	f.bind("q", bindT2, "--as", "peer_x")
	if len(f.entries()) != 2 || bindValue[string](t, f.entries()[1], "as") != "peer_x" {
		t.Fatal("--as binding missing")
	}
	// The pair profile@as, rather than the profile alone, is unique.
	f.bind("q", bindID(3), "--as", "peer_y")
	if len(f.entries()) != 3 {
		t.Fatal("distinct --as did not bind")
	}
}
func TestCodexBindRejectsBadIds(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	for _, id := range []string{"../x", strings.ToUpper(bindT1), "", bindT1 + "\n"} {
		f.rejected("thread", "--net", "q", "bind", "--thread", id)
	}
	f.rejected("absolute", "--net", "q", "bind", "--thread", bindT2, "--codex-home", "relative")
}
func TestCodexBindFileMode(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	check := func(path string, want os.FileMode) {
		t.Helper()
		s, e := os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		if s.Mode().Perm() != want {
			t.Fatalf("%s: mode %o want %o", path, s.Mode().Perm(), want)
		}
	}
	check(f.path, 0600)
	check(filepath.Dir(f.path), 0700)
	if err := os.Chmod(f.path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(f.path), 0755); err != nil {
		t.Fatal(err)
	}
	f.bind("p", bindT1)
	check(f.path, 0600)
	check(filepath.Dir(f.path), 0700)
}
func TestCodexUnbind(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	f.bind("q", bindT2)
	f.ok("unbind")
	var list []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(f.ok("bindings", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || bindValue[string](t, list[0], "profile") != "q" || bindValue[string](t, list[0], "state") != "active" {
		t.Fatalf("list=%v", list)
	}
	f.ok("unbind", "--thread", bindT2)
	if out := strings.TrimSpace(f.ok("bindings", "--json")); out != "[]" {
		t.Fatalf("empty list=%s", out)
	}
	f.bind("p", bindT1)
	f.rejected("thread", "unbind", "--thread", "../x")
}
func TestCodexBindTTL(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	b := f.entries()[0]
	if got := bindValue[time.Time](t, b, "expires_at"); !got.Equal(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("default expiry=%s", got)
	}
	if _, ok := b["idle_dormant_hours"]; ok {
		t.Fatal("idle rule enabled by default")
	}
	f.bind("q", bindT2, "--ttl", "2h", "--idle-dormant", "6h")
	b = f.entries()[1]
	if got := bindValue[time.Time](t, b, "expires_at"); !got.Equal(time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry=%s", got)
	}
	if got := bindValue[float64](t, b, "idle_dormant_hours"); got != 6 {
		t.Fatalf("idle=%v", got)
	}
	for _, duration := range []string{"30m", "169h", "nonsense"} {
		f.rejected("ttl", "--net", "r", "bind", "--thread", bindID(3), "--ttl", duration)
		f.rejected("idle-dormant", "--net", "r", "bind", "--thread", bindID(3), "--idle-dormant", duration)
	}
}
func TestCodexBindRenew(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1, "--ttl", "168h", "--idle-dormant", "6h")
	created := f.entries()[0]["created_at"]
	f.now = f.now.Add(24 * time.Hour)
	f.bind("p", bindT1, "--renew", "--ttl", "168h")
	b := f.entries()[0]
	if got := bindValue[time.Time](t, b, "expires_at"); !got.Equal(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("renew added to old expiry instead of now or exceeded cap: %s", got)
	}
	if !bytes.Equal(created, b["created_at"]) || bindValue[float64](t, b, "idle_dormant_hours") != 6 {
		t.Fatal("renew lost creation time or idle option")
	}
	f.rejected("ttl", "bind", "--thread", bindT1, "--renew", "--ttl", "169h")
	f.now = time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	if out := f.ok("bindings"); !strings.Contains(out, "expired") {
		t.Fatalf("expiry boundary=%s", out)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(f.ok("bindings", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if bindValue[string](t, list[0], "state") != "expired" {
		t.Fatal("JSON expiry state missing")
	}
}
func TestCodexBindAttended(t *testing.T) {
	f := newBindFixture(t)
	out := f.bind("p", bindT1)
	if _, ok := f.entries()[0]["attended"]; ok || !strings.Contains(out, "needs -a never or the auto reviewer; pass --attended if a human answers approvals") {
		t.Fatalf("default attended=%s output=%s", f.raw(), out)
	}
	out = f.bind("q", bindT2, "--attended")
	if !bindValue[bool](t, f.entries()[1], "attended") || strings.Contains(out, "needs -a never") {
		t.Fatal("attended flag or hint wrong")
	}
	f.bind("q", bindT2, "--renew")
	if !bindValue[bool](t, f.entries()[1], "attended") {
		t.Fatal("renew cleared attended")
	}
	f.bind("p", bindT1, "--renew", "--attended")
	if !bindValue[bool](t, f.entries()[0], "attended") {
		t.Fatal("renew did not set attended")
	}
}
func TestCodexBindRecordsIdentity(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	b := f.entries()[0]
	if bindValue[string](t, b, "hub") != f.hub || bindValue[string](t, b, "caller") != "peer_A" {
		t.Fatalf("identity=%s", f.raw())
	}
	f.peer.Store("peer_B")
	f.rejected("identity changed since bind; unbind and bind again", "bind", "--thread", bindT1, "--renew")
	f.server.Close()
	before := f.raw()
	code, _, errs := pinRun(func(k string) string { return f.env[k] }, "--net", "q", "codex", "bind", "--thread", bindT2)
	if code != 1 || !strings.Contains(errs, "identity") || !bytes.Equal(before, f.raw()) {
		t.Fatalf("unreachable bind: exit %d stderr %q", code, errs)
	}
}
func TestCodexBindCanonicalHome(t *testing.T) {
	f := newBindFixture(t)
	alias := filepath.Join(filepath.Dir(f.home), "alias")
	if err := os.Symlink(f.home, alias); err != nil {
		t.Fatal(err)
	}
	f.bind("p0", bindID(0), "--codex-home", alias)
	if bindValue[string](t, f.entries()[0], "codex_home") != f.home {
		t.Fatal("home not canonical")
	}
	for i := 1; i < 8; i++ {
		f.bind(fmt.Sprintf("p%d", i), bindID(i))
	}
	f.rejected("binding limit 8", "--net", "p8", "bind", "--thread", bindID(8), "--codex-home", alias)
	f.rejected("codex-home", "--net", "p8", "bind", "--thread", bindID(8), "--codex-home", filepath.Join(f.home, "missing"))
	file := filepath.Join(filepath.Dir(f.home), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.rejected("directory", "--net", "p8", "bind", "--thread", bindID(8), "--codex-home", file)
}
func TestCodexBindLimit(t *testing.T) {
	for _, limit := range []string{"", "2", "99"} {
		t.Run("limit="+limit, func(t *testing.T) {
			f := newBindFixture(t)
			f.env["VECNET_CODEX_MAX_BINDINGS"] = limit
			n := 8
			if limit == "2" {
				n = 2
			}
			for i := 0; i < n; i++ {
				f.bind(fmt.Sprintf("p%d", i), bindID(i), "--ttl", "1h")
			}
			f.rejected(fmt.Sprintf("binding limit %d reached for %s; unbind one or raise VECNET_CODEX_MAX_BINDINGS", n, f.home), "--net", "p8", "bind", "--thread", bindID(8))
			// No-op and renewal still work when the home is full.
			f.bind("p0", bindID(0))
			f.bind("p0", bindID(0), "--renew", "--ttl", "2h")
			other := filepath.Join(filepath.Dir(f.home), "other")
			if err := os.Mkdir(other, 0700); err != nil {
				t.Fatal(err)
			}
			f.bind("p8", bindID(8), "--codex-home", other)
			f.now = f.now.Add(time.Hour)
			f.bind("p9", bindID(9))
			if len(f.entries()) != n+2 {
				t.Fatal("expiry did not free a slot")
			}
		})
	}
}

// These regressions use real dispatch so automatic follow and its retry are
// exercised, with every path and environment lookup confined to the fixture.
func TestCodexBindTOFU(t *testing.T) {
	for _, verified := range []bool{true, false} {
		t.Run(fmt.Sprintf("verified=%t", verified), func(t *testing.T) {
			f := newBindFixture(t)
			key := testIdentityKey(t, "1")
			var identityCalls, callerCalls atomic.Int32
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/hub/identity":
					identityCalls.Add(1)
					if r.Header.Get("Authorization") != "" {
						t.Error("identity challenge carried credentials")
					}
					if !verified {
						http.NotFound(w, r)
						return
					}
					sig, err := key.SignChallenge(srv.URL, r.URL.Query().Get("challenge"))
					if err != nil {
						t.Error(err)
					}
					_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: srv.URL, ChallengeSig: sig})
				case "/v1/peers/me":
					callerCalls.Add(1)
					pp, _ := pinsPath(func(k string) string { return f.env[k] })
					records, err := readPins(pp)
					if err != nil || records["p"].Hub != srv.URL {
						t.Errorf("identity lookup preceded TOFU bookkeeping: records=%+v err=%v", records, err)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": "peer_A"})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			f.config(srv.URL)
			before := pinRead(t, f.env["VECNET_CONFIG"])
			for i := 0; i < 2; i++ {
				if code, _, errs := pinRun(func(k string) string { return f.env[k] }, "codex", "bind", "--thread", bindT1); code != 0 {
					t.Fatalf("exit %d: %s", code, errs)
				}
			}
			if identityCalls.Load() != 1 || callerCalls.Load() != 2 {
				t.Fatalf("identity calls=%d caller calls=%d", identityCalls.Load(), callerCalls.Load())
			}
			pp, _ := pinsPath(func(k string) string { return f.env[k] })
			records, err := readPins(pp)
			if err != nil {
				t.Fatal(err)
			}
			r := records["p"]
			if verified {
				if r.Pubkey != key.Pubkey() || r.How != "tofu" || r.PinnedAt == "" || r.TofuTriedAt != "" {
					t.Fatalf("missing verified pin: %+v", r)
				}
			} else if r.Pubkey != "" || r.TofuTriedAt == "" {
				t.Fatalf("missing failed-attempt record: %+v", r)
			}
			if !bytes.Equal(before, pinRead(t, f.env["VECNET_CONFIG"])) {
				t.Fatal("TOFU rewrote config")
			}
		})
	}
}

func TestCodexBindFollowsSignedMove(t *testing.T) {
	for _, mode := range []string{"follow", "no-follow", "bad-signature"} {
		t.Run(mode, func(t *testing.T) {
			f := newBindFixture(t)
			key := testIdentityKey(t, "1")
			var targetIdentity, targetCaller, sourceCaller atomic.Int32
			var target *httptest.Server
			target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/hub/identity":
					targetIdentity.Add(1)
					if r.Header.Get("Authorization") != "" {
						t.Error("destination verification carried credentials")
					}
					sig, err := key.SignChallenge(target.URL, r.URL.Query().Get("challenge"))
					if err != nil {
						t.Error(err)
					}
					_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: target.URL, ChallengeSig: sig})
				case "/v1/peers/me":
					targetCaller.Add(1)
					if targetIdentity.Load() != 1 || r.Header.Get("Authorization") != "Bearer vn_peer_test" {
						t.Error("retry did not authenticate after destination verification")
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"kind": "peer", "peer_id": "peer_A"})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(target.Close)
			var notice envelope.HubNotice
			var source *httptest.Server
			source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/hub/identity" {
					if r.Header.Get("Authorization") != "" {
						t.Error("source verification carried credentials")
					}
					sig, err := key.SignChallenge(source.URL, r.URL.Query().Get("challenge"))
					if err != nil {
						t.Error(err)
					}
					_ = json.NewEncoder(w).Encode(envelope.HubIdentity{Pubkey: key.Pubkey(), PublicURL: source.URL, ChallengeSig: sig})
					return
				}
				if r.URL.Path == "/v1/peers/me" {
					sourceCaller.Add(1)
					w.WriteHeader(http.StatusGone)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "hub_moved", "message": "moved", "details": map[string]any{"notice": notice}}})
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(source.Close)
			notice = envelope.HubNotice{NoticeID: "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV", Kind: "moving", Hub: source.URL, NewURL: target.URL, Message: "move", IssuedAt: envelope.FormatTime(time.Now().Add(-time.Second)), ExpiresAt: envelope.FormatTime(time.Now().Add(time.Hour))}
			if err := key.SignNotice(&notice); err != nil {
				t.Fatal(err)
			}
			if mode == "bad-signature" {
				notice.Sig = "invalid"
			}
			if mode == "no-follow" {
				f.env["VECNET_NO_FOLLOW"] = "1"
			}
			f.config(source.URL)
			before := pinRead(t, f.env["VECNET_CONFIG"])
			code, _, errs := pinRun(func(k string) string { return f.env[k] }, "codex", "bind", "--thread", bindT1)
			if sourceCaller.Load() != 1 {
				t.Fatalf("source identity calls=%d", sourceCaller.Load())
			}
			if mode != "follow" {
				hint := "signature"
				if mode == "no-follow" {
					hint = "hub moved; run `vecnet --net p profile follow`"
				}
				if code != 1 || !strings.Contains(errs, "relocation_unverified") || !strings.Contains(errs, hint) {
					t.Fatalf("exit %d: %s", code, errs)
				}
				if targetIdentity.Load() != 0 || targetCaller.Load() != 0 || !bytes.Equal(before, pinRead(t, f.env["VECNET_CONFIG"])) {
					t.Fatal("refused move contacted destination or changed config")
				}
				if _, err := os.Stat(f.path); !os.IsNotExist(err) {
					t.Fatalf("refused bind wrote bindings: %v", err)
				}
				return
			}
			if code != 0 || targetIdentity.Load() != 1 || targetCaller.Load() != 1 || !strings.Contains(errs, "profile p now uses "+target.URL) {
				t.Fatalf("exit %d, destination identity=%d caller=%d: %s", code, targetIdentity.Load(), targetCaller.Load(), errs)
			}
			cfg, err := loadConfig(f.env["VECNET_CONFIG"])
			if err != nil || cfg.Profiles["p"].Hub != target.URL || cfg.Profiles["p"].HubPubkey != key.Pubkey() {
				t.Fatalf("move not recorded in config: %+v %v", cfg.Profiles["p"], err)
			}
			entry := f.entries()[0]
			if bindValue[string](t, entry, "hub") != target.URL || bindValue[string](t, entry, "caller") != "peer_A" {
				t.Fatalf("binding did not use retried identity: %s", f.raw())
			}
			pp, _ := pinsPath(func(k string) string { return f.env[k] })
			records, err := readPins(pp)
			r := records["p"]
			if err != nil || r.How != "follow" || r.LastMove == nil || r.LastMove.From != source.URL || r.LastMove.To != target.URL || r.LastMove.NoticeID != notice.NoticeID {
				t.Fatalf("move not recorded in sidecar: %+v %v", r, err)
			}
		})
	}
}

func TestCodexBindOperatorClientLabel(t *testing.T) {
	for _, agent := range []bool{false, true} {
		t.Run(fmt.Sprintf("agent=%t", agent), func(t *testing.T) {
			f := newBindFixture(t)
			f.env["VECNET_HUMAN"] = "1"
			if agent {
				f.env["VECNET_AGENT"] = "1"
			}
			var callerCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/peers/me" {
					http.NotFound(w, r)
					return
				}
				callerCalls.Add(1)
				if got := r.Header.Get("Authorization"); got != "Bearer vn_operator_test" {
					t.Errorf("authorization=%q", got)
				}
				var label envelope.OperatorClient
				if err := json.Unmarshal([]byte(r.Header.Get(envelope.ClientHeader)), &label); err != nil {
					t.Errorf("invalid operator client label: %v", err)
				} else if label.Kind != envelope.ClientKindOperatorCLI || label.Host == "" || label.AgentSuspected != agent {
					t.Errorf("operator client label=%+v, want operator-cli with agent_suspected=%t", label, agent)
				}
				_, _ = fmt.Fprint(w, `{"kind":"operator"}`)
			}))
			t.Cleanup(srv.Close)
			if err := saveConfig(f.env["VECNET_CONFIG"], Config{Profiles: map[string]Profile{
				"p": {Hub: srv.URL, Secret: "vn_operator_test"},
			}}); err != nil {
				t.Fatal(err)
			}
			code, _, errs := pinRun(func(k string) string { return f.env[k] }, "--net", "p", "codex", "bind", "--thread", bindT1)
			if code != 1 || !strings.Contains(errs, "cannot resolve binding identity") || callerCalls.Load() != 1 {
				t.Fatalf("exit %d, caller calls=%d: %s", code, callerCalls.Load(), errs)
			}
			if _, err := os.Stat(f.path); !os.IsNotExist(err) {
				t.Fatalf("operator identity wrote bindings: %v", err)
			}
		})
	}
}

func TestCodexBindCallerKinds(t *testing.T) {
	for _, tc := range []struct {
		name, body, caller string
	}{
		{"principal", `{"kind":"principal","principal_id":"principal_A"}`, "principal_A"},
		{"operator", `{"kind":"operator"}`, ""},
		{"missing peer", `{"kind":"peer"}`, ""},
		{"invalid peer", `{"kind":"peer","peer_id":"../unsafe"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBindFixture(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/peers/me" {
					_, _ = fmt.Fprint(w, tc.body)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(srv.Close)
			f.config(srv.URL)
			code, _, errs := pinRun(func(k string) string { return f.env[k] }, "codex", "bind", "--thread", bindT1)
			if tc.caller == "" {
				if code != 1 || !strings.Contains(errs, "cannot resolve binding identity") {
					t.Fatalf("exit %d: %s", code, errs)
				}
				if _, err := os.Stat(f.path); !os.IsNotExist(err) {
					t.Fatalf("unusable identity wrote bindings: %v", err)
				}
			} else if code != 0 || bindValue[string](t, f.entries()[0], "caller") != tc.caller {
				t.Fatalf("principal identity: exit %d: %s", code, errs)
			}
		})
	}
}

func TestCodexBindTakeover(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	f.rejected("already bound:", "bind", "--thread", bindT2)
	f.bind("p", bindT2, "--takeover")
	if len(f.entries()) != 1 || bindValue[string](t, f.entries()[0], "thread_id") != bindT2 {
		t.Fatal("takeover did not replace binding")
	}
	f.rejected("already bound:", "--net", "q", "bind", "--thread", bindT2, "--takeover")
}

func TestCodexAutoOptOut(t *testing.T) {
	f := newBindFixture(t)
	f.ok("unbind")
	var file struct {
		Disabled []struct{ Profile, As string }
	}
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Disabled) != 1 || file.Disabled[0].Profile != "p" {
		t.Fatalf("unbind without a row must disable p: %s", f.raw())
	}
	f.bind("p", bindT1)
	file.Disabled = nil
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Disabled) != 0 {
		t.Fatalf("explicit bind must clear opt-out: %s", f.raw())
	}
	f.ok("unbind", "--thread", bindT1)
	if err := json.Unmarshal(f.raw(), &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Disabled) != 1 || file.Disabled[0].Profile != "p" {
		t.Fatalf("thread unbind must disable its profile: %s", f.raw())
	}
}
