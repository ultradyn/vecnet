package push

import (
	"context"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
)

// noteAttempt records one delivery push that was handed to the HTTP client.
// NoteRouteAttempt swallows a store error, so a stats failure cannot change
// the delivery result already written by PushResult or GrokbotPushResult.
func (w *Worker) noteAttempt(ctx context.Context, key, kind string, body, status int, latency time.Duration, netErr error, ok bool, failText string) {
	if key == "" {
		return
	}
	st := 0
	if netErr == nil {
		st = status
	}
	if ok {
		failText = ""
	} else if failText == "" {
		failText = hub.RouteAttemptError(status, netErr)
	}
	w.Hub.NoteRouteAttempt(context.WithoutCancel(ctx), hub.RouteAttempt{
		Key: key, Kind: kind, BodyBytes: body,
		Latency: latency, HasLatency: true,
		OK: ok, Status: st, Error: failText, At: w.now(),
	})
}
