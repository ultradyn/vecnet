// Package audit appends rows to the hub's append-only audit table
// (PROTOCOL §12). Message bodies and secrets never enter an audit row: Record
// refuses any detail carrying a forbidden key, at any depth.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
)

// ErrForbiddenDetail reports a detail map that carries a body or secret key.
var ErrForbiddenDetail = errors.New("audit: detail carries a forbidden key")

// forbidden lists the lower-cased detail keys that would carry a message body
// or a secret into the audit table.
var forbidden = map[string]bool{"text": true, "body": true, "secret": true, "token": true}

// Record appends one audit row inside tx. targets may be nil ([]); detail may
// be nil ({}). It returns an error wrapping ErrForbiddenDetail, and writes
// nothing, when detail contains any of the keys "text", "body", "secret" or
// "token" (case-insensitive) at any nesting depth.
func Record(ctx context.Context, tx *sql.Tx, actor, action string, targets []string, detail map[string]any) error {
	if key, ok := findForbidden(detail); ok {
		return fmt.Errorf("%w: %q (action %s)", ErrForbiddenDetail, key, action)
	}
	if targets == nil {
		targets = []string{}
	}
	if detail == nil {
		detail = map[string]any{}
	}
	tj, err := json.Marshal(targets)
	if err != nil {
		return fmt.Errorf("audit: marshal targets: %w", err)
	}
	dj, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("audit: marshal detail: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit(at, actor, action, target_ids, detail) VALUES (?, ?, ?, ?, ?)`,
		envelope.FormatTime(time.Now()), actor, action, string(tj), string(dj)); err != nil {
		return fmt.Errorf("audit: insert %s: %w", action, err)
	}
	return nil
}

// findForbidden walks maps and slices and returns the first forbidden key.
func findForbidden(v any) (string, bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, sub := range x {
			if forbidden[strings.ToLower(k)] {
				return k, true
			}
			if key, ok := findForbidden(sub); ok {
				return key, true
			}
		}
	case map[string]string:
		for k := range x {
			if forbidden[strings.ToLower(k)] {
				return k, true
			}
		}
	case []any:
		for _, sub := range x {
			if key, ok := findForbidden(sub); ok {
				return key, true
			}
		}
	}
	return "", false
}
