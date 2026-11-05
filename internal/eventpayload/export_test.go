package eventpayload

import "context"

// The observer is deliberately test-only API. Production callers use the same
// hydration path without exposing diagnostics or mutable package-global hooks.
func HydrateBatchWithBodyObserverForTest(ctx context.Context, q Queryer, rows []StoredPayload, observe func(int)) ([][]byte, error) {
	return hydrateBatch(ctx, q, rows, observe)
}
