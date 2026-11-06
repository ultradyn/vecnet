-- Retention probes exact durable copies, including exhausted retries and
-- terminal suppression rows. The pending-only index cannot serve both probes.
CREATE INDEX event_outbox_retention_seq ON event_outbox(sub_id, seq);
