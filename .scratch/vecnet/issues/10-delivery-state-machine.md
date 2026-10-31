# Delivery state machine

Type: grilling
Status: claimed
Blocked by: 08

## Question

Per-delivery states and transitions: queued -> pushed -> accepted -> processed / failed -> retry (backoff schedule, bounded attempts, `Retry-After`) -> dead-letter; dedupe rules (same `delivery_id` + digest = same receipt; different bytes = 409); per-thread ordering and what it means for parallel recipients; how "hold until next turn" deliveries are parked and released; what the operator sees and can do with dead letters.
