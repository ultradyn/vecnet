package tui

import "unicode/utf8"

// strayByte reports b in s outside a valid UTF-8 sequence: a raw C1 control
// byte, as opposed to the same byte inside a multi-byte glyph such as ┛.
func strayByte(s string, b byte) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 && s[i] == b {
			return true
		}
		i += size
	}
	return false
}
