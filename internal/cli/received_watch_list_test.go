package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hubid"
)

type receivedWatchSnapshot struct {
	move  *envelope.HubNotice
	timer *time.Timer
	c     <-chan time.Time
	due   time.Time
	files map[string]string
	read  int
	calls int
}

func receivedScheduled(t *testing.T, f *receivedFixture, id string) envelope.HubNotice {
	t.Helper()
	n := f.notice()
	n.NoticeID = id
	n.At = envelope.FormatTime(time.Now().Add(2 * time.Hour))
	n.ExpiresAt = envelope.FormatTime(time.Now().Add(4 * time.Hour))
	if err := f.key.SignNotice(&n); err != nil {
		t.Fatal(err)
	}
	if err := hubid.VerifyNotice(f.key.Pubkey(), n); err != nil {
		t.Fatalf("fixture did not prepare a valid signed notice: %v", err)
	}
	return n
}

func receivedWatch(t *testing.T, mode string, seeded bool) (*receivedFixture, *watcher, receivedWatchSnapshot) {
	t.Helper()
	f := newReceivedFixture(t, receivedListHub)
	a := f.app()
	a.json = mode == "json"
	c, name, ch, err := a.connect()
	if err != nil {
		t.Fatal(err)
	}
	_, _, p, err := a.profileSelection()
	if err != nil {
		t.Fatal(err)
	}
	w := &watcher{a: a, c: c, ch: ch, name: name, profile: p, forMonitor: mode == "monitor"}
	t.Cleanup(w.stopMoveTimer)
	prior := receivedWatchSnapshot{}
	if seeded {
		n := receivedScheduled(t, f, "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAV")
		receivedNoticeReply(t, f, map[string]any{"notices": []envelope.HubNotice{n}})
		w.refreshMoving() // Establish the prior state through the real admission path.
		if w.move == nil || *w.move != n || w.moveTimer == nil || w.moveC != w.moveTimer.C || !w.moveDue.After(time.Now()) {
			t.Fatalf("valid seed was not installed/scheduled: move=%+v timer=%v due=%v", w.move, w.moveTimer, w.moveDue)
		}
		copy := *w.move
		prior.move = &copy
		prior.timer, prior.c, prior.due = w.moveTimer, w.moveC, w.moveDue
		timer := prior.timer
		t.Cleanup(func() { timer.Stop() })
		prior.calls, _, _, prior.read = f.counts()
	}
	if a.stdout.(*bytes.Buffer).Len() != 0 || a.stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("valid setup unexpectedly emitted output")
	}
	prior.files = f.snapshot()
	return f, w, prior
}

func receivedWatchRetained(t *testing.T, w *watcher, prior receivedWatchSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(w.move, prior.move) || w.moveTimer != prior.timer || w.moveC != prior.c || !w.moveDue.Equal(prior.due) {
		t.Errorf("rejected snapshot replaced state: move=%+v timer=%p channel=%v due=%v; want move=%+v timer=%p channel=%v due=%v",
			w.move, w.moveTimer, w.moveC, w.moveDue, prior.move, prior.timer, prior.c, prior.due)
	}
	// A pointer alone cannot prove that refresh did not stop the prior timer.
	// All schedules are hours away; Stop reports its active state synchronously.
	if prior.timer != nil && !prior.timer.Stop() {
		t.Error("refresh stopped the previously accepted timer")
	}
}

func receivedWatchEffects(t *testing.T, f *receivedFixture, prior receivedWatchSnapshot) {
	t.Helper()
	f.unchanged(prior.files)
	source, destination, closed, read := f.counts()
	if source != prior.calls+1 || destination != 0 || closed != source || read != prior.read+len(f.body) {
		t.Errorf("refresh HTTP source/destination/closed/read=%d/%d/%d/%d; want=%d/0/%d/%d",
			source, destination, closed, read, prior.calls+1, prior.calls+1, prior.read+len(f.body))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r.origin == f.hub && r.path != "/v1/hub/identity" && (r.method != "GET" || r.path != "/v1/hub/notices") {
			t.Errorf("refresh made a non-list request: %s %s", r.method, r.path)
		}
	}
}

func receivedWatchRefusal(t *testing.T, w *watcher, mode string) {
	t.Helper()
	out, errs := w.a.stdout.(*bytes.Buffer).String(), w.a.stderr.(*bytes.Buffer).String()
	if errs != "" {
		t.Errorf("refusal stderr=%q", errs)
	}
	if mode == "json" {
		var event envelope.Event
		var data map[string]string
		if err := json.Unmarshal([]byte(out), &event); err != nil {
			t.Errorf("expected one refusal event, output=%q error=%v", out, err)
			return
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Errorf("refusal event data=%s error=%v", event.Data, err)
			return
		}
		if event.Type != "relocation_unverified" || event.Cursor != "" || !reflect.DeepEqual(data, map[string]string{"text": "invalid hub notice"}) {
			t.Errorf("refusal event=%+v data=%v", event, data)
		}
		return
	}
	want := "- relocation_unverified - hub - - invalid hub notice\n"
	if mode == "monitor" {
		want = "- relocation_unverified - hub hub: invalid hub notice\n"
	}
	if out != want {
		t.Errorf("fixed refusal output=%q want=%q", out, want)
	}
}

func TestWatchNoticeListRefusal(t *testing.T) {
	for _, bad := range receivedBadNotices(t) {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("whole-list/%s/reverse=%v", bad.name, reverse), func(t *testing.T) {
				f, w, prior := receivedWatch(t, "json", true)
				candidate := receivedScheduled(t, f, "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAW")
				items := []any{candidate, bad.value}
				if reverse {
					items[0], items[1] = items[1], items[0]
				}
				receivedNoticeReply(t, f, map[string]any{"notices": items})
				w.refreshMoving()
				receivedWatchRetained(t, w, prior)
				receivedWatchRefusal(t, w, "json")
				receivedWatchEffects(t, f, prior)
			})
		}
	}
	for _, seeded := range []bool{false, true} {
		for _, mode := range []string{"json", "plain", "monitor"} {
			for _, noFollow := range []bool{false, true} {
				t.Run(fmt.Sprintf("signed-public-http/seeded=%v/%s/no-follow=%v", seeded, mode, noFollow), func(t *testing.T) {
					f, w, prior := receivedWatch(t, mode, seeded)
					if noFollow {
						f.env["VECNET_NO_FOLLOW"] = "1"
					}
					n := receivedScheduled(t, f, "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAW")
					n.NewURL = "http://public.example.invalid/" + receivedCanary
					if err := f.key.SignNotice(&n); err != nil {
						t.Fatal(err)
					}
					receivedNoticeReply(t, f, map[string]any{"notices": []envelope.HubNotice{n}})
					w.refreshMoving()
					receivedWatchRetained(t, w, prior)
					receivedWatchRefusal(t, w, mode)
					receivedWatchEffects(t, f, prior)
				})
			}
		}
	}
	t.Run("controls", receivedWatchControls)
}

func receivedWatchControls(t *testing.T) {
	for _, mode := range []string{"json", "plain", "monitor"} {
		for _, state := range []string{"ordinary-outage", "same-notice", "valid-replacement", "empty-list", "missing-list", "null-list", "nonmoving"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				f, w, prior := receivedWatch(t, mode, true)
				n := *prior.move
				body := map[string]any{"notices": []envelope.HubNotice{n}}
				switch state {
				case "valid-replacement":
					n = receivedScheduled(t, f, "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAW")
					body["notices"] = []envelope.HubNotice{n}
				case "empty-list":
					body["notices"] = []any{}
				case "missing-list":
					delete(body, "notices")
				case "null-list":
					body["notices"] = nil
				case "nonmoving":
					n.Kind, n.NewURL = "maintenance", ""
					if err := f.key.SignNotice(&n); err != nil {
						t.Fatal(err)
					}
					body["notices"] = []envelope.HubNotice{n}
				}
				receivedNoticeReply(t, f, body)
				if state == "ordinary-outage" {
					f.reply(503, "unavailable", "ordinary "+receivedCanary, nil)
				}
				w.refreshMoving()
				switch state {
				case "ordinary-outage", "same-notice":
					receivedWatchRetained(t, w, prior)
				case "valid-replacement":
					if w.move == nil || *w.move != n || w.moveTimer == nil || w.moveTimer == prior.timer || w.moveC != w.moveTimer.C || !w.moveDue.After(time.Now()) {
						t.Errorf("valid replacement not installed/scheduled: move=%+v timer=%p due=%v", w.move, w.moveTimer, w.moveDue)
					}
					if prior.timer.Stop() {
						t.Error("replacement did not stop old timer")
					}
				default:
					if w.move != nil || w.moveTimer != nil || w.moveC != nil || !w.moveDue.IsZero() || prior.timer.Stop() {
						t.Error("accepted list without a move did not clear the prior schedule")
					}
				}
				if w.a.stdout.(*bytes.Buffer).Len() != 0 || w.a.stderr.(*bytes.Buffer).Len() != 0 {
					t.Errorf("control emitted output: %q/%q", w.a.stdout, w.a.stderr)
				}
				receivedWatchEffects(t, f, prior)
			})
		}
	}
	for _, policy := range []string{"profile", "environment"} {
		t.Run("admitted-public-http/"+policy, func(t *testing.T) {
			f, w, prior := receivedWatch(t, "json", false)
			if policy == "profile" {
				f.profile(true)
			} else {
				f.env[insecureEnv] = "1"
			}
			// Reconnect through the current constructor to take the selected policy.
			var err error
			w.c, _, _, err = w.a.connect()
			if err != nil {
				t.Fatal(err)
			}
			_, _, w.profile, err = w.a.profileSelection()
			if err != nil {
				t.Fatal(err)
			}
			prior.files = f.snapshot()
			n := receivedScheduled(t, f, "ntc_01ARZ3NDEKTSV4RRFFQ69G5FAW")
			n.NewURL = "http://public.example.invalid/Prefix/%2F"
			if err := f.key.SignNotice(&n); err != nil {
				t.Fatal(err)
			}
			receivedNoticeReply(t, f, map[string]any{"notices": []envelope.HubNotice{n}})
			w.refreshMoving()
			if w.move == nil || *w.move != n || w.moveTimer == nil || w.moveC != w.moveTimer.C || !w.moveDue.After(time.Now()) {
				t.Errorf("admitted signed HTTP move not scheduled: %+v", w.move)
			}
			if out, errs := w.a.stdout.(*bytes.Buffer).String(), w.a.stderr.(*bytes.Buffer).String(); strings.TrimSpace(out+errs) != "" {
				t.Errorf("admitted move emitted output: %q/%q", out, errs)
			}
			receivedWatchEffects(t, f, prior)
		})
	}
}
