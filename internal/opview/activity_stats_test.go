package opview_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Catches a null/short series, invented presence-based activity, or multiple
// clock reads that disagree about the snapshot window.
func TestActivityStatsEmptyAndSingleClock(t *testing.T) {
	env := testhub.Start(t)
	env.NewPeer("session", "present but never sent")
	fixed := time.Date(2026, 11, 10, 0, 30, 45, 123987654, time.FixedZone("offset", 0))
	calls := 0
	env.Hub.Now = func() time.Time {
		calls++
		return fixed.Add(time.Duration(calls-1) * time.Minute)
	}
	got, err := opview.New(env.Hub).ActivityStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Now != "2026-11-10T00:30:45.123Z" || got.WindowEnd != got.Now || got.WindowStart != "2026-11-09T23:30:45.123Z" || got.BucketSeconds != 60 {
		t.Fatalf("clock calls=%d, snapshot=%+v", calls, got)
	}
	if got.Messages != 0 || got.MessagesPerMinute != 0 || got.ActivePeers != 0 || got.Peers == nil || len(got.Peers) != 0 || !reflect.DeepEqual(got.Buckets[:], make([]int, 60)) {
		t.Fatalf("empty snapshot = %+v", got)
	}
}

// Catches rounded/calendar-aligned buckets, inclusive end, excluded start,
// reaction volume, name/current-lifecycle joins, and source/hub active counts.
func TestActivityStatsRollingBoundariesAndSenders(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	fixed := time.Date(2026, 11, 10, 0, 30, 45, 123987654, time.UTC)
	env.Now.Advance(fixed.Sub(env.Now.Now()))
	peer, secret := env.NewPeer("session", "original")
	dot, _ := env.NewPeer("dot", "dot")
	source, _, _, err := env.Hub.AddSource(ctx, "github", "source", peer, nil, "hold")
	if err != nil {
		t.Fatal(err)
	}
	seed := threadSend(t, env, secret, "", []string{env.OperatorPeer}, nil)
	start := time.Date(2026, 11, 9, 23, 30, 45, 123000000, time.UTC)
	cases := []struct {
		sender, kind string
		offset       time.Duration
	}{
		{peer, "message", -time.Millisecond},
		{peer, "message", 0},
		{peer, "approval_request", time.Minute - time.Millisecond},
		{peer, "approval_response", time.Minute},
		{peer, "job_status", time.Minute + time.Millisecond},
		{env.OperatorPeer, "message", 30 * time.Minute},
		{dot, "message", 59 * time.Minute},
		{source, "message", 59 * time.Minute},
		{"hub", "approval_response", time.Hour - time.Millisecond},
		{peer, "reaction", 2 * time.Minute},
		{peer, "message", time.Hour},
		{peer, "message", time.Hour + time.Millisecond},
	}
	for i, tc := range cases {
		dashExec(t, env, `INSERT INTO messages(msg_id,thr_id,seq,sender,kind,created_at,body) VALUES(?,?,?,?,?,?,?)`, ids.New(ids.Msg), seed.ThreadID, i+2, tc.sender, tc.kind, envelope.FormatTime(start.Add(tc.offset)), `{"secret":"must not read body"}`)
	}
	dashExec(t, env, `UPDATE peers SET name='renamed' WHERE peer_id=?`, peer)
	if err := env.Hub.SetPeerArchived(ctx, peer, true); err != nil {
		t.Fatal(err)
	}
	got, err := opview.New(env.Hub).ActivityStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]int, 60)
	want[0], want[1], want[30], want[59] = 2, 2, 1, 3
	if got.Messages != 8 || got.MessagesPerMinute != 8.0/60 || got.ActivePeers != 3 || !reflect.DeepEqual(got.Buckets[:], want) {
		t.Fatalf("snapshot = %+v; want 8 messages, 3 active peers, buckets %v", got, want)
	}
	wantSenders := map[string]int{peer: 4, env.OperatorPeer: 1, dot: 1, source: 1, "hub": 1}
	if len(got.Peers) != len(wantSenders) {
		t.Fatalf("senders = %+v", got.Peers)
	}
	total := 0
	combined := make([]int, 60)
	for sender, count := range wantSenders {
		p, ok := got.Peers[sender]
		if !ok || p.Messages != count || p.MessagesPerMinute != float64(count)/60 || len(p.Buckets) != 60 {
			t.Fatalf("sender %s = %+v, exists=%v", sender, p, ok)
		}
		sum := 0
		for i, n := range p.Buckets {
			sum += n
			combined[i] += n
		}
		if sum != count {
			t.Fatalf("sender %s buckets sum %d, want %d", sender, sum, count)
		}
		total += count
	}
	if total != got.Messages || !reflect.DeepEqual(combined, want) {
		t.Fatalf("sender totals/series disagree: total=%d combined=%v", total, combined)
	}
}

// Catches counting delivery fanout, replay attempts, rejected/rolled-back sends,
// or principal credentials instead of the resolved sender peer.
func TestActivityStatsCommittedSendAccounting(t *testing.T) {
	env := testhub.Start(t)
	principal, secret := env.NewPrincipal("delegator")
	peer, _, err := env.Hub.AddPeer(context.Background(), "session", "delegated", principal)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := env.NewPeer("session", "B")
	c, _ := env.NewPeer("session", "C")
	request := body("count-once", "hold", b, c)
	request["as"] = peer
	dashExec(t, env, `CREATE TRIGGER reject_activity_send BEFORE INSERT ON audit WHEN NEW.action='send' BEGIN SELECT RAISE(ABORT, 'fixture rollback'); END`)
	resp, raw := env.Do(secret, "POST", "/v1/messages", request)
	if resp.StatusCode != 500 {
		t.Fatalf("rollback: %d %s", resp.StatusCode, raw)
	}
	dashExec(t, env, `DROP TRIGGER reject_activity_send`)
	sent := send(t, env, secret, request)
	if len(sent.Deliveries) != 2 {
		t.Fatalf("fanout fixture: %+v", sent)
	}
	resp, raw = env.Do(secret, "POST", "/v1/messages", request)
	if resp.StatusCode != 200 {
		t.Fatalf("replay: %d %s", resp.StatusCode, raw)
	}
	env.SetLimits(peer, 1, 1000)
	rejected := body("rejected", "hold", b)
	rejected["as"] = peer
	resp, raw = env.Do(secret, "POST", "/v1/messages", rejected)
	if resp.StatusCode != 429 {
		t.Fatalf("rate cut: %d %s", resp.StatusCode, raw)
	}
	env.Now.Advance(time.Millisecond)
	got, err := opview.New(env.Hub).ActivityStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages != 1 || got.ActivePeers != 1 || len(got.Peers) != 1 || got.Peers[peer].Messages != 1 || got.Buckets[59] != 1 {
		t.Fatalf("committed accounting: %+v", got)
	}
}

func TestActivityStatsCanceledContext(t *testing.T) {
	env := testhub.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := opview.New(env.Hub).ActivityStats(ctx); err == nil {
		t.Fatal("canceled aggregate should fail, not return an empty successful snapshot")
	}
}
