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
		for _, mode := range []string{"explicit", "default"} {
			t.Run(profile+"/"+mode, func(t *testing.T) {
				u := installUser(t)
				body := fmt.Sprintf("default = %q\n[profiles.%s]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_peer_unused\"\n", profile, profile)
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

func TestHookInstallDefaultsToLocalAtGitRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
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
	_, errs := installRun(t, u, "--write") // no --profile: resolved config default
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
	// Git discovery uses the process environment, not the CLI user's getenv.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES"} {
		t.Setenv(key, "") // Restore the inherited value when the test finishes.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
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
