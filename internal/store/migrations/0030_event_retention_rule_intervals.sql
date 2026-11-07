-- Consumer eligibility metadata, not a preferences store. Each node describes
-- a binary sequence interval with a lazy protective minimum; terminal nodes
-- are uniform. A candidate follows at most 64 primary-key lookups.
CREATE TABLE event_retention_rule_nodes (
    id INTEGER PRIMARY KEY,
    from_seq INTEGER NOT NULL CHECK(from_seq>=0),
    through_seq INTEGER NOT NULL CHECK(through_seq>=from_seq),
    protection INTEGER NOT NULL CHECK(protection>=0),
    left_id INTEGER,
    right_id INTEGER,
    CHECK((left_id IS NULL)=(right_id IS NULL))
) STRICT;
CREATE TABLE event_retention_rule_roots (
    projection TEXT PRIMARY KEY,
    node_id INTEGER NOT NULL
) STRICT;
-- Detached subtrees are reclaimed incrementally, never by a recursive DELETE.
CREATE TABLE event_retention_rule_garbage (node_id INTEGER PRIMARY KEY) STRICT;
