package cli_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/cli"
	"github.com/XertroV/vecnet/internal/testhub"
)

// runOnceWatch starts the CLI like startWatch but exposes its natural exit:
// done receives the exit code without the test cancelling first, so a
// one-shot waiter can be observed stopping on its own.
func runOnceWatch(t *testing.T, u *user, args ...string) (*syncBuffer, *syncBuffer, <-chan int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.RunContext(ctx, args, strings.NewReader(""), &out, &errb, u.getenv)
	}()
	return &out, &errb, done
}

// stillWaiting asserts a one-shot watch has not exited yet.
func stillWaiting(t *testing.T, done <-chan int) {
	t.Helper()
	select {
	case code := <-done:
		t.Fatalf("watch --once exited before its wake delivery (code %d)", code)
	case <-time.After(300 * time.Millisecond):
	}
}

// onceExit waits for a one-shot watch to exit on its own.
func onceExit(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("watch --once did not exit after its wake delivery")
		return -1
	}
}

// TestWatchOnceForMonitorEndsAtFirstWake: hold deliveries and non-delivery
// events are printed but never end a one-shot waiter; the first printed wake
// delivery ends it with exit 0, nothing later is printed, and the backlog is
// left unacked.
func TestWatchOnceForMonitorEndsAtFirstWake(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "Sender")
	bID, bSec := env.NewPeer("session", "Watcher")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	out, errb, done := runOnceWatch(t, b, "watch", "--once", "--for-monitor")

	// A hold delivery is printed but never ends the waiter.
	held := a.send(bID, "held back", "--hold")
	waitLine(t, out, regexp.MustCompile(` delivery `+regexp.QuoteMeta(held.ThreadID)+` `+regexp.QuoteMeta(aID)+` Sender: held back$`), 5*time.Second)
	stillWaiting(t, done)

	// A non-delivery event (enrolment emits directory_updated) never ends it.
	env.NewPeer("session", "Late Join")
	waitLine(t, out, regexp.MustCompile(` directory_updated - -$`), 5*time.Second)
	stillWaiting(t, done)

	// The first printed wake delivery ends the watch with exit 0.
	wake := a.send(bID, "wake up", "--wake")
	waitLine(t, out, regexp.MustCompile(` delivery `+regexp.QuoteMeta(wake.ThreadID)+` `+regexp.QuoteMeta(aID)+` Sender: wake up$`), 5*time.Second)
	if code := onceExit(t, done); code != 0 {
		t.Fatalf("watch --once exit = %d, want 0; stderr: %s", code, errb.String())
	}

	// The waiter stopped consuming: an event after the exit is never printed.
	a.send(bID, "after exit", "--wake")
	if strings.Contains(out.String(), "after exit") {
		t.Fatalf("one-shot watch printed an event after its wake:\n%s", out.String())
	}
	// It never acked: the backlog is still readable afterwards.
	if inbox := b.mustRun("inbox"); !strings.Contains(inbox, "held back") || !strings.Contains(inbox, "wake up") {
		t.Fatalf("one-shot watch acked or lost the backlog:\n%s", inbox)
	}
}

// TestWatchOnceJSONEndsAfterFirstWakeRecord: in --json mode the one-shot
// waiter exits after the first wake/wake_idle delivery record; earlier hold
// records are printed and the output stays one JSON record per line.
func TestWatchOnceJSONEndsAfterFirstWakeRecord(t *testing.T) {
	env := testhub.Start(t)
	_, aSec := env.NewPeer("session", "Sender")
	bID, bSec := env.NewPeer("session", "Watcher")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	out, errb, done := runOnceWatch(t, b, "--json", "watch", "--once")

	a.send(bID, "held record", "--hold")
	waitLine(t, out, regexp.MustCompile(`"type":"delivery"`), 5*time.Second)
	stillWaiting(t, done)

	a.send(bID, "wake idle record", "--wake-idle")
	waitLine(t, out, regexp.MustCompile(regexp.QuoteMeta("wake idle record")), 5*time.Second)
	if code := onceExit(t, done); code != 0 {
		t.Fatalf("watch --json --once exit = %d, want 0; stderr: %s", code, errb.String())
	}

	lines := nonEmptyLines(out.String())
	if len(lines) != 2 {
		t.Fatalf("one-shot JSON watch printed %d lines, want the hold and wake records only:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var ev inboxEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d is not one JSON record: %q (%v)", i, line, err)
		}
		if ev.Data.Message.MessageID == "" {
			t.Fatalf("line %d lost its message: %q", i, line)
		}
	}
	if hold, wake := lines[0], lines[1]; !strings.Contains(hold, `"priority":"hold"`) || !strings.Contains(wake, `"priority":"wake_idle"`) {
		t.Fatalf("one-shot JSON records are not hold then wake_idle:\n%s", out.String())
	}
}

// TestWatchOncePlainEndsAfterWake: plain output mode ends the same way, with
// the inbox header followed by the printed events.
func TestWatchOncePlainEndsAfterWake(t *testing.T) {
	env := testhub.Start(t)
	aID, aSec := env.NewPeer("session", "Sender")
	bID, bSec := env.NewPeer("session", "Watcher")
	a := profileUser(t, env.URL, aSec)
	b := profileUser(t, env.URL, bSec)

	out, errb, done := runOnceWatch(t, b, "watch", "--once")

	const header = "CURSOR TYPE THREAD FROM MSG DELIVERY TEXT"
	waitLine(t, out, regexp.MustCompile(regexp.QuoteMeta(header)), 5*time.Second)

	a.send(bID, "plain held", "--hold")
	waitLine(t, out, regexp.MustCompile(` plain held$`), 5*time.Second)
	stillWaiting(t, done)

	a.send(bID, "plain wake", "--wake")
	waitLine(t, out, regexp.MustCompile(` plain wake$`), 5*time.Second)
	if code := onceExit(t, done); code != 0 {
		t.Fatalf("watch --once (plain) exit = %d, want 0; stderr: %s", code, errb.String())
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 3 || lines[0] != header {
		t.Fatalf("plain one-shot output = %q, want header, hold and wake lines", lines)
	}
	if !strings.Contains(lines[1], aID) || !strings.Contains(lines[2], aID) {
		t.Fatalf("plain one-shot lines lost the sender %s: %q", aID, lines)
	}
}

// TestWatchOnceAutoAckIsUsageError: a one-shot idle waiter must never ack.
func TestWatchOnceAutoAckIsUsageError(t *testing.T) {
	env := testhub.Start(t)
	_, bSec := env.NewPeer("session", "Watcher")
	b := profileUser(t, env.URL, bSec)
	code, _, errs := b.run("watch", "--once", "--auto-ack")
	if code == 0 {
		t.Fatal("watch --once --auto-ack exited 0; want a usage error")
	}
	if !strings.Contains(errs, "--once") || !strings.Contains(errs, "--auto-ack") {
		t.Fatalf("usage error does not explain the conflict: exit %d, stderr %q", code, errs)
	}
}
