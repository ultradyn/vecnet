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

	"github.com/XertroV/vecnet/internal/envelope"
)

// Grok Bot routine routes (PROTOCOL §13.3, docs/research/grokbot.md): the
// hub POSTs the Delivery with the routine's bearer key. There is no
// signature and no verification challenge, and the routine answers with a
// run id only, never a reply.

// GrokbotURLPattern is what a routine URL must match. Tests relax it to
// reach an httptest server; production keeps this value.
var GrokbotURLPattern = regexp.MustCompile(`^https://api2\.cursor\.sh/automations/webhook/[0-9a-f-]{36}$`)

const grokbotKeyPrefix = "crsr_"

// Outcomes of a routine POST (ClassifyGrokbot).
const (
	GrokbotStarted       = "started"        // a run began; not a reply, not a receipt of processing
	GrokbotRetry         = "retry"          // transient: back off like a 503
	GrokbotTerminalAlert = "terminal_alert" // the key or the routine is dead: fail and alert
	GrokbotTerminal      = "terminal"       // anything else: dead letter
)

const (
	// GrokbotBudgetWindow is the trailing window of runs_per_hour.
	GrokbotBudgetWindow = time.Hour
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
	if ownerKind != "peer" && ownerKind != "principal" {
		return "", invalidRouteField("owner_kind", "a route is owned by a peer or a principal")
	}
	if !GrokbotURLPattern.MatchString(routineURL) {
		return "", invalidURL("url", errors.New("not a Grok Bot routine webhook URL"))
	}
	if _, uerr := h.EgressPolicy().CheckURL(routineURL); uerr != nil {
		return "", invalidURL("url", uerr)
	}
	if !validGrokbotKey(key) {
		e := E(http.StatusUnprocessableEntity, "invalid_key", "a routine key is printable ASCII starting crsr_")
		e.Details["field"] = "key"
		return "", e
	}
	sealed, err := h.Seal([]byte(key))
	if err != nil {
		return "", err
	}
	err = h.DB.Tx(ctx, func(tx *sql.Tx) error {
		routeID, _, err = h.insertRoute(ctx, tx, newRoute{
			ownerKind: ownerKind, ownerID: ownerID, kind: RouteKindGrokbot, url: routineURL,
			keyEnc: sealed, verified: true,
		})
		return err
	})
	if err != nil {
		return "", err
	}
	return routeID, nil
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

// GrokbotBudgetOK takes one run from routeID's budget if fewer than
// runs_per_hour runs fall in the hour before now, and returns true. Taking
// the run in the same transaction as the count is what holds the budget
// when a batch of pushes checks it concurrently; a push that turns out not
// to start a run gives it back with ReleaseGrokbotRun.
//
// Over budget it returns false and the wait until a run leaves the window.
// A database failure is also false, with a short wait.
func (h *Hub) GrokbotBudgetOK(ctx context.Context, routeID string, now time.Time) (bool, time.Duration) {
	ok, wait := false, time.Duration(0)
	err := h.DB.Tx(ctx, func(tx *sql.Tx) error {
		ok, wait = false, 0
		if _, err := tx.ExecContext(ctx, `DELETE FROM route_runs WHERE route_id = ? AND at <= ?`,
			routeID, envelope.FormatTime(now.Add(-GrokbotBudgetWindow))); err != nil {
			return fmt.Errorf("hub: prune route runs: %w", err)
		}
		var limit, used int
		if err := tx.QueryRowContext(ctx,
			`SELECT r.runs_per_hour, (SELECT count(*) FROM route_runs u WHERE u.route_id = r.route_id)
			 FROM routes r WHERE r.route_id = ?`, routeID).Scan(&limit, &used); err != nil {
			return fmt.Errorf("hub: route budget: %w", err)
		}
		if used < limit {
			ok = true
			if _, err := tx.ExecContext(ctx, `INSERT INTO route_runs(route_id, at) VALUES (?, ?)`,
				routeID, envelope.FormatTime(now)); err != nil {
				return fmt.Errorf("hub: record route run: %w", err)
			}
			return nil
		}
		// The run whose departure brings used below limit.
		var at string
		err := tx.QueryRowContext(ctx,
			`SELECT at FROM route_runs WHERE route_id = ? ORDER BY at LIMIT 1 OFFSET ?`, routeID, max(used-limit, 0)).Scan(&at)
		if errors.Is(err, sql.ErrNoRows) {
			wait = GrokbotBudgetWindow // limit <= 0: nothing to wait for but the operator
			return nil
		}
		if err != nil {
			return fmt.Errorf("hub: route budget: %w", err)
		}
		t, err := envelope.ParseTime(at)
		if err != nil {
			return fmt.Errorf("hub: route budget: %w", err)
		}
		wait = t.Add(GrokbotBudgetWindow).Sub(now)
		return nil
	})
	if err != nil {
		slog.Error("hub: grokbot budget", "route", routeID, "err", err)
		return false, grokbotBudgetErrWait
	}
	return ok, wait
}

// ReleaseGrokbotRun gives back the run GrokbotBudgetOK took at at: the POST
// never went out, or the routine refused it (any non-200 means no run
// started, grokbot.md §2).
func (h *Hub) ReleaseGrokbotRun(ctx context.Context, routeID string, at time.Time) error {
	if _, err := h.DB.ExecContext(ctx,
		`DELETE FROM route_runs WHERE rowid = (SELECT rowid FROM route_runs WHERE route_id = ? AND at = ? LIMIT 1)`,
		routeID, envelope.FormatTime(at)); err != nil {
		return fmt.Errorf("hub: release route run: %w", err)
	}
	return nil
}

// ClassifyGrokbot reads a routine's answer (PROTOCOL §13.3, grokbot.md §2).
// Rate limits arrive as a 400, so a 400 is told apart by its body, in lower
// case. A 200 without "success":true is not a started run.
func ClassifyGrokbot(status int, body []byte) (outcome string) {
	switch {
	case status == http.StatusOK:
		var r struct {
			Success bool `json:"success"`
		}
		if json.Unmarshal(body, &r) == nil && r.Success {
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
// error too), terminal_alert is failed with counter push_auth_failed and a
// dead_letters notification, and terminal is dead_letter. body is the
// response body and is never stored.
func (h *Hub) GrokbotPushResult(ctx context.Context, d DueDelivery, status int, body []byte, retryAfter time.Duration, netErr error, now time.Time) error {
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
	return h.recordPush(ctx, d, outcome, status, retryAfter, netErr, now)
}
