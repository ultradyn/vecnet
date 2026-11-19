//go:build linux

package cli

// Task 3 (claude delivery foundation): passive registration of an exact
// Claude root. Every fixture here runs registration inside a helper
// subprocess that is a real child of the claimed Claude process, so the
// ancestry proof exercised is the kernel's process tree, not a mock. The
// fixtures start a loopback hub counter and a real private socket listener:
// registration must touch neither.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const claudeFixtureVersion = "2.1.322"

type claudeRegResult struct {
	OK      bool          `json:"ok"`
	Binding claudeBinding `json:"binding"`
	Err     string        `json:"error"`
}

type claudeRegFixture struct {
	t      *testing.T
	dir    string
	env    map[string]string
	config string
	hub    string
	hits   *atomic.Int32
	conns  *atomic.Int32
	boot   string
	seq    int
	trees  []*claudeTree
	server *httptest.Server
}

func newClaudeRegFixture(t *testing.T) *claudeRegFixture {
	t.Helper()
	d, err := canonicalCodexPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &claudeRegFixture{t: t, dir: d, hits: &atomic.Int32{}, conns: &atomic.Int32{}, boot: claudeBootID()}
	if f.boot == "" {
		t.Fatal("cannot read this host's boot id")
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(f.server.Close)
	f.hub = f.server.URL
	f.config = filepath.Join(d, "v.toml")
	profiles := map[string]Profile{
		"p": {Hub: f.hub, Secret: "vn_peer_test", PeerID: "peer_A"},
		"q": {Hub: f.hub, Secret: "vn_peer_test_q", PeerID: "peer_A"},
		"r": {Hub: f.hub, Secret: "vn_peer_test_r", PeerID: "peer_B"},
	}
	if err := saveConfig(f.config, Config{Profiles: profiles}); err != nil {
		t.Fatal(err)
	}
	f.env = map[string]string{
		"HOME":            d,
		"VECNET_CONFIG":   f.config,
		"XDG_CONFIG_HOME": filepath.Join(d, "config"),
		"XDG_CACHE_HOME":  filepath.Join(d, "cache"),
	}
	return f
}

// scope builds a scope for profile p (or q/r); caller may mutate the result.
func (f *claudeRegFixture) scope(profile string) claudeScope {
	caller := "peer_A"
	if profile == "r" {
		caller = "peer_B"
	}
	return claudeScope{
		ConfigPath:  f.config,
		Profile:     profile,
		HubIdentity: f.hub,
		HubEndpoint: f.hub,
		Caller:      caller,
		Recipient:   caller,
	}
}

type claudeTree struct {
	f          *claudeRegFixture
	name       string
	pid        int
	start      string
	configRoot string
	socket     string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	jobs       *bufio.Writer
	listener   net.Listener
	dead       bool
}

// startTree launches a helper "Claude process" whose children perform
// registrations. The tree owns a private config root and a real (never
// accepted-from) unix socket.
func (f *claudeRegFixture) startTree(name string) *claudeTree {
	f.t.Helper()
	tr := &claudeTree{f: f, name: name}
	tr.socket = filepath.Join(f.dir, name+".sock")
	ln, err := net.Listen("unix", tr.socket)
	if err != nil {
		f.t.Fatal(err)
	}
	tr.listener = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.conns.Add(1)
			c.Close()
		}
	}()
	tr.configRoot = filepath.Join(f.dir, name+"-root")
	if err := os.MkdirAll(tr.configRoot, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(tr.configRoot, 0o700); err != nil {
		f.t.Fatal(err)
	}
	identity := filepath.Join(f.dir, name+"-identity.json")
	tr.cmd = exec.Command(os.Args[0], "-test.run=^TestClaudeHelperProcess$")
	tr.cmd.Env = append(os.Environ(), "VECNET_CLAUDE_HELPER_ROLE=claude", "VECNET_CLAUDE_IDENTITY="+identity)
	var out, errs bytes.Buffer
	tr.cmd.Stdout, tr.cmd.Stderr = &out, &errs
	stdin, err := tr.cmd.StdinPipe()
	if err != nil {
		f.t.Fatal(err)
	}
	tr.stdin = stdin
	tr.jobs = bufio.NewWriter(stdin)
	if err := tr.cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
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
			f.t.Fatalf("claude helper %s did not start: %s %s", name, out.String(), errs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	tr.pid, tr.start = id.PID, id.Start
	if tr.start == "" {
		f.t.Fatal("helper did not report its start identity")
	}
	f.t.Cleanup(func() {
		tr.jobs.Flush()
		tr.stdin.Close()
		if !tr.dead {
			_ = tr.cmd.Wait()
		}
		ln.Close()
	})
	f.trees = append(f.trees, tr)
	return tr
}

// sessionRecord writes the Claude session registry row for pid under this
// config root, exactly as Claude Code 2.1.322 does.
func (tr *claudeTree) sessionRecord(pid int, session, socket string) {
	tr.f.t.Helper()
	dir := filepath.Join(tr.configRoot, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		tr.f.t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"sessionId": session, "messagingSocketPath": socket})
	if err != nil {
		tr.f.t.Fatal(err)
	}
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		tr.f.t.Fatal(err)
	}
}

// root is the default claimed root for this tree.
func (tr *claudeTree) root(session string) claudeRoot {
	return claudeRoot{
		ConfigRoot:   tr.configRoot,
		SessionID:    session,
		SocketPath:   tr.socket,
		ProcessStart: tr.start,
		BootID:       tr.f.boot,
		Version:      claudeFixtureVersion,
		PID:          tr.pid,
		UID:          os.Geteuid(),
	}
}

func (tr *claudeTree) kill() {
	tr.f.t.Helper()
	if tr.dead {
		return
	}
	tr.dead = true
	_ = tr.cmd.Process.Kill()
	_, _ = tr.cmd.Process.Wait()
}

// reg runs one registration from a child of this tree.
func (tr *claudeTree) reg(scope claudeScope, root claudeRoot, env map[string]string) claudeRegResult {
	tr.f.t.Helper()
	tr.f.seq++
	base := filepath.Join(tr.f.dir, fmt.Sprintf("job-%s-%d", tr.name, tr.f.seq))
	payload := base + "-payload.json"
	result := base + "-result.json"
	if env == nil {
		env = map[string]string{}
	}
	merged := map[string]string{}
	for k, v := range tr.f.env {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}
	raw, err := json.Marshal(struct {
		Env   map[string]string `json:"env"`
		Scope claudeScope       `json:"scope"`
		Root  claudeRoot        `json:"root"`
	}{merged, scope, root})
	if err != nil {
		tr.f.t.Fatal(err)
	}
	if err := os.WriteFile(payload, raw, 0o600); err != nil {
		tr.f.t.Fatal(err)
	}
	if _, err := tr.jobs.WriteString(payload + " " + result + "\n"); err != nil {
		tr.f.t.Fatal(err)
	}
	if err := tr.jobs.Flush(); err != nil {
		tr.f.t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(result)
		if err == nil {
			var r claudeRegResult
			if json.Unmarshal(raw, &r) == nil {
				_ = os.Remove(result)
				return r
			}
		}
		if time.Now().After(deadline) {
			tr.f.t.Fatalf("registration job on tree %s produced no result", tr.name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *claudeRegFixture) registryPath() string {
	return filepath.Join(f.dir, ".local", "state", "vecnet", "projects", "claude-bindings.json")
}

func (f *claudeRegFixture) registryBytes() []byte {
	f.t.Helper()
	raw, err := os.ReadFile(f.registryPath())
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func (f *claudeRegFixture) app() *app {
	return &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return f.env[k] }}
}

// mustReg asserts the registration succeeded.
func mustReg(t *testing.T, r claudeRegResult) claudeBinding {
	t.Helper()
	if !r.OK {
		t.Fatalf("registration refused: %s", r.Err)
	}
	if r.Binding.Generation == "" {
		t.Fatal("registered binding has no generation")
	}
	return r.Binding
}

// refuseReg asserts the registration was refused and reports why.
func refuseReg(t *testing.T, r claudeRegResult, want string) {
	t.Helper()
	if r.OK {
		t.Fatalf("registration unexpectedly succeeded (generation %s)", r.Binding.Generation)
	}
	if !strings.Contains(r.Err, want) {
		t.Fatalf("registration error %q does not contain %q", r.Err, want)
	}
}

// assertPassive: no hub request and no socket connect happened.
func (f *claudeRegFixture) assertPassive() {
	f.t.Helper()
	if n := f.hits.Load(); n != 0 {
		f.t.Fatalf("%d hub request(s) during registration", n)
	}
	if n := f.conns.Load(); n != 0 {
		f.t.Fatalf("%d native socket connect(s) during registration", n)
	}
}

// storedGeneration returns the stored generation for the one binding row.
func (f *claudeRegFixture) storedRows(t *testing.T) []claudeBinding {
	t.Helper()
	rows, err := f.app().readClaudeBindings()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestClaudeRegistrationSameRootIsIdempotent(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	first := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	before := f.registryBytes()

	same := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	if same.Generation != first.Generation {
		t.Fatalf("idempotent registration changed generation: %s vs %s", same.Generation, first.Generation)
	}
	if after := f.registryBytes(); !bytes.Equal(before, after) {
		t.Fatal("idempotent registration rewrote the registry")
	}

	// A profile alias for the same canonical recipient re-registering the
	// same root stays idempotent: aliases cannot buy a second row.
	alias := mustReg(t, a.reg(f.scope("q"), a.root("S1"), nil))
	if alias.Generation != first.Generation {
		t.Fatalf("alias registration changed generation: %s vs %s", alias.Generation, first.Generation)
	}
	if after := f.registryBytes(); !bytes.Equal(before, after) {
		t.Fatal("alias registration rewrote the registry")
	}
	if rows := f.storedRows(t); len(rows) != 1 {
		t.Fatalf("expected one binding row, got %d", len(rows))
	}
	f.assertPassive()
}

func TestClaudeRegistrationNeverStealsAcrossProfilesOrRoots(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	first := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	base := f.registryBytes()

	// A second live root for the same (hub identity, recipient) through a
	// different profile is refused and leaves the first generation.
	b := f.startTree("b")
	b.sessionRecord(b.pid, "S2", b.socket)
	refuseReg(t, b.reg(f.scope("q"), b.root("S2"), nil), "already")
	refuseReg(t, b.reg(f.scope("p"), b.root("S2"), nil), "already")

	// A separate config root with an alias profile is still the same
	// canonical recipient: refusal, no second row.
	aliasConfig := filepath.Join(f.dir, "v2.toml")
	if err := saveConfig(aliasConfig, Config{Profiles: map[string]Profile{
		"p2": {Hub: f.hub, Secret: "vn_peer_test_2", PeerID: "peer_A"},
	}}); err != nil {
		t.Fatal(err)
	}
	aliasScope := f.scope("p2")
	aliasScope.ConfigPath, aliasScope.Profile = aliasConfig, "p2"
	refuseReg(t, b.reg(aliasScope, b.root("S2"), map[string]string{"VECNET_CONFIG": aliasConfig}), "already")
	if after := f.registryBytes(); !bytes.Equal(base, after) {
		t.Fatal("refused registration changed the registry")
	}
	if rows := f.storedRows(t); len(rows) != 1 || rows[0].Generation != first.Generation {
		t.Fatalf("first binding was disturbed: %+v", rows)
	}

	// A genuinely different recipient registers its own exact root.
	other := mustReg(t, b.reg(f.scope("r"), b.root("S2"), nil))
	if other.Generation == first.Generation {
		t.Fatal("different recipient reused the first generation")
	}
	if rows := f.storedRows(t); len(rows) != 2 {
		t.Fatalf("expected two binding rows, got %d", len(rows))
	}
	f.assertPassive()
}

func TestClaudeRegistrationFencesResumeAndClear(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	first := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))

	b := f.startTree("b")
	// Resume keeps the conversation and config root but has a fresh process
	// and endpoint: Claude's session row for the new pid appears there.
	resume := claudeRoot{ConfigRoot: a.configRoot, SessionID: "S1", SocketPath: b.socket, ProcessStart: b.start, BootID: f.boot, Version: claudeFixtureVersion, PID: b.pid, UID: os.Geteuid()}
	a.sessionRecord(b.pid, "S1", b.socket)

	// The old process is still live: the resume is fenced and refused.
	refuseReg(t, b.reg(f.scope("p"), resume, nil), "fenced")
	state := f.registryBytes()

	a.kill()
	// A claim for the dead process is a stale observation and changes nothing.
	refuseReg(t, b.reg(f.scope("p"), a.root("S1"), nil), "")

	second := mustReg(t, b.reg(f.scope("p"), resume, nil))
	if second.Generation == first.Generation {
		t.Fatal("approved resume kept the old generation")
	}
	if rows := f.storedRows(t); len(rows) != 1 || rows[0].PID != b.pid {
		t.Fatalf("resume did not replace the binding: %+v", rows)
	}

	// /clear: same live process, new conversation id recorded by Claude.
	clear := resume
	clear.SessionID = "S2"
	a.sessionRecord(b.pid, "S2", b.socket)
	third := mustReg(t, b.reg(f.scope("p"), clear, nil))
	if third.Generation == second.Generation {
		t.Fatal("verified clear kept the old generation")
	}
	state = f.registryBytes()

	// Unknown transitions stay pending: same conversation and process but a
	// different endpoint is neither resume nor clear.
	unknown := clear
	unknown.SocketPath = filepath.Join(f.dir, "other.sock")
	if ln, err := net.Listen("unix", unknown.SocketPath); err == nil {
		t.Cleanup(func() { ln.Close() })
	} else {
		t.Fatal(err)
	}
	a.sessionRecord(b.pid, "S2", unknown.SocketPath)
	refuseReg(t, b.reg(f.scope("p"), unknown, nil), "pending")

	// A stale clear observation (registry still reports S2) cannot register S3.
	stale := clear
	stale.SessionID = "S3"
	refuseReg(t, b.reg(f.scope("p"), stale, nil), "session")

	// The retired conversation id is no longer registrable either.
	retired := clear
	retired.SessionID = "S1"
	refuseReg(t, b.reg(f.scope("p"), retired, nil), "session")

	if after := f.registryBytes(); !bytes.Equal(state, after) {
		t.Fatal("refused transitions changed the registry")
	}
	f.assertPassive()
}

func TestClaudeRegistrationRejectsUnsafeRegistryAndPIDReuse(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	base := f.registryBytes()
	checkUnchanged := func(label string, r claudeRegResult, want string) {
		t.Helper()
		refuseReg(t, r, want)
		if after := f.registryBytes(); !bytes.Equal(after, base) {
			t.Fatalf("%s: refused registration rewrote the registry", label)
		}
	}

	// Version and schema are strict: unknown fields, missing fields and a
	// wrong version are all refused without repairing the file.
	for label, doc := range map[string]string{
		"wrong version": `{"version":2,"bindings":[],"opt_outs":[]}`,
		"unknown field": `{"version":1,"bindings":[{"config_path":"/x","profile":"p","hub_identity":"h","hub_endpoint":"h","caller":"c","recipient":"c","config_root":"/x","session_id":"S1","process_start":"12","boot_id":"01234567-89ab-cdef-0123-456789abcdef","version":"v","pid":9,"uid":1000,"generation":"0123456789abcdef0123456789abcdef","updated_at":"2026-11-19T00:00:00Z","mystery":1}],"opt_outs":[]}`,
		"missing field": `{"version":1,"bindings":[{"config_path":"/x","profile":"p","hub_identity":"h","hub_endpoint":"h","caller":"c","recipient":"c","config_root":"/x","session_id":"S1","process_start":"12","version":"v","pid":9,"uid":1000,"generation":"0123456789abcdef0123456789abcdef","updated_at":"2026-11-19T00:00:00Z"}],"opt_outs":[]}`,
		"trailing data": string(base[:len(base)-1]) + "\n{}\n",
		"duplicate key": `{"version":1,"version":1,"bindings":[],"opt_outs":[]}`,
	} {
		if err := os.WriteFile(f.registryPath(), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		r := a.reg(f.scope("p"), a.root("S1"), nil)
		if r.OK {
			t.Fatalf("%s: registration accepted an unsafe registry", label)
		}
		if after := f.registryBytes(); !bytes.Equal(after, []byte(doc)) {
			t.Fatalf("%s: refused registration rewrote the registry", label)
		}
	}
	if err := os.WriteFile(f.registryPath(), base, 0o600); err != nil {
		t.Fatal(err)
	}

	// Unsafe permissions refuse every use until made private again.
	if err := os.Chmod(f.registryPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	checkUnchanged("group-readable registry", a.reg(f.scope("p"), a.root("S1"), nil), "unsafe")
	if err := os.Chmod(f.registryPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := a.reg(f.scope("p"), a.root("S1"), nil); !r.OK {
		t.Fatalf("private registry still refused: %s", r.Err)
	}

	// A symlinked registry document is never followed.
	if err := os.Rename(f.registryPath(), f.registryPath()+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.registryPath()+".real", f.registryPath()); err != nil {
		t.Fatal(err)
	}
	refuseReg(t, a.reg(f.scope("p"), a.root("S1"), nil), "")
	if err := os.Remove(f.registryPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.registryPath()+".real", f.registryPath()); err != nil {
		t.Fatal(err)
	}

	// PID reuse: the same pid with a different start identity is a
	// different process, never a continuation.
	reused := a.root("S1")
	reused.ProcessStart = "424242"
	checkUnchanged("pid reuse start mismatch", a.reg(f.scope("p"), reused, nil), "start")

	// The claimed process must belong to this user.
	otherUID := a.root("S1")
	otherUID.UID++
	checkUnchanged("another uid", a.reg(f.scope("p"), otherUID, nil), "user")

	// A child agent (hook payload agent_id) never registers its parent
	// session, and a contradicting CLAUDE_PID is never accepted as proof.
	checkUnchanged("child agent", a.reg(f.scope("p"), a.root("S1"), map[string]string{"CLAUDE_AGENT_ID": "agent_7"}), "child agent")
	checkUnchanged("contradicting pid", a.reg(f.scope("p"), a.root("S1"), map[string]string{"CLAUDE_PID": strconv.Itoa(os.Getpid())}), "contradicts")
	checkUnchanged("contradicting socket", a.reg(f.scope("p"), a.root("S1"), map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": filepath.Join(f.dir, "elsewhere.sock")}), "contradicts")

	// The socket must be an owned socket: symlinks and wrong types refuse.
	link := filepath.Join(f.dir, "sock-link")
	if err := os.Symlink(a.socket, link); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(f.dir, "sock-regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for label, path := range map[string]string{"symlinked socket": link, "wrong socket type": regular} {
		bad := a.root("S1")
		bad.SocketPath = path
		a.sessionRecord(a.pid, "S1", path)
		checkUnchanged(label, a.reg(f.scope("p"), bad, nil), "socket")
	}
	a.sessionRecord(a.pid, "S1", a.socket)

	// A symlinked config root is not a trusted root either.
	cfgLink := filepath.Join(f.dir, "cfg-link")
	if err := os.Symlink(a.configRoot, cfgLink); err != nil {
		t.Fatal(err)
	}
	badRoot := a.root("S1")
	badRoot.ConfigRoot = cfgLink
	checkUnchanged("symlinked config root", a.reg(f.scope("p"), badRoot, nil), "root")

	// A dead claimed process is a stale observation: no idempotent renewal,
	// whatever tree observes it.
	c := f.startTree("c")
	a.kill()
	checkUnchanged("dead process", c.reg(f.scope("p"), a.root("S1"), nil), "live")
	if !claudeProcessFenced(a.pid, a.start, f.boot) {
		t.Fatal("dead process was not reported fenced")
	}
	if !claudeProcessFenced(a.pid, "424242", f.boot) {
		t.Fatal("reused pid start was not reported fenced")
	}
	f.assertPassive()
}

func TestClaudeRegistrationOptOutSurvivesRepair(t *testing.T) {
	f := newClaudeRegFixture(t)
	ctx := context.Background()
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	first := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	base := f.registryBytes()

	app := f.app()
	if err := app.setClaudeOptOut(ctx, f.scope("p"), true); err != nil {
		t.Fatal(err)
	}
	// A duplicate opt-out write stays idempotent.
	if err := app.setClaudeOptOut(ctx, f.scope("p"), true); err != nil {
		t.Fatal(err)
	}

	// Registration (including idempotent renewal, "repair") is refused and
	// the durable opt-out plus the stored binding survive.
	refuseReg(t, a.reg(f.scope("p"), a.root("S1"), nil), "opt-out")
	// A profile alias for the same recipient cannot bypass the opt-out.
	refuseReg(t, a.reg(f.scope("q"), a.root("S1"), nil), "opt-out")
	rows := f.storedRows(t)
	if len(rows) != 1 || rows[0].Generation != first.Generation {
		t.Fatalf("opt-out disturbed the stored binding: %+v", rows)
	}
	var doc struct {
		OptOuts []claudeScope `json:"opt_outs"`
	}
	if err := json.Unmarshal(f.registryBytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.OptOuts) != 1 {
		t.Fatalf("expected one durable opt-out row, got %d", len(doc.OptOuts))
	}
	if base2 := f.registryBytes(); bytes.Equal(base, base2) {
		t.Fatal("opt-out was not persisted")
	}

	// Other recipients are unaffected by an addressed opt-out.
	b := f.startTree("b")
	b.sessionRecord(b.pid, "S2", b.socket)
	other := mustReg(t, b.reg(f.scope("r"), b.root("S2"), nil))
	_ = other

	// Only the explicit re-enable clears it; renewal then resumes
	// idempotently with the original generation.
	if err := app.setClaudeOptOut(ctx, f.scope("p"), false); err != nil {
		t.Fatal(err)
	}
	again := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	if again.Generation != first.Generation {
		t.Fatalf("re-enable changed the generation: %s vs %s", again.Generation, first.Generation)
	}
	f.assertPassive()
}

// TestClaudeHelperProcess is re-entered as a subprocess: "claude" is the
// fake Claude process owning claimed roots; "register" is its child that
// performs one registration with an isolated environment.
func TestClaudeHelperProcess(t *testing.T) {
	switch os.Getenv("VECNET_CLAUDE_HELPER_ROLE") {
	case "":
		return
	case "claude":
		claudeHelperClaude()
	case "register":
		claudeHelperRegister()
	default:
		fmt.Fprintln(os.Stderr, "unknown helper role")
	}
	os.Exit(0)
}

func claudeHelperClaude() {
	raw, err := json.Marshal(struct {
		PID   int    `json:"pid"`
		Start string `json:"start"`
	}{os.Getpid(), procStart(os.Getpid())})
	if err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile(os.Getenv("VECNET_CLAUDE_IDENTITY"), raw, 0o600); err != nil {
		os.Exit(1)
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		payload, result, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeHelperProcess$")
		cmd.Env = append(os.Environ(),
			"VECNET_CLAUDE_HELPER_ROLE=register",
			"VECNET_CLAUDE_PAYLOAD="+payload,
			"VECNET_CLAUDE_RESULT="+result)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		_ = cmd.Run()
	}
}

func claudeHelperRegister() {
	result := os.Getenv("VECNET_CLAUDE_RESULT")
	fail := func(msg string) {
		raw, _ := json.Marshal(claudeRegResult{Err: msg})
		_ = os.WriteFile(result, raw, 0o600)
		os.Exit(0)
	}
	raw, err := os.ReadFile(os.Getenv("VECNET_CLAUDE_PAYLOAD"))
	if err != nil {
		fail("payload: " + err.Error())
	}
	var payload struct {
		Env   map[string]string `json:"env"`
		Scope claudeScope       `json:"scope"`
		Root  claudeRoot        `json:"root"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		fail("payload: " + err.Error())
	}
	a := &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return payload.Env[k] }}
	binding, err := a.registerClaudeRoot(context.Background(), payload.Scope, payload.Root)
	out := claudeRegResult{OK: err == nil, Binding: binding}
	if err != nil {
		out.Err = err.Error()
	}
	raw, _ = json.Marshal(out)
	_ = os.WriteFile(result, raw, 0o600)
	os.Exit(0)
}
