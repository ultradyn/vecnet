//go:build !linux

package cli

import "testing"

// agyNameSelf is a no-op off Linux: agyHookProc reads no process names there
// (it always returns the zero hookProc), so there is nothing to impersonate.
func agyNameSelf(t *testing.T) { t.Helper() }
