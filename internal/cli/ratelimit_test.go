package cli_test

import (
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

// TestSendRateLimitMessage: CLI stderr is the hub's self-explaining 429, so
// an agent reading the command output can tell a send cap from a new-thread
// cap (PROTOCOL §16.2).
func TestSendRateLimitMessage(t *testing.T) {
	t.Run("sends_per_min", func(t *testing.T) {
		env := testhub.Start(t)
		aID, aSec := env.NewPeer("session", "A")
		bID, _ := env.NewPeer("session", "B")
		env.SetLimits(aID, 1, 100)
		a := profileUser(t, env.URL, aSec)
		a.mustRun("send", bID, "one", "--hold")
		env.Now.Advance(19 * time.Second)

		code, out, errs := a.run("send", bID, "two", "--hold")
		want := "rate_limited: per-peer rate limit exceeded: sends_per_min 1 (sends in the last minute); retry in 41s\n"
		if code != 2 || errs != want {
			t.Fatalf("send over the minute cap: exit %d stdout %q stderr %q, want 2 and %q", code, out, errs, want)
		}
	})

	t.Run("threads_per_hour", func(t *testing.T) {
		env := testhub.Start(t)
		cID, cSec := env.NewPeer("session", "C")
		bID, _ := env.NewPeer("session", "B")
		env.SetLimits(cID, 100, 2)
		c := profileUser(t, env.URL, cSec)
		c.mustRun("send", bID, "thread one", "--hold")
		c.mustRun("send", bID, "thread two", "--hold")
		env.Now.Advance(19 * time.Minute)

		code, out, errs := c.run("send", bID, "thread three", "--hold")
		want := "rate_limited: per-peer rate limit exceeded: threads_per_hour 2 (new threads in the last hour); retry in 41m\n"
		if code != 2 || errs != want {
			t.Fatalf("send over the thread cap: exit %d stdout %q stderr %q, want 2 and %q", code, out, errs, want)
		}
	})
}
