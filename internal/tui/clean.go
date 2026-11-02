package tui

import (
	"strings"
	"unicode/utf8"
)

// Clean makes a peer-supplied string safe for one line of the terminal: every
// control and escape sequence is gone, and line breaks and tabs become spaces.
func Clean(s string) string { return (&cleaner{s: s}).run(false) }

// CleanBody is Clean for multi-line text: it keeps \n and \t.
func CleanBody(s string) string { return (&cleaner{s: s}).run(true) }

// cleaner removes ESC sequences (CSI, OSC, DCS, SOS, PM, APC and the two-byte
// and charset forms) and their 8-bit introducers, other C0 and C1 controls,
// DEL, invalid UTF-8 and bidi controls. The output alphabet holds no control at
// all, so deleting bytes can never assemble a new sequence.
//
// An introducer with no terminator is not a sequence: it is dropped and its
// text stays visible, so a stray ESC ] cannot hide the rest of a message.
type cleaner struct {
	s string
	// noST and noBEL remember that a scan reached the end of s, which keeps a
	// body made of nothing but introducers linear.
	noST, noBEL bool
}

// Byte values of the sequence introducers and terminators.
const (
	bel   = 0x07
	can   = 0x18
	sub   = 0x1a
	esc   = 0x1b
	c1DCS = 0x90
	c1SOS = 0x98
	c1CSI = 0x9b
	c1ST  = 0x9c
	c1OSC = 0x9d
	c1PM  = 0x9e
	c1APC = 0x9f
)

func (c *cleaner) run(body bool) string {
	var b strings.Builder
	b.Grow(len(c.s))
	for i := 0; i < len(c.s); {
		r, n := utf8.DecodeRuneInString(c.s[i:])
		// A raw 0x80–0x9f byte reaches an 8-bit terminal as a C1 control, so an
		// invalid byte in that range counts as the C1 rune it would be.
		invalid := r == utf8.RuneError && n <= 1
		if invalid {
			r = rune(c.s[i])
		}
		next := i + n
		switch {
		case r == esc:
			i = c.skipEsc(next)
			continue
		case r == c1CSI:
			i = skipCSI(c.s, next)
			continue
		case r == c1OSC:
			i = c.skipString(next, true)
			continue
		case r == c1DCS || r == c1SOS || r == c1PM || r == c1APC:
			i = c.skipString(next, false)
			continue
		}
		i = next
		switch {
		case invalid:
		case r == '\n', r == ' ', r == ' ':
			if body {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		case r == '\t':
			if body {
				b.WriteByte('\t')
			} else {
				b.WriteByte(' ')
			}
		case r < 0x20, r >= 0x7f && r <= 0x9f:
		case r >= '‪' && r <= '‮', r >= '⁦' && r <= '⁩':
		default:
			b.WriteString(c.s[i-n : i])
		}
	}
	return b.String()
}

// skipEsc returns where the text resumes after an ESC whose next byte is at i.
func (c *cleaner) skipEsc(i int) int {
	if i >= len(c.s) {
		return i
	}
	switch b := c.s[i]; {
	case b == '[':
		return skipCSI(c.s, i+1)
	case b == ']':
		return c.skipString(i+1, true)
	case b == 'P', b == 'X', b == '^', b == '_':
		return c.skipString(i+1, false)
	case b >= 0x20 && b <= 0x2f: // intermediates, then one final byte
		for i < len(c.s) && c.s[i] >= 0x20 && c.s[i] <= 0x2f {
			i++
		}
		if i < len(c.s) && c.s[i] >= 0x30 && c.s[i] <= 0x7e {
			i++
		}
		return i
	case b >= 0x30 && b <= 0x7e:
		return i + 1
	}
	return i
}

// skipCSI returns where the text resumes after a CSI whose parameters start at
// i. A byte that cannot belong to the sequence ends it and stays in the text.
func skipCSI(s string, i int) int {
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipString returns where the text resumes after a string sequence whose
// payload starts at i. BEL ends only an OSC. ST ends any of them; CAN and SUB
// abort one; an ESC that is not ST aborts one and starts the next sequence. With
// no terminator it returns i, leaving the payload as text.
func (c *cleaner) skipString(i int, osc bool) int {
	if c.noST && (!osc || c.noBEL) {
		return i
	}
	for j := i; j < len(c.s); {
		r, n := utf8.DecodeRuneInString(c.s[j:])
		if r == utf8.RuneError && n <= 1 {
			r = rune(c.s[j])
		}
		switch {
		case r == bel && osc, r == can, r == sub, r == c1ST:
			return j + n
		case r == esc:
			if j+1 < len(c.s) && c.s[j+1] == '\\' {
				return j + 2
			}
			return j
		}
		j += n
	}
	c.noST = true
	if osc {
		c.noBEL = true
	}
	return i
}
