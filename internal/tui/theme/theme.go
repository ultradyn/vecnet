// Package theme is the TUI's one source of colour (docs/design/tui-color.md).
// Screens ask for a semantic Role or Fill and never hold a hex value, a 256
// index or an ANSI number: every one of those lives in this package.
//
// A Theme is built for one colour profile (rung) and one background set:
//
//   - truecolor and 256 use hand-picked values, dark or light;
//   - 16 uses the normal ANSI colours 1–6, faint for quiet roles and reverse
//     video for fills, and leaves dark or light to the terminal's palette;
//   - NoColor emits no colour at all, only bold and reverse.
//
// Nothing here paints the terminal's background: only the fills (selection,
// header bar, chips) set one, and each of them sets its own foreground.
//
// Hostile text (design §6.8): a Role is chosen from the field a string sits in
// or from a hub enum, never from the string's content. Every helper here takes
// an enum or an id, and Style.Render wraps text the caller has already passed
// through tui.Clean.
package theme

import (
	"image/color"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Profile is a colour rung. Its values are colorprofile's, so a Profile goes
// straight to tea.WithColorProfile and the renderer agrees with the theme.
type Profile = colorprofile.Profile

// The four rungs (design §4.2).
const (
	NoColor   Profile = colorprofile.Ascii
	ANSI16    Profile = colorprofile.ANSI
	ANSI256   Profile = colorprofile.ANSI256
	TrueColor Profile = colorprofile.TrueColor
)

// Detect works out the rung once, at start (design §4.1):
//
//  1. NO_COLOR set and non-empty, whatever its value: NoColor.
//  2. COLORTERM is truecolor or 24bit, any case: TrueColor.
//  3. colorprofile's terminal detection, from TERM (and terminfo, and tmux's
//     own report) with COLORTERM and NO_COLOR removed, says 256 or better:
//     ANSI256. COLORTERM=yes or true does not count as truecolor.
//  4. Otherwise ANSI16.
//
// The TUI only ever draws to a terminal, so step 3 treats out as one: only
// TERM decides, and a test can pass a nil writer.
func Detect(env []string, out io.Writer) Profile {
	if v, ok := lookup(env, "NO_COLOR"); ok && v != "" {
		return NoColor
	}
	if v, _ := lookup(env, "COLORTERM"); strings.EqualFold(v, "truecolor") || strings.EqualFold(v, "24bit") {
		return TrueColor
	}
	clean := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "COLORTERM", "NO_COLOR", "TTY_FORCE":
			continue
		}
		clean = append(clean, kv)
	}
	clean = append(clean, "TTY_FORCE=1")
	if colorprofile.Detect(out, clean) >= colorprofile.ANSI256 {
		return ANSI256
	}
	return ANSI16
}

// Background reads VECNET_TUI_BG. forced reports that it named a set, in which
// case the TUI does not ask the terminal. Unset or unknown is dark, not forced.
func Background(env []string) (dark, forced bool) {
	v, _ := lookup(env, "VECNET_TUI_BG")
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "light":
		return false, true
	case "dark":
		return true, true
	}
	return true, false
}

// lookup is the last value of key in env, as os.Getenv would see it.
func lookup(env []string, key string) (string, bool) {
	val, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val, found = v, true
		}
	}
	return val, found
}

// Theme resolves roles for one rung and one set. It is immutable; the TUI
// swaps the whole Theme when the terminal reports its background.
type Theme struct {
	p    Profile
	dark bool
}

// New builds the theme for rung p. dark picks the set for the truecolor and
// 256 rungs; the 16 and NoColor rungs ignore it.
func New(p Profile, dark bool) *Theme {
	switch {
	case p >= TrueColor:
		p = TrueColor
	case p <= NoColor:
		p = NoColor
	}
	return &Theme{p: p, dark: dark}
}

// Profile is the rung this theme draws for.
func (t *Theme) Profile() Profile { return t.p }

// Dark reports the set in use. It is meaningful for truecolor and 256 only.
func (t *Theme) Dark() bool { return t.dark }

// Role is a foreground meaning (design §5.1) or an identity hue (§5.4).
type Role int

// Foreground roles. Text is the terminal's own foreground with no SGR.
const (
	Text Role = iota
	Bold
	Muted
	Dim
	Accent
	AccentBold // the focused pane's title
	Indigo
	Action
	Unread
	Warn
	Success
	Error
	Info
	Hold
	Rule
	Focus
	Ident1 // indigo
	Ident2 // purple
	Ident3 // fuchsia
	Ident4 // cyan
	Ident5 // blue
	numRoles
)

// Roles lists every role, for tests.
func Roles() []Role {
	rs := make([]Role, numRoles)
	for i := range rs {
		rs[i] = Role(i)
	}
	return rs
}

var roleNames = [...]string{"text", "bold", "muted", "dim", "accent", "accent-bold", "indigo", "action", "unread", "warn", "success", "error", "info", "hold", "rule", "focus", "ident1", "ident2", "ident3", "ident4", "ident5"}

func (r Role) String() string {
	if r >= 0 && int(r) < len(roleNames) {
		return roleNames[r]
	}
	return "role?"
}

// tone is one role's value on every rung. An empty hex, a negative index and
// a zero ANSI number mean the terminal's default foreground.
type tone struct {
	tcDark, tcLight   string
	c256Dark, c256Lit int
	ansi              int  // 1–6, or 0 for default
	faint16           bool // the 16 rung draws it faint instead of a colour
	bold              bool
	keep              bool // keeps its hue on the selection and bar fills
}

var tones = [numRoles]tone{
	Text:       {c256Dark: -1, c256Lit: -1},
	Bold:       {c256Dark: -1, c256Lit: -1, bold: true},
	Muted:      {"#9aa4b8", "#5f6a84", 248, 60, 0, true, false, false},
	Dim:        {"#5f6a84", "#8391ad", 60, 103, 0, true, false, false},
	Accent:     {"#a855f7", "#7c3aed", 135, 92, 5, false, false, false},
	AccentBold: {"#a855f7", "#7c3aed", 135, 92, 5, false, true, false},
	Indigo:     {"#818cf8", "#4f46e5", 105, 63, 4, false, false, false},
	Action:     {"#a5b4fc", "#4f46e5", 147, 63, 4, false, false, false},
	Unread:     {"#fbbf24", "#b45309", 214, 130, 3, false, true, true},
	Warn:       {"#fbbf24", "#b45309", 214, 130, 3, false, false, true},
	Success:    {"#34d399", "#047857", 43, 29, 2, false, false, true},
	Error:      {"#fb7185", "#be123c", 204, 161, 1, false, false, true},
	Info:       {"#38bdf8", "#0369a1", 74, 24, 6, false, false, true},
	Hold:       {"#8391ad", "#64748b", 103, 67, 0, true, false, false},
	Rule:       {"#3d4a73", "#b8c0d6", 60, 146, 0, true, false, false},
	Focus:      {"#a855f7", "#7c3aed", 135, 92, 5, false, false, false},
	Ident1:     {"#818cf8", "#4f46e5", 105, 63, 4, false, false, true},
	Ident2:     {"#c084fc", "#9333ea", 141, 92, 5, false, false, true},
	Ident3:     {"#e879f9", "#c026d3", 213, 164, 5, false, false, true},
	Ident4:     {"#22d3ee", "#0e7490", 45, 31, 6, false, false, true},
	Ident5:     {"#60a5fa", "#2563eb", 75, 26, 4, false, false, true},
}

// pick is the colour for one rung and set from its three designed values.
// It never asks lipgloss to approximate (design §4.1).
func (t *Theme) pick(ansi int, dark256, light256 int, dark, light string) color.Color {
	switch t.p {
	case TrueColor:
		hex := dark
		if !t.dark {
			hex = light
		}
		if hex == "" {
			return nil
		}
		return lipgloss.Color(hex)
	case ANSI256:
		n := dark256
		if !t.dark {
			n = light256
		}
		if n < 16 || n > 255 {
			return nil
		}
		return lipgloss.ANSIColor(n)
	case ANSI16:
		if ansi < 1 || ansi > 6 {
			return nil
		}
		return lipgloss.Color(string(rune('0' + ansi)))
	}
	return nil
}

func (t *Theme) fg(r Role) color.Color {
	if r < 0 || r >= numRoles {
		return nil
	}
	v := tones[r]
	return t.pick(v.ansi, v.c256Dark, v.c256Lit, v.tcDark, v.tcLight)
}

// Fg is role r on the terminal's own background.
func (t *Theme) Fg(r Role) lipgloss.Style {
	s := lipgloss.NewStyle()
	if r < 0 || r >= numRoles {
		return s
	}
	v := tones[r]
	if v.bold {
		s = s.Bold(true)
	}
	if t.p == ANSI16 && v.faint16 {
		return s.Faint(true)
	}
	if c := t.fg(r); c != nil {
		s = s.Foreground(c)
	}
	return s
}

// Fill is a background the TUI may paint (design §5.2). Nothing else sets one.
type Fill int

const (
	// Selection is the selected row, filled across the pane's inner width.
	Selection Fill = iota
	// Bar is the header row, across the full width.
	Bar
	// Tab is the active tab's label in the bar.
	Tab
	// Brand is the ` vecnet ` chip, solid; Theme.Brand draws the gradient.
	Brand
	// ModeNormal, ModeInsert and ModeCommand are the status-line chips.
	ModeNormal
	ModeInsert
	ModeCommand
	numFills
)

// Fills lists every fill, for tests.
func Fills() []Fill {
	fs := make([]Fill, numFills)
	for i := range fs {
		fs[i] = Fill(i)
	}
	return fs
}

// fillTone is a fill's background and foreground on each rung. ansi is the
// role colour a 16-colour chip shows in reverse; 0 is plain reverse.
type fillTone struct {
	bgDark, bgLight, fgDark, fgLight         string
	bg256Dark, bg256Lit, fg256Dark, fg256Lit int
	ansi                                     int
	bold                                     bool
	reverse16                                bool // the 16 and NoColor rungs draw it in reverse; false: no fill there
}

var fillTones = [numFills]fillTone{
	Selection:   {"#2c3756", "#c7d2fe", "#f3f4f6", "#111827", 17, 189, 255, 234, 0, false, true},
	Bar:         {"#1e2640", "#e2e8f0", "#f3f4f6", "#111827", 236, 254, 255, 234, 0, false, false},
	Tab:         {"#3d4a73", "#c7d2fe", "#f3f4f6", "#111827", 60, 189, 255, 234, 0, true, true},
	Brand:       {"#6366f1", "#4f46e5", "#ffffff", "#ffffff", 62, 63, 231, 231, 5, true, true},
	ModeNormal:  {"#6366f1", "#4f46e5", "#ffffff", "#ffffff", 62, 63, 231, 231, 4, true, true},
	ModeInsert:  {"#a855f7", "#7c3aed", "#ffffff", "#ffffff", 135, 92, 231, 231, 5, true, true},
	ModeCommand: {"#38bdf8", "#0369a1", "#0b0f19", "#ffffff", 74, 24, 16, 231, 6, true, true},
}

// brandEnd is the gradient's last cell; the first is the Brand fill's bg.
const brandEndDark, brandEndLight = "#a855f7", "#7c3aed"

// Fill is fill f with its own foreground. On the 16 and NoColor rungs a fill
// is reverse video (a chip in its role colour on 16), or nothing for the bar.
// The 16-rung Tab is plain reverse, as the design's masks draw it.
func (t *Theme) Fill(f Fill) lipgloss.Style {
	s := lipgloss.NewStyle()
	if f < 0 || f >= numFills {
		return s
	}
	v := fillTones[f]
	switch t.p {
	case TrueColor, ANSI256:
		if v.bold {
			s = s.Bold(true)
		}
		bg := t.pick(0, v.bg256Dark, v.bg256Lit, v.bgDark, v.bgLight)
		fg := t.pick(0, v.fg256Dark, v.fg256Lit, v.fgDark, v.fgLight)
		return s.Background(bg).Foreground(fg)
	}
	if !v.reverse16 {
		return s
	}
	s = s.Reverse(true)
	if f != Tab && v.bold {
		s = s.Bold(true)
	}
	if t.p == ANSI16 && v.ansi != 0 {
		s = s.Foreground(t.pick(v.ansi, 0, 0, "", ""))
	}
	return s
}

// Sel is role r drawn on the selection fill (design §5.3). Quiet roles take
// the fill's foreground; state and identity hues keep theirs. On the 16 and
// NoColor rungs the row is reverse video with no per-cell colour.
func (t *Theme) Sel(r Role) lipgloss.Style {
	return t.onFill(Selection, r)
}

// On is Sel(r) when sel, else Fg(r): one call for a list row.
func (t *Theme) On(sel bool, r Role) lipgloss.Style {
	if sel {
		return t.Sel(r)
	}
	return t.Fg(r)
}

// OnBar is role r drawn on the header bar. Text and muted take the bar's own
// values; success, warn and error keep their hue. With no bar fill (16 and
// NoColor) it is Fg(r).
func (t *Theme) OnBar(r Role) lipgloss.Style {
	if t.p != TrueColor && t.p != ANSI256 {
		return t.Fg(r)
	}
	if r == Muted || r == Dim {
		return t.Fill(Bar).Foreground(t.pick(0, barMuted256Dark, barMuted256Light, barMutedDark, barMutedLight))
	}
	return t.onFill(Bar, r)
}

// The bar's muted foreground (design §5.2).
const (
	barMutedDark, barMutedLight       = "#9aa4b8", "#5f6a84"
	barMuted256Dark, barMuted256Light = 248, 60
)

func (t *Theme) onFill(f Fill, r Role) lipgloss.Style {
	base := t.Fill(f)
	if r < 0 || r >= numRoles {
		return base
	}
	v := tones[r]
	if v.bold {
		base = base.Bold(true)
	}
	if (t.p == TrueColor || t.p == ANSI256) && v.keep {
		if c := t.fg(r); c != nil {
			base = base.Foreground(c)
		}
	}
	return base
}

// Brand draws label as the brand chip. Truecolor interpolates its background
// cell by cell from the brand's start to its end; other rungs are solid.
func (t *Theme) Brand(label string) string {
	if t.p != TrueColor {
		return t.Fill(Brand).Render(label)
	}
	start, end := fillTones[Brand].bgDark, brandEndDark
	if !t.dark {
		start, end = fillTones[Brand].bgLight, brandEndLight
	}
	a, b := lipgloss.Color(start), lipgloss.Color(end)
	cells := []rune(label)
	base := t.Fill(Brand)
	var out strings.Builder
	for i, r := range cells {
		frac := 0.0
		if len(cells) > 1 {
			frac = float64(i) / float64(len(cells)-1)
		}
		out.WriteString(base.Background(mix(a, b, frac)).Render(string(r)))
	}
	return out.String()
}

// mix is the straight RGB blend of a and b at frac (0 is a, 1 is b).
func mix(a, b color.Color, frac float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	ch := func(x, y uint32) uint8 {
		fx, fy := float64(x>>8), float64(y>>8)
		return uint8(fx + (fy-fx)*frac + 0.5)
	}
	return color.RGBA{R: ch(ar, br), G: ch(ag, bg), B: ch(ab, bb), A: 0xff}
}

// Mode is the status-line chip for a mode name: NORMAL, INSERT or COMMAND.
func (t *Theme) Mode(name string) lipgloss.Style {
	switch name {
	case "INSERT":
		return t.Fill(ModeInsert)
	case "COMMAND":
		return t.Fill(ModeCommand)
	}
	return t.Fill(ModeNormal)
}

// Pane is a pane's border glyphs, border style and title style (design §6.4).
// A focused pane is heavy (┏━┓┃┗┛) in focus with an accent bold title; any
// other is light (┌─┐│└┘) in rule with a muted title. The glyphs are the same
// width, so focus never moves the layout, and heavy stays under NoColor.
func (t *Theme) Pane(focused bool) (border lipgloss.Border, edge, title lipgloss.Style) {
	if focused {
		return lipgloss.ThickBorder(), t.Fg(Focus), t.Fg(AccentBold)
	}
	return lipgloss.NormalBorder(), t.Fg(Rule), t.Fg(Muted)
}
