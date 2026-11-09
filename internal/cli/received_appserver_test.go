package cli

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

func receivedAppArgs(p *receivedPageFixture) []string {
	return []string{"--event", "app-server", "--claim", "batch", "--priorities", "wake_idle", "--expect-hub", p.hub, "--expect-caller", "peer_fixture"}
}

func receivedAppScenario(t *testing.T, p *receivedPageFixture, phase string, bad envelope.Event, order string) (string, []string) {
	t.Helper()
	cursor := "e1.0"
	if phase == "head" || phase == "backlog" {
		cursor = ""
	}
	path := receivedPageState(t, p, "e1.0", cursor, func(s *hookState) {
		if strings.HasPrefix(phase, "replay") {
			s.Replay = []replayRef{{ID: "dly_1", After: "e1.4", Priority: "wake_idle", Order: 7}}
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
	case "replay":
		p.steps = []receivedPageStep{receivedPage(t, "e1.4", items, "e1.9")}
		return path, []string{"e1.4"}
	case "replay-then-main":
		p.steps = []receivedPageStep{receivedPage(t, "e1.4", []envelope.Event{receivedPageDelivery(t, 1)}, "e1.1"), receivedPage(t, "e1.0", items, "e1.9")}
		return path, []string{"e1.4", "e1.0"}
	case "purged-retry":
		p.steps = []receivedPageStep{receivedPurge("e1.0"), receivedPage(t, "e1.5", items, "e1.9")}
		return path, []string{"e1.0", "e1.5"}
	default:
		p.steps = []receivedPageStep{receivedPage(t, "e1.0", items, "e1.9")}
		return path, []string{"e1.0"}
	}
}

func TestAppServerNoticePageRefusal(t *testing.T) {
	for _, bad := range receivedBadPageEvents(t) {
		for _, phase := range []string{"main", "head", "backlog", "replay", "replay-then-main", "purged-retry"} {
			for _, order := range []string{"bad-first", "bad-last"} {
				t.Run(bad.name+"/"+phase+"/"+order, func(t *testing.T) {
					p := newReceivedPageFixture(t)
					receivedPagePolicy(p, bad.policy)
					_, polls := receivedAppScenario(t, p, phase, bad.event, order)
					before := p.snapshot()
					call := receivedCallHook(t, p, receivedAppArgs(p), nil)
					diagnostic := ""
					if phase == "purged-retry" {
						diagnostic = "vecnet: app-server: cursor purged; resuming from e1.5\n"
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
		bad := receivedPageNotice(t, map[string]any{"hub": "https://hub.invalid/#"}, "cleared")
		_, polls := receivedAppScenario(t, p, "main", bad, "notice-only")
		before := p.snapshot()
		call := receivedCallHook(t, p, receivedAppArgs(p), nil)
		receivedHookRefusal(t, call, 1, "")
		p.unchanged(before)
		p.traffic(t, polls, nil)
	})
	t.Run("preservation", receivedAppPreservation)
	t.Run("controls", receivedAppControls)
}

func receivedAppPreservation(t *testing.T) {
	for _, good := range receivedGoodPageEvents(t) {
		t.Run(good.name, func(t *testing.T) {
			p := newReceivedPageFixture(t)
			receivedPagePolicy(p, good.policy)
			path := receivedPageState(t, p, "e1.0", "e1.0", nil)
			p.steps = []receivedPageStep{receivedPage(t, "e1.0", []envelope.Event{receivedPageDelivery(t, 1), good.event}, "e1.9")}
			call := receivedCallHook(t, p, receivedAppArgs(p), nil)
			receivedAppClaimed(t, p, path, call, "e1.0")
			p.traffic(t, []string{"e1.0"}, nil)
		})
	}
}

func receivedAppClaimed(t *testing.T, p *receivedPageFixture, path string, call receivedHookCall, after string) {
	t.Helper()
	var out appHookOutput
	if err := json.Unmarshal([]byte(call.out), &out); err != nil {
		t.Fatalf("accepted app-server output=%q error=%v", call.out, err)
	}
	s := readHookState(t, path)
	if out.Version != 1 || !reflect.DeepEqual(out.Deliveries, []string{"dly_1"}) || out.Priorities["dly_1"] != "wake_idle" || !strings.Contains(call.out, receivedCanary) || call.writes != 1 || call.errs != "" || call.result.ackCursor != "" {
		t.Errorf("accepted app-server did not claim exact delivery: call=%+v output=%+v", call, out)
	}
	if s.PollCursor != "e1.0" || s.AppCursor != "e1.9" || len(s.Replay) != 0 || len(s.Claims) != 1 || s.Claims[0].ID != "dly_1" || s.Claims[0].After != after || s.Claims[0].Claim != "batch" || s.Claims[0].Priority != "wake_idle" || !reflect.DeepEqual(s.Displayed, []string{"dly_kept"}) || !reflect.DeepEqual(s.Shown, []string{"dly_kept", "dly_1"}) {
		t.Errorf("accepted reservation/cursor/proof state=%+v", s)
	}
	dir, err := cacheDir(func(k string) string { return p.env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if thread, ok := (cache{dir}).threadOf("msg_1"); !ok || thread != "thr_X" {
		t.Errorf("accepted delivery cache=%q/%v", thread, ok)
	}
}

func receivedAppControls(t *testing.T) {
	for _, mode := range []string{"empty", "unrelated", "bootstrap", "replay", "purge-outage", "failed-publish", "failed-commit"} {
		t.Run(mode, func(t *testing.T) {
			p := newReceivedPageFixture(t)
			cursor := "e1.0"
			if mode == "bootstrap" {
				cursor = ""
			}
			path := receivedPageState(t, p, "e1.0", cursor, func(s *hookState) {
				if mode == "replay" {
					s.Replay = []replayRef{{ID: "dly_1", After: "e1.4", Priority: "wake_idle", Order: 7}}
				}
			})
			before := readHookState(t, path)
			events := []envelope.Event{receivedPageDelivery(t, 1)}
			if mode == "empty" || mode == "replay" {
				events = nil
			}
			if mode == "unrelated" {
				events = []envelope.Event{{Cursor: "e1.2", Type: "unrelated", Data: []byte(`{"notice":null}`)}}
			}
			p.steps = []receivedPageStep{receivedPage(t, "e1.0", events, "e1.9")}
			polls := []string{"e1.0"}
			if mode == "bootstrap" {
				p.steps = []receivedPageStep{receivedPage(t, "head", nil, "e1.100"), receivedPage(t, "", events, "e1.9")}
				polls = []string{"head", ""}
			}
			if mode == "replay" {
				p.steps = append([]receivedPageStep{receivedPage(t, "e1.4", []envelope.Event{receivedPageDelivery(t, 1)}, "e1.1")}, p.steps...)
				polls = []string{"e1.4", "e1.0"}
			}
			if mode == "purge-outage" {
				p.steps = []receivedPageStep{receivedPurge("e1.0"), {"e1.5", 503, []byte(`{"error":{"code":"unavailable","message":"ordinary outage"}}`)}}
				polls = []string{"e1.0", "e1.5"}
			}
			var publish func([]byte) error
			var after []func()
			if mode == "failed-publish" {
				publish = func([]byte) error { return errors.New("synthetic publish failure") }
				p.steps = append(p.steps, receivedPage(t, "e1.0", events, "e1.1"), receivedPage(t, "e1.9", nil, "e1.9"))
			}
			if mode == "failed-commit" {
				after = []func(){func() { receivedBlockStateCommit(t, path) }}
			}
			call := receivedCallHook(t, p, receivedAppArgs(p), publish, after...)
			if call.polls != len(polls) || call.result.ackCursor != "" {
				t.Errorf("app-server completion/ack=%+v", call)
			}
			if mode == "failed-commit" {
				path += ".saved"
			}
			got := readHookState(t, path)
			switch mode {
			case "failed-commit":
				if call.writes != 0 || call.out != "" || !reflect.DeepEqual(got, before) {
					t.Errorf("failed app-server commit published/claimed: call=%+v state=%+v", call, got)
				}
			case "failed-publish":
				if call.writes != 1 || call.out != "" || got.AppCursor != "e1.9" || len(got.Claims) != 1 || got.Claims[0].ID != "dly_1" || !reflect.DeepEqual(got.Displayed, before.Displayed) {
					t.Errorf("failed publish lost durable reservation: call=%+v state=%+v", call, got)
				}
				release := receivedCallHook(t, p, []string{"--release", "--claim", "batch"}, nil)
				if release.out != "{\"released\":1}\n" || release.polls != 0 || len(readHookState(t, path).Replay) != 1 {
					t.Errorf("failed publication cannot release claim: %+v", release)
				}
				call = receivedCallHook(t, p, receivedAppArgs(p), nil)
				receivedAppClaimed(t, p, path, call, "e1.0")
				polls = []string{"e1.0", "e1.0", "e1.9"}
			case "purge-outage":
				before.AppCursor = "e1.5"
				if !reflect.DeepEqual(got, before) || call.errs != "vecnet: app-server: cursor purged; resuming from e1.5\n" || call.out != "{\"version\":1,\"deliveries\":[],\"lifecycle\":\"receipt-display-v1\"}\n" {
					t.Errorf("ordinary app retry did not save resumption: call=%+v state=%+v", call, got)
				}
			case "bootstrap":
				if got.AppCursor != "e1.100" || got.PollCursor != "e1.0" || len(got.Claims) != 0 || !strings.Contains(call.out, "1 unacked deliveries waiting") {
					t.Errorf("app bootstrap summary changed: call=%+v state=%+v", call, got)
				}
			case "replay":
				receivedAppClaimed(t, p, path, call, "e1.4")
			default:
				before.AppCursor = "e1.9"
				if !reflect.DeepEqual(got, before) || call.out != "{\"version\":1,\"deliveries\":[],\"lifecycle\":\"receipt-display-v1\"}\n" {
					t.Errorf("empty/unrelated app page changed contract: call=%+v state=%+v", call, got)
				}
			}
			p.traffic(t, polls, nil)
		})
	}
	t.Run("dispatcher-silent-refusal", func(t *testing.T) {
		p := newReceivedPageFixture(t)
		receivedPageState(t, p, "e1.0", "e1.0", nil)
		p.steps = []receivedPageStep{receivedPage(t, "e1.0", []envelope.Event{receivedPageNotice(t, nil, "posted")}, "e1.9")}
		before := p.snapshot()
		code, out, errs := p.run(append([]string{"hook"}, receivedAppArgs(p)...)...)
		if code != 0 || out != "" || errs != "" {
			t.Errorf("app-server dispatcher refusal=%d/%q/%q", code, out, errs)
		}
		p.unchanged(before)
		p.traffic(t, []string{"e1.0"}, nil)
	})
}
