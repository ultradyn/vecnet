# Dots on oauth.xk.io (Option A) Implementation Plan

> **For agentic workers:** execute with `docs/plans/vecnet-core-build.workflow.mjs` and `docs/plans/dot-oauth-xk.workflow-args.json`. Each worker receives its task and Global Constraints; interfaces below are contracts. Every task ends at `---`. This is a plan, not implementation or deployment evidence.

**Goal:** A dot authenticates to vecnet's `/mcp` through oauth.xk.io and acts only as its operator-prebound dot peer, including durable webhook push and local revocation.

**Architecture:** Keep vecnet bearer authentication first. In opt-in external mode, an ES256 verifier validates the configured issuer's access tokens, the hub resolves an exact `(issuer, subject)` binding, and MCP subscriptions persist a typed authorization lease. A shared stack wires the same binding/mode checks into requests and the Events worker. The built-in AS remains the default when external mode is off; external mode advertises only the external AS and disables local issuance and local OAuth-token authentication. Seven tasks cover report gaps 3–6 and 8.

**Tech stack:** Go 1.26, SQLite migrations, stdlib `crypto/ecdsa`, `crypto/elliptic`, `crypto/sha256`, `encoding/json`, `net/http`; reuse guarded `internal/egress` transport. No new module or handwritten cryptographic primitive. Node ESM for existing conformance checks.

**Spec:** Readiness report `/home/xertrov/src/vecnet4/.scratch/vecnet/integrations/dot-oauth-readiness.md` (2026-11-05), especially option A and gaps 3–6/8; coordinator decision in `plan-oauth-xk.assignment.md`; `docs/PROTOCOL.md` §4.1/4.4/4.5/13.4/20.1. Starting snapshot: `master` 0f273d0. The report is the research input; its live observations are not acceptance of this integration.

## Global Constraints

- Docs-only drafting here. Implementation workers later change only their task files. Never touch `connectors/**`, `secrets/`, `*.secret`, env files, `.data/`, `.demo-data/`, `~/.config/vecnet`, the dev hub on 45752 or preview on 49575. Do not use `attn`. No live state changes, deployment, registration, consent or ops edits are authorized by this plan.
- Before implementation, rebase onto master with **`integ-grok` gaps 1 and 2** landed: exact Origin allowlist and stable profile `id`. Preserve their interfaces/tests; do not reimplement them. Preserve `hub-notices` work and migration **0019**; this plan exclusively reserves **0020**. If 0019 has not landed, wait before applying/testing the migration chain; do not renumber either migration. Merge PROTOCOL changes by section, keeping §21 notices intact.
- Build/test only on a build box. Targeted commands use `CGO_ENABLED=0 just bb go test -p 2 ./internal/<pkg>/ -run '...'`. Node checks use `just bb build2 node ...`. Existing tests must all continue to pass with external mode off. Go formatting, git, ISA inspection and `go list` may run locally.
- Tests use temporary databases and injected clocks/getenv; set `VECNET_CONFIG`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `HOME` to temporary directories through app injection. Issuer, JWKS and callbacks are **httptest fakes**, never oauth.xk.io or other live hosts. An injected transport may trust the test TLS certificate; no production insecure-TLS/private-network bypass.
- Production contract: `VECNET_PUBLIC_URL=https://vecnet.xk.io`; external issuer `https://oauth.xk.io`; explicit JWKS `https://oauth.xk.io/jwks`; audience derived from public URL as `https://vecnet.xk.io/mcp`; required scope `vecnet`. Never infer issuer from a token, change public URL to the issuer, or fetch token-supplied URLs.
- New configuration: `--oauth-issuer` / `VECNET_OAUTH_ISSUER`, `--oauth-jwks-url` / `VECNET_OAUTH_JWKS_URL`. Both empty selects built-in mode; both set selects external mode. Flags override env. Partial, malformed, non-HTTPS, userinfo/query/fragment URLs or an invalid public HTTPS origin fail startup before listener/workers. Exact issuer bytes are preserved; no silent issuer aliasing/trailing-slash normalization. JWKS may have a different explicit HTTPS host. No automatic discovery or fallback on failure.
- External mode mounts both resource documents with exactly one external authorization server; local RFC 8414 and `/oauth/*` return 404 and `vat_` tokens fail `/mcp`. Off mode retains the original AS and opaque-token path. `vn_peer_`, `vn_principal_`, `vn_operator_` secrets keep their existing authentication and authorization on MCP and HTTP. External JWTs are accepted only on `/mcp`, never as operator/API bearer credentials.
- External binding is operator-only, exact/case-sensitive, one active identity per dot and one dot per identity, in a separate table from `oauth_principals`. Never auto-provision on JWT arrival or map a display name/client ID to a peer. `(https://oauth.xk.io, max)` is the report's pilot identity; all its ChatGPT connections share one peer. Browser code linking and per-connection discrimination are follow-ups.
- Rebinding requires explicit replacement and retires prior authority atomically. Each activation gets a new local principal and incremented generation. `valid_after` is the next whole UTC second after commit-time clock sampling; require JWT `iat >= valid_after` on every new authentication. This prevents pre-unbind JWTs reclaiming a later binding; ops must refresh/reconnect after a bind and that second boundary. Same-active-tuple retries are idempotent and do not move the cutoff.
- Lease kinds are `bearer`, `local_oauth`, `external_oauth`; missing or inconsistent OAuth lease data denies, never becomes bearer. No raw JWT, cookie, refresh token, callback secret/path or key material appears in audit/tool output/logs. Scope/client identity comes only from verified claims; absent/unconfirmed provider client claim stays empty and is not authorization input.
- Offline issuer revocation is visible only at access-token expiry: enforce `0 < exp-iat <= 3600s`, no expiry grace, and at most 30s future `iat`/`nbf` tolerance. Thus a trusted clock's upstream revocation delay is at most remaining token lifetime (up to one hour plus bounded issuer clock offset). Local unbind, ownership move, disable, mode change and lease revocation stop the next authorization check immediately; an HTTP request already authorized/in flight cannot be recalled. Check again immediately before every challenge/callback attempt, including retries. Do not claim upstream introspection exists.
- Mode is immutable for a running stack. Persist a mode fingerprint and monotonically increasing mode generation; before workers start, a change of mode/issuer/JWKS/resource increments it and revokes existing external leases transactionally. Same-config restart preserves leases. An off-mode boot invalidates them even if external mode is later re-enabled. Nil external resolver or any store/verification error denies push.
- Existing MCP methods, profile ID semantics from `integ-grok`, callback signatures, cursors, subscription tuple identity, retry policy, pause precedence and local OAuth family revocation stay intact. Gap 7 parity work is deferred. Guard tests explicitly marked below pass before their task; all other named tests must fail first.

## Review Focus

1. **Token confusion and algorithm pinning:** Task 2 `TestExternalJWTRejectsConfusion` and `TestExternalJWTClaims`; Task 6 `TestExternalMixedAuth`. Wrong-resource/issuer/scope/ID tokens, `none`, HS256, RS256 and malformed ES256 never authenticate.
2. **kid, rotation and outage:** Task 2 `TestExternalJWKSCacheRotation`, `TestExternalJWKSOutage`, `TestExternalJWKSBounds`. Bounded fetches, no attacker-directed egress, no expired-key-cache fallback, no unknown-kid request storm.
3. **Subject-to-peer confusion and replay:** Task 3 `TestExternalBindingAtomicReplacement`, `TestExternalBindingConcurrent`, `TestExternalBindingGeneration`; Task 4 `TestExternalBindingOperatorOnly`. No cross-issuer collision, silent reassignment or old-token resurrection.
4. **Revocation delay and push after unbind/disable:** Task 5 `TestExternalLeaseGatesEveryAttempt`, `TestExternalLeaseRenewal`; Task 6 `TestExternalModeTransition`, Task 7 `TestExternalDotRoundTrip`. Queued/retry sends require live ownership, binding generation, mode generation and unexpired lease. No blank-hash bypass.
5. **Default compatibility and wiring:** Task 6 `TestLocalModeCompatibility` (guard) and Task 7 `TestExternalDotRoundTrip`; production and testhub use the same configured stack. All existing tests and conformance remain green; no live-service fixtures.

## Dependency map

```json
{"1":[],"2":[1],"3":[1],"4":[3],"5":[1,3],"6":[2,4,5],"7":[6]}
```

All tasks start after the preflight dependencies above. Prefix `dx`; implementation branch `dot-oauth-xk`; worktree `/home/xertrov/src/vecnet4/.worktrees/dot-oauth-xk`; status `/home/xertrov/src/vecnet4/.scratch/vecnet/build-dot-oauth-xk`; runner `just bb `. Backend tasks route `sol`. Shared file order: `internal/hub/external_oauth.go` 3 → 6; `internal/mcpserver/auth.go` 5 → 6; HTTP routes 4 → 6; testhub 6 → 7. Task 1 owns all schema changes; task 7 owns shipped docs. Coordinator ensures statusDir is ignored before running the workflow. No task edits the source research report.

## Decisions

Context: retain default behavior while selecting one login authority per deployment. Goals G1: built-in defaults still work; G2: external selection has no local OAuth bypass; G3: bearer operators/peers continue working.

| Idea | All | G1 | G2 | G3 |
|---|---|---|---|---|
| **External-only metadata, disable local OAuth in external mode, retain bearers** | PASS | PASS | PASS | PASS |
| Advertise and accept both OAuth authorities in external mode | FAIL | PASS | FAIL | PASS |
| Replace all bearer authentication with external OAuth | FAIL | FAIL | PASS | FAIL |

Keeping two OAuth authorities permits invitation/cookie login despite selecting shared sign-in (G2). Replacing bearers breaks existing clients and operator recovery (G1/G3). Disabling local OAuth only in external mode selects one authority without deleting its implementation or data. Existing local OAuth subscriptions also stop in external mode; returning to local mode reuses only still-valid local grants/bindings.

Context: first slice must preserve explicit invitation/ownership controls without a new browser client. Operator prebind plus generation leases satisfies that goal; automatic audience-to-peer assignment fails because audience authorizes a resource, not a local peer. Browser link-code login is deferred, not silently delegated to oauth.xk's consent page.

---

### Task 1: Typed authorization contract and migration 0020

**Depends:** none (preflight master dependencies required).

**Files:** create `internal/auth/oauth.go`, `internal/auth/oauth_test.go`, `internal/store/migrations/0020_external_oauth.sql`; modify `internal/store/store_test.go`.

**Interfaces:**

```go
// internal/auth; value types only, no events/hub/mcpserver imports
 type OAuthLease struct {
     Kind string // local_oauth | external_oauth; nil lease means bearer only for a vecnet subscription
     TokenHash, Issuer, Subject, PrincipalID, PeerID, ClientID string
     BindingGeneration, ModeGeneration int64
     IssuedAt, Expires time.Time
 }
 func (l OAuthLease) Validate() error
```

Migration: create STRICT `external_oauth_bindings` with `(issuer,subject)` primary key, `principal_id` FK, nullable `peer_id` FK, monotonically increasing `generation`, integer `valid_after`, `bound_at`, nullable `revoked_at`; partial unique active-peer index. Retain tombstones for generation monotonicity. Create singleton `external_oauth_mode` with fingerprint/generation. Create STRICT `event_oauth_leases` keyed/FK by subscription ID with ON DELETE CASCADE, kind CHECK, token hash, issuer/subject, principal/peer, binding/mode generation, issued/expiry/revoked timestamps and CHECKs for required external fields. Preserve existing subscription columns and uniqueness. Backfill local OAuth lease rows from existing `principal_kind='oauth'` subscriptions with nonempty `token_hash` and expiry; OAuth rows lacking evidence remain unauthorized. Bearer rows stay unchanged. No FK from external leases into local `oauth_tokens`.

**Failing tests and assertions:** `TestMigration0020ExternalOAuth`: upgrade fixture through 0019 then 0020, restart, retain old data/0019 identity bytes, reject duplicate active peer and malformed lease; valid external lease does not require a local token row. `TestMigration0020LegacyLeases`: existing local OAuth rows backfill, vecnet rows untouched, missing-token OAuth row cannot become bearer. `TestOAuthLeaseValidate`: reject empty external subject/hash/generations/expiry and kind mismatch.

- [ ] Add failing migration/contract tests; implement migration and validation without modifying shipped migrations.
- [ ] Run `CGO_ENABLED=0 just bb go test -p 2 ./internal/store/ -run 'TestMigration0020'` and `CGO_ENABLED=0 just bb go test -p 2 ./internal/auth/ -run 'TestOAuthLeaseValidate'`; commit schema/contract.

---

### Task 2: Bounded ES256 JWT verifier and JWKS cache

**Depends:** 1.

**Files:** create `internal/externaloauth/verifier.go`, `jwks.go`, `verifier_test.go`, `jwks_test.go`.

**Interfaces:**

```go
// internal/externaloauth; no hub/mcpserver imports
 type Config struct { Issuer, JWKSURL, Audience string }
 type Claims struct {
     Issuer, Subject, ClientID string
     Scopes []string
     IssuedAt, Expires time.Time
 }
 type Verifier struct { /* immutable config; bounded, mutex-protected cache */ }
 var ErrInvalidToken, ErrUnavailable error
 func ValidateConfig(c Config) error
 func New(c Config, client *http.Client, now func() time.Time) (*Verifier, error)
 func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error)
```

Verification policy: compact JWS at most 16 KiB, exactly three base64url segments, bounded decoded objects, reject duplicate security-relevant JSON keys, critical extensions and unsupported `b64`. Pin `alg=ES256`; require bounded nonempty `kid` (128 bytes), public EC P-256 key, matching alg/use/key_ops when present; reject duplicate kids, invalid/off-curve points, private key fields and unsupported key types. Verify 64-byte JOSE R||S using stdlib ECDSA before trusting claims. Ignore no token-supplied key location: reject `jku`, `x5u`, embedded `jwk`. Require exact issuer, nonempty subject at most 255 bytes with no controls, audience string or singleton array exactly the resource, integer `exp`/`iat`, optional integer `nbf`, exact whitespace-delimited `vecnet` scope and time limits from Global Constraints. Reject explicit ID-token type; accept absent typ or provider-compatible `JWT`/`at+jwt` only with all access-token claims. ID tokens fail audience/scope even when signed by the same key. Do not require an unverified client claim or use `azp`/`client_id` to select a peer; leave ClientID empty in this slice. Add synthetic oauth-xk-shaped access/ID fixtures, no real tokens.

JWKS GET only the configured URL through injected guarded transport: 3s timeout, no redirects, 256 KiB response, at most 32 keys. Cache TTL is min(valid server max-age, 5 minutes), default 5 minutes; no-store/max-age=0 is not retained. Coalesce fetches; at most one refresh per 30s globally for unknown kid, including negative results. Known fresh keys continue during an outage; stale cache is never used. Refresh replaces the set atomically, so removed keys cease working on refresh. Malformed/missing/unknown key => invalid token after a successful fetch; failed required fetch => unavailable. No startup network call required.

**Failing tests and assertions:** `TestExternalJWTClaims` covers valid access, issuer/audience/scope/type/size, missing/null/fractional/expired/future claims, nbf and >1h lifetime. `TestExternalJWTRejectsConfusion` covers ID token, none/HS256/RS256, signature tampering, duplicated claims/header, hostile URL headers, malformed key/signature. `TestExternalJWKSCacheRotation` covers known key cache hit, new kid refresh, removed keys and parallel miss coalescing. `TestExternalJWKSOutage` covers fresh known key success, expired-cache/unknown-key refusal and recovery. `TestExternalJWKSBounds` asserts timeout, redirect refusal, key/body limits and refresh rate bound with fake clock.

- [ ] Add failing table/rotation tests with httptest TLS issuer/JWKS and generated test keys.
- [ ] Implement verifier/cache; errors never include tokens or response bodies.
- [ ] Run `CGO_ENABLED=0 just bb go test -p 2 ./internal/externaloauth/ -run 'TestExternal(JWT|JWKS)'`; commit verifier.

---

### Task 3: Atomic external identity binding and revocation

**Depends:** 1.

**Files:** create `internal/hub/external_oauth.go`, `external_oauth_test.go`; modify `internal/hub/admin.go` only for common retirement/invalidation hooks.

**Interfaces:**

```go
// internal/hub
 type ExternalBinding struct {
     Issuer, Subject, PrincipalID, PeerID string
     Generation int64
     ValidAfter time.Time
 }
 func (h *Hub) BindExternalDot(ctx context.Context, actor auth.Caller, issuer, subject, peerID string, replace bool) (ExternalBinding, error)
 func (h *Hub) UnbindExternalDot(ctx context.Context, actor auth.Caller, issuer, subject string) error
 func (h *Hub) ResolveExternalDot(ctx context.Context, issuer, subject string, issuedAt time.Time) (ExternalBinding, error)
 func (h *Hub) ResolveOAuthDotPeer(ctx context.Context, principalID string) (string, error)
```

Bind checks actor is operator, valid exact identity and enabled `kind=dot` target. API restricts issuer to configured issuer (Task 4); hub validates syntax independently. In one transaction, reject conflicts with 409 unless `replace=true`; retire both conflicting external bindings and any target local `oauth_principals` binding/grants, consume outstanding target dot links, revoke old leases, use reviewed `BindDotPrincipalTx` to allocate/move to a fresh principal and revoke peer bearers, then write mapping/generation/cutoff and sanitized audit. Do not manipulate a local cookie subject into an external subject. Unbind sets tombstone, increments generation and revokes leases; repeated unbind succeeds without recreating anything. Resolver requires current peer ownership, enabled dot, active binding and issue cutoff; no arbitrary `as` peer. Combined principal resolver retains current local ownership semantics and handles external principals uniquely. Move/disable invalidation is transactional; re-enable or moving away/back must not resurrect old external leases (revoke on transition). Audit actions `external_dot_bound`, `external_dot_unbound`, `external_dot_replaced` record actor, exact issuer/subject and affected peer/generation, never token material.

**Failing tests and assertions:** `TestExternalBindingExactIdentity`: distinct issuer/subject/case never collide, no auto-bind and non-dot/disabled targets rejected. `TestExternalBindingAtomicReplacement`: conflict without replace mutates nothing; replace cuts off old local/external mapping, grants, links and leases atomically; injected rollback leaves all unchanged. `TestExternalBindingConcurrent`: simultaneous conflicting binds yield one owner; same-tuple retry preserves principal/generation/cutoff. `TestExternalBindingGeneration`: unbind/rebind increments generation, old iat cannot authenticate, fresh iat works; move-away/back and disable/enable cannot revive old leases.

- [ ] Write failing transactional tests; implement operator check and retirement with shared hub primitives.
- [ ] Run `CGO_ENABLED=0 just bb go test -p 2 ./internal/hub/ -run 'TestExternalBinding'`; commit binding service.

---

### Task 4: Operator prebind/unbind API and CLI

**Depends:** 3.

**Files:** create `internal/httpapi/external_oauth.go`, `external_oauth_test.go`, `internal/cli/oauth.go`, `oauth_test.go`; modify `internal/httpapi/admin.go`, `server.go` (Deps issuer field only), `internal/cli/commands.go` usage/dispatch.

**Interfaces:**

```go
// internal/httpapi; Deps gains ExternalOAuthIssuer string
 func (s *server) adminBindExternalDot(w http.ResponseWriter, r *http.Request)
 func (s *server) adminUnbindExternalDot(w http.ResponseWriter, r *http.Request)
// internal/cli; uses existing app.call, no sqlite/hub import
 func (a *app) cmdOAuth(args []string) error
```

Wire `POST /v1/admin/oauth/bind` body `{issuer,subject,peer_id,replace?}` to binding service, returning 200 `{issuer,subject,principal_id,peer_id,generation,valid_after}`; `POST /v1/admin/oauth/unbind` body `{issuer,subject}`, returning 204 idempotently. Existing admin middleware enforces operator and body limit; hub checks operator again. Missing/unknown fields => 400, conflict => 409, disabled external binding mode => 409 `external_oauth_disabled`, wrong configured issuer => 400. Unbind remains allowed while external mode is off for recovery. No external JWT gains admin access.

Publish exact commands (global `--net` selection unchanged):

```sh
vecnet --net fly-operator oauth bind PEER_ID --issuer https://oauth.xk.io --subject max
vecnet --net fly-operator oauth bind PEER_ID --issuer https://oauth.xk.io --subject max --replace
vecnet --net fly-operator oauth unbind --issuer https://oauth.xk.io --subject max
```

Print binding IDs/cutoff and instruct refresh/reconnect after cutoff, never accept/prompt for a JWT. Subject is data, not a peer name lookup. `--replace` is explicit destructive retirement, no implicit retry with replace.

**Failing tests and assertions:** `TestExternalBindingOperatorOnly`: unauthenticated 401, peer/principal/JWT 403 or 401 per existing middleware, operator succeeds, no rejected-call mutations. `TestExternalBindingAPIValidation`: exact issuer, limits, unknown fields, conflict and idempotent unbind. `TestOAuthCLICommands`: exact paths/JSON/replace bit, missing arguments fail before request, secrets absent from output. `TestOAuthCLIIsolatedConfig`: temporary injected config only, no writes to profile or credentials.

- [ ] Write failing handler/CLI tests and implement thin adapters, preserving every existing command.
- [ ] Run `CGO_ENABLED=0 just bb go test -p 2 ./internal/httpapi/ -run 'TestExternalBinding'` and `CGO_ENABLED=0 just bb go test -p 2 ./internal/cli/ -run 'TestOAuthCLI'`; commit admin surface.

---

### Task 5: Typed push leases and per-attempt authorization

**Depends:** 1, 3.

**Files:** modify `internal/mcpserver/auth.go`, `events.go`, `events_test.go`, `internal/oauth/verify.go`, `internal/events/store.go`, `worker.go`, `store_test.go`, `worker_test.go`; create `internal/events/oauth_lease.go`, `oauth_lease_test.go`.

**Interfaces:**

```go
// internal/mcpserver: OAuthInfo gains Lease *auth.OAuthLease; existing fields stay
// internal/events: Subscription gains Lease *auth.OAuthLease; Upsert signature stays unchanged
 type LeaseAuthorizer func(context.Context, auth.OAuthLease) (bool, error)
// Worker gains AuthorizeLease LeaseAuthorizer and ExternalOAuthEnabled bool; no externaloauth/httpapi import
 func (st *Store) SaveOAuthLeaseTx(ctx context.Context, tx *sql.Tx, subscriptionID string, lease auth.OAuthLease) error
 func (st *Store) LoadOAuthLease(ctx context.Context, subscriptionID string) (auth.OAuthLease, error)
```

Local TokenAuth fills a local lease while preserving existing OAuthInfo fields and token-row checks. For existing in-process local OAuthInfo callers, derive a local lease only from a nonempty TokenHash verified against the local access-token row (including principal, resource, expiry and revocation); never derive an external lease or a bearer from missing data. External adapter (Task 6) fills an external lease with verified expiry/hash and live binding/mode generations; never insert JWTs into `oauth_tokens`. Both retain existing `KindOAuth` subscription identity (no schema CHECK widening). `eventsSubscribe` clamps expiry to verified exp and persists/upserts lease in the same transaction as subscription. Persisted lease is authoritative; tuple renewals retain ID/cursor/outbox, changing token hash/expiry only after full auth and challenge checks. Recheck auth after challenge before committing. Missing lease for OAuth denies, with legacy local backfill supported; nil lease is bearer only for KindVecnet. No inference solely from blank TokenHash.

Worker checks peer enabled/ownership and subscription expiry, then branches explicitly: bearer uses existing gates; local OAuth checks unrevoked local token, expiry and local binding and is disallowed in external mode; external OAuth checks persisted lease revoked_at, expiry, current external mode generation, issuer/subject, binding generation/cutoff, principal and peer ownership. Resolve immediately before every outgoing verification/delivery POST and every retry; nil external authorizer/error denies external push; local mode retains its existing ResolveDotPeer check even for existing worker fixtures that omit the new callback. ExternalOAuthEnabled gates local OAuth sends off in external mode. Do not fetch JWKS per callback or extend a lease on refresh alone. Existing operator pause stays stronger than renewal; admin resume cannot bypass a revoked/expired lease. Unbind/replacement/move/disable revoke leases via Task 3; mode transition revokes via Task 6. Restart restores leases without keeping raw tokens.

**Failing tests and assertions:** `TestExternalLeasePersistence`: restart preserves typed claims/expiry and no local token row or JWT plaintext, bogus/missing lease denies. `TestExternalLeaseGatesEveryAttempt`: queue then unbind/rebind/move/disable/expire/revoke; callbacks and retries after change count zero; disable/enable still zero for old lease; store/nil resolver failure makes no POST. Use blocking callback barriers for deterministic race boundaries. `TestExternalLeaseRenewal`: refreshed token alone does not renew; resubscribe with fresh token updates expiry/hash retaining ID/cursor, expired token cannot renew and operator pause persists. `TestLocalOAuthLeaseRevocation` (guard existing semantics, new adapter coverage): local family revoke stops next callback; bearer subscriptions keep existing behavior. Add MCP test `TestEventsExternalLeaseBoundPeer`: foreign peer argument/unsubscribe denied, missing lease rejected before challenge.

- [ ] Add failing fake-clock/store and callback-count tests; implement typed store branching and MCP lease plumbing.
- [ ] Run `CGO_ENABLED=0 just bb go test -p 2 ./internal/events/ -run 'Test(ExternalLease|LocalOAuthLease)'` and `CGO_ENABLED=0 just bb go test -p 2 ./internal/mcpserver/ -run 'TestEventsExternalLease'`; commit lease path.

---

### Task 6: Fail-closed external mode, authenticator, metadata and shared stack

**Depends:** 2, 4, 5.

**Files:** modify `cmd/vecnet-hub/main.go`, `main_test.go`, `internal/httpapi/stack.go`, `mcp.go`, `oauth.go`, `server.go`, `internal/oauth/server.go`, `internal/hub/external_oauth.go`, `internal/testhub/testhub.go`; create `internal/httpapi/external_auth.go`, `external_auth_test.go`, `external_mode_test.go`.

**Interfaces:**

```go
// internal/httpapi; preserve NewStack(h) default compatibility
 type OAuthConfig struct {
     Issuer, JWKSURL string
     JWKSClient *http.Client // programmatic test seam only; nil in production
 }
 type ExternalTokenAuth struct { Verifier *externaloauth.Verifier; Hub *hub.Hub; ModeGeneration int64 }
 func (a *ExternalTokenAuth) Authenticate(ctx context.Context, r *http.Request) (mcpserver.Principal, error)
 func NewStackWithOAuth(h *hub.Hub, cfg OAuthConfig) (*Stack, error)
// Deps gains ExternalAuth mcpserver.Authenticator,
// ResolveOAuthDotPeer func(context.Context,string)(string,error).
// ExternalOAuthIssuer from Task 4 is set from the same validated config.
// internal/testhub; Start and StartWith keep their current signatures
 func StartWithOAuth(t *testing.T, cfgFn func(*hub.Config), oauthCfg httpapi.OAuthConfig) *Env
// internal/hub
 func (h *Hub) ConfigureExternalOAuth(ctx context.Context, fingerprint string) (int64, error)
 func (h *Hub) AuthorizeExternalLease(ctx context.Context, lease auth.OAuthLease, modeGeneration int64) (bool, error)
// internal/oauth; split metadata from local AS mounting, preserve Mount default
 func (s *Server) MountResourceMetadata(mux *http.ServeMux, authorizationServer string)
```

Constructor validates before listener/workers, derives audience from hub public URL, builds guarded JWKS client, persists mode transition, installs authenticators and the combined dot resolver for both MCP and worker. Preserve `NewStack(h)` callers by delegating to the empty-config path (the no-error compatibility wrapper panics with a sanitized construction error if the configured constructor fails; production cmd uses the error-returning constructor); production and testhub call the same configured constructor. Test injection uses OAuthConfig.JWKSClient plus the hub clock, never a production insecure URL flag; validate HTTPS URLs even with an injected client. No CLI/env input can select that client. Ensure relocation/drain behavior from hub-notices takes precedence and does not accidentally start workers.

Auth order: existing bearer resolver first; then exactly selected OAuth authenticator. External adapter skips non-JWS and reserved local/bearer prefixes without JWKS fetch; validates JWT, resolves exact binding each request, returns `auth.Caller{Kind:"principal", PrincipalID: binding.PrincipalID, PeerID: binding.PeerID, PeerKind:"dot"}` plus OAuthInfo/typed lease. Known bad tokens map to `auth.ErrUnauthenticated` (401 with existing resource challenge); JWKS required-fetch failure maps to sanitized infrastructure error (existing MCP 500), no auth fallback. Binding database failures also 500, never grant. MCP session reuse does not cache identity past an authenticated request or allow a different peer via `as`.

External mode mounts only both RFC 9728 documents: `{resource:<public>/mcp,authorization_servers:[<issuer>],scopes_supported:["vecnet"],bearer_methods_supported:["header"]}`. Explicitly return 404 for local RFC 8414 and all `/oauth/*`, not the UI SPA fallback. Existing `/v1/admin/dot-links` returns 409 `external_oauth_disabled` in external mode, explaining that prebind is required; off mode retains its current behavior. No misleading local OAuth cookies or issuer metadata. Off mode calls original Mount and TokenAuth. Mode switch invalidation must be done even on off-mode startup; fingerprint includes mode, issuer, JWKS and audience. Empty/off mode refuses external leases; external mode refuses local OAuth leases.

**Failing tests and assertions:** `TestExternalConfigFailClosed`: incomplete/malformed flags/env and bad public URL fail before serving; empty preserves defaults and flags override env. `TestExternalMetadata`: both paths, exact selected issuer/resource/scope, challenge resolves; local endpoints 404, link code rejected. `TestExternalMixedAuth`: valid external JWT acts only as bound peer, unknown subject/disabled/old iat fail; every bearer kind unchanged; external JWT fails admin/REST, local vat fails external MCP, no JWKS request for bearer/vat. `TestExternalModeTransition`: ext→off→ext and issuer/JWKS/resource changes revoke prior leases; same-config restart preserves them; local lease cannot push in ext mode. `TestLocalModeCompatibility` (guard): existing built-in metadata, consent, refresh, bearer and local push paths still pass when off.

- [ ] Add failing mode/stack/auth tests; wire constructor into cmd and testhub without duplicating logic. Reconcile `integ-grok` Origin/profile seams and hub-notices relocation gates.
- [ ] Run `CGO_ENABLED=0 just bb go test -p 2 ./internal/httpapi/ -run 'Test(External|LocalModeCompatibility)'` and `CGO_ENABLED=0 just bb go test -p 2 ./cmd/vecnet-hub/ -run 'TestExternalConfig'`; commit mode integration.

---

### Task 7: Integration acceptance, connector/deploy/protocol docs and gate

**Depends:** 6.

**Files:** create `internal/httpapi/external_dot_test.go`; modify `docs/connectors/dots.md`, `docs/DEPLOY.md`, `docs/PROTOCOL.md`, `CHANGELOG.md`. Do not edit `connectors/**`.

**Interfaces:**

```go
// internal/httpapi_test, using shared testhub stack, httptest issuer and callback
 func TestExternalDotRoundTrip(t *testing.T)
 func TestExternalDotProfileContinuity(t *testing.T)
```

**Failing tests and assertions:** `TestExternalDotRoundTrip`: fake issuer ES256 token → operator prebind → token issued after cutoff → correct profile → signed challenge/subscribe → wake message → signed callback/accepted → authenticated same-thread tool reply and processed receipt. Advance beyond original exp, renewed token alone cannot push; resubscribe preserves ID/cursor and permits next delivery; unsubscribe stops callback. Repeat with unbind/disable while delivery queued and reused MCP session: no outbound attempt or unauthorized tool action. `TestExternalDotProfileContinuity`: `integ-grok` stable nonempty profile ID survives refresh/reconnect/rename for same peer, differs for another peer, foreign-peer `as` rejected; preserve its documented rebind ID semantics.

- [ ] Write failing end-to-end tests using fakes and production stack seams; run `CGO_ENABLED=0 just bb go test -p 2 ./internal/httpapi/ -run 'TestExternalDot'`.
- [ ] Amend PROTOCOL §4.1/4.4/4.5/13.4/20.1 with mode selection, exact claims, operator endpoints/CLI, replacement/cutoff, lease schema/revocation delay/errors, Origin/profile dependencies and pending live acceptance. Keep existing protocol revisions and gap 7 behavior documented accurately.
- [ ] Update dots.md with external vs local account distinction, exact bind/unbind commands, DCR/PKCE and `openid profile offline_access vecnet`, explicit resource, no hardcoded client, one shared subject/peer limitation, re-subscription requirement, failure/recovery matrix. Explain `peer link` belongs to built-in mode; oauth.xk consent cannot consume that code.
- [ ] Update DEPLOY with exact new flags/env, unchanged public URL, migration 0020 backup/upgrade, fail-closed startup and JWKS outage behavior, upstream revocation bound, restart/mode rollback and pending ops step below. Returning to built-in mode does not recreate retired grants; use its normal fresh link flow where needed. Do not suggest rotating a working peer as cleanup.
- [ ] Document the later Max/ops acceptance run: authorized fresh peer/config/deploy; verify both metadata paths and absent/allowed/denied Origins using the exact flag/env published by integ-grok (do not invent a second Origin setting); record requested/negotiated protocol, Origin presence and UI prompts; exact subject/profile peer; challenge→callback 2xx→same-thread tool reply→processed; renewal beyond original exp; unsubscribe; then local unbind/disable stop checks. All evidence redacted. Until that run, label real ChatGPT installation/push **pending**, even with fake integration green.
- [ ] Run full Go suite/vet via build box, existing UI/ISA/dependency/Windows checks and protocol/MCP conformance from workflow args. Check diff contains no `connectors/**` or real config. Commit docs/gate receipt with actual commands/outcomes, not claimed future passes.

---

## Ops step (Max/ops applies)

Not performed by implementation workers. Merge this exact member into `resources` in active mounted `AUTH_CONFIG` JSON:

```json
"https://vecnet.xk.io/mcp": {
  "scopes": ["vecnet"],
  "allowedAccounts": ["max"],
  "accessTokenTTL": 3600
}
```

Preserve issuer, other resources, `defaultResource=https://gb2cxc.xpp.exchange/mcp`, accounts, keys, clients and registration policy. ChatGPT must send `resource=https://vecnet.xk.io/mcp`; do not repurpose the webhook2mcp admin client or add CIMD advertising. No oauth-xk code/schema/key/password change is needed. The report records host `/mnt/Main/webhook2mcp/config/auth-oauth-xk.json` mounted at `/config/auth.json`; ops verifies the authoritative mount, merges/validates with normal staging procedure, then (only after validation):

```sh
cd /mnt/Main/webhook2mcp
sudo docker compose up -d --force-recreate --no-deps auth
```

Check discovery advertises `vecnet`, existing gb2cxc still works, and the new browser grant has the exact vecnet audience. Metadata alone cannot prove the private allowlist or end-to-end dot acceptance. Deploy vecnet only through the separately authorized release process, with the two explicit external settings and unchanged public URL; create/prebind a fresh dot using Task 4 commands before Max connects.

## Follow-ups and questions for Max

- Browser link-code binding needs a dedicated hub web client/callback and validated oauth.xk PKCE login; out of scope. Per-client/connection assignment needs a confirmed signed client claim and an explicit multi-dot model; do not guess it from DCR.
- Gap 7 subscription-ID/expiry/challenge-cache/retry parity and built-in refresh robustness remain separate work. Gaps 1/2 are `integ-grok` dependencies; hub identity/notices are `hub-notices`, migration 0019.
- Confirm the upstream revocation window is acceptable for the pilot. Recommendation: accept at most the remaining one-hour access-token life with immediate local unbind/disable; require provider-supported checking before shipping if that bound is unacceptable.
- Confirm one `(https://oauth.xk.io, max)` binding shared by all Max connections is intended for this first slice. Recommendation: use one fresh pilot dot; defer multiple dot assignment until a separate connection identity design.

--- SUMMARY ---
7 tasks. Gaps 3–6 and external-mode gap 8; migration 0020; no connector code changes. External OAuth is opt-in, selects only oauth.xk.io, retains existing bearer auth and preserves built-in behavior off. Operator prebind/unbind, strict JWT/JWKS checks, generation-bound typed push leases and honest revocation limits precede external metadata. Implementation and real ChatGPT acceptance remain pending; ops policy/deployment are separately applied by Max/ops.
