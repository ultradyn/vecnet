package blob_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/blob"
)

func TestR2PurgeHashSchedulesDiskCleanupWithoutStorePurge(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "retry-after-restart"}[retry], func(t *testing.T) {
			f := newTransfers(t)
			ctx := context.Background()
			s := f.env.Hub.Blobs
			meta, err := s.Put(ctx, f.env.OperatorPeer, strings.NewReader("disk"), "text/plain", 0)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Root, "sha256", meta.SHA256[:2], meta.SHA256[2:4], meta.SHA256)
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := f.s.PurgeHash(ctx, meta.SHA256); err != nil {
					t.Fatal(err)
				}
			}
			if retry {
				ordinary, err := s.Put(ctx, f.env.OperatorPeer, strings.NewReader("ordinary expired upload"), "text/plain", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				ordinaryPath := filepath.Join(s.Root, "sha256", ordinary.SHA256[:2], ordinary.SHA256[2:4], ordinary.SHA256)
				if _, err := os.Stat(ordinaryPath); err != nil {
					t.Fatal(err)
				}
				f.env.Now.Advance(time.Hour + time.Second)
				// A nonempty directory reliably fails removal even as root.
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "busy"), []byte("disk"), 0600); err != nil {
					t.Fatal(err)
				}
				removed, err := s.GC(ctx)
				if err == nil {
					t.Error("cleanup failure was not reported")
				}
				if removed != 1 {
					t.Errorf("collected %d ordinary uploads, want 1", removed)
				}
				if _, err := os.Stat(ordinaryPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("ordinary expired file remains after purged-path cleanup failure: %v", err)
				}
				for _, table := range []string{"blobs", "blob_uploaders"} {
					if got := f.number(t, "SELECT count(*) FROM "+table+" WHERE sha256=?", ordinary.SHA256); got != 0 {
						t.Errorf("ordinary expired upload still has %s metadata", table)
					}
				}
				if err := os.Remove(filepath.Join(path, "busy")); err != nil {
					t.Fatal(err)
				}
				s, err = blob.Open(s.Root, s.DB, s.Now)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.GC(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("disk bytes remain after standalone PurgeHash and GC: %v", err)
			}
			if _, _, err := s.Open(ctx, meta.SHA256); !errors.Is(err, blob.ErrPurged) {
				t.Fatal("disk purge tombstone lost")
			}
			if _, err := s.GC(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
