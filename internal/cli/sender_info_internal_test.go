package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/envelope"
)

// TestLinesPreferSenderInfo: a delivery's send-time sender_info.name wins
// over the cached directory (it is the name at send time and needs no
// lookup); without it the cache is used as before; other event types keep
// using the cache. The name is sanitised like any directory name.
func TestLinesPreferSenderInfo(t *testing.T) {
	names := map[string]string{"peer_X": "Dir"}
	delivery := func(info string) envelope.Event {
		msg := `"message_id":"msg_X","thread_id":"thr_X","sender":"peer_X","content":{"text":"hi"}`
		if info != "" {
			msg += `,"sender_info":` + info
		}
		return envelope.Event{Cursor: "e1.1", Type: "delivery", Data: json.RawMessage(`{"delivery_id":"dly_X","message":{` + msg + `}}`)}
	}
	for _, tc := range []struct {
		label, info, names, name string
	}{
		{"snapshot", `{"name":"Snap","kind":"session"}`, "Dir", "Snap"},
		{"legacy", "", "Dir", "Dir"},
		{"empty snapshot name", `{"name":"","kind":"session"}`, "Dir", "Dir"},
		{"hostile", `{"name":"a‮b \u001b[2Kc\n","kind":"session"}`, "Dir", "a_b__[2Kc_"},
		{"uncached", `{"name":"Snap","kind":"session"}`, "", "Snap"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			m := names
			if tc.names == "" {
				m = map[string]string{}
			}
			ev := delivery(tc.info)
			if got, want := monitorLine(ev, m), "e1.1 delivery thr_X peer_X "+tc.name+": hi"; got != want {
				t.Errorf("monitorLine = %q, want %q", got, want)
			}
			if got, want := inboxLine(ev, m), "e1.1 delivery thr_X "+tc.name+"(peer_X) msg_X dly_X hi"; got != want {
				t.Errorf("inboxLine = %q, want %q", got, want)
			}
		})
	}
	receipt := envelope.Event{Cursor: "e1.2", Type: "receipt", Data: json.RawMessage(`{"delivery_id":"dly_X","message_id":"msg_X","thread_id":"thr_X","state":"processed","by":"peer_X","at":"2026-11-04T00:00:00.000Z"}`)}
	if got := inboxLine(receipt, names); !strings.Contains(got, " Dir(peer_X) ") {
		t.Errorf("receipt inboxLine = %q, want the cached name", got)
	}
}
