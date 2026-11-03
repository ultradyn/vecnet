package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCmdTUINotATerminal(t *testing.T) {
	t.Run("buffer", func(t *testing.T) {
		getenv := tuiTestEnv(t)
		code, out, errs := runTUITest(t, getenv, "tui")
		if code != 1 || out != "" || !strings.Contains(errs, "needs a terminal") {
			t.Fatalf("exit %d stdout %q stderr %q", code, out, errs)
		}
	})
	t.Run("pipe", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		getenv := tuiTestEnv(t)
		var errb bytes.Buffer
		code := Run([]string{"tui"}, strings.NewReader(""), w, &errb, getenv)
		if code != 1 || !strings.Contains(errb.String(), "needs a terminal") {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
	})
	t.Run("a device that is not a terminal", func(t *testing.T) {
		null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer null.Close()
		getenv := tuiTestEnv(t)
		var errb bytes.Buffer
		code := Run([]string{"tui"}, strings.NewReader(""), null, &errb, getenv)
		if code != 1 || !strings.Contains(errb.String(), "needs a terminal") {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
	})
}

func TestCmdTUIUsage(t *testing.T) {
	getenv := tuiTestEnv(t)
	if code, out, errs := runTUITest(t, getenv, "help"); code != 0 || !strings.Contains(out, "\n  tui ") {
		t.Fatalf("exit %d stderr %q; usage lacks tui:\n%s", code, errs, out)
	}
	if code, _, errs := runTUITest(t, getenv, "tui", "extra"); code != 1 || !strings.Contains(errs, "tui") {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
}

// These commands stop at terminal/usage validation, before connecting to a hub.
func tuiTestEnv(t *testing.T) func(string) string {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{
		"VECNET_CONFIG":   filepath.Join(dir, "vecnet.toml"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"),
		"XDG_CACHE_HOME":  filepath.Join(dir, "cache"),
	}
	return func(key string) string { return env[key] }
}

func runTUITest(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &errs, getenv)
	return code, out.String(), errs.String()
}

func TestTUIBeatOverrideEnv(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    time.Duration
		warning string
	}{
		{"", 0, ""},
		{"5s", 5 * time.Second, ""},
		{"20m", 20 * time.Minute, ""},
		{"abc", 0, "ignoring VECNET_TEST_TUI_BEAT=abc: want a duration from 5s to 20m"},
		{"0", 0, "ignoring VECNET_TEST_TUI_BEAT=0: want a duration from 5s to 20m"},
		{"1s", 0, "ignoring VECNET_TEST_TUI_BEAT=1s: want a duration from 5s to 20m"},
		{"4s", 0, "ignoring VECNET_TEST_TUI_BEAT=4s: want a duration from 5s to 20m"},
		{"21m", 0, "ignoring VECNET_TEST_TUI_BEAT=21m: want a duration from 5s to 20m"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			getenv := func(key string) string {
				if key != "VECNET_TEST_TUI_BEAT" {
					t.Fatalf("unexpected environment lookup %q", key)
				}
				return tc.value
			}
			got, warning := tuiBeatOverride(getenv)
			if got != tc.want || warning != tc.warning {
				t.Fatalf("override(%q) = (%v, %q), want (%v, %q)", tc.value, got, warning, tc.want, tc.warning)
			}
		})
	}
}
