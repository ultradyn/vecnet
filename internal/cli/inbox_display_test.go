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

func TestInboxDisplayPreservesUnreadReplayMetadata(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, printedInReplay := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/printed-in-replay=%t", format, printedInReplay), func(t *testing.T) {
				f := newAppHookFixture(t)
				for _, body := range []string{"printed first", "unread second", "unread third"} {
					f.add(t, "wake_idle", body, "")
				}
				f.run(t, "busy", "wake")
				var before map[string]json.RawMessage
				if err := json.Unmarshal(f.bytes(t), &before); err != nil {
					t.Fatal(err)
				}
				var refs []json.RawMessage
				if err := json.Unmarshal(before["replay"], &refs); err != nil {
					t.Fatal(err)
				}
				if len(refs) != 3 {
					t.Fatal("fixture did not defer three deliveries")
				}
				for n := range refs {
					var ref map[string]json.RawMessage
					if err := json.Unmarshal(refs[n], &ref); err != nil {
						t.Fatal(err)
					}
					ref["future_field"] = json.RawMessage(fmt.Sprintf(`{"nested":{"keep":true,"ordinal":%d},"values":[0,"fixture",false]}`, n))
					refs[n], _ = json.Marshal(ref)
				}
				wantReplay := refs[1:]
				if !printedInReplay {
					refs = refs[1:]
				}
				before["replay"], _ = json.Marshal(refs)
				before["claims"] = json.RawMessage(`[{"ID":"dly_pending","After":"opaque-before","Priority":"wake_idle","Claim":"ambiguous","Generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Accepted":true,"Order":19,"future_claim":{"keep":true}}]`)
				before["poll_cursor"] = json.RawMessage(`"opaque-poll"`)
				before["ack_after"] = json.RawMessage(`"opaque-fence"`)
				before["future_top"] = json.RawMessage(`{"keep":[true,0]}`)
				raw, err := json.Marshal(before)
				if err != nil {
					t.Fatal(err)
				}
				if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				binding := []byte("{\n \"session\":\"fixture-owned\", \"future_field\":true\n}\n")
				if err := cliFixtureWriteFile(bindingPath(f.path), binding, 0600); err != nil {
					t.Fatal(err)
				}
				flags := []string{"inbox", "--since", "e1.0", "--limit", "1"}
				if format == "json" {
					flags = append([]string{"--json"}, flags...)
				}
				var output bytes.Buffer
				if code, stderr := runDisplayInbox(t, f.env, &output, flags...); code != 0 || !strings.Contains(output.String(), "printed first") || strings.Contains(output.String(), "unread second") {
					t.Fatalf("one-delivery inbox failed: %d %s %s", code, stderr, output.String())
				}
				var after map[string]json.RawMessage
				if err := json.Unmarshal(f.bytes(t), &after); err != nil {
					t.Fatal(err)
				}
				sameJSON := func(want, got json.RawMessage) bool {
					var w, g any
					return json.Unmarshal(want, &w) == nil && json.Unmarshal(got, &g) == nil && reflect.DeepEqual(w, g)
				}
				wanted, _ := json.Marshal(wantReplay)
				if !sameJSON(wanted, after["replay"]) {
					t.Fatalf("unread replay metadata/order changed: got %s, want %s", after["replay"], wanted)
				}
				if !sameJSON(json.RawMessage(`["dly_1"]`), after["displayed"]) {
					t.Fatal("printed delivery did not publish proof")
				}
				for key, value := range before {
					if key != "replay" && key != "displayed" && !sameJSON(value, after[key]) {
						t.Fatalf("unrelated state field changed: %s", key)
					}
				}
				if got, err := os.ReadFile(bindingPath(f.path)); err != nil || !bytes.Equal(got, binding) {
					t.Fatal("session sidecar changed")
				}
			})
		}
	}
}

func TestInboxExplicitReplayPreservesCursorsClaimsAndBinding(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "manual replay", "")
	f.run(t, "busy", "wake")
	file := stateFile{f.path}
	if err := file.update(t.Context(), func(s *hookState) error {
		s.PollCursor = "opaque-poll"
		s.Claims = append(s.Claims, claimRef{ID: "dly_pending", Claim: "pending", After: "opaque-before", Priority: "wake_idle", Order: 19})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := readHookState(t, f.path)
	// Keep an exact existing session sidecar; inbox proof must not rewrite it.
	binding := []byte("{\n  \"session\":\"owned-session\", \"session_how\":\"declared\", \"future_field\":true\n}\n")
	if err := cliFixtureWriteFile(bindingPath(f.path), binding, 0600); err != nil {
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
	if err := cliFixtureWriteFile(f.path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox", "--since", "e1.0"); code != 0 || !strings.Contains(out.String(), "manual replay") {
			t.Fatalf("explicit replay suppressed: %d %s", code, stderr)
		}
	}
	after := readHookState(t, f.path)
	if after.AppCursor != before.AppCursor || after.PollCursor != before.PollCursor || !reflect.DeepEqual(after.Claims, before.Claims) || !slices.Equal(inboxProof(t, f.path), []string{"dly_1"}) || len(after.Replay) != 0 {
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
			if !slices.Equal(inboxProof(t, f.path), []string{"dly_1"}) || len(s.Claims) != 1 || s.Claims[0].ID != "dly_2" || s.AppCursor != "opaque-a" {
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
	if !reflect.DeepEqual(before.Claims, after.Claims) || len(inboxProof(t, f.path)) != 0 || !slices.Equal(before.Shown, after.Shown) {
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
			if err := cliFixtureWriteFile(f.path, raw, 0600); err != nil {
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

func inboxProof(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Displayed []string `json:"displayed"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state.Displayed
}

func TestInboxFailedOutputPreservesReplayAndAck(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/short=%t", format, short), func(t *testing.T) {
				f := newAppHookFixture(t)
				f.add(t, "wake_idle", "unprinted", "")
				f.run(t, "busy", "wake")
				before := f.bytes(t)
				flags := []string{"inbox", "--ack"}
				if format == "json" {
					flags = append([]string{"--json"}, flags...)
				}
				writer := inboxOutputFunc(func(p []byte) (int, error) {
					if short {
						return len(p) - 1, nil
					}
					return 0, io.ErrClosedPipe
				})
				if code, err := runDisplayInbox(t, f.env, writer, flags...); code == 0 {
					t.Fatalf("failed stdout succeeded: %s", err)
				}
				if !bytes.Equal(before, f.bytes(t)) || f.acks != 0 {
					t.Fatal("failed stdout changed proof or acknowledged")
				}
				got, _, _ := f.run(t, "retry", "wake,wake_idle")
				appHookIDs(t, got, "dly_1")
			})
		}
	}
}

type inboxOutputFunc func([]byte) (int, error)

func (f inboxOutputFunc) Write(p []byte) (int, error) { return f(p) }

func TestInboxCommitFailureNeverRetriesOutputOrAcknowledges(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "printed once", "")
	f.run(t, "busy", "wake")
	before := f.bytes(t)
	writes := 0
	var out bytes.Buffer
	writer := inboxOutputFunc(func(p []byte) (int, error) {
		writes++
		if writes == 1 {
			if err := os.Rename(f.path, f.path+".saved"); err != nil {
				return 0, err
			}
			if err := cliFixtureMkdir(f.path, 0700); err != nil {
				return 0, err
			}
		}
		return out.Write(p)
	})
	code, stderr := runDisplayInbox(t, f.env, writer, "--json", "inbox", "--ack")
	if code == 0 || !strings.Contains(stderr, "display state commit failed") || writes != 1 || strings.Count(out.String(), "printed once") != 1 || f.acks != 0 {
		t.Fatalf("commit failure: code=%d writes=%d acks=%d stderr=%s", code, writes, f.acks, stderr)
	}
	if raw, err := os.ReadFile(f.path + ".saved"); err != nil || !bytes.Equal(raw, before) {
		t.Fatal("failed commit altered old proof")
	}
}

func TestInboxAmbiguousStateDoesNotPublishProof(t *testing.T) {
	for _, suffix := range []string{`,"caller":"peer_test"}`, `,"CALLER":"peer_test"}`, `,"claims":[{"ID":"pending","Accepted":true,"accepted":null}]}`} {
		t.Run(suffix, func(t *testing.T) {
			f := newAppHookFixture(t)
			f.add(t, "wake_idle", "readable", "")
			f.run(t, "busy", "wake")
			raw := f.bytes(t)
			raw = append(bytes.TrimSpace(raw)[:len(bytes.TrimSpace(raw))-1], []byte(suffix)...)
			f.seed(t, string(raw))
			var out bytes.Buffer
			if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 || !strings.Contains(out.String(), "readable") {
				t.Fatalf("ordinary output: %d %s", code, stderr)
			}
			if !bytes.Equal(raw, f.bytes(t)) {
				t.Fatal("ambiguous recovery state rewritten")
			}
		})
	}
}

func TestInboxDisplayRequiresFreshIdentity(t *testing.T) {
	for _, identity := range []string{`{"kind":"peer","peer_id":"peer_other"}`, `{}`, `{"kind":"operator"}`, "outage"} {
		t.Run(identity, func(t *testing.T) {
			f := newAppHookFixture(t)
			f.add(t, "wake_idle", "ordinary output", "")
			f.run(t, "busy", "wake")
			before := f.bytes(t)
			f.identity = identity
			var out bytes.Buffer
			if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 || !strings.Contains(out.String(), "ordinary output") {
				t.Fatalf("ordinary output unavailable: %d %s", code, stderr)
			}
			if !bytes.Equal(before, f.bytes(t)) {
				t.Fatal("cached identity authorized fresh proof")
			}
		})
	}
}

// TestInboxNoDisplayProofMachineRead pins the delivery host's machine read:
// `inbox --no-display-proof` prints the same bounded page but never publishes
// display proof, so the legacy hook state stays untouched and a subsequent
// UserPromptSubmit hook still shows the message the daemon merely observed.
func TestInboxNoDisplayProofMachineRead(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "hook must still show this", "")
	got, _, _ := f.run(t, "busy", "wake")
	appHookIDs(t, got) // deferred into replay, not shown yet
	before := f.bytes(t)

	var out bytes.Buffer
	code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox", "--since", "e1.0", "--no-display-proof")
	if code != 0 || !strings.Contains(out.String(), "hook must still show this") {
		t.Fatalf("machine read failed: %d %s %s", code, stderr, out.String())
	}
	if !bytes.Equal(before, f.bytes(t)) || len(inboxProof(t, f.path)) != 0 {
		t.Fatal("the machine read published display proof or changed hook state")
	}

	// The legacy hook still delivers the unread message after the daemon read.
	got, _, _ = f.run(t, "after-machine-read", "wake,wake_idle")
	appHookIDs(t, got, "dly_1")
}

func TestInboxDisplayBusyStateStillPrints(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "busy output", "")
	f.run(t, "busy", "wake")
	before := f.bytes(t)
	unlock, err := lockPath(t.Context(), f.path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var out bytes.Buffer
	start := time.Now()
	if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 || !strings.Contains(out.String(), "busy output") {
		t.Fatalf("lock blocked output: %d %s", code, stderr)
	}
	if time.Since(start) > 2*time.Second || !bytes.Equal(before, f.bytes(t)) {
		t.Fatal("busy state fallback changed state or was not bounded")
	}
}

func TestInboxDisplayPreservesAcceptedGenerationClaim(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "accepted output", "")
	f.run(t, "accepted", "wake,wake_idle")
	if err := codexUpdateState(t.Context(), f.path, false, func(s *hookState) error { s.Claims[0].Accepted = true; return nil }); err != nil {
		t.Fatal(err)
	}
	before := f.bytes(t)
	var out bytes.Buffer
	if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox", "--since", "e1.0"); code != 0 {
		t.Fatalf("explicit inbox: %d %s", code, stderr)
	}
	if !bytes.Equal(before, f.bytes(t)) || len(inboxProof(t, f.path)) != 0 {
		t.Fatal("manual output settled accepted generation claim")
	}
}

func TestInboxDisplayPreservesSingleCaseVariantKeys(t *testing.T) {
	f := newAppHookFixture(t)
	f.add(t, "wake_idle", "case variant", "")
	f.run(t, "busy", "wake")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(f.bytes(t), &fields); err != nil {
		t.Fatal(err)
	}
	fields["Replay"] = fields["replay"]
	delete(fields, "replay")
	fields["Displayed"] = json.RawMessage(`[]`)
	raw, _ := json.Marshal(fields)
	f.seed(t, string(raw))
	var out bytes.Buffer
	if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 {
		t.Fatalf("inbox: %d %s", code, stderr)
	}
	after := f.bytes(t)
	if validateCodexStateJSON(after) != nil || !slices.Equal(inboxProof(t, f.path), []string{"dly_1"}) {
		t.Fatalf("created ambiguous proof keys: %s", after)
	}
	fields = nil
	if err := json.Unmarshal(after, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["Replay"]; ok {
		t.Fatal("printed replay not retired")
	}
}

func TestInboxDisplayPreservesMalformedBinding(t *testing.T) {
	for _, sidecar := range []bool{false, true} {
		t.Run(fmt.Sprint(sidecar), func(t *testing.T) {
			f := newAppHookFixture(t)
			f.add(t, "wake_idle", "ordinary binding output", "")
			f.run(t, "busy", "wake")
			if sidecar {
				if err := cliFixtureWriteFile(bindingPath(f.path), []byte(`{"session":17}`), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(bindingPath(f.path)); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(f.bytes(t), &fields); err != nil {
					t.Fatal(err)
				}
				fields["session"] = json.RawMessage(`17`)
				raw, _ := json.Marshal(fields)
				f.seed(t, string(raw))
			}
			before := f.bytes(t)
			bindingBefore, bindingErr := os.ReadFile(bindingPath(f.path))
			var out bytes.Buffer
			if code, stderr := runDisplayInbox(t, f.env, &out, "--json", "inbox"); code != 0 || !strings.Contains(out.String(), "ordinary binding output") {
				t.Fatalf("ordinary output: %d %s", code, stderr)
			}
			bindingAfter, afterErr := os.ReadFile(bindingPath(f.path))
			if !bytes.Equal(before, f.bytes(t)) || !bytes.Equal(bindingBefore, bindingAfter) || os.IsNotExist(bindingErr) != os.IsNotExist(afterErr) {
				t.Fatal("uncertain binding manufactured proof or mutated sidecar")
			}
		})
	}
}
