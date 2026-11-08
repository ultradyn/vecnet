package opapi

// RouteStats is a retained attempt sample and its separate retained-delivery
// context. Every field is present on the wire; absent measurements are null.
// This DTO has no hub or storage dependencies so all read surfaces can share it.
type RouteStats struct {
	Subject            RouteStatsSubject            `json:"subject"`
	Window             RouteStatsWindow             `json:"window"`
	RetainedDeliveries RouteStatsRetainedDeliveries `json:"retained_deliveries"`
	Routes             []RouteStatsRow              `json:"routes"`
}

type RouteStatsSubject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type RouteStatsWindow struct {
	Since       string `json:"since"`
	Until       string `json:"until"`
	MaxRows     int    `json:"max_rows"`
	MaxAgeHours int    `json:"max_age_hours"`
}

// RouteStatsRetainedDeliveries counts current delivery rows by updated_at.
// Evidence includes claims and uncapped in-window telemetry on any route;
// these counts do not describe total traffic or pushed/pulled deliveries.
type RouteStatsRetainedDeliveries struct {
	PeerIDs                     []string `json:"peer_ids"`
	Total                       int64    `json:"total"`
	WithPushEvidence            int64    `json:"with_push_evidence"`
	AcceptedWithoutPushEvidence int64    `json:"accepted_without_push_evidence"`
}

// RouteStatsRow aggregates the whole route, including shared principal routes.
// Metadata describes current ownership and hostname, not historical routing.
type RouteStatsRow struct {
	RouteID       string                 `json:"route_id"`
	Kind          string                 `json:"kind"`
	Host          string                 `json:"host"`
	OwnerKind     string                 `json:"owner_kind"`
	OwnerID       string                 `json:"owner_id"`
	AttemptScope  string                 `json:"attempt_scope"`
	Requests      int                    `json:"requests"`
	OK            int                    `json:"ok"`
	Fail          int                    `json:"fail"`
	Refused       int                    `json:"refused"`
	DistinctIDs   int                    `json:"distinct_ids"`
	LastUsedAt    *string                `json:"last_used_at"`
	LastSuccessAt *string                `json:"last_success_at"`
	LastError     *string                `json:"last_error"`
	LastErrorAt   *string                `json:"last_error_at"`
	LastStatus    *int                   `json:"last_status"`
	BodyBytes     RouteStatsDistribution `json:"body_bytes"`
	LatencyMS     RouteStatsDistribution `json:"latency_ms"`
}

type RouteStatsDistribution struct {
	Avg     *float64 `json:"avg"`
	P95     *float64 `json:"p95"`
	P99     *float64 `json:"p99"`
	Samples int      `json:"samples"`
}
