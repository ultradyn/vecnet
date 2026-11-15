package hub

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/ids"
)

// SendWarning is a soft advisory attached to a successful send. It contains no
// history details: a sender need not be able to read the group's other threads.
type SendWarning struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	GroupID       string `json:"group_id"`
	WindowSeconds int    `json:"window_seconds"`
	Threshold     int    `json:"threshold"`
}

const rapidThreadWindow = time.Minute
const rapidThreadThreshold = 2

// LIMIT bounds evidence work even for very active groups; the covering
// (group_id, created_at) index bounds the lookup to this rolling window.
const recentGroupThreadCreationsSQL = `SELECT 1 FROM group_thread_creations
 WHERE group_id=? AND created_at>? AND created_at<=? LIMIT 3`

// recordGroupThreadCreations runs only for new threads, after send validation,
// in the same transaction as the message and stored idempotency response.
// SQLite's writer lock serialises concurrent successful creations. Any later
// failure rolls back both this evidence and the message it describes.
func recordGroupThreadCreations(ctx context.Context, tx *sql.Tx, threadID string, addressed []string, now time.Time) ([]SendWarning, error) {
	var warnings []SendWarning
	seen := make(map[string]bool)
	at := envelope.FormatTime(now)
	since := envelope.FormatTime(now.Add(-rapidThreadWindow))
	for _, group := range addressed {
		if !ids.IsGroup(group) || seen[group] {
			continue
		}
		seen[group] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_thread_creations(group_id,thread_id,created_at) VALUES(?,?,?)`, group, threadID, at); err != nil {
			return nil, fmt.Errorf("hub: record group thread creation: %w", err)
		}
		n, err := recentGroupThreadCreations(ctx, tx, group, since, at)
		if err != nil {
			return nil, err
		}
		if n > rapidThreadThreshold {
			warnings = append(warnings, SendWarning{
				Code:          "rapid_thread_creation",
				Message:       "Send succeeded. This group has received more than 2 new threads in 60 seconds. For related work, reply to or reuse an existing thread; distinct topics and legitimate batches are welcome.",
				GroupID:       group,
				WindowSeconds: 60,
				Threshold:     rapidThreadThreshold,
			})
		}
	}
	return warnings, nil
}

func recentGroupThreadCreations(ctx context.Context, tx *sql.Tx, group, since, now string) (int, error) {
	rows, err := tx.QueryContext(ctx, recentGroupThreadCreationsSQL, group, since, now)
	if err != nil {
		return 0, fmt.Errorf("hub: recent group thread creations: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("hub: recent group thread creations: %w", err)
	}
	return n, nil
}
