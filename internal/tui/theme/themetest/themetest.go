// Package themetest checks rendered TUI output against the colour rungs of
// docs/design/tui-color.md §4.2. Tests in internal/tui and internal/tui/theme
// share it.
package themetest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/XertroV/vecnet/internal/tui/theme"
)

var sgrRe = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// SGRs is every SGR parameter list in s, in order. An empty list is a reset.
func SGRs(s string) [][]int {
	var out [][]int
	for _, m := range sgrRe.FindAllStringSubmatch(s, -1) {
		var ps []int
		if m[1] != "" {
			for _, f := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' }) {
				n, err := strconv.Atoi(f)
				if err != nil {
					n = -1
				}
				ps = append(ps, n)
			}
		}
		out = append(out, ps)
	}
	return out
}

// Attr is one decoded SGR parameter: a plain attribute or reset (Code), or a
// colour (Fg/Bg with Mode 5 or 2 for extended, 0 for a basic 30–37 code).
type Attr struct {
	Code    int
	Colour  bool
	Fg      bool
	Mode    int // 0 basic (30–37, 40–47, 90–107), 5 indexed, 2 rgb
	Index   int // basic code or 256 index
	R, G, B int
}

// Decode splits one SGR parameter list into attributes.
func Decode(ps []int) []Attr {
	var out []Attr
	for i := 0; i < len(ps); i++ {
		c := ps[i]
		switch {
		case c == 38 || c == 48:
			a := Attr{Code: c, Colour: true, Fg: c == 38}
			if i+1 < len(ps) {
				a.Mode = ps[i+1]
				switch a.Mode {
				case 5:
					if i+2 < len(ps) {
						a.Index = ps[i+2]
					}
					i += 2
				case 2:
					if i+4 < len(ps) {
						a.R, a.G, a.B = ps[i+2], ps[i+3], ps[i+4]
					}
					i += 4
				default:
					i++
				}
			}
			out = append(out, a)
		case (c >= 30 && c <= 37) || (c >= 90 && c <= 97):
			out = append(out, Attr{Code: c, Colour: true, Fg: true, Index: c})
		case (c >= 40 && c <= 47) || (c >= 100 && c <= 107):
			out = append(out, Attr{Code: c, Colour: true, Index: c})
		default:
			out = append(out, Attr{Code: c})
		}
	}
	return out
}

// Violations lists every SGR in s that rung p may not emit (design §4.2):
//
//   - every rung: no italic (3), underline (4), blink (5, 6) or concealed (8);
//   - NoColor: no colour at all, and no faint;
//   - ANSI16: foregrounds 31–36 only, no background colour (fills are reverse),
//     no bright or extended colours;
//   - ANSI256: indexed colours 16–255 only, never 0–15, no basic or RGB codes;
//   - TrueColor: RGB colours only.
//
// Resets (0, 22, 27, 39, 49 and the empty list) are always allowed.
func Violations(p theme.Profile, s string) []string {
	var bad []string
	for _, ps := range SGRs(s) {
		for _, a := range Decode(ps) {
			if why := violation(p, a); why != "" {
				bad = append(bad, fmt.Sprintf("%v: %s", ps, why))
			}
		}
	}
	return bad
}

func violation(p theme.Profile, a Attr) string {
	if !a.Colour {
		switch a.Code {
		case 3, 4, 5, 6, 8, 9:
			return fmt.Sprintf("attribute %d is not used by any rung", a.Code)
		case 2:
			if p == theme.NoColor {
				return "faint under NO_COLOR"
			}
		}
		return ""
	}
	if a.Code == 39 || a.Code == 49 {
		return ""
	}
	switch p {
	case theme.NoColor:
		return "colour under NO_COLOR"
	case theme.ANSI16:
		if a.Mode != 0 {
			return "extended colour in the 16 rung"
		}
		if !a.Fg {
			return "background colour in the 16 rung"
		}
		if a.Index < 31 || a.Index > 36 {
			return fmt.Sprintf("foreground %d outside 31–36", a.Index)
		}
	case theme.ANSI256:
		if a.Mode != 5 {
			return "non-indexed colour in the 256 rung"
		}
		if a.Index < 16 || a.Index > 255 {
			return fmt.Sprintf("256 index %d outside 16–255", a.Index)
		}
	case theme.TrueColor:
		if a.Mode != 2 {
			return "non-RGB colour in the truecolor rung"
		}
	}
	return ""
}

// Has reports whether s holds an SGR that sets attribute code (1 bold, 2
// faint, 7 reverse) on a run whose printed text starts with prefix.
func Has(s string, code int, prefix string) bool {
	locs := sgrRe.FindAllStringSubmatchIndex(s, -1)
	for i, loc := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if !strings.HasPrefix(s[loc[1]:end], prefix) {
			continue
		}
		var ps []int
		for _, f := range strings.FieldsFunc(s[loc[2]:loc[3]], func(r rune) bool { return r == ';' || r == ':' }) {
			n, _ := strconv.Atoi(f)
			ps = append(ps, n)
		}
		for _, a := range Decode(ps) {
			if !a.Colour && a.Code == code {
				return true
			}
		}
	}
	return false
}
