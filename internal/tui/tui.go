// Package tui is `vecnet tui`, the operator's terminal client (Bubble Tea v2).
// It talks to the hub only through API, and it is linked into `vecnet` alone,
// never into the hub.
//
// Everything a peer supplies is hostile (PROTOCOL §14.2): it passes through
// Clean or CleanBody before it reaches lipgloss, so no escape sequence in a bot
// message can touch the operator's terminal.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
	"github.com/XertroV/vecnet/internal/tui/theme"
)

// API is the subset of *opclient.Client the TUI calls; tests use a fake.
type API interface {
	Inbox(ctx context.Context, q opapi.InboxQuery) (opapi.Inbox, error)
	ReadThread(ctx context.Context, id string) (opapi.ReadResult, error)
	Threads(ctx context.Context, q opapi.ThreadQuery) (opapi.ThreadPage, error)
	Thread(ctx context.Context, id string) (opapi.ThreadDetail, error)
	History(ctx context.Context, id, after string, limit int) (opapi.History, error)
	Matrix(ctx context.Context, id, after string, limit int) (opapi.Matrix, error)
	Peers(ctx context.Context) ([]opapi.PeerRow, error)
	Notices(ctx context.Context) ([]envelope.HubNotice, error)
	LoopCuts(ctx context.Context, hours int) (opapi.LoopCuts, error)
	DeliveryHealth(ctx context.Context) (opapi.DeliveryHealth, error)
	Subscriptions(ctx context.Context) ([]opapi.Subscription, error)
	ResumeSubscription(ctx context.Context, id string) error
	Audit(ctx context.Context, limit int, before string) (opapi.AuditPage, error)
	Counters(ctx context.Context) (map[string]int, error)
	RetryDeadLetter(ctx context.Context, dly string) error
	DiscardDeadLetter(ctx context.Context, dly string) error
	Send(ctx context.Context, r envelope.SendRequest) (opapi.SendResult, error)
	React(ctx context.Context, req envelope.ReactionRequest) (opapi.ReactionResult, error)
	Answer(ctx context.Context, a opapi.OpenApproval, option int) error
	SetPresence(ctx context.Context, state string, at time.Time) error
	Blob(ctx context.Context, sha string, max int64) ([]byte, string, error)
	Upload(ctx context.Context, r io.Reader, size int64, mediaType, name string) (envelope.Attachment, error)
	TailWithAck(ctx context.Context, handle func(opclient.Batch) error, status func(string), accepted func(string)) error
	// Preferences is the read-only GET /v1/admin/prefs. The TUI never patches.
	Preferences(ctx context.Context) (opapi.Preferences, error)
}

// Options configures Run. Now, In and Out default to the system clock and the
// process's stdin and stdout.
type Options struct {
	API          API
	Profile, Hub string
	Now          func() time.Time
	In           io.Reader
	Out          io.Writer
	BeatEvery    time.Duration // presence heartbeat; zero means 20 minutes. Test-only override.
}

// Screen is one tab. Update receives every key the shell does not claim while
// the screen is active, and every non-key message whichever tab is showing, so
// a command that finishes after a tab switch still reaches its screen.
type Screen interface {
	Title() string
	Update(msg tea.Msg, m *Model) tea.Cmd
	View(m *Model, w, h int) string
	Hints() string
}

// notOperatorError names the profile, and still matches opclient.ErrForbidden.
type notOperatorError struct{ profile string }

func (e notOperatorError) Error() string {
	return fmt.Sprintf("profile %s is not the operator credential", e.profile)
}
func (e notOperatorError) Unwrap() error { return opclient.ErrForbidden }

// detectTheme is the theme for this process's environment and out (stdout
// when nil), and whether to ask the terminal for its background: only on the
// rungs with two sets, and only when VECNET_TUI_BG has not forced one.
func detectTheme(out io.Writer) (*theme.Theme, bool) {
	if out == nil {
		out = os.Stdout
	}
	env := os.Environ()
	p := theme.Detect(env, out)
	dark, forced := theme.Background(env)
	return theme.New(p, dark), !forced && (p == theme.TrueColor || p == theme.ANSI256)
}

// Run shows the TUI until the operator quits or ctx ends. opclient.ErrForbidden
// during the first load returns "profile <x> is not the operator credential".
func Run(ctx context.Context, o Options) error {
	if o.API == nil {
		return errors.New("tui: no API")
	}
	m := newModel(ctx, o)
	defer m.cancel()
	var askBg bool
	m.th, askBg = detectTheme(o.Out)
	m.askBg = askBg
	// ctx, not m.ctx: quitting cancels m.ctx, and that must not read as killed.
	// The renderer gets the theme's rung, so the two agree on every cell.
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithColorProfile(m.th.Profile())}
	if o.In != nil {
		opts = append(opts, tea.WithInput(o.In))
	}
	if o.Out != nil {
		opts = append(opts, tea.WithOutput(o.Out))
	}
	_, err := runProgram(m, opts...)
	switch {
	case m.fatal != nil:
		return m.fatal
	case err == nil, errors.Is(err, tea.ErrInterrupted), ctx.Err() != nil:
		return nil
	}
	return err
}
