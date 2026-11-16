package cli

import (
	"context"

	"github.com/XertroV/vecnet/internal/platform"
)

// lockLegacySource serializes on the exact legacy filename, without following
// symlinks or accepting unsafe parent/lock ownership and permissions.
func (a *app) lockLegacySource(path string) (func(), error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return lockLegacySourceWith(ctx, platform.Home(a.getenv), path)
}

// archiveLegacySource retains the safe source parent and lock until the
// verified original bytes have been archived and the parent directory synced.
func (a *app) archiveLegacySource(path string, expected []byte) error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return archiveLegacySourceWith(ctx, platform.Home(a.getenv), path, expected, func(ctx context.Context) error {
		worker := *a
		worker.ctx = ctx
		return worker.verifyLegacyRetired(path)
	})
}
