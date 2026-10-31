package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/eventlog"
	"github.com/XertroV/vecnet/internal/ids"
	"github.com/XertroV/vecnet/internal/webhook"
)

// Operator notification webhooks (PROTOCOL §5.2 "Operator peer", §2.12
// PUT /v1/admin/notify, [T15, R23]).

// Notification classes: the filter a notify route subscribes to, besides
// "all".
const (
	NotifyWake        = "wake"         // a wake delivery to the operator peer
	NotifyApprovals   = "approvals"    // an approval_request addressed to the operator
	NotifyDeadLetters = "dead_letters" // a dead_letter or push_conflict receipt
)

// notifyFilters are the notify_routes.filter values.
var notifyFilters = map[string]bool{NotifyWake: true, NotifyApprovals: true, NotifyDeadLetters: true, "all": true}

// notifyPrefix tags notify route ids ("ntf_<ULID>"); operator config, not a
// wire id kind.
const notifyPrefix ids.Prefix = "ntf"

// Notification delivery limits.
const (
	// MaxNotifyInFlight bounds the notification goroutines; a notification
	// that finds them all busy is dropped (logged), never queued, so the
	// caller never blocks.
	MaxNotifyInFlight = 32
	// NotifyTimeout is the per-request timeout of a notification POST.
	NotifyTimeout = 10 * time.Second
)

// DefaultNotifyRetry is the wait before each of the 3 retries of a
// notification that did not get a 2xx: 30 s in all.
var DefaultNotifyRetry = []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}

// NotifyRouteSpec is one route of a PUT /v1/admin/notify body.
type NotifyRouteSpec struct {
	URL    string
	Filter string
}

// NotifyRoute is a stored operator notification route with its key.
type NotifyRoute struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Filter string `json:"filter"`
	Whsec  string `json:"whsec"`
}

// SetNotifyRoutes replaces the operator's notification routes with specs
// and returns them in order. A URL that already had a route keeps its id
// and whsec_ (only its filter changes); every other URL gets a fresh id and
// whsec_. A filter outside wake|approvals|dead_letters|all, a URL that is
// not absolute http(s), or the same URL twice is 422 invalid_value.
func (h *Hub) SetNotifyRoutes(ctx context.Context, specs []NotifyRouteSpec) ([]NotifyRoute, error) {
	seen := map[string]bool{}
	for i, s := range specs {
		invalid := func(field, msg string) error {
			e := E(http.StatusUnprocessableEntity, "invalid_value", msg)
			e.Details["field"] = "routes/" + strconv.Itoa(i) + "/" + field
			return e
		}
		if !notifyFilters[s.Filter] {
			return nil, invalid("filter", "filter is one of wake, approvals, dead_letters, all")
		}
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, invalid("url", "a notify URL is an absolute http(s) URL without credentials")
		}
		if seen[s.URL] {
			return nil, invalid("url", "each notify URL appears once")
		}
		seen[s.URL] = true
	}
	out := make([]NotifyRoute, 0, len(specs))
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		out = out[:0]
		old := map[string]NotifyRoute{}
		rows, err := tx.QueryContext(ctx, `SELECT id, url, filter, whsec FROM notify_routes`)
		if err != nil {
			return fmt.Errorf("hub: notify routes: %w", err)
		}
		for rows.Next() {
			var r NotifyRoute
			if err := rows.Scan(&r.ID, &r.URL, &r.Filter, &r.Whsec); err != nil {
				rows.Close()
				return fmt.Errorf("hub: notify routes: %w", err)
			}
			old[r.URL] = r
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("hub: notify routes: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM notify_routes`); err != nil {
			return fmt.Errorf("hub: clear notify routes: %w", err)
		}
		now := h.nowStr()
		for _, s := range specs {
			r, kept := old[s.URL]
			if !kept {
				r = NotifyRoute{ID: ids.New(notifyPrefix), URL: s.URL, Whsec: webhook.NewSecret()}
			}
			r.Filter = s.Filter
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO notify_routes(id, url, filter, whsec, created_at) VALUES (?, ?, ?, ?, ?)`,
				r.ID, r.URL, r.Filter, r.Whsec, now); err != nil {
				return fmt.Errorf("hub: insert notify route: %w", err)
			}
			out = append(out, r)
		}
		routeIDs := make([]string, len(out))
		for i, r := range out {
			routeIDs[i] = r.ID
		}
		return audit.Record(ctx, tx, "operator", "notify_set", routeIDs, map[string]any{"routes": len(out)})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Notify POSTs ev as JSON to every notify route whose filter is class or
// "all", signed with that route's whsec_ (Standard Webhooks, webhook-id =
// ev.Cursor). It is fire-and-forget: each POST runs in its own goroutine,
// bounded by MaxNotifyInFlight (beyond it the notification is dropped and
// logged), retried 3 times over 30 s on a non-2xx or network error, and
// never blocks the caller beyond reading the routes.
//
// Triggers (the hub calls Notify itself): a delivery Event to the operator
// peer with priority wake → "wake"; an approval_request addressed to the
// operator → "approvals" (and "wake" when wake); a dead_letter or
// push_conflict receipt → "dead_letters".
func (h *Hub) Notify(ctx context.Context, ev envelope.Event, class string) {
	h.notify(ctx, ev, class)
}

// notify is Notify for an Event of several classes at once: each matching
// route gets it once.
func (h *Hub) notify(ctx context.Context, ev envelope.Event, classes ...string) {
	ctx = context.WithoutCancel(ctx)
	rows, err := h.DB.QueryContext(ctx, `SELECT id, url, filter, whsec FROM notify_routes ORDER BY id`)
	if err != nil {
		slog.Error("hub: notify routes", "err", err)
		return
	}
	var routes []NotifyRoute
	for rows.Next() {
		var r NotifyRoute
		if err := rows.Scan(&r.ID, &r.URL, &r.Filter, &r.Whsec); err != nil {
			rows.Close()
			slog.Error("hub: notify routes", "err", err)
			return
		}
		if r.Filter == "all" || slices.Contains(classes, r.Filter) {
			routes = append(routes, r)
		}
	}
	rows.Close()
	if len(routes) == 0 {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		slog.Error("hub: notify encode", "err", err)
		return
	}
	id := ev.Cursor
	if id == "" {
		id = ids.New(notifyPrefix)
	}
	for _, r := range routes {
		select {
		case h.notifySem <- struct{}{}:
		default:
			slog.Warn("hub: notification dropped, too many in flight", "route", r.ID, "type", ev.Type)
			continue
		}
		h.notifyWG.Add(1)
		go func() {
			defer h.notifyWG.Done()
			defer func() { <-h.notifySem }()
			h.postNotify(ctx, r, id, body)
		}()
	}
}

// postNotify sends one notification, retrying per NotifyRetry.
func (h *Hub) postNotify(ctx context.Context, r NotifyRoute, id string, body []byte) {
	delays := h.NotifyRetry
	if delays == nil {
		delays = DefaultNotifyRetry
	}
	client := h.NotifyClient
	if client == nil {
		client = defaultNotifyClient
	}
	var lastErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if attempt > 0 {
			t := time.NewTimer(delays[attempt-1])
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		if lastErr = h.postNotifyOnce(ctx, client, r, id, body); lastErr == nil {
			return
		}
	}
	slog.Warn("hub: notification failed", "route", r.ID, "err", lastErr)
}

func (h *Hub) postNotifyOnce(ctx context.Context, client *http.Client, r NotifyRoute, id string, body []byte) error {
	now := h.Now()
	sig, err := webhook.Sign(r.Whsec, id, now, body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webhook.HeaderID, id)
	req.Header.Set(webhook.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(webhook.HeaderSignature, sig)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, PushResponseCap))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("HTTP " + strconv.Itoa(resp.StatusCode))
	}
	return nil
}

// defaultNotifyClient never follows redirects and times out per request.
var defaultNotifyClient = &http.Client{
	Timeout: NotifyTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// notifyOperatorDeliveries fires the operator notifications for message
// msgID after its send committed: each Delivery to an operator peer with
// priority wake is class "wake"; one of an approval_request is
// "approvals".
func (h *Hub) notifyOperatorDeliveries(ctx context.Context, msgID string) {
	var has bool
	if err := h.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM notify_routes)`).Scan(&has); err != nil || !has {
		return
	}
	rows, err := h.DB.QueryContext(ctx,
		`SELECT e.seq, e.type, e.at, e.data, d.priority, m.kind
		 FROM deliveries d
		 JOIN peers p ON p.peer_id = d.recipient AND p.kind = 'operator'
		 JOIN events e ON e.seq = d.event_seq
		 JOIN messages m ON m.msg_id = d.msg_id
		 WHERE d.msg_id = ?
		 ORDER BY e.seq`, msgID)
	if err != nil {
		slog.Error("hub: operator notifications", "err", err)
		return
	}
	type pending struct {
		ev      envelope.Event
		classes []string
	}
	var out []pending
	for rows.Next() {
		var seq int64
		var ev envelope.Event
		var data, priority, kind string
		if err := rows.Scan(&seq, &ev.Type, &ev.At, &data, &priority, &kind); err != nil {
			slog.Error("hub: operator notifications", "err", err)
			break
		}
		ev.Cursor = eventlog.Cursor{Epoch: h.Log.Epoch(), Seq: seq}.String()
		ev.Data = json.RawMessage(data)
		var classes []string
		if priority == "wake" {
			classes = append(classes, NotifyWake)
		}
		if kind == "approval_request" {
			classes = append(classes, NotifyApprovals)
		}
		if len(classes) > 0 {
			out = append(out, pending{ev, classes})
		}
	}
	rows.Close()
	for _, p := range out {
		h.notify(ctx, p.ev, p.classes...)
	}
}
