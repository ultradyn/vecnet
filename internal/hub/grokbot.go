package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/audit"
	"github.com/XertroV/vecnet/internal/envelope"
)

// Grok Bot routine routes (PROTOCOL §13.3, docs/research/grokbot.md): the
// hub POSTs the Delivery with the routine's bearer key. There is no
// signature and no verification challenge, and the routine answers with a
// run id only, never a reply.

// GrokbotURLPattern is what a routine URL must match. Tests relax it to
// reach an httptest server; production keeps this value. Onboarding uses
// grokbotOnboardURL instead, which requires UUID groups 8-4-4-4-12.
// Relaxing this pattern does not relax onboarding.
var GrokbotURLPattern = regexp.MustCompile(`^https://api2\.cursor\.sh/automations/webhook/[0-9a-f-]{36}$`)

// grokbotOnboardURL is the onboarding rule from Decisions §3. It is stricter
// than GrokbotURLPattern and is not used by route add or rotate.
var grokbotOnboardURL = regexp.MustCompile(`^https://api2\.cursor\.sh/automations/webhook/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const grokbotKeyPrefix = "crsr_"

// Outcomes of a routine POST (ClassifyGrokbot).
const (
	GrokbotStarted       = "started"        // a run began; not a reply, not a receipt of processing
	GrokbotRetry         = "retry"          // transient: back off like a 503
	GrokbotTerminalAlert = "terminal_alert" // the key or the routine is dead: dead letter and alert
	GrokbotTerminal      = "terminal"       // anything else: dead letter
)

const (
	// GrokbotBudgetWindow is the trailing window of both run budgets.
	GrokbotBudgetWindow = time.Hour
	// GrokbotAccountRunsPerHour caps runs across every grokbot route, the
	// per-account limit of grokbot.md §3. Routes carry no account identity,
	// so the cap is hub-wide: never too high, and too low only for a hub
	// whose routines belong to several accounts.
	GrokbotAccountRunsPerHour = 300
	// grokbotBudgetErrWait is how long a delivery waits when the budget could
	// not be read: fail closed, without spending an attempt.
	grokbotBudgetErrWait = time.Minute
)

// AddGrokbotRoute creates a grokbot route owned by a peer or a principal,
// with the same owner rules and queued-delivery backfill as AddRoute. The
// routine URL must match GrokbotURLPattern (422 invalid_url) and key must be
// a printable crsr_ key (422 invalid_key); neither error echoes the key.
// The key is sealed under meta.hub_key and never returned. The route is
// verified at once, since routines have no challenge, and it has no whsec_.
func (h *Hub) AddGrokbotRoute(ctx context.Context, ownerKind, ownerID, routineURL, key string) (routeID string, err error) {
	return h.AddGrokbotRouteWithTransport(ctx, ownerKind, ownerID, routineURL, key, GrokbotTransportDeliveryV1)
}

// AddGrokbotRouteWithTransport requires an explicit supported transport selection.
func (h *Hub) AddGrokbotRouteWithTransport(ctx context.Context, ownerKind, ownerID, routineURL, key, transport string) (routeID string, err error) {
	if err := validateGrokbotTransport(transport); err != nil {
		return "", err
	}
	if ownerKind != "peer" && ownerKind != "principal" {
		return "", invalidRouteField("owner_kind", "a route is owned by a peer or a principal")
	}
	sealed, err := h.prepareGrokbotRoute(routineURL, key, false)
	if err != nil {
		return "", err
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		routeID, _, err = h.insertRoute(ctx, tx, newRoute{
			ownerKind: ownerKind, ownerID: ownerID, kind: RouteKindGrokbot, url: routineURL,
			keyEnc: sealed, verified: true, grokbotTransport: transport,
		})
		return err
	})
	if err != nil {
		return "", err
	}
	return routeID, nil
}

func invalidGrokbotKey() *APIError {
	e := E(http.StatusUnprocessableEntity, "invalid_key", "a routine key is printable ASCII starting crsr_")
	e.Details["field"] = "key"
	return e
}

// prepareGrokbotRoute checks routineURL and key and returns the key sealed
// for routes.key_enc. strict selects the onboarding grouped-UUID rule.
// Route add and rotate pass false and keep GrokbotURLPattern and the raw-key
// contract. The error never includes the URL or the key.
func (h *Hub) prepareGrokbotRoute(routineURL, key string, strict bool) (string, error) {
	if strict {
		if !grokbotOnboardURL.MatchString(routineURL) {
			return "", invalidURL("url", errors.New("not a Grok Bot routine webhook URL"))
		}
	} else if !GrokbotURLPattern.MatchString(routineURL) {
		return "", invalidURL("url", errors.New("not a Grok Bot routine webhook URL"))
	}
	if _, uerr := h.EgressPolicy().CheckURL(routineURL); uerr != nil {
		return "", invalidURL("url", uerr)
	}
	return h.sealGrokbotKey(key)
}

// sealGrokbotKey seals a raw crsr_ key. Callers pass the normalized key, not
// an Authorization header. The error never includes the key.
func (h *Hub) sealGrokbotKey(key string) (string, error) {
	if !validGrokbotKey(key) {
		return "", invalidGrokbotKey()
	}
	return h.Seal([]byte(key))
}

// validGrokbotKey accepts crsr_ plus at least one visible ASCII character,
// so a key can never split the header it is sent in.
func validGrokbotKey(key string) bool {
	rest, ok := strings.CutPrefix(key, grokbotKeyPrefix)
	if !ok || rest == "" {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] <= ' ' || rest[i] > '~' {
			return false
		}
	}
	return true
}

// RotateGrokbotKey replaces routeID's routine key, for a key that went
// stale: every later push sends the new one, and a dead-lettered delivery
// reaches it through an operator retry. key is checked like AddGrokbotRoute's
// (422 invalid_key, never echoed). An unknown route is 404 not_found; an http
// route, which has no routine key, is 422 invalid_value.
func (h *Hub) RotateGrokbotKey(ctx context.Context, routeID, key string) error {
	sealed, err := h.sealGrokbotKey(key)
	if err != nil {
		return err
	}
	return h.DB.Tx(ctx, func(tx *sql.Tx) error {
		kind, err := routeKind(ctx, tx, routeID)
		if err != nil {
			return err
		}
		if kind != RouteKindGrokbot {
			return invalidRouteField("key", "only a grokbot route takes a key")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routes SET key_enc = ? WHERE route_id = ?`, sealed, routeID); err != nil {
			return fmt.Errorf("hub: replace routine key: %w", err)
		}
		return audit.Record(ctx, tx, "operator", "route_rotate", []string{routeID}, nil)
	})
}

// GrokbotKey unseals routeID's routine key at send time. Callers must never
// log it. A route that is not a grokbot route is 404 not_found.
func (h *Hub) GrokbotKey(ctx context.Context, routeID string) (string, error) {
	var sealed sql.NullString
	err := h.DB.QueryRowContext(ctx,
		`SELECT key_enc FROM routes WHERE route_id = ? AND kind = ?`, routeID, RouteKindGrokbot).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unknownRoute(routeID)
	}
	if err != nil {
		return "", fmt.Errorf("hub: grokbot key: %w", err)
	}
	plain, err := h.Unseal(sealed.String)
	if err != nil {
		return "", fmt.Errorf("hub: grokbot key of %s: %w", routeID, err)
	}
	return string(plain), nil
}

// GrokbotBudgetOK takes one run if fewer than routeID's runs_per_hour runs,
// and fewer than GrokbotAccountRunsPerHour runs across every route, fall in
// the hour before now, and returns true. Taking the run in the same
// transaction as the count is what holds the budget when a batch of pushes
// checks it concurrently; a push that turns out not to start a run gives it
// back with ReleaseGrokbotRun.
//
// Over budget it returns false and the wait until a run leaves the window
// (the later of the two, when both limits are reached). A database failure
// is also false, with a short wait.
func (h *Hub) GrokbotBudgetOK(ctx context.Context, routeID string, now time.Time) (bool, time.Duration) {
	ok, wait := false, time.Duration(0)
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		ok, wait = false, 0
		var err error
		ok, wait, err = takeGrokbotRun(ctx, tx, routeID, nil, now)
		return err
	})
	if err != nil {
		slog.Error("hub: grokbot budget", "route", routeID, "err", err)
		return false, grokbotBudgetErrWait
	}
	return ok, wait
}

// takeGrokbotRun shares the original rolling budgets with token-owned runs.
func takeGrokbotRun(ctx context.Context, tx *sql.Tx, routeID string, token any, now time.Time) (ok bool, wait time.Duration, err error) {
	err = func() error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM route_runs WHERE at <= ?`,
			envelope.FormatTime(now.Add(-GrokbotBudgetWindow))); err != nil {
			return fmt.Errorf("hub: prune route runs: %w", err)
		}
		var limit, used, total int
		if err := tx.QueryRowContext(ctx,
			`SELECT r.runs_per_hour,
			        (SELECT count(*) FROM route_runs u WHERE u.route_id = r.route_id),
			        (SELECT count(*) FROM route_runs)
			 FROM routes r WHERE r.route_id = ?`, routeID).Scan(&limit, &used, &total); err != nil {
			return fmt.Errorf("hub: route budget: %w", err)
		}
		if used < limit && total < GrokbotAccountRunsPerHour {
			ok = true
			if _, err := tx.ExecContext(ctx, `INSERT INTO route_runs(route_id, at, grok_wake_token) VALUES (?, ?, ?)`,
				routeID, envelope.FormatTime(now), token); err != nil {
				return fmt.Errorf("hub: record route run: %w", err)
			}
			return nil
		}
		if used >= limit {
			w, err := grokbotRunWait(ctx, tx, routeID, used-limit, now)
			if err != nil {
				return err
			}
			wait = max(wait, w)
		}
		if total >= GrokbotAccountRunsPerHour {
			w, err := grokbotRunWait(ctx, tx, "", total-GrokbotAccountRunsPerHour, now)
			if err != nil {
				return err
			}
			wait = max(wait, w)
		}
		return nil
	}()
	return ok, wait, err
}

// grokbotRunWait is the time until the run at index offset, oldest first,
// leaves the window; that is the run whose departure brings a count back
// under its limit. routeID "" means the runs of every route.
func grokbotRunWait(ctx context.Context, tx *sql.Tx, routeID string, offset int, now time.Time) (time.Duration, error) {
	var at string
	err := tx.QueryRowContext(ctx,
		`SELECT at FROM route_runs WHERE ?1 = '' OR route_id = ?1 ORDER BY at LIMIT 1 OFFSET ?2`,
		routeID, offset).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return GrokbotBudgetWindow, nil // limit <= 0: nothing to wait for but the operator
	}
	if err != nil {
		return 0, fmt.Errorf("hub: route budget: %w", err)
	}
	t, err := envelope.ParseTime(at)
	if err != nil {
		return 0, fmt.Errorf("hub: route budget: %w", err)
	}
	return t.Add(GrokbotBudgetWindow).Sub(now), nil
}

// ReleaseGrokbotRun gives back the run GrokbotBudgetOK took at at: the POST
// never went out, or the routine refused it (any non-200 means no run
// started, grokbot.md §2).
func (h *Hub) ReleaseGrokbotRun(ctx context.Context, routeID string, at time.Time) error {
	if _, err := h.DB.ExecContext(ctx,
		`DELETE FROM route_runs WHERE rowid = (SELECT rowid FROM route_runs WHERE route_id = ? AND at = ? AND grok_wake_token IS NULL LIMIT 1)`,
		routeID, envelope.FormatTime(at)); err != nil {
		return fmt.Errorf("hub: release route run: %w", err)
	}
	return nil
}

// ClassifyGrokbot reads a routine's answer (PROTOCOL §13.3, grokbot.md §2).
// Rate limits arrive as a 400, so a 400 is told apart by its body, in lower
// case. A 200 without "success":true is not a started run, unless the answer
// was too long to read whole: then it is ignored and the status counts (§3.3).
func ClassifyGrokbot(status int, body []byte) (outcome string) {
	switch {
	case status == http.StatusOK:
		var r struct {
			Success bool `json:"success"`
		}
		err := json.Unmarshal(body, &r)
		if (err == nil && r.Success) || (err != nil && len(body) >= PushResponseCap) {
			return GrokbotStarted
		}
		return GrokbotTerminal
	case status == http.StatusBadRequest:
		lower := bytes.ToLower(body)
		switch {
		case bytes.Contains(lower, []byte("rate limit")):
			return GrokbotRetry
		case bytes.Contains(lower, []byte("disabled")):
			return GrokbotTerminalAlert
		}
		return GrokbotTerminal
	case status == http.StatusUnauthorized:
		return GrokbotTerminalAlert
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests, status >= 500 && status < 600:
		return GrokbotRetry
	default:
		return GrokbotTerminal
	}
}

// GrokbotPushResult is PushResult for a routine POST, whose outcome depends
// on the response body: started is accepted, retry is as a 503 (a network
// error too), terminal is dead_letter, and terminal_alert is dead_letter
// plus counter push_auth_failed, so the operator can retry once the routine
// is back or its key replaced (§3.3, §5.5). body is the response body and is
// never stored.
func (h *Hub) GrokbotPushResult(ctx context.Context, d DueDelivery, status int, body []byte, retryAfter time.Duration, netErr error, now time.Time) error {
	return h.recordPush(ctx, d, grokbotPushOutcome(status, body, netErr), status, retryAfter, netErr, now)
}

func grokbotPushOutcome(status int, body []byte, netErr error) pushOutcome {
	outcome := pushRetry
	if netErr == nil {
		switch ClassifyGrokbot(status, body) {
		case GrokbotStarted:
			outcome = pushAccepted
		case GrokbotRetry:
		case GrokbotTerminalAlert:
			outcome = pushAuthFailed
		default:
			outcome = pushTerminal
		}
	}
	return outcome
}
