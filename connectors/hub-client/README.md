# Shared hub client

Dependency-free TypeScript wire types, ports and retry helpers for vecnet connectors. The launcher type-checks this package together with its own source using `just ln-typecheck`; `just ln-test` runs both packages' tests. `testing/` provides an HTTP fake with SSE replay and long-poll, fixtures, a deterministic clock and an in-memory log. Relative imports use `.ts` so Node 22.18 or newer can run the erasable TypeScript directly.
