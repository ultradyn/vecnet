// Package blob is the hub's content-addressed blob store (PROTOCOL §2.7,
// §11): bytes live at <root>/sha256/<aa>/<bb>/<hex>, written through a temp
// file on the same filesystem, fsynced and renamed; metadata, references
// (pins) and per-uploader daily quotas live in SQLite. Read links carry an
// HMAC token scoped to one recipient and one hash (§14.6, token.go).
package blob

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/store"
)

// Sentinel errors. Callers map them onto the wire: ErrTooLarge → 413
// blob_too_large, ErrQuota → 429 upload_quota_exceeded, ErrNotFound → 404
// unknown_blob, ErrPurged → 410 blob_purged.
var (
	ErrTooLarge = errors.New("blob: larger than the per-blob cap")
	ErrQuota    = errors.New("blob: daily upload quota exceeded")
	ErrNotFound = errors.New("blob: not found")
	ErrPurged   = errors.New("blob: purged")
)

const (
	DefaultMaxSize    = 25 << 20  // per-blob cap (§2.7)
	DefaultDailyQuota = 500 << 20 // per-uploader bytes per UTC day (§2.7)
	MinTTL            = time.Hour // smallest uploader TTL (§2.7)
	// UnreferencedGrace is how long an unreferenced upload survives GC (§11).
	UnreferencedGrace = 24 * time.Hour
)

// Meta describes a stored blob.
type Meta struct {
	SHA256    string
	Size      int64
	MediaType string
}

// Store is the blob store. MaxSize and DailyQuota may be lowered after Open
// (tests do); Now is the hub clock.
type Store struct {
	// Transfers is nil for the default disk backend; hub wiring owns installation.
	Transfers *Transfers

	Root       string // <data>/blobs
	DB         *store.DB
	Key        []byte // meta.hub_key, keys read tokens
	MaxSize    int64
	DailyQuota int64
	Now        func() time.Time

	// mu serialises the filesystem halves of Put, GC and Purge so a dedupe
	// can never keep a file that a concurrent GC or Purge is removing.
	mu sync.Mutex
}

// ValidSHA reports whether sha is 64 lowercase hex characters. Every method
// checks it before a hash reaches a path or a query, so a hostile value can
// never traverse the filesystem.
func ValidSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Open prepares root (creating root/tmp and root/sha256, and clearing temp
// files a crash left behind) and loads the token key from meta.hub_key. A
// nil now means time.Now.
func Open(root string, db *store.DB, now func() time.Time) (*Store, error) {
	if db == nil {
		return nil, errors.New("blob: nil store")
	}
	if root == "" {
		return nil, errors.New("blob: empty root")
	}
	if now == nil {
		now = time.Now
	}
	keyHex, err := db.Meta(context.Background(), "hub_key")
	if err != nil {
		return nil, fmt.Errorf("blob: %w", err)
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) < 32 {
		return nil, errors.New("blob: meta.hub_key is not 32+ bytes of hex")
	}
	for _, d := range []string{"tmp", "sha256"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, fmt.Errorf("blob: %w", err)
		}
	}
	// Only this process writes under root/tmp; anything there at startup is
	// an upload a crash interrupted.
	ents, err := os.ReadDir(filepath.Join(root, "tmp"))
	if err != nil {
		return nil, fmt.Errorf("blob: %w", err)
	}
	for _, e := range ents {
		_ = os.RemoveAll(filepath.Join(root, "tmp", e.Name()))
	}
	return &Store{
		Root: root, DB: db, Key: key,
		MaxSize: DefaultMaxSize, DailyQuota: DefaultDailyQuota, Now: now,
	}, nil
}

// path is the final location of a (validated) hash.
func (s *Store) path(sha string) string {
	return filepath.Join(s.Root, "sha256", sha[:2], sha[2:4], sha)
}

// quotaDay is the UTC calendar day that quota is charged against.
func quotaDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// querier is a *sql.Tx or the *sql.DB.
type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func (s *Store) q(tx *sql.Tx) querier {
	if tx != nil {
		return tx
	}
	return s.DB
}

func usedQuota(ctx context.Context, q querier, uploader, day string) (int64, error) {
	var used int64
	err := q.QueryRowContext(ctx, `SELECT coalesce((SELECT sum(bytes) FROM blob_quota WHERE peer_id IN (?1,'durable:spill:'||?1,'durable:source_raw:'||?1) AND day=?2),0)+coalesce((SELECT reserved_bytes+accepted_bytes+abandoned_bytes+rejected_bytes FROM blob_r2_quota WHERE uploader=?1 AND day=?2),0)`, uploader, day).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("blob: read quota: %w", err)
	}
	return used, nil
}

// ctxReader fails a read once ctx is done, so a cancelled request aborts the
// copy even while its body keeps delivering bytes.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Put streams r into a temp file while hashing it, aborting (and removing
// the temp file) past MaxSize or the uploader's remaining quota for the
// current UTC day, then fsyncs and renames it to its content address. A hash
// already stored is not rewritten, but the upload still counts against quota
// and refreshes the blob's unreferenced lifetime. ttl 0 means none; a ttl
// below MinTTL is raised to it. Re-uploading purged bytes is ErrPurged. An
// empty mediaType is application/octet-stream. The temp file is removed on
// every error path.
func (s *Store) Put(ctx context.Context, uploader string, r io.Reader, mediaType string, ttl time.Duration) (Meta, error) {
	if uploader == "" {
		return Meta{}, errors.New("blob: empty uploader")
	}
	if ttl < 0 {
		return Meta{}, errors.New("blob: negative ttl")
	}
	if ttl > 0 && ttl < MinTTL {
		ttl = MinTTL
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	now := s.Now()
	day := quotaDay(now)
	used, err := usedQuota(ctx, s.DB, uploader, day)
	if err != nil {
		return Meta{}, err
	}
	remaining := s.DailyQuota - used
	if remaining <= 0 {
		return Meta{}, ErrQuota
	}
	limit := min(s.MaxSize, remaining)

	f, err := os.CreateTemp(filepath.Join(s.Root, "tmp"), "up-")
	if err != nil {
		return Meta{}, fmt.Errorf("blob: temp file: %w", err)
	}
	tmp := f.Name()
	closed, kept := false, false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if !kept {
			_ = os.Remove(tmp)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(ctxReader{ctx, r}, limit+1))
	if err != nil {
		return Meta{}, fmt.Errorf("blob: read upload: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Meta{}, err
	}
	if n > s.MaxSize {
		return Meta{}, ErrTooLarge
	}
	if n > remaining {
		return Meta{}, ErrQuota
	}
	if err := f.Sync(); err != nil {
		return Meta{}, fmt.Errorf("blob: fsync: %w", err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return Meta{}, fmt.Errorf("blob: close temp: %w", err)
	}
	m := Meta{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, MediaType: mediaType}

	nowS := envelope.FormatTime(now)
	var ttlUntil any // NULL unless a TTL was asked for
	if ttl > 0 {
		ttlUntil = envelope.FormatTime(now.Add(ttl))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		used, err := usedQuota(ctx, tx, uploader, day) // a concurrent upload may have charged since
		if err != nil {
			return err
		}
		if used+n > s.DailyQuota {
			return ErrQuota
		}
		purgedHash, err := r2Tombstone(ctx, tx, m.SHA256)
		if err != nil {
			return err
		}
		if purgedHash {
			return ErrPurged
		}
		var size int64
		var media string
		var purged sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT size, media_type, purged_at FROM blobs WHERE sha256 = ?`, m.SHA256).
			Scan(&size, &media, &purged)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO blobs(sha256, size, media_type, uploader, created_at, ttl_until) VALUES (?, ?, ?, ?, ?, ?)`,
				m.SHA256, m.Size, m.MediaType, uploader, nowS, ttlUntil); err != nil {
				return fmt.Errorf("blob: insert: %w", err)
			}
		case err != nil:
			return fmt.Errorf("blob: lookup: %w", err)
		case purged.Valid:
			return ErrPurged
		default:
			// Dedupe: keep the first upload's media type; restart the
			// unreferenced grace and never shorten an existing lifetime.
			m.MediaType = media
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET created_at = ?1,
				   ttl_until = CASE WHEN ?2 IS NULL OR ttl_until IS NULL THEN NULL ELSE max(ttl_until, ?2) END
				 WHERE sha256 = ?3`,
				nowS, ttlUntil, m.SHA256); err != nil {
				return fmt.Errorf("blob: refresh: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO blob_uploaders(sha256, uploader) VALUES (?, ?)`, m.SHA256, uploader); err != nil {
			return fmt.Errorf("blob: record uploader: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO blob_quota(peer_id, day, bytes) VALUES (?, ?, ?)
			 ON CONFLICT(peer_id, day) DO UPDATE SET bytes = bytes + excluded.bytes`,
			uploader, day, n); err != nil {
			return fmt.Errorf("blob: charge quota: %w", err)
		}
		// Place the bytes before commit: a crash after the rename leaves an
		// orphan file a later Put of the same bytes reuses, never a row
		// without bytes.
		return s.place(tmp, m.SHA256, &kept)
	})
	if err != nil {
		return Meta{}, err
	}
	return m, nil
}

// place moves tmp to sha's final path unless the bytes are already there.
func (s *Store) place(tmp, sha string, kept *bool) error {
	final := s.path(sha)
	if _, err := os.Stat(final); err == nil {
		return nil // deduped: the deferred cleanup removes tmp
	}
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("blob: mkdir: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("blob: rename: %w", err)
	}
	*kept = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // persist the directory entry
		_ = d.Close()
	}
	return nil
}

// Open returns the bytes and metadata of sha. A malformed or unknown hash is
// ErrNotFound; a purged one is ErrPurged.
func (s *Store) Open(ctx context.Context, sha string) (io.ReadSeekCloser, Meta, error) {
	if !ValidSHA(sha) {
		return nil, Meta{}, ErrNotFound
	}
	if f, m, err := s.openDurable(ctx, sha); !errors.Is(err, ErrNotFound) {
		return f, m, err
	}
	m := Meta{SHA256: sha}
	var purged sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT size, media_type, purged_at FROM blobs WHERE sha256 = ?`, sha).
		Scan(&m.Size, &m.MediaType, &purged)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Meta{}, ErrNotFound
	}
	if err != nil {
		return nil, Meta{}, fmt.Errorf("blob: lookup: %w", err)
	}
	if purged.Valid {
		return nil, Meta{}, ErrPurged
	}
	f, err := os.Open(s.path(sha))
	if errors.Is(err, os.ErrNotExist) {
		return nil, Meta{}, ErrNotFound
	}
	if err != nil {
		return nil, Meta{}, fmt.Errorf("blob: open: %w", err)
	}
	return f, m, nil
}

// Exists reports whether sha is stored and not purged. tx may be nil.
func (s *Store) Exists(ctx context.Context, tx *sql.Tx, sha string) (bool, error) {
	if !ValidSHA(sha) {
		return false, nil
	}
	var one int
	err := s.q(tx).QueryRowContext(ctx, `SELECT 1 FROM blobs WHERE sha256 = ? AND purged_at IS NULL`, sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("blob: exists: %w", err)
	}
	return true, nil
}

// Pin records that message msgID references sha (a blob_refs row), which
// exempts it from GC for as long as the reference exists. Idempotent. tx may
// be nil.
func (s *Store) Pin(ctx context.Context, tx *sql.Tx, sha, msgID string) error {
	if !ValidSHA(sha) {
		return ErrNotFound
	}
	if msgID == "" {
		return errors.New("blob: pin without a message id")
	}
	if _, err := s.q(tx).ExecContext(ctx,
		`INSERT INTO blob_refs(sha256, msg_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, sha, msgID); err != nil {
		return fmt.Errorf("blob: pin: %w", err)
	}
	return nil
}

// GC deletes every unreferenced, unpurged blob uploaded more than
// UnreferencedGrace ago or whose TTL has passed: its row, then its file.
// Purge tombstones are kept and also durably schedule disk removal. Retry
// their filesystem deletion on every GC, independently of retained references.
func (s *Store) GC(ctx context.Context) (removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	var shas, purged []string
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT sha256 FROM blobs WHERE purged_at IS NOT NULL`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sha string
			if err := rows.Scan(&sha); err != nil {
				rows.Close()
				return err
			}
			purged = append(purged, sha)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx,
			`DELETE FROM blobs
			 WHERE purged_at IS NULL
			   AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = blobs.sha256)
			   AND (created_at < ? OR (ttl_until IS NOT NULL AND ttl_until < ?))
			 RETURNING sha256`,
			envelope.FormatTime(now.Add(-UnreferencedGrace)), envelope.FormatTime(now))
		if err != nil {
			return fmt.Errorf("blob: gc: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var sha string
			if err := rows.Scan(&sha); err != nil {
				return fmt.Errorf("blob: gc: %w", err)
			}
			shas = append(shas, sha)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM blob_uploaders WHERE NOT EXISTS (SELECT 1 FROM blobs b WHERE b.sha256 = blob_uploaders.sha256)`); err != nil {
			return fmt.Errorf("blob: gc uploaders: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var cleanupErr error
	for _, sha := range append(purged, shas...) {
		if !ValidSHA(sha) {
			continue
		}
		if err := os.Remove(s.path(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Ordinary rows are already deleted; attempt every file even when
			// another removal fails so an unrelated purge cannot orphan them.
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("blob: gc remove: %w", err))
		}
	}
	return len(shas), cleanupErr
}

// Purge removes sha's bytes and leaves a tombstone (purged_at), so later
// reads are ErrPurged. Purging twice is a no-op; an unknown hash is
// ErrNotFound.
func (s *Store) Purge(ctx context.Context, sha string) error {
	if !ValidSHA(sha) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := purgeHash(ctx, s.DB, sha, s.Now(), true); err != nil {
		return err
	}
	if err := os.Remove(s.path(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: purge remove: %w", err)
	}
	return nil
}
