package hub_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

type recipientWarning struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	Recipient        string `json:"recipient"`
	LastSeen         string `json:"last_seen"`
	AgeSeconds       int    `json:"age_seconds"`
	ThresholdSeconds int    `json:"threshold_seconds"`
}

func recipientWarnings(t *testing.T, sr hub.SendResponse) []recipientWarning {
	t.Helper()
	raw, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Warnings []recipientWarning `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	var out []recipientWarning
	for _, w := range receipt.Warnings {
		if w.Code == "recipient_not_seen" {
			out = append(out, w)
		}
	}
	return out
}

func TestRecipientWarningDefaultBoundaryAndDelivery(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want bool
	}{
		{59 * time.Minute, false},
		{time.Hour, false},
		{time.Hour + time.Millisecond, true},
		{75 * time.Minute, true},
		{-time.Minute, false},
	} {
		t.Run(tc.age.String(), func(t *testing.T) {
			env := testhub.Start(t)
			_, sec := env.NewPeer("session", "Sender")
			b, _ := env.NewPeer("session", "Recipient")
			seen := envelope.FormatTime(env.Now.Now())
			env.Now.Advance(tc.age)
			sr := mustSend(t, env, sec, req("stale", b))
			warnings := recipientWarnings(t, sr)
			if (len(warnings) == 1) != tc.want {
				t.Fatalf("warnings = %+v, want warning %t", warnings, tc.want)
			}
			if tc.want {
				w := warnings[0]
				if w.Recipient != b || w.LastSeen != seen || w.AgeSeconds != int(tc.age/time.Second) || w.ThresholdSeconds != 3600 || !strings.Contains(w.Message, "last seen") || !strings.Contains(w.Message, "minutes ago") {
					t.Fatalf("missing last-seen context: %+v", w)
				}
			}
			if len(sr.Deliveries) != 1 || sr.Deliveries[0].Recipient != b || len(deliveryEvents(t, env, b)) != 1 {
				t.Fatalf("advisory changed delivery: %+v", sr)
			}
		})
	}
}

func TestRecipientWarningActivityAndMonitor(t *testing.T) {
	for _, activity := range []string{"none", "inbound delivery", "authenticated call", "outgoing send", "open monitor", "closed monitor"} {
		t.Run(activity, func(t *testing.T) {
			env := testhub.Start(t)
			a, aSec := env.NewPeer("session", "Sender")
			b, bSec := env.NewPeer("session", "Recipient")
			if activity == "open monitor" || activity == "closed monitor" {
				env.Hub.StreamOpened(context.Background(), b)
			}
			env.Now.Advance(2 * time.Hour)
			switch activity {
			case "inbound delivery":
				mustSend(t, env, aSec, req("incoming", b))
			case "authenticated call":
				r, raw := env.Do(bSec, http.MethodGet, "/v1/peers", nil)
				if r.StatusCode != http.StatusOK {
					t.Fatalf("activity call: %d %s", r.StatusCode, raw)
				}
			case "outgoing send":
				mustSend(t, env, bSec, req("outgoing", a))
			case "closed monitor":
				env.Hub.StreamClosed(context.Background(), b)
				env.Now.Advance(time.Hour + time.Second)
			}
			warnings := recipientWarnings(t, mustSend(t, env, aSec, req("check", b)))
			want := activity == "none" || activity == "inbound delivery" || activity == "closed monitor"
			if (len(warnings) == 1) != want {
				t.Fatalf("warnings=%+v, want warning %t", warnings, want)
			}
		})
	}
}

func TestRecipientWarningGroupsRepliesAndExistingAdvisories(t *testing.T) {
	env := testhub.Start(t)
	a, sec := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Stale")
	c, cSec := env.NewPeer("session", "Fresh")
	g := addGroup(t, env, a, a, b, c)
	env.Now.Advance(2 * time.Hour)
	env.Do(cSec, http.MethodGet, "/v1/peers", nil)
	first := mustSend(t, env, sec, req("one", b, g, b))
	mustSend(t, env, sec, req("two", g))
	third := mustSend(t, env, sec, req("three", g))
	if len(third.Warnings) != 2 {
		t.Fatalf("existing advisory lost: %+v", third.Warnings)
	}
	reply := req("reply")
	delete(reply, "recipients")
	reply["thread_id"] = first.ThreadID
	for _, sr := range []hub.SendResponse{first, third, mustSend(t, env, sec, reply)} {
		warnings := recipientWarnings(t, sr)
		if len(warnings) != 1 || warnings[0].Recipient != b {
			t.Fatalf("fanout warnings: %+v", warnings)
		}
	}
}

func TestRecipientWarningLiveThresholdDisableAndReplay(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "Sender")
	b, bSec := env.NewPeer("session", "Recipient")
	env.Now.Advance(20 * time.Minute)
	runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.send_warning_after": prefs.Duration(600)})
	body := req("snapshot", b)
	status, sr, original := sendHTTP(t, env, sec, body)
	w := recipientWarnings(t, sr)
	if status != http.StatusAccepted || len(w) != 1 || w[0].ThresholdSeconds != 600 {
		t.Fatalf("live threshold ignored: %s", original)
	}
	env.Do(bSec, http.MethodGet, "/v1/peers", nil)
	runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.send_warning_after": prefs.Duration(0)})
	status, _, replay := sendHTTP(t, env, sec, body)
	if status != http.StatusOK || !bytes.Equal(original, replay) {
		t.Fatalf("warning snapshot changed: %d %s != %s", status, replay, original)
	}
	env.Now.Advance(2 * time.Hour)
	if w := recipientWarnings(t, mustSend(t, env, sec, req("disabled", b))); len(w) != 0 {
		t.Fatalf("disabled warning: %+v", w)
	}
}

func TestRecipientWarningNeverSeenAndOperatorExclusion(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("dot", "Never connected")
	if w := recipientWarnings(t, mustSend(t, env, sec, req("new", b))); len(w) != 0 {
		t.Fatalf("new peer warned too early: %+v", w)
	}
	env.Now.Advance(2 * time.Hour)
	sr := mustSend(t, env, sec, req("old", b, env.OperatorPeer))
	w := recipientWarnings(t, sr)
	if len(w) != 1 || w[0].Recipient != b || w[0].LastSeen != "" || w[0].AgeSeconds != 7200 || !strings.Contains(w[0].Message, "never been seen") {
		t.Fatalf("unknown activity misrepresented: %+v", w)
	}
}

func TestRecipientWarningRestartClearsMonitorAndKeepsThreshold(t *testing.T) {
	env := testhub.Start(t)
	_, sec := env.NewPeer("session", "Sender")
	b, _ := env.NewPeer("session", "Recipient")
	env.Hub.StreamOpened(context.Background(), b)
	env.Now.Advance(20 * time.Minute)
	runtimePatch(t, env.Hub, map[string]prefs.Value{"presence.send_warning_after": prefs.Duration(600)})
	if w := recipientWarnings(t, mustSend(t, env, sec, req("connected", b))); len(w) != 0 {
		t.Fatalf("active monitor warned: %+v", w)
	}
	restarted := env.CloneDB(t)
	w := recipientWarnings(t, mustSend(t, restarted, sec, req("after-restart", b)))
	if len(w) != 1 || w[0].Recipient != b || w[0].AgeSeconds != 1200 || w[0].ThresholdSeconds != 600 {
		t.Fatalf("restart lost threshold or kept dead monitor: %+v", w)
	}
}

// Unknown activity must never roll back otherwise valid deliveries, including
// healthy recipients sharing a group with a never-connected unarchived dot.
func TestRecipientWarningUnarchivedNeverConnectedDeliveryAndReplay(t *testing.T) {
	for _, group := range []bool{false, true} {
		name := "direct"
		if group {
			name = "group"
		}
		t.Run(name, func(t *testing.T) {
			env := testhub.Start(t)
			a, sec := env.NewPeer("session", "Sender")
			b, _ := env.NewPeer("dot", "Never connected")
			c, _ := env.NewPeer("session", "Healthy")
			for _, archived := range []bool{true, false} {
				if err := env.Hub.SetPeerArchived(context.Background(), b, archived); err != nil {
					t.Fatal(err)
				}
			}
			var lastSeen sql.NullString
			var enrolled string
			if err := env.Hub.DB.QueryRow(`SELECT last_seen,enrolled_at FROM peers WHERE peer_id=?`, b).Scan(&lastSeen, &enrolled); err != nil {
				t.Fatal(err)
			}
			if lastSeen.Valid || enrolled != "" {
				t.Fatalf("lifecycle did not produce unknown age: last_seen=%+v enrolled=%q", lastSeen, enrolled)
			}
			recipient := b
			wantPeers := []string{b}
			if group {
				recipient = addGroup(t, env, a, a, b, c)
				wantPeers = append(wantPeers, c)
			}
			body := req("unknown-age", recipient)
			status, sr, original := sendHTTP(t, env, sec, body)
			if status != http.StatusAccepted {
				t.Fatalf("send = %d, want 202: %s", status, original)
			}
			if w := recipientWarnings(t, sr); len(w) != 0 {
				t.Fatalf("unknown age warned: %+v", w)
			}
			if len(sr.Deliveries) != len(wantPeers) {
				t.Fatalf("deliveries = %+v, want %v", sr.Deliveries, wantPeers)
			}
			for _, peer := range wantPeers {
				if n := len(deliveryEvents(t, env, peer)); n != 1 {
					t.Fatalf("committed deliveries for %s = %d, want 1", peer, n)
				}
			}
			// New activity evidence must not change the stored receipt on replay.
			env.Hub.StreamOpened(context.Background(), b)
			status, _, replay := sendHTTP(t, env, sec, body)
			if status != http.StatusOK || !bytes.Equal(original, replay) {
				t.Fatalf("replay = %d %s, want original %s", status, replay, original)
			}
			for _, peer := range wantPeers {
				if n := len(deliveryEvents(t, env, peer)); n != 1 {
					t.Fatalf("replay duplicated deliveries for %s: %d", peer, n)
				}
			}
		})
	}
}

func TestRecipientWarningUnavailableObservation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lastSeen any
		enrolled string
	}{
		{"missing enrollment", nil, ""},
		{"malformed enrollment", nil, "bad-time"},
		{"empty last seen", "", "2020-01-01T00:00:00.000Z"},
		{"malformed last seen", "bad-time", "2020-01-01T00:00:00.000Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			_, sec := env.NewPeer("session", "Sender")
			b, _ := env.NewPeer("dot", "Unknown age")
			archiveExec(t, env, `UPDATE peers SET last_seen=?,enrolled_at=? WHERE peer_id=?`, tc.lastSeen, tc.enrolled, b)
			sr := mustSend(t, env, sec, req("unknown-observation", b))
			if w := recipientWarnings(t, sr); len(w) != 0 {
				t.Fatalf("unavailable observation warned: %+v", w)
			}
			if len(sr.Deliveries) != 1 || sr.Deliveries[0].Recipient != b || len(deliveryEvents(t, env, b)) != 1 {
				t.Fatalf("missing committed delivery: %+v", sr)
			}
		})
	}
}
