package localruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRepairRecordsPendingBeforeLivePathChanges(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Linux managed paths")
	}
	home := t.TempDir()
	if e := os.Chmod(home, 0700); e != nil {
		t.Fatal(e)
	}
	release := filepath.Join(root(home), "managed-release")
	if e := os.MkdirAll(release, 0700); e != nil {
		t.Fatal(e)
	}
	old := filepath.Join(release, "vecnet")
	if e := os.WriteFile(old, []byte("old-running-code"), 0700); e != nil {
		t.Fatal(e)
	}
	interrupted := errors.New("interrupted after durable intent")
	_, e := stage(context.Background(), home, release, map[string][]byte{"vecnet": []byte("restored-code")}, true, func() error {
		b, e := os.ReadFile(old)
		if e != nil || string(b) != "old-running-code" {
			t.Fatal("live release moved before pending intent")
		}
		if e = atomicWrite(filepath.Join(root(home), pendingName), []byte("pending-sentinel"), 0600); e != nil {
			t.Fatal(e)
		}
		return interrupted
	})
	if !errors.Is(e, interrupted) {
		t.Fatalf("stage did not stop at durable intent: %v", e)
	}
	if b, e := os.ReadFile(old); e != nil || string(b) != "old-running-code" {
		t.Fatal("interrupted intent replaced live release")
	}
	if _, e := os.Stat(filepath.Join(root(home), pendingName)); e != nil {
		t.Fatal("restart intent not durable")
	}
}
