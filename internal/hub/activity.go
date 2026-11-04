package hub

import (
	"context"
	"sync"
	"sync/atomic"
)

// ActivityBuffer is each activity subscriber's queue. A subscriber that
// falls this far behind loses events and is told to resync.
const ActivityBuffer = 64

// ActivityEvent is one entry of the operator activity feed (PROTOCOL §2.13
// "Activity feed"): metadata only, never a body or a title. Type "message"
// carries every field; type "thread" (a thread's metadata changed) carries
// only ThreadID and At.
type ActivityEvent struct {
	Type      string `json:"-"`
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Kind      string `json:"kind,omitempty"`
	At        string `json:"at"`
}

// ActivityFeed is an in-memory, lossy broadcaster of committed thread
// activity to the operator's open feeds. Nothing is stored or replayed:
// clients refetch on connect and on resync. Publish never blocks.
type ActivityFeed struct {
	mu   sync.Mutex
	subs map[*ActivitySub]struct{}
}

// ActivitySub is one subscriber. Events arrives on C; a publish that found C
// full sets lost instead, and the reader resyncs (TakeLost).
type ActivitySub struct {
	C    chan ActivityEvent
	lost atomic.Bool
}

// TakeLost reports whether an event was dropped since the last call, and
// clears the mark.
func (s *ActivitySub) TakeLost() bool { return s.lost.Swap(false) }

// Subscribe registers a subscriber until ctx is done.
func (f *ActivityFeed) Subscribe(ctx context.Context) *ActivitySub {
	s := &ActivitySub{C: make(chan ActivityEvent, ActivityBuffer)}
	f.mu.Lock()
	if f.subs == nil {
		f.subs = map[*ActivitySub]struct{}{}
	}
	f.subs[s] = struct{}{}
	f.mu.Unlock()
	context.AfterFunc(ctx, func() {
		f.mu.Lock()
		delete(f.subs, s)
		f.mu.Unlock()
	})
	return s
}

// Publish offers evs to every subscriber without waiting. Call it only after
// the transaction that made them has committed.
func (f *ActivityFeed) Publish(evs ...ActivityEvent) {
	if f == nil || len(evs) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for s := range f.subs {
		for _, ev := range evs {
			select {
			case s.C <- ev:
			default:
				s.lost.Store(true)
			}
		}
	}
}

// Subscribers is the number of open subscriptions (tests wait on it).
func (f *ActivityFeed) Subscribers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// messageActivity is the feed entry for a stored message.
func messageActivity(threadID, messageID, sender, kind, at string) ActivityEvent {
	return ActivityEvent{Type: "message", ThreadID: threadID, MessageID: messageID, Sender: sender, Kind: kind, At: at}
}
