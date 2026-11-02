package tui

import "charm.land/lipgloss/v2"

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
