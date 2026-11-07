-- Bounded expiry scans oldest keys without inspecting retained response bytes.
CREATE INDEX idempotency_created_at ON idempotency(created_at);
