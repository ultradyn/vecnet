# Public Demo Instance Implementation Plan

> **For agentic workers:** execute with `docs/plans/vecnet-core-build.workflow.mjs` and `docs/plans/demo-instance.workflow-args.json`. Each task sees its own section and Global Constraints. Follow the dependency map, checkbox steps and terminating `---`. This document is a plan; it does not authorise deployment, capture, credentials access or other live changes during the planning assignment.

**Goal:** The public site's “Operator sign-in” link opens `https://demo.vecnet.ai/ui/`, where anyone can explore a clearly disclosed replay of an actual vecnet session without signing up. First launch is snapshot replay only. Live visitor chat and progressive playback are follow-up designs in the appendices, excluded from this build.

**Architecture:** A separate Fly app, `vecnet-demo`, runs a new `cmd/vecnet-demo` binary with a default-deny HTTP mux. A reviewed fixture becomes immutable, allowlisted display DTOs in memory, served through `/v1/demo/*` with the existing UI read contracts. No replay hub/store reconstruction, SQLite, hub identity, credentials, event cursors or persistent data volume. An atomic snapshot replacement every six hours applies one timestamp offset. Reuse existing screen components through the demo API adapter; no operator bearer. No outbound transport, provider client or chat routes; `/v1/demo/info` always reports `live_enabled=false`.

**Tech stack:** Existing Go HTTP/DTO conventions, vanilla web modules, headless Playwright through `scripts/ui/lib.mjs`. Reuse UI API shapes from `internal/opapi`/`internal/opview`, without invoking database-backed readers or production workers. No frontend dependency installation or change to production authentication semantics.

**Spec:** Max's 2026-11-05 request in `.scratch/vecnet/integrations/plan-demo.assignment.md`; README, `docs/DEPLOY.md`, `docs/OPERATOR.md`; PROTOCOL §§2.7, 2.8, 2.12–2.13, 8.1, 12, 14, 20. **Starts from:** master `c36f621` or later, after this plan is merged. Implementation branch `demo-instance`, distinct from planning branch `plan/demo-instance`.

**Observed seams and preflight:**
- `internal/opseed/seed.go` is a deliberately hostile synthetic UI fixture. `internal/opseed/demo/main.go` binds loopback, prints secrets in a manifest, permits private egress and has unauthenticated `/__demo/*` controls. Preserve it for ui-shots; never ship it or call its Seed in the public runtime. `subs.go` inserts OAuth and callback state that must not enter the public fixture.
- `internal/httpapi/middleware.go` grants `/v1/admin/*` by `Caller.Kind == "operator"`, and valid credentials bypass the anonymous limiter. A shared operator secret is unsafe. The new mux must not delegate unknown paths to `httpapi.New`.
- `internal/ui/static/app.js` stores the operator secret in localStorage, starts a presence heartbeat, and uses a store that acks events; opening a thread marks it read. Demo must explicitly suppress these writes and use its own refresh path.
- `internal/audit/audit.go` refuses keys text/body/secret/token recursively, but is neither a session transcript nor a general content scrubber. There is no session-export command; `internal/httpapi/export_test.go` only exports a test counter.
- `fly.toml` is the real `vecnet` app at `vecnet.xk.io`. Add separate deployment files; do not repoint it.
- No public-site source or “Operator sign-in” markup was found in this checkout. Task 8 records the exact external-site handoff; the actual source location is a question for Max, not permission to search unrelated repositories.
- UNVERIFIED before public launch: actual capture source and publication permission; demo Fly organisation/network isolation, app-name availability, regional quote and DNS access. Task 7 documents checks; no live checks belong to this docs-only assignment.

## Decisions (IGC)

Context: a public internet demo must open without signup, show all **sanitised** operator views, and grant no operator writes. G1 = no signup; G2 = no browser-held operator authority; G3 = can browse all sanitised threads/dashboard.

| Visitor access idea | All | G1 | G2 | G3 |
|---|:---:|:---:|:---:|:---:|
| Publish existing operator secret | ✘ | ✔ | ✘ | ✔ |
| Publish ordinary peer secret | ✘ | ✔ | ✔ | ✘ |
| Anonymous demo mux with projected read views | ✔ | ✔ | ✔ | ✔ |

Operator credentials can create peers, change routes and rotate secrets; ordinary peers cannot read operator views or arbitrary threads. The dedicated mux is the surviving design. Keep real operator sign-in in the self-hosting guide; public CTA label becomes **Explore the demo** (retain the old label only if Max prefers).

Context for simplification 3: all launch data is a bounded, approved, immutable snapshot. G1 = preserve existing UI read contracts/screens; G2 = avoid replay auth/store/event reconstruction; G3 = retain every approved browse view and timestamp relationship.

| Replay implementation | All | G1 | G2 | G3 |
|---|:---:|:---:|:---:|:---:|
| Reconstruct hub DB and invoke opview readers | ✘ | ✔ | ✘ | ✔ |
| **Immutable allowlisted DTO reader in memory** | ✔ | ✔ | ✔ | ✔ |

Adopt simplification 3. `internal/ui/static/api.js` supplies read methods consumed by screen context; `store.js` caches JSON resources, and `dash.js` issues nine independent reads. The components require the response shapes, pagination and relationships, not a hub instance. A snapshot reader can implement those contracts with bounded indexes and generation-bound cursors; no demonstrated launch read requires SQL or hub workers. Reconstructing a hub fails G2 by introducing keys, credentials, domain-table migrations and event state solely to display approved history. Task 3 must test contract completeness across every Task 4 view; missing display fields return to the fixture owner for explicit schema/review, never a raw metadata passthrough or forked UI. This removes the loader's DB lifecycle, persistent replay volume and filesystem reset command.

Other fixed decisions:
1. **Replay first.** Capture a purpose-run local disposable development hub with consenting participants, real model calls and non-sensitive source data. Do not use the screenshot seed as evidence of a real session. Fly capture is an explicitly approved future alternative, using an authenticated read-only API adapter, never a DB backup. The exporter supports a selected local hub first.
2. **No credential on the page.** Public GET projections have no signup and no shared bearer. No visitor sessions or cookies are needed at launch. Future chat permission is scoped separately in the appendix.
3. **No attachments at launch.** Strip attachment references, original URLs, avatars, image markdown and link previews; show text placeholders. Browser links in message bodies are inert text. This removes both server fetch and browser tracking surfaces; a future reviewed static asset fixture is separate work.
4. **No generic egress.** Replay runtime has no outbound HTTP. Future live runtime egress restrictions belong to the appendix. No tools, shell, file access, browsing, real peers, MCP or connectors. Egress networking also isolates the Fly private network from the real app.
5. **Truthful time shift.** Shift activity timestamps with one constant offset per reset generation, retain original capture dates in provenance, and never call shifted data “live”. Launch is snapshot-only: latest activity is generation anchor minus three minutes; provenance stays at its original dates.
6. **Replay-only build.** Always report `live_enabled=false`; chat routes are absent regardless of supplied flags. No model gateway, budget ledger, visitor sessions or progressive scheduler in the launch workflow. Separate follow-up authorisation/workflow is required.

## Global Constraints

- This plan's execution must respect the operator's separately granted scope. This assignment edits only the plan and workflow args, commits them, and writes the result JSON. No attn, live changes, capture or secrets access now.
- Never read `secrets/`, `*.secret`, `.data/`, environment files or `~/.config/vecnet`. Never touch the real dev hub (45752), preview (49575), real Fly app/volume or real peers. Tests use temp HOME/config/cache/data roots and injected clocks; network/provider tests use fake servers and transports.
- All builds, tests, scripts that validate code and screenshots run as `BB_HOST=x-boss BB_JOBS=2 just bb ...`; Go production builds `CGO_ENABLED=0`, `-p 2`, Node `--test-concurrency=2`. Set `BB_JOBS=2` (the wrapper otherwise raises Go parallelism to 8). Race checks alone use `CGO_ENABLED=1 CC=/usr/bin/gcc` on x-boss; production binaries remain pure Go. No local build/test exceptions, including isa-check if it builds. The workflow extraRules override its generic local ISA/build exceptions and unprefixed final-gate examples. Only file inspection, git and formatting run locally. No npm/pnpm install. Chromium headless; no desktop windows.
- Public demo gets a dedicated Fly organisation/private network boundary, app and immutable fixture image. No persistent volume, hub identity or provider key at launch. No shared database, peer credentials, identity seed, route keys, OAuth state or production control-plane credentials. Shared public code and reviewed fixture are allowed. No peering or WireGuard routes to the real organisation.
- Public mux default-denies every unlisted method/path, including all `/v1/admin/*`, normal `/v1/*`, `/mcp`, OAuth discovery, ingress, enrol, blob/avatar paths and `/__demo/*`. GET/HEAD must not mutate persistent state; do not call reachability, presence, ack or read-marker writers. Unknown future routes remain closed.
- Every response uses allowlisted DTOs, not raw SQL rows, JSON metadata passthrough or an unrestricted “GET-only admin proxy”. No secrets, hashes of credentials, source hostnames, callback URLs, original IDs, request headers or raw errors in responses/logs. Audit projections use a separate approved display schema.
- Fixture immutable, schema-versioned, strictly decoded, ≤2 MiB, ≤500 messages, ≤32 peers, ≤32 threads; all references validated. Human signoff must bind its exact SHA-256. Export candidate files are private and outside git; only the approved bundle enters `internal/demofixture/testdata/session-v1.json`. Missing or stale approval refuses load.
- All source IDs, including IDs embedded in text and structured references, map consistently to fresh valid demo IDs. No source-to-demo map is published. All free text (names, titles, bodies, notes, error strings, audit detail, model labels, locations) requires automated scanning and human review. Unknown fields are rejected, not copied.
- Successful anonymous reads are limited too. Separate known embedded assets from dynamic API traffic: known assets 600 requests/min/IP (IPv6 /64), burst 128, 16 concurrent/IP; unknown asset paths charge the dynamic abuse bucket. Dynamic reads provisionally 120/min/IP, burst 64, 4 concurrent/IP; the demo adapter queues at most three concurrent dynamic reads, coalesces duplicate reads and polls generation info at most once per 60 seconds (pause hidden tabs). No activity SSE/streams at launch. Keep 64 requests globally, deadlines, pagination ≤100, read response ≤256 KiB and bounded TTL-evicted limiter maps. Fly-overwritten client IP only on Fly; spoofed headers ignored off-Fly. Task 5 must measure cold-load asset/API request counts against the actual binary and record them; dynamic burst must be ≥2× the measured cold-load API count, and per-minute allowance must cover cold entry + dashboard navigation + reset refetch + polling with ≥2× measured margin. Raise these provisional budgets if needed and retain bounded abuse tests. Asset budget must cover the measured transitive module graph with ≥2× margin (review already found ≥32 requests before transitive imports/API). No unmeasured claim of adequacy.
- Reset snapshot every 6 hours with ≤100 MiB total live replay memory, including retired generations and indexes; fixture caps remain tighter. Build a bounded replacement, atomically publish it, then release old references after bounded request deadlines. No visitor data/session retention machinery at launch. No persistent writes, analytics or durable IP storage; short-lived limiter state stays in memory. No HTTP reset; private in-process scheduler only, with injected reset clock for tests. Restart loads the reviewed immutable fixture and a new generation anchor.
- New files/interfaces named below are proposed; preserve current hub wire and auth behaviour. Merge shared-file edits in the declared order. Keep `connectors/**`, `internal/opseed/**` and the real `fly.toml` unchanged.

## Review Focus

1. Public access never becomes operator authority: `TestDemoRouteDenyMatrix`, `TestDemoReadsDoNotWrite`, `TestDemoDTORejectsSecretFields` (Task 4), normal auth guards remain green. Chat/activity routes absent and live_enabled always false.
2. Capture cannot publish PII/secrets or invent provenance: `TestExportRedactsAllSurfaces`, `TestExportRejectsUnreviewedBundle`, manual review checklist (Tasks 1–2).
3. Snapshot shift preserves causality, links and original provenance without hub reconstruction: `TestReplayConstantOffset`, `TestReplayLinksConsistent`, `TestReplayUIContracts`, `TestReplayNoRuntimeAuthority` (Task 3).
4. No outbound transports, browser remote loads or hidden writes: `TestDemoNoOutboundTransports` (Task 6), demo unit tests and actual-binary browser journey (Task 5).
5. Production traffic limits permit a cold page load, dashboard and reset refetch: `TestDemoColdLoadProductionLimits` plus measured request-count receipt (Task 5); separate asset/API abuse and limiter cardinality tests (Tasks 4, 6).
6. Atomic reset never mixes generations, leaks old cursors or retains unbounded snapshots: `TestResetConcurrentReaders`, `TestResetFailureKeepsSafeSnapshot`, `TestResetMemoryBound` (Task 6), generation change journey (Task 5).
7. Dedicated deployment has no volume, provider secret, outbound client or test controls (Task 7); desktop/mobile disclosure and all-view navigation reviewed (Tasks 5, 8). Provider budgets, spill prevention and chat retention are must-pass follow-up gates in the appendix, not launch prerequisites.

## Dependency map

```json
{"1":[],"2":[1],"3":[1],"4":[1,3],"5":[4,6],"6":[3,4],"7":[2,5,6],"8":[2,5,7]}
```

**Build settings:** prefix `dm`, implementation worktree `.worktrees/demo-instance`, branch `demo-instance`, status `.scratch/vecnet/build-demo-instance`, runner `BB_HOST=x-boss BB_JOBS=2 just bb `. Full range 1–8 is replay-only. Task 6 supplies the runnable runtime required by Task 5's actual-binary/reset journey; numeric order is not scheduling order. Tasks 2 and 3 independently consume Task 1. Appendices are not workflow tasks. Do not capture real data in ordinary agent execution.

**Shared-file order:** `cmd/vecnet-demo/main.go`: 4 → 6; `internal/demoserver/server.go`: 4 → 6; UI adapter/screens: Task 5 only; `justfile`: 6 → 7; `docs/DEMO.md`: 2 → 7 → 8. Fixture contract owned by Task 1; schema changes return to controller. Only human-approved actual-session artifacts satisfy public launch; synthetic bundles remain test-only. Any future chat workflow must depend on merged Task 5 before editing `demo.js`/`demo.test.mjs`, and order server/main edits after Task 6; consistency checks must verify this transitive prerequisite in that future workflow.

---

### Task 1: Versioned fixture and provenance contract

**Depends:** none

**Files:** Create `internal/demofixture/fixture.go`, `fixture_test.go`, `schema.json`, `testdata/synthetic-v1.json` (explicitly synthetic, test-only).

**Interfaces (Produces):** `Bundle{Version, Provenance, Peers, Threads, Messages, Deliveries, DisplayViews}`; `Validate(Bundle) error`; `Approved(Bundle, digest string) error`. JSON uses snake_case. Provenance includes original start/end UTC, consent/review reference, reviewer, approval digest (sidecar/excluded from hashed bytes), endpoint host + approved exact model/provider IDs, redaction summary, and `source_kind: actual_session|synthetic_test`. Only `actual_session` may launch publicly. `DisplayViews` has separately typed allowlisted fields for principals, inert route labels, inbox summaries, dead letters, subscription status, loop cuts, delivery health, counters and audit; agree the full Task 4/UI response shapes here before downstream work. Empty sections must reflect source observations, never invented evidence. No credentials or original hub URL.

- [ ] **Step 1:** Write failing `TestFixtureStrictSchema` (unknown/deep forbidden fields), `TestFixtureReferenceIntegrity` (orphan/cross-thread references), `TestFixtureApprovalBindsBytes`, `TestSyntheticCannotLaunchPublic` and size/count cap tests. Expected RED: missing validator/contract.
- [ ] **Step 2:** Define the allowlisted display schema, stable demo-ID relationships and provenance fields. Validate every timestamp, reference and cap; scanning applies to nested arrays/strings, not only JSON keys. Approval record is a separate sidecar bound to exact bundle bytes to avoid a self-referential hash.
- [ ] **Step 3:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demofixture/`; require GREEN and commit `feat(demo): reviewed fixture contract`.

---

### Task 2: Offline session exporter and publication review

**Depends:** 1

**Files:** Create `cmd/vecnet-demo-export/main.go`, `main_test.go`, `internal/demofixture/export.go`, `export_test.go`, `docs/DEMO.md`.

**Interfaces:** Consumes Task 1 Bundle/Validate. Produces `Export(ctx, source Reader, selection Selection) (Bundle, ReviewReport, error)`. `Reader` exposes paginated read-only peer/thread/message/delivery and explicitly selected display-view reads for the Task 1 contract (principals, inert route/subscription status, health/counters/loop cuts and approved audit). Never import their control configuration or raw metadata. CLI `vecnet-demo-export --hub URL --thread ID --out DIR` takes an operator credential on stdin, never flag/env-file discovery. Output candidate and private remapping report use 0700 directory/0600 files, never stdout content. Human produces approval sidecar after review; exporter cannot self-approve.

- [ ] **Step 1:** Write failing `TestExportUsesReadOnlyPaginatedAPI`, `TestExportRedactsAllSurfaces`, `TestExportNoCredentialInOutputOrLogs`, `TestExportRejectsUnreviewedBundle`. Include nested metadata, sender_info, reaction actors, approval text, emails, IPs, paths, URLs, bearer-like strings, IDs in body text and Unicode obfuscation.
- [ ] **Step 2:** Export selected threads from a disposable **local dev hub explicitly created for capture**, through its operator reads. Source selection and consenting real session are an operator preparation step; this task tests with fakes. No SQLite/volume copying, audit-only transcript or inferred endpoint/model labels. Drop metadata by default, attachments/avatar URLs, OAuth state and route/subscription execution configuration entirely; retain only explicitly allowlisted historical display fields for every Task 4 view; remap identities and references consistently; review/replace sensitive text without pretending replaced text is verbatim. Record edits in disclosure.
- [ ] **Step 3:** Document mandatory human read of **every** surviving string and scanner report, participant permission, provenance verification, second secrets scan on final bytes, approval digest and copying only approved artifacts into `internal/demofixture/testdata/session-v1.json` plus approval sidecar. Scan failure blocks; a clean scan never substitutes for review. Raw candidates/maps stay outside git and are deleted after approval per operator handling policy. A Fly source is deferred, requires explicit operator selection and the same API/review pipeline, never backup reuse.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./cmd/vecnet-demo-export/ ./internal/demofixture/`; require GREEN and commit `feat(demo): sanitised session export and review runbook`. No live capture is required for this code commit; public launch remains blocked until an actual approved fixture exists.

---

### Task 3: Immutable snapshot reader and timestamp shift

**Depends:** 1

**Files:** Create `internal/demoreplay/snapshot.go`, `snapshot_test.go`, `clock.go`, `clock_test.go`.

**Interfaces:** Consumes Bundle/Approved. Produces `Build(bundle, anchor) (Generation, error)` and immutable `GenerationReader` exposing the Task 4 allowlisted read methods with existing UI response contracts, validated filters and bounded pagination. Generation includes ID, anchor, reset_at, unchanged provenance and indexed display DTOs; no hub/store dependency. Cursor tokens are bounded, generation-bound display offsets, never source event cursors.

- [ ] **Step 1:** Failing `TestReplayConstantOffset`, `TestReplayLinksConsistent`, `TestReplayUIContracts`, `TestReplayNoRuntimeAuthority`, `TestReplayRejectsOldGenerationCursor`. Assert every Task 4 view's response shape, filters, pagination, equal-time stable ordering and independent valid demo IDs. No credentials, raw metadata, source cursors or hub identity generated/imported.
- [ ] **Step 2:** Build a deep-owned immutable DTO graph from the approved bundle and bounded indexes for peers, principals, routes, inbox, threads, messages, delivery matrix, dead letters, subscriptions, loop cuts, health, counters and audit. If a required field is missing, return the schema change to the Task 1 owner/controller before proceeding; no empty invented success data for missing required views. Reuse UI contracts and rendering; do not fork screens or call database readers. Health/routes/subscription examples are inert historical display data.
- [ ] **Step 3:** Apply one constant delta so latest approved activity = generation anchor minus three minutes. Shift all operational timestamps including receipts, presence, expiry and audit, preserving relationships; original provenance dates never shift. All snapshot rows are available immediately and labelled “Replay snapshot”; no progressive mode, future-row scheduler, notification regeneration or activity feed. New generations are built for reset; readers cannot mutate returned backing slices/maps.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demoreplay/`; require GREEN and commit `feat(demo): immutable time-shifted snapshot reader`.

---

### Task 4: Anonymous read-only demo server boundary

**Depends:** 1, 3

**Files:** Create `internal/demoserver/server.go`, `server_test.go`, `views.go`, `views_test.go`, `cmd/vecnet-demo/main.go`.

**Interfaces:** Consumes immutable GenerationReader; produces `demoserver.New(Config, GenerationReader) http.Handler`, `GET /v1/demo/info` => `{mode, generation, now, capture, endpoints_models, live_enabled, reset_at, capabilities}`. Capabilities are permanently read-only for this build; mode is always snapshot and live_enabled is always false. New binary has separate config, requires explicit approved fixture/public URL; default listen loopback `127.0.0.1:0`, production listen explicitly configured. Refuse public launch of synthetic data.

- [ ] **Step 1:** Write failing `TestDemoRouteDenyMatrix`, `TestDemoReadsDoNotWrite`, `TestDemoDTORejectsSecretFields`, `TestNormalHubAuthUnchanged` (guard). Test methods, encoded paths/traversal, token queries, fake bearer/operator headers and newly unknown routes. Snapshot digest before/after reads must match; assert no filesystem/store writes and no last_seen/unread/ack writers are constructed. Also deny every chat method/path and activity stream even if a live flag is supplied.
- [ ] **Step 2:** Explicit GET allowlist under `/v1/demo/`: info, peers, principals, routes (display host labels only), inbox, threads, threads/{id}, threads/{id}/messages, threads/{id}/deliveries, dead-letters, subscriptions (display status only), loop-cuts, delivery-health, counters, audit. Bounded query validation mirrors existing views. Read only the immutable generation and project Task 1 allowlisted display schemas using existing UI response shapes. Info supplies generation/reset metadata for polling; no operator context, SSE or event cursor.
- [ ] **Step 3:** Mount only `/healthz`, `/ui/` embedded assets and these reads. GET/HEAD semantics explicitly enumerated (HEAD only health/assets/info); everything else refuses without delegating. Apply CSP/HSTS/timeouts and global resource bounds before dispatch, then separate known-asset and dynamic API budgets from Global Constraints, including successful responses. Unknown assets consume the dynamic budget. Add `TestDemoAssetAndAPIBudgets` to prove the asset burst cannot starve APIs, limits remain bounded under abuse and Retry-After is emitted on 429; Task 5 measures and verifies cold-load sizing. No debug endpoints; request/error logs contain fixed codes and aggregate counts only.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demoserver/ ./internal/httpapi/`; require GREEN and commit `feat(demo): anonymous projected operator views`.

---

### Task 5: Demo UI adapter, disclosure and visual journey

**Depends:** 4, 6

**Files:** Create `internal/ui/static/demo.js`, `internal/ui/jstest/demo.test.mjs`, `scripts/ui/demo.mjs`; modify `internal/ui/static/app.js`, `api.js`, `thread.js`, `presence.js`, `compose.js`, `inbox.js`, `store.js`, `dash.js` only where capabilities/refresh need enforcement; create reviewed `docs/plans/shots/oui/dm-*.png` screenshots and add their index entries in `docs/plans/shots/oui/README.md`. This bounded cross-cutting task may split into sequential adapter/control sub-dispatches; no concurrent writers.

**Interfaces:** Consume `/v1/demo/info`; produce `createDemoApi()` compatible with existing read methods and a capability object passed through screen context. Read methods explicitly map to Task 4 endpoints. All mutation methods reject locally as `demo_read_only`. Chat capabilities are false; chat UI/routes are absent in this build. Demo uses info discovery before reading `vecnet.ui.secret`; real hub info 404 preserves existing login flow.

- [ ] **Step 1:** Failing `demo.test.mjs`: auto-entry without credential, no localStorage secret read/write, no Authorization, no ack/read markers/presence heartbeat/reactions/approvals/retry/discard/invite/upload, bounded info polling and invalidation on generation change, credential rejection flow unchanged outside demo (guard). Inventory every screen that can write and enforce capability both at handler and API layer; disabled controls explain “Read-only replay”.
- [ ] **Step 2:** Render inbox, threads, peer pages, matrix and dashboard with ordinary components and projected data. Poll info at most once per 60 seconds (pause while hidden), invalidate all resources once when generation changes, and refetch; no event pump or activity subscription. Add a demo store refresh seam to `store.js` and disable normal dashboard/sidebar timers in demo to prevent duplicate periodic reads; both files are included in the declared Files. Queue dynamic reads at concurrency three, coalesce duplicates, cancel stale requests and honor Retry-After with at most two bounded retries. Bind read requests to the discovered generation; stale-generation errors trigger one coordinated rediscovery/refetch, never mixed old/new results. Read status is local memory per tab. Block external href navigation and all markdown/image URL loading for demo content; no avatars/thumbnails/attachment downloads. Do not just hide buttons while retaining startup writes.
- [ ] **Step 3:** Persistent banner: **“This is a replay of a real vecnet session recorded {original_date_range}, against {approved_endpoint_hosts/models}. Names and identifiers were changed and sensitive text was redacted. Timestamps are shifted for demonstration; this is not live traffic.”** Show original UTC dates, “Replay snapshot”, generation anchor and reset time in Details. Only show real-session copy for approved actual_session fixtures; synthetic previews say “Synthetic test data”. Never put API keys in page/config/storage.
- [ ] **Step 4:** `TestDemoColdLoadProductionLimits` runs a fresh browser/cache against the actual demo binary with unchanged production quotas (synthetic approved-for-test fixture only), no mocked asset responses or enlarged test quotas. Record actual asset/API counts including transitive imports and simultaneous dashboard reads; enforce the Global Constraints margins in config/tests. Assert every asset/module loads, automatic entry succeeds, all dashboard sections render, and generation reset refetch completes under those same limits. Use an injected reset clock, no public test endpoint. The journey observes all requests: zero external browser requests, no forbidden writes, tabs cannot change shared replay state. Capture 1440×900 and 390×844 replay/inbox/thread/dashboard, offline/reset states; inspect both visual layout and geometry, keyboard focus and disclosure wrapping.
- [ ] **Step 5:** Run `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 internal/ui/jstest/` and `BB_HOST=x-boss BB_JOBS=2 just bb build2 node scripts/ui/demo.mjs` with an isolated SHOTS_DIR, then copy only reviewed dm screenshots into the committed shot directory; require GREEN and commit `feat(ui): no-signup disclosed replay experience`.

---

### Task 6: Isolated runtime, reset lifecycle and no-egress preview

**Depends:** 3, 4

**Files:** Modify `cmd/vecnet-demo/main.go`, `internal/demoserver/server.go`, `justfile`; create `internal/demoruntime/runtime.go`, `runtime_test.go`.

**Interfaces:** Produces `Runtime.Current() GenerationReader`, private in-process `Runtime.Reset(ctx) error`, and `just demo-preview FIXTURE`. The runtime owns bounded atomic snapshot publication, injectable clocks and request-generation pinning. No data directory, persistent reset command or HTTP reset endpoint. UI polls info for generation changes; old cursors fail with a fixed stale-generation code.

- [ ] **Step 1:** Failing `TestResetConcurrentReaders`, `TestResetFailureKeepsSafeSnapshot`, `TestResetMemoryBound`, `TestDemoNoOutboundTransports`, limiter cardinality and deadline tests. File/transport spies prove no replay DB/WAL/blob/temp writes, push, notification, Events, CIMD, provider or avatar clients are constructed. `live_enabled` stays false and chat/activity routes remain absent.
- [ ] **Step 2:** Validate fixture approval on startup and before reset; build a complete replacement before atomic publication every six hours. Failed rebuild keeps the safe previous snapshot or fails health if none exists. Pin one generation per request and reject mismatched client generation/cursors; never combine rows across generations. Bound request deadlines and retired references; at most current + one replacement/retired snapshot within the 100 MiB total memory cap, refusing overlapping resets. Release old indexes/DTO references once readers drain. No database migration, filesystem deletion, session cleanup, events or production tickers.
- [ ] **Step 3:** Runtime constructs no outbound transport, `httpapi.NewStack.Run` or hub workers; allow only local embedded fixture/UI reads. Tests prove no egress under startup, all views, reset, errors or attempted chat configuration. Future provider client belongs only to the separately reviewed follow-up.
- [ ] **Step 4:** Preview recipe runs with explicit fixture (synthetic requires a test-only switch), loopback port 0 and printed credential-free URL on the build box. Smoke: `curl -fsS "$DEMO_PREVIEW_URL/healthz"` then `/v1/demo/info`; interactive human may tunnel the printed port. No secret manifests or windows. Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demoruntime/ ./internal/demoserver/ ./cmd/vecnet-demo/`; require GREEN and commit `feat(demo): atomic snapshot reset and isolated preview`.

---

### Task 7: Separate Fly packaging and deployment runbook

**Depends:** 2, 5, 6

**Files:** Create `Dockerfile.demo`, `fly.demo.toml`, `scripts/demo-deploy-check.mjs`, `scripts/demo-deploy-check.test.mjs`; modify `.dockerignore`, `justfile`, `docs/DEMO.md`.

**Interfaces:** `just demo-deploy-check --offline` validates code/config packaging without needing publication approval; `just demo-deploy-check --public` additionally requires an approved actual fixture and operator infrastructure attestations, failing closed when absent. `just demo-deploy` first requires the strict `--public` check then invokes explicit `fly deploy --config fly.demo.toml --app vecnet-demo --remote-only` only after operator authorisation. The image includes only public runtime, assets and approved fixture/sidecar, never exporter, screenshot demo, raw candidates or credential manifests. Config has no volume mount or writable data root and uses `https://demo.vecnet.ai`, shared-cpu-1x/512 MB, one always-on machine, no autoscaling, 15-second kill timeout and HTTPS health checks.

- [ ] **Step 1:** Failing config tests reject real app/domain references, volume mounts, provider credentials/clients, live enablement, private egress enablement, leaked source data in image context, extra machines and missing caps. `--public` additionally rejects missing isolation attestations and absent/unreviewed/synthetic fixtures. `--offline` tests use isolated synthetic fixture paths to validate packaging rules, report actual-fixture/infrastructure readiness as pending, and never produce a deployable public artifact. Document organisation/network checks as operator preconditions, not claims that an app name alone isolates Fly 6PN.
- [ ] **Step 2:** Add files and app-specific recipes; leave `fly.toml` and ordinary fly-* recipes unchanged. Docker ignore private exports/maps; COPY only explicit reviewed fixture paths (never broad testdata). Runbook creates the app in its dedicated organisation/private network, uses shared IPv4/IPv6, adds demo.vecnet.ai certificate and DNS from Fly's actual output (no copied production addresses). No shared org private networking, service tokens or real secrets.
- [ ] **Step 3:** Runbook order: approve fixture and offline checks → provision isolated app/network → deploy replay-only → verify TLS/domain/read views/denied endpoints/zero outbound. Chat remains absent and no provider key is installed. Emergency stop affects only the demo app; rollback uses an approved immutable image/fixture and rebuilds its in-memory snapshot. DNS and app operations are manual steps until separately authorised; never run them in tests.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 scripts/demo-deploy-check.test.mjs` and `BB_HOST=x-boss BB_JOBS=2 just bb just demo-deploy-check --offline`; require GREEN and commit `build(demo): isolated Fly app and guarded deployment`.

---

### Task 8: Documentation, CTA handoff and full acceptance gate

**Depends:** 2, 5, 7

**Files:** Modify `README.md`, `docs/DEPLOY.md`, `docs/OPERATOR.md`, `docs/DEMO.md`, `docs/PROTOCOL.md`, `CHANGELOG.md`; add `docs/plans/shots/oui/dm-acceptance.md` verification receipt. Public website file is external/unverified; record its exact handoff destination when Max identifies it.

**Interfaces:** Docs specify demo-only `/v1/demo/*` extension, no changes to standard operator/peer auth, public disclosure and self-hosting real sign-in distinction. Receipt lists commit, fixture digest/review reference, snapshot generation/live_enabled=false, negative checks, screenshot paths, captured model provenance (no inference spend) and outstanding manual gates; never credentials or visitor content.

- [ ] **Step 1:** Add a spec-coverage checklist mapping each assignment requirement to tests/tasks. Verify every named launch test exists and carries assertion evidence, all shared interfaces match, and screenshot labels distinguish shifted snapshot replay and synthetic preview. Appendix tests remain future gates.
- [ ] **Step 2:** Document public CTA exact replacement `Explore the demo` → `https://demo.vecnet.ai/ui/`. If site source is not available, save a concrete handoff (existing label “Operator sign-in”, target and disclosure) in docs/DEMO.md and mark actual site edit pending; do not call the whole public launch complete until it is applied and link-tested. Maintain real `/ui/` operator login instructions for self-hosters.
- [ ] **Step 3:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./...`, `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go vet ./...`, `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 internal/ui/jstest/ scripts/demo-deploy-check.test.mjs`, `BB_HOST=x-boss BB_JOBS=2 just bb just validate-protocol`, and headless demo journey with isolated SHOTS_DIR. Use a fresh reviewer for auth/egress/export/snapshot isolation/production traffic limits and visual evidence; fix findings and rerun affected checks. Expected: all pass, normal operator UI unchanged. Small wiring gaps may be fixed; design gaps return to controller with a ruling.
- [ ] **Step 4:** Provide executable local try-it: `BB_HOST=x-boss BB_JOBS=2 just bb just demo-preview <approved-fixture-path>` (printed loopback URL); smoke `/healthz` and `/v1/demo/info`, browse all read views, forbidden admin POST gets refusal, reset refetches and no remote images load. Public smoke **only after separately authorised deployment**: `curl -fsS https://demo.vecnet.ai/healthz`, info/disclosure read, negative admin/control calls, app/network isolation and absent chat/provider checks. No real model/network credentials in acceptance logs. Unavailable approvals/infra/source leave explicit pending gates; never invent deployment evidence.
- [ ] **Step 5:** Commit `docs(demo): public demo guide and acceptance evidence`. Hand over exact preview/deploy commands, reviewed fixture status, website handoff and deferred follow-up status.

---

## Costs and failure policy

Planning estimate in USD for a 30-day month, tax excluded; regional/provider quote remains **unverified** and must be refreshed by the operator before deployment. Retain the prior $5–8/month Sydney compute allowance for one shared-cpu-1x/512 MB machine and assumed 5 GB outbound at $0.04/GB ($0.20). Removing the replay volume removes its prior $0.15/month line: **$5.20–8.20/month estimated**, with **$10/month planning envelope**. No dedicated IPv4, secondary machine, persistent volume or snapshots. These are planning assumptions, not current verified prices or a hard hosting cap; bandwidth abuse can exceed them. Apply response/global concurrency bounds and cost alerts; operator may stop the demo app. Obtain current regional compute/egress and certificate quote before public deployment.

**No model spend in this build: $0.** The replay runtime has no provider client, provider key, chat routes or inference calls. Purpose-run real capture is a separate, one-time operator-authorised preparation expense; a proposed $1 ceiling is not incurred or authorised here. Future live inference budget/account policy is preserved only in the follow-up appendix.

## Pending operator gates and settled launch scope

1. **Public CTA source:** provide repo/path; Task 8 hands off “Explore the demo” → `https://demo.vecnet.ai/ui/` and records the actual edit/link test pending.
2. **Publishable session:** purpose-run disposable local hub, consenting participants and non-sensitive real source data; mandatory human review/digest approval of final bytes. No production backup or private-history reuse.
3. **Settled scope:** snapshot replay only, latest activity anchor minus three minutes; no live visitor chat or progressive playback in this build. These are follow-ups, not unresolved launch options.
4. **Spend/isolation:** estimated $10/month envelope, no runtime model spend or persistent volume; dedicated Fly organisation/private network, current regional quote and DNS checks before separately authorised deployment.

## Amendment review notes

This amendment adopts review simplifications 1–3: eight launch tasks, snapshot-only replay and immutable in-memory display reader using existing UI contracts. Public route/output/export boundaries remain mandatory. Task 5 now depends on Task 6 so its actual-binary cold-load/reset journey can run; Task 7 packages only Tasks 1–6, and Task 8 closes documentation/CTA acceptance. Shared UI files cannot be modified by follow-up chat before Task 5 merges. The original spill/retention finding remains a must-fix live-chat gate below. Amendment validation ran on x-boss via `BB_HOST=x-boss BB_JOBS=2 just bb` against a minimal document mirror: PASS for eight task titles/range, matching acyclic dependencies, dm prefix/host, offline gate, cold-load requirements and follow-up spill/retention/budget scope. `git diff --check` passed. Independent review could not start because the agent service failed to load its thread context; goal-fit and cross-file consistency were self-reviewed. No known blocker/major remained in those passes. No implementation/browser/live checks are claimed by this docs amendment; the named checks are future implementation acceptance requirements.

---

## Follow-up: live visitor chat (not in this build)

This appendix preserves the design; it is excluded from the launch dependency map, range and workflow args. Separate authorisation, a new workflow and fresh security/provider policy review are required. Do not construct its stores, gateway, sessions or routes in the replay-only binary.

**Safety/lifecycle policy:** optional live starts disabled, one agent default, maximum three fixed peers; no paid fallback/top-up, tools, shell, browsing, MCP, connectors or access to real peers. Egress only through the constrained model gateway below. Dedicated demo-only key and durable quota/kill state at `/data/control/quota.db` require separately planned storage/network provisioning; none exists in launch packaging. Invalid/missing budget after initial provisioning disables chat. Reset/restart cannot reset spend, ambiguous/failed-call reservations, credentials or kill state. Keep one machine until shared atomic budgets are redesigned.

**Retention policy:** max 100 sessions and 10 MiB visitor text in memory, 30-minute access TTL, deadline deletion/cancellation by 31 minutes with a one-minute watchdog. Separate in-memory hub per session; no visitor data in replay, DB/WAL, blobs/temp files, snapshots, swap, logs or analytics. Disable host swap/core dumps and any volume snapshots/backups; verify no existing snapshots before visitor writes. This is application deletion, not forensic memory erasure. Capacity disables chat. Provider retention must be disclosed and approved independently. Generation reset invalidates sessions, cancels providers and prevents late publication.

**Integration prerequisites:** launch Tasks 4, 5 and 6 must merge before Follow-up B. Shared order: server/main 4 → 6 → B; `demo.js` and `demo.test.mjs` 5 → B. A future workflow consistency check must assert Task 5 is a transitive prerequisite of B, not just that maps match. Add cleanup lifecycle seams and final-wiring tests in that follow-up; the launch runtime does not include visitor cleanup.

### Follow-up A: Free-model gateway and durable quota kill switch

**Prerequisites:** launch fixture contract; separate authorisation and follow-up workflow.

**Files:** Create `internal/demomodel/client.go`, `client_test.go`, `quota.go`, `quota_test.go`, `policy.go`.

**Interfaces:** Produces `Generate(ctx, Request) (Reply, error)` with fixed model/prompt policy, `Reserve(dayUTC, requestID) (Reservation, error)`, `SetKilled(bool) error`. Errors have fixed reason codes; no response-body logging. Budget and kill state at `/data/control/quota.db`, outside every resettable session resource; immutable config pins model IDs, live flag and dollar ceiling.

- [ ] **Step 1:** Failing `TestQuotaReservationsSurviveReset`, `TestQuotaConcurrentCeiling`, `TestQuotaCrashDoesNotRefundUnknown`, `TestQuotaUnknownUsageTripsKill`, `TestQuotaUTCClockRollback`, `TestFreeModelPriceChangeDisables`, `TestGatewayRejectsRedirectPrivateIPTools`, `TestGatewayTimeoutCountsAttempt`. Use fake catalog/key/chat transports; no real model calls.
- [ ] **Step 2:** Free-only policy: pin 1–3 exact `:free` model IDs after operator verification; no automatic router, fallback, plugins, web search, tools or BYOK. Check all catalog prices (including request/ancillary fees) are zero, refresh at most every 5 minutes, refuse stale/unknown/missing prices. Permit HTTPS `openrouter.ai:443` only: GET `/api/v1/models`, GET `/api/v1/key`, POST `/api/v1/chat/completions`; reject redirects/private DNS results and all other paths. Configuration cannot override origin. Provider key stays server-side and is demo-only, never available to model input.
- [ ] **Step 3:** Atomic durable reservations **before** dispatch, one global in-flight call, at most 40 attempts/UTC day, 6/min global, 1024 output tokens, ≤8192 input tokens using a conservative byte bound when no verified tokenizer exists, 20-second timeout and ≤64 KiB response. Failed/cancelled/retried/ambiguous attempts count; zero automatic retries. Refuse malformed ledger or rollback to an older day; provider usage reconciliation only reduces allowance. Default hard dollar ceiling **$0/day**: any nonzero quoted price rejects before dispatch, any reported charge kills live immediately. No application can guarantee upstream billing accuracy; provider key cap is an independent backstop, replay remains available.
- [ ] **Step 4:** Kill switch checked before admission and before dispatch, cancels in-flight work, persists until deliberate operator re-enable. A generation reset cannot clear it. Document account usage/credit limits as extra controls, never the sole limiter. No paid mode in this plan; changing that requires another explicit policy/review.
- [ ] **Step 5:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=1 CC=/usr/bin/gcc go test -p 2 -race ./internal/demomodel/`; require GREEN and commit `feat(demo): durable free-model budgets and kill switch`.

---

### Follow-up B: Optional isolated visitor chat and tool-free agents

**Prerequisites:** launch Tasks 4, 5 and 6 merged, plus Follow-up A; the UI adapter and its tests must exist before this work starts.

**Files:** Create `internal/demochat/chat.go`, `chat_test.go`; modify `internal/demoserver/server.go`, `cmd/vecnet-demo/main.go`, `internal/ui/static/demo.js`, `internal/ui/jstest/demo.test.mjs`.

**Interfaces:** Add POST `/v1/demo/chat/session`, POST `/v1/demo/chat/messages` `{text, client_message_id}`, GET `/v1/demo/chat/messages?after=`; only available with live enabled and valid model policy. Each session owns an in-memory hub/store for visitor and bot messages, separate from replay DTO memory; discard it on expiry/reset. Session cookie random ≥128-bit opaque, Secure/HttpOnly/SameSite=Strict, 30-minute expiry; session/peer/thread assignment held server-side. No visitor-supplied recipient/model/thread/sender/URL/config fields. Replay DTOs exclude visitor chat entirely. Session state must not reuse the immutable replay reader.

- [ ] **Step 1:** Failing `TestChatSessionIsolation`, `TestChatCannotChooseRecipientOrModel`, `TestChatNeverExecutesToolCall`, `TestChatResetCannotPublishLateReply`, `TestChatLimitsAndIdempotency`, `TestChatRequiresSameOrigin`, plus disclosure/disabled/budget UX cases. Tamper cookies, IDs, Origin, forwarded IP headers, JSON fields and prompt requests to fetch URLs/reveal keys; inspect outbound traffic and hub mutations.
- [ ] **Step 2:** Same-origin JSON requests, exact Origin and content-type validation, no CORS. Max 2 KiB text, no attachments or metadata; per-session and per-IP 2 messages/min, 10/session, global 40 provider attempts/day from Follow-up A; capacity limits precede allocation. Reject likely secrets/PII in visitor input before persistence or provider submission; scanning is best-effort and the UI prohibition remains explicit. HTTP/session abuse has independent global limits. Idempotency keyed by session + client_message_id cannot make duplicate provider calls, survives concurrent requests and returns existing outcome; reset invalidates sessions.
- [ ] **Step 3:** One fixed agent default, maximum three configured. One reply per accepted visitor turn (round-robin if multiple), no bot-to-bot wake loop. Scope each server-owned demo peer to its own session thread; orchestration is trusted application code with only local vecnet send/reply, no generic agent harness. Provider API receives only fixed instructions and bounded current-session messages, never other sessions, control config or credentials. Tool-call responses rejected; output always inert text, secrets/PII scan failure withholds response and closes session. Prompts are untrusted data; containment comes from absent capabilities and IDs enforced by code.
- [ ] **Step 4:** Private conversation only; before sending, disclose provider/model, external processing, “Do not enter personal or confidential information”, 30-minute access expiry, local application deletion within 31 minutes and reset semantics. Do not promise zero provider retention; enable only after provider policy review. On budget/kill/provider errors disable chat with plain reason and keep replay browsing. Generation cancellation prevents late replies reaching a new visitor session.
- [ ] **Must-fix retention finding before live chat:** `hub.New` opens a filesystem blob store even with in-memory SQL, and `hub.Send` spills message text above its default 4096-byte TextCap. Token/HTTP response limits do not enforce this byte ceiling. Before any send/reply call, enforce both visitor text and decoded bot text ≤4096 UTF-8 bytes and configure session `TextCap=4096`. Reject oversized decoded replies with fixed `reply_too_large`, without storing or logging their text. Keep SQLite and its temporary storage genuinely in memory; prohibit file DB/WAL/SHM, blob/temp files, host swap, core dumps and backups of visitor content.
- [ ] **Required final-wiring proof:** `TestChatNeverSpillsToDisk` uses the real session hub/send/reply path with 4096- and 4097-byte fake-provider replies (plus visitor boundaries), observing blob/temp files and DB/WAL writes. An actual-session expiry/reset integration test proves access ends at 30 minutes, cleanup closes the session store and cancels pending provider work by 31 minutes, including late replies. Fake lifecycle tests alone are insufficient. Add runtime cleanup ownership and cancellation barriers before enabling these routes.

- [ ] **Step 5:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=1 CC=/usr/bin/gcc go test -p 2 -race ./internal/demochat/ ./internal/demomodel/ ./internal/demoserver/` and `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 internal/ui/jstest/demo.test.mjs`; require GREEN and commit `feat(demo): opt-in private visitor chat`.

---

**Follow-up UI and enablement:** add a distinct panel labelled “Live demo chat · {model} · messages expire within 30 minutes; local chat data deleted within 31 minutes; sent to {provider}”. Show input/privacy warning before sending; capture disabled/budget-exhausted/kill/offline states and preserve replay navigation. A separately authorised runbook must provision control storage with snapshots disabled, check exact free model IDs/catalog prices, provider retention and demo key cap, prove kill/reset/quota and no-spill/expiry integration, then explicitly enable live. Do not infer live readiness from snapshot acceptance.

**Budget/cost policy preserved:** target $0 inference, $0/day paid ceiling, ≤40 attempts/UTC day (≤1,200/30 days), all failed/cancelled/ambiguous attempts count. Check current provider free-model/account caps and data policy before enablement; no purchase of credits or reliance on provider limits as our cap. Provider budget is an independent backstop to persisted reservations, price checks and kill switch. Unsupported/unknown/stale prices, unknown usage, reported charges, timeouts or exhaustion disable chat and leave replay available. Upstream billing accuracy cannot be guaranteed. Hosting/storage quote for this follow-up is separate from the volume-free launch estimate.

## Follow-up: progressive replay (not in this build)

Preserve the former progressive design for a separately scheduled task: injected replay clock/scheduler, latest scheduled item three minutes behind cycle end, expose only due rows across history, counters, directory activity, audit and matrix. Label “Replay in progress” and “Replay complete”; original dates/models stay disclosed and shifted traffic is never called live. Cancel the old scheduler before replacement and do not copy source event cursors. Require `TestProgressiveReplayNoFutureRows` and cross-view causal/order tests; if an activity feed is later justified, add bounded admission, heartbeat/reconnect/lifetime and UI queue accounting for its open connection. First launch has neither this mode nor SSE; its bounded info poll/refetch remains sufficient.
