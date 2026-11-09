# Bound event-pump abort bookkeeping

Goal: fix the shared Web UI event pump's source-confirmed abort lifetime accumulation under issue #60; do not claim attribution of the multi-GB incident. Base3d06953f in fix/webui-pump-lifetime.

## Global Constraints

Use isolated worktree and explicit GPT-6.1-Sol implementation and independent review. Parent owns all x-boss execution, aggregate max2jobs. Synthetic API fixtures/private temporary homes only; no real users, secrets or browser. Keep native release3d frozen. Preserve reload/ack order, abort-while-settling, API error behavior and final connection state. No API/schema additions.

## Review Focus

Each settled wait removes its abort bookkeeping; nothing persists after disabled/auth stop without signal abort. An abort during outstanding resource load must stop promptly, publish no ack and leave no listeners. Failure/retry/409 behavior remains as documented and tested. Existing store test oracles remain unchanged. Source accumulation is distinct from measured renderer memory attribution.

## Task 1: Tests then production

Files: create internal/ui/jstest/store-pump-lifetime.test.mjs; after parent genuine RED, modify internal/ui/static/store.js only.

- [x] Source-only test author uses real createStore, real AbortSignal listener accounting and gated resource/API completion to demonstrate listener lifetime after repeated successful settlement and normal terminal/auth paths, plus abort pending-load safety controls. Tests should exercise semantic lifetime boundaries rather than source text or Promise.race replacement.
- [x] Parent runs tests against unchanged source on x-boss; classify actual RED/control output.
- [x] Production replaces the per-session unresolved abort race with per-wait listener cleanup on either completion, preserving all safety/ordering.
- [x] Parent focused GREEN, full UI and relevant Go embedding checks. Independent Sol review and fix loop.

## Task 2: Integrate and measure

- [ ] Preserve dashboard measurements as separate evidence, add real-browser steady event-load measurement if a remaining memory claim depends on it.
- [ ] Commit reviewed source and dated changelog, publish both remotes, serial integration against fresh master with full gates/screenshots. Do not close #60 from a bounded component test or source-only review.

--- SUMMARY ---

Bound the event pump's abort wait lifetime, establish regression evidence before production edits, preserve ack and abort semantics, and keep incident attribution and final integration explicit.
