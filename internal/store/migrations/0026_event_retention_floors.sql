-- Durable owner floors, independent of other owners' retained rows. Preserve
-- the known legacy global prefix on upgrade; new owners start at zero.
CREATE TABLE event_retention (
    owner TEXT PRIMARY KEY,
    purged_through_seq INTEGER NOT NULL DEFAULT 0 CHECK(purged_through_seq >= 0),
    updated_at TEXT NOT NULL
) STRICT;
-- Capture scalar legacy bounds only. Owner enumeration is checkpointed by
-- initializeEventRetention in one-owner transactions before Open returns.
INSERT INTO meta(key,value) VALUES('event_retention_legacy_floor',
 CAST(coalesce((SELECT min(seq)-1 FROM events),
    (SELECT seq FROM sqlite_sequence WHERE name='events'),0) AS TEXT));
INSERT INTO meta(key,value) VALUES('event_retention_init_owner','');
INSERT INTO meta(key,value) VALUES('event_retention_init_done','0');

-- Also protect legacy deletion paths: a committed removal can never silently
-- leave a readable cursor below the missing event. Normal GC deletes prefixes.
CREATE TRIGGER events_retention_floor AFTER DELETE ON events BEGIN
    INSERT INTO event_retention(owner,purged_through_seq,updated_at)
    VALUES(OLD.owner,OLD.seq,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    ON CONFLICT(owner) DO UPDATE SET
      purged_through_seq=max(purged_through_seq,excluded.purged_through_seq),
      updated_at=excluded.updated_at;
END;

-- Original logs use epoch 1. New acknowledgements explicitly snapshot epoch;
-- a rebuilt log cannot consume acknowledgements from an older epoch.
ALTER TABLE acks ADD COLUMN epoch INTEGER NOT NULL DEFAULT 1;


-- Pre-upgrade rows require the owner's explicit apply-existing approval. New
-- rows use the approved seven-day default without retroactive activation.
INSERT INTO meta(key,value) VALUES('event_retention_initial_high_water',
 CAST(coalesce((SELECT seq FROM sqlite_sequence WHERE name='events'),0) AS TEXT));
