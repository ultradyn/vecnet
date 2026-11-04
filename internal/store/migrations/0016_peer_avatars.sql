-- Peer avatars (PROTOCOL §8.1). The hub stores only normalised PNGs it made
-- itself, keyed by the SHA-256 of those bytes; peers.avatar names one, and an
-- image no peer names is deleted in the same transaction that drops it.
CREATE TABLE avatars (
	sha256     TEXT PRIMARY KEY CHECK (length(sha256) = 64),
	png        BLOB NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

ALTER TABLE peers ADD COLUMN avatar TEXT REFERENCES avatars(sha256);

-- One row per self-service avatar change, for the per-peer rate limit;
-- rows older than the window are pruned on write.
CREATE TABLE avatar_ops (
	peer_id TEXT NOT NULL,
	at      TEXT NOT NULL
) STRICT;
CREATE INDEX avatar_ops_peer_at ON avatar_ops(peer_id, at);
