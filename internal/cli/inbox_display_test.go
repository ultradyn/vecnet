package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func runDisplayInbox(t *testing.T, env map[string]string, output io.Writer, flags ...string) (int, string) {
	t.Helper()
	var errs bytes.Buffer
	args := append([]string{"--net", "p"}, flags...)
	code := RunContext(context.Background(), args, strings.NewReader(""), output, &errs, func(k string) string { return env[k] })
	return code, errs.String()
}

func TestInboxFailedOutputPreservesReplayAndAck(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/short=%t", format, short), func(t *testing.T) {
				f := newDisplayFixture(t)
				f.delivery()
				runHook(t, f.env, "--event", "app-server", "--claim", "busy", "--priorities", "wake", "--expect-hub", f.hub, "--expect-caller", "peer_test")
				before, _ := os.ReadFile(f.path)
				flags := []string{"inbox", "--ack"}
				if format == "json" {
					flags = append([]string{"--json"}, flags...)
				}
				writer := &failedDisplay{short: short, tried: make(chan struct{})}
				if code, stderr := runDisplayInbox(t, f.env, writer, flags...); code == 0 {
					t.Fatalf("failed stdout reported success: %s", stderr)
				}
				after, _ := os.ReadFile(f.path)
				if !bytes.Equal(before, after) {
					t.Fatal("failed/partial output changed shared proof")
				}
				select {
				case cursor := <-f.acks:
					t.Fatalf("failed output acknowledged %s", cursor)
				default:
				}
				out, _ := runHook(t, f.env, "--event", "app-server", "--claim", "fresh", "--priorities", "wake,wake_idle", "--expect-hub", f.hub, "--expect-caller", "peer_test")
				if !strings.Contains(out, "dly_1") {
					t.Fatal("failed output lost deferred delivery")
				}
			})
		}
	}
}

func TestInboxDisplayRequiresFreshIdentity(t *testing.T) {
	for _, identity := range []string{`{"kind":"peer","peer_id":"peer_other"}`, `{}`, `{"kind":"operator"}`, "outage"} {
		t.Run(identity, func(t *testing.T) {
			f := newDisplayFixture(t)
			f.delivery()
			runHook(t, f.env, "--event", "app-server", "--claim", "busy", "--priorities", "wake", "--expect-hub", f.hub, "--expect-caller", "peer_test")
			before, _ := os.ReadFile(f.path)
			setDisplayIdentity(f, identity)
			f.unverified.Store(identity == "outage")
			var out bytes.Buffer
			if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 || !strings.Contains(out.String(), "live delivery") {
				t.Fatalf("ordinary output unavailable: %d %s", code, stderr)
			}
			after, _ := os.ReadFile(f.path)
			if !bytes.Equal(before, after) {
				t.Fatal("cached identity authorized proof despite fresh identity mismatch")
			}
		})
	}
}

type inboxOutputFunc func([]byte) (int, error)

func (f inboxOutputFunc) Write(p []byte) (int, error) { return f(p) }

func TestInboxCommitFailureNeverRetriesOutputOrAcknowledges(t *testing.T) {
	f := newDisplayFixture(t)
	f.delivery()
	runHook(t, f.env, "--event", "app-server", "--claim", "busy", "--priorities", "wake", "--expect-hub", f.hub, "--expect-caller", "peer_test")
	before, _ := os.ReadFile(f.path)
	var output bytes.Buffer
	writes := 0
	writer := inboxOutputFunc(func(p []byte) (int, error) {
		writes++
		if writes == 1 {
			// Make the atomic proof rename fail after successful stdout. Keep
			// the original bytes separately so this is not a lost-output test.
			if err := os.Rename(f.path, f.path+".saved"); err != nil {
				return 0, err
			}
			if err := os.Mkdir(f.path, 0700); err != nil {
				return 0, err
			}
		}
		return output.Write(p)
	})
	code, stderr := runDisplayInbox(t, f.env, writer, "--json", "inbox", "--ack")
	if code == 0 || !strings.Contains(stderr, "display state commit failed") || writes != 1 || strings.Count(output.String(), "live delivery") != 1 {
		t.Fatalf("commit failure retried/lost output or reported success: code=%d writes=%d stderr=%s", code, writes, stderr)
	}
	select {
	case cursor := <-f.acks:
		t.Fatalf("ack %s crossed failed proof commit", cursor)
	default:
	}
	if saved, err := os.ReadFile(f.path + ".saved"); err != nil || !bytes.Equal(saved, before) {
		t.Fatal("failed commit manufactured proof")
	}
}

func TestInboxDisplayBusyOrUnwritableStateStillPrints(t *testing.T) {
	for _, block := range []string{"busy", "unwritable-lock"} {
		t.Run(block, func(t *testing.T) {
			f := newAppHookFixture(t)
			f.add(t, "wake_idle", "ordinary output", "")
			f.run(t, "busy", "wake")
			before := f.bytes(t)
			if block == "busy" {
				unlock, err := lockPath(t.Context(), f.path+".lock")
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			} else {
				if err := os.Remove(f.path + ".lock"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.path+".lock", 0700); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			start := time.Now()
			if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 || !strings.Contains(out.String(), "ordinary output") {
				t.Fatalf("state blocked inbox: %d %s", code, stderr)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("state fallback was not bounded")
			}
			if !bytes.Equal(before, f.bytes(t)) {
				t.Fatal("unavailable state changed")
			}
		})
	}
}

func TestInboxExplicitReplayPreservesCursorsClaimsAndBinding(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "manual replay", "")
	f.run(t, "busy", "wake")
	file := stateFile{f.path}
	if err := file.update(t.Context(), func(s *hookState) error {
		s.PollCursor, s.AckAfter = "opaque-poll", "opaque-fence"
		s.Claims = append(s.Claims, claimRef{ID: "dly_pending", Claim: "pending", After: "opaque-before", Priority: "wake_idle", Order: 19})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := readHookState(t, f.path)
	// Keep an exact existing session sidecar; inbox proof must not rewrite it.
	binding := []byte("{\n  \"session\":\"owned-session\", \"session_how\":\"declared\", \"future_field\":true\n}\n")
	if err := os.WriteFile(bindingPath(f.path), binding, 0600); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(f.bytes(t), &fields); err != nil {
		t.Fatal(err)
	}
	fields["future_field"] = json.RawMessage(`{"cursor":"opaque-legacy","enabled":true}`)
	legacy, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox", "--since", "e1.0"); code != 0 || !strings.Contains(out.String(), "manual replay") {
			t.Fatalf("explicit replay suppressed: %d %s", code, stderr)
		}
	}
	after := readHookState(t, f.path)
	if after.AppCursor != before.AppCursor || after.PollCursor != before.PollCursor || after.AckAfter != before.AckAfter || !reflect.DeepEqual(after.Claims, before.Claims) || !slices.Equal(after.Displayed, []string{"dly_1"}) || len(after.Replay) != 0 {
		t.Fatalf("proof changed unrelated ownership/cursors: %+v", after)
	}
	if got, err := os.ReadFile(bindingPath(f.path)); err != nil || !bytes.Equal(got, binding) {
		t.Fatal("session sidecar changed")
	}
	var preserved map[string]json.RawMessage
	if err := json.Unmarshal(f.bytes(t), &preserved); err != nil || !bytes.Equal(fields["future_field"], preserved["future_field"]) {
		t.Fatal("unknown state field changed")
	}
	f.add(t, "wake", "fresh arrival", "")
	got, _, _ := f.run(t, "after-restart", "wake,wake_idle")
	appHookIDs(t, got, "dly_2")
}

type blockingInboxOutput struct {
	entered, resume chan struct{}
	bytes.Buffer
}

func (w *blockingInboxOutput) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.resume
	return w.Buffer.Write(p)
}

func TestInboxDisplaySerializesOutputWithClaims(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "serialized output", "")
	f.run(t, "busy", "wake")
	out := &blockingInboxOutput{entered: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan int, 1)
	go func() { code, _ := runDisplayInbox(t, f.env, out, "--json", "inbox"); done <- code }()
	select {
	case <-out.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("inbox never attempted output")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	unlock, err := lockPath(ctx, f.path+".lock")
	cancel()
	if err == nil {
		unlock()
	}
	close(out.resume)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("inbox failed: %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inbox did not finish")
	}
	if err == nil {
		t.Fatal("app-server ownership lock was available during stdout")
	}
	got, _, _ := f.run(t, "fresh-hook", "wake,wake_idle")
	appHookIDs(t, got)
}

func TestInboxConsumesDeferredAppServerReplay(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			f := newAppHookFixture(t)
			f.add(t, "wake_idle", "read first", "")
			f.add(t, "wake_idle", "keep unread", "")
			// Opaque cursor values deliberately disagree with lexical ordering.
			f.events[0].Cursor, f.events[1].Cursor = "opaque-z", "opaque-a"
			got, _, _ := f.run(t, "busy", "wake")
			appHookIDs(t, got)
			if len(f.state(t).Replay) != 2 {
				t.Fatal("both idle deliveries must actually enter deferred replay")
			}
			flags := []string{"inbox", "--since", "e1.0", "--limit", "1"}
			if format == "json" {
				flags = append([]string{"--json"}, flags...)
			}
			var out bytes.Buffer
			if code, stderr := runDisplayInbox(t, f.env, &out, flags...); code != 0 {
				t.Fatalf("manual inbox failed: %d %s", code, stderr)
			}
			if !strings.Contains(out.String(), "read first") || strings.Contains(out.String(), "keep unread") {
				t.Fatalf("manual output did not cover exactly one delivery: %s", out.String())
			}
			// A fresh CLI/hook instance must not reinject the item just printed.
			got, _, _ = f.run(t, "idle", "wake,wake_idle")
			appHookIDs(t, got, "dly_2")
			s := readHookState(t, f.path)
			if !slices.Equal(s.Displayed, []string{"dly_1"}) || len(s.Claims) != 1 || s.Claims[0].ID != "dly_2" || s.AppCursor != "opaque-a" {
				t.Fatalf("manual consumption lost unread work or moved opaque cursors: %+v", s)
			}
		})
	}
}

func TestInboxDisplayPreservesPendingAppServerClaim(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "pending claim", "")
	got, _, _ := f.run(t, "accepted-or-ambiguous", "wake,wake_idle")
	appHookIDs(t, got, "dly_1")
	before := readHookState(t, f.path)
	var out bytes.Buffer
	if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox", "--since", "e1.0"); code != 0 {
		t.Fatalf("manual inbox failed: %d %s", code, stderr)
	}
	after := readHookState(t, f.path)
	if !reflect.DeepEqual(before.Claims, after.Claims) || len(after.Displayed) != 0 || !slices.Equal(before.Shown, after.Shown) {
		t.Fatal("manual output must not manufacture settlement for a pending claim")
	}
	f.release(t, "accepted-or-ambiguous", 1)
	got, _, _ = f.run(t, "retry-after-proven-release", "wake,wake_idle")
	appHookIDs(t, got, "dly_1")
}

func TestInboxDisplayPreservesUnknownOrForeignState(t *testing.T) {
	for _, mutation := range []string{"hub", "caller", "foreign-caller", "as", "credential", "corrupt", "absent"} {
		t.Run(mutation, func(t *testing.T) {
			f := newAppHookFixture(t)
			f.add(t, "wake_idle", "readable without shared proof", "")
			f.run(t, "busy", "wake")
			s := readHookState(t, f.path)
			switch mutation {
			case "hub":
				s.Hub = "https://foreign.invalid"
			case "caller":
				s.Caller, s.Cred = "", ""
			case "foreign-caller":
				s.Caller = "peer_other"
			case "as":
				s.As = "other"
			case "credential":
				s.Cred = "0000000000000000"
			}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "corrupt" {
				raw = []byte("not JSON")
			}
			if err := os.WriteFile(f.path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if mutation == "absent" {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 || !strings.Contains(out.String(), "readable without shared proof") {
				t.Fatalf("ordinary inbox must remain available: %d %s", code, stderr)
			}
			if mutation == "absent" {
				if _, err := os.Stat(f.path); !os.IsNotExist(err) {
					t.Fatal("ordinary inbox unexpectedly created shared state")
				}
			} else if !bytes.Equal(f.bytes(t), raw) {
				t.Fatal("ordinary inbox rewrote unverified state")
			}
		})
	}
}
