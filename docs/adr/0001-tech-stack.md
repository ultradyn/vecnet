# ADR 0001: Tech stack for the hub, connectors and CLI

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** delegated to the agent by Max (ticket 07); Max may veto
- **Research:** [`docs/research/stack.md`](../research/stack.md)

## Context

vecnet is one self-hosted hub that all of Max's agents message through. The hub must:

- push signed webhooks (Standard Webhooks) with retries and dead letters;
- accept HTTP sends and serve resumable SSE subscriptions;
- run an MCP Streamable HTTP server that includes **MCP Events**, the only way to reach OpenAI dots;
- keep state in SQLite and attachments in a content-addressed blob store on disk;
- ship a `vecnet` CLI for coding sessions, and later a web inbox and chat UI.

The constraints that shape the choice:

- **One operator.** Max runs it alone, and agents write the code.
- **Small host.** Ticket 05 recommends one Fly `shared-cpu-1x` 512 MB machine with a volume.
- **MCP Events has no SDK support.** The extension (SEP-3415) is an open draft that no official MCP SDK implements in any language, so Events must be hand-written whatever the stack.
- **w2m is the prior art.** Max's webhook2mcp already implements MCP `2026-07-28` with Events, Standard Webhooks signing, retrying delivery and a `node:sqlite` migration runner. It is TypeScript on Node and live in production.
- **pag-chat is TypeScript.** pag-chat, the first external consumer, plans a TypeScript server.
- **No xsm-linked binaries.** Anything linked on xsm is stamped AVX-512 and must not be deployed.
- **Generated ports.** Dev server ports are generated, not framework defaults.

## Decision

- **Language and runtime:** TypeScript (ESM) on **Node.js 26 LTS**, bundled with esbuild and pinned in the Dockerfile and `engines`.
- **Web framework:** **Hono** on `@hono/node-server`. SSE uses `streamSSE` with event ids from a monotonic SQLite event log, `Last-Event-ID` resume and a heartbeat about every 15 s.
- **SQLite:** built-in **`node:sqlite`**, configured with WAL, `synchronous=FULL`, `busy_timeout`, foreign keys and STRICT tables.
  - Migrations reuse w2m's checksummed `NNNN_name.sql` runner.
  - Queries are hand-written SQL in a typed repository module, with no ORM.
  - Every `node:sqlite` import stays inside one storage module.
- **MCP:** **`@modelcontextprotocol/server` v2** with the `@modelcontextprotocol/hono` helper handles discovery, tools and Streamable HTTP. **MCP Events is hand-rolled**, lifted from w2m and kept behind an interface so an SDK implementation can replace it later.
- **Webhook push:** Standard Webhooks HMAC and the retry and receipt semantics are lifted from w2m.
- **Shared types:** zod 4 schemas in an `@vecnet/protocol` workspace package. It also exports JSON Schema for peers that do not use TypeScript.
- **Blob store:** files live at `/data/blobs/sha256/<aa>/<bb>/<hex>`.
  - Each write goes to a temp file, is fsynced, then renamed into place.
  - Metadata rows live in SQLite.
  - Ticket 13 owns policy: upload rights, TTL and read tokens.
- **Repo layout:** npm workspaces with `packages/protocol`, `apps/hub`, `apps/cli` and later `apps/web`, plus a `justfile`. Tests use vitest. Builds and tests on xsm run via `build2`.
- **CLI distribution:** an **npm package** with a `vecnet` bin, bundled to one ESM file and requiring Node 22 or later.
  - Install it with `npm i -g` from a tarball or git URL; no public registry is required.
  - A Bun-compiled single binary is the fallback for any host without Node.
- **Deploy:** one image built `FROM node:26-slim` and shipped with `fly deploy --remote-only`.
- **Dev port:** the hub dev server uses **45752**, generated once and confirmed free on xsm. The web UI's dev server gets its own generated port when it is created.

## Consequences

**Good.**

- **Production-proven code is reused.** The riskiest protocol code (Events, signing, delivery, migrations) is copied from w2m instead of re-implemented.
- **One language end to end.** Hub, CLI, web UI and pag-chat share types and client code directly.
- **No native addons and no host linking.** The AVX-512 hazard cannot reach production.
- **It fits the host.** One process and one image fit a 512 MB Fly machine.

**Bad, and the mitigations.**

- **`node:sqlite` is still a release candidate** (Stability 1.2). Mitigation: a single storage module, so `better-sqlite3` can be swapped in.
- **Hand-rolled MCP Events can drift** from SEP-3415 or OpenAI's profile. Mitigation: follow OpenAI's published profile as w2m does, and keep it behind an interface.
- **The CLI needs Node on each session host.** Node 22+ is present on xsm, x-tuf and x-tall. x-alpha is unverified.
- **Lifting w2m code assumes Max permits reuse** from the private `xpp-tm` repo.

## Alternatives considered

The full IGC matrix is in [`stack.md`](../research/stack.md) §2.

- **Node + Fastify + `better-sqlite3`.** Rejected: it rewrites w2m's storage layer and adds a native addon. Fastify with `node:sqlite` also survives the matrix. Hono was picked over it as a minor, non-decisive preference: its fetch-native handlers match w2m.
- **Bun + Hono.** Rejected because hot-path runtime caveats remain: quiet SSE streams close after 10 s by default, and `node:sqlite` has documented differences. It also moves off the runtime w2m was proven on.
- **Rust (axum + sqlx + rmcp), Python (FastAPI + `mcp`), Go (go-sdk).** Each has a Tier 1 MCP SDK. All are rejected because they cannot share types with the TypeScript pag-chat, CLI and web UI without code generation. They would also re-implement MCP Events, signing and delivery from scratch.
- **Node SEA for the CLI.** Rejected because the feature is Stability 1.1, active development.
