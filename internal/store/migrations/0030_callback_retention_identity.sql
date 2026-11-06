-- The peer may retain unlimited live subscriptions from former principals.
-- Seek only its three current authorized identity scopes before lease ranges.
CREATE INDEX event_subscriptions_retention_identity ON event_subscriptions(
    peer_id, principal_kind, principal_id,
    min(coalesce(expires_at,'9999'),coalesce(token_expires_at,'9999'))
) WHERE verified_at IS NOT NULL AND paused_reason IS NULL;
