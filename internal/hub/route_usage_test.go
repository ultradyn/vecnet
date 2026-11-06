package hub_test

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
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
	if v, _ := hub.PercentileNearestRank([]int64{10, 40}, 95); v != 40 {
		t.Fatalf("p95 of {10,40} = %d, want 40", v)
	}
}

func TestRouteAttemptPruneKeepsNewest1000And7Days(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	peer, _ := env.NewPeer("session", "p")
	route, _, err := env.Hub.AddRoute(ctx, "peer", peer, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	base := env.Now.Now()
	// Older than the window: the next insert must drop it even though the route
	// is nowhere near 1000 rows yet.
	insertAttempt(t, env, route, "dly_old", base.Add(-8*24*time.Hour), hub.AttemptOK, 200, "", 1, 1)
	for i := 1; i <= store.RouteAttemptKeep+1; i++ {
		outcome := hub.AttemptOK
		class := ""
		if i%10 == 0 {
			outcome = hub.AttemptFail
			class = "http_5xx"
		}
		insertAttempt(t, env, route, "dly_"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Millisecond), outcome, 200, class, i, i)
	}
	var n, minB, maxB int
	if err := env.Hub.DB.QueryRow(`SELECT count(*), min(body_bytes), max(body_bytes) FROM route_attempts WHERE route_id = ?`, route).Scan(&n, &minB, &maxB); err != nil {
		t.Fatal(err)
	}
	if n != store.RouteAttemptKeep {
		t.Fatalf("kept %d attempts, want %d", n, store.RouteAttemptKeep)
	}
	if minB != 2 || maxB != store.RouteAttemptKeep+1 {
		t.Fatalf("kept bodies %d..%d, want 2..%d (the oldest recent row and the 8-day row are gone)", minB, maxB, store.RouteAttemptKeep+1)
	}
	var old int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM route_attempts WHERE route_id = ? AND delivery_id = 'dly_old'`, route).Scan(&old); err != nil || old != 0 {
		t.Fatalf("8-day-old attempt still stored: %d %v", old, err)
	}
	stats, err := env.Hub.RouteStats(ctx, route)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Window.MaxRows != store.RouteAttemptKeep || stats.Window.MaxAgeHours != 168 {
		t.Fatalf("window = %+v", stats.Window)
	}
	if len(stats.Routes) != 1 || stats.Routes[0].Requests != store.RouteAttemptKeep {
		t.Fatalf("read-time count = %+v", stats.Routes)
	}
}

func TestRouteStatsPercentilesOverTheWindow(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	peer, _ := env.NewPeer("grokbot", "g")
	route, _, err := env.Hub.AddRoute(ctx, "peer", peer, "http://127.0.0.1:9/hook")
	if err != nil {
		t.Fatal(err)
	}
	base := env.Now.Now()
	for i := 1; i <= 300; i++ {
		outcome := hub.AttemptOK
		class := ""
		status := 200
		if i%10 == 0 {
			outcome = hub.AttemptFail
			class = "http_5xx"
			status = 503
		}
		insertAttempt(t, env, route, "dly_"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Millisecond), outcome, status, class, i, i)
	}
	stats, err := env.Hub.RouteStats(ctx, peer)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Subject.Kind != "peer" || len(stats.Routes) != 1 {
		t.Fatalf("subject %+v routes %d", stats.Subject, len(stats.Routes))
	}
	u := stats.Routes[0]
	if u.Requests != 300 || u.OK != 270 || u.Fail != 30 || u.Refused != 0 {
		t.Fatalf("counts = %+v", u)
	}
	// 1..300: avg 150.5, p95 rank 285, p99 rank 297.
	if u.BodyBytes.Avg != 150.5 || u.BodyBytes.P95 != 285 || u.BodyBytes.P99 != 297 || u.BodyBytes.Samples != 300 {
		t.Fatalf("body = %+v", u.BodyBytes)
	}
	if u.LatencyMS.P95 != 285 || u.LatencyMS.P99 != 297 {
		t.Fatalf("latency = %+v", u.LatencyMS)
	}
	if u.LastError == nil || *u.LastError != "http_5xx" {
		t.Fatalf("last error = %v", u.LastError)
	}
	insertAttempt(t, env, route, "dly_later", base.Add(time.Second), hub.AttemptOK, 200, "", 8, 1)
	stats, err = env.Hub.RouteStats(ctx, route)
	if err != nil {
		t.Fatal(err)
	}
	u = stats.Routes[0]
	if u.Requests != 301 || u.OK != 271 || u.LastError == nil || *u.LastError != "http_5xx" {
		t.Fatalf("success cleared the last error: %+v", u)
	}
	if u.LastStatus == nil || *u.LastStatus != 200 || u.LastSuccessAt == nil {
		t.Fatalf("last success = status %v at %v", u.LastStatus, u.LastSuccessAt)
	}
	var leaked int
	if err := env.Hub.DB.QueryRow(`SELECT count(*) FROM route_attempts WHERE error_class LIKE '% %' OR error_class LIKE '%body%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("error_class stored free text: %d %v", leaked, err)
	}
}

func TestRouteAttemptRejectsBodyText(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	err := env.Hub.DB.Tx(ctx, func(tx *sql.Tx) error {
		return store.InsertRouteAttempt(ctx, tx, store.RouteAttemptInsert{
			RouteID: "route_x", DeliveryID: "dly_x",
			At: envelope.FormatTime(env.Now.Now()), Cutoff: envelope.FormatTime(env.Now.Now().Add(-7 * 24 * time.Hour)),
			Outcome: hub.AttemptFail, ErrorClass: sql.NullString{String: "peer said " + strings.Repeat("no", 20), Valid: true},
			BodyBytes: 1, LatencyMS: 1,
		})
	})
	if err == nil {
		t.Fatal("error_class accepted response text")
	}
}

func insertAttempt(t *testing.T, env *testhub.Env, route, delivery string, at time.Time, outcome string, status int, class string, body, latency int) {
	t.Helper()
	err := env.Hub.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		var httpStatus sql.NullInt64
		if status != 0 {
			httpStatus = sql.NullInt64{Int64: int64(status), Valid: true}
		}
		var errClass sql.NullString
		if class != "" {
			errClass = sql.NullString{String: class, Valid: true}
		}
		return store.InsertRouteAttempt(context.Background(), tx, store.RouteAttemptInsert{
			RouteID: route, DeliveryID: delivery,
			At: envelope.FormatTime(at), Cutoff: envelope.FormatTime(at.Add(-7 * 24 * time.Hour)),
			Outcome: outcome, HTTPStatus: httpStatus, ErrorClass: errClass,
			BodyBytes: body, LatencyMS: latency,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
