package tui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxHistory = 100
	// retryKey and presenceKey name no resource: their commands finish through
	// loadMsg, the one message whose apply runs on the program's goroutine.
	retryKey    = "cmd:retry"
	presenceKey = "cmd:presence"
	// retryShown keeps the progress line up however slowly the hub answers.
	// The summary replaces it; this only bounds a run that never reports.
	retryShown = time.Hour
)

var presenceStates = []string{"idle", "working", "blocked", "offline"} // PROTOCOL §2.6

// cmdline is the command line's memory between keys.
type cmdline struct {
	history []string
	nav     int    // position in history; len(history) means not browsing
	draft   string // what was being typed before the first up
	matches []string
	next    int // the match the next tab shows
}

// cmdlines holds each Model's cmdline, which Model has no field for, and goes
// when the Model's context ends.
var cmdlines sync.Map // *Model → *cmdline

// lineOf returns m's command line, registering the built-ins on first use. A
// name another screen has already registered keeps its own command.
func lineOf(m *Model) *cmdline {
	if c, ok := cmdlines.Load(m); ok {
		return c.(*cmdline)
	}
	for name, cmd := range builtinCommands() {
		if _, taken := m.Commands[name]; !taken {
			m.Commands[name] = cmd
		}
	}
	c := &cmdline{}
	cmdlines.Store(m, c)
	context.AfterFunc(m.ctx, func() { cmdlines.Delete(m) })
	return c
}

func builtinCommands() map[string]Command {
	return map[string]Command{
		"q":        {Help: "quit", Run: func(m *Model, _ []string) tea.Cmd { return m.quit() }},
		"refresh":  {Help: "reload everything", Run: func(m *Model, _ []string) tea.Cmd { return m.reloadAll() }},
		"presence": {Help: "<" + strings.Join(presenceStates, "|") + ">  set your presence", Run: runPresence},
		"retry":    {Help: "all  retry every listed dead letter", Run: runRetry},
		"thread":   {Help: "<n>  nth thread in the chat list", Run: runThread},
		"tab":      {Help: "<name>  switch tab", Run: runTab},
	}
}

// parseLine splits a command line on whitespace. There is no quoting: a quote
// is an ordinary character. An empty name means nothing was typed.
func parseLine(line string) (string, []string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) == 1 {
		return fields[0], nil
	}
	return fields[0], fields[1:]
}

// updateCommand edits the command line while Mode is ModeCommand.
func updateCommand(m *Model, msg tea.Msg) tea.Cmd {
	c := lineOf(m)
	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.Input += Clean(msg.Content)
		c.matches = nil
	case tea.KeyPressMsg:
		key := msg.String()
		if key != "tab" {
			c.matches = nil
		}
		switch key {
		case "esc", "ctrl+c":
			c.leave(m)
		case "enter":
			line := m.Input
			c.leave(m)
			return c.run(m, line)
		case "tab":
			c.complete(m)
		case "up":
			c.browse(m, -1)
		case "down":
			c.browse(m, 1)
		case "backspace":
			if r := []rune(m.Input); len(r) > 0 {
				m.Input = string(r[:len(r)-1])
			}
		default:
			m.Input += Clean(msg.Text)
		}
	}
	return nil
}

func (c *cmdline) leave(m *Model) {
	m.Mode = ModeNormal
	m.Input = ""
	c.nav, c.draft = len(c.history), ""
}

// run records line and dispatches it. A mistyped line is kept too, so it can be
// recalled and fixed.
func (c *cmdline) run(m *Model, line string) tea.Cmd {
	name, args := parseLine(line)
	if name == "" {
		return nil
	}
	c.remember(strings.TrimSpace(line))
	cmd, ok := m.Commands[name]
	if !ok {
		m.Toast("unknown command: "+name, true)
		return nil
	}
	return cmd.Run(m, args)
}

func (c *cmdline) remember(line string) {
	if n := len(c.history); n == 0 || c.history[n-1] != line {
		c.history = append(c.history, line)
	}
	if extra := len(c.history) - maxHistory; extra > 0 {
		c.history = slices.Delete(c.history, 0, extra)
	}
	c.nav = len(c.history)
}

// browse moves through history by step. Going past the newest entry returns
// what was being typed.
func (c *cmdline) browse(m *Model, step int) {
	nav := c.nav + step
	if nav < 0 || nav > len(c.history) {
		return
	}
	if c.nav == len(c.history) {
		c.draft = m.Input
	}
	c.nav = nav
	if nav == len(c.history) {
		m.Input = c.draft
		return
	}
	m.Input = c.history[nav]
}

// complete finishes the command name being typed. One match ends in a space,
// ready for its arguments; several cycle in order on each tab. Arguments are
// not completed.
func (c *cmdline) complete(m *Model) {
	if c.matches == nil {
		word := strings.TrimLeft(m.Input, " ")
		if strings.ContainsAny(word, " \t") {
			return
		}
		for _, name := range slices.Sorted(maps.Keys(m.Commands)) {
			if strings.HasPrefix(name, word) {
				c.matches = append(c.matches, name)
			}
		}
		c.next = 0
	}
	switch len(c.matches) {
	case 0:
	case 1:
		m.Input = c.matches[0] + " "
		c.matches = nil
	default:
		m.Input = c.matches[c.next%len(c.matches)]
		c.next++
	}
}

// finish is a command's final message: fn runs on the program's goroutine, and
// reload, if any, starts beside it.
func finish(key string, fn func(*Model), reload tea.Cmd) tea.Msg {
	done := loadMsg{key: key, apply: fn}
	if reload == nil {
		return done
	}
	return tea.BatchMsg{func() tea.Msg { return done }, reload}
}

func usage(m *Model, name string) tea.Cmd {
	syntax, _, _ := strings.Cut(m.Commands[name].Help, "  ")
	m.Toast("usage: :"+name+" "+syntax, true)
	return nil
}

// refuseStopped reports whether the hub has refused this credential, so a
// command must send nothing.
func refuseStopped(m *Model) bool {
	if m.stopped() {
		m.Toast(m.Conn+": restart with a valid profile", true)
	}
	return m.stopped()
}

// runPresence reports the operator's presence (PROTOCOL §2.6). Its at is the
// hub's clock: the hub ages presence by at, and this client's clock may be out.
func runPresence(m *Model, args []string) tea.Cmd {
	if len(args) != 1 || !slices.Contains(presenceStates, args[0]) {
		return usage(m, "presence")
	}
	if refuseStopped(m) {
		return nil
	}
	state := args[0]
	api, ctx, at := m.API, m.ctx, m.HubNow()
	// A presence event tags only peers; the status line reads the inbox's me.
	reload := m.Reload("inbox", "peers")
	return func() tea.Msg {
		if err := api.SetPresence(ctx, state, at); err != nil {
			return finish(presenceKey, func(m *Model) {
				if isStatus(err, 401) {
					m.reject(err)
				}
				m.Toast("presence: "+err.Error(), true)
			}, nil)
		}
		return finish(presenceKey, func(m *Model) { m.Toast("presence "+state, false) }, reload)
	}
}

// runRetry retries every dead letter listed in the last inbox, one at a time. A
// failure moves on to the next, except a credential the hub refused: each later
// attempt would be refused too, so those count as failed, unsent.
func runRetry(m *Model, args []string) tea.Cmd {
	if len(args) != 1 || args[0] != "all" {
		return usage(m, "retry")
	}
	if refuseStopped(m) {
		return nil
	}
	if m.inflight[retryKey] {
		m.Toast("retry all is already running", true)
		return nil
	}
	ids := make([]string, 0, len(m.Data.Inbox.DeadLetters))
	for _, dl := range m.Data.Inbox.DeadLetters {
		ids = append(ids, dl.DeliveryID)
	}
	if len(ids) == 0 {
		m.Toast("no dead letters to retry", false)
		return nil
	}
	m.inflight[retryKey] = true
	m.Toast(fmt.Sprintf("retrying dead letters: %d", len(ids)), false)
	m.toast.until = m.Now().Add(retryShown)
	api, ctx := m.API, m.ctx
	reload := m.Reload("inbox", "health")
	return func() tea.Msg {
		ok := 0
		var refused error
		for _, id := range ids {
			err := api.RetryDeadLetter(ctx, id)
			if err == nil {
				ok++
			}
			if isStatus(err, 401) || isStatus(err, 403) || ctx.Err() != nil {
				refused = err
				break
			}
		}
		failed := len(ids) - ok
		summary := func(m *Model) {
			if isStatus(refused, 401) {
				m.reject(refused)
			}
			m.Toast(fmt.Sprintf("%d ok, %d failed", ok, failed), failed > 0)
			// A run can outlast the operator's attention; a clean one should
			// not vanish faster than a failed one.
			m.toast.until = m.Now().Add(toastBad)
		}
		// With nothing retried there is nothing to show, and a reload failing
		// for the same reason would replace the summary.
		if ok == 0 {
			return finish(retryKey, summary, nil)
		}
		return finish(retryKey, summary, reload)
	}
}

// runThread opens the nth thread of the chat list, counting from 1.
func runThread(m *Model, args []string) tea.Cmd {
	if len(args) != 1 {
		return usage(m, "thread")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return usage(m, "thread")
	}
	rows := m.Data.Threads.Threads
	if n < 1 || n > len(rows) {
		m.Toast(fmt.Sprintf("no thread %d: the chat list has %d", n, len(rows)), true)
		return nil
	}
	return m.OpenThread(rows[n-1].Thread.ThreadID)
}

func runTab(m *Model, args []string) tea.Cmd {
	if len(args) != 1 {
		return usage(m, "tab")
	}
	for i, s := range m.screens {
		if s.Title() == args[0] {
			m.Tab = i
			return nil
		}
	}
	m.Toast("unknown tab: "+args[0], true)
	return nil
}

// helpRow is one line of the help overlay.
type helpRow struct{ key, text string }

var helpKeys = []helpRow{
	{"1-4", "switch tab"},
	{"q", "quit"},
	{":", "command line"},
	{"?", "this help; any key closes it"},
	{"ctrl+l", "reload everything"},
}

// viewHelp is the body while Help is open: the global keys, the active screen's
// hints and every registered command. A command's Help is its usage, two
// spaces, then what it does; with no two spaces it is all description.
func viewHelp(m *Model) string {
	lineOf(m)
	commands := make([]helpRow, 0, len(m.Commands))
	for _, name := range slices.Sorted(maps.Keys(m.Commands)) {
		key, text := ":"+name, m.Commands[name].Help
		if syntax, rest, ok := strings.Cut(text, "  "); ok {
			key, text = key+" "+syntax, rest
		}
		commands = append(commands, helpRow{key, text})
	}
	section := func(title string, rows []helpRow) []string {
		width := 0
		for _, row := range rows {
			width = max(width, lipgloss.Width(row.key))
		}
		lines := []string{styleAccent.Render(title)}
		for _, row := range rows {
			lines = append(lines, "  "+styleText.Render(fit(row.key, width))+"  "+styleMuted.Render(row.text))
		}
		return lines
	}
	hints := ansi.Wordwrap(m.active().Hints(), max(m.W-4, minW-4), "")
	lines := section("global keys", helpKeys)
	lines = append(lines, "", styleAccent.Render(m.active().Title()))
	for _, hint := range strings.Split(hints, "\n") {
		lines = append(lines, "  "+styleMuted.Render(hint))
	}
	lines = append(lines, "")
	lines = append(lines, section("commands", commands)...)
	return strings.Join(lines, "\n")
}
