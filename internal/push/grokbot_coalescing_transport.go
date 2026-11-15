package push

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/XertroV/vecnet/internal/hub"
)

func (w *Worker) pushGrokbotWake(ctx context.Context, d hub.DueDelivery) {
	base := context.WithoutCancel(ctx)
	// All deterministically known preflight failures precede reservation.
	key, err := w.Hub.GrokbotKey(ctx, d.RouteID)
	if err == nil && len(d.Body) > hub.MaxPushBody {
		err = &hub.OutboundError{Reason: "body_too_large", Err: fmt.Errorf("push: body over cap")}
	}
	var req *http.Request
	if err == nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(d.Body))
	}
	if err != nil || ctx.Err() != nil {
		if e := w.Hub.DeferGrokbotPOST(base, d, err != nil && ctx.Err() == nil, w.now()); e != nil {
			slog.Error("push: defer Grok wake preflight", "err", e)
		}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Automation-Key", key)
	ok, wait, err := w.Hub.BeginGrokbotPOST(ctx, d, w.now())
	if !ok || err != nil {
		if e := w.Hub.DeferPush(base, d, w.now().Add(wait)); e != nil {
			slog.Error("push: defer Grok wake", "err", e)
		}
		return
	}
	if ctx.Err() != nil {
		// No payload client has been entered, so exact-token compensation is safe.
		if e := w.Hub.CancelUninvokedGrokbotPOST(base, d, w.now()); e != nil {
			slog.Error("push: defer Grok wake cancellation", "err", e)
		}
		return
	}
	var answer bytes.Buffer
	observation := hub.RouteAttemptObservation{RouteID: d.RouteID, DeliveryID: d.DeliveryID, Kind: hub.RouteKindGrokbot, BodyBytes: len(d.Body)}
	status, retryAfter, netErr := w.do(req, &answer, &observation)
	observation.HTTPStatus, observation.NetError = status, netErr
	observation.GrokClassification = hub.ClassifyGrokbot(status, answer.Bytes())
	defer func() { w.Hub.NoteRouteAttempt(base, observation) }()
	if ctx.Err() != nil && netErr != nil {
		return
	} // ambiguity: preserve reservation/lease
	if err := w.Hub.GrokbotWakePushResult(base, d, status, answer.Bytes(), retryAfter, netErr, w.now()); err != nil {
		slog.Error("push: record Grok wake result", "err", err)
	}
	w.Kick()
}
