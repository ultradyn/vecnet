package push

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/XertroV/vecnet/internal/hub"
)

// pushGrokbot POSTs d.Body, the Delivery JSON, to the routine with its
// bearer key, on the egress client and without signature headers (PROTOCOL
// §13.3), and records the result with Hub.GrokbotPushResult.
//
// The budget comes first: when exhausted the delivery is requeued until a
// run leaves the window, which is not an attempt. A POST the routine refuses
// gives its run back, since a non-200 means none started; a network error
// keeps it, since one may have. The key and the routine's answer never reach
// a log or a stored error.
func (w *Worker) pushGrokbot(ctx context.Context, d hub.DueDelivery) {
	w.init()
	base := context.WithoutCancel(ctx)
	now := w.now()
	ok, wait := w.Hub.GrokbotBudgetOK(ctx, d.RouteID, now)
	if !ok {
		if err := w.Hub.DeferPush(base, d, now.Add(wait)); err != nil {
			slog.Error("push: grokbot budget requeue", "delivery", d.DeliveryID, "err", err)
		}
		return
	}
	release := func() {
		if err := w.Hub.ReleaseGrokbotRun(base, d.RouteID, now); err != nil {
			slog.Error("push: grokbot release run", "route", d.RouteID, "err", err)
		}
	}

	key, err := w.Hub.GrokbotKey(ctx, d.RouteID)
	if err == nil && len(d.Body) > hub.MaxPushBody {
		err = &hub.OutboundError{Reason: "body_too_large", Err: fmt.Errorf("push: body is %d bytes, over the %d-byte cap", len(d.Body), hub.MaxPushBody)}
	}
	if err != nil { // nothing went out
		release()
		if ctx.Err() != nil {
			return
		}
		slog.Error("push: grokbot not sent", "delivery", d.DeliveryID, "route", d.RouteID, "err", err)
		w.grokbotResult(ctx, d, 0, nil, 0, err)
		return
	}

	var answer bytes.Buffer
	status, retryAfter, latency, sent, netErr := w.postGrokbot(ctx, d, key, &answer)
	if ctx.Err() != nil && netErr != nil {
		// Shutting down: leave the lease to lapse rather than burn an attempt.
		return
	}
	if netErr == nil && status != http.StatusOK {
		release()
	}
	if netErr == nil && hub.ClassifyGrokbot(status, answer.Bytes()) == hub.GrokbotTerminalAlert {
		slog.Warn("push: grokbot routine refused the key or is disabled",
			"delivery", d.DeliveryID, "route", d.RouteID, "status", status)
	}
	if sent {
		d.RecordAttempt = true
		d.AttemptBody = len(d.Body)
		d.AttemptLatency = latency
	}
	w.grokbotResult(ctx, d, status, answer.Bytes(), retryAfter, netErr)
}

// postGrokbot sends one routine POST; the first 4 KiB of the answer go to sink.
func (w *Worker) postGrokbot(ctx context.Context, d hub.DueDelivery, key string, sink *bytes.Buffer) (status int, retryAfter time.Duration, latency time.Duration, sent bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(d.Body))
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("push: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Automation-Key", key) // undocumented; local senders send both (grokbot.md §1a)
	status, retryAfter, latency, err = w.do(req, sink)
	return status, retryAfter, latency, true, err
}

func (w *Worker) grokbotResult(ctx context.Context, d hub.DueDelivery, status int, body []byte, retryAfter time.Duration, netErr error) {
	if err := w.Hub.GrokbotPushResult(context.WithoutCancel(ctx), d, status, body, retryAfter, netErr, w.now()); err != nil {
		slog.Error("push: record result", "delivery", d.DeliveryID, "err", err)
	}
	w.Kick()
}
