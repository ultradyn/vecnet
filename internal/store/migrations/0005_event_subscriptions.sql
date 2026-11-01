-- MCP Events subscriptions (PROTOCOL §4.4). A subscription tails its peer's
-- event log from its cursor; the log is the durable queue, and event_outbox
-- holds what was read, with its exact body bytes until it settles.
-- principal_id: the OAuth principal; the peer of a peer credential; the
-- principal of a principal or operator credential.
-- secret_enc, prev_secret_enc: whsec_ secrets sealed under meta.hub_key.
-- truncated: the worker skipped purged events since the last events/subscribe.
-- attempts, next_attempt_at: the oldest unsettled outbox row's, for the operator.
CREATE TABLE event_subscriptions (
	sub_id            TEXT PRIMARY KEY,
	principal_kind    TEXT NOT NULL CHECK (principal_kind IN ('oauth', 'vecnet')),
	principal_id      TEXT NOT NULL,
	peer_id           TEXT NOT NULL,
	name              TEXT NOT NULL,
	arguments         TEXT NOT NULL CHECK (json_valid(arguments)),
	url               TEXT NOT NULL,
	secret_enc        TEXT NOT NULL,
	prev_secret_enc   TEXT,
	prev_secret_until TEXT,
	cursor_epoch      INT  NOT NULL,
	cursor_seq        INT  NOT NULL,
	truncated         INT  NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
	types             TEXT NOT NULL CHECK (json_valid(types)),
	expires_at        TEXT,
	verified_at       TEXT,
	paused_reason     TEXT,
	token_expires_at  TEXT,
	last_delivery_at  TEXT,
	last_status       INT,
	attempts          INT  NOT NULL DEFAULT 0,
	next_attempt_at   TEXT,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL,
	UNIQUE (principal_kind, principal_id, url, name, arguments)
) STRICT;

-- event_id: delivery_id for a delivery Event, else the log cursor string
-- [R14]; unique per subscription, since two subscriptions may tail one peer.
-- seq: the Event's log seq, the drain order. terminal: why a row was given up
-- on ('http_410', 'http_413', 'exhausted', 'gated'); NULL for a delivered one.
CREATE TABLE event_outbox (
	sub_id          TEXT NOT NULL,
	event_id        TEXT NOT NULL,
	seq             INT  NOT NULL,
	body            TEXT NOT NULL,
	attempts        INT  NOT NULL DEFAULT 0,
	next_attempt_at TEXT,
	created_at      TEXT NOT NULL,
	done_at         TEXT,
	terminal        TEXT,
	PRIMARY KEY (sub_id, event_id)
) STRICT;
CREATE INDEX event_outbox_pending ON event_outbox(sub_id, seq) WHERE done_at IS NULL;
CREATE INDEX event_outbox_done ON event_outbox(done_at) WHERE done_at IS NOT NULL;
