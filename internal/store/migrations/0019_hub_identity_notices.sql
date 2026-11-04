-- Decision 1: the public Ed25519 identity must be separate from hub_key.
-- hub_key is a symmetric secret used for blob tokens and sealing secrets;
-- deriving or rotating the identity from it would expose or break those uses.
-- Keeping the random identity seed in meta makes database backups carry it.
INSERT INTO meta(key, value) VALUES
    ('hub_identity_seed', lower(hex(randomblob(32)))),
    ('hub_identity_created_at', strftime('%Y-%m-%dT%H:%M:%fZ','now'));

CREATE TABLE hub_notices (
    notice_id   TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('maintenance', 'restart', 'moving')),
    message     TEXT NOT NULL,
    hub         TEXT NOT NULL,
    issued_at   TEXT NOT NULL,
    at          TEXT,
    expires_at  TEXT NOT NULL,
    new_url     TEXT,
    sig         TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    cleared_at  TEXT,
    cleared_how TEXT
) STRICT;

CREATE INDEX hub_notices_active ON hub_notices(cleared_at, kind);
