package cli_test

// The delivery host's Claude adapter rebaselines a scope when the hub
// refuses its stored cursor itself, and keys that on the CLI's exact stderr
// for `--json inbox --since <cursor>` (claude-adapter.ts CURSOR_REJECTED).
// This pins that text and exit code against a real loopback hub, and against
// the shared fixture the TS stub reproduces, so the two can never drift.

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/XertroV/vecnet/internal/testhub"
)

type cursorRefusalFixture struct {
	Exit      int    `json:"exit"`
	Purged    string `json:"purged"`
	Malformed string `json:"malformed"`
}

func TestInboxCursorRefusalStderr(t *testing.T) {
	raw, err := os.ReadFile("../../connectors/launcher/test/fixtures/cli-cursor-errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var want cursorRefusalFixture
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	env := testhub.Start(t)
	_, secret := env.NewPeer("session", "cursor-refusal")
	u := profileUser(t, env.URL, secret)
	// A working read gives this hub's real cursor shape and epoch.
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(u.mustRun("--json", "inbox", "--since", "head")), &page); err != nil || page.NextCursor == "" {
		t.Fatalf("head read: %q (%v)", page.NextCursor, err)
	}
	for name, c := range map[string]struct{ cursor, stderr string }{
		// A cursor from another epoch is what a rebuilt hub log leaves behind.
		"foreign epoch": {"e999999.0", want.Purged},
		"malformed":     {"not-a-cursor", want.Malformed},
	} {
		code, out, errs := u.run("--json", "inbox", "--since", c.cursor)
		if code != want.Exit || errs != c.stderr || out != "" {
			t.Fatalf("%s: exit %d stdout %q stderr %q; want exit %d stderr %q", name, code, out, errs, want.Exit, c.stderr)
		}
	}
}
