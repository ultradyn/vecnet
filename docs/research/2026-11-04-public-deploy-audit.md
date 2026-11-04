# Public Fly deploy: privacy and abuse audit

Date: 2026-11-04. Branch `research/deploy-audit` from master `9b62751`. Scope: `fly.toml`, `Dockerfile`, `docs/DEPLOY.md`, `internal/httpapi`, `internal/oauth`, `internal/auth`, `internal/mcpserver`, `internal/egress`, `internal/ingress`, `internal/ui`, `internal/store`, `cmd/vecnet-hub`. I checked the code, not the docs.

Requirement (Max): *"it should be private to me even when public. Random stuff can't join it; things need to be invited."*

## Verdict

**Privacy: met.** Every way into the network needs a secret that the operator issued. I found no anonymous route that creates a peer, principal, thread or message, or reads hub data. Bearer, enrol, OAuth code, refresh, access and ingress secrets are all 256-bit random values, stored as hashes or sealed, and compared by hash lookup or `hmac.Equal`. The only short secret is the 8-character dot link code (S1).

**Abuse resistance: not hardened.** No unauthenticated path has a rate limit. Two public endpoints take the SQLite write lock for junk input. The store has 4 pooled connections, and Fly's default concurrency cap is about 25 connections. An anonymous client can make the hub unavailable cheaply. That is an availability problem, not a privacy one.

**No blockers.** Fix S3 before the first `just fly-backup`, and S1 before issuing link codes on the public hub. S2 and S4 should follow soon after.

## 1. Every route served without an authenticated caller

`authenticate` (`internal/httpapi/middleware.go:95`) skips the bearer check only for paths that `isPublic` (`middleware.go:55`) accepts. Every other path needs a bearer that resolves (401 otherwise), and `/v1/admin/*` also needs the operator kind (403). The anonymous paths:

| Route | What an anonymous client can do | Creates | Reads | Verdict |
|---|---|---|---|---|
| `GET /healthz` | Get `{"ok":true}` (`server.go:68`) | nothing | nothing | Safe |
| `GET /ui`, `GET /ui/*` | Load the embedded static operator UI (flat files only; no dirs, no dotfiles, no listing; `internal/ui/ui.go`) | nothing | the UI's JS/CSS only | Safe. CSP is strict. Reveals that the host runs vecnet |
| `GET /.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource[/mcp]` | Get static RFC 8414 / 9728 metadata (`oauth/server.go:112-138`) | nothing | issuer URL | Safe |
| `POST /oauth/register` (DCR) | Register a public client. Redirect URIs must match the ChatGPT patterns (`oauth/clients.go:62`). Body capped at 16 KiB | an `oauth_clients` row (at most 10,000; idle rows evicted after 1 h at the cap) | nothing | Safe for privacy: a client grants no access. Minor M1 (table fill) |
| `GET /oauth/authorize` | Get a consent form holding a sealed request. With a `https://` client_id, the hub does a CIMD fetch through the egress guard (`clients.go:335`) | a cached CIMD client row | client name only | Safe. Minor M2 (blind fetch) |
| `GET /oauth/consent.css` | Get static CSS | nothing | nothing | Safe |
| `POST /oauth/authorize` | Deny. Or approve, which needs either a valid sealed, HttpOnly, SameSite=Lax consent cookie or a live link code (`consent.go:293`) | a principal binding plus a code, **only with a link code or cookie** | nothing | **S1**: link codes have no attempt limit |
| `POST /oauth/token` | Exchange a 256-bit code plus a PKCE verifier, or rotate a 256-bit refresh token (`tokens.go`) | tokens, only with valid input | nothing | Safe for access. **S2**: junk takes the write lock |
| `POST /v1/enrol` | Exchange a one-time, 24 h, 256-bit enrol token for a peer secret (`auth.go:181`) | a credential, only with a valid token | nothing | Safe for access. **S2**: junk takes the write lock |
| `POST /v1/ingress/{src_id}` | Unknown source: 404 from a read only. Known source: GitHub HMAC-SHA256 checked with `hmac.Equal` against a 32-byte per-source secret before the body is parsed (`ingress.go:24`, `ingress/github.go:45`). Disabled source: 403. Body capped at 256 KiB | a message, **only with a valid signature** (redeliveries are deduped) | nothing | Safe |
| `GET /v1/blobs/{sha}?t=` | Read a blob with an HMAC token bound to one recipient and one sha, valid 7 days. The token dies when its recipient is disabled (`hub/blobs.go:84`). Gives 403 whether or not the blob exists, so there is no existence oracle. Served with `CSP: sandbox` and `nosniff` | nothing | that blob, with a valid token | Safe. Minor M8 (7-day bearer URL) |
| `/mcp` (all methods) | Origin check (403), then method check (405), then authentication before any parsing: 401 plus `WWW-Authenticate: Bearer resource_metadata=…` (`mcpserver/server.go:227`) | nothing | nothing | Safe |
| anything else, e.g. `/`, unknown paths | 401 (no bearer) or 404 envelope | nothing | nothing | Safe |

There are no separate link-code pages. Codes are typed into the `/oauth/authorize` consent form, and only the operator can mint them (`POST /v1/admin/dot-links`).

**Path confusion.** `isPublic` reads the decoded `r.URL.Path`. Go's `ServeMux` matches escaped segments and redirects dotted paths to their cleaned form, which is then authenticated again. `%2f` cannot turn a public prefix into an admin route. `CONNECT` paths are not cleaned, but no method-qualified handler matches `CONNECT`. `TestAuditPublicPathConfusion` (`internal/httpapi/deploy_audit_test.go`) pins this, and it **passes**.

**Body limits.** `limitBody` (`middleware.go:33`) caps every body at 256 KiB, except authenticated `PUT /v1/blobs` (25 MiB store cap; the bearer is checked before the body is read). DCR has its own 16 KiB cap.

## 2. Joining paths: each needs something the operator issued

| Path | Operator-issued secret | Strength | Verdict |
|---|---|---|---|
| Session/agent peer | Enrol token from `POST /v1/admin/peers` | `vn_enrol_` + 32 random bytes, SHA-256 at rest, one-time (`UPDATE … AND used_at IS NULL`), 24 h TTL, refused for disabled peers | OK |
| Dot (ChatGPT) | Link code from `POST /v1/admin/dot-links` | 8 characters from a 31-symbol alphabet = 31^8 ≈ 8.5×10^11 ≈ **2^39.6**, 10 min TTL, one-time, hashed. **No attempt limit, no lockout, failures not audited** | **S1** |
| Returning dot login | Consent cookie | AES-256-GCM sealed under `meta.hub_key`, with a purpose tag, 30 d. Rechecked against a live binding each time | OK |
| OAuth client registration (DCR/CIMD) | none, by design | Registering a client grants nothing: access needs a link code. Redirects are pinned to chatgpt.com | OK (M1, M2) |
| CIMD fetch | n/a | `egress.Policy`: https only, port 443 only, DNS pinned, socket-level `Control` recheck, no proxy, no redirects, 16 KiB cap, 30 s timeout, 8 concurrent. Blocks RFC 1918, loopback, link-local (169.254 metadata), CGNAT, ULA `fc00::/7` (covers Fly 6PN `fdaa::/16`), NAT64/6to4, and `.internal`/`.local`/`localhost` names (covers `_api.internal`) | OK |
| Ingress source | Per-source secret from `POST /v1/admin/sources` | 32 random bytes, sealed at rest, HMAC verified in constant time | OK |
| Issue 28 provisioning (`POST /v1/peers`) | Principal credential (operator-created) | Needs `Kind == "principal"`. Kinds allowlist, `max_peers` and `creates_per_hour` come from `principal_limits` (operator-set, defaults empty). OAuth dot principals get no limits row, so they provision nothing. No MCP tool provisions | OK |
| Operator | `vecnet-hub init` over `fly ssh` | `EnsureOperator` is called only from `init` (and the demo seeder, which is not in the image). No HTTP route creates an operator, so the gap between `fly-deploy` and `fly-init` is safe | OK |

## 3. Brute force and abuse

| Surface | Limit today |
|---|---|
| Bearer auth failures | None. Unneeded for guessing: 256-bit secrets cannot be brute-forced. But every junk bearer costs a DB query (S2) |
| Link-code attempts | **None** (S1) |
| Enrol attempts | None. Each junk attempt opens `BEGIN IMMEDIATE` (S2) |
| `/oauth/token` | None. Each junk attempt opens `BEGIN IMMEDIATE` (S2) |
| DCR registrations | Durable bound of 10,000 rows (`clients.go:31`), each ≤16 KiB. No per-client rate. An attacker can keep the table full (M1) |
| CIMD fetches | 8 concurrent, 1 min negative cache per URL, 1,024 remembered failures. Rotating URLs defeats the cache (M2) |
| Per-peer sends | `sends_per_min` / `threads_per_hour` (authenticated only) |
| Web UI login | The UI has no login endpoint. It stores the secret and sends it as a bearer, so the bearer row above applies |

Nothing reads `Fly-Client-IP` or `X-Forwarded-For`. The hub cannot tell clients apart.

## 4. Operator credential

- **Generation:** not on first boot. Max runs `just fly-init` (`fly ssh console -C "/vecnet-hub init …"`) after the first deploy. `NewSecret("operator")` makes `vn_operator_` + 32 bytes from `crypto/rand`.
- **Storage:** only its SHA-256 is kept, in `hub.db` on the `vecnet_data` volume. It is not a Fly secret and not in any env var.
- **Retrieval:** printed once to Max's terminal over the `fly ssh` session. It never goes to the app's stdout, so it is not in `fly logs`. A second `init` prints only the peer id. Max keeps it in `~/.config/vecnet/config.toml`, which the CLI writes as 0600.
- **Recovery:** nothing on the hub rotates the operator credential (M7). Losing it means editing the DB by hand over `fly ssh`.
- **Web UI:** keeps the secret in `localStorage` (`internal/ui/static/app.js:223`). XSS would therefore expose it, but the CSP (`script-src 'self'`, no inline code, `frame-ancestors 'none'`) and sandboxed blob responses make that hard. There is no login rate limit, and none is needed for a 256-bit secret.

## 5. Deploy config

- **`--allow-private-egress`: off.** `fly.toml` does not set `VECNET_ALLOW_PRIVATE_EGRESS`, and the Dockerfile entrypoint does not pass the flag. But the flag reads the environment (`cmd/vecnet-hub/main.go:63`), so `fly secrets set VECNET_ALLOW_PRIVATE_EGRESS=1` would turn it on silently (M5).
- **Dev-only flags:** `--dev-ui-dir` is a flag only (no env var) and is not in the entrypoint. `VECNET_SQLITE_SYNCHRONOUS` is unset, so FULL is used. Neither leaks into the Fly config.
- **TLS:** `force_https = true`. The hub speaks plain HTTP on 8080 behind Fly's edge. The CLI still accepts `http://` hub URLs (M9).
- **Headers:**
  - The UI CSP is strict.
  - The consent page sends CSP, `X-Frame-Options: DENY`, `no-store` and `no-referrer`.
  - Blobs are served with `CSP: sandbox; default-src 'none'` and `nosniff`.
  - **No HSTS anywhere** (M3). `*.fly.dev` is covered, because the whole `.dev` TLD is HSTS-preloaded in browsers, but a custom domain would not be.
- **`/healthz`:** `{"ok":true}`, no version, no counts.
- **Logs:** `internal/logging` redacts every minted secret prefix (`vn_*_`, `vat_`, `vrt_`, `vac_`, `whsec_`, `crsr_`, `rt_`). No request or access logging. CIMD URLs are deliberately not logged. Link codes are never logged.
- **Backups (`docs/DEPLOY.md`):** `just fly-backup` uses `sftp get` to copy a live WAL database. The copy can be inconsistent, and it holds `meta.hub_key` (S3, M6).

## Findings

### Blockers

None. No anonymous path in.

### Serious

**S1. Link codes: no attempt limit, no lockout, failures not audited.**
- Evidence:
  - `hub/admin.go:135-139`: 8 characters, about 39.6 bits, 10 min TTL.
  - `oauth/consent.go:293-372`: `linkCodeWorth` → `retryPage` answers every wrong guess with the same 400 and records nothing.
  - One `GET /oauth/authorize` with a freshly registered DCR client gives a sealed request that is good for 15 minutes of guesses.
  - `DotLinkLive` matches **any** live code, so each live code adds to the target.
- Odds: at R guesses/s, each live code falls with probability about 600·R / 8.5×10^11 over its lifetime. At R = 5,000/s (near what one shared CPU can answer, and enough to load the hub) that is about 3.5×10^-6 per code. Small, but unbounded and invisible.
- Impact of a hit: `bindLink` binds the attacker's ChatGPT account to the dot, retires Max's principal (cutting his ChatGPT off), and gives the attacker that dot's reach. By default that is every peer, including the operator (M10).
- Fix:
  1. Keep a global failure budget for link codes (e.g. 20 failures in 10 min). When it runs out, burn every live code and audit `dot_link_bruteforce`, so the operator must reissue.
  2. Add a per-`Fly-Client-IP` limiter on `POST /oauth/authorize`.
  3. Lengthen the code to 10-12 characters (about 50-59 bits). It is typed once.
- Test: `TestAuditLinkCodeGuessesAreLimited` (`internal/oauth/deploy_audit_test.go`). **Fails:** 200 wrong guesses were all answered, and the real code still bound the dot.

**S2. Anonymous requests can starve the store: no throttling, and two endpoints take the write lock for junk.**
- Evidence:
  - `store.go:23`: every `DB.Tx` is `BEGIN IMMEDIATE`.
  - `store.go:48`: `maxOpenConns = 4`, shared by all reads and writes.
  - `auth.go:186`: `Enrol` opens the transaction before it looks up the token.
  - `oauth/tokens.go:103` and `:173`: `exchangeCode` and `refresh` do the same.
  - Any junk bearer on any non-public path costs one `Resolve` query.
  - Nothing reads `Fly-Client-IP`.
  - `postAuthorize` already does this right with a read-only pre-check (`linkCodeWorth`, `consent.go:353`), which shows the intended rule.
- Impact: a flood of junk enrol or token requests queues every legitimate writer on `busy_timeout` (5 s) and exhausts the 4-connection pool, so messages, acks and SSE reads stall hub-wide. Availability only.
- Fix:
  1. Pre-check with a read before `DB.Tx` in `Enrol`, `exchangeCode` and `refresh`, as `DotLinkLive` does.
  2. Add a small per-IP token bucket middleware keyed on `Fly-Client-IP` (trusted only behind Fly) for all `isPublic` paths and for 401 responses.
  3. Consider a separate read pool so writers cannot starve readers.
- Tests: **fail.** With the write lock held, a junk token or enrol request blocks until the client times out at 2 s, instead of getting a quick 400/401.
  - `TestAuditGarbageTokenRequestSkipsWriteLock` (`internal/oauth/deploy_audit_test.go`)
  - `TestAuditGarbageEnrolSkipsWriteLock` (`internal/httpapi/deploy_audit_test.go`)

**S3. `.dockerignore` does not exclude `backups/` or `*.db`.**
- Evidence:
  - `justfile:38-40`: `fly-backup` writes `backups/hub-*.db` at the repo root.
  - `justfile` `fly-deploy`: `fly deploy --remote-only` uploads the repo root as the build context.
  - `Dockerfile:9`: `COPY . .` in the build stage.
  - `.dockerignore` lists `.data/` but neither `backups/` nor `.demo-data/`.
- Impact: once nightly backups are running, each deploy ships the full database to Fly's remote builder and leaves it in a cached builder layer. That database includes `meta.hub_key`, which unseals source secrets, signs blob tokens and forges consent cookies (M6). The final `scratch` image is clean, but the builder cache is not.
- Fix: add `backups/`, `*.db`, `*.db-wal`, `*.db-shm` and `.demo-data/` to `.dockerignore`.
- Test: `TestAuditDockerignoreExcludesDatabases` (`cmd/vecnet-hub/deploy_audit_test.go`). **Fails.**

**S4. No `[http_service.concurrency]` in `fly.toml`.**
- Evidence: `fly.toml` `[http_service]` has no concurrency block. Fly's documented defaults are soft 20 / hard 25 per machine (confirm against current Fly docs or `fly config show`). There is one machine (`min_machines_running = 1`, no autostart).
- Impact: each peer's SSE stream or long-poll, each ChatGPT `/mcp` stream, and each slow anonymous request holds one slot. At about 25 the proxy stops sending the machine new traffic. `cmd/vecnet-hub/main.go:169` sets only `ReadHeaderTimeout`, with no `ReadTimeout`, so 25 slow-body POSTs to public routes can hold the hub off the air (see M4).
- Fix:
  - Add an explicit `[http_service.concurrency]` with `type = "connections"`, `hard_limit` sized for streams (e.g. 500) and `soft_limit` a bit below it.
  - Add `ReadTimeout` for non-stream routes, e.g. via `http.TimeoutHandler` or a per-route `http.ResponseController.SetReadDeadline`.
- Test: `TestAuditFlyConcurrencyConfigured` (`cmd/vecnet-hub/deploy_audit_test.go`). **Fails.**

### Minor

- **M1. DCR table fill.** An anonymous client can keep `oauth_clients` at 10,000 rows by registering about 10,000 per hour. New connectors then get 503 (`clients.go:31-36`, `storeClient`). Existing tokens keep working. Worst case is about 160 MB of rows. Fix: per-IP DCR rate limit; evict DCR rows with no code after 15 min.
- **M2. CIMD is an anonymous blind-GET relay.** Any public `https://…:443/path` is fetched from the hub's IP, 8 at a time, 30 s each. Rotating URLs defeats the negative cache. Fix: per-IP limit on `GET /oauth/authorize`, and/or a CIMD host allowlist (e.g. `chatgpt.com`, `openai.com`), since ChatGPT is the only intended client.
- **M3. No `Strict-Transport-Security`.** Covered for `vecnet.fly.dev` by the `.dev` preload. Add `max-age=31536000` before any custom domain.
- **M4. No server `ReadTimeout`/`WriteTimeout`** (`main.go:168-171`). Slow bodies on public routes hold goroutines and Fly slots (feeds S4).
- **M5. `VECNET_ALLOW_PRIVATE_EGRESS` can be turned on by a Fly secret.** Fix: refuse to start with it when `FLY_APP_NAME` is set, or when the public URL is https and not loopback.
- **M6. Backups.**
  - `sftp get` of a live WAL database is not a consistent copy (DEPLOY.md says so).
  - Files are written with the default umask.
  - Each copy holds `meta.hub_key`, so a leaked backup is a full compromise: it can unseal secrets and forge consent cookies for any bound OAuth subject.
  - Fix: add a `vecnet-hub backup <path>` subcommand doing `VACUUM INTO`, `chmod 600` the result, optionally `age`-encrypt it, then fetch it with `sftp`.
- **M7. No operator-credential recovery.** `init` refuses once the operator exists, and there is no `vecnet-hub rotate-operator`. Add one for use over `fly ssh`. It prints once, like `init`.
- **M8. Blob URLs are 7-day bearer links** (`hub/hub.go:61`). A URL that leaks into an agent transcript or a ChatGPT conversation is readable anonymously until it expires or the recipient is disabled. Consider 24 h.
- **M9. CLI accepts `http://` hubs.** The first request carries the bearer in cleartext before Fly's redirect. Refuse `http://` unless the host is loopback or private.
- **M10. Trust boundary notes.** These are not escalations; they set what "invited" means.
  - Default send scopes are allow-all, so any invited peer, including a dot, can message every peer, the operator included.
  - `GET /v1/peers` shows the directory to every peer.
  - Anyone with access to Max's Fly org can `fly ssh` in and read `hub.db` and `meta.hub_key`, so org 2FA and membership are part of the privacy boundary.
  - The returning-principal consent page shows only "Approve" (with a new-client warning). An attacker who sends Max a consent link for their own connector relies on Max clicking through the warning, and on ChatGPT's state check failing.

## Tests added (worktree `.worktrees/deploy-audit`, run on the build box)

```
just bb go test -count=1 -run TestAudit ./internal/oauth/ ./internal/httpapi/ ./cmd/vecnet-hub/
```

| Test | Finding | Result on master |
|---|---|---|
| `internal/oauth`: `TestAuditLinkCodeGuessesAreLimited` | S1 | FAIL (200 guesses unthrottled; code still binds) |
| `internal/oauth`: `TestAuditGarbageTokenRequestSkipsWriteLock` | S2 | FAIL (blocked 2.0 s on the write lock) |
| `internal/httpapi`: `TestAuditGarbageEnrolSkipsWriteLock` | S2 | FAIL (blocked 2.0 s on the write lock) |
| `internal/httpapi`: `TestAuditPublicPathConfusion` | guard for §1 | PASS |
| `cmd/vecnet-hub`: `TestAuditDockerignoreExcludesDatabases` | S3 | FAIL |
| `cmd/vecnet-hub`: `TestAuditFlyConcurrencyConfigured` | S4 | FAIL |

Run on x-tall, 2026-11-04, total 9.3 s.

--- SUMMARY ---

- **Verdict:** private as required. Every join path needs an operator-issued secret, and no anonymous route creates or reads hub data. Safe to deploy publicly as far as privacy goes. Not hardened against anonymous DoS.
- **Blockers:** none.
- **Serious (each has a failing test):**
  - **S1:** link codes (~40 bits, 10 min) have no attempt limit or lockout. A hit hijacks the dot and cuts Max's ChatGPT off. Fix with a global failure budget that burns live codes, a per-IP limit and longer codes.
  - **S2:** no anonymous throttling. Junk `/v1/enrol` and `/oauth/token` take `BEGIN IMMEDIATE` on a 4-connection pool. Fix with read-only pre-checks and a per-IP limiter on `Fly-Client-IP`.
  - **S3:** `.dockerignore` misses `backups/` and `*.db`, so deploys ship `hub.db` (with `meta.hub_key`) to Fly's builder. A one-line fix.
  - **S4:** no Fly concurrency block (default about 25 connections) and no `ReadTimeout`, so the single machine is easy to saturate.
- **Minor:** DCR table fill; CIMD blind-GET relay; no HSTS (covered by the `.dev` preload); no read/write timeouts; the egress override can be set by a Fly secret; backup consistency and key exposure; no operator-secret recovery; 7-day blob bearer URLs; CLI accepts `http://`; trust-boundary notes (allow-all scopes, Fly org access, consent phishing).
- **Order:** S3 before the first backup. S1 before issuing link codes on the public hub. S2 and S4 next. Minors as convenient.
