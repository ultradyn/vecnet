package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func hookTestEnv(t *testing.T) map[string]string {
	t.Helper()
	d := t.TempDir()
	return map[string]string{"VECNET_CONFIG": filepath.Join(d, "config", "v.toml"), "XDG_CONFIG_HOME": filepath.Join(d, "xdg"), "XDG_CACHE_HOME": filepath.Join(d, "cache"), "HOME": filepath.Join(d, "home")}
}

func TestStatePathPerProfileAndPeer(t *testing.T) {
	env := hookTestEnv(t)
	get := func(k string) string { return env[k] }
	dir := filepath.Dir(env["VECNET_CONFIG"])
	for _, tc := range []struct{ profile, as, base string }{
		{"p", "", "p.json"}, {"p", "peer_1", "p@peer_1.json"},
		{"a/b", "", "x-c14cddc033f64b9d.json"},
		{"p", "a/b", "p@x-c14cddc033f64b9d.json"},
	} {
		got, err := statePath(get, tc.profile, tc.as)
		if want := filepath.Join(dir, "state", tc.base); err != nil || got != want {
			t.Fatalf("statePath(%q,%q) = %q, %v; want %q", tc.profile, tc.as, got, err, want)
		}
	}
	delete(env, "VECNET_CONFIG")
	got, err := statePath(get, "p", "")
	if want := filepath.Join(env["XDG_CONFIG_HOME"], "vecnet", "state", "p.json"); err != nil || got != want {
		t.Fatalf("XDG state path = %q, %v; want %q", got, err, want)
	}
}

func TestClaimRing(t *testing.T) {
	var s hookState
	if !s.claim("d1") || s.claim("d1") {
		t.Fatal("first claim must succeed, duplicate must fail")
	}
	for i := 0; i < 1500; i++ {
		if !s.claim(fmt.Sprintf("other%d", i)) {
			t.Fatalf("new id %d rejected", i)
		}
	}
	if len(s.Shown) != 1024 || s.has("d1") || !s.has("other1499") || !s.claim("d1") {
		t.Fatalf("ring failed eviction or membership: len=%d", len(s.Shown))
	}
}

func readHookState(t *testing.T, path string) hookState {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s hookState
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStateConcurrentWriters(t *testing.T) {
	env := hookTestEnv(t)
	path, err := statePath(func(k string) string { return env[k] }, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	var claims [200]atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			f := stateFile{path: path}
			for j := 0; j < 200; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				ok := f.claimOne(ctx, fmt.Sprintf("d%d", j))
				cancel()
				if ok {
					claims[j].Add(1)
				}
			}
		})
	}
	wg.Wait()
	for i := range claims {
		if n := claims[i].Load(); n != 1 {
			t.Errorf("d%d claimed %d times, want 1", i, n)
		}
	}
	s := readHookState(t, path)
	if len(s.Shown) != 200 {
		t.Fatalf("stored %d ids, want 200", len(s.Shown))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %o", info.Mode().Perm())
	}
}

func TestStateBusyTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	raw := []byte(`{"shown":["before"]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockPath(context.Background(), path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = (stateFile{path: path}).update(ctx, func(s *hookState) error { t.Error("busy update invoked callback"); return nil })
	if !errors.Is(err, errStateBusy) || time.Since(start) >= 300*time.Millisecond {
		t.Fatalf("busy update = %v after %s", err, time.Since(start))
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("busy update changed bytes: %q, %v", got, err)
	}
}

func TestStateCorruptReadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	// A partially decoded corrupt object must not retain any of its fields.
	for _, raw := range []string{"{not json", `{"poll_cursor":"bad","shown":["bad"],"last_post_tool_poll":42}`} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		err := (stateFile{path: path}).update(context.Background(), func(s *hookState) error {
			if s.PollCursor != "" || len(s.Shown) != 0 || !s.LastPostToolPoll.IsZero() {
				t.Errorf("corrupt state not empty: %+v", s)
			}
			s.claim("recovered")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if s := readHookState(t, path); len(s.Shown) != 1 || s.Shown[0] != "recovered" {
			t.Fatalf("repaired state = %+v", s)
		}
	}
}

func TestStateUpdateErrorAndFailOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	f := stateFile{path: path}
	sentinel := errors.New("callback failed")
	if err := f.update(context.Background(), func(s *hookState) error { s.claim("no"); return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("callback failure wrote state: %v", err)
	}
	if !f.claimOne(context.Background(), "yes") {
		t.Fatal("callback failure did not unlock")
	}
	bad := stateFile{path: filepath.Join(path, "impossible.json")}
	if !bad.claimOne(context.Background(), "open") {
		t.Fatal("I/O error must fail open")
	}
	if s := readHookState(t, path); strings.Join(s.Shown, ",") != "yes" {
		t.Fatalf("state = %+v", s)
	}
}
