-- One preferences store; defaults remain an inherited bootstrap layer.
CREATE TABLE hub_preferences (
    key TEXT PRIMARY KEY,
    value_json TEXT NOT NULL CHECK(json_valid(value_json)),
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE hub_preferences_state (
    id INTEGER PRIMARY KEY CHECK(id=1),
    revision INTEGER NOT NULL CHECK(revision>=0)
) STRICT;
INSERT INTO hub_preferences_state(id, revision) VALUES (1, 0);

CREATE TABLE hub_preference_activations (
    revision INTEGER NOT NULL,
    key TEXT NOT NULL,
    effective_from TEXT NOT NULL,
    not_before TEXT,
    cutoff_at TEXT,
    high_water INTEGER,
    approval_digest TEXT NOT NULL,
    PRIMARY KEY(revision, key)
) STRICT;
