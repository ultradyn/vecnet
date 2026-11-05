package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/XertroV/vecnet/internal/mcpdiag"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestMCPDiagnosticsOperatorAndFilters(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "diagnostic operator test")
	mcpdiag.Emit("events.refresh", map[string]any{"peer_id": peer, "method": "events/subscribe", "result": "refreshed"})
	path := "/v1/diag/mcp?peer=" + peer + "&method=events%2Fsubscribe&limit=1"
	for _, credential := range []string{"", secret} {
		resp, _ := env.Do(credential, http.MethodGet, path, nil)
		if resp.StatusCode != 401 && resp.StatusCode != 403 {
			t.Fatalf("nonoperator got %d", resp.StatusCode)
		}
	}
	resp, raw := env.Do(env.OperatorSecret, http.MethodGet, path, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("operator got %d %s", resp.StatusCode, raw)
	}
	var page struct {
		Records []mcpdiag.Record `json:"records"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].Fields["peer_id"] != peer || page.Records[0].Fields["method"] != "events/subscribe" {
		t.Fatalf("filtered response: %s", raw)
	}
	for _, q := range []string{"limit=0", "limit=1001", "limit=no", "after=no"} {
		resp, _ := env.Do(env.OperatorSecret, http.MethodGet, "/v1/diag/mcp?"+q, nil)
		if resp.StatusCode != 400 {
			t.Fatalf("%s got %d", q, resp.StatusCode)
		}
	}
}

func TestMCPOuterRefusalsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"body limit", string(make([]byte, 256*1024+1)), 413}, {"throttle", "{}", 429}} {
		t.Run(tc.name, func(t *testing.T) {
			env := testhub.Start(t)
			if tc.status == 429 {
				limited(env, 1, false)
				rawPost(t, env, "/mcp", "{}", nil)
			}
			before := mcpdiag.Snapshot(mcpdiag.Filter{Limit: 1})
			var after uint64
			if len(before) > 0 {
				after = before[0].Sequence
			}
			resp := rawPost(t, env, "/mcp", tc.body, nil)
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tc.status)
			}
			records := mcpdiag.Snapshot(mcpdiag.Filter{After: after})
			count := 0
			for _, rec := range records {
				if rec.Kind == "mcp.request" {
					count++
					if rec.Fields["http_status"] != tc.status {
						t.Fatalf("status metadata=%v", rec.Fields)
					}
				}
			}
			if count != 1 {
				t.Fatalf("completed request records=%d want 1", count)
			}
		})
	}
}

func TestMCPDiagnosticsExplicitZeroCursor(t *testing.T) {
	env := testhub.Start(t)
	peer, _ := env.NewPeer("session", "diagnostic zero cursor")
	for _, id := range []string{"evt_first", "evt_second", "evt_third"} {
		mcpdiag.Emit("events.delivery", map[string]any{"peer_id": peer, "event_id": id})
	}
	for _, tc := range []struct{ query, want string }{{"", "evt_third"}, {"&after=0", "evt_first"}} {
		resp, raw := env.Do(env.OperatorSecret, http.MethodGet, "/v1/diag/mcp?peer="+peer+"&limit=1"+tc.query, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
		}
		var page struct {
			Records []mcpdiag.Record `json:"records"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Records) != 1 || page.Records[0].Fields["event_id"] != tc.want {
			t.Fatalf("query %q records=%s want event=%s", tc.query, raw, tc.want)
		}
	}
}
