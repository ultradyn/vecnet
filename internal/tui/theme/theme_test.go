package theme_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/tui/theme"
	"github.com/XertroV/vecnet/internal/tui/theme/themetest"
)

var rungs = []theme.Profile{theme.TrueColor, theme.ANSI256, theme.ANSI16, theme.NoColor}

func rungName(p theme.Profile) string {
	switch p {
	case theme.TrueColor:
		return "truecolor"
	case theme.ANSI256:
		return "256"
	case theme.ANSI16:
		return "16"
	}
	return "none"
}

// Design §4.1 and §10.2 test 1.
func TestDetect(t *testing.T) {
	const tc, c256, c16, none = theme.TrueColor, theme.ANSI256, theme.ANSI16, theme.NoColor
	cases := []struct {
		name string
		env  []string
		want theme.Profile
	}{
		{"NO_COLOR=1", []string{"NO_COLOR=1", "COLORTERM=truecolor", "TERM=xterm-256color"}, none},
		{"NO_COLOR=yes", []string{"NO_COLOR=yes", "COLORTERM=truecolor", "TERM=xterm-256color"}, none},
		{"NO_COLOR=0", []string{"NO_COLOR=0", "COLORTERM=truecolor", "TERM=xterm-256color"}, none},
		{"NO_COLOR empty keeps colour", []string{"NO_COLOR=", "COLORTERM=truecolor", "TERM=xterm-256color"}, tc},
		{"COLORTERM=truecolor", []string{"COLORTERM=truecolor", "TERM=xterm"}, tc},
		{"COLORTERM=24bit", []string{"COLORTERM=24bit", "TERM=xterm"}, tc},
		{"COLORTERM=TrueColor", []string{"COLORTERM=TrueColor", "TERM=xterm"}, tc},
		{"COLORTERM=yes is not truecolor", []string{"COLORTERM=yes", "TERM=xterm-256color"}, c256},
		{"COLORTERM=true is not truecolor", []string{"COLORTERM=true", "TERM=xterm-256color"}, c256},
		{"COLORTERM=yes on a 16 terminal", []string{"COLORTERM=yes", "TERM=xterm"}, c16},
		{"COLORTERM unset, xterm-256color", []string{"TERM=xterm-256color"}, c256},
		{"xterm", []string{"TERM=xterm"}, c16},
		// colorprofile reads screen as a 256-colour terminal.
		{"screen", []string{"TERM=screen"}, c256},
		{"dumb", []string{"TERM=dumb"}, c16},
		{"TERM unset", nil, c16},
		{"a truecolor TERM without COLORTERM is capped at 256", []string{"TERM=xterm-kitty"}, c256},
		{"last value wins", []string{"NO_COLOR=1", "NO_COLOR="}, c16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := theme.Detect(tc.env, nil); got != tc.want {
				t.Errorf("Detect(%q) = %s, want %s", tc.env, rungName(got), rungName(tc.want))
			}
		})
	}
}

func TestBackground(t *testing.T) {
	for _, tc := range []struct {
		v            string
		dark, forced bool
	}{
		{"", true, false}, {"dark", true, true}, {"light", false, true}, {"LIGHT", false, true}, {"auto", true, false},
	} {
		env := []string{}
		if tc.v != "" {
			env = append(env, "VECNET_TUI_BG="+tc.v)
		}
		dark, forced := theme.Background(env)
		if dark != tc.dark || forced != tc.forced {
			t.Errorf("VECNET_TUI_BG=%q: dark %v forced %v, want %v %v", tc.v, dark, forced, tc.dark, tc.forced)
		}
	}
}

// Design §10.2 test 2: every role and fill resolves on every rung in both sets,
// and stays inside the rung's limits (§10.2 test 4).
func TestEveryRoleResolves(t *testing.T) {
	for _, p := range rungs {
		for _, dark := range []bool{true, false} {
			th := theme.New(p, dark)
			name := fmt.Sprintf("%s/dark=%v", rungName(p), dark)
			var all strings.Builder
			for _, r := range theme.Roles() {
				out := th.Fg(r).Render("X")
				if !strings.Contains(out, "X") {
					t.Fatalf("%s %s lost its text: %q", name, r, out)
				}
				all.WriteString(out)
				all.WriteString(th.Sel(r).Render("X"))
				all.WriteString(th.OnBar(r).Render("X"))
				// A quiet role on a colour rung sets a colour; text and bold never do.
				hasColour := strings.Contains(out, "38;")
				switch {
				case r == theme.Text || r == theme.Bold:
					if hasColour || (r == theme.Text && out != "X") {
						t.Errorf("%s %s sets a foreground: %q", name, r, out)
					}
				case p == theme.TrueColor || p == theme.ANSI256:
					if !hasColour {
						t.Errorf("%s %s has no colour: %q", name, r, out)
					}
				}
			}
			for _, f := range theme.Fills() {
				all.WriteString(th.Fill(f).Render("X"))
			}
			all.WriteString(th.Brand(" vecnet "))
			for _, mode := range []string{"NORMAL", "INSERT", "COMMAND"} {
				all.WriteString(th.Mode(mode).Render(mode))
			}
			if bad := themetest.Violations(p, all.String()); len(bad) > 0 {
				t.Errorf("%s emits SGR outside its rung:\n%s", name, strings.Join(bad, "\n"))
			}
		}
	}
}

// Every filled cell sets both its background and its foreground (C2).
func TestFillsSetBothColours(t *testing.T) {
	for _, p := range []theme.Profile{theme.TrueColor, theme.ANSI256} {
		for _, dark := range []bool{true, false} {
			th := theme.New(p, dark)
			check := func(what, out string) {
				t.Helper()
				var fg, bg bool
				for _, ps := range themetest.SGRs(out) {
					for _, a := range themetest.Decode(ps) {
						if a.Colour && a.Mode != 0 {
							if a.Fg {
								fg = true
							} else {
								bg = true
							}
						}
					}
				}
				if !fg || !bg {
					t.Errorf("%s dark=%v %s: fg %v bg %v in %q", rungName(p), dark, what, fg, bg, out)
				}
			}
			for _, f := range theme.Fills() {
				check(fmt.Sprintf("fill %d", f), th.Fill(f).Render("X"))
			}
			for _, r := range theme.Roles() {
				check("sel "+r.String(), th.Sel(r).Render("X"))
				check("bar "+r.String(), th.OnBar(r).Render("X"))
			}
		}
	}
}

// §5.1 values, spot-checked on each rung.
func TestRoleValues(t *testing.T) {
	cases := []struct {
		p    theme.Profile
		dark bool
		r    theme.Role
		want string
	}{
		{theme.TrueColor, true, theme.Unread, "1;38;2;251;191;36"},
		{theme.TrueColor, false, theme.Unread, "1;38;2;180;83;9"},
		{theme.TrueColor, true, theme.Error, "38;2;251;113;133"},
		{theme.TrueColor, true, theme.Muted, "38;2;154;164;184"},
		{theme.ANSI256, true, theme.Muted, "38;5;248"},
		{theme.ANSI256, false, theme.Muted, "38;5;60"},
		{theme.ANSI256, true, theme.Accent, "38;5;135"},
		{theme.ANSI256, false, theme.Info, "38;5;24"},
		{theme.ANSI16, true, theme.Accent, "35"},
		{theme.ANSI16, true, theme.Error, "31"},
		{theme.ANSI16, true, theme.Muted, "2"},
		{theme.ANSI16, true, theme.Rule, "2"},
		{theme.ANSI16, true, theme.Unread, "1;33"},
		{theme.NoColor, true, theme.Unread, "1"},
		{theme.NoColor, true, theme.AccentBold, "1"},
	}
	for _, tc := range cases {
		out := theme.New(tc.p, tc.dark).Fg(tc.r).Render("X")
		if !strings.HasPrefix(out, "\x1b["+tc.want+"m") {
			t.Errorf("%s dark=%v %s = %q, want SGR %s", rungName(tc.p), tc.dark, tc.r, out, tc.want)
		}
	}
	if out := theme.New(theme.NoColor, true).Fg(theme.Muted).Render("X"); out != "X" {
		t.Errorf("NO_COLOR muted = %q, want plain", out)
	}
}

// §5.3: quiet roles take the fill's foreground on the selection, state hues
// keep theirs; on 16 and NoColor a selected cell is reverse with no colour.
func TestSelection(t *testing.T) {
	dark := theme.New(theme.TrueColor, true)
	if got, want := dark.Sel(theme.Muted).Render("X"), dark.Sel(theme.Text).Render("X"); got != want {
		t.Errorf("muted on selection %q, want the fill's text %q", got, want)
	}
	if got := dark.Sel(theme.Error).Render("X"); !strings.Contains(got, "38;2;251;113;133") || !strings.Contains(got, "48;2;44;55;86") {
		t.Errorf("error on selection %q keeps neither hue nor fill", got)
	}
	for _, p := range []theme.Profile{theme.ANSI16, theme.NoColor} {
		th := theme.New(p, true)
		for _, r := range theme.Roles() {
			out := th.Sel(r).Render("X")
			if !themetest.Has(out, 7, "X") {
				t.Errorf("%s sel %s is not reverse: %q", rungName(p), r, out)
			}
			for _, ps := range themetest.SGRs(out) {
				for _, a := range themetest.Decode(ps) {
					if a.Colour {
						t.Errorf("%s sel %s keeps a colour: %q", rungName(p), r, out)
					}
				}
			}
		}
	}
}

// §11.3: the brand chip is a per-cell gradient in truecolor only.
func TestBrand(t *testing.T) {
	tc := theme.New(theme.TrueColor, true).Brand(" vecnet ")
	if n := strings.Count(tc, "48;2;"); n != 8 {
		t.Errorf("truecolor brand has %d background cells, want 8: %q", n, tc)
	}
	if !strings.Contains(tc, "48;2;99;102;241") || !strings.Contains(tc, "48;2;168;85;247") {
		t.Errorf("truecolor brand does not run #6366f1 → #a855f7: %q", tc)
	}
	for _, p := range []theme.Profile{theme.ANSI256, theme.ANSI16, theme.NoColor} {
		out := theme.New(p, true).Brand(" vecnet ")
		if n := len(themetest.SGRs(out)); n != 2 {
			t.Errorf("%s brand is not one solid run (%d SGRs): %q", rungName(p), n, out)
		}
		if !themetest.Has(out, 1, " vecnet ") {
			t.Errorf("%s brand is not bold: %q", rungName(p), out)
		}
	}
	if out := theme.New(theme.NoColor, true).Brand(" vecnet "); !themetest.Has(out, 7, " vecnet ") {
		t.Errorf("NO_COLOR brand is not reverse: %q", out)
	}
}

// §6.4: focused panes are heavy in every rung, NO_COLOR included.
func TestPaneBorders(t *testing.T) {
	for _, p := range rungs {
		th := theme.New(p, true)
		b, edge, title := th.Pane(true)
		if b.Top != "━" || b.Left != "┃" || b.TopLeft != "┏" {
			t.Errorf("%s focused border is not heavy: %+v", rungName(p), b)
		}
		if !themetest.Has(title.Render("t"), 1, "t") {
			t.Errorf("%s focused title is not bold", rungName(p))
		}
		_ = edge
		b, _, title = th.Pane(false)
		if b.Top != "─" || b.Left != "│" || b.TopLeft != "┌" {
			t.Errorf("%s unfocused border is not light: %+v", rungName(p), b)
		}
		if themetest.Has(title.Render("t"), 1, "t") {
			t.Errorf("%s unfocused title is bold", rungName(p))
		}
	}
}

// §5.5 state maps.
func TestStateMaps(t *testing.T) {
	check := func(what string, got, want theme.Role) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %s, want %s", what, got, want)
		}
	}
	check("priority wake", theme.Priority("wake"), theme.Warn)
	check("priority wake_idle", theme.Priority("wake_idle"), theme.Info)
	check("priority hold", theme.Priority("hold"), theme.Hold)
	for state, want := range map[string]theme.Role{"queued": theme.Muted, "pushed": theme.Info, "accepted": theme.Indigo, "processed": theme.Success, "read": theme.Text, "failed": theme.Error, "dead_letter": theme.Error, "expired": theme.Muted, "zz": theme.Muted} {
		check("receipt "+state, theme.Receipt(state), want)
	}
	for state, want := range map[string]theme.Role{"live": theme.Success, "connecting": theme.Warn, "reconnecting": theme.Warn, "restarting": theme.Warn, "credential rejected": theme.Error, "operator disabled": theme.Error} {
		check("conn "+state, theme.Conn(state), want)
	}
	for state, want := range map[string]theme.Role{"working": theme.Text, "idle": theme.Muted, "blocked": theme.Warn, "offline": theme.Dim, "": theme.Dim} {
		check("presence "+state, theme.Presence(state), want)
	}
	for state, want := range map[string]theme.Role{"connected": theme.Success, "reachable": theme.Info, "unreachable": theme.Error, "unknown": theme.Dim} {
		check("reach "+state, theme.Reach(state), want)
	}
	for h, want := range map[string]theme.Role{"ok": theme.Success, "late": theme.Warn, "unverified": theme.Warn, "paused_operator": theme.Warn, "failing": theme.Error, "expired": theme.Error, "paused_token": theme.Error, "paused_failed": theme.Error, "paused_auth": theme.Error, "new_state": theme.Muted} {
		check("health "+h, theme.Health(h), want)
	}
	for ev, want := range map[string]theme.Role{"receipt": theme.Success, "approval_expired": theme.Accent, "approval_request": theme.Accent, "delivery_failed": theme.Error, "dead_letter": theme.Error, "presence": theme.Info, "thread_updated": theme.Info, "directory_updated": theme.Info} {
		check("event "+ev, theme.EventType(ev), want)
	}
	check("quota 44", theme.Quota(44, 25, 10), theme.Success)
	check("quota 20", theme.Quota(20, 25, 10), theme.Warn)
	check("quota 3", theme.Quota(3, 25, 10), theme.Error)
	check("count 0", theme.Count(0, theme.Error), theme.Muted)
	check("count 3", theme.Count(3, theme.Error), theme.Error)
	check("http 409", theme.HTTPStatus(409), theme.Error)
	check("http 200", theme.HTTPStatus(200), theme.Text)
	for kind, want := range map[string]theme.Role{"operator": theme.Accent, "session": theme.Ident1, "pagchat-bot": theme.Ident2, "openclaw": theme.Ident3, "dot": theme.Ident3, "grokbot": theme.Ident4, "launcher": theme.Ident5, "source": theme.Muted, "custom": theme.Text} {
		check("kind "+kind, theme.Kind(kind), want)
	}
}

// §5.4: identity hues are never red, amber, orange, green or pink, and the
// sender hue comes from the peer id alone.
func TestSenderHues(t *testing.T) {
	seen := map[theme.Role]bool{}
	for i := range 200 {
		r := theme.Sender(fmt.Sprintf("peer_%03d", i))
		if r < theme.Ident1 || r > theme.Ident5 {
			t.Fatalf("sender hue %s is not an identity hue", r)
		}
		seen[r] = true
	}
	if len(seen) != 5 {
		t.Errorf("200 ids use %d of 5 hues", len(seen))
	}
	if theme.Sender("peer_x") != theme.Sender("peer_x") {
		t.Error("sender hue is not stable")
	}
	banned := []string{"38;2;52;211;153", "38;2;163;230;53", "38;2;45;212;191", "38;2;251;146;60", "38;2;244;114;182"}
	th := theme.New(theme.TrueColor, true)
	for _, r := range []theme.Role{theme.Ident1, theme.Ident2, theme.Ident3, theme.Ident4, theme.Ident5} {
		out := th.Fg(r).Render("X")
		for _, b := range banned {
			if strings.Contains(out, b) {
				t.Errorf("%s uses a retired hue %s", r, b)
			}
		}
	}
}
