package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"time"
)

const DurableSourceMaxSize = 256 << 10 // existing source HTTP ingress body cap

// PutDurable is a trusted hub-message-content write, never a public attachment
// upload. Its caller's message transaction owns both the payload and reference.
// The composite key preserves purpose without promoting R2 objects by hash.
func PutDurable(ctx context.Context, tx *sql.Tx, purpose, sha, media string, data []byte) error {
	if tx == nil || !ValidSHA(sha) || (purpose != "spill" && purpose != "source_raw") || len(media) == 0 || len(media) > 128 {
		return ErrR2Metadata
	}
	if _, _, err := mime.ParseMediaType(media); err != nil {
		return ErrR2Metadata
	}
	if purpose == "spill" && len(data) > 65536 || purpose == "source_raw" && len(data) > DurableSourceMaxSize {
		return ErrTooLarge
	}
	if data == nil {
		data = []byte{}
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		return ErrR2Metadata
	}
	purged, err := r2Tombstone(ctx, tx, sha)
	if err != nil {
		return err
	}
	if purged {
		return ErrPurged
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO durable_blob_payloads(sha256,purpose,bytes,media_type,created_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, sha, purpose, data, media, r2Time(time.Now()))
	return err
}

// PutMessageData adds the separate finite text/source quota in the same
// transaction. Its quota subject cannot collide with an enrolled uploader and
// never consumes the remote attachment daily or retained-storage budgets.
func (s *Store) PutMessageData(ctx context.Context, tx *sql.Tx, uploader, purpose, media string, data []byte) (Meta, error) {
	if tx == nil || uploader == "" || (purpose != "spill" && purpose != "source_raw") {
		return Meta{}, ErrR2Metadata
	}
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	subject := "durable:" + purpose + ":" + uploader
	day := quotaDay(s.Now())

	if purpose == "spill" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_blob_payloads WHERE sha256=? AND purpose='spill'`, sha).Scan(&exists); err != nil {
			return Meta{}, err
		}
		if exists > 0 {
			purged, err := r2Tombstone(ctx, tx, sha)
			if err != nil {
				return Meta{}, err
			}
			if purged {
				return Meta{}, ErrPurged
			}
			return Meta{SHA256: sha, Size: int64(len(data)), MediaType: media}, nil
		}
	}
	// Preserve the legacy hub/source daily allowance across cutover, while
	// keeping new remote attachment reservations out of this message budget.
	var used int64
	err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(bytes),0) FROM blob_quota WHERE peer_id IN (?,?) AND day=?`, uploader, subject, day).Scan(&used)
	if err != nil {
		return Meta{}, err
	}
	if int64(len(data)) > s.DailyQuota-used {
		return Meta{}, ErrQuota
	}
	if err = PutDurable(ctx, tx, purpose, sha, media, data); err != nil {
		return Meta{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO blob_quota(peer_id,day,bytes) VALUES(?,?,?) ON CONFLICT(peer_id,day) DO UPDATE SET bytes=bytes+excluded.bytes`, subject, day, len(data)); err != nil {
		return Meta{}, err
	}
	return Meta{SHA256: sha, Size: int64(len(data)), MediaType: media}, nil
}
func PinDurable(ctx context.Context, tx *sql.Tx, purpose, sha, msgID string) error {
	if tx == nil || (purpose != "spill" && purpose != "source_raw") || !ValidSHA(sha) || msgID == "" {
		return ErrR2Metadata
	}
	purged, err := r2Tombstone(ctx, tx, sha)
	if err != nil {
		return err
	}
	if purged {
		return ErrPurged
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO durable_blob_refs(msg_id,sha256,purpose) VALUES(?,?,?) ON CONFLICT DO NOTHING`, msgID, sha, purpose)
	return err
}

type durableReader struct{ *bytes.Reader }

func (durableReader) Close() error { return nil }
func (s *Store) openDurable(ctx context.Context, sha string) (io.ReadSeekCloser, Meta, error) {
	p, err := r2Tombstone(ctx, s.DB, sha)
	if err != nil {
		return nil, Meta{}, err
	}
	if p {
		return nil, Meta{}, ErrPurged
	}
	var data []byte
	var media string
	err = s.DB.QueryRowContext(ctx, `SELECT bytes,media_type FROM durable_blob_payloads WHERE sha256=? ORDER BY purpose LIMIT 1`, sha).Scan(&data, &media)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Meta{}, ErrNotFound
	}
	if err != nil {
		return nil, Meta{}, err
	}
	return durableReader{bytes.NewReader(data)}, Meta{SHA256: sha, Size: int64(len(data)), MediaType: media}, nil
}

// Keep the FK target and retained-message references for access-before-state
// checks. The permanent hash tombstone prevents reads or rewrites of this stub;
// ordinary last-reference GC removes it when its messages are gone.
func eraseDurable(ctx context.Context, tx *sql.Tx, sha string) error {
	_, err := tx.ExecContext(ctx, `UPDATE durable_blob_payloads SET bytes=x'' WHERE sha256=?`, sha)
	return err
}
