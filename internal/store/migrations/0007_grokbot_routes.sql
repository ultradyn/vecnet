-- Grok Bot routine routes (PROTOCOL §13.3). SQLite cannot alter a CHECK, so
-- routes is recreated; ids, URLs and verification survive unchanged, and
-- route_keys, which refers to routes by id, is untouched.
-- key_enc: the routine's crsr_ key sealed under meta.hub_key; NULL on http
-- routes, whose keys live in route_keys.
-- runs_per_hour: the routine's run budget (200 per routine, grokbot.md §3).
CREATE TABLE routes_0007 (
	route_id      TEXT PRIMARY KEY,
	owner_kind    TEXT NOT NULL CHECK (owner_kind IN ('peer', 'principal')),
	owner_id      TEXT NOT NULL,
	kind          TEXT NOT NULL DEFAULT 'http' CHECK (kind IN ('http', 'grokbot')),
	url           TEXT NOT NULL,
	verified_at   TEXT,
	created_at    TEXT NOT NULL,
	key_enc       TEXT,
	runs_per_hour INT  NOT NULL DEFAULT 200
) STRICT;

INSERT INTO routes_0007(route_id, owner_kind, owner_id, kind, url, verified_at, created_at)
SELECT route_id, owner_kind, owner_id, kind, url, verified_at, created_at FROM routes;

DROP TABLE routes;
ALTER TABLE routes_0007 RENAME TO routes;
CREATE INDEX routes_owner ON routes(owner_kind, owner_id);

-- One row per routine run in the trailing hour (Hub.GrokbotBudgetOK).
CREATE TABLE route_runs (
	route_id TEXT NOT NULL,
	at       TEXT NOT NULL
) STRICT;
CREATE INDEX route_runs_route_at ON route_runs(route_id, at);
