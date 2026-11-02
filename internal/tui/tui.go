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
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
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
	LoopCuts(ctx context.Context, hours int) (opapi.LoopCuts, error)
	DeliveryHealth(ctx context.Context) (opapi.DeliveryHealth, error)
	Subscriptions(ctx context.Context) ([]opapi.Subscription, error)
	ResumeSubscription(ctx context.Context, id string) error
	Audit(ctx context.Context, limit int, before string) (opapi.AuditPage, error)
	Counters(ctx context.Context) (map[string]int, error)
	RetryDeadLetter(ctx context.Context, dly string) error
	DiscardDeadLetter(ctx context.Context, dly string) error
	Send(ctx context.Context, r envelope.SendRequest) (opapi.SendResult, error)
	Answer(ctx context.Context, a opapi.OpenApproval, option int) error
	SetPresence(ctx context.Context, state string, at time.Time) error
	Blob(ctx context.Context, sha string, max int64) ([]byte, string, error)
	Upload(ctx context.Context, r io.Reader, size int64, mediaType, name string) (envelope.Attachment, error)
	Tail(ctx context.Context, handle func(opclient.Batch) error, status func(string)) error
}

// Options configures Run. Now, In and Out default to the system clock and the
// process's stdin and stdout.
type Options struct {
	API          API
	Profile, Hub string
	Now          func() time.Time
	In           io.Reader
	Out          io.Writer
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

// Run shows the TUI until the operator quits or ctx ends. opclient.ErrForbidden
// during the first load returns "profile <x> is not the operator credential".
func Run(ctx context.Context, o Options) error {
	if o.API == nil {
		return errors.New("tui: no API")
	}
	m := newModel(ctx, o)
	defer m.cancel()
	// ctx, not m.ctx: quitting cancels m.ctx, and that must not read as killed.
	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if o.In != nil {
		opts = append(opts, tea.WithInput(o.In))
	}
	if o.Out != nil {
		opts = append(opts, tea.WithOutput(o.Out))
	}
	_, err := tea.NewProgram(m, opts...).Run()
	switch {
	case m.fatal != nil:
		return m.fatal
	case err == nil, errors.Is(err, tea.ErrInterrupted), ctx.Err() != nil:
		return nil
	}
	return err
}
