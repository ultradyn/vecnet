package cli_test

import (
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

func TestPeerRenameUsage(t *testing.T) {
	u := newUser(t)
	for _, args := range [][]string{
		{"peer", "rename"},
		{"peer", "rename", "peer_x"},
		{"peer", "rename", "peer_x", "new-name", "extra"},
	} {
		code, _, errs := u.run(args...)
		if code != 1 || !strings.Contains(errs, "peer rename") {
			t.Errorf("vecnet %s: exit %d stderr %q, want 1 containing peer rename", strings.Join(args, " "), code, errs)
		}
	}
	out := u.mustRun("--help")
	if !strings.Contains(out, "peer rename <id> <name>") {
		t.Fatalf("help lacks peer rename:\n%s", out)
	}
}

func TestPeerRenameCommand(t *testing.T) {
	env := testhub.Start(t)
	op := profileUser(t, env.URL, env.OperatorSecret)
	alice, aliceSec := env.NewPeer("grokbot", "grok-smoke")
	_, _ = env.NewPeer("session", "taken")

	if got := op.mustRun("peer", "rename", alice, "xpp-cos"); got != "renamed "+alice+" to xpp-cos\n" {
		t.Fatalf("peer rename = %q", got)
	}
	var name string
	if err := env.Hub.DB.QueryRow(`SELECT name FROM peers WHERE peer_id = ?`, alice).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "xpp-cos" {
		t.Fatalf("name = %q, want xpp-cos", name)
	}
	if got := op.mustRun("--json", "peer", "rename", alice, "xpp-cos"); strings.TrimSpace(got) != `{"ok":true}` {
		t.Fatalf("peer rename --json no-op = %q", got)
	}
	if got := op.mustRun("peer", "rename", alice, "web claude"); got != "renamed "+alice+" to web claude\n" {
		t.Fatalf("peer rename spaced = %q", got)
	}

	code, _, errs := op.run("peer", "rename", alice, "max")
	if code != 2 || !strings.HasPrefix(errs, "name_reserved: ") {
		t.Fatalf("reserved = %d %q, want 2 name_reserved", code, errs)
	}
	code, _, errs = op.run("peer", "rename", alice, "taken")
	if code != 2 || !strings.HasPrefix(errs, "name_taken: ") {
		t.Fatalf("taken = %d %q, want 2 name_taken", code, errs)
	}
	code, _, errs = op.run("peer", "rename", "peer_missing", "someone")
	if code != 2 || !strings.HasPrefix(errs, "not_found: ") {
		t.Fatalf("missing = %d %q, want 2 not_found", code, errs)
	}

	peer := profileUser(t, env.URL, aliceSec)
	code, _, errs = peer.run("peer", "rename", alice, "other")
	if code != 2 || !strings.HasPrefix(errs, "forbidden: ") {
		t.Fatalf("peer credential = %d %q, want 2 forbidden", code, errs)
	}
	if err := env.Hub.DB.QueryRow(`SELECT name FROM peers WHERE peer_id = ?`, alice).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "web claude" {
		t.Fatalf("name after refusals = %q, want web claude", name)
	}
}
