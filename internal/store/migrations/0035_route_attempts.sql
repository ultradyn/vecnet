-- Independent, best-effort payload observations; no history or routing pins.
CREATE TABLE route_attempts (
    id INTEGER PRIMARY KEY,
    route_id TEXT NOT NULL CHECK (length(CAST(route_id AS BLOB)) BETWEEN 1 AND 80),
    delivery_id TEXT NOT NULL CHECK (length(CAST(delivery_id AS BLOB)) BETWEEN 1 AND 80),
    at TEXT NOT NULL CHECK (
        length(at) = 24 AND
        strftime('%Y-%m-%dT%H:%M:%fZ', at, '+0 seconds') IS NOT NULL AND
        at = strftime('%Y-%m-%dT%H:%M:%fZ', at, '+0 seconds')
    ),
    outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'fail', 'refused')),
    http_status INTEGER CHECK (http_status BETWEEN 100 AND 599),
    error_class TEXT CHECK (error_class IN (
        'http_3xx', 'http_4xx', 'http_408', 'http_409', 'http_410',
        'http_413', 'http_429', 'http_5xx', 'http_status',
        'routine_auth', 'routine_rate_limited', 'routine_not_started',
        'redirect_refused', 'private_address', 'scheme_refused',
        'userinfo_refused', 'invalid_url', 'canceled', 'timeout',
        'dns_error', 'tls_error', 'connection_refused', 'connection_reset',
        'connection_closed', 'network_error', 'request_failed'
    )),
    body_bytes INTEGER NOT NULL CHECK (body_bytes BETWEEN 0 AND 1048576),
    latency_ms INTEGER NOT NULL CHECK (latency_ms BETWEEN 0 AND 600000),
    CHECK ((outcome = 'ok' AND error_class IS NULL) OR
           (outcome != 'ok' AND error_class IS NOT NULL))
) STRICT;

CREATE INDEX route_attempts_route_at ON route_attempts(route_id, at DESC, id DESC);
CREATE INDEX route_attempts_delivery_at ON route_attempts(delivery_id, at);
