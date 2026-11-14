package hub_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func archiveExec(t *testing.T, env *testhub.Env, q string, args ...any) {
	t.Helper()
	if _, err := env.Hub.DB.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func archiveCount(t *testing.T, env *testhub.Env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Hub.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestArchiveIdlePeersRules(t *testing.T) {
	ctx := context.Background()
	env := testhub.Start(t)
	now := env.Now.Now()
	old := envelope.FormatTime(now.Add(-3 * 24 * time.Hour))
	var wanted []string
	for _, tc := range []struct {
		name, kind, change string
		want               bool
	}{
		{"idle", "session", "", true}, {"future-presence", "session", "presence_at='2099-01-01T00:00:00Z'", true},
		{"disabled", "session", "disabled=1", false}, {"recent", "session", "last_seen='" + envelope.FormatTime(now.Add(-time.Hour)) + "'", false},
		{"unarchived", "session", "unarchived_at='" + envelope.FormatTime(now.Add(-time.Hour)) + "'", false},
		{"boundary", "session", "last_seen='" + envelope.FormatTime(now.Add(-48*time.Hour)) + "',enrolled_at='" + envelope.FormatTime(now.Add(-48*time.Hour)) + "'", false},
		{"missing", "session", "last_seen=NULL,enrolled_at=''", false},
		{"malformed", "session", "last_seen='bad-time'", false},
		{"launcher", "launcher", "", false}, {"grok", "grokbot", "", false}, {"source", "source", "", false},
		{"stream", "session", "", false},
	} {
		p, _ := env.NewPeer(tc.kind, tc.name)
		archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=? WHERE peer_id=?`, old, old, p)
		if tc.change != "" {
			archiveExec(t, env, `UPDATE peers SET `+tc.change+` WHERE peer_id=?`, p)
		}
		if tc.name == "stream" {
			env.Hub.StreamOpened(ctx, p)
			t.Cleanup(func() { env.Hub.StreamClosed(ctx, p) })
			archiveExec(t, env, `UPDATE peers SET last_seen=? WHERE peer_id=?`, old, p)
		}
		if tc.want {
			wanted = append(wanted, p)
		}
	}
	n, err := env.Hub.ArchiveIdlePeers(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("idle sweep=%d %v", n, err)
	}
	for _, p := range wanted {
		if n := archiveCount(t, env, `SELECT count(*) FROM peers WHERE peer_id=? AND archived_at IS NOT NULL AND disabled=1`, p); n != 1 {
			t.Fatalf("peer %s not archived", p)
		}
		if n := archiveCount(t, env, `SELECT count(*) FROM credentials WHERE subject_id=? AND revoked_at IS NULL`, p); n != 0 {
			t.Fatal("credentials not revoked")
		}
		if n := archiveCount(t, env, `SELECT count(*) FROM audit WHERE actor='hub' AND action='peer_archive' AND json_extract(detail,'$.auto')=1 AND json_extract(detail,'$.idle_seconds')=259200 AND target_ids LIKE ?`, "%"+p+"%"); n != 1 {
			t.Fatal("auto audit missing")
		}
	}
	runtimePatch(t, env.Hub, map[string]prefs.Value{"agents.auto_archive_all_kinds": prefs.Bool(true)})
	n, err = env.Hub.ArchiveIdlePeers(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("all kinds sweep=%d %v", n, err)
	}
	runtimePatch(t, env.Hub, map[string]prefs.Value{"agents.auto_archive_after": prefs.Duration(0)})
	p, _ := env.NewPeer("session", "off")
	archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=? WHERE peer_id=?`, old, old, p)
	n, err = env.Hub.ArchiveIdlePeers(ctx, now)
	if err != nil || n != 0 {
		t.Fatalf("off=%d %v", n, err)
	}
}

func TestPeerUnarchiveGraceAndTimestampParsing(t *testing.T) {
	ctx := context.Background()
	env := testhub.Start(t)
	now := env.Now.Now()
	p, _ := env.NewPeer("session", "grace")
	// RFC3339 offsets and fractions must be compared as instants, not strings.
	archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=? WHERE peer_id=?`, now.Add(-3*24*time.Hour).Format(time.RFC3339), now.Add(-time.Hour).In(time.FixedZone("west", -12*3600)).Format(time.RFC3339Nano), p)
	if n, err := env.Hub.ArchiveIdlePeers(ctx, now); err != nil || n != 0 {
		t.Fatalf("parsed recent timestamp=%d %v", n, err)
	}
	archiveExec(t, env, `UPDATE peers SET last_seen=?,enrolled_at=? WHERE peer_id=?`, now.Add(-3*24*time.Hour).In(time.FixedZone("east", 12*3600)).Format(time.RFC3339Nano), envelope.FormatTime(now.Add(-3*24*time.Hour)), p)
	if n, err := env.Hub.ArchiveIdlePeers(ctx, now); err != nil || n != 1 {
		t.Fatalf("parsed old timestamp=%d %v", n, err)
	}
	if err := env.Hub.SetPeerArchived(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	var at sql.NullString
	if err := env.Hub.DB.QueryRow(`SELECT unarchived_at FROM peers WHERE peer_id=?`, p).Scan(&at); err != nil || !at.Valid || at.String != envelope.FormatTime(now) {
		t.Fatalf("grace=%v %v", at, err)
	}
	// Re-enabling does not remove the grace timestamp.
	if err := env.Hub.SetPeerDisabled(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	archiveExec(t, env, `UPDATE peers SET last_seen=NULL WHERE peer_id=?`, p)
	if n, err := env.Hub.ArchiveIdlePeers(ctx, now); err != nil || n != 0 {
		t.Fatalf("grace sweep=%d %v", n, err)
	}
}

func TestArchiveAbandonedThreadsRules(t *testing.T) {
	ctx := context.Background()
	env := testhub.Start(t)
	a, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	sent := mustSend(t, env, as, req("majority", b))
	id := sent.ThreadID
	// Operator participants never contribute to either side of the fraction.
	archiveExec(t, env, `INSERT INTO thread_participants(thr_id,peer_id,joined_at) VALUES(?,?,?)`, id, env.OperatorPeer, envelope.FormatTime(env.Now.Now()))
	archiveExec(t, env, `UPDATE peers SET archived_at=? WHERE peer_id=?`, envelope.FormatTime(env.Now.Now().Add(-time.Minute)), a)
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("50 < 51=%d %v", n, err)
	}
	runtimePatch(t, env.Hub, map[string]prefs.Value{"threads.auto_archive_share": prefs.Int(50)})
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, env.Now.Now()); err != nil || n != 1 {
		t.Fatalf("50 boundary=%d %v", n, err)
	}
	if n := archiveCount(t, env, `SELECT count(*) FROM threads WHERE thr_id=? AND archived_by='hub'`, id); n != 1 {
		t.Fatal("not hub archived")
	}
	if n := archiveCount(t, env, `SELECT count(*) FROM audit WHERE actor='hub' AND action='thread_archive' AND json_extract(detail,'$.auto')=1 AND json_extract(detail,'$.reason')='participants_archived'`); n != 1 {
		t.Fatal("batch audit missing")
	}
	if err := env.Hub.SetThreadArchived(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("unarchive respected=%d %v", n, err)
	}
	env.Now.Advance(time.Second)
	// Manual archival applies the rule immediately to this peer's threads.
	if err := env.Hub.SetPeerArchived(ctx, b, true); err != nil {
		t.Fatal(err)
	}
	if n := archiveCount(t, env, `SELECT count(*) FROM threads WHERE thr_id=? AND archived_by='hub'`, id); n != 1 {
		t.Fatal("manual peer archive did not re-evaluate thread")
	}
	if err := env.Hub.SetThreadArchived(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	runtimePatch(t, env.Hub, map[string]prefs.Value{"threads.auto_archive_share": prefs.Int(0)})
	env.Now.Advance(time.Second)
	archiveExec(t, env, `UPDATE peers SET archived_at=? WHERE peer_id=?`, envelope.FormatTime(env.Now.Now()), a)
	if n, err := env.Hub.ArchiveAbandonedThreads(ctx, env.Now.Now()); err != nil || n != 0 {
		t.Fatalf("off=%d %v", n, err)
	}
}

func TestArchiveSweepBatchInvalidationAndTick(t *testing.T) {
	env := testhub.Start(t)
	ctx := context.Background()
	a, as := env.NewPeer("session", "A")
	b, _ := env.NewPeer("session", "B")
	first := mustSend(t, env, as, req("batch1", b))
	second := mustSend(t, env, as, req("batch2", b))
	old := envelope.FormatTime(env.Now.Now().Add(-3 * 24 * time.Hour))
	archiveExec(t, env, `UPDATE peers SET enrolled_at=?,last_seen=? WHERE peer_id IN (?,?)`, old, old, a, b)
	env.Hub.Tick(ctx, false)
	if n := archiveCount(t, env, `SELECT count(*) FROM threads WHERE archived_at IS NOT NULL`); n != 0 {
		t.Fatal("fast tick archived threads")
	}
	env.Hub.Tick(ctx, true)
	if n := archiveCount(t, env, `SELECT count(*) FROM threads WHERE archived_by='hub' AND thr_id IN (?,?)`, first.ThreadID, second.ThreadID); n != 2 {
		t.Fatalf("slow tick archived=%d", n)
	}
	evs, err := env.Hub.Log.Read(ctx, []string{env.OperatorPeer}, eventlog.Cursor{Epoch: env.Hub.Log.Epoch()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type != "operator_view" {
			continue
		}
		var d struct {
			Reason   string  `json:"reason"`
			ThreadID *string `json:"thread_id"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason == "archive" {
			n++
			if d.ThreadID != nil {
				t.Fatal("batch invalidation had thread id")
			}
		}
	}
	if n != 1 {
		t.Fatalf("batch invalidations=%d", n)
	}
}
