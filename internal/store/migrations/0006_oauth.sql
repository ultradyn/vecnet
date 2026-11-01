-- OAuth 2.1 for dots (PROTOCOL §4.5): the hub is its own authorization server
-- and the resource server for /mcp. Codes, tokens and link codes are stored
-- only as the lower-hex SHA-256 of their plaintext (§12).

-- metadata: the client's metadata as registered, or its CIMD document.
-- redirect_uris: a JSON array. For kind 'cimd', client_id is the document's
-- URL and created_at is when it was last fetched (the cache is 1 h old from
-- there); this row is the cache.
CREATE TABLE oauth_clients (
	client_id     TEXT PRIMARY KEY,
	kind          TEXT NOT NULL CHECK (kind IN ('dcr', 'cimd', 'static')),
	metadata      TEXT NOT NULL CHECK (json_valid(metadata)),
	redirect_uris TEXT NOT NULL CHECK (json_valid(redirect_uris)),
	created_at    TEXT NOT NULL
) STRICT;

-- One row per ChatGPT account. prn_id is a vecnet principal; subject is
-- "oauth:" + first client_id + ":" + a random stable id issued at first
-- consent; peer_id is the bound dot peer. A peer has one OAuth principal.
CREATE TABLE oauth_principals (
	prn_id     TEXT PRIMARY KEY REFERENCES principals(prn_id),
	subject    TEXT NOT NULL UNIQUE,
	peer_id    TEXT UNIQUE REFERENCES peers(peer_id),
	created_at TEXT NOT NULL
) STRICT;

CREATE TABLE oauth_codes (
	code_hash      TEXT PRIMARY KEY,
	client_id      TEXT NOT NULL,
	redirect_uri   TEXT NOT NULL,
	scope          TEXT NOT NULL,
	code_challenge TEXT NOT NULL,
	resource       TEXT NOT NULL,
	principal_id   TEXT NOT NULL REFERENCES oauth_principals(prn_id),
	expires_at     TEXT NOT NULL,
	used_at        TEXT
) STRICT;
CREATE INDEX oauth_codes_expires ON oauth_codes(expires_at);

-- rotated_from links a token family, so reuse can revoke all of it: a refresh
-- token names the refresh token it replaced, or the hash of the code that
-- started the family; an access token names the refresh token minted with it.
CREATE TABLE oauth_tokens (
	token_hash   TEXT PRIMARY KEY,
	kind         TEXT NOT NULL CHECK (kind IN ('access', 'refresh')),
	client_id    TEXT NOT NULL,
	principal_id TEXT NOT NULL REFERENCES oauth_principals(prn_id),
	scope        TEXT NOT NULL,
	resource     TEXT NOT NULL,
	expires_at   TEXT NOT NULL,
	revoked_at   TEXT,
	rotated_from TEXT,
	created_at   TEXT NOT NULL
) STRICT;
CREATE INDEX oauth_tokens_rotated_from ON oauth_tokens(rotated_from);

-- One-time codes printed by `vecnet peer link` (PROTOCOL §4.5, R15).
CREATE TABLE dot_links (
	code_hash  TEXT PRIMARY KEY,
	peer_id    TEXT NOT NULL REFERENCES peers(peer_id),
	expires_at TEXT NOT NULL,
	used_at    TEXT
) STRICT;
