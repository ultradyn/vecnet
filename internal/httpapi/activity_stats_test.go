package httpapi_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
)

// Catches a missing route, non-operator access, cached authorization errors,
// and a JSON response that cannot distinguish empty data from unavailable data.
func TestActivityStatsHTTPContractAndAuthorization(t *testing.T) {
	env := testhub.Start(t)
	_, peerSecret := env.NewPeer("session", "reader")
	_, principalSecret := env.NewPrincipal("reader principal")
	for _, tc := range []struct {
		secret string
		status int
	}{
		{"", 401}, {"invalid", 401}, {peerSecret, 403}, {principalSecret, 403}, {env.OperatorSecret, 200},
	} {
		resp, raw := env.Do(tc.secret, "GET", "/v1/admin/activity-stats", nil)
		if resp.StatusCode != tc.status {
			t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.status, raw)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("status %d Cache-Control = %q", tc.status, resp.Header.Get("Cache-Control"))
		}
		if tc.status != 200 {
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		wantKeys := []string{"active_peers", "bucket_seconds", "buckets", "messages", "messages_per_minute", "now", "peers", "window_end", "window_start"}
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Fatalf("wire keys = %v", keys)
		}
		if got["messages"] != float64(0) || got["messages_per_minute"] != float64(0) || got["active_peers"] != float64(0) || got["bucket_seconds"] != float64(60) {
			t.Fatalf("empty totals: %s", raw)
		}
		buckets, ok := got["buckets"].([]any)
		if !ok || len(buckets) != 60 {
			t.Fatalf("zero series: %s", raw)
		}
		for _, n := range buckets {
			if n != float64(0) {
				t.Fatalf("zero series: %s", raw)
			}
		}
		peers, ok := got["peers"].(map[string]any)
		if !ok || len(peers) != 0 {
			t.Fatalf("empty map: %s", raw)
		}
		if got["now"] != got["window_end"] {
			t.Fatalf("end != now: %s", raw)
		}
	}
}

func TestActivityStatsHTTPMetadataAndDatabaseError(t *testing.T) {
	env := testhub.Start(t)
	peer, secret := env.NewPeer("session", "sender")
	request := sendBody("activity-wire", env.OperatorPeer)
	request["content"] = map[string]any{"text": "private-body-should-not-leak"}
	postSend(t, env, secret, request, 202)
	env.Now.Advance(time.Millisecond)
	resp, raw := env.Do(env.OperatorSecret, "GET", "/v1/admin/activity-stats", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("stats: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		Messages int                       `json:"messages"`
		Peers    map[string]map[string]any `json:"peers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	p := got.Peers[peer]
	if got.Messages != 1 || len(got.Peers) != 1 || len(p) != 3 || p["messages"] != float64(1) || p["messages_per_minute"] != 1.0/60 || len(p["buckets"].([]any)) != 60 {
		t.Fatalf("peer wire stats: %s", raw)
	}
	for _, forbidden := range []string{"private-body-should-not-leak", secret, env.OperatorSecret, "content", "deliveries"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("metadata response leaked %q", forbidden)
		}
	}
	// Breaking the real aggregate must produce an uncached server error,
	// not a successful empty snapshot or a SQL/schema disclosure.
	if _, err := env.Hub.DB.Exec(`ALTER TABLE messages RENAME TO unavailable_messages`); err != nil {
		t.Fatal(err)
	}
	resp, raw = env.Do(env.OperatorSecret, "GET", "/v1/admin/activity-stats", nil)
	if resp.StatusCode != 500 || resp.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(raw), "no such table") {
		t.Fatalf("DB error status=%d cache=%q body=%s", resp.StatusCode, resp.Header.Get("Cache-Control"), raw)
	}
}
