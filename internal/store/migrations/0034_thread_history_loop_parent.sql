-- Preserve loop limits without retaining Message bodies. Populated lazily by
-- history GC in the same transaction that removes a non-reaction Message.
CREATE TABLE thread_history_loop_parents (
 thr_id TEXT PRIMARY KEY REFERENCES threads(thr_id),
 msg_id TEXT NOT NULL,
 seq INTEGER NOT NULL CHECK(seq>0),
 loop TEXT NOT NULL CHECK(json_valid(loop) AND json_type(loop)='object')
) STRICT;
