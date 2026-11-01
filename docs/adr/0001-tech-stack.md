# ADR 0001: Tech stack for the hub, connectors and CLI

- **Status:** Accepted (Go), 2026-11-01. Supersedes the same-day Proposed draft that chose Node/TypeScript.
- **Deciders:** Max (language), agent (components). Max delegated the stack in ticket 07, then vetoed Node by voice.
- **Research:** [`docs/research/stack.md`](../research/stack.md) (original matrix) and the addendum in ticket 07.

## Context

vecnet is one self-hosted hub that all of Max's agents message through. The hub must:

- push signed webhooks (Standard Webhooks) with retries and dead letters;
- accept HTTP sends and serve resumable SSE subscriptions;
- run an MCP Streamable HTTP server with **MCP Events**, the only way to reach OpenAI dots;
- keep state in SQLite and attachments in a content-addressed blob store on disk;
- ship a `vecnet` CLI that runs inside coding sessions on several machines, and later a web inbox and chat UI.

Constraints that shape the choice:

- **One operator.** Max runs it alone; agents write the code.
- **Small host.** One always-on Fly `shared-cpu-1x` 512 MB machine with a volume (ticket 05, confirmed by Max).
- **MCP Events has no SDK support.** SEP-3415 is an open draft no official SDK implements, so Events is hand-written in any stack.
- **webhook2mcp is inspiration, not a constraint.** Max: vecnet borrows w2m's ideas (id separation, signing, receipt vs `job.status`, the Events profile) but is not obliged to reuse its TypeScript.
- **Max wants a static binary and control.** He is not keen on Node's runtime and dependency profile, and the CLI must install on many hosts without a runtime.
- **No xsm-linked C runtime in deployed binaries.** Anything that links glibc on xsm is stamped AVX-512 and dies elsewhere.
- **Generated ports.** Dev server ports are generated, not framework defaults.

## Decision

- **Language:** **Go** (current stable, 1.26 or later), one module, `cmd/vecnet-hub` and `cmd/vecnet` (the CLI). `CGO_ENABLED=0` everywhere.
- **HTTP:** standard library `net/http` with the Go 1.22+ pattern mux. No framework. SSE is a hand-written handler: `text/event-stream`, flush per event, event ids from the per-peer SQLite event-log sequence, `Last-Event-ID` replay, a heartbeat comment about every 15 s (ticket 06).
- **MCP:** the official **`github.com/modelcontextprotocol/go-sdk`** for discovery, tools and Streamable HTTP. **MCP Events is hand-rolled** (`events/list`, `events/subscribe`, verification challenge, signed webhook delivery, cursors), written from OpenAI's published profile with w2m's `src/worker.ts` as reference reading, and kept behind an interface so an SDK implementation can replace it later.
- **MCP amendment (2026-11-01, connectors plan Task 2):** `/mcp` is **hand-rolled** JSON-RPC 2.0 on `net/http` in `internal/mcpserver`, dual-era (2026-07-28 and 2025-11-25 on one endpoint). The go-sdk server cannot host MCP Events: its request dispatch (`handleReceive` → `checkRequest`) rejects unknown methods such as `events/subscribe` before any middleware runs, and its `ServerCapabilities` has no top-level `events` field, which ChatGPT reads. Do not use `mcp.Server` or `NewStreamableHTTPHandler`. The SDK stays a dependency for wire types where they fit and as the 2025-era client in tests (`mcp.NewClient` + `mcp.StreamableClientTransport`). This supersedes the MCP bullet above where they conflict.
- **SQLite:** **`modernc.org/sqlite`** (pure Go, keeps `CGO_ENABLED=0`), WAL, `synchronous=FULL`, `busy_timeout`, foreign keys, STRICT tables. Migrations are ordered `NNNN_name.sql` files embedded with `embed.FS`, checksummed on apply. Hand-written SQL behind a small repository package; no ORM. Switch to `mattn/go-sqlite3` only if a benchmark on the real workload forces it, and then the build moves to x-alpha.
- **Webhook push:** Standard Webhooks HMAC-SHA256 (`webhook-id`, `webhook-timestamp`, `webhook-signature`), retry and receipt semantics as in ticket 10, verified in tests against an independent implementation.
- **Wire contract:** JSON Schema files in `docs/protocol/` are the source of truth. Go types are hand-written to match and checked against the schema in tests. pag-chat and other TypeScript consumers generate their types from the same schema.
- **Blob store:** files at `/data/blobs/sha256/<aa>/<bb>/<hex>`; write to a temp file in the same filesystem, fsync, rename. Metadata rows in SQLite. Ticket 13 owns policy.
- **CLI distribution:** a **single static binary** per OS/arch from `go build`, published as GitHub release assets; `go install` also works. No runtime on the host.
- **Deploy:** multi-stage Dockerfile, final stage `FROM scratch` (plus CA certs and tzdata), shipped with `fly deploy --remote-only`.
- **Tests:** `go test` with a smoke test that boots the real server on a random port. On xsm run through `build2`, two threads.
- **Dev port:** the hub dev server uses **45752** (generated once, confirmed free). The web UI gets its own generated port when ticket 18 creates it.

## Consequences

**Good.**

- **Static binaries by construction.** Hub and CLI are single files; the CLI installs anywhere with a copy. No Node, no `node_modules`, no supply-chain surface beyond Go modules.
- **AVX-512 hazard removed, not managed.** Pure-Go binaries link no glibc CRT, so a build on xsm carries no ISA stamp. Verify once with `readelf -n` and keep a CI check.
- **Long-lived connections are cheap.** Goroutines per SSE stream fit the 512 MB host.
- **Official MCP SDK.** Streamable HTTP and tools come from the maintained Go SDK.

**Bad, and the mitigations.**

- **MCP Events, signing and delivery are written fresh.** Mitigation: w2m is the reference, OpenAI's profile is the contract, and conformance tests use recorded w2m payloads from `docs/research/w2m-envelope.md`.
- **No shared TypeScript types with pag-chat.** Mitigation: JSON Schema is the contract and pag-chat generates from it; schema changes are versioned.
- **Pure-Go SQLite is slower than cgo SQLite.** Irrelevant at this scale; the escape hatch is `mattn/go-sqlite3` behind the repository package.
- **Hand-written SSE.** Small, but it must get flushing, cancellation and heartbeat right. Mitigation: an integration test that drops and resumes a stream.

## Alternatives considered

- **Node 26 + TypeScript + Hono + `node:sqlite`.** The agent's original pick; survived a matrix whose goals included reusing w2m code and sharing types without codegen. Max dropped both goals and added "static binary, no runtime", which Node fails. Rejected.
- **Bun.** Fails the same goal and has hot-path caveats (quiet SSE streams close after 10 s by default). Rejected.
- **Rust (axum + sqlx + rmcp).** Passes every revised goal. Rejected on Max's stated preference for Go (performance is not the constraint; iteration speed with AI coding agents is) and because Rust links the C runtime, so the x-alpha-only build rule would stay.
- **Python (FastAPI + `mcp`).** Fails the static-binary goal. Rejected.
