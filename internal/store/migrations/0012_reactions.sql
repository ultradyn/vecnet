-- Widen the Message kind CHECK without changing existing message bytes.
-- No table, trigger or view references messages; preserve its unique indexes.
CREATE TABLE messages_0012 (
	msg_id      TEXT PRIMARY KEY,
	thr_id      TEXT NOT NULL,
	seq         INT  NOT NULL CHECK (seq >= 1),
	sender      TEXT NOT NULL,
	kind        TEXT NOT NULL CHECK (kind IN ('message', 'approval_request', 'approval_response', 'job_status', 'reaction')),
	created_at  TEXT NOT NULL,
	body        TEXT NOT NULL CHECK (json_valid(body)),
	job_id      TEXT,
	approval_id TEXT,
	UNIQUE (thr_id, seq)
) STRICT;

INSERT INTO messages_0012(msg_id, thr_id, seq, sender, kind, created_at, body, job_id, approval_id)
SELECT msg_id, thr_id, seq, sender, kind, created_at, body, job_id, approval_id FROM messages;
DROP TABLE messages;
ALTER TABLE messages_0012 RENAME TO messages;

CREATE TABLE reactions (
	msg_id     TEXT NOT NULL,
	peer_id    TEXT NOT NULL,
	emoji      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (msg_id, peer_id, emoji)
) STRICT;

CREATE TABLE reaction_ops (
	peer_id TEXT NOT NULL,
	at      TEXT NOT NULL
) STRICT;
CREATE INDEX reaction_ops_peer_at ON reaction_ops(peer_id, at);

-- An immutable record of each wake ever created; remove never deletes it.
CREATE TABLE reaction_wakes (
	msg_id     TEXT NOT NULL,
	peer_id    TEXT NOT NULL,
	emoji      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (msg_id, peer_id, emoji)
) STRICT;
