-- Whole-branch review fixes to MCP Events (PROTOCOL §4.4).
-- parked: the delivery was a held wake_idle when the worker read it. The row
-- waits here, outside the client's cursor, and is pushed once on release
-- (PROTOCOL §5.2).
ALTER TABLE event_outbox ADD COLUMN parked INT NOT NULL DEFAULT 0 CHECK (parked IN (0, 1));
-- token_hash: the OAuth access token that last subscribed, so a revoked family
-- stops its subscriptions. NULL for a vecnet bearer.
ALTER TABLE event_subscriptions ADD COLUMN token_hash TEXT;
-- last_refresh_at: the last events/subscribe of an existing row; updated_at
-- moves with every cursor change and cannot show this (PROTOCOL §13.4).
ALTER TABLE event_subscriptions ADD COLUMN last_refresh_at TEXT;
