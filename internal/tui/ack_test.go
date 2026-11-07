package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
	"github.com/XertroV/vecnet/internal/opclient"
)

type ackBarrierMsg struct{}

type ackBarrierScreen struct {
	staticScreen
	seen bool
}

func (s *ackBarrierScreen) Update(msg tea.Msg, m *Model) tea.Cmd {
	if _, ok := msg.(ackBarrierMsg); ok {
		s.seen = true
	}
	return s.staticScreen.Update(msg, m)
}

// ackAPI uses the real HTTP tail and matrix with the other snapshots faked.
type ackAPI struct {
	*fakeAPI
	client *opclient.Client
	paid   *atomic.Bool
	pump   *pump
}

func (a *ackAPI) Matrix(ctx context.Context, id, after string, limit int) (opapi.Matrix, error) {
	return a.client.Matrix(ctx, id, after, limit)
}

func (a *ackAPI) TailWithAck(ctx context.Context, handle func(opclient.Batch) error, status func(string), accepted func(string)) error {
	return a.client.TailWithAck(ctx, handle, func(s string) {
		status(s)
		if s == "live" && a.paid.Load() {
			// Receiving this status requires the previous ack notification to
			// have reached Update. Its loads have started before this barrier.
			a.pump.msgs <- ackBarrierMsg{}
		}
	}, accepted)
}

func TestTailAckRefreshesOwnReceipts(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "already read"}[read], func(t *testing.T) {
			ready := make(chan struct{})
			var paid atomic.Bool
			var ownLoads, otherLoads atomic.Int32
			var polls atomic.Int32
			delivery := event(t, "e1.1", "delivery", map[string]any{"message": map[string]string{"thread_id": "thr_own"}})
			view := event(t, "e1.2", "operator_view", map[string]string{"thread_id": "thr_other", "reason": "read"})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/events":
					if polls.Add(1) == 1 {
						select {
						case <-ready:
						case <-r.Context().Done():
							return
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"events": []envelope.Event{delivery, view}, "next_cursor": "e1.2"})
						return
					}
					<-r.Context().Done() // no later event can correct the snapshot
				case r.URL.Path == "/v1/ack":
					if ownLoads.Load() != 2 || otherLoads.Load() != 2 {
						t.Errorf("ack before snapshots loaded: own=%d other=%d", ownLoads.Load(), otherLoads.Load())
					}
					paid.Store(true)
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/deliveries"):
					state := "queued"
					if strings.Contains(r.URL.Path, "/thr_own/") {
						ownLoads.Add(1)
						if paid.Load() {
							state = "accepted"
						}
					} else {
						otherLoads.Add(1)
					}
					var readAt *string
					if read {
						at := envelope.FormatTime(fixedNow)
						readAt = &at
					}
					cell := opapi.MatrixCell{Recipient: "peer_op", State: state, Display: opapi.DisplayState(state, readAt), ReadAt: readAt}
					_ = json.NewEncoder(w).Encode(opapi.Matrix{Rows: []opapi.MatrixRow{{MessageID: "msg_1", Cells: []opapi.MatrixCell{cell}}}})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			a := &ackAPI{fakeAPI: seeded(), client: opclient.New(srv.URL, "test"), paid: &paid}
			m := newTestModel(t, a, newClock())
			barrier := &ackBarrierScreen{}
			m.screens = []Screen{barrier}
			p := newPump(t, m)
			a.pump = p
			// Stop the tail before the HTTP server cleanup waits for its poll.
			t.Cleanup(m.cancel)
			p.run(m.Init())
			p.until("first load", func() bool { return !m.firstLoading() })
			m.Data.Thread["thr_own"], m.Data.Thread["thr_other"] = &ThreadState{}, &ThreadState{}
			p.run(m.startLoads([]string{"thread:thr_own", "thread:thr_other"}, nil))
			p.idle()
			close(ready)
			p.until("paid ack notification", func() bool { return barrier.seen })
			p.idle()
			want := "accepted"
			if read {
				want = "read"
			}
			if got := m.Data.Thread["thr_own"].Matrix.Rows[0].Cells[0].Display; got != want {
				t.Fatalf("own receipt after successful ack=%q, want %q", got, want)
			}
			if ownLoads.Load() != 3 || otherLoads.Load() != 2 {
				t.Fatalf("post-ack refresh must cover only delivery threads: own=%d other=%d", ownLoads.Load(), otherLoads.Load())
			}
		})
	}
}

func TestTailAckCarriesDeliveryThreadsUntilPaid(t *testing.T) {
	f := seeded()
	m, p, handle := tailHandle(t, f)
	for _, id := range []string{"thr_one", "thr_two", "thr_view"} {
		m.Data.Thread[id] = &ThreadState{}
	}
	for i, id := range []string{"thr_one", "thr_two"} {
		cursor := fmt.Sprintf("e1.%d", i+1)
		b := opclient.Batch{Cursor: cursor, Events: []envelope.Event{event(t, cursor, "delivery", map[string]any{"message": map[string]string{"thread_id": id}})}}
		if i == 1 {
			b.Events = append(b.Events, event(t, "e1.3", "operator_view", map[string]string{"thread_id": "thr_view", "reason": "read"}))
			b.Cursor = "e1.3"
		}
		if err := deliver(p, handle, b); err != nil {
			t.Fatal(err)
		}
		// The first HTTP ack was refused: no tailAckMsg is sent for it.
	}
	f.reset()
	p.send(tailAckMsg{})
	p.idle()
	if f.count("Matrix") != 2 || f.count("Inbox") != 0 || f.count("Threads") != 0 {
		t.Fatalf("paid ack should refresh both delivery threads only: matrix=%d inbox=%d threads=%d", f.count("Matrix"), f.count("Inbox"), f.count("Threads"))
	}
	f.reset()
	p.send(tailAckMsg{})
	p.idle()
	if f.count("Matrix") != 0 {
		t.Fatal("a second paid ack reloaded already covered threads")
	}
}
