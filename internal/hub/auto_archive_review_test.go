package hub_test

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestReviewFreshDotLinkIsNotIdle(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	p, _ := env.NewPeer("dot", "late-link")
	old := envelope.FormatTime(env.Now.Now().Add(-72 * time.Hour))
	archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=NULL WHERE peer_id=?`, old, p)
	code, err := env.Hub.DotLink(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Hub.ConsumeDotLink(ctx, code); err != nil {
		t.Fatal(err)
	}
	runtimePatch(t, env.Hub, map[string]prefs.Value{"agents.auto_archive_all_kinds": prefs.Bool(true)})
	if n, err := env.Hub.ArchiveIdlePeers(ctx, env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("new link swept=%d %v", n, err)
	}
	var enrolled, reach string
	if err := env.Hub.DB.QueryRow(`SELECT enrolled_at,reachability FROM peers WHERE peer_id=?`, p).Scan(&enrolled, &reach); err != nil {
		t.Fatal(err)
	}
	if enrolled != old || reach != "unknown" {
		t.Fatalf("redemption changed provisioning/display state: %s %s", enrolled, reach)
	}
}

func TestReviewPrincipalSendRacesIdleSweep(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	p, _ := env.NewPeer("session", "race-send")
	b, _ := env.NewPeer("session", "receiver")
	old := envelope.FormatTime(env.Now.Now().Add(-72 * time.Hour))
	archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=? WHERE peer_id=?`, old, old, p)
	var prn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, p).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	body := req("race-send", b)
	body["as"] = p
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	env.Hub.DB.SetMaxOpenConns(1)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.Hub.Now = func() time.Time { once.Do(func() { close(entered); <-release }); return env.Now.Now() }
	done := make(chan error, 1)
	go func() {
		_, _, err := env.Hub.Send(ctx, auth.Caller{Kind: "principal", PrincipalID: prn}, raw, "http_send")
		done <- err
	}()
	<-entered
	waits := env.Hub.DB.Stats().WaitCount
	swept := make(chan error, 1)
	go func() { _, err := env.Hub.ArchiveIdlePeers(ctx, env.Now.Now()); swept <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for env.Hub.DB.Stats().WaitCount == waits && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if env.Hub.DB.Stats().WaitCount == waits {
		close(release)
		t.Fatal("sweep never queued")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-swept; err != nil {
		t.Fatal(err)
	}
	if n := archiveCount(t, env, `SELECT count(*) FROM peers WHERE peer_id=? AND archived_at IS NOT NULL`, p); n != 0 {
		t.Fatal("successful principal send was archived")
	}
}

func TestReviewThreadArchiveOffsetAndFraction(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, as := env.NewPeer("session", "offset-a")
	b, _ := env.NewPeer("session", "offset-b")
	sent := mustSend(t, env, as, req("offset", b))
	archiveExec(t, env, `UPDATE peers SET archived_at='2026-11-14T11:00:00.123+11:00' WHERE peer_id IN (?,?)`, a, b)
	archiveExec(t, env, `UPDATE threads SET unarchived_at='2026-11-14T00:00:00.124Z' WHERE thr_id=?`, sent.ThreadID)
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("later unarchive: %d %v", n, err)
	}
	archiveExec(t, env, `UPDATE threads SET unarchived_at='2026-11-14T00:00:00.122Z' WHERE thr_id=?`, sent.ThreadID)
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("earlier unarchive: %d %v", n, err)
	}
}

func TestReviewFreshEnrollmentIsNotIdle(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	p, token, _, err := env.Hub.AddPeerWith(ctx, hub.AddPeerOpts{Kind: "session", Name: "late-enroll", TTL: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	env.Now.Advance(72 * time.Hour)
	if _, _, _, err := env.Hub.Enrol(ctx, token, ""); err != nil {
		t.Fatal(err)
	}
	n, err := env.Hub.ArchiveIdlePeers(ctx, env.Now.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("newly redeemed peer %s archived immediately", p)
	}
}

func TestReviewPrincipalPresenceRacesIdleSweep(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	p, _ := env.NewPeer("session", "race-presence")
	old := envelope.FormatTime(env.Now.Now().Add(-72 * time.Hour))
	archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=? WHERE peer_id=?`, old, old, p)
	var prn string
	if err := env.Hub.DB.QueryRow(`SELECT prn_id FROM peers WHERE peer_id=?`, p).Scan(&prn); err != nil {
		t.Fatal(err)
	}
	env.Hub.DB.SetMaxOpenConns(1)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.Hub.Now = func() time.Time { once.Do(func() { close(entered); <-release }); return env.Now.Now() }
	presenceDone := make(chan error, 1)
	go func() {
		presenceDone <- env.Hub.SetPresence(ctx, auth.Caller{Kind: "principal", PrincipalID: prn}, p, hub.PresenceReport{State: "working"})
	}()
	<-entered // SetPresence owns BEGIN IMMEDIATE, before committing its fresh presence.
	waits := env.Hub.DB.Stats().WaitCount
	sweepDone := make(chan error, 1)
	go func() { _, err := env.Hub.ArchiveIdlePeers(ctx, env.Now.Now()); sweepDone <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for env.Hub.DB.Stats().WaitCount == waits && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if env.Hub.DB.Stats().WaitCount == waits {
		close(release)
		t.Fatal("sweep never queued")
	}
	close(release) // sweep owns reachMu; it takes the DB writer before the postcommit touch.
	if err := <-presenceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-sweepDone; err != nil {
		t.Fatal(err)
	}
	if n := archiveCount(t, env, `SELECT count(*) FROM peers WHERE peer_id=? AND archived_at IS NOT NULL`, p); n != 0 {
		t.Fatal("peer was archived despite a successful fresh presence report")
	}
}
