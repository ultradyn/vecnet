package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexLegacyHookReservesRecipient(t *testing.T) {
	for _, profile := range []string{"p", "q"} {
		for _, expired := range []bool{false, true} {
			t.Run(profile+"/"+map[bool]string{false: "live", true: "expired"}[expired], func(t *testing.T) {
				env, state, polls := sessionHub(t)
				delete(env, "VECNET_PROFILE")
				seedHook(t, state, "e1.0")
				before, _ := os.ReadFile(state)
				path, err := codexBindingsPath(func(k string) string { return env[k] })
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := loadConfig(env["VECNET_CONFIG"])
				if err != nil {
					t.Fatal(err)
				}
				_, p, err := cfg.profile("p")
				if err != nil {
					t.Fatal(err)
				}
				expires := time.Now().Add(time.Hour)
				if expired {
					expires = time.Now().Add(-time.Hour)
				}
				file := codexBindingsFile{Version: 1, Bindings: []codexBinding{{ConfigPath: env["VECNET_CONFIG"], Profile: "p", Hub: p.Hub, Caller: "peer_test", Generation: appHookGeneration, CodexHome: env["HOME"], ThreadID: bindT1, CreatedAt: expires.Add(-24 * time.Hour), ExpiresAt: expires}}}
				raw, _ := json.Marshal(file)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				out, _ := runHookAs(t, env, "unregistered", profile)
				after, _ := os.ReadFile(state)
				if out != "" || polls.Load() != 0 || !bytes.Equal(before, after) {
					t.Fatalf("legacy hook consumed Codex recipient: output=%q polls=%d", out, polls.Load())
				}
			})
		}
	}
}

func TestCodexLegacyHookLocksBeforeFirstBinding(t *testing.T) {
	env, _, _ := sessionHub(t)
	a := &app{ctx: context.Background(), getenv: func(k string) string { return env[k] }, net: "p"}
	c, _, _, err := a.connect()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := a.lockLegacyHookRecipient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	path, _ := codexBindingsPath(a.getenv)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if other, err := lockPath(ctx, path+".lock"); err == nil {
		other()
		t.Fatal("first bind can race a running legacy hook")
	}
}

func TestCodexLegacyHookCorruptAuthorityFailsClosed(t *testing.T) {
	env, state, polls := sessionHub(t)
	seedHook(t, state, "e1.0")
	before, _ := os.ReadFile(state)
	path, _ := codexBindingsPath(func(k string) string { return env[k] })
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	out, _ := runHookAs(t, env, "S1", "p")
	after, _ := os.ReadFile(state)
	if out != "" || polls.Load() != 0 || !bytes.Equal(before, after) {
		t.Fatalf("corrupt authority allowed legacy delivery: %q", out)
	}
}
