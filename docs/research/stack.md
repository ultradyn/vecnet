# Tech stack for the vecnet hub, connectors and CLI

_Researched 2026-11-01 by rs-07-stack for ticket 07. Sources are primary: official SDK repos, Node/Bun/Hono docs read via the GitHub API, the MCP spec repo, and Max's own webhook2mcp ("w2m") checkout. Nothing was built or installed._

## Answer

**Node.js 26 LTS + TypeScript, Hono on `@hono/node-server`, built-in `node:sqlite` with numbered SQL migrations, `@modelcontextprotocol/server` v2 for standard MCP, and a hand-rolled MCP Events layer lifted from w2m.** One process, one Docker image, deployed by `fly deploy` (ticket 05). The `vecnet` CLI is an npm package from the same workspace, installable without a public registry. The hub dev server uses generated port **45752**.

The decisive facts:

- **No official MCP SDK implements MCP Events**, in any language. The extension is still an open draft. Every stack would hand-roll it [F3].
- **w2m already hand-rolls it in production**, in TypeScript on Node with `node:sqlite`, alongside Standard Webhooks signing and retrying delivery [F8]. Only a Node/TypeScript stack can lift that code instead of porting it.
- **pag-chat plans a TypeScript server**, so a TypeScript protocol package can be shared with it directly, with no code generation [F9].

## 1. Facts

### MCP SDKs

| # | Fact | Source |
|---|---|---|
| F1 | TypeScript, Python, C#, Go, Rust and Ruby are the six **Tier 1** SDKs as of 2026-09-29. Tier 1 means 100% conformance, triage within 2 business days and critical fixes within 7 days. | [Ruby Tier 1 post](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/blog/content/posts/2026-09-29-ruby-sdk-tier-1.md), [SDK tiers](https://modelcontextprotocol.io/community/sdk-tiers) |
| F2 | All four candidate SDKs implement the current spec revision `2026-07-28`, including Streamable HTTP servers. TypeScript v2 is `@modelcontextprotocol/server` with `/node`, `/hono`, `/fastify` and `/express` helpers. v1 (`@modelcontextprotocol/sdk`) gets fixes for at least six months after 2026-07-27. Python's README lists Streamable HTTP. Go v1.7.0+ lists `2026-07-28`. Rust `rmcp` says it implements the stable `2026-07-28` spec; it was beta on spec day. | [TS README](https://github.com/modelcontextprotocol/typescript-sdk), [Python README](https://github.com/modelcontextprotocol/python-sdk), [Go README](https://github.com/modelcontextprotocol/go-sdk), [Rust README](https://github.com/modelcontextprotocol/rust-sdk), [spec GA post](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/blog/content/posts/2026-07-28-spec-ga/index.md) |
| F2b | Latest releases: TS v2.4.0 (2026-10-23), Python v2.3.0 (2026-10-02), Go v1.8.0 (2026-09-14), Rust rmcp v3.8.0 (2026-10-26). All four repos were pushed to within the last week. | `gh api repos/modelcontextprotocol/<repo>/releases/latest`, read 2026-11-01 |
| F3 | **MCP Events is not in any SDK.** A code search for `events/subscribe` returns 0 hits in all four SDK repos. SEP-3415 "Events Extension" is an open, unmerged draft PR, last updated 2026-10-31. Its text says "the reference implementation in an official SDK is outstanding". ChatGPT and dots implement OpenAI's own webhook-only profile of the draft. | GitHub code search 2026-11-01; [PR #3415](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/3415); `pag-chat/docs/research/prelim/dots-mcp-events.md` §3 |

### Runtimes, storage and HTTP

| # | Fact | Source |
|---|---|---|
| F4 | Node 26 entered **LTS on 2026-10-28** and is supported until 2029-04-30. Node 24 moved to maintenance on 2026-10-20. The latest release is v26.13.0. | [nodejs/Release schedule.json](https://github.com/nodejs/Release/blob/main/schedule.json) |
| F5 | `node:sqlite` is **Stability 1.2, release candidate**. It was added in v22.5.0. In current docs the class is `Database`, and `DatabaseSync` is a deprecated alias kept for compatibility. It is built in, so it needs no native addon or compile step. | [doc/api/sqlite.md](https://github.com/nodejs/node/blob/main/doc/api/sqlite.md) |
| F6 | Node single executable applications (SEA) are **Stability 1.1, active development**. | [doc/api/single-executable-applications.md](https://github.com/nodejs/node/blob/main/doc/api/single-executable-applications.md) |
| F7 | Bun v1.4.4 (2026-10-22) implements `node:sqlite` fully, with documented differences: for example, `backup()` blocks the event loop. `Bun.serve` closes connections after **10 s idle by default**, which drops a quiet SSE stream unless `server.timeout(req, 0)` is set. `bun build --compile` cross-compiles single binaries that target Nehalem and pick AVX2/AVX-512 paths at runtime. | [nodejs-compat.mdx](https://github.com/oven-sh/bun/blob/main/docs/runtime/nodejs-compat.mdx), [guides/http/sse.mdx](https://github.com/oven-sh/bun/blob/main/docs/guides/http/sse.mdx), [runtime/http/server.mdx](https://github.com/oven-sh/bun/blob/main/docs/runtime/http/server.mdx), [compiler/executables.mdx](https://github.com/oven-sh/bun/blob/main/docs/compiler/executables.mdx) |
| F7b | Hono v4.13.14 provides `streamSSE()` with `id`/`event` fields and `onAbort`. `@hono/node-server` v2.1.5 runs it on Node. Hono handlers are fetch-style (`Request` in, `Response` out). | [Hono streaming helper](https://github.com/honojs/website/blob/main/docs/helpers/streaming.md), releases via `gh api` |
| F7c | `better-sqlite3` is a native addon. It ships prebuilt binaries for major platforms and falls back to a local `node-gyp` compile. | [better-sqlite3 README](https://github.com/WiseLibs/better-sqlite3), [troubleshooting](https://github.com/WiseLibs/better-sqlite3/blob/master/docs/troubleshooting.md) |

### Max's existing code and constraints

| # | Fact | Source |
|---|---|---|
| F8 | w2m (`xpp-server-gpt-webhook2mcp` v1.1.0) is TypeScript ESM on Node `>=24.19.0` with zod 4, TypeScript 7, vitest and esbuild. It uses `node:sqlite` with WAL, `synchronous=FULL`, `busy_timeout`, and a checksummed `NNNN_name.sql` migration runner (`selfhost/sqlite.ts`). It hand-rolls MCP `2026-07-28` and the Events profile (`src/worker.ts`), Standard Webhooks signing (`src/crypto.ts`) and retrying delivery (`selfhost/communication-delivery.ts`). Its handlers are fetch-style and mounted on bare `node:http`. It is live in production. | `/home/xertrov/src/w2m-ops-admin` `package.json`, the files named; `docs/research/transports-facts.md` |
| F9 | pag-chat has research docs only so far. Its architecture notes recommend a "single TypeScript server" and "TypeScript/Node … shared schemas and simple event streaming". | `pag-chat/docs/research/grokbot_research_and_architecture_chatlog_report.md` §16–17 |
| F10 | The xsm host stamps every binary it links as needing x86-64-v4 (AVX-512), so such binaries refuse to load on lower-ISA hosts. Compiled artefacts must come from x-alpha, x-tuf or a remote builder. | `~/.llm-general/systems/xsm/xsm-isa-stamp-build-hazard.md` |
| F11 | Ticket 05 recommends one Fly `shared-cpu-1x` machine with 512 MB RAM and a 3 GB volume at `/data`, deployed with `fly deploy`. | `docs/research/hosting.md` |
| F12 | Node is installed on xsm (v22.23.3), x-tuf (v22.22.1, which also has Bun) and x-tall (v24.22.1). x-alpha did not resolve from xsm at check time. | `ssh <host> node --version`, 2026-11-01 |

## 2. Hub stack IGC

**Context.** Max operates the hub alone, and agents write the code. The hub runs on one small Fly machine with SQLite and blobs on a volume. It must speak four transports: signed webhook push with retries, HTTP send, SSE, and an MCP Streamable HTTP server with MCP Events for dots. MCP Events must be hand-written whatever the stack (F3). w2m is Max's proven TypeScript implementation of most of these pieces (F8). pag-chat is the first external consumer and plans TypeScript (F9).

| Idea | All | G1 | G2 | G3 | G4 | G5 | G6 | G7 | G8 |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| A. Node 26 + TS + Hono + `node:sqlite` + MCP TS SDK v2 | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ |
| B. Node + TS + Fastify + `better-sqlite3` + MCP TS SDK | ✘ | ✔ | ✔ | ✔ | ✔ | ✔ | ✘ | ✔ | ✔ |
| B′. Node + TS + Fastify + `node:sqlite` + MCP TS SDK | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ |
| C. Bun + TS + Hono + `bun:sqlite` or `node:sqlite` | ✘ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✘ |
| D. Rust: axum + sqlx + rmcp | ✘ | ✔ | ✔ | ✔ | ✔ | ✘ | ✘ | ✔ | ✔ |
| E. Python: FastAPI + `mcp` SDK | ✘ | ✔ | ✔ | ✔ | ✔ | ✘ | ✘ | ✔ | ✔ |
| F. Go: net/http + go-sdk + SQLite driver | ✘ | ✔ | ✔ | ✔ | ✔ | ✘ | ✘ | ✔ | ✔ |

- **G1** An official Tier 1 MCP SDK serves Streamable HTTP at spec `2026-07-28`.
- **G2** SSE and other long-lived connections need no per-connection thread and no workaround that silently drops quiet streams.
- **G3** SQLite with WAL and ordered migrations is available in-stack.
- **G4** It runs as one process in one Docker image on a 512 MB machine via `fly deploy`.
- **G5** pag-chat, the CLI and the later web UI can import the protocol types and client code directly, without code generation or a second language.
- **G6** w2m's production code is reused, not re-implemented: MCP Events, Standard Webhooks signing, delivery retries and the `node:sqlite` migration runner. A thin glue adapter is allowed; rewriting the storage layer or the protocol logic is not.
- **G7** The deployable artefact never contains code compiled or linked on xsm, by construction rather than by discipline.
- **G8** The code runs on the runtime it was proven on (Node), with no documented runtime-compat caveats on the hot paths: SSE and SQLite.

**Why each ✘.**

- **B on G6.** `better-sqlite3` replaces w2m's `node:sqlite` storage layer, so the migration runner and store must be rewritten. It also adds a native addon that falls back to a local compile (F7c). The Fastify choice itself is not the error, as row B′ shows.
- **C on G8.** Bun closes quiet SSE streams after 10 s by default, and its `node:sqlite` has documented differences such as a blocking `backup()` (F7). Each can be worked around, but the goal is "proven runtime, no caveats on the hot paths", and Bun fails it on both.
- **D, E and F on G5.** pag-chat, the CLI and the web UI are TypeScript (F9). Sharing would need schema code generation or a second language in the repo.
- **D, E and F on G6.** Events, signing and delivery would need porting from w2m's TypeScript into Rust, Python or Go. No SDK supplies Events in those languages either (F3), so this is a full re-implementation of the most delicate code.

**Contested ✔s.**

- **G7 passes for every row**, because the artefact is a Docker image. Any compile happens inside the build container against the image's own toolchain and CRT, not xsm's `/usr/lib/Scrt1.o`, and `fly deploy --remote-only` keeps even that off xsm. Rust's local `cargo build` on xsm is fine for development; the hazard (F10) applies only if a host-built binary is copied to another machine. The constraint is real but not decisive here.
- **A on G8.** `node:sqlite` is still a release candidate (F5). That is a known risk, not a decisive error: w2m runs it in production, and the hub keeps it behind one small storage module so `better-sqlite3` can be swapped in if Node ever regresses.
- **C on G1–G4.** These pass. The MCP TS SDK installs with `bun add` (F2), and Bun can run `node:sqlite` (F7).

**Reading the survivors.** A and B′ both survive, and they differ only in the web framework. On the goals above they are equivalent, so either is acceptable. Pick **Hono (A)**, because it is fetch-native like w2m's handlers and like the SDK's web-standard transport. w2m's `handle(request)` code therefore mounts without the request/response adapter that Fastify needs. This is a real but small preference, not a decisive goal. Revisit B′ only if Hono blocks something concrete.

## 3. Component choices

| Concern | Choice | Notes |
|---|---|---|
| Runtime | **Node 26 LTS**, ESM, TypeScript 7 | Supported to 2029-04 (F4). Matches w2m's toolchain (F8). Run the TS output built by esbuild; do not use `--experimental-strip-types` in production. |
| Web framework | **Hono** + `@hono/node-server` | `streamSSE` for subscriptions (F7b); the `@modelcontextprotocol/hono` helper for MCP (F2). |
| SQLite | **`node:sqlite`** (`Database` class), WAL, `synchronous=FULL`, `busy_timeout`, `foreign_keys=ON`, STRICT tables | Port w2m's checksummed `NNNN_name.sql` migration runner. Write SQL directly in a typed repository module; use no ORM. Keep all `node:sqlite` imports in one module. |
| Validation and types | **zod 4** schemas in a shared `@vecnet/protocol` package | Export JSON Schema for non-TS peers. pag-chat imports the package directly (G5). |
| MCP | **`@modelcontextprotocol/server` v2** for discovery, tools and Streamable HTTP; **hand-rolled Events** (`events/list`, `events/subscribe`, verification challenge, signed delivery, cursors) lifted from w2m `src/worker.ts` | Track SEP-3415. If an SDK later ships Events, swap the hand-rolled layer behind the same interface. Ticket 06 decides the endpoint shape. |
| Webhook push | Standard Webhooks HMAC, lifted from w2m `src/crypto.ts` and `communication-delivery.ts` | Use the `standardwebhooks` npm package in tests as the independent verifier, as w2m does. |
| SSE | Hono `streamSSE`, event ids from a monotonic SQLite event-log sequence, `Last-Event-ID` resume, a heartbeat comment about every 15 s | Fly must keep the machine always-on (ticket 05). |
| Blob store | Files under `/data/blobs/sha256/<aa>/<bb>/<hex>`. Write to `/data/blobs/tmp/`, `fsync`, then `rename` (atomic, same filesystem). Metadata rows (hash, size, media type, created, retention) live in SQLite. | Identical bytes dedupe for free. Ticket 13 owns upload policy, TTL and read tokens. The nightly off-host backup is SQLite `.backup` plus a blob-directory sync (ticket 05). |
| Tests | vitest, plus a smoke test that boots the real server on a random port | As in w2m. Builds and tests run through `build2` on xsm. |
| Repo shape | npm workspaces: `packages/protocol`, `apps/hub`, `apps/cli`, `apps/web` (later) | Add a `justfile` per Max's repo norms. |
| Dev port | Hub dev server on **45752** | Generated once with Python `secrets.randbelow` in 3000–63000 and confirmed free on xsm. The web UI's Vite dev server gets its own generated port when ticket 18 creates it. |
| Deploy | One Dockerfile `FROM node:26-slim`, `fly deploy --remote-only` | Satisfies G7 by construction: no native code, and the image is built off xsm. |

## 4. CLI distribution IGC

**Context.** The `vecnet` CLI runs inside codex, claude and pi sessions on xsm and the build boxes. It needs HTTP, SSE and a small config file, but no SQLite. It should share `@vecnet/protocol` with the hub.

| Idea | All | C1 | C2 | C3 | C4 |
|---|:-:|:-:|:-:|:-:|:-:|
| npm package (`bin: vecnet`), one esbuild-bundled ESM file, Node ≥ 22 | ✔ | ✔ | ✔ | ✔ | ✔ |
| Bun `--compile` single binary | ✘ | ✔ | ✘ | ✔ | ✔ |
| Node SEA single binary | ✘ | ✔ | ✔ | ✔ | ✘ |

- **C1** One command installs it on every session host.
- **C2** The repo needs no second toolchain or runtime.
- **C3** It imports `@vecnet/protocol` and the hub client code directly.
- **C4** The packaging step uses a stable, documented feature.

**Why each ✘.**

- **Bun on C2.** It adds Bun as a build toolchain beside Node.
- **SEA on C4.** SEA is Stability 1.1, active development (F6).

**Assumptions and fallback.**

- **Node is on the session hosts.** Node 22+ is on xsm, x-tuf and x-tall (F12), so the npm package passes C1. x-alpha is unverified.
- **No public registry is needed.** Install with `npm i -g <tarball-or-git-url>`; the hub could serve its own tarball. Publishing to public npm is Max's call.
- **Fallback for a Node-less host.** A Bun-compiled binary is safe there because it is not linked on xsm and dispatches CPU paths at runtime (F7). It costs C2, so build it only when such a host appears.

## 5. Risks and open points

- **`node:sqlite` is a release candidate.** The storage module boundary is the mitigation. Pin Node 26.x in the Dockerfile and `engines`.
- **MCP Events may change.** SEP-3415 is a draft, and OpenAI's profile differs from it, for example in error codes. Follow OpenAI's published profile as w2m does (see `w2m-envelope.md` and ticket 06).
- **w2m licence and ownership.** The reuse assumes Max can copy code from his private `xpp-tm/xpp-server-gpt-webhook2mcp` repo into vecnet. That repo has no `license` field in `package.json`.
- **Node 26 versus w2m's Node 24.** The `DatabaseSync` alias still works (F5). Port to `Database` while lifting the code.
