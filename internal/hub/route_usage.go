package hub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/store"
)

// RouteAttemptObservation belongs to one payload invocation. It contains no
// request URL, keys, headers or response content. GrokClassification comes
// directly from ClassifyGrokbot over the existing bounded response buffer.
type RouteAttemptObservation struct {
	RouteID, DeliveryID, Kind string
	Attempted                 bool
	HTTPStatus                int
	NetError                  error
	GrokClassification        string
	BodyBytes                 int
	Elapsed                   time.Duration
	CompletedAt               time.Time
}

// NoteRouteAttempt is the best-effort boundary after authoritative settlement.
func (h *Hub) NoteRouteAttempt(ctx context.Context, a RouteAttemptObservation) {
	if !a.Attempted {
		return
	}
	outcome, class := routeAttemptOutcome(a)
	row := store.RouteAttemptInsert{
		RouteID: a.RouteID, DeliveryID: a.DeliveryID,
		At: envelope.FormatTime(a.CompletedAt), Outcome: outcome,
		BodyBytes: a.BodyBytes, LatencyMS: int(a.Elapsed.Round(time.Millisecond) / time.Millisecond),
	}
	if a.HTTPStatus >= 100 && a.HTTPStatus <= 599 {
		row.HTTPStatus = &a.HTTPStatus
	}
	if class != "" {
		row.ErrorClass = &class
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := store.InsertRouteAttempt(bounded, h.DB, row, h.Now()); err != nil {
		stage := "normalize"
		var failure *store.RouteAttemptWriteError
		if errors.As(err, &failure) {
			stage = failure.Stage
		}
		// No raw driver error or SQL. IDs are optional unless their bytes are
		// safe for diagnostics; these are observations, not trusted selectors.
		slog.Warn("route attempt telemetry failed", "stage", stage, "code", "write_failed",
			"route", routeAttemptLogID(a.RouteID), "delivery", routeAttemptLogID(a.DeliveryID))
	}
}

func routeAttemptLogID(id string) string {
	if len(id) == 0 || len(id) > 80 {
		return ""
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return ""
		}
	}
	return id
}

func routeAttemptOutcome(a RouteAttemptObservation) (outcome, class string) {
	if a.NetError != nil {
		// EgressErr preserves arbitrary OutboundError.Reason for older callers.
		// Telemetry accepts only its closed network enumeration.
		class = EgressErr(a.NetError)
		switch class {
		case "redirect_refused", "private_address", "scheme_refused", "userinfo_refused", "invalid_url", "canceled", "timeout", "dns_error", "tls_error", "connection_refused", "connection_reset", "connection_closed", "network_error", "request_failed":
		default:
			class = "request_failed"
		}
		return "fail", class
	}
	status := a.HTTPStatus
	if a.Kind == RouteKindGrokbot {
		switch a.GrokClassification {
		case GrokbotStarted:
			return "ok", ""
		case GrokbotTerminalAlert:
			return "refused", "routine_auth"
		case GrokbotRetry:
			if status == 400 {
				return "fail", "routine_rate_limited"
			}
			return "fail", routeAttemptStatusClass(status)
		default:
			if status == 200 {
				return "refused", "routine_not_started"
			}
			return "refused", routeAttemptStatusClass(status)
		}
	}
	if status >= 200 && status < 300 {
		return "ok", ""
	}
	class = routeAttemptStatusClass(status)
	if a.Kind == "events" {
		if status == 410 || status == 413 {
			return "refused", class
		}
		return "fail", class
	}
	if result := classifyPush(status, a.NetError); result == pushConflict || result == pushTerminal {
		return "refused", class
	}
	return "fail", class
}

func routeAttemptStatusClass(status int) string {
	switch status {
	case 408:
		return "http_408"
	case 409:
		return "http_409"
	case 410:
		return "http_410"
	case 413:
		return "http_413"
	case 429:
		return "http_429"
	}
	switch {
	case status >= 300 && status < 400:
		return "http_3xx"
	case status >= 400 && status < 500:
		return "http_4xx"
	case status >= 500 && status < 600:
		return "http_5xx"
	default:
		return "http_status"
	}
}
