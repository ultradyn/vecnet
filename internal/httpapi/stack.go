package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/oauth"
	"github.com/XertroV/vecnet/internal/push"
)

// Stack is everything that serves a hub besides the HTTP listener: its two
// workers and its OAuth server, wired to each other. The binary and testhub
// both build it here, so a seam such as the Events worker's ResolveDotPeer
// cannot be wired in one and missed in the other.
type Stack struct {
	Hub    *hub.Hub
	Push   *push.Worker
	Events *events.Worker
	OAuth  *oauth.Server
	// TickEvery is the period of the hub's tickers; zero means
	// hub.DefaultTickInterval.
	TickEvery time.Duration
}

// NewStack returns h's Stack. Nothing runs until Run.
func NewStack(h *hub.Hub) *Stack {
	o := oauth.New(h)
	return &Stack{
		Hub:    h,
		Push:   &push.Worker{Hub: h},
		Events: &events.Worker{Hub: h, ResolveDotPeer: o.ResolveDotPeer},
		OAuth:  o,
	}
}

// Deps returns the handler's collaborators from this Stack.
func (st *Stack) Deps() Deps {
	return Deps{Hub: st.Hub, Kick: st.Push.Kick, VerifyRoute: st.Push.Verify, OAuth: st.OAuth, Limiter: DefaultLimiter()}
}

// Run runs the push worker, the Events worker and the hub's tickers until ctx
// ends.
func (st *Stack) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { st.Push.Run(ctx) })
	wg.Go(func() { st.Events.Run(ctx) })
	wg.Go(func() { st.Hub.RunTickers(ctx, st.TickEvery) })
	wg.Wait()
}
