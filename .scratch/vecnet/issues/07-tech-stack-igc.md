# Tech stack for hub, connectors and CLI

Type: research
Status: resolved
Blocked by:

## Question

Max delegated this. Decide language/runtime, web framework, SQLite access layer, MCP server library, SSE handling, blob storage layout, and how the `vecnet` CLI is distributed, with an IGC matrix. Constraints: pag-chat and webhook2mcp are Node/TypeScript; an MCP SDK must exist; one person operates it; anything compiled must build on x-alpha not xsm (AVX-512 stamp hazard); dev server ports are generated, not defaults. Deliverable: `docs/research/stack.md` + draft ADR in `docs/adr/`.

## Answer

**Node.js 26 LTS + TypeScript, Hono on `@hono/node-server`, built-in `node:sqlite` with w2m's checksummed SQL migrations, `@modelcontextprotocol/server` v2 for standard MCP, and a hand-rolled MCP Events layer lifted from w2m.** One process and one Docker image, shipped with `fly deploy --remote-only`.

- **Why TypeScript on Node.** No official MCP SDK implements MCP Events (SEP-3415 is an open draft), so every stack would hand-roll it. w2m already does, in production, in TypeScript on Node with `node:sqlite`.
- **Why the rest lost.** Rust, Python and Go cannot share types with pag-chat, the CLI and web UI without codegen. Bun has hot-path caveats (SSE idle timeout, `node:sqlite`). `better-sqlite3` would rewrite w2m's storage layer.
- **Blob store.** Files go at `/data/blobs/sha256/aa/bb/<hex>`, written via temp file, fsync and rename. Metadata lives in SQLite.
- **CLI.** It ships as an npm package from the same workspace, bundled to one file for Node 22 or later, and installs from a tarball or git URL. A Bun-compiled binary is the fallback for hosts without Node.
- **Dev port.** The hub dev server uses 45752, which was generated, not a default.
- **AVX-512.** Moot: no native addons, and the image is built off xsm.

Research and IGC matrices: [`docs/research/stack.md`](../../../docs/research/stack.md). Draft ADR: [`docs/adr/0001-tech-stack.md`](../../../docs/adr/0001-tech-stack.md).

## Addendum 2026-11-01 (Max, by voice): decision revised to Go

Max rejected the Node/TypeScript pick: he wants a static binary, more control, and is not keen on Node's performance profile; he prefers Go over Rust ("performance is not the biggest issue, Go gives a static binary and works well with AI coding"). He also stated vecnet is *inspired by* webhook2mcp, not bound to reuse its code. That refutes two goals the matrix used:

- **G6 (reuse w2m code verbatim) is dropped.** Not a goal Max holds. MCP Events is a hand-roll in every stack; porting w2m's TS logic is reference reading, not a constraint.
- **G5 (type sharing without codegen) is not decisive.** The wire contract is JSON Schema in `docs/`; pag-chat generates TS types from it. Codegen from one schema is routine.
- **New goal G9: the hub and the CLI ship as single static binaries with no runtime to install.** Node and Bun fail; Go and Rust pass.
- **G7 strengthens for Go.** `CGO_ENABLED=0` Go binaries link no glibc CRT, so even a binary built on xsm carries no AVX-512 ISA stamp (verify once with `readelf -n`). Rust links the C runtime and keeps the x-alpha-only rule.

Survivors on the revised goals: Go and Rust. Max's stated preference and the G7 advantage break the tie: **Go**. Stack: Go 1.26+, `net/http` (stdlib mux) , official `modelcontextprotocol/go-sdk` for MCP, hand-rolled MCP Events, `modernc.org/sqlite` (pure Go, WAL) unless a benchmark forces mattn/cgo, SQL migrations as embedded files, blob layout unchanged, CLI as a second `cmd/` in the same module, one scratch-image Docker deploy to Fly. Dev port 45752 stands. ADR 0001 rewritten accordingly.
