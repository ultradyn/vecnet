-- Peer archival is a reversible administrative soft state. Historical rows
-- continue to reference the peer; credentials are revoked by the hub action.
ALTER TABLE peers ADD COLUMN archived_at TEXT;
CREATE INDEX peers_archived ON peers(archived_at);
