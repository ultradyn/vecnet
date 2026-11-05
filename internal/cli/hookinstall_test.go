package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func installUser(t *testing.T) *user {
	t.Helper()
	u := profileUser(t, "http://127.0.0.1:1", "vn_peer_unused")
	u.env["XDG_CONFIG_HOME"] = filepath.Join(filepath.Dir(u.conf), "xdg")
	u.env["CODEX_HOME"] = filepath.Join(filepath.Dir(u.conf), "codex")
	return u
}
func installRun(t *testing.T, u *user, args ...string) (string, string) {
	t.Helper()
	code, out, errs := u.run(append([]string{"hook", "install", "--bin", "vecnet"}, args...)...)
	if code != 0 {
		t.Fatalf("install: exit %d, stdout=%q stderr=%q", code, out, errs)
	}
	return out, errs
}
func settingsFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "settings", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func installFile(t *testing.T, body []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.local.json")
	if err := os.WriteFile(p, body, mode); err != nil {
		t.Fatal(err)
	}
	return p
}
func readSettings(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func decodeSettings(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("JSON %q: %v", b, err)
	}
	return v
}
func settingsCommands(t *testing.T, body []byte, event string) []string {
	t.Helper()
	hooks, ok := decodeSettings(t, body)["hooks"].(map[string]any)
	if !ok {
		t.Fatal("missing hooks object")
	}
	groups, ok := hooks[event].([]any)
	if !ok {
		t.Fatalf("missing event array %s", event)
	}
	var commands []string
	for _, group := range groups {
		g, ok := group.(map[string]any)
		if !ok {
			t.Fatalf("bad group %v", group)
		}
		handlers, ok := g["hooks"].([]any)
		if !ok {
			t.Fatalf("bad handlers %v", g)
		}
		for _, handler := range handlers {
			h, ok := handler.(map[string]any)
			if !ok {
				t.Fatalf("bad handler %v", handler)
			}
			command, _ := h["command"].(string)
			if strings.Contains(command, "vecnet hook --profile ") {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

// Profiles may not look like flags: install is an ordinary command, so a
// refused profile is a usage error and nothing is printed or written.
func TestHookInstallRefusesFlagProfiles(t *testing.T) {
	for _, profile := range []string{"-h", "--help", "-p"} {
		for _, mode := range []string{"explicit", "env"} {
			t.Run(profile+"/"+mode, func(t *testing.T) {
				u := installUser(t)
				u.env["VECNET_PROFILE"] = profile
				body := fmt.Sprintf("[profiles.%s]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_peer_unused\"\n", profile)
				if err := os.WriteFile(u.conf, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "settings.local.json")
				args := []string{"hook", "install", "--bin", "vecnet", "--write", "--settings", path}
				if mode == "explicit" {
					args = append(args, "--profile="+profile)
				}
				code, out, errs := u.run(args...)
				if code != 1 || out != "" || !strings.Contains(errs, "profile must match") {
					t.Fatalf("flag-like profile accepted: exit=%d stdout=%q stderr=%q", code, out, errs)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("settings written: %v", err)
				}
			})
		}
	}
}

func TestHookInstallPrintsStanza(t *testing.T) {
	u := installUser(t)
	path := filepath.Join(t.TempDir(), "absent.json")
	out, errs := installRun(t, u, "--profile", "p", "--settings", path)
	const want = `{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "vecnet hook --profile p --event user-prompt",
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "vecnet hook --profile p --event post-tool",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
`
	if out != want {
		t.Fatalf("stanza:\n%s\nwant:\n%s", out, want)
	}
	if !strings.Contains(errs, "would merge into "+path+"; rerun with --write") || !strings.Contains(errs, "--settings") {
		t.Fatalf("target reason/hint: %q", errs)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("print created file: %v", err)
	}
}

func TestHookInstallWriteMergesPreservingHooks(t *testing.T) {
	u := installUser(t)
	original := settingsFixture(t, "mixed.json")
	path := installFile(t, original, 0640)
	out, errs := installRun(t, u, "--profile", "p", "--write", "--settings", path)
	if out != "" || !strings.Contains(errs, "wrote "+path+" (UserPromptSubmit, PostToolUse)") {
		t.Fatalf("output=%q, stderr=%q", out, errs)
	}
	merged := readSettings(t, path)
	before, after := decodeSettings(t, original), decodeSettings(t, merged)
	for _, key := range []string{"permissions", "env"} {
		if !reflect.DeepEqual(before[key], after[key]) {
			t.Fatalf("%s changed", key)
		}
	}
	bh, ah := before["hooks"].(map[string]any), after["hooks"].(map[string]any)
	if !reflect.DeepEqual(bh["PreToolUse"], ah["PreToolUse"]) {
		t.Fatal("foreign PreToolUse hooks changed")
	}
	bg, ag := bh["UserPromptSubmit"].([]any), ah["UserPromptSubmit"].([]any)
	if len(ag) != 2 || !reflect.DeepEqual(bg[0], ag[0]) {
		t.Fatal("c2c hook changed or missing")
	}
	for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		if got := settingsCommands(t, merged, event); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event " + suffix}) {
			t.Fatalf("%s commands=%v", event, got)
		}
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0640 {
		t.Fatalf("mode=%v err=%v", st, err)
	}
}

func TestHookInstallPreservesOrderAndLayout(t *testing.T) {
	u := installUser(t)
	path := installFile(t, settingsFixture(t, "mixed.json"), 0644)
	installRun(t, u, "--profile", "p", "--write", "--settings", path)
	const want = `{
  "permissions": {},
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "command": "dcg",
            "type": "command"
          },
          {
            "command": "deny-find-home",
            "type": "command"
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "vecnet hook --profile p --event post-tool",
            "timeout": 5
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "command": "c2c hook",
            "type": "command"
          }
        ]
      },
      {
        "hooks": [
          {
            "type": "command",
            "command": "vecnet hook --profile p --event user-prompt",
            "timeout": 5
          }
        ]
      }
    ]
  },
  "env": {
    "escaped": "\u00e9",
    "html": "<&>",
    "decimal": 1.0,
    "exponent": 1e3
  }
}
`
	if got := string(readSettings(t, path)); got != want {
		t.Fatalf("layout:\n%s\nwant:\n%s", got, want)
	}
	t.Run("four spaces", func(t *testing.T) {
		path := installFile(t, settingsFixture(t, "four.json"), 0644)
		installRun(t, u, "--profile", "p", "--write", "--settings", path)
		const expected = `{
    "env": {},
    "hooks": {
        "UserPromptSubmit": [
            {
                "hooks": [
                    {
                        "type": "command",
                        "command": "vecnet hook --profile p --event user-prompt",
                        "timeout": 5
                    }
                ]
            }
        ],
        "PostToolUse": [
            {
                "matcher": "*",
                "hooks": [
                    {
                        "type": "command",
                        "command": "vecnet hook --profile p --event post-tool",
                        "timeout": 5
                    }
                ]
            }
        ]
    }
}
`
		if got := string(readSettings(t, path)); got != expected {
			t.Fatalf("four-space layout:\n%s", got)
		}
	})
	t.Run("hooks last", func(t *testing.T) {
		path := installFile(t, settingsFixture(t, "nohooks.json"), 0644)
		installRun(t, u, "--profile", "p", "--write", "--settings", path)
		out := string(readSettings(t, path))
		if !strings.HasPrefix(out, "{\n  \"permissions\": {},\n  \"env\": {},\n  \"hooks\": {") {
			t.Fatalf("hooks not appended last: %s", out)
		}
		if len(decodeSettings(t, []byte(out))) != 3 {
			t.Fatal("unexpected keys")
		}
	})
}

func TestHookInstallIdempotent(t *testing.T) {
	u := installUser(t)
	path := installFile(t, settingsFixture(t, "mixed.json"), 0644)
	args := []string{"--profile", "p", "--write", "--settings", path}
	installRun(t, u, args...)
	first := string(readSettings(t, path))
	_, errs := installRun(t, u, args...)
	if !strings.Contains(errs, "unchanged "+path) || string(readSettings(t, path)) != first {
		t.Fatalf("not idempotent: %q", errs)
	}
	installRun(t, u, append(args, "--auto-ack")...)
	updated := readSettings(t, path)
	for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		if got := settingsCommands(t, updated, event); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event " + suffix + " --auto-ack"}) {
			t.Fatalf("%s commands=%v", event, got)
		}
	}
	if got, want := string(updated), strings.ReplaceAll(first, `",`+"\n            \"timeout\"", ` --auto-ack",`+"\n            \"timeout\""); got != want {
		t.Fatalf("auto-ack did not replace in place:\n%s", got)
	}
}

func TestHookInstallSecondProfile(t *testing.T) {
	u := installUser(t)
	path := installFile(t, []byte("{}\n"), 0644)
	for _, profile := range []string{"p", "q"} {
		installRun(t, u, "--profile", profile, "--write", "--settings", path)
	}
	for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		got := settingsCommands(t, readSettings(t, path), event)
		want := []string{"vecnet hook --profile p --event " + suffix, "vecnet hook --profile q --event " + suffix}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s commands=%v", event, got)
		}
	}
}

func TestHookInstallRenamedBinaryIdempotent(t *testing.T) {
	for _, tc := range []struct{ bin, shellBin string }{
		{"/opt/bin/vn", "/opt/bin/vn"},
		{"/opt/$(unsafe);>|&*/vecnet", "'/opt/$(unsafe);>|&*/vecnet'"},
		{"/opt/it's tools/vn", "'/opt/it'\"'\"'s tools/vn'"},
	} {
		t.Run(tc.bin, func(t *testing.T) {
			u := installUser(t)
			foreign := fmt.Sprintf(`{"hooks":{"UserPromptSubmit":[{"extra":"keep","hooks":[{"type":"command","command":"/opt/foreign/vn hook --profile p --event user-prompt"},{"type":"command","command":%q},{"type":"command","command":%q},{"type":"command","command":%q}]}]}}`,
				tc.shellBin+" hook --profile p-other --event user-prompt",
				tc.shellBin+" hook --profile p --event user-prompt-extra",
				tc.shellBin+" hook --profile p --event post-tool")
			path := installFile(t, []byte(foreign), 0644)
			args := []string{"hook", "install", "--bin", tc.bin, "--profile=p", "--write", "--settings", path}
			code, out, errs := u.run(args...)
			if code != 0 || out != "" {
				t.Fatalf("initial install: exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			first := string(readSettings(t, path))
			code, out, errs = u.run(args...)
			if code != 0 || out != "" || !strings.Contains(errs, "unchanged "+path) || string(readSettings(t, path)) != first {
				t.Fatalf("renamed binary not idempotent: exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			code, out, errs = u.run(append(args, "--auto-ack")...)
			if code != 0 || out != "" {
				t.Fatalf("auto-ack install: exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			hooks := decodeSettings(t, readSettings(t, path))["hooks"].(map[string]any)
			for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
				groups := hooks[event].([]any)
				index := 0
				if event == "UserPromptSubmit" {
					index = 1
					original := decodeSettings(t, []byte(foreign))["hooks"].(map[string]any)[event].([]any)[0]
					if !reflect.DeepEqual(groups[0], original) {
						t.Fatal("foreign hooks changed")
					}
				}
				if len(groups) != index+1 {
					t.Fatalf("%s groups=%v", event, groups)
				}
				handlers := groups[index].(map[string]any)["hooks"].([]any)
				want := tc.shellBin + " hook --profile p --event " + suffix + " --auto-ack"
				if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != want {
					t.Fatalf("%s handlers=%v", event, handlers)
				}
			}
		})
	}
}

func TestHookInstallRefusesBadShapes(t *testing.T) {
	for _, body := range []string{`{"hooks": [`, `[]`, `{"hooks":[]}`, `{"hooks":{"UserPromptSubmit":{}}}`, `{"hooks":{"PostToolUse":{}}}`, `{"hooks":{"UserPromptSubmit":[],"PostToolUse":false}}`, `{"hooks":{"PreToolUse":{}}}`, `{"hooks":{"SessionStart":null}}`} {
		t.Run(body, func(t *testing.T) {
			u := installUser(t)
			path := installFile(t, []byte(body), 0640)
			code, out, _ := u.run("hook", "install", "--profile", "p", "--bin", "vecnet", "--write", "--settings", path)
			if code != 1 || out != "" {
				t.Fatalf("exit=%d stdout=%q", code, out)
			}
			if got := string(readSettings(t, path)); got != body {
				t.Fatalf("bad file modified: %q", got)
			}
		})
	}
}

func TestHookInstallRefusesUnsafeProfile(t *testing.T) {
	u := installUser(t)
	path := filepath.Join(t.TempDir(), "absent.json")
	code, out, _ := u.run("hook", "install", "--profile", "p;rm -rf x", "--bin", "vecnet", "--write", "--settings", path)
	if code != 1 || out != "" {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file written: %v", err)
	}
}

// isolateGitEnv clears Git's repository-location and discovery overrides
// for the test, restoring the inherited values when it finishes. Git
// discovery uses the process environment, not the CLI user's getenv, so an
// inherited GIT_DIR/GIT_WORK_TREE would otherwise point git init and the
// installer at another checkout.
func isolateGitEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"} {
		t.Setenv(key, "") // Restore the inherited value when the test finishes.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

// foreignCheckout points GIT_DIR/GIT_WORK_TREE at a disposable checkout, as
// a caller's environment might, and returns a check that it is unchanged.
func foreignCheckout(t *testing.T) func() {
	t.Helper()
	foreign := filepath.Join(t.TempDir(), "foreign")
	if out, err := exec.Command("git", "init", "--quiet", foreign).CombinedOutput(); err != nil {
		t.Fatalf("git init foreign: %v %s", err, out)
	}
	t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
	t.Setenv("GIT_WORK_TREE", foreign)
	return func() {
		t.Helper()
		if _, err := os.Lstat(filepath.Join(foreign, ".claude")); !os.IsNotExist(err) {
			t.Fatalf("foreign checkout settings touched: %v", err)
		}
	}
}

func TestHookInstallDefaultsToLocalAtGitRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	isolateGitEnv(t)
	foreignUnchanged := foreignCheckout(t)
	defer foreignUnchanged()
	isolateGitEnv(t) // the caller's overrides must not reach git init or the installer
	u := installUser(t)
	root := filepath.Join(t.TempDir(), "repo with trailing space ")
	cmd := exec.Command("git", "init", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte(`{"env":{"KEEP":"yes"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	_, errs := installRun(t, u, "--write") // no --profile: resolved from $VECNET_PROFILE
	path := filepath.Join(root, ".claude", "settings.local.json")
	if got := settingsCommands(t, readSettings(t, path), "UserPromptSubmit"); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event user-prompt"}) {
		t.Fatalf("default profile=%v", got)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0644 {
		t.Fatalf("mode=%v err=%v", st, err)
	}
	if !strings.Contains(errs, path) || !strings.Contains(errs, "git") {
		t.Fatalf("target reason=%q", errs)
	}
	if string(readSettings(t, shared)) != `{"env":{"KEEP":"yes"}}` {
		t.Fatal("shared settings changed")
	}
	if _, err := os.Stat(filepath.Join(u.env["HOME"], ".claude")); !os.IsNotExist(err) {
		t.Fatalf("HOME/.claude exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("sub/.claude exists: %v", err)
	}
}

func TestHookInstallKeepsSymlink(t *testing.T) {
	u := installUser(t)
	real := installFile(t, []byte("{}\n"), 0640)
	link := filepath.Join(filepath.Dir(real), "linked.json")
	if err := os.Symlink(filepath.Base(real), link); err != nil {
		t.Fatal(err)
	}
	installRun(t, u, "--profile", "p", "--write", "--settings", link)
	st, err := os.Lstat(link)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", st, err)
	}
	if target, err := os.Readlink(link); err != nil || target != filepath.Base(real) {
		t.Fatalf("link target=%q %v", target, err)
	}
	for _, event := range []string{"UserPromptSubmit", "PostToolUse"} {
		if len(settingsCommands(t, readSettings(t, real), event)) != 1 {
			t.Fatalf("missing %s", event)
		}
	}
	st, err = os.Stat(real)
	if err != nil || st.Mode().Perm() != 0640 {
		t.Fatalf("target mode=%v %v", st, err)
	}
}

func TestHookInstallPreservesMixedGroupAndQuotedBinary(t *testing.T) {
	u := installUser(t)
	const before = `{
  "hooks": {
    "UserPromptSubmit": [
      {
        "matcher": "custom",
        "extra": "\u00e9<&>",
        "hooks": [
          {
            "command": "c2c hook",
            "type": "command"
          },
          {
            "command": "'/opt/it'\"'\"'s tools/vecnet' hook --profile p --event user-prompt",
            "type": "command",
            "timeout": 2
          },
          {
            "type": "command",
            "command": "vecnet hook --profile q --event user-prompt",
            "timeout": 5
          },
          {
            "type": "command",
            "command": "vecnet hook --profile p --event user-prompt-extra"
          }
        ]
      }
    ]
  }
}
`
	path := installFile(t, []byte(before), 0644)
	code, out, errs := u.run("hook", "install", "--profile", "p", "--bin", "/opt/it's tools/vecnet", "--write", "--auto-ack", "--settings", path)
	if code != 0 || out != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errs)
	}
	body := readSettings(t, path)
	hooks := decodeSettings(t, body)["hooks"].(map[string]any)
	groups := hooks["UserPromptSubmit"].([]any)
	if len(groups) != 1 {
		t.Fatalf("replaced group appended: %v", groups)
	}
	old := decodeSettings(t, []byte(before))["hooks"].(map[string]any)["UserPromptSubmit"].([]any)[0].(map[string]any)
	group := groups[0].(map[string]any)
	if group["matcher"] != old["matcher"] || group["extra"] != old["extra"] {
		t.Fatal("group properties changed")
	}
	handlers, original := group["hooks"].([]any), old["hooks"].([]any)
	if len(handlers) != 4 {
		t.Fatalf("handlers=%v", handlers)
	}
	for _, i := range []int{0, 2, 3} {
		if !reflect.DeepEqual(handlers[i], original[i]) {
			t.Fatalf("foreign handler %d changed", i)
		}
	}
	want := map[string]any{"type": "command", "command": "'/opt/it'\"'\"'s tools/vecnet' hook --profile p --event user-prompt --auto-ack", "timeout": float64(5)}
	if !reflect.DeepEqual(handlers[1], want) {
		t.Fatalf("replacement=%v want=%v", handlers[1], want)
	}
	if !strings.Contains(string(body), `"extra": "\u00e9<&>"`) {
		t.Fatal("foreign scalar spelling changed")
	}
	code, _, errs = u.run("hook", "install", "--profile", "p", "--bin", "/opt/it's tools/vecnet", "--write", "--auto-ack", "--settings", path)
	if code != 0 || !strings.Contains(errs, "unchanged "+path) || string(readSettings(t, path)) != string(body) {
		t.Fatalf("quoted binary not idempotent: exit=%d stderr=%q", code, errs)
	}
}

func TestHookInstallPreservesTrailingNewline(t *testing.T) {
	for _, before := range []string{"", " \n", "{}", "{}\n", `{"env":{"raw":"\u00e9"}}`} {
		t.Run(before, func(t *testing.T) {
			u := installUser(t)
			path := installFile(t, []byte(before), 0644)
			installRun(t, u, "--profile", "p", "--write", "--settings", path)
			body := string(readSettings(t, path))
			if strings.HasSuffix(body, "\n") != strings.HasSuffix(before, "\n") {
				t.Fatalf("newline policy changed: before=%q after=%q", before, body)
			}
			if before == `{"env":{"raw":"\u00e9"}}` && !strings.HasPrefix(body, "{\n  \"env\": {\n    \"raw\": \"\\u00e9\"\n  },\n  \"hooks\": {") {
				t.Fatalf("compact layout normalization changed spelling/order: %s", body)
			}
		})
	}
}

func TestHookInstallFallbackToCwd(t *testing.T) {
	isolateGitEnv(t)
	u := installUser(t)
	parent := t.TempDir()
	if out, err := exec.Command("git", "-C", parent, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("init disposable ancestor repository: %v: %s", err, out)
	}
	root := filepath.Join(parent, "cwd")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	// TMPDIR may itself be inside a checkout. Keep discovery below the
	// private cwd's parent, including when the caller supplied Git overrides.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	// Fail before --write if Git could target an ancestor's real settings.
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput(); err == nil {
		t.Fatalf("private cwd unexpectedly belongs to a repository: %s", out)
	}
	_, errs := installRun(t, u, "--write", "--profile", "p")
	path := filepath.Join(root, ".claude", "settings.local.json")
	if len(settingsCommands(t, readSettings(t, path), "PostToolUse")) != 1 {
		t.Fatal("missing post-tool hook")
	}
	if _, err := os.Stat(filepath.Join(parent, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("ancestor settings directory touched: %v", err)
	}
	if !strings.Contains(errs, "not in a git repository; using "+root) || !strings.Contains(errs, path) {
		t.Fatalf("missing fallback reason: %q", errs)
	}
	st, err := os.Stat(filepath.Dir(path))
	if err != nil || st.Mode().Perm() != 0755 {
		t.Fatalf("directory mode=%v err=%v", st, err)
	}
}

func hookInstallExtraFlagSeed(t *testing.T, command string) []byte {
	t.Helper()
	hooks := map[string]any{}
	for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		hooks[event] = []any{map[string]any{
			"extra": "keep",
			"hooks": []any{
				map[string]any{"type": "command", "command": "c2c hook"},
				map[string]any{"type": "command", "command": fmt.Sprintf(command, suffix), "timeout": 2},
				map[string]any{"type": "command", "command": "vecnet hook --profile p-other --event " + suffix + " --as peer_other"},
			},
		}}
	}
	body, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestHookInstallRefusesDuplicateKeys(t *testing.T) {
	for _, body := range []string{
		`{"hooks":{},"hooks":{}}`,
		`{"hooks":{},"hooks":[]}`,
		`{"hooks":{},"ho\u006fks":{}}`,
		`{"ho\u006fks":{},"hooks":[]}`,
		`{"hooks":{"UserPromptSubmit":[],"UserPromptSubmit":[]}}`,
		`{"hooks":{"UserPromptSubmit":[],"UserPromptSubmit":{}}}`,
		`{"hooks":{"PostToolUse":[],"PostTool\u0055se":[]}}`,
		`{"hooks":{"PostTool\u0055se":[],"PostToolUse":false}}`,
		`{"hooks":{"UserPromptSubmit":[{"hooks":[],"hooks":[]}]}}`,
		`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","type":"prompt","command":"vecnet hook --profile p --event user-prompt"}]}]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			u := installUser(t)
			path := installFile(t, []byte(body), 0640)
			code, out, errs := u.run("hook", "install", "--profile", "p", "--bin", "vecnet", "--write", "--settings", path)
			if code != 1 || out != "" || !strings.Contains(errs, "duplicate JSON object key") {
				t.Errorf("duplicate keys not rejected: exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			if got := string(readSettings(t, path)); got != body {
				t.Errorf("invalid settings changed: %q", got)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0640 {
				t.Errorf("invalid settings mode changed: %v %v", info, err)
			}
		})
	}
}

// Only the exact form hookCommand writes is ours. Every variant, including
// ones a shell would run as an equivalent vecnet hook, is foreign: it is
// preserved byte for byte and the installer appends its own group.
func TestHookInstallPreservesForeignCommands(t *testing.T) {
	const peer = "peer_00000000000000000000000000"
	for _, tc := range []struct{ name, command string }{
		{"assignment prefix", "C2C_CONFIG=/foreign/vecnet hook --profile p --event %s"},
		{"quoted assignment prefix", "C2C_CONFIG='/foreign/my dir/vecnet' hook --profile p --event %s"},
		{"assignment before vecnet", "VECNET_CONFIG=/foreign/v.toml vecnet hook --profile p --event %s"},
		{"as suffix", "vecnet hook --profile p --event %s --as " + peer},
		{"as prefix", "vecnet --as=" + peer + " hook --profile p --event %s"},
		{"as interleaved", "vecnet hook --profile p --as " + peer + " --event %s"},
		{"net global", "vecnet --net p hook --profile p --event %s"},
		{"json global", "vecnet --json hook --profile p --event %s"},
		{"repeated boolean", "vecnet hook --profile p --event %s --auto-ack --auto-ack"},
		{"reordered flags", "vecnet hook --event %s --profile p"},
		{"profile equals", "vecnet hook --profile=p --event %s"},
		{"event equals", "vecnet hook --profile p --event=%s"},
		{"double space", "vecnet hook  --profile p --event %s"},
		{"trailing space", "vecnet hook --profile p --event %s "},
		{"tab", "vecnet\thook --profile p --event %s"},
		{"quoted safe binary", "'vecnet' hook --profile p --event %s"},
		{"quoted arguments", "vecnet hook --profile 'p' --event '%s'"},
		{"last profile wins", "vecnet hook --profile other --event %s --profile p"},
		{"redirected c2c", "c2c>/foreign/vecnet hook --profile p --event %s"},
		{"redirected vecnet", "vecnet hook --profile p --event %s --as peer>/foreign/output"},
		{"semicolon", "vecnet hook --profile p --event %s --as peer;c2c"},
		{"background", "vecnet hook --profile p --event %s --as peer&c2c"},
		{"pipeline", "vecnet hook --profile p --event %s --as peer|c2c"},
		{"and list", "vecnet hook --profile p --event %s --as peer&&c2c"},
		{"or list", "vecnet hook --profile p --event %s --as peer||c2c"},
		{"newline", "vecnet hook --profile p --event %s --as peer\nc2c"},
		{"command substitution", "$(c2c)/vecnet hook --profile p --event %s"},
		{"double quoted backslash", `"/opt/\vecnet" hook --profile p --event %s`},
		{"quoted command substitution", "\"$(c2c)/vecnet\" hook --profile p --event %s"},
		{"backticks", "`c2c`/vecnet hook --profile p --event %s"},
		{"parameter expansion", "${OTHER}/vecnet hook --profile p --event %s"},
		{"argument expansion", "vecnet hook --profile p --event %s --as $PEER"},
		{"glob", "/opt/*/vecnet hook --profile p --event %s"},
		{"tilde", "~/vecnet hook --profile p --event %s"},
		{"brace expansion", "/opt/{one,two}/vecnet hook --profile p --event %s"},
		{"comment hides flags", "vecnet # hook --profile p --event %s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := installUser(t)
			seed := hookInstallExtraFlagSeed(t, tc.command)
			path := installFile(t, seed, 0640)
			original := decodeSettings(t, seed)["hooks"].(map[string]any)
			for _, autoAck := range []bool{false, true} {
				args := []string{"--profile", "p", "--write", "--settings", path}
				if autoAck {
					args = append(args, "--auto-ack")
				}
				installRun(t, u, args...)
				body := readSettings(t, path)
				hooks := decodeSettings(t, body)["hooks"].(map[string]any)
				for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
					groups := hooks[event].([]any)
					if !reflect.DeepEqual(groups[0], original[event].([]any)[0]) {
						t.Fatalf("%s: foreign command or metadata changed: %q", event, fmt.Sprintf(tc.command, suffix))
					}
					if len(groups) != 2 {
						t.Fatalf("%s: groups=%v, want original plus installed group", event, groups)
					}
					handlers := groups[1].(map[string]any)["hooks"].([]any)
					want := "vecnet hook --profile p --event " + suffix
					if autoAck {
						want += " --auto-ack"
					}
					if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != want {
						t.Fatalf("%s: installed handlers=%v, want %q", event, handlers, want)
					}
				}
				_, errs := installRun(t, u, args...)
				if string(readSettings(t, path)) != string(body) || !strings.Contains(errs, "unchanged "+path) {
					t.Fatalf("repeat install changed settings: %s", errs)
				}
				if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0640 {
					t.Fatalf("settings mode changed: %v %v", info, err)
				}
			}
		})
	}
}

// A pre-existing handler in the exact form, at the --bin being installed, is
// ours even though the binary is not named vecnet: it is replaced in place.
func TestHookInstallRenamedBinaryReplacedInPlace(t *testing.T) {
	for _, tc := range []struct{ bin, shellBin string }{
		{"/opt/bin/vn", "/opt/bin/vn"},
		{"/opt/it's tools/vn", "'/opt/it'\"'\"'s tools/vn'"},
	} {
		t.Run(tc.bin, func(t *testing.T) {
			u := installUser(t)
			seed := hookInstallExtraFlagSeed(t, tc.shellBin+" hook --profile p --event %s")
			path := installFile(t, seed, 0644)
			original := decodeSettings(t, seed)["hooks"].(map[string]any)
			for _, autoAck := range []bool{false, false, true, true} {
				args := []string{"hook", "install", "--bin", tc.bin, "--profile", "p", "--write", "--settings", path}
				if autoAck {
					args = append(args, "--auto-ack")
				}
				if code, out, errs := u.run(args...); code != 0 || out != "" {
					t.Fatalf("install: exit=%d stdout=%q stderr=%q", code, out, errs)
				}
				hooks := decodeSettings(t, readSettings(t, path))["hooks"].(map[string]any)
				for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
					groups := hooks[event].([]any)
					if len(groups) != 1 {
						t.Fatalf("%s: appended instead of replacing: %v", event, groups)
					}
					handlers := groups[0].(map[string]any)["hooks"].([]any)
					oldHandlers := original[event].([]any)[0].(map[string]any)["hooks"].([]any)
					if len(handlers) != 3 || !reflect.DeepEqual(handlers[0], oldHandlers[0]) || !reflect.DeepEqual(handlers[2], oldHandlers[2]) {
						t.Fatalf("%s: foreign handlers changed: %v", event, handlers)
					}
					want := tc.shellBin + " hook --profile p --event " + suffix
					if autoAck {
						want += " --auto-ack"
					}
					if h := handlers[1].(map[string]any); h["command"] != want || h["timeout"] != float64(5) {
						t.Fatalf("%s: handler=%v want %q", event, h, want)
					}
				}
			}
		})
	}
}

// Without --settings the installer writes only the personal
// settings.local.json at the git top level. A symlink at that path, or at
// .claude, must not redirect the write to shared or global settings: both
// the link and its target stay unchanged and --settings is required.
func TestHookInstallDefaultRefusesSymlinkRedirect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, mode := range []string{"to-shared", "to-global", "parent-dir"} {
		t.Run(mode, func(t *testing.T) {
			isolateGitEnv(t)
			u := installUser(t)
			root := filepath.Join(t.TempDir(), "repo")
			if out, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			claude := filepath.Join(root, ".claude")
			local := filepath.Join(claude, "settings.local.json")
			body := []byte(`{"env":{"KEEP":"yes"}}`)
			var target, link string
			switch mode {
			case "to-shared":
				if err := os.MkdirAll(claude, 0755); err != nil {
					t.Fatal(err)
				}
				target, link = filepath.Join(claude, "settings.json"), local
				if err := os.Symlink("settings.json", link); err != nil {
					t.Fatal(err)
				}
			case "to-global":
				if err := os.MkdirAll(claude, 0755); err != nil {
					t.Fatal(err)
				}
				target, link = filepath.Join(u.env["HOME"], ".claude", "settings.json"), local
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			case "parent-dir":
				target, link = filepath.Join(u.env["HOME"], ".claude", "settings.local.json"), claude
				if err := os.Symlink(filepath.Dir(target), link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, body, 0644); err != nil {
				t.Fatal(err)
			}
			before, err := os.Readlink(link)
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(root)
			code, out, errs := u.run("hook", "install", "--bin", "vecnet", "--profile", "p", "--write")
			if code == 0 || out != "" || !strings.Contains(errs, "--settings") {
				t.Fatalf("redirected default accepted: exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			if got := readSettings(t, target); string(got) != string(body) {
				t.Fatalf("link target changed: %q", got)
			}
			if after, err := os.Readlink(link); err != nil || after != before {
				t.Fatalf("link changed: %q %v", after, err)
			}
			// The explicit target is still honoured through the link.
			installRun(t, u, "--profile", "p", "--write", "--settings", local)
			if len(settingsCommands(t, readSettings(t, target), "PostToolUse")) != 1 {
				t.Fatal("explicit --settings through the link not written")
			}
			if after, err := os.Readlink(link); err != nil || after != before {
				t.Fatalf("explicit write replaced link: %q %v", after, err)
			}
		})
	}
}

// fakeClaude puts a logging `claude` first on PATH (so the test cannot be
// parallel) and returns its log path: one line per run, its cwd and argv.
// The real claude is never run. exitCode is the fake's exit status.
func fakeClaude(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "claude.log")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'cwd=%%s argv=%%s\\n' \"$(pwd -P)\" \"$*\" >> '%s'\nexit %d\n", log, exitCode)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func claudeRuns(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestHookInstallMCPPrintsRegistration(t *testing.T) {
	log := fakeClaude(t, 0)
	u := installUser(t)
	delete(u.env, "VECNET_CONFIG") // --profile names the profile; no config is read
	// An absent --settings file keeps the target off the checkout's real
	// .claude/settings.local.json (and off git, ancestors and worktrees).
	absent := filepath.Join(t.TempDir(), "absent.json")
	out, errs := installRun(t, u, "--profile", "p", "--mcp", "--settings", absent)
	for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		if got := settingsCommands(t, []byte(out), event); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event " + suffix + " --mcp vecnet-p"}) {
			t.Fatalf("%s commands=%v", event, got)
		}
	}
	if !strings.Contains(errs, "claude mcp add --scope local vecnet-p -- vecnet --net p mcp") || strings.Contains(errs, "--env") {
		t.Fatalf("registration=%q", errs)
	}
	out, _ = installRun(t, u, "--profile", "p", "--mcp", "--auto-ack", "--settings", absent)
	for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		if got := settingsCommands(t, []byte(out), event); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event " + suffix + " --auto-ack --mcp vecnet-p"}) {
			t.Fatalf("%s auto-ack commands=%v", event, got)
		}
	}
	// An explicit VECNET_CONFIG goes into the registration, after the name.
	u = installUser(t)
	other := installFile(t, []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"vecnet hook --profile q --event user-prompt --mcp vecnet-q","timeout":5}]}]}}`), 0o644)
	_, errs = installRun(t, u, "--profile", "p", "--mcp", "--settings", other)
	abs, err := filepath.Abs(u.conf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs, "claude mcp add --scope local vecnet-p --env VECNET_CONFIG="+abs+" -- vecnet --net p mcp") {
		t.Fatalf("registration with config=%q", errs)
	}
	if !strings.Contains(errs, "profile(s) q") || !strings.Contains(errs, "every session there also sees each registered vecnet MCP server's tools") {
		t.Fatalf("other-profile warning=%q", errs)
	}
	if runs := claudeRuns(t, log); runs != nil {
		t.Fatalf("claude ran without --write: %q", runs)
	}
}

func TestHookInstallMCPWriteRunsClaude(t *testing.T) {
	log := fakeClaude(t, 0)
	u := installUser(t)
	project := t.TempDir()
	u.env["CLAUDE_PROJECT_DIR"] = project
	realProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	path := installFile(t, []byte("{}\n"), 0o644)
	args := []string{"--profile", "p", "--mcp", "--write", "--settings", path}
	installRun(t, u, args...)
	add := "cwd=" + realProject + " argv=mcp add --scope local vecnet-p --env VECNET_CONFIG=" + u.conf + " -- vecnet --net p mcp"
	remove := "cwd=" + realProject + " argv=mcp remove --scope local vecnet-p"
	if got := claudeRuns(t, log); !reflect.DeepEqual(got, []string{remove, add}) {
		t.Fatalf("claude runs=%q\nwant %q", got, []string{remove, add})
	}
	first := readSettings(t, path)
	for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		if got := settingsCommands(t, first, event); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event " + suffix + " --mcp vecnet-p"}) {
			t.Fatalf("%s commands=%v", event, got)
		}
	}
	// A re-run replaces in place: no duplicate group; claude runs again.
	_, errs := installRun(t, u, args...)
	if string(readSettings(t, path)) != string(first) || !strings.Contains(errs, "unchanged "+path) {
		t.Fatalf("re-run changed settings: %q", errs)
	}
	if got := claudeRuns(t, log); !reflect.DeepEqual(got, []string{remove, add, remove, add}) {
		t.Fatalf("re-run claude runs=%q", got)
	}
	// Without --mcp the handler loses it in place, and claude is not run.
	installRun(t, u, "--profile", "p", "--write", "--settings", path)
	after := readSettings(t, path)
	if want := strings.ReplaceAll(string(first), " --mcp vecnet-p", ""); string(after) != want {
		t.Fatalf("--mcp not dropped in place:\n%s\nwant\n%s", after, want)
	}
	for _, event := range []string{"UserPromptSubmit", "PostToolUse"} {
		if got := settingsCommands(t, after, event); len(got) != 1 {
			t.Fatalf("%s commands=%v", event, got)
		}
	}
	if got := claudeRuns(t, log); len(got) != 4 {
		t.Fatalf("claude ran without --mcp: %q", got)
	}
	// Without CLAUDE_PROJECT_DIR the start directory is the cwd.
	delete(u.env, "CLAUDE_PROJECT_DIR")
	cwd := t.TempDir()
	t.Chdir(cwd)
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	installRun(t, u, args...)
	if got := claudeRuns(t, log); len(got) != 6 || got[4] != "cwd="+realCwd+" argv=mcp remove --scope local vecnet-p" || !strings.HasPrefix(got[5], "cwd="+realCwd+" argv=mcp add ") {
		t.Fatalf("cwd claude runs=%q", got)
	}
}

func TestHookInstallMCPWithoutClaudeStillWrites(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no claude anywhere on PATH
	u := installUser(t)
	u.env["CLAUDE_PROJECT_DIR"] = t.TempDir() // claude runs there, not in the checkout
	path := installFile(t, []byte("{}\n"), 0o644)
	code, _, errs := u.run("hook", "install", "--bin", "vecnet", "--profile", "p", "--mcp", "--write", "--settings", path)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, errs)
	}
	if got := settingsCommands(t, readSettings(t, path), "UserPromptSubmit"); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event user-prompt --mcp vecnet-p"}) {
		t.Fatalf("commands=%v", got)
	}
	if !strings.Contains(errs, "claude mcp add --scope local vecnet-p ") || !strings.Contains(errs, "not on PATH") || !strings.Contains(errs, "run the command above") {
		t.Fatalf("stderr=%q", errs)
	}
	// A claude that fails is reported; the hooks are still written.
	log := fakeClaude(t, 3)
	path = installFile(t, []byte("{}\n"), 0o644)
	code, _, errs = u.run("hook", "install", "--bin", "vecnet", "--profile", "p", "--mcp", "--write", "--settings", path)
	if code != 0 || !strings.Contains(errs, "claude mcp add failed") || !strings.Contains(errs, "run the command above") {
		t.Fatalf("failing claude: exit %d, stderr=%q", code, errs)
	}
	if len(claudeRuns(t, log)) != 2 {
		t.Fatalf("claude runs=%q", claudeRuns(t, log))
	}
	if got := settingsCommands(t, readSettings(t, path), "PostToolUse"); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event post-tool --mcp vecnet-p"}) {
		t.Fatalf("commands after failing claude=%v", got)
	}
}

func TestHookInstallMCPRefusesUnnameable(t *testing.T) {
	log := fakeClaude(t, 0)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"dot", []string{"hook", "install", "--profile", "a.b", "--mcp"}},
		{"25 chars", []string{"hook", "install", "--profile", strings.Repeat("p", 25), "--mcp"}},
		{"as", []string{"--as", "peer_1", "hook", "install", "--profile", "p", "--mcp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := installUser(t)
			const seed = "{\n  \"keep\": true\n}\n"
			path := installFile(t, []byte(seed), 0o640)
			code, out, errs := u.run(append(tc.args, "--bin", "vecnet", "--write", "--settings", path)...)
			if code != 1 || out != "" || !strings.Contains(errs, "hook install --mcp: profile must be 1-24 letters, digits, _ or - (MCP server names), and the installed hooks never take --as") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errs)
			}
			if got := string(readSettings(t, path)); got != seed {
				t.Fatalf("settings changed: %q", got)
			}
			if runs := claudeRuns(t, log); runs != nil {
				t.Fatalf("claude ran: %q", runs)
			}
		})
	}
}

// Codex installer tests exercise the CLI and disposable files only. All
// client/config/cache paths come from installUser's private environment.
func codexInstallRepo(t *testing.T) (*user, string) {
	t.Helper()
	isolateGitEnv(t)
	u := installUser(t)
	root := filepath.Join(t.TempDir(), "codex repo ")
	gitT(t, "init", "--quiet", root)
	t.Chdir(root)
	return u, root
}

func TestHookInstallCodexPrintsStanza(t *testing.T) {
	u, root := codexInstallRepo(t)
	code, out, errs := u.run("hook", "install", "--client", "codex", "--profile", "p", "--bin", "/opt/vecnet")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errs)
	}
	hooks := decodeSettings(t, []byte(out))["hooks"].(map[string]any)
	if len(hooks) != 3 {
		t.Fatalf("installed events=%v", hooks)
	}
	for event, name := range map[string]string{"SessionStart": "session-start", "UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		groups, ok := hooks[event].([]any)
		if !ok || len(groups) != 1 {
			t.Fatalf("%s groups=%v", event, hooks[event])
		}
		group := groups[0].(map[string]any)
		want := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/opt/vecnet hook --profile p --event " + name, "timeout": float64(5), "additionalContextLimit": float64(18000)}}}
		if event == "PostToolUse" {
			want["matcher"] = "*"
		}
		if !reflect.DeepEqual(group, want) {
			t.Fatalf("%s group=%v want=%v", event, group, want)
		}
	}
	for _, event := range []string{"Stop", "SubagentStop", "Interrupt", "SessionEnd", "SubagentStart", "PreToolUse"} {
		if _, ok := hooks[event]; ok {
			t.Fatalf("installed forbidden event %s", event)
		}
	}
	for _, want := range []string{"git top-level; Codex project hooks", "codex: trust these hooks in /hooks before they run; new or changed hooks are skipped until trusted", "codex: project hooks load only in a trusted project; to keep this file personal, list .codex/hooks.json in .git/info/exclude"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("missing %q: %q", want, errs)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote .codex: %v", err)
	}
}

func TestHookInstallCodexDefaultTarget(t *testing.T) {
	u, root := codexInstallRepo(t)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	out, errs := installRun(t, u, "--client", "codex", "--write")
	path := filepath.Join(root, ".codex", "hooks.json")
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse"} {
		if len(settingsCommands(t, readSettings(t, path), event)) != 1 {
			t.Fatalf("missing %s", event)
		}
	}
	for _, want := range []string{"wrote " + path + " (SessionStart, UserPromptSubmit, PostToolUse)", "/hooks", ".git/info/exclude"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("missing %q: %q", want, errs)
		}
	}
	if out != "" {
		t.Fatalf("write stdout=%q", out)
	}
	for _, path := range []string{filepath.Join(root, ".claude"), filepath.Join(sub, ".codex"), filepath.Join(root, ".git", "info", "exclude")} {
		if strings.HasSuffix(path, "exclude") {
			if body, err := os.ReadFile(path); err == nil && strings.Contains(string(body), ".codex/hooks.json") {
				t.Fatal("installer edited git excludes")
			}
		} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected target %s: %v", path, err)
		}
	}
	before := string(readSettings(t, path))
	_, errs = installRun(t, u, "--client", "codex", "--write")
	if !strings.Contains(errs, "unchanged "+path) || string(readSettings(t, path)) != before {
		t.Fatalf("not idempotent: %q", errs)
	}
}

func TestHookInstallCodexRefusesTrackedDefault(t *testing.T) {
	u, root := codexInstallRepo(t)
	path := filepath.Join(root, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0640); err != nil {
		t.Fatal(err)
	}
	gitT(t, "add", "--", ".codex/hooks.json")
	gitT(t, "-c", "commit.gpgsign=false", "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "tracked hooks")
	// A dry run may describe even a tracked target, but must not write it.
	installRun(t, u, "--client", "codex", "--profile", "p")
	code, out, errs := u.run("hook", "install", "--client", "codex", "--profile", "p", "--write")
	want := "hook install: " + path + " is tracked by git (or its status is unknown); writing it would share your profile with everyone; pass --settings " + path + " to write it anyway"
	if code != 1 || out != "" || !strings.Contains(errs, want) {
		t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
	}
	if got := string(readSettings(t, path)); got != "{}\n" {
		t.Fatalf("tracked file changed: %q", got)
	}
	installRun(t, u, "--client", "codex", "--profile", "p", "--write", "--settings", path)
	if len(settingsCommands(t, readSettings(t, path), "SessionStart")) != 1 {
		t.Fatal("explicit settings did not override guard")
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0640 {
		t.Fatalf("mode=%v err=%v", st, err)
	}
}

func TestHookInstallCodexRefusesSymlinkDefault(t *testing.T) {
	for _, fileLink := range []bool{false, true} {
		t.Run(fmt.Sprint(fileLink), func(t *testing.T) {
			u, root := codexInstallRepo(t)
			target := filepath.Join(t.TempDir(), "hooks.json")
			if err := os.WriteFile(target, []byte("{}\n"), 0640); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, ".codex")
			to := filepath.Dir(target)
			if fileLink {
				if err := os.Mkdir(link, 0755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, "hooks.json")
				to = target
			}
			if err := os.Symlink(to, link); err != nil {
				t.Fatal(err)
			}
			code, out, errs := u.run("hook", "install", "--client", "codex", "--profile", "p", "--write")
			if code != 1 || out != "" || !strings.Contains(errs, "the project .codex/hooks.json") {
				t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
			}
			if got, err := os.Readlink(link); err != nil || got != to {
				t.Fatalf("link=%q err=%v", got, err)
			}
			if string(readSettings(t, target)) != "{}\n" {
				t.Fatal("symlink target changed")
			}
		})
	}
}

func TestHookInstallCodexPreservesForeign(t *testing.T) {
	u := installUser(t)
	const before = `{
  "description": "personal \u0068ooks",
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume",
        "hooks": [
          {
            "type": "command",
            "command": "c2c hook"
          }
        ]
      }
    ],
    "UserPromptSubmit": [
    ],
    "PostToolUse": [
    ],
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "dcg"
          }
        ]
      }
    ]
  }
}
`
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	installRun(t, u, "--client", "codex", "--profile", "p", "--write", "--settings", path)
	after := string(readSettings(t, path))
	b, a := decodeSettings(t, []byte(before)), decodeSettings(t, []byte(after))
	bh, ah := b["hooks"].(map[string]any), a["hooks"].(map[string]any)
	if !reflect.DeepEqual(b["description"], a["description"]) || !reflect.DeepEqual(bh["PreToolUse"], ah["PreToolUse"]) || !reflect.DeepEqual(bh["SessionStart"].([]any)[0], ah["SessionStart"].([]any)[0]) {
		t.Fatal("foreign keys or groups changed")
	}
	// Every original line survives in order; additions may only put a
	// comma after an existing closing bracket/brace.
	lines := strings.Split(after, "\n")
	next, commas := 0, 0
	for _, line := range strings.Split(before, "\n") {
		found := false
		for next < len(lines) {
			got := lines[next]
			next++
			if got == line {
				found = true
				break
			}
			if got == line+"," {
				commas++
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("original line removed/reordered: %q\n%s", line, after)
		}
	}
	if commas != 1 {
		t.Fatalf("changed %d commas, want one", commas)
	}
	_, errs := installRun(t, u, "--client", "codex", "--profile", "q", "--write", "--settings", path)
	for _, want := range []string{"VECNET_PROFILE=q codex", "Session binding", "vecnet hook rebind --profile P"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("missing %q: %q", want, errs)
		}
	}
	for event, name := range map[string]string{"SessionStart": "session-start", "UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
		if got := settingsCommands(t, readSettings(t, path), event); !reflect.DeepEqual(got, []string{"vecnet hook --profile p --event " + name, "vecnet hook --profile q --event " + name}) {
			t.Fatalf("%s commands=%v", event, got)
		}
	}
}

func TestHookInstallRejectsUnknownClient(t *testing.T) {
	u, root := codexInstallRepo(t)
	path := filepath.Join(root, "missing.json")
	code, out, errs := u.run("hook", "install", "--client", "cursor", "--profile", "p", "--write", "--settings", path)
	if code != 1 || out != "" || !strings.Contains(errs, "hook install: --client must be claude or codex") {
		t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("file written: %v", err)
	}
}

func TestHookInstallCodexRejectsMCP(t *testing.T) {
	u, root := codexInstallRepo(t)
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "client-ran")
	for _, client := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(binDir, client), []byte("#!/bin/sh\n: > '"+marker+"'\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(root, "absent", "hooks.json")
	for _, write := range []bool{false, true} {
		args := []string{"hook", "install", "--client", "codex", "--mcp", "--profile", "p", "--settings", path}
		if write {
			args = append(args, "--write")
		}
		code, out, errs := u.run(args...)
		if code != 1 || out != "" || !strings.Contains(errs, "hook install: --mcp registration is supported only for --client claude") {
			t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
		}
	}
	for _, path := range []string{marker, filepath.Dir(path)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("file/client side effect at %s: %v", path, err)
		}
	}
}

func TestHookInstallCodexIgnoresClaudeProjectDir(t *testing.T) {
	for _, projectExists := range []bool{false, true} {
		t.Run(fmt.Sprint(projectExists), func(t *testing.T) {
			u, root := codexInstallRepo(t)
			other := filepath.Join(t.TempDir(), "claude-project")
			if projectExists {
				gitT(t, "init", "--quiet", other)
			}
			u.env["CLAUDE_PROJECT_DIR"] = other
			_, errs := installRun(t, u, "--client", "codex", "--write")
			readSettings(t, filepath.Join(root, ".codex", "hooks.json"))
			if strings.Contains(errs, "CLAUDE_PROJECT_DIR") || strings.Contains(errs, "Claude Code") {
				t.Fatalf("Claude advice leaked: %q", errs)
			}
			for _, path := range []string{filepath.Join(other, ".codex"), filepath.Join(other, ".claude")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("Claude project touched: %v", err)
				}
			}
		})
	}
}

func TestHookInstallCodexRefusesUnknownTrackingStatus(t *testing.T) {
	for _, exit := range []string{"0", "2", "128"} {
		t.Run(exit, func(t *testing.T) {
			u, root := codexInstallRepo(t)
			binDir := t.TempDir()
			// Git's process boundary: successful discovery, then tracked (0)
			// or unknown (2/128) status, all of which must refuse writes.
			stub := "#!/bin/sh\ncase \"$3\" in\nrev-parse) printf '%s\\n' \"$2\";;\nls-files) exit " + exit + ";;\n*) exit 99;;\nesac\n"
			if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			path := filepath.Join(root, ".codex", "hooks.json")
			code, out, errs := u.run("hook", "install", "--client", "codex", "--profile", "p", "--write")
			if code != 1 || out != "" || !strings.Contains(errs, "tracked by git (or its status is unknown)") {
				t.Fatalf("exit=%d out=%q errs=%q", code, out, errs)
			}
			if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("unknown-status target written: %v", err)
			}
		})
	}
}

func TestHookInstallCodexFallbackToCwd(t *testing.T) {
	isolateGitEnv(t)
	u := installUser(t)
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput(); err == nil {
		t.Fatalf("private cwd is inside git: %s", out)
	}
	_, errs := installRun(t, u, "--client", "codex", "--write")
	readSettings(t, filepath.Join(root, ".codex", "hooks.json"))
	if !strings.Contains(errs, "cwd fallback; Codex project hooks") {
		t.Fatalf("missing fallback reason: %q", errs)
	}
}
