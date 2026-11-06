package hub_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
)

func TestRetentionOneRowSliceIndependentOfFormerPrincipals(t *testing.T) {
	for _, analyze := range []bool{false, true} {
		small := 0
		for _, n := range []int{100, 10000} {
			cfg := hub.DefaultConfig()
			cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Target: time.Hour}
			h, _, clock := preferencesHubClock(t, cfg)
			ctx := context.Background()
			if _, err := h.DB.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES('prn_initial','synthetic','2026-01-01T00:00:00.000Z');
    INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES('peer_target','prn_initial','synthetic','session','2026-01-01T00:00:00.000Z');
    WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<=?)
    INSERT INTO principals(prn_id,name,created_at) SELECT 'prn_move_'||i,'synthetic','2026-01-01T00:00:00.000Z' FROM n`, n); err != nil {
				t.Fatal(err)
			}
			c := retentionEvent(t, h, "peer_target", clock.Now().Add(-8*24*time.Hour))
			retentionAck(t, h, "peer_target", "*", c)
			st := events.NewStore(h)
			st.Key = make([]byte, 32)
			// Establish a real moved/verified subscription, then clone its historical
			// state in bulk. This measures prune page work at the same 100x history
			// sizes without repeating unrelated move/audit/encryption transactions.
			setupStart := time.Now()
			if err := h.MovePeer(ctx, "peer_target", "prn_move_1"); err != nil {
				t.Fatal(err)
			}
			sub, _, err := st.Upsert(ctx, events.Subscription{PrincipalKind: events.KindVecnet, PrincipalID: "prn_move_1", PeerID: "peer_target", Name: events.Name, URL: "https://synthetic.invalid/callback", Cursor: eventlog.Cursor{Epoch: c.Epoch}}, "synthetic", events.MaxTTL, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.MarkVerified(ctx, sub.ID); err != nil {
				t.Fatal(err)
			}
			tx, err := h.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer tx.Rollback()
				if _, err := tx.ExecContext(ctx, `WITH RECURSIVE n(i) AS (VALUES(2) UNION ALL SELECT i+1 FROM n WHERE i<?)
    INSERT INTO event_subscriptions(sub_id,principal_kind,principal_id,peer_id,name,arguments,url,secret_enc,cursor_epoch,cursor_seq,types,expires_at,verified_at,token_expires_at,created_at,updated_at,refresh_before_at)
    SELECT 'sub_former_'||i,s.principal_kind,'prn_move_'||i,s.peer_id,s.name,s.arguments,s.url,s.secret_enc,s.cursor_epoch,s.cursor_seq,s.types,s.expires_at,s.verified_at,s.token_expires_at,s.created_at,s.updated_at,s.refresh_before_at
    FROM n CROSS JOIN event_subscriptions s WHERE s.sub_id=?`, n, sub.ID); err != nil {
					t.Fatal(err)
				}
				// Each former subscriber has a terminal exact copy, which covers its
				// cursor but must not introduce a pending/retryable-copy pin.
				if _, err := tx.ExecContext(ctx, `INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal)
    SELECT sub_id,?,?,'{}',?,?,'http_410' FROM event_subscriptions WHERE peer_id='peer_target'`, c.String(), c.Seq, h.Now().Format(time.RFC3339Nano), h.Now().Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}()
			if err := h.MovePeer(ctx, "peer_target", fmt.Sprintf("prn_move_%d", n+1)); err != nil {
				t.Fatal(err)
			}
			var former, copies int
			if err := h.DB.QueryRow(`SELECT count(*) FROM event_subscriptions s JOIN peers p ON p.peer_id=s.peer_id
    WHERE s.peer_id='peer_target' AND s.principal_id<>p.prn_id AND s.verified_at IS NOT NULL AND s.paused_reason IS NULL AND s.expires_at>? AND s.cursor_seq=0`, h.Now().Format(time.RFC3339Nano)).Scan(&former); err != nil || former != n {
				t.Fatalf("former live subscriptions=%d want=%d err=%v", former, n, err)
			}
			if err := h.DB.QueryRow(`SELECT count(*) FROM event_outbox WHERE seq=? AND body='{}' AND done_at IS NOT NULL AND terminal='http_410'`, c.Seq).Scan(&copies); err != nil || copies != n {
				t.Fatalf("former terminal copies=%d want=%d err=%v", copies, n, err)
			}
			t.Logf("analyze=%v former_principals=%d fixture_setup=%s", analyze, n, time.Since(setupStart))
			if analyze {
				if _, err := h.DB.Exec(`ANALYZE`); err != nil {
					t.Fatal(err)
				}
			}
			stat, pages := retentionSlicePageReads(t, h)
			t.Logf("analyze=%v former_principals=%d pages=%d scanned=%d deleted=%d", analyze, n, pages, stat.Scanned, stat.Deleted)
			if stat.Scanned != 1 || stat.Deleted != 1 {
				t.Fatalf("eligibility changed: %+v", stat)
			}
			if n == 100 {
				small = pages
			} else if pages > small+64 {
				t.Errorf("writer scans former principals: small=%d large=%d", small, pages)
			}
		}
	}
}

func TestRetentionCurrentIdentityScopesAndFormerCopies(t *testing.T) {
	for _, state := range []string{"former_lag", "principal", "peer", "oauth", "oauth_former", "pending", "parked", "exhausted", "terminal"} {
		t.Run(state, func(t *testing.T) {
			cfg := hub.DefaultConfig()
			cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Target: time.Minute}
			h, _, clock := preferencesHubClock(t, cfg)
			ctx := context.Background()
			if _, err := h.DB.Exec(`INSERT INTO principals(prn_id,name,created_at) VALUES('prn_old','synthetic','2026-01-01T00:00:00.000Z'),('prn_current','synthetic','2026-01-01T00:00:00.000Z');
    INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES('peer_target','prn_old','synthetic','dot','2026-01-01T00:00:00.000Z');`); err != nil {
				t.Fatal(err)
			}
			if err := h.MovePeer(ctx, "peer_target", "prn_current"); err != nil {
				t.Fatal(err)
			}
			c := retentionEvent(t, h, "peer_target", clock.Now().Add(-8*24*time.Hour))
			retentionAck(t, h, "peer_target", "*", c)
			kind, principal := events.KindVecnet, "prn_old"
			switch state {
			case "principal":
				principal = "prn_current"
			case "peer":
				principal = "peer_target"
			case "oauth", "oauth_former":
				kind = events.KindOAuth
				if state == "oauth" {
					principal = "prn_current"
				}
				if _, err := h.DB.Exec(`INSERT INTO oauth_principals(prn_id,subject,peer_id,created_at) VALUES(?,'synthetic','peer_target','2026-01-01T00:00:00.000Z')`, principal); err != nil {
					t.Fatal(err)
				}
			}
			st := events.NewStore(h)
			st.Key = make([]byte, 32)
			sub, _, err := st.Upsert(ctx, events.Subscription{PrincipalKind: kind, PrincipalID: principal, PeerID: "peer_target", Name: events.Name, URL: "https://synthetic.invalid/callback", Cursor: eventlog.Cursor{Epoch: c.Epoch}}, "synthetic", events.MaxTTL, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.MarkVerified(ctx, sub.ID); err != nil {
				t.Fatal(err)
			}
			hasCopy := state == "pending" || state == "parked" || state == "exhausted" || state == "terminal"
			if hasCopy {
				var done, terminal any
				parked := state == "parked"
				if state == "exhausted" || state == "terminal" {
					done = "2026-11-01T00:00:00.000Z"
					terminal = "exhausted"
				}
				if state == "terminal" {
					terminal = "http_410"
				}
				if _, err := h.DB.Exec(`INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal,parked) VALUES(?,?,?,'{"synthetic":"exact callback bytes"}','2026-11-01T00:00:00.000Z',?,?,?)`, sub.ID, c.String(), c.Seq, done, terminal, parked); err != nil {
					t.Fatal(err)
				}
			}
			stat, err := h.PruneEventSlice(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if state == "former_lag" || state == "oauth_former" || state == "terminal" {
				want = 0
			}
			if stat.Pinned != want || stat.Deleted != 1-want {
				t.Fatalf("identity/copy pin state=%s: %+v", state, stat)
			}
			if hasCopy {
				var body string
				if err := h.DB.QueryRow(`SELECT body FROM event_outbox WHERE sub_id=?`, sub.ID).Scan(&body); err != nil || body != `{"synthetic":"exact callback bytes"}` {
					t.Fatal("frozen callback changed", err)
				}
			}
		})
	}
}
