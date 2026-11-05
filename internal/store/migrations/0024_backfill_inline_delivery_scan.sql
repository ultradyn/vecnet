-- Bound both forward backfill queries by eligible deliveries rather than the
-- size of gaps or suffixes containing presence, receipts or reference events.
-- Matches candidate ORDER BY seq / LIMIT and completion EXISTS range lookups.
CREATE INDEX events_backfill_inline_delivery_seq ON events(seq)
 WHERE type='delivery' AND payload_format='inline';
