-- dispatching counts outbound attempts in flight for a delivery that bypasses
-- the push claim (an Events callback or an operator notification). Reaction
-- supersession (PROTOCOL §17.5) skips a delivery while it is nonzero, so a
-- wake that has started to go out is never withdrawn. The hub resets it at
-- start: no attempt survives a restart.
ALTER TABLE deliveries ADD COLUMN dispatching INT NOT NULL DEFAULT 0 CHECK (dispatching >= 0);
