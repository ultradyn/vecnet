package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/platform"
)

const piMarker = "// vecnet-managed: written by `vecnet hook install --client pi`; rerun the installer to change it."

// piInstallRepo returns an install user chdir'ed into a disposable git
// repository, with pi's global directory isolated.
func piInstallRepo(t *testing.T) (*user, string) {
	t.Helper()
	isolateGitEnv(t)
	u := installUser(t)
	u.env["PI_CODING_AGENT_DIR"] = filepath.Join(filepath.Dir(u.conf), "pi-agent")
	root := filepath.Join(t.TempDir(), "pi repo")
	gitT(t, "init", "--quiet", root)
	root, err := platform.Canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return u, root
}

// piConfigOf extracts the VECNET object literal from an installed extension.
func piConfigOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	s := string(body)
	if !strings.HasPrefix(s, piMarker+"\n") {
		t.Fatalf("missing marker: %q", s[:min(len(s), 120)])
	}
	i := strings.Index(s, "const VECNET = ")
	j := strings.Index(s[i:], ";\n")
	if i < 0 || j < 0 {
		t.Fatalf("no VECNET config: %s", s)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(s[i+len("const VECNET = "):i+j]), &cfg); err != nil {
		t.Fatalf("VECNET config: %v", err)
	}
	return cfg
}

// The default target is <cwd>/.pi/extensions/vecnet-P.ts — pi loads project
// extensions only from its start directory — even from a subdirectory of
// the repository.
func TestHookInstallPiDefaultTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	u, root := piInstallRepo(t)
	sub := filepath.Join(root, "sub")
	if err := cliFixtureMkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	out, errs := installRun(t, u, "--client", "pi", "--profile", "p", "--write")
	if out != "" {
		t.Fatalf("write stdout=%q", out)
	}
	path := filepath.Join(sub, ".pi", "extensions", "vecnet-p.ts")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("extension not written: %v (stderr=%q)", err, errs)
	}
	cfg := piConfigOf(t, body)
	args, _ := json.Marshal(cfg["args"])
	if cfg["bin"] != "vecnet" || string(args) != `["hook","--client","pi","--profile","p"]` {
		t.Fatalf("config=%v", cfg)
	}
	env, _ := cfg["env"].(map[string]any)
	if conf, _ := env["VECNET_CONFIG"].(string); !filepath.IsAbs(conf) {
		t.Fatalf("VECNET_CONFIG not baked absolute: %v", cfg["env"])
	}
	for _, want := range []string{"wrote " + path, "pi: project extensions load only from the directory pi starts in", "/reload"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("stderr lacks %q: %q", want, errs)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".pi")); !os.IsNotExist(err) {
		t.Fatal("wrote to the repository root instead of the cwd")
	}
	// Idempotent.
	_, errs = installRun(t, u, "--client", "pi", "--profile", "p", "--write")
	if !strings.Contains(errs, "unchanged "+path) {
		t.Fatalf("rerun stderr=%q", errs)
	}
}

// Without --write (or with --dry-run) the installer prints a diff and
// writes nothing.
func TestHookInstallPiDryRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, args := range [][]string{nil, {"--dry-run"}} {
		u, root := piInstallRepo(t)
		out, errs := installRun(t, u, append([]string{"--client", "pi", "--profile", "p"}, args...)...)
		if !strings.Contains(out, "+"+piMarker) || !strings.Contains(errs, "rerun with --write") {
			t.Fatalf("dry run out=%q errs=%q", out, errs)
		}
		if _, err := os.Stat(filepath.Join(root, ".pi")); !os.IsNotExist(err) {
			t.Fatal("dry run wrote")
		}
	}
}

// A cwd reached through a symlink still installs into the repository; a
// symlinked .pi is refused without --settings.
func TestHookInstallPiSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	u, root := piInstallRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	installRun(t, u, "--client", "pi", "--profile", "p", "--write")
	if _, err := os.Stat(filepath.Join(root, ".pi", "extensions", "vecnet-p.ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".pi")); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(root, ".pi")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	code, _, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "pi", "--profile", "p", "--write")
	if code == 0 || !strings.Contains(errs, "is a symlink") {
		t.Fatalf("symlinked .pi: code=%d errs=%q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(shared, "extensions")); !os.IsNotExist(err) {
		t.Fatal("wrote through a symlinked .pi")
	}
}

// --global writes into PI_CODING_AGENT_DIR/extensions; --settings takes a
// file or an existing directory.
func TestHookInstallPiGlobalAndSettings(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	u, _ := piInstallRepo(t)
	_, errs := installRun(t, u, "--client", "pi", "--profile", "p", "--global", "--write")
	global := filepath.Join(u.env["PI_CODING_AGENT_DIR"], "extensions", "vecnet-p.ts")
	if _, err := os.Stat(global); err != nil || strings.Contains(errs, "project extensions load only") {
		t.Fatalf("global: %v stderr=%q", err, errs)
	}
	dir := t.TempDir()
	installRun(t, u, "--client", "pi", "--profile", "q", "--settings", dir, "--write")
	if _, err := os.Stat(filepath.Join(dir, "vecnet-q.ts")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "mine.ts")
	installRun(t, u, "--client", "pi", "--profile", "q", "--settings", file, "--write")
	if body, err := os.ReadFile(file); err != nil || !strings.HasPrefix(string(body), piMarker) {
		t.Fatalf("settings file: %v", err)
	}
}

// A file the installer did not write is never replaced, and a tracked
// project target is refused without --settings.
func TestHookInstallPiRefusals(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	u, root := piInstallRepo(t)
	path := filepath.Join(root, ".pi", "extensions", "vecnet-p.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := cliFixtureWriteFile(path, []byte("export default () => {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := u.run("hook", "install", "--bin", "vecnet", "--client", "pi", "--profile", "p", "--write")
	if code == 0 || !strings.Contains(errs, "was not written by vecnet") {
		t.Fatalf("foreign file: code=%d errs=%q", code, errs)
	}
	if body, _ := os.ReadFile(path); string(body) != "export default () => {}\n" {
		t.Fatal("foreign file replaced")
	}
	// Tracked target.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	installRun(t, u, "--client", "pi", "--profile", "p", "--write")
	gitT(t, "-C", root, "add", ".pi/extensions/vecnet-p.ts")
	code, _, errs = u.run("hook", "install", "--bin", "vecnet", "--client", "pi", "--profile", "p", "--write")
	if code == 0 || !strings.Contains(errs, "is tracked by git") || !strings.Contains(errs, ".git/info/exclude") {
		t.Fatalf("tracked: code=%d errs=%q", code, errs)
	}
	for _, args := range [][]string{{"--mcp"}, {"--auto-ack"}, {"--rollback", "x"}, {"--global", "--settings", "x.ts"}} {
		code, _, errs := u.run(append([]string{"hook", "install", "--bin", "vecnet", "--client", "pi", "--profile", "p"}, args...)...)
		if code != 2 && code != 1 || errs == "" {
			t.Fatalf("%v: code=%d errs=%q", args, code, errs)
		}
	}
	code, _, errs = u.run("hook", "install", "--bin", "vecnet", "--client", "claude", "--profile", "p", "--global")
	if code == 0 || !strings.Contains(errs, "--global requires --client pi") {
		t.Fatalf("--global for claude: code=%d errs=%q", code, errs)
	}
}

// The generated extension, loaded by node against a fake pi API, spawns the
// configured binary with the documented payload and turns its output into
// a before_agent_start message and a steered post-tool message; print-mode
// and custom-message runs pass nothing through.
func TestHookInstallPiExtensionRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake vecnet is a POSIX shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, "--experimental-strip-types", "-e", "1").CombinedOutput(); err != nil {
		t.Skipf("node cannot strip types: %s", out)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	u, _ := piInstallRepo(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	fake := filepath.Join(dir, "fake-vecnet")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nprintf 'ENV %s\\n' \"$VECNET_CONFIG\" >> " + log + "\ncat >> " + log + "\nprintf '{\"additionalContext\":\"inbox for %s\"}\\n' \"$7\"\n"
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(dir, "vecnet-p.ts")
	code, out, errs := u.run("hook", "install", "--bin", fake, "--client", "pi", "--profile", "p", "--settings", ext, "--write")
	if code != 0 {
		t.Fatalf("install: %d %q %q", code, out, errs)
	}
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harness, []byte(`
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.argv[2]).href);
const handlers = {};
const sent = [];
const pi = { on: (ev, fn) => { handlers[ev] = fn; }, sendMessage: (m, o) => sent.push([m, o]) };
mod.default(pi);
const entries = [];
const ctx = (mode) => ({ mode, sessionManager: { getSessionId: () => "sess-1", getSessionFile: () => "/s/sess-1.jsonl", getEntries: () => entries } });
const results = {};
results.prompt = await handlers.before_agent_start({ messageType: "user", prompt: "hi" }, ctx("tui"));
results.custom = await handlers.before_agent_start({ messageType: "custom" }, ctx("tui"));
handlers.tool_execution_end({}, ctx("tui"));
handlers.tool_execution_end({}, ctx("tui")); // in flight: skipped
await new Promise((r) => setTimeout(r, 1500));
// The steer was discarded (Esc): it never became a session entry, so the
// settle handler stores it again, without a delivery mode.
handlers.agent_settled({ aborted: true }, ctx("tui"));
// A second settle has nothing pending.
handlers.agent_settled({}, ctx("tui"));
// Print mode never spawns the hook.
results.print = await handlers.before_agent_start({ messageType: "user", prompt: "hi" }, ctx("print"));
handlers.tool_execution_end({}, ctx("json"));
await new Promise((r) => setTimeout(r, 500));
results.sent = sent;
console.log(JSON.stringify(results));
`), 0644); err != nil {
		t.Fatal(err)
	}
	run, err := exec.Command(node, "--experimental-strip-types", "--no-warnings", harness, ext).CombinedOutput()
	if err != nil {
		t.Fatalf("harness: %v %s", err, run)
	}
	var got struct {
		Prompt struct {
			Message struct {
				CustomType, Content string
				Display             bool
			}
		}
		Custom any
		Print  any
		Sent   [][]map[string]any
	}
	if err := json.Unmarshal(run, &got); err != nil {
		t.Fatalf("harness output %q: %v", run, err)
	}
	if got.Prompt.Message.CustomType != "vecnet" || got.Prompt.Message.Content != "inbox for user-prompt" || !got.Prompt.Message.Display || got.Custom != nil {
		t.Fatalf("prompt=%+v custom=%v", got.Prompt, got.Custom)
	}
	if len(got.Sent) != 2 || got.Sent[0][0]["content"] != "inbox for post-tool" || got.Sent[0][1]["deliverAs"] != "steer" {
		t.Fatalf("sent=%v", got.Sent)
	}
	// The re-send is the same message (same steer id), stored while idle.
	if got.Sent[1][0]["content"] != "inbox for post-tool" || got.Sent[1][1] != nil || got.Print != nil {
		t.Fatalf("re-send=%v print=%v", got.Sent[1], got.Print)
	}
	d0, _ := got.Sent[0][0]["details"].(map[string]any)
	d1, _ := got.Sent[1][0]["details"].(map[string]any)
	if d0["vecnetSteer"] == nil || d0["vecnetSteer"] != d1["vecnetSteer"] {
		t.Fatalf("steer ids %v %v", d0, d1)
	}
	calls, _ := os.ReadFile(log)
	c := string(calls)
	pid := `"pid":`
	for _, want := range []string{"hook --client pi --profile p --event user-prompt", "hook --client pi --profile p --event post-tool", `"event":"user-prompt"`, `"session_id":"sess-1"`, `"session_file":"/s/sess-1.jsonl"`, `"mode":"tui"`, pid, "ENV " + u.conf} {
		if !strings.Contains(c, want) {
			t.Fatalf("calls lack %q:\n%s", want, c)
		}
	}
	if strings.Count(c, "--event post-tool") != 1 || strings.Count(c, "--event user-prompt") != 1 {
		t.Fatalf("post-tool not deduplicated while in flight:\n%s", c)
	}
}
