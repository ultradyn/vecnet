-- Candidate-first copy discovery includes pending, parked and exhausted rows.
CREATE INDEX event_outbox_retention_candidate ON event_outbox(seq, sub_id)
    WHERE done_at IS NULL OR terminal='exhausted';
-- One live range excludes both expired leases and expired access tokens, even
-- when the other deadline is NULL. Historical unverified/paused rows are absent.
CREATE INDEX event_subscriptions_retention_live ON event_subscriptions(
    peer_id, min(coalesce(expires_at,'9999'),coalesce(token_expires_at,'9999'))
) WHERE verified_at IS NOT NULL AND paused_reason IS NULL;
