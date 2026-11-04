# Hub Notices and Verified Relocation Implementation Plan

> **For agentic workers:** executed by `docs/plans/vecnet-core-build.workflow.mjs` with `docs/plans/hub-notices.workflow-args.json`. Each task runs in its own worktree from the dependency map and sees only its own section plus the Global Constraints. Steps use checkbox (`- [ ]`) syntax. A `---` ends every task.

**Goal:** Connected agents hear about planned maintenance, restarts and moves before they happen. A restart reads as "hub restarting", not a dead stream. When a hub moves (new domain or new host), clients follow on their own, but only to a hub that proves it holds the same identity key. Self-hosters get the same tools.

**Architecture:**
- **Identity key (Tasks 1, 3).** An Ed25519 key whose 32-byte seed sits in `meta.hub_identity_seed`. Migration 0019 generates it in SQL, as 0001 did for `hub_key`, so every existing and new hub has one. `GET /v1/hub/identity[?challenge=]` is public. It serves the public key and the hub's `public_url`, and signs a client challenge bound to that URL. Enrol returns `hub_pubkey`. The key is kept separate from `meta.hub_key`, for the reasons in Decisions.
- **Notices (Task 4).** One table, `hub_notices`, with at most one active notice per kind (`maintenance`, `restart`, `moving`). The identity key signs every notice. A post or a clear appends a `hub_notice` **Event** (not a Message) to every enabled peer's log, as `directory_updated` does. `GET /v1/hub/notices` lists the active ones.
- **Restart signal (Task 2).** On SIGTERM the hub announces a drain. Every open `/v1/events` SSE stream and `/v1/admin/activity` stream gets one final frame, `event: hub_restarting` with `data: {"retry_after_ms":N}` and no `id:`. A parked long-poll answers with a `hub_restarting` field. Only then does `http.Server.Shutdown` run.
- **Relocation (Task 5).** `vecnet-hub --relocated-to URL` runs no workers. It answers only `/healthz`, `/v1/hub/identity`, `/v1/hub/notices` and a `/ui` "moved" page. Everything else, reads included, gets 410 `hub_moved` with the signed moving notice.
- **Client (Tasks 6–7).** The CLI pins the hub key. New profiles store `hub_pubkey`. Existing profiles are pinned trust-on-first-use (TOFU) in a sidecar, so `config.toml` is never rewritten for a pin. A signed moving notice is followed only when all of these hold:
  - its signature verifies against the pin;
  - the target hub's `public_url` equals `new_url`;
  - the target signs a fresh challenge with the pinned key.

  The profile edit is a text-level TOML edit that keeps comments and unknown keys.
- **Surfaces.**
  - `watch --for-monitor` lines (Task 7).
  - The TUI banner (Task 8).
  - The web banner, with screenshots (Task 9).
  - Hook notice lines (Task 11) and MCP restart notifications plus relay follow (Task 12). These two come after mcp-first merges.

**Tech Stack:** Go 1.26 (`CGO_ENABLED=0`), stdlib `crypto/ed25519`, no new modules. Vanilla ES modules for the web UI. Node screen scripts through Playwright and the private tmux harness.

**Spec:**
- `.scratch/vecnet/issues/41-hub-notices-and-relocation.md`, and Max, 2026-11-05: "warn connected agents if it's going down … rebooting, or if dns needs moving … users selfhosting later might want to also".
- PROTOCOL §2.3 (SSE), §2.12 (enrol, admin), §13.1, §13.7, §13.9, §20 (deploy hardening). This plan adds **§21**.
- `docs/DEPLOY.md` (`meta.hub_key`, backups, `kill_timeout = 15`, `ShutdownGrace = 10s`).

**Starts from:** `master` at `0461666` or later. **Tasks 11–12 start only after `mcp-first` is merged to master** (see Rebase points).

**Preflight (coordinator):**
- Add `.scratch/vecnet/build-hub-notices/` to `.gitignore`.
- Run Tasks 1–10 (`endTask: 10`). After mcp-first merges, run Tasks 11–12 as a second slice (`startTask: 11`, `endTask: 12`). Preflight check 4 already accepts a partly merged plan.

**Rebase points:**
- `internal/cli/commands.go` (`usageText`, `dispatch`): mcp-first Tasks 7–8 add `mcp`. This plan's Tasks 6–7 add `notice` and `profile`. Both only add lines; keep both.
- `internal/cli/hook.go`, `internal/cli/guide.md` and the guide goldens: mcp-first Task 8 owns them. This plan touches them **only in Task 11**, after mcp-first merges.
- `internal/mcpserver/**`: mcp-first Tasks 1–6 own it. This plan touches it **only in Task 12**, after mcp-first merges. The `vecnet mcp` relay exists only after mcp-first Task 7.
- `docs/PROTOCOL.md`, `CHANGELOG.md`: Task 10, plus Task 11 (§13.9) and Task 12 (§4). mcp-first Task 10 also edits §4, §13.4, §13.7 and §13.9, and the launcher slice edits §13.8 and §18. Merge by section; add text, never reorder theirs.
- `connectors/**`: never touched. The launcher daemon and hub-client are building on branch `launcher`. Their handling of `hub_restarting` is a follow-up (§13.1 makes it a SHOULD).

---

## Dependency map

```json
{"1":[],"2":[],"3":[1,2],"4":[3],"5":[4],"6":[3],"7":[4,5,6],"8":[2,4],"9":[2,4,8],"10":[1,2,3,4,5,6,7,8,9],"11":[7,10],"12":[2,7,11]}
```

**Build settings:**
- `taskPrefix: "hn"`, worktree `.worktrees/hub-notices`, branch `hub-notices`, status files in `.scratch/vecnet/build-hub-notices/`.
- `endTask: 10` for the first slice; Tasks 11–12 are the second slice.
- **Routes:** Sol for Tasks 1–7 and 12. Opus for Tasks 8–9 (UI), 10 (docs) and 11 (agent-facing hook and guide copy).
- **Shared files:**

  | File | Order |
  |---|---|
  | `internal/httpapi/server.go`, `middleware.go`, `ratelimit.go` | 2 → 3 → 4 → 5 |
  | `cmd/vecnet-hub/main.go`, `main_test.go` | 2 → 5 |
  | `internal/testhub/testhub.go` | 2 → 3 → 5 |
  | `scripts/ui/lib.mjs`, `scripts/ui/tmux.mjs` | Task 2 only |
  | `internal/cli/commands.go`, `config.go` | 6 → 7 |
  | `internal/cli/watch.go`, `client.go` | Task 7 only |
  | `docs/plans/shots/oui/README.md` | 8 → 9 |
  | `docs/PROTOCOL.md`, `CHANGELOG.md` | 10 → 11 → 12 |

## Decisions

1. **Identity key separate from `meta.hub_key`.**
   - `hub_key` is a symmetric secret. It keys blob URL tokens and seals route and source secrets (`hub/seal.go`), so publishing anything derived from it publicly, or rotating it, would break those.
   - An Ed25519 seed is just 32 random bytes, so migration 0019 can generate it with `randomblob(32)`, like 0001 did for `hub_key`.
   - It lives in the same `meta` table, so every backup of `hub.db` (Fly volume snapshots, `just fly-backup`) carries it. That is exactly what makes a database-copy move transparent.
   - A copy of the database **is** the hub's identity. DEPLOY.md says so, and `.dockerignore` already keeps databases out of builds.
2. **Rotation is offline and cross-signed.**
   - `vecnet-hub identity rotate --yes` runs with the hub stopped. It writes a new seed, plus `meta.hub_identity_prev` = `{pubkey, rotated_at, endorsement}`, where the endorsement is the old key's signature over the new key.
   - A client pinned to the previous key accepts the new one only through that endorsement, and only one generation back.
   - A stolen key is not something rotation can fix (the thief can endorse too). The answer to theft is re-pinning by fingerprint (`vecnet profile pin --accept`) after an out-of-band announcement.
3. **Notices are Events, not Message Deliveries** (a deviation from the issue's "system delivery").
   - An Event cannot be replied to, cannot loop and spends no thread budget (§1.2).
   - It has no 64-participant thread cap, needs no `messages.kind` table rebuild, and no peer can author it.
   - The cost is that push-only receivers (webhook routes, Grok Bot) and dots do not see notices. Open question 4.
4. **TOFU without touching Max's config.**
   - `enrol` writes `hub_pubkey` into the new profile; it already writes the file.
   - An existing profile is pinned on its first non-hook command after upgrade. The CLI makes one unauthenticated `GET /v1/hub/identity` with a challenge and records the pin in `<config dir>/state/pins.json`, keyed by profile and the normalised hub URL.
   - `config.toml` is rewritten only by an explicit `profile pin`/`repoint` or a verified follow. Even then it is a line-level edit, which a re-decode checks before the atomic replace. A `config.toml.bak` (0600) is written first.
   - The hook never TOFUs, follows or rewrites anything: it keeps its 1.5 s budget.
5. **When the CLI checks the key.** At TOFU; on every `watch` (re)connect, before the bearer-carrying events request; and when following. One-shot commands are not checked every time, because TLS authenticates the URL and the pin exists to make moves safe (open question 6).
6. **When the CLI follows a move.** Only on:
   - a 410 `hub_moved` (the old hub is in relocated mode); or
   - in `watch`, an active signed moving notice whose `at` has passed.

   A moving notice without `at` is information until the 410. `VECNET_NO_FOLLOW=1` turns auto-follow off; the client still warns and prints `vecnet --net P profile follow`.
   - Peer text is never parsed for moves.
   - The follow rewrites every profile that has the same normalised hub URL and the same effective pin (Max runs many agents on one hub). It never rewrites any other profile.
7. **`--relocated-to` refuses reads as well as sends.** The relocated hub's database is a frozen copy: reads would be stale, and acks and receipts would be lost. One unambiguous 410 drives every client to follow. It runs no push, Events or ticker workers, so nothing is delivered twice.
8. **The restart frame is a transport frame, not an Event.**
   - It has no `id:`, so `Last-Event-ID` is unchanged, and no `retry:`.
   - Old CLIs print it as an empty-cursor line and carry on.
   - On `/mcp` streams it becomes a `notifications/message` (Task 12), because MCP clients only dispatch JSON-RPC.
9. **Command names follow house style.** It is `vecnet notice post|list|clear`, not `vecnet admin notice`, because every other operator command is top-level and `notice` reads clearly beside `notify`. The other new commands are `vecnet profile show|pin|repoint|follow`.
10. **A new hub clears notices about itself.** Restart notices clear at boot. A moving notice whose `new_url` is the hub's own `public_url` also clears at boot: the database copied to the new host carries the moving notice that pointed at it.

## Global Constraints

- **Build box.** Every build and test runs through `just bb` (docs/BUILDBOX.md):
  - Go: `CGO_ENABLED=0 just bb go test -p 2 ./internal/<pkg>/ -run '…'`.
  - Node: `just bb build2 node --test --test-concurrency=2 <files>`.
  - Screen scripts: `just bb build2 node scripts/ui/<x>.mjs`.
  - Goldens: `UPDATE_GOLDEN=1 CGO_ENABLED=0 just bb go test -p 2 ./internal/tui/ -run '<Test>'`.
  - Only gofmt, git, `just isa-check` and `go list` run locally.
- **Never touch the real config.** Tests set `VECNET_CONFIG`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME` and `HOME` to `t.TempDir()` through the app's injected `getenv`. No test or script reads or writes `~/.config/vecnet/`, `~/.cache/vecnet/`, `~/.claude*` or `~/.codex/`.
- **Never touch** `connectors/**`, the dev hub on 45752, the preview on 49575, `.data/` or `.demo-data/`. Tmux only through the private `-L oui-<pid>` server. Before Task 12, never touch `internal/mcpserver/**`; before Task 11, never touch `internal/cli/hook*.go` or `guide.md`.
- **Dependency rules.** `internal/hubid` imports only the stdlib and `internal/envelope`, never `internal/store`. `go list -deps ./cmd/vecnet | grep -c modernc.org/sqlite` stays 0.
- **Canonical signed bytes** (Task 1 fixes them; every task uses them). Each record ends with `\n`. URLs are `hubid.NormalizeURL` output: lower-case scheme and host, no default port, no trailing slash. `sig`, `challenge_sig` and `endorsement` are unpadded base64url Ed25519 signatures.
  - **Notice:** `vecnet-hub-notice-v1\nnotice_id=<id>\nkind=<k>\nhub=<url>\nissued_at=<ts>\nat=<ts|>\nexpires_at=<ts>\nnew_url=<url|>\nmessage_sha256=<hex>\n`.
  - **Challenge:** `vecnet-hub-challenge-v1\n<public_url>\n<challenge>\n`.
  - **Rotation:** `vecnet-hub-rotation-v1\nold=<pub>\nnew=<pub>\nrotated_at=<ts>\n`.
  - **Public key:** `ed25519:<base64url, 43 chars>`.
  - **Fingerprint:** `SHA256:<base64 std, unpadded, of sha256(pub)>`.
- **Untrusted text.** Notice `message` is operator text but is rendered like peer text: `Clean` in the TUI, `textContent` in the web UI, `flatten`/`unbidi` and the rune cap in the CLI, `inert` in the hook. No client acts on any text. Only the signed structured fields (`kind`, `new_url`, `at`) drive behaviour.
- **Wire stability.** Every Event type, frame and field already shipped stays byte-identical. A test marked **(guard)** passes before its task and must keep passing; every other listed test must fail first.
- **Numbering.** Migration **0019**, PROTOCOL **§21**, id prefix **`ntc_`**. Reason codes:
  - `hub_moved` (410, hub);
  - `relocation_unverified` (client outcome, exit 1);
  - `hub_identity_changed` (client outcome, exit 1).

## Review Focus

1. **No redirect to an unverified hub.** Pinned by:
   - Task 7: `TestFollowRefusesBadSignature`, `TestFollowRefusesKeyMismatch`, `TestFollowRefusesPublicURLMismatch`, `TestFollowIgnoresPeerText`, `TestFollowRefusesReplay`.
   - Task 6: `TestVerifyHubChecks`, `TestProfileRepointRefusesWithoutForce`.
2. **The identity key is stable, separate and backed up with the database.** Pinned by `TestMigration0019SeedsIdentity` (Task 1), and by `TestHubIdentityStableAcrossRestart` and `TestRotateIdentityEndorses` (Task 3).
3. **Max's config survives.** Pinned by Task 6's `TestTOFUNeverWritesConfig`, `TestEditProfilePreservesCommentsAndUnknownKeys` and `TestEditProfileRefusesUnsafe`, and by Task 7's `TestFollowAllProfilesSameHub`.
4. **Relocated hub semantics.** Pinned by Task 5's `TestRelocatedAnswers410`, `TestRelocatedRunsNoWorkers` and `TestRelocatedServesIdentityAndNotices`.
5. **A restart is announced and not counted as failure.** Pinned by:
   - Task 2: `TestSSERestartFrame`, `TestSIGTERMSendsHubRestarting`.
   - Task 7: `TestWatchPrintsHubRestarting`.
   - Task 8: `TestTailReportsRestarting`.
6. **Notices are operator-only, signed, fanned out once per enabled peer, and inert on every surface.** Pinned by:
   - Task 4: `TestPostNoticeOperatorOnly`, `TestNoticeFanOutEveryEnabledPeer`.
   - Task 7: `TestMonitorLineHubNotice`.
   - Task 8: `TestNoticeBannerHostile`.
   - Task 9: `notice.test.mjs`.
7. **Visual.** The banners look like the same product, at desktop and phone width, in every state (Tasks 8–9 shots).

---

### Task 1: Shared contract: `internal/hubid`, envelope types, `ntc_` ids, migration 0019

**Depends:** none

**Files:**
- Create `internal/hubid/hubid.go`, `hubid_test.go`.
- Create `internal/store/migrations/0019_hub_identity_notices.sql`.
- Modify `internal/store/store_test.go`.
- Modify `internal/envelope/types.go`, `schema.json` (and the identical `docs/protocol/envelope.schema.json`), `validate_test.go`.
- Create `docs/protocol/examples/11-hub-notice-event.json`.
- Modify `internal/ids/ids.go`, `ids_test.go`.

**Interfaces (Produces):**

```go
// internal/ids
const Ntc Prefix = "ntc"

// internal/envelope
type HubNotice struct {
	NoticeID   string `json:"notice_id"`
	Kind       string `json:"kind"`             // maintenance | restart | moving
	Message    string `json:"message"`
	Hub        string `json:"hub"`              // issuing hub's public_url
	IssuedAt   string `json:"issued_at"`
	At         string `json:"at,omitempty"`
	ExpiresAt  string `json:"expires_at"`
	NewURL     string `json:"new_url,omitempty"` // moving only
	Sig        string `json:"sig"`
	ClearedAt  string `json:"cleared_at,omitempty"`  // not signed
	ClearedHow string `json:"cleared_how,omitempty"` // operator | expired | replaced | boot; not signed
}
type HubNoticeEvent struct {
	Change string    `json:"change"` // posted | cleared
	Notice HubNotice `json:"notice"`
}
type HubKeyPrevious struct {
	Pubkey      string `json:"pubkey"`
	RotatedAt   string `json:"rotated_at"`
	Endorsement string `json:"endorsement"`
}
type HubIdentity struct {
	Pubkey       string          `json:"pubkey"`
	Fingerprint  string          `json:"fingerprint"`
	PublicURL    string          `json:"public_url"`
	KeyCreatedAt string          `json:"key_created_at"`
	Previous     *HubKeyPrevious `json:"previous,omitempty"`
	ChallengeSig string          `json:"challenge_sig,omitempty"`
}

// internal/hubid (stdlib + envelope only)
type Key struct{ /* ed25519.PrivateKey */ }
func KeyFromSeedHex(seed string) (Key, error) // 64 hex chars
func (k Key) Pubkey() string                  // "ed25519:<b64url>"
func DecodePubkey(s string) (ed25519.PublicKey, error)
func Fingerprint(pubkey string) (string, error)
func NormalizeURL(raw string) (string, error) // http/https only; no userinfo, query or fragment
func NoticeBytes(n envelope.HubNotice) ([]byte, error)
func (k Key) SignNotice(n *envelope.HubNotice) error
func VerifyNotice(pubkey string, n envelope.HubNotice) error
func (k Key) SignChallenge(publicURL, challenge string) (string, error)
func VerifyChallenge(pubkey, publicURL, challenge, sig string) error
func (k Key) Endorse(newPubkey, rotatedAt string) string
func VerifyEndorsement(prev envelope.HubKeyPrevious, current string) error
func ValidChallenge(c string) bool // unpadded base64url of 16–64 bytes
```

**Schema and migration:**
- Schema: `$defs` `hubNotice`, `hubNoticeEvent` and `hubIdentity` (`additionalProperties: false`); the `event.type` enum gains `hub_notice`; the id pattern gains `ntc_`.
- Migration 0019:
  - `INSERT INTO meta` rows `hub_identity_seed` = `lower(hex(randomblob(32)))` and `hub_identity_created_at` = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`.
  - A `hub_notices` table, `STRICT`, with columns `notice_id` PK, `kind` CHECK IN (…), `message`, `hub`, `issued_at`, `at` NULL, `expires_at`, `new_url` NULL, `sig`, `created_by`, `cleared_at` NULL, `cleared_how` NULL.
  - Index `hub_notices_active(cleared_at, kind)`.
  - A header comment saying why the seed is separate from `hub_key` (Decision 1).

- [ ] **Step 1: Write the failing tests**

```go
func TestNoticeBytesGolden(t *testing.T)           // a fixed moving notice → exactly the canonical bytes in Global Constraints (literal string), message_sha256 of "Moving tonight"
func TestNoticeSignVerifyRoundTrip(t *testing.T)   // sign → verify ok; changing kind, hub, issued_at, at, expires_at, new_url or message → error; another key → error; ClearedAt/ClearedHow changes still verify
func TestChallengeBindsPublicURL(t *testing.T)     // sig for (https://a.example, c) fails for https://b.example and for another challenge; trailing slash and upper-case host normalise equal
func TestEndorsementVerifies(t *testing.T)         // old.Endorse(new) verifies for current=new; current=other or swapped keys → error
func TestPubkeyAndFingerprintFormat(t *testing.T)  // Pubkey has prefix "ed25519:" and 51 chars; DecodePubkey round-trips; Fingerprint has prefix "SHA256:" and 50 chars
func TestNormalizeURL(t *testing.T)                // table: HTTPS://Hub.Example:443/ → https://hub.example; http://x:80 → http://x; userinfo, query, fragment, ftp: → error
func TestValidChallenge(t *testing.T)              // 15 or 65 bytes, padding, '+' → false; 16 and 64 bytes → true
func TestHubNoticeEventSchema(t *testing.T)        // envelope: example 11 validates; an Event with type hub_notice validates; a notice with an unknown field fails
func TestMigration0019SeedsIdentity(t *testing.T)  // store: migrate fresh → 64-hex seed and created_at; two fresh DBs differ; seed != hub_key; reopening keeps it; hub_notices accepts kind moving and rejects kind "other"
func TestNoticeIDPrefix(t *testing.T)              // ids.New(ids.Ntc) starts "ntc_" and validates
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/hubid/ ./internal/envelope/ ./internal/store/ ./internal/ids/ -run 'Notice|Challenge|Endorsement|Pubkey|NormalizeURL|ValidChallenge|Migration0019'` fails.
- [ ] **Step 3:** Implement. Copy the schema byte-identically into both files (`just bb just validate-protocol`).
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/hubid/ ./internal/envelope/ ./internal/store/ ./internal/ids/ ./internal/hub/` passes; `just bb just validate-protocol` passes.
- [ ] **Step 5: Commit** `feat(hubid): Ed25519 hub identity, signed notice bytes, migration 0019`

---

### Task 2: Graceful restart signal (`hub_restarting`)

**Depends:** none

**Files:**
- Create `internal/httpapi/drain.go`, `drain_test.go`.
- Modify `internal/httpapi/sse.go`, `events.go`, `activity.go`, `server.go` (`Deps.Drain` only), `events_test.go`, `activity_test.go`.
- Modify `cmd/vecnet-hub/main.go`, `main_test.go`.
- Modify `internal/testhub/testhub.go` (wire `Drain`), `internal/opseed/demo/main.go`, `main_test.go`, `scripts/ui/lib.mjs` and `scripts/ui/tmux.mjs` (expose `restart`).

**Interfaces (Produces):**

```go
// internal/httpapi/drain.go
const DefaultRestartRetry = 5 * time.Second
const drainFlush = time.Second
type Drain struct{ /* mu, generation chan, retry, WaitGroup */ }
func NewDrain() *Drain
func (d *Drain) Announce(retryAfter time.Duration) // closes the current generation once; nil-safe
func (d *Drain) C() <-chan struct{}                // nil for a nil *Drain (never fires)
func (d *Drain) RetryAfter() time.Duration
func (d *Drain) Join() (done func())               // a stream registers; done after its final frame
func (d *Drain) Wait(timeout time.Duration) bool   // true when every joined stream finished
func (d *Drain) Reset()                            // new generation (demo restart only)
func restartFrame(retry time.Duration) []byte      // "event: hub_restarting\ndata: {\"retry_after_ms\":N}\n\n"
func (c *sseConn) finish(frame []byte, wait time.Duration) // queue, wait until the writer has flushed it (or wait), then close

// Deps
Drain *Drain // nil: no announcement

// cmd/vecnet-hub: serve takes a struct (Task 5 adds RelocatedTo)
type serveConfig struct {
	Listen, DataDir, PublicURL, DevUIDir string
	AllowPrivateEgress, TrustFly         bool
	RestartRetry                         time.Duration // --restart-retry-ms / VECNET_RESTART_RETRY_MS, default 5000, 0..60000
}
func serve(ctx context.Context, cfg serveConfig, stdout io.Writer) error
```

**Behaviour:**
- `streamSSE` and `opActivity` select on `s.Drain.C()`. On a drain they call `conn.finish(restartFrame(s.Drain.RetryAfter()), drainFlush)` and return.
- A parked long-poll answers 200 `{"events":[],"next_cursor":<unchanged>,"hub_restarting":{"retry_after_ms":N}}`.
- `serve` handles `ctx.Done()` in order:
  1. `Drain.Announce(cfg.RestartRetry)`;
  2. `Drain.Wait(drainFlush + 250ms)`;
  3. the existing `Shutdown`/`closeStop` path.

  All of it stays inside `ShutdownGrace` (10 s, below Fly's `kill_timeout = 15`).
- The demo adds `POST /__demo/restart {retry_after_ms}`, which runs `Announce` → `Wait(2s)` → `Reset` and returns `{streams: n}`. `lib.mjs` `bootDemo` adds `restart: ms => control('restart', {retry_after_ms: ms})`, and `tmux.mjs` passes it through.

- [ ] **Step 1: Write the failing tests**

```go
func TestSSERestartFrame(t *testing.T)          // httpapi: open /v1/events SSE after one event; Announce(1500ms) → next frame is exactly "event: hub_restarting\ndata: {\"retry_after_ms\":1500}\n\n" (no id:, no retry:), then EOF; last id seen equals the event's cursor
func TestActivityRestartFrame(t *testing.T)     // same frame on /v1/admin/activity, then EOF
func TestLongPollRestartField(t *testing.T)     // a parked wait=25 long-poll returns within 1s of Announce with hub_restarting.retry_after_ms 1500 and next_cursor unchanged
func TestDrainResetServesAgain(t *testing.T)    // after Announce, Wait, Reset: a new SSE stream opens and receives a new delivery
func TestDrainNilIsInert(t *testing.T)          // (guard) Deps.Drain nil: streams behave as before; existing stream tests pass unchanged
func TestSIGTERMSendsHubRestarting(t *testing.T) // cmd/vecnet-hub: run() on 127.0.0.1:0 in a temp data dir, open SSE with a peer secret, cancel ctx (what NotifyContext does on SIGTERM) → client reads hub_restarting before EOF; run returns nil within ShutdownGrace
func TestRestartRetryFlag(t *testing.T)         // --restart-retry-ms 70000 → usage error; VECNET_RESTART_RETRY_MS=2000 → frame says 2000
func TestDemoRestartControl(t *testing.T)       // opseed/demo: POST /__demo/restart ends an open activity stream with hub_restarting; a new stream then gets "event: ready"
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/httpapi/ ./cmd/vecnet-hub/ ./internal/opseed/... -run 'Restart|Drain'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/httpapi/ ./cmd/vecnet-hub/ ./internal/opseed/... ./internal/testhub/` passes; `just bb build2 node --test --test-concurrency=2 scripts/ui/lib.test.mjs` passes.
- [ ] **Step 5: Commit** `feat(hub): announce hub_restarting on SSE, activity and long-poll before shutdown`

---

### Task 3: Hub identity endpoint, enrol pin, rotation, `testhub.CloneDB`

**Depends:** 1, 2

**Files:**
- Create `internal/hub/identity.go`, `identity_test.go`.
- Modify `internal/hub/hub.go` (load the key in `New`).
- Create `internal/httpapi/identity.go`, `identity_test.go`.
- Modify `internal/httpapi/server.go` (route), `middleware.go` (`isPublic`), `ratelimit.go` (cost), `enrol.go`, `hardening_test.go`.
- Modify `internal/testhub/testhub.go`.

**Interfaces:**
- **Consumes:** `hubid.KeyFromSeedHex`, `SignChallenge`, `Endorse`, `ValidChallenge`; `envelope.HubIdentity`.
- **Produces:**

```go
// internal/hub
func (h *Hub) IdentityKey() hubid.Key
func (h *Hub) IdentityDoc(challenge string) (envelope.HubIdentity, error) // bad challenge → E(422,"invalid_value") details.field "challenge"
func RotateIdentity(ctx context.Context, db *store.DB, now time.Time) (oldFP, newFP string, err error) // offline; writes seed, created_at, hub_identity_prev; never touches hub_key

// internal/testhub
func (e *Env) CloneDB(t *testing.T) *Env // a second hub over a VACUUM INTO copy of e's database: same identity, peers and credentials; own URL and PublicURL
```

**HTTP:**
- `GET /v1/hub/identity[?challenge=C]` → 200 `HubIdentity`.
  - Public (`isPublic`).
  - Free under §20.2 without `challenge`; one token with it.
  - `Cache-Control: no-store`.
- `POST /v1/enrol` → 200 also carries `"hub_pubkey"`.

- [ ] **Step 1: Write the failing tests**

```go
func TestHubIdentityStableAcrossRestart(t *testing.T) // hub: two hub.New over the same store → same Pubkey; a fresh store → different
func TestGetHubIdentityPublic(t *testing.T)           // no bearer → 200; pubkey decodes; fingerprint == hubid.Fingerprint(pubkey); public_url == Cfg.PublicURL; no challenge_sig, no previous
func TestGetHubIdentityChallenge(t *testing.T)        // ?challenge=<32 random bytes> → hubid.VerifyChallenge(pubkey, public_url, c, challenge_sig) == nil
func TestGetHubIdentityBadChallenge(t *testing.T)     // 8-byte, padded and non-base64url challenges → 422 invalid_value, details.field "challenge"
func TestIdentityChallengeCostsToken(t *testing.T)    // hardening: 31 challenge requests from one client → the 31st is 429 client_rate_limited; 100 plain GETs → all 200
func TestEnrolReturnsHubPubkey(t *testing.T)          // enrol body hub_pubkey == GET /v1/hub/identity pubkey
func TestRotateIdentityEndorses(t *testing.T)         // RotateIdentity → new pubkey differs; previous.pubkey == old; hubid.VerifyEndorsement(previous, new) == nil; meta.hub_key unchanged and a sealed route key still unseals
func TestCloneDBSameIdentity(t *testing.T)            // testhub: clone serves the same pubkey at a different public_url; a peer secret works on both
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/hub/ ./internal/httpapi/ ./internal/testhub/ -run 'Identity|EnrolReturnsHubPubkey|CloneDB'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/hub/ ./internal/httpapi/ ./internal/testhub/ ./cmd/vecnet-hub/` passes.
- [ ] **Step 5: Commit** `feat(hub): GET /v1/hub/identity with challenge, hub_pubkey on enrol, offline rotation`

---

### Task 4: Notices: store, admin API, fan-out, expiry, boot clear

**Depends:** 3

**Files:**
- Create `internal/hub/notices.go`, `notices_test.go`.
- Modify `internal/hub/tickers.go`.
- Create `internal/httpapi/notices.go`, `notices_test.go`.
- Modify `internal/httpapi/admin.go` (`adminRoutes`), `server.go` (`routes`).
- Modify `internal/opapi/tags.go`, `tags_test.go`.

**Interfaces:**
- **Consumes:** `Hub.IdentityKey`, `hubid.NormalizeURL`, `envelope.HubNotice`/`HubNoticeEvent`, `ids.Ntc`.
- **Produces:**

```go
// internal/hub
const MaxNoticeMessage = 1024 // bytes, UTF-8
type NoticeRequest struct{ Kind, Message, At, ExpiresAt, NewURL string }
func (h *Hub) PostNotice(ctx context.Context, c auth.Caller, r NoticeRequest) (envelope.HubNotice, error)
func (h *Hub) ClearNotice(ctx context.Context, c auth.Caller, id string) error
func (h *Hub) ActiveNotices(ctx context.Context) ([]envelope.HubNotice, error)          // issued_at order
func (h *Hub) ExpireNotices(ctx context.Context) (int, error)                          // RunTickers calls it
func (h *Hub) ClearOnBoot(ctx context.Context) (int, error)                            // restart notices; moving notices whose new_url == NormalizeURL(Cfg.PublicURL)
func (h *Hub) EnsureMovingNotice(ctx context.Context, newURL string) (envelope.HubNotice, error) // relocated mode: the active moving notice for newURL, else a new one (actor "hub", at = now)

// internal/opapi
// Tags(hub_notice event) == []string{"notices"}
```

**Rules:**
- **Operator only:** any other caller gets 403 `forbidden`.
- **Validation** (422 `invalid_value`, `details.field`):
  - `kind` is one of the three;
  - `message` is 1–`MaxNoticeMessage` bytes;
  - `moving` requires a `new_url` that `NormalizeURL` accepts and that differs from the hub's own URL; any other kind forbids `new_url`;
  - `at` and `expires_at` parse as `envelope` times, and `expires_at` is in the future.
- **Defaults.** `expires_at` defaults to 24 h after `max(now, at)`, or 30 days for `moving`. `hub` is `NormalizeURL(Cfg.PublicURL)`. `sig` comes from `IdentityKey().SignNotice`.
- **One transaction per post:**
  - clear any active notice of the same kind (`replaced`);
  - insert the new one;
  - append one `hub_notice` Event per enabled peer (the `DirectoryUpdated` owner query) for each change;
  - audit `notice_post` `{notice_id, kind}` or `notice_clear` `{notice_id, how}`.

  After commit: `Log.Notify` and `kick`.
- **HTTP:**
  - `POST /v1/admin/notices` → 201 `{"notice": HubNotice}`.
  - `POST /v1/admin/notices/{id}/clear` → 204 (idempotent); an unknown id is 404 `not_found`.
  - `GET /v1/hub/notices` (any credential) → 200 `{"notices": […]}`.

- [ ] **Step 1: Write the failing tests**

```go
func TestPostNoticeOperatorOnly(t *testing.T)       // peer and principal → 403 forbidden; operator → 201, notice_id has ntc_, hubid.VerifyNotice(identity pubkey, notice) == nil
func TestPostNoticeValidation(t *testing.T)         // table: unknown kind, "" and 1025-byte message, moving without new_url, restart with new_url, new_url with userinfo or query, new_url == own URL, expires_at in the past, at "tomorrow" → 422 invalid_value with the named details.field
func TestNoticeFanOutEveryEnabledPeer(t *testing.T) // 3 enabled peers + 1 disabled → each enabled log has one hub_notice {change:"posted"}; the disabled one none; a principal stream over two peers sees two copies with the same notice_id
func TestNoticeReplacesSameKind(t *testing.T)       // second maintenance → first gets cleared_how "replaced" (cleared event fanned out); ActiveNotices has one maintenance; a moving notice is unaffected
func TestClearNotice(t *testing.T)                  // 204 + cleared event; again 204 with no new event; unknown id 404 not_found
func TestExpireNotices(t *testing.T)                // FakeClock past expires_at + tick → cleared_how "expired", one cleared event per peer
func TestClearOnBoot(t *testing.T)                  // restart and moving-to-own-URL cleared ("boot"); maintenance and moving-elsewhere kept
func TestEnsureMovingNotice(t *testing.T)           // none → new signed moving notice, at == now, audit actor "hub"; second call with the same URL returns the same notice_id
func TestGetHubNoticesAnyCredential(t *testing.T)   // peer, principal and operator → 200 active list; no bearer → 401
func TestNoticeTags(t *testing.T)                   // opapi.Tags(hub_notice) == ["notices"]; malformed data → []
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/hub/ ./internal/httpapi/ ./internal/opapi/ -run 'Notice|ClearOnBoot|EnsureMoving'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/hub/ ./internal/httpapi/ ./internal/opapi/ ./internal/opview/` passes.
- [ ] **Step 5: Commit** `feat(hub): signed operator notices with hub_notice events and GET /v1/hub/notices`

---

### Task 5: Relocated mode, boot clear and `vecnet-hub identity`

**Depends:** 4

**Files:**
- Create `internal/httpapi/relocated.go`, `relocated_test.go`.
- Modify `cmd/vecnet-hub/main.go`, `main_test.go`.
- Modify `internal/testhub/testhub.go` (`Relocate`).

**Interfaces:**
- **Consumes:** `Hub.EnsureMovingNotice`, `Hub.ClearOnBoot`, `hub.RotateIdentity`, `Hub.IdentityDoc`.
- **Produces:**

```go
// internal/httpapi
func Relocated(d Deps, n envelope.HubNotice) http.Handler

// internal/testhub
func (e *Env) Relocate(t *testing.T, newURL string) envelope.HubNotice // swaps e's handler for Relocated

// cmd/vecnet-hub
// serveConfig.RelocatedTo string  (--relocated-to / VECNET_RELOCATED_TO)
// vecnet-hub identity show   [--data-dir D] → "pubkey …\nfingerprint …\ncreated_at …\n" (+ "previous …" when set)
// vecnet-hub identity rotate [--data-dir D] --yes → "old <fp>\nnew <fp>\n"; without --yes: usage error, nothing written
```

**`Relocated` routes:**
- `GET /healthz` is unchanged.
- `GET /v1/hub/identity` and `GET /v1/hub/notices` use the normal middleware: limiter, bearer resolution and HSTS.
- `GET /ui`, `/ui/*` → 410 HTML that says the hub moved, with the escaped `new_url` as a link.
- Everything else, including `/mcp`, `/v1/events` and `/oauth/*`, → 410 `{"error":{"code":"hub_moved","message":"this hub moved to <new_url>","details":{"new_url":…,"notice":HubNotice}}}`.

**`serve`:**
- With `RelocatedTo`:
  - validate it with `hubid.NormalizeURL` and require it to differ from `PublicURL`;
  - run `EnsureMovingNotice`;
  - serve `Relocated`;
  - **do not** start `stack.Run`;
  - print `vecnet-hub relocated: answering hub_moved → <url>`.
- Without it: run `ClearOnBoot` before listening.

- [ ] **Step 1: Write the failing tests**

```go
func TestRelocatedAnswers410(t *testing.T)               // POST /v1/messages, GET /v1/events (SSE and JSON), POST /v1/ack, GET /v1/threads, POST /mcp, GET /oauth/authorize → 410 hub_moved; details.new_url; details.notice verifies with the hub's pubkey
func TestRelocatedServesIdentityAndNotices(t *testing.T) // /healthz 200; /v1/hub/identity?challenge= 200 and verifies; /v1/hub/notices with a peer bearer lists the moving notice; without one 401
func TestRelocatedUIPage(t *testing.T)                    // GET /ui/ → 410 text/html with <a href="<new_url>">; no script tag
func TestRelocatedRunsNoWorkers(t *testing.T)             // cmd: a due push delivery to a testhub.Callback; run --relocated-to for 3 tick intervals → Callback.Requests() == 0
func TestRelocatedReusesActiveNotice(t *testing.T)        // an operator-posted moving notice for the same URL is served unchanged (same notice_id, at)
func TestRelocatedRefusesOwnURL(t *testing.T)             // --relocated-to == --public-url → error before listening
func TestServeClearsBootNotices(t *testing.T)             // an active restart notice is cleared ("boot") after a normal serve start
func TestIdentityShowAndRotate(t *testing.T)              // show prints GET /v1/hub/identity's pubkey; rotate without --yes refuses (seed unchanged); with --yes prints two fingerprints; the next serve's identity has previous + a valid endorsement
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/httpapi/ ./cmd/vecnet-hub/ -run 'Relocated|BootNotices|IdentityShow'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/httpapi/ ./cmd/vecnet-hub/ ./internal/testhub/` passes; `just isa-check` prints 0.
- [ ] **Step 5: Commit** `feat(hub): --relocated-to answers 410 hub_moved; vecnet-hub identity show|rotate`

---

### Task 6: CLI pins, hub verification, safe profile edits, `vecnet profile show|pin|repoint`

**Depends:** 3

**Files:**
- Create `internal/cli/pins.go`, `pins_test.go`, `profileedit.go`, `profileedit_test.go`, `profile.go`, `profile_test.go`.
- Modify `internal/cli/config.go` (field), `commands.go` (enrol, `connect`, `dispatch`, `usageText`).

**Interfaces:**
- **Consumes:** `GET /v1/hub/identity`, enrol's `hub_pubkey`, `hubid.*`, `testhub.CloneDB`.
- **Produces:**

```go
// config.go
// Profile.HubPubkey string `toml:"hub_pubkey,omitempty"`

// pins.go — <config dir>/state/pins.json (0600, dir 0700), lock <path>.lock via lockPath
type pinRecord struct {
	Hub, Pubkey, PinnedAt, How string // How: tofu | rotation | follow | accept
	TofuTriedAt string              `json:",omitempty"` // 404/network: retry after 24 h
	LastMove    *moveRecord         `json:",omitempty"`
}
type moveRecord struct{ From, To, NoticeID, IssuedAt string }
func pinsPath(getenv func(string) string) (string, error)
func (a *app) updatePins(fn func(map[string]pinRecord) error) error
func (a *app) effectivePin(name string, p Profile) (pubkey, how string) // p.HubPubkey, else the sidecar record whose Hub == NormalizeURL(p.Hub)
func (a *app) ensurePin(name string, p Profile)                         // TOFU, best effort, 2 s budget, never fails the command
func verifyHub(ctx context.Context, rawURL, pin string, allowInsecure bool) (doc envelope.HubIdentity, rotated bool, err error)
type relocationError struct{ Code, Reason string } // Code relocation_unverified | hub_identity_changed; exit 1

// profileedit.go
func editProfiles(path string, edits map[string]map[string]string) error
// Under the lock <config>.lock: read; for each [profiles.<name>] (bare or quoted header) replace or insert only the named
// key lines; re-decode and require (a) every profile equal to the old one plus the edits and (b) the same
// MetaData.Undecoded keys; write <config>.bak (0600) then atomic replace. Any other shape: an error, file untouched.
```

**`verifyHub` steps:**
1. `checkHubURL`.
2. A 32-byte random challenge, then the GET with no credential and no redirects.
3. Require `NormalizeURL(doc.public_url) == NormalizeURL(rawURL)`, then `VerifyChallenge`.
4. `doc.pubkey == pin` → ok. `doc.previous.pubkey == pin` with a valid endorsement → ok, with `rotated` set. Otherwise `relocationError`.

**Commands and behaviour:**
- **`connect`** calls `ensurePin` unless `a.inHook` is set (`cmdHook` sets it).
- **`enrol`** writes `HubPubkey` from the response (an older hub sends none: the field is left empty and TOFU fills the sidecar later). `enrol` also takes the config lock.
- **`vecnet profile show`** prints the hub, the pin fingerprint and how it was set, and the last move (`--json` too).
- **`vecnet profile pin [--accept FP]`**:
  - with no pin, writes `hub_pubkey`;
  - with a different served key, refuses unless `--accept` equals the served fingerprint.
- **`vecnet profile repoint URL [--force]`**:
  - `verifyHub(URL, pin)`, then edits `hub` and `hub_pubkey`;
  - `--force` sets `hub`, drops `hub_pubkey` and the sidecar record, and the next run TOFUs.

All of them take the profile from `--net` or `VECNET_PROFILE`; a bare run follows the operator rules (§13.7).

- [ ] **Step 1: Write the failing tests**

```go
func TestEnrolPinsHubPubkey(t *testing.T)                     // enrol against testhub → profile hub_pubkey == served pubkey
func TestTOFUNeverWritesConfig(t *testing.T)                  // existing profile without pin; `--net p peers` → config.toml bytes identical; pins.json[p] {Hub normalised, Pubkey served, How "tofu"}
func TestTOFUOldHub404(t *testing.T)                          // stub hub 404s identity → command succeeds; no pin; a second run within 24h makes no identity request
func TestHookDoesNotTOFU(t *testing.T)                        // hook run on an unpinned profile → no /v1/hub/identity request, no pins.json
func TestEditProfilePreservesCommentsAndUnknownKeys(t *testing.T) // config with comments, an unknown top-level key, a [profiles."a.b"] table and three profiles → only the target's hub line changes and one hub_pubkey line is added (line diff asserted); .bak == original; mode 0600
func TestEditProfileRefusesUnsafe(t *testing.T)               // inline-table profiles, a target absent from the file, or a hub value spanning lines → error; file byte-identical; no .bak
func TestVerifyHubChecks(t *testing.T)                        // same key ok; other key → relocation_unverified; public_url != URL (an identity relay) → relocation_unverified; corrupted challenge_sig → relocation_unverified; previous == pin + valid endorsement → ok, rotated; plain http public host → refused with no request
func TestProfileRepointVerifies(t *testing.T)                 // repoint to testhub.CloneDB → hub and hub_pubkey edited; a peers call then reaches the clone
func TestProfileRepointRefusesWithoutForce(t *testing.T)      // repoint to an unrelated testhub → exit 1 relocation_unverified, config byte-identical; with --force → hub changed, pin dropped
func TestProfilePinAccept(t *testing.T)                       // pin mismatch → refused; --accept <wrong fp> refused; --accept <served fp> writes hub_pubkey, How "accept"
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/cli/ -run 'Pin|TOFU|EditProfile|VerifyHub|Repoint'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/cli/` passes; `just bb GOOS=windows CGO_ENABLED=0 go build -o /dev/null ./cmd/vecnet` passes (`lock_other.go` path).
- [ ] **Step 5: Commit** `feat(cli): pin the hub key (TOFU sidecar), verify hubs, safe profile edits, vecnet profile`

---

### Task 7: CLI notices, restart handling and verified follow

**Depends:** 4, 5, 6

**Files:**
- Create `internal/cli/notice.go`, `notice_test.go`, `follow.go`, `follow_test.go`.
- Modify `internal/cli/watch.go`, `watch_test.go`, `client.go` (`hubError.Details`), `commands.go` (`dispatch` retry, `usageText`), `profile.go` (`follow`).

**Interfaces:**
- **Consumes:** the notices API, the 410 body, `verifyHub`, `effectivePin`, `editProfiles`, `updatePins`, `testhub.Relocate`/`CloneDB`.
- **Produces:**

```go
func (a *app) cmdNotice(args []string) error // post --kind K --message TEXT [--at TIME] [--new-url URL] [--expires DUR|TIME] | list | clear (ID | --kind K)
func movedNotice(err error) (envelope.HubNotice, bool) // 410 hub_moved → details.notice
func (a *app) follow(ctx context.Context, name string, p Profile, n envelope.HubNotice) (string, error)
// follow: kind moving; NormalizeURL(n.Hub) == NormalizeURL(p.Hub); new_url differs; pin (TOFU from the old URL if none);
// VerifyNotice(pin, n); n.IssuedAt > pins[name].LastMove.IssuedAt; verifyHub(n.NewURL, pin, p.AllowInsecureHTTP);
// editProfiles for every profile with the same normalised hub and effective pin (hub=new_url, hub_pubkey=pin);
// record LastMove; print one line on stderr. Any failure: relocationError, nothing changed.
// eventFieldsOf(hub_notice): sender "hub", name "hub", text "[<kind>[ <at>][ -> <new_url host>]] <message>" | "[cleared <kind>] <message>"
```

**One-shot commands (`dispatch`):**
- After a command fails with `hub_moved` and `VECNET_NO_FOLLOW` is not `1`: follow, then re-run the command once against the new hub.
- A command that consumed stdin (`-` text) is not re-run; the client prints `re-run the command`.
- With `VECNET_NO_FOLLOW=1`: exit 1, with the hint `vecnet --net P profile follow`.

**`watch`:**
- On each (re)connect, `verifyHub(p.Hub, pin)` runs before the events request. A rotation updates the pin; a mismatch exits 1 `hub_identity_changed`, so the bearer is never sent to that hub.
- At start, and on each posted moving notice, it calls `GET /v1/hub/notices` and arms a timer for `at` plus 0–30 s of jitter, which follows.
- A 410 on connect follows and reconnects.
- On `event: hub_restarting` it prints `- hub_restarting - hub hub: restarting; reconnecting in ~Ns`. It sleeps `retry_after_ms` ±20%. For `retry_after_ms + 60 s`, connection failures do not count toward the long-poll fallback and print one stderr line in total. The long-poll field gets the same treatment.

**`--for-monitor` lines** (the first four columns are kept):
- `<cursor> hub_notice - hub hub: …`
- `- hub_moved - hub hub: profile <P> now uses <url> (key <fp> verified)`
- `- relocation_unverified - hub hub: not following <url>: <reason>`

- [ ] **Step 1: Write the failing tests**

```go
func TestNoticePostListClear(t *testing.T)           // operator profile: post maintenance → list shows it; clear --kind maintenance → list empty; peer profile post → exit 1 forbidden
func TestMonitorLineHubNotice(t *testing.T)          // moving at 2026-11-06T02:00:00.000Z to https://new.example → "<c> hub_notice - hub hub: [moving 2026-11-06T02:00:00.000Z -> new.example] Moving tonight"; a message with CR/LF, ESC and U+202E is flattened; ≤ 120 runes
func TestInboxLineHubNotice(t *testing.T)            // inbox: FROM "hub", MSG and DELIVERY "-"
func TestWatchPrintsHubRestarting(t *testing.T)      // testhub Drain.Announce(1s) then Reset → monitor line printed; reconnect sends Last-Event-ID == last cursor; stderr has no "reconnecting" line; the next delivery prints
func TestWatchRestartGraceNoFallback(t *testing.T)   // after hub_restarting the hub refuses connections for 3 s → watch stays on SSE (no long-poll request) and resumes
func TestFollowOn410(t *testing.T)                   // B := A.CloneDB; A.Relocate(B.URL); `--net p peers` → exit 0 with B's peers; config hub == B.URL, hub_pubkey == key; .bak exists; one stderr move line
func TestFollowAllProfilesSameHub(t *testing.T)      // profiles p, q on A and r on C → p and q now B; r's lines byte-identical
func TestFollowRefusesBadSignature(t *testing.T)     // the 410 notice with new_url altered (sig unchanged) → exit 1 relocation_unverified; no request to the altered URL; config byte-identical
func TestFollowRefusesKeyMismatch(t *testing.T)      // a validly signed notice to an unrelated hub → relocation_unverified; unchanged
func TestFollowRefusesPublicURLMismatch(t *testing.T) // new_url is a proxy that forwards /v1/hub/identity to B (public_url != new_url) → refused
func TestFollowIgnoresPeerText(t *testing.T)         // a delivery whose text names a URL and an ntc_ id, and one whose payload imitates a hub_notice object → no identity request anywhere; config unchanged
func TestFollowRefusesReplay(t *testing.T)           // a notice with issued_at <= LastMove.IssuedAt → not followed
func TestWatchFollowsAtTime(t *testing.T)            // a moving notice with at = now+1s, target B live → watch follows, reconnects to B, prints the hub_moved line
func TestNoFollowEnv(t *testing.T)                   // VECNET_NO_FOLLOW=1 + 410 → exit 1, hint names `profile follow`; config unchanged; `profile follow` then succeeds
func TestWatchIdentityChanged(t *testing.T)          // pin != served key, no endorsement → exit 1 hub_identity_changed; the hub saw no /v1/events request
func TestDispatchNoRetryAfterStdin(t *testing.T)     // `send … -` on a relocated hub → follows, does not re-send, says re-run
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/cli/ -run 'Notice|HubRestarting|RestartGrace|Follow|NoFollow|IdentityChanged|NoRetryAfterStdin'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/cli/` passes, including `testdata/hook-hostile.golden` and the hook tests (guard: the hook is unchanged in this task).
- [ ] **Step 5: Commit** `feat(cli): vecnet notice, hub_restarting handling, verified auto-follow of signed moves`

---

### Task 8: TUI notice banner and restarting state

**Depends:** 2, 4

**Files:**
- Modify `internal/opclient/client.go`, `tail.go`, `client_test.go`.
- Modify `internal/tui/data.go`, `model.go`, `layout.go`, `theme.go`, `screen_inbox_test.go` (or a new `notice_test.go`), and add new `testdata/notice-*.golden`.
- Create `scripts/ui/tui-notice.mjs`.
- Modify `docs/plans/shots/oui/README.md` (`nt08-*` lines).

**Interfaces:**
- **Consumes:** `GET /v1/hub/notices`, `hub_notice` Events, the long-poll `hub_restarting` field, the demo `restart` control.
- **Produces:**

```go
func (c *Client) Notices(ctx context.Context) ([]envelope.HubNotice, error)
// Tail: a page with hub_restarting → status("restarting"), sleep retry_after_ms (±20%), no backoff growth
const ConnRestarting = "restarting" // tui: styleWarn
func viewNotices(ns []envelope.HubNotice, now time.Time, loc *time.Location, w int) string
// One row under the top bar, only while a notice is active, so body height drops by 1:
// "! MOVING 02:00 -> vecnet.xk.io  Moving to Fly tonight  (+1 more)"; ASCII only; Clean() on message and host; fit() to w.
// Priority moving > restart > maintenance. A hub_notice Event in a Batch triggers a Notices refetch.
```

- [ ] **Step 1: Write the failing tests**

```go
func TestNoticeBanner80x24(t *testing.T)       // golden notice-moving-80x24: banner row, body one row shorter
func TestNoticeBanner140x42(t *testing.T)      // golden notice-two-140x42: moving + maintenance → "(+1 more)"
func TestNoticeBannerHostile(t *testing.T)     // a message with ESC[2J, CR/LF, U+202E and 400 runes → one row, no ESC, width == w
func TestNoBannerFramesUnchanged(t *testing.T) // (guard) every existing golden is unchanged with no notices
func TestNoticeEventsRefresh(t *testing.T)     // a Batch with a hub_notice → one Notices call; cleared → banner gone
func TestTailReportsRestarting(t *testing.T)   // opclient: a long-poll page with hub_restarting → status "restarting", then "live" after the next page; backoff not raised
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/tui/ ./internal/opclient/ -run 'Notice|Restarting'` fails.
- [ ] **Step 3:** Implement, then write `tui-notice.mjs` (`withTui`; post notices through `asPeer('operator', 'POST', '/v1/admin/notices', …)`; `restart(4000)` together with `fail('/v1/', 503, …)` for the outage). Shots:
  - `nt08-tui-initial` (no banner);
  - `nt08-tui-progress` (conn `restarting`);
  - `nt08-tui-success` (moving + maintenance banner);
  - `nt08-tui-error` (the notice fetch failed: no banner, a toast).

  Wait on pane text, never on time.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/tui/ ./internal/opclient/` passes; `just bb build2 node scripts/ui/tui-notice.mjs` passes.
- [ ] **Step 5: Commit** `feat(tui): hub notice banner and restarting connection state`

---

### Task 9: Web UI notice banner and restart banner

**Depends:** 2, 4, 8

**Files:**
- Create `internal/ui/static/notice-model.js`, `notice.js`, `notice.css`.
- Modify `api.js` (`notices`), `store.js` (`tagsFor`: `hub_notice` → `['notices']`, mirroring Task 4's `opapi.Tags`; the events long-poll passes on the `hub_restarting` field), `app.js` (wiring), `index.html` (banner slot, stylesheet).
- Create `internal/ui/jstest/notice.test.mjs` (register it in `jstest/index.js` if the suite lists files).
- Create `scripts/ui/notice.mjs`.
- Modify `docs/plans/shots/oui/README.md` (`nt09-*` lines).

**Interfaces (Produces):**

```js
// notice-model.js (pure)
export function bannerItems(notices, now) // → [{ id, kind, label, when, host, text }] sorted moving, restart, maintenance; "when" relative + absolute
export function restartState(state, input) // inputs: {frame:'hub_restarting', retryAfterMs, at} | {frame:'ready'} | {tick, at}
                                            // → {phase:'none'|'restarting'|'unreachable', until, since}
export function isNoticeEvent(ev)           // ev.type === 'hub_notice'
// notice.js renders with textContent only; role="status" aria-live="polite"; dismiss hides a notice id for the session (it is not cleared hub-side)
```

**Behaviour:**
- `GET /v1/hub/notices` runs on sign-in and whenever the store invalidates the `notices` tag (a `hub_notice` from the existing events long-poll in `store.js`).
- An activity `hub_restarting` frame, or the long-poll field, shows "Hub restarting: reconnecting in N s" (amber).
- The next `ready` clears it.
- With no `ready` by `retry_after_ms + 30 s`, it becomes "Hub unreachable since HH:MM" (red), next to the existing `#conn` state.
- Moving notices link `new_url` (`rel="noopener"`, text = host).
- At 390 px the banner wraps to at most 3 lines with no horizontal scroll.

- [ ] **Step 1: Write the failing tests** (`notice.test.mjs`, node)

```js
test('bannerItems orders moving, restart, maintenance and drops cleared')
test('hostile message stays text') // '<img src=x onerror=alert(1)>' rendered via the DOM shim → no element created, textContent equal
test('restartState: hub_restarting → restarting; ready → none; tick past until+30s → unreachable')
test('moving link uses new_url host as text and rel=noopener')
test('tagsFor(hub_notice) is ["notices"]; malformed data invalidates nothing') // store.test.mjs
```

- [ ] **Step 2:** `just bb build2 node --test --test-concurrency=2 internal/ui/jstest/notice.test.mjs` fails.
- [ ] **Step 3:** Implement, then write `scripts/ui/notice.mjs` on `withDemo`.
  - **Shots** (1440×900 desktop; 390×844 phone via `page.setViewportSize`):
    - `nt09-initial` (no banner);
    - `nt09-success-desktop` (moving + maintenance);
    - `nt09-success-phone`;
    - `nt09-progress-desktop` (restarting, after `restart(5000)`);
    - `nt09-progress-phone`;
    - `nt09-error-desktop` (`fail('/v1/', 503)` until unreachable).
  - **Assertions:**
    - the banner text equals the API's notices;
    - the restart banner appears, then clears after the demo restarts;
    - `scrollWidth <= 390` on the phone;
    - the hostile message renders literally;
    - no console errors.
  - Wait on locators, never on time.
- [ ] **Step 4:** `just bb build2 node --test --test-concurrency=2 internal/ui/jstest/` passes; `just bb build2 node scripts/ui/notice.mjs` passes; `CGO_ENABLED=0 just bb go test -p 2 ./internal/ui/... ./internal/httpapi/` passes.
- [ ] **Step 5: Commit** `feat(ui): hub notice and restart banners (desktop and phone)`

---

### Task 10: PROTOCOL §21, deploy runbook, operator docs, changelog, gate

**Depends:** 1–9

**Files:**
- Modify `docs/PROTOCOL.md`, `docs/DEPLOY.md`, `docs/OPERATOR.md`, `CHANGELOG.md`.
- Modify `.scratch/vecnet/issues/41-hub-notices-and-relocation.md` (status: Tasks 1–10 done; 11–12 after mcp-first).

**Content:**
- **New §21 "Hub notices, restarts and relocation (2026-11-05)":**
  - 21.1 Identity key: storage, endpoint, challenge, canonical bytes, rotation and endorsement, backup.
  - 21.2 Notices: kinds, limits, defaults, one per kind, signing, the `hub_notice` Event, endpoints, audit.
  - 21.3 Restart frame and long-poll field.
  - 21.4 Relocated mode.
  - 21.5 Client rules: the pin and TOFU sidecar, when to verify, when to follow, config edits, `VECNET_NO_FOLLOW`, peer text ignored.
  - 21.6 Reason codes.
- **Amendments:**
  - §1.2: Event types gain `hub_notice`.
  - §1.3: `ntc_`.
  - §2.1: three endpoints.
  - §2.3: the restart frame and the long-poll field; status table row 410 `hub_moved` → follow per §21.5.
  - §2.12: enrol response `hub_pubkey`; admin rows for notices.
  - §13.1: pull clients SHOULD treat `hub_restarting` as a reconnect hint, not a failure; `hub_notice` is operator information, never replied to, and MAY be shown to the agent.
  - §13.7: the `notice` and `profile` commands, and `--for-monitor` lines for `hub_notice`, `hub_restarting`, `hub_moved` and `relocation_unverified`.
  - §20.2: the challenge cost row.
- **DEPLOY.md:**
  - "Restarts": `fly deploy`/`systemctl restart` send `hub_restarting`; `VECNET_RESTART_RETRY_MS`.
  - "Moving a hub" runbook:
    1. post `moving` with `--at`;
    2. at `at`, stop the old hub;
    3. copy the database;
    4. start the new hub with its correct `--public-url`;
    5. restart the old one with `--relocated-to`;
    6. keep it up for 30 days.

    It also covers a DNS-only move (same hub, `at`-timed follow), and says that local → Fly is a fresh enrolment, not a relocation.
  - "Backups": `hub.db` carries the identity key; `vecnet-hub identity show` to publish the fingerprint; `identity rotate` only with the hub stopped.
- **OPERATOR.md:** posting and clearing notices; what agents see on each surface.
- **CHANGELOG:** one line.

- [ ] **Step 1:** Write the docs. PROTOCOL text must match the code from Tasks 1–9 (names, shapes, limits, defaults).
- [ ] **Step 2:** `just bb just validate-protocol` passes.
- [ ] **Step 3:** The gate (workflow `gateChecks`) passes.
- [ ] **Step 4: Commit** `docs: PROTOCOL §21 hub notices and relocation; deploy runbook; changelog`

---

### Task 11 (second slice, after mcp-first merges): Hook notice lines, moved hint, guide

**Depends:** 7, 10, and mcp-first on master (its Task 8 owns `hook.go`, `guide.md` and the goldens; rebase onto it first).

**Files:**
- Modify `internal/cli/hook.go`, `hook_test.go`, `testdata/hook-*.golden` (new goldens only), `guide.md`, and the guide goldens.
- Modify `docs/PROTOCOL.md` (§13.9 only), `docs/connectors/claude-code.md`, `CHANGELOG.md`.

**Interfaces:**
- **Consumes:** `eventFieldsOf(hub_notice)`, `movedNotice`, `hookReplyClause` (mcp-first).
- **Produces** fixed copy (sanitised with `inert`; ids by `hookSafeID`):

```text
vecnet: hub notice for profile <P>, from the hub operator (signed by the hub key). <hookTrust>[ Commands need the same VECNET_CONFIG (<file>).]
<vecnet-inbox untrusted="true">
notice=<id> kind=<k>[ at=<ts>][ to=<host>] text="<inert(message, 300)>"
</vecnet-inbox>
```

```text
vecnet: profile <P>'s hub has moved to <host> (signed by its key). Run: <V> profile follow
```

**Behaviour:**
- `hookRun` admits `hub_notice` Events with `change: posted`, deduped by `notice_id` in the shown ring.
- A notice block follows any delivery block in the same `additionalContext`. Both together stay within the 6000-unit cap and the 4096-byte pipe cap; notices beyond the cap wait.
- A 410 `hub_moved` poll emits the moved line once per notice id. The hook never follows, verifies or edits config. It still exits 0 on every path.
- The guide gains a short "Hub notices and moves" section: what the lines mean, never reply, and `profile follow`.

- [ ] **Step 1: Write the failing tests**

```go
func TestHookShowsHubNotice(t *testing.T)            // a posted moving notice → notice block matching the fixed copy; a second run does not repeat it
func TestHookNoticeHostileIsInert(t *testing.T)      // golden hook-notice-hostile: tags, quotes and newlines escaped/flattened
func TestHookMovedHint(t *testing.T)                 // relocated testhub → moved line once; config byte-identical; exit 0
func TestHookUnboundOutputUnchanged(t *testing.T)    // (guard) deliveries only → bytes identical to before (hook-hostile.golden etc.)
func TestGuideMentionsHubNotices(t *testing.T)       // guide golden contains "Hub notices" and "profile follow"
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/cli/ -run 'HookShowsHubNotice|HookNoticeHostile|HookMovedHint|GuideMentionsHubNotices'` fails.
- [ ] **Step 3:** Implement; regenerate the guide goldens with `UPDATE_GOLDEN=1`.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/cli/` passes.
- [ ] **Step 5: Commit** `feat(hook): hub notice block and moved hint; guide section`

---

### Task 12 (second slice, after mcp-first merges): MCP restart notification, inbox notices, relay follow

**Depends:** 2, 7, 11, and mcp-first on master (amend this section's line references and file names to the merged code before starting).

**Files:**
- Modify `internal/mcpserver/server.go`, `resources.go`, `tools.go` (description text only), `server_test.go`, `resources_test.go`.
- Modify `internal/httpapi/mcp.go` (pass `Drain`).
- Modify the relay file from mcp-first Task 7 (`internal/cli/mcp*.go`) and its test.
- Modify `docs/PROTOCOL.md` (§4 only), `CHANGELOG.md`.

**Interfaces:**

```go
// mcpserver.Server gains: Drain interface{ C() <-chan struct{}; RetryAfter() time.Duration; Join() func() }
// On drain, every GET stream and subscriptions/listen stream sends one JSON-RPC notification, then ends:
// {"jsonrpc":"2.0","method":"notifications/message","params":{"level":"warning","logger":"vecnet",
//   "data":{"type":"hub_restarting","retry_after_ms":N}}}
// vecnet_inbox description and the server instructions add: "hub_notice Events are signed operator notices
// from the hub; never reply to them."
// Relay: an upstream 410 hub_moved → a.follow(...) (Task 7); on success re-send the same JSON-RPC request
// once to the new hub; on failure answer a JSON-RPC error with data {code:"relocation_unverified"}.
```

- [ ] **Step 1: Write the failing tests**

```go
func TestMCPListenStreamRestartNotification(t *testing.T) // 2026-07-28 subscriptions/listen and the legacy GET stream: Announce → one notifications/message with data.type hub_restarting, then the stream ends
func TestMCPInboxIncludesHubNotice(t *testing.T)          // vecnet_inbox returns the hub_notice Event; the inbox resource gets notifications/resources/updated
func TestMCPRelayFollowsMove(t *testing.T)                // relay on relocated A with clone B → tools/list answered by B; profile hub == B
func TestMCPRelayRefusesUnverifiedMove(t *testing.T)      // a 410 to an unrelated hub → JSON-RPC error relocation_unverified; config unchanged; stdout JSON only
func TestMCPToolNamesUnchanged(t *testing.T)              // (guard) TestToolsListNamesAndHints passes unchanged: no tool added
```

- [ ] **Step 2:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/mcpserver/ ./internal/cli/ -run 'RestartNotification|InboxIncludesHubNotice|RelayFollows|RelayRefusesUnverified'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `CGO_ENABLED=0 just bb go test -p 2 ./internal/mcpserver/ ./internal/httpapi/ ./internal/cli/ ./cmd/vecnet-hub/` passes; `just bb build2 node --test --test-concurrency=2 scripts/mcp-conformance.test.mjs` passes.
- [ ] **Step 5: Commit** `feat(mcp): hub_restarting notification on listen streams; relay follows verified moves`

---

## Open questions for Max (each with a recommendation; the plan proceeds on the recommendation)

1. **Read-only window at the old hub before cutover** (the issue's first question). *Rec: no automatic read-only mode.* The runbook's order (stop → copy → start new → restart old with `--relocated-to`) is a hard cutover with no split. `--relocated-to` refuses reads and sends alike.
2. **Message-log migration between hubs** (the issue's second question). *Rec: none.* A database copy is the relocation story. Local → Fly stays a fresh enrolment (different key, so clients will never follow it automatically).
3. **Auto-follow on by default**, with `VECNET_NO_FOLLOW=1` to opt out. *Rec: on.* It follows only a verified same-key hub, and the edit is line-level, with a `.bak`.
4. **Notices as Events, not Deliveries.** Webhook routes, Grok Bot and dots will not see notices. *Rec: accept for now.* If needed later, the hub can add a hub-authored `hold` message to push routes only.
5. **One-shot key checks.** Should every CLI command check the pinned key? That is stronger against a lost or hijacked domain, at the cost of one extra request per command. *Rec: no.* Check at TOFU, on watch connects and on follow.

--- SUMMARY ---

- **Goal:** warn connected agents about maintenance, restarts and moves, and let clients follow a move safely, for Max's hub and for self-hosters.
- **Key pieces:**
  - **Ed25519 hub identity.**
    - Its seed is in `meta.hub_identity_seed` (migration 0019), separate from the symmetric `hub_key`.
    - It travels with every `hub.db` backup or copy.
    - `GET /v1/hub/identity` signs a client challenge bound to `public_url`. Enrol returns `hub_pubkey`.
    - Rotation is offline and cross-signed one generation back.
  - **Operator notices** (`maintenance`, `restart`, `moving`), one active per kind, signed. They fan out as `hub_notice` Events to every enabled peer, and `GET /v1/hub/notices` lists them. Restart notices, and moving notices aimed at the hub itself, clear at boot.
  - **SIGTERM** sends `event: hub_restarting {retry_after_ms}` (no id) on SSE and activity streams, and a long-poll field, inside the existing shutdown grace.
  - **`--relocated-to URL`**: the old hub answers 410 `hub_moved` with the signed notice to everything except healthz, identity, notices and a moved page. It runs no workers.
  - **CLI pins:**
    - new profiles pin at enrol; existing ones TOFU into `state/pins.json`, so `config.toml` is never rewritten for a pin;
    - follows happen only on a 410, or (in `watch`) at a signed notice's `at`;
    - a follow needs the signature, a matching `public_url` and a fresh challenge;
    - it edits all same-hub, same-pin profiles with a verified line-level edit, keeping a `.bak`.
  - **Surfaces:**
    - `watch --for-monitor` lines (Claude Monitor);
    - `vecnet notice` and `vecnet profile show|pin|repoint|follow`;
    - the TUI banner and restarting state;
    - the web banner with desktop and phone shots;
    - after mcp-first: hook notice lines and moved hint, the MCP `notifications/message` restart hint, and relay follow.
- **Tasks:**
  1. Contract: hubid, envelope, `ntc_`, migration 0019.
  2. Restart drain.
  3. Identity endpoint, enrol pin, rotation, `CloneDB`.
  4. Notices.
  5. Relocated mode and `vecnet-hub identity`.
  6. CLI pins and profile edits.
  7. CLI notices, restart handling and follow.
  8. TUI.
  9. Web.
  10. Docs and gate.
  11. Hook and guide (after mcp-first).
  12. MCP and relay (after mcp-first).
- **Order:**
  - 1 and 2 run in parallel; then 3 → 4 → 5; 6 after 3; 7 after 4, 5 and 6; 8 after 2 and 4; 9 after 8.
  - 10 gates the first slice (`endTask: 10`).
  - 11 → 12 are the second slice once mcp-first is on master.
- **Decisions:**
  - The key is separate from `hub_key`.
  - Notices are Events.
  - The pin is TOFU into a sidecar.
  - The CLI follows only a same-key, verified target.
  - Relocated mode refuses reads too.
  - The restart frame is transport-only.
  - House-style command names.
  - Boot clears self-referential notices.
  - The hook never follows.
  - No `connectors/**` or pre-merge `internal/mcpserver`, hook or guide edits.
- **Open questions:** read-only window (no), log migration (no), auto-follow default (on), Events vs Deliveries for push-only peers (accept), per-command key checks (no).
