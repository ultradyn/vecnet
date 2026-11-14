ALTER TABLE threads ADD COLUMN archived_at TEXT;
ALTER TABLE threads ADD COLUMN archived_by TEXT CHECK (archived_by IN ('operator', 'hub'));
ALTER TABLE threads ADD COLUMN unarchived_at TEXT;
CREATE INDEX threads_archived ON threads(archived_at);
ALTER TABLE peers ADD COLUMN unarchived_at TEXT;
