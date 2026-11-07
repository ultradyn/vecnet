package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAutoChildRecursion(t *testing.T) {
	a := &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string {
		if k == "VECNET_CODEX_AUTO_DELIVERY" {
			return "0"
		}
		t.Errorf("disabled automation read environment key %s", k)
		return ""
	}}
	a.autoCodexAction("peers", nil)
}

func TestCodexAutoInput(t *testing.T) {
	for _, invalid := range []string{"thread", "home", "profile", "bare", "config"} {
		t.Run(invalid, func(t *testing.T) {
			f := newBindFixture(t)
			f.env["CODEX_THREAD_ID"] = bindT1
			switch invalid {
			case "thread":
				f.env["CODEX_THREAD_ID"] = strings.ToUpper(bindT1)
			case "home":
				f.env["CODEX_HOME"] = "relative"
			case "profile":
				f.env["VECNET_PROFILE"] = "absent"
			case "bare":
				delete(f.env, "VECNET_PROFILE")
			case "config":
				if err := os.WriteFile(f.env["VECNET_CONFIG"], []byte("bad = ["), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := &app{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return f.env[k] }}
			a.autoCodexAction("peers", nil)
			if _, err := os.Stat(f.path); !os.IsNotExist(err) {
				t.Fatal("invalid automatic input mutated bindings")
			}
		})
	}
}

func TestCodexAutoHome(t *testing.T) {
	f := newBindFixture(t)
	a := &app{ctx: context.Background(), getenv: func(k string) string { return f.env[k] }}
	link := filepath.Join(f.env["HOME"], "linked-home")
	if err := os.Symlink(f.home, link); err != nil {
		t.Fatal(err)
	}
	f.env["CODEX_HOME"] = link
	_, _, home, _, err := a.codexAutoInput(bindT1, "")
	if err != nil || home != f.home {
		t.Fatalf("canonical home=%s error=%v", home, err)
	}
	delete(f.env, "CODEX_HOME")
	fallback := filepath.Join(f.env["HOME"], ".codex")
	if err := os.Mkdir(fallback, 0700); err != nil {
		t.Fatal(err)
	}
	_, _, home, _, err = a.codexAutoInput(bindT1, "")
	if err != nil || home != fallback {
		t.Fatalf("default home=%s error=%v", home, err)
	}
}
