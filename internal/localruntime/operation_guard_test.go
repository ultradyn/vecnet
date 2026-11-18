package localruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Signal select evaluation without relying on scheduler delays or sleeps.
type guardWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *guardWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// A waiter must not enter preflight while another operation can still replace
// metadata. Canceling it must neither enter nor release the active operation.
func TestWindowsOperationGuardExcludesPreflightAndCancelsWaiter(t *testing.T) {
	var guard operationGuard
	unlock, err := guard.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &guardWaitContext{Context: base, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		release, err := guard.acquire(ctx)
		if release != nil {
			release()
		}
		result <- err
	}()
	select {
	case err := <-result:
		unlock()
		t.Fatalf("contending preflight entered before cancellation: %v", err)
	case <-ctx.waiting:
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		unlock()
		t.Fatalf("contending preflight entered: %v", err)
	}
	// A canceled contender must not release the first owner's guard.
	if release, err := guard.acquire(ctx); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("canceled contender released active operation: %v", err)
	}
	unlock()
	unlock, err = guard.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if release, err := guard.acquire(ctx); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("idle guard accepted canceled operation: %v", err)
	}
}
