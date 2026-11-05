-- Additive only: existing event bytes and migration checksums stay untouched.
ALTER TABLE events ADD COLUMN payload_format TEXT NOT NULL DEFAULT 'inline'
 CHECK (payload_format IN ('inline', 'delivery_ref_v1'));
ALTER TABLE events ADD COLUMN msg_id TEXT
 CHECK ((payload_format = 'inline' AND msg_id IS NULL) OR
        (payload_format = 'delivery_ref_v1' AND type = 'delivery' AND msg_id IS NOT NULL
         AND json_type(data, '$.msg_id') IS 'text'
         AND json_extract(data, '$.msg_id') IS msg_id));
ALTER TABLE events ADD COLUMN snapshot_id TEXT
 CHECK ((payload_format = 'inline' AND snapshot_id IS NULL) OR
        (payload_format = 'delivery_ref_v1'
         AND json_extract(data, '$.snapshot_id') IS snapshot_id
         AND (snapshot_id IS NULL OR json_type(data, '$.snapshot_id') IS 'text')));
CREATE INDEX events_msg_ref ON events(msg_id) WHERE msg_id IS NOT NULL;
CREATE INDEX events_snapshot_ref ON events(snapshot_id) WHERE snapshot_id IS NOT NULL;

CREATE TABLE event_message_snapshots (
 snapshot_id TEXT PRIMARY KEY,
 msg_id TEXT NOT NULL,
 body_sha256 TEXT NOT NULL,
 body TEXT NOT NULL CHECK (json_valid(body)),
 UNIQUE(msg_id, body_sha256)
) STRICT;

CREATE TRIGGER event_snapshot_immutable BEFORE UPDATE ON event_message_snapshots
WHEN NEW.snapshot_id IS NOT OLD.snapshot_id OR NEW.msg_id IS NOT OLD.msg_id
 OR NEW.body_sha256 IS NOT OLD.body_sha256 OR NEW.body IS NOT OLD.body
BEGIN SELECT RAISE(ABORT, 'event_snapshot_immutable'); END;

-- Task 3 sets this flag atomically with enabling reference writes. It is not
-- set here: legacy migration 0018 and pre-cutover behavior must still work.
CREATE TRIGGER messages_replay_immutable BEFORE UPDATE OF body, msg_id ON messages
WHEN (NEW.body IS NOT OLD.body OR NEW.msg_id IS NOT OLD.msg_id)
 AND ((SELECT value FROM meta WHERE key = 'event_payload_refs_enabled') = '1'
      OR EXISTS (SELECT 1 FROM events WHERE msg_id = OLD.msg_id AND snapshot_id IS NULL))
BEGIN SELECT RAISE(ABORT, 'message_replay_immutable'); END;

CREATE TRIGGER messages_replay_pinned BEFORE DELETE ON messages
WHEN EXISTS (SELECT 1 FROM events WHERE msg_id = OLD.msg_id AND snapshot_id IS NULL)
BEGIN SELECT RAISE(ABORT, 'message_replay_pinned'); END;
CREATE TRIGGER event_snapshot_pinned BEFORE DELETE ON event_message_snapshots
WHEN EXISTS (SELECT 1 FROM events WHERE snapshot_id = OLD.snapshot_id)
BEGIN SELECT RAISE(ABORT, 'event_snapshot_pinned'); END;
