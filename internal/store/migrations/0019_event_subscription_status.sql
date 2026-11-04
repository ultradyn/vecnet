-- ChatGPT frequently asks for its own subscription health. Bound status reads
-- to the peer's rows rather than scanning subscriptions for every principal.
CREATE INDEX event_subscriptions_peer_expiry ON event_subscriptions(peer_id, expires_at);
