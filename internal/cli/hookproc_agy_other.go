//go:build !linux

package cli

// agyHookProc is unknown outside Linux: there is no /proc walk, so an agy
// binding never matches a process there. A binding is released only by
// `vecnet hook rebind` or keeps serving later root prompts in the same
// conversation.
func agyHookProc() hookProc { return hookProc{} }

// procParent is unknown outside Linux.
func procParent(pid int) int { return 0 }
