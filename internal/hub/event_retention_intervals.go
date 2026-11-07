package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/XertroV/vecnet/internal/prefs"
)

// A lazy binary interval tree implements the historical rule ledger exactly:
// each change protects [0, FromSeq] with max(before, after), and assigns the
// new rule to (FromSeq, MaxInt64]. Zero duration means infinite protection.
// Splitting only the boundary path bounds every activation and candidate lookup
// by the 63-bit sequence width, independently of the number of policy edits.
// Uniform assignments detach old subtrees for bounded incremental collection.
type eventRuleInterval struct {
	id, lo, hi, protection int64
	left, right            sql.NullInt64
}

func ruleProtection(seconds int64) int64 {
	if seconds == 0 {
		return math.MaxInt64
	}
	return seconds
}
func readRuleInterval(ctx context.Context, tx *sql.Tx, id int64) (eventRuleInterval, error) {
	n := eventRuleInterval{id: id}
	err := tx.QueryRowContext(ctx, `SELECT from_seq,through_seq,protection,left_id,right_id FROM event_retention_rule_nodes WHERE id=?`, id).Scan(&n.lo, &n.hi, &n.protection, &n.left, &n.right)
	if err == nil && (n.lo < 0 || n.hi < n.lo || n.protection < 0 || n.left.Valid != n.right.Valid) {
		err = errors.New("retention: invalid_rule_interval")
	}
	return n, err
}
func insertRuleInterval(ctx context.Context, tx *sql.Tx, lo, hi, protection int64) (eventRuleInterval, error) {
	r, err := tx.ExecContext(ctx, `INSERT INTO event_retention_rule_nodes(from_seq,through_seq,protection) VALUES(?,?,?)`, lo, hi, protection)
	if err != nil {
		return eventRuleInterval{}, err
	}
	id, err := r.LastInsertId()
	return eventRuleInterval{id: id, lo: lo, hi: hi, protection: protection}, err
}
func saveRuleInterval(ctx context.Context, tx *sql.Tx, n eventRuleInterval) error {
	_, err := tx.ExecContext(ctx, `UPDATE event_retention_rule_nodes SET protection=?,left_id=?,right_id=? WHERE id=?`, n.protection, n.left, n.right, n.id)
	return err
}
func assignRuleInterval(ctx context.Context, tx *sql.Tx, n *eventRuleInterval, p int64) error {
	if n.left.Valid {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO event_retention_rule_garbage(node_id) VALUES(?),(?)`, n.left.Int64, n.right.Int64); err != nil {
			return err
		}
	}
	n.protection = p
	n.left, n.right = sql.NullInt64{}, sql.NullInt64{}
	return saveRuleInterval(ctx, tx, *n)
}
func protectRuleInterval(ctx context.Context, tx *sql.Tx, n *eventRuleInterval, p int64) error {
	if p <= n.protection {
		return nil
	}
	if p == math.MaxInt64 {
		return assignRuleInterval(ctx, tx, n, p)
	}
	n.protection = p
	return saveRuleInterval(ctx, tx, *n)
}
func updateRuleInterval(ctx context.Context, tx *sql.Tx, n *eventRuleInterval, high, protect, after int64, depth int) error {
	if depth > 63 {
		return errors.New("retention: invalid_rule_interval")
	}
	if n.hi <= high {
		return protectRuleInterval(ctx, tx, n, protect)
	}
	if n.lo > high {
		return assignRuleInterval(ctx, tx, n, after)
	}
	if !n.left.Valid && n.protection >= protect && n.protection == after {
		return nil
	}
	mid := n.lo + (n.hi-n.lo)/2
	var left, right eventRuleInterval
	var err error
	if !n.left.Valid {
		left, err = insertRuleInterval(ctx, tx, n.lo, mid, n.protection)
		if err != nil {
			return err
		}
		right, err = insertRuleInterval(ctx, tx, mid+1, n.hi, n.protection)
		if err != nil {
			return err
		}
	} else {
		left, err = readRuleInterval(ctx, tx, n.left.Int64)
		if err != nil {
			return err
		}
		right, err = readRuleInterval(ctx, tx, n.right.Int64)
		if err != nil {
			return err
		}
		if left.lo != n.lo || left.hi != mid || right.lo != mid+1 || right.hi != n.hi {
			return errors.New("retention: invalid_rule_interval")
		}
		if err = protectRuleInterval(ctx, tx, &left, n.protection); err != nil {
			return err
		}
		if err = protectRuleInterval(ctx, tx, &right, n.protection); err != nil {
			return err
		}
	}
	n.left, n.right = sql.NullInt64{Int64: left.id, Valid: true}, sql.NullInt64{Int64: right.id, Valid: true}
	n.protection = 0
	if err = updateRuleInterval(ctx, tx, &left, high, protect, after, depth+1); err != nil {
		return err
	}
	if err = updateRuleInterval(ctx, tx, &right, high, protect, after, depth+1); err != nil {
		return err
	}
	// Coalesce equal neighbours immediately. Large overwritten subtrees are
	// unreachable before their incremental physical collection completes.
	if !left.left.Valid && !right.left.Valid && left.protection == right.protection {
		return assignRuleInterval(ctx, tx, n, left.protection)
	}
	return saveRuleInterval(ctx, tx, *n)
}
func applyEventRuleBoundary(ctx context.Context, tx *sql.Tx, b eventRuleBoundary) error {
	var root int64
	err := tx.QueryRowContext(ctx, `SELECT node_id FROM event_retention_rule_roots WHERE projection=?`, eventRetentionProjectionKey).Scan(&root)
	var n eventRuleInterval
	if errors.Is(err, sql.ErrNoRows) {
		n, err = insertRuleInterval(ctx, tx, 0, math.MaxInt64, ruleProtection(b.BeforeSeconds))
		if err != nil {
			return err
		}
		var initial int64
		if err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='event_retention_initial_high_water'`).Scan(&initial); err != nil {
			return err
		}
		if err = updateRuleInterval(ctx, tx, &n, initial, math.MaxInt64, ruleProtection(b.BeforeSeconds), 0); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO event_retention_rule_roots(projection,node_id) VALUES(?,?)`, eventRetentionProjectionKey, n.id); err != nil {
			return err
		}
	} else if err == nil {
		n, err = readRuleInterval(ctx, tx, root)
	}
	if err != nil {
		return err
	}
	return updateRuleInterval(ctx, tx, &n, b.FromSeq, max(ruleProtection(b.BeforeSeconds), ruleProtection(b.AfterSeconds)), ruleProtection(b.AfterSeconds), 0)
}
func eventRuleSeconds(ctx context.Context, tx *sql.Tx, seq int64) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT node_id FROM event_retention_rule_roots WHERE projection=?`, eventRetentionProjectionKey).Scan(&id); err != nil {
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
			return 0, errors.New("retention: invalid_rule_interval")
		}
		protection = max(protection, n.protection)
		if !n.left.Valid {
			if protection == math.MaxInt64 {
				return 0, nil
			}
			// Validate the bounded scalar before converting seconds to a duration.
			d, _ := prefs.Lookup("retention.ack_events")
			if err := prefs.Validate(d, prefs.Duration(protection)); err != nil {
				return 0, errors.New("retention: invalid_rule_interval")
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
	return 0, errors.New("retention: invalid_rule_interval")
}
func collectEventRuleIntervals(ctx context.Context, tx *sql.Tx, limit int) error {
	for range limit {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT node_id FROM event_retention_rule_garbage ORDER BY node_id LIMIT 1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		n, err := readRuleInterval(ctx, tx, id)
		if err != nil {
			return err
		}
		if n.left.Valid {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO event_retention_rule_garbage(node_id) VALUES(?),(?)`, n.left.Int64, n.right.Int64); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM event_retention_rule_nodes WHERE id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM event_retention_rule_garbage WHERE node_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// Keep only the latest fixed approval/provenance record. Historical eligibility
// is represented by indexed intervals, never a growing JSON value in a writer.
func saveEventBoundary(ctx context.Context, tx *sql.Tx, key string, b eventRuleBoundary) error {
	if key == eventRetentionProjectionKey {
		if err := applyEventRuleBoundary(ctx, tx, b); err != nil {
			return err
		}
	}
	raw, err := json.Marshal([]eventRuleBoundary{b})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(raw))
	if err != nil {
		return err
	}
	return collectEventRuleIntervals(ctx, tx, 8)
}

// Upgrade old JSON projections before serving. The potentially large read and
// validation happen without a writer lock. Replay commits ONE boundary per
// bounded transaction. Until the final latest-only checkpoint commits, restart
// repeats the upgrade from the intact legacy ledger; no pruning can run here.
// The old blob remains read-only: freeing an arbitrarily large overflow chain
// in one writer transaction would defeat the upgrade bound. Only v2 is live.
func (h *Hub) upgradeEventRuleIntervals(ctx context.Context) error {
	for _, key := range []string{eventRetentionProjectionKey, eventRetentionProjectionKey + "_archive"} {
		var raw string
		err := h.DB.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&raw)
		legacy := errors.Is(err, sql.ErrNoRows)
		if legacy {
			legacyKey := eventRetentionLegacyProjectionKey
			if key != eventRetentionProjectionKey {
				legacyKey += "_archive"
			}
			err = h.DB.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, legacyKey).Scan(&raw)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		bounds, err := decodeEventBoundaries(raw)
		if err != nil {
			return err
		}
		if len(bounds) == 0 {
			continue
		}
		if !legacy {
			if len(bounds) != 1 || len(raw) > 8192 {
				return errors.New("retention: invalid_activation_projection")
			}
			if key == eventRetentionProjectionKey {
				var root int64
				if err := h.DB.QueryRowContext(ctx, `SELECT node_id FROM event_retention_rule_roots WHERE projection=?`, key).Scan(&root); err != nil {
					return errors.New("retention: missing_rule_intervals")
				}
			}
			continue
		}
		if key == eventRetentionProjectionKey {
			if err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO event_retention_rule_garbage(node_id) SELECT node_id FROM event_retention_rule_roots WHERE projection=?`, key); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `DELETE FROM event_retention_rule_roots WHERE projection=?`, key)
				return err
			}); err != nil {
				return err
			}
			for _, b := range bounds {
				if err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
					if err := applyEventRuleBoundary(ctx, tx, b); err != nil {
						return err
					}
					return collectEventRuleIntervals(ctx, tx, 8)
				}); err != nil {
					return err
				}
			}
		}
		latest, _ := json.Marshal(bounds[len(bounds)-1:])
		if err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, key, string(latest))
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// The current projection names its exact shared activation. Looking it up by
// (revision,key) uses the existing primary key, even with arbitrarily much
// unrelated or obsolete policy history. Bootstrap transitions without a shared
// activation have no retrospective authority.
func eventBoundaryRevision(bounds []eventRuleBoundary) uint64 {
	if len(bounds) == 0 {
		return 0
	}
	return bounds[len(bounds)-1].Revision
}
func invalidateEventApproval(ctx context.Context, tx *sql.Tx, key string) error {
	projection := eventRetentionProjectionKey
	if key == "retention.archive_mode" {
		projection += "_archive"
	}
	bounds, err := loadEventBoundariesKey(ctx, tx, projection)
	if err != nil || len(bounds) == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hub_preference_activations SET not_before=NULL,cutoff_at=NULL,high_water=NULL WHERE revision=? AND key=?`, eventBoundaryRevision(bounds), key)
	return err
}
