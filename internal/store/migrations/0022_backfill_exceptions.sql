-- Durable content-free manual-review exceptions; progress uses existing meta.
-- No foreign key: a concurrently pruned event must not erase its diagnostic.
CREATE TABLE delivery_backfill_exceptions (
 seq INTEGER PRIMARY KEY,
 reason TEXT NOT NULL CHECK (reason IN ('invalid_shape','duplicate_key','identity_mismatch','delivery_hash_mismatch'))
) STRICT;
