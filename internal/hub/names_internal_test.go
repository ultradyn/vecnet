package hub

import "testing"

// Lookalikes share a skeleton with the name they imitate; distinct names do
// not (PROTOCOL §2.12).
func TestNameSkeleton(t *testing.T) {
	same := [][2]string{
		{"alice", "ALICE"}, {"alice", "a l i c e"}, {"alice", "a.l_i-c.e"}, {"alice", "al1ce"}, {"alice", "aIice"},
		{"alice", "аlice"},  // Cyrillic а
		{"alice", "ａｌｉｃｅ"},  // fullwidth, folded by NFKC
		{"alice", "al​ice"}, // zero-width space
		{"max", "rnax"}, {"max", "MAX"}, {"wave", "vvave"}, {"scout", "sc0ut"}, {"operator", "0perat0r"},
		{"bob", "b\u03bfb"}, // Greek omicron
	}
	for _, p := range same {
		if nameSkeleton(p[0]) != nameSkeleton(p[1]) {
			t.Errorf("skeleton(%q) = %q != skeleton(%q) = %q", p[0], nameSkeleton(p[0]), p[1], nameSkeleton(p[1]))
		}
	}
	for _, p := range [][2]string{{"alice", "alice2"}, {"scout", "scott"}, {"web claude", "web claudia"}, {"max", "maxi"}} {
		if nameSkeleton(p[0]) == nameSkeleton(p[1]) {
			t.Errorf("skeleton(%q) == skeleton(%q)", p[0], p[1])
		}
	}
	for _, ok := range []string{"Scout", "web claude", "codex-7", "a.b_c", "ab"} {
		if !agentNameRE.MatchString(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "x", " lead", "trail ", "-dash", "it's", "a$b", "semi;colon", "аlice", "tab\tname", "line\nbreak", "thirty-three-characters-long-name"} {
		if agentNameRE.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
