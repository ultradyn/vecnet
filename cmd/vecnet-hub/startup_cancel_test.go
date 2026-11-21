package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/testhub"
	"modernc.org/sqlite"
)

func TestServeStartupCancellation(t *testing.T) {
	// A stop during store open is first observed when the notice transaction
	// begins. Pre-cancel to force that ordering without a slow disk or sleeps.
	for _, mode := range []struct {
		name   string
		target string
	}{
		{name: "ordinary"},
		{name: "relocated", target: "https://next.example"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := testhub.FreshStore(t, filepath.Join(dir, "hub.db")).Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var out bytes.Buffer
			env := func(key string) string {
				if key == "VECNET_RELOCATED_TO" {
					return mode.target
				}
				return ""
			}
			if err := run(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dir}, &out, env); err != nil {
				t.Errorf("hub stop: %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("canceled startup announced readiness: %q", out.String())
			}
		})
	}

	t.Run("startup deadline", func(t *testing.T) {
		dir := t.TempDir()
		if err := testhub.FreshStore(t, filepath.Join(dir, "hub.db")).Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithDeadline(context.Background(), time.Time{})
		defer cancel()
		var out bytes.Buffer
		err := run(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dir}, &out, noEnv)
		if !errors.Is(err, context.DeadlineExceeded) || out.Len() != 0 {
			t.Fatalf("expected startup deadline before readiness, got %v, stdout %q", err, out.String())
		}
	})

	t.Run("corrupt store during stop", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "hub.db"), []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var out bytes.Buffer
		err := run(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dir}, &out, noEnv)
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || out.Len() != 0 {
			t.Fatalf("expected corrupt store error despite cancellation, got %v, stdout %q", err, out.String())
		}
	})

	t.Run("notice store failure", func(t *testing.T) {
		dir := t.TempDir()
		db := testhub.FreshStore(t, filepath.Join(dir, "hub.db"))
		if _, err := db.Exec(`DROP TABLE hub_notices`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var out bytes.Buffer
		err := run(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dir, "--relocated-to", "https://next.example"}, &out, noEnv)
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || out.Len() != 0 {
			t.Fatalf("expected startup SQLite failure before readiness, got %v, stdout %q", err, out.String())
		}
	})
}
