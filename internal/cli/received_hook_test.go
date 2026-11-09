package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

func receivedPageClock() time.Time { return time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC) }

func receivedPageState(t *testing.T, p *receivedPageFixture, poll, appCursor string, edit func(*hookState)) string {
	t.Helper()
	path, err := statePath(func(k string) string { return p.env[k] }, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	seedHook(t, path, poll)
	err = (stateFile{path}).update(context.Background(), func(s *hookState) error {
		*s = hookState{Hub: p.hub, Caller: "peer_fixture", Cred: hookCredential(receivedPageSecret),
			PollCursor: poll, AppCursor: appCursor, Shown: []string{"dly_kept"}, Displayed: []string{"dly_kept"}, LastPostToolPoll: receivedPageClock()}
		if edit != nil {
			edit(s)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Fail a real atomic state rename after output, without privilege assumptions.
// Own restoration separately so fatal assertions cannot strand the fixture path.
func receivedBlockStateCommit(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
		if err := os.Rename(path+".saved", path); err != nil {
			t.Error(err)
		}
	})
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
}

type receivedHookCall struct {
	result        hookResult
	out, errs     string
	writes, polls int
}

// Run the real synchronous callback/transaction, avoiding a supervisor timeout
// masquerading as silent refusal. Any returned auto-ack crosses actual HTTP.
func receivedCallHook(t *testing.T, p *receivedPageFixture, args []string, publish func([]byte) error, after ...func()) receivedHookCall {
	t.Helper()
	a := p.app()
	a.inHook = true
	var out bytes.Buffer
	call := receivedHookCall{}
	call.result = a.hookRun(args, receivedPageClock, func() {
		call.polls++
		for _, fn := range after {
			fn()
		}
	}, func(raw []byte) error {
		call.writes++
		if publish != nil {
			if err := publish(raw); err != nil {
				return err
			}
		}
		_, err := out.Write(raw)
		return err
	}, 0)
	if a.ctx.Err() != nil {
		t.Fatalf("callback did not complete: %v", a.ctx.Err())
	}
	if call.result.ackCursor != "" {
		if err := a.ack(call.result.client, call.result.ackCursor); err != nil {
			t.Errorf("actual auto-ack: %v", err)
		}
	}
	call.out, call.errs = out.String(), a.stderr.(*bytes.Buffer).String()
	return call
}

func receivedPurge(after string) receivedPageStep {
	return receivedPageStep{after, 409, []byte(`{"error":{"code":"cursor_purged","message":"purged","details":{"oldest_cursor":"e1.5"}}}`)}
}

func receivedClaudeScenario(t *testing.T, p *receivedPageFixture, phase string, bad envelope.Event, order string) (string, []string) {
	t.Helper()
	cursor := "e1.0"
	if phase == "head" || phase == "backlog" {
		cursor = ""
	}
	path := receivedPageState(t, p, cursor, "e1.0", func(s *hookState) {
		if phase == "probe" {
			s.Claims = []claimRef{{ID: "dly_7", After: "e1.0", Priority: "wake_idle", Claim: "reserved", Order: 1}}
			s.Shown = append(s.Shown, "dly_7")
		}
	})
	items := receivedMixedPage(t, bad, order)
	switch phase {
	case "head":
		p.steps = []receivedPageStep{receivedPage(t, "head", items, "e1.100")}
		return path, []string{"head"}
	case "backlog":
		p.steps = []receivedPageStep{receivedPage(t, "head", nil, "e1.100"), receivedPage(t, "", items, "e1.9")}
		return path, []string{"head", ""}
	case "probe":
		items = append(items, receivedPageDelivery(t, 7))
		p.steps = []receivedPageStep{receivedPage(t, "e1.0", []envelope.Event{receivedPageDelivery(t, 1)}, "e1.1"), receivedPage(t, "e1.1", items, "e1.9")}
		return path, []string{"e1.0", "e1.1"}
	case "purged-retry":
		p.steps = []receivedPageStep{receivedPurge("e1.0"), receivedPage(t, "e1.5", items, "e1.9")}
		return path, []string{"e1.0", "e1.5"}
	default:
		p.steps = []receivedPageStep{receivedPage(t, "e1.0", items, "e1.9")}
		return path, []string{"e1.0"}
	}
}

func receivedHookRefusal(t *testing.T, call receivedHookCall, polls int, diagnostic string) {
	t.Helper()
	if call.out != "" || call.writes != 0 || call.result.ackCursor != "" || call.polls != polls || call.errs != diagnostic {
		t.Errorf("callback accepted page: output=%q writes=%d ack=%q completed polls=%d stderr=%q; want polls=%d stderr=%q",
			call.out, call.writes, call.result.ackCursor, call.polls, call.errs, polls, diagnostic)
	}
}

func TestHookNoticePageRefusal(t *testing.T) {
	for _, bad := range receivedBadPageEvents(t) {
		for _, phase := range []string{"main", "head", "backlog", "probe", "purged-retry"} {
			for _, order := range []string{"bad-first", "bad-last"} {
				t.Run(bad.name+"/"+phase+"/"+order, func(t *testing.T) {
					p := newReceivedPageFixture(t)
					receivedPagePolicy(p, bad.policy)
					_, polls := receivedClaudeScenario(t, p, phase, bad.event, order)
					before := p.snapshot()
					call := receivedCallHook(t, p, []string{"--event", "user-prompt", "--auto-ack"}, nil)
					diagnostic := ""
					if phase == "purged-retry" {
						diagnostic = "vecnet: hook: cursor purged; resuming from e1.5\n"
					}
					receivedHookRefusal(t, call, len(polls), diagnostic)
					p.unchanged(before)
					p.traffic(t, polls, nil)
				})
			}
		}
	}
	t.Run("notice-only", func(t *testing.T) {
		p := newReceivedPageFixture(t)
		bad := receivedPageNotice(t, map[string]any{"hub": "https://hub.invalid/?"}, "posted")
		_, polls := receivedClaudeScenario(t, p, "main", bad, "notice-only")
		before := p.snapshot()
		call := receivedCallHook(t, p, []string{"--event", "user-prompt", "--auto-ack"}, nil)
		receivedHookRefusal(t, call, 1, "")
		p.unchanged(before)
		p.traffic(t, polls, nil)
	})
	t.Run("controls", receivedClaudeControls)
}

func receivedClaudeControls(t *testing.T) {
	for _, good := range receivedGoodPageEvents(t) {
		t.Run("preservation/"+good.name, func(t *testing.T) {
			p := newReceivedPageFixture(t)
			receivedPagePolicy(p, good.policy)
			path := receivedPageState(t, p, "e1.0", "e1.0", nil)
			p.steps = []receivedPageStep{receivedPage(t, "e1.0", []envelope.Event{receivedPageDelivery(t, 1), good.event}, "e1.9")}
			call := receivedCallHook(t, p, []string{"--event", "user-prompt", "--auto-ack"}, nil)
			s := readHookState(t, path)
			if call.writes != 1 || call.errs != "" || !strings.Contains(contextOf(t, call.out), "delivery=dly_1 ") || s.PollCursor != "e1.9" || !reflect.DeepEqual(s.Displayed, []string{"dly_kept", "dly_1"}) {
				t.Errorf("valid notice page blocked ordinary delivery: call=%+v state=%+v", call, s)
			}
			p.traffic(t, []string{"e1.0"}, []string{"e1.9"})
		})
	}
	for _, mode := range []string{"valid", "empty", "unrelated", "bootstrap", "purge-outage", "failed-publish", "failed-commit"} {
		t.Run(mode, func(t *testing.T) {
			p := newReceivedPageFixture(t)
			cursor := "e1.0"
			if mode == "bootstrap" {
				cursor = ""
			}
			path := receivedPageState(t, p, cursor, "e1.0", nil)
			before := readHookState(t, path)
			events := []envelope.Event{receivedPageDelivery(t, 1), receivedPageNotice(t, receivedListNotice(), "posted")}
			if mode == "empty" {
				events = nil
			}
			if mode == "unrelated" {
				events = []envelope.Event{{Cursor: "e1.2", Type: "unrelated", Data: []byte(`{"notice":null}`)}}
			}
			p.steps = []receivedPageStep{receivedPage(t, "e1.0", events, "e1.9")}
			polls, acks := []string{"e1.0"}, []string{"e1.9"}
			if mode == "bootstrap" {
				p.steps = []receivedPageStep{receivedPage(t, "head", nil, "e1.100"), receivedPage(t, "", events, "e1.9")}
				polls, acks = []string{"head", ""}, nil
			}
			if mode == "purge-outage" {
				p.steps = []receivedPageStep{receivedPurge("e1.0"), {"e1.5", 503, []byte(`{"error":{"code":"unavailable","message":"ordinary outage"}}`)}}
				polls, acks = []string{"e1.0", "e1.5"}, nil
			}
			var publish func([]byte) error
			if mode == "failed-publish" {
				publish = func([]byte) error { return errors.New("synthetic publish failure") }
				acks = nil
			}
			if mode == "failed-commit" {
				publish = func([]byte) error { receivedBlockStateCommit(t, path); return nil }
				acks = nil
			}
			call := receivedCallHook(t, p, []string{"--event", "user-prompt", "--auto-ack"}, publish)
			if call.polls != len(polls) {
				t.Errorf("completed polls=%d want=%d", call.polls, len(polls))
			}
			if mode == "failed-commit" {
				path += ".saved"
			}
			got := readHookState(t, path)
			switch mode {
			case "failed-publish", "failed-commit":
				if call.writes != 1 || !reflect.DeepEqual(got, before) || call.result.ackCursor != "" {
					t.Errorf("failed output/commit changed accepted state: call=%+v state=%+v", call, got)
				}
			case "purge-outage":
				before.PollCursor = "e1.5"
				if !reflect.DeepEqual(got, before) || call.out != "" || call.errs != "vecnet: hook: cursor purged; resuming from e1.5\n" {
					t.Errorf("ordinary retry did not save resumption: call=%+v state=%+v", call, got)
				}
			case "bootstrap":
				if got.PollCursor != "e1.100" || !strings.Contains(contextOf(t, call.out), "1 unacked deliveries waiting") || !reflect.DeepEqual(got.Displayed, before.Displayed) {
					t.Errorf("bootstrap summary/cursor/proof changed: call=%+v state=%+v", call, got)
				}
			default:
				if got.PollCursor != "e1.9" {
					t.Errorf("accepted cursor=%q", got.PollCursor)
				}
				if mode == "valid" && (!strings.Contains(contextOf(t, call.out), "delivery=dly_1 ") || !reflect.DeepEqual(got.Displayed, []string{"dly_kept", "dly_1"})) {
					t.Errorf("valid delivery did not display: call=%+v state=%+v", call, got)
				}
				if mode != "valid" && (call.out != "" || !reflect.DeepEqual(got.Displayed, before.Displayed)) {
					t.Errorf("nondelivery page produced display: %+v", call)
				}
			}
			p.traffic(t, polls, acks)
		})
	}
	t.Run("pending-receipt-ack-barrier", receivedPendingBarrier)
	t.Run("dispatcher-silent-refusal", func(t *testing.T) {
		p := newReceivedPageFixture(t)
		receivedPageState(t, p, "e1.0", "e1.0", nil)
		p.steps = []receivedPageStep{receivedPage(t, "e1.0", []envelope.Event{receivedPageNotice(t, nil, "posted")}, "e1.9")}
		code, out, errs := p.run("hook", "--event", "user-prompt", "--auto-ack")
		if code != 0 || out != "" || errs != "" {
			t.Errorf("hook dispatcher refusal=%d/%q/%q", code, out, errs)
		}
		p.traffic(t, []string{"e1.0"}, nil)
	})
}

func receivedPendingBarrier(t *testing.T) {
	p := newReceivedPageFixture(t)
	path := receivedPageState(t, p, "e1.0", "e1.0", func(s *hookState) {
		s.Claims = []claimRef{{ID: "dly_1", After: "e1.0", Priority: "wake_idle", Claim: "reserved", Order: 1}}
		s.Shown = append(s.Shown, "dly_1")
	})
	events := []envelope.Event{receivedPageDelivery(t, 1), receivedPageDelivery(t, 2)}
	p.steps = []receivedPageStep{receivedPage(t, "e1.0", events, "e1.2"), receivedPage(t, "e1.0", events, "e1.2")}
	call := receivedCallHook(t, p, []string{"--event", "user-prompt", "--auto-ack"}, nil)
	s := readHookState(t, path)
	if s.AckAfter != "e1.0" || call.result.ackCursor != "" || !strings.Contains(contextOf(t, call.out), "delivery=dly_2 ") || strings.Contains(call.out, "delivery=dly_1 ") {
		t.Errorf("pending reservation lost cumulative ack fence: call=%+v state=%+v", call, s)
	}
	settled := receivedCallHook(t, p, []string{"--release", "--claim", "reserved", "--receipt-key", "vn.received", "--delivery-ids", "dly_1", "--expect-hub", p.hub, "--expect-caller", "peer_fixture"}, nil)
	if settled.polls != 0 || settled.writes != 1 || !strings.Contains(settled.out, `"receipt_key":"vn.received"`) || readHookState(t, path).AckAfter != "e1.0" {
		t.Errorf("receipt crossed the ack fence: %+v", settled)
	}
	call = receivedCallHook(t, p, []string{"--event", "user-prompt", "--auto-ack"}, nil)
	if call.out != "" || call.result.ackCursor != "e1.2" || readHookState(t, path).AckAfter != "" {
		t.Errorf("resolved fence did not resume cumulative ack: %+v", call)
	}
	p.traffic(t, []string{"e1.0", "e1.0"}, []string{"e1.2"})
}
