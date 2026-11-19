//go:build !linux

package cli

// Nonqualified platforms return a typed refusal with no partially trusted
// binding: every platform needs its own qualified peer identity and
// lifecycle evidence before registration can exist there.
func validateClaudeRoot(root claudeRoot) error { return errClaudeUnsupported }

func claudeRootAncestry(root claudeRoot) error { return errClaudeUnsupported }

// claudeProcessFenced fails closed: without qualified platform evidence no
// transition that requires fencing may proceed.
func claudeProcessFenced(pid int, start, boot string) bool { return false }

// claudeBootID reports no boot identity here, so the setup path's evidence
// builder returns the same typed unsupported refusal as validateClaudeRoot.
func claudeBootID() string { return "" }

// Without qualified platform evidence no heartbeat can name a live writer
// and no singleton probe can confirm the shared host, so status fails closed.
func claudeHostLive(pid int, start string) bool { return false }

func claudeSingletonHeld(lock string) bool { return false }
