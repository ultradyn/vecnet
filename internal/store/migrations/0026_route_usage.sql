-- Outbound push usage for a delivery route. A route here is either a push
-- route (routes.route_id, kind http or grokbot) or an MCP Events callback
-- (event_subscriptions.sub_id, kind events). Lifetime counters live on
-- route_usage. Percentiles are computed from route_usage_samples, a fixed
-- ring of the most recent 256 attempts: body sizes and latencies only,
-- never a request body, URL, header or key.
-- A failed stats write must not fail a delivery, so nothing here is
-- referenced by deliveries.

CREATE TABLE route_usage (
	route_key       TEXT PRIMARY KEY,
	kind            TEXT NOT NULL CHECK (kind IN ('http', 'grokbot', 'events')),
	requests        INTEGER NOT NULL CHECK (requests >= 0),
	successes       INTEGER NOT NULL CHECK (successes >= 0),
	failures        INTEGER NOT NULL CHECK (failures >= 0),
	last_error      TEXT CHECK (last_error IS NULL OR length(last_error) BETWEEN 1 AND 200),
	last_error_at   TEXT CHECK (last_error_at IS NULL OR julianday(last_error_at) IS NOT NULL),
	last_status     INTEGER CHECK (last_status IS NULL OR last_status BETWEEN 100 AND 599),
	last_used_at    TEXT NOT NULL CHECK (julianday(last_used_at) IS NOT NULL),
	last_success_at TEXT CHECK (last_success_at IS NULL OR julianday(last_success_at) IS NOT NULL),
	sample_n        INTEGER NOT NULL CHECK (sample_n >= 0),
	CHECK (requests = successes + failures)
) STRICT;

CREATE TABLE route_usage_samples (
	route_key   TEXT NOT NULL REFERENCES route_usage(route_key) ON DELETE CASCADE,
	slot        INTEGER NOT NULL CHECK (slot >= 0 AND slot < 256),
	body_bytes  INTEGER NOT NULL CHECK (body_bytes >= 0 AND body_bytes <= 1048576),
	latency_us  INTEGER CHECK (latency_us IS NULL OR (latency_us >= 0 AND latency_us <= 600000000)),
	PRIMARY KEY (route_key, slot)
) STRICT;
