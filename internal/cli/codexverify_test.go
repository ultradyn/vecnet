package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexVerifyAuthenticatesWithoutDelivery(t *testing.T) {
	f := newBindFixture(t)
	f.bind("p", bindT1)
	generation := bindValue[string](t, f.entries()[0], "generation")
	args := []string{"verify", "--thread", bindT1, "--codex-home", f.home, "--expect-generation", generation, "--expect-hub", f.hub, "--expect-caller", "peer_A"}
	code, out, stderr := f.run(args...)
	if code != 0 || !strings.Contains(out, `"verified":true`) {
		t.Fatalf("verify: code=%d out=%s stderr=%s", code, out, stderr)
	}
	state, err := statePath(func(k string) string { return f.env[k] }, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("verify created delivery state", err)
	}
	markers, err := filepath.Glob(f.path + ".*.pending")
	if err != nil || len(markers) != 0 {
		t.Fatal("verify created pending send", markers, err)
	}
	f.peer.Store("peer_replaced")
	code, out, _ = f.run(args...)
	if code == 0 || strings.Contains(out, `"verified":true`) {
		t.Fatal("replacement identity verified")
	}
}
