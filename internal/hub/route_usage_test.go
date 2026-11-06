package hub_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestPercentileNearestRank(t *testing.T) {
	if _, ok := hub.PercentileNearestRank(nil, 95); ok {
		t.Fatal("empty sample reported a percentile")
	}
	if _, ok := hub.PercentileNearestRank([]int64{1}, 0); ok {
		t.Fatal("p=0 reported a percentile")
	}
	if _, ok := hub.PercentileNearestRank([]int64{1}, 101); ok {
		t.Fatal("p=101 reported a percentile")
	}
	if v, ok := hub.PercentileNearestRank([]int64{7}, 99); !ok || v != 7 {
		t.Fatalf("single sample p99 = %d ok=%v, want 7", v, ok)
	}
	samples := make([]int64, 100)
	for i := range samples {
		samples[i] = int64(i + 1)
	}
	if v, _ := hub.PercentileNearestRank(samples, 95); v != 95 {
		t.Fatalf("p95 of 1..100 = %d, want 95", v)
	}
	if v, _ := hub.PercentileNearestRank(samples, 99); v != 99 {
		t.Fatalf("p99 of 1..100 = %d, want 99", v)
	}
	if v, _ := hub.PercentileNearestRank(samples, 100); v != 100 {
		t.Fatalf("p100 of 1..100 = %d, want 100", v)
	}
	// Unsorted input is not the contract; the read path sorts first.
	// A two-point sample: p95 is the higher one (ceil(0.95*2)=2).
	if v, _ := hub.PercentileNearestRank([]int64{10, 40}, 95); v != 40 {
		t.Fatalf("p95 of {10,40} = %d, want 40", v)
	}
}

func TestRouteUsageRingKeepsLifetimeCounters(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	base := env.Now.Now()
	const key = "route_ring"
	for i := 1; i <= 300; i++ {
		ok := i%10 != 0
		status := 503
		if ok {
			status = 200
		}
		env.Hub.NoteRouteAttempt(ctx, hub.RouteAttempt{
			Key: key, Kind: hub.RouteKindHTTP, BodyBytes: i,
			Latency: time.Duration(i) * time.Millisecond, HasLatency: true,
			OK: ok, Status: status, Error: "HTTP 503",
			At: base.Add(time.Duration(i) * time.Millisecond),
		})
	}
	got, err := env.Hub.RouteUsages(ctx, []string{key, "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["missing"]; ok {
		t.Fatal("a route with no attempts should be absent")
	}
	u, ok := got[key]
	if !ok {
		t.Fatal("usage missing")
	}
	if u.Requests != 300 || u.Successes != 270 || u.Failures != 30 {
		t.Fatalf("counters = %d ok %d fail %d, want 300/270/30", u.Requests, u.Successes, u.Failures)
	}
	if u.BodyBytes.Samples != hub.RouteUsageSamples || u.LatencyMS.Samples != hub.RouteUsageSamples {
		t.Fatalf("samples body %d latency %d, want %d", u.BodyBytes.Samples, u.LatencyMS.Samples, hub.RouteUsageSamples)
	}
	// The ring holds attempts 45..300. avg = 172.5. p95 rank 244 → 288. p99 rank 254 → 298.
	if u.BodyBytes.Avg != 172.5 || u.BodyBytes.P95 != 288 || u.BodyBytes.P99 != 298 {
		t.Fatalf("body dist = %+v, want avg 172.5 p95 288 p99 298", u.BodyBytes)
	}
	if u.LatencyMS.Avg != 172.5 || u.LatencyMS.P95 != 288 || u.LatencyMS.P99 != 298 {
		t.Fatalf("latency dist = %+v, want the same numbers in milliseconds", u.LatencyMS)
	}
	if u.LastError == nil || *u.LastError != "HTTP 503" || u.LastStatus == nil || *u.LastStatus != 503 {
		t.Fatalf("last failure = error %v status %v", u.LastError, u.LastStatus)
	}
	wantUsed := envelope.FormatTime(base.Add(300 * time.Millisecond))
	wantOK := envelope.FormatTime(base.Add(299 * time.Millisecond))
	if u.LastUsedAt != wantUsed || u.LastSuccessAt == nil || *u.LastSuccessAt != wantOK {
		t.Fatalf("last used %s success %v, want %s and %s", u.LastUsedAt, u.LastSuccessAt, wantUsed, wantOK)
	}

	// A later success does not erase the last error.
	later := base.Add(time.Second)
	env.Hub.NoteRouteAttempt(ctx, hub.RouteAttempt{
		Key: key, Kind: hub.RouteKindHTTP, BodyBytes: 8,
		Latency: time.Millisecond, HasLatency: true,
		OK: true, Status: 200, At: later,
	})
	got, err = env.Hub.RouteUsages(ctx, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	u = got[key]
	if u.Requests != 301 || u.Successes != 271 || u.Failures != 30 {
		t.Fatalf("after success counters = %d/%d/%d", u.Requests, u.Successes, u.Failures)
	}
	if u.LastError == nil || *u.LastError != "HTTP 503" {
		t.Fatalf("success cleared last error: %v", u.LastError)
	}
	if u.LastStatus == nil || *u.LastStatus != 200 || u.LastSuccessAt == nil || *u.LastSuccessAt != envelope.FormatTime(later) {
		t.Fatalf("last status %v success %v", u.LastStatus, u.LastSuccessAt)
	}

	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM route_usage_samples WHERE route_key = ?`, key).Scan(&n); err != nil || n != hub.RouteUsageSamples {
		t.Fatalf("ring rows = %d, %v", n, err)
	}
	var leaked int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM pragma_table_info('route_usage_samples') WHERE name LIKE '%body%' AND name != 'body_bytes'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("sample table stores something other than a byte count: %d %v", leaked, err)
	}
}

func TestNoteRouteAttemptSwallowsBadWrite(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	env.Hub.NoteRouteAttempt(ctx, hub.RouteAttempt{Key: "route_bad", Kind: "nope", OK: true, Status: 200, At: env.Now.Now()})
	long := strings.Repeat("e", 500)
	env.Hub.NoteRouteAttempt(ctx, hub.RouteAttempt{
		Key: "route_clip", Kind: hub.RouteKindHTTP, BodyBytes: -4, OK: false, Status: 0,
		Error: long, At: env.Now.Now(), HasLatency: true, Latency: -time.Second,
	})
	got, err := env.Hub.RouteUsages(ctx, []string{"route_bad", "route_clip"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["route_bad"]; ok {
		t.Fatal("bad kind was stored")
	}
	u := got["route_clip"]
	if u.Requests != 1 || u.Failures != 1 || u.LastError == nil || len(*u.LastError) != 200 {
		t.Fatalf("clipped failure = %+v", u)
	}
	if u.LastStatus != nil {
		t.Fatalf("status with no HTTP response = %d", *u.LastStatus)
	}
	if u.BodyBytes.Avg != 0 || u.LatencyMS.Avg != 0 {
		t.Fatalf("negative body/latency not clamped: %+v %+v", u.BodyBytes, u.LatencyMS)
	}
}

func TestRemoveRouteDropsUsage(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	peer, _ := env.NewPeer("grokbot", "routed")
	id, _, err := env.Hub.AddRoute(ctx, "peer", peer, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	env.Hub.NoteRouteAttempt(ctx, hub.RouteAttempt{
		Key: id, Kind: hub.RouteKindHTTP, BodyBytes: 12, HasLatency: true, Latency: 2 * time.Millisecond,
		OK: true, Status: 200, At: env.Now.Now(),
	})
	routes, err := env.Hub.ListRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, r := range routes {
		if r.RouteID != id {
			continue
		}
		saw = true
		if r.Usage == nil || r.Usage.Requests != 1 || r.Usage.Successes != 1 {
			t.Fatalf("list usage = %+v", r.Usage)
		}
	}
	if !saw {
		t.Fatal("route missing from list")
	}
	if err := env.Hub.RemoveRoute(ctx, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM route_usage WHERE route_key = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("usage rows after remove = %d, %v", n, err)
	}
}
