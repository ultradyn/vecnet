package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestNoticePostListClear(t *testing.T) {
	e := testhub.Start(t)
	_, env := pinTestEnv(t, "[profiles.p]\nhub = \""+e.URL+"\"\nsecret = \""+e.OperatorSecret+"\"\n")
	for _, args := range [][]string{{"notice", "post", "--kind", "maintenance", "--message", "Tonight", "--expires", "2h"}, {"notice", "list"}} {
		code, out, errs := pinRun(env, args...)
		if code != 0 || !strings.Contains(out, "Tonight") || !strings.Contains(out, "maintenance") {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
	}
	if code, _, errs := pinRun(env, "notice", "clear", "--kind", "maintenance"); code != 0 {
		t.Fatalf("clear: %d %s", code, errs)
	}
	if code, out, errs := pinRun(env, "--json", "notice", "list"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list: %d %q %s", code, out, errs)
	}
	_, secret := e.NewPeer("session", "peer")
	_, peerEnv := pinTestEnv(t, "[profiles.p]\nhub = \""+e.URL+"\"\nsecret = \""+secret+"\"\n")
	if code, _, errs := pinRun(peerEnv, "notice", "post", "--kind", "maintenance", "--message", "No"); code != 1 || !strings.Contains(errs, "forbidden") {
		t.Fatalf("peer post: %d %s", code, errs)
	}
}

func noticeEvent(message string) envelope.Event {
	raw, _ := json.Marshal(envelope.HubNoticeEvent{Change: "posted", Notice: envelope.HubNotice{Kind: "moving", At: "2026-11-06T02:00:00.000Z", NewURL: "https://new.example", Message: message}})
	return envelope.Event{Cursor: "c", Type: "hub_notice", Data: raw}
}
func TestMonitorLineHubNotice(t *testing.T) {
	if got := monitorLine(noticeEvent("Moving tonight"), nil); got != "c hub_notice - hub hub: [moving 2026-11-06T02:00:00.000Z -> new.example] Moving tonight" {
		t.Fatal(got)
	}
	got := monitorLine(noticeEvent("a\r\nb\n\x1b\u202e"+strings.Repeat("界", 150)), nil)
	if strings.ContainsAny(got, "\r\n\x1b\u202e") || utf8.RuneCountInString(strings.TrimPrefix(got, "c hub_notice - hub ")) > 120 {
		t.Fatalf("unsafe or oversized: %q", got)
	}
	raw, _ := json.Marshal(envelope.HubNoticeEvent{Change: "cleared", Notice: envelope.HubNotice{Kind: "maintenance", Message: "done"}})
	if got := monitorLine(envelope.Event{Cursor: "c", Type: "hub_notice", Data: raw}, nil); got != "c hub_notice - hub hub: [cleared maintenance] done" {
		t.Fatal(got)
	}
}
func TestInboxLineHubNotice(t *testing.T) {
	got := inboxLine(noticeEvent("Moving tonight"), nil)
	fields := strings.Fields(got)
	if len(fields) < 7 || fields[3] != "hub" || fields[4] != "-" || fields[5] != "-" || !strings.Contains(got, "Moving tonight") {
		t.Fatal(got)
	}
}
