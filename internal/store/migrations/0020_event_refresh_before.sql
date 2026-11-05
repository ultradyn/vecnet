-- Preserve the exact refreshBefore returned by a successful events/subscribe.
-- Existing subscriptions retain their expiry-based deadline until refreshed.
ALTER TABLE event_subscriptions ADD COLUMN refresh_before_at TEXT;
