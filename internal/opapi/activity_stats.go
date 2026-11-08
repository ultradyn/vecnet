package opapi

// PeerActivityStats counts committed non-reaction messages by immutable sender
// id, including archived/disabled peers and source/hub senders. Buckets are
// chronological, zero-filled rolling minutes, not calendar-aligned minutes.
type PeerActivityStats struct {
	Messages          int     `json:"messages"`
	MessagesPerMinute float64 `json:"messages_per_minute"`
	Buckets           [60]int `json:"buckets"`
}

// ActivityStats is a metadata-only snapshot of [window_start, window_end).
// ActivePeers counts distinct peer_ senders (including operator and dot), not
// presence or connectivity. Source/hub messages still contribute to volume.
// MessagesPerMinute is the hourly average (messages / 60), not a live rate.
type ActivityStats struct {
	Now               string                       `json:"now"`
	WindowStart       string                       `json:"window_start"`
	WindowEnd         string                       `json:"window_end"`
	BucketSeconds     int                          `json:"bucket_seconds"`
	Messages          int                          `json:"messages"`
	MessagesPerMinute float64                      `json:"messages_per_minute"`
	ActivePeers       int                          `json:"active_peers"`
	Buckets           [60]int                      `json:"buckets"`
	Peers             map[string]PeerActivityStats `json:"peers"`
}
