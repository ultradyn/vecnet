package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/events"
	"github.com/XertroV/vecnet/internal/hub"
)

const subscriptionSetupHint = "Event subscription is not active. You, the model, cannot subscribe: there is no model tool for it. Tell the user: Open the vecnet plugin page in ChatGPT and confirm vecnet.message appears beside the tools (if missing, Rescan). Then in a Work chat (web, or desktop with Work + Cloud) or a dot, ask: \"Subscribe to the vecnet.message event from the vecnet plugin with no arguments, and when an event arrives, read it and reply via vecnet.\" Workspace controls for plugins and event-triggered tasks must allow it. Never claim you subscribed. Use vecnet_inbox meanwhile, and call vecnet_subscriptions again to confirm."

type subscriptionStatus struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Arguments      map[string]any `json:"arguments"`
	CreatedAt      string         `json:"created_at"`
	RefreshBefore  *string        `json:"refresh_before"`
	ExpiresAt      *string        `json:"expires_at"`
	Paused         bool           `json:"paused"`
	Verified       bool           `json:"verified"`
	PausedReason   *string        `json:"paused_reason"`
	LastDeliveryAt *string        `json:"last_delivery_at"`
	LastStatus     *int           `json:"last_status"`
	CallbackHost   string         `json:"callback_host"`
}

type subscriptionsResult struct {
	Subscriptions []subscriptionStatus `json:"subscriptions"`
	Hint          string               `json:"hint,omitempty"`
}

// callSubscriptions scopes the query to the credential's own peer, including
// for operators. A principal without its own peer cannot select another one.
// Paused subscriptions remain visible until expiry so their cause is clear.
func callSubscriptions(ctx context.Context, s *Server, c auth.Caller, _ []byte) (any, error) {
	peer := c.PeerID
	if p := PrincipalFromContext(ctx); p.OAuth != nil {
		if s.ResolveDotPeer != nil {
			var err error
			peer, err = s.ResolveDotPeer(ctx, p.OAuth.PrincipalID)
			if err != nil {
				return nil, err
			}
		}
		if peer == "" {
			return nil, hub.E(http.StatusForbidden, "not_linked", "this account is not linked to a dot peer")
		}
	}
	result := subscriptionsResult{Subscriptions: []subscriptionStatus{}, Hint: subscriptionSetupHint}
	if peer == "" {
		return result, nil
	}
	now := s.now()
	rows, err := s.Events.DB.QueryContext(ctx,
		`SELECT sub_id, name, arguments, created_at, expires_at, paused_reason,
		        last_delivery_at, last_status, url, verified_at IS NOT NULL
		 FROM event_subscriptions WHERE peer_id = ? AND (expires_at IS NULL OR expires_at > ?)
		 ORDER BY created_at, sub_id`, peer, envelope.FormatTime(now))
	if err != nil {
		return nil, fmt.Errorf("mcp: subscriptions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sub subscriptionStatus
		var arguments, callback string
		if err := rows.Scan(&sub.ID, &sub.Name, &arguments, &sub.CreatedAt, &sub.ExpiresAt, &sub.PausedReason,
			&sub.LastDeliveryAt, &sub.LastStatus, &callback, &sub.Verified); err != nil {
			return nil, fmt.Errorf("mcp: subscription status: %w", err)
		}
		if err := json.Unmarshal([]byte(arguments), &sub.Arguments); err != nil {
			return nil, fmt.Errorf("mcp: subscription arguments: %w", err)
		}
		if sub.ExpiresAt != nil {
			expires, err := envelope.ParseTime(*sub.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("mcp: subscription expiry: %w", err)
			}
			refresh := expires.Add(-events.RefreshMargin)
			if refresh.Before(now) {
				refresh = now
			}
			formatted := envelope.FormatTime(refresh)
			sub.RefreshBefore = &formatted
		}
		// Paths, queries, userinfo and ports are deliberately omitted: callback
		// URLs can contain credentials. Never put the original URL in errors.
		if u, err := url.Parse(callback); err == nil {
			sub.CallbackHost = u.Hostname()
		}
		sub.Paused = sub.PausedReason != nil && *sub.PausedReason != ""
		result.Subscriptions = append(result.Subscriptions, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mcp: subscription status: %w", err)
	}
	if len(result.Subscriptions) > 0 {
		result.Hint = ""
	}
	return result, nil
}
