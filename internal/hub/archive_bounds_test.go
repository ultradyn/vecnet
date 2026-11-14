package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestArchiveSweepBoundsAndAgentlessThread(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	now := env.Now.Now()
	old := envelope.FormatTime(now.Add(-3 * 24 * time.Hour))
	archiveExec(t, env, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<201)
 INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) SELECT 'peer_bound_'||i,(SELECT prn_id FROM peers WHERE peer_id=?),'bound-'||i,'session',? FROM n`, env.OperatorPeer, old)
	if n, err := env.Hub.ArchiveIdlePeers(ctx, now); err != nil || n != 200 {
		t.Fatalf("peer batch=%d %v", n, err)
	}
	if n, err := env.Hub.ArchiveIdlePeers(ctx, now); err != nil || n != 1 {
		t.Fatalf("peer remainder=%d %v", n, err)
	}
	archiveExec(t, env, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<501)
 INSERT INTO threads(thr_id,created_by,created_at,last_message_at) SELECT 'thr_bound_'||i,?,?,? FROM n`, env.OperatorPeer, old, old)
	archiveExec(t, env, `INSERT INTO thread_participants(thr_id,peer_id,joined_at) SELECT thr_id,'peer_bound_1',? FROM threads`, old)
	archiveExec(t, env, `INSERT INTO threads(thr_id,created_by,created_at,last_message_at) VALUES('thr_operator_only',?,?,?)`, env.OperatorPeer, old, old)
	archiveExec(t, env, `INSERT INTO thread_participants(thr_id,peer_id,joined_at) VALUES('thr_operator_only',?,?)`, env.OperatorPeer, old)
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, now); err != nil || n != 500 {
		t.Fatalf("thread batch=%d %v", n, err)
	}
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, now); err != nil || n != 1 {
		t.Fatalf("thread remainder=%d %v", n, err)
	}
	if n := archiveCount(t, env, `SELECT count(*) FROM threads WHERE thr_id='thr_operator_only' AND archived_at IS NOT NULL`); n != 0 {
		t.Fatal("operator-only thread auto archived")
	}
}

func TestMessageRevivalWakesOperatorLog(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	_, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	sent := mustSend(t, env, as, req("wake-archive", b))
	if err := env.Hub.SetThreadArchived(ctx, sent.ThreadID, true); err != nil {
		t.Fatal(err)
	}
	wakeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake := env.Hub.Log.Wait(wakeCtx, []string{env.OperatorPeer})
	body := req("wake-revival", b)
	delete(body, "recipients")
	body["thread_id"] = sent.ThreadID
	mustSend(t, env, as, body)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("non-participant operator was not notified after revival commit")
	}
}
