//go:build !linux

package cli

import "context"

// Other platforms keep the existing hooks path; never guess at ownership.
func (a *app) ensureCodexDeliveryPlatform(context.Context, string, string, string, codexDeliveryRelease) error {
	return errCodexDelivery
}
func (a *app) cmdCodexDeliveryPlatform([]string) error { return errCodexDelivery }
