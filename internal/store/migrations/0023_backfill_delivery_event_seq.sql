-- Backfill verifies all deliveries linked by the authoritative event sequence.
-- Cover the hash probe without trusting embedded message or delivery ids.
CREATE INDEX deliveries_event_seq_hash ON deliveries(event_seq, body_sha256);
