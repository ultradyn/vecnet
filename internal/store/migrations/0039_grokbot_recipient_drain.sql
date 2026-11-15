-- Explicit capability; omission and all historical routes retain Delivery v1.
ALTER TABLE routes ADD COLUMN grokbot_transport TEXT NOT NULL DEFAULT 'delivery_v1'
 CHECK (grokbot_transport IN ('delivery_v1', 'recipient_drain_v1'));
ALTER TABLE route_runs ADD COLUMN grok_wake_token TEXT;
-- Settlement fence, not invocation evidence: today's HTTP-200 reservation
-- cannot be refunded by a duplicate result after its coordinator rotates.
ALTER TABLE route_runs ADD COLUMN grok_wake_settled_200 INTEGER NOT NULL DEFAULT 0
 CHECK (grok_wake_settled_200 IN (0,1));
CREATE UNIQUE INDEX route_runs_grok_wake_token ON route_runs(grok_wake_token)
 WHERE grok_wake_token IS NOT NULL;

-- No route/representative FK: deletion must not erase a live exclusion gate.
CREATE TABLE grokbot_wakes (
 route_id TEXT NOT NULL,
 recipient TEXT NOT NULL,
 representative_id TEXT NOT NULL DEFAULT '',
 token TEXT NOT NULL DEFAULT '',
 phase TEXT NOT NULL CHECK (phase IN ('claimed','armed','waiting','covered')),
 lease_until TEXT NOT NULL DEFAULT '',
 retry_at TEXT NOT NULL DEFAULT '',
 cover_until TEXT NOT NULL DEFAULT '',
 pending_count INTEGER NOT NULL DEFAULT 0,
 failure_ordinal INTEGER NOT NULL DEFAULT 0,
 idle_counter INTEGER NOT NULL DEFAULT 0,
 begin_idle_counter INTEGER NOT NULL DEFAULT 0,
 cover_valid INTEGER NOT NULL DEFAULT 0 CHECK (cover_valid IN (0,1)),
 expected_url TEXT NOT NULL DEFAULT '',
 expected_principal TEXT NOT NULL DEFAULT '',
 expected_key TEXT NOT NULL DEFAULT '',
 frozen_body BLOB,
 PRIMARY KEY (route_id, recipient)
) STRICT;
CREATE INDEX grokbot_wakes_deadlines ON grokbot_wakes(lease_until,retry_at,cover_until);
CREATE TABLE grokbot_covers (
 route_id TEXT NOT NULL,
 delivery_id TEXT NOT NULL REFERENCES deliveries(dly_id) ON DELETE CASCADE,
 recipient TEXT NOT NULL,
 first_covered_at TEXT NOT NULL,
 PRIMARY KEY (route_id, delivery_id)
) STRICT;
CREATE INDEX grokbot_covers_first ON grokbot_covers(route_id,first_covered_at,delivery_id);
CREATE INDEX deliveries_grok_domain ON deliveries(route_id,recipient,event_seq,state,held,expires_at)
 WHERE state IN ('queued','pushed') AND priority IN ('wake','wake_idle');
CREATE INDEX deliveries_grok_due ON deliveries(next_attempt_at,event_seq,route_id,recipient)
 WHERE state IN ('queued','pushed') AND priority IN ('wake','wake_idle') AND held=0;

CREATE INDEX deliveries_grok_cursor ON deliveries(event_seq,dly_id,route_id,recipient)
 WHERE state IN ('queued','pushed') AND priority IN ('wake','wake_idle');
