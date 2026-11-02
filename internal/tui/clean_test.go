package tui

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClean pins Review Focus 1: whatever a peer sends, the terminal only ever
// receives inert text.
func TestClean(t *testing.T) {
	cases := []struct{ name, in, want, wantBody string }{
		{"osc52 clipboard write bel", "a\x1b]52;c;aGVsbG8=\x07b", "ab", "ab"},
		{"osc52 clipboard write st", "a\x1b]52;c;aGVsbG8=\x1b\\b", "ab", "ab"},
		{"osc8 link", "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", "click", "click"},
		{"osc title", "\x1b]0;pwned\x07ok", "ok", "ok"},
		{"csi clear screen", "a\x1b[2Jb", "ab", "ab"},
		{"csi cursor move", "\x1b[10;20Hx", "x", "x"},
		{"csi sgr", "\x1b[31;1mred\x1b[0m", "red", "red"},
		{"csi private mode", "\x1b[?1049lx", "x", "x"},
		{"dcs", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab", "ab"},
		{"apc", "a\x1b_Gf=100;AAAA\x1b\\b", "ab", "ab"},
		{"pm", "a\x1b^secret\x1b\\b", "ab", "ab"},
		{"sos", "a\x1bXsecret\x1b\\b", "ab", "ab"},
		{"two byte reset", "a\x1bcb", "ab", "ab"},
		{"charset designate", "a\x1b(0b", "ab", "ab"},
		{"lone esc at end", "a\x1b", "a", "a"},
		{"8-bit csi raw byte", "a\x9b2Jb", "ab", "ab"},
		{"8-bit csi encoded", "a\u009b2Jb", "ab", "ab"},
		{"8-bit osc raw byte", "a\x9d52;c;aGk=\x9cb", "ab", "ab"},
		{"8-bit osc encoded", "a\u009d52;c;aGk=\u009cb", "ab", "ab"},
		{"8-bit dcs encoded", "a\u0090q\u009cb", "ab", "ab"},
		{"c1 next line", "a\u0085b", "ab", "ab"},
		{"bel", "a\x07b", "ab", "ab"},
		{"bs", "a\x08b", "ab", "ab"},
		{"cr", "safe\rrm -rf", "saferm -rf", "saferm -rf"},
		{"del", "a\x7fb", "ab", "ab"},
		{"nul", "a\x00b", "ab", "ab"},
		{"vt ff", "a\x0b\x0cb", "ab", "ab"},
		{"rlo u202e", "a\u202egnp.exe", "agnp.exe", "agnp.exe"},
		{"lre u202a", "a\u202ab", "ab", "ab"},
		{"lri u2066", "a\u2066b\u2069c", "abc", "abc"},
		{"pdi range", "\u2067\u2068x", "x", "x"},
		{"invalid utf-8", "a\xff\xfeb\xc3", "ab", "ab"},
		{"overlong encoding", "a\xc0\xafb", "ab", "ab"},
		{"newline", "a\nb", "a b", "a\nb"},
		{"crlf", "a\r\nb", "a b", "a\nb"},
		{"tab", "a\tb", "a b", "a\tb"},
		{"plain", "plain text, with: punctuation!", "plain text, with: punctuation!", "plain text, with: punctuation!"},
		{"cjk", "日本語のメッセージ", "日本語のメッセージ", "日本語のメッセージ"},
		{"accents and emoji", "café 🚀 ✓", "café 🚀 ✓", "café 🚀 ✓"},
		{"replacement char kept", "a\ufffdb", "a\ufffdb", "a\ufffdb"},
		{"continuation bytes are not st", "✜ok", "✜ok", "✜ok"},
		// An unterminated string is not a sequence: keep the text visible, so a
		// stray introducer cannot hide the rest of what a peer said.
		{"unterminated osc keeps text", "ok\x1b]and drop prod", "okand drop prod", "okand drop prod"},
		{"esc aborts osc", "a\x1b]0;title\x1b[2Jb", "ab", "ab"},
		{"can aborts osc", "a\x1b]0;title\x18b", "ab", "ab"},
		{"osc payload with cjk", "a\x1b]0;日本\x07b", "ab", "ab"},
		{"nested csi after strip", "\x1b\x1b[2J[2J", "[2J", "[2J"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clean(c.in); got != c.want {
				t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
			}
			if got := CleanBody(c.in); got != c.wantBody {
				t.Errorf("CleanBody(%q) = %q, want %q", c.in, got, c.wantBody)
			}
		})
	}
}

// inert reports the first byte or rune the terminal could act on.
func inert(t *testing.T, label, in, s string, body bool) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("%s(%q) produced invalid UTF-8: %q", label, in, s)
	}
	for _, r := range s {
		bad := r < 0x20 || (r >= 0x7f && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
		if body && (r == '\n' || r == '\t') {
			bad = false
		}
		if bad {
			t.Fatalf("%s(%q) kept %U in %q", label, in, r, s)
		}
	}
}

func TestCleanOutputAlwaysInert(t *testing.T) {
	alphabet := []string{"\x1b", "[", "]", "P", "_", "^", "X", "\x07", "\\", "\x9b", "\x9d", "\x9c", "\u009b", "\u009d", "\u009c", "\u202e", "\u2066", "\xff", "\xc3", "0", ";", "m", "a", "\n", "\t", "\r", "\x18", "日"}
	rng := rand.New(rand.NewPCG(17, 42))
	for range 20000 {
		var b strings.Builder
		for range rng.IntN(24) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		in := b.String()
		once := Clean(in)
		inert(t, "Clean", in, once, false)
		if strings.Contains(once, "\n") {
			t.Fatalf("Clean(%q) is not a single line: %q", in, once)
		}
		if again := Clean(once); again != once {
			t.Fatalf("Clean not idempotent on %q: %q then %q", in, once, again)
		}
		inert(t, "CleanBody", in, CleanBody(in), true)
	}
}

// Linear time: a hostile body of unterminated introducers must not rescan the
// rest of the string for every one of them.
func TestCleanHostileLengthIsLinear(t *testing.T) {
	for _, unit := range []string{"\x9d", "\x90", "\x1b]", "\u009d", "\x9b", "\x1b["} {
		if got := Clean(strings.Repeat(unit, 1<<21)); len(got) > 1<<23 {
			t.Fatalf("unit %q grew to %d bytes", unit, len(got))
		}
	}
}
