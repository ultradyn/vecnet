//go:build linux

package cli

// Task 5 (claude delivery foundation): the live-owner checks of the passive
// status reader. The fixtures hold the real delivery singleton lock and write
// the sidecar exactly as the elected host does, then require the CLI to accept
// only a fresh, exact, live writer. An occupied lease is one more check, never
// proof: the lock is never unlinked to test it.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// claudeStatusHost stands in for the elected delivery host: it holds the
// singleton lock (flock, never unlinked) and owns a live process identity.
type claudeStatusHost struct {
	t     *testing.T
	home  string
	lock  string
	fd    int
	pid   int
	start string
	// version is the sidecar document version written (1 unless a test
	// publishes the v2 native canary marker).
	version int
}

func holdClaudeStatusHost(t *testing.T, home string) *claudeStatusHost {
	t.Helper()
	dir := filepath.Join(home, ".local", "state", "vecnet", "codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, "bindings.json.status.json.lock")
	fd, err := unix.Open(lock, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return &claudeStatusHost{t: t, home: home, lock: lock, fd: fd, pid: os.Getpid(), start: procStart(os.Getpid())}
}

// release drops the singleton lock (a crashed or stopped host).
func (h *claudeStatusHost) release() {
	h.t.Helper()
	if err := unix.Flock(h.fd, unix.LOCK_UN); err != nil {
		h.t.Fatal(err)
	}
}

func (h *claudeStatusHost) sidecarPath() string {
	return filepath.Join(h.home, ".local", "state", "vecnet", "codex", "bindings.json.claude-status.json")
}

// write publishes the sidecar as the host would: atomic, private, fresh.
func (h *claudeStatusHost) write(bindings []claudeStatusBinding, at time.Time, pid int, start, boot string) {
	h.t.Helper()
	version := h.version
	if version == 0 {
		version = 1
	}
	doc := claudeStatusFile{
		Version:   version,
		UpdatedAt: at.UTC(),
		HostPID:   pid,
		HostStart: start,
		BootID:    boot,
		Bindings:  bindings,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		h.t.Fatal(err)
	}
	path := h.sidecarPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		h.t.Fatal(err)
	}
}

func (h *claudeStatusHost) writeLive(bindings []claudeStatusBinding) {
	h.t.Helper()
	h.write(bindings, time.Now(), h.pid, h.start, claudeBootID())
}

// inode proves the lock was never unlinked and recreated by a check.
func (h *claudeStatusHost) inode() [2]uint64 {
	h.t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(h.lock, &st); err != nil {
		h.t.Fatal(err)
	}
	return [2]uint64{st.Dev, st.Ino}
}

func TestClaudeStatusNeverClaimsNativeReady(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	binding := mustReg(t, a.reg(f.scope("c"), a.root("S1"), nil))
	key := claudeBindingKey(f.scope("c"))

	host := holdClaudeStatusHost(t, f.dir)
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered"}})

	env := map[string]string{}
	for k, v := range f.env {
		env[k] = v
	}
	result := claudeStatusJSON(t, env, "--net", "c", "claude", "status", "--json")
	if result.Owner != "registered" || result.Mode != "legacy" {
		t.Fatalf("fresh exact owner is not registered/legacy: %+v", result)
	}
	if result.Generation != binding.Generation {
		t.Fatalf("status generation %q is not the binding generation %q", result.Generation, binding.Generation)
	}
	if result.PendingCount != 0 {
		t.Fatalf("passive status reported pending work: %+v", result)
	}
	// Human output states the same passive facts.
	code, out, errs := claudeStatusRun(t, env, "--net", "c", "claude", "status")
	if code != 0 {
		t.Fatalf("claude status exit %d: %s", code, errs)
	}
	for _, want := range []string{"owner: registered", "mode: legacy", "pending_count: 0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("human status missing %q: %q", want, out)
		}
	}
	lower := strings.ToLower(out + errs)
	for _, banned := range []string{"native", "ready", "deliveries", "resolve", "socket", "credential"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("status output claims %q: %q", banned, out+errs)
		}
	}

	// The Codex v1 status sidecar is a separate document and stays untouched.
	codexStatus := filepath.Join(f.dir, ".local", "state", "vecnet", "codex", "bindings.json.status.json")
	codexBefore := []byte(`{"version":1,"updated_at":"2026-11-19T00:00:00Z","bindings":[]}` + "\n")
	if err := os.WriteFile(codexStatus, codexBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if result := claudeStatusJSON(t, env, "--net", "c", "claude", "status", "--json"); result.Owner != "registered" {
		t.Fatalf("second status read diverged: %+v", result)
	}
	if after, err := os.ReadFile(codexStatus); err != nil || !bytes.Equal(after, codexBefore) {
		t.Fatalf("claude status disturbed the Codex status sidecar: %s %v", after, err)
	}
	if host.sidecarPath() == codexStatus {
		t.Fatal("claude sidecar aliases the codex status path")
	}

	// A recipient with no registration stays legacy with no owner; a sidecar
	// entry reporting an unsupported build is not a live owner either.
	if result := claudeStatusJSON(t, env, "--net", "r", "claude", "status", "--json"); result.Owner != "none" || result.Mode != "legacy" {
		t.Fatalf("missing registration is not legacy: %+v", result)
	}
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "unsupported", Reason: "unqualified platform"}})
	unsupported := claudeStatusJSON(t, env, "--net", "c", "claude", "status", "--json")
	if unsupported.Owner != "none" || unsupported.Mode != "legacy" {
		t.Fatalf("unsupported build claimed a live owner: %+v", unsupported)
	}

	// No sidecar shape ever yields a native mode: a v2 row carrying the
	// host's canary marker without this recipient's opt-in stays legacy, a
	// v1 row carrying it (or any other native value) is not a live owner,
	// and the only non-legacy live mode is "canary", never "native".
	host.version = 2
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered", Native: "canary"}})
	unopted := claudeStatusJSON(t, env, "--net", "c", "claude", "status", "--json")
	if unopted.Owner != "registered" || unopted.Mode != "legacy" || unopted.CanaryOptIn {
		t.Fatalf("a host canary marker without an opt-in left legacy: %+v", unopted)
	}
	for name, row := range map[string]struct {
		version int
		native  string
	}{"v1 native": {1, "canary"}, "native value": {2, "native"}, "ready value": {2, "ready"}} {
		host.version = row.version
		host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered", Native: row.native}})
		got := claudeStatusJSON(t, env, "--net", "c", "claude", "status", "--json")
		if got.Owner != "none" || got.Mode != "legacy" {
			t.Fatalf("%s: a malformed native marker was accepted: %+v", name, got)
		}
	}
	host.version = 2
	mustCanary(t, f, "c")
	host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered", Native: "canary"}})
	for _, args := range [][]string{{"--json"}, {}} {
		code, out, errs := claudeStatusRun(t, env, append([]string{"--net", "c", "claude", "status"}, args...)...)
		if code != 0 {
			t.Fatalf("claude status exit %d: %s", code, errs)
		}
		if strings.Contains(out, `"mode":"native"`) || strings.Contains(out, "mode: native") || strings.Contains(strings.ToLower(out), "ready") {
			t.Fatalf("status claimed a native-ready mode: %q", out)
		}
	}
	f.assertPassive()
}

// mustCanary records the canary opt-in for profile through the real verb.
func mustCanary(t *testing.T, f *claudeRegFixture, profile string) {
	t.Helper()
	if code, out, errs := claudeStatusRun(t, f.env, "--net", profile, "claude", "canary", "enable"); code != 0 {
		t.Fatalf("claude canary enable exit %d: %s %s", code, out, errs)
	}
}

func TestClaudeStatusRejectsDeadOrMismatchedHost(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	binding := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	key := claudeBindingKey(f.scope("p"))
	live := []claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "registered"}}

	env := map[string]string{}
	for k, v := range f.env {
		env[k] = v
	}
	status := func() claudeStatusResult {
		t.Helper()
		return claudeStatusJSON(t, env, "--net", "p", "claude", "status", "--json")
	}

	// A genuinely dead host process: a real killed child with its own start.
	dead := exec.Command("sleep", "30")
	dead.SysProcAttr = &syscall.SysProcAttr{}
	if err := dead.Start(); err != nil {
		t.Fatal(err)
	}
	deadStart := procStart(dead.Process.Pid)
	if err := dead.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = dead.Wait()

	host := holdClaudeStatusHost(t, f.dir)
	lockInode := host.inode()
	cases := []struct {
		name  string
		setup func()
	}{
		{"no sidecar", func() { _ = os.Remove(host.sidecarPath()) }},
		{"stale heartbeat", func() { host.write(live, time.Now().Add(-31*time.Second), host.pid, host.start, claudeBootID()) }},
		{"wrong start identity", func() { host.write(live, time.Now(), host.pid, "424242", claudeBootID()) }},
		{"wrong boot identity", func() { host.write(live, time.Now(), host.pid, host.start, "01234567-89ab-cdef-0123-456789abcdef") }},
		{"dead host process", func() { host.write(live, time.Now(), dead.Process.Pid, deadStart, claudeBootID()) }},
		{"wrong generation", func() {
			host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: "ffffffffffffffffffffffffffffffff", State: "registered"}})
		}},
		{"other binding key", func() {
			host.writeLive([]claudeStatusBinding{{BindingKey: strings.Repeat("ab", 32), Generation: binding.Generation, State: "registered"}})
		}},
		{"blocked state", func() {
			host.writeLive([]claudeStatusBinding{{BindingKey: key, Generation: binding.Generation, State: "blocked", Reason: "claude process is not live"}})
		}},
		{"future heartbeat", func() { host.write(live, time.Now().Add(2*time.Minute), host.pid, host.start, claudeBootID()) }},
		{"world-readable sidecar", func() {
			host.writeLive(live)
			if err := os.Chmod(host.sidecarPath(), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked sidecar", func() {
			host.writeLive(live)
			if err := os.Rename(host.sidecarPath(), host.sidecarPath()+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(host.sidecarPath()+".real", host.sidecarPath()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		c.setup()
		if result := status(); result.Owner != "none" {
			t.Fatalf("%s: status accepted a dead or mismatched host: %+v", c.name, result)
		}
		if result := status(); result.Owner != "none" {
			t.Fatalf("%s: status accepted a dead or mismatched host twice: %+v", c.name, result)
		}
	}

	// An occupied lease alone proves nothing: fresh file naming a live pid
	// with a matching start, but the singleton is not actually held.
	host.writeLive(live)
	host.release()
	if result := status(); result.Owner != "none" {
		t.Fatalf("unavailable lease claimed a live owner: %+v", result)
	}
	// The lock file was never unlinked by any check.
	if inode := host.inode(); inode != lockInode {
		t.Fatal("the singleton lock was unlinked or replaced during status checks")
	}
	if _, err := os.Lstat(host.lock); err != nil {
		t.Fatalf("lock file removed: %v", err)
	}

	// With the singleton held again the same sidecar is accepted once more.
	host2 := holdClaudeStatusHost(t, f.dir)
	host2.pid = host.pid
	host2.start = host.start
	host2.writeLive(live)
	if result := status(); result.Owner != "registered" || result.Mode != "legacy" {
		t.Fatalf("restored host is not registered/legacy: %+v", result)
	}
	f.assertPassive()
}

// TestClaudeAdmissionIgnoresBuild pins the parked build gating: an unknown
// build (no evidence, or a digest and label the pinned table never had) is
// admitted on liveness and policy alone, and the admission says honestly
// that no build check ran. Liveness and policy still gate.
func TestClaudeAdmissionIgnoresBuild(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	unknown := claudeBinding{claudeScope: f.scope("p"), claudeRoot: a.root("S1")}
	untabled := unknown
	untabled.Build, untabled.ExeSHA256, untabled.ExeIdentity = "9.9.9", strings.Repeat("0", 64), "1:2:3:4:5"
	for label, b := range map[string]claudeBinding{"no build evidence": unknown, "untabled build": untabled} {
		adm := claudeAdmissionEvidence(b, nil, nil, false)
		if !adm.Live || !adm.Policy || !adm.VersionQualified || adm.BuildCheck != "disabled" {
			t.Fatalf("%s was not admitted with build_check disabled: %+v", label, adm)
		}
	}
	raw, err := json.Marshal(claudeAdmissionEvidence(unknown, nil, nil, false))
	if err != nil || string(raw) != `{"live":true,"policy":true,"version_qualified":true,"build_check":"disabled","canary":false,"witness":false}` {
		t.Fatalf("admission JSON = %s, %v", raw, err)
	}
	if adm := claudeAdmissionEvidence(unknown, []claudeScope{f.scope("q")}, nil, false); adm.Policy {
		t.Fatalf("an alias opt-out did not withdraw policy: %+v", adm)
	}
	if adm := claudeAdmissionEvidence(unknown, nil, nil, true); adm.Policy {
		t.Fatalf("the global disable did not withdraw policy: %+v", adm)
	}
	a.kill()
	if adm := claudeAdmissionEvidence(unknown, nil, nil, false); adm.Live {
		t.Fatalf("a dead root stayed live: %+v", adm)
	}
}
