package push_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/hub"
)

func usageOf(t *testing.T, f *fixture, key string) hub.RouteStat {
	t.Helper()
	stats, err := f.env.Hub.RouteStats(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Routes) != 1 || stats.Routes[0].RouteID != key && key != f.b {
		// key may be the route id; find it.
	}
	for _, r := range stats.Routes {
		if r.RouteID == key {
			return r
		}
	}
	if len(stats.Routes) == 1 {
		return stats.Routes[0]
	}
	t.Fatalf("no route stat for %s in %+v", key, stats.Routes)
	return hub.RouteStat{}
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
	if u.Requests != 3 || u.OK != 3 || u.Fail != 0 || u.LastError != nil {
		t.Fatalf("usage = %+v", u)
	}
	if u.BodyBytes.Samples != 3 || u.LatencyMS.Samples != 3 {
		t.Fatalf("samples %+v %+v", u.BodyBytes, u.LatencyMS)
	}
	if u.BodyBytes.P95 < u.BodyBytes.Avg || u.BodyBytes.P99 < u.BodyBytes.P95 {
		t.Fatalf("percentiles not ordered: %+v", u.BodyBytes)
	}
	var sizes []int
	rows, err := f.env.Hub.DB.Query(`SELECT body_bytes FROM route_attempts WHERE route_id = ? ORDER BY at, id`, f.route)
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
	err = f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_attempts WHERE route_id = ? AND ifnull(error_class,'') LIKE '%`+texts[2][:8]+`%'`, f.route).Scan(&leaked)
	if err != nil || leaked != 0 {
		t.Fatalf("usage stored request text: %d %v", leaked, err)
	}
	stats, err := f.env.Hub.RouteStats(context.Background(), f.b)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deliveries.Pushed != 3 || len(stats.Routes) != 1 || stats.Routes[0].Requests != 3 || stats.Routes[0].OK != 3 {
		t.Fatalf("stats = %+v routes %+v", stats.Deliveries, stats.Routes)
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
	stats, err := f.env.Hub.RouteStats(context.Background(), f.b)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Routes) != 1 || stats.Routes[0].Requests != 0 || stats.Deliveries.Pushed != 0 {
		t.Fatalf("hold recorded a push: %+v %+v", stats.Deliveries, stats.Routes)
	}
}

func TestPushStatsFailureDoesNotFailDelivery(t *testing.T) {
	f := setup(t, always(http.StatusOK))
	m := f.msg("wake")
	m["content"] = map[string]any{"text": "CANARY-BODY"}
	sr := f.send(f.aSec, m)
	if _, err := f.env.Hub.DB.Exec(`DROP TABLE route_attempts`); err != nil {
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
	if u.Requests != 1 || u.OK != 1 || u.Fail != 0 {
		t.Fatalf("started usage = %+v", u)
	}
	var oks int
	if err := f.env.Hub.DB.QueryRow(`SELECT count(*) FROM route_attempts WHERE route_id = ? AND outcome = 'ok'`, f.route).Scan(&oks); err != nil || oks != 1 {
		t.Fatalf("ok attempts = %d, %v", oks, err)
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
	if bad.Requests != 1 || bad.Fail != 1 || bad.OK != 0 || bad.LastError == nil || *bad.LastError != "http_5xx" {
		t.Fatalf("failed routine usage = %+v", bad)
	}
	if bad.LastSuccessAt != nil {
		t.Fatal("failure recorded a last success")
	}
}
