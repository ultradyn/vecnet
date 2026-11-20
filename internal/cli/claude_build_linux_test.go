//go:build linux

package cli

// The build-observation fixtures observe a real helper process — the Go test
// binary re-entered through TestClaudeHelperProcess, optionally from a copy —
// so /proc/<pid>/exe, start identities and the digest cache exercise the
// kernel's process evidence, not a mock. The real Claude binary and the
// user's live session are never fixtures.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// claudeBuildFixture is one live helper "Claude" process plus the private
// HOME its observer uses for the digest cache.
type claudeBuildFixture struct {
	t     *testing.T
	dir   string
	env   map[string]string
	exe   string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	pid   int
	start string
}

// startClaudeBuildFixture starts the helper from exe (the test binary when
// empty) and waits for its identity row.
func startClaudeBuildFixture(t *testing.T, exe string) *claudeBuildFixture {
	t.Helper()
	d, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if exe == "" {
		if exe, err = filepath.Abs(os.Args[0]); err != nil {
			t.Fatal(err)
		}
	}
	f := &claudeBuildFixture{t: t, dir: d, exe: exe, env: map[string]string{"HOME": d}}
	identity := filepath.Join(d, "identity.json")
	f.cmd = exec.Command(exe, "-test.run=^TestClaudeHelperProcess$")
	f.cmd.Env = append(os.Environ(), "VECNET_CLAUDE_HELPER_ROLE=claude", "VECNET_CLAUDE_IDENTITY="+identity)
	var out, errs bytes.Buffer
	f.cmd.Stdout, f.cmd.Stderr = &out, &errs
	stdin, err := f.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	f.stdin = stdin
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.stdin.Close()
		_ = f.cmd.Wait()
	})
	var id struct {
		PID   int    `json:"pid"`
		Start string `json:"start"`
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		raw, err := os.ReadFile(identity)
		if err == nil && json.Unmarshal(raw, &id) == nil && id.PID > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("build helper did not start: %s %s", out.String(), errs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id.Start == "" {
		t.Fatal("build helper did not report its start identity")
	}
	f.pid, f.start = id.PID, id.Start
	return f
}

func newClaudeBuildFixture(t *testing.T) *claudeBuildFixture {
	t.Helper()
	return startClaudeBuildFixture(t, "")
}

func (f *claudeBuildFixture) app() *app {
	return &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return f.env[k] }}
}

func (f *claudeBuildFixture) observe() (claudeBuildEvidence, error) {
	f.t.Helper()
	return f.app().observeClaudeBuild(context.Background(), f.pid, f.start)
}

// kill ends the helper and reaps it, so /proc/<pid> is gone.
func (f *claudeBuildFixture) kill() {
	f.t.Helper()
	_ = f.cmd.Process.Kill()
	_, _ = f.cmd.Process.Wait()
}

// claudeFileDigest is the plain sha256 of a file's current bytes.
func claudeFileDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// claudeCacheEntries counts the fixture's stored digest rows; a missing
// cache is zero rows.
func claudeCacheEntries(t *testing.T, f *claudeBuildFixture) int {
	t.Helper()
	raw, err := f.app().readLocalState(claudeExeDigestsName)
	if err != nil {
		return 0
	}
	doc, err := claudeExeDigestsDecode(raw)
	if err != nil {
		t.Fatalf("cache document is invalid: %v", err)
	}
	return len(doc.Entries)
}

func TestClaudeBuildDigestComesFromKernelExe(t *testing.T) {
	f := newClaudeBuildFixture(t)
	digest := claudeFileDigest(t, f.exe)
	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.SHA256 != digest {
		t.Fatalf("observed digest %s is not the kernel executable's %s", ev.SHA256, digest)
	}
	if ev.Identity == "" {
		t.Fatal("observation carries no exe identity")
	}
	if ev.Build != "" || ev.Reason != "claude build digest is not qualified" {
		t.Fatalf("untabled build is not legacy: %+v", ev)
	}

	claudeWithQualifiedBuild(t, digest)
	ev, err = f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Build != claudeTestBuildLabel || ev.Reason != "" {
		t.Fatalf("tabled build was not accepted: %+v", ev)
	}
	if ev.SHA256 != digest {
		t.Fatalf("tabled observation changed the digest: %+v", ev)
	}
}

func TestClaudeBuildSurvivesUnlinkedExecutable(t *testing.T) {
	d, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	// An auto-update installs a new version file; the running session keeps
	// executing the old image. Pose that by running the helper from a copy
	// and then unlinking the copy.
	copyPath := filepath.Join(d, "claude-version-copy")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	f := startClaudeBuildFixture(t, copyPath)
	digest := claudeFileDigest(t, copyPath)
	first, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != digest || first.Identity == "" {
		t.Fatalf("first observation is not the copy's digest: %+v", first)
	}
	if err := os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	second, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != digest {
		t.Fatalf("digest changed after the unlink: %s vs %s", second.SHA256, digest)
	}
	if !claudeExeIdentityLive(f.pid, first.Identity) {
		t.Fatal("the running session's exe identity stopped matching after the unlink")
	}
}

func TestClaudeBuildExecPathHint(t *testing.T) {
	f := newClaudeBuildFixture(t)
	digest := claudeFileDigest(t, f.exe)
	claudeWithQualifiedBuild(t, digest)

	// A hint naming another existing executable contradicts the
	// kernel-attested one, so the build stays unknown.
	other := filepath.Join(f.dir, "other-exe")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env["CLAUDE_CODE_EXECPATH"] = other
	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Reason != "CLAUDE_CODE_EXECPATH names a different executable" || ev.Build != "" {
		t.Fatalf("contradictory hint did not keep the build unknown: %+v", ev)
	}

	// A hint naming a missing file (an auto-update unlinked it) is stale
	// and ignored.
	f.env["CLAUDE_CODE_EXECPATH"] = filepath.Join(f.dir, "gone")
	ev, err = f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Build != claudeTestBuildLabel || ev.Reason != "" {
		t.Fatalf("stale hint was not ignored: %+v", ev)
	}

	// A hint naming the same inode as the opened exe is accepted.
	f.env["CLAUDE_CODE_EXECPATH"] = f.exe
	ev, err = f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Build != claudeTestBuildLabel || ev.Reason != "" {
		t.Fatalf("hint naming the same executable refused the build: %+v", ev)
	}
}

func TestClaudeBuildCacheAvoidsRehash(t *testing.T) {
	f := newClaudeBuildFixture(t)
	digest := claudeFileDigest(t, f.exe)
	realHash := claudeBuildHash
	var calls atomic.Int32
	claudeBuildHash = func(ctx context.Context, r io.Reader) (string, error) {
		calls.Add(1)
		return realHash(ctx, r)
	}
	t.Cleanup(func() { claudeBuildHash = realHash })

	if _, err := f.observe(); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("first observation hashed %d times, want 1", n)
	}
	second, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("cached observation rehashed (%d calls total)", n)
	}
	if second.SHA256 != digest {
		t.Fatalf("cached digest differs: %s vs %s", second.SHA256, digest)
	}

	// A new mtime is a new identity: exactly one more hash, then cached
	// again.
	touched := time.Now().Add(2 * time.Minute)
	if err := os.Chtimes(f.exe, touched, touched); err != nil {
		t.Fatal(err)
	}
	if _, err := f.observe(); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("post-touch observation made %d hash calls total, want 2", n)
	}
	if _, err := f.observe(); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("re-cached observation hashed again: %d calls total", n)
	}
}

func TestClaudeBuildObservationIsBounded(t *testing.T) {
	f := newClaudeBuildFixture(t)
	realHash, realBudget := claudeBuildHash, claudeBuildHashBudget
	release := make(chan struct{})
	exited := make(chan struct{})
	claudeBuildHash = func(context.Context, io.Reader) (string, error) {
		defer close(exited)
		<-release // blocks well past the budget and ignores its context
		return "", nil
	}
	claudeBuildHashBudget = 50 * time.Millisecond
	t.Cleanup(func() {
		close(release)
		<-exited // the leaked call must finish before the seam is restored
		claudeBuildHash, claudeBuildHashBudget = realHash, realBudget
	})

	begin := time.Now()
	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > 500*time.Millisecond {
		t.Fatalf("observation took %s, want it bounded well under 500ms", elapsed)
	}
	if ev.Reason != "claude build observation timed out" || ev.Build != "" || ev.SHA256 != "" {
		t.Fatalf("unbounded hash did not time out cleanly: %+v", ev)
	}

	// A bounded observation caches nothing.
	if n := claudeCacheEntries(t, f); n != 0 {
		t.Fatalf("timed-out observation cached %d entries", n)
	}
}

func TestClaudeBuildUnreadableExeIsUnqualified(t *testing.T) {
	f := newClaudeBuildFixture(t)
	realOpen := claudeBuildOpenExe
	claudeBuildOpenExe = func(int) (*os.File, error) { return nil, unix.EACCES }
	t.Cleanup(func() { claudeBuildOpenExe = realOpen })

	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Reason != "claude executable is unreadable" || ev.Build != "" || ev.SHA256 != "" {
		t.Fatalf("unreadable exe is not a stable refusal: %+v", ev)
	}
}

func TestClaudeBuildStalePidErrors(t *testing.T) {
	f := newClaudeBuildFixture(t)
	if ev, err := f.app().observeClaudeBuild(context.Background(), f.pid, f.start+"0"); err == nil {
		t.Fatalf("a mismatched start identity was not an error: %+v", ev)
	}
}

func TestClaudeExeIdentityLive(t *testing.T) {
	f := newClaudeBuildFixture(t)
	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if !claudeExeIdentityLive(f.pid, ev.Identity) {
		t.Fatal("the live helper's identity did not match")
	}
	f.kill()
	if claudeExeIdentityLive(f.pid, ev.Identity) {
		t.Fatal("identity matched a dead pid")
	}
}

// TestClaudeBuildIdentityChangedDuringHash pins the post-hash fstat guard:
// the opened exe's identity changing under the hash (an updater touched the
// file mid-read) leaves no stable read, so nothing is attested or cached.
func TestClaudeBuildIdentityChangedDuringHash(t *testing.T) {
	f := newClaudeBuildFixture(t)
	realHash := claudeBuildHash
	claudeBuildHash = func(ctx context.Context, r io.Reader) (string, error) {
		touched := time.Now().Add(time.Minute)
		if err := os.Chtimes(f.exe, touched, touched); err != nil {
			return "", err
		}
		return realHash(ctx, r)
	}
	t.Cleanup(func() { claudeBuildHash = realHash })

	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Reason != "claude executable is unreadable" || ev.Build != "" || ev.SHA256 != "" || ev.Identity != "" {
		t.Fatalf("identity change under the hash is not a bare refusal: %+v", ev)
	}
	if n := claudeCacheEntries(t, f); n != 0 {
		t.Fatalf("raced observation cached %d entries", n)
	}
}

// TestClaudeBuildStalePidAfterHash pins the post-hash start-identity guard
// on both paths: a pid that dies during the hash errors, and a dead pid
// resolved from the cache errors too rather than reading as unreadable.
func TestClaudeBuildStalePidAfterHash(t *testing.T) {
	// The pid dies while the hash is in flight (hashed path).
	f := newClaudeBuildFixture(t)
	realHash := claudeBuildHash
	claudeBuildHash = func(ctx context.Context, r io.Reader) (string, error) {
		f.kill() // kills and reaps, so /proc/<pid> is gone
		return realHash(ctx, r)
	}
	t.Cleanup(func() { claudeBuildHash = realHash })
	ev, err := f.observe()
	if err == nil {
		t.Fatalf("a pid that died during the hash was not an error: %+v", ev)
	}
	if ev.SHA256 != "" || ev.Identity != "" || ev.Build != "" {
		t.Fatalf("stale-pid error carries evidence fields: %+v", ev)
	}
	if n := claudeCacheEntries(t, f); n != 0 {
		t.Fatalf("stale-pid observation cached %d entries", n)
	}

	// The pid dies after the open but before the cached checks: the start
	// identity must error there too, not read as an unreadable exe.
	g := newClaudeBuildFixture(t)
	if _, err := g.observe(); err != nil {
		t.Fatal(err)
	}
	realOpen := claudeBuildOpenExe
	claudeBuildOpenExe = func(pid int) (*os.File, error) {
		exe, err := realOpen(pid)
		if err == nil {
			g.kill() // kills and reaps, after the kernel already gave us the fd
		}
		return exe, err
	}
	t.Cleanup(func() { claudeBuildOpenExe = realOpen })
	if ev, err := g.observe(); err == nil {
		t.Fatalf("a cached observation of a dead pid was not an error: %+v", ev)
	}
}

// TestClaudeBuildForeignExeDuringHash pins the hashed-path dev:ino guard:
// a descriptor that is not the pid's current executable (here posed through
// the open seam) is never attested, whatever bytes it hashes to.
func TestClaudeBuildForeignExeDuringHash(t *testing.T) {
	f := newClaudeBuildFixture(t)
	decoy := filepath.Join(f.dir, "decoy-exe")
	if err := os.WriteFile(decoy, []byte("a foreign regular file"), 0o755); err != nil {
		t.Fatal(err)
	}
	realOpen := claudeBuildOpenExe
	claudeBuildOpenExe = func(int) (*os.File, error) { return os.Open(decoy) }
	t.Cleanup(func() { claudeBuildOpenExe = realOpen })

	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Reason != "claude executable is unreadable" || ev.Build != "" || ev.SHA256 != "" || ev.Identity != "" {
		t.Fatalf("a foreign descriptor is not a bare refusal: %+v", ev)
	}
	if n := claudeCacheEntries(t, f); n != 0 {
		t.Fatalf("foreign-descriptor observation cached %d entries", n)
	}
}

// TestClaudeBuildForeignExeFromCache pins the same dev:ino guard on the
// cached path: a digest resolved from the cache still has to name the pid's
// current executable, and no rehash may paper over the mismatch.
func TestClaudeBuildForeignExeFromCache(t *testing.T) {
	f := newClaudeBuildFixture(t)
	decoy := filepath.Join(f.dir, "decoy-exe")
	if err := os.WriteFile(decoy, []byte("a foreign regular file"), 0o755); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(decoy, &st); err != nil {
		t.Fatal(err)
	}
	identity := claudeBuildStatIdentity(&st)
	if err := f.app().claudeExeDigestStore(context.Background(), identity, claudeFileDigest(t, decoy), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	realHash, realOpen := claudeBuildHash, claudeBuildOpenExe
	var calls atomic.Int32
	claudeBuildHash = func(ctx context.Context, r io.Reader) (string, error) {
		calls.Add(1)
		return realHash(ctx, r)
	}
	claudeBuildOpenExe = func(int) (*os.File, error) { return os.Open(decoy) }
	t.Cleanup(func() {
		claudeBuildHash, claudeBuildOpenExe = realHash, realOpen
	})

	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the cached path rehashed %d times", n)
	}
	if ev.Reason != "claude executable is unreadable" || ev.Build != "" || ev.SHA256 != "" || ev.Identity != "" {
		t.Fatalf("a cached foreign descriptor is not a bare refusal: %+v", ev)
	}
	if n := claudeCacheEntries(t, f); n != 1 {
		t.Fatalf("observation left %d cache rows, want only the primed one", n)
	}
}

// TestClaudeBuildReplacedInPlaceFailsClosed pins the ruling for an in-place
// version-file replacement: CLAUDE_CODE_EXECPATH then names a new inode at
// the old path while /proc/<pid>/exe holds the running image, and the
// contradiction keeps the build legacy even when the running image's digest
// is tabled.
func TestClaudeBuildReplacedInPlaceFailsClosed(t *testing.T) {
	d, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(d, "claude-version-copy")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	f := startClaudeBuildFixture(t, copyPath)
	claudeWithQualifiedBuild(t, claudeFileDigest(t, copyPath))

	// The updater replaces the version file in place: same path, new inode.
	if err := os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, []byte("the replacement install"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env["CLAUDE_CODE_EXECPATH"] = copyPath
	ev, err := f.observe()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Reason != "CLAUDE_CODE_EXECPATH names a different executable" || ev.Build != "" {
		t.Fatalf("a replaced-in-place version file did not fail closed: %+v", ev)
	}
}
