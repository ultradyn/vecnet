-- An invite that leaves the display name to the agent: it picks the name once,
-- at enrol (PROTOCOL §2.12). Cleared when the name is taken.
ALTER TABLE peers ADD COLUMN name_open INTEGER NOT NULL DEFAULT 0;
