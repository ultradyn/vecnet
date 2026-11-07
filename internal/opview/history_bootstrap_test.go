package opview_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestHistoryRetentionProtectiveBootstrapRevealsPinnedHistory(t *testing.T) {
	for _, fixture := range []struct {
		origin string
		legacy bool
	}{
		{origin: "--retention-history"},
		{origin: "VECNET_RETENTION_HISTORY"},
		{origin: "--retention-history", legacy: true},
		{origin: "VECNET_RETENTION_HISTORY", legacy: true},
	} {
		name := fixture.origin
		if fixture.legacy {
			name += "/legacy_marks"
		}
		t.Run(name, func(t *testing.T) {
			origin := fixture.origin
			configure := func(c *hub.Config, seconds int64) {
				c.PreferenceBootstrap = map[string]prefs.Effective{
					"retention.history": {Value: prefs.Duration(seconds), Source: prefs.SourceFlag, Origin: origin},
				}
			}
			e := testhub.StartWith(t, func(c *hub.Config) { configure(c, 86400) })
			e.Now.Advance(time.Date(2035, 1, 1, 14, 0, 0, 0, time.UTC).Sub(e.Now.Now()))
			const sender = "peer_00000000000000000000000001"
			const peer = "peer_00000000000000000000000002"
			const secret = "synthetic-bootstrap-credential"
			at := envelope.FormatTime(e.Hub.Now())
			for _, identity := range []struct{ peer, principal string }{
				{sender, "prn_00000000000000000000000001"},
				{peer, "prn_00000000000000000000000002"},
			} {
				threadExecFixture(t, e, `INSERT INTO principals(prn_id,name,created_at) VALUES(?,'synthetic',?)`, identity.principal, at)
				threadExecFixture(t, e, `INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES(?,?,'synthetic','session',?)`, identity.peer, identity.principal, at)
			}
			threadExecFixture(t, e, `INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES('bootstrap-sender','peer',?,?,?)`, sender, auth.HashSecret(secret), at)
			ctx := context.Background()
			caller := auth.Caller{Kind: "operator", PeerKind: "operator"}
			set := func(h *hub.Hub, p prefs.Patch) {
				t.Helper()
				s, err := h.Preferences.ReadPreferences(ctx, caller)
				if err != nil {
					t.Fatal(err)
				}
				preview, err := h.Preferences.PreviewPreferences(ctx, caller, s.ETag, p)
				if err != nil {
					t.Fatal(err)
				}
				if len(preview.Risks) > 0 {
					p.Confirm = []string{preview.ConfirmToken}
				}
				if _, err := h.Preferences.PatchPreferences(ctx, caller, s.ETag, p); err != nil {
					t.Fatal(err)
				}
			}
			m := threadSend(t, e, secret, "", []string{peer}, map[string]any{"content": map[string]any{"text": strings.Repeat("synthetic ", 700)}})
			e.Now.Advance(35 * time.Hour)
			// Confirm the flag/env policy without creating a stored override.
			set(e.Hub, prefs.Patch{Reset: []string{"retention.history"}, ApplyExisting: true})
			e.Now.Advance(time.Hour) // next nightly sweep; the body is now 36h old
			sweep := func(h *hub.Hub) hub.HistoryRetentionStatus {
				t.Helper()
				s, err := h.PruneHistorySlice(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			assertVisible := func(h *hub.Hub, want int) {
				t.Helper()
				history, _, err := h.ThreadMessages(ctx, caller, m.ThreadID, "", 50)
				if err != nil || len(history) != want {
					t.Errorf("ThreadMessages count=%d want=%d error=%v", len(history), want, err)
				}
				r := opview.New(h)
				matrix, err := r.Matrix(ctx, m.ThreadID, "", 50)
				if err != nil || len(matrix.Rows) != want {
					t.Errorf("Matrix count=%d want=%d error=%v", len(matrix.Rows), want, err)
				}
				detail, err := r.Thread(ctx, caller, m.ThreadID)
				if err != nil || len(detail.Files) != want {
					t.Errorf("Files count=%d want=%d error=%v", len(detail.Files), want, err)
				}
			}
			assertVisible(e.Hub, 1)
			if s := sweep(e.Hub); s.Marked != 1 || s.Pinned != 1 || s.Deleted != 0 {
				t.Fatalf("initial mark: marked=%d pinned=%d deleted=%d", s.Marked, s.Pinned, s.Deleted)
			}
			assertVisible(e.Hub, 0)
			if fixture.legacy {
				// Model the previous release's projection and revision-based marks.
				threadExecFixture(t, e, `UPDATE messages SET history_expired_revision=(SELECT json_extract(value,'$.revision') FROM meta WHERE key='history_retention_v1') WHERE msg_id=?`, m.MessageID)
				threadExecFixture(t, e, `UPDATE meta SET value=json_remove(value,'$.mark_generation') WHERE key='history_retention_v1'`)
			}
			restart := func(c hub.Config) *hub.Hub {
				t.Helper()
				h, err := hub.New(e.Hub.DB, c, e.Hub.Now)
				if err != nil {
					t.Fatal(err)
				}
				return h
			}
			// Identical restarts and unrelated saves preserve the existing marks.
			h := restart(e.Hub.Cfg)
			assertVisible(h, 0)
			set(h, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(501)}})
			assertVisible(h, 0)
			revision := h.Preferences.Current().Revision
			c := h.Cfg
			configure(&c, 172800)
			h = restart(c)
			if h.Preferences.Current().Revision != revision {
				t.Fatal("bootstrap change unexpectedly advanced preference revision")
			}
			// A protective restart restores visibility before any maintenance.
			assertVisible(h, 1)
			if s := sweep(h); s.ConfiguredSeconds != 172800 || s.EffectiveSeconds != 172800 || s.PendingConfirmation || s.Marked != 0 || s.Deleted != 0 || s.Policy.History.Source != prefs.SourceFlag || s.Policy.History.Origin != origin {
				t.Fatalf("protective bootstrap: configured=%d effective=%d pending=%v marked=%d deleted=%d source=%s", s.ConfiguredSeconds, s.EffectiveSeconds, s.PendingConfirmation, s.Marked, s.Deleted, s.Policy.History.Source)
			}
			assertVisible(h, 1)
			h = restart(c)
			assertVisible(h, 1)
			// Lengthening cancels the old retrospective approval; aging alone
			// cannot revive it for rows originally protected by forever.
			e.Now.Advance(24 * time.Hour)
			if s := sweep(h); s.Deleted != 0 || s.Marked != 0 {
				t.Fatalf("protective bootstrap revived old approval: marked=%d deleted=%d", s.Marked, s.Deleted)
			}
			assertVisible(h, 1)
			// A fresh confirmation can mark under the new policy, after the
			// next nightly sweep; PATCH itself never hides or deletes the body.
			set(h, prefs.Patch{Reset: []string{"retention.history"}, ApplyExisting: true})
			assertVisible(h, 1)
			e.Now.Advance(24 * time.Hour)
			sweep(h)
			assertVisible(h, 0)
			h = restart(c)
			assertVisible(h, 0)
			set(h, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(502)}})
			sweep(h)
			assertVisible(h, 0)
			// PATCH transitions must use the same marker domain as restarts.
			set(h, prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(345600)}})
			assertVisible(h, 1)
			h = restart(c)
			assertVisible(h, 1)
			var retained int
			if err := h.DB.QueryRow(`SELECT count(*) FROM messages WHERE msg_id=? AND history_expired_at IS NOT NULL`, m.MessageID).Scan(&retained); err != nil || retained != 1 {
				t.Fatalf("protective policy lost pinned body: count=%d error=%v", retained, err)
			}
		})
	}
}
