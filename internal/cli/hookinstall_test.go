package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
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

// Execute installed argv through the public CLI, including global flag parsing.
func TestHookInstallFlagProfilesExecuteHook(t *testing.T) {
	for _, profile := range []string{"-h", "--help"} {
		for _, mode := range []string{"explicit", "default"} {
			for _, event := range []string{"UserPromptSubmit", "PostToolUse"} {
				t.Run(profile+"/"+mode+"/"+event, func(t *testing.T) {
					var polled atomic.Bool
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/v1/events" || r.URL.Query().Get("wait") != "0" {
							t.Errorf("unexpected hook request: %s", r.URL)
						}
						polled.Store(true)
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"events":[{"cursor":"e1.1","type":"delivery","data":{"delivery_id":"dly_1","message":{"message_id":"msg_1","thread_id":"thr_1","sender":"peer_1","content":{"text":"hello"}}}}],"next_cursor":"e1.1"}`))
					}))
					defer srv.Close()
					u := installUser(t)
					defaultProfile := profile
					if mode == "explicit" {
						defaultProfile = "p"
					}
					body := fmt.Sprintf("default = %q\n[profiles.%s]\nhub = %q\nsecret = %q\n", defaultProfile, profile, srv.URL, "vn_peer_unused")
					if mode == "explicit" {
						body += "[profiles.p]\nhub = \"http://127.0.0.1:1\"\nsecret = \"vn_peer_unused\"\n"
					}
					if err := os.WriteFile(u.conf, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
					args := []string{"--settings", filepath.Join(t.TempDir(), "settings.local.json")}
					if mode == "explicit" {
						args = append(args, "--profile="+profile)
					}
					stanza, _ := installRun(t, u, args...)
					var settings struct {
						Hooks map[string][]struct {
							Hooks []struct{ Command string }
						}
					}
					if err := json.Unmarshal([]byte(stanza), &settings); err != nil {
						t.Fatal(err)
					}
					command := settings.Hooks[event][0].Hooks[0].Command
					code, out, errs := u.run(strings.Fields(command)[1:]...)
					var result struct {
						HookSpecificOutput struct{ HookEventName, AdditionalContext string }
					}
					if code != 0 || json.Unmarshal([]byte(out), &result) != nil || result.HookSpecificOutput.HookEventName != event || !strings.Contains(result.HookSpecificOutput.AdditionalContext, "profile "+profile) {
						t.Fatalf("command %q: exit=%d stdout=%q stderr=%q", command, code, out, errs)
					}
					if !polled.Load() {
						t.Fatal("installed command did not poll")
					}
				})
			}
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

func TestHookInstallFlagProfilesReplaceExisting(t *testing.T) {
	for _, profile := range []string{"-h", "--help"} {
		for _, separator := range []string{" ", "="} {
			t.Run(profile+separator, func(t *testing.T) {
				u := installUser(t)
				before := fmt.Sprintf(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":%q,"timeout":5}]}],"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":%q,"timeout":5}]}]}}`,
					"vecnet hook --profile"+separator+profile+" --event user-prompt",
					"vecnet hook --profile"+separator+profile+" --event post-tool")
				path := installFile(t, []byte(before), 0644)
				args := []string{"--profile=" + profile, "--write", "--settings", path}
				installRun(t, u, args...)
				first := string(readSettings(t, path))
				_, errs := installRun(t, u, args...)
				if !strings.Contains(errs, "unchanged "+path) || string(readSettings(t, path)) != first {
					t.Fatalf("flag profile not idempotent: %q", errs)
				}
				installRun(t, u, append(args, "--auto-ack")...)
				hooks := decodeSettings(t, readSettings(t, path))["hooks"].(map[string]any)
				for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
					groups := hooks[event].([]any)
					if len(groups) != 1 {
						t.Fatalf("%s groups=%v", event, groups)
					}
					handlers := groups[0].(map[string]any)["hooks"].([]any)
					want := "vecnet hook --profile=" + profile + " --event " + suffix + " --auto-ack"
					if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != want {
						t.Fatalf("%s handlers=%v", event, handlers)
					}
				}
			})
		}
	}
}

func TestHookInstallRenamedBinaryIdempotent(t *testing.T) {
	for _, tc := range []struct{ bin, shellBin string }{
		{"/opt/bin/vn", "/opt/bin/vn"},
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
	u := installUser(t)
	root := t.TempDir()
	t.Chdir(root)
	_, errs := installRun(t, u, "--write", "--profile", "p")
	path := filepath.Join(root, ".claude", "settings.local.json")
	if len(settingsCommands(t, readSettings(t, path), "PostToolUse")) != 1 {
		t.Fatal("missing post-tool hook")
	}
	if !strings.Contains(errs, "not in a git repository; using "+root) || !strings.Contains(errs, path) {
		t.Fatalf("missing fallback reason: %q", errs)
	}
	st, err := os.Stat(filepath.Dir(path))
	if err != nil || st.Mode().Perm() != 0755 {
		t.Fatalf("directory mode=%v err=%v", st, err)
	}
}

func TestHookInstallReplacesSupportedExtraFlags(t *testing.T) {
	const peer = "peer_00000000000000000000000000"
	for _, tc := range []struct{ name, command string }{
		{"as suffix", "vecnet hook --profile p --event %s --as " + peer},
		{"as prefix", "vecnet --as=" + peer + " hook --profile p --event %s"},
		{"as interleaved", "vecnet hook --profile p --as " + peer + " --event %s"},
		{"repeated boolean", "vecnet hook --profile p --event %s --auto-ack --auto-ack"},
		{"reordered flags", "vecnet hook --auto-ack --event=%s --as=" + peer + " --profile=p --auto-ack"},
		{"quoted arguments", "'vecnet' hook --profile 'p' --event '%s' --as '" + peer + "'"},
		{"global flags", "vecnet --json --net=other hook --profile p --event %s --json"},
		{"last profile wins", "vecnet hook --profile other --event %s --profile p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := installUser(t)
			seed := hookInstallExtraFlagSeed(t, tc.command)
			path := installFile(t, seed, 0640)
			args := []string{"--profile", "p", "--write", "--settings", path}
			for _, autoAck := range []bool{false, true} {
				runArgs := append([]string(nil), args...)
				if autoAck {
					runArgs = append(runArgs, "--auto-ack")
				}
				installRun(t, u, runArgs...)
				body := readSettings(t, path)
				hooks := decodeSettings(t, body)["hooks"].(map[string]any)
				original := decodeSettings(t, seed)["hooks"].(map[string]any)
				for event, suffix := range map[string]string{"UserPromptSubmit": "user-prompt", "PostToolUse": "post-tool"} {
					groups := hooks[event].([]any)
					if len(groups) != 1 {
						t.Fatalf("%s: appended a duplicate group: %v", event, groups)
					}
					group := groups[0].(map[string]any)
					handlers := group["hooks"].([]any)
					oldGroup := original[event].([]any)[0].(map[string]any)
					oldHandlers := oldGroup["hooks"].([]any)
					if len(handlers) != 3 || !reflect.DeepEqual(handlers[0], oldHandlers[0]) || !reflect.DeepEqual(handlers[2], oldHandlers[2]) || group["extra"] != oldGroup["extra"] {
						t.Fatalf("%s: foreign handlers or group metadata changed: %v", event, group)
					}
					want := "vecnet hook --profile p --event " + suffix
					if autoAck {
						want += " --auto-ack"
					}
					handler := handlers[1].(map[string]any)
					if handler["command"] != want || handler["type"] != "command" || handler["timeout"] != float64(5) {
						t.Fatalf("%s: handler was not replaced in place: %v", event, handler)
					}
				}
				_, errs := installRun(t, u, runArgs...)
				if string(readSettings(t, path)) != string(body) || !strings.Contains(errs, "unchanged "+path) {
					t.Fatalf("repeat install changed settings: %s", errs)
				}
			}
		})
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
