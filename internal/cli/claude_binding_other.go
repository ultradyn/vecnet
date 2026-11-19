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
