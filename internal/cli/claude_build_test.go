package cli

// The pinned build table is the whole qualification mechanism (delivery
// Phase 2, task 1): these tests hold it to the Global Constraints entry and
// refuse every other path to a qualified build, and pin the digest cache
// document's strict v1 shape and eviction. The kernel build evidence itself
// needs /proc, so those tests live in claude_build_linux_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// claudeTestBuildLabel is the label the test-only table extension injects.
const claudeTestBuildLabel = "fixture-build"

// claudeWithBuild extends the pinned table with one row for a single test
// and restores the table when it ends. Only _test.go code may extend the
// table; no env var, flag or file ever can.
func claudeWithBuild(t *testing.T, b claudeQualifiedBuild) {
	t.Helper()
	saved := append([]claudeQualifiedBuild(nil), claudeQualifiedBuilds...)
	claudeQualifiedBuilds = append(append([]claudeQualifiedBuild(nil), saved...), b)
	t.Cleanup(func() { claudeQualifiedBuilds = saved })
}

// claudeWithQualifiedBuild tables one digest for this platform under the
// fixture label, with t.Cleanup restoring the pinned table. It lives here so
// every platform's tests can share it, like claudeFixtureVersion did.
func claudeWithQualifiedBuild(t *testing.T, sha string) {
	t.Helper()
	claudeWithBuild(t, claudeQualifiedBuild{
		Label:    claudeTestBuildLabel,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		SHA256:   sha,
	})
}

func TestClaudeQualifiedBuildTableIsPinned(t *testing.T) {
	want := []claudeQualifiedBuild{
		{Label: "2.1.322", Platform: "linux/amd64", SHA256: "bfb3703ad5380242328459bc62ada4bde8ae7ba57c6e1eeadb9c00e08874c478"},
	}
	if !reflect.DeepEqual(claudeQualifiedBuilds, want) {
		t.Fatalf("pinned table changed: %+v", claudeQualifiedBuilds)
	}
	if _, ok := claudeQualifiedBuildFor(strings.Repeat("0", 64)); ok {
		t.Fatal("an unknown digest qualified")
	}
	// A tabled digest whose platform differs never qualifies: the same
	// bytes on another platform are a different, untested build.
	claudeWithBuild(t, claudeQualifiedBuild{Label: "off-platform", Platform: "plan9/arm", SHA256: strings.Repeat("a", 64)})
	if _, ok := claudeQualifiedBuildFor(strings.Repeat("a", 64)); ok {
		t.Fatal("a digest qualified on a platform the build was not tested on")
	}
	if !reflect.DeepEqual(claudeQualifiedBuilds[:len(claudeQualifiedBuilds)-1], want) {
		t.Fatalf("test extension disturbed the pinned rows: %+v", claudeQualifiedBuilds)
	}
}

// TestClaudeExeDigestsCacheDocument pins the private digest cache: a strict
// v1 document (exact keys, no duplicates, well-formed rows) and a store that
// caps the document at 16 rows by evicting the oldest observation.
func TestClaudeExeDigestsCacheDocument(t *testing.T) {
	d, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": d}
	a := &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return env[k] }}
	ctx := context.Background()

	sha := func(n int) string { return fmt.Sprintf("%064x", n) }
	bad := []string{
		``,
		`{"version":2,"entries":[]}`,
		`{"version":1}`,
		`{"entries":[]}`,
		`{"version":1,"entries":[],"extra":0}`,
		`{"version":1,"entries":[{"identity":"1:2:3:4","sha256":"` + sha(1) + `","observed_at":"2026-01-02T03:04:05Z"}]}`,
		`{"version":1,"entries":[{"identity":"1:2:3:4:5:x","sha256":"` + sha(1) + `","observed_at":"2026-01-02T03:04:05Z"}]}`,
		`{"version":1,"entries":[{"identity":"1:2:3:4:5","sha256":"nothex","observed_at":"2026-01-02T03:04:05Z"}]}`,
		`{"version":1,"entries":[{"identity":"1:2:3:4:5","sha256":"` + strings.Repeat("A", 64) + `","observed_at":"2026-01-02T03:04:05Z"}]}`,
		`{"version":1,"entries":[{"identity":"1:2:3:4:5","sha256":"` + sha(1) + `"}]}`,
		`{"version":1,"entries":[{"identity":"1:2:3:4:5","sha256":"` + sha(1) + `","observed_at":"not-a-time"}]}`,
		`{"version":1,"entries":[{"identity":"1:2:3:4:5","sha256":"` + sha(1) + `","observed_at":"2026-01-02T03:04:05Z","identity":"1:2:3:4:5"}]}`,
	}
	for _, raw := range bad {
		if _, err := claudeExeDigestsDecode([]byte(raw)); err == nil {
			t.Fatalf("decoded an invalid digest document: %s", raw)
		}
	}

	// Seed the document with a full set of rows with increasing ages.
	seed := claudeExeDigestsEmpty()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 1; i <= claudeExeDigestsMax; i++ {
		seed.Entries = append(seed.Entries, claudeExeDigest{
			Identity:   fmt.Sprintf("%d:%d:%d:%d:%d", i, i, i, i, i),
			SHA256:     sha(i),
			ObservedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	unlock, err := a.lockLocalState(ctx, claudeExeDigestsLock)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(seed)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := a.writeLocalState(claudeExeDigestsName, raw); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if got, ok := a.claudeExeDigestLookup("1:1:1:1:1"); !ok || got != sha(1) {
		t.Fatalf("seeded lookup failed: %q %v", got, ok)
	}

	// One more identity evicts the oldest row, never the newer ones.
	fresh := "99:99:99:99:99"
	if err := a.claudeExeDigestStore(ctx, fresh, sha(99), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.claudeExeDigestLookup("1:1:1:1:1"); ok {
		t.Fatal("the oldest observation survived eviction")
	}
	if got, ok := a.claudeExeDigestLookup(fresh); !ok || got != sha(99) {
		t.Fatalf("the fresh observation was not stored: %q %v", got, ok)
	}
	raw, err = a.readLocalState(claudeExeDigestsName)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := claudeExeDigestsDecode(raw)
	if err != nil {
		t.Fatalf("stored document is not strict v1: %v", err)
	}
	if len(doc.Entries) != claudeExeDigestsMax {
		t.Fatalf("document holds %d rows, want the fixed cap %d", len(doc.Entries), claudeExeDigestsMax)
	}
	for _, e := range doc.Entries[1:] {
		if !doc.Entries[0].ObservedAt.Before(e.ObservedAt) {
			t.Fatalf("eviction was not by observed_at: %+v", doc.Entries)
		}
	}

	// A missing or corrupt document reads as a miss and self-heals on the
	// next store.
	if err := a.writeLocalState(claudeExeDigestsName, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.claudeExeDigestLookup(fresh); ok {
		t.Fatal("a corrupt document resolved a digest")
	}
	if err := a.claudeExeDigestStore(ctx, "98:98:98:98:98", sha(98), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	raw, err = a.readLocalState(claudeExeDigestsName)
	if err != nil {
		t.Fatal(err)
	}
	doc, err = claudeExeDigestsDecode(raw)
	if err != nil {
		t.Fatalf("store did not self-heal a corrupt document: %v", err)
	}
	if len(doc.Entries) != 1 || doc.Entries[0].Identity != "98:98:98:98:98" {
		t.Fatalf("self-healed document is not the fresh row: %+v", doc.Entries)
	}
}
