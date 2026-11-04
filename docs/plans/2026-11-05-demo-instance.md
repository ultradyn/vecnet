# Public Demo Instance Implementation Plan

> **For agentic workers:** execute with `docs/plans/vecnet-core-build.workflow.mjs` and `docs/plans/demo-instance.workflow-args.json`. Each task sees its own section and Global Constraints. Follow the dependency map, checkbox steps and terminating `---`. This document is a plan; it does not authorise deployment, capture, credentials access or other live changes during the planning assignment.

**Goal:** The public site's “Operator sign-in” link opens `https://demo.vecnet.ai/ui/`, where anyone can explore a clearly disclosed replay of an actual vecnet session without signing up. Optional live demo agents answer bounded visitor messages with no execution tools and no access to the real hub.

**Architecture:** A separate Fly app, `vecnet-demo`, runs a new `cmd/vecnet-demo` binary with a deliberately small HTTP mux. It loads a reviewed, versioned fixture into a fresh private hub store and exposes projected read views through `/v1/demo/*`. It never mounts the normal authenticated hub mux, MCP, OAuth, ingress or screenshot controls. Reuse the web components and `opview` readers behind explicit demo adapters; never publish an operator bearer. Replay is the default and remains useful when live models are unavailable. A later opt-in chat worker uses only local hub send/reply and one fixed OpenRouter client. Daily quotas live outside the resettable session database.

**Tech stack:** Existing Go/sqlite, `internal/opapi`, `internal/opview`, vanilla web modules, headless Playwright through `scripts/ui/lib.mjs`. No frontend dependency installation. Reuse existing envelope validation and HTTP timeout/CSP constants where possible; no change to production authentication semantics.

**Spec:** Max's 2026-11-05 request in `.scratch/vecnet/integrations/plan-demo.assignment.md`; README, `docs/DEPLOY.md`, `docs/OPERATOR.md`; PROTOCOL §§2.7, 2.8, 2.12–2.13, 8.1, 12, 14, 20. **Starts from:** master `c36f621` or later, after this plan is merged. Implementation branch `demo-instance`, distinct from planning branch `plan/demo-instance`.

**Observed seams and preflight:**
- `internal/opseed/seed.go` is a deliberately hostile synthetic UI fixture. `internal/opseed/demo/main.go` binds loopback, prints secrets in a manifest, permits private egress and has unauthenticated `/__demo/*` controls. Preserve it for ui-shots; never ship it or call its Seed in the public runtime. `subs.go` inserts OAuth and callback state that must not enter the public fixture.
- `internal/httpapi/middleware.go` grants `/v1/admin/*` by `Caller.Kind == "operator"`, and valid credentials bypass the anonymous limiter. A shared operator secret is unsafe. The new mux must not delegate unknown paths to `httpapi.New`.
- `internal/ui/static/app.js` stores the operator secret in localStorage, starts a presence heartbeat, and uses a store that acks events; opening a thread marks it read. Demo must explicitly suppress these writes and use its own refresh path.
- `internal/audit/audit.go` refuses keys text/body/secret/token recursively, but is neither a session transcript nor a general content scrubber. There is no session-export command; `internal/httpapi/export_test.go` only exports a test counter.
- `fly.toml` is the real `vecnet` app at `vecnet.xk.io`. Add separate deployment files; do not repoint it.
- No public-site source or “Operator sign-in” markup was found in this checkout. Task 10 records the exact external-site handoff; the actual source location is a question for Max, not permission to search unrelated repositories.
- UNVERIFIED before live enablement: actual capture source and publication permission; chosen free model IDs and provider data policy; demo Fly organisation/network isolation, app-name availability, regional quote and DNS access. Task 9 documents checks; no live checks belong to this docs-only assignment.

## Decisions (IGC)

Context: a public internet demo must open without signup, show all **sanitised** operator views, and grant no operator writes. G1 = no signup; G2 = no browser-held operator authority; G3 = can browse all sanitised threads/dashboard.

| Visitor access idea | All | G1 | G2 | G3 |
|---|:---:|:---:|:---:|:---:|
| Publish existing operator secret | ✘ | ✔ | ✘ | ✔ |
| Publish ordinary peer secret | ✘ | ✔ | ✔ | ✘ |
| Anonymous demo mux with projected read views | ✔ | ✔ | ✔ | ✔ |

Operator credentials can create peers, change routes and rotate secrets; ordinary peers cannot read operator views or arbitrary threads. The dedicated mux is the surviving design. Keep real operator sign-in in the self-hosting guide; public CTA label becomes **Explore the demo** (retain the old label only if Max prefers).

Other fixed decisions:
1. **Replay first.** Capture a purpose-run local disposable development hub with consenting participants, real model calls and non-sensitive source data. Do not use the screenshot seed as evidence of a real session. Fly capture is an explicitly approved future alternative, using an authenticated read-only API adapter, never a DB backup. The exporter supports a selected local hub first.
2. **No credential on the page.** Public GET projections have no signup and no shared bearer. Optional chat uses an opaque, expiring same-origin session cookie and server-owned peer/thread IDs; it conveys only chat permission.
3. **No attachments at launch.** Strip attachment references, original URLs, avatars, image markdown and link previews; show text placeholders. Browser links in message bodies are inert text. This removes both server fetch and browser tracking surfaces; a future reviewed static asset fixture is separate work.
4. **No generic egress.** Replay runtime has no outbound HTTP. Live runtime permits only explicit OpenRouter API host/method/path combinations; no arbitrary callback or model-supplied URL. No tools, shell, file access, browsing, real peers, MCP or connectors. Egress networking also isolates the Fly private network from the real app.
5. **Truthful time shift.** Shift activity timestamps with one constant offset per reset generation, retain original capture dates in provenance, and never call shifted data “live”. Optional progressive replay is labelled replay and is separate from true live-chat status.
6. **Live off by default.** Implement the optional worker but deploy `live_enabled=false`. Start with one agent; maximum three fixed configured peers. Free-only, no paid fallback or auto top-up. Failed safety, budget, provider or fixture checks leave replay available and chat disabled.

## Global Constraints

- This plan's execution must respect the operator's separately granted scope. This assignment edits only the plan and workflow args, commits them, and writes the result JSON. No attn, live changes, capture or secrets access now.
- Never read `secrets/`, `*.secret`, `.data/`, environment files or `~/.config/vecnet`. Never touch the real dev hub (45752), preview (49575), real Fly app/volume or real peers. Tests use temp HOME/config/cache/data roots and injected clocks; network/provider tests use fake servers and transports.
- All builds, tests, scripts that validate code and screenshots run as `BB_HOST=x-boss BB_JOBS=2 just bb ...`; Go production builds `CGO_ENABLED=0`, `-p 2`, Node `--test-concurrency=2`. Set `BB_JOBS=2` (the wrapper otherwise raises Go parallelism to 8). Race checks alone use `CGO_ENABLED=1 CC=/usr/bin/gcc` on x-boss; production binaries remain pure Go. No local build/test exceptions, including isa-check if it builds. The workflow extraRules override its generic local ISA/build exceptions and unprefixed final-gate examples. Only file inspection, git and formatting run locally. No npm/pnpm install. Chromium headless; no desktop windows.
- Public demo gets a dedicated Fly organisation/private network boundary, app, volume, generated hub identity and app-scoped provider key. No shared database, peer credentials, identity seed, route keys, OAuth state or production control-plane credentials. Shared public code and reviewed fixture are allowed. No peering or WireGuard routes to the real organisation.
- Public mux default-denies every unlisted method/path, including all `/v1/admin/*`, normal `/v1/*`, `/mcp`, OAuth discovery, ingress, enrol, blob/avatar paths and `/__demo/*`. GET/HEAD must not mutate persistent state; do not call reachability, presence, ack or read-marker writers. Unknown future routes remain closed.
- Every response uses allowlisted DTOs, not raw SQL rows, JSON metadata passthrough or an unrestricted “GET-only admin proxy”. No secrets, hashes of credentials, source hostnames, callback URLs, original IDs, request headers or raw errors in responses/logs. Audit projections use a separate approved display schema.
- Fixture immutable, schema-versioned, strictly decoded, ≤2 MiB, ≤500 messages, ≤32 peers, ≤32 threads; all references validated. Human signoff must bind its exact SHA-256. Export candidate files are private and outside git; only the approved bundle enters `internal/demofixture/testdata/session-v1.json`. Missing or stale approval refuses load.
- All source IDs, including IDs embedded in text and structured references, map consistently to fresh valid demo IDs. No source-to-demo map is published. All free text (names, titles, bodies, notes, error strings, audit detail, model labels, locations) requires automated scanning and human review. Unknown fields are rejected, not copied.
- Limits apply to anonymous **successful reads** too: 60 requests/min/IP (IPv6 /64), burst 20; 4 concurrent requests/IP, 64 globally; activity streams max 2/IP, 32 globally, 60-second lifetime with bounded heartbeat/reconnect; pagination ≤100, read response ≤256 KiB, slow clients terminated. Use only Fly-overwritten client IP on Fly; spoofed headers off-Fly ignored. Storage and limiter maps bounded, TTL-evicted. Separate global chat and egress caps withstand rotating IPs.
- Reset every 6 hours, chat session TTL 30 minutes, maximum 100 sessions, 10 MiB visitor data in memory and 100 MiB replay generation storage. Visitor chat uses a separate in-memory hub/store: no visitor content in SQLite files, WAL, snapshots, swap-backed temp files, logs or replay projections. Do not enable host swap/core dumps for this process. Access ends at 30 minutes; deadline scheduling removes session data and cancels work by 31 minutes, with a one-minute watchdog fallback. This is application deletion, not a forensic memory-erasure guarantee. Capacity reached disables chat; reset is private scheduler/CLI only. No visitor text in logs or public replay, no analytics or durable IP storage. Short-lived IP limiter state is held only in memory. Disable demo volume snapshots/backups and document provider-side retention separately.
- Budget state and kill-switch survive generation resets/restarts. Invalid/missing quota state after initial provisioning disables live. Reset never resets spend, calls, failed-call reservations or provider credentials. Keep one machine; do not scale without a shared atomic budget redesign.
- New files/interfaces named below are proposed; preserve current hub wire and auth behaviour. Merge shared-file edits in the declared order. Keep `connectors/**`, `internal/opseed/**` and the real `fly.toml` unchanged.

## Review Focus

1. Public access never becomes operator authority: `TestDemoRouteDenyMatrix`, `TestDemoReadsDoNotWrite`, `TestDemoDTORejectsSecretFields` (Task 4), normal auth guard tests remain green.
2. Capture cannot publish PII/secrets or silently invent provenance: `TestExportRedactsAllSurfaces`, `TestExportRejectsUnreviewedBundle`, manual review checklist (Tasks 1–2).
3. Time shift preserves causality and discloses original dates: `TestReplayConstantOffset`, `TestProgressiveReplayNoFutureRows`, `TestReplayLinksConsistent` (Task 3).
4. No SSRF/browser remote loads or hidden UI writes: `TestDemoNoOutboundTransports` (Task 6), `demo.test.mjs` and network interception journey (Task 5).
5. Hard budget under concurrent calls, crash, retries, reset and date rollover: `TestQuotaReservationsSurviveReset`, `TestQuotaConcurrentCeiling`, `TestQuotaUnknownUsageTripsKill`, `TestFreeModelPriceChangeDisables` (Task 7).
6. Visitor isolation and prompt injection containment: `TestChatSessionIsolation`, `TestChatCannotChooseRecipientOrModel`, `TestChatNeverExecutesToolCall` (Task 8).
7. Dedicated deployment cannot target real assets, snapshots cannot retain visitor messages, runtime includes no test controls (Task 9); UI and disclosure reviewed at desktop and mobile (Tasks 5, 10).

## Dependency map

```json
{"1":[],"2":[1],"3":[1],"4":[1,3],"5":[4],"6":[3,4],"7":[1],"8":[4,6,7],"9":[2,5,6,8],"10":[2,5,9]}
```

**Build settings:** prefix `dm`, implementation worktree `.worktrees/demo-instance`, branch `demo-instance`, status `.scratch/vecnet/build-demo-instance`, runner `BB_HOST=x-boss BB_JOBS=2 just bb `. Full range 1–10 implements the optional feature but keeps it off. A replay-only executable preview exists after Task 6; Task 9 packages it. Tasks 2 and 3 can proceed independently against the same Task 1 schema. Do not capture real data as part of ordinary agent execution.

**Shared-file order:** `cmd/vecnet-demo/main.go`: 4 → 6 → 8; `internal/demoserver/server.go`: 4 → 6 → 8; `internal/ui/static/app.js`: Task 5 only; `justfile`: 6 → 9; `docs/DEMO.md`: 2 → 9 → 10. Fixture contract is owned by Task 1; schema changes return to the controller. Task 2 adds the real reviewed fixture only after human approval; synthetic test bundles must be conspicuously labelled synthetic and cannot satisfy the public-launch gate.

---

### Task 1: Versioned fixture and provenance contract

**Depends:** none

**Files:** Create `internal/demofixture/fixture.go`, `fixture_test.go`, `schema.json`, `testdata/synthetic-v1.json` (explicitly synthetic, test-only).

**Interfaces (Produces):** `Bundle{Version, Provenance, Peers, Threads, Messages, Deliveries, DisplayAudit}`; `Validate(Bundle) error`; `Approved(Bundle, digest string) error`. JSON uses snake_case. Provenance includes original start/end UTC, consent/review reference, reviewer, approval digest (sidecar/excluded from hashed bytes), endpoint host + approved exact model/provider IDs, redaction summary, and `source_kind: actual_session|synthetic_test`. Only `actual_session` may launch publicly. No credentials or original hub URL.

- [ ] **Step 1:** Write failing `TestFixtureStrictSchema` (unknown/deep forbidden fields), `TestFixtureReferenceIntegrity` (orphan/cross-thread references), `TestFixtureApprovalBindsBytes`, `TestSyntheticCannotLaunchPublic` and size/count cap tests. Expected RED: missing validator/contract.
- [ ] **Step 2:** Define the allowlisted display schema, stable demo-ID relationships and provenance fields. Validate every timestamp, reference and cap; scanning applies to nested arrays/strings, not only JSON keys. Approval record is a separate sidecar bound to exact bundle bytes to avoid a self-referential hash.
- [ ] **Step 3:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demofixture/`; require GREEN and commit `feat(demo): reviewed fixture contract`.

---

### Task 2: Offline session exporter and publication review

**Depends:** 1

**Files:** Create `cmd/vecnet-demo-export/main.go`, `main_test.go`, `internal/demofixture/export.go`, `export_test.go`, `docs/DEMO.md`.

**Interfaces:** Consumes Task 1 Bundle/Validate. Produces `Export(ctx, source Reader, selection Selection) (Bundle, ReviewReport, error)`. `Reader` exposes paginated peer/thread/message/delivery reads only. CLI `vecnet-demo-export --hub URL --thread ID --out DIR` takes an operator credential on stdin, never flag/env-file discovery. Output candidate and private remapping report use 0700 directory/0600 files, never stdout content. Human produces approval sidecar after review; exporter cannot self-approve.

- [ ] **Step 1:** Write failing `TestExportUsesReadOnlyPaginatedAPI`, `TestExportRedactsAllSurfaces`, `TestExportNoCredentialInOutputOrLogs`, `TestExportRejectsUnreviewedBundle`. Include nested metadata, sender_info, reaction actors, approval text, emails, IPs, paths, URLs, bearer-like strings, IDs in body text and Unicode obfuscation.
- [ ] **Step 2:** Export selected threads from a disposable **local dev hub explicitly created for capture**, through its operator reads. Source selection and consenting real session are an operator preparation step; this task tests with fakes. No SQLite/volume copying, audit-only transcript or inferred endpoint/model labels. Drop metadata by default, attachments/avatar URLs and route/subscription/OAuth data entirely; remap identities and references consistently; review/replace sensitive text without pretending replaced text is verbatim. Record edits in disclosure.
- [ ] **Step 3:** Document mandatory human read of **every** surviving string and scanner report, participant permission, provenance verification, second secrets scan on final bytes, approval digest and copying only approved artifacts into `internal/demofixture/testdata/session-v1.json` plus approval sidecar. Scan failure blocks; a clean scan never substitutes for review. Raw candidates/maps stay outside git and are deleted after approval per operator handling policy. A Fly source is deferred, requires explicit operator selection and the same API/review pipeline, never backup reuse.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./cmd/vecnet-demo-export/ ./internal/demofixture/`; require GREEN and commit `feat(demo): sanitised session export and review runbook`. No live capture is required for this code commit; public launch remains blocked until an actual approved fixture exists.

---

### Task 3: Deterministic fixture loader and replay clock

**Depends:** 1

**Files:** Create `internal/demoreplay/load.go`, `load_test.go`, `clock.go`, `clock_test.go`.

**Interfaces:** Consumes Bundle/Approved. Produces `Load(ctx, freshStore, bundle, anchor, mode) (Generation, error)`, `Generation.Close() error`, `Generation.Snapshot(at)`. Mode `snapshot|progressive`; injected wall clock and scheduler. Snapshot latest activity = anchor minus 3 minutes; constant delta applies to all operational timestamps, including presence, receipts, expiry and audit display, while capture provenance never shifts.

- [ ] **Step 1:** Write failing `TestReplayConstantOffset`, `TestReplayLinksConsistent`, `TestProgressiveReplayNoFutureRows`, `TestReplayRejectsExistingStore`, `TestReplayNoSourceCredentials`. Assert stable causal ordering with equal timestamps and independent valid IDs; no source cursors/events or identity reuse.
- [ ] **Step 2:** Reconstruct only approved domain/display records in one fresh generation; generate new hub key/identity/credentials internally, never import source auth rows. A display-only route/subscription health example is inert DTO data, never a worker configuration. Rebuild delivery/read-view relationships and regenerate replay notifications rather than copying source event cursors. All relationships satisfy current migrations/foreign keys.
- [ ] **Step 3:** Snapshot is launch default. Progressive mode starts the approved sequence with latest scheduled item three minutes behind the cycle end, exposes only due rows, and marks progress “Replay in progress”. Do not put future rows in history, counters, directory activity, audit or matrix before due. Completed mode stays “Replay complete” until next scheduled reset. Cancel old scheduler before replacing a generation.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demoreplay/`; require GREEN and commit `feat(demo): causal time-shifted replay loader`.

---

### Task 4: Anonymous read-only demo server boundary

**Depends:** 1, 3

**Files:** Create `internal/demoserver/server.go`, `server_test.go`, `views.go`, `views_test.go`, `cmd/vecnet-demo/main.go`.

**Interfaces:** Consumes Generation and `opview` reader; produces `demoserver.New(Config, GenerationReader) http.Handler`, `GET /v1/demo/info` => `{mode, generation, now, capture, endpoints_models, live_enabled, reset_at, capabilities}`. Capabilities read-only at this stage. New binary has separate config, requires explicit approved fixture/data directory/public URL; default listen loopback `127.0.0.1:0`, production listen explicitly configured. Refuse public launch of synthetic data.

- [ ] **Step 1:** Write failing `TestDemoRouteDenyMatrix`, `TestDemoReadsDoNotWrite`, `TestDemoDTORejectsSecretFields`, `TestNormalHubAuthUnchanged` (guard). Test methods, encoded paths/traversal, token queries, fake bearer/operator headers and newly unknown routes. Database before/after public reads must match, including last_seen/unread/acked cursors.
- [ ] **Step 2:** Explicit GET allowlist under `/v1/demo/`: info, peers, principals, routes (display host labels only), inbox, threads, threads/{id}, threads/{id}/messages, threads/{id}/deliveries, dead-letters, subscriptions (display status only), loop-cuts, delivery-health, counters, audit, activity. Bounded query validation mirrors existing views. Use internal operator context only for reading the already sanitised local generation; all outputs projected through Task 1 DTO schemas. Activity is an ephemeral sanitised generation-change SSE, never `/v1/events` or a shared operator cursor.
- [ ] **Step 3:** Mount only `/healthz`, `/ui/` embedded assets and these reads. GET/HEAD semantics explicitly enumerated (HEAD only health/assets/info); everything else refuses without delegating. Apply existing CSP/HSTS/timeouts and the Global Constraints quotas to the full mux before dispatch, including successful responses and assets. No debug endpoints; request/error logs contain fixed codes and aggregate counts only.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demoserver/ ./internal/httpapi/`; require GREEN and commit `feat(demo): anonymous projected operator views`.

---

### Task 5: Demo UI adapter, disclosure and visual journey

**Depends:** 4

**Files:** Create `internal/ui/static/demo.js`, `internal/ui/jstest/demo.test.mjs`, `scripts/ui/demo.mjs`; modify `internal/ui/static/app.js`, `api.js`, `thread.js`, `presence.js`, `compose.js`, `inbox.js` only where capabilities need enforcement; create reviewed `docs/plans/shots/oui/dm-*.png` screenshots and add their index entries in `docs/plans/shots/oui/README.md`. This bounded cross-cutting task may split into sequential adapter/control sub-dispatches; no concurrent writers.

**Interfaces:** Consume `/v1/demo/info`; produce `createDemoApi()` compatible with existing read methods and a capability object passed through screen context. Read methods explicitly map to Task 4 endpoints. All mutation methods reject locally as `demo_read_only`. An opt-in chat capability is wired only in Task 8. Demo uses info discovery before reading `vecnet.ui.secret`; real hub info 404 preserves existing login flow.

- [ ] **Step 1:** Failing `demo.test.mjs`: auto-entry without credential, no localStorage secret read/write, no Authorization, no ack/read markers/presence heartbeat/reactions/approvals/retry/discard/invite/upload, bounded reconnect on generation change, credential rejection flow unchanged outside demo (guard). Inventory every screen that can write and enforce capability both at handler and API layer; disabled controls explain “Read-only replay”.
- [ ] **Step 2:** Render inbox, threads, peer pages, matrix and dashboard with ordinary components and projected data. Use activity to invalidate snapshots; no existing durable-event pump in demo. Read status is local memory per tab. Block external href navigation and all markdown/image URL loading for demo content; no avatars/thumbnails/attachment downloads. Do not just hide buttons while retaining startup writes.
- [ ] **Step 3:** Persistent banner: **“This is a replay of a real vecnet session recorded {original_date_range}, against {approved_endpoint_hosts/models}. Names and identifiers were changed and sensitive text was redacted. Timestamps are shifted for demonstration; this is not live traffic.”** Show original UTC dates, replay progress and reset time in Details. Only show real-session copy for approved actual_session fixtures; synthetic previews say “Synthetic test data”. Live panel separately says “Live demo chat · {model} · messages expire within 30 minutes; local chat data deleted within 31 minutes; sent to {provider}”. Never put API keys in page/config/storage.
- [ ] **Step 4:** Headless journey intercepts all requests: zero external browser requests, no forbidden writes, tabs cannot change shared replay state. Capture 1440×900 and 390×844 replay/inbox/thread/dashboard, offline/reset/budget-exhausted states; inspect both visual layout and geometry, keyboard focus and disclosure wrapping.
- [ ] **Step 5:** Run `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 internal/ui/jstest/` and `BB_HOST=x-boss BB_JOBS=2 just bb build2 node scripts/ui/demo.mjs` with an isolated SHOTS_DIR, then copy only reviewed dm screenshots into the committed shot directory; require GREEN and commit `feat(ui): no-signup disclosed replay experience`.

---

### Task 6: Isolated runtime, reset lifecycle and no-egress preview

**Depends:** 3, 4

**Files:** Modify `cmd/vecnet-demo/main.go`, `internal/demoserver/server.go`, `justfile`; create `internal/demoruntime/runtime.go`, `runtime_test.go`.

**Interfaces:** Produces `Runtime.Current() GenerationReader`, `Runtime.Reset(ctx) error`, private `vecnet-demo reset --data-dir DIR` (requires server stopped/exclusive lock), and `just demo-preview FIXTURE`. Runtime owns generation lock/cancellation. No HTTP reset endpoint. UI receives a new generation ID then refetches; prior cursors never apply to new data.

- [ ] **Step 1:** Failing `TestResetQuiescesReadersAndWorkers`, `TestResetRefusesWrongDataDir`, `TestResetFailureKeepsChatClosed`, `TestDemoNoOutboundTransports`, `TestVisitorRetentionDeadline`, limiter cardinality/stream-cap tests. Store and HTTP spies prove replay creates no push, notification, Events, CIMD or avatar outbound clients.
- [ ] **Step 2:** Validate marked demo data root, exclusive process lock and filesystem/symlink containment; never recursively delete an arbitrary flag path. Deadline-driven session cleanup enforces the 30-minute access/31-minute local deletion bounds (one-minute watchdog); `TestVisitorRetentionDeadline` checks removed session/store references, cancelled tasks and zero disk/WAL writes with an injected clock. Six-hour reset builds a new generation, cancels workers/streams, drains references, swaps atomically, closes old DB and removes old DB/WAL/SHM/temp files. On startup remove abandoned generations only inside marked root before serving. Failure refuses chat and new writes, logs a code, and serves only a safe generation or fails health. No `httpapi.NewStack.Run` or production tickers; schedule only needed replay/retention work.
- [ ] **Step 3:** Replay process constructs no outbound transport; optional model client later is the sole explicit exception, with host/IP/path/method enforcement, redirect denial, TLS validation, public-address pinning and proxy-env bypass. Do not treat `AllowPrivateEgress=false` as an allowlist. All workers share cancellation and reset barriers.
- [ ] **Step 4:** Preview recipe runs only with explicit fixture and a temp root on the build box, loopback port 0 and printed credential-free URL. Smoke: `curl -fsS "$DEMO_PREVIEW_URL/healthz"` then `/v1/demo/info`; interactive human may tunnel the printed port. No secret manifests, no windows. Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./internal/demoruntime/ ./internal/demoserver/ ./cmd/vecnet-demo/`; require GREEN and commit `feat(demo): bounded reset lifecycle and isolated preview`.

---

### Task 7: Free-model gateway and durable quota kill switch

**Depends:** 1

**Files:** Create `internal/demomodel/client.go`, `client_test.go`, `quota.go`, `quota_test.go`, `policy.go`.

**Interfaces:** Produces `Generate(ctx, Request) (Reply, error)` with fixed model/prompt policy, `Reserve(dayUTC, requestID) (Reservation, error)`, `SetKilled(bool) error`. Errors have fixed reason codes; no response-body logging. Budget and kill state at `/data/control/quota.db`, outside `/data/generations/`; immutable config pins model IDs, live flag and dollar ceiling.

- [ ] **Step 1:** Failing `TestQuotaReservationsSurviveReset`, `TestQuotaConcurrentCeiling`, `TestQuotaCrashDoesNotRefundUnknown`, `TestQuotaUnknownUsageTripsKill`, `TestQuotaUTCClockRollback`, `TestFreeModelPriceChangeDisables`, `TestGatewayRejectsRedirectPrivateIPTools`, `TestGatewayTimeoutCountsAttempt`. Use fake catalog/key/chat transports; no real model calls.
- [ ] **Step 2:** Free-only policy: pin 1–3 exact `:free` model IDs after operator verification; no automatic router, fallback, plugins, web search, tools or BYOK. Check all catalog prices (including request/ancillary fees) are zero, refresh at most every 5 minutes, refuse stale/unknown/missing prices. Permit HTTPS `openrouter.ai:443` only: GET `/api/v1/models`, GET `/api/v1/key`, POST `/api/v1/chat/completions`; reject redirects/private DNS results and all other paths. Configuration cannot override origin. Provider key stays server-side and is demo-only, never available to model input.
- [ ] **Step 3:** Atomic durable reservations **before** dispatch, one global in-flight call, at most 40 attempts/UTC day, 6/min global, 1024 output tokens, ≤8192 input tokens using a conservative byte bound when no verified tokenizer exists, 20-second timeout and ≤64 KiB response. Failed/cancelled/retried/ambiguous attempts count; zero automatic retries. Refuse malformed ledger or rollback to an older day; provider usage reconciliation only reduces allowance. Default hard dollar ceiling **$0/day**: any nonzero quoted price rejects before dispatch, any reported charge kills live immediately. No application can guarantee upstream billing accuracy; provider key cap is an independent backstop, replay remains available.
- [ ] **Step 4:** Kill switch checked before admission and before dispatch, cancels in-flight work, persists until deliberate operator re-enable. A generation reset cannot clear it. Document account usage/credit limits as extra controls, never the sole limiter. No paid mode in this plan; changing that requires another explicit policy/review.
- [ ] **Step 5:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=1 CC=/usr/bin/gcc go test -p 2 -race ./internal/demomodel/`; require GREEN and commit `feat(demo): durable free-model budgets and kill switch`.

---

### Task 8: Optional isolated visitor chat and tool-free agents

**Depends:** 4, 6, 7

**Files:** Create `internal/demochat/chat.go`, `chat_test.go`; modify `internal/demoserver/server.go`, `cmd/vecnet-demo/main.go`, `internal/ui/static/demo.js`, `internal/ui/jstest/demo.test.mjs`.

**Interfaces:** Add POST `/v1/demo/chat/session`, POST `/v1/demo/chat/messages` `{text, client_message_id}`, GET `/v1/demo/chat/messages?after=`; only available with live enabled and valid model policy. Each session owns an in-memory hub/store for visitor and bot messages, outside the replay store; discard it on expiry/reset. Session cookie random ≥128-bit opaque, Secure/HttpOnly/SameSite=Strict, 30-minute expiry; session/peer/thread assignment held server-side. No visitor-supplied recipient/model/thread/sender/URL/config fields. Replay DTOs exclude visitor chat entirely.

- [ ] **Step 1:** Failing `TestChatSessionIsolation`, `TestChatCannotChooseRecipientOrModel`, `TestChatNeverExecutesToolCall`, `TestChatResetCannotPublishLateReply`, `TestChatLimitsAndIdempotency`, `TestChatRequiresSameOrigin`, plus disclosure/disabled/budget UX cases. Tamper cookies, IDs, Origin, forwarded IP headers, JSON fields and prompt requests to fetch URLs/reveal keys; inspect outbound traffic and hub mutations.
- [ ] **Step 2:** Same-origin JSON requests, exact Origin and content-type validation, no CORS. Max 2 KiB text, no attachments or metadata; per-session and per-IP 2 messages/min, 10/session, global 40 provider attempts/day from Task 7; capacity limits precede allocation. Reject likely secrets/PII in visitor input before persistence or provider submission; scanning is best-effort and the UI prohibition remains explicit. HTTP/session abuse has independent global limits. Idempotency keyed by session + client_message_id cannot make duplicate provider calls, survives concurrent requests and returns existing outcome; reset invalidates sessions.
- [ ] **Step 3:** One fixed agent default, maximum three configured. One reply per accepted visitor turn (round-robin if multiple), no bot-to-bot wake loop. Scope each server-owned demo peer to its own session thread; orchestration is trusted application code with only local vecnet send/reply, no generic agent harness. Provider API receives only fixed instructions and bounded current-session messages, never other sessions, control config or credentials. Tool-call responses rejected; output always inert text, secrets/PII scan failure withholds response and closes session. Prompts are untrusted data; containment comes from absent capabilities and IDs enforced by code.
- [ ] **Step 4:** Private conversation only; before sending, disclose provider/model, external processing, “Do not enter personal or confidential information”, 30-minute access expiry, local application deletion within 31 minutes and reset semantics. Do not promise zero provider retention; enable only after provider policy review. On budget/kill/provider errors disable chat with plain reason and keep replay browsing. Generation cancellation prevents late replies reaching a new visitor session.
- [ ] **Step 5:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=1 CC=/usr/bin/gcc go test -p 2 -race ./internal/demochat/ ./internal/demomodel/ ./internal/demoserver/` and `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 internal/ui/jstest/demo.test.mjs`; require GREEN and commit `feat(demo): opt-in private visitor chat`.

---

### Task 9: Separate Fly packaging and deployment runbook

**Depends:** 2, 5, 6, 8

**Files:** Create `Dockerfile.demo`, `fly.demo.toml`, `scripts/demo-deploy-check.mjs`, `scripts/demo-deploy-check.test.mjs`; modify `.dockerignore`, `justfile`, `docs/DEMO.md`.

**Interfaces:** `just demo-deploy-check --offline` validates code/config packaging without needing publication approval; `just demo-deploy-check --public` additionally requires an approved actual fixture and operator infrastructure attestations, failing closed when absent. `just demo-deploy` first requires the strict `--public` check then invokes explicit `fly deploy --config fly.demo.toml --app vecnet-demo --remote-only` only after operator authorisation. The image includes only public runtime, assets and approved fixture/sidecar, never exporter, screenshot demo, raw candidates or credential manifests. Config uses dedicated `vecnet_demo_data` 1 GiB volume, `/data`, `https://demo.vecnet.ai`, shared-cpu-1x/512 MB, one always-on machine, no autoscaling, 15-second kill timeout and HTTPS health checks.

- [ ] **Step 1:** Failing config tests reject real app/domain/volume references, nonzero dollar policy, private egress enablement, leaked source data in image context, extra machines and missing caps. `--public` additionally rejects missing isolation/snapshot attestations and absent/unreviewed/synthetic fixtures. `--offline` tests use isolated synthetic fixture paths to validate packaging rules, report actual-fixture/infrastructure readiness as pending, and never produce a deployable public artifact. Document organisation/network checks as operator preconditions, not claims that an app name alone isolates Fly 6PN.
- [ ] **Step 2:** Add files and app-specific recipes; leave `fly.toml` and ordinary fly-* recipes unchanged. Docker ignore private exports/maps; COPY only explicit reviewed fixture paths (never broad testdata). Runbook creates the app in its dedicated organisation/private network, provisions volume with snapshots disabled and verifies existing snapshots absent before visitor writes, uses shared IPv4/IPv6, adds demo.vecnet.ai certificate and DNS from Fly's actual output (no copied production addresses). No shared org private networking, service tokens or real secrets.
- [ ] **Step 3:** Runbook order: approve fixture and offline checks → provision isolated app/network/volume → deploy replay-only → verify TLS/domain/read views/denied endpoints/zero outbound → only then optionally install demo-only provider key and pin free IDs after pricing/data-policy checks → test kill/reset/quota → enable chat explicitly. Revoke only demo key/stop demo app for emergency; rollback uses approved immutable image/fixture, preserves quota/kill state and deletes visitor generations. DNS and app operations are manual steps until separately authorised; never run them in tests.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 scripts/demo-deploy-check.test.mjs` and `BB_HOST=x-boss BB_JOBS=2 just bb just demo-deploy-check --offline`; require GREEN and commit `build(demo): isolated Fly app and guarded deployment`.

---

### Task 10: Documentation, CTA handoff and full acceptance gate

**Depends:** 2, 5, 9

**Files:** Modify `README.md`, `docs/DEPLOY.md`, `docs/OPERATOR.md`, `docs/DEMO.md`, `docs/PROTOCOL.md`, `CHANGELOG.md`; add `docs/plans/shots/oui/dm-acceptance.md` verification receipt. Public website file is external/unverified; record its exact handoff destination when Max identifies it.

**Interfaces:** Docs specify demo-only `/v1/demo/*` extension, no changes to standard operator/peer auth, public disclosure and self-hosting real sign-in distinction. Receipt lists commit, fixture digest/review reference, replay/live status, negative checks, screenshot paths, model IDs/prices and outstanding manual gates; never credentials or visitor content.

- [ ] **Step 1:** Add a spec-coverage checklist mapping each assignment requirement to tests/tasks. Verify every named test exists and carries assertion evidence, all shared interfaces match, and screenshot labels distinguish shifted replay, synthetic preview and live chat.
- [ ] **Step 2:** Document public CTA exact replacement `Explore the demo` → `https://demo.vecnet.ai/ui/`. If site source is not available, save a concrete handoff (existing label “Operator sign-in”, target and disclosure) in docs/DEMO.md and mark actual site edit pending; do not call the whole public launch complete until it is applied and link-tested. Maintain real `/ui/` operator login instructions for self-hosters.
- [ ] **Step 3:** Run `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go test -p 2 ./...`, `BB_HOST=x-boss BB_JOBS=2 just bb env CGO_ENABLED=0 go vet ./...`, `BB_HOST=x-boss BB_JOBS=2 just bb build2 node --test --test-concurrency=2 internal/ui/jstest/ scripts/demo-deploy-check.test.mjs`, `BB_HOST=x-boss BB_JOBS=2 just bb just validate-protocol`, and headless demo journey with isolated SHOTS_DIR. Use a fresh reviewer for auth/egress/export/budget/retention and visual evidence; fix findings and rerun affected checks. Expected: all pass, normal operator UI unchanged. Small wiring gaps may be fixed; design gaps return to controller with a ruling.
- [ ] **Step 4:** Provide executable local try-it: `BB_HOST=x-boss BB_JOBS=2 just bb just demo-preview <approved-fixture-path>` (printed loopback URL); smoke `/healthz` and `/v1/demo/info`, browse all read views, forbidden admin POST gets refusal, reset refetches and no remote images load. Public smoke **only after separately authorised deployment**: `curl -fsS https://demo.vecnet.ai/healthz`, info/disclosure read, negative admin/control calls, app/network isolation and budget/kill checks. No real model/network credentials in acceptance logs. Unavailable approvals/infra/source leave explicit pending gates; never invent deployment evidence.
- [ ] **Step 5:** Commit `docs(demo): public demo guide and acceptance evidence`. Hand over exact preview/deploy commands, reviewed fixture status, website handoff and live opt-in status.

---

## Costs and failure policy

Planning basis checked 2026-11-05; all figures USD, 30-day month, tax excluded. [Fly resource pricing](https://docs.fly.io/about/pricing) lists 512 MB shared-cpu-1x at $3.69/month in its lowest-cost US regions, 1 GB volume $0.15/month and APAC internet egress $0.04/GB. Sydney compute differs; **UNVERIFIED regional quote**, reserve $5–8/month compute. With 1 GB storage and assumed 5 GB/month outbound ($0.20), estimate **$5.35–8.35/month**, plan **$10/month**. No dedicated IPv4, secondary machine or snapshots; certificate allowances are organisation-dependent. This is an estimate, not a hosting hard cap: bandwidth abuse can exceed it. Apply response/stream/global concurrency bounds and cost alerts; operator can stop app if spend grows. A Fly billing alert alone is not a spending limit.

Replay model spend $0. Optional live inference targets $0 with strict free-model policy; 40 calls/day → at most 1,200 attempts/30 days. [OpenRouter rate limits](https://openrouter.ai/docs/api_reference/limits) document free-model/account caps and key-budget status. The [OpenRouter pricing page](https://openrouter.ai/pricing) currently lists free models at 20 requests/min and 50/day without the qualifying credit purchase (1,000/day after at least $10 purchased). Do not buy credits or rely on these ceilings for our own cap. Availability is not promised; provider limits may close chat earlier. Provider key cap, persisted reservations, free-price checks and kill switch layer controls. Unsupported free model, unknown usage, missing policy, timeout or cap exhaustion never triggers paid fallback; replay stays open. Capture inference spend is a separate, one-time operator-authorised expense; recommend a $1 maximum for the purpose-run capture and record actuals before publication.

## Questions for Max (recommendations define the current plan)

1. **Where is the public website's CTA source?** Recommend provide its repo/path; use “Explore the demo” → `https://demo.vecnet.ai/ui/`. Until identified, Task 10 delivers a precise handoff and marks actual edit pending.
2. **Which session may be published?** Recommend a purpose-run disposable local hub with consenting agents/participants, approved non-sensitive live source data and one mandatory human review of the final bytes. No production Fly backup or existing private history.
3. **Launch replay-only or live too?** Recommend replay-only launch; implement live capability off, enable one free-model agent after provider/data-policy/budget checks. Three peers are the supported ceiling, not a default fleet.
4. **Spend/isolation envelope?** Recommend $10/month estimated hosting, zero paid inference, 40 attempts/day, no top-up; a separate Fly organisation/private network. Confirm regional quote before deployment. A $1 one-time capture allowance is proposed, not incurred.
5. **Progressive replay?** Recommend snapshot launch with last activity shifted to three minutes before each generation; progressive mode optional and always labelled. Original dates/models remain visible.

## Self-review notes

Planning validation: a schema/task/dependency consistency check ran on x-boss through `just bb` using a minimal mirror containing only these documents and go.mod; PASS for all ten task titles, interfaces, boundaries, dependencies, task range/prefix/host, offline publication gate and race configuration. `git diff --check` passed. Independent read-only review initially found approval-gate, CGO/race and retention-copy conflicts; corrected them and obtained PASS on focused re-review. No implementation, screenshot or live runtime checks were claimed.

Coverage: hosting/isolation Task 9; anonymous access Tasks 4–5; capture/review Tasks 1–2; shift/progressive replay Task 3; limits/reset/egress Task 6; optional free agents/budget/containment Tasks 7–8; copy/CTA/costs Tasks 5/9/10. Named tests pin each Review Focus item. Fixture schema and reader interface have one owner; shared runtime edits are ordered by dependencies. Dedicated binary avoids widening existing auth; UI task is deliberately cross-cutting and permits sequential sub-dispatches. No changes to real hub, secrets, screenshots or live infrastructure during planning. Real capture approval, external CTA source, live model IDs/policy and Fly pricing/network/DNS remain explicit publication/enablement gates. No sudo is needed. The plan ends in runnable disposable preview plus an authorised public launch checklist; creating this plan does not execute those tasks.

--- SUMMARY ---

- **10 tasks**, prefix **dm**, all build/test execution via **BB_HOST=x-boss**.
- Separate `vecnet-demo` Fly app, data, identity, credentials and private network at **demo.vecnet.ai**; preserve real hub configuration.
- Anonymous projected read views, no printed/shared operator key, no hidden UI writes, no attachments/remote assets.
- Real purpose-run session → allowlisted export → scan and human review → digest-approved fixture → fresh store with disclosed shifted timestamps.
- Snapshot replay launches first; optional progressive replay never masquerades as live traffic.
- Optional private visitor chat uses 1–3 tool-free agents, fixed free model IDs, **$0/day paid spend**, **40 attempts/day**, persisted quota/kill state, no paid fallback.
- 30-minute chat access expiry, local application deletion within 31 minutes, six-hour generation resets, bounded public traffic/storage; snapshots disabled; resets preserve budget state.
- Estimated hosting **$5.35–8.35/month** with **$10/month planning envelope**, region quote pending; live model target **$0**.
- Actual capture approval, model/provider verification, isolated infrastructure and external-site link edit remain recorded operator gates. This planning assignment performs none of them.
