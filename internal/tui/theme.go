package tui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Dark only, in the operator UI's palette.
var (
	colorBg      = lipgloss.Color("#0b0f19")
	colorBar     = lipgloss.Color("#111827")
	colorText    = lipgloss.Color("#f3f4f6")
	colorMuted   = lipgloss.Color("#6b7280")
	colorBorder  = lipgloss.Color("#374151")
	colorAccent  = lipgloss.Color("#6366f1")
	colorAccent2 = lipgloss.Color("#a855f7")
	colorGood    = lipgloss.Color("#22c55e")
	colorWarn    = lipgloss.Color("#eab308")
	colorBad     = lipgloss.Color("#ef4444")
	// colorWake is the web unread colour (--wake). Red stays errors only.
	colorWake = lipgloss.Color("#fbbf24")
)

// Shared styles. They hold no state, so screens may use them directly.
var (
	styleText     = lipgloss.NewStyle().Foreground(colorText)
	styleMuted    = lipgloss.NewStyle().Foreground(colorMuted)
	styleAccent   = lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	styleGood     = lipgloss.NewStyle().Foreground(colorGood)
	styleWarn     = lipgloss.NewStyle().Foreground(colorWarn)
	styleBad      = lipgloss.NewStyle().Foreground(colorBad)
	styleBrand    = lipgloss.NewStyle().Foreground(colorText).Background(colorAccent).Bold(true)
	styleTabOn    = lipgloss.NewStyle().Foreground(colorText).Background(colorBorder).Bold(true)
	styleModeChip = lipgloss.NewStyle().Foreground(colorText).Background(colorAccent2).Bold(true)
	styleBorder   = lipgloss.NewStyle().Foreground(colorBorder)
	styleFocused  = lipgloss.NewStyle().Foreground(colorAccent)
)

// termProfile is truecolor when COLORTERM is truecolor or 24bit, 256-colour
// when TERM says so, and 16-colour otherwise. A non-empty NO_COLOR drops
// colour and leaves bold, reverse and the glyphs themselves.
func termProfile() colorprofile.Profile {
	if os.Getenv("NO_COLOR") != "" {
		return colorprofile.ASCII
	}
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return colorprofile.TrueColor
	}
	if strings.Contains(os.Getenv("TERM"), "256color") {
		return colorprofile.ANSI256
	}
	return colorprofile.ANSI
}

// fg paints one foreground, downsampled for the terminal. Bold is independent
// of colour so NO_COLOR still shows it. No background is set.
func fg(c color.Color, bold bool) lipgloss.Style {
	s := lipgloss.NewStyle()
	if bold {
		s = s.Bold(true)
	}
	p := termProfile()
	if p < colorprofile.ANSI || c == nil {
		return s
	}
	conv := p.Convert(c)
	if conv == nil {
		return s
	}
	return s.Foreground(conv)
}
