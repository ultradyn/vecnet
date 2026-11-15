-- One creation per explicitly addressed collective destination (grp_ or
-- team_), even when a direct recipient or another group came first.
-- Evidence commits with the send and its response snapshot.
CREATE TABLE group_thread_creations (
    group_id TEXT NOT NULL,
    thread_id TEXT NOT NULL REFERENCES threads(thr_id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (group_id, thread_id)
) STRICT;

-- Collective destinations live in two parent tables. Validate the shared ID
-- and cascade either parent's deletion without nullable typed foreign keys.
CREATE TRIGGER group_thread_creations_valid_insert
BEFORE INSERT ON group_thread_creations
WHEN NOT EXISTS (SELECT 1 FROM groups WHERE grp_id=NEW.group_id)
 AND NOT EXISTS (SELECT 1 FROM teams WHERE team_id=NEW.group_id)
BEGIN
    SELECT RAISE(ABORT, 'unknown group thread creation recipient');
END;
CREATE TRIGGER group_thread_creations_valid_update
BEFORE UPDATE OF group_id ON group_thread_creations
WHEN NOT EXISTS (SELECT 1 FROM groups WHERE grp_id=NEW.group_id)
 AND NOT EXISTS (SELECT 1 FROM teams WHERE team_id=NEW.group_id)
BEGIN
    SELECT RAISE(ABORT, 'unknown group thread creation recipient');
END;
CREATE TRIGGER group_thread_creations_delete_group
AFTER DELETE ON groups
BEGIN
    DELETE FROM group_thread_creations WHERE group_id=OLD.grp_id;
END;
CREATE TRIGGER group_thread_creations_delete_team
AFTER DELETE ON teams
BEGIN
    DELETE FROM group_thread_creations WHERE group_id=OLD.team_id;
END;

-- Only sequence 1 records creation recipients. A later retained message may
-- introduce a group to an existing thread and must not invent creation history.
-- The primary group remains known even if the first body has been removed.
-- Join existing destinations to avoid reviving history for deleted groups.
WITH destinations(group_id) AS (
    SELECT grp_id FROM groups UNION ALL SELECT team_id FROM teams
)
INSERT INTO group_thread_creations(group_id, thread_id, created_at)
SELECT g.group_id, t.thr_id, t.created_at
FROM threads t JOIN destinations g ON g.group_id=t.group_id
UNION
SELECT g.group_id, t.thr_id, t.created_at
FROM threads t
JOIN messages m ON m.thr_id=t.thr_id AND m.seq=1
JOIN json_each(m.body, '$.recipients') r
JOIN destinations g ON r.type='text' AND g.group_id=r.value;

CREATE INDEX group_thread_creations_recent ON group_thread_creations(group_id, created_at);
CREATE INDEX group_thread_creations_thread ON group_thread_creations(thread_id);
