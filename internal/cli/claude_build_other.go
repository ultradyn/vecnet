//go:build !linux

package cli

import "context"

// Platforms without kernel build evidence never attest one: observation is
// always an unqualified build with a stable reason, and no stored identity is
// ever confirmed live. Each platform needs its own attested-executable path
// before any build can qualify there.
func (a *app) observeClaudeBuild(ctx context.Context, pid int, start string, mode claudeBuildMode) (claudeBuildEvidence, error) {
	return claudeBuildEvidence{Reason: claudeBuildReasonUnqualified}, nil
}

func claudeExeIdentityLive(pid int, identity string) bool { return false }
