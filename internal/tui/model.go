package tui

import (
	"context"
	"net/url"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
)

var _ tea.Model = (*Model)(nil)

// Mode is how keys are routed.
type Mode int

const (
	// ModeNormal gives the shell its keys first, then the active screen.
	ModeNormal Mode = iota
	// ModeInsert gives every key to the active screen except ctrl+c, ctrl+q
	// (quit, with confirm) and ctrl+l (reload). A screen enters it for a
	// composer or an answer prompt and sets ModeNormal again when done.
	ModeInsert
	// ModeCommand gives every key to updateCommand.
	ModeCommand
)

func (m Mode) String() string {
	switch m {
	case ModeInsert:
		return "INSERT"
	case ModeCommand:
		return "COMMAND"
	}
	return "NORMAL"
}

// Connection states, as the top bar renders them.
const (
	ConnConnecting = "connecting"
	ConnLive       = "live"
	ConnOffline    = "offline, retrying"
	ConnRejected   = "credential rejected"
	ConnDisabled   = "operator disabled"
)

// Tab indexes.
const (
	tabInbox = iota
	tabChat
	tabDash
	tabSubs
)

// tabNames are the tabs' stable names, indexed like the tab constants. Titles
// are for display and may carry counts.
var tabNames = []string{"inbox", "chat", "dash", "subs"}

// Command is one entry of the command line.
type Command struct {
	Help string
	Run  func(m *Model, args []string) tea.Cmd
}

type toast struct {
	text  string
	bad   bool
	until time.Time
}

// Model is the whole TUI state. It is a tea.Model through its pointer; Update
// and View run on the program's goroutine, and commands must not touch it.
type Model struct {
	API           API
	Data          Data
	Mode          Mode
	Tab           int
	W, H          int
	Conn          string
	CurrentThread string
	Now           func() time.Time
	Commands      map[string]Command

	Profile, Hub string
	// Prio is the priority a new message from this client would carry.
	Prio string
	// Input is the command line's text; cmdline.go owns editing it.
	Input string
	// Help replaces the body with viewHelp.
	Help bool

	screens []Screen
	loc     *time.Location
	ctx     context.Context
	cancel  context.CancelFunc

	hubOffset   time.Duration
	started     time.Time
	lastSlow    time.Time
	toast       toast
	confirmQuit bool
	fatal       error

	// Load bookkeeping; see data.go.
	inflight map[string]bool
	dirty    map[string]bool
	waiters  []*waiter
	first    map[string]bool
	tailCh   chan tea.Msg
	stopTail context.CancelFunc
}

func newModel(ctx context.Context, o Options) *Model {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Model{
		API:      o.API,
		Profile:  o.Profile,
		Hub:      o.Hub,
		Now:      now,
		Conn:     ConnConnecting,
		Prio:     "wake",
		Commands: map[string]Command{},
		Data:     Data{Thread: map[string]*ThreadState{}, Errs: map[string]error{}},
		screens:  []Screen{newInboxScreen(), newChatScreen(), newDashScreen(), newSubsScreen()},
		loc:      time.Local,
		ctx:      ctx,
		cancel:   cancel,
		inflight: map[string]bool{},
		dirty:    map[string]bool{},
		first:    map[string]bool{},
		tailCh:   make(chan tea.Msg),
		stopTail: func() {},
	}
}

// Init starts the first load, the event tail and the clock.
func (m *Model) Init() tea.Cmd {
	m.started, m.lastSlow = m.Now(), m.Now()
	for _, key := range allKeys {
		m.first[key] = true
	}
	return tea.Batch(m.Reload(TagAll), m.startTail(), m.listenTail(), tick())
}

// Update routes one message. Shell messages stop here; anything else reaches
// every screen, so a screen's own command results find it.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m, m.updateKey(msg)
	case tea.PasteMsg:
		return m, m.updatePaste(msg)
	case tea.WindowSizeMsg:
		m.W, m.H = msg.Width, msg.Height
	case tickMsg:
		return m, m.onTick()
	case reloadMsg:
		return m, m.startLoads(msg.tags, nil)
	case loadMsg:
		return m, m.onLoad(msg)
	case tailStatusMsg:
		return m, tea.Batch(m.onTailStatus(msg.status), m.listenTail())
	case tailBatchMsg:
		return m, tea.Batch(m.onTailBatch(msg), m.listenTail())
	case tailDoneMsg:
		return m, m.onTailDone(msg.err)
	}
	return m, m.broadcast(msg)
}

func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.screens))
	for _, s := range m.screens {
		cmds = append(cmds, s.Update(msg, m))
	}
	return tea.Batch(cmds...)
}

func (m *Model) active() Screen { return m.screens[m.Tab] }

// View draws the full-screen frame.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorText
	return v
}

// HubNow is the hub's clock: m.Now() plus the offset from the last snapshot's
// now. Countdowns use it, because the client's clock may be minutes out.
func (m *Model) HubNow() time.Time { return m.Now().Add(m.hubOffset) }

// noteHubNow takes the offset from a snapshot received at the client time at.
// An unreadable now keeps the last good offset.
func (m *Model) noteHubNow(now string, at time.Time) {
	if now == "" {
		return
	}
	if t, err := envelope.ParseTime(now); err == nil {
		m.hubOffset = t.Sub(at)
	}
}

const (
	toastGood = 2500 * time.Millisecond
	toastBad  = 6 * time.Second
)

// Toast shows msg on the command line for a few seconds. msg may hold
// peer-supplied text; it is cleaned here.
func (m *Model) Toast(msg string, bad bool) {
	d := toastGood
	if bad {
		d = toastBad
	}
	m.toast = toast{text: Clean(msg), bad: bad, until: m.Now().Add(d)}
}

// OpenThread switches to the chat tab, sets m.CurrentThread and loads it.
func (m *Model) OpenThread(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	m.Tab = tabChat
	m.CurrentThread = id
	if m.Data.Thread[id] == nil {
		m.Data.Thread[id] = &ThreadState{Loading: true}
	}
	return m.Reload(threadPrefix + id)
}

// quit ends the program and every request and tail it started.
func (m *Model) quit() tea.Cmd {
	m.cancel()
	return tea.Quit
}

// hubHost is the host of the hub URL, without credentials or path.
func hubHost(hub string) string {
	if u, err := url.Parse(hub); err == nil && u.Host != "" {
		return Clean(u.Host)
	}
	return Clean(hub)
}
