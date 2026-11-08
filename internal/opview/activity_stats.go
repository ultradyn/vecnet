package opview

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/envelope"
	"github.com/XertroV/vecnet/internal/opapi"
)

// Persisted timestamps are fixed-width UTC milliseconds. Keep the range
// predicate on the raw column so messages_history_age bounds the work to one
// hour. Integer epoch milliseconds avoid floating-point bucket-edge rounding.
const activityStatsQuery = `SELECT sender,
  (CAST(strftime('%s', created_at) AS INTEGER) * 1000
    + CAST(substr(created_at, 21, 3) AS INTEGER) - ?) / 60000 AS bucket,
  COUNT(*)
FROM messages
WHERE created_at >= ? AND created_at < ? AND kind <> 'reaction'
GROUP BY sender, bucket`

// ActivityStats reads committed message volume in one time-indexed aggregate.
// No peer joins: renamed, disabled and archived senders retain their history.
// Request retries and recipient fanout cannot inflate the stored message count.
func (r *Reader) ActivityStats(ctx context.Context) (opapi.ActivityStats, error) {
	now := r.Hub.Now().UTC().Truncate(time.Millisecond)
	start := now.Add(-time.Hour)
	out := opapi.ActivityStats{
		Now: envelope.FormatTime(now), WindowStart: envelope.FormatTime(start),
		WindowEnd: envelope.FormatTime(now), BucketSeconds: 60,
		Peers: map[string]opapi.PeerActivityStats{},
	}
	rows, err := r.Hub.DB.QueryContext(ctx, activityStatsQuery, start.UnixMilli(), out.WindowStart, out.WindowEnd)
	if err != nil {
		return opapi.ActivityStats{}, fmt.Errorf("opview: activity stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sender string
		var bucket, count int
		if err := rows.Scan(&sender, &bucket, &count); err != nil {
			return opapi.ActivityStats{}, fmt.Errorf("opview: activity stats: %w", err)
		}
		if bucket < 0 || bucket >= len(out.Buckets) {
			return opapi.ActivityStats{}, fmt.Errorf("opview: activity stats: invalid time bucket")
		}
		peer, exists := out.Peers[sender]
		if !exists && strings.HasPrefix(sender, "peer_") {
			out.ActivePeers++
		}
		peer.Messages += count
		peer.MessagesPerMinute = float64(peer.Messages) / 60
		peer.Buckets[bucket] += count
		out.Peers[sender] = peer
		out.Messages += count
		out.Buckets[bucket] += count
	}
	if err := rows.Err(); err != nil {
		return opapi.ActivityStats{}, fmt.Errorf("opview: activity stats: %w", err)
	}
	out.MessagesPerMinute = float64(out.Messages) / 60
	return out, nil
}
