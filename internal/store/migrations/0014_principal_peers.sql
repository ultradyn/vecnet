-- Creator provenance survives ownership changes; operator enrolment leaves it NULL.
ALTER TABLE peers ADD COLUMN created_by TEXT;
CREATE INDEX peers_created_by ON peers(created_by, enrolled_at);

CREATE TABLE principal_limits (
    prn_id TEXT PRIMARY KEY REFERENCES principals(prn_id),
    max_peers INT NOT NULL DEFAULT 0 CHECK (max_peers >= 0),
    creates_per_hour INT NOT NULL DEFAULT 60 CHECK (creates_per_hour >= 1),
    kinds TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(kinds)),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE principal_default_scopes (
    prn_id TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('allow','deny')),
    target_id TEXT NOT NULL,
    PRIMARY KEY (prn_id, mode, target_id)
) STRICT;

CREATE TABLE peer_create_keys (
    prn_id TEXT NOT NULL,
    key TEXT NOT NULL,
    body_sha256 TEXT NOT NULL,
    response TEXT NOT NULL CHECK (json_valid(response)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (prn_id, key)
) STRICT;
