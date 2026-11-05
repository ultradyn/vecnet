package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/prefs"
)

const eventRetentionProjectionKey = "event_retention_projection_v1"
const defaultAckSeconds = int64(7 * 24 * 60 * 60)

// RetentionPolicy is a transaction-local projection, never a second preference
// store or cached deletion authority. Effective keeps exact prefs provenance.
type RetentionPolicy struct {
	Ack      prefs.Effective `json:"ack"`
	History  prefs.Effective `json:"history"`
	Archive  prefs.Effective `json:"archive"`
	Revision uint64          `json:"revision"`
}

func retentionPolicy(s prefs.Snapshot) (RetentionPolicy, error) {
	p := RetentionPolicy{Revision: s.Revision}
	var err error
	if p.Ack, err = s.Get("retention.ack_events"); err != nil {
		return p, err
	}
	if p.History, err = s.Get("retention.history"); err != nil {
		return p, err
	}
	if p.Archive, err = s.Get("retention.archive_mode"); err != nil {
		return p, err
	}
	for key, e := range map[string]prefs.Effective{"retention.ack_events": p.Ack, "retention.history": p.History, "retention.archive_mode": p.Archive} {
		d, err := prefs.Lookup(key)
		if err != nil {
			return p, err
		}
		if err = prefs.Validate(d, e.Value); err != nil {
			return p, err
		}
	}
	return p, nil
}

// eventRuleBoundary is the minimal persisted prior-rule projection needed by
// prospective-only policy edits. Preferences remain solely in hub_preferences;
// signed retrospective bounds remain solely in hub_preference_activations.
type eventRuleBoundary struct {
	Revision            uint64 `json:"revision"`
	FromSeq             int64  `json:"from_seq"`
	BeforeSeconds       int64  `json:"before_seconds"`
	AfterSeconds        int64  `json:"after_seconds"`
	PolicyTag           string `json:"policy_tag"`
	BootstrapGeneration uint64 `json:"bootstrap_generation,omitempty"`
	// Bootstrap protection does not mutate the shared preference revision.
	// It is ordered after the activation at that revision, before future Saves.
	BootstrapTransition bool `json:"bootstrap_transition,omitempty"`
	// Hard expiry is a separate class: disabled classes receive no authority.
	HardDuration time.Duration `json:"hard_duration_ns,omitempty"`
	HardCutoffAt string        `json:"hard_cutoff_at,omitempty"`
	AckCutoffAt  string        `json:"ack_cutoff_at,omitempty"`
}

func eventPolicyTag(e prefs.Effective) string {
	b, _ := json.Marshal(struct {
		Value  prefs.Value
		Source prefs.Source
		Origin string
	}{e.Value, e.Source, e.Origin})
	return string(b)
}
func loadEventBoundaries(ctx context.Context, tx *sql.Tx) ([]eventRuleBoundary, error) {
	return loadEventBoundariesKey(ctx, tx, eventRetentionProjectionKey)
}
func loadEventBoundariesKey(ctx context.Context, tx *sql.Tx, key string) ([]eventRuleBoundary, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b []eventRuleBoundary
	if json.Unmarshal([]byte(raw), &b) != nil {
		return nil, errors.New("retention: invalid_activation_projection")
	}
	var revision uint64
	var seq int64
	for _, v := range b {
		if v.Revision < revision || v.Revision == revision && !v.BootstrapTransition || v.FromSeq < seq || v.BeforeSeconds < 0 || v.AfterSeconds < 0 || v.HardDuration < 0 {
			return nil, errors.New("retention: invalid_activation_projection")
		}
		revision = v.Revision
		seq = v.FromSeq
	}
	return b, nil
}

// EventRetentionAdapter installs only acknowledged-event and archive keys.
// History expiry remains unavailable until its Task 7 consumer is installed;
// blobs.expiry belongs to R2. All methods are metadata-only and never delete.
type EventRetentionAdapter struct{ Hub *Hub }

func (a *EventRetentionAdapter) Check(_ context.Context, _, _ prefs.Snapshot, p prefs.Patch) error {
	for _, key := range preferencePatchKeys(p) {
		if key == "retention.history" || key == "blobs.expiry" {
			return preferenceError(prefs.CodeUnavailable)
		}
	}
	return nil
}
func (h *Hub) nextRetentionSweep(now time.Time) time.Time {
	n := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), h.Cfg.RetentionSweepHourUTC, 0, 0, 0, time.UTC)
	if !n.After(now) {
		n = n.Add(24 * time.Hour)
	}
	return n
}
func (a *EventRetentionAdapter) Preview(ctx context.Context, _, after prefs.Snapshot, p prefs.Patch) (prefs.Impact, error) {
	if !p.ApplyExisting {
		return prefs.Impact{}, nil
	}
	policy, err := retentionPolicy(after)
	if err != nil {
		return prefs.Impact{}, err
	}
	seconds, _ := policy.Ack.Value.Int64()
	now := a.Hub.Now()
	ackAge := time.Duration(seconds) * time.Second
	hardAge := a.Hub.Cfg.UnackedEventRetention
	archive, _ := policy.Archive.Value.Bool()
	if archive {
		hardAge = 0
	}
	cutoffAge := ackAge
	if hardAge > 0 && (cutoffAge == 0 || hardAge < cutoffAge) {
		cutoffAge = hardAge
	}
	cutoff := envelope.FormatTime(now.Add(-cutoffAge))
	var high, eligible, pinned, bytes int64
	err = a.Hub.DB.ReadSnapshot(ctx, func(q *sql.Conn) error {
		if err := q.QueryRowContext(ctx, `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0)`).Scan(&high); err != nil {
			return err
		}
		var err error
		eligible, pinned, bytes, err = a.Hub.Log.PreviewRetention(ctx, q, now, ackAge, hardAge, high)
		return err
	})
	return prefs.Impact{CutoffAt: cutoff, HighWater: &high, NotBefore: envelope.FormatTime(a.Hub.nextRetentionSweep(now)), Eligible: &eligible, Pinned: &pinned, EstimatedBytes: &bytes}, err
}
func (a *EventRetentionAdapter) Activate(ctx context.Context, tx *sql.Tx, before, after prefs.Snapshot, p prefs.Patch, impact prefs.Impact, digest string) error {
	keys := preferencePatchKeys(p)
	hasRetention := false
	for _, key := range keys {
		if key == "retention.ack_events" || key == "retention.archive_mode" {
			hasRetention = true
		}
	}
	if !hasRetention {
		return nil
	}
	if p.ApplyExisting {
		if impact.NotBefore != envelope.FormatTime(a.Hub.nextRetentionSweep(a.Hub.Now())) || impact.HighWater == nil {
			return preferenceError(prefs.CodeConfirmationStale)
		}
		var high int64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0)`).Scan(&high); err != nil {
			return err
		}
		if *impact.HighWater > high {
			return preferenceError(prefs.CodeConfirmationStale)
		}
	}
	bootstrap, err := loadRetentionBootstrap(ctx, tx)
	if err != nil {
		return err
	}
	now := envelope.FormatTime(a.Hub.Now())
	for _, key := range keys {
		if key != "retention.ack_events" && key != "retention.archive_mode" {
			continue
		}
		old, _ := before.Get(key)
		next, _ := after.Get(key)
		// Effective.Revision changes for every key when any preference changes.
		// A touched no-op key must not activate or cancel a retention rule.
		if old.Value == next.Value && old.Source == next.Source && old.Origin == next.Origin && !p.ApplyExisting {
			continue
		}
		// A source-only Save carries no shared confirmation for a pending
		// bootstrap transition. Preserve both its pending generation and prior
		// eligibility ledger; provenance is not deletion authority.
		pending := key == "retention.ack_events" && bootstrap.AckPending || key == "retention.archive_mode" && bootstrap.ArchivePending
		if pending && !p.ApplyExisting {
			if old.Value == next.Value {
				continue
			}
			if key == "retention.ack_events" {
				was, _ := old.Value.Int64()
				seconds, _ := next.Value.Int64()
				approved := *bootstrap.ApprovedAckSeconds
				stillShorter := seconds > 0 && (approved == 0 || seconds < approved)
				// Only a visible shortening receives the shared service's
				// retention_shortened confirmation. Partial lengthening of
				// an unapproved value must keep the transition pending.
				confirmedShortening := seconds > 0 && (was == 0 || seconds < was)
				if stillShorter && !confirmedShortening {
					bootstrap.AckSeconds = seconds
					continue
				}
			}
		}
		// Any policy edit invalidates unused incompatible retrospective approval.
		if _, err := tx.ExecContext(ctx, `UPDATE hub_preference_activations SET not_before=NULL,cutoff_at=NULL,high_water=NULL WHERE key=?`, key); err != nil {
			return err
		}
		var nb, cutoff, high any
		if p.ApplyExisting {
			nb, cutoff, high = impact.NotBefore, impact.CutoffAt, *impact.HighWater
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hub_preference_activations(revision,key,effective_from,not_before,cutoff_at,high_water,approval_digest) VALUES(?,?,?,?,?,?,?)`, after.Revision, key, now, nb, cutoff, high, digest); err != nil {
			return err
		}
		if key == "retention.ack_events" || key == "retention.archive_mode" {
			projectionKey := eventRetentionProjectionKey
			if key == "retention.archive_mode" {
				projectionKey += "_archive"
			}
			bounds, err := loadEventBoundariesKey(ctx, tx, projectionKey)
			if err != nil {
				return err
			}
			var from int64
			if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0)`).Scan(&from); err != nil {
				return err
			}
			was, _ := old.Value.Int64()
			if key == "retention.ack_events" && len(bounds) == 0 {
				was = *bootstrap.ApprovedAckSeconds
			}
			seconds, _ := next.Value.Int64()
			if key == "retention.archive_mode" {
				v, _ := old.Value.Bool()
				if v {
					was = 1
				}
				v, _ = next.Value.Bool()
				if v {
					seconds = 1
				}
			}
			// A changed bootstrap cannot authorize shorter historical rules on restart.
			// If the prior projection no longer matches the snapshot, retain indefinitely.
			if len(bounds) > 0 && bounds[len(bounds)-1].PolicyTag != eventPolicyTag(old) && !pending {
				was = 0
			}
			if pending {
				if key == "retention.archive_mode" {
					was = 1
				}
				transition := bootstrap.AckFromSeq
				if key == "retention.archive_mode" {
					transition = bootstrap.ArchiveFromSeq
				}
				if from < transition {
					return errors.New("retention: invalid_activation_boundary")
				}
			}
			if key == "retention.archive_mode" && seconds == 0 && (was != 0 || p.ApplyExisting) {
				// A true archive exit is confirmed by the shared service, even
				// prospectively. A source-only false Save is not confirmation.
				bootstrap.HardPending = false
			}
			boundary := eventRuleBoundary{Revision: after.Revision, FromSeq: from, BeforeSeconds: was, AfterSeconds: seconds, PolicyTag: eventPolicyTag(next), BootstrapGeneration: bootstrap.Generation}
			if p.ApplyExisting {
				if key == "retention.ack_events" && seconds > 0 {
					boundary.AckCutoffAt, err = a.classPreviewCutoff(after, impact, time.Duration(seconds)*time.Second)
				} else if key == "retention.archive_mode" && seconds == 0 && a.Hub.Cfg.UnackedEventRetention > 0 {
					boundary.HardDuration = a.Hub.Cfg.UnackedEventRetention
					boundary.HardCutoffAt, err = a.classPreviewCutoff(after, impact, boundary.HardDuration)
				}
				if err != nil {
					return err
				}
			}
			bounds = append(bounds, boundary)
			// Only a new shared activation (or a protective value change) can
			// resolve this pending generation, atomically with its boundary.
			if key == "retention.ack_events" {
				bootstrap.AckPending = false
				bootstrap.AckSeconds = seconds
				bootstrap.ApprovedAckSeconds = &seconds
			} else {
				bootstrap.ArchivePending = false
				bootstrap.Archive = seconds != 0
			}
			raw, err := json.Marshal(bounds)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, projectionKey, string(raw)); err != nil {
				return err
			}
		}
	}
	return saveRetentionBootstrap(ctx, tx, bootstrap)
}

// The signed impact carries the youngest enabled class's cutoff. Recover its
// preview instant, then fix each enabled class's own bound at that instant.
// Never let rows age into a previous approval, or invent bounds on upgrade.
func (a *EventRetentionAdapter) classPreviewCutoff(after prefs.Snapshot, impact prefs.Impact, classAge time.Duration) (string, error) {
	cutoff, err := envelope.ParseTime(impact.CutoffAt)
	if err != nil {
		return "", preferenceError(prefs.CodeConfirmationStale)
	}
	policy, err := retentionPolicy(after)
	if err != nil {
		return "", err
	}
	seconds, _ := policy.Ack.Value.Int64()
	age := time.Duration(seconds) * time.Second
	archive, _ := policy.Archive.Value.Bool()
	hard := a.Hub.Cfg.UnackedEventRetention
	if !archive && hard > 0 && (age == 0 || hard < age) {
		age = hard
	}
	return envelope.FormatTime(cutoff.Add(age - classAge)), nil
}

type EventRetentionStatus struct {
	Policy RetentionPolicy `json:"policy"`
	eventlog.RetentionStats
	Owners      int    `json:"owners"`
	ErrorReason string `json:"error_reason,omitempty"`
}

func (h *Hub) EventRetentionStatus() EventRetentionStatus {
	h.retentionMu.Lock()
	defer h.retentionMu.Unlock()
	return h.retentionLast
}

// PruneEventSlice takes a fresh shared preference snapshot under the writer
// lock, applies persisted activation bounds, checks pins and prunes a bounded
// prefix per owner. Subscription wakes cannot invoke this method. The rotating
// owner checkpoint prevents a pinned early peer starving the remaining owners.
func (h *Hub) PruneEventSlice(ctx context.Context) (EventRetentionStatus, error) {
	var result EventRetentionStatus
	limits := h.Cfg.EventRetentionLimits
	if limits.Rows <= 0 {
		limits.Rows = 100
	}
	if limits.Bytes <= 0 {
		limits.Bytes = 1 << 20
	}
	if limits.Target <= 0 {
		limits.Target = 50 * time.Millisecond
	}
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		result = EventRetentionStatus{}
		snapshot, err := h.Preferences.LoadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		policy, err := retentionPolicy(snapshot)
		if err != nil {
			return err
		}
		result.Policy = policy
		ackSeconds, _ := policy.Ack.Value.Int64()
		archive, _ := policy.Archive.Value.Bool()
		hard := h.Cfg.UnackedEventRetention
		if hard < 0 {
			return errors.New("retention: invalid_hard_duration")
		}
		if archive {
			hard = 0
		}
		if ackSeconds == 0 && hard == 0 {
			result.Reason = "disabled"
			return nil
		}
		bounds, err := loadEventBoundaries(ctx, tx)
		if err != nil {
			return err
		}
		bootstrap, err := loadRetentionBootstrap(ctx, tx)
		if err != nil {
			return err
		}
		tag := eventPolicyTag(policy.Ack)
		// Nondefault shorter bootstrap without confirmation is never destructive.
		authorized := len(bounds) == 0 && (ackSeconds == 0 || ackSeconds >= defaultAckSeconds) && !bootstrap.AckPending
		if len(bounds) > 0 {
			authorized = !bootstrap.AckPending && bounds[len(bounds)-1].PolicyTag == tag
		}
		if !authorized {
			result.Reason = "activation_required"
			return nil
		}
		var nb, cutoff sql.NullString
		var high sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT not_before,cutoff_at,high_water FROM hub_preference_activations WHERE key='retention.ack_events' ORDER BY revision DESC LIMIT 1`).Scan(&nb, &cutoff, &high)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		archiveBounds, err := loadEventBoundariesKey(ctx, tx, eventRetentionProjectionKey+"_archive")
		if err != nil {
			return err
		}
		var initialHigh int64
		if err := tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='event_retention_initial_high_water'`).Scan(&initialHigh); err != nil {
			return err
		}
		var archiveNB, archiveCutoff sql.NullString
		var archiveHigh sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT not_before,cutoff_at,high_water FROM hub_preference_activations WHERE key='retention.archive_mode' ORDER BY revision DESC LIMIT 1`).Scan(&archiveNB, &archiveCutoff, &archiveHigh)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := h.Now()
		nowStr := envelope.FormatTime(now)
		rule := func(seq int64, at time.Time) (time.Duration, bool) {
			if ackSeconds == 0 {
				return 0, true
			}
			seconds := ackSeconds
			if seq <= initialHigh {
				seconds = 0
			}
			if len(bounds) > 0 {
				seconds = bounds[0].BeforeSeconds
				if seq <= initialHigh {
					seconds = 0
				}
				for _, b := range bounds {
					// Every boundary's previous rule matters, including an unknown
					// prior projection (zero means retain indefinitely). Earlier
					// protective changes cannot be lost at a later shortening.
					if b.BeforeSeconds == 0 || seconds != 0 && b.BeforeSeconds > seconds {
						seconds = b.BeforeSeconds
					}
					// A protective change applies immediately, including to older rows.
					if b.AfterSeconds == 0 || seconds != 0 && b.AfterSeconds > seconds || seq > b.FromSeq {
						seconds = b.AfterSeconds
					}
				}
			}
			if len(bounds) > 0 && bounds[len(bounds)-1].AckCutoffAt != "" && nb.Valid && cutoff.Valid && high.Valid && nowStr >= nb.String && seq <= high.Int64 && envelope.FormatTime(at) <= bounds[len(bounds)-1].AckCutoffAt {
				seconds = ackSeconds
			}
			return time.Duration(seconds) * time.Second, true
		}
		hardAllowed := func(seq int64, at time.Time) bool {
			if archive {
				return false
			}
			if bootstrap.ArchivePending || bootstrap.HardPending {
				return false
			}
			if len(archiveBounds) == 0 {
				return true
			}
			latest := archiveBounds[len(archiveBounds)-1]
			// Archive exits protect earlier rows unless this exact exit approved them.
			if latest.AfterSeconds == 0 && seq <= latest.FromSeq {
				return latest.HardDuration == hard && latest.BootstrapGeneration == bootstrap.Generation && latest.HardCutoffAt != "" && archiveNB.Valid && archiveHigh.Valid && archiveCutoff.Valid && nowStr >= archiveNB.String && seq <= archiveHigh.Int64 && envelope.FormatTime(at) <= latest.HardCutoffAt
			}
			return true
		}
		var last string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='event_retention_owner_cursor'`).Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT owner FROM events ORDER BY (owner<=?),owner LIMIT ?`, last, limits.Rows)
		if err != nil {
			return err
		}
		var owners []string
		for rows.Next() {
			var owner string
			if err := rows.Scan(&owner); err != nil {
				rows.Close()
				return err
			}
			owners = append(owners, owner)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		start := time.Now()
		for _, owner := range owners {
			if result.Scanned >= limits.Rows || result.Bytes >= limits.Bytes || result.Owners > 0 && time.Since(start) >= limits.Target {
				break
			}
			remaining := limits
			remaining.Rows -= result.Scanned
			remaining.Bytes -= result.Bytes
			remaining.Target = max(time.Nanosecond, limits.Target-time.Since(start))
			stat, err := h.Log.PrunePrefix(ctx, tx, owner, now, rule, hard, hardAllowed, remaining)
			if err != nil {
				return err
			}
			result.Floor = max(result.Floor, stat.Floor)
			result.Owners++
			result.Scanned += stat.Scanned
			result.Deleted += stat.Deleted
			result.Bytes += stat.Bytes
			result.Pinned += stat.Pinned
			result.AckLag = max(result.AckLag, stat.AckLag)
			result.OldestAgeSeconds = max(result.OldestAgeSeconds, stat.OldestAgeSeconds)
			result.Reason = stat.Reason
			last = owner
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('event_retention_owner_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, last)
		return err
	})
	if err != nil {
		result.Deleted = 0
		result.Bytes = 0
		result.ErrorReason = "policy_or_slice_failed"
	}
	h.retentionMu.Lock()
	h.retentionLast = result
	h.retentionMu.Unlock()
	return result, err
}

// Bootstrap changes must not bypass the prefs confirmation service merely by
// restarting. This checkpoint is consumer activation metadata, not a preference
// layer: every value still comes from LoadSnapshot. Only a fresh shared
// activation can supersede a pending generation; matching old tags cannot.
type eventRetentionBootstrap struct {
	ApprovedAckSeconds *int64         `json:"approved_ack_seconds"`
	HardDuration       *time.Duration `json:"hard_duration_ns,omitempty"`
	HardPending        bool           `json:"hard_pending,omitempty"`
	Generation         uint64         `json:"generation"`
	AckFromSeq         int64          `json:"ack_from_seq"`
	ArchiveFromSeq     int64          `json:"archive_from_seq"`
	AckSeconds         int64          `json:"ack_seconds"`
	Archive            bool           `json:"archive"`
	AckPending         bool           `json:"ack_pending"`
	ArchivePending     bool           `json:"archive_pending"`
}

const eventRetentionBootstrapKey = "event_retention_bootstrap_v1"

func loadRetentionBootstrap(ctx context.Context, tx *sql.Tx) (eventRetentionBootstrap, error) {
	var b eventRetentionBootstrap
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, eventRetentionBootstrapKey).Scan(&raw)
	if err != nil {
		return b, err
	}
	if json.Unmarshal([]byte(raw), &b) != nil || b.AckSeconds < 0 {
		return b, errors.New("retention: invalid_bootstrap_checkpoint")
	}
	// Older checkpoints did not retain the approved duration. An unresolved
	// old transition has no provable prior rule, so retain indefinitely.
	if b.ApprovedAckSeconds == nil {
		approved := b.AckSeconds
		if b.AckPending {
			approved = 0
		}
		b.ApprovedAckSeconds = &approved
	}
	if *b.ApprovedAckSeconds < 0 || b.AckFromSeq < 0 || b.ArchiveFromSeq < 0 || b.HardDuration != nil && *b.HardDuration < 0 {
		return b, errors.New("retention: invalid_bootstrap_checkpoint")
	}
	return b, nil
}
func saveRetentionBootstrap(ctx context.Context, tx *sql.Tx, b eventRetentionBootstrap) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, eventRetentionBootstrapKey, string(raw))
	return err
}
func (h *Hub) initializeEventRetention(ctx context.Context) error {
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
		seconds, _ := p.Ack.Value.Int64()
		archive, _ := p.Archive.Value.Bool()
		b, err := loadRetentionBootstrap(ctx, tx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var high int64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0)`).Scan(&high); err != nil {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			approved := seconds
			if seconds > 0 && seconds < defaultAckSeconds {
				b.AckPending = true
				approved = 0
				b.Generation++
				b.AckFromSeq = high
			}
			b.ApprovedAckSeconds = &approved
		} else {
			if seconds != b.AckSeconds {
				approved := *b.ApprovedAckSeconds
				if seconds > 0 && (approved == 0 || seconds < approved) {
					b.AckPending = true
					b.Generation++
					b.AckFromSeq = high
				} else {
					// A longer/disabled bootstrap protects existing rows too.
					// Persist it in the same ordered ledger as runtime activations,
					// so a subsequent prospective Save retains that historical rule.
					bounds, err := loadEventBoundaries(ctx, tx)
					if err != nil {
						return err
					}
					b.Generation++
					bounds = append(bounds, eventRuleBoundary{
						Revision: s.Revision, FromSeq: high,
						BeforeSeconds: approved, AfterSeconds: seconds,
						PolicyTag: eventPolicyTag(p.Ack), BootstrapGeneration: b.Generation,
						BootstrapTransition: true,
					})
					raw, err := json.Marshal(bounds)
					if err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, eventRetentionProjectionKey, string(raw)); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `UPDATE hub_preference_activations SET not_before=NULL,cutoff_at=NULL,high_water=NULL WHERE key='retention.ack_events'`); err != nil {
						return err
					}
					b.AckPending = false
					b.ApprovedAckSeconds = &seconds
				}
			}
			if b.Archive && !archive {
				b.ArchivePending = true
				b.Generation++
				b.ArchiveFromSeq = high
			} else if archive {
				b.ArchivePending = false
			}
		}
		hard := h.Cfg.UnackedEventRetention
		if hard < 0 {
			return errors.New("retention: invalid_hard_duration")
		}
		if b.HardDuration == nil || *b.HardDuration != hard {
			// Every hard config change invalidates previews and old retrospective
			// grants, even a disable/re-enable cycle back to the same duration.
			b.Generation++
			if hard > 0 && (b.HardDuration == nil || *b.HardDuration == 0 || hard < *b.HardDuration) {
				b.HardPending = true
			} else if hard == 0 {
				b.HardPending = false
			}
		}
		b.HardDuration = &hard
		b.AckSeconds, b.Archive = seconds, archive
		generation = b.Generation
		return saveRetentionBootstrap(ctx, tx, b)
	})
	if err != nil {
		return err
	}
	// Startup completes before serving. The shared service binds its ETag
	// and signed previews to this persisted generation, including after a
	// bootstrap cycle returns to an otherwise identical configuration.
	return h.Preferences.bindRetentionGeneration(ctx, generation)
}

// Wakeups refresh observable status only. They are never deletion authority.
func (h *Hub) refreshEventRetentionStatus(ctx context.Context) error {
	s, err := h.Preferences.LoadSnapshot(ctx, nil)
	var p RetentionPolicy
	if err == nil {
		p, err = retentionPolicy(s)
	}
	h.retentionMu.Lock()
	defer h.retentionMu.Unlock()
	if err != nil {
		h.retentionLast.ErrorReason = "policy_read_failed"
		return err
	}
	h.retentionLast.Policy = p
	h.retentionLast.ErrorReason = ""
	return nil
}
