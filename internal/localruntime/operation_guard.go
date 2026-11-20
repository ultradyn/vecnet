package localruntime

import (
	"context"
	"sync"
)

// operationGuard serializes in-process Windows preflight with activation without
// creating a lock file before trust checks. The filesystem lock still excludes
// writers in other processes. A channel permits cancellation while waiting.
type operationGuard struct {
	once  sync.Once
	token chan struct{}
}

func (g *operationGuard) acquire(ctx context.Context) (func(), error) {
	g.once.Do(func() { g.token = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case g.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g.token
			return nil, err
		}
		return func() { <-g.token }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
