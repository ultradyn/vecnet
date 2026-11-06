package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/prefs"
)

const historyProjection = "history_retention_v1"

// HistoryRetentionAdapter composes the existing event adapter with the history
// consumer. The shared service alone validates, signs and commits confirmations.
// R2's blob policy remains unavailable until its own adapter is installed.
type HistoryRetentionAdapter struct{ Hub *Hub }

func historyEventPatch(p prefs.Patch) prefs.Patch {
	out := p
	out.Set = map[string]prefs.Value{}
	out.Reset = nil
	for k, v := range p.Set {
		if k != "retention.history" {
			out.Set[k] = v
		}
	}
	for _, k := range p.Reset {
		if k != "retention.history" {
			out.Reset = append(out.Reset, k)
		}
	}
	return out
}
func (a *HistoryRetentionAdapter) Check(ctx context.Context, before, after prefs.Snapshot, p prefs.Patch) error {
	return (&EventRetentionAdapter{a.Hub}).Check(ctx, before, after, historyEventPatch(p))
}
func retentionPreviewAge(p RetentionPolicy, hard time.Duration) time.Duration {
	seconds, _ := p.Ack.Value.Int64()
	age := time.Duration(seconds) * time.Second
	archive, _ := p.Archive.Value.Bool()
	if !archive {
		seconds, _ = p.History.Value.Int64()
		history := time.Duration(seconds) * time.Second
		for _, d := range []time.Duration{hard, history} {
			if d > 0 && (age == 0 || d < age) {
				age = d
			}
		}
	}
	return age
}
func (a *HistoryRetentionAdapter) Preview(ctx context.Context, before, after prefs.Snapshot, p prefs.Patch) (prefs.Impact, error) {
	impact, err := (&EventRetentionAdapter{a.Hub}).Preview(ctx, before, after, historyEventPatch(p))
	if err != nil || !p.ApplyExisting {
		return impact, err
	}
	policy, err := retentionPolicy(after)
	if err != nil {
		return impact, err
	}
	impact.CutoffAt = envelope.FormatTime(a.Hub.Now().Add(-retentionPreviewAge(policy, a.Hub.Cfg.UnackedEventRetention)))
	relevant := false
	for _, key := range preferencePatchKeys(p) {
		if key == "retention.history" || key == "retention.archive_mode" {
			relevant = true
		}
	}
	if !relevant {
		return impact, nil
	}
	archive, _ := policy.Archive.Value.Bool()
	seconds, _ := policy.History.Value.Int64()
	if archive || seconds == 0 {
		return impact, nil
	}
	// Preview is a consistent read snapshot. It estimates a bounded sample and
	// never promises exact physical page savings or obtains a writer lock.
	err = a.Hub.DB.ReadSnapshot(ctx, func(q *sql.Conn) error {
		rows, err := q.QueryContext(ctx, `SELECT m.msg_id,m.thr_id,length(CAST(m.body AS BLOB)) FROM messages m WHERE m.created_at<=? ORDER BY m.created_at,m.msg_id LIMIT 100`, envelope.FormatTime(a.Hub.Now().Add(-time.Duration(seconds)*time.Second)))
		if err != nil {
			return err
		}
		type candidate struct {
			id, thread string
			bytes      int64
		}
		var list []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.thread, &c.bytes); err != nil {
				rows.Close()
				return err
			}
			list = append(list, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var eligible, pinned, bytes int64
		for _, c := range list {
			reason, err := historyPin(ctx, q, c.id, c.thread, 100)
			if err != nil {
				return err
			}
			if reason != "" {
				pinned++
			} else {
				eligible++
				bytes += c.bytes
			}
		}
		*impact.Eligible += eligible
		*impact.Pinned += pinned
		*impact.EstimatedBytes += bytes
		return nil
	})
	return impact, err
}

type historyActivation struct {
	Seconds         int64  `json:"seconds"`
	Archive         bool   `json:"archive"`
	ApprovedSeconds int64  `json:"approved_seconds"`
	Pending         bool   `json:"pending"`
	ArchivePending  bool   `json:"archive_pending"`
	Generation      uint64 `json:"generation"`
	MarkGeneration  uint64 `json:"mark_generation"`
	Revision        uint64 `json:"revision"`
	ActivationKey   string `json:"activation_key,omitempty"`
	CutoffAt        string `json:"cutoff_at,omitempty"`
}

func readHistoryActivation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (historyActivation, error) {
	var a historyActivation
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, historyProjection).Scan(&raw)
	if err != nil {
		return a, err
	}
	if json.Unmarshal([]byte(raw), &a) != nil || a.Seconds < 0 || a.ApprovedSeconds < 0 {
		return a, errors.New("retention: invalid_history_activation")
	}
	// Older projections used the activation revision for expiry marks. Adopt
	// that value so an upgrade alone preserves existing marks. Revision still
	// identifies prefs-owned approvals; marks also need to track bootstrap
	// transitions, which do not advance the preference revision.
	if a.MarkGeneration == 0 {
		a.MarkGeneration = a.Revision
	}
	for _, seconds := range []int64{a.Seconds, a.ApprovedSeconds} {
		d, _ := prefs.Lookup("retention.history")
		if prefs.Validate(d, prefs.Duration(seconds)) != nil {
			return a, errors.New("retention: invalid_history_activation")
		}
	}
	return a, nil
}
func saveHistoryActivation(ctx context.Context, tx *sql.Tx, a historyActivation) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, historyProjection, string(raw))
	return err
}
func historyRuleChange(ctx context.Context, tx *sql.Tx, high, before, after int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT node_id FROM event_retention_rule_roots WHERE projection=?`, historyProjection).Scan(&id)
	var n eventRuleInterval
	if errors.Is(err, sql.ErrNoRows) {
		n, err = insertRuleInterval(ctx, tx, 0, math.MaxInt64, math.MaxInt64)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO event_retention_rule_roots(projection,node_id) VALUES(?,?)`, historyProjection, n.id)
	} else if err == nil {
		n, err = readRuleInterval(ctx, tx, id)
	}
	if err != nil {
		return err
	}
	if err = updateRuleInterval(ctx, tx, &n, high, max(ruleProtection(before), ruleProtection(after)), ruleProtection(after), 0); err != nil {
		return err
	}
	return collectEventRuleIntervals(ctx, tx, 8)
}
func historyRuleSeconds(ctx context.Context, tx *sql.Tx, seq int64) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT node_id FROM event_retention_rule_roots WHERE projection=?`, historyProjection).Scan(&id); err != nil {
		return 0, err
	}
	var protection int64
	lo, hi := int64(0), int64(math.MaxInt64)
	for depth := 0; depth <= 63; depth++ {
		n, err := readRuleInterval(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		if n.lo != lo || n.hi != hi {
			return 0, errors.New("retention: invalid_history_interval")
		}
		protection = max(protection, n.protection)
		if !n.left.Valid {
			if protection == math.MaxInt64 {
				return 0, nil
			}
			d, _ := prefs.Lookup("retention.history")
			if err := prefs.Validate(d, prefs.Duration(protection)); err != nil {
				return 0, errors.New("retention: invalid_history_interval")
			}
			return protection, nil
		}
		mid := lo + (hi-lo)/2
		if seq <= mid {
			id = n.left.Int64
			hi = mid
		} else {
			id = n.right.Int64
			lo = mid + 1
		}
	}
	return 0, errors.New("retention: invalid_history_interval")
}
func (h *Hub) initializeHistoryRetention(ctx context.Context) error {
	var generation uint64
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		s, err := h.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		p, err := retentionPolicy(s)
		if err != nil {
			return err
		}
		seconds, _ := p.History.Value.Int64()
		archive, _ := p.Archive.Value.Bool()
		var high int64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0)`).Scan(&high); err != nil {
			return err
		}
		a, err := readHistoryActivation(ctx, tx)
		fresh := errors.Is(err, sql.ErrNoRows)
		if err != nil && !fresh {
			return err
		}
		if fresh {
			a.Pending = seconds > 0
			a.ApprovedSeconds = 0
			if err := historyRuleChange(ctx, tx, high, 0, 0); err != nil {
				return err
			}
		} else if seconds != a.Seconds || archive != a.Archive {
			a.Generation++
			a.MarkGeneration++
			a.ActivationKey = ""
			a.CutoffAt = ""
			if seconds != a.Seconds {
				if seconds > 0 && (a.ApprovedSeconds == 0 || seconds < a.ApprovedSeconds) {
					a.Pending = true
				} else {
					a.Pending = false
					a.ApprovedSeconds = seconds
				}
			}
			if a.Archive && !archive {
				a.ArchivePending = true
			} else if archive {
				a.ArchivePending = false
			}
			before, after := a.ApprovedSeconds, a.ApprovedSeconds
			if a.Archive || archive || a.Pending || a.ArchivePending {
				before = 0
				after = 0
			}
			if err := historyRuleChange(ctx, tx, high, before, after); err != nil {
				return err
			}
		}
		if fresh && a.Pending {
			a.Generation++
		}
		a.Seconds, a.Archive = seconds, archive
		generation = a.Generation
		return saveHistoryActivation(ctx, tx, a)
	})
	if err != nil {
		return err
	}
	return h.Preferences.bindRetentionGeneration(ctx, generation)
}
func (a *HistoryRetentionAdapter) Activate(ctx context.Context, tx *sql.Tx, before, after prefs.Snapshot, p prefs.Patch, impact prefs.Impact, digest string) error {
	if err := (&EventRetentionAdapter{a.Hub}).Activate(ctx, tx, before, after, historyEventPatch(p), impact, digest); err != nil {
		return err
	}
	relevant := false
	historyKey := false
	archiveKey := false
	for _, key := range preferencePatchKeys(p) {
		if key == "retention.history" {
			relevant = true
			historyKey = true
		}
		if key == "retention.archive_mode" {
			relevant = true
			archiveKey = true
		}
	}
	if !relevant {
		return nil
	}
	prior, err := retentionPolicy(before)
	if err != nil {
		return err
	}
	next, err := retentionPolicy(after)
	if err != nil {
		return err
	}
	was, _ := prior.History.Value.Int64()
	seconds, _ := next.History.Value.Int64()
	oldArchive, _ := prior.Archive.Value.Bool()
	archive, _ := next.Archive.Value.Bool()
	state, err := readHistoryActivation(ctx, tx)
	if err != nil {
		return err
	}
	// A provenance-only Save cannot acknowledge pending bootstrap destruction.
	changed := was != seconds || oldArchive != archive
	if !changed && !p.ApplyExisting {
		return nil
	}
	var high int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0)`).Scan(&high); err != nil {
		return err
	}
	if p.ApplyExisting && (impact.HighWater == nil || *impact.HighWater > high || impact.NotBefore != envelope.FormatTime(a.Hub.nextRetentionSweep(a.Hub.Now()))) {
		return preferenceError(prefs.CodeConfirmationStale)
	}
	// Cancel old authorization on every relevant policy change. The interval
	// ledger keeps the prior eligibility rule; fixed bounds live in shared prefs.
	state.ActivationKey = ""
	state.CutoffAt = ""
	if historyKey {
		confirmedShortening := seconds > 0 && (was == 0 || seconds < was)
		if !state.Pending || confirmedShortening || p.ApplyExisting || seconds == 0 || state.ApprovedSeconds > 0 && seconds >= state.ApprovedSeconds {
			state.Pending = false
			state.ApprovedSeconds = seconds
		}
		if err := invalidateEventApproval(ctx, tx, "retention.history"); err != nil {
			return err
		}
		var nb, cutoff, bound any
		if p.ApplyExisting {
			nb, cutoff, bound = impact.NotBefore, impact.CutoffAt, *impact.HighWater
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hub_preference_activations(revision,key,effective_from,not_before,cutoff_at,high_water,approval_digest) VALUES(?,?,?,?,?,?,?)`, after.Revision, "retention.history", a.Hub.nowStr(), nb, cutoff, bound, digest); err != nil {
			return err
		}
	}
	if archiveKey {
		if archive || oldArchive && !archive || p.ApplyExisting {
			state.ArchivePending = false
		}
	}
	priorSeconds := was
	if oldArchive || state.Pending || state.ArchivePending {
		priorSeconds = 0
	}
	nextSeconds := state.ApprovedSeconds
	if archive || state.Pending || state.ArchivePending {
		nextSeconds = 0
	}
	if err := historyRuleChange(ctx, tx, high, priorSeconds, nextSeconds); err != nil {
		return err
	}
	if p.ApplyExisting && seconds > 0 && !archive && !state.Pending && !state.ArchivePending {
		cutoff, err := envelope.ParseTime(impact.CutoffAt)
		if err != nil {
			return preferenceError(prefs.CodeConfirmationStale)
		}
		state.CutoffAt = envelope.FormatTime(cutoff.Add(retentionPreviewAge(next, a.Hub.Cfg.UnackedEventRetention) - time.Duration(seconds)*time.Second))
		state.ActivationKey = "retention.history"
		if !historyKey {
			state.ActivationKey = "retention.archive_mode"
		}
	}
	// Unrelated and provenance-only saves returned above. A semantic policy
	// change or a new existing-data authorization invalidates prior marks in
	// the same transaction as the new policy, without performing any deletion.
	state.MarkGeneration++
	state.Seconds, state.Archive, state.Revision = seconds, archive, after.Revision
	return saveHistoryActivation(ctx, tx, state)
}
