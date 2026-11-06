package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	confirm bool
	mouse   bool
	w, h    int
	hits    []hit
	result  PickResult
}

func newPick(choices []ProfileChoice) *pickModel {
	return &pickModel{choices: choices, mouse: true, w: 80, h: 24}
}

func (p *pickModel) Init() tea.Cmd { return nil }

func (p *pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
		}
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
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
	var lines []string
	add := func(s string) { lines = append(lines, fit(s, w)) }
	if p.confirm {
		name := p.choices[p.cursor].Name
		hub := profileHubLabel(name, p.choices[p.cursor].Hub)
		add(styleBrand.Render(" vecnet ") + styleMuted.Render(" confirm"))
		add("")
		add(styleText.Render("run " + name + " for this session"))
		add(styleMuted.Render(hub))
		add("")
		add(styleText.Render("make this the default operator_profile?"))
		y := len(lines)
		yes := styleGood.Render(" y ")
		no := styleWarn.Render(" n ")
		add(yes + styleMuted.Render(" make default   ") + no + styleMuted.Render(" this session only   ") + styleMuted.Render("esc back  q cancel"))
		p.hits = append(p.hits,
			hit{x: 0, y: y, w: lipgloss.Width(yes), h: 1, kind: hitButton, id: "y"},
			hit{x: lipgloss.Width(yes + " make default   "), y: y, w: lipgloss.Width(no), h: 1, kind: hitButton, id: "n"},
		)
		add("")
		add(styleMuted.Render("Shift+drag selects text"))
	} else {
		add(styleBrand.Render(" vecnet ") + styleMuted.Render(" choose an operator profile"))
		add("")
		add(styleMuted.Render(fit("name", 16) + " hub"))
		room := max(h-8, 1)
		if p.cursor < p.top {
			p.top = p.cursor
		}
		if p.cursor >= p.top+room {
			p.top = p.cursor - room + 1
		}
		for i := p.top; i < len(p.choices) && i < p.top+room; i++ {
			c := p.choices[i]
			mark := "  "
			style := styleText
			if i == p.cursor {
				mark = "> "
				style = styleText.Background(colorPick)
			}
			label := mark + fit(c.Name, 16) + " " + hubHost(c.Hub)
			if loopbackHost(hubHost(c.Hub)) {
				label += " local"
			}
			y := len(lines)
			add(style.Render(ansi.Truncate(label, w, "")))
			p.hits = append(p.hits, hit{x: 0, y: y, w: w, h: 1, kind: hitRow, id: fmt.Sprintf("%d", i)})
		}
		add("")
		add(styleMuted.Render("j/k move  enter choose  q cancel  click a row"))
		add(styleMuted.Render("Shift+drag selects text"))
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
	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if in != nil {
		opts = append(opts, tea.WithInput(in))
	}
	if out != nil {
		opts = append(opts, tea.WithOutput(out))
	}
	final, err := tea.NewProgram(m, opts...).Run()
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
