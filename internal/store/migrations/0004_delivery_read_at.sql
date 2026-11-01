-- When the recipient first posted a "read" receipt (PROTOCOL §2.5). A
-- repeated read receipt for the same delivery is a no-op, so a second
-- browser or cleared UI state never sends the sender duplicate receipts.
ALTER TABLE deliveries ADD COLUMN read_at TEXT;
