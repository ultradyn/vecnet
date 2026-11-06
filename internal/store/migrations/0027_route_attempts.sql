-- One row per outbound delivery attempt (http push, grokbot routine, or
-- MCP Events callback). route_id is the push route or the subscription id.
-- error_class is a fixed hub code: letters, digits and underscores only,
-- never a response body. Rows are pruned on insert to the newest 1000 per
-- route that are also newer than 7 days. Stats are computed at read time.
-- route_runs is unchanged.

CREATE TABLE route_attempts (
	id           INTEGER PRIMARY KEY,
	route_id     TEXT NOT NULL,
	delivery_id  TEXT NOT NULL,
	at           TEXT NOT NULL CHECK (julianday(at) IS NOT NULL),
	outcome      TEXT NOT NULL CHECK (outcome IN ('ok', 'fail', 'refused')),
	http_status  INTEGER CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 599),
	error_class  TEXT CHECK (error_class IS NULL OR (length(error_class) BETWEEN 1 AND 64 AND error_class NOT GLOB '*[^a-z0-9_]*')),
	body_bytes   INTEGER NOT NULL CHECK (body_bytes BETWEEN 0 AND 1048576),
	latency_ms   INTEGER NOT NULL CHECK (latency_ms BETWEEN 0 AND 600000)
) STRICT;

CREATE INDEX route_attempts_route_at ON route_attempts(route_id, at DESC, id DESC);
