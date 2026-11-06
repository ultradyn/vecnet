package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/tui/theme"
	"github.com/XertroV/vecnet/internal/tui/theme/themetest"
)

// Colour checks from docs/design/tui-color.md §10.2: every screen, on every
// rung, in both sets.

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

// colourScreen is one screen state to render under a theme.
type colourScreen struct {
	name   string
	render func(th *theme.Theme) string
}

// frameOf renders m at the shot size under th.
func frameOf(m *Model, th *theme.Theme) string {
	m.th = th
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
	return m.View().Content
}

// colourScreens loads every TUI screen once. Each render swaps in a theme and
// draws a fresh frame, so one load serves every rung.
func colourScreens(t *testing.T) []colourScreen {
	t.Helper()
	var out []colourScreen
	add := func(name string, m *Model) {
		out = append(out, colourScreen{name, func(th *theme.Theme) string { return frameOf(m, th) }})
	}

	inbox, _ := inboxLoaded(t, inboxSeed(), newClock())
	add("inbox", inbox)

	chat, _ := chatModel(t, chatSeed(), newClock())
	add("chat", chat)

	dash, _ := openDash(t, seededDash(), newClock())
	add("dash", dash)

	subs, _ := subsLoaded(t, subsFake(subsNine()), newClock())
	add("subs", subs)

	f := seeded()
	f.prefs = prefsSample(t)
	prefsM, _ := prefsOpen(t, f)
	add("prefs", prefsM)

	bad := seeded()
	bad.fail("Preferences", fmt.Errorf("503 Service Unavailable"))
	prefsErr, _ := prefsOpen(t, bad)
	add("prefs-error", prefsErr)

	help, hp, _ := cmdModel(t, newClock())
	hp.press("?")
	add("help", help)

	header, _ := inboxLoaded(t, inboxSeed(), newClock())
	header.Hub = "http://127.0.0.1:8443"
	header.Data.Notices = []envelope.HubNotice{maintenanceNotice()}
	add("header-notice-local", header)

	insert, ip := chatModel(t, chatSeed(), newClock())
	ip.press("i")
	typeText(ip, "Patched on x-tuf too")
	add("insert", insert)

	command, cp, _ := cmdModel(t, newClock())
	cp.press(":")
	typeText(cp, "retry all")
	add("command", command)

	good, _, _ := cmdModel(t, newClock())
	good.Toast("retried 3 dead letters", false)
	add("toast-ok", good)

	failed, _, _ := cmdModel(t, newClock())
	failed.Toast("unknown command: retyr  (:help lists commands)", true)
	add("toast-error", failed)

	quit, _, _ := cmdModel(t, newClock())
	quit.confirmQuit = true
	add("confirm-quit", quit)

	choices := []ProfileChoice{{Name: "home", Hub: "https://hub.example:8443"}, {Name: "dev", Hub: "http://127.0.0.1:8443"}}
	out = append(out,
		colourScreen{"picker", func(th *theme.Theme) string {
			p := newPick(choices)
			p.th = th
			return p.View().Content
		}},
		colourScreen{"picker-confirm", func(th *theme.Theme) string {
			p := newPick(choices)
			p.th = th
			p.cursor, p.confirm = 1, true
			return p.View().Content
		}},
	)
	return out
}

var colourRungs = []theme.Profile{theme.TrueColor, theme.ANSI256, theme.ANSI16, theme.NoColor}

func rungName(p theme.Profile) string {
	return map[theme.Profile]string{theme.TrueColor: "truecolor", theme.ANSI256: "256", theme.ANSI16: "16", theme.NoColor: "none"}[p]
}

// §10.2 tests 3 and 4: no screen emits SGR outside its rung. NO_COLOR has no
// colour at all; 256 never touches indices 0–15; 16 uses foregrounds 31–36
// only and fills by reverse video.
func TestEveryScreenStaysInItsRung(t *testing.T) {
	screens := colourScreens(t)
	for _, p := range colourRungs {
		for _, dark := range []bool{true, false} {
			th := theme.New(p, dark)
			for _, s := range screens {
				raw := s.render(th)
				if bad := themetest.Violations(p, raw); len(bad) > 0 {
					t.Errorf("%s %s dark=%v: %d SGR outside the rung, first: %s", s.name, rungName(p), dark, len(bad), bad[0])
				}
				// Not vacuous: the frame has text, and colour where the rung has it.
				if n := len(strings.Fields(ansiStrip(raw))); n < 8 {
					t.Errorf("%s %s: frame is nearly empty (%d words)", s.name, rungName(p), n)
				}
				if p != theme.NoColor && !strings.Contains(raw, "\x1b[") {
					t.Errorf("%s %s: frame has no SGR at all", s.name, rungName(p))
				}
				// VECNET_COLOUR_FRAMES names a directory for the raw frames; 1
				// means SHOTS_DIR, which `just bb` pulls back.
				if dir := os.Getenv("VECNET_COLOUR_FRAMES"); dir != "" {
					if dir == "1" {
						dir = os.Getenv("SHOTS_DIR")
					}
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					name := fmt.Sprintf("%s-%s-%s.ansi", s.name, rungName(p), map[bool]string{true: "dark", false: "light"}[dark])
					if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

// §10.2 test 3, second half: under NO_COLOR, bold and reverse still carry the
// selected row, the focused pane's title and unread counts (§6.4, §6.5, §6.7).
func TestNoColorKeepsStructure(t *testing.T) {
	th := theme.New(theme.NoColor, true)
	inbox, _ := inboxLoaded(t, inboxSeed(), newClock())
	raw := frameOf(inbox, th)
	plain := ansiStrip(raw)
	if !themetest.Has(raw, 7, ">") || !strings.Contains(plain, "┃>APR") {
		t.Error("the selected inbox row is not reverse video")
	}
	if !themetest.Has(raw, 1, "inbox 12 items") {
		t.Error("the focused pane's title is not bold")
	}
	if !themetest.Has(raw, 1, "2 unread") {
		t.Error("the unread count is not bold")
	}
	if !themetest.Has(raw, 7, " NORMAL ") || !themetest.Has(raw, 1, " NORMAL ") {
		t.Error("the mode chip is not reverse and bold")
	}
	if !themetest.Has(raw, 7, " vecnet ") {
		t.Error("the brand chip is not reverse")
	}
	if !strings.Contains(plain, "┏━ inbox") || !strings.Contains(plain, "┌─ detail") {
		t.Error("focus is not shown by a heavy border")
	}

	chat, _ := chatModel(t, chatSeed(), newClock())
	raw = frameOf(chat, th)
	if !themetest.Has(raw, 7, "hark B196") {
		t.Error("the current thread in the sidebar is not reverse video")
	}
	if !themetest.Has(raw, 1, "<codex-hark>") {
		t.Error("sender names are not bold")
	}
	if !themetest.Has(raw, 7, "07:05") {
		t.Error("the selected chat message is not reverse video")
	}
}

// §10.2 test 5: the frame sets no background and no foreground (C2).
func TestViewSetsNoFrameColour(t *testing.T) {
	for _, s := range []string{"inbox", "chat"} {
		var m *Model
		if s == "inbox" {
			m, _ = inboxLoaded(t, inboxSeed(), newClock())
		} else {
			m, _ = chatModel(t, chatSeed(), newClock())
		}
		v := m.View()
		if v.BackgroundColor != nil || v.ForegroundColor != nil {
			t.Errorf("%s: View sets frame colours bg=%v fg=%v", s, v.BackgroundColor, v.ForegroundColor)
		}
	}
	p := newPick([]ProfileChoice{{Name: "home", Hub: "https://hub.example"}})
	if v := p.View(); v.BackgroundColor != nil || v.ForegroundColor != nil {
		t.Errorf("picker sets frame colours bg=%v fg=%v", v.BackgroundColor, v.ForegroundColor)
	}
}

var sgrAt = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// colourBefore is the colour attributes in effect at byte i of raw: those of
// the last SGR before it, or none after a reset.
func colourBefore(raw string, i int) []themetest.Attr {
	locs := sgrAt.FindAllStringIndex(raw[:i], -1)
	if len(locs) == 0 {
		return nil
	}
	last := raw[locs[len(locs)-1][0]:locs[len(locs)-1][1]]
	var out []themetest.Attr
	for _, ps := range themetest.SGRs(last) {
		for _, a := range themetest.Decode(ps) {
			if a.Colour {
				out = append(out, a)
			}
		}
	}
	return out
}

// §6.8 and §10.2 test 6: a role comes from the field and hub enums, never from
// what a peer wrote.
func TestHostileTextIsNotColouredByContent(t *testing.T) {
	a := chatSeed()
	for i := range a.peers {
		if a.peers[i].Peer.PeerID == chatGrok {
			a.peers[i].Peer.Name = "\x1b[31mERROR"
		}
	}
	thread := a.byID[chatHark]
	thread.messages[1].Content.Text = "dead_letter failed"
	m, _ := chatModel(t, a, newClock())
	errorSGR := theme.New(theme.TrueColor, true).Fg(theme.Error).Render("X")
	errorSGR = errorSGR[:strings.Index(errorSGR, "X")]
	raw := frameOf(m, theme.New(theme.TrueColor, true))
	if strings.Contains(raw, "\x1b[31m") {
		t.Fatal("the peer's own escape reached the frame")
	}
	if !strings.Contains(ansiStrip(raw), "ERROR") {
		t.Fatalf("the hostile name is not shown as text:\n%s", ansiStrip(raw))
	}
	for i := 0; ; {
		j := strings.Index(raw[i:], "ERROR")
		if j < 0 {
			break
		}
		i += j
		if strings.HasSuffix(raw[:i], errorSGR) {
			t.Errorf("a name reading ERROR is drawn in the error role at %d", i)
		}
		for _, a := range colourBefore(raw, i) {
			if a.Mode == 0 && a.Index == 31 {
				t.Errorf("a name reading ERROR is drawn in red at %d", i)
			}
		}
		i += len("ERROR")
	}
	at := strings.Index(raw, "dead_letter failed")
	if at < 0 {
		t.Fatalf("the body is not drawn:\n%s", ansiStrip(raw))
	}
	if c := colourBefore(raw, at); len(c) != 0 {
		t.Errorf("a body reading dead_letter failed is coloured %+v, want plain text", c)
	}
}

// §6.9: no hex value, 256 index or ANSI number outside internal/tui/theme.
func TestNoColourLiteralsOutsideTheme(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	literal := regexp.MustCompile(`lipgloss\.(Color|ANSIColor|RGBColor)\(|"#[0-9a-fA-F]{3}([0-9a-fA-F]{3})?"|\.(Fore|Back)ground\(|colorprofile\.`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(src), "\n") {
			// theme.Background reads VECNET_TUI_BG; it sets no colour.
			if literal.MatchString(strings.ReplaceAll(line, "theme.Background(", "")) {
				t.Errorf("%s:%d sets a colour outside internal/tui/theme: %s", f, n+1, strings.TrimSpace(line))
			}
		}
	}
}

func ansiStrip(s string) string { return sgrAt.ReplaceAllString(s, "") }
