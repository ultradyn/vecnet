package blob_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A stalled provider DELETE ends the round at the cleanup budget. Logical
// transitions still commit, the cut job backs off, and untouched jobs stay
// durable for the next round. Fake-only evidence, not provider acceptance.
func TestR2CleanupRoundBudget(t *testing.T) {
	f := newTransfers(t)
	f.cfg.CleanupBudget = 300 * time.Millisecond
	f.restart(t)
	f.reserve(t, "budget-a", 3)
	f.reserve(t, "budget-b", 3)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var entered atomic.Int32
	f.mu.Lock()
	f.deleteWait = func() { entered.Add(1); <-release }
	f.mu.Unlock()
	f.env.Now.Advance(7 * time.Minute)
	done := make(chan error, 1)
	go func() { done <- f.s.Tick(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stalled provider DELETE held the cleanup round past its budget")
	}
	if n := f.number(t, `SELECT count(*) FROM blob_objects WHERE state='abandoned'`); n != 2 {
		t.Fatalf("abandoned=%d, want 2: logical transitions must not wait for DELETE", n)
	}
	if n := f.number(t, `SELECT count(*) FROM blob_delete_jobs`); n != 2 {
		t.Fatalf("delete jobs=%d, want 2", n)
	}
	if n := f.number(t, `SELECT count(*) FROM blob_delete_jobs WHERE attempts=0`); n != 1 {
		t.Fatalf("untouched jobs=%d, want 1 left for a later round", n)
	}
	if n := entered.Load(); n > 1 {
		t.Fatalf("round issued %d DELETEs after its budget", n)
	}
	once.Do(func() { close(release) })
	f.mu.Lock()
	f.deleteWait = nil
	f.mu.Unlock()
	f.env.Now.Advance(time.Hour)
	f.tick(t)
	if n := f.number(t, `SELECT count(*) FROM blob_delete_jobs`); n != 0 {
		t.Fatalf("delete jobs=%d after recovery, want 0", n)
	}
	f.charge(t, 0, 0)
}
