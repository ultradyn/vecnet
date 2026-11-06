package blob

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/store"
)

// Ledger entries contain stable IDs, counters and timestamps only. A transition
// has exactly one entry; download mints in the later hub wrapper use distinct
// issuance IDs and zero bytes, never estimated delivered-byte charges.
func r2Usage(ctx context.Context, tx *sql.Tx, o R2Object, kind string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO blob_usage_events(event_id,blob_id,uploader,day,kind,byte_count,at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, ids.New(ids.Blo), o.BlobID, o.Uploader, quotaDay(o.IssuedAt), kind, o.Size, r2Time(now))
	return err
}
func r2Terminal(ctx context.Context, tx *sql.Tx, o R2Object, state string, now time.Time) error {
	if o.State == "purged" {
		state = "purged"
	}
	res, err := tx.ExecContext(ctx, `UPDATE blob_objects SET state=?,purged_at=CASE WHEN ?='purged' THEN coalesce(purged_at,?) ELSE purged_at END WHERE blob_id=? AND state=?`, state, state, r2Time(now), o.BlobID, o.State)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if o.State == "pending" {
		column := "abandoned_bytes"
		if state == "rejected" {
			column = "rejected_bytes"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE blob_r2_quota SET reserved_bytes=reserved_bytes-?,`+column+`=`+column+`+? WHERE uploader=? AND day=?`, o.Size, o.Size, o.Uploader, quotaDay(o.IssuedAt)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO blob_delete_jobs(object_key,blob_id,not_before,next_attempt) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, o.ObjectKey, o.BlobID, r2Time(o.UploadDeadline.Add(time.Minute)), r2Time(now)); err != nil {
		return err
	}
	return r2Usage(ctx, tx, o, state, now)
}

func (s *Transfers) PurgeHash(ctx context.Context, sha string) error {
	return purgeHash(ctx, s.cfg.DB, sha, s.cfg.Now(), false)
}

// Both purge entry points publish the tombstone, mark every incarnation and
// erase durable bytes under one writer lock. A disk purged_at row is also a
// persistent deletion request consumed by Store.GC, even without Transfers
// installed after rollback. R2 jobs retain their presign safety window.
func purgeHash(ctx context.Context, db *store.DB, sha string, now time.Time, requireKnown bool) error {
	if !ValidSHA(sha) {
		return ErrNotFound
	}
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if requireKnown {
			var found bool
			if err := tx.QueryRowContext(ctx, `SELECT
                EXISTS(SELECT 1 FROM blobs WHERE sha256=?1) OR
                EXISTS(SELECT 1 FROM durable_blob_payloads WHERE sha256=?1) OR
                EXISTS(SELECT 1 FROM blob_objects WHERE sha256=?1) OR
                EXISTS(SELECT 1 FROM blob_purge_tombstones WHERE sha256=?1)`, sha).Scan(&found); err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blob_purge_tombstones(sha256,purged_at,actor) VALUES(?,?,'operator') ON CONFLICT DO NOTHING`, sha, r2Time(now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE blobs SET purged_at=coalesce(purged_at,?) WHERE sha256=?`, r2Time(now), sha); err != nil {
			return err
		}
		if err := eraseDurable(ctx, tx, sha); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT blob_id FROM blob_objects WHERE sha256=?`, sha)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			o, err := r2ReadObject(ctx, tx, `blob_id=?`, id)
			if err != nil {
				return err
			}
			if o.State == "purged" {
				continue
			}
			if err = r2Terminal(ctx, tx, o, "purged", now); err != nil {
				return err
			}
		}
		return nil
	})
}

// Tick makes bounded logical transitions under the writer lock, then performs
// DELETE outside transactions within CleanupBudget. A successful early DELETE
// never frees storage: the persistent job repeats at upload_deadline+60s to
// defeat PUT recreation.
func (s *Transfers) Tick(ctx context.Context) error {
	now := s.cfg.Now()
	err := s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT o.blob_id FROM blob_objects o WHERE
   (o.state='pending' AND julianday(o.upload_deadline)<=julianday(?1)) OR
   (o.state IN ('pending','complete') AND o.expires_at IS NOT NULL AND julianday(o.expires_at)<=julianday(?1)) OR
   (o.state='complete' AND o.retention_class='keep' AND NOT EXISTS(SELECT 1 FROM blob_object_refs r WHERE r.blob_id=o.blob_id) AND (o.first_attach_deadline IS NULL OR julianday(o.first_attach_deadline)<=julianday(?1)))
   ORDER BY o.issued_at,o.blob_id LIMIT 100`, r2Time(now))
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			o, err := r2ReadObject(ctx, tx, `blob_id=?`, id)
			if err != nil {
				return err
			}
			// SQLite's julianday rounds sub-millisecond values. Its indexed
			// candidate scan is advisory; exact Go deadlines decide transitions.
			state := ""
			if o.State == "pending" && !now.Before(o.UploadDeadline) {
				state = "abandoned"
			}
			if o.State == "complete" && o.RetentionClass == "keep" && (o.FirstAttachDeadline == nil || !now.Before(*o.FirstAttachDeadline)) {
				state = "abandoned"
			}
			if o.ExpiresAt != nil && !now.Before(*o.ExpiresAt) {
				state = "expired"
			}
			if state == "" {
				continue
			}
			if o.State == "complete" && o.RetentionClass == "keep" && o.FirstAttachDeadline == nil {
				if err = r2Usage(ctx, tx, o, "last_ref_removed", now); err != nil {
					return err
				}
			}
			if err = r2Terminal(ctx, tx, o, state, now); err != nil {
				return err
			}
		}
		// Recover durable terminal rows without jobs (also safe after a restart).
		_, err = tx.ExecContext(ctx, `INSERT INTO blob_delete_jobs(object_key,blob_id,not_before,next_attempt) SELECT object_key,blob_id,strftime('%Y-%m-%dT%H:%M:%fZ',upload_deadline,'+60 seconds'),? FROM blob_objects WHERE state IN ('abandoned','rejected','expired','purged') AND storage_charge_state<>'released' ON CONFLICT DO NOTHING`, r2Time(now))
		return err
	})
	if err != nil {
		return err
	}
	rows, err := s.cfg.DB.QueryContext(ctx, `SELECT object_key,blob_id FROM blob_delete_jobs WHERE julianday(next_attempt)<=julianday(?) ORDER BY next_attempt,object_key LIMIT 100`, r2Time(now))
	if err != nil {
		return err
	}
	type deleteJob struct{ key, id string }
	var jobs []deleteJob
	for rows.Next() {
		var job deleteJob
		if err = rows.Scan(&job.key, &job.id); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// The DELETE phase has a whole-round budget so a stalled provider cannot
	// hold this round (and the next logical transitions) for 100 x 30s. A job
	// cut by the budget backs off like any failure; later jobs are untouched.
	deleteCtx, cancelDeletes := context.WithTimeout(ctx, s.cfg.CleanupBudget)
	defer cancelDeletes()
	for _, job := range jobs {
		if deleteCtx.Err() != nil {
			break
		}
		key := job.key
		deleteStarted := s.cfg.Now()
		deleteErr := s.cfg.Objects.Delete(deleteCtx, key)
		s.observeOperation(job.id, "DELETE", deleteErr)
		if deleteErr == nil {
			if err = s.checkpoint("after_delete"); err != nil {
				return err
			}
		}
		err = s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
			var id, notBefore string
			var attempts int
			err := tx.QueryRowContext(ctx, `SELECT blob_id,not_before,attempts FROM blob_delete_jobs WHERE object_key=?`, key).Scan(&id, &notBefore, &attempts)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			safe, err := time.Parse(time.RFC3339Nano, notBefore)
			if err != nil {
				return ErrR2Metadata
			}
			current := s.cfg.Now()
			if deleteErr != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				if attempts > 8 {
					attempts = 8
				}
				delay := time.Second * time.Duration(1<<attempts)
				_, err = tx.ExecContext(ctx, `UPDATE blob_delete_jobs SET attempts=attempts+1,next_attempt=?,last_code='blob_unavailable' WHERE object_key=?`, r2Time(current.Add(delay)), key)
				return err
			}
			if deleteStarted.Before(safe) {
				_, err = tx.ExecContext(ctx, `UPDATE blob_delete_jobs SET attempts=attempts+1,next_attempt=?,last_code=NULL WHERE object_key=?`, r2Time(safe), key)
				return err
			}
			o, err := r2ReadObject(ctx, tx, `blob_id=?`, id)
			if err != nil {
				return err
			}
			if o.StorageChargeState != "released" {
				column := "storage_reserved_bytes"
				if o.StorageChargeState == "retained" {
					column = "retained_bytes"
				}
				if _, err = tx.ExecContext(ctx, `UPDATE blob_r2_storage SET `+column+`=`+column+`-? WHERE singleton=1`, o.Size); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `UPDATE blob_objects SET storage_charge_state='released',deletion_confirmed_at=? WHERE blob_id=? AND storage_charge_state<>'released'`, r2Time(current), id); err != nil {
					return err
				}
				if err = r2Usage(ctx, tx, o, "storage_released", current); err != nil {
					return err
				}
				if err = r2Usage(ctx, tx, o, "delete_confirmed", current); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM blob_delete_jobs WHERE object_key=?`, key)
			return err
		})
		if err != nil {
			return err
		}
	}
	return s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
		// No time expiry for durable message bytes. Cascaded reference removal
		// makes them eligible only after the last retained Message disappears.
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM durable_blob_payloads WHERE NOT EXISTS(SELECT 1 FROM durable_blob_refs r WHERE r.sha256=durable_blob_payloads.sha256 AND r.purpose=durable_blob_payloads.purpose)`, nil},
			{`DELETE FROM blob_upload_requests WHERE blob_id IN (SELECT blob_id FROM blob_objects WHERE julianday(issued_at)<julianday(?,'-7 days') )`, []any{r2Time(now)}},
			{`DELETE FROM blob_objects WHERE state IN ('expired','rejected','abandoned','purged') AND storage_charge_state='released' AND deletion_confirmed_at IS NOT NULL AND julianday(issued_at)<julianday(?,'-7 days') AND julianday(upload_deadline)<julianday(?,'-60 seconds') AND NOT EXISTS(SELECT 1 FROM blob_object_refs r WHERE r.blob_id=blob_objects.blob_id) AND NOT EXISTS(SELECT 1 FROM blob_upload_requests r WHERE r.blob_id=blob_objects.blob_id) AND NOT EXISTS(SELECT 1 FROM blob_delete_jobs j WHERE j.blob_id=blob_objects.blob_id)`, []any{r2Time(now), r2Time(now)}},
			{`DELETE FROM blob_r2_quota WHERE day<date(?,'-35 days')`, []any{r2Time(now)}},
			{`DELETE FROM blob_usage_events WHERE julianday(at)<julianday(?,'-90 days')`, []any{r2Time(now)}},
		} {
			if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// DownloadGrantEvent identifies one issuance or denial. Reuse EventID when
// retrying its write; distinct explicit gets always allocate fresh IDs. This
// records zero bytes: direct S3 bodies and capability replays are unobserved.
type DownloadGrantEvent struct {
	EventID        string
	BlobID         string
	CallerIdentity string
	Kind           string
	At             time.Time
}

// RecordDownloadGrant returns true only for a newly recorded event. Conflicting
// retries fail closed; no caller-supplied text, URL, or status enters the ledger.
func (s *Transfers) RecordDownloadGrant(ctx context.Context, event DownloadGrantEvent) (bool, error) {
	if !ids.Is(ids.Blo, event.EventID) || (!ids.Is(ids.Peer, event.CallerIdentity) && !ids.Is(ids.Prn, event.CallerIdentity)) || event.At.IsZero() || (event.Kind != "download_grant_issued" && event.Kind != "download_grant_rate_limited") {
		return false, ErrR2Metadata
	}
	inserted := false
	err := s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
		var same bool
		err := tx.QueryRowContext(ctx, `SELECT blob_id=? AND caller_identity=? AND kind=? AND byte_count=0 AND at=? FROM blob_usage_events WHERE event_id=?`, event.BlobID, event.CallerIdentity, event.Kind, r2Time(event.At), event.EventID).Scan(&same)
		if err == nil {
			if !same {
				return ErrR2Idempotency
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		o, err := r2ReadObject(ctx, tx, `blob_id=?`, event.BlobID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO blob_usage_events(event_id,blob_id,uploader,day,caller_identity,kind,byte_count,at) VALUES(?,?,?,?,?,?,0,?)`, event.EventID, event.BlobID, o.Uploader, quotaDay(event.At), event.CallerIdentity, event.Kind, r2Time(event.At))
		inserted = err == nil
		return err
	})
	return inserted, err
}

// TransferOperation reports an observed metadata request, with no key, URL,
// provider text, credentials, or downloaded bytes. Exporters must deduplicate
// EventID on retry. PUT and GET bodies travel directly to S3 and are unobserved.
type TransferOperation struct {
	EventID string
	BlobID  string
	Method  string
	Status  string
	At      time.Time
}

func (s *Transfers) observeOperation(blobID string, method string, err error) {
	if s.cfg.ObserveOperation == nil {
		return
	}
	status := "ok"
	if err != nil {
		status = "unavailable"
	}
	s.cfg.ObserveOperation(TransferOperation{EventID: ids.New(ids.Blo), BlobID: blobID, Method: method, Status: status, At: s.cfg.Now()})
}

// ProbeObject observes one HEAD without estimating PUTs or downloads. Completion
// and download probes share it, including failures and repeated provider calls.
func (s *Transfers) ProbeObject(ctx context.Context, o R2Object) (ObjectProof, error) {
	proof, err := s.cfg.Objects.Head(ctx, o.ObjectKey)
	s.observeOperation(o.BlobID, "HEAD", err)
	return proof, err
}

// TransferUsage separates requests, verified acceptance, observed occupancy and
// current durable DB content. Completed objects accrue byte-seconds from verified
// completion through confirmed deletion, even after logical expiry/purge. This
// excludes unknown pre-completion PUT time and provider deletion lag, and is not
// exact provider billing. Historical totals cover the ledger's 90-day retention;
// live objects still accrue beyond it. No tenant mapping or download estimate is
// inferred from principals or capability issuance.
type TransferUsage struct {
	RequestedBytes      int64
	VerifiedBytes       int64
	OccupiedByteSeconds float64
	DurableBytes        int64
}

func (s *Transfers) Usage(ctx context.Context) (TransferUsage, error) {
	var out TransferUsage
	now := s.cfg.Now()
	err := s.cfg.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(CASE WHEN kind='reserve' THEN byte_count ELSE 0 END),0),coalesce(sum(CASE WHEN kind='verified' THEN byte_count ELSE 0 END),0) FROM blob_usage_events`).Scan(&out.RequestedBytes, &out.VerifiedBytes); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(length(bytes)),0) FROM durable_blob_payloads`).Scan(&out.DurableBytes); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT v.byte_count,v.at,d.at FROM blob_usage_events v LEFT JOIN blob_usage_events d ON d.blob_id=v.blob_id AND d.kind='delete_confirmed' WHERE v.kind='verified'
 UNION ALL SELECT o.size,o.verified_at,o.deletion_confirmed_at FROM blob_objects o WHERE o.sha256_verified=1 AND NOT EXISTS(SELECT 1 FROM blob_usage_events v WHERE v.blob_id=o.blob_id AND v.kind='verified')`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var size int64
			var start string
			var end sql.NullString
			if err := rows.Scan(&size, &start, &end); err != nil {
				return err
			}
			from, err := time.Parse(time.RFC3339Nano, start)
			if err != nil {
				return ErrR2Metadata
			}
			until := now
			if end.Valid {
				until, err = time.Parse(time.RFC3339Nano, end.String)
				if err != nil {
					return ErrR2Metadata
				}
			}
			if until.After(from) {
				out.OccupiedByteSeconds += float64(size) * until.Sub(from).Seconds()
			}
		}
		return rows.Err()
	})
	return out, err
}
