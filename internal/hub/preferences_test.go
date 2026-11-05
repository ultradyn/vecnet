package hub_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/prefs"
	"github.com/XertroV/vecnet/internal/store"
)

var prefsOperator = auth.Caller{Kind: "operator", PeerKind: "operator"}

func preferencesHub(t *testing.T, cfg hub.Config) (*hub.Hub, string) {
	t.Helper()
	dir := t.TempDir()
	cfg.DataDir = dir
	path := filepath.Join(dir, "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h, err := hub.New(db, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h, path
}
func readPrefs(t *testing.T, h *hub.Hub) prefs.Snapshot {
	t.Helper()
	s, err := h.Preferences.ReadPreferences(context.Background(), prefsOperator)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func prefValue(t *testing.T, s prefs.Snapshot, key string, n int64, source prefs.Source, origin string) {
	t.Helper()
	e, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := e.Value.Int64()
	if !ok || got != n || e.Source != source || e.Origin != origin || e.Revision != s.Revision {
		t.Fatalf("%s = %+v (%d), want %d %s %s revision %d", key, e, got, n, source, origin, s.Revision)
	}
}
func patchPrefs(t *testing.T, h *hub.Hub, s prefs.Snapshot, p prefs.Patch) prefs.Snapshot {
	t.Helper()
	next, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, p)
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func prefsCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *prefs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
func prefsAuditCount(t *testing.T, h *hub.Hub, want int) {
	t.Helper()
	var n int
	if err := h.DB.QueryRow(`SELECT count(*) FROM audit WHERE action='prefs_changed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("audits = %d, want %d", n, want)
	}
}
func TestPreferencesPrecedenceResetAndRestart(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.PreferenceBootstrap = map[string]prefs.Effective{"agents.thread_budget": {Value: prefs.Int(700), Source: prefs.SourceFlag, Origin: "VECNET_THREAD_BUDGET"}}
	h, path := preferencesHub(t, cfg)
	s := readPrefs(t, h)
	prefValue(t, s, "agents.thread_budget", 700, prefs.SourceFlag, "VECNET_THREAD_BUDGET")
	prefValue(t, s, "events.refresh_interval", 1800, prefs.SourceDefault, "")
	var count int
	if err := h.DB.QueryRow(`SELECT count(*) FROM hub_preferences`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("defaults inserted: %d %v", count, err)
	}
	s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(500)}})
	prefValue(t, s, "agents.thread_budget", 500, prefs.SourceStored, "")
	cfg.DataDir = h.Cfg.DataDir
	cfg.PreferenceBootstrap["agents.thread_budget"] = prefs.Effective{Value: prefs.Int(800), Source: prefs.SourceFlag, Origin: "--thread-budget"}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	again, err := hub.New(db, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted := readPrefs(t, again)
	prefValue(t, restarted, "agents.thread_budget", 500, prefs.SourceStored, "")
	if restarted.Revision != s.Revision || restarted.ETag == s.ETag {
		t.Fatal("restart must preserve revision and invalidate bootstrap ETag")
	}
	_, err = again.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Reset: []string{"agents.thread_budget"}})
	prefsCode(t, err, prefs.CodeRevisionConflict)
	reset := patchPrefs(t, again, restarted, prefs.Patch{Reset: []string{"agents.thread_budget"}})
	prefValue(t, reset, "agents.thread_budget", 800, prefs.SourceFlag, "--thread-budget")
}
func TestPreferencesNoopNormalizationSourcesAndImmutability(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	old := readPrefs(t, h)
	s := patchPrefs(t, h, old, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(500)}})
	if s.ETag != old.ETag || s.Revision != 0 {
		t.Fatal("inherited built-in must normalize to no-op")
	}
	prefsAuditCount(t, h, 0)
	s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}})
	same := patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}})
	if same.ETag != s.ETag {
		t.Fatal("identical override revised")
	}
	prefsAuditCount(t, h, 1)
	prefValue(t, old, "agents.thread_budget", 500, prefs.SourceDefault, "")
	rows := s.Descriptors()
	rows[0].Label = "mutated"
	if s.Descriptors()[0].Label == "mutated" {
		t.Fatal("mutable snapshot")
	}
	s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(500)}})
	prefValue(t, s, "agents.thread_budget", 500, prefs.SourceDefault, "")
	if s.Revision != 2 {
		t.Fatal("reset must revise")
	}
	prefsAuditCount(t, h, 2)
	cfg := hub.DefaultConfig()
	cfg.PreferenceBootstrap = map[string]prefs.Effective{"agents.thread_budget": {Value: prefs.Int(500), Source: prefs.SourceFlag, Origin: "--thread-budget"}}
	h, _ = preferencesHub(t, cfg)
	s = readPrefs(t, h)
	prefValue(t, s, "agents.thread_budget", 500, prefs.SourceFlag, "--thread-budget")
	s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(500)}})
	prefValue(t, s, "agents.thread_budget", 500, prefs.SourceStored, "")
	if s.Revision != 1 {
		t.Fatal("source-only change did not revise")
	}
	s = patchPrefs(t, h, s, prefs.Patch{Reset: []string{"agents.thread_budget"}})
	prefValue(t, s, "agents.thread_budget", 500, prefs.SourceFlag, "--thread-budget")
}
func TestPreferencesConcurrentMultiKeyCAS(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	initial := readPrefs(t, h)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, n := range []int64{700, 800} {
		wg.Go(func() {
			_, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, initial.ETag, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(n), "rates.sends_per_minute": prefs.Int(n / 10)}})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			prefsCode(t, err, prefs.CodeRevisionConflict)
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d", successes)
	}
	s := readPrefs(t, h)
	budget, _ := s.Get("agents.thread_budget")
	n, _ := budget.Value.Int64()
	if n != 700 && n != 800 {
		t.Fatal("budget did not match either contender")
	}
	prefValue(t, s, "agents.thread_budget", n, prefs.SourceStored, "")
	prefValue(t, s, "rates.sends_per_minute", n/10, prefs.SourceStored, "")
	if s.Revision != 1 {
		t.Fatal("CAS revision")
	}
	prefsAuditCount(t, h, 1)
}
func TestPreferencesRollbackStorageAndAudit(t *testing.T) {
	for _, target := range []string{"audit", "hub_preferences_state", "hub_preferences"} {
		t.Run(target, func(t *testing.T) {
			h, _ := preferencesHub(t, hub.DefaultConfig())
			s := readPrefs(t, h)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wake := h.Preferences.Subscribe(ctx)
			operation := "INSERT"
			if target == "hub_preferences_state" {
				operation = "UPDATE"
			}
			_, err := h.DB.Exec(`CREATE TRIGGER fail_pref BEFORE ` + operation + ` ON ` + target + ` BEGIN SELECT RAISE(ABORT, 'synthetic rejected credential'); END`)
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.Preferences.PatchPreferences(ctx, prefsOperator, s.ETag, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600), "rates.sends_per_minute": prefs.Int(70)}})
			prefsCode(t, err, prefs.CodeStorageUnavailable)
			if strings.Contains(err.Error(), "credential") {
				t.Fatal("storage detail leaked")
			}
			after := readPrefs(t, h)
			if after.ETag != s.ETag {
				t.Fatal("failure changed revision")
			}
			prefValue(t, after, "agents.thread_budget", 500, prefs.SourceDefault, "")
			prefsAuditCount(t, h, 0)
			select {
			case <-wake:
				t.Fatal("uncommitted notification")
			default:
			}
		})
	}
}
func TestPreferencesETagsAuthorityAndPolicy(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	p := prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}}
	for _, tag := range []string{"", "*", "W/" + s.ETag, s.ETag + ", " + s.ETag} {
		_, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, tag, p)
		prefsCode(t, err, prefs.CodePreconditionRequired)
	}
	for _, caller := range []auth.Caller{{}, {Kind: "principal"}, {Kind: "peer", PeerKind: "operator"}, {Kind: "principal", PeerKind: "dot"}, {Kind: "operator", PeerKind: "session", PeerID: "peer_delegated"}} {
		if _, err := h.Preferences.ReadPreferences(context.Background(), caller); err == nil {
			t.Fatal("read auth bypass")
		}
		if _, err := h.Preferences.PreviewPreferences(context.Background(), caller, s.ETag, p); err == nil {
			t.Fatal("preview auth bypass")
		}
		if _, err := h.Preferences.PatchPreferences(context.Background(), caller, s.ETag, p); err == nil {
			t.Fatal("patch auth bypass")
		}
	}
	for _, key := range []string{"retention.history", "agents.max_expiry"} {
		_, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Reset: []string{key}})
		prefsCode(t, err, prefs.CodeUnavailable)
	}
	_, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(10001)}})
	prefsCode(t, err, prefs.CodeInvalidValue)
	_, err = h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{ApplyExisting: true, Set: p.Set})
	prefsCode(t, err, prefs.CodeUnavailable)
	prefsAuditCount(t, h, 0)
}
func TestPreferencesInvalidPersistedStateFailsStartupWithoutExposure(t *testing.T) {
	for _, tc := range []struct{ key, value, code string }{{"agents.thread_budget", "10001", prefs.CodeInvalidValue}, {"agents.thread_budget", `"synthetic-secret"`, prefs.CodeInvalidValue}, {"synthetic-secret", "1", prefs.CodeUnknownKey}, {"retention.history", "0", prefs.CodeUnavailable}} {
		t.Run(tc.code+tc.key, func(t *testing.T) {
			h, _ := preferencesHub(t, hub.DefaultConfig())
			if _, err := h.DB.Exec(`INSERT INTO hub_preferences VALUES (?, ?, 'now', 'operator')`, tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			_, err := hub.New(h.DB, h.Cfg, nil)
			if err == nil || !strings.Contains(err.Error(), tc.code) || !strings.Contains(err.Error(), "runbook") || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("unsafe or missing startup diagnostic: %v", err)
			}
		})
	}
}
func TestPreferencesLatestWakeupCancellationAndPublication(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	wake := h.Preferences.Subscribe(ctx)
	if cap(wake) != 1 {
		t.Fatal("wakeup must have capacity one")
	}
	defer cancel()
	s := readPrefs(t, h)
	for _, n := range []int64{600, 700, 800} {
		s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(n)}})
	}
	select {
	case rev := <-wake:
		if rev != 3 {
			t.Fatalf("latest wakeup %d", rev)
		}
	case <-time.After(time.Second):
		t.Fatal("no commit wakeup")
	}
	if h.Preferences.Current().Revision != s.Revision {
		t.Fatal("snapshot not published before return")
	}
	if h.Cfg.ThreadBudget != 500 {
		t.Fatal("write mutated Cfg")
	}
	cancel()
	select {
	case _, ok := <-wake:
		if ok {
			t.Fatal("channel open after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close subscription")
	}
}
func TestPreferencesAuditTypedValuesAndConsistentTransactionRead(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	s = patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(600)}})
	var detail string
	if err := h.DB.QueryRow(`SELECT detail FROM audit WHERE action='prefs_changed'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Revision uint64 `json:"revision"`
		Changes  []struct {
			Key       string          `json:"key"`
			Operation string          `json:"operation"`
			Old       prefs.Effective `json:"old"`
			New       prefs.Effective `json:"new"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(detail), &audit); err != nil {
		t.Fatal(err)
	}
	if audit.Revision != 1 || len(audit.Changes) != 1 {
		t.Fatalf("audit %s", detail)
	}
	c := audit.Changes[0]
	old, _ := c.Old.Value.Int64()
	next, _ := c.New.Value.Int64()
	if c.Key != "agents.thread_budget" || c.Operation != "set" || old != 500 || next != 600 || c.Old.Source != prefs.SourceDefault || c.New.Source != prefs.SourceStored {
		t.Fatalf("audit %s", detail)
	}
	tx, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE hub_preferences SET value_json='750' WHERE key='agents.thread_budget'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE hub_preferences_state SET revision=? WHERE id=1`, s.Revision+1); err != nil {
		t.Fatal(err)
	}
	loaded, err := h.Preferences.LoadSnapshot(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != s.Revision+1 || loaded.ETag == s.ETag {
		t.Fatal("transaction snapshot did not observe uncommitted revision")
	}
	prefValue(t, loaded, "agents.thread_budget", 750, prefs.SourceStored, "")
	if h.Preferences.Current().ETag != s.ETag {
		t.Fatal("transaction read published uncommitted snapshot")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	standalone, err := h.Preferences.LoadSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, committed := range []prefs.Snapshot{standalone, h.Preferences.Current()} {
		if committed.Revision != s.Revision || committed.ETag != s.ETag {
			t.Fatal("rollback changed committed snapshot")
		}
		prefValue(t, committed, "agents.thread_budget", 600, prefs.SourceStored, "")
	}
	prefsAuditCount(t, h, 1)
}
func TestPreferencesCrashAfterCommitRecovers(t *testing.T) {
	h, path := preferencesHub(t, hub.DefaultConfig())
	if err := h.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hub_preferences VALUES ('agents.thread_budget','750','now','operator')`); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE hub_preferences_state SET revision=1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	again, err := hub.New(db, h.Cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := readPrefs(t, again)
	prefValue(t, s, "agents.thread_budget", 750, prefs.SourceStored, "")
	if s.Revision != 1 || again.Preferences.Current().Revision != 1 {
		t.Fatal("committed generation lost on restart")
	}
}

func TestPreferencesCompletePolicyAndBootstrapGeneration(t *testing.T) {
	cfg := hub.DefaultConfig()
	cfg.MaxExpiry = 24 * time.Hour
	h, path := preferencesHub(t, cfg)
	s := readPrefs(t, h)
	_, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Set: map[string]prefs.Value{"agents.default_ttl": prefs.Duration(172800), "agents.thread_budget": prefs.Int(600)}})
	prefsCode(t, err, prefs.CodeInvalidValue)
	after := readPrefs(t, h)
	if after.ETag != s.ETag {
		t.Fatal("invalid prospective policy committed")
	}
	prefValue(t, after, "agents.thread_budget", 500, prefs.SourceDefault, "")
	prefsAuditCount(t, h, 0)
	cfg.DataDir = h.Cfg.DataDir
	cfg.PreferencePolicyGeneration = "deployment-policy-2"
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	again, err := hub.New(db, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := readPrefs(t, again)
	if next.Revision != 0 || next.ETag == s.ETag {
		t.Fatal("deployment generation did not invalidate ETag")
	}
}
func TestPreferencesConfirmationSeamsFailClosed(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	p := prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800)}}
	preview, err := h.Preferences.PreviewPreferences(context.Background(), prefsOperator, s.ETag, p)
	if err != nil {
		t.Fatal(err)
	}
	if preview.BaseRevision != 0 || len(preview.Risks) != 1 || preview.Risks[0].Code != "presence_shortened" || preview.ConfirmToken != "" {
		t.Fatal("missing safe preview seam")
	}
	_, err = h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, p)
	prefsCode(t, err, prefs.CodeConfirmationRequired)
	prefsAuditCount(t, h, 0)
	h.Preferences.AuthorizePatch = func(ctx context.Context, tx *sql.Tx, caller auth.Caller, before, after prefs.Snapshot, patch prefs.Patch) ([]string, error) {
		if before.Revision != 0 || after.Revision != 1 {
			t.Fatal("authorization has wrong generations")
		}
		// A later-task hook shares the value/audit transaction. Its write must roll
		// back when it refuses authorization.
		if _, err := tx.ExecContext(ctx, `INSERT INTO hub_preference_activations VALUES(1,'presence.default_ttl','now',NULL,NULL,NULL,'synthetic-digest')`); err != nil {
			return nil, err
		}
		return nil, &prefs.Error{Code: prefs.CodeConfirmationStale}
	}
	_, err = h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, p)
	prefsCode(t, err, prefs.CodeConfirmationStale)
	var n int
	if err := h.DB.QueryRow(`SELECT count(*) FROM hub_preference_activations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("activation escaped failed transaction: %d %v", n, err)
	}
	if h.Preferences.Current().Revision != 0 {
		t.Fatal("failed hook published")
	}
}

func TestPreferencesSuccessfulConfirmationAuditExcludesTokens(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	confirmations := []string{"synthetic-confirmation-one", "synthetic-confirmation-\"two<&>"}
	hookCalled := false
	h.Preferences.AuthorizePatch = func(ctx context.Context, tx *sql.Tx, caller auth.Caller, before, after prefs.Snapshot, patch prefs.Patch) ([]string, error) {
		hookCalled = true
		if len(patch.Confirm) != len(confirmations) || patch.Confirm[0] != confirmations[0] || patch.Confirm[1] != confirmations[1] {
			t.Fatal("authorization did not receive the confirmations")
		}
		return []string{"presence_shortened"}, nil
	}
	next := patchPrefs(t, h, s, prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800)}, Confirm: confirmations})
	if !hookCalled || next.Revision != 1 {
		t.Fatal("successful authorization did not commit")
	}
	prefsAuditCount(t, h, 1)
	var row string
	if err := h.DB.QueryRow(`SELECT at || actor || action || target_ids || detail FROM audit WHERE action='prefs_changed'`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	for _, confirmation := range confirmations {
		escaped, err := json.Marshal(confirmation)
		if err != nil {
			t.Fatal("cannot encode synthetic confirmation")
		}
		for _, encoding := range []string{
			confirmation, string(escaped[1 : len(escaped)-1]),
			base64.StdEncoding.EncodeToString([]byte(confirmation)),
			base64.RawStdEncoding.EncodeToString([]byte(confirmation)),
			base64.URLEncoding.EncodeToString([]byte(confirmation)),
			base64.RawURLEncoding.EncodeToString([]byte(confirmation)),
			hex.EncodeToString([]byte(confirmation)),
		} {
			if strings.Contains(row, encoding) {
				t.Fatal("confirmation material entered an audit row")
			}
		}
	}
	var detail string
	if err := h.DB.QueryRow(`SELECT detail FROM audit WHERE action='prefs_changed'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var record struct {
		ApprovedRisks []string `json:"approved_risks"`
	}
	if err := json.Unmarshal([]byte(detail), &record); err != nil {
		t.Fatal("invalid audit detail")
	}
	if len(record.ApprovedRisks) != 1 || record.ApprovedRisks[0] != "presence_shortened" {
		t.Fatal("audit must contain only the approved server-derived risk codes")
	}
}
func TestPreferencesRevisionMonotonicityAndConcurrentPublication(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Go(func() {
			for {
				s, err := h.Preferences.ReadPreferences(context.Background(), prefsOperator)
				if err != nil {
					errs <- err
					return
				}
				v, _ := s.Get("agents.thread_budget")
				n, _ := v.Value.Int64()
				next, err := h.Preferences.PatchPreferences(context.Background(), prefsOperator, s.ETag, prefs.Patch{Set: map[string]prefs.Value{"agents.thread_budget": prefs.Int(n + 1)}})
				if err != nil {
					var e *prefs.Error
					if errors.As(err, &e) && e.Code == prefs.CodeRevisionConflict {
						continue
					}
					errs <- err
					return
				}
				if next.Revision != s.Revision+1 {
					errs <- errors.New("non-monotonic revision")
					return
				}
				errs <- nil
				return
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := readPrefs(t, h)
	if s.Revision != 6 || h.Preferences.Current().Revision != 6 {
		t.Fatal("older publication replaced latest revision")
	}
	prefValue(t, s, "agents.thread_budget", 506, prefs.SourceStored, "")
	prefsAuditCount(t, h, 6)
}

func TestPreferencesConfigDurationsValidatedBeforeConversion(t *testing.T) {
	fields := []struct {
		key   string
		set   func(*hub.Config, time.Duration)
		valid time.Duration
		max   time.Duration
	}{
		{"events.refresh_interval", func(c *hub.Config, d time.Duration) { c.EventsRefreshInterval = d }, 2 * time.Minute, 24 * time.Hour},
		{"presence.default_ttl", func(c *hub.Config, d time.Duration) { c.PresenceTTL = d }, 2 * time.Minute, 24 * time.Hour},
		{"presence.session_ttl", func(c *hub.Config, d time.Duration) { c.SessionPresenceTTL = d }, 2 * time.Minute, 24 * time.Hour},
		{"presence.launcher_ttl", func(c *hub.Config, d time.Duration) { c.LauncherPresenceTTL = d }, 2 * time.Minute, 24 * time.Hour},
		{"agents.default_ttl", func(c *hub.Config, d time.Duration) { c.DefaultTTL = d }, 2 * time.Minute, 7 * 24 * time.Hour},
		{"agents.max_expiry", func(c *hub.Config, d time.Duration) { c.MaxExpiry = d }, 2 * 24 * time.Hour, 7 * 24 * time.Hour},
	}
	for _, field := range fields {
		t.Run(field.key, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				duration time.Duration
				invalid  bool
			}{
				{"subsecond", 500 * time.Millisecond, true},
				{"fractional_second", field.valid + 500*time.Millisecond, true},
				{"negative_subsecond", -500 * time.Millisecond, true},
				{"negative_whole_second", -time.Second, true},
				{"above_maximum", field.max + time.Second, true},
				{"above_maximum_fractional", field.max + 500*time.Millisecond, true},
				{"zero_unset", 0, false},
				{"whole_seconds", field.valid, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg := hub.DefaultConfig()
					cfg.DataDir = t.TempDir()
					field.set(&cfg, tc.duration)
					db, err := store.Open(filepath.Join(cfg.DataDir, "hub.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					h, err := hub.New(db, cfg, nil)
					if tc.invalid {
						prefsCode(t, err, prefs.CodeInvalidValue)
						var pe *prefs.Error
						if !errors.As(err, &pe) || pe.Details.Field != field.key {
							t.Fatalf("duration validation did not identify %s: %v", field.key, err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if field.key == "agents.max_expiry" {
						return
					}
					want, source := int64(tc.duration/time.Second), prefs.SourceFlag
					if tc.duration == 0 {
						d, err := prefs.Lookup(field.key)
						if err != nil {
							t.Fatal(err)
						}
						want, _ = d.Default.Int64()
						source = prefs.SourceDefault
					}
					prefValue(t, h.Preferences.Current(), field.key, want, source, "")
				})
			}
		})
	}
}

func TestPreferencesCommitFailureDoesNotPublish(t *testing.T) {
	h, _ := preferencesHub(t, hub.DefaultConfig())
	s := readPrefs(t, h)
	for _, statement := range []string{
		`CREATE TABLE prefs_commit_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE prefs_commit_child (parent_id INTEGER REFERENCES prefs_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
	} {
		if _, err := h.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := h.Preferences.Subscribe(ctx)
	blockCommit, hookSucceeded := true, false
	h.Preferences.AuthorizePatch = func(ctx context.Context, tx *sql.Tx, caller auth.Caller, before, after prefs.Snapshot, patch prefs.Patch) ([]string, error) {
		if blockCommit {
			// This statement succeeds. The missing parent is checked at COMMIT,
			// after the preference, revision and audit statements have succeeded.
			if _, err := tx.ExecContext(ctx, `INSERT INTO prefs_commit_child VALUES (1)`); err != nil {
				return nil, err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO hub_preference_activations VALUES (?, 'presence.default_ttl', 'now', NULL, NULL, NULL, 'synthetic-digest')`, after.Revision)
		hookSucceeded = err == nil
		return []string{"presence_shortened"}, err
	}
	patch := prefs.Patch{Set: map[string]prefs.Value{"presence.default_ttl": prefs.Duration(1800), "agents.thread_budget": prefs.Int(700)}}
	_, err := h.Preferences.PatchPreferences(ctx, prefsOperator, s.ETag, patch)
	prefsCode(t, err, prefs.CodeStorageUnavailable)
	if !hookSucceeded {
		t.Fatal("authorization statements failed before commit")
	}
	for _, snapshot := range []prefs.Snapshot{readPrefs(t, h), h.Preferences.Current()} {
		if snapshot.ETag != s.ETag || snapshot.Revision != s.Revision {
			t.Fatal("failed commit published a revision")
		}
		prefValue(t, snapshot, "presence.default_ttl", 3600, prefs.SourceDefault, "")
		prefValue(t, snapshot, "agents.thread_budget", 500, prefs.SourceDefault, "")
	}
	for _, table := range []string{"hub_preferences", "hub_preference_activations", "prefs_commit_child"} {
		var count int
		if err := h.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed commit left rows in %s: %d %v", table, count, err)
		}
	}
	prefsAuditCount(t, h, 0)
	select {
	case <-wake:
		t.Fatal("failed commit sent a wakeup")
	default:
	}
	// The same ETag remains usable after rollback; only a successful retry
	// may publish the next snapshot and notify subscribers.
	blockCommit = false
	next := patchPrefs(t, h, s, patch)
	if next.Revision != s.Revision+1 || h.Preferences.Current().ETag != next.ETag {
		t.Fatal("retry did not publish the committed generation")
	}
	select {
	case revision := <-wake:
		if revision != next.Revision {
			t.Fatal("retry notified the wrong revision")
		}
	default:
		t.Fatal("successful retry did not notify")
	}
	prefsAuditCount(t, h, 1)
}
