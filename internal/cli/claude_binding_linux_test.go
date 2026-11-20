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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"github.com/XertroV/vecnet/internal/hubid"
	"golang.org/x/sys/unix"
)

// claudeTestHubPin lives in claude_setup_test.go so every platform's tests
// can share it.

// claudeSocketDir is a private fixture directory short enough to bind unix
// sockets in. t.TempDir() lives under $TMPDIR, which CI points at a deep
// per-job path, and a socket path there overflows the 108-byte sun_path
// (bind: invalid argument). MkdirTemp creates it owner-only (0700), like
// t.TempDir().
func claudeSocketDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "vn-claude-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(d); err != nil {
			t.Errorf("remove socket fixture dir: %v", err)
		}
	})
	return d
}

// claudeSelfBuild returns the kernel build evidence of this test process
// (its digest and fstat identity, computed directly rather than through the
// bounded observation) and tables the digest under the fixture label, for
// fixtures that register this process itself as the Claude root.
func claudeSelfBuild(t *testing.T) (sha, identity string) {
	t.Helper()
	exe, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer exe.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(exe.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, exe); err != nil {
		t.Fatal(err)
	}
	sha = hex.EncodeToString(h.Sum(nil))
	claudeWithQualifiedBuild(t, sha)
	return sha, claudeBuildStatIdentity(&st)
}

// claudeSelfBuildJSON is the stored-row JSON fragment for claudeSelfBuild.
func claudeSelfBuildJSON(t *testing.T) string {
	t.Helper()
	sha, identity := claudeSelfBuild(t)
	return `"build":` + jsonString(claudeTestBuildLabel) + `,"exe_sha256":` + jsonString(sha) + `,"exe_identity":` + jsonString(identity) + `,`
}

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
	// qualified and blockHash travel to every helper job in its payload: the
	// jobs run in child processes, where the parent's table extensions and
	// seams do not reach. Only this test code ever applies them.
	qualified []string
	blockHash bool
	// runtimeOK stubs the local delivery runtime in reconcile jobs.
	runtimeOK bool
	// hashLog, when set, is a file each helper job appends one line to per
	// claudeBuildHash call, counting hashes across the child processes.
	hashLog string
}

// hashCalls counts the claudeBuildHash calls helper jobs logged.
func (f *claudeRegFixture) hashCalls(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(f.hashLog)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(raw, []byte("\n"))
}

func newClaudeRegFixture(t *testing.T) *claudeRegFixture {
	t.Helper()
	d, err := canonicalCodexPath(claudeSocketDir(t))
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
		"p": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test", PeerID: "peer_A"},
		"q": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_q", PeerID: "peer_A"},
		"r": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_r", PeerID: "peer_B"},
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
		HubIdentity: claudeTestHubPin,
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
		PID:          tr.pid,
		UID:          os.Geteuid(),
	}
}

// observedRoot is root with the build evidence this test process observes
// for the tree's executable (the test binary); Build follows this process's
// table.
func (tr *claudeTree) observedRoot(session string) claudeRoot {
	tr.f.t.Helper()
	ev, err := tr.f.app().observeClaudeBuild(context.Background(), tr.pid, tr.start, claudeBuildMayHash)
	if err != nil || ev.SHA256 == "" || ev.Identity == "" {
		tr.f.t.Fatalf("cannot observe the tree's build: %+v %v", ev, err)
	}
	r := tr.root(session)
	r.Build, r.ExeSHA256, r.ExeIdentity = ev.Build, ev.SHA256, ev.Identity
	return r
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
		Env       map[string]string `json:"env"`
		Scope     claudeScope       `json:"scope"`
		Root      claudeRoot        `json:"root"`
		Qualified []string          `json:"qualified"`
		BlockHash bool              `json:"block_hash"`
		HashLog   string            `json:"hash_log"`
	}{merged, scope, root, tr.f.qualified, tr.f.blockHash, tr.f.hashLog})
	if err != nil {
		tr.f.t.Fatal(err)
	}
	if err := os.WriteFile(payload, raw, 0o600); err != nil {
		tr.f.t.Fatal(err)
	}
	tr.job("register", payload, result)
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

// hook runs one hook invocation from a child of this tree, through the
// real hookRun path, and captures its output.
func (tr *claudeTree) hook(env map[string]string, args []string, stdin string) claudeHookResult {
	tr.f.t.Helper()
	tr.f.seq++
	base := filepath.Join(tr.f.dir, fmt.Sprintf("job-%s-%d", tr.name, tr.f.seq))
	payload := base + "-payload.json"
	result := base + "-result.json"
	merged := map[string]string{}
	for k, v := range tr.f.env {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}
	raw, err := json.Marshal(struct {
		Env       map[string]string `json:"env"`
		Args      []string          `json:"args"`
		Stdin     string            `json:"stdin"`
		Qualified []string          `json:"qualified"`
		BlockHash bool              `json:"block_hash"`
		HashLog   string            `json:"hash_log"`
	}{merged, args, stdin, tr.f.qualified, tr.f.blockHash, tr.f.hashLog})
	if err != nil {
		tr.f.t.Fatal(err)
	}
	if err := os.WriteFile(payload, raw, 0o600); err != nil {
		tr.f.t.Fatal(err)
	}
	tr.job("hook", payload, result)
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(result)
		if err == nil {
			var r claudeHookResult
			if json.Unmarshal(raw, &r) == nil {
				_ = os.Remove(result)
				return r
			}
		}
		if time.Now().After(deadline) {
			tr.f.t.Fatalf("hook job on tree %s produced no result", tr.name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// job submits one helper job line for a child of this tree.
func (tr *claudeTree) job(role, payload, result string) {
	tr.f.t.Helper()
	line, err := json.Marshal(struct {
		Role    string `json:"role"`
		Payload string `json:"payload"`
		Result  string `json:"result"`
	}{role, payload, result})
	if err != nil {
		tr.f.t.Fatal(err)
	}
	if _, err := tr.jobs.WriteString(string(line) + "\n"); err != nil {
		tr.f.t.Fatal(err)
	}
	if err := tr.jobs.Flush(); err != nil {
		tr.f.t.Fatal(err)
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
		"p2": {Hub: f.hub, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_2", PeerID: "peer_A"},
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
	resume := claudeRoot{ConfigRoot: a.configRoot, SessionID: "S1", SocketPath: b.socket, ProcessStart: b.start, BootID: f.boot, PID: b.pid, UID: os.Geteuid()}
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
		"wrong version": `{"version":3,"bindings":[],"opt_outs":[],"canary":[]}`,
		"v2 no canary":  `{"version":2,"bindings":[],"opt_outs":[]}`,
		"v2 bad canary": `{"version":2,"bindings":[],"opt_outs":[],"canary":[{"config_path":"/x","profile":"p","hub_identity":"https://h","hub_endpoint":"https://h","caller":"peer_A","recipient":"peer_A"}]}`,
		"v2 bad digest": `{"version":2,"bindings":[{"config_path":"/x","profile":"p","hub_identity":"` + claudeTestHubPin + `","hub_endpoint":"https://h","caller":"peer_A","recipient":"peer_A","config_root":"/x","session_id":"S1","process_start":"12","boot_id":"01234567-89ab-cdef-0123-456789abcdef","exe_sha256":"ABC","pid":9,"uid":1000,"generation":"0123456789abcdef0123456789abcdef","updated_at":"2026-11-19T00:00:00Z"}],"opt_outs":[],"canary":[]}`,
		"v2 old field":  `{"version":2,"bindings":[{"config_path":"/x","profile":"p","hub_identity":"` + claudeTestHubPin + `","hub_endpoint":"https://h","caller":"peer_A","recipient":"peer_A","config_root":"/x","session_id":"S1","process_start":"12","boot_id":"01234567-89ab-cdef-0123-456789abcdef","version":"2.1.322","pid":9,"uid":1000,"generation":"0123456789abcdef0123456789abcdef","updated_at":"2026-11-19T00:00:00Z"}],"opt_outs":[],"canary":[]}`,
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
// performs one registration with an isolated environment; "hook" is its
// child that runs one real hook invocation.
func TestClaudeHelperProcess(t *testing.T) {
	switch os.Getenv("VECNET_CLAUDE_HELPER_ROLE") {
	case "":
		return
	case "claude":
		claudeHelperClaude()
	case "register":
		claudeHelperRegister(t)
	case "reconcile":
		claudeHelperReconcile(t)
	case "hook":
		claudeHelperHook(t)
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
		var job struct {
			Role    string `json:"role"`
			Payload string `json:"payload"`
			Result  string `json:"result"`
		}
		if json.Unmarshal([]byte(line), &job) != nil || job.Payload == "" || job.Result == "" {
			continue
		}
		if job.Role == "" {
			job.Role = "register"
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeHelperProcess$")
		cmd.Env = append(os.Environ(),
			"VECNET_CLAUDE_HELPER_ROLE="+job.Role,
			"VECNET_CLAUDE_PAYLOAD="+job.Payload,
			"VECNET_CLAUDE_RESULT="+job.Result)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		_ = cmd.Run()
	}
}

// claudeHelperJobSeams applies a job payload's test-only table extension and
// hash seam inside the helper child, restored by t.Cleanup like every other
// table extension. Production binaries carry none of this code.
func claudeHelperJobSeams(t *testing.T, qualified []string, blockHash bool, hashLog string) {
	for _, sha := range qualified {
		claudeWithQualifiedBuild(t, sha)
	}
	real := claudeBuildHash
	t.Cleanup(func() { claudeBuildHash = real })
	claudeBuildHash = func(ctx context.Context, r io.Reader) (string, error) {
		if hashLog != "" {
			if f, err := os.OpenFile(hashLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_, _ = f.WriteString("hash\n")
				_ = f.Close()
			}
		}
		if blockHash {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return real(ctx, r)
	}
}

func claudeHelperRegister(t *testing.T) {
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
		Env       map[string]string `json:"env"`
		Scope     claudeScope       `json:"scope"`
		Root      claudeRoot        `json:"root"`
		Qualified []string          `json:"qualified"`
		BlockHash bool              `json:"block_hash"`
		HashLog   string            `json:"hash_log"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		fail("payload: " + err.Error())
	}
	claudeHelperJobSeams(t, payload.Qualified, payload.BlockHash, payload.HashLog)
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

type claudeHookResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// claudeHelperReconcile runs one enrollment reconciliation with an isolated
// environment, as a real child of the claimed Claude process.
func claudeHelperReconcile(t *testing.T) {
	result := os.Getenv("VECNET_CLAUDE_RESULT")
	fail := func(msg string) {
		raw, _ := json.Marshal(localSetupResult{State: "pending", Reason: msg})
		_ = os.WriteFile(result, raw, 0o600)
		os.Exit(0)
	}
	raw, err := os.ReadFile(os.Getenv("VECNET_CLAUDE_PAYLOAD"))
	if err != nil {
		fail("payload: " + err.Error())
	}
	var payload struct {
		Env       map[string]string `json:"env"`
		Record    claudeEnrollment  `json:"record"`
		Qualified []string          `json:"qualified"`
		BlockHash bool              `json:"block_hash"`
		RuntimeOK bool              `json:"runtime_ok"`
		HashLog   string            `json:"hash_log"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		fail("payload: " + err.Error())
	}
	claudeHelperJobSeams(t, payload.Qualified, payload.BlockHash, payload.HashLog)
	a := &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return payload.Env[k] }}
	if payload.RuntimeOK {
		a.localRuntime = func(context.Context) error { return nil }
	}
	res := a.reconcileClaudeEnrollment(context.Background(), payload.Record)
	out, _ := json.Marshal(res)
	_ = os.WriteFile(result, out, 0o600)
	os.Exit(0)
}

// reconcile runs one enrollment reconciliation from a child of this tree.
func (tr *claudeTree) reconcile(record claudeEnrollment, env map[string]string) localSetupResult {
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
		Env       map[string]string `json:"env"`
		Record    claudeEnrollment  `json:"record"`
		Qualified []string          `json:"qualified"`
		BlockHash bool              `json:"block_hash"`
		RuntimeOK bool              `json:"runtime_ok"`
		HashLog   string            `json:"hash_log"`
	}{merged, record, tr.f.qualified, tr.f.blockHash, tr.f.runtimeOK, tr.f.hashLog})
	if err != nil {
		tr.f.t.Fatal(err)
	}
	if err := os.WriteFile(payload, raw, 0o600); err != nil {
		tr.f.t.Fatal(err)
	}
	tr.job("reconcile", payload, result)
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(result)
		if err == nil {
			var r localSetupResult
			if json.Unmarshal(raw, &r) == nil && r.State != "" {
				_ = os.Remove(result)
				return r
			}
		}
		if time.Now().After(deadline) {
			tr.f.t.Fatalf("reconcile job on tree %s produced no result", tr.name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// claudeHelperHook runs one hook invocation inside the real hook worker,
// with an isolated environment, the job's stdin and captured output.
func claudeHelperHook(t *testing.T) {
	result := os.Getenv("VECNET_CLAUDE_RESULT")
	fail := func(msg string) {
		raw, _ := json.Marshal(claudeHookResult{Stderr: msg})
		_ = os.WriteFile(result, raw, 0o600)
		os.Exit(0)
	}
	raw, err := os.ReadFile(os.Getenv("VECNET_CLAUDE_PAYLOAD"))
	if err != nil {
		fail("payload: " + err.Error())
	}
	var job struct {
		Env       map[string]string `json:"env"`
		Args      []string          `json:"args"`
		Stdin     string            `json:"stdin"`
		Qualified []string          `json:"qualified"`
		BlockHash bool              `json:"block_hash"`
		HashLog   string            `json:"hash_log"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		fail("payload: " + err.Error())
	}
	claudeHelperJobSeams(t, job.Qualified, job.BlockHash, job.HashLog)
	var out, errs bytes.Buffer
	a := &app{ctx: context.Background(), stdin: strings.NewReader(job.Stdin), stdout: &out, stderr: &errs, getenv: func(k string) string { return job.Env[k] }, inHook: true}
	a.hookRun(job.Args, hookNow, func() {}, func(data []byte) error {
		_, e := out.Write(data)
		return e
	}, 0)
	raw, _ = json.Marshal(claudeHookResult{Stdout: out.String(), Stderr: errs.String()})
	_ = os.WriteFile(result, raw, 0o600)
	os.Exit(0)
}

// TestClaudeRegistrationGlobalDisableFailsClosedOnBadSettings covers the
// task 5 review finding: the global-disable refusal in registerClaudeRoot
// must fail closed when the versioned Claude settings are unreadable or
// invalid (as readClaudeStatus already does), never skip the refusal and
// register through an unknown policy state.
func TestClaudeRegistrationGlobalDisableFailsClosedOnBadSettings(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)

	// An unreadable settings document must itself refuse registration.
	os.MkdirAll(filepath.Dir(claudeStatePath(f.dir, claudeSettingsName)), 0o700)
	if err := os.WriteFile(claudeStatePath(f.dir, claudeSettingsName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	refuseReg(t, a.reg(f.scope("p"), a.root("S1"), nil), "invalid claude settings state")
	if n := f.storedBindingCount(t); n != 0 {
		t.Fatalf("registration through an unreadable settings document left %d rows", n)
	}

	// A valid document without the disable flag keeps registration working,
	// proving the fail-closed branch is the settings reader, not a blanket
	// refusal.
	if err := os.WriteFile(claudeStatePath(f.dir, claudeSettingsName), []byte(`{"version":1,"disabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	f.assertPassive()
}

// --- delivery Phase 2, task 2: binding document v2 ---

// hookRegister runs one owned SessionStart hook from this tree (the real
// hook path, which observes the build) and returns the stored row for scope.
func (tr *claudeTree) hookRegister(t *testing.T, profile, session string, extra map[string]string) claudeBinding {
	t.Helper()
	env := tr.claudeHookEnv(session)
	for k, v := range extra {
		env[k] = v
	}
	out := tr.hook(env, []string{"--profile", profile, "--event", "session-start"}, sessionStartPayload(session))
	if out.Stdout != "" || out.Stderr != "" {
		t.Fatalf("session-start emitted output: stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
	for _, b := range tr.f.storedRows(t) {
		if claudeSameRecipient(b.claudeScope, tr.f.scope(profile)) {
			return b
		}
	}
	t.Fatalf("hook for profile %s registered no row", profile)
	return claudeBinding{}
}

// admission evaluates the stored row's admission evidence in this process.
func (f *claudeRegFixture) admission(t *testing.T, b claudeBinding) claudeSnapshotAdmission {
	t.Helper()
	return claudeAdmissionEvidence(b, nil, false)
}

func TestClaudeRegistrationRecordsKernelBuild(t *testing.T) {
	f := newClaudeRegFixture(t)
	digest := claudeFileDigest(t, os.Args[0])

	// Untabled: the row records the attested digest and identity, Build is
	// empty, admission is unqualified, and registration still succeeds.
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	row := a.hookRegister(t, "p", "S1", nil)
	want := a.observedRoot("S1")
	if row.ExeSHA256 != digest || row.ExeIdentity != want.ExeIdentity || row.Build != "" {
		t.Fatalf("untabled row evidence = (%q, %q, %q), want (%q, %q, \"\")", row.Build, row.ExeSHA256, row.ExeIdentity, digest, want.ExeIdentity)
	}
	if adm := f.admission(t, row); !adm.Live || adm.VersionQualified {
		t.Fatalf("untabled build admission = %+v, want live and unqualified", adm)
	}

	// Tabled (in the hook child and here): Build is the table label and the
	// live attested executable qualifies.
	f.qualified = []string{digest}
	claudeWithQualifiedBuild(t, digest)
	b := f.startTree("b")
	b.sessionRecord(b.pid, "S2", b.socket)
	row = b.hookRegister(t, "r", "S2", nil)
	if row.ExeSHA256 != digest || row.Build != claudeTestBuildLabel || row.ExeIdentity == "" {
		t.Fatalf("tabled row evidence = (%q, %q, %q)", row.Build, row.ExeSHA256, row.ExeIdentity)
	}
	if adm := f.admission(t, row); !adm.Live || !adm.VersionQualified {
		t.Fatalf("tabled build admission = %+v, want live and qualified", adm)
	}
	f.assertPassive()
}

// TestClaudeRegistrationIgnoresVersionEnv pins Review Focus 4: a stale
// CLAUDE_CODE_VERSION export never qualifies an untabled build, a
// contradictory one never disqualifies a tabled build, and registration
// needs none.
func TestClaudeRegistrationIgnoresVersionEnv(t *testing.T) {
	f := newClaudeRegFixture(t)
	digest := claudeFileDigest(t, os.Args[0])
	claudeWithQualifiedBuild(t, digest) // this process judges admission

	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	stale := a.hookRegister(t, "p", "S1", map[string]string{"CLAUDE_CODE_VERSION": "2.1.322"})
	if stale.Build != "" || f.admission(t, stale).VersionQualified {
		t.Fatalf("a stale version export qualified an untabled build: build=%q", stale.Build)
	}

	f.qualified = []string{digest}
	b := f.startTree("b")
	b.sessionRecord(b.pid, "S2", b.socket)
	contradict := b.hookRegister(t, "r", "S2", map[string]string{"CLAUDE_CODE_VERSION": "9.9.9"})
	if contradict.Build != claudeTestBuildLabel || !f.admission(t, contradict).VersionQualified {
		t.Fatalf("a contradictory version export disqualified a tabled build: build=%q", contradict.Build)
	}

	// Unset: registration succeeds on kernel evidence alone.
	q := newClaudeRegFixture(t)
	c := q.startTree("c")
	c.sessionRecord(c.pid, "S3", c.socket)
	env := c.claudeHookEnv("S3")
	if _, set := env["CLAUDE_CODE_VERSION"]; set {
		t.Fatal("the hook fixture still exports CLAUDE_CODE_VERSION")
	}
	if row := c.hookRegister(t, "p", "S3", nil); row.Generation == "" || row.ExeSHA256 != digest {
		t.Fatalf("registration without the version export failed: %+v", row)
	}
	f.assertPassive()
	q.assertPassive()
}

func TestClaudeRegistrationRenewsBuildWithoutNewGeneration(t *testing.T) {
	f := newClaudeRegFixture(t)
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	first := mustReg(t, a.reg(f.scope("p"), a.root("S1"), nil))
	base := f.registryBytes()

	// Same process and session with fresh build evidence: renewed in place.
	claudeWithQualifiedBuild(t, claudeFileDigest(t, os.Args[0]))
	observed := a.observedRoot("S1")
	renewed := mustReg(t, a.reg(f.scope("p"), observed, nil))
	if renewed.Generation != first.Generation {
		t.Fatalf("build renewal minted a new generation: %s vs %s", renewed.Generation, first.Generation)
	}
	if bytes.Equal(base, f.registryBytes()) {
		t.Fatal("build renewal did not rewrite the row")
	}
	rows := f.storedRows(t)
	if len(rows) != 1 || rows[0].Generation != first.Generation || rows[0].Build != claudeTestBuildLabel ||
		rows[0].ExeSHA256 != observed.ExeSHA256 || rows[0].ExeIdentity != observed.ExeIdentity {
		t.Fatalf("renewed row = %+v", rows)
	}

	// The same evidence again is idempotent.
	renewedBytes := f.registryBytes()
	if again := mustReg(t, a.reg(f.scope("p"), observed, nil)); again.Generation != first.Generation {
		t.Fatal("idempotent renewal changed the generation")
	}
	if !bytes.Equal(renewedBytes, f.registryBytes()) {
		t.Fatal("idempotent renewal rewrote the registry")
	}

	// Under the lock the attested executable must still be the pid's live
	// image: an identity from another inode is refused, the row unchanged.
	stale := observed
	stale.ExeIdentity = "1:2:3:4:5"
	refuseReg(t, a.reg(f.scope("p"), stale, nil), "executable changed")
	if !bytes.Equal(renewedBytes, f.registryBytes()) {
		t.Fatal("a stale executable identity rewrote the registry")
	}
	f.assertPassive()
}

// TestClaudeRegistrationHubAliasesShareOwner pins Review Focus 5: one hub
// reached through two URL aliases is one recipient scope.
func TestClaudeRegistrationHubAliasesShareOwner(t *testing.T) {
	f := newClaudeRegFixture(t)
	viaIP := f.hub
	viaName := strings.Replace(f.hub, "127.0.0.1", "localhost", 1)
	if viaIP == viaName {
		t.Fatalf("fixture hub %s is not a 127.0.0.1 URL", f.hub)
	}
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{
		"a": {Hub: viaIP, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_a", PeerID: "peer_A"},
		"b": {Hub: viaName, HubPubkey: claudeTestHubPin, Secret: "vn_peer_test_b", PeerID: "peer_A"},
	}}); err != nil {
		t.Fatal(err)
	}
	app := f.app()
	scopeA, err := app.claudeScopeForProfile("a")
	if err != nil {
		t.Fatal(err)
	}
	scopeB, err := app.claudeScopeForProfile("b")
	if err != nil {
		t.Fatal(err)
	}
	if scopeA.HubEndpoint == scopeB.HubEndpoint || scopeA.HubIdentity != claudeTestHubPin || scopeB.HubIdentity != claudeTestHubPin {
		t.Fatalf("alias scopes are not one pinned hub through two endpoints: %+v %+v", scopeA, scopeB)
	}
	if claudeBindingKey(scopeA) != claudeBindingKey(scopeB) {
		t.Fatal("two URL aliases of one pinned hub gave two binding keys")
	}

	first := f.startTree("first")
	first.sessionRecord(first.pid, "S1", first.socket)
	owner := mustReg(t, first.reg(scopeA, first.root("S1"), nil))
	base := f.registryBytes()
	second := f.startTree("second")
	second.sessionRecord(second.pid, "S2", second.socket)
	refuseReg(t, second.reg(scopeB, second.root("S2"), nil), "cannot be stolen")
	if !bytes.Equal(base, f.registryBytes()) {
		t.Fatal("the refused alias root rewrote the registry")
	}
	if rows := f.storedRows(t); len(rows) != 1 || rows[0].Generation != owner.Generation || rows[0].PID != first.pid {
		t.Fatalf("the alias disturbed the owner: %+v", rows)
	}
	f.assertPassive()
}

func TestClaudeRegistrationNeedsPinnedHub(t *testing.T) {
	f := newClaudeRegFixture(t)
	if err := saveConfig(f.config, Config{Profiles: map[string]Profile{
		"u": {Hub: f.hub, Secret: "vn_peer_test_u", PeerID: "peer_A"},
	}}); err != nil {
		t.Fatal(err)
	}
	app := f.app()
	const reason = "claude registration needs a pinned hub identity; run a vecnet command for this profile once"
	if _, err := app.claudeScopeForProfile("u"); err == nil || err.Error() != reason {
		t.Fatalf("unpinned scope derivation = %v, want the fixed reason", err)
	}
	// The endpoint form is never accepted as a hub identity.
	legacy, err := app.claudeScopeForProfileAllowLegacy("u")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.HubIdentity != legacy.HubEndpoint {
		t.Fatalf("unpinned legacy scope = %+v", legacy)
	}
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	if r := a.reg(legacy, a.root("S1"), nil); r.OK || r.Err != reason {
		t.Fatalf("unpinned registration = %+v, want refusal with the fixed reason", r)
	}
	// The real hook path stays silent and registers nothing.
	a.hook(a.claudeHookEnv("S1"), []string{"--profile", "u", "--event", "session-start"}, sessionStartPayload("S1"))
	if n := f.storedBindingCount(t); n != 0 {
		t.Fatalf("an unpinned profile registered %d rows", n)
	}
	// Status reports legacy with the same fixed reason.
	if status := claudeStatusJSON(t, f.env, "--net", "u", "claude", "status", "--json"); status.Mode != "legacy" || status.Owner != "none" || status.Reason != reason {
		t.Fatalf("unpinned status = %+v", status)
	}

	// A TOFU pin (the sidecar a first ordinary command writes) is enough.
	hub, err := hubid.NormalizeURL(f.hub)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.updatePins(func(m map[string]pinRecord) error {
		m["u"] = pinRecord{Hub: hub, Pubkey: claudeTestHubPin, PinnedAt: time.Now().UTC().Format(time.RFC3339Nano), How: "tofu"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pinned, err := app.claudeScopeForProfile("u")
	if err != nil || pinned.HubIdentity != claudeTestHubPin {
		t.Fatalf("TOFU-pinned scope = %+v, %v", pinned, err)
	}
	mustReg(t, a.reg(pinned, a.root("S1"), nil))
	f.assertPassive()
}

func TestClaudeBindingsV1Migrates(t *testing.T) {
	f := newClaudeRegFixture(t)
	endpoint := normalizeHub(f.hub)
	// A slice-3 v1 document: one endpoint-keyed binding with a version
	// string, and one opt-out for peer_A in the endpoint form.
	v1 := `{"version":1,"bindings":[{"config_path":` + jsonString(f.config) + `,"profile":"p","hub_identity":` + jsonString(endpoint) +
		`,"hub_endpoint":` + jsonString(endpoint) + `,"caller":"peer_B","recipient":"peer_B","config_root":"/x","session_id":"S9",` +
		`"process_start":"12","boot_id":"01234567-89ab-cdef-0123-456789abcdef","version":"2.1.322","pid":9,"uid":1000,` +
		`"generation":"0123456789abcdef0123456789abcdef","updated_at":"2026-11-19T00:00:00Z"}],` +
		`"opt_outs":[{"config_path":` + jsonString(f.config) + `,"profile":"p","hub_identity":` + jsonString(endpoint) +
		`,"hub_endpoint":` + jsonString(endpoint) + `,"caller":"peer_A","recipient":"peer_A"}]}`
	claudeSeedLocalState(t, f.env, claudeBindingsName, []byte(v1))

	if rows := f.storedRows(t); len(rows) != 0 {
		t.Fatalf("v1 bindings were not discarded: %+v", rows)
	}
	// The kept opt-out blocks the pinned form (registration, and a profile
	// alias of the same recipient) and the legacy endpoint form (status).
	a := f.startTree("a")
	a.sessionRecord(a.pid, "S1", a.socket)
	refuseReg(t, a.reg(f.scope("p"), a.root("S1"), nil), "opt-out")
	refuseReg(t, a.reg(f.scope("q"), a.root("S1"), nil), "opt-out")
	legacy := f.scope("p")
	legacy.HubIdentity = legacy.HubEndpoint
	for label, scope := range map[string]claudeScope{"pinned": f.scope("p"), "legacy": legacy} {
		status, err := f.app().readClaudeStatus(context.Background(), scope)
		if err != nil || status.Mode != "opted_out" {
			t.Fatalf("%s form is not opted out after migration: %+v %v", label, status, err)
		}
	}
	if !bytes.Equal(f.registryBytes(), []byte(v1)) {
		t.Fatal("a refused registration rewrote the v1 document")
	}

	// The next write emits v2 with the opt-out kept and no canary record.
	b := f.startTree("b")
	b.sessionRecord(b.pid, "S2", b.socket)
	mustReg(t, b.reg(f.scope("r"), b.root("S2"), nil))
	var doc struct {
		Version  int               `json:"version"`
		Bindings []json.RawMessage `json:"bindings"`
		OptOuts  []claudeScope     `json:"opt_outs"`
		Canary   []claudeScope     `json:"canary"`
	}
	if err := json.Unmarshal(f.registryBytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 2 || len(doc.Bindings) != 1 || len(doc.OptOuts) != 1 || doc.OptOuts[0].Recipient != "peer_A" ||
		doc.Canary == nil || len(doc.Canary) != 0 {
		t.Fatalf("the rewrite is not v2 with the opt-out kept: %s", f.registryBytes())
	}
	if bytes.Contains(f.registryBytes(), []byte(`"version":"2.1.322"`)) || bytes.Contains(f.registryBytes(), []byte(`"S9"`)) {
		t.Fatalf("the rewrite kept a v1 binding: %s", f.registryBytes())
	}
	refuseReg(t, a.reg(f.scope("p"), a.root("S1"), nil), "opt-out")
	f.assertPassive()
}
