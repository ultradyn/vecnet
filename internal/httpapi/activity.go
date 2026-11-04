package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
)

// opActivity is GET /v1/admin/activity (PROTOCOL §2.13 "Activity feed"): an
// operator-only SSE stream of committed thread activity, metadata only. It is
// ephemeral and lossy: nothing is replayed, Last-Event-ID is ignored, and a
// subscriber that falls behind gets "event: resync" in place of what it
// missed. The first frame is "event: ready"; clients refetch their thread
// lists then and on every resync. The credential is checked again on each
// heartbeat, so rotation ends the stream.
func (s *server) opActivity(w http.ResponseWriter, r *http.Request) {
	activeStreams.Add(1)
	defer activeStreams.Add(-1)
	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	if s.Stop != nil {
		go func() {
			select {
			case <-s.Stop:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	token := bearer(r)
	sub := s.Hub.Activity.Subscribe(ctx)
	conn := startSSE(w, s.sseStall())
	defer conn.close()
	if !conn.send(ctx, []byte("retry: 3000\n\n")) || !conn.send(ctx, []byte("event: ready\ndata: {}\n\n")) {
		return
	}
	hb := time.NewTicker(s.heartbeat())
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-conn.failed():
			return
		case <-hb.C:
			if c, err := s.Hub.Auth.Resolve(ctx, token); err != nil || c.Kind != "operator" {
				return
			}
			if !conn.send(ctx, []byte(": hb\n\n")) {
				return
			}
		case ev := <-sub.C:
			if sub.TakeLost() {
				// What is queued predates the gap; the refetch covers it.
				for drained := false; !drained; {
					select {
					case <-sub.C:
					default:
						drained = true
					}
				}
				if !conn.send(ctx, []byte("event: resync\ndata: {}\n\n")) {
					return
				}
				continue
			}
			f, err := activityFrame(ev)
			if err != nil || !conn.send(ctx, f) {
				return
			}
		}
	}
}

// activityFrame renders "event: <type>\ndata: <json>\n\n" with no id: the
// feed has no cursor to resume from.
func activityFrame(ev hub.ActivityEvent) ([]byte, error) {
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	return []byte("event: " + ev.Type + "\ndata: " + string(data) + "\n\n"), nil
}
