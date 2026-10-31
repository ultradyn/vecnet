-- Which observation set peers.reachability (PROTOCOL §8): 'call' (an
-- authenticated inbound call, or a stream; "reachable" decays to unknown
-- 24 h after last_seen) or 'push' (a push result; no window). NULL means no
-- observation yet, or a row from before this migration (treated as 'call').
ALTER TABLE peers ADD COLUMN reach_via TEXT CHECK (reach_via IN ('call', 'push'));
