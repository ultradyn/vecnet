# Permission-Gated Admin Surface Parity Implementation Plan

> **For agentic workers:** Plan only; this document does not authorize implementation or deployment. Follow the house format in `2026-11-05-hub-notices.md`: workers receive their task, the contracts below and Global Constraints; steps use checkboxes; a `---` ends every task. Workflow script and argument generation are follow-up work.

**Goal:** An authorized user or bot can inspect and perform hub administration through HTTP, CLI and MCP, with web controls where useful. The immediate acceptance journey is: read grok-smoke's actual scopes, add two destinations without losing the old list, review the change, apply it, then read it back. No caller reconstructs policy from conversation history. Credential-bearing operations complete through a human channel without exposing credentials to a model.

**Architecture:** One hub authorization/service boundary owns operation permissions, resource ownership, grant attenuation, optimistic concurrency and audit. HTTP handlers, CLI and MCP call the same services; web controls consume the same HTTP contract. An explicit operation manifest connects registered routes to CLI commands, MCP tools, matching reads and permission tests. Existing principal provisioning, group membership and self-avatar authority remain separate, narrow powers.

**Tech Stack:** Existing Go, SQLite migrations, stdlib, MCP SDK/schema machinery and vanilla web ES modules; no new authorization framework. All builds/tests use `just bb` per `docs/BUILDBOX.md`.

**Spec:** Max's 2026-11-05 instruction quoted in `.scratch/vecnet/integrations/plan-admin-parity.assignment.md`; `docs/PROTOCOL.md` §§2.12, 4.3–4.6, 7–8, 10–12, 14, 16.2, 18.4, 20.1; `docs/OPERATOR.md`.

**Starts from:** assignment base `master` `79dfd2e`, branch `plan-admin-parity`. Inventory is checkout evidence, not a claim about a running hub. Re-inventory after companion branches merge.

**Deliverable of this assignment:** this plan only, committed; result receipt outside Git in the assignment's JSON path. Contributor rules and conformance code below are implementation tasks, not changes made by the planning run.

## Inventory: current behavior and authority

Evidence: `internal/cli/commands.go` (help and actual dispatch), `invite.go`, `connectors.go`, `internal/httpapi/{admin,op,provision,avatar,mcpdiag}.go`, `internal/httpapi/oauth.go` dot-link registration, `internal/hub/{admin,provision,routes,sources}.go`, `internal/opapi/types.go`, `internal/mcpserver/{tools,auth}.go`, `internal/oauth/{server,consent,verify,tokens}.go`, `internal/ui/static/{api,peer,invite}.js`, and `internal/tui/tui.go`'s API interface.

Legend: `Y` implemented; `—` missing; `R` read/display only; `P` partial, with qualification. `O` operator credential; `P-own` principal credential for peers it both created and still owns; `Self` authenticated peer or authorized principal acting as peer; `Member` group participant subject to send scope. CLI names are top-level, although help groups them under “admin”. HTTP paths below are relative to `/v1/admin` unless explicitly absolute. Every method in a combined row is individually in scope.

| Operation | HTTP API today | CLI today | MCP today | Web UI today | TUI today | Who today |
|---|---|---|---|---|---|---|
| Principal create | POST `/principals`, returns secret once | `principal add` | — | — | — | O |
| Principal list/get, limits/default scopes read | GET `/principals` includes limits/defaults; no targeted GET | `principal list` | — | R principal detail | — | O |
| Principal limits replace | PUT `/principals/{id}/limits` | `principal limits` | — | R | — | O |
| Principal default scopes replace | PUT `/principals/{id}/default-scopes` | `principal scope` | — | R | — | O |
| Principal credential rotate | POST `/principals/{id}/rotate` | — | — | — | — | O |
| Operator peer create / invite | POST `/peers`, returns enrol token except dot | `peer add`, `peer invite` | — | Y Add Agent | — | O |
| Re-invite pending peer | POST `/peers/{id}/enrol-token` | `peer invite <peer>` | — | — | — | O |
| Dot/grokbot OAuth linking | POST `/v1/admin/dot-links` | `peer link` | — | Y invite flow | — | O (code redemption has its own OAuth rules) |
| Provision peer without credential | POST `/v1/peers` | — (operator `peer add` is different) | `vecnet_peer_create` | — | — | P-own provisioning policy; allowed kinds/caps |
| Own-created peer disable/enable | PATCH `/v1/peers/{id}` | — | `vecnet_peer_set_disabled` | — | — | P-own, re-enable checks cap |
| Operator peer directory including disabled, scopes, limits | GET `/peers` | P `peers` uses ordinary directory | P `vecnet_peers` ordinary directory | Y dashboard/detail | Y dashboard | O; ordinary directory has narrower visibility |
| Peer get/scopes/limits targeted read | No targeted routes; GET `/peers` embeds all three | — | — | R from embedded directory fields | P directory only | O |
| Peer ownership move | POST `/peers/{id}/move` | `peer move` | — | — | — | O |
| Peer credential rotate | POST `/peers/{id}/rotate` | `peer rotate` | — | — | — | O |
| Peer scope replacement | PUT `/peers/{id}/scopes` | `peer scope` | — | R | — | O |
| Peer limit replacement | PUT `/peers/{id}/limits` | `peer limits` | — | R | — | O |
| Operator peer disable/enable | POST `/peers/{id}/disable`, `/enable` | — | — (own-created tool is narrower) | R disabled status | R | O |
| Arbitrary peer avatar set/clear | PUT/DELETE `/peers/{id}/avatar` | `peer avatar` | — | R avatar | R avatar | O |
| Avatar read, own set/clear | GET `/v1/peers/{id}/avatar`; PUT/DELETE `/v1/peers/me/avatar` | `avatar`, self set/clear | `vecnet_set_avatar`, `vecnet_clear_avatar` (self/as); read via blob/hash | R | R | Read authenticated subject to existing rules; write Self |
| Route create | POST `/routes` | `route add` (grokbot key from 0600 file) | — | R route metadata only | — | O |
| Route list/get | GET `/routes`, host-only metadata; no detail GET | `route list` | — | R peer route section | — | O |
| Route verify / rotate / remove | POST `/routes/{id}/verify`, `/rotate`; DELETE `/routes/{id}` | `route verify/rotate/remove` | — | — | — | O |
| Source create / list | POST/GET `/sources`; create issues secret once | `source add/list` | — | — | — | O |
| Source disable | POST `/sources/{id}/disable` | — | — | — | — | O |
| Team create / members replace / list | POST `/teams`, POST `/teams/{id}/members`, GET `/teams` | P `team add` only | — | — | — | O |
| Group create / add / list | POST `/v1/groups`, POST `/v1/groups/{id}/members`, GET `/v1/groups` | `groups create/add/list` | `vecnet_group_create/add`, `vecnet_groups` | — dedicated management | — dedicated management | Member/Self, send scopes; not general admin |
| Group unarchive | POST `/groups/{id}/unarchive` | — | — | — | — | O |
| Dead-letter list / retry / discard | GET `/dead-letters`; POST `/{dly}/retry`, `/{dly}/discard` | `dead-letters list/retry/discard` | — | Y | P actions via inbox | O |
| Thread title/budget/history/participant-lock patch | PATCH `/threads/{id}` | — | — | R thread detail | R thread detail | O |
| Blob purge | POST `/blobs/{sha}/purge` | — | — | — | — | O |
| Notification routes replace / read | PUT `/notify`; no GET | `notify set` | — | — | — | O |
| Audit page | GET `/audit` | `audit` | — | Y | Y | O |
| Counters | GET `/counters` | — | — | Y | Y | O |
| Subscription list / pause / resume | GET `/subscriptions`; POST `/{id}/pause`, `/{id}/resume` | `subscriptions list/pause/resume` | P `vecnet_subscriptions` is self-only read | P list/resume | P list/resume | O admin; Self MCP status |
| Operator inbox / mark thread read | GET `/inbox`; POST `/threads/{id}/read` | — (ordinary inbox/ack differ) | — | Y | Y | O |
| Operator thread list / detail / delivery matrix | GET `/threads`, `/{id}`, `/{id}/deliveries` | — (ordinary `threads/thread` differ) | — (ordinary tools differ) | Y | Y | O |
| Loop cuts / delivery health | GET `/loop-cuts`, `/delivery-health` | — | — | Y | Y | O |
| Operator activity stream | GET `/activity` | — (`watch` is own event log) | — | Y | Y via tail | O |
| MCP diagnostics snapshot / follow | GET `/v1/diag/mcp` (explicit operator check outside admin prefix) | `diag mcp` / `--follow` | — | — | — | O |
| Local hub bootstrap / host lifecycle | CLI-local initialization; no online admin HTTP | `init-hub`; hub process flags | — | — | — | Host filesystem/process authority, not a bus role |

**Important correction to the trigger:** There is no `GET .../scopes`, but `GET /v1/admin/peers` already returns `PeerRow.Scopes` and `Limits` (`internal/opapi/types.go`), and `peer.js` renders them. Add a targeted authorized read and MCP/CLI access instead of duplicating stored policy. Principal list likewise already exposes defaults and limits. Route list and source list already exist; preserve and expose them, add useful safe details. A write's matching read reports configuration and lifecycle state, never the old secret. Creation, rotation, purge and discard therefore have metadata/status reads, not credential recovery APIs.

**In-flight surfaces, references only:**

| Owner | Integration contract |
|---|---|
| `plan-prefs`: `docs/plans/2026-11-05-hub-preferences.md` | Owns typed registry, GET/PATCH `/v1/admin/prefs`, preview/confirmation, CLI, Settings and read-only TUI. This plan owns MCP `vecnet_prefs_list/get/set` and authorization adaptation after merge. Keep its preview/retention safeguards; granting a capability never bypasses confirmation. Its current operator-only rule must be amended explicitly before delegation. |
| `plan-r2-blobs`: `docs/plans/2026-11-05-r2-blobs.md` | Owns backend, object lifecycle, provider grants, transfer tools and deployment credentials. Register its admin operations/reads in the manifest after merge. This plan covers purge permission and safe metadata only; do not copy or redesign R2 here. Transfer URLs remain governed by that plan, not admin-secret handling. |
| `hub-notices`: `docs/plans/2026-11-05-hub-notices.md` | Owns public identity/notices, post/list/clear, CLI, banners and MCP restart transport. Add permission/parity adapters after merge for online notice administration; offline identity rotation stays host-only, explicitly exempt. Preserve signing and relocation behavior. |

## Decisions and security contracts

1. **Small model: explicit grants, no inferred authority from kind.** `operator` has all online admin permissions. Principal/peer/session/dot/grokbot kind alone grants none. Store grants to either a principal or an individual peer; principal grants apply to native principal credentials only. Peer credentials and OAuth-bound peers use peer grants, not ambient ownership inheritance. Resolve OAuth's bound peer from server-verified state (currently `Caller` may be principal-shaped with a `PeerID`), never from an argument or `as`. Existing P-own provisioning, group and self powers keep their contracts.
2. **Capabilities and boundaries.** Use `admin:peers`, `admin:scopes`, `admin:limits`, `admin:principals`, `admin:routes`, `admin:sources`, `admin:teams`, `admin:groups`, `admin:deliveries`, `admin:threads`, `admin:blobs`, `admin:notify`, `admin:subscriptions`, `admin:observe`, `admin:audit`, `admin:prefs`, `admin:notices`, `admin:credentials`, `admin:grants`. Each grant has `access=read|write` (write includes read), `boundary={kind:own_principal,principal_id:<server-fixed id>}|{kind:hub}`, and `delegable` default false. Own-principal means **the grant subject's** current principal, not a principal supplied by the caller. Store its ID to prevent move-following. Principal mutation, teams, hub preferences/notices/notify, global observe/audit and unarchive begin hub-only; do not invent ownership for hub-global objects. Routes belong to peers/principals; sources map through their target. Deliveries, subscriptions and threads need ownership queries: own-boundary mutation only when every affected peer/participant belongs to that principal. Otherwise deny. Global audit/activity are never offered under own boundary in v1; filtered resource audit is a separate future contract.
3. **Boundaries alone do not prevent scope escalation.** Operator-managed delegation envelopes carry explicit destination peer sets and numerical ceilings for peer/principal limits and permitted kinds. A delegated `admin:scopes` writer may change only peers in its boundary, and may authorize sends only inside that ceiling. Empty allow currently means open: reject open policy under a finite ceiling, and do not confuse it with deny-all. Expand team/group targets at evaluation; fail closed on mixed/out-of-envelope membership. Persist the envelope with delegated policy and enforce its intersection at send time too, so later membership changes cannot widen it. Defaults receive the same constraints; created peers inherit the ceiling. Grant targets cannot include the operator peer or peers whose credentials would confer greater authority. Creation of operator peers and reserved infrastructure kinds, changes to operator credentials, and issuance for any subject with authority above the issuer are operator-only; credential administration may never mint a route around the grant ceiling. Preserve `NeverProvisionable` for principal provisioning. A delegated limits writer cannot raise provisioning or send caps above its envelope. Hub-wide unrestricted editing requires an explicitly unrestricted operator grant. Test denied recipients and changing memberships, not just target IDs.
4. **Delegation is attenuated and revocation is live.** An `admin:grants` write grant is necessary but insufficient to grant/revoke: issuer must also hold each capability with `delegable=true`, at least the requested access and a boundary/envelope superset. Own cannot grant hub, read cannot grant write, and finite ceilings cannot become unrestricted. Record one parent authority grant per child; multiple requested capabilities create separate children. Parent revoke/expiry invalidates descendants immediately through the checked ancestry, including at tools/call and an open stream's next batch. Nonoperators cannot edit their own authority, revoke ancestors, touch unrelated grants or alter operator credentials. Operator alone can create roots/expand envelopes. A peer move invalidates grants and policies tied to its old principal; it never carries delegated authority into another principal. Disabling/rotating a subject preserves the existing credential-revocation behavior, and disables grant use immediately.
5. **Authorize inside transactions, not just middleware.** Existing `hub/admin.go` methods often have no caller and hardcode audit actor `operator`; existing `/v1/admin` middleware rejects everyone else. Do not simply loosen that prefix. Introduce caller-aware service entrypoints taking a verified actor context; authenticate the prefix, deny unclassified admin routes, then authorize operation/target and recheck grant ancestry plus ownership within the mutation transaction. Retain legacy helpers only as private implementations. Audit actual actor, principal, OAuth client identifier where applicable, operation ID, target IDs, grant/parent IDs, result/reason and safe counts. Do not audit raw bodies, destination URLs, pasted malformed identifiers, secrets or tokens. Denials need a committed sanitized audit independent of a rolled-back mutation. Restrict read audit volume with a documented policy; always record sensitive reads and refused admin calls.
6. **OAuth is an extra gate.** Require both effective hub grants and explicit OAuth `admin:<domain>:read|write` scopes; write scope includes read for that domain only. Keep `vecnet` base scope. Add scopes to discovery, client validation, consent and tool security metadata. Existing `AuthorizationScopes` automatically adds Events scopes on consent; never add admin scopes there. A client explicitly requests them, a human explicitly consents, and current subject grants bound the result. Refresh can narrow, never add admin scopes; a new grant requires fresh consent. Revoking a hub capability takes effect despite an unexpired token. Native bearer `Principal.HasScope` returning true is only an OAuth bypass, not a capability grant. Events scopes remain independent. HTTP-native bearer authorization remains supported; no new requirement to accept OAuth on all HTTP routes.
7. **Stable tool lists; enforced forbidden.** Retain the existing static tools/list design. List tools with clear permission and safe-channel descriptions, filter sensitive values from schemas, and check authority on **every call**. A known but unauthorized tool returns tool-level `isError` with `{code:"forbidden"}`; missing OAuth consent uses existing `RequireScope` HTTP 403 / RPC -32003 `insufficient_scope` challenge. This gives a stable client cache and a clear way to request access; discovery is not authorization. Do not expose an allowed tool list as proof a call will succeed. Extend `vecnet_whoami` with safe effective capabilities/boundaries, and `vecnet_guide(topic:"admin")` with the user's grant/consent path.
8. **Safe credential channel: requests, no redeemable links in MCP.** A “one-time link” that itself redeems a secret is a bearer credential and is not safe model output. Credential-bearing create/invite/reinvite/rotate/link operations use a pending action. MCP returns only `{request_id,status:"awaiting_human",revision,review_path:"/ui/admin/requests/<id>",expires_at}`; request ID/path carries no authority and requires a separately authenticated human with both operation permission and `admin:credentials` at least as broad. Merely fetching a path never mints, rotates or consumes anything. Human reviews exact bound arguments, target, issuer and expiry and explicitly confirms in a protected UI POST. Then the server issues credentials to that human response only, or accepts route/source keys through a protected form. MCP polling returns terminal status and safe resource metadata, never the token, link code, key, enrol command or human response. The human channel is a separately authenticated browser session with a server-issued CSRF secret; no caller-supplied `human=true` or MCP argument can assert it. MCP never exposes confirm/redeem as a tool. Final authorization rechecks issuer AND approver; no impersonation via a more powerful approver. Pending action is immutable, expires in 10 minutes, single-use, idempotent by issuer/key and request digest; no credential side effects before approval. A headless client receives `human_action_required` plus the same non-secret request metadata. Existing raw-secret operator endpoints remain trusted human/CLI compatibility surfaces, explicitly marked in the manifest; MCP never calls them and discards nothing after minting.
9. **CLI and web secret hygiene.** New safe CLI credential operations write generated values only to a new 0600 `--output-file` or install directly into an explicitly selected protected profile; no argv/environment secret flags, stdout, `--json`, clipboard or pasteable message block. External route keys remain `--key-file` only, with existing permission checks; do not let MCP pass server-local file paths. Deprecate and make legacy token-printing behavior fail with a migration hint, including `peer invite --copy`, `principal add`, `peer add/rotate/link`, `source add` generated webhook key output from `route add`, and `notify set` (which currently prints URL and whsec). That is an intentional compatibility change with release notes and tests. The protected human UI masks values by default, uses `Cache-Control: no-store`, no URL secrets, no analytics/storage/log captures; host/session-bound CSRF protection, Origin checks, no cross-origin framing, and explicit confirm. Pending IDs can appear in model context because they cannot approve or redeem. Do not use vector messages or event logs to deliver credentials.
10. **Read-modify-write and redaction.** Dedicated config GETs return exact safe configuration, effective/default metadata, `revision` and ETag. PUT/PATCH requires If-Match; missing precondition 428 `precondition_required`, stale revision 412 `revision_conflict`, no silent overwrite. Existing clients retain legacy behavior only during a documented compatibility window; new CLI/MCP require revision from first release. Each scope set supplies both allow and deny arrays; a scoped CLI `--add-allow` convenience fetches, merges locally, conditionally replaces, and on conflict requires refetch/review. Never silently rebase a scope change. Secret-bearing route/notification URLs (userinfo, path tokens, query) are not safe metadata. Return scheme/host plus redacted display and presence flags; edits either preserve opaque existing fields server-side or use the protected human channel. Never return encrypted secret ciphertext as a substitute for a secret.

## Dependency map

```json
{"1":[],"2":[1],"3":[2],"4":[3],"5":[3,4],"6":[3,4,5],"7":[3,4,5,6],"8":[5,6,7],"9":[1,2,3,4,5,6,7,8]}
```

**Build settings:** follow-up workflow prefix `ap`, implementation worktrees under `.worktrees/admin-parity-*`, receipts `.scratch/vecnet/build-admin-parity/`. Serialize shared-file edits: operation/schema definitions 1 → authorization 2 → HTTP reads 3 → secret requests 4 → MCP/CLI 5–7 → UI 8 → docs/gates 9. Companion adapter Task 7 waits for the referenced owning branches to merge; other tasks can proceed first. Do not reserve a migration or protocol section number across branches.

## Global Constraints

- Plan-only run edits only this plan and its external JSON receipt. Implementation workers obey their task scope and do not deploy, change production grants, widen a real peer's scopes or access real config.
- Pick the **next free migration number at build time** after rebasing onto all prerequisite migrations. Additive grants/request/revision tables; all existing databases start with zero delegated grants. Existing operator and principal provisioning behavior must have guard tests.
- Shared Go wire types and schema definitions feed HTTP and MCP. MCP schemas are top-level objects, `additionalProperties:false`, explicit required fields, no top-level `anyOf`/`oneOf`/`allOf`. Use existing ID/value validation order and limits where defined; do not change existing reason codes accidentally.
- New/revised tools have input AND output schemas, read/write/idempotence/destructive annotations, safe examples, permission description, protocol entry and guide text. Destructive writes need explicit intent fields bound to revisions; safe credentials require human confirmation. Tool annotations are hints, never auth.
- Tests use temporary DBs/config/home/cache and synthetic credentials only. Never print credentials, including failure diffs, snapshot output, diagnostics or request traces. No `attn`. Do not touch `.data/`, `.demo-data/`, real agents' homes, live ports 45752/49575 or `connectors/**`.
- All builds, Go/Node tests and screen scripts use `just bb` and bounded lanes (`CGO_ENABLED=0 just bb go test -p 2 ...`, `just bb build2 node --test --test-concurrency=2 ...`). Only Git/text inspection, gofmt and lightweight allowed local checks run locally. Follow current build-box docs if flags change.
- UI uses existing inert rendering/hostile text handling, desktop and phone tests, no focus stealing; screenshots use isolated harness. CLI must retain no SQLite dependency.
- Protocol plan uses a named new subsection under §12, “Delegated administration”, plus route/tool tables in §§2.12/4.3 and consent §4.5; do not claim §21 owned by hub-notices or overwrite other plans' sections.

## Shared tool contracts

Notation for task catalogs: `id` means the named typed ID, not an unbounded arbitrary string; `S[]` unique bounded arrays using existing scope target types; `rev` nonnegative integer represented as HTTP If-Match; `page` = optional `limit` (1..100), opaque `cursor`; `key` = idempotency key under existing 128-character pattern; `intent` = exact action enum (not free text). Fields followed by `?` are optional; all other fields required. Default output for a conditional write is the safe updated resource plus revision; a list returns `{items,next_cursor?}`. Reads never include secrets. Existing list response wrappers remain compatible at HTTP, even if new MCP uses the common wrapper.

Common error set **E**: 401 `unauthenticated` at transport, tool `forbidden` (403), `not_found` (404) after permitted-boundary lookup, `invalid_body` (400), `invalid_value` (422), `precondition_required` (428), `revision_conflict` (412), `idempotency_conflict` (409), resource/rate caps (429 with retry hints), sanitized internal failure. JSON shape errors use existing MCP -32602. Cross-boundary IDs get `not_found` once operation authority is established; missing capability returns `forbidden` before resource lookup. Permission errors never echo raw submitted values. Tools needing OAuth scopes use Contract 6/7. Catalog rows add domain codes; they inherit E.

Permission notation `domain R/W` means `admin:<domain>` read/write plus OAuth domain read/write if OAuth; operator bypasses hub grants, never a human-channel requirement. `C` additionally requires credentials write for final human action. Every tool's guide text must include the quoted journey instruction in its task, its permission/boundary, errors and its matching read.

---

### Task 1: Freeze operation contracts and establish coverage manifest

**Depends:** none

**Files:** create `docs/admin-operations.json`, shared admin types/schema in `internal/envelope/`, `internal/adminops/` (or existing narrow shared package after dependency review); update `docs/PROTOCOL.md` contract stubs only.

**Interfaces (Produces):** operation entries `{id,method,path,permission,boundaries,read_operation,cli,mcp,web,tui,exception?}`; enums for capabilities, boundaries, grant access, request states and domain error codes. Manifest stores operation identity, not a second authorization implementation. Pure descriptors must not import SQLite into the CLI.

- [ ] **Step 1:** Enumerate registrations in admin.go, op.go, avatar.go, OAuth dot-links and explicit checks outside the prefix (diag). Expand all combined inventory rows into individual method/path records. Classify existing self/principal powers separately from admin powers.
- [ ] **Step 2:** Specify revision-bearing read DTOs and redaction views; assert actual current wire fields before adding types. Classify secret-return endpoints as human/CLI-only with a named safe request equivalent, owner and reason, not “MCP unsupported”. Define reads for irreversible operations as status/tombstone/existence reads.
- [ ] **Step 3:** Tests: manifest rejects unknown capabilities, duplicate method/path, write without read, unrestricted secret tool, or exception without reason/safe equivalent. Guard that ordinary tools/routes keep their schemas and authority.
- [ ] **Step 4:** Validate shared schemas/protocol examples through build-box gates. Commit `docs(admin): define operation and parity contracts`.

---

### Task 2: Transactional authorization, grants, policy ceilings and OAuth consent

**Depends:** 1

**Files:** `internal/auth/`, `internal/hub/{admin,provision}.go`, relevant route/source/team/group services, `internal/httpapi/middleware.go`, `internal/oauth/`, `internal/mcpserver/{auth,server}.go`, next-free `internal/store/migrations/NNNN_admin_authority.sql` and tests. Introduce an actor-aware admin facade in `internal/hub/`; no surface may call an unauthenticated mutation helper.

**Interfaces (Produces):** `Authorize(actor, operation, target, tx)` returning checked effective authority; grants with parent/expiry/envelope; safe `GET /v1/admin/permissions/me`; GET `/v1/admin/grants`, GET `/v1/admin/grants/{id}`, POST `/v1/admin/grants`, POST `/v1/admin/grants/{id}/revoke`. Grants list restricted to visible descendants/subject; operator sees roots. Each grant POST handles one capability, explicit access/boundary/envelope/delegable/expiry and `key`; no bulk role expansion. Grant revoke requires revision and `intent:"revoke"`.

- [ ] **Step 1:** Add migration/default-deny behavior, actor propagation and transaction-aware checks. Unknown admin operation stays forbidden. Remove hardcoded operator audit attribution in changed entrypoints; preserve current real operator authority. Define target resolvers for every domain; unsupported own-boundary combinations fail closed.
- [ ] **Step 2:** Implement grant tree attenuation, current-owner lookups, credential power checks and revocation. Keep existing own-created provenance tests; move cannot turn operator-created peers into P-own authority. Enforce delegated scope ceiling on policy assignment, provisioning and sends and on dynamic membership changes/evaluation.
- [ ] **Step 3:** Add admin OAuth scope discovery/consent without automatic issuance, dual capability/scope gates, metadata for each tool. Continue verifying revocation/expiry on every request; stop authority-dependent streams on revoke. Extend safe whoami/guide data.
- [ ] **Step 4:** Tests: operator guards; ungranted principal/session/dot/grokbot forbidden; OAuth principal-shaped caller cannot inherit principal grant; `as` spoof denied; foreign principal IDs, moved peers, mixed-participant thread, source indirect target and subscription owner escapes denied. Attenuation tests cover read→write, own→hub, self grant, child grant after parent revoke, lost `delegable`, expanded numeric/destination/kind envelopes, changing group membership and empty allow. Race revoke/move against mutation; assert no unauthorized write/audit success.
- [ ] **Step 5:** OAuth tests cover no automatic scopes, requested-but-ungranted scope denied, refresh widening rejected, missing consent challenge, expired/revoked token, valid token with revoked grant, and native bearer HasScope bypass still denied without grants. Commit `feat(auth): bounded delegated admin authority`.

---

### Task 3: Missing reads and conditional HTTP mutations

**Depends:** 2

**Files:** `internal/httpapi/{admin,op,admin_subscriptions}.go`, `internal/hub/`, `internal/opapi/`, revision migration chosen after rebase, tests.

**Interfaces (Produces):** targeted GET `/peers/{id}`, `/peers/{id}/scopes`, `/peers/{id}/limits`; `/principals/{id}`, `/principals/{id}/limits`, `/principals/{id}/default-scopes`; `/routes/{id}`, `/sources/{id}`, `/teams/{id}`, `/groups/{id}`, `/dead-letters/{id}`, `/subscriptions/{id}`, `/blobs/{sha}` metadata/existence, GET `/notify`. Paths in this paragraph have `/v1/admin` prefix. Existing `/threads/{id}` is the thread patch read. Scope DTO is `{peer_id,allow,deny,open,revision}`; limits DTO includes effective/default values and revision; status metadata includes enrollment/credential version timestamps, never hashes or credential material. Return redacted route config sufficient for preserve-existing edits; no raw route URLs to MCP.

- [ ] **Step 1:** Reuse directory/principal snapshot queries instead of competing policy stores. Add safe pagination and filters to broad admin lists as needed; own-boundary lists filter in SQL before pagination, totals and cursor creation. Match every write to its read in manifest; retain tombstones/status for removed resources where needed.
- [ ] **Step 2:** Add per-resource revisions including notify, team membership, grant and lifecycle changes. Document compatibility window for old unconditional operator writes; modern clients always conditional. In one transaction check precondition, permission/current owner, validate, mutate, bump revision and append audit. Idempotent replays cannot reapply changed policy.
- [ ] **Step 3:** Tests: scopes round-trip including deny and open semantics; default inheritance; peer limits defaults; principal defaults apply only to later creates; route/source reads already exist and never reveal secrets; disabled/pending status visible to authorized callers. Exercise no-capability and cross-boundary read/write, cursor/totals leaks, missing/stale revision, two writers and delete/rotate stale state. Verify GET has no side effects.
- [ ] **Step 4:** Build-box HTTP/hub/opapi suites and schema gates pass. Commit `feat(admin): inspect configuration and conditionally update it`.

---

### Task 4: Safe human requests for invites, rotation and secret-backed setup

**Depends:** 3 (reuse its read DTOs; no duplicated DTOs)

**Files:** new hub admin-request service, HTTP routes, next-free request-table migration, protected UI request controller/API tests; existing secret-generating endpoints remain isolated compatibility implementations.

**Interfaces (Produces):** POST `/v1/admin/requests` with typed operation payload, expected revision and `key`; GET `/v1/admin/requests/{id}` returns safe status; POST `/{id}/confirm` and `/{id}/cancel` require human channel/auth/CSRF. States `awaiting_human|completed|cancelled|expired|failed`; actor-visible errors exclude credentials. The request creator can cancel its own pending action; only authorized final human can confirm. Request status is the matching read for every request write, plus resulting resource reads from Task 3.

- [ ] **Step 1:** Implement immutable pending intent, digest/replay protection, expiry, final dual-party authority checks and single transaction state transitions. Route URL/key intake may occur only in the protected confirmation form. Unknown arbitrary “operation” strings never dispatch methods.
- [ ] **Step 2:** Invite/reinvite action mints enrol token only on human confirm; credential rotate requires target read/revision/intent; dot link returns code only to human; source/route setup may accept/generate secrets only at confirm. Existing reenrol restrictions and rotation subscription pauses remain. Require specific operation permission plus credentials W; requests never let a caller borrow the approver's broader authority.
- [ ] **Step 3:** Tests: raw secrets absent from MCP results/error content, pending rows, audits, diagnostics, SSE, messages, traces and snapshots. Scan synthetic sentinel values without printing a matching secret. Request ID alone cannot approve/redeem; GET/prefetch cannot mutate; CSRF/origin/expiry/replay/cross-user/path tampering refused. Revoke, move or disable creator before approval refuses. Concurrent confirms produce one issuance. Permission-denied and boundary-escape cases cover every request operation.
- [ ] **Step 4:** Build-box request/rotation/enrol/OAuth regression suites pass. Commit `feat(admin): human credential handoff requests`.

---

### Task 5: First vertical slice — peer scopes, limits, grants, provisioning and CLI safety

**Depends:** 3, 4

**Files:** `internal/mcpserver/{tools,auth,guide}.go`, shared tool schema definitions, `internal/cli/{commands,invite,guide.md}`, opclient as needed; tests/goldens; PROTOCOL §§2.12, 4.3, 4.5, 12, 13.7, 18.4.

**Tool catalog (input → result; permission; domain errors beyond E):**

| Tool | Schema / result | Permission / extra errors |
|---|---|---|
| `vecnet_admin_permissions` | `{}` → own effective capabilities/boundaries/ceilings and consent requirements | Any authenticated caller; self only |
| `vecnet_admin_grants_list` | `{subject_id?,page}` → grants | grants R, constrained visibility |
| `vecnet_admin_grant_get` | `{grant_id}` → grant | grants R |
| `vecnet_admin_grant_create` | `{subject_kind:principal|peer,subject_id,capability,access,boundary,envelope,delegable,expires_at?,key}` → grant | grants W + delegated authority being granted; `delegation_exceeds_authority` 403 |
| `vecnet_admin_grant_revoke` | `{grant_id,rev,intent:revoke}` → revoked grant | grants W + authority over child; `delegation_exceeds_authority` |
| `vecnet_admin_peers_list` / `vecnet_admin_peer_get` | `{page,principal_id?,disabled?}` / `{peer_id}` → safe peers / peer | peers R; distinct from ordinary directory |
| `vecnet_peer_scopes_get` | `{peer_id}` → scope DTO | scopes R |
| `vecnet_peer_scopes_set` | `{peer_id,allow:S[],deny:S[],rev}` → scope DTO | scopes W; `scope_ceiling_exceeded` 403 |
| `vecnet_peer_limits_get` / `vecnet_peer_limits_set` | `{peer_id}` / `{peer_id,sends_per_min:int>=1,threads_per_hour:int>=1,rev}` → limits DTO | limits R/W; `limit_ceiling_exceeded` 403 |
| `vecnet_admin_peer_set_disabled` | `{peer_id,disabled:bool,rev}` → peer | peers W; preserves operator-peer protection |
| `vecnet_admin_peer_move` | `{peer_id,principal_id,rev,intent:move}` → peer | peers W hub boundary; `invalid_value` for protected target |
| `vecnet_peer_invite_request` | `{kind,name?,name_open?:bool,principal_id?,ttl_seconds?:1..604800,key}` → pending status | peers W + C at confirmation; unsupported kind/open-name rules unchanged |
| `vecnet_peer_reinvite_request` | `{peer_id,ttl_seconds?:1..604800,rev,key}` → pending status | peers W + C; preserve existing enrolled-peer rejection |
| `vecnet_peer_rotate_request` / `vecnet_peer_link_request` | `{peer_id,rev,key,intent:rotate|link}` → pending status | peers W + C; kind-specific validation |
| `vecnet_admin_request_get` / `vecnet_admin_request_cancel` | `{request_id}` / `{request_id,rev,intent:cancel}` → safe status | creator plus live operation authority, or authorized human; foreign request `not_found` |
| `vecnet_admin_peer_avatar_set` / `vecnet_admin_peer_avatar_clear` | `{peer_id,data_base64?,url?,rev}` (exactly one media input) / `{peer_id,rev}` → avatar hash/absence | peers W; reuse avatar size/fetch/SSRF errors |

- [ ] **Step 1:** Implement above MCP handlers through shared facade, stable listing, dual auth and sanitized outputs. Add CLI `permissions`, `grant list/get/add/revoke`, `peer get/list --admin`, `peer scope get/set`, `peer limits get/set`, `peer disable/enable`, and conditional move/avatar. Preserve `peer scope <id> --allow/--deny` as documented conditional-set compatibility syntax; no flags must read, never clear. `--clear` must be explicit and ceiling-checked.
- [ ] **Step 2:** Add CLI principal-owned `peer create --kind --name --idempotency-key` and `peer set-disabled --owned` to reach existing POST/PATCH provisioning; do not reroute them through operator enrol. Existing `vecnet_peer_create` / `vecnet_peer_set_disabled` keep names/schemas/provenance and get CLI parity. Implement safe files/profile output and retire token-printing paths in Contract 9, with a migration guide. No real profile access in tests.
- [ ] **Step 3:** Guide text: “Read scopes and keep both lists. Add destinations to the returned allow list, keep deny unchanged, and set with the returned revision. On conflict, read and review again. A pending invite needs an authorized human to open the review page; never paste credentials into chat.” Include permissions, open-policy warning and explicit confirmation path in tool descriptions/CLI guide.
- [ ] **Step 4:** Per-tool schema/success/error tests, no-capability and foreign-target tests, OAuth missing-scope and revoked-grant tests. End-to-end fixture: grok-smoke has allow A and deny D; add B,C through CLI and MCP separately, preserve A,D, reject stale/over-ceiling updates, read back exactly. Principal provisioning remains credential-free. Avatar target escape denied and legacy self tools unchanged. CLI secret fixtures create 0600 files without stdout leakage, reject unsafe files/argv/clipboard and do not overwrite without explicit instruction.
- [ ] **Step 5:** Build-box focused CLI/MCP/hub suites plus protocol/guide goldens pass. Commit `feat(admin): peer policy and grant parity`.

---

### Task 6: Principal, route, source, team and operational parity

**Depends:** 3, 4, 5

**Files:** admin facade/DTOs, MCP tool definitions, CLI commands/opclient, guides, PROTOCOL §§2.12, 3, 4.3, 7, 10–12, 13.7 and 16.2; tests.

**Tool catalog:** Each slash-separated tool below is an individual named tool with its own schema, output schema, permission check, manifest entry and tests. Shared E errors apply; preserve domain-specific hub errors rather than flattening to generic failure.

| Tools | Input schema → safe result | Permission / extra codes |
|---|---|---|
| `vecnet_admin_principals_list`, `vecnet_admin_principal_get` | `{page}` / `{principal_id}` → principal records | principals R, hub |
| `vecnet_principal_limits_get`, `vecnet_principal_limits_set` | `{principal_id}` / `{principal_id,max_peers:int>=0,creates_per_hour:int>=1,kinds:S[],rev}` → limits | principals R/W, hub; `kind_not_allowed`, `limit_ceiling_exceeded` |
| `vecnet_principal_scopes_get`, `vecnet_principal_scopes_set` | `{principal_id}` / `{principal_id,allow:S[],deny:S[],rev}` → defaults | principals R/W + scopes W on set, hub; `scope_ceiling_exceeded` |
| `vecnet_principal_create_request`, `vecnet_principal_rotate_request` | `{name,key}` / `{principal_id,rev,key,intent:rotate}` → pending | principals W + C, hub |
| `vecnet_routes_list`, `vecnet_route_get` | `{owner_id?,page}` / `{route_id}` → redacted metadata/config | routes R |
| `vecnet_route_create_request`, `vecnet_route_rotate_request` | `{owner_id,kind:webhook|grokbot,key}` / `{route_id,rev,key,intent:rotate}` → pending | routes W + C; URL/key human intake only |
| `vecnet_route_verify`, `vecnet_route_remove` | `{route_id,rev}` / `{route_id,rev,intent:remove}` → status/tombstone | routes W; preserve verification, egress and HTTP failure codes |
| `vecnet_sources_list`, `vecnet_source_get` | `{page}` / `{source_id}` → source configuration/status | sources R |
| `vecnet_source_create_request` | `{kind:github,name,target_id,event_allow:S[],priority:wake|wake_idle|hold,key}` → pending | sources W + C; preserve default wake_idle and source target validation |
| `vecnet_source_disable` | `{source_id,rev,intent:disable}` → source | sources W |
| `vecnet_teams_list`, `vecnet_team_get` | `{page}` / `{team_id}` → team/members | teams R, hub |
| `vecnet_team_create`, `vecnet_team_members_set` | `{name,members:S[],key}` / `{team_id,members:S[],rev}` → team | teams W, hub; check scope envelope impact |
| `vecnet_admin_group_get`, `vecnet_group_unarchive` | `{group_id}` / `{group_id,rev,intent:unarchive}` → group | groups R/W, hub; members ordinary authority unchanged |
| `vecnet_dead_letters_list`, `vecnet_dead_letter_get` | `{page}` / `{delivery_id}` → safe delivery status | deliveries R |
| `vecnet_dead_letter_retry`, `vecnet_dead_letter_discard` | `{delivery_id,rev,intent:retry|discard}` → status/tombstone | deliveries W; preserve state-conflict code |
| `vecnet_admin_thread_patch` | `{thread_id,title?:string|null,budget_remaining?:int>=0|null,history_access?:from_join|full,locked_participants?:bool,rev,intent:update}` → thread | threads W; at least one patch field; preserve omitted versus null; `unknown_thread`, `invalid_value` |
| `vecnet_admin_blob_get`, `vecnet_admin_blob_purge` | `{sha256}` / `{sha256,rev,intent:purge}` → metadata/tombstone | blobs R/W; own boundary permitted only with proven exclusive affected ownership; otherwise hub |
| `vecnet_notify_get`, `vecnet_notify_set_request` | `{}` / `{filters:(wake|approvals|dead_letters|all)[],rev,key}` → redacted routes / pending | notify R/W hub + C on set confirmation; ordered filter array (duplicates valid for distinct routes); URLs only human intake, no whsec returned |
| `vecnet_admin_subscriptions_list`, `vecnet_admin_subscription_get` | `{peer_id?,page}` / `{subscription_id}` → health metadata, callback host only | subscriptions R |
| `vecnet_admin_subscription_pause`, `vecnet_admin_subscription_resume` | `{subscription_id,rev}` → updated health | subscriptions W; same operator-pause semantics |
| `vecnet_admin_audit`, `vecnet_admin_counters` | `{page}` / `{}` → audit/counters | audit R / observe R, hub |
| `vecnet_admin_inbox`, `vecnet_admin_thread_read` | `{limit?,before?,unread?}` / `{thread_id,rev}` → inbox/read state | observe R / threads W, hub |
| `vecnet_admin_threads`, `vecnet_admin_thread`, `vecnet_admin_thread_deliveries` | `{participant?,exclude_participant?,page}` / `{thread_id}` / `{thread_id,after?,limit?}` → list/detail/matrix | threads R; own-boundary all-participant check |
| `vecnet_admin_loop_cuts`, `vecnet_admin_delivery_health` | `{hours?:1..168}` / `{}` → summary | observe R, hub |
| `vecnet_admin_activity` | `{after?,limit?:1..100}` → bounded activity batch/next cursor | observe R, hub; no indefinite tools/call stream |
| `vecnet_admin_mcp_diag` | `{peer_id?,method?,after?,limit?:1..1000}` → sanitized diagnostic snapshot | observe R, hub; never headers/bodies/tokens |

- [ ] **Step 1:** Freeze catalog schemas to actual existing wire fields (including ThreadPatch null/omission, source priority and notify filters); add corresponding CLI subcommands for every row. Use `principal get/limits get|set/scope get|set/rotate`, `route get`, `source get/disable`, `team list/get/members`, `groups get/unarchive`, `thread admin-get/patch/read`, `blob info/purge`, `notify get`, `subscriptions get`, `counters`, `admin inbox/threads/deliveries/loop-cuts/health/activity`, existing `diag mcp`. Ordinary message/inbox tools must not become silent admin views. Route/source external secrets remain file-only in CLI.
- [ ] **Step 2:** Explicit safe guide copy: “List/get before changing configuration; preserve existing opaque secrets. Route/source setup and rotation require a human review request. Source disable stops ingress. Retry can redeliver; discard and purge are destructive. Operator observation may include private message content; a peer directory grant does not grant conversation access.” Describe each tool's permission, schema/example, E/domain errors and matching read in protocol and guides.
- [ ] **Step 3:** For every catalog tool, schema round-trip plus successful read/write, missing capability, OAuth missing scope and foreign target test. Cover indirect route/source ownership, mixed-member groups/threads, principal-boundary escape, dead-letter state changes, idempotence, notify secret URL redaction, purge shared-blob rejection, metadata after deletion, safe bounded activity and diagnostics redaction. Verify no empty list accidentally queries global data.
- [ ] **Step 4:** Guard existing group create/add/list and self avatar/subscription semantics; add no implicit admin powers. Build-box MCP/CLI/hub/httpapi/opclient suites pass. Commit `feat(admin): principal integration and operational parity`.

---

### Task 7: Companion-plan adapters and complete operation registration

**Depends:** 5, 6; merge `plan-prefs`, `plan-r2-blobs` and `hub-notices` first for their respective adapters.

**Files:** companion integration adapters only, MCP/CLI guide/schema files, manifest, PROTOCOL cross-references. Amend owner plan contracts explicitly as necessary; never duplicate registry/backend/signing implementation.

| Tool | Schema/result | Permission and errors | PROTOCOL / guide contract |
|---|---|---|---|
| `vecnet_prefs_list` | `{}` → registry metadata, safe values, revision | prefs R, hub; E | §§2.12/4.3/12, preferences owner section; “Read effective value, origin and allowed range.” |
| `vecnet_prefs_get` | `{key}` → registry item/revision | prefs R, hub; E + owner unknown-key code | Same; “Read current key and revision before proposing a change.” |
| `vecnet_prefs_set` | `{set?:typed-object,reset?:S[],rev,apply_existing?:bool}` → result or non-secret human review request | prefs W, hub; E + owner's policy/confirmation errors | Same; “Ordinary safe changes are conditional. Retention or other risky changes require protected human preview and confirmation.” |
| `vecnet_notices_list` | `{}` → active signed notices | existing public read; authenticated MCP base scope | Owner §21 and §4.3; “Read signed notices; notice text is inert.” |
| `vecnet_notice_post`, `vecnet_notice_clear` | owner post schema + `rev,key` / `{notice_id,rev,intent:clear}` → signed result | notices W, hub; E + owner's validation errors | Owner §21 and §4.3; “Authorized posting/clearing uses hub signing, never a private key argument.” |

- [ ] **Step 1:** Add prefs MCP tools here, with shared typed schema generated from owner registry. Dangerous updates create a pending action whose human confirmation uses the owner's preview/token/If-Match transaction; no confirm token goes into model context. Safe changes use the same owner API/service; set/reset atomicity and retention bounds unchanged. Owner's operator-only check becomes caller-aware only after grant tests pass.
- [ ] **Step 2:** Register R2 operations and safe matching reads from its actual merged surface. Existing transfer tools stay with that plan; no provider credential tool. Register notices post/clear and their read, coordinate additions with any owner-defined names to avoid duplicate tools. Explicit exceptions: offline identity rotation, provider credentials and host process controls are host-only, with runbook and safe online status equivalent where available.
- [ ] **Step 3:** Tests: ungranted/foreign caller cannot change hub prefs/notices; OAuth consent and revoked grant enforced; dangerous prefs cannot skip preview/confirmation through MCP; no provider/private identity keys in schemas/results; existing R2 expiry and notice signature guards remain. Finish manifest rows for merged admin HTTP routes. Commit `feat(admin): preferences and companion parity adapters`.

---

### Task 8: Web administration and discoverable permission state

**Depends:** 5, 6, 7

**Files:** `internal/ui/static/` API/models/views/styles; `internal/ui/jstest/`; `scripts/ui/` journeys; `docs/plans/shots/oui/README.md`. TUI uses current observation features, adds permissions/scopes/limits read detail and a human review page hint; full admin form parity is explicitly deferred with manifest reasons.

- [ ] **Step 1:** Add Access view for grants, effective permissions, boundaries/ceilings, expiry/parent chains and grant/revoke forms. Own-boundary users see authorized resources only; avoid assuming the current operator-only dashboard can load global audit/inbox/principals. Gate loading/navigation by safe permissions metadata, while server remains authoritative. A permission-denied response is an actionable state, not an empty successful form.
- [ ] **Step 2:** Peer detail adds conditional scope/limit editing, enable/disable and move; principal detail adds limits/defaults and credential request actions. Routes/sources/teams/groups receive list/detail and supported mutation forms. Operational views gain missing pause, thread patch, purge and notify controls. Reuse companion Settings/notices UI. Read-only users see effective values and a permission explanation; forms show target/boundary and exact proposed diff before destructive changes.
- [ ] **Step 3:** Implement protected request review from Task 4: separate human authentication, no auto-confirm on navigation, final authorization/CSRF checks, masked credential display or protected download only. Pending action status is discoverable to requesting agents without exposing human-only content. Approver cannot change immutable payload; cancel/recreate when a change is needed.
- [ ] **Step 4:** Unit and real isolated browser journeys for operator, read delegate, write delegate, foreign boundary, OAuth dot and revoked permission during edit. Test stale revision without losing draft, denied direct URL/API, read-only navigation with no global queries, hostile IDs/text, no credential values in page URLs/storage/telemetry/network logs. Screenshots at desktop and 390px for initial/loading/success/error/conflict/forbidden/human-review states, no overflow or console errors. TUI tests verify safe detail/forbidden state and no secret output.
- [ ] **Step 5:** Build-box UI Node/Go suites and screenshot journeys pass; record receipts. Commit `feat(ui): bounded admin controls and human credential review`.

---

### Task 9: Contributor rule, conformance enforcement and release gate

**Depends:** 1–8

**Files:** create repo-root `AGENTS.md` (none tracked at this checkout), add contributor link from `README.md`; `docs/admin-operations.json`, `scripts/admin-conformance.mjs` plus tests, actual CI workflow/`justfile` check target discovered at build time, `docs/PROTOCOL.md`, `docs/OPERATOR.md`, `CHANGELOG.md`, CLI/MCP guide goldens.

**Contributor rule to add verbatim (as a small section, preserving any intervening repo instructions):**

> Every new or changed admin operation ships with an authenticated HTTP contract, a CLI command, an MCP tool, a stated capability and target boundary, a safe matching read, and permission-denied and boundary-escape tests. Add web controls where users reasonably need them. Register the operation in `docs/admin-operations.json`. An omitted surface requires an explicit reason and safe alternative, checked by conformance. Credentials, invite/link codes and raw integration secrets never enter MCP inputs/results or message bodies; use the authenticated human request flow. Peer kind, OAuth consent and tool discovery never substitute for hub authorization.

- [ ] **Step 1:** Implement conformance from **actual registrations**, not just matching a manifest to itself: export operation descriptors used by HTTP registration or use a Go AST extractor including registrations in all files, plus explicitly protected non-prefix operations. Compare the complete method/path set against manifest; new admin routes without an operation entry fail. At runtime/test startup unknown admin operations default-deny. Compare CLI dispatch/help and actual MCP tool definitions against declared counterparts; test that declared names resolve and invoke the intended service operation. A dummy command/tool name cannot satisfy coverage.
- [ ] **Step 2:** Require matching read, capability/boundary, protocol/guide references, surface/test mapping and web/TUI decisions. Exceptions enumerate exact routes, reason, owner, safe alternative and review trigger. Allow raw-secret human endpoints only with safe request equivalents; host-only operations are distinguished from online admin routes. Activity SSE maps to bounded CLI/MCP read plus CLI follow, not an exemption for all observation. Existing MCP self-only names cannot satisfy operator parity. Enforce no new unclassified route via an AST fixture added in a different registration file.
- [ ] **Step 3:** Negative conformance fixtures: route without manifest, missing CLI, missing MCP, made-up counterpart, false self/operator equivalence, missing read, secret schema, unreasoned exception, new outside-prefix operator check, unsafe boundary, and stale companion rows. Positive fixtures cover safe pending requests and documented offline exceptions. Add checker to normal CI/`just` gate; current branch must have zero unexplained gaps.
- [ ] **Step 4:** Update PROTOCOL §§2.12, 4.3–4.6, 7–8, 10–12, 14, 16.2, 18.4, 13.7 with every catalog schema/permission/error/example and named delegation contract; cross-link companion owner sections. Remove hardcoded “exactly twenty-three tools” count in favor of generated verified table. Document compatibility window and end date for unconditional writes, migration from clipboard/stdout invitations, human request process, grant/revoke/OAuth consent and emergency operator recovery. No secret-bearing examples.
- [ ] **Step 5:** Acceptance on isolated build-box fixtures: operator creates bounded scopes delegate; agent reads/merges/conditionally updates grok-smoke; foreign peer denied; scope ceiling prevents broadening; OAuth client without consent challenged; consented client works; revoke stops same token; invite request never shows token to model and authorized human completes it; route/source file/human secret setup works; web reflects effective grant and revision. Run focused Go suites, protocol validation, existing MCP conformance, new admin conformance, UI tests/screens and then whole Go suite once. `go list -deps ./cmd/vecnet` must not introduce SQLite. Record commands/outcomes and any unavailable gates honestly; no live smoke on real hub.
- [ ] **Step 6:** Commit `docs(admin): enforce client parity and document delegated operations`. A coordinator reviews the final integrated diff against Review Focus before requesting merge/deployment; this planning assignment requests neither.

---

## Review Focus

1. **Coverage is real.** Every method/path registered in admin.go/op.go/avatar/OAuth plus diagnostic operator check is accounted for; source disable, team membership, unarchive, notify reads, purge and thread patch cannot disappear behind the high-profile scope fix. Existing ordinary MCP tools do not count as operator tools.
2. **The trigger is solved without guessing.** Exact scopes including deny/open and limits are readable. Conditional update preserves old entries, conflicts visibly, and checks target and recipient ceiling. Embedded directory reads remain available and accurate.
3. **Authorization survives all entrypoints.** Test the hub facade directly, HTTP and MCP; middleware relaxation cannot expose old unauthenticated helpers. OAuth-bound principal-shaped callers cannot inherit native principal authority. Revocation, dynamic team membership, peer moves and concurrent approval cannot escape boundaries.
4. **No authority laundering.** Grant ancestry, delegable bit, access, numerical ceilings, allowed kinds/destinations and final human approval all attenuate. A human approver never lends a broader permission to the initiating agent. Existing creator provenance and operator-peer protections survive.
5. **Secret non-disclosure is structural.** MCP request schemas cannot carry raw keys or server file paths; pending responses carry no redeemable capability; mint/rotate happens only on protected confirm. CLI migration covers every existing output path; audits/diagnostics and failure snapshots stay sanitized. Route URL paths/query are treated as potentially secret.
6. **Companion ownership stays intact.** Prefs preview/retention guards, R2 lifecycle/provider isolation and notices signing/relocation are tested via adapters; no hardcoded migration number, copied subsystem or conflicting tool namespace.
7. **Usable denial and read-only paths.** Stable tool listing describes requirements; tool call gives explicit forbidden or consent challenge. Web read-only delegates do not issue global queries and can understand boundaries. TUI omissions have explicit reasons and safe alternatives.
8. **Conformance fails on actual drift.** A real added HTTP route, including one in a new file, fails without CLI/MCP/read/permission coverage or an exact justified exception. Generated tables and real dispatch tests prevent manifest-only green tests.

--- SUMMARY ---

- Inventory covers existing HTTP/CLI/MCP/web/TUI operations and distinguishes operator authority from principal provisioning, group membership and self powers. Current operator directory already exposes scopes/limits; targeted reads, client access and concurrency are the immediate gaps.
- Nine dependency-ordered tasks deliver shared contracts, bounded grants/OAuth, missing reads, safe credential requests, peer parity, all remaining operational parity, companion adapters, web controls and an enforceable contributor convention. Implementation and production changes remain unperformed.
- Preferences MCP `vecnet_prefs_list/get/set` belongs to this plan after `plan-prefs` merges; registry, preview and Settings stay in that owner plan. R2 and notices are integrated by reference, preserving their security contracts. Choose migration numbers only at implementation rebase time.
- Open question for Max: may delegates administer only their own principal, or also hub-wide? Recommendation: support both explicit boundaries, default grants to own-principal and nondelegable; hub-wide is an operator-created exception, never inferred from peer kind.
- Open question for Max: should admin tools be hidden when unavailable? Recommendation: keep stable static listing and return clear forbidden/insufficient-scope errors; expose safe effective permissions and guide text so an agent can request the right authority.
- Open question for Max: may an agent receive an invite or a one-time credential link? Recommendation: no. Return only a non-authorizing pending request path; require separately authenticated human confirmation, and keep minted material in that human channel or protected local files.
- Open question for Max: can delegated scopes/limits writers expand authority up to hub defaults? Recommendation: no. Operator-managed explicit recipient/kind/numerical ceilings, including runtime dynamic-membership intersection, bound every delegated write and descendant grant.
- Open question for Max: should old invite paste blocks, `--copy` and secret stdout remain available? Recommendation: retire them with explicit release notes and safe `--output-file`/profile migration; this is a deliberate compatibility change required by the no-model-credentials contract.
- Open question for Max: how long to accept old unconditional operator writes? Recommendation: one release compatibility window with warnings, then require revisions everywhere; CLI/MCP in this plan use preconditions immediately. Set the exact release/end date at build time.
- Open question for Max: full admin forms in the TUI? Recommendation: keep TUI observation plus policy/permission reads and protected-review hints in this slice; prioritize HTTP/CLI/MCP and useful web controls, with exact TUI omissions recorded in the manifest.
