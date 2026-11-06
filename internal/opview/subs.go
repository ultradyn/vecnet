package opview

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Subscriptions preserves the existing admin list fields and adds health at
// the hub's clock. Callback paths, credentials and ports are never exposed.
func (r *Reader) Subscriptions(ctx context.Context) ([]opapi.Subscription, error) {
	stored, err := events.NewStore(r.Hub).ListHealth(ctx)
	if err != nil {
		return nil, err
	}
	now := r.Hub.Now()
	ids := make([]string, len(stored))
	for i := range stored {
		ids[i] = stored[i].ID
	}
	usage, err := r.Hub.RouteUsages(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]opapi.Subscription, 0, len(stored))
	for _, h := range stored {
		host := ""
		if u, err := url.Parse(h.URL); err == nil {
			host = u.Hostname()
		}
		s := opapi.Subscription{
			SubID: h.ID, Principal: h.PrincipalKind + ":" + h.PrincipalID, PeerID: h.PeerID,
			URL: host, Name: h.Name, Cursor: h.Cursor.String(), ExpiresAt: h.ExpiresAt,
			VerifiedAt: h.VerifiedAt, PausedReason: h.PausedReason, LastRefreshAt: h.LastRefreshAt,
			LastDeliveryAt: h.LastDeliveryAt, LastStatus: h.LastStatus, Attempts: h.Attempts, RefreshBefore: h.RefreshBefore,
		}
		var expires *time.Time
		if h.ExpiresAt != nil {
			at, err := envelope.ParseTime(*h.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("operator subscription %s expiry: %w", h.ID, err)
			}
			expires = &at
			deadline := envelope.FormatTime(at.Add(-events.RefreshMargin))
			if s.RefreshBefore == nil {
				s.RefreshBefore = &deadline
			}
		}
		var refresh *time.Time
		if s.RefreshBefore != nil {
			at, err := envelope.ParseTime(*s.RefreshBefore)
			if err != nil {
				return nil, fmt.Errorf("operator subscription %s refresh: %w", h.ID, err)
			}
			refresh = &at
		}
		s.Health, s.HealthNote = subscriptionHealth(h, expires, refresh, now)
		if u, ok := usage[h.ID]; ok {
			s.Usage = wireUsage(u)
		}
		out = append(out, s)
	}
	return out, nil
}

func wireUsage(u hub.RouteUsage) *opapi.RouteUsage {
	return &opapi.RouteUsage{
		Requests: u.Requests, Successes: u.Successes, Failures: u.Failures,
		LastError: u.LastError, LastErrorAt: u.LastErrorAt, LastStatus: u.LastStatus,
		LastUsedAt: u.LastUsedAt, LastSuccessAt: u.LastSuccessAt,
		BodyBytes: opapi.RouteUsageDist{Avg: u.BodyBytes.Avg, P95: u.BodyBytes.P95, P99: u.BodyBytes.P99, Samples: u.BodyBytes.Samples},
		LatencyMS: opapi.RouteUsageDist{Avg: u.LatencyMS.Avg, P95: u.LatencyMS.P95, P99: u.LatencyMS.P99, Samples: u.LatencyMS.Samples},
	}
}

func subscriptionHealth(h events.Health, expires, refresh *time.Time, now time.Time) (string, string) {
	if h.PausedReason != nil {
		switch *h.PausedReason {
		case events.PausedTokenExpired:
			return opapi.HealthPausedToken, "Waiting for ChatGPT to re-subscribe with a fresh token. Nothing is lost; delivery resumes from the cursor."
		case events.PausedDeliveryFailed:
			return opapi.HealthPausedFailed, "One event used all 8 attempts. Fix the callback, then resume. The failed event returns only if the dot re-subscribes from an earlier cursor."
		case events.PausedOperator:
			return opapi.HealthPausedOperator, "You paused this subscription. A re-subscribe does not lift it."
		case events.PausedUnauthorized:
			return opapi.HealthPausedAuth, "The peer was disabled or moved, or its login was replaced or revoked. Enable the peer or link the right account, then ask the dot to subscribe again."
		case events.PausedCredentialRotated:
			return opapi.HealthPausedAuth, "The bearer that subscribed was rotated. The holder of the new bearer must subscribe again."
		default:
			return opapi.HealthPausedAuth, "Paused: " + *h.PausedReason + "."
		}
	}
	if h.VerifiedAt == nil {
		return opapi.HealthUnverified, "Waiting for callback verification. No events are sent until a subscribe challenge succeeds."
	}
	if expires != nil {
		if !now.Before(*expires) {
			return opapi.HealthExpired, "The subscription expired. Ask the dot to subscribe again; queued events resume from its cursor."
		}
		if refresh != nil && !now.Before(*refresh) {
			return opapi.HealthLate, "Refresh is due. ChatGPT must re-subscribe before expiry to keep delivery flowing."
		}
	}
	// A null status means no delivery has been attempted yet.
	if h.Attempts > 0 || (h.LastStatus != nil && (*h.LastStatus < 200 || *h.LastStatus >= 300)) {
		return opapi.HealthFailing, "Delivery is retrying. Check the callback; repeated failures pause the subscription."
	}
	return opapi.HealthOK, ""
}

// Attention is the subscription seam consumed by the inbox. Authentication
// failures rank first because delivery cannot recover without the holder's
// action; an intentional operator pause ranks last. Equal ranks retain the
// subscription-id ordering supplied by ListHealth.
func (r *Reader) Attention(ctx context.Context) ([]opapi.Attention, error) {
	subs, err := r.Subscriptions(ctx)
	if err != nil {
		return nil, err
	}
	out := []opapi.Attention{}
	at := envelope.FormatTime(r.Hub.Now())
	ranks := map[string]int{
		opapi.HealthPausedAuth: 0, opapi.HealthPausedToken: 1, opapi.HealthPausedFailed: 2,
		opapi.HealthExpired: 3, opapi.HealthLate: 4, opapi.HealthFailing: 5, opapi.HealthPausedOperator: 6,
	}
	for _, s := range subs {
		if _, needed := ranks[s.Health]; !needed {
			continue
		}
		action := ""
		if s.Health == opapi.HealthPausedFailed || s.Health == opapi.HealthPausedOperator {
			action = "resume"
		}
		// At records when the hub observed this computed health, not a guessed
		// transition time (the stored row has no health-transition timestamp).
		out = append(out, opapi.Attention{Kind: "subscription", ID: s.SubID, PeerID: s.PeerID,
			Health: s.Health, Summary: s.HealthNote, Action: action, At: at})
	}
	if len(out) == 0 {
		return out, nil
	}
	// Read names directly so disabled peers still have an actionable identity.
	rows, err := r.Hub.DB.QueryContext(ctx, `SELECT peer_id, name FROM peers`)
	if err != nil {
		return nil, fmt.Errorf("operator subscription peer names: %w", err)
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("operator subscription peer name: %w", err)
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("operator subscription peer names: %w", err)
	}
	for i := range out {
		name, found := names[out[i].PeerID]
		if !found {
			name = out[i].PeerID
		}
		out[i].Summary = name + " · " + out[i].Summary
	}
	sort.SliceStable(out, func(i, j int) bool { return ranks[out[i].Health] < ranks[out[j].Health] })
	return out, nil
}
