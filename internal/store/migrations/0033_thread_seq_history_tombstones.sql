-- Existing thread maxima are adopted lazily by the allocator and before GC.
-- No startup rewrite of existing messages or threads is required.
ALTER TABLE threads ADD COLUMN last_allocated_seq INTEGER NOT NULL DEFAULT 0 CHECK(last_allocated_seq>=0);
ALTER TABLE messages ADD COLUMN history_expired_at TEXT;
ALTER TABLE messages ADD COLUMN history_expired_revision INTEGER;
ALTER TABLE messages ADD COLUMN history_event_seq INTEGER;
CREATE TABLE message_history_locators (
 msg_id TEXT PRIMARY KEY,
 thr_id TEXT NOT NULL,
 seq INTEGER NOT NULL CHECK(seq>0),
 expired_at TEXT NOT NULL,
 UNIQUE(thr_id,seq)
) STRICT;
CREATE INDEX messages_history_age ON messages(created_at,msg_id);
-- Bounded dependency discovery for inline deliveries, reactions and receipts.
CREATE INDEX events_history_inline_message ON events(json_extract(data,'$.message.message_id')) WHERE payload_format='inline';
CREATE INDEX events_history_message ON events(json_extract(data,'$.message_id')) WHERE payload_format='inline';
CREATE INDEX events_history_delivery ON events(json_extract(data,'$.delivery_id')) WHERE payload_format='inline';
CREATE INDEX event_outbox_history_delivery ON event_outbox(event_id);
CREATE INDEX approvals_history_message ON approvals(msg_id);
CREATE INDEX jobs_history_thread ON jobs(thr_id);
INSERT INTO meta(key,value) VALUES('history_locator_count','0');
CREATE TRIGGER threads_seq_never_decreases BEFORE UPDATE OF last_allocated_seq ON threads
WHEN NEW.last_allocated_seq<OLD.last_allocated_seq
BEGIN SELECT RAISE(ABORT,'thread_sequence_regression'); END;
