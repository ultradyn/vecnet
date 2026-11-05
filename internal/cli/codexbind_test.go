package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	if !strings.Contains(out.String(), "bound p to Codex thread "+bindT1+"; the launcher connector picks it up within 10 s") {
		t.Fatalf("output=%q", out.String())
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
	f.rejected("identity", "--net", "q", "bind", "--thread", bindT2)
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
