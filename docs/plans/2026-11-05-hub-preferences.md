# Hub Preferences Implementation Plan

> **For agentic workers:** This is a plan, not authorization to implement or deploy. Follow the house format in `2026-11-05-hub-notices.md`: each worker receives its task plus Global Constraints and the contracts below; steps use checkboxes; a `---` ends every task. Respect the dependency map and shared-file order. Workflow script/arguments are follow-up work.

**Goal:** Operators can inspect and change typed hub settings from Settings, CLI and an authenticated API, see where each value came from, and have workers apply changes without restarting. Retention changes cannot silently delete existing data. Theme and density belong to the current browser, with no effect on agents or other operators.

**Architecture:** A stdlib-only `internal/prefs` package owns typed keys, metadata, validation and immutable snapshots. Hub-only persistence resolves built-in → environment/flag → SQLite overrides; a transactional revision protects atomic patches and audit. Hub services and workers consume versioned snapshots rather than mutating `Hub.Cfg`. The existing operator API gains preferences and preview routes. Vanilla ES-module Settings uses those contracts; the CLI/opclient and read-only TUI share wire types without importing SQLite.

**Tech Stack:** Existing Go, SQLite/store and audit infrastructure; stdlib only for new Go code, existing vanilla JS/CSS and Node/Playwright screen harness. No new frontend framework, config service or provider SDK.

**Spec and evidence:** Max's assignment `.scratch/vecnet/integrations/plan-prefs.assignment.md`; `cmd/vecnet-hub/main.go`, `internal/hub/{hub.go,tickers.go,presence.go,loop.go,provision.go}`, `internal/httpapi/{admin.go,ratelimit.go}`, `internal/ui/static/{app.js,index.html,api.js}`, `scripts/ui/{lib.mjs,sidebar.mjs}`, `justfile` (`ui-shots`). Companion plans read with `git show`: `plan-db-compaction:docs/plans/2026-11-05-db-compaction.md` at `65f2019`, and `plan-r2-blobs:docs/plans/2026-11-05-r2-blobs.md` at `622ebcf`. Starting checkout is `79dfd2e`.

**Design decision (Max, 2026-11-05):** Use `.scratch/vecnet/prefs-proto/README.md` and `index.html`: List (variant A, Ledger) is the default; a header toggle selects Policy (variant D). Both render one draft/store, including dirty/save/undo/source state. Remember only `list|policy` in `vecnet.ui.settingsView`, with try/catch and List fallback. Flag/env-set values are read-only in both views. Saving a built-in default removes its override and shows `default` when the inherited layer is default. The prototype's claim that archive mode retains acknowledged events is superseded: 7-day acknowledged pruning continues; set `retention.ack_events=0` to retain events too. Prototype prices/growth are placeholders, never production estimates. Preserve the selected visual inputs outside scratch for implementation review; no further variant decision is needed.

## Dependency map and ownership

```json
{"1":[],"2":[1],"3":[2],"4":[2],"5":[3,4],"6":[3,4],"7":[3,5,6],"8":[7]}
```

Cross-plan sequencing is defined in [the build order](2026-11-05-build-order.md). Task 4 builds the shared confirmation service before consumer activation; it does not wait for completed DB/R2 adapters. Task 2 supplies core publication; Task 3 supplies runtime consumers and completes each adapter after its consumer merges. Task 7/sign-off waits for both. Task 1 owns registry/wire contracts; 2 persistence/migration; 3 runtime and retention adapters; 4 HTTP; 5 CLI/TUI; 6 web; 7 journeys/shots; 8 documentation and whole-surface review. Task 5 and 6 may run in parallel after 3 and 4. Shared-file order: `hub.go`, `main.go` 2 → 3; `opapi/types.go` 1 → 4; HTTP admin registration 4 only; CLI dispatch 5 only; UI app/nav/API 6 only; screenshot index/README 7 only; protocol/changelog 8 only. Rebase against companion implementations before touching their files. Allocate one preferences migration at the **next free number at build time**, inspecting the merged migration directory; master currently ends at 0020 and hub-notices will take 0021; never reserve a literal number or alter shipped migrations. This is the only prefs migration (values/state/activations); DB and R2 add only their own domain tables.

## Decisions and contracts

### 1. Typed registry, sources and authority

Registry definitions contain `key`, type (`bool`, `int`, `duration`, `enum`), typed default, allowed range/enum, description/help/label, group, setter scope, runtime status, consumer and danger classifier. Durations are nonnegative whole seconds encoded as integers on the wire and in SQLite; human CLI/UI accept units and convert with overflow checks. Zero disables only keys explicitly declaring that meaning. No float coercion, arbitrary JSON, secret, URL or free-form string preferences. Stable keys are shared by all clients; unknown keys are errors, never silently stored.

Canonical exported seam (consumers use these names/types; metadata implementation details may follow repository style):

```go
// internal/prefs imports stdlib only; opapi uses wire DTOs from this package.
type Source string // default | flag | stored
type Definition struct { /* metadata above; typed Value and constraints */ }
type Value struct { /* tagged bool/int64/enum, no untyped map access */ }
type Effective struct { Value Value; Source Source; Origin string; Revision uint64 }
type Snapshot struct { Revision uint64; ETag string; /* immutable definitions + effective values */ }
func (s Snapshot) Get(key string) (Effective, error)
func Validate(def Definition, value Value) error
// Hub-only service in internal/hub/preferences.go:
// Concrete hub-only service methods (s is *Preferences):
// s.LoadSnapshot(ctx context.Context, tx *sql.Tx) (prefs.Snapshot, error)
// uses the caller transaction; nil opens a consistent standalone read snapshot.
// A narrow reader interface with this method and Subscribe below can live in
// internal/prefs (database/sql is stdlib); internal/blob must not import hub/store.
// s.Subscribe(ctx context.Context) <-chan uint64 -- latest revision wakeup, closed on cancel.
// s.notify(revision uint64) -- private after-commit fan-out, never a public writer.
// ReadPreferences(ctx, caller) (prefs.Snapshot, error)
// PreviewPreferences(ctx, caller, expectedETag, patch) (prefs.Preview, error)
// PatchPreferences(ctx, caller, expectedETag, patch) (prefs.Snapshot, error)
// prefs.Patch contains Set map[string]Value, Reset []string, Confirm []string,
// ApplyExisting bool; prefs.Preview is the typed DTO for the HTTP preview below.
```

`Snapshot.Get(key)` returns `Effective{Value,Source,Origin,Revision}` plus error; its revision equals the enclosing snapshot. Consumers call `Preferences.LoadSnapshot(ctx, tx)` and then `snapshot.Get(key)`, never a separate `prefs.Get/Set/RetentionSnapshot` store. All use `prefs.Validate(Definition, Value)`, complete-policy validation and the same audited preview/PATCH service. `Subscribe(ctx)` wakeups prompt re-read; `notify` is private and fires only after commit. `Get` always returns value plus source and revision. `Origin` optionally identifies the nonsecret flag/env name. Built-in default is lowest priority, then env, then explicitly supplied flag, then stored override. Wire source remains `flag` for both environment and flag initialization, with `origin` distinguishing them. Capture explicit flag visits and environment presence; an explicitly configured value equal to the built-in still has source `flag`. Defaults are a fallback layer, not an INSERT of every key on startup. Restart never overwrites stored choices. Reset removes the override and reveals the current bootstrap layer, not necessarily the built-in default. Validate bootstrap values before use; invalid or unsupported persisted values fail startup with key/code only and a recovery runbook, never a silent destructive fallback.

Until admin parity lands, every hub-wide key is readable/settable only by an operator credential; principal, peer, dot/OAuth and delegated/as-peer identities cannot elevate authority by naming a scope. Recheck in the hub service, as well as existing `/v1/admin/` middleware. Future per-peer preferences need a separate authenticated scope/table/key namespace and ownership model. Do not add a client-supplied `peer_id` or `scope` escape hatch now. Existing peer/principal limits, scopes and notification configuration remain authoritative dedicated resources.

[Admin parity](2026-11-05-admin-parity.md), Tasks 1/2/7, exclusively owns `vecnet_prefs_list/get/set`, their registration/schema/guide and capability `admin:prefs` with read/write access, hub boundary only; OAuth additionally requires `admin:prefs:read|write` (write includes read). When its authorization facade lands, this plan's HTTP/CLI/web and read-only TUI use those same service checks; no operator-only middleware may block an authorized delegate, and no surface may skip capability checks. Until then they remain operator-only; principal/peer/OAuth identities receive no implicit permission. Protected human confirmation remains mandatory for model-initiated dangerous changes. This plan owns registry/store/preview/HTTP/CLI/Settings, never duplicate MCP tools or a second capability implementation.

### 2. Persistence, audit and optimistic concurrency

One additive next-free migration creates (master ends at 0020; hub-notices takes 0021; allocate only after build-time rebase):

- `hub_preferences(key TEXT PRIMARY KEY, value_json TEXT NOT NULL CHECK(json_valid(value_json)), updated_at TEXT NOT NULL, updated_by TEXT NOT NULL) STRICT`.
- `hub_preferences_state(id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL CHECK(revision>=0)) STRICT`, seeded to revision 0.
- `hub_preference_activations(revision INTEGER NOT NULL, key TEXT NOT NULL, effective_from TEXT NOT NULL, not_before TEXT, cutoff_at TEXT, high_water INTEGER, approval_digest TEXT NOT NULL, PRIMARY KEY(revision,key)) STRICT`. This records deletion policy boundaries and explicit authorizations, not job payloads or copies of messages.

Use existing `audit.Record`/`audit` rows for append-only `prefs_changed` records in the same transaction: actor identity, changed keys, typed old/new values and sources, reset/set operation, revision and approved risk codes. These keys contain only bounded nonsecret scalars. Validation/auth failures log only sanitized reason codes/known keys, never raw rejected values, request bodies, confirmation tokens or credentials. A failed transaction leaves no values, revision, activation or success audit rows. No-op operations that change neither stored state nor effective source produce no revision/audit. Setting a value equal to the built-in default normalizes to removing the override when the inherited bootstrap layer is also default; it shows source `default`. If an explicit flag/env layer differs, an explicit operator API override equal to the built-in is `stored`; Reset reveals `flag`. The UI cannot edit a flag/env-set row and never claims this override is `default`. Explicit stored zero/false differing from fallback remains a real override.

A strong ETag covers DB revision plus a canonical hash of effective bootstrap values, registry version and immutable deployment policy generation: e.g. `"prefs-12-<digest>"`. Thus restart with changed flags/defaults/limits invalidates stale edits even when the DB revision is unchanged. Read DB revision and rows in one consistent read snapshot. PATCH requires exactly one matching `If-Match`, checks it again under `store.Tx`'s writer transaction, validates the complete prospective snapshot and commits all keys atomically. Reject `*`, weak or multiple tags. One stale field rejects the entire patch. Reset participates in the same transaction. ETags contain no secrets. The service serializes local publication with writes so an earlier committed snapshot can never overwrite a newer one.

### 3. Runtime application and retention safety

Never write shared mutable `h.Cfg` fields from HTTP. Install an immutable snapshot via `atomic.Pointer` after commit, before returning success. Requests load once per operation; transactional consumers read the applicable persisted preference/revision through their transaction when deletion or quota consistency requires it. Startup loads stored state before workers start. After-commit notifications are local capacity-one wakeups carrying the latest generation, not durable Events or Message deliveries. Consumers re-read the newest snapshot, so bursts can coalesce. Reconnect/read returns the latest state even if a wakeup was missed. Failed persistence sends no notification. A crash between commit/publication recovers on restart.

Ticker/worker loops select on cancellation, timers and preferences wakeups; stop/drain/reset timers safely and reread on the next iteration. Events refresh updates the next refresh schedule, including existing subscriptions, while preserving lease/expiry and avoiding catch-up storms; stagger due work within existing budgets. Presence recalculates from stored report timestamps and wakes stale-held evaluation; warn that shorter TTLs may release held wakes. Request defaults affect only newly created messages/threads. Existing expires_at, max_depth, budgets, explicit peer limits and callback bodies stay frozen. Provisioning defaults do not widen kinds or change current explicit principal policies. Runtime preferences do not relax parser/wire hard caps or auth policy. Add a nightly retention schedule (the current ticker has only hourly slow jobs) as maintenance config, not a preference, with initial default 02:00 UTC; compute and persist the first scheduled instant strictly after confirmation. If a restart misses that instant, resume at the next available sweep, never change the approved cutoff/high-water. Wakeups cannot bypass `not_before`.

**DB companion mapping:** The refreshed DB plan defines `retention.ack_events` (7d), `retention.history` (0/forever) and `retention.archive_mode` (false), consumed from this plan's shared typed-store seam. Map them to its existing consumer, never a second GC. Archive mode retains Message history, revisions/snapshots and their readable dependencies, suppresses marking/hiding/sweeping and hard unacknowledged-event expiry, including already marked rows. Acknowledged operational events may still prune under ack retention and owner floors; archive mode does not promise indefinite replay. Set ack retention to zero for that. Archive mode is not export or permanent attachment-byte storage. Effective history duration is 0 while archive mode is on; the configured finite duration stays visible. Default archive off/history 0 matches the DB plan and preserves existing history. Turning archive mode off always asks confirmation; with finite history or hard unacked expiry it requires a dry-run and the separate existing-data authorization before retrospective eligibility. With expiry disabled the confirmation authorizes no later deletion. No impact on automatic group archiving.

The DB adapter uses exactly this registry and source vocabulary `default|flag|stored`; environment provenance uses `flag` with env-name `Origin`. Its local retention-policy projection reads all three keys from one `Preferences.LoadSnapshot(ctx, tx)` and retains `Snapshot.Revision`; it is not another preference store. On read/validation failure workers skip expiry. Both plans use this one persistence migration and confirmation service.

**No retroactive delete by ordinary Save:** Every newly enabled/shortened retention policy records `effective_from`. Rows created before that boundary retain the previous eligibility rule. An upgrade establishes the boundary before first cleanup; existing default 7-day acknowledged-event pruning must first be dry-run/approved before deleting pre-upgrade rows. Worker eligibility reads activation boundaries and rechecks them inside each bounded deletion transaction, retaining the DB plan's ack/pin/floor/mark-sweep invariants. Neither a PATCH nor an audit entry performs deletes. Returning to a safer policy cancels unconsumed old deletion authorization; already-deleted bytes cannot be restored by Undo.

Operators can separately request a **preview** of applying a specific policy to existing data. It returns fixed cutoff time, captured high-water, eligible/pinned counts and estimated bytes (explicitly estimates), cursor/history/export implications and required risk codes. The second confirm commits a bounded authorization for those keys, cutoff and high-water only; rows arriving later cannot drift into the old preview. Pins are rechecked at deletion time. Policy changes invalidate outstanding previews and cancel unfinished incompatible cleanup. Workers serialize each deletion slice with preference writes via SQLite's writer lock and re-read authorization/revision, so an older snapshot cannot continue deleting after a more protective change commits. Backup/restore retains these boundaries/approvals. If adapters are unavailable, show the key as unavailable and reject writes; never acknowledge a nonfunctional retention setting.

**R2 companion integration:** [R2](2026-11-05-r2-blobs.md) consumes `blobs.expiry` as enum `1h|6h|12h|1d|7d|30d|off`, default `1d`; it is not a duration/zero API. Map finite enum values to seconds only in the transfer adapter. `off` records class `keep`, `expires_at:null` / `object_expires_at:null`; do not add a competing `expiry_disabled` or `expiry_policy_version` contract. Capabilities expose the enum/source/revision and `retention.seconds:null` for off. New grants snapshot value/source/revision; existing keys/deadlines never change or revive, and retrospective file expiry is rejected even with preview. R2 owns class-first keys `attachments/v1/exp/<class>/<hub_namespace>/<blob_id>` and `attachments/v1/keep/<hub_namespace>/<blob_id>`, six shared expiration rules and no keep expiration. Provider setup is an operator step, never a PATCH side effect. Preserve legacy disk/pin behavior and durable spills/source raw/avatar data.

The selected checksum profile is `sha256`, enforced by signed `x-amz-checksum-sha256`; MD5 fallback is parked, not build work. R2's wire/signing contract owns PUT 300s and repeatable GET up to 600s (floor-capped by the immutable finite object deadline, 600s for keep), including explicit MCP transfer tools. Preference adapters must not impose the obsolete 120s GET limit.

Hosted off uses this same preview → explicit confirm → conditional PATCH with a storage-cost risk, current retained/reserved/used/limit estimates, lack of disaster expiry and a finite positive quota. No separate blob confirmation writer or token is allowed. R2 storage quota is bootstrap-only `--r2-storage-quota-bytes` / `VECNET_R2_STORAGE_QUOTA_BYTES`, default 5 GiB, strictly positive; it is **not a preference** and has no registry key or runtime Settings editor. Lowering config below usage blocks new grants without deleting retained objects. Verified backend/deployment capabilities constrain the enum; unsupported lifecycle/off support fails closed, and confirmation cannot bypass a hard policy lock. Hosted off is supported once verified keep storage and quota are installed; there is no unresolved product decision or universal 24h hosted maximum.

**One confirmation schedule:** archive off always requires preview/explicit confirmation. A new/shorter DB retention policy also requires preview/confirmation even when prospective-only; its default `apply_existing:false` leaves earlier rows under their previous rule. Applying to existing rows requires a separate explicit choice and fixed cutoff/high-water authorization. Confirm only records policy/activation/audit, never deletes or queues an immediate trim. Confirmed retrospective trimming starts at the **next nightly sweep after confirmation**, with a persisted `not_before` at the next nightly sweep, separate from the prospective row boundary `effective_from`, and fixed cutoff/high-water in `hub_preference_activations`. Wakes refresh policy but cannot trigger early deletion. Returning to a protective policy cancels incompatible unused authorization. R2 expiry edits affect new uploads only; its 30-second recorded-deadline ticker is independent of retrospective DB trimming.

### 4. First registry and rollout classification

These are proposed bounds unless a narrower existing hard cap applies; confirm the merged protocol validators in Task 1 and preserve all hard caps. `Now` means runtime support is included in this plan; `Gated` means included once its companion adapter is built; `Later` entries are documented read-only descriptors with `editable:false` and a reason, never silently writable placeholders. All hub entries are operator-only initially and use `admin:prefs` write at hub boundary once admin parity lands; metadata must reflect that transition.

| Key / section | Type; built-in default; permitted values | Consumer and timing |
|---|---|---|
| `retention.ack_events` / Retention & storage | duration; 604800; 0 or 3600..31536000 | Gated DB acknowledged prefix GC; zero disables; preview for existing rows |
| `retention.history` / Retention & storage | duration; 0; 0 or 86400..315360000 | Gated DB Message mark/sweep; zero forever; dormant in archive mode |
| `retention.archive_mode` / Retention & storage | bool; false | Gated DB adapter above; confirm off |
| `blobs.expiry` / Files | enum; `1d`; `1h`, `6h`, `12h`, `1d`, `7d`, `30d`, `off` | Gated R2 new grants; verified deployment may narrow; explicit off cost confirmation + finite bootstrap quota |
| `events.refresh_interval` / Notifications/presence | duration; 1800; 60..86400 | Now; `--events-refresh-interval`, `VECNET_EVENTS_REFRESH_INTERVAL`; reset worker timers |
| `presence.default_ttl` / Notifications/presence | duration; 3600; 60..86400 | Now; `Cfg.PresenceTTL`; effective presence and held-wake evaluation |
| `presence.session_ttl` / Notifications/presence | duration; 900; 60..86400 | Now; `Cfg.SessionPresenceTTL`; same warning on shortening |
| `presence.launcher_ttl` / Notifications/presence | duration; 900; 60..86400 | Now; `Cfg.LauncherPresenceTTL`; same warning |
| `agents.default_max_depth` / Agents defaults | int; 32; 1..64 | Now; `Cfg.DefaultMaxDepth`; omitted loop field on new sends only |
| `agents.default_ttl` / Agents defaults | duration; 86400; 60..604800 | Now; `Cfg.DefaultTTL`; must be ≤ MaxExpiry; new sends only |
| `agents.thread_budget` / Agents defaults | int; 500; 1..10000 | Now; `Cfg.ThreadBudget`; new threads only |
| `rates.sends_per_minute` / Agents defaults | int; 60; 1..600 | Now as new-peer default; `insertPeer` copies Cfg today; existing peer rows unchanged |
| `rates.threads_per_hour` / Agents defaults | int; 20; 1..200 | Now as new-peer default; explicit per-peer limits remain authoritative |
| `provisioning.creates_per_hour` / Agents defaults | int; 60; 1..600 | Now fallback/new policy default; replace `DefaultCreatesPerHour` lookup, explicit principal rows win |
| `provisioning.max_peers` / Agents defaults | int; 0; 0..10000 | Later; existing absent policy permits no provisioning; never widen on upgrade |
| `rates.anonymous_burst`, `rates.anonymous_refill` / Agents defaults | int 30 (1..300); duration 2 (1..60) | Later; `httpapi.Limiter` owns mutable bucket state; needs locked reconfiguration preserving token balances |
| `rates.avatar_changes_per_hour` / Files | int; 10; 1..100 | Later; preserve `DefaultAvatarChangesPerHour` until all consumers migrate |
| `agents.max_expiry` / Agents defaults | duration; 604800; 86400..31536000 | Later; explicit loop TTL protocol/policy cap, not just a default |
| `agents.text_cap` / Agents defaults | int; 4096; 1024..65536 | Later; spill/MCP/connector representation review needed |
| `files.read_token_ttl` / Files | duration; 604800; 60..604800 | Later; current `Cfg.BlobTokenTTL`; distinct from object expiry and S3 download grants |

The only existing runtime-related hub flag in this inventory is Events refresh; many others are Go config fields/constants, not shipped flags. Add matching nonsecret bootstrap flags/env for enabled keys (`--presence-ttl`/`VECNET_PRESENCE_TTL`, `--session-presence-ttl`, `--launcher-presence-ttl`, `--default-max-depth`, `--default-ttl`, `--thread-budget`, `--sends-per-minute`, `--threads-per-hour`, `--creates-per-hour`, with matching uppercase `VECNET_` underscore names). Companion retention flags/env map to the same registry (`--retention-ack-events`, `--retention-history`, `--retention-archive-mode`, `--blobs-expiry`; matching `VECNET_RETENTION_ACK_EVENTS`, `VECNET_RETENTION_HISTORY`, `VECNET_RETENTION_ARCHIVE_MODE`, `VECNET_BLOBS_EXPIRY`). Reconcile companion spellings at integration, documenting any compatibility aliases instead of two competing config paths.

Keep listen/data-dir/public URL, dev UI path, private egress, trusted proxy, OAuth redirects, MCP allowed origins/legacy sessions, blob backend/bucket/credentials, identity keys and hosted policy limits bootstrap-only and outside the registry/API. Notification webhook URLs and per-principal allowed kinds/scopes retain existing APIs. The Notifications section links to their existing management surface when available; do not invent a global notifications boolean that disables peer delivery. Appearance contains only browser preferences: theme enum `system|dark|light` default `system`, density enum `comfortable|compact` default `comfortable`.

### 5. Operator HTTP API and errors (PROTOCOL addition)

Add **§ Hub preferences** under operator API §2.12, with linked runtime/retention semantics in §8/§11 and operator-client guidance. Choose the next free subsection after rebasing; do not hardcode the hub-notices section number. Add request/response/error examples and validate them through the existing protocol tooling. No new peer event type is needed.

`GET /v1/admin/prefs` → 200 JSON, `ETag`, `Cache-Control: no-store`:

```json
{"revision":12,"preferences":[{"key":"events.refresh_interval","type":"duration","value":1800,"source":"flag","origin":"VECNET_EVENTS_REFRESH_INTERVAL","default":1800,"min":60,"max":86400,"unit":"seconds","label":"Events refresh interval","description":"Refresh cadence for MCP Events subscriptions.","group":"notifications_presence","settable_by":"operator","editable":true,"runtime":"live","danger":[]} ]}
```

Full response also includes applicable `allowed_values`, `disabled_value`, `unavailable_reason`, deployment restrictions and last change time/actor when present. Stable key ordering. Group identifiers: `retention_storage`, `files`, `notifications_presence`, `agents_defaults`; Appearance is client-only. GET contains no arbitrary config dump. Read-only/unsupported keys carry a reason. Browser may refresh conditionally by ETag; no caching by shared proxies.

`PATCH /v1/admin/prefs`, `Content-Type: application/json`, `If-Match: <GET ETag>`:

```json
{"set":{"events.refresh_interval":600},"reset":[],"confirm":[],"apply_existing":false}
```

All fields optional except at least one set/reset operation; no key may occur in both. Maximum 64 keys and 16 KiB body. `reset` removes stored overrides. Strict decoding rejects unknown/duplicate fields, duplicate preference keys, fractions, negatives, overflow, null and wrong types. `confirm` is an array of server-issued preview tokens, never a blanket true or `force`. A normal safe change needs no preview. A dangerous change returns confirmation requirements without writing. Success → 200 complete effective snapshot + new ETag, same shape as GET. A failed multi-key validation writes nothing.

`POST /v1/admin/prefs/preview` accepts the same proposed set/reset and `apply_existing` boolean, with `If-Match`. It is read-only: no deletion, preference or revision mutation. Return `{base_revision,risks:[{code,keys,message}],impact:{cutoff_at,high_water,not_before,eligible,pinned,estimated_bytes,storage:{retained_bytes,reserved_bytes,used_bytes,limit_bytes}},confirm_token,expires_at}`; counts may be absent when not needed. `storage` is required (used = retained + reserved, finite positive limit; used may exceed limit) whenever the `blob_expiry_off` risk is present and is signed into the approval. Five-minute token is HMAC-bound to authenticated caller and protected human action when applicable, exact canonical patch, ETag, deployment generation, risk set and fixed impact bounds, with a separate domain separator using the existing private hub key. No tokens in URLs, logs or audit. For retrospective DB work, preview includes and signs the next scheduled sweep as `impact.not_before`; if confirmation crosses that instant or the maintenance schedule changes, return `prefs_confirmation_stale` and preview again. Revalidate bounds/policy at PATCH; key/policy/ETag change or expiry requires a fresh preview. A replay after successful commit fails ETag. Token alone grants no auth. `apply_existing:true` is supported only for DB retention keys and always requires preview/confirmation, even if the configured value itself is unchanged. Such an authorization increments revision and audits even with no value delta. Preview does not lock the DB while collecting estimates; use consistent read snapshots and label estimates.

| HTTP / code | Meaning and recovery |
|---|---|
| 400 `invalid_body` | Malformed/duplicate/unknown fields or invalid If-Match syntax; fix request |
| 401 / existing auth code | Missing/invalid credential; retain current auth contract |
| 403 `forbidden` | Missing operator authority before parity, or `admin:prefs` access/hub boundary after parity; no preference values disclosed |
| 413 `payload_too_large` | Over 16 KiB; keep existing HTTP error envelope |
| 415 `unsupported_media_type` | PATCH/preview requires application/json |
| 422 `prefs_unknown_key` | Unknown key; details known key name only, bounded/sanitized |
| 422 `prefs_invalid_value` | Type/range/enum/cross-key violation; details field and constraint, no raw value |
| 409 `prefs_unavailable` | Required worker/backend adapter absent or key not runtime-editable |
| 409 `prefs_policy_locked` | Hosted plan/storage policy disallows value; confirmation cannot override |
| 428 `prefs_precondition_required` | Missing If-Match; GET first |
| 412 `prefs_revision_conflict` | Stale ETag; reload and explicitly reconcile draft |
| 409 `prefs_confirmation_required` | Missing risk approval; call preview, show impact, confirm |
| 409 `prefs_confirmation_stale` | Expired/mismatched preview; preview again |
| 503 `prefs_storage_unavailable` | Persistence failed; no partial success, retain draft |

Use the existing API error envelope `{error:{code,message,details}}`, with empty details encoded as `{}`; reuse existing generic status codes rather than changing unrelated endpoints. Confirm the exact shipped envelope in Task 4 fixtures. HTTP errors disclose no secret/provider configuration. No peer-visible preferences listing.

### 6. CLI, TUI and browser behavior

`vecnet prefs list [--json]`, `vecnet prefs get KEY [--json]`, `vecnet prefs set KEY VALUE [--json]` are initially operator commands (after parity: matching `admin:prefs` read/write at hub boundary) under the selected `--net` profile. Human list includes key/value/source/live-or-unavailable; get includes help/range/effect/deployment lock. CLI uses GET's ETag for one PATCH and never retries a 412 automatically. `set --reset KEY` is the explicit reset form (VALUE prohibited). `set KEY VALUE --preview [--apply-existing]` prints safe impact and the confirmation phrase, holding the opaque token in memory only. Interactive set previews and asks a human to confirm risks. For noninteractive automation, `--confirm-risk CODE` repeated for every required risk explicitly authorizes preview-and-apply of that risk set; `--apply-existing` is separately required for retroactive retention. Unexpected/new risks abort. Do not introduce a generic `--yes` bypass. JSON mode returns structured codes and never tokens. Success exit 0, validation/conflict/refusal exit 1, usage exit 2; existing auth/transport conventions otherwise prevail. No config-file rewrite or raw server body printing.

TUI adds a discoverable read-only Preferences view through existing command/navigation conventions (`:prefs`), grouped list with value/source/status, help pane and refresh. It uses opclient, handles forbidden/older-hub 404 as unavailable, cleans text through existing TUI rendering and performs no PATCH. Avoid assigning an already-bound key until inspecting the merged keymap.

Web adds Settings to sidebar nav and hash route `#settings` in the existing `app.js` router/SCREENS. Five sections: Retention & storage, Files, Notifications/presence, Agents defaults, Appearance. List (A/Ledger) is the default and a header toggle selects Policy (D), remembered with try/catch at `vecnet.ui.settingsView`. Both are projections of one store: toggling retains edits, dirty state, Save/Undo, errors, source badges and confirmation state. List uses label/help left and controls right, search, All/Customised/Flags filter and an on-page rail; Policy uses keyboard-accessible retention lanes, a clearly labeled estimated storage forecast and sentence/token editors. Do not encode a second draft or mutation path. Each hub field has label/help, current wire source badge (`default`, `flag`, `stored`; prototype `saved` maps to `stored` and `edited` is a draft state), effective value/unit/range, saved value and disabled explanation where applicable. Give rate default rows copy saying “For newly created agents”; no implied bulk policy changes. For retention show archive mode's effect, exact object expiry vs cleanup lag, and links to backup/export instructions. Unsupported/hard-locked values have visible reasons, not hidden failures. Flag/env-set rows are read-only in both views with nonsecret origin/help; switching views cannot unlock them. Archive lanes disable history expiry only; the acknowledged-event lane remains active at 7 days unless explicitly set to 0. Both views normalize a Save back to built-in fallback to Reset so the response shows `default`; they never invent that source locally.

Edit a draft locally; explicit Save submits one atomic patch of dirty hub keys; Undo restores last fetched/saved values and clears errors without network mutations. A separate Reset to inherited value is a dirty operation with the fallback displayed before Save. Saving disables repeat submission and announces progress; Saved updates source/revision from server response, clears draft and announces success. Validation errors stay beside fields, focus the first invalid field and keep valid edits. Server errors keep the draft. Conflict shows reload/compare with latest values, retains the draft for manual reconciliation and requires another Save; never overwrite automatically. Unsaved navigation/sign-out warns; do not put drafts in localStorage or URLs. Reloads while the form is dirty show an update notice rather than replacing typed edits. Refresh on focus/explicit Reload; a live SSE preference event is unnecessary in this slice.

Danger confirmation is a focus-trapped, keyboard-accessible dialog showing old/new values, affected scope, irreversible effects, impact estimate/pins and required acknowledgement. Cancel preserves draft and writes nothing. Confirmation for archive off, blob expiry off and shorter presence TTL is specific; `apply_existing` has a separate unchecked choice with a second preview and backup/export warning. Never imply browser Undo restores deleted data. All labels/help/error text use textContent and existing hostile-text handling. At phone width stack rows, preserve readable help, reachable Save/Undo and no horizontal overflow; report progress and errors via aria-live.

Theme/density are **per-browser operator UI** preferences in versioned localStorage key `vecnet.ui.appearance.v1`, not hub settings, API values or flags. Store only validated enum scalars, no credential/identity/draft data in that object; catch blocked/corrupt storage and fall back to defaults. Explicit local Apply/Undo controls in Appearance are separate from hub Save. Apply root theme/density attributes, `prefers-color-scheme` for system, and storage-event synchronization across same-origin tabs. Existing login credential storage is outside this task. No cross-device sync or per-peer themes promised.

## Global Constraints

- This reconciled planning assignment writes and commits only the four named plans and build-order document on `plans-1105`, plus the untracked result JSON at the assigned main-checkout scratch path. Future implementation uses the owning task scopes. No implementation, merge, push, provider action, real configuration changes or deployment. Do not use `attn`; never print secrets. Exploration is read-only.
- Future builds/tests/screens run on the build box per `docs/BUILDBOX.md`: `BB_JOBS=2 just bb CGO_ENABLED=0 go test -p 2 <packages>`, `just bb build2 node --test --test-concurrency=2 <tests>`, `just bb build2 node scripts/ui/prefs.mjs`. Race checks use the build box's supported CGO compiler and at most two jobs. Do not build on this PC.
- Use disposable synthetic fixtures, injected clock/environment and temp HOME/XDG/VECNET_CONFIG. No real user configs/credentials, `.data/`, `.demo-data/`, dev hub 45752, preview 49575 or shared browser. Screens use `withDemo` and private tmux `-L oui-<pid>` only if TUI shots need it. Never log auth/confirmation tokens or signed URLs.
- `internal/prefs`/wire/opclient/CLI must remain SQLite-free. `go list -deps ./cmd/vecnet` must not acquire `modernc.org/sqlite` or `internal/store`. Hub persistence stays behind hub/store interfaces.
- Preserve DB retention's owner floors, pending/held/outbox/dispatch pins, immutable replay, history tombstones/high-water and bounded writer slices. Preserve R2 authorization, reservation quotas, per-incarnation identity, durable content and credential-free transfers. Confirmation never weakens these invariants.
- Implement only adapters supported by merged companions. Retention and editable R2 expiry are required release gates; unavailable states enable staged integration but cannot count as finished feature acceptance. Migration numbers and protocol sections are allocated after rebase. No new dependencies or arbitrary remote fetch fields.

## Review Focus

1. Stored overrides survive flags/restart; source and reset behavior are accurate; concurrent operators never silently overwrite each other.
2. Preference/audit/activation writes are atomic; a crash recovers the committed snapshot; workers consume generations without races, restart or timer leaks.
3. Archive off/new or shorter retention require the shared preview/confirm; retrospective work cannot run before its persisted next-nightly `not_before`. Retention never retroactively deletes through normal Save; explicit existing-data preview/approval is scoped, bounded and checked inside every slice. More protective policy updates stop stale cleanup.
4. Archive mode is history protection, not an export promise. Off/finite policy preserves pins/cursors and never revives deleted data.
5. Expiry off truly uses nonexpiring backend support; a one-day lifecycle cannot contradict the UI. Old object deadlines and short capability deadlines remain independent.
6. Operator-only until parity; then all surfaces enforce admin parity's `admin:prefs` read/write, hub boundary and OAuth gates, with no duplicate MCP ownership. Registry never exposes secrets, origins with credentials or unsupported stored keys. CLI/TUI import no DB driver.
7. Both List/Policy views share drafts, preserve read-only flag/env locks and default-reset badges; all 32 state/viewport screenshots are inspected. Settings preserves drafts, handles conflicts/failures, works by keyboard/phone, and source badges/confirmations tell the truth. Appearance remains local and distinct from hub Save.

---

### Task 1: Registry, metadata and shared wire contract

**Depends:** none.

**Files:** Create `internal/prefs/{registry.go,value.go,types.go,registry_test.go,value_test.go}`; add preference DTO exposure in `internal/opapi/types.go`. Inspect merged protocol/config validators; no persistence yet.

**Interfaces (Produces):** Typed `prefs.Definition/Value/Effective/Snapshot/Patch/Preview`, exact `Snapshot.Get`/`prefs.Validate` and source vocabulary above, serializable public descriptors/patch/preview/error details; stable keys/ranges/source/group conventions above. Registry includes available and later descriptors, with later descriptors unwritable.

- [ ] **Step 1:** Write failing table tests for every default, min/max/enum/zero rule, cross-key TTL bound, unknown/duplicate keys, JSON wrong types/fractions/null/overflow and deterministic metadata order. Assert all keys have owner/group/help/consumer and no secret/free-text types.
- [ ] **Step 2:** Run `just bb CGO_ENABLED=0 go test -p 2 ./internal/prefs/ ./internal/opapi/`; observe new failures, implement registry and wire DTOs.
- [ ] **Step 3:** Run those suites and schema round-trip fixtures. Confirm protocol hard caps and actual default values against the merged code; resolve any narrower bounds explicitly.
- [ ] **Step 4: Commit** `feat(prefs): define typed settings registry and wire metadata`.

---

### Task 2: One migration, fallback resolution and transactional writes

**Depends:** 1.

**Files:** Create one `internal/store/migrations/<next>_hub_preferences.sql`, migration tests, `internal/hub/{preferences.go,preferences_test.go}`; modify `hub.go`, `cmd/vecnet-hub/{main.go,main_test.go}`; use existing audit package.

**Interfaces (Produces):** Startup bootstrap layer with source provenance, persistent overrides/revision/activations, consistent reads and atomic patch primitive, plus core immutable publication and `Subscribe`/private `notify` required by Task 4. Task 3 owns consumer/timer adapters, not the existence of this core service. No HTTP handler directly accesses SQL.

- [ ] **Step 1:** Write failing fresh/upgrade/reopen tests, unchanged shipped checksums, default/env/explicit flag/stored precedence including equal values, reset fallback, invalid persisted state, no secret exposure, no-op/source-only changes snapshot immutability and setting the inherited built-in default normalizing to Reset/default badge.
- [ ] **Step 2:** Add concurrent multi-key CAS, audit rollback/storage failure, revision monotonicity, restart bootstrap ETag invalidation and crash-after-commit tests. Guard no auth bypass in hub service.
- [ ] **Step 3:** Observe failures with build-box store/hub/main suites; implement the additive migration, source resolver and transactional revision/audit writes. Add bootstrap flags only for enabled keys, preserving old startup behavior.
- [ ] **Step 4:** Run affected suites; inspect audit rows and startup/restart fixtures, proving no partial success or stored override overwrite, after-commit latest-only wakeups, cancellation and publication ordering.
- [ ] **Step 5: Commit** `feat(prefs): persist operator overrides with audited revisions`.

---

### Task 3: Runtime consumers, safe activation and companion adapters

**Depends:** 2; DB retention implementation and R2 transfer implementation must exist before their respective adapters can pass acceptance.

**Files:** Create `internal/hub/{preferences_runtime.go,preferences_runtime_test.go,preferences_retention.go,preferences_retention_test.go}`; modify `hub.go`, `tickers.go`, `presence.go`, `loop.go`, `send.go`, `provision.go`, `internal/events/{worker.go,store.go}` as needed. Integrate companion retention consumers and R2 grant/metadata/GC code, `internal/blobwire`, S3 signing/metadata adapter, capability projections and client parsers in serialized companion-owned commits. No additional preferences migration; amend new companion schema only before it ships, otherwise coordinate additive companion migrations separately.

**Interfaces:** Consume Task 2 publication/`LoadSnapshot`/`Subscribe`; wire versioned immutable snapshots into runtime consumers; retention eligibility/preview bounds; deployment backend policy capabilities; versioned optional-expiry wire/S3 grant contract described above.

- [ ] **Step 1:** Write failing tests that live refresh/presence changes apply without restart, rapid changes coalesce to newest generation, canceled workers stop cleanly, a single operation sees one snapshot and existing thread/message defaults remain frozen. Test new-peer rate defaults and explicit principal/peer policies unchanged.
- [ ] **Step 2:** Add deletion-safety tests: pre-change rows untouched without apply-existing; fixed cutoff/high-water and next-nightly not-before across restart; no trim on confirm/wakeup; archive on/off/history zero with ack pruning continuing; confirmation cancel/stale policy; pins retained; concurrent protection change stops the next stale cleanup slice; restart/backup preserves boundaries. Retain DB cursor/replay guard suites.
- [ ] **Step 3:** Add R2 tests for default 24h, finite shorter/longer and off with fake provider capabilities; unsupported lifecycle/off rejects; verified hosted keep + finite quota permits off only after shared cost confirmation; existing objects never extend/revive; off survives restarts, completion and S3 grant validation; short grants still expire. Keep all R2 zero-Fly-file-byte/authorization/quota guards. Test the R2 capabilities enum/nullable object-deadline and legacy disk compatibility in Go/JS/TS/MCP serializers; do not invent a parallel expiry-policy version.
- [ ] **Step 4:** Observe failures in affected build-box suites; implement publication/timer updates and adapters. Configure no real provider resources. Unavailable states reject writes until consumers are installed; document the required verified lifecycle-prefix deployment receipt for real enablement.
- [ ] **Step 5:** Run focused races for publication/timers/cleanup plus companion DB/R2 guard suites, browser/connector parsing and direct S3 signer/metadata tests. Record consumers and application timing per key; resolve every required gated row before feature sign-off.
- [ ] **Step 6: Commit** isolated runtime/DB/R2 adapter commits with messages describing each final seam; record the integration order in the implementation receipt.

---

### Task 4: Operator HTTP API, preview and confirmation

**Depends:** 2; shares Task 3 runtime publication but does not wait for completed consumer adapters. Uninstalled consumers remain unavailable.

**Files:** Create `internal/httpapi/{prefs.go,prefs_test.go}`; modify `admin.go`, opapi DTOs if needed and focused auth/middleware tests. Add preview-token implementation/tests in hub preferences code.

**Interfaces (Produces):** GET/PATCH/preview routes, ETag/error table, exact canonical preview binding, atomic confirmation/activation transaction; existing auth/error envelopes.

- [ ] **Step 1:** Write failing operator vs principal/peer/OAuth/as-peer tests, full metadata/source response fixtures, conditional read, strict JSON/body limits, missing/weak/stale If-Match and simultaneous patches where exactly one wins.
- [ ] **Step 2:** Test tampered/expired/cross-operator/cross-patch/cross-bootstrap preview tokens, repeated confirmations, extra/missing risks, hosted backend locks, verified hosted off + finite quota cost confirmation, apply-existing with unchanged policy and unsupported blob retroactivity. Assert no request body/token appears in logs or audits.
- [ ] **Step 3:** Run build-box httpapi/hub/prefs tests to observe failures; implement route registration and HTTP translation, reusing hub service validation/auth and existing audit.
- [ ] **Step 4:** Run suites and a disposable request journey: safe save → stale write rejected → dangerous preview/cancel → confirmed save → restart/re-read. Verify sanitized failure responses and exact DTO fixtures.
- [ ] **Step 5: Commit** `feat(api): expose conditional operator preferences and risk previews`.

---

### Task 5: CLI and read-only TUI

**Depends:** 3, 4.

**Files:** Modify `internal/opclient/{client.go,client_test.go}`, `internal/cli/{commands.go,cli_test.go}`, create CLI prefs command/tests; modify TUI API interface/navigation and create preferences screen/tests/goldens following merged package naming.

**Interfaces:** opclient GET/preview/PATCH carrying ETags and structured errors; CLI commands and risk flags above; TUI read-only load/refresh/help/unavailable states.

- [ ] **Step 1:** Write failing CLI profile/operator/source/duration/reset/exit-code tests, conflict with no retry, JSON token redaction, noninteractive explicit-risk acceptance and unexpected-risk refusal. Use injected HOME/config and no real profiles.
- [ ] **Step 2:** Write TUI loading/error/empty/group/help/source/unavailable goldens and hostile-text fixtures; prove navigation and refresh perform only GET.
- [ ] **Step 3:** Observe failures in build-box opclient/cli/tui suites; implement adapters and surfaces. Check merged help/keymap rather than claiming an occupied shortcut.
- [ ] **Step 4:** Run suites and `go list -deps ./cmd/vecnet` on the box; assert no store/SQLite imports. Update only intended help/goldens.
- [ ] **Step 5: Commit** `feat(clients): add operator prefs CLI and TUI inspection`.

---

### Task 6: Settings page, draft model and local appearance

**Depends:** 3 runtime core, 4; use Max's selected List/Policy design above. Consumer-specific rows stay unavailable until their adapters land; acceptance waits for all required adapters.

**Files:** Create `internal/ui/static/{settings.js,settings-model.js,appearance.js}` and matching model tests in `internal/ui/jstest/`; modify `app.js`, `index.html`, `api.js` and existing stylesheet(s) after inspecting merged filenames; add router/API/UI tests.

**Interfaces:** Existing screen lifecycle plus `#settings`; API response/error/ETag contracts; separate browser appearance object and controls.

- [ ] **Step 1:** Write failing model tests for List default/header toggle/blocked or corrupt view storage, one-store edit/save/undo/error/confirmation persistence across views, flag/env read-only in both, save-to-default Reset and response badge, continued ack pruning in archive mode, atomic dirty patch/reset/undo, source badges, archive dormant policy, server bounds/locks, 412 draft preservation/manual reconciliation, failed save, dangerous cancel/confirm and no token persistence.
- [ ] **Step 2:** Write API tests preserving If-Match/ETag and structured errors, appearance tests for invalid/corrupt/blocked storage/system theme/cross-tab enum updates, and no hub PATCH from appearance Apply.
- [ ] **Step 3:** Run `just bb build2 node --test --test-concurrency=2 internal/ui/jstest/`; observe failures and implement page/nav/model following the selected prototype. All five sections remain discoverable with accessibility and truthful per-field application timing.
- [ ] **Step 4:** Repeat affected Node suites, verify unmount cancels pending requests/listeners, keyboard focus/error announcements, dirty navigation guard and no theme flash on load. Do not leak draft or preview token into storage/URL.
- [ ] **Step 5: Commit** `feat(ui): add Settings with validated drafts and local appearance`.

---

### Task 7: Real browser/CLI journeys and screenshot receipt

**Depends:** 5, 6.

**Files:** Create `scripts/ui/prefs.mjs`; update `docs/plans/shots/oui/README.md`, generated shots index per `scripts/ui/shots-index.mjs`, and screenshot artifacts according to existing SHOTS_DIR conventions. Add private TUI prefs shot only if needed using existing tmux harness.

**Interfaces:** Automatic inclusion in `just ui-shots` glob; use `withDemo`, real disposable hub, authenticated API and actual CLI. Network holds/injected failures are deterministic and explicitly labeled.

- [ ] **Step 1:** Add assertions for nav/deep link, groups/labels/source, one atomic save, CLI reads same saved value, server worker reflects change and restart retains it. Use `hold` to capture saving; do not fake a screenshot by changing DOM labels. Inject a competing operator PATCH for 412 and test manual reconciliation.
- [ ] **Step 2:** In `scripts/ui/prefs.mjs`, capture the full Cartesian matrix: views `list|policy` × states `initial|dirty|saving|saved|error|confirm-archive|confirm-trim|confirm-files` × sizes `desk` (1440×900) / `phone` (390×844): **32 screenshots**, named `prefs-<view>-<state>-<desk|phone>.png`, listed individually in the shot README/index. Use real fixture edits and deterministic request holds/failures, not prototype query/DOM state injection. Assert edits and source badges carry across view switches, archive leaves ack pruning enabled, default-reset shows `default`, and flag/env controls stay read-only. Archive confirmation defaults to prospective-only; trim requires explicit existing-data choice, fixed preview and next-nightly copy; files confirmation shows hosted cost/finite bootstrap quota with real supported keep fixture. Also cover validation/conflict/policy-lock and 320px overflow checks in both views.
- [ ] **Step 3:** Run `just bb build2 node scripts/ui/prefs.mjs`, inspect every PNG at native size, then run `just bb build2 node scripts/ui/shots-index.mjs`. Verify focus trap/Escape cancel, phone Save/Undo, no clipped help and no console/network surprises. Record selected prototype, screenshot paths, viewport and asserted behavior in README.
- [ ] **Step 4:** Run fixture-only `just bb just ui-shots` once for whole-nav regression coverage (including shared sidebar/nav assertions). Fix failures; do not regenerate unrelated goldens without cause. No real credentials or production provider calls.
- [ ] **Step 5: Commit** `test(ui): cover preference journeys and Settings screenshots`.

---

### Task 8: Protocol, operator runbook and whole-surface acceptance

**Depends:** 7.

**Files:** Modify `docs/PROTOCOL.md`, `docs/OPERATOR.md`, `docs/DEPLOY.md`, protocol examples/schema tooling where applicable, `README.md`, `CHANGELOG.md`; add an implementation receipt with actual migration/companion commits, prototype pick and test/shot evidence.

**Interfaces:** Published key/source/range/default/reset/error/ETag contract, CLI/TUI/UI usage, live-effect timing and retention safety. Deployment runbook describes bootstrap-only locks, verified backend lifetimes/prefixes, rollback and restore boundaries.

- [ ] **Step 1:** Document keys and exact implemented flags/env names, source precedence/reset, stale-write recovery and explicit existing-data preview/confirmation. State prospective changes, immutable old blob deadlines, archive protection vs export, pin-delayed GC, eventual physical deletion and separate backup retention. Include local appearance/no device sync.
- [ ] **Step 2:** Write rollout/recovery steps: consistent external backup; migration/start with existing bootstrap defaults; verify sources; dry-run retention; operator confirmation for existing data; provision/verify R2 prefixes outside runtime API; enable allowed lifetime policy; inspect worker generation and audit. Rollback must preserve preferences/activation data and use a binary that honors deletion boundaries; an older binary blindly running GC is unsafe. Recover an invalid stored key only under a stopped-hub, backed-up maintenance procedure, not by adding a remote force bypass.
- [ ] **Step 3:** Validate protocol fixtures and run `just bb CGO_ENABLED=0 go test -p 2 ./...`, Node UI suites, direct S3 adapter tests and changed connector checks, focused race checks and the Task 7 screen receipts. Reuse already-passing broad checks unless subsequent changes justify rerunning. No test or deployment in this planning assignment.
- [ ] **Step 4:** Review against every Review Focus point, fix findings and rerun affected checks. Confirm the required retention/R2 adapters work, off really persists bytes beyond 24h in the fake clock/provider fixture, source/confirmation/audit invariants hold, CLI is SQLite-free and the prototype is Max's selection. Append one dated CHANGELOG line for the new protocol/agent-facing commands.
- [ ] **Step 5: Commit** `docs(prefs): document runtime settings and safe retention controls`. Prepare a reviewable implementation PR only when that execution is authorized; no deployment implicit in this plan.

---

--- SUMMARY ---

- A typed Go registry resolves built-in → env/flag → SQLite overrides, returns `Effective{Value,Source,Origin,Revision}` with sources `default|flag|stored`, and records atomic operator changes with revision/ETag concurrency and audit. One next-free preferences migration; no secrets or arbitrary config dump.
- Immutable snapshots and after-commit wakeups apply supported settings to workers without restart; defaults affect new records and existing explicit policies remain authoritative. Later settings are visibly read-only with reasons.
- Initially operator-only GET/PATCH/preview API, `vecnet prefs get|set|list` and read-only TUI share the contract. Settings has five groups, source badges, validation, Save/Undo, explicit dangerous-change dialogs and conflict recovery. Theme/density use separate local browser storage and controls.
- Retention keys map to the DB plan's existing mechanisms. Archive mode protects Message history. Normal Save is prospective; deleting existing rows needs a bounded preview and separate explicit confirmation, retaining every pin/cursor/replay guarantee.
- Blob expiry is enum `1h|6h|12h|1d|7d|30d|off`, default `1d`, for new grants only. Verified keep support and finite bootstrap quota permit hosted off after the shared cost confirmation; quota is not a pref. R2 owns class-first keys, six lifecycle rules, sha256 checksum and 300s PUT/600s GET.
- Shared preview/explicit confirmation guards archive off and new/shorter retention; existing-data approval is separately scoped and trimming waits for the next nightly sweep. Archive preserves messages/history while 7-day acknowledged-event pruning continues; `retention.ack_events=0` also retains events.
- Max selected List (A/Ledger) default with Policy (D) header toggle, one draft/store and guarded `vecnet.ui.settingsView` storage. Flag/env rows are read-only; a save back to inherited default shows `default`. Task 7 captures both views × eight states × desktop/phone (32 shots).
- Admin parity exclusively owns `vecnet_prefs_list/get/set` and `admin:prefs` hub read/write capability/OAuth checks. HTTP/CLI/web use the same model after parity; operator-only until then.
- Eight tasks deliver registry/persistence, runtime adapters, shared API/confirmation, clients, Settings, journeys and runbooks. API/confirmation can land before consumer adapters; full acceptance waits for both DB and R2. See [build order](2026-11-05-build-order.md) for migration inventory, dependency gates and serialized files.
