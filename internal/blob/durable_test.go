package blob_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"

	"github.com/XertroV/vecnet/internal/blob"
)

func TestR2DurableTransactionPurposeAndPurge(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	data := []byte("durable text")
	sha := hexSHA(data)
	rollback := errors.New("rollback")
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := blob.PutDurable(ctx, tx, "spill", sha, "text/markdown", data); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal("rollback retained bytes")
	}
	for _, purpose := range []string{"spill", "source_raw"} {
		if err := s.DB.Tx(ctx, func(tx *sql.Tx) error { return blob.PutDurable(ctx, tx, purpose, sha, "text/markdown", data) }); err != nil {
			t.Fatal(err)
		}
	}
	f, m, err := s.Open(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if err != nil || string(got) != string(data) || m.Size != int64(len(data)) {
		t.Fatal("durable bytes differ")
	}
	f.Close()
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error { return blob.PutDurable(ctx, tx, "attachment", sha, "text/plain", data) }); err == nil {
		t.Fatal("attachment promoted to durable")
	}
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		return blob.PutDurable(ctx, tx, "spill", hexSHA([]byte("other")), "text/plain", data)
	}); err == nil {
		t.Fatal("wrong digest stored")
	}
	if err := s.Purge(ctx, sha); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, blob.ErrPurged) {
		t.Fatal("durable purge bypassed")
	}
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error { return blob.PutDurable(ctx, tx, "spill", sha, "text/plain", data) }); !errors.Is(err, blob.ErrPurged) {
		t.Fatal("purged durable restored")
	}
}
