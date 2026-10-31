-- vecnet hub schema v1.
-- Every table is STRICT. Ids are TEXT (prefixed ULIDs); times are TEXT in
-- envelope.TimeLayout (YYYY-MM-DDTHH:MM:SS.sssZ). STRICT accepts only
-- INT/INTEGER/REAL/TEXT/BLOB/ANY, so JSON columns are TEXT CHECK (json_valid(col)).
-- This file is checksummed into schema_migrations: never edit it once shipped;
-- add a new numbered migration instead.

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) STRICT;

-- epoch: the event-log epoch in cursors e<epoch>.<seq> (PROTOCOL §1.3).
-- hub_key: 32 random bytes, lower hex, keys blob URL tokens.
INSERT INTO meta(key, value) VALUES
	('epoch', '1'),
	('hub_key', lower(hex(randomblob(32))));

CREATE TABLE principals (
	prn_id     TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

CREATE TABLE peers (
	peer_id            TEXT PRIMARY KEY,
	prn_id             TEXT NOT NULL REFERENCES principals(prn_id),
	name               TEXT NOT NULL,
	kind               TEXT NOT NULL,
	enrolled_at        TEXT NOT NULL,
	disabled           INT  NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
	subscribe_presence TEXT NOT NULL DEFAULT 'teams' CHECK (subscribe_presence IN ('all', 'teams', 'none')),
	presence_state     TEXT CHECK (presence_state IN ('idle', 'working', 'blocked', 'offline')),
	presence_at        TEXT,
	capabilities       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(capabilities)),
	quota              TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(quota)),
	location           TEXT,
	note               TEXT,
	reachability       TEXT NOT NULL DEFAULT 'unknown',
	last_seen          TEXT,
	route_id           TEXT,
	sends_per_min      INT  NOT NULL DEFAULT 60,
	threads_per_hour   INT  NOT NULL DEFAULT 20
) STRICT;
CREATE INDEX peers_prn ON peers(prn_id);

CREATE TABLE credentials (
	cred_id     TEXT PRIMARY KEY,
	kind        TEXT NOT NULL CHECK (kind IN ('peer', 'principal', 'operator')),
	subject_id  TEXT NOT NULL,
	secret_hash TEXT NOT NULL UNIQUE,
	created_at  TEXT NOT NULL,
	revoked_at  TEXT
) STRICT;
CREATE INDEX credentials_subject ON credentials(subject_id);

CREATE TABLE enrol_tokens (
	token_hash TEXT PRIMARY KEY,
	peer_id    TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	used_at    TEXT
) STRICT;

-- An empty table means allow all.
CREATE TABLE scopes (
	peer_id   TEXT NOT NULL,
	mode      TEXT NOT NULL CHECK (mode IN ('allow', 'deny')),
	target_id TEXT NOT NULL,
	PRIMARY KEY (peer_id, mode, target_id)
) STRICT;

CREATE TABLE teams (
	team_id    TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

CREATE TABLE team_members (
	team_id TEXT NOT NULL,
	peer_id TEXT NOT NULL,
	PRIMARY KEY (team_id, peer_id)
) STRICT;
CREATE INDEX team_members_peer ON team_members(peer_id);

CREATE TABLE groups (
	grp_id         TEXT PRIMARY KEY,
	created_by     TEXT NOT NULL,
	created_at     TEXT NOT NULL,
	last_active_at TEXT NOT NULL,
	archived_at    TEXT
) STRICT;

CREATE TABLE group_members (
	grp_id  TEXT NOT NULL,
	peer_id TEXT NOT NULL,
	PRIMARY KEY (grp_id, peer_id)
) STRICT;
CREATE INDEX group_members_peer ON group_members(peer_id);

CREATE TABLE threads (
	thr_id              TEXT PRIMARY KEY,
	title               TEXT,
	group_id            TEXT,
	created_by          TEXT NOT NULL,
	created_at          TEXT NOT NULL,
	message_count       INT  NOT NULL DEFAULT 0,
	budget_remaining    INT,
	history_access      TEXT NOT NULL DEFAULT 'from_join' CHECK (history_access IN ('from_join', 'full')),
	locked_participants INT  NOT NULL DEFAULT 0 CHECK (locked_participants IN (0, 1)),
	last_message_at     TEXT NOT NULL
) STRICT;

-- joined_after_seq: under history_access=from_join the participant sees
-- messages with seq > joined_after_seq.
CREATE TABLE thread_participants (
	thr_id           TEXT NOT NULL,
	peer_id          TEXT NOT NULL,
	joined_at        TEXT NOT NULL,
	joined_after_seq INT  NOT NULL DEFAULT 0,
	PRIMARY KEY (thr_id, peer_id)
) STRICT;
CREATE INDEX thread_participants_peer ON thread_participants(peer_id);

-- body: the exact serialised Message bytes. seq is per-thread, 1-based.
CREATE TABLE messages (
	msg_id      TEXT PRIMARY KEY,
	thr_id      TEXT NOT NULL,
	seq         INT  NOT NULL CHECK (seq >= 1),
	sender      TEXT NOT NULL,
	kind        TEXT NOT NULL CHECK (kind IN ('message', 'approval_request', 'approval_response', 'job_status')),
	created_at  TEXT NOT NULL,
	body        TEXT NOT NULL CHECK (json_valid(body)),
	job_id      TEXT,
	approval_id TEXT,
	UNIQUE (thr_id, seq)
) STRICT;

CREATE TABLE idempotency (
	sender      TEXT NOT NULL,
	key         TEXT NOT NULL,
	body_sha256 TEXT NOT NULL,
	response    TEXT NOT NULL CHECK (json_valid(response)),
	created_at  TEXT NOT NULL,
	PRIMARY KEY (sender, key)
) STRICT;

-- event_seq: the recipient's event-log seq for this delivery.
-- held: 1 while a wake_idle delivery is held.
CREATE TABLE deliveries (
	dly_id          TEXT PRIMARY KEY,
	msg_id          TEXT NOT NULL,
	thr_id          TEXT NOT NULL,
	recipient       TEXT NOT NULL,
	via             TEXT,
	also_via        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(also_via)),
	priority        TEXT NOT NULL CHECK (priority IN ('wake', 'wake_idle', 'hold')),
	state           TEXT NOT NULL,
	attempt         INT  NOT NULL DEFAULT 0,
	attempts_left   INT  NOT NULL,
	next_attempt_at TEXT,
	route_id        TEXT,
	held            INT  NOT NULL DEFAULT 0 CHECK (held IN (0, 1)),
	expires_at      TEXT NOT NULL,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL,
	last_status     INT,
	last_error      TEXT,
	event_seq       INT,
	body_sha256     TEXT NOT NULL
) STRICT;
CREATE INDEX deliveries_msg ON deliveries(msg_id);
CREATE INDEX deliveries_recipient_thr ON deliveries(recipient, thr_id, created_at);
CREATE INDEX deliveries_due ON deliveries(state, next_attempt_at);
CREATE INDEX deliveries_expiry ON deliveries(state, expires_at);

-- The per-owner durable event log. seq is global and monotonic; cursors are
-- e<epoch>.<seq>.
CREATE TABLE events (
	seq   INTEGER PRIMARY KEY AUTOINCREMENT,
	owner TEXT NOT NULL,
	type  TEXT NOT NULL,
	at    TEXT NOT NULL,
	data  TEXT NOT NULL CHECK (json_valid(data))
) STRICT;
CREATE INDEX events_owner_seq ON events(owner, seq);

-- scope is '*' or a peer_id (a principal stream narrowed with ?peer=).
CREATE TABLE acks (
	owner    TEXT NOT NULL,
	scope    TEXT NOT NULL,
	seq      INT  NOT NULL,
	acked_at TEXT NOT NULL,
	PRIMARY KEY (owner, scope)
) STRICT;

CREATE TABLE routes (
	route_id    TEXT PRIMARY KEY,
	owner_kind  TEXT NOT NULL CHECK (owner_kind IN ('peer', 'principal')),
	owner_id    TEXT NOT NULL,
	kind        TEXT NOT NULL DEFAULT 'http' CHECK (kind IN ('http')),
	url         TEXT NOT NULL,
	verified_at TEXT,
	created_at  TEXT NOT NULL
) STRICT;
CREATE INDEX routes_owner ON routes(owner_kind, owner_id);

CREATE TABLE route_keys (
	route_id   TEXT NOT NULL,
	whsec      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	retired_at TEXT,
	PRIMARY KEY (route_id, whsec)
) STRICT;

CREATE TABLE notify_routes (
	id         TEXT PRIMARY KEY,
	url        TEXT NOT NULL,
	filter     TEXT NOT NULL CHECK (filter IN ('wake', 'approvals', 'dead_letters', 'all')),
	whsec      TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

CREATE TABLE blobs (
	sha256     TEXT PRIMARY KEY,
	size       INT  NOT NULL,
	media_type TEXT NOT NULL,
	uploader   TEXT NOT NULL,
	created_at TEXT NOT NULL,
	ttl_until  TEXT,
	purged_at  TEXT
) STRICT;

CREATE TABLE blob_refs (
	sha256 TEXT NOT NULL,
	msg_id TEXT NOT NULL,
	PRIMARY KEY (sha256, msg_id)
) STRICT;
CREATE INDEX blob_refs_msg ON blob_refs(msg_id);

CREATE TABLE blob_quota (
	peer_id TEXT NOT NULL,
	day     TEXT NOT NULL,
	bytes   INT  NOT NULL DEFAULT 0,
	PRIMARY KEY (peer_id, day)
) STRICT;

CREATE TABLE jobs (
	job_id         TEXT PRIMARY KEY,
	thr_id         TEXT NOT NULL,
	job_key        TEXT NOT NULL,
	request_sha256 TEXT NOT NULL,
	requester      TEXT NOT NULL,
	recipient      TEXT NOT NULL,
	state          TEXT NOT NULL,
	last_sequence  INT  NOT NULL DEFAULT 0,
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL,
	UNIQUE (thr_id, job_key)
) STRICT;

CREATE TABLE approvals (
	apr_id     TEXT PRIMARY KEY,
	thr_id     TEXT NOT NULL,
	msg_id     TEXT NOT NULL,
	requester  TEXT NOT NULL,
	options    TEXT NOT NULL CHECK (json_valid(options)),
	expires_at TEXT NOT NULL,
	closed_at  TEXT,
	decision   TEXT,
	decided_by TEXT
) STRICT;
CREATE INDEX approvals_open ON approvals(closed_at, expires_at);

-- Append-only. Never holds secrets or message bodies (PROTOCOL §12).
CREATE TABLE audit (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	at         TEXT NOT NULL,
	actor      TEXT NOT NULL,
	action     TEXT NOT NULL,
	target_ids TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(target_ids)),
	detail     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(detail))
) STRICT;

-- Dashboard counters (loop cuts, rate-limit rejections).
CREATE TABLE counters (
	name  TEXT PRIMARY KEY,
	value INT  NOT NULL DEFAULT 0
) STRICT;
