package push_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
)

func usageOf(t *testing.T, f *fixture, key string) hub.RouteUsage {
	t.Helper()
	got, err := f.env.Hub.RouteUsages(context.Background(), []string{key})
	if err != nil {
		t.Fatal(err)
	}
	u, ok := got[key]
	if !ok {
		t.Fatalf("no usage for %s", key)
	}
	return u
}

func TestPushRecordsBodySizes(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	texts := []string{"a", strings.Repeat("b", 1000), strings.Repeat("c", 4000)}
	for _, text := range texts {
		m := f.msg("wake")
		m["content"] = map[string]any{"text": text}
		f.send(f.aSec, m)
		if n := f.step(); n != 1 {
			t.Fatalf("step = %d, want 1", n)
		}
	}
	hits := f.rcv.Hits()
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(hits))
	}
	u := usageOf(t, f, f.route)
	if u.Requests != 3 || u.Successes != 3 || u.Failures != 0 || u.LastError != nil {
		t.Fatalf("usage = %+v", u)
	}
	if u.BodyBytes.Samples != 3 || u.LatencyMS.Samples != 3 {
		t.Fatalf("samples %+v %+v", u.BodyBytes, u.LatencyMS)
	}
	if u.BodyBytes.P95 < u.BodyBytes.Avg || u.BodyBytes.P99 < u.BodyBytes.P95 {
		t.Fatalf("percentiles not ordered: %+v", u.BodyBytes)
	}
	var sizes []int
	rows, err := f.env.Hub.DB.Query(`SELECT body_bytes FROM route_usage_samples WHERE route_key = ? ORDER BY slot`, f.route)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] >= sizes[1] || sizes[1] >= sizes[2] {
		t.Fatalf("body sizes = %v, want three increasing counts", sizes)
	}
	for i, h := range hits {
		if sizes[i] != len(h.Body) {
			t.Fatalf("sample %d = %d bytes, request was %d", i, sizes[i], len(h.Body))
		}
		if strings.Contains(string(h.Body), texts[i]) && sizes[i] == len(texts[i]) {
			t.Fatalf("sample stored the text, not the request size")
		}
	}
	var leaked int
	err = f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_usage WHERE route_key = ? AND ifnull(last_error,'') LIKE '%`+texts[2][:8]+`%'`, f.route).Scan(&leaked)
	if err != nil || leaked != 0 {
		t.Fatalf("usage stored request text: %d %v", leaked, err)
	}
	routes, err := f.env.Hub.ListRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var listed *hub.RouteUsage
	for i := range routes {
		if routes[i].RouteID == f.route {
			listed = routes[i].Usage
		}
	}
	if listed == nil || listed.Requests != 3 {
		t.Fatalf("GET routes usage = %+v", listed)
	}
}

func TestHoldDoesNotUseTheRoute(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	f.send(f.aSec, f.msg("hold"))
	if n := f.step(); n != 0 {
		t.Fatalf("hold step = %d, want 0", n)
	}
	if hits := f.rcv.Hits(); len(hits) != 0 {
		t.Fatalf("hold pushed %d times", len(hits))
	}
	got, err := f.env.Hub.RouteUsages(context.Background(), []string{f.route})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[f.route]; ok {
		t.Fatal("hold incremented route usage")
	}
}

func TestPushStatsFailureDoesNotFailDelivery(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	m := f.msg("wake")
	m["content"] = map[string]any{"text": "CANARY-BODY"}
	sr := f.send(f.aSec, m)
	if _, err := f.env.Hub.DB.Exec(`DROP TABLE route_usage_samples`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Hub.DB.Exec(`DROP TABLE route_usage`); err != nil {
		t.Fatal(err)
	}
	if n := f.step(); n != 1 {
		t.Fatalf("step = %d, want 1 even though usage cannot be written", n)
	}
	if len(f.rcv.Hits()) != 1 {
		t.Fatalf("hits = %d", len(f.rcv.Hits()))
	}
	var state string
	if err := f.env.Hub.DB.QueryRow(`SELECT state FROM deliveries WHERE dly_id = ?`, sr.Deliveries[0].DeliveryID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("state = %q, want accepted", state)
	}
}

func TestGrokbotPushRecordsUsage(t *testing.T) {
	f := setup(t, nil)
	f.addGrokbot(started)
	m := f.msg("wake")
	m["content"] = map[string]any{"text": "grok-body"}
	f.send(f.aSec, m)
	if n := f.step(); n != 1 {
		t.Fatalf("step = %d", n)
	}
	u := usageOf(t, f, f.route)
	if u.Requests != 1 || u.Successes != 1 || u.Failures != 0 {
		t.Fatalf("started usage = %+v", u)
	}
	var kind string
	if err := f.env.Hub.DB.QueryRow(`SELECT kind FROM route_usage WHERE route_key = ?`, f.route).Scan(&kind); err != nil || kind != hub.RouteKindGrokbot {
		t.Fatalf("kind = %q, %v", kind, err)
	}
	hits := f.rcv.Hits()
	if len(hits) != 1 || int(u.BodyBytes.Avg) != len(hits[0].Body) {
		t.Fatalf("body avg %v, hit %d", u.BodyBytes.Avg, len(hits[0].Body))
	}

	f2 := setup(t, nil)
	f2.addGrokbot(always(http.StatusInternalServerError))
	f2.send(f2.aSec, f2.msg("wake"))
	if n := f2.step(); n != 1 {
		t.Fatalf("step = %d", n)
	}
	bad := usageOf(t, f2, f2.route)
	if bad.Requests != 1 || bad.Failures != 1 || bad.Successes != 0 || bad.LastError == nil || *bad.LastError != "HTTP 500" {
		t.Fatalf("failed routine usage = %+v", bad)
	}
	if bad.LastSuccessAt != nil {
		t.Fatal("failure recorded a last success")
	}
}
