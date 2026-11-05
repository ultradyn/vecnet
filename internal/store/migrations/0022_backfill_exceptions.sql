-- Durable content-free manual-review exceptions; progress uses existing meta.
-- No foreign key: a concurrently pruned event must not erase its diagnostic.
CREATE TABLE delivery_backfill_exceptions (
 seq INTEGER PRIMARY KEY,
 reason TEXT NOT NULL CHECK (reason IN ('invalid_shape','duplicate_key','identity_mismatch','delivery_hash_mismatch'))
) STRICT;

-- Backfill verifies all deliveries linked by the authoritative event sequence.
-- Cover the hash probe without trusting embedded message or delivery ids.
CREATE INDEX deliveries_event_seq_hash ON deliveries(event_seq, body_sha256);

-- Bound both forward backfill queries by eligible deliveries rather than the
-- size of gaps or suffixes containing presence, receipts or reference events.
-- Matches candidate ORDER BY seq / LIMIT and completion EXISTS range lookups.
CREATE INDEX events_backfill_inline_delivery_seq ON events(seq)
 WHERE type='delivery' AND payload_format='inline';
