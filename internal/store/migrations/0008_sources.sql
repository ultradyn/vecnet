-- Inbound webhook sources (PROTOCOL §10). A source is not a peer: it has no
-- principal, no log and no credential row, and it cannot receive messages.
-- secret_hash: lower-hex SHA-256 of the verification secret.
-- secret_enc: the secret sealed under meta.hub_key, since verifying a GitHub
-- HMAC needs the plaintext.
-- target_id: a peer_, team_ or thr_ id.
-- event_allow: a JSON array of event names; NULL allows every event.
CREATE TABLE sources (
	src_id           TEXT PRIMARY KEY,
	kind             TEXT NOT NULL CHECK (kind IN ('github', 'standard_webhooks', 'token')),
	name             TEXT NOT NULL,
	target_id        TEXT NOT NULL,
	secret_hash      TEXT NOT NULL,
	secret_enc       TEXT NOT NULL,
	event_allow      TEXT CHECK (event_allow IS NULL OR json_valid(event_allow)),
	priority         TEXT NOT NULL DEFAULT 'wake_idle' CHECK (priority IN ('wake', 'wake_idle', 'hold')),
	sends_per_min    INT  NOT NULL DEFAULT 60,
	threads_per_hour INT  NOT NULL DEFAULT 20,
	created_at       TEXT NOT NULL,
	disabled         INT  NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1))
) STRICT;

-- Redelivery dedupe: one row per accepted provider delivery id (R09).
CREATE TABLE source_deliveries (
	src_id      TEXT NOT NULL,
	provider_id TEXT NOT NULL,
	msg_id      TEXT NOT NULL,
	at          TEXT NOT NULL,
	PRIMARY KEY (src_id, provider_id)
) STRICT;

-- One thread per (source, external conversation) (R09).
CREATE TABLE source_threads (
	src_id           TEXT NOT NULL,
	origin_thread_id TEXT NOT NULL,
	thr_id           TEXT NOT NULL,
	PRIMARY KEY (src_id, origin_thread_id)
) STRICT;
