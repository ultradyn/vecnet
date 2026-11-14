package opview

import (
	"context"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
)

func (r *Reader) ThreadStats(ctx context.Context) (opapi.ThreadStats, error) {
	now := r.Hub.Now()
	out := opapi.ThreadStats{Now: envelope.FormatTime(now)}
	err := r.Hub.DB.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE archived_at IS NULL),count(*) FILTER (WHERE archived_at IS NOT NULL),count(*) FILTER (WHERE archived_at IS NULL AND last_message_at>=?),count(*) FILTER (WHERE archived_at IS NULL AND last_message_at>=?) FROM threads`, envelope.FormatTime(now.Add(-24*time.Hour)), envelope.FormatTime(now.Add(-7*24*time.Hour))).Scan(&out.Total, &out.Open, &out.Archived, &out.Active24h, &out.Active7d)
	return out, err
}
