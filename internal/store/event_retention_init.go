package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// initializeEventRetention completes the unshipped floor migration's owner
// backfill before exposing the database. Every committed slice advances one
// owner, with two indexed successor seeks independent of event count. Reopen
// resumes the checkpoint; a failed slice rolls its floor and checkpoint back.
func (db *DB) initializeEventRetention(ctx context.Context) error {
	for {
		done, err := db.eventRetentionInitSlice(ctx)
		if err != nil {
			return fmt.Errorf("store: retention_initialization_failed: %w", err)
		}
		if done {
			return nil
		}
	}
}

func (db *DB) eventRetentionInitSlice(ctx context.Context) (done bool, err error) {
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		var ready, last string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='event_retention_init_done'`).Scan(&ready); err != nil {
			return err
		}
		if ready == "1" {
			done = true
			return nil
		}
		if ready != "0" {
			return errors.New("invalid_retention_initialization_state")
		}
		if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='event_retention_init_owner'`).Scan(&last); err != nil {
			return err
		}
		var peer, owner string
		err := tx.QueryRowContext(ctx, `SELECT peer_id FROM peers WHERE peer_id>? ORDER BY peer_id LIMIT 1`, last).Scan(&peer)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT owner FROM events WHERE owner>? ORDER BY owner LIMIT 1`, last).Scan(&owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if owner == "" || peer != "" && peer < owner {
			owner = peer
		}
		if owner == "" {
			_, err = tx.ExecContext(ctx, `UPDATE meta SET value='1' WHERE key='event_retention_init_done'`)
			done = err == nil
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO event_retention(owner,purged_through_seq,updated_at)
   VALUES(?,CAST((SELECT value FROM meta WHERE key='event_retention_legacy_floor') AS INTEGER),strftime('%Y-%m-%dT%H:%M:%fZ','now'))
   ON CONFLICT(owner) DO UPDATE SET purged_through_seq=max(purged_through_seq,excluded.purged_through_seq),updated_at=excluded.updated_at`, owner); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key='event_retention_init_owner'`, owner)
		return err
	})
	return done, err
}
