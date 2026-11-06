package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/XertroV/vecnet/internal/tui/theme"
	"github.com/charmbracelet/x/ansi"
)

// ProfileChoice is one operator profile the picker may offer. It carries no
// secret: the CLI detects vn_operator_ before anything is drawn.
type ProfileChoice struct {
	Name string
	Hub  string
}

// PickResult is the operator's choice. Cancelled runs nothing and writes
// nothing. MakeDefault rewrites only the config's operator_profile line.
type PickResult struct {
	Name        string
	MakeDefault bool
	Cancelled   bool
}

type pickModel struct {
	choices []ProfileChoice
	cursor  int
	top     int
	follow  bool // keyboard selection owns the viewport; the wheel clears it
	confirm bool
	mouse   bool
	w, h    int
	hits    []hit
	result  PickResult
	// th is the picker's own theme, from the same Detect as the shell's.
	th    *theme.Theme
	askBg bool
}

func newPick(choices []ProfileChoice) *pickModel {
	return &pickModel{choices: choices, mouse: true, w: 80, h: 24, th: theme.New(theme.TrueColor, true)}
}

func (p *pickModel) Init() tea.Cmd {
	if p.askBg {
		return tea.RequestBackgroundColor
	}
	return nil
}

func (p *pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		if p.askBg {
			p.th = theme.New(p.th.Profile(), msg.IsDark())
		}
	case tea.WindowSizeMsg:
		p.w, p.h = msg.Width, msg.Height
	case tea.KeyPressMsg:
		return p, p.onKey(msg.String())
	case tea.MouseClickMsg:
		if !p.mouse || msg.Button != tea.MouseLeft || msg.Mod&tea.ModShift != 0 {
			return p, nil
		}
		return p, p.onClick(msg.X, msg.Y)
	case tea.MouseWheelMsg:
		if !p.mouse || msg.Mod&tea.ModShift != 0 {
			return p, nil
		}
		if msg.Button == tea.MouseWheelDown {
			p.scroll(3)
		} else if msg.Button == tea.MouseWheelUp {
			p.scroll(-3)
		}
	}
	return p, nil
}

func (p *pickModel) onKey(key string) tea.Cmd {
	if key == "ctrl+z" {
		disarmStop()
		return doSuspend
	}
	if p.confirm {
		switch key {
		case "y":
			p.result = PickResult{Name: p.choices[p.cursor].Name, MakeDefault: true}
			return tea.Quit
		case "n":
			p.result = PickResult{Name: p.choices[p.cursor].Name}
			return tea.Quit
		case "esc":
			p.confirm = false
		case "q", "ctrl+c":
			p.result = PickResult{Cancelled: true}
			return tea.Quit
		}
		return nil
	}
	switch key {
	case "j", "down":
		if p.cursor < len(p.choices)-1 {
			p.cursor++
			p.follow = true
		}
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
			p.follow = true
		}
	case "enter":
		p.confirm = true
	case "q", "esc", "ctrl+c":
		p.result = PickResult{Cancelled: true}
		return tea.Quit
	case "M":
		p.mouse = false
	}
	return nil
}

func (p *pickModel) onClick(x, y int) tea.Cmd {
	for i := len(p.hits) - 1; i >= 0; i-- {
		h := p.hits[i]
		if x < h.x || x >= h.x+h.w || y < h.y || y >= h.y+h.h {
			continue
		}
		switch h.id {
		case "y":
			p.result = PickResult{Name: p.choices[p.cursor].Name, MakeDefault: true}
			return tea.Quit
		case "n":
			p.result = PickResult{Name: p.choices[p.cursor].Name}
			return tea.Quit
		case "cancel":
			p.result = PickResult{Cancelled: true}
			return tea.Quit
		default:
			if n, err := strconvAtoi(h.id); err == nil && n >= 0 && n < len(p.choices) {
				p.cursor = n
				p.confirm = true
			}
		}
		return nil
	}
	return nil
}

func strconvAtoi(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("nan")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func (p *pickModel) scroll(d int) {
	p.follow = false
	p.top += d
	if p.top < 0 {
		p.top = 0
	}
	room := max(p.h-8, 1)
	maxTop := max(len(p.choices)-room, 0)
	if p.top > maxTop {
		p.top = maxTop
	}
}

func (p *pickModel) View() tea.View {
	p.hits = p.hits[:0]
	w, h := p.w, p.h
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 24
	}
	th := p.th
	muted, text := th.Fg(theme.Muted), th.Fg(theme.Text)
	var lines []string
	add := func(s string) { lines = append(lines, fit(s, w)) }
	if p.confirm {
		c := p.choices[p.cursor]
		name := Clean(c.Name)
		hub := profileHubLabel(name, c.Hub)
		add(th.Brand(" vecnet ") + muted.Render(" confirm"))
		add("")
		add(text.Render("run " + name + " for this session"))
		add(hubLabelStyled(th, hub, loopbackHost(hubHost(c.Hub)), false))
		add("")
		add(text.Render("make this the default operator_profile?"))
		y := len(lines)
		// Neither choice is a warning: both keys are actions (design §7.6).
		yes := th.Fg(theme.Action).Render(" y ")
		no := th.Fg(theme.Action).Render(" n ")
		add(yes + muted.Render(" make default   ") + no + muted.Render(" this session only   ") + muted.Render("esc back  q cancel"))
		p.hits = append(p.hits,
			hit{x: 0, y: y, w: lipgloss.Width(yes), h: 1, kind: hitButton, id: "y"},
			hit{x: lipgloss.Width(yes + " make default   "), y: y, w: lipgloss.Width(no), h: 1, kind: hitButton, id: "n"},
		)
		add("")
		add(muted.Render("Shift+drag selects text"))
	} else {
		add(th.Brand(" vecnet ") + muted.Render(" choose an operator profile"))
		add("")
		add(muted.Render(fit("name", 16) + " hub"))
		room := max(h-8, 1)
		if p.follow {
			if p.cursor < p.top {
				p.top = p.cursor
			}
			if p.cursor >= p.top+room {
				p.top = p.cursor - room + 1
			}
		}
		for i := p.top; i < len(p.choices) && i < p.top+room; i++ {
			c := p.choices[i]
			sel := i == p.cursor
			row := th.On(sel, theme.Text)
			label := selMark(th, sel) + row.Render(" "+fit(Clean(c.Name), 16)+" "+hubHost(c.Hub))
			if loopbackHost(hubHost(c.Hub)) {
				label += row.Render(" ") + th.On(sel, theme.Warn).Render("local")
			}
			y := len(lines)
			add(selPad(th, ansi.Truncate(label, w, ""), w, sel))
			p.hits = append(p.hits, hit{x: 0, y: y, w: w, h: 1, kind: hitRow, id: fmt.Sprintf("%d", i)})
		}
		add("")
		add(muted.Render("j/k move  enter choose  q cancel  click a row"))
		add(muted.Render("Shift+drag selects text"))
	}
	for len(lines) < h {
		lines = append(lines, fit("", w))
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	if p.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// Pick runs the profile picker until the operator chooses or cancels.
func Pick(ctx context.Context, choices []ProfileChoice, in io.Reader, out io.Writer) (PickResult, error) {
	if len(choices) == 0 {
		return PickResult{}, errors.New("tui: no operator profile to choose")
	}
	m := newPick(choices)
	m.th, m.askBg = detectTheme(out)
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithColorProfile(m.th.Profile())}
	if in != nil {
		opts = append(opts, tea.WithInput(in))
	}
	if out != nil {
		opts = append(opts, tea.WithOutput(out))
	}
	final, err := runProgram(m, opts...)
	if final != nil {
		if got, ok := final.(*pickModel); ok {
			if err == nil || errors.Is(err, tea.ErrInterrupted) {
				return got.result, nil
			}
		}
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return PickResult{Cancelled: true}, nil
	}
	return PickResult{}, err
}
