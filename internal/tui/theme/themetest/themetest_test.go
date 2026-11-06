package themetest

import (
	"testing"

	"github.com/XertroV/vecnet/internal/tui/theme"
)

// The checker must catch what each rung forbids, or every test that leans on
// it passes vacuously.
func TestViolations(t *testing.T) {
	cases := []struct {
		p   theme.Profile
		s   string
		bad bool
	}{
		{theme.NoColor, "\x1b[1;7mX\x1b[m", false},
		{theme.NoColor, "\x1b[31mX\x1b[m", true},
		{theme.NoColor, "\x1b[38;5;200mX\x1b[m", true},
		{theme.NoColor, "\x1b[48;2;1;2;3mX\x1b[m", true},
		{theme.NoColor, "\x1b[2mX\x1b[m", true},
		{theme.NoColor, "\x1b[94mX\x1b[m", true},
		{theme.ANSI16, "\x1b[1;35mX\x1b[m", false},
		{theme.ANSI16, "\x1b[2mX\x1b[m", false},
		{theme.ANSI16, "\x1b[37mX\x1b[m", true},
		{theme.ANSI16, "\x1b[30mX\x1b[m", true},
		{theme.ANSI16, "\x1b[41mX\x1b[m", true},
		{theme.ANSI16, "\x1b[91mX\x1b[m", true},
		{theme.ANSI16, "\x1b[38;5;100mX\x1b[m", true},
		{theme.ANSI256, "\x1b[38;5;16;48;5;255mX\x1b[m", false},
		{theme.ANSI256, "\x1b[38;5;9mX\x1b[m", true},
		{theme.ANSI256, "\x1b[48;5;0mX\x1b[m", true},
		{theme.ANSI256, "\x1b[31mX\x1b[m", true},
		{theme.TrueColor, "\x1b[38;2;1;2;3mX\x1b[m", false},
		{theme.TrueColor, "\x1b[3mX\x1b[m", true},
		{theme.TrueColor, "\x1b[4mX\x1b[m", true},
	}
	for _, tc := range cases {
		if got := len(Violations(tc.p, tc.s)) > 0; got != tc.bad {
			t.Errorf("Violations(%d, %q) bad=%v, want %v", tc.p, tc.s, got, tc.bad)
		}
	}
	if !Has("a\x1b[1;35mhead\x1b[m", 1, "head") || Has("\x1b[35mhead\x1b[m", 1, "head") {
		t.Error("Has does not find bold on the run it precedes")
	}
}
