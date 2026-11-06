package hub_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/testhub"
)

// Task 7 seams: Send/React allocate lifetime sequence numbers; ThreadMessages
// reads history and locator cursors; the shared preferences service authorizes
// policy; PruneHistorySlice is bounded maintenance. SQL only arranges synthetic
// dependencies or checks durable state that has no public representation.
func historyEnv(t *testing.T, configure func(*hub.Config)) *testhub.Env {
	t.Helper()
	// Behavioral fixtures assert completion of small mark/sweep phases. The
	// production 50 ms target may yield partway through them under -race;
	// explicit bounded-slice tests below install their own tighter limits.
	e := testhub.StartWith(t, func(c *hub.Config) {
		c.EventRetentionLimits.Target = time.Minute
		if configure != nil {
			configure(c)
		}
	})
	e.Now.Advance(prefsEpoch.Sub(e.Now.Now()))
	return e
}
func historyPeer(t *testing.T, e *testhub.Env, name string) (string, string) {
	t.Helper()
	n := int(name[0] - 'a' + 1)
	peer := fmt.Sprintf("peer_%026d", n)
	prn := fmt.Sprintf("prn_%026d", n)
	secret := "synthetic-history-credential-" + name
	historyExec(t, e, `INSERT INTO principals(prn_id,name,created_at) VALUES(?,?,?)`, prn, name, envelope.FormatTime(e.Hub.Now()))
	historyExec(t, e, `INSERT INTO peers(peer_id,prn_id,name,kind,enrolled_at) VALUES(?,?,?,'session',?)`, peer, prn, name, envelope.FormatTime(e.Hub.Now()))
	historyExec(t, e, `INSERT INTO credentials(cred_id,kind,subject_id,secret_hash,created_at) VALUES(?,'peer',?,?,?)`, "history-"+name, peer, auth.HashSecret(secret), envelope.FormatTime(e.Hub.Now()))
	return peer, secret
}

func historyExec(t *testing.T, e *testhub.Env, q string, args ...any) {
	t.Helper()
	if _, err := e.Hub.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func historySend(t *testing.T, e *testhub.Env, secret, key, thread, recipient string) hub.SendResponse {
	t.Helper()
	b := req(key, recipient)
	if thread != "" {
		b["thread_id"] = thread
	}
	return mustSend(t, e, secret, b)
}
func historySeq(t *testing.T, e *testhub.Env, id string) int {
	t.Helper()
	var n int
	if err := e.Hub.DB.QueryRow(`SELECT seq FROM messages WHERE msg_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func historySweep(t *testing.T, e *testhub.Env) hub.HistoryRetentionStatus {
	t.Helper()
	s, err := e.Hub.PruneHistorySlice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func historyMessages(t *testing.T, e *testhub.Env, thread, after string) []envelope.Message {
	t.Helper()
	m, _, err := e.Hub.ThreadMessages(context.Background(), prefsOperator, thread, after, 200)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func historyPolicy(t *testing.T, e *testhub.Env, seconds int64, existing bool) {
	t.Helper()
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(seconds)}, ApplyExisting: existing})
}
func historySettle(t *testing.T, e *testhub.Env, id string) {
	t.Helper()
	historyExec(t, e, `UPDATE deliveries SET state='accepted',held=0,dispatching=0 WHERE msg_id=?`, id)
	// Synthetic absence of replay dependencies; unrelated rows are preserved.
	historyExec(t, e, `DELETE FROM events WHERE seq IN (SELECT event_seq FROM deliveries WHERE msg_id=?)`, id)
}
func historyNight(t *testing.T, e *testhub.Env) { t.Helper(); e.Now.Advance(24 * time.Hour) }

func TestHistoryLifetimeSequenceAfterNewestAndAllBodiesDeleted(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			e := historyEnv(t, nil)
			_, as := historyPeer(t, e, "a")
			b, bs := historyPeer(t, e, "b")
			c, _ := historyPeer(t, e, "c")
			first := historySend(t, e, as, "first", "", b)
			second := historySend(t, e, as, "second", first.ThreadID, b)
			before := historySeq(t, e, second.MessageID)
			var oldCount, oldBudget int
			var oldAt string
			if err := e.Hub.DB.QueryRow(`SELECT message_count,budget_remaining,last_message_at FROM threads WHERE thr_id=?`, first.ThreadID).Scan(&oldCount, &oldBudget, &oldAt); err != nil {
				t.Fatal(err)
			}
			historyExec(t, e, `DELETE FROM events`)
			historyExec(t, e, `DELETE FROM deliveries`)
			if all {
				historyExec(t, e, `DELETE FROM messages WHERE thr_id=?`, first.ThreadID)
			} else {
				historyExec(t, e, `DELETE FROM messages WHERE msg_id=?`, second.MessageID)
			}
			next := historySend(t, e, as, "next", first.ThreadID, c)
			if n := historySeq(t, e, next.MessageID); n <= before {
				t.Fatalf("ordinary seq=%d must exceed lifetime high=%d", n, before)
			}
			var boundary int
			if err := e.Hub.DB.QueryRow(`SELECT joined_after_seq FROM thread_participants WHERE thr_id=? AND peer_id=?`, first.ThreadID, c).Scan(&boundary); err != nil {
				t.Fatal(err)
			}
			if boundary != before {
				t.Fatalf("join boundary=%d want lifetime high=%d", boundary, before)
			}
			// A reaction to the new message must follow the removed suffix too.
			w := needWake(t, rx(t, e, bs, next.MessageID, "✅", "add", "wake"))
			_ = bs
			if n := historySeq(t, e, w.MessageID); n <= historySeq(t, e, next.MessageID) {
				t.Fatalf("reaction seq=%d reused suffix", n)
			}
			var gotCount, gotBudget int
			var gotAt string
			if err := e.Hub.DB.QueryRow(`SELECT message_count,budget_remaining,last_message_at FROM threads WHERE thr_id=?`, first.ThreadID).Scan(&gotCount, &gotBudget, &gotAt); err != nil {
				t.Fatal(err)
			}
			if gotCount != oldCount+1 || gotBudget != oldBudget-1 || gotAt < oldAt {
				t.Fatalf("lifetime counters count=%d budget=%d activity_regressed=%v", gotCount, gotBudget, gotAt < oldAt)
			}
		})
	}
}

func TestHistoryRetentionDefaultForeverAndConfirmedNightlyLocator(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	first := historySend(t, e, as, "first", "", b)
	historySettle(t, e, first.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	historySweep(t, e)
	if len(historyMessages(t, e, first.ThreadID, "")) != 1 {
		t.Fatal("default removed history")
	}
	historyPolicy(t, e, 86400, true)
	historySweep(t, e)
	if len(historyMessages(t, e, first.ThreadID, "")) != 1 {
		t.Fatal("confirmation deleted before nightly not_before")
	}
	restored := e.CloneDB(t)
	historySweep(t, restored)
	if len(historyMessages(t, restored, first.ThreadID, "")) != 1 {
		t.Fatal("restart bypassed not_before")
	}
	historyNight(t, restored)
	historySweep(t, restored)
	s := historySweep(t, restored)
	if n := count(t, restored, `SELECT count(*) FROM messages WHERE msg_id=?`, first.MessageID); n != 0 {
		t.Fatal("eligible body retained")
	}
	if s.Tombstones != 1 {
		t.Fatalf("locator growth=%d want 1", s.Tombstones)
	}
	next := historySend(t, restored, restored.OperatorSecret, "retained", first.ThreadID, b)
	page := historyMessages(t, restored, first.ThreadID, first.MessageID)
	if len(page) != 1 || page[0].MessageID != next.MessageID {
		t.Fatal("purged locator failed to return retained suffix")
	}
	body := req("reply", b)
	body["in_reply_to"] = first.MessageID
	r, raw := restored.Do(as, "POST", "/v1/messages", body)
	code, _ := apiError(t, raw)
	if r.StatusCode != 422 || code != "not_in_thread" {
		t.Fatalf("erased reply status=%d code=%s", r.StatusCode, code)
	}
}

func TestHistoryRetentionProspectiveArchiveAndOldMarks(t *testing.T) {
	e := historyEnv(t, nil)
	a, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	old := historySend(t, e, as, "old", "", b)
	historySettle(t, e, old.MessageID)
	historyPolicy(t, e, 86400, false)
	newer := historySend(t, e, as, "newer", old.ThreadID, b)
	e.Now.Advance(2 * 24 * time.Hour)
	s := historySweep(t, e)
	if s.Marked != 1 || s.Pins["direct_delivery"] == 0 {
		t.Fatalf("mark/pin counts=%d/%v", s.Marked, s.Pins)
	}
	if n := len(historyMessages(t, e, old.ThreadID, "")); n != 1 {
		t.Fatalf("visible=%d want prior-policy message only", n)
	}
	floor, err := e.Hub.Log.OldestFor(context.Background(), []string{b})
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := e.Hub.Log.Read(context.Background(), []string{b}, floor, 200)
	if err != nil || len(eventsBefore) == 0 {
		t.Fatalf("missing retry replay fixture: count=%d error=%v", len(eventsBefore), err)
	}
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	historySweep(t, e)
	if n := len(historyMessages(t, e, old.ThreadID, "")); n != 2 {
		t.Fatalf("archive did not restore old mark: visible=%d", n)
	}
	if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, newer.MessageID) != 1 {
		t.Fatal("archive swept previously marked body")
	}
	state := readPrefs(t, e.Hub)
	_, err = e.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, state.ETag, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}})
	prefsCode(t, err, prefs.CodeConfirmationRequired)
	status := e.Hub.HistoryRetentionStatus()
	if !status.ArchiveMode || status.ConfiguredSeconds != 86400 || status.EffectiveSeconds != 0 || status.Policy.Revision != state.Revision {
		t.Fatalf("masked status=%+v", status)
	}
	// Neither archive nor history marking changes any durable retry bytes.
	eventsAfter, err := e.Hub.Log.Read(context.Background(), []string{b}, floor, 200)
	if err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatal("retry replay page changed")
	}
	for i := range eventsBefore {
		if !bytes.Equal(eventsBefore[i].Data, eventsAfter[i].Data) {
			t.Fatal("retention changed frozen retry bytes")
		}
	}
	_ = a
}

func TestHistoryRetentionDependenciesAndArchiveStopsNextSlice(t *testing.T) {
	for _, kind := range []string{"reference_event", "inline_event", "queued", "held", "dispatching", "outbox", "parked", "exhausted", "job", "approval", "reaction_wake", "legacy_snapshot", "durable_blob"} {
		t.Run(kind, func(t *testing.T) {
			e := historyEnv(t, func(c *hub.Config) { c.DeliveryReferences = kind != "inline_event" })
			_, as := historyPeer(t, e, "a")
			b, _ := historyPeer(t, e, "b")
			m := historySend(t, e, as, "pin", "", b)
			var seq int64
			if err := e.Hub.DB.QueryRow(`SELECT event_seq FROM deliveries WHERE msg_id=?`, m.MessageID).Scan(&seq); err != nil {
				t.Fatal(err)
			}
			if kind != "reference_event" && kind != "inline_event" {
				historySettle(t, e, m.MessageID)
			} else {
				historyExec(t, e, `UPDATE deliveries SET state='accepted' WHERE msg_id=?`, m.MessageID)
			}
			switch kind {
			case "queued":
				historyExec(t, e, `UPDATE deliveries SET state='queued' WHERE msg_id=?`, m.MessageID)
			case "held":
				historyExec(t, e, `UPDATE deliveries SET state='queued',held=1 WHERE msg_id=?`, m.MessageID)
			case "dispatching":
				historyExec(t, e, `UPDATE deliveries SET dispatching=1 WHERE msg_id=?`, m.MessageID)
			case "outbox", "parked", "exhausted":
				retentionSubscription(t, e.Hub, b, eventlog.Cursor{Epoch: e.Hub.Log.Epoch(), Seq: seq}, "live")
				var done, terminal any
				parked := 0
				if kind == "parked" {
					parked = 1
				}
				if kind == "exhausted" {
					done = envelope.FormatTime(e.Hub.Now())
					terminal = "exhausted"
				}
				historyExec(t, e, `INSERT INTO event_outbox(sub_id,event_id,seq,body,created_at,done_at,terminal,parked) SELECT 'sub_synthetic',dly_id,event_seq,'{}',created_at,?,?,? FROM deliveries WHERE msg_id=?`, done, terminal, parked, m.MessageID)
			case "job":
				historyExec(t, e, `INSERT INTO jobs(job_id,thr_id,job_key,request_sha256,requester,recipient,state,created_at,updated_at) VALUES('job_synthetic',?,'key','hash','a','b','running',?,?)`, m.ThreadID, envelope.FormatTime(e.Hub.Now()), envelope.FormatTime(e.Hub.Now()))
			case "approval":
				historyExec(t, e, `INSERT INTO approvals(apr_id,thr_id,msg_id,requester,options,expires_at) VALUES('apr_synthetic',?,?,'a','[]',?)`, m.ThreadID, m.MessageID, envelope.FormatTime(e.Hub.Now().Add(90*24*time.Hour)))
			case "reaction_wake":
				historyExec(t, e, `INSERT INTO reaction_wakes VALUES(?,'peer_synthetic','✅',?)`, m.MessageID, envelope.FormatTime(e.Hub.Now()))
			case "legacy_snapshot":
				historyExec(t, e, `INSERT INTO event_message_snapshots SELECT 'snapshot_synthetic',msg_id,'synthetic',body FROM messages WHERE msg_id=?`, m.MessageID)
			case "durable_blob":
				historyExec(t, e, `INSERT INTO durable_blob_payloads(sha256,purpose,bytes,media_type,created_at) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','spill',X'01','text/plain',?)`, envelope.FormatTime(e.Hub.Now()))
				historyExec(t, e, `INSERT INTO durable_blob_refs(msg_id,sha256,purpose) VALUES(?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','spill')`, m.MessageID)
			}
			e.Now.Advance(10 * 24 * time.Hour)
			historyPolicy(t, e, 86400, true)
			historyNight(t, e)
			historySweep(t, e)
			s := historySweep(t, e)
			if s.Pinned == 0 || len(s.Pins) == 0 {
				t.Fatalf("missing dependency reason for %s", kind)
			}
			if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, m.MessageID) != 1 {
				t.Fatalf("deleted pinned %s", kind)
			}
			retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
			s = historySweep(t, e)
			if s.Marked != 0 || s.Deleted != 0 {
				t.Fatal("archive failed to stop next slice")
			}
			if len(historyMessages(t, e, m.ThreadID, "")) != 1 {
				t.Fatal("archive hid pinned history")
			}
		})
	}
}

func TestHistoryRetentionReadFailureAndBoundedSlices(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	var ids []string
	for i := 0; i < 4; i++ {
		m := historySend(t, e, as, fmt.Sprint(i), "", b)
		ids = append(ids, m.MessageID)
		historySettle(t, e, m.MessageID)
	}
	e.Now.Advance(10 * 24 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	e.Hub.Cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 1, Bytes: 1 << 20, Target: time.Second}
	for i := 0; i < 2; i++ {
		s := historySweep(t, e)
		if s.Scanned > 1 || s.Deleted > 1 {
			t.Fatal("slice exceeded row budget")
		}
	}
	historyExec(t, e, `UPDATE hub_preferences SET value_json='"invalid"' WHERE key='retention.history'`)
	before := count(t, e, `SELECT count(*) FROM messages`)
	s, err := e.Hub.PruneHistorySlice(context.Background())
	if err == nil || s.Deleted != 0 || s.ErrorReason == "" {
		t.Fatal("policy failure did not fail closed")
	}
	if count(t, e, `SELECT count(*) FROM messages`) != before {
		t.Fatal("policy failure deleted history")
	}
	_ = ids
}

func TestHistoryFromJoinAfterPurgedSuffix(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	c, _ := historyPeer(t, e, "c")
	old := historySend(t, e, as, "old", "", b)
	historySettle(t, e, old.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	historySweep(t, e)
	// The original loop expired during retention; the operator revives it.
	m := historySend(t, e, e.OperatorSecret, "join", old.ThreadID, c)
	page, _, err := e.Hub.ThreadMessages(context.Background(), auth.Caller{Kind: "peer", PeerID: c}, old.ThreadID, old.MessageID, 50)
	if err != nil || len(page) != 1 || page[0].MessageID != m.MessageID {
		t.Fatalf("from_join suffix count=%d err=%v", len(page), err)
	}
}

func TestHistoryRetentionFixedPreviewBoundsAndStaleConfirmation(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	old := historySend(t, e, as, "old", "", b)
	historySettle(t, e, old.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	patch := prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(86400)}, ApplyExisting: true}
	state := readPrefs(t, e.Hub)
	preview := previewPrefs(t, e.Hub.Preferences, prefsOperator, state, patch)
	later := historySend(t, e, as, "late", "", b)
	historySettle(t, e, later.MessageID)
	// Even a later imported/backdated row must not drift into a fixed preview.
	historyExec(t, e, `UPDATE messages SET created_at=? WHERE msg_id=?`, envelope.FormatTime(e.Hub.Now().Add(-10*24*time.Hour)), later.MessageID)
	patch.Confirm = []string{preview.ConfirmToken}
	patchPrefs(t, e.Hub, state, patch)
	historyNight(t, e)
	historySweep(t, e)
	historySweep(t, e)
	if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, old.MessageID) != 0 || count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, later.MessageID) != 1 {
		t.Fatal("preview high-water drifted")
	}
	state = readPrefs(t, e.Hub)
	preview = previewPrefs(t, e.Hub.Preferences, prefsOperator, state, patch)
	patchPrefs(t, e.Hub, state, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}})
	patch.Confirm = []string{preview.ConfirmToken}
	_, err := e.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, readPrefs(t, e.Hub).ETag, patch)
	prefsCode(t, err, prefs.CodeConfirmationStale)
}

func TestHistoryRetentionArchiveExitCannotAuthorizeLaterFiniteExpiry(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	m := historySend(t, e, as, "old", "", b)
	historySettle(t, e, m.MessageID)
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(false)}, ApplyExisting: true})
	state := readPrefs(t, e.Hub)
	finite := prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(86400)}}
	_, err := e.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, state.ETag, finite)
	prefsCode(t, err, prefs.CodeConfirmationRequired)
	retentionConfirm(t, e.Hub, finite)
	e.Now.Advance(10 * 24 * time.Hour)
	historySweep(t, e)
	historySweep(t, e)
	if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, m.MessageID) != 1 {
		t.Fatal("expiry-disabled exit pre-authorized later history deletion")
	}
	// Resetting a true archive flag also goes through the same confirmation.
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	_, err = e.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, readPrefs(t, e.Hub).ETag, prefs.Patch{Reset: []string{"retention.archive_mode"}})
	prefsCode(t, err, prefs.CodeConfirmationRequired)
}

func TestHistoryRetentionBootstrapChangesRemainPending(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	m := historySend(t, e, as, "old", "", b)
	historySettle(t, e, m.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	cfg := e.Hub.Cfg
	cfg.MessageHistoryRetention = 24 * time.Hour
	restarted, err := hub.New(e.Hub.DB, cfg, e.Hub.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err := restarted.PruneHistorySlice(context.Background())
	if err != nil || !s.PendingConfirmation || s.Deleted != 0 {
		t.Fatalf("bootstrap finite bypass: pending=%v err=%v", s.PendingConfirmation, err)
	}
	// A plain Save of the already configured value cannot bless bootstrap.
	state := readPrefs(t, restarted)
	patchPrefs(t, restarted, state, prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(86400)}})
	s, err = restarted.PruneHistorySlice(context.Background())
	if err != nil || !s.PendingConfirmation {
		t.Fatal("plain Save cleared pending bootstrap")
	}
	if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, m.MessageID) != 1 {
		t.Fatal("bootstrap deleted existing body")
	}
	// Stored forever wins over finite flags on every restart.
	retentionConfirm(t, restarted, prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(0)}})
	restored, err := hub.New(e.Hub.DB, cfg, e.Hub.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err = restored.PruneHistorySlice(context.Background())
	if err != nil || s.ConfiguredSeconds != 0 || s.Policy.History.Source != prefs.SourceStored {
		t.Fatal("stored forever lost to bootstrap")
	}
	// A changed archive bootstrap must preserve history until an actual exit.
	archiveCfg := cfg
	archiveCfg.ArchiveMode = true
	archived, err := hub.New(e.Hub.DB, archiveCfg, e.Hub.Now)
	if err != nil {
		t.Fatal(err)
	}
	exitCfg := archiveCfg
	exitCfg.ArchiveMode = false
	exited, err := hub.New(e.Hub.DB, exitCfg, e.Hub.Now)
	if err != nil {
		t.Fatal(err)
	}
	_ = archived
	s, err = exited.PruneHistorySlice(context.Background())
	if err != nil || !s.PendingConfirmation {
		t.Fatal("archive flag/restart bypassed confirmed exit")
	}
}

func TestHistoryRetentionFrozenActiveRetryAcrossRestore(t *testing.T) {
	for _, refs := range []bool{false, true} {
		t.Run(fmt.Sprint(refs), func(t *testing.T) {
			e := historyEnv(t, func(c *hub.Config) { c.DeliveryReferences = refs })
			_, as := historyPeer(t, e, "a")
			b, _ := historyPeer(t, e, "b")
			cb := testhub.NewCallback(t)
			route, _, err := e.Hub.AddRoute(context.Background(), "peer", b, cb.URL)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Worker.Verify(context.Background(), route); err != nil {
				t.Fatal(err)
			}
			m := historySend(t, e, as, "retry", "", b)
			first, err := e.Hub.ClaimDue(context.Background(), e.Hub.Now(), 1)
			if err != nil || len(first) != 1 {
				t.Fatal("missing initial retry")
			}
			e.Now.Advance(10 * 24 * time.Hour)
			historyPolicy(t, e, 86400, true)
			historyNight(t, e)
			historySweep(t, e)
			historySweep(t, e)
			restored := e.CloneDB(t)
			// Expired delivery leases are held open synthetically to isolate history GC.
			historyExec(t, restored, `UPDATE deliveries SET expires_at=?,next_attempt_at=NULL WHERE msg_id=?`, envelope.FormatTime(restored.Hub.Now().Add(time.Hour)), m.MessageID)
			retry, err := restored.Hub.ClaimDue(context.Background(), restored.Hub.Now(), 1)
			if err != nil || len(retry) != 1 || !bytes.Equal(first[0].Body, retry[0].Body) || first[0].DeliveryID != retry[0].DeliveryID {
				t.Fatal("restore/history expiry changed active retry bytes or identity")
			}
		})
	}
}

func TestHistoryRetentionSharedBlobReferencesGoLast(t *testing.T) {
	e := historyEnv(t, nil)
	a, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	blob, err := e.Hub.Blobs.Put(context.Background(), a, bytes.NewBufferString("synthetic shared attachment"), "text/plain", 0)
	if err != nil {
		t.Fatal(err)
	}
	send := func(key string) hub.SendResponse {
		body := req(key, b)
		body["attachments"] = []map[string]any{{"sha256": blob.SHA256, "media_type": blob.MediaType, "size": blob.Size}}
		return mustSend(t, e, as, body)
	}
	first := send("first")
	second := send("second")
	historySettle(t, e, first.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	historySweep(t, e)
	if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, first.MessageID) != 0 {
		t.Fatal("settled shared attachment message retained")
	}
	if count(t, e, `SELECT count(*) FROM blob_refs WHERE sha256=?`, blob.SHA256) != 1 {
		t.Fatal("removed retained message's shared blob contract")
	}
	if _, err := e.Hub.Blobs.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(t, e, `SELECT count(*) FROM blobs WHERE sha256=?`, blob.SHA256) != 1 {
		t.Fatal("shared attachment bytes lost")
	}
	historySettle(t, e, second.MessageID)
	historySweep(t, e)
	if count(t, e, `SELECT count(*) FROM blob_refs WHERE sha256=?`, blob.SHA256) != 0 {
		t.Fatal("last erased message kept its disk blob ref")
	}
	if _, err := e.Hub.Blobs.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(t, e, `SELECT count(*) FROM blobs WHERE sha256=?`, blob.SHA256) != 0 {
		t.Fatal("existing blob GC did not collect final unreferenced bytes")
	}
}

func TestHistoryRetentionProtectionBetweenDestructiveSlices(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	for i := 0; i < 3; i++ {
		m := historySend(t, e, as, fmt.Sprint(i), "", b)
		historySettle(t, e, m.MessageID)
	}
	e.Now.Advance(10 * 24 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	e.Hub.Cfg.EventRetentionLimits = eventlog.RetentionLimits{Rows: 2, Bytes: 1 << 20, Target: time.Second}
	s := historySweep(t, e)
	if s.Deleted != 1 {
		t.Fatalf("first destructive slice deleted=%d want 1", s.Deleted)
	}
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	s = historySweep(t, e)
	if s.Deleted != 0 || s.Marked != 0 || count(t, e, `SELECT count(*) FROM messages`) != 2 {
		t.Fatal("stale cleanup continued after protective commit")
	}
}

func TestHistoryRetentionRollbackDoesNotReportPhantomLocators(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	m := historySend(t, e, as, "failure", "", b)
	historySettle(t, e, m.MessageID)
	second := historySend(t, e, as, "failure-second", m.ThreadID, b)
	historySettle(t, e, second.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	historyExec(t, e, `CREATE TRIGGER history_synthetic_failure BEFORE DELETE ON messages WHEN OLD.seq=2 BEGIN SELECT RAISE(ABORT,'history_synthetic_failure'); END`)
	s, err := e.Hub.PruneHistorySlice(context.Background())
	if err == nil || s.Deleted != 0 || s.Bytes != 0 || s.Tombstones != 0 {
		t.Fatalf("rollback status: deleted=%d tombstones=%d error=%v", s.Deleted, s.Tombstones, err)
	}
	if count(t, e, `SELECT count(*) FROM message_history_locators`) != 0 || count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, m.MessageID) != 1 || count(t, e, `SELECT count(*) FROM deliveries WHERE msg_id=?`, m.MessageID) != 1 {
		t.Fatal("failed body deletion partially committed")
	}
}

func TestHistoryRetentionWakeupsNeverTrim(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	m := historySend(t, e, as, "wake", "", b)
	historySettle(t, e, m.MessageID)
	e.Now.Advance(10 * 24 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.Hub.RunTickers(ctx, time.Hour) }()
	defer func() { cancel(); <-done }()
	historyPolicy(t, e, 86400, true)
	revision := readPrefs(t, e.Hub).Revision
	runtimeEventually(t, func() bool { return e.Hub.HistoryRetentionStatus().Policy.Revision == revision })
	if count(t, e, `SELECT count(*) FROM message_history_locators`) != 0 || len(historyMessages(t, e, m.ThreadID, "")) != 1 {
		t.Fatal("confirmation wake trimmed before nightly")
	}
	historyNight(t, e)
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}})
	revision = readPrefs(t, e.Hub).Revision
	runtimeEventually(t, func() bool { return e.Hub.HistoryRetentionStatus().Policy.Revision == revision })
	if count(t, e, `SELECT count(*) FROM message_history_locators`) != 0 || len(historyMessages(t, e, m.ThreadID, "")) != 1 {
		t.Fatal("post-not_before wake itself trimmed")
	}
}

func TestHistoryRetentionHubAuthoredApprovalBodyRemainsReadable(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	m := mustSend(t, e, as, approvalReq("synthetic-approval", "apr_00000000000000000000000001", e.Hub.Now().Add(time.Hour), []string{"yes", "no"}, b))
	e.Now.Advance(2 * time.Hour)
	if _, err := e.Hub.ExpireApprovals(context.Background(), e.Hub.Now()); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := e.Hub.DB.QueryRow(`SELECT msg_id FROM messages WHERE thr_id=? AND sender=?`, m.ThreadID, hub.HubSender).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.Now.Advance(10 * 24 * time.Hour)
	historyPolicy(t, e, 86400, true)
	historyNight(t, e)
	historySweep(t, e)
	historySweep(t, e)
	if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, id) != 1 {
		t.Fatal("hub-authored approval history lost")
	}
	retentionConfirm(t, e.Hub, prefs.Patch{Set: map[string]prefs.Value{"retention.archive_mode": prefs.Bool(true)}})
	found := false
	for _, message := range historyMessages(t, e, m.ThreadID, "") {
		if message.MessageID == id && message.Sender == hub.HubSender {
			found = true
		}
	}
	if !found {
		t.Fatal("archive cannot read hub-authored retained body")
	}
}

func TestHistoryRetentionNightBoundaryInvalidatesConfirmation(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	m := historySend(t, e, as, "night", "", b)
	historySettle(t, e, m.MessageID)
	e.Now.Advance(10*24*time.Hour + 56*time.Minute + 57*time.Second)
	patch := prefs.Patch{Set: map[string]prefs.Value{"retention.history": prefs.Duration(86400)}, ApplyExisting: true}
	state := readPrefs(t, e.Hub)
	preview := previewPrefs(t, e.Hub.Preferences, prefsOperator, state, patch)
	// Confirm at 02:01, still within the five-minute token lifetime. The signed
	// 02:00 sweep is now past, so confirmation must demand a fresh preview.
	e.Now.Advance(2 * time.Minute)
	patch.Confirm = []string{preview.ConfirmToken}
	_, err := e.Hub.Preferences.PatchPreferences(context.Background(), prefsOperator, state.ETag, patch)
	prefsCode(t, err, prefs.CodeConfirmationStale)
	if readPrefs(t, e.Hub).Revision != state.Revision || count(t, e, `SELECT count(*) FROM messages WHERE msg_id=?`, m.MessageID) != 1 {
		t.Fatal("stale cutoff confirmation changed policy or history")
	}
}

func TestHistoryReactionHighWaterSurvivesErasedWake(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, bs := historyPeer(t, e, "b")
	m := historySend(t, e, as, "target", "", b)
	first := needWake(t, rx(t, e, bs, m.MessageID, "✅", "add", "wake"))
	firstSeq := historySeq(t, e, first.MessageID)
	historySettle(t, e, first.MessageID)
	historyExec(t, e, `DELETE FROM deliveries WHERE msg_id=?`, first.MessageID)
	historyExec(t, e, `DELETE FROM messages WHERE msg_id=?`, first.MessageID)
	second := needWake(t, rx(t, e, bs, m.MessageID, "❗", "add", "wake"))
	if got := historySeq(t, e, second.MessageID); got <= firstSeq {
		t.Fatalf("reaction seq=%d reused erased wake seq=%d", got, firstSeq)
	}
	secondSeq := historySeq(t, e, second.MessageID)
	ordinary := historySend(t, e, as, "ordinary", m.ThreadID, b)
	if got := historySeq(t, e, ordinary.MessageID); got <= secondSeq {
		t.Fatalf("ordinary seq=%d did not follow reaction high=%d", got, secondSeq)
	}
	var lifetime int
	if err := e.Hub.DB.QueryRow(`SELECT message_count FROM threads WHERE thr_id=?`, m.ThreadID).Scan(&lifetime); err != nil || lifetime != 2 {
		t.Fatal("reaction suffix changed lifetime non-reaction count")
	}
}

func TestHistoryProspectiveShorteningKeepsPriorCohortAge(t *testing.T) {
	e := historyEnv(t, nil)
	_, as := historyPeer(t, e, "a")
	b, _ := historyPeer(t, e, "b")
	historyPolicy(t, e, 2*86400, false)
	older := historySend(t, e, as, "prior", "", b)
	historyPolicy(t, e, 86400, false)
	newer := historySend(t, e, as, "after-shortening", older.ThreadID, b)
	e.Now.Advance(36 * time.Hour)
	historySweep(t, e)
	page := historyMessages(t, e, older.ThreadID, "")
	if len(page) != 1 || page[0].MessageID != older.MessageID {
		t.Fatal("shortened policy retroactively hid the prior cohort")
	}
	if count(t, e, `SELECT count(*) FROM messages WHERE msg_id=? AND history_expired_at IS NOT NULL`, newer.MessageID) != 1 {
		t.Fatal("new cohort did not use confirmed shorter age")
	}
}
