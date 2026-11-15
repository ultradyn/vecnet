-- One expiring self-report per peer. This is independent of manual presence,
-- quotas, transport reachability, routes and local binding authority.
CREATE TABLE peer_runtime_status (
    peer_id TEXT PRIMARY KEY REFERENCES peers(peer_id) ON DELETE CASCADE,
    runtime TEXT NOT NULL CHECK (runtime = 'codex'),
    state TEXT NOT NULL CHECK (state IN ('idle', 'working', 'blocked', 'offline')),
    delivery TEXT NOT NULL CHECK (delivery IN ('ready', 'unavailable', 'unsupported')),
    reason TEXT CHECK (reason IN (
        'connecting', 'unreachable', 'refused_version', 'stopped', 'expired',
        'archived', 'not_loaded', 'idle_stale', 'approval_policy',
        'waiting_approval', 'waiting_input', 'over_limit', 'identity_changed',
        'ownership_changed', 'unverified_thread', 'identity_unverified',
        'watch_stopped', 'delivery_unresolved'
    )),
    version TEXT CHECK (length(CAST(version AS BLOB)) BETWEEN 1 AND 128),
    updated_at TEXT NOT NULL CHECK (
        length(updated_at) = 24 AND
        strftime('%Y-%m-%dT%H:%M:%fZ', updated_at, '+0 seconds') IS NOT NULL AND
        updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', updated_at, '+0 seconds')
    ),
    expires_at TEXT NOT NULL CHECK (
        length(expires_at) = 24 AND
        strftime('%Y-%m-%dT%H:%M:%fZ', expires_at, '+0 seconds') IS NOT NULL AND
        expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', expires_at, '+0 seconds') AND
        expires_at > updated_at
    ),
    CHECK (state != 'offline' OR delivery = 'unavailable'),
    CHECK (delivery != 'ready' OR state IN ('idle', 'working'))
) STRICT;
