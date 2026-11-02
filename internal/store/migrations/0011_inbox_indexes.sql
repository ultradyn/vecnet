CREATE INDEX deliveries_recipient_dly ON deliveries(recipient, dly_id);
CREATE INDEX deliveries_unread ON deliveries(recipient) WHERE read_at IS NULL;
