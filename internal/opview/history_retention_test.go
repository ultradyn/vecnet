package opview_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/opview"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

func TestHistoryRetentionMatrixFilesAndLocator(t *testing.T) {
	e := testhub.Start(t)
	_, as := e.NewPeer("session", "a")
	b, _ := e.NewPeer("session", "b")
	caller := auth.Caller{Kind: "operator", PeerKind: "operator"}
	ctx := context.Background()
	set := func(p prefs.Patch) {
		t.Helper()
		s, err := e.Hub.Preferences.ReadPreferences(ctx, caller)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := e.Hub.Preferences.PreviewPreferences(ctx, caller, s.ETag, p)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Risks) > 0 {
			p.Confirm = []string{preview.ConfirmToken}
		}
		if _, err := e.Hub.Preferences.PatchPreferences(ctx, caller, s.ETag, p); err != nil {
			t.Fatal(err)
		}
	}
	set(prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(86400)}})
	m := threadSend(t, e, as, "", []string{b}, map[string]any{"content": map[string]any{"text": strings.Repeat("synthetic ", 700)}})
	reader := opview.New(e.Hub)
	initial, err := reader.Thread(ctx, caller, m.ThreadID)
	if err != nil || len(initial.Files) != 1 {
		t.Fatal("missing attachment fixture")
	}
	e.Now.Advance(2 * 24 * time.Hour)
	if _, err := e.Hub.PruneHistorySlice(ctx); err != nil {
		t.Fatal(err)
	}
	matrix, err := reader.Matrix(ctx, m.ThreadID, "", 50)
	if err != nil || len(matrix.Rows) != 0 {
		t.Fatal("marked matrix row visible")
	}
	detail, err := reader.Thread(ctx, caller, m.ThreadID)
	if err != nil || len(detail.Files) != 0 {
		t.Fatal("marked attachment history visible")
	}
	// An unrelated write advances the global preferences revision without
	// changing the history activation. Pinned bodies must stay hidden before
	// any maintenance slice gets a chance to revisit their expiry marks.
	before, err := e.Hub.Preferences.ReadPreferences(ctx, caller)
	if err != nil {
		t.Fatal(err)
	}
	set(prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(501)}})
	after, err := e.Hub.Preferences.ReadPreferences(ctx, caller)
	if err != nil {
		t.Fatal(err)
	}
	if before.ETag == after.ETag {
		t.Fatal("unrelated preference write did not advance revision")
	}
	var pinnedExpired int
	if err := e.Hub.DB.QueryRow(`SELECT count(*) FROM messages WHERE msg_id=? AND history_expired_at IS NOT NULL`, m.MessageID).Scan(&pinnedExpired); err != nil || pinnedExpired != 1 {
		t.Fatalf("missing pinned expired body: count=%d error=%v", pinnedExpired, err)
	}
	assertHidden := func() {
		t.Helper()
		history, _, err := e.Hub.ThreadMessages(ctx, caller, m.ThreadID, "", 50)
		if err != nil || len(history) != 0 {
			t.Errorf("unrelated preference revealed ThreadMessages: count=%d error=%v", len(history), err)
		}
		matrix, err := reader.Matrix(ctx, m.ThreadID, "", 50)
		if err != nil || len(matrix.Rows) != 0 {
			t.Errorf("unrelated preference revealed Matrix: count=%d error=%v", len(matrix.Rows), err)
		}
		detail, err := reader.Thread(ctx, caller, m.ThreadID)
		if err != nil || len(detail.Files) != 0 {
			t.Errorf("unrelated preference revealed Files: count=%d error=%v", len(detail.Files), err)
		}
	}
	assertHidden()
	// Re-marking under the new global revision must use the same activation
	// as the readers, rather than replacing a valid mark with a global one.
	if _, err := e.Hub.PruneHistorySlice(ctx); err != nil {
		t.Fatal(err)
	}
	assertHidden()
	set(prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	matrix, err = reader.Matrix(ctx, m.ThreadID, "", 50)
	if err != nil || len(matrix.Rows) != 1 {
		t.Fatal("archive failed to reveal matrix mark")
	}
	detail, err = reader.Thread(ctx, caller, m.ThreadID)
	if err != nil || len(detail.Files) != 1 {
		t.Fatal("archive failed to reveal frozen attachment metadata")
	}
	set(prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	e.Now.Advance(24 * time.Hour)
	threadExecFixture(t, e, `UPDATE deliveries SET state='accepted',held=0 WHERE msg_id=?`, m.MessageID)
	threadExecFixture(t, e, `DELETE FROM events WHERE seq IN (SELECT event_seq FROM deliveries WHERE msg_id=?)`, m.MessageID)
	for range 2 {
		if _, err := e.Hub.PruneHistorySlice(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// History GC preserves the expired loop; only the operator revives it.
	retained := threadSend(t, e, e.OperatorSecret, m.ThreadID, []string{b}, nil)
	matrix, err = reader.Matrix(ctx, m.ThreadID, m.MessageID, 50)
	if err != nil || len(matrix.Rows) != 1 || matrix.Rows[0].MessageID != retained.MessageID || matrix.NextAfter != nil {
		t.Fatal("matrix locator pagination changed response")
	}
}
